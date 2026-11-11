package ui

import (
	"cmp"
	"strings"

	"gioui.org/app"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/ui/kit"
)

// pane is what one window keeps for itself while it shows a card's
// session: its message box, scrolls, the subagent or history it shows, its
// keyboard. The main window has u.mainPane; a detached session's window its
// own. u.pane is the one being laid out, so the session code reads u.chatIn
// and friends whichever window it draws in.
type pane struct {
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
	id  string
	w   *app.Window
	p   pane
	ops op.Ops
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
		raise(d.w)
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
	startDetached(u, d)
}

// detachedOpts are the window's options, again after a lost display: Gio
// drops them once used.
func (u *ui) detachedOpts(d *detached) {
	title := "desk"
	if c := u.b.Card(d.id); c != nil {
		title = c.Key() + " · " + c.Title
	}
	d.w.Option(app.Title(title), app.Size(unit.Dp(1000), unit.Dp(900)), app.MinSize(unit.Dp(520), unit.Dp(400)))
}

// startDetached runs a detached window's events; tests open none. Set in
// init: it reaches back to detach through the session it draws.
var startDetached func(u *ui, d *detached)

func init() { startDetached = func(u *ui, d *detached) { go u.runDetached(d, d.w.Event) } }

// raise asks for w on top; tests have no window. ponytail: Wayland lets a
// window raise itself only with an activation token, so there it may not.
var raise = func(w *app.Window) { go w.Perform(system.ActionRaise) }

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
	if c := u.b.Card(id); c != nil {
		c.Detached = false
		u.save()
	}
	if d.w != nil {
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
			d.w = nil
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
	prevScope := u.k.Scope
	u.k.Scope = "w" + d.id + ":"
	defer func() { u.k.Scope = prevScope }()
	u.inPane(&d.p, func() {
		before := u.sig()
		defer func() {
			if u.sig() != before || len(u.events) > 0 {
				gtx.Execute(op.InvalidateCmd{})
				u.w.Invalidate() // what changed here may show there
			}
		}()
		u.detachedKeys(gtx, d.id)
		if u.blur {
			gtx.Execute(key.FocusCmd{})
			u.blur = false
		}
		u.sessionBody(gtx, d.id)
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
	filters := append(kit.ZoomFilters(), key.Filter{Name: key.NameEscape}, key.Filter{Name: "O", Required: key.ModShortcut | key.ModShift})
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press || u.zoomKey(e) {
			continue
		}
		switch {
		case e.Name == "O":
			u.reattach(id)
		case u.sub != "":
			u.sub = ""
		case u.rail:
			u.rail = false
		}
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
