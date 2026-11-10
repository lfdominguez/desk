package ui

import (
	"bytes"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gioui.org/io/key"
	"gioui.org/layout"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/oligo/gvcode"
	gvcolor "github.com/oligo/gvcode/color"
	"github.com/oligo/gvcode/gutter/providers"
	"github.com/oligo/gvcode/textstyle/syntax"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// wsTab is a file open in a card's workspace: gvcode's editor over it.
type wsTab struct {
	path    string
	ed      *gvcode.Editor
	diff    *providers.VCSDiffProvider // the gutter's hunks of the change opened from the chat
	mtime   time.Time                  // the file's when read or saved: a newer one on disk was changed meanwhile
	mode    os.FileMode
	dirty   bool         // edited since read or saved
	disk    bool         // changed on disk while dirty: the bar asks Reload or Keep mine
	closing bool         // its × was pressed while dirty: a second one discards
	gen     int          // bumps on every edit
	tokGen  int          // the gen its syntax tokens are of
	lexing  bool         // a lex is running off the UI goroutine: one at a time per tab
	edit    *kanban.Edit // the change opened from the chat (Open in workspace), nil none
	note    string       // why the change isn't shown
	want    *[2]int      // a selection to make once laid out (gvcode places the caret on its shaped text)
}

// wsMaxFile is the largest file the editor opens.
const wsMaxFile = 4 << 20

// wsScheme is the editor's colours: chroma's github-dark (the chat's code
// blocks') per token type, scopes named after them.
var wsScheme = sync.OnceValue(func() syntax.ColorScheme {
	var cs syntax.ColorScheme
	for tt := range chroma.StandardTypes {
		e := chromaStyle.Get(tt)
		if !e.Colour.IsSet() {
			continue
		}
		cs.AddStyle(syntax.StyleScope(tt.String()), textStyle(e), gvcolor.MakeColor(color.NRGBA{e.Colour.Red(), e.Colour.Green(), e.Colour.Blue(), 0xff}), gvcolor.Color{})
	}
	return cs
})

// textStyle is a chroma style's bold and italic as gvcode's.
func textStyle(e chroma.StyleEntry) (st syntax.TextStyle) {
	if e.Bold == chroma.Yes {
		st |= syntax.Bold
	}
	if e.Italic == chroma.Yes {
		st |= syntax.Italic
	}
	return st
}

// chromaTokens are src's syntax tokens for the editor (rune offsets), by
// the lexer path's name picks; none when no lexer knows it.
func chromaTokens(src, path string) []syntax.Token {
	lx := lexers.Match(filepath.Base(path))
	if lx == nil {
		return nil
	}
	it, err := chroma.Coalesce(lx).Tokenise(&chroma.TokeniseOptions{State: "root"}, src) // no EnsureLF: offsets stay the editor's
	if err != nil {
		return nil
	}
	var out []syntax.Token
	at := 0
	for tok := it(); tok != chroma.EOF; tok = it() {
		n := utf8.RuneCountInString(tok.Value)
		if chromaStyle.Get(tok.Type).Colour.IsSet() && strings.TrimSpace(tok.Value) != "" {
			out = append(out, syntax.Token{Start: at, End: at + n, Scope: syntax.StyleScope(tok.Type.String())})
		}
		at += n
	}
	return out
}

// wsTokens is the lexer wsLex runs (a var tests replace, to hold one in flight).
var wsTokens = chromaTokens

// wsLex brings t's syntax tokens up to its text's gen, off the UI goroutine:
// chroma lexes the whole file (about 30 ms for 20 KB, seconds for MBs), so
// in a frame it froze typing. One lex runs at a time; edits made meanwhile
// get the next one, and until it lands the old tokens stay (gvcode looks
// them up by range, so ones past the text do nothing).
// ponytail: whole-file re-lex per edit, colors lag on multi-MB files; lex
// only from the edit on if that ever matters.
func (u *ui) wsLex(t *wsTab) {
	if t.tokGen == t.gen || t.lexing {
		return
	}
	t.lexing = true
	src, gen, path := t.ed.Text(), t.gen, t.path
	go func() {
		toks := wsTokens(src, path)
		u.later(func() {
			t.ed.SetSyntaxTokens(toks...)
			t.tokGen, t.lexing = gen, false
			u.wsRedraw()
		})
	}()
}

// wsOpen opens path in st (or shows its tab), checking it against the disk.
func (u *ui) wsOpen(st *wsState, path string) *wsTab {
	for i, t := range st.tabs {
		if t.path == path {
			st.cur = i
			u.wsCheck(t)
			u.wsRedraw()
			return t
		}
	}
	b, fi, err := readText(path)
	if err != nil {
		u.showToast(err.Error(), true)
		return nil
	}
	return u.wsAddTab(st, path, string(b), fi.ModTime(), fi.Mode().Perm())
}

// wsAddTab opens a tab over path holding text, as of the file's mtime, and shows it.
func (u *ui) wsAddTab(st *wsState, path, text string, mtime time.Time, mode os.FileMode) *wsTab {
	t := &wsTab{path: path, diff: providers.NewVCSDiffProvider(), mtime: mtime, mode: mode, tokGen: -1}
	t.ed = kit.CodeEditor(wsScheme(), t.diff)
	t.ed.SetText(text)
	t.ed.RegisterCommand(t, key.Filter{Name: "S", Required: key.ModShortcut}, func(layout.Context, key.Event) gvcode.EditorEvent {
		u.wsSave(t)
		return nil
	})
	st.tabs = append(st.tabs, t)
	st.cur = len(st.tabs) - 1
	u.wsRedraw()
	return t
}

// readText reads a file the editor can show: text, not too big.
func readText(path string) ([]byte, os.FileInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if fi.Size() > wsMaxFile {
		return nil, nil, fmt.Errorf("%s is too big to open here (%d MB)", filepath.Base(path), fi.Size()>>20)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 || !utf8.Valid(b) {
		return nil, nil, fmt.Errorf("%s is not a text file", filepath.Base(path))
	}
	return b, fi, nil
}

// wsSave writes t's text over its file (atomically, keeping its mode).
func (u *ui) wsSave(t *wsTab) {
	if err := writeAtomic(t.path, t.ed.PrepareForSave(t.ed.Text()), t.mode); err != nil {
		u.showToast("Not saved: "+err.Error(), true)
		return
	}
	if fi, err := os.Stat(t.path); err == nil {
		t.mtime = fi.ModTime()
	}
	t.dirty, t.disk, t.closing = false, false, false
	u.wsRedraw()
	u.showToast("Saved "+filepath.Base(t.path), false)
}

// writeAtomic writes text over path through a temporary file beside it
// renamed into place, so a crash leaves the old file or the new, never half.
func writeAtomic(path, text string, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = tmp.WriteString(text)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), mode)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// wsCheck looks at t's file on disk (the view shown, the main Claude's edit
// to it done): a change reloads it, or, over edits not saved, asks.
func (u *ui) wsCheck(t *wsTab) {
	fi, err := os.Stat(t.path)
	if err != nil || fi.ModTime().Equal(t.mtime) {
		return
	}
	u.wsRedraw()
	if t.dirty {
		t.disk = true
		return
	}
	u.wsReload(t)
}

// wsReload reads t's file again, the caret kept where it was.
func (u *ui) wsReload(t *wsTab) {
	b, fi, err := readText(t.path)
	if err != nil {
		u.showToast(err.Error(), true)
		return
	}
	s, e := t.ed.Selection()
	t.ed.SetText(string(b))
	t.want = &[2]int{s, e}
	t.mtime, t.dirty, t.disk, t.closing = fi.ModTime(), false, false, false
	t.diff.ClearDiff()
	t.edit, t.note = nil, ""
	t.gen++
	u.wsRedraw()
}

// wsKeepMine leaves t's edits over the file's change on disk.
func (u *ui) wsKeepMine(t *wsTab) {
	if fi, err := os.Stat(t.path); err == nil {
		t.mtime = fi.ModTime()
	}
	t.disk = false
	u.wsRedraw()
}

// wsTouched is the main session's edit to path done: its open tabs check it.
func (u *ui) wsTouched(id, path string) {
	if st := u.ws[id]; st != nil {
		for _, t := range st.tabs {
			if t.path == path {
				u.wsCheck(t)
			}
		}
	}
}

// wsOpenEdit shows an Edit from card id's chat in its workspace: the file,
// its first change found in it (by the new text: no line is kept)
// selected, with its hunk in the gutter.
func (u *ui) wsOpenEdit(id, path string, es []kanban.Edit) {
	u.openWS(id)
	t := u.wsOpen(u.wsOf(id), path)
	if t == nil || len(es) == 0 {
		return
	}
	text := t.ed.Text()
	e, i := es[0], -1
	for _, x := range es {
		if j := strings.Index(text, x.New); x.New != "" && j >= 0 {
			e, i = x, j
			break
		}
	}
	t.edit, t.note = &e, ""
	if i < 0 {
		t.note = "This change isn't in the file as it is now (changed since?): opened at the top."
		t.diff.ClearDiff()
		t.want = &[2]int{0, 0}
		return
	}
	line := strings.Count(text[:i], "\n")
	h := &providers.DiffHunk{Type: providers.DiffModified, StartLine: line, EndLine: line + len(splitLines(e.New)) - 1, OldLines: splitLines(e.Old), NewLines: splitLines(e.New)}
	if e.Old == "" {
		h.Type = providers.DiffAdded
	}
	t.diff.UpdateDiff([]*providers.DiffHunk{h})
	at := utf8.RuneCountInString(text[:i])
	t.want = &[2]int{at, at + utf8.RuneCountInString(e.New)} // the caret at its start, where the view scrolls
}

// changeText is an Edit as a diff, for the workspace Claude to read.
func changeText(path string, e kanban.Edit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "```diff\n--- %s\n+++ %s\n", path, path)
	for _, l := range lineDiff(e.Old, e.New) {
		b.WriteString(string(l.op) + l.text + "\n")
	}
	b.WriteString("```")
	return b.String()
}

