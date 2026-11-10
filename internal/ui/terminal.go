package ui

import (
	"cmp"
	"image"
	"image/color"
	"io"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/image/math/fixed"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/term"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// termTab is a card's Claude Code TUI, open in its own view ("term:<id>")
// while desk runs: the card's session, never two processes on it at once.
type termTab struct {
	t          *term.Term
	scroll     int // lines up into the scrollback, 0 the live screen
	cols, rows int
	focus      bool // take the keyboard on the next frame
	pasteText  bool // Ctrl+Shift+V found no image: paste the clipboard's text
	fixedCols  int  // keep this width whatever the space (the sign-in's URL on one row); 0 follows it
	glyphs     []text.Glyph
}

// startTerm is how a terminal starts; tests replace it.
var startTerm = term.Start

// terminal opens card id's session in the full Claude Code TUI, in its
// view: desk lets go of the card's -p process first. Again, it goes there.
func (u *ui) terminal(id string) {
	c := u.b.Card(id)
	if c == nil || !c.Archived.IsZero() {
		return
	}
	if tt := u.terms[id]; tt != nil {
		u.showTerm(id)
		return
	}
	if p := u.procs[id]; p != nil {
		p.Close()
		delete(u.procs, id)
	}
	if c.Session.ID == "" {
		c.Session.ID = claude.NewSessionID()
	}
	var cmd *exec.Cmd
	extra, err := u.agentArgs(c)
	if c.Remote {
		extra = append(extra, "--remote-control", c.Key()+" "+c.Title)
	}
	system := claude.System(c.Key(), c.Title, c.Org) + cardLang(c)
	if u.cardSock != "" {
		system += claude.CardTools
	}
	if c.Alert != "" {
		system += claude.AlertSnooze
	}
	if err == nil {
		cmd, err = claude.Interactive(u.home, orgs.Account(u.root, c.Org), orgs.Dir(u.root, c.Org, c.Dir), orgs.Dir(u.root, c.Org, ""), c.Session.ID, system, extra...)
	}
	if err == nil {
		var t *term.Term
		var started atomic.Pointer[term.Term]
		if t, err = startTerm(cmd, 120, 40, func() { u.termNotify(id, started.Load()) }); err == nil {
			started.Store(t)
			u.terms[id] = &termTab{t: t}
		}
	}
	if err != nil {
		u.showToast("Terminal: "+err.Error(), true)
		return
	}
	c.Status, c.Note, c.Updated = kanban.StatusWaiting, "In its terminal", time.Now()
	u.save()
	u.showTerm(id)
}

// termNotify redraws on card id's terminal output, from its reader
// goroutine, when it is the one shown (a Claude Code at work repaints its
// status line many times a second: the window needn't, from the board),
// or when it exited, for reapTerms. frame asks again when it came
// mid-frame (Gio drops that Invalidate).
func (u *ui) termNotify(id string, t *term.Term) {
	if u.shownTerm.Load() != id {
		if t == nil {
			return
		}
		if done, _ := t.Exited(); !done {
			return
		}
	}
	u.termGen.Add(1)
	u.w.Invalidate()
}

func (u *ui) showTerm(id string) {
	u.view, u.sel, u.panel = "term:"+id, "", ""
	u.terms[id].focus = true
}

// closeTerm hangs card id's terminal up; its card goes back to the board.
func (u *ui) closeTerm(id string) {
	tt := u.terms[id]
	if tt == nil {
		return
	}
	tt.t.Close()
	u.dropTerm(id, "Terminal closed")
}

// remoteControl turns card id's Remote Control on or off. Only an
// interactive Claude serves it (a -p stream-json one ignores the flag), so
// it runs in the card's terminal: turned on, the terminal (re)starts with it
// and shows the link and QR code; turned off, an open one restarts without.
func (u *ui) remoteControl(id string) {
	c := u.b.Card(id)
	if c == nil || !c.Archived.IsZero() {
		return
	}
	c.Remote = !c.Remote
	u.save()
	tt := u.terms[id]
	if tt != nil {
		tt.t.Close()
		delete(u.terms, id)
	}
	if c.Remote || tt != nil {
		u.terminal(id)
	}
	if c.Remote {
		u.showToast("Remote Control on: open the link or scan the QR code in its terminal", false)
	} else {
		u.showToast("Remote Control off", false)
	}
}

func (u *ui) dropTerm(id, note string) {
	delete(u.terms, id)
	if u.view == "term:"+id {
		u.view, u.blur = "", true
	}
	if c := u.b.Card(id); c != nil {
		c.Status, c.Note, c.Updated = kanban.StatusWaiting, note, time.Now()
		u.save()
	}
}

// reapTerms drops the terminals whose Claude exited (/exit, a crash).
func (u *ui) reapTerms() {
	for id, tt := range u.terms {
		if done, code := tt.t.Exited(); done {
			note := "Terminal closed"
			if code != 0 && code != -1 {
				note = "Claude exited in its terminal"
			}
			u.dropTerm(id, note)
		}
	}
}

// termCard is the card the view's terminal belongs to, if one is shown.
func (u *ui) termCard() (string, *termTab) {
	id, ok := strings.CutPrefix(u.view, "term:")
	if !ok {
		return "", nil
	}
	return id, u.terms[id]
}

// termView is the shown terminal: a header with its card, then the screen.
func (u *ui) termView(gtx layout.Context) layout.Dimensions {
	k := u.k
	id, tt := u.termCard()
	c := u.b.Card(id)
	gtx.Constraints.Min = gtx.Constraints.Max
	kit.Fill(gtx, kit.CPane)
	if tt == nil || c == nil {
		return layout.Center.Layout(gtx, k.Lbl("This terminal was closed.", 13, kit.CText3, kit.Sans))
	}
	var act func()
	d := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Inset{Top: 6, Bottom: 6, Left: 14, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(kit.Icon(14, kit.CText3, kit.IcoTerminal)),
					layout.Rigid(kit.HGap(8)),
					layout.Rigid(k.Lbl(c.Key(), 12, kit.CAccText, kit.Mono)),
					layout.Rigid(kit.HGap(8)),
					layout.Flexed(1, k.LblFill(c.Title, 13, kit.CText, kit.Semi(kit.Sans))),
					layout.Rigid(k.Lbl(strings.Replace(orgs.Dir(u.root, c.Org, c.Dir), u.home, "~", 1), 11, kit.CText3, kit.Sans)),
					layout.Rigid(kit.HGap(10)),
					layout.Rigid(k.Pill("Claude · "+u.accountName(c.Org), kit.CText2, kit.CPanelCard)),
					layout.Rigid(kit.HGap(10)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						label, fg, bg := "Remote", kit.CText, kit.CPanelCard
						if c.Remote {
							label, fg, bg = "Remote · on", kit.CGreen, kit.Mix(kit.CGreen, kit.CPane, .85)
						}
						if k.Btn(gtx, "termRemote", label, fg, bg) {
							act = func() { u.remoteControl(id) }
						}
						return layout.Dimensions{Size: image.Pt(gtx.Dp(96), gtx.Dp(26))}
					}),
					layout.Rigid(kit.HGap(6)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if k.Btn(gtx, "termChat", "Chat", kit.CText, kit.CPanelCard) {
							act = func() { u.openSession(id) } // the terminal keeps running
						}
						return layout.Dimensions{Size: image.Pt(gtx.Dp(64), gtx.Dp(26))}
					}),
					layout.Rigid(kit.HGap(6)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if k.Btn(gtx, "termClose", "Close", kit.CText, kit.CPanelCard) {
							act = func() { u.closeTerm(id) }
						}
						return layout.Dimensions{Size: image.Pt(gtx.Dp(72), gtx.Dp(26))}
					}))
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, gtx.Dp(1))}
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			u.termGrid(gtx, tt)
			return layout.Dimensions{Size: gtx.Constraints.Max}
		}))
	if act != nil { // after drawing: Close drops tt
		act()
	}
	return d
}

