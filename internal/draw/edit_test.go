package draw

import (
	"math"
	"strings"
	"testing"
)

// drag is a press at a, a move to b and the release, with the tool.
func drag(ed *Editor, tool string, a, b Pt, shift bool) {
	if tool != "" {
		ed.SetTool(tool)
	}
	ed.Down(a, 4, shift)
	ed.Drag(b, 4, shift)
	ed.Up()
}

func box(e *Element) [4]float64 { return [4]float64{e.X, e.Y, e.Width, e.Height} }

func nearPt(a, b Pt) bool { return math.Abs(a.X-b.X) < 1e-6 && math.Abs(a.Y-b.Y) < 1e-6 }

func TestEditorCreatesShapes(t *testing.T) {
	ed := NewEditor(NewScene())
	drag(ed, ToolRectangle, pt(100, 100), pt(40, 60), false)
	if len(ed.Scene.Elements) != 1 {
		t.Fatalf("%d elements", len(ed.Scene.Elements))
	}
	e := ed.Scene.Elements[0]
	if box(e) != [4]float64{40, 60, 60, 40} || e.Type != "rectangle" {
		t.Errorf("a drag up and left: %v", box(e))
	}
	if ed.Tool != ToolSelect || !ed.Sel[e.ID] || len(ed.Sel) != 1 {
		t.Errorf("made: tool %s, selection %v", ed.Tool, ed.Sel)
	}
	if !ed.CanUndo() || ed.CanRedo() {
		t.Error("one undo step")
	}
	ed.Undo()
	if len(ed.Scene.Elements) != 0 || len(ed.Sel) != 0 || !ed.CanRedo() {
		t.Errorf("undone: %d elements, selection %v", len(ed.Scene.Elements), ed.Sel)
	}
	ed.Redo()
	if len(ed.Scene.Elements) != 1 || box(ed.Scene.Elements[0]) != [4]float64{40, 60, 60, 40} {
		t.Error("redone")
	}
	drag(ed, ToolEllipse, pt(0, 0), pt(30, 80), true)
	if e := ed.Scene.Elements[1]; e.Width != 80 || e.Height != 80 {
		t.Errorf("shift keeps it round: %v", box(e))
	}
	drag(ed, ToolDiamond, pt(0, 0), pt(-30, 10), true)
	if e := ed.Scene.Elements[2]; box(e) != [4]float64{-30, 0, 30, 30} {
		t.Errorf("shift up-left: %v", box(e))
	}
}

func TestEditorClickMakesNothing(t *testing.T) {
	ed := NewEditor(NewScene())
	drag(ed, ToolRectangle, pt(10, 10), pt(11, 11), false)
	if len(ed.Scene.Elements) != 0 || ed.CanUndo() {
		t.Errorf("a click drew %d, undo %v", len(ed.Scene.Elements), ed.CanUndo())
	}
	drag(ed, ToolRectangle, pt(10, 10), pt(13, 10), false)
	if len(ed.Scene.Elements) != 1 {
		t.Error("3 wide is a shape")
	}
	ed = NewEditor(NewScene())
	drag(ed, ToolRectangle, pt(10, 10), pt(10, 13), false)
	if len(ed.Scene.Elements) != 1 {
		t.Error("3 tall is a shape")
	}
}

func TestEditorLines(t *testing.T) {
	ed := NewEditor(NewScene())
	drag(ed, ToolLine, pt(10, 20), pt(110, 70), false)
	e := ed.Scene.Elements[0]
	if e.X != 10 || e.Y != 20 || len(e.Points) != 2 || e.Points[1] != [2]float64{100, 50} || e.Width != 100 || e.Height != 50 {
		t.Errorf("line: %+v %v", box(e), e.Points)
	}
	if e.EndArrowhead != nil {
		t.Error("a line has a head")
	}
	drag(ed, ToolArrow, pt(0, 0), pt(100, 10), true)
	a := ed.Scene.Elements[1]
	if math.Abs(a.Points[1][1]) > 1e-9 || math.Abs(a.Points[1][0]-math.Hypot(100, 10)) > 1e-9 {
		t.Errorf("shift snaps to 0°: %v", a.Points)
	}
	if a.EndArrowhead == nil || *a.EndArrowhead != "arrow" || a.StartArrowhead != nil {
		t.Error("an arrow's head")
	}
	drag(ed, ToolArrow, pt(0, 0), pt(-50, -40), false)
	if b := ed.Scene.Elements[2]; b.X != 0 || b.Points[1] != [2]float64{-50, -40} || b.Width != 50 || b.Height != 40 {
		t.Errorf("up-left arrow: %+v %v", box(b), b.Points)
	}
}

func TestEditorFreedraw(t *testing.T) {
	ed := NewEditor(NewScene())
	ed.SetTool(ToolFreedraw)
	ed.Down(pt(10, 10), 4, false)
	for _, p := range []Pt{pt(20, 15), pt(30, 5), pt(25, 30)} {
		ed.Drag(p, 4, false)
	}
	ed.Up()
	e := ed.Scene.Elements[0]
	if len(e.Points) != 4 || e.Points[0] != [2]float64{0, 0} || e.Points[3] != [2]float64{15, 20} || e.Width != 20 || e.Height != 25 {
		t.Errorf("pen: %v %v", e.Points, box(e))
	}
	if ed.Tool != ToolFreedraw || len(ed.Sel) != 0 {
		t.Error("the pen stays the pen, nothing selected")
	}
	ed.Down(pt(50, 50), 4, false)
	ed.Up()
	if len(ed.Scene.Elements) != 2 {
		t.Error("a pen's dot stays")
	}
}

func TestEditorText(t *testing.T) {
	ed := NewEditor(NewScene())
	ed.SetTool(ToolText)
	ed.Down(pt(100, 100), 4, false)
	ed.Up()
	e := ed.Scene.Find(ed.Editing)
	if e == nil || e.Type != "text" || ed.Tool != ToolSelect || !ed.Sel[e.ID] {
		t.Fatalf("typing into %v", e)
	}
	if e.Y != 100-20*1.25/2 || e.X != 100 || e.FontFamily != FontExcalifont {
		t.Errorf("text centered on the click: %v", box(e))
	}
	if ed.CanUndo() {
		t.Error("an empty text made a step")
	}
	ed.SetText("hello\nworld!")
	if e.Text != "hello\nworld!" || e.OriginalText != e.Text || e.Height != 2*20*1.25 || e.Width <= 0 {
		t.Errorf("typed: %q %v", e.Text, box(e))
	}
	g := ed.Gen
	ed.SetText("hello\nworld!")
	if ed.Gen != g {
		t.Error("the same text changes it")
	}
	v := e.Version
	ed.EndText()
	if ed.Editing != "" || !ed.CanUndo() || e.Version != v+1 {
		t.Errorf("ended: editing %q, undo %v", ed.Editing, ed.CanUndo())
	}
	ed.Undo()
	if len(ed.Scene.Elements) != 0 {
		t.Error("the text is one step")
	}
	ed.Redo()
	ed.EndText() // nothing being typed
	ed.EditText(e.ID)
	if ed.Editing != e.ID {
		t.Fatal("edit again")
	}
	ed.SetText("")
	ed.EndText()
	if len(ed.Scene.Elements) != 0 {
		t.Error("an emptied text stays")
	}
	ed.EditText("nope")
	if ed.Editing != "" {
		t.Error("edits a missing text")
	}
	ed.SetText("x") // nothing being typed: nothing happens
}

