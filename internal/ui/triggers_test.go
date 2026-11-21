package ui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
)

// cardTitled is the board's card titled t, nil none.
func (h *harness) cardTitled(t string) *kanban.Card {
	for _, c := range h.u.boardCards() {
		if c.Title == t {
			return h.u.b.Card(c.ID)
		}
	}
	return nil
}

// TestTriggerTicket: a new ticket of the project makes its card (started),
// once; the first look only notes what's there, and a ticket with a card
// or of another project is left alone.
func TestTriggerTicket(t *testing.T) {
	h := newHarness(t)
	h.orgToml("Acme", "[[trigger]]\nname = \"new tickets\"\non = \"ticket\"\nproject = \"ABC\"\nstart = true\n\n[[trigger]]\nname = \"\"\non = \"ticket\"\n\n[[trigger]]\nname = \"off\"\non = \"ticket\"\noff = true\n")
	h.orgToml("Globex", "not toml [")
	tk := func(key, project string) links.Ticket {
		return links.Ticket{Key: key, Project: project, Title: "Fix " + key, URL: "https://globex.atlassian.net/browse/" + key}
	}
	h.u.tickets["Acme"] = []links.Ticket{tk("ABC-1", "ABC")}
	h.u.runTriggers()
	if h.cardTitled("ABC-1 Fix ABC-1") != nil {
		t.Fatal("the first look made a card")
	}
	h.u.b.Add(0, kanban.Card{Title: "mine", Org: "Acme", Links: []kanban.Link{{URL: tk("ABC-3", "ABC").URL}}})
	h.u.tickets["Acme"] = []links.Ticket{tk("ABC-1", "ABC"), tk("ABC-2", "ABC"), tk("XYZ-1", "XYZ"), tk("ABC-3", "ABC")}
	h.u.runTriggers()
	h.u.runTriggers()
	c := h.cardTitled("ABC-2 Fix ABC-2")
	if c == nil || c.Org != "Acme" || len(c.Links) != 1 || !strings.Contains(c.Notes, "(Made by the trigger new tickets.)") || h.u.procs[c.ID] == nil {
		t.Fatalf("%+v", c)
	}
	if h.cards() != 2 || h.cardTitled("XYZ-1 Fix XYZ-1") != nil {
		t.Fatalf("cards %d", h.cards())
	}
	b, _ := os.ReadFile(h.u.trigFile("Acme"))
	if !strings.Contains(string(b), "ABC-2") {
		t.Fatalf("kept %s", b)
	}
}

// TestTriggerCI: a new failed run on a card's link tells its Claude, once;
// a card in Done isn't told.
func TestTriggerCI(t *testing.T) {
	h := newHarness(t)
	h.orgToml("Acme", "[[trigger]]\nname = \"ci\"\non = \"ci_failed\"\n")
	c := h.u.b.Add(h.u.b.Column("Review"), kanban.Card{Title: "x", Org: "Acme"})
	done := h.u.b.Add(h.u.b.Column("Done"), kanban.Card{Title: "old", Org: "Acme"})
	fk := &fakeClaude{}
	h.u.procs[c.ID] = fk
	l := kanban.Link{URL: "https://github.com/acme/web/pull/7", Ref: "PR #7"}
	run := func(id int64, st string) links.Run { return links.Run{ID: id, Name: "test", Status: st, URL: "https://ci.example.com/1"} }
	h.u.ci["Acme"] = []ciItem{{id: c.ID, link: l, runs: []links.Run{run(1, links.CIFailed)}}}
	h.u.runTriggers()
	h.u.ci["Acme"] = []ciItem{{id: c.ID, link: l, runs: []links.Run{run(1, links.CIFailed), run(2, links.CIFailed), run(3, links.CISuccess)}},
		{id: done.ID, link: l, runs: []links.Run{run(4, links.CIFailed)}}, {id: "gone", link: l, runs: []links.Run{run(5, links.CIFailed)}}}
	h.u.runTriggers()
	h.u.runTriggers()
	if len(fk.prompts) != 1 || !strings.Contains(fk.prompts[0], "CI failed on PR #7: test") {
		t.Fatalf("prompts %q", fk.prompts)
	}
}

