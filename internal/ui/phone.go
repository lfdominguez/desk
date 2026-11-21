package ui

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/tgbot"
)

// The phone: settings.toml's [phone] Telegram bot brings what wants the
// user (a card's permission request or question, its turn, a block) to
// their phone while desk isn't in front of them. Buttons answer a request;
// a reply to a card's message is sent to it as the user's.

// pairFor is how long after desk starts a /start may pair a chat.
const pairFor = 10 * time.Minute

// phoneText caps a message's text (Telegram takes 4096).
const phoneText = 3500

type phoneState struct {
	bot       *tgbot.Bot
	chat      int64
	always    bool
	pairUntil time.Time
	next      int
	asks      map[string]phoneAsk // a button's data → what it answers
	cards     map[int64]string    // a message sent → its card's id, for replies
}

// phoneAsk is one button: card's request req, allowed or not, or answered
// with option (-1 none).
type phoneAsk struct {
	card, req string
	allow     bool
	option    int
}

// phoneBot makes the bot for settings p; tests replace it.
var phoneBot = func(token string) *tgbot.Bot { return &tgbot.Bot{Token: token} }

// startPhone starts the phone's bot when settings.toml has one; what it
// returns stops its polling.
func (u *ui) startPhone() (stop func()) {
	ctx, stop := context.WithCancel(context.Background())
	p := orgs.PhoneSettings()
	tok, err := p.Get()
	if err != nil || tok == "" {
		return stop
	}
	u.phone = &phoneState{bot: phoneBot(tok), chat: p.Chat, always: p.Always, pairUntil: time.Now().Add(pairFor),
		asks: map[string]phoneAsk{}, cards: map[int64]string{}}
	go u.phonePoll(ctx, u.phone.bot)
	return stop
}

// phoneSettled takes settings.toml's pairing and always into the running
// bot, after Settings saved them; unpaired, a /start pairs again for
// pairFor. A new token needs desk started again.
func (u *ui) phoneSettled() {
	ph := u.phone
	if ph == nil {
		return
	}
	p := orgs.PhoneSettings()
	if p.Chat == 0 && ph.chat != 0 {
		ph.pairUntil = time.Now().Add(pairFor)
	}
	ph.chat, ph.always = p.Chat, p.Always
}

// phonePoll hands each update to the UI goroutine until ctx ends; a failed
// poll waits a little, then tries again.
func (u *ui) phonePoll(ctx context.Context, bot *tgbot.Bot) {
	var offset int64
	for {
		us, err := bot.Updates(ctx, offset)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(phoneRetry):
			}
			continue
		}
		for _, up := range us {
			offset = up.ID + 1
			u.later(func() { u.phoneUpdate(up) })
		}
	}
}

// phoneRetry is how long a failed poll waits.
var phoneRetry = 5 * time.Second

// phoneUpdate acts on one update, on the UI goroutine: the pairing
// /start, a button pressed, a reply to a card's message.
func (u *ui) phoneUpdate(up tgbot.Update) {
	ph := u.phone
	m := up.Message
	if up.Callback != nil {
		m = up.Callback.Message
	}
	if ph == nil || m == nil {
		return
	}
	if t := m.Chat.Type; t == "group" || t == "supergroup" {
		if up.Message != nil && ph.chat != 0 && m.From.ID == ph.chat && strings.HasPrefix(m.Text, "/link") {
			u.phoneLink(m) // only the paired chat's own user: a private chat's id is its user's
		}
		return
	}
	if ph.chat == 0 {
		if strings.HasPrefix(m.Text, "/start") && time.Now().Before(ph.pairUntil) {
			ph.chat = m.Chat.ID
			if err := orgs.SetPhoneChat(ph.chat); err != nil {
				u.showToast("Phone: "+err.Error(), true)
			}
			u.showToast("Phone paired: Telegram chat "+strconv.FormatInt(ph.chat, 10), false)
			u.phoneSend("desk is paired with this chat: cards that need you come here.", nil, "")
		}
		return
	}
	if m.Chat.ID != ph.chat { // anyone else who found the bot
		return
	}
	if cb := up.Callback; cb != nil {
		said := u.phonePress(ph.asks[cb.Data])
		bot, chat, msg := ph.bot, ph.chat, m.ID
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			bot.Answer(ctx, cb.ID, said)
			bot.Edit(ctx, chat, msg, m.Text+"\n\n→ "+said)
		}()
		return
	}
	id := ""
	if m.Reply != nil {
		id = ph.cards[m.Reply.ID]
	}
	c := u.b.Card(id)
	if c == nil || strings.TrimSpace(m.Text) == "" {
		u.phoneSend("Reply to a card's message to write to its Claude.", nil, "")
		return
	}
	key := c.Key() // before send: moving the card to In progress moves its slot
	u.send(id, m.Text)
	u.phoneSend("Sent to "+key+".", nil, "")
}

