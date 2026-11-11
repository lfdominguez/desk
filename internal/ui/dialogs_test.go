package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/term"
	"github.com/lfdominguez/desk/internal/ui/kit"
	"github.com/lfdominguez/desk/internal/vault"
)

// sessionWith puts card c in the full window with a fake Claude asking r.
func sessionWith(h *harness, c kanban.Card, r *claude.Request) *fakeClaude {
	f := &fakeClaude{}
	h.u.procs[c.ID] = f
	h.u.view = "sess:" + c.ID
	h.u.asks[c.ID] = newAsk(r)
	h.frame()
	return f
}

// TestPermissionKeys: N denies, Y allows, S allows the tool for the rest
// of desk's run (the next ask of it answers itself), E edits the command
// and enter allows it as edited.
func TestPermissionKeys(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	bash := func(id, cmd string) *claude.Request {
		return &claude.Request{ID: id, Tool: "Bash", Line: cmd, Input: json.RawMessage(`{"command":"` + cmd + `"}`)}
	}
	f := sessionWith(h, c, bash("r1", "ls"))
	h.send(press("N", 0))
	if len(f.replies) != 1 || f.replies[0] != "r1 deny The user denied it." || h.u.asks[c.ID] != nil {
		t.Fatalf("N: %q", f.replies)
	}
	h.u.asks[c.ID] = newAsk(bash("r2", "ls"))
	h.frame() // its keys listen from the frame that draws it
	h.send(press("Y", 0))
	if len(f.replies) != 2 || !strings.HasPrefix(f.replies[1], "r2 allow") {
		t.Fatalf("Y: %q", f.replies)
	}
	h.u.asks[c.ID] = newAsk(bash("r3", "make"))
	h.frame()
	h.send(press("E", 0))
	a := h.u.asks[c.ID]
	if !a.editing || a.cmd.Text() != "make" || !h.u.typing(gtxFor(h)) {
		t.Fatalf("E: editing %v, %q", a.editing, a.cmd.Text())
	}
	h.send(press("Y", 0)) // a letter in the command, not an answer
	if len(f.replies) != 2 {
		t.Fatal("Y while editing answered")
	}
	a.cmd.SetText("make test")
	h.send(press(key.NameReturn, 0))
	if len(f.replies) != 3 || !strings.Contains(f.replies[2], `"command":"make test"`) {
		t.Fatalf("edited: %q", f.replies)
	}
	h.u.asks[c.ID] = newAsk(bash("r4", "ls"))
	h.frame()
	h.send(press("S", 0))
	if len(f.replies) != 4 || !strings.HasPrefix(f.replies[3], "r4 allow") || !h.u.allowed[c.ID]["Bash"] {
		t.Fatalf("S: %q", f.replies)
	}
	h.u.onEvent(c.ID, 0, claude.Event{Kind: claude.KindAsk, Ask: bash("r5", "pwd")})
	if len(f.replies) != 5 || !strings.HasPrefix(f.replies[4], "r5 allow") || h.u.asks[c.ID] != nil {
		t.Fatalf("allowed Bash still asked: %q", f.replies)
	}
	h.u.onEvent(c.ID, 0, claude.Event{Kind: claude.KindAsk, Ask: &claude.Request{ID: "r6", Tool: "Write", Line: "a.txt", Input: json.RawMessage(`{}`)}})
	if h.u.asks[c.ID] == nil || len(f.replies) != 5 {
		t.Fatal("another tool was let through")
	}
	if h.u.asks[c.ID].command() != "" {
		t.Fatal("a Write has a command to edit")
	}
	h.u.allowAll("gone") // no ask: nothing
}

// TestPermissionButtons: the split button lists the other ways to allow.
func TestPermissionButtons(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	f := sessionWith(h, c, &claude.Request{ID: "r1", Tool: "Bash", Line: "ls", Input: json.RawMessage(`{"command":"ls"}`)})
	h.clickButton("askMore" + c.ID)
	if !h.u.asks[c.ID].more {
		t.Fatal("the chevron didn't list the ways to allow")
	}
	h.clickButton("askAll" + c.ID)
	if len(f.replies) != 1 || !h.u.allowed[c.ID]["Bash"] {
		t.Fatalf("Allow every: %q", f.replies)
	}
	h.u.asks[c.ID] = newAsk(&claude.Request{ID: "r2", Tool: "Bash", Line: "ls", Input: json.RawMessage(`{"command":"ls"}`)})
	h.u.asks[c.ID].more = true
	h.frame()
	h.clickButton("askOnce" + c.ID)
	h.u.asks[c.ID] = newAsk(&claude.Request{ID: "r3", Tool: "Bash", Line: "ls", Input: json.RawMessage(`{"command":"ls"}`)})
	h.frame()
	h.clickButton("askEdit" + c.ID)
	h.frame()
	if a := h.u.asks[c.ID]; !a.editing || a.cmd.Text() != "ls" {
		t.Fatal("Edit")
	}
	h.clickButton("askAllow" + c.ID)
	if len(f.replies) != 3 || !strings.HasPrefix(f.replies[1], "r2 allow") || !strings.HasPrefix(f.replies[2], "r3 allow") {
		t.Fatalf("replies %q", f.replies)
	}
}

