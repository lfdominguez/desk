package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/term"
	"github.com/lfdominguez/desk/internal/ui/kit"
	"github.com/lfdominguez/desk/internal/vault"
)

// harness drives the window's frames through Gio's router, no GPU.
type harness struct {
	t       *testing.T
	u       *ui
	r       input.Router
	ops     op.Ops
	size    image.Point   // the window, 1700×900 unless set
	claudes []*fakeClaude // the sessions it started, in order
	origin  f32.Point     // the open drawing's canvas origin in the window (suggested)
}

// newHarness runs a window on an empty board, over a root of two
// organizations: Acme (with an api folder) and Globex.
func newHarness(t *testing.T) *harness {
	root := t.TempDir()
	for _, d := range []string{"Acme/api/v2", "Globex"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	t.Setenv("DESK_ROOT", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // not the user's folders to search
	if home, _ := os.UserHomeDir(); strings.HasPrefix(root, home+string(filepath.Separator)) {
		// a temp folder inside home (Windows' always, a CI runner's TMPDIR): root would read as ~/…
		t.Setenv(map[bool]string{true: "USERPROFILE", false: "HOME"}[onWindows], t.TempDir())
	}
	fakeBin(t, "claude", "exit 0") // a terminal's command needs one on PATH; never the machine's
	assigned = func(context.Context, orgs.Config) ([]links.Ticket, error) { return nil, nil }
	nagiosAlerts = func(context.Context, orgs.Config) ([]links.Alert, error) { return nil, nil }
	planLimits = func(context.Context, string, string) ([]claude.Limit, error) { return nil, nil }
	desktopNotify = func(string, string) {}
	pushTicket = func(context.Context, kanban.Link, string, orgs.Config) (string, error) { return "", nil }
	clipImage = func() ([]byte, string, error) { return nil, "", nil }
	optimize = func(_ context.Context, a claude.Ask, _ func(claude.Progress)) (claude.Draft, error) {
		return claude.Draft{Title: a.Request}, nil
	} // the request, as is
	explainCmd = func(context.Context, string, string, string, string) (string, error) {
		return "", errors.New("no Haiku in tests")
	}
	h := &harness{t: t, u: newUI(kanban.Default(), filepath.Join(t.TempDir(), "board.json"))}
	startClaude = func(o claude.Options, on func(claude.Event)) (session, error) { // a new card starts at once: never a real Claude
		f := &fakeClaude{opts: o, on: on}
		h.claudes = append(h.claudes, f)
		return f, nil
	}
	h.frame()
	return h
}

// boardAt is where the board starts in the window: right of the 232dp
// sidebar and below the 44dp toolbar, each with its 1dp line.
var boardAt = image.Pt(233, 45)

func (h *harness) frame() {
	h.ops.Reset()
	if h.size == (image.Point{}) {
		h.size = image.Pt(1700, 900)
	}
	gtx := layout.Context{Ops: &h.ops, Source: h.r.Source(), Now: time.Now(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(h.size)}
	h.u.frame(gtx)
	h.r.Frame(&h.ops)
}

// send queues events, then runs two frames (one to deliver, one to settle).
func (h *harness) send(evs ...any) {
	for _, e := range evs {
		h.r.Queue(e.(interface{ ImplementsEvent() }))
	}
	h.frame()
	h.frame()
}

// only is the board's first card, whatever its column (a new one starts
// working, so it moves to In progress); cards is how many there are.
func (h *harness) only() *kanban.Card {
	for c := range h.u.b.Columns {
		if len(h.u.b.Columns[c].Cards) > 0 {
			return &h.u.b.Columns[c].Cards[0]
		}
	}
	h.t.Fatal("no card on the board")
	return nil
}

func (h *harness) cards() (n int) {
	for _, c := range h.u.b.Columns {
		n += len(c.Cards)
	}
	return n
}

func press(n key.Name, m key.Modifiers) key.Event {
	return key.Event{Name: n, Modifiers: m, State: key.Press}
}

func (h *harness) titles(col int) (out []string) {
	b, err := kanban.Load(h.u.path)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, c := range b.Columns[col].Cards {
		out = append(out, c.Title)
	}
	return out
}

// create makes a card through the dialog, as the keys would: Ctrl+N, the
// title, org (if given), enter; the new card's panel opens, esc closes it.
func (h *harness) create(title, org string) {
	h.send(press("N", key.ModShortcut))
	if h.u.edit == nil || h.u.edit.id != "" {
		h.t.Fatal("Ctrl+N did not open the new card dialog")
	}
	h.send(key.EditEvent{Text: title})
	if org != "" {
		h.u.edit.org.SetText(org)
	}
	h.send(press(key.NameReturn, key.ModShortcut)) // creates: Improve is optional
	if h.u.edit != nil || h.u.panel == "" {
		h.t.Fatalf("creating %q did not close the dialog and open its session", title)
	}
	h.send(press(key.NameEscape, 0))
}

// TestNewCardDialog: a card can't be made without an organization; the
// folder picker lists the org's subfolders; a folder that isn't there is
// refused; the next dialog starts in the last org.
func TestNewCardDialog(t *testing.T) {
	h := newHarness(t)
	h.send(press("N", key.ModShortcut))
	h.send(key.EditEvent{Text: "Call the bank"})
	h.send(press(key.NameReturn, key.ModShortcut))
	if h.u.edit == nil || h.cards() != 0 {
		t.Fatal("a card without an organization was created")
	}
	h.u.edit.org.SetText("Acme")
	h.u.edit.more = true // the folder picker is among the other fields
	h.frame()
	if !slices.Equal(h.u.edit.dirs, []string{"api"}) {
		t.Fatalf("folders of Acme: %v", h.u.edit.dirs)
	}
	h.u.edit.dir.SetText("nope")
	h.send(press(key.NameReturn, key.ModShortcut))
	if h.u.edit == nil {
		t.Fatal("a missing folder was accepted")
	}
	h.u.edit.dir.SetText("api/v2")
	h.send(press(key.NameReturn, key.ModShortcut))
	c := h.u.b.Card(h.u.panel)
	if c == nil || c.Title != "Call the bank" || c.Org != "Acme" || c.Dir != "api/v2" {
		t.Fatalf("created %+v", c)
	}
	h.send(press(key.NameEscape, 0))
	h.send(press("N", key.ModShortcut))
	if h.u.edit.org.Text() != "Acme" {
		t.Fatalf("the next dialog starts in %q", h.u.edit.org.Text())
	}
}

// TestDialogScrolls: in a short window the dialog's body scrolls with the
// wheel, and its buttons stay inside the window.
func TestDialogScrolls(t *testing.T) {
	h := newHarness(t)
	h.size = image.Pt(1200, 480)
	h.u.newCard(0)
	h.frame()
	h.frame()
	foot := h.u.k.Click("shCancel")
	if foot.Hovered() {
		t.Fatal("hovered before the pointer came")
	}
	mid := f32.Pt(600, 300)
	h.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: mid},
		pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: mid, Scroll: f32.Pt(0, 200)})
	if p := h.u.edit.list.Position; p.Offset <= 0 {
		t.Fatalf("the wheel did not scroll the dialog: %+v", p)
	}
	// the Cancel button is on screen: sweep its row near the window's foot
	for y := float32(480 - 24); y > 300 && !foot.Hovered(); y -= 4 {
		h.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(740, y)}) // the 620dp dialog's Cancel, left of Create
	}
	if !foot.Hovered() {
		t.Fatal("the dialog's buttons are not inside the window")
	}
}

// seek hovers the middle of each clickable area of the last frame in area,
// bottom up, until button id is under the pointer: a few frames, where a
// sweep of the window takes thousands.
func (h *harness) seek(id string, area image.Rectangle, draw func()) (f32.Point, bool) {
	c := h.u.k.Click(id)
	var at []image.Point
	for _, n := range h.r.AppendSemantics(nil) {
		if p := n.Desc.Bounds.Min.Add(n.Desc.Bounds.Max).Div(2); n.Desc.Gestures&input.ClickGesture != 0 && p.In(area) {
			at = append(at, p)
		}
	}
	slices.SortStableFunc(at, func(a, b image.Point) int { return b.Y - a.Y })
	for _, p := range at {
		pos := f32.Pt(float32(p.X), float32(p.Y))
		h.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: pos})
		draw()
		if c.Hovered() {
			return pos, true
		}
	}
	return f32.Point{}, false
}

// clickButton finds button id among what the frame can click, or failing
// that by sweeping the pointer over the window, bottom up (where dialogs
// keep their buttons), then clicks it.
func (h *harness) clickButton(id string) {
	c := h.u.k.Click(id)
	h.frame()
	if p, ok := h.seek(id, image.Rectangle{Max: h.size}, h.frame); ok {
		h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p},
			pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
		return
	}
	for y := h.size.Y - 4; y > 0; y -= 8 {
		for x := 0; x < h.size.X; x += 16 {
			p := f32.Pt(float32(x), float32(y))
			h.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: p})
			h.frame()
			if c.Hovered() {
				h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p},
					pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
				return
			}
		}
	}
	h.t.Fatalf("button %s is not on screen", id)
}

// TestDialogButtons: Cancel closes a new card's dialog without a card (it
// used to crash the app: the footer, laid out first, closed the dialog the
// body then drew); Delete removes an existing card.
func TestDialogButtons(t *testing.T) {
	h := newHarness(t)
	h.size = image.Pt(1200, 800)
	h.u.newCard(0)
	h.frame()
	h.clickButton("shCancel")
	if h.u.edit != nil || h.cards() != 0 {
		t.Fatal("Cancel did not close the dialog cleanly")
	}
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Globex"})
	h.u.open(c.ID)
	h.frame()
	h.clickButton("shDel")
	if h.u.edit != nil || h.u.b.Card(c.ID) != nil {
		t.Fatal("Delete did not remove the card")
	}
}

// TestUnlockDialog: a locked vault asks for its password; a failed unlock
// says why, forgets what was typed and keeps asking; Later puts it off to
// the toolbar's button, which asks again; esc closes it.
func TestUnlockDialog(t *testing.T) {
	h := newHarness(t)
	h.size = image.Pt(1200, 500)
	h.u.vaultLocked(&vault.LockedError{Vault: orgs.Vault{Name: "globex", Kind: "nope"}})
	h.frame()
	d := h.u.unlock
	if d == nil {
		t.Fatal("a locked vault did not ask for its password")
	}
	h.send(key.EditEvent{Text: "hunter2"})
	h.send(press(key.NameReturn, 0))
	for end := time.Now().Add(5 * time.Second); d.busy && time.Now().Before(end); { // the unlock runs off the UI goroutine
		time.Sleep(5 * time.Millisecond)
		h.frame()
	}
	if h.u.unlock != d || !strings.Contains(d.err, "not bitwarden or passbolt") || d.pass.Text() != "" {
		t.Fatalf("failed unlock: open %v, err %q, field %q", h.u.unlock == d, d.err, d.pass.Text())
	}
	h.clickButton("ulCancel")
	if h.u.unlock != nil || len(h.u.locked) != 1 {
		t.Fatal("Later did not put the vault off")
	}
	h.clickButton("tbUnlock")
	if h.u.unlock == nil {
		t.Fatal("the toolbar's Unlock did not ask again")
	}
	h.send(press(key.NameEscape, 0))
	if h.u.unlock != nil {
		t.Fatal("esc did not close the unlock dialog")
	}
}

