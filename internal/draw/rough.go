package draw

import (
	"math"
	"sort"
)

// rough.go ports rough.js (MIT, Preet Shihn), which Excalidraw draws with:
// lines bowed and drawn twice with seeded jitter, ellipses fitted through
// jittered points, hachure fills. The same seed always gives the same
// strokes, so a shape doesn't wobble between frames.

// Pt is a point in scene units.
type Pt struct{ X, Y float64 }

// Op is one path command: 'M' move to P[0], 'L' line to P[0], 'C' cubic
// through P[0], P[1] to P[2].
type Op struct {
	Kind byte
	P    [3]Pt
}

func mv(p Pt) Op              { return Op{Kind: 'M', P: [3]Pt{p}} }
func ln(p Pt) Op              { return Op{Kind: 'L', P: [3]Pt{p}} }
func cu(a, b, c Pt) Op        { return Op{Kind: 'C', P: [3]Pt{a, b, c}} }
func pt(x, y float64) Pt      { return Pt{x, y} }
func (p Pt) add(q Pt) Pt      { return Pt{p.X + q.X, p.Y + q.Y} }
func (p Pt) sub(q Pt) Pt      { return Pt{p.X - q.X, p.Y - q.Y} }
func (p Pt) mul(s float64) Pt { return Pt{p.X * s, p.Y * s} }
func (p Pt) len() float64     { return math.Hypot(p.X, p.Y) }

// rough holds the options of one rough.js generator call and its random
// stream, which starts from the seed on every call, as rough.js does.
type rough struct {
	seed, seed0  int32
	roughness    float64
	bowing       float64
	maxOffset    float64
	curveFitting float64
	curveSteps   float64
	hachureAngle float64
	hachureGap   float64
	fillWeight   float64
	strokeWidth  float64
	multiStroke  bool
	preserve     bool
}

func newRough(seed int64, roughness, strokeWidth float64) *rough {
	r := &rough{seed: int32(seed), roughness: roughness, bowing: 1, maxOffset: 2, curveFitting: 0.95,
		curveSteps: 9, hachureAngle: -41, hachureGap: strokeWidth * 4, fillWeight: strokeWidth / 2,
		strokeWidth: strokeWidth, multiStroke: roughness > 0}
	if r.seed == 0 {
		r.seed = 1
	}
	r.seed0 = r.seed
	return r
}

// next is rough.js's Random: a Park–Miller step in 32-bit integers.
func (o *rough) next() float64 {
	o.seed = int32(uint32(o.seed) * 48271)
	return float64(o.seed&0x7fffffff) / (1 << 31)
}

func (o *rough) offset(min, max, gain float64) float64 {
	return o.roughness * gain * (o.next()*(max-min) + min)
}

func (o *rough) offsetOpt(x, gain float64) float64 { return o.offset(-x, x, gain) }

func (o *rough) line(a, b Pt) []Op { return o.doubleLine(a, b, o.multiStroke) }

func (o *rough) doubleLine(a, b Pt, twice bool) []Op {
	ops := o.oneLine(a, b, true, false)
	if twice {
		ops = append(ops, o.oneLine(a, b, true, true)...)
	}
	return ops
}

func (o *rough) oneLine(a, b Pt, move, overlay bool) []Op {
	lenSq := (a.X-b.X)*(a.X-b.X) + (a.Y-b.Y)*(a.Y-b.Y)
	length := math.Sqrt(lenSq)
	gain := 1.0
	switch {
	case length < 200:
	case length > 500:
		gain = 0.4
	default:
		gain = -0.0016668*length + 1.233334
	}
	off := o.maxOffset
	if off*off*100 > lenSq {
		off = length / 10
	}
	half := off / 2
	diverge := 0.2 + o.next()*0.2
	midX := o.offsetOpt(o.bowing*o.maxOffset*(b.Y-a.Y)/200, gain)
	midY := o.offsetOpt(o.bowing*o.maxOffset*(a.X-b.X)/200, gain)
	rnd := func() float64 {
		if overlay {
			return o.offsetOpt(half, gain)
		}
		return o.offsetOpt(off, gain)
	}
	end := func() float64 {
		if o.preserve {
			return 0
		}
		return rnd()
	}
	var ops []Op
	if move {
		ops = append(ops, mv(pt(a.X+end(), a.Y+end())))
	}
	c1 := pt(midX+a.X+(b.X-a.X)*diverge+rnd(), midY+a.Y+(b.Y-a.Y)*diverge+rnd())
	c2 := pt(midX+a.X+2*(b.X-a.X)*diverge+rnd(), midY+a.Y+2*(b.Y-a.Y)*diverge+rnd())
	return append(ops, cu(c1, c2, pt(b.X+end(), b.Y+end())))
}

