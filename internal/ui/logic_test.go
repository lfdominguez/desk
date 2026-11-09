package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/x/richtext"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/image/math/fixed"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/term"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// draw lays w out alone, sz at most, through the harness's router (so
// pointer events reach it), and returns its size.
func (h *harness) draw(sz image.Point, w layout.Widget) layout.Dimensions {
	h.ops.Reset()
	gtx := layout.Context{Ops: &h.ops, Source: h.r.Source(), Now: time.Now(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Constraints{Max: sz}}
	d := w(gtx)
	h.r.Frame(&h.ops)
	return d
}

// clickIn clicks button id of w (drawn by draw), scanning area for it.
func (h *harness) clickIn(sz image.Point, area image.Rectangle, w layout.Widget, id string) {
	c := h.u.k.Click(id)
	h.draw(sz, w)
	if p, ok := h.seek(id, area, func() { h.draw(sz, w) }); ok {
		h.r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p},
			pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
		h.draw(sz, w)
		h.draw(sz, w)
		return
	}
	for y := area.Min.Y + 2; y < area.Max.Y; y += 6 {
		for x := area.Min.X + 2; x < area.Max.X; x += 12 {
			p := f32.Pt(float32(x), float32(y))
			h.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: p})
			h.draw(sz, w)
			if c.Hovered() {
				h.r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p},
					pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
				h.draw(sz, w)
				h.draw(sz, w)
				return
			}
		}
	}
	h.t.Fatalf("button %s is not drawn", id)
}

// gtxOf is a throwaway context for helpers that only measure.
func gtxOf(w int) layout.Context {
	return layout.Context{Ops: new(op.Ops), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Constraints{Max: image.Pt(w, 4000)}}
}

// ---- markdown ----

// TestMarkdownInlines: emphasis, autolinks (mail ones as mailto:), hard
// breaks, ordered lists from their start, indented code, a plain quote.
func TestMarkdownInlines(t *testing.T) {
	const src = "*soft* and **hard**  \nnext <https://example.com/a> <me@example.com>\n\n3. three\n4. four\n\n> just a quote\n\n    indented()\n"
	bs := parseMarkdown(src)
	if len(bs) != 5 {
		t.Fatalf("blocks %+v", bs)
	}
	p := bs[0].spans
	if !p[0].em || p[0].strong || p[0].text != "soft" || !p[2].strong || p[2].em {
		t.Fatalf("emphasis %+v", p)
	}
	var url, mail *mdSpan
	for i := range p {
		switch p[i].text {
		case "https://example.com/a":
			url = &p[i]
		case "me@example.com":
			mail = &p[i]
		}
	}
	if url == nil || url.link != "https://example.com/a" || mail == nil || mail.link != "mailto:me@example.com" {
		t.Fatalf("autolinks %+v", p)
	}
	if !strings.Contains(plain(p), "hard next") {
		t.Fatalf("a hard break reads as a space: %q", plain(p))
	}
	if bs[1].mark != "3." || bs[2].mark != "4." || bs[1].level != 0 {
		t.Fatalf("ordered %+v %+v", bs[1], bs[2])
	}
	if bs[4].kind != "code" || bs[4].lang != "" || bs[4].code != "indented()" {
		t.Fatalf("indented code %+v", bs[4])
	}
	if bs[3].kind != "quote" || plain(bs[3].spans) != "just a quote" {
		t.Fatalf("quote %+v", bs[3])
	}
	if again := parseMarkdown(src); &again[0] != &bs[0] {
		t.Error("the same text parsed twice")
	}
	if p := parseMarkdown("<me@example.com>")[0].spans; len(p) != 1 || p[0].link != "mailto:me@example.com" {
		t.Fatalf("mail %+v", p)
	}
}

// TestMarkdownListParts: a list item's second paragraph has no marker; a
// nested list is one deeper.
func TestMarkdownListParts(t *testing.T) {
	bs := parseMarkdown("- first\n\n  more of it\n- second\n  1. inner\n")
	var got []string
	for _, b := range bs {
		got = append(got, b.kind+"|"+b.mark+"|"+plain(b.spans)+"|"+string(rune('0'+b.level)))
	}
	want := []string{"li|•|first|0", "li||more of it|0", "li|•|second|0", "li|1.|inner|1"}
	if !slices.Equal(got, want) {
		t.Fatalf("%q, want %q", got, want)
	}
}

// TestCachePut: a cache keeps what it is given until it gets big, then
// drops half of it.
func TestCachePut(t *testing.T) {
	c := cache[int]{}
	for i := range 401 {
		c.put(string(rune(1000+i)), i)
	}
	if len(c) != 401 || c.put("x", 7) != 7 || len(c) != 202 || c["x"] != 7 {
		t.Fatalf("len %d", len(c))
	}
}

// TestAlertSpans: the alert marker is cut across spans, styles kept.
func TestAlertSpans(t *testing.T) {
	kind, rest, ok := alertSpans([]mdSpan{{text: "[!TIP]"}, {text: " Use "}, {text: "this", strong: true}})
	if !ok || kind != "TIP" || len(rest) != 2 || rest[0].text != "Use " || rest[1].text != "this" || !rest[1].strong {
		t.Fatalf("%q %+v %v", kind, rest, ok)
	}
	if _, _, ok := alertSpans([]mdSpan{{text: "a [!NOTE] late"}}); ok {
		t.Error("a marker not at the start is no alert")
	}
	if _, _, ok := alertSpans([]mdSpan{{text: "[!DANGER] x"}}); ok {
		t.Error("an unknown kind is no alert")
	}
	bs := parseMarkdown("> [!CAUTION]\n> hot\n")
	if bs[0].kind != "alert" || bs[0].mark != "CAUTION" || plain(bs[0].spans) != "hot" {
		t.Fatalf("%+v", bs[0])
	}
}

// TestSpans: code in mono a size smaller, links in the accent with their
// URL, bold and italic on the font; nothing at all is one blank span.
func TestSpans(t *testing.T) {
	ss := spans([]mdSpan{{text: "c", code: true}, {text: "l", link: "https://example.com"}, {text: "b", strong: true}, {text: "i", em: true}, {text: "p"}}, 14, kit.CText, kit.Sans)
	if ss[0].Font.Typeface != kit.Mono.Typeface || ss[0].Size != 13 || ss[0].Color != kit.CAccText || ss[0].url != "" {
		t.Errorf("code %+v", ss[0])
	}
	if ss[1].url != "https://example.com" || ss[1].Color != kit.CAccText || ss[1].Size != 14 {
		t.Errorf("link %+v", ss[1])
	}
	if ss[2].Font.Weight != font.SemiBold || ss[2].Font.Style == font.Italic {
		t.Errorf("bold %+v", ss[2])
	}
	if ss[3].Font.Style != font.Italic || ss[3].Font.Weight == font.SemiBold {
		t.Errorf("italic %+v", ss[3])
	}
	if ss[4].Color != kit.CText || ss[4].Font != kit.Sans || ss[4].url != "" {
		t.Errorf("plain %+v", ss[4])
	}
	if e := spans(nil, 14, kit.CText, kit.Sans); len(e) != 1 || e[0].Content != " " {
		t.Errorf("empty %+v", e)
	}
}

// TestTableCells: which cells read as numbers and percentages, the meter's
// color by level, and columns fitted to the width.
func TestTableCells(t *testing.T) {
	for _, s := range []string{"42", "42%", "1.5 GB", "$12", "120ms", "~3x", "-7", "2KiB"} {
		if !numCell.MatchString(s) {
			t.Errorf("%q not a number", s)
		}
	}
	for _, s := range []string{"abc", "12 apples", "v1.2-beta"} {
		if numCell.MatchString(s) {
			t.Errorf("%q a number", s)
		}
	}
	if m := pctCell.FindStringSubmatch("12.5 %"); m == nil || m[1] != "12.5" || pctCell.MatchString("12") {
		t.Errorf("pct %q", m)
	}
	for pct, want := range map[float64]color.NRGBA{95: kit.CRed, 90: kit.CRed, 89.9: kit.COrange, 70: kit.COrange, 69.9: kit.CGreen, 0: kit.CGreen} {
		if levelColor(pct) != want {
			t.Errorf("level %v", pct)
		}
	}
	if w := fitColumns([]int{10, 30}, 80); !slices.Equal(w, []int{20, 60}) {
		t.Errorf("spare spread: %v", w)
	}
	if w := fitColumns([]int{10, 30}, 40); !slices.Equal(w, []int{10, 30}) {
		t.Errorf("exact: %v", w)
	}
	if w := fitColumns([]int{200, 10, 100}, 150); !slices.Equal(w, []int{70, 10, 70}) {
		t.Errorf("narrow first: %v", w)
	}
}

