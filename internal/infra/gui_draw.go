package infra

import (
	"image"
	"image/color"
	"io"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/x/styledtext"

	"github.com/lfdominguez/desk/internal/ui/kit"
)

// ui is the window's own state: what is open, scroll positions, editors.
// Everything about sessions comes from the driver's snapshot, st.
type ui struct {
	inv     func()          // asks desk's window for a frame (from any goroutine)
	org     string          // the organization Layout shows; its tabs only
	lastTab map[string]*Tab // the tab last active in each org, to come back to
	d       *driver
	st      *guiState
	sh      *text.Shaper
	gen     atomic.Uint64 // bumps when there is something new to draw
	hidden  atomic.Bool   // desk doesn't show the workspace: terminal output asks for no frame (SetShown)
	treeOf  *guiState     // the snapshot tree was made from (nil: make it again)
	tree    []Org         // Tree's last answer

	side, claude bool
	claudeW      unit.Dp
	hlists       map[string]*layout.List // sideways scroll of wide chat pictures, by block
	closed       map[string]bool         // folded sidebar groups
	pins         map[string]bool         // pinned hosts (hostKey), kept in stateDir()/pins
	sideSel      int                     // the search's keyboard row among the host rows
	clicks       map[string]*widget.Clickable
	terms        map[*Term]*termView

	search, input, broadcastIn, palIn, askIn, apEdit, nameIn widget.Editor
	files                                                    map[*Tab]*filesView // each tab's directory split, if it has one
	treeList, chatList, cardList, palList                    layout.List

	pal, editing                 bool
	palSel, palShown             int    // palette selection; the one last scrolled into view
	palQ, palScope               string // the palette's query; the kind it shows ("" all)
	sheetSig, askSig             string
	wantTerm, toggleComposer     bool
	keyTerm                      *Term // the pane the keyboard was on (paneNext: the next shown); a new focused pane takes it
	copySelection, pasteWanted   bool
	sels                         map[string]*selText // chat text blocks by id
	chatSel                      *selText            // the one with a selection
	selGlyphs                    []text.Glyph
	clipOut                      chan string
	toast                        string
	toastBad                     bool
	toastUntil                   time.Time
	lastFlash, lastErr, focusSig string
	grip                         gripState
}

func newUI(d *driver, l guiLayout, invalidate func()) *ui {
	u := &ui{inv: invalidate, lastTab: map[string]*Tab{}, d: d, st: &guiState{}, side: !l.NoSidebar, claude: !l.NoClaude, claudeW: 400, closed: map[string]bool{},
		clicks: map[string]*widget.Clickable{}, terms: map[*Term]*termView{}, files: map[*Tab]*filesView{}, sels: map[string]*selText{}, clipOut: make(chan string, 4), wantTerm: true,
		hlists: map[string]*layout.List{}, pins: loadPins()}
	if l.ClaudeW > 0 {
		u.claudeW = unit.Dp(l.ClaudeW)
	}
	u.sh = kit.New().Sh
	u.search.SingleLine, u.search.Submit = true, true
	u.input.Submit = true
	u.broadcastIn.SingleLine, u.broadcastIn.Submit = true, true
	u.palIn.SingleLine, u.palIn.Submit = true, true
	u.askIn.SingleLine, u.askIn.Submit, u.askIn.Mask = true, true, '•'
	u.apEdit.Submit = true
	u.nameIn.SingleLine, u.nameIn.Submit = true, true
	u.treeList.Axis, u.chatList.Axis, u.cardList.Axis, u.palList.Axis = layout.Vertical, layout.Vertical, layout.Vertical, layout.Vertical
	u.chatList.ScrollToEnd = true
	return u
}

// sansFont and monoFont are desk's (kit: Manrope, Geist Mono), so the
// workspace reads like the rest of the window.
var (
	sansFont = kit.Sans
	monoFont = kit.Mono
)

func fSemi(f font.Font) font.Font   { return kit.Semi(f) }
func fBold(f font.Font) font.Font   { return kit.Bold(f) }
func fMedium(f font.Font) font.Font { return kit.Medium(f) }

