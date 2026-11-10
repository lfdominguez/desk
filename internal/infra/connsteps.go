package infra

import (
	"fmt"
	"slices"
	"time"
)

// A pane's new SSH connection is shown step by step in its connection box,
// each with its time, so a slow connect says where the time went. A pane
// that reuses the pool's connection has none.

// connStep is one step of a dial: running while End is zero, failed when
// Err is set; User marks a wait on the user's answer, not on the network.
type connStep struct {
	Name       string
	User       bool
	Start, End time.Time
	Err        string
}

// dur is how long s took, or has taken by now.
func (s connStep) dur(now time.Time) time.Duration {
	if !s.End.IsZero() {
		now = s.End
	}
	return now.Sub(s.Start)
}

// step ends the running step and starts name (sshc.Options.Step).
func (t *Term) step(name string, user bool) {
	now := time.Now()
	t.mu.Lock()
	if n := len(t.steps); n > 0 && t.steps[n-1].End.IsZero() {
		t.steps[n-1].End = now
	}
	t.steps = append(t.steps, connStep{Name: name, User: user, Start: now})
	t.mu.Unlock()
	if t.notify != nil {
		t.notify()
	}
}

// dialed says this connection was dialed for the pane (it has steps), not
// taken from the pool.
func (t *Term) dialed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.steps) > 0
}

// endSteps ends the running step, failed with err when there is one.
func (t *Term) endSteps(err error) {
	t.mu.Lock()
	if n := len(t.steps); n > 0 && t.steps[n-1].End.IsZero() {
		t.steps[n-1].End = time.Now()
		if err != nil {
			t.steps[n-1].Err = err.Error()
		}
	}
	t.mu.Unlock()
}

// Steps is a copy of the dial's steps, for drawing; none for no Term.
func (t *Term) Steps() []connStep {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.steps)
}

// connecting says the dial is under way.
func (t *Term) connecting() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(t.steps)
	return n > 0 && t.steps[n-1].End.IsZero() && !t.exited
}

// stepsTotal is "Connected in 10.2 s", with the part spent waiting for the
// user apart; "" until the dial ended well.
func stepsTotal(ss []connStep) string {
	if len(ss) == 0 || ss[len(ss)-1].End.IsZero() || ss[len(ss)-1].Err != "" {
		return ""
	}
	s := "Connected in " + fmtDur(ss[len(ss)-1].End.Sub(ss[0].Start))
	var you time.Duration
	for _, st := range ss {
		if st.User {
			you += st.dur(st.End)
		}
	}
	if you > 0 {
		s += " · " + fmtDur(you) + " of it waiting for you"
	}
	return s
}

// fmtDur is milliseconds under a second, then seconds with one decimal.
func fmtDur(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1f s", d.Seconds())
}
