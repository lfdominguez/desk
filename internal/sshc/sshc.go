// Package sshc is desk's SSH client: golang.org/x/crypto/ssh driven by an
// OpenSSH client config that internal/sshconfig resolves, each option applied
// the way OpenSSH 10.5's ssh does where x/crypto can, and a clear error where
// it can't and the option matters. No ssh binary is run.
package sshc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/lfdominguez/desk/internal/sshconfig"
)

// Options are sshconfig's (the config file, -o overrides…) plus what ssh
// would ask the user or print.
type Options struct {
	sshconfig.Options
	// Prompt asks the user: passwords, passphrases, keyboard-interactive
	// questions and yes/no host-key questions; echo says whether the answer
	// may show as it is typed. nil (or BatchMode yes) asks nothing: an error.
	Prompt func(prompt string, echo bool) (string, error)
	// Password is a first password to try before Prompt (a vault's); ok is
	// false when there is none.
	Password func() (string, bool)
	// Logf gets the lines ssh prints or logs: warnings, banners, debug.
	Logf func(format string, args ...any)
	// Step hears each step of the dial as it starts (a step ends when the
	// next one starts or Dial returns); user marks a wait on the user's
	// answer rather than on the network. nil hears nothing.
	Step func(name string, user bool)

	stepped    bool // Step, Prompt and Password already report steps (a jump host's dial)
	noPassword bool // Password was nil: no spare try for it
}

// ErrLost is a connection whose server stopped answering keepalives
// (ServerAliveInterval × ServerAliveCountMax).
var ErrLost = errors.New("server not responding")

// errNoPrompt answers a question when nobody can be asked.
var errNoPrompt = errors.New("no way to ask the user")

// Client is one authenticated connection, and what lives on it: the jump
// host it went through, its forwards and keepalives.
type Client struct {
	*ssh.Client
	cfg  *sshconfig.Config
	opt  Options
	hc   *hostCheck
	jump *Client

	once sync.Once
	done chan struct{}
	err  error

	mu    sync.Mutex
	stops []func()

	agentFwd sync.Once
}

// Dial resolves host's configuration as ssh would and connects:
// ProxyJump/ProxyCommand or TCP, host key checks, then authentication. A
// failure is a *NegotiationError, a *HostKeyChangedError, wraps ErrHostKey,
// or says what ssh would.
func Dial(ctx context.Context, host string, o Options) (*Client, error) {
	if o.Step != nil {
		o.Step("Reading the config", false) // Match exec runs commands
	}
	cfg, err := sshconfig.Resolve(host, o.Options)
	if err != nil {
		return nil, err
	}
	return dial(ctx, cfg, o)
}

