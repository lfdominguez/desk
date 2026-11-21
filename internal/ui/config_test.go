package ui

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/lfdominguez/desk/internal/orgs"
)

// writeAt writes a fixture, its folders first (an org's .desk among them).
func writeAt(path string, b []byte, perm os.FileMode) error {
	os.MkdirAll(filepath.Dir(path), 0o755)
	return os.WriteFile(path, b, perm)
}

// cfgHarness draws the settings alone in a tall window, for clickIn.
func cfgHarness(h *harness) (image.Point, image.Rectangle, layout.Widget) {
	sz := image.Pt(1100, 2000)
	return sz, image.Rectangle{Max: sz}, func(gtx layout.Context) layout.Dimensions {
		h.u.configDialog(gtx)
		return layout.Dimensions{Size: sz}
	}
}

// TestConfigChanges: an edit, an add and a remove count one change each,
// every page's together; the bar says how many; Discard drops them all.
func TestConfigChanges(t *testing.T) {
	h := newHarness(t)
	h.orgToml("Acme", "[[jira]]\nurl = \"https://globex.atlassian.net\"\n[[github]]\nhost = \"github.com\"\n[[gitlab]]\n")
	h.u.openConfig("Acme")
	d := h.u.cfg
	if d.changes() != 0 || len(d.entries) != 3 {
		t.Fatalf("as read: %d changes, %d entries", d.changes(), len(d.entries))
	}
	sz, all, w := cfgHarness(h)
	h.draw(sz, w) // drawing makes empty editors: not a change
	if d.changes() != 0 || d.dirty != 0 {
		t.Fatalf("drawn: %d changes", d.changes())
	}
	d.entries[0].ed("host").SetText("github.acme.test") // the GitHub one: kinds load in cfgKinds' order
	if !d.entries[0].edited() || d.changes() != 1 {
		t.Fatalf("an edit: %d", d.changes())
	}
	d.entries = d.entries[:2] // gitlab removed
	if d.changes() != 2 {
		t.Fatalf("an edit and a remove: %d", d.changes())
	}
	h.clickIn(sz, all, w, "cfgAddnagios")
	if d.changes() != 3 || d.dirty != 3 {
		t.Fatalf("plus an add: %d (dirty %d)", d.changes(), d.dirty)
	}
	h.clickIn(sz, all, w, "cfgOrgGlobex")
	g := h.u.cfg
	g.entries = append(g.entries, newEntry("jira", map[string]any{}))
	if g.total() != 4 || g.changes() != 1 {
		t.Fatalf("both pages: %d, Globex %d", g.total(), g.changes())
	}
	h.draw(sz, w)
	if g.dirty != 4 {
		t.Fatalf("dirty %d", g.dirty)
	}
	h.clickIn(sz, all, w, "cfgDiscard")
	if c := h.u.cfg; c == g || c.org != "Globex" || c.total() != 0 || len(c.set.pages) != 1 {
		t.Fatal("Discard kept edits")
	}
}

