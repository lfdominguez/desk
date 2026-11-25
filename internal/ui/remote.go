package ui

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/tgbot"
	"github.com/lfdominguez/desk/internal/tgfmt"
	"github.com/lfdominguez/desk/internal/tgsetup"
)

// Desk Control's card topics: in each mirrored organization's forum group
// (settings.toml's [telegram.orgs]), every card with a session gets a
// topic. Its name carries the card's state, a pinned header its column,
// links and cost; the conversation follows as the mirror allows (Notify:
// the turns that end on the user; Summary: the user's and Claude's words;
// Full: also its tools), and a request comes with buttons. Done,
// archived or deleted takes the topic away (the conversation stays in desk);
// back on the board, the card gets a new one. What's typed in a topic goes to the card's Claude.
//
// Each board change (save) plans what Telegram lacks on the UI goroutine,
// then one goroutine sends it, one plan at a time; what went is kept in
// telegram.json beside the board.

// rmTopic is a card's topic as desk last left it.
type rmTopic struct {
	Chat    int64  `json:"chat"`
	Thread  int64  `json:"thread"`
	Header  int64  `json:"header"` // the pinned header's message
	Name    string `json:"name"`
	Head    string `json:"head"` // the header as last sent
	Sent    int    `json:"sent"` // the card's chat messages mirrored
	Closed  bool   `json:"closed,omitempty"`
	Said    string `json:"said,omitempty"` // Notify: the turn's end last told
	Ask     string `json:"ask,omitempty"`  // the request whose buttons are up
	AskMsg  int64  `json:"ask_msg,omitempty"`
	AskText string `json:"ask_text,omitempty"`
}

// remote is the topics and the plan under way.
type remote struct {
	topics map[string]*rmTopic // by card id
	orgs   map[string]orgs.TelegramOrg
	busy   bool              // a plan is being sent
	again  bool              // the board changed meanwhile: plan again after
	from   map[string]string // a card's message that came from its topic: not sent back
	menus  map[int64]bool    // the groups whose command menu is set this run
	screen bool              // [telegram] screen, for the Settings topic
	pinned map[string]bool   // "chat:topic" pinned (or tried) this run
}

// rmCommands is an organization group's command menu.
var rmCommands = []tgbot.Command{
	{Name: "stop", What: "In a card's topic: stop its Claude's turn"},
	{Name: "drawings", What: "In a card's topic: its drawings and its organization's, as pictures"},
	{Name: "review", What: "In a card's topic: its branch, the diff, merge, push or PR"},
	{Name: "canvas", What: "In a card's topic: its canvas as a picture"},
	{Name: "screen", What: "In the Infra topic: a picture of a terminal, /screen web1"},
	{Name: "help", What: "What this topic does"},
}

// rmFile is where the topics are kept.
func (u *ui) rmFile() string { return filepath.Join(filepath.Dir(u.path), "telegram.json") }

// startRemote reads the topics desk made; after startPhone.
func (u *ui) startRemote() {
	u.rm = &remote{topics: map[string]*rmTopic{}, from: map[string]string{}, menus: map[int64]bool{}, pinned: map[string]bool{}}
	if b, err := os.ReadFile(u.rmFile()); err == nil {
		json.Unmarshal(b, &u.rm.topics)
	}
	if u.phone != nil {
		u.tgWidgetLoad()
	}
	u.rmConfig()
}

// rmConfig takes the organizations' mirrors and groups again (after
// Settings, Set up, /link) and catches up.
func (u *ui) rmConfig() {
	if u.rm == nil {
		return
	}
	u.rm.orgs, u.rm.screen = orgs.TelegramSettings().Orgs, orgs.PhoneSettings().Screen
	if u.phone != nil {
		bot := u.phone.bot
		for _, o := range u.rm.orgs {
			if o.Chat != 0 && !u.rm.menus[o.Chat] {
				u.rm.menus[o.Chat] = true
				go func(chat int64) {
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()
					bot.Commands(ctx, chat, rmCommands)
				}(o.Chat)
			}
		}
	}
	u.remoteKick()
	u.rmPin()
}

// tgPinTopics pins topics in a forum's list as the user's account; tests
// replace it.
var tgPinTopics = tgsetup.Account.PinTopics

