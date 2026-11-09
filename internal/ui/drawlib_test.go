package ui

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/lfdominguez/desk/internal/draw"
)

func pngBytes(w, h int) []byte {
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)))
	return b.Bytes()
}

// pumped draws frames until done says the background work landed.
func (h *harness) pumped(done func() bool) {
	h.t.Helper()
	h.pumpUntil("background work", done)
}

func TestDrawImagePicked(t *testing.T) {
	h := newHarness(t)
	defer func(f func(...string) (io.ReadCloser, error)) { pickFile = f }(pickFile)
	st, _ := h.drawing("D")
	pickFile = func(...string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(pngBytes(1200, 600))), nil }
	h.clickButton("drawTool" + drawImageTool)
	h.pumped(func() bool { return len(st.ed.Scene.Elements) == 1 })
	e := st.ed.Scene.Elements[0]
	c := st.drawCenter()
	if e.Type != "image" || e.Width != drawMaxImage || e.Height != 300 || e.X+e.Width/2 != c.X || e.Y+e.Height/2 != c.Y {
		t.Errorf("in the middle of the view, at most %d: %+v (center %v)", drawMaxImage, e, c)
	}
	pickFile = func(...string) (io.ReadCloser, error) { return nil, nil } // cancelled
	h.send(press("9", 0))
	h.frame()
	pickFile = func(...string) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("text")), nil }
	h.send(press("9", 0))
	h.pumped(func() bool { return h.u.toast != "" })
	if !strings.HasPrefix(h.u.toast, "Couldn't add the image: not a picture") || len(st.ed.Scene.Elements) != 1 {
		t.Errorf("not a picture: %q", h.u.toast)
	}
	h.u.toast = ""
	pickFile = func(...string) (io.ReadCloser, error) { return nil, errors.New("no portal") }
	h.u.drawPickImage(st.path)
	h.pumped(func() bool { return h.u.toast != "" })
	if h.u.toast != "Couldn't open the image: no portal" {
		t.Errorf("%q", h.u.toast)
	}
	pickFile = func(...string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(pngBytes(2, 2))), nil }
	done := false
	h.u.drawPickImage("/gone.excalidraw")
	h.u.later(func() { done = true })
	h.pumped(func() bool { return done })
}

func TestDrawImagePasted(t *testing.T) {
	h := newHarness(t)
	st, _ := h.drawing("D")
	clipImage = func() ([]byte, string, error) { return nil, "", errors.New("text") }
	h.send(press("V", key.ModShortcut))
	done := false
	h.u.later(func() { done = true })
	h.pumped(func() bool { return done })
	if len(st.ed.Scene.Elements) != 0 {
		t.Fatal("no image: nothing")
	}
	clipImage = func() ([]byte, string, error) { return pngBytes(20, 10), "image/png", nil }
	h.send(press("V", key.ModShortcut))
	h.pumped(func() bool { return len(st.ed.Scene.Elements) == 1 })
	if e := st.ed.Scene.Elements[0]; e.Width != 20 || !st.ed.Sel[e.ID] {
		t.Errorf("%+v", e)
	}
}

func TestDrawFrameAndGroupKeys(t *testing.T) {
	h := newHarness(t)
	a, b := rect(0, 0, 50, 50, "#ffffff"), rect(100, 0, 50, 50, "#ffffff")
	st, _ := h.drawing("D", a, b)
	h.send(press("F", 0))
	if st.ed.Tool != draw.ToolFrame {
		t.Fatalf("F: %s", st.ed.Tool)
	}
	st.ed.Select(a.ID, b.ID)
	h.send(press("G", key.ModShortcut))
	if g := st.ed.Scene.Find(a.ID).GroupIDs; len(g) != 1 {
		t.Fatal("Ctrl+G groups")
	}
	h.send(press("G", key.ModShortcut|key.ModShift))
	if g := st.ed.Scene.Find(a.ID).GroupIDs; len(g) != 0 {
		t.Error("Ctrl+Shift+G ungroups")
	}
	h.clickButton("drawGroup")
	if len(st.ed.Scene.Find(b.ID).GroupIDs) != 1 {
		t.Fatal("the panel's Group")
	}
	h.clickButton("drawUngroup")
	if len(st.ed.Scene.Find(b.ID).GroupIDs) != 0 {
		t.Error("the panel's Ungroup")
	}
	st.ed.Select(a.ID)
	h.frame()
	_, g := h.exact("Group")
	_, ug := h.exact("Ungroup")
	if g || ug {
		t.Error("one ungrouped: nothing to group")
	}
}

