package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"github.com/oligo/gvcode"
)

// TestFindAll: literal matches as rune ranges (after multibyte text too),
// smart case, at most max, none for an empty query.
func TestFindAll(t *testing.T) {
	r := func(s, e int) gvcode.TextRange { return gvcode.TextRange{Start: s, End: e} }
	for _, c := range []struct {
		text, q string
		max     int
		want    []gvcode.TextRange
	}{
		{"beta Beta BETA", "beta", 10, []gvcode.TextRange{r(0, 4), r(5, 9), r(10, 14)}}, // no capitals: any case
		{"beta Beta BETA", "Beta", 10, []gvcode.TextRange{r(5, 9)}},                     // capitals: exact
		{"ñandú ñu ÑU", "ñu", 10, []gvcode.TextRange{r(6, 8), r(9, 11)}},                 // runes, not bytes
		{"aaaa", "aa", 10, []gvcode.TextRange{r(0, 2), r(2, 4)}},                         // not overlapping
		{"a a a", "a", 2, []gvcode.TextRange{r(0, 1), r(2, 3)}},
		{"abc", "", 10, nil},
		{"abc", "x", 10, nil},
	} {
		got := findAll(c.text, c.q, c.max)
		if len(got) != len(c.want) {
			t.Errorf("findAll(%q, %q) = %v, want %v", c.text, c.q, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("findAll(%q, %q) = %v, want %v", c.text, c.q, got, c.want)
			}
		}
	}
}

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// wsFindTab opens a file holding text in a card's workspace, the editor
// focused.
func wsFindTab(t *testing.T, text string) (*harness, *wsState, *wsTab) {
	h := newHarness(t)
	c, dir := wsCard(h)
	p := filepath.Join(dir, "f.txt")
	writeFile(t, p, text)
	h.u.openWS(c.ID)
	st := h.u.wsOf(c.ID)
	tb := h.u.wsOpen(st, p)
	h.frame()
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: editorAt},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: editorAt})
	tb.ed.SetCaret(0, 0)
	return h, st, tb
}

// sel is the editor's selection, in order.
func sel(tb *wsTab) gvcode.TextRange {
	s, e := tb.ed.Selection()
	return gvcode.TextRange{Start: min(s, e), End: max(s, e)}
}

