package draw

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// rjsCase is one rough.js generator call and what it drew, from
// testdata/roughjs.mjs (rough.js 4.6.6).
type rjsCase struct {
	Call             string
	Seed             int64
	Roughness, SW    float64
	Pts              [][2]float64
	Box              []float64
	Preserve, Single bool
	CurveFitting     *float64
	Fill, Path       string
	Sets             []struct {
		Type string
		Ops  [][]any
	}
}

// port is what the Go port draws for c, as sets of ops.
func (c rjsCase) port() [][]Op {
	o := newRough(c.Seed, c.Roughness, c.SW)
	o.preserve = c.Preserve
	if c.Single {
		o.multiStroke = false
	}
	if c.CurveFitting != nil {
		o.curveFitting = *c.CurveFitting
	}
	ps := make([]Pt, len(c.Pts))
	for i, p := range c.Pts {
		ps[i] = pt(p[0], p[1])
	}
	var fill []Op
	filled := func(poly []Pt, solid func() []Op) {
		switch c.Fill {
		case "":
		case "solid":
			fill = solid()
		default:
			fill = o.hachure(poly, c.Fill == "cross-hatch")
		}
	}
	var out []Op
	switch c.Call {
	case "line":
		out = o.line(ps[0], ps[1])
	case "linearPath":
		out = o.linearPath(ps, false)
	case "rectangle", "polygon":
		if c.Call == "rectangle" {
			x, y, w, h := c.Box[0], c.Box[1], c.Box[2], c.Box[3]
			ps = []Pt{pt(x, y), pt(x+w, y), pt(x+w, y+h), pt(x, y+h)}
		}
		out = o.linearPath(ps, true)
		filled(ps, func() []Op { return o.solidFill(ps) })
	case "ellipse":
		ctr := pt(c.Box[0], c.Box[1])
		inc, rx, ry := o.ellipseParams(c.Box[2], c.Box[3])
		var est []Pt
		out, est = o.ellipse(ctr, inc, rx, ry)
		filled(est, func() []Op { f, _ := o.ellipse(ctr, inc, rx, ry); return f })
	case "curve":
		out = o.smoothCurve(ps)
		filled(nil, func() []Op { return merged(o.solidCopy().smoothCurve(ps)) })
	case "path":
		segs := roundRect(200, 120, 24)
		if c.Path == "diamond" {
			segs = roundDiamond(200, 200, &Roundness{Type: RoundAdaptive})
		}
		out = o.path(segs)
		filled(nil, func() []Op { return merged(o.solidCopy().path(segs)) })
	}
	if fill != nil {
		return [][]Op{fill, out}
	}
	return [][]Op{out}
}

func TestRoughJSParity(t *testing.T) {
	b, err := os.ReadFile("testdata/roughjs.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []rjsCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 100 {
		t.Fatalf("%d cases", len(cases))
	}
	kinds := map[string]byte{"move": 'M', "lineTo": 'L', "bcurveTo": 'C'}
	sizes := map[byte]int{'M': 2, 'L': 2, 'C': 6}
	for n, c := range cases {
		name := fmt.Sprintf("#%d %s seed %d roughness %v fill %q preserve %v single %v", n, c.Call, c.Seed, c.Roughness, c.Fill, c.Preserve, c.Single)
		got := c.port()
		if len(got) != len(c.Sets) {
			t.Errorf("%s: %d sets, rough.js %d", name, len(got), len(c.Sets))
			continue
		}
		for i, set := range c.Sets {
			if len(got[i]) != len(set.Ops) {
				t.Errorf("%s set %d (%s): %d ops, rough.js %d", name, i, set.Type, len(got[i]), len(set.Ops))
				continue
			}
		ops:
			for j, want := range set.Ops {
				op := got[i][j]
				nums := want[1:]
				if op.Kind != kinds[want[0].(string)] || len(nums) != sizes[op.Kind] {
					t.Errorf("%s set %d op %d: %c, rough.js %v", name, i, j, op.Kind, want)
					break
				}
				for k, v := range nums {
					g := op.P[k/2].X
					if k%2 == 1 {
						g = op.P[k/2].Y
					}
					if math.Abs(g-v.(float64)) > 1e-7 {
						t.Errorf("%s set %d op %d: %v, rough.js %v", name, i, j, op.P, want)
						break ops
					}
				}
			}
		}
	}
}

func TestRandomIsRoughJS(t *testing.T) {
	o := newRough(1, 1, 2)
	if v := o.next(); !near(v, 48271.0/(1<<31)) {
		t.Errorf("first value %v", v)
	}
	if z := newRough(0, 1, 2); z.seed != 1 || z.seed0 != 1 {
		t.Errorf("seed 0 is kept at 0 (Math.random in rough.js): %d", z.seed)
	}
}

