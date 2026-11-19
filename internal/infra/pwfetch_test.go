package infra

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	deskorgs "github.com/lfdominguez/desk/internal/orgs"
	deskvault "github.com/lfdominguez/desk/internal/vault"
)

// TestFetchSteps: a fetch's steps end one another, its box shows while it
// runs, stays once it failed and goes the moment it went well.
func TestFetchSteps(t *testing.T) {
	var none *Term
	none.fetchStart(nil)
	none.fetchStep(nil, "x", false)
	none.fetchEnd(nil)
	none.fetchHide()
	if _, ok := none.Fetch(); ok {
		t.Error("no Term, no box")
	}
	if live, typed := none.typeSecretFor(nil, "pw"); !live || typed {
		t.Error("no Term: nothing to cancel, nowhere to type")
	}
	term := &Term{}
	if _, ok := term.Fetch(); ok {
		t.Error("no fetch yet, no box")
	}
	term.fetchStep(nil, "before", false) // no fetch started: nothing to add to
	old := term.fetchStart(nil)
	f := term.fetchStart(nil)
	term.fetchStep(f, "Looking up", false)
	term.fetchStep(old, "a replaced one's", false) // not the new one's step
	term.fetchStep(f, "Waiting for you", true)
	ss, ok := term.Fetch()
	if !ok || !slices.Equal(stepNames(ss), []string{"Looking up", "Waiting for you"}) || ss[0].End.IsZero() || !ss[1].End.IsZero() || !ss[1].User {
		t.Fatalf("running: %+v %t", ss, ok)
	}
	if live, _ := term.typeSecretFor(old, "pw"); live {
		t.Error("a replaced fetch types")
	}
	term.fetchEnd(nil)
	term.fetchStep(f, "after", false) // an ended fetch takes no more
	if _, ok := term.Fetch(); ok {
		t.Error("a fetch that went well goes at once")
	}
	if ss := term.fetch.steps; len(ss) != 2 || ss[1].End.IsZero() || fetchFailed(ss) {
		t.Fatalf("done: %+v", ss)
	}

	term.fetchStart(nil)
	term.fetchEnd(errors.New("no vault"))
	ss, ok = term.Fetch()
	if !ok || len(ss) != 1 || ss[0].Err != "no vault" || !fetchFailed(ss) {
		t.Fatalf("failed before any step: %+v %t", ss, ok)
	}
	term.fetchHide()
	if _, ok := term.Fetch(); ok {
		t.Error("hidden")
	}
	term.fetchStart(nil)
	if _, ok := term.Fetch(); !ok {
		t.Error("a new fetch shows again")
	}
}

func TestStepOf(t *testing.T) {
	stepOf(context.Background())("nobody listens", false)
	var got []string
	stepOf(withStep(context.Background(), func(n string, _ bool) { got = append(got, n) }))("a", false)
	if !slices.Equal(got, []string{"a"}) {
		t.Errorf("%q", got)
	}
}

// TestServerPasswordSteps: the lookup names its item and vault; a raw
// password says where it came from.
func TestServerPasswordSteps(t *testing.T) {
	g := filepath.Join(t.TempDir(), "Acme", infraDir)
	orgsMu.Lock()
	orgs[g] = &orgConfig{Vaults: []VaultCfg{{Name: "pb", Type: "passbolt"}},
		vaults: map[string]vault{"pb": fakeVault{"id1": {ID: "id1", Name: "srv", Password: "fake-s3cret"}}}}
	orgsMu.Unlock()
	var got []string
	ctx := withStep(context.Background(), func(n string, _ bool) { got = append(got, n) })
	if pw, err := serverPasswordCtx(ctx, g, &SecretRef{Item: "srv"}); pw != "fake-s3cret" || err != nil {
		t.Fatalf("%q %v", pw, err)
	}
	if !slices.Equal(got, []string{"Looking up srv in pb"}) {
		t.Errorf("vault: %q", got)
	}
	got = nil
	if pw, _ := serverPasswordCtx(ctx, g, &SecretRef{Raw: "fake-raw"}); pw != "fake-raw" || !slices.Equal(got, []string{"Taking password-raw from the servers file"}) {
		t.Errorf("raw: %q", got)
	}
}

// TestUnlockingSteps: a locked vault's wait on the user is a step of the
// user's, then its unlock one of the network's.
func TestUnlockingSteps(t *testing.T) {
	type st struct {
		name string
		user bool
	}
	var got []st
	ctx := withStep(context.Background(), func(n string, u bool) { got = append(got, st{n, u}) })
	lbFakeAsk(t, func(int, *question) (string, bool) { return "fake-pw", true })
	locked := func() (string, error) { return "", &deskvault.LockedError{} }
	unlocking(ctx, deskorgs.Vault{Name: "pb", Kind: "passbolt"}, locked)
	if len(got) < 2 || got[0] != (st{"Waiting for your passbolt passphrase · pb", true}) || got[1] != (st{"Unlocking pb", false}) {
		t.Errorf("passphrase: %+v", got)
	}
	got = nil
	unlocking(ctx, deskorgs.Vault{Name: "bw", Kind: "bitwarden"}, locked)
	if len(got) < 1 || got[0] != (st{"Waiting for your master password · bw", true}) {
		t.Errorf("master password: %+v", got)
	}
	got = nil
	lbFakeAsk(t, func(int, *question) (string, bool) { return "", false })
	unlocking(ctx, deskorgs.Vault{Name: "v"}, func() (string, error) { return "", &deskvault.LockedError{Code: true} })
	if len(got) != 1 || got[0] != (st{"Waiting for your one-time code · v", true}) {
		t.Errorf("code: %+v", got)
	}
}

