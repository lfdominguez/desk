package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/tgbot"
	"github.com/lfdominguez/desk/internal/tgbot/tgfake"
	"github.com/lfdominguez/desk/internal/tgfmt"
	"github.com/lfdominguez/desk/internal/tgsetup"
)

// remoteOn mirrors Acme (mode) to group -100555 with a bot that isn't
// polling: the test hands it updates.
func remoteOn(t *testing.T, h *harness, mode string) *tgfake.Server {
	t.Helper()
	f := tgFake(t)
	settingsFile(t, "[telegram]\ntoken = \"T0K\"\nchat_id = 42\n[telegram.orgs.Acme]\nmirror = \""+mode+"\"\nchat_id = -100555\n")
	h.u.phone = &phoneState{bot: &tgbot.Bot{Token: "T0K"}, chat: 42, asks: map[string]phoneAsk{}, cards: map[int64]string{}}
	h.u.startRemote()
	return f
}

// topicOf waits for card id's topic to settle (no plan under way).
func (h *harness) topicOf(id string) *rmTopic {
	h.t.Helper()
	h.pumpUntil("the topics", func() bool { return !h.u.rm.busy })
	return h.u.rm.topics[id]
}

// posted is every rich message's markdown so far, in order.
func posted(f *tgfake.Server) (out []string) {
	for _, c := range f.Calls("sendRichMessage") {
		out = append(out, fmt.Sprint(c.Params["rich_message"].(map[string]any)["markdown"]))
	}
	return out
}

// buttonData is the callback data of the last rich message's button text.
func buttonData(t *testing.T, f *tgfake.Server, text string) string {
	t.Helper()
	b, _ := json.Marshal(f.Last("sendRichMessage")["reply_markup"])
	var kb struct {
		Rows [][]tgbot.Button `json:"inline_keyboard"`
	}
	json.Unmarshal(b, &kb)
	for _, r := range kb.Rows {
		for _, btn := range r {
			if btn.Text == text {
				return btn.Data
			}
		}
	}
	t.Fatalf("no %q in %s", text, b)
	return ""
}

func groupMsg(from, thread int64, text string) tgbot.Update {
	return tgbot.Update{Message: &tgbot.Message{ID: 9, Thread: thread, Text: text, Chat: tgbot.Chat{ID: -100555, Type: "supergroup", IsForum: true}, From: tgbot.User{ID: from}}}
}

func groupPress(from int64, data string) tgbot.Update {
	return tgbot.Update{Callback: &tgbot.Callback{ID: "cb", Data: data, From: tgbot.User{ID: from},
		Message: &tgbot.Message{ID: 9, Chat: tgbot.Chat{ID: -100555, Type: "supergroup"}}}}
}