// TestHighlight: code keeps its text (tabs as spaces), its keywords get a color of their
// own, an unknown language still reads.
func TestHighlight(t *testing.T) {
	join := func(ss []richtext.SpanStyle) string {
		var b strings.Builder
		for _, s := range ss {
			b.WriteString(s.Content)
		}
		return b.String()
	}
	code := "func main() {\n\treturn\n}"
	hl := highlight(code, "go")
	if join(hl) != strings.ReplaceAll(code, "\t", "    ") /* no tab glyph */ || hl[0].Content != "func" || hl[0].Color == kit.CText || hl[0].Font.Typeface != kit.Mono.Typeface {
		t.Fatalf("%+v", hl)
	}
	if again := highlight(code, "go"); &again[0] != &hl[0] {
		t.Error("highlighted twice")
	}
	if s := highlight("some text", "no-such-lang"); join(s) != "some text" {
		t.Fatalf("fallback %+v", s)
	}
}

// TestMarkdownDraws: every kind of block draws, each selectable by its own
// id: a table's header in capitals, a code block as its code.
func TestMarkdownDraws(t *testing.T) {
	h := newHarness(t)
	src := "# Title\n\n## Sub\n\n### Small\n\ntext\n\n- item\n\n> quote\n\n> [!NOTE]\n> noted\n\n```go\nx := 1\n```\n\n| Name | Used | Note |\n|:-:|---|---|\n| disk | 95% | very long cell that has to wrap because the columns do not fit in the space |\n| mem | 40% | - |\n| cpu | 75% | ok |\n\n---\n"
	d := h.draw(image.Pt(500, 3000), func(gtx layout.Context) layout.Dimensions { return h.u.markdown(gtx, "m", src) })
	if d.Size.Y < 200 || d.Size.X > 500 {
		t.Fatalf("size %v", d.Size)
	}
	bs := parseMarkdown(src)
	ti := slices.IndexFunc(bs, func(b mdBlock) bool { return b.kind == "table" })
	ci := slices.IndexFunc(bs, func(b mdBlock) bool { return b.kind == "code" })
	id := func(i int) string { return "m-" + string(rune('0'+i)) }
	for _, c := range []struct{ id, want string }{
		{id(0), "Title"}, {id(ti) + "-0-0", "NAME"}, {id(ti) + "-0-1", "USED"}, {id(ti) + "-1-1", "95%"}, {id(ci) + "code", "x := 1"}, {id(6), "noted"},
	} {
		st := h.u.rsels[c.id]
		if st == nil || string(st.src) != c.want {
			t.Errorf("%s: %+v, want %q", c.id, st, c.want)
		}
	}
	if st := h.u.rsels[id(ti)+"-1-2"]; st == nil || len(st.lines) < 2 {
		t.Error("a long cell did not wrap")
	}
	if d := h.draw(image.Pt(500, 3000), func(gtx layout.Context) layout.Dimensions { return h.u.table(gtx, "e", mdBlock{kind: "table"}) }); d.Size != (image.Point{}) {
		t.Errorf("an empty table takes %v", d.Size)
	}
}

// ---- diff ----

// TestLineDiffEnds: a common end is kept, a huge middle is all removed then
// all added, nothing is no lines.
func TestLineDiffEnds(t *testing.T) {
	ops := func(ls []diffLine) string {
		var s []string
		for _, l := range ls {
			s = append(s, string(l.op)+l.text)
		}
		return strings.Join(s, ",")
	}
	if got := ops(lineDiff("a\nx\nz\nw\n", "a\ny\nz\nw\n")); got != " a,-x,+y, z, w" {
		t.Fatalf("%q", got)
	}
	if got := ops(lineDiff("1\n2\n3\nx\n4\n", "0\n2\n3\n4\n5\n")); got != "-1,+0, 2, 3,-x, 4,+5" {
		t.Fatalf("the longest common middle: %q", got)
	}
	if got := ops(lineDiff("a\nb\n", "b\n")); got != "-a, b" {
		t.Fatalf("%q", got)
	}
	var a, b []string
	for i := range 1001 {
		a = append(a, "old"+string(rune(0x100+i)))
		b = append(b, "new"+string(rune(0x100+i)))
	}
	big := lineDiff("top\n"+strings.Join(a, "\n")+"\nend\n", "top\n"+strings.Join(b, "\n")+"\nend\n")
	if len(big) != 2004 || big[0] != (diffLine{' ', "top"}) || big[1].op != '-' || big[1001].op != '-' || big[1002].op != '+' || big[2002].op != '+' || big[2003] != (diffLine{' ', "end"}) {
		t.Fatalf("big: %d lines, %v %v %v", len(big), big[1], big[1002], big[2003])
	}
	if splitLines("") != nil || len(lineDiff("", "")) != 0 {
		t.Error("nothing is no lines")
	}
}

// TestWordDiffEdges: too many words says all changed, two blanks are the
// same, spaces alone don't make two lines alike.
func TestWordDiffEdges(t *testing.T) {
	long := strings.Repeat("w ", 201)
	a, b, both, al := wordDiff(long, long+"x")
	if al != 0 || len(a) != 1 || a[0].op != '-' || b[0] != (seg{'+', long + "x"}) || len(both) != 2 {
		t.Fatalf("huge: %v %v %v %v", a, b, both, al)
	}
	if _, _, _, al := wordDiff("", "  "); al != 1 {
		t.Errorf("blanks: %v", al)
	}
	if _, _, _, al := wordDiff("a b", "c d"); al != 0 {
		t.Errorf("only spaces kept: %v", al)
	}
	if _, _, _, al := wordDiff("same", "same"); al != 1 {
		t.Errorf("same: %v", al)
	}
}

// TestDiffRowsWords: in Words, an alike pair merges, a rewrite shows old
// then new, lone lines alone; a gap marks the next edit; a removal after an
// addition starts another block.
func TestDiffRowsWords(t *testing.T) {
	ops := func(rs []diffRow) string {
		var s strings.Builder
		for _, r := range rs {
			if r.op == 0 {
				s.WriteByte('0')
			} else {
				s.WriteByte(r.op)
			}
		}
		return s.String()
	}
	lines := []diffLine{{'-', "return total, nil"}, {'+', "return sum, nil"}, {}, {'-', "x := load()"}, {'+', "for _, r := range rows {"}, {'+', "extra"}, {'-', "gone"}}
	if got := ops(diffRows(lines, diffWords, false)); got != "~0-++-" {
		t.Fatalf("words: %q", got)
	}
	if got := ops(diffRows(lines, diffUnified, false)); got != "-+0-++-" {
		t.Fatalf("unified: %q", got)
	}
	// Auto: a removed-only block is unified even when wide
	if got := ops(diffRows([]diffLine{{' ', "k"}, {'-', "a"}, {'-', "b"}}, diffAuto, true)); got != " --" {
		t.Fatalf("removals: %q", got)
	}
	// Auto: one pair alike, one not: not merged by words
	mixed := []diffLine{{'-', "return total, nil"}, {'-', "x := load()"}, {'+', "return sum, nil"}, {'+', "for _, r := range rows {"}}
	if got := ops(diffRows(mixed, diffAuto, false)); got != "--++" {
		t.Fatalf("mixed: %q", got)
	}
	if rs := diffRows([]diffLine{{' ', "k"}}, diffSplit, true); rs[0].op != '=' || rs[0].r[0].text != "k" {
		t.Fatalf("kept in split: %+v", rs)
	}
}

