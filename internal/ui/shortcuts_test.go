package ui

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gioui.org/io/key"
	"gioui.org/io/pointer"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/keys"
	"github.com/lfdominguez/desk/internal/orgs"
)

// shortcutsPage opens Settings on the Keyboard shortcuts page, from the
// palette as a user would.
func (h *harness) shortcutsPage() *cfgDialog {
	h.t.Helper()
	h.t.Cleanup(func() { keys.Load(nil) })
	h.send(press("K", key.ModShortcut))
	h.palGo("keyboard shortcuts", "act:keys")
	d := h.u.cfg
	if d == nil || d.org != cfgKeys {
		h.t.Fatalf("the palette opened %+v", d)
	}
	return d
}

// TestShortcutsPage: Change records a key; Shift alone and a bare letter
// are refused with why; esc cancels without closing Settings; a clash in
// the scope is named and keeps Save from saving; Ctrl+S saves the
// rebinding to settings.toml, which the board then answers, and the key caps
// read; Reset puts the default back.
func TestShortcutsPage(t *testing.T) {
	h := newHarness(t)
	h.size.Y = 2400 // every row on screen
	d := h.shortcutsPage()

	h.clickButton("keyChangeboard.new")
	if d.rec != "board.new" {
		t.Fatalf("Change didn't record: %q", d.rec)
	}
	h.send(press("F6", key.ModShift))
	if d.rec != "board.new" || !strings.Contains(d.recErr, "Shift needs Ctrl or Alt") {
		t.Fatalf("Shift+F6: rec %q, %q", d.rec, d.recErr)
	}
	h.send(press("2", key.ModShift)) // @ on many layouts
	if d.rec != "board.new" || !strings.Contains(d.recErr, "needs Ctrl or Alt") {
		t.Fatalf("Shift+2: %q", d.recErr)
	}
	h.send(press(key.NameEscape, 0))
	if h.u.cfg == nil || d.rec != "" || d.recErr != "" || len(d.kdraft) != 0 {
		t.Fatalf("esc: cfg %v, rec %q, draft %v", h.u.cfg != nil, d.rec, d.kdraft)
	}

	h.clickButton("keyChangeboard.new")
	h.send(press("Z", key.ModShortcut)) // undo's, in the same scope
	if d.rec != "" || d.kdraft["board.new"] == nil || d.clash(keys.Get("board.new")).ID != "undo" {
		t.Fatalf("a clash wasn't drafted and named: %v", d.kdraft)
	}
	h.send(press("S", key.ModShortcut))
	if h.u.cfg == nil || !strings.Contains(d.err, "runs both") || keys.Changed("board.new") {
		t.Fatalf("Save with a clash: %q", d.err)
	}

	d.err = ""
	h.clickButton("keyChangeboard.new")
	if d.rec != "board.new" {
		t.Fatal("Change again")
	}
	h.send(press("S", key.ModShortcut)) // recorded, not Save
	if h.u.cfg == nil || d.kdraft["board.new"] == nil || *d.kdraft["board.new"] != (keys.Chord{Name: "S", Mods: key.ModShortcut}) {
		t.Fatalf("Ctrl+S while recording: %v", d.kdraft)
	}
	h.clickButton("keyChangeboard.new")
	h.send(press("B", key.ModAlt))
	if d.total() != 1 {
		t.Fatalf("%d changes", d.total())
	}
	h.send(press("S", key.ModShortcut))
	if h.u.cfg != nil {
		t.Fatalf("Save kept Settings: %q", d.err)
	}
	if keys.Cap("board.new") != "Alt+B" || orgs.Keys()["board.new"] != "Alt+B" {
		t.Fatalf("saved: %q, file %v", keys.Cap("board.new"), orgs.Keys())
	}
	if h.u.toast != "Saved the keyboard shortcuts" {
		t.Errorf("toast %q", h.u.toast)
	}

	h.send(press("N", key.ModShortcut))
	if h.u.edit != nil {
		t.Fatal("Ctrl+N still makes a card")
	}
	h.send(press("B", key.ModAlt))
	if h.u.edit == nil {
		t.Fatal("Alt+B doesn't make a card")
	}
	h.send(press(key.NameEscape, 0))

	keys.Load(nil) // a restart: the file gives it back
	h.u.loadKeys()
	if keys.Cap("board.new") != "Alt+B" {
		t.Fatal("the rebinding didn't survive a restart")
	}

	d = h.shortcutsPage()
	h.clickButton("keyResetboard.new")
	if c, ok := d.kdraft["board.new"]; !ok || c != nil || d.keysOf("board.new")[0] != (keys.Chord{Name: "N", Mods: key.ModShortcut}) {
		t.Fatalf("Reset: %v", d.kdraft)
	}
	h.clickButton("cfgSave")
	if keys.Changed("board.new") || len(orgs.Keys()) != 0 {
		t.Fatalf("Reset saved: %v", orgs.Keys())
	}
}

