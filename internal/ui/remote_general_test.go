package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/tgbot"
	"github.com/lfdominguez/desk/internal/tgbot/tgfake"
)

// dm is the user's message text in the bot's private chat.
func dm(text string) tgbot.Update {
	return tgbot.Update{Message: &tgbot.Message{ID: 5, Text: text, Chat: tgbot.Chat{ID: 42, Type: "private"}, From: tgbot.User{ID: 42}}}
}

// ask sends text in the General chat and waits for desk's next rich
// message, which it returns.
func (h *harness) ask(f *tgfake.Server, text string) string {
	h.t.Helper()
	n := len(posted(f))
	h.u.phoneUpdate(dm(text))
	h.pumpUntil(text, func() bool { return len(posted(f)) > n })
	return posted(f)[n]
}

// TestGeneralCommands: the General chat's commands read the board, the
// archive, tickets, alerts and CI, by organization or all of them.
func TestGeneralCommands(t *testing.T) {
	h := newHarness(t)
	f := remoteOn(t, h, "full")
	work := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Fix login", Org: "Acme", Status: kanban.StatusWorking})
	docs := h.u.b.Add(h.u.b.Column("To do"), kanban.Card{Title: "Write docs", Org: "Globex"})
	h.u.b.Add(h.u.b.Column("Done"), kanban.Card{Title: "Old thing", Org: "Acme"})
	h.u.b.Archive = append(h.u.b.Archive, kanban.Card{ID: "z9", Title: "Login page, archived", Org: "Acme"})
	h.u.save()

	got := h.ask(f, "/board")
	if !strings.Contains(got, "### In progress (1)") || !strings.Contains(got, "`"+work.Key()+"` Fix login · _Acme_") ||
		!strings.Contains(got, "### Done (1)") || strings.Contains(got, "Old thing") {
		t.Fatalf("board: %s", got)
	}
	if got := h.ask(f, "/board@desk_test_bot acme"); strings.Contains(got, "Write docs") || strings.Contains(got, "_Acme_") || !strings.Contains(got, "**Acme**") {
		t.Fatalf("Acme's board: %s", got)
	}
	if got := h.ask(f, "/board Nope"); !strings.Contains(got, "no organization called Nope") {
		t.Fatalf("no org: %s", got)
	}

	h.u.orgList = []string{"Acme", "Globex"}
	replace(t, &planLimits, func(_ context.Context, _, acct string) ([]claude.Limit, error) {
		return []claude.Limit{{Name: "5h", Percent: 40, Resets: time.Now().Add(2 * time.Hour)}}, nil
	})
	if got := h.ask(f, "/usage"); !strings.Contains(got, "### Acme · Acme") || !strings.Contains(got, "### Globex") || !strings.Contains(got, `- 5h · 40\%`) {
		t.Fatalf("usage: %s", got)
	}
	replace(t, &planLimits, func(context.Context, string, string) ([]claude.Limit, error) { return nil, errors.New("token expired") })
	if got := h.ask(f, "/usage"); !strings.Contains(got, "token expired") {
		t.Fatalf("usage error: %s", got)
	}
	h.u.orgList = nil
	if got := h.ask(f, "/usage"); !strings.Contains(got, "No organizations") {
		t.Fatalf("no orgs: %s", got)
	}

	if got := h.ask(f, "/inbox"); got != "Nothing waits on you\\." && got != "Nothing waits on you." {
		t.Fatalf("empty inbox: %s", got)
	}
	h.u.b.Card(docs.ID).Status, h.u.b.Card(docs.ID).Note = kanban.StatusBlocked, "needs a key"
	h.u.asks[work.ID] = &askState{r: &claude.Request{ID: "r1", Tool: "Bash", Line: "make `test`"}}
	got = h.ask(f, "/inbox")
	if !strings.Contains(got, "Waiting on you (2)") || !strings.Contains(got, "wants to run `make 'test'`") || !strings.Contains(got, "needs a key") {
		t.Fatalf("inbox: %s", got)
	}
	delete(h.u.asks, work.ID)

	if got := h.ask(f, "/find login"); !strings.Contains(got, "Fix login") || !strings.Contains(got, "archived") || strings.Contains(got, "docs") {
		t.Fatalf("find: %s", got)
	}
	if got := h.ask(f, "/find"); !strings.Contains(got, "Say what to look for") {
		t.Fatalf("find nothing: %s", got)
	}
	if got := h.ask(f, "/find zzzz"); !strings.Contains(got, "No card matches") {
		t.Fatalf("find none: %s", got)
	}

	if got := h.ask(f, "/digest week"); !strings.Contains(got, "Digest since") {
		t.Fatalf("digest: %s", got)
	}
	if got := h.ask(f, "/digest someday"); !strings.Contains(got, "Say since when") {
		t.Fatalf("bad digest: %s", got)
	}

	if got := h.ask(f, "/tickets"); !strings.Contains(got, "No tickets") {
		t.Fatalf("no tickets: %s", got)
	}
	h.u.tickets["Acme"] = []links.Ticket{{Key: "ABC-1", Title: "Login breaks", Status: "To Do", URL: "https://globex.atlassian.net/browse/ABC-1"}}
	h.u.b.Card(work.ID).Links = []kanban.Link{{URL: "https://globex.atlassian.net/browse/ABC-1", Ref: "ABC-1", Kind: links.KindJira}}
	if got := h.ask(f, "/tickets Acme"); !strings.Contains(got, "[ABC\\-1](https://globex.atlassian.net/browse/ABC-1) Login breaks") || !strings.Contains(got, "`"+work.Key()+"`") {
		t.Fatalf("tickets: %s", got)
	}

	if got := h.ask(f, "/find ABC-1"); !strings.Contains(got, "Fix login") {
		t.Fatalf("find by ticket: %s", got)
	}
	if got := h.ask(f, "/alerts"); got != "No alerts\\." && got != "No alerts." {
		t.Fatalf("no alerts: %s", got)
	}
	h.u.alerts["Acme"] = []links.Alert{{Host: "web1", Service: "HTTP", State: "critical", Output: "timeout\nmore"}, {Host: "db1", State: "warning", Acked: true}}
	got = h.ask(f, "/alerts")
	if !strings.Contains(got, "🔴 web1 · HTTP — timeout") || !strings.Contains(got, "- 🟡 _db1 — _") {
		t.Fatalf("alerts: %s", got)
	}

	if got := h.ask(f, "/ci"); !strings.Contains(got, "No CI") {
		t.Fatalf("no ci: %s", got)
	}
	pr := kanban.Link{URL: "https://github.com/globex-org/web/pull/3", Ref: "web#3"}
	h.u.ci["Acme"] = []ciItem{{key: work.Key(), title: "Fix login", link: pr, runs: []links.Run{{Status: links.CIFailed}}},
		{key: "DESK-99", link: pr, runs: []links.Run{{Status: links.CISuccess}}}}
	if got := h.ask(f, "/ci"); !strings.Contains(got, "❌ `"+work.Key()+"` [web\\#3](https://github.com/globex-org/web/pull/3) Fix login") || strings.Contains(got, "DESK-99") {
		t.Fatalf("ci: %s", got)
	}

	if got := h.ask(f, "/help"); !strings.Contains(got, "/board") || !strings.Contains(got, "desk Claude") {
		t.Fatalf("help: %s", got)
	}
	if got := h.ask(f, ""); !strings.Contains(got, "Only text") {
		t.Fatalf("empty: %s", got)
	}
}

