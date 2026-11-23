package ui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/tgbot"
	"github.com/lfdominguez/desk/internal/tgfmt"
)

// The board as an HTML widget in the private chat (Bot API 10.4: widgets
// go only to private chats): /widget sends it; its chat data follows the
// board (tgWidgetSync after each save) and a card tapped in it asks desk
// to post that card, as /card does.

// tgWidget is the widget message the board's data goes to, kept over
// restarts in tgWidgetFile.
type tgWidget struct {
	thread int64
	msg    int64  // 0: being sent
	data   string // the chat data last sent
	busy   bool   // an edit is under way
	again  bool   // the board changed meanwhile
}

// tgWidgetMax is how many waiting cards the widget lists; its chat data
// holds 1024 bytes, so fewer go when titles are long.
var tgWidgetMax = 8

// tgWidgetHTML is the widget: columns with their counts, then the cards
// waiting on the user as buttons, drawn from the chat data and again on
// each change. Text goes in with textContent: titles are the user's.
const tgWidgetHTML = `<style>
body{margin:0;font:14px system-ui,sans-serif;color:var(--tg-theme-text-color,#222)}
.cols{display:flex;flex-wrap:wrap;gap:6px;padding:8px}
.col{flex:1 1 30%;padding:6px 8px;border-radius:8px;background:var(--tg-theme-secondary-bg-color,#f1f1f1)}
.n{font-size:20px;font-weight:600}.t{font-size:12px;opacity:.7}
h4{margin:6px 8px 2px;font-size:13px}#w{padding:0 8px 8px;display:flex;flex-direction:column;gap:4px}
button{width:100%;text-align:left}#m{padding:0 8px 6px;font-size:12px;opacity:.7}
</style>
<div class="cols" id="c"></div><h4 id="h"></h4><div id="w"></div><div id="m"></div>
<script>
const W = window.TelegramWidget, $ = (id) => document.getElementById(id);
function el(tag, cls, text) { const e = document.createElement(tag); if (cls) e.className = cls; e.textContent = text; return e; }
function render() {
  let d = null;
  try { d = JSON.parse(W.chatData); } catch (e) {}
  d = d || {c: [], n: []};
  $('c').innerHTML = ''; $('w').innerHTML = '';
  for (const [name, n] of d.c) { const b = el('div', 'col', ''); b.append(el('div', 'n', String(n)), el('div', 't', name)); $('c').append(b); }
  $('h').textContent = d.n.length ? 'Waiting on you' : 'Nothing waits on you';
  for (const [key, title, why] of d.n) {
    const b = el('button', '', key + ' · ' + title + (why ? ' (' + why + ')' : ''));
    b.addEventListener('click', () => {
      $('m').textContent = 'Asking desk…';
      W.request('c:' + key).then((a) => { $('m').textContent = a.result || ''; }, (e) => { $('m').textContent = 'Not sent: ' + e.code; });
    });
    $('w').append(b);
  }
  W.ready();
}
W.onEvent('chatDataChanged', render);
render();
</script>`

// tgWidgetData is the board as the widget's chat data: each column's count
// and the cards waiting on the user, at most 1024 bytes.
func (u *ui) tgWidgetData() string {
	type data struct {
		C [][2]any    `json:"c"`
		N [][3]string `json:"n"`
	}
	var d data
	for _, col := range u.b.Columns {
		n := 0
		for _, c := range col.Cards {
			if c.Parent == "" {
				n++
			}
		}
		d.C = append(d.C, [2]any{col.Title, n})
	}
	for _, c := range u.boardCards() {
		why := map[string]string{kanban.StatusWaiting: "your turn", kanban.StatusBlocked: "blocked", kanban.StatusFailed: "failed", kanban.StatusReview: "review"}[c.Status]
		if u.asks[c.ID] != nil {
			why = "asks"
		}
		if c.Parent == "" && why != "" && len(d.N) < tgWidgetMax {
			d.N = append(d.N, [3]string{c.Key(), tgfmt.Clip(tgfmt.Redact(c.Title), 40), why})
		}
	}
	for {
		b, _ := json.Marshal(d)
		if len(b) <= 1024 || len(d.N) == 0 {
			return string(b)
		}
		d.N = d.N[:len(d.N)-1]
	}
}