// TestPairLinesMany: past 40 lines a side, lines pair by position and the
// rest stand alone.
func TestPairLinesMany(t *testing.T) {
	var dels, adds []string
	for i := range 41 {
		dels = append(dels, "value = "+string(rune('a'+i%26)))
	}
	for i := range 43 {
		adds = append(adds, "value = "+string(rune('a'+i%26))+"!")
	}
	ps := pairLines(dels, adds)
	if len(ps) != 43 || ps[0].d != 0 || ps[0].a != 0 || ps[40].d != 40 || ps[40].a != 40 || ps[42].d != -1 || ps[42].a != 42 || ps[42].new[0].text != adds[42] {
		t.Fatalf("%d pairs: %+v", len(ps), ps[len(ps)-1])
	}
	if ps[0].alike < markAlike || ps[0].old[0].op != ' ' || len(ps[0].new) < 2 {
		t.Fatalf("an alike pair marks its words: %+v", ps[0])
	}
	ps = pairLines(append(dels, "x"), adds[:1])
	if len(ps) != 42 || ps[1].a != -1 || ps[1].old[0].text != dels[1] {
		t.Fatalf("more removed: %+v", ps[1])
	}
	// many on one side only is enough: the one added line pairs with the
	// first removed, not with the one it is like
	ps = pairLines(append(slices.Clone(dels[:40]), "zzz yyy"), []string{"zzz yyy!"})
	if ps[0].d != 0 || ps[0].a != 0 {
		t.Fatalf("one side past 40: %+v", ps[0])
	}
}

// TestWrote: a Write's line, its size in the unit it reads best in.
func TestWrote(t *testing.T) {
	for b, want := range map[int]string{512: "512 B", 1023: "1023 B", 1024: "1.0 KB", 2048: "2.0 KB", 3 << 20: "3.0 MB"} {
		if got := wrote(kanban.ChatMsg{Path: "/x/y/main.go", Lines: 7, Bytes: b}); got != "Wrote main.go with 7 lines · "+want {
			t.Errorf("%d: %q", b, got)
		}
	}
}

// TestDiffCells: tabs as four spaces, changed words tinted, kept text
// quieter only when asked.
func TestDiffCells(t *testing.T) {
	cs := diffCells([]seg{{' ', "\tx"}, {'-', "a"}, {'+', "b"}}, "txt", true)
	if len(cs) != 7 || cs[0].r != ' ' || cs[4].r != 'x' || cs[5].bg != diffDelWord || cs[6].bg != diffAddWord || cs[4].bg != (color.NRGBA{}) {
		t.Fatalf("%+v", cs)
	}
	if kw := diffCells([]seg{{'+', "func f() {}"}}, "go", false); kw[0].fg == kit.CText || kw[0].fg != highlight("func f() {}", "go")[0].Color {
		t.Fatalf("not highlighted: %v", kw[0].fg)
	}
	loud := diffCells([]seg{{' ', "\tx"}, {'-', "a"}}, "txt", false)
	if cs[4].fg == loud[4].fg || cs[5].fg != loud[5].fg {
		t.Fatalf("quiet %v loud %v", cs[4].fg, loud[4].fg)
	}
}

// TestDiffOf: a MultiEdit's edits joined by a gap, computed once.
func TestDiffOf(t *testing.T) {
	h := newHarness(t)
	m := kanban.ChatMsg{Edits: []kanban.Edit{{Old: "a\n", New: "b\n"}, {Old: "", New: "c\n"}}}
	d := h.u.diffOf("e1", m)
	if len(d) != 4 || d[2] != (diffLine{}) || d[3] != (diffLine{'+', "c"}) {
		t.Fatalf("%+v", d)
	}
	if d2 := h.u.diffOf("e1", kanban.ChatMsg{}); len(d2) != 4 {
		t.Fatal("computed again")
	}
}

// TestDiffBlock: a long change folds until Show all, which toggles; the
// view switch sets the message's mode and Auto clears it.
func TestDiffBlock(t *testing.T) {
	h := newHarness(t)
	var old, new []string
	for i := range 30 {
		old = append(old, "line "+string(rune('a'+i%26)))
		new = append(new, "LINE "+string(rune('a'+i%26)))
	}
	m := kanban.ChatMsg{Who: "tool", Path: "/src/main.go", Edits: []kanban.Edit{{Old: strings.Join(old, "\n"), New: strings.Join(new, "\n")}}}
	sz := image.Pt(600, 2400)
	w := func(gtx layout.Context) layout.Dimensions { return h.u.diffBlock(gtx, "", "d1", m) }
	folded := h.draw(sz, w)
	foot := func(d layout.Dimensions) image.Rectangle { return image.Rect(0, d.Size.Y-40, 30, d.Size.Y) } // Show all/less is full width
	head := image.Rect(0, 0, sz.X, 40)
	h.clickIn(sz, foot(folded), w, "diffMored1")
	if !h.u.diffOpen["d1"] || h.u.diffOpenCount() != 1 {
		t.Fatal("Show all did not open it")
	}
	all := h.draw(sz, w)
	if all.Size.Y <= folded.Size.Y {
		t.Fatalf("open %v, folded %v", all.Size, folded.Size)
	}
	// from 640 wide a rewrite goes side by side: half the rows
	if wide := h.draw(image.Pt(700, 2400), w); wide.Size.Y >= all.Size.Y*3/4 {
		t.Fatalf("wide %v, narrow %v", wide.Size, all.Size)
	}
	h.clickIn(sz, foot(all), w, "diffMored1")
	if h.u.diffOpen["d1"] || h.u.diffOpenCount() != 0 {
		t.Fatal("Show less did not fold it")
	}
	h.clickIn(sz, head, w, "diffModed12")
	if h.u.diffMode["d1"] != diffSplit {
		t.Fatalf("mode %v", h.u.diffMode)
	}
	h.draw(sz, w) // split rows draw
	h.clickIn(sz, head, w, "diffModed10")
	if _, ok := h.u.diffMode["d1"]; ok {
		t.Fatal("Auto kept a mode")
	}
}

// TestDiffRowDraw: rows are as wide as given; a long line wraps taller; a
// split side without a line still takes the row's height.
func TestDiffRowDraw(t *testing.T) {
	h := newHarness(t)
	gtx := gtxOf(400)
	short := diffCells([]seg{{' ', "x"}}, "go", false)
	long := diffCells([]seg{{' ', strings.Repeat("abcd ", 60)}}, "go", false)
	one, many := h.u.diffHeight(gtx, 400, short), h.u.diffHeight(gtx, 400, long)
	if many < 3*(one-2) || h.u.diffHeight(gtx, 400, nil) != one {
		t.Fatalf("one %d, many %d", one, many)
	}
	for _, r := range []diffRow{{}, {op: ' ', l: []seg{{' ', "k"}}}, {op: '~', l: []seg{{' ', "a "}, {'-', "b"}, {'+', "c"}}},
		{op: '-', l: []seg{{' ', "x"}}}, {op: '+', l: []seg{{' ', "y"}}}, {op: '=', l: []seg{{' ', "k"}}, r: []seg{{' ', "k"}}},
		{op: '|', r: []seg{{' ', strings.Repeat("abcd ", 60)}}}} {
		d := h.u.diffRow(gtxOf(400), r, "main.go")
		if r.op != 0 && d.Size.X != 400 {
			t.Errorf("%q: %v", r.op, d.Size)
		}
		if r.op == '|' && d.Size.Y != h.u.diffHeight(gtx, 200, long) {
			t.Errorf("split row %d tall, its long side %d", d.Size.Y, h.u.diffHeight(gtx, 200, long))
		}
	}
}

// ---- selrich ----

// TestWords: words keep their trailing spaces; newlines stand alone.
func TestWords(t *testing.T) {
	if got := words("a  b\nc"); !slices.Equal(got, []string{"a  ", "b", "\n", "c"}) {
		t.Fatalf("%q", got)
	}
	if got := words("\n\nx "); !slices.Equal(got, []string{"\n", "\n", "x "}) {
		t.Fatalf("%q", got)
	}
	if words("") != nil {
		t.Fatal("nothing has words")
	}
}

// TestRuneXs: each rune's left edge, a cluster's width shared by its
// runes, missing runes at the end.
func TestRuneXs(t *testing.T) {
	g := func(adv, runes int) text.Glyph { return text.Glyph{Advance: fixed.I(adv), Runes: uint16(runes)} }
	if xs := runeXs([]text.Glyph{g(10, 1), g(10, 1), g(10, 1)}, 3); !slices.Equal(xs, []int{0, 10, 20, 30}) {
		t.Errorf("plain %v", xs)
	}
	// a glyph with no runes belongs to the cluster the next one closes
	if xs := runeXs([]text.Glyph{g(20, 2), g(5, 0), g(10, 1)}, 3); !slices.Equal(xs, []int{0, 10, 20, 35}) {
		t.Errorf("cluster %v", xs)
	}
	if xs := runeXs([]text.Glyph{g(10, 1)}, 3); !slices.Equal(xs, []int{0, 10, 10, 10}) {
		t.Errorf("short %v", xs)
	}
}

