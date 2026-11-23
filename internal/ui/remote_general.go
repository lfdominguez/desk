package ui

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/company"
	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/tgbot"
	"github.com/lfdominguez/desk/internal/tgfmt"
)

// Desk Control's General chat: the bot's private chat with the user. Slash
// commands read the board and act on it, the way desk's window does; any
// other text goes to the desk Claude, which has the whole board at hand
// (infra.DeskMCP's tools, answered by deskTool). A reply to a card's
// message still goes to that card.

// gnCommands is the General chat's command menu.
var gnCommands = []tgbot.Command{
	{Name: "board", What: "The board by column: /board or /board Acme"},
	{Name: "inbox", What: "Cards waiting on you"},
	{Name: "find", What: "Find cards: /find login"},
	{Name: "card", What: "One card: /card DESK-12"},
	{Name: "new", What: "A new card: /new @Acme fix the login !high"},
	{Name: "tickets", What: "Tickets assigned to you"},
	{Name: "alerts", What: "Nagios alerts"},
	{Name: "ci", What: "CI that failed or is running"},
	{Name: "digest", What: "The standup: /digest today, yesterday or week"},
	{Name: "usage", What: "Each Claude account's plan limits"},
	{Name: "drawings", What: "Drawings as pictures: /drawings or /drawings Acme"},
	{Name: "review", What: "A card's branch, its diff, merge, push or PR: /review DESK-12"},
	{Name: "canvas", What: "A card's canvas as a picture: /canvas DESK-12"},
	{Name: "widget", What: "The board as a live widget: its columns and what waits on you"},
	{Name: "company", What: "Who works on what, the leads and the last hand-offs: /company or /company Acme"},
	{Name: "always", What: "Messages even while desk has the focus: /always on or off"},
	{Name: "mirror", What: "An organization's topics: /mirror Acme full, summary, notify or off"},
	{Name: "stop", What: "Stop the desk Claude's answer"},
	{Name: "forget", What: "Start the desk Claude afresh"},
	{Name: "help", What: "What this chat does"},
}

// gnList caps the lines a list command sends; tests lower it.
var gnList = 40

// gnSpans maps /digest's word to digestSpans' index.
var gnSpans = map[string]int{"today": 0, "yesterday": 1, "week": 2}

// gnMenu sets the General chat's command menu, off the UI goroutine.
func (u *ui) gnMenu() {
	bot, chat := u.phone.bot, u.phone.chat
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		bot.Commands(ctx, chat, gnCommands)
	}()
}

// gnPost sends md (redacted, split) to the General chat in order, the
// last part with rows; a card's message is kept for the replies to it.
func (u *ui) gnPost(md string, rows [][]tgbot.Button, card string) {
	u.gnPostIn(u.phone.thread, md, rows, card)
}

// gnPostIn is gnPost into the private chat's topic thread (0: the chat).
func (u *ui) gnPostIn(thread int64, md string, rows [][]tgbot.Button, card string) {
	u.gnPostAt(u.phone.chat, thread, md, rows, card)
}

// gnPostAt is gnPost into any chat and thread: an organization's topic too.
func (u *ui) gnPostAt(chat, thread int64, md string, rows [][]tgbot.Button, card string) {
	bot := u.phone.bot
	parts := tgfmt.Split(tgfmt.Redact(md), tgfmt.Max-96)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for i, p := range parts {
			o := tgbot.Out{Chat: chat, Thread: thread, Markdown: p}
			if i == len(parts)-1 {
				o.Rows = rows
			}
			id, err := bot.Post(ctx, o)
			if err != nil {
				u.later(func() { u.showToast("Telegram: "+err.Error(), true) })
				return
			}
			if card != "" && i == len(parts)-1 {
				u.later(func() { u.phone.cards[id] = card })
			}
		}
	}()
}

// gnButton is a button that runs f when pressed (on the UI goroutine);
// what f returns is the press's answer.
func (u *ui) gnButton(label, style string, f func() string) tgbot.Button {
	data := u.phoneData("g")
	if u.phone.runs == nil {
		u.phone.runs = map[string]func() string{}
	}
	u.phone.runs[data] = f
	return tgbot.Button{Text: label, Data: data, Style: style}
}

// gnPress runs a pressed button's f once, and takes the message's buttons off.
func (u *ui) gnPress(cb *tgbot.Callback, m *tgbot.Message, f func() string) {
	said := f()
	bot, chat, msg := u.phone.bot, m.Chat.ID, m.ID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		bot.Answer(ctx, cb.ID, said)
		bot.Buttons(ctx, chat, msg, nil)
	}()
}

