package ui

import (
	"cmp"
	"fmt"
	"image"
	"image/color"
	"math"
	"slices"
	"strconv"
	"strings"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// The settings' pages: desk's own (General, Search folders, Phone, Keyboard
// shortcuts, Diagnostics), then each organization as a group in the nav
// with a part per concern. A page (cfgDialog) is a file: settings.toml's
// parts share cfgFolders, an organization's its desk.toml; cfgSet.sub says
// which part of it shows. The nav's search finds parts by their words.

// cfgPart is one part of a page in the nav.
type cfgPart struct {
	key, title, about, words string
}

// deskParts are settings.toml's parts (cfgFolders' page), in the nav's
// order around Keyboard shortcuts (cfgKeys, its own page).
var deskParts = []cfgPart{
	{"general", "General", "Who you are, the languages new cards start with, and the text size.", "who am i me languages card chat text size zoom"},
	{"folders", "Search folders", "The folders desk searches for organizations.", "folders depth only toml root organizations scan"},
	{"phone", "Phone", "A Telegram bot that brings cards that need you to your phone while desk isn't in front of you.", "phone telegram bot token pair chat always notify"},
	{"diag", "Diagnostics", "Where desk reports its own crashes.", "diagnostics new relic license crash telemetry"},
}

// orgParts are an organization's parts, in the nav's order.
var orgParts = []cfgPart{
	{"accounts", "Accounts", "Accounts its cards' links, tickets and alerts go through.", "accounts vault github jira linear bitbucket gitlab nagios model provider token"},
	{"workflow", "Ticket workflow", "How its tickets move, as its people use them: told to every card's Claude.", "ticket workflow status learn follow rules"},
	{"git", "Git & review", "How its cards use their git repositories, and the work time logged on tickets.", "git worktree branch review merge worklog time"},
	{"triggers", "Triggers", "What desk does by itself here: new tickets, failed CI, alerts, schedules, digests.", "triggers ticket ci failed alert schedule digest automation"},
	{"models", "Models", "Which of desk's Claudes run on the Claude subscription or on a model provider.", "models provider openrouter subscription tasks"},
	{"company", "Company", "The model each of desk's roles runs on, and the budgets that pause a session.", "company roles budget card day cost"},
	{"servers", "Servers", "Its servers file, the login message, and importing from Ásbrú.", "servers ssh asbru login message"},
	{"me", "About me", "Who you are in this organization, for its Claudes.", "about me who am i role"},
}

// phoneKind and diagKind are settings.toml's [phone] and [newrelic] as
// entries, drawn and checked as an account's fields are.
var (
	phoneKind = cfgKind{"phone", "Phone", []cfgField{
		{key: "token", label: "Bot token", secret: "."},
		{key: "always", label: "Also while desk has the focus", hint: "off: only while no desk window is in front", toggle: true},
	}}
	diagKind = cfgKind{"diag", "Diagnostics", []cfgField{
		{key: "license_key", label: "New Relic license key", hint: "empty: nothing is sent"},
		{key: "app_name", label: "App name", hint: "desk"},
	}}
)

// page is the part of d shown.
func (d *cfgDialog) page() string {
	if p := d.set.sub[d.org]; p != "" {
		return p
	}
	switch d.org {
	case cfgFolders:
		return "general"
	case cfgKeys:
		return ""
	}
	return "accounts"
}

// partOf is part key of page o.
func partOf(o, key string) cfgPart {
	ps := orgParts
	if o == cfgFolders {
		ps = deskParts
	}
	i := slices.IndexFunc(ps, func(p cfgPart) bool { return p.key == key })
	if i < 0 {
		return cfgPart{}
	}
	return ps[i]
}

// partOfKind is the part an entry of kind is edited on.
func partOfKind(kind string) string {
	if kind == "trigger" {
		return "triggers"
	}
	return "accounts"
}

// kinds is d's triggers (triggers true) or its accounts.
func (d *cfgDialog) kinds(triggers bool) []*cfgEntry {
	var out []*cfgEntry
	for _, e := range d.entries {
		if (e.kind == "trigger") == triggers {
			out = append(out, e)
		}
	}
	return out
}

// cfgShown is the page shown: its title, what it is, its body.
func (u *ui) cfgShown() (title, sub string, body layout.Widget) {
	d := u.cfg
	switch d.org {
	case cfgKeys:
		return "Keyboard shortcuts", "Every shortcut in desk and its infra workspace, by where it works.", u.cfgKeysPage
	case cfgFolders:
		p := partOf(d.org, d.page())
		body = u.cfgFolderPage
		switch p.key {
		case "phone":
			body = u.cfgPhone
		case "diag":
			body = u.cfgDiag
		}
		return p.title, p.about, body
	}
	p := partOf(d.org, d.page())
	bodies := map[string]layout.Widget{"accounts": u.cfgAccounts, "workflow": u.cfgOne(u.cfgWorkflow), "git": u.cfgOne(u.cfgGit),
		"triggers": u.cfgTriggers, "models": u.cfgOne(u.cfgModels), "company": u.cfgOne(u.cfgCompany), "servers": u.cfgOne(u.cfgServers), "me": u.cfgOne(u.cfgMe)}
	return d.org + " · " + p.title, strings.TrimSuffix(p.about, ".") + " · " + strings.Replace(d.file, u.home, "~", 1), bodies[p.key]
}

// hasWords says whether every word of q starts a word of hay, any case: a
// fuzzy match over these long word lists finds nearly everything.
func hasWords(q, hay string) bool {
	hs := strings.FieldsFunc(strings.ToLower(hay), func(r rune) bool { return r == ' ' || r == ',' || r == '.' || r == ':' || r == '&' })
	for _, w := range strings.Fields(strings.ToLower(q)) {
		if !slices.ContainsFunc(hs, func(h string) bool { return strings.HasPrefix(h, w) }) {
			return false
		}
	}
	return true
}

// cfgOne is a part alone on its page, scrolling.
func (u *ui) cfgOne(w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		d := u.cfg
		if d.broken {
			return layout.Inset{Top: 12}.Layout(gtx, u.k.Lbl("Fix the file by hand, then open the settings again.", 13, kit.CText3, kit.Sans))
		}
		return d.list.Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions {
			return layout.Inset{Bottom: 20}.Layout(gtx, w)
		})
	}
}