// TestRemoteTopic: a card with a conversation gets a topic named by its
// state, a pinned header, its last message; then what's said follows (the
// user's in desk, Claude's, its tools run together), a request with its
// buttons, answered from the topic and marked so; the name and header
// follow the card; what's typed in the topic reaches its Claude and isn't
// sent back; /stop, /help; strangers are ignored.
func TestRemoteTopic(t *testing.T) {
	h := newHarness(t)
	f := remoteOn(t, h, "full")
	c := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Fix login", Org: "Acme", Status: kanban.StatusWorking})
	fk := &fakeClaude{}
	h.u.procs[c.ID] = fk
	h.u.save() // no conversation: no topic
	if h.topicOf(c.ID) != nil {
		t.Fatal("a topic without a conversation")
	}
	h.u.say(c.ID, "you", "fix the login")
	h.u.say(c.ID, "claude", "On it.")
	h.u.save()
	tp := h.topicOf(c.ID)
	if tp == nil || tp.Thread == 0 || tp.Header == 0 || tp.Sent != 2 {
		t.Fatalf("topic %+v", tp)
	}
	if name := f.Last("createForumTopic")["name"]; name != "🟢 "+c.Key() {
		t.Fatalf("name %v", name)
	}
	if p := posted(f); len(p) != 2 || !strings.Contains(p[0], "### "+c.Key()+"\n\nFix login") || !strings.Contains(p[0], "In progress") || p[1] != "On it." {
		t.Fatalf("posted %q", p)
	}
	h.pumpUntil("the group's menu", func() bool { return f.Last("setMyCommands") != nil })
	if m := f.Last("setMyCommands"); fmt.Sprint(m["scope"]) != "map[chat_id:-100555 type:chat]" {
		t.Fatalf("the group's menu %v", m)
	}
	h.u.rmConfig()
	if n := len(f.Calls("setMyCommands")); n != 1 {
		t.Fatalf("menu set %d times", n)
	}
	if f.Last("pinChatMessage") == nil {
		t.Fatal("header not pinned")
	}

	h.u.say(c.ID, "you", "also the logout")
	h.u.sayMsg(c.ID, kanban.ChatMsg{Who: "tool", Text: "Bash: go test ./..."})
	h.u.sayMsg(c.ID, kanban.ChatMsg{Who: "tool", Text: "Edit auth.go"})
	h.u.say(c.ID, "claude", "Fixed with token ghp_"+strings.Repeat("a", 36))
	h.u.say(c.ID, "error", "a hiccup")
	h.u.sayMsg(c.ID, kanban.ChatMsg{Who: "lead", Name: "CTO", Text: "approved"})
	h.u.sayMsg(c.ID, kanban.ChatMsg{Who: "claude", Text: "  "})
	h.u.save()
	h.topicOf(c.ID)
	last := posted(f)[len(posted(f))-1]
	for _, s := range []string{"**🧑 You** · _in desk_\n\nalso the logout", "<details><summary>🔧 2 tools</summary>", "```\nBash: go test ./...\nEdit auth.go\n```", "‹redacted›", "**⚠️ Error**\n\na hiccup", "**💬 CTO**", "\n\n---\n\n"} {
		if !strings.Contains(last, s) {
			t.Fatalf("%q lacks %q", last, s)
		}
	}
	if strings.Contains(last, "ghp_") {
		t.Fatal("a token went to Telegram")
	}

	h.u.onEvent(c.ID, 0, claude.Event{Kind: claude.KindAsk, Ask: &claude.Request{ID: "r1", Tool: "Bash", Line: "Bash: git push", Input: json.RawMessage(`{"command":"git push"}`)}})
	h.u.save()
	h.topicOf(c.ID)
	if name := f.Last("editForumTopic")["name"]; name != "🟡 "+c.Key() {
		t.Fatalf("renamed %v", name)
	}
	if !strings.Contains(fmt.Sprint(f.Last("editMessageText")["rich_message"]), "Your turn") {
		t.Fatalf("header %v", f.Last("editMessageText"))
	}
	if !strings.Contains(posted(f)[len(posted(f))-1], "git push") || !strings.Contains(js(f.Last("sendRichMessage")["reply_markup"]), `"style":"success"`) {
		t.Fatal("request not posted")
	}
	h.u.phoneUpdate(groupPress(7, buttonData(t, f, "Allow once"))) // a stranger
	if len(fk.replies) != 0 {
		t.Fatal("a stranger answered")
	}
	h.u.phoneUpdate(groupPress(42, buttonData(t, f, "Allow once")))
	if len(fk.replies) != 1 || !strings.HasPrefix(fk.replies[0], "r1 allow") {
		t.Fatalf("replies %v", fk.replies)
	}
	h.pumpUntil("answered", func() bool { return f.Last("answerCallbackQuery") != nil })
	h.u.save()
	h.topicOf(c.ID)
	if !strings.Contains(fmt.Sprint(f.Last("editMessageText")["rich_message"]), "→ answered") || h.u.rm.topics[c.ID].Ask != "" {
		t.Fatalf("request not closed: %v", f.Last("editMessageText"))
	}
	h.u.onEvent(c.ID, 0, claude.Event{Kind: claude.KindAsk, Ask: &claude.Request{ID: "r2", Tool: "Bash", Line: "Bash: make", Input: json.RawMessage(`{}`)}})
	h.u.save()
	h.topicOf(c.ID)
	h.u.phoneUpdate(groupPress(42, buttonData(t, f, "Allow every Bash")))
	if !h.u.allowed[c.ID]["Bash"] {
		t.Fatal("not allowed every Bash")
	}
	h.u.onEvent(c.ID, 0, claude.Event{Kind: claude.KindAsk, Ask: &claude.Request{ID: "r3", Tool: "AskUserQuestion", Input: json.RawMessage(`{}`),
		Questions: []claude.Question{{Question: "Which?", Options: []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		}{{Label: "A"}, {Label: "B"}}}}}})
	h.u.save()
	h.topicOf(c.ID)
	h.u.phoneUpdate(groupPress(42, buttonData(t, f, "B")))
	if len(fk.replies) != 3 || !strings.Contains(fk.replies[2], `"Which?":"B"`) {
		t.Fatalf("replies %v", fk.replies)
	}

	th := h.u.rm.topics[c.ID].Thread
	h.u.phoneUpdate(groupMsg(42, th, "now the tests"))
	if len(fk.prompts) != 1 || !strings.Contains(fk.prompts[0], "now the tests") {
		t.Fatalf("prompts %v", fk.prompts)
	}
	h.pumpUntil("seen", func() bool { return f.Last("setMessageReaction") != nil })
	h.u.save()
	h.topicOf(c.ID)
	for _, p := range posted(f) {
		if strings.Contains(p, "now the tests") {
			t.Fatal("the topic's own message sent back")
		}
	}
	h.u.phoneUpdate(groupMsg(42, th, "/stop"))
	h.pumpUntil("stopping", func() bool { p := f.Last("sendMessage"); return p != nil && p["text"] == "Stopping its turn." })
	h.u.phoneUpdate(groupMsg(42, th, "/help"))
	h.pumpUntil("help", func() bool { p := f.Last("sendMessage"); return p != nil && strings.Contains(p["text"].(string), "/stop stops") })
	h.u.phoneUpdate(groupMsg(42, th, ""))
	h.pumpUntil("text only", func() bool { p := f.Last("sendMessage"); return p != nil && strings.Contains(p["text"].(string), "voice notes reach") })

	f.Files = map[string][]byte{"big": []byte("\x89PNG\r\n\x1a\n....")}
	pic := groupMsg(42, th, "")
	pic.Message.Caption, pic.Message.Photo = "look at this", []tgbot.File{{ID: "small"}, {ID: "big"}}
	n := len(fk.prompts)
	h.u.phoneUpdate(pic)
	h.pumpUntil("picture sent", func() bool { return len(fk.prompts) == n+1 })
	if !strings.Contains(fk.prompts[n], "look at this [Image 1]") {
		t.Fatalf("prompt %q", fk.prompts[n])
	}
	f.Files["notes"] = []byte("plain notes")
	file := groupMsg(42, th, "")
	file.Message.Document = &tgbot.File{ID: "notes", Name: "../notes.txt"}
	n = len(fk.prompts)
	h.u.phoneUpdate(file)
	h.pumpUntil("file sent", func() bool { return len(fk.prompts) == n+1 })
	if p := fk.prompts[n]; !strings.Contains(p, "I sent you a file: ") || !strings.HasSuffix(p, "-notes.txt") || !strings.Contains(p, h.u.imagesDir()) {
		t.Fatalf("file prompt %q", p)
	}
	doc := groupMsg(42, th, "")
	doc.Message.Document = &tgbot.File{ID: "gone", Mime: "image/png"}
	h.u.phoneUpdate(doc)
	h.pumpUntil("no picture", func() bool { p := f.Last("sendMessage"); return p != nil && strings.Contains(p["text"].(string), "Couldn't take") })
	n = len(fk.prompts)
	h.u.phoneUpdate(groupMsg(7, th, "from someone else"))
	h.u.phoneUpdate(groupMsg(42, th+100, "in no card's topic"))
	if len(fk.prompts) != n {
		t.Fatal("heard a stranger or another topic")
	}
}

