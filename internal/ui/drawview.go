package ui

import (
	"fmt"
	"image"
	"math"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/draw"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// drawState is an open drawing: its editor, how it is looked at, the
// pointer's press, the text being typed.
type drawState struct {
	org, path string
	ed        *draw.Editor
	written   []byte // the file as last written
	failed    int    // the editor's Gen a save failed at
	title     widget.Editor
	titleFoc  bool
	text      widget.Editor // the text element being typed (ed.Editing)
	typing    string        // the element text holds, "" none
	textFoc   bool

	pan     f32.Point // px: where the scene's origin is in the pane
	zoom    float32   // 1: a scene unit is a dp
	placed  bool      // pan and zoom set for the first look
	down    bool      // the editor has the press
	panning bool
	space   bool // held: a drag pans
	last    f32.Point
	ptr     f32.Point
	pressAt time.Time // the last press, for a double click
	pressP  f32.Point

	shown    *draw.Scene // the scene themed (see themed), for ed.Gen
	shownGen int
	rasters  map[string]*drawRaster
	styles   layout.List // the style panel, scrolled when the window is short
}

const (
	drawZoomMin, drawZoomMax = .1, 30
	drawTol                  = 6 // px: how near the pointer counts as on something
)

func (st *drawState) toScene(p f32.Point, px float32) draw.Pt {
	q := p.Sub(st.pan).Div(st.zoom * px)
	return draw.Pt{X: float64(q.X), Y: float64(q.Y)}
}

func (st *drawState) toPane(p draw.Pt, px float32) f32.Point {
	return f32.Pt(float32(p.X), float32(p.Y)).Mul(st.zoom * px).Add(st.pan)
}

// zoomAt scales by f around pane point p, which stays put.
func (st *drawState) zoomAt(p f32.Point, f float32) {
	z := min(max(st.zoom*f, drawZoomMin), drawZoomMax)
	st.pan = p.Sub(p.Sub(st.pan).Mul(z / st.zoom))
	st.zoom = z
}

// fit shows the whole drawing in a pane of size, never past 100%; an
// empty one at 100%, its origin near the top left.
func (st *drawState) fit(size image.Point, px float32) {
	x0, y0, x1, y1 := st.ed.Scene.Bounds()
	if x0 > x1 {
		st.zoom, st.pan = 1, f32.Pt(80*px, 80*px)
		return
	}
	m := 60 * px
	w, h := float32(x1-x0)*px, float32(y1-y0)*px
	st.zoom = min(max(min((float32(size.X)-2*m)/max(w, 1), (float32(size.Y)-2*m)/max(h, 1), 1), drawZoomMin), drawZoomMax)
	s := st.zoom * px
	st.pan = f32.Pt((float32(size.X)-float32(x1-x0)*s)/2-float32(x0)*s, (float32(size.Y)-float32(y1-y0)*s)/2-float32(y0)*s)
}

func (st *drawState) dark() bool { return st.ed.Scene.AppState["theme"] == "dark" }

// drawSig is the open drawing's state a frame draws from, for sig.
func (u *ui) drawSig() string {
	st := u.drawOpen()
	if st == nil {
		return ""
	}
	return fmt.Sprint(st.ed.Gen, st.ed.Tool, st.ed.Editing, st.pan, st.zoom, st.space)
}

// drawOpen is the drawing in view, nil when none is.
func (u *ui) drawOpen() *drawState {
	if p, ok := strings.CutPrefix(u.view, "draw:"); ok {
		return u.draws[p]
	}
	return nil
}

// drawTyping reports the open drawing's editors having the keyboard.
func (u *ui) drawTyping(gtx layout.Context) bool {
	st := u.drawOpen()
	return st != nil && (gtx.Focused(&st.text) || gtx.Focused(&st.title))
}

// drawEscape backs out of the drawing one step: the text being typed,
// the tool, the selection, then to the list. No blur: Gio drops the focus
// of an editor no longer laid out (the text box, the list's view).
func (u *ui) drawEscape(st *drawState) {
	switch {
	case st.ed.Editing != "":
		st.ed.EndText()
	case st.ed.Tool != draw.ToolSelect:
		st.ed.SetTool(draw.ToolSelect)
	case len(st.ed.Sel) > 0:
		st.ed.Select()
	default:
		u.view = "dr:" + st.org
	}
}

// drawView is the draw:<path> view: the header, then the canvas with its
// tools over it.
func (u *ui) drawView(gtx layout.Context) layout.Dimensions {
	gtx.Constraints.Min = gtx.Constraints.Max
	kit.Fill(gtx, kit.CColumn)
	st := u.drawOpen()
	if st == nil {
		return layout.Center.Layout(gtx, u.k.Lbl("This drawing is gone.", 13, kit.CText3, kit.Sans))
	}
	var act func()
	defer func() {
		if act != nil {
			act()
		}
		u.drawSave(st)
	}()
	k := u.k
	header := func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		kit.Fill(gtx, kit.CPanel)
		return layout.Inset{Top: 6, Bottom: 6, Left: 10, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.Button(gtx, "drawBack", kit.CPanel, kit.CHover, kit.RCtl, layout.Inset{Top: 4, Bottom: 4, Left: 8, Right: 10}, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(kit.Icon(14, kit.CText2, kit.IcoBoard)), layout.Rigid(kit.HGap(6)),
							layout.Rigid(k.Lbl(st.org+" · Drawings", 12, kit.CText2, kit.Medium(kit.Sans))))
					})
					if ok {
						act = func() { u.drawEscapeAll(st) }
					}
					return d
				}),
				layout.Rigid(kit.HGap(12)),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					for {
						ev, ok := st.title.Update(gtx)
						if !ok {
							break
						}
						if _, ok := ev.(widget.SubmitEvent); ok {
							act = func() { u.renameDrawing(st, st.title.Text()); u.blur = true }
						}
					}
					foc := gtx.Focused(&st.title)
					if st.titleFoc && !foc && act == nil {
						act = func() { u.renameDrawing(st, st.title.Text()) }
					}
					st.titleFoc = foc
					return k.Editor(gtx, &st.title, "Name", 14, kit.Semi(kit.Sans), kit.CText)
				}))
		})
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(header),
		layout.Rigid(kit.VGap(1)),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return u.drawCanvas(gtx, st, &act) }))
}