// Design tokens (dark), opaque: Gio has no backdrop blur, so the panels are
// solid tints of the window background.
var (
	cPanel      = rgb(0x22232a)
	cRim        = rgb(0x31323a)
	cPane       = rgb(0x0e0f13)
	cCard       = rgb(0x1c1d23)
	cPanelCard  = rgb(0x2b2c33)
	cHover      = rgb(0x30313a)
	cLine       = rgb(0x2c2d34)
	cText       = rgb(0xf5f5f7)
	cText2      = rgb(0xb8b8c0)
	cText3      = rgb(0x85858e)
	cAcc        = rgb(0x0a84ff)
	cAccText    = rgb(0x64b5ff)
	cAccSoft    = rgb(0x14325a)
	cGreen      = rgb(0x30d158)
	cOrange     = rgb(0xff9f0a)
	cOrangeSoft = rgb(0x3d2c12)
	cPurple     = rgb(0xbf5af2)
	cPurpleSoft = rgb(0x33204a)
	cRed        = rgb(0xff6961)
	cIdle       = rgb(0x5a5a62)
	cWhite      = rgb(0xffffff)
	cScrim      = color.NRGBA{0, 0, 0, 0x88}
	cDlgScrim   = kit.CDim // behind a dialog (kit.Modal's)
	// the dialog redesign's tokens (kit/dialog.go); applyTheme derives them
	// for themes other than desk's
	cKeyRim  = kit.CKeyRim
	cBtnRim  = kit.CBtnRim
	cDanger  = kit.CDanger
	cSel     = kit.CSel
	cRedSoft = kit.CRedSoft
	cRedText = kit.CRedText
)

// Corner radii: square, flush panels (the window background shows through
// 1dp gaps as dividers) and small corners on controls, cards and sheets.
const (
	rCtl   unit.Dp = 4
	rCard  unit.Dp = 6
	rSheet unit.Dp = 8
)

func rgb(h uint32) color.NRGBA { return color.NRGBA{uint8(h >> 16), uint8(h >> 8), uint8(h), 0xff} }

func (u *ui) background(gtx layout.Context) {
	paint.FillShape(gtx.Ops, cLine, clip.Rect{Max: gtx.Constraints.Max}.Op())
}

// fillRRect paints a rounded rectangle of any size. Gio rasterizes rounded
// shapes in a texture of at most 4096px, which a full-window pane at 2x
// exceeds, so big ones are plain rectangles plus four rounded corners.
func fillRRect(ops *op.Ops, r image.Rectangle, rad int, c color.NRGBA) {
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

// panel paints a rounded panel with a 1px rim.
func panel(gtx layout.Context, r image.Rectangle, rad unit.Dp, fill, rim color.NRGBA) {
	px := gtx.Dp(rad)
	if rim.A != 0 {
		fillRRect(gtx.Ops, r, px, rim)
		r = r.Inset(gtx.Dp(1))
		px = max(0, px-gtx.Dp(1))
	}
	fillRRect(gtx.Ops, r, px, fill)
}

// boxed lays w out on a rounded background sized to w plus padding.
func boxed(fill, rim color.NRGBA, rad unit.Dp, in layout.Inset, w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Background{}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				panel(gtx, image.Rectangle{Max: gtx.Constraints.Min}, rad, fill, rim)
				return layout.Dimensions{Size: gtx.Constraints.Min}
			},
			func(gtx layout.Context) layout.Dimensions { return in.Layout(gtx, w) })
	}
}

func mat(gtx layout.Context, c color.NRGBA) op.CallOp {
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	return m.Stop()
}

// label draws text; maxLines 0 wraps freely.
func (u *ui) label(gtx layout.Context, s string, sp unit.Sp, c color.NRGBA, f font.Font, maxLines int) layout.Dimensions {
	return widget.Label{MaxLines: maxLines}.Layout(gtx, u.sh, f, sp, s, mat(gtx, c))
}

func (u *ui) lbl(s string, sp unit.Sp, c color.NRGBA, f font.Font) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Point{}
		return u.label(gtx, s, sp, c, f, 1)
	}
}

// lblFill is a one-line label that takes all the width it is given, so what
// follows it in a row lands at the far end.
func (u *ui) lblFill(s string, sp unit.Sp, c color.NRGBA, f font.Font) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		gtx.Constraints.Min.Y = 0
		return u.label(gtx, s, sp, c, f, 1)
	}
}

func (u *ui) pill(s string, fg, bg color.NRGBA) layout.Widget {
	return boxed(bg, color.NRGBA{}, 3, layout.Inset{Top: 1, Bottom: 1, Left: 6, Right: 6}, u.lbl(s, 11, fg, fSemi(sansFont)))
}

func (u *ui) click(id string) *widget.Clickable {
	c, ok := u.clicks[id]
	if !ok {
		c = new(widget.Clickable)
		u.clicks[id] = c
	}
	return c
}

// button lays out content as a clickable with a hover tint and reports clicks.
func (u *ui) button(gtx layout.Context, id string, fill, hover color.NRGBA, rad unit.Dp, in layout.Inset, content layout.Widget) (layout.Dimensions, bool) {
	return u.rimButton(gtx, id, fill, hover, color.NRGBA{}, rad, in, content)
}