func TestDrawFrameName(t *testing.T) {
	h := newHarness(t)
	st, _ := h.drawing("D")
	st.ed.SetTool(draw.ToolFrame)
	st.ed.Down(draw.Pt{X: 0, Y: 0}, 4, false)
	st.ed.Drag(draw.Pt{X: 200, Y: 100}, 4, false)
	st.ed.Up()
	f := st.ed.Scene.Elements[0]
	h.frame()
	if _, ok := h.exact("Stroke"); ok {
		t.Error("a frame has no stroke to pick")
	}
	if st.frameName.Text() != "Frame" {
		t.Fatalf("its name: %q", st.frameName.Text())
	}
	h.u.focusLater = nil
	h.r.Source().Execute(key.FocusCmd{Tag: &st.frameName})
	h.frame()
	if !h.u.drawTyping(layout.Context{Source: h.r.Source()}) {
		t.Error("typing")
	}
	st.frameName.SetText("Login")
	h.send(press(key.NameReturn, 0))
	if f = st.ed.Scene.Find(f.ID); *f.Name != "Login" {
		t.Fatalf("Enter renames: %q", *f.Name)
	}
	st.ed.Undo()
	h.frame()
	if f = st.ed.Scene.Find(f.ID); *f.Name != "Frame" || st.frameName.Text() != "Frame" {
		t.Errorf("an undo step, the field follows: %q %q", *f.Name, st.frameName.Text())
	}
	h.r.Source().Execute(key.FocusCmd{Tag: &st.frameName})
	h.frame()
	st.frameName.SetText("  ")
	h.r.Source().Execute(key.FocusCmd{})
	h.frame()
	h.frame()
	if f = st.ed.Scene.Find(f.ID); *f.Name != "Frame" || st.frameName.Text() != "Frame" {
		t.Error("an empty name keeps the old")
	}
	h.r.Source().Execute(key.FocusCmd{Tag: &st.frameName})
	h.frame()
	st.frameName.SetText("Flow")
	h.r.Source().Execute(key.FocusCmd{})
	h.frame()
	h.frame()
	if f = st.ed.Scene.Find(f.ID); *f.Name != "Flow" {
		t.Errorf("leaving the field renames: %q", *f.Name)
	}
	f.Name = nil
	st.nameOf = ""
	h.frame()
	if st.frameName.Text() != "Frame" {
		t.Error("no name reads Frame")
	}
}

func TestDrawFrameClipsOnCanvas(t *testing.T) {
	h := newHarness(t)
	f := draw.New("frame")
	f.Width, f.Height = 100, 100
	r := rect(50, 50, 200, 200, "#000000")
	r.FrameID = &f.ID
	st, _ := h.drawing("D", f, r)
	h.frame()
	if st.rasters[r.ID] == nil {
		t.Error("drawn")
	}
}

func TestLibrary(t *testing.T) {
	h := newHarness(t)
	a := rect(0, 0, 50, 50, "#ffc9c9")
	st, _ := h.drawing("D", a)
	h.clickButton("drawLib")
	if !st.libOpen {
		t.Fatal("open")
	}
	if _, ok := h.shown("Nothing here yet"); !ok {
		t.Error("empty")
	}
	st.ed.Select(a.ID)
	h.clickButton("libAdd")
	l := h.u.library("Acme")
	if len(l.lib.Items) != 1 {
		t.Fatal("added")
	}
	if m, _ := draw.LoadLibrary(h.u.libPath("Acme")); len(m.Items) != 1 {
		t.Error("kept beside the drawings")
	}
	if len(h.u.drawings("Acme")) != 1 {
		t.Error("the library isn't a drawing")
	}
	id := l.lib.Items[0].ID
	h.clickTop("libItem" + id)
	if n := len(st.ed.Scene.Elements); n != 2 {
		t.Fatalf("put in: %d", n)
	}
	if e := st.ed.Scene.Elements[1]; e.ID == a.ID || !st.ed.Sel[e.ID] {
		t.Error("a copy, selected")
	}
	if len(l.thumbs) != 1 {
		t.Error("its picture, once")
	}
	h.clickButton("libDel" + id)
	if len(l.lib.Items) != 0 {
		t.Fatal("removed")
	}
	h.u.runUndo()
	if len(l.lib.Items) != 1 || l.lib.Items[0].ID != id {
		t.Error("undone")
	}
	h.u.libRemove("Acme", "nope")
	st.ed.Select()
	h.u.libAdd(st)
	if len(l.lib.Items) != 1 {
		t.Error("nothing selected: nothing added")
	}
	h.clickButton("drawLib")
	if st.libOpen {
		t.Error("closed")
	}
}

func TestLibraryFails(t *testing.T) {
	h := newHarness(t)
	os.MkdirAll(h.u.drawingsDir("Acme"), 0o755)
	os.WriteFile(h.u.libPath("Acme"), []byte("{"), 0o644)
	st, _ := h.drawing("D", rect(0, 0, 10, 10, "#fff"))
	st.libOpen = true
	st.ed.SelectAll()
	h.frame()
	if _, ok := h.shown("Couldn't read the library: "); !ok {
		t.Error("said")
	}
	h.u.libAdd(st)
	if l := h.u.library("Acme"); l.err == nil || l.lib != nil {
		t.Error("an unreadable library is left alone")
	}
	delete(h.u.libs, "Acme")
	os.Remove(h.u.libPath("Acme"))
	os.Mkdir(h.u.libPath("Acme")+".tmp", 0o755) // where its write goes: a folder
	os.WriteFile(filepath.Join(h.u.libPath("Acme")+".tmp", "x"), nil, 0o644)
	h.u.libAdd(st)
	if !strings.HasPrefix(h.u.toast, "Couldn't keep the library: ") || !h.u.toastBad {
		t.Errorf("%q", h.u.toast)
	}
}

func TestLibraryMovesSuggestions(t *testing.T) {
	h := newHarness(t)
	st, _ := suggested(h, "infra:Acme", "Infra Claude")
	at := h.areaOf("sgOKsg1")
	st.libOpen = true
	h.frame()
	if moved := h.areaOf("sgOKsg1"); at.Min.X-moved.Min.X != 240+12 {
		t.Errorf("left of the library: %v → %v", at, moved)
	}
}
