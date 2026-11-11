package ui

import (
	"fmt"
	"github.com/lfdominguez/desk/internal/testwait"
	"image"
	"reflect"
	"slices"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"

	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

var ctrlK = press("K", key.ModShortcut)

// palOrder is what the open palette lists now, in order, as ids.
func (h *harness) palOrder() (ids []string) {
	_, order := h.u.palRows(h.u.pal.q.Text(), h.u.pal.scope)
	for _, it := range order {
		ids = append(ids, it.id)
	}
	return ids
}

// palGo searches q in the open palette and opens item id with the keys.
func (h *harness) palGo(q, id string) {
	h.u.pal.q.SetText(q)
	h.frame()
	i := slices.Index(h.palOrder(), id)
	if i < 0 {
		h.t.Fatalf("%q doesn't list %s: %v", q, id, h.palOrder())
	}
	for range i {
		h.send(press(key.NameDownArrow, 0))
	}
	if h.u.pal.sel != i {
		h.t.Fatalf("down %d times: sel %d", i, h.u.pal.sel)
	}
	h.send(press(key.NameReturn, 0))
	if h.u.pal != nil {
		h.t.Fatal("enter left the palette open")
	}
}

// TestPaletteOpens: Ctrl+K opens it on the board and over a session's
// message box, its search focused, the board's keys off; not over a dialog;
// esc closes it.
func TestPaletteOpens(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "Renew the certificate", Org: "Globex"})
	h.send(ctrlK)
	if h.u.pal == nil || !h.r.Source().Focused(&h.u.pal.q) {
		t.Fatal("Ctrl+K didn't open the palette with its search focused")
	}
	h.send(press("N", key.ModShortcut), press("E", 0))
	if h.u.edit != nil {
		t.Fatal("a board shortcut fired under the palette")
	}
	h.send(press(key.NameEscape, 0))
	if h.u.pal != nil || h.u.sel != "" {
		t.Fatal("esc didn't close the palette (alone)")
	}
	h.u.newCard(0)
	h.frame()
	h.send(ctrlK)
	if h.u.pal != nil {
		t.Fatal("Ctrl+K opened the palette over the card dialog")
	}
	h.send(press(key.NameEscape, 0))
	h.u.openSession(c.ID)
	h.frame()
	h.frame()
	if !h.r.Source().Focused(&h.u.chatIn) {
		t.Fatal("the session's message box isn't focused")
	}
	h.send(ctrlK)
	if h.u.pal == nil {
		t.Fatal("Ctrl+K didn't open over the session view")
	}
	h.send(press(key.NameEscape, 0))
	if h.u.pal != nil || h.u.view != "sess:"+c.ID {
		t.Fatalf("esc: palette %v, view %q", h.u.pal != nil, h.u.view)
	}
	h.u.view = "srv:Acme" // the servers' panes keep their keys
	h.frame()
	h.send(ctrlK)
	if h.u.pal != nil {
		t.Fatal("Ctrl+K opened over the servers")
	}
}

// TestPaletteFilter: a search puts the best match on top, enter opens a
// card's panel; reopened empty, it comes first as a recent.
func TestPaletteFilter(t *testing.T) {
	h := newHarness(t)
	h.u.b.Add(1, kanban.Card{Title: "Write the docs", Org: "Acme"})
	c := h.u.b.Add(0, kanban.Card{Title: "Renew the api certificate", Org: "Globex", Status: kanban.StatusBlocked, Note: "no access",
		Links: []kanban.Link{{URL: "https://github.com/globex-org/api/pull/1"}}})
	h.send(ctrlK)
	h.send(key.EditEvent{Text: "renew"})
	rows, order := h.u.palRows(h.u.pal.q.Text(), 0)
	if len(rows) < 2 || rows[0].head != "Top result" || order[0].id != "card:"+c.ID {
		t.Fatalf("searching renew: %v", h.palOrder())
	}
	pv := order[0].preview
	if !slices.Contains(pv, [2]string{"Column", h.u.b.Columns[0].Title}) || !slices.Contains(pv, [2]string{"Organization", "Globex"}) ||
		!slices.Contains(pv, [2]string{"Status", "blocked · no access"}) || !slices.Contains(pv, [2]string{"Links", "1"}) {
		t.Fatalf("the card's preview: %v", pv)
	}
	if slices.Contains(h.palOrder(), "card:"+h.u.b.Columns[1].Cards[0].ID) {
		t.Fatal("a card that doesn't match is listed")
	}
	h.send(press(key.NameReturn, 0))
	if h.u.pal != nil || h.u.panel != c.ID {
		t.Fatalf("enter: palette %v, panel %q", h.u.pal != nil, h.u.panel)
	}
	h.send(press(key.NameEscape, 0))
	h.send(ctrlK)
	rows, order = h.u.palRows("", 0)
	if rows[0].head != "Recent" || order[0].id != "card:"+c.ID || rows[2].head != "Cards" {
		t.Fatalf("empty search after opening it: %v", h.palOrder())
	}
	if slices.Contains(h.palOrder()[1:], "card:"+c.ID) {
		t.Fatal("the recent card is listed twice")
	}
	if rows, order := h.u.palRows("zzzz", 0); rows != nil || order != nil {
		t.Fatal("rows for a search that matches nothing")
	}
}

