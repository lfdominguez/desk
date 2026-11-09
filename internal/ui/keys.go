package ui

import (
	"image"
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/lfdominguez/desk/internal/ui/kit"
)

// keys handles the window's keys that no focused editor took: arrows move
// the selection (with Ctrl, the card), enter opens its session, E its
// fields, T its terminal, / or Ctrl+F search, Ctrl+N a new card, esc backs
// out of whatever is open.
func (u *ui) keys(gtx layout.Context) {
	filters := kit.ZoomFilters() // the text size everywhere: no editor, terminal or pane keeps Ctrl+=, Ctrl+- or Ctrl+0
	if _, tt := u.termCard(); tt != nil && u.edit == nil && u.unlock == nil && u.cfg == nil {
		u.zoomKeys(gtx, filters) // a terminal takes every other key, esc and Ctrl+N included: Claude Code uses them
		return
	}
	if strings.HasPrefix(u.view, "srv:") && u.edit == nil && u.unlock == nil && u.cfg == nil && u.pick == "" {
		u.zoomKeys(gtx, filters) // so do the servers' panes, and the workspace has its own Ctrl+Shift keys
		return
	}
	filters = append(filters, key.Filter{Name: key.NameEscape}) // esc always: it backs out of everything
	if u.edit == nil && u.unlock == nil && u.cfg == nil && u.pick == "" && u.pal == nil {
		// the palette also over the message box: no editor uses Ctrl+K
		filters = append(filters, key.Filter{Name: "K", Required: key.ModShortcut})
	}
	// Gio hands a key to these focus-less filters even over a focused
	// editor, so the shortcuts only listen on the board itself: not while
	// typing, in a dialog or in a card's session view.
	if !u.quiet(gtx) {
		filters = append(filters, key.Filter{Name: "N", Required: key.ModShortcut}, key.Filter{Name: "F", Required: key.ModShortcut},
			key.Filter{Name: "/"}, key.Filter{Name: key.NameReturn}, key.Filter{Name: "E"}, key.Filter{Name: "T"})
		for _, n := range []key.Name{key.NameUpArrow, key.NameDownArrow, key.NameLeftArrow, key.NameRightArrow} {
			filters = append(filters, key.Filter{Name: n, Optional: key.ModShortcut})
		}
		if u.undo != nil {
			filters = append(filters, key.Filter{Name: "Z", Required: key.ModShortcut})
		}
		if u.menu != nil {
			filters = append(filters, u.menuFilters()...)
		}
		if u.view == viewArchive {
			filters = append(filters, key.Filter{Name: key.NameSpace}, key.Filter{Name: key.NameDeleteForward}, key.Filter{Name: "A", Required: key.ModShortcut})
		}
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		if e.Name == key.NameEscape {
			u.escape(gtx)
			continue
		}
		if u.zoomKey(e) {
			continue
		}
		if e.Name == "K" && e.Modifiers.Contain(key.ModShortcut) {
			u.openPalette()
			continue
		}
		if u.menu != nil && u.menuKey(e.Name) {
			continue
		}
		if u.view == viewArchive && u.archKey(e) {
			continue
		}
		move := e.Modifiers.Contain(key.ModShortcut)
		switch e.Name {
		case "Z":
			u.runUndo()
		case "N":
			u.newCard(u.selCol())
		case "F", "/":
			gtx.Execute(key.FocusCmd{Tag: &u.search})
		case key.NameReturn: // the session
			if u.sel != "" {
				u.openPanel(u.sel)
			}
		case "E": // the card's fields
			if u.sel != "" {
				u.open(u.sel)
			}
		case "T":
			if u.sel != "" {
				u.terminal(u.sel)
			}
		case key.NameUpArrow, key.NameDownArrow, key.NameLeftArrow, key.NameRightArrow:
			d := map[key.Name]image.Point{key.NameUpArrow: {0, -1}, key.NameDownArrow: {0, 1}, key.NameLeftArrow: {-1, 0}, key.NameRightArrow: {1, 0}}[e.Name]
			if move {
				u.moveSel(d.X, d.Y)
			} else {
				u.nav(d.X, d.Y)
			}
		}
	}
}

// quiet is when the shortcuts stay off: a dialog is open, an editor has
// the keyboard, or a card's session fills the window (its keys are for it).
func (u *ui) quiet(gtx layout.Context) bool {
	return u.edit != nil || u.unlock != nil || u.cfg != nil || u.pick != "" || u.pal != nil || u.typing(gtx) || strings.HasPrefix(u.view, "sess:") || strings.HasPrefix(u.view, "ws:") || strings.HasPrefix(u.view, "draw:")
}

// typing reports whether one of the window's own editors (not a dialog's:
// those turn the board keys off whole) has the keyboard. Any new editor
// outside a dialog goes here, or typing an E in it opens the card's sheet.
func (u *ui) typing(gtx layout.Context) bool {
	if gtx.Focused(&u.search) || gtx.Focused(&u.chatIn) || u.wsTyping(gtx) || u.drawTyping(gtx) {
		return true
	}
	if u.selFocus != nil && u.selFocus.Focused() { // selected chat text: Ctrl+C, not the board's keys
		return true
	}
	for _, a := range u.asks {
		if gtx.Focused(&a.cmd) {
			return true
		}
		for i := range a.other {
			if gtx.Focused(&a.other[i]) {
				return true
			}
		}
	}
	return false
}

