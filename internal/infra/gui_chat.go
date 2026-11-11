package infra

import (
	"fmt"
	"image"
	"image/color"
	"regexp"
	"strings"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/x/styledtext"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	gtext "github.com/yuin/goldmark/text"
)

type gripState struct {
	dragging bool
	startX   float32
	startW   unit.Dp
	drawnW   int // the panel's width in px when the grip was last laid out: its events' frame
}

func (u *ui) claudePanel(gtx layout.Context) layout.Dimensions {
	u.gripEvents(gtx) // before reading claudeW, so this frame draws the dragged width
	w := gtx.Dp(u.claudeW)
	gtx.Constraints = layout.Exact(image.Pt(w, gtx.Constraints.Max.Y))
	panel(gtx, image.Rectangle{Max: gtx.Constraints.Max}, 0, cPanel, color.NRGBA{})
	u.resizeGrip(gtx)
	t := u.tab()
	where := "Open a session to talk to Claude"
	if t != nil {
		if t.OrgOnly {
			where = t.Org + " · org"
		} else {
			where = t.Group
			for _, p := range t.Panes {
				if p.Focused {
					where += " · " + p.Name
				}
			}
		}
		where += " · " + t.Account
		if t.Model != "" {
			where += " · " + t.Model
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 7, Bottom: 7, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(icon(14, cAcc, icoSpark)),
					layout.Rigid(hgap(8)),
					layout.Rigid(u.lbl("Claude", 12, cText, fSemi(sansFont))),
					layout.Rigid(hgap(8)),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return u.label(gtx, where, 11, cText3, sansFont, 1) }),
					layout.Rigid(u.lbl(modText("L"), 11, cText3, sansFont)))
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if t == nil {
				return layout.Dimensions{}
			}
			return u.ctxBar(gtx, t)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: 12, Right: 12, Bottom: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				if t == nil {
					return u.label(gtx, "Claude reads the commands you point it at and runs commands in your panes. Every change waits for your approval.", 13, cText3, sansFont, 0)
				}
				n := len(t.Chat)
				if t.Busy {
					n++
				}
				return u.chatList.Layout(gtx, n, func(gtx layout.Context, k int) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return layout.Inset{Bottom: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						if k == len(t.Chat) {
							s := "thinking…"
							if t.Compacting {
								s = "compacting the context…"
							}
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(dot(cAcc, true)), layout.Rigid(hgap(8)),
								layout.Rigid(u.lbl(s, 12, cText3, sansFont)))
						}
						return u.entry(gtx, t, k)
					})
				})
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.composer(gtx, t) }),
	)
}

// ctxBar is the context meter under the panel header: how full Claude's
// context is (green, orange from 70%, red from 90%), with compact and clear.
func (u *ui) ctxBar(gtx layout.Context, t *guiTab) layout.Dimensions {
	pct, label := 0.0, "nothing yet"
	if t.Ctx > 0 {
		label = fmtNum(float64(t.Ctx)) + " tokens"
		if t.CtxWindow > 0 {
			pct = float64(t.Ctx) * 100 / float64(t.CtxWindow)
			label = fmt.Sprintf("%s / %s · %.0f%%", fmtNum(float64(t.Ctx)), fmtNum(float64(t.CtxWindow)), pct)
		}
	}
	act := func(id, s string, do func()) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			fg := cText2
			if t.Busy {
				fg = cText3 // Claude is mid-turn: the model says so if clicked
			}
			d, ok := u.button(gtx, id, color.NRGBA{}, cHover, rCtl, layout.Inset{Top: 2, Bottom: 2, Left: 6, Right: 6}, u.lbl(s, 11, fg, fMedium(sansFont)))
			if ok {
				do()
			}
			return d
		}
	}
	return layout.Inset{Left: 12, Right: 8, Bottom: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(u.lbl("Context", 11, cText3, fSemi(sansFont))),
			layout.Rigid(hgap(8)),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				w, h := gtx.Constraints.Max.X, gtx.Dp(4)
				fillRRect(gtx.Ops, image.Rect(0, 0, w, h), h/2, cLine)
				if pct > 0 {
					fillRRect(gtx.Ops, image.Rect(0, 0, max(h, int(float64(w)*min(pct, 100)/100)), h), h/2, levelColor(pct))
				}
				return layout.Dimensions{Size: image.Pt(w, h)}
			}),
			layout.Rigid(hgap(8)),
			layout.Rigid(u.lbl(label, 11, cText3, monoFont)),
			layout.Rigid(hgap(4)),
			layout.Rigid(act("ctxCompact", "Compact", u.d.compact)),
			layout.Rigid(act("ctxClear", "Clear", u.d.clearContext)))
	})
}