// TestRmName: a topic's name is its state, key and ticket, short for a
// phone's topic list; links that aren't tickets stay out.
func TestRmName(t *testing.T) {
	c := &kanban.Card{Num: 77, Status: kanban.StatusWaiting, Title: "A long title nobody reads on a phone",
		Links: []kanban.Link{{Kind: links.KindGitHubPR, Ref: "PR #12"}, {Kind: links.KindJira, Ref: "ABC-123"}}}
	if n := rmName(c); n != "🟡 DESK-77 :: ABC-123" {
		t.Fatal(n)
	}
	c.Links = c.Links[:1]
	if n := rmName(c); n != "🟡 DESK-77" {
		t.Fatal(n)
	}
}

// TestRemoteLife: Done deletes a card's topic (the topic list keeps only
// work under way), back on the board it gets a new one with its last
// message, the archive deletes it too, a topic an older desk closed is
// reopened, a deleted card's topic goes; the topics are kept in
// telegram.json for the next start; a failure says so and the next change
// tries again.
func TestRemoteLife(t *testing.T) {
	h := newHarness(t)
	f := remoteOn(t, h, "summary")
	c := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Fix login", Org: "Acme"})
	h.u.say(c.ID, "claude", "On it.")
	h.u.save()
	h.topicOf(c.ID)
	h.u.moveTo(c.ID, "Done")
	h.u.save()
	if tp := h.topicOf(c.ID); tp != nil || f.Last("deleteForumTopic") == nil || f.Last("closeForumTopic") != nil {
		t.Fatalf("Done kept its topic %+v", tp)
	}
	h.u.moveTo(c.ID, "In progress")
	h.u.save()
	if tp := h.topicOf(c.ID); tp == nil || len(f.Calls("createForumTopic")) != 2 || posted(f)[len(posted(f))-1] != "On it." {
		t.Fatalf("no new topic %+v %q", tp, posted(f))
	}
	h.u.sayMsg(c.ID, kanban.ChatMsg{Who: "tool", Text: "Bash: ls"}) // summary: no tools
	h.u.save()
	h.topicOf(c.ID)
	for _, p := range posted(f) {
		if strings.Contains(p, "Bash: ls") {
			t.Fatal("a tool in a summary")
		}
	}
	h.u.rm.topics[c.ID].Closed = true // as an older desk left it
	h.u.save()
	if tp := h.topicOf(c.ID); tp.Closed || f.Last("reopenForumTopic") == nil {
		t.Fatalf("not reopened %+v", tp)
	}
	b, _ := os.ReadFile(h.u.rmFile())
	if !strings.Contains(string(b), `"thread"`) {
		t.Fatalf("not kept: %s", b)
	}
	h.u.startRemote() // read back
	if h.u.rm.topics[c.ID] == nil {
		t.Fatal("not read back")
	}
	deleted := len(f.Calls("deleteForumTopic"))
	h.u.b.ArchiveCard(c.ID)
	h.u.save()
	if tp := h.topicOf(c.ID); tp != nil || len(f.Calls("deleteForumTopic")) != deleted+1 {
		t.Fatalf("archived kept its topic %+v", tp)
	}
	h.u.b.Restore(c.ID)
	h.u.save()
	h.topicOf(c.ID)
	h.u.b.Delete(c.ID)
	h.u.save()
	if h.topicOf(c.ID) != nil || len(f.Calls("deleteForumTopic")) != deleted+2 {
		t.Fatal("topic of a deleted card kept")
	}

	d := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Again", Org: "Acme"})
	h.u.say(d.ID, "claude", "hi")
	f.Fail("createForumTopic", "Bad Request: not enough rights")
	h.u.save()
	h.topicOf(d.ID)
	if !strings.Contains(h.u.toast, "Telegram: make a topic: telegram createForumTopic: Bad Request: not enough rights") {
		t.Fatal(h.u.toast)
	}
	h.u.save()
	if tp := h.topicOf(d.ID); tp == nil || tp.Thread == 0 {
		t.Fatal("not tried again")
	}
	e := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Elsewhere", Org: "Globex"})
	h.u.say(e.ID, "claude", "hi")
	kid := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "A pilot's", Org: "Acme", Parent: d.ID})
	h.u.say(kid.ID, "claude", "hi")
	h.u.save()
	if h.topicOf(e.ID) != nil || h.topicOf(kid.ID) != nil {
		t.Fatal("a topic for an unmirrored org or a pilot's card")
	}
}

