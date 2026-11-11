package kit

import (
	"image"
	"image/color"
	"math"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
)

// FillRRect paints a rounded rectangle of any size. Gio rasterizes rounded
// shapes in a texture of at most 4096px, which a full-window panel at 2x
// exceeds, so big ones are plain rectangles plus four rounded corners.
func FillRRect(ops *op.Ops, r image.Rectangle, rad int, c color.NRGBA) {
	if c.A == 0 || r.Empty() {
		return // Gio would still rasterize the path
	}
	rad = min(rad, r.Dx()/2, r.Dy()/2)
	if r.Dx() < 2048 && r.Dy() < 2048 {
		paint.FillShape(ops, c, clip.UniformRRect(r, rad).Op(ops))
		return
	}
	paint.FillShape(ops, c, clip.Rect(image.Rect(r.Min.X+rad, r.Min.Y, r.Max.X-rad, r.Max.Y)).Op())
	paint.FillShape(ops, c, clip.Rect(image.Rect(r.Min.X, r.Min.Y+rad, r.Min.X+rad, r.Max.Y-rad)).Op())
	paint.FillShape(ops, c, clip.Rect(image.Rect(r.Max.X-rad, r.Min.Y+rad, r.Max.X, r.Max.Y-rad)).Op())
	for _, p := range []image.Point{r.Min, {r.Max.X - rad, r.Min.Y}, {r.Min.X, r.Max.Y - rad}, {r.Max.X - rad, r.Max.Y - rad}} {
		corner := image.Rectangle{Min: p, Max: p.Add(image.Pt(rad, rad))}
		cl := clip.Rect(corner).Push(ops)
		big := image.Rectangle{Min: p, Max: p.Add(image.Pt(2*rad, 2*rad))}
		if p.X > r.Min.X {
			big = big.Sub(image.Pt(rad, 0))
		}
		if p.Y > r.Min.Y {
			big = big.Sub(image.Pt(0, rad))
		}
		paint.FillShape(ops, c, clip.UniformRRect(big, rad).Op(ops))
		cl.Pop()
	}
}

// Fill paints the whole of gtx's max constraint.
func Fill(gtx layout.Context, c color.NRGBA) {
	paint.FillShape(gtx.Ops, c, clip.Rect{Max: gtx.Constraints.Max}.Op())
}

// Panel paints a rounded panel with a 1px rim.
func Panel(gtx layout.Context, r image.Rectangle, rad unit.Dp, fill, rim color.NRGBA) {
	px := gtx.Dp(rad)
	if rim.A != 0 {
		FillRRect(gtx.Ops, r, px, rim)
		r = r.Inset(gtx.Dp(1))
		px = max(0, px-gtx.Dp(1))
	}
	FillRRect(gtx.Ops, r, px, fill)
}

// Boxed lays w out on a rounded background sized to w plus padding.
func Boxed(fill, rim color.NRGBA, rad unit.Dp, in layout.Inset, w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Background{}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				Panel(gtx, image.Rectangle{Max: gtx.Constraints.Min}, rad, fill, rim)
				return layout.Dimensions{Size: gtx.Constraints.Min}
			},
			func(gtx layout.Context) layout.Dimensions { return in.Layout(gtx, w) })
	}
}

func Mat(gtx layout.Context, c color.NRGBA) op.CallOp {
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	return m.Stop()
}

func HGap(dp unit.Dp) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: image.Pt(gtx.Dp(dp), 0)} }
}

func VGap(dp unit.Dp) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: image.Pt(0, gtx.Dp(dp))} }
}

// Wrap lays ws out left to right, starting a new row when one doesn't fit,
// gap dp apart both ways.
func Wrap(gtx layout.Context, gap unit.Dp, ws []layout.Widget) layout.Dimensions {
	g, maxW := gtx.Dp(gap), gtx.Constraints.Max.X
	var x, y, rowH, w int
	for _, wd := range ws {
		m := op.Record(gtx.Ops)
		cgtx := gtx
		cgtx.Constraints.Min = image.Point{}
		d := wd(cgtx)
		call := m.Stop()
		if x > 0 && x+d.Size.X > maxW {
			x, y, rowH = 0, y+rowH+g, 0
		}
		st := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		st.Pop()
		x += d.Size.X + g
		w, rowH = max(w, x-g), max(rowH, d.Size.Y)
	}
	return layout.Dimensions{Size: image.Pt(w, y+rowH)}
}

// Label draws text; maxLines 0 wraps freely.
func (k *Kit) Label(gtx layout.Context, s string, sp unit.Sp, c color.NRGBA, f font.Font, maxLines int) layout.Dimensions {
	return widget.Label{MaxLines: maxLines}.Layout(gtx, k.Sh, f, sp, s, Mat(gtx, c))
}

func (k *Kit) Lbl(s string, sp unit.Sp, c color.NRGBA, f font.Font) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Point{}
		return k.Label(gtx, s, sp, c, f, 1)
	}
}

// LblFill is a one-line label that takes all the width it is given, so what
// follows it in a row lands at the far end.
func (k *Kit) LblFill(s string, sp unit.Sp, c color.NRGBA, f font.Font) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		gtx.Constraints.Min.Y = 0
		return k.Label(gtx, s, sp, c, f, 1)
	}
}

func (k *Kit) Pill(s string, fg, bg color.NRGBA) layout.Widget {
	return Boxed(bg, color.NRGBA{}, 3, layout.Inset{Top: 1, Bottom: 1, Left: 6, Right: 6}, k.Lbl(s, 11, fg, Semi(Sans)))
}

