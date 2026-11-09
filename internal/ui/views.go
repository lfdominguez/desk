package ui

import (
	"cmp"
	"fmt"
	"image"
	"image/color"
	"maps"
	"slices"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// Views are the sidebar's filters, by key: "" all cards, "st:<status>",
// "org:<name>", "tk:<org>" (its tickets, not cards: tickets.go), "lab:<label>", "ln:review|ready|changes|tickets",
// "due:over|week", and the archive.
const viewArchive = "archive"

// inView reports whether card c belongs in view v.
func inView(v string, c kanban.Card, now time.Time) bool {
	kind, arg, _ := strings.Cut(v, ":")
	switch kind {
	case "":
		return true
	case "st":
		return c.Status == arg
	case "org":
		return c.Org == arg
	case "lab":
		return slices.Contains(c.Labels, arg)
	case "ln":
		return slices.ContainsFunc(c.Links, func(l kanban.Link) bool {
			pr := l.Kind == links.KindGitHubPR || l.Kind == links.KindBitbucketPR || l.Kind == links.KindGitLabMR
			switch arg {
			case "review":
				return pr && l.Tone == links.ToneWait
			case "ready":
				return pr && l.Tone == links.ToneGood
			case "changes":
				return pr && l.Tone == links.ToneBad
			case "tickets":
				return l.Kind == links.KindJira && l.Tone != links.ToneDone
			}
			return false
		})
	case "due":
		d, ok := c.DueIn(now)
		return ok && ((arg == "over" && d < 0) || (arg == "week" && d >= 0 && d < 7))
	}
	return false
}

// boardCards is every card on the board, column by column.
func (u *ui) boardCards() []kanban.Card {
	var out []kanban.Card
	for _, c := range u.b.Columns {
		out = append(out, c.Cards...)
	}
	return out
}

func (u *ui) count(v string) int {
	if v == viewArchive {
		return len(u.doneCards()) + len(u.b.Archive)
	}
	if org, ok := strings.CutPrefix(v, "tk:"); ok {
		return len(u.tickets[org])
	}
	if org, ok := strings.CutPrefix(v, "ci:"); ok {
		return u.ciCount(org)
	}
	if org, ok := strings.CutPrefix(v, "dr:"); ok {
		return len(u.drawings(org))
	}
	if org, ok := strings.CutPrefix(v, "al:"); ok {
		return u.alertCount(org)
	}
	if org, ok := strings.CutPrefix(v, "srv:"); ok { // servers with a session
		n := 0
		if o := u.infraOrg(org); o != nil {
			for _, g := range o.Groups {
				for _, h := range g.Hosts {
					if h.Open {
						n++
					}
				}
			}
		}
		return n
	}
	// a board view's count: kept until the board or the day changes (the
	// sidebar asks for each of its rows every frame)
	now := time.Now()
	if k := fmt.Sprint(u.gen, u.cardCount(), now.Format(time.DateOnly)); k != u.countKey {
		u.countKey, u.counts = k, map[string]int{}
	}
	if n, ok := u.counts[v]; ok {
		return n
	}
	n, done := 0, u.b.Column("Done")
	for ci := range u.b.Columns {
		for j := range u.b.Columns[ci].Cards {
			if ci != done && inView(v, u.b.Columns[ci].Cards[j], now) {
				n++
			}
		}
	}
	u.counts[v] = n
	return n
}

type sideItem struct {
	view, label string
	ico         kit.IconPath // or a dot of color c
	c           color.NRGBA
}

var statusColor = map[string]color.NRGBA{
	kanban.StatusWorking: kit.CPurple, kanban.StatusWaiting: kit.CAccText, kanban.StatusBlocked: kit.COrange,
	kanban.StatusFailed: kit.CRed, kanban.StatusDone: kit.CGreen,
}

var statusName = map[string]string{
	kanban.StatusWorking: "Working", kanban.StatusWaiting: "Your turn", kanban.StatusBlocked: "Blocked",
	kanban.StatusFailed: "Failed", kanban.StatusDone: "Done",
}

// sections is the sidebar: fixed views, then the orgs and labels in use.
func (u *ui) sections() []struct {
	title string
	items []sideItem
} {
	type sec = struct {
		title string
		items []sideItem
	}
	cards := u.boardCards()
	var orgItems, labItems []sideItem
	var orgSeen, labSeen []string
	for _, c := range cards {
		if c.Org != "" && !slices.Contains(orgSeen, c.Org) {
			orgSeen = append(orgSeen, c.Org)
		}
		for _, l := range c.Labels {
			if !slices.Contains(labSeen, l) {
				labSeen = append(labSeen, l)
			}
		}
	}
	infraOrgs := make([]string, len(u.tree))
	for i, o := range u.tree {
		infraOrgs[i] = o.Name
	}
	for _, o := range u.orgList { // an organization with drawings shows for them
		if len(u.drawings(o)) > 0 {
			infraOrgs = append(infraOrgs, o)
		}
	}
	for _, o := range slices.Concat(slices.Collect(maps.Keys(u.tickets)), slices.Collect(maps.Keys(u.alerts)), slices.Collect(maps.Keys(u.ci)), infraOrgs) {
		if !slices.Contains(orgSeen, o) {
			orgSeen = append(orgSeen, o)
		}
	}
	slices.Sort(orgSeen)
	slices.Sort(labSeen)
	for _, o := range orgSeen {
		orgItems = append(orgItems, sideItem{"org:" + o, o, nil, kit.CText3})
		if _, ok := u.tickets[o]; ok {
			orgItems = append(orgItems, sideItem{"tk:" + o, "Tickets", kit.IcoLink, kit.CText3})
		}
		if _, ok := u.alerts[o]; ok {
			orgItems = append(orgItems, sideItem{"al:" + o, "Alerts", kit.IcoWarn, u.alertColor(o)})
		}
		if _, ok := u.ci[o]; ok {
			orgItems = append(orgItems, sideItem{"ci:" + o, "CI", kit.IcoCheck, u.ciTone(o)})
		}
		orgItems = append(orgItems, sideItem{"dr:" + o, "Drawings", kit.IcoShapes, kit.CText3})
		orgItems = append(orgItems, u.serverItems(o)...)
	}
	for _, l := range labSeen {
		labItems = append(labItems, sideItem{"lab:" + l, l, nil, labelColor(l)})
	}
	var st []sideItem
	for _, s := range []string{kanban.StatusWorking, kanban.StatusWaiting, kanban.StatusBlocked, kanban.StatusFailed} {
		st = append(st, sideItem{"st:" + s, statusName[s], nil, statusColor[s]})
	}
	byOrg := map[string][]sideItem{} // every card with a session but the Done ones, by org, board order: its status dot, or the terminal when open
	doneIDs := map[string]bool{}
	if d := u.b.Column("Done"); d >= 0 {
		for _, c := range u.b.Columns[d].Cards {
			doneIDs[c.ID] = true
		}
	}
	for _, c := range cards {
		if doneIDs[c.ID] && u.terms[c.ID] == nil && u.asks[c.ID] == nil && u.view != "sess:"+c.ID && u.view != "ws:"+c.ID { // finished: the archive has it
			continue
		}
		it := sideItem{"sess:" + c.ID, c.Key() + " " + c.Title, nil, cmp.Or(statusColor[c.Status], kit.CText3)}
		switch {
		case u.terms[c.ID] != nil:
			it.ico, it.c = kit.IcoTerminal, kit.CGreen
		case u.asks[c.ID] != nil:
			it.ico, it.c = kit.IcoWarn, kit.COrange
		case c.Session.ID == "":
			continue
		}
		byOrg[c.Org] = append(byOrg[c.Org], it)
	}
	var agents []sideItem
	for _, o := range append(orgSeen, "") { // orgs sorted, then the cards with none
		if len(byOrg[o]) == 0 {
			continue
		}
		if len(byOrg) > 1 { // one group needs no header
			agents = append(agents, sideItem{view: "hdr:" + o, label: cmp.Or(o, "No organization")})
		}
		agents = append(agents, byOrg[o]...)
	}
	out := []sec{
		{"Board", []sideItem{{"", "All cards", kit.IcoBoard, kit.CText2}}},
		{"Agent Sessions", agents},
		{"Sessions", st},
		{"Organizations", orgItems},
		{"Labels", labItems},
		{"Links", []sideItem{
			{"ln:review", "Waiting on review", kit.IcoClock, kit.CText2},
			{"ln:ready", "Ready to merge", kit.IcoCheck, kit.CGreen},
			{"ln:changes", "Changes requested · CI failed", kit.IcoWarn, kit.CRed},
			{"ln:tickets", "Open tickets", kit.IcoLink, kit.CText2},
		}},
		{"Due", []sideItem{{"due:over", "Overdue", kit.IcoWarn, kit.CRed}, {"due:week", "This week", kit.IcoClock, kit.COrange}}},
		{"Archive", []sideItem{{viewArchive, "Done archive", kit.IcoArchive, kit.CText2}}},
	}
	return slices.DeleteFunc(out, func(s sec) bool { return len(s.items) == 0 && s.title != "Organizations" }) // its header holds Settings
}

func (u *ui) viewTitle() string {
	if org, ok := strings.CutPrefix(u.view, "tk:"); ok {
		return org + " · my tickets"
	}
	if org, ok := strings.CutPrefix(u.view, "al:"); ok {
		return org + " · alerts"
	}
	if org, ok := strings.CutPrefix(u.view, "ci:"); ok {
		return org + " · CI"
	}
	if org, ok := strings.CutPrefix(u.view, "srv:"); ok {
		return org + " · servers"
	}
	if org, ok := strings.CutPrefix(u.view, "dr:"); ok {
		return org + " · drawings"
	}
	if p, ok := u.placeOf(u.view); ok {
		return u.b.Card(p.card).Key() + " · drawings"
	}
	if st := u.drawOpen(); st != nil {
		return st.org + " · " + drawName(st.path)
	}
	if id, ok := strings.CutPrefix(u.view, "term:"); ok {
		if c := u.b.Card(id); c != nil {
			return c.Key() + " · Claude Code"
		}
	}
	if id, ok := strings.CutPrefix(u.view, "sess:"); ok {
		if c := u.b.Card(id); c != nil {
			return c.Key() + " · agent session"
		}
	}
	if id, ok := strings.CutPrefix(u.view, "ws:"); ok {
		if c := u.b.Card(id); c != nil {
			return c.Key() + " · workspace"
		}
	}
	for _, s := range u.sections() {
		for _, it := range s.items {
			if it.view == u.view {
				if strings.HasPrefix(it.view, "lab:") {
					return "#" + it.label
				}
				return it.label
			}
		}
	}
	return "All cards"
}

// sentence is the view in words: "2 working · 4 blocked · 1 overdue".
func (u *ui) sentence() string {
	if u.view == viewArchive {
		return fmt.Sprintf("%d done · %d archived", len(u.doneCards()), len(u.b.Archive))
	}
	if org, ok := strings.CutPrefix(u.view, "tk:"); ok {
		return u.ticketsSentence(org)
	}
	if org, ok := strings.CutPrefix(u.view, "al:"); ok {
		return u.alertsSentence(org)
	}
	if org, ok := strings.CutPrefix(u.view, "ci:"); ok {
		return u.ciSentence(org)
	}
	if org, ok := strings.CutPrefix(u.view, "srv:"); ok {
		return u.serversSentence(org)
	}
	if strings.HasPrefix(u.view, "term:") {
		return "Its session in the full Claude Code TUI; Close, or /exit, gives it back to the board"
	}
	if strings.HasPrefix(u.view, "sess:") {
		return "The card's Claude session, full window; Esc goes back to the board"
	}
	if org, ok := strings.CutPrefix(u.view, "dr:"); ok {
		return plural(len(u.drawings(org)), "drawing", "drawings") + " · .excalidraw files in the organization's .desk folder"
	}
	if p, ok := u.placeOf(u.view); ok {
		return plural(len(u.drawingsIn(u.placeDir(p))), "drawing", "drawings") + " of the card · its Claudes read and draw them too"
	}
	if strings.HasPrefix(u.view, "draw:") {
		return "Draw with the toolbar or its keys; Esc steps back to the list"
	}
	if strings.HasPrefix(u.view, "ws:") {
		return "The card's projects and a Claude that reads them with you; Esc goes back to the session"
	}
	now := time.Now()
	var n, over int
	by := map[string]int{}
	for _, c := range u.boardCards() {
		if !inView(u.view, c, now) || u.isDone(c.ID) {
			continue
		}
		n++
		by[c.Status]++
		if d, ok := c.DueIn(now); ok && d < 0 {
			over++
		}
	}
	var parts []string
	for _, s := range []string{kanban.StatusWorking, kanban.StatusWaiting, kanban.StatusBlocked, kanban.StatusFailed} {
		if by[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", by[s], strings.ToLower(statusName[s])))
		}
	}
	if over > 0 {
		parts = append(parts, fmt.Sprintf("%d overdue", over))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%d cards", n)
	}
	return strings.Join(parts, " · ")
}

func (u *ui) sidebar(gtx layout.Context) layout.Dimensions {
	k := u.k
	gtx.Constraints = layout.Exact(image.Pt(gtx.Dp(232), gtx.Constraints.Max.Y))
	kit.Fill(gtx, kit.CPanel)
	var rows []layout.Widget
	for si, s := range u.sections() {
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			top := unit.Dp(14)
			if si == 0 {
				top = 4
			}
			title := k.Lbl(s.title, 11, kit.CText3, kit.Semi(kit.Sans))
			if s.title != "Organizations" {
				return layout.Inset{Top: top, Bottom: 4, Left: 8}.Layout(gtx, title)
			}
			return layout.Inset{Top: top - 4, Left: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(title),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d, ok := k.Button(gtx, "sideOrgReload", color.NRGBA{}, kit.CHover, kit.RCtl, layout.Inset{Top: 3, Bottom: 3, Left: 6, Right: 6}, k.Lbl("Reload", 11, kit.CAccText, kit.Medium(kit.Sans)))
						if ok {
							u.reloadConfig()
						}
						return d
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d, ok := k.Button(gtx, "sideOrgCfg", color.NRGBA{}, kit.CHover, kit.RCtl, layout.Inset{Top: 3, Bottom: 3, Left: 6, Right: 6}, k.Lbl("Settings", 11, kit.CAccText, kit.Medium(kit.Sans)))
						if ok {
							u.openConfig(u.configOrg())
						}
						return d
					}))
			})
		})
		for _, it := range s.items {
			rows = append(rows, func(gtx layout.Context) layout.Dimensions { return u.sideRow(gtx, it) })
		}
	}
	return layout.UniformInset(8).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return u.sideList.Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions { return rows[i](gtx) })
	})
}

