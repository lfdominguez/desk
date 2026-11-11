//go:build linux

package infra

import (
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/lfdominguez/desk/internal/testwait"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/x/styledtext"
	"github.com/charmbracelet/x/vt"
	east "github.com/yuin/goldmark/extension/ast"
	"golang.org/x/image/math/fixed"
)

// wEnv points every state, config and runtime dir at a temp dir.
func wEnv(t *testing.T) {
	t.Helper()
	for _, v := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(v, t.TempDir())
	}
}

// wUI is a window with a driver nobody runs: its actions queue in d.in.
func wUI(t *testing.T) *ui {
	t.Helper()
	wEnv(t)
	u := newUI(&driver{in: make(chan any, 256)}, guiLayout{}, func() {})
	u.org = "Acme"
	return u
}

// wActs takes the actions the window sent the model.
func wActs(u *ui) []func(*model) {
	var fs []func(*model)
	for {
		select {
		case x := <-u.d.in:
			if f, ok := x.(func(*model)); ok {
				fs = append(fs, f)
			}
		default:
			return fs
		}
	}
}

// wRun runs the window's actions on m and says how many there were.
func wRun(u *ui, m *model) int {
	fs := wActs(u)
	for _, f := range fs {
		f(m)
	}
	return len(fs)
}

// wh drives draw through a real input.Router, frame by frame.
type wh struct {
	t    *testing.T
	u    *ui
	r    input.Router
	ops  op.Ops
	size image.Point
	draw func(gtx layout.Context)
	pre  func(gtx layout.Context) // runs once at the next frame's start
}

func (h *wh) gtx() layout.Context {
	return layout.Context{Ops: &h.ops, Source: h.r.Source(), Now: time.Now(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(h.size)}
}

func (h *wh) frame() {
	h.ops.Reset()
	gtx := h.gtx()
	if h.pre != nil {
		h.pre(gtx)
		h.pre = nil
	}
	h.draw(gtx)
	h.r.Frame(&h.ops)
}

func (h *wh) send(evs ...event.Event) {
	for _, e := range evs {
		h.r.Queue(e)
	}
	h.frame()
	h.frame()
}

func wPress(n key.Name) key.Event { return key.Event{Name: n, State: key.Press} }

func wClick(p f32.Point) []event.Event {
	return []event.Event{
		pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p},
	}
}

// clickBtn clicks the window's button id as a user would, then lays it out.
func (h *wh) clickBtn(id string) {
	h.u.click(id).Click()
	h.frame()
	h.frame()
}

func newWH(t *testing.T, u *ui, draw func(gtx layout.Context)) *wh {
	h := &wh{t: t, u: u, size: image.Pt(1200, 800), draw: draw}
	h.frame()
	return h
}

// --- gui_sheet.go ---

// TestAskSheetAnswers: the question sheet masks a secret, sends the typed
// answer on enter or Continue, and cancels on esc or Cancel.
func TestAskSheetAnswers(t *testing.T) {
	u := wUI(t)
	u.st = &guiState{Ask: "Vault passphrase for Acme", AskMasked: true}
	h := newWH(t, u, func(gtx layout.Context) { u.sheets(gtx) })
	if u.askIn.Mask != '•' || u.sheetSig != "askVault passphrase for Acme" {
		t.Fatalf("mask %q sig %q", u.askIn.Mask, u.sheetSig)
	}
	answer := func(want string, ok bool) {
		t.Helper()
		q := &question{done: make(chan string, 1)}
		m := &model{asks: []*question{q}}
		if n := wRun(u, m); n != 1 {
			t.Fatalf("%d actions, want 1", n)
		}
		got, sent := <-q.done
		if sent != ok || got != want {
			t.Errorf("answer %q (sent %v), want %q (%v)", got, sent, want, ok)
		}
	}
	h.send(key.EditEvent{Text: "hunter2"}, wPress(key.NameReturn))
	answer("hunter2", true)
	h.send(wPress(key.NameEscape))
	answer("", false)
	h.clickBtn("askNo")
	answer("", false)
	u.askIn.SetText("swordfish")
	h.clickBtn("askOk")
	answer("swordfish", true)
	// an unmasked question
	u.st = &guiState{Ask: "Vault user"}
	h.frame()
	if u.askIn.Mask != 0 || u.askIn.Text() != "" {
		t.Errorf("reopened: mask %q text %q", u.askIn.Mask, u.askIn.Text())
	}
	// gone: the terminal takes the keyboard back
	u.wantTerm = false
	u.st = &guiState{}
	h.frame()
	if !u.wantTerm || u.sheetSig != "" {
		t.Error("closing the sheet left the keyboard")
	}
	if wRun(u, &model{}) != 0 {
		t.Error("no sheet, yet actions")
	}
}

// TestAskFor: vault unlocks read as Unlock <vault>, ssh's prompts as what
// the host asks with their note as the error, one-time codes get the boxes.
func TestAskFor(t *testing.T) {
	for _, c := range []struct {
		q    string
		want askView
	}{
		{"Master password · acme-vault", askView{title: "Unlock acme-vault", caption: "Master password", ok: "Unlock", note: "Kept in memory only, never written to disk."}},
		{"One-time code · acme-vault", askView{title: "Unlock acme-vault", caption: "One-time code", ok: "Unlock", note: "Kept in memory only, never written to disk.", code: true}},
		{"ssh web-01 · deploy@web-01's password:  (the vault password was rejected)", askView{title: "ssh web-01 asks", sub: "deploy@web-01's password:",
			err: "The vault password was rejected", ok: "Continue", note: "Sent to this ssh session only, not stored.", ssh: true}},
		{"ssh web-01 · pw:  ()", askView{title: "ssh web-01 asks", sub: "pw:  ()", ok: "Continue", note: "Sent to this ssh session only, not stored.", ssh: true}},
		{"ssh web-01 · pw:  (x", askView{title: "ssh web-01 asks", sub: "pw:  (x", ok: "Continue", note: "Sent to this ssh session only, not stored.", ssh: true}},
		{"ssh codehost · Are you sure (yes/no)?", askView{title: "ssh codehost asks", sub: "Are you sure (yes/no)?", ok: "Continue", note: "Sent to this ssh session only, not stored.", ssh: true}},
		{"ssh web-01 · Verification code:", askView{title: "ssh web-01 asks", sub: "Verification code:", ok: "Continue", note: "Sent to this ssh session only, not stored.", ssh: true, code: true}},
		{"ssh web-01 · Password (2FA):", askView{title: "ssh web-01 asks", sub: "Password (2FA):", ok: "Continue", note: "Sent to this ssh session only, not stored.", ssh: true, code: true}},
		{"Your OTP", askView{title: "Your OTP", sub: "desk needs this to continue", ok: "Continue", code: true}},
		{"ssh-agent passphrase", askView{title: "ssh-agent passphrase", sub: "desk needs this to continue", ok: "Continue"}},
	} {
		if got := askFor(c.q); got != c.want {
			t.Errorf("%q:\n got %+v\nwant %+v", c.q, got, c.want)
		}
	}
}

// TestAskSheetShowAndCode: Show reveals a masked answer and Hide masks it
// again; a one-time code is typed into the boxes and sent on enter; an ssh
// prompt's error shows.
func TestAskSheetShowAndCode(t *testing.T) {
	u := wUI(t)
	u.st = &guiState{Ask: "Master password · acme-vault", AskMasked: true}
	h := newWH(t, u, func(gtx layout.Context) { u.sheets(gtx) })
	h.clickBtn("askShow")
	if u.askIn.Mask != 0 {
		t.Error("Show kept the mask")
	}
	h.clickBtn("askShow")
	if u.askIn.Mask != '•' {
		t.Error("Hide didn't mask")
	}
	u.st = &guiState{Ask: "One-time code · acme-vault"}
	h.frame()
	h.send(key.EditEvent{Text: "123456"}, wPress(key.NameReturn))
	q := &question{done: make(chan string, 1)}
	if wRun(u, &model{asks: []*question{q}}); <-q.done != "123456" {
		t.Error("the code wasn't sent")
	}
	// a masked code: boxes (no Show) up to six, a plain field with Show past them
	u.st = &guiState{Ask: "One-time code · acme-vault", AskMasked: true}
	h.frame()
	u.askIn.SetText("123456")
	delete(u.clicks, "askShow")
	h.frame()
	if u.clicks["askShow"] != nil {
		t.Error("six digits: not the boxes")
	}
	u.askIn.SetText("1234567")
	h.frame()
	if u.clicks["askShow"] == nil {
		t.Error("seven: not the field")
	}
	u.st = &guiState{Ask: "ssh web-01 · Verification code:  (rejected)"}
	h.frame()
	if u.sheetSig != "askssh web-01 · Verification code:  (rejected)" {
		t.Errorf("sig %q", u.sheetSig)
	}
}

