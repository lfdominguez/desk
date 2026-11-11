package ui

import (
	"errors"
	"image"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/lfdominguez/desk/internal/kanban"
)

// windows records what detach.go asked of the platform: tests open no window.
type windows struct{ started, raised, closed int }

func fakeWindows(t *testing.T) *windows {
	w := &windows{}
	s, r, c := startDetached, raise, closeWindow
	t.Cleanup(func() { startDetached, raise, closeWindow = s, r, c })
	startDetached = func(*ui, *detached) { w.started++ }
	raise = func(*app.Window) { w.raised++ }
	closeWindow = func(*app.Window) { w.closed++ }
	return w
}

// dwin drives a detached window's frames through its own router.
type dwin struct {
	h   *harness
	d   *detached
	r   input.Router
	ops op.Ops
}

func (w *dwin) frame() {
	w.ops.Reset()
	gtx := layout.Context{Ops: &w.ops, Source: w.r.Source(), Now: time.Now(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(1000, 900))}
	w.h.u.detachedFrame(gtx, w.d)
	w.r.Frame(&w.ops)
}

func (w *dwin) send(evs ...event.Event) {
	for _, e := range evs {
		w.r.Queue(e)
	}
	w.frame()
	w.frame()
}

// click finds the window's button id (its kit id, scoped) and clicks it.
func (w *dwin) click(id string) {
	w.h.t.Helper()
	c := w.h.u.k.Click("w" + w.d.id + ":" + id)
	w.frame()
	var at []image.Point
	for _, n := range w.r.AppendSemantics(nil) {
		if n.Desc.Gestures&input.ClickGesture != 0 {
			at = append(at, n.Desc.Bounds.Min.Add(n.Desc.Bounds.Max).Div(2))
		}
	}
	slices.SortStableFunc(at, func(a, b image.Point) int { return a.Y - b.Y })
	for _, p := range at {
		pos := f32.Pt(float32(p.X), float32(p.Y))
		w.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: pos})
		w.frame()
		if c.Hovered() {
			w.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: pos},
				pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: pos})
			return
		}
	}
	w.h.t.Fatalf("no button %s in the detached window", id)
}

// TestDetachAndBack: a card's session goes to its own window with what was
// written for it; the main window shows where it went; its window has its
// own message box and keyboard; Ctrl+Shift+O puts it back, draft and all.
func TestDetachAndBack(t *testing.T) {
	h := newHarness(t)
	wins := fakeWindows(t)
	id := h.u.b.Add(0, kanban.Card{Title: "a"}).ID
	h.u.openPanel(id)
	h.frame()
	h.u.chatIn.SetText("half written")
	h.send(press("O", key.ModShortcut|key.ModShift))
	d := h.u.detached[id]
	if d == nil || wins.started != 1 || !h.u.b.Card(id).Detached || h.u.chatIn.Text() != "" || h.u.b.Card(id).Draft != "half written" {
		t.Fatalf("detached: %v, started %d, main box %q, draft %q", d != nil, wins.started, h.u.chatIn.Text(), h.u.b.Card(id).Draft)
	}
	if !h.hasButton("dtBack" + id) {
		t.Error("the main window says where the session went")
	}
	h.u.detach(id) // again: its window comes up
	if wins.raised != 1 {
		t.Error("a second detach raises the window")
	}
	w := &dwin{h: h, d: d}
	w.frame()
	w.frame()
	if d.p.chatIn.Text() != "half written" || !w.r.Source().Focused(&d.p.chatIn) {
		t.Errorf("the window's box: %q, focused %v", d.p.chatIn.Text(), w.r.Source().Focused(&d.p.chatIn))
	}
	if h.u.pane != &h.u.mainPane || h.u.k.Scope != "" {
		t.Error("the main pane and kit scope are back after the window's frame")
	}
	d.p.chatIn.SetText("half written, and more")
	w.send(press("O", key.ModShortcut|key.ModShift))
	if h.u.detached[id] != nil || h.u.b.Card(id).Detached || wins.closed != 1 || h.u.b.Card(id).Draft != "half written, and more" {
		t.Fatalf("back: detached %v, flag %v, closed %d, draft %q", h.u.detached[id] != nil, h.u.b.Card(id).Detached, wins.closed, h.u.b.Card(id).Draft)
	}
	h.frame()
	if h.u.chatIn.Text() != "half written, and more" {
		t.Errorf("the main box has the window's text: %q", h.u.chatIn.Text())
	}
}