// TestWordAt: a word takes path punctuation, other punctuation and spaces
// are runs of their own; out of range is nothing.
func TestWordAt(t *testing.T) {
	line := []rune("see ~/a.go(x)  ok")
	for _, c := range []struct{ i, lo, hi int }{{1, 0, 3}, {6, 4, 10}, {10, 10, 11}, {14, 13, 15}, {16, 15, 17}, {-1, -1, -1}, {17, 17, 17}} {
		if lo, hi := wordAt(line, c.i); lo != c.lo || hi != c.hi {
			t.Errorf("%d: %d,%d want %d,%d", c.i, lo, hi, c.lo, c.hi)
		}
	}
}

// TestClicks: quick presses at one spot count up; a slow or far one starts
// over.
func TestClicks(t *testing.T) {
	var c clicks
	p := image.Pt(10, 10)
	if c.press(p) != 1 || c.press(p.Add(image.Pt(2, 2))) != 2 || c.press(p) != 3 {
		t.Fatal("quick presses")
	}
	if c.press(p.Add(image.Pt(5, 0))) != 1 {
		t.Fatal("a far press")
	}
	c.at = time.Now().Add(-time.Second)
	if c.press(c.pos) != 1 {
		t.Fatal("a slow press")
	}
}

// TestRichLayout: words wrap at the width, a word wider than a line is
// cut, newlines break; hit and spanAt find runes and spans.
func TestRichLayout(t *testing.T) {
	h := newHarness(t)
	sp := func(s string) span {
		return span{SpanStyle: richtext.SpanStyle{Font: kit.Sans, Size: 14, Color: kit.CText, Content: s}}
	}
	ss := []span{sp("alpha beta "), sp("gamma\ndelta "), sp(strings.Repeat("x", 40))}
	h.draw(image.Pt(90, 1000), func(gtx layout.Context) layout.Dimensions { return h.u.rich(gtx, "r", ss...) })
	st := h.u.rsels["r"]
	n := 0
	for _, p := range st.pieces {
		n += len(p.xs) - 1
		if end := p.x + p.xs[len(p.xs)-1]; end > 90+8 && p.x > 0 { // trailing spaces may hang over
			t.Errorf("piece at %d ends at %d", p.at, end)
		}
	}
	src := string(st.src)
	if n+1 != len(st.src) || src != "alpha beta gamma\ndelta "+strings.Repeat("x", 40) {
		t.Fatalf("pieces cover %d of %d runes", n, len(st.src))
	}
	if len(st.lines) < 5 {
		t.Fatalf("%d lines", len(st.lines))
	}
	nl := slices.IndexFunc(st.lines, func(l selLine) bool { return l.at == strings.Index(src, "delta") })
	if nl < 0 {
		t.Fatalf("no line starts at delta: %+v", st.lines)
	}
	l0 := st.lines[0]
	if st.hit(image.Pt(0, -1)) != 0 || st.hit(image.Pt(0, l0.top+1)) != 0 || st.hit(image.Pt(0, 1<<20)) != len(st.src) {
		t.Error("hit at the edges")
	}
	if i := st.hit(image.Pt(1000, l0.top+1)); src[i-1] != ' ' || i > len("alpha beta ") {
		t.Errorf("past a line's end: %d", i)
	}
	if st.spanAt(image.Pt(1, st.lines[nl].top+1)) != 1 || st.spanAt(image.Pt(1, st.lines[len(st.lines)-1].top+1)) != 2 || st.spanAt(image.Pt(-5, 1)) != -1 {
		t.Error("spanAt")
	}
	if (&richSel{}).hit(image.Pt(3, 3)) != 0 {
		t.Error("an empty block")
	}
}

// TestRichSelect: a drag selects and copies, a double press a word, a
// triple the block; a press in another block clears this one's; new text
// clears it, grown text keeps it.
func TestRichSelect(t *testing.T) {
	h := newHarness(t)
	sp := func(s string) span {
		return span{SpanStyle: richtext.SpanStyle{Font: kit.Sans, Size: 14, Color: kit.CText, Content: s}}
	}
	txt := "hello brave world"
	sz := image.Pt(600, 200)
	w := func(gtx layout.Context) layout.Dimensions {
		d := h.u.rich(gtx, "s1", sp(txt))
		op.Offset(image.Pt(0, 100)).Add(gtx.Ops)
		h.u.rich(gtx, "s2", sp("other block"))
		return d
	}
	h.draw(sz, w)
	st := h.u.rsels["s1"]
	y := float32(st.lines[0].top + st.lines[0].h/2)
	x := func(i int) float32 { // just right of rune i's left edge
		for _, p := range st.pieces {
			if i >= p.at && i < p.at+len(p.xs)-1 {
				return float32(p.x + p.xs[i-p.at] + 1)
			}
		}
		t.Fatalf("no rune %d", i)
		return 0
	}
	ev := func(k pointer.Kind, px float32, py float32) pointer.Event {
		e := pointer.Event{Kind: k, Source: pointer.Mouse, Position: f32.Pt(px, py)}
		if k != pointer.Release {
			e.Buttons = pointer.ButtonPrimary
		}
		return e
	}
	end := x(11) - 2 // just left of the space: the end of "brave"
	h.r.Queue(ev(pointer.Press, x(6), y), ev(pointer.Move, end, y), ev(pointer.Release, end, y))
	h.draw(sz, w)
	if st.selected() != "brave" || h.u.chatSel != st || h.u.toast != "Copied" || h.u.selRange() != [2]int{6, 11} {
		t.Fatalf("drag selected %q (%v), toast %q", st.selected(), st.sel, h.u.toast)
	}
	time.Sleep(450 * time.Millisecond) // not a double click of the drag's press
	h.r.Queue(ev(pointer.Press, x(13), y), ev(pointer.Release, x(13), y), ev(pointer.Press, x(13), y), ev(pointer.Release, x(13), y))
	h.draw(sz, w)
	if st.selected() != "world" {
		t.Fatalf("double press: %q", st.selected())
	}
	h.r.Queue(ev(pointer.Press, x(13), y), ev(pointer.Release, x(13), y))
	h.draw(sz, w)
	if st.selected() != txt {
		t.Fatalf("triple press: %q", st.selected())
	}
	h.r.Queue(ev(pointer.Press, 2, 110), ev(pointer.Release, 2, 110))
	h.draw(sz, w)
	if st.selected() != "" || h.u.chatSel != h.u.rsels["s2"] {
		t.Fatalf("another block's press left %q", st.selected())
	}
	st.sel = [2]int{0, 5}
	txt += " again"
	h.draw(sz, w)
	if st.selected() != "hello" {
		t.Fatal("grown text lost the selection")
	}
	txt = "different"
	h.draw(sz, w)
	if st.sel != [2]int{} {
		t.Fatal("new text kept the selection")
	}
	h.u.chatSel = nil
	if h.u.selRange() != [2]int{} {
		t.Fatal("no selection has a range")
	}
	if ps := plainSpans([]richtext.SpanStyle{{Content: "a"}, {Content: "b"}}); len(ps) != 2 || ps[1].Content != "b" || ps[1].url != "" {
		t.Fatalf("plainSpans %+v", ps)
	}
}

// ---- ci ----