func wApproval(cmd string) (*approval, *model) {
	a := &approval{cmd: cmd, done: make(chan approvalResult, 1)}
	return a, &model{tabs: []*Tab{{org: "Initech", pending: []*approval{a}}}}
}

// TestApprovalSheetVerdicts: Y/A/N/enter/esc and the buttons decide, E edits
// the command (enter sends the edit, esc leaves the edit), a secret can't be
// edited; another org's approval shows no sheet.
func TestApprovalSheetVerdicts(t *testing.T) {
	u := wUI(t)
	ap := &guiApproval{Tab: "web-1", Host: "web-1", Cmd: "systemctl restart nginx", Reason: "nginx is down",
		Explain: []step{{Part: "systemctl", Does: "service manager"}, {Part: "restart nginx", Does: "stops and starts it"}}, Org: "Acme"}
	u.st = &guiState{Approval: ap}
	h := newWH(t, u, func(gtx layout.Context) { u.sheets(gtx) })
	if u.sheetSig != "approvalweb-1systemctl restart nginx" {
		t.Fatalf("sig %q", u.sheetSig)
	}
	verdict := func(ok bool, cmd string) {
		t.Helper()
		a, m := wApproval("systemctl restart nginx")
		if n := wRun(u, m); n != 1 {
			t.Fatalf("%d actions, want 1", n)
		}
		select {
		case r := <-a.done:
			if r.ok != ok || ok && r.cmd != cmd {
				t.Errorf("result %+v, want ok %v cmd %q", r, ok, cmd)
			}
		default:
			t.Error("not decided")
		}
	}
	h.send(wPress("Y"))
	verdict(true, "systemctl restart nginx")
	h.send(wPress(key.NameReturn))
	verdict(true, "systemctl restart nginx")
	h.send(wPress("N"))
	verdict(false, "")
	h.send(wPress(key.NameEscape))
	verdict(false, "")
	h.clickBtn("apN")
	verdict(false, "")
	h.clickBtn("apY")
	verdict(true, "systemctl restart nginx")
	h.clickBtn("apA")
	verdict(true, "systemctl restart nginx")

	// E edits; esc leaves the edit and the keys work again
	h.send(wPress("E"))
	if !u.editing || u.apEdit.Text() != "systemctl restart nginx" {
		t.Fatalf("editing %v text %q", u.editing, u.apEdit.Text())
	}
	h.send(wPress(key.NameEscape))
	if u.editing {
		t.Fatal("esc kept editing")
	}
	if wRun(u, &model{}) != 0 {
		t.Error("esc in the editor answered")
	}
	h.clickBtn("apE")
	if !u.editing {
		t.Fatal("Edit button didn't edit")
	}
	u.apEdit.SetText("edited")
	h.clickBtn("apE") // already editing: the text stays
	if u.apEdit.Text() != "edited" {
		t.Error("Edit again reset the edit")
	}
	h.frame()
	if !h.r.Source().Focused(&u.apEdit) {
		t.Error("Edit button: the editor isn't focused")
	}
	u.apEdit.SetText("  systemctl reload nginx ")
	h.send(wPress(key.NameReturn))
	verdict(true, "systemctl reload nginx")

	// a secret: E does nothing, there's no Edit or Always button
	u.editing = false
	sec := *ap
	sec.Secret, sec.Cmd = true, "vault/acme/db"
	u.st = &guiState{Approval: &sec}
	h.frame()
	h.send(wPress("E"))
	h.send(wPress("A"))
	if u.editing || len(wActs(u)) != 1 { // A is still sent: the model refuses "always" for secrets
		t.Error("a secret was edited")
	}
	// keys, blocked, without a host or reason
	u.st = &guiState{Approval: &guiApproval{Tab: "db-1", Host: "db-1", Cmd: "q", Keys: true, Blocked: true, Org: "Acme"}}
	h.frame()
	u.st = &guiState{Approval: &guiApproval{Tab: "db-1", Cmd: "uptime", Org: "Acme"}}
	h.frame()
	// another org's approval: nothing
	u.st = &guiState{Approval: &guiApproval{Tab: "x", Cmd: "ls", Org: "Globex"}}
	h.frame()
	if u.sheetSig != "" {
		t.Errorf("Globex's approval over Acme: sig %q", u.sheetSig)
	}
}

// TestApprovalText: the approval sheet's header for a command, keys, a
// secret, a blocked one and one without a host.
func TestApprovalText(t *testing.T) {
	for _, c := range []struct {
		a    guiApproval
		want [5]string
	}{
		{guiApproval{Tab: "t", Host: "web-01", Why: "[approvals] always asks for rm"},
			[5]string{"Claude wants to run a command", "changes the server", "on ", "web-01", "[approvals] always asks for rm"}},
		{guiApproval{Tab: "t", Host: "web-01", Keys: true}, [5]string{"Claude wants to press keys", "keystrokes", "in ", "web-01", ""}},
		{guiApproval{Tab: "t", Host: "web-01", Secret: true, Why: "Claude will see this password"},
			[5]string{"Claude wants to read a secret", "secret", "", "t", "Claude will see this password"}},
		{guiApproval{Tab: "t", Blocked: true}, [5]string{"Claude wants to run a command", "changes the server", "", "t", "Blocked by Claude Code's permission classifier"}},
	} {
		title, kind, place, where, why := approvalText(&c.a)
		if got := [5]string{title, kind, place, where, why}; got != c.want {
			t.Errorf("%+v: %q", c.a, got)
		}
	}
}

// TestPaletteScopes: the tabs are All and the kinds the items have (and the
// chosen one); tab and shift+tab cycle them, a click picks one, the list and
// enter work within it.
func TestPaletteScopes(t *testing.T) {
	items := []guiPal{{Kind: "hosts", Label: "a"}, {Kind: "actions", Label: "b"}, {Kind: "hosts", Label: "c"}}
	for _, c := range []struct {
		scope         string
		scopes, shown string
	}{
		{"", "[ hosts actions]", "[0 1 2]"},
		{"hosts", "[ hosts actions]", "[0 2]"},
		{"themes", "[ hosts actions themes]", "[]"},
	} {
		scopes, shown := palScopes(items, c.scope)
		if fmt.Sprint(scopes) != c.scopes || fmt.Sprint(shown) != c.shown {
			t.Errorf("%q: %q %v", c.scope, scopes, shown)
		}
	}
	u := wUI(t)
	u.st = &guiState{Pal: items, PalOpen: true}
	u.openPalette()
	wActs(u)
	h := newWH(t, u, func(gtx layout.Context) { u.sheets(gtx) })
	h.send(wPress(key.NameTab))
	if u.palScope != "hosts" {
		t.Fatalf("tab: scope %q", u.palScope)
	}
	h.send(wPress(key.NameDownArrow), wPress(key.NameDownArrow))
	if u.palSel != 1 {
		t.Errorf("down past the scope's end: %d", u.palSel)
	}
	h.send(wPress(key.NameTab))
	if u.palScope != "actions" || u.palSel != 0 {
		t.Errorf("tab again: scope %q sel %d", u.palScope, u.palSel)
	}
	h.send(key.Event{Name: key.NameTab, Modifiers: key.ModShift, State: key.Press})
	if u.palScope != "hosts" {
		t.Errorf("shift+tab: scope %q", u.palScope)
	}
	h.send(wPress(key.NameTab), wPress(key.NameTab))
	if u.palScope != "" {
		t.Errorf("tab wraps to All: scope %q", u.palScope)
	}
	h.clickBtn("palScopeactions")
	if u.palScope != "actions" || !h.r.Source().Focused(&u.palIn) {
		t.Errorf("click: scope %q", u.palScope)
	}
	h.send(wPress(key.NameReturn))
	if u.pal || len(wActs(u)) != 1 {
		t.Error("enter didn't pick the scope's item")
	}
	// a scope with nothing in it: enter picks nothing
	u.openPalette()
	wActs(u)
	u.palScope = "themes"
	h.frame()
	h.send(wPress(key.NameReturn))
	if !u.pal || len(wActs(u)) != 0 {
		t.Error("enter on an empty scope picked")
	}
	u.closePalette()
	u.openPalette()
	if u.palScope != "" {
		t.Error("reopened in a scope")
	}
}

