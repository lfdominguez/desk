package tgbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lfdominguez/desk/internal/tgbot/tgfake"
)

// slept is what sleep was asked to wait, in order; tests never wait.
var (
	sleptMu sync.Mutex
	slept   []time.Duration
)

var realSleep = sleep

func TestMain(m *testing.M) {
	sleep = func(ctx context.Context, d time.Duration) error {
		sleptMu.Lock()
		slept = append(slept, d)
		sleptMu.Unlock()
		return ctx.Err()
	}
	os.Exit(m.Run())
}

func fake(t *testing.T) (*tgfake.Server, *Bot, context.Context) {
	f := tgfake.New()
	t.Cleanup(f.Close)
	old := API
	API = f.URL
	t.Cleanup(func() { API = old })
	return f, &Bot{Token: "T0K"}, context.Background()
}

func js(v any) string { b, _ := json.Marshal(v); return string(b) }

// TestRich: a markdown or html Out is a rich message, edited as one by
// Change; plain text stays sendMessage; threads, silence and buttons go
// along.
func TestRich(t *testing.T) {
	f, b, ctx := fake(t)
	id, err := b.Post(ctx, Out{Chat: -100, Thread: 5, Markdown: "**hi**", Silent: true,
		Rows: [][]Button{{{Text: "Allow", Data: "a", Style: "success"}}}})
	p := f.Last("sendRichMessage")
	if err != nil || id == 0 || js(p["rich_message"]) != `{"markdown":"**hi**"}` || js(p["message_thread_id"]) != "5" || p["disable_notification"] != true ||
		!strings.Contains(js(p["reply_markup"]), `"style":"success"`) {
		t.Fatalf("%d %v %v", id, err, p)
	}
	if _, err := b.Post(ctx, Out{Chat: 1, HTML: "<b>x</b>"}); err != nil || fmt.Sprint(f.Last("sendRichMessage")["rich_message"]) != "map[html:<b>x</b>]" {
		t.Fatal(err, f.Last("sendRichMessage"))
	}
	if _, err := b.Post(ctx, Out{Chat: 1, Text: "plain"}); err != nil || f.Last("sendMessage")["text"] != "plain" || f.Last("sendMessage")["message_thread_id"] != nil {
		t.Fatal(err, f.Last("sendMessage"))
	}
	if err := b.Change(ctx, id, Out{Chat: -100, Markdown: "**bye**"}); err != nil || js(f.Last("editMessageText")["rich_message"]) != `{"markdown":"**bye**"}` {
		t.Fatal(err, f.Last("editMessageText"))
	}
	if err := b.Buttons(ctx, -100, id, nil); err != nil || js(f.Last("editMessageReplyMarkup")["reply_markup"]) != `{"inline_keyboard":[]}` {
		t.Fatal(err, f.Last("editMessageReplyMarkup"))
	}
	if err := b.Draft(ctx, 1, 3, 9, "so far"); err != nil || js(f.Last("sendRichMessageDraft")) != `{"chat_id":1,"draft_id":9,"message_thread_id":3,"rich_message":{"markdown":"so far"}}` {
		t.Fatal(err, f.Last("sendRichMessageDraft"))
	}
	if err := b.Draft(ctx, 1, 0, 9, "x"); err != nil || f.Last("sendRichMessageDraft")["message_thread_id"] != nil {
		t.Fatal(err)
	}
	f.Fail("editMessageText", "Bad Request: message is not modified: specified new message content and reply markup are exactly the same")
	if err := b.Change(ctx, id, Out{Chat: -100, Text: "same"}); !NotModified(err) {
		t.Fatalf("not modified %v", err)
	}
	if NotModified(errors.New("x")) || NotModified(&Error{Code: 400, Description: "chat not found"}) {
		t.Fatal("NotModified too wide")
	}
}