// TestInterviewSteps: one question at a time; digits pick (one answer
// replaces the other, several toggle), the last digit goes to the free
// answer; enter needs an answer to go on; Back and the steps move; the
// last enter sends all.
func TestInterviewSteps(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	var qs []claude.Question
	json.Unmarshal([]byte(`[{"question":"Where?","options":[{"label":"eu"},{"label":"us"}]},{"question":"Then?","header":"After","multiSelect":true,"options":[{"label":"keep"},{"label":"tell"}]}]`), &qs)
	f := sessionWith(h, c, &claude.Request{ID: "r1", Tool: "AskUserQuestion", Input: json.RawMessage(`{}`), Questions: qs})
	a := h.u.asks[c.ID]
	h.send(press(key.NameReturn, 0))
	if a.cur != 0 || h.u.toast != "Pick an option or write your own" || !h.u.toastBad {
		t.Fatalf("enter with no answer: cur %d, toast %q", a.cur, h.u.toast)
	}
	h.send(press("1", 0), press("2", 0))
	if !slices.Equal(a.picks[0], []bool{false, true}) {
		t.Fatalf("one answer: %v", a.picks[0])
	}
	h.send(press(key.NameReturn, 0))
	if a.cur != 1 {
		t.Fatal("enter didn't go on")
	}
	h.send(press("1", 0), press("2", 0))
	if !slices.Equal(a.picks[1], []bool{true, true}) {
		t.Fatalf("several: %v", a.picks[1])
	}
	h.send(press("3", 0)) // the free answer takes the keys
	if !h.u.typing(gtxFor(h)) {
		t.Fatal("3 didn't focus the free answer")
	}
	h.u.blur = true
	h.frame()
	h.clickButton("askBack" + c.ID)
	if a.cur != 0 {
		t.Fatal("Back")
	}
	h.clickButton("askStep" + c.ID + "-1")
	if a.cur != 1 {
		t.Fatal("the step chip")
	}
	h.send(press(key.NameReturn, 0))
	if len(f.replies) != 1 || !strings.Contains(f.replies[0], `"Then?":"keep, tell"`) || !strings.Contains(f.replies[0], `"Where?":"us"`) {
		t.Fatalf("sent %q", f.replies)
	}
	h.u.next("gone") // no ask: nothing
}

// TestAskGuards: the box's keys wait while something else has the keyboard
// (a dialog, the picker, an editor) and take presses only; denying an
// edited command denies it; a card gone or a process gone answers nothing;
// the free answers submit with enter; the chat keeps the questions numbered.
func TestAskGuards(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	ls := func(id string) *claude.Request {
		return &claude.Request{ID: id, Tool: "Bash", Line: "ls", Input: json.RawMessage(`{"command":"ls"}`)}
	}
	f := sessionWith(h, c, ls("r1"))
	blocked := map[string]func(on bool){
		"dialog": func(on bool) { h.u.edit = map[bool]*sheet{true: {}}[on] },
		"unlock": func(on bool) { h.u.unlock = map[bool]*unlockDialog{true: {}}[on] },
		"config": func(on bool) { h.u.cfg = map[bool]*cfgDialog{true: {}}[on] },
		"picker": func(on bool) { h.u.pick = map[bool]string{true: "x"}[on] },
		"none":   func(bool) {}, // the control: Y answers
	}
	for what, set := range blocked {
		set(true)
		var act func()
		h.r.Queue(press("Y", 0))
		h.u.askKeys(gtxFor(h), c.ID, h.u.asks[c.ID], &act)
		set(false)
		if (act != nil) != (what == "none") {
			t.Fatalf("Y under the %s: answered %v", what, act != nil)
		}
		for gtx := gtxFor(h); ; { // drop the Y left waiting, or the next frame answers with it
			if _, ok := gtx.Event(key.Filter{Name: "Y"}); !ok {
				break
			}
		}
		h.frame()
	}
	h.send(key.Event{Name: "Y", State: key.Release})
	if len(f.replies) != 0 {
		t.Fatal("a key's release answered")
	}
	a := h.u.asks[c.ID]
	a.editing = true
	a.cmd.SetText("rm -rf /")
	h.u.answerAsk(c.ID, false)
	if len(f.replies) != 1 || f.replies[0] != "r1 deny The user denied it." || h.u.toastBad {
		t.Fatalf("deny while editing: %q", f.replies)
	}
	h.u.asks[c.ID] = newAsk(ls("r2"))
	h.u.b.Delete(c.ID)
	h.u.answerAsk(c.ID, true) // the card went: nothing to answer for
	if len(f.replies) != 1 {
		t.Fatal("answered for a gone card")
	}
	var qs []claude.Question
	json.Unmarshal([]byte(`[{"question":"Where?","options":[{"label":"eu"},{"label":"us"}]},{"question":"When?"}]`), &qs)
	if s := interviewText(qs); s != "1. Where? (eu / us)\n2. When? ()" {
		t.Fatalf("interviewText %q", s)
	}
	if b := newAsk(&claude.Request{Questions: qs}); !b.other[1].SingleLine || !b.other[1].Submit || !b.cmd.Submit {
		t.Fatal("the free answers don't submit with enter")
	}
}

