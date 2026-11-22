package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lfdominguez/desk/internal/draw"
	"github.com/lfdominguez/desk/internal/gitw"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/tgbot"
)

// markupData is the callback data of button text in a call's reply_markup
// (a JSON body's object, or a multipart upload's string).
func markupData(t *testing.T, p map[string]any, text string) string {
	t.Helper()
	b, ok := p["reply_markup"].(string)
	if !ok {
		j, _ := json.Marshal(p["reply_markup"])
		b = string(j)
	}
	var kb struct {
		Rows [][]tgbot.Button `json:"inline_keyboard"`
	}
	json.Unmarshal([]byte(b), &kb)
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

// dmPress presses a button in the paired private chat.
func dmPress(data string) tgbot.Update {
	return tgbot.Update{Callback: &tgbot.Callback{ID: "cb", Data: data, From: tgbot.User{ID: 42},
		Message: &tgbot.Message{ID: 5, Chat: tgbot.Chat{ID: 42, Type: "private"}}}}
}

// TestRemoteDrawings: /drawings offers the drawings as buttons, and one
// pressed comes as a picture; in a card's topic, its own come first; none,
// or an organization desk doesn't have, says so.
func TestRemoteDrawings(t *testing.T) {
	h := newHarness(t)
	f := remoteOn(t, h, "full")
	h.u.orgList = []string{"Acme", "Globex"}
	h.ask(f, "/drawings")
	if !strings.Contains(posted(f)[len(posted(f))-1], "No drawings yet") {
		t.Fatal(posted(f))
	}
	if !strings.Contains(h.ask(f, "/drawings Nope"), "no organization called Nope") {
		t.Fatal(posted(f))
	}
	h.drawing("Flow", rect(0, 0, 50, 50, "#ffffff"))
	h.ask(f, "/drawings Acme")
	h.u.phoneUpdate(dmPress(buttonData(t, f, "🖼 Flow")))
	h.pumpUntil("the picture", func() bool { return f.Last("sendPhoto") != nil })
	if p := f.Last("sendPhoto"); !strings.HasPrefix(fmt.Sprint(p["file"]), "\x89PNG") || p["caption"] != "🖼 Flow" || fmt.Sprint(p["chat_id"]) != "42" {
		t.Fatalf("%v %v", p["caption"], p["chat_id"])
	}
	if f.Last("editMessageReplyMarkup") == nil {
		t.Fatal("the buttons stayed")
	}

	c := acmeCard(h)
	os.MkdirAll(h.u.cardDrawingsDir("Acme", c.ID), 0o755)
	draw.NewScene().Save(filepath.Join(h.u.cardDrawingsDir("Acme", c.ID), "Mine.excalidraw"))
	h.u.say(c.ID, "claude", "On it.")
	h.u.save()
	tp := h.topicOf(c.ID)
	n := len(posted(f))
	h.u.phoneUpdate(groupMsg(42, tp.Thread, "/drawings"))
	h.pumpUntil("offered", func() bool { return len(posted(f)) > n })
	if p := f.Last("sendRichMessage"); fmt.Sprint(p["message_thread_id"]) != fmt.Sprint(tp.Thread) || !strings.Contains(js(p["reply_markup"]), "Mine") || !strings.Contains(js(p["reply_markup"]), "Flow") {
		t.Fatal(p)
	}
	k := len(f.Calls("sendPhoto"))
	h.u.phoneUpdate(groupPress(42, markupData(t, f.Last("sendRichMessage"), "🖼 Mine")))
	h.pumpUntil("its picture", func() bool { return len(f.Calls("sendPhoto")) > k })

	f.Fail("sendPhoto", "Bad Request: no photo")
	h.u.tgDrawing(42, 0, filepath.Join(h.u.drawingsDir("Acme"), "Flow.excalidraw"))
	h.pumpUntil("its failure", func() bool { return strings.Contains(h.u.toast, "no photo") })
	os.WriteFile(filepath.Join(h.u.drawingsDir("Acme"), "Flow.excalidraw"), []byte("not json"), 0o644)
	delete(h.u.draws, filepath.Join(h.u.drawingsDir("Acme"), "Flow.excalidraw"))
	h.u.tgDrawing(42, 0, filepath.Join(h.u.drawingsDir("Acme"), "Flow.excalidraw"))
	h.pumpUntil("refused", func() bool { return strings.Contains(posted(f)[len(posted(f))-1], "Couldn") })
}

// TestRemoteSuggest: a Claude's drawing suggestion comes as its after, a
// picture with Approve and Dismiss: in the card's topic when it has one,
// else the private chat; Approve applies it on the file or the open
// drawing, Dismiss drops it, a second press finds it gone.
func TestRemoteSuggest(t *testing.T) {
	h := newHarness(t)
	f := remoteOn(t, h, "full")
	c := acmeCard(h)
	h.drawing("S", rect(0, 0, 50, 50, "#ffffff"))
	path := filepath.Join(h.u.drawingsDir("Acme"), "S.excalidraw")
	delete(h.u.draws, path)

	h.tool(c.ID, "drawing_suggest", `{"drawing":"S","note":"an ellipse","add":[{"type":"ellipse","id":"e1"}]}`)
	h.pumpUntil("the picture", func() bool { return f.Last("sendPhoto") != nil })
	p := f.Last("sendPhoto")
	if fmt.Sprint(p["chat_id"]) != "42" || !strings.Contains(fmt.Sprint(p["caption"]), "suggests on S: an ellipse") {
		t.Fatal(p["chat_id"], p["caption"])
	}
	h.u.phoneUpdate(dmPress(markupData(t, p, "Approve")))
	h.pumpUntil("approved", func() bool { return len(loadSide(path).Suggestions) == 0 })
	if s, _ := draw.Load(path); len(s.Elements) != 2 {
		t.Fatalf("not applied: %d", len(s.Elements))
	}
	if h.u.drawApproveAt(path, "gone") != "Already gone" || h.u.drawDismissAt(path, "gone") != "Already gone" {
		t.Fatal("gone")
	}

	h.u.say(c.ID, "claude", "On it.")
	h.u.save()
	tp := h.topicOf(c.ID)
	h.tool(c.ID, "drawing_suggest", `{"drawing":"S","note":"a diamond","add":[{"type":"diamond"}]}`)
	h.pumpUntil("in the topic", func() bool {
		return fmt.Sprint(f.Last("sendPhoto")["message_thread_id"]) == fmt.Sprint(tp.Thread)
	})
	h.u.phoneUpdate(groupPress(42, markupData(t, f.Last("sendPhoto"), "Dismiss")))
	h.pumpUntil("dismissed", func() bool { return len(loadSide(path).Suggestions) == 0 })

	h.u.openDrawing("Acme", path) // open: through its editor
	st := h.u.draws[path]
	h.tool(c.ID, "drawing_suggest", `{"drawing":"S","note":"a line","add":[{"type":"line"}]}`)
	id := st.side.Suggestions[0].ID
	if h.u.drawApproveAt(path, id) != "Approved" || len(st.ed.Scene.Elements) != 3 || h.u.drawApproveAt(path, id) != "It's gone, or no longer fits" {
		t.Fatal(len(st.ed.Scene.Elements))
	}
	h.tool(c.ID, "drawing_suggest", `{"drawing":"S","note":"a box","add":[{"type":"rectangle"}]}`)
	id = st.side.Suggestions[0].ID
	if h.u.drawDismissAt(path, id) != "Dismissed" || h.u.drawDismissAt(path, id) != "Already gone" {
		t.Fatal("dismiss open")
	}

	delete(h.u.draws, path) // closed, and no longer fits: the file can't be read
	h.tool(c.ID, "drawing_suggest", `{"drawing":"S","note":"x","add":[{"type":"rectangle"}]}`)
	id = loadSide(path).Suggestions[0].ID
	os.WriteFile(path, []byte("not json"), 0o644)
	if s := h.u.drawApproveAt(path, id); !strings.HasPrefix(s, "Couldn't") {
		t.Fatal(s)
	}
	if h.u.drawDismissAt(path, id) != "Dismissed" {
		t.Fatal("dismiss closed")
	}
}

// TestRemoteSuggestQuiet: unpaired, or a desk window with the focus and no
// topic, nothing is sent.
func TestRemoteSuggestQuiet(t *testing.T) {
	h := newHarness(t)
	f := remoteOn(t, h, "")
	sg := drawSuggestion{ID: "x", Note: "n"}
	h.u.mainPane.focused = true
	h.u.tgSuggest("p", drawWho{}, sg, draw.NewScene())
	h.u.phone.chat = 0
	h.u.tgSuggest("p", drawWho{}, sg, draw.NewScene())
	h.frame()
	if f.Last("sendPhoto") != nil || len(h.u.phone.runs) != 0 {
		t.Fatal("sent")
	}
}

// TestRemoteReview: /review in a card's topic sends its branch with the
// diff as a file; Push and Merge each ask again, then run, their end said
// in the topic; a card without a worktree, or unknown in /review, says so.
func TestRemoteReview(t *testing.T) {
	h := newHarness(t)
	_, origin := gitRepo(t, h, "Acme")
	h.orgToml("Acme", "[git]\nworktrees = true\n")
	f := remoteOn(t, h, "full")
	c := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Fix login", Org: "Acme"})
	worked(h, c.ID)
	wt := h.u.b.Card(c.ID).Worktree
	os.WriteFile(filepath.Join(wt, "a.txt"), []byte("one\ntwo\n"), 0o644)
	git(t, wt, "commit", "-q", "-am", "Add two")
	os.WriteFile(filepath.Join(wt, "bin"), []byte("a\x00b"), 0o644)
	h.u.say(c.ID, "claude", "Done.")
	h.u.save()
	tp := h.topicOf(c.ID)

	h.u.phoneUpdate(groupMsg(42, tp.Thread, "/review"))
	h.pumpUntil("the diff", func() bool { return f.Last("sendDocument") != nil })
	md := posted(f)[len(posted(f))-1]
	for _, s := range []string{"1 commit ahead, 0 behind", "1 change not committed", "Add two", "`a.txt` \\+1 −0", "`bin` binary"} {
		if !strings.Contains(md, s) {
			t.Fatalf("no %q in %q", s, md)
		}
	}
	if d := f.Last("sendDocument"); !strings.Contains(fmt.Sprint(d["file"]), "+two") || d["file_name"] != c.Key()+".diff" {
		t.Fatal(d)
	}
	os.Remove(filepath.Join(wt, "bin"))

	h.u.phoneUpdate(groupPress(42, buttonData(t, f, "Push")))
	h.pumpUntil("asked again", func() bool { return strings.Contains(posted(f)[len(posted(f))-1], "Push `") })
	h.u.phoneUpdate(groupPress(42, buttonData(t, f, "Push")))
	h.pumpUntil("pushed", func() bool { return strings.Contains(posted(f)[len(posted(f))-1], "pushed") })
	if b := git(t, origin, "branch", "--list", strings.ToLower(c.Key())); b == "" {
		t.Fatal("not pushed")
	}

	n := len(posted(f))
	h.u.tgReview(42, 0, c.ID)
	h.pumpUntil("again", func() bool { return len(posted(f)) > n })
	h.u.phoneUpdate(dmPress(buttonData(t, f, "Merge")))
	h.pumpUntil("asked", func() bool { return strings.Contains(posted(f)[len(posted(f))-1], "Merge `") })
	h.u.phoneUpdate(dmPress(buttonData(t, f, "Merge")))
	h.pumpUntil("merged", func() bool { return strings.Contains(posted(f)[len(posted(f))-1], "merged into main") })
	if col, _ := h.u.b.Find(c.ID); h.u.b.Columns[col].Title != "Done" {
		t.Fatal("not in Done")
	}
	n = len(posted(f))
	h.u.tgReview(42, 0, c.ID) // merged: no buttons
	h.pumpUntil("merged already", func() bool { return len(posted(f)) > n })
	if p := f.Last("sendRichMessage"); p["reply_markup"] != nil || !strings.Contains(posted(f)[n], "Merged already") {
		t.Fatal(p)
	}

	f.Fail("sendRichMessage", "Bad Request: refused")
	h.u.tgReview(42, 0, c.ID)
	h.pumpUntil("its failure", func() bool { return strings.Contains(h.u.toast, "refused") })
	if !strings.Contains(h.ask(f, "/review ABC-123"), "No card ABC") {
		t.Fatal("unknown")
	}
	g := h.u.b.Add(0, kanban.Card{Title: "plain", Org: "Acme"})
	if !strings.Contains(h.ask(f, "/review "+g.Key()), "no worktree") {
		t.Fatal("no worktree")
	}
	h.u.b.Card(g.ID).Worktree = filepath.Join(t.TempDir(), "gone")
	if !strings.Contains(h.ask(f, "/review "+g.Key()), "couldn") {
		t.Fatal("unreadable")
	}
}

// TestRemoteConfirmRefusals: a confirmation for a card that's gone, a
// Cancel, and an action that can't run now say so.
func TestRemoteConfirmRefusals(t *testing.T) {
	h := newHarness(t)
	f := remoteOn(t, h, "")
	if h.u.tgConfirm(42, 0, "gone", "push", "Push") != "The card is gone" {
		t.Fatal("gone")
	}
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme", Branch: "acme-1", Base: "main"})
	h.u.tgConfirm(42, 0, c.ID, "pr", "Open PR")
	h.pumpUntil("asked", func() bool { return len(posted(f)) > 0 })
	if !strings.Contains(posted(f)[0], "open a request into `main`") {
		t.Fatal(posted(f))
	}
	h.u.phoneUpdate(dmPress(buttonData(t, f, "Cancel")))
	h.u.phoneUpdate(dmPress(buttonData(t, f, "Open PR")))
	h.pumpUntil("refused", func() bool { return strings.Contains(posted(f)[len(posted(f))-1], "Not now") })
}

// TestRvText: a branch not merged, with several commits and more files
// than listed.
func TestRvText(t *testing.T) {
	fs := make([]gitw.File, 32)
	for i := range fs {
		fs[i] = gitw.File{Path: fmt.Sprint("f", i), New: "x\n"}
	}
	md, patch := rvText("ABC-1", gitw.Status{Branch: "b", Base: "main", Ahead: 2, Dirty: []string{"a", "b"}}, fs)
	if !strings.Contains(md, "2 commits ahead") || !strings.Contains(md, "2 changes not") || !strings.Contains(md, "… 2 more") || !strings.Contains(patch, "+++ b/f31") {
		t.Fatal(md)
	}
}

// TestRemoteCanvas: /canvas in a card's topic, or /canvas KEY in the
// private chat, sends its canvas as a picture; an unknown key says so.
func TestRemoteCanvas(t *testing.T) {
	h := newHarness(t)
	f := remoteOn(t, h, "full")
	c := acmeCard(h)
	h.u.say(c.ID, "claude", "On it.")
	h.u.save()
	tp := h.topicOf(c.ID)
	h.u.phoneUpdate(groupMsg(42, tp.Thread, "/canvas"))
	h.pumpUntil("the picture", func() bool { return f.Last("sendPhoto") != nil })
	p := f.Last("sendPhoto")
	if !strings.HasPrefix(fmt.Sprint(p["file"]), "\x89PNG") || p["caption"] != "🧭 "+c.Key()+"'s canvas" || fmt.Sprint(p["message_thread_id"]) != fmt.Sprint(tp.Thread) {
		t.Fatalf("%v %v", p["caption"], p["message_thread_id"])
	}
	if out := os.Getenv("DESK_CANVAS_PNG"); out != "" {
		os.WriteFile(out, []byte(fmt.Sprint(p["file"])), 0o644)
	}
	if !strings.Contains(h.ask(f, "/canvas NOPE-1"), "No card NOPE") {
		t.Fatal(posted(f))
	}
	n := len(f.Calls("sendPhoto"))
	h.u.phoneUpdate(dm("/canvas " + c.Key()))
	h.pumpUntil("the second", func() bool { return len(f.Calls("sendPhoto")) > n })
	if s := cardScene(h.u.cardGraph(h.u.b.Card(c.ID), time.Now())); len(s.Elements) < 3 {
		t.Fatalf("%d elements", len(s.Elements))
	}
}

// TestRemoteRework: Rework under a suggestion's picture asks for a
// comment, and the next message in that chat goes back to its Claude (a
// closed drawing's own Claude too); a command cancels; the infra Claude's
// have no Rework.
func TestRemoteRework(t *testing.T) {
	h := newHarness(t)
	f := remoteOn(t, h, "full")
	cid := acmeCard(h).ID // the card moves slots as it starts
	h.drawing("S", rect(0, 0, 50, 50, "#ffffff"))
	path := filepath.Join(h.u.drawingsDir("Acme"), "S.excalidraw")
	delete(h.u.draws, path)

	h.tool(cid, "drawing_suggest", `{"drawing":"S","note":"an ellipse","add":[{"type":"ellipse"}]}`)
	h.pumpUntil("the picture", func() bool { return f.Last("sendPhoto") != nil })
	h.u.phoneUpdate(dmPress(markupData(t, f.Last("sendPhoto"), "Rework")))
	h.pumpUntil("asked", func() bool {
		return len(posted(f)) > 0 && strings.Contains(posted(f)[len(posted(f))-1], "Your next message here goes back")
	})
	h.u.phoneUpdate(dm("smaller, please"))
	h.pumpUntil("sent back", func() bool { return strings.Contains(strings.Join(posted(f), "\n"), "Sent back to") })
	if !loadSide(path).Suggestions[0].Sent {
		t.Fatal("not marked")
	}
	if !slices.ContainsFunc(h.u.chat(cid), func(m kanban.ChatMsg) bool { return strings.Contains(m.Text, "smaller, please") }) {
		t.Fatalf("not told: %+v", h.u.chat(cid))
	}
	if h.u.phone.rework != nil {
		t.Fatal("still waiting")
	}

	h.u.phoneUpdate(dmPress(markupData(t, f.Last("sendPhoto"), "Rework"))) // a command cancels: it runs as itself
	h.ask(f, "/help")
	if h.u.phone.rework != nil {
		t.Fatal("not cancelled")
	}

	delete(h.u.draws, path) // the drawing's own Claude, its drawing closed: loaded to reach it
	if _, err := h.u.drawSuggest(drawWho{key: "draw:" + path, name: "S's Claude", org: "Acme", mine: path}, "S", "a box", draw.Patch{Add: []json.RawMessage{json.RawMessage(`{"type":"rectangle"}`)}}); err != nil {
		t.Fatal(err)
	}
	side := loadSide(path)
	id := side.Suggestions[slices.IndexFunc(side.Suggestions, func(s drawSuggestion) bool { return s.Who == "draw:"+path })].ID
	if s := h.u.drawReworkAt("Acme", path, id, "wider"); !strings.HasPrefix(s, "Sent back to S's Claude") || h.u.draws[path] == nil {
		t.Fatal(s)
	}
	if s := h.u.drawReworkAt("Acme", path, "gone", "x"); s != "That suggestion is gone." {
		t.Fatal(s)
	}
	h.u.draws[path].side.Suggestions[0].Who = "draw:/nowhere" // its Claude is gone
	if s := h.u.drawReworkAt("Acme", path, h.u.draws[path].side.Suggestions[0].ID, "x"); !strings.HasPrefix(s, "Couldn't send it back") {
		t.Fatal(s)
	}
	if s := h.u.drawReworkAt("Acme", filepath.Join(h.u.drawingsDir("Acme"), "none.excalidraw"), "x", "y"); !strings.HasPrefix(s, "Couldn't") {
		t.Fatal(s)
	}

	h.u.tgSuggest(path, drawWho{org: "Acme"}, drawSuggestion{ID: "i", Who: "infra:Acme", Name: "Infra", Note: "n"}, draw.NewScene())
	var infra map[string]any
	h.pumpUntil("infra's", func() bool {
		for _, c := range f.Calls("sendPhoto") {
			if strings.HasPrefix(fmt.Sprint(c.Params["caption"]), "Infra suggests") {
				infra = c.Params
			}
		}
		return infra != nil
	})
	if strings.Contains(fmt.Sprint(infra["reply_markup"]), "Rework") {
		t.Fatal("infra has Rework")
	}
}
