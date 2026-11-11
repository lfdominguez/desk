package infra

import (
	"errors"
	"github.com/lfdominguez/desk/internal/testwait"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/lfdominguez/desk/internal/sshc"
	"github.com/lfdominguez/desk/internal/sshc/sshtest"
	"github.com/lfdominguez/desk/internal/sshconfig"
)

// sshW is a test's SSH world: an in-process server running real shells
// here, and an org (Acme) whose servers file reaches it as web-01 (or web1)
// with a key of its own. dir is the org's infra dir (the panes' dir).
type sshW struct {
	srv                    *sshtest.Server
	home, org, dir, file   string
	known, remote, keyPath string
}

// newSSHW starts the world; extra goes first in web-01's block (it wins).
func newSSHW(t *testing.T, o sshtest.Opts, extra string) *sshW {
	t.Helper()
	if o.Real {
		needSh(t) // the server runs this machine's sh
	}
	runtimeA(t)
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("SHELL", "/bin/sh")
	w := &sshW{home: t.TempDir(), org: filepath.Join(t.TempDir(), "Acme"), remote: t.TempDir()}
	w.dir = filepath.Join(w.org, infraDir)
	os.MkdirAll(w.dir, 0o755)
	w.file, w.known, w.keyPath = serversTarget(w.org), filepath.Join(w.home, "known_hosts"), filepath.Join(w.home, "id")
	k := sshtest.WriteKey(t, w.keyPath, "")
	if o.Keys == nil && o.Password == "" && o.Kbd == nil {
		o.Keys = []ssh.PublicKey{k.PublicKey()}
	}
	o.Real, o.Dir = true, w.remote
	w.srv = sshtest.New(t, o)
	w.config(extra)
	oldBase, oldPool := sshBase, sshPool
	sshBase, sshPool = sshconfig.Options{System: "none", Home: w.home, LocalUser: "me"}, &sshc.Pool{}
	t.Cleanup(func() {
		sshBase, sshPool = oldBase, oldPool
		fwdStops.Lock()
		for f, stop := range fwdStops.m {
			stop()
			delete(fwdStops.m, f)
		}
		fwdStops.Unlock()
	})
	return w
}

// freePort is a TCP port nothing listens on (just now).
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// config (re)writes the servers file with extra first in web-01's block.
func (w *sshW) config(extra string) {
	os.MkdirAll(filepath.Dir(w.file), 0o755)
	os.WriteFile(w.file, []byte("Host web-01 web1\n"+extra+"    HostName 127.0.0.1\n    Port "+strconv.Itoa(w.srv.Port)+
		"\n    User me\n    UserKnownHostsFile "+w.known+"\n    StrictHostKeyChecking accept-new\n    IdentityAgent none\n    IdentityFile "+w.keyPath+"\n"), 0o644)
}

// pane opens a terminal on web-01.
func (w *sshW) pane(t *testing.T, prelude paneState) *Term {
	t.Helper()
	term, err := NewTerm("web-01", w.dir, 80, 24, func() {}, prelude)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(term.Close)
	return term
}

// stubPath is a PATH with sh alone: scripts run, their tools are missing
// (the changes tool's output stays the same on any machine).
func stubPath(t *testing.T) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	d := t.TempDir()
	if err := os.Symlink(sh, filepath.Join(d, "sh")); err != nil {
		t.Fatal(err)
	}
	return d
}