// TestGeneralCards: /card shows one with a Start button that starts it,
// and a reply to it reaches its Claude; /new reads the request, asks for
// the organization when it names none, and makes the card in To do.
func TestGeneralCards(t *testing.T) {
	h := newHarness(t)
	f := remoteOn(t, h, "")
	fake := &fakeClaude{}
	replace(t, &startClaude, func(o claude.Options, on func(claude.Event)) (session, error) { fake.opts, fake.on = o, on; return fake, nil })
	c := h.u.b.Add(h.u.b.Column("To do"), kanban.Card{Title: "Fix login", Org: "Acme", Notes: "the form"})
	h.u.save()

	if got := h.ask(f, "/card desk-404"); !strings.Contains(got, "No card") {
		t.Fatalf("no card: %s", got)
	}
	got := h.ask(f, "/card "+strings.ToLower(c.Key()))
	if !strings.Contains(got, "Fix login") || !strings.Contains(got, "the form") || !strings.Contains(got, "Reply to this message") {
		t.Fatalf("card: %s", got)
	}
	data := buttonData(t, f, "Start it")
	h.u.phoneUpdate(tgbot.Update{Callback: &tgbot.Callback{ID: "cb", Data: data, From: tgbot.User{ID: 42}, Message: &tgbot.Message{ID: 77, Chat: tgbot.Chat{ID: 42, Type: "private"}}}})
	if len(fake.prompts) != 1 {
		t.Fatalf("not started: %v", fake.prompts)
	}
	h.pumpUntil("buttons gone", func() bool { return f.Last("editMessageReplyMarkup") != nil && f.Last("answerCallbackQuery") != nil })
	if a := f.Last("answerCallbackQuery"); a["text"] != "Started "+h.u.b.Card(c.ID).Key() {
		t.Fatalf("answer %v", a)
	}
	h.pumpUntil("its message kept", func() bool { return len(h.u.phone.cards) > 0 })
	var msg int64
	for id := range h.u.phone.cards {
		msg = id
	}
	h.u.b.Card(c.ID).Status = kanban.StatusWorking
	h.u.procs[c.ID] = fake
	h.ask(f, "/card "+c.Key())
	buttonData(t, f, "Stop its turn")

	reply := dm("and the button")
	reply.Message.Reply = &tgbot.Message{ID: msg}
	h.u.phoneUpdate(reply)
	if fake.prompts[len(fake.prompts)-1] != "and the button" {
		t.Fatalf("reply: %v", fake.prompts)
	}

	if got := h.ask(f, "/new"); !strings.Contains(got, "Say what to do") {
		t.Fatalf("empty /new: %s", got)
	}
	got = h.ask(f, "/new add a logout button #auth !high")
	if !strings.Contains(got, "Which organization") {
		t.Fatalf("no org: %s", got)
	}
	data = buttonData(t, f, "Globex")
	h.u.phoneUpdate(tgbot.Update{Callback: &tgbot.Callback{ID: "cb2", Data: data, From: tgbot.User{ID: 42}, Message: &tgbot.Message{ID: 78, Chat: tgbot.Chat{ID: 42, Type: "private"}}}})
	h.pumpUntil("made", func() bool { return strings.Contains(posted(f)[len(posted(f))-1], "Made") })
	todo := h.u.b.Columns[h.u.b.Column("To do")].Cards
	n := todo[len(todo)-1]
	if n.Org != "Globex" || n.Title != "add a logout button" || n.Priority != 3 || len(n.Labels) != 1 || n.Labels[0] != "auth" {
		t.Fatalf("made %+v", n)
	}
	var asked claude.Ask
	replace(t, &optimize, func(_ context.Context, a claude.Ask, _ func(claude.Progress)) (claude.Draft, error) {
		asked = a
		return claude.Draft{Title: "Add a logout button to the header", Prompt: "Add a **logout** button.", Labels: []string{"ui", "auth"}, Priority: "low", Due: "2026-12-01"}, nil
	})
	data = buttonData(t, f, "Improve it")
	h.u.phoneUpdate(tgbot.Update{Callback: &tgbot.Callback{ID: "cb3", Data: data, From: tgbot.User{ID: 42}, Message: &tgbot.Message{ID: 79, Chat: tgbot.Chat{ID: 42, Type: "private"}}}})
	h.pumpUntil("improved", func() bool { return strings.Contains(posted(f)[len(posted(f))-1], "Add a logout button to the header") })
	if m := h.u.b.Card(n.ID); asked.Org != "Globex" || asked.Request != "add a logout button #auth !high" ||
		m.Title != "Add a logout button to the header" || m.Notes != "Add a **logout** button." || m.Priority != 3 || m.Due != "2026-12-01" || strings.Join(m.Labels, ",") != "auth,ui" {
		t.Fatalf("improved %+v (asked %+v)", m, asked)
	}
	replace(t, &optimize, func(context.Context, claude.Ask, func(claude.Progress)) (claude.Draft, error) { return claude.Draft{}, errors.New("haiku down") })
	h.u.gnImprove(n.ID)
	h.pumpUntil("not improved", func() bool { return strings.Contains(posted(f)[len(posted(f))-1], "Improve didn") })
	replace(t, &optimize, func(context.Context, claude.Ask, func(claude.Progress)) (claude.Draft, error) {
		return claude.Draft{Title: "late"}, nil
	})
	h.u.gnImprove(n.ID)
	h.u.b.Card(n.ID).Status = kanban.StatusWorking
	h.pumpUntil("started meanwhile", func() bool { return strings.Contains(posted(f)[len(posted(f))-1], "left as it was") })
	h.u.gnImprove(n.ID)
	h.u.b.Delete(n.ID) // gone meanwhile: nothing said
	if got := h.u.gnImprove(n.ID); got != "That card is gone" {
		t.Fatalf("gone: %s", got)
	}

	got = h.ask(f, "/new @Initech whatever")
	if !strings.Contains(got, "Which organization") { // Initech isn't one of desk's: no org read
		t.Fatalf("unknown org: %s", got)
	}
	if got := h.ask(f, "/new @Acme  "); !strings.Contains(got, "a card needs a title") {
		t.Fatalf("no title: %s", got)
	}
}

