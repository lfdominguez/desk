package ui

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/fzf"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/keys"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// sheet is the card dialog over a scrim: a new card (id "") or an existing
// one. Every card is created here, since its session needs a place to start:
// an organization, and optionally a folder inside it.
type sheet struct {
	in                        *pane  // the window it was opened in: drawn and closed there
	id                        string // "" a new card
	col, prio                 int    // where it goes and its priority on save
	title, notes, labels, due widget.Editor
	org, dir, links           widget.Editor
	lang, chatLang            widget.Editor  // a new card's languages (Card.Lang, ChatLang), the settings' to start
	opened                    bool           // focus the title on the first frame
	dirsOf                    string         // the folder dirs lists
	dirs                      []string       // its subfolders, for the browser
	dirKey, dirAt, dirQ       string         // dirQuery's last answer, for org + "\x00" + the field's text
	list                      layout.List    // the body's scroll
	focus                     *widget.Editor // the field focused last frame
	act                       func()         // a footer button's action, run after the frame's dialog
	optStop                   func()         // stops Prompt Optimization while it runs, else nil
	optNote                   string         // what it last said
	optErr                    string         // why the last run failed, whole (the footer has room for a line)
	optStep                   string         // what it is doing now: "Reading ABC-123", "Thinking", "Writing"
	optStage                  int            // 0 the tickets, 1 the org, 2 writing
	optTickets                bool           // the request links tickets desk reads first: stage 0 shows
	optLive                   claude.Draft   // the title and prompt as they stream in
	optStart                  time.Time      // when it began, for the elapsed time
	optimized                 bool           // a draft was applied: a new card can be created now
	agent                     string         // the agent its session runs as, "" none
	agentsOf                  string         // the organization agents were read for
	agentsRead                bool
	agents                    []claude.Agent        // that organization's
	pasteText                 bool                  // Ctrl+V found text, not an image: the editor pastes it
	ticket                    bool                  // a new card: its Claude creates its ticket first
	preview                   bool                  // the notes rendered as Markdown, not the editor
	trackerOf, tracker        string                // the organization links.Tracker was read for, and what it said
	plain                     map[string]bool       // the request's words made plain text: not tokens
	toks                      map[string][]smartTok // what tokens worked out, by text and what else it read
	more                      bool                  // a new card's other fields are shown
	orgRow                    bool                  // its organization was asked for: the field stays
	mineTitle, mineNotes      string                // what the user wrote before Claude's suggestions
	accepted                  bool                  // the suggestions were accepted: no more "suggested" marks
	editNotes                 bool                  // the drawer's notes are being edited, not read
}

// asking is a new card before Optimize: one field, what to do, which the
// draft turns into its title and notes.
func (s *sheet) asking() bool { return s.id == "" && !s.optimized }

// sheetEditors are the single-line fields, where enter saves.
func (s *sheet) singles() []*widget.Editor {
	return []*widget.Editor{&s.title, &s.labels, &s.due, &s.org, &s.dir, &s.lang, &s.chatLang}
}

func (u *ui) newSheet(id string, col int) *sheet {
	u.orgList = orgs.List(u.root) // folders made in ~/Work since start show up
	s := &sheet{in: u.pane, id: id, col: col, opened: true, list: layout.List{Axis: layout.Vertical}, plain: map[string]bool{}}
	for _, e := range s.singles() {
		e.SingleLine, e.Submit = true, true
	}
	return s
}

// newCard opens the dialog for a card in column col, its organization the
// view's, else the last one used.
func (u *ui) newCard(col int) {
	s := u.newSheet("", col)
	org := u.lastOrg
	if o, ok := strings.CutPrefix(u.view, "org:"); ok {
		org = o
	} else if o, ok := strings.CutPrefix(u.view, "tk:"); ok {
		org = o
	}
	s.org.SetText(org)
	if l, ok := strings.CutPrefix(u.view, "lab:"); ok {
		s.labels.SetText(l)
	}
	s.notes.SetText(u.draft) // what was left when it last closed
	card, chat := orgs.Langs()
	s.lang.SetText(card)
	s.chatLang.SetText(chat)
	s.optNote = improveNote
	u.edit = s
	u.w.Invalidate()
}