func TestEditorTextToolEditsText(t *testing.T) {
	ed := NewEditor(NewScene())
	ed.SetTool(ToolText)
	ed.Down(pt(0, 0), 4, false)
	ed.Up()
	ed.SetText("Hi")
	ed.EndText()
	id := ed.Scene.Elements[0].ID
	ed.SetTool(ToolText)
	ed.Down(pt(5, 0), 4, false)
	ed.Up()
	if ed.Editing != id || len(ed.Scene.Elements) != 1 || ed.Tool != ToolSelect {
		t.Errorf("a text tool click on text edits it: %q", ed.Editing)
	}
}

func TestEditorStyleDefaults(t *testing.T) {
	ed := NewEditor(NewScene())
	ed.Style.Roundness, ed.Style.StrokeColor, ed.Style.Opacity = nil, "#e03131", 50
	s := "bar"
	ed.Style.StartArrowhead = &s
	for _, typ := range []string{ToolRectangle, ToolDiamond, ToolLine, ToolArrow} {
		if e := ed.styled(typ); e.Roundness != nil || e.StrokeColor != "#e03131" || e.Opacity != 50 {
			t.Errorf("%s: %+v", typ, e.Roundness)
		}
	}
	if e := ed.styled(ToolEllipse); e.Roundness != nil {
		t.Error("an ellipse got corners")
	}
	if a := ed.styled(ToolArrow); a.StartArrowhead == nil || *a.StartArrowhead != "bar" || a.EndArrowhead != nil {
		t.Error("the style's heads")
	}
	ed.Style.FontSize, ed.Style.FontFamily, ed.Style.TextAlign = 36, FontNunito, "center"
	if e := ed.styled(ToolText); e.FontSize != 36 || e.FontFamily != FontNunito || e.TextAlign != "center" {
		t.Errorf("text style: %v %v", e.FontSize, e.FontFamily)
	}
	if e := ed.styled(ToolRectangle); e.FontSize != 0 {
		t.Error("a rectangle took the font")
	}
	ed2 := NewEditor(NewScene())
	ed2.Style.Roundness = &Roundness{Type: RoundAdaptive}
	if e := ed2.styled(ToolRectangle); e.Roundness == nil {
		t.Error("rounded style lost")
	}
}

// two is a scene with a filled rectangle at 0,0 100×100 and a transparent
// one at 200,0.
func two() (*Editor, *Element, *Element) {
	ed := NewEditor(NewScene())
	ed.Style.BackgroundColor = "#a5d8ff"
	drag(ed, ToolRectangle, pt(0, 0), pt(100, 100), false)
	ed.Style.BackgroundColor = "transparent"
	drag(ed, ToolRectangle, pt(200, 0), pt(300, 100), false)
	ed.Select()
	return ed, ed.Scene.Elements[0], ed.Scene.Elements[1]
}

func TestEditorSelects(t *testing.T) {
	ed, a, b := two()
	ed.Down(pt(50, 50), 4, false)
	ed.Up()
	if !ed.Sel[a.ID] || len(ed.Sel) != 1 {
		t.Error("a click in a filled shape")
	}
	ed.Down(pt(250, 50), 4, false)
	ed.Up()
	if len(ed.Sel) != 0 {
		t.Error("a click inside a transparent shape selects it")
	}
	ed.Down(pt(201, 50), 4, true)
	ed.Up()
	ed.Down(pt(50, 50), 4, true)
	ed.Up()
	if !ed.Sel[a.ID] || !ed.Sel[b.ID] {
		t.Errorf("shift adds: %v", ed.Sel)
	}
	ed.Down(pt(50, 50), 4, true)
	ed.Up()
	if ed.Sel[a.ID] || !ed.Sel[b.ID] {
		t.Error("shift again takes it out")
	}
	ed.Down(pt(250, 400), 4, true)
	ed.Up()
	if !ed.Sel[b.ID] {
		t.Error("shift on empty space keeps the selection")
	}
	ed.Down(pt(-10, -10), 4, false)
	if ed.Box == nil || len(ed.Sel) != 0 {
		t.Fatal("a press on nothing starts a box, dropping the selection")
	}
	ed.Drag(pt(150, 150), 4, false)
	if !ed.Sel[a.ID] || ed.Sel[b.ID] {
		t.Errorf("the box holds a only: %v", ed.Sel)
	}
	ed.Drag(pt(310, 120), 4, false)
	if !ed.Sel[a.ID] || !ed.Sel[b.ID] {
		t.Error("then both")
	}
	ed.Up()
	if ed.Box != nil || ed.CanUndo() && len(ed.undo) != 2 {
		t.Error("the box stays, or made a step")
	}
	ed.Down(pt(150, 50), 4, false) // between them, inside the selection's box: moves both
	ed.Drag(pt(160, 70), 4, false)
	ed.Up()
	if a.X != 10 || a.Y != 20 || b.X != 210 || b.Y != 20 {
		t.Errorf("moved both: %v %v", box(a), box(b))
	}
}

func TestEditorBoxSelectReverse(t *testing.T) {
	ed, a, _ := two()
	ed.Down(pt(150, 150), 4, false)
	ed.Drag(pt(-10, -10), 4, false)
	ed.Up()
	if !ed.Sel[a.ID] || len(ed.Sel) != 1 {
		t.Errorf("a box dragged up-left: %v", ed.Sel)
	}
	a.Locked = true
	ed.Down(pt(150, 150), 4, false)
	ed.Drag(pt(-10, -10), 4, false)
	ed.Up()
	if len(ed.Sel) != 0 {
		t.Error("a locked shape in the box")
	}
}

func TestEditorMoves(t *testing.T) {
	ed, a, _ := two()
	drag(ed, "", pt(50, 50), pt(80, 40), false)
	if a.X != 30 || a.Y != -10 {
		t.Errorf("moved to %v", box(a))
	}
	ed.Undo()
	if a := ed.Scene.Elements[0]; a.X != 0 || a.Y != 0 {
		t.Error("undo the move")
	}
	if ed.Busy() {
		t.Error("busy after the release")
	}
	ed.Down(pt(50, 50), 4, false)
	if !ed.Busy() {
		t.Error("not busy pressing")
	}
	ed.Up()
	if len(ed.undo) != 2 {
		t.Error("a click without a move made a step")
	}
}