// TestPaletteRecents: the last 8 opened, newest first, each once.
func TestPaletteRecents(t *testing.T) {
	h := newHarness(t)
	open := func(id string) {
		h.u.openPalette()
		h.u.palPick(&palItem{id: id, run: func() {}})
	}
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "b"} {
		open(id)
	}
	if want := []string{"b", "i", "h", "g", "f", "e", "d", "c"}; !slices.Equal(h.u.recent, want) {
		t.Fatalf("recents %v, want %v", h.u.recent, want)
	}
}

// TestPaletteKeys: up and down move and stop at the ends, tab and
// shift+tab cycle the scopes, each listing only its kind.
func TestPaletteKeys(t *testing.T) {
	h := newHarness(t)
	for _, s := range []string{"One", "Two", "Three"} {
		h.u.b.Add(0, kanban.Card{Title: s, Org: "Acme"})
	}
	h.send(ctrlK)
	n := len(h.palOrder())
	h.send(press(key.NameDownArrow, 0), press(key.NameDownArrow, 0))
	if h.u.pal.sel != 2 {
		t.Fatalf("down twice: %d", h.u.pal.sel)
	}
	h.send(press(key.NameUpArrow, 0), press(key.NameUpArrow, 0), press(key.NameUpArrow, 0))
	if h.u.pal.sel != 0 {
		t.Fatalf("up past the top: %d", h.u.pal.sel)
	}
	for range n + 3 {
		h.r.Queue(press(key.NameDownArrow, 0))
	}
	h.frame()
	if h.u.pal.sel != n-1 {
		t.Fatalf("down past the end: %d of %d", h.u.pal.sel, n)
	}
	h.u.pal.q.SetText("t")
	h.frame()
	if h.u.pal.sel != 0 {
		t.Fatal("a new search kept the selection")
	}
	h.u.pal.q.SetText("zzzz")
	h.frame()
	h.send(press(key.NameDownArrow, 0), press(key.NameReturn, 0))
	if h.u.pal == nil || h.u.pal.sel != 0 {
		t.Fatal("down and enter with nothing listed")
	}
	h.u.pal.q.SetText("")
	h.frame()
	for want := 1; want <= 4; want++ {
		h.send(press(key.NameTab, 0))
		if h.u.pal.scope != want {
			t.Fatalf("tab %d: scope %d", want, h.u.pal.scope)
		}
		_, order := h.u.palRows("", want)
		for _, it := range order {
			if it.kind != want {
				t.Fatalf("scope %s lists %s", palScopes[want], it.id)
			}
		}
		if want != palServer && want != palSession && len(order) == 0 {
			t.Fatalf("scope %s is empty", palScopes[want])
		}
	}
	h.send(press(key.NameTab, 0))
	if h.u.pal.scope != 0 {
		t.Fatalf("tab past Actions: %d", h.u.pal.scope)
	}
	h.send(press(key.NameTab, key.ModShift))
	if h.u.pal.scope != 4 {
		t.Fatalf("shift+tab from All: %d", h.u.pal.scope)
	}
	h.send(press(key.NameDownArrow, 0), press(key.NameTab, key.ModShift))
	if h.u.pal.scope != 3 || h.u.pal.sel != 0 {
		t.Fatalf("shift+tab: scope %d, sel %d", h.u.pal.scope, h.u.pal.sel)
	}
}

// TestPaletteActions: new card (in an org), settings (of an org) and the
// search folders.
func TestPaletteActions(t *testing.T) {
	h := newHarness(t)
	h.send(ctrlK)
	h.palGo("new card in globex", "act:new:Globex")
	if h.u.edit == nil || h.u.edit.org.Text() != "Globex" {
		t.Fatal("New card in Globex didn't open the dialog in Globex")
	}
	h.send(press(key.NameEscape, 0))
	h.send(ctrlK)
	h.palGo("new card", "act:new")
	if h.u.edit == nil || h.u.edit.id != "" {
		t.Fatal("New card didn't open the dialog")
	}
	h.send(press(key.NameEscape, 0))
	h.send(ctrlK)
	h.palGo("acme settings", "act:settings:Acme")
	if h.u.cfg == nil || h.u.cfg.org != "Acme" {
		t.Fatal("Open Acme settings didn't")
	}
	h.send(press(key.NameEscape, 0))
	h.send(ctrlK)
	h.palGo("folders", "act:folders")
	if h.u.cfg == nil || h.u.cfg.org != cfgFolders {
		t.Fatal("Search folders didn't")
	}
	h.send(press(key.NameEscape, 0))
	h.u.lastOrg = "Globex"
	h.send(ctrlK)
	h.palGo("settings", "act:settings")
	if h.u.cfg == nil || h.u.cfg.org != "Globex" {
		t.Fatal("Settings didn't open on the last org")
	}
	_, order := h.u.palRows("new card", palAction)
	if order[0].right != kit.ModText("N") || !order[0].keyCap {
		t.Fatalf("New card's cap: %q", order[0].right)
	}
}