// TestCIState: an item's state is its worst run's, an error a failure;
// the org's count, tone and sentence follow.
func TestCIState(t *testing.T) {
	h := newHarness(t)
	run := func(s string) []links.Run { return []links.Run{{Status: s}} }
	items := []ciItem{{err: "boom"}, {runs: run(links.CIRunning)}, {runs: run(links.CIPending)}, {runs: run(links.CISuccess)}, {}}
	want := []string{links.CIFailed, links.CIRunning, links.CIRunning, links.CISuccess, ""}
	for i, it := range items {
		if got := ciState(it); got != want[i] {
			t.Errorf("%d: %q want %q", i, got, want[i])
		}
	}
	if ciState(ciItem{runs: []links.Run{{Status: links.CISuccess}, {Status: links.CIFailed}}}) != links.CIFailed {
		t.Error("worst run")
	}
	h.u.ci["Acme"] = items
	if h.u.ciCount("Acme") != 1 || h.u.ciTone("Acme") != kit.CRed || h.u.ciSentence("Acme") != "1 failed · 2 running · 1 passed" {
		t.Fatalf("count %d sentence %q", h.u.ciCount("Acme"), h.u.ciSentence("Acme"))
	}
	for _, c := range []struct {
		its  []ciItem
		want color.NRGBA
	}{{nil, kit.CText3}, {items[3:], kit.CGreen}, {items[1:4], kit.CPurple}, {[]ciItem{items[3], items[1], items[3]}, kit.CPurple}} {
		h.u.ci["Globex"] = c.its
		if got := h.u.ciTone("Globex"); got != c.want {
			t.Errorf("tone of %d items: %v", len(c.its), got)
		}
	}
}

// TestCheckCI: Done cards' links are left out, links without CI too, and
// an org with none left loses its view.
func TestCheckCI(t *testing.T) {
	h := newHarness(t)
	var asked []string
	ciRuns = func(_ context.Context, l kanban.Link, _ orgs.Config) ([]links.Run, error) {
		asked = append(asked, l.URL)
		if strings.HasSuffix(l.URL, "/2") {
			return nil, errors.New("rate limited")
		}
		return []links.Run{{Name: "build", Status: links.CISuccess}}, nil
	}
	defer func() { ciRuns = links.CI }()
	pr := func(n string) kanban.Link {
		return kanban.Link{URL: "https://github.com/acme/api/pull/" + n, Kind: links.KindGitHubPR, Ref: "PR #" + n}
	}
	h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "a", Org: "Acme", Links: []kanban.Link{pr("1"), {URL: "https://example.com", Kind: links.KindURL}}})
	h.u.b.Add(h.u.b.Column("To do"), kanban.Card{Title: "b", Org: "Acme", Links: []kanban.Link{pr("2")}})
	h.u.b.Add(h.u.b.Column("Done"), kanban.Card{Title: "c", Org: "Acme", Links: []kanban.Link{pr("3")}})
	h.u.ci["Globex"] = []ciItem{{}}
	done := make(chan struct{})
	go func() { h.u.checkCI(); close(done) }()
	for wait := true; wait; {
		select {
		case <-done:
			wait = false
		default:
			h.frame()
		}
	}
	h.frame()
	slices.Sort(asked)
	if !slices.Equal(asked, []string{pr("1").URL, pr("2").URL}) {
		t.Fatalf("asked %q", asked)
	}
	its := slices.Clone(h.u.ci["Acme"])
	slices.SortFunc(its, func(a, b ciItem) int { return strings.Compare(a.link.URL, b.link.URL) })
	if len(its) != 2 || its[0].err != "" || len(its[0].runs) != 1 || its[1].err == "" || !strings.Contains(its[1].err, "rate limited") {
		t.Fatalf("items %+v", its)
	}
	if _, ok := h.u.ci["Globex"]; ok {
		t.Fatal("Globex has no CI links left but keeps its view")
	}
}

// TestCheckCINoAccount: a link without an account has no CI here: dropped,
// and an org left with none has no view; an org with an account keeps its
// other links.
func TestCheckCINoAccount(t *testing.T) {
	h := newHarness(t)
	ciRuns = func(_ context.Context, l kanban.Link, _ orgs.Config) ([]links.Run, error) {
		if strings.Contains(l.URL, "/globex/") || strings.HasSuffix(l.URL, "/2") {
			return nil, &links.NoAccountError{Provider: "GitHub", Owner: "globex"}
		}
		return []links.Run{{Name: "build", Status: links.CISuccess}}, nil
	}
	defer func() { ciRuns = links.CI }()
	pr := func(owner, n string) kanban.Link {
		return kanban.Link{URL: "https://github.com/" + owner + "/api/pull/" + n, Kind: links.KindGitHubPR, Ref: "PR #" + n}
	}
	h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "a", Org: "Acme", Links: []kanban.Link{pr("acme", "1"), pr("acme", "2")}})
	h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "b", Org: "Globex", Links: []kanban.Link{pr("globex", "3")}})
	h.u.ci["Globex"] = []ciItem{{link: pr("globex", "3")}} // a view from before
	h.off(h.u.checkCI)
	h.frame()
	if its := h.u.ci["Acme"]; len(its) != 1 || its[0].link.URL != pr("acme", "1").URL || its[0].err != "" || len(its[0].runs) != 1 {
		t.Fatalf("Acme %+v", its)
	}
	if _, ok := h.u.ci["Globex"]; ok {
		t.Fatal("Globex has no account but keeps a CI view")
	}
}

// TestCIView: rows per item: the card, its error or "no runs", its runs and
// the jobs of a run that didn't pass (not passed or skipped ones); the
// search narrows; a card's row opens its session.
func TestCIView(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "Fix login", Org: "Acme"})
	l := func(n string) kanban.Link { return kanban.Link{URL: "https://example.com/" + n, Ref: "PR #" + n} }
	h.u.ci["Acme"] = []ciItem{
		{id: c.ID, key: "DESK-1", title: "Fix login", link: l("1"), runs: []links.Run{{ID: 1, Name: "test", Status: links.CIFailed, Jobs: []links.Job{
			{ID: 1, Name: "unit", Stage: "test", Status: links.CIFailed}, {ID: 2, Name: "lint", Status: links.CISuccess}, {ID: 3, Name: "e2e", Status: links.CISkipped}}}}},
		{id: c.ID, key: "DESK-1", title: "Fix login", link: l("2"), err: "rate limited"},
		{id: c.ID, key: "DESK-1", title: "Fix login", link: l("3")},
		{id: c.ID, key: "DESK-1", title: "Fix login", link: l("4"), runs: []links.Run{{ID: 4, Name: "deploy", Status: links.CISuccess, Jobs: []links.Job{{ID: 5, Name: "ship", Status: links.CISuccess}}}}},
	}
	h.u.view = "ci:Acme"
	sz := image.Pt(900, 2000)
	w := func(gtx layout.Context) layout.Dimensions { return h.u.ciView(gtx) }
	count := func(q string) int {
		h.u.search.SetText(q)
		h.u.ciList.Position.Count = -1
		h.draw(sz, w)
		return h.u.ciList.Position.Count
	}
	if n := count(""); n != 9 {
		t.Fatalf("%d rows, want 9", n)
	}
	if n := count("deploy"); n != 2 {
		t.Fatalf("searching deploy: %d rows", n)
	}
	if n := count("zzzz"); n != -1 {
		t.Fatalf("no match laid out %d rows", n)
	}
	h.u.search.SetText("")
	h.clickIn(sz, image.Rectangle{Max: sz}, w, "ci"+c.ID+l("3").URL)
	if h.u.view != "sess:"+c.ID {
		t.Fatalf("view %q", h.u.view)
	}
}

// ---- cardtools ----

