package ui

import (
	"cmp"
	"errors"
	"fmt"
	"image"
	"image/color"
	"regexp"
	"strings"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/fzf"
	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// The infra workspace (internal/infra) lives in desk: its
// host tree under each organization in the sidebar (Servers, then under it
// what has a session: the org's Claude, connected hosts; a double click on
// Servers opens the picker of them all), its main column as the srv:<org>
// view. Views: "srv:<org>"; sidebar-only rows "srvc:<org>" (the org's
// Claude) and "host:<org>/<group>/<host>".

// infraTasks is the workspace's open peer tasks; tests replace it (a real
// one starts a Claude).
var infraTasks = (*infra.Workspace).Tasks

// infraOrg is org's part of this frame's host tree, if it has servers.
func (u *ui) infraOrg(org string) *infra.Org {
	for i := range u.tree {
		if u.tree[i].Name == org {
			return &u.tree[i]
		}
	}
	return nil
}

// infraGen is the workspace's redraw count (0 without one, in tests).
func (u *ui) infraGen() uint64 {
	if u.infra == nil {
		return 0
	}
	return u.infra.Gen()
}

// infraFrame reads the host tree once a frame and tells the workspace
// whether it's what the user looks at (its notifications go out when not).
func (u *ui) infraFrame() {
	if u.infra == nil {
		return
	}
	u.tree, u.infraTasks = u.infra.Tree(), infraTasks(u.infra)
	for _, t := range u.infraTasks { // remembered past the task: the split and the way back stay
		if c := u.cardOfKey(t.From); c != nil {
			u.cardInfra[c.ID] = t.Org
		}
	}
	split := u.splitShown
	u.splitShown = false
	if !split {
		u.splitFor = "" // coming back to a split asks for its org again
	}
	u.infra.SetShown(strings.HasPrefix(u.view, "srv:") || split) // hidden, its terminals' output asks no frames of desk
	if on := u.focused && (strings.HasPrefix(u.view, "srv:") || split); on != u.infraOn {
		u.infraOn = on
		u.infra.SetFocused(on)
	}
}

// cardTask is the newest open server task a card's Claude handed over
// (desk peer do --from its key).
func (u *ui) cardTask(key string) (infra.Task, bool) {
	for i := len(u.infraTasks) - 1; i >= 0; i-- {
		if u.infraTasks[i].From == key {
			return u.infraTasks[i], true
		}
	}
	return infra.Task{}, false
}

// showTask shows the infra session working on t: its org's Claude tab.
// cardKey is what a card's key reads like (DESK-12): an infra peer that is a card.
var cardKey = regexp.MustCompile(`^` + kanban.Prefix + `-\d+$`)

// boardWait is how long an infra Claude's tool waits for the board.
var boardWait = 10 * time.Second

// onBoard runs f on the UI's goroutine and waits for it: the infra Claudes'
// tools reach the board through it.
func onBoard[T any](u *ui, f func() (T, error)) (T, error) {
	type res struct {
		v   T
		err error
	}
	done := make(chan res, 1)
	u.later(func() { v, err := f(); done <- res{v, err} })
	select {
	case r := <-done:
		return r.v, r.err
	case <-time.After(boardWait):
		var zero T
		return zero, errors.New("desk's board didn't answer in time; it may still do it, so look before asking again") // the work stays queued
	}
}

// cardTell brings an infra Claude's question, result or message into card
// key's session, where its Claude answers with desk peer: no desk peer wait
// to keep running. While the card can't take it (its terminal open, a "!"
// command running, a permission question up) it waits in peerOut for the
// next message, or the turn's end.
// cardOfKey is the board's card with key (DESK-12), nil none.
func (u *ui) cardOfKey(key string) *kanban.Card {
	if !cardKey.MatchString(key) {
		return nil
	}
	for _, col := range u.b.Columns {
		for i := range col.Cards {
			if col.Cards[i].Key() == key {
				return &col.Cards[i]
			}
		}
	}
	return nil
}

func (u *ui) cardTell(key, text string) error {
	c := u.cardOfKey(key)
	if c == nil {
		return fmt.Errorf("%s isn't on the board (archived?)", key)
	}
	u.say(c.ID, "peer", text)
	if u.terms[c.ID] != nil || u.shellStop[c.ID] != nil || u.asks[c.ID] != nil {
		u.peerOut[c.ID] += "From the infra Claude (desk peer):\n" + text + "\n\n"
		return nil
	}
	u.deliver(c.ID, text, true)
	return nil
}

// cardNew puts an infra Claude's follow-up work in org's To do: its key.
func (u *ui) cardNew(org, title, notes string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", errors.New("the card needs a title")
	}
	col := u.b.Column("To do")
	if col < 0 {
		col = 0
	}
	c := u.b.Add(col, kanban.Card{Title: title, Notes: notes + "\n\n(From " + org + "'s infra Claude.)", Org: org})
	u.save()
	u.showToast(org+"'s infra Claude made "+c.Key()+": "+title, false)
	return c.Key(), nil
}

