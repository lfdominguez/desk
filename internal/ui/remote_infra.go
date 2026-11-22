package ui

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/tgbot"
	"github.com/lfdominguez/desk/internal/tgfmt"
)

// Desk Control's Infra topic: a mirrored organization with an infra
// session gets one topic (its key "infra:<org>" among the cards'), named
// by the infra Claudes' state, its pinned header each tab with what its
// terminals run; its own Claude's replies follow (not in Notify), and each
// command or change waiting for approval comes with Approve, Deny and
// Always. What's typed there goes to the organization's infra Claude;
// /screen shows a terminal when Settings allows it. A card's infra
// session's news already reaches the card's topic, as peer messages.

// infraAnswer and infraSay reach the infra workspace; tests replace them.
var (
	infraAnswer = (*infra.Workspace).Answer
	infraSay    = (*infra.Workspace).Say
)

// rmInfraKey is org's Infra topic's key in the topics.
func rmInfraKey(org string) string { return "infra:" + org }

// infraStatus takes the infra tabs' status (infra.Workspace.OnStatus) and
// catches the topics up.
func (u *ui) infraStatus(ss []infra.TabStatus) {
	u.infraStat = ss
	u.remoteKick()
}

// rmInfras is each mirrored organization's Infra topic's job; seen gets
// their keys.
func (u *ui) rmInfras(seen map[string]bool) (jobs []*rmJob) {
	for org, o := range u.rm.orgs {
		seen[rmInfraKey(org)] = true
		if j := u.rmInfra(org, o); j != nil {
			jobs = append(jobs, j)
		}
	}
	return jobs
}

// rmInfra is org's Infra topic's job (nil: nothing to do).
func (u *ui) rmInfra(org string, o orgs.TelegramOrg) *rmJob {
	var tabs []infra.TabStatus
	for _, s := range u.infraStat {
		if s.Org == org {
			tabs = append(tabs, s)
		}
	}
	key := rmInfraKey(org)
	t := u.rm.topics[key]
	if o.Chat == 0 || o.Mirror == orgs.MirrorOff || t == nil && len(tabs) == 0 {
		return nil
	}
	j := &rmJob{id: key}
	if t == nil {
		j.create, j.t = true, rmTopic{Chat: o.Chat}
	} else {
		j.t = *t
	}
	emoji := "⏸"
	var first *infra.Approval
	var from string
	var b strings.Builder
	fmt.Fprintf(&b, "🖥 **Infra · %s**\n", tgfmt.Escape(org))
	for _, s := range tabs {
		mark := map[string]string{"approval": "🔐", "working": "🟢", "your turn": "🟡"}[s.State]
		switch {
		case s.State == "approval":
			emoji = "🔐"
		case s.State == "working" && emoji != "🔐":
			emoji = "🟢"
		}
		fmt.Fprintf(&b, "\n%s **%s** · %s\n", cmp.Or(mark, "⏸"), tgfmt.Escape(s.Name), tgfmt.Escape(s.State))
		for _, p := range s.Panes {
			fmt.Fprintf(&b, "- `%s` %s\n", strings.ReplaceAll(p.Name, "`", "'"), tgfmt.Escape(p.Status))
		}
		if first == nil && len(s.Pending) > 0 {
			first, from = &s.Pending[0], s.Name
		}
		if s.Card == "" && s.Last != "" && o.Mirror != orgs.MirrorNotify && s.Last != j.t.Said {
			j.said = s.Last
			j.posts = tgfmt.Split(tgfmt.Redact(s.Last), tgfmt.Max-96)
		}
	}
	if len(tabs) == 0 {
		b.WriteString("\n_No infra session open._")
	}
	if j.said == "" {
		j.said = j.t.Said
	}
	if name := emoji + " Infra"; name != j.t.Name {
		j.name = name
	}
	if head := strings.TrimSpace(b.String()); head != j.t.Head {
		j.head = head
	}
	j.sent = j.t.Sent
	if first != nil && fmt.Sprint("infra:", first.ID) != j.t.Ask {
		j.ask, j.askID = u.rmApproval(from, *first), fmt.Sprint("infra:", first.ID)
	}
	if j.t.Ask != "" && (first == nil || fmt.Sprint("infra:", first.ID) != j.t.Ask) {
		j.askEnd = j.t.AskText + "\n\n→ answered"
	}
	if !j.create && j.name == "" && j.head == "" && len(j.posts) == 0 && j.ask == nil && j.askEnd == "" && j.said == j.t.Said {
		return nil
	}
	return j
}

