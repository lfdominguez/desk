package sshc

import (
	"context"
	"errors"
	"github.com/lfdominguez/desk/internal/sshc/sshtest"
	"github.com/lfdominguez/desk/internal/testwait"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lfdominguez/desk/internal/sshconfig"
)

// common is every host's part of a multi-host config.
const common = "Host *\n  HostName 127.0.0.1\n  User me\n  UserKnownHostsFile %HOME/known_hosts\n  StrictHostKeyChecking accept-new\n  IdentityAgent none\n"

func TestProxyJumpOneHop(t *testing.T) {
	w := newWorld(t)
	jump, target := pwServer(t, w, sshtest.Opts{}), pwServer(t, w, sshtest.Opts{})
	w.ans = []string{"pw", "pw"}
	c, err := Dial(context.Background(), "target", w.opts("Host target\n  Port %PORT1\n  ProxyJump jump\nHost jump\n  Port %PORT0\n"+common, jump, target))
	if err != nil {
		t.Fatal(err)
	}
	if c.jump == nil || jump.Count() != 1 || target.Count() != 1 || !strings.Contains(w.asks[0], "@127.0.0.1's password") {
		t.Fatalf("jump %v, conns %d %d", c.jump, jump.Count(), target.Count())
	}
	// Through a jump there is no address to check.
	if c.hc.shown != "<no hostip for proxy command>" {
		t.Fatalf("shown %q", c.hc.shown)
	}
	c.Close()
	select {
	case <-c.jump.Done():
	case <-time.After(testwait.Long):
		t.Fatal("jump host left open")
	}
}

func TestProxyJumpTwoHops(t *testing.T) {
	w := newWorld(t)
	j1, j2, target := pwServer(t, w, sshtest.Opts{}), pwServer(t, w, sshtest.Opts{}), pwServer(t, w, sshtest.Opts{})
	w.ans = []string{"pw", "pw", "pw"}
	// The second hop is named with its user and port, the first by its block.
	c, err := Dial(context.Background(), "target", w.opts("Host target\n  Port %PORT2\n  ProxyJump j1,me@127.0.0.1:%PORT1\nHost j1\n  Port %PORT0\n"+common, j1, j2, target))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if j1.Count() != 1 || j2.Count() != 1 || target.Count() != 1 || c.jump.jump == nil {
		t.Fatalf("conns %d %d %d", j1.Count(), j2.Count(), target.Count())
	}
}

func TestProxyJumpFails(t *testing.T) {
	w := newWorld(t)
	jump := pwServer(t, w, sshtest.Opts{})
	w.ans = []string{"pw"}
	_, err := Dial(context.Background(), "target", w.opts("Host target\n  Port 1\n  ProxyJump jump\nHost jump\n  Port %PORT0\n"+common, jump))
	if err == nil || !strings.Contains(err.Error(), "jump host 127.0.0.1: channel to 127.0.0.1:1") {
		t.Fatalf("err %v", err)
	}
	_, err = Dial(context.Background(), "target", w.opts("Host target\n  ProxyJump jump\nHost jump\n  Port 1\n"+common))
	if err == nil || !strings.Contains(err.Error(), "connect to host 127.0.0.1:1") {
		t.Fatalf("err %v", err)
	}
}

func TestHopString(t *testing.T) {
	for want, h := range map[string]sshconfig.Hop{
		"box.example.com":         {Host: "box.example.com"},
		"me@box.example.com:2222": {User: "me", Host: "box.example.com", Port: 2222},
		"[2001:db8::1]:22":        {Host: "2001:db8::1", Port: 22},
		"me@192.0.2.1":            {User: "me", Host: "192.0.2.1"},
	} {
		if got := hopString(h); got != want {
			t.Errorf("%v: %s", h, got)
		}
	}
}

func TestProxyCommand(t *testing.T) {
	w := newWorld(t)
	s := pwServer(t, w, sshtest.Opts{})
	bin, _ := os.Executable()
	w.ans = []string{"pw"}
	c := mustDial(t, w.opts(base("  ProxyCommand "+bin+" sshc-bridge %h %p\n"), s))
	if _, _, code, err := c.Run(context.Background(), "echo hi", nil); err != nil || code != 0 {
		t.Fatalf("run: %d %v", code, err)
	}
	time.Sleep(50 * time.Millisecond)
	if !w.logged("bridging") || c.hc.ip != "" {
		t.Fatalf("logs %q", w.logs)
	}
	c.Close()
}

