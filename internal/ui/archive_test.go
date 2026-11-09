package ui

import (
	"fmt"
	"image"
	"slices"
	"testing"
	"time"

	"gioui.org/layout"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
)

func groupNames(gs []archGroup) (out []string) {
	for _, g := range gs {
		out = append(out, g.name)
		for _, e := range g.es {
			out = append(out, " "+e.c.Title)
		}
	}
	return out
}

// TestArchGroupsByDate: newest first, in Today, Yesterday, This week, This
// month and then one section per earlier month.
func TestArchGroupsByDate(t *testing.T) {
	now := time.Date(2026, 11, 20, 15, 0, 0, 0, time.Local)
	at := func(title string, t time.Time) archEntry { return archEntry{c: kanban.Card{Title: title, Finished: t}} }
	es := []archEntry{
		at("older", now.AddDate(0, -2, 0)),
		at("week", now.AddDate(0, 0, -6)),
		at("today-early", time.Date(2026, 11, 20, 0, 1, 0, 0, time.Local)),
		at("month", now.AddDate(0, 0, -7)),
		at("today", now.Add(-time.Hour)),
		at("yesterday", time.Date(2026, 11, 19, 23, 59, 0, 0, time.Local)),
		at("last-month", now.AddDate(0, -1, 0)),
		at("last-year", time.Date(2025, 11, 20, 0, 0, 0, 0, time.Local)),
	}
	want := []string{
		"Today", " today", " today-early",
		"Yesterday", " yesterday",
		"This week", " week",
		"This month", " month",
		"October 2026", " last-month",
		"September 2026", " older",
		"November 2025", " last-year",
	}
	if got := groupNames(archGroups(es, false, now)); !slices.Equal(got, want) {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

// TestArchGroupsByOrg: one section per organization, alphabetical, the
// cards with none last.
func TestArchGroupsByOrg(t *testing.T) {
	now := time.Now()
	es := []archEntry{
		{c: kanban.Card{Title: "n", Finished: now}},
		{c: kanban.Card{Title: "g1", Org: "Globex", Finished: now.Add(-2 * time.Hour)}},
		{c: kanban.Card{Title: "a", Org: "Acme", Finished: now}},
		{c: kanban.Card{Title: "g2", Org: "Globex", Finished: now.Add(-time.Hour)}},
		{c: kanban.Card{Title: "i", Org: "Initech", Finished: now}},
	}
	want := []string{"Acme", " a", "Globex", " g2", " g1", "Initech", " i", "No organization", " n"}
	if got := groupNames(archGroups(es, true, now)); !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}

// TestArchChips: Done and Archived, the organizations alphabetical, the six
// most used labels, each with its count.
func TestArchChips(t *testing.T) {
	es := []archEntry{
		{c: kanban.Card{Org: "Globex", Labels: []string{"a", "b"}}, done: true},
		{c: kanban.Card{Org: "Acme", Labels: []string{"b", "c", "d", "e", "f", "g"}}},
		{c: kanban.Card{Labels: []string{"b", "c"}}},
	}
	chips, n := archChips(es)
	want := []string{"done", "archived", "org:Acme", "org:Globex", "label:b", "label:c", "label:a", "label:d", "label:e", "label:f"}
	if !slices.Equal(chips, want) {
		t.Fatalf("chips %q", chips)
	}
	if n["done"] != 1 || n["archived"] != 2 || n["org:Globex"] != 1 || n["label:b"] != 3 || n["label:c"] != 2 || n["label:g"] != 1 {
		t.Fatalf("counts %v", n)
	}
}

// TestArchShows: Done and Archived turn their kind off; lit organizations
// and labels each narrow to cards with one of them.
func TestArchShows(t *testing.T) {
	acme := archEntry{c: kanban.Card{Org: "Acme", Labels: []string{"api"}}, done: true}
	globex := archEntry{c: kanban.Card{Org: "Globex", Labels: []string{"db"}}}
	var a archState
	if !a.shows(acme) || !a.shows(globex) {
		t.Fatal("no chips, yet hidden")
	}
	a.toggle("done")
	if a.shows(acme) || !a.shows(globex) {
		t.Fatal("Done off")
	}
	a.toggle("done")
	a.toggle("org:Globex")
	if a.shows(acme) || !a.shows(globex) {
		t.Fatal("org:Globex lit")
	}
	a.toggle("org:Acme")
	if !a.shows(acme) || !a.shows(globex) {
		t.Fatal("both orgs lit")
	}
	a.toggle("label:api")
	if !a.shows(acme) || a.shows(globex) {
		t.Fatal("label:api lit")
	}
	if a.ver != 5 {
		t.Fatalf("ver %d", a.ver)
	}
}

// TestArchChipLook: how chips read, and which show lit.
func TestArchChipLook(t *testing.T) {
	for chip, want := range map[string]string{"done": "Done", "archived": "Archived", "org:Acme": "Acme", "label:api": "#api"} {
		if got := archChipName(chip); got != want {
			t.Errorf("%s reads %q", chip, got)
		}
	}
	var a archState
	if !a.lit("done") || !a.lit("archived") || a.lit("org:Acme") || a.lit("label:api") {
		t.Fatal("at first: Done and Archived lit, the rest not")
	}
	a.toggle("archived")
	a.toggle("org:Acme")
	if !a.lit("done") || a.lit("archived") || !a.lit("org:Acme") {
		t.Fatal("after toggling Archived and Acme")
	}
}

// TestArchShown: two labels and links at most, how many labels more, and
// the cost only when there is one.
func TestArchShown(t *testing.T) {
	l := func(n int) kanban.Link { return kanban.Link{Ref: fmt.Sprint(n)} }
	labels, more, ls, cost := archShown(kanban.Card{Labels: []string{"a", "b", "c", "d"}, Links: []kanban.Link{l(1), l(2), l(3)}, Session: kanban.Session{Cost: 1.234}})
	if !slices.Equal(labels, []string{"a", "b"}) || more != 2 || len(ls) != 2 || ls[1].Ref != "2" || cost != "$1.23" {
		t.Fatalf("full card: %v %d %v %q", labels, more, ls, cost)
	}
	labels, more, ls, cost = archShown(kanban.Card{Labels: []string{"a"}})
	if !slices.Equal(labels, []string{"a"}) || more != 0 || len(ls) != 0 || cost != "" {
		t.Fatalf("bare card: %v %d %v %q", labels, more, ls, cost)
	}
}

// TestArchMatches: the Done column's cards come marked done, archived ones
// not, both by the search.
func TestArchMatches(t *testing.T) {
	h := newHarness(t)
	d := h.u.b.Add(h.u.b.Column("Done"), kanban.Card{Title: "Ship login"})
	a := h.u.b.Add(0, kanban.Card{Title: "Old login"})
	h.u.b.Add(0, kanban.Card{Title: "Still to do"})
	h.u.b.ArchiveCard(a.ID)
	es := h.u.archMatches()
	if len(es) != 2 || es[0].c.ID != d.ID || !es[0].done || es[1].c.ID != a.ID || es[1].done {
		t.Fatalf("%+v", es)
	}
	h.u.search.SetText("ship")
	if es := h.u.archMatches(); len(es) != 1 || es[0].c.ID != d.ID {
		t.Fatalf("searching: %+v", es)
	}
}

// TestArchiveBar:the chips and Group by change what the archive lists;
// a list the filters empty says so.
func TestArchiveBar(t *testing.T) {
	h := newHarness(t)
	done := h.u.b.Column("Done")
	pr := func(n int) kanban.Link {
		return kanban.Link{Ref: fmt.Sprint("PR #", n), State: "merged", Tone: links.ToneDone}
	}
	h.u.b.Add(done, kanban.Card{Title: "Ship login", Org: "Acme", Finished: time.Now(), Labels: []string{"api", "auth", "web"},
		Links: []kanban.Link{pr(1), pr(2), pr(3)}, Session: kanban.Session{Cost: 1.5}})
	old := h.u.b.Add(0, kanban.Card{Title: "Old idea", Org: "Globex"})
	h.u.b.ArchiveCard(old.ID)
	h.u.view = viewArchive
	sz := image.Pt(1200, 700)
	view := func(gtx layout.Context) layout.Dimensions { return h.u.archive(gtx) }
	count := func() int {
		h.u.archList.Position.Count = -1
		h.draw(sz, view)
		return h.u.archList.Position.Count
	}
	if n := count(); n != 3 { // Today, and its two cards
		t.Fatalf("at first: %d", n)
	}
	h.clickIn(sz, image.Rectangle{Max: sz}, view, "archGroup1")
	if !h.u.arch.byOrg || count() != 4 { // Acme, its card, Globex, its card
		t.Fatalf("by organization: %v %d", h.u.arch.byOrg, h.u.archList.Position.Count)
	}
	h.clickIn(sz, image.Rectangle{Max: sz}, view, "archChiporg:Globex")
	if n := count(); n != 2 {
		t.Fatalf("Globex only: %d", n)
	}
	h.clickIn(sz, image.Rectangle{Max: sz}, view, "archChiparchived")
	if n := count(); n != -1 {
		t.Fatalf("Globex, archived off: %d", n)
	}
	h.clickIn(sz, image.Rectangle{Max: sz}, view, "archGroup0")
	if h.u.arch.byOrg {
		t.Fatal("back by date")
	}
}
