package ui

import (
	"context"
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
)

var (
	apTk1 = links.Ticket{Project: "ABC", Key: "ABC-1", Title: "Fix login", Status: "To Do", URL: "https://globex.atlassian.net/browse/ABC-1"}
	apTk2 = links.Ticket{Project: "ABC", Key: "ABC-2", Title: "Ship it", Status: "To Do", URL: "https://globex.atlassian.net/browse/ABC-2"}
)

// apStart makes an autopilot over both tickets, ABC-1 already a card: the
// autopilot's ID and its cards' IDs, ABC-1's first.
func apStart(t *testing.T, h *harness, p kanban.Pilot) (string, []string) {
	t.Helper()
	h.u.b.Add(0, kanban.Card{Title: "old", Org: "Globex", Links: []kanban.Link{{URL: apTk1.URL, Kind: links.KindJira, Ref: "ABC-1"}}})
	key := h.u.startPilot("Globex", "Autopilot: ABC-1, ABC-2", []links.Ticket{apTk1, apTk2}, p)
	pc := h.u.cardOfKey(key)
	if pc == nil || pc.Pilot == nil {
		t.Fatal("no autopilot card")
	}
	kids := h.u.pilotKids(pc.ID)
	if len(kids) != 2 {
		t.Fatalf("%d cards", len(kids))
	}
	return pc.ID, kids
}

// claudeOf is the fake Claude card id's session runs.
func (h *harness) claudeOf(id string) *fakeClaude {
	f, _ := h.u.procs[id].(*fakeClaude)
	return f
}

// TestAutopilotStarts: a card per ticket (its own when it has one) under
// the autopilot, which starts at once, told its cards, level and grants,
// with no editing tools; its cards leave the board for it.
func TestAutopilotStarts(t *testing.T) {
	h := newHarness(t)
	pid, kids := apStart(t, h, kanban.Pilot{Level: kanban.PilotAuto, Grants: "the staging server is free", Max: 1})
	if h.u.b.Card(kids[0]).Title != "old" || h.u.b.Card(kids[1]).Links[0].Ref != "ABC-2" {
		t.Error("the tickets' cards")
	}
	f := h.claudeOf(pid)
	if f == nil {
		t.Fatal("the autopilot didn't start")
	}
	if s := f.opts.System; !strings.Contains(s, "an autopilot") || !strings.Contains(s, "the staging server is free") || !strings.Contains(s, "at most 1 working") || !strings.Contains(s, "unless the user's grants") {
		t.Errorf("system prompt:\n%s", s)
	}
	if !slices.Contains(f.opts.Args, "--disallowedTools=Edit,Write,NotebookEdit") || !strings.Contains(f.prompts[0], "ABC-2 Ship it") {
		t.Errorf("args %v prompt %q", f.opts.Args, f.prompts[0])
	}
	for col := range h.u.b.Columns {
		for _, j := range h.u.visible(col) {
			if c := h.u.b.Columns[col].Cards[j]; c.Parent != "" {
				t.Errorf("%s on the board", c.Key())
			}
		}
	}
	if n := h.u.count(""); n != 1 {
		t.Errorf("board counts %d", n)
	}
	sup := pilotPrompt(&kanban.Card{Num: 9, Pilot: &kanban.Pilot{Level: kanban.PilotSupervised}})
	if !strings.Contains(sup, "Allow only what reads") || !strings.Contains(sup, "nothing beyond the level") || !strings.Contains(sup, "at most 3") {
		t.Errorf("supervised:\n%s", sup)
	}
}