// gnText is what the user typed in the General chat that isn't a reply to
// a card: a command, or a question for the desk Claude.
func (u *ui) gnText(m *tgbot.Message) {
	text := strings.TrimSpace(m.Text)
	took := u.tgTake(m, func(s string) { u.gnPost(tgfmt.Escape(s), nil, "") }, func(text, pic, file string) {
		if file != "" { // the desk Claude reads no files: only a card can take one
			os.Remove(file)
			u.gnPost(tgfmt.Escape("The desk Claude takes text and pictures. Send a file as a reply to a card's message to give it to that card."), nil, "")
			return
		}
		u.gnAsk(m, text, pic)
	})
	if took {
		return
	}
	if text == "" {
		u.gnPost(tgfmt.Escape("Text, pictures, files and voice notes reach desk from here."), nil, "")
		return
	}
	if !strings.HasPrefix(text, "/") {
		u.gnAsk(m, text)
		return
	}
	cmd, arg, _ := strings.Cut(text[1:], " ")
	cmd, _, _ = strings.Cut(cmd, "@") // /board@desk_bot
	arg = strings.TrimSpace(arg)
	switch strings.ToLower(cmd) {
	case "board":
		u.gnPost(u.gnBoard(arg), nil, "")
	case "inbox":
		u.gnPost(u.gnInbox(), nil, "")
	case "find":
		u.gnPost(u.gnFind(arg), nil, "")
	case "card":
		u.gnCard(arg)
	case "new":
		u.gnNewCmd(arg)
	case "tickets":
		u.gnPost(u.gnTickets(arg), nil, "")
	case "alerts":
		u.gnPost(u.gnAlerts(arg), nil, "")
	case "ci":
		u.gnPost(u.gnCI(arg), nil, "")
	case "digest":
		span, ok := gnSpans[strings.ToLower(cmp.Or(arg, "today"))]
		if !ok {
			u.gnPost("Say since when: /digest today, yesterday or week.", nil, "")
			return
		}
		u.gnPost(u.digestText("", digestSince(span, time.Now())), nil, "")
	case "usage":
		u.gnUsage()
	case "company":
		u.gnPost(u.gnCompany(arg), nil, "")
	case "drawings":
		u.tgDrawings(u.phone.chat, u.phone.thread, arg, "")
	case "review":
		c := u.cardOfKey(strings.ToUpper(arg))
		if c == nil {
			u.gnPost("No card "+tgfmt.Escape(cmp.Or(arg, "named"))+": /review DESK-12", nil, "")
			return
		}
		u.tgReview(u.phone.chat, u.phone.thread, c.ID)
	case "widget":
		u.tgWidgetSend(u.phone.thread)
	case "canvas":
		c := u.cardOfKey(strings.ToUpper(arg))
		if c == nil {
			u.gnPost("No card "+tgfmt.Escape(cmp.Or(arg, "named"))+": /canvas DESK-12", nil, "")
			return
		}
		u.tgCanvas(u.phone.chat, u.phone.thread, c.ID)
	case "always":
		u.gnPost(u.gnAlways(arg), nil, "")
	case "mirror":
		u.gnPost(u.gnMirror(arg), nil, "")
	case "stop":
		if p := u.phone.gnProc; p != nil {
			p.Interrupt()
		}
		u.gnPost("Stopped.", nil, "")
	case "forget":
		u.gnForget()
		u.gnPost("The desk Claude starts afresh with your next message.", nil, "")
	default: // help, start and what desk doesn't know
		var b strings.Builder
		b.WriteString("**Desk Control**\n\nWrite anything and the desk Claude answers, with the whole board at hand: " +
			"it reads cards, makes them and tells them things. Reply to a card's message to write to that card. Commands:\n\n")
		for _, c := range gnCommands {
			fmt.Fprintf(&b, "- /%s · %s\n", c.Name, tgfmt.Escape(c.What))
		}
		u.gnPost(b.String(), nil, "")
	}
}

// gnOrg is name as one of desk's organizations, ok false when none.
func (u *ui) gnOrg(name string) (string, bool) {
	i := slices.IndexFunc(u.orgList, func(o string) bool { return strings.EqualFold(o, name) })
	if i < 0 {
		return "", false
	}
	return u.orgList[i], true
}

// gnLine is card c's line in a list: state, key, title, organization.
func gnLine(c kanban.Card, org bool) string {
	s := rmEmoji(c.Status) + " `" + c.Key() + "` " + tgfmt.Escape(c.Title)
	if org && c.Org != "" {
		s += " · _" + tgfmt.Escape(c.Org) + "_"
	}
	return "- " + s + "\n"
}

