package sshc

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lfdominguez/desk/internal/sshconfig"
)

// connect is the byte stream to the server: through the ProxyJump hops, a
// ProxyCommand, or TCP. jump is the last hop's client, closed with ours.
func connect(ctx context.Context, cfg *sshconfig.Config, o Options) (net.Conn, *Client, error) {
	addr := net.JoinHostPort(cfg.HostName(), strconv.Itoa(cfg.Port()))
	if hops := cfg.ProxyJump(); len(hops) > 0 {
		jc, err := dialHop(ctx, hops, o)
		if err != nil {
			return nil, nil, err
		}
		o.Step("Opening a channel to "+addr, false)
		conn, err := jc.DialContext(ctx, "tcp", addr)
		if err != nil {
			jc.Close()
			return nil, nil, fmt.Errorf("jump host %s: channel to %s: %w", jc.cfg.HostName(), addr, err)
		}
		return conn, jc, nil
	}
	if pc := cfg.ProxyCommand(); pc != "" {
		o.Step("Starting ProxyCommand", false)
		conn, err := proxyCommand(cfg, pc, o.Logf)
		return conn, nil, err
	}
	conn, err := dialTCP(ctx, cfg, addr, o.Step)
	return conn, nil, err
}

// dialHop is ssh's ProxyJump: `ssh -J <the hops before> -W host:port
// <last hop>`, the last hop resolved from the same config files with its
// own user and port, so its own ProxyJump (or the earlier hops) applies.
func dialHop(ctx context.Context, hops []sshconfig.Hop, o Options) (*Client, error) {
	last := hops[len(hops)-1]
	ho := o
	ho.Step = func(name string, user bool) { o.Step("via "+last.Host+": "+name, user) }
	ho.Options.User, ho.Options.Port, ho.Options.Overrides = last.User, last.Port, nil
	if len(hops) > 1 {
		var rest []string
		for _, h := range hops[:len(hops)-1] {
			rest = append(rest, hopString(h))
		}
		ho.Options.Overrides = []string{"ProxyJump " + strings.Join(rest, ",")}
	}
	return Dial(ctx, last.Host, ho)
}

func hopString(h sshconfig.Hop) string {
	s := h.Host
	if strings.Contains(s, ":") {
		s = "[" + s + "]"
	}
	if h.User != "" {
		s = h.User + "@" + s
	}
	if h.Port > 0 {
		s += ":" + strconv.Itoa(h.Port)
	}
	return s
}

// dialTCP is ssh_connect_direct: AddressFamily, BindAddress or
// BindInterface, ConnectTimeout and TCPKeepAlive, tried ConnectionAttempts
// times a second apart.
// step names the lookup of a host name, then each connect (the dialer's
// Control runs once the address is known).
func dialTCP(ctx context.Context, cfg *sshconfig.Config, addr string, step func(string, bool)) (net.Conn, error) {
	d, network, err := dialer(cfg)
	if err != nil {
		return nil, err
	}
	if net.ParseIP(cfg.HostName()) == nil {
		step("Looking up "+cfg.HostName(), false)
	}
	d.Control = func(_, address string, _ syscall.RawConn) error {
		step("Connecting to "+address, false)
		return nil
	}
	var conn net.Conn
	for i := range max(cfg.Int("connectionattempts"), 1) {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryPause):
			}
		}
		if conn, err = d.DialContext(ctx, network, addr); err == nil {
			return conn, nil
		}
	}
	return nil, fmt.Errorf("ssh: connect to host %s: %w", addr, err)
}

// dialer is the TCP dialer and network the config asks for.
func dialer(cfg *sshconfig.Config) (*net.Dialer, string, error) {
	network := "tcp"
	switch cfg.Get("addressfamily") {
	case "inet":
		network = "tcp4"
	case "inet6":
		network = "tcp6"
	}
	d := &net.Dialer{Timeout: cfg.Duration("connecttimeout")}
	if !cfg.Bool("tcpkeepalive") {
		d.KeepAlive = -1
	}
	local, err := bindAddr(cfg, network)
	if local != nil {
		d.LocalAddr = &net.TCPAddr{IP: local}
	}
	return d, network, err
}