func (o *rough) linearPath(ps []Pt, closed bool) []Op {
	if len(ps) < 2 {
		return nil
	}
	if len(ps) == 2 {
		return o.line(ps[0], ps[1])
	}
	var ops []Op
	for i := 0; i+1 < len(ps); i++ {
		ops = append(ops, o.line(ps[i], ps[i+1])...)
	}
	if closed {
		ops = append(ops, o.line(ps[len(ps)-1], ps[0])...)
	}
	return ops
}

// ellipseParams is rough.js's generateEllipseParams: the step and the radii,
// jittered by the curve fitting. The stroke and a solid fill share them.
func (o *rough) ellipseParams(w, h float64) (inc, rx, ry float64) {
	psq := math.Sqrt(math.Pi * 2 * math.Sqrt((math.Pow(w/2, 2)+math.Pow(h/2, 2))/2))
	steps := math.Ceil(math.Max(o.curveSteps, o.curveSteps/math.Sqrt(200)*psq))
	inc = math.Pi * 2 / steps
	rx, ry = math.Abs(w/2), math.Abs(h/2)
	fit := 1 - o.curveFitting
	rx += o.offsetOpt(rx*fit, 1)
	ry += o.offsetOpt(ry*fit, 1)
	return
}

// ellipse is rough.js's ellipseWithParams centered at c: the stroke and the
// points a pattern fill uses.
func (o *rough) ellipse(c Pt, inc, rx, ry float64) (ops []Op, est []Pt) {
	ap1, cp1 := o.ellipsePoints(inc, c, rx, ry, 1, inc*o.offset(0.1, o.offset(0.4, 1, 1), 1))
	ops = o.curve(ap1)
	if o.multiStroke {
		ap2, _ := o.ellipsePoints(inc, c, rx, ry, 1.5, 0)
		ops = append(ops, o.curve(ap2)...)
	}
	return ops, cp1
}

func (o *rough) ellipsePoints(inc float64, c Pt, rx, ry, off, overlap float64) (all, core []Pt) {
	at := func(r, a float64, jitter bool) Pt {
		p := pt(c.X+r*rx*math.Cos(a), c.Y+r*ry*math.Sin(a))
		if jitter {
			p.X += o.offsetOpt(off, 1)
			p.Y += o.offsetOpt(off, 1)
		}
		return p
	}
	if o.roughness == 0 {
		inc /= 4
		all = append(all, at(1, -inc, false))
		for a := 0.0; a <= math.Pi*2; a += inc {
			p := at(1, a, false)
			core = append(core, p)
			all = append(all, p)
		}
		return append(all, at(1, 0, false), at(1, inc, false)), core
	}
	rad := o.offsetOpt(0.5, 1) - math.Pi/2
	all = append(all, at(0.9, rad-inc, true))
	for a := rad; a < math.Pi*2+rad-0.01; a += inc {
		p := at(1, a, true)
		core = append(core, p)
		all = append(all, p)
	}
	all = append(all, at(1, rad+overlap*0.5, true), at(0.98, rad+overlap, true), at(0.9, rad+overlap*0.5, true))
	return all, core
}

