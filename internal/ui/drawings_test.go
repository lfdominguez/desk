package ui

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"

	"github.com/lfdominguez/desk/internal/draw"
)

// drawing makes Acme's drawing name with els and opens it at 100% zoom, the
// scene's origin at the canvas's; it returns its state and where the
// canvas's origin is in the window.
func (h *harness) drawing(name string, els ...*draw.Element) (*drawState, f32.Point) {
	h.t.Helper()
	s := draw.NewScene()
	s.Elements = append(s.Elements, els...)
	path := filepath.Join(h.u.drawingsDir("Acme"), name+".excalidraw")
	if err := s.Save(path); err != nil {
		h.t.Fatal(err)
	}
	h.u.drawRead()
	h.u.openDrawing("Acme", path)
	st := h.u.draws[path]
	st.placed, st.zoom, st.pan = true, 1, f32.Point{}
	st.claude.hide = true // the canvas the whole width; drawchat_test.go shows the chat
	h.frame()
	// a press on empty space starts a selection box at the scene's point under it
	p := f32.Pt(float32(h.size.X-150), float32(h.size.Y/2))
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p})
	if st.ed.Box == nil {
		h.t.Fatal("no selection box: the probe missed the canvas")
	}
	o := p.Sub(f32.Pt(float32(st.ed.Box[0]), float32(st.ed.Box[1])))
	h.send(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
	st.pressAt = time.Time{} // the probe's press is no double click's first
	return st, o
}

// drag presses at a, moves through the middle to b and lets go, window points.
func (h *harness) drag(a, b f32.Point, btn pointer.Buttons, mods key.Modifiers) {
	h.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: a},
		pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: btn, Position: a, Modifiers: mods},
		pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: btn, Position: a.Add(b).Mul(.5), Modifiers: mods},
		pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: btn, Position: b, Modifiers: mods},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: b, Modifiers: mods})
}

// clickTop clicks button id at the topmost clickable area it hovers in: a
// card's own body, not the bin inside it (which hovers the card too).
func (h *harness) clickTop(id string) {
	h.t.Helper()
	h.frame()
	var at []image.Point
	for _, n := range h.r.AppendSemantics(nil) {
		if n.Desc.Gestures&input.ClickGesture != 0 {
			at = append(at, n.Desc.Bounds.Min.Add(n.Desc.Bounds.Max).Div(2))
		}
	}
	slices.SortStableFunc(at, func(a, b image.Point) int { return a.Y - b.Y })
	for _, p := range at {
		pos := f32.Pt(float32(p.X), float32(p.Y))
		h.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: pos})
		h.frame()
		if h.u.k.Click(id).Hovered() {
			h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: pos},
				pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: pos})
			return
		}
	}
	h.t.Fatalf("button %s is not on screen", id)
}

func rect(x, y, w, h float64, fill string) *draw.Element {
	e := draw.New("rectangle")
	e.X, e.Y, e.Width, e.Height, e.BackgroundColor = x, y, w, h, fill
	return e
}