func TestProxyCommandFails(t *testing.T) {
	w := newWorld(t)
	s := pwServer(t, w, sshtest.Opts{})
	if _, err := dialT(t, w.opts(base("  ProxyCommand exit 1\n"), s)); err == nil {
		t.Fatal("dead command connected")
	}
	if _, err := dialT(t, w.opts(base("  ProxyCommand x %Z\n"), s)); err == nil || !strings.Contains(err.Error(), "ProxyCommand x %Z") {
		t.Fatalf("err %v", err)
	}
	if onWindows {
		return // cmd /C, whatever SHELL says
	}
	t.Setenv("SHELL", w.path("noshell"))
	if _, err := dialT(t, w.opts(base("  ProxyCommand true\n"), s)); err == nil || !strings.Contains(err.Error(), "ProxyCommand true") {
		t.Fatalf("err %v", err)
	}
}

func TestShellCommandDefaultShell(t *testing.T) {
	t.Setenv("SHELL", "")
	if c := shellCommand("echo", true); onWindows {
		if c.Args[0] != "cmd" || c.Args[1] != "/C" || c.Args[2] != "echo" {
			t.Fatalf("%v", c.Args)
		}
		return
	}
	c := shellCommand("echo", true)
	if c.Path != "/bin/sh" || c.Args[2] != "exec echo" {
		t.Fatalf("%v", c.Args)
	}
	if c := shellCommand("echo", false); c.Args[2] != "echo" {
		t.Fatalf("%v", c.Args)
	}
}

func TestUnsupportedTransports(t *testing.T) {
	w := newWorld(t)
	s := pwServer(t, w, sshtest.Opts{})
	for _, extra := range []string{"  ProxyCommand nc %h %p\n  ProxyUseFdpass yes\n", "  ProxyCommand -\n"} {
		if _, err := dialT(t, w.opts(base(extra), s)); err == nil || !strings.Contains(err.Error(), "isn't supported by desk") {
			t.Fatalf("%q: err %v", extra, err)
		}
	}
}

func TestIgnoredOptionsLogged(t *testing.T) {
	w := newWorld(t)
	s := pwServer(t, w, sshtest.Opts{})
	w.ans = []string{"pw"}
	mustDial(t, w.opts(base("  ForwardX11 yes\n  Compression yes\n  Tunnel yes\n  VerifyHostKeyDNS yes\n  UseRoaming no\n"), s))
	for _, l := range []string{"X11 forwarding isn't supported", "Compression isn't", "Tunnel isn't", "VerifyHostKeyDNS isn't", "useroaming"} {
		if !w.logged(l) {
			t.Errorf("not logged: %s (%q)", l, w.logs)
		}
	}
	w.logs = nil
	w.ans = []string{"pw"}
	mustDial(t, w.opts(base("  Tunnel no\n"), s))
	if w.logged("isn't supported") {
		t.Fatalf("logs %q", w.logs)
	}
}