// gnBoard is the board (org's, "" every one), a section per column but
// Done, which is counted.
func (u *ui) gnBoard(org string) string {
	if org != "" {
		o, ok := u.gnOrg(org)
		if !ok {
			return "desk has no organization called " + tgfmt.Escape(org) + "."
		}
		org = o
	}
	var b strings.Builder
	b.WriteString("**" + tgfmt.Escape(cmp.Or(org, "The board")) + "**\n")
	for _, col := range u.b.Columns {
		var cs []kanban.Card
		for _, c := range col.Cards {
			if c.Parent == "" && (org == "" || c.Org == org) {
				cs = append(cs, c)
			}
		}
		if len(cs) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n### %s (%d)\n", tgfmt.Escape(col.Title), len(cs))
		if col.Title == "Done" {
			continue
		}
		for i, c := range cs {
			if i == gnList {
				fmt.Fprintf(&b, "- … %d more\n", len(cs)-i)
				break
			}
			b.WriteString(gnLine(c, org == ""))
		}
	}
	return b.String()
}

// gnInbox is the cards waiting on the user: a request, their turn, a
// block, a failure, a review.
func (u *ui) gnInbox() string {
	var b strings.Builder
	n := 0
	for _, c := range u.boardCards() {
		wants := u.asks[c.ID] != nil || slices.Contains([]string{kanban.StatusWaiting, kanban.StatusBlocked, kanban.StatusFailed, kanban.StatusReview}, c.Status)
		if c.Parent != "" || !wants {
			continue
		}
		n++
		b.WriteString(gnLine(c, true))
		if a := u.asks[c.ID]; a != nil && len(a.r.Questions) == 0 {
			b.WriteString("  wants to run `" + strings.ReplaceAll(firstLine(a.r.Line), "`", "'") + "`\n")
		} else if c.Note != "" {
			b.WriteString("  " + tgfmt.Escape(firstLine(c.Note)) + "\n")
		}
	}
	if n == 0 {
		return "Nothing waits on you."
	}
	return fmt.Sprintf("**Waiting on you (%d)**\n\n", n) + b.String() + "\n_/card DESK-n shows one; reply to it to answer._"
}

// gnFind is the board's and the archive's cards matching q.
func (u *ui) gnFind(q string) string {
	if q == "" {
		return "Say what to look for: /find login"
	}
	var b strings.Builder
	n := 0
	add := func(c kanban.Card, where string) {
		t := ""
		if l := ticketOf(&c); l != nil {
			t = " " + l.Ref
		}
		if c.Parent != "" || !matches(q, c.Key()+" "+c.Title+" "+c.Notes+t) {
			return
		}
		if n++; n <= gnList {
			b.WriteString(strings.TrimSuffix(gnLine(c, true), "\n") + " · " + tgfmt.Escape(where) + "\n")
		}
	}
	for _, col := range u.b.Columns {
		for _, c := range col.Cards {
			add(c, col.Title)
		}
	}
	for _, c := range u.b.Archive {
		add(c, "archived")
	}
	if n == 0 {
		return "No card matches " + tgfmt.Escape(q) + "."
	}
	if n > gnList {
		fmt.Fprintf(&b, "- … %d more\n", n-gnList)
	}
	return b.String()
}

// gnCard sends card key's header and last messages; a reply to it goes to
// its Claude, and a card that hasn't started gets a Start button.
func (u *ui) gnCard(key string) {
	c := u.cardOfKey(strings.ToUpper(strings.TrimSpace(key)))
	if c == nil {
		u.gnPost("No card "+tgfmt.Escape(cmp.Or(key, "named"))+": /card DESK-12", nil, "")
		return
	}
	col, _ := u.b.Find(c.ID)
	md := rmHead(c, u.b.Columns[col].Title)
	if s := strings.TrimSpace(c.Notes); s != "" {
		md += "\n\n" + clipKeep(s, 1500)
	}
	if posts := u.rmPosts(c.ID, orgs.MirrorSummary, lastOf(u.chat(c.ID), boardReadMsgs)); len(posts) > 0 {
		md += "\n\n———\n\n" + posts[len(posts)-1]
	}
	md += "\n\n_Reply to this message to write to its Claude._"
	var rows [][]tgbot.Button
	id := c.ID
	switch {
	case u.procs[id] != nil && c.Status == kanban.StatusWorking:
		rows = [][]tgbot.Button{{u.gnButton("Stop its turn", "danger", func() string { u.stop(id); return "Stopping" })}}
	case c.Session.ID == "" && u.procs[id] == nil:
		rows = [][]tgbot.Button{{u.gnButton("Start it", "success", func() string { return u.gnStart(id) })}}
	}
	u.gnPost(md, rows, id)
}