// TestPalSpans: the matched runes are their own accent spans.
func TestPalSpans(t *testing.T) {
	var got []string
	for _, s := range palSpans("web-01", []int{0, 1, 2, 5}) {
		got = append(got, fmt.Sprintf("%s:%v", s.Content, s.Color == cAccText))
	}
	if fmt.Sprint(got) != "[web:true -0:false 1:true]" {
		t.Errorf("spans %v", got)
	}
	if s := palSpans("web", nil); len(s) != 1 || s[0].Color != cText {
		t.Errorf("no match: %+v", s)
	}
}

// TestPalInfo: the preview shows what the window has for a host, a session
// and an org, and the hint for anything else.
func TestPalInfo(t *testing.T) {
	u := wUI(t)
	u.st = &guiState{
		Tree: []guiNode{{Label: "Acme", Org: "Acme"},
			{Label: "web-01", Org: "Acme", Group: "api", Host: "web-01", Addr: "10.0.0.5", Dir: "/d", Alert: "CRITICAL", Note: "front"},
			{Label: "web-02", Org: "Acme", Group: "api", Host: "web-02", Dir: "/d", Alert: "WARNING"},
			{Label: "web-01", Org: "Acme", Group: "old", Host: "web-01", Dir: "/e"}},
		Tabs: []guiTab{{Name: "web-01", Org: "Acme", Group: "api", State: "working", Panes: []guiPane{{Name: "web-01", Dir: "/d"}, {Name: "db-1", Dir: "/d"}}},
			{Name: "Claude", Org: "Globex", Panes: []guiPane{{Name: "web-01", Dir: "/e"}}},
			{Name: "web-01 again", Org: "Globex", Panes: []guiPane{{Name: "web-01", Dir: "/d"}}}}}
	f := func(k, v string, mono bool, c color.NRGBA) palField { return palField{k, v, mono, c} }
	for _, c := range []struct {
		it    guiPal
		title string
		rows  []palField
	}{
		{guiPal{Kind: "hosts", Label: "Acme › api › web-01", Host: "web-01", Dir: "/d"}, "web-01", []palField{
			f("Address", "10.0.0.5", true, cText2), f("Group", "Acme / api", false, cText2), f("Alert", "critical", false, cRed),
			f("Notes", "front", false, cText2), f("Open tabs", "2", false, cText2)}},
		{guiPal{Kind: "hosts", Label: "Acme › api › web-02", Host: "web-02", Dir: "/d"}, "web-02", []palField{
			f("Group", "Acme / api", false, cText2), f("Alert", "warning", false, cOrange)}},
		{guiPal{Kind: "sessions", Label: "Acme › api › web-01", Tab: 0}, "web-01", []palField{
			f("Organization", "Acme", false, cText2), f("Group", "api", false, cText2), f("Panes", "web-01, db-1", false, cText2), f("State", "working", false, cText2)}},
		{guiPal{Kind: "sessions", Label: "gone", Hint: "tab 9", Tab: 8}, "gone", []palField{f("", "tab 9", false, cText2)}},
		{guiPal{Kind: "orgs", Label: "Acme", Hint: "Claude for the org"}, "Acme", []palField{f("Servers", "3", false, cText2), f("Open tabs", "1", false, cText2)}},
		{guiPal{Kind: "orgs", Label: "Initech", Hint: "Claude for the org"}, "Initech", []palField{f("", "Claude for the org", false, cText2)}},
		{guiPal{Kind: "actions", Label: "reload config", Hint: "desk.toml"}, "reload config", []palField{f("", "desk.toml", false, cText2)}},
	} {
		title, rows := u.palInfo(c.it)
		if title != c.title || !slices.Equal(rows, c.rows) {
			t.Errorf("%s %q: %q %+v", c.it.Kind, c.it.Label, title, rows)
		}
	}
}

// TestPaletteSheet: arrows move (clamped), the list follows the selection,
// typing filters the window's own commands, enter runs one, esc and the
// scrim close it, a click picks a model item.
func TestPaletteSheet(t *testing.T) {
	u := wUI(t)
	var opened []string
	old := openLocal
	openLocal = func(p string) error { opened = append(opened, p); return nil }
	t.Cleanup(func() { openLocal = old })
	var pal []guiPal
	for i := range 60 {
		kind := "hosts"
		if i >= 30 {
			kind = "actions"
		}
		pal = append(pal, guiPal{Kind: kind, Label: "web-" + string(rune('a'+i%26)), Hint: "acme.example.com"})
	}
	u.st = &guiState{Pal: pal, PalOpen: true}
	u.openPalette()
	m := &model{}
	if wRun(u, m); !u.d.palOpen || m.palQ != "" {
		t.Fatal("openPalette didn't tell the model")
	}
	h := newWH(t, u, func(gtx layout.Context) { u.sheets(gtx) })
	h.send(wPress(key.NameUpArrow), key.Event{Name: key.NameDownArrow, State: key.Release})
	if u.palSel != 0 {
		t.Errorf("up from the top, or a release moved: %d", u.palSel)
	}
	var downs []event.Event
	for range 40 {
		downs = append(downs, wPress(key.NameDownArrow))
	}
	h.send(downs...)
	if u.palSel != 40 || u.palList.Position.First == 0 || u.palShown != 40 {
		t.Fatalf("down 40: sel %d first %d", u.palSel, u.palList.Position.First)
	}
	if p := u.palList.Position; 42 < p.First || 42 >= p.First+p.Count { // item 40 is kid 42 (two headers)
		t.Errorf("selection out of view: %+v", p)
	}
	h.send(downs...)
	if u.palSel != 59 {
		t.Errorf("down past the end: %d", u.palSel)
	}
	var ups []event.Event
	for range 58 {
		ups = append(ups, wPress(key.NameUpArrow))
	}
	h.send(ups...)
	if u.palSel != 1 || u.palList.Position.First != 2 {
		t.Errorf("up to 1: sel %d first %d", u.palSel, u.palList.Position.First)
	}
	h.send(wPress(key.NameUpArrow))
	if u.palList.Position.First != 0 || u.palList.Position.Offset != 0 {
		t.Errorf("back to the top: %+v", u.palList.Position)
	}
	// a click on a model item
	u.wantTerm = false
	h.clickBtn("pal3")
	m = &model{}
	if u.pal || !u.wantTerm || wRun(u, m) != 1 {
		t.Error("click on item 3 didn't pick it")
	}
	// typing: the window's own commands
	u.openPalette()
	wActs(u)
	h.frame()
	h.send(key.EditEvent{Text: "recordings"})
	m = &model{}
	if u.palQ != "recordings" || u.palSel != 0 || wRun(u, m) != 1 || m.palQ != "recordings" {
		t.Fatalf("query %q sel %d model %q", u.palQ, u.palSel, m.palQ)
	}
	if g := u.guiPal(); len(g) != 1 || g[0].Label != "Open recordings" {
		t.Fatalf("guiPal %+v", g)
	}
	h.send(wPress(key.NameReturn))
	if want := filepath.Join(stateDir(), "recordings"); len(opened) != 1 || opened[0] != want || u.pal {
		t.Fatalf("opened %v, pal %v", opened, u.pal)
	}
	if fi, err := os.Stat(opened[0]); err != nil || !fi.IsDir() {
		t.Error("recordings dir not made")
	}
	// esc closes
	u.openPalette()
	h.frame()
	wActs(u)
	h.send(wPress(key.NameEscape))
	if u.pal || len(wActs(u)) != 1 {
		t.Error("esc left the palette open")
	}
	// the scrim closes it; an empty list says so
	u.openPalette()
	u.st = &guiState{PalOpen: true}
	h.frame()
	wActs(u)
	h.send(wClick(f32.Pt(5, 5))...)
	m = &model{}
	if u.pal || wRun(u, m) != 1 {
		t.Error("the scrim didn't close the palette")
	}
	// out of range picks nothing
	u.pal = true
	u.pick(-1, 3)
	u.pick(3, 3)
	if !u.pal || len(wActs(u)) != 0 {
		t.Error("an out-of-range pick ran")
	}
	if u.palQ = ""; len(u.guiPal()) != 0 {
		t.Error("no query lists the window's commands")
	}
}

