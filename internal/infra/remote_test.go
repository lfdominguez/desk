package infra

import (
	"strings"
	"testing"

	"github.com/lfdominguez/desk/internal/testwait"
	"time"
)

// drain runs the actions queued for the model on m.
func drain(d *driver, m *model) {
	for len(d.in) > 0 {
		if f, ok := (<-d.in).(func(*model)); ok {
			f(m)
		}
	}
}

// TestRemoteStatus: OnStatus hears each change once, with every tab's
// state, Claude's last reply, panes and numbered approvals; Answer decides
// any tab's approval by number (always becomes approve when it's fixed);
// Say writes to an organization's Claude, queued while it works, opening
// its session if needed without moving the window.
func TestRemoteStatus(t *testing.T) {
	stateC(t)
	h := newGM(t)
	w := &Workspace{u: h.u}
	got := make(chan []TabStatus, 8)
	w.OnStatus(func(ss []TabStatus) { got <- ss })
	term, _ := fakeTermA(t, "web1")
	busy := &Tab{org: "Acme", name: "Acme", orgOnly: true, busy: true, term: term, panes: []*Pane{{id: 1, term: term, name: "web1"}},
		chat: []*chatEntry{{who: "claude", text: "checking"}, {who: "you", text: "go"}}}
	other := &Tab{org: "Globex", name: "Globex · DESK-3", card: "DESK-3"}
	m := &model{tabs: []*Tab{busy, other}}
	drain(h.d, m)
	other.await(&approval{cmd: "rm -rf /tmp/x", reason: "clean", done: make(chan approvalResult, 1)})
	other.await(&approval{cmd: "--- a\n+++ b", file: "/etc/x", title: "Edit /etc/x", done: make(chan approvalResult, 1)})
	h.d.tellStatus(m)
	h.d.tellStatus(m) // the same: not told again
	var ss []TabStatus
	select {
	case ss = <-got:
	case <-time.After(testwait.Long):
		t.Fatal("not told")
	}
	if len(ss) != 2 || ss[0].State != "working" || ss[0].Last != "checking" || len(ss[0].Panes) != 1 || ss[0].Panes[0].Name != "web1" ||
		ss[1].State != "approval" || len(ss[1].Pending) != 2 || ss[1].Pending[0].Cmd != "rm -rf /tmp/x" || ss[1].Pending[1].Title != "Edit /etc/x" || !ss[1].Pending[1].Fixed {
		t.Fatalf("status %+v", ss)
	}
	if ss[0].Panes[0].Screen == nil {
		t.Fatal("no screen")
	}
	ss[0].Panes[0].Screen()
	select {
	case <-got:
		t.Fatal("told twice")
	default:
	}

	fixed, rm := other.pending[1], other.pending[0]
	w.Answer(fixed.id, "a") // the second first: always on a diff is approve
	drain(h.d, m)
	if r := <-fixed.done; !r.ok || len(other.pending) != 1 || other.pending[0] != rm {
		t.Fatalf("fixed: %+v, left %v", r, other.pending)
	}
	w.Answer(rm.id, "n")
	w.Answer(999, "y") // gone: nothing
	drain(h.d, m)
	if r := <-rm.done; r.ok || len(other.pending) != 0 {
		t.Fatalf("denied: %+v", r)
	}

	h.d.state.Store(&guiState{Tree: []guiNode{{Depth: 0, Org: "Acme"}, {Depth: 0, Org: "Globex"}}})
	if w.Say("Initech", "hi") {
		t.Fatal("no such org")
	}
	if !w.Say("Acme", "restart nginx") {
		t.Fatal("Acme refused")
	}
	drain(h.d, m)
	if len(busy.asked) != 1 || busy.asked[0] != "restart nginx" {
		t.Fatalf("queued %v", busy.asked)
	}
	t.Setenv("PATH", t.TempDir()) // no claude: the send fails, in its chat
	m.tree = []node{{depth: 0, org: "Globex", label: "Globex", dir: t.TempDir()}}
	m.focus = busy
	w.Say("Globex", "uptime?")
	drain(h.d, m)
	if len(m.tabs) != 3 || m.focus != busy || m.tabs[2].org != "Globex" || len(m.tabs[2].chat) == 0 {
		t.Fatalf("opened %d tabs, focus %v", len(m.tabs), m.focus)
	}
	if e := m.tabs[2].chat[len(m.tabs[2].chat)-1]; e.who != "error" && !strings.Contains(e.text, "uptime?") {
		t.Fatalf("chat %+v", e)
	}
	h.d.state.Store(&guiState{Tree: []guiNode{{Depth: 0, Org: "Initech"}}})
	m.tree = nil
	w.Say("Initech", "x") // its node gone from the model meanwhile: nothing
	drain(h.d, m)
	if len(m.tabs) != 3 {
		t.Fatalf("%d tabs", len(m.tabs))
	}
}