// gripEvents handles drags of the panel's left edge. The panel is right
// aligned, so its frame moves with its width: an event's X minus the width
// the grip was laid out at (drawnW) is the window's X less its width, which
// stays put, while claudeW changes under the events of one frame (adding
// claudeW made the width jump back and forth while dragging).
func (u *ui) gripEvents(gtx layout.Context) {
	g := &u.grip
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: g, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
		if !ok {
			break
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch e.Kind {
		case pointer.Press:
			g.dragging, g.startW = true, u.claudeW
			g.startX = e.Position.X - float32(g.drawnW)
		case pointer.Drag:
			if g.dragging {
				dx := g.startX - (e.Position.X - float32(g.drawnW))
				u.claudeW = max(300, min(800, g.startW+unit.Dp(dx/gtx.Metric.PxPerDp)))
			}
		case pointer.Release, pointer.Cancel:
			if g.dragging {
				u.saveLayout()
			}
			g.dragging = false
		}
	}
}

// resizeGrip lays out the panel's left edge as the grip.
func (u *ui) resizeGrip(gtx layout.Context) {
	u.grip.drawnW = gtx.Constraints.Max.X
	st := clip.Rect(image.Rect(0, 0, gtx.Dp(6), gtx.Constraints.Max.Y)).Push(gtx.Ops)
	event.Op(gtx.Ops, &u.grip)
	pointer.CursorColResize.Add(gtx.Ops)
	st.Pop()
}

func (u *ui) entry(gtx layout.Context, t *guiTab, k int) layout.Dimensions {
	e := t.Chat[k]
	id := fmt.Sprintf("e%d-%d", u.st.Active, k)
	switch e.Who {
	case "you":
		return layout.E.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = gtx.Constraints.Max.X * 85 / 100
			gtx.Constraints.Min.X = 0
			return boxed(cAcc, color.NRGBA{}, rCard, layout.Inset{Top: 6, Bottom: 6, Left: 10, Right: 10}, func(gtx layout.Context) layout.Dimensions {
				var chips []layout.FlexChild
				for _, n := range e.Ctx {
					chips = append(chips, layout.Rigid(u.pill(fmt.Sprintf("#%d", n), cWhite, color.NRGBA{0x4a, 0x9f, 0xff, 0xff})), layout.Rigid(hgap(5)))
				}
				if e.Screen {
					chips = append(chips, layout.Rigid(u.pill("□ screen", cWhite, color.NRGBA{0x4a, 0x9f, 0xff, 0xff})))
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if len(chips) == 0 {
							return layout.Dimensions{}
						}
						return layout.Inset{Bottom: 5}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return layout.Flex{}.Layout(gtx, chips...) })
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.selLabel(gtx, id, e.Text, styledtext.SpanStyle{Size: 13, Color: cWhite, Font: sansFont})
					}))
			})(gtx)
		})
	case "claude":
		return u.markdown(gtx, fmt.Sprintf("c%d-%d", u.st.Active, k), e.Text, t.Suggest)
	case "tool":
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(u.lbl("▸ ", 12, cPurple, monoFont)),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return u.selLabel(gtx, id, e.Text, styledtext.SpanStyle{Size: 12, Color: cText2, Font: monoFont})
					}))
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				var kids []layout.FlexChild
				for _, s := range e.Steps {
					kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Left: 18, Top: 3}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return styledtext.Text(u.sh,
								styledtext.SpanStyle{Font: monoFont, Size: 11, Color: cText2, Content: s.Part},
								styledtext.SpanStyle{Font: sansFont, Size: 11, Color: cText3, Content: " — " + s.Does}).Layout(gtx, nil)
						})
					}))
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
			}))
	case "error":
		return boxed(cPanelCard, color.NRGBA{}, rCtl, layout.Inset{Top: 8, Bottom: 8, Left: 12, Right: 12}, func(gtx layout.Context) layout.Dimensions {
			return u.selLabel(gtx, id, e.Text, styledtext.SpanStyle{Size: 12, Color: cRed, Font: sansFont})
		})(gtx)
	case "peer":
		head, body, _ := strings.Cut(e.Text, "\n")
		return boxed(cPanelCard, cLine, rCtl, layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(u.lbl(head, 11, cPurple, fSemi(sansFont))),
				layout.Rigid(vgap(4)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.markdown(gtx, fmt.Sprintf("p%d-%d", u.st.Active, k), body, "")
				}))
		})(gtx)
	}
	return u.selLabel(gtx, id, e.Text, styledtext.SpanStyle{Size: 12, Color: cText3, Font: sansFont})
}

