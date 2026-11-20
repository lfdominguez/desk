package ui

import (
	"testing"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
)

// TestLinearTicket: a Linear issue is a card's ticket, as a Jira one: it
// syncs, counts as an open ticket, and Settings has its account kind.
func TestLinearTicket(t *testing.T) {
	l := kanban.Link{URL: "https://linear.app/acme/issue/ENG-1", Kind: links.KindLinear, Ref: "ENG-1"}
	c := &kanban.Card{Links: []kanban.Link{{URL: "https://github.com/a/b/pull/1", Kind: links.KindGitHubPR}, l}}
	if !ticket(l) || !tracked(l) || ticketOf(c) != &c.Links[1] {
		t.Error("a Linear issue isn't a ticket")
	}
	if tracked(kanban.Link{Kind: links.KindGitHubIssue}) {
		t.Error("a GitHub issue has no key")
	}
	found := false
	for _, k := range cfgKinds {
		found = found || k.key == "linear" && cfgAbbr["linear"] == "LN"
	}
	if !found {
		t.Error("no Linear kind in Settings")
	}
}
