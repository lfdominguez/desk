package draw

import (
	"image/color"
	"math"
	"strconv"
	"strings"
)

// SetKind says how a Set's ops are painted.
type SetKind int

const (
	Stroke SetKind = iota // stroked Width wide, dashed by Dash
	Fill                  // filled, nonzero
)

// Set is one painted path of an element, in the element's own units
// (relative to X, Y, before rotation).
type Set struct {
	Kind  SetKind
	Ops   []Op
	Color color.NRGBA
	Width float64
	Dash  []float64
}

// Shape is how e draws, as Excalidraw generates it: its fill, then its
// strokes, computed in rough.js's order (the outline first, the fill from
// the same random stream). canvas is the background, which outline
// arrowheads are filled with. Text and images draw themselves (see Render);
// a frame is its outline.
func Shape(e *Element, canvas color.NRGBA) []Set {
	stroke, okStroke := Color(e.StrokeColor, e.Opacity)
	bg, okBg := Color(e.BackgroundColor, e.Opacity)
	o := roughFor(e)
	sw := e.StrokeWidth
	var dash []float64
	switch e.StrokeStyle {
	case "dashed":
		dash, sw = []float64{8, 8 + e.StrokeWidth}, sw+0.5
	case "dotted":
		dash, sw = []float64{1.5, 6 + e.StrokeWidth}, sw+0.5
	}
	var fill, outline []Set
	stroked := func(ops []Op) {
		if okStroke {
			outline = append(outline, Set{Kind: Stroke, Ops: ops, Color: stroke, Width: sw, Dash: dash})
		}
	}
	// filled is the fill rough.js makes after the outline: solid's ops, or
	// hachure lines across poly.
	filled := func(poly []Pt, solid func() []Op) {
		switch {
		case !okBg:
		case e.FillStyle == "solid":
			fill = append(fill, Set{Kind: Fill, Ops: solid(), Color: bg})
		default:
			fill = append(fill, Set{Kind: Stroke, Ops: o.hachure(poly, e.FillStyle == "cross-hatch"), Color: bg, Width: o.fillWeight})
		}
	}
	w, h := e.Width, e.Height
	switch e.Type {
	case "rectangle", "iframe", "embeddable", "diamond":
		var p []Op
		switch {
		case e.Type == "diamond" && e.Roundness != nil:
			p = roundDiamond(w, h, e.Roundness)
		case e.Roundness != nil:
			p = roundRect(w, h, cornerRadius(math.Min(w, h), e.Roundness))
		}
		if p != nil {
			stroked(o.path(p))
			filled(flatten(p), func() []Op { return merged(o.solidCopy().path(p)) })
			break
		}
		poly := []Pt{pt(0, 0), pt(w, 0), pt(w, h), pt(0, h)}
		if e.Type == "diamond" {
			poly = []Pt{pt(w/2, 0), pt(w, h/2), pt(w/2, h), pt(0, h/2)}
		}
		stroked(o.linearPath(poly, true))
		filled(poly, func() []Op { return o.solidFill(poly) })
	case "ellipse":
		o.curveFitting = 1
		c := pt(w/2, h/2)
		inc, rx, ry := o.ellipseParams(w, h)
		ops, est := o.ellipse(c, inc, rx, ry)
		stroked(ops)
		filled(est, func() []Op { f, _ := o.ellipse(c, inc, rx, ry); return f })
	case "line", "arrow":
		ps := e.pts()
		if len(ps) == 0 {
			ps = []Pt{{}}
		}
		loop := e.Type == "line" && isLoop(ps) && okBg
		var ops []Op
		if e.Roundness == nil || e.Elbowed {
			ops = o.linearPath(ps, loop)
			stroked(ops)
			if loop {
				filled(ps, func() []Op { return o.solidFill(ps) })
			}
		} else {
			ops = o.smoothCurve(ps)
			stroked(ops)
			if loop {
				clean := *o
				clean.roughness = 0
				filled(flatten(clean.curveWithOffset(ps, 0)), func() []Op { return merged(o.solidCopy().smoothCurve(ps)) })
			}
		}
		if e.Type == "arrow" && okStroke {
			outline = append(outline, arrowhead(e, ops, false, e.StartArrowhead, stroke, canvas)...)
			outline = append(outline, arrowhead(e, ops, true, e.EndArrowhead, stroke, canvas)...)
		}
	case "freedraw":
		outline = append(outline, Set{Kind: Fill, Ops: freehand(e), Color: stroke})
	case "frame", "magicframe":
		outline = append(outline, Set{Kind: Stroke, Ops: roundRect(w, h, 8), Color: color.NRGBA{0xbb, 0xbb, 0xbb, 0xff}, Width: 2})
	}
	return append(fill, outline...)
}