// TestTopics: a topic is made (with its color), renamed, closed, reopened
// and deleted; pins, reactions, command menus and getMe.
func TestTopics(t *testing.T) {
	f, b, ctx := fake(t)
	th, err := b.NewTopic(ctx, -100, "🟢 DESK-1 · Fix", Green)
	if err != nil || th == 0 || js(f.Last("createForumTopic")["icon_color"]) != fmt.Sprint(Green) {
		t.Fatal(th, err, f.Last("createForumTopic"))
	}
	if _, err := b.NewTopic(ctx, -100, "plain", 0); err != nil || f.Last("createForumTopic")["icon_color"] != nil {
		t.Fatal(err)
	}
	if err := b.Rename(ctx, -100, th, "🟡 DESK-1 · Fix"); err != nil || f.Last("editForumTopic")["name"] != "🟡 DESK-1 · Fix" {
		t.Fatal(err)
	}
	for _, act := range []string{"close", "reopen", "delete"} {
		if err := b.Topic(ctx, -100, th, act); err != nil || f.Last(act+"ForumTopic") == nil {
			t.Fatal(act, err)
		}
	}
	if err := b.Pin(ctx, -100, 3); err != nil || f.Last("pinChatMessage")["disable_notification"] != true {
		t.Fatal(err)
	}
	if err := b.React(ctx, -100, 3, "👀"); err != nil || js(f.Last("setMessageReaction")["reaction"]) != `[{"emoji":"👀","type":"emoji"}]` {
		t.Fatal(err, f.Last("setMessageReaction"))
	}
	if err := b.React(ctx, -100, 3, ""); err != nil || js(f.Last("setMessageReaction")["reaction"]) != `[]` {
		t.Fatal(err)
	}
	cs := []Command{{"board", "the board"}}
	if err := b.Commands(ctx, -100, cs); err != nil || js(f.Last("setMyCommands")["scope"]) != `{"chat_id":-100,"type":"chat"}` {
		t.Fatal(err, f.Last("setMyCommands"))
	}
	if err := b.Commands(ctx, 0, cs); err != nil || js(f.Last("setMyCommands")["scope"]) != `{"type":"default"}` {
		t.Fatal(err)
	}
	f.Me["has_topics_enabled"] = false
	if me, err := b.Me(ctx); err != nil || me.Username != "desk_test_bot" || me.Topics || me.ID != 7 {
		t.Fatalf("%+v %v", me, err)
	}
}

// TestFiles: a photo or document goes up as multipart with its caption and
// thread; a file comes down by its id, refused over MaxFile.
func TestFiles(t *testing.T) {
	f, b, ctx := fake(t)
	id, err := b.SendFile(ctx, -100, 4, "photo", "canvas.png", []byte("PNG"), "DESK-1's canvas", [][]Button{{{Text: "Open", Data: "o"}}})
	p := f.Last("sendPhoto")
	if err != nil || id == 0 || p["file"] != "PNG" || p["file_name"] != "canvas.png" || p["caption"] != "DESK-1's canvas" || js(p["message_thread_id"]) != "4" || !strings.Contains(fmt.Sprint(p["reply_markup"]), `"callback_data":"o"`) {
		t.Fatal(id, err, p)
	}
	if _, err := b.SendFile(ctx, 1, 0, "document", "x.diff", []byte("+a"), "", nil); err != nil || f.Last("sendDocument")["caption"] != nil || f.Last("sendDocument")["message_thread_id"] != nil {
		t.Fatal(err, f.Last("sendDocument"))
	}
	f.Files["abc"] = []byte("a voice")
	if data, err := b.Download(ctx, "abc"); err != nil || string(data) != "a voice" {
		t.Fatal(string(data), err)
	}
	if _, err := b.Download(ctx, "gone"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatal(err)
	}
	f.Files["big"] = make([]byte, MaxFile+1)
	if _, err := b.Download(ctx, "big"); err == nil || !strings.Contains(err.Error(), "20 MB") {
		t.Fatal(err)
	}
	f.Fail("getFile", "Bad Request: file is too big")
	if _, err := b.Download(ctx, "abc"); err == nil {
		t.Fatal("getFile refused")
	}
	API = "http://127.0.0.1:1"
	if _, err := b.SendFile(ctx, 1, 0, "photo", "a", nil, "", nil); err == nil || strings.Contains(err.Error(), "T0K") {
		t.Fatalf("unreachable, token kept out: %v", err)
	}
	API = "://bad"
	if _, err := b.Download(ctx, "abc"); err == nil {
		t.Fatal("bad url")
	}
}