func (u *ui) open(id string) {
	c := u.b.Card(id)
	if c == nil || !c.Archived.IsZero() {
		return
	}
	col, _ := u.b.Find(id)
	s := u.newSheet(id, col)
	s.prio = c.Priority
	s.title.SetText(c.Title)
	s.notes.SetText(c.Notes)
	s.labels.SetText(strings.Join(c.Labels, ", "))
	s.due.SetText(c.Due)
	s.org.SetText(c.Org)
	s.dir.SetText(c.Dir)
	s.agent = c.Agent
	var ls []string
	for _, l := range c.Links {
		ls = append(ls, l.URL)
	}
	s.links.SetText(strings.Join(ls, "\n"))
	u.edit = s
	u.w.Invalidate()
}

func (u *ui) closeSheet() {
	if u.edit != nil && u.edit.optStop != nil { // Improve is writing the fields: closing now would drop it halfway
		u.showToast("Improve with Claude is running: Stop it first", true)
		return
	}
	if s := u.edit; s != nil && s.id == "" && !s.optimized { // a new card's request is kept for the next one
		u.draft = strings.TrimSpace(s.notes.Text())
	}
	u.edit, u.blur = nil, true
}

// smartApply fills a new card's fields from what its request says (@org,
// #label, !priority, a due day, ticket keys and links), where they are
// still unset, and its title from the request's first line.
func (u *ui) smartApply(s *sheet) {
	ts := smartParse(s.notes.Text(), u.orgList, time.Now(), s.plain)
	if o := smartVal(ts, "org"); o != "" && o != strings.TrimSpace(s.org.Text()) {
		s.org.SetText(o)
		s.dir.SetText("")
	}
	if ls := kanban.ParseLabels(s.labels.Text()); len(smartVals(ts, "label")) > 0 {
		for _, l := range smartVals(ts, "label") {
			if !slices.Contains(ls, l) {
				ls = append(ls, l)
			}
		}
		s.labels.SetText(strings.Join(ls, ", "))
	}
	if p := slices.Index(prioNames, smartVal(ts, "prio")); p > 0 && s.prio == 0 {
		s.prio = p
	}
	if d := smartVal(ts, "due"); d != "" && strings.TrimSpace(s.due.Text()) == "" {
		s.due.SetText(d)
	}
	cfg, have := u.orgConfig(strings.TrimSpace(s.org.Text())), strings.TrimSpace(s.links.Text())
	for _, l := range smartVals(ts, "link") {
		if _, ok := links.Parse(l, cfg); ok && !strings.Contains(have, l) {
			have = strings.TrimSpace(have + "\n" + l)
		}
	}
	s.links.SetText(have)
	if strings.TrimSpace(s.title.Text()) == "" {
		s.title.SetText(smartTitle(s.notes.Text(), ts))
	}
}

// commit creates the card, or saves it. A new card's request fills its
// fields (smartApply) unless Claude already did (Improve).
func (u *ui) commit() {
	s := u.edit
	if s.optStop != nil {
		return // Improve is running (a first run or Ask again): what it writes would land on a card already made
	}
	if s.id == "" && !s.optimized {
		u.smartApply(s)
	}
	title := strings.TrimSpace(s.title.Text())
	if title == "" {
		u.showToast("A card needs a title", true)
		return
	}
	due := strings.TrimSpace(s.due.Text())
	if _, err := time.Parse(kanban.DateLayout, due); due != "" && err != nil {
		u.showToast("Due date: use YYYY-MM-DD, e.g. "+time.Now().Format(kanban.DateLayout), true)
		return
	}
	org := strings.TrimSpace(s.org.Text())
	if !slices.Contains(u.orgList, org) {
		u.showToast("Pick the organization the session starts in (a folder of "+u.root+")", true)
		return
	}
	dir := strings.Trim(strings.TrimSpace(s.dir.Text()), "/")
	if fi, err := os.Stat(orgs.Dir(u.root, org, dir)); err != nil || !fi.IsDir() {
		u.showToast("Folder: no "+dir+" inside "+org, true)
		return
	}
	cfg := u.orgConfig(org)
	old := u.b.Card(s.id) // nil for a new card
	var ls []kanban.Link
	for _, raw := range strings.FieldsFunc(s.links.Text(), func(r rune) bool { return r == '\n' || r == ',' || r == ' ' }) {
		l, ok := links.Parse(raw, cfg)
		if !ok {
			u.showToast("Not a link: "+raw+" (a URL, or a Jira key of a project in desk.toml)", true)
			return
		}
		if old != nil {
			if i := slices.IndexFunc(old.Links, func(x kanban.Link) bool { return x.URL == l.URL }); i >= 0 {
				l = old.Links[i] // keep what its provider last said
			}
		}
		ls = append(ls, l)
	}
	c := old
	if s.id == "" {
		n := u.b.Add(s.col, kanban.Card{})
		c = u.b.Card(n.ID)
	} else if c == nil {
		u.closeSheet()
		return
	}
	c.Title, c.Notes, c.Labels, c.Priority, c.Due = title, strings.TrimSpace(s.notes.Text()), kanban.ParseLabels(s.labels.Text()), s.prio, due
	u.sheetAgents(s)
	if !slices.ContainsFunc(s.agents, func(a claude.Agent) bool { return a.Name == s.agent }) {
		s.agent = "" // the organization has no such agent
	}
	c.Org, c.Dir, c.Links, c.Agent, c.Updated = org, dir, ls, s.agent, time.Now()
	if s.id == "" {
		c.Ticket = s.ticket && s.tracker != "" && ticketOf(c) == nil
		c.Lang, c.ChatLang = strings.TrimSpace(s.lang.Text()), strings.TrimSpace(s.chatLang.Text())
	}
	id := c.ID
	go u.checkLinks([]string{id})
	if ci, _ := u.b.Find(id); ci != s.col { // same column keeps its place
		u.b.MoveBefore(id, s.col, "")
	}
	u.lastOrg = org
	u.closeSheet()
	if s.id == "" {
		u.draft = "" // made: nothing to keep
	}
	u.save()
	if s.id == "" { // straight to its session, already at work: its title, notes and links go with the first message
		u.openPanel(id)
		u.send(id, startWork)
	}
}

