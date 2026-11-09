package ui

import (
	"cmp"
	"fmt"
	"image"
	"image/color"
	"slices"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// doneCards is the Done column's cards: All cards leaves them out, the Done
// archive shows them with the archived ones.
func (u *ui) doneCards() []kanban.Card {
	if d := u.b.Column("Done"); d >= 0 {
		return u.b.Columns[d].Cards
	}
	return nil
}

// isDone is whether card id sits in the Done column.
func (u *ui) isDone(id string) bool {
	c, _ := u.b.Find(id)
	return c >= 0 && c == u.b.Column("Done")
}

// archState is the archive view's own state: how it groups and which
// filter chips are lit. A chip is "done" or "archived" (both lit unless
// turned off), "org:<name>" or "label:<name>" (lit ones narrow to the cards
// with one of them, per kind).
type archState struct {
	byOrg bool
	off   map[string]bool // "done", "archived" turned off
	on    map[string]bool // "org:…", "label:…" turned on
	sel   map[string]bool // the cards checked, by id
	ver   int             // bumped by every change, for sig
}

// check checks or unchecks the cards es.
func (a *archState) check(es []archEntry, on bool) {
	if a.sel == nil {
		a.sel = map[string]bool{}
	}
	for _, e := range es {
		a.sel[e.c.ID] = on
	}
	a.ver++
}

// checked is the cards of es that are checked: what the bulk bar acts on,
// so a checked card the filters hide is left alone.
func (a *archState) checked(es []archEntry) []archEntry {
	var out []archEntry
	for _, e := range es {
		if a.sel[e.c.ID] {
			out = append(out, e)
		}
	}
	return out
}

func (a *archState) toggle(chip string) {
	m := &a.on
	if chip == "done" || chip == "archived" {
		m = &a.off
	}
	if *m == nil {
		*m = map[string]bool{}
	}
	(*m)[chip] = !(*m)[chip]
	a.ver++
}

// archEntry is a card the archive lists, and whether it's still in Done
// (else archived).
type archEntry struct {
	c    kanban.Card
	done bool
}

// archGroup is a section of the archive: its name and cards, newest first.
type archGroup struct {
	name string
	es   []archEntry
}

// archMatches is the Done and archived cards that match the search, before
// the chips.
func (u *ui) archMatches() []archEntry {
	var es []archEntry
	for _, c := range u.doneCards() {
		if c.Matches(u.search.Text()) {
			es = append(es, archEntry{c, true})
		}
	}
	for _, c := range u.b.Archive {
		if c.Matches(u.search.Text()) {
			es = append(es, archEntry{c, false})
		}
	}
	return es
}

// archKind is entry e's "done" or "archived" chip.
func archKind(e archEntry) string {
	if e.done {
		return "done"
	}
	return "archived"
}

// shows is whether the chips let e through.
func (a *archState) shows(e archEntry) bool {
	if a.off[archKind(e)] {
		return false
	}
	lit := func(kind string) (any, mine bool) {
		for chip, on := range a.on {
			if name, ok := strings.CutPrefix(chip, kind); on && ok {
				any = true
				mine = mine || kind == "org:" && e.c.Org == name || kind == "label:" && slices.Contains(e.c.Labels, name)
			}
		}
		return any, mine
	}
	for _, kind := range []string{"org:", "label:"} {
		if any, mine := lit(kind); any && !mine {
			return false
		}
	}
	return true
}

// archGroups sorts es newest finished first into sections: by when they
// finished (Today, Yesterday, This week, This month, then each earlier
// month) or by organization (alphabetical, none last).
func archGroups(es []archEntry, byOrg bool, now time.Time) []archGroup {
	es = slices.Clone(es)
	slices.SortStableFunc(es, func(a, b archEntry) int { return b.c.FinishedAt().Compare(a.c.FinishedAt()) })
	day := func(t time.Time) time.Time {
		y, m, d := t.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	}
	today := day(now)
	name := func(e archEntry) string {
		if byOrg {
			return cmp.Or(e.c.Org, "No organization")
		}
		t := e.c.FinishedAt().In(now.Location())
		switch d := day(t); {
		case !d.Before(today):
			return "Today"
		case d.Equal(today.AddDate(0, 0, -1)):
			return "Yesterday"
		case d.After(today.AddDate(0, 0, -7)):
			return "This week"
		case t.Year() == now.Year() && t.Month() == now.Month():
			return "This month"
		}
		return t.Format("January 2006")
	}
	var gs []archGroup
	for _, e := range es {
		n := name(e)
		i := slices.IndexFunc(gs, func(g archGroup) bool { return g.name == n })
		if i < 0 {
			gs, i = append(gs, archGroup{name: n}), len(gs)
		}
		gs[i].es = append(gs[i].es, e)
	}
	if byOrg {
		key := func(g archGroup) string { // none sorts after every name
			if g.name == "No organization" {
				return "\U0010FFFF"
			}
			return g.name
		}
		slices.SortStableFunc(gs, func(a, b archGroup) int { return cmp.Compare(key(a), key(b)) })
	}
	return gs
}

// archChips is the chips over the archive with their counts among the
// search's matches: Done, Archived, each organization, and the six most
// used labels.
func archChips(es []archEntry) (chips []string, n map[string]int) {
	n = map[string]int{}
	var orgs, labels []string
	for _, e := range es {
		n[archKind(e)]++
		if e.c.Org != "" {
			if n["org:"+e.c.Org]++; n["org:"+e.c.Org] == 1 {
				orgs = append(orgs, e.c.Org)
			}
		}
		for _, l := range e.c.Labels {
			if n["label:"+l]++; n["label:"+l] == 1 {
				labels = append(labels, l)
			}
		}
	}
	slices.Sort(orgs)
	slices.SortStableFunc(labels, func(a, b string) int { return cmp.Or(n["label:"+b]-n["label:"+a], cmp.Compare(a, b)) })
	chips = []string{"done", "archived"}
	for _, o := range orgs {
		chips = append(chips, "org:"+o)
	}
	for _, l := range labels[:min(len(labels), 6)] {
		chips = append(chips, "label:"+l)
	}
	return chips, n
}

// archive is the Done column's cards and the archived ones, filtered by the
// search and the chips, in sections by date or organization, one compact
// row each; a click opens a card's session to read or restore it.
func (u *ui) archive(gtx layout.Context) layout.Dimensions {
	k, a := u.k, &u.arch
	gtx.Constraints.Min = gtx.Constraints.Max
	kit.Fill(gtx, kit.CPane)
	all := u.archMatches()
	if len(u.doneCards())+len(u.b.Archive) == 0 {
		return layout.Center.Layout(gtx, k.Lbl("No done cards. Cards you move to Done show here.", 13, kit.CText3, kit.Sans))
	}
	var es []archEntry
	for _, e := range all {
		if a.shows(e) {
			es = append(es, e)
		}
	}
	gs := archGroups(es, a.byOrg, time.Now())
	type item struct {
		g *archGroup
		e *archEntry
	}
	var items []item
	for i := range gs {
		items = append(items, item{g: &gs[i]})
		for j := range gs[i].es {
			items = append(items, item{e: &gs[i].es[j]})
		}
	}
	chk := a.checked(es)
	var act func() // the bulk bar's, run once the list is drawn from the board as it was
	w := min(gtx.Constraints.Max.X-gtx.Dp(48), gtx.Dp(960))
	defer func() {
		if act != nil {
			act()
		}
	}()
	return layout.N.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints = layout.Constraints{Min: image.Pt(w, gtx.Constraints.Max.Y), Max: image.Pt(w, gtx.Constraints.Max.Y)}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 16, Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return u.archBar(gtx, all) })
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if len(chk) == 0 {
					return layout.Dimensions{}
				}
				return layout.Inset{Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return u.archBulkBar(gtx, chk, &act) })
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				if len(items) == 0 {
					return layout.N.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Top: 40}.Layout(gtx, k.Lbl("Nothing matches the search and filters.", 13, kit.CText3, kit.Sans))
					})
				}
				return u.archList.Layout(gtx, len(items), func(gtx layout.Context, n int) layout.Dimensions {
					if g := items[n].g; g != nil {
						return u.archHead(gtx, g, n == 0)
					}
					return u.archRow(gtx, *items[n].e)
				})
			}))
	})
}