// cfgGo shows part sub of page o, after the frame.
func (u *ui) cfgGo(o, sub string) {
	d := u.cfg
	d.act = func() {
		s := d.set
		same := u.cfg.org == o && u.cfg.page() == sub
		u.cfgPage(o)
		if sub != "" {
			s.sub[o] = sub
		}
		if o != cfgFolders && o != cfgKeys {
			s.open[o] = true
		}
		if !same {
			u.cfg.list.ScrollTo(0)
		}
	}
}

// navRow is one of the nav's rows: page o's part sub ("" the page itself).
type navRow struct {
	o, sub, title string
	lead          layout.Widget
	indent        unit.Dp
	head          bool // a section's heading, not a page
}

// navRows is the nav as it is now: desk's pages, then each organization,
// open ones with their parts; with a search, the parts that match it.
func (u *ui) navRows() []navRow {
	s := u.cfg.set
	order := s.order(u.orgList)
	if q := strings.TrimSpace(s.q.Text()); q != "" {
		var rows []navRow
		match := func(o string, p cfgPart, title string) {
			if hasWords(q, title+" "+p.words+" "+p.about) {
				rows = append(rows, navRow{o: o, sub: p.key, title: title, indent: 10})
			}
		}
		for _, p := range deskParts {
			match(cfgFolders, p, p.title)
		}
		if hasWords(q, "keyboard shortcuts keys bindings") {
			rows = append(rows, navRow{o: cfgKeys, title: "Keyboard shortcuts", indent: 10})
		}
		for _, o := range order {
			for _, p := range orgParts {
				match(o, p, o+" › "+p.title)
			}
		}
		return rows
	}
	rows := []navRow{{title: "desk", head: true}}
	for _, p := range deskParts {
		if p.key == "diag" {
			rows = append(rows, navRow{o: cfgKeys, title: "Keyboard shortcuts", indent: 10})
		}
		rows = append(rows, navRow{o: cfgFolders, sub: p.key, title: p.title, indent: 10})
	}
	rows = append(rows, navRow{title: fmt.Sprintf("Organizations · %d", len(order)), head: true})
	for _, o := range order {
		in := unit.Dp(10)
		if s.parent[o] != "" {
			in = 24
		}
		rows = append(rows, navRow{o: o, title: o, lead: kit.Dot(orgDots[s.state[o]]), indent: in})
		if s.open[o] {
			for _, p := range orgParts {
				rows = append(rows, navRow{o: o, sub: p.key, title: p.title, indent: in + 18})
			}
		}
	}
	return rows
}