// TestPaletteSessions: cards with a live session or terminal, opened full
// window.
func TestPaletteSessions(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(1, kanban.Card{Title: "Run migration", Org: "Acme"})
	d := h.u.b.Add(1, kanban.Card{Title: "Idle card", Org: "Acme"})
	h.u.procs[c.ID] = &fakeClaude{}
	h.u.terms[d.ID] = &termTab{} // only read, never drawn
	items := h.u.palItems()
	delete(h.u.terms, d.ID)
	var sess []*palItem
	for _, it := range items {
		if it.kind == palSession {
			sess = append(sess, it)
		}
	}
	if len(sess) != 2 || sess[0].id != "sess:"+c.ID || sess[0].mono != c.Key() {
		t.Fatalf("sessions %v", sess)
	}
	if !sameIcon(sess[0].ico, kit.IcoChat) || !sameIcon(sess[1].ico, kit.IcoTerminal) {
		t.Fatal("a session's icon isn't its kind's")
	}
	h.send(ctrlK)
	h.palGo("migration", "sess:"+c.ID)
	if h.u.view != "sess:"+c.ID {
		t.Fatalf("the session item went to %q", h.u.view)
	}
}

// sameIcon is whether a and b are the same icon function.
func sameIcon(a, b kit.IconPath) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

// TestPaletteServers: the host tree's servers, matched by name and org,
// with their address and group in the preview; with no workspace, picking
// one does nothing.
func TestPaletteServers(t *testing.T) {
	h := newHarness(t)
	h.u.tree = []infra.Org{{Name: "Globex", Groups: []infra.Group{{Name: "api", Hosts: []infra.Host{
		{Name: "web-01", Addr: "10.0.4.21", Meta: "2 panes", State: infra.Connected},
		{Name: "db-01", Addr: "10.0.4.30", Meta: "10.0.4.30"},
	}}}}}
	h.send(ctrlK)
	h.u.pal.q.SetText("glo web")
	h.frame()
	_, order := h.u.palRows("glo web", 0)
	if len(order) == 0 || order[0].id != "host:Globex/api/web-01" || order[0].right != "connected" || order[0].rightC != kit.CGreen {
		t.Fatalf("glo web: %v", h.palOrder())
	}
	if pv := order[0].preview; !slices.Equal(pv, [][2]string{{"Address", "10.0.4.21"}, {"Group", "Globex / api"}, {"Status", "2 panes"}}) {
		t.Fatalf("web-01's preview: %v", pv)
	}
	_, order = h.u.palRows("db-01", palServer)
	if pv := order[0].preview; !slices.Equal(pv, [][2]string{{"Address", "10.0.4.30"}, {"Group", "Globex / api"}}) {
		t.Fatalf("db-01's preview (its meta the address): %v", pv)
	}
	if ids := h.palOrder(); slices.Contains(ids, "host:Globex/api/db-01") {
		t.Fatal("db-01 matched glo web")
	}
	_, order = h.u.palRows("", palServer)
	if len(order) != 3 || order[0].id != "srvc:Globex" {
		t.Fatalf("the servers scope: %d items", len(order))
	}
	h.send(press(key.NameReturn, 0))
	if h.u.pal != nil || h.u.view != "" {
		t.Fatalf("picking with no workspace: palette %v, view %q", h.u.pal != nil, h.u.view)
	}
}

// TestPaletteOpensHost: a server picked opens its session in the workspace.
func TestPaletteOpensHost(t *testing.T) {
	for _, v := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "XDG_CACHE_HOME"} {
		t.Setenv(v, t.TempDir()) // never the user's infra session
	}
	h := newHarness(t)
	localServer(t, h.u.root, "Acme")
	h.u.infra = infra.New(h.u.root, func() {})
	defer h.u.infra.Close()
	for end := time.Now().Add(testwait.Long); h.u.infraOrg("Acme") == nil && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		h.frame()
	}
	h.send(ctrlK)
	h.palGo("local", "host:Acme/web/local")
	if h.u.view != "srv:Acme" {
		t.Fatalf("the host went to %q", h.u.view)
	}
}