func TestHit(t *testing.T) {
	r := New("rectangle")
	r.Width, r.Height = 100, 50
	if !r.hit(pt(0, 25), 2) || !r.hit(pt(-2.5, 25), 2) || r.hit(pt(50, 25), 2) || r.hit(pt(-4, 25), 2) {
		t.Error("a transparent rectangle is its outline")
	}
	if !r.hit(pt(50, 50.5), 1) || !r.hit(pt(100, 10), 0) {
		t.Error("its bottom and right edges")
	}
	r.BackgroundColor = "#fff"
	if !r.hit(pt(50, 25), 0) {
		t.Error("a filled one is its inside")
	}
	r.BackgroundColor = ""
	if r.hit(pt(50, 25), 0) {
		t.Error("no color is transparent")
	}
	r.Angle = math.Pi / 2 // turned around its center (50, 25): now 50 wide, 100 tall
	if !r.hit(pt(25, -25), 1) || r.hit(pt(0, 0), 1) {
		t.Error("a turned rectangle")
	}
	tx := New("text")
	tx.Width, tx.Height = 40, 20
	if !tx.hit(pt(20, 10), 0) {
		t.Error("text is solid")
	}
	im := New("image")
	im.Width, im.Height = 40, 20
	if !im.hit(pt(20, 10), 0) {
		t.Error("an image is solid")
	}
	el := New("ellipse")
	el.Width, el.Height, el.StrokeWidth = 100, 50, 0
	if !el.hit(pt(100, 25), 1) || !el.hit(pt(50, 0), 1) || el.hit(pt(50, 25), 1) || el.hit(pt(0, 0), 1) {
		t.Error("an ellipse's outline")
	}
	el.BackgroundColor = "#fff"
	if !el.hit(pt(50, 25), 1) || !el.hit(pt(101, 25), 1.2) || el.hit(pt(105, 25), 1) || el.hit(pt(2, 2), 1) {
		t.Error("a filled ellipse")
	}
	d := New("diamond")
	d.Width, d.Height, d.StrokeWidth = 100, 50, 0
	if !d.hit(pt(25, 12.5), 1) || !d.hit(pt(50, 0), 1) || d.hit(pt(50, 25), 1) || d.hit(pt(5, 5), 1) {
		t.Error("a diamond's outline")
	}
	d.BackgroundColor = "#fff"
	if !d.hit(pt(50, 25), 1) || d.hit(pt(5, 5), 1) || !d.hit(pt(101, 25), 1.2) {
		t.Error("a filled diamond")
	}
	z := New("ellipse")
	z.BackgroundColor = "#fff"
	if !z.hit(pt(0, 0), 1) {
		t.Error("a sizeless ellipse")
	}
	l := New("line")
	l.Points, l.StrokeWidth = [][2]float64{{0, 0}, {100, 0}, {100, 100}}, 2
	if !l.hit(pt(50, 2), 1) || !l.hit(pt(102, 50), 1) || l.hit(pt(50, 50), 1) || l.hit(pt(50, 3), 1) {
		t.Error("a line is near its segments")
	}
	dot := New("freedraw")
	dot.Points, dot.StrokeWidth = [][2]float64{{0, 0}}, 0
	if !dot.hit(pt(1, 0), 1) || dot.hit(pt(2, 0), 1) {
		t.Error("a dot")
	}
	if segDist(pt(3, 4), pt(0, 0), pt(0, 0)) != 5 || segDist(pt(-3, 4), pt(0, 0), pt(10, 0)) != 5 || segDist(pt(13, 4), pt(0, 0), pt(10, 0)) != 5 || segDist(pt(5, 4), pt(0, 0), pt(10, 0)) != 4 {
		t.Error("segDist")
	}
}

func TestHitTestOrder(t *testing.T) {
	ed, a, _ := two()
	ed.Style.BackgroundColor = "#fff"
	drag(ed, ToolRectangle, pt(50, 50), pt(150, 150), false)
	c := ed.Scene.Elements[2]
	if ed.HitTest(pt(75, 75), 1) != c {
		t.Error("the topmost wins")
	}
	c.Locked = true
	if ed.HitTest(pt(75, 75), 1) != a {
		t.Error("a locked one is passed over")
	}
	a.IsDeleted = true
	if ed.HitTest(pt(25, 25), 1) != nil {
		t.Error("a deleted one is hit")
	}
}

func TestHandles(t *testing.T) {
	ed, a, b := two()
	if ed.Handles(5) != nil {
		t.Error("handles without a selection")
	}
	ed.Select(a.ID)
	hs := ed.Handles(5)
	want := map[string]Pt{"nw": pt(-5, -5), "n": pt(50, -5), "ne": pt(105, -5), "e": pt(105, 50), "se": pt(105, 105), "s": pt(50, 105), "sw": pt(-5, 105), "w": pt(-5, 50), "rotation": pt(50, -20)}
	if len(hs) != len(want) {
		t.Fatalf("%d handles", len(hs))
	}
	for _, h := range hs {
		if !nearPt(h.P, want[h.Name]) {
			t.Errorf("%s at %v, want %v", h.Name, h.P, want[h.Name])
		}
	}
	ed.Select(a.ID, b.ID)
	hs = ed.Handles(5)
	if len(hs) != 8 || !nearPt(hs[2].P, pt(305, -5)) || !nearPt(hs[6].P, pt(-5, 105)) {
		t.Errorf("several: %v", hs)
	}
	drag(ed, ToolLine, pt(0, 200), pt(50, 260), false)
	hs = ed.Handles(5)
	if len(hs) != 2 || hs[0].Name != "p0" || !nearPt(hs[1].P, pt(50, 260)) {
		t.Errorf("a line's points: %v", hs)
	}
	ed.Editing = "x"
	if ed.Handles(5) != nil {
		t.Error("handles while typing")
	}
}

func TestResize(t *testing.T) {
	ed, a, _ := two()
	ed.Select(a.ID)
	drag(ed, "", pt(105, 105), pt(205, 155), false) // se, 5 off the box
	if box(a) != [4]float64{0, 0, 200, 150} {
		t.Errorf("se: %v", box(a))
	}
	drag(ed, "", pt(-5, -5), pt(45, -55), false) // nw
	if box(a) != [4]float64{50, -50, 150, 200} {
		t.Errorf("nw: %v", box(a))
	}
	drag(ed, "", pt(205, 50), pt(355, 0), false) // e only
	if box(a) != [4]float64{50, -50, 300, 200} {
		t.Errorf("e: %v", box(a))
	}
	drag(ed, "", pt(200, 155), pt(200, -145), false) // s, past the top: flips
	if box(a) != [4]float64{50, -150, 300, 100} {
		t.Errorf("s flipped: %v", box(a))
	}
	drag(ed, "", pt(200, -155), pt(200, 45), false) // n, past the bottom: flips
	if box(a) != [4]float64{50, -50, 300, 100} {
		t.Errorf("n flipped: %v", box(a))
	}
	drag(ed, "", pt(45, 0), pt(-55, 0), false) // w
	if box(a) != [4]float64{-50, -50, 400, 100} {
		t.Errorf("w: %v", box(a))
	}
	ed.Undo()
	if box(ed.Scene.Elements[0]) != [4]float64{50, -50, 300, 100} {
		t.Error("each resize is a step")
	}
}