// curve is rough.js's _curve: a Catmull-Rom spline through ps, from ps[1]
// to the second-to-last point (curveTightness 0, as Excalidraw leaves it).
func (o *rough) curve(ps []Pt) []Op {
	switch n := len(ps); {
	case n > 3:
		ops := []Op{mv(ps[1])}
		for i := 1; i+2 < n; i++ {
			b1 := pt(ps[i].X+(ps[i+1].X-ps[i-1].X)/6, ps[i].Y+(ps[i+1].Y-ps[i-1].Y)/6)
			b2 := pt(ps[i+1].X+(ps[i].X-ps[i+2].X)/6, ps[i+1].Y+(ps[i].Y-ps[i+2].Y)/6)
			ops = append(ops, cu(b1, b2, ps[i+1]))
		}
		return ops
	case n == 3:
		return []Op{mv(ps[1]), cu(ps[1], ps[2], ps[2])}
	case n == 2:
		return o.oneLine(ps[0], ps[1], true, true)
	}
	return nil
}

// smoothCurve is rough.js's curve(): a jittered spline through ps (lines and
// arrows with round corners), drawn twice, the second time with seed+1.
func (o *rough) smoothCurve(ps []Pt) []Op {
	ops := o.curveWithOffset(ps, 1*(1+o.roughness*0.2))
	if o.multiStroke {
		o2 := *o
		o2.seed = o.seed0 + 1
		ops = append(ops, o2.curveWithOffset(ps, 1.5*(1+o.roughness*0.22))...)
	}
	return ops
}

func (o *rough) curveWithOffset(ps []Pt, off float64) []Op {
	if len(ps) == 0 {
		return nil
	}
	j := func(p Pt) Pt { return pt(p.X+o.offsetOpt(off, 1), p.Y+o.offsetOpt(off, 1)) }
	q := []Pt{j(ps[0]), j(ps[0])}
	for i := 1; i < len(ps); i++ {
		q = append(q, j(ps[i]))
		if i == len(ps)-1 {
			q = append(q, j(ps[i]))
		}
	}
	return o.curve(q)
}

// path is rough.js's svgPath: a path of lines and cubics (rounded
// rectangles and diamonds) sketched, lines as doubled rough lines, cubics
// with jittered control points; a move draws nothing by itself.
func (o *rough) path(segs []Op) []Op {
	var ops []Op
	var cur Pt
	for _, s := range segs {
		switch s.Kind {
		case 'M':
			cur = s.P[0]
		case 'L':
			ops = append(ops, o.line(cur, s.P[0])...)
			cur = s.P[0]
		case 'C':
			ops = append(ops, o.bezier(cur, s.P[0], s.P[1], s.P[2])...)
			cur = s.P[2]
		}
	}
	return ops
}

func (o *rough) bezier(cur, c1, c2, p Pt) []Op {
	ros := [2]float64{o.maxOffset, o.maxOffset + 0.3}
	n := 1
	if o.multiStroke {
		n = 2
	}
	var ops []Op
	j := func(q Pt, r float64) Pt { return pt(q.X+o.offsetOpt(r, 1), q.Y+o.offsetOpt(r, 1)) }
	for i := 0; i < n; i++ {
		if i == 0 || o.preserve {
			ops = append(ops, mv(cur))
		} else {
			ops = append(ops, mv(j(cur, ros[0])))
		}
		end := p
		if !o.preserve {
			end = j(p, ros[i])
		}
		ops = append(ops, cu(j(c1, ros[i]), j(c2, ros[i]), end))
	}
	return ops
}

// solidFill is the polygon jittered by the roughness, as one filled path.
func (o *rough) solidFill(ps []Pt) []Op {
	if len(ps) < 3 {
		return nil
	}
	ops := make([]Op, 0, len(ps))
	for i, p := range ps {
		q := pt(p.X+o.offsetOpt(o.maxOffset, 1), p.Y+o.offsetOpt(o.maxOffset, 1))
		if i == 0 {
			ops = append(ops, mv(q))
		} else {
			ops = append(ops, ln(q))
		}
	}
	return ops
}

// merged is rough.js's _mergedShape: one path, its later moves dropped,
// which a solid fill of a path or curve is.
func merged(ops []Op) []Op {
	out := ops[:0:0]
	for i, op := range ops {
		if i == 0 || op.Kind != 'M' {
			out = append(out, op)
		}
	}
	return out
}

