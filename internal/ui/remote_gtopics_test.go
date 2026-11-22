package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/tgbot"
)

// TestGeneralTopics: with the bot's topics mode on, the private chat gets
// Board, Inbox, Digest and Settings, each with a pinned header that
// follows the board; a card that needs the user comes to Inbox, and a
// command typed in a topic is answered there. Off, nothing is made.
func TestGeneralTopics(t *testing.T) {
	h := newHarness(t)
	f := remoteOn(t, h, "")
	h.u.orgList = []string{"Acme", "Globex"}
	f.Me = map[string]any{"id": 7, "has_topics_enabled": false}
	h.u.phoneTopics() // the mode is off: no topics
	h.pumpUntil("asked", func() bool { return f.Last("getMe") != nil })
	h.frame()
	if h.u.phone.topics || f.Last("createForumTopic") != nil {
		t.Fatalf("topics without the mode: %v %v", h.u.phone.topics, f.Last("createForumTopic"))
	}

	f.Me = map[string]any{"id": 1, "has_topics_enabled": true}
	h.u.phoneTopics()
	h.pumpUntil("the topics", func() bool { return h.u.rm.topics[gtKey("settings")] != nil })
	h.topicOf("")
	for _, g := range gtopics {
		tp := h.u.rm.topics[gtKey(g.key)]
		if tp == nil || tp.Chat != 42 || tp.Name != g.name || tp.Header == 0 {
			t.Fatalf("%s: %+v", g.key, tp)
		}
	}
	if head := h.u.rm.topics[gtKey("settings")].Head; !strings.Contains(head, "Acme · off") || !strings.Contains(head, "Globex · no group") || !strings.Contains(head, "focus: **off**") {
		t.Fatalf("settings header %q", head)
	}
	if n := len(f.Calls("pinChatMessage")); n != 4 {
		t.Fatalf("pinned %d", n)
	}

	c := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Fix login", Org: "Acme", Status: kanban.StatusWorking})
	h.u.save()
	h.topicOf("")
	if head := h.u.rm.topics[gtKey("board")].Head; !strings.Contains(head, "Fix login") {
		t.Fatalf("board header %q", head)
	}
	if f.Last("editMessageText") == nil {
		t.Fatal("the header not changed in place")
	}
	n := len(f.Calls("createForumTopic"))
	h.u.save()
	h.topicOf("")
	if len(f.Calls("createForumTopic")) != n {
		t.Fatal("made again")
	}

	inbox := h.u.gtThread("inbox")
	h.u.phoneTell(h.u.b.Card(c.ID), "your turn")
	h.pumpUntil("told", func() bool { p := f.Last("sendMessage"); return p != nil && strings.Contains(fmtAny(p["text"]), "your turn") })
	if p := f.Last("sendMessage"); fmt.Sprint(p["message_thread_id"]) != fmt.Sprint(inbox) {
		t.Fatalf("told in %v, not Inbox %d", p["message_thread_id"], inbox)
	}

	board := h.u.gtThread("board")
	m := dm("/inbox")
	m.Message.Thread = board
	k := len(posted(f))
	h.u.phoneUpdate(m)
	h.pumpUntil("answered", func() bool { return len(posted(f)) > k })
	if p := f.Last("sendRichMessage"); fmt.Sprint(p["message_thread_id"]) != fmt.Sprint(board) {
		t.Fatalf("answered in %v, not Board %d", p["message_thread_id"], board)
	}

	h.u.phone.chat = 99 // paired again elsewhere: the old chat's topics stay as they are
	h.u.save()
	h.topicOf("")
	if h.u.gtThread("board") != 0 || h.u.rm.topics[gtKey("board")].Chat != 42 {
		t.Fatal("another chat's topic used")
	}
}

// TestClipLines: a header too long is cut at a line's end.
func TestClipLines(t *testing.T) {
	if got := clipLines("short", 10); got != "short" {
		t.Fatalf("%q", got)
	}
	if got := clipLines("- one\n- two\n- three", 14); got != "- one\n- two\n…" {
		t.Fatalf("%q", got)
	}
	if got := clipLines("a single long line", 8); got != "…" {
		t.Fatalf("%q", got)
	}
}

// TestSaveFile: a file from Telegram keeps its name, made safe; a picture
// or a file that can't be saved says so.
func TestSaveFile(t *testing.T) {
	h := newHarness(t)
	for name, want := range map[string]string{"../x/notes.txt": "-notes.txt", "": "-file", ".env": "-file.env"} {
		p, err := h.u.saveFile([]byte("x"), name)
		if err != nil || !strings.HasSuffix(p, want) || filepath.Dir(p) != h.u.imagesDir() {
			t.Fatalf("%q: %s %v", name, p, err)
		}
	}
	os.RemoveAll(h.u.imagesDir())
	os.WriteFile(h.u.imagesDir(), nil, 0o600) // a file where the folder goes
	if _, err := h.u.saveFile([]byte("x"), "a"); err == nil {
		t.Fatal("saved under a file")
	}
	f := tgFake(t)
	f.Files = map[string][]byte{"pic": []byte("\x89PNG\r\n\x1a\n...."), "doc": []byte("text")}
	h.u.phone = &phoneState{bot: &tgbot.Bot{Token: "T0K"}}
	for _, m := range []*tgbot.Message{{Photo: []tgbot.File{{ID: "pic"}}}, {Document: &tgbot.File{ID: "doc"}}} {
		var why string
		h.u.tgTake(m, func(s string) { why = s }, func(string, string, string) { t.Fatal("taken") })
		h.pumpUntil("refused", func() bool { return why != "" })
		if !strings.Contains(why, "Couldn't take the file") {
			t.Fatal(why)
		}
	}
}
