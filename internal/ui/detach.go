package ui

import (
	"cmp"
	"strings"
	"sync/atomic"

	"gioui.org/app"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/keys"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// pane is what one window keeps for itself while it shows a card's
// session: its message box, scrolls, the subagent or history it shows, its
// keyboard. The main window has u.mainPane; a detached session's window its
// own. u.pane is the one being laid out, so the session code reads u.chatIn
// and friends whichever window it draws in.
type pane struct {
	view      string // what the window shows: the main one's sidebar pick (views.go), "" all cards; a detached one's "" its session
	chatIn    widget.Editor
	chatList  layout.List
	chatFocus bool   // focus chatIn on the next frame
	chatPaste bool   // Ctrl+V in chatIn found text: the editor pastes it
	chatFor   string // the card chatIn's text is for (drafts.go)
	chatTap   int    // the session pane's press area: its tag
	selBack   *widget.Editor
	selFocus  *widget.Selectable // the selectable text that last had the keyboard
	selLast   *widget.Selectable // the selectable text last pressed: the one with a selection
	teamList  layout.List        // the canvas's handoffs
	sub       string             // the task the session's pane shows instead of the conversation
	subList   layout.List
	rail      bool // the session view shows its rail of subagents and shells
	railList  layout.List
	histList  layout.List
	focused   bool      // the window has the keyboard: notifications skip the card in view
	blur      bool      // drop the key focus on the next frame
	win       *detached // the detached window this is, nil for the main one
}

func (p *pane) init() {
	p.chatIn.Submit = true
	p.chatList.Axis, p.chatList.ScrollToEnd = layout.Vertical, true
	p.teamList.Axis, p.teamList.ScrollToEnd = layout.Vertical, true
	p.subList.Axis, p.subList.ScrollToEnd, p.railList.Axis, p.histList.Axis = layout.Vertical, true, layout.Vertical, layout.Vertical
}

// detached is a card's session in a window of its own.
type detached struct {
	id        string
	w         *app.Window // set once, before the window is listed in u.wins: read from other goroutines
	title     string      // what its window is called, to find it (raise)
	p         pane
	ops       op.Ops
	shownTerm atomic.Value // the card whose terminal it shows (string), for the terminal's reader
	drewInfra bool         // this frame drew the infra session beside the chat
	gone      bool         // its window closed itself: nothing to close on reattach
}

// detach moves card id's session into its own window (or brings that window
// up): what was written for it in the main window goes along, and the card
// remembers it, so the window comes back when desk starts again.
func (u *ui) detach(id string) {
	c := u.b.Card(id)
	if c == nil {
		return
	}
	if d := u.detached[id]; d != nil {
		raise(d)
		return
	}
	u.inPane(&u.mainPane, func() {
		if u.chatFor == id {
			u.keepDraft()
			u.chatFor = ""
			u.chatIn.SetText("")
		}
	})
	c.Detached = true
	u.save()
	u.openDetached(id)
}

// reopenDetached opens the windows of the cards detached when desk last
// closed (an archived card's stays shut: the archive isn't in the columns).
func (u *ui) reopenDetached() {
	for _, col := range u.b.Columns {
		for _, c := range col.Cards {
			if c.Detached {
				u.openDetached(c.ID)
			}
		}
	}
}

// sessionCard is the card whose session the main window shows (its view,
// else the side panel), else the one selected on the board; "" none.
func (u *ui) sessionCard() string {
	if id, ok := strings.CutPrefix(u.view, "sess:"); ok {
		return id
	}
	return cmp.Or(u.panel, u.sel)
}

// openDetached opens card id's window.
func (u *ui) openDetached(id string) {
	d := &detached{id: id}
	d.p.init()
	d.p.win, d.p.chatFocus = d, true
	u.inPane(&d.p, func() { u.chatSwitch(id) }) // the card's draft, now: a quit before its first frame keeps it
	u.detached[id] = d
	d.w = new(app.Window)
	u.detachedOpts(d)
	u.listWins()
	startDetached(u, d)
}

// listWins publishes the detached windows for goroutines that may not read
// u.detached (a terminal's reader): a fresh slice each change.
func (u *ui) listWins() {
	ws := make([]*detached, 0, len(u.detached))
	for _, d := range u.detached {
		ws = append(ws, d)
	}
	u.wins.Store(&ws)
}

// detachedWins is the last list listWins published.
func (u *ui) detachedWins() []*detached {
	if ws := u.wins.Load(); ws != nil {
		return *ws
	}
	return nil
}

// detachedOpts are the window's options, again after a lost display: Gio
// drops them once used.
func (u *ui) detachedOpts(d *detached) {
	d.title = "desk"
	if c := u.b.Card(d.id); c != nil {
		d.title = c.Key() + " · " + c.Title
	}
	d.w.Option(app.Title(d.title), app.Size(unit.Dp(1000), unit.Dp(900)), app.MinSize(unit.Dp(520), unit.Dp(400)))
}

// startDetached runs a detached window's events; tests open none. Set in
// init: it reaches back to detach through the session it draws.
var startDetached func(u *ui, d *detached)

func init() { startDetached = func(u *ui, d *detached) { go u.runDetached(d, d.w.Event) } }

// raise brings d's window to the front, off the UI goroutine. Gio's
// Wayland windows can't raise themselves (that takes an activation token),
// so on Hyprland through its socket, on KDE through a KWin script, else
// Gio's raise (X11, macOS, Windows). Tests have no window.
// ponytail: sway and GNOME need xdg-activation, a token asked by the
// focused window and spent by this one, which Gio has no API for.
var raise = func(d *detached) { go raiseNow(d) }

func raiseNow(d *detached) {
	if !hyprFocus(d.title) && !kwinFocus(d.title) {
		d.w.Perform(system.ActionRaise)
	}
}

// reattach puts card id's session back in the main window: its window's
// text becomes the card's draft, and the window closes when it is still
// open (Reattach, not the window's own close).
func (u *ui) reattach(id string) {
	d := u.detached[id]
	if d == nil {
		return
	}
	u.inPane(&d.p, u.keepDraft)
	delete(u.detached, id)
	u.listWins()
	if u.infraBy == id { // the servers go back to the main window
		u.infraBy = ""
		u.infraWin.Store(nil)
	}
	if u.edit != nil && u.edit.in == &d.p { // its card editor closes with it, saving
		u.inPane(&d.p, func() {
			if u.edit.id != "" {
				u.commit()
			} else {
				u.closeSheet()
			}
		})
	}
	if c := u.b.Card(id); c != nil {
		c.Detached = false
		u.save()
	}
	if !d.gone {
		closeWindow(d.w)
	}
	u.w.Invalidate()
}

// closeWindow closes w from outside its own events; tests have no window.
var closeWindow = func(w *app.Window) { go w.Perform(system.ActionClose) }

// inPane runs f with p as the pane being laid out.
func (u *ui) inPane(p *pane, f func()) {
	prev := u.pane
	u.pane = p
	defer func() { u.pane = prev }()
	f()
}

// runDetached handles d's window's events, next's one after another, each
// under u.mu: both windows' frames share u. A display that dropped the
// window opens it again a moment later.
func (u *ui) runDetached(d *detached, next func() event.Event) {
	for {
		ev := next()
		u.mu.Lock()
		state := u.detachedEvent(d, ev)
		u.mu.Unlock()
		switch state {
		case winDone:
			return
		case winLost:
			<-reopenWait()
			u.mu.Lock()
			if u.detached[d.id] == d {
				u.detachedOpts(d)
			} else {
				state = winDone // reattached meanwhile
			}
			u.mu.Unlock()
			if state == winDone {
				return
			}
		}
	}
}

const (
	winGoing = iota
	winDone
	winLost
)

// detachedEvent handles one of d's window's events.
func (u *ui) detachedEvent(d *detached, ev event.Event) int {
	switch e := ev.(type) {
	case app.DestroyEvent:
		if u.detached[d.id] != d { // reattached: the close it asked for
			return winDone
		}
		if e.Err != nil && !u.closing {
			u.crashNote("window of %s lost, reopening: %v", d.id, e.Err)
			return winLost
		}
		if !u.closing { // closed by the user: back into the main window
			d.gone = true
			u.reattach(d.id)
		}
		return winDone
	case app.ConfigEvent:
		d.p.focused = e.Config.Focused
	case app.FrameEvent:
		gtx := app.NewContext(&d.ops, e)
		u.detachedFrame(gtx, d)
		e.Frame(gtx.Ops)
	default: // a wakeup
		u.drain()
	}
	return winGoing
}

// detachedFrame draws d's window: the card's session over all of it, its
// rail beside it when open, the toasts on top.
func (u *ui) detachedFrame(gtx layout.Context, d *detached) {
	u.drain() // with the main pane: the queued work belongs to no window
	u.infraRead()
	prevScope := u.k.Scope
	u.k.Scope = "w" + d.id + ":"
	defer func() { u.k.Scope = prevScope }()
	u.inPane(&d.p, func() {
		before, tg, ig := u.sig(), u.termGen.Load(), u.infraGen()
		defer func() {
			if u.sig() != before || len(u.events) > 0 || u.termGen.Load() != tg || u.infraGen() != ig {
				gtx.Execute(op.InvalidateCmd{})
				u.w.Invalidate() // what changed here may show there
			}
		}()
		u.detachedKeys(gtx, d.id)
		if u.blur {
			gtx.Execute(key.FocusCmd{})
			u.blur = false
		}
		id, _ := u.termCard()
		d.shownTerm.Store(id)
		d.drewInfra = false
		gtx.Constraints.Min = gtx.Constraints.Max
		if v := u.view; v == "" || v == "sess:"+d.id { // its session; else what its header opened (workspace, drawings, terminal)
			u.sessionSplit(gtx, d.id)
		} else {
			u.mainView()(gtx)
		}
		if u.infraBy == d.id && !d.drewInfra { // gone from here: the main window may draw them again
			u.infraBy = ""
			u.infraWin.Store(nil)
		}
		u.sheet(gtx) // the card editor opened here
		u.toasts(gtx)
		if f := u.post; f != nil {
			u.post = nil
			f()
		}
	})
}

// detachedKeys are a detached window's own: esc backs out of a subagent or
// the rail, Ctrl+Shift+O puts the session back in the main window, and the
// text size keys.
func (u *ui) detachedKeys(gtx layout.Context, id string) {
	filters := kit.ZoomFilters()
	if _, tt := u.termCard(); tt == nil || u.edit != nil && u.edit.in == u.pane { // a terminal takes every other key, esc included
		filters = append(append(filters, key.Filter{Name: key.NameEscape}), keys.Filters(nil, "session.window")...)
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press || u.zoomKey(e) {
			continue
		}
		if keys.Is("session.window", e) {
			u.reattach(id)
		} else {
			u.detachedEscape(gtx)
		}
	}
}

// detachedEscape backs out of the innermost thing in a detached window, as
// esc does in the main one: its card editor, a drawing, the workspace's
// find bar, then the view its header opened, an answer field, a subagent,
// the rail.
func (u *ui) detachedEscape(gtx layout.Context) {
	ws := strings.TrimPrefix(u.view, "ws:")
	switch {
	case u.edit != nil && u.edit.in == u.pane && u.edit.id != "": // the drawer saves as it closes
		u.commit()
	case u.edit != nil && u.edit.in == u.pane:
		u.closeSheet()
	case u.drawOpen() != nil:
		u.drawEscape(u.drawOpen())
	case strings.HasPrefix(u.view, "ws:") && u.wsOf(ws).find != nil:
		u.wsFindClose(u.wsOf(ws))
	case u.view != "" && u.view != "sess:"+u.win.id: // the workspace, drawings or terminal: back to the session
		u.view, u.blur = "", true
	case u.typing(gtx) && !gtx.Focused(&u.chatIn):
		u.blur = true
	case u.sub != "":
		u.sub = ""
	case u.rail:
		u.rail = false
	}
}

// wakeDetached redraws every detached window but the one being laid out:
// work drained in one window may show in the others.
func (u *ui) wakeDetached() {
	for _, d := range u.detached {
		if &d.p != u.pane && d.w != nil {
			d.w.Invalidate()
		}
	}
}

// detachedHere is the placeholder the main window shows for a card whose
// session has its own window: where it is, and the way back.
func (u *ui) detachedHere(gtx layout.Context, id string, act *func()) layout.Dimensions {
	k := u.k
	kit.Fill(gtx, kit.CPanel)
	c := u.b.Card(id)
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return kit.Icon(28, kit.CText3, kit.IcoPopOut)(gtx)
			}),
			layout.Rigid(kit.VGap(10)),
			layout.Rigid(k.Lbl(c.Key()+" is open in its own window", 14, kit.CText, kit.Semi(kit.Sans))),
			layout.Rigid(kit.VGap(14)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d, ok := k.Button(gtx, "dtShow"+id, kit.CAcc, kit.Mix(kit.CAcc, kit.CWhite, .12), kit.RCtl, layout.Inset{Top: 7, Bottom: 7, Left: 14, Right: 14},
							k.Lbl("Show window", 13, kit.CWhite, kit.Semi(kit.Sans)))
						if ok {
							*act = func() { u.detach(id) }
						}
						return d
					}),
					layout.Rigid(kit.HGap(8)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d, ok := k.Button(gtx, "dtBack"+id, kit.CPanelCard, kit.CHover, kit.RCtl, layout.Inset{Top: 7, Bottom: 7, Left: 14, Right: 14},
							k.Lbl("Bring it back here", 13, kit.CText2, kit.Medium(kit.Sans)))
						if ok {
							*act = func() { u.reattach(id) }
						}
						return d
					}))
			}))
	})
}
