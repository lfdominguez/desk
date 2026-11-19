package infra

import (
	"context"
	"io"
	"slices"
	"time"
)

// Fetching a server's password (alt+p, for a sudo prompt) can take a while:
// the vault's server answers, or it asks for its master password first. The
// pane shows each step in a box like the connection's, so the wait says what
// it waits on. It goes the moment the password is typed; a failed one stays
// with its error until closed or retried. Cancel stops a running one, and
// its password, should it come anyway, is never typed (fetchLive).

// pwFetch is a pane's password fetch: its steps, when it ended (zero while
// it runs), whether the user closed its box, and how to stop it.
type pwFetch struct {
	steps  []connStep
	end    time.Time
	hidden bool
	cancel context.CancelFunc
}

// fetchStart begins a fetch that cancel stops, replacing (and stopping) the
// last one; the fetch, which its password is checked against (fetchLive).
func (t *Term) fetchStart(cancel context.CancelFunc) *pwFetch {
	if t == nil {
		return nil
	}
	f := &pwFetch{cancel: cancel}
	t.mu.Lock()
	old := t.fetch
	t.fetch = f
	t.mu.Unlock()
	if old != nil && old.cancel != nil {
		old.cancel()
	}
	t.redraw()
	return f
}

// fetchLive says f is still the pane's fetch and wasn't cancelled: only then
// is its password typed or copied. No pane, no box to cancel.
func (t *Term) fetchLive(f *pwFetch) bool {
	if t == nil {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.fetch == f && !f.hidden
}

// fetchStep ends the fetch's running step and starts name; user marks a
// wait on the user (the vault's master password). Only while f is the pane's
// fetch: a replaced one's late steps aren't the new one's.
func (t *Term) fetchStep(f *pwFetch, name string, user bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.fetch == f {
		f.step(name, user)
	}
	t.mu.Unlock()
	t.redraw()
}

// step is fetchStep with the Term's mu held.
func (f *pwFetch) step(name string, user bool) {
	if f == nil || !f.end.IsZero() {
		return
	}
	now := time.Now()
	if n := len(f.steps); n > 0 && f.steps[n-1].End.IsZero() {
		f.steps[n-1].End = now
	}
	f.steps = append(f.steps, connStep{Name: name, User: user, Start: now})
}

// typeSecretFor types pw into the sudo prompt at the cursor, only while f is
// the pane's live fetch and the prompt is still there: both checked under
// the same lock as the write, so a Cancel or the prompt going away can't come
// in between. live false: cancelled or replaced, nothing to do.
func (t *Term) typeSecretFor(f *pwFetch, pw string) (live, typed bool) {
	if t == nil {
		return true, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fetch != f || f.hidden {
		return false, false
	}
	if _, ok := t.sudoPrompt(); !ok {
		return true, false
	}
	f.step("Typing it into the prompt", false)
	io.WriteString(t.pty, pw+"\r")
	return true, true
}

// fetchEnd ends the fetch, its running step failed with err when there is one.
func (t *Term) fetchEnd(err error) {
	if t == nil {
		return
	}
	now := time.Now()
	t.mu.Lock()
	if f := t.fetch; f != nil && f.end.IsZero() {
		if n := len(f.steps); n > 0 && f.steps[n-1].End.IsZero() {
			f.steps[n-1].End = now
		}
		if err != nil {
			if len(f.steps) == 0 {
				f.steps = append(f.steps, connStep{Name: "Getting the password", Start: now, End: now})
			}
			f.steps[len(f.steps)-1].Err = err.Error()
		}
		f.end = now
	}
	t.mu.Unlock()
	t.redraw()
}

// fetchHide takes the fetch's box away and stops a running fetch: what it
// brings later is dropped, so nothing lands in a prompt nobody watches.
func (t *Term) fetchHide() {
	if t == nil {
		return
	}
	t.mu.Lock()
	f := t.fetch
	if f != nil {
		f.hidden = true
	}
	t.mu.Unlock()
	if f != nil && f.cancel != nil {
		f.cancel()
	}
}

// Fetch is the fetch's box: its steps, and whether it shows: while it runs
// and once it failed, never after it went well.
func (t *Term) Fetch() (ss []connStep, shown bool) {
	if t == nil {
		return nil, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	f := t.fetch
	if f == nil || f.hidden || !f.end.IsZero() && !fetchFailed(f.steps) {
		return nil, false
	}
	return slices.Clone(f.steps), true
}

// fetchFailed says the fetch's last step failed.
func fetchFailed(ss []connStep) bool { return len(ss) > 0 && ss[len(ss)-1].Err != "" }

// redraw asks the window for a frame (notify), when there is one.
func (t *Term) redraw() {
	if t.notify != nil {
		t.notify()
	}
}

// stepKey carries a fetch's step function through a context, down to the
// vault code that knows when it waits on the user.
type stepKey struct{}

// withStep is ctx carrying step.
func withStep(ctx context.Context, step func(name string, user bool)) context.Context {
	return context.WithValue(ctx, stepKey{}, step)
}

// stepOf is ctx's step function, one doing nothing without one.
func stepOf(ctx context.Context) func(name string, user bool) {
	if f, ok := ctx.Value(stepKey{}).(func(string, bool)); ok && f != nil {
		return f
	}
	return func(string, bool) {}
}