// TestRemoteNotify: Notify sends no conversation, only a turn that ends
// on the user, once per state; the DM doesn't hear of a mirrored card.
func TestRemoteNotify(t *testing.T) {
	h := newHarness(t)
	f := remoteOn(t, h, "notify")
	c := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Fix login", Org: "Acme", Status: kanban.StatusWorking})
	h.u.say(c.ID, "you", "fix it")
	h.u.say(c.ID, "claude", "Working on it")
	h.u.save()
	h.topicOf(c.ID)
	if p := posted(f); len(p) != 1 { // the header only
		t.Fatalf("posted %q", p)
	}
	cc := h.u.b.Card(c.ID)
	cc.Status, cc.Last = kanban.StatusWaiting, "Done, merge?"
	h.u.save()
	h.topicOf(c.ID)
	h.u.save()
	h.topicOf(c.ID)
	var turns int
	for _, p := range posted(f) {
		if strings.Contains(p, "Your turn") && strings.Contains(p, "Done, merge?") {
			turns++
		}
	}
	if turns != 1 {
		t.Fatalf("told %d times: %q", turns, posted(f))
	}
	n := len(f.Calls("sendMessage"))
	h.u.phone.always = true
	h.u.phoneTell(cc, "Your turn")
	h.frame()
	if len(f.Calls("sendMessage")) != n {
		t.Fatal("the DM heard of a mirrored card")
	}
	cc.Status = kanban.StatusDone
	h.u.moveTo(c.ID, "Done")
	h.u.save()
	h.topicOf(c.ID)
	if statusWords(&kanban.Card{}) != "Idle" || rmEmoji("x") != "⏸" || rmEmoji(kanban.StatusFailed) != "❌" || rmEmoji(kanban.StatusBlocked) != "🔴" || rmEmoji(kanban.StatusReview) != "👀" || rmEmoji(kanban.StatusDone) != "✅" {
		t.Fatal("emoji or words")
	}
}