func TestResizeKeepsProportions(t *testing.T) {
	for _, c := range []struct {
		h    string
		from Pt
		to   Pt
		want [4]float64
	}{
		{"se", pt(105, 105), pt(305, 125), [4]float64{0, 0, 300, 300}},
		{"nw", pt(-5, -5), pt(-105, 45), [4]float64{-100, -100, 200, 200}},
		{"ne", pt(105, -5), pt(55, -55), [4]float64{0, -50, 150, 150}},
		{"sw", pt(-5, 105), pt(45, 125), [4]float64{-20, 0, 120, 120}},
	} {
		ed, a, _ := two()
		ed.Select(a.ID)
		drag(ed, "", c.from, c.to, true)
		if box(a) != c.want {
			t.Errorf("%s: %v, want %v", c.h, box(a), c.want)
		}
	}
	x0, y0, x1, y1 := grip("e", 0, 0, 10, 10, pt(10, 50), true)
	if x0 != 0 || y0 != 0 || x1 != 20 || y1 != 10 {
		t.Error("an edge grip keeps nothing")
	}
	x0, _, x1, _ = grip("se", 0, 0, 0, 10, pt(20, 40), true)
	if x0 != 0 || x1 != 20 {
		t.Error("a sizeless box")
	}
	_, y0, _, y1 = grip("se", 0, 0, 10, 0, pt(10, 50), true)
	if y0 != 0 || y1 != 50 {
		t.Error("a heightless box")
	}
	if scaleOf(5, 5, 0, 10) != 1 || scaleOf(0, 10, 0, 30) != 3 || sign(-2) != -1 || sign(0) != 1 {
		t.Error("scaleOf, sign")
	}
}

func TestResizeTurned(t *testing.T) {
	ed, a, _ := two()
	a.Angle = math.Pi / 2
	ed.Select(a.ID)
	far := a.Transform().Apply(pt(0, 0)) // the corner the se grip pulls from
	se := a.Transform().Apply(pt(105, 105))
	drag(ed, "", se, a.Transform().Apply(pt(205, 105)), false)
	if math.Abs(a.Width-200) > 1e-9 || math.Abs(a.Height-100) > 1e-9 {
		t.Errorf("resized in its own frame: %v", box(a))
	}
	if !nearPt(a.Transform().Apply(pt(0, 0)), far) {
		t.Errorf("the far corner moved: %v, was %v", a.Transform().Apply(pt(0, 0)), far)
	}
}

func TestResizeLinesAndText(t *testing.T) {
	ed := NewEditor(NewScene())
	ed.SetTool(ToolFreedraw)
	ed.Down(pt(0, 0), 4, false)
	ed.Drag(pt(100, 50), 4, false)
	ed.Up()
	f := ed.Scene.Elements[0]
	ed.SetTool(ToolSelect)
	ed.Select(f.ID)
	drag(ed, "", pt(105, 55), pt(205, 105), false)
	if f.Points[1] != [2]float64{200, 100} || f.Width != 200 {
		t.Errorf("pen scaled: %v", f.Points)
	}
	ed.SetTool(ToolText)
	ed.Down(pt(0, 300), 4, false)
	ed.Up()
	ed.SetText("Hi")
	ed.EndText()
	tx := ed.Scene.Elements[1]
	ed.Select(tx.ID)
	w0 := tx.Width
	x0, y0, x1, y1 := tx.local()
	_ = x0
	_ = y0
	drag(ed, "", tx.Transform().Apply(pt(x1+5, y1+5)), tx.Transform().Apply(pt(x1+5, y1*2+5)), false)
	if tx.FontSize != 40 || tx.Width <= w0 {
		t.Errorf("text grows by its height: %v %v", tx.FontSize, tx.Width)
	}
	drag(ed, "", tx.Transform().Apply(pt(tx.Width+5, tx.Height+5)), tx.Transform().Apply(pt(tx.Width+5, 5)), false)
	if tx.FontSize != 1 {
		t.Errorf("text shrinks to 1 at least: %v", tx.FontSize)
	}
}

func TestResizeSeveral(t *testing.T) {
	ed, a, b := two()
	ed.Select(a.ID, b.ID)
	drag(ed, "", pt(305, 105), pt(605, 205), false) // se of the 300×100 union: ×2
	if box(a) != [4]float64{0, 0, 200, 200} || box(b) != [4]float64{400, 0, 200, 200} {
		t.Errorf("scaled: %v %v", box(a), box(b))
	}
	drag(ed, ToolLine, pt(0, 300), pt(100, 400), false)
	l := ed.Scene.Elements[2]
	ed.Select(a.ID, l.ID)
	drag(ed, "", pt(-5, -5), pt(95, -5), false) // nw: the left edge in by 100 of 200
	if l.X != 100 || l.Points[1] != [2]float64{50, 100} || a.X != 100 || a.Width != 100 {
		t.Errorf("a line among them: %v %v, %v", box(l), l.Points, box(a))
	}
}

func TestRotate(t *testing.T) {
	ed, a, b := two()
	ed.Select(a.ID)
	drag(ed, "", pt(50, -20), pt(150, 50), false)
	if math.Abs(a.Angle-math.Pi/2) > 1e-9 {
		t.Errorf("a quarter turn: %v", a.Angle)
	}
	drag(ed, "", a.Transform().Apply(pt(50, -20)), pt(50+100*math.Cos(-0.2), 50+100*math.Sin(-0.2)), true)
	if math.Abs(a.Angle-5*math.Pi/12) > 1e-9 {
		t.Errorf("shift snaps to 15°: %v", a.Angle)
	}
	drag(ed, "", a.Transform().Apply(pt(50, -20)), pt(-50, 50), false)
	if math.Abs(a.Angle-3*math.Pi/2) > 1e-9 {
		t.Errorf("past half a turn stays positive: %v", a.Angle)
	}
	ed.Select(a.ID, b.ID)
	ed.g = gesture{kind: gRotate}
	ed.hold()
	ed.Drag(pt(0, 0), 4, false)
	if math.Abs(a.Angle-3*math.Pi/2) > 1e-9 {
		t.Error("several turned")
	}
}

func TestMovePoints(t *testing.T) {
	ed := NewEditor(NewScene())
	drag(ed, ToolLine, pt(0, 0), pt(100, 0), false)
	l := ed.Scene.Elements[0]
	drag(ed, "", pt(100, 0), pt(100, 50), false)
	if l.Points[1] != [2]float64{100, 50} {
		t.Errorf("the end moved: %v", l.Points)
	}
	drag(ed, "", pt(0, 0), pt(-20, 0), false)
	if l.X != -20 || l.Points[0] != [2]float64{0, 0} || l.Points[1] != [2]float64{120, 50} {
		t.Errorf("the start moved: %v %v", box(l), l.Points)
	}
	drag(ed, "", pt(100, 50), pt(110, 10), true)
	if p := l.Points[1]; math.Abs(p[1]) > 1e-9 {
		t.Errorf("shift snaps around its neighbour: %v", l.Points)
	}
	drag(ed, "", pt(-20, 0), pt(-20, 3), true)
	if p := l.Points[1]; math.Abs(p[1]) > 1e-9 || math.Abs(l.Y) > 1e-9 {
		t.Errorf("shift snaps the start around the end: %v %v", box(l), l.Points)
	}
	ed.g = gesture{kind: gPoint, handle: "p9"}
	ed.hold()
	ed.Drag(pt(1, 1), 4, false) // a point the line hasn't
	ed.g = gesture{kind: gPoint, handle: "px"}
	ed.hold()
	ed.Drag(pt(1, 1), 4, false)
}