// waitS waits up to 10 s for ok.
func waitS(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(testwait.Long); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestSSHPane: a pane is a shell on the server, in a PTY of its size (and
// TERM), with desk's shell integration injected; resizing reaches the PTY;
// the shell's exit status ends it, not as a dropped link.
func TestSSHPane(t *testing.T) {
	needBash44(t) // the server's shell is this machine's bash
	w := newSSHW(t, sshtest.Opts{}, "")
	term := w.pane(t, paneState{})
	waitS(t, "the prompt", term.AtPrompt)
	if !term.Connected() || term.Err() != nil {
		t.Fatalf("connected %v err %v", term.Connected(), term.Err())
	}
	if r := term.Run("echo $TERM $(stty size)", 5*time.Second); r.IsError || !strings.Contains(r.Text, "xterm-256color 24 80") {
		t.Fatalf("run: %+v", r)
	}
	term.Resize(100, 30)
	waitS(t, "the new size", func() bool {
		r := term.Run("stty size", 5*time.Second)
		return strings.Contains(r.Text, "30 100")
	})
	if r := term.Run("false", 5*time.Second); !r.IsError || !strings.HasPrefix(r.Text, "block #3 exit=1") {
		t.Errorf("a failing command: %+v", r)
	}
	term.Run("exit 3", time.Second)
	waitS(t, "the exit", func() bool { _, e := term.State(); return e })
	if term.code != 3 || term.Lost() {
		t.Errorf("exit 3: code %d lost %v", term.code, term.Lost())
	}
}

// TestSSHPaneLost: the server going away is a lost link (the redial's
// cue); a hangup isn't.
func TestSSHPaneLost(t *testing.T) {
	skipOnWindows(t, serverPty)
	w := newSSHW(t, sshtest.Opts{}, "")
	term := w.pane(t, paneState{})
	waitS(t, "the prompt", term.AtPrompt)
	w.srv.Drop()
	waitS(t, "lost", term.Lost)
	if term.Err() != nil {
		t.Errorf("a dropped link isn't a failed connect: %v", term.Err())
	}
	other := w.pane(t, paneState{}) // the pool dials again: the old connection is dead
	waitS(t, "the second prompt", other.AtPrompt)
	other.Close()
	time.Sleep(100 * time.Millisecond)
	if other.Lost() {
		t.Error("a pane closed by desk isn't lost")
	}
}

// TestSSHPaneRestore: a kept state comes back on the server: its text
// above, its directory and exported variables in the new shell.
func TestSSHPaneRestore(t *testing.T) {
	needBash44(t) // the server's shell is this machine's bash
	w := newSSHW(t, sshtest.Opts{}, "")
	sub := filepath.Join(w.remote, "sub dir")
	os.Mkdir(sub, 0o755)
	term := w.pane(t, paneState{Text: "before the drop", Cwd: sub, Env: map[string]string{"DESK_T": "kept value"}})
	waitS(t, "the prompt", term.AtPrompt)
	if r := term.Run(`echo "$PWD|$DESK_T"`, 5*time.Second); !strings.Contains(r.Text, sub+"|kept value") {
		t.Errorf("restored: %+v", r)
	}
	if k := term.Keep(); !strings.Contains(k.Text, "before the drop") {
		t.Errorf("history: %q", k.Text)
	}
}

// TestSSHConnectError: a server that can't be reached fails the pane with
// the error printed and kept, never connected, not lost.
func TestSSHConnectError(t *testing.T) {
	w := newSSHW(t, sshtest.Opts{}, "    Port 1\n")
	term := w.pane(t, paneState{})
	waitS(t, "the failure", func() bool { _, e := term.State(); return e })
	if term.Err() == nil || term.Connected() || term.Lost() || !strings.Contains(term.Unwrapped(), "127.0.0.1") {
		t.Errorf("err %v connected %v lost %v screen %q", term.Err(), term.Connected(), term.Lost(), term.Unwrapped())
	}
	p := &Pane{term: term, waitUntil: time.Now().Add(time.Hour)}
	if p.connState() != "failed" {
		t.Errorf("state %q", p.connState())
	}
}

// TestSSHPasswordPrompt: a password the vault doesn't have is asked in the
// ask sheet, masked, as "ssh <host> · <prompt>"; a cancelled one fails.
func TestSSHPasswordPrompt(t *testing.T) {
	skipOnWindows(t, serverPty)
	w := newSSHW(t, sshtest.Opts{Password: "pw"}, "    PreferredAuthentications password\n")
	qs := lbFakeAsk(t, func(i int, q *question) (string, bool) { return "pw", i == 0 })
	term := w.pane(t, paneState{})
	waitS(t, "the prompt", term.AtPrompt)
	if len(*qs) != 1 || (*qs)[0].title != "ssh web-01 · me@127.0.0.1's password:" || !(*qs)[0].masked {
		t.Fatalf("asked %+v", (*qs)[0])
	}
	sshPool = &sshc.Pool{}
	failed := w.pane(t, paneState{})
	waitS(t, "the failure", func() bool { _, e := failed.State(); return e })
	if failed.Err() == nil || failed.Connected() {
		t.Errorf("cancelled: %v", failed.Err())
	}
}

// TestSSHVaultPassword: the host's #@ password is tried first and asks
// nothing; once rejected, the user is asked, told so; a vault error is
// shown with the question.
func TestSSHVaultPassword(t *testing.T) {
	skipOnWindows(t, serverPty)
	w := newSSHW(t, sshtest.Opts{Password: "s3cret"}, "    PreferredAuthentications password\n    #@ password-raw s3cret\n")
	qs := lbFakeAsk(t, func(int, *question) (string, bool) { return "s3cret", true })
	term := w.pane(t, paneState{})
	waitS(t, "the prompt", term.AtPrompt)
	if len(*qs) != 0 {
		t.Fatalf("asked %d times", len(*qs))
	}
	w.config("    PreferredAuthentications password\n    #@ password-raw wrong\n")
	sshPool = &sshc.Pool{}
	again := w.pane(t, paneState{})
	waitS(t, "the second prompt", again.AtPrompt)
	if len(*qs) != 1 || !strings.HasSuffix((*qs)[0].title, "password:  (the vault password was rejected)") {
		t.Fatalf("asked %+v", *qs)
	}
	o := sshc.Options{}
	sshAsk(&o, "web-01", t.TempDir(), func() {}) // no servers file: no #@ password
	if _, ok := o.Password(); ok {
		t.Error("no password to try")
	}
	w.config("    #@ password nosuch\n") // a vault item, and no vault
	o = sshc.Options{}
	sshAsk(&o, "web-01", w.dir, func() {})
	if _, ok := o.Password(); ok {
		t.Error("a vault that fails gives none")
	}
	o.Prompt("me@x's password: ", false)
	if q := (*qs)[len(*qs)-1]; !strings.Contains(q.title, "password:  (vault: ") {
		t.Errorf("the vault's error with the question: %q", q.title)
	}
}

// TestSSHPassphraseAndYesNo: a key's passphrase is asked masked; a new
// host's yes/no (StrictHostKeyChecking ask) unmasked; each answer reaches
// the connection.
func TestSSHPassphraseAndYesNo(t *testing.T) {
	skipOnWindows(t, serverPty)
	w := newSSHW(t, sshtest.Opts{}, "    StrictHostKeyChecking ask\n")
	k := sshtest.WriteKey(t, w.keyPath, "pp")
	w.srv.With(func(o *sshtest.Opts) { o.Keys = []ssh.PublicKey{k.PublicKey()} })
	qs := lbFakeAsk(t, func(_ int, q *question) (string, bool) {
		if strings.Contains(q.title, "passphrase") {
			return "pp", true
		}
		return "yes", true
	})
	term := w.pane(t, paneState{})
	waitS(t, "the prompt", term.AtPrompt)
	var yes, pass *question
	for _, q := range *qs {
		if strings.Contains(q.title, "(yes/no") {
			yes = q
		}
		if strings.Contains(q.title, "Enter passphrase for key") {
			pass = q
		}
	}
	if yes == nil || yes.masked || pass == nil || !pass.masked || !strings.HasPrefix(pass.title, "ssh web-01 · ") {
		t.Fatalf("asked %+v %+v", yes, pass)
	}
	if b, _ := os.ReadFile(w.known); !strings.Contains(string(b), "127.0.0.1") {
		t.Errorf("the key kept: %s", b)
	}
}

// TestSSHHostKeyChanged: a host whose known key isn't the one it sends
// fails with the typed error; the box shows it; A accepts it (the old line
// dropped, the new key kept) and reconnects.
func TestSSHHostKeyChanged(t *testing.T) {
	skipOnWindows(t, serverPty)
	w := newSSHW(t, sshtest.Opts{}, "")
	old := sshtest.NewSigner(t).PublicKey()
	line := "[127.0.0.1]:" + strconv.Itoa(w.srv.Port) + " " + string(ssh.MarshalAuthorizedKey(old))
	os.WriteFile(w.known, []byte("# mine\n"+line), 0o600)
	term := w.pane(t, paneState{})
	waitS(t, "the refusal", func() bool { _, e := term.State(); return e })
	var hk *sshc.HostKeyChangedError
	if !errors.As(term.Err(), &hk) {
		t.Fatalf("err %v", term.Err())
	}
	tb := &Tab{org: "Acme", name: "web-01", pinned: map[int]bool{}, panes: []*Pane{{id: 1, term: term, name: "web-01", dir: w.dir}}}
	tb.term = term
	gp := (&driver{}).snapshot(&model{tabs: []*Tab{tb}}).Tabs[0].Panes[0]
	if gp.Conn != "failed" || gp.KeyChange == nil || gp.KeyChange.Remote != ssh.FingerprintSHA256(w.srv.Host.PublicKey()) ||
		len(gp.KeyChange.Old) != 1 || gp.KeyChange.Old[0].Print != ssh.FingerprintSHA256(old) || gp.KeyChange.Old[0].Line != 2 {
		t.Fatalf("box %+v", gp.KeyChange)
	}
	r, _ := model{tabs: []*Tab{tb}}.key(keyOf("a"))
	if e := r.(model).errText; e != "" || tb.panes[0].term == term {
		t.Fatalf("a: %q", e)
	}
	t.Cleanup(tb.panes[0].term.Close)
	waitS(t, "the new connection", tb.panes[0].term.AtPrompt)
	b, _ := os.ReadFile(w.known)
	if strings.Contains(string(b), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(old)))) || !strings.HasPrefix(string(b), "# mine\n") {
		t.Errorf("known_hosts:\n%s", b)
	}
	tb.term, tb.panes[0].term = term, term // the refused pane again: its old line is gone now
	if r, _ := r.(model).key(keyOf("a")); !strings.HasPrefix(r.(model).errText, "accepting the new host key: ") {
		t.Errorf("a stale refusal: %q", r.(model).errText)
	}
}

