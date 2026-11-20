package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/vault"
)

// syncTicket and transitionTicket are links.Sync and links.Transition;
// tests replace them.
var (
	syncTicket       = links.Sync
	transitionTicket = links.Transition
)

// workflowPrompt is card c's organization's workflow, for its Claude's
// system prompt; "" without rules.
func (u *ui) workflowPrompt(c *kanban.Card) string {
	cfg, err := orgs.Load(u.root, c.Org)
	if err != nil {
		return ""
	}
	return claude.WorkflowPrompt(cfg.Workflow.Rules)
}

// columnOf is the title of card id's column, "" off the board.
func (u *ui) columnOf(id string) string {
	if ci, _ := u.b.Find(id); ci >= 0 {
		return u.b.Columns[ci].Title
	}
	return ""
}

// followTicket moves card id from column from to column to, where its
// ticket ref's status now (someone moved it) puts it, and takes that as
// the column the ticket matches; "" when the card had moved on already.
func (u *ui) followTicket(id, ref, url, status, from, to string) string {
	c := u.b.Card(id)
	if c == nil {
		return ""
	}
	for i, l := range c.Links {
		if l.URL == url {
			c.Links[i].State, c.Links[i].Seen = status, status
		}
	}
	ti := u.b.Column(to)
	if to == "" || ti < 0 || u.columnOf(id) != from || from == to {
		u.save()
		return ""
	}
	u.b.MoveBefore(id, ti, "")
	if c = u.b.Card(id); c != nil {
		c.Pushed = to
	}
	u.showToast(ref+" is "+status+": "+c.Key()+" follows to "+to, false)
	u.save()
	return to
}

// followColumn is the column ticket l in status puts its card, in column
// from, in: the workflow's (board order breaks a tie); else a GitHub
// issue's own, closed in Done and reopened out of it to In progress; ""
// stays.
func (u *ui) followColumn(w orgs.Workflow, l kanban.Link, status, from string) string {
	if to := w.ColumnOf(status, from, u.boardColumns()); to != "" || l.Kind != links.KindGitHubIssue {
		return to
	}
	switch {
	case strings.EqualFold(status, "closed"):
		return "Done"
	case strings.EqualFold(status, "open") && from == "Done":
		return "In progress"
	}
	return ""
}

// ticketTool is the card tools' ticket_status: card id's ticket (url's, else
// its only one) moved to a status by its Claude, as the organization's
// workflow says; the card follows to that status's column.
func (u *ui) ticketTool(id string, raw json.RawMessage) (string, bool) {
	var a struct{ Status, URL string }
	json.Unmarshal(raw, &a)
	c, err := onBoard(u, func() (kanban.Card, error) {
		if c := u.b.Card(id); c != nil {
			return *c, nil
		}
		return kanban.Card{}, errors.New("the card is gone from the board")
	})
	if err != nil {
		return err.Error(), false
	}
	cfg, err := orgs.Load(u.root, c.Org)
	switch {
	case err != nil:
		return err.Error(), false
	case !cfg.Workflow.On():
		return c.Org + " has no ticket workflow in desk: tickets follow the card's column. Ask the user to move it.", false
	case strings.TrimSpace(a.Status) == "":
		return "which status? Its name, as the tracker spells it", false
	}
	var ts []kanban.Link
	for _, l := range c.Links {
		if ticket(l) && (a.URL == "" || l.URL == a.URL || strings.EqualFold(l.Ref, a.URL)) {
			ts = append(ts, l)
		}
	}
	if len(ts) != 1 {
		var refs []string
		for _, l := range c.Links {
			if ticket(l) {
				refs = append(refs, l.Ref+" "+l.URL)
			}
		}
		if len(refs) == 0 {
			return "the card has no ticket (a Jira, Linear or GitHub issue) linked", false
		}
		return "which ticket? Give its url: " + strings.Join(refs, "; "), false
	}
	l := ts[0]
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now, err := transitionTicket(ctx, l, a.Status, cfg)
	if le := (*vault.LockedError)(nil); errors.As(err, &le) {
		u.later(func() { u.vaultLocked(le) })
		return err.Error() + " (desk asks the user to unlock it; try again after)", false
	}
	if err != nil {
		return err.Error(), false
	}
	moved, _ := onBoard(u, func() (string, error) {
		from := u.columnOf(id)
		return u.followTicket(id, l.Ref, l.URL, now, from, u.followColumn(cfg.Workflow, l, now, from)), nil
	})
	text := fmt.Sprintf("%s is %s now.", l.Ref, now)
	if moved != "" {
		text += " The card moved to " + moved + "."
	}
	return text, true
}
