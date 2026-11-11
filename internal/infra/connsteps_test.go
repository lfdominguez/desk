package infra

import (
	"fmt"
	"image"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/sshc/sshtest"
)

func stepNames(ss []connStep) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.Name)
	}
	return out
}

// TestSSHPaneSteps: a pane that dials shows its steps, the vault's password
// lookup among them, every one ended, through the shell's opening; a pane
// that then reuses the pool's connection has none.
func TestSSHPaneSteps(t *testing.T) {
	skipOnWindows(t, serverPty)
	w := newSSHW(t, sshtest.Opts{Password: "s3cret"}, "    PreferredAuthentications password\n    #@ password-raw s3cret\n")
	term := w.pane(t, paneState{})
	waitS(t, "the prompt", term.AtPrompt)
	ss := term.Steps()
	names := stepNames(ss)
	if names[0] != "Reading the config" || names[1] != "Connecting to 127.0.0.1:"+strconv.Itoa(w.srv.Port) ||
		!slices.Contains(names, "Looking up the password") || names[len(names)-1] != "Opening the shell" {
		t.Fatalf("steps %q", names)
	}
	for _, s := range ss {
		if s.End.IsZero() || s.Err != "" || s.User {
			t.Errorf("step %+v", s)
		}
	}
	if !strings.HasPrefix(stepsTotal(ss), "Connected in ") || term.connecting() {
		t.Errorf("total %q connecting %v", stepsTotal(ss), term.connecting())
	}
	again := w.pane(t, paneState{})
	waitS(t, "the second prompt", again.AtPrompt)
	if n := len(again.Steps()); n != 0 || w.srv.Count() != 1 {
		t.Errorf("pool hit: %d steps, %d connections", n, w.srv.Count())
	}
}

// TestSSHPaneKeySkipsVault: a server whose key logs in never has its
// #@ password looked up.
func TestSSHPaneKeySkipsVault(t *testing.T) {
	skipOnWindows(t, serverPty)
	w := newSSHW(t, sshtest.Opts{}, "    #@ password-raw s3cret\n")
	term := w.pane(t, paneState{})
	waitS(t, "the prompt", term.AtPrompt)
	if names := stepNames(term.Steps()); slices.Contains(names, "Looking up the password") || !slices.Contains(names, "Authenticating: public key") {
		t.Fatalf("steps %q", names)
	}
}

// TestSSHPaneStepFailed: the step that failed keeps the error; the pane is
// failed, not connecting.
func TestSSHPaneStepFailed(t *testing.T) {
	w := newSSHW(t, sshtest.Opts{}, "    Port 1\n")
	term := w.pane(t, paneState{})
	waitS(t, "the failure", func() bool { _, e := term.State(); return e })
	ss := term.Steps()
	last := ss[len(ss)-1]
	if last.Name != "Connecting to 127.0.0.1:1" || last.Err == "" || last.End.IsZero() || stepsTotal(ss) != "" {
		t.Fatalf("steps %+v", ss)
	}
	if p := (&Pane{term: term, waitUntil: time.Now().Add(time.Hour)}); p.connState() != "failed" {
		t.Errorf("state %q", p.connState())
	}
}

// TestConnectingState: a running step is "connecting" until it ends or
// the terminal does.
func TestConnectingState(t *testing.T) {
	term := &Term{}
	p := &Pane{term: term, waitUntil: time.Now().Add(time.Hour)}
	if p.connState() != "" || term.dialed() {
		t.Fatalf("no steps: %q", p.connState())
	}
	n := 0
	term.notify = func() { n++ }
	term.step("Exchanging keys", false)
	if p.connState() != "connecting" || !term.dialed() || n != 1 {
		t.Fatalf("running: %q %d", p.connState(), n)
	}
	term.step("Waiting for your answer", true)
	ss := term.Steps()
	if len(ss) != 2 || ss[0].End.IsZero() || !ss[1].End.IsZero() || !ss[1].User {
		t.Fatalf("steps %+v", ss)
	}
	term.endSteps(fmt.Errorf("refused"))
	if ss = term.Steps(); ss[1].Err != "refused" || ss[1].End.IsZero() || p.connState() == "connecting" {
		t.Fatalf("failed: %+v %q", ss, p.connState())
	}
	term.endSteps(fmt.Errorf("again")) // nothing running: nothing changes
	if term.Steps()[1].Err != "refused" {
		t.Error("an ended step changed")
	}
	other := &Term{}
	other.step("Connecting", false)
	other.exited = true
	if other.connecting() {
		t.Error("an exited terminal isn't connecting")
	}
	var none *Term
	if none.Steps() != nil {
		t.Error("no Term, no steps")
	}
}