// remove deletes the dialog's card; the toast's Undo puts it back where it was.
func (u *ui) remove() {
	id := u.edit.id
	col, i := u.b.Find(id)
	c := u.b.Card(id)
	if c == nil {
		u.closeSheet()
		return
	}
	card := *c
	u.b.Delete(id)
	u.sel = ""
	u.edit, u.blur = nil, true // not closeSheet: there is nothing left to save
	u.save()
	u.showUndo("Deleted "+card.Key(), func() {
		cs := u.b.Columns[col].Cards
		u.b.Columns[col].Cards = slices.Insert(cs, min(i, len(cs)), card)
		u.save()
	})
}

func (u *ui) sheet(gtx layout.Context) {
	s := u.edit
	if s == nil || s.in != u.pane { // another window's
		return
	}
	if s.optStop == nil { // while Claude works the request stays as sent
		u.pasteImage(gtx, &s.notes, &s.pasteText, func() bool { return u.edit == s })
		kit.Newline(gtx, &s.notes)
	}
	// the dialog's own shortcuts, over its fields: Ctrl+Enter creates or
	// saves, Ctrl+J asks Claude to improve a new card, Ctrl+Backspace deletes
	fs := keys.Filters(nil, "sheet.commit")
	if s.id == "" {
		fs = append(fs, keys.Filters(nil, "sheet.improve")...)
	} else {
		fs = append(fs, keys.Filters(nil, "sheet.delete")...)
	}
	for {
		ev, ok := gtx.Event(fs...)
		if !ok {
			break
		}
		e, ok := ev.(key.Event)
		if !ok {
			continue
		}
		switch {
		case keys.Is("sheet.improve", e):
			u.optimizeCard()
		case keys.Is("sheet.delete", e):
			u.remove()
		case keys.Is("sheet.commit", e):
			u.commit()
		}
		if u.edit != s {
			return
		}
	}
	for _, e := range s.singles() {
		for {
			ev, ok := e.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				if m := u.orgMatches(s.org.Text()); e == &s.org && !slices.Contains(u.orgList, s.org.Text()) && len(m) > 0 {
					s.org.SetText(m[0]) // enter in a filter takes its first match
					s.org.SetCaret(len(m[0]), len(m[0]))
					s.dir.SetText("")
					continue
				}
				if e == &s.dir {
					if dir, q, m := u.dirChoices(s, strings.TrimSpace(s.org.Text())); q != "" && len(m) > 0 {
						t := path.Join(dir, m[0]) // enter in the folder filter takes its first match too
						s.dir.SetText(t)
						s.dir.SetCaret(len(t), len(t))
						continue
					}
				}
				u.commit()
				if u.edit == nil {
					return
				}
			}
		}
	}
	u.focusNext(gtx)
	if s.opened {
		tag := &s.title
		if s.asking() {
			tag = &s.notes
		}
		gtx.Execute(key.FocusCmd{Tag: tag})
		s.opened = false
	}

	if s.id == "" {
		k := u.k
		k.Modal(gtx, s, 64, 680, nil, func(gtx layout.Context) layout.Dimensions {
			return u.sheetLayout(gtx, u.newCardRows(), u.sheetFooter)
		})
	} else {
		u.drawer(gtx, s)
	}
	if act := s.act; act != nil { // a footer button, now that nothing more draws from the dialog
		s.act = nil // once: a refused Create keeps the dialog, not the click
		act()
	}
}