// TestDetachFromButtons: the session header's pop-out, the card menu's
// Open in new window and the placeholder's buttons; in the window, × puts
// the session back.
func TestDetachFromButtons(t *testing.T) {
	h := newHarness(t)
	wins := fakeWindows(t)
	id := h.u.b.Add(0, kanban.Card{Title: "a"}).ID
	h.u.openPanel(id)
	h.clickButton("pnDetach")
	if h.u.detached[id] == nil {
		t.Fatal("the header's pop-out detaches")
	}
	h.clickButton("dtShow" + id)
	if wins.raised != 1 {
		t.Error("Show window raises it")
	}
	h.clickButton("dtBack" + id)
	if h.u.detached[id] != nil || wins.closed != 1 {
		t.Fatal("Bring it back here reattaches")
	}
	h.u.panel = ""
	h.u.menu, h.u.sel = &cardMenu{id: id, at: image.Pt(400, 200)}, id
	h.frame() // draws the items; the next frame listens for their keys
	h.frame()
	h.send(press("O", 0))
	d := h.u.detached[id]
	if d == nil {
		t.Fatal("the menu's O detaches")
	}
	w := &dwin{h: h, d: d}
	w.click("pnClose")
	if h.u.detached[id] != nil {
		t.Error("the window's × reattaches")
	}
}

// TestDetachedKeys: esc in a detached window leaves a subagent, then the
// rail; the session view in the main window shows the placeholder.
func TestDetachedKeys(t *testing.T) {
	h := newHarness(t)
	fakeWindows(t)
	id := h.u.b.Add(0, kanban.Card{Title: "a"}).ID
	h.u.sel = id
	h.u.view = "sess:" + id
	h.frame()
	if h.u.sessionCard() != id {
		t.Fatal("the session view's card")
	}
	h.u.view = ""
	if h.u.sessionCard() != id {
		t.Fatal("else the selected card")
	}
	h.u.view = "sess:" + id
	h.u.detach(id)
	d := h.u.detached[id]
	h.u.mainPane.rail = true
	if !h.hasButton("dtBack" + id) {
		t.Error("the session view shows the placeholder")
	}
	w := &dwin{h: h, d: d}
	w.frame() // its keys listen from its first frame
	d.p.sub, d.p.rail = "x", true
	w.send(press(key.NameEscape, 0))
	if d.p.sub != "" || !d.p.rail {
		t.Fatalf("esc leaves the subagent first: %q %v", d.p.sub, d.p.rail)
	}
	w.send(press(key.NameEscape, 0))
	if d.p.rail {
		t.Fatal("then the rail")
	}
	w.send(press("=", key.ModShortcut)) // the text size: taken, nothing else
	if h.u.detached[id] == nil {
		t.Error("zoom keys don't reattach")
	}
	h.u.b.Delete(id)
	w.frame() // a card gone: its window says so
}