func TestDrawingsList(t *testing.T) {
	h := newHarness(t)
	if n := len(h.u.drawings("Acme")); n != 0 {
		t.Fatalf("%d drawings", n)
	}
	for _, s := range h.u.sections() {
		for _, it := range s.items {
			if it.view == "dr:Acme" {
				t.Error("an org without cards or drawings in the sidebar")
			}
		}
	}
	h.u.view = "dr:Acme"
	h.frame()
	if !strings.Contains(h.u.viewTitle(), "Acme · drawings") || !strings.HasPrefix(h.u.sentence(), "0 drawings") {
		t.Errorf("header: %q, %q", h.u.viewTitle(), h.u.sentence())
	}
	h.clickButton("drNewAcme")
	st := h.u.drawOpen()
	if st == nil || drawName(st.path) != "Untitled" || !st.dark() {
		t.Fatalf("New drawing: view %q", h.u.view)
	}
	if _, err := os.Stat(st.path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.u.viewTitle(), "Untitled") || !strings.HasPrefix(h.u.sentence(), "Draw with") {
		t.Errorf("drawing header: %q, %q", h.u.viewTitle(), h.u.sentence())
	}
	h.u.view = "dr:Acme"
	h.clickButton("drNewAcme")
	if drawName(h.u.drawOpen().path) != "Untitled 2" {
		t.Errorf("the second: %s", h.u.drawOpen().path)
	}
	found := false
	for _, s := range h.u.sections() {
		for _, it := range s.items {
			found = found || it.view == "dr:Acme"
		}
	}
	if !found || h.u.count("dr:Acme") != 2 {
		t.Errorf("the sidebar's Drawings: %v, %d", found, h.u.count("dr:Acme"))
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(st.path, old, old)
	h.u.drawRead()
	if l := h.u.drawings("Acme"); l[0].name != "Untitled 2" || l[1].name != "Untitled" {
		t.Errorf("newest first: %v", l)
	}
	os.WriteFile(filepath.Join(h.u.drawingsDir("Acme"), "notes.txt"), nil, 0o644)
	os.Mkdir(filepath.Join(h.u.drawingsDir("Acme"), "dir.excalidraw"), 0o755)
	h.u.drawRead()
	if n := len(h.u.drawings("Acme")); n != 2 {
		t.Errorf("other files listed: %d", n)
	}
	h.u.view = "dr:Acme"
	h.frame()
	if _, ok := h.u.thumbs[st.path]; !ok {
		t.Error("no picture for the list")
	}
	h.clickTop("drOpen" + st.path)
	if h.u.drawOpen() != st {
		t.Errorf("a click on a drawing opens it: %q", h.u.view)
	}
}

func TestDrawingThumb(t *testing.T) {
	h := newHarness(t)
	h.drawing("Pic", rect(0, 0, 100, 50, "#ffc9c9"))
	it := h.u.drawings("Acme")[0]
	th, ok := h.u.thumb(it)
	if !ok || th.size.X <= 100 {
		t.Fatalf("thumb %v %v", th.size, ok)
	}
	if again, _ := h.u.thumb(it); again.img != th.img {
		t.Error("rendered again for the same file")
	}
	it.mod = it.mod.Add(time.Second)
	if again, _ := h.u.thumb(it); again.img == th.img {
		t.Error("kept for a changed file")
	}
	s := draw.NewScene()
	s.Save(filepath.Join(h.u.drawingsDir("Acme"), "Empty.excalidraw"))
	h.u.drawRead()
	for _, it := range h.u.drawings("Acme") {
		if _, ok := h.u.thumb(it); ok == (it.name == "Empty") {
			t.Errorf("%s: picture %v", it.name, ok)
		}
	}
	h.u.view = "dr:Acme"
	h.frame()
}

func TestDrawingDelete(t *testing.T) {
	h := newHarness(t)
	st, _ := h.drawing("Gone")
	h.u.view = "dr:Acme"
	h.frame()
	h.clickButton("drDel" + st.path)
	if _, err := os.Stat(st.path); !os.IsNotExist(err) || h.u.draws[st.path] != nil || h.u.drawOpen() != nil {
		t.Fatal("not deleted")
	}
	h.u.runUndo()
	if _, err := os.Stat(st.path); err != nil || len(h.u.drawings("Acme")) != 1 {
		t.Fatal("undo didn't bring it back")
	}
	h.u.deleteDrawing(filepath.Join(h.u.drawingsDir("Acme"), "nope.excalidraw"))
	if !strings.Contains(h.u.toast, "Couldn't delete") || !h.u.toastBad {
		t.Errorf("toast %q", h.u.toast)
	}
	h.u.deleteDrawing(st.path)
	os.Remove(h.u.drawingsDir("Acme"))
	os.WriteFile(h.u.drawingsDir("Acme"), nil, 0o644) // a file where the folder was: undo can't write
	h.u.runUndo()
	if !strings.Contains(h.u.toast, "Couldn't restore") || !h.u.toastBad {
		t.Errorf("toast %q", h.u.toast)
	}
}

func TestDrawingOpenFails(t *testing.T) {
	h := newHarness(t)
	p := filepath.Join(t.TempDir(), "bad.excalidraw")
	os.WriteFile(p, []byte("{"), 0o644)
	h.u.view = ""
	h.u.openDrawing("Acme", p)
	if h.u.view != "" || (!strings.Contains(h.u.toast, "Couldn't open") || !h.u.toastBad) {
		t.Errorf("view %q, toast %q", h.u.view, h.u.toast)
	}
	os.WriteFile(filepath.Join(h.u.root, "Acme", ".desk"), nil, 0o644) // .desk a file: no drawings folder
	h.u.newDrawing(drawPlace{org: "Acme"})
	if !strings.Contains(h.u.toast, "Couldn't make") || !h.u.toastBad {
		t.Errorf("toast %q", h.u.toast)
	}
	h.u.view = "draw:/gone"
	h.frame()
	if h.u.viewTitle() == "" {
		t.Error("no title for a gone drawing")
	}
}

func TestDrawingRename(t *testing.T) {
	h := newHarness(t)
	st, _ := h.drawing("Old")
	h.drawing("Taken")
	h.u.openDrawing("Acme", st.path)
	h.frame()
	h.u.renameDrawing(st, "  New/one ")
	if drawName(st.path) != "New-one" || h.u.drawOpen() != st || h.u.draws[st.path] != st {
		t.Fatalf("renamed to %s, view %q", st.path, h.u.view)
	}
	if _, err := os.Stat(st.path); err != nil {
		t.Fatal(err)
	}
	st.title.SetText("Taken")
	h.u.renameDrawing(st, "Taken")
	if drawName(st.path) != "New-one" || st.title.Text() != "New-one" || (!strings.Contains(h.u.toast, "exists") || !h.u.toastBad) {
		t.Errorf("onto another: %s, %q", st.path, h.u.toast)
	}
	st.title.SetText("")
	h.u.renameDrawing(st, " ")
	if st.title.Text() != "New-one" {
		t.Error("an empty name stays")
	}
	h.u.renameDrawing(st, "New-one") // the same
	h.u.renameDrawing(st, "sub\\x")
	if drawName(st.path) != "sub-x" {
		t.Errorf("a backslash: %s", st.path)
	}
	path := st.path
	os.Remove(path)
	h.u.renameDrawing(st, "Lost")
	if st.path != path || (!strings.Contains(h.u.toast, "Couldn't rename") || !h.u.toastBad) {
		t.Errorf("a missing file: %s, %q", st.path, h.u.toast)
	}
}

func TestDrawingRenameByTitle(t *testing.T) {
	h := newHarness(t)
	st, _ := h.drawing("Old")
	h.frame()
	h.r.Source().Execute(key.FocusCmd{Tag: &st.title})
	h.frame()
	if !h.r.Source().Focused(&st.title) {
		t.Fatal("the title doesn't have the keyboard")
	}
	st.title.SetText("Fresh")
	h.send(press(key.NameReturn, 0))
	if drawName(st.path) != "Fresh" {
		t.Errorf("enter: %s", st.path)
	}
	h.r.Source().Execute(key.FocusCmd{Tag: &st.title})
	h.frame()
	st.title.SetText("Blurred")
	h.r.Source().Execute(key.FocusCmd{})
	h.frame()
	h.frame()
	if drawName(st.path) != "Blurred" {
		t.Errorf("leaving the field: %s", st.path)
	}
}

func TestDrawingDrawAndSave(t *testing.T) {
	h := newHarness(t)
	st, o := h.drawing("D")
	h.send(press("R", 0))
	if st.ed.Tool != draw.ToolRectangle || h.r.Cursor() == pointer.CursorGrab {
		t.Fatal("R")
	}
	h.drag(o.Add(f32.Pt(500, 100)), o.Add(f32.Pt(700, 200)), pointer.ButtonPrimary, 0) // right of the style panel
	if len(st.ed.Scene.Elements) != 1 {
		t.Fatalf("%d elements", len(st.ed.Scene.Elements))
	}
	e := st.ed.Scene.Elements[0]
	if e.X != 500 || e.Y != 100 || e.Width != 200 || e.Height != 100 {
		t.Errorf("drawn at %v,%v %v×%v", e.X, e.Y, e.Width, e.Height)
	}
	s, err := draw.Load(st.path)
	if err != nil || len(s.Elements) != 1 {
		t.Fatalf("not saved: %v", err)
	}
	h.send(press("Z", key.ModShortcut))
	if len(st.ed.Scene.Elements) != 0 {
		t.Error("Ctrl+Z")
	}
	h.send(press("Z", key.ModShortcut|key.ModShift))
	if len(st.ed.Scene.Elements) != 1 {
		t.Error("Ctrl+Shift+Z")
	}
	h.send(press("Z", key.ModShortcut), press("Y", key.ModShortcut))
	if len(st.ed.Scene.Elements) != 1 {
		t.Error("Ctrl+Y")
	}
	h.send(press("A", key.ModShortcut))
	if len(st.ed.Sel) != 1 {
		t.Error("Ctrl+A")
	}
	h.send(press("D", key.ModShortcut))
	if len(st.ed.Scene.Elements) != 2 {
		t.Error("Ctrl+D")
	}
	c := st.ed.Selected()[0]
	h.send(press(key.NameRightArrow, 0), press(key.NameDownArrow, key.ModShift), press(key.NameLeftArrow, 0), press(key.NameUpArrow, 0))
	if c.X != 510 || c.Y != 119 {
		t.Errorf("arrows: %v,%v", c.X, c.Y)
	}
	h.send(press("[", key.ModShortcut))
	if st.ed.Scene.Elements[0] != c {
		t.Error("Ctrl+[")
	}
	h.send(press("]", key.ModShortcut))
	if st.ed.Scene.Elements[1] != c {
		t.Error("Ctrl+]")
	}
	h.send(press("[", key.ModShortcut|key.ModShift))
	if st.ed.Scene.Elements[0] != c {
		t.Error("Ctrl+Shift+[")
	}
	h.send(press("]", key.ModShortcut|key.ModShift))
	if st.ed.Scene.Elements[1] != c {
		t.Error("Ctrl+Shift+]")
	}
	h.send(press(key.NameDeleteBackward, 0))
	if len(st.ed.Scene.Elements) != 1 {
		t.Error("Backspace")
	}
	h.send(press("A", key.ModShortcut), press(key.NameDeleteForward, 0))
	if len(st.ed.Scene.Elements) != 0 {
		t.Error("Delete")
	}
	h.send(press("E", key.ModAlt)) // Alt with a letter: nothing
	if st.ed.Tool == draw.ToolEraser {
		t.Error("Alt+E")
	}
	for k, tool := range map[key.Name]string{"V": draw.ToolSelect, "H": draw.ToolHand, "2": draw.ToolRectangle, "D": draw.ToolDiamond,
		"O": draw.ToolEllipse, "A": draw.ToolArrow, "L": draw.ToolLine, "P": draw.ToolFreedraw, "T": draw.ToolText, "E": draw.ToolEraser, "0": draw.ToolEraser, "1": draw.ToolSelect} {
		h.send(press(k, 0))
		if st.ed.Tool != tool {
			t.Errorf("%s: %s", k, st.ed.Tool)
		}
	}
	h.send(key.Event{Name: "R", State: key.Release}) // a release: nothing
}

func TestDrawingSaveFails(t *testing.T) {
	h := newHarness(t)
	st, o := h.drawing("D")
	os.Remove(st.path)
	os.Mkdir(st.path, 0o755) // a folder where the file goes: Save can't rename over it
	h.send(press("R", 0))
	h.drag(o.Add(f32.Pt(500, 100)), o.Add(f32.Pt(700, 200)), pointer.ButtonPrimary, 0)
	if (!strings.Contains(h.u.toast, "Couldn't save") || !h.u.toastBad) || st.failed != st.ed.Gen {
		t.Fatalf("toast %q", h.u.toast)
	}
	h.u.toast = ""
	h.frame()
	if h.u.toast != "" {
		t.Error("tried again without a change")
	}
}

func TestDrawingTheme(t *testing.T) {
	h := newHarness(t)
	st, _ := h.drawing("D", rect(0, 0, 50, 50, "#ffffff"))
	if st.dark() {
		t.Fatal("a new scene from draw is light")
	}
	h.send(press("D", key.ModAlt|key.ModShift))
	if !st.dark() || st.shown.Background() != (color.NRGBA{0x12, 0x12, 0x12, 0xff}) {
		t.Errorf("dark: %v", st.shown.Background())
	}
	if c := st.shown.Elements[0].BackgroundColor; c != "#121212" {
		t.Errorf("a white fill shows %s", c)
	}
	if st.ed.Scene.Elements[0].BackgroundColor != "#ffffff" {
		t.Error("the file's colors changed")
	}
	h.clickButton("drawTheme")
	if st.dark() {
		t.Error("the button")
	}
	if darkHex("transparent") != "transparent" || darkHex("") != "" {
		t.Error("no color stays none")
	}
	if c := darkColor(color.NRGBA{0x1e, 0x1e, 0x1e, 0xff}); c.R < 0xd0 || c.R != c.G || c.G != c.B {
		t.Errorf("black ink turns light gray: %v", c)
	}
	if c := darkColor(color.NRGBA{0xe0, 0x31, 0x31, 0xff}); c.R < c.G || c.R < c.B {
		t.Errorf("red stays red: %v", c)
	}
}

func TestDrawingEraserShows(t *testing.T) {
	h := newHarness(t)
	st, o := h.drawing("D", rect(500, 0, 100, 100, "#ffc9c9"))
	h.send(press("E", 0))
	at := o.Add(f32.Pt(550, 50))
	h.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: at},
		pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: at})
	if st.shown.Elements[0].Opacity != 25 {
		t.Errorf("erased shows at %v", st.shown.Elements[0].Opacity)
	}
	if st.ed.Scene.Elements[0].Opacity != 100 {
		t.Error("the element itself faded")
	}
	h.send(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: at})
	if len(st.ed.Scene.Elements) != 0 {
		t.Error("not erased")
	}
}