// TestConfigSaveAll: Save writes every page with changes, the one shown
// first; one that can't be saved is shown with why, the others kept.
func TestConfigSaveAll(t *testing.T) {
	h := newHarness(t)
	h.u.openConfig("Globex")
	g := h.u.cfg
	g.entries = append(g.entries, newEntry("gitlab", map[string]any{}))
	h.u.cfgPage("Acme")
	a := h.u.cfg
	bad := newEntry("vault", map[string]any{}) // no name
	a.entries = append(a.entries, bad)
	h.u.cfgPage("Globex")
	h.u.saveConfig()
	if h.u.cfg != a || a.err != "A vault needs a name" {
		t.Fatalf("shown %v, err %q", h.u.cfg == a, a.err)
	}
	if c, _ := orgs.Load(h.u.root, "Globex"); len(c.GitLab) != 1 {
		t.Fatal("the page shown first was not saved")
	}
	bad.ed("name").SetText("vw")
	h.frame() // the dialog's keys listen from its first frame
	h.send(press("S", key.ModShortcut))
	if h.u.cfg != nil {
		t.Fatalf("Ctrl+S did not save: %q", a.err)
	}
	if c, _ := orgs.Load(h.u.root, "Acme"); len(c.Vaults) != 1 {
		t.Fatal("Acme not saved")
	}
	if h.u.toast != "Saved Acme, Globex's settings" || h.u.toastBad {
		t.Fatalf("toast %q bad %v", h.u.toast, h.u.toastBad)
	}
	h.u.blur = false
	h.u.dropConfig()
	if !h.u.blur {
		t.Fatal("closing left an editor of the dialog focused")
	}
	h.u.toast = ""
	h.u.openConfig("Acme")
	h.u.saveConfig() // nothing changed: just closes
	if h.u.cfg != nil || h.u.toast != "" {
		t.Fatalf("Save without changes: kept %v, toast %q", h.u.cfg != nil, h.u.toast)
	}
	h.u.openConfig("Acme")
	hv := newEntry("vault", map[string]any{"name": "infra", "kind": "hashicorp", "url": "https://vault.example.com", "mount": "kv"})
	h.u.cfg.entries = append(h.u.cfg.entries, hv)
	if !h.u.saveOrg(h.u.cfg) {
		t.Fatal(h.u.cfg.err)
	}
	raw, _ := orgs.LoadRaw(h.u.root, "Acme")
	if vs, _ := raw["vault"].([]map[string]any); len(vs) != 2 || vs[1]["kind"] != "hashicorp" || vs[1]["mount"] != "kv" {
		t.Fatalf("a hashicorp vault the dialog doesn't edit is kept as it is: %v", raw["vault"])
	}
	h.u.dropConfig()
	d := &cfgDialog{entries: []*cfgEntry{newEntry("github", map[string]any{"name": "g"}), newEntry("vault", map[string]any{"name": "v"}), newEntry("vault", map[string]any{})}}
	if n := d.vaultNames(); !slices.Equal(n, []string{"v"}) {
		t.Fatalf("vault names %v", n)
	}
}

// TestConfigNav: the nav's dots and notes: an org whose desk.toml doesn't
// read, one with accounts, a nested one under its outer one, an edited page.
func TestConfigNav(t *testing.T) {
	h := newHarness(t)
	h.orgToml("Acme", "[[github]]\n")
	h.orgToml("Globex", "[[jira]\n")
	writeAt(filepath.Join(h.u.root, "Acme", "api", ".desk", "desk.toml"), nil, 0o644)
	h.u.openConfig("nope") // an unknown org opens the first
	d := h.u.cfg
	s := d.set
	if d.org != "Acme" || s.state["Acme"] != orgOK || s.state["Globex"] != orgBroken || s.state["api"] != orgEmpty || s.parent["api"] != "Acme" || s.parent["Acme"] != "" {
		t.Fatalf("org %s, state %v, parent %v", d.org, s.state, s.parent)
	}
	sz, all, w := cfgHarness(h)
	d.entries = nil
	h.draw(sz, w)
	h.clickIn(sz, all, w, "cfgOrgapi")
	if h.u.cfg.org != "api" {
		t.Fatal("the nested org's row")
	}
	h.clickIn(sz, all, w, "cfgOrgGlobex")
	if g := h.u.cfg; !g.broken {
		t.Fatal("Globex reads")
	}
	h.u.saveConfig() // the page shown is broken: nothing saved, kept open
	if h.u.cfg == nil {
		t.Fatal("a broken page closed on Save")
	}
}

// TestConfigNavAccounts: any one kind of account makes an org green.
func TestConfigNavAccounts(t *testing.T) {
	h := newHarness(t)
	kinds := map[string]string{"Initech": "jira", "Umbrella": "bitbucket", "Soylent": "gitlab", "Vandelay": "nagios", "Acme": "vault"}
	for org, kind := range kinds {
		os.MkdirAll(filepath.Join(h.u.root, org), 0o755)
		writeAt(filepath.Join(h.u.root, org, ".desk", "desk.toml"), []byte("[["+kind+"]]\n"), 0o644)
	}
	h.u.openConfig("Acme")
	for org := range kinds {
		if h.u.cfg.set.state[org] != orgOK {
			t.Errorf("%s: state %d", org, h.u.cfg.set.state[org])
		}
	}
}

// TestConfigEmptyRoot: without organizations the settings open on the
// search folders.
func TestConfigEmptyRoot(t *testing.T) {
	h := newHarness(t)
	os.RemoveAll(filepath.Join(h.u.root, "Acme"))
	os.RemoveAll(filepath.Join(h.u.root, "Globex"))
	h.u.openConfig("Acme")
	if h.u.cfg.org != cfgFolders {
		t.Fatalf("opened %q", h.u.cfg.org)
	}
}