// TestConfigDialog: the sidebar's Settings opens an organization's
// accounts; a vault and a GitHub account whose token is in it save as a
// desk.toml that loads; a token from a vault that isn't there is refused.
func TestConfigDialog(t *testing.T) {
	h := newHarness(t)
	h.size = image.Pt(1200, 700)
	h.clickButton("sideOrgCfg")
	d := h.u.cfg
	if d == nil || d.org != "Acme" {
		t.Fatalf("Settings opened %+v", d)
	}
	h.clickButton("cfgAddvault")
	h.clickButton(fmt.Sprintf("cfg%p", d.entries[0]) + "hd") // the new vault, open, pushes the add buttons down: close it
	h.clickButton("cfgAddgithub")
	if len(d.entries) != 2 {
		t.Fatalf("%d accounts after two adds", len(d.entries))
	}
	v, gh := d.entries[0], d.entries[1]
	v.ed("name").SetText("me")
	v.ed("email").SetText("me@x.com")
	gh.ed("owners").SetText("acme-org, other")
	gh.src["."] = srcVault
	gh.ed("..vault").SetText("me")
	gh.ed("..item").SetText("GitHub token")
	h.frame()
	h.clickButton("cfgSave")
	if h.u.cfg != nil {
		t.Fatalf("Save kept the dialog: %s", d.err)
	}
	c, err := orgs.Load(h.u.root, "Acme")
	if err != nil || len(c.Vaults) != 1 || c.Vaults[0].Kind != "bitwarden" || c.Vaults[0].Email != "me@x.com" ||
		c.GitHub[0].Vault != "me" || c.GitHub[0].Item != "GitHub token" || !slices.Equal(c.GitHub[0].Owners, []string{"acme-org", "other"}) {
		t.Fatalf("saved %+v %v", c, err)
	}
	h.u.openConfig("Acme")
	h.frame()
	d = h.u.cfg
	if len(d.entries) != 2 || d.entries[1].src["."] != srcVault || d.entries[1].text("..item") != "GitHub token" {
		t.Fatal("the saved settings did not load back into the dialog")
	}
	d.entries[1].ed("..vault").SetText("nope")
	h.clickButton("cfgSave")
	if h.u.cfg == nil || !strings.Contains(d.err, "pick one of this organization's vaults") {
		t.Fatalf("a token from a missing vault: %q", d.err)
	}
	h.send(press(key.NameEscape, 0))
	if h.u.cfg == nil || !h.u.cfg.warn {
		t.Fatal("the first esc dropped unsaved changes")
	}
	h.send(press(key.NameEscape, 0))
	if h.u.cfg != nil {
		t.Fatal("esc again did not close the settings")
	}
}

// TestDialogTab: Tab walks the fields; in a short window the one focused
// scrolls into view; enter in the organization filter takes its first match
// instead of saving.
func TestDialogTab(t *testing.T) {
	h := newHarness(t)
	h.size = image.Pt(1200, 480)
	h.send(press("N", key.ModShortcut))
	s := h.u.edit
	tab := func(n int) { // what app.Window does with a Tab no one takes
		for range n {
			h.r.MoveFocus(key.FocusForward)
			h.frame()
			h.frame()
		}
	}
	for i := 0; i < 20 && s.focus != &s.org; i++ { // what to do → past the pills (buttons take tabs too) → organization
		tab(1)
	}
	if s.focus != &s.org {
		t.Fatal("a tab from what to do did not reach the organization")
	}
	h.send(key.EditEvent{Text: "gl"})
	h.send(press(key.NameReturn, 0))
	if h.u.edit == nil || s.org.Text() != "Globex" {
		t.Fatalf("enter in the filter: dialog open %v, org %q", h.u.edit != nil, s.org.Text())
	}
	h.clickButton("pillmore") // the other fields
	s.org.SetText("Acme")
	for n := 0; n < 20 && s.focus != &s.dir; n++ {
		tab(1)
	}
	h.send(key.EditEvent{Text: "ap"})
	h.send(press(key.NameReturn, 0))
	if h.u.edit == nil || s.dir.Text() != "api" {
		t.Fatalf("enter in the folder filter: dialog open %v, folder %q", h.u.edit != nil, s.dir.Text())
	}
	for n := 0; n < 20 && s.focus != &s.due; n++ { // on past the folder, its chips, links, labels
		tab(1)
	}
	if s.focus != &s.due {
		t.Fatal("tabs did not reach the due date")
	}
	if p := s.list.Position; p.First == 0 && p.Offset == 0 {
		t.Fatalf("the due date's section was not scrolled into view: %+v", p)
	}
	s.notes.SetText("Call the bank")
	s.dir.SetText("") // no filter: enter saves, it doesn't pick the first folder
	h.r.Source().Execute(key.FocusCmd{Tag: &s.dir})
	h.frame()
	h.send(press(key.NameReturn, 0))
	if h.u.edit != nil || s.dir.Text() != "" {
		t.Fatalf("enter in an empty folder field: dialog open %v, folder %q", h.u.edit != nil, s.dir.Text())
	}
}

// TestKeyboardFlow: cards made with the dialog, arrows select, Ctrl+→
// moves the card, enter opens its session, E its sheet, esc closes each.
func TestKeyboardFlow(t *testing.T) {
	h := newHarness(t)
	h.create("Call the bank", "Globex")
	h.create("Pay rent", "")                                       // starts in Globex, the last org
	if got := h.titles(2); len(got) != 2 || got[1] != "Pay rent" { // In progress: a new card starts working
		t.Fatalf("created %v", got)
	}
	h.send(press(key.NameDownArrow, 0)) // selects the first card
	h.send(press(key.NameDownArrow, 0))
	h.send(press(key.NameRightArrow, key.ModShortcut))
	if got := h.titles(3); len(got) != 1 || got[0] != "Pay rent" {
		t.Fatalf("Ctrl+→ moved %v", got)
	}
	h.send(press(key.NameReturn, 0))
	if c := h.u.b.Card(h.u.panel); c == nil || c.Title != "Pay rent" {
		t.Fatal("enter did not open the selected card's session")
	}
	h.send(press(key.NameEscape, 0))
	if h.u.panel != "" {
		t.Fatal("esc did not close the panel")
	}
	h.send(press("E", 0))
	if h.u.edit == nil || h.u.edit.title.Text() != "Pay rent" {
		t.Fatal("E did not open the selected card's sheet")
	}
	h.send(press(key.NameEscape, 0))
	if h.u.edit != nil {
		t.Fatal("esc did not close the sheet")
	}
}

// fakeClaude records prompts; its on func plays Claude's side.
type fakeClaude struct {
	prompts []string
	replies []string // Respond's: "id allow input" or "id deny msg"
	opts    claude.Options
	on      func(claude.Event)
}

func (f *fakeClaude) Send(p string) error { f.prompts = append(f.prompts, p); return nil }
func (f *fakeClaude) Respond(id string, allow bool, input json.RawMessage, msg string) error {
	if allow {
		f.replies = append(f.replies, id+" allow "+string(input))
	} else {
		f.replies = append(f.replies, id+" deny "+msg)
	}
	return nil
}
func (f *fakeClaude) Interrupt() error { return nil }
func (f *fakeClaude) Close()           {}

// TestInterview: Claude's AskUserQuestion shows in the card's full-window
// session; picking an option and Send answers it, and a tool asking
// permission can be denied.
func TestInterview(t *testing.T) {
	h := newHarness(t)
	fake := &fakeClaude{}
	startClaude = func(o claude.Options, on func(claude.Event)) (session, error) {
		fake.opts, fake.on = o, on
		return fake, nil
	}
	c := h.u.b.Add(0, kanban.Card{Title: "Plan it", Org: "Acme"})
	h.u.send(c.ID, "start")
	ev := claude.Parse([]byte(`{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion",
		"input":{"questions":[{"question":"Which DB?","header":"DB","multiSelect":false,"options":[{"label":"Postgres"},{"label":"SQLite"}]}]}}}`))
	fake.on(ev[0])
	h.frame()
	if h.u.b.Card(c.ID).Note != "Claude asks: Which DB?" {
		t.Fatalf("note %q", h.u.b.Card(c.ID).Note)
	}
	h.clickButton("sidesess:" + c.ID)
	if h.u.view != "sess:"+c.ID {
		t.Fatalf("view %q", h.u.view)
	}
	h.r.Source().Execute(key.FocusCmd{Tag: &h.u.asks[c.ID].other[0]}) // typing an answer: E and T are letters, not the card's keys
	h.frame()
	h.send(key.EditEvent{Text: "et"}, press("E", 0), press("T", 0))
	if h.u.edit != nil || h.u.terms[c.ID] != nil || h.u.asks[c.ID].other[0].Text() != "et" {
		t.Fatalf("typing in an answer: sheet %v, terminal %v, text %q", h.u.edit != nil, h.u.terms[c.ID] != nil, h.u.asks[c.ID].other[0].Text())
	}
	h.u.asks[c.ID].other[0].SetText("")
	h.clickButton("ask" + c.ID + "-0-1")
	h.clickButton("askSend" + c.ID)
	if len(fake.replies) != 1 || !strings.Contains(fake.replies[0], `"answers":{"Which DB?":"SQLite"}`) || h.u.asks[c.ID] != nil || h.u.b.Card(c.ID).Status != kanban.StatusWorking {
		t.Fatalf("replies %q, ask %v, status %q", fake.replies, h.u.asks[c.ID], h.u.b.Card(c.ID).Status)
	}
	fake.on(claude.Parse([]byte(`{"type":"control_request","request_id":"r2","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"rm -rf build"}}}`))[0])
	h.frame()
	h.clickButton("askDeny" + c.ID)
	if len(fake.replies) != 2 || fake.replies[1] != "r2 deny The user denied it." {
		t.Fatalf("replies %q", fake.replies)
	}
}

// TestTicketFirst: a card made to create its ticket asks Claude for it
// before any work; DESK-TICKET creates and links it and tells Claude to
// start; a second one is refused (one ticket a card); DESK-CARD makes a
// new card that creates its own.
func TestTicketFirst(t *testing.T) {
	h := newHarness(t)
	fake := &fakeClaude{}
	startClaude = func(o claude.Options, on func(claude.Event)) (session, error) {
		fake.opts, fake.on = o, on
		return fake, nil
	}
	var asked []string
	createTicket = func(_ context.Context, _ orgs.Config, title, body string) (kanban.Link, error) {
		asked = append(asked, title+"|"+body)
		return kanban.Link{URL: "https://jira.example.com/browse/ABC-9", Kind: links.KindJira, Ref: "ABC-9"}, nil
	}
	defer func() { createTicket = links.Create }()
	c := h.u.b.Add(h.u.b.Column("To do"), kanban.Card{Title: "Fix login", Org: "Acme", Ticket: true, Lang: "Spanish"})
	h.u.send(c.ID, startWork)
	if !strings.Contains(fake.prompts[0], "DESK-TICKET") || !strings.Contains(fake.opts.System, "DESK-CARD") || !strings.Contains(fake.opts.System, claude.Lang("Spanish", "English")) {
		t.Fatalf("first prompt %q, system %q", fake.prompts[0], fake.opts.System)
	}
	turn := func(text string) {
		fake.on(claude.Event{Kind: claude.KindText, Text: text})
		fake.on(claude.Event{Kind: claude.KindResult})
		for end := time.Now().Add(2 * time.Second); len(fake.prompts) < 2 && time.Now().Before(end); {
			h.frame()
		}
	}
	turn("Login fails on retry.\nDESK-TICKET: Fix login retry")
	got := h.u.b.Card(c.ID)
	if len(asked) != 1 || asked[0] != "Fix login retry|Login fails on retry." || got.Ticket || ticketOf(got) == nil {
		t.Fatalf("asked %q, card %+v", asked, got)
	}
	if len(fake.prompts) != 2 || !strings.Contains(fake.prompts[1], "ABC-9 created") || !strings.Contains(fake.prompts[1], "start working") {
		t.Fatalf("prompts %q", fake.prompts)
	}
	fake.prompts = fake.prompts[:1]
	turn("Another one.\nDESK-TICKET: Other\nDESK-CARD: Add audit logging to login")
	if len(asked) != 1 || len(fake.prompts) != 2 || !strings.Contains(fake.prompts[1], "already tracks ABC-9") {
		t.Fatalf("a second ticket: asked %q, prompts %q", asked, fake.prompts)
	}
	todo := h.u.b.Columns[h.u.b.Column("To do")].Cards
	if len(todo) != 1 || todo[0].Title != "Add audit logging to login" || !todo[0].Ticket || todo[0].Org != "Acme" || todo[0].Lang != "Spanish" {
		t.Fatalf("split card %+v", todo)
	}
}

