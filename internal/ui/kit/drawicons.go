package kit

import (
	"gioui.org/f32"
	"gioui.org/op/clip"
)

// poly strokes a polyline through xy pairs given in fractions of the box s.
func poly(p *clip.Path, s float32, xy ...float32) {
	p.MoveTo(f32.Pt(xy[0]*s, xy[1]*s))
	for i := 2; i+1 < len(xy); i += 2 {
		p.LineTo(f32.Pt(xy[i]*s, xy[i+1]*s))
	}
}

// The drawing editor's tools and actions.
var (
	IcoPointer IconPath = func(p *clip.Path, s float32) { // a mouse pointer: select
		poly(p, s, .3, .18, .3, .8, .45, .64, .56, .86, .65, .82, .54, .6, .74, .58, .3, .18)
	}
	IcoHand IconPath = func(p *clip.Path, s float32) { // an open hand: pan
		poly(p, s, .3, .55, .3, .3, .38, .26, .44, .3, .44, .5)
		poly(p, s, .44, .45, .44, .22, .52, .18, .58, .22, .58, .48)
		poly(p, s, .58, .45, .58, .28, .66, .25, .72, .3, .72, .62)
		poly(p, s, .3, .55, .22, .5, .17, .56, .34, .8, .46, .86, .62, .86, .72, .74, .72, .6)
	}
	IcoSquare IconPath = func(p *clip.Path, s float32) { // a rounded square: rectangle
		poly(p, s, .26, .2, .74, .2, .8, .26, .8, .74, .74, .8, .26, .8, .2, .74, .2, .26, .26, .2)
	}
	IcoDiamond IconPath = func(p *clip.Path, s float32) {
		poly(p, s, .5, .14, .86, .5, .5, .86, .14, .5, .5, .14)
	}
	IcoCircle IconPath = func(p *clip.Path, s float32) { // ellipse
		c := f32.Pt(s*.5, s*.5)
		p.MoveTo(f32.Pt(s*.84, s*.5))
		p.ArcTo(c, c, 6.2832)
	}
	IcoArrow IconPath = func(p *clip.Path, s float32) { // a long arrow to the right
		poly(p, s, .14, .5, .86, .5)
		poly(p, s, .64, .3, .86, .5, .64, .7)
	}
	IcoLine IconPath = func(p *clip.Path, s float32) {
		poly(p, s, .14, .5, .86, .5)
	}
	IcoPencil IconPath = func(p *clip.Path, s float32) { // freedraw
		poly(p, s, .2, .8, .24, .62, .66, .2, .8, .34, .38, .76, .2, .8)
		poly(p, s, .58, .28, .72, .42)
	}
	IcoLetter IconPath = func(p *clip.Path, s float32) { // a capital A: text
		poly(p, s, .22, .82, .5, .16, .78, .82)
		poly(p, s, .33, .58, .67, .58)
	}
	IcoEraser IconPath = func(p *clip.Path, s float32) {
		poly(p, s, .36, .82, .16, .62, .54, .22, .84, .52, .54, .82, .36, .82)
		poly(p, s, .34, .44, .64, .74)
		poly(p, s, .54, .82, .86, .82)
	}
	IcoUndo IconPath = func(p *clip.Path, s float32) { // a hook to the left
		poly(p, s, .36, .26, .18, .42, .36, .58)
		p.MoveTo(f32.Pt(s*.18, s*.42))
		p.LineTo(f32.Pt(s*.6, s*.42))
		p.QuadTo(f32.Pt(s*.84, s*.42), f32.Pt(s*.84, s*.62))
		p.QuadTo(f32.Pt(s*.84, s*.8), f32.Pt(s*.6, s*.8))
		p.LineTo(f32.Pt(s*.4, s*.8))
	}
	IcoRedo IconPath = func(p *clip.Path, s float32) { // a hook to the right
		poly(p, s, .64, .26, .82, .42, .64, .58)
		p.MoveTo(f32.Pt(s*.82, s*.42))
		p.LineTo(f32.Pt(s*.4, s*.42))
		p.QuadTo(f32.Pt(s*.16, s*.42), f32.Pt(s*.16, s*.62))
		p.QuadTo(f32.Pt(s*.16, s*.8), f32.Pt(s*.4, s*.8))
		p.LineTo(f32.Pt(s*.6, s*.8))
	}
	IcoMoon IconPath = func(p *clip.Path, s float32) { // a crescent: the dark canvas
		p.MoveTo(f32.Pt(s*.62, s*.16))
		p.QuadTo(f32.Pt(s*.14, s*.2), f32.Pt(s*.2, s*.6))
		p.QuadTo(f32.Pt(s*.4, s*.92), f32.Pt(s*.84, s*.66))
		p.QuadTo(f32.Pt(s*.46, s*.62), f32.Pt(s*.62, s*.16))
	}
	IcoCopy IconPath = func(p *clip.Path, s float32) { // two sheets: duplicate
		poly(p, s, .36, .36, .82, .36, .82, .82, .36, .82, .36, .36)
		poly(p, s, .64, .36, .64, .18, .18, .18, .18, .64, .36, .64)
	}
	IcoFront IconPath = func(p *clip.Path, s float32) { // up to the top bar: to the front
		poly(p, s, .2, .16, .8, .16)
		poly(p, s, .5, .84, .5, .3)
		poly(p, s, .3, .5, .5, .3, .7, .5)
	}
	IcoForward IconPath = func(p *clip.Path, s float32) { // one step up
		poly(p, s, .5, .84, .5, .2)
		poly(p, s, .3, .4, .5, .2, .7, .4)
	}
	IcoBackward IconPath = func(p *clip.Path, s float32) { // one step down
		poly(p, s, .5, .16, .5, .8)
		poly(p, s, .3, .6, .5, .8, .7, .6)
	}
	IcoBack IconPath = func(p *clip.Path, s float32) { // down to the bottom bar: to the back
		poly(p, s, .2, .84, .8, .84)
		poly(p, s, .5, .16, .5, .7)
		poly(p, s, .3, .5, .5, .7, .7, .5)
	}
	IcoFit IconPath = func(p *clip.Path, s float32) { // four corners: show it all
		poly(p, s, .16, .36, .16, .16, .36, .16)
		poly(p, s, .64, .16, .84, .16, .84, .36)
		poly(p, s, .84, .64, .84, .84, .64, .84)
		poly(p, s, .36, .84, .16, .84, .16, .64)
	}
	IcoMinus IconPath = func(p *clip.Path, s float32) {
		poly(p, s, .22, .5, .78, .5)
	}
)
