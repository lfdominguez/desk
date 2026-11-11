package ui

// Selectable rich text, as the infra chat's (internal/infra/gui_select.go): the
// chat's markdown blocks keep their styles and the mouse still selects them.

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/x/richtext"
	"golang.org/x/image/math/fixed"

	"github.com/lfdominguez/desk/internal/ui/kit"
)

// span is a run of rich text; url makes it a link, opened by a click.
type span struct {
	richtext.SpanStyle
	url string
}

// plainSpans is ss with no links (highlighted code).
func plainSpans(ss []richtext.SpanStyle) []span {
	out := make([]span, len(ss))
	for i, s := range ss {
		out[i] = span{SpanStyle: s}
	}
	return out
}

// richSel is a block of rich text the mouse selects: it lays out its own
// words (shaped one by one, wrapped between words) so inline styles survive:
// Gio's widget.Selectable takes a single style, richtext can't select.
// ponytail: a selection stays inside one block (paragraph, list item, code);
// crossing blocks needs every block's offset in the chat list.
type richSel struct {
	src       []rune
	sel       [2]int // rune offsets, anchor and end; equal: none
	selecting bool
	down      image.Point // where the last press was
	clicks    clicks
	pieces    []selPiece
	lines     []selLine
	key       string      // what pieces were laid out from (richKey)
	size      image.Point // and the size they took
	paint     op.Ops      // their glyphs painted, replayed each frame
	glyphs    op.CallOp
}

// richKey is what a block's layout depends on: the width, and each span's
// text, font, size and color.
func richKey(gtx layout.Context, spans []span) string {
	var b strings.Builder
	fmt.Fprint(&b, gtx.Constraints.Max.X, gtx.Constraints.Min.X, gtx.Metric.PxPerSp)
	for _, sp := range spans {
		fmt.Fprint(&b, "\x00", sp.Font, sp.Size, sp.Color, len(sp.Content), "\x00", sp.Content)
	}
	return b.String()
}

type selPiece struct {
	line, x, at int   // line, left edge, first rune in src
	xs          []int // each rune's left edge from x, then the piece's end
	gl          []text.Glyph
	span        int
}

type selLine struct{ top, h, base, at int } // at: the line's first rune

// rich draws spans and handles selecting them: drag, double-click a word,
// triple-click the block; the selection is copied when the mouse lets go.
// A click (no selection) on a link opens it.
func (u *ui) rich(gtx layout.Context, id string, spans ...span) layout.Dimensions {
	st := u.rsels[id]
	if st == nil {
		st = &richSel{}
		u.rsels[id] = st // pruned by sweepSels once out of sight a while
	}
	u.selSeen[id] = gtx.Now
	var src []rune
	for _, sp := range spans {
		src = append(src, []rune(sp.Content)...)
	}
	if !strings.HasPrefix(string(src), string(st.src)) { // new text (a streaming reply only grows)
		st.sel = [2]int{}
	}
	st.src = src
	if k := richKey(gtx, spans); k != st.key { // shaped again only when the text, its styles or the width change
		st.key = k
		st.size = st.layout(gtx, u.k.Sh, spans, &u.selGl)
		st.paint.Reset()
		m := op.Record(&st.paint)
		for _, p := range st.pieces {
			paintGlyphs(&st.paint, u.k.Sh, p.x, st.lines[p.line].base, spans[p.span].Color, p.gl)
		}
		st.glyphs = m.Stop()
	}
	size := st.size

	for {
		ev, ok := gtx.Event(pointer.Filter{Target: st, Kinds: pointer.Press | pointer.Drag | pointer.Release})
		if !ok {
			break
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		p := e.Position.Round()
		switch e.Kind {
		case pointer.Press:
			if u.chatSel != nil && u.chatSel != st { // one selection at a time
				u.chatSel.sel = [2]int{}
			}
			if u.selLast != nil {
				u.selLast.ClearSelection()
			}
			u.chatSel, st.down = st, p
			i := st.hit(p)
			st.sel, st.selecting = [2]int{i, i}, true
			switch st.clicks.press(p, e.Time) {
			case 2:
				if i > 0 && (i == len(st.src) || st.src[i] == ' ' || st.src[i] == '\n') { // hit is the nearest edge: the word may end here
					i--
				}
				lo, hi := wordAt(st.src, i)
				st.sel = [2]int{lo, hi}
			case 3: // the block
				st.sel = [2]int{0, len(st.src)}
			}
		case pointer.Drag:
			if st.selecting && st.clicks.n == 1 {
				st.sel[1] = st.hit(p)
			}
		case pointer.Release:
			if !st.selecting {
				break
			}
			st.selecting = false
			if s := st.selected(); s != "" {
				gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(s))})
				u.showToast("Copied", false)
			} else if i := st.spanAt(st.down); i >= 0 && spans[i].url != "" && st.clicks.n == 1 {
				openURL(spans[i].url)
			}
		}
	}

	lo, hi := min(st.sel[0], st.sel[1]), max(st.sel[0], st.sel[1])
	for _, p := range st.pieces { // the selection under the words
		a, b := max(0, min(len(p.xs)-1, lo-p.at)), max(0, min(len(p.xs)-1, hi-p.at))
		if a < b {
			l := st.lines[p.line]
			paint.FillShape(gtx.Ops, alpha(kit.CAcc, 0x70), clip.Rect(image.Rect(p.x+p.xs[a], l.top, p.x+p.xs[b], l.top+l.h)).Op())
		}
	}
	st.glyphs.Add(gtx.Ops) // the words, as painted when last shaped
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, st)
	pointer.CursorText.Add(gtx.Ops)
	return layout.Dimensions{Size: size}
}