// TestShortcutsPageDraws: rows show their keys ("or" between defaults),
// fixed ones say so; the row recording says "Press a key…" and Cancel; a
// refusal, an edit and a clash are named under their row; a bare letter is
// refused where text is typed, and keeps the row recording.
func TestShortcutsPageDraws(t *testing.T) {
	h := newHarness(t)
	h.size.Y = 9000 // the list lays out only the rows it shows: all of them
	d := h.shortcutsPage()
	has := func(s string) bool {
		for _, l := range h.labels() {
			if l.text == s {
				return true
			}
		}
		return false
	}
	if !has("fixed") || !has("or") || !has("Ctrl+N") || !has("Change") || has("Press a key…") || has("Cancel") {
		t.Fatal("the page's rows")
	}
	h.clickButton("keyChangeboard.new")
	if !has("Press a key…") || !has("Cancel") {
		t.Fatal("the recording row")
	}
	ors := 0 // "or" goes between a binding's keys: once per binding with two
	for _, b := range keys.All {
		if len(b.Def) == 2 {
			ors++
		}
	}
	if n := h.count("or", image.Rect(0, 0, h.size.X, h.size.Y)); n != ors {
		t.Errorf("%d \"or\", want %d", n, ors)
	}
	h.send(press("Q", 0))
	if d.rec != "board.new" || h.count("Q: a key that types needs Ctrl or Alt here", image.Rect(0, 0, h.size.X, h.size.Y)) != 1 {
		t.Fatalf("a bare Q, named once under its row: rec %q, %q", d.rec, d.recErr)
	}
	h.clickButton("keyChangeboard.new") // Cancel
	if d.rec != "" || has("Press a key…") {
		t.Fatal("Cancel")
	}
	h.clickButton("keyChangedraw.select")
	h.send(press("V", 0)) // one of its two defaults: now its only key
	if d.kdraft["draw.select"] == nil || !has("edited") {
		t.Fatalf("V alone wasn't drafted: %v", d.kdraft)
	}
	h.clickButton("keyChangeboard.new")
	h.send(press("Z", key.ModShortcut))
	if !has("also runs “Undo an archive or a delete, while its toast shows” — change one") {
		t.Fatal("the clash isn't named")
	}
}

// TestShortcutsResetAll: Reset all drafts every rebinding back, and one
// drafted to what it already is isn't a change.
func TestShortcutsResetAll(t *testing.T) {
	h := newHarness(t)
	h.size.Y = 2400
	keys.Set("undo", keys.Chord{Name: "U", Mods: key.ModAlt})
	d := h.shortcutsPage()
	keys.Set("undo", keys.Chord{Name: "U", Mods: key.ModAlt}) // shortcutsPage's cleanup is registered; set again after
	h.clickButton("keyChangepalette")
	h.send(press("K", key.ModShortcut)) // as it is
	if len(d.kdraft) != 0 {
		t.Fatalf("the same key drafted: %v", d.kdraft)
	}
	h.clickButton("keyChangeundo")
	h.send(press("U", key.ModAlt)) // as it is now, rebound
	if len(d.kdraft) != 0 {
		t.Fatalf("the rebound key drafted: %v", d.kdraft)
	}
	h.clickButton("keyChangepalette")
	h.send(press("P", key.ModAlt))
	h.clickButton("keysResetAll")
	if c, ok := d.kdraft["undo"]; !ok || c != nil || len(d.kdraft) != 1 || d.rec != "" {
		t.Fatalf("Reset all: %v", d.kdraft)
	}
	h.clickButton("cfgSave")
	if keys.Changed("undo") {
		t.Fatal("undo still rebound")
	}
}

// TestShortcutsLoadBad: a rebinding settings.toml has that can't be used
// is skipped and named in a toast; the good ones bind.
func TestShortcutsLoadBad(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(func() { keys.Load(nil) })
	file := orgs.SettingsFile()
	os.MkdirAll(filepath.Dir(file), 0o755)
	os.WriteFile(file, []byte("[keys]\n\"board.new\" = \"Alt+B\"\nundo = \"Shift+2\"\n"), 0o644)
	h.u.loadKeys()
	if keys.Cap("board.new") != "Alt+B" || keys.Changed("undo") || !h.u.toastBad || !strings.Contains(h.u.toast, "undo") {
		t.Fatalf("toast %q bad %v, board.new %q", h.u.toast, h.u.toastBad, keys.Cap("board.new"))
	}
	h.u.toast = ""
	os.WriteFile(file, []byte("[keys]\n\"board.new\" = \"Alt+B\"\n"), 0o644)
	h.u.loadKeys()
	if h.u.toast != "" {
		t.Errorf("a good file toasted %q", h.u.toast)
	}
}

// TestShortcutsSaveFails: a settings file that can't be written keeps the
// page open and says the rebinding holds until desk quits.
func TestShortcutsSaveFails(t *testing.T) {
	h := newHarness(t)
	h.size.Y = 2400
	d := h.shortcutsPage()
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o644)
	t.Setenv("XDG_CONFIG_HOME", file) // a file where a folder should be
	h.clickButton("keyChangeboard.new")
	h.send(press("B", key.ModAlt))
	h.send(press("S", key.ModShortcut))
	if h.u.cfg == nil || !strings.Contains(d.err, "not saved") || keys.Cap("board.new") != "Alt+B" {
		t.Fatalf("err %q, cap %q", d.err, keys.Cap("board.new"))
	}
}