func TestDrawingPanZoom(t *testing.T) {
	h := newHarness(t)
	st, o := h.drawing("D", rect(0, 0, 100, 100, "#ffc9c9"))
	at := o.Add(f32.Pt(400, 300))
	h.send(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: at, Scroll: f32.Pt(10, 20)})
	if st.pan != f32.Pt(-10, -20) {
		t.Errorf("scroll pans: %v", st.pan)
	}
	h.send(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: at, Scroll: f32.Pt(0, -240), Modifiers: key.ModShortcut})
	if st.zoom <= 1 {
		t.Errorf("Ctrl+scroll zooms: %v", st.zoom)
	}
	st.zoom, st.pan = 1, f32.Point{}
	h.drag(at, at.Add(f32.Pt(30, 40)), pointer.ButtonTertiary, 0)
	if st.pan != f32.Pt(30, 40) || len(st.ed.Sel) != 0 {
		t.Errorf("middle drag pans: %v", st.pan)
	}
	h.send(press("H", 0))
	h.drag(at, at.Add(f32.Pt(10, 0)), pointer.ButtonPrimary, 0)
	if st.pan != f32.Pt(40, 40) {
		t.Errorf("the hand pans: %v", st.pan)
	}
	if h.r.Cursor() != pointer.CursorGrab {
		t.Errorf("hand cursor %v", h.r.Cursor())
	}
	h.send(press("V", 0), key.Event{Name: key.NameSpace, State: key.Press})
	if !st.space || h.r.Cursor() != pointer.CursorGrab {
		t.Error("space held")
	}
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: at},
		pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: at.Add(f32.Pt(0, 10))})
	if h.r.Cursor() != pointer.CursorGrabbing {
		t.Errorf("panning cursor %v", h.r.Cursor())
	}
	h.send(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: at.Add(f32.Pt(0, 10))}, key.Event{Name: key.NameSpace, State: key.Release})
	if st.pan != f32.Pt(40, 50) || st.space {
		t.Errorf("space drag pans: %v", st.pan)
	}
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonSecondary, Position: at},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: at})
	if st.ed.Box != nil || st.down {
		t.Error("a right click does something")
	}
	h.clickButton("drawZoomIn")
	if st.zoom != 1.25 {
		t.Errorf("zoom in: %v", st.zoom)
	}
	h.clickButton("drawZoomOut")
	h.clickButton("drawZoomOut")
	if st.zoom != .8 {
		t.Errorf("zoom out: %v", st.zoom)
	}
	h.clickButton("drawZoom100")
	if st.zoom != 1 {
		t.Errorf("100%%: %v", st.zoom)
	}
	h.clickButton("drawFit")
	if st.zoom != 1 || st.pan.X <= 0 {
		t.Errorf("fit a small drawing: %v %v", st.zoom, st.pan)
	}
	h.send(press("1", key.ModShift))
	st.zoomAt(f32.Point{}, 1000)
	if st.zoom != drawZoomMax {
		t.Error("zoom past the most")
	}
	st.fit(image.Pt(200, 200), 1)
	if st.zoom >= 1 {
		t.Error("a big drawing in a small pane")
	}
	empty := &drawState{ed: draw.NewEditor(draw.NewScene())}
	empty.fit(image.Pt(800, 600), 2)
	if empty.zoom != 1 || empty.pan != f32.Pt(160, 160) {
		t.Errorf("an empty drawing: %v %v", empty.zoom, empty.pan)
	}
}

