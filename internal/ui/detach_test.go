package ui

import (
	"errors"
	"fmt"
	"image"
	"io"
	"net"
	"os"
	"os/exec"
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

	dbus "github.com/godbus/dbus/v5"

	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/term"
	"github.com/lfdominguez/desk/internal/testwait"
)

// windows records what detach.go asked of the platform: tests open no window.
type windows struct{ started, raised, closed int }

func fakeWindows(t *testing.T) *windows {
	w := &windows{}
	s, r, c := startDetached, raise, closeWindow
	t.Cleanup(func() { startDetached, raise, closeWindow = s, r, c })
	startDetached = func(*ui, *detached) { w.started++ }
	raise = func(*detached) { w.raised++ }
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
		Frame: func(*op.Ops) {
			framed = true
			if !h.u.mu.TryLock() { // a swap the driver holds would stall the main window too
				t.Error("the detached frame is presented holding u.mu")
			} else {
				h.u.mu.Unlock()
			}
		}}
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
	h.u.detach(id) // lost, and reattached while it waits to reopen
	d = h.u.detached[id]
	reopenWait = func() <-chan time.Time { h.u.reattach(id); return ended }
	h.u.runDetached(d, func() event.Event { return app.DestroyEvent{Err: lost} })
	reopenWait = func() <-chan time.Time { return ended }
	if h.u.detached[id] != nil {
		t.Error("reattached meanwhile: not reopened")
	}
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
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "") // never the machine's Hyprland
	w := new(app.Window)
	raiseNow(&detached{w: w, title: "DESK-1 · a"})
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

// TestDetachedSplit: the infra session goes with a detached card's chat
// (the workspace is drawn in one window at a time); the main window's
// servers view says where it is and can take it back, which leaves that
// window the chat alone; reattaching gives it back too.
func TestDetachedSplit(t *testing.T) {
	h := newHarness(t)
	fakeWindows(t)
	localServer(t, h.u.root, "Acme")
	h.u.infra = infra.New(h.u.root, func() {})
	defer h.u.infra.Close()
	c := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Check web", Org: "Acme"})
	other := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Check db", Org: "Acme"})
	h.u.cardInfra[c.ID], h.u.cardInfra[other.ID] = "Acme", "Acme"
	h.u.detach(c.ID)
	d := h.u.detached[c.ID]
	w := &dwin{h: h, d: d}
	w.frame()
	if h.u.infraBy != c.ID || !d.drewInfra || h.u.infraWin.Load() != d.w {
		t.Fatalf("the detached window draws the servers: by %q", h.u.infraBy)
	}
	h.u.infraWake() // its output redraws that window
	h.u.detach(other.ID)
	w2 := &dwin{h: h, d: h.u.detached[other.ID]}
	w2.frame()
	if h.u.infraBy != c.ID || w2.d.drewInfra {
		t.Error("a second detached window shows its chat alone")
	}
	h.u.view = "sess:" + other.ID
	h.u.reattach(other.ID)
	h.frame()
	if h.u.splitShown {
		t.Error("the main window's session shows its chat alone meanwhile")
	}
	h.u.view = "srv:Acme"
	h.frame()
	h.clickButton("infraHere")
	if h.u.infraBy != "" || !h.u.noSplit[c.ID] {
		t.Fatal("Show them here takes the servers back")
	}
	w.frame()
	if h.u.infraBy != "" || d.drewInfra {
		t.Error("that window then shows the chat alone")
	}
	h.u.noSplit[c.ID] = false
	w.frame()
	h.u.reattach(c.ID)
	if h.u.infraBy != "" || h.u.infraWin.Load() != nil {
		t.Error("reattaching gives the servers back")
	}
	h.u.infraWake() // the main window's again
	h.u.detach(c.ID)
	d = h.u.detached[c.ID]
	w = &dwin{h: h, d: d}
	w.frame()
	h.u.noSplit[c.ID] = true
	w.frame()
	if h.u.infraBy != "" {
		t.Error("its own Chat only lets the servers go")
	}
	h.u.noSplit[c.ID] = false
	w.frame()
	d.p.focused = true
	h.frame()
	if !h.u.infraOn {
		t.Error("the servers have the keyboard when their window has")
	}
}

