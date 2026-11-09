package ui

import (
	"fmt"
	"image"
	"image/color"
	"slices"
	"strings"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/draw"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// drawTool is a button of the toolbar.
type drawTool struct {
	tool, tip string
	ico       kit.IconPath
}

var drawToolbar = []drawTool{
	{draw.ToolHand, "Hand (panning tool) — H", kit.IcoHand},
	{draw.ToolSelect, "Selection — V or 1", kit.IcoPointer},
	{draw.ToolRectangle, "Rectangle — R or 2", kit.IcoSquare},
	{draw.ToolDiamond, "Diamond — D or 3", kit.IcoDiamond},
	{draw.ToolEllipse, "Ellipse — O or 4", kit.IcoCircle},
	{draw.ToolArrow, "Arrow — A or 5", kit.IcoArrow},
	{draw.ToolLine, "Line — L or 6", kit.IcoLine},
	{draw.ToolFreedraw, "Draw — P or 7", kit.IcoPencil},
	{draw.ToolText, "Text — T or 8", kit.IcoLetter},
	{drawImageTool, "Insert image — 9", kit.IcoImage},
	{draw.ToolEraser, "Eraser — E or 0", kit.IcoEraser},
	{draw.ToolFrame, "Frame — F", kit.IcoFrame},
}

// drawImageTool is the toolbar's Image: a file to choose, not a tool to draw with.
const drawImageTool = "image"

// Excalidraw's first picks of its palette.
var (
	drawStrokes = []string{"#1e1e1e", "#e03131", "#2f9e44", "#1971c2", "#f08c00"}
	drawFills   = []string{"transparent", "#ffc9c9", "#b2f2bb", "#a5d8ff", "#ffec99"}
	drawHeads   = []string{"", "arrow", "triangle", "bar", "dot", "diamond", "triangle_outline", "circle_outline", "diamond_outline"}
)

// drawTools lays the editor's controls over the canvas: the toolbar at
// the top, the style of what is selected (or about to be drawn) on the
// left, zoom and undo at the bottom left, the theme and Fit at the top
// right.
func (u *ui) drawTools(gtx layout.Context, st *drawState, size image.Point, act *func()) {
	k, ed := u.k, st.ed
	place := func(at image.Point, call op.CallOp) {
		t := op.Offset(at).Push(gtx.Ops)
		call.Add(gtx.Ops)
		t.Pop()
	}
	g := gtx
	g.Constraints = layout.Constraints{Max: size}
	island := func(w layout.Widget) layout.Widget {
		return kit.Boxed(kit.CPanel, kit.CRim, kit.RCard, layout.UniformInset(4), w)
	}
	pad := gtx.Dp(12)

	m := op.Record(gtx.Ops)
	var tips []func()
	d := island(func(gtx layout.Context) layout.Dimensions {
		var kids []layout.FlexChild
		for i, t := range drawToolbar {
			if i == 2 {
				kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					paint.FillShape(gtx.Ops, kit.CLine, clip.Rect{Min: image.Pt(gtx.Dp(4), gtx.Dp(4)), Max: image.Pt(gtx.Dp(5), gtx.Dp(22))}.Op())
					return layout.Dimensions{Size: image.Pt(gtx.Dp(9), gtx.Dp(26))}
				}))
			}
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				id := "drawTool" + t.tool
				d, ok := k.IconBtn(gtx, id, t.ico, ed.Tool == t.tool)
				tips = append(tips, func() { k.Tip(gtx, id, d, t.tip, false) })
				if ok {
					tool := t.tool
					*act = func() { ed.EndText(); ed.SetTool(tool) }
					if tool == drawImageTool {
						*act = func() { u.drawPickImage(st.path) }
					}
				}
				return d
			}))
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
	})(g)
	for _, f := range tips {
		f()
	}
	place(image.Pt((size.X-d.Size.X)/2, pad), m.Stop())

	if sel := ed.Selected(); len(sel) > 0 || slices.Contains([]string{draw.ToolRectangle, draw.ToolDiamond, draw.ToolEllipse, draw.ToolArrow, draw.ToolLine, draw.ToolFreedraw, draw.ToolText}, ed.Tool) {
		m := op.Record(gtx.Ops)
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(220), max(size.Y-2*pad-gtx.Dp(50+60), gtx.Dp(80)))} // above the zoom and undo row; it scrolls
		island(func(gtx layout.Context) layout.Dimensions { return u.drawStyle(gtx, st, sel, act) })(g)
		place(image.Pt(pad, pad+gtx.Dp(50)), m.Stop())
	}

	m = op.Record(gtx.Ops)
	d = layout.Flex{Alignment: layout.Middle}.Layout(g,
		layout.Rigid(island(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.IconBtn(gtx, "drawZoomOut", kit.IcoMinus, false)
					if ok {
						*act = func() { st.zoomAt(layout.FPt(size).Div(2), 1/1.25) }
					}
					return d
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.Button(gtx, "drawZoom100", color.NRGBA{}, kit.CHover, kit.RCtl, layout.Inset{Top: 5, Bottom: 5, Left: 6, Right: 6},
						k.Lbl(fmt.Sprintf("%.0f%%", st.zoom*100), 12, kit.CText2, kit.Mono))
					if ok {
						*act = func() { st.zoomAt(layout.FPt(size).Div(2), 1/st.zoom) }
					}
					return d
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.IconBtn(gtx, "drawZoomIn", kit.IcoPlus, false)
					if ok {
						*act = func() { st.zoomAt(layout.FPt(size).Div(2), 1.25) }
					}
					return d
				}))
		})),
		layout.Rigid(kit.HGap(8)),
		layout.Rigid(island(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.IconBtn(gtx, "drawUndo", kit.IcoUndo, false)
					if ok {
						*act = ed.Undo
					}
					return d
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.IconBtn(gtx, "drawRedo", kit.IcoRedo, false)
					if ok {
						*act = ed.Redo
					}
					return d
				}))
		})))
	place(image.Pt(pad, size.Y-d.Size.Y-pad), m.Stop())

	m = op.Record(gtx.Ops)
	d = island(func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d, ok := k.IconBtn(gtx, "drawFit", kit.IcoFit, false)
				k.Tip(gtx, "drawFit", d, "Show it all — Shift+1", true)
				if ok {
					*act = func() { st.fit(size, gtx.Metric.PxPerDp) }
				}
				return d
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d, ok := k.IconBtn(gtx, "drawLib", kit.IcoLibrary, st.libOpen)
				k.Tip(gtx, "drawLib", d, "Library", true)
				if ok {
					*act = func() { st.libOpen = !st.libOpen }
				}
				return d
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d, ok := k.IconBtn(gtx, "drawTheme", kit.IcoMoon, st.dark())
				k.Tip(gtx, "drawTheme", d, "Dark canvas — Alt+Shift+D", true)
				if ok {
					*act = func() { u.drawToggleTheme(st) }
				}
				return d
			}))
	})(g)
	place(image.Pt(size.X-d.Size.X-pad, pad), m.Stop())
}

