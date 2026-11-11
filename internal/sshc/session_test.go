package sshc

import (
	"bufio"
	"context"
	"github.com/lfdominguez/desk/internal/sshc/sshtest"
	"github.com/lfdominguez/desk/internal/testwait"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// pwClient is a client of a fresh password server, with extra config lines.
func pwClient(t *testing.T, extra string, o sshtest.Opts) (*Client, *server, *world) {
	w := newWorld(t)
	s := pwServer(t, w, o)
	w.ans = []string{"pw"}
	return mustDial(t, w.opts(base(extra), s)), s, w
}

// expect reads lines from r until one is want.
func expect(t *testing.T, r *bufio.Reader, want string) {
	t.Helper()
	done := make(chan string, 1)
	go func() {
		var seen []string
		for {
			l, err := r.ReadString('\n')
			l = strings.TrimSpace(strings.TrimPrefix(l, "$ "))
			seen = append(seen, l)
			if l == want || err != nil {
				done <- strings.Join(seen, " | ")
				return
			}
		}
	}()
	select {
	case got := <-done:
		if !strings.HasSuffix(got, want) {
			t.Fatalf("want %q, got %q", want, got)
		}
	case <-time.After(testwait.Long):
		t.Fatalf("no %q", want)
	}
}

func TestShellPTY(t *testing.T) {
	c, _, _ := pwClient(t, "", sshtest.Opts{})
	s, err := c.Shell("xterm-256color", 80, 24, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := bufio.NewReader(s.Stdout)
	expect(t, out, "pty xterm-256color 80x24")
	if err := s.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	expect(t, out, "size 100x30")
	io.WriteString(s.Stdin, "echo hello\n")
	expect(t, out, "hello")
	io.WriteString(s.Stdin, "exit 3\n")
	if code, err := s.Wait(); code != 3 || err != nil {
		t.Fatalf("exit %d %v", code, err)
	}
	s.Close()
}

func TestShellSignal(t *testing.T) {
	c, srv, _ := pwClient(t, "  RemoteCommand sleep\n", sshtest.Opts{})
	s, err := c.Shell("xterm", 80, 24, nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := s.Signal(ssh.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code, err := s.Wait(); code != -1 || err == nil {
		t.Fatalf("exit %d %v", code, err)
	}
	if sig := srv.Signal(); sig != "TERM" {
		t.Fatalf("signal %q", sig)
	}
}

func TestRemoteCommandNoPTY(t *testing.T) {
	for extra, want := range map[string]string{
		"  RemoteCommand pty\n":                     "nopty",
		"  RemoteCommand pty\n  RequestTTY yes\n":   "pty xterm 80x24",
		"  RemoteCommand pty\n  RequestTTY force\n": "pty xterm 80x24",
	} {
		c, _, _ := pwClient(t, extra, sshtest.Opts{})
		s, err := c.Shell("xterm", 80, 24, nil)
		if err != nil {
			t.Fatal(err)
		}
		expect(t, bufio.NewReader(s.Stdout), want)
		s.Wait()
	}
}

func TestRequestTTYNo(t *testing.T) {
	c, _, _ := pwClient(t, "  RequestTTY no\n", sshtest.Opts{})
	s, err := c.Shell("xterm", 80, 24, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := bufio.NewReader(s.Stdout)
	io.WriteString(s.Stdin, "pty\n")
	expect(t, out, "nopty")
	s.Stdin.Close()
	if code, err := s.Wait(); code != 0 || err != nil {
		t.Fatalf("exit %d %v", code, err)
	}
}

func TestShellStderr(t *testing.T) {
	c, _, _ := pwClient(t, "  RemoteCommand stderr oops\n", sshtest.Opts{})
	s, err := c.Shell("xterm", 80, 24, nil)
	if err != nil {
		t.Fatal(err)
	}
	expect(t, bufio.NewReader(s.Stderr), "oops")
}

func TestSessionType(t *testing.T) {
	c, _, _ := pwClient(t, "  SessionType none\n", sshtest.Opts{})
	if _, err := c.Shell("xterm", 80, 24, nil); err == nil {
		t.Fatal("session opened")
	}
	c, _, _ = pwClient(t, "  SessionType subsystem\n  RemoteCommand echo\n", sshtest.Opts{})
	s, err := c.Shell("xterm", 80, 24, nil)
	if err != nil {
		t.Fatal(err)
	}
	expect(t, bufio.NewReader(s.Stdout), "subsystem echo")
	c, _, _ = pwClient(t, "  SessionType subsystem\n  RemoteCommand nosuch\n", sshtest.Opts{})
	if _, err := c.Shell("xterm", 80, 24, nil); err == nil {
		t.Fatal("unknown subsystem opened")
	}
}

func TestStdinNull(t *testing.T) {
	c, _, _ := pwClient(t, "  StdinNull yes\n  RemoteCommand cat\n", sshtest.Opts{})
	s, err := c.Shell("xterm", 80, 24, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Stdin != nil {
		t.Fatal("stdin given")
	}
	if code, err := s.Wait(); code != 0 || err != nil {
		t.Fatalf("exit %d %v", code, err)
	}
	out, _, _, err := c.Run(context.Background(), "cat", strings.NewReader("lost"))
	if err != nil || len(out) != 0 {
		t.Fatalf("cat %q %v", out, err)
	}
}

func TestShellOnDeadClient(t *testing.T) {
	c, _, _ := pwClient(t, "", sshtest.Opts{})
	c.Close()
	if _, err := c.Shell("xterm", 80, 24, nil); err == nil {
		t.Fatal("shell on a closed client")
	}
	if _, _, code, err := c.Run(context.Background(), "echo", nil); err == nil || code != -1 {
		t.Fatal("run on a closed client")
	}
}

func TestRun(t *testing.T) {
	c, _, _ := pwClient(t, "", sshtest.Opts{})
	ctx := context.Background()
	out, errOut, code, err := c.Run(ctx, "echo hi", nil)
	if string(out) != "hi\n" || len(errOut) != 0 || code != 0 || err != nil {
		t.Fatalf("echo: %q %q %d %v", out, errOut, code, err)
	}
	out, errOut, code, err = c.Run(ctx, "stderr oops", nil)
	if len(out) != 0 || string(errOut) != "oops\n" || code != 0 || err != nil {
		t.Fatalf("stderr: %q %q %d %v", out, errOut, code, err)
	}
	if _, _, code, err = c.Run(ctx, "exit 7", nil); code != 7 || err != nil {
		t.Fatalf("exit: %d %v", code, err)
	}
	if out, _, _, _ = c.Run(ctx, "cat", strings.NewReader("in\n")); string(out) != "in\n" {
		t.Fatalf("cat: %q", out)
	}
	ctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, _, code, err = c.Run(ctx, "sleep", nil); code != -1 || err != context.DeadlineExceeded {
		t.Fatalf("sleep: %d %v", code, err)
	}
}

func TestEnv(t *testing.T) {
	for _, kv := range os.Environ() { // the machine's own LC_* (macOS sets LC_CTYPE) would go too
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "LC_") {
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}
	t.Setenv("LC_DESK", "fr")
	t.Setenv("LC_OTHER", "de")
	t.Setenv("DESK_X", "local")
	c, _, _ := pwClient(t, "  SendEnv LC_* DESK_X\n  SetEnv DESK_X=set DESK_Y=${LC_DESK}\n", sshtest.Opts{})
	s, err := c.Shell("xterm", 80, 24, map[string]string{"DESK_Z": "given", "DESK_Y": "given"})
	if err != nil {
		t.Fatal(err)
	}
	out := bufio.NewReader(s.Stdout)
	io.WriteString(s.Stdin, "env\n")
	expect(t, out, "DESK_X=set DESK_Y=given DESK_Z=given LC_DESK=fr LC_OTHER=de")
	s.Close()
	got, _, _, _ := c.Run(context.Background(), "env", nil)
	if string(got) != "DESK_X=set DESK_Y=fr LC_DESK=fr LC_OTHER=de\n" {
		t.Fatalf("run env %q", got)
	}
}

func TestEnvRefusedLogged(t *testing.T) {
	c, _, w := pwClient(t, "  SetEnv A=1\n", sshtest.Opts{Config: func(*ssh.ServerConfig) {}})
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	c.setEnv(s, nil) // a closed session refuses
	if !w.logged("Setting A: refused") {
		t.Fatalf("logs %q", w.logs)
	}
}

func TestForwardAgent(t *testing.T) {
	raw, _ := genRaw(t)
	sock, _ := agentSock(t, raw)
	for _, fwd := range []string{"yes", sock} {
		c, srv, _ := pwClient(t, "  ForwardAgent "+fwd+"\n  IdentityAgent "+sock+"\n", sshtest.Opts{})
		for range 2 { // the agent is offered on every session
			out, _, _, err := c.Run(context.Background(), "agent", nil)
			if err != nil || string(out) != "agent keys 1 <nil>\n" {
				t.Fatalf("%s: %q %v", fwd, out, err)
			}
		}
		if !srv.Forwarded() {
			t.Fatal("no agent request")
		}
	}
	c, srv, _ := pwClient(t, "  ForwardAgent no\n", sshtest.Opts{})
	c.Run(context.Background(), "echo", nil)
	if srv.Forwarded() {
		t.Fatal("agent forwarded")
	}
	c, _, w := pwClient(t, "  ForwardAgent %HOME/none\n", sshtest.Opts{})
	c.Run(context.Background(), "echo", nil)
	if !w.logged("Agent forwarding:") {
		t.Fatalf("logs %q", w.logs)
	}
}

func TestForwardAgentUnsafe(t *testing.T) {
	w, s, _ := changedWorld(t)
	k := writeKey(t, w.path("id"), "")
	s.With(func(o *sshtest.Opts) { o.Keys = []ssh.PublicKey{k.PublicKey()} })
	c := mustDial(t, w.opts(base("  StrictHostKeyChecking no\n  ForwardAgent yes\n"), s))
	c.Run(context.Background(), "echo", nil)
	if s.Forwarded() || !w.logged("Agent forwarding is disabled") {
		t.Fatalf("agent forwarded, logs %q", w.logs)
	}
}

func TestLocalCommand(t *testing.T) {
	if onWindows {
		t.Skip("POSIX shell syntax; Windows runs LocalCommand with cmd /C")
	}
	w := newWorld(t)
	s := pwServer(t, w, sshtest.Opts{})
	w.ans = []string{"pw"}
	t.Setenv("DESK_LC", "env")
	mustDial(t, w.opts(base("  PermitLocalCommand yes\n  LocalCommand echo %n %T '${DESK_LC}' > %HOME/lc; echo done\n"), s))
	if got := read(t, w.path("lc")); got != "srv NONE env\n" || !w.logged("done") {
		t.Fatalf("lc %q, logs %q", got, w.logs)
	}
	os.Remove(w.path("lc"))
	w.ans = []string{"pw"}
	mustDial(t, w.opts(base("  LocalCommand echo %n > %HOME/lc\n"), s))
	if _, err := os.Stat(w.path("lc")); err == nil {
		t.Fatal("ran without PermitLocalCommand")
	}
	w.ans = []string{"pw"}
	mustDial(t, w.opts(base("  PermitLocalCommand yes\n  LocalCommand exit 2\n"), s))
	if !w.logged("LocalCommand \"exit 2\"") {
		t.Fatalf("logs %q", w.logs)
	}
	w.ans = []string{"pw"}
	if _, err := dialT(t, w.opts(base("  PermitLocalCommand yes\n  LocalCommand echo %Z\n"), s)); err == nil {
		t.Fatal("bad token ran")
	}
}

func TestSFTP(t *testing.T) {
	dir := t.TempDir()
	c, _, _ := pwClient(t, "", sshtest.Opts{Dir: dir})
	sc, err := c.SFTP()
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	f, err := sc.Create("hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("hi"))
	f.Close()
	if got := read(t, dir+"/hello.txt"); got != "hi" {
		t.Fatalf("file %q", got)
	}
}