// TestSSHLegacyFix: a server speaking only an old key exchange fails with
// the negotiation error; F writes the algorithms into its Host block and
// the reconnect gets through.
func TestSSHLegacyFix(t *testing.T) {
	skipOnWindows(t, serverPty)
	w := newSSHW(t, sshtest.Opts{Config: func(c *ssh.ServerConfig) { c.KeyExchanges = []string{"diffie-hellman-group1-sha1"} }}, "")
	term := w.pane(t, paneState{})
	waitS(t, "the refusal", func() bool { _, e := term.State(); return e })
	fixes := legacyFixes(term.Err())
	if len(fixes) != 1 || fixes[0].String() != "KexAlgorithms +diffie-hellman-group1-sha1" {
		t.Fatalf("fixes %v (%v)", fixes, term.Err())
	}
	tb := &Tab{org: "Acme", name: "web-01", pinned: map[int]bool{}, panes: []*Pane{{id: 1, term: term, name: "web-01", dir: w.dir}}}
	tb.term = term
	if gp := (&driver{}).snapshot(&model{tabs: []*Tab{tb}}).Tabs[0].Panes[0]; !gp.CanFix || !strings.HasPrefix(gp.Err, "Unable to negotiate") {
		t.Errorf("box %+v", gp)
	}
	r, _ := model{tabs: []*Tab{tb}}.key(keyOf("f"))
	if e := r.(model).errText; e != "" || tb.panes[0].term == term {
		t.Fatalf("f: %q", e)
	}
	t.Cleanup(tb.panes[0].term.Close)
	waitS(t, "the fixed connection", tb.panes[0].term.AtPrompt)
	if b, _ := os.ReadFile(w.file); !strings.Contains(string(b), "    KexAlgorithms +diffie-hellman-group1-sha1\n") {
		t.Errorf("servers file:\n%s", b)
	}
}