// lastOf is msgs' last n.
func lastOf(msgs []kanban.ChatMsg, n int) []kanban.ChatMsg {
	return msgs[max(0, len(msgs)-n):]
}

// gnStart starts card id's Claude on it, as the board's Start does.
func (u *ui) gnStart(id string) string {
	c := u.b.Card(id)
	if c == nil {
		return "That card is gone"
	}
	key := c.Key()
	u.send(id, startWork)
	return "Started " + key
}

// gnNew makes card c in its organization's To do, started when start:
// its id and what was made, or why not.
func (u *ui) gnNew(c kanban.Card, start bool) (string, string, error) {
	org, ok := u.gnOrg(c.Org)
	switch {
	case !ok:
		return "", "", fmt.Errorf("desk has no organization called %q; it has %s", c.Org, strings.Join(u.orgList, ", "))
	case strings.TrimSpace(c.Title) == "":
		return "", "", errors.New("a card needs a title")
	}
	c.Org, c.Title, c.Updated = org, strings.TrimSpace(c.Title), time.Now()
	n := u.b.Add(max(u.b.Column("To do"), 0), c)
	u.save()
	if start {
		u.send(n.ID, startWork)
		return n.ID, "Made " + n.Key() + " and started it: " + n.Title, nil
	}
	return n.ID, "Made " + n.Key() + " in To do: " + n.Title, nil
}

// gnNewCmd is /new: the request read as the new-card dialog reads it
// (@Org, #label, !priority, a due day, ticket keys and links); without an
// organization, buttons ask which.
func (u *ui) gnNewCmd(text string) {
	if text == "" {
		u.gnPost("Say what to do: /new @Acme fix the login #auth !high tomorrow", nil, "")
		return
	}
	ts := smartParse(text, u.orgList, time.Now(), nil)
	org := smartVal(ts, "org")
	if org == "" && len(u.orgList) == 1 {
		org = u.orgList[0]
	}
	if org == "" {
		var rows [][]tgbot.Button
		for i, o := range u.orgList {
			if i%3 == 0 {
				rows = append(rows, nil)
			}
			rows[len(rows)-1] = append(rows[len(rows)-1], u.gnButton(o, "", func() string { u.gnMade(o, text, ts); return o }))
		}
		u.gnPost("Which organization is it for?", rows, "")
		return
	}
	u.gnMade(org, text, ts)
}

// gnMade makes /new's card in org and says so, with a button to start it.
func (u *ui) gnMade(org, text string, ts []smartTok) {
	c := kanban.Card{Org: org, Title: smartTitle(text, ts), Notes: text, Labels: smartVals(ts, "label"), Due: smartVal(ts, "due"),
		Priority: max(0, slices.Index(prioNames, smartVal(ts, "prio")))}
	cfg := u.orgConfig(org)
	for _, raw := range smartVals(ts, "link") {
		if l, ok := links.Parse(raw, cfg); ok {
			c.Links = append(c.Links, l)
		}
	}
	id, said, err := u.gnNew(c, false)
	if err != nil {
		u.gnPost("Not made: "+tgfmt.Escape(err.Error()), nil, "")
		return
	}
	go u.checkLinks([]string{id})
	u.gnPost(tgfmt.Escape(said), [][]tgbot.Button{{u.gnButton("Improve it", "", func() string { return u.gnImprove(id) }), u.gnStartBtn(id)}}, id)
}

// gnStartBtn is a card's Start it.
func (u *ui) gnStartBtn(id string) tgbot.Button {
	return u.gnButton("Start it", "success", func() string { return u.gnStart(id) })
}