// termGrid draws tt's screen in the space given and feeds it the input.
func (u *ui) termGrid(gtx layout.Context, tt *termTab) {
	gtx = kit.Zoomed(gtx, orgs.Zoom()) // the cells follow the text: a zoom resizes the program
	sh := u.k.Sh
	size := fixed.I(gtx.Sp(14))
	cw, chh, asc := cellMetrics(sh, kit.Mono, size, &tt.glyphs)
	rowH := int(float32(chh) * 1.25)
	area := image.Rectangle{Max: gtx.Constraints.Max}
	inner := image.Rect(gtx.Dp(10), gtx.Dp(6), area.Max.X-gtx.Dp(10), area.Max.Y-gtx.Dp(4))
	if c, r := cmp.Or(tt.fixedCols, inner.Dx()/max(cw, 1)), inner.Dy()/max(rowH, 1); (c != tt.cols || r != tt.rows) && c >= 20 && r >= 3 {
		tt.cols, tt.rows = c, r
		tt.t.Resize(c, r)
	}
	if tt.focus && u.edit == nil && u.unlock == nil && u.cfg == nil {
		gtx.Execute(key.FocusCmd{Tag: tt})
		tt.focus = false
	}
	u.termEvents(gtx, tt, inner, cw, rowH)

	cl := clip.Rect(area).Push(gtx.Ops)
	defer cl.Pop()
	runs, cx, cy, cursor := tt.t.Grid(tt.scroll)
	for k, row := range runs {
		py := inner.Min.Y + k*rowH
		for _, r := range row {
			fg, bg := cellColor(r.Style.Fg, kit.CText), cellColor(r.Style.Bg, color.NRGBA{})
			if r.Style.Attrs&uv.AttrReverse != 0 {
				fg, bg = cellColor(r.Style.Bg, kit.CPane), cellColor(r.Style.Fg, kit.CText)
			}
			if r.Style.Attrs&uv.AttrFaint != 0 {
				fg.A = 0x90
			}
			f := kit.Mono
			if r.Style.Attrs&uv.AttrBold != 0 {
				f.Weight = font.Bold
			}
			if r.Style.Attrs&uv.AttrItalic != 0 {
				f.Style = font.Italic
			}
			x := inner.Min.X + r.X*cw
			if bg.A != 0 {
				paint.FillShape(gtx.Ops, bg, clip.Rect(image.Rect(x, py, x+r.W*cw, py+rowH)).Op())
			}
			if txt := strings.TrimRight(r.Text, " "); txt != "" { // a row's padding needn't be shaped
				drawRun(gtx.Ops, sh, f, size, txt, x, py+(rowH-chh)/2+asc, cw, fg, &tt.glyphs)
			}
		}
	}
	if cursor && cy < len(runs) {
		x, top := inner.Min.X+cx*cw, inner.Min.Y+cy*rowH+(rowH-chh)/2
		c := kit.CAcc
		if !gtx.Focused(tt) {
			c.A = 0x60
		}
		paint.FillShape(gtx.Ops, c, clip.Rect(image.Rect(x, top, x+cw, top+chh)).Op())
	}
}