// TestGeneralSettings: /always and /mirror change settings.toml and the
// running bot.
func TestGeneralSettings(t *testing.T) {
	h := newHarness(t)
	f := remoteOn(t, h, "")
	if got := h.ask(f, "/always"); !strings.Contains(got, "It's off") {
		t.Fatalf("always? %s", got)
	}
	if got := h.ask(f, "/always on"); !strings.Contains(got, "even while") || !h.u.phone.always || !orgs.PhoneSettings().Always {
		t.Fatalf("always on: %s", got)
	}
	if got := h.ask(f, "/always off"); !strings.Contains(got, "only while") || h.u.phone.always {
		t.Fatalf("always off: %s", got)
	}
	if got := h.ask(f, "/mirror Acme loud"); !strings.Contains(got, "Say which organization") {
		t.Fatalf("bad mirror: %s", got)
	}
	if got := h.ask(f, "/mirror acme summary"); !strings.Contains(got, "talk in its group: summary") || orgs.TelegramSettings().Orgs["Acme"].Mirror != orgs.MirrorSummary {
		t.Fatalf("mirror: %s %+v", got, orgs.TelegramSettings().Orgs)
	}
	if got := h.ask(f, "/mirror Globex full"); !strings.Contains(got, "no group yet") {
		t.Fatalf("no group: %s", got)
	}
	if got := h.ask(f, "/mirror Acme off"); !strings.Contains(got, "stay off Telegram") || orgs.TelegramSettings().Orgs["Acme"].Mirror != orgs.MirrorOff {
		t.Fatalf("off: %s", got)
	}
}