// TestInvestigateCard: investigating an alert makes it a card in progress
// whose Claude starts on it; the same alert again shows that card.
func TestInvestigateCard(t *testing.T) {
	h := newHarness(t)
	fake := &fakeClaude{}
	startClaude = func(o claude.Options, on func(claude.Event)) (session, error) {
		fake.opts, fake.on = o, on
		return fake, nil
	}
	since := time.Now().Add(-time.Hour)
	h.u.investigate("Acme", "web1", "HTTP", "critical", "HTTP CRITICAL: 503", "https://nagios.example.com/x", since)
	cs := h.u.b.Columns[h.u.b.Column("In progress")].Cards
	if len(cs) != 1 || cs[0].Title != "Investigate CRITICAL web1 / HTTP" || cs[0].Org != "Acme" || !strings.Contains(cs[0].Notes, "HTTP CRITICAL: 503") {
		t.Fatalf("cards %+v", cs)
	}
	if h.u.view != "sess:"+cs[0].ID || len(fake.prompts) != 1 || !strings.Contains(fake.prompts[0], "Investigate this alert") {
		t.Fatalf("view %q, prompts %q", h.u.view, fake.prompts)
	}
	h.u.view = ""
	h.u.investigate("Acme", "web1", "HTTP", "critical", "HTTP CRITICAL: 503", "", since)
	if n := len(h.u.b.Columns[h.u.b.Column("In progress")].Cards); n != 1 || h.u.view != "sess:"+cs[0].ID || len(fake.prompts) != 1 {
		t.Fatalf("again: %d cards, view %q, prompts %d", n, h.u.view, len(fake.prompts))
	}

	// left as it is: its Claude says so, desk offers the snooze; snoozed, the
	// alert stops counting until then
	if !strings.Contains(fake.opts.System, "DESK-SNOOZE") {
		t.Fatal("no snooze directive in the system prompt")
	}
	fake.on(claude.Event{Kind: claude.KindText, Text: "Stopped by hand, nothing to do.\nDESK-SNOOZE: stopped on purpose"})
	fake.on(claude.Event{Kind: claude.KindResult})
	h.frame()
	if h.u.snoozeAsk[cs[0].ID] != "stopped on purpose" {
		t.Fatalf("snooze offer %q", h.u.snoozeAsk)
	}
	a := links.Alert{Host: "web1", Service: "HTTP", State: "critical"}
	h.u.alerts["Acme"] = []links.Alert{a}
	if h.u.alertCount("Acme") != 1 {
		t.Fatal("alert not counted")
	}
	h.u.snooze(cs[0].Alert, 2*time.Hour)
	if _, sn := h.u.snoozedUntil("Acme", a); !sn || h.u.alertCount("Acme") != 0 || !strings.Contains(h.u.alertsSentence("Acme"), "1 snoozed") {
		t.Fatalf("snoozed: %v count %d", h.u.snoozed, h.u.alertCount("Acme"))
	}
	h.u.loadSnoozed() // kept next to the board
	if _, sn := h.u.snoozedUntil("Acme", a); !sn {
		t.Fatal("snooze not saved")
	}
	for in, want := range map[string]time.Duration{"3d": 72 * time.Hour, "1w": 168 * time.Hour, "2h30m": 150 * time.Minute, "1.5d": 36 * time.Hour} {
		if d, err := parseSnooze(in); d != want || err != nil {
			t.Errorf("%s: %v %v", in, d, err)
		}
	}
	if _, err := parseSnooze("soon"); err == nil {
		t.Error("parsed soon")
	}
}

// TestCardTools: a card's Claude reads its links' CI through desk, the CI
// view gathers the org's, and the session gets desk's tools allowed.
func TestCardTools(t *testing.T) {
	h := newHarness(t)
	ciRuns = func(_ context.Context, l kanban.Link, _ orgs.Config) ([]links.Run, error) {
		return []links.Run{{Name: "test", Status: links.CIFailed, Jobs: []links.Job{{ID: 52, Name: "unit", Status: links.CIFailed}}}}, nil
	}
	defer func() { ciRuns = links.CI }()
	pr := kanban.Link{URL: "https://github.com/acme/api/pull/9", Kind: links.KindGitHubPR, Ref: "PR #9"}
	c := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Fix login", Org: "Acme", Links: []kanban.Link{pr}})
	wait := func(f func()) {
		done := make(chan struct{})
		go func() { f(); close(done) }()
		for {
			select {
			case <-done:
				return
			default:
				h.frame()
			}
		}
	}
	var text string
	var ok bool
	wait(func() { text, ok = h.u.cardTool(c.ID, "ci", nil) })
	if !ok || !strings.Contains(text, "PR #9") || !strings.Contains(text, "job 52 unit: failed") {
		t.Fatalf("ci tool %v: %s", ok, text)
	}
	wait(func() { text, ok = h.u.cardTool("nope", "ci", nil) })
	if ok {
		t.Error("a card that's gone must fail")
	}
	wait(h.u.checkCI)
	h.frame()
	if items := h.u.ci["Acme"]; len(items) != 1 || ciState(items[0]) != links.CIFailed || h.u.ciCount("Acme") != 1 {
		t.Fatalf("ci view %+v", items)
	}
	h.u.cardSock = "/run/desk/cards.sock"
	args, _ := h.u.agentArgs(h.u.b.Card(c.ID))
	if j := strings.Join(args, " "); !strings.Contains(j, "--mcp-config") || !strings.Contains(j, "card="+c.ID) || !strings.Contains(j, "mcp__desk__ci_log") {
		t.Errorf("args %q", args)
	}
}

// TestCardTell: the infra Claude's events go into the card's session (held
// while a permission question is up, until the turn ends), and its
// follow-up work becomes a card in To do.
func TestCardTell(t *testing.T) {
	h := newHarness(t)
	fake := &fakeClaude{}
	startClaude = func(o claude.Options, on func(claude.Event)) (session, error) {
		fake.opts, fake.on = o, on
		return fake, nil
	}
	c := h.u.b.Add(h.u.b.Column("To do"), kanban.Card{Title: "Fix login", Org: "Acme"})
	if err := h.u.cardTell("DESK-99", "x"); err == nil {
		t.Error("an unknown card must fail")
	}
	h.u.cardTell(c.Key(), "question 4 from Acme's Claude:\nwhich table?")
	if len(fake.prompts) != 1 || !strings.Contains(fake.prompts[0], "which table?") || !strings.Contains(fake.prompts[0], "Fix login") {
		t.Fatalf("prompts %q", fake.prompts)
	}
	if ms := h.u.chat(c.ID); len(ms) != 1 || ms[0].Who != "peer" {
		t.Fatalf("chat %+v", ms)
	}
	h.u.asks[c.ID] = &askState{}
	h.u.cardTell(c.Key(), "result of task 2 (Acme):\ndone")
	if len(fake.prompts) != 1 {
		t.Fatal("a permission question is up: it waits")
	}
	fake.on(claude.Event{Kind: claude.KindResult})
	for end := time.Now().Add(2 * time.Second); len(fake.prompts) < 2 && time.Now().Before(end); {
		h.frame()
	}
	if len(fake.prompts) != 2 || !strings.Contains(fake.prompts[1], "result of task 2") || h.u.peerOut[c.ID] != "" {
		t.Fatalf("at the turn's end: %q", fake.prompts)
	}
	key, err := h.u.cardNew("Acme", "Rotate app logs", "/var/log/app fills the disk")
	todo := h.u.b.Columns[h.u.b.Column("To do")].Cards
	if err != nil || len(todo) != 1 || todo[0].Key() != key || todo[0].Org != "Acme" || !strings.Contains(todo[0].Notes, "fills the disk") {
		t.Fatalf("new card %q %v: %+v", key, err, todo)
	}
}

// TestSessionTurn: a message from the panel starts the card's session with
// the card as context; Claude's reply, DESK- lines and result land on the
// card, which moves to Blocked with the PR attached.
func TestSessionTurn(t *testing.T) {
	h := newHarness(t)
	fake := &fakeClaude{}
	startClaude = func(o claude.Options, on func(claude.Event)) (session, error) {
		fake.opts, fake.on = o, on
		return fake, nil
	}
	c := h.u.b.Add(h.u.b.Column("To do"), kanban.Card{Title: "Fix N+1 query", Notes: "in PartnerListHandler", Org: "Globex"})
	h.u.openPanel(c.ID)
	h.frame()
	h.send(key.EditEvent{Text: "please look into it"})
	h.send(press(key.NameReturn, 0))
	if len(fake.prompts) != 1 || !strings.Contains(fake.prompts[0], "DESK-1: Fix N+1 query") || !strings.HasSuffix(fake.prompts[0], "please look into it") {
		t.Fatalf("prompts %q", fake.prompts)
	}
	if fake.opts.Org != "Globex" || !strings.Contains(fake.opts.System, "DESK-1") {
		t.Fatalf("options %+v", fake.opts)
	}
	card := func() *kanban.Card { return h.u.b.Card(c.ID) }
	if card().Status != kanban.StatusWorking || h.u.b.Columns[h.u.b.Column("In progress")].Cards[0].ID != c.ID {
		t.Fatalf("after send: %+v", card())
	}
	fake.on(claude.Event{Kind: claude.KindInit, Session: "s-1"})
	fake.on(claude.Event{Kind: claude.KindTool, Text: "Bash  go test ./..."})
	fake.on(claude.Event{Kind: claude.KindText, Text: "Fixed and opened a PR.\nDESK-LINK: https://example.com/pr/9\nDESK-STATUS: blocked — waiting for PR review"})
	fake.on(claude.Event{Kind: claude.KindResult, Tokens: 1500, Cost: 0.42})
	h.frame()
	got := card()
	if got.Session.ID != "s-1" || got.Session.Turns != 1 || got.Session.Tokens != 1500 || got.Session.Cost != 0.42 {
		t.Fatalf("session %+v", got.Session)
	}
	if got.Status != kanban.StatusBlocked || got.Note != "waiting for PR review" || got.Last != "Fixed and opened a PR." || len(got.Links) != 1 {
		t.Fatalf("card %+v", got)
	}
	if col, _ := h.u.b.Find(c.ID); col != h.u.b.Column("Blocked") {
		t.Fatal("blocked card did not move to Blocked")
	}
	if m := kanban.LoadChat(kanban.ChatPath(h.u.path, c.ID)); len(m) != 3 || m[2].Text != "Fixed and opened a PR." {
		t.Fatalf("chat %+v", m)
	}
	if h.u.sentence() != "1 blocked" {
		t.Fatalf("sentence %q", h.u.sentence())
	}
}

// TestStateChangeRedraws: anything that changes state during a frame (here
// the dialog's enter, handled mid-layout after the board was drawn) asks
// for the next frame, and so does work queued by other goroutines.
func TestStateChangeRedraws(t *testing.T) {
	h := newHarness(t)
	now := func() bool { // a redraw asked for right away (a caret blink asks for one later)
		at, ok := h.r.WakeupTime()
		return ok && !at.After(time.Now())
	}
	h.u.newCard(0)
	h.u.edit.org.SetText("Globex")
	h.u.edit.optimized = true // past Prompt Optimization: enter creates
	h.frame()
	now() // the router keeps the earliest unread wakeup: read this frame's
	h.frame()
	if now() {
		t.Fatal("an idle frame asked for another")
	}
	h.r.Queue(key.EditEvent{Text: "x"})
	h.frame()
	now()
	h.r.Queue(press(key.NameReturn, 0))
	h.frame()
	if !now() || h.cards() != 1 {
		t.Fatal("adding a card mid-frame did not ask for a redraw")
	}
	h.frame()
	h.u.events <- func() {}
	h.frame() // drains it; pretend more came in during the frame:
	h.u.events <- func() {}
	h.ops.Reset()
	h.u.frame(layout.Context{Ops: &h.ops, Source: h.r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1700, 900))})
	h.r.Frame(&h.ops)
	if !now() {
		t.Fatal("queued work did not ask for a redraw")
	}
	h.frame()
	now()
	h.u.events <- func() { h.u.gen++ } // work that changed the board: drawn by the frame that ran it
	h.frame()
	if now() {
		t.Fatal("work run before layout asked for a second frame")
	}
}

// TestSaveBurst: with saveAfter, a burst of changes is one write, made
// after the wait; gen moves at once.
func TestSaveBurst(t *testing.T) {
	h := newHarness(t)
	h.u.saveAfter = 30 * time.Millisecond
	h.u.b.Columns[0].Cards = append(h.u.b.Columns[0].Cards, kanban.Card{ID: "a", Title: "one"})
	g := h.u.gen
	h.u.save()
	h.u.save()
	if h.u.gen != g+2 || !h.u.dirty {
		t.Fatalf("gen %d dirty %v", h.u.gen-g, h.u.dirty)
	}
	if b, err := kanban.Load(h.u.path); err == nil && b != nil && len(b.Columns[0].Cards) != 0 {
		t.Fatal("written before the wait")
	}
	h.pumpUntil("the write", func() bool { return !h.u.dirty })
	if b, err := kanban.Load(h.u.path); err != nil || len(b.Columns[0].Cards) != 1 {
		t.Fatalf("not written: %v", err)
	}
}

