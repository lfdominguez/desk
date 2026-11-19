package infra

import (
	"image"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/x/styledtext"
	"golang.org/x/image/math/fixed"

	deskclip "github.com/lfdominguez/desk/internal/clip"
)

// selText is a chat text block the mouse can select. It lays out its own
// words (shaped one by one, wrapped between words) so inline styles survive:
// Gio's widget.Selectable takes a single style, styledtext can't select.
// ponytail: a selection stays inside one block (paragraph, list item,
// message); crossing blocks needs every block's offset in the chat list.
type selText struct {
	src       []rune
	sel       [2]int // rune offsets, anchor and end; equal: none
	selecting bool
	clicks    clicks
	pieces    []selPiece
	lines     []selLine
	// what pieces and lines were laid out from (layout)
	laid     []styledtext.SpanStyle
	laidW    int
	laidM    unit.Metric
	laidSize image.Point // before the constraints
}

type selPiece struct {
	line, x, at int   // line, left edge, first rune in src
	xs          []int // each rune's left edge from x, then the piece's end
	gl          []text.Glyph
	span        int
}

type selLine struct{ top, h, base, at int } // at: the line's first rune

// selLabel is u.label that can be selected.
func (u *ui) selLabel(gtx layout.Context, id string, s string, sp styledtext.SpanStyle) layout.Dimensions {
	sp.Content = s
	return u.selRich(gtx, id, sp)
}

// selRich draws spans like styledtext.Text and handles selecting them.
func (u *ui) selRich(gtx layout.Context, id string, spans ...styledtext.SpanStyle) layout.Dimensions {
	st := u.sels[id]
	if st == nil {
		st = &selText{}
		u.sels[id] = st
	}
	var src []rune
	for _, sp := range spans {
		src = append(src, []rune(sp.Content)...)
	}
	if !strings.HasPrefix(string(src), string(st.src)) { // new text (a streaming answer only grows)
		st.sel = [2]int{}
	}
	st.src = src
	size := st.layout(gtx, u.sh, spans, &u.selGlyphs)

	for {
		ev, ok := gtx.Event(pointer.Filter{Target: st, Kinds: pointer.Press | pointer.Drag | pointer.Release})
		if !ok {
			break
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch e.Kind {
		case pointer.Press:
			u.clearSelections()
			u.chatSel = st
			i := st.hit(e.Position.Round())
			st.sel, st.selecting = [2]int{i, i}, true
			switch st.clicks.press(e.Position.Round()) {
			case 2:
				if i > 0 && (i == len(st.src) || st.src[i] == ' ' || st.src[i] == '\n') { // hit is the nearest edge: the word may end here
					i--
				}
				lo, hi := wordAt(st.src, i)
				st.sel = [2]int{lo, hi}
			case 3: // the paragraph
				st.sel = [2]int{0, len(st.src)}
			}
		case pointer.Drag:
			if st.selecting && st.clicks.n == 1 {
				st.sel[1] = st.hit(e.Position.Round())
			}
		case pointer.Release:
			if st.selecting {
				st.selecting = false
				u.copyOnSelect(st.selected())
			}
		}
	}

	lo, hi := min(st.sel[0], st.sel[1]), max(st.sel[0], st.sel[1])
	for _, p := range st.pieces {
		a, b := max(0, min(len(p.xs)-1, lo-p.at)), max(0, min(len(p.xs)-1, hi-p.at))
		if a < b {
			l := st.lines[p.line]
			paint.FillShape(gtx.Ops, cAccSoft, clip.Rect(image.Rect(p.x+p.xs[a], l.top, p.x+p.xs[b], l.top+l.h)).Op())
		}
		paintGlyphs(gtx.Ops, u.sh, p.x, st.lines[p.line].base, spans[p.span].Color, p.gl)
	}
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, st)
	pointer.CursorText.Add(gtx.Ops)
	return layout.Dimensions{Size: size}
}