// TestPace: one chat's calls are spaced by Gap, other chats aren't held
// by it, drafts and calls without a chat aren't paced; a 429 is waited out
// by its retry_after, and given up after retries; every call also waits
// for the bot-wide slot (All), taken once its chat's comes.
func TestPace(t *testing.T) {
	f, b, ctx := fake(t)
	t0 := time.Unix(1000, 0)
	now = func() time.Time { return t0 }
	t.Cleanup(func() { now = time.Now })
	sleptMu.Lock()
	slept = nil
	sleptMu.Unlock()
	b.Send(ctx, -100, "a", nil)
	b.Send(ctx, -100, "b", nil)
	b.Send(ctx, 5, "c", nil)
	b.Draft(ctx, 5, 0, 1, "d")
	b.Answer(ctx, "cb", "")
	sleptMu.Lock()
	got := fmt.Sprint(slept)
	sleptMu.Unlock()
	if got != "[0s 3s 33.333333ms 0s 66.666666ms]" { // the bot-wide slot after each chat's
		t.Fatalf("slept %s", got)
	}
	sleptMu.Lock()
	slept = nil
	sleptMu.Unlock()
	f.Limit("sendMessage", 2)
	if _, err := b.Send(ctx, 9, "x", nil); err != nil || len(f.Calls("sendMessage")) != 6 {
		t.Fatal(err, len(f.Calls("sendMessage")))
	}
	f.Limit("answerCallbackQuery", retries+1)
	var e *Error
	if err := b.Answer(ctx, "cb", ""); !errors.As(err, &e) || e.Code != 429 {
		t.Fatalf("gave up %v", err)
	}
	sleptMu.Lock()
	got = fmt.Sprint(slept)
	sleptMu.Unlock()
	if got != "[0s 99.999999ms 1s 1s 1s 1s 1s]" {
		t.Fatalf("slept %s", got)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := b.Send(cctx, 9, "x", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled %v", err)
	}
	f.Limit("answerCallbackQuery", 1)
	if err := b.Answer(cctx, "cb", ""); err == nil {
		t.Fatal("canceled during a 429 wait")
	}
	if Gap(-5) != 3*time.Second || Gap(5) != time.Second || All != time.Second/30 {
		t.Fatal("gaps")
	}
}

// noGet is a transport that refuses downloads (GETs) only.
type noGet struct{}

func (noGet) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == "GET" {
		return nil, errors.New("no")
	}
	return http.DefaultTransport.RoundTrip(r)
}

// TestEdges: the real sleep waits or gives up with its context; a sleep
// cut short ends a 429's wait; a canceled upload; a download refused on
// its way or by a path that isn't a URL; an Error says its method.
func TestEdges(t *testing.T) {
	f, b, ctx := fake(t)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if realSleep(ctx, 0) != nil || realSleep(ctx, time.Millisecond) != nil || realSleep(cctx, time.Hour) == nil || realSleep(cctx, 0) == nil {
		t.Fatal("real sleep")
	}
	old := sleep
	sleep = func(ctx context.Context, d time.Duration) error {
		if d > 0 {
			return context.Canceled
		}
		return nil
	}
	f.Limit("answerCallbackQuery", 1)
	err := b.Answer(ctx, "cb", "")
	sleep = old
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("429 wait cut short: %v", err)
	}
	if _, err := b.SendFile(cctx, 1, 0, "photo", "a", nil, "", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("upload canceled: %v", err)
	}
	gb := &Bot{Token: "T0K", Client: &http.Client{Transport: noGet{}}}
	f.Files["abc"] = []byte("x")
	if _, err := gb.Download(ctx, "abc"); err == nil || err.Error() != "telegram file: unreachable" {
		t.Fatalf("download refused: %v", err)
	}
	if _, err := b.Download(ctx, "%zz"); err == nil {
		t.Fatal("a path that isn't a URL")
	}
	if e := (&Error{Method: "getMe", Description: "Unauthorized"}); e.Error() != "telegram getMe: Unauthorized" {
		t.Fatal(e.Error())
	}
	if (&retryError{time.Second}).Error() != "retry after 1s" {
		t.Fatal("retryError")
	}
}