// TestGuiCommands: each window command opens its folder (made first), or
// the directory split.
func TestGuiCommands(t *testing.T) {
	u := wUI(t)
	var opened []string
	old := openLocal
	openLocal = func(p string) error { opened = append(opened, p); return nil }
	t.Cleanup(func() { openLocal = old })
	for _, c := range guiCommands {
		c.run(u)
	}
	want := []string{filepath.Join(stateDir(), "notes"), filepath.Join(stateDir(), "recordings"), auditDir()}
	if strings.Join(opened, "|") != strings.Join(want, "|") {
		t.Errorf("opened %v, want %v", opened, want)
	}
	for _, d := range want {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("%s not made", d)
		}
	}
	if !u.toastBad || !strings.Contains(u.toast, "Open a server first") {
		t.Errorf("files without a server: toast %q", u.toast)
	}
	// through the palette: a query matching one command, picked by index
	u.palQ, u.pal = "server notes", true
	u.pick(0, 1)
	if u.pal || len(opened) != 4 || opened[3] != want[0] {
		t.Errorf("pick of the notes command: %v", opened)
	}
}

// TestToasts: flash and error lines show once each, "!" flashes as bad, and
// an expired toast stops asking for frames.
func TestToasts(t *testing.T) {
	u := wUI(t)
	gtx := layout.Context{Ops: new(op.Ops), Constraints: layout.Exact(image.Pt(800, 600)), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	u.st = &guiState{Flash: "!disk full"}
	u.toasts(gtx)
	if u.toast != "disk full" || !u.toastBad {
		t.Errorf("toast %q bad %v", u.toast, u.toastBad)
	}
	u.toast = ""
	u.toasts(gtx) // the same flash again: not shown again
	if u.toast != "" {
		t.Error("a flash showed twice")
	}
	u.st = &guiState{Flash: "✓ saved"}
	u.toasts(gtx)
	if u.toast != "saved" || u.toastBad {
		t.Errorf("toast %q bad %v", u.toast, u.toastBad)
	}
	u.st = &guiState{Flash: "✓ saved", Err: "connection refused"}
	u.toasts(gtx)
	if u.toast != "connection refused" || !u.toastBad {
		t.Errorf("err toast %q bad %v", u.toast, u.toastBad)
	}
	u.toastUntil = time.Now().Add(-time.Second)
	u.toasts(gtx) // expired: draws nothing
	if u.lastErr != "connection refused" || u.lastFlash != "✓ saved" {
		t.Error("last lines not kept")
	}
}

// TestGroupView: the group's sessions as cards (a click activates one), one
// line asks them all (not blank), esc leaves the view.
func TestGroupView(t *testing.T) {
	u := wUI(t)
	g := groupKey("/srv/acme", "web")
	u.st = &guiState{Group: g, Tabs: []guiTab{
		{Name: "web-1", Dir: "/srv/acme", Group: "web", State: "your turn",
			Chat: []guiChat{{Who: "you", Text: "is nginx up?"}, {Who: "claude", Text: "**Yes**, it is.\n\n```\nactive\n```"}},
			Panes: []guiPane{{Focused: false, Blocks: map[int]Block{1: {N: 1, Cmd: "hidden"}}},
				{Focused: true, Blocks: map[int]Block{1: {N: 1, Cmd: "ls"}, 2: {N: 2, Cmd: "false", Exit: 1}, 4: {N: 4, Cmd: "uptime"}}}}},
		{Name: "db-1", Dir: "/srv/globex", Group: "db", State: "idle"},
		{Name: "web-2", Dir: "/srv/acme", Group: "web", State: "strange"},
	}}
	focus := func(gtx layout.Context) { gtx.Execute(key.FocusCmd{Tag: &u.broadcastIn}) }
	h := newWH(t, u, func(gtx layout.Context) { u.groupView(gtx) })
	h.pre = focus
	h.frame()
	h.send(key.EditEvent{Text: "   "}, wPress(key.NameReturn))
	if len(wActs(u)) != 0 {
		t.Error("a blank line was asked")
	}
	u.broadcastIn.SetText("")
	h.send(key.EditEvent{Text: " uptime "}, wPress(key.NameReturn))
	if len(wActs(u)) != 1 || u.broadcastIn.Text() != "" {
		t.Errorf("enter: text %q", u.broadcastIn.Text())
	}
	h.clickBtn("bcast")
	if len(wActs(u)) != 0 {
		t.Error("Ask all with nothing typed asked")
	}
	u.broadcastIn.SetText("df -h")
	h.clickBtn("bcast")
	if len(wActs(u)) != 1 || u.broadcastIn.Text() != "" {
		t.Error("Ask all didn't ask")
	}
	h.pre = focus
	h.frame()
	h.send(wPress(key.NameEscape))
	m := &model{group: g}
	if wRun(u, m) != 1 || m.group != "" {
		t.Error("esc didn't leave the group view")
	}
	u.wantTerm = false
	h.clickBtn("card2")
	m = &model{tabs: []*Tab{{}, {}, {}}, group: g}
	if wRun(u, m) != 1 || m.active != 2 || m.group != "" || !u.wantTerm {
		t.Errorf("card click: active %d group %q", m.active, m.group)
	}
	u.st.Group = "nothing"
	h.frame() // no sessions: the hint
}

// TestPlainText: markdown down to one line, code left out.
func TestPlainText(t *testing.T) {
	if got := plainText("**Done**: restarted.\n\n```\nsecret code\n```\n\n- one\n- two"); got != "Done: restarted. one two" {
		t.Errorf("plainText %q", got)
	}
	if keyFocus(&struct{}{}).Tag == nil {
		t.Error("keyFocus lost its tag")
	}
}

// --- gui_select.go ---

func wSelSpans() []styledtext.SpanStyle {
	return []styledtext.SpanStyle{
		{Content: "Hello world from Acme", Size: 14, Color: cText, Font: sansFont},
		{Content: " and Globex", Size: 14, Color: cAccText, Font: fBold(sansFont)},
	}
}

// wAt is the middle of rune i's left half in st (rune i's own spot).
func wAt(st *selText, i int) f32.Point {
	for _, p := range st.pieces {
		if i >= p.at && i < p.at+len(p.xs)-1 {
			l := st.lines[p.line]
			x := p.x + p.xs[i-p.at] + (p.xs[i-p.at+1]-p.xs[i-p.at])/4
			return f32.Pt(float32(x), float32(l.top+l.h/2))
		}
	}
	panic("no rune")
}

// TestSelRichSelects: a drag selects and copies on release, a double click
// a word (also from the edge after it), a triple click the block; a press
// clears other selections; new text drops the selection, a longer one keeps it.
func TestSelRichSelects(t *testing.T) {
	u := wUI(t)
	spans := wSelSpans()
	h := newWH(t, u, func(gtx layout.Context) { u.selRich(gtx, "m1", spans...) })
	st := u.sels["m1"]
	if st == nil || string(st.src) != "Hello world from Acme and Globex" || len(st.lines) != 1 {
		t.Fatalf("laid out %+v", st)
	}
	tv := &termView{sel: [2]image.Point{{1, 1}, {3, 1}}}
	u.terms[&Term{}] = tv
	from, to := wAt(st, 6), wAt(st, 11) // "world"
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: from},
		pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: to},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: to})
	if got := st.selected(); got != "world" {
		t.Fatalf("drag selected %q", got)
	}
	if tv.sel != ([2]image.Point{}) || u.chatSel != st {
		t.Error("the press didn't clear the terminal's selection")
	}
	select {
	case s := <-u.clipOut:
		if s != "world" || u.toast != "Copied" {
			t.Errorf("copied %q toast %q", s, u.toast)
		}
	default:
		t.Error("nothing copied")
	}
	// a double click past "Hello"'s end: the word, not the space
	end := wAt(st, 5)
	end.X -= 2
	// a drag after the second press keeps the word
	far := wAt(st, 20)
	h.send(append(wClick(end),
		pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: end},
		pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: far},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: far})...)
	if got := st.selected(); got != "Hello" {
		t.Errorf("double click: %q", got)
	}
	time.Sleep(450 * time.Millisecond) // a new click count
	p := wAt(st, 13)
	h.send(append(append(wClick(p), wClick(p)...), wClick(p)...)...)
	if got := st.selected(); got != string(st.src) {
		t.Errorf("triple click: %q", got)
	}
	// a press on another block clears this one
	h2 := newWH(t, u, func(gtx layout.Context) { u.selLabel(gtx, "m2", "Initech", spans[0]) })
	time.Sleep(450 * time.Millisecond)
	h2.send(wClick(f32.Pt(2, 5))...)
	if st.sel != ([2]int{}) || u.chatSel != u.sels["m2"] {
		t.Error("the other block kept its selection")
	}
	// growing text keeps a selection, new text drops it
	st.sel = [2]int{0, 5}
	spans[1].Content += " too"
	h.frame()
	if st.selected() != "Hello" {
		t.Error("a longer answer dropped the selection")
	}
	spans[0].Content = "Bye"
	h.frame()
	if st.sel != ([2]int{}) {
		t.Error("new text kept the selection")
	}
	for len(u.clipOut) > 0 {
		<-u.clipOut
	}
	u.copyOnSelect("")
	select {
	case <-u.clipOut:
		t.Error("an empty selection was copied")
	default:
	}
}