// TestInterviewUnanswered: sending with an earlier question unanswered
// goes back to it.
func TestInterviewUnanswered(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	var qs []claude.Question
	json.Unmarshal([]byte(`[{"question":"A?","options":[{"label":"a"}]},{"question":"B?","options":[{"label":"b"}]}]`), &qs)
	f := sessionWith(h, c, &claude.Request{ID: "r1", Tool: "AskUserQuestion", Input: json.RawMessage(`{}`), Questions: qs})
	a := h.u.asks[c.ID]
	a.cur, a.picks[1][0] = 1, true
	h.frame()
	h.clickButton("askSend" + c.ID)
	if len(f.replies) != 0 || a.cur != 0 || !h.u.toastBad {
		t.Fatalf("sent with A unanswered: %q, cur %d", f.replies, a.cur)
	}
	a.other[0].SetText("mine")
	h.send(press(key.NameReturn, 0))
	h.send(press(key.NameReturn, 0))
	if len(f.replies) != 1 || !strings.Contains(f.replies[0], `"A?":"mine"`) {
		t.Fatalf("sent %q", f.replies)
	}
}

// TestMenuKeys: the open menu's keys run its items: a digit moves the
// card, Backspace archives it, Ctrl+Z (or Undo) puts it back.
func TestMenuKeys(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	id := c.ID
	h.u.menu, h.u.sel = &cardMenu{id: id, at: image.Pt(400, 200)}, id
	h.frame() // draws the items; the next frame listens for their keys
	h.frame()
	h.send(press("2", 0))
	if col, _ := h.u.b.Find(id); col != 1 || h.u.menu != nil {
		t.Fatalf("2 moved to %d, menu %v", col, h.u.menu != nil)
	}
	h.u.menu = &cardMenu{id: id, at: image.Pt(400, 200)}
	h.frame()
	h.frame()
	h.send(press(key.NameDeleteBackward, 0))
	if h.u.b.Card(id).Archived.IsZero() || h.u.undo == nil || h.u.toast != "Archived DESK-1" {
		t.Fatalf("Backspace: toast %q", h.u.toast)
	}
	h.send(press("Z", key.ModShortcut))
	if !h.u.b.Card(id).Archived.IsZero() || h.u.toast != "Restored DESK-1" || h.u.undo != nil {
		t.Fatalf("Ctrl+Z: toast %q", h.u.toast)
	}
	h.u.menu = &cardMenu{id: id, at: image.Pt(400, 200)}
	h.frame()
	h.frame()
	h.send(press("C", key.ModShortcut))
	if _, b, ok := h.r.WriteClipboard(); !ok || string(b) != "DESK-1" {
		t.Fatalf("Ctrl+C: %q", b)
	}
	h.u.archiveCard(id)
	h.frame()
	h.clickButton("toastUndo")
	if !h.u.b.Card(id).Archived.IsZero() {
		t.Fatal("Undo")
	}
	h.u.showUndo("late", func() { t.Fatal("undone after the toast went") })
	h.u.toastUntil = time.Now().Add(-time.Second)
	h.u.runUndo()
}

// TestMenuPlacement: a menu opened at the window's corner stays inside it;
// Move to's columns open on hover, on the right or, short of room, the
// left; a plain row's hover closes them. A running session's Archive says
// it stops it, the card's own column is marked, and keys that aren't the
// menu's are left to the board.
func TestMenuPlacement(t *testing.T) {
	h := newHarness(t)
	h.size = image.Pt(1700, 900)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	id := c.ID
	h.u.procs[id] = &fakeClaude{}
	h.u.menu = &cardMenu{id: id, at: h.size}
	h.frame()
	m := h.u.menu
	if m.r.Max != h.size.Sub(boardAt).Sub(image.Pt(4, 4)) || m.r.Dx() != 260 { // in board coordinates
		t.Fatalf("menu at %v in %v", m.r, h.size)
	}
	if last := m.items[len(m.items)-1]; last.label != "Archive and stop the session" {
		t.Fatalf("archive row %q", last.label)
	}
	if here := m.items[8]; !strings.HasSuffix(here.label, " · here") || here.fg != kit.CText3 {
		t.Fatalf("own column %+v", here)
	}
	r := m.r
	hover := func(id string) {
		for y := r.Min.Y + 2; y < r.Max.Y; y += 4 {
			h.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(float32(boardAt.X+r.Min.X+40), float32(boardAt.Y+y))})
			if h.u.k.Click(id).Hovered() {
				return
			}
		}
		t.Fatalf("%s not under the menu", id)
	}
	hover("cardMenuMove to")
	if !m.sub || m.r.Min.X != r.Min.X-204 || m.r.Max.X != r.Max.X {
		t.Fatalf("columns at the right edge: sub %v, %v from %v", m.sub, m.r, r)
	}
	hover("cardMenuEdit card")
	if m.sub {
		t.Fatal("a plain row's hover left the columns open")
	}
	h.u.menu = &cardMenu{id: id, at: image.Pt(10, 10)}
	h.frame()
	m, r = h.u.menu, h.u.menu.r
	hover("cardMenuMove to")
	if m.r.Max.X != r.Max.X+204 {
		t.Fatalf("columns at the left: %v from %v", m.r, r)
	}
	if h.u.menuKey("Q") || h.u.menu == nil {
		t.Fatal("Q was the menu's")
	}
	if !h.u.menuKey("E") || h.u.menu != nil || h.u.edit == nil {
		t.Fatal("E")
	}
	h.u.edit = nil
	h.u.archiveCard(id)
	if h.u.toastBad || h.u.toast != "Archived DESK-1; its session stopped" {
		t.Fatalf("toast %q bad %v", h.u.toast, h.u.toastBad)
	}
	h.u.runUndo()
	if h.u.toastBad {
		t.Fatal("Restored is bad news")
	}
	h.u.copyKey = "DESK-1"
	h.frame()
	if h.u.toastBad || h.u.toast != "Copied DESK-1" || h.u.copyKey != "" {
		t.Fatalf("copy toast %q", h.u.toast)
	}
	h.u.showUndo("x", func() {})
	if h.u.toastBad {
		t.Fatal("an undo toast is bad news")
	}
}