func TestDrawingText(t *testing.T) {
	h := newHarness(t)
	st, o := h.drawing("D")
	at := o.Add(f32.Pt(200, 200))
	click := func() {
		h.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: at},
			pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: at},
			pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: at})
	}
	click()
	click() // a double click on nothing: a text there
	if st.ed.Editing == "" {
		t.Fatal("no text began")
	}
	h.frame()
	if !h.r.Source().Focused(&st.text) {
		t.Fatal("the text box doesn't have the keyboard")
	}
	h.send(key.EditEvent{Text: "Hello"})
	e := st.ed.Scene.Find(st.ed.Editing)
	if e.Text != "Hello" {
		t.Fatalf("typed %q", e.Text)
	}
	h.send(press("R", 0)) // a letter while typing is text, not a tool
	if st.ed.Tool != draw.ToolSelect {
		t.Error("a tool key while typing")
	}
	h.send(press(key.NameEscape, 0))
	if st.ed.Editing != "" || len(st.ed.Scene.Elements) != 1 {
		t.Fatal("esc ends the text")
	}
	st.pressAt = time.Time{}
	click()
	click() // a double click on the text: edits it
	if st.ed.Editing != e.ID {
		t.Fatal("not editing the text again")
	}
	h.frame()
	st.pressAt = time.Time{}
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: o.Add(f32.Pt(600, 500))},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: o.Add(f32.Pt(600, 500))})
	if st.ed.Editing != "" {
		t.Error("a press elsewhere ends the text")
	}
	st.ed.Select(e.ID)
	h.send(press(key.NameReturn, 0))
	if st.ed.Editing != e.ID {
		t.Fatal("enter on a selected text edits it")
	}
	h.frame()
	h.r.Source().Execute(key.FocusCmd{}) // the keyboard went elsewhere
	h.frame()
	h.frame()
	if st.ed.Editing != "" {
		t.Error("losing the keyboard ends the text")
	}
	for _, a := range []string{"center", "right"} {
		e.TextAlign = a
		st.ed.EditText(e.ID)
		h.frame()
		st.ed.EndText()
	}
	st.ed.Select()
	h.send(press(key.NameReturn, 0)) // nothing selected: nothing
	if st.ed.Editing != "" {
		t.Error("enter edits nothing")
	}
}