func TestSnapAngle(t *testing.T) {
	if p := snapAngle(pt(10, 1)); !nearPt(p, pt(math.Hypot(10, 1), 0)) {
		t.Errorf("0°: %v", p)
	}
	if p := snapAngle(pt(10, 9)); math.Abs(math.Atan2(p.Y, p.X)-math.Pi/4) > 1e-9 {
		t.Errorf("45°: %v", p)
	}
}

func TestArrowBinds(t *testing.T) {
	ed, a, b := two()
	drag(ed, ToolArrow, pt(50, 50), pt(250, 50), false)
	ar := ed.Scene.Elements[2]
	if ar.StartBinding == nil || ar.StartBinding.ElementID != a.ID || ar.EndBinding == nil || ar.EndBinding.ElementID != b.ID {
		t.Fatalf("bindings: %+v %+v", ar.StartBinding, ar.EndBinding)
	}
	if len(a.BoundElements) != 1 || a.BoundElements[0] != (Bound{ID: ar.ID, Type: "arrow"}) || len(b.BoundElements) != 1 {
		t.Error("the shapes know the arrow")
	}
	s, e := ar.Transform().Apply(pt(ar.Points[0][0], ar.Points[0][1])), ar.Transform().Apply(pt(ar.Points[1][0], ar.Points[1][1]))
	if !nearPt(s, pt(108, 50)) || !nearPt(e, pt(192, 50)) {
		t.Errorf("ends on the outlines, 8 off: %v %v", s, e)
	}
	drag(ed, "", pt(150, 120), pt(150, 120), false) // deselect
	drag(ed, "", pt(50, 50), pt(50, 150), false)    // a moves down 100
	s = ar.Transform().Apply(pt(ar.Points[0][0], ar.Points[0][1]))
	e = ar.Transform().Apply(pt(ar.Points[1][0], ar.Points[1][1]))
	if s.Y <= 50 || !nearPt(e, ed.Scene.Find(b.ID).edgePoint(pt(50, 150), 8)) {
		t.Errorf("follows a: %v %v", s, e)
	}
	ed.Select(ar.ID)
	ed.Nudge(0, 5)
	if ar.StartBinding != nil || ar.EndBinding != nil || len(a.BoundElements) != 0 || len(b.BoundElements) != 0 {
		t.Error("an arrow moved alone keeps its ends")
	}
}

func TestArrowRebindsItsEnd(t *testing.T) {
	ed, a, b := two()
	drag(ed, ToolArrow, pt(50, 50), pt(150, 50), false)
	ar := ed.Scene.Elements[2]
	if ar.StartBinding == nil || ar.EndBinding != nil {
		t.Fatal("bound at the start only")
	}
	end := ar.Transform().Apply(pt(ar.Points[1][0], ar.Points[1][1]))
	drag(ed, "", end, pt(250, 50), false)
	if ar.EndBinding == nil || ar.EndBinding.ElementID != b.ID || ar.StartBinding == nil {
		t.Errorf("dragged onto b: %+v %+v", ar.StartBinding, ar.EndBinding)
	}
	drag(ed, "", ar.Transform().Apply(pt(ar.Points[1][0], ar.Points[1][1])), pt(150, 300), false)
	if ar.EndBinding != nil || len(b.BoundElements) != 0 || ar.StartBinding == nil || len(a.BoundElements) != 1 {
		t.Errorf("dragged off b: %+v", ar.EndBinding)
	}
	start := ar.Transform().Apply(pt(ar.Points[0][0], ar.Points[0][1]))
	drag(ed, "", start, pt(150, 200), false)
	if ar.StartBinding != nil {
		t.Error("the start dragged off a")
	}
	ed.Select(a.ID)
	ed.Delete()
	if ed.Scene.Find(a.ID) != nil {
		t.Error("deleted")
	}
}

func TestDeleteUnbinds(t *testing.T) {
	ed, a, b := two()
	drag(ed, ToolArrow, pt(50, 50), pt(250, 50), false)
	ar := ed.Scene.Elements[2]
	ed.Select(a.ID)
	ed.Delete()
	if ar.StartBinding != nil || ar.EndBinding == nil || len(ed.Sel) != 0 {
		t.Errorf("a's binding left: %+v", ar.StartBinding)
	}
	ed.Select(b.ID)
	ed.Delete()
	if ar.EndBinding != nil {
		t.Error("b's binding left")
	}
	ed.Select(ar.ID)
	drag(ed, ToolRectangle, pt(0, 400), pt(50, 450), false)
	r := ed.Scene.Elements[1]
	r.BoundElements = []Bound{{ID: ar.ID, Type: "arrow"}, {ID: "other", Type: "text"}}
	ed.Select(ar.ID)
	ed.Delete()
	if len(r.BoundElements) != 1 || r.BoundElements[0].ID != "other" {
		t.Errorf("bound elements kept: %v", r.BoundElements)
	}
}

func TestEdgePoint(t *testing.T) {
	r := New("rectangle")
	r.X, r.Y, r.Width, r.Height = 0, 0, 100, 50
	for _, c := range []struct {
		typ  string
		to   Pt
		want Pt
	}{
		{"rectangle", pt(200, 25), pt(110, 25)},
		{"rectangle", pt(50, -100), pt(50, -10)},
		{"rectangle", pt(50, 25), pt(50, -10)}, // at the center: up
		{"ellipse", pt(200, 25), pt(110, 25)},
		{"ellipse", pt(50, 100), pt(50, 60)},
		{"diamond", pt(-100, 25), pt(-10, 25)},
		{"diamond", pt(150, 125), pt(50+100.0/6+10/math.Sqrt2, 25+100.0/6+10/math.Sqrt2)},
	} {
		r.Type = c.typ
		if got := r.edgePoint(c.to, 10); !nearPt(got, c.want) {
			t.Errorf("%s toward %v: %v, want %v", c.typ, c.to, got, c.want)
		}
	}
	r.Type, r.Angle = "rectangle", math.Pi/2 // 50 wide, 100 tall around (50, 25)
	if got := r.edgePoint(pt(200, 25), 0); !nearPt(got, pt(75, 25)) {
		t.Errorf("turned: %v", got)
	}
}