// solidCopy is the options rough.js fills a path or curve with: the same
// random stream, rougher by its fillShapeRoughnessGain (0.8), drawn once.
func (o *rough) solidCopy() *rough {
	f := *o
	if f.roughness != 0 {
		f.roughness += 0.8
	}
	f.multiStroke = false
	return &f
}

// roundRect is Excalidraw's rounded rectangle path, corners r.
func roundRect(w, h, r float64) []Op {
	return []Op{mv(pt(r, 0)), ln(pt(w-r, 0)), quad(pt(w-r, 0), pt(w, 0), pt(w, r)), ln(pt(w, h-r)),
		quad(pt(w, h-r), pt(w, h), pt(w-r, h)), ln(pt(r, h)), quad(pt(r, h), pt(0, h), pt(0, h-r)),
		ln(pt(0, r)), quad(pt(0, r), pt(0, 0), pt(r, 0))}
}

// roundDiamond is Excalidraw's rounded diamond path.
func roundDiamond(w, h float64, round *Roundness) []Op {
	top, right, bottom, left := pt(w/2, 0), pt(w, h/2), pt(w/2, h), pt(0, h/2)
	vr, hr := cornerRadius(math.Abs(w/2), round), cornerRadius(math.Abs(h/2), round)
	return []Op{mv(pt(top.X+vr, top.Y+hr)), ln(pt(right.X-vr, right.Y-hr)), cu(right, right, pt(right.X-vr, right.Y+hr)),
		ln(pt(bottom.X+vr, bottom.Y-hr)), cu(bottom, bottom, pt(bottom.X-vr, bottom.Y-hr)),
		ln(pt(left.X+vr, left.Y+hr)), cu(left, left, pt(left.X+vr, left.Y-hr)),
		ln(pt(top.X-vr, top.Y+hr)), cu(top, top, pt(top.X+vr, top.Y+hr))}
}

// roughFor is the rough.js options Excalidraw uses for e.
func roughFor(e *Element) *rough {
	o := newRough(e.Seed, adjustRoughness(e), e.StrokeWidth)
	if e.StrokeStyle != "" && e.StrokeStyle != "solid" {
		o.multiStroke = false
		o.strokeWidth = e.StrokeWidth + 0.5
	}
	o.preserve = e.Roughness < 2
	return o
}

// adjustRoughness calms small shapes, which jitter too much at full
// roughness (Excalidraw's adjustRoughness).
func adjustRoughness(e *Element) float64 {
	r := e.Roughness
	maxS, minS := math.Max(e.Width, e.Height), math.Min(e.Width, e.Height)
	round := e.Roundness != nil && (e.Type == "rectangle" || e.Type == "diamond" || e.Type == "line" || e.Type == "arrow")
	linear := e.Type == "line" || e.Type == "arrow" || e.Type == "freedraw"
	if (minS >= 20 && maxS >= 50) || (minS >= 15 && round) || (linear && maxS >= 50) || r == 0 {
		return r
	}
	if maxS < 10 {
		return math.Min(r/3, 2.5)
	}
	return math.Min(r/2, 2.5)
}

func cornerRadius(x float64, r *Roundness) float64 {
	switch r.Type {
	case RoundProportional, RoundLegacy:
		return x * 0.25
	case RoundAdaptive:
		fixed := 32.0
		if r.Value != nil {
			fixed = *r.Value
		}
		if x <= fixed/0.25 {
			return x * 0.25
		}
		return fixed
	}
	return 0
}

// quad is a quadratic Bézier from a through c to b as a cubic.
func quad(a, c, b Pt) Op {
	return cu(a.add(c.sub(a).mul(2.0/3)), b.add(c.sub(b).mul(2.0/3)), b)
}

func (e *Element) pts() []Pt {
	ps := make([]Pt, len(e.Points))
	for i, p := range e.Points {
		ps[i] = pt(p[0], p[1])
	}
	return ps
}

// isLoop is a line whose ends meet, which Excalidraw fills.
func isLoop(ps []Pt) bool {
	return len(ps) > 2 && ps[0].sub(ps[len(ps)-1]).len() <= 8
}

