package draw

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
)

// Tools are the editor's, by the names Excalidraw's toolbar uses.
const (
	ToolSelect    = "selection"
	ToolHand      = "hand"
	ToolRectangle = "rectangle"
	ToolDiamond   = "diamond"
	ToolEllipse   = "ellipse"
	ToolArrow     = "arrow"
	ToolLine      = "line"
	ToolFreedraw  = "freedraw"
	ToolText      = "text"
	ToolEraser    = "eraser"
)

// Editor is a drawing being edited: the scene, the tool, the selection,
// undo, and the gesture under way. It has no GUI: the window feeds it
// pointer positions in scene units and draws what it holds.
type Editor struct {
	Scene   *Scene
	Tool    string
	Style   *Element        // what new elements start as; the style panel changes it with the selection
	Sel     map[string]bool // the selected elements' ids
	Gen     int             // bumps on every change (a redraw, a save)
	Box     *[4]float64     // the selection rectangle being dragged: x0, y0, x1, y1
	Editing string          // the text element being typed into, "" none
	Erasing map[string]bool // what the eraser went over, removed when it lifts

	undo, redo [][]byte // the elements as JSON, before each change
	textBefore []byte   // the elements before the text being typed: its undo step
	g          gesture
}

// gesture is a press until its release.
type gesture struct {
	kind   int
	start  Pt
	handle string              // the handle pressed (see Handles)
	orig   map[string]*Element // the selection as it was when the press came, copies
	box    [4]float64          // their union box then
	el     *Element            // the element being made
	before []byte              // the elements before: the undo step, when the gesture changes them
}

const (
	gNone = iota
	gCreate
	gMove
	gResize
	gRotate
	gPoint
	gBox
	gErase
)

// NewEditor edits s with the select tool and Excalidraw's default style.
func NewEditor(s *Scene) *Editor {
	return &Editor{Scene: s, Tool: ToolSelect, Style: New("rectangle"), Sel: map[string]bool{}}
}

func (ed *Editor) snapshot() []byte {
	b, _ := json.Marshal(ed.Scene.Elements)
	return b
}

func (ed *Editor) restore(b []byte) {
	var els []*Element
	_ = json.Unmarshal(b, &els)
	ed.Scene.Elements = els
	for id := range ed.Sel {
		if ed.Scene.Find(id) == nil {
			delete(ed.Sel, id)
		}
	}
	ed.Gen++
}

// commit makes the change since before one undo step, if it changed anything.
func (ed *Editor) commit(before []byte) {
	if string(before) == string(ed.snapshot()) {
		return
	}
	ed.undo = append(ed.undo, before)
	if len(ed.undo) > 200 { // ponytail: whole-scene snapshots, capped; diffs if drawings get large
		ed.undo = ed.undo[1:]
	}
	ed.redo = nil
	ed.Gen++
}

// change runs f on the elements as one undo step.
func (ed *Editor) change(f func()) {
	before := ed.snapshot()
	f()
	ed.commit(before)
}

// Undo takes back the last change; Redo does it again.
func (ed *Editor) Undo() { ed.step(&ed.undo, &ed.redo) }
func (ed *Editor) Redo() { ed.step(&ed.redo, &ed.undo) }

func (ed *Editor) step(from, to *[][]byte) {
	if len(*from) == 0 {
		return
	}
	*to = append(*to, ed.snapshot())
	ed.restore((*from)[len(*from)-1])
	*from = (*from)[:len(*from)-1]
}

// CanUndo and CanRedo say whether there is a step to take.
func (ed *Editor) CanUndo() bool { return len(ed.undo) > 0 }
func (ed *Editor) CanRedo() bool { return len(ed.redo) > 0 }

// Selected is the selection in drawing order.
func (ed *Editor) Selected() []*Element {
	var out []*Element
	for _, e := range ed.Scene.Elements {
		if ed.Sel[e.ID] && !e.IsDeleted {
			out = append(out, e)
		}
	}
	return out
}

// Select makes ids the selection.
func (ed *Editor) Select(ids ...string) {
	ed.Sel = map[string]bool{}
	for _, id := range ids {
		ed.Sel[id] = true
	}
	ed.Gen++
}

