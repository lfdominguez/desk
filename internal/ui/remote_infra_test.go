package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/tgbot/tgfake"
)

// said is the text of the last plain message sent.
func said(f *tgfake.Server) string { return fmt.Sprint(f.Last("sendMessage")["text"]) }

// TestRemoteInfraCards: a card's infra session's approval comes to the
// card's topic (after the card's own request), not the Infra topic, unless
// the card has no topic; /screen sends a pane's picture.
func TestRemoteInfraCards(t *testing.T) {
	h := newHarness(t)
	f := remoteOn(t, h, "full")
	var answers []string
	replace(t, &infraAnswer, func(_ *infra.Workspace, id int, v string) { answers = append(answers, fmt.Sprint(id, v)) })
	c := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Fix DNS", Org: "Acme", Status: kanban.StatusWorking})
	h.u.say(c.ID, "you", "fix the DNS")
	h.u.save()
	key := h.u.b.Card(c.ID).Key()
	h.u.asks[c.ID] = &askState{r: &claude.Request{ID: "r1", Tool: "Bash", Line: "dig example.com"}}
	h.u.infraStatus([]infra.TabStatus{
		{Org: "Acme", Card: key, Name: "Acme · " + key, State: "approval", Pending: []infra.Approval{{ID: 20, Host: "ns1", Cmd: "systemctl reload bind9"}}},
		{Org: "Acme", Card: "DESK-99", Name: "Acme · DESK-99", State: "approval", Pending: []infra.Approval{{ID: 21, Cmd: "uptime"}}},
	})
	if top := h.topicOf(c.ID); top.Ask != "r1" {
		t.Fatalf("the card's own request first: %q", top.Ask)
	}
	if top := h.topicOf(rmInfraKey("Acme")); top.Ask != "infra:21" {
		t.Fatalf("a card without a topic: Infra's %q", top.Ask)
	}
	delete(h.u.asks, c.ID)
	h.u.save()
	if top := h.topicOf(c.ID); top.Ask != "infra:20" || !strings.Contains(top.AskText, "wants to run on `ns1`") {
		t.Fatalf("card's topic %+v", top)
	}
	h.u.phoneUpdate(groupPress(42, buttonData(t, f, "Approve")))
	if strings.Join(answers, ",") != "20y" {
		t.Fatalf("answers %v", answers)
	}
	h.u.infraStatus([]infra.TabStatus{{Org: "Acme", Card: key, Name: "Acme · " + key, State: "working",
		Panes: []infra.PaneStatus{{Name: "ns1", Status: "idle prompt", Picture: func() []byte { return []byte("\x89PNG pic") }}}}})
	if top := h.topicOf(c.ID); top.Ask != "" {
		t.Fatalf("still asking %q", top.Ask)
	}
	settingsFile(t, "[telegram]\ntoken = \"T0K\"\nchat_id = 42\nscreen = true\n[telegram.orgs.Acme]\nmirror = \"full\"\nchat_id = -100555\n")
	h.u.phoneUpdate(groupMsg(42, h.topicOf(rmInfraKey("Acme")).Thread, "/screen"))
	h.pumpUntil("photo", func() bool { return f.Last("sendPhoto") != nil })
	if p := f.Last("sendPhoto"); fmt.Sprint(p["caption"]) != "🖥 ns1 · idle prompt" {
		t.Fatalf("photo %v", p)
	}
	f.Fail("sendPhoto", "Bad Request: no")
	h.u.phoneUpdate(groupMsg(42, h.topicOf(rmInfraKey("Acme")).Thread, "/screen"))
	h.pumpUntil("toast", func() bool { return strings.Contains(h.u.toast, "Telegram: ") })
}