// TestEntrySummary: an account's line says where it points, what it covers
// and where its token comes from.
func TestEntrySummary(t *testing.T) {
	e := newEntry("jira", map[string]any{"name": "globex", "url": "https://globex.atlassian.net", "projects": []any{"ABC", "OPS"}, "vault": "vw"})
	if s := e.summary(); s != "globex.atlassian.net · projects ABC, OPS · api token from vw" {
		t.Fatalf("summary %q", s)
	}
	for src, want := range map[int]string{srcNone: "no token", srcEnv: "token from $GL", srcCmd: "token from a command", srcFile: "token in desk.toml", srcVault: "token from a vault"} {
		g := newEntry("gitlab", map[string]any{"url": "http://git.acme.test", "token_env": "GL"})
		g.src["."] = src
		if s := g.summary(); s != "git.acme.test · "+want {
			t.Errorf("source %d: %q", src, s)
		}
	}
	v := newEntry("vault", map[string]any{"name": "vw", "kind": "passbolt", "email": "me@globex.com"})
	if s := v.summary(); s != "asks for its password" {
		t.Fatalf("vault %q", s)
	}
	if !v.ed("url").SingleLine {
		t.Fatal("a field takes several lines")
	}
	bw := newEntry("vault", map[string]any{"name": "vw", "email": "me@globex.com"})
	if s := bw.summary(); s != "me@globex.com · asks for its password" {
		t.Fatalf("bitwarden %q", s)
	}
}

// TestFolderPage: a folder's depth stepper (0 to 6), its switches, Remove,
// Add folder; the preview finds what the folders give as they change, new
// ones marked, and lists the subfolders only_toml skipped.
func TestFolderPage(t *testing.T) {
	h := newHarness(t)
	other := t.TempDir()
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "Initech"} {
		os.MkdirAll(filepath.Join(other, n), 0o755)
	}
	writeAt(filepath.Join(other, "Initech", ".desk", "desk.toml"), nil, 0o644)
	h.u.openConfig(cfgFolders)
	h.u.cfg.set.sub[cfgFolders] = "folders"
	d := h.u.cfg
	sz, all, w := cfgHarness(h)
	h.draw(sz, w)
	if len(d.prev) != 2 || len(d.skipped) != 0 {
		t.Fatalf("preview %v skipped %v", d.prev, d.skipped)
	}
	f := d.entries[0]
	id := fmt.Sprintf("cfg%p", f)
	for i, want := range []string{"3", "4", "5", "6", "6"} {
		h.clickIn(sz, all, w, id+"depth+")
		if f.text("depth") != want {
			t.Fatalf("+ %d: depth %s", i, f.text("depth"))
		}
	}
	f.ed("depth").SetText("1")
	for _, want := range []string{"0", "0"} {
		h.clickIn(sz, all, w, id+"depth-")
		if f.text("depth") != want {
			t.Fatalf("-: depth %s", f.text("depth"))
		}
	}
	f.ed("depth").SetText("x")
	h.clickIn(sz, all, w, id+"depth+")
	if f.text("depth") != "3" {
		t.Fatalf("not a number, then +: %s", f.text("depth"))
	}
	h.clickIn(sz, all, w, id+"only_toml")
	if !f.on["only_toml"] {
		t.Fatal("only_toml switch")
	}
	h.clickIn(sz, all, w, id+"only_toml")
	if f.on["only_toml"] {
		t.Fatal("only_toml switch, off again")
	}
	h.clickIn(sz, all, w, "cfgAddfolder")
	n := d.entries[1]
	if n.text("depth") != "1" || !n.on["only_toml"] || n.old {
		t.Fatalf("a new folder: %q %v", n.text("depth"), n.on)
	}
	n.ed("path").SetText(other)
	h.draw(sz, w)
	if !slices.ContainsFunc(d.prev, func(f orgs.Found) bool { return f.Name == "Initech" && f.Folder == 1 }) || len(d.skipped) != 7 {
		t.Fatalf("preview %v skipped %v", d.prev, d.skipped)
	}
	if d.showSkipped {
		t.Fatal("all skipped shown at first")
	}
	h.clickIn(sz, all, w, "cfgSkipped")
	if !d.showSkipped {
		t.Fatal("Show all")
	}
	h.draw(sz, w)
	h.clickIn(sz, all, w, fmt.Sprintf("cfg%p", n)+"rm")
	if len(d.entries) != 1 {
		t.Fatal("Remove")
	}
	if fs := d.folders(); fs[0].Depth != 3 || fs[0].OnlyToml {
		t.Fatalf("folders %+v", fs)
	}
}