// SelectAll selects every live element.
func (ed *Editor) SelectAll() {
	var ids []string
	for _, e := range ed.Scene.Elements {
		if !e.IsDeleted && !e.Locked {
			ids = append(ids, e.ID)
		}
	}
	ed.Select(ids...)
}

// SetTool picks a tool, dropping the selection for any but select.
func (ed *Editor) SetTool(t string) {
	ed.Tool = t
	if t != ToolSelect {
		ed.Select()
	}
	ed.Gen++
}

// Down is a press at p (scene units); tol is how near counts as on
// something, in scene units (a few pixels at the zoom shown); add is Shift
// (adds to the selection).
func (ed *Editor) Down(p Pt, tol float64, add bool) {
	ed.g = gesture{start: p, before: ed.snapshot()}
	switch ed.Tool {
	case ToolHand:
	case ToolEraser:
		ed.g.kind, ed.Erasing = gErase, map[string]bool{}
		ed.erase(p, tol)
	case ToolText:
		if hit := ed.HitTest(p, tol); hit != nil && hit.Type == "text" {
			ed.EditText(hit.ID)
			ed.Tool = ToolSelect
			return
		}
		ed.textBefore = ed.g.before
		e := ed.styled(ToolText)
		e.X, e.Y = p.X, p.Y-e.FontSize*e.LineHeight/2
		e.FitText()
		ed.Scene.Elements = append(ed.Scene.Elements, e)
		ed.Editing, ed.Tool = e.ID, ToolSelect
		ed.Select(e.ID)
	case ToolSelect:
		if h := ed.handleAt(p, tol); h != "" {
			ed.g.handle = h
			ed.g.kind = map[bool]int{true: gRotate, false: gResize}[h == "rotation"]
			if h[0] == 'p' {
				ed.g.kind = gPoint
			}
			ed.hold()
			return
		}
		hit := ed.HitTest(p, tol)
		switch {
		case hit == nil && !ed.inSelection(p):
			if !add {
				ed.Select()
			}
			ed.g.kind, ed.Box = gBox, &[4]float64{p.X, p.Y, p.X, p.Y}
			return
		case hit != nil && add:
			if ed.Sel[hit.ID] {
				delete(ed.Sel, hit.ID)
			} else {
				ed.Sel[hit.ID] = true
			}
			ed.Gen++
		case hit != nil && !ed.Sel[hit.ID]:
			ed.Select(hit.ID)
		}
		ed.g.kind = gMove
		ed.hold()
	default: // a shape tool
		e := ed.styled(ed.Tool)
		e.X, e.Y = p.X, p.Y
		if e.Type == ToolArrow || e.Type == ToolLine {
			e.Points = [][2]float64{{0, 0}, {0, 0}}
		}
		ed.Scene.Elements = append(ed.Scene.Elements, e)
		ed.g.kind, ed.g.el = gCreate, e
		ed.Select()
	}
}

// hold keeps a copy of the selection as it is, for a gesture to work from.
func (ed *Editor) hold() {
	ed.g.orig = map[string]*Element{}
	for _, e := range ed.Selected() {
		c := *e
		c.Points = slices.Clone(e.Points)
		ed.g.orig[e.ID] = &c
	}
	ed.g.box = boxOf(ed.Selected())
}

// styled is a new element of typ in the current style.
func (ed *Editor) styled(typ string) *Element {
	e, s := New(typ), ed.Style
	e.StrokeColor, e.BackgroundColor, e.FillStyle, e.StrokeWidth = s.StrokeColor, s.BackgroundColor, s.FillStyle, s.StrokeWidth
	e.StrokeStyle, e.Roughness, e.Opacity = s.StrokeStyle, s.Roughness, s.Opacity
	if typ == ToolText {
		e.FontFamily, e.FontSize, e.TextAlign = s.FontFamily, s.FontSize, s.TextAlign
		if e.FontSize == 0 {
			e.FontSize, e.FontFamily, e.TextAlign = 20, FontExcalifont, "left"
		}
	}
	if s.Roundness == nil && (typ == ToolRectangle || typ == ToolDiamond || typ == ToolLine || typ == ToolArrow) {
		e.Roundness = nil
	}
	if typ == ToolArrow {
		e.StartArrowhead, e.EndArrowhead = s.StartArrowhead, s.EndArrowhead
		if s.EndArrowhead == nil && s.StartArrowhead == nil {
			a := "arrow"
			e.EndArrowhead = &a
		}
	}
	return e
}

