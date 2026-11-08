package draw

import (
	"image/color"
	"math"
	"strings"
	"testing"
)

func el(typ string, w, h float64) *Element {
	e := New(typ)
	e.Width, e.Height, e.Seed = w, h, 42
	return e
}

func kinds(sets []Set) (out []SetKind) {
	for _, s := range sets {
		out = append(out, s.Kind)
	}
	return
}

var (
	ink   = color.NRGBA{0x1e, 0x1e, 0x1e, 0xff}
	paper = color.NRGBA{0xff, 0xff, 0xff, 0xff}
	blue  = color.NRGBA{0xa5, 0xd8, 0xff, 0xff}
)

func TestShapeFills(t *testing.T) {
	for _, typ := range []string{"rectangle", "diamond", "ellipse", "embeddable"} {
		for _, round := range []bool{true, false} {
			e := el(typ, 120, 80)
			if !round {
				e.Roundness = nil
			}
			if sets := Shape(e, paper); len(sets) != 1 || sets[0].Kind != Stroke || sets[0].Color != ink || sets[0].Width != 2 {
				t.Errorf("%s (round %v) without a background: %v", typ, round, kinds(sets))
			}
			e.BackgroundColor = "#a5d8ff"
			sets := Shape(e, paper)
			if len(sets) != 2 || sets[0].Kind != Fill || sets[0].Color != blue || sets[1].Color != ink {
				t.Errorf("%s (round %v) solid: %v", typ, round, kinds(sets))
			}
			e.FillStyle = "hachure"
			sets = Shape(e, paper)
			if len(sets) != 2 || sets[0].Kind != Stroke || sets[0].Color != blue || sets[0].Width != 1 {
				t.Errorf("%s (round %v) hachure: %+v", typ, round, sets[0])
			}
			e.FillStyle = "cross-hatch"
			if c := Shape(e, paper); len(c[0].Ops) <= len(sets[0].Ops) {
				t.Errorf("%s cross-hatch has no more lines than hachure", typ)
			}
			e.StrokeColor = "transparent"
			if sets := Shape(e, paper); len(sets) != 1 || sets[0].Color != blue {
				t.Errorf("%s with a transparent stroke draws it", typ)
			}
		}
	}
}

func TestShapeStrokeStyles(t *testing.T) {
	e := el("rectangle", 100, 100)
	solid := Shape(e, paper)[0]
	e.StrokeStyle = "dashed"
	d := Shape(e, paper)[0]
	if d.Width != 2.5 || len(d.Dash) != 2 || d.Dash[0] != 8 || d.Dash[1] != 10 {
		t.Errorf("dashed: %+v", d)
	}
	if len(d.Ops) >= len(solid.Ops) {
		t.Error("a dashed stroke is drawn twice")
	}
	e.StrokeStyle = "dotted"
	if d := Shape(e, paper)[0]; d.Dash[0] != 1.5 || d.Dash[1] != 8 {
		t.Errorf("dotted: %v", d.Dash)
	}
}

func TestShapeRoundedCorners(t *testing.T) {
	e := el("rectangle", 200, 100)
	e.Roughness = 0
	ops := Shape(e, paper)[0].Ops
	if ops[0] != mv(pt(25, 0)) {
		t.Errorf("a rounded rectangle starts after its corner: %v", ops[0])
	}
	e.Roundness = nil
	if ops := Shape(e, paper)[0].Ops; ops[0] != mv(pt(0, 0)) {
		t.Errorf("a sharp one at its corner: %v", ops[0])
	}
	d := el("diamond", 200, 100)
	d.Roughness = 0
	if ops := Shape(d, paper)[0].Ops; ops[0] != mv(pt(125, 12.5)) {
		t.Errorf("a rounded diamond starts past its top: %v", ops[0])
	}
}