// flatten samples a path's cubics into a polygon.
func flatten(ops []Op) []Pt {
	var ps []Pt
	var cur Pt
	for _, o := range ops {
		switch o.Kind {
		case 'M', 'L':
			cur = o.P[0]
			ps = append(ps, cur)
		case 'C':
			for i := 1; i <= 8; i++ {
				ps = append(ps, bez(cur, o.P[0], o.P[1], o.P[2], float64(i)/8))
			}
			cur = o.P[2]
		}
	}
	return ps
}

func bez(p0, p1, p2, p3 Pt, t float64) Pt {
	u := 1 - t
	return p0.mul(u * u * u).add(p1.mul(3 * u * u * t)).add(p2.mul(3 * u * t * t)).add(p3.mul(t * t * t))
}

// arrowhead is the head at an arrow's end (or start) as Excalidraw draws
// it: its direction from the drawn curve (the tip and the point 30% back
// from the end, or 70% along from the start), sized by the head and by the
// last segment, rough.js calmed (1 at most, 0.5 for dots); outline heads
// are filled with the canvas.
func arrowhead(e *Element, ops []Op, end bool, kind *string, stroke, canvas color.NRGBA) []Set {
	if kind == nil || len(ops) < 2 || len(e.Points) < 2 {
		return nil
	}
	var tip, back Pt
	if end {
		i := len(ops) - 1 // lines and curves end on a cubic
		prev := ops[i-1].P[0]
		if ops[i-1].Kind == 'C' {
			prev = ops[i-1].P[2]
		}
		tip, back = ops[i].P[2], bez(prev, ops[i].P[0], ops[i].P[1], ops[i].P[2], 0.7)
	} else {
		tip, back = ops[0].P[0], bez(ops[0].P[0], ops[1].P[0], ops[1].P[1], ops[1].P[2], 0.7)
	}
	d := tip.sub(back)
	if d.len() == 0 {
		return nil
	}
	n := d.mul(1 / d.len())
	ps := e.pts()
	seg := ps[len(ps)-1].sub(ps[len(ps)-2]).len()
	if !end {
		seg = ps[1].sub(ps[0]).len()
	}
	size, mult, angle := 15.0, 0.5, 25.0
	switch *kind {
	case "arrow":
		size, angle = 25, 20
	case "bar":
		angle = 90
	case "diamond", "diamond_outline":
		size, mult = 12, 0.25
	}
	m := math.Min(size, seg*mult)
	base := tip.sub(n.mul(m))
	rot := func(p Pt, deg float64) Pt {
		s, c := math.Sin(deg*math.Pi/180), math.Cos(deg*math.Pi/180)
		q := p.sub(tip)
		return tip.add(pt(q.X*c-q.Y*s, q.X*s+q.Y*c))
	}
	w1, w2 := rot(base, -angle), rot(base, angle)
	calm := func(max float64) *rough {
		o := roughFor(e)
		o.roughness = math.Min(max, o.roughness)
		return o
	}
	sw := roughFor(e).strokeWidth
	fillCol := stroke
	if strings.HasSuffix(*kind, "_outline") {
		fillCol = canvas
	}
	poly := func(ps []Pt) []Set {
		o := calm(1)
		line := o.linearPath(ps, true)
		return []Set{{Kind: Fill, Ops: o.solidFill(ps), Color: fillCol}, {Kind: Stroke, Ops: line, Color: stroke, Width: sw}}
	}
	switch *kind {
	case "dot", "circle", "circle_outline":
		o := calm(0.5)
		dia := base.sub(tip).len() + e.StrokeWidth - 2
		inc, rx, ry := o.ellipseParams(dia, dia)
		ring, _ := o.ellipse(tip, inc, rx, ry)
		dot, _ := o.ellipse(tip, inc, rx, ry)
		return []Set{{Kind: Fill, Ops: dot, Color: fillCol}, {Kind: Stroke, Ops: ring, Color: stroke, Width: sw}}
	case "triangle", "triangle_outline":
		return poly([]Pt{tip, w1, w2, tip})
	case "diamond", "diamond_outline":
		return poly([]Pt{tip, w1, tip.sub(n.mul(m * 2)), w2, tip})
	case "bar":
		return []Set{{Kind: Stroke, Ops: calm(1).line(w1, w2), Color: stroke, Width: sw}}
	}
	return []Set{{Kind: Stroke, Ops: calm(1).line(w1, tip), Color: stroke, Width: sw}, {Kind: Stroke, Ops: calm(1).line(w2, tip), Color: stroke, Width: sw}}
}