func TestCleanShapes(t *testing.T) {
	ops := newRough(3, 0, 2).line(pt(0, 0), pt(300, 0))
	if len(ops) != 2 || ops[1].P[0].Y != 0 || ops[1].P[1].Y != 0 || ops[1].P[2] != pt(300, 0) {
		t.Errorf("a clean line is drawn once, straight: %v", ops)
	}
	o := newRough(1, 0, 2)
	if o.linearPath([]Pt{pt(0, 0)}, false) != nil {
		t.Error("one point draws")
	}
	clean := newRough(9, 0, 2)
	clean.curveFitting = 1
	inc, rx, ry := clean.ellipseParams(100, 60)
	ell, est := clean.ellipse(pt(50, 30), inc, rx, ry)
	on := func(p Pt) bool { return near(math.Pow((p.X-50)/50, 2)+math.Pow((p.Y-30)/30, 2), 1) }
	for _, p := range est {
		if !on(p) {
			t.Fatalf("a clean ellipse's point %v is off it", p)
		}
	}
	for _, op := range ell[1:] {
		if !on(op.P[2]) {
			t.Fatalf("a clean ellipse's curve leaves it at %v", op.P[2])
		}
	}
	if len(ell) != len(est)+1 {
		t.Errorf("a clean ellipse is drawn once: %d ops for %d points", len(ell), len(est))
	}
	if o.curve([]Pt{pt(0, 0)}) != nil || o.curveWithOffset(nil, 1) != nil {
		t.Error("a curve through one point or none")
	}
	r, twin := newRough(8, 1, 2), newRough(8, 1, 2)
	if got, want := r.curve([]Pt{pt(0, 0), pt(90, 30)}), twin.oneLine(pt(0, 0), pt(90, 30), true, true); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("a two-point curve is an overlay line: %v, want %v", got, want)
	}
	if ops := o.curve([]Pt{pt(0, 0), pt(1, 1), pt(2, 0)}); len(ops) != 2 || ops[0].P[0] != pt(1, 1) || ops[1].P[2] != pt(2, 0) {
		t.Errorf("three points: %v", ops)
	}
	if ops := o.smoothCurve([]Pt{pt(0, 0), pt(10, 10), pt(20, 0)}); ops[len(ops)-1].P[2] != pt(20, 0) {
		t.Errorf("a clean smooth curve ends at %v", ops[len(ops)-1].P[2])
	}
	f := o.solidFill([]Pt{pt(0, 0), pt(100, 0), pt(100, 100), pt(0, 100)})
	if len(f) != 4 || f[0] != mv(pt(0, 0)) || f[2] != ln(pt(100, 100)) {
		t.Errorf("a clean solid fill is the polygon: %v", f)
	}
	if o.solidFill([]Pt{pt(0, 0), pt(1, 1)}) != nil {
		t.Error("two points fill")
	}
	if c := o.solidCopy(); c.roughness != 0 || c.multiStroke {
		t.Errorf("a clean fill copy: %+v", c)
	}
	if c := newRough(1, 1, 2).solidCopy(); c.roughness != 1.8 || c.multiStroke {
		t.Errorf("a rough fill copy: %+v", c)
	}
	sq := []Pt{pt(0, 0), pt(50, 0), pt(50, 50), pt(0, 50)}
	if ops, n := o.hachure(sq, false), len(hachureLines(sq, 8, 49, 1)); len(ops) != 2*n || n < 5 {
		t.Errorf("clean hachure lines are drawn once: %d ops", len(ops))
	}
}

func TestHachureLines(t *testing.T) {
	sq := []Pt{pt(0, 0), pt(100, 0), pt(100, 100), pt(0, 100)}
	for _, step := range []float64{1, 10} {
		lines := hachureLines(sq, 10, 0, step)
		if len(lines) != 10 {
			t.Fatalf("step %v: %d lines across a 100 square every 10", step, len(lines))
		}
		for i, l := range lines {
			if l[0] != pt(0, float64(10*i)) || l[1] != pt(100, float64(10*i)) {
				t.Errorf("step %v line %d: %v", step, i, l)
			}
		}
	}
	if v := hachureLines(sq, 25, 90, 1); len(v) != 4 || !near(v[1][0].X, 25) || !near(v[1][1].X, 25) || !near(math.Abs(v[1][0].Y-v[1][1].Y), 100) {
		t.Errorf("vertical hachure: %v", v)
	}
	u := []Pt{pt(0, 0), pt(30, 0), pt(30, 70), pt(70, 70), pt(70, 0), pt(100, 0), pt(100, 100), pt(0, 100), pt(0, 0)}
	if n := len(hachureLines(u, 10, 0, 1)); n != 7*2+3 {
		t.Errorf("U shape: %d lines", n)
	}
	if hachureLines(nil, 10, 0, 1) != nil || hachureLines(sq[:1], 10, 0, 1) != nil || hachureLines(sq[:2], 10, 0, 1) != nil || hachureLines([]Pt{pt(0, 0), pt(9, 0), pt(5, 0)}, 10, 0, 1) != nil {
		t.Error("hachure of a line")
	}
	if l := hachureLines([]Pt{pt(0.5, 0), pt(10.4, 0), pt(10.4, 3), pt(0.5, 3)}, 0.05, 0, 1); l[0][0].X != 1 || l[0][1].X != 10 {
		t.Errorf("x rounds as JavaScript does: %v", l[0])
	}
	if jsRound(-2.5) != -2 || jsRound(2.5) != 3 || jsRound(-2.6) != -3 {
		t.Error("jsRound")
	}
	if m := merged([]Op{mv(pt(0, 0)), ln(pt(1, 0)), mv(pt(5, 5)), ln(pt(6, 6))}); len(m) != 3 || m[2] != ln(pt(6, 6)) {
		t.Errorf("merged: %v", m)
	}
}
