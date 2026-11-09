package draw

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// shapes is an editor over rectangles a (0,0 100×100), b (200,0) and c
// (400,0), each 100 square, filled so a click inside hits them.
func shapes() (*Editor, *Element, *Element, *Element) {
	var els []*Element
	for i := range 3 {
		e := New("rectangle")
		e.X, e.Width, e.Height, e.BackgroundColor = float64(i*200), 100, 100, "#ffc9c9"
		els = append(els, e)
	}
	s := NewScene()
	s.Elements = els
	return NewEditor(s), els[0], els[1], els[2]
}

func sel(ed *Editor) []string {
	var ids []string
	for id := range ed.Sel {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func idsOf(els ...*Element) []string {
	var out []string
	for _, e := range els {
		out = append(out, e.ID)
	}
	slices.Sort(out)
	return out
}

func TestGroupSelectsWhole(t *testing.T) {
	ed, a, b, c := shapes()
	ed.Select(a.ID, b.ID)
	ed.Group()
	if len(a.GroupIDs) != 1 || a.GroupIDs[0] != b.GroupIDs[0] || len(c.GroupIDs) != 0 {
		t.Fatalf("grouped: %v %v %v", a.GroupIDs, b.GroupIDs, c.GroupIDs)
	}
	ed.Select()
	drag(ed, ToolSelect, pt(50, 50), pt(60, 60), false)
	if !slices.Equal(sel(ed), idsOf(a, b)) || a.X != 10 || b.X != 210 {
		t.Fatalf("a click takes the group, a drag moves it: %v %v %v", sel(ed), a.X, b.X)
	}
	ed.Down(pt(450, 50), 4, true)
	ed.Up()
	if !slices.Equal(sel(ed), idsOf(a, b, c)) {
		t.Errorf("shift adds: %v", sel(ed))
	}
	ed.Down(pt(260, 60), 4, true)
	ed.Up()
	if !slices.Equal(sel(ed), idsOf(c)) {
		t.Errorf("shift takes the whole group off: %v", sel(ed))
	}
	ed.Select(a.ID, b.ID, c.ID)
	ed.Group()
	if len(a.GroupIDs) != 2 || a.GroupIDs[1] != c.GroupIDs[0] {
		t.Fatalf("nested: %v %v", a.GroupIDs, c.GroupIDs)
	}
	ed.Ungroup()
	if len(a.GroupIDs) != 1 || len(c.GroupIDs) != 0 {
		t.Errorf("ungroup takes the outermost: %v %v", a.GroupIDs, c.GroupIDs)
	}
	ed.Undo()
	if a = ed.Scene.Find(a.ID); len(a.GroupIDs) != 2 {
		t.Error("an undo step")
	}
	ed.Select(c.ID)
	n := len(ed.undo)
	ed.Group()
	ed.Select()
	ed.Ungroup()
	if len(ed.undo) != n {
		t.Error("one element, or none: no step")
	}
}

func TestBoxTakesWholeGroups(t *testing.T) {
	ed, a, b, c := shapes()
	ed.Select(a.ID, b.ID)
	ed.Group()
	ed.Select()
	drag(ed, ToolSelect, pt(-10, -10), pt(150, 150), false)
	if len(ed.Sel) != 0 {
		t.Errorf("half a group is none of it: %v", sel(ed))
	}
	drag(ed, ToolSelect, pt(-10, -10), pt(320, 150), false)
	if !slices.Equal(sel(ed), idsOf(a, b)) {
		t.Errorf("%v", sel(ed))
	}
	_ = c
}

func TestLabelSelectsItsShape(t *testing.T) {
	ed, a, _, _ := shapes()
	ed.Apply(Patch{Update: rawAll(`{"id":"` + a.ID + `","label":"Hello there"}`)})
	ed.Select()
	drag(ed, ToolSelect, pt(50, 50), pt(80, 50), false)
	if !slices.Equal(sel(ed), []string{a.ID}) || a.X != 30 {
		t.Fatalf("a click on the label is on its shape: %v", sel(ed))
	}
	for _, e := range ed.Scene.Elements {
		if e.ContainerID != nil && e.X+e.Width/2 != 80 {
			t.Errorf("its label came along: %v", e.X+e.Width/2)
		}
	}
	drag(ed, ToolSelect, pt(-50, -50), pt(150, 150), false)
	if !slices.Equal(sel(ed), []string{a.ID}) {
		t.Errorf("a box takes the shape, not its label: %v", sel(ed))
	}
	ed.Duplicate()
	if len(ed.Scene.Elements) != 6 || len(ed.Sel) != 1 {
		t.Fatalf("duplicated with its label: %d, %v", len(ed.Scene.Elements), sel(ed))
	}
	cp := ed.Scene.Find(sel(ed)[0])
	lbl := ed.Scene.Elements[5]
	if lbl.ContainerID == nil || *lbl.ContainerID != cp.ID || len(cp.BoundElements) != 1 || cp.BoundElements[0].ID != lbl.ID {
		t.Errorf("the copy's label is the copy's: %+v %+v", lbl.ContainerID, cp.BoundElements)
	}
}

func rawAll(s ...string) (out []json.RawMessage) {
	for _, x := range s {
		out = append(out, json.RawMessage(x))
	}
	return out
}

func TestFrames(t *testing.T) {
	ed, a, b, c := shapes()
	drag(ed, ToolFrame, pt(-20, -20), pt(320, 140), false)
	f := ed.Scene.Elements[0]
	if f.Type != "frame" || f.Name == nil || *f.Name != "Frame" || f.Roughness != 0 || f.BackgroundColor != "transparent" {
		t.Fatalf("a frame, behind: %+v", f)
	}
	if a.FrameID == nil || *a.FrameID != f.ID || b.FrameID == nil || c.FrameID != nil {
		t.Fatalf("it holds what it went round: %v %v %v", a.FrameID, b.FrameID, c.FrameID)
	}
	ed.Select(f.ID)
	ed.Nudge(10, 0)
	if f.X != -10 || a.X != 10 || b.X != 210 || c.X != 400 {
		t.Errorf("its elements move with it: %v %v %v %v", f.X, a.X, b.X, c.X)
	}
	ed.Down(pt(-10, 50), 4, false) // its left edge
	ed.Drag(pt(-10, 150), 4, false)
	ed.Up()
	if a.Y != 100 || b.Y != 100 || c.Y != 0 {
		t.Errorf("dragged with it: %v %v %v", a.Y, b.Y, c.Y)
	}
	ed.Select(c.ID)
	ed.Nudge(-200, 100) // into the frame
	if c.FrameID == nil || *c.FrameID != f.ID {
		t.Error("moved into the frame: in it")
	}
	drag(ed, ToolSelect, pt(50, 150), pt(50, 450), false) // a, out of it
	if a.FrameID != nil {
		t.Error("moved out of it: out")
	}
	ed.Select(f.ID)
	ed.Duplicate()
	if n := len(ed.Scene.Elements); n != 7 { // it holds b and c now
		t.Fatalf("a frame duplicates with what it holds: %d", n)
	}
	ed.Undo()
	ed.Select(b.ID)
	ed.Duplicate()
	if cp := ed.Scene.Find(sel(ed)[0]); cp.FrameID == nil || *cp.FrameID != f.ID {
		t.Error("a copy stays in its frame")
	}
	cs := ed.Copies()
	ed.Undo()
	ed.Insert(cs, nil, pt(2000, 2000))
	if cp := ed.Scene.Find(sel(ed)[0]); cp.FrameID != nil {
		t.Error("put in elsewhere, out of any frame")
	}
	ed.Undo()
	ed.Select(f.ID)
	ed.Delete()
	if len(ed.Scene.Elements) != 1 || ed.Scene.Elements[0].ID != a.ID { // undo made new ones: by id
		t.Errorf("deleted with what it holds: %d", len(ed.Scene.Elements))
	}
	ed.Undo()
	f = ed.Scene.Elements[0]
	ed.Apply(Patch{Delete: []string{f.ID}})
	for _, e := range ed.Scene.Elements {
		if e.FrameID != nil {
			t.Errorf("a frame deleted alone lets its elements go: %s", e.ID)
		}
	}
}

func TestReframeSkips(t *testing.T) {
	ed, a, _, _ := shapes()
	ed.reframe(map[string]*Element{"gone": nil})
	f := New("frame")
	f.X, f.Y, f.Width, f.Height = -10, -10, 500, 500
	ed.Scene.Elements = append([]*Element{f}, ed.Scene.Elements...)
	ed.reframe(map[string]*Element{f.ID: f})
	if f.FrameID != nil {
		t.Error("a frame is in no frame")
	}
	ed.Apply(Patch{Update: rawAll(`{"id":"` + a.ID + `","label":"x"}`)})
	l := ed.Scene.Elements[len(ed.Scene.Elements)-1]
	ed.reframe(map[string]*Element{l.ID: l})
	if l.FrameID != nil {
		t.Error("a label is its shape's")
	}
	ed.adopt(f)
	if l.FrameID != nil || a.FrameID == nil {
		t.Error("adopt takes shapes, not labels")
	}
}

func png1(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func TestAddImage(t *testing.T) {
	ed := NewEditor(NewScene())
	ed.Scene.Files = nil
	if _, err := ed.AddImage([]byte("nope"), pt(0, 0), 500); err == nil || !strings.Contains(err.Error(), "not a picture") {
		t.Error(err)
	}
	e, err := ed.AddImage(png1(1000, 500), pt(100, 100), 500)
	if err != nil {
		t.Fatal(err)
	}
	if e.Width != 500 || e.Height != 250 || e.X != -150 || e.Y != -25 || !ed.Sel[e.ID] {
		t.Errorf("fitted round the point: %+v", e)
	}
	f := ed.Scene.Files[*e.FileID]
	if f == nil || f.MimeType != "image/png" || !strings.HasPrefix(f.DataURL, "data:image/png;base64,") || len(*e.FileID) != 40 {
		t.Fatalf("%+v", f)
	}
	small, _ := ed.AddImage(png1(10, 20), pt(0, 0), 500)
	if small.Width != 10 || small.Height != 20 {
		t.Error("small stays its size")
	}
	again, _ := ed.AddImage(png1(1000, 500), pt(0, 0), 500)
	if *again.FileID != *e.FileID || len(ed.Scene.Files) != 2 {
		t.Error("the same picture is one file")
	}
	if img := Render(ed.Scene, Options{}); img.Bounds().Dx() < 500 {
		t.Error("renders")
	}
	ed.Undo()
	if len(ed.Scene.Elements) != 2 {
		t.Error("an undo step")
	}
}

func TestInsert(t *testing.T) {
	ed, a, b, _ := shapes()
	ed.Apply(Patch{Add: rawAll(`{"id":"ar","type":"arrow","from":"`+a.ID+`","to":"`+b.ID+`"}`), Update: rawAll(`{"id":"`+a.ID+`","label":"L"}`)})
	ed.Select(a.ID, b.ID, "ar")
	ed.Group()
	cs := ed.Copies()
	if len(cs) != 4 {
		t.Fatalf("with its label: %d", len(cs))
	}
	if ed.Insert(nil, nil, pt(0, 0)) != nil {
		t.Error("nothing")
	}
	n := len(ed.Scene.Elements)
	got := ed.Insert(cs, nil, pt(1000, 1000))
	if len(got) != 4 || len(ed.Scene.Elements) != n+4 || len(ed.Sel) != 3 {
		t.Fatalf("%d inserted, %v selected", len(got), sel(ed))
	}
	ins := ed.Scene.Elements[n:]
	box := boxOf(ins)
	if (box[0]+box[2])/2 != 1000 || (box[1]+box[3])/2 != 1000 {
		t.Errorf("centered: %v", box)
	}
	g := ins[0].GroupIDs
	if len(g) != 2 || g[0] == a.GroupIDs[0] || ins[1].GroupIDs[0] != g[0] {
		t.Errorf("their own groups, one more round them all: %v %v", g, a.GroupIDs)
	}
	for _, e := range ins {
		if e.ContainerID != nil && len(e.GroupIDs) != 1 {
			t.Error("a label isn't grouped again")
		}
		if slices.Contains([]string{a.ID, b.ID, "ar"}, e.ID) {
			t.Error("new ids")
		}
	}
	var ar *Element
	for _, e := range ins {
		if e.Type == "arrow" {
			ar = e
		}
	}
	if ar.StartBinding == nil || ed.Scene.Find(ar.StartBinding.ElementID) == nil || ar.StartBinding.ElementID == a.ID {
		t.Error("the copy's arrow joins the copies")
	}
	ed.Undo()
	if len(ed.Scene.Elements) != n {
		t.Error("an undo step")
	}
	one := ed.Insert(cs[1:2], nil, pt(0, 0))
	if e := ed.Scene.Find(one[0]); len(e.GroupIDs) != 1 {
		t.Errorf("one element gets no group round it: %v", e.GroupIDs)
	}
	im := New("image")
	fid := "f1"
	im.FileID = &fid
	ed.Scene.Files = nil
	ed.Insert([]*Element{im}, map[string]*File{"f1": {ID: "f1", DataURL: "data:x"}}, pt(0, 0))
	if ed.Scene.Files["f1"] == nil {
		t.Error("an image brings its file")
	}
}

func TestLibrary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lib", "library.excalidrawlib")
	l, err := LoadLibrary(path)
	if err != nil || len(l.Items) != 0 || l.Type != "excalidrawlib" || l.Version != 2 {
		t.Fatal(l, err)
	}
	ed, a, _, _ := shapes()
	ed.Select(a.ID)
	l.Add(ed.Copies())
	l.Add(ed.Copies())
	l.Items[0].Name = "Box"
	if err := l.Save(path); err != nil {
		t.Fatal(err)
	}
	m, err := LoadLibrary(path)
	if err != nil || len(m.Items) != 2 || m.Items[0].Name != "Box" || m.Items[0].Status != "unpublished" || m.Items[1].Elements[0].ID != a.ID {
		t.Fatalf("%+v %v", m, err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"libraryItems":[`) {
		t.Errorf("excalidraw's format: %s", b[:80])
	}
	os.WriteFile(path, []byte("{"), 0o644)
	if _, err := LoadLibrary(path); err == nil {
		t.Error("bad JSON")
	}
	if _, err := LoadLibrary(t.TempDir()); err == nil {
		t.Error("a folder")
	}
	ro := t.TempDir()
	os.Chmod(ro, 0o555)
	defer os.Chmod(ro, 0o755)
	if l.Save(filepath.Join(ro, "x", "l.excalidrawlib")) == nil || l.Save(filepath.Join(ro, "l.excalidrawlib")) == nil {
		if os.Getuid() != 0 {
			t.Error("unwritable")
		}
	}
}

func TestFrameClips(t *testing.T) {
	f := New("frame")
	f.X, f.Y, f.Width, f.Height = 0, 0, 100, 100
	r := New("rectangle")
	r.X, r.Y, r.Width, r.Height, r.BackgroundColor, r.Roughness = 50, 50, 200, 200, "#000000", 0
	r.FrameID = &f.ID
	s := NewScene()
	s.Elements = []*Element{f, r}
	img := Render(s, Options{})
	in, out := img.RGBAAt(75-int(0), 75+frameNameSize+6), img.RGBAAt(175, 175+frameNameSize+6)
	if in.R > 50 || out.R < 200 {
		t.Errorf("drawn inside its frame only: %v %v", in, out)
	}
	svg := string(SVG(s, Options{}))
	if strings.Count(svg, `<clipPath id="frame-`+f.ID+`">`) != 1 || !strings.Contains(svg, `<g clip-path="url(#frame-`+f.ID+`)">`) {
		t.Errorf("svg clips: %s", svg[:200])
	}
	gone := "nope"
	r.FrameID = &gone
	if s.FrameOf(r) != nil {
		t.Error("a frame that isn't there")
	}
	r.FrameID = &r.ID
	if s.FrameOf(r) != nil {
		t.Error("only a frame frames")
	}
	r.FrameID = &f.ID
	l := New("text")
	l.ContainerID = &r.ID
	s.Elements = append(s.Elements, l)
	if s.FrameOf(l) != f {
		t.Error("a label is in its shape's frame")
	}
	gone2 := "gone"
	l.ContainerID = &gone2
	if s.FrameOf(l) != nil {
		t.Error("a label of no shape: its own")
	}
}