// layout places the words of spans in lines of the width available; the same
// spans (text, font, size) at the same width and scale keep the last layout.
func (st *selText) layout(gtx layout.Context, sh *text.Shaper, spans []styledtext.SpanStyle, gl *[]text.Glyph) image.Point {
	maxW := gtx.Constraints.Max.X
	if st.lines != nil && maxW == st.laidW && gtx.Metric == st.laidM && slices.EqualFunc(spans, st.laid, sameSpan) {
		return gtx.Constraints.Constrain(st.laidSize)
	}
	st.laidW, st.laidM, st.laid = maxW, gtx.Metric, append(st.laid[:0], spans...)
	st.pieces, st.lines = st.pieces[:0], st.lines[:0]
	var size image.Point
	x, at, top, lineAt := 0, 0, 0, 0
	asc, desc, spAsc, spDesc := 0, 0, 0, 0 // the line's metrics; the span's
	endLine := func() {
		h := int(float32(asc+desc) * 1.2)
		st.lines = append(st.lines, selLine{top: top, h: h, base: top + (h-asc-desc)/2 + asc, at: lineAt})
		size.X, top = max(size.X, x), top+h
		x, lineAt, asc, desc = 0, at, spAsc, spDesc
	}
	for si, sp := range spans {
		px := fixed.I(gtx.Sp(sp.Size))
		if shape(sh, sp.Font, px, "M", gl); len(*gl) > 0 {
			spAsc, spDesc = (*gl)[0].Ascent.Ceil(), (*gl)[0].Descent.Ceil()
			asc, desc = max(asc, spAsc), max(desc, spDesc)
		}
		for _, tok := range words(sp.Content) {
			if tok == "\n" {
				at++
				endLine()
				continue
			}
			for tok != "" {
				shape(sh, sp.Font, px, tok, gl)
				xs := runeXs(*gl, utf8.RuneCountInString(tok))
				trim := xs[utf8.RuneCountInString(strings.TrimRight(tok, " "))]
				if x > 0 && x+trim > maxW {
					endLine()
				}
				n := len(xs) - 1
				if trim > maxW-x { // a word wider than the line: cut it
					n = 1
					for n < len(xs)-1 && xs[n+1] <= maxW-x {
						n++
					}
				}
				rs := []rune(tok)
				if n < len(rs) {
					shape(sh, sp.Font, px, string(rs[:n]), gl)
					xs = runeXs(*gl, n)
				}
				st.pieces = append(st.pieces, selPiece{line: len(st.lines), x: x, at: at, xs: xs, gl: append([]text.Glyph(nil), *gl...), span: si})
				x, at, tok = x+xs[n], at+n, string(rs[n:])
				if tok != "" {
					endLine()
				}
			}
		}
	}
	endLine()
	st.laidSize = image.Pt(size.X, top)
	return gtx.Constraints.Constrain(st.laidSize)
}

// sameSpan: a and b shape the same (colors are painted, not laid out).
func sameSpan(a, b styledtext.SpanStyle) bool {
	return a.Content == b.Content && a.Font == b.Font && a.Size == b.Size
}

// words splits s into words with their trailing spaces, and newlines.
func words(s string) []string {
	var out []string
	for s != "" {
		if s[0] == '\n' {
			out, s = append(out, "\n"), s[1:]
			continue
		}
		i := strings.IndexAny(s, " \n")
		if i < 0 {
			return append(out, s)
		}
		for i < len(s) && s[i] == ' ' {
			i++
		}
		out, s = append(out, s[:i]), s[i:]
	}
	return out
}

// runeXs is the left edge of each of n runes shaped in gl, then their end;
// a cluster's width is shared by its runes.
func runeXs(gl []text.Glyph, n int) []int {
	xs := make([]int, 0, n+1)
	start, pos := fixed.I(0), fixed.I(0)
	for _, g := range gl {
		pos += g.Advance
		if g.Runes > 0 {
			k := fixed.Int26_6(g.Runes)
			for i := fixed.Int26_6(0); i < k; i++ {
				xs = append(xs, (start + (pos-start)*i/k).Round())
			}
			start = pos
		}
	}
	for len(xs) < n {
		xs = append(xs, pos.Round())
	}
	return append(xs[:n], pos.Round())
}

// hit is the rune offset nearest to p.
func (st *selText) hit(p image.Point) int {
	if len(st.lines) == 0 || p.Y < 0 {
		return 0
	}
	l := len(st.lines) - 1
	for i, ln := range st.lines {
		if p.Y < ln.top+ln.h {
			l = i
			break
		}
	}
	if p.Y >= st.lines[l].top+st.lines[l].h {
		return len(st.src)
	}
	best := st.lines[l].at
	for _, pc := range st.pieces {
		if pc.line != l {
			continue
		}
		for i := range len(pc.xs) - 1 {
			if p.X < pc.x+(pc.xs[i]+pc.xs[i+1])/2 {
				return pc.at + i
			}
		}
		best = pc.at + len(pc.xs) - 1
	}
	return min(best, len(st.src))
}

func (st *selText) selected() string {
	lo, hi := min(st.sel[0], st.sel[1]), max(st.sel[0], st.sel[1])
	return string(st.src[min(lo, len(st.src)):min(hi, len(st.src))])
}

// clicks counts quick presses at one spot: 2 selects a word, 3 a line.
type clicks struct {
	at  time.Time
	pos image.Point
	n   int
}

func (c *clicks) press(p image.Point) int {
	if d := p.Sub(c.pos); time.Since(c.at) < 400*time.Millisecond && d.X*d.X+d.Y*d.Y < 25 {
		c.n++
	} else {
		c.n = 1
	}
	c.at, c.pos = time.Now(), p
	return c.n
}

// clearSelections drops the chat's and the terminals' selections: one at a time.
func (u *ui) clearSelections() {
	if u.chatSel != nil {
		u.chatSel.sel = [2]int{}
		u.chatSel = nil
	}
	for _, v := range u.terms {
		v.sel = [2]image.Point{}
	}
}

// copyOnSelect puts a finished mouse selection on the clipboard.
func (u *ui) copyOnSelect(s string) {
	if s != "" {
		u.setClipboard(s)
		setPrimary(s) // and the primary selection, as an X terminal: a middle click anywhere pastes it
		u.showToast("Copied", false)
	}
}

// setPrimary and readPrimary are the system's primary selection
// (internal/clip); tests replace them so they never take the desktop's.
var (
	setPrimary  = deskclip.SetPrimary
	readPrimary = deskclip.Primary
)