// Drag moves the press to p; shift keeps proportions, snaps angles.
func (ed *Editor) Drag(p Pt, tol float64, shift bool) {
	g := &ed.g
	d := p.sub(g.start)
	switch g.kind {
	case gErase:
		ed.erase(p, tol)
	case gBox:
		ed.Box[2], ed.Box[3] = p.X, p.Y
		ed.Select()
		for _, e := range ed.Scene.Elements {
			if x0, y0, x1, y1 := e.Bounds(); !e.IsDeleted && !e.Locked && x0 >= math.Min(ed.Box[0], p.X) && y0 >= math.Min(ed.Box[1], p.Y) && x1 <= math.Max(ed.Box[0], p.X) && y1 <= math.Max(ed.Box[1], p.Y) {
				ed.Sel[e.ID] = true
			}
		}
	case gCreate:
		e := g.el
		switch e.Type {
		case ToolFreedraw:
			q := p.sub(pt(e.X, e.Y))
			e.Points = append(e.Points, [2]float64{q.X, q.Y})
			ed.fitPoints(e)
		case ToolArrow, ToolLine:
			if shift {
				d = snapAngle(d)
			}
			e.Points[1] = [2]float64{d.X, d.Y}
			ed.fitPoints(e)
		default:
			if shift {
				s := math.Max(math.Abs(d.X), math.Abs(d.Y))
				d = pt(math.Copysign(s, d.X), math.Copysign(s, d.Y))
			}
			e.X, e.Y = math.Min(g.start.X, g.start.X+d.X), math.Min(g.start.Y, g.start.Y+d.Y)
			e.Width, e.Height = math.Abs(d.X), math.Abs(d.Y)
		}
	case gMove:
		for id, o := range g.orig {
			if e := ed.Scene.Find(id); e != nil {
				e.X, e.Y = o.X+d.X, o.Y+d.Y
			}
		}
		ed.moved()
	case gRotate:
		if len(g.orig) == 1 {
			for id, o := range g.orig {
				e := ed.Scene.Find(id)
				x0, y0, x1, y1 := o.Bounds()
				c := pt((x0+x1)/2, (y0+y1)/2)
				a := math.Atan2(p.Y-c.Y, p.X-c.X) + math.Pi/2
				if shift {
					a = math.Round(a/(math.Pi/12)) * math.Pi / 12
				}
				e.Angle = math.Mod(a+2*math.Pi, 2*math.Pi)
			}
			ed.follow()
		}
	case gResize:
		ed.resize(p, shift)
		ed.follow()
	case gPoint:
		for id, o := range g.orig {
			e := ed.Scene.Find(id)
			i, err := strconv.Atoi(g.handle[1:])
			if err != nil || i >= len(e.Points) {
				return
			}
			q := o.Transform().Inverse().Apply(p)
			if shift && len(o.Points) > 1 { // snapped around its neighbour
				n := o.Points[map[bool]int{true: 1, false: i - 1}[i == 0]]
				q = pt(n[0], n[1]).add(snapAngle(q.sub(pt(n[0], n[1]))))
			}
			e.Points[i] = [2]float64{q.X, q.Y} // its binding is settled on release (bindEnds)
			ed.fitPoints(e)
		}
	}
	ed.Gen++
}

func (g *gesture) held(id string) bool { _, ok := g.orig[id]; return ok }

// snapAngle turns d to the nearest 15°.
func snapAngle(d Pt) Pt {
	a := math.Round(math.Atan2(d.Y, d.X)/(math.Pi/12)) * math.Pi / 12
	l := d.len()
	return pt(l*math.Cos(a), l*math.Sin(a))
}

