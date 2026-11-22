package infra

import (
	"fmt"
	"slices"
	"strings"
)

// Desk Control's view of the infra workspace (ui's remote_infra.go): each
// tab's state, its panes' commands and the approvals it waits on, handed
// over whenever they change; and the calls that answer an approval, write
// to an organization's Claude and read a pane's screen.

// TabStatus is one infra tab as Desk Control shows it.
type TabStatus struct {
	Org, Card, Name string
	State           string // approval, working, your turn, idle
	Last            string // its Claude's last reply
	Panes           []PaneStatus
	Pending         []Approval // oldest first
}

// PaneStatus is one of a tab's terminals.
type PaneStatus struct {
	Name, Addr, Status string        // Status: Term.Status, "running …", "idle prompt"…
	Screen             func() string `json:"-"` // its visible text now; any goroutine
}

// Approval is a command or a change a tab's Claude waits on.
type Approval struct {
	ID                       int
	Host, Cmd, Reason, Title string // Title: a file change's or a servers edit's (Cmd is then its diff)
	Fixed                    bool   // approve or deny only: no always
}

// approvalSeq numbers approvals (model goroutine).
var approvalSeq int

// await adds a to t's approvals with a number of its own (model goroutine).
func (t *Tab) await(a *approval) {
	approvalSeq++
	a.id = approvalSeq
	t.pending = append(t.pending, a)
}

// OnStatus calls f, on a goroutine of its own, with every tab's status each
// time it changes. Call before any change matters (at start).
func (w *Workspace) OnStatus(f func([]TabStatus)) {
	w.u.d.act(func(m *model) { w.u.d.onStatus = f; w.u.d.statusSig = "" })
}

// statuses is every tab's status (model goroutine).
func statuses(m *model) []TabStatus {
	var out []TabStatus
	for _, t := range m.tabs {
		s := TabStatus{Org: t.org, Card: t.card, Name: t.name, State: tabState(t)}
		for i := len(t.chat) - 1; i >= 0; i-- {
			if t.chat[i].who == "claude" {
				s.Last = t.chat[i].text
				break
			}
		}
		t.mu.Lock()
		panes := slices.Clone(t.panes)
		t.mu.Unlock()
		for _, p := range panes {
			term := p.term
			s.Panes = append(s.Panes, PaneStatus{Name: p.name, Addr: p.addr, Status: term.Status(), Screen: func() string { return term.Screen().Text }})
		}
		for _, a := range t.pending {
			s.Pending = append(s.Pending, Approval{ID: a.id, Host: t.paneName(a.pane), Cmd: a.cmd, Reason: a.reason, Title: a.title, Fixed: a.fixed()})
		}
		out = append(out, s)
	}
	return out
}

// tellStatus hands the statuses to onStatus when they changed (model
// goroutine).
func (d *driver) tellStatus(m *model) {
	if d.onStatus == nil {
		return
	}
	ss := statuses(m)
	var b strings.Builder
	for _, s := range ss {
		fmt.Fprintf(&b, "%s|%s|%s|%s|%s|", s.Org, s.Card, s.Name, s.State, s.Last)
		for _, p := range s.Panes {
			b.WriteString(p.Name + "|" + p.Status + "|")
		}
		for _, a := range s.Pending {
			fmt.Fprintf(&b, "%d|", a.ID)
		}
		b.WriteString("\n")
	}
	if b.String() == d.statusSig {
		return
	}
	d.statusSig = b.String()
	go d.onStatus(ss)
}

// Answer decides approval id wherever it waits: verdict "y" approve, "a"
// always (approve when Fixed), "n" deny. Any goroutine; what happened shows
// in the next status.
func (w *Workspace) Answer(id int, verdict string) {
	d := w.u.d
	d.act(func(m *model) {
		for _, t := range m.tabs {
			i := slices.IndexFunc(t.pending, func(a *approval) bool { return a.id == id })
			if i < 0 {
				continue
			}
			a := t.pending[i]
			if verdict == "a" && a.fixed() {
				verdict = "y"
			}
			a.editing = false
			t.pending = append([]*approval{a}, slices.Delete(t.pending, i, i+1)...) // answer takes the first
			d.run(m.answer(t, keyOf(verdict)))
			return
		}
	})
}

// Say gives text to org's own infra Claude as the user's, opening its
// session if it has none (the window stays where it is); queued while it
// works. Any goroutine; false when desk has no such organization.
func (w *Workspace) Say(org, text string) bool {
	if !slices.ContainsFunc(w.u.d.state.Load().Tree, func(n guiNode) bool { return n.Depth == 0 && n.Org == org }) {
		return false
	}
	w.u.d.act(func(m *model) {
		var t *Tab
		for _, x := range m.tabs {
			if x.orgOnly && x.org == org && x.card == "" {
				t = x
			}
		}
		if t == nil {
			i := slices.IndexFunc(m.tree, func(n node) bool { return n.depth == 0 && n.org == org })
			if i < 0 {
				return
			}
			was := m.focus
			m.openOrg(m.tree[i], "")
			t, m.focus = m.focus, was
		}
		if t.busy {
			t.asked = append(t.asked, text)
			t.chat = append(t.chat, &chatEntry{who: "note", text: "queued: " + text})
			return
		}
		if err := m.send(t, text, &chatEntry{who: "you", text: text}); err != nil {
			t.chat = append(t.chat, &chatEntry{who: "error", text: err.Error()})
		}
	})
	return true
}