// navID is a row's button: the page's first rows keep the ids they had.
func navID(r navRow) string {
	if r.sub == "" || r.o == cfgFolders && r.sub == "general" {
		return "cfgOrg" + r.o
	}
	return "cfgSub" + r.o + "/" + r.sub
}

// cfgNav is the pages: a search, desk's pages, then the organizations (a
// nested one under its outer one) with a dot for how they read; an open
// one lists its parts.
func (u *ui) cfgNav(gtx layout.Context) layout.Dimensions {
	k, d, s := u.k, u.cfg, u.cfg.set
	wd, h := gtx.Dp(250), gtx.Constraints.Max.Y
	gtx.Constraints = layout.Exact(image.Pt(wd, h))
	// the left corners follow the dialog's; the right edge is a line
	kit.FillRRect(gtx.Ops, image.Rect(1, 1, wd, h-1), gtx.Dp(kit.RSheet)-1, kit.CCard)
	kit.FillRRect(gtx.Ops, image.Rect(wd/2, 1, wd, h-1), 0, kit.CCard)
	kit.FillRRect(gtx.Ops, image.Rect(wd-1, 0, wd, h), 0, kit.CLine)
	rows := u.navRows()
	row := func(r navRow) layout.Widget {
		if r.head {
			return func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 12, Bottom: 6, Left: 10}.Layout(gtx, k.Lbl(r.title, 11, kit.CText3, kit.Semi(kit.Sans)))
			}
		}
		return func(gtx layout.Context) layout.Dimensions {
			on := r.o == d.org && (r.sub == d.page() || r.sub == "" && r.o == cfgKeys)
			fg, bg := kit.CText2, color.NRGBA{}
			if on {
				fg, bg = kit.CText, kit.CAccSoft
			} else if r.sub == "" && r.o == d.org {
				fg = kit.CText
			}
			note, tone := "", kit.CText3
			if r.sub == "" || r.o == cfgFolders && r.sub == "general" {
				note, tone = s.note(r.o)
			}
			kids := []layout.FlexChild{}
			if r.lead != nil {
				kids = append(kids, layout.Rigid(r.lead), layout.Rigid(kit.HGap(8)))
			}
			kids = append(kids, layout.Rigid(k.Lbl(r.title, 13, fg, kit.Sans)), layout.Flexed(1, fill), layout.Rigid(k.Lbl(note, 11, tone, kit.Sans)))
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			dim, ok := k.Button(gtx, navID(r), bg, kit.CHover, kit.RCtl, layout.Inset{Top: 6, Bottom: 6, Left: r.indent, Right: 10}, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
			})
			switch {
			case !ok:
			case r.sub == "" && r.o == d.org && r.o != cfgKeys: // the open organization's row folds it
				d.act = func() { s.open[r.o] = !s.open[r.o] }
			default:
				sub := r.sub
				if sub == "" && r.o != cfgKeys {
					sub = s.sub[r.o]
				}
				u.cfgGo(r.o, sub)
			}
			return dim
		}
	}
	return layout.Inset{Top: 12, Bottom: 12, Left: 10, Right: 11}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Bottom: 6}.Layout(gtx, k.Field(&s.q, "Search settings", 0))
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				if len(rows) == 0 {
					return layout.Inset{Top: 10, Left: 10}.Layout(gtx, k.Lbl("Nothing matches.", 12, kit.CText3, kit.Sans))
				}
				return s.nav.Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
					return layout.Inset{Bottom: 1}.Layout(gtx, row(rows[i]))
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 8, Left: 10, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return k.Label(gtx, "Dots: green has accounts, orange its desk.toml doesn't read, grey none yet.", 11, kit.CText3, kit.Sans, 0)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 8, Left: 10, Right: 10}.Layout(gtx, k.Lbl("desk "+Version, 11, kit.CText3, kit.Mono))
			}))
	})
}

