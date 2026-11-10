package ui

import (
	"path/filepath"
	"strings"
	"unicode/utf8"

	"gioui.org/io/key"
	"gioui.org/layout"
	"github.com/oligo/gvcode"
)

// The editor's line commands, on the lines the selection touches: Alt+↑/↓
// move them, Ctrl+Shift+D duplicates them below, Ctrl+/ comments them out
// or back in. Each is one edit through gvcode (ReplaceAll), so one undo.
// The work is pure functions over the text's runes; wsLineCmd applies them.

// lineSpan is the whole lines selection s..e touches in r: from the first
// one's start to past the last one's newline (or the text's end). A
// selection ending at a line's very start leaves that line out.
func lineSpan(r []rune, s, e int) (lo, hi int) {
	lo, hi = min(s, e), max(s, e)
	if hi > lo && r[hi-1] == '\n' {
		hi--
	}
	for lo > 0 && r[lo-1] != '\n' {
		lo--
	}
	for hi < len(r) && r[hi] != '\n' {
		hi++
	}
	if hi < len(r) {
		hi++
	}
	return lo, hi
}

// lineEdit is a line command's change: the runes [lo,hi) become text, and
// the selection goes to s..e after it.
type lineEdit struct {
	lo, hi int
	text   string
	s, e   int
}

// moveLines moves the lines s..e touches down (d 1) or up (-1) past their
// neighbour; false at the text's end that way.
func moveLines(r []rune, s, e, d int) (lineEdit, bool) {
	lo, hi := lineSpan(r, s, e)
	block := string(r[lo:hi])
	if d > 0 {
		if hi == len(r) {
			return lineEdit{}, false
		}
		_, nhi := lineSpan(r, hi, hi)
		next := string(r[hi:nhi])
		if !strings.HasSuffix(next, "\n") { // the last line: the newline moves to it
			next, block = next+"\n", strings.TrimSuffix(block, "\n")
		}
		n := utf8.RuneCountInString(next)
		return lineEdit{lo, nhi, next + block, s + n, e + n}, true
	}
	if lo == 0 {
		return lineEdit{}, false
	}
	plo, _ := lineSpan(r, lo-1, lo-1)
	prev := string(r[plo:lo])
	if !strings.HasSuffix(block, "\n") { // the block is the last line: it takes the newline
		block, prev = block+"\n", strings.TrimSuffix(prev, "\n")
	}
	n := lo - plo
	return lineEdit{plo, hi, block + prev, s - n, e - n}, true
}

// dupLines puts a copy of the lines s..e touches below them, selected as
// the originals were.
func dupLines(r []rune, s, e int) lineEdit {
	lo, hi := lineSpan(r, s, e)
	ins := string(r[lo:hi])
	if !strings.HasSuffix(ins, "\n") { // the last line, no newline: one between them
		ins = "\n" + ins
	}
	n := utf8.RuneCountInString(ins)
	return lineEdit{hi, hi, ins, s + n, e + n}
}

// commentLines comments out the lines s..e touches with prefix (at their
// least indent, blank lines left alone), or, when every non-blank one is
// already, takes it (and a space after it) away. The lines end selected.
func commentLines(r []rune, s, e int, prefix string) lineEdit {
	lo, hi := lineSpan(r, s, e)
	lines := strings.SplitAfter(string(r[lo:hi]), "\n")
	indent, all := -1, true
	for _, l := range lines {
		t := strings.TrimLeft(l, " \t")
		if strings.TrimSpace(t) == "" {
			continue
		}
		if i := len(l) - len(t); indent < 0 || i < indent {
			indent = i
		}
		all = all && strings.HasPrefix(t, prefix)
	}
	if indent < 0 { // only blank lines
		return lineEdit{lo, hi, string(r[lo:hi]), lo, hi}
	}
	var b strings.Builder
	for _, l := range lines {
		t := strings.TrimLeft(l, " \t")
		switch {
		case strings.TrimSpace(t) == "":
			b.WriteString(l)
		case all:
			b.WriteString(l[:len(l)-len(t)] + strings.TrimPrefix(strings.TrimPrefix(t, prefix), " "))
		default:
			b.WriteString(l[:indent] + prefix + " " + l[indent:])
		}
	}
	out := b.String()
	return lineEdit{lo, hi, out, lo, lo + utf8.RuneCountInString(out)}
}

// lineComment is the line comment of path's language, "" for one without
// (Markdown, HTML, JSON: their comments wrap text).
func lineComment(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".java", ".kt", ".kts", ".scala", ".c", ".h", ".cc", ".cpp", ".hpp", ".cs", ".rs", ".swift", ".php", ".dart", ".proto", ".zig":
		return "//"
	case ".py", ".sh", ".bash", ".zsh", ".fish", ".rb", ".pl", ".r", ".yml", ".yaml", ".toml", ".conf", ".cfg", ".nix", ".tf", ".ps1", ".mk", ".cmake", ".dockerfile", ".gitignore", ".env":
		return "#"
	case ".sql", ".lua", ".hs", ".elm":
		return "--"
	case ".tex", ".erl", ".m":
		return "%"
	case ".el", ".lisp", ".clj", ".scm", ".ini":
		return ";"
	}
	switch strings.ToLower(filepath.Base(path)) {
	case "makefile", "dockerfile", "containerfile", "justfile", ".bashrc", ".zshrc", ".profile":
		return "#"
	}
	return ""
}

// wsLineCmd applies a line command's change to t: one undo, the selection
// placed, the tab marked changed.
func (u *ui) wsLineCmd(t *wsTab, c lineEdit) {
	t.ed.ReplaceAll([]gvcode.TextRange{{Start: c.lo, End: c.hi}}, c.text)
	t.ed.SetCaret(c.s, c.e)
	u.wsEdited(t)
}

// wsLineKeys are t's editor commands for the line commands.
func (u *ui) wsLineKeys(t *wsTab) {
	cmd := func(name key.Name, mods key.Modifiers, f func(r []rune, s, e int)) {
		t.ed.RegisterCommand(t, key.Filter{Name: name, Required: mods}, func(layout.Context, key.Event) gvcode.EditorEvent {
			s, e := t.ed.Selection()
			f([]rune(t.ed.Text()), s, e)
			return nil
		})
	}
	for name, d := range map[key.Name]int{key.NameUpArrow: -1, key.NameDownArrow: 1} {
		cmd(name, key.ModAlt, func(r []rune, s, e int) {
			if c, ok := moveLines(r, s, e, d); ok {
				u.wsLineCmd(t, c)
			}
		})
	}
	cmd("D", key.ModShortcut|key.ModShift, func(r []rune, s, e int) { u.wsLineCmd(t, dupLines(r, s, e)) })
	cmd("/", key.ModShortcut, func(r []rune, s, e int) {
		if p := lineComment(t.path); p != "" {
			u.wsLineCmd(t, commentLines(r, s, e, p))
		} else {
			u.showToast("No line comments in "+filepath.Base(t.path)+"'s language", false)
		}
	})
}