// TestSelTextLayout: lines wrap between words, a newline breaks, a word
// wider than the line is cut, and hit finds the rune nearest a point.
func TestSelTextLayout(t *testing.T) {
	u := wUI(t)
	gtx := layout.Context{Ops: new(op.Ops), Constraints: layout.Constraints{Max: image.Pt(60, 1000)}, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	st := &selText{}
	spans := []styledtext.SpanStyle{{Content: "ab cd\nsupercalifragilistic x", Size: 14, Font: sansFont}}
	st.src = []rune(spans[0].Content)
	size := st.layout(gtx, u.sh, spans, &u.selGlyphs)
	if len(st.lines) < 4 || size.X > 60 {
		t.Fatalf("%d lines, size %v", len(st.lines), size)
	}
	if st.lines[1].at != 6 { // after "ab cd\n"
		t.Errorf("second line starts at %d", st.lines[1].at)
	}
	for _, p := range st.pieces {
		if p.x+p.xs[len(p.xs)-1] > 60+u.textW(gtx, " ", 14, sansFont) {
			t.Errorf("piece at %d overflows: %d", p.at, p.x+p.xs[len(p.xs)-1])
		}
	}
	if st.hit(image.Pt(0, -1)) != 0 || st.hit(image.Pt(0, size.Y+5)) != len(st.src) {
		t.Error("hit outside the text")
	}
	if i := st.hit(image.Pt(0, st.lines[1].top+1)); i != 6 {
		t.Errorf("start of line 2: %d", i)
	}
	if i := st.hit(image.Pt(59, st.lines[0].top+1)); i != 5 { // past "ab cd": its end
		t.Errorf("end of line 1: %d", i)
	}
	if (&selText{}).hit(image.Pt(3, 3)) != 0 {
		t.Error("hit without lines")
	}
	st.sel = [2]int{3, 1}
	if st.selected() != "b " {
		t.Errorf("backwards selection %q", st.selected())
	}
	st.sel = [2]int{1, 99}
	if st.selected() != string(st.src[1:]) {
		t.Error("selection past the end")
	}
}

func TestSelWordsAndRuneXs(t *testing.T) {
	if got := strings.Join(words("\na  b\nc"), "|"); got != "\n|a  |b|\n|c" {
		t.Errorf("words %q", got)
	}
	if got := strings.Join(words("a b  "), "|"); got != "a |b  " {
		t.Errorf("trailing spaces: %q", got)
	}
	gl := []text.Glyph{{Advance: fixed.I(10), Runes: 2}, {Advance: fixed.I(4)}, {Advance: fixed.I(6), Runes: 1}}
	for n, want := range map[int][]int{3: {0, 5, 10, 20}, 5: {0, 5, 10, 20, 20, 20}, 2: {0, 5, 20}} {
		if got := runeXs(gl, n); !equalInts(got, want) {
			t.Errorf("runeXs(%d) = %v, want %v", n, got, want)
		}
	}
	var c clicks
	if c.press(image.Pt(1, 1)) != 1 || c.press(image.Pt(3, 2)) != 2 || c.press(image.Pt(30, 2)) != 1 {
		t.Error("click counting")
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- gui_rich.go ---

func wGtx(w int) layout.Context {
	return layout.Context{Ops: new(op.Ops), Constraints: layout.Constraints{Max: image.Pt(w, 4000)}, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
}

func TestRichColors(t *testing.T) {
	if seriesColor(0) != cAcc || seriesColor(6) != cAcc || seriesColor(4) != cRed {
		t.Error("seriesColor")
	}
	for pct, want := range map[float64]color.NRGBA{0: cGreen, 69.9: cGreen, 70: cOrange, 89.9: cOrange, 90: cRed, 120: cRed} {
		if levelColor(pct) != want {
			t.Errorf("levelColor(%v)", pct)
		}
	}
}

// TestChartShape: which plot a chart takes, its value range and whether
// it's a percentage.
func TestChartShape(t *testing.T) {
	short := chart{labels: []string{"a", "b"}, series: []chartSeries{{vals: []float64{1, 2}}}}
	long := chart{labels: []string{"/var/lib/docker", "/home"}, series: []chartSeries{{vals: []float64{1, 2}}}}
	if short.useHorizontal() || !long.useHorizontal() {
		t.Error("labels decide horizontal")
	}
	if c := (chart{horizontal: true}); !c.useHorizontal() {
		t.Error("the horizontal keyword")
	}
	lined := long
	lined.series = []chartSeries{{vals: []float64{1}}, {line: true, vals: []float64{1}}}
	if lined.useHorizontal() {
		t.Error("a line series went horizontal")
	}
	many := long
	many.labels = make([]string, 17)
	for i := range many.labels {
		many.labels[i] = "host-number-" + string(rune('a'+i))
	}
	if many.useHorizontal() {
		t.Error("17 long labels went horizontal")
	}
	if lo, hi := (&chart{series: []chartSeries{{vals: []float64{3, math.NaN(), -2, 8}}}}).bounds(); lo != -2 || hi != 8 {
		t.Errorf("bounds %v %v", lo, hi)
	}
	if lo, hi := (&chart{series: []chartSeries{{vals: []float64{3}}}}).bounds(); lo != 0 || hi != 3 {
		t.Errorf("bounds from zero %v %v", lo, hi)
	}
	if lo, hi := (&chart{ranged: true, lo: 10, hi: 20, series: []chartSeries{{vals: []float64{99}}}}).bounds(); lo != 10 || hi != 20 {
		t.Error("the y-axis range")
	}
	one := []chartSeries{{}}
	for c, want := range map[*chart]bool{
		{ranged: true, lo: 0, hi: 100, series: one}:                     true,
		{ranged: true, lo: 0, hi: 100, series: []chartSeries{{}, {}}}:   false,
		{ranged: true, lo: 1, hi: 100, series: one}:                     false,
		{ranged: true, lo: 0, hi: 99, series: one}:                      false,
		{ranged: false, lo: 0, hi: 100, series: one}:                    false,
		{title: "Disk use (%)", series: one}:                            true,
		{yTitle: "% used", series: one}:                                 true,
		{title: "Requests", yTitle: "count", series: []chartSeries{{}}}: false,
	} {
		if c.percent() != want {
			t.Errorf("percent %+v = %v", *c, !want)
		}
	}
}

// TestChartDraws: each kind of chart lays out at the width given, with the
// height its rows need.
func TestChartDraws(t *testing.T) {
	u := wUI(t)
	parse := func(src string) *chart {
		c, ok := parseChart(src)
		if !ok {
			t.Fatalf("not a chart: %s", src)
		}
		return &c
	}
	nan := math.NaN()
	charts := map[string]*chart{
		"xy":  parse("xychart-beta\n title \"Load\"\n x-axis [mon, tue, wed]\n bar \"web\" [1, -2, 3]\n bar [2, 1, x]\n line \"avg\" [1.5, x, 2, 9]"),
		"pct": {labels: []string{"a", "b", "c", "d"}, ranged: true, lo: 0, hi: 100, series: []chartSeries{{vals: []float64{20, 75, 95}}}},
		"one": {labels: []string{"x"}, series: []chartSeries{{vals: []float64{-5}}}},
		"dense": {labels: []string{"label-one", "label-two", "label-three", "label-four", "label-five"},
			series: []chartSeries{{line: true, vals: []float64{1, 2, nan, 4, 5, 6}}}},
		"hbar": {title: "Disk %", labels: []string{"/var/lib/docker", "/home", "/"}, series: []chartSeries{{vals: []float64{91, 72, 0}}}},
		"hbar2": {horizontal: true, labels: []string{"/var/lib/docker", "/home", "/"},
			series: []chartSeries{{name: "used", vals: []float64{40, 3}}, {vals: []float64{nan, 5, 6}}}},
		"hranged": {horizontal: true, ranged: true, lo: 0, hi: 50, labels: []string{"a"}, series: []chartSeries{{vals: []float64{25}}}},
		"pie":     parse("pie title Pets\n \"Dogs\" : 30\n \"Cats\" : 10"),
		"pie1":    parse("pie\n \"All\" : 5"),
	}
	for name, c := range charts {
		d := u.chart(wGtx(600), c)
		if d.Size.X != 600 || d.Size.Y <= 24 {
			t.Errorf("%s: %v", name, d.Size)
		}
	}
	// the plots alone
	if d := u.xyChart(wGtx(500), charts["xy"]); d.Size != image.Pt(500, 200) {
		t.Errorf("xy %v", d.Size)
	}
	if d := u.barsH(wGtx(500), charts["hbar"]); d.Size != image.Pt(500, 3*24) {
		t.Errorf("one series: 24 per row: %v", d.Size)
	}
	if d := u.barsH(wGtx(500), charts["hbar2"]); d.Size != image.Pt(500, 3*(7*2+4+14)) {
		t.Errorf("two series: %v", d.Size)
	}
	if d := u.pieChart(wGtx(500), charts["pie"]); d.Size != image.Pt(500, 140) {
		t.Errorf("pie %v", d.Size)
	}
	many := &chart{pie: true, series: []chartSeries{{vals: make([]float64, 10)}}}
	for i := range 10 {
		many.labels = append(many.labels, "slice")
		many.series[0].vals[i] = 1
	}
	if d := u.pieChart(wGtx(500), many); d.Size.Y != 10*20 { // the legend is taller than the donut
		t.Errorf("tall legend %v", d.Size)
	}
}

func TestRichTableCells(t *testing.T) {
	for s, want := range map[string]bool{"12.5 GiB": true, "$1,200": true, "~3ms": true, "-4": true, "99%": true, "web-1": false, "v1.2": false} {
		if numCell.MatchString(s) != want {
			t.Errorf("numCell %q", s)
		}
	}
	if m := pctCell.FindStringSubmatch("45.5 %"); m == nil || m[1] != "45.5" {
		t.Errorf("pctCell %v", m)
	}
	if plain([]mdSpan{{text: " a"}, {text: "b "}}) != "ab" {
		t.Error("plain")
	}
}

// TestTableDraws: a table is as tall as its rows and as wide as the panel;
// no columns draw nothing.
func TestTableDraws(t *testing.T) {
	u := wUI(t)
	cell := func(s string) []mdSpan { return []mdSpan{{text: s}} }
	b := mdBlock{kind: "table", rows: [][][]mdSpan{
		{cell("Host"), cell("Disk"), cell("Note"), cell("Mid")},
		{cell("web-1"), cell("45%"), cell("ok"), cell("a")},
		{cell("db-1"), cell("0%"), cell("—"), cell("b")},
		{cell("cache"), cell("120%")},
		{cell("x"), cell("-"), cell(strings.Repeat("a very long note ", 40)), cell("c")},
	}, align: []east.Alignment{east.AlignNone, east.AlignNone, east.AlignLeft, east.AlignCenter}}
	d := u.table(wGtx(700), b)
	if d.Size.X != 700 || d.Size.Y < 5*(14+14) {
		t.Errorf("table %v", d.Size)
	}
	if d := u.table(wGtx(700), mdBlock{rows: [][][]mdSpan{{}}}); d.Size != (image.Point{}) {
		t.Errorf("no columns: %v", d.Size)
	}
}

// TestDiagramAndFigureSpans: line art dims its lines, words stay bright;
// figure marks take their role's color, strong is bold.
func TestDiagramAndFigureSpans(t *testing.T) {
	got := diagramSpans("ab ─→ c\n▲")
	type sp struct {
		s string
		c color.NRGBA
	}
	want := []sp{{"ab ", cText}, {"─→ ", cAccText}, {"c\n", cText}, {"▲", cAccText}}
	if len(got) != len(want) {
		t.Fatalf("diagram spans %+v", got)
	}
	for i, w := range want {
		if got[i].Content != w.s || got[i].Color != w.c || got[i].Font != monoFont {
			t.Errorf("span %d = %q %v", i, got[i].Content, got[i].Color)
		}
	}
	fs := figureSpans("x {warn:bad} {strong:B}{ok:} y\n\n")
	wantF := []sp{{"x ", cText2}, {"bad", cRed}, {" ", cText2}, {"B", cText}, {" y", cText2}}
	if len(fs) != len(wantF) {
		t.Fatalf("figure spans %+v", fs)
	}
	for i, w := range wantF {
		if fs[i].Content != w.s || fs[i].Color != w.c {
			t.Errorf("figure span %d = %q", i, fs[i].Content)
		}
	}
	if fs[3].Font.Weight < font.SemiBold || fs[1].Font.Weight >= font.SemiBold {
		t.Error("strong isn't bold, or warn is")
	}
	grid := artGrid(fs)
	if len(grid) != 1 || string(runesOf(grid[0])) != "x bad B y" || !grid[0][6].bold || grid[0][0].bold || grid[0][2].c != cRed {
		t.Errorf("art grid %+v", grid)
	}
}

// TestArtBlock: a text picture is as tall as its lines (trailing blank
// ones dropped) and keeps one sideways list per block.
func TestArtBlock(t *testing.T) {
	u := wUI(t)
	art := "┌──┐ ╔═╗\n│ok│→├┼┤ ◀ ▶\n└──┘ ↑ ↓ ▼\n{dim:x}\n"
	spans := append(diagramSpans(art), figureSpans("{strong:Acme} plain")...)
	spans = append(spans, styledtext.SpanStyle{Content: "\n\n\n"}) // blank lines at the end don't count
	d := u.artBlock(wGtx(400), "a1", spans)
	if d.Size.Y != 5*18+20 || d.Size.X != 400 {
		t.Errorf("art block %v", d.Size)
	}
	l := u.hlists["a1"]
	if l == nil || l.Axis != layout.Horizontal {
		t.Fatal("no sideways list")
	}
	u.artBlock(wGtx(400), "a1", spans)
	if u.hlists["a1"] != l {
		t.Error("the list wasn't kept")
	}
}

// --- gui_camera.go ---

// TestToI420: BT.601 limited range, chroma from each block's top-left pixel.
func TestToI420(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for i := range img.Pix {
		img.Pix[i] = 0xff // white
	}
	copy(img.Pix[0:4], []byte{255, 0, 0, 255}) // red at (0,0)
	out := make([]byte, 4*2*3/2)
	toI420(img, out)
	if out[0] != 82 || out[1] != 235 || out[7] != 235 {
		t.Errorf("Y %v", out[:8])
	}
	if u, v := out[8:10], out[10:12]; u[0] != 90 || v[0] != 240 || u[1] != 128 || v[1] != 128 {
		t.Errorf("U %v V %v", u, v)
	}
}

// wFakeBins puts fake programs first in PATH: name → shell body.
func wFakeBins(t *testing.T, bins map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range bins {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// wTerm is a Term showing what the escape sequences s draw.
func wTerm(w, h int, s string) *Term {
	tm := &Term{emu: vt.NewEmulator(w, h)}
	tm.emu.Write([]byte(s))
	return tm
}

// TestCamRender: the camera frame redraws only when the screen changes,
// fits the font to the terminal, draws glyphs, backgrounds and the cursor.
func TestCamRender(t *testing.T) {
	// fontconfig "finds" a JetBrains file that isn't a font: Go Mono instead
	bad := filepath.Join(t.TempDir(), "JetBrainsMono-Regular.ttf")
	os.WriteFile(bad, []byte("not a font"), 0o644)
	wFakeBins(t, map[string]string{"fc-match": `printf '%s' "` + bad + `"`})
	if fs := camFonts(); fs[0] == nil || fs[1] == nil {
		t.Fatal("no camera fonts")
	}
	img := image.NewRGBA(image.Rect(0, 0, 320, 120))
	var r camRender
	if !r.draw(img, nil) {
		t.Fatal("no terminal: not cleared")
	}
	if img.RGBAAt(5, 5) != color.RGBA(cPane) {
		t.Error("not cleared to the pane color")
	}
	if r.draw(img, nil) {
		t.Error("still no terminal: nothing changed, yet it redrew")
	}
	tm := wTerm(20, 4, "\x1b[1mAB\x1b[0m \x1b[7mR\x1b[0m \x1b[2mF\x1b[0m \x1b[41mbg\x1b[0m 世\r\nline")
	if !r.draw(img, tm) {
		t.Fatal("a terminal didn't draw")
	}
	if r.cols != 20 || r.rows != 4 || r.adv*20 > 320 || r.lh*4 > 120 || r.adv == 0 {
		t.Errorf("fit %d×%d adv %d lh %d", r.cols, r.rows, r.adv, r.lh)
	}
	if r.draw(img, tm) {
		t.Error("an unchanged screen redrew")
	}
	ox, oy := (320-r.adv*20)/2, (120-r.lh*4)/2
	_, _, cx, cy, cursor := tm.Grid(0)
	if !cursor || cy != 1 {
		t.Fatalf("cursor %v at %d,%d", cursor, cx, cy)
	}
	if got := img.RGBAAt(ox+cx*r.adv+r.adv/2, oy+cy*r.lh+r.lh/2); got != color.RGBA(cAcc) {
		t.Errorf("cursor cell %v", got)
	}
	inked := false // "A" in row 0, cell 0
	for y := oy; y < oy+r.lh; y++ {
		for x := ox; x < ox+r.adv; x++ {
			inked = inked || img.RGBAAt(x, y) != color.RGBA(cPane)
		}
	}
	if !inked {
		t.Error("no glyph drawn")
	}
	if g := r.glyph(camGlyph{r: 'A', bold: true}); g.m == nil || r.glyph(camGlyph{r: 'A', bold: true}) != g {
		t.Error("bold glyph not cached")
	}
	tm.emu.Write([]byte("x"))
	if !r.draw(img, tm) {
		t.Error("a change didn't redraw")
	}
	r.fit(1000, 1, image.Pt(100, 10)) // can't fit: the smallest size tried
	if r.adv == 0 || r.adv*1000 <= 100 {
		t.Errorf("tiny fit adv %d lh %d", r.adv, r.lh)
	}
}

// TestCamLoop: frames go out only while somebody watches, until stop.
func TestCamLoop(t *testing.T) {
	stop := make(chan struct{})
	var mu sync.Mutex
	asked, pushed := 0, 0
	var first []byte
	done := make(chan struct{})
	go camLoop(stop, func() bool {
		mu.Lock()
		defer mu.Unlock()
		asked++
		return asked > 1
	}, func(b []byte) {
		mu.Lock()
		defer mu.Unlock()
		if pushed == 0 {
			first = append([]byte(nil), b...)
		}
		pushed++
	}, func() { close(done) })
	for end := time.Now().Add(testwait.Long); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		mu.Lock()
		n := pushed
		mu.Unlock()
		if n > 0 {
			break
		}
	}
	close(stop)
	<-done
	mu.Lock()
	defer mu.Unlock()
	y := byte((66*int(cPane.R)+129*int(cPane.G)+25*int(cPane.B)+128)>>8 + 16) // nothing to show: the pane color
	if pushed == 0 || pushed >= asked || len(first) != camW*camH*3/2 || first[0] != y {
		t.Errorf("asked %d pushed %d, first Y %v", asked, pushed, first[:1])
	}
}

// wCamDev makes a fake v4l2loopback tree under camRoot.
func wCamDev(t *testing.T, names map[string]string, dev string) string {
	t.Helper()
	root := t.TempDir()
	for v, name := range names {
		d := filepath.Join(root, "sys/devices/virtual/video4linux", v)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "name"), []byte(name+"\n"), 0o644)
	}
	if dev != "" {
		os.MkdirAll(filepath.Join(root, "dev"), 0o755)
		os.WriteFile(filepath.Join(root, "dev", dev), nil, 0o644)
	}
	old := camRoot
	camRoot = root
	t.Cleanup(func() { camRoot = old })
	return root
}

// wRealIoctl is camIoctl before any test replaced it.
var wRealIoctl = camIoctl

// wShareRules: Hyprland's rules go out once per process, so only the first
// run of TestShareToggleHyprland (-count) sees them.
var wShareRules = true

func wIoctl(t *testing.T, e syscall.Errno, seen *[208]byte) {
	old := camIoctl
	camIoctl = func(fd, req uintptr, arg unsafe.Pointer) syscall.Errno {
		if req != 0xc0d05605 {
			t.Errorf("ioctl %#x", req)
		}
		if seen != nil {
			*seen = *(*[208]byte)(arg)
		}
		return e
	}
	t.Cleanup(func() { camIoctl = old })
}

// TestCamV4L2: no device, desk's own device picked, open and format errors,
// and the I420 1280×720 format set on success.
func TestCamV4L2(t *testing.T) {
	wCamDev(t, nil, "")
	if _, _, err := camV4L2(); !errors.Is(err, errNoLoopback) {
		t.Errorf("no device: %v", err)
	}
	wCamDev(t, map[string]string{"video0": "Globex cam", "video3": "desk loop"}, "")
	if _, _, err := camV4L2(); err == nil || !strings.Contains(err.Error(), "video3") {
		t.Errorf("missing /dev node: %v", err)
	}
	wCamDev(t, map[string]string{"video0": "Globex cam", "video3": "desk cam"}, "video3")
	wIoctl(t, syscall.ENOTTY, nil)
	if _, _, err := camV4L2(); err == nil || !strings.Contains(err.Error(), "set format") || !errors.Is(err, syscall.ENOTTY) {
		t.Errorf("format refused: %v", err)
	}
	wCamDev(t, map[string]string{"video0": "Globex cam", "video3": "desk loop"}, "video3")
	var fm [208]byte
	wIoctl(t, 0, &fm)
	f, name, err := camV4L2()
	if err != nil || name != "desk loop" || filepath.Base(f.Name()) != "video3" {
		t.Fatalf("open: %v %q", err, name)
	}
	f.Close()
	le := binary.NativeEndian
	if le.Uint32(fm[0:]) != 2 || le.Uint32(fm[8:]) != camW || le.Uint32(fm[12:]) != camH || le.Uint32(fm[16:]) != 0x32315559 || le.Uint32(fm[28:]) != camW*camH*3/2 {
		t.Errorf("format %v", fm[:36])
	}
	// the real ioctl on a plain file: refused, nothing opened
	tmp, _ := os.CreateTemp(t.TempDir(), "notvideo")
	if e := wRealIoctl(tmp.Fd(), 0xc0d05605, unsafe.Pointer(&fm[0])); e != syscall.ENOTTY {
		t.Errorf("ioctl on a file: %v", e)
	}
	tmp.Close()
	wCamDev(t, map[string]string{"video1": "Globex cam"}, "video1") // none is desk's: the first
	if _, name, err := camV4L2(); err != nil || name != "Globex cam" {
		t.Errorf("first device: %v %q", err, name)
	}
}

// TestCameraToggle: PipeWire unreachable, PipeWire only (with or without a
// loopback error), and the loopback device getting frames until off.
func TestCameraToggle(t *testing.T) {
	defer camTerm.Store(nil)
	oldPW := camPipeWire
	t.Cleanup(func() { camPipeWire = oldPW })
	freed := make(chan struct{}, 4)
	camPipeWire = func() (func() bool, func([]byte), func(), bool) { return nil, nil, nil, false }
	wCamDev(t, nil, "")
	if msg := cameraToggle(); msg != "terminal camera: can't reach PipeWire" || cameraOn() {
		t.Fatalf("no PipeWire: %q", msg)
	}
	camPipeWire = func() (func() bool, func([]byte), func(), bool) {
		return func() bool { return false }, func([]byte) {}, func() { freed <- struct{}{} }, true
	}
	if msg := cameraToggle(); msg != `terminal camera on (PipeWire only): pick "desk terminal" as the camera in the call` || !cameraOn() {
		t.Fatalf("PipeWire: %q", msg)
	}
	if msg := cameraToggle(); msg != "terminal camera off" || cameraOn() {
		t.Fatalf("off: %q", msg)
	}
	<-freed
	wCamDev(t, map[string]string{"video2": "desk loop"}, "video2")
	wIoctl(t, syscall.EINVAL, nil)
	if msg := cameraToggle(); !strings.Contains(msg, "(PipeWire only)") || !strings.Contains(msg, "; v4l2loopback: ") {
		t.Fatalf("loopback refused: %q", msg)
	}
	cameraToggle()
	<-freed
	root := wCamDev(t, map[string]string{"video2": "desk loop"}, "video2")
	wIoctl(t, 0, nil)
	msg := cameraToggle()
	if !strings.Contains(msg, `pick "desk loop"`) || !strings.Contains(msg, "video2") {
		t.Fatalf("loopback: %q", msg)
	}
	dev := filepath.Join(root, "dev", "video2")
	for end := time.Now().Add(testwait.Long); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if fi, _ := os.Stat(dev); fi != nil && fi.Size() > 0 {
			break
		}
	}
	cameraToggle()
	if fi, _ := os.Stat(dev); fi == nil || fi.Size() < camW*camH*3/2 {
		t.Error("no frame reached the device")
	}
}

// --- gui_share.go ---

// wHypr fakes hyprctl: it logs its arguments, prints mons for monitors,
// and fails "output create" when failCreate.
func wHypr(t *testing.T, mons string, failCreate bool) (log func() string, setMons func(string)) {
	t.Helper()
	dir := t.TempDir()
	logf, monf := filepath.Join(dir, "log"), filepath.Join(dir, "mons")
	os.WriteFile(monf, []byte(mons), 0o644)
	fail := ""
	if failCreate {
		fail = `[ "$1 $2" = "output create" ] && exit 1`
	}
	wFakeBins(t, map[string]string{"hyprctl": `echo "$*" >> "` + logf + `"
` + fail + `
[ "$1" = monitors ] && cat "` + monf + `"
exit 0`})
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "fake")
	return func() string { b, _ := os.ReadFile(logf); return string(b) },
		func(s string) { os.WriteFile(monf, []byte(s), 0o644) }
}

// wShareWin records the windows shareToggle would open.
func wShareWin(t *testing.T) chan int {
	ch := make(chan int, 4)
	old := shareWin
	shareWin = func(stop chan struct{}, scale int) { ch <- scale }
	t.Cleanup(func() { shareWin = old })
	return ch
}

func wWait(t *testing.T, ch chan int, d time.Duration) int {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(testwait.Long):
		t.Fatal("no share window")
	}
	return 0
}