// sheetLayout is a dialog's body rows, scrolling when the window is too
// short for them, over its footer, which always shows.
func (u *ui) sheetLayout(gtx layout.Context, rows []sheetRow, footer layout.Widget) layout.Dimensions {
	s := u.edit
	m := op.Record(gtx.Ops)
	fgtx := gtx
	fgtx.Constraints.Min.Y = 0
	fd := footer(fgtx)
	foot := m.Stop()
	bgtx := gtx
	bgtx.Constraints.Min.Y, bgtx.Constraints.Max.Y = 0, max(0, gtx.Constraints.Max.Y-fd.Size.Y)
	for i, r := range rows { // a field just focused (Tab, a click) whose section is out of view: bring it in
		for _, e := range r.eds {
			if gtx.Focused(e) && e != s.focus {
				s.focus = e
				p := s.list.Position
				out := i < p.First || i >= p.First+p.Count || (i == p.First && p.Offset > 0) || (i == p.First+p.Count-1 && p.OffsetLast < 0)
				if p.Count > 0 && out { // Count 0: nothing laid out yet, the first frame

					s.list.ScrollTo(i)
				}
			}
		}
	}
	bd := s.list.Layout(bgtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
		if s.optStop != nil && i > 0 { // Improve is running: the fields take no input, so what it was sent stays what's there (the header's × still closes)
			gtx = gtx.Disabled()
		}
		return rows[i].w(gtx)
	})
	st := op.Offset(image.Pt(0, bd.Size.Y)).Push(gtx.Ops)
	foot.Add(gtx.Ops)
	st.Pop()
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, bd.Size.Y+fd.Size.Y)}
}

// pinned is sheetLayout for a box of exact height (the drawer): the
// footer at its foot however short the body.
func (u *ui) pinned(gtx layout.Context, rows []sheetRow, footer layout.Widget) layout.Dimensions {
	m := op.Record(gtx.Ops)
	fgtx := gtx
	fgtx.Constraints.Min.Y = 0
	fd := footer(fgtx)
	foot := m.Stop()
	bgtx := gtx
	bgtx.Constraints.Min.Y, bgtx.Constraints.Max.Y = 0, max(0, gtx.Constraints.Max.Y-fd.Size.Y)
	u.sheetLayout(bgtx, rows, func(layout.Context) layout.Dimensions { return layout.Dimensions{} })
	st := op.Offset(image.Pt(0, gtx.Constraints.Max.Y-fd.Size.Y)).Push(gtx.Ops)
	foot.Add(gtx.Ops)
	st.Pop()
	return layout.Dimensions{Size: gtx.Constraints.Max}
}

// choice is a row of toggle buttons, wrapped when narrow; the one at cur
// is lit in its color.
func (u *ui) choice(gtx layout.Context, id string, names []string, cur int, tint func(int) color.NRGBA, pick func(int)) layout.Dimensions {
	k := u.k
	var kids []layout.Widget
	for i, n := range names {
		kids = append(kids, func(gtx layout.Context) layout.Dimensions {
			fg, bg, c := kit.CText2, kit.CPanelCard, tint(i)
			switch {
			case i == cur && c.A == 0: // no color of its own (priority None): lit plain
				fg, bg = kit.CText, kit.CHover
			case i == cur:
				fg, bg = c, kit.Mix(c, kit.CPanel, .8)
			}
			d, ok := k.Button(gtx, fmt.Sprint(id, i), bg, kit.CHover, kit.RCtl, layout.Inset{Top: 5, Bottom: 5, Left: 10, Right: 10}, func(gtx layout.Context) layout.Dimensions {
				kids := []layout.FlexChild{layout.Rigid(k.Lbl(n, 12, fg, kit.Medium(kit.Sans)))}
				if c.A != 0 {
					kids = append([]layout.FlexChild{layout.Rigid(kit.Dot(c)), layout.Rigid(kit.HGap(7))}, kids...)
				}
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
			})
			if ok {
				pick(i)
			}
			return d
		})
	}
	return kit.Wrap(gtx, 4, kids)
}