// retryPause is ssh's sleep between ConnectionAttempts.
var retryPause = time.Second

// bindAddr is the local address to connect from: BindAddress, else the
// first of BindInterface's addresses in the family (not an IPv6 link-local).
func bindAddr(cfg *sshconfig.Config, network string) (net.IP, error) {
	fits := func(ip net.IP) bool { v4 := ip.To4() != nil; return network == "tcp" || v4 == (network == "tcp4") }
	if a := cfg.Get("bindaddress"); a != "" {
		if ip := net.ParseIP(a); ip != nil {
			return ip, nil
		}
		ips, err := net.LookupIP(a)
		for _, ip := range ips {
			if fits(ip) {
				return ip, nil
			}
		}
		return nil, fmt.Errorf("BindAddress %s: no address (%v)", a, err)
	}
	name := cfg.Get("bindinterface")
	if name == "" {
		return nil, nil
	}
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return nil, fmt.Errorf("BindInterface %s: %v", name, err)
	}
	// IPv4 first for any family: Windows lists ::1 before 127.0.0.1.
	// ponytail: OpenSSH takes the target address's family; do that if a
	// v6-only target behind BindInterface ever needs it.
	addrs, _ := ifc.Addrs()
	for _, v4 := range []bool{true, false} {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && !n.IP.IsLinkLocalUnicast() && fits(n.IP) && (n.IP.To4() != nil) == v4 {
				return n.IP, nil
			}
		}
	}
	return nil, fmt.Errorf("BindInterface %s has no usable address", name)
}

// shellCommand is ssh's way of running a config command: `exec cmd` in the
// user's shell ($SHELL, else /bin/sh), `cmd /C` on Windows.
func shellCommand(cmd string, execIt bool) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/C", cmd)
	}
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	if execIt {
		cmd = "exec " + cmd
	}
	return exec.Command(sh, "-c", cmd)
}

// proxyCommand is ssh_proxy_connect: the expanded command's stdin and
// stdout are the connection, its stderr goes to the log.
func proxyCommand(cfg *sshconfig.Config, pc string, logf func(string, ...any)) (net.Conn, error) {
	cmdline, err := sshconfig.Expand(pc, cfg.Tokens(), false)
	if err != nil {
		return nil, fmt.Errorf("ProxyCommand %s: %v", pc, err)
	}
	cmd := shellCommand(cmdline, true)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errOut, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ProxyCommand %s: %w", cmdline, err)
	}
	go func() {
		sc := bufio.NewScanner(errOut)
		for sc.Scan() {
			logf("%s", sc.Text())
		}
	}()
	return &pipeConn{ReadCloser: out, w: in, cmd: cmd}, nil
}

// pipeConn is a command's stdio as a net.Conn; deadlines aren't kept.
type pipeConn struct {
	io.ReadCloser
	w    io.WriteCloser
	cmd  *exec.Cmd
	once sync.Once
}

func (p *pipeConn) Write(b []byte) (int, error) { return p.w.Write(b) }

// Close ends the command: its stdin closed, then killed.
func (p *pipeConn) Close() error {
	p.once.Do(func() {
		p.w.Close()
		p.cmd.Process.Kill()
		p.cmd.Wait()
	})
	return nil
}

func (p *pipeConn) LocalAddr() net.Addr              { return pipeAddr{} }
func (p *pipeConn) RemoteAddr() net.Addr             { return pipeAddr{} }
func (p *pipeConn) SetDeadline(time.Time) error      { return nil }
func (p *pipeConn) SetReadDeadline(time.Time) error  { return nil }
func (p *pipeConn) SetWriteDeadline(time.Time) error { return nil }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "proxy-command" }
