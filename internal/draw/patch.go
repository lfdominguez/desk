package draw

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Patch is a change asked for by a Claude: elements to add (Excalidraw
// JSON with only what differs from the type's defaults), fields to change
// by id, ids to delete. Besides Excalidraw's keys, an element may carry
// label (text centered in a shape) and, for an arrow, from and to (the
// ids of the shapes its ends attach to).
type Patch struct {
	Add    []json.RawMessage `json:"add,omitempty"`
	Update []json.RawMessage `json:"update,omitempty"`
	Delete []string          `json:"delete,omitempty"`
}

// extras are a patch element's keys that aren't Excalidraw's.
type extras struct {
	ID    string  `json:"id"`
	Type  string  `json:"type"`
	Label *string `json:"label"`
	From  *string `json:"from"`
	To    *string `json:"to"`
}

var patchTypes = []string{"rectangle", "diamond", "ellipse", "arrow", "line", "text", "freedraw", "frame"}

// Apply makes p one undo step and says which elements it added or
// changed. Nothing changes when any part of p is wrong.
func (ed *Editor) Apply(p Patch) ([]string, error) {
	before := ed.snapshot()
	touched, err := ed.apply(p)
	if err != nil {
		ed.restore(before)
		return nil, err
	}
	ed.commit(before)
	return touched, nil
}

func (ed *Editor) apply(p Patch) ([]string, error) {
	var touched []string
	type link struct {
		e        *Element
		from, to *string
	}
	var links []link
	for i, raw := range p.Add {
		var x extras
		if err := json.Unmarshal(raw, &x); err != nil {
			return nil, fmt.Errorf("add %d: %v", i+1, err)
		}
		if !slices.Contains(patchTypes, x.Type) {
			return nil, fmt.Errorf("add %d: type %q is not one of %s", i+1, x.Type, strings.Join(patchTypes, ", "))
		}
		if x.ID != "" && ed.Scene.Find(x.ID) != nil {
			return nil, fmt.Errorf("add %d: id %s is taken", i+1, x.ID)
		}
		e := New(x.Type)
		id := e.ID
		if err := json.Unmarshal(raw, e); err != nil {
			return nil, fmt.Errorf("add %d: %v", i+1, err)
		}
		if x.ID == "" {
			e.ID = id
		}
		e.Version, e.IsDeleted = 1, false
		if (e.Type == "line" || e.Type == "arrow") && len(e.Points) < 2 {
			e.Points = [][2]float64{{0, 0}, {e.Width, e.Height}}
		}
		if e.Type == "text" {
			e.OriginalText = e.Text
			e.FitText()
		}
		ed.Scene.Elements = append(ed.Scene.Elements, e)
		if x.Label != nil {
			ed.label(e, *x.Label)
		}
		links = append(links, link{e, x.From, x.To})
		touched = append(touched, e.ID)
	}
	for i, raw := range p.Update {
		var x extras
		if err := json.Unmarshal(raw, &x); err != nil {
			return nil, fmt.Errorf("update %d: %v", i+1, err)
		}
		e := ed.Scene.Find(x.ID)
		if e == nil {
			return nil, fmt.Errorf("update %d: no element %q", i+1, x.ID)
		}
		typ := e.Type
		if err := json.Unmarshal(raw, e); err != nil {
			return nil, fmt.Errorf("update %d: %v", i+1, err)
		}
		e.Type, e.ID = typ, x.ID
		if e.Type == "text" {
			e.OriginalText = e.Text
			if e.ContainerID == nil {
				e.FitText()
			}
		}
		if x.Label != nil {
			ed.label(e, *x.Label)
		}
		e.Touch()
		links = append(links, link{e, x.From, x.To})
		touched = append(touched, e.ID)
	}
	for _, id := range p.Delete {
		e := ed.Scene.Find(id)
		if e == nil {
			return nil, fmt.Errorf("delete: no element %q", id)
		}
		ed.remove(id)
	}
	for _, l := range links {
		if l.from == nil && l.to == nil {
			continue
		}
		if l.e.Type != "arrow" && l.e.Type != "line" {
			return nil, fmt.Errorf("%s: from and to are for arrows", l.e.ID)
		}
		if err := ed.connect(l.e, l.from, l.to); err != nil {
			return nil, err
		}
	}
	held := map[string]*Element{}
	for _, id := range touched {
		held[id] = nil
	}
	g := ed.g
	ed.g.orig = held
	ed.follow()
	ed.g = g
	return touched, nil
}

// label puts text s centered in shape e (Excalidraw's bound text),
// replacing the one it had; "" takes it away.
func (ed *Editor) label(e *Element, s string) {
	for _, b := range e.BoundElements {
		if b.Type == "text" {
			ed.remove(b.ID)
		}
	}
	if s == "" {
		return
	}
	t := New("text")
	t.Text, t.OriginalText, t.TextAlign, t.VerticalAlign = s, s, "center", "middle"
	t.StrokeColor, t.Roughness, t.Opacity = e.StrokeColor, e.Roughness, e.Opacity
	t.ContainerID = &e.ID
	t.FitText()
	e.BoundElements = append(e.BoundElements, Bound{ID: t.ID, Type: "text"})
	ed.Scene.Elements = append(ed.Scene.Elements, t)
	center(t, e)
}

// center puts label t in the middle of its shape e, at e's angle.
func center(t, e *Element) {
	x0, y0, x1, y1 := e.local()
	c := e.Transform().Apply(pt((x0+x1)/2, (y0+y1)/2))
	t.X, t.Y, t.Angle = c.X-t.Width/2, c.Y-t.Height/2, e.Angle
}

// connect attaches arrow a's ends to the shapes from and to name, and
// draws it straight between them when it has only two points.
func (ed *Editor) connect(a *Element, from, to *string) error {
	ends := [2]*Element{}
	for i, id := range []*string{from, to} {
		if id == nil {
			continue
		}
		s := ed.Scene.Find(*id)
		if s == nil || !bindable(s) {
			return fmt.Errorf("%s: no shape %q to attach to", a.ID, *id)
		}
		ends[i] = s
	}
	t := a.Transform()
	abs := []Pt{t.Apply(pt(a.Points[0][0], a.Points[0][1])), t.Apply(pt(a.Points[len(a.Points)-1][0], a.Points[len(a.Points)-1][1]))}
	for i, s := range ends {
		if s != nil {
			x0, y0, x1, y1 := s.Bounds()
			abs[i] = pt((x0+x1)/2, (y0+y1)/2)
		}
	}
	if len(a.Points) == 2 {
		a.X, a.Y, a.Angle = abs[0].X, abs[0].Y, 0
		a.Points = [][2]float64{{0, 0}, {abs[1].X - abs[0].X, abs[1].Y - abs[0].Y}}
	}
	ed.unbind(a)
	for i, s := range ends {
		if s == nil {
			continue
		}
		b := &Binding{ElementID: s.ID, Gap: 8}
		if i == 0 {
			a.StartBinding = b
		} else {
			a.EndBinding = b
		}
		s.BoundElements = append(s.BoundElements, Bound{ID: a.ID, Type: "arrow"})
	}
	ed.route(a)
	return nil
}

// Preview is s with p applied, on a copy: what a suggestion would make.
func Preview(s *Scene, p Patch) (*Scene, []string, error) {
	b, _ := s.Marshal()
	c, _ := Parse(b) // just marshaled: it parses
	touched, err := NewEditor(c).Apply(p)
	return c, touched, err
}

// Summary lists s's elements for a Claude, one a line, in drawing order:
// id, type, box, what isn't a default, a shape's label, an arrow's ends.
func Summary(s *Scene) string {
	var b strings.Builder
	n := func(v float64) string { return strconv.FormatFloat(math.Round(v), 'f', -1, 64) }
	for _, e := range s.Elements {
		if e.IsDeleted || (e.Type == "text" && e.ContainerID != nil && s.Find(*e.ContainerID) != nil) {
			continue
		}
		fmt.Fprintf(&b, "%s %s x=%s y=%s w=%s h=%s", e.ID, e.Type, n(e.X), n(e.Y), n(e.Width), n(e.Height))
		if e.Angle != 0 {
			fmt.Fprintf(&b, " angle=%s", strconv.FormatFloat(math.Round(e.Angle*100)/100, 'f', -1, 64))
		}
		if e.StrokeColor != "#1e1e1e" {
			fmt.Fprintf(&b, " stroke=%s", e.StrokeColor)
		}
		if e.BackgroundColor != "transparent" && e.BackgroundColor != "" {
			fmt.Fprintf(&b, " bg=%s fill=%s", e.BackgroundColor, e.FillStyle)
		}
		if e.StrokeStyle != "solid" && e.StrokeStyle != "" {
			fmt.Fprintf(&b, " style=%s", e.StrokeStyle)
		}
		if e.Roughness != 1 && e.Type != "text" {
			fmt.Fprintf(&b, " roughness=%s", n(e.Roughness))
		}
		if e.Type == "text" {
			fmt.Fprintf(&b, " size=%s text=%q", n(e.FontSize), e.Text)
		}
		if e.Type == "frame" && e.Name != nil {
			fmt.Fprintf(&b, " name=%q", *e.Name)
		}
		if f := s.FrameOf(e); f != nil {
			fmt.Fprintf(&b, " frame=%s", f.ID)
		}
		if len(e.GroupIDs) > 0 {
			fmt.Fprintf(&b, " groups=%s", strings.Join(e.GroupIDs, ","))
		}
		for _, bd := range e.BoundElements {
			if t := s.Find(bd.ID); bd.Type == "text" && t != nil {
				fmt.Fprintf(&b, " label=%q", t.Text)
			}
		}
		if e.Type == "line" || e.Type == "arrow" {
			ps := make([]string, len(e.Points))
			for i, p := range e.Points {
				ps[i] = "[" + n(p[0]) + "," + n(p[1]) + "]"
			}
			fmt.Fprintf(&b, " points=[%s]", strings.Join(ps, ","))
			if e.StartBinding != nil {
				fmt.Fprintf(&b, " from=%s", e.StartBinding.ElementID)
			}
			if e.EndBinding != nil {
				fmt.Fprintf(&b, " to=%s", e.EndBinding.ElementID)
			}
		}
		b.WriteByte('\n')
	}
	if b.Len() == 0 {
		return "(empty)\n"
	}
	return b.String()
}