// termEvents feeds keys, paste (Ctrl+Shift+V, or a drop; Ctrl+V too for an
// image) and the wheel to
// tt. ponytail: no mouse selection or copy yet; Claude Code's own /copy
// covers its replies.
func (u *ui) termEvents(gtx layout.Context, tt *termTab, inner image.Rectangle, cw, rowH int) {
	mods := key.ModCtrl | key.ModShift | key.ModAlt | key.ModSuper | key.ModCommand
	if tt.pasteText {
		tt.pasteText = false
		gtx.Execute(clipboard.ReadCmd{Tag: tt})
	}
	for {
		ev, ok := gtx.Event(
			key.FocusFilter{Target: tt},
			key.Filter{Focus: tt, Name: "", Optional: mods},
			key.Filter{Focus: tt, Name: key.NameTab, Optional: mods},
			pointer.Filter{Target: tt, Kinds: pointer.Press | pointer.Scroll, ScrollY: pointer.ScrollRange{Min: -1e6, Max: 1e6}},
			transfer.TargetFilter{Target: tt, Type: "application/text"},
		)
		if !ok {
			break
		}
		switch ev := ev.(type) {
		case key.EditEvent:
			tt.scroll = 0
			for _, r := range ev.Text {
				tt.t.SendKey(uv.KeyPressEvent{Code: r, Text: string(r)})
			}
		case key.Event:
			if ev.State == key.Press && ev.Name == "V" && ev.Modifiers&(key.ModCtrl|key.ModCommand) != 0 { // an image goes in as its saved file, which Claude Code attaches
				k, raw := termKey(ev)
				shift := ev.Modifiers&(key.ModShift|key.ModCommand) != 0 // ⌘V is macOS's Ctrl+Shift+V: text when no image
				go func() {
					b, typ, err := clipImage()
					u.later(func() {
						switch {
						case err == nil && b != nil:
							p, err := u.saveImage(b, typ)
							if err != nil {
								u.showToast("Couldn't keep the image: "+err.Error(), true)
								return
							}
							tt.scroll = 0
							tt.t.Paste(p)
						case shift: // text: read it next frame
							tt.pasteText = true
						case raw:
							tt.t.SendKey(k)
						}
					})
				}()
				continue
			}
			if k, ok := termKey(ev); ok {
				tt.scroll = 0
				tt.t.SendKey(k)
			}
		case pointer.Event:
			switch ev.Kind {
			case pointer.Press:
				gtx.Execute(key.FocusCmd{Tag: tt})
			case pointer.Scroll:
				lines := int(ev.Scroll.Y / float32(rowH/3+1))
				if lines == 0 {
					lines = map[bool]int{true: 1, false: -1}[ev.Scroll.Y > 0]
				}
				p := ev.Position.Round()
				x, y := max(0, (p.X-inner.Min.X)/max(cw, 1)), max(0, (p.Y-inner.Min.Y)/max(rowH, 1))
				btn := uv.MouseWheelDown
				if lines < 0 {
					btn = uv.MouseWheelUp
				}
				if !tt.t.WheelToApp(uv.MouseWheelEvent{X: x, Y: y, Button: btn}) {
					tt.scroll = max(0, min(tt.t.Scrollback(), tt.scroll-lines))
				}
			}
		case transfer.DataEvent:
			if r := ev.Open(); r != nil {
				b, _ := io.ReadAll(r)
				r.Close()
				tt.t.Paste(string(b))
			}
		}
	}
	st := clip.Rect(inner.Inset(-gtx.Dp(8))).Push(gtx.Ops)
	event.Op(gtx.Ops, tt)
	pointer.CursorText.Add(gtx.Ops)
	st.Pop()
}