// drawEscapeAll leaves the drawing for its list, the text typed kept.
func (u *ui) drawEscapeAll(st *drawState) {
	st.ed.EndText()
	u.view = "dr:" + st.org
}

// drawSave writes the drawing once a change settles (no press or typing
// under way) and differs from the file.
func (u *ui) drawSave(st *drawState) {
	if st.ed.Busy() || st.ed.Editing != "" || st.failed == st.ed.Gen {
		return
	}
	b, err := st.ed.Scene.Marshal()
	if err != nil || string(b) == string(st.written) {
		return
	}
	if err := st.ed.Scene.Save(st.path); err != nil {
		u.showToast("Couldn't save the drawing: "+err.Error(), true)
		st.failed = st.ed.Gen // no retry every frame; the next change tries again
		return
	}
	st.written = b
	u.drawRead(st.org)
}

// drawCanvas is the drawing itself: the scene, the selection, the
// pointer and keys, then the tools.
func (u *ui) drawCanvas(gtx layout.Context, st *drawState, act *func()) layout.Dimensions {
	size := gtx.Constraints.Max
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	px := gtx.Metric.PxPerDp
	if st.rasters == nil {
		st.rasters = map[string]*drawRaster{}
	}
	if !st.placed {
		st.fit(size, px)
		st.placed = true
	}
	u.drawKeys(gtx, st, size)
	u.drawPointer(gtx, st, px)
	ed := st.ed
	if st.shown == nil || st.shownGen != ed.Gen {
		st.shown, st.shownGen = themed(ed.Scene, ed.Erasing), ed.Gen
	}
	paint.FillShape(gtx.Ops, st.shown.Background(), clip.Rect{Max: size}.Op())
	s := st.zoom * px
	t := op.Affine(f32.Affine2D{}.Scale(f32.Point{}, f32.Pt(s, s)).Offset(st.pan)).Push(gtx.Ops)
	st.paintScene(gtx.Ops, st.shown, s, ed.Editing)
	t.Pop()
	u.drawSelection(gtx, st, px)

	hand := clip.Rect{Max: size}.Push(gtx.Ops)
	st.cursor().Add(gtx.Ops)
	event.Op(gtx.Ops, st)
	hand.Pop()
	u.drawTextBox(gtx, st, px)
	u.drawTools(gtx, st, size, act)
	return layout.Dimensions{Size: size}
}