// TestGeneralEdges: lists cut at gnList; one organization needs no
// asking; /new's links; a failed save, a failed send, buttons on a card
// that's gone or working.
func TestGeneralEdges(t *testing.T) {
	h := newHarness(t)
	f := remoteOn(t, h, "")
	fake := &fakeClaude{}
	replace(t, &startClaude, func(o claude.Options, on func(claude.Event)) (session, error) { return fake, nil })
	replace(t, &gnList, 1)
	pr := kanban.Link{URL: "https://github.com/globex-org/web/pull/3", Ref: "web#3", Kind: links.KindGitHubPR}
	a := h.u.b.Add(h.u.b.Column("To do"), kanban.Card{Title: "Login one", Org: "Acme", Links: []kanban.Link{pr}})
	h.u.b.Add(h.u.b.Column("To do"), kanban.Card{Title: "Login two", Org: "Acme"})
	h.u.save()
	if got := h.ask(f, "/board"); !strings.Contains(got, "… 1 more") {
		t.Fatalf("board cut: %s", got)
	}
	if got := h.ask(f, "/find login"); !strings.Contains(got, "… 1 more") {
		t.Fatalf("find cut: %s", got)
	}
	h.u.tickets["Acme"] = []links.Ticket{{Key: "ABC-1"}, {Key: "ABC-2"}}
	if got := h.ask(f, "/tickets"); !strings.Contains(got, "… 1 more") {
		t.Fatalf("tickets cut: %s", got)
	}
	if got := h.ask(f, "/tickets Globex"); !strings.Contains(got, "No tickets") {
		t.Fatalf("Globex's tickets: %s", got)
	}
	h.u.alerts["Acme"] = []links.Alert{{Host: "a"}, {Host: "b"}}
	if got := h.ask(f, "/alerts"); !strings.Contains(got, "… 1 more") {
		t.Fatalf("alerts cut: %s", got)
	}
	if got := h.ask(f, "/alerts Globex"); !strings.Contains(got, "No alerts") {
		t.Fatalf("Globex's alerts: %s", got)
	}

	press := func(label string) string {
		data := buttonData(t, f, label)
		n := len(f.Calls("answerCallbackQuery"))
		h.u.phoneUpdate(tgbot.Update{Callback: &tgbot.Callback{ID: "cb", Data: data, From: tgbot.User{ID: 42}, Message: &tgbot.Message{ID: 70, Chat: tgbot.Chat{ID: 42, Type: "private"}}}})
		h.pumpUntil("answered", func() bool { return len(f.Calls("answerCallbackQuery")) > n })
		return fmtAny(f.Last("answerCallbackQuery")["text"])
	}
	h.ask(f, "/card "+a.Key())
	h.u.b.Delete(a.ID)
	if got := press("Start it"); got != "That card is gone" {
		t.Fatalf("gone: %s", got)
	}
	w := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Busy", Org: "Acme", Status: kanban.StatusWorking})
	h.u.procs[w.ID] = fake
	h.ask(f, "/card "+w.Key())
	if got := press("Stop its turn"); got != "Stopping" {
		t.Fatalf("stop: %s", got)
	}

	h.u.orgList = []string{"Acme"}
	got := h.ask(f, "/new review https://github.com/globex-org/web/pull/3")
	if !strings.Contains(got, "Made") {
		t.Fatalf("one org: %s", got)
	}
	todo := h.u.b.Columns[h.u.b.Column("To do")].Cards
	if n := todo[len(todo)-1]; len(n.Links) != 1 || n.Links[0].URL != pr.URL {
		t.Fatalf("links %+v", n.Links)
	}
	if got := press("Start it"); !strings.HasPrefix(got, "Started ") {
		t.Fatalf("start: %s", got)
	}

	dir := filepath.Dir(orgs.SettingsFile())
	os.Chmod(dir, 0o500)
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if got := h.ask(f, "/always on"); !strings.Contains(got, "Not saved") {
		t.Fatalf("always unsaved: %s", got)
	}
	if got := h.ask(f, "/mirror Acme full"); !strings.Contains(got, "Not saved") {
		t.Fatalf("mirror unsaved: %s", got)
	}
	h.u.phone.chat, h.u.phone.pairUntil = 0, time.Now().Add(time.Minute)
	h.u.phoneUpdate(dm("/start")) // paired while desk runs, though not kept
	if h.u.phone.chat != 42 {
		t.Fatalf("pairing %d", h.u.phone.chat)
	}
	os.Chmod(dir, 0o700)

	replace(t, &startClaude, func(o claude.Options, on func(claude.Event)) (session, error) { return &failSend{}, nil })
	if got := h.ask(f, "hello"); !strings.Contains(got, "Not sent") {
		t.Fatalf("failed send: %s", got)
	}
	h.u.gnForget()

	f.Fail("sendRichMessage", "Forbidden: blocked")
	h.u.phoneUpdate(dm("/help"))
	h.pumpUntil("toast", func() bool { return strings.Contains(h.u.toast, "Telegram: ") })
}