// Color parses an Excalidraw color (#rgb, #rrggbb, #rrggbbaa, a few names)
// with opacity 0–100 applied; ok is false for transparent.
func Color(s string, opacity float64) (c color.NRGBA, ok bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	if n, found := named[s]; found {
		s = n
	}
	if !strings.HasPrefix(s, "#") {
		return c, false
	}
	h := s[1:]
	if len(h) == 3 || len(h) == 4 {
		var b strings.Builder
		for _, r := range h {
			b.WriteRune(r)
			b.WriteRune(r)
		}
		h = b.String()
	}
	if len(h) == 6 {
		h += "ff"
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if len(h) != 8 || err != nil {
		return c, false
	}
	c = color.NRGBA{uint8(v >> 24), uint8(v >> 16), uint8(v >> 8), uint8(v)}
	if opacity >= 0 && opacity < 100 {
		c.A = uint8(float64(c.A) * opacity / 100)
	}
	return c, c.A > 0
}

var named = map[string]string{"black": "#000000", "white": "#ffffff", "red": "#ff0000", "green": "#008000",
	"blue": "#0000ff", "yellow": "#ffff00", "orange": "#ffa500", "gray": "#808080", "grey": "#808080", "transparent": ""}

// Transform maps e's own units to scene units: rotated by Angle around its
// center, then moved to X, Y.
func (e *Element) Transform() Aff {
	x0, y0, x1, y1 := e.local()
	c := pt((x0+x1)/2, (y0+y1)/2)
	return Translate(e.X, e.Y).Mul(Rotate(e.Angle, c))
}

// local is e's box in its own units: the points' extent for lines, else
// 0,0 to Width, Height.
func (e *Element) local() (x0, y0, x1, y1 float64) {
	if len(e.Points) > 0 && (e.Type == "line" || e.Type == "arrow" || e.Type == "freedraw") {
		x0, y0, x1, y1 = math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
		for _, p := range e.Points {
			x0, y0, x1, y1 = math.Min(x0, p[0]), math.Min(y0, p[1]), math.Max(x1, p[0]), math.Max(y1, p[1])
		}
		return
	}
	return 0, 0, e.Width, e.Height
}

// Bounds is e's box in scene units, rotation included.
func (e *Element) Bounds() (x0, y0, x1, y1 float64) {
	lx0, ly0, lx1, ly1 := e.local()
	t := e.Transform()
	x0, y0, x1, y1 = math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range []Pt{pt(lx0, ly0), pt(lx1, ly0), pt(lx1, ly1), pt(lx0, ly1)} {
		q := t.Apply(p)
		x0, y0, x1, y1 = math.Min(x0, q.X), math.Min(y0, q.Y), math.Max(x1, q.X), math.Max(y1, q.Y)
	}
	return
}

// Aff is a 2D affine map: x' = A x + C y + E, y' = B x + D y + F.
type Aff struct{ A, B, C, D, E, F float64 }

func Identity() Aff              { return Aff{A: 1, D: 1} }
func Translate(x, y float64) Aff { return Aff{A: 1, D: 1, E: x, F: y} }
func Scale(s float64) Aff        { return Aff{A: s, D: s} }

// Rotate turns by rad around c.
func Rotate(rad float64, c Pt) Aff {
	s, co := math.Sincos(rad)
	return Translate(c.X, c.Y).Mul(Aff{A: co, B: s, C: -s, D: co}).Mul(Translate(-c.X, -c.Y))
}

// Mul is m after n: (m.Mul(n)).Apply(p) == m.Apply(n.Apply(p)).
func (m Aff) Mul(n Aff) Aff {
	return Aff{
		A: m.A*n.A + m.C*n.B, B: m.B*n.A + m.D*n.B,
		C: m.A*n.C + m.C*n.D, D: m.B*n.C + m.D*n.D,
		E: m.A*n.E + m.C*n.F + m.E, F: m.B*n.E + m.D*n.F + m.F,
	}
}

func (m Aff) Apply(p Pt) Pt { return pt(m.A*p.X+m.C*p.Y+m.E, m.B*p.X+m.D*p.Y+m.F) }
