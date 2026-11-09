package ui

import (
	"image"
	"image/color"
	"math"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/lfdominguez/desk/internal/draw"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// A Claude's live edit is marked on the canvas: an outline in its color
// around what it drew and its name, until the user's next press. Ctrl+Z
// takes the edit back (draw.Editor.Apply makes it one step).

// mark records ids as drawn by who, in place of the last edit's.
func (st *drawState) mark(ids []string, who string) {
	st.marks = map[string]string{}
	for _, id := range ids {
		st.marks[id] = who
	}
}

// markBox is a box in pane pixels and whose it is.
type markBox struct {
	r   image.Rectangle
	who string
}

// markBoxes are the marked elements still there, outlined (a shape's
// label goes with its shape), and a box per run of one Claude's: its tag's.
func (st *drawState) markBoxes(px float32) (outlines, tags []markBox) {
	pad := float64(6 / st.zoom)
	for _, e := range st.ed.Scene.Elements {
		who, ok := st.marks[e.ID]
		if !ok || e.IsDeleted || (e.ContainerID != nil && st.marks[*e.ContainerID] != "") {
			continue
		}
		x0, y0, x1, y1 := e.Bounds()
		a, b := st.toPane(draw.Pt{X: x0 - pad, Y: y0 - pad}, px), st.toPane(draw.Pt{X: x1 + pad, Y: y1 + pad}, px)
		r := image.Rect(int(a.X), int(a.Y), int(math.Ceil(float64(b.X))), int(math.Ceil(float64(b.Y))))
		outlines = append(outlines, markBox{r, who})
		if i := len(tags) - 1; i >= 0 && tags[i].who == who {
			tags[i].r = tags[i].r.Union(r)
			continue
		}
		tags = append(tags, markBox{r, who})
	}
	return outlines, tags
}

// drawMarks outlines the marked elements in their Claude's color, named under them.
func (u *ui) drawMarks(gtx layout.Context, st *drawState, px float32) {
	outlines, tags := st.markBoxes(px)
	for _, o := range outlines {
		paint.FillShape(gtx.Ops, labelColor(o.who), clip.Stroke{Path: clip.UniformRRect(o.r, int(3*px)).Path(gtx.Ops), Width: 1.5 * px}.Op())
	}
	for _, t := range tags {
		u.drawTag(gtx, image.Pt(t.r.Min.X, t.r.Max.Y), t.who, labelColor(t.who), true)
	}
}

// drawTag is a name on a chip of color c, its bottom left just above at
// (below: its top left just under at).
func (u *ui) drawTag(gtx layout.Context, at image.Point, name string, c color.NRGBA, below bool) {
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(260), gtx.Dp(40))}
	d := u.k.Label(g, name, 11, kit.RGB(0x101012), kit.Semi(kit.Sans), 1)
	text := m.Stop()
	padX, padY := gtx.Dp(6), gtx.Dp(2)
	h := d.Size.Y + 2*padY
	r := image.Rectangle{Min: at.Sub(image.Pt(0, h+gtx.Dp(3))), Max: at.Add(image.Pt(d.Size.X+2*padX, -gtx.Dp(3)))}
	if below {
		r = r.Add(image.Pt(0, h+2*gtx.Dp(3)))
	}
	kit.FillRRect(gtx.Ops, r, gtx.Dp(kit.RCtl), c)
	defer op.Offset(r.Min.Add(image.Pt(padX, padY))).Push(gtx.Ops).Pop()
	text.Add(gtx.Ops)
}
