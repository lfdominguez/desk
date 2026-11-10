package ui

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"gioui.org/io/key"
)

// TestWorkspaceQuickOpen: Ctrl+P in a workspace (from its editor too) opens
// the palette on its projects' files, hidden folders left out; the best
// match opens in a tab. Elsewhere the palette has no files.
func TestWorkspaceQuickOpen(t *testing.T) {
	h, st, _ := wsFindTab(t, "x\n")
	c := h.u.b.Card(st.id)
	dir := h.u.projects(c)[0]
	for _, d := range []string{"pkg", ".worktrees/copy", "node_modules/dep"} {
		os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	for _, f := range []string{"pkg/handler.go", ".worktrees/copy/handler.go", "node_modules/dep/index.js"} {
		writeFile(t, filepath.Join(dir, f), "x\n")
	}
	h.send(press("P", key.ModShortcut)) // the editor has the keys
	p := h.u.pal
	if p == nil || p.scope != palFile || p.scopes() != len(palScopes) {
		t.Fatal("Ctrl+P: no palette on the files")
	}
	h.send(press(key.NameTab, 0)) // past Files: round to All
	if p.scope != 0 {
		t.Fatalf("Tab past Files: scope %d", p.scope)
	}
	var names []string
	for _, it := range p.files {
		names = append(names, it.title)
		if it.kind != palFile {
			t.Errorf("%s is kind %d", it.title, it.kind)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"f.txt", "handler.go", "main.go"}) {
		t.Fatalf("files %v", names)
	}
	_, order := h.u.palRows("hand", palFile)
	if len(order) != 1 || order[0].sub != "· api/pkg" || order[0].preview[0][1] != filepath.Join(dir, "pkg", "handler.go") {
		t.Fatalf("%d found, sub %q", len(order), order[0].sub)
	}
	if rows, _ := h.u.palRows("", 0); !slices.ContainsFunc(rows, func(r palRow) bool { return r.head == "Files" }) {
		t.Fatal("no Files section with no query")
	}
	h.u.palPick(order[0])
	if tb := st.tab(); h.u.pal != nil || tb == nil || tb.path != filepath.Join(dir, "pkg", "handler.go") {
		t.Fatal("not opened in a tab")
	}
	h.u.view = ""
	h.u.openPalette()
	if h.u.pal.files != nil || h.u.pal.scopes() != palFile {
		t.Fatal("files outside a workspace")
	}
	if h.u.wsFileItems("gone") != nil {
		t.Fatal("files of a card gone")
	}
}