// gnImprove is /new's Improve it: the card's request through Prompt
// Optimization, as the new card dialog's Improve, then the card takes the
// draft (title and notes; labels added; priority, due and folder where
// unset) unless it started meanwhile. Its tickets and PRs are read first.
func (u *ui) gnImprove(id string) string {
	c := u.b.Card(id)
	if c == nil {
		return "That card is gone"
	}
	ask := u.optAsk(c.Org, c.Dir, cmp.Or(strings.TrimSpace(c.Notes), c.Title), c.Lang)
	org, key, thread, cfg := c.Org, c.Key(), u.phone.thread, u.orgConfig(c.Org)
	var ls []kanban.Link
	for _, l := range c.Links {
		if l.Kind != links.KindURL {
			ls = append(ls, l)
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		for _, l := range ls { // what its tickets and PRs say, through the org's accounts, as the dialog's Improve
			text, err := describe(ctx, l, cfg)
			if err != nil {
				text = l.Ref + ": not read (" + firstLine(err.Error()) + ")"
			}
			if text != "" {
				ask.Linked = append(ask.Linked, text)
			}
		}
		var d claude.Draft
		env, err := u.goEnv(org, "improve")
		if err == nil {
			ask.Env = env
			d, err = optimize(ctx, ask, func(claude.Progress) {})
		}
		u.later(func() {
			c := u.b.Card(id)
			switch {
			case c == nil:
				return
			case err != nil:
				u.gnPostIn(thread, tgfmt.Escape(key+": Improve didn't finish: "+firstLine(err.Error())), nil, "")
				return
			case c.Session.ID != "" || c.Status != "":
				u.gnPostIn(thread, tgfmt.Escape(key+" started before Improve finished: left as it was."), nil, "")
				return
			}
			c.Title = cmp.Or(strings.TrimSpace(d.Title), c.Title)
			c.Notes = cmp.Or(strings.TrimSpace(d.Prompt), c.Notes)
			for _, l := range d.Labels {
				if l = strings.ToLower(strings.TrimSpace(l)); l != "" && !slices.Contains(c.Labels, l) {
					c.Labels = append(c.Labels, l)
				}
			}
			if p := slices.IndexFunc(prioNames, func(n string) bool { return strings.EqualFold(n, d.Priority) }); p > 0 && c.Priority == 0 {
				c.Priority = p
			}
			if _, err := time.Parse(kanban.DateLayout, d.Due); err == nil && c.Due == "" {
				c.Due = d.Due
			}
			if f := strings.Trim(d.Folder, "/"); f != "" && c.Dir == "" {
				if fi, err := os.Stat(orgs.Dir(u.root, org, f)); err == nil && fi.IsDir() {
					c.Dir = f
				}
			}
			c.Updated = time.Now()
			u.save()
			u.gnPostIn(thread, "**"+tgfmt.Escape(key+" · "+c.Title)+"**\n\n"+c.Notes,
				[][]tgbot.Button{{u.gnStartBtn(id)}}, id)
		})
	}()
	return "Improving " + key + " with Claude…"
}

// gnCompanyStates are coCardState's states, in the order /company lists them.
var gnCompanyStates = []struct{ state, what string }{
	{"you", "Needs you"}, {"stuck", "Stuck"}, {"turn", "Your turn"}, {"work", "Working"}, {"done", "Ready for review"},
}

// gnCompany is the Company view in words (org's, "" every one's): its
// cards with a session by state, the leads at work and the last hand-offs.
func (u *ui) gnCompany(org string) string {
	if org != "" {
		o, ok := u.gnOrg(org)
		if !ok {
			return tgfmt.Escape("desk has no organization called " + org + ".")
		}
		org = o
	}
	byOrg := map[string][]kanban.Card{}
	for _, c := range u.coCards() {
		if org == "" || c.Org == org {
			byOrg[c.Org] = append(byOrg[c.Org], c)
		}
	}
	for _, st := range u.leads {
		if _, ok := byOrg[st.org]; !ok && (org == "" || st.org == org) {
			byOrg[st.org] = nil
		}
	}
	var b strings.Builder
	b.WriteString("**Company** · " + tgfmt.Escape(u.companySentence()) + "\n")
	if len(byOrg) == 0 {
		b.WriteString("\nNo card has a session.")
		return b.String()
	}
	now := time.Now()
	for _, o := range slices.Sorted(maps.Keys(byOrg)) {
		dir := orgs.Dir(u.root, o, "")
		fmt.Fprintf(&b, "\n### %s · %s\n", tgfmt.Escape(o), tgfmt.Escape(fmt.Sprintf("$%.2f today", company.Spent(dir, now))))
		for _, s := range gnCompanyStates {
			var keys []string
			for _, c := range byOrg[o] {
				if u.coCardState(c) == s.state {
					keys = append(keys, "`"+c.Key()+"` "+tgfmt.Escape(tgfmt.Clip(c.Title, 40)))
				}
			}
			if len(keys) > 0 {
				fmt.Fprintf(&b, "- **%s:** %s\n", s.what, strings.Join(keys, ", "))
			}
		}
		for _, k := range slices.Sorted(maps.Keys(u.leads)) {
			if st := u.leads[k]; st.org == o && len(st.from) > 0 {
				fmt.Fprintf(&b, "- **%s lead:** %s\n", tgfmt.Escape(st.dept.Name), tgfmt.Escape(fmt.Sprintf("%d %s waiting", len(st.from), map[bool]string{true: "brief", false: "briefs"}[len(st.from) == 1])))
			}
		}
		es := company.Read(dir)
		for _, e := range es[max(0, len(es)-5):] {
			what := tgfmt.Escape(e.From + " → " + e.To + ": " + tgfmt.Clip(e.Text, 80))
			if e.Verdict != "" {
				what += " · **" + tgfmt.Escape(e.Verdict) + "**"
			}
			fmt.Fprintf(&b, "- _%s_ %s\n", e.At.Format("15:04"), what)
		}
	}
	return b.String()
}

// gnUsage posts each Claude account's plan limits, with the organizations
// that use it, read off the UI goroutine.
func (u *ui) gnUsage() {
	by := map[string][]string{} // account → its organizations
	for _, o := range u.orgList {
		a := orgs.Account(u.root, o)
		by[a] = append(by[a], o)
	}
	home, thread := u.home, u.phone.thread
	go func() {
		var b strings.Builder
		for _, a := range slices.Sorted(maps.Keys(by)) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			ls, err := planLimits(ctx, home, a)
			cancel()
			fmt.Fprintf(&b, "\n### %s\n", tgfmt.Escape(cmp.Or(a, "default")+" · "+strings.Join(by[a], ", ")))
			if err != nil {
				fmt.Fprintf(&b, "%s\n", tgfmt.Escape(firstLine(err.Error())))
			}
			for _, l := range ls {
				fmt.Fprintf(&b, "- %s · %s\n", tgfmt.Escape(l.Name), tgfmt.Escape(fmt.Sprintf("%.0f%%, resets %s", l.Percent, resets(l.Resets))))
			}
		}
		u.later(func() { u.gnPostIn(thread, cmp.Or(strings.TrimSpace(b.String()), "No organizations."), nil, "") })
	}()
}

