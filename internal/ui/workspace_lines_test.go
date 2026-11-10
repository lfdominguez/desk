package ui

import (
	"strings"
	"testing"

	"gioui.org/io/key"
)

// at is the rune offset of the first sub in text, plus d.
func at(text, sub string, d int) int { return len([]rune(text[:strings.Index(text, sub)])) + d }

// apply is text after c.
func apply(text string, c lineEdit) string {
	r := []rune(text)
	return string(r[:c.lo]) + c.text + string(r[c.hi:])
}

// TestLineSpan: the whole lines a selection touches, newline included; a
// selection ending at a line's start leaves that line out.
func TestLineSpan(t *testing.T) {
	r := []rune("one\ntwo\nthree")
	for _, c := range []struct{ s, e, lo, hi int }{
		{0, 0, 0, 4}, {5, 5, 4, 8}, {6, 2, 0, 8}, {2, 4, 0, 4}, {2, 5, 0, 8}, {9, 9, 8, 13}, {13, 13, 8, 13}, {4, 4, 4, 8},
	} {
		if lo, hi := lineSpan(r, c.s, c.e); lo != c.lo || hi != c.hi {
			t.Errorf("lineSpan(%d, %d) = %d, %d; want %d, %d", c.s, c.e, lo, hi, c.lo, c.hi)
		}
	}
}

// TestMoveLines: lines go down and up past their neighbour, the last line's
// missing newline staying at the end, the selection with them; not past the
// ends.
func TestMoveLines(t *testing.T) {
	text := "one\ntwo\nthree"
	for _, c := range []struct {
		s, e, d int
		want    string
		ws      int
	}{
		{1, 1, 1, "two\none\nthree", 5},    // one down
		{5, 5, -1, "two\none\nthree", 1},   // two up
		{5, 5, 1, "one\nthree\ntwo", 11},   // two down, past the last line
		{10, 10, -1, "one\nthree\ntwo", 6}, // the last line up
		{0, 6, 1, "three\none\ntwo", 6},    // two lines down past the last
		{4, 13, -1, "two\nthree\none", 0},  // two lines up, the last among them
	} {
		m, ok := moveLines([]rune(text), c.s, c.e, c.d)
		if !ok || apply(text, m) != c.want || m.s != c.ws || m.e-m.s != c.e-c.s {
			t.Errorf("move %d..%d by %d: %q, selection %d..%d; want %q from %d", c.s, c.e, c.d, apply(text, m), m.s, m.e, c.want, c.ws)
		}
	}
	if _, ok := moveLines([]rune(text), 0, 2, -1); ok {
		t.Error("the first line moved up")
	}
	if _, ok := moveLines([]rune(text), 9, 9, 1); ok {
		t.Error("the last line moved down")
	}
}

// TestDupLines: a copy below, selected where the originals were selected.
func TestDupLines(t *testing.T) {
	for _, c := range []struct {
		text string
		s, e int
		want string
		ws   int
	}{
		{"one\ntwo\n", 1, 2, "one\none\ntwo\n", 5},
		{"one\ntwo", 5, 6, "one\ntwo\ntwo", 9}, // the last line: a newline between
		{"a\nb\nc\n", 0, 3, "a\nb\na\nb\nc\n", 4},
	} {
		d := dupLines([]rune(c.text), c.s, c.e)
		if apply(c.text, d) != c.want || d.s != c.ws || d.e != c.ws+c.e-c.s {
			t.Errorf("dup %q %d..%d: %q, selection %d..%d", c.text, c.s, c.e, apply(c.text, d), d.s, d.e)
		}
	}
}

// TestCommentLines: out at the least indent (blank lines alone), back in
// when every non-blank line is out, the lines then selected.
func TestCommentLines(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"\tif x {\n\t\ty()\n\n\t}\n", "\t// if x {\n\t// \ty()\n\n\t// }\n"},
		{"\t// if x {\n\t// \ty()\n\n\t// }\n", "\tif x {\n\t\ty()\n\n\t}\n"},
		{"a\n//b\n", "// a\n// //b\n"}, // not all out: all go out
		{"//b\n", "b\n"},               // no space after it: still back in
		{"\n\n", "\n\n"},               // blank only: nothing
	} {
		r := []rune(c.text)
		m := commentLines(r, 0, len(r), "//")
		if got := apply(c.text, m); got != c.want || m.lo != 0 || m.e != len([]rune(c.want)) || m.s != 0 {
			t.Errorf("comment %q: %q, selection %d..%d; want %q", c.text, got, m.s, m.e, c.want)
		}
	}
	if m := commentLines([]rune("a\nb\nc\n"), 2, 3, "#"); apply("a\nb\nc\n", m) != "a\n# b\nc\n" {
		t.Errorf("one line: %q", apply("a\nb\nc\n", m))
	}
}

// TestLineComment: the line comment by extension or file name, none for
// languages whose comments wrap.
func TestLineComment(t *testing.T) {
	for p, want := range map[string]string{"a.go": "//", "A.TSX": "//", "x.py": "#", "q.sql": "--", "d.tex": "%", "c.ini": ";",
		"Makefile": "#", "dir/Dockerfile": "#", "r.md": "", "p.json": "", "i.html": ""} {
		if got := lineComment(p); got != want {
			t.Errorf("%s: %q, want %q", p, got, want)
		}
	}
}

// TestWorkspaceLineKeys: the commands on their keys in the editor, each
// one undo; Ctrl+/ in a file without line comments says so.
func TestWorkspaceLineKeys(t *testing.T) {
	h, _, tb := wsFindTab(t, "one\ntwo\n")
	tb.path = "f.go"
	h.send(press(key.NameDownArrow, key.ModAlt))
	if tb.ed.Text() != "two\none\n" || !tb.dirty {
		t.Fatalf("Alt+Down: %q", tb.ed.Text())
	}
	h.send(press(key.NameUpArrow, key.ModAlt))
	if tb.ed.Text() != "one\ntwo\n" {
		t.Fatalf("Alt+Up: %q", tb.ed.Text())
	}
	h.send(press(key.NameUpArrow, key.ModAlt)) // the first line: nothing
	h.send(press("D", key.ModShortcut|key.ModShift))
	if tb.ed.Text() != "one\none\ntwo\n" {
		t.Fatalf("Ctrl+Shift+D: %q", tb.ed.Text())
	}
	h.send(press("/", key.ModShortcut))
	if tb.ed.Text() != "one\n// one\ntwo\n" {
		t.Fatalf("Ctrl+/: %q", tb.ed.Text())
	}
	h.send(press("Z", key.ModShortcut))
	if tb.ed.Text() != "one\none\ntwo\n" {
		t.Fatalf("one undo: %q", tb.ed.Text())
	}
	tb.ed.SetCaret(0, 0)
	h.send(press(key.NameDownArrow, 0)) // plain arrows still move the caret
	if l, _ := tb.ed.CaretPos(); l != 1 || tb.ed.Text() != "one\none\ntwo\n" {
		t.Fatalf("Down: line %d, %q", l, tb.ed.Text())
	}
	tb.path = "f.md"
	h.send(press("/", key.ModShortcut))
	if tb.ed.Text() != "one\none\ntwo\n" || !strings.Contains(h.u.toast, "No line comments in f.md") || h.u.toastBad {
		t.Fatalf("Ctrl+/ in Markdown: %q, toast %q", tb.ed.Text(), h.u.toast)
	}
}