func TestLines(t *testing.T) {
	l := el("line", 100, 100)
	l.Roughness = 0
	l.Points = [][2]float64{{0, 0}, {100, 0}, {100, 100}, {2, 3}}
	l.BackgroundColor = "#a5d8ff"
	if k := kinds(Shape(l, paper)); len(k) != 2 || k[0] != Fill {
		t.Errorf("a closed curved line is filled: %v", k)
	}
	l.Roundness = nil
	if k := kinds(Shape(l, paper)); len(k) != 2 || k[0] != Fill {
		t.Errorf("a closed sharp line is filled: %v", k)
	}
	l.Points[3] = [2]float64{50, 50}
	if k := kinds(Shape(l, paper)); len(k) != 1 {
		t.Errorf("an open line isn't filled: %v", k)
	}
	l.Points = [][2]float64{{0, 0}, {50, 80}, {100, 0}}
	l.Roundness = &Roundness{Type: RoundProportional}
	curved := Shape(l, paper)[0].Ops
	l.Roundness = nil
	sharp := Shape(l, paper)[0].Ops
	if len(curved) == len(sharp) || sharp[1].P[2] != pt(50, 80) {
		t.Errorf("curved %d ops, sharp %d", len(curved), len(sharp))
	}
	a := el("arrow", 100, 100)
	a.Points = [][2]float64{{0, 0}, {100, 0}, {100, 100}, {0, 0}}
	a.BackgroundColor = "#a5d8ff"
	if sets := Shape(a, paper); sets[0].Kind != Stroke {
		t.Error("a closed arrow is filled")
	}
	a.Elbowed, a.Roughness = true, 0
	if sets := Shape(a, paper); sets[0].Ops[1].P[2] != pt(100, 0) {
		t.Error("an elbow arrow curves")
	}
}

func TestArrowheads(t *testing.T) {
	str := func(s string) *string { return &s }
	red := color.NRGBA{0xff, 0, 0, 0xff}
	for head, n := range map[string]int{"arrow": 2, "bar": 1, "dot": 2, "circle": 2, "circle_outline": 2,
		"triangle": 2, "triangle_outline": 2, "diamond": 2, "diamond_outline": 2, "crowfoot_one": 2} {
		a := el("arrow", 200, 0)
		a.Points = [][2]float64{{0, 0}, {200, 0}}
		a.EndArrowhead = str(head)
		sets := Shape(a, red)
		if got := len(sets) - 1; got != n {
			t.Errorf("%s: %d sets, want %d", head, got, n)
		}
		if n == 2 && sets[1].Kind == Fill {
			want := ink
			if strings.HasSuffix(head, "_outline") {
				want = red
			}
			if sets[1].Color != want {
				t.Errorf("%s is filled with %v", head, sets[1].Color)
			}
		}
	}
	a := el("arrow", 200, 0)
	a.Roughness = 0
	a.Points = [][2]float64{{0, 0}, {200, 0}}
	a.StartArrowhead = str("bar")
	sets := Shape(a, paper)
	if len(sets) != 4 {
		t.Fatalf("both heads: %d sets", len(sets))
	}
	bar := sets[1].Ops
	if !near(bar[0].P[0].X, 0) || !near(math.Abs(bar[0].P[0].Y), 15) {
		t.Errorf("the start bar is across the start: %v", bar)
	}
	wing := sets[2].Ops
	if !near(wing[0].P[0].X, 200-25*math.Cos(20*math.Pi/180)) || !near(math.Abs(wing[0].P[0].Y), 25*math.Sin(20*math.Pi/180)) || wing[1].P[2] != pt(200, 0) {
		t.Errorf("the end arrow's wing: %v", wing)
	}
	a.Points = [][2]float64{{0, 0}, {20, 0}}
	if w := Shape(a, paper)[2].Ops[0].P[0]; !near(w.X, 20-10*math.Cos(20*math.Pi/180)) {
		t.Errorf("a short arrow's head is half its length: %v", w)
	}
	a.Points, a.StartArrowhead, a.Roundness = [][2]float64{{0, 0}, {20, 0}, {300, 0}}, str("arrow"), nil
	sets = Shape(a, paper)
	if w := sets[1].Ops[0].P[0]; !near(w.X, 10*math.Cos(20*math.Pi/180)) {
		t.Errorf("the start head sizes by the first segment: %v", w)
	}
	if w := sets[3].Ops[0].P[0]; !near(w.X, 300-25*math.Cos(20*math.Pi/180)) {
		t.Errorf("the end head sizes by the last segment: %v", w)
	}
	a.Points = [][2]float64{{0, 0}, {0, 0}}
	if n := len(Shape(a, paper)); n != 1 {
		t.Errorf("a zero-length arrow has heads: %d", n)
	}
	a.Points = nil
	if sets := Shape(a, paper); len(sets) != 1 {
		t.Errorf("an arrow without points: %v", sets)
	}
	a.Points = [][2]float64{{0, 0}}
	if n := len(Shape(a, paper)); n != 1 {
		t.Errorf("a one-point arrow has heads: %d", n)
	}
	a.Points = [][2]float64{{0, 0}, {200, 0}}
	a.StrokeColor = "transparent"
	if n := len(Shape(a, paper)); n != 0 {
		t.Errorf("an invisible arrow has heads: %d", n)
	}
	if arrowhead(a, []Op{mv(pt(0, 0))}, true, str("arrow"), ink, paper) != nil {
		t.Error("a head on one op")
	}
}

