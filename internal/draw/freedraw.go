package draw

import "math"

// freehand is a freedraw element's outline, filled with its stroke color: the
// points smoothed, each widened by its pressure (or one simulated from the
// pen's speed), round caps at both ends. Excalidraw uses perfect-freehand
// with these options (size strokeWidth×4.25, thinning 0.6, streamline 0.5,
// sine easing).
// ponytail: a simplified perfect-freehand (no tapers, no corner splitting);
// the outline differs a little from excalidraw.com's, port the rest if it shows.
func freehand(e *Element) []Op {
	in := e.pts()
	if len(in) == 0 {
		return nil
	}
	size := e.StrokeWidth * 4.25
	ps := []Pt{in[0]}
	for _, p := range in[1:] {
		prev := ps[len(ps)-1]
		q := prev.add(p.sub(prev).mul(0.575))
		if q.sub(prev).len() > 0.5 {
			ps = append(ps, q)
		}
	}
	if last := in[len(in)-1]; last.sub(ps[len(ps)-1]).len() > 0.5 {
		ps = append(ps, last)
	}
	radii := make([]float64, len(ps))
	pressure := 0.5
	for i := range ps {
		switch {
		case !e.SimulatePressure && i < len(e.Pressures):
			pressure = e.Pressures[i]
		case e.SimulatePressure && i > 0:
			sp := math.Min(1, ps[i].sub(ps[i-1]).len()/size)
			rp := math.Min(1, 1-sp)
			pressure = math.Min(1, pressure+(rp-pressure)*(sp*0.275))
		}
		radii[i] = size / 2 * math.Sin((0.5-0.6*(0.5-pressure))*math.Pi/2)
	}
	if len(ps) == 1 {
		return smoothClosed(arc(ps[0], radii[0], 0, 2*math.Pi))
	}
	left, right := make([]Pt, len(ps)), make([]Pt, len(ps))
	for i, p := range ps {
		a, b := ps[max(i-1, 0)], ps[min(i+1, len(ps)-1)]
		d := b.sub(a)
		n := pt(-d.Y, d.X).mul(radii[i] / math.Max(d.len(), 1e-9))
		left[i], right[i] = p.add(n), p.sub(n)
	}
	dir := func(a, b Pt) float64 { return math.Atan2(b.Y-a.Y, b.X-a.X) }
	n := len(ps) - 1
	outline := append([]Pt{}, left...)
	outline = append(outline, arc(ps[n], radii[n], dir(ps[n], left[n]), dir(ps[n], left[n])-math.Pi)...)
	for i := n; i >= 0; i-- {
		outline = append(outline, right[i])
	}
	outline = append(outline, arc(ps[0], radii[0], dir(ps[0], right[0]), dir(ps[0], right[0])-math.Pi)...)
	return smoothClosed(outline)
}

// arc is points on a circle around c from angle a0 to a1.
func arc(c Pt, r, a0, a1 float64) []Pt {
	const steps = 8
	var ps []Pt
	for i := 0; i <= steps; i++ {
		a := a0 + (a1-a0)*float64(i)/steps
		ps = append(ps, pt(c.X+r*math.Cos(a), c.Y+r*math.Sin(a)))
	}
	return ps
}

// smoothClosed is a closed path through the midpoints of ps, each point a
// quadratic control, as Excalidraw draws freedraw outlines. ps has at least
// two points.
func smoothClosed(ps []Pt) []Op {
	mid := func(a, b Pt) Pt { return a.add(b).mul(0.5) }
	start := mid(ps[0], ps[1])
	ops := []Op{mv(start)}
	cur := start
	for i := 1; i <= len(ps); i++ {
		c, next := ps[i%len(ps)], mid(ps[i%len(ps)], ps[(i+1)%len(ps)])
		ops = append(ops, quad(cur, c, next))
		cur = next
	}
	return ops
}