// termKey turns a Gio key press into the terminal key it means. Printable
// text comes as EditEvent instead, except with Ctrl/Alt held.
func termKey(e key.Event) (uv.KeyPressEvent, bool) {
	if e.State != key.Press {
		return uv.KeyPressEvent{}, false
	}
	var k uv.KeyPressEvent
	if e.Modifiers.Contain(key.ModCtrl) {
		k.Mod |= uv.ModCtrl
	}
	if e.Modifiers.Contain(key.ModAlt) {
		k.Mod |= uv.ModAlt
	}
	if e.Modifiers.Contain(key.ModShift) {
		k.Mod |= uv.ModShift
	}
	if e.Name == key.NameSpace && k.Mod&(uv.ModCtrl|uv.ModAlt) == 0 {
		return uv.KeyPressEvent{}, false // a plain space also arrives as EditEvent text
	}
	if c, ok := specialKeys[e.Name]; ok {
		k.Code = c
		if c != uv.KeyTab && c != uv.KeyEnter { // shift+tab, and shift+enter (Claude Code's newline)
			k.Mod &^= uv.ModShift
		}
		return k, true
	}
	if k.Mod&(uv.ModCtrl|uv.ModAlt) == 0 {
		return uv.KeyPressEvent{}, false
	}
	r := []rune(strings.ToLower(string(e.Name)))
	if len(r) != 1 {
		return uv.KeyPressEvent{}, false
	}
	k.Code, k.Mod = r[0], k.Mod&^uv.ModShift
	if k.Mod&uv.ModCtrl == 0 {
		k.Text = string(r[0])
	}
	return k, true
}