// Up ends the press.
func (ed *Editor) Up() {
	g := ed.g
	ed.g = gesture{}
	switch g.kind {
	case gErase:
		for id := range ed.Erasing {
			ed.remove(id)
		}
		ed.Erasing = nil
	case gBox:
		ed.Box = nil
	case gCreate:
		e := g.el
		x0, y0, x1, y1 := e.Bounds()
		if x1-x0 < 2 && y1-y0 < 2 && e.Type != ToolFreedraw { // a click, not a drag: nothing made
			ed.Scene.Elements = slices.DeleteFunc(ed.Scene.Elements, func(x *Element) bool { return x == e })
			break
		}
		if e.Type == ToolArrow {
			ed.bindEnds(e)
		}
		if e.Type != ToolFreedraw {
			ed.Tool = ToolSelect
			ed.Select(e.ID)
		}
	case gPoint:
		for id := range g.orig {
			if e := ed.Scene.Find(id); e != nil && e.Type == ToolArrow {
				ed.bindEnds(e)
			}
		}
	}
	if ed.Editing == "" { // a text just begun commits when its typing ends
		ed.commit(g.before)
	}
	ed.Gen++
}

// Busy reports a press under way.
func (ed *Editor) Busy() bool { return ed.g.kind != gNone }

// erase marks what is under p for the eraser.
func (ed *Editor) erase(p Pt, tol float64) {
	if e := ed.HitTest(p, tol); e != nil && !ed.Erasing[e.ID] {
		ed.Erasing[e.ID] = true
		ed.Gen++ // it shows faded at once
	}
}

// HitTest is the topmost element at p (scene units), within tol.
func (ed *Editor) HitTest(p Pt, tol float64) *Element {
	for i := len(ed.Scene.Elements) - 1; i >= 0; i-- {
		if e := ed.Scene.Elements[i]; !e.IsDeleted && !e.Locked && e.hit(p, tol) {
			return e
		}
	}
	return nil
}

// hit reports whether p is on e: its stroke, or inside it when it is
// filled (text, images and frames' names are always solid).
func (e *Element) hit(p Pt, tol float64) bool {
	q := e.Transform().Inverse().Apply(p)
	tol += e.StrokeWidth / 2
	w, h := e.Width, e.Height
	filled := e.BackgroundColor != "" && e.BackgroundColor != "transparent"
	switch e.Type {
	case "line", "arrow", "freedraw":
		ps := e.pts()
		if len(ps) == 1 {
			return q.sub(ps[0]).len() <= tol
		}
		for i := 1; i < len(ps); i++ {
			if segDist(q, ps[i-1], ps[i]) <= tol {
				return true
			}
		}
		return false
	case "ellipse":
		rx, ry := math.Max(w/2, 1e-6), math.Max(h/2, 1e-6)
		r := math.Hypot((q.X-rx)/rx, (q.Y-ry)/ry)
		if filled {
			return r <= 1+tol/math.Min(rx, ry)
		}
		return math.Abs(r-1)*math.Min(rx, ry) <= tol
	case "diamond":
		rx, ry := math.Max(w/2, 1e-6), math.Max(h/2, 1e-6)
		r := math.Abs(q.X-rx)/rx + math.Abs(q.Y-ry)/ry
		if filled {
			return r <= 1+tol/math.Min(rx, ry)
		}
		return math.Abs(r-1)*math.Min(rx, ry) <= tol
	}
	in := q.X >= -tol && q.Y >= -tol && q.X <= w+tol && q.Y <= h+tol
	if filled || e.Type == "text" || e.Type == "image" {
		return in
	}
	return in && !(q.X > tol && q.Y > tol && q.X < w-tol && q.Y < h-tol)
}

// segDist is p's distance to the segment a–b.
func segDist(p, a, b Pt) float64 {
	ab := b.sub(a)
	l := ab.X*ab.X + ab.Y*ab.Y
	if l == 0 {
		return p.sub(a).len()
	}
	t := math.Max(0, math.Min(1, ((p.X-a.X)*ab.X+(p.Y-a.Y)*ab.Y)/l))
	return p.sub(a.add(ab.mul(t))).len()
}

// inSelection reports p inside the selection's box (a press there moves it).
func (ed *Editor) inSelection(p Pt) bool {
	sel := ed.Selected()
	if len(sel) < 2 {
		return false
	}
	b := boxOf(sel)
	return p.X >= b[0] && p.Y >= b[1] && p.X <= b[2] && p.Y <= b[3]
}

// boxOf is the union of els' bounds.
func boxOf(els []*Element) [4]float64 {
	b := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, e := range els {
		x0, y0, x1, y1 := e.Bounds()
		b = [4]float64{math.Min(b[0], x0), math.Min(b[1], y0), math.Max(b[2], x1), math.Max(b[3], y1)}
	}
	return b
}

