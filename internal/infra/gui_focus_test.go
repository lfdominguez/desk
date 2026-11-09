package infra

import (
	"testing"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op/clip"
)

// focusTabs is Acme's two tabs: web1 alone and db1 alone, web1 active.
func focusTabs(t *testing.T) (*guiState, *Term, *Term) {
	a, _ := gmTerm(t, "web1", 80, 24)
	b, _ := gmTerm(t, "db1", 80, 24)
	st := &guiState{
		Tree: []guiNode{{Depth: 0, Org: "Acme", Label: "Acme"}, {Depth: 1, Org: "Acme", Group: "web", Host: "web1", Label: "web1"},
			{Depth: 1, Org: "Acme", Group: "db", Host: "db1", Label: "db1"}},
		Tabs: []guiTab{
			{Key: &Tab{}, Name: "web1", Org: "Acme", Group: "web", Pinned: map[int]bool{}, Panes: []guiPane{{Term: a, N: 1, Name: "web1", Focused: true}}},
			{Key: &Tab{}, Name: "db1", Org: "Acme", Group: "db", Pinned: map[int]bool{}, Panes: []guiPane{{Term: b, N: 1, Name: "db1", Focused: true}}},
		}}
	return st, a, b
}

// other is an editor-like widget beside the terminals that can hold the keyboard.
type other struct{}

func (h *gmH) drawWith(o *other, take bool) {
	h.draw(func(gtx layout.Context) {
		h.u.terminals(gtx)
		st := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
		event.Op(gtx.Ops, o)
		st.Pop()
		for {
			if _, ok := gtx.Event(key.FocusFilter{Target: o}); !ok {
				break
			}
		}
		if take {
			gtx.Execute(key.FocusCmd{Tag: o})
		}
	})
}

func (h *gmH) hasKeys(term *Term) bool {
	v := h.u.terms[term]
	return v != nil && h.r.Source().Focused(v)
}

// closeFirst is st with its first pane closed (Ctrl+D): the second is the tab's focused one.
func closeFirst(st *guiState) *guiState {
	st2 := *st
	st2.Tabs = []guiTab{st.Tabs[0]}
	st2.Tabs[0].Panes = []guiPane{st.Tabs[0].Panes[1]}
	st2.Tabs[0].Panes[0].Focused = true
	return &st2
}

func TestKeysFollowClosedPane(t *testing.T) {
	h := newGM(t)
	h.u.org = "Acme"
	st, ts, _ := gmTermState(t)
	h.set(st)
	w := func(gtx layout.Context) { h.u.terminals(gtx) }
	h.draw(w)
	h.draw(w)
	if !h.hasKeys(ts[0]) {
		t.Fatal("the focused pane starts with the keyboard")
	}
	h.set(closeFirst(st))
	h.draw(w)
	h.draw(w)
	if !h.hasKeys(ts[1]) || h.u.keyTerm != ts[1] {
		t.Errorf("the keyboard stays nowhere: keyTerm %p", h.u.keyTerm)
	}
	h.draw(w) // settled: it keeps it
	if !h.hasKeys(ts[1]) || h.u.wantTerm {
		t.Error("settled")
	}
}

func TestKeysStayInEditor(t *testing.T) {
	h := newGM(t)
	h.u.org = "Acme"
	st, ts, _ := gmTermState(t)
	h.set(st)
	o := &other{}
	h.drawWith(o, false)
	h.drawWith(o, false)
	h.drawWith(o, true) // the user clicks the composer
	h.drawWith(o, false)
	if h.hasKeys(ts[0]) || !h.r.Source().Focused(o) || h.u.keyTerm != nil {
		t.Fatalf("the editor has the keyboard: keyTerm %p", h.u.keyTerm)
	}
	h.set(closeFirst(st))
	h.drawWith(o, false)
	h.drawWith(o, false)
	if h.hasKeys(ts[1]) || !h.r.Source().Focused(o) {
		t.Error("a pane closing doesn't take the editor's keys")
	}
}

func TestKeysFollowHostClick(t *testing.T) {
	h := newGM(t)
	h.u.org = "Acme"
	st, a, b := focusTabs(t)
	h.set(st)
	o := &other{}
	h.drawWith(o, false)
	h.drawWith(o, true) // the keyboard is elsewhere (desk's board, the composer)
	h.drawWith(o, false)
	if h.hasKeys(a) || h.u.keyTerm != nil {
		t.Fatal("web1 hasn't the keyboard")
	}
	h.u.openHost(2) // db1: its tab activates frames later
	if len(h.acts()) != 1 {
		t.Fatal("activates db1's tab")
	}
	h.drawWith(o, false) // still web1's state: it takes the request
	st2 := *st
	st2.Active = 1
	h.set(&st2)
	h.drawWith(o, false)
	h.drawWith(o, false)
	if !h.hasKeys(b) || h.u.keyTerm != b {
		t.Errorf("db1 gets the keyboard: keyTerm %p", h.u.keyTerm)
	}
}

func TestKeysNoPaneForgets(t *testing.T) {
	h := newGM(t)
	h.u.org = "Acme"
	st, a, b := focusTabs(t)
	h.set(st)
	w := func(gtx layout.Context) { h.u.terminals(gtx) }
	h.draw(w)
	h.draw(w)
	if !h.hasKeys(a) {
		t.Fatal("web1 has the keyboard")
	}
	// the org's Claude tab, no pane yet: the composer's
	empty := &guiState{Tree: st.Tree, Tabs: []guiTab{{Key: &Tab{}, Org: "Acme", OrgOnly: true}}}
	h.set(empty)
	h.draw(w)
	if h.u.keyTerm != nil {
		t.Error("no pane: nothing to follow")
	}
	// a host picked while it shows: the keyboard waits for the pane
	h.u.keyTerm = paneNext
	h.draw(w)
	if h.u.keyTerm != paneNext {
		t.Error("keeps waiting")
	}
	// Claude opens db1 in it later: the user's typing stays put
	h.u.keyTerm = nil
	withPane := &guiState{Tree: st.Tree, Tabs: []guiTab{{Key: empty.Tabs[0].Key, Org: "Acme", OrgOnly: true, Pinned: map[int]bool{},
		Panes: []guiPane{{Term: b, N: 1, Name: "db1", Focused: true}}}}}
	h.set(withPane)
	h.u.wantTerm = false
	h.draw(w)
	h.draw(w)
	if h.hasKeys(b) {
		t.Error("a pane Claude opens doesn't take the keyboard")
	}
}

func TestKeysOrgOpenForgets(t *testing.T) {
	h := newGM(t)
	st, a, _ := focusTabs(t)
	h.set(st)
	w := &Workspace{u: h.u}
	h.u.keyTerm = a
	if !w.OpenOrg("Acme") || h.u.keyTerm != nil || h.u.wantTerm {
		t.Error("the org's Claude: the composer's keys")
	}
	h.u.keyTerm = a
	w.OpenCard("Acme", "ABC-1")
	if h.u.keyTerm != nil {
		t.Error("a card's session: the composer's keys")
	}
}