func TestDrawingEscape(t *testing.T) {
	h := newHarness(t)
	st, _ := h.drawing("D", rect(0, 0, 100, 100, "#ffc9c9"))
	st.ed.Tool = draw.ToolText
	st.ed.Down(draw.Pt{X: 300, Y: 300}, 4, false)
	st.ed.Up()
	st.ed.SetText("x")
	h.frame()
	h.send(press(key.NameEscape, 0))
	if st.ed.Editing != "" {
		t.Fatal("esc: the text")
	}
	h.send(press("R", 0))
	h.send(press(key.NameEscape, 0))
	if st.ed.Tool != draw.ToolSelect {
		t.Error("esc: the tool")
	}
	st.ed.Select(st.ed.Scene.Elements[0].ID)
	h.send(press(key.NameEscape, 0))
	if len(st.ed.Sel) != 0 {
		t.Error("esc: the selection")
	}
	h.send(press(key.NameEscape, 0))
	if h.u.view != "dr:Acme" {
		t.Errorf("esc: back to the list, %q", h.u.view)
	}
	h.u.openDrawing("Acme", st.path)
	h.frame()
	h.clickButton("drawBack")
	if h.u.view != "dr:Acme" {
		t.Errorf("back: %q", h.u.view)
	}
}

func TestDrawingToolbar(t *testing.T) {
	h := newHarness(t)
	st, _ := h.drawing("D")
	for _, tb := range drawToolbar {
		h.clickButton("drawTool" + tb.tool)
		if st.ed.Tool != tb.tool {
			t.Errorf("%s: %s", tb.tool, st.ed.Tool)
		}
	}
	h.clickButton("drawTool" + draw.ToolRectangle)
	st.ed.Style.BackgroundColor = "#ffc9c9"
	st.ed.Down(draw.Pt{X: 0, Y: 0}, 4, false)
	st.ed.Drag(draw.Pt{X: 50, Y: 50}, 4, false)
	st.ed.Up()
	h.frame()
	h.clickButton("drawUndo")
	if len(st.ed.Scene.Elements) != 0 {
		t.Error("undo button")
	}
	h.clickButton("drawRedo")
	if len(st.ed.Scene.Elements) != 1 {
		t.Error("redo button")
	}
}

// TestDrawingStyle clicks every option of the panel for each kind of
// element, checking a few land.
func TestDrawingStyle(t *testing.T) {
	h := newHarness(t)
	r := rect(0, 0, 100, 100, "#ffc9c9")
	ar := draw.New("arrow")
	ar.X, ar.Y, ar.Points = 0, 200, [][2]float64{{0, 0}, {100, 0}}
	tx := draw.New("text")
	tx.X, tx.Y, tx.Text = 0, 300, "Hi"
	tx.FitText()
	st, _ := h.drawing("D", r, ar, tx)
	opts := func() []string {
		var ids []string
		for _, c := range drawStrokes {
			ids = append(ids, "drawOpts"+c)
		}
		for _, c := range drawFills {
			ids = append(ids, "drawOptb"+c)
		}
		for _, id := range []string{"fh", "fx", "fs", "w1", "w2", "w4", "sssolid", "ssdashed", "ssdotted", "r0", "r1", "r2", "esharp", "eround",
			"hs", "he", "ffHand", "ffNormal", "ffCode", "fzS", "fzM", "fzL", "fzXL", "taleft", "tacenter", "taright"} {
			ids = append(ids, "drawOpt"+id)
		}
		return ids
	}
	for _, e := range []*draw.Element{r, ar, tx} {
		st.ed.Select(e.ID)
		h.frame()
		for _, id := range opts() {
			if _, ok := h.seek(id, image.Rectangle{Max: h.size}, h.frame); ok {
				h.clickButton(id)
			}
		}
	}
	st.ed.Select(r.ID)
	h.frame()
	h.clickButton("drawOpts#e03131")
	h.clickButton("drawOptb#a5d8ff")
	h.clickButton("drawOptfx")
	h.clickButton("drawOptw4")
	h.clickButton("drawOptssdashed")
	h.clickButton("drawOptr2")
	h.clickButton("drawOptesharp")
	r = st.ed.Scene.Find(r.ID)
	if r.StrokeColor != "#e03131" || r.BackgroundColor != "#a5d8ff" || r.FillStyle != "cross-hatch" || r.StrokeWidth != 4 || r.StrokeStyle != "dashed" || r.Roughness != 2 || r.Roundness != nil {
		t.Errorf("styled: %+v", r)
	}
	h.clickButton("drawOpteround")
	if st.ed.Scene.Find(r.ID).Roundness.Type != draw.RoundAdaptive {
		t.Error("round corners")
	}
	h.clickButton("drawOpacity+")
	h.clickButton("drawOpacity-")
	h.clickButton("drawOpacity-")
	if st.ed.Scene.Find(r.ID).Opacity != 80 { // + at 100 stays
		t.Errorf("opacity %v", st.ed.Scene.Find(r.ID).Opacity)
	}
	st.ed.Select(ar.ID)
	h.frame()
	h.clickButton("drawOpteround")
	if st.ed.Scene.Find(ar.ID).Roundness.Type != draw.RoundProportional {
		t.Error("a curved arrow")
	}
	head := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	was := head(st.ed.Scene.Find(ar.ID).EndArrowhead)
	h.clickButton("drawOpthe")
	if got := head(st.ed.Scene.Find(ar.ID).EndArrowhead); got != drawHeads[(indexOf(drawHeads, was)+1)%len(drawHeads)] {
		t.Errorf("the end's head after %q: %q", was, got)
	}
	was = head(st.ed.Scene.Find(ar.ID).StartArrowhead)
	for range drawHeads {
		h.clickButton("drawOpths")
	}
	if got := head(st.ed.Scene.Find(ar.ID).StartArrowhead); got != was {
		t.Errorf("round the heads: %q, was %q", got, was)
	}
	st.ed.Select(tx.ID)
	h.frame()
	h.clickButton("drawOptffCode")
	h.clickButton("drawOptfzXL")
	h.clickButton("drawOpttaright")
	if x := st.ed.Scene.Find(tx.ID); x.FontFamily != draw.FontComic || x.FontSize != 36 || x.TextAlign != "right" {
		t.Errorf("text: %+v", x)
	}
	st.ed.Select(r.ID, ar.ID)
	h.frame()
	h.clickButton("drawToBack")
	if st.ed.Scene.Elements[0].ID != r.ID {
		t.Error("to back")
	}
	h.clickButton("drawFront")
	h.clickButton("drawBackward")
	h.clickButton("drawForward")
	h.clickButton("drawDup")
	if len(st.ed.Scene.Elements) != 5 {
		t.Error("duplicate")
	}
	h.clickButton("drawDel")
	if len(st.ed.Scene.Elements) != 3 {
		t.Error("delete")
	}
	st.ed.SetTool(draw.ToolArrow) // no selection, an arrow tool: the panel for what comes
	h.frame()
	was = head(st.ed.Style.EndArrowhead)
	h.clickButton("drawOpthe")
	if got := head(st.ed.Style.EndArrowhead); got != drawHeads[(indexOf(drawHeads, was)+1)%len(drawHeads)] {
		t.Errorf("the next arrow's head after %q: %q", was, got)
	}
	if drawFont(draw.FontLilita) == drawFont(draw.FontNunito) || drawFont(draw.FontExcalifont) != drawFont(draw.FontVirgil) {
		t.Error("drawFont")
	}
}

