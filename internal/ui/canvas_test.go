package ui

import (
	"fmt"
	"image"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// ---- geometry ----

// box is a node of n rows (all parameters) in column col, band band.
func box(id string, col, band, rows int) cvNode {
	n := cvNode{id: id, col: col, band: band}
	for range rows {
		n.rows = append(n.rows, cvRow{name: "p"})
	}
	return n
}

// TestCvLayout: empty columns take no room, a column's nodes stack with a
// gap, centered on the band's tallest column, and bands stack below.
func TestCvLayout(t *testing.T) {
	ns := []cvNode{box("a", 0, 0, 1), box("b", 3, 0, 2), box("c", 3, 0, 1), box("d", 5, 1, 0)}
	cvLayout(ns)
	h := func(rows int) int { return cvHeadH + rows*cvRowH + cvPadB }
	step := cvNodeW + cvColGap
	if ns[0].r.Min.X != 0 || ns[1].r.Min.X != step || ns[3].r.Min.X != 2*step || ns[0].r.Dx() != cvNodeW {
		t.Fatalf("columns: %v %v %v", ns[0].r, ns[1].r, ns[3].r)
	}
	tall := h(2) + cvNodeGap + h(1)
	if ns[1].r.Min.Y != 0 || ns[2].r.Min.Y != h(2)+cvNodeGap || ns[2].r.Dy() != h(1) {
		t.Fatalf("stack: %v %v", ns[1].r, ns[2].r)
	}
	if ns[0].r.Min.Y != (tall-h(1))/2 {
		t.Fatalf("centered: %v (band %d tall)", ns[0].r, tall)
	}
	if ns[3].r.Min.Y != tall+cvBandGap {
		t.Fatalf("band 1 at %d, want %d", ns[3].r.Min.Y, tall+cvBandGap)
	}
}

// TestCvBoundsFrames: bounds take in frames and status lines; frames group
// their nodes, padded, in the order they come.
func TestCvBoundsFrames(t *testing.T) {
	a, b, c := box("a", 0, 0, 1), box("b", 1, 0, 1), box("c", 2, 0, 1)
	b.frame, c.frame = "S", "S"
	a.who = &cvWho{}
	ns := []cvNode{a, b, c}
	cvLayout(ns)
	names, rs := cvFrames(ns)
	want := image.Rect(ns[1].r.Min.X-cvFrameX, ns[1].r.Min.Y-cvFrameT, ns[2].r.Max.X+cvFrameX, ns[2].r.Max.Y+cvFrameB)
	if !slices.Equal(names, []string{"S"}) || rs[0] != want {
		t.Fatalf("frames %v %v, want %v", names, rs, want)
	}
	if bd := cvBounds(ns); bd.Min.X != 0 || bd.Min.Y != want.Min.Y || bd.Max != want.Max {
		t.Fatalf("bounds %v", bd)
	}
	solo := []cvNode{a}
	cvLayout(solo)
	if bd := cvBounds(solo); bd.Min.Y != solo[0].r.Min.Y-20 || bd.Max != solo[0].r.Max {
		t.Fatalf("a Claude's status line: %v", bd)
	}
	framed := []cvNode{b, c}
	cvLayout(framed)
	if _, rs := cvFrames(framed); cvBounds(framed) != rs[0] {
		t.Fatalf("framed only: %v, frame %v", cvBounds(framed), rs[0])
	}
	if cvBounds(nil) != (image.Rectangle{}) {
		t.Fatal("no nodes, no bounds")
	}
}

// TestCvSockets: inputs on the left edge, outputs on the right, each on its
// row's middle; a row without that socket has none.
func TestCvSockets(t *testing.T) {
	n := cvNode{r: image.Rect(10, 20, 10+cvNodeW, 200), rows: []cvRow{{name: "Run", in: true}, {name: "p"}, {name: "Out", out: true, sock: sockDiff}}}
	p, typ, ok := n.sock("Out", true)
	if !ok || typ != sockDiff || p != f32.Pt(float32(10+cvNodeW), float32(20+cvHeadH+2+2*cvRowH+cvRowH/2)) {
		t.Fatalf("out at %v %v %v", p, typ, ok)
	}
	if p, _, ok := n.sock("Run", false); !ok || p.X != 10 || p.Y != n.rowY(0) {
		t.Fatalf("in at %v", p)
	}
	for _, c := range []struct {
		name string
		out  bool
	}{{"Run", true}, {"Out", false}, {"p", false}, {"p", true}, {"x", false}} {
		if _, _, ok := n.sock(c.name, c.out); ok {
			t.Errorf("%s out=%v: a socket", c.name, c.out)
		}
	}
}

// hits is whether the curve passes inside r.
func hits(segs []cvSeg, r image.Rectangle) bool {
	for _, s := range segs {
		for i := 0; i <= 50; i++ {
			if p := s.at(float32(i) / 50).Round(); p.In(r) {
				return true
			}
		}
	}
	return false
}

// TestCvRoute: a wire between neighbors is one curve; one past a node in
// between takes a lane over it (or under, when nearer) and misses it; a
// missing node or socket draws nothing.
func TestCvRoute(t *testing.T) {
	src := cvNode{id: "s", rows: []cvRow{{name: "o", out: true, sock: sockVerdict}}}
	mid := box("m", 1, 0, 4)
	dst := cvNode{id: "d", col: 2, rows: []cvRow{{name: "i", in: true}}}
	g := cvGraph{nodes: []cvNode{src, mid, dst}, wires: []cvWire{{"s", "o", "d", "i"}}}
	cvLayout(g.nodes)
	segs, typ, ok := cvRoute(&g, g.wires[0])
	if !ok || typ != sockVerdict || len(segs) != 3 {
		t.Fatalf("past a node: %d segments, %v %v", len(segs), typ, ok)
	}
	m := g.nodes[1].r
	if hits(segs, m) || segs[1][0].Y != float32(m.Max.Y+20) { // the wire is low on it: under
		t.Fatalf("under %v: lane at %v", m, segs[1][0].Y)
	}
	g.nodes[1].r = m.Add(image.Pt(0, 40)) // the wire is high on it: over
	segs, _, _ = cvRoute(&g, g.wires[0])
	if hits(segs, g.nodes[1].r) || segs[1][0].Y != float32(m.Min.Y+40-28) {
		t.Fatalf("over %v: lane at %v", g.nodes[1].r, segs[1][0].Y)
	}
	m = g.nodes[1].r
	// the lane passes only what is between: not a node beyond either end
	far := cvNode{id: "far", r: image.Rect(m.Min.X+3*(cvNodeW+cvColGap), -500, m.Max.X+3*(cvNodeW+cvColGap), -400)}
	before := cvNode{id: "before", r: image.Rect(-2*cvNodeW, -500, -cvNodeW, -400)}
	g.nodes = append(g.nodes, far, before)
	if segs, _, _ := cvRoute(&g, g.wires[0]); segs[1][0].Y != float32(m.Min.Y-28) {
		t.Fatalf("lane at %v, want %v", segs[1][0].Y, m.Min.Y-28)
	}
	g.nodes = g.nodes[:3]
	a, _, _ := g.nodes[0].sock("o", true)
	b, _, _ := g.nodes[2].sock("i", false)
	if segs[0][0] != a || segs[2][3] != b {
		t.Fatalf("ends %v %v, want %v %v", segs[0][0], segs[2][3], a, b)
	}
	if !hits(cvCurve(a, b), m) {
		t.Fatal("the straight curve should cross the middle node")
	}
	g.nodes = []cvNode{g.nodes[0], g.nodes[2]} // neighbors now: one curve
	if segs, _, ok := cvRoute(&g, g.wires[0]); !ok || len(segs) != 1 || segs[0][0] != a || segs[0][3] != b {
		t.Fatalf("neighbors: %v %v", segs, ok)
	}
	for _, w := range []cvWire{{"x", "o", "d", "i"}, {"s", "o", "x", "i"}, {"s", "x", "d", "i"}, {"s", "o", "d", "x"}} {
		if _, _, ok := cvRoute(&g, w); ok {
			t.Errorf("%v routed", w)
		}
	}
}

// TestCvCurve: level at both ends, at least 40 units out, and its ends.
func TestCvCurve(t *testing.T) {
	a, b := f32.Pt(100, 0), f32.Pt(300, 100)
	s := cvCurve(a, b)[0]
	if s[1] != f32.Pt(200, 0) || s[2] != f32.Pt(200, 100) || s.at(0) != a || s.at(1) != b {
		t.Fatalf("curve %v", s)
	}
	if s := cvCurve(a, f32.Pt(120, 50))[0]; s[1].X != 140 || s[2].X != 80 {
		t.Fatalf("short: %v", s)
	}
	if p := s.at(.5); p != f32.Pt(200, 50) {
		t.Fatalf("middle %v", p)
	}
	l := cvLaned(a, b, -50)
	g := float32(cvColGap) / 2
	want := []cvSeg{
		{a, f32.Pt(100+g/2, 0), f32.Pt(100+g/2, -50), f32.Pt(100+g, -50)},
		{f32.Pt(100+g, -50), f32.Pt(100+g, -50), f32.Pt(300-g, -50), f32.Pt(300-g, -50)},
		{f32.Pt(300-g, -50), f32.Pt(300-g/2, -50), f32.Pt(300-g/2, 100), b},
	}
	if !slices.Equal(l, want) {
		t.Fatalf("laned\n%v\nwant\n%v", l, want)
	}
}

// TestCvHits: a curve beside a node, on any side, misses it; through it,
// hits; the nodes skipped never count.
func TestCvHits(t *testing.T) {
	ns := []cvNode{{id: "n", r: image.Rect(100, 100, 200, 200)}}
	line := func(a, b f32.Point) []cvSeg { return []cvSeg{{a, a, b, b}} }
	for name, c := range map[string]struct {
		segs []cvSeg
		want bool
	}{
		"left":    {line(f32.Pt(0, 150), f32.Pt(90, 150)), false},
		"right":   {line(f32.Pt(210, 150), f32.Pt(400, 150)), false},
		"above":   {line(f32.Pt(150, 0), f32.Pt(150, 90)), false},
		"below":   {line(f32.Pt(150, 210), f32.Pt(150, 400)), false},
		"through": {line(f32.Pt(0, 150), f32.Pt(400, 150)), true},
		"grazing": {line(f32.Pt(0, 97), f32.Pt(400, 97)), true}, // within its 6-unit margin
	} {
		if got := cvHits(c.segs, ns); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
	if cvHits(line(f32.Pt(0, 150), f32.Pt(400, 150)), ns, "x", "n") {
		t.Error("a skipped node hit")
	}
}

// TestCvHit: the node under a point, the last drawn first; edges in, far
// edges out.
func TestCvHit(t *testing.T) {
	ns := []cvNode{{id: "a", r: image.Rect(0, 0, 100, 100)}, {id: "b", r: image.Rect(50, 50, 150, 150)}}
	for p, want := range map[[2]float32]string{{10, 10}: "a", {60, 60}: "b", {149, 149}: "b", {150, 60}: "", {0, 0}: "a", {-1, 5}: "", {5, 100}: "", {200, 0}: "", {5, -1}: ""} {
		if got := cvHit(ns, f32.Pt(p[0], p[1])); got != want {
			t.Errorf("at %v: %q, want %q", p, got, want)
		}
	}
}

// TestCvZoom: zooming keeps the point under the pointer still, stays in
// its limits, and stops the fit; the wheel zooms out going down.
func TestCvZoom(t *testing.T) {
	v := &cvView{pan: f32.Pt(100, 50), zoom: 1, auto: true}
	p := f32.Pt(300, 200)
	before := v.toCanvas(p, 2)
	if before != f32.Pt(100, 75) {
		t.Fatalf("toCanvas %v", before)
	}
	v.zoomAt(p, 1.5)
	if v.zoom != 1.5 || v.auto {
		t.Fatalf("zoom %v auto %v", v.zoom, v.auto)
	}
	if after := v.toCanvas(p, 2); math.Abs(float64(after.X-before.X)) > 1e-3 || math.Abs(float64(after.Y-before.Y)) > 1e-3 {
		t.Fatalf("moved under the pointer: %v → %v", before, after)
	}
	v.zoomAt(p, 10)
	if v.zoom != cvZoomMax {
		t.Fatalf("over the max: %v", v.zoom)
	}
	v.zoomAt(p, .01)
	if v.zoom != cvZoomMin {
		t.Fatalf("under the min: %v", v.zoom)
	}
	if wheelZoom(120) >= 1 || wheelZoom(-120) <= 1 || wheelZoom(0) != 1 || math.Abs(float64(wheelZoom(240))-math.Exp(-1)) > 1e-6 {
		t.Fatalf("wheel %v %v", wheelZoom(120), wheelZoom(-120))
	}
}

// TestCvFit: the bounds fill the pane above the controls, centered, never
// past 1:1, never under the min zoom.
func TestCvFit(t *testing.T) {
	v := &cvView{}
	b := image.Rect(100, 100, 1100, 600)
	v.fit(b, image.Pt(540, 1000), 1) // 1000 wide in 500: half
	if v.zoom != .5 || !v.auto || v.fitted != b || v.size != image.Pt(540, 1000) {
		t.Fatalf("fit %+v", v)
	}
	mid := v.toCanvas(f32.Pt(270, 20+(1000-20-48)/2), 1)
	if mid != f32.Pt(600, 350) {
		t.Fatalf("not centered: %v", mid)
	}
	v.fit(b, image.Pt(4000, 4000), 2) // room to spare: 1:1
	if v.zoom != 1 {
		t.Fatalf("past 1:1: %v", v.zoom)
	}
	v.fit(b, image.Pt(1040, 20+48+125), 1) // 500 tall in 125: a quarter, held at the min
	if v.zoom != cvZoomMin {
		t.Fatalf("under the min: %v", v.zoom)
	}
}

// TestWireWidth: control wires thicker than data ones, hover thickens both.
func TestWireWidth(t *testing.T) {
	for _, c := range []struct {
		t    cvSock
		lit  bool
		want float32
	}{{sockControl, false, 3.4}, {sockDiff, false, 2.2}, {sockControl, true, 4.6}, {sockText, true, 3.4}} {
		if got := wireWidth(c.t, c.lit); math.Abs(float64(got-c.want)) > 1e-5 {
			t.Errorf("wireWidth(%v, %v) = %v, want %v", c.t, c.lit, got, c.want)
		}
	}
}

// TestGridLines: the first line at or past 0, a line every step, every
// fifth from the origin stronger, whichever side of 0 the origin is.
func TestGridLines(t *testing.T) {
	for _, c := range []struct {
		o    float32
		n    int
		want []gridLine
	}{
		{40, 90, []gridLine{{16, false}, {40, true}, {64, false}, {88, false}}},
		{-30, 70, []gridLine{{18, false}, {42, false}, {66, false}}}, // index 2 first
		{-48, 30, []gridLine{{0, false}, {24, false}}},               // exactly on 0: index 2
		{-120, 30, []gridLine{{0, true}, {24, false}}},               // index 5
		{100, 30, []gridLine{{4, false}, {28, false}}},               // indexes -4 and -3
		{120, 30, []gridLine{{0, true}, {24, false}}},                // index -5
	} {
		if got := gridLines(c.o, 24, c.n); !slices.Equal(got, c.want) {
			t.Errorf("gridLines(%v, 24, %d) = %v, want %v", c.o, c.n, got, c.want)
		}
	}
	if got := gridLines(-1000, 24, 0); len(got) != 0 {
		t.Errorf("no room, lines %v", got)
	}
}

// TestInitials: a name's two letters.
func TestInitials(t *testing.T) {
	for in, want := range map[string]string{"planner": "PL", "test-writer": "TW", "Explore": "EX", "a": "A", "": "?", "x_y z": "XY", "web.app": "WA", "--": "?"} {
		if got := initials(in); got != want {
			t.Errorf("initials(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- the graph ----

// row is node n's row named name, and whether there is one.
func (n *cvNode) row(name string) (cvRow, bool) {
	for _, r := range n.rows {
		if r.name == name {
			return r, true
		}
	}
	return cvRow{}, false
}

// canvasCard is teamCard with a ticket, a pull request whose CI failed, a
// commit with no CI known, a plain link, an artifact and a workspace.
func canvasCard(h *harness) *kanban.Card {
	c := teamCard(h)
	pr := kanban.Link{URL: "https://github.com/acme-org/api/pull/7", Kind: links.KindGitHubPR, Ref: "PR #7", State: "open · CI failed", Tone: links.ToneBad}
	c.Links = []kanban.Link{
		{URL: "https://acme.atlassian.net/browse/ABC-123", Kind: links.KindJira, Ref: "ABC-123", State: "In Progress", Tone: links.ToneWait},
		pr,
		{URL: "https://github.com/acme-org/api/commit/abc", Kind: links.KindGitHubCommit, Ref: "abc"},
		{URL: "https://docs.example.com/x", Kind: links.KindURL},
	}
	c.Artifacts, c.WSSession, c.Dir, c.Labels = []string{"https://claude.ai/artifact/12345678abcd"}, "ws1", "api", []string{"api", "web"}
	c.Session = kanban.Session{Turns: 3, Tokens: 96000, Cost: 1.27, Context: 50000, Window: 200000}
	h.u.ci["Acme"] = []ciItem{{id: c.ID, link: pr, runs: []links.Run{
		{Name: "Docs", Status: links.CISuccess, URL: "https://ci.example.com/2"},
		{Name: "CI", Status: links.CIFailed, URL: "https://ci.example.com/1", Jobs: []links.Job{{Name: "lint", Status: links.CISuccess}, {Name: "e2e", Status: links.CIFailed}}},
	}}}
	return c
}

// TestCardGraph: a card's graph is what desk knows of it: the card, its
// ticket and workspace feed the session; the session runs its agents (one
// per kind) and shells; its PR goes to CI and both to Done.
func TestCardGraph(t *testing.T) {
	h := newHarness(t)
	c := canvasCard(h)
	h.u.findTask(c.ID, "a1", "").Summary = "so far" // running: no result yet
	g := h.u.cardGraph(c, time.Now())
	var ids []string
	for _, n := range g.nodes {
		ids = append(ids, n.id)
	}
	pr, commit := "link:https://github.com/acme-org/api/pull/7", "link:https://github.com/acme-org/api/commit/abc"
	want := []string{"card", "main", "link:https://acme.atlassian.net/browse/ABC-123", "ws", "agent:api", "agent:web", "shell:s1",
		pr, "ci:https://github.com/acme-org/api/pull/7", commit, "link:https://docs.example.com/x", "link:https://claude.ai/artifact/12345678abcd", "done"}
	if !slices.Equal(ids, want) {
		t.Fatalf("nodes\n%q\nwant\n%q", ids, want)
	}
	cols := map[string]int{"card": 0, "main": 1, "ws": 0, "agent:api": 2, "shell:s1": 3, pr: 2, "ci:" + pr[5:]: 3, "done": 4}
	for id, col := range cols {
		if n := g.node(id); n.col != col {
			t.Errorf("%s in column %d, want %d", id, n.col, col)
		}
	}
	if g.node(pr).band != 1 || g.node("main").band != 0 || g.node(pr).frame != "Delivery" || g.node("agent:api").frame != "Session" || g.node("card").frame != "" {
		t.Error("bands or frames")
	}
	for _, w := range []cvWire{
		{"card", "Start", "main", "Run"}, {"card", "Context", "main", "Context"}, {"card", "Repo", "main", "Repo"},
		{"link:https://acme.atlassian.net/browse/ABC-123", "Ticket", "main", "Ticket"}, {"ws", "Answers", "main", "Workspace"},
		{"main", "Agents", "agent:api", "Run"}, {"main", "Agents", "agent:web", "Run"}, {"agent:api", "Shells", "shell:s1", "Run"},
		{"main", "Diff", pr, "Diff"}, {pr, "CI", "ci:" + pr[5:], "Run"}, {"ci:" + pr[5:], "Verdict", "done", "Verdict"},
		{"main", "Diff", commit, "Diff"}, {commit, "Link", "done", "PR"}, {"main", "Links", "link:https://docs.example.com/x", "Link"},
		{"main", "Links", "link:https://claude.ai/artifact/12345678abcd", "Link"}, {"main", "Finish", "done", "Finish"},
	} {
		if !slices.Contains(g.wires, w) {
			t.Errorf("no wire %v", w)
		}
		if _, _, ok := cvRoute(&g, w); !ok {
			t.Errorf("wire %v has no sockets", w)
		}
	}
	if len(g.wires) != 16 {
		t.Errorf("%d wires", len(g.wires))
	}
	card := g.node("card")
	if r, _ := card.row("folder"); r.value != "Acme/api" || !r.mono {
		t.Errorf("folder %+v", r)
	}
	if r, _ := card.row("labels"); r.value != "api, web" || card.open != "edit" || card.title != "Card · "+c.Key() {
		t.Errorf("labels %+v, %q", r, card.title)
	}
	m := g.node("main")
	if m.title != "Claude · planner" || m.open != "convo" || m.who.initials != "PL" || m.who.busy {
		t.Errorf("main %q %q %+v", m.title, m.open, m.who)
	}
	if m.who.status != "Waiting on api" { // waiting on the user, but its agent works
		t.Errorf("main's status %q", m.who.status)
	}
	if r, _ := m.row("usage"); r.value != "3 turns · 96k tokens · $1.27" {
		t.Errorf("usage %q", r.value)
	}
	if r, _ := m.row("context"); r.value != "25%" {
		t.Errorf("context %q", r.value)
	}
	if r, _ := m.row("status"); r.value != "Your turn" || r.tone != statusColor[kanban.StatusWaiting] {
		t.Errorf("status %+v", r)
	}
	api, web := g.node("agent:api"), g.node("agent:web")
	if api.open != "sub:a1" || api.dim || !api.who.busy || api.who.initials != "AP" || api.badge != "Sonnet 5.5" || api.who.col != pastels[1] {
		t.Errorf("api %+v %+v", api, api.who)
	}
	if r, _ := api.row("brief"); r.value != "Parse the new statement format; PUT upserts only." {
		t.Errorf("brief %q", r.value)
	}
	if r, _ := api.row("state"); r.value != "Working" || api.who.status != "Working" {
		t.Errorf("api state %q, status %q", r.value, api.who.status)
	}
	if r, ok := api.row("result"); ok {
		t.Errorf("a running agent's result %+v", r)
	}
	if r, _ := web.row("result"); r.value != "Upload screen done, es and en copy." || !web.dim || web.who.busy || web.who.status != "Upload screen done, es and en copy." {
		t.Errorf("web %+v %+v", r, web.who)
	}
	if r, _ := web.row("state"); !strings.HasPrefix(r.value, "Done · ") || r.tone != kit.CGreen {
		t.Errorf("web state %+v", r)
	}
	sh := g.node("shell:s1")
	if r, _ := sh.row("$"); r.value != "go test ./..." || !r.mono || sh.open != "sub:s1" || sh.dim || sh.fam != famCheck {
		t.Errorf("shell %+v", sh)
	}
	ci := g.node("ci:" + pr[5:])
	if ci.title != "CI failed" || ci.open != "url:https://ci.example.com/1" || ci.tone != kit.CRed {
		t.Errorf("ci %q %q", ci.title, ci.open)
	}
	if r, _ := ci.row("e2e"); r.value != links.CIFailed || r.tone != kit.CRed {
		t.Errorf("e2e %+v", r)
	}
	if r, _ := ci.row("Docs"); r.tone != kit.CGreen {
		t.Errorf("a run without jobs: %+v", r)
	}
	if r, _ := g.node(pr).row("state"); r.value != "open · CI failed" || r.tone != kit.CRed || g.node(pr).title != "PR · PR #7" {
		t.Errorf("pr %+v", r)
	}
	if g.node(commit).title != "Commit · abc" || g.node("link:https://docs.example.com/x").title != "Link · docs.example.com" {
		t.Error("link titles")
	}
	if r, _ := g.node("link:https://docs.example.com/x").row("url"); !r.mono || r.value != "https://docs.example.com/x" {
		t.Errorf("url %+v", r)
	}
	if w := g.node("ws"); w.badge != "read-only" || w.open != "ws" || w.who.status != "Idle" || w.who.busy {
		t.Errorf("ws %+v", w.who)
	}
	done := g.node("done")
	if r, _ := done.row("column"); r.value != "In progress" || !done.dim || done.open != "" {
		t.Errorf("done %+v", r)
	}
}

// TestCardGraphPlain: a card with nothing but itself: no folder, no repo,
// no agents; Done when in Done; archived; a session working on a tool.
func TestCardGraphPlain(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Card(h.u.b.Add(h.u.b.Column("Done"), kanban.Card{Title: "Tidy", Status: kanban.StatusWorking, Note: "Reading"}).ID)
	g := h.u.cardGraph(c, time.Now())
	if len(g.nodes) != 3 || len(g.wires) != 3 {
		t.Fatalf("%d nodes, %d wires", len(g.nodes), len(g.wires))
	}
	if _, ok := g.node("card").row("Repo"); ok {
		t.Error("a repo without a folder")
	}
	if _, ok := g.node("card").row("folder"); ok {
		t.Error("a folder without an org")
	}
	m := g.node("main")
	if m.title != "Claude · session" || m.who.initials != "CL" || !m.who.busy || m.who.status != "Reading" {
		t.Errorf("main %q %+v", m.title, m.who)
	}
	for _, r := range []string{"Ticket", "Workspace", "Agents", "Shells", "Diff", "Links"} {
		if _, ok := m.row(r); ok {
			t.Errorf("a %s socket with none", r)
		}
	}
	for _, r := range []string{"Verdict", "PR"} {
		if _, ok := g.node("done").row(r); ok {
			t.Errorf("Done takes a %s with none", r)
		}
	}
	h.u.ws[c.ID] = &wsState{} // opened, its Claude never asked
	if h.u.cardGraph(c, time.Now()).node("ws") != nil {
		t.Error("a workspace Claude that never ran")
	}
	h.u.ws[c.ID] = &wsState{proc: &fakeClaude{}}
	if w := h.u.cardGraph(c, time.Now()).node("ws"); w == nil || w.who.busy || w.who.status != "Idle" {
		t.Errorf("a workspace Claude running, idle: %+v", w)
	}
	h.u.ws[c.ID].from = []string{"you"}
	if w := h.u.cardGraph(c, time.Now()).node("ws"); !w.who.busy || w.who.status != "Answering" {
		t.Errorf("a workspace Claude answering: %+v", w.who)
	}
	delete(h.u.ws, c.ID)
	if _, ok := m.row("usage"); ok {
		t.Error("usage before a turn")
	}
	if _, ok := m.row("context"); ok {
		t.Error("context without a window")
	}
	d := g.node("done")
	if r, _ := d.row("column"); r.value != "Done" || d.dim || d.tone != kit.CGreen {
		t.Errorf("done %+v", r)
	}
	h.u.say(c.ID, "tool", "Bash  go   test")
	if s := h.u.cardGraph(c, time.Now()).node("main").who.status; s != "Bash go test" {
		t.Errorf("on a tool: %q", s)
	}
	c.Status, c.Note = kanban.StatusBlocked, "no access"
	if s := h.u.cardGraph(c, time.Now()).node("main").who.status; s != "Blocked: no access" {
		t.Errorf("blocked: %q", s)
	}
	c.Note = ""
	if s := h.u.cardGraph(c, time.Now()).node("main").who.status; s != "Blocked" {
		t.Errorf("blocked, no note: %q", s)
	}
	c.Status = kanban.StatusWorking
	h.u.say(c.ID, "claude", "ok")
	if s := h.u.cardGraph(c, time.Now()).node("main").who.status; s != "Working" {
		t.Errorf("working: %q", s)
	}
	off := kanban.Card{ID: "gone", Title: "x"}
	if r, _ := h.u.cardGraph(&off, time.Now()).node("done").row("column"); r.value != "Archived" {
		t.Errorf("archived: %q", r.value)
	}
}

// TestCardGraphCI: a CI's jobs, past five the first four and how many
// more; a URL without a host reads as itself.
func TestCardGraphCI(t *testing.T) {
	h := newHarness(t)
	pr := kanban.Link{URL: "https://github.com/acme-org/api/pull/9", Kind: links.KindGitHubPR}
	c := h.u.b.Card(h.u.b.Add(1, kanban.Card{Title: "x", Org: "Acme", Links: []kanban.Link{pr}}).ID)
	ci := func(n int) *cvNode {
		var jobs []links.Job
		for i := range n {
			jobs = append(jobs, links.Job{Name: fmt.Sprint("j", i), Status: links.CIRunning})
		}
		h.u.ci["Acme"] = []ciItem{{id: c.ID, link: pr, runs: []links.Run{{Name: "CI", Status: links.CIRunning, Jobs: jobs}}}}
		return h.u.cardGraph(c, time.Now()).node("ci:" + pr.URL)
	}
	n := ci(5)
	if _, ok := n.row("j4"); !ok || n.title != "CI running" || n.tone != kit.CText2 {
		t.Fatalf("five jobs: %+v", n.rows)
	}
	if r, _ := n.row("j0"); r.tone != kit.CText2 {
		t.Errorf("running job %+v", r)
	}
	n = ci(6)
	if _, ok := n.row("j4"); ok {
		t.Fatal("six jobs, all shown")
	}
	if _, ok := n.row("and 2 more"); !ok {
		t.Fatalf("six jobs: %+v", n.rows)
	}
	if r, _ := h.u.cardGraph(c, time.Now()).node("link:" + pr.URL).row("state"); r.value != "unknown" {
		t.Errorf("a PR never checked: %q", r.value)
	}
	for in, want := range map[string]string{"https://a.example.com/x": "a.example.com", "notes": "notes", "%zz": "%zz"} {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q", in, got)
		}
	}
}

// TestCardGraphAgents: a held agent waits, a failed one says so, one run
// again shows ×N, the session's own shell hangs off it, a done shell is
// dimmed, and the long-done leave until the earlier chip brings them.
func TestCardGraphAgents(t *testing.T) {
	h := newHarness(t)
	c := teamCard(h)
	h.u.onTask(c.ID, &claude.Task{ID: "a3", ToolUse: "tu4", Name: "Parse again", Agent: "api", Status: "completed"})
	h.u.onTask(c.ID, &claude.Task{ID: "a4", ToolUse: "tu5", Name: "Migrate", Agent: "db", Status: "failed"})
	h.u.onTask(c.ID, &claude.Task{ID: "s2", ToolUse: "tu6", Shell: true, Status: "completed"}) // the session's own
	h.u.findTask(c.ID, "a1", "").pending["x"] = pend{line: "TaskOutput  s1", since: time.Now()}
	now := time.Now()
	g := h.u.cardGraph(c, now)
	api, db := g.node("agent:api"), g.node("agent:db")
	if api.badge != "Sonnet 5.5 · ×2" || api.who.busy || !strings.HasPrefix(api.who.status, "Waiting on ") {
		t.Errorf("api %q %+v", api.badge, api.who)
	}
	if r, _ := api.row("state"); r.value != "Waiting" || r.tone != kit.COrange {
		t.Errorf("held %+v", r)
	}
	if r, _ := db.row("state"); r.value != "failed" || r.tone != kit.CRed || db.badge != "" {
		t.Errorf("failed %+v %q", r, db.badge)
	}
	s2 := g.node("shell:s2")
	if s2 == nil || s2.col != 2 || !s2.dim || !slices.Contains(g.wires, cvWire{"main", "Shells", "shell:s2", "Run"}) {
		t.Fatalf("the session's shell %+v", s2)
	}
	if r, _ := s2.row("state"); !strings.HasPrefix(r.value, "completed · ") || r.tone != kit.CGreen {
		t.Errorf("done shell %+v", r)
	}
	h.u.findTask(c.ID, "a2", "").end = now.Add(-settled - time.Minute) // web, long done
	h.u.findTask(c.ID, "s2", "").end = now.Add(-settled - time.Minute)
	g = h.u.cardGraph(c, now)
	if g.node("agent:web") != nil || g.node("shell:s2") != nil || g.earlier != 1 {
		t.Fatalf("long done shown, earlier %d", g.earlier)
	}
	h.u.earlier[c.ID] = true
	g = h.u.cardGraph(c, now)
	if g.node("agent:web") == nil || g.node("shell:s2") == nil || g.earlier != 1 {
		t.Fatal("the chip didn't bring them back")
	}
	for _, id := range []string{"a1", "a3"} { // its shell's agent long done, the shell just: the shell hangs off the session
		h.u.findTask(c.ID, id, "").Status, h.u.findTask(c.ID, id, "").end = "completed", now.Add(-settled-time.Minute)
	}
	h.u.findTask(c.ID, "s1", "").Status, h.u.findTask(c.ID, "s1", "").end = "completed", now
	delete(h.u.earlier, c.ID)
	g = h.u.cardGraph(c, now)
	if !slices.Contains(g.wires, cvWire{"main", "Shells", "shell:s1", "Run"}) {
		t.Fatalf("orphan shell: %v", g.wires)
	}
}

// ---- the view ----

// cvAt is where node n of card id's canvas is in the window, at its middle:
// the canvas knows its px, and where the pointer was in them.
func (h *harness) cvAt(id, n string) f32.Point {
	h.t.Helper()
	probe := f32.Pt(float32(h.size.X)-300, float32(h.size.Y)/2)
	h.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: probe})
	v := h.u.cv[id]
	off := probe.Sub(v.ptr)
	for _, x := range v.nodes {
		if x.id == n {
			c := f32.Pt(float32(x.r.Min.X+x.r.Max.X)/2, float32(x.r.Min.Y+x.r.Max.Y)/2)
			return off.Add(v.pan).Add(c.Mul(v.zoom))
		}
	}
	h.t.Fatalf("no node %s", n)
	return f32.Point{}
}

func (h *harness) clickAt(p f32.Point) {
	h.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: p},
		pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
}

// TestCanvasView: an agent card's full window shows its canvas; a click on
// an agent opens its conversation, back returns to the canvas, one on the
// session opens its conversation, and the switch goes both ways.
func TestCanvasView(t *testing.T) {
	h := newHarness(t)
	c := canvasCard(h)
	h.u.openSession(c.ID)
	h.frame()
	if !h.u.canvasMode(c, true) || h.u.canvasMode(c, false) {
		t.Fatal("not in canvas mode")
	}
	h.clickAt(h.cvAt(c.ID, "agent:api"))
	if h.u.sub != "a1" {
		t.Fatalf("clicked api, sub %q", h.u.sub)
	}
	h.clickButton("subBack")
	if h.u.sub != "" || !h.u.canvasMode(c, true) {
		t.Fatalf("back: sub %q", h.u.sub)
	}
	h.clickAt(h.cvAt(c.ID, "shell:s1"))
	if h.u.sub != "s1" {
		t.Fatalf("clicked the shell, sub %q", h.u.sub)
	}
	h.u.sub = ""
	h.frame()
	h.clickButton("pnMode1")
	if on, ok := h.u.canvas[c.ID]; !ok || on || h.u.canvasMode(c, true) {
		t.Fatal("Conversation didn't switch")
	}
	h.clickButton("pnMode0")
	if !h.u.canvas[c.ID] {
		t.Fatal("Canvas didn't switch back")
	}
	h.clickAt(h.cvAt(c.ID, "main"))
	if h.u.canvasOn(c) {
		t.Fatal("the session didn't open its conversation")
	}
	plain := h.u.b.Card(h.u.b.Add(1, kanban.Card{Title: "Plain", Org: "Acme"}).ID)
	if h.u.canvasOn(plain) || h.u.started(plain) {
		t.Fatal("a card without an agent opens on its conversation")
	}
	h.u.say(plain.ID, "you", "go")
	h.u.canvas[plain.ID] = true
	if !h.u.canvasMode(plain, true) {
		t.Fatal("any card's canvas, once picked")
	}
}

// TestCanvasOpens: a link node opens its URL, CI its failed run, the
// workspace's its view, the card's its editor; Done nothing; a press that
// moves off its node doesn't click.
func TestCanvasOpens(t *testing.T) {
	h := newHarness(t)
	var opened []string
	was := openURL
	openURL = func(s string) { opened = append(opened, s) }
	t.Cleanup(func() { openURL = was })
	c := canvasCard(h)
	h.u.openSession(c.ID)
	h.frame()
	pr := "https://github.com/acme-org/api/pull/7"
	h.clickAt(h.cvAt(c.ID, "link:"+pr))
	h.clickAt(h.cvAt(c.ID, "ci:"+pr))
	h.clickAt(h.cvAt(c.ID, "done"))
	if !slices.Equal(opened, []string{pr, "https://ci.example.com/1"}) {
		t.Fatalf("opened %q", opened)
	}
	a, b := h.cvAt(c.ID, "link:"+pr), h.cvAt(c.ID, "ci:"+pr)
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: a},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: b})
	if len(opened) != 2 {
		t.Fatalf("a press on one node, released on another, opened %q", opened)
	}
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonSecondary, Position: a},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: a})
	if len(opened) != 2 {
		t.Fatalf("a right click opened %q", opened)
	}
	h.clickAt(h.cvAt(c.ID, "card"))
	if h.u.edit == nil || h.u.edit.id != c.ID {
		t.Fatal("the card's node didn't open its editor")
	}
	h.u.edit = nil
	h.frame()
	h.clickAt(h.cvAt(c.ID, "ws"))
	if h.u.view != "ws:"+c.ID {
		t.Fatalf("the workspace's node: view %q", h.u.view)
	}
}