// tgWidgetSaved is the widget as tgWidgetFile keeps it.
type tgWidgetSaved struct {
	Thread int64  `json:"thread"`
	Msg    int64  `json:"msg"`
	Data   string `json:"data"`
}

// tgWidgetFile is where the widget's message is kept, beside the board.
func (u *ui) tgWidgetFile() string {
	return filepath.Join(filepath.Dir(u.path), "telegram-widget.json")
}

// tgWidgetKeep writes the widget down, or removes the file without one.
func (u *ui) tgWidgetKeep() {
	w := u.phone.widget
	if w == nil || w.msg == 0 {
		os.Remove(u.tgWidgetFile())
		return
	}
	b, _ := json.Marshal(tgWidgetSaved{w.thread, w.msg, w.data})
	writeAtomic(u.tgWidgetFile(), string(b), 0o600)
}

// tgWidgetLoad takes the widget desk last sent back (startRemote) and
// brings it up to the board.
func (u *ui) tgWidgetLoad() {
	var s tgWidgetSaved
	if b, err := os.ReadFile(u.tgWidgetFile()); err != nil || json.Unmarshal(b, &s) != nil || s.Msg == 0 {
		return
	}
	u.phone.widget = &tgWidget{thread: s.Thread, msg: s.Msg, data: s.Data}
	u.tgWidgetSync()
}

// tgWidgetSend sends the board widget to the private chat's thread.
func (u *ui) tgWidgetSend(thread int64) {
	ph := u.phone
	w := &tgWidget{thread: thread, data: u.tgWidgetData()}
	ph.widget = w
	bot, chat := ph.bot, ph.chat
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		id, err := bot.SendWidget(ctx, chat, thread, tgWidgetHTML, w.data)
		u.later(func() {
			if err != nil {
				if ph.widget == w {
					ph.widget = nil
					u.tgWidgetKeep()
				}
				u.gnPostAt(chat, thread, tgfmt.Escape("Couldn't send the widget: "+firstLine(err.Error())+" (Telegram's apps show widgets from Bot API 10.4 on)."), nil, "")
				return
			}
			w.msg = id
			if ph.widget == w {
				u.tgWidgetKeep()
			}
			u.tgWidgetSync()
		})
	}()
}

// tgWidgetSync brings the widget's chat data up to the board, one edit at
// a time; nothing is sent while the data is as last sent.
func (u *ui) tgWidgetSync() {
	ph := u.phone
	if ph == nil || ph.widget == nil || ph.widget.msg == 0 {
		return
	}
	w := ph.widget
	if w.busy {
		w.again = true
		return
	}
	d := u.tgWidgetData()
	if d == w.data {
		return
	}
	w.busy, w.data = true, d
	bot, chat := ph.bot, ph.chat
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := bot.WidgetData(ctx, chat, w.msg, d)
		u.later(func() {
			w.busy = false
			if err != nil && strings.Contains(err.Error(), "not found") && ph.widget == w { // deleted in Telegram: stop following
				ph.widget = nil
				u.tgWidgetKeep()
				return
			}
			if w.again {
				w.again = false
				u.tgWidgetSync()
			}
		})
	}()
}

// tgWidgetQuery is a tap in the widget: "c:KEY" posts that card.
func (u *ui) tgWidgetQuery(q *tgbot.WidgetQuery) {
	ph := u.phone
	said := "desk doesn't know that"
	if key, ok := strings.CutPrefix(q.Data, "c:"); ok {
		said = "That card is gone"
		if u.cardOfKey(key) != nil {
			if ph.widget != nil {
				ph.thread = ph.widget.thread
			}
			u.gnCard(key)
			said = "Sent " + key + " to the chat"
		}
	}
	bot := ph.bot
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		bot.AnswerWidget(ctx, q.ID, said)
	}()
}