func TestOtherShapes(t *testing.T) {
	f := el("freedraw", 0, 0)
	f.Points = [][2]float64{{0, 0}, {50, 10}, {100, 0}}
	if sets := Shape(f, paper); len(sets) != 1 || sets[0].Kind != Fill || sets[0].Color != ink {
		t.Errorf("freedraw: %+v", sets)
	}
	fr := el("frame", 300, 200)
	if sets := Shape(fr, paper); len(sets) != 1 || sets[0].Color != (color.NRGBA{0xbb, 0xbb, 0xbb, 0xff}) || sets[0].Ops[0] != mv(pt(8, 0)) {
		t.Errorf("frame: %+v", sets)
	}
	for _, typ := range []string{"text", "image"} {
		if sets := Shape(el(typ, 10, 10), paper); sets != nil {
			t.Errorf("%s draws itself, not sets", typ)
		}
	}
}

func TestFreehand(t *testing.T) {
	f := el("freedraw", 0, 0)
	f.Points = nil
	if freehand(f) != nil {
		t.Error("no points draw")
	}
	for _, pts := range [][][2]float64{{{5, 5}}, {{5, 5}, {5.1, 5}}} {
		f.Points = pts
		for _, p := range flatten(freehand(f)) {
			if r := p.sub(pt(5, 5)).len(); r > 5 || r < 1 {
				t.Errorf("%d points: a dot's outline at %v (r %v)", len(pts), p, r)
			}
		}
	}
	width := func(press []float64, sim bool) float64 {
		g := el("freedraw", 0, 0)
		g.SimulatePressure, g.Pressures = sim, press
		for i := 0; i <= 20; i++ {
			g.Points = append(g.Points, [2]float64{float64(i * 10), 0})
		}
		y0, y1 := math.Inf(1), math.Inf(-1)
		for _, p := range flatten(freehand(g)) {
			y0, y1 = math.Min(y0, p.Y), math.Max(y1, p.Y)
		}
		return y1 - y0
	}
	hard, soft := make([]float64, 21), make([]float64, 21)
	for i := range hard {
		hard[i], soft[i] = 1, 0
	}
	if h, s := width(hard, false), width(soft, false); h <= s || h > 4.25*2*1.01 {
		t.Errorf("pressure widens: hard %v, soft %v", h, s)
	}
	if fast, slow := width(nil, true), width(hard, false); fast >= slow {
		t.Errorf("a fast pen draws thinner: %v vs %v", fast, slow)
	}
	g := el("freedraw", 0, 0)
	g.Points = [][2]float64{{0, 0}, {100, 0}}
	ops := freehand(g)
	if ops[0].Kind != 'M' || ops[len(ops)-1].P[2] != ops[0].P[0] {
		t.Error("the outline is closed")
	}
	xs := flatten(ops)
	x0, x1 := math.Inf(1), math.Inf(-1)
	for _, p := range xs {
		x0, x1 = math.Min(x0, p.X), math.Max(x1, p.X)
	}
	if x0 > -1 || x1 < 101 {
		t.Errorf("round caps reach past the ends: %v..%v", x0, x1)
	}
}