// phonePress answers the request a button stands for, if it still waits;
// what happened, for the phone.
func (u *ui) phonePress(pa phoneAsk) string {
	a := u.asks[pa.card]
	if pa.card == "" || a == nil || a.r.ID != pa.req {
		return "Already answered"
	}
	if pa.option >= 0 {
		a.picks[0][pa.option] = true
		u.answerAsk(pa.card, true)
		return "Answered: " + a.r.Questions[0].Options[pa.option].Label
	}
	u.answerAsk(pa.card, pa.allow)
	return map[bool]string{true: "Allowed", false: "Denied"}[pa.allow]
}

// phoneTell sends card c's news to the phone (from notifyChange): its
// request with buttons, else what it said; skipped while a desk window has
// the focus, unless [phone] always.
func (u *ui) phoneTell(c *kanban.Card, what string) {
	ph := u.phone
	if ph == nil || ph.chat == 0 || !ph.always && u.anyFocused() {
		return
	}
	text := c.Key() + " · " + what + "\n" + c.Title
	var rows [][]tgbot.Button
	if a := u.asks[c.ID]; a != nil {
		switch {
		case len(a.r.Questions) == 0:
			text += "\n\nWants to run " + a.r.Line
			rows = [][]tgbot.Button{{u.phoneButton("Allow", phoneAsk{c.ID, a.r.ID, true, -1}), u.phoneButton("Deny", phoneAsk{c.ID, a.r.ID, false, -1})}}
		default:
			q := a.r.Questions[0]
			text += "\n\n" + interviewText(a.r.Questions)
			if len(a.r.Questions) == 1 && !q.Multi {
				for j, o := range q.Options {
					rows = append(rows, []tgbot.Button{u.phoneButton(o.Label, phoneAsk{c.ID, a.r.ID, true, j})})
				}
			}
			text += "\n\nReply to this message to answer in your own words."
		}
	} else if s := unmark(cmp.Or(c.Last, c.Note)); s != "" {
		text += "\n\n" + s + "\n\nReply to this message to answer."
	}
	if r := []rune(text); len(r) > phoneText {
		text = string(r[:phoneText-1]) + "…"
	}
	u.phoneSend(text, rows, c.ID)
}

// phoneButton is a button standing for pa.
func (u *ui) phoneButton(label string, pa phoneAsk) tgbot.Button {
	u.phone.next++
	data := fmt.Sprint("p", u.phone.next)
	u.phone.asks[data] = pa // ponytail: never pruned; a few per request while desk runs
	return tgbot.Button{Text: label, Data: data}
}

// phoneSend sends text off the UI goroutine; a card's message is kept for
// the replies to it.
func (u *ui) phoneSend(text string, rows [][]tgbot.Button, card string) {
	bot, chat := u.phone.bot, u.phone.chat
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		id, err := bot.Send(ctx, chat, text, rows)
		u.later(func() {
			if err != nil {
				u.showToast("Phone: "+err.Error(), true)
			} else if card != "" {
				u.phone.cards[id] = card
			}
		})
	}()
}

// anyFocused says whether one of desk's windows has the focus.
func (u *ui) anyFocused() bool {
	if u.mainPane.focused {
		return true
	}
	for _, d := range u.detached {
		if d.p.focused {
			return true
		}
	}
	return false
}

// phoneLink is /link Acme in a group the user made: that forum group is
// the organization's in Desk Control, mirrored fully unless Settings says
// how already. A group desk didn't make can't go in the folder.
func (u *ui) phoneLink(m *tgbot.Message) {
	name := strings.TrimSpace(strings.TrimPrefix(m.Text, "/link"))
	if strings.HasPrefix(name, "@") { // /link@desk_bot Acme
		_, name, _ = strings.Cut(name, " ")
		name = strings.TrimSpace(name)
	}
	i := slices.IndexFunc(u.orgList, func(o string) bool { return strings.EqualFold(o, name) })
	var reply string
	switch {
	case name == "":
		reply = "Say which organization: /link Acme"
	case i < 0:
		reply = "desk has no organization called " + name + ". It has: " + strings.Join(u.orgList, ", ") + "."
	case !m.Chat.IsForum:
		reply = "Turn on Topics in this group's settings first, then /link again: each card gets a topic."
	default:
		org := u.orgList[i]
		o := orgs.TelegramSettings().Orgs[org]
		if o.Chat != m.Chat.ID {
			o.Channel, o.Access = 0, 0
		}
		o.Chat = m.Chat.ID
		if o.Mirror == orgs.MirrorOff {
			o.Mirror = orgs.MirrorFull
		}
		if err := orgs.SetTelegramOrg(org, o); err != nil {
			reply = "Not linked: " + firstLine(err.Error())
			break
		}
		reply = "Linked: this group is " + org + "'s in desk. Its cards get a topic each."
		u.showToast("Telegram: a group linked to "+org, false)
	}
	bot, chat, thread := u.phone.bot, m.Chat.ID, m.Thread
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		bot.Post(ctx, tgbot.Out{Chat: chat, Thread: thread, Text: reply})
	}()
}