// rmPin keeps each mirrored group's General topic and its Infra topic at
// the top of the topic list, where a phone sees them first. Only the
// user's logged-in account can pin (a bot can't); each is tried once a run.
func (u *ui) rmPin() {
	t := orgs.TelegramSettings()
	a, err := tgAccount(t)
	if err != nil || !a.LoggedIn() {
		return
	}
	for org, o := range u.rm.orgs {
		if o.Chat == 0 || o.Channel == 0 {
			continue
		}
		var topics []int
		for _, id := range []int64{1, u.rmThread(rmInfraKey(org))} { // 1: General
			if k := fmt.Sprint(o.Chat, ":", id); id != 0 && !u.rm.pinned[k] {
				u.rm.pinned[k] = true
				topics = append(topics, int(id))
			}
		}
		if len(topics) == 0 {
			continue
		}
		c := tgsetup.Chat{ID: o.Chat, Channel: o.Channel, Access: o.Access}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := tgPinTopics(a, ctx, c, topics...); err != nil {
				u.later(func() { u.showToast("Telegram: "+firstLine(err.Error()), true) })
			}
		}()
	}
}

// rmThread is key's topic thread, 0 none yet.
func (u *ui) rmThread(key string) int64 {
	if t := u.rm.topics[key]; t != nil {
		return t.Thread
	}
	return 0
}

// rmMirror is how card c's organization is mirrored ("" none) and its group.
func (u *ui) rmMirror(c *kanban.Card) (string, int64) {
	if u.rm == nil || c.Parent != "" { // an autopilot's cards report to it
		return orgs.MirrorOff, 0
	}
	o := u.rm.orgs[c.Org]
	if o.Chat == 0 {
		return orgs.MirrorOff, 0
	}
	return o.Mirror, o.Chat
}

// rmCardOf is the card whose topic thread is in chat.
func (u *ui) rmCardOf(chat, thread int64) string {
	if u.rm == nil || thread == 0 {
		return ""
	}
	for id, t := range u.rm.topics {
		if t.Chat == chat && t.Thread == thread {
			return id
		}
	}
	return ""
}

// rmJob is what one card's topic lacks.
type rmJob struct {
	id      string
	t       rmTopic // as it is, with Chat set
	create  bool
	name    string // a new name ("": as it is)
	head    string // a new header
	posts   []string
	ask     *tgbot.Out // a request's message, buttons and all
	askID   string
	askEnd  string // the old request's message once it's answered
	act     string // "reopen" or "delete"
	sent    int
	said    string
	failure string
}

// remoteKick plans and sends what the topics lack, unless a plan is under
// way (then once more after it).
func (u *ui) remoteKick() {
	if u.rm == nil || u.phone == nil {
		return
	}
	if u.rm.busy {
		u.rm.again = true
		return
	}
	jobs := u.remotePlan()
	if len(jobs) == 0 {
		return
	}
	u.rm.busy = true
	bot := u.phone.bot
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		for _, j := range jobs {
			rmRun(ctx, bot, j)
		}
		u.later(func() { u.remoteDone(jobs) })
	}()
}

// remoteDone keeps what went and plans again if the board moved meanwhile.
func (u *ui) remoteDone(jobs []*rmJob) {
	var failed []string
	for _, j := range jobs {
		switch {
		case j.act == "delete" && j.failure == "":
			delete(u.rm.topics, j.id)
		case j.t.Thread != 0:
			t := j.t
			u.rm.topics[j.id] = &t
		}
		if j.failure != "" {
			failed = append(failed, j.failure)
		}
	}
	if b, err := json.Marshal(u.rm.topics); err == nil {
		writeAtomic(u.rmFile(), string(b), 0o600)
	}
	if len(failed) > 0 {
		u.showToast("Telegram: "+failed[0], true)
	}
	u.rm.busy = false
	// An Infra topic may have just been made.
	u.rmPin()
	if u.rm.again && len(failed) == 0 { // a failure waits for the next change: no loop on a dead network
		u.rm.again = false
		u.remoteKick()
	}
}

// remotePlan is each card's job, on the UI goroutine.
func (u *ui) remotePlan() (jobs []*rmJob) {
	seen := map[string]bool{}
	for ci, col := range u.b.Columns {
		for i := range col.Cards {
			c := &u.b.Columns[ci].Cards[i]
			seen[c.ID] = true
			if j := u.rmCard(c, col.Title, false); j != nil {
				jobs = append(jobs, j)
			}
		}
	}
	for i := range u.b.Archive {
		c := &u.b.Archive[i]
		seen[c.ID] = true
		if j := u.rmCard(c, "", true); j != nil {
			jobs = append(jobs, j)
		}
	}
	jobs = append(jobs, u.rmInfras(seen)...)
	jobs = append(jobs, u.rmGeneral(seen)...)
	for id, t := range u.rm.topics { // cards deleted: their topics go
		if !seen[id] {
			jobs = append(jobs, &rmJob{id: id, t: *t, act: "delete"})
		}
	}
	return jobs
}

