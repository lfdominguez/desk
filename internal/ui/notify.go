package ui

import (
	"cmp"
	"strings"

	"gioui.org/x/notify"

	"github.com/lfdominguez/desk/internal/kanban"
)

// desktopNotify shows a desktop notification (freedesktop over D-Bus on
// Linux; on macOS UserNotifications, which shows none for a binary outside
// an .app bundle), off the UI goroutine; tests replace it.
var desktopNotify = func(title, body string) { go notify.Push(title, body) }

// needsYou is how a status that wants the user reads in a notification.
var needsYou = map[string]string{
	kanban.StatusWaiting: "Your turn", kanban.StatusBlocked: "Blocked",
	kanban.StatusFailed: "Failed", kanban.StatusReview: "Ready for review",
}

// notifyChange tells the desktop when card c turns to one that wants the
// user (its turn, blocked, failed, done), unless the user is looking at
// it already: the window focused with its session open.
func (u *ui) notifyChange(c *kanban.Card, before string) {
	what := needsYou[c.Status]
	if c.Status == before || what == "" || c.Note == "Stopped" { // a stop is the user's own doing
		return
	}
	if u.focused && (u.panel == c.ID || u.view == "sess:"+c.ID) {
		return
	}
	desktopNotify(c.Key()+" · "+what, c.Title+"\n"+unmark(cmp.Or(c.Note, c.Last)))
}

// unmark is markdown s as a notification shows it: its first paragraph,
// no emphasis, code ticks or list and heading marks.
func unmark(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n\n")
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		l = strings.TrimLeft(l, "#>-*+ ")
		out = append(out, l)
	}
	return strings.NewReplacer("**", "", "__", "", "`", "").Replace(strings.Join(out, " "))
}