// TestAutopilotRunsCards: its tools start cards (no more than its Max at
// once), answer their requests and questions, add cards; its cards' asks
// and turn ends reach it and never the user.
func TestAutopilotRunsCards(t *testing.T) {
	h := newHarness(t)
	var notes []string
	desktopNotify = func(title, _ string) { notes = append(notes, title) }
	pid, kids := apStart(t, h, kanban.Pilot{Level: kanban.PilotAuto, Max: 1})
	k1, k2 := h.u.b.Card(kids[0]).Key(), h.u.b.Card(kids[1]).Key()
	if text, ok := h.tool(pid, "pilot_cards", `{}`); !ok || !strings.Contains(text, "Up to 1") || !strings.Contains(text, k2+" ABC-2 Ship it [not started]") {
		t.Fatalf("cards: %v %s", ok, text)
	}
	if text, ok := h.tool(pid, "pilot_tell", `{"card":"`+k1+`","text":"Fix the login."}`); !ok || text != "Sent to "+k1+"." {
		t.Fatalf("tell: %v %s", ok, text)
	}
	kf := h.claudeOf(kids[0])
	if kf == nil || !strings.Contains(kf.prompts[0], "From the autopilot") || !strings.Contains(kf.opts.System, "runs this card for the user") {
		t.Fatal("the card didn't start under its autopilot")
	}
	for _, c := range []struct{ args, want string }{
		{`{"card":"` + k2 + `","text":"go"}`, "already work"},
		{`{"card":"DESK-999","text":"go"}`, "not one of your cards"},
		{`{"card":"` + k1 + `","text":" "}`, "say something"},
	} {
		if text, ok := h.tool(pid, "pilot_tell", c.args); ok || !strings.Contains(text, c.want) {
			t.Errorf("%s: %v %s", c.args, ok, text)
		}
	}
	pf := h.claudeOf(pid)
	told := func(want string) {
		t.Helper()
		h.frame()
		if p := pf.prompts[len(pf.prompts)-1]; !strings.Contains(p, want) {
			t.Fatalf("the autopilot got %q, want %q", p, want)
		}
	}
	// a permission request
	kf.on(claude.Parse([]byte(`{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"rm -rf build"}}}`))[0])
	told("From " + k1 + ": asks to run")
	if c := h.u.b.Card(kids[0]); c.Status != kanban.StatusWorking || !strings.HasPrefix(c.Note, "Asked the autopilot") {
		t.Errorf("asking: %q %q", c.Status, c.Note)
	}
	if text, ok := h.tool(pid, "pilot_cards", `{}`); !ok || !strings.Contains(text, "waits on your pilot_answer") {
		t.Errorf("cards while it asks: %s", text)
	}
	if text, ok := h.tool(pid, "pilot_tell", `{"card":"`+k1+`","text":"hold on"}`); !ok || !strings.Contains(text, "once its open request") {
		t.Errorf("tell while it asks: %s", text)
	}
	if _, ok := h.tool(pid, "pilot_answer", `{"card":"`+k1+`","allow":false,"message":"not granted"}`); !ok || kf.replies[0] != "r1 deny not granted" {
		t.Fatalf("answer: %v", kf.replies)
	}
	if text, ok := h.tool(pid, "pilot_answer", `{"card":"`+k1+`","allow":true}`); ok || !strings.Contains(text, "no open request") {
		t.Errorf("answered twice: %s", text)
	}
	// a request that stays inside, allowed
	kf.on(claude.Parse([]byte(`{"type":"control_request","request_id":"r3","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"make test"}}}`))[0])
	h.frame()
	if _, ok := h.tool(pid, "pilot_answer", `{"card":"`+k1+`","allow":true}`); !ok || !strings.HasPrefix(kf.replies[1], "r3 allow ") {
		t.Fatalf("allow: %v", kf.replies)
	}
	// a question
	kf.on(claude.Parse([]byte(`{"type":"control_request","request_id":"r2","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion",
		"input":{"questions":[{"question":"Which DB?","header":"DB","multiSelect":false,"options":[{"label":"Postgres"},{"label":"SQLite"}]}]}}}`))[0])
	told("Which DB?")
	if _, ok := h.tool(pid, "pilot_answer", `{"card":"`+k1+`","message":"Postgres"}`); !ok || kf.replies[2] != "r2 deny The autopilot answers: Postgres" {
		t.Fatalf("question: %v", kf.replies)
	}
	// the turn ends on the user's side: the autopilot hears, the user doesn't
	kf.on(claude.Event{Kind: claude.KindText, Text: "Fixed; tests pass."})
	kf.on(claude.Event{Kind: claude.KindResult})
	told(k1 + ": ended its turn: waits for you")
	told("Fixed; tests pass.")
	if len(notes) != 0 {
		t.Errorf("the user was notified: %v", notes)
	}
	if text, ok := h.tool(pid, "pilot_add", `{"title":"Fix the logout too","notes":"same bug"}`); !ok || !strings.Contains(text, "start it with pilot_tell") || len(h.u.pilotKids(pid)) != 3 {
		t.Errorf("add: %v %s", ok, text)
	}
	if text, ok := h.tool(pid, "pilot_add", `{"title":" "}`); ok || !strings.Contains(text, "needs a title") {
		t.Errorf("add untitled: %s", text)
	}
	if text, ok := h.tool(kids[0], "pilot_cards", `{}`); ok || !strings.Contains(text, "no autopilot") {
		t.Errorf("not an autopilot: %s", text)
	}
}

