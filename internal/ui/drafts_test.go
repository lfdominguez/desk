package ui

import (
	"testing"

	"github.com/lfdominguez/desk/internal/kanban"
)

// TestDraftsPerCard: a message written for one card stays with it while
// another card's session is open, and comes back with the caret at its end;
// the side panel and the full window share it, and a quit keeps it.
func TestDraftsPerCard(t *testing.T) {
	h := newHarness(t)
	a := h.u.b.Add(0, kanban.Card{Title: "a"}).ID
	b := h.u.b.Add(0, kanban.Card{Title: "b"}).ID
	h.u.openPanel(a)
	h.u.chatIn.SetText("a long message")
	h.u.openSession(a)
	if h.u.chatIn.Text() != "a long message" {
		t.Errorf("the full window keeps the panel's text: %q", h.u.chatIn.Text())
	}
	h.u.openPanel(b)
	if h.u.chatIn.Text() != "" || h.u.b.Card(a).Draft != "a long message" {
		t.Errorf("b starts empty, a keeps its draft: %q, %q", h.u.chatIn.Text(), h.u.b.Card(a).Draft)
	}
	h.u.chatIn.SetText("for b")
	h.u.openSession(a)
	if s, e := h.u.chatIn.Selection(); h.u.chatIn.Text() != "a long message" || s != e || s != len("a long message") {
		t.Errorf("a's draft back, caret at its end: %q at %d-%d", h.u.chatIn.Text(), s, e)
	}
	h.u.chatIn.SetText("")
	h.u.quitting()
	if h.u.b.Card(a).Draft != "" || h.u.b.Card(b).Draft != "for b" {
		t.Errorf("drafts after a quit: a %q, b %q", h.u.b.Card(a).Draft, h.u.b.Card(b).Draft)
	}
	loaded, err := kanban.Load(h.u.path)
	if err != nil || loaded.Card(b) == nil || loaded.Card(b).Draft != "for b" {
		t.Errorf("the draft is on the saved board: %v", err)
	}
}