func TestFetchText(t *testing.T) {
	now := time.Now()
	run := []connStep{{Name: "Looking up", Start: now.Add(-3 * time.Second)}}
	if title, sub := fetchText("web1", run, now); title != "Getting web1's password…" || sub != "3s" {
		t.Errorf("running: %q %q", title, sub)
	}
	if _, sub := fetchText("web1", nil, now); sub != "0s" {
		t.Errorf("no step yet: %q", sub)
	}
	run[0].Err = "boom"
	if title, sub := fetchText("web1", run, now); title != "Couldn't get web1's password" || sub != "Nothing was typed or copied" {
		t.Errorf("failed: %q %q", title, sub)
	}
}

// TestGMFetchBox: the box over the pane while the password is fetched
// (Cancel), once it failed (Close, Try again), none the moment it went well;
// its keys cancel it and retry it, and keep the rest from the prompt.
func TestGMFetchBox(t *testing.T) {
	h := newGM(t)
	u := h.u
	st, ts, _ := gmTermState(t)
	h.set(st)
	u.org = "Acme"
	draw := func(gtx layout.Context) { u.terminals(gtx) }
	term := ts[0]
	p := st.Tabs[0].Panes[0]
	p.Term = term

	term.fetchStart(nil)
	term.fetchStep(term.fetch, "Looking up srv in pb", false)
	u.clicks = map[string]*widget.Clickable{}
	h.draw(draw)
	if u.clicks[pwButton("C", term)] == nil || u.clicks[pwButton("R", term)] != nil {
		t.Fatal("running: Cancel only")
	}
	if !u.fetchKey(p, "x", "") || !u.fetchKey(p, "", "X") {
		t.Error("a running fetch keeps the keys from the prompt")
	}
	stopped := false
	f := term.fetchStart(func() { stopped = true })
	if !u.fetchKey(p, "", key.NameEscape) {
		t.Error("esc cancels")
	}
	if _, ok := term.Fetch(); ok || !stopped || term.fetchLive(f) {
		t.Fatalf("esc must stop the fetch and take the box away: box %t, stopped %t", ok, stopped)
	}
	term.fetchStart(func() { stopped = true }) // typed on the pane: the box's keys, never the prompt's
	stopped = false                            // after: starting it stopped the last one
	v := u.view(term)
	h.draw(func(gtx layout.Context) { gtx.Execute(key.FocusCmd{Tag: v}); u.terminals(gtx) })
	h.r.Queue(key.EditEvent{Text: "x"}, key.Event{Name: key.NameReturn, State: key.Press})
	h.draw(draw)
	if _, ok := term.Fetch(); !ok || stopped {
		t.Fatalf("typing and Enter must leave the box up, the fetch going: box %t, stopped %t", ok, stopped)
	}
	h.r.Queue(key.Event{Name: key.NameEscape, State: key.Press})
	h.draw(draw)
	if _, ok := term.Fetch(); ok || !stopped {
		t.Fatal("Esc on the pane cancels")
	}
	term.fetchStart(func() { stopped = true })
	stopped = false
	h.draw(draw)
	u.click(pwButton("C", term)).Click() // the button does the same
	h.draw(draw)
	if _, ok := term.Fetch(); ok || !stopped || !u.wantTerm {
		t.Fatalf("Cancel: box %t, stopped %t", ok, stopped)
	}

	term.fetchStart(nil)
	term.fetchEnd(errors.New("no item \"srv\""))
	u.clicks = map[string]*widget.Clickable{}
	h.draw(draw)
	if u.clicks[pwButton("C", term)] == nil || u.clicks[pwButton("R", term)] == nil {
		t.Fatal("failed: Close and Try again")
	}
	h.acts()
	if !u.fetchKey(p, "r", "") || len(h.acts()) != 1 {
		t.Error("R tries again")
	}
	u.click(pwButton("R", term)).Click()
	h.draw(draw)
	acts := h.acts()
	if len(acts) != 1 {
		t.Fatal("Try again tries again")
	}
	other := &Pane{term: &Term{}} // the focused pane: not the one retried
	acts[0](&model{tabs: []*Tab{{panes: []*Pane{other, {term: term}}}}})
	if _, ok := term.Fetch(); !ok || other.term.fetch != nil {
		t.Error("Try again fetches for its own pane")
	}
	acts[0](&model{}) // its pane gone: nothing
	if !u.fetchKey(p, "c", "") {
		t.Error("C closes")
	}
	term.fetchStart(nil)
	term.fetchEnd(errors.New("no item \"srv\""))
	if !u.fetchKey(p, "c", "") {
		t.Error("C closes")
	}
	if u.fetchKey(p, "c", "") {
		t.Error("closed: the keys go to the prompt again")
	}

	term.fetchStart(nil)
	term.fetchEnd(nil)
	u.clicks = map[string]*widget.Clickable{}
	h.draw(draw)
	if u.clicks[pwButton("C", term)] != nil {
		t.Error("done: the box is gone at once")
	}
	if u.fetchKey(p, "x", "") {
		t.Error("done: typing goes on at once")
	}
}