// TestCardToolRun: each tool's answer from what desk knows, and its errors
// for what it can't answer.
func TestCardToolRun(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	since := time.Date(2026, 3, 4, 5, 6, 0, 0, time.UTC)
	var alerts []links.Alert
	var alertErr error
	nagiosAlerts = func(context.Context, orgs.Config) ([]links.Alert, error) { return alerts, alertErr }
	ciRuns = func(_ context.Context, l kanban.Link, _ orgs.Config) ([]links.Run, error) {
		if strings.HasSuffix(l.URL, "/2") {
			return nil, errors.New("rate limited")
		}
		if strings.HasSuffix(l.URL, "/3") {
			return nil, nil
		}
		return []links.Run{{Name: "ci", Status: links.CIFailed, URL: "https://example.com/run", Jobs: []links.Job{{ID: 7, Name: "compile", Stage: "build", Status: links.CIFailed}, {ID: 8, Name: "vet", Status: links.CISuccess}}}}, nil
	}
	defer func() { ciRuns = links.CI }()
	pr := func(n string) kanban.Link {
		return kanban.Link{URL: "https://github.com/acme/api/pull/" + n, Kind: links.KindGitHubPR, Ref: "PR #" + n}
	}
	c := kanban.Card{Title: "x", Org: "Acme", Links: []kanban.Link{{URL: "https://example.com/doc", Kind: links.KindURL, Ref: "doc"}, pr("1"), pr("2"), pr("3")}}
	run := func(cfg orgs.Config, name, url string) (string, error) {
		return h.u.cardToolRun(ctx, c, cfg, name, url, 0, 0)
	}
	if _, err := run(orgs.Config{}, "alerts", ""); err == nil || !strings.Contains(err.Error(), "Acme has no [[nagios]]") {
		t.Errorf("no nagios: %v", err)
	}
	nag := orgs.Config{Nagios: []orgs.Nagios{{}}}
	if s, err := run(nag, "alerts", ""); s != "No warnings or errors." || err != nil {
		t.Errorf("none: %q %v", s, err)
	}
	alertErr = errors.New("down")
	if _, err := run(nag, "alerts", ""); err == nil {
		t.Error("an error with no alerts")
	}
	alerts = []links.Alert{{Monitor: "main", Host: "web1", Service: "HTTP", State: "critical", Output: "503", Since: since, Acked: true}, {Monitor: "main", Host: "db1", State: "down", Since: since}}
	s, err := run(nag, "alerts", "")
	if err != nil || s != "CRITICAL web1 / HTTP since Mar 4 05:06 [acknowledged] (main): 503\nDOWN db1 since Mar 4 05:06 (main): \n" {
		t.Errorf("alerts %q %v", s, err)
	}
	if s, err := run(orgs.Config{}, "tickets", ""); s != "No open tickets assigned." || err != nil {
		t.Errorf("tickets %q %v", s, err)
	}
	s, err = run(orgs.Config{}, "ci", "")
	want := "PR #1 " + pr("1").URL + "\n  ci: failed https://example.com/run\n    job 7 build / compile: failed\n    job 8 vet: success\n" +
		"PR #2 " + pr("2").URL + "\n  error: rate limited\n" + "PR #3 " + pr("3").URL + "\n  no CI runs\n"
	if err != nil || s != want {
		t.Errorf("ci %q\nwant %q", s, want)
	}
	if s, err := run(orgs.Config{}, "ci", pr("1").URL); err != nil || !strings.HasPrefix(s, "PR #1 ") || strings.Contains(s, "PR #2") {
		t.Errorf("ci of a url: %q %v", s, err)
	}
	if _, err := run(orgs.Config{}, "ci", "not a link"); err == nil || !strings.Contains(err.Error(), "is not a link desk reads") {
		t.Errorf("a bad url: %v", err)
	}
	c.Links = c.Links[:1]
	if _, err := run(orgs.Config{}, "ci", ""); err == nil || !strings.Contains(err.Error(), "no such link") {
		t.Errorf("no CI link: %v", err)
	}
	if _, err := run(orgs.Config{}, "ci_log", ""); err == nil || !strings.Contains(err.Error(), "no such link") {
		t.Errorf("ci_log, no CI link: %v", err)
	}
	if _, err := run(orgs.Config{}, "ci_log", "https://example.com/doc"); err == nil || !strings.Contains(err.Error(), "has no CI desk reads") {
		t.Errorf("ci_log of a plain url: %v", err)
	}
	if _, err := run(orgs.Config{}, "link", "nope"); err == nil {
		t.Error("link of no link")
	}
	if _, err := run(orgs.Config{}, "frobnicate", ""); err == nil || err.Error() != "no tool frobnicate" {
		t.Errorf("unknown tool: %v", err)
	}
}

// TestCardToolBadConfig: an org whose desk.toml doesn't read fails the
// tool with why.
func TestCardToolBadConfig(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	if err := writeAt(orgs.File(h.u.root, "Acme"), []byte("[[nagios]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var text string
	var ok bool
	done := make(chan struct{})
	go func() { text, ok = h.u.cardTool(c.ID, "alerts", []byte(`{"bytes":5}`)); close(done) }()
	for wait := true; wait; {
		select {
		case <-done:
			wait = false
		default:
			h.frame()
		}
	}
	if ok || !strings.Contains(text, "desk.toml") {
		t.Fatalf("%v %q", ok, text)
	}
}

// ---- sessions ----

// TestOnEventEnds: how a turn's end leaves the card: failed with the
// error, stopped, blocked on permissions, done; usage, artifacts and
// cost on the card; an exit fails a working card.
func TestOnEventEnds(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "x", Org: "Acme"})
	id := c.ID
	card := func() *kanban.Card { return h.u.b.Card(id) }
	col := func() string { i, _ := h.u.b.Find(id); return h.u.b.Columns[i].Title }
	on := func(base float64, e claude.Event) { h.u.onEvent(id, base, e) }

	on(0, claude.Event{Kind: claude.KindInit, Session: "s1"})
	if card().Status != kanban.StatusWorking || card().Session.ID != "s1" {
		t.Fatalf("init %+v", card())
	}
	on(0, claude.Event{Kind: claude.KindUsage, Context: 1234})
	on(0, claude.Event{Kind: claude.KindArtifact, Text: "https://example.com/a"})
	on(0, claude.Event{Kind: claude.KindArtifact, Text: "https://example.com/a"})
	if card().Session.Context != 1234 || len(card().Artifacts) != 1 {
		t.Fatalf("usage, artifacts %+v", card())
	}
	on(1.5, claude.Event{Kind: claude.KindResult, Error: true, Text: "API error\nmore detail", Cost: 0.25, Window: 200000, Tokens: 10})
	if card().Status != kanban.StatusFailed || card().Note != "API error" || card().Session.Cost != 1.75 || card().Session.Window != 200000 || card().Session.Tokens != 10 {
		t.Fatalf("error %+v", card())
	}
	if ms := h.u.chat(id); ms[len(ms)-1].Who != "error" {
		t.Fatalf("chat %+v", ms)
	}
	on(0, claude.Event{Kind: claude.KindResult, Error: true, Window: 1000})
	if card().Status != kanban.StatusWaiting || card().Note != "Stopped" || card().Session.Window != 200000 || card().Session.Turns != 2 {
		t.Fatalf("stopped %+v", card())
	}
	on(0, claude.Event{Kind: claude.KindAsk, Ask: &claude.Request{ID: "r1", Line: "Bash rm -rf build"}})
	if card().Status != kanban.StatusWaiting || card().Note != "Wants to run Bash rm -rf build: allow or deny" || h.u.asks[id] == nil {
		t.Fatalf("ask %+v", card())
	}
	on(0, claude.Event{Kind: claude.KindResult, Denied: []string{"Bash", "Bash", "Write"}})
	if h.u.asks[id] != nil || card().Status != kanban.StatusBlocked || card().Note != "Needs permission for Bash, Write: open in terminal" || col() != "Blocked" {
		t.Fatalf("denied %+v in %s", card(), col())
	}
	on(0, claude.Event{Kind: claude.KindText, Text: "All good.\nDESK-STATUS: done — shipped"})
	on(0, claude.Event{Kind: claude.KindResult})
	if card().Status != kanban.StatusReview || card().Note != "shipped" || col() != "Review" || card().Last != "All good." { // only a person moves it to Done
		t.Fatalf("done %+v in %s", card(), col())
	}
	on(0, claude.Event{Kind: claude.KindResult})
	if card().Status != kanban.StatusWaiting || card().Note != "" {
		t.Fatalf("plain end %+v", card())
	}

	h.u.procs[id] = &fakeClaude{}
	card().Status = kanban.StatusWorking
	on(0, claude.Event{Kind: claude.KindExit})
	if card().Status != kanban.StatusFailed || card().Note != "Claude exited" || h.u.procs[id] != nil {
		t.Fatalf("exit %+v", card())
	}
	card().Status = kanban.StatusWaiting
	on(0, claude.Event{Kind: claude.KindExit, Text: "killed\nby signal"})
	if card().Status != kanban.StatusFailed || card().Note != "killed" {
		t.Fatalf("exit with text %+v", card())
	}
	card().Status = kanban.StatusWaiting
	on(0, claude.Event{Kind: claude.KindExit})
	if card().Status != kanban.StatusWaiting {
		t.Fatal("a quiet exit failed a waiting card")
	}
	h.u.onEvent("gone", 0, claude.Event{Kind: claude.KindInit}) // no card: nothing
}