// TestBoardArrows: each arrow moves the selection; with Ctrl, the card.
func TestBoardArrows(t *testing.T) {
	h := newHarness(t)
	a := h.u.b.Add(0, kanban.Card{Title: "a", Org: "Acme"}).ID
	b := h.u.b.Add(0, kanban.Card{Title: "b", Org: "Acme"}).ID
	c := h.u.b.Add(1, kanban.Card{Title: "c", Org: "Acme"}).ID
	h.u.sel = a
	h.frame()
	for _, s := range []struct {
		k    key.Name
		want string
	}{{key.NameDownArrow, b}, {key.NameUpArrow, a}, {key.NameRightArrow, c}, {key.NameLeftArrow, a}} {
		h.send(press(s.k, 0))
		if h.u.sel != s.want {
			t.Fatalf("%s: selected %s, want %s", s.k, h.u.sel, s.want)
		}
	}
	h.send(press(key.NameRightArrow, key.ModShortcut))
	if col, _ := h.u.b.Find(a); col != 1 {
		t.Errorf("Ctrl+→ left it in column %d", col)
	}
}

// TestPaletteScopeBack: Ctrl+Shift+Tab goes back a scope; Shift+Tab, which
// has no Ctrl or Alt, doesn't.
func TestPaletteScopeBack(t *testing.T) {
	h := newHarness(t)
	h.send(press("K", key.ModShortcut))
	n := h.u.pal.scopes() // Files only in a workspace
	h.send(press(key.NameTab, 0))
	if h.u.pal.scope != 1 {
		t.Fatalf("Tab: %d", h.u.pal.scope)
	}
	h.send(press(key.NameTab, key.ModShift))
	if h.u.pal.scope != 1 {
		t.Fatalf("Shift+Tab moved: %d", h.u.pal.scope)
	}
	h.send(press(key.NameTab, key.ModShortcut|key.ModShift), press(key.NameTab, key.ModShortcut|key.ModShift))
	if h.u.pal.scope != n-1 {
		t.Fatalf("Ctrl+Shift+Tab twice: %d", h.u.pal.scope)
	}
	h.send(press(key.NameTab, 0)) // All, then the keypad's Enter runs the top item
	h.send(press(key.NameEnter, 0))
	if h.u.pal != nil {
		t.Fatal("the keypad's Enter didn't run the item")
	}
}

// TestComposerNewline: in the session's message box Alt+Enter is a new
// line and Shift+Enter neither one nor a send.
func TestComposerNewline(t *testing.T) {
	h := newHarness(t)
	h.create("a card", "Acme")
	id := h.only().ID
	h.u.openPanel(id)
	h.frame()
	h.u.chatIn.SetText("hi")
	h.u.chatIn.SetCaret(2, 2)
	h.frame()
	h.r.Source().Execute(key.FocusCmd{Tag: &h.u.chatIn})
	h.frame()
	sent := len(h.claudes[0].prompts)
	h.send(press(key.NameReturn, key.ModAlt), press(key.NameReturn, key.ModShift))
	if h.u.chatIn.Text() != "hi\n" || len(h.claudes[0].prompts) != sent {
		t.Fatalf("box %q, prompts %v", h.u.chatIn.Text(), h.claudes[0].prompts)
	}
}

// TestShortcutsClickKey: a click on a row's keys records its new key, as
// Change does; another click stops.
func TestShortcutsClickKey(t *testing.T) {
	h := newHarness(t)
	d := h.shortcutsPage()
	h.clickButton("keyCappalette")
	if d.rec != "palette" {
		t.Fatalf("the keys' click didn't record: %q", d.rec)
	}
	h.clickButton("keyCappalette")
	if d.rec != "" {
		t.Fatalf("a second click didn't stop: %q", d.rec)
	}
}

// TestShortcutsRekeyOpenTab: saving a new key for ws.save reaches a
// workspace tab already open: its old key stops saving, the new one saves.
func TestShortcutsRekeyOpenTab(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(func() { keys.Load(nil) })
	c, dir := wsCard(h)
	p := filepath.Join(dir, "main.go")
	h.u.openWS(c.ID)
	tb := h.u.wsOpen(h.u.wsOf(c.ID), p)
	h.frame()
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: editorAt},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: editorAt})
	tb.ed.SetCaret(0, 0)
	h.send(key.EditEvent{Text: "// x\n"})
	alt := keys.Chord{Name: "S", Mods: key.ModAlt}
	if !h.u.saveKeys(&cfgDialog{kdraft: map[string]*keys.Chord{"ws.save": &alt}}) {
		t.Fatal("not saved")
	}
	h.send(press("S", key.ModShortcut))
	if !tb.dirty {
		t.Fatal("the old key still saves the open tab")
	}
	h.send(press("S", key.ModAlt))
	if b, _ := os.ReadFile(p); tb.dirty || !strings.HasPrefix(string(b), "// x\n") {
		t.Fatalf("the new key didn't save: dirty %v, %q", tb.dirty, b)
	}
}