// gnOrgs is org as a list of one, or every organization that has m's
// entries, sorted.
func gnOrgs[T any](m map[string]T, org string) []string {
	if org != "" {
		return []string{org}
	}
	return slices.Sorted(maps.Keys(m))
}

// gnTickets is the tickets assigned to the user (org's, "" every one's).
func (u *ui) gnTickets(org string) string {
	var b strings.Builder
	for _, o := range gnOrgs(u.tickets, org) {
		ts, ok := u.tickets[o]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "\n### %s (%d)\n", tgfmt.Escape(o), len(ts))
		for i, t := range ts {
			if i == gnList {
				fmt.Fprintf(&b, "- … %d more\n", len(ts)-i)
				break
			}
			card := ""
			if c := u.ticketCard(t); c != nil {
				card = " · `" + c.Key() + "`"
			}
			fmt.Fprintf(&b, "- [%s](%s) %s · _%s_%s\n", tgfmt.Escape(t.Key), t.URL, tgfmt.Escape(t.Title), tgfmt.Escape(t.Status), card)
		}
	}
	if b.Len() == 0 {
		return "No tickets assigned to you, or no tracker set up."
	}
	return "**Your tickets**\n" + b.String()
}

// gnAlerts is the Nagios alerts (org's, "" every one's), acknowledged
// ones in italics.
func (u *ui) gnAlerts(org string) string {
	var b strings.Builder
	for _, o := range gnOrgs(u.alerts, org) {
		as := u.alerts[o]
		if len(as) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n### %s (%d)\n", tgfmt.Escape(o), len(as))
		for i, a := range as {
			if i == gnList {
				fmt.Fprintf(&b, "- … %d more\n", len(as)-i)
				break
			}
			mark := map[bool]string{true: "🔴", false: "🟡"}[a.Critical()]
			s := tgfmt.Escape(strings.Trim(a.Host+" · "+a.Service, " ·")) + " — " + tgfmt.Escape(tgfmt.Clip(firstLine(a.Output), 200))
			if a.Acked {
				s = "_" + s + "_"
			}
			fmt.Fprintf(&b, "- %s %s\n", mark, s)
		}
	}
	if b.Len() == 0 {
		return "No alerts."
	}
	return "**Alerts**\n" + b.String()
}