// TestDrawerSaves: the drawer saves as it closes (esc, ×), keeps open on
// a bad due date, and Ctrl+Backspace deletes with an undo that puts the
// card back where it was.
func TestDrawerSaves(t *testing.T) {
	h := newHarness(t)
	h.u.b.Add(0, kanban.Card{Title: "first", Org: "Acme"})
	c := h.u.b.Add(0, kanban.Card{Title: "old", Org: "Acme"})
	h.u.b.Add(0, kanban.Card{Title: "last", Org: "Acme"})
	h.u.open(c.ID)
	h.frame()
	h.u.edit.title.SetText("new")
	h.send(press(key.NameEscape, 0))
	if h.u.edit != nil || h.u.b.Card(c.ID).Title != "new" {
		t.Fatalf("esc: open %v, title %q", h.u.edit != nil, h.u.b.Card(c.ID).Title)
	}
	h.u.open(c.ID)
	h.frame()
	h.u.edit.due.SetText("someday")
	h.clickButton("shClose")
	if h.u.edit == nil || !strings.HasPrefix(h.u.toast, "Due date") {
		t.Fatalf("a bad due date closed it: toast %q", h.u.toast)
	}
	h.u.edit.due.SetText("")
	h.clickButton("shNotes")
	if !h.u.edit.editNotes {
		t.Fatal("a click on the notes didn't edit them")
	}
	h.send(press(key.NameDeleteBackward, key.ModShortcut))
	if h.u.edit != nil || h.u.b.Card(c.ID) != nil || h.u.toast != "Deleted DESK-2" {
		t.Fatalf("Ctrl+Backspace: toast %q", h.u.toast)
	}
	h.send(press("Z", key.ModShortcut))
	if cs := h.u.b.Columns[0].Cards; len(cs) != 3 || cs[1].ID != c.ID {
		t.Fatalf("undo put it at %v", cs)
	}
	h.u.open(c.ID)
	h.frame()
	h.clickButton("shSess")
	if h.u.edit != nil || h.u.panel != c.ID {
		t.Fatal("the session button")
	}
	h.u.panel = ""
	h.u.open(c.ID)
	h.frame()
	h.u.edit.title.SetText("beside")
	in := f32.Pt(float32(h.size.X-100), 300) // on the drawer: stays open
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: in},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: in})
	if h.u.edit == nil {
		t.Fatal("a click on the drawer closed it")
	}
	out := f32.Pt(100, 300)
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: out},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: out})
	if h.u.edit != nil || h.u.b.Card(c.ID).Title != "beside" {
		t.Fatal("a click beside the drawer didn't close it, saving")
	}
	now := time.Date(2026, 10, 31, 10, 0, 0, 0, time.Local)
	if dueDay("2026-11-06", now) != "Fri, Nov 6" || dueDay("2026-10-30", now) != "Fri, Oct 30 · overdue" || dueDay("soon", now) != "" {
		t.Fatalf("dueDay %q %q", dueDay("2026-11-06", now), dueDay("2026-10-30", now))
	}
}