// TestBoardCaches: the visible cards and the sidebar's counts follow the
// board, the search and the view; cards scrolled out keep their places
// (only their layout is skipped).
func TestBoardCaches(t *testing.T) {
	h := newHarness(t)
	for i := range 30 {
		h.u.b.Add(0, kanban.Card{Title: fmt.Sprint("card ", i), Org: "Acme", Status: kanban.StatusBlocked})
	}
	h.u.save()
	h.frame()
	if n := len(h.u.visible(0)); n != 30 {
		t.Fatalf("visible %d", n)
	}
	if n := h.u.count("st:blocked"); n != 30 {
		t.Fatalf("count %d", n)
	}
	h.u.search.SetText("'card 1")
	want := 0
	for _, c := range h.u.b.Columns[0].Cards {
		if c.Matches("'card 1") {
			want++
		}
	}
	if n := len(h.u.visible(0)); n != want || n == 30 || n == 0 {
		t.Fatalf("searched %d, want %d", n, want)
	}
	h.u.search.SetText("")
	h.u.view = "st:working"
	if n := len(h.u.visible(0)); n != 0 {
		t.Fatalf("other view %d", n)
	}
	h.u.view = ""
	h.u.b.Columns[0].Cards[0].Status = kanban.StatusWorking
	h.u.save()
	if a, b := h.u.count("st:blocked"), h.u.count("st:working"); a != 29 || b != 1 {
		t.Fatalf("after a change: %d %d", a, b)
	}
	h.u.b.Add(h.u.b.Column("Done"), kanban.Card{Title: "old", Status: kanban.StatusBlocked})
	h.u.save()
	if n := h.u.count("st:blocked"); n != 29 {
		t.Fatalf("a Done card counted: %d", n)
	}
	h.frame()
	first := slices.Clone(h.u.lay.cards)
	h.frame() // the ones below the column's foot now skipped
	if !slices.Equal(first, h.u.lay.cards) || len(first) != 30 {
		t.Fatalf("places moved: %d → %d", len(first), len(h.u.lay.cards))
	}
	h.u.scroll[0] = h.u.maxScroll[0]
	h.frame()
	last := h.u.lay.cards[len(h.u.lay.cards)-1]
	if last.r.Max.Y > h.u.lay.cols[0].Max.Y || last.r.Min.Y < h.u.lay.cols[0].Min.Y {
		t.Fatalf("the last card out of the column at the bottom: %v in %v", last.r, h.u.lay.cols[0])
	}
}

// TestQuitOn: a signal writes the change still waiting, then exits; with
// no frame to run that, it exits anyway after quitWait.
func TestQuitOn(t *testing.T) {
	h := newHarness(t)
	h.u.saveAfter = time.Hour
	h.u.b.Columns[0].Cards = append(h.u.b.Columns[0].Cards, kanban.Card{ID: "a", Title: "one"})
	h.u.save()
	c, code := make(chan os.Signal, 1), make(chan int, 1)
	go h.u.quitOn(c, func(n int) { code <- n })
	c <- os.Interrupt
	h.pumpUntil("the exit", func() bool { return len(code) == 1 })
	if b, err := kanban.Load(h.u.path); err != nil || len(b.Columns[0].Cards) != 1 || <-code != 1 || h.u.dirty {
		t.Fatalf("not written before the exit: %v", err)
	}
	quitWait = 10 * time.Millisecond
	defer func() { quitWait = 2 * time.Second }()
	go h.u.quitOn(c, func(n int) { code <- n })
	c <- os.Interrupt
	select { // no frames: the UI goroutine never answers
	case <-code:
	case <-time.After(time.Second):
		t.Fatal("waited forever for the UI goroutine")
	}
	h.frame()        // the queued write runs; nothing waits on it now
	h.u.windowed()() // the window's waits; its signal handler let go at once
	if h.u.saveAfter != 500*time.Millisecond || h.u.scanWait != 300*time.Millisecond {
		t.Fatalf("waits %v %v", h.u.saveAfter, h.u.scanWait)
	}
}

func TestViews(t *testing.T) {
	now := time.Date(2026, 10, 27, 9, 0, 0, 0, time.Local)
	c := kanban.Card{Org: "Globex", Labels: []string{"dev"}, Status: kanban.StatusBlocked, Due: "2026-10-26",
		Links: []kanban.Link{{Kind: links.KindGitHubPR, Tone: links.ToneGood}, {Kind: links.KindJira, Tone: links.ToneWait}}}
	for v, want := range map[string]bool{"": true, "st:blocked": true, "st:working": false, "org:Globex": true, "org:Initech": false,
		"lab:dev": true, "ln:ready": true, "ln:review": false, "ln:tickets": true, "due:over": true, "due:week": false} {
		if inView(v, c, now) != want {
			t.Errorf("%q: want %v", v, want)
		}
	}
}

// TestTicketsView picks an org's Tickets in the sidebar; a ticket without a
// card opens the new-card dialog for it.
func TestTicketsView(t *testing.T) {
	h := newHarness(t)
	h.u.tickets["Globex"] = []links.Ticket{{Project: "ABC", Key: "ABC-7", Title: "Fix login", Status: "In Progress", URL: "https://globex.atlassian.net/browse/ABC-7"}}
	h.frame()
	h.clickButton("sidetk:Globex")
	if h.u.view != "tk:Globex" || h.u.sentence() != "1 ticket in 1 project" {
		t.Fatalf("view %q, %q", h.u.view, h.u.sentence())
	}
	h.clickButton("tkhttps://globex.atlassian.net/browse/ABC-7")
	if s := h.u.edit; s == nil || s.title.Text() != "ABC-7 Fix login" || s.org.Text() != "Globex" || s.links.Text() != "https://globex.atlassian.net/browse/ABC-7" {
		t.Fatal("no new-card dialog for the ticket")
	}
}

// TestAlertsView: an org with a Nagios gets an Alerts view under it, its
// count the unacknowledged ones, red with an error; orgs without none.
func TestAlertsView(t *testing.T) {
	h := newHarness(t)
	writeAt(filepath.Join(h.u.root, "Globex", ".desk", "desk.toml"), []byte("[[nagios]]\nname = \"prod\"\nurl = \"https://mon.globex.com/nagios\"\n"), 0o644)
	nagiosAlerts = func(_ context.Context, cfg orgs.Config) ([]links.Alert, error) {
		if len(cfg.Nagios) != 1 {
			t.Errorf("asked with %+v", cfg.Nagios)
		}
		return []links.Alert{
			{Monitor: "prod", Host: "web1", Service: "HTTP", State: "critical", Output: "Connection refused", URL: "https://mon.globex.com/a"},
			{Monitor: "prod", Host: "web1", Service: "Disk /", State: "warning", Output: "12% free", URL: "https://mon.globex.com/b"},
			{Monitor: "prod", Host: "db1", State: "down", Acked: true, URL: "https://mon.globex.com/c"},
		}, nil
	}
	h.u.checkAlerts()
	h.frame()
	if _, ok := h.u.alerts["Acme"]; ok {
		t.Fatal("Acme has no Nagios, but an Alerts view")
	}
	if n, c := h.u.count("al:Globex"), h.u.alertColor("Globex"); n != 2 || c != kit.CRed {
		t.Fatalf("count %d, color %v", n, c)
	}
	h.clickButton("sideal:Globex")
	if h.u.view != "al:Globex" || h.u.sentence() != "1 error · 1 warning · 1 acknowledged" || h.u.viewTitle() != "Globex · alerts" {
		t.Fatalf("view %q, %q", h.u.view, h.u.sentence())
	}
	h.frame() // draws the rows
}

