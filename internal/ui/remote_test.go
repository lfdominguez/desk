package ui

import (
	"image"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/term"
)

// TestRemoteControl: the menu's R turns it on and opens the card's terminal
// with --remote-control and the card's name; the terminal's Remote button
// turns it off and restarts it without; off with no terminal opens none; an
// archived card is left alone.
func TestRemoteControl(t *testing.T) {
	h := newHarness(t)
	var runs [][]string
	startTerm = func(cmd *exec.Cmd, w, ht int, notify func()) (*term.Term, error) {
		runs = append(runs, cmd.Args)
		return term.Start(prog("raw"), w, ht, notify)
	}
	defer func() { startTerm = term.Start }()
	on := func(s string) bool { _, ok := h.shown(s); return ok }
	c :=h.u.b.Add(0, kanban.Card{Title: "Fix login", Org: "Acme"})
	card := func() *kanban.Card { return h.u.b.Card(c.ID) } // Add's pointer goes stale
	h.u.menu = &cardMenu{id: c.ID, at: image.Pt(400, 200)}
	h.frame()
	if !on("Turn on Remote Control") {
		t.Fatal("menu: no Turn on Remote Control")
	}
	h.frame()
	h.send(press("R", 0))
	name := c.Key() + " Fix login"
	if !card().Remote || len(runs) != 1 || h.u.view != "term:"+c.ID {
		t.Fatalf("on: remote %v, runs %d, view %q", card().Remote, len(runs), h.u.view)
	}
	if i := slices.Index(runs[0], "--remote-control"); i < 0 || i+1 >= len(runs[0]) || runs[0][i+1] != name {
		t.Fatalf("on: args %q", runs[0])
	}
	if h.u.toastBad || !strings.HasPrefix(h.u.toast, "Remote Control on") {
		t.Fatalf("on: toast %q bad %v", h.u.toast, h.u.toastBad)
	}
	first := h.u.terms[c.ID].t
	h.frame()
	if !on("Remote · on") {
		t.Fatal("header: no Remote · on")
	}
	h.clickButton("termRemote")
	if card().Remote || len(runs) != 2 || slices.Contains(runs[1], "--remote-control") || h.u.terms[c.ID] == nil || h.u.terms[c.ID].t == first {
		t.Fatalf("off: remote %v, runs %q", card().Remote, runs)
	}
	if h.u.toastBad || h.u.toast != "Remote Control off" {
		t.Fatalf("off: toast %q bad %v", h.u.toast, h.u.toastBad)
	}
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if done, _ := first.Exited(); done {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the old terminal still runs")
		}
	}
	h.frame()
	if !on("Remote") || on("Remote · on") {
		t.Fatal("header: not Remote off")
	}
	h.u.closeTerm(c.ID)
	card().Remote = true
	h.u.remoteControl(c.ID) // off, no terminal: none opens
	if card().Remote || len(runs) != 2 || h.u.terms[c.ID] != nil {
		t.Fatalf("off without terminal: remote %v, runs %d", card().Remote, len(runs))
	}
	h.u.menu = &cardMenu{id: c.ID, at: image.Pt(400, 200)}
	card().Remote = true
	h.frame()
	if !on("Turn off Remote Control") {
		t.Fatal("menu: no Turn off Remote Control")
	}
	h.u.menu = nil
	card().Remote = false
	h.u.archiveCard(c.ID)
	h.u.remoteControl(c.ID)
	if card().Remote || len(runs) != 2 {
		t.Fatal("archived card turned on")
	}
	h.u.remoteControl("nope") // gone: nothing
}