// TestNewCardTokens: Ctrl+Enter makes the card from the request alone:
// @org, #label, !priority, a day and a ticket key fill its fields and
// leave the title; a pill's click makes its word plain text.
func TestNewCardTokens(t *testing.T) {
	h := newHarness(t)
	h.u.newCard(0)
	s := h.u.edit
	s.notes.SetText("Fix login on @acme tomorrow !high #auth #web\nsteps follow")
	h.frame()
	h.clickButton("pilllabweb") // #web: plain text
	if !s.plain["#web"] {
		t.Fatal("the pill didn't make #web plain")
	}
	h.send(press(key.NameReturn, key.ModShortcut))
	c := h.u.b.Card(h.u.panel)
	if c == nil || c.Title != "Fix login on tomorrow #web" || c.Org != "Acme" || c.Priority != kanban.PrioHigh ||
		!slices.Equal(c.Labels, []string{"auth"}) || c.Due != time.Now().AddDate(0, 0, 1).Format(kanban.DateLayout) {
		t.Fatalf("created %+v", c)
	}
}

// TestPills: a new card's pills come from its request's tokens (a click
// makes the word plain) unless a field says otherwise (a click shows the
// fields); an org is asked for until there is one; the last pill toggles
// the other fields.
func TestPills(t *testing.T) {
	h := newHarness(t)
	h.u.newCard(0)
	s := h.u.edit
	s.org.SetText("")
	shown := func() (out []string) {
		for _, p := range h.u.pillsOf(s) {
			out = append(out, p.id+"="+p.label)
		}
		return out
	}
	get := func(id string) pill {
		for _, p := range h.u.pillsOf(s) {
			if p.id == id {
				return p
			}
		}
		t.Fatalf("no pill %s in %q", id, shown())
		return pill{}
	}
	if got := shown(); !slices.Equal(got, []string{"org=Pick the organization", "more=+ Folder, links, due, column…"}) || get("org").fg != kit.COrange {
		t.Fatalf("empty: %q", got)
	}
	get("more").f()
	if !s.more || get("more").label != "− Fewer fields" {
		t.Fatal("more")
	}
	get("more").f()
	s.org.SetText("Acme")
	get("org").f()
	if get("org").label != "Acme" || !s.more {
		t.Fatal("the org field's pill shows the fields")
	}
	s.more = false
	tomorrow := time.Now().AddDate(0, 0, 1)
	s.notes.SetText("Fix @globex !high tomorrow #auth https://example.com/x")
	h.u.orgList = append(h.u.orgList, "Globex")
	want := []string{"org=Globex", "prio=High", "due=Due " + tomorrow.Format("Mon, Jan 2"), "labauth=auth", "linkhttps://example.com/x=https://example.com/x", "more=+ Folder, links, due, column…"}
	if got := shown(); !slices.Equal(got, want) {
		t.Fatalf("tokens:\n%q\nwant\n%q", got, want)
	}
	for _, id := range []string{"org", "prio", "due", "labauth", "linkhttps://example.com/x"} {
		get(id).f()
		if s.more {
			t.Fatalf("%s's pill showed the fields", id)
		}
	}
	if len(s.plain) != 5 {
		t.Fatalf("plain %v", s.plain)
	}
	clear(s.plain)
	s.prio = 2
	s.due.SetText("2026-11-05")
	s.labels.SetText("auth")
	s.links.SetText("https://example.com/x")
	s.dir.SetText("api/")
	want = []string{"org=Globex", "dir=api", "prio=Medium", "due=Due Thu, Nov 5", "labauth=auth", "linkhttps://example.com/x=https://example.com/x", "more=+ Folder, links, due, column…"}
	if got := shown(); !slices.Equal(got, want) {
		t.Fatalf("fields:\n%q\nwant\n%q", got, want)
	}
	for _, id := range []string{"dir", "prio", "due", "labauth", "linkhttps://example.com/x"} {
		s.more = false
		get(id).f()
		if !s.more || len(s.plain) != 0 {
			t.Fatalf("%s's pill didn't show the fields", id)
		}
	}
	if !get("dir").mono || !get("linkhttps://example.com/x").mono || get("org").dot != kit.CAcc {
		t.Fatal("looks")
	}
	s.optimized = true // the draft wrote the tokens into the fields
	if get("org").label != "Acme" {
		t.Fatalf("optimized org %q", get("org").label)
	}
}

// TestNewCardDraft: a closed new card keeps its request for the next;
// creating clears it.
func TestNewCardDraft(t *testing.T) {
	h := newHarness(t)
	h.u.newCard(0)
	h.u.edit.notes.SetText("half a thought")
	h.send(press(key.NameEscape, 0))
	h.u.newCard(0)
	if h.u.edit.notes.Text() != "half a thought" {
		t.Fatalf("draft %q", h.u.edit.notes.Text())
	}
	h.u.edit.org.SetText("Acme")
	h.frame()
	h.send(press(key.NameReturn, key.ModShortcut))
	if h.u.draft != "" {
		t.Fatalf("draft after create %q", h.u.draft)
	}
}

