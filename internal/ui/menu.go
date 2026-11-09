package ui

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"strings"
	"time"

	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"

	"github.com/lfdominguez/desk/internal/ui/kit"
)

// cardMenu is a card's right-click menu: where it opened, in board
// coordinates, and where it drew (the last frame), so a press outside it
// closes it and one inside is left to its items. Not x/component's
// ContextArea: that is a handler per card, and the board has one pointer
// handler hit-testing u.lay.
type cardMenu struct {
	id    string
	at    image.Point
	r     image.Rectangle // the menu and its submenu, last frame
	sub   bool            // Move to's columns are open
	items []menuItem      // last frame's, for the keys
}

// menuItem is a row of the menu: what it says and does, and the key that
// does it while the menu is open.
type menuItem struct {
	id, label string
	key       key.Name
	cap       string // the key as shown
	ico       kit.IconPath
	dot       color.NRGBA // a column's color, for Move to's
	fg        color.NRGBA
	f         func()
}

// archiveCard ends card id's session and archives it; the toast undoes it.
func (u *ui) archiveCard(id string) {
	c := u.b.Card(id)
	if c == nil {
		return
	}
	key, stopped := c.Key(), u.endSession(id)
	u.b.ArchiveCard(id)
	u.save()
	msg := "Archived " + key
	if stopped {
		msg += "; its session stopped"
	}
	u.showUndo(msg, func() { u.b.Restore(id); u.save(); u.showToast("Restored "+key, false) })
}

// endSession stops card id's Claudes and leaves its views, before it goes
// off the board; it says whether its session was running.
func (u *ui) endSession(id string) bool {
	p := u.procs[id]
	if p != nil {
		p.Close()
		delete(u.procs, id)
	}
	delete(u.asks, id)
	if u.panel == id {
		u.panel = ""
	}
	if u.view == "sess:"+id || u.view == "ws:"+id {
		u.view = ""
	}
	if st := u.ws[id]; st != nil && st.proc != nil { // its workspace Claude too
		st.proc.Close()
		st.proc, st.from = nil, nil
	}
	return p != nil
}

// menuFilters are the keys of the open menu's items.
func (u *ui) menuFilters() []event.Filter {
	var fs []event.Filter
	for _, it := range u.menu.items {
		if it.key == "C" {
			fs = append(fs, key.Filter{Name: it.key, Required: key.ModShortcut})
		} else if it.key != "" {
			fs = append(fs, key.Filter{Name: it.key})
		}
	}
	return fs
}

// menuKey runs the open menu's item for key name; it reports whether one ran.
func (u *ui) menuKey(name key.Name) bool {
	for _, it := range u.menu.items {
		if it.key == name {
			u.menu = nil
			it.f()
			return true
		}
	}
	return false
}