// localServer gives org under root a servers file with one host, "local"
// (a local shell), in group web.
func localServer(t *testing.T, root, org string) {
	t.Helper()
	os.MkdirAll(filepath.Join(root, org, ".desk"), 0o755)
	if err := os.WriteFile(filepath.Join(root, org, ".desk", "ssh_config"), []byte("#@ group web\nHost local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestServers: an org with a servers file gets Servers under it, and a
// host opens its pane in the workspace; the sidebar lists only connected
// ones, a double click on
// Servers picks among all; other views still open from there.
func TestServers(t *testing.T) {
	for _, v := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "XDG_CACHE_HOME"} {
		t.Setenv(v, t.TempDir()) // never the user's infra session
	}
	h := newHarness(t)
	localServer(t, h.u.root, "Acme")
	h.u.infra = infra.New(h.u.root, func() {})
	defer h.u.infra.Close()
	h.frame()
	h.clickButton("sidesrv:Acme")
	if h.u.view != "srv:Acme" || h.u.viewTitle() != "Acme · servers" || h.u.pick != "" {
		t.Fatalf("view %q, picker %q", h.u.view, h.u.pick)
	}
	hosts := func() bool {
		return slices.ContainsFunc(h.u.serverItems("Acme"), func(it sideItem) bool { return strings.HasPrefix(it.view, "host:") })
	}
	if hosts() {
		t.Fatal("the sidebar lists a server nobody connected to")
	}
	if h.u.pick != "" {
		t.Fatal("one click on Servers opened the picker")
	}
	t0 := time.Now() // the button sweep is too slow for a real double click: its timing, then
	h.u.serversClicked("Acme", t0.Add(time.Second))
	h.u.serversClicked("Acme", t0.Add(time.Second+200*time.Millisecond))
	if h.u.pick != "Acme" {
		t.Fatal("a double click on Servers didn't open the picker")
	}
	h.frame()
	h.clickButton("pickweb/local")
	if h.u.pick != "" || h.u.view != "srv:Acme" {
		t.Fatalf("picking a server: picker %q, view %q", h.u.pick, h.u.view)
	}
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end) && h.u.count("srv:Acme") == 0; time.Sleep(50 * time.Millisecond) {
		h.frame()
	}
	if h.u.count("srv:Acme") != 1 || !strings.Contains(h.u.sentence(), "1 server") {
		t.Fatalf("the host didn't open: %d, %q", h.u.count("srv:Acme"), h.u.sentence())
	}
	h.frame()
	h.clickButton("side") // another view, with a pane open
	if h.u.view != "" {
		t.Fatalf("All cards didn't open from the servers: view %q", h.u.view)
	}
	if !hosts() {
		t.Fatal("the connected server isn't in the sidebar")
	}
	h.clickButton("sidehost:Acme/web/local") // back to it from the board
	if h.u.view != "srv:Acme" {
		t.Fatalf("the connected host's row went to %q", h.u.view)
	}
	// a card whose Claude handed server work over: its header shows the task, a click the infra session
	c := h.u.b.Add(1, kanban.Card{Title: "Run migration", Org: "Acme"})
	h.u.view = ""
	h.u.openPanel(c.ID)
	infraTasks = func(*infra.Workspace) []infra.Task {
		return []infra.Task{{ID: 7, Org: "Acme", From: c.Key(), Text: "Run 0042 on db1"}}
	}
	defer func() { infraTasks = (*infra.Workspace).Tasks }()
	h.frame()
	h.clickButton("pnTask" + c.ID)
	if h.u.view != "srv:Acme" || h.u.panel != "" {
		t.Fatalf("the task chip went to %q, panel %q", h.u.view, h.u.panel)
	}
}

// TestPromptOptimization: the new card's request goes to the optimizer in
// its org, and the draft fills the dialog, keeping what the user set.
func TestPromptOptimization(t *testing.T) {
	h := newHarness(t)
	var got claude.Ask
	optimize = func(_ context.Context, a claude.Ask, _ func(claude.Progress)) (claude.Draft, error) {
		got = a
		return claude.Draft{Title: "Fix API login", Prompt: "Goal: fix login.\nSteps: …", Labels: []string{"Auth", "dev"}, Priority: "high",
			Folder: "api", Links: []string{"https://github.com/acme/api/pull/3", "not a link"}, Due: "2026-11-02"}, nil
	}
	describe = func(_ context.Context, l kanban.Link, _ orgs.Config) (string, error) {
		return l.Ref + ": Login fails", nil
	}
	h.u.newCard(0)
	h.frame()
	s := h.u.edit
	s.org.SetText("Acme")
	s.notes.SetText("login is broken in the api, PR https://github.com/acme/api/pull/3")
	s.labels.SetText("dev")
	h.clickButton("shOpt") // Improve with Claude: suggestions, not a card yet
	for end := time.Now().Add(2 * time.Second); s.optStop != nil && time.Now().Before(end); {
		h.frame()
	}
	if h.u.edit == nil || !s.optimized || h.cards() != 0 {
		t.Fatalf("Create made the card before optimizing: edit %v", h.u.edit)
	}
	if got.Org != "Acme" || !strings.HasSuffix(got.Dir, "Acme") || !slices.Contains(got.Folders, "api") || !strings.Contains(got.Request, "login is broken") ||
		!slices.Equal(got.Linked, []string{"PR #3: Login fails"}) {
		t.Fatalf("asked %+v", got)
	}
	if s.title.Text() != "Fix API login" || s.notes.Text() != "Goal: fix login.\nSteps: …" || s.labels.Text() != "dev, auth" || s.dir.Text() != "api" ||
		prioNames[s.prio] != "High" || s.links.Text() != "https://github.com/acme/api/pull/3" || s.due.Text() != "2026-11-02" {
		t.Fatalf("dialog: %q %q %q %q %d %q %q", s.title.Text(), s.notes.Text(), s.labels.Text(), s.dir.Text(), s.prio, s.links.Text(), s.due.Text())
	}
	if h.frame(); !s.preview {
		t.Fatal("the optimized notes aren't shown as Markdown")
	}
	h.clickButton("notesView") // Edit: the source
	if s.preview {
		t.Fatal("Edit kept the Markdown view")
	}
	h.clickButton("shSave") // now it creates what was looked over
	if h.u.edit != nil || h.cards() != 1 || h.only().Title != "Fix API login" {
		t.Fatalf("second Create: edit %v cards %d", h.u.edit, h.cards())
	}
}

// TestShortcutsQuiet: no shortcut fires in a card's session view or while
// typing in an editor; esc still backs out.
func TestShortcutsQuiet(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	keys := []any{press("N", key.ModShortcut), press("F", key.ModShortcut), press("E", 0), press("T", 0), press(key.NameReturn, 0), press(key.NameRightArrow, 0), press("/", 0)}
	h.u.openSession(c.ID)
	h.frame()
	h.r.Source().Execute(key.FocusCmd{}) // the view itself, nobody typing
	h.frame()
	h.send(keys...)
	if h.u.edit != nil || len(h.u.terms) != 0 || h.u.panel != "" || h.u.view != "sess:"+c.ID {
		t.Fatalf("a shortcut fired in the session view: sheet %v, terms %d, panel %q, view %q", h.u.edit != nil, len(h.u.terms), h.u.panel, h.u.view)
	}
	h.send(press(key.NameEscape, 0))
	if h.u.view != "" {
		t.Fatalf("esc left the view at %q", h.u.view)
	}
	h.u.openPanel(c.ID) // the board's panel, its message box focused
	h.frame()
	h.send(append(keys, key.EditEvent{Text: "hola"})...)
	if h.u.edit != nil || len(h.u.terms) != 0 || h.r.Source().Focused(&h.u.search) || h.u.chatIn.Text() != "hola" {
		t.Fatalf("a shortcut fired while typing: sheet %v, terms %d, search focused %v, chat %q", h.u.edit != nil, len(h.u.terms), h.r.Source().Focused(&h.u.search), h.u.chatIn.Text())
	}
}

// TestReloadConfig: an org made after start shows up on Reload.
func TestReloadConfig(t *testing.T) {
	h := newHarness(t)
	writeAt(filepath.Join(h.u.root, "Acme/api/.desk/desk.toml"), []byte(`org = "Api"`), 0o600)
	h.clickButton("sideOrgReload")
	if !slices.Contains(h.u.orgList, "Api") || orgs.Dir(h.u.root, "Api", "") != filepath.Join(h.u.root, "Acme", "api") {
		t.Fatalf("orgs after reload: %v", h.u.orgList)
	}
}

// TestDoneHidden: no view has the Done column; its cards are in the Done
// archive.
func TestDoneHidden(t *testing.T) {
	h := newHarness(t)
	d := h.u.b.Column("Done")
	h.u.b.Add(d, kanban.Card{Title: "shipped", Org: "Acme"})
	h.u.b.Add(0, kanban.Card{Title: "todo", Org: "Acme"})
	h.frame()
	if len(h.u.lay.cards) != 1 || h.u.lay.cols[d] != (image.Rectangle{}) || h.u.count("") != 1 {
		t.Fatalf("All cards drew %d cards, Done at %v, count %d", len(h.u.lay.cards), h.u.lay.cols[d], h.u.count(""))
	}
	if h.u.count(viewArchive) != 1 || h.u.sentence() != "1 cards" {
		t.Fatalf("archive count %d, sentence %q", h.u.count(viewArchive), h.u.sentence())
	}
	h.clickButton("sideorg:Acme")
	if len(h.u.lay.cards) != 1 || h.u.lay.cols[d] != (image.Rectangle{}) || h.u.count("org:Acme") != 1 {
		t.Fatalf("org view drew %d cards, Done at %v", len(h.u.lay.cards), h.u.lay.cols[d])
	}
}

// TestTerminal: T opens the card's session in its own view with an id the
// card keeps, keys (esc too) go to the program, Close hands it back.
func TestTerminal(t *testing.T) {
	h := newHarness(t)
	var got []string
	startTerm = func(cmd *exec.Cmd, w, ht int, notify func()) (*term.Term, error) {
		got = cmd.Args
		return term.Start(prog("out:ready\n", "raw"), w, ht, notify)
	}
	defer func() { startTerm = term.Start }()
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	h.u.sel = c.ID
	h.send(press("T", 0))
	id := h.u.b.Card(c.ID).Session.ID
	if h.u.view != "term:"+c.ID || len(id) != 36 || !slices.Contains(got, "--session-id") || !slices.Contains(got, id) {
		t.Fatalf("view %q, session %q, args %v", h.u.view, id, got)
	}
	screen := func() string {
		runs, _, _, _ := h.u.terms[c.ID].t.Grid(0)
		var b strings.Builder
		for _, row := range runs {
			for _, r := range row {
				b.WriteString(r.Text)
			}
		}
		return b.String()
	}
	waitFor := func(s string) {
		for end := time.Now().Add(5 * time.Second); !strings.Contains(screen(), s); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatalf("no %q on the terminal: %q", s, screen())
			}
			h.frame()
		}
	}
	waitFor("ready")
	h.send(key.EditEvent{Text: "hola"}, press(key.NameEscape, 0))
	if wine() {
		// Wine's ConPTY drops a lone ESC from the input (Windows' passes it on
		// as the Escape key, read back as ESC in VT input mode).
		waitFor("hola")
	} else {
		waitFor("hola^[") // the program got them, esc too
	}
	if h.u.view != "term:"+c.ID {
		t.Fatal("esc left the terminal instead of reaching it")
	}
	h.clickButton("sidesess:" + c.ID) // its Agent Sessions row: the terminal, since it is open
	if h.u.view != "term:"+c.ID {
		t.Fatalf("its session row opened %q", h.u.view)
	}
	h.clickButton("termClose")
	if h.u.view != "" || len(h.u.terms) != 0 || h.u.b.Card(c.ID).Note != "Terminal closed" {
		t.Fatalf("after Close: view %q, %d terminals, note %q", h.u.view, len(h.u.terms), h.u.b.Card(c.ID).Note)
	}
}

// TestDragToColumn drags the first card into the third column, then a click
// opens a card's sheet.
func TestDragToColumn(t *testing.T) {
	h := newHarness(t)
	for _, s := range []string{"a", "b"} {
		h.u.b.Add(0, kanban.Card{Title: s})
	}
	h.u.b.Add(2, kanban.Card{Title: "x"})
	h.frame()
	from := h.u.lay.cards[0].r.Min.Add(image.Pt(20, 10))
	target := h.u.lay.cards[2].r                    // card x, in column 2
	to := image.Pt(target.Min.X+20, target.Min.Y+2) // over x's top half: lands before it
	pt := func(p image.Point) f32.Point { p = p.Add(boardAt); return f32.Pt(float32(p.X), float32(p.Y)) }
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: pt(from)})
	h.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: pt(from.Add(image.Pt(30, 0)))})
	h.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: pt(to)})
	if !h.u.drag.on {
		t.Fatal("drag did not start")
	}
	h.send(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: pt(to)})
	if got := h.titles(2); len(got) != 2 || got[0] != "a" || got[1] != "x" {
		t.Fatalf("column 2 after drop: %v", got)
	}
	c := h.u.lay.cards[0].r.Min.Add(image.Pt(20, 10)) // "b", now first
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: pt(c)},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: pt(c)})
	if c := h.u.b.Card(h.u.panel); c == nil || c.Title != "b" {
		t.Fatal("click did not open the card's session")
	}
}

func TestDueText(t *testing.T) {
	now := time.Date(2026, 10, 27, 9, 0, 0, 0, time.Local)
	for due, want := range map[string]string{"2026-10-20": "Overdue · Oct 20", "2026-10-27": "Today", "2026-10-28": "Tomorrow", "2026-10-30": "Fri", "2026-11-12": "Nov 12"} {
		if s, _, _, _ := dueText(kanban.Card{Due: due}, now, false); s != want {
			t.Errorf("%s: %q, want %q", due, s, want)
		}
	}
	if s, _, _, _ := dueText(kanban.Card{Due: "2026-10-20"}, now, true); s != "Oct 20" {
		t.Errorf("done card flagged: %q", s)
	}
}

// TestAgentSessionsHideDone: a Done card's session leaves Agent Sessions,
// unless it is the one being looked at.
func TestAgentSessionsHideDone(t *testing.T) {
	h := newHarness(t)
	work := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Working", Session: kanban.Session{ID: "a"}})
	done := h.u.b.Add(h.u.b.Column("Done"), kanban.Card{Title: "Finished", Session: kanban.Session{ID: "b"}})
	rows := func() (ids []string) {
		for _, s := range h.u.sections() {
			if s.title == "Agent Sessions" {
				for _, it := range s.items {
					ids = append(ids, strings.TrimPrefix(it.view, "sess:"))
				}
			}
		}
		return ids
	}
	if got := rows(); !slices.Equal(got, []string{work.ID}) {
		t.Fatalf("rows %q", got)
	}
	h.u.view = "sess:" + done.ID
	if got := rows(); len(got) != 2 {
		t.Fatalf("the open one stays: %q", got)
	}
}

// TestAgentSessionsByOrg: sessions group under their organization, sorted,
// the ones without an org last; a single group gets no header.
func TestAgentSessionsByOrg(t *testing.T) {
	h := newHarness(t)
	col := h.u.b.Column("In progress")
	rows := func() (views []string) {
		for _, s := range h.u.sections() {
			if s.title == "Agent Sessions" {
				for _, it := range s.items {
					views = append(views, it.view+"="+it.label)
				}
			}
		}
		return views
	}
	g := h.u.b.Add(col, kanban.Card{Title: "G", Org: "Globex", Session: kanban.Session{ID: "1"}})
	if got := rows(); !slices.Equal(got, []string{"sess:" + g.ID + "=" + g.Key() + " G"}) {
		t.Fatalf("one org: %q", got)
	}
	n := h.u.b.Add(col, kanban.Card{Title: "N", Session: kanban.Session{ID: "2"}})
	a := h.u.b.Add(col, kanban.Card{Title: "A", Org: "Acme", Session: kanban.Session{ID: "3"}})
	h.u.b.Add(col, kanban.Card{Title: "No session", Org: "Initech"})
	want := []string{"hdr:Acme=Acme", "sess:" + a.ID + "=" + a.Key() + " A", "hdr:Globex=Globex", "sess:" + g.ID + "=" + g.Key() + " G", "hdr:=No organization", "sess:" + n.ID + "=" + n.Key() + " N"}
	if got := rows(); !slices.Equal(got, want) {
		t.Fatalf("grouped:\n%q\nwant\n%q", got, want)
	}
	h.frame() // the headers draw as labels
	if h.u.view != "" {
		t.Fatalf("a header is no view: %q", h.u.view)
	}
}

// TestSessionRowFromPanel: with a card's panel open, its Agent Sessions row
// opens the full window. The sidebar is laid out before the panel, so the
// panel it had decided to draw is gone by then (it used to crash).
func TestSessionRowFromPanel(t *testing.T) {
	h := newHarness(t)
	h.create("A card", "Acme")
	c := h.only()
	c.Session.ID = "s1"
	h.u.openPanel(c.ID)
	h.frame()
	for y := float32(110); y < 200 && h.u.view == ""; y += 4 { // the row under Agent Sessions
		p := f32.Pt(100, y)
		h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Position: p, Buttons: pointer.ButtonPrimary},
			pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
	}
	if h.u.view != "sess:"+c.ID || h.u.panel != "" {
		t.Fatalf("view %q panel %q", h.u.view, h.u.panel)
	}
}