func TestRouteEdges(t *testing.T) {
	ed := NewEditor(NewScene())
	a := New("line")
	a.Points = [][2]float64{{0, 0}}
	ed.route(a) // one point: nothing to route
	ar := New("arrow")
	ar.Points = [][2]float64{{0, 0}, {50, 0}}
	ar.StartBinding = &Binding{ElementID: "gone", Gap: 8}
	ed.route(ar)
	if ar.Points[1] != [2]float64{50, 0} {
		t.Error("routed to a missing shape")
	}
	if bindable(New("line")) || !bindable(New("frame")) || !bindable(New("text")) || !bindable(New("image")) {
		t.Error("bindable")
	}
}

func TestRouteThreePoints(t *testing.T) {
	ed, a, b := two()
	ar := New("arrow")
	ar.X, ar.Y = 50, 50
	ar.Points = [][2]float64{{0, 0}, {100, 100}, {200, 0}}
	ed.Scene.Elements = append(ed.Scene.Elements, ar)
	ed.bindEnds(ar)
	if ar.StartBinding == nil || ar.EndBinding == nil {
		t.Fatal("not bound")
	}
	s := ar.Transform().Apply(pt(ar.Points[0][0], ar.Points[0][1]))
	if !nearPt(s, a.edgePoint(pt(150, 150), 8)) {
		t.Errorf("a bent arrow aims at its next point: %v", s)
	}
	e := ar.Transform().Apply(pt(ar.Points[2][0], ar.Points[2][1]))
	if !nearPt(e, b.edgePoint(pt(150, 150), 8)) {
		t.Errorf("and its end at the one before: %v", e)
	}
}

func TestDuplicateNudgeOrder(t *testing.T) {
	ed, a, b := two()
	drag(ed, ToolArrow, pt(50, 50), pt(250, 50), false)
	ar := ed.Scene.Elements[2]
	ed.Select(a.ID, ar.ID)
	ed.Duplicate()
	if len(ed.Scene.Elements) != 5 || len(ed.Sel) != 2 || ed.Sel[a.ID] {
		t.Fatalf("duplicates: %d, selection %v", len(ed.Scene.Elements), ed.Sel)
	}
	c, d := ed.Scene.Elements[3], ed.Scene.Elements[4]
	if c.ID == a.ID || c.X != 10 || c.Y != 10 || c.Seed == a.Seed {
		t.Errorf("a copy: %+v", c)
	}
	if len(c.BoundElements) != 1 || c.BoundElements[0].ID != d.ID || d.StartBinding == nil || d.StartBinding.ElementID != c.ID || d.EndBinding != nil {
		t.Errorf("copied together, still joined; what wasn't copied lets go: %v %+v %+v", c.BoundElements, d.StartBinding, d.EndBinding)
	}
	ed.Undo()
	if len(ed.Scene.Elements) != 3 {
		t.Error("duplicate is a step")
	}
	a, b, ar = ed.Scene.Elements[0], ed.Scene.Elements[1], ed.Scene.Elements[2] // undo made new ones
	ed.Select(a.ID)
	ed.Nudge(1, -2)
	if a.X != 1 || a.Y != -2 || ed.g.orig != nil {
		t.Error("nudged")
	}
	if s := ar.Transform().Apply(pt(ar.Points[0][0], ar.Points[0][1])); s.X < 108 {
		t.Errorf("the arrow followed the nudge: %v", s)
	}
	ed.Scene.Elements = []*Element{a, b, ar}
	ed.Select(a.ID)
	ed.Order(1)
	if ed.Scene.Elements[1] != a {
		t.Error("forward")
	}
	ed.Order(2)
	if ed.Scene.Elements[2] != a {
		t.Error("to the front")
	}
	ed.Order(1)
	if ed.Scene.Elements[2] != a {
		t.Error("forward from the front")
	}
	ed.Order(-1)
	if ed.Scene.Elements[1] != a {
		t.Error("backward")
	}
	ed.Order(-2)
	if ed.Scene.Elements[0] != a {
		t.Error("to the back")
	}
	ed.Order(-1)
	if ed.Scene.Elements[0] != a {
		t.Error("backward from the back")
	}
	ed.Select(a.ID, b.ID)
	ed.Order(2)
	if ed.Scene.Elements[1] != a || ed.Scene.Elements[2] != b {
		t.Errorf("several keep their order: %v", ids(ed.Scene.Elements))
	}
}

func ids(els []*Element) string {
	var s []string
	for _, e := range els {
		s = append(s, e.ID[:3])
	}
	return strings.Join(s, " ")
}

func TestRestyle(t *testing.T) {
	ed, a, b := two()
	ed.SetTool(ToolText)
	ed.Down(pt(0, 300), 4, false)
	ed.Up()
	ed.SetText("Hi")
	ed.EndText()
	tx := ed.Scene.Elements[2]
	ed.Select(a.ID, tx.ID)
	w, v := tx.Width, a.Version
	ed.Restyle(func(e *Element) { e.StrokeColor, e.FontSize = "#e03131", 40 })
	if a.StrokeColor != "#e03131" || tx.StrokeColor != "#e03131" || b.StrokeColor == "#e03131" || ed.Style.StrokeColor != "#e03131" {
		t.Error("restyled")
	}
	if tx.Width <= w || a.Version != v+1 {
		t.Errorf("text refit %v, version %d", tx.Width, a.Version)
	}
	ed.Undo()
	if ed.Scene.Elements[0].StrokeColor == "#e03131" {
		t.Error("restyle is a step")
	}
}

func TestSelectAllAndTools(t *testing.T) {
	ed, a, b := two()
	b.Locked = true
	ed.SelectAll()
	if !ed.Sel[a.ID] || ed.Sel[b.ID] {
		t.Errorf("all but the locked: %v", ed.Sel)
	}
	ed.SetTool(ToolSelect)
	if len(ed.Sel) != 1 {
		t.Error("select keeps the selection")
	}
	ed.SetTool(ToolHand)
	if len(ed.Sel) != 0 {
		t.Error("another tool drops it")
	}
	ed.Down(pt(50, 50), 4, false) // the hand: nothing
	ed.Drag(pt(60, 60), 4, false)
	ed.Up()
	if a.X != 0 || ed.CanUndo() && len(ed.undo) != 2 {
		t.Error("the hand moved something")
	}
	ed.Select(a.ID)
	if got := ed.Selected(); len(got) != 1 || got[0] != a {
		t.Error("Selected")
	}
	a.IsDeleted = true
	if len(ed.Selected()) != 0 {
		t.Error("a deleted one is selected")
	}
}

func TestEraser(t *testing.T) {
	ed, a, b := two()
	ed.SetTool(ToolEraser)
	g := ed.Gen
	ed.Down(pt(50, 50), 4, false)
	if !ed.Erasing[a.ID] || ed.Gen <= g {
		t.Fatal("the press erases, and shows")
	}
	g = ed.Gen
	ed.Drag(pt(51, 50), 4, false)
	ed.erase(pt(52, 50), 4)
	if ed.Gen != g+1 {
		t.Errorf("an element already erased counts again: %d → %d", g, ed.Gen)
	}
	ed.Drag(pt(250, 50), 4, false) // inside b: transparent, nothing
	ed.Drag(pt(300, 50), 4, false)
	if !ed.Erasing[b.ID] {
		t.Error("b's edge")
	}
	if len(ed.Scene.Elements) != 2 {
		t.Error("erased before the release")
	}
	ed.Up()
	if len(ed.Scene.Elements) != 0 || ed.Erasing != nil {
		t.Error("erased on release")
	}
	ed.Undo()
	if len(ed.Scene.Elements) != 2 {
		t.Error("erasing is a step")
	}
}