// TestRemoteInfra: a mirrored organization's infra gets an Infra topic
// named by its state, a header of its tabs and terminals, its Claude's
// replies, and each approval with Approve, Deny, Always (no Always on a
// diff), answered from the topic; what's typed there goes to its Claude;
// /screen shows a terminal only when Settings allows.
func TestRemoteInfra(t *testing.T) {
	h := newHarness(t)
	f := remoteOn(t, h, "full")
	var answers []string
	replace(t, &infraAnswer, func(_ *infra.Workspace, id int, v string) { answers = append(answers, fmt.Sprint(id, v)) })
	var says []string
	sayOK := true
	replace(t, &infraSay, func(_ *infra.Workspace, org, text string) bool { says = append(says, org+": "+text); return sayOK })

	h.u.infraStatus(nil) // no session: no topic
	if h.topicOf(rmInfraKey("Acme")) != nil {
		t.Fatal("a topic without infra")
	}
	web := infra.PaneStatus{Name: "web1", Status: "running make deploy", Screen: func() string { return "$ make deploy\nok\n" }}
	db := infra.PaneStatus{Name: "db1", Status: "idle prompt", Screen: func() string { return "$ " }}
	h.u.infraStatus([]infra.TabStatus{
		{Org: "Acme", Name: "Acme", State: "approval", Last: "Disk is fine.", Panes: []infra.PaneStatus{db, web},
			Pending: []infra.Approval{{ID: 7, Host: "web1", Cmd: "systemctl restart nginx", Reason: "reload the config"}}},
		{Org: "Acme", Card: "DESK-3", Name: "Acme · DESK-3", State: "working", Last: "not posted"},
		{Org: "Globex", Name: "Globex", State: "idle"}, // not mirrored
	})
	top := h.topicOf(rmInfraKey("Acme"))
	if top == nil || top.Name != "🔐 Infra" || h.topicOf(rmInfraKey("Globex")) != nil {
		t.Fatalf("topic %+v", top)
	}
	ps := strings.Join(posted(f), "\n=====\n")
	for _, want := range []string{"Infra · Acme", "- `web1` running make deploy", "🟢 **Acme · DESK\\-3** · working", "Disk is fine.",
		"Acme wants to run on `web1`", "reload the config", "systemctl restart nginx"} {
		if !strings.Contains(ps, want) {
			t.Fatalf("no %q in %s", want, ps)
		}
	}
	if strings.Contains(ps, "not posted") {
		t.Fatal("a card session's reply posted")
	}
	h.u.phoneUpdate(groupPress(42, buttonData(t, f, "Always")))
	h.u.phoneUpdate(groupPress(7, buttonData(t, f, "Approve"))) // a stranger
	h.u.phoneUpdate(groupPress(42, buttonData(t, f, "Approve")))
	if strings.Join(answers, ",") != "7a,7y" {
		t.Fatalf("answers %v", answers)
	}
	h.pumpUntil("answered", func() bool { return f.Last("answerCallbackQuery") != nil })

	h.u.infraStatus([]infra.TabStatus{{Org: "Acme", Name: "Acme", State: "approval", Last: "Disk is fine.", Panes: []infra.PaneStatus{web},
		Pending: []infra.Approval{{ID: 8, Cmd: "--- a\n+++ b\n" + strings.Repeat("x", 4000), Title: "Edit /etc/hosts", Fixed: true}}}})
	top = h.topicOf(rmInfraKey("Acme"))
	if top.Ask != "infra:8" || !strings.Contains(fmt.Sprint(f.Last("editMessageText")), "→ answered") {
		t.Fatalf("ask %q, edit %v", top.Ask, f.Last("editMessageText"))
	}
	if last := posted(f)[len(posted(f))-1]; !strings.Contains(last, "hosts**\n\n```diff\n--- a\n+++ b\nxxx") || !strings.Contains(last, "x…\n```") || strings.Contains(fmt.Sprint(f.Last("sendRichMessage")["reply_markup"]), "Always") {
		t.Fatalf("diff approval %s %v", last, f.Last("sendRichMessage")["reply_markup"])
	}
	h.u.infraStatus([]infra.TabStatus{{Org: "Acme", Name: "Acme", State: "working", Last: "Disk is fine."}})
	if top = h.topicOf(rmInfraKey("Acme")); top.Name != "🟢 Infra" {
		t.Fatalf("working: %+v", top)
	}
	h.u.infraStatus([]infra.TabStatus{{Org: "Acme", Name: "Acme", State: "your turn", Last: "Restarted."}})
	if top = h.topicOf(rmInfraKey("Acme")); top.Name != "⏸ Infra" || top.Ask != "" || top.Said != "Restarted." {
		t.Fatalf("after %+v", top)
	}
	n := len(posted(f))
	h.u.infraStatus([]infra.TabStatus{{Org: "Acme", Name: "Acme", State: "your turn", Last: "Restarted."}})
	h.u.infraStatus(nil) // closed: the header says so
	if h.topicOf(rmInfraKey("Acme")); len(posted(f)) != n || !strings.Contains(h.u.rm.topics[rmInfraKey("Acme")].Head, "No infra session open") {
		t.Fatalf("posted again, or head %q", h.u.rm.topics[rmInfraKey("Acme")].Head)
	}

	msg := func(text string) string {
		k := len(f.Calls("sendMessage"))
		h.u.phoneUpdate(groupMsg(42, top.Thread, text))
		h.pumpUntil(text, func() bool { return len(f.Calls("sendMessage")) > k })
		return said(f)
	}
	h.u.phoneUpdate(groupMsg(42, top.Thread, "restart nginx"))
	h.pumpUntil("seen", func() bool { return f.Last("setMessageReaction") != nil })
	if strings.Join(says, ",") != "Acme: restart nginx" {
		t.Fatalf("says %v", says)
	}
	sayOK = false
	if got := msg("again"); !strings.Contains(got, "no infra for Acme") {
		t.Fatalf("no infra: %s", got)
	}
	if got := msg(""); !strings.Contains(got, "Only text") {
		t.Fatalf("empty: %s", got)
	}
	if got := msg("/help"); !strings.Contains(got, "infra Claude") {
		t.Fatalf("help: %s", got)
	}
	if got := msg("/screen"); !strings.Contains(got, "/screen is off") {
		t.Fatalf("screen off: %s", got)
	}
	settingsFile(t, "[telegram]\ntoken = \"T0K\"\nchat_id = 42\nscreen = true\n[telegram.orgs.Acme]\nmirror = \"full\"\nchat_id = -100555\n")
	if got := msg("/screen"); !strings.Contains(got, "No terminal open in Acme's infra.") {
		t.Fatalf("no terminal: %s", got)
	}
	h.u.infraStat = []infra.TabStatus{{Org: "Globex", Panes: []infra.PaneStatus{web}}, {Org: "Acme", Panes: []infra.PaneStatus{db, web}}}
	screen := func(text string) string {
		k := len(posted(f))
		h.u.phoneUpdate(groupMsg(42, top.Thread, text))
		h.pumpUntil(text, func() bool { return len(posted(f)) > k })
		return posted(f)[k]
	}
	if got := screen("/screen"); !strings.Contains(got, "`web1` · running make deploy") || !strings.Contains(got, "$ make deploy\nok\n```") {
		t.Fatalf("screen: %s", got)
	}
	if got := screen("/screen DB1"); !strings.Contains(got, "`db1`") {
		t.Fatalf("screen db1: %s", got)
	}
	if got := msg("/screen web9"); !strings.Contains(got, "called web9") {
		t.Fatalf("screen none: %s", got)
	}
}