// cfgToggle is a switch with its label and what it means.
func (u *ui) cfgToggle(gtx layout.Context, id string, e *cfgEntry, key, label, hint string) layout.Dimensions {
	k, d := u.k, u.cfg
	kids := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		dim, ok := k.Switch(gtx, id, e.on[key])
		if ok {
			d.act = func() { e.on[key] = !e.on[key] }
		}
		return dim
	}), layout.Rigid(kit.HGap(10))}
	text := []layout.FlexChild{layout.Rigid(k.Lbl(label, 13, kit.CText, kit.Sans))}
	if hint != "" {
		text = append(text, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, hint, 11, kit.CText3, kit.Sans, 0) }))
	}
	kids = append(kids, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, text...)
	}))
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
}

// newPart is an entry for a page's own switches and fields, as read.
func newPart(kind string) *cfgEntry {
	return &cfgEntry{kind: kind, eds: map[string]*widget.Editor{}, src: map[string]int{}, on: map[string]bool{}}
}

func (e *cfgEntry) kept() *cfgEntry {
	e.old, e.base = true, e.snap()
	return e
}

// loadGit is an organization's [git] and [company] budgets as entries.
func loadGit(raw map[string]any) (git, comp *cfgEntry) {
	git, comp = newPart("git"), newPart("company")
	if g, ok := raw["git"].(map[string]any); ok {
		git.on["worktrees"], _ = g["worktrees"].(bool)
	}
	if c, ok := raw["company"].(map[string]any); ok {
		for _, k := range []string{"card_budget", "day_budget"} {
			if v, ok := c[k]; ok {
				comp.ed(k).SetText(fmt.Sprint(v))
			}
		}
	}
	return git.kept(), comp.kept()
}

// saveGit writes [git] and the budgets into out, keeping their other keys;
// a budget that isn't a number is refused.
func (d *cfgDialog) saveGit(out map[string]any) bool {
	g, _ := out["git"].(map[string]any)
	if g == nil {
		g = map[string]any{}
	}
	delete(g, "worktrees")
	if d.git.on["worktrees"] {
		g["worktrees"] = true
	}
	if len(g) == 0 {
		delete(out, "git")
	} else {
		out["git"] = g
	}
	c, _ := out["company"].(map[string]any)
	if c == nil {
		c = map[string]any{}
	}
	for _, k := range []string{"card_budget", "day_budget"} {
		delete(c, k)
		if t := d.comp.text(k); t != "" {
			v, err := strconv.ParseFloat(strings.TrimPrefix(t, "$"), 64)
			if err != nil || !(v >= 0) || math.IsInf(v, 0) { // NaN too
				d.err, d.errAt = "A budget is dollars, like 5 or 2.50", "company"
				return false
			}
			c[k] = v
		}
	}
	if len(c) == 0 {
		delete(out, "company")
	} else {
		out["company"] = c
	}
	return true
}

