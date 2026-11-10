package sshc

import (
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/lfdominguez/desk/internal/sshc/sshtest"
)

// steps records o's Step calls, a wait on the user marked "you: ".
type steps struct {
	mu  sync.Mutex
	got []string
}

func (s *steps) on(o *Options) {
	o.Step = func(name string, user bool) {
		if user {
			name = "you: " + name
		}
		s.mu.Lock()
		s.got = append(s.got, name)
		s.mu.Unlock()
	}
}

func (s *steps) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.got)
}

func (s *steps) want(t *testing.T, want ...string) {
	t.Helper()
	if got := s.list(); !slices.Equal(got, want) {
		t.Fatalf("steps\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestStepsPasswordFromHook(t *testing.T) {
	w := newWorld(t)
	srv := newServer(t, sshtest.Opts{Password: "vault"})
	o := w.opts(base("  PreferredAuthentications password\n"), srv)
	o.Password = func() (string, bool) { return "vault", true }
	var s steps
	s.on(&o)
	mustDial(t, o)
	s.want(t, "Reading the config", "Connecting to 127.0.0.1:"+strconv.Itoa(srv.Port),
		"Preparing authentication", "Exchanging keys", "Checking the host key", "Authenticating",
		"Authenticating: password", "Looking up the password", "Authenticating: password",
		"Starting forwards")
}

func TestStepsPassphraseIsTheUsers(t *testing.T) {
	w := newWorld(t)
	k := writeKey(t, w.path("id"), "secret")
	srv := newServer(t, sshtest.Opts{Keys: []ssh.PublicKey{k.PublicKey()}})
	w.ans = []string{"secret"}
	o := w.opts(base("  PreferredAuthentications publickey\n"), srv)
	var s steps
	s.on(&o)
	mustDial(t, o)
	got := s.list()
	i := slices.Index(got, "you: Waiting for your answer")
	if i < 1 || got[i-1] != "Authenticating: public key" || got[i+1] != "Authenticating: public key" {
		t.Fatalf("steps %q", got)
	}
}

func TestStepsBatchModeWaitsOnNobody(t *testing.T) {
	w := newWorld(t)
	srv := newServer(t, sshtest.Opts{Password: "pw"})
	o := w.opts(base("  BatchMode yes\n  PreferredAuthentications password\n"), srv)
	var s steps
	s.on(&o)
	if _, err := dialT(t, o); err == nil {
		t.Fatal("connected")
	}
	if got := s.list(); slices.Contains(got, "you: Waiting for your answer") || got[len(got)-1] != "Authenticating: password" {
		t.Fatalf("steps %q", got)
	}
}

func TestStepsFailedAuthEndsOnIt(t *testing.T) {
	w := newWorld(t)
	srv := newServer(t, sshtest.Opts{Password: "right"})
	w.ans = []string{"wrong"}
	o := w.opts(base("  NumberOfPasswordPrompts 1\n  PreferredAuthentications password\n"), srv)
	var s steps
	s.on(&o)
	if _, err := dialT(t, o); err == nil {
		t.Fatal("connected")
	}
	got := s.list()
	if got[len(got)-1] != "Authenticating: password" || got[len(got)-2] != "you: Waiting for your answer" {
		t.Fatalf("steps %q", got)
	}
}

// TestStepsKeyboardInteractive: the method's step is the network's, its
// questions the user's.
func TestStepsKeyboardInteractive(t *testing.T) {
	w := newWorld(t)
	srv := newServer(t, sshtest.Opts{Kbd: []string{"one"}})
	w.ans = []string{"one"}
	o := w.opts(base("  PreferredAuthentications keyboard-interactive\n"), srv)
	var s steps
	s.on(&o)
	mustDial(t, o)
	got := s.list()
	i := slices.Index(got, "Authenticating: keyboard-interactive")
	if i < 0 || got[i+1] != "you: Waiting for your answer" || got[i+2] != "Authenticating: keyboard-interactive" {
		t.Fatalf("steps %q", got)
	}
}

// TestStepsWithoutPrompt: with Step but no Prompt, a question fails as it
// would without Step.
func TestStepsWithoutPrompt(t *testing.T) {
	w := newWorld(t)
	srv := newServer(t, sshtest.Opts{Password: "pw"})
	o := w.opts(base("  PreferredAuthentications password\n"), srv)
	o.Prompt = nil
	var s steps
	s.on(&o)
	if _, err := dialT(t, o); err == nil || !strings.Contains(err.Error(), errNoPrompt.Error()) {
		t.Fatalf("err %v", err)
	}
	if slices.Contains(s.list(), "you: Waiting for your answer") {
		t.Errorf("steps %q", s.list())
	}
}

func TestStepsLookupThenConnect(t *testing.T) {
	w := newWorld(t)
	srv := pwServer(t, w, sshtest.Opts{})
	w.ans = []string{"pw"}
	o := w.opts(strings.Replace(base(""), "127.0.0.1", "localhost", 1), srv)
	var s steps
	s.on(&o)
	mustDial(t, o)
	got := s.list()
	if got[1] != "Looking up localhost" || !strings.HasPrefix(got[2], "Connecting to ") || !strings.HasSuffix(got[2], ":"+strconv.Itoa(srv.Port)) {
		t.Fatalf("steps %q", got)
	}
}

func TestStepsThroughJumpHost(t *testing.T) {
	w := newWorld(t)
	jump, target := pwServer(t, w, sshtest.Opts{}), pwServer(t, w, sshtest.Opts{})
	w.ans = []string{"pw", "pw"}
	o := w.opts("Host target\n  Port %PORT1\n  ProxyJump jump\nHost jump\n  Port %PORT0\n"+common, jump, target)
	var s steps
	s.on(&o)
	c, err := Dial(context.Background(), "target", o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got := s.list()
	hop := slices.Index(got, "via jump: Exchanging keys")
	ch := slices.Index(got, "Opening a channel to 127.0.0.1:"+strconv.Itoa(target.Port))
	// A question on the hop resumes the hop's step, not the target's.
	ask := slices.Index(got, "you: Waiting for your answer")
	// One wait per question: the hop's dial doesn't wrap Prompt again.
	if n := len(slices.DeleteFunc(slices.Clone(got), func(s string) bool { return !strings.HasPrefix(s, "you: ") })); n != 2 {
		t.Fatalf("%d waits:\n%s", n, strings.Join(got, "\n"))
	}
	if hop < 0 || ch < hop || ask < hop || ask > ch || !strings.HasPrefix(got[ask+1], "via jump: Authenticating") || got[ch+1] != "Preparing authentication" {
		t.Fatalf("steps\n%s", strings.Join(got, "\n"))
	}
}

func TestStepsProxyCommand(t *testing.T) {
	w := newWorld(t)
	srv := pwServer(t, w, sshtest.Opts{})
	bin, _ := os.Executable()
	w.ans = []string{"pw"}
	o := w.opts(base("  ProxyCommand "+bin+" sshc-bridge %h %p\n"), srv)
	var s steps
	s.on(&o)
	mustDial(t, o)
	if got := s.list(); got[1] != "Starting ProxyCommand" || got[2] != "Preparing authentication" {
		t.Fatalf("steps %q", got)
	}
}

func TestStepsHostKeyQuestionResumesCheck(t *testing.T) {
	w := newWorld(t)
	srv := pwServer(t, w, sshtest.Opts{})
	w.ans = []string{"yes", "pw"}
	o := w.opts(strings.Replace(base(""), "accept-new", "ask", 1), srv)
	var s steps
	s.on(&o)
	mustDial(t, o)
	got := s.list()
	i := slices.Index(got, "Checking the host key")
	if i < 0 || got[i+1] != "you: Waiting for your answer" || got[i+2] != "Checking the host key" || got[i+3] != "Authenticating" {
		t.Fatalf("steps\n%s", strings.Join(got, "\n"))
	}
}

func TestStepsNoneOnPoolHit(t *testing.T) {
	w := newWorld(t)
	srv := pwServer(t, w, sshtest.Opts{})
	w.ans = []string{"pw"}
	var p Pool
	o := w.opts(base(""), srv)
	var first, second steps
	first.on(&o)
	_, rel, err := p.Get(context.Background(), "srv", o)
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	second.on(&o)
	_, rel2, err := p.Get(context.Background(), "srv", o)
	if err != nil {
		t.Fatal(err)
	}
	rel2()
	if len(first.list()) == 0 || len(second.list()) != 0 {
		t.Fatalf("first %q, second %q", first.list(), second.list())
	}
}