// Markdown: Claude's replies parsed with goldmark into blocks drawn natively.

type mdSpan struct {
	text             string
	strong, em, code bool
	link             bool
}

type mdBlock struct {
	kind  string // p h li quote code table hr, diagram math alert (rich, as the TUI draws them)
	spans []mdSpan
	level int // heading level, list depth
	mark  string
	lang  string
	code  string
	rows  [][][]mdSpan // table: rows of cells; row 0 is the header
	align []east.Alignment
	chart *chart // kind "chart": a mermaid xychart-beta or pie
}

var (
	mdParser = goldmark.New(goldmark.WithExtensions(extension.GFM))
	mdBlocks = map[string][]mdBlock{}
)

func parseMarkdown(src string) []mdBlock {
	if b, ok := mdBlocks[src]; ok {
		return b
	}
	source := []byte(src)
	doc := mdParser.Parser().Parse(gtext.NewReader(source))
	var blocks []mdBlock
	var inline func(n ast.Node, s mdSpan, out *[]mdSpan)
	inline = func(n ast.Node, s mdSpan, out *[]mdSpan) {
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch c := c.(type) {
			case *ast.Text:
				s2 := s
				s2.text = string(c.Segment.Value(source))
				if c.SoftLineBreak() || c.HardLineBreak() {
					s2.text += " "
				}
				*out = append(*out, s2)
			case *ast.CodeSpan:
				s2 := s
				s2.code = true
				inline(c, s2, out)
			case *ast.Emphasis:
				s2 := s
				if c.Level >= 2 {
					s2.strong = true
				} else {
					s2.em = true
				}
				inline(c, s2, out)
			case *ast.Link:
				s2 := s
				s2.link = true
				inline(c, s2, out)
			case *ast.AutoLink:
				s2 := s
				s2.link, s2.text = true, string(c.URL(source))
				*out = append(*out, s2)
			default:
				inline(c, s, out)
			}
		}
	}
	var walk func(first ast.Node, depth int) // first and its next siblings
	walk = func(first ast.Node, depth int) {
		for c := first; c != nil; c = c.NextSibling() {
			switch c := c.(type) {
			case *ast.Paragraph, *ast.TextBlock:
				b := mdBlock{kind: "p"}
				inline(c, mdSpan{}, &b.spans)
				if tex, ok := displayMath(rawLines(c, source)); ok {
					b = mdBlock{kind: "math", code: tex}
				}
				blocks = append(blocks, b)
			case *ast.Heading:
				b := mdBlock{kind: "h", level: c.Level}
				inline(c, mdSpan{}, &b.spans)
				blocks = append(blocks, b)
			case *ast.List:
				i := c.Start
				for item := c.FirstChild(); item != nil; item = item.NextSibling() {
					mark := "•"
					if c.IsOrdered() {
						mark = fmt.Sprintf("%d.", i)
						i++
					}
					first := true
					for part := item.FirstChild(); part != nil; part = part.NextSibling() {
						if _, ok := part.(*ast.List); ok {
							walk(part, depth+1) // the nested list and what follows it in the item
							break
						}
						b := mdBlock{kind: "li", level: depth}
						if first {
							b.mark, first = mark, false
						}
						inline(part, mdSpan{}, &b.spans)
						blocks = append(blocks, b)
					}
				}
			case *ast.Blockquote:
				b := mdBlock{kind: "quote"}
				inline(c, mdSpan{}, &b.spans)
				if kind, rest, ok := alertSpans(b.spans); ok {
					b = mdBlock{kind: "alert", mark: kind, spans: rest}
				}
				blocks = append(blocks, b)
			case *ast.FencedCodeBlock, *ast.CodeBlock:
				var sb strings.Builder
				lines := c.Lines()
				for i := 0; i < lines.Len(); i++ {
					seg := lines.At(i)
					sb.Write(seg.Value(source))
				}
				b := mdBlock{kind: "code", code: strings.TrimRight(sb.String(), "\n")}
				if f, ok := c.(*ast.FencedCodeBlock); ok {
					b.lang = string(f.Language(source))
				}
				switch b.lang {
				case "mermaid":
					if c, ok := parseChart(b.code); ok {
						b.kind, b.chart = "chart", &c
					} else if art, ok := mermaid(b.code, 400); ok {
						b.kind, b.code = "diagram", ansi.Strip(art)
					}
				case "figure":
					b.kind = "figure"
				case "math", "latex", "tex":
					b.kind, b.code = "math", mathText(b.code)
				}
				blocks = append(blocks, b)
			case *ast.ThematicBreak:
				blocks = append(blocks, mdBlock{kind: "hr"})
			case *east.Table:
				b := mdBlock{kind: "table", align: c.Alignments}
				for row := c.FirstChild(); row != nil; row = row.NextSibling() {
					var cells [][]mdSpan
					for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
						var spans []mdSpan
						inline(cell, mdSpan{}, &spans)
						cells = append(cells, spans)
					}
					b.rows = append(b.rows, cells)
				}
				blocks = append(blocks, b)
			default:
				walk(c.FirstChild(), depth)
			}
		}
	}
	walk(doc.FirstChild(), 0)
	bound(mdBlocks, cacheMax)
	mdBlocks[src] = blocks
	return blocks
}