// TestWorkspaceFind: Ctrl+F opens the bar with the selection as its query;
// typing selects the first match from the caret, Enter and the arrows go
// round them; Esc closes it and gives the editor the keys back.
func TestWorkspaceFind(t *testing.T) {
	h, st, tb := wsFindTab(t, "alpha beta Alpha\nbeta gamma\n")
	tb.ed.SetCaret(22, 27) // gamma
	h.send(press("F", key.ModShortcut))
	h.frame()
	f := st.find
	if f == nil || f.mode != findText || f.q.Text() != "gamma" || !h.u.wsTyping(layoutCtx(h)) {
		t.Fatalf("bar open %v, query %q", f != nil, f.q.Text())
	}
	if !f.q.SingleLine || !f.rep.SingleLine || !f.line.SingleLine || !f.q.Submit || !f.rep.Submit || !f.line.Submit {
		t.Fatal("a field takes Enter as a new line")
	}
	hits := &f.hits[0]
	h.frame()
	if &f.hits[0] != hits {
		t.Fatal("matches looked for again with nothing changed")
	}
	tb.ed.SetCaret(0, 11) // more than a word, on one line: the query
	h.u.wsFindOpen(st, tb, findText)
	tb.ed.SetCaret(6, 21) // over two lines: the query stays
	h.u.wsFindOpen(st, tb, findText)
	if f.q.Text() != "alpha beta " {
		t.Fatalf("query %q", f.q.Text())
	}
	f.q.SetText("gamma")
	tb.ed.SetCaret(22, 27)
	h.send(key.EditEvent{Range: key.Range{Start: 0, End: 5}, Text: "beta"}) // typed over the selected query
	if f.q.Text() != "beta" || len(f.hits) != 2 || sel(tb) != (gvcode.TextRange{Start: 6, End: 10}) || f.at != 0 {
		t.Fatalf("query %q, %d hits, selection %v, at %d", f.q.Text(), len(f.hits), sel(tb), f.at)
	}
	h.send(press(key.NameReturn, 0))
	if sel(tb) != (gvcode.TextRange{Start: 17, End: 21}) || f.at != 1 {
		t.Fatalf("Enter: %v", sel(tb))
	}
	h.send(press(key.NameReturn, 0)) // round to the first
	if f.at != 0 {
		t.Fatalf("Enter at the last: at %d", f.at)
	}
	h.clickButton("wsFindPrev")
	if f.at != 1 {
		t.Fatalf("Previous from the first: at %d", f.at)
	}
	h.clickButton("wsFindNext")
	if f.at != 0 {
		t.Fatalf("Next from the last: at %d", f.at)
	}
	for _, c := range []struct{ caret, dir, want int }{ // from a caret between the matches (6 and 17)
		{12, 0, 1}, {12, 1, 1}, {12, -1, 0}, {17, 0, 1}, {17, 1, 0}, {17, -1, 0}, {6, -1, 1},
	} {
		tb.ed.SetCaret(c.caret, c.caret)
		if wsFindGo(f, tb, c.dir); f.at != c.want {
			t.Errorf("from %d, dir %d: at %d, want %d", c.caret, c.dir, f.at, c.want)
		}
	}
	f.hits = []gvcode.TextRange{{Start: 0, End: 1}, {Start: 6, End: 7}, {Start: 12, End: 13}} // three: going round can't hide a wrong step
	tb.ed.SetCaret(12, 12)
	if wsFindGo(f, tb, -1); f.at != 1 {
		t.Errorf("previous of the third: at %d", f.at)
	}
	f.q.SetText("Alpha") // capitals: that case only
	h.u.wsFindSync(f, tb)
	if len(f.hits) != 1 || f.hits[0].Start != 11 {
		t.Fatalf("Alpha: %v", f.hits)
	}
	h.send(press(key.NameEscape, 0))
	h.frame()
	if st.find != nil || h.u.view != "ws:"+st.id || !layoutCtx(h).Focused(tb.ed) {
		t.Fatalf("Esc: bar %v, view %s", st.find != nil, h.u.view)
	}
}

// TestWorkspaceFindFollows: an edit moves the matches; another tab gets its
// own; the X closes the bar.
func TestWorkspaceFindFollows(t *testing.T) {
	h, st, tb := wsFindTab(t, "one two one\n")
	h.u.wsFindOpen(st, tb, findText)
	st.find.q.SetText("one")
	h.frame()
	f := st.find
	if len(f.hits) != 2 {
		t.Fatalf("%d hits", len(f.hits))
	}
	tb.ed.SetCaret(0, 0)
	tb.ed.Insert("one ")
	h.u.wsEdited(tb)
	h.frame()
	if len(f.hits) != 3 || f.hits[2].Start != 12 {
		t.Fatalf("after the edit: %v", f.hits)
	}
	writeFile(t, filepath.Join(filepath.Dir(tb.path), "g.txt"), "one\n")
	g := h.u.wsOpen(st, filepath.Join(filepath.Dir(tb.path), "g.txt"))
	h.frame()
	if f.tab != g || len(f.hits) != 1 {
		t.Fatalf("other tab: %d hits", len(f.hits))
	}
	h.clickButton("wsFindX")
	if st.find != nil {
		t.Fatal("the X left it open")
	}
}

