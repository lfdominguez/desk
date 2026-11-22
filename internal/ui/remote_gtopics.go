package ui

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/tgfmt"
)

// The General chat's own topics, once BotFather's topics mode is on for
// the bot (getMe's has_topics_enabled): Board, Inbox, Digest and
// Settings, each with a pinned header that follows the board (kept among
// the cards' topics as "general:<name>"). Cards that need the user come to
// Inbox; a command or a question typed in a topic is answered there.
// Without the mode, it's all one chat.

// gtopics are the General chat's topics, by key.
var gtopics = []struct{ key, name string }{
	{"board", "📋 Board"}, {"inbox", "📥 Inbox"}, {"digest", "📰 Digest"}, {"settings", "⚙️ Settings"},
}

// gtKey is a General topic's key among the topics.
func gtKey(k string) string { return "general:" + k }

// gtHeadMax is how much of a header goes in its message.
var gtHeadMax = tgfmt.Max - 200

// phoneTopics asks the bot whether its private chats have topics, then
// catches the topics up.
func (u *ui) phoneTopics() {
	bot := u.phone.bot
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		me, err := bot.Me(ctx)
		u.later(func() {
			if err == nil && me.Topics && u.phone != nil {
				u.phone.topics = true
				u.remoteKick()
			}
		})
	}()
}

// gtThread is General topic k's thread, 0 (the chat itself) without one.
func (u *ui) gtThread(k string) int64 {
	if u.rm == nil || !u.phone.topics {
		return 0
	}
	if t := u.rm.topics[gtKey(k)]; t != nil && t.Chat == u.phone.chat {
		return t.Thread
	}
	return 0
}

// gtHead is General topic k's header.
func (u *ui) gtHead(k string) string {
	var b string
	switch k {
	case "board":
		b = u.gnBoard("")
	case "inbox":
		b = u.gnInbox()
	case "digest":
		b = u.digestText("", digestSince(0, time.Now()))
	default:
		b = u.gnSettings()
	}
	return clipLines(strings.TrimSpace(tgfmt.Redact(b)), gtHeadMax)
}

// clipLines is s cut at a line's end to at most n runes, "…" on a line of
// its own where it was cut: no markup is cut in two.
func clipLines(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)[:n-2]
	if i := strings.LastIndexByte(string(r), '\n'); i > 0 {
		return string(r)[:i] + "\n…"
	}
	return "…"
}

// gnSettings is what Desk Control does now, as the Settings topic says it.
func (u *ui) gnSettings() string {
	var b strings.Builder
	onOff := map[bool]string{true: "on", false: "off"}
	fmt.Fprintf(&b, "**Settings**\n\n- Messages while desk has the focus: **%s** · /always on or off\n", onOff[u.phone.always])
	fmt.Fprintf(&b, "- /screen in Infra topics: **%s** · Settings → Telegram\n", onOff[u.rm.screen])
	b.WriteString("\n### Organizations\n")
	for _, o := range u.orgList {
		mode := cmp.Or(u.rm.orgs[o].Mirror, "off")
		if u.rm.orgs[o].Chat == 0 {
			mode = "no group"
		}
		fmt.Fprintf(&b, "- %s · %s\n", tgfmt.Escape(o), tgfmt.Escape(mode))
	}
	b.WriteString("\n" + tgfmt.Escape("/mirror Acme full, summary, notify or off changes one."))
	return b.String()
}

// rmGeneral is the General topics' jobs; seen gets their keys, so they
// stay even while the mode is off.
func (u *ui) rmGeneral(seen map[string]bool) (jobs []*rmJob) {
	ph := u.phone
	for _, g := range gtopics {
		key := gtKey(g.key)
		seen[key] = true
		t := u.rm.topics[key]
		if !ph.topics || ph.chat == 0 || t != nil && t.Chat != ph.chat {
			continue
		}
		j := &rmJob{id: key}
		if t == nil {
			j.create, j.t = true, rmTopic{Chat: ph.chat}
		} else {
			j.t = *t
		}
		if g.name != j.t.Name {
			j.name = g.name
		}
		if head := u.gtHead(g.key); head != j.t.Head {
			j.head = head
		}
		j.sent, j.said = j.t.Sent, j.t.Said
		if j.create || j.name != "" || j.head != "" {
			jobs = append(jobs, j)
		}
	}
	return jobs
}
