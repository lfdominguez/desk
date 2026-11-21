package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
)

// TestBoardTools: cards and card_read see only the card's organization;
// card_create splits a card off and says its key; the workspace Claude
// only reads.
func TestBoardTools(t *testing.T) {
	h := newHarness(t)
	src := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Fix login", Org: "Acme", Dir: "api", Status: kanban.StatusWorking,
		Links: []kanban.Link{{URL: "https://globex.atlassian.net/browse/ABC-9", Kind: "jira", Ref: "ABC-9", State: "In Progress"}}})
	other := h.u.b.Add(h.u.b.Column("To do"), kanban.Card{Title: "Audit logging", Org: "Acme", Notes: "log every login"})
	h.u.b.Add(0, kanban.Card{Title: "Secret plan", Org: "Globex"})
	h.u.say(other.ID, "you", "start with the API")
	h.u.say(other.ID, "tool", "Bash ls")
	for i := range boardReadMsgs {
		h.u.say(src.ID, "claude", fmt.Sprint("old ", i))
	}
	h.u.say(src.ID, "you", "newest")

	if text, ok := h.tool(src.ID, "cards", `{}`); !ok || !strings.Contains(text, src.Key()+" [In progress, working] ABC-9 Fix login") ||
		!strings.Contains(text, other.Key()+" [To do] Audit logging") || strings.Contains(text, "Secret") {
		t.Fatalf("cards %v %q", ok, text)
	}
	if text, _ := h.tool(src.ID, "cards", `{"query":"login","column":"to do"}`); strings.Contains(text, "Fix login") || !strings.Contains(text, "Audit") {
		t.Fatalf("narrowed %q", text)
	}
	if text, _ := h.tool(src.ID, "cards", `{"query":"zzzq"}`); text != "No cards match." {
		t.Fatalf("none %q", text)
	}
	if text, ok := h.tool(src.ID, "card_read", `{"key":"`+strings.ToLower(other.Key())+`"}`); !ok || !strings.Contains(text, "Column: To do") ||
		!strings.Contains(text, "log every login") || !strings.Contains(text, "[you] start with the API") || strings.Contains(text, "Bash ls") {
		t.Fatalf("read %v %q", ok, text)
	}
	if text, ok := h.tool(src.ID, "card_read", `{"key":"`+src.Key()+`"}`); !ok || !strings.Contains(text, "Status: working") || !strings.Contains(text, "ABC-9 In Progress") ||
		strings.Contains(text, "old 0") || !strings.Contains(text, "[you] newest") {
		t.Fatalf("read own %q", text)
	}
	if _, ok := h.tool(src.ID, "card_read", `{"key":"DESK-3"}`); ok {
		t.Fatal("another organization's card was read")
	}

	text, ok := h.tool(src.ID, "card_create", `{"title":"Rate-limit logins","notes":"per IP"}`)
	todo := h.u.b.Columns[h.u.b.Column("To do")].Cards
	n := todo[len(todo)-1]
	if !ok || !strings.Contains(text, "Made "+n.Key()+" in To do") || n.Org != "Acme" || n.Dir != "api" || !n.Ticket || !strings.HasPrefix(n.Notes, "per IP") {
		t.Fatalf("create %v %q %+v", ok, text, n)
	}
	if text, ok := h.tool(src.ID, "card_create", `{"title":"Later","column":"Backlog","ticket":false}`); !ok || !strings.Contains(text, "until the user starts it") {
		t.Fatalf("no ticket %q", text)
	}
	for _, args := range []string{`{"title":" "}`, `{"title":"x","column":"Nowhere"}`} {
		if _, ok := h.tool(src.ID, "card_create", args); ok {
			t.Fatalf("made with %s", args)
		}
	}
	if _, ok := h.tool("ws:"+src.ID, "card_create", `{"title":"x"}`); ok {
		t.Fatal("the workspace Claude made a card")
	}
	if text, ok := h.tool("ws:"+src.ID, "cards", `{}`); !ok || !strings.Contains(text, "Fix login") {
		t.Fatalf("workspace cards %q", text)
	}
	if _, ok := h.tool("gone", "cards", `{}`); ok {
		t.Fatal("a card off the board listed")
	}
}

// TestTicketCreate: ticket_create makes the card's ticket and links it,
// refuses a second one, and says a tracker's error.
func TestTicketCreate(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme", Ticket: true})
	var asked, drop string
	fail := false
	defer func() { createTicket = links.Create }()
	createTicket = func(_ context.Context, _ orgs.Config, title, body string) (kanban.Link, error) {
		if fail {
			return kanban.Link{}, errors.New("403 Forbidden")
		}
		if drop != "" { // the card leaves the board while the tracker works
			h.u.later(func() { h.u.b.Delete(drop) })
		}
		asked = title + "|" + body
		return kanban.Link{URL: "https://globex.atlassian.net/browse/ABC-1", Kind: "jira", Ref: "ABC-1"}, nil
	}
	if _, ok := h.tool(c.ID, "ticket_create", `{"title":" "}`); ok {
		t.Fatal("made without a title")
	}
	text, ok := h.tool(c.ID, "ticket_create", `{"title":"Fix it","description":"Why."}`)
	got := h.u.b.Card(c.ID)
	if !ok || asked != "Fix it|Why." || !strings.Contains(text, "Created ABC-1") || got.Ticket || ticketOf(got) == nil {
		t.Fatalf("%v %q %+v", ok, text, got)
	}
	if text, ok := h.tool(c.ID, "ticket_create", `{"title":"Again"}`); ok || !strings.Contains(text, "already tracks ABC-1") {
		t.Fatalf("second %q", text)
	}
	d := h.u.b.Add(0, kanban.Card{Title: "y", Org: "Acme"})
	fail = true
	if text, ok := h.tool(d.ID, "ticket_create", `{"title":"t"}`); ok || !strings.Contains(text, "403") {
		t.Fatalf("error %q", text)
	}
	fail, drop = false, d.ID
	if text, ok := h.tool(d.ID, "ticket_create", `{"title":"t"}`); ok || !strings.Contains(text, "the ticket is https://") {
		t.Fatalf("card gone %q", text)
	}
	if _, ok := h.tool("gone", "ticket_create", `{"title":"t"}`); ok {
		t.Fatal("a ticket for no card")
	}
	if _, ok := h.tool("ws:"+d.ID, "ticket_create", `{"title":"t"}`); ok {
		t.Fatal("the workspace Claude made a ticket")
	}
}