// Handle is a grip on the selection: "nw", "n", … "w" resize, "rotation"
// turns, "p<i>" moves a line's point i.
type Handle struct {
	Name string
	P    Pt // scene units
}

// Handles are the selection's grips, pad scene units off its box.
// One element: its own (rotated) box and the rotation grip, or a line's
// points; several: their union box, no rotation.
func (ed *Editor) Handles(pad float64) []Handle {
	sel := ed.Selected()
	if len(sel) == 0 || ed.Editing != "" {
		return nil
	}
	if len(sel) == 1 {
		e := sel[0]
		if e.Type == "line" || e.Type == "arrow" {
			t := e.Transform()
			var hs []Handle
			for i, p := range e.pts() {
				hs = append(hs, Handle{"p" + strconv.Itoa(i), t.Apply(p)})
			}
			return hs
		}
		x0, y0, x1, y1 := e.local()
		hs := boxHandles(x0-pad, y0-pad, x1+pad, y1+pad, e.Transform())
		return append(hs, Handle{"rotation", e.Transform().Apply(pt((x0+x1)/2, y0-pad*4))})
	}
	b := boxOf(sel)
	return boxHandles(b[0]-pad, b[1]-pad, b[2]+pad, b[3]+pad, Identity())
}

func boxHandles(x0, y0, x1, y1 float64, t Aff) []Handle {
	mx, my := (x0+x1)/2, (y0+y1)/2
	ps := []Handle{{"nw", pt(x0, y0)}, {"n", pt(mx, y0)}, {"ne", pt(x1, y0)}, {"e", pt(x1, my)},
		{"se", pt(x1, y1)}, {"s", pt(mx, y1)}, {"sw", pt(x0, y1)}, {"w", pt(x0, my)}}
	for i := range ps {
		ps[i].P = t.Apply(ps[i].P)
	}
	return ps
}

// handleAt is the selection's grip at p, "" none.
func (ed *Editor) handleAt(p Pt, tol float64) string {
	for _, h := range ed.Handles(tol) {
		if p.sub(h.P).len() <= tol {
			return h.Name
		}
	}
	return ""
}

// resize moves the pressed grip to p: one element in its own (rotated)
// frame, several scaled around the far side of their box.
func (ed *Editor) resize(p Pt, keep bool) {
	g := &ed.g
	h := g.handle
	if len(g.orig) == 1 {
		for id, o := range g.orig {
			e := ed.Scene.Find(id)
			lx0, ly0, lx1, ly1 := o.local()
			inv := o.Transform().Inverse()
			x0, y0, x1, y1 := grip(h, lx0, ly0, lx1, ly1, inv.Apply(p).sub(inv.Apply(g.start)), keep)
			ed.place(e, o, lx0, ly0, lx1, ly1, x0, y0, x1, y1)
			c := o.Transform().Apply(pt((x0+x1)/2, (y0+y1)/2)) // the new center where the old frame puts it
			nx0, ny0, nx1, ny1 := e.local()
			e.X, e.Y = c.X-(nx0+nx1)/2, c.Y-(ny0+ny1)/2
			if e.Type == "line" || e.Type == "arrow" || e.Type == "freedraw" {
				ed.fitPoints(e)
			}
		}
		return
	}
	b := g.box
	x0, y0, x1, y1 := grip(h, b[0], b[1], b[2], b[3], p.sub(g.start), keep)
	for id, o := range g.orig {
		e := ed.Scene.Find(id)
		ox0, oy0, ox1, oy1 := o.Bounds()
		sx, sy := scaleOf(b[0], b[2], x0, x1), scaleOf(b[1], b[3], y0, y1)
		nx0, ny0 := x0+(ox0-b[0])*sx, y0+(oy0-b[1])*sy
		nx1, ny1 := x0+(ox1-b[0])*sx, y0+(oy1-b[1])*sy
		lx0, ly0, lx1, ly1 := o.local()
		ed.place(e, o, lx0, ly0, lx1, ly1, lx0, ly0, lx0+(lx1-lx0)*sx, ly0+(ly1-ly0)*sy)
		if e.Type == "line" || e.Type == "arrow" || e.Type == "freedraw" {
			ed.fitPoints(e)
		}
		ex0, ey0, ex1, ey1 := e.Bounds()
		e.X += (nx0+nx1)/2 - (ex0+ex1)/2
		e.Y += (ny0+ny1)/2 - (ey0+ey1)/2
	}
}