func fmtAny(v any) string { return fmt.Sprint(v) }

// TestGeneralClaude: other text goes to the desk Claude, started once with
// the desk tools and resumed after it ends; its turn's text comes back at
// the turn's end, asks are refused, /stop, /forget starts afresh.
func TestGeneralClaude(t *testing.T) {
	h := newHarness(t)
	f := remoteOn(t, h, "")
	var fakes []*fakeClaude
	fail := false
	replace(t, &startClaude, func(o claude.Options, on func(claude.Event)) (session, error) {
		if fail {
			return nil, errNoCard
		}
		fk := &fakeClaude{opts: o, on: on}
		fakes = append(fakes, fk)
		return fk, nil
	})
	h.u.cardSock = "/run/x.sock"
	h.u.phoneUpdate(dm("what's blocked?"))
	if len(fakes) != 1 || fakes[0].prompts[0] != "what's blocked?" || fakes[0].opts.New == "" || fakes[0].opts.Dir != h.u.root ||
		!strings.Contains(strings.Join(fakes[0].opts.Args, " "), `"desk"]`) {
		t.Fatalf("started %+v", fakes)
	}
	h.pumpUntil("seen", func() bool { return f.Last("setMessageReaction") != nil })
	h.u.phoneUpdate(dm("and in Acme?"))
	if len(fakes) != 1 || len(fakes[0].prompts) != 2 {
		t.Fatal("a second Claude")
	}
	on := func(e claude.Event) { fakes[len(fakes)-1].on(e); h.frame() }
	on(claude.Event{Kind: claude.KindText, Text: "Nothing is blocked."})
	on(claude.Event{Kind: claude.KindText, Text: "sub", Parent: "t1"})
	on(claude.Event{Kind: claude.KindAsk, Ask: &claude.Request{ID: "a1", Tool: "Bash"}})
	if fakes[0].replies[0] != "a1 deny The desk Claude only uses its board tools." {
		t.Fatalf("ask: %v", fakes[0].replies)
	}
	n := len(posted(f))
	on(claude.Event{Kind: claude.KindResult, Error: true, Text: "max turns"})
	h.pumpUntil("answer", func() bool { return len(posted(f)) > n })
	if got := posted(f)[n]; got != "Nothing is blocked.\n\nError\\: max turns" {
		t.Fatalf("answer %q", got)
	}
	on(claude.Event{Kind: claude.KindResult}) // nothing said: nothing sent

	sid := fakes[0].opts.New
	on(claude.Event{Kind: claude.KindExit, Text: "killed"})
	h.pumpUntil("stopped", func() bool { return strings.Contains(posted(f)[len(posted(f))-1], "stopped: killed") })
	h.u.phoneUpdate(dm("again"))
	if len(fakes) != 2 || fakes[1].opts.Resume != sid {
		t.Fatalf("resumed %+v", fakes[1].opts)
	}
	h.u.gnQuit() // desk quits; the next run resumes the same session
	h.u.phone.gnSid = ""
	h.u.phoneUpdate(dm("after a restart"))
	if len(fakes) != 3 || fakes[2].opts.Resume != sid {
		t.Fatalf("after a restart %+v", fakes[2].opts)
	}
	fakes[0].on(claude.Event{Kind: claude.KindText, Text: "old"}) // a replaced one's
	h.frame()
	if len(h.u.phone.gnSaid) != 0 {
		t.Fatal("an old Claude's text kept")
	}
	if got := h.ask(f, "/stop"); !strings.Contains(got, "Stopped") {
		t.Fatalf("stop: %s", got)
	}
	if got := h.ask(f, "/forget"); !strings.Contains(got, "afresh") || h.u.phone.gnProc != nil || h.u.phone.gnSid != "" || func() bool { _, err := os.Stat(h.u.gnSidFile()); return err == nil }() {
		t.Fatalf("forget: %s", got)
	}
	fail = true
	if got := h.ask(f, "hello"); !strings.Contains(got, "didn't start") {
		t.Fatalf("failed start: %s", got)
	}
}

