package ui

import (
	"cmp"
	"fmt"
	"image"
	"image/color"
	"slices"
	"strings"
	"unicode/utf8"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/fzf"
	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/keys"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// The palette (Ctrl+K) goes to anything in desk: a card's panel, a server's
// session, a card's session full window, or an action. Tab cycles its
// scopes; an empty search shows the last items opened first.

// palScopes are the palette's tabs; an item's kind is its tab's index.
var palScopes = []string{"All", "Cards", "Servers", "Sessions", "Actions", "Files"}

const (
	palCard = 1 + iota
	palServer
	palSession
	palAction
	palFile // a card workspace's files, while it is the view (workspace_open.go)
)

// palRecents is how many opened items the empty search remembers.
const palRecents = 8

type palette struct {
	q     widget.Editor
	last  string // the query sel was picked under: a new one starts at the top
	scope int    // its index in palScopes
	sel   int    // the selected item, in the order shown
	focus bool   // focus q on the next frame
	list  layout.List
	files []*palItem // the workspace's files, walked as it opened there
	// what palRowsKept last worked out, and for what
	rowsKey string
	rows    []palRow
	order   []*palItem
}

// palItem is one thing the palette goes to.
type palItem struct {
	id         string // stable, for recents and buttons: "card:<id>", "host:<org>/<group>/<host>"…
	kind       int
	title, sub string // sub is muted after the title: "· Acme / web"
	mono       string // a card's key, before its title
	dot        color.NRGBA
	ico        kit.IconPath
	right      string // on the far right: a shortcut's cap (keyCap) or a server's state
	keyCap     bool
	rightC     color.NRGBA
	preview    [][2]string // what the right pane says: label, value
	run        func()
}

// palRow is a header (it nil) or an item.
type palRow struct {
	head string
	it   *palItem
}

func (u *ui) openPalette() {
	p := &palette{focus: true, list: layout.List{Axis: layout.Vertical}}
	p.q.SingleLine = true
	u.pal, u.menu = p, nil
	if id, ok := strings.CutPrefix(u.view, "ws:"); ok {
		p.files = u.wsFileItems(id)
	}
}

// scopes is how many of palScopes the palette shows: Files only with a
// workspace's files.
func (p *palette) scopes() int {
	if p.files == nil {
		return palFile
	}
	return len(palScopes)
}

// closePalette closes it; its search's focus goes with it (the router
// drops a handler no frame draws).
func (u *ui) closePalette() { u.pal = nil }

// palSig is the palette's part of sig.
func (u *ui) palSig() string {
	if u.pal == nil {
		return ""
	}
	return fmt.Sprint(u.pal.scope, u.pal.sel, u.pal.q.Text())
}

// palItems is everything the palette can go to, by kind.
func (u *ui) palItems() []*palItem {
	var cards, sessions, servers, actions []*palItem
	for ci, col := range u.b.Columns {
		for _, c := range col.Cards {
			if !c.Archived.IsZero() {
				continue
			}
			id := c.ID
			status := cmp.Or(c.Status, "idle")
			if c.Note != "" {
				status += " · " + c.Note
			}
			pv := [][2]string{{"Column", col.Title}, {"Organization", cmp.Or(c.Org, "none")}, {"Status", status}, {"Links", fmt.Sprint(len(c.Links))}}
			sub := ""
			if c.Org != "" {
				sub = "· " + c.Org
			}
			cards = append(cards, &palItem{id: "card:" + id, kind: palCard, title: c.Title, sub: sub, mono: c.Key(), dot: u.colAccent(ci), preview: pv,
				run: func() { u.openPanel(id) }})
			if u.procs[id] != nil || u.terms[id] != nil {
				ico := kit.IcoChat
				if u.terms[id] != nil {
					ico = kit.IcoTerminal
				}
				sessions = append(sessions, &palItem{id: "sess:" + id, kind: palSession, title: c.Title, sub: sub, mono: c.Key(), ico: ico, preview: pv,
					run: func() { u.openSession(id) }})
			}
		}
	}
	for _, o := range u.tree {
		org := o.Name
		servers = append(servers, &palItem{id: "srvc:" + org, kind: palServer, title: org + " Claude", sub: "· the org-wide session", ico: kit.IcoSpark,
			preview: [][2]string{{"Organization", org}}, run: func() { u.palServer(org, pickRow{}) }})
		for _, g := range o.Groups {
			for _, h := range g.Hosts {
				r := pickRow{g.Name, h}
				pv := [][2]string{{"Group", org + " / " + g.Name}}
				if h.Addr != "" {
					pv = append([][2]string{{"Address", h.Addr}}, pv...)
				}
				if h.Meta != "" && h.Meta != h.Addr {
					pv = append(pv, [2]string{"Status", h.Meta})
				}
				servers = append(servers, &palItem{id: "host:" + org + "/" + g.Name + "/" + h.Name, kind: palServer, title: h.Name, sub: "· " + org + " / " + g.Name,
					ico: kit.IcoTerminal, right: stateWord[h.State], rightC: serverColor[h.State], preview: pv, run: func() { u.palServer(org, r) }})
			}
		}
	}
	act := func(id, title, cap string, ico kit.IconPath, run func()) {
		actions = append(actions, &palItem{id: "act:" + id, kind: palAction, title: title, ico: ico, right: cap, keyCap: cap != "", run: run})
	}
	act("new", "New card", keys.Cap("board.new"), kit.IcoPlus, func() { u.newCard(u.selCol()) })
	for _, o := range u.orgList {
		act("new:"+o, "New card in "+o, "", kit.IcoPlus, func() {
			u.newCard(u.selCol())
			u.edit.org.SetText(o)
		})
	}
	act("settings", "Settings", "", kit.IcoEdit, func() { u.openConfig(u.lastOrg) })
	for _, o := range u.orgList {
		act("settings:"+o, "Open "+o+" settings", "", kit.IcoEdit, func() { u.openConfig(o) })
	}
	act("folders", "Search folders", "", kit.IcoSearch, func() { u.openConfig(cfgFolders) })
	act("keys", "Keyboard shortcuts", "", kit.IcoEdit, func() { u.openConfig(cfgKeys) })
	var files []*palItem
	if u.pal != nil {
		files = u.pal.files
	}
	return slices.Concat(cards, servers, sessions, actions, files)
}

// stateWord is how a server's state reads on its row; idle says nothing.
var stateWord = map[infra.State]string{infra.Connected: "connected", infra.Working: "with Claude", infra.Waiting: "waiting", infra.Alert: "alert"}

// palServer opens a server's session as the picker does.
func (u *ui) palServer(org string, r pickRow) {
	if u.infra != nil {
		u.pickServer(org, r)
	}
}

func (it *palItem) hay() string { return it.mono + " " + it.title + " " + it.sub }

// palRows is what the palette lists for query q in scope: the best match
// first ("Top result"), then the rest by kind, kinds in the order their best
// comes; with no query, the recent items, then all by kind. order is the
// items as shown, what sel counts.
func (u *ui) palRows(q string, scope int) (rows []palRow, order []*palItem) {
	var items []*palItem
	for _, it := range u.palItems() {
		if scope == 0 || it.kind == scope {
			items = append(items, it)
		}
	}
	add := func(head string, its []*palItem) {
		if len(its) == 0 {
			return
		}
		rows = append(rows, palRow{head: head})
		for _, it := range its {
			rows = append(rows, palRow{it: it})
			order = append(order, it)
		}
	}
	var rest []*palItem
	if strings.TrimSpace(q) == "" {
		var recent []*palItem
		for _, id := range u.recent {
			if i := slices.IndexFunc(items, func(it *palItem) bool { return it.id == id }); i >= 0 {
				recent = append(recent, items[i])
			}
		}
		add("Recent", recent)
		for _, it := range items {
			if !slices.Contains(recent, it) {
				rest = append(rest, it)
			}
		}
		for k := palCard; k < len(palScopes); k++ {
			add(palScopes[k], slices.DeleteFunc(slices.Clone(rest), func(it *palItem) bool { return it.kind != k }))
		}
		return rows, order
	}
	sorted := fzf.Sort(q, items, (*palItem).hay)
	if len(sorted) == 0 {
		return nil, nil
	}
	add("Top result", sorted[:1])
	rest = sorted[1:]
	var kinds []int
	for _, it := range rest {
		if !slices.Contains(kinds, it.kind) {
			kinds = append(kinds, it.kind)
		}
	}
	for _, k := range kinds {
		add(palScopes[k], slices.DeleteFunc(slices.Clone(rest), func(it *palItem) bool { return it.kind != k }))
	}
	return rows, order
}

// palPick closes the palette and goes to it, remembering it first.
func (u *ui) palPick(it *palItem) {
	u.recent = append([]string{it.id}, slices.DeleteFunc(u.recent, func(id string) bool { return id == it.id })...)
	u.recent = u.recent[:min(len(u.recent), palRecents)]
	u.closePalette()
	it.run()
}

// palMarks is where the query's words land in s, for the highlight: a word
// found whole marks there, else where fzf.Runes finds its letters.
func palMarks(q, s string) map[int]bool {
	m := map[int]bool{}
	low := strings.ToLower(s)
	for _, w := range strings.Fields(strings.ToLower(q)) {
		if strings.HasPrefix(w, "!") || w == "|" {
			continue
		}
		at := fzf.Runes(w, s)
		if t := strings.Trim(w, "'^$"); t != "" {
			if b := strings.Index(low, t); b >= 0 {
				at = nil
				for j := range utf8.RuneCountInString(t) {
					at = append(at, utf8.RuneCountInString(low[:b])+j)
				}
			}
		}
		for _, i := range at {
			m[i] = true
		}
	}
	return m
}

// palRowsKept is palRows for p's query and scope, kept until they, the
// board or the servers change: it builds and sorts every card and host.
func (u *ui) palRowsKept(p *palette) ([]palRow, []*palItem) {
	k := fmt.Sprint(p.q.Text(), "\x00", p.scope, u.gen, u.cardCount(), len(u.tree), u.tkGen, len(u.orgList))
	if k != p.rowsKey {
		p.rowsKey = k
		p.rows, p.order = u.palRows(p.q.Text(), p.scope)
	}
	return p.rows, p.order
}

// paletteDialog draws the palette over everything and takes its keys:
// up and down move, enter opens, tab the next scope (esc is keys'). Clicks
// run once it is drawn.
func (u *ui) paletteDialog(gtx layout.Context) {
	p := u.pal
	if p == nil {
		return
	}
	if q := p.q.Text(); q != p.last {
		p.sel, p.last = 0, q
	}
	rows, order := u.palRowsKept(p)
	for {
		ev, ok := gtx.Event(append(keys.Filters(nil, "pal.next", "pal.prev"), key.Filter{Name: key.NameUpArrow}, key.Filter{Name: key.NameDownArrow},
			key.Filter{Name: key.NameReturn}, key.Filter{Name: key.NameEnter})...)
		if !ok {
			break
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		switch n := p.scopes(); {
		case keys.Is("pal.next", e), keys.Is("pal.prev", e):
			if keys.Is("pal.prev", e) {
				p.scope = (p.scope + n - 1) % n
			} else {
				p.scope = (p.scope + 1) % n
			}
			p.sel = 0
			rows, order = u.palRowsKept(p)
		case e.Name == key.NameUpArrow:
			p.sel = max(p.sel-1, 0)
		case e.Name == key.NameDownArrow:
			p.sel = max(min(p.sel+1, len(order)-1), 0)
		case e.Name == key.NameReturn, e.Name == key.NameEnter:
			if p.sel < len(order) {
				u.palPick(order[p.sel])
				return
			}
		}
		u.palReveal(rows, order)
	}
	if p.focus {
		gtx.Execute(key.FocusCmd{Tag: &p.q})
		p.focus = false
	}
	var act func() // a click: run after drawing
	k := u.k
	sel := (*palItem)(nil)
	if p.sel < len(order) {
		sel = order[p.sel]
	}
	line := func(gtx layout.Context) layout.Dimensions {
		r := image.Rect(0, 0, gtx.Constraints.Max.X, gtx.Dp(1))
		kit.FillRRect(gtx.Ops, r, 0, kit.CLine)
		return layout.Dimensions{Size: r.Max}
	}
	search := func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.Y = gtx.Dp(52)
		return layout.Inset{Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(kit.Icon(16, kit.CText3, kit.IcoSearch)), layout.Rigid(kit.HGap(10)),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.Y = 0 // the row's height would put the text at its top
					return k.Editor(gtx, &p.q, "Go to a card, server, session or action", 16, kit.Medium(kit.Sans), kit.CText)
				}),
				layout.Rigid(k.KeyCap("esc", false)))
		})
	}
	tabs := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Left: 12, Right: 12, Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			var ch []layout.FlexChild
			for i, s := range palScopes[:p.scopes()] {
				ch = append(ch, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					fg, fill := kit.CText3, color.NRGBA{}
					if i == p.scope {
						fg, fill = kit.CText, kit.CHover
					}
					d, ok := k.Button(gtx, "palScope"+s, fill, kit.CHover, 4, layout.Inset{Top: 4, Bottom: 4, Left: 9, Right: 9}, k.Lbl(s, 12, fg, kit.Semi(kit.Sans)))
					if ok {
						act = func() { p.scope, p.sel, p.focus = i, 0, true }
					}
					return layout.Inset{Right: 2}.Layout(gtx, func(layout.Context) layout.Dimensions { return d })
				}))
			}
			ch = append(ch, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)}
			}),
				layout.Rigid(k.Hint(keys.Cap("pal.next"), "next scope")))
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, ch...)
		})
	}
	q := p.q.Text()
	item := func(gtx layout.Context, it *palItem) layout.Dimensions {
		on := it == sel
		fill := color.NRGBA{}
		if on {
			fill = kit.CSel
		}
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		d, ok := k.Button(gtx, "pal"+it.id, fill, kit.Mix(kit.CPanel, kit.CHover, .6), 0, layout.Inset{Left: 16, Right: 16}, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, gtx.Dp(36))
			ch := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions { // the icon's box
				s := gtx.Dp(22)
				bg, fg := kit.CPanelCard, kit.CText2
				if on {
					bg, fg = kit.CAccSoft, kit.CAccText
				}
				kit.FillRRect(gtx.Ops, image.Rect(0, 0, s, s), gtx.Dp(5), bg)
				if it.ico == nil {
					m := gtx.Dp(7)
					kit.FillRRect(gtx.Ops, image.Rect(m, m, s-m, s-m), gtx.Dp(2), it.dot)
				} else {
					defer op.Offset(image.Pt(gtx.Dp(5), gtx.Dp(5))).Push(gtx.Ops).Pop()
					kit.Icon(12, fg, it.ico)(gtx)
				}
				return layout.Dimensions{Size: image.Pt(s, s)}
			}), layout.Rigid(kit.HGap(10))}
			if it.mono != "" {
				ch = append(ch, layout.Rigid(k.Lbl(it.mono, 12, kit.CText3, kit.Mono)), layout.Rigid(kit.HGap(10)))
			}
			ch = append(ch, layout.Rigid(k.Marked(it.title, palMarks(q, it.title), 13, kit.CText, kit.Sans)))
			if it.sub != "" {
				ch = append(ch, layout.Rigid(kit.HGap(8)), layout.Rigid(k.Marked(it.sub, palMarks(q, it.sub), 12, kit.CText3, kit.Sans)))
			}
			ch = append(ch, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)}
			}))
			switch {
			case it.keyCap:
				ch = append(ch, layout.Rigid(kit.HGap(8)), layout.Rigid(k.KeyCap(it.right, false)))
			case it.right != "":
				ch = append(ch, layout.Rigid(kit.HGap(8)), layout.Rigid(k.Lbl(it.right, 12, it.rightC, kit.Sans)))
			}
			if on {
				ch = append(ch, layout.Rigid(kit.HGap(8)), layout.Rigid(k.KeyCap("↵", false)))
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, ch...)
		})
		if ok {
			act = func() { u.palPick(it) }
		}
		return d
	}
	list := func(gtx layout.Context) layout.Dimensions {
		if len(rows) == 0 {
			return layout.Inset{Top: 14, Bottom: 14, Left: 16}.Layout(gtx, k.Lbl("Nothing matches.", 13, kit.CText3, kit.Sans))
		}
		return layout.Inset{Bottom: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return p.list.Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
				if r := rows[i]; r.it != nil {
					return item(gtx, r.it)
				}
				return layout.Inset{Top: 10, Bottom: 4, Left: 16, Right: 16}.Layout(gtx, k.Lbl(rows[i].head, 11, kit.CText3, kit.Semi(kit.Sans)))
			})
		})
	}
	preview := func(gtx layout.Context) layout.Dimensions {
		if sel == nil {
			return layout.Dimensions{}
		}
		return layout.UniformInset(14).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			head := sel.title
			if sel.mono != "" {
				head = sel.mono + " · " + head
			}
			ch := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return k.Label(gtx, head, 14, kit.CText, kit.Bold(kit.Sans), 2)
			})}
			for _, pv := range sel.preview {
				f := kit.Sans
				if pv[0] == "Address" {
					f = kit.Mono
				}
				ch = append(ch, layout.Rigid(kit.VGap(8)), layout.Rigid(k.Lbl(pv[0], 12, kit.CText3, kit.Sans)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, pv[1], 12, kit.CText2, f, 3) }))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, ch...)
		})
	}
	body := func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.Y = min(gtx.Constraints.Max.Y, gtx.Dp(440))
		pw := gtx.Dp(230)
		lg := gtx
		lg.Constraints.Min = image.Point{}
		lg.Constraints.Max.X -= pw + gtx.Dp(1)
		m := op.Record(gtx.Ops)
		ld := list(lg)
		lc := m.Stop()
		pg := gtx
		pg.Constraints = layout.Constraints{Max: image.Pt(pw, gtx.Constraints.Max.Y)}
		m = op.Record(gtx.Ops)
		pd := preview(pg)
		pc := m.Stop()
		h := min(max(ld.Size.Y, pd.Size.Y, gtx.Dp(120)), gtx.Constraints.Max.Y)
		lc.Add(gtx.Ops)
		x := gtx.Constraints.Max.X - pw - gtx.Dp(1)
		kit.FillRRect(gtx.Ops, image.Rect(x, 0, x+gtx.Dp(1), h), 0, kit.CLine)
		defer op.Offset(image.Pt(x+gtx.Dp(1), 0)).Push(gtx.Ops).Pop()
		pc.Add(gtx.Ops)
		return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, h)}
	}
	foot := func(gtx layout.Context) layout.Dimensions {
		return layout.Stack{}.Layout(gtx,
			layout.Expanded(func(gtx layout.Context) layout.Dimensions {
				kit.FillRRect(gtx.Ops, image.Rectangle{Max: gtx.Constraints.Min}, 0, kit.CFoot)
				return layout.Dimensions{Size: gtx.Constraints.Min}
			}),
			layout.Stacked(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Inset{Top: 9, Bottom: 9, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(k.KeyCap("↑", false)), layout.Rigid(kit.HGap(4)), layout.Rigid(k.Hint("↓", "move")), layout.Rigid(kit.HGap(14)),
						layout.Rigid(k.Hint("↵", "open")),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)}
						}),
						layout.Rigid(k.Lbl("Empty search shows recents", 12, kit.CText3, kit.Sans)))
				})
			}))
	}
	closed := false
	k.Modal(gtx, p, 84, 700, func() { closed = true }, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(search), layout.Rigid(tabs), layout.Rigid(line),
			layout.Rigid(body), layout.Rigid(line), layout.Rigid(foot))
	})
	switch {
	case act != nil:
		act()
	case closed:
		u.closePalette()
	}
}

// palReveal scrolls the list so the selected item shows.
func (u *ui) palReveal(rows []palRow, order []*palItem) {
	p := u.pal
	if p.sel >= len(order) {
		return
	}
	r := slices.IndexFunc(rows, func(r palRow) bool { return r.it == order[p.sel] })
	if p.sel == 0 {
		r = 0 // its header too
	}
	if pos := p.list.Position; r < pos.First || r >= pos.First+pos.Count-1 {
		p.list.ScrollTo(max(r-1, 0))
	}
}