func TestStyleRules(t *testing.T) {
	e := el("rectangle", 100, 100)
	e.Roughness = 2
	for _, c := range []struct {
		typ   string
		w, h  float64
		round bool
		want  float64
	}{
		{"rectangle", 100, 100, false, 2},
		{"rectangle", 30, 15, true, 2},
		{"rectangle", 30, 15, false, 1},
		{"rectangle", 8, 8, false, 2.0 / 3},
		{"arrow", 60, 0, false, 2},
		{"ellipse", 40, 16, true, 1},
		{"rectangle", 30, 30, false, 1},
		{"rectangle", 30, 10, true, 1},
		{"arrow", 40, 0, false, 1},
	} {
		e.Type, e.Width, e.Height = c.typ, c.w, c.h
		e.Roundness = nil
		if c.round {
			e.Roundness = &Roundness{Type: RoundAdaptive}
		}
		if got := adjustRoughness(e); !near(got, c.want) {
			t.Errorf("%+v: roughness %v", c, got)
		}
	}
	e.Roughness, e.Width = 0, 5
	if adjustRoughness(e) != 0 {
		t.Error("a clean shape got rough")
	}
	v := 10.0
	for _, c := range []struct {
		r    Roundness
		x    float64
		want float64
	}{
		{Roundness{Type: RoundProportional}, 100, 25},
		{Roundness{Type: RoundLegacy}, 40, 10},
		{Roundness{Type: RoundAdaptive}, 100, 25},
		{Roundness{Type: RoundAdaptive}, 128, 32},
		{Roundness{Type: RoundAdaptive}, 400, 32},
		{Roundness{Type: RoundAdaptive, Value: &v}, 400, 10},
		{Roundness{Type: 9}, 400, 0},
	} {
		if got := cornerRadius(c.x, &c.r); got != c.want {
			t.Errorf("radius %+v of %v = %v", c.r, c.x, got)
		}
	}
	o := roughFor(&Element{Seed: 1, Roughness: 1, StrokeWidth: 2, StrokeStyle: "dashed", Width: 100, Height: 100})
	if o.multiStroke || o.strokeWidth != 2.5 || !o.preserve {
		t.Errorf("dashed options: %+v", o)
	}
	if o := roughFor(&Element{Seed: 1, Roughness: 2, StrokeWidth: 2, Width: 100, Height: 100}); o.preserve || !o.multiStroke {
		t.Errorf("cartoonist options: %+v", o)
	}
}

func TestColor(t *testing.T) {
	for in, want := range map[string]color.NRGBA{
		"#abc":      {0xaa, 0xbb, 0xcc, 0xff},
		"#a5d8ff":   {0xa5, 0xd8, 0xff, 0xff},
		" #A5D8FF ": {0xa5, 0xd8, 0xff, 0xff},
		"#a5d8ff80": {0xa5, 0xd8, 0xff, 0x80},
		"#abcd":     {0xaa, 0xbb, 0xcc, 0xdd},
		"red":       {0xff, 0, 0, 0xff},
	} {
		if c, ok := Color(in, 100); !ok || c != want {
			t.Errorf("Color(%q) = %v %v", in, c, ok)
		}
	}
	for _, in := range []string{"transparent", "", "#12345", "#gggggg", "rgb(1,2,3)", "#00000000"} {
		if _, ok := Color(in, 100); ok {
			t.Errorf("Color(%q) is visible", in)
		}
	}
	if c, _ := Color("#000", 50); c.A != 127 {
		t.Errorf("half opacity: %v", c.A)
	}
	if _, ok := Color("#000", 0); ok {
		t.Error("zero opacity is visible")
	}
	if c, _ := Color("#000", -1); c.A != 0xff {
		t.Error("an unset opacity hides")
	}
}