// archBulkBar is "N selected" and what can be done to them: Archive the
// Done ones, Restore the archived ones, Delete, Clear. Its pick goes in act.
func (u *ui) archBulkBar(gtx layout.Context, chk []archEntry, act *func()) layout.Dimensions {
	k := u.k
	done := slices.ContainsFunc(chk, func(e archEntry) bool { return e.done })
	archived := slices.ContainsFunc(chk, func(e archEntry) bool { return !e.done })
	btn := func(id string, kind kit.Kind, label string, f func()) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			d, ok := k.KBtn(gtx, id, kind, label, "")
			if ok {
				*act = f
			}
			return layout.Inset{Left: 6}.Layout(gtx, func(layout.Context) layout.Dimensions { return d })
		})
	}
	return kit.Boxed(kit.CPanelCard, kit.CLine, kit.RCtl, layout.Inset{Top: 6, Bottom: 6, Left: 12, Right: 6}, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		ch := []layout.FlexChild{
			layout.Rigid(k.Lbl(fmt.Sprintf("%d selected", len(chk)), 13, kit.CText, kit.Semi(kit.Sans))),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)} }),
		}
		if done {
			ch = append(ch, btn("archBulkArchive", kit.Secondary, "Archive", func() { u.archBulk("archive", chk) }))
		}
		if archived {
			ch = append(ch, btn("archBulkRestore", kit.Secondary, "Restore", func() { u.archBulk("restore", chk) }))
		}
		ch = append(ch,
			btn("archBulkDelete", kit.Danger, "Delete", func() { u.archBulk("delete", chk) }),
			btn("archBulkClear", kit.Ghost, "Clear", func() { u.arch.sel = nil; u.arch.ver++ }))
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, ch...)
	})(gtx)
}