// TestDetachedViews: the header's workspace, drawings, editor and terminal
// open in the detached window itself, the main window's panel and
// selection untouched; esc comes back to the session step by step.
func TestDetachedViews(t *testing.T) {
	h := newHarness(t)
	fakeWindows(t)
	id := h.u.b.Add(0, kanban.Card{Title: "a", Org: "Acme"}).ID
	other := h.u.b.Add(0, kanban.Card{Title: "b"}).ID
	h.u.openPanel(other)
	h.u.detach(id)
	d := h.u.detached[id]
	w := &dwin{h: h, d: d}
	w.click("pnWS")
	if d.p.view != "ws:"+id || h.u.mainPane.view != "" || h.u.panel != other {
		t.Fatalf("workspace: detached %q, main %q, panel %q", d.p.view, h.u.mainPane.view, h.u.panel)
	}
	st := h.u.wsOf(id)
	st.find = &wsFind{at: -1}
	w.send(press(key.NameEscape, 0))
	if st.find != nil || d.p.view != "ws:"+id {
		t.Fatal("esc closes its find bar first")
	}
	ran := false
	h.u.post = func() { ran = true } // a diff's Open in workspace, after the frame
	w.frame()
	if !ran {
		t.Error("the window runs what waits for the end of its frame")
	}
	w.send(press(key.NameEscape, 0))
	if d.p.view != "" {
		t.Fatalf("esc back to the session: %q", d.p.view)
	}
	w.click("pnEdit")
	if h.u.edit == nil || h.u.edit.in != &d.p {
		t.Fatal("the editor opens in the detached window")
	}
	h.frame()
	h.send(press(key.NameEscape, 0)) // the main window's esc leaves it alone
	if h.u.edit == nil {
		t.Fatal("main esc closed the detached window's editor")
	}
	w.send(press(key.NameEscape, 0))
	if h.u.edit != nil {
		t.Fatal("its esc closes it")
	}
	h.u.inPane(&d.p, func() { h.u.newCard(0) }) // a new-card dialog there: esc closes it unsaved
	w.frame()
	w.send(press(key.NameEscape, 0))
	if h.u.edit != nil {
		t.Fatal("esc closes a new card there")
	}
	w.click("pnDraw")
	if d.p.view != "drc:"+id {
		t.Fatalf("drawings: %q", d.p.view)
	}
	dst, _ := h.drawing("plan") // a drawing open there: esc goes to it
	h.u.mainPane.view, d.p.view = "", "draw:"+dst.path
	w.frame()
	w.send(press(key.NameEscape, 0))
	if d.p.view == "" {
		t.Fatalf("esc is the drawing's first: it backs out its own way, not to the session")
	}
	d.p.view = "sess:" + id // the way the workspace comes back
	w.frame()
	d.p.sub, d.p.rail = "x", true
	w.send(press(key.NameEscape, 0))
	w.send(press(key.NameEscape, 0))
	if d.p.sub != "" || d.p.rail {
		t.Error("then the subagent and the rail")
	}
	w.click("pnEdit")
	h.u.reattach(id) // its editor closes with it
	if h.u.edit != nil {
		t.Error("reattaching closes its editor")
	}
	h.u.detach(id)
	d = h.u.detached[id]
	h.u.inPane(&d.p, func() { h.u.newCard(0) })
	h.u.reattach(id)
	if h.u.edit != nil {
		t.Error("and a new card's dialog")
	}
}

// TestDetachedTerminal: a terminal shown in a detached window redraws
// that window on its output, not the main one, and takes esc itself.
func TestDetachedTerminal(t *testing.T) {
	h := newHarness(t)
	fakeWindows(t)
	id := h.u.b.Add(0, kanban.Card{Title: "a"}).ID
	h.u.detach(id)
	d := h.u.detached[id]
	w := &dwin{h: h, d: d}
	startTerm = func(cmd *exec.Cmd, w, ht int, notify func()) (*term.Term, error) {
		return term.Start(prog("cat"), w, ht, notify)
	}
	defer func() { startTerm = term.Start }()
	w.click("pnTerm")
	defer h.u.closeTerm(id)
	if d.p.view != "term:"+id || h.u.mainPane.view != "" {
		t.Fatalf("the terminal opens in its window: %q, main %q", d.p.view, h.u.mainPane.view)
	}
	w.frame()
	if d.shownTerm.Load() != id {
		t.Fatal("the window says which terminal it shows")
	}
	g := h.u.termGen.Load()
	h.u.termNotify(id, nil)
	if h.u.termGen.Load() == g {
		t.Error("its output asks for a frame")
	}
	g = h.u.termGen.Load()
	h.u.termNotify("other", nil)
	if h.u.termGen.Load() != g {
		t.Error("a hidden terminal's output asks nothing")
	}
	w.send(press(key.NameEscape, 0))
	if d.p.view != "term:"+id {
		t.Error("esc goes to the terminal")
	}
}