// rmCard is card c's job (nil: nothing to do); col is its column's title,
// archived whether it's in the archive.
func (u *ui) rmCard(c *kanban.Card, col string, archived bool) *rmJob {
	mode, chat := u.rmMirror(c)
	t := u.rm.topics[c.ID]
	msgs := u.chat(c.ID)
	if t == nil && (mode == orgs.MirrorOff || archived || col == "Done" || len(msgs) == 0) {
		return nil
	}
	j := &rmJob{id: c.ID}
	if t == nil {
		j.create = true
		j.t = rmTopic{Chat: chat, Sent: max(0, len(msgs)-1)} // a card from before: its last message for context
	} else {
		j.t = *t
	}
	if archived || col == "Done" { // done work leaves the topic list: the General chat and the desk Claude still reach it
		return &rmJob{id: c.ID, t: j.t, act: "delete"}
	}
	if j.t.Closed { // closed by an older desk
		j.act = "reopen"
	}
	if name := rmName(c); name != j.t.Name {
		j.name = name
	}
	if head := rmHead(c, col); head != j.t.Head {
		j.head = head
	}
	j.sent, j.said = len(msgs), j.t.Said
	if mode != orgs.MirrorOff && j.t.Sent < len(msgs) {
		j.posts = u.rmPosts(c.ID, mode, msgs[j.t.Sent:])
	}
	if mode == orgs.MirrorNotify && c.Status != kanban.StatusWorking && c.Status != kanban.StatusIdle && j.t.Said != c.Status+c.Note {
		j.said = c.Status + c.Note
		if c.Status != kanban.StatusDone {
			j.posts = append(j.posts, rmEmoji(c.Status)+" **"+tgfmt.Escape(statusWords(c))+"**\n\n"+tgfmt.Redact(cmp.Or(c.Last, c.Note)))
		}
	}
	a := u.asks[c.ID] // its own request first, then its infra session's approval
	tab, ia := u.infraAsk(c.Key())
	cur := ""
	switch {
	case a != nil:
		cur = a.r.ID
	case ia != nil:
		cur = fmt.Sprint("infra:", ia.ID)
	}
	if cur != "" && cur != j.t.Ask && mode != orgs.MirrorOff {
		if a != nil {
			j.ask = u.rmAsk(c, a)
		} else {
			j.ask = u.rmApproval(tab, *ia)
		}
		j.askID = cur
	}
	if j.t.Ask != "" && cur != j.t.Ask {
		j.askEnd = j.t.AskText + "\n\n→ answered"
	}
	if !j.create && j.act == "" && j.name == "" && j.head == "" && len(j.posts) == 0 && j.ask == nil && j.askEnd == "" && j.sent == j.t.Sent && j.said == j.t.Said {
		return nil
	}
	return j
}

// statusWords is the card's state as a short sentence.
func statusWords(c *kanban.Card) string {
	s := map[string]string{kanban.StatusWaiting: "Your turn", kanban.StatusBlocked: "Blocked", kanban.StatusFailed: "Failed",
		kanban.StatusReview: "Ready for review", kanban.StatusWorking: "Working", kanban.StatusDone: "Done"}[c.Status]
	if s == "" {
		s = "Idle"
	}
	if c.Note != "" {
		s += ": " + c.Note
	}
	return s
}

// rmEmoji is a status as the topic's name starts.
func rmEmoji(status string) string {
	switch status {
	case kanban.StatusWorking:
		return "🟢"
	case kanban.StatusWaiting:
		return "🟡"
	case kanban.StatusBlocked:
		return "🔴"
	case kanban.StatusFailed:
		return "❌"
	case kanban.StatusReview:
		return "👀"
	case kanban.StatusDone:
		return "✅"
	}
	return "⏸"
}

// rmName is card c's topic name: its state, key and ticket, short enough
// to read in a phone's topic list (the title is in the pinned header).
func rmName(c *kanban.Card) string {
	name := rmEmoji(c.Status) + " " + c.Key()
	if t := ticketOf(c); t != nil && t.Ref != "" {
		name += " :: " + t.Ref
	}
	return tgfmt.Clip(name, 128)
}