// TestAutopilotSkipsCarded: a ticket that has a card shows no checkbox,
// and one checked before its card was made drops out of the autopilot.
func TestAutopilotSkipsCarded(t *testing.T) {
	h := newHarness(t)
	h.u.tickets["Globex"] = []links.Ticket{apTk1, apTk2}
	h.u.tkSel[apTk1.URL], h.u.tkSel[apTk2.URL] = true, true
	h.u.view = "tk:Globex"
	h.u.b.Add(0, kanban.Card{Title: "Fix login", Org: "Globex", Links: []kanban.Link{{URL: apTk1.URL}}})
	sz := image.Pt(900, 600)
	view := func(gtx layout.Context) layout.Dimensions { return h.u.ticketsView(gtx) }
	h.draw(sz, view)
	if _, ok := h.seek("tkSel"+apTk1.URL, image.Rectangle{Max: sz}, func() { h.draw(sz, view) }); ok {
		t.Error("a carded ticket has a checkbox")
	}
	if _, ok := h.seek("tkSel"+apTk2.URL, image.Rectangle{Max: sz}, func() { h.draw(sz, view) }); !ok {
		t.Error("a free ticket has no checkbox")
	}
	if ts := h.u.pilotTickets("Globex"); len(ts) != 1 || ts[0].Key != "ABC-2" {
		t.Errorf("autopilot tickets %v", ts)
	}
}

// TestAutopilotView: tickets check into a bar whose Autopilot… opens the
// dialog; Start makes it with the level and number picked; the cards show
// on the autopilot's card and session, each opening its own.
func TestAutopilotView(t *testing.T) {
	h := newHarness(t)
	h.u.tickets["Globex"] = []links.Ticket{apTk1, apTk2}
	h.u.view = "tk:Globex"
	sz := image.Pt(900, 600)
	view := func(gtx layout.Context) layout.Dimensions { return h.u.ticketsView(gtx) }
	all := image.Rectangle{Max: sz}
	h.clickIn(sz, all, view, "tkSel"+apTk1.URL)
	h.clickIn(sz, all, view, "tkSel"+apTk2.URL)
	h.clickIn(sz, all, view, "tkSel"+apTk2.URL)
	if len(h.u.pilotTickets("Globex")) != 1 {
		t.Fatalf("checked %v", h.u.tkSel)
	}
	h.clickIn(sz, all, view, "tkClear")
	if len(h.u.tkSel) != 0 {
		t.Fatal("Clear")
	}
	h.u.openPilot("Globex") // nothing checked: no dialog
	if h.u.pilotDlg != nil {
		t.Fatal("a dialog for no tickets")
	}
	for _, tk := range []links.Ticket{apTk1, apTk2} {
		h.clickIn(sz, all, view, "tkSel"+tk.URL)
	}
	h.clickIn(sz, all, view, "tkPilot")
	d := h.u.pilotDlg
	if d == nil || d.title.Text() != "Autopilot: ABC-1, ABC-2" || len(d.tickets) != 2 {
		t.Fatal("no dialog")
	}
	h.clickButton("apLevel0")
	h.clickButton("apMax1")
	h.clickButton("apBudget2")
	if d.level != 0 || d.max != 2 || pilotBudgets[d.budget] != 10 {
		t.Fatalf("level %d max %d budget %d", d.level, d.max, d.budget)
	}
	h.clickButton("apCancel")
	if h.u.pilotDlg != nil {
		t.Fatal("Cancel")
	}
	h.u.openPilot("Globex")
	h.u.pilotDlg.title.SetText(" ")
	h.u.pilotDlg.grants.SetText("push to feature branches")
	h.clickButton("apStart")
	pc := h.u.b.Card(h.u.panel)
	if h.u.pilotDlg != nil || pc == nil || pc.Pilot == nil || pc.Title != "Autopilot" || pc.Pilot.Grants != "push to feature branches" || pc.Pilot.Level != kanban.PilotAuto || len(h.u.tkSel) != 0 {
		t.Fatalf("start: panel %q card %+v", h.u.panel, pc)
	}
	kids := h.u.pilotKids(pc.ID)
	h.u.b.Card(kids[0]).Status = kanban.StatusReview
	h.u.save()
	if done, n := h.u.pilotDone(pc.ID); done != 1 || n != 2 {
		t.Errorf("done %d of %d", done, n)
	}
	h.frame() // the card and its session draw the cards
	h.clickButton("pnKid" + kids[1])
	if h.u.panel != kids[1] {
		t.Fatalf("a card's row: panel %q", h.u.panel)
	}
	h.u.sayMsg(kids[1], kanban.ChatMsg{Who: "pilot", Text: "Start with the tests."}) // drawn under its autopilot's name
	h.u.sayMsg(kids[1], kanban.ChatMsg{Who: "pilot", Name: "Autopilot DESK-1", Text: "Go on."})
	h.frame()
	h.clickButton("pnPilot" + kids[1])
	if h.u.panel != pc.ID {
		t.Fatalf("its autopilot: panel %q", h.u.panel)
	}
	// esc closes it; enter in the name starts it (on a ticket with no card yet)
	tk3 := links.Ticket{Project: "ABC", Key: "ABC-3", Title: "Docs", Status: "To Do", URL: "https://globex.atlassian.net/browse/ABC-3"}
	h.u.tickets["Globex"] = append(h.u.tickets["Globex"], tk3)
	h.u.tkSel[tk3.URL] = true
	h.u.openPilot("Globex")
	h.frame()
	h.send(press(key.NameEscape, 0))
	if h.u.pilotDlg != nil {
		t.Fatal("esc")
	}
	h.u.openPilot("Globex")
	h.frame()
	h.u.pilotDlg.title.SetText("Second")
	h.send(press(key.NameReturn, 0))
	if h.u.pilotDlg != nil || h.u.b.Card(h.u.panel).Title != "Second" {
		t.Fatal("enter")
	}
}