// gnCI is the CI that failed or runs (org's, "" every one's).
func (u *ui) gnCI(org string) string {
	var b strings.Builder
	for _, o := range gnOrgs(u.ci, org) {
		var lines []string
		for _, it := range u.ci[o] {
			st := ciState(it)
			if st != links.CIFailed && st != links.CIRunning {
				continue
			}
			mark := map[bool]string{true: "❌", false: "🟣"}[st == links.CIFailed]
			lines = append(lines, fmt.Sprintf("- %s `%s` [%s](%s) %s\n", mark, it.key, tgfmt.Escape(cmp.Or(it.link.Ref, "link")), it.link.URL, tgfmt.Escape(it.title)))
		}
		if len(lines) > 0 {
			fmt.Fprintf(&b, "\n### %s\n%s", tgfmt.Escape(o), strings.Join(lines, ""))
		}
	}
	if b.Len() == 0 {
		return "No CI failed or running."
	}
	return "**CI**\n" + b.String()
}

// gnAlways is /always on|off: messages while desk has the focus too.
func (u *ui) gnAlways(arg string) string {
	on, ok := map[string]bool{"on": true, "off": false}[strings.ToLower(arg)]
	if !ok {
		return fmt.Sprintf("It's %s. Say /always on or /always off.", map[bool]string{true: "on", false: "off"}[u.phone.always])
	}
	p := orgs.PhoneSettings()
	p.Always = on
	if err := orgs.SavePhone(p); err != nil {
		return "Not saved: " + tgfmt.Escape(err.Error())
	}
	u.phone.always = on
	if on {
		return "Cards reach you here even while desk has the focus."
	}
	return "Cards reach you here only while desk isn't in front of you."
}

// gnMirror is /mirror Acme full: how much of an organization's cards its
// group gets.
func (u *ui) gnMirror(arg string) string {
	name, mode, _ := strings.Cut(arg, " ")
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "off" {
		mode = orgs.MirrorOff
	}
	org, ok := u.gnOrg(name)
	if !ok || !slices.Contains(orgs.Mirrors, mode) {
		return "Say which organization and how much: /mirror Acme full, summary, notify or off."
	}
	o := orgs.TelegramSettings().Orgs[org]
	o.Mirror = mode
	if err := orgs.SetTelegramOrg(org, o); err != nil {
		return "Not saved: " + tgfmt.Escape(err.Error())
	}
	u.rmConfig()
	switch {
	case mode == orgs.MirrorOff:
		return tgfmt.Escape(org) + "'s cards stay off Telegram."
	case o.Chat == 0:
		return tgfmt.Escape(org) + " is on, with no group yet: press Set up in desk's Settings → Telegram, or /link " + tgfmt.Escape(org) + " in a group of yours."
	}
	return tgfmt.Escape(org) + "'s cards talk in its group: " + mode + "."
}

// gnPrompt is the desk Claude's system prompt.
const gnPrompt = `You are desk's own Claude, in the user's Telegram chat with desk (Desk Control's General chat).
desk is the user's work manager: a Kanban board of cards (keys DESK-n), each a Claude Code session in an organization's folder.
Answer from the board through your tools: cards, card_read, digest. Make a card (card_create) or message one (card_message) only when the user asks.
You don't write code or run commands yourself: that is the cards' work.
Your answers are read on a phone: short, Markdown, card keys in backticks.`

// gnAsk hands text to the desk Claude, started when it isn't running.
func (u *ui) gnAsk(m *tgbot.Message, text string, pics ...string) {
	ph := u.phone
	if ph.gnProc == nil {
		p, err := u.gnLaunch()
		if err != nil {
			u.gnPost("The desk Claude didn't start: "+tgfmt.Escape(err.Error()), nil, "")
			return
		}
		ph.gnProc = p
	}
	ph.gnThread = ph.thread
	if err := ph.gnProc.Send(text, pics...); err != nil {
		u.gnPost("Not sent: "+tgfmt.Escape(err.Error()), nil, "")
		return
	}
	bot, chat, msg := ph.bot, m.Chat.ID, m.ID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		bot.React(ctx, chat, msg, "👀")
	}()
}