// layout places the words of spans in lines of the width available.
func (st *richSel) layout(gtx layout.Context, sh *text.Shaper, spans []span, gl *[]text.Glyph) image.Point {
	maxW := gtx.Constraints.Max.X
	st.pieces, st.lines = st.pieces[:0], st.lines[:0]
	var size image.Point
	x, at, top, lineAt := 0, 0, 0, 0
	asc, desc, spAsc, spDesc := 0, 0, 0, 0 // the line's metrics; the span's
	endLine := func() {
		h := int(float32(asc+desc) * chatLead)
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
	return gtx.Constraints.Constrain(image.Pt(size.X, top))
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
func (st *richSel) hit(p image.Point) int {
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

// spanAt is the span drawn under p, -1 none.
func (st *richSel) spanAt(p image.Point) int {
	for _, pc := range st.pieces {
		l := st.lines[pc.line]
		if p.Y >= l.top && p.Y < l.top+l.h && p.X >= pc.x && p.X < pc.x+pc.xs[len(pc.xs)-1] {
			return pc.span
		}
	}
	return -1
}

// selRange is the chat's selection, for sig: it changes what is drawn.
func (u *ui) selRange() [2]int {
	if u.chatSel == nil {
		return [2]int{}
	}
	return u.chatSel.sel
}

func (st *richSel) selected() string {
	lo, hi := min(st.sel[0], st.sel[1]), max(st.sel[0], st.sel[1])
	return string(st.src[min(lo, len(st.src)):min(hi, len(st.src))])
}

// clicks counts quick presses at one spot: 2 selects a word, 3 a block.
// at is the press event's Time (as gesture.Click counts), not the wall
// clock: how late a frame handles the events doesn't split a double click.
type clicks struct {
	at  time.Duration
	pos image.Point
	n   int
}

func (c *clicks) press(p image.Point, at time.Duration) int {
	if d := p.Sub(c.pos); c.n > 0 && at-c.at < 400*time.Millisecond && d.X*d.X+d.Y*d.Y < 25 {
		c.n++
	} else {
		c.n = 1
	}
	c.at, c.pos = at, p
	return c.n
}

// wordAt is the run of rune i's kind (a word, spaces, punctuation) in line.
func wordAt(line []rune, i int) (int, int) {
	if i < 0 || i >= len(line) {
		return i, i
	}
	class := func(r rune) int {
		switch {
		case r == ' ' || r == '\t':
			return 0
		case unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-_./~:@%+=?&#", r):
			return 1
		}
		return 2
	}
	c, lo, hi := class(line[i]), i, i+1
	for lo > 0 && class(line[lo-1]) == c {
		lo--
	}
	for hi < len(line) && class(line[hi]) == c {
		hi++
	}
	return lo, hi
}

func shape(sh *text.Shaper, f font.Font, size fixed.Int26_6, s string, gl *[]text.Glyph) {
	sh.LayoutString(text.Parameters{Font: f, PxPerEm: size, MaxWidth: 1 << 24, MaxLines: 1}, s)
	*gl = (*gl)[:0]
	for g, ok := sh.NextGlyph(); ok; g, ok = sh.NextGlyph() {
		*gl = append(*gl, g)
	}
}

func paintGlyphs(ops *op.Ops, sh *text.Shaper, x, baseline int, c color.NRGBA, gl []text.Glyph) {
	if len(gl) == 0 {
		return
	}
	t := op.Offset(image.Pt(x, baseline)).Push(ops)
	o := clip.Outline{Path: sh.Shape(gl)}.Op().Push(ops)
	paint.ColorOp{Color: c}.Add(ops)
	paint.PaintOp{}.Add(ops)
	o.Pop()
	if call := sh.Bitmaps(gl); call != (op.CallOp{}) {
		call.Add(ops)
	}
	t.Pop()
}