// drawStyle is the style panel: each property the selection (or the tool
// about to draw) has, Excalidraw's choices for it; a pick changes the
// selection as one undo step and what is drawn next.
func (u *ui) drawStyle(gtx layout.Context, st *drawState, sel []*draw.Element, act *func()) layout.Dimensions {
	k, ed := u.k, st.ed
	cur := ed.Style
	types := map[string]bool{}
	for _, e := range sel {
		types[e.Type] = true
	}
	if len(sel) > 0 {
		cur = sel[0]
	} else {
		types[ed.Tool] = true
	}
	has := func(ts ...string) bool {
		for _, t := range ts {
			if types[t] {
				return true
			}
		}
		return false
	}
	shown := func(c string) color.NRGBA {
		if st.dark() {
			c = darkHex(c)
		}
		v, _ := draw.Color(c, 100)
		return v
	}
	set := func(f func(e *draw.Element)) { *act = func() { ed.Restyle(f) } }
	var rows []layout.Widget // a section each, in a list: a short window scrolls the panel
	section := func(title string, opts ...layout.Widget) {
		var kids []layout.FlexChild
		for i, o := range opts {
			if i > 0 {
				kids = append(kids, layout.Rigid(kit.HGap(4)))
			}
			kids = append(kids, layout.Rigid(o))
		}
		top := unit.Dp(10)
		if len(rows) == 0 {
			top = 0
		}
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: top}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(k.Lbl(title, 11, kit.CText3, kit.Medium(kit.Sans))), layout.Rigid(kit.VGap(4)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
					}))
			})
		})
	}
	opt := func(id string, on bool, content layout.Widget, f func(e *draw.Element)) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			bg := color.NRGBA{}
			if on {
				bg = kit.CAccSoft
			}
			d, ok := k.Button(gtx, "drawOpt"+id, bg, kit.CHover, kit.RCtl, layout.UniformInset(4), func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints = layout.Exact(image.Pt(gtx.Dp(22), gtx.Dp(22)))
				return content(gtx)
			})
			if ok {
				set(f)
			}
			return d
		}
	}
	fg := func(on bool) color.NRGBA {
		if on {
			return kit.CAccText
		}
		return kit.CText2
	}
	// glyph strokes a preview drawn into the 22dp box, w dp wide.
	glyph := func(on bool, w unit.Dp, draw func(p *clip.Path, s float32)) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			s := float32(gtx.Dp(22))
			var p clip.Path
			p.Begin(gtx.Ops)
			draw(&p, s)
			paint.FillShape(gtx.Ops, fg(on), clip.Stroke{Path: p.End(), Width: float32(gtx.Dp(w))}.Op())
			return layout.Dimensions{Size: image.Pt(int(s), int(s))}
		}
	}
	word := func(on bool, s string, sp unit.Sp) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return layout.Center.Layout(gtx, k.Lbl(s, sp, fg(on), kit.Semi(kit.Sans)))
		}
	}
	swatch := func(c string) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			r := image.Rect(gtx.Dp(2), gtx.Dp(2), gtx.Dp(20), gtx.Dp(20))
			if c == "transparent" {
				kit.FillRRect(gtx.Ops, r, 3, kit.CPane)
				var p clip.Path
				p.Begin(gtx.Ops)
				p.MoveTo(layout.FPt(image.Pt(r.Min.X, r.Max.Y)))
				p.LineTo(layout.FPt(image.Pt(r.Max.X, r.Min.Y)))
				paint.FillShape(gtx.Ops, kit.CRed, clip.Stroke{Path: p.End(), Width: float32(gtx.Dp(1.5))}.Op())
				return layout.Dimensions{Size: image.Pt(gtx.Dp(22), gtx.Dp(22))}
			}
			kit.FillRRect(gtx.Ops, r, 3, shown(c))
			return layout.Dimensions{Size: image.Pt(gtx.Dp(22), gtx.Dp(22))}
		}
	}
	line := func(p *clip.Path, s float32) { p.MoveTo(f32.Pt(s*.15, s*.5)); p.LineTo(f32.Pt(s*.85, s*.5)) }

	var strokes []layout.Widget
	for _, c := range drawStrokes {
		strokes = append(strokes, opt("s"+c, cur.StrokeColor == c, swatch(c), func(e *draw.Element) { e.StrokeColor = c }))
	}
	if len(sel) == 1 && sel[0].Type == "frame" {
		rows = append(rows, func(gtx layout.Context) layout.Dimensions { return u.drawFrameName(gtx, st, sel[0], act) })
	}
	if has("rectangle", "diamond", "ellipse", "arrow", "line", "freedraw", "text") { // not images, frames
		section("Stroke", strokes...)
	}
	if has("rectangle", "diamond", "ellipse", "line") {
		var fills []layout.Widget
		for _, c := range drawFills {
			fills = append(fills, opt("b"+c, cur.BackgroundColor == c, swatch(c), func(e *draw.Element) { e.BackgroundColor = c }))
		}
		section("Background", fills...)
		if cur.BackgroundColor != "transparent" && cur.BackgroundColor != "" {
			hatch := func(cross bool) func(p *clip.Path, s float32) {
				return func(p *clip.Path, s float32) {
					for i := float32(0); i < 4; i++ {
						p.MoveTo(f32.Pt(s*(.1+i*.22), s*.9))
						p.LineTo(f32.Pt(s*(.1+i*.22)+s*.3, s*.1))
						if cross {
							p.MoveTo(f32.Pt(s*(.1+i*.22), s*.1))
							p.LineTo(f32.Pt(s*(.1+i*.22)+s*.3, s*.9))
						}
					}
				}
			}
			section("Fill",
				opt("fh", cur.FillStyle == "hachure", glyph(cur.FillStyle == "hachure", 1.2, hatch(false)), func(e *draw.Element) { e.FillStyle = "hachure" }),
				opt("fx", cur.FillStyle == "cross-hatch", glyph(cur.FillStyle == "cross-hatch", 1.2, hatch(true)), func(e *draw.Element) { e.FillStyle = "cross-hatch" }),
				opt("fs", cur.FillStyle == "solid", func(gtx layout.Context) layout.Dimensions {
					kit.FillRRect(gtx.Ops, image.Rect(gtx.Dp(3), gtx.Dp(3), gtx.Dp(19), gtx.Dp(19)), 3, fg(cur.FillStyle == "solid"))
					return layout.Dimensions{Size: image.Pt(gtx.Dp(22), gtx.Dp(22))}
				}, func(e *draw.Element) { e.FillStyle = "solid" }))
		}
	}
	if has("rectangle", "diamond", "ellipse", "arrow", "line", "freedraw") {
		var ws []layout.Widget
		for _, w := range []float64{1, 2, 4} {
			ws = append(ws, opt(fmt.Sprint("w", w), cur.StrokeWidth == w, glyph(cur.StrokeWidth == w, unit.Dp(w*1.2), line), func(e *draw.Element) { e.StrokeWidth = w }))
		}
		section("Stroke width", ws...)
	}
	if has("rectangle", "diamond", "ellipse", "arrow", "line") {
		var ss []layout.Widget
		for i, s := range []string{"solid", "dashed", "dotted"} {
			gap := []float32{0, .14, .06}[i]
			ss = append(ss, opt("ss"+s, cur.StrokeStyle == s, glyph(cur.StrokeStyle == s, 1.6, func(p *clip.Path, sz float32) {
				if gap == 0 {
					line(p, sz)
					return
				}
				for x := float32(.12); x < .86; x += gap * 2 {
					p.MoveTo(f32.Pt(sz*x, sz*.5))
					p.LineTo(f32.Pt(sz*min(x+gap, .88), sz*.5))
				}
			}), func(e *draw.Element) { e.StrokeStyle = s }))
		}
		section("Stroke style", ss...)
		var rs []layout.Widget
		for _, r := range []float64{0, 1, 2} {
			rs = append(rs, opt(fmt.Sprint("r", r), cur.Roughness == r, glyph(cur.Roughness == r, 1.4, func(p *clip.Path, s float32) {
				a := float32(r) * .1
				p.MoveTo(f32.Pt(s*.12, s*.6))
				p.CubeTo(f32.Pt(s*.35, s*(.6-a*2)), f32.Pt(s*.6, s*(.4+a*2)), f32.Pt(s*.88, s*.4))
			}), func(e *draw.Element) { e.Roughness = r }))
		}
		section("Sloppiness", rs...)
	}
	if has("rectangle", "diamond", "arrow", "line") {
		round := cur.Roundness != nil
		section("Edges",
			opt("esharp", !round, glyph(!round, 1.6, func(p *clip.Path, s float32) {
				p.MoveTo(f32.Pt(s*.2, s*.85))
				p.LineTo(f32.Pt(s*.2, s*.2))
				p.LineTo(f32.Pt(s*.85, s*.2))
			}), func(e *draw.Element) { e.Roundness = nil }),
			opt("eround", round, glyph(round, 1.6, func(p *clip.Path, s float32) {
				p.MoveTo(f32.Pt(s*.2, s*.85))
				p.LineTo(f32.Pt(s*.2, s*.5))
				p.QuadTo(f32.Pt(s*.2, s*.2), f32.Pt(s*.5, s*.2))
				p.LineTo(f32.Pt(s*.85, s*.2))
			}), func(e *draw.Element) {
				if e.Type == "line" || e.Type == "arrow" {
					e.Roundness = &draw.Roundness{Type: draw.RoundProportional}
				} else {
					e.Roundness = &draw.Roundness{Type: draw.RoundAdaptive}
				}
			}))
	}
	if has("arrow") {
		head := func(id string, h *string, f func(e *draw.Element, v *string)) layout.Widget {
			v := ""
			if h != nil {
				v = *h
			}
			next := drawHeads[(slices.Index(drawHeads, v)+1)%len(drawHeads)]
			label := map[string]string{"": "None"}[v]
			if label == "" {
				label = v
			}
			return func(gtx layout.Context) layout.Dimensions {
				d, ok := k.Button(gtx, "drawOpt"+id, kit.CPane, kit.CHover, kit.RCtl, layout.Inset{Top: 4, Bottom: 4, Left: 8, Right: 8}, k.Lbl(label, 11, kit.CText2, kit.Sans))
				if ok {
					set(func(e *draw.Element) {
						if e.Type == "arrow" || e == ed.Style {
							var p *string
							if next != "" {
								p = &next
							}
							f(e, p)
						}
					})
				}
				return d
			}
		}
		section("Arrowheads",
			head("hs", cur.StartArrowhead, func(e *draw.Element, v *string) { e.StartArrowhead = v }),
			word(false, "→", 12),
			head("he", cur.EndArrowhead, func(e *draw.Element, v *string) { e.EndArrowhead = v }))
	}
	if has("text") {
		var fams []layout.Widget
		for _, f := range []struct {
			fam  int
			name string
		}{{draw.FontExcalifont, "Hand"}, {draw.FontNunito, "Normal"}, {draw.FontComic, "Code"}} {
			on := drawFont(cur.FontFamily) == drawFont(f.fam)
			fams = append(fams, func(gtx layout.Context) layout.Dimensions {
				bg := color.NRGBA{}
				if on {
					bg = kit.CAccSoft
				}
				d, ok := k.Button(gtx, "drawOptff"+f.name, bg, kit.CHover, kit.RCtl, layout.Inset{Top: 4, Bottom: 4, Left: 8, Right: 8}, k.Lbl(f.name, 12, fg(on), drawFont(f.fam)))
				if ok {
					set(func(e *draw.Element) { e.FontFamily = f.fam })
				}
				return d
			})
		}
		section("Font family", fams...)
		var sizes []layout.Widget
		for i, s := range []float64{16, 20, 28, 36} {
			name := []string{"S", "M", "L", "XL"}[i]
			sizes = append(sizes, opt("fz"+name, cur.FontSize == s, word(cur.FontSize == s, name, 12), func(e *draw.Element) { e.FontSize = s }))
		}
		section("Font size", sizes...)
		var aligns []layout.Widget
		for _, a := range []string{"left", "center", "right"} {
			on := cur.TextAlign == a
			aligns = append(aligns, opt("ta"+a, on, glyph(on, 1.6, func(p *clip.Path, s float32) {
				for i, w := range []float32{.7, .45, .6} {
					x := map[string]float32{"left": .15, "center": (1 - w) / 2, "right": .85 - w}[a]
					y := s * (.3 + float32(i)*.2)
					p.MoveTo(f32.Pt(s*x, y))
					p.LineTo(f32.Pt(s*(x+w), y))
				}
			}), func(e *draw.Element) { e.TextAlign = a }))
		}
		section("Text align", aligns...)
	}
	section("Opacity", func(gtx layout.Context) layout.Dimensions {
		d, step := k.Stepper(gtx, "drawOpacity", fmt.Sprintf("%.0f", cur.Opacity))
		if step != 0 {
			v := min(max(cur.Opacity+float64(step)*10, 0), 100)
			set(func(e *draw.Element) { e.Opacity = v })
		}
		return d
	})
	if len(sel) > 0 {
		btn := func(id, tip string, ico kit.IconPath, f func()) layout.Widget {
			return func(gtx layout.Context) layout.Dimensions {
				d, ok := k.IconBtn(gtx, id, ico, false)
				k.Tip(gtx, id, d, tip, false)
				if ok {
					*act = f
				}
				return d
			}
		}
		grouped := slices.ContainsFunc(sel, func(e *draw.Element) bool { return len(e.GroupIDs) > 0 })
		if len(sel) > 1 || grouped {
			var gs []layout.Widget
			if len(sel) > 1 {
				gs = append(gs, func(gtx layout.Context) layout.Dimensions {
					d, ok := k.Button(gtx, "drawGroup", kit.CPane, kit.CHover, kit.RCtl, layout.Inset{Top: 4, Bottom: 4, Left: 8, Right: 8}, k.Lbl("Group", 11, kit.CText2, kit.Sans))
					k.Tip(gtx, "drawGroup", d, "Group — Ctrl+G", false)
					if ok {
						*act = ed.Group
					}
					return d
				})
			}
			if grouped {
				gs = append(gs, func(gtx layout.Context) layout.Dimensions {
					d, ok := k.Button(gtx, "drawUngroup", kit.CPane, kit.CHover, kit.RCtl, layout.Inset{Top: 4, Bottom: 4, Left: 8, Right: 8}, k.Lbl("Ungroup", 11, kit.CText2, kit.Sans))
					k.Tip(gtx, "drawUngroup", d, "Ungroup — Ctrl+Shift+G", false)
					if ok {
						*act = ed.Ungroup
					}
					return d
				})
			}
			section("Group", gs...)
		}
		section("Layers",
			btn("drawToBack", "Send to back — Ctrl+Shift+[", kit.IcoBack, func() { ed.Order(-2) }),
			btn("drawBackward", "Send backward — Ctrl+[", kit.IcoBackward, func() { ed.Order(-1) }),
			btn("drawForward", "Bring forward — Ctrl+]", kit.IcoForward, func() { ed.Order(1) }),
			btn("drawFront", "Bring to front — Ctrl+Shift+]", kit.IcoFront, func() { ed.Order(2) }))
		section("Actions",
			btn("drawDup", "Duplicate — Ctrl+D", kit.IcoCopy, ed.Duplicate),
			btn("drawDel", "Delete — Delete", kit.IcoTrash, ed.Delete))
	}
	return layout.UniformInset(8).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		st.styles.Axis = layout.Vertical
		return st.styles.Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions { return rows[i](gtx) })
	})
}