// TestTasks: a session's subagent and background shell list under its
// header; the subagent's messages stay out of the conversation and show in
// its own view, the shell's output in its; Esc goes back to the conversation.
func TestTasks(t *testing.T) {
	h := newHarness(t)
	h.create("A card", "Acme")
	id := h.only().ID
	out := filepath.Join(t.TempDir(), "b1.output")
	os.WriteFile(out, []byte("tick 1\ntick 2\n"), 0o600)
	for _, l := range []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"sleep 20; echo done","run_in_background":true}}]}}`,
		`{"type":"system","subtype":"task_started","task_id":"b1","tool_use_id":"tu1","description":"Sleep then echo","task_type":"local_bash"}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","content":"Command running in background with ID: b1. Output is being written to: ` + strings.ReplaceAll(out, `\`, `\\`) + `"}]}}`, // JSON: Windows' backslashes escaped
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu2","name":"Agent","input":{"description":"Echo hi"}}]}}`,
		`{"type":"system","subtype":"task_started","task_id":"a1","tool_use_id":"tu2","description":"Echo hi","subagent_type":"general-purpose","task_type":"local_agent","prompt":"Run echo hi"}`,
		`{"type":"assistant","parent_tool_use_id":"tu2","message":{"content":[{"type":"tool_use","id":"tu3","name":"Bash","input":{"command":"echo hi-from-sub"}}]}}`,
		`{"type":"system","subtype":"task_notification","task_id":"a1","status":"completed","summary":"It printed hi."}`,
		// resumed with SendMessage: its messages now name that tool use
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu4","name":"SendMessage","input":{"to":"a1","summary":"Ask for more"}}]}}`,
		`{"type":"assistant","parent_tool_use_id":"tu4","message":{"content":[{"type":"text","text":"More: hi again."}]}}`,
		// one desk never saw start (before it opened)
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu5","name":"SendMessage","input":{"to":"a9","summary":"Resume"}}]}}`,
		`{"type":"assistant","parent_tool_use_id":"tu5","message":{"content":[{"type":"text","text":"Resuming."}]}}`,
	} {
		for _, e := range claude.Parse([]byte(l)) {
			h.u.onEvent(id, 0, e)
		}
	}
	for _, m := range h.u.chat(id) {
		if strings.Contains(m.Text, "hi-from-sub") {
			t.Fatalf("a subagent's tool use in the conversation: %+v", m)
		}
	}
	ts := h.u.tasks[id]
	if len(ts) != 3 || !ts[0].Shell || ts[0].cmd != "sleep 20; echo done" || ts[1].Status != "running" || len(ts[1].msgs) != 3 || ts[2].ID != "a9" || len(ts[2].msgs) != 2 {
		t.Fatalf("%+v", ts)
	}
	h.u.openPanel(id)
	h.u.sub = "b1"
	h.frame()
	if !strings.Contains(ts[0].out, "tick 2") {
		t.Fatalf("shell output not read: %q", ts[0].out)
	}
	h.u.sub = "a1"
	h.frame()
	h.send(press(key.NameEscape, 0))
	if h.u.sub != "" || h.u.panel != id {
		t.Fatalf("esc: sub %q panel %q", h.u.sub, h.u.panel)
	}
	if got := h.u.by(id, ts[0]); got != "by the session" {
		t.Errorf("session's shell: %q", got)
	}
	// a shell a subagent started says so
	for _, l := range []string{
		`{"type":"assistant","parent_tool_use_id":"tu2","message":{"content":[{"type":"tool_use","id":"tu6","name":"Bash","input":{"command":"cargo test","run_in_background":true}}]}}`,
		`{"type":"system","subtype":"task_started","task_id":"b2","tool_use_id":"tu6","description":"Cargo tests","task_type":"local_bash"}`,
	} {
		for _, e := range claude.Parse([]byte(l)) {
			h.u.onEvent(id, 0, e)
		}
	}
	if got := h.u.by(id, h.u.findTask(id, "b2", "")); got != "by Echo hi" {
		t.Errorf("subagent's shell: %q", got)
	}
	ts = h.u.tasks[id]
	// a finished task moves to the history a minute after it ends
	ts[0].Status, ts[0].end = "completed", time.Now().Add(-2*time.Minute)
	now, history := h.u.split(id, time.Now())
	if len(history) != 1 || history[0] != ts[0] || len(now) != 3 {
		t.Fatalf("now %d history %d", len(now), len(history))
	}
	// the full window, its rail open, then the history: drawn without trouble
	h.u.openSession(id)
	h.u.rail = true
	h.frame()
	h.u.sub = historyKey
	h.frame()
	h.send(press(key.NameEscape, 0))
	h.send(press(key.NameEscape, 0))
	if h.u.sub != "" || h.u.rail {
		t.Fatalf("esc: sub %q rail %v", h.u.sub, h.u.rail)
	}
}

// TestWaiting: a subagent says what holds it: polling its background
// shell, a tool that runs long, or nothing at all for a while.
func TestWaiting(t *testing.T) {
	h := newHarness(t)
	h.create("A card", "Acme")
	id := h.only().ID
	feed := func(ls ...string) {
		for _, l := range ls {
			for _, e := range claude.Parse([]byte(l)) {
				h.u.onEvent(id, 0, e)
			}
		}
	}
	feed(`{"type":"system","subtype":"task_started","task_id":"a1","tool_use_id":"tu1","description":"Review","task_type":"local_agent"}`,
		`{"type":"assistant","parent_tool_use_id":"tu1","message":{"content":[{"type":"tool_use","id":"tu2","name":"Bash","input":{"command":"cargo test","run_in_background":true}}]}}`,
		`{"type":"system","subtype":"task_started","task_id":"b1","tool_use_id":"tu2","description":"Cargo tests","task_type":"local_bash"}`,
		`{"type":"user","parent_tool_use_id":"tu1","message":{"content":[{"type":"tool_result","tool_use_id":"tu2","content":"Command running in background with ID: b1. Output is being written to: /tmp/x"}]}}`)
	a := h.u.findTask(id, "a1", "")
	now := time.Now()
	if w, ok := h.u.waiting(id, a, now); ok {
		t.Fatalf("busy just now, yet %+v", w)
	}
	if w, ok := h.u.waiting(id, a, now.Add(time.Minute)); !ok || w.text != "Waiting on Cargo tests" {
		t.Fatalf("idle with its shell running: %+v %v", w, ok)
	}
	feed(`{"type":"assistant","parent_tool_use_id":"tu1","message":{"content":[{"type":"tool_use","id":"tu3","name":"TaskOutput","input":{"task_id":"b1"}}]}}`)
	if w, ok := h.u.waiting(id, a, time.Now()); !ok || w.text != "Waiting on Cargo tests" {
		t.Fatalf("polling its shell: %+v %v", w, ok)
	}
	feed(`{"type":"user","parent_tool_use_id":"tu1","message":{"content":[{"type":"tool_result","tool_use_id":"tu3","content":"done"}]}}`,
		`{"type":"system","subtype":"task_updated","task_id":"b1","patch":{"status":"completed"}}`,
		`{"type":"assistant","parent_tool_use_id":"tu1","message":{"content":[{"type":"tool_use","id":"tu4","name":"Bash","input":{"command":"cargo build"}}]}}`)
	if w, ok := h.u.waiting(id, a, time.Now().Add(45*time.Second)); !ok || w.text != "Running Bash cargo build" {
		t.Fatalf("a long tool: %+v %v", w, ok)
	}
	feed(`{"type":"user","parent_tool_use_id":"tu1","message":{"content":[{"type":"tool_result","tool_use_id":"tu4","content":"ok"}]}}`)
	if w, ok := h.u.waiting(id, a, time.Now().Add(3*time.Minute)); !ok || w.text != "No activity" {
		t.Fatalf("quiet: %+v %v", w, ok)
	}
}

func TestModelName(t *testing.T) {
	for id, want := range map[string]string{"claude-opus-5-5": "Opus 5.5", "claude-haiku-5-5-20261001": "Haiku 5.5", "claude-fable-5-1": "Fable 5.1", "": ""} {
		if got := modelName(id); got != want {
			t.Errorf("%q: %q, want %q", id, got, want)
		}
	}
}

// TestNotify: a card that turns to the user's notifies the desktop, unless
// its session is open in the focused window.
func TestNotify(t *testing.T) {
	h := newHarness(t)
	var got []string
	desktopNotify = func(title, body string) { got = append(got, title) }
	defer func() { desktopNotify = func(string, string) {} }()
	h.create("A card", "Acme")
	id := h.only().ID
	key := h.u.b.Card(id).Key()
	h.u.b.Card(id).Status = kanban.StatusWorking
	turn := claude.Parse([]byte(`{"type":"result","subtype":"success","is_error":false}`))[0]
	h.u.onEvent(id, 0, turn)
	if len(got) != 1 || got[0] != key+" · Your turn" {
		t.Fatalf("%q", got)
	}
	h.u.onEvent(id, 0, turn) // no change: nothing more
	h.u.b.Card(id).Status = kanban.StatusWorking
	h.u.focused, h.u.panel = true, id // looking at it
	h.u.onEvent(id, 0, turn)
	if len(got) != 1 {
		t.Fatalf("notified while in view: %q", got)
	}
}

// TestCardMenu: a right-click on a card opens its menu; an item runs and
// closes it; esc closes it too.
func TestCardMenu(t *testing.T) {
	h := newHarness(t)
	h.create("A card", "Acme")
	id := h.only().ID
	var r image.Rectangle
	for _, c := range h.u.lay.cards {
		if c.id == id {
			r = c.r
		}
	}
	at := f32.Pt(float32(boardAt.X+r.Min.X+20), float32(boardAt.Y+r.Min.Y+10))
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Position: at, Buttons: pointer.ButtonSecondary},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: at})
	if h.u.menu == nil || h.u.menu.id != id {
		t.Fatal("no menu")
	}
	h.send(press(key.NameEscape, 0))
	if h.u.menu != nil {
		t.Fatal("esc left the menu open")
	}
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Position: at, Buttons: pointer.ButtonSecondary},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: at})
	first := h.u.menu.r.Min.Add(boardAt).Add(image.Pt(30, 14)) // Open session
	p := f32.Pt(float32(first.X), float32(first.Y))
	h.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: p})
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Position: p, Buttons: pointer.ButtonPrimary},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
	if h.u.menu != nil || h.u.panel != id {
		t.Fatalf("menu %v panel %q", h.u.menu, h.u.panel)
	}
}

// TestSelectMsg: a Claude reply's markdown is selectable as it reads: a
// drag across a block selects its text without the markup, a double-click
// a word; a link stays a link.
func TestSelectMsg(t *testing.T) {
	h := newHarness(t)
	h.create("A card", "Acme")
	id := h.only().ID
	h.u.say(id, "claude", "**Because** of `this`, see [the docs](https://example.com/docs).")
	h.u.openPanel(id)
	h.frame()
	reply := fmt.Sprintf("%s-%d-0", id, len(h.u.chat(id))-1) // the reply's first block
	st := h.u.rsels[reply]
	if st == nil || string(st.src) != "Because of this, see the docs." {
		t.Fatalf("selectable block %v", st)
	}
	l := st.lines[0]
	st.sel = [2]int{st.hit(image.Pt(0, l.top)), st.hit(image.Pt(1e6, l.top))}
	if st.selected() != "Because of this, see the docs." {
		t.Fatalf("selected %q", st.selected())
	}
	if lo, hi := wordAt(st.src, 1); string(st.src[lo:hi]) != "Because" {
		t.Fatalf("word %q", string(st.src[lo:hi]))
	}
	last := st.pieces[len(st.pieces)-1]
	if sp := st.spanAt(image.Pt(last.x+1, st.lines[last.line].top+1)); sp < 0 || sp != last.span {
		t.Fatalf("span under the link: %d", sp)
	}
}