// rimButton is button with a 1dp rim.
func (u *ui) rimButton(gtx layout.Context, id string, fill, hover, rim color.NRGBA, rad unit.Dp, in layout.Inset, content layout.Widget) (layout.Dimensions, bool) {
	c := u.click(id)
	clicked := c.Clicked(gtx)
	bg := fill
	if c.Hovered() && hover.A != 0 {
		bg = hover
	}
	d := c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		d := boxed(bg, rim, rad, in, content)(gtx)
		st := clip.Rect{Max: d.Size}.Push(gtx.Ops)
		pointer.CursorPointer.Add(gtx.Ops)
		st.Pop()
		return d
	})
	return d, clicked
}

// keyCap draws key k ("Y", "esc", "↵"…) as a raised cap: kit.KeyCap, but in
// the window's themed tokens (kit's are desk's dark look only). On a filled
// button it turns translucent white.
func (u *ui) keyCap(k string, onFill bool) layout.Widget {
	fg, bg, rim := cText2, cPanelCard, cKeyRim
	if onFill {
		fg, bg, rim = cWhite, color.NRGBA{0xff, 0xff, 0xff, 0x29}, color.NRGBA{0xff, 0xff, 0xff, 0x47}
	}
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Point{}
		return layout.Stack{}.Layout(gtx,
			layout.Expanded(func(gtx layout.Context) layout.Dimensions {
				// a thicker bottom edge makes it read as a key
				r := image.Rectangle{Max: gtx.Constraints.Min}
				fillRRect(gtx.Ops, r, gtx.Dp(3), rim)
				fillRRect(gtx.Ops, image.Rect(r.Min.X+gtx.Dp(1), r.Min.Y+gtx.Dp(1), r.Max.X-gtx.Dp(1), r.Max.Y-gtx.Dp(2)), gtx.Dp(2), bg)
				return layout.Dimensions{Size: gtx.Constraints.Min}
			}),
			layout.Stacked(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 1, Bottom: 2, Left: 5, Right: 5}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = max(gtx.Constraints.Min.X, gtx.Dp(8))
					return layout.Center.Layout(gtx, u.lbl(k, 11, fg, monoFont))
				})
			}))
	}
}

// hint is a key cap followed by what it does, for footers.
func (u *ui) hint(k, what string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(u.keyCap(k, false)), layout.Rigid(hgap(6)), layout.Rigid(u.lbl(what, 12, cText3, sansFont)))
	}
}

// kbtn is a dialog button (kit.KBtn in the themed tokens): its label, then
// its key cap when k isn't "". A click asks for another frame, since what it
// changes was partly drawn already.
func (u *ui) kbtn(gtx layout.Context, id string, kind kit.Kind, label, k string) (layout.Dimensions, bool) {
	fg, fill, hover, rim := cText, cPanelCard, cHover, cBtnRim
	switch kind {
	case kit.Primary:
		fg, fill, hover, rim = cWhite, cAcc, mix(cAcc, cWhite, 0.12), color.NRGBA{}
	case kit.Danger:
		fg, fill, hover, rim = cWhite, cDanger, mix(cDanger, cWhite, 0.12), color.NRGBA{}
	case kit.Ghost:
		fg, fill, rim = cText2, color.NRGBA{}, color.NRGBA{}
	}
	onFill := kind == kit.Primary || kind == kit.Danger
	d, ok := u.rimButton(gtx, id, fill, hover, rim, rCtl, layout.Inset{Top: 7, Bottom: 7, Left: 12, Right: 12}, func(gtx layout.Context) layout.Dimensions {
		ch := []layout.FlexChild{layout.Rigid(u.lbl(label, 13, fg, fSemi(sansFont)))}
		if k != "" {
			ch = append(ch, layout.Rigid(hgap(8)), layout.Rigid(u.keyCap(k, onFill)))
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, ch...)
	})
	if ok {
		gtx.Execute(op.InvalidateCmd{})
	}
	return d, ok
}

// kb is kbtn as a row's child that runs f when clicked.
func (u *ui) kb(id string, kind kit.Kind, label, k string, f func()) layout.FlexChild {
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		d, ok := u.kbtn(gtx, id, kind, label, k)
		if ok {
			f()
		}
		return d
	})
}

// spacer takes a row's free width.
func spacer() layout.FlexChild {
	return layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} })
}