// drawFrameName is a selected frame's name in the style panel: Enter or
// leaving the field renames it (one undo step).
func (u *ui) drawFrameName(gtx layout.Context, st *drawState, f *draw.Element, act *func()) layout.Dimensions {
	k := u.k
	name := "Frame"
	if f.Name != nil {
		name = *f.Name
	}
	foc := gtx.Focused(&st.frameName)
	if st.nameOf != f.ID || (!foc && !st.nameFoc && st.frameName.Text() != name) {
		st.nameOf = f.ID
		st.frameName.SetText(name)
	}
	rename := func() {
		v := strings.TrimSpace(st.frameName.Text())
		if v == "" || v == name {
			st.frameName.SetText(name)
			return
		}
		id := f.ID
		st.ed.Restyle(func(e *draw.Element) {
			if e.ID == id {
				e.Name = &v
			}
		})
	}
	for {
		ev, ok := st.frameName.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			*act = func() { rename(); u.blur = true }
		}
	}
	if st.nameFoc && !foc && *act == nil {
		*act = rename
	}
	st.nameFoc = foc
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(k.Lbl("Name", 11, kit.CText3, kit.Medium(kit.Sans))), layout.Rigid(kit.VGap(4)),
		layout.Rigid(kit.Boxed(kit.CPane, kit.CLine, kit.RCtl, layout.Inset{Top: 5, Bottom: 5, Left: 8, Right: 8}, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return k.Editor(gtx, &st.frameName, "Frame", 12, kit.Sans, kit.CText)
		})))
}
