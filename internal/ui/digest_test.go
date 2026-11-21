package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/tgbot"
)

// TestDigest: what was finished since then (archived too), then what is
// in review, blocked and in progress and moved since, by organization.
func TestDigest(t *testing.T) {
	h := newHarness(t)
	now := time.Now()
	add := func(col string, c kanban.Card) kanban.Card { return h.u.b.Add(h.u.b.Column(col), c) }
	done := add("Done", kanban.Card{Title: "Fix login", Org: "Acme", Finished: now, Session: kanban.Session{Work: 4800},
		Links: []kanban.Link{{Ref: "ABC-1"}, {Ref: ""}}})
	old := add("Done", kanban.Card{Title: "Old work", Org: "Acme", Finished: now.AddDate(0, 0, -1)})
	h.u.b.ArchiveCard(old.ID)
	add("Review", kanban.Card{Title: "Add audit", Org: "Acme"})
	add("Blocked", kanban.Card{Title: "Deploy", Org: "Globex", Status: kanban.StatusBlocked, Note: "waiting on access"})
	add("In progress", kanban.Card{Title: "Loose", Org: ""})
	stale := add("In progress", kanban.Card{Title: "Stale", Org: "Acme"})
	h.u.b.Card(stale.ID).Updated = now.AddDate(0, 0, -10)

	today := h.u.digestText("", digestSince(0, now))
	for _, s := range []string{"### Acme", "**Done**\n- " + done.Key() + " Fix login · ABC-1 · 1h 20m", "**In review**", "### Globex",
		"Deploy — waiting on access", "### No organization"} {
		if !strings.Contains(today, s) {
			t.Fatalf("no %q in\n%s", s, today)
		}
	}
	if strings.Contains(today, "Old work") || strings.Contains(today, "Stale") {
		t.Fatalf("too much:\n%s", today)
	}
	if !strings.Contains(h.u.digestText("Acme", digestSince(1, now)), "Old work") {
		t.Fatal("yesterday's archived card missing")
	}
	if !strings.Contains(h.u.digestText("Umbrella", digestSince(2, now)), "Nothing done or moving") {
		t.Fatal("empty not said")
	}
	if n := h.u.count(viewDigest); n != 1 {
		t.Fatalf("count %d", n)
	}

	h.u.view = viewDigest
	h.frame()
	if !h.has("Copy") || h.u.viewTitle() != "Digest" || !strings.Contains(h.u.sentence(), "standup") {
		var ls []string
		for _, l := range h.labels() {
			ls = append(ls, l.text)
		}
		t.Fatalf("view %q %q %q", h.u.viewTitle(), h.u.sentence(), ls)
	}
	h.clickTop("dgCopy")
	if h.u.toast != "Digest copied" {
		t.Fatalf("toast %q", h.u.toast)
	}
	h.u.dgSpan = 2
	h.frame()
}

// TestWorkTime: each turn's time, from the message (or Claude's own start)
// to its result, adds to the card's work.
func TestWorkTime(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	h.u.procs[c.ID] = &fakeClaude{}
	h.u.send(c.ID, "go")
	if h.u.turnAt[c.ID].IsZero() {
		t.Fatal("no clock")
	}
	h.u.turnAt[c.ID] = time.Now().Add(-90 * time.Second)
	h.u.onEvent(c.ID, 0, claude.Event{Kind: claude.KindResult})
	if w := h.u.b.Card(c.ID).Session.Work; w != 90 || !h.u.turnAt[c.ID].IsZero() {
		t.Fatalf("work %d", w)
	}
	h.u.onEvent(c.ID, 0, claude.Event{Kind: claude.KindInit, Session: "s"}) // a turn of its own
	if h.u.turnAt[c.ID].IsZero() {
		t.Fatal("its own turn not timed")
	}
}

// TestWorklog: reaching Done logs the work not logged yet on the card's
// ticket, once; a tracker's error and an account without worklog say so
// or nothing, and never ask again for the same work.
func TestWorklog(t *testing.T) {
	h := newHarness(t)
	defer func() { logWork = links.LogWork }()
	var calls []string
	var answer error
	logged := true
	logWork = func(_ context.Context, l kanban.Link, d time.Duration, note string, _ orgs.Config) (bool, error) {
		calls = append(calls, l.Ref+" "+d.String()+" "+note)
		return logged, answer
	}
	tk := []kanban.Link{{URL: "https://globex.atlassian.net/browse/ABC-1", Kind: links.KindJira, Ref: "ABC-1"}}
	c := h.u.b.Add(h.u.b.Column("Review"), kanban.Card{Title: "Fix", Org: "Acme", Links: tk, Session: kanban.Session{Work: 600}})
	h.u.moveTo(c.ID, "Done")
	h.u.save()
	h.pumpUntil("logged", func() bool { return h.u.b.Card(c.ID).Logged == 600 })
	if len(calls) != 1 || calls[0] != "ABC-1 10m0s "+c.Key()+": Fix" || !strings.Contains(h.u.toast, "logged 10m on ABC-1") {
		t.Fatalf("%q toast %q", calls, h.u.toast)
	}
	h.u.save()
	h.frame()
	if len(calls) != 1 {
		t.Fatal("logged twice")
	}

	answer = errors.New("403 worklogs off")
	h.u.b.Card(c.ID).Session.Work = 900
	h.u.save()
	h.pumpUntil("refused", func() bool { return h.u.b.Card(c.ID).Logged == 900 })
	if calls[1] != "ABC-1 5m0s "+c.Key()+": Fix" || !strings.Contains(h.u.toast, "not logged on ABC-1: 403") {
		t.Fatalf("%q toast %q", calls, h.u.toast)
	}

	answer, logged = nil, false
	n := h.u.b.Add(h.u.b.Column("Done"), kanban.Card{Title: "y", Org: "Acme", Links: tk, Session: kanban.Session{Work: 60}})
	plain := h.u.b.Add(h.u.b.Column("Done"), kanban.Card{Title: "z", Org: "Acme", Session: kanban.Session{Work: 60}})
	h.u.save()
	h.pumpUntil("noted", func() bool { return h.u.b.Card(n.ID).Logged == 60 })
	if h.u.b.Card(plain.ID).Logged != 0 || len(calls) != 3 {
		t.Fatalf("calls %q", calls)
	}
	h.u.logTime(&kanban.Card{ID: "gone", Org: "Acme", Links: tk, Session: kanban.Session{Work: 5}}) // a card that left
	h.pumpUntil("done", func() bool { return len(h.u.logging) == 0 })
}

// TestDigestTrigger: a digest trigger sends the organization's digest to
// the phone at its time; with no phone paired it says so.
func TestDigestTrigger(t *testing.T) {
	h := newHarness(t)
	f := newFakeTG(t)
	h.orgToml("Acme", "[[trigger]]\nname = \"standup\"\non = \"digest\"\nat = \"00:00\"\n")
	defer func() { trigNow = time.Now }()
	h.u.runTriggers() // the first look, today
	trigNow = func() time.Time { return time.Now().Add(24 * time.Hour) }
	h.u.runTriggers()
	if !strings.Contains(h.u.toast, "none is paired") {
		t.Fatalf("toast %q", h.u.toast)
	}
	h.u.phone = &phoneState{bot: &tgbot.Bot{Token: "T"}, chat: 42, asks: map[string]phoneAsk{}, cards: map[int64]string{}}
	trigNow = func() time.Time { return time.Now().Add(48 * time.Hour) }
	h.u.runTriggers()
	h.pumpUntil("sent", func() bool { b, _ := f.said("sendMessage", "Digest since"); return b != nil })
}