func js(v any) string { b, _ := json.Marshal(v); return string(b) }

// TestRemoteRun: a job stops at the step Telegram refuses, keeping what
// went before it, and names that step; an edit to the same text isn't a
// failure. The header shows links, their state, cost and context.
func TestRemoteRun(t *testing.T) {
	f := tgFake(t)
	bot, ctx := &tgbot.Bot{Token: "T0K"}, context.Background()
	full := func() *rmJob {
		return &rmJob{id: "c1", t: rmTopic{Chat: -100, Thread: 4, Header: 5, AskMsg: 6, Ask: "r0", AskText: "old", Closed: true},
			act: "reopen", name: "n", head: "h", posts: []string{"p"}, askEnd: "old → answered", ask: &tgbot.Out{Markdown: "q"}, askID: "r1", sent: 3}
	}
	for _, step := range []struct{ method, what string }{
		{"reopenForumTopic", "reopen a topic"}, {"editForumTopic", "rename a topic"}, {"editMessageText", "change the header"},
		{"sendRichMessage", "send a message"},
	} {
		f.Fail(step.method, "Bad Request: no")
		j := full()
		rmRun(ctx, bot, j)
		if j.failure != step.what+": telegram "+step.method+": Bad Request: no" {
			t.Fatalf("%s: %q", step.method, j.failure)
		}
	}
	j := full()
	j.posts = nil
	f.Fail("editMessageText", "Bad Request: message is not modified") // the header: as it was
	rmRun(ctx, bot, j)
	if j.failure != "" || j.t.Ask != "r1" || j.t.Sent != 3 || j.t.Closed {
		t.Fatalf("%q %+v", j.failure, j.t)
	}
	j = full()
	j.head, j.posts = "", nil
	f.Fail("editMessageText", "Bad Request: message to edit not found")
	rmRun(ctx, bot, j)
	if !strings.HasPrefix(j.failure, "close a request") || j.t.Sent != 3 {
		t.Fatalf("%q", j.failure)
	}
	j = full()
	j.head, j.posts, j.askEnd = "", nil, ""
	f.Fail("sendRichMessage", "Forbidden")
	rmRun(ctx, bot, j)
	if !strings.HasPrefix(j.failure, "send a request") {
		t.Fatalf("%q", j.failure)
	}
	for _, step := range []struct{ method, act string }{{"deleteForumTopic", "delete"}} {
		f.Fail(step.method, "Bad Request: no")
		j := &rmJob{t: rmTopic{Chat: -100, Thread: 4}, act: step.act}
		rmRun(ctx, bot, j)
		if !strings.HasPrefix(j.failure, step.act) {
			t.Fatalf("%s %q", step.method, j.failure)
		}
	}
	for _, method := range []string{"sendRichMessage", "pinChatMessage"} {
		f.Fail(method, "Bad Request: no")
		j := &rmJob{t: rmTopic{Chat: -100}, create: true, name: "n", head: "h"}
		rmRun(ctx, bot, j)
		if j.failure == "" || j.t.Thread == 0 {
			t.Fatalf("%s %q %+v", method, j.failure, j.t)
		}
	}
	c := &kanban.Card{Title: "x", Links: []kanban.Link{{URL: "https://github.com/globex-org/web/pull/12", Ref: "PR #12", State: "approved"}, {URL: "https://globex.atlassian.net/browse/ABC-123"}},
		Session: kanban.Session{Turns: 3, Cost: 0.5, Window: 200000, Context: 50000}}
	if h := rmHead(c, "Review"); !strings.Contains(h, "- [PR \\#12](https://github.com/globex-org/web/pull/12) · approved") || !strings.Contains(h, "ABC-123") ||
		!strings.Contains(h, "<footer>$0.50 · 3 turns · context 25%</footer>") {
		t.Fatal(h)
	}
}