// TestAutopilotEdges: what it hears while it can't take it waits; a
// blocked card's why reaches it; many tickets and cards fold; archiving it
// puts its cards back on the board.
func TestAutopilotEdges(t *testing.T) {
	h := newHarness(t)
	pid, kids := apStart(t, h, kanban.Pilot{Level: kanban.PilotAuto, Max: 3})
	h.u.pilotAsk(pid) // not a card under an autopilot: nothing
	h.u.pilotEnded(pid)
	h.u.asks[pid] = newAsk(&claude.Request{ID: "x", Line: "Bash: ls"})
	h.u.pilotTell(pid, "DESK-9", "news")
	if !strings.Contains(h.u.peerOut[pid], "From DESK-9: news") {
		t.Fatalf("held: %q", h.u.peerOut[pid])
	}
	delete(h.u.asks, pid)
	pf := h.claudeOf(pid)
	h.tool(pid, "pilot_tell", `{"card":"`+h.u.b.Card(kids[1]).Key()+`","text":"go"}`)
	kf := h.claudeOf(kids[1])
	kf.on(claude.Event{Kind: claude.KindText, Text: "No access.\nDESK-STATUS: blocked — the API key is missing"})
	kf.on(claude.Event{Kind: claude.KindResult})
	h.frame()
	if p := pf.prompts[len(pf.prompts)-1]; !strings.Contains(p, "ended its turn: Blocked (the API key is missing)") {
		t.Fatalf("blocked: %q", p)
	}
	if text, _ := h.tool(pid, "pilot_cards", `{}`); !strings.Contains(text, "last: No access.") {
		t.Errorf("cards: %s", text)
	}
	for i := range 7 {
		h.tool(pid, "pilot_add", `{"title":"more `+string(rune('a'+i))+`"}`)
	}
	h.u.openPanel(pid)
	h.frame() // nine cards: the card folds the ninth
	var ts []links.Ticket
	for i := range 5 {
		k := "ABC-" + string(rune('3'+i))
		ts = append(ts, links.Ticket{Key: k, Title: k, URL: "https://globex.atlassian.net/browse/" + k})
		h.u.tkSel[ts[i].URL] = true
	}
	h.u.tickets["Globex"] = ts
	h.u.openPilot("Globex")
	if got := h.u.pilotDlg.title.Text(); got != "Autopilot: ABC-3, ABC-4, ABC-5, ABC-6, +1" {
		t.Errorf("five tickets: %q", got)
	}
	h.u.pilotDlg = nil
	h.u.b.ArchiveCard(pid)
	h.u.save()
	if h.u.pilotOf(h.u.b.Card(kids[0])) != nil || h.u.count("") != 9 { // its nine cards
		t.Errorf("archived autopilot: board counts %d", h.u.count(""))
	}
}