// TestSuggestions: Ctrl+J asks Claude; Use mine and Discard take its
// suggestions back; Accept all drops the marks.
func TestSuggestions(t *testing.T) {
	h := newHarness(t)
	optimize = func(_ context.Context, a claude.Ask, _ func(claude.Progress)) (claude.Draft, error) {
		return claude.Draft{Title: "Their title", Prompt: "Their notes"}, nil
	}
	h.u.newCard(0)
	s := h.u.edit
	s.notes.SetText("my words @Acme")
	h.frame()
	h.send(press("J", key.ModShortcut))
	h.pumpUntil("the draft", func() bool { return s.optimized })
	if s.org.Text() != "Acme" || s.title.Text() != "Their title" || s.mineNotes != "my words @Acme" {
		t.Fatalf("org %q title %q mine %q", s.org.Text(), s.title.Text(), s.mineNotes)
	}
	h.frame()
	h.clickButton("shMineTitle")
	h.clickButton("shMineNotes")
	if s.title.Text() != "my words" || s.notes.Text() != "my words @Acme" || s.preview {
		t.Fatalf("Use mine: %q %q", s.title.Text(), s.notes.Text())
	}
	h.clickButton("shAccept")
	if !s.accepted {
		t.Fatal("Accept all")
	}
	h.clickButton("shDiscard")
	if s.optimized || s.notes.Text() != "my words @Acme" || s.optNote != improveNote {
		t.Fatal("Discard suggestions")
	}
}

// TestImproveLive: while Improve runs, its steps and the draft as it
// streams show, the fields take no clicks, and the end clears them.
func TestImproveLive(t *testing.T) {
	h := newHarness(t)
	wrote, done := make(chan bool), make(chan bool)
	optimize = func(_ context.Context, a claude.Ask, on func(claude.Progress)) (claude.Draft, error) {
		on(claude.Progress{Step: "Reading CLAUDE.md"})
		on(claude.Progress{Step: "Writing", Draft: claude.Draft{Title: "Their ti", Prompt: "Their **no"}})
		on(claude.Progress{Step: "Thinking"})
		wrote <- true
		<-done
		return claude.Draft{Title: "Their title", Prompt: "Their notes"}, nil
	}
	h.u.newCard(0)
	s := h.u.edit
	s.notes.SetText("my words @Acme")
	h.frame()
	h.send(press("J", key.ModShortcut))
	<-wrote
	h.pumpUntil("the progress", func() bool { return s.optStep == "Thinking" })
	if s.optStage != 2 || s.optLive.Title != "Their ti" || s.optLive.Prompt != "Their **no" || s.optTickets {
		t.Fatalf("stage %d live %+v tickets %v", s.optStage, s.optLive, s.optTickets)
	}
	h.frame()
	if _, ok := h.seek("pillmore", image.Rectangle{Max: h.size}, h.frame); ok {
		t.Fatal("the pills took a click while Improve ran")
	}
	if _, ok := h.seek("shClose", image.Rectangle{Max: h.size}, h.frame); !ok {
		t.Fatal("the header's × should still close")
	}
	close(done)
	h.pumpUntil("the draft", func() bool { return s.optimized })
	if s.optStep != "" || s.optLive.Title != "" {
		t.Fatalf("left over: %q %+v", s.optStep, s.optLive)
	}
	h.frame()
	if _, ok := h.seek("pillmore", image.Rectangle{Max: h.size}, h.frame); !ok {
		t.Fatal("the pills should click again")
	}
}

// TestImproveClosed: progress from a run whose dialog closed is dropped.
func TestImproveClosed(t *testing.T) {
	h := newHarness(t)
	closed, done := make(chan bool), make(chan bool)
	optimize = func(_ context.Context, a claude.Ask, on func(claude.Progress)) (claude.Draft, error) {
		<-closed
		on(claude.Progress{Step: "late"})
		close(done)
		return claude.Draft{}, nil
	}
	h.u.newCard(0)
	s := h.u.edit
	s.notes.SetText("my words @Acme")
	h.frame()
	h.send(press("J", key.ModShortcut))
	h.u.closeSheet()
	close(closed)
	<-done
	h.frame()
	h.frame()
	if s.optStep == "late" {
		t.Fatal("a closed dialog took progress")
	}
}

// TestImproveAsks: the org's folders go to Claude without hidden ones;
// no org or no request is a bad toast, not a run.
func TestImproveAsks(t *testing.T) {
	h := newHarness(t)
	os.MkdirAll(filepath.Join(h.u.root, "Acme", ".git"), 0o755)
	var got []string
	lang := ""
	optimize = func(_ context.Context, a claude.Ask, _ func(claude.Progress)) (claude.Draft, error) {
		got, lang = a.Folders, a.Lang
		return claude.Draft{Title: "t"}, nil
	}
	orgs.SaveLangs("Spanish", "French") // the settings' start every new card
	h.u.newCard(0)
	s := h.u.edit
	if s.lang.Text() != "Spanish" || s.chatLang.Text() != "French" {
		t.Fatalf("languages %q %q", s.lang.Text(), s.chatLang.Text())
	}
	s.lang.SetText(" German ") // this card's own
	s.notes.SetText("do it @Acme")
	h.u.optimizeCard()
	h.pumpUntil("the draft", func() bool { return s.optimized })
	if !slices.Equal(got, []string{"api"}) || lang != "German" {
		t.Fatalf("folders %q, language %q", got, lang)
	}
	h.u.commit()
	if c := h.only(); c.Lang != "German" || c.ChatLang != "French" {
		t.Fatalf("the card's languages %q %q", c.Lang, c.ChatLang)
	}
	for _, c := range []struct{ org, notes string }{{"Nowhere", "x"}, {"Acme", " "}} {
		h.u.newCard(0)
		h.u.edit.org.SetText(c.org)
		h.u.edit.notes.SetText(c.notes)
		h.u.toastBad = false
		h.u.optimizeCard()
		if !h.u.toastBad || h.u.edit.optStop != nil {
			t.Fatalf("%+v: toast %q bad %v", c, h.u.toast, h.u.toastBad)
		}
	}
}

