package ui

import (
	"context"
	"time"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
)

// logWork records a card's work time on its ticket (links.LogWork); tests
// replace it.
var logWork = links.LogWork

// logTime logs card c's work time not logged yet on its ticket, off the
// UI goroutine, as it reaches Done (syncTickets). Tried once per stretch
// of work: Logged moves up whatever the answer, so a failing tracker isn't
// asked on every save.
func (u *ui) logTime(c *kanban.Card) {
	l := ticketOf(c)
	if l == nil {
		return
	}
	id, key, org, link := c.ID, c.Key(), c.Org, *l
	work, d := c.Session.Work, time.Duration(c.Session.Work-c.Logged)*time.Second
	note := key + ": " + c.Title
	u.logging[id] = true
	go func() {
		cfg, err := orgs.Load(u.root, org)
		logged := false
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			logged, err = logWork(ctx, link, d, note, cfg)
			cancel()
		}
		u.later(func() {
			delete(u.logging, id)
			if c := u.b.Card(id); c != nil {
				c.Logged = work
				u.save()
			}
			switch {
			case err != nil:
				u.showToast(key+": work time not logged on "+link.Ref+": "+firstLine(err.Error()), true)
			case logged:
				u.showToast(key+": logged "+links.Spent(d)+" on "+link.Ref, false)
			}
		})
	}()
}