// TestAutopilotGrants: a change outside goes to the user unless the
// autopilot quotes their grant; with no grants, straight to them, and that
// notifies. Its cards can't move tickets unasked, but their columns move
// them as any card's do.
func TestAutopilotGrants(t *testing.T) {
	h := newHarness(t)
	var notes []string
	desktopNotify = func(title, _ string) { notes = append(notes, title) }
	pushed := 0
	pushTicket = func(context.Context, kanban.Link, string, orgs.Config) (string, error) { pushed++; return "", nil }
	pid, kids := apStart(t, h, kanban.Pilot{Level: kanban.PilotAuto, Grants: "Push to feature branches freely", Max: 3})
	k1 := h.u.b.Card(kids[0]).Key()
	h.tool(pid, "pilot_tell", `{"card":"`+k1+`","text":"go"}`)
	kf, pf := h.claudeOf(kids[0]), h.claudeOf(pid)
	if slices.ContainsFunc(kf.opts.Args, func(a string) bool { return strings.Contains(a, "ticket_status") }) {
		t.Error("a card under an autopilot moves tickets unasked")
	}
	ask := func(rid, cmd string) {
		t.Helper()
		kf.on(claude.Parse([]byte(`{"type":"control_request","request_id":"` + rid + `","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"` + cmd + `"}}}`))[0])
		h.frame()
	}
	ask("r1", "git push origin fix")
	if p := pf.prompts[len(pf.prompts)-1]; !strings.Contains(p, "grant=<the user's words") {
		t.Fatalf("told %q", p)
	}
	if text, ok := h.tool(pid, "pilot_answer", `{"card":"`+k1+`","allow":true,"grant":"deploy anything"}`); !ok || !strings.Contains(text, "went to the user") {
		t.Fatalf("an unquoted allow: %v %s", ok, text)
	}
	if c := h.u.b.Card(kids[0]); c.Status != kanban.StatusWaiting || !strings.HasPrefix(c.Note, youNote) || len(kf.replies) != 0 || len(notes) != 1 {
		t.Fatalf("to the user: %q %q %v %v", c.Status, c.Note, kf.replies, notes)
	}
	if text, _ := h.tool(pid, "pilot_cards", `{}`); strings.Contains(text, "waits on your pilot_answer") {
		t.Error("the user's request listed as the autopilot's")
	}
	if text, ok := h.tool(pid, "pilot_answer", `{"card":"`+k1+`","allow":true,"grant":"push to feature branches"}`); ok || !strings.Contains(text, "no open request for you") {
		t.Errorf("answered the user's: %s", text)
	}
	h.u.answerAsk(kids[0], true) // the user allows it
	ask("r2", "git push origin fix")
	if _, ok := h.tool(pid, "pilot_answer", `{"card":"`+k1+`","allow":true,"grant":"push to feature branches"}`); !ok || !strings.HasPrefix(kf.replies[len(kf.replies)-1], "r2 allow") {
		t.Fatalf("a quoted allow: %v", kf.replies)
	}
	ask("r3", "make test")
	if text, _ := h.tool(pid, "pilot_answer", `{"card":"`+k1+`","user":true}`); !strings.Contains(text, "Left to the user") {
		t.Errorf("left to the user: %s", text)
	}
	h.u.b.Card(pid).Pilot.Grants = ""
	ask("r4", "curl -X POST https://example.com")
	if p := pf.prompts[len(pf.prompts)-1]; !strings.Contains(p, "asks the user to run") || !strings.HasPrefix(h.u.b.Card(kids[0]).Note, youNote) {
		t.Errorf("no grants: %q", p)
	}
	h.u.moveTo(kids[0], "Review") // its column moves its ticket, the autopilot still running
	h.u.save()
	h.pumpUntil("the push", func() bool { return pushed > 0 && !h.u.pushing[kids[0]] })
}