// cacheMax bounds the chat's parse and highlight caches: a streaming answer
// adds an entry per step it grows.
const cacheMax = 512

// bound drops about half of c's entries once it holds cacheMax or more; what
// the chat still shows is made again on the next frame.
func bound[V any](c map[string]V, n int) {
	if len(c) < n {
		return
	}
	for k := range c {
		if len(c) <= n/2 {
			break
		}
		delete(c, k)
	}
}

// rawLines is a block's source text.
func rawLines(n ast.Node, source []byte) string {
	var sb strings.Builder
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		sb.Write(seg.Value(source))
	}
	return strings.TrimSpace(sb.String())
}

// displayMath reads a paragraph that is one $$…$$ formula.
func displayMath(raw string) (string, bool) {
	if len(raw) > 4 && strings.HasPrefix(raw, "$$") && strings.HasSuffix(raw, "$$") && !strings.Contains(raw[2:len(raw)-2], "$$") {
		return mathText(raw[2 : len(raw)-2]), true
	}
	return "", false
}

// mathText is LaTeX as Unicode lines, like the TUI's math pieces.
func mathText(tex string) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(tex), "\\\\") {
		lines = append(lines, texToUnicode(strings.TrimSpace(l)))
	}
	return strings.Join(lines, "\n")
}