// TestLegacyFixesOf: what each negotiation error asks for; ssh-rsa host
// keys bring their user keys along; none for anything else.
func TestLegacyFixesOf(t *testing.T) {
	f := legacyFixes(&sshc.NegotiationError{What: "HostKeyAlgorithms", Theirs: []string{"ssh-rsa", "no-such-alg"}})
	if len(f) != 2 || f[0].String() != "HostKeyAlgorithms +ssh-rsa" || f[1].String() != "PubkeyAcceptedAlgorithms +ssh-rsa" {
		t.Errorf("%v", f)
	}
	if f := legacyFixes(&sshc.NegotiationError{What: "Ciphers", Theirs: []string{"aes128-cbc"}}); len(f) != 1 || f[0].String() != "Ciphers +aes128-cbc" {
		t.Errorf("%v", f)
	}
	if legacyFixes(&sshc.NegotiationError{What: "MACs", Theirs: []string{"no-such-mac"}}) != nil || legacyFixes(errors.New("x")) != nil || legacyFixes(nil) != nil {
		t.Error("nothing to fix")
	}
	if f := legacyFixes(&sshc.NegotiationError{What: "HostKeyAlgorithms", Theirs: []string{"ssh-dss"}}); len(f) != 1 {
		t.Errorf("no RSA, no user keys: %v", f)
	}
	if f := legacyFixes(&sshc.NegotiationError{What: "KexAlgorithms", Theirs: []string{"diffie-hellman-group1-sha1", "ssh-rsa"}}); len(f) != 1 {
		t.Errorf("ssh-rsa matters for host keys only: %v", f)
	}
}