// archBulk archives the Done cards of es, restores the archived ones or
// deletes them all (their chat files stay), as one change the toast undoes:
// each card goes back where it was, as it was.
func (u *ui) archBulk(op string, es []archEntry) {
	var spots []kanban.Spot
	for _, e := range es {
		if s, ok := u.b.Spot(e.c.ID); ok && (op == "delete" || (op == "archive") == e.done) {
			spots = append(spots, s)
		}
	}
	// es comes in board order (archMatches), so each column's and the
	// archive's spots are lowest index first, as Put needs them back
	for _, s := range spots {
		id := s.Card.ID
		u.endSession(id)
		switch op {
		case "archive":
			u.b.ArchiveCard(id)
		case "restore":
			u.b.Restore(id)
		default:
			u.b.Delete(id)
		}
	}
	u.arch.sel = nil
	u.arch.ver++
	u.save()
	verb := map[string]string{"archive": "Archived", "restore": "Restored", "delete": "Deleted"}[op]
	n := fmt.Sprintf("%d cards", len(spots))
	if len(spots) == 1 {
		n = spots[0].Card.Key()
	}
	u.showUndo(verb+" "+n, func() {
		for _, s := range spots {
			u.b.Put(s)
		}
		u.save()
	})
}

// archBar is the row over the archive: Group by, then the filter chips.
func (u *ui) archBar(gtx layout.Context, all []archEntry) layout.Dimensions {
	k, a := u.k, &u.arch
	chips, n := archChips(all)
	ws := []layout.Widget{
		func(gtx layout.Context) layout.Dimensions { // down to the chips' text
			return layout.Inset{Top: 7}.Layout(gtx, k.Lbl("Group by", 12, kit.CText3, kit.Sans))
		},
		func(gtx layout.Context) layout.Dimensions {
			cur := 0
			if a.byOrg {
				cur = 1
			}
			return u.choice(gtx, "archGroup", []string{"Date", "Organization"}, cur, func(int) color.NRGBA { return color.NRGBA{} }, func(i int) {
				a.byOrg = i == 1
				a.ver++ // only that it changes matters, for sig
			})
		},
		kit.HGap(8),
	}
	for _, chip := range chips {
		ws = append(ws, u.archChip(chip, n[chip]))
	}
	return kit.Wrap(gtx, 6, ws)
}