func (u *ui) showTask(t infra.Task) {
	if u.infra != nil && u.infra.OpenCard(t.Org, t.From) {
		u.view, u.panel = "srv:"+t.Org, ""
	}
}

var serverColor = map[infra.State]color.NRGBA{
	infra.Idle: kit.CText3, infra.Connected: kit.CGreen, infra.Working: kit.CPurple, infra.Waiting: kit.COrange, infra.Alert: kit.CRed,
}

// serverItems is org's Servers row and under it what has a session: the
// org's Claude and the connected hosts. The whole list is the picker's
// (a double click on Servers).
func (u *ui) serverItems(org string) []sideItem {
	o := u.infraOrg(org)
	if o == nil {
		return nil
	}
	items := []sideItem{{"srv:" + org, "Servers", kit.IcoTerminal, serverColor[o.State]}}
	if o.Claude {
		items = append(items, sideItem{"srvc:" + org, org + " Claude", kit.IcoSpark, kit.CPurple})
	}
	for _, g := range o.Groups {
		for _, h := range g.Hosts {
			if h.Open {
				items = append(items, sideItem{"host:" + org + "/" + g.Name + "/" + h.Name, h.Name, nil, serverColor[h.State]})
			}
		}
	}
	return items
}

// doubleClick is how close two clicks on Servers come to open the picker.
const doubleClick = 400 * time.Millisecond

// serversClicked is a click on org's Servers row: its view, and the picker
// of all its servers when it's the second in a row.
func (u *ui) serversClicked(org string, now time.Time) {
	if u.srvClickOf == org && now.Sub(u.srvClickAt) < doubleClick {
		u.pick, u.srvClickOf = org, ""
		u.pickQ.SetText("")
		u.pickSel, u.pickLast, u.pickFocus = 0, "", true
		return
	}
	u.srvClickOf, u.srvClickAt = org, now
}

// pickRow is one row of the server picker: the org's Claude (host "") or
// a host.
type pickRow struct {
	group string
	host  infra.Host
}

// pickRows is org's servers matching q, by group, the org's Claude first.
func (u *ui) pickRows(org, q string) []pickRow {
	o := u.infraOrg(org)
	if o == nil {
		return nil
	}
	var rs []pickRow
	if _, ok := fzf.Match(q, org+" claude"); ok {
		rs = append(rs, pickRow{})
	}
	for _, g := range o.Groups {
		for _, h := range g.Hosts {
			if _, ok := fzf.Match(q, g.Name+" "+h.Name+" "+h.Addr+" "+h.Meta); ok {
				rs = append(rs, pickRow{g.Name, h})
			}
		}
	}
	return rs
}

// pickServer opens r's session and closes the picker.
func (u *ui) pickServer(org string, r pickRow) {
	if r.host.Name == "" {
		u.infra.OpenOrg(org)
	} else {
		u.infra.OpenHost(org, r.group, r.host.Name)
	}
	u.view, u.pick = "srv:"+org, ""
}

// pickAt is the list index of rows[i]: the group headers come before
// their first host.
func pickAt(rows []pickRow, i int) int {
	at, group := 0, "\x00"
	for j, r := range rows[:i+1] {
		if r.host.Name != "" && r.group != group {
			group = r.group
			at++
		}
		if j < i {
			at++
		}
	}
	return at
}