func indexOf(l []string, s string) int {
	for i, v := range l {
		if v == s {
			return i
		}
	}
	return -1
}

func TestDrawingSelectionDrawn(t *testing.T) {
	h := newHarness(t)
	r := rect(0, 0, 100, 100, "#ffc9c9")
	l := draw.New("line")
	l.X, l.Y, l.Points = 0, 200, [][2]float64{{0, 0}, {100, 50}}
	st, o := h.drawing("D", r, l)
	st.ed.Select(r.ID)
	h.frame()
	st.ed.Select(l.ID)
	h.frame()
	st.ed.Select(r.ID, l.ID)
	h.frame()
	st.ed.Select()
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: o.Add(f32.Pt(500, 500))},
		pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: o.Add(f32.Pt(-20, -20))})
	if len(st.ed.Sel) != 2 {
		t.Errorf("box selected %d", len(st.ed.Sel))
	}
	h.send(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: o.Add(f32.Pt(-20, -20))})
	st.dark()
	st.ed.Scene.AppState["theme"] = "dark"
	st.ed.Select(r.ID)
	h.frame()
	for tool, c := range map[string]pointer.Cursor{draw.ToolText: pointer.CursorText, draw.ToolEllipse: pointer.CursorCrosshair, draw.ToolSelect: pointer.CursorDefault} {
		st.ed.Tool = tool
		if st.cursor() != c {
			t.Errorf("%s: cursor %v", tool, st.cursor())
		}
	}
}

func TestDrawingRasters(t *testing.T) {
	h := newHarness(t)
	tx := draw.New("text")
	tx.Text = "Hi"
	tx.FitText()
	st, _ := h.drawing("D", rect(0, 0, 100, 100, "#ffc9c9"), tx)
	if len(st.rasters) != 2 {
		t.Fatalf("%d rasters", len(st.rasters))
	}
	r := st.rasters[tx.ID]
	st.ed.Scene.Elements[1].X += 50
	st.ed.Gen++
	h.frame()
	if st.rasters[tx.ID] != r {
		t.Error("a move rendered it again")
	}
	st.ed.Scene.Elements[1].Text = "Hello"
	st.ed.Gen++
	h.frame()
	if st.rasters[tx.ID] == r {
		t.Error("a change kept the old picture")
	}
	st.ed.Scene.Elements = st.ed.Scene.Elements[:1]
	st.ed.Gen++
	h.frame()
	if len(st.rasters) != 1 {
		t.Error("a gone element's picture kept")
	}
	big := rect(0, 0, 5000, 10, "")
	st.ed.Scene.Elements = []*draw.Element{big}
	st.zoom = 4
	st.ed.Gen++
	h.frame()
	if rs := st.rasters[big.ID]; rs.size.X > 4096 || rs.size.X < 4000 {
		t.Errorf("a huge shape: %v", rs.size)
	}
	tall := rect(0, 1000, 10, 5000, "")
	st.ed.Scene.Elements = []*draw.Element{tall}
	st.ed.Gen++
	h.frame()
	if rs := st.rasters[tall.ID]; rs.size.Y > 4096 || rs.size.Y < 4000 {
		t.Errorf("a huge tall shape away from the origin: %v", rs.size)
	}
	if quarter(1) != 1 || quarter(2) != 2 || quarter(1.04) != 1 {
		t.Error("quarter")
	}
}

// areaOf is where button id is: the clickable area whose middle hovers it.
func (h *harness) areaOf(id string) image.Rectangle {
	h.t.Helper()
	h.frame()
	for _, n := range h.r.AppendSemantics(nil) {
		if n.Desc.Gestures&input.ClickGesture == 0 {
			continue
		}
		c := n.Desc.Bounds.Min.Add(n.Desc.Bounds.Max).Div(2)
		h.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(float32(c.X), float32(c.Y))})
		h.frame()
		if h.u.k.Click(id).Hovered() {
			return n.Desc.Bounds
		}
	}
	h.t.Fatalf("button %s is not on screen", id)
	return image.Rectangle{}
}

func TestDrawReadBumps(t *testing.T) {
	h := newHarness(t)
	g := h.u.drawGen
	h.u.drawRead()
	if h.u.drawGen <= g {
		t.Error("a list read again draws nothing new")
	}
}