// archChip is one filter chip: its name and count, lit while it filters
// (Done and Archived lit while shown).
func (u *ui) archChip(chip string, n int) layout.Widget {
	k, a := u.k, &u.arch
	name, c := archChipName(chip), kit.CText2
	if l, ok := strings.CutPrefix(chip, "label:"); ok {
		c = labelColor(l)
	} else if chip == "done" {
		c = kit.CGreen
	}
	lit := a.lit(chip)
	return func(gtx layout.Context) layout.Dimensions {
		fg, bg := kit.CText3, kit.CPanelCard
		if lit {
			fg, bg = c, kit.Mix(c, kit.CPanel, .82)
		}
		d, ok := k.Button(gtx, "archChip"+chip, bg, kit.CHover, kit.RCtl, layout.Inset{Top: 5, Bottom: 5, Left: 10, Right: 10}, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(k.Lbl(name, 12, fg, kit.Medium(kit.Sans))),
				layout.Rigid(kit.HGap(6)),
				layout.Rigid(k.Lbl(fmt.Sprint(n), 11, kit.CText3, kit.Mono)))
		})
		if ok {
			a.toggle(chip)
		}
		return d
	}
}

// archChipName is how chip reads: Done, Archived, the organization, #label.
func archChipName(chip string) string {
	switch chip {
	case "done":
		return "Done"
	case "archived":
		return "Archived"
	}
	if l, ok := strings.CutPrefix(chip, "label:"); ok {
		return "#" + l
	}
	return strings.TrimPrefix(chip, "org:")
}

// lit is whether chip shows lit: Done and Archived while not turned off,
// the others while turned on.
func (a *archState) lit(chip string) bool {
	if chip == "done" || chip == "archived" {
		return !a.off[chip]
	}
	return a.on[chip]
}

// archShown is what a row shows of c past its title: two labels at most
// and how many more, two links at most, and its cost ("" none).
func archShown(c kanban.Card) (labels []string, more int, ls []kanban.Link, cost string) {
	labels, ls = c.Labels[:min(len(c.Labels), 2)], c.Links[:min(len(c.Links), 2)]
	if c.Session.Cost > 0 {
		cost = fmt.Sprintf("$%.2f", c.Session.Cost)
	}
	return labels, len(c.Labels) - len(labels), ls, cost
}

// archBox is a checkbox button: a click calls f.
func (u *ui) archBox(gtx layout.Context, id string, on bool, f func()) layout.Dimensions {
	d, ok := u.k.Button(gtx, id, color.NRGBA{}, kit.CHover, kit.RCtl, layout.UniformInset(7), mark(on, true))
	if ok {
		f()
	}
	return d
}