// TestAutopilotBudget: past its group's budget the autopilot can't start
// cards and every one pauses; the user's word to it goes on.
func TestAutopilotBudget(t *testing.T) {
	h := newHarness(t)
	pid, kids := apStart(t, h, kanban.Pilot{Level: kanban.PilotAuto, Max: 3, Budget: 5})
	if s := h.claudeOf(pid).opts.System; !strings.Contains(s, "$5.00 together") {
		t.Errorf("system: %s", s)
	}
	h.u.b.Card(kids[0]).Session.Cost = 3
	if text, _ := h.tool(pid, "pilot_cards", `{}`); !strings.Contains(text, "Spent $3.00 of $5.00") {
		t.Errorf("cards: %s", text)
	}
	h.u.b.Card(pid).Session.Cost = 2
	k2 := h.u.b.Card(kids[1]).Key()
	if text, ok := h.tool(pid, "pilot_tell", `{"card":"`+k2+`","text":"go"}`); ok || !strings.Contains(text, "the autopilot's $5.00 budget") {
		t.Errorf("tell over budget: %v %s", ok, text)
	}
	if why := h.u.overBudget(h.u.b.Card(kids[1])); !strings.Contains(why, "autopilot's") {
		t.Errorf("a card over: %q", why)
	}
	h.u.deliver(pid, "go on", false)
	if h.u.b.Card(pid).Pilot.From != 5 || h.u.overBudget(h.u.b.Card(kids[1])) != "" {
		t.Error("the user's go-ahead")
	}
}

// TestAutopilotSearch: a search finds an autopilot's cards on the board.
func TestAutopilotSearch(t *testing.T) {
	h := newHarness(t)
	_, kids := apStart(t, h, kanban.Pilot{Level: kanban.PilotAuto})
	h.u.search.SetText("Ship it")
	col, _ := h.u.b.Find(kids[1])
	if !slices.ContainsFunc(h.u.visible(col), func(j int) bool { return h.u.b.Columns[col].Cards[j].ID == kids[1] }) {
		t.Error("search doesn't find it")
	}
}

// TestExternal: what looks like a change outside the cards' folders.
func TestExternal(t *testing.T) {
	for line, want := range map[string]bool{"Bash: git push origin x": true, "Bash: make test": false, "mcp__other__send": true, "Edit: a.go": false} {
		if external(line) != want {
			t.Errorf("%q: %v", line, !want)
		}
	}
}

// apSessions gives card ids sessions, as if each had started.
func apSessions(h *harness, ids ...string) {
	for _, id := range ids {
		h.u.b.Card(id).Session.ID = "s-" + id
	}
}

// TestAutopilotSidebar: an autopilot's cards list right under it in Agent
// Sessions; without its own row, they still list.
func TestAutopilotSidebar(t *testing.T) {
	h := newHarness(t)
	other := h.u.b.Add(0, kanban.Card{Title: "other", Org: "Globex"}).ID
	pid, kids := apStart(t, h, kanban.Pilot{Level: kanban.PilotAuto})
	apSessions(h, other, pid, kids[0], kids[1])
	rows := func() (out []string) {
		for _, s := range h.u.sections() {
			if s.title == "Agent Sessions" {
				for _, it := range s.items {
					out = append(out, strings.TrimPrefix(it.view, "sess:"))
				}
			}
		}
		return out
	}
	got := rows()
	i := slices.Index(got, pid)
	if i < 0 || len(got) != 4 || !slices.Equal(got[i+1:i+3], kids) {
		t.Fatalf("rows %v, autopilot %s, cards %v", got, pid, kids)
	}
	h.frame() // drawn, its cards indented
	h.u.b.Card(pid).Session.ID = ""
	if got := rows(); len(got) != 4 || got[len(got)-3] != pid || !slices.Equal(got[len(got)-2:], kids) {
		t.Errorf("an autopilot not listed still heads its cards: %v", got)
	}
}