func TestConnectionAttempts(t *testing.T) {
	retryPause = 0
	t.Cleanup(func() { retryPause = time.Second })
	w := newWorld(t)
	_, err := dialT(t, w.opts("Host srv\n  HostName 127.0.0.1\n  Port 1\n  ConnectionAttempts 3\n"))
	if err == nil || !strings.Contains(err.Error(), "ssh: connect to host 127.0.0.1:1") {
		t.Fatalf("err %v", err)
	}
	retryPause = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// One attempt: no pause before it.
	if _, err := Dial(ctx, "srv", w.opts("Host srv\n  HostName 127.0.0.1\n  Port 1\n")); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	_, err = Dial(ctx, "srv", w.opts("Host srv\n  HostName 127.0.0.1\n  Port 1\n  ConnectionAttempts 2\n"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
}

func TestDialOptions(t *testing.T) {
	needLo(t)
	w := newWorld(t)
	s := pwServer(t, w, sshtest.Opts{})
	for _, extra := range []string{"  BindAddress 127.0.0.1\n", "  BindAddress localhost\n  AddressFamily inet\n", "  BindInterface " + lo + "\n  AddressFamily inet\n", "  TCPKeepAlive no\n", "  BindInterface " + lo + "\n"} {
		w.ans = []string{"pw"}
		if _, err := dialT(t, w.opts(base(extra), s)); err != nil {
			t.Fatalf("%q: %v", extra, err)
		}
	}
	for _, tc := range []struct{ extra, want string }{
		{"  AddressFamily inet6\n", "connect to host"},
		{"  BindAddress nowhere.invalid\n", "BindAddress nowhere.invalid"},
		{"  BindInterface nosuch0\n", "BindInterface nosuch0"},
		{"  BindInterface " + lo + "\n  AddressFamily inet6\n  BindAddress ::1\n", "connect to host"},
	} {
		if _, err := dialT(t, w.opts(base(tc.extra), s)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%q: err %v", tc.extra, err)
		}
	}
}

func TestDialer(t *testing.T) {
	w := newWorld(t)
	d, network, err := dialer(resolve(t, w, "Host *\n  ConnectTimeout 7\n  TCPKeepAlive no\n  AddressFamily inet6\n"))
	if err != nil || d.Timeout != 7*time.Second || d.KeepAlive != -1 || d.LocalAddr != nil || network != "tcp6" {
		t.Fatalf("%+v %s %v", d, network, err)
	}
	d, network, _ = dialer(resolve(t, w, "Host *\n  BindAddress 127.0.0.1\n"))
	if d.KeepAlive != 0 || d.LocalAddr.String() != "127.0.0.1:0" || network != "tcp" {
		t.Fatalf("%+v %s", d, network)
	}
}

func TestProxyCommandNoEnv(t *testing.T) {
	// ProxyCommand takes % tokens only: ${…} is the shell's.
	w := newWorld(t)
	s := pwServer(t, w, sshtest.Opts{})
	bin, _ := os.Executable()
	t.Setenv("DESK_PORT", strconv.Itoa(s.Port))
	w.ans = []string{"pw"}
	if _, err := dialT(t, w.opts(base("  ProxyCommand "+bin+" sshc-bridge %h '${DESK_PORT}'\n"), s)); err == nil {
		t.Fatal("${} expanded")
	}
}

// lo is the loopback interface's name (lo; lo0 on macOS and the BSDs;
// "Loopback Pseudo-Interface 1" on Windows), quoted for a config; "" when
// there's none (Wine lists only real adapters).
var lo = func() string {
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if a, _ := i.Addrs(); i.Flags&net.FlagLoopback != 0 && len(a) > 0 {
			return `"` + i.Name + `"`
		}
	}
	return ""
}()

// needLo skips t without a loopback interface to bind to.
func needLo(t *testing.T) {
	if lo == "" {
		t.Skip("no loopback interface listed")
	}
}

func TestBindInterfaceFamily(t *testing.T) {
	needLo(t)
	w := newWorld(t)
	cfg := resolve(t, w, "Host *\n  BindInterface "+lo+"\n")
	ip, err := bindAddr(cfg, "tcp4")
	if err != nil || ip.To4() == nil {
		t.Fatalf("tcp4: %v %v", ip, err)
	}
	if ip, err := bindAddr(cfg, "tcp6"); err == nil && ip.To4() != nil {
		t.Fatalf("tcp6: %v", ip)
	}
	if ip, _ := bindAddr(resolve(t, w, "Host *\n"), "tcp"); ip != nil {
		t.Fatalf("none: %v", ip)
	}
}

func TestConnectTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // silent: no banner
		}
	}()
	w := newWorld(t)
	// The silent server would hold the dial forever: ConnectTimeout's
	// deadline ends it, not the test's own context.
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Long)
	defer cancel()
	c, err := Dial(ctx, "srv", w.opts("Host srv\n  HostName 127.0.0.1\n  Port "+strings.Split(ln.Addr().String(), ":")[1]+"\n  ConnectTimeout 1\n"))
	if c != nil {
		c.Close()
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
}

func TestDialCancelled(t *testing.T) {
	w := newWorld(t)
	s := pwServer(t, w, sshtest.Opts{})
	ctx, cancel := context.WithCancel(context.Background())
	o := w.opts(base(""), s)
	o.Prompt = func(string, bool) (string, error) {
		cancel()
		time.Sleep(50 * time.Millisecond)
		return "pw", nil
	}
	if _, err := Dial(ctx, "srv", o); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}

func TestResolveFails(t *testing.T) {
	w := newWorld(t)
	if _, err := dialT(t, w.opts("Host srv\n  Port nope\n")); err == nil {
		t.Fatal("bad config dialed")
	}
}