// TestToolMsg: a tool use keeps its line, command, file and edits.
func TestToolMsg(t *testing.T) {
	m := toolMsg(claude.Event{Text: "Edit main.go", Cmd: "c", Detail: "d", Path: "/x/main.go", Lines: 3, Bytes: 9, Edits: []claude.Edit{{Old: "a", New: "b"}}})
	if m.Who != "tool" || m.Text != "Edit main.go" || m.Cmd != "c" || m.Detail != "d" || m.Path != "/x/main.go" || m.Lines != 3 || m.Bytes != 9 || len(m.Edits) != 1 || m.Edits[0] != (kanban.Edit{Old: "a", New: "b"}) {
		t.Fatalf("%+v", m)
	}
}

// TestFirstPrompt: the card's notes (with the image hint when they hold
// one), its links, then the user's words; firstLine cuts at a line and at
// 120 runes.
func TestFirstPrompt(t *testing.T) {
	c := &kanban.Card{Title: "Fix it", Notes: "see ![image](/tmp/a.png)", Links: []kanban.Link{{URL: "https://example.com/pr/1", Ref: "PR #1"}, {URL: "https://example.com/x", Ref: "x"}}}
	p := firstPrompt(c, "go")
	if !strings.Contains(p, "Notes:\nsee ![image](/tmp/a.png)\n"+imageHint+"\n") || !strings.Contains(p, "\nLinks:\n- PR #1 https://example.com/pr/1\n- x https://example.com/x\n") || !strings.HasSuffix(p, "\ngo") || strings.Contains(p, "DESK-TICKET") {
		t.Fatalf("%q", p)
	}
	if p := firstPrompt(&kanban.Card{Title: "t"}, "go"); strings.Contains(p, "Notes:") || strings.Contains(p, "Links:") {
		t.Fatalf("bare %q", p)
	}
	if got := firstLine("  one\ntwo"); got != "one" {
		t.Errorf("%q", got)
	}
	long := strings.Repeat("é", 130)
	if got := []rune(firstLine(long)); len(got) != 120 || got[119] != '…' {
		t.Errorf("%d runes", len(got))
	}
	if got := firstLine(strings.Repeat("a", 120)); got != strings.Repeat("a", 120) {
		t.Error("120 runes cut")
	}
}

// TestDeliverPaths: an archived card takes nothing; an open terminal takes
// the words instead; words typed over an open question answer it; a failed
// start or send leaves the card as it was; the session's extras.
func TestDeliverPaths(t *testing.T) {
	h := newHarness(t)
	fake := &fakeClaude{}
	startClaude = func(o claude.Options, on func(claude.Event)) (session, error) {
		fake.opts, fake.on = o, on
		return fake, nil
	}
	c := h.u.b.Add(h.u.b.Column("To do"), kanban.Card{Title: "x", Org: "Acme", Alert: "Acme/web1/HTTP"})
	id := c.ID
	card := func() *kanban.Card { return h.u.b.Card(id) }

	card().Archived = time.Now()
	h.u.send(id, "hi")
	card().Archived = time.Time{}
	h.u.send(id, "   ")
	if len(fake.prompts) != 0 {
		t.Fatal("archived or blank text was sent")
	}

	h.u.terms[id] = &termTab{}
	h.u.send(id, "hi")
	if len(fake.prompts) != 0 || h.u.view != "term:"+id || !strings.Contains(h.u.toast, "is open in its terminal") {
		t.Fatalf("with a terminal: view %q toast %q", h.u.view, h.u.toast)
	}
	delete(h.u.terms, id)

	startClaude = func(claude.Options, func(claude.Event)) (session, error) { return nil, errors.New("no claude here") }
	h.u.send(id, "hi")
	if h.u.procs[id] != nil || h.u.toast != "Claude: no claude here" || card().Status == kanban.StatusWorking {
		t.Fatalf("failed start: toast %q", h.u.toast)
	}
	bad := &sendFails{}
	startClaude = func(claude.Options, func(claude.Event)) (session, error) { return bad, nil }
	h.u.send(id, "hi")
	if h.u.toast != "Claude: pipe closed" || card().Status == kanban.StatusWorking || len(h.u.chat(id)) != 0 {
		t.Fatalf("failed send: toast %q, %+v", h.u.toast, card())
	}
	delete(h.u.procs, id)

	startClaude = func(o claude.Options, on func(claude.Event)) (session, error) {
		fake.opts, fake.on = o, on
		return fake, nil
	}
	h.u.cardSock = "/run/desk/cards.sock"
	h.u.shellOut[id] = "<bash-input>ls</bash-input>"
	h.u.send(id, "hi")
	if len(fake.prompts) != 1 || !strings.HasPrefix(fake.prompts[0], "The user ran these commands directly in the card's folder, not through you:\n<bash-input>ls</bash-input>\n") || h.u.shellOut[id] != "" {
		t.Fatalf("prompt %q", fake.prompts)
	}
	if !strings.Contains(fake.opts.System, claude.AlertSnooze) || !strings.Contains(fake.opts.System, claude.CardTools) {
		t.Fatalf("system %q", fake.opts.System)
	}
	if col, _ := h.u.b.Find(id); col != h.u.b.Column("In progress") || card().Status != kanban.StatusWorking || card().Last != "hi" {
		t.Fatalf("after send %+v", card())
	}
	h.u.send(id, "look ![image](/tmp/a.png)")
	if p := fake.prompts[1]; p != "look ![image](/tmp/a.png)\n"+imageHint {
		t.Fatalf("second prompt %q", p)
	}
	h.u.deliver(id, "quiet note", true)
	if n := len(h.u.chat(id)); n != 2 || card().Last != "look ![image](/tmp/a.png)" {
		t.Fatalf("a quiet note in the chat: %d", n)
	}

	h.u.asks[id] = newAsk(&claude.Request{ID: "r7", Line: "Bash ls"})
	h.u.send(id, "use the other one")
	if len(fake.replies) != 1 || fake.replies[0] != "r7 deny The user answered in their own words instead: use the other one" || h.u.asks[id] != nil || len(fake.prompts) != 3 || card().Last != "use the other one" {
		t.Fatalf("replies %q prompts %d", fake.replies, len(fake.prompts))
	}
}

type sendFails struct{ fakeClaude }

func (*sendFails) Send(string) error { return errors.New("pipe closed") }

// TestMakeTicketFails: a ticket that can't be created blocks the card with
// why; a card that's gone makes none.
func TestMakeTicketFails(t *testing.T) {
	h := newHarness(t)
	calls := 0
	createTicket = func(context.Context, orgs.Config, string, string) (kanban.Link, error) {
		calls++
		return kanban.Link{}, errors.New("403 Forbidden\n{json}")
	}
	defer func() { createTicket = links.Create }()
	h.u.makeTicket("gone", "t", "b")
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	h.u.makeTicket(c.ID, "Title", "Body")
	for end := time.Now().Add(2 * time.Second); h.u.b.Card(c.ID).Status != kanban.StatusBlocked && time.Now().Before(end); {
		h.frame()
	}
	if got := h.u.b.Card(c.ID); calls != 1 || got.Note != "Ticket not created: 403 Forbidden" || !strings.Contains(h.u.toast, "ticket not created") {
		t.Fatalf("calls %d, %+v", calls, got)
	}
}

// TestNewCards: split-off cards go to To do with their source's org and
// folder, long titles cut at 80 runes, each to make its ticket.
func TestNewCards(t *testing.T) {
	h := newHarness(t)
	src := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "x", Org: "Globex", Dir: "api"})
	h.u.newCards(src.ID, nil)
	h.u.newCards("gone", []string{"y"})
	long := strings.Repeat("a", 85)
	h.u.newCards(src.ID, []string{"short one", long})
	todo := h.u.b.Columns[h.u.b.Column("To do")].Cards
	if len(todo) != 2 || todo[0].Title != "short one" || todo[0].Org != "Globex" || todo[0].Dir != "api" || !todo[0].Ticket || !strings.Contains(todo[0].Notes, "(Split off "+src.Key()+".)") {
		t.Fatalf("%+v", todo)
	}
	if r := []rune(todo[1].Title); len(r) != 80 || r[79] != '…' || !strings.HasPrefix(todo[1].Notes, long) {
		t.Fatalf("long %q", todo[1].Title)
	}
}