// TestApplyDraftKeeps: the user's priority, folder and due stay; a
// folder that isn't one isn't taken.
func TestApplyDraftKeeps(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(filepath.Join(h.u.root, "Acme", "f.txt"), nil, 0o644)
	h.u.newCard(0)
	s := h.u.edit
	s.prio = 1
	s.dir.SetText("mine")
	s.due.SetText("2026-01-01")
	s.opened = false
	h.u.applyDraft(s, "Acme", claude.Draft{Priority: "high", Folder: "api", Due: "2026-12-01"})
	if s.prio != 1 || s.dir.Text() != "mine" || s.due.Text() != "2026-01-01" || !s.opened {
		t.Fatalf("prio %d dir %q due %q", s.prio, s.dir.Text(), s.due.Text())
	}
	s.dir.SetText("")
	h.u.applyDraft(s, "Acme", claude.Draft{Folder: "f.txt"})
	if s.dir.Text() != "" {
		t.Fatalf("a file taken as the folder: %q", s.dir.Text())
	}
}

// TestScanFolders: with scanWait the preview fills later, off the UI
// goroutine; a scan whose folders changed meanwhile, or whose dialog
// closed, is dropped.
func TestScanFolders(t *testing.T) {
	h := newHarness(t)
	h.u.scanWait = 5 * time.Millisecond
	h.u.openConfig(cfgFolders)
	d := h.u.cfg
	h.frame()
	if d.prev != nil || d.prevKey == "" {
		t.Fatalf("scanned at once: %v %q", d.prev, d.prevKey)
	}
	h.pumpUntil("the scan", func() bool { return len(d.prev) == 2 })
	fs := []orgs.Folder{{Path: h.u.root, Depth: 1}}
	for _, c := range []struct {
		key string
		cfg *cfgDialog
	}{{"older", d}, {d.prevKey, nil}} {
		d.prev = nil
		h.u.cfg = c.cfg
		h.u.scanFolders(d, c.key, fs)
		time.Sleep(30 * time.Millisecond)
		h.frame()
		h.frame()
		h.frame()
		if d.prev != nil {
			t.Fatalf("%+v: a stale scan landed", c.key)
		}
	}
}