// TestDetachedWindowEvents: the window's own loop, through a fake event
// source: focus, a frame, wakeups running queued work, a lost display
// reopening, the user's close reattaching, and desk's quit leaving it be.
func TestDetachedWindowEvents(t *testing.T) {
	h := newHarness(t)
	fakeWindows(t)
	ended := make(chan time.Time)
	close(ended)
	defer func(w func() <-chan time.Time) { reopenWait = w }(reopenWait)
	reopenWait = func() <-chan time.Time { return ended }
	id := h.u.b.Add(0, kanban.Card{Title: "a"}).ID
	h.u.detach(id)
	d := h.u.detached[id]
	d.w = nil // no platform window: Invalidate and Option would need one
	ran, framed := 0, false
	r := new(input.Router)
	fe := app.FrameEvent{Now: time.Now(), Size: image.Pt(800, 600), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Source: r.Source(),
		Frame: func(*op.Ops) { framed = true }}
	lost := errors.New("wayland: display file descriptor gone")
	evs := []event.Event{app.ConfigEvent{Config: app.Config{Focused: true}}, wakeup{}, fe, app.DestroyEvent{Err: lost}}
	i := 0
	h.u.later(func() { ran++ })
	d.w = new(app.Window) // options only: no Event, no platform window
	h.u.runDetached(d, func() event.Event {
		if i == len(evs) {
			h.u.reattach(id) // meanwhile: the lost window isn't reopened
			return app.DestroyEvent{}
		}
		i++
		return evs[i-1]
	})
	if !d.p.focused || ran != 1 || !framed || h.u.detached[id] != nil {
		t.Errorf("focused %v, work ran %d, framed %v, still detached %v", d.p.focused, ran, framed, h.u.detached[id] != nil)
	}
	log, _ := os.ReadFile(filepath.Join(filepath.Dir(h.u.path), "crash.log"))
	if !strings.Contains(string(log), "lost, reopening: "+lost.Error()) {
		t.Errorf("crash.log: %q", log)
	}

	h.u.detach(id) // the user closes it: back in the main window
	d = h.u.detached[id]
	if h.u.detachedEvent(d, app.DestroyEvent{}) != winDone || h.u.detached[id] != nil {
		t.Error("closing the window reattaches")
	}
	h.u.detach(id) // lost, and reattached before it reopens
	d = h.u.detached[id]
	n := 0
	h.u.runDetached(d, func() event.Event {
		if n++; n == 2 {
			return app.DestroyEvent{}
		}
		h.u.reattach(id)
		return app.DestroyEvent{Err: lost}
	})
	h.u.detach(id) // desk quits: the window closes, the card stays detached
	d = h.u.detached[id]
	d.p.chatIn.SetText("for later")
	h.u.quitting()
	if h.u.detachedEvent(d, app.DestroyEvent{}) != winDone || !h.u.b.Card(id).Detached || h.u.b.Card(id).Draft != "for later" {
		t.Errorf("a quit keeps it detached with its text: %v %q", h.u.b.Card(id).Detached, h.u.b.Card(id).Draft)
	}
	if h.u.detachedEvent(d, app.DestroyEvent{Err: lost}) != winDone {
		t.Error("a lost display while quitting ends the window")
	}
}

// TestReopenDetached: a card detached when desk closed gets its window
// again at start; detaching an unknown card does nothing.
func TestReopenDetached(t *testing.T) {
	h := newHarness(t)
	wins := fakeWindows(t)
	id := h.u.b.Add(0, kanban.Card{Title: "a", Detached: true}).ID
	h.u.b.Add(0, kanban.Card{Title: "b"})
	h.u.reopenDetached()
	if wins.started != 1 || h.u.detached[id] == nil {
		t.Errorf("reopened %d", wins.started)
	}
	h.u.detach("nope")
	h.u.reattach("nope")
	if len(h.u.detached) != 1 {
		t.Error("unknown cards change nothing")
	}
	d := h.u.detached[id]
	d.p.focused = true
	h.u.b.Card(id).Status = kanban.StatusWaiting
	told := 0
	desktopNotify = func(string, string) { told++ }
	h.u.notifyChange(h.u.b.Card(id), kanban.StatusWorking)
	if told != 0 {
		t.Error("no notification for the card in its own window, in front")
	}
}

// TestDetachDefaults: the real platform calls take a window not yet open
// (they queue for it), so they run here without a display.
func TestDetachDefaults(t *testing.T) {
	w := new(app.Window)
	raise(w)
	closeWindow(w)
}

// TestQuitKeepsDetached: desk quitting keeps a detached window's text and
// leaves the card detached for the next start.
func TestQuitKeepsDetached(t *testing.T) {
	h := newHarness(t)
	fakeWindows(t)
	id := h.u.b.Add(0, kanban.Card{Title: "a"}).ID
	h.u.detach(id)
	h.u.detached[id].p.chatIn.SetText("kept")
	h.u.quitting()
	b, err := kanban.Load(h.u.path)
	if err != nil || b.Card(id) == nil || !b.Card(id).Detached || b.Card(id).Draft != "kept" || !h.u.closing {
		t.Errorf("after quitting: %v", err)
	}
}