// TestConfigTexts: the nav's order and notes, the bar's message, a card's
// status and its field rows, what the preview says of a folder.
func TestConfigTexts(t *testing.T) {
	h := newHarness(t)
	h.u.home = "/home/me"
	s := &cfgSet{pages: map[string]*cfgDialog{}, state: map[string]int{"Acme": orgOK, "Globex": orgBroken, "api": orgOK, "x": orgEmpty}, parent: map[string]string{"api": "Acme"}}
	if o := s.order([]string{"api", "Acme", "Globex"}); !slices.Equal(o, []string{"Acme", "api", "Globex"}) {
		t.Fatalf("order %v", o)
	}
	for o, want := range map[string]string{"Acme": "", "Globex": "can't read", "x": "no accounts", "api": "in Acme", cfgFolders: ""} {
		if n, _ := s.note(o); n != want {
			t.Errorf("note of %q: %q", o, n)
		}
	}
	d := &cfgDialog{org: "Acme", set: s, file: orgs.File(h.u.root, "Acme")}
	s.pages["Acme"] = d
	if n, _ := s.note("Acme"); n != "" {
		t.Fatal("a clean page says edited")
	}
	d.entries = []*cfgEntry{newEntry("jira", map[string]any{})}
	if n, _ := s.note("Acme"); n != "edited" {
		t.Fatal("an edited page")
	}
	if m, _, note := h.u.barText(d); m != "1 unsaved change" || !strings.HasSuffix(note, filepath.FromSlash("Acme/.desk/desk.toml")+"; comments in it are not kept") {
		t.Fatalf("bar %q %q", m, note)
	}
	d.entries = append(d.entries, newEntry("jira", map[string]any{}))
	if m, _, _ := h.u.barText(d); m != "2 unsaved changes" {
		t.Fatalf("bar %q", m)
	}
	d.warn = true
	if m, _, note := h.u.barText(d); !strings.HasPrefix(m, "Unsaved changes: ") || note != "" {
		t.Fatalf("warned %q", m)
	}
	d.err = "Nope"
	if m, _, note := h.u.barText(d); m != "Nope" || note != "" {
		t.Fatalf("err %q", m)
	}
	f := &cfgDialog{org: cfgFolders, set: &cfgSet{pages: map[string]*cfgDialog{}}}
	t.Setenv("XDG_CONFIG_HOME", "/home/me/.config")
	if m, _, note := h.u.barText(f); m != "No changes" || note != "· saves ~/.config/desk/settings.toml; comments in it are not kept" && !onWindows { // a POSIX home
		t.Fatalf("folders %q %q", m, note)
	}
	e := newEntry("jira", map[string]any{"url": "u"})
	if st, _ := e.status(); st != "New" {
		t.Fatal(st)
	}
	f.keep(e)
	if st, _ := e.status(); st != "" {
		t.Fatal(st)
	}
	e.ed("url").SetText("v")
	if st, _ := e.status(); st != "Edited" {
		t.Fatal(st)
	}
	keys := func(e *cfgEntry) (out []string) {
		for _, r := range e.formRows() {
			var ks []string
			for _, f := range r {
				ks = append(ks, f.key)
			}
			out = append(out, strings.Join(ks, "+"))
		}
		return out
	}
	if k := keys(newEntry("jira", map[string]any{})); !slices.Equal(k, []string{"name+url", "email+projects", "token", "worklog"}) {
		t.Fatalf("jira rows %v", k)
	}
	if k := keys(newEntry("vault", map[string]any{"kind": "passbolt"})); !slices.Equal(k, []string{"name", "kind", "url+config_file", "key_file", "password"}) {
		t.Fatalf("passbolt rows %v", k)
	}
	if k := keys(newEntry("github", map[string]any{})); !slices.Equal(k, []string{"name+host", "owners", "token"}) {
		t.Fatalf("github rows %v", k)
	}
	f.prev = []orgs.Found{{Name: "A", Folder: 0}, {Name: "B", Folder: 1}}
	for _, n := range []string{"C", "D", "E", "F", "G"} {
		f.prev = append(f.prev, orgs.Found{Name: n, Folder: 1})
	}
	if s := f.finds(0); s != "finds 1 · A" {
		t.Fatal(s)
	}
	if s := f.finds(1); s != "finds 6 · B, C, D, E, F…" {
		t.Fatal(s)
	}
	if s := f.finds(2); s != "finds none" {
		t.Fatal(s)
	}
	f.skipped = []string{"a", "b", "c", "d", "e", "f"}
	if l, more := f.skippedText(); l != "a, b, c, d, e, f" || more {
		t.Fatalf("six %q", l)
	}
	f.skipped = append(f.skipped, "g", "h")
	if l, more := f.skippedText(); l != "a, b, c, d, e, f and 2 more." || !more {
		t.Fatalf("eight %q", l)
	}
	h.u.orgList = []string{"Acme", "api"}
	fs := []orgs.Folder{{Path: "/w"}}
	for _, c := range []struct {
		f    orgs.Found
		note string
		new  bool
	}{
		{orgs.Found{Name: "Acme", Dir: "/w/Acme"}, "", false},
		{orgs.Found{Name: "api", Dir: "/w/Acme/api"}, " · Acme/api", false},
		{orgs.Found{Name: "Initech", Dir: "/w/Initech"}, " · new, from /w", true},
	} {
		if n, isNew := h.u.chipNote(c.f, fs); n != c.note || isNew != c.new {
			t.Errorf("%s: %q %v", c.f.Name, n, isNew)
		}
	}
}