// TestWorkspaceReplace: Ctrl+H; Enter in Replace replaces the match selected
// and selects the next after it (selecting one first if none is), even when
// the replacement holds the query; All replaces the rest as one undo.
func TestWorkspaceReplace(t *testing.T) {
	h, st, tb := wsFindTab(t, "a b a b a\n")
	h.send(press("H", key.ModShortcut))
	f := st.find
	if f == nil || f.mode != findReplace {
		t.Fatal("Ctrl+H: no replace bar")
	}
	f.q.SetText("a")
	f.rep.SetText("aa")
	h.u.wsFindSync(f, tb)
	h.u.wsReplace(f, tb) // nothing selected yet: selects the first
	if tb.ed.Text() != "a b a b a\n" || f.at != 0 {
		t.Fatalf("replaced before showing: %q", tb.ed.Text())
	}
	tb.closing = true
	h.u.wsReplace(f, tb)
	if tb.ed.Text() != "aa b a b a\n" || !tb.dirty || tb.closing || sel(tb) != (gvcode.TextRange{Start: 5, End: 6}) {
		t.Fatalf("one: %q, selection %v (the next after the replacement)", tb.ed.Text(), sel(tb))
	}
	if f.count() != "3 of 4" { // a, a (the replacement), a, a
		t.Fatalf("count %q", f.count())
	}
	f.rep.SetText("xy")
	h.clickButton("wsReplaceAll")
	if tb.ed.Text() != "xyxy b xy b xy\n" || !strings.Contains(h.u.toast, "Replaced 4 in f.txt") || h.u.toastBad {
		t.Fatalf("all: %q, toast %q", tb.ed.Text(), h.u.toast)
	}
	if f.count() != "No results" {
		t.Fatalf("count after all: %q", f.count())
	}
	h.u.wsReplaceAll(f, tb) // nothing left: no toast
	if strings.Contains(h.u.toast, "Replaced 0") {
		t.Fatal("replaced nothing, said so")
	}
	h.u.focusLater = tb.ed // back in the editor: its Ctrl+Z
	h.frame()
	h.send(press("Z", key.ModShortcut))
	if tb.ed.Text() != "aa b a b a\n" {
		t.Fatalf("one undo didn't take back All: %q", tb.ed.Text())
	}
}

// TestFindCount: what the bar says of the matches.
func TestFindCount(t *testing.T) {
	f := &wsFind{at: -1}
	if f.count() != "" {
		t.Errorf("no query: %q", f.count())
	}
	f.q.SetText("x")
	for _, c := range []struct {
		hits, at int
		want     string
	}{{0, -1, "No results"}, {3, -1, "3 found"}, {3, 0, "1 of 3"}, {3, 2, "3 of 3"}, {wsFindMax, 4, "10000+"}} {
		f.hits, f.at = make([]gvcode.TextRange, c.hits), c.at
		if got := f.count(); got != c.want {
			t.Errorf("%d hits at %d: %q, want %q", c.hits, c.at, got, c.want)
		}
	}
}

// TestWorkspaceGoLine: Ctrl+G, a number and Enter put the caret at that
// line's start (clamped to the file) and close the bar; not a number: nothing.
func TestWorkspaceGoLine(t *testing.T) {
	h, st, tb := wsFindTab(t, "one\ntwo\nthree\n")
	h.send(press("G", key.ModShortcut))
	h.frame()
	if st.find == nil || st.find.mode != findLine || !layoutCtx(h).Focused(&st.find.line) {
		t.Fatal("no line field focused")
	}
	h.send(key.EditEvent{Text: "x"}, press(key.NameReturn, 0))
	if st.find == nil {
		t.Fatal("closed on a non-number")
	}
	st.find.line.SetText("2")
	h.send(press(key.NameReturn, 0))
	if l, c := tb.ed.CaretPos(); l != 1 || c != 0 || st.find != nil {
		t.Fatalf("line %d col %d, bar %v", l, c, st.find != nil)
	}
	h.u.wsFindOpen(st, tb, findLine)
	h.frame()
	st.find.line.SetText("99")
	h.send(press(key.NameReturn, 0))
	if l, _ := tb.ed.CaretPos(); l != tb.ed.Lines()-1 {
		t.Fatalf("past the end: line %d of %d", l, tb.ed.Lines())
	}
}