// dirChoices is the folder the picker is in, the filter typed, and the
// subfolders it lists: the filter's matches, best first; none once a folder
// is picked, until a "/" after it asks for them.
func (u *ui) dirChoices(s *sheet, org string) (dir, q string, dirs []string) {
	dir, q = u.dirQuery(s, org)
	if at := orgs.Dir(u.root, org, dir); s.dirsOf != at { // read a folder once, not every frame
		s.dirsOf, s.dirs = at, nil
		ents, _ := os.ReadDir(at)
		for _, e := range ents {
			nest := slices.ContainsFunc(u.orgList, func(o string) bool { return orgs.Dir(u.root, o, "") == filepath.Join(at, e.Name()) })
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && !nest { // a nested org is picked as itself
				s.dirs = append(s.dirs, e.Name())
			}
		}
	}
	if dir != "" && q == "" && !strings.HasSuffix(strings.TrimSpace(s.dir.Text()), "/") {
		return dir, q, nil
	}
	return dir, q, fzf.Sort(q, s.dirs, func(d string) string { return d })
}

// dirQuery splits the folder field: a folder inside org that exists is
// itself with no query; otherwise its last part is a filter over its
// parent's subfolders ("ca" finds car_platform, "web/ap" api inside web).
// It stats the disk only when the field or the org changed, not every frame.
func (u *ui) dirQuery(s *sheet, org string) (dir, q string) {
	text := s.dir.Text()
	if key := org + "\x00" + text; key != s.dirKey {
		s.dirKey, s.dirAt, s.dirQ = key, strings.Trim(strings.TrimSpace(text), "/"), ""
		if fi, err := os.Stat(orgs.Dir(u.root, org, s.dirAt)); s.dirAt != "" && (err != nil || !fi.IsDir()) {
			if s.dirAt, s.dirQ = path.Dir(s.dirAt), path.Base(s.dirAt); s.dirAt == "." {
				s.dirAt = ""
			}
		}
	}
	return s.dirAt, s.dirQ
}

// orgMatches is the organizations matching search q (fzf's), best first.
func (u *ui) orgMatches(q string) []string {
	return fzf.Sort(q, u.orgList, func(o string) string { return o })
}

// placePicker is where the session starts, picked by clicks: the
// organizations matching the field until one is chosen, then that folder's
// subfolders to step into (and up out of).
func (u *ui) placePicker(gtx layout.Context, s *sheet) layout.Dimensions {
	k := u.k
	chip := func(id, label string, fg color.NRGBA, f func()) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			d, ok := k.Button(gtx, id, kit.CPanelCard, kit.CHover, kit.RCtl, layout.Inset{Top: 3, Bottom: 3, Left: 8, Right: 8}, k.Lbl(label, 12, fg, kit.Medium(kit.Sans)))
			if ok {
				f()
			}
			return d
		}
	}
	org := strings.TrimSpace(s.org.Text())
	if !slices.Contains(u.orgList, org) {
		var ws []layout.Widget
		for n, o := range u.orgMatches(org) {
			fg := kit.CText2
			if n == 0 && org != "" {
				fg = kit.CAccText // what enter takes
			}
			ws = append(ws, chip("org"+o, o, fg, func() {
				s.org.SetText(o)
				s.dir.SetText("")
			}))
		}
		if len(ws) == 0 {
			return layout.Inset{Top: 6}.Layout(gtx, k.Lbl("No folder like that in "+u.root, 11, kit.CRed, kit.Sans))
		}
		return layout.Inset{Top: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return kit.Wrap(gtx, 4, ws) })
	}
	dir, q, dirs := u.dirChoices(s, org)
	var ws []layout.Widget
	if dir != "" {
		ws = append(ws, chip("dirUp", "↑ up", kit.CAccText, func() {
			if d := path.Dir(dir); d == "." {
				s.dir.SetText("")
			} else {
				s.dir.SetText(d)
			}
		}))
	}
	for n, d := range dirs {
		if n == 40 { // ponytail: a long folder is typed into the field rather than clicked
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 3}.Layout(gtx, k.Lbl(fmt.Sprintf("+%d more: type the name", len(dirs)-40), 11, kit.CText3, kit.Sans))
			})
			break
		}
		fg := kit.CText2
		if n == 0 && q != "" {
			fg = kit.CAccText // what enter takes
		}
		ws = append(ws, chip("dir"+d, d+"/", fg, func() { s.dir.SetText(path.Join(dir, d)) }))
	}
	if q != "" && len(dirs) == 0 {
		ws = append(ws, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 3}.Layout(gtx, k.Lbl("No folder like "+q+" here", 11, kit.CRed, kit.Sans))
		})
	}
	where := strings.Replace(s.dirsOf, u.home, "~", 1)
	return layout.Inset{Top: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(k.Lbl("Starts in", 11, kit.CText3, kit.Sans)),
					layout.Rigid(kit.HGap(6)),
					layout.Rigid(k.Lbl(where, 12, kit.CGreen, kit.Mono)),
					layout.Rigid(kit.HGap(8)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d, ok := k.Button(gtx, "orgChange", color.NRGBA{}, kit.CHover, kit.RCtl, layout.Inset{Top: 2, Bottom: 2, Left: 6, Right: 6}, k.Lbl("change organization", 11, kit.CAccText, kit.Medium(kit.Sans)))
						if ok {
							s.org.SetText("")
							s.dir.SetText("")
						}
						return d
					}))
			}),
			layout.Rigid(kit.VGap(6)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return kit.Wrap(gtx, 4, ws) }))
	})
}