// TestRmPin: with the account logged in, each mirrored group's General
// topic and its Infra topic are pinned at the top of its list, once a run;
// no account, or a group desk didn't make, pins nothing; a refusal says so.
func TestRmPin(t *testing.T) {
	h := newHarness(t)
	remoteOn(t, h, "full")
	var mu sync.Mutex
	var pins [][]int
	var fail error
	old := tgPinTopics
	tgPinTopics = func(_ tgsetup.Account, _ context.Context, c tgsetup.Chat, topics ...int) error {
		mu.Lock()
		defer mu.Unlock()
		if c.Channel != 555 || c.Access != 5550 {
			t.Errorf("chat %+v", c)
		}
		pins = append(pins, topics)
		return fail
	}
	t.Cleanup(func() { tgPinTopics = old })
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(pins) }
	acct := "[telegram]\ntoken = \"T0K\"\nchat_id = 42\napi_id = 5\n[telegram.api_hash]\ntoken = \"h\"\n[telegram.orgs.Acme]\nmirror = \"full\"\nchat_id = -100555\n"
	settingsFile(t, acct+"channel = 555\naccess_hash = 5550\n")
	h.u.rmConfig() // not logged in
	settingsFile(t, acct)
	os.MkdirAll(filepath.Dir(orgs.TelegramSession()), 0o700)
	os.WriteFile(orgs.TelegramSession(), nil, 0o600)
	h.u.rmConfig() // no channel: linked by hand
	if count() != 0 {
		t.Fatalf("pinned %v", pins)
	}
	settingsFile(t, acct+"channel = 555\naccess_hash = 5550\n")
	h.u.rmConfig()
	h.pumpUntil("General pinned", func() bool { return count() == 1 })
	h.u.rm.topics[rmInfraKey("Acme")] = &rmTopic{Chat: -100555, Thread: 7}
	fail = errors.New("pin topic 7: CHAT_ADMIN_REQUIRED")
	h.u.rmPin()
	h.u.rmPin() // tried once a run
	h.pumpUntil("the refusal", func() bool { return strings.Contains(h.u.toast, "CHAT_ADMIN_REQUIRED") })
	if count() != 2 || fmt.Sprint(pins) != "[[1] [7]]" {
		t.Fatalf("pins %v", pins)
	}
}

// TestRmPack: parts share a post, divided, until the next would pass
// RichMax; a part longer than one is cut.
func TestRmPack(t *testing.T) {
	big := strings.Repeat("w", tgfmt.RichMax-2)
	p := rmPack([]string{"a", "b", big, strings.Repeat("x\n\n", tgfmt.RichMax)})
	if len(p) < 4 || p[0] != "a\n\n---\n\nb" || p[1] != big {
		t.Fatalf("%d posts, first %.40q", len(p), p[0])
	}
	for _, s := range p {
		if tgfmt.Len(s) > tgfmt.RichMax {
			t.Fatal("a post over RichMax")
		}
	}
	if rmPack(nil) != nil {
		t.Fatal("posts from nothing")
	}
}