// TestAddLinks: links once each, junk dropped; linkedIDs lists the cards
// with any; moveTo ignores a column that isn't there.
func TestAddLinks(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	h.u.b.Add(0, kanban.Card{Title: "y"})
	h.u.addLinks(h.u.b.Card(c.ID), []string{"https://example.com/a", "not a link", "https://example.com/a"})
	h.frame()
	if ls := h.u.b.Card(c.ID).Links; len(ls) != 1 || ls[0].URL != "https://example.com/a" {
		t.Fatalf("links %+v", ls)
	}
	if ids := h.u.linkedIDs(); !slices.Equal(ids, []string{c.ID}) {
		t.Fatalf("linked %q", ids)
	}
	h.u.moveTo(c.ID, "Nowhere")
	if col, _ := h.u.b.Find(c.ID); col != 0 {
		t.Fatal("moved to no column")
	}
	h.u.moveTo(c.ID, "Blocked")
	if col, _ := h.u.b.Find(c.ID); col != h.u.b.Column("Blocked") {
		t.Fatal("not moved")
	}
}

// ---- terminal ----

// TestTermKey: special keys by name (shift kept only on Tab and Enter),
// letters only with Ctrl or Alt (lower case; Alt's carry their text), a
// plain space is text.
func TestTermKey(t *testing.T) {
	type want struct {
		ok   bool
		code rune
		mod  uv.KeyMod
		text string
	}
	for _, c := range []struct {
		e key.Event
		w want
	}{
		{key.Event{Name: key.NameReturn, State: key.Release}, want{}},
		{press(key.NameReturn, 0), want{true, uv.KeyEnter, 0, ""}},
		{press(key.NameReturn, key.ModShift), want{true, uv.KeyEnter, uv.ModShift, ""}},
		{press(key.NameTab, key.ModShift), want{true, uv.KeyTab, uv.ModShift, ""}},
		{press(key.NameUpArrow, key.ModShift), want{true, uv.KeyUp, 0, ""}},
		{press(key.NameF5, key.ModAlt), want{true, uv.KeyF5, uv.ModAlt, ""}},
		{press(key.NameSpace, 0), want{}},
		{press(key.NameSpace, key.ModShift), want{}},
		{press(key.NameSpace, key.ModCtrl), want{true, uv.KeySpace, uv.ModCtrl, ""}},
		{press("A", 0), want{}},
		{press("A", key.ModShift), want{}},
		{press("C", key.ModCtrl), want{true, 'c', uv.ModCtrl, ""}},
		{press("X", key.ModAlt), want{true, 'x', uv.ModAlt, "x"}},
		{press("V", key.ModCtrl|key.ModShift), want{true, 'v', uv.ModCtrl, ""}},
		{press(key.NameCtrl, key.ModCtrl), want{}},
	} {
		k, ok := termKey(c.e)
		got := want{ok, k.Code, k.Mod, k.Text}
		if got != c.w {
			t.Errorf("%v %v: %+v, want %+v", c.e.Name, c.e.Modifiers, got, c.w)
		}
	}
}

// TestCellColor: no color is the default, the 16 ANSI ones desk's own,
// others as they are.
func TestCellColor(t *testing.T) {
	def := color.NRGBA{1, 2, 3, 4}
	if cellColor(nil, def) != def || cellColor(ansi.BasicColor(1), def) != ansi16[1] || cellColor(ansi.BasicColor(9), def) != ansi16[9] ||
		cellColor(ansi.IndexedColor(12), def) != ansi16[12] {
		t.Fatal("ANSI colors")
	}
	if got := cellColor(color.NRGBA{10, 20, 30, 255}, def); got != (color.NRGBA{10, 20, 30, 255}) {
		t.Fatalf("rgb %v", got)
	}
	if got := cellColor(ansi.IndexedColor(196), def); got == ansi16[196&15] || got.A != 0xff || got.R < 0xc0 {
		t.Fatalf("indexed 196 %v", got)
	}
}

// TestTerminalLife: opening lets go of the card's -p process and carries
// the card's extras; again it just shows it; the wheel scrolls back and
// typing returns to the live screen; Ctrl+V pastes a clipboard image as its
// saved file; a Claude that exits with an error is reaped with that note;
// a terminal that can't start says why.
func TestTerminalLife(t *testing.T) {
	h := newHarness(t)
	var args []string
	var seq strings.Builder // seq 1 300
	for i := 1; i <= 300; i++ {
		fmt.Fprintln(&seq, i)
	}
	script := []string{"out:" + seq.String(), "cat"}
	startTerm = func(cmd *exec.Cmd, w, ht int, notify func()) (*term.Term, error) {
		args = cmd.Args
		return term.Start(prog(script...), w, ht, notify)
	}
	defer func() { startTerm = term.Start }()
	t.Cleanup(func() {
		for id := range h.u.terms {
			h.u.closeTerm(id)
		}
	})
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme", Alert: "Acme/web1/HTTP"})
	id := c.ID
	h.u.terminal("gone")
	h.u.procs[id] = &fakeClaude{}
	h.u.cardSock = "/run/desk/cards.sock"
	h.u.terminal(id)
	j := strings.Join(args, " ")
	if h.u.procs[id] != nil || !strings.Contains(j, strings.TrimSpace(claude.CardTools)) || !strings.Contains(j, strings.TrimSpace(claude.AlertSnooze)) {
		t.Fatalf("procs %v, args %q", h.u.procs[id], j)
	}
	tt := h.u.terms[id]
	h.u.view = ""
	h.u.terminal(id)
	if h.u.view != "term:"+id || h.u.terms[id] != tt {
		t.Fatal("opening again")
	}
	for end := time.Now().Add(5 * time.Second); tt.t.Scrollback() < 100; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("scrollback %d", tt.t.Scrollback())
		}
		h.frame()
	}
	at := f32.Pt(900, 400)
	h.send(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: at, Scroll: f32.Pt(0, -60)})
	if tt.scroll <= 0 {
		t.Fatalf("scrolled to %d", tt.scroll)
	}
	up := tt.scroll
	h.send(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: at, Scroll: f32.Pt(0, 1)})
	if tt.scroll != up-1 {
		t.Fatalf("a small wheel step: %d from %d", tt.scroll, up)
	}
	h.send(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: at, Scroll: f32.Pt(0, -1e5)})
	if tt.scroll != tt.t.Scrollback() {
		t.Fatalf("scrolled past the top: %d of %d", tt.scroll, tt.t.Scrollback())
	}
	h.send(key.EditEvent{Text: "q"})
	if tt.scroll != 0 {
		t.Fatal("typing kept the scrollback")
	}
	h.send(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: at, Scroll: f32.Pt(0, 60)})
	if tt.scroll != 0 {
		t.Fatalf("scrolled below the live screen: %d", tt.scroll)
	}

	clipImage = func() ([]byte, string, error) { return []byte("\x89PNG fake"), "image/png", nil }
	tt.scroll = 3
	h.send(press("V", key.ModCtrl))
	for end := time.Now().Add(2 * time.Second); tt.scroll != 0 && time.Now().Before(end); {
		h.frame()
	}
	if ents, _ := os.ReadDir(h.u.imagesDir()); len(ents) != 1 || tt.scroll != 0 {
		t.Fatalf("pasted image: %v, scroll %d", ents, tt.scroll)
	}

	script = []string{"exit:3"}
	d := h.u.b.Add(0, kanban.Card{Title: "y", Org: "Acme"})
	h.u.terminal(d.ID)
	for end := time.Now().Add(5 * time.Second); h.u.terms[d.ID] != nil && time.Now().Before(end); {
		h.frame()
	}
	if h.u.terms[d.ID] != nil || h.u.b.Card(d.ID).Note != "Claude exited in its terminal" || h.u.view != "" {
		t.Fatalf("reaped: note %q view %q", h.u.b.Card(d.ID).Note, h.u.view)
	}
	h.u.closeTerm(d.ID) // none open: nothing

	startTerm = func(*exec.Cmd, int, int, func()) (*term.Term, error) { return nil, errors.New("no pty") }
	e := h.u.b.Add(0, kanban.Card{Title: "z", Org: "Acme"})
	h.u.terminal(e.ID)
	if h.u.terms[e.ID] != nil || h.u.toast != "Terminal: no pty" || h.u.b.Card(e.ID).Note != "" {
		t.Fatalf("failed start: toast %q", h.u.toast)
	}
	h.u.b.Card(e.ID).Archived = time.Now()
	h.u.toast = ""
	h.u.terminal(e.ID)
	if h.u.toast != "" {
		t.Fatal("an archived card's terminal tried to start")
	}
}