// serverPicker is the dialog of all an org's servers, by group, searched as
// typed; up and down move the selection (the first row at the start), and
// enter or a click opens its session.
func (u *ui) serverPicker(gtx layout.Context) {
	org := u.pick
	if org == "" || u.infra == nil {
		return
	}
	k := u.k
	if q := u.pickQ.Text(); q != u.pickLast {
		u.pickSel, u.pickLast = 0, q
	}
	rows := u.pickRows(org, u.pickQ.Text())
	u.pickSel = max(min(u.pickSel, len(rows)-1), 0)
	open := func() bool {
		if u.pickSel < len(rows) {
			u.pickServer(org, rows[u.pickSel])
			return true
		}
		return false
	}
	for { // focus-less filters, so they beat the search field's own
		ev, ok := gtx.Event(key.Filter{Name: key.NameUpArrow}, key.Filter{Name: key.NameDownArrow},
			key.Filter{Name: key.NamePageUp}, key.Filter{Name: key.NamePageDown},
			key.Filter{Name: key.NameReturn}, key.Filter{Name: key.NameEnter})
		if !ok {
			break
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		step := 0
		switch e.Name {
		case key.NameUpArrow:
			step = -1
		case key.NameDownArrow:
			step = 1
		case key.NamePageUp:
			step = -pickPage
		case key.NamePageDown:
			step = pickPage
		default:
			if open() {
				return
			}
		}
		u.pickSel = max(min(u.pickSel+step, len(rows)-1), 0)
		u.pickReveal(rows)
	}
	for {
		ev, ok := u.pickQ.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok && open() {
			return
		}
	}
	if u.pickFocus {
		gtx.Execute(key.FocusCmd{Tag: &u.pickQ})
		u.pickFocus = false
	}
	paint.FillShape(gtx.Ops, kit.CScrim, clip.Rect{Max: gtx.Constraints.Max}.Op())
	for { // a press outside closes it
		ev, ok := gtx.Event(pointer.Filter{Target: &u.pick, Kinds: pointer.Press})
		if !ok {
			break
		}
		if _, ok := ev.(pointer.Event); ok {
			u.pick = ""
		}
	}
	cl := clip.Rect(image.Rectangle{Max: gtx.Constraints.Max}).Push(gtx.Ops)
	event.Op(gtx.Ops, &u.pick)
	cl.Pop()
	top := op.Offset(image.Pt(0, gtx.Dp(110))).Push(gtx.Ops)
	defer top.Pop()
	cgtx := gtx
	cgtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, 0)
	cgtx.Constraints.Max.Y -= gtx.Dp(160)
	layout.N.Layout(cgtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X-gtx.Dp(32), gtx.Dp(560))
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return kit.Boxed(kit.CPanel, kit.CRim, kit.RSheet, layout.Inset{Top: 16, Bottom: 12, Left: 16, Right: 16}, func(gtx layout.Context) layout.Dimensions {
			box := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops) // its own presses don't close it
			event.Op(gtx.Ops, &u.pickList)
			box.Pop()
			var ws []layout.Widget
			at := -1
			if len(rows) > 0 {
				at = pickAt(rows, u.pickSel)
			}
			group := "\x00"
			for _, r := range rows {
				if r.host.Name != "" && r.group != group {
					group = r.group
					ws = append(ws, func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Top: 10, Bottom: 4, Left: 4}.Layout(gtx, k.Lbl(r.group, 12, kit.CText3, kit.Semi(kit.Sans)))
					})
				}
				on := len(ws) == at
				ws = append(ws, func(gtx layout.Context) layout.Dimensions { return u.pickItem(gtx, org, r, on) })
			}
			if len(ws) == 0 {
				ws = append(ws, func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: 14, Bottom: 10, Left: 4}.Layout(gtx, k.Lbl("No server matches.", 13, kit.CText3, kit.Sans))
				})
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(k.Lbl(org+" · servers", 15, kit.CText, kit.Semi(kit.Sans))),
				layout.Rigid(kit.VGap(10)),
				layout.Rigid(kit.Boxed(kit.CPanelCard, kit.CRim, kit.RCtl, layout.Inset{Top: 8, Bottom: 8, Left: 10, Right: 10}, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return k.Editor(gtx, &u.pickQ, "Find a server: name, group, address", 13, kit.Sans, kit.CText)
				})),
				layout.Rigid(kit.VGap(4)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { // as tall as its rows, up to the window
					gtx.Constraints.Min.Y = 0
					return kit.ScrollList(gtx, &u.pickList, len(ws), func(gtx layout.Context, i int) layout.Dimensions { return ws[i](gtx) })
				}))
		})(gtx)
	})
}

// pickPage is how many rows page up and down move the picker's selection.
const pickPage = 8

// pickReveal scrolls the picker's list to keep the selected row in view;
// the first row takes its group's header along.
func (u *ui) pickReveal(rows []pickRow) {
	if len(rows) == 0 {
		return
	}
	r := pickAt(rows, u.pickSel)
	if u.pickSel == 0 {
		r = 0
	}
	if pos := u.pickList.Position; r < pos.First || r >= pos.First+pos.Count-1 {
		u.pickList.ScrollTo(max(r-1, 0))
	}
}