// sheetRow is one section of the dialog's body: what it draws, and the
// fields in it, so focusing one (Tab) can scroll its section into view.
type sheetRow struct {
	w   layout.Widget
	eds []*widget.Editor
}

func (u *ui) sheetRows() []sheetRow {
	k, s := u.k, u.edit
	small := func(t string) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: 6}.Layout(gtx, k.Caption(t[:1]+strings.ToLower(t[1:])))
		})
	}
	setDue := func(days int) func() {
		return func() { s.due.SetText(time.Now().AddDate(0, 0, days).Format(kanban.DateLayout)) }
	}
	row := func(eds []*widget.Editor, kids ...layout.FlexChild) sheetRow {
		return sheetRow{func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
			})
		}, eds}
	}
	eds := func(e ...*widget.Editor) []*widget.Editor { return e }
	u.sheetAgents(s)
	none := sheetRow{w: func(layout.Context) layout.Dimensions { return layout.Dimensions{} }} // a row that doesn't apply
	agent := row(nil, small("AGENT"), layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.agentRow(gtx, s) }))
	if len(s.agents) == 0 {
		agent = none // the organization has none
	}
	ticket := none
	if org := strings.TrimSpace(s.org.Text()); s.id == "" && slices.Contains(u.orgList, org) {
		if s.trackerOf != org { // read the org's config once, not every frame
			s.trackerOf, s.tracker = org, links.Tracker(u.orgConfig(org))
		}
		if s.tracker != "" {
			ticket = row(nil, small("TICKET"), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.choice(gtx, "ticket", []string{"No ticket", "Create in " + s.tracker + " first"}, map[bool]int{false: 0, true: 1}[s.ticket], func(i int) color.NRGBA {
					return map[bool]color.NRGBA{true: kit.CGreen}[i == 1]
				}, func(i int) { s.ticket = i == 1 })
			}))
		}
	}
	lang := none
	if s.id == "" { // set as the card is made: its session and Improve follow it
		lang = row(eds(&s.lang, &s.chatLang), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx, small("CARD AND TICKET IN"), layout.Rigid(k.Field(&s.lang, "English", 0)))
				}),
				layout.Rigid(kit.HGap(12)),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx, small("CLAUDE SPEAKS WITH YOU IN"), layout.Rigid(k.Field(&s.chatLang, "English", 0)))
				}))
		}))
	}
	return []sheetRow{
		row(eds(&s.org, &s.dir), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx, small("ORGANIZATION"), layout.Rigid(k.Field(&s.org, "Type to filter, or pick below", 0)))
				}),
				layout.Rigid(kit.HGap(12)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X, gtx.Constraints.Max.X = gtx.Dp(250), gtx.Dp(250)
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx, small("FOLDER"), layout.Rigid(k.Field(&s.dir, "inside it (optional)", 0)))
				}))
		}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.placePicker(gtx, s) })),
		ticket,
		agent,
		lang,
		row(eds(&s.links), small("LINKS"), layout.Rigid(k.Field(&s.links, "PR, MR or ticket URLs, or Jira keys like ABC-1317; one per line", 40))),
		row(eds(&s.labels, &s.due), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx, small("LABELS"), layout.Rigid(k.Field(&s.labels, "infra, client-x", 0)))
				}),
				layout.Rigid(kit.HGap(12)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X, gtx.Constraints.Max.X = gtx.Dp(250), gtx.Dp(250)
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx, small("DUE"), layout.Rigid(k.Field(&s.due, "YYYY-MM-DD", 0)))
				}))
		}),
			layout.Rigid(kit.VGap(6)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				var kids []layout.FlexChild
				kids = append(kids, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }))
				for _, q := range []struct {
					name string
					f    func()
				}{{"Today", setDue(0)}, {"Tomorrow", setDue(1)}, {"Next week", setDue(7)}, {"Clear", func() { s.due.SetText("") }}} {
					kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d, ok := k.Button(gtx, "due"+q.name, color.NRGBA{}, kit.CHover, kit.RCtl, layout.Inset{Top: 3, Bottom: 3, Left: 7, Right: 7}, k.Lbl(q.name, 11, kit.CAccText, kit.Medium(kit.Sans)))
						if ok {
							q.f()
						}
						return d
					}))
				}
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
			})),
		row(nil, small("PRIORITY"), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.choice(gtx, "prio", prioNames, s.prio, prioColor, func(i int) { s.prio = i })
		})),
		row(nil, small("COLUMN"), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			var names []string
			for _, c := range u.b.Columns {
				names = append(names, c.Title)
			}
			return u.choice(gtx, "col", names, s.col, u.colAccent, func(i int) { s.col = i })
		})),
	}
}