// TestHyprFocus: on Hyprland the window is found among the clients by pid
// and title and focused through the socket: hl.dsp.focus, else the older
// focuswindow; nothing off Hyprland or without the window.
func TestHyprFocus(t *testing.T) {
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	if hyprFocus("x") {
		t.Fatal("off Hyprland")
	}
	rt, err := os.MkdirTemp("", "hy") // short: a socket path has a limit
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(rt)
	os.MkdirAll(filepath.Join(rt, "hypr", "sig"), 0o700)
	t.Setenv("XDG_RUNTIME_DIR", rt)
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "sig")
	if hyprFocus("x") {
		t.Fatal("no socket")
	}
	l, err := net.Listen("unix", filepath.Join(rt, "hypr", "sig", ".socket.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	clients, lua := "", "ok"
	var got []string
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			b := make([]byte, 512)
			n, _ := c.Read(b)
			req := string(b[:n])
			got = append(got, req)
			switch {
			case req == "j/clients":
				io.WriteString(c, clients)
			case strings.Contains(req, "hl.dsp.focus"):
				io.WriteString(c, lua)
			default:
				io.WriteString(c, "ok")
			}
			c.Close()
		}
	}()
	clients = "not json"
	if hyprFocus("DESK-1 · a") {
		t.Error("bad clients")
	}
	clients = fmt.Sprintf(`[{"address":"0xa","pid":%d,"title":"other"},{"address":"0xb","pid":1,"title":"DESK-1 · a"},{"address":"0xc","pid":%d,"title":"DESK-1 · a"}]`, os.Getpid(), os.Getpid())
	if !hyprFocus("DESK-1 · a") || !strings.Contains(got[len(got)-1], "address:0xc") {
		t.Errorf("Lua focus: %q", got)
	}
	lua = "error"
	if !hyprFocus("DESK-1 · a") || got[len(got)-1] != "dispatch focuswindow address:0xc" {
		t.Errorf("older focus: %q", got)
	}
	if hyprFocus("DESK-9 · gone") {
		t.Error("no such window")
	}
}

// fakeKWin is org.kde.KWin's /Scripting on a test bus: the script it was
// given, whether it started, and a loadScript that can refuse.
type fakeKWin struct {
	script  string
	started bool
	refuse  bool
}

func (k *fakeKWin) UnloadScript(name string) (bool, *dbus.Error) { return false, nil }
func (k *fakeKWin) LoadScript(f, name string) (int32, *dbus.Error) {
	if k.refuse {
		return -1, nil
	}
	b, _ := os.ReadFile(f)
	k.script = string(b)
	return 3, nil
}
func (k *fakeKWin) Start() *dbus.Error { k.started = true; return nil }

// TestKWinFocus: on KDE a KWin script is loaded that activates this
// process's window by its title, then started; nothing without KWin.
func TestKWinFocus(t *testing.T) {
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("no dbus-daemon")
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent")
	if kwinFocus("x") {
		t.Fatal("no bus")
	}
	dir, err := os.MkdirTemp("", "kw") // short: a socket path has a limit
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	addr := "unix:path=" + filepath.Join(dir, "bus")
	cmd := exec.Command(daemon, "--session", "--nofork", "--nopidfile", "--address="+addr)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
	t.Setenv("TMPDIR", dir)
	var kw *dbus.Conn
	for end := time.Now().Add(testwait.Long); kw == nil; {
		if kw, err = dbus.ConnectSessionBus(); err != nil && time.Now().After(end) {
			t.Fatal(err)
		}
	}
	defer kw.Close()
	if kwinFocus("x") {
		t.Fatal("no KWin")
	}
	k := &fakeKWin{}
	kw.ExportMethodTable(map[string]any{"unloadScript": k.UnloadScript, "loadScript": k.LoadScript, "start": k.Start}, "/Scripting", "org.kde.kwin.Scripting")
	kw.RequestName("org.kde.KWin", 0)
	if !kwinFocus(`DESK-1 · "a"`) || !k.started {
		t.Fatal("not run")
	}
	if !strings.Contains(k.script, `const want = "DESK-1 · \"a\"", pid = `+fmt.Sprint(os.Getpid())) {
		t.Errorf("script: %s", k.script)
	}
	k.refuse, k.started = true, false
	if kwinFocus("x") || k.started {
		t.Error("refused script ran")
	}
	os.RemoveAll(dir + "/nodir")
	t.Setenv("TMPDIR", filepath.Join(dir, "nodir"))
	if kwinFocus("x") {
		t.Error("unwritable script")
	}
}