func (k *Kit) Click(id string) *widget.Clickable {
	id = k.Scope + id
	c, ok := k.clicks[id]
	if !ok {
		c = new(widget.Clickable)
		k.clicks[id] = c
	}
	return c
}

// Button lays out content as a clickable with a hover tint and reports clicks.
func (k *Kit) Button(gtx layout.Context, id string, fill, hover color.NRGBA, rad unit.Dp, in layout.Inset, content layout.Widget) (layout.Dimensions, bool) {
	return k.RimButton(gtx, id, fill, hover, color.NRGBA{}, rad, in, content)
}

// RimButton is Button with a rim. The hover tint is picked here, after the
// frame's pointer events: read before them (outside), it lags a frame and,
// since Gio draws only on events, stays wrong until the pointer moves again.
func (k *Kit) RimButton(gtx layout.Context, id string, fill, hover, rim color.NRGBA, rad unit.Dp, in layout.Inset, content layout.Widget) (layout.Dimensions, bool) {
	c := k.Click(id)
	clicked := c.Clicked(gtx)
	if clicked { // what the click changes was partly drawn already this frame: draw again
		gtx.Execute(op.InvalidateCmd{})
	}
	bg := fill
	if c.Hovered() && hover.A != 0 {
		bg = hover
	}
	d := c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		d := Boxed(bg, rim, rad, in, content)(gtx)
		st := clip.Rect{Max: d.Size}.Push(gtx.Ops)
		pointer.CursorPointer.Add(gtx.Ops)
		st.Pop()
		return d
	})
	return d, clicked
}

// Btn is a text button.
func (k *Kit) Btn(gtx layout.Context, id, s string, fg, fill color.NRGBA) bool {
	_, ok := k.Button(gtx, id, fill, CHover, RCtl, layout.Inset{Top: 5, Bottom: 5, Left: 12, Right: 12}, k.Lbl(s, 13, fg, Semi(Sans)))
	return ok
}

// IconBtn is a square icon button, tinted when on.
func (k *Kit) IconBtn(gtx layout.Context, id string, ico IconPath, on bool) (layout.Dimensions, bool) {
	c, bg := CText2, color.NRGBA{}
	if on {
		c, bg = CAccText, CAccSoft
	}
	return k.Button(gtx, id, bg, CHover, RCtl, layout.UniformInset(5), Icon(15, c, ico))
}

// Tip shows s under button id (laid out as d) while the pointer is on it,
// over everything else: right aligns it to the button's right edge, for
// buttons near the window's right.
func (k *Kit) Tip(gtx layout.Context, id string, d layout.Dimensions, s string, right bool) {
	if !k.Click(id).Hovered() {
		return
	}
	m := op.Record(gtx.Ops)
	tgtx := gtx
	tgtx.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(360), gtx.Dp(120))}
	td := Boxed(CPanelCard, CRim, RCtl, layout.Inset{Top: 4, Bottom: 4, Left: 8, Right: 8}, k.Lbl(s, 11, CText, Sans))(tgtx)
	call := m.Stop()
	x := 0
	if right {
		x = d.Size.X - td.Size.X
	}
	m = op.Record(gtx.Ops)
	op.Offset(image.Pt(x, d.Size.Y+gtx.Dp(4))).Add(gtx.Ops)
	call.Add(gtx.Ops)
	op.Defer(gtx.Ops, m.Stop())
}

func Dot(c color.NRGBA) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		s := gtx.Dp(7)
		paint.FillShape(gtx.Ops, c, clip.Ellipse{Max: image.Pt(s, s)}.Op(gtx.Ops))
		return layout.Dimensions{Size: image.Pt(s, s)}
	}
}

// Spinner is a turning three-quarter ring, sz dp wide; it keeps asking
// for frames while drawn.
func Spinner(sz unit.Dp, c color.NRGBA) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		s, w := gtx.Dp(sz), float32(gtx.Dp(2))
		ctr := f32.Pt(float32(s)/2, float32(s)/2)
		r := ctr.X - w/2
		a := float64(gtx.Now.UnixMilli()%900) / 900 * 2 * math.Pi
		var p clip.Path
		p.Begin(gtx.Ops)
		p.MoveTo(ctr.Add(f32.Pt(r*float32(math.Cos(a)), r*float32(math.Sin(a)))))
		p.ArcTo(ctr, ctr, 1.5*math.Pi)
		paint.FillShape(gtx.Ops, c, clip.Stroke{Path: p.End(), Width: w}.Op())
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(50 * time.Millisecond)}) // 20 a second: each redraws the whole window
		return layout.Dimensions{Size: image.Pt(s, s)}
	}
}

// Editor draws a widget.Editor with a hint when empty.
func (k *Kit) Editor(gtx layout.Context, e *widget.Editor, hint string, sp unit.Sp, f font.Font, c color.NRGBA) layout.Dimensions {
	if e.Len() == 0 {
		m := op.Record(gtx.Ops)
		k.Label(gtx, hint, sp, CText3, f, 1)
		hintCall := m.Stop()
		d := e.Layout(gtx, k.Sh, f, sp, Mat(gtx, c), Mat(gtx, CAccSoft))
		hintCall.Add(gtx.Ops)
		return d
	}
	return e.Layout(gtx, k.Sh, f, sp, Mat(gtx, c), Mat(gtx, CAccSoft))
}

// Field is an editor in a sunken box, the look of every text input.
func (k *Kit) Field(e *widget.Editor, hint string, minH unit.Dp) layout.Widget {
	return Boxed(CPane, CLine, RCtl, layout.Inset{Top: 9, Bottom: 9, Left: 12, Right: 12}, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, gtx.Dp(minH))
		return k.Editor(gtx, e, hint, 14, Sans, CText)
	})
}
