package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/tgbot"
)

// fakeTG is Telegram's Bot API: updates the test queues, long-polled; what
// desk sent, by method.
type fakeTG struct {
	mu   sync.Mutex
	ups  chan tgbot.Update
	sent []map[string]any // sendMessage, editMessageText, answerCallbackQuery bodies, with "method"
	next int64
	fail bool
}

func newFakeTG(t *testing.T) *fakeTG {
	f := &fakeTG{ups: make(chan tgbot.Update, 16), next: 100}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if method == "getUpdates" {
			select {
			case up := <-f.ups:
				json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []tgbot.Update{up}})
			case <-r.Context().Done():
			case <-time.After(time.Second):
				fmt.Fprint(w, `{"ok":true,"result":[]}`)
			}
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.fail {
			fmt.Fprint(w, `{"ok":false,"description":"Forbidden: bot was blocked by the user"}`)
			return
		}
		body["method"] = method
		f.sent = append(f.sent, body)
		f.next++
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, f.next)
	}))
	t.Cleanup(srv.Close)
	old := tgbot.API
	tgbot.API = srv.URL
	t.Cleanup(func() { tgbot.API = old })
	return f
}

// said is the last body desk sent with method containing text, and its
// message id (sendMessage's reply), or nil.
func (f *fakeTG) said(method, text string) (map[string]any, int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.sent) - 1; i >= 0; i-- {
		b := f.sent[i]
		if b["method"] == method && strings.Contains(fmt.Sprint(b["text"]), text) {
			return b, 101 + int64(i)
		}
	}
	return nil, 0
}

func tgMsg(chat int64, text string, reply int64) tgbot.Update {
	m := &tgbot.Message{ID: 1, Text: text}
	m.Chat.ID = chat
	if reply != 0 {
		m.Reply = &tgbot.Message{ID: reply}
	}
	return tgbot.Update{Message: m}
}

func tgPress(chat, msgID int64, data string) tgbot.Update {
	m := &tgbot.Message{ID: msgID, Text: "the question"}
	m.Chat.ID = chat
	return tgbot.Update{Callback: &tgbot.Callback{ID: "cb", Data: data, Message: m}}
}

// phoneOn writes a [phone] with token and starts it; its stop.
func phoneOn(t *testing.T, h *harness, extra string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(orgs.SettingsFile()), 0o700)
	if err := os.WriteFile(orgs.SettingsFile(), []byte("[phone]\ntoken = \"T0K\"\n"+extra), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.u.startPhone())
	if h.u.phone == nil {
		t.Fatal("no phone")
	}
}

// TestPhonePair: the first /start pairs its chat and keeps it in
// settings.toml; before that nothing is sent, and another chat is ignored.
func TestPhonePair(t *testing.T) {
	h := newHarness(t)
	f := newFakeTG(t)
	phoneOn(t, h, "")
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme", Status: kanban.StatusWaiting})
	h.u.phoneTell(h.u.b.Card(c.ID), "Your turn") // not paired: nothing
	f.ups <- tgMsg(42, "/start", 0)
	h.pumpUntil("paired", func() bool { return h.u.phone.chat == 42 })
	if orgs.PhoneSettings().Chat != 42 || !strings.Contains(h.u.toast, "Phone paired") {
		t.Fatalf("settings %+v toast %q", orgs.PhoneSettings(), h.u.toast)
	}
	h.pumpUntil("hello", func() bool { b, _ := f.said("sendMessage", "paired with this chat"); return b != nil })
	f.ups <- tgMsg(7, "hi", 0) // a stranger
	f.ups <- tgMsg(42, "/help", 0)
	h.pumpUntil("help", func() bool { b, _ := f.said("sendRichMessage", ""); return b != nil })
	if b, _ := f.said("sendRichMessage", ""); b["chat_id"] != float64(42) {
		t.Fatalf("sent to %v", b["chat_id"])
	}
}