func (u *ui) sideRow(gtx layout.Context, it sideItem) layout.Dimensions {
	k := u.k
	if strings.HasPrefix(it.view, "hdr:") { // an org's sessions start here: a label, not a view
		return layout.Inset{Top: 6, Bottom: 2, Left: 8}.Layout(gtx, k.LblFill(it.label, 11, kit.CText3, kit.Medium(kit.Sans)))
	}
	id, sess := strings.CutPrefix(it.view, "sess:")
	on := u.view == it.view || (sess && (u.view == "term:"+id || u.view == "ws:"+id)) // a session shows lit in its terminal and workspace too
	fill, fg := color.NRGBA{}, kit.CText2
	if on {
		fill, fg = kit.CAccSoft, kit.CText
	}
	n := u.count(it.view)
	in := layout.Inset{Top: 5, Bottom: 5, Left: 8, Right: 8}
	if strings.HasPrefix(it.view, "tk:") || strings.HasPrefix(it.view, "al:") || strings.HasPrefix(it.view, "ci:") || strings.HasPrefix(it.view, "dr:") { // under its organization
		in.Left = 24
	}
	row := serverIndent(it.view) > 0 && !strings.HasPrefix(it.view, "srv:") // a server row: no count, its own click
	if i := serverIndent(it.view); i > 0 {
		in.Left = unit.Dp(i)
	}
	d, ok := k.Button(gtx, "side"+it.view, fill, kit.CHover, kit.RCtl, in, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		mark := kit.Dot(it.c)
		if it.ico != nil {
			mark = kit.Icon(14, it.c, it.ico)
		}
		if org, ok := strings.CutPrefix(it.view, "org:"); ok && u.loading[org] > 0 {
			mark = kit.Spinner(10, kit.CAccText)
		}
		nc := kit.CText3
		if n == 0 {
			nc = kit.CIdle
		} else if strings.HasPrefix(it.view, "al:") || strings.HasPrefix(it.view, "ci:") { // alerts and CI failures count in their color
			nc = it.c
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Dp(16)
				return layout.W.Layout(gtx, mark)
			}),
			layout.Rigid(kit.HGap(8)),
			layout.Flexed(1, k.LblFill(it.label, 13, fg, kit.Medium(kit.Sans))),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if sess || row { // a session or a server, not a count
					return layout.Dimensions{}
				}
				return k.Pill(fmt.Sprint(n), nc, kit.Mix(kit.CPanelCard, kit.CPanel, .3))(gtx)
			}))
	})
	if org, srv := strings.CutPrefix(it.view, "srv:"); ok && srv {
		u.serversClicked(org, gtx.Now)
	}
	if ok && row {
		u.serverClick(it.view)
	} else if ok {
		u.view, u.sel = it.view, ""
		switch {
		case sess && u.terms[id] != nil:
			u.showTerm(id)
		case sess:
			u.openSession(id)
		}
		for i := range u.scroll {
			u.scroll[i] = 0
		}
	}
	return d
}