// TestAutopilotDone: an autopilot moved to Done takes its cards there,
// done, whatever they were doing; the sidebar lists none of them then.
func TestAutopilotDone(t *testing.T) {
	h := newHarness(t)
	pid, kids := apStart(t, h, kanban.Pilot{Level: kanban.PilotAuto})
	apSessions(h, pid, kids[0], kids[1])
	h.u.b.Card(kids[0]).Status = kanban.StatusBlocked
	h.u.b.Card(kids[1]).Status = kanban.StatusReview
	h.u.moveTo(pid, "Done")
	h.u.save()
	done := h.u.b.Column("Done")
	for _, k := range kids {
		if col, _ := h.u.b.Find(k); col != done || h.u.b.Card(k).Status != kanban.StatusDone || h.u.b.Card(k).Finished.IsZero() {
			t.Errorf("card %s: column %d, %+v", k, col, h.u.b.Card(k))
		}
	}
	for _, s := range h.u.sections() {
		if s.title == "Agent Sessions" && len(s.items) != 0 {
			t.Errorf("still listed: %v", s.items)
		}
	}
}

// TestAutopilotCanvas: an autopilot's canvas (its default) has a node per
// card, wired from its session and into Done; a click opens that card.
func TestAutopilotCanvas(t *testing.T) {
	h := newHarness(t)
	pid, kids := apStart(t, h, kanban.Pilot{Level: kanban.PilotAuto})
	pc := h.u.b.Card(pid)
	if !h.u.canvasOn(pc) {
		t.Error("an autopilot opens on its canvas")
	}
	k0, k1 := h.u.b.Card(kids[0]), h.u.b.Card(kids[1])
	k0.Status, k0.Note = kanban.StatusWorking, "Reading"
	k1.Status = kanban.StatusReview
	g := h.u.cardGraph(pc, time.Now())
	for _, k := range kids {
		n := g.node("kid:" + k)
		if n == nil || n.open != "card:"+k || n.frame != "Autopilot" {
			t.Fatalf("card %s: %+v", k, n)
		}
		if !slices.Contains(g.wires, cvWire{"main", "Cards", n.id, "Run"}) || !slices.Contains(g.wires, cvWire{n.id, "Finish", "done", "Cards"}) {
			t.Errorf("wires %v", g.wires)
		}
	}
	if n := g.node("kid:" + kids[0]); n.dim || !n.who.busy || n.who.status != "Reading" {
		t.Errorf("a working card: %+v %+v", n, n.who)
	}
	if n := g.node("kid:" + kids[1]); !n.dim || n.who.status != "Review" {
		t.Errorf("a card in review: %+v %+v", n, n.who)
	}
	if _, ok := g.node("main").row("Cards"); !ok {
		t.Error("no Cards socket on the autopilot's session")
	}
	if h.u.cardGraph(k0, time.Now()).node("kid:"+kids[1]) != nil {
		t.Error("a card's canvas shows its siblings")
	}
	h.u.cvOpen(pid, "card:"+kids[1])
	if h.u.view != "sess:"+kids[1] {
		t.Errorf("view %q", h.u.view)
	}
}

// TestAutopilotCompany: in the sky an autopilot's cards follow it on the
// outer ring, tethered to it; without it in the sky, they stand alone.
func TestAutopilotCompany(t *testing.T) {
	h := newHarness(t)
	pid, kids := apStart(t, h, kanban.Pilot{Level: kanban.PilotAuto})
	apSessions(h, pid, kids[0], kids[1])
	h.u.view = viewCompany
	h.frame() // the tethers drawn
	_, ps, _ := h.u.coModel(time.Now())
	i := slices.IndexFunc(ps, func(p coPlanet) bool { return p.id == "card:"+pid })
	if i < 0 || i+2 >= len(ps) || ps[i].note != "autopilot · 2 cards" || ps[i].fam != 0 {
		t.Fatalf("autopilot planet at %d of %+v", i, ps)
	}
	for j, k := range kids {
		if p := ps[i+1+j]; p.id != "card:"+k || p.up != "card:"+pid {
			t.Errorf("card %s: %+v", k, p)
		}
	}
	h.u.b.Card(pid).Session.ID = ""
	_, ps, _ = h.u.coModel(time.Now())
	for _, p := range ps {
		if p.id == "card:"+pid || p.ring == 2 && p.up != "" {
			t.Errorf("without the autopilot: %+v", p)
		}
	}
	if n := slices.IndexFunc(ps, func(p coPlanet) bool { return p.id == "card:"+kids[0] }); n < 0 {
		t.Error("its card left the sky")
	}
}
