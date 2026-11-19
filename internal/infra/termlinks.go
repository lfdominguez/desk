package infra

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// A pane's URLs open with Ctrl+click, as in Konsole and Kitty: Ctrl over one
// underlines it and shows the hand.

// urlRE is a web link as terminals find them: up to a space, a quote or an
// angle bracket.
var urlRE = regexp.MustCompile("(?:https?|ftp)://[^\\s<>\"'`]+")

// linkSpan is a URL in a pane: the row its line starts on and its cells
// [lo, hi) counted along that line, over the rows the terminal wrapped it
// onto (cols each). Zero: none.
type linkSpan struct {
	row, lo, hi int
	url         string
}

// linkAt is the URL covering cell (x, y) of the view, followed over the rows
// the terminal wrapped its line onto. ponytail: only the rows on screen; a
// URL running past the view's top or bottom opens cut there (the scrollback's
// lines would complete it).
func (v *termView) linkAt(x, y int) linkSpan {
	if y < 0 || y >= len(v.runs) {
		return linkSpan{}
	}
	wrapped := func(r int) bool { return r >= 0 && r < len(v.meta) && v.meta[r].Wrap }
	top, bot := y, y
	for wrapped(top - 1) {
		top--
	}
	for wrapped(bot) && bot+1 < len(v.runs) {
		bot++
	}
	var line []rune
	for r := top; r <= bot; r++ {
		line = append(line, v.cells(r)...)
		for r < bot && len(line) < (r-top+1)*v.cols { // a wrapped row is full: its trailing blanks count
			line = append(line, ' ')
		}
	}
	lo, hi, url := urlAt(line, (y-top)*v.cols+x)
	if url == "" {
		return linkSpan{}
	}
	return linkSpan{top, lo, hi, strings.ReplaceAll(url, string(halfCell), "")}
}

// halfCell stands for a wide character's second cell in cells: no space, so
// a URL runs on over it.
const halfCell = '​'

// cells is row y as one rune per cell (a wide character then halfCell), so
// positions in it are columns. ponytail: a combining mark (no cell of its
// own) still counts one; graphemes would place it.
func (v *termView) cells(y int) []rune {
	var line []rune
	for _, r := range v.runs[y] {
		for len(line) < r.X {
			line = append(line, ' ')
		}
		for _, c := range r.Text {
			line = append(line, c)
			if ansi.StringWidth(string(c)) == 2 {
				line = append(line, halfCell)
			}
		}
	}
	return []rune(strings.TrimRight(string(line), " "))
}

// rows is the span cut at each row: a row, and its cells [lo, hi) on it.
func (l linkSpan) rows(cols int) [][3]int {
	var out [][3]int
	for at := l.lo; cols > 0 && at < l.hi; {
		r, x := at/cols, at%cols
		end := min(l.hi-r*cols, cols)
		out = append(out, [3]int{l.row + r, x, end})
		at = (r + 1) * cols
	}
	return out
}

// urlAt is the URL in line covering cell x, its trailing punctuation left
// out (a sentence's full stop, a closing bracket it didn't open).
func urlAt(line []rune, x int) (lo, hi int, url string) {
	s := string(line)
	for _, m := range urlRE.FindAllStringIndex(s, -1) {
		u := trimURL(s[m[0]:m[1]])
		a := utf8.RuneCountInString(s[:m[0]])
		b := a + utf8.RuneCountInString(u)
		if x >= a && x < b {
			return a, b, u
		}
	}
	return 0, 0, ""
}

// trimURL drops what ends a sentence after a URL, and a closing bracket
// with no opening one inside it.
func trimURL(u string) string {
	for u != "" {
		c := u[len(u)-1]
		switch {
		case strings.IndexByte(".,;:!?", c) >= 0:
		case c == ')' && strings.Count(u, "(") < strings.Count(u, ")"):
		case c == ']' && strings.Count(u, "[") < strings.Count(u, "]"):
		default:
			return u
		}
		u = u[:len(u)-1]
	}
	return u
}