// TestPhonePermission: a permission request comes with Allow and Deny; a
// press answers it once; a question's options are buttons.
func TestPhonePermission(t *testing.T) {
	h := newHarness(t)
	f := newFakeTG(t)
	phoneOn(t, h, "chat_id = 42\n")
	c := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Fix login", Org: "Acme", Status: kanban.StatusWorking})
	fk := &fakeClaude{}
	h.u.procs[c.ID] = fk
	h.u.onEvent(c.ID, 0, claude.Event{Kind: claude.KindAsk, Ask: &claude.Request{ID: "r1", Tool: "Bash", Line: "Bash: rm -rf build", Input: json.RawMessage(`{"command":"rm -rf build"}`)}})
	var sent map[string]any
	var id int64
	h.pumpUntil("the request", func() bool { sent, id = f.said("sendMessage", "Wants to run Bash: rm -rf build"); return sent != nil })
	kb, _ := json.Marshal(sent["reply_markup"])
	if !strings.Contains(string(kb), `"text":"Allow"`) || !strings.Contains(string(kb), `"text":"Deny"`) {
		t.Fatalf("buttons %s", kb)
	}
	f.ups <- tgPress(42, id, "p1")
	h.pumpUntil("allowed", func() bool { return len(fk.replies) == 1 })
	if !strings.HasPrefix(fk.replies[0], "r1 allow") || h.u.asks[c.ID] != nil {
		t.Fatalf("replies %v", fk.replies)
	}
	h.pumpUntil("edited", func() bool { b, _ := f.said("editMessageText", "→ Allowed"); return b != nil })
	f.ups <- tgPress(42, id, "p2") // Deny, too late
	h.pumpUntil("stale", func() bool { b, _ := f.said("editMessageText", "→ Already answered"); return b != nil })

	h.u.onEvent(c.ID, 0, claude.Event{Kind: claude.KindAsk, Ask: &claude.Request{ID: "r2", Tool: "AskUserQuestion",
		Input: json.RawMessage(`{"questions":[{"question":"Which?","options":[{"label":"A"},{"label":"B"}]}]}`),
		Questions: []claude.Question{{Question: "Which?", Options: []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		}{{Label: "A"}, {Label: "B"}}}}}})
	h.pumpUntil("the question", func() bool { sent, id = f.said("sendMessage", "Which?"); return sent != nil })
	f.ups <- tgPress(42, id, "p4") // B
	h.pumpUntil("answered", func() bool { return len(fk.replies) == 2 })
	if !strings.Contains(fk.replies[1], `"Which?":"B"`) {
		t.Fatalf("answer %v", fk.replies[1])
	}
}

// TestPhoneReply: a reply to a card's message goes to its Claude; while a
// desk window has the focus nothing is sent unless always; a send that
// fails says so.
func TestPhoneReply(t *testing.T) {
	h := newHarness(t)
	f := newFakeTG(t)
	phoneOn(t, h, "chat_id = 42\n")
	c := h.u.b.Add(h.u.b.Column("Review"), kanban.Card{Title: "Fix login", Org: "Acme", Status: kanban.StatusWaiting, Last: "Done. **Merge** it?"})
	fk := &fakeClaude{}
	h.u.procs[c.ID] = fk
	h.u.phoneTell(h.u.b.Card(c.ID), "Your turn")
	var id int64
	h.pumpUntil("its turn", func() bool { b, i := f.said("sendMessage", "Done. Merge it?"); id = i; return b != nil })
	h.pumpUntil("kept", func() bool { return h.u.phone.cards[id] == c.ID })
	f.ups <- tgMsg(42, "yes, merge", id)
	h.pumpUntil("sent", func() bool { return len(fk.prompts) == 1 })
	if !strings.Contains(fk.prompts[0], "yes, merge") {
		t.Fatalf("prompt %q", fk.prompts[0])
	}
	h.pumpUntil("ack", func() bool { b, _ := f.said("sendMessage", "Sent to "+c.Key()); return b != nil })

	h.u.mainPane.focused = true
	n := len(f.sent)
	h.u.phoneTell(h.u.b.Card(c.ID), "Your turn")
	h.frame()
	if len(f.sent) != n {
		t.Fatal("sent while desk was in front")
	}
	h.u.phone.always = true
	f.mu.Lock()
	f.fail = true
	f.mu.Unlock()
	h.u.b.Card(c.ID).Last = strings.Repeat("long ", 1000)
	h.u.phoneTell(h.u.b.Card(c.ID), "Your turn")
	h.pumpUntil("failure", func() bool { return strings.Contains(h.u.toast, "bot was blocked") })
}

// TestPhoneOff: no token, no phone; a pairing past its time, or an update
// with nothing in it, does nothing; polling stops with its context and
// waits after a failure.
func TestPhoneOff(t *testing.T) {
	h := newHarness(t)
	h.u.startPhone()()
	if h.u.phone != nil {
		t.Fatal("a phone without a token")
	}
	h.u.phoneUpdate(tgMsg(1, "/start", 0)) // no phone: nothing
	newFakeTG(t)
	phoneOn(t, h, "")
	h.u.phone.pairUntil = time.Now().Add(-time.Second)
	h.u.phoneUpdate(tgMsg(42, "/start", 0))
	h.u.phoneUpdate(tgbot.Update{})
	if h.u.phone.chat != 0 {
		t.Fatal("paired late")
	}
	if got := h.u.phonePress(phoneAsk{}); got != "Already answered" {
		t.Fatal(got)
	}

	old := phoneRetry
	phoneRetry = time.Millisecond
	defer func() { phoneRetry = old }()
	tgbot.API = "http://127.0.0.1:1"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.u.phonePoll(ctx, &tgbot.Bot{Token: "x"}); close(done) }()
	time.Sleep(20 * time.Millisecond) // a few failed polls, waited out; nothing is checked of them
	cancel()
	<-done
	h.u.phonePoll(ctx, &tgbot.Bot{Token: "x"}) // ended already: returns
}