// cfgGit is the Git & review part: worktrees, and work time on each
// account's tickets.
func (u *ui) cfgGit(gtx layout.Context) layout.Dimensions {
	k, d := u.k, u.cfg
	kids := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.cfgToggle(gtx, "cfgWorktrees", d.git, "worktrees", "Each card on its own worktree",
				"A new card in a git repository gets a branch desk-n and a worktree <repo>/.worktrees/desk-n off origin's default branch, before its first turn; its Review merges, pushes or opens a request.")
		}),
		layout.Rigid(kit.VGap(22)), layout.Rigid(k.Caption("Work time on tickets")), layout.Rigid(kit.VGap(4)),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return k.Label(gtx, "When a card goes to Done, the time its Claude worked goes on its ticket: a worklog in Jira, a comment in Linear. Per account:", 12, kit.CText3, kit.Sans, 0)
		}),
		layout.Rigid(kit.VGap(10)),
	}
	n := 0
	for _, e := range d.entries {
		if e.kind != "jira" && e.kind != "linear" {
			continue
		}
		n++
		_, kind := kindOf(e.kind)
		name := kind.title + " · " + cmp.Or(e.text("name"), e.text("url"), e.text("workspace"))
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return u.cfgToggle(gtx, fmt.Sprintf("cfg%p", e)+"worklog", e, "worklog", name, "")
			})
		}))
	}
	if n == 0 {
		kids = append(kids, layout.Rigid(k.Lbl("No Jira or Linear account: add one in Accounts.", 12, kit.CText3, kit.Sans)))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

// cfgCompany is the Company part: budgets, then the roles' models.
func (u *ui) cfgCompany(gtx layout.Context) layout.Dimensions {
	k, d := u.k, u.cfg
	field := func(key, label, hint string) layout.FlexChild {
		return layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, layout.Rigid(k.Caption(label)), layout.Rigid(kit.VGap(5)),
				layout.Rigid(k.Field(d.comp.ed(key), hint, 0)))
		})
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return k.Label(gtx, "A card's session pauses at the end of a turn once it has spent its budget since your last message, or the organization its budget for the day; your next message goes on. Empty: no limit.", 12, kit.CText3, kit.Sans, 0)
		}),
		layout.Rigid(kit.VGap(10)),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx, field("card_budget", "Per card, dollars", "e.g. 5"), layout.Rigid(kit.HGap(14)), field("day_budget", "Per day, dollars", "e.g. 40"))
		}),
		layout.Rigid(u.cfgRoles))
}

// cfgTriggers is the Triggers part: a card per trigger, then + Trigger.
func (u *ui) cfgTriggers(gtx layout.Context) layout.Dimensions {
	k, d := u.k, u.cfg
	if d.broken {
		return u.cfgOne(fill)(gtx)
	}
	es := d.kinds(true)
	n := len(es)
	return d.list.Layout(gtx, n+1, func(gtx layout.Context, i int) layout.Dimensions {
		if i < n {
			return layout.Inset{Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return u.cfgEntry(gtx, es[i]) })
		}
		kids := []layout.FlexChild{}
		if n == 0 {
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return k.Label(gtx, "No triggers yet. A trigger makes a card for a new ticket, tells a card's Claude its CI failed, opens an investigation for an alert, or makes a card or sends the digest at a time of day. It runs on desk's checks, every 3 minutes, and its first run only notes what is already there.", 13, kit.CText3, kit.Sans, 0)
			}), layout.Rigid(kit.VGap(12)))
		}
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			dim, ok := k.KBtn(gtx, "cfgAddtrigger", kit.Secondary, "+ Trigger", "")
			if ok {
				d.act = func() {
					e := newEntry("trigger", map[string]any{"on": "ticket"})
					d.entries = append(d.entries, e)
					d.open = e
					d.list.ScrollTo(len(d.kinds(true)) - 1)
				}
			}
			return dim
		}))
		return layout.Inset{Top: 8, Bottom: 20}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
		})
	})
}