// cursor is the pointer's look for what is under it.
func (st *drawState) cursor() pointer.Cursor {
	ed := st.ed
	switch {
	case st.panning:
		return pointer.CursorGrabbing
	case st.space || ed.Tool == draw.ToolHand:
		return pointer.CursorGrab
	case ed.Tool == draw.ToolText:
		return pointer.CursorText
	case ed.Tool != draw.ToolSelect:
		return pointer.CursorCrosshair
	}
	return pointer.CursorDefault
}

// drawPointer feeds the canvas's pointer to the editor: a drag pans with
// the hand, Space or the middle button; scroll pans, Ctrl/⌘+scroll zooms;
// a double click types text.
func (u *ui) drawPointer(gtx layout.Context, st *drawState, px float32) {
	ed := st.ed
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: st, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel | pointer.Move | pointer.Scroll,
			ScrollX: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}, ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}})
		if !ok {
			return
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		p := e.Position
		sp, tol := st.toScene(p, px), float64(drawTol/st.zoom)
		shift := e.Modifiers.Contain(key.ModShift)
		switch e.Kind {
		case pointer.Move:
			st.ptr = p
		case pointer.Scroll:
			if e.Modifiers.Contain(key.ModShortcut) {
				st.zoomAt(p, wheelZoom(e.Scroll.Y))
			} else {
				st.pan = st.pan.Sub(e.Scroll)
			}
		case pointer.Press:
			if ed.Editing != "" {
				ed.EndText() // its box goes, and the keyboard with it
			}
			if e.Buttons == pointer.ButtonTertiary || (e.Buttons == pointer.ButtonPrimary && (st.space || ed.Tool == draw.ToolHand)) {
				st.panning = true
				gtx.Execute(pointer.GrabCmd{Tag: st, ID: e.PointerID})
				break
			}
			if e.Buttons != pointer.ButtonPrimary {
				break
			}
			double := gtx.Now.Sub(st.pressAt) < 400*time.Millisecond && p.Sub(st.pressP).X*p.Sub(st.pressP).X+p.Sub(st.pressP).Y*p.Sub(st.pressP).Y < 36*px*px
			st.pressAt, st.pressP = gtx.Now, p
			if double && ed.Tool == draw.ToolSelect {
				if hit := ed.HitTest(sp, tol); hit != nil && hit.Type == "text" {
					ed.EditText(hit.ID)
				} else if hit == nil {
					ed.Tool = draw.ToolText
					ed.Down(sp, tol, false)
					ed.Up()
				}
				break
			}
			ed.Down(sp, tol, shift)
			st.down = true
			gtx.Execute(pointer.GrabCmd{Tag: st, ID: e.PointerID})
		case pointer.Drag:
			if st.panning {
				st.pan = st.pan.Add(p.Sub(st.last))
			} else if st.down {
				ed.Drag(sp, tol, shift)
			}
		case pointer.Release, pointer.Cancel:
			if st.down {
				ed.Up()
			}
			st.down, st.panning = false, false
		}
		st.last = p
	}
}