// iconTile is a dialog's icon on a rounded square: sz the square, isz the icon.
func iconTile(sz, isz, rad unit.Dp, bg, fg color.NRGBA, ico func(p *clip.Path, s float32)) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		s := gtx.Dp(sz)
		panel(gtx, image.Rect(0, 0, s, s), rad, bg, color.NRGBA{})
		o := (s - gtx.Dp(isz)) / 2
		st := op.Offset(image.Pt(o, o)).Push(gtx.Ops)
		icon(isz, fg, ico)(gtx)
		st.Pop()
		return layout.Dimensions{Size: image.Pt(s, s)}
	}
}

// rich lays out spans of different styles as one wrapping text.
func (u *ui) rich(spans ...styledtext.SpanStyle) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Point{}
		return styledtext.Text(u.sh, spans...).Layout(gtx, nil)
	}
}

func span(s string, sp unit.Sp, c color.NRGBA, f font.Font) styledtext.SpanStyle {
	return styledtext.SpanStyle{Content: s, Size: sp, Color: c, Font: f}
}

func (u *ui) btn(gtx layout.Context, id, s string, fg, fill color.NRGBA) bool {
	_, ok := u.button(gtx, id, fill, cHover, rCtl, layout.Inset{Top: 5, Bottom: 5, Left: 12, Right: 12}, u.lbl(s, 13, fg, fSemi(sansFont)))
	return ok
}

func dot(c color.NRGBA, halo bool) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		s := gtx.Dp(7)
		if halo {
			h := gtx.Dp(13)
			hc := c
			hc.A = 0x30
			paint.FillShape(gtx.Ops, hc, clip.Ellipse{Max: image.Pt(h, h)}.Op(gtx.Ops))
			o := (h - s) / 2
			paint.FillShape(gtx.Ops, c, clip.Ellipse{Min: image.Pt(o, o), Max: image.Pt(o+s, o+s)}.Op(gtx.Ops))
			return layout.Dimensions{Size: image.Pt(h, h)}
		}
		paint.FillShape(gtx.Ops, c, clip.Ellipse{Max: image.Pt(s, s)}.Op(gtx.Ops))
		return layout.Dimensions{Size: image.Pt(s, s)}
	}
}

// icon draws a small stroked glyph from a path function in a dp box.
func icon(sz unit.Dp, c color.NRGBA, draw func(p *clip.Path, s float32)) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		px := gtx.Dp(sz)
		var p clip.Path
		p.Begin(gtx.Ops)
		draw(&p, float32(px))
		paint.FillShape(gtx.Ops, c, clip.Stroke{Path: p.End(), Width: float32(gtx.Dp(1.6))}.Op())
		return layout.Dimensions{Size: image.Pt(px, px)}
	}
}

func rectPath(p *clip.Path, x0, y0, x1, y1 float32) {
	p.MoveTo(f32.Pt(x0, y0))
	p.LineTo(f32.Pt(x1, y0))
	p.LineTo(f32.Pt(x1, y1))
	p.LineTo(f32.Pt(x0, y1))
	p.Close()
}

