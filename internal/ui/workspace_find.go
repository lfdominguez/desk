package ui

import (
	"fmt"
	"image"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"github.com/oligo/gvcode"
	gvcolor "github.com/oligo/gvcode/color"
	"github.com/oligo/gvcode/textstyle/decoration"

	"github.com/lfdominguez/desk/internal/ui/kit"
)

// The find bar over a workspace's editor: Ctrl+F finds in the file shown,
// Ctrl+H replaces, Ctrl+G goes to a line. Matching is literal and smart case
// (a query without capitals ignores case), not fzf's fuzzy match: a find has
// to land on text that is there.

// The find bar's modes.
const (
	findText = iota
	findReplace
	findLine
)

// wsFindMax bounds the matches found; wsFindMarks the ones marked.
// ponytail: past wsFindMax the count reads "10000+" and Next stops there;
// search from the caret on each jump if files that full ever matter.
const (
	wsFindMax   = 10000
	wsFindMarks = 2000
)

// wsFind is a workspace's find bar (wsState.find, nil closed).
type wsFind struct {
	mode         int
	q, rep, line widget.Editor
	tab          *wsTab             // the tab its matches are of
	key          string             // the query and text gen they are of
	hits         []gvcode.TextRange // in runes, in order
	at           int                // the match selected, -1 none
}

// findAll is where q is in text, as rune ranges, at most max; a q without
// capitals matches any case.
func findAll(text, q string, max int) []gvcode.TextRange {
	if q == "" {
		return nil
	}
	if low := strings.Map(unicode.ToLower, q); low == q { // rune for rune: offsets stay the text's
		text = strings.Map(unicode.ToLower, text)
	}
	n := utf8.RuneCountInString(q)
	var out []gvcode.TextRange
	for at, runes := 0, 0; len(out) < max; {
		i := strings.Index(text[at:], q)
		if i < 0 {
			break
		}
		runes += utf8.RuneCountInString(text[at : at+i])
		out = append(out, gvcode.TextRange{Start: runes, End: runes + n})
		at, runes = at+i+len(q), runes+n
	}
	return out
}

// wsFindOpen opens st's find bar in mode over tab t, the selection (on one
// line) as the query, the field focused with its text selected.
func (u *ui) wsFindOpen(st *wsState, t *wsTab, mode int) {
	f := st.find
	if f == nil {
		f = &wsFind{at: -1}
		for _, e := range []*widget.Editor{&f.q, &f.rep, &f.line} {
			e.SingleLine, e.Submit = true, true
		}
		st.find = f
	}
	f.mode = mode
	field := &f.q
	if mode == findLine {
		field = &f.line
		f.line.SetText("")
	} else if s := t.ed.SelectedText(); s != "" && !strings.Contains(s, "\n") {
		f.q.SetText(s)
	}
	field.SetCaret(field.Len(), 0) // all selected: typing replaces it
	u.focusLater = field
	u.wsRedraw()
}

// wsFindClose closes st's find bar, its marks gone, the editor focused.
func (u *ui) wsFindClose(st *wsState) {
	if f := st.find; f != nil && f.tab != nil {
		f.tab.ed.ClearDecorations("find")
	}
	st.find = nil
	if t := st.tab(); t != nil {
		u.focusLater = t.ed
	}
	u.wsRedraw()
}

// wsFindSync brings f's matches up to t's text and the query, marked in
// the editor; a text or query unchanged costs nothing.
func (u *ui) wsFindSync(f *wsFind, t *wsTab) {
	k := fmt.Sprint(f.q.Text(), "\x00", t.gen, "\x00", f.mode == findLine)
	if f.tab == t && f.key == k {
		return
	}
	if f.tab != nil {
		f.tab.ed.ClearDecorations("find")
	}
	f.tab, f.key, f.hits, f.at = t, k, nil, -1
	if f.mode == findLine {
		return
	}
	f.hits = findAll(t.ed.Text(), f.q.Text(), wsFindMax)
	s, e := t.ed.Selection() // a match still selected (an edit elsewhere) stays the one counted
	f.at = slices.Index(f.hits, gvcode.TextRange{Start: min(s, e), End: max(s, e)})
	c := gvcolor.MakeColor(kit.COrange).MulAlpha(0x50)
	var ds []decoration.Decoration
	for _, h := range f.hits[:min(len(f.hits), wsFindMarks)] {
		ds = append(ds, decoration.Decoration{Source: "find", Start: h.Start, End: h.End, Background: &decoration.Background{Color: c}})
	}
	t.ed.AddDecorations(ds...)
}