// TestDeskTool: the desk Claude's tools see every organization's cards,
// read one, the digest, make a card and message one.
func TestDeskTool(t *testing.T) {
	h := newHarness(t)
	fake := &fakeClaude{}
	replace(t, &startClaude, func(o claude.Options, on func(claude.Event)) (session, error) { return fake, nil })
	a := h.u.b.Add(h.u.b.Column("To do"), kanban.Card{Title: "Fix login", Org: "Acme"})
	h.u.b.Add(h.u.b.Column("To do"), kanban.Card{Title: "Write docs", Org: "Globex"})
	call := func(name, args string) (string, bool) { return h.tool("desk:", name, args) }
	if s, ok := call("cards", `{}`); !ok || !strings.Contains(s, a.Key()+" Acme [To do] Fix login") || !strings.Contains(s, "Globex [To do] Write docs") {
		t.Fatalf("cards: %s", s)
	}
	if s, _ := call("cards", `{"org":"globex"}`); strings.Contains(s, "Fix login") || !strings.Contains(s, "Write docs") || strings.Contains(s, "Globex [") {
		t.Fatalf("Globex's: %s", s)
	}
	if s, ok := call("cards", `{"org":"Nope"}`); ok || !strings.Contains(s, "no organization") {
		t.Fatalf("no org: %s", s)
	}
	if s, ok := call("card_read", `{"key":"`+strings.ToLower(a.Key())+`"}`); !ok || !strings.Contains(s, "Fix login") {
		t.Fatalf("read: %s", s)
	}
	if s, ok := call("card_read", `{"key":"DESK-404"}`); ok || !strings.Contains(s, "no card DESK-404") {
		t.Fatalf("read none: %s", s)
	}
	if s, ok := call("digest", `{"since":"week"}`); !ok || !strings.Contains(s, "Digest since") {
		t.Fatalf("digest: %s", s)
	}
	if s, ok := call("digest", `{"since":"ever"}`); ok || !strings.Contains(s, "since:") {
		t.Fatalf("bad digest: %s", s)
	}
	if s, ok := call("card_create", `{"org":"acme","title":"Add logout","start":true}`); !ok || !strings.Contains(s, "and started it: Add logout") || len(fake.prompts) != 1 {
		t.Fatalf("create: %s %v", s, fake.prompts)
	}
	if s, ok := call("card_create", `{"org":"Initech","title":"x"}`); ok || !strings.Contains(s, "no organization") {
		t.Fatalf("create in none: %s", s)
	}
	if s, ok := call("card_message", `{"key":"`+a.Key()+`"}`); ok || !strings.Contains(s, "say what") {
		t.Fatalf("message nothing: %s", s)
	}
	if s, ok := call("card_message", `{"key":"`+a.Key()+`","text":"use the new API"}`); !ok || s != "Sent to "+a.Key()+"." || !strings.HasSuffix(fake.prompts[len(fake.prompts)-1], "use the new API") {
		t.Fatalf("message: %s %v", s, fake.prompts)
	}
}
