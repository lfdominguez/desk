package ui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/tgbot"
)

// TestRemoteWidget: /widget sends the board as a widget with its data; a
// board change edits the data (once per change, nothing when it's the
// same); a tap on a card posts it; a refused send says so.
func TestRemoteWidget(t *testing.T) {
	h := newHarness(t)
	f := remoteOn(t, h, "")
	id := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Fix login", Org: "Acme"}).ID
	h.u.save()
	h.u.phoneUpdate(dm("/widget"))
	h.pumpUntil("sent", func() bool { return h.u.phone.widget != nil && h.u.phone.widget.msg != 0 })
	p := f.Last("sendWidget")
	if !strings.Contains(p["html"].(string), "TelegramWidget") || !strings.Contains(p["chat_data"].(string), `["In progress",1]`) || p["allow_queries"] != true {
		t.Fatal(p)
	}

	h.u.b.Card(id).Status = kanban.StatusWaiting
	h.u.save()
	h.pumpUntil("edited", func() bool { return f.Last("editMessageWidgetData") != nil && !h.u.phone.widget.busy })
	var d struct{ N [][3]string }
	json.Unmarshal([]byte(f.Last("editMessageWidgetData")["chat_data"].(string)), &d)
	key := h.u.b.Card(id).Key()
	if len(d.N) != 1 || d.N[0] != [3]string{key, "Fix login", "your turn"} {
		t.Fatal(d)
	}
	n := len(f.Calls("editMessageWidgetData"))
	h.u.save()
	h.frame()
	if len(f.Calls("editMessageWidgetData")) != n {
		t.Fatal("sent the same data again")
	}

	np := len(posted(f))
	h.u.phoneUpdate(tgbot.Update{Widget: &tgbot.WidgetQuery{ID: "q", From: tgbot.User{ID: 42}, Data: "c:" + key}})
	h.pumpUntil("answered", func() bool { return f.Last("answerWidgetQuery") != nil })
	if f.Last("answerWidgetQuery")["data"] != "Sent "+key+" to the chat" {
		t.Fatal(f.Last("answerWidgetQuery"))
	}
	h.pumpUntil("posted", func() bool { return len(posted(f)) > np && strings.Contains(posted(f)[np], "Fix login") })
	for data, want := range map[string]string{"c:NOPE-9": "That card is gone", "x": "desk doesn't know that"} {
		h.u.phoneUpdate(tgbot.Update{Widget: &tgbot.WidgetQuery{ID: data, From: tgbot.User{ID: 42}, Data: data}})
		h.pumpUntil(data, func() bool { return f.Last("answerWidgetQuery")["widget_query_id"] == data })
		if f.Last("answerWidgetQuery")["data"] != want {
			t.Fatal(data, f.Last("answerWidgetQuery"))
		}
	}
	nq := len(f.Calls("answerWidgetQuery"))
	h.u.phoneUpdate(tgbot.Update{Widget: &tgbot.WidgetQuery{ID: "s", From: tgbot.User{ID: 7}, Data: "c:" + key}}) // a stranger
	h.frame()
	if len(f.Calls("answerWidgetQuery")) != nq {
		t.Fatal("answered a stranger")
	}

	f.Fail("editMessageWidgetData", "Bad Request: message to edit not found") // deleted in Telegram: no longer followed
	h.u.b.Card(id).Status = kanban.StatusBlocked
	h.u.save()
	h.pumpUntil("dropped", func() bool { return h.u.phone.widget == nil })

	f.Fail("sendWidget", "Bad Request: method not found")
	h.u.phoneUpdate(dm("/widget"))
	h.pumpUntil("refused", func() bool {
		return len(posted(f)) > 0 && strings.Contains(posted(f)[len(posted(f))-1], "send the widget")
	})
	if h.u.phone.widget != nil {
		t.Fatal("kept a widget that never went")
	}
}

// TestRemoteWidgetData: long titles shed waiting cards until the data
// fits Telegram's 1024 bytes; a pending edit runs again after.
func TestRemoteWidgetData(t *testing.T) {
	h := newHarness(t)
	remoteOn(t, h, "")
	for range 12 {
		h.u.b.Add(h.u.b.Column("Review"), kanban.Card{Title: strings.Repeat("long title words ", 5), Org: "Acme", Status: kanban.StatusReview})
	}
	d := h.u.tgWidgetData()
	var got struct{ N [][3]string }
	json.Unmarshal([]byte(d), &got)
	if len(d) > 1024 || len(got.N) == 0 || len(got.N) > tgWidgetMax {
		t.Fatal(len(d), len(got.N))
	}
	w := &tgWidget{msg: 1, busy: true}
	h.u.phone.widget = w
	h.u.tgWidgetSync()
	if !w.again {
		t.Fatal("not queued")
	}
}