var (
	icoSplit = func(p *clip.Path, s float32) {
		rectPath(p, s*.12, s*.18, s*.88, s*.82)
		p.MoveTo(f32.Pt(s*.12, s*.5))
		p.LineTo(f32.Pt(s*.88, s*.5))
	}
	icoGrid = func(p *clip.Path, s float32) {
		for _, o := range [][2]float32{{.12, .12}, {.56, .12}, {.12, .56}, {.56, .56}} {
			rectPath(p, s*o[0], s*o[1], s*(o[0]+.32), s*(o[1]+.32))
		}
	}
	icoPanel = func(p *clip.Path, s float32) {
		rectPath(p, s*.12, s*.18, s*.88, s*.82)
		p.MoveTo(f32.Pt(s*.62, s*.18))
		p.LineTo(f32.Pt(s*.62, s*.82))
	}
	icoSearch = func(p *clip.Path, s float32) {
		c := f32.Pt(s*.44, s*.44)
		p.MoveTo(f32.Pt(c.X+s*.28, c.Y))
		p.ArcTo(c, c, 6.2832)
		p.MoveTo(f32.Pt(s*.64, s*.64))
		p.LineTo(f32.Pt(s*.88, s*.88))
	}
	icoUp = func(p *clip.Path, s float32) {
		p.MoveTo(f32.Pt(s*.5, s*.82))
		p.LineTo(f32.Pt(s*.5, s*.2))
		p.MoveTo(f32.Pt(s*.22, s*.46))
		p.LineTo(f32.Pt(s*.5, s*.18))
		p.LineTo(f32.Pt(s*.78, s*.46))
	}
	icoBroadcast = func(p *clip.Path, s float32) { // one input fanning out to three
		p.MoveTo(f32.Pt(s*.1, s*.5))
		p.LineTo(f32.Pt(s*.42, s*.5))
		for _, y := range []float32{.18, .5, .82} {
			p.MoveTo(f32.Pt(s*.42, s*.5))
			p.LineTo(f32.Pt(s*.9, s*y))
		}
	}
	icoStop  = func(p *clip.Path, s float32) { rectPath(p, s*.26, s*.26, s*.74, s*.74) }
	icoSpark = func(p *clip.Path, s float32) {
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
	icoShield = func(p *clip.Path, s float32) {
		p.MoveTo(f32.Pt(s*.5, s*.1))
		p.LineTo(f32.Pt(s*.85, s*.24))
		p.LineTo(f32.Pt(s*.85, s*.5))
		p.QuadTo(f32.Pt(s*.82, s*.82), f32.Pt(s*.5, s*.92))
		p.QuadTo(f32.Pt(s*.18, s*.82), f32.Pt(s*.15, s*.5))
		p.LineTo(f32.Pt(s*.15, s*.24))
		p.Close()
		p.MoveTo(f32.Pt(s*.5, s*.34))
		p.LineTo(f32.Pt(s*.5, s*.58))
		p.MoveTo(f32.Pt(s*.5, s*.68))
		p.LineTo(f32.Pt(s*.5, s*.72))
	}
	icoChevron = func(p *clip.Path, s float32) {
		p.MoveTo(f32.Pt(s*.25, s*.38))
		p.LineTo(f32.Pt(s*.5, s*.62))
		p.LineTo(f32.Pt(s*.75, s*.38))
	}
	icoChevronRight = func(p *clip.Path, s float32) {
		p.MoveTo(f32.Pt(s*.38, s*.25))
		p.LineTo(f32.Pt(s*.62, s*.5))
		p.LineTo(f32.Pt(s*.38, s*.75))
	}
	icoStar = func(p *clip.Path, s float32) {
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
	icoPlus = func(p *clip.Path, s float32) {
		p.MoveTo(f32.Pt(s*.5, s*.2))
		p.LineTo(f32.Pt(s*.5, s*.8))
		p.MoveTo(f32.Pt(s*.2, s*.5))
		p.LineTo(f32.Pt(s*.8, s*.5))
	}
	icoServer = func(p *clip.Path, s float32) { // two stacked units
		rectPath(p, s*.12, s*.18, s*.88, s*.44)
		rectPath(p, s*.12, s*.56, s*.88, s*.82)
	}
	icoNoLink = func(p *clip.Path, s float32) { // signal arcs, struck through
		for _, r := range []float32{.42, .26} {
			c := f32.Pt(s*.5, s*.82)
			p.MoveTo(f32.Pt(c.X-s*r*.87, c.Y-s*r*.5))
			p.ArcTo(c, c, 2.0944)
		}
		p.MoveTo(f32.Pt(s*.12, s*.12))
		p.LineTo(f32.Pt(s*.88, s*.88))
	}
	icoX = func(p *clip.Path, s float32) {
		p.MoveTo(f32.Pt(s*.28, s*.28))
		p.LineTo(f32.Pt(s*.72, s*.72))
		p.MoveTo(f32.Pt(s*.72, s*.28))
		p.LineTo(f32.Pt(s*.28, s*.72))
	}
)

func (u *ui) setClipboard(s string) {
	select {
	case u.clipOut <- s:
	default:
	}
	u.inv()
}

// flushClipboard writes text queued from other goroutines; Gio only takes
// clipboard commands inside a frame.
func (u *ui) flushClipboard(gtx layout.Context) {
	for {
		select {
		case s := <-u.clipOut:
			gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(s))})
		default:
			return
		}
	}
}

func (u *ui) showToast(s string, bad bool) {
	u.toast, u.toastBad = s, bad
	u.toastUntil = time.Now().Add(map[bool]time.Duration{false: 2500 * time.Millisecond, true: 6 * time.Second}[bad])
}

// editor draws a widget.Editor with a hint when empty.
func (u *ui) editor(gtx layout.Context, e *widget.Editor, hint string, sp unit.Sp, f font.Font, c color.NRGBA) layout.Dimensions {
	if e.Len() == 0 {
		m := op.Record(gtx.Ops)
		u.label(gtx, hint, sp, cText3, f, 1)
		hintCall := m.Stop()
		d := e.Layout(gtx, u.sh, f, sp, mat(gtx, c), mat(gtx, cAccSoft))
		hintCall.Add(gtx.Ops)
		return d
	}
	return e.Layout(gtx, u.sh, f, sp, mat(gtx, c), mat(gtx, cAccSoft))
}