func (o Options) withDefaults(cfg *sshconfig.Config) Options {
	if o.Step == nil {
		o.Step = func(string, bool) {}
	} else if !o.stepped {
		o = o.stepping() // before BatchMode's Prompt below, which asks nobody

	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.Prompt == nil || cfg.Bool("batchmode") {
		o.Prompt = func(string, bool) (string, error) { return "", errNoPrompt }
	}
	if o.Password == nil {
		o.noPassword = true
		o.Password = func() (string, bool) { return "", false }
	}
	return o
}

// stepping makes Prompt and Password steps of their own: a question is the
// user's time, a password lookup (a vault, maybe unlocked first) its own;
// the step they interrupt resumes after them.
func (o Options) stepping() Options {
	step, cur := o.Step, ""
	var mu sync.Mutex
	o.Step = func(name string, user bool) {
		if !user {
			mu.Lock()
			cur = name
			mu.Unlock()
		}
		step(name, user)
	}
	around := func(name string, user bool) func() {
		mu.Lock()
		was := cur
		mu.Unlock()
		step(name, user)
		return func() { o.Step(was, false) }
	}
	if ask := o.Prompt; ask != nil {
		o.Prompt = func(prompt string, echo bool) (string, error) {
			defer around("Waiting for your answer", true)()
			return ask(prompt, echo)
		}
	}
	if pw := o.Password; pw != nil {
		o.Password = func() (string, bool) {
			defer around("Looking up the password", false)()
			return pw()
		}
	}
	o.stepped = true
	return o
}

func dial(ctx context.Context, cfg *sshconfig.Config, o Options) (*Client, error) {
	o = o.withDefaults(cfg)
	for _, w := range cfg.Warnings {
		o.Logf("%s", w)
	}
	if err := unsupported(cfg, o.Logf); err != nil {
		return nil, err
	}
	conn, jump, err := connect(ctx, cfg, o)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Client, error) {
		conn.Close()
		if jump != nil {
			jump.Close()
		}
		return nil, err
	}
	var ip net.IP
	if a, ok := conn.RemoteAddr().(*net.TCPAddr); ok && jump == nil && cfg.ProxyCommand() == "" {
		ip = a.IP
	}
	hc := newHostCheck(cfg, o, ip)
	conf, err := clientConfig(cfg, hc)
	if err != nil {
		return fail(err)
	}
	o.Step("Preparing authentication", false) // the agent
	au := newAuth(cfg, o, hc)
	defer au.close()
	conf.Auth = au.methods()
	conf.BannerCallback = func(m string) error { o.Logf("%s", strings.TrimRight(m, "\n")); return nil }
	// ConnectTimeout covers the banner and key exchange, as ssh's does; the
	// check clears it so a password typed slowly isn't cut off.
	if t := cfg.Duration("connecttimeout"); t > 0 {
		conn.SetDeadline(time.Now().Add(t))
	}
	hc.clear = func() { conn.SetDeadline(time.Time{}) }
	conf.HostKeyCallback = func(_ string, _ net.Addr, key ssh.PublicKey) error {
		hc.clear()
		o.Step("Checking the host key", false)
		if err := hc.check(key); err != nil {
			return err
		}
		o.Step("Authenticating", false)
		return nil
	}
	addr := net.JoinHostPort(cfg.HostName(), strconv.Itoa(cfg.Port()))
	o.Step("Exchanging keys", false)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	sc, chans, reqs, err := ssh.NewClientConn(conn, addr, conf)
	stop()
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return fail(au.explain(negotiation(err)))
	}
	c := &Client{Client: ssh.NewClient(sc, chans, reqs), cfg: cfg, opt: o, hc: hc, jump: jump, done: make(chan struct{})}
	go func() { c.end(c.Client.Wait()) }()
	o.Step("Starting forwards", false)
	if err := c.start(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// start is what ssh does once authenticated: the config's forwards, the
// keepalives, LocalCommand.
func (c *Client) start() error {
	cfg := c.cfg
	exit := cfg.Bool("exitonforwardfailure")
	fwds := 0
	for _, l := range []struct {
		kind byte
		fs   []sshconfig.Forward
	}{{'L', cfg.LocalForwards()}, {'R', cfg.RemoteForwards()}, {'D', cfg.DynamicForwards()}} {
		for _, f := range l.fs {
			fwds++
			if c.hc.unsafe {
				continue
			}
			if _, _, err := c.Forward(l.kind, f); err != nil {
				if exit {
					return fmt.Errorf("Error: forwarding failed: %w", err)
				}
				c.opt.Logf("Warning: forwarding failed: %v", err)
			}
		}
	}
	if c.hc.unsafe && fwds > 0 {
		c.opt.Logf("Port forwarding is disabled to avoid man-in-the-middle attacks.")
		if exit {
			return errors.New("Error: forwarding disabled due to host key check failure")
		}
	}
	if t := time.Duration(cfg.Int("serveraliveinterval")) * aliveUnit; t > 0 {
		go c.keepalive(t, max(cfg.Int("serveralivecountmax"), 1))
	}
	if lc := cfg.Get("localcommand"); lc != "" && cfg.Bool("permitlocalcommand") {
		tok := cfg.Tokens()
		tok['T'] = "NONE"
		cmd, err := sshconfig.Expand(lc, tok, true)
		if err != nil {
			return fmt.Errorf("LocalCommand: %v", err)
		}
		out, err := shellCommand(cmd, false).CombinedOutput()
		if s := strings.TrimRight(string(out), "\n"); s != "" {
			c.opt.Logf("%s", s)
		}
		if err != nil {
			c.opt.Logf("LocalCommand %q: %v", cmd, err)
		}
	}
	return nil
}

// aliveUnit is ServerAliveInterval's unit; tests shorten it.
var aliveUnit = time.Second

// keepalive is ssh's server_alive_check: a keepalive@openssh.com request
// every interval (any reply will do), the connection lost once count of
// them go unanswered.
func (c *Client) keepalive(every time.Duration, count int) {
	t := time.NewTicker(every)
	defer t.Stop()
	replied := make(chan struct{}, 1)
	inFlight, missed := false, 0
	for {
		select {
		case <-c.done:
			return
		case <-replied:
			inFlight, missed = false, 0
		case <-t.C:
			if !inFlight {
				inFlight = true
				go func() {
					if _, _, err := c.SendRequest("keepalive@openssh.com", true, nil); err == nil {
						replied <- struct{}{}
					}
				}()
				continue
			}
			if missed++; missed >= count {
				c.opt.Logf("Timeout, server %s not responding.", c.cfg.HostName())
				c.end(ErrLost)
				return
			}
		}
	}
}

// end closes everything once, keeping the first reason.
func (c *Client) end(err error) {
	c.once.Do(func() {
		c.err = err
		close(c.done)
		c.mu.Lock()
		stops := c.stops
		c.stops = nil
		c.mu.Unlock()
		for _, s := range stops {
			s()
		}
		c.Client.Close()
		if c.jump != nil {
			c.jump.Close()
		}
	})
}

// Close ends the connection, its forwards and the jump host it went through.
func (c *Client) Close() error {
	c.end(net.ErrClosed)
	return nil
}

// Done is closed when the connection ends, for any reason.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err is why the connection ended (ErrLost, net.ErrClosed after Close, the
// transport's error), nil while it lives.
func (c *Client) Err() error {
	select {
	case <-c.done:
		return c.err
	default:
		return nil
	}
}

// Config is the resolved configuration the client connected with.
func (c *Client) Config() *sshconfig.Config { return c.cfg }

// onClose runs stop when the client ends (or now, if it has).
func (c *Client) onClose(stop func()) {
	c.mu.Lock()
	if c.Err() == nil {
		c.stops = append(c.stops, stop)
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	stop()
}

// unsupported says which options desk can't honor: an error for those the
// connection can't do without, a log line for those it does fine without.
// ControlMaster and friends are silent: desk shares connections itself (Pool).
func unsupported(cfg *sshconfig.Config, logf func(string, ...any)) error {
	if cfg.Bool("proxyusefdpass") {
		return errors.New("ProxyUseFdpass isn't supported by desk; use a ProxyCommand that talks on its stdin/stdout")
	}
	if cfg.ProxyCommand() == "-" {
		return errors.New("ProxyCommand - isn't supported by desk")
	}
	for _, k := range []struct{ key, what string }{
		{"forwardx11", "X11 forwarding"}, {"compression", "Compression"}, {"verifyhostkeydns", "VerifyHostKeyDNS"},
	} {
		if cfg.Bool(k.key) {
			logf("%s isn't supported by desk; ignored", k.what)
		}
	}
	if t := cfg.Get("tunnel"); t != "" && t != "false" {
		logf("Tunnel isn't supported by desk; ignored")
	}
	return nil
}