// wsFindGo selects the match after the caret (dir 1), before it (-1), or
// at or after it (0), going round at the ends.
func wsFindGo(f *wsFind, t *wsTab, dir int) {
	if len(f.hits) == 0 {
		f.at = -1
		return
	}
	s, e := t.ed.Selection()
	lo := min(s, e)
	i := sort.Search(len(f.hits), func(i int) bool { return f.hits[i].Start > lo })
	switch dir {
	case 0:
		i = sort.Search(len(f.hits), func(i int) bool { return f.hits[i].Start >= lo })
	case -1:
		i = sort.Search(len(f.hits), func(i int) bool { return f.hits[i].Start >= lo }) - 1
	}
	f.at = (i + len(f.hits)) % len(f.hits)
	t.ed.SetCaret(f.hits[f.at].Start, f.hits[f.at].End)
}

// wsReplace replaces the match selected with the replacement and selects
// the next; with none selected, it selects the next first.
func (u *ui) wsReplace(f *wsFind, t *wsTab) {
	s, e := t.ed.Selection()
	if f.at < 0 || f.hits[f.at] != (gvcode.TextRange{Start: min(s, e), End: max(s, e)}) {
		wsFindGo(f, t, 0)
		return
	}
	h, rep := f.hits[f.at], f.rep.Text()
	t.ed.ReplaceAll([]gvcode.TextRange{h}, rep)
	end := h.Start + utf8.RuneCountInString(rep)
	t.ed.SetCaret(end, end)
	u.wsEdited(t)
	u.wsFindSync(f, t)
	wsFindGo(f, t, 0)
}

// wsReplaceAll replaces every match, as one undo.
func (u *ui) wsReplaceAll(f *wsFind, t *wsTab) {
	if n := len(f.hits); n > 0 {
		t.ed.ReplaceAll(f.hits, f.rep.Text())
		u.wsEdited(t)
		u.wsFindSync(f, t)
		u.showToast(fmt.Sprintf("Replaced %d in %s", n, filepath.Base(t.path)), false)
	}
}

// wsEdited marks t changed by desk itself (a replace): gvcode tells only
// the user's typing.
func (u *ui) wsEdited(t *wsTab) {
	t.dirty, t.closing = true, false
	t.gen++
	u.wsRedraw()
}

// wsGoLine puts the caret at the start of the line asked for (clamped to
// the file's), and closes the bar.
func (u *ui) wsGoLine(st *wsState, t *wsTab) {
	n, err := strconv.Atoi(strings.TrimSpace(st.find.line.Text()))
	if err != nil {
		return
	}
	off, _ := t.ed.ConvertPos(min(max(n, 1), t.ed.Lines())-1, 0)
	t.ed.SetCaret(off, off)
	u.wsFindClose(st)
}

// count is what the bar says of the matches.
func (f *wsFind) count() string {
	switch {
	case f.q.Len() == 0:
		return ""
	case len(f.hits) == wsFindMax:
		return fmt.Sprintf("%d+", wsFindMax)
	case f.at >= 0:
		return fmt.Sprintf("%d of %d", f.at+1, len(f.hits))
	case len(f.hits) > 0:
		return fmt.Sprintf("%d found", len(f.hits))
	}
	return "No results"
}

// wsFindKeys are the editor's commands that open the bar.
func (u *ui) wsFindKeys(st *wsState, t *wsTab) {
	for name, mode := range map[key.Name]int{"F": findText, "H": findReplace, "G": findLine} {
		t.ed.RegisterCommand(t, key.Filter{Name: name, Required: key.ModShortcut}, func(layout.Context, key.Event) gvcode.EditorEvent {
			u.wsFindOpen(st, t, mode)
			return nil
		})
	}
}