// drawKeys are the editor's keys, Excalidraw's: a tool's letter or digit,
// Delete, Ctrl+Z/Ctrl+Shift+Z (Ctrl+Y), Ctrl+D, Ctrl+A, arrows nudge,
// Ctrl+] and Ctrl+[ (with Shift: to the front, the back), Space held
// pans, Shift+1 fits, Alt+Shift+D the dark canvas. Off while one of
// desk's editors or dialogs has the keyboard.
func (u *ui) drawKeys(gtx layout.Context, st *drawState, size image.Point) {
	if u.drawKeysOff(gtx) {
		return
	}
	mods := key.ModShortcut | key.ModShift | key.ModAlt
	var fs []event.Filter
	for _, n := range []key.Name{"V", "H", "R", "D", "O", "A", "L", "P", "T", "E", "Y", "Z", "[", "]", "0", "1", "2", "3", "4", "5", "6", "7", "8",
		key.NameDeleteBackward, key.NameDeleteForward, key.NameUpArrow, key.NameDownArrow, key.NameLeftArrow, key.NameRightArrow, key.NameSpace, key.NameReturn} {
		fs = append(fs, key.Filter{Name: n, Optional: mods})
	}
	ed := st.ed
	tools := map[key.Name]string{"V": draw.ToolSelect, "1": draw.ToolSelect, "H": draw.ToolHand, "R": draw.ToolRectangle, "2": draw.ToolRectangle,
		"D": draw.ToolDiamond, "3": draw.ToolDiamond, "O": draw.ToolEllipse, "4": draw.ToolEllipse, "A": draw.ToolArrow, "5": draw.ToolArrow,
		"L": draw.ToolLine, "6": draw.ToolLine, "P": draw.ToolFreedraw, "7": draw.ToolFreedraw, "T": draw.ToolText, "8": draw.ToolText,
		"E": draw.ToolEraser, "0": draw.ToolEraser}
	for {
		ev, ok := gtx.Event(fs...)
		if !ok {
			return
		}
		e, ok := ev.(key.Event)
		if !ok {
			continue
		}
		if e.Name == key.NameSpace {
			st.space = e.State == key.Press
			continue
		}
		if e.State != key.Press {
			continue
		}
		ctrl, shift, alt := e.Modifiers.Contain(key.ModShortcut), e.Modifiers.Contain(key.ModShift), e.Modifiers.Contain(key.ModAlt)
		step := 1.0
		if shift {
			step = 10
		}
		switch {
		case ctrl && e.Name == "Z" && shift, ctrl && e.Name == "Y":
			ed.Redo()
		case ctrl && e.Name == "Z":
			ed.Undo()
		case ctrl && e.Name == "D":
			ed.Duplicate()
		case ctrl && e.Name == "A":
			ed.SelectAll()
		case ctrl && e.Name == "]":
			ed.Order(map[bool]int{true: 2, false: 1}[shift])
		case ctrl && e.Name == "[":
			ed.Order(map[bool]int{true: -2, false: -1}[shift])
		case alt && shift && e.Name == "D":
			u.drawToggleTheme(st)
		case shift && e.Name == "1":
			st.fit(size, gtx.Metric.PxPerDp)
		case ctrl || alt:
		case e.Name == key.NameDeleteBackward || e.Name == key.NameDeleteForward:
			ed.Delete()
		case e.Name == key.NameReturn:
			if sel := ed.Selected(); len(sel) == 1 && sel[0].Type == "text" {
				ed.EditText(sel[0].ID)
			}
		case e.Name == key.NameUpArrow:
			ed.Nudge(0, -step)
		case e.Name == key.NameDownArrow:
			ed.Nudge(0, step)
		case e.Name == key.NameLeftArrow:
			ed.Nudge(-step, 0)
		case e.Name == key.NameRightArrow:
			ed.Nudge(step, 0)
		case tools[e.Name] != "":
			ed.SetTool(tools[e.Name])
		}
	}
}

// drawKeysOff is when the drawing's keys are someone else's: a dialog
// is open, or one of desk's editors (the search, the name, the text box)
// has the keyboard.
func (u *ui) drawKeysOff(gtx layout.Context) bool {
	return u.edit != nil || u.pal != nil || u.cfg != nil || u.unlock != nil || u.pick != "" || gtx.Focused(&u.search) || u.drawTyping(gtx)
}

// drawToggleTheme flips the canvas between light and dark (saved in the
// file, as excalidraw.com does).
func (u *ui) drawToggleTheme(st *drawState) {
	if st.dark() {
		delete(st.ed.Scene.AppState, "theme")
	} else {
		st.ed.Scene.AppState["theme"] = "dark"
	}
	st.rasters = map[string]*drawRaster{}
	st.ed.Gen++
}