// loadPhone is settings.toml's [phone] and [newrelic] as entries.
func loadPhone() (phone, diag *cfgEntry) {
	p, nr := orgs.PhoneSettings(), orgs.Telemetry()
	raw := map[string]any{"token": p.Token, "token_env": p.TokenEnv, "token_cmd": p.TokenCmd, "always": p.Always, "chat_id": p.Chat}
	phone = newEntry(phoneKind.key, raw)
	diag = newEntry(diagKind.key, map[string]any{"license_key": nr.LicenseKey, "app_name": nr.AppName})
	diag.ed("license_key").Mask = '•'
	return phone.kept(), diag.kept()
}

// savePhone writes [phone] and [newrelic] when they changed.
func (d *cfgDialog) savePhone() error {
	if e := d.phone; e != nil && e.edited() {
		p := orgs.Phone{Always: e.on["always"]}
		if !e.on["unpair"] { // as it is now: a /start may have paired one since the page opened
			p.Chat = orgs.PhoneSettings().Chat
		}
		switch e.src["."] {
		case srcEnv:
			p.TokenEnv = e.text("..token_env")
		case srcCmd:
			p.TokenCmd = e.text("..token_cmd")
		case srcFile:
			p.Token = e.text("..token")
		}
		if err := orgs.SavePhone(p); err != nil {
			return err
		}
	}
	if e := d.diag; e != nil && e.edited() {
		return orgs.SaveTelemetry(orgs.NewRelic{LicenseKey: e.text("license_key"), AppName: e.text("app_name")})
	}
	return nil
}

// cfgPhone is the Phone part: whether a chat is paired, the bot's token,
// when it sends.
func (u *ui) cfgPhone(gtx layout.Context) layout.Dimensions {
	k, d := u.k, u.cfg
	e := d.phone
	chat, _ := e.raw["chat_id"].(int64)
	state, tone := "Not paired: send /start to your bot within 10 minutes of starting desk.", kit.CText3
	if chat != 0 && !e.on["unpair"] {
		state, tone = fmt.Sprintf("Paired with Telegram chat %d", chat), kit.CGreen
	} else if e.on["unpair"] {
		state, tone = "Unpaired on Save: then send /start within 10 minutes to pair a chat.", kit.COrange
	}
	head := func(gtx layout.Context) layout.Dimensions {
		kids := []layout.FlexChild{layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, state, 13, tone, kit.Sans, 0) })}
		if chat != 0 {
			label := "Unpair"
			if e.on["unpair"] {
				label = "Keep paired"
			}
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				dim, ok := k.KBtn(gtx, "cfgUnpair", kit.Ghost, label, "")
				if ok {
					d.act = func() { e.on["unpair"] = !e.on["unpair"] }
				}
				return dim
			}))
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
	}
	return d.list.Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(head), layout.Rigid(kit.VGap(16)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.cfgFields(gtx, "cfgPhone", e) }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return k.Label(gtx, "Make the bot with @BotFather in Telegram. Permission requests come with Allow and Deny, questions with their options, and a reply to a card's message goes to its Claude. A new token counts once desk starts again.", 12, kit.CText3, kit.Sans, 0)
			}))
	})
}

// cfgDiag is the Diagnostics part: where desk reports its crashes.
func (u *ui) cfgDiag(gtx layout.Context) layout.Dimensions {
	k, d := u.k, u.cfg
	return d.list.Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.cfgFields(gtx, "cfgDiag", d.diag) }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return k.Label(gtx, "With a license key, desk reports its own crashes to that New Relic account: where it crashed and which version. Nothing else, and nothing without a key. Counts once desk starts again.", 12, kit.CText3, kit.Sans, 0)
			}))
	})
}