// wsFindBar is the bar over t's editor: its fields' keys, then its rows.
func (u *ui) wsFindBar(gtx layout.Context, st *wsState, t *wsTab, act *func()) layout.Dimensions {
	f, k := st.find, u.k
	for {
		ev, ok := f.q.Update(gtx)
		if !ok {
			break
		}
		switch ev.(type) {
		case widget.ChangeEvent: // as typed: the first match from the caret on
			u.wsFindSync(f, t)
			wsFindGo(f, t, 0)
		case widget.SubmitEvent:
			u.wsFindSync(f, t)
			wsFindGo(f, t, 1)
		}
		u.wsRedraw()
	}
	for {
		ev, ok := f.rep.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			u.wsReplace(f, t)
		}
	}
	for {
		ev, ok := f.line.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			*act = func() { u.wsGoLine(st, t) }
		}
	}
	u.wsFindSync(f, t) // the text changed under it, or another tab: the matches follow
	for _, e := range []*widget.Editor{&f.q, &f.rep, &f.line} {
		if u.focusLater == e {
			gtx.Execute(key.FocusCmd{Tag: e})
			u.focusLater = nil
		}
	}
	field := func(e *widget.Editor, hint string) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			rim := kit.CLine
			if gtx.Focused(e) {
				rim = kit.CAcc
			}
			return kit.Boxed(kit.CPane, rim, kit.RCtl, layout.Inset{Top: 5, Bottom: 5, Left: 8, Right: 8}, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Dp(240)
				gtx.Constraints.Max.X = gtx.Dp(240)
				return k.Editor(gtx, e, hint, 12, kit.Mono, kit.CText)
			})(gtx)
		})
	}
	btn := func(id, label, tip string, f func()) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			d, ok := k.Button(gtx, id, kit.CPanelCard, kit.CHover, kit.RCtl, layout.Inset{Top: 4, Bottom: 4, Left: 9, Right: 9}, k.Lbl(label, 12, kit.CText, kit.Semi(kit.Sans)))
			k.Tip(gtx, id, d, tip, true)
			if ok {
				*act = f
			}
			return layout.Inset{Left: 6}.Layout(gtx, func(layout.Context) layout.Dimensions { return d })
		})
	}
	closeBtn := layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		d, ok := k.Button(gtx, "wsFindX", kit.CPanel, kit.CHover, kit.RCtl, layout.UniformInset(4), kit.Icon(12, kit.CText3, kit.IcoX))
		k.Tip(gtx, "wsFindX", d, "Close (Esc)", true)
		if ok {
			*act = func() { u.wsFindClose(st) }
		}
		return layout.Inset{Left: 6}.Layout(gtx, func(layout.Context) layout.Dimensions { return d })
	})
	row := func(kids ...layout.FlexChild) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 3, Bottom: 3}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
			})
		})
	}
	var rows []layout.FlexChild
	if f.mode == findLine {
		rows = append(rows, row(field(&f.line, fmt.Sprintf("Go to line, 1 to %d", t.ed.Lines())),
			layout.Rigid(kit.HGap(8)), layout.Rigid(k.Lbl("↵ goes", 11, kit.CText3, kit.Sans)), closeBtn))
	} else {
		count := f.count()
		rows = append(rows, row(field(&f.q, "Find (no capitals: any case)"),
			layout.Rigid(kit.HGap(8)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Dp(unit.Dp(70))
				return k.Lbl(count, 11, kit.CText3, kit.Sans)(gtx)
			}),
			btn("wsFindPrev", "↑", "Previous", func() { wsFindGo(f, t, -1); u.wsRedraw() }),
			btn("wsFindNext", "↓", "Next (↵)", func() { wsFindGo(f, t, 1); u.wsRedraw() }),
			closeBtn))
		if f.mode == findReplace {
			rows = append(rows, row(field(&f.rep, "Replace with"),
				btn("wsReplace", "Replace", "Replace this one and go to the next (↵)", func() { u.wsReplace(f, t) }),
				btn("wsReplaceAll", "All", "Replace every match", func() { u.wsReplaceAll(f, t) })))
		}
	}
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	kit.Fill(gtx, kit.CPanel)
	return layout.Inset{Top: 3, Bottom: 3, Left: 12, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Point{}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
	})
}