// drawSelection outlines the selection and its grips, and the box being
// dragged, in window pixels so they stay thin at any zoom.
func (u *ui) drawSelection(gtx layout.Context, st *drawState, px float32) {
	ed := st.ed
	acc := kit.RGB(0x6965db)
	if st.dark() {
		acc = kit.RGB(0xa8a5ff)
	}
	w := 1.2 * px
	stroke := func(pts []f32.Point, closed bool) {
		var p clip.Path
		p.Begin(gtx.Ops)
		p.MoveTo(pts[0])
		for _, q := range pts[1:] {
			p.LineTo(q)
		}
		if closed {
			p.Close()
		}
		paint.FillShape(gtx.Ops, acc, clip.Stroke{Path: p.End(), Width: w}.Op())
	}
	pane := func(p draw.Pt) f32.Point { return st.toPane(p, px) }
	pad := float64(4 / st.zoom)
	sel := ed.Selected()
	for _, e := range sel {
		if e.ID == ed.Editing || len(sel) == 1 && (e.Type == "line" || e.Type == "arrow") { // a lone line has its points' grips
			continue
		}
		t := e.Transform()
		x0, y0, x1, y1 := e.Bounds()
		if e.Type != "line" && e.Type != "arrow" && e.Type != "freedraw" {
			x0, y0, x1, y1 = 0, 0, e.Width, e.Height
		} else {
			t = draw.Identity()
		}
		var pts []f32.Point
		for _, c := range []draw.Pt{{X: x0 - pad, Y: y0 - pad}, {X: x1 + pad, Y: y0 - pad}, {X: x1 + pad, Y: y1 + pad}, {X: x0 - pad, Y: y1 + pad}} {
			pts = append(pts, pane(t.Apply(c)))
		}
		stroke(pts, true)
	}
	for _, h := range ed.Handles(pad * 2) {
		c := pane(h.P)
		r, in := int(4*px), int(w+.5)
		rect := image.Rect(int(c.X)-r, int(c.Y)-r, int(c.X)+r, int(c.Y)+r)
		if h.Name == "rotation" || h.Name[0] == 'p' { // a ring: the accent, the canvas inside
			paint.FillShape(gtx.Ops, acc, clip.Ellipse(rect).Op(gtx.Ops))
			paint.FillShape(gtx.Ops, st.shown.Background(), clip.Ellipse(rect.Inset(in)).Op(gtx.Ops))
			continue
		}
		kit.FillRRect(gtx.Ops, rect, 2, acc)
		kit.FillRRect(gtx.Ops, rect.Inset(in), 1, st.shown.Background())
	}
	if b := ed.Box; b != nil {
		a, c := pane(draw.Pt{X: b[0], Y: b[1]}), pane(draw.Pt{X: b[2], Y: b[3]})
		r := image.Rect(int(math.Min(float64(a.X), float64(c.X))), int(math.Min(float64(a.Y), float64(c.Y))), int(math.Max(float64(a.X), float64(c.X))), int(math.Max(float64(a.Y), float64(c.Y))))
		fill := acc
		fill.A = 0x22
		paint.FillShape(gtx.Ops, fill, clip.Rect(r).Op())
		stroke([]f32.Point{layout.FPt(r.Min), f32.Pt(float32(r.Max.X), float32(r.Min.Y)), layout.FPt(r.Max), f32.Pt(float32(r.Min.X), float32(r.Max.Y))}, true)
	}
}

// drawFont is the window's font for a text element's family.
func drawFont(family int) font.Font {
	switch draw.FontFile(family) {
	case "Manrope-Regular":
		return kit.Sans
	case "Manrope-Bold":
		return kit.Bold(kit.Sans)
	case "GeistMono-Regular":
		return kit.Mono
	}
	return kit.Hand
}

// drawTextBox is the editor over the text element being typed: in its
// font, size and color, its text the element's as it is typed.
func (u *ui) drawTextBox(gtx layout.Context, st *drawState, px float32) {
	ed := st.ed
	e := ed.Scene.Find(ed.Editing)
	if e == nil {
		st.typing, st.textFoc = "", false
		return
	}
	if st.typing != e.ID { // a new text: start from its own
		st.typing = e.ID
		st.text.SetText(e.Text)
		st.text.SetCaret(st.text.Len(), st.text.Len())
		gtx.Execute(key.FocusCmd{Tag: &st.text})
	}
	foc := gtx.Focused(&st.text)
	if st.textFoc && !foc { // the keyboard went elsewhere: the text is done
		st.textFoc = false
		ed.EndText()
		return
	}
	st.textFoc = foc
	if st.text.Text() != e.Text {
		ed.SetText(st.text.Text())
	}
	s := st.zoom * px
	c, _ := draw.Color(e.StrokeColor, e.Opacity)
	if st.dark() {
		c = darkColor(c)
	}
	at := st.toPane(draw.Pt{X: e.X, Y: e.Y}, px)
	defer op.Offset(image.Pt(int(at.X), int(at.Y))).Push(gtx.Ops).Pop()
	g := gtx
	g.Constraints = layout.Constraints{Min: image.Pt(int(float32(e.FontSize)*s), 0), Max: image.Pt(1<<16, 1<<16)}
	st.text.LineHeightScale = float32(e.LineHeight)
	switch e.TextAlign {
	case "center":
		st.text.Alignment = text.Middle
	case "right":
		st.text.Alignment = text.End
	default:
		st.text.Alignment = text.Start
	}
	st.text.Layout(g, u.k.Sh, drawFont(e.FontFamily), unit.Sp(float32(e.FontSize)*s/gtx.Metric.PxPerSp), kit.Mat(g, c), kit.Mat(g, kit.Mix(c, st.shown.Background(), .7)))
}