// TestShellBang: a "!" message runs directly in the card's folder, no
// Claude started, the card out of Blocked, the message box locked; its
// output shows as it comes, and goes to Claude by itself when it ends, with
// its exit status and time (no "you" line in the chat); a stopped one too.
func TestShellBang(t *testing.T) {
	h := newHarness(t)
	fake := &fakeClaude{}
	startClaude = func(o claude.Options, on func(claude.Event)) (session, error) {
		fake.opts, fake.on = o, on
		return fake, nil
	}
	var ran []string
	gate := make(chan struct{})
	defer func(f func(context.Context, string, string, io.Writer) error) { runShell = f }(runShell)
	runShell = func(ctx context.Context, dir, cmd string, w io.Writer) error {
		ran = append(ran, dir+"|"+cmd)
		io.WriteString(w, "main.go\n")
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
		io.WriteString(w, "README.md\n")
		return nil
	}
	c := h.u.b.Add(h.u.b.Column("Blocked"), kanban.Card{Title: "Look", Org: "Acme", Status: kanban.StatusBlocked, Note: "waiting for the user to run it"})
	h.u.openSession(c.ID)
	h.frame()
	h.u.chatIn.SetText("! ls")
	h.frame() // the box warns: draws with the warning row
	h.send(press(key.NameReturn, 0))
	last := func() kanban.ChatMsg { ms := h.u.chat(c.ID); return ms[len(ms)-1] }
	for end := time.Now().Add(2 * time.Second); last().Text == "" && time.Now().Before(end); {
		h.frame()
	}
	if len(ran) != 1 || ran[0] != filepath.Join(h.u.root, "Acme")+"|ls" || last().Text != "main.go" || last().Detail != "running…" {
		t.Fatalf("ran %q, while running %+v", ran, last())
	}
	if col, _ := h.u.b.Find(c.ID); col != h.u.b.Column("In progress") || h.u.b.Card(c.ID).Status != kanban.StatusWorking {
		t.Fatalf("while the user's command runs the card is %+v in column %d", h.u.b.Card(c.ID), col)
	}
	h.frame()
	if !h.u.chatIn.ReadOnly {
		t.Fatal("the message box is open while the command runs")
	}
	h.u.send(c.ID, "what's here?") // refused: the command runs
	if len(fake.prompts) != 0 {
		t.Fatalf("sent while the command ran: %q", fake.prompts)
	}
	close(gate)
	for end := time.Now().Add(2 * time.Second); len(fake.prompts) == 0 && time.Now().Before(end); {
		h.frame()
	}
	if len(fake.prompts) != 1 || !strings.Contains(fake.prompts[0], "<bash-input>ls</bash-input>\n<bash-stdout>main.go\nREADME.md</bash-stdout>") ||
		!strings.Contains(fake.prompts[0], "Card DESK-1: Look") || !strings.Contains(fake.prompts[0], "My command ended: exit 0 after ") { // still the card's first message
		t.Fatalf("prompt %q", fake.prompts)
	}
	h.frame()
	if m := last(); m.Who != "shell" || m.Text != "main.go\nREADME.md" || !strings.HasPrefix(m.Detail, "exit 0") || m.Path != toClaudeSent || h.u.chatIn.ReadOnly {
		t.Fatalf("chat ends with %+v (no note of desk's own), box read-only %v", m, h.u.chatIn.ReadOnly)
	}
	if h.u.shellOut[c.ID] != "" || h.u.shellStop[c.ID] != nil {
		t.Fatal("the output is still waiting")
	}

	gate = make(chan struct{}) // a long one, stopped
	h.u.send(c.ID, "! sleep 600")
	h.u.shellStop[c.ID]()
	for end := time.Now().Add(2 * time.Second); len(fake.prompts) < 2 && time.Now().Before(end); {
		h.frame()
	}
	if m := last(); !strings.HasPrefix(m.Detail, "stopped") || m.Path != toClaudeSent || len(fake.prompts) != 2 ||
		!strings.Contains(fake.prompts[1], "<bash-input>sleep 600</bash-input>") || !strings.Contains(fake.prompts[1], "My command ended: stopped after") {
		t.Fatalf("stopped %+v, prompts %q", m, fake.prompts)
	}
}

// TestChatPasteText: Ctrl+V of text in the session view's message box
// pastes it (no image in the clipboard).
func TestChatPasteText(t *testing.T) {
	h := newHarness(t)
	h.create("A card", "Acme")
	id := h.only().ID
	h.u.openSession(id)
	h.frame()
	h.r.Source().Execute(key.FocusCmd{Tag: &h.u.chatIn})
	h.frame()
	asked := make(chan bool, 4)
	clipImage = func() ([]byte, string, error) { asked <- true; return nil, "", nil }
	h.send(press("V", key.ModShortcut))
	select {
	case <-asked:
	case <-time.After(2 * time.Second):
		t.Fatal("Ctrl+V never looked for an image")
	}
	req := false // ClipboardRequested says so once
	for end := time.Now().Add(2 * time.Second); !req && time.Now().Before(end); {
		h.frame()
		req = h.r.ClipboardRequested()
	}
	if !req {
		t.Fatal("the clipboard's text was never asked for")
	}
	h.r.Queue(transfer.DataEvent{Type: "application/text", Open: func() io.ReadCloser { return io.NopCloser(strings.NewReader("pasted words")) }})
	h.frame()
	if h.u.chatIn.Text() != "pasted words" {
		t.Fatalf("message box %q", h.u.chatIn.Text())
	}
}