var alertMark = regexp.MustCompile(`^\s*\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]\s*`)

// alertSpans spots a GitHub alert ("> [!NOTE] …") and drops its marker.
func alertSpans(ss []mdSpan) (string, []mdSpan, bool) {
	var joined strings.Builder
	for _, s := range ss {
		joined.WriteString(s.text)
	}
	m := alertMark.FindStringSubmatch(joined.String())
	if m == nil {
		return "", nil, false
	}
	cut := len(m[0])
	var rest []mdSpan
	for _, s := range ss {
		if cut >= len(s.text) {
			cut -= len(s.text)
			continue
		}
		s.text = s.text[cut:]
		cut = 0
		rest = append(rest, s)
	}
	return m[1], rest, true
}

var alertLooks = map[string]struct {
	title string
	c     func() color.NRGBA // read at draw time: themes change the tokens
}{
	"NOTE": {"ℹ Note", func() color.NRGBA { return cAccText }}, "TIP": {"✓ Tip", func() color.NRGBA { return cGreen }},
	"IMPORTANT": {"◆ Important", func() color.NRGBA { return cPurple }}, "WARNING": {"▲ Warning", func() color.NRGBA { return cOrange }},
	"CAUTION": {"✗ Caution", func() color.NRGBA { return cRed }},
}

func (u *ui) spans(ss []mdSpan, sp unit.Sp, c color.NRGBA, base font.Font) []styledtext.SpanStyle {
	var out []styledtext.SpanStyle
	for _, s := range ss {
		st := styledtext.SpanStyle{Font: base, Size: sp, Color: c, Content: s.text}
		switch {
		case s.code:
			st.Font, st.Size, st.Color = monoFont, sp-1, cAccText
		case s.link:
			st.Color = cAccText
		}
		if s.strong {
			st.Font = fSemi(st.Font)
		}
		if s.em {
			st.Font.Style = font.Italic
		}
		out = append(out, st)
	}
	if len(out) == 0 {
		out = append(out, styledtext.SpanStyle{Font: base, Size: sp, Color: c, Content: " "})
	}
	return out
}

func (u *ui) markdown(gtx layout.Context, id, src, suggest string) layout.Dimensions {
	blocks := parseMarkdown(src)
	var kids []layout.FlexChild
	for bi, b := range blocks {
		bid := fmt.Sprintf("%s-%d", id, bi)
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				switch b.kind {
				case "h":
					return u.selRich(gtx, bid, u.spans(b.spans, 14, cText, fSemi(sansFont))...)
				case "li":
					return layout.Inset{Left: unit.Dp(4 + 16*b.level)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								gtx.Constraints.Min.X = gtx.Dp(16)
								return u.label(gtx, b.mark, 13, cText3, sansFont, 1)
							}),
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								return u.selRich(gtx, bid, u.spans(b.spans, 13, cText, sansFont)...)
							}))
					})
				case "quote":
					return boxed(cPanelCard, color.NRGBA{}, rCtl, layout.Inset{Top: 6, Bottom: 6, Left: 12, Right: 12}, func(gtx layout.Context) layout.Dimensions {
						return u.selRich(gtx, bid, u.spans(b.spans, 13, cText2, sansFont)...)
					})(gtx)
				case "code":
					return u.codeBlock(gtx, fmt.Sprintf("%s-%d", id, bi), b, suggest)
				case "table":
					return u.table(gtx, b)
				case "chart":
					return u.chart(gtx, b.chart)
				case "diagram":
					return u.artBlock(gtx, fmt.Sprintf("%s-%d", id, bi), diagramSpans(b.code))
				case "figure":
					return u.artBlock(gtx, fmt.Sprintf("%s-%d", id, bi), figureSpans(b.code))
				case "math":
					return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Top: 4, Bottom: 4}.Layout(gtx, u.lbl(b.code, 15, cPurple, sansFont))
					})
				case "alert":
					lk := alertLooks[b.mark]
					c := lk.c()
					return layout.Stack{}.Layout(gtx,
						layout.Expanded(func(gtx layout.Context) layout.Dimensions {
							paint.FillShape(gtx.Ops, withAlpha(c, 0x18), clip.Rect{Max: gtx.Constraints.Min}.Op())
							paint.FillShape(gtx.Ops, c, clip.Rect{Max: image.Pt(gtx.Dp(3), gtx.Constraints.Min.Y)}.Op())
							return layout.Dimensions{Size: gtx.Constraints.Min}
						}),
						layout.Stacked(func(gtx layout.Context) layout.Dimensions {
							gtx.Constraints.Min.X = gtx.Constraints.Max.X
							return layout.Inset{Top: 6, Bottom: 6, Left: 12, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
									layout.Rigid(u.lbl(lk.title, 12, c, fSemi(sansFont))),
									layout.Rigid(vgap(3)),
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										return u.selRich(gtx, bid, u.spans(b.spans, 13, cText, sansFont)...)
									}))
							})
						}))
				case "hr":
					paint.FillShape(gtx.Ops, cLine, clip.Rect(image.Rect(0, gtx.Dp(4), gtx.Constraints.Max.X, gtx.Dp(5))).Op())
					return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, gtx.Dp(9))}
				}
				return u.selRich(gtx, bid, u.spans(b.spans, 13, cText, sansFont)...)
			})
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

