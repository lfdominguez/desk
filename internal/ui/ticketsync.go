package ui

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/telemetry"
	"github.com/lfdominguez/desk/internal/vault"
)

// pushTicket is links.Push; tests replace it.
var pushTicket = links.Push

// ticket is a link the card's column can move: a Jira issue, a GitHub issue.
func ticket(l kanban.Link) bool { return tracked(l) || l.Kind == links.KindGitHubIssue }

// tracked says l is a tracker's issue (Jira, Linear): one with a key.
func tracked(l kanban.Link) bool { return l.Kind == links.KindJira || l.Kind == links.KindLinear }

// syncTickets moves, off the UI goroutine, the tickets of every card whose
// column differs from the one they were last moved to match (Pushed), or of
// every card with all (the background check: the remote may have drifted,
// or did while desk was closed). Push does nothing to a ticket in step.
func (u *ui) syncTickets(all bool) {
	for ci := range u.b.Columns {
		col := u.b.Columns[ci].Title
		for j := range u.b.Columns[ci].Cards {
			c := &u.b.Columns[ci].Cards[j]
			if c.Pushed != col {
				delete(u.pushErr, c.ID) // moved: a failure is news again
			}
			if col == "Done" && c.Session.Work > c.Logged && !u.logging[c.ID] {
				u.logTime(c)
			}
			if slices.ContainsFunc(c.Links, ticket) && (all || c.Pushed != col) && !u.pushing[c.ID] {
				u.pushing[c.ID] = true
				go u.push(c.ID, c.Key(), c.Org, col, slices.Clone(c.Links), c.Pushed == col) // in step as far as desk knows: the check's
			}
		}
	}
}

// push moves card id's tickets ls to match column col, each through its
// organization's account, and says what each one is now. check is the
// background check's (syncTicket): an organization's workflow then has the
// card follow a ticket someone moved instead.
func (u *ui) push(id, key, org, col string, ls []kanban.Link, check bool) {
	defer u.busy(org)()
	cfg, err := orgs.Load(u.root, org)
	retry := false // a locked vault: the next save tries again, once it is unlocked
	failed := err != nil
	followed := false           // the card follows a ticket: followTicket keeps Pushed
	seen := map[string]string{} // each ticket's status as read, by url: the next check's Seen
	for _, l := range ls {
		if err != nil || !ticket(l) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		var s links.Synced
		var perr error
		if check {
			s, perr = syncTicket(ctx, l, col, cfg)
		} else {
			s.Now, perr = pushTicket(ctx, l, col, cfg)
		}
		cancel()
		now := s.Now
		if s.Seen != "" {
			seen[l.URL] = s.Seen
		}
		var locked *vault.LockedError
		var na *links.NoAccountError
		switch {
		case errors.As(perr, &locked):
			retry = true
			u.later(func() { u.vaultLocked(locked) })
		case errors.As(perr, &na): // no account to move it with: nothing to do, nothing to say
		case perr != nil:
			failed = true
			telemetry.Report("ticket move", perr)
			u.later(func() { u.pushFailed(id, key+": couldn't move "+l.Ref+": "+firstLine(perr.Error())) })
		case s.Moved && !followed:
			followed = true
			u.later(func() { u.followTicket(id, l.Ref, l.URL, s.Seen, col, u.followColumn(cfg.Workflow, l, s.Seen, col)) })
		case now != "":
			u.later(func() { u.showToast(l.Ref+" → "+now, false) })
		}
	}
	if err != nil {
		u.later(func() { u.pushFailed(id, key+": tickets not moved: "+firstLine(err.Error())) })
	}
	u.later(func() {
		delete(u.pushing, id)
		if !failed {
			delete(u.pushErr, id) // in step again: a new failure is news
		}
		c := u.b.Card(id)
		if c != nil {
			for i, l := range c.Links {
				if st, ok := seen[l.URL]; ok {
					c.Links[i].Seen = st
				}
			}
		}
		if retry { // no save here: it would try again at once, still locked
			if c != nil {
				c.Pushed = "" // out of step: the first save after unlocking tries again
			}
			return
		}
		if c != nil && !followed {
			c.Pushed = col // given up on a failure too: a toast said so, and the next move tries again
		}
		u.save()
		go u.checkLinks([]string{id}) // the chips show the new states
	})
}

// pushFailed says why card id's tickets didn't move, unless the last check
// said just that: the background one tries every few minutes.
func (u *ui) pushFailed(id, msg string) {
	if u.pushErr[id] != msg {
		u.pushErr[id] = msg
		u.showToast(msg, true)
	}
}