var specialKeys = map[key.Name]rune{
	key.NameReturn: uv.KeyEnter, key.NameEnter: uv.KeyEnter, key.NameEscape: uv.KeyEscape, key.NameTab: uv.KeyTab,
	key.NameDeleteBackward: uv.KeyBackspace, key.NameDeleteForward: uv.KeyDelete, key.NameUpArrow: uv.KeyUp,
	key.NameDownArrow: uv.KeyDown, key.NameLeftArrow: uv.KeyLeft, key.NameRightArrow: uv.KeyRight,
	key.NameHome: uv.KeyHome, key.NameEnd: uv.KeyEnd, key.NamePageUp: uv.KeyPgUp, key.NamePageDown: uv.KeyPgDown,
	key.NameSpace: uv.KeySpace, key.NameF1: uv.KeyF1, key.NameF2: uv.KeyF2, key.NameF3: uv.KeyF3, key.NameF4: uv.KeyF4,
	key.NameF5: uv.KeyF5, key.NameF6: uv.KeyF6, key.NameF7: uv.KeyF7, key.NameF8: uv.KeyF8, key.NameF9: uv.KeyF9,
	key.NameF10: uv.KeyF10, key.NameF11: uv.KeyF11, key.NameF12: uv.KeyF12,
}

var ansi16 = [16]color.NRGBA{
	kit.RGB(0x3a3a3c), kit.RGB(0xff6961), kit.RGB(0x30d158), kit.RGB(0xffd60a), kit.RGB(0x409cff), kit.RGB(0xda8fff), kit.RGB(0x70d7ff), kit.RGB(0xd1d1d6),
	kit.RGB(0x636366), kit.RGB(0xff8a84), kit.RGB(0x63e07f), kit.RGB(0xffe45c), kit.RGB(0x6bb5ff), kit.RGB(0xe5a8ff), kit.RGB(0x8fe3ff), kit.RGB(0xf5f5f7),
}

// cellColor is a cell's color in the window: the 16 ANSI ones from desk's
// palette, the rest as they are; nil is def.
func cellColor(c color.Color, def color.NRGBA) color.NRGBA {
	switch c := c.(type) {
	case nil:
		return def
	case ansi.BasicColor:
		return ansi16[c&15]
	case ansi.IndexedColor:
		if c < 16 {
			return ansi16[c]
		}
	}
	r, g, b, a := c.RGBA()
	return color.NRGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
}

// Text for the grid: runs snapped to cells, in chunks under Gio's 4096px
// rasterization limit.

func cellMetrics(sh *text.Shaper, f font.Font, size fixed.Int26_6, gl *[]text.Glyph) (w, h, ascent int) {
	sh.LayoutString(text.Parameters{Font: f, PxPerEm: size, MaxWidth: 1 << 24}, "M")
	g, _ := sh.NextGlyph()
	for _, ok := sh.NextGlyph(); ok; _, ok = sh.NextGlyph() {
	}
	return g.Advance.Round(), (g.Ascent + g.Descent).Ceil(), g.Ascent.Ceil()
}

func drawRun(ops *op.Ops, sh *text.Shaper, f font.Font, size fixed.Int26_6, s string, x, baseline, cw int, c color.NRGBA, gl *[]text.Glyph) {
	if r := []rune(s); len(r) > 128 {
		drawRun(ops, sh, f, size, string(r[:128]), x, baseline, cw, c, gl)
		drawRun(ops, sh, f, size, string(r[128:]), x+128*cw, baseline, cw, c, gl)
		return
	}
	sh.LayoutString(text.Parameters{Font: f, PxPerEm: size, MaxWidth: 1 << 24, MaxLines: 1}, s)
	*gl = (*gl)[:0]
	for g, ok := sh.NextGlyph(); ok; g, ok = sh.NextGlyph() {
		*gl = append(*gl, g)
	}
	if len(*gl) == 0 {
		return
	}
	for i := range *gl {
		(*gl)[i].X = fixed.I(i * cw)
	}
	t := op.Offset(image.Pt(x, baseline)).Push(ops)
	o := clip.Outline{Path: sh.Shape(*gl)}.Op().Push(ops)
	paint.ColorOp{Color: c}.Add(ops)
	paint.PaintOp{}.Add(ops)
	o.Pop()
	if call := sh.Bitmaps(*gl); call != (op.CallOp{}) {
		call.Add(ops)
	}
	t.Pop()
}