func TestUndoLimits(t *testing.T) {
	ed := NewEditor(NewScene())
	ed.Undo()
	ed.Redo()
	for i := range 205 {
		drag(ed, ToolRectangle, pt(float64(i), 0), pt(float64(i)+10, 10), false)
	}
	if len(ed.undo) != 200 {
		t.Errorf("%d steps kept", len(ed.undo))
	}
	for ed.CanUndo() {
		ed.Undo()
	}
	if len(ed.Scene.Elements) != 5 {
		t.Errorf("the oldest steps went first: %d left", len(ed.Scene.Elements))
	}
	ed.Select(ed.Scene.Elements[0].ID)
	ed.Redo()
	ed.Delete()
	if ed.CanRedo() {
		t.Error("a change keeps the redo")
	}
}

func TestRestoreDropsGoneSelection(t *testing.T) {
	ed := NewEditor(NewScene())
	drag(ed, ToolRectangle, pt(0, 0), pt(10, 10), false)
	id := ed.Scene.Elements[0].ID
	ed.Undo()
	if ed.Sel[id] {
		t.Error("the undone shape stays selected")
	}
}

func TestInverse(t *testing.T) {
	m := Translate(3, -4).Mul(Rotate(.7, pt(10, 5))).Mul(Scale(2))
	p := pt(7, 11)
	if q := m.Inverse().Apply(m.Apply(p)); !nearPt(q, p) {
		t.Errorf("round trip %v", q)
	}
	ed := NewEditor(NewScene())
	ed.fitPoints(New("rectangle")) // no points
}

func TestFitText(t *testing.T) {
	e := New("text")
	e.Text, e.LineHeight = "a\nlonger line\n", 0
	e.FitText()
	if e.Height != 3*20*1.25 {
		t.Errorf("height %v", e.Height)
	}
	one := New("text")
	one.Text = "longer line"
	one.FitText()
	if e.Width != one.Width || one.Width <= 0 || one.Height != 25 {
		t.Errorf("width %v, one line %v×%v", e.Width, one.Width, one.Height)
	}
}

func TestArrowOnOneShape(t *testing.T) {
	ed, a, b := two()
	b.IsDeleted = true
	drag(ed, ToolArrow, pt(20, 50), pt(80, 50), false)
	ar := ed.Scene.Elements[2]
	if ar.StartBinding == nil || ar.EndBinding == nil || len(a.BoundElements) != 1 {
		t.Errorf("both ends on a, listed once: %v", a.BoundElements)
	}
	drag(ed, ToolArrow, pt(220, 50), pt(380, 50), false)
	if c := ed.Scene.Elements[3]; c.StartBinding != nil {
		t.Error("bound to a deleted shape")
	}
}

func TestGenBumps(t *testing.T) {
	ed, a, _ := two()
	for _, c := range []struct {
		name string
		f    func()
	}{
		{"select", func() { ed.Select(a.ID) }},
		{"restyle", func() { ed.Restyle(func(e *Element) { e.Opacity = 50 }) }},
		{"undo", ed.Undo},
		{"redo", ed.Redo},
		{"restyle nothing", func() { ed.Select(); ed.Restyle(func(e *Element) { e.Opacity = 40 }) }},
		{"text", func() { ed.Editing = a.ID; ed.SetText("x") }},
		{"end", ed.EndText},
	} {
		g := ed.Gen
		c.f()
		if ed.Gen <= g {
			t.Errorf("%s: Gen %d → %d", c.name, g, ed.Gen)
		}
	}
}

func TestPressOutsideTheSelection(t *testing.T) {
	ed, a, b := two()
	for _, p := range []Pt{pt(-20, 50), pt(150, -20), pt(320, 50), pt(150, 120)} {
		ed.Select(a.ID, b.ID)
		ed.Down(p, 4, false)
		if ed.Box == nil {
			t.Errorf("a press at %v moves the selection", p)
		}
		ed.Up()
	}
	ed.Select(a.ID)
	if ed.inSelection(pt(50, 50)) {
		t.Error("one element has a selection box")
	}
}

func TestResizeSeveralAway(t *testing.T) {
	ed := NewEditor(NewScene())
	ed.Style.BackgroundColor = "#fff"
	drag(ed, ToolRectangle, pt(100, 200), pt(150, 250), false)
	drag(ed, ToolRectangle, pt(250, 300), pt(300, 400), false)
	a, b := ed.Scene.Elements[0], ed.Scene.Elements[1]
	ed.Select(a.ID, b.ID) // union 100,200 – 300,400
	drag(ed, "", pt(95, 195), pt(-105, 195), false)
	if box(a) != [4]float64{-100, 200, 100, 50} || box(b) != [4]float64{200, 300, 100, 100} {
		t.Errorf("nw out by 200: %v %v", box(a), box(b))
	}
	drag(ed, "", pt(305, 405), pt(305, 305), false) // se: half as tall
	if box(a) != [4]float64{-100, 200, 100, 25} || box(b) != [4]float64{200, 250, 100, 50} {
		t.Errorf("se up by 100: %v %v", box(a), box(b))
	}
}

func TestResizeLineAwayFromOrigin(t *testing.T) {
	ed := NewEditor(NewScene())
	drag(ed, ToolLine, pt(100, 100), pt(50, 40), false) // points 0,0 → -50,-60
	l := ed.Scene.Elements[0]
	drag(ed, ToolSelect, pt(0, 0), pt(0, 0), false)
	l.Points = append(l.Points, [2]float64{-20, -10})
	ed.Select(l.ID)
	ed.g = gesture{kind: gResize, handle: "se", start: pt(100, 100)}
	ed.hold()
	ed.Drag(pt(150, 160), 4, false) // the box 50×60 to 100×120
	ed.Up()
	if l.X != 150 || l.Y != 160 || l.Points[1] != [2]float64{-100, -120} || l.Points[2] != [2]float64{-40, -20} { // its far corner stays at 50, 40
		t.Errorf("scaled from its far corner: %v %v", box(l), l.Points)
	}
}

func TestGripAway(t *testing.T) {
	x0, y0, x1, y1 := grip("se", 10, 20, 30, 60, pt(20, 0), true) // 20×40 to 40 wide: ×2
	if x0 != 10 || y0 != 20 || x1 != 50 || y1 != 100 {
		t.Errorf("se: %v %v %v %v", x0, y0, x1, y1)
	}
	x0, y0, x1, y1 = grip("nw", 10, 20, 30, 60, pt(40, 0), true) // flipped past the right: 20 wide the other way
	if x0 != 30 || y0 != 20 || x1 != 50 || y1 != 60 {
		t.Errorf("nw flipped: %v %v %v %v", x0, y0, x1, y1)
	}
	if scaleOf(10, 20, 0, 30) != 3 {
		t.Error("scaleOf away from 0")
	}
}