// TestCanvasPanZoom: a drag on the canvas pans it (and clicks nothing),
// the wheel pans, Ctrl+wheel zooms around the pointer, and Fit fits again.
func TestCanvasPanZoom(t *testing.T) {
	h := newHarness(t)
	c := canvasCard(h)
	h.u.openSession(c.ID)
	h.frame()
	v := h.u.cv[c.ID]
	if !v.auto || v.zoom <= cvZoomMin || v.zoom > 1 {
		t.Fatalf("not fitted on open: %+v", v)
	}
	fitted := v.pan
	a := h.cvAt(c.ID, "agent:api")
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: a},
		pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: a.Add(f32.Pt(2, 0))})
	if v.panning || v.pan != fitted {
		t.Fatal("a 2px move panned")
	}
	h.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: a.Add(f32.Pt(60, 30))},
		pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: a.Add(f32.Pt(80, 40))})
	if !v.panning || v.pan != fitted.Add(f32.Pt(78, 40)) || v.auto || v.hover != "" {
		t.Fatalf("drag: pan %v (from %v), panning %v", v.pan, fitted, v.panning)
	}
	if h.r.Cursor() != pointer.CursorGrabbing {
		t.Errorf("panning, cursor %v", h.r.Cursor())
	}
	h.send(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: a.Add(f32.Pt(80, 40))})
	if h.u.sub != "" || v.panning || v.down {
		t.Fatalf("a drag clicked: sub %q", h.u.sub)
	}
	p := v.pan
	h.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: a})
	if v.pan != p {
		t.Fatal("a drag without a press panned")
	}
	h.send(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: a, Scroll: f32.Pt(10, 20)})
	if v.pan != p.Sub(f32.Pt(10, 20)) {
		t.Fatalf("wheel: pan %v", v.pan)
	}
	z := v.zoom
	at := h.cvAt(c.ID, "main")
	h.send(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: at, Scroll: f32.Pt(0, -120), Modifiers: key.ModShortcut})
	if v.zoom <= z {
		t.Fatalf("Ctrl+wheel up: zoom %v from %v", v.zoom, z)
	}
	if got := h.cvAt(c.ID, "main"); math.Abs(float64(got.X-at.X)) > 1 || math.Abs(float64(got.Y-at.Y)) > 1 {
		t.Fatalf("the node under the pointer moved: %v → %v", at, got)
	}
	h.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: at})
	if v.hover != "main" || h.r.Cursor() != pointer.CursorPointer {
		t.Fatalf("hover %q, cursor %v", v.hover, h.r.Cursor())
	}
	h.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: h.cvAt(c.ID, "done")})
	if v.hover != "done" || h.r.Cursor() != pointer.CursorDefault {
		t.Fatalf("over Done (nothing to open): hover %q, cursor %v", v.hover, h.r.Cursor())
	}
	h.send(pointer.Event{Kind: pointer.Leave, Source: pointer.Mouse, Position: at})
	if v.hover != "" {
		t.Fatalf("hover after leaving %q", v.hover)
	}
	h.clickButton("cvFit" + c.ID)
	h.frame()
	if !v.auto || v.pan != fitted || v.zoom != z {
		t.Fatalf("Fit: pan %v zoom %v, want %v %v", v.pan, v.zoom, fitted, z)
	}
	h.send(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: at, Scroll: f32.Pt(0, 5)})
	if v.auto {
		t.Fatal("the wheel left it fitted")
	}
	at = h.cvAt(c.ID, "main")
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: at},
		pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: at.Add(f32.Pt(0, 6))})
	if !v.panning {
		t.Fatal("a 6px move down didn't pan")
	}
	h.send(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: at.Add(f32.Pt(0, 6))})
}