func TestTransformAndBounds(t *testing.T) {
	e := el("rectangle", 100, 50)
	e.X, e.Y = 10, 20
	if x0, y0, x1, y1 := e.Bounds(); x0 != 10 || y0 != 20 || x1 != 110 || y1 != 70 {
		t.Errorf("bounds %v %v %v %v", x0, y0, x1, y1)
	}
	e.Angle = math.Pi / 2
	x0, y0, x1, y1 := e.Bounds()
	if !near(x0, 35) || !near(y0, -5) || !near(x1, 85) || !near(y1, 95) {
		t.Errorf("turned bounds %v %v %v %v", x0, y0, x1, y1)
	}
	if p := e.Transform().Apply(pt(0, 0)); !near(p.X, 85) || !near(p.Y, -5) {
		t.Errorf("the top left turns around the center to %v", p)
	}
	l := el("line", 0, 0)
	l.X, l.Y = 100, 100
	l.Points = [][2]float64{{0, 0}, {-50, 20}, {30, 40}}
	if x0, y0, x1, y1 := l.Bounds(); x0 != 50 || y0 != 100 || x1 != 130 || y1 != 140 {
		t.Errorf("line bounds %v %v %v %v", x0, y0, x1, y1)
	}
	l.Points = nil
	l.Width, l.Height = 30, 20
	if x0, y0, x1, y1 := l.Bounds(); x0 != 100 || y0 != 100 || x1 != 130 || y1 != 120 {
		t.Errorf("a line without points is its box: %v %v %v %v", x0, y0, x1, y1)
	}
	if p := Rotate(0.3, pt(1, 2)).Mul(Rotate(0.5, pt(1, 2))).Apply(pt(7, 3)); p.sub(Rotate(0.8, pt(1, 2)).Apply(pt(7, 3))).len() > 1e-9 {
		t.Errorf("two turns compose: %v", p)
	}
	m := Translate(1, 2).Mul(Scale(3))
	if p := m.Apply(pt(1, 1)); p != pt(4, 5) {
		t.Errorf("translate after scale: %v", p)
	}
	if p := Identity().Mul(Rotate(math.Pi, pt(1, 1))).Apply(pt(2, 1)); !near(p.X, 0) || !near(p.Y, 1) {
		t.Errorf("half turn: %v", p)
	}
}

func TestPathHelpers(t *testing.T) {
	q := quad(pt(0, 0), pt(10, 10), pt(20, 0))
	if !near(q.P[0].X, 20.0/3) || !near(q.P[0].Y, 20.0/3) || !near(q.P[1].X, 40.0/3) || !near(q.P[1].Y, 20.0/3) || q.P[2] != pt(20, 0) {
		t.Errorf("quad as cubic: %v", q)
	}
	if p := bez(pt(0, 0), pt(0, 10), pt(10, 10), pt(10, 0), 0.5); p != pt(5, 7.5) {
		t.Errorf("midpoint %v", p)
	}
	ps := flatten([]Op{mv(pt(0, 0)), ln(pt(10, 0)), cu(pt(10, 5), pt(5, 10), pt(0, 10))})
	if len(ps) != 10 || ps[1] != pt(10, 0) || ps[9] != pt(0, 10) {
		t.Errorf("flatten: %v", ps)
	}
	if isLoop([]Pt{pt(0, 0), pt(9, 0)}) || !isLoop([]Pt{pt(0, 0), pt(50, 0), pt(5, 5)}) || isLoop([]Pt{pt(0, 0), pt(50, 0), pt(9, 0)}) {
		t.Error("isLoop")
	}
}