// clipKeep is s cut to n runes with … where it was cut, its lines kept
// (tgfmt.Clip makes one line).
func clipKeep(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// rmApproval is approval a of tab as a message with Approve, Deny and,
// unless it's Fixed, Always (this organization).
func (u *ui) rmApproval(tab string, a infra.Approval) *tgbot.Out {
	o := &tgbot.Out{}
	what := tgfmt.Escape(tab) + " wants to run"
	if a.Host != "" {
		what += " on `" + strings.ReplaceAll(a.Host, "`", "'") + "`"
	}
	body := tgfmt.Fence("sh", tgfmt.Redact(a.Cmd))
	if a.Title != "" {
		what, body = tgfmt.Escape(a.Title), tgfmt.Fence("diff", clipKeep(tgfmt.Redact(a.Cmd), 3000))
	}
	o.Markdown = "🔐 **" + what + "**\n\n"
	if a.Reason != "" {
		o.Markdown += tgfmt.Escape(a.Reason) + "\n\n"
	}
	o.Markdown += body
	id, w := a.ID, u.infra
	answer := func(v, said string) func() string {
		return func() string { infraAnswer(w, id, v); return said }
	}
	row := []tgbot.Button{u.gnButton("Approve", "success", answer("y", "Approved")), u.gnButton("Deny", "danger", answer("n", "Denied"))}
	if !a.Fixed {
		row = append(row, u.gnButton("Always", "", answer("a", "Always allowed in this organization")))
	}
	o.Rows = [][]tgbot.Button{row}
	return o
}

// rmInfraIncoming is what the user typed in org's Infra topic: /screen,
// /help, else a message for the organization's infra Claude.
func (u *ui) rmInfraIncoming(org string, m *tgbot.Message) {
	text := strings.TrimSpace(m.Text)
	bot, chat, thread, msg := u.phone.bot, m.Chat.ID, m.Thread, m.ID
	post := func(o tgbot.Out) {
		o.Chat, o.Thread = chat, thread
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			bot.Post(ctx, o)
		}()
	}
	switch {
	case text == "":
		post(tgbot.Out{Text: "Only text reaches the infra Claude from here, for now."})
	case strings.HasPrefix(text, "/screen"):
		post(u.rmScreen(org, strings.TrimSpace(strings.TrimPrefix(text, "/screen"))))
	case strings.HasPrefix(text, "/help"), strings.HasPrefix(text, "/start"):
		post(tgbot.Out{Text: "Write here and it goes to " + org + "'s infra Claude. Buttons answer what it waits on. /screen shows a terminal (/screen web1), when Settings → Telegram allows it."})
	case !infraSay(u.infra, org, text):
		post(tgbot.Out{Text: "desk has no infra for " + org + " now."})
	default:
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			bot.React(ctx, chat, msg, "👀")
		}()
	}
}

// rmScreen is a terminal of org's infra as text: the pane named host, else
// the first one running something, else the first.
func (u *ui) rmScreen(org, host string) tgbot.Out {
	if !orgs.PhoneSettings().Screen {
		return tgbot.Out{Text: "/screen is off: turn on \"Let /screen show a terminal\" in desk's Settings → Telegram."}
	}
	var pick *infra.PaneStatus
	for _, s := range u.infraStat {
		for i := range s.Panes {
			p := &s.Panes[i]
			switch {
			case s.Org != org:
			case host != "" && strings.EqualFold(p.Name, host):
				pick = p
			case host == "" && pick == nil, host == "" && strings.HasPrefix(p.Status, "running") && !strings.HasPrefix(pick.Status, "running"):
				pick = p
			}
		}
	}
	if pick == nil {
		return tgbot.Out{Text: "No terminal open in " + org + "'s infra" + map[bool]string{true: "", false: " called " + host}[host == ""] + "."}
	}
	scr := strings.TrimRight(pick.Screen(), "\n ")
	return tgbot.Out{Markdown: "🖥 `" + strings.ReplaceAll(pick.Name, "`", "'") + "` · " + tgfmt.Escape(pick.Status) + "\n\n" + tgfmt.Fence("", clipKeep(tgfmt.Redact(scr), 3500))}
}