// rmHead is card c's pinned header: what it is, where, its links, cost.
func rmHead(c *kanban.Card, col string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s\n\n%s\n\n", c.Key(), tgfmt.Escape(c.Title))
	where := col
	if where == "" {
		where = "Archived"
	}
	fmt.Fprintf(&b, "%s **%s** · %s\n", rmEmoji(c.Status), tgfmt.Escape(where), tgfmt.Escape(statusWords(c)))
	for _, l := range c.Links {
		s := "- [" + tgfmt.Escape(cmp.Or(l.Ref, l.URL)) + "](" + l.URL + ")"
		if l.State != "" {
			s += " · " + tgfmt.Escape(l.State)
		}
		b.WriteString("\n" + s)
	}
	if s := c.Session; s.Turns > 0 {
		foot := fmt.Sprintf("$%.2f · %d turns", s.Cost, s.Turns)
		if s.Window > 0 && s.Context > 0 {
			foot += fmt.Sprintf(" · context %d%%", s.Context*100/s.Window)
		}
		b.WriteString("\n\n<footer>" + foot + "</footer>")
	}
	return strings.TrimSpace(b.String())
}

// rmPosts are new chat messages as the mode shows them, packed into as few
// rich posts as fit (every post a message of its own, paced): a divider
// between messages, who said it in bold, a run of tools folded away.
func (u *ui) rmPosts(id, mode string, msgs []kanban.ChatMsg) []string {
	var parts []string
	var tools []string
	flushTools := func() {
		if len(tools) > 0 {
			what := fmt.Sprintf("🔧 %d tools", len(tools))
			if len(tools) == 1 {
				what = "🔧 " + tgfmt.Escape(tgfmt.Clip(tools[0], 48))
			}
			parts = append(parts, tgfmt.Details(what, tgfmt.Fence("", clipKeep(strings.Join(tools, "\n"), 3000))))
			tools = nil
		}
	}
	for _, m := range msgs {
		text := tgfmt.Redact(strings.TrimSpace(m.Text))
		switch {
		case m.Who == "you" && u.rm.from[id] != "" && m.Text == u.rm.from[id]:
			delete(u.rm.from, id) // it came from the topic
			continue
		case text == "":
			continue
		case m.Who == "tool":
			if mode == orgs.MirrorFull {
				tools = append(tools, text)
			}
			continue
		case mode == orgs.MirrorNotify:
			continue
		}
		flushTools()
		switch m.Who {
		case "you":
			parts = append(parts, "**🧑 You** · _in desk_\n\n"+text)
		case "claude":
			parts = append(parts, text)
		case "error":
			parts = append(parts, "**⚠️ Error**\n\n"+text)
		default: // a peer, the workspace, a lead, a shell
			parts = append(parts, "**💬 "+tgfmt.Escape(cmp.Or(m.Name, m.Who))+"**\n\n"+text)
		}
	}
	flushTools()
	return rmPack(parts)
}

// rmPack joins parts with a divider into posts of at most tgfmt.RichMax,
// cutting only a part too long for one.
func rmPack(parts []string) (posts []string) {
	const sep = "\n\n---\n\n"
	cur := ""
	for _, p := range parts {
		for _, piece := range tgfmt.Split(p, tgfmt.RichMax) {
			if cur != "" && tgfmt.Len(cur)+tgfmt.Len(sep)+tgfmt.Len(piece) > tgfmt.RichMax {
				posts, cur = append(posts, cur), ""
			}
			if cur == "" {
				cur = piece
			} else {
				cur += sep + piece
			}
		}
	}
	if cur != "" {
		posts = append(posts, cur)
	}
	return posts
}

// rmAsk is card c's request as a message with its buttons: Allow once,
// Deny, Allow every <tool>; a lone question's options.
func (u *ui) rmAsk(c *kanban.Card, a *askState) *tgbot.Out {
	o := &tgbot.Out{}
	if len(a.r.Questions) == 0 {
		lang := ""
		if a.r.Tool == "Bash" {
			lang = "sh"
		}
		o.Markdown = "### 🔐 Wants to run\n\n" + tgfmt.Fence(lang, tgfmt.Redact(a.r.Line))
		allow, deny := u.phoneButton("Allow once", phoneAsk{c.ID, a.r.ID, true, -1, false}), u.phoneButton("Deny", phoneAsk{c.ID, a.r.ID, false, -1, false})
		allow.Style, deny.Style = "success", "danger"
		every := u.phoneButton("Allow every "+cmp.Or(a.r.Tool, "use"), phoneAsk{c.ID, a.r.ID, true, -1, true})
		o.Rows = [][]tgbot.Button{{allow, deny}, {every}}
		return o
	}
	o.Markdown = "### ❓ A question\n\n" + tgfmt.Redact(interviewText(a.r.Questions))
	if q := a.r.Questions[0]; len(a.r.Questions) == 1 && !q.Multi {
		for j, opt := range q.Options {
			o.Rows = append(o.Rows, []tgbot.Button{u.phoneButton(opt.Label, phoneAsk{c.ID, a.r.ID, true, j, false})})
		}
	}
	o.Markdown += "\n\n_Or write your answer here._"
	return o
}