// hachure is rough.js's hachure (cross: hatch) filler: scan lines across
// the polygon, each a sketched line. At roughness 0 each is drawn once, since
// a second would land on the first.
func (o *rough) hachure(ps []Pt, cross bool) []Op {
	ops := o.hachureAt(ps, o.hachureAngle)
	if cross {
		ops = append(ops, o.hachureAt(ps, o.hachureAngle+90)...)
	}
	return ops
}

// hachureAt is polygonHachureLines then renderLines: the gap (strokeWidth×4
// by default, rounded), and at roughness 1 or more a three-in-ten chance to
// step gap at a time instead of one.
func (o *rough) hachureAt(ps []Pt, angle float64) []Op {
	gap := math.Round(math.Max(o.hachureGap, 0.1))
	step := 1.0
	if o.roughness >= 1 && o.next() > 0.7 {
		step = gap
	}
	var ops []Op
	for _, l := range hachureLines(ps, gap, angle+90, step) {
		ops = append(ops, o.doubleLine(l[0], l[1], o.roughness > 0)...)
	}
	return ops
}

// hachureLines ports hachure-fill (MIT, Preet Shihn): the polygon turned by
// deg, scanned every step units with a line every gap scans (or every scan
// when step isn't 1) between pairs of active edges, x rounded, turned back.
func hachureLines(ps []Pt, gap, deg, step float64) [][2]Pt {
	gap = math.Max(gap, 0.1)
	a := deg * math.Pi / 180
	rot := func(p Pt, a float64) Pt {
		s, c := math.Sin(a), math.Cos(a)
		return pt(p.X*c-p.Y*s, p.X*s+p.Y*c)
	}
	vs := make([]Pt, 0, len(ps)+1)
	for _, p := range ps {
		vs = append(vs, rot(p, a))
	}
	if len(vs) > 0 && vs[0] != vs[len(vs)-1] {
		vs = append(vs, vs[0])
	}
	if len(vs) <= 2 {
		return nil
	}
	type edge struct{ ymin, ymax, x, islope float64 }
	var edges []*edge
	for i := 0; i+1 < len(vs); i++ {
		p1, p2 := vs[i], vs[i+1]
		if p1.Y != p2.Y {
			e := &edge{ymin: math.Min(p1.Y, p2.Y), ymax: math.Max(p1.Y, p2.Y), x: p2.X, islope: (p2.X - p1.X) / (p2.Y - p1.Y)}
			if e.ymin == p1.Y {
				e.x = p1.X
			}
			edges = append(edges, e)
		}
	}
	// by where they start; ties need no order, since the active edges are
	// sorted by x on every scan
	sort.SliceStable(edges, func(i, j int) bool { return edges[i].ymin < edges[j].ymin })
	if len(edges) == 0 {
		return nil
	}
	var lines [][2]Pt
	var active []*edge
	y := edges[0].ymin
	// it counts scans up to gap: a line on every gap-th
	for it := 0.0; len(active) > 0 || len(edges) > 0; it++ {
		if it == gap {
			it = 0
		}
		n := 0
		for n < len(edges) && edges[n].ymin <= y {
			n++
		}
		active, edges = append(active, edges[:n]...), edges[n:]
		kept := active[:0]
		for _, e := range active {
			if e.ymax > y {
				kept = append(kept, e)
			}
		}
		active = kept
		sort.SliceStable(active, func(i, j int) bool { return active[i].x < active[j].x })
		if step != 1 || it == 0 {
			for i := 0; i+1 < len(active); i += 2 {
				lines = append(lines, [2]Pt{rot(pt(jsRound(active[i].x), y), -a), rot(pt(jsRound(active[i+1].x), y), -a)})
			}
		}
		y += step
		for _, e := range active {
			e.x += step * e.islope
		}
	}
	return lines
}

// jsRound is JavaScript's Math.round: halves go up, -2.5 to -2.
func jsRound(x float64) float64 { return math.Floor(x + 0.5) }