// TestTriggerAlert: an error that has lasted long enough, matching, makes
// an investigation card; warnings only when asked, and acknowledged ones
// never.
func TestTriggerAlert(t *testing.T) {
	h := newHarness(t)
	h.orgToml("Acme", "[[trigger]]\nname = \"db down\"\non = \"alert\"\nmatch = \"db\"\nafter = 10\n")
	now := time.Now()
	al := func(host, state string, ago time.Duration, acked bool) links.Alert {
		return links.Alert{Host: host, Service: "PING", State: state, Since: now.Add(-ago), Acked: acked, Output: "down"}
	}
	h.u.alerts["Acme"] = []links.Alert{al("db1", "critical", time.Hour, false)}
	h.u.runTriggers()
	h.u.alerts["Acme"] = []links.Alert{al("db1", "critical", time.Hour, false), al("db2", "critical", time.Hour, false), al("db3", "critical", time.Minute, false),
		al("db4", "warning", time.Hour, false), al("db5", "critical", time.Hour, true), al("web1", "critical", time.Hour, false)}
	h.u.runTriggers()
	c := h.cardTitled("Investigate CRITICAL db2 / PING")
	if c == nil || !strings.Contains(c.Notes, "(Made by the trigger db down.)") || h.cards() != 1 {
		t.Fatalf("%+v, %d cards", c, h.cards())
	}
	h.orgToml("Acme", "[[trigger]]\nname = \"db down\"\non = \"alert\"\nmatch = \"db\"\nwarnings = true\n")
	h.u.runTriggers()
	if h.cardTitled("Investigate WARNING db4 / PING") == nil || h.cardTitled("Investigate CRITICAL db3 / PING") == nil {
		t.Fatal("warnings or a young alert not made with warnings on and no wait")
	}
}

// TestTriggerSchedule: a card at its time on its days, once a day; the
// first look on its day only notes it.
func TestTriggerSchedule(t *testing.T) {
	h := newHarness(t)
	h.orgToml("Acme", "[[trigger]]\nname = \"audit\"\non = \"schedule\"\nat = \"09:00\"\ndays = [\"Mon\"]\ntitle = \"Dependency audit\"\nnotes = \"Check the dependencies.\"\ncolumn = \"Backlog\"\n")
	defer func() { trigNow = time.Now }()
	at := func(s string) { trigNow = func() time.Time { t, _ := time.ParseInLocation("2006-01-02 15:04", s, time.Local); return t } }
	at("2026-11-23 09:05") // a Monday
	h.u.runTriggers()
	if h.cardTitled("Dependency audit") != nil {
		t.Fatal("the first look made it")
	}
	at("2026-11-24 09:30") // Tuesday
	h.u.runTriggers()
	at("2026-11-30 08:59")
	h.u.runTriggers()
	if h.cardTitled("Dependency audit") != nil {
		t.Fatal("made off its day or time")
	}
	at("2026-11-30 09:00")
	h.u.runTriggers()
	h.u.runTriggers()
	c := h.cardTitled("Dependency audit")
	if col, _ := h.u.b.Find(c.ID); c == nil || h.u.b.Columns[col].Title != "Backlog" || !strings.HasPrefix(c.Notes, "Check the dependencies.") || h.cards() != 1 {
		t.Fatalf("%+v", c)
	}
}

// TestTriggerOdd: an unknown kind says so; a triggers.json that can't be
// written says so; a card with no title is named after its trigger.
func TestTriggerOdd(t *testing.T) {
	h := newHarness(t)
	h.orgToml("Acme", "[[trigger]]\nname = \"odd\"\non = \"weather\"\n")
	h.u.runTriggers()
	if !strings.Contains(h.u.toast, "on = weather?") {
		t.Fatalf("toast %q", h.u.toast)
	}
	h.orgToml("Acme", "[[trigger]]\nname = \"daily\"\non = \"schedule\"\nat = \"00:00\"\n")
	os.MkdirAll(h.u.trigFile("Acme"), 0o755) // a folder where the file goes
	h.u.runTriggers()
	if !strings.Contains(h.u.toast, "Triggers:") {
		t.Fatalf("toast %q", h.u.toast)
	}
	os.Remove(h.u.trigFile("Acme"))
	h.u.runTriggers() // first look today
	defer func() { trigNow = time.Now }()
	trigNow = func() time.Time { return time.Now().Add(24 * time.Hour) }
	h.u.runTriggers()
	if h.cardTitled("daily") == nil {
		t.Fatal("no card named after its trigger")
	}
}
