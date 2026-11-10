package infra

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/lfdominguez/desk/internal/sshc"
	"github.com/lfdominguez/desk/internal/sshconfig"
)

// desk speaks SSH itself (internal/sshc, no ssh binary): one connection per
// server shared by its panes, the files panel, the changes tool and its
// forwards, as OpenSSH's ControlMaster with ControlPersist 60 did. Its
// questions (passwords, passphrases, host keys) go to the ask sheet; a
// password from the vault is tried first. Claude never sees them.

// sshPool is the workspace's shared connections.
var sshPool = &sshc.Pool{}

// sshBase is where every connection's config resolution starts; tests point
// Home and System at their own.
var sshBase sshconfig.Options

// sshOpts is host's config as desk connects with it: the org's servers file
// (like ssh -F, its relative paths from its own folder rather than desk's
// working directory) and keepalives that notice a dead link in ~45s, so
// redial can bring it back, unless the config sets ServerAliveInterval itself.
func sshOpts(host, dir string) sshc.Options {
	o := sshc.Options{Options: sshBase}
	if f := sshConfigFor(dir); fileExists(f) {
		o.File, o.RelativeToFile = f, true
	}
	if c, err := sshconfig.Resolve(host, o.Options); err == nil && c.Int("serveraliveinterval") == 0 {
		o.Overrides = append(o.Overrides, "ServerAliveInterval 15", "ServerAliveCountMax 3")
	}
	return o
}

var passwordPrompt = regexp.MustCompile(`(?i)password[^:]*:\s*$`)

// sshAsk sets o's questions for host: the vault's password (the host's
// #@ password) on the first try, then the ask sheet, masked unless the
// answer may echo (yes/no). asked runs before each question.
func sshAsk(o *sshc.Options, host, dir string, asked func()) {
	ref := passwordRef(dir, host)
	var mu sync.Mutex
	vaultErr, used := "", false
	o.Password = func() (string, bool) {
		if ref == nil {
			return "", false
		}
		pw, err := serverPassword(dir, ref)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			vaultErr = err.Error()
			return "", false
		}
		used = true
		return pw, true
	}
	o.Prompt = func(prompt string, echo bool) (string, error) {
		asked()
		prompt = strings.TrimSpace(prompt)
		mu.Lock()
		if passwordPrompt.MatchString(prompt) {
			switch {
			case vaultErr != "":
				prompt += "  (vault: " + vaultErr + ")"
				vaultErr = ""
			case used:
				prompt += "  (the vault password was rejected)"
			}
		}
		mu.Unlock()
		a, ok := askUser("ssh "+host+" · "+prompt, !echo)
		if !ok {
			return "", errors.New("not answered")
		}
		return a, nil
	}
}

// sshClient is host's shared connection (dialed when there is none) and its
// release, for one-off work: files, the changes tool, forwards.
func sshClient(ctx context.Context, host, dir string) (*sshc.Client, func(), error) {
	o := sshOpts(host, dir)
	sshAsk(&o, host, dir, func() {})
	return sshPool.Get(ctx, host, o)
}

// sshProc is a pane's SSH session: connecting until ready closes, then the
// shell. Keys typed before it starts are dropped (no shell to read them).
type sshProc struct {
	ctx    context.Context
	cancel context.CancelFunc
	ready  chan struct{}

	mu      sync.Mutex
	s       *sshc.Session
	out     io.Reader // stdout and stderr
	release func()
	w, h    int
}

func newSSHProc(w, h int) *sshProc {
	ctx, cancel := context.WithCancel(context.Background())
	return &sshProc{ctx: ctx, cancel: cancel, ready: make(chan struct{}), w: w, h: h}
}

func (p *sshProc) Read(b []byte) (int, error) {
	<-p.ready
	if p.out == nil {
		return 0, io.EOF
	}
	return p.out.Read(b)
}

func (p *sshProc) session() *sshc.Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.s
}

func (p *sshProc) Write(b []byte) (int, error) {
	if s := p.session(); s != nil && s.Stdin != nil {
		return s.Stdin.Write(b)
	}
	return len(b), nil
}

func (p *sshProc) Resize(w, h int) {
	p.mu.Lock()
	p.w, p.h = w, h
	s := p.s
	p.mu.Unlock()
	if s != nil {
		s.Resize(w, h)
	}
}

func (p *sshProc) Hangup() {
	p.cancel()
	if s := p.session(); s != nil {
		s.Signal(ssh.SIGHUP)
		s.Close()
	}
}

// Wait: a session that ended without an exit status went with its
// connection (or a signal killed the shell): dropped, as ssh's 255 was.
func (p *sshProc) Wait() (int, bool) {
	<-p.ready
	s := p.session()
	if s == nil {
		return 255, false // never connected
	}
	code, err := s.Wait()
	p.release()
	if err != nil {
		return 255, true
	}
	return code, false
}

// start sets the session up, once connected.
func (p *sshProc) start(s *sshc.Session, release func()) {
	r, w := io.Pipe()
	var wg sync.WaitGroup
	for _, src := range []io.Reader{s.Stdout, s.Stderr} {
		wg.Add(1)
		go func() { io.Copy(w, src); wg.Done() }()
	}
	go func() { wg.Wait(); w.Close() }()
	p.mu.Lock()
	p.s, p.out, p.release = s, r, release
	p.mu.Unlock()
	if p.ctx.Err() != nil { // closed while connecting
		s.Close()
	}
}

// connect dials host over the shared connection and starts the pane's
// shell; what goes wrong is printed in the pane, as ssh would, and kept
// for the connection box (Err).
func (t *Term) connect(p *sshProc, host, dir string) {
	defer close(p.ready)
	o := sshOpts(host, dir)
	sshAsk(&o, host, dir, func() { t.markReached() })
	o.Logf = func(f string, a ...any) { t.print(fmt.Sprintf(f, a...)) }
	o.Step = t.step // heard only when the pool dials for this pane
	c, release, err := sshPool.Get(p.ctx, host, o)
	var s *sshc.Session
	if err == nil {
		if t.dialed() {
			t.step("Opening the shell", false)
		}
		p.mu.Lock()
		w, h := p.w, p.h
		p.mu.Unlock()
		if s, err = c.Shell("xterm-256color", w, h, nil); err != nil {
			release()
		}
	}
	t.endSteps(err)
	if err != nil {
		t.mu.Lock()
		t.err, t.reached = err, false // a question answered, then refused: failed, not connected
		t.mu.Unlock()
		t.print(err.Error())
		return
	}
	p.start(s, release)
	t.mu.Lock()
	t.up = true
	t.mu.Unlock()
	if t.notify != nil {
		t.notify()
	}
}

// print writes a line of desk's own (ssh's warnings, banners, errors) into
// the pane.
func (t *Term) print(s string) {
	t.mu.Lock()
	t.emu.Write([]byte(strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\r\n") + "\r\n"))
	t.ver.Add(1)
	t.mu.Unlock()
	if t.notify != nil {
		t.notify()
	}
}