// TestExplain: a shell command keeps its whole text and Claude's
// description; Haiku explains it once, automatically in the shell's view
// and on asking in the chat, and a failure can be asked again.
func TestExplain(t *testing.T) {
	h := newHarness(t)
	asked := 0
	explainCmd = func(_ context.Context, _, _, dir, cmd string) (string, error) {
		asked++
		if asked == 1 {
			return "", errors.New("overloaded")
		}
		return "Runs the tests of " + cmd, nil
	}
	h.create("A card", "Acme")
	id := h.only().ID
	long := "cargo test -p licensing --features it -- --nocapture " + strings.Repeat("x", 80)
	for _, l := range []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"` + long + `","description":"Run the licensing tests to check the fix","run_in_background":true}}]}}`,
		`{"type":"system","subtype":"task_started","task_id":"b1","tool_use_id":"tu1","description":"Run the licensing tests to check the fix","task_type":"local_bash"}`,
	} {
		for _, e := range claude.Parse([]byte(l)) {
			h.u.onEvent(id, 0, e)
		}
	}
	m := h.u.chat(id)[len(h.u.chat(id))-1]
	if m.Cmd != long || m.Detail != "Run the licensing tests to check the fix" || h.u.tasks[id][0].cmd != long {
		t.Fatalf("%+v / %q", m, h.u.tasks[id][0].cmd)
	}
	h.u.openSession(id)
	h.u.sub = "b1" // the shell's view asks by itself
	h.frame()
	waitFor := func(cond func() bool) {
		for i := 0; i < 100 && !cond(); i++ {
			time.Sleep(10 * time.Millisecond)
			h.frame()
		}
	}
	waitFor(func() bool { e := h.u.explains[long]; return e != nil && !e.busy })
	if e := h.u.explains[long]; e.err != "overloaded" {
		t.Fatalf("%+v", e)
	}
	h.frame() // shown failed: it doesn't ask again by itself
	if asked != 1 {
		t.Fatalf("asked %d times", asked)
	}
	h.u.explain(id, long) // Try again
	waitFor(func() bool { return !h.u.explains[long].busy })
	if e := h.u.explains[long]; e.text != "Runs the tests of "+long || e.err != "" || asked != 2 {
		t.Fatalf("%+v asked %d", e, asked)
	}
}

func TestLineDiff(t *testing.T) {
	got := lineDiff("a\nb\nc\nd\n", "a\nB\nc\nd\ne\n")
	var s []string
	for _, l := range got {
		s = append(s, string(l.op)+l.text)
	}
	if want := []string{" a", "-b", "+B", " c", " d", "+e"}; !slices.Equal(s, want) {
		t.Fatalf("%q, want %q", s, want)
	}
	if d := lineDiff("", "x\ny"); len(d) != 2 || d[0].op != '+' {
		t.Fatalf("a new text: %+v", d)
	}
}

// TestWordDiff: two lines word by word, kept, lost and gained in order, and
// how alike they are.
func TestWordDiff(t *testing.T) {
	a, b, both, alike := wordDiff("return total, nil", "return sum, nil")
	if len(a) != 3 || a[1] != (seg{'-', "total"}) || b[1] != (seg{'+', "sum"}) || len(both) != 4 || both[2] != (seg{'+', "sum"}) || alike < wordsAlike {
		t.Fatalf("a %q b %q both %q alike %.2f", a, b, both, alike)
	}
	if _, _, _, alike := wordDiff("x := load()", "for _, r := range rows {"); alike >= markAlike {
		t.Fatalf("a rewrite reads as alike: %.2f", alike)
	}
}

// TestDiffRows: Auto takes each block on its own: a small edit merged by
// words, an added block unified, a rewrite split when wide (unified when
// narrow); a picked view holds for every block.
func TestDiffRows(t *testing.T) {
	lines := lineDiff("a := 1\nreturn total, nil\nkeep\nx := load()\n", "a := 1\nreturn sum, nil\nkeep\nfor _, r := range rows {\n\tuse(r)\n}\nadded\n")
	ops := func(rs []diffRow) string {
		var s strings.Builder
		for _, r := range rs {
			s.WriteByte(r.op)
		}
		return s.String()
	}
	if got := ops(diffRows(lines, diffAuto, true)); got != " ~ ||||" {
		t.Fatalf("auto, wide: %q", got)
	}
	if got := ops(diffRows(lines, diffAuto, false)); got != " ~ -++++" {
		t.Fatalf("auto, narrow: %q", got)
	}
	if got := ops(diffRows(lines, diffUnified, true)); got != " -+ -++++" {
		t.Fatalf("unified: %q", got)
	}
	if got := ops(diffRows(lines, diffSplit, true)); got != "=|=||||" {
		t.Fatalf("split: %q", got)
	}
	if got := ops(diffRows(lineDiff("", "x\ny\n"), diffAuto, true)); got != "++" {
		t.Fatalf("a new file: %q", got)
	}
	// lines pair with the most alike, not by position: the comment stands alone
	rs := diffRows(lineDiff("const limit = 100\n", "// limit caps a page.\nconst limit = 250\n"), diffSplit, true)
	if len(rs) != 2 || rs[0].l != nil || rs[1].l == nil || rs[1].r[0].text != "const limit = " {
		t.Fatalf("pairing: %+v", rs)
	}
}

// TestPairLines: an edit beside its line, a rewrite still beside the line
// it replaced, an extra line alone.
func TestPairLines(t *testing.T) {
	ps := pairLines([]string{"x := load()", "return total, nil"}, []string{"for _, r := range rows {", "return sum, nil", "}"})
	var got []string
	for _, p := range ps {
		got = append(got, fmt.Sprint(p.d, p.a))
	}
	if want := []string{"0 0", "1 1", "-1 2"}; !slices.Equal(got, want) {
		t.Fatalf("pairs %q, want %q", got, want)
	}
}

// TestTaskBarIdle: with nothing running, the bar shows the history and
// the rail, and its Hide details takes them away again.
func TestTaskBarIdle(t *testing.T) {
	h := newHarness(t)
	h.create("A card", "Acme")
	id := h.only().ID
	h.u.onTask(id, &claude.Task{ID: "b1", Shell: true, Name: "Tests", Status: "completed"})
	h.u.tasks[id][0].end = time.Now().Add(-2 * time.Minute)
	h.u.openSession(id)
	h.frame()
	h.clickButton("taskBar" + id)
	if !h.u.rail || h.u.sub != historyKey {
		t.Fatalf("show: rail %v sub %q", h.u.rail, h.u.sub)
	}
	h.clickButton("taskBar" + id)
	if h.u.rail || h.u.sub != "" {
		t.Fatalf("hide: rail %v sub %q", h.u.rail, h.u.sub)
	}
}

// TestTurnEndsWhileBackground: a turn that ends with a subagent still
// running leaves the card working (Claude goes on when it finishes), and
// tells nobody it's their turn; the next turn, with nothing left, does.
func TestTurnEndsWhileBackground(t *testing.T) {
	h := newHarness(t)
	var got []string
	desktopNotify = func(title, body string) { got = append(got, title+"|"+body) }
	defer func() { desktopNotify = func(string, string) {} }()
	h.create("A card", "Acme")
	id := h.only().ID
	h.u.b.Card(id).Status = kanban.StatusWorking
	feed := func(l string) {
		for _, e := range claude.Parse([]byte(l)) {
			h.u.onEvent(id, 0, e)
		}
	}
	feed(`{"type":"system","subtype":"task_started","task_id":"a1","tool_use_id":"tu1","description":"Run e2e","task_type":"local_agent","is_backgrounded":true}`)
	feed(`{"type":"assistant","message":{"content":[{"type":"text","text":"**Now running:** the e2e agent."}]}}`)
	feed(`{"type":"result","subtype":"success","is_error":false}`)
	if c := h.u.b.Card(id); c.Status != kanban.StatusWorking || c.Note != "Waiting on its subagent: Run e2e" || len(got) != 0 {
		t.Fatalf("status %q note %q notified %q", c.Status, c.Note, got)
	}
	feed(`{"type":"system","subtype":"task_notification","task_id":"a1","status":"completed","summary":"done"}`)
	feed(`{"type":"system","subtype":"init","session_id":"s1"}`)
	feed(`{"type":"result","subtype":"success","is_error":false}`)
	if c := h.u.b.Card(id); c.Status != kanban.StatusWaiting || len(got) != 1 || !strings.Contains(got[0], "Now running: the e2e agent.") {
		t.Fatalf("status %q notified %q", c.Status, got)
	}
}

// TestStartsWorking: a created card starts at once (its title and notes
// go with the first message); one with no conversation has Start working.
func TestStartsWorking(t *testing.T) {
	h := newHarness(t)
	h.create("Fix the export", "Acme")
	c := h.only()
	if len(h.claudes) != 1 || len(h.claudes[0].prompts) != 1 || !strings.Contains(h.claudes[0].prompts[0], "Fix the export") ||
		!strings.HasSuffix(h.claudes[0].prompts[0], startWork) || c.Status != kanban.StatusWorking {
		t.Fatalf("claudes %d, status %q", len(h.claudes), c.Status)
	}
	old := h.u.b.Add(0, kanban.Card{Title: "Older card", Org: "Acme"})
	h.u.openPanel(old.ID)
	h.frame()
	h.clickButton("pnStart" + old.ID)
	if len(h.claudes) != 2 || !strings.Contains(h.claudes[1].prompts[0], "Older card") {
		t.Fatalf("Start working: %d sessions", len(h.claudes))
	}
}

// TestPasteImage: Ctrl+V of an image in the notes keeps the image and puts
// a reference to it there, which Prompt Optimization doesn't lose and the
// session can read; text still pastes as text.
func TestPasteImage(t *testing.T) {
	h := newHarness(t)
	clip := []byte("\x89PNG fake")
	clipImage = func() ([]byte, string, error) { return clip, "image/png", nil }
	optimize = func(_ context.Context, a claude.Ask, _ func(claude.Progress)) (claude.Draft, error) {
		return claude.Draft{Title: "Fix the chart", Prompt: "Goal: the chart's labels overlap."}, nil
	}
	h.u.newCard(0)
	h.frame()
	s := h.u.edit
	s.org.SetText("Acme")
	h.r.Source().Execute(key.FocusCmd{Tag: &s.notes})
	h.frame()
	h.send(press("V", key.ModShortcut), key.Event{Name: "V", Modifiers: key.ModShortcut, State: key.Release})
	for end := time.Now().Add(2 * time.Second); !strings.Contains(s.notes.Text(), "![image]") && time.Now().Before(end); {
		h.frame()
	}
	h.frame()
	m := imageRef.FindStringSubmatch(s.notes.Text())
	if m == nil || strings.Count(s.notes.Text(), "![image]") != 1 {
		t.Fatalf("notes %q: one paste, one image", s.notes.Text())
	}
	if b, err := os.ReadFile(m[1]); err != nil || string(b) != string(clip) || filepath.Dir(m[1]) != h.u.imagesDir() {
		t.Fatalf("image at %s: %q %v", m[1], b, err)
	}
	h.clickButton("shOpt") // improves: the rewritten notes keep the image
	for end := time.Now().Add(2 * time.Second); !s.optimized && time.Now().Before(end); {
		h.frame()
	}
	if n := s.notes.Text(); !strings.HasPrefix(n, "Goal:") || !strings.Contains(n, m[0]) {
		t.Fatalf("optimized notes %q", n)
	}
	h.clickButton("shSave")
	if len(h.claudes) != 1 || !slices.Contains(h.claudes[0].opts.Args, "--add-dir="+h.u.imagesDir()) || !strings.Contains(h.claudes[0].prompts[0], m[1]) {
		t.Fatalf("session args %q", h.claudes[0].opts.Args)
	}
	clipImage = func() ([]byte, string, error) { return nil, "", nil } // text: the editor's own paste, untouched
	h.u.newCard(0)
	h.frame()
	h.r.Source().Execute(key.FocusCmd{Tag: &h.u.edit.notes})
	h.frame()
	h.send(press("V", key.ModShortcut))
	h.frame()
	if h.u.edit.pasteText || strings.Contains(h.u.edit.notes.Text(), "![image]") {
		t.Fatalf("text paste: pending %v, notes %q", h.u.edit.pasteText, h.u.edit.notes.Text())
	}
}

// teamCard is an agent card whose lead handed work to two agents: one
// working, with a shell, and one done.
func teamCard(h *harness) *kanban.Card {
	c := h.u.b.Add(2, kanban.Card{Title: "Import bank statements v2", Org: "Acme", Agent: "planner", Status: kanban.StatusWaiting})
	h.u.say(c.ID, "you", "Start working on this card.")
	h.u.say(c.ID, "claude", "Planning: api parses, web does the upload screen.")
	h.u.onTask(c.ID, &claude.Task{ID: "a1", ToolUse: "tu1", Name: "Parse statements", Agent: "api", Model: "claude-sonnet-5-5", Prompt: "Parse the new statement format; PUT upserts only.", Status: "running", Tools: 31, Tokens: 48000})
	h.u.onTask(c.ID, &claude.Task{ID: "a2", ToolUse: "tu2", Name: "Upload screen", Agent: "web", Model: "claude-sonnet-5-5", Prompt: "Upload screen with per-file progress.", Status: "completed", Summary: "Upload screen done, es and en copy."})
	h.u.toolBy["tu3"] = "tu1"
	h.u.toolCmds["tu3"] = "go test ./..."
	h.u.onTask(c.ID, &claude.Task{ID: "s1", ToolUse: "tu3", Shell: true, Status: "running"})
	return h.u.b.Card(c.ID)
}

// TestCardAgent: a new card of an organization with agents takes its
// default one, the dialog can change it, and the session runs as it.
func TestCardAgent(t *testing.T) {
	h := newHarness(t)
	root := os.Getenv("DESK_ROOT")
	writeAt(filepath.Join(root, "Acme", ".desk", "desk.toml"), []byte("agent = \"planner\"\nagents = \"meta/.claude/agents\"\n"), 0o644)
	dir := filepath.Join(root, "Acme", "meta", ".claude", "agents")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "planner.md"), []byte("---\nname: planner\ndescription: Tech lead\n---\nLead.\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "api.md"), []byte("---\nname: api\ndescription: API work\n---\nBuild.\n"), 0o644)
	h.create("Plan the sprint", "Acme")
	c := h.u.b.Card(h.only().ID)
	if c.Agent != "planner" || len(h.claudes) != 1 || !slices.Contains(h.claudes[0].opts.Args, "--agent") || h.claudes[0].opts.Args[len(h.claudes[0].opts.Args)-1] != "planner" {
		t.Fatalf("agent %q, args %q", c.Agent, h.claudes[0].opts.Args)
	}
	h.u.open(c.ID)
	h.frame()
	h.clickButton("agent1")  // api: the names are sorted after None
	h.clickButton("shClose") // the drawer saves as it closes
	if c := h.u.b.Card(c.ID); c.Agent != "api" {
		t.Fatalf("picked api, card has %q", c.Agent)
	}
	h.create("Elsewhere", "Globex") // no agents there: none
	if n := len(h.claudes); n != 2 || h.claudes[1].opts.Args != nil {
		t.Fatalf("Globex session %d started with %q", n, h.claudes[n-1].opts.Args)
	}
}

// TestTicketSync: a card's tickets follow its column, pushed once per
// change, and all of them again when desk starts.
func TestTicketSync(t *testing.T) {
	h := newHarness(t)
	var got []string
	pushTicket = func(_ context.Context, l kanban.Link, col string, _ orgs.Config) (string, error) {
		got = append(got, l.Ref+">"+col)
		return "En curso", nil
	}
	c := h.u.b.Add(1, kanban.Card{Title: "Fix it", Org: "Acme", Links: []kanban.Link{
		{URL: "https://globex.atlassian.net/browse/ABC-1", Kind: links.KindJira, Ref: "ABC-1"},
		{URL: "https://github.com/globex-org/api/pull/3", Kind: links.KindGitHubPR, Ref: "PR #3"},
	}})
	wait := func() {
		for i := 0; i < 100 && h.u.pushing[c.ID]; i++ {
			time.Sleep(5 * time.Millisecond)
			h.frame()
		}
	}
	h.u.save()
	wait()
	if c := h.u.b.Card(c.ID); len(got) != 1 || got[0] != "ABC-1>To do" || c.Pushed != "To do" {
		t.Fatalf("first sight: %v, card at %q", got, c.Pushed)
	}
	h.u.save()
	wait()
	if len(got) != 1 {
		t.Fatalf("in step, pushed again: %v", got)
	}
	h.u.moveTo(c.ID, "In progress")
	h.u.save()
	wait()
	if c := h.u.b.Card(c.ID); len(got) != 2 || got[1] != "ABC-1>In progress" || c.Pushed != "In progress" || h.u.toast != "ABC-1 → En curso" {
		t.Fatalf("pushed %v, card at %q, toast %q", got, c.Pushed, h.u.toast)
	}
	h.u.syncTickets(true) // the background check: every ticket again
	wait()
	if len(got) != 3 || got[2] != "ABC-1>In progress" {
		t.Fatalf("start sync: %v", got)
	}
	pushTicket = func(context.Context, kanban.Link, string, orgs.Config) (string, error) {
		return "", errors.New("no transition")
	}
	for i := range 2 { // a failure is said once, not every check
		h.u.toast = ""
		h.u.syncTickets(true)
		wait()
		said := strings.HasSuffix(h.u.toast, ": couldn't move ABC-1: no transition")
		if said != (i == 0) || i == 1 && h.u.toast != "" {
			t.Fatalf("check %d: toast %q", i, h.u.toast)
		}
	}
}

// TestSplitWithServers: a card that handed server work to its org's infra
// session shows that session beside its chat, can show the chat alone, and
// the servers view has the way back to the card.
func TestSplitWithServers(t *testing.T) {
	h := newHarness(t)
	localServer(t, h.u.root, "Acme")
	h.u.infra = infra.New(h.u.root, func() {})
	defer h.u.infra.Close()
	c := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Check web", Org: "Acme"})
	h.u.cardInfra[c.ID] = "Acme"
	h.u.openSession(c.ID)
	h.frame()
	if !h.u.splitShown {
		t.Fatal("no infra session beside the chat")
	}
	h.clickButton("pnSplit" + c.ID)
	h.frame()
	if !h.u.noSplit[c.ID] || h.u.splitShown {
		t.Fatal("Chat only still split")
	}
	h.u.view = "srv:Acme"
	h.frame()
	h.clickButton("srvBack" + c.ID)
	if h.u.view != "sess:"+c.ID {
		t.Fatalf("back: view %q", h.u.view)
	}
}

// TestFoldersTab: the settings' Folders tab shows where desk searches,
// refuses what isn't a folder, and saving finds the organizations there.
func TestFoldersTab(t *testing.T) {
	h := newHarness(t)
	h.u.openConfig(cfgFolders)
	d := h.u.cfg
	if len(d.entries) != 1 || d.entries[0].text("path") != h.u.root || d.entries[0].text("depth") != "2" {
		t.Fatalf("default folder: %+v", d.entries)
	}
	other := t.TempDir()
	os.MkdirAll(filepath.Join(other, "Initech"), 0o755)
	e := newEntry(folderKind.key, map[string]any{})
	d.entries = append(d.entries, e)
	e.ed("path").SetText(filepath.Join(other, "nope"))
	h.u.saveConfig()
	if h.u.cfg == nil || !strings.Contains(d.err, "is not a folder") {
		t.Fatalf("saved a missing folder: %q", d.err)
	}
	e.ed("path").SetText(other)
	e.ed("depth").SetText("1")
	h.u.saveConfig()
	if h.u.cfg != nil || !slices.Contains(h.u.orgList, "Initech") || !slices.Contains(h.u.orgList, "Globex") {
		t.Fatalf("after save: %v %v", h.u.orgList, d.err)
	}
}

// TestOrgLoading: an org's fetches running spin its icon; the last one
// ending stops it.
func TestOrgLoading(t *testing.T) {
	h := newHarness(t)
	a, b := h.u.busy("acme"), h.u.busy("acme")
	h.frame()
	if h.u.loading["acme"] != 2 {
		t.Fatalf("loading %v", h.u.loading)
	}
	a()
	h.frame()
	if h.u.loading["acme"] != 1 {
		t.Fatalf("one ended: %v", h.u.loading)
	}
	b()
	h.frame()
	if len(h.u.loading) != 0 {
		t.Fatalf("all ended: %v", h.u.loading)
	}
}