func TestStepsTotal(t *testing.T) {
	t0 := time.Unix(1000, 0)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	ss := []connStep{
		{Name: "a", Start: at(0), End: at(400)},
		{Name: "you", User: true, Start: at(400), End: at(3400)},
		{Name: "b", Start: at(3400), End: at(10200)},
	}
	if s := stepsTotal(ss); s != "Connected in 10.2 s · 3.0 s of it waiting for you" {
		t.Errorf("%q", s)
	}
	if s := stepsTotal(ss[2:]); s != "Connected in 6.8 s" {
		t.Errorf("%q", s)
	}
	running := slices.Clone(ss)
	running[2].End = time.Time{}
	failed := slices.Clone(ss)
	failed[2].Err = "no"
	if stepsTotal(running) != "" || stepsTotal(failed) != "" || stepsTotal(nil) != "" {
		t.Error("only a dial that ended well has a total")
	}
	if d := ss[1].dur(at(99999)); d != 3*time.Second {
		t.Errorf("ended: %v", d)
	}
	if d := running[2].dur(at(4400)); d != time.Second {
		t.Errorf("running: %v", d)
	}
}

func TestConnTextConnecting(t *testing.T) {
	now := time.Now()
	p := guiPane{Name: "web1", Conn: "connecting", Since: now.Add(-3 * time.Second)}
	if title, sub, _ := connText(p, now); title != "Connecting to web1…" || sub != "3s" {
		t.Errorf("%q %q", title, sub)
	}
	p.Since = now.Add(-12 * time.Second)
	if _, sub, _ := connText(p, now); sub != "12s and counting. A VPN may be down." {
		t.Errorf("%q", sub)
	}
	p.Since = now.Add(-connectWait)
	if _, sub, _ := connText(p, now); sub != "10s" {
		t.Errorf("at the wait: %q", sub)
	}
}

func TestFmtDur(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0 ms", 340 * time.Millisecond: "340 ms", 999 * time.Millisecond: "999 ms",
		time.Second: "1.0 s", 10240 * time.Millisecond: "10.2 s",
	} {
		if got := fmtDur(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}

// TestGMConnSteps: the box lists the steps while connecting and when it
// failed; once up, the header's "connected in" shows them on hover.
func TestGMConnSteps(t *testing.T) {
	h := newGM(t)
	u := h.u
	st, ts, _ := gmTermState(t)
	h.set(st)
	u.org = "Acme"
	u.clicks = map[string]*widget.Clickable{}
	term := ts[0]
	term.step("Exchanging keys", false)
	term.step("Waiting for your answer", true)
	draw := func(gtx layout.Context) { u.terminals(gtx) }
	st.Tabs[0].Panes[0].Conn = "connecting"
	h.draw(draw)
	if u.clicks["connC"] == nil {
		t.Fatal("the box while connecting")
	}
	term.endSteps(fmt.Errorf("refused"))
	st.Tabs[0].Panes[0].Conn = "failed"
	h.draw(draw)
	id := fmt.Sprintf("steps%p", term)
	if u.clicks[id] != nil {
		t.Fatal("no header total for a failed dial")
	}
	up := &Term{}
	up.step("Exchanging keys", false)
	up.endSteps(nil)
	term.mu.Lock()
	term.steps = up.steps
	term.mu.Unlock()
	st.Tabs[0].Panes[0].Conn = ""
	h.draw(draw)
	c := u.clicks[id]
	if c == nil {
		t.Fatal("the header's connected in")
	}
	h.hover(c, image.Rect(0, 0, h.size.X, 30), 4, draw)
}