func TestDrawingNameCounts(t *testing.T) {
	h := newHarness(t)
	h.drawing("Untitled")
	h.drawing("Untitled 2")
	if n := drawingName(h.u.drawingsDir("Acme"), "Untitled"); n != "Untitled 3" {
		t.Errorf("next: %s", n)
	}
}

func TestDrawingTitleIsOneLine(t *testing.T) {
	h := newHarness(t)
	st, _ := h.drawing("D")
	h.r.Source().Execute(key.FocusCmd{Tag: &st.title})
	h.frame()
	h.send(key.EditEvent{Text: "a\nb"})
	if strings.Contains(st.title.Text(), "\n") {
		t.Errorf("a name with a line break: %q", st.title.Text())
	}
}

func TestDrawingTitleKeepsWhileTyping(t *testing.T) {
	h := newHarness(t)
	st, _ := h.drawing("D")
	h.r.Source().Execute(key.FocusCmd{Tag: &st.title})
	h.frame()
	st.title.SetText("Half")
	h.frame()
	h.frame()
	if drawName(st.path) != "D" {
		t.Errorf("renamed while typing: %s", st.path)
	}
	h.send(press(key.NameReturn, 0))
	if drawName(st.path) != "Half" || h.r.Source().Focused(&st.title) {
		t.Errorf("enter renames and leaves the field: %s, focused %v", st.path, h.r.Source().Focused(&st.title))
	}
}

func TestDrawingThumbEmptyCached(t *testing.T) {
	h := newHarness(t)
	h.drawing("Empty")
	it := h.u.drawings("Acme")[0]
	for range 2 {
		if _, ok := h.u.thumb(it); ok {
			t.Error("an empty drawing has a picture")
		}
	}
}

func TestDrawingListColumns(t *testing.T) {
	for _, c := range []struct {
		width, items int
		lastRow      bool
	}{
		{1085, 2, false}, // 812 wide inside: three cards (the New card and two) in a row exactly
		{1273, 3, true},  // 1000 wide: three a row, the fourth below
	} {
		h := newHarness(t)
		h.size = image.Pt(c.width, 900)
		for i := range c.items {
			h.drawing(fmt.Sprint("D", i))
			p := filepath.Join(h.u.drawingsDir("Acme"), fmt.Sprint("D", i, ".excalidraw"))
			at := time.Now().Add(-time.Duration(i) * time.Hour)
			os.Chtimes(p, at, at)
		}
		h.u.drawRead()
		h.u.view = "dr:Acme"
		first := h.areaOf("drNewAcme")
		last := h.areaOf("drDel" + filepath.Join(h.u.drawingsDir("Acme"), fmt.Sprint("D", c.items-1, ".excalidraw")))
		if below := last.Min.Y > first.Max.Y; below != c.lastRow {
			t.Errorf("%d wide, %d drawings: the last below the first %v", c.width, c.items, below)
		}
		if c.lastRow {
			continue
		}
		second := h.areaOf("drDel" + filepath.Join(h.u.drawingsDir("Acme"), "D0.excalidraw"))
		if gap := last.Min.X - second.Min.X; gap != 260+16 {
			t.Errorf("cards %d apart", gap)
		}
	}
}

func TestDrawingRastersSkip(t *testing.T) {
	h := newHarness(t)
	gone := rect(0, 0, 50, 50, "#ffc9c9")
	gone.IsDeleted = true
	far := rect(10000, 10000, 100, 100, "#ffc9c9")
	st, _ := h.drawing("D", gone, far)
	if _, ok := st.rasters[gone.ID]; ok {
		t.Error("a deleted element drawn")
	}
	if r := st.rasters[far.ID]; r == nil || r.scaled != 1 {
		t.Errorf("a small shape far away drawn smaller: %+v", r)
	}
}

func TestDrawingSaveWaits(t *testing.T) {
	h := newHarness(t)
	st, o := h.drawing("D")
	h.send(press("R", 0))
	a := o.Add(f32.Pt(500, 100))
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: a},
		pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: a.Add(f32.Pt(100, 100))})
	if s, _ := draw.Load(st.path); len(s.Elements) != 0 {
		t.Error("saved mid-drag")
	}
	h.send(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: a.Add(f32.Pt(100, 100))})
	if s, _ := draw.Load(st.path); len(s.Elements) != 1 {
		t.Fatal("not saved on release")
	}
	g := h.u.drawGen
	st.ed.Select() // a change of nothing in the file
	h.frame()
	if h.u.drawGen != g {
		t.Error("written again without a change")
	}
	st.ed.Tool = draw.ToolText
	st.ed.Down(draw.Pt{X: 600, Y: 400}, 4, false)
	st.ed.Up()
	st.ed.SetText("x")
	h.frame()
	if s, _ := draw.Load(st.path); len(s.Elements) != 1 {
		t.Error("saved while typing")
	}
}

func TestDrawingPlacedOnce(t *testing.T) {
	h := newHarness(t)
	s := draw.NewScene()
	s.Elements = append(s.Elements, rect(0, 0, 100, 100, "#fff"))
	path := filepath.Join(h.u.drawingsDir("Acme"), "D.excalidraw")
	s.Save(path)
	h.u.openDrawing("Acme", path)
	h.frame()
	st := h.u.drawOpen()
	if !st.placed {
		t.Fatal("not placed")
	}
	st.pan = f32.Pt(3, 4)
	h.frame()
	if st.pan != f32.Pt(3, 4) {
		t.Error("fitted again")
	}
}

func TestDrawingDoubleClickNear(t *testing.T) {
	h := newHarness(t)
	st, o := h.drawing("D")
	for _, d := range []f32.Point{{X: 0, Y: 100}, {X: 100, Y: 0}} {
		a := o.Add(f32.Pt(600, 300))
		for _, p := range []f32.Point{a, a.Add(d)} {
			h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p},
				pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
		}
		if st.ed.Editing != "" {
			t.Errorf("two clicks %v apart typed", d)
			st.ed.EndText()
		}
		st.pressAt = time.Time{}
	}
	a := o.Add(f32.Pt(600, 300))
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: a},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: a})
	st.pressAt = st.pressAt.Add(-time.Second)
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: a},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: a})
	if st.ed.Editing != "" {
		t.Error("two slow clicks typed")
	}
}