// rmRun sends job j, keeping in j.t what went; it stops at a failure.
func rmRun(ctx context.Context, bot *tgbot.Bot, j *rmJob) {
	t := &j.t
	fail := func(what string, err error) bool {
		if err == nil || tgbot.NotModified(err) {
			return false
		}
		j.failure = what + ": " + firstLine(err.Error())
		return true
	}
	if j.act == "delete" {
		fail("delete a topic", bot.Topic(ctx, t.Chat, t.Thread, "delete"))
		return
	}
	if j.create {
		th, err := bot.NewTopic(ctx, t.Chat, j.name, 0)
		if fail("make a topic", err) {
			return
		}
		t.Thread, t.Name, j.name = th, j.name, ""
		hd, err := bot.Post(ctx, tgbot.Out{Chat: t.Chat, Thread: th, Markdown: j.head, Silent: true})
		if fail("send the header", err) {
			return
		}
		t.Header, t.Head, j.head = hd, j.head, ""
		if fail("pin the header", bot.Pin(ctx, t.Chat, hd)) {
			return
		}
	}
	if j.act == "reopen" {
		if fail("reopen a topic", bot.Topic(ctx, t.Chat, t.Thread, "reopen")) {
			return
		}
		t.Closed = false
	}
	if j.name != "" {
		if fail("rename a topic", bot.Rename(ctx, t.Chat, t.Thread, j.name)) {
			return
		}
		t.Name = j.name
	}
	if j.head != "" {
		if fail("change the header", bot.Change(ctx, t.Header, tgbot.Out{Chat: t.Chat, Markdown: j.head})) {
			return
		}
		t.Head = j.head
	}
	for _, p := range j.posts {
		if _, err := bot.Post(ctx, tgbot.Out{Chat: t.Chat, Thread: t.Thread, Markdown: p}); fail("send a message", err) {
			return
		}
	}
	t.Sent, t.Said = j.sent, j.said
	if j.askEnd != "" {
		if fail("close a request", bot.Change(ctx, t.AskMsg, tgbot.Out{Chat: t.Chat, Markdown: j.askEnd})) {
			return
		}
		t.Ask, t.AskMsg, t.AskText = "", 0, ""
	}
	if j.ask != nil {
		o := *j.ask
		o.Chat, o.Thread = t.Chat, t.Thread
		msg, err := bot.Post(ctx, o)
		if fail("send a request", err) {
			return
		}
		t.Ask, t.AskMsg, t.AskText = j.askID, msg, o.Markdown
	}
}

// rmIncoming is what the user typed in card id's topic: /stop interrupts
// its Claude, anything else goes to it as the user's message.
func (u *ui) rmIncoming(id string, m *tgbot.Message) {
	text := strings.TrimSpace(m.Text)
	bot, chat, thread, msg := u.phone.bot, m.Chat.ID, m.Thread, m.ID
	reply := func(s string) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			bot.Post(ctx, tgbot.Out{Chat: chat, Thread: thread, Text: s})
		}()
	}
	took := u.tgTake(m, reply, func(text, pic, _ string) {
		u.rm.from[id] = text
		if pic != "" {
			u.deliver(id, text, false, pic)
		} else {
			u.deliver(id, text, false)
		}
		go bot.React(context.Background(), chat, msg, "👀")
	})
	switch {
	case took:
	case text == "":
		reply("Text, pictures, files and voice notes reach the card's Claude from here.")
	case strings.HasPrefix(text, "/stop"):
		u.stop(id)
		reply("Stopping its turn.")
	case strings.HasPrefix(text, "/drawings"):
		u.tgDrawings(chat, thread, u.b.Card(id).Org, id)
	case strings.HasPrefix(text, "/review"):
		u.tgReview(chat, thread, id)
	case strings.HasPrefix(text, "/canvas"):
		u.tgCanvas(chat, thread, id)
	case strings.HasPrefix(text, "/help"), strings.HasPrefix(text, "/start"):
		reply("Write here and it goes to this card's Claude as your message. /stop stops its turn, /review shows its branch, /canvas its canvas, /drawings its drawings. Buttons answer its requests.")
	default:
		u.rm.from[id] = text
		u.send(id, text)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			bot.React(ctx, chat, msg, "👀")
		}()
	}
}
