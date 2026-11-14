package ui

import (
	"fmt"
	"testing"

	"gioui.org/app"
	"gioui.org/io/key"
	"gioui.org/op"

	"github.com/lfdominguez/desk/internal/kanban"
)

// askClose sends the window's close request; aborted when desk kept it open.
func (h *harness) askClose() (aborted bool) {
	e := &app.ClosingEvent{}
	h.u.event(e, new(op.Ops))
	h.frame()
	return *e != (app.ClosingEvent{})
}

// working puts n cards on the board with Claude at work.
func (h *harness) working(n int) {
	for i := range n {
		h.u.b.Add(0, kanban.Card{Title: fmt.Sprint("task ", i), Status: kanban.StatusWorking})
	}
}

func TestExitIdleCloses(t *testing.T) {
	h := newHarness(t)
	h.u.b.Add(0, kanban.Card{Title: "resting"})
	if h.askClose() || h.u.exit != nil {
		t.Error("nothing at work: the window closes without asking")
	}
}

func TestExitAsksWhileWorking(t *testing.T) {
	h := newHarness(t)
	w := fakeWindows(t)
	h.working(1)
	if !h.askClose() || h.u.exit == nil {
		t.Fatal("a card at work: the close waits for an answer")
	}
	if _, ok := h.shown("1 card has Claude at work. Exiting stops it."); !ok {
		t.Error("the dialog says how many")
	}
	if !h.askClose() { // asked again meanwhile: still kept open, one dialog
		t.Error("a second close request while asking")
	}
	h.send(press(key.NameEscape, 0))
	if h.u.exit != nil || w.closed != 0 {
		t.Errorf("esc stays: asking %v, closed %d", h.u.exit != nil, w.closed)
	}
	h.askClose()
	h.send(press(key.NameReturn, 0))
	if h.u.exit != nil || w.closed != 1 || !h.u.exitOK {
		t.Fatalf("enter exits: asking %v, closed %d", h.u.exit != nil, w.closed)
	}
	if h.askClose() {
		t.Error("the close that follows goes through")
	}
}

func TestExitButtons(t *testing.T) {
	h := newHarness(t)
	w := fakeWindows(t)
	h.working(2)
	h.askClose()
	h.clickTop("exitCancel")
	if h.u.exit != nil || w.closed != 0 {
		t.Error("Cancel stays")
	}
	h.askClose()
	h.clickTop("exitOK")
	if h.u.exit != nil || w.closed != 1 {
		t.Error("Exit anyway closes the window")
	}
}

func TestExitNamesTheCards(t *testing.T) {
	h := newHarness(t)
	h.working(exitShown + 2)
	h.askClose()
	if _, ok := h.shown(fmt.Sprintf("%d cards have Claude at work. Exiting stops it.", exitShown+2)); !ok {
		t.Error("the count")
	}
	if _, ok := h.shown("and 2 more"); !ok {
		t.Error("past the first few, how many more")
	}
}

func TestExitWorkEnds(t *testing.T) {
	h := newHarness(t)
	h.working(1)
	h.askClose()
	h.only().Status = kanban.StatusWaiting
	h.frame()
	if h.u.exit != nil {
		t.Error("the work ended meanwhile: nothing left to ask")
	}
}