// TestConfigKeyRelease: Ctrl+S saves on its press, not on its release.
func TestConfigKeyRelease(t *testing.T) {
	h := newHarness(t)
	h.u.openConfig("Acme")
	h.u.cfg.entries = append(h.u.cfg.entries, newEntry("gitlab", map[string]any{}))
	h.frame()
	h.send(key.Event{Name: "S", Modifiers: key.ModShortcut, State: key.Release})
	if h.u.cfg == nil {
		t.Fatal("saved on the release")
	}
}

// TestFolderPageClamp: a typed depth out of range previews clamped.
func TestFolderPageClamp(t *testing.T) {
	h := newHarness(t)
	h.u.openConfig(cfgFolders)
	h.u.cfg.set.sub[cfgFolders] = "folders"
	d := h.u.cfg
	d.entries[0].ed("depth").SetText("9")
	if fs := d.folders(); fs[0].Depth != 6 {
		t.Fatalf("9 previews as %d", fs[0].Depth)
	}
	d.entries[0].ed("depth").SetText("-3")
	if fs := d.folders(); fs[0].Depth != 0 {
		t.Fatalf("-3 previews as %d", fs[0].Depth)
	}
	for _, bad := range []string{"-1", "x", "7"} {
		d.entries[0].ed("depth").SetText(bad)
		h.u.saveConfig()
		if h.u.cfg == nil || !strings.HasSuffix(d.err, "its depth is a number from 0 to 6") {
			t.Fatalf("depth %s saved: %q", bad, d.err)
		}
	}
}

// TestFolderSaveOnlyToml: only_toml is saved with the folder and loads back.
func TestFolderSaveOnlyToml(t *testing.T) {
	h := newHarness(t)
	h.u.openConfig(cfgFolders)
	h.u.cfg.set.sub[cfgFolders] = "folders"
	h.u.cfg.entries[0].on["only_toml"] = true
	h.u.cfg.entries[0].on["root"] = true
	h.u.saveConfig()
	if h.u.cfg != nil {
		t.Fatalf("not saved: %q", h.u.cfg.err)
	}
	if fs := orgs.Folders(h.u.root); len(fs) != 1 || !fs[0].OnlyToml || !fs[0].Root {
		t.Fatalf("saved %+v", fs)
	}
	h.u.openConfig(cfgFolders)
	h.u.cfg.set.sub[cfgFolders] = "folders"
	if e := h.u.cfg.entries[0]; !e.on["only_toml"] || !e.on["root"] || e.edited() || !strings.HasSuffix(e.text("path"), filepath.Base(h.u.root)) {
		t.Fatal("did not load back")
	}
}