// TestWidget: a widget goes with its chat data and queries allowed, its
// data is replaced in place, a query is answered; widget queries are
// among the updates asked for.
func TestWidget(t *testing.T) {
	f, b, ctx := fake(t)
	id, err := b.SendWidget(ctx, 42, 9, "<div>hi</div>", `{"a":1}`)
	p := f.Last("sendWidget")
	if err != nil || id == 0 || p["html"] != "<div>hi</div>" || p["chat_data"] != `{"a":1}` || p["allow_queries"] != true || js(p["message_thread_id"]) != "9" {
		t.Fatal(id, err, p)
	}
	if _, err := b.SendWidget(ctx, 42, 0, "x", ""); err != nil || f.Last("sendWidget")["message_thread_id"] != nil {
		t.Fatal(err)
	}
	if err := b.WidgetData(ctx, 42, id, `{"a":2}`); err != nil || f.Last("editMessageWidgetData")["chat_data"] != `{"a":2}` {
		t.Fatal(err)
	}
	if err := b.AnswerWidget(ctx, "q1", "ok"); err != nil || f.Last("answerWidgetQuery")["widget_query_id"] != "q1" {
		t.Fatal(err)
	}
	f.Push(map[string]any{"widget_query": map[string]any{"id": "q2", "from": map[string]any{"id": 42}, "data": "c:DESK-1"}})
	us, err := b.Updates(ctx, 0)
	if err != nil || len(us) != 1 || us[0].Widget == nil || us[0].Widget.Data != "c:DESK-1" || us[0].Widget.From.ID != 42 {
		t.Fatal(us, err)
	}
}

// TestQuiet: inside the quiet hours (past midnight too) what's sent goes
// without a sound, a file as well; outside them, and with none, it sounds.
func TestQuiet(t *testing.T) {
	f, b, ctx := fake(t)
	at := func(h, m int) { now = func() time.Time { return time.Date(2026, 11, 22, h, m, 0, 0, time.Local) } }
	t.Cleanup(func() { now = time.Now })
	silent := func() bool {
		b.Post(ctx, Out{Chat: 1, Text: "x"})
		return f.Last("sendMessage")["disable_notification"] == true
	}
	at(23, 0)
	if silent() {
		t.Fatal("no quiet hours set")
	}
	b.Quiet(22*time.Hour, 7*time.Hour)
	if !silent() {
		t.Fatal("23:00 sounded")
	}
	at(6, 59)
	if !silent() {
		t.Fatal("06:59 sounded")
	}
	at(7, 0)
	if silent() {
		t.Fatal("07:00 was quiet")
	}
	b.Quiet(13*time.Hour, 14*time.Hour)
	at(13, 30)
	b.SendFile(ctx, 1, 0, "photo", "a.png", []byte("p"), "", nil)
	if !silent() || f.Last("sendPhoto")["disable_notification"] != "true" {
		t.Fatal("13:30 sounded")
	}
	at(14, 0)
	if silent() {
		t.Fatal("14:00 was quiet")
	}
	b.Draft(ctx, 1, 0, 1, "x")
	if f.Last("sendRichMessageDraft")["disable_notification"] != nil {
		t.Fatal("a draft took it")
	}
}