// TestApplyFixesBlock: a host's block runs to the next Host, Match or
// #@ group; other #@ lines, and a value that reads "group", are in it.
func TestApplyFixesBlock(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "ssh_config")
	write(t, cfg, "Host old\n    User group\n#@\n#@ desc in old\n    Ciphers aes128-ctr\n#@ group g\nHost other\n    Ciphers aes256-ctr\n")
	if err := applyFixes(cfg, "old", []legacyFix{{"Ciphers", []string{"aes128-cbc"}}}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(cfg); string(b) != "Host old\n    User group\n#@\n#@ desc in old\n    Ciphers aes128-ctr,aes128-cbc\n#@ group g\nHost other\n    Ciphers aes256-ctr\n" {
		t.Errorf("got:\n%s", b)
	}
}

// TestSSHOpts: the servers file is the config (as ssh -F), keepalives added
// unless it sets its own; the copied command says the same.
func TestSSHOpts(t *testing.T) {
	w := newSSHW(t, sshtest.Opts{}, "")
	o := sshOpts("web-01", w.dir)
	if o.File != w.file || strings.Join(o.Overrides, ",") != "ServerAliveInterval 15,ServerAliveCountMax 3" || o.Home != w.home {
		t.Errorf("opts %+v", o)
	}
	file := w.file
	if exeSuffix != "" {
		file = shQuote(file) // Windows' backslashes: quoted, which PowerShell reads too
	}
	if c := sshCommand("web-01", w.dir); c != "ssh -o ServerAliveInterval=15 -o ServerAliveCountMax=3 -F "+file+" web-01" {
		t.Errorf("command %q", c)
	}
	w.config("    ServerAliveInterval 5\n")
	if o := sshOpts("web-01", w.dir); o.Overrides != nil {
		t.Errorf("its own keepalives: %v", o.Overrides)
	}
	if o := sshOpts("web-01", t.TempDir()); o.File != "" {
		t.Errorf("no servers file: %q", o.File)
	}
	spaced := filepath.Join(t.TempDir(), "Org Two")
	os.MkdirAll(filepath.Join(spaced, ".desk"), 0o755)
	os.WriteFile(serversTarget(spaced), []byte("Host a\n    ServerAliveInterval 1\n"), 0o644)
	if c := sshCommand("a", filepath.Join(spaced, infraDir)); c != "ssh -F "+shQuote(serversTarget(spaced))+" a" {
		t.Errorf("quoted: %q", c)
	}
}

// TestSSHPoolShared: panes and the files panel of one server share one
// connection.
func TestSSHPoolShared(t *testing.T) {
	skipOnWindows(t, serverPty)
	w := newSSHW(t, sshtest.Opts{}, "")
	a, b := w.pane(t, paneState{}), w.pane(t, paneState{})
	waitS(t, "both prompts", func() bool { return a.AtPrompt() && b.AtPrompt() })
	if _, _, err := listRemote(t.Context(), "web-01", w.dir, ""); err != nil {
		t.Fatal(err)
	}
	if n := w.srv.Count(); n != 1 {
		t.Errorf("%d connections", n)
	}
}

// TestSSHProcBeforeConnect: keys typed while connecting go nowhere, a
// resize then is the shell's size, a hangup then ends it unconnected.
func TestSSHProcBeforeConnect(t *testing.T) {
	p := newSSHProc(80, 24)
	if n, err := p.Write([]byte("ls\r")); n != 3 || err != nil {
		t.Errorf("write %d %v", n, err)
	}
	p.Resize(100, 30)
	if p.w != 100 || p.h != 30 {
		t.Errorf("size %dx%d", p.w, p.h)
	}
	p.Hangup()
	if p.ctx.Err() == nil {
		t.Error("hangup cancels the dial")
	}
	close(p.ready)
	if n, err := p.Read(make([]byte, 4)); n != 0 || err == nil {
		t.Errorf("read %d %v", n, err)
	}
	if code, dropped := p.Wait(); code != 255 || dropped {
		t.Errorf("wait %d %v", code, dropped)
	}
}