// pickItem is a picker row: its state dot, name and what the host tree prints
// beside it (address, "2 panes", the alert); on, the selected one, shows
// the enter key.
func (u *ui) pickItem(gtx layout.Context, org string, r pickRow, on bool) layout.Dimensions {
	k := u.k
	name, ico, c := r.host.Name, kit.IconPath(nil), serverColor[r.host.State]
	meta := cmp.Or(r.host.Meta, r.host.Addr)
	if name == "" {
		name, ico, c, meta = org+" Claude", kit.IcoSpark, kit.CPurple, "the org-wide session, no server"
	}
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	fill := color.NRGBA{}
	if on {
		fill = kit.CSel
	}
	d, ok := k.Button(gtx, "pick"+r.group+"/"+r.host.Name, fill, kit.CHover, kit.RCtl, layout.Inset{Top: 6, Bottom: 6, Left: 8, Right: 8}, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		mark := kit.Dot(c)
		if ico != nil {
			mark = kit.Icon(14, c, ico)
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Dp(16)
				return layout.W.Layout(gtx, mark)
			}),
			layout.Rigid(kit.HGap(8)),
			layout.Flexed(1, k.LblFill(name, 13, kit.CText, kit.Medium(kit.Sans))),
			layout.Rigid(k.Lbl(meta, 12, kit.CText3, kit.Sans)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if !on {
					return layout.Dimensions{}
				}
				return layout.Inset{Left: 8}.Layout(gtx, k.KeyCap("↵", false))
			}))
	})
	if ok {
		u.pickServer(org, r)
	}
	return d
}

// serverIndent is how far a server row sits in, under its organization;
// 0 not one.
func serverIndent(view string) int {
	switch {
	case strings.HasPrefix(view, "srv:"):
		return 24
	case strings.HasPrefix(view, "srvc:"), strings.HasPrefix(view, "host:"):
		return 36
	}
	return 0
}

// serverClick does a sidebar server row's click; false: not one, or one
// that is a view (Servers itself).
func (u *ui) serverClick(view string) bool {
	if u.infra == nil {
		return false
	}
	if org, ok := strings.CutPrefix(view, "srvc:"); ok {
		u.infra.OpenOrg(org)
		u.view = "srv:" + org
		return true
	}
	if key, ok := strings.CutPrefix(view, "host:"); ok {
		p := strings.SplitN(key, "/", 3)
		if len(p) == 3 {
			u.infra.OpenHost(p[0], p[1], p[2])
			u.view = "srv:" + p[0]
		}
		return true
	}
	return false
}

// serversView is the org's infra workspace: its tabs, panes and Claude. It
// follows the workspace when that moves to another org (the palette, a
// peer's task, the health check).
func (u *ui) serversView(gtx layout.Context) layout.Dimensions {
	org, ok := strings.CutPrefix(u.view, "srv:")
	if !ok { // the sidebar, drawn first, just left for another view: this frame's last draw of it
		return layout.Dimensions{Size: gtx.Constraints.Max}
	}
	if u.infra == nil {
		return layout.Center.Layout(gtx, u.k.Lbl("The infra workspace isn't running.", 13, kit.CText3, kit.Sans))
	}
	if a := u.infra.ActiveOrg(); a != "" && a != org && u.infraOrg(a) != nil {
		org = a
		u.view = "srv:" + a
	}
	var back []layout.Widget // the cards whose server work this is: a click goes back to one
	for i, col := range u.b.Columns {
		for _, c := range col.Cards {
			if u.cardInfra[c.ID] != org || i == u.b.Column("Done") {
				continue
			}
			back = append(back, func(gtx layout.Context) layout.Dimensions {
				d, ok := u.k.Button(gtx, "srvBack"+c.ID, kit.CPanelCard, kit.CHover, kit.RCtl, layout.Inset{Top: 3, Bottom: 3, Left: 8, Right: 8},
					u.k.Lbl("← "+c.Key()+" "+c.Title, 12, kit.CAccText, kit.Medium(kit.Sans)))
				if ok {
					u.openSession(c.ID)
				}
				return d
			})
		}
	}
	if len(back) == 0 {
		return u.infra.Layout(gtx, org)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 5, Bottom: 5, Left: 10, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return kit.Wrap(gtx, 6, back) })
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return u.infra.Layout(gtx, org) }))
}

// serversSentence is the header for an org's servers: "2 connected · 1 waiting".
func (u *ui) serversSentence(org string) string {
	o := u.infraOrg(org)
	if o == nil {
		return ""
	}
	n := map[infra.State]int{}
	hosts := 0
	for _, g := range o.Groups {
		for _, h := range g.Hosts {
			n[h.State]++
			hosts++
		}
	}
	var p []string
	for _, s := range []struct {
		st   infra.State
		word string
	}{{infra.Working, "with Claude"}, {infra.Waiting, "waiting"}, {infra.Connected, "connected"}, {infra.Alert, "with an alert"}} {
		if n[s.st] > 0 {
			p = append(p, fmt.Sprintf("%d %s", n[s.st], s.word))
		}
	}
	if len(p) == 0 {
		return plural(hosts, "server", "servers") + ": pick one, or the org's Claude"
	}
	return plural(hosts, "server", "servers") + " · " + strings.Join(p, " · ")
}