// escape closes the innermost thing open: the sheet, the
// search, the chat panel, then the selection.
func (u *ui) escape(gtx layout.Context) {
	switch {
	case u.pal != nil:
		u.closePalette()
	case u.unlock != nil:
		u.cancelUnlock()
	case u.pick != "":
		u.pick = ""
	case u.cfg != nil:
		u.closeConfig()
	case u.edit != nil && u.edit.id != "": // the drawer saves as it closes
		u.commit()
	case u.edit != nil:
		u.closeSheet()
	case u.menu != nil:
		u.menu = nil
	case u.snoozing != "":
		u.snoozing = ""
		u.tkGen++
	case u.view == viewArchive && u.arch.anyChecked() && !gtx.Focused(&u.search): // the archive's checks, then its search
		u.arch.sel = nil
		u.arch.ver++
	case gtx.Focused(&u.search) || u.search.Len() > 0:
		u.search.SetText("")
		u.blur = true
	case u.drawOpen() != nil && (gtx.Focused(&u.drawOpen().claude.in) || u.drawOpen().rework != ""): // its chat, a suggestion's comment
		u.drawOpen().rework, u.blur = "", true
	case u.drawOpen() != nil: // the drawing backs out step by step, from its text too
		u.drawEscape(u.drawOpen())
	case strings.HasPrefix(u.view, "drc:"): // a card's drawings: back to the board, its panel open
		u.view, u.blur = "", true
	case strings.HasPrefix(u.view, "ws:"): // the workspace, from its editors too: back to the session
		u.view, u.blur = "sess:"+strings.TrimPrefix(u.view, "ws:"), true
	case u.typing(gtx) && !gtx.Focused(&u.chatIn): // an answer field: leave it, not the session
		u.blur = true
	case u.sub != "" && (u.panel != "" || strings.HasPrefix(u.view, "sess:")): // a subagent or shell: back to the conversation
		u.sub = ""
	case u.rail && strings.HasPrefix(u.view, "sess:"): // the rail of subagents and shells
		u.rail = false
	case u.panel != "":
		u.panel, u.blur = "", true
	case strings.HasPrefix(u.view, "sess:"):
		u.view, u.blur = "", true
	default:
		u.sel = ""
	}
}

// selAt is the selected card's column and place among its visible cards,
// or -1, -1.
func (u *ui) selAt() (col, idx int) {
	for c := range u.b.Columns {
		for n, j := range u.visible(c) {
			if u.b.Columns[c].Cards[j].ID == u.sel {
				return c, n
			}
		}
	}
	return -1, -1
}

// selCol is the selected card's column, else the first.
func (u *ui) selCol() int {
	c, _ := u.b.Find(u.sel)
	return max(0, c)
}

func (u *ui) visibleID(col, n int) string { return u.b.Columns[col].Cards[u.visible(col)[n]].ID }

// nav moves the selection by dx columns or dy cards; with nothing selected
// (or dx, dy 0) it picks the first card shown.
func (u *ui) nav(dx, dy int) {
	col, idx := u.selAt()
	if col < 0 {
		for c := range u.b.Columns {
			if len(u.visible(c)) > 0 {
				u.sel = u.visibleID(c, 0)
				break
			}
		}
		u.reveal()
		return
	}
	if dy != 0 {
		idx = min(max(idx+dy, 0), len(u.visible(col))-1)
	}
	for c := col + dx; dx != 0 && c >= 0 && c < len(u.b.Columns); c += dx { // skip empty columns
		if v := len(u.visible(c)); v > 0 {
			col, idx = c, min(idx, v-1)
			break
		}
	}
	u.sel = u.visibleID(col, idx)
	u.reveal()
}

// moveSel moves the selected card one place up or down, or to the column
// beside at the same height.
func (u *ui) moveSel(dx, dy int) {
	col, idx := u.selAt()
	if col < 0 {
		return
	}
	vis := u.visible(col)
	before := "-" // no move
	switch {
	case dy < 0 && idx > 0:
		before = u.visibleID(col, idx-1)
	case dy > 0 && idx < len(vis)-1:
		before = ""
		if idx+2 < len(vis) {
			before = u.visibleID(col, idx+2)
		}
	case dx != 0 && col+dx >= 0 && col+dx < len(u.b.Columns):
		col += dx
		before = ""
		if idx < len(u.visible(col)) {
			before = u.visibleID(col, idx)
		}
	}
	if before == "-" {
		return
	}
	u.b.MoveBefore(u.sel, col, before)
	u.save()
	u.reveal()
}

// reveal scrolls the selected card's column so the card shows, using the
// last frame's layout.
func (u *ui) reveal() {
	for _, h := range u.lay.cards {
		if h.id != u.sel {
			continue
		}
		body := u.lay.cols[h.col]
		if h.r.Min.Y < body.Min.Y {
			u.scroll[h.col] -= body.Min.Y - h.r.Min.Y + 8
		} else if h.r.Max.Y > body.Max.Y {
			u.scroll[h.col] += h.r.Max.Y - body.Max.Y + 8
		}
	}
	u.w.Invalidate()
}