// drawCardMenu draws the open card menu over the board; an item runs once
// the menu is drawn, and closes it.
func (u *ui) drawCardMenu(gtx layout.Context) {
	k := u.k
	m := u.menu
	c := u.b.Card(m.id)
	if c == nil {
		u.menu = nil
		return
	}
	var act func()
	id := m.id
	archive := "Archive"
	if u.procs[id] != nil {
		archive = "Archive and stop the session"
	}
	items := []menuItem{
		{id: "Open session", label: "Open session", key: key.NameReturn, cap: "↵", ico: kit.IcoChat, f: func() { u.openPanel(id) }},
		{id: "Open full window", label: "Open full window", ico: kit.IcoSide, f: func() { u.openSession(id) }},
		{id: "Claude Code terminal", label: "Claude Code terminal", key: "T", cap: "T", ico: kit.IcoTerminal, f: func() { u.terminal(id) }},
		{id: "Workspace", label: "Workspace", key: "W", cap: "W", ico: kit.IcoCode, f: func() { u.openWS(id) }},
		{id: "Edit card", label: "Edit card", key: "E", cap: "E", ico: kit.IcoEdit, f: func() { u.open(id) }},
		{id: "Copy key " + c.Key(), label: "Copy key " + c.Key(), key: "C", cap: kit.ModText("C"), ico: kit.IcoLink, f: func() { u.copyKey = c.Key() }},
	}
	var moves []menuItem
	col, _ := u.b.Find(id)
	for i, cl := range u.b.Columns {
		title := cl.Title
		it := menuItem{id: "Move to " + title, label: title, dot: u.colAccent(i), f: func() { u.moveTo(id, title); u.save() }}
		if i < 9 {
			it.key, it.cap = key.Name(fmt.Sprint(i+1)), fmt.Sprint(i+1)
		}
		if i == col {
			it.label, it.fg, it.f = title+" · here", kit.CText3, func() {}
		}
		moves = append(moves, it)
	}
	tail := menuItem{id: "Archive", label: archive, key: key.NameDeleteBackward, cap: "Backspace", ico: kit.IcoArchive, fg: kit.CRedText, f: func() { u.archiveCard(id) }}
	m.items = append(append(append([]menuItem{}, items...), moves...), tail)

	row := func(it menuItem) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			fill := kit.NRGBA0
			if it.id == "Move to" && m.sub {
				fill = kit.CHover
			}
			bid := "cardMenu" + it.id
			d, ok := k.Button(gtx, bid, fill, kit.CHover, kit.RCtl, layout.Inset{Top: 7, Bottom: 7, Left: 8, Right: 8}, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				fg := kit.CText
				if it.fg != (color.NRGBA{}) {
					fg = it.fg
				}
				var lead layout.Widget
				switch {
				case it.ico != nil:
					ic := kit.CText2
					if it.fg == kit.CRedText {
						ic = it.fg
					}
					lead = kit.Icon(14, ic, it.ico)
				case it.dot != (color.NRGBA{}):
					lead = func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Left: 3, Right: 4}.Layout(gtx, kit.Dot(it.dot))
					}
				}
				ch := []layout.FlexChild{}
				if lead != nil {
					ch = append(ch, layout.Rigid(lead), layout.Rigid(kit.HGap(10)))
				}
				ch = append(ch, layout.Flexed(1, k.LblFill(it.label, 13, fg, kit.Sans)))
				switch {
				case it.id == "Move to":
					ch = append(ch, layout.Rigid(k.Lbl("›", 14, kit.CText3, kit.Sans)))
				case it.cap != "":
					ch = append(ch, layout.Rigid(kit.HGap(8)), layout.Rigid(k.KeyCap(it.cap, false)))
				}
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx, ch...)
			})
			if it.id == "Move to" {
				if k.Click(bid).Hovered() { // read after its events: hovering opens the columns, so a click has nothing to add
					m.sub = true
				}
				return d
			}
			if k.Click(bid).Hovered() && it.dot == (color.NRGBA{}) {
				m.sub = false
			}
			if ok {
				act = it.f
			}
			return d
		})
	}
	sep := layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		h := gtx.Dp(9)
		kit.FillRRect(gtx.Ops, image.Rect(gtx.Dp(6), h/2, gtx.Constraints.Min.X-gtx.Dp(6), h/2+gtx.Dp(1)), 0, kit.CKeyRim)
		return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, h)}
	})
	box := func(width int, rows []layout.FlexChild) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Exact(image.Pt(gtx.Dp(unit.Dp(width)), gtx.Constraints.Max.Y))
			gtx.Constraints.Min.Y = 0
			return kit.Boxed(kit.CPanelCard, kit.CKeyRim, kit.RCard, layout.UniformInset(4), func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
			})(gtx)
		}
	}
	var rows []layout.FlexChild
	for _, it := range items {
		rows = append(rows, row(it))
	}
	rows = append(rows, sep, row(menuItem{id: "Move to", label: "Move to", ico: kit.IcoSend}), sep, row(tail))
	d, call := measure(gtx, box(260, rows))
	room := gtx.Constraints.Max
	at := image.Pt(max(min(m.at.X, room.X-d.Size.X-gtx.Dp(4)), 0), max(min(m.at.Y, room.Y-d.Size.Y-gtx.Dp(4)), 0))
	r := image.Rectangle{Min: at, Max: at.Add(d.Size)}
	block := func(r image.Rectangle, call op.CallOp) {
		off := op.Offset(r.Min).Push(gtx.Ops)
		cl := clip.Rect{Max: r.Size()}.Push(gtx.Ops) // the whole box, gaps and separators too: hover and clicks stop here, not at the board below
		event.Op(gtx.Ops, m)
		pointer.CursorDefault.Add(gtx.Ops)
		cl.Pop()
		call.Add(gtx.Ops)
		off.Pop()
	}
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: m, Kinds: pointer.Press | pointer.Move | pointer.Enter | pointer.Leave}); !ok {
			break
		}
	}
	block(r, call)
	m.r = r
	if m.sub {
		var subRows []layout.FlexChild
		for _, it := range moves {
			subRows = append(subRows, row(it))
		}
		sd, scall := measure(gtx, box(200, subRows))
		sx := r.Max.X + gtx.Dp(4)
		if sx+sd.Size.X > room.X {
			sx = r.Min.X - gtx.Dp(4) - sd.Size.X
		}
		sy := max(min(r.Max.Y-gtx.Dp(110), room.Y-sd.Size.Y-gtx.Dp(4)), 0)
		sr := image.Rectangle{Min: image.Pt(sx, sy), Max: image.Pt(sx, sy).Add(sd.Size)}
		block(sr, scall)
		m.r = m.r.Union(sr)
	}
	if act != nil {
		u.menu = nil
		act()
	}
	u.copyPending(gtx)
}

// copyPending writes the key Copy key asked for to the clipboard: the
// write needs a frame's gtx, which the menu's keys don't have.
func (u *ui) copyPending(gtx layout.Context) {
	if u.copyKey != "" {
		gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(u.copyKey))})
		u.showToast("Copied "+u.copyKey, false)
		u.copyKey = ""
	}
}

// showUndo shows s with an Undo button (and Ctrl+Z) for a while.
func (u *ui) showUndo(s string, undo func()) {
	u.showToast(s, false)
	u.toastFrom, u.toastUntil = time.Now(), time.Now().Add(undoFor)
	u.undo = undo
}

const undoFor = 6 * time.Second

// runUndo undoes what the toast offers to, while it shows.
func (u *ui) runUndo() {
	if f := u.undo; f != nil && time.Until(u.toastUntil) > 0 {
		u.undo = nil
		f()
	}
}