var chromaStyle = styles.Get("github-dark")

func (u *ui) codeBlock(gtx layout.Context, id string, b mdBlock, suggest string) layout.Dimensions {
	return boxed(cPane, cLine, rCtl, layout.Inset{}, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return u.selRich(gtx, id+"code", u.highlight(b.code, b.lang)...)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				paint.FillShape(gtx.Ops, cLine, clip.Rect(image.Rect(0, 0, gtx.Constraints.Max.X, gtx.Dp(1))).Op())
				return layout.Inset{Top: 5, Bottom: 5, Left: 12, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					lang := b.lang
					if lang == "" {
						lang = "text"
					}
					kids := []layout.FlexChild{
						layout.Flexed(1, u.lblFill(lang, 11, cText3, sansFont)),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							d, ok := u.button(gtx, id+"copy", cPanelCard, cHover, rCtl, layout.Inset{Top: 4, Bottom: 4, Left: 10, Right: 10}, u.lbl("Copy", 12, cText2, fSemi(sansFont)))
							if ok {
								u.setClipboard(b.code)
								u.showToast("Copied", false)
							}
							return d
						}),
					}
					if suggest != "" && strings.TrimSpace(b.code) == strings.TrimSpace(suggest) {
						kids = append(kids, layout.Rigid(hgap(6)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							d, ok := u.button(gtx, id+"type", cAccSoft, cHover, rCtl, layout.Inset{Top: 4, Bottom: 4, Left: 10, Right: 10}, u.lbl("Type at prompt  "+modText("Y"), 12, cAccText, fSemi(sansFont)))
							if ok {
								u.d.press("ctrl+y")
								u.wantTerm = true
							}
							return d
						}))
					}
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
				})
			}))
	})(gtx)
}

var hlCache = map[string][]styledtext.SpanStyle{}

// highlight colors code with chroma's github-dark style, once per block.
func (u *ui) highlight(code, lang string) []styledtext.SpanStyle {
	key := lang + "\x00" + code
	if s, ok := hlCache[key]; ok {
		return s
	}
	s := u.tokens(code, lang)
	bound(hlCache, cacheMax)
	hlCache[key] = s
	return s
}