// archHead is a section's checkbox (all its cards), name and count, with a
// rule under it.
func (u *ui) archHead(gtx layout.Context, g *archGroup, first bool) layout.Dimensions {
	k, a := u.k, &u.arch
	in := layout.Inset{Top: 14, Bottom: 6}
	if first {
		in.Top = 0
	}
	all := len(a.checked(g.es)) == len(g.es)
	return in.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		d := layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.archBox(gtx, "archChkGroup"+g.name, all, func() { a.check(g.es, !all) })
			}),
			layout.Rigid(kit.HGap(4)),
			layout.Rigid(k.Lbl(g.name, 13, kit.CText, kit.Semi(kit.Sans))),
			layout.Rigid(kit.HGap(8)),
			layout.Rigid(k.Lbl(fmt.Sprint(len(g.es)), 12, kit.CText3, kit.Mono)))
		paint.FillShape(gtx.Ops, kit.CLine, clip.Rect(image.Rect(0, d.Size.Y+gtx.Dp(5), gtx.Constraints.Max.X, d.Size.Y+gtx.Dp(6))).Op())
		d.Size.Y += gtx.Dp(6)
		return d
	})
}

// archRow is one card in a line: its checkbox, Done or Archived, key,
// title, organization, labels, links, cost and when it finished.
func (u *ui) archRow(gtx layout.Context, e archEntry) layout.Dimensions {
	c, a := e.c, &u.arch
	on := a.sel[c.ID]
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { // beside the row's button, not in it, so a click is one or the other
			return u.archBox(gtx, "archChk"+c.ID, on, func() { a.check([]archEntry{e}, !on) })
		}),
		layout.Rigid(kit.HGap(4)),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return u.archCard(gtx, e, on) }))
}

// archCard is a row past its checkbox, tinted while checked.
func (u *ui) archCard(gtx layout.Context, e archEntry, on bool) layout.Dimensions {
	k, c := u.k, e.c
	id := "arch" + c.ID
	bg := color.NRGBA{}
	switch {
	case on:
		bg = kit.Mix(kit.CAcc, kit.CPane, .88)
	case c.ID == u.panel:
		bg = kit.CPanelCard
	}
	d, ok := k.Button(gtx, id, bg, kit.CHover, kit.RCtl, layout.Inset{Top: 7, Bottom: 7, Left: 8, Right: 10}, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		kind, kc := "Archived", kit.CText3
		if e.done {
			kind, kc = "Done", kit.CGreen
		}
		var tail []layout.FlexChild
		if c.Org != "" {
			tail = append(tail, layout.Rigid(kit.HGap(8)), layout.Rigid(k.Pill(c.Org, kit.CText3, kit.CPanelCard)))
		}
		labels, more, ls, cost := archShown(c)
		for _, l := range labels {
			lc := labelColor(l)
			tail = append(tail, layout.Rigid(kit.HGap(6)), layout.Rigid(k.Pill(l, lc, kit.Mix(lc, kit.CPanel, .82))))
		}
		if more > 0 {
			tail = append(tail, layout.Rigid(kit.HGap(6)), layout.Rigid(k.Lbl(fmt.Sprintf("+%d", more), 11, kit.CText3, kit.Sans)))
		}
		for _, l := range ls {
			tail = append(tail, layout.Rigid(kit.HGap(6)), layout.Rigid(u.linkChip(l)))
		}
		if cost != "" {
			tail = append(tail, layout.Rigid(kit.HGap(10)), layout.Rigid(k.Lbl(cost, 11, kit.CText3, kit.Mono)))
		}
		tail = append(tail, layout.Rigid(kit.HGap(10)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Dp(72)
			return layout.E.Layout(gtx, k.Lbl(ago(c.FinishedAt(), time.Now()), 11, kit.CText3, kit.Sans))
		}))
		head := []layout.FlexChild{
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Dp(62)
				return k.Pill(kind, kc, kit.Mix(kc, kit.CPanel, .85))(gtx)
			}),
			layout.Rigid(kit.HGap(10)),
			layout.Rigid(k.Lbl(c.Key(), 12, kit.CText3, kit.Mono)),
			layout.Rigid(kit.HGap(10)),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, c.Title, 13, kit.CText, kit.Sans, 1) }),
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, append(head, tail...)...)
	})
	if ok {
		u.openPanel(c.ID)
	}
	return d
}