func TestDrawKeysOff(t *testing.T) {
	h := newHarness(t)
	st, _ := h.drawing("D")
	gtx := layout.Context{Source: h.r.Source()}
	if h.u.drawKeysOff(gtx) {
		t.Fatal("off with nothing open")
	}
	for name, set := range map[string]func(bool){
		"sheet":   func(on bool) { h.u.edit = map[bool]*sheet{true: {}}[on] },
		"palette": func(on bool) { h.u.pal = map[bool]*palette{true: {}}[on] },
		"config":  func(on bool) { h.u.cfg = map[bool]*cfgDialog{true: {}}[on] },
		"unlock":  func(on bool) { h.u.unlock = map[bool]*unlockDialog{true: {}}[on] },
		"picker":  func(on bool) { h.u.pick = map[bool]string{true: "Acme"}[on] },
	} {
		set(true)
		if !h.u.drawKeysOff(gtx) {
			t.Errorf("%s open: the keys still draw", name)
		}
		set(false)
	}
	for name, tag := range map[string]any{"search": &h.u.search, "name": &st.title} {
		h.r.Source().Execute(key.FocusCmd{Tag: tag})
		h.frame()
		if !h.u.drawKeysOff(gtx) {
			t.Errorf("%s typing: the keys still draw", name)
		}
		h.r.Source().Execute(key.FocusCmd{})
		h.frame()
	}
}

func TestDrawingKeysWantMods(t *testing.T) {
	h := newHarness(t)
	a, b := rect(500, 0, 50, 50, "#fff"), rect(600, 0, 50, 50, "#fff")
	st, _ := h.drawing("D", a, b)
	st.ed.Select(b.ID)
	h.send(press("[", 0))
	if st.ed.Scene.Elements[1].ID != b.ID {
		t.Error("[ without Ctrl moved it back")
	}
	h.send(press("E", key.ModAlt))
	if st.dark() {
		t.Error("Alt+E darkened the canvas")
	}
	h.send(press("D", key.ModShift))
	if st.dark() {
		t.Error("Shift+D darkened the canvas")
	}
}

func TestDrawingEnterEditsOneText(t *testing.T) {
	h := newHarness(t)
	tx := draw.New("text")
	tx.X, tx.Text = 500, "Hi"
	tx.FitText()
	st, _ := h.drawing("D", tx, rect(600, 0, 50, 50, "#fff"))
	st.ed.SelectAll()
	h.send(press(key.NameReturn, 0))
	if st.ed.Editing != "" {
		t.Error("enter with two selected typed into the text")
	}
}

func TestDrawingFitAway(t *testing.T) {
	for _, c := range []struct{ x, y, w, h float64 }{{1000, 2000, 100, 50}, {-3000, 500, 4000, 200}, {700, -900, 100, 3000}} {
		st := &drawState{ed: draw.NewEditor(draw.NewScene())}
		st.ed.Scene.Elements = []*draw.Element{rect(c.x, c.y, c.w, c.h, "#fff")}
		size := image.Pt(800, 600)
		st.fit(size, 1)
		x0, y0, x1, y1 := st.ed.Scene.Bounds()
		wantZ := float32(math.Min(math.Min((800-120)/(x1-x0), (600-120)/(y1-y0)), 1))
		if math.Abs(float64(st.zoom-wantZ)) > 1e-4 {
			t.Errorf("%v: zoom %v, want %v", c, st.zoom, wantZ)
		}
		mid := st.toPane(draw.Pt{X: (x0 + x1) / 2, Y: (y0 + y1) / 2}, 1)
		if math.Abs(float64(mid.X-400)) > .5 || math.Abs(float64(mid.Y-300)) > .5 {
			t.Errorf("%v: its middle at %v", c, mid)
		}
	}
}

func TestDrawingToolsPlaced(t *testing.T) {
	h := newHarness(t)
	st, o := h.drawing("D", rect(500, 0, 50, 50, "#fff"))
	st.ed.Select(st.ed.Scene.Elements[0].ID)
	hand, eraser := h.areaOf("drawTool"+draw.ToolHand), h.areaOf("drawTool"+draw.ToolEraser)
	if mid := (hand.Min.X + eraser.Max.X) / 2; abs(mid-(int(o.X)+h.size.X)/2) > 2 {
		t.Errorf("the toolbar's middle at %d, the canvas's %d", mid, (int(o.X)+h.size.X)/2)
	}
	if hand.Min.Y-int(o.Y) > 20 {
		t.Errorf("the toolbar %d down", hand.Min.Y-int(o.Y))
	}
	if sel, rc := h.areaOf("drawTool"+draw.ToolSelect), h.areaOf("drawTool"+draw.ToolRectangle); rc.Min.X-sel.Max.X != 9 {
		t.Errorf("the divider: %d", rc.Min.X-sel.Max.X)
	}
	if u := h.areaOf("drawUndo"); u.Max.Y < h.size.Y-25 {
		t.Errorf("undo at %v", u)
	}
	if f := h.areaOf("drawTheme"); f.Max.X < h.size.X-25 || f.Min.Y-int(o.Y) > 20 {
		t.Errorf("the theme at %v", f)
	}
	s0, s1 := h.areaOf("drawOpts"+drawStrokes[0]), h.areaOf("drawOpts"+drawStrokes[1])
	if s1.Min.X-s0.Max.X != 4 {
		t.Errorf("options %d apart", s1.Min.X-s0.Max.X)
	}
	if d := s0.Min.Y - int(o.Y); d < 62 || d > 110 {
		t.Errorf("the style panel's first option %d down", d)
	}
	if b0 := h.areaOf("drawOptb" + drawFills[0]); b0.Min.Y-s0.Max.Y < 25 {
		t.Errorf("sections %d apart", b0.Min.Y-s0.Max.Y)
	}
}