func (u *ui) tokens(code, lang string) []styledtext.SpanStyle {
	lx := lexers.Get(lang)
	if lx == nil {
		lx = lexers.Fallback
	}
	it, err := chroma.Coalesce(lx).Tokenise(nil, code)
	if err != nil {
		return []styledtext.SpanStyle{{Font: monoFont, Size: 12, Color: cText, Content: code}}
	}
	var out []styledtext.SpanStyle
	for tok := it(); tok != chroma.EOF; tok = it() {
		c := cText
		if e := chromaStyle.Get(tok.Type); e.Colour.IsSet() {
			c = color.NRGBA{e.Colour.Red(), e.Colour.Green(), e.Colour.Blue(), 0xff}
		}
		out = append(out, styledtext.SpanStyle{Font: monoFont, Size: 12, Color: c, Content: tok.Value})
	}
	return out
}

func (u *ui) composer(gtx layout.Context, t *guiTab) layout.Dimensions {
	for {
		ev, ok := u.input.Update(gtx)
		if !ok {
			break
		}
		if s, ok := ev.(widget.SubmitEvent); ok && t != nil && strings.TrimSpace(s.Text) != "" { // queued while busy
			u.d.ask(strings.TrimSpace(s.Text))
			u.input.SetText("")
		}
	}
	if u.toggleComposer {
		u.toggleComposer = false
		if gtx.Focused(&u.input) {
			u.wantTerm = true
		} else {
			gtx.Execute(keyFocus(&u.input))
		}
	}
	focused := gtx.Focused(&u.input)
	rim := cLine
	if focused {
		rim = cAcc
	}
	return layout.Inset{Left: 8, Right: 8, Bottom: 8}.Layout(gtx, boxed(cPane, rim, rCard, layout.Inset{Top: 8, Bottom: 6, Left: 10, Right: 8}, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.chips(gtx, t) }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.Y = gtx.Dp(180)
				return u.editor(gtx, &u.input, "Ask Claude… #n or “last N” cites blocks", 13, sansFont, cText)
			}),
			layout.Rigid(vgap(6)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, u.lblFill("↵ send · ⇧↵ new line", 11, cText3, sansFont)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if t != nil && t.Busy { // stop the turn; ↵ still queues a question
							d, ok := u.button(gtx, "stop", cPanelCard, cHover, rCtl, layout.UniformInset(6), icon(16, cText, icoStop))
							if ok {
								u.d.stop()
							}
							return d
						}
						fill := cAcc
						if t == nil {
							fill = cAccSoft
						}
						d, ok := u.button(gtx, "send", fill, fill, rCtl, layout.UniformInset(6), icon(16, cWhite, icoUp))
						if ok && t != nil && strings.TrimSpace(u.input.Text()) != "" {
							u.d.ask(strings.TrimSpace(u.input.Text()))
							u.input.SetText("")
						}
						return d
					}))
			}))
	}))
}

// chips are the blocks pinned for the next question and the screen toggle.
func (u *ui) chips(gtx layout.Context, t *guiTab) layout.Dimensions {
	if t == nil {
		return layout.Dimensions{}
	}
	var kids []layout.FlexChild
	for _, p := range t.Panes {
		if !p.Focused {
			continue
		}
		for n := range t.Pinned {
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d, ok := u.button(gtx, fmt.Sprint("chip", n), cAccSoft, cHover, rCtl, layout.Inset{Top: 2, Bottom: 2, Left: 8, Right: 8}, u.lbl(fmt.Sprintf("#%d pinned", n), 11, cAccText, fSemi(monoFont)))
				if ok {
					u.d.pin(n)
				}
				return d
			}), layout.Rigid(hgap(6)))
		}
		s, fg, bg := "□ screen", cText2, cPane
		if t.PinScreen {
			fg, bg = cAccText, cAccSoft
		} else if t.Live {
			s += " auto"
		}
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			d, ok := u.button(gtx, "chipScreen", bg, cHover, rCtl, layout.Inset{Top: 2, Bottom: 2, Left: 8, Right: 8}, u.lbl(s, 11, fg, fSemi(monoFont)))
			if ok {
				u.d.press("ctrl+s")
			}
			return d
		}))
	}
	if len(kids) == 0 {
		return layout.Dimensions{}
	}
	return layout.Inset{Bottom: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return layout.Flex{}.Layout(gtx, kids...) })
}