// sheetFooter is the dialog's buttons, outside the scroll so they always
// show. It is laid out before the body, so a click only records what to do
// (s.act); sheet runs it once the whole dialog is drawn.
func (u *ui) sheetFooter(gtx layout.Context) layout.Dimensions {
	k, s := u.k, u.edit
	btn := func(id string, kind kit.Kind, label, cap string, f func()) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			d, ok := k.KBtn(gtx, id, kind, label, cap)
			if ok {
				s.act = f
			}
			return d
		})
	}
	return dialogFoot(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		var row []layout.FlexChild
		improve := map[bool]string{false: "Improve with Claude", true: "Ask again…"}[s.optimized]
		if s.optStop != nil {
			improve = "Stop"
		}
		if s.optimized {
			row = append(row, btn("shDiscard", kit.Ghost, "Discard suggestions", "", u.discardDraft), layout.Rigid(kit.HGap(6)))
		}
		row = append(row, btn("shOpt", kit.Secondary, improve, keys.Cap("sheet.improve"), u.optimizeCard))
		row = append(row, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			if s.optimized {
				return layout.Dimensions{Size: gtx.Constraints.Min}
			}
			tone := kit.CText3
			if strings.HasPrefix(s.optNote, "⚠") {
				tone = kit.CRedText
			}
			return layout.Inset{Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				ch := []layout.FlexChild{}
				if s.optStop != nil {
					ch = append(ch, layout.Rigid(kit.Spinner(12, kit.CAccText)), layout.Rigid(kit.HGap(8)))
				}
				ch = append(ch, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, s.optNote, 12, tone, kit.Sans, 2) }))
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx, ch...)
			})
		}))
		cancel, create := kit.Ghost, kit.Primary
		if s.optStop != nil { // Improve is writing the fields: Stop first
			cancel, create = kit.Off, kit.Off
		}
		row = append(row, btn("shCancel", cancel, "Cancel", "esc", u.closeSheet), layout.Rigid(kit.HGap(6)),
			btn("shSave", create, "Create card", keys.Cap("sheet.commit"), u.commit))
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, row...)
	})
}

// discardDraft takes back Claude's suggestions: the request as the user
// wrote it, ready to create or improve again.
func (u *ui) discardDraft() {
	s := u.edit
	s.title.SetText(s.mineTitle)
	s.notes.SetText(s.mineNotes)
	s.optimized, s.preview, s.accepted, s.opened = false, false, false, true
	s.optNote, s.optErr = improveNote, ""
}