// wsContext is what goes with the user's message to the workspace Claude:
// the change being viewed, else the selection with its lines, else the
// file open and the caret's line; "" no file. text is the message: a
// change it already holds isn't sent twice.
func wsContext(st *wsState, text string) string {
	t := st.tab()
	if t == nil {
		return ""
	}
	if t.edit != nil {
		if d := changeText(t.path, *t.edit); !strings.Contains(text, d) {
			return "I'm viewing this change to " + t.path + ":\n" + d
		}
		return "I'm viewing a change to " + t.path + "."
	}
	if s, e := t.ed.Selection(); s != e {
		lo, hi := min(s, e), max(s, e)
		r := []rune(t.ed.Text())
		a := strings.Count(string(r[:lo]), "\n") + 1
		b := a + strings.Count(string(r[lo:hi]), "\n")
		return fmt.Sprintf("I have lines %d-%d of %s selected:\n```\n%s\n```", a, b, t.path, string(r[lo:hi]))
	}
	line, _ := t.ed.CaretPos()
	return fmt.Sprintf("I have %s open, at line %d.", t.path, line+1)
}

// tab is the tab shown, nil none.
func (st *wsState) tab() *wsTab {
	if st.cur < 0 || st.cur >= len(st.tabs) {
		return nil
	}
	return st.tabs[st.cur]
}

// wsCloseTab closes tab i; one with edits not saved asks first (once).
func (u *ui) wsCloseTab(st *wsState, i int) {
	t := st.tabs[i]
	if t.dirty && !t.closing {
		t.closing = true
		u.showToast(filepath.Base(t.path)+" has changes not saved: "+kit.ModText("S")+" saves them, × again drops them", false)
		u.wsRedraw()
		return
	}
	st.tabs = append(st.tabs[:i], st.tabs[i+1:]...)
	if st.cur >= i && st.cur > 0 {
		st.cur--
	}
	u.wsRedraw()
}