// gnLaunch starts the desk Claude in desk's root, under the user's own
// Claude profile, with the board tools only.
func (u *ui) gnLaunch() (session, error) {
	ph := u.phone
	o := claude.Options{Home: u.home, Dir: u.root, System: gnPrompt + u.aboutMe("")}
	mcp, allow := infra.DeskMCP(u.cardSock)
	if mcp != "" {
		o.Args = append(o.Args, "--mcp-config", mcp)
	}
	// no built-in tools and only desk's MCP server: each tool's schema goes with every turn
	o.Args, o.Strict = append(o.Args, "--allowedTools="+strings.Join(allow, ","), "--tools", ""), true
	if ph.gnSid == "" { // the last run's, kept beside the board
		b, _ := os.ReadFile(u.gnSidFile())
		ph.gnSid = strings.TrimSpace(string(b))
	}
	if ph.gnSid == "" {
		ph.gnSid = claude.NewSessionID()
		o.New = ph.gnSid
		os.WriteFile(u.gnSidFile(), []byte(ph.gnSid+"\n"), 0o600)
	} else {
		o.Resume = ph.gnSid
	}
	var p session
	p, err := startClaude(o, func(e claude.Event) { u.later(func() { u.gnEvent(p, e) }) })
	if err == nil {
		own(p, infra.Owner{Card: "desk:"})
	}
	return p, err
}

// gnEvent applies one of the desk Claude's events (p: its process; a
// replaced one's are dropped): its turn's text goes out at the turn's end.
func (u *ui) gnEvent(p session, e claude.Event) {
	ph := u.phone
	if ph == nil || ph.gnProc != p {
		return
	}
	switch e.Kind {
	case claude.KindText:
		if t := strings.TrimSpace(e.Text); t != "" && e.Parent == "" {
			ph.gnSaid = append(ph.gnSaid, t)
		}
	case claude.KindAsk: // its tools are allowed; anything else is refused
		p.Respond(e.Ask.ID, false, nil, "The desk Claude only uses its board tools.")
	case claude.KindResult:
		said := strings.Join(ph.gnSaid, "\n\n")
		ph.gnSaid = nil
		if e.Error && e.Text != "" {
			said = strings.TrimSpace(said + "\n\n" + tgfmt.Escape("Error: "+firstLine(e.Text)))
		}
		if said != "" {
			u.gnPostIn(ph.gnThread, said, nil, "")
		}
	case claude.KindExit:
		ph.gnProc, ph.gnSaid = nil, nil
		if e.Text != "" {
			u.gnPostIn(ph.gnThread, "The desk Claude stopped: "+tgfmt.Escape(firstLine(e.Text)), nil, "")
		}
	}
}

// gnSidFile keeps the desk Claude's session over restarts.
func (u *ui) gnSidFile() string { return filepath.Join(filepath.Dir(u.path), "telegram-claude.txt") }

// gnQuit ends the desk Claude's process; its session stays for the next.
func (u *ui) gnQuit() {
	ph := u.phone
	if ph.gnProc != nil {
		ph.gnProc.Close()
	}
	ph.gnProc, ph.gnSaid = nil, nil
}

// gnForget ends the desk Claude and its session: the next message starts
// a new one.
func (u *ui) gnForget() {
	u.gnQuit()
	u.phone.gnSid = ""
	os.Remove(u.gnSidFile())
}

// deskTool answers the desk Claude's tool call, off the UI goroutine.
func (u *ui) deskTool(name string, raw json.RawMessage) (string, bool) {
	var a struct {
		Org, Query, Column, Key, Title, Notes, Text, Since string
		Start                                              bool
	}
	json.Unmarshal(raw, &a)
	key := strings.ToUpper(strings.TrimSpace(a.Key))
	text, err := onBoard(u, func() (string, error) {
		switch name {
		case "cards":
			org := ""
			if a.Org != "" {
				o, ok := u.gnOrg(a.Org)
				if !ok {
					return "", fmt.Errorf("no organization %q; desk has %s", a.Org, strings.Join(u.orgList, ", "))
				}
				org = o
			}
			return u.boardList(org, a.Query, a.Column), nil
		case "digest":
			span, ok := gnSpans[cmp.Or(a.Since, "today")]
			if !ok {
				return "", fmt.Errorf("since: today, yesterday or week, not %q", a.Since)
			}
			return u.digestText("", digestSince(span, time.Now())), nil
		case "card_create":
			_, said, err := u.gnNew(kanban.Card{Org: a.Org, Title: a.Title, Notes: cmp.Or(a.Notes, a.Title)}, a.Start)
			return said, err
		}
		c := u.cardOfKey(key)
		if c == nil {
			return "", fmt.Errorf("no card %s", key)
		}
		if name == "card_read" {
			return u.boardRead(c.Org, key)
		}
		if strings.TrimSpace(a.Text) == "" {
			return "", errors.New("say what to tell it: text")
		}
		u.send(c.ID, a.Text)
		return "Sent to " + key + ".", nil
	})
	if err != nil {
		return err.Error(), false
	}
	return text, true
}