// TestTermNotify: a terminal's output redraws only while it is the one
// shown; its exit always does, for reapTerms.
func TestTermNotify(t *testing.T) {
	h := newHarness(t)
	h.frame() // the board: no terminal shown
	g := h.u.termGen.Load()
	h.u.termNotify("a", nil)
	live, err := term.Start(prog("cat"), 20, 5, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	h.u.termNotify("a", live)
	if h.u.termGen.Load() != g {
		t.Fatal("a hidden terminal's output redrew")
	}
	tm, err := term.Start(prog(), 20, 5, func() {})
	if err != nil {
		t.Fatal(err)
	}
	h.pumpUntil("the exit", func() bool { done, _ := tm.Exited(); return done })
	h.u.termNotify("a", tm)
	if h.u.termGen.Load() != g+1 {
		t.Fatal("a hidden terminal's exit didn't redraw")
	}
	h.u.view = "term:a"
	h.frame()
	h.u.termNotify("a", nil)
	if h.u.termGen.Load() != g+2 {
		t.Fatal("the shown terminal's output didn't redraw")
	}
}

// TestShellBuf: a long output keeps only its end, marked cut; writes are
// counted, so the ticker can tell nothing new came.
func TestShellBuf(t *testing.T) {
	var s shellBuf
	s.Write([]byte("short\n\n"))
	if txt, n := s.text(); txt != "short" || n != 1 {
		t.Fatalf("%q %d", txt, n)
	}
	if _, _, ok := s.since(1); ok {
		t.Fatal("nothing new since the first write")
	}
	if txt, n, ok := s.since(0); !ok || n != 1 || txt != "short" {
		t.Fatalf("since 0: %q %d %v", txt, n, ok)
	}
	big := strings.Repeat("x", 3*shellKept)
	s.Write([]byte(big + "END\n"))
	txt, n := s.text()
	if n != 2 || len(s.b) > 2*shellKept || !strings.HasPrefix(txt, "…") || !strings.HasSuffix(txt, "END") || len(txt) != len("…")+shellKept {
		t.Fatalf("n %d buf %d text %d", n, len(s.b), len(txt))
	}
	s.Write([]byte(strings.Repeat("\n", shellKept))) // what's kept is now mostly newlines: short, but still cut
	if txt, _ := s.text(); !strings.HasPrefix(txt, "…") || len(txt) >= shellKept {
		t.Fatalf("a cut output reads cut: %d", len(txt))
	}
}

// TestSweepSels: selectable text not drawn for selKeep goes, at most once
// a minute; what was drawn lately, and the text being selected, stay.
func TestSweepSels(t *testing.T) {
	h := newHarness(t)
	now := time.Now()
	old, kept := &richSel{}, &richSel{}
	h.u.rsels = map[string]*richSel{"old": old, "new": {}, "held": kept}
	h.u.sels = map[string]*widget.Selectable{"old": {}, "new": {}}
	h.u.selSeen = map[string]time.Time{"old": now.Add(-selKeep - time.Second), "new": now, "held": now.Add(-2 * selKeep)}
	h.u.chatSel = kept
	h.u.selSwept = now.Add(-30 * time.Second)
	h.u.sweepSels(now)
	if len(h.u.rsels) != 3 {
		t.Fatal("swept again within the minute")
	}
	h.u.selSwept = now.Add(-time.Minute - time.Second)
	h.u.sweepSels(now)
	if h.u.rsels["old"] != nil || h.u.sels["old"] != nil || h.u.rsels["new"] == nil || h.u.sels["new"] == nil || h.u.rsels["held"] != kept || h.u.selSwept != now {
		t.Fatalf("rsels %v sels %v", h.u.rsels, h.u.sels)
	}
}

// TestSnoozeKeys: a digit snoozes for that duration; the typed time reads
// as its end; until() names the day only past today.
func TestSnoozeKeys(t *testing.T) {
	h := newHarness(t)
	got := time.Duration(-1)
	sz := image.Pt(800, 420)
	w := func(gtx layout.Context) layout.Dimensions {
		return h.u.snoozePicker(gtx, "p", func(d time.Duration) { got = d })
	}
	h.draw(sz, w)
	h.r.Queue(press("2", 0))
	h.draw(sz, w)
	if got != 4*time.Hour {
		t.Fatalf("2: %v", got)
	}
	got = -1
	last := key.Name(fmt.Sprint(len(snoozeFor)))
	h.r.Queue(key.Event{Name: last, State: key.Release})
	h.draw(sz, w)
	if got != -1 {
		t.Fatal("a key's release snoozed")
	}
	h.r.Queue(press(last, 0))
	h.draw(sz, w)
	if got != snoozeFor[len(snoozeFor)-1].d {
		t.Fatalf("%s: %v", last, got)
	}
	got = -1
	h.draw(sz, func(gtx layout.Context) layout.Dimensions {
		gtx.Execute(key.FocusCmd{Tag: &h.u.snoozeEd})
		return w(gtx)
	})
	h.draw(sz, w)
	h.r.Queue(press("1", 0), key.EditEvent{Text: "1"})
	h.draw(sz, w)
	if got != -1 {
		t.Fatal("a digit typed in the time snoozed")
	}
	got = -1
	h.u.snoozeEd.SetText("")
	h.clickIn(sz, image.Rectangle{Max: sz}, w, "pcustom") // nothing typed: the highlighted one
	if got != snoozeFor[h.u.snoozeSel].d {
		t.Fatalf("Snooze with nothing typed: %v", got)
	}
	now := time.Date(2026, 10, 31, 10, 0, 0, 0, time.Local)
	if until(now, time.Hour) != "11:00" || until(now, 24*time.Hour) != "Sun 10:00" || until(now, 7*24*time.Hour) != "Sat, Nov 7" {
		t.Fatalf("until: %q %q %q", until(now, time.Hour), until(now, 24*time.Hour), until(now, 7*24*time.Hour))
	}
}

// TestUnlockShow: Show unmasks the password; Not now puts the vault off.
func TestUnlockShow(t *testing.T) {
	h := newHarness(t)
	h.u.askUnlock(&vault.LockedError{Vault: orgs.Vault{Name: "v", Kind: "passbolt", URL: "https://pass.example.com"}})
	h.frame()
	h.clickButton("ulShow")
	h.frame()
	if !h.u.unlock.show || h.u.unlock.pass.Mask != 0 {
		t.Fatal("Show kept the mask")
	}
	h.clickButton("ulShow")
	h.frame()
	if d := h.u.unlock; d.pass.Mask != '•' || !d.pass.SingleLine || !d.pass.Submit {
		t.Fatal("Hide")
	}
	h.clickButton("ulCancel")
	if h.u.unlock != nil {
		t.Fatal("Not now")
	}
	h.u.blur = false
	h.u.cancelUnlock() // gives the keyboard back to the board (a frame takes it)
	if !h.u.blur {
		t.Fatal("Not now kept the focus")
	}
}

// gtxFor is a context reading the harness's focus, for typing().
func gtxFor(h *harness) layout.Context {
	gtx := gtxOf(800)
	gtx.Source = h.r.Source()
	return gtx
}