// TestShareToggleWindow: off Hyprland the window itself is shared.
func TestShareToggleWindow(t *testing.T) {
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	wins := wShareWin(t)
	if msg := shareToggle(); msg != `terminal screen on: share the "desk terminal share" window` || !shareOn() {
		t.Fatalf("on: %q", msg)
	}
	if wWait(t, wins, 5*time.Second) != 2 {
		t.Error("scale")
	}
	if msg := shareToggle(); msg != "terminal screen off" || shareOn() {
		t.Fatalf("off: %q", msg)
	}
	shareQuit() // off: nothing to remove
	shareRemove()
}

// TestShareToggleHyprland: the rules once, a headless monitor at the
// biggest whole scale that divides 1080 (not its own), the window once the
// monitor took it; without an output, the window; the monitor removed on
// quit with focus back.
func TestShareToggleHyprland(t *testing.T) {
	mons := `[{"Name":"DP-1","Focused":true,"X":0,"Scale":1.5},{"Name":"eDP-1","X":1920,"Scale":7},{"Name":"DESK-SHARE","X":0,"Scale":3}]`
	log, setMons := wHypr(t, mons, true)
	wins := wShareWin(t)
	msg := shareToggle()
	if !strings.HasPrefix(msg, `terminal screen on: share the "desk terminal share" window (no Hyprland output: `) || wWait(t, wins, 5*time.Second) != 2 {
		t.Fatalf("no output: %q", msg)
	}
	if l := log(); wShareRules && (!strings.Contains(l, "hl.workspace_rule") || !strings.Contains(l, "hl.window_rule")) || shareFrom != "DP-1" {
		t.Errorf("rules: %s, from %q", l, shareFrom)
	}
	wShareRules = false
	shareToggle()

	log, setMons = wHypr(t, mons, false)
	if msg := shareToggle(); msg != `terminal screen on: share the "DESK-SHARE" screen` {
		t.Fatalf("monitor: %q", msg)
	}
	if wWait(t, wins, 10*time.Second) != 2 {
		t.Error("scale")
	}
	l := log()
	if !strings.Contains(l, "output create headless DESK-SHARE") || strings.Contains(l, "workspace_rule") ||
		!strings.Contains(l, `hl.monitor{ output = "DESK-SHARE", mode = "1920x1080@30", position = "-20000x-20000", scale = 2 }`) {
		t.Errorf("log %s", l)
	}
	if n := strings.Count(l, "monitors -j"); n < 31 { // the monitor never took scale 2: waited it out
		t.Errorf("%d monitor polls", n)
	}
	shareToggle()

	log, _ = wHypr(t, `[{"Name":"DP-1","Focused":true,"Scale":2},{"Name":"DESK-SHARE","X":-20000,"Scale":2}]`, false)
	shareToggle()
	if wWait(t, wins, 10*time.Second) != 2 {
		t.Error("scale")
	}
	if n := strings.Count(log(), "monitors -j"); n > 4 {
		t.Errorf("the monitor was ready, yet %d polls", n)
	}
	shareQuit() // still on: the monitor goes, DP-1 gets focus back
	if l := log(); !strings.Contains(l, "output remove DESK-SHARE") || !strings.Contains(l, `hl.dsp.focus({ monitor = "DP-1" })`) {
		t.Errorf("quit: %s", l)
	}
	shareToggle()

	// focus on the share monitor: back to where sharing started; nowhere known: no focus
	log, setMons = wHypr(t, `[{"Name":"DESK-SHARE","Focused":true}]`, false)
	shareFrom = "HDMI-A-1"
	shareRemove()
	if l := log(); !strings.Contains(l, `monitor = "HDMI-A-1"`) {
		t.Errorf("back to the start: %s", l)
	}
	setMons("not json")
	shareFrom = ""
	shareRemove()
	if l := log(); strings.Count(l, "dispatch") != 1 || strings.Count(l, "output remove") != 2 {
		t.Errorf("nowhere to go back: %s", l)
	}
	if _, ok := hyprMonitor("DESK-SHARE"); ok {
		t.Error("a monitor from bad JSON")
	}
	_ = setMons
}

// cameraPalC: the palette's stop items stop a running camera and share
// (gui_camera_other_test.go has the rest of the world's).
func cameraPalC(t *testing.T, m *model) {
	camMu.Lock()
	camStop = make(chan struct{})
	camMu.Unlock()
	palPickC(t, m, "stop the terminal camera", "stop the terminal camera")
	if m.flash != "terminal camera off" || cameraOn() {
		t.Errorf("camera: %q", m.flash)
	}
	shareMu.Lock()
	shareStop = make(chan struct{})
	shareMu.Unlock()
	palPickC(t, m, "stop sharing the terminal as a screen", "stop sharing the terminal as a screen")
	if m.flash != "terminal screen off" || shareOn() {
		t.Errorf("share: %q", m.flash)
	}
}
