package kit

import (
	"image"
	"image/color"
	"math"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
)

// IconPath draws a glyph into a box of side s.
type IconPath func(p *clip.Path, s float32)

// Icon draws a small stroked glyph in a dp box.
func Icon(sz unit.Dp, c color.NRGBA, draw IconPath) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		px := gtx.Dp(sz)
		var p clip.Path
		p.Begin(gtx.Ops)
		draw(&p, float32(px))
		paint.FillShape(gtx.Ops, c, clip.Stroke{Path: p.End(), Width: float32(gtx.Dp(1.6))}.Op())
		return layout.Dimensions{Size: image.Pt(px, px)}
	}
}

func RectPath(p *clip.Path, x0, y0, x1, y1 float32) {
	p.MoveTo(f32.Pt(x0, y0))
	p.LineTo(f32.Pt(x1, y0))
	p.LineTo(f32.Pt(x1, y1))
	p.LineTo(f32.Pt(x0, y1))
	p.Close()
}

var (
	IcoPlus IconPath = func(p *clip.Path, s float32) {
		p.MoveTo(f32.Pt(s*.5, s*.2))
		p.LineTo(f32.Pt(s*.5, s*.8))
		p.MoveTo(f32.Pt(s*.2, s*.5))
		p.LineTo(f32.Pt(s*.8, s*.5))
	}
	IcoX IconPath = func(p *clip.Path, s float32) {
		p.MoveTo(f32.Pt(s*.28, s*.28))
		p.LineTo(f32.Pt(s*.72, s*.72))
		p.MoveTo(f32.Pt(s*.72, s*.28))
		p.LineTo(f32.Pt(s*.28, s*.72))
	}
	IcoSearch IconPath = func(p *clip.Path, s float32) {
		c := f32.Pt(s*.44, s*.44)
		p.MoveTo(f32.Pt(c.X+s*.28, c.Y))
		p.ArcTo(c, c, 6.2832)
		p.MoveTo(f32.Pt(s*.64, s*.64))
		p.LineTo(f32.Pt(s*.88, s*.88))
	}
	IcoClock IconPath = func(p *clip.Path, s float32) {
		c := f32.Pt(s*.5, s*.5)
		p.MoveTo(f32.Pt(c.X+s*.38, c.Y))
		p.ArcTo(c, c, 6.2832)
		p.MoveTo(f32.Pt(s*.5, s*.26))
		p.LineTo(f32.Pt(s*.5, s*.5))
		p.LineTo(f32.Pt(s*.68, s*.62))
	}
	IcoChat IconPath = func(p *clip.Path, s float32) { // a speech bubble
		p.MoveTo(f32.Pt(s*.14, s*.2))
		p.LineTo(f32.Pt(s*.86, s*.2))
		p.LineTo(f32.Pt(s*.86, s*.68))
		p.LineTo(f32.Pt(s*.42, s*.68))
		p.LineTo(f32.Pt(s*.24, s*.86))
		p.LineTo(f32.Pt(s*.24, s*.68))
		p.LineTo(f32.Pt(s*.14, s*.68))
		p.Close()
	}
	IcoWarn IconPath = func(p *clip.Path, s float32) {
		p.MoveTo(f32.Pt(s*.5, s*.12))
		p.LineTo(f32.Pt(s*.9, s*.84))
		p.LineTo(f32.Pt(s*.1, s*.84))
		p.Close()
		p.MoveTo(f32.Pt(s*.5, s*.38))
		p.LineTo(f32.Pt(s*.5, s*.6))
		p.MoveTo(f32.Pt(s*.5, s*.7))
		p.LineTo(f32.Pt(s*.5, s*.74))
	}
	IcoCheck IconPath = func(p *clip.Path, s float32) { // a tick in a circle
		c := f32.Pt(s*.5, s*.5)
		p.MoveTo(f32.Pt(c.X+s*.38, c.Y))
		p.ArcTo(c, c, 6.2832)
		p.MoveTo(f32.Pt(s*.32, s*.52))
		p.LineTo(f32.Pt(s*.45, s*.65))
		p.LineTo(f32.Pt(s*.69, s*.38))
	}
	IcoSpark IconPath = func(p *clip.Path, s float32) { // working
		p.MoveTo(f32.Pt(s*.5, s*.1))
		p.LineTo(f32.Pt(s*.6, s*.4))
		p.LineTo(f32.Pt(s*.9, s*.5))
		p.LineTo(f32.Pt(s*.6, s*.6))
		p.LineTo(f32.Pt(s*.5, s*.9))
		p.LineTo(f32.Pt(s*.4, s*.6))
		p.LineTo(f32.Pt(s*.1, s*.5))
		p.LineTo(f32.Pt(s*.4, s*.4))
		p.Close()
	}
	IcoTerminal IconPath = func(p *clip.Path, s float32) {
		RectPath(p, s*.1, s*.18, s*.9, s*.82)
		p.MoveTo(f32.Pt(s*.26, s*.38))
		p.LineTo(f32.Pt(s*.4, s*.5))
		p.LineTo(f32.Pt(s*.26, s*.62))
		p.MoveTo(f32.Pt(s*.48, s*.64))
		p.LineTo(f32.Pt(s*.7, s*.64))
	}
	IcoArchive IconPath = func(p *clip.Path, s float32) {
		RectPath(p, s*.1, s*.18, s*.9, s*.36)
		p.MoveTo(f32.Pt(s*.18, s*.36))
		p.LineTo(f32.Pt(s*.18, s*.84))
		p.LineTo(f32.Pt(s*.82, s*.84))
		p.LineTo(f32.Pt(s*.82, s*.36))
		p.MoveTo(f32.Pt(s*.4, s*.52))
		p.LineTo(f32.Pt(s*.6, s*.52))
	}
	IcoEdit IconPath = func(p *clip.Path, s float32) { // a pencil
		p.MoveTo(f32.Pt(s*.18, s*.82))
		p.LineTo(f32.Pt(s*.22, s*.62))
		p.LineTo(f32.Pt(s*.66, s*.18))
		p.LineTo(f32.Pt(s*.82, s*.34))
		p.LineTo(f32.Pt(s*.38, s*.78))
		p.Close()
	}
	IcoLink IconPath = func(p *clip.Path, s float32) { // two chain links
		RectPath(p, s*.1, s*.36, s*.52, s*.64)
		RectPath(p, s*.48, s*.36, s*.9, s*.64)
	}
	IcoSend IconPath = func(p *clip.Path, s float32) {
		p.MoveTo(f32.Pt(s*.5, s*.84))
		p.LineTo(f32.Pt(s*.5, s*.18))
		p.MoveTo(f32.Pt(s*.24, s*.42))
		p.LineTo(f32.Pt(s*.5, s*.16))
		p.LineTo(f32.Pt(s*.76, s*.42))
	}
	IcoStop IconPath = func(p *clip.Path, s float32) { RectPath(p, s*.26, s*.26, s*.74, s*.74) }
	IcoSide IconPath = func(p *clip.Path, s float32) {
		RectPath(p, s*.1, s*.18, s*.9, s*.82)
		p.MoveTo(f32.Pt(s*.38, s*.18))
		p.LineTo(f32.Pt(s*.38, s*.82))
	}
	IcoPopOut IconPath = func(p *clip.Path, s float32) { // a window with an arrow leaving it: its own window
		p.MoveTo(f32.Pt(s*.46, s*.18))
		p.LineTo(f32.Pt(s*.16, s*.18))
		p.LineTo(f32.Pt(s*.16, s*.84))
		p.LineTo(f32.Pt(s*.82, s*.84))
		p.LineTo(f32.Pt(s*.82, s*.54))
		p.MoveTo(f32.Pt(s*.46, s*.54))
		p.LineTo(f32.Pt(s*.84, s*.16))
		p.MoveTo(f32.Pt(s*.6, s*.16))
		p.LineTo(f32.Pt(s*.84, s*.16))
		p.LineTo(f32.Pt(s*.84, s*.4))
	}
	IcoStar IconPath = func(p *clip.Path, s float32) {
		for k := range 10 {
			a, rad := float64(k)*math.Pi/5-math.Pi/2, float32(.44)
			if k%2 == 1 {
				rad = .19
			}
			pt := f32.Pt(s*(.5+rad*float32(math.Cos(a))), s*(.54+rad*float32(math.Sin(a))))
			if k == 0 {
				p.MoveTo(pt)
			} else {
				p.LineTo(pt)
			}
		}
		p.Close()
	}
	IcoBoard IconPath = func(p *clip.Path, s float32) { // three columns of uneven height
		RectPath(p, s*.12, s*.16, s*.34, s*.84)
		RectPath(p, s*.39, s*.16, s*.61, s*.6)
		RectPath(p, s*.66, s*.16, s*.88, s*.72)
	}
	IcoLock IconPath = func(p *clip.Path, s float32) { // a padlock
		RectPath(p, s*.22, s*.46, s*.78, s*.86)
		p.MoveTo(f32.Pt(s*.34, s*.46))
		p.LineTo(f32.Pt(s*.34, s*.32))
		p.ArcTo(f32.Pt(s*.5, s*.32), f32.Pt(s*.5, s*.32), math.Pi)
		p.LineTo(f32.Pt(s*.66, s*.46))
	}
	IcoDown IconPath = func(p *clip.Path, s float32) { // a chevron pointing down: more under it
		p.MoveTo(f32.Pt(s*.28, s*.4))
		p.LineTo(f32.Pt(s*.5, s*.62))
		p.LineTo(f32.Pt(s*.72, s*.4))
	}
	IcoTrash IconPath = func(p *clip.Path, s float32) { // a bin with its lid
		p.MoveTo(f32.Pt(s*.18, s*.28))
		p.LineTo(f32.Pt(s*.82, s*.28))
		p.MoveTo(f32.Pt(s*.4, s*.28))
		p.LineTo(f32.Pt(s*.4, s*.18))
		p.LineTo(f32.Pt(s*.6, s*.18))
		p.LineTo(f32.Pt(s*.6, s*.28))
		p.MoveTo(f32.Pt(s*.28, s*.28))
		p.LineTo(f32.Pt(s*.33, s*.85))
		p.LineTo(f32.Pt(s*.67, s*.85))
		p.LineTo(f32.Pt(s*.72, s*.28))
	}
	IcoCode IconPath = func(p *clip.Path, s float32) { // </>: the workspace
		p.MoveTo(f32.Pt(s*.32, s*.3))
		p.LineTo(f32.Pt(s*.12, s*.5))
		p.LineTo(f32.Pt(s*.32, s*.7))
		p.MoveTo(f32.Pt(s*.68, s*.3))
		p.LineTo(f32.Pt(s*.88, s*.5))
		p.LineTo(f32.Pt(s*.68, s*.7))
		p.MoveTo(f32.Pt(s*.56, s*.2))
		p.LineTo(f32.Pt(s*.44, s*.8))
	}
)