// TestPaletteClicks: a click on a row opens it, on a scope picks it,
// outside the box closes the palette.
func TestPaletteClicks(t *testing.T) {
	h := newHarness(t)
	h.size = image.Pt(1200, 800)
	c := h.u.b.Add(0, kanban.Card{Title: "Renew the certificate", Org: "Globex"})
	h.send(ctrlK)
	h.clickButton("palScopeActions")
	if h.u.pal == nil || h.u.pal.scope != palAction {
		t.Fatal("clicking Actions didn't scope the palette")
	}
	h.clickButton("palScopeAll")
	h.clickButton("palcard:" + c.ID)
	if h.u.pal != nil || h.u.panel != c.ID {
		t.Fatalf("clicking the card: palette %v, panel %q", h.u.pal != nil, h.u.panel)
	}
	h.send(ctrlK)
	p := f32.Pt(10, 790)
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
	if h.u.pal != nil {
		t.Fatal("a click outside left the palette open")
	}
}

// TestPaletteFocus: its search keeps one line, holds the focus it was given
// only until something else takes it, gets it back from a scope's click and
// lets it go on close; a key's release does nothing.
func TestPaletteFocus(t *testing.T) {
	h := newHarness(t)
	h.u.b.Add(0, kanban.Card{Title: "One", Org: "Acme"})
	h.send(ctrlK)
	p := h.u.pal
	h.send(key.EditEvent{Text: "a\nb"})
	if p.q.Text() != "a b" {
		t.Fatalf("a pasted newline: %q", p.q.Text())
	}
	p.q.SetText("")
	h.frame()
	h.send(key.Event{Name: key.NameDownArrow, State: key.Release})
	if p.sel != 0 {
		t.Fatal("a release moved the selection")
	}
	h.r.Source().Execute(key.FocusCmd{})
	h.frame()
	h.frame()
	if h.r.Source().Focused(&p.q) {
		t.Fatal("the search took the focus back by itself")
	}
	h.clickButton("palScopeCards")
	if !h.r.Source().Focused(&p.q) {
		t.Fatal("a scope's click left the search unfocused")
	}
	h.send(press(key.NameEscape, 0))
	if h.r.Source().Focused(&p.q) {
		t.Fatal("the closed palette's search kept the focus")
	}
}

// TestPaletteScrolls: the selected item stays in view going down a long
// list and back up.
func TestPaletteScrolls(t *testing.T) {
	h := newHarness(t)
	h.size = image.Pt(1200, 700)
	for i := range 40 {
		h.u.b.Add(0, kanban.Card{Title: fmt.Sprint("Card ", i), Org: "Acme"})
	}
	h.send(ctrlK)
	p := h.u.pal
	shown := func() {
		t.Helper()
		rows, order := h.u.palRows("", p.scope)
		r := slices.IndexFunc(rows, func(r palRow) bool { return r.it == order[p.sel] })
		if pos := p.list.Position; r < pos.First || r >= pos.First+pos.Count {
			t.Fatalf("sel %d (row %d) out of view: rows %d..%d", p.sel, r, pos.First, pos.First+pos.Count)
		}
	}
	h.send(press(key.NameDownArrow, 0))
	if p.list.Position.First != 0 {
		t.Fatal("the list scrolled for an item in view")
	}
	for range 30 {
		h.send(press(key.NameDownArrow, 0))
		shown()
	}
	if p.list.Position.First == 0 {
		t.Fatal("the list didn't scroll")
	}
	for range 31 {
		h.send(press(key.NameUpArrow, 0))
		shown()
	}
	if p.list.Position.First != 0 {
		t.Fatalf("back at the top, the list starts at row %d", p.list.Position.First)
	}
}

// TestPalMarks: every word of the query lands in the text; exclusions and
// "|" don't.
func TestPalMarks(t *testing.T) {
	got := palMarks("glo web !x |", "Globex / web-01")
	var at []int
	for i := range 20 {
		if got[i] {
			at = append(at, i)
		}
	}
	if !slices.Equal(at, []int{0, 1, 2, 9, 10, 11}) {
		t.Fatalf("marks %v", at)
	}
	for _, c := range []struct {
		q, s string
		want []int
	}{
		{"web", "Renew web-01", []int{6, 7, 8}}, // whole, not the first w's letters
		{"gx", "Globex", []int{0, 5}},           // scattered letters
		{"web", "ñu web", []int{3, 4, 5}},       // runes, not bytes
		{"'01", "web-01", []int{4, 5}},
		{"zz", "web", nil},
		{"web", "we", nil}, // longer than the text
	} {
		var at []int
		m := palMarks(c.q, c.s)
		for i := range 20 {
			if m[i] {
				at = append(at, i)
			}
		}
		if !slices.Equal(at, c.want) {
			t.Errorf("palMarks(%q, %q) = %v, want %v", c.q, c.s, at, c.want)
		}
	}
}
