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
	ver   int             // bumped by every change, for sig
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
	w := min(gtx.Constraints.Max.X-gtx.Dp(48), gtx.Dp(960))
	return layout.N.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints = layout.Constraints{Min: image.Pt(w, gtx.Constraints.Max.Y), Max: image.Pt(w, gtx.Constraints.Max.Y)}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 16, Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return u.archBar(gtx, all) })
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

// archBar is the row over the archive: Group by, then the filter chips.
func (u *ui) archBar(gtx layout.Context, all []archEntry) layout.Dimensions {
	k, a := u.k, &u.arch
	chips, n := archChips(all)
	ws := []layout.Widget{
		k.Lbl("Group by", 12, kit.CText3, kit.Sans),
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

// archHead is a section's name and count, with a rule under it.
func (u *ui) archHead(gtx layout.Context, g *archGroup, first bool) layout.Dimensions {
	k := u.k
	in := layout.Inset{Top: 18, Bottom: 6, Left: 4}
	if first {
		in.Top = 4
	}
	return in.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		d := layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
			layout.Rigid(k.Lbl(g.name, 13, kit.CText, kit.Semi(kit.Sans))),
			layout.Rigid(kit.HGap(8)),
			layout.Rigid(k.Lbl(fmt.Sprint(len(g.es)), 12, kit.CText3, kit.Mono)))
		paint.FillShape(gtx.Ops, kit.CLine, clip.Rect(image.Rect(0, d.Size.Y+gtx.Dp(5), gtx.Constraints.Max.X, d.Size.Y+gtx.Dp(6))).Op())
		d.Size.Y += gtx.Dp(6)
		return d
	})
}

// archRow is one card in a line: Done or Archived, key, title, organization,
// labels, links, cost and when it finished.
func (u *ui) archRow(gtx layout.Context, e archEntry) layout.Dimensions {
	k, c := u.k, e.c
	id := "arch" + c.ID
	bg := color.NRGBA{}
	if c.ID == u.panel {
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