func TestOrderSeveral(t *testing.T) {
	ed := NewEditor(NewScene())
	for i := range 4 {
		drag(ed, ToolRectangle, pt(float64(i*20), 0), pt(float64(i*20+10), 10), false)
	}
	els := append([]*Element{}, ed.Scene.Elements...)
	a, b, c, d := els[0], els[1], els[2], els[3]
	ed.Select(a.ID, c.ID)
	ed.Order(1)
	if got := ed.Scene.Elements; got[0] != b || got[1] != a || got[2] != d || got[3] != c {
		t.Errorf("forward: %v", ids(got))
	}
	ed.Order(-1)
	if got := ed.Scene.Elements; got[0] != a || got[1] != b || got[2] != c || got[3] != d {
		t.Errorf("backward: %v", ids(got))
	}
	ed.Select(a.ID, b.ID)
	ed.Order(1)
	if got := ed.Scene.Elements; got[0] != c || got[1] != a || got[2] != b {
		t.Errorf("two together forward: %v", ids(got))
	}
}

func TestNear(t *testing.T) {
	r := New("rectangle")
	r.Width, r.Height = 100, 50
	if !r.near(pt(105, 55), 10) || r.near(pt(115, 25), 10) || r.near(pt(50, 65), 10) || !r.near(pt(-5, -5), 10) {
		t.Error("near")
	}
}

func TestMoveKeepsBindingsHeld(t *testing.T) {
	ed, a, b := two()
	drag(ed, ToolArrow, pt(50, 50), pt(250, 50), false)
	ar := ed.Scene.Elements[2]
	ed.Select(a.ID, b.ID, ar.ID)
	ed.Nudge(0, 10)
	if ar.StartBinding == nil || ar.EndBinding == nil {
		t.Error("moved with both its shapes, it let go")
	}
	ed.Select(a.ID, ar.ID)
	ed.Nudge(0, 10)
	if ar.StartBinding != nil || ar.EndBinding != nil {
		t.Error("moved without b, still bound")
	}
	drag(ed, ToolArrow, pt(50, 70), pt(250, 70), false)
	ar = ed.Scene.Elements[3]
	ed.Select(b.ID, ar.ID)
	ed.Nudge(0, 10)
	if ar.EndBinding != nil {
		t.Error("moved without a, still bound")
	}
	l := New("line")
	l.X, l.Y, l.Points = 10, 300, [][2]float64{{0, 0}, {40, 0}}
	l.StartBinding = &Binding{ElementID: a.ID, Gap: 8}
	gone := New("arrow")
	gone.X, gone.Y, gone.Points, gone.IsDeleted = 10, 300, [][2]float64{{0, 0}, {40, 0}}, true
	gone.StartBinding = &Binding{ElementID: a.ID, Gap: 8}
	ed.Scene.Elements = append(ed.Scene.Elements, l, gone)
	ed.Select(a.ID)
	ed.Nudge(5, 0)
	if l.Points[0] != [2]float64{0, 0} || l.X != 10 || gone.X != 10 {
		t.Error("a line or a deleted arrow followed")
	}
}

func TestMoreGenBumps(t *testing.T) {
	ed, a, b := two()
	g := ed.Gen
	ed.SetTool(ToolSelect)
	if ed.Gen <= g {
		t.Error("SetTool")
	}
	ed.Select(a.ID)
	g = ed.Gen
	ed.Down(pt(201, 50), 4, true)
	if ed.Gen <= g || !ed.Sel[b.ID] {
		t.Error("a shift click shows at once")
	}
	ed.Up()
	ed.Down(pt(500, 500), 4, false)
	ed.Drag(pt(600, 600), 4, false)
	g = ed.Gen
	ed.Up()
	if ed.Gen <= g || ed.Box != nil {
		t.Error("the box's end shows")
	}
}

func TestShiftPressBetweenSelected(t *testing.T) {
	ed, a, b := two()
	ed.Select(a.ID, b.ID)
	ed.Down(pt(150, 50), 4, true) // nothing under it, inside the selection: a move, not a toggle
	ed.Drag(pt(160, 50), 4, true)
	ed.Up()
	if a.X != 10 || b.X != 210 || len(ed.Sel) != 2 {
		t.Errorf("shift drag between them: %v %v %v", a.X, b.X, ed.Sel)
	}
}

func TestLinePointsDontBind(t *testing.T) {
	ed, _, _ := two()
	drag(ed, ToolLine, pt(150, 200), pt(150, 300), false)
	l := ed.Scene.Elements[2]
	drag(ed, "", pt(150, 300), pt(50, 50), false)
	if l.EndBinding != nil {
		t.Error("a line's point bound to a shape")
	}
}

func TestSegDistAway(t *testing.T) {
	if d := segDist(pt(15, 24), pt(10, 20), pt(20, 20)); d != 4 {
		t.Errorf("above the middle: %v", d)
	}
	if d := segDist(pt(13, 20), pt(10, 20), pt(10, 30)); d != 3 {
		t.Errorf("beside a vertical one: %v", d)
	}
}

func TestGripFlipsDown(t *testing.T) {
	x0, y0, x1, y1 := grip("se", 10, 20, 30, 60, pt(0, -60), true)
	if x0 != 10 || y0 != -20 || x1 != 30 || y1 != 20 {
		t.Errorf("se above the top: %v %v %v %v", x0, y0, x1, y1)
	}
}

func TestPlaceAway(t *testing.T) {
	ed := NewEditor(NewScene())
	o := New("line")
	o.Points = [][2]float64{{-50, -60}, {0, 0}}
	e := New("line")
	e.Points = [][2]float64{{0, 0}, {0, 0}}
	ed.place(e, o, -50, -60, 0, 0, -40, -30, 60, 90) // 50×60 to 100×120 at -40, -30
	if e.Points[0] != [2]float64{-40, -30} || e.Points[1] != [2]float64{60, 90} {
		t.Errorf("placed: %v", e.Points)
	}
}

func TestResizeSeveralLineAway(t *testing.T) {
	ed := NewEditor(NewScene())
	ed.Style.BackgroundColor = "#fff"
	drag(ed, ToolRectangle, pt(0, 0), pt(100, 100), false)
	drag(ed, ToolLine, pt(200, 100), pt(100, 0), false) // points 0,0 → -100,-100: its box starts at -100
	a, l := ed.Scene.Elements[0], ed.Scene.Elements[1]
	ed.Select(a.ID, l.ID)                           // union 0,0 – 200,100
	drag(ed, "", pt(205, 105), pt(405, 105), false) // se: twice as wide
	if l.X != 400 || l.Y != 100 || l.Points[1] != [2]float64{-200, -100} || a.Width != 200 {
		t.Errorf("the line: %v %v; a %v", box(l), l.Points, box(a))
	}
}