// grip is the box x0..x1, y0..y1 with the edges handle h holds moved by
// d, flipped back to order; keep holds its proportions from a corner.
func grip(h string, x0, y0, x1, y1 float64, d Pt, keep bool) (float64, float64, float64, float64) {
	w, ht := x1-x0, y1-y0
	switch h {
	case "nw", "w", "sw":
		x0 += d.X
	case "ne", "e", "se":
		x1 += d.X
	}
	switch h {
	case "nw", "n", "ne":
		y0 += d.Y
	case "sw", "s", "se":
		y1 += d.Y
	}
	if keep && len(h) == 2 && w > 0 && ht > 0 {
		s := math.Max(math.Abs(x1-x0)/w, math.Abs(y1-y0)/ht)
		nw, nh := w*s*sign(x1-x0), ht*s*sign(y1-y0)
		if h[1] == 'w' {
			x0 = x1 - nw
		} else {
			x1 = x0 + nw
		}
		if h[0] == 'n' {
			y0 = y1 - nh
		} else {
			y1 = y0 + nh
		}
	}
	return math.Min(x0, x1), math.Min(y0, y1), math.Max(x0, x1), math.Max(y0, y1)
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

// scaleOf is how much a span a0..a1 grew to b0..b1 (1 when it had none).
func scaleOf(a0, a1, b0, b1 float64) float64 {
	if a1-a0 == 0 {
		return 1
	}
	return (b1 - b0) / (a1 - a0)
}

// place gives e (o as it was) the local box x0..x1, y0..y1 in place of
// lx0..lx1, ly0..ly1: a line's points scaled, text's size by its height.
func (ed *Editor) place(e, o *Element, lx0, ly0, lx1, ly1, x0, y0, x1, y1 float64) {
	sx, sy := scaleOf(lx0, lx1, x0, x1), scaleOf(ly0, ly1, y0, y1)
	switch e.Type {
	case "line", "arrow", "freedraw":
		for i, p := range o.Points {
			e.Points[i] = [2]float64{x0 + (p[0]-lx0)*sx, y0 + (p[1]-ly0)*sy}
		}
	case "text":
		e.FontSize = math.Max(1, o.FontSize*sy)
		e.FitText()
	default:
		e.Width, e.Height = x1-x0, y1-y0
	}
}

// fitPoints keeps a line's first point at 0, 0 (moving X, Y instead,
// which keeps it in place, rotated or not) and its size the points'.
func (ed *Editor) fitPoints(e *Element) {
	if len(e.Points) == 0 {
		return
	}
	o := e.Points[0]
	for i := range e.Points {
		e.Points[i] = [2]float64{e.Points[i][0] - o[0], e.Points[i][1] - o[1]}
	}
	e.X, e.Y = e.X+o[0], e.Y+o[1]
	x0, y0, x1, y1 := e.local()
	e.Width, e.Height = x1-x0, y1-y0
}

// Inverse is m undone (m must be invertible: elements' maps are).
func (m Aff) Inverse() Aff {
	d := m.A*m.D - m.B*m.C
	return Aff{A: m.D / d, B: -m.B / d, C: -m.C / d, D: m.A / d,
		E: (m.C*m.F - m.D*m.E) / d, F: (m.B*m.E - m.A*m.F) / d}
}

// remove deletes element id and every binding to it.
func (ed *Editor) remove(id string) {
	ed.Scene.Elements = slices.DeleteFunc(ed.Scene.Elements, func(e *Element) bool { return e.ID == id })
	for _, e := range ed.Scene.Elements {
		if e.StartBinding != nil && e.StartBinding.ElementID == id {
			e.StartBinding = nil
		}
		if e.EndBinding != nil && e.EndBinding.ElementID == id {
			e.EndBinding = nil
		}
		e.BoundElements = slices.DeleteFunc(e.BoundElements, func(b Bound) bool { return b.ID == id })
	}
	delete(ed.Sel, id)
}

// Delete removes the selection.
func (ed *Editor) Delete() {
	ed.change(func() {
		for _, e := range ed.Selected() {
			ed.remove(e.ID)
		}
	})
}

// Duplicate copies the selection 10 units down and right, selecting the copies.
func (ed *Editor) Duplicate() {
	ed.change(func() {
		var ids []string
		for _, e := range ed.Selected() {
			b, _ := json.Marshal(e)
			c := new(Element)
			_ = json.Unmarshal(b, c)
			c.ID, c.Seed, c.X, c.Y = NewID(), randInt(), e.X+10, e.Y+10
			c.StartBinding, c.EndBinding, c.BoundElements = nil, nil, nil
			ed.Scene.Elements = append(ed.Scene.Elements, c)
			ids = append(ids, c.ID)
		}
		ed.Select(ids...)
	})
}

// Nudge moves the selection by dx, dy.
func (ed *Editor) Nudge(dx, dy float64) {
	ed.change(func() {
		ed.g.orig = map[string]*Element{}
		for _, e := range ed.Selected() {
			e.X, e.Y = e.X+dx, e.Y+dy
			ed.g.orig[e.ID] = e
		}
		ed.moved()
		ed.g.orig = nil
	})
}

// Order moves the selection in the drawing order: 1 forward, 2 to the
// front, -1 backward, -2 to the back.
func (ed *Editor) Order(dir int) {
	ed.change(func() {
		els := ed.Scene.Elements
		in := func(e *Element) bool { return ed.Sel[e.ID] }
		switch dir {
		case 2, -2:
			var sel, rest []*Element
			for _, e := range els {
				if in(e) {
					sel = append(sel, e)
				} else {
					rest = append(rest, e)
				}
			}
			if dir == 2 {
				ed.Scene.Elements = append(rest, sel...)
			} else {
				ed.Scene.Elements = append(sel, rest...)
			}
		case 1:
			for i := len(els) - 2; i >= 0; i-- {
				if in(els[i]) && !in(els[i+1]) {
					els[i], els[i+1] = els[i+1], els[i]
				}
			}
		case -1:
			for i := 1; i < len(els); i++ {
				if in(els[i]) && !in(els[i-1]) {
					els[i], els[i-1] = els[i-1], els[i]
				}
			}
		}
	})
}

// Restyle changes the selection with f as one undo step, and the style
// new elements get.
func (ed *Editor) Restyle(f func(e *Element)) {
	f(ed.Style)
	ed.change(func() {
		for _, e := range ed.Selected() {
			f(e)
			if e.Type == "text" {
				e.FitText()
			}
			e.Touch()
		}
	})
	ed.Gen++
}

// SetText types s into the text element being edited.
func (ed *Editor) SetText(s string) {
	if e := ed.Scene.Find(ed.Editing); e != nil && e.Text != s {
		e.Text, e.OriginalText = s, s
		e.FitText()
		ed.Gen++
	}
}

// EditText starts typing into text element id again (a double click).
func (ed *Editor) EditText(id string) {
	if e := ed.Scene.Find(id); e != nil && e.Type == "text" {
		ed.textBefore = ed.snapshot()
		ed.Editing = id
		ed.Select(id)
	}
}

// EndText finishes typing: an empty text is removed. The text from its
// start is one undo step.
func (ed *Editor) EndText() {
	id := ed.Editing
	if id == "" {
		return
	}
	ed.Editing = ""
	if e := ed.Scene.Find(id); e != nil && e.Text == "" {
		ed.remove(id)
	} else if e != nil {
		e.Touch()
	}
	ed.commit(ed.textBefore)
	ed.textBefore = nil
	ed.Gen++
}

// bindable is what an arrow's end attaches to.
func bindable(e *Element) bool {
	switch e.Type {
	case "rectangle", "diamond", "ellipse", "text", "image", "frame":
		return true
	}
	return false
}

// bindEnds attaches arrow a's ends to the shapes they end on (or near),
// or lets them go, and puts them on those shapes' outlines.
func (ed *Editor) bindEnds(a *Element) {
	ed.unbind(a)
	t := a.Transform()
	for i, n := range []int{0, len(a.Points) - 1} {
		p := t.Apply(pt(a.Points[n][0], a.Points[n][1]))
		for j := len(ed.Scene.Elements) - 1; j >= 0; j-- {
			s := ed.Scene.Elements[j]
			if s == a || s.IsDeleted || !bindable(s) || !s.near(p, 10) { // past the 8 gap a routed end sits at
				continue
			}
			b := &Binding{ElementID: s.ID, Gap: 8}
			if i == 0 {
				a.StartBinding = b
			} else {
				a.EndBinding = b
			}
			if !slices.ContainsFunc(s.BoundElements, func(x Bound) bool { return x.ID == a.ID }) {
				s.BoundElements = append(s.BoundElements, Bound{ID: a.ID, Type: "arrow"})
			}
			break
		}
	}
	ed.route(a)
}

// near reports p inside e's box, or within tol of it.
func (e *Element) near(p Pt, tol float64) bool {
	q := e.Transform().Inverse().Apply(p)
	return q.X >= -tol && q.Y >= -tol && q.X <= e.Width+tol && q.Y <= e.Height+tol
}

// unbind lets both of arrow a's ends go.
func (ed *Editor) unbind(a *Element) {
	for _, b := range []*Binding{a.StartBinding, a.EndBinding} {
		if b == nil {
			continue
		}
		if s := ed.Scene.Find(b.ElementID); s != nil {
			s.BoundElements = slices.DeleteFunc(s.BoundElements, func(x Bound) bool { return x.ID == a.ID })
		}
	}
	a.StartBinding, a.EndBinding = nil, nil
}

// route puts arrow a's bound ends on their shapes' outlines, aimed at
// the next point (or, for a straight arrow bound at both ends, the other
// shape's center), Gap off: what Excalidraw does with focus 0.
func (ed *Editor) route(a *Element) {
	n := len(a.Points)
	if n < 2 {
		return
	}
	ends := []struct {
		b         *Binding
		at, aimAt int
	}{{a.StartBinding, 0, 1}, {a.EndBinding, n - 1, n - 2}}
	for i, end := range ends {
		if end.b == nil {
			continue
		}
		s := ed.Scene.Find(end.b.ElementID)
		if s == nil {
			continue
		}
		t := a.Transform()
		aim := t.Apply(pt(a.Points[end.aimAt][0], a.Points[end.aimAt][1]))
		if other := ends[1-i].b; n == 2 && other != nil {
			if o := ed.Scene.Find(other.ElementID); o != nil {
				x0, y0, x1, y1 := o.Bounds()
				aim = pt((x0+x1)/2, (y0+y1)/2)
			}
		}
		q := t.Inverse().Apply(s.edgePoint(aim, end.b.Gap))
		a.Points[end.at] = [2]float64{q.X, q.Y}
		ed.fitPoints(a)
	}
}

// edgePoint is where the line from e's center toward p leaves e's
// outline, gap further out.
func (e *Element) edgePoint(p Pt, gap float64) Pt {
	t := e.Transform()
	w, h := math.Max(e.Width/2, 1e-6), math.Max(e.Height/2, 1e-6)
	c := pt(w, h)
	d := t.Inverse().Apply(p).sub(c)
	if d.len() == 0 {
		d = pt(0, -1)
	}
	var k float64
	switch e.Type {
	case "ellipse":
		k = 1 / math.Hypot(d.X/w, d.Y/h)
	case "diamond":
		k = 1 / (math.Abs(d.X)/w + math.Abs(d.Y)/h)
	default:
		k = math.Min(w/math.Abs(d.X), h/math.Abs(d.Y))
	}
	return t.Apply(c.add(d.mul(k)).add(d.mul(gap / d.len())))
}

// moved settles what the gesture moved: an arrow moved without a shape
// it was bound to lets go, arrows bound to moved shapes follow them.
func (ed *Editor) moved() {
	for id := range ed.g.orig {
		if e := ed.Scene.Find(id); e != nil && ((e.StartBinding != nil && !ed.g.held(e.StartBinding.ElementID)) || (e.EndBinding != nil && !ed.g.held(e.EndBinding.ElementID))) {
			ed.unbind(e)
		}
	}
	ed.follow()
}

// follow re-routes the arrows bound to what the gesture holds.
func (ed *Editor) follow() {
	for _, a := range ed.Scene.Elements {
		if a.Type != "arrow" || a.IsDeleted {
			continue
		}
		if (a.StartBinding != nil && ed.g.held(a.StartBinding.ElementID)) || (a.EndBinding != nil && ed.g.held(a.EndBinding.ElementID)) {
			ed.route(a)
		}
	}
}