// TestCanvasControls: Handoffs shows the list beside the canvas and hides
// it; the earlier chip brings the long-done agents back and sends them
// away; a working Claude breathes.
func TestCanvasControls(t *testing.T) {
	h := newHarness(t)
	c := canvasCard(h)
	h.u.findTask(c.ID, "a2", "").end = time.Now().Add(-settled - time.Minute)
	h.u.openSession(c.ID)
	h.frame()
	h.clickButton("cvHand")
	if !h.u.handoffsOn {
		t.Fatal("Handoffs didn't show")
	}
	if h.count("Handoffs", image.Rectangle{Max: h.size}) != 1 {
		t.Fatal("the handoffs aren't drawn")
	}
	h.clickButton("cvHand")
	if h.u.handoffsOn {
		t.Fatal("Hide handoffs didn't hide them")
	}
	h.clickButton("cvEarlier" + c.ID)
	if !h.u.earlier[c.ID] {
		t.Fatal("earlier: not shown")
	}
	h.clickButton("cvEarlier" + c.ID)
	if h.u.earlier[c.ID] {
		t.Fatal("earlier: not hidden")
	}
	h.u.b.Card(c.ID).Status = kanban.StatusWorking
	h.frame()
	if at, ok := h.r.WakeupTime(); !ok || at.After(time.Now().Add(600*time.Millisecond)) {
		t.Fatalf("a working Claude doesn't breathe: %v %v", at, ok)
	}
}