// TestPortFwdRun: L, R and D forwards on the shared connection carry
// traffic and stop; a bad spec or an unreachable server fails; starting
// one again replaces the old one's stop.
func TestPortFwdRun(t *testing.T) {
	w := newSSHW(t, sshtest.Opts{}, "")
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	ep := strconv.Itoa(echo.Addr().(*net.TCPAddr).Port)
	through := func(addr string) string {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			return err.Error()
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(2 * time.Second))
		c.Write([]byte("ping"))
		b := make([]byte, 4)
		io.ReadFull(c, b)
		return string(b)
	}
	lp, rp := strconv.Itoa(freePort(t)), strconv.Itoa(freePort(t))
	l := portFwd{host: "web-01", dir: w.dir, kind: "L", spec: lp + ":127.0.0.1:" + ep}
	rf := portFwd{host: "web-01", dir: w.dir, kind: "R", spec: rp + ":127.0.0.1:" + ep}
	if err := l.run(false); err != nil {
		t.Fatal(err)
	}
	if err := rf.run(false); err != nil {
		t.Fatal(err)
	}
	if got := through("127.0.0.1:" + lp); got != "ping" {
		t.Errorf("L: %q", got)
	}
	if got := through("127.0.0.1:" + rp); got != "ping" { // the test server listens here for R
		t.Errorf("R: %q", got)
	}
	if err := l.run(false); err == nil {
		t.Error("its port is taken by itself")
	}
	if err := l.run(true); err != nil {
		t.Fatal(err)
	}
	if got := through("127.0.0.1:" + lp); got == "ping" {
		t.Error("stopped, still forwarding")
	}
	if err := l.run(false); err != nil { // the port is free again: a new stop replaces none
		t.Fatal(err)
	}
	fwdStops.Lock()
	old := fwdStops.m[l]
	fwdStops.Unlock()
	old() // its listener gone, as with a dropped connection
	if err := l.run(false); err != nil {
		t.Fatalf("again after the old one went: %v", err)
	}
	if err := (portFwd{host: "web-01", dir: w.dir, kind: "L", spec: "x"}).run(false); err == nil {
		t.Error("a bad spec")
	}
	w.config("    Port 1\n")
	sshPool = &sshc.Pool{}
	if err := (portFwd{host: "web-01", dir: w.dir, kind: "D", spec: "1"}).run(false); err == nil {
		t.Error("an unreachable server")
	}
}

// TestSSHConnectEdges: a session the server won't open fails the pane
// (its connection given back); a pane closed while its password is asked
// ends without a shell.
func TestSSHConnectEdges(t *testing.T) {
	w := newSSHW(t, sshtest.Opts{}, "    SessionType none\n")
	term := w.pane(t, paneState{})
	waitS(t, "the failure", func() bool { _, e := term.State(); return e })
	if term.Err() == nil || !strings.Contains(term.Err().Error(), "SessionType none") {
		t.Errorf("err %v", term.Err())
	}

	w = newSSHW(t, sshtest.Opts{Password: "pw", Config: func(c *ssh.ServerConfig) {
		c.BannerCallback = func(ssh.ConnMetadata) string { return "Authorized use only\n" }
	}}, "    PreferredAuthentications password\n")
	asked, answer := make(chan struct{}), make(chan struct{})
	lbFakeAsk(t, func(int, *question) (string, bool) { close(asked); <-answer; return "pw", true })
	var notified atomic.Int32
	term, _ = NewTerm("web-01", w.dir, 80, 24, func() { notified.Add(1) }, paneState{})
	<-asked
	if !term.Connected() || notified.Load() == 0 || !strings.Contains(term.Unwrapped(), "Authorized use only") {
		t.Errorf("asking is connected enough (no timeout box), the banner shown: %d %q", notified.Load(), term.Unwrapped())
	}
	term.Close()
	close(answer)
	waitS(t, "the end", func() bool { _, e := term.State(); return e })
	time.Sleep(200 * time.Millisecond)
	if term.Lost() || term.AtPrompt() {
		t.Errorf("closed while connecting: lost %v", term.Lost())
	}

	// a command with no prompt and no output: connected (and told) once
	// the session starts; the pool keeps its connection while it runs
	skipOnWindows(t, serverPty)
	w = newSSHW(t, sshtest.Opts{}, "    RemoteCommand sleep 30\n")
	sshPool = &sshc.Pool{Linger: 20 * time.Millisecond}
	var cur atomic.Pointer[Term]
	var toldUp atomic.Bool
	term, _ = NewTerm("web-01", w.dir, 80, 24, func() {
		if tp := cur.Load(); tp != nil && tp.Connected() {
			toldUp.Store(true)
		}
	}, paneState{})
	cur.Store(term)
	t.Cleanup(term.Close)
	waitS(t, "told it's up", toldUp.Load)
	time.Sleep(200 * time.Millisecond)
	if _, exited := term.State(); exited {
		t.Error("the pooled connection went while the pane used it")
	}
}
