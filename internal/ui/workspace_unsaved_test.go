package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lfdominguez/desk/internal/kanban"
)

// reopen is desk started again over the same board: no workspaces open, the
// kept tabs read back.
func (h *harness) reopen() {
	h.u.ws = map[string]*wsState{}
	h.u.unsaved = loadUnsaved(h.u.path)
}

// TestWorkspaceUnsavedKept: quitting keeps the tabs with changes not saved
// (only those, in a file only the user reads); the workspace opened again
// puts them back as they were, and a save with nothing else left removes it.
func TestWorkspaceUnsavedKept(t *testing.T) {
	h := newHarness(t)
	c, dir := wsCard(h)
	os.WriteFile(filepath.Join(dir, "clean.go"), []byte("package main\n"), 0o640)
	st := h.u.wsOf(c.ID)
	h.u.wsOpen(st, filepath.Join(dir, "clean.go"))
	tb := h.u.wsOpen(st, filepath.Join(dir, "main.go"))
	tb.ed.SetText("package main // mine\n")
	tb.dirty = true
	h.u.quitting()
	p := unsavedFile(h.u.path)
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("not kept for the user only: %v", err)
	}
	h.reopen()
	if len(h.u.unsaved) != 1 {
		t.Fatalf("kept %d tabs, want the one changed", len(h.u.unsaved))
	}
	st = h.u.wsOf(c.ID)
	if len(st.tabs) != 2 || len(h.u.unsaved) != 0 || st.tabs[0].dirty {
		t.Fatalf("%d tabs back (the clean one as on disk), %d still waiting", len(st.tabs), len(h.u.unsaved))
	}
	back := st.tab() // the kept one, over its tab reopened from the card, shown
	if len(st.tabs) != 2 || back != st.tabs[1] || back.gen == 0 {
		t.Fatal("the kept text didn't land on the reopened tab")
	}
	if back.path != tb.path || back.ed.Text() != "package main // mine\n" || !back.dirty || back.disk || back.mode != 0o640 || !back.mtime.Equal(tb.mtime) {
		t.Fatalf("put back as %q dirty %v disk %v mode %v", back.ed.Text(), back.dirty, back.disk, back.mode)
	}
	if !strings.Contains(h.u.toast, "Put back 1 file") || h.u.toastBad {
		t.Fatalf("toast %q", h.u.toast)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("dropped from the file once put back: a crash now would lose it")
	}
	h.u.wsSave(back)
	h.u.wsStash()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("the file stays with nothing left to keep")
	}
	h.reopen()
	for _, tb := range h.u.wsOf(c.ID).tabs {
		if tb.dirty || h.u.unsaved != nil {
			t.Fatalf("put back %s, already saved", tb.path)
		}
	}
}

// TestWorkspaceTabsKept: the files open are kept on the card (in the board
// file) and open again with its workspace; one gone from disk is skipped.
func TestWorkspaceTabsKept(t *testing.T) {
	h := newHarness(t)
	c, dir := wsCard(h)
	a, b, gone := filepath.Join(dir, "main.go"), filepath.Join(dir, "b.go"), filepath.Join(dir, "gone.go")
	os.WriteFile(b, []byte("package main\n"), 0o644)
	os.WriteFile(gone, []byte("package main\n"), 0o644)
	st := h.u.wsOf(c.ID)
	for _, p := range []string{a, gone, b, a} { // a again: its tab shown, not a second one
		h.u.wsOpen(st, p)
	}
	h.u.wsCloseTab(st, 0)
	if want := []string{gone, b}; !slices.Equal(h.u.b.Card(c.ID).Tabs, want) {
		t.Fatalf("kept %v, want %v", h.u.b.Card(c.ID).Tabs, want)
	}
	if saved, err := kanban.Load(h.u.path); err != nil || len(saved.Card(c.ID).Tabs) != 2 {
		t.Fatalf("not in the board file: %v", err)
	}
	os.Remove(gone)
	h.reopen()
	st = h.u.wsOf(c.ID)
	if len(st.tabs) != 1 || st.tabs[0].path != b || h.u.toastBad {
		t.Fatalf("%d tabs, toast %q", len(st.tabs), h.u.toast)
	}
	if !slices.Equal(h.u.b.Card(c.ID).Tabs, []string{b}) {
		t.Fatalf("the gone file still kept: %v", h.u.b.Card(c.ID).Tabs)
	}
}

// TestWorkspaceUnsavedChangedOnDisk: a file changed while desk was closed
// comes back with the bar asking Reload or Keep mine.
func TestWorkspaceUnsavedChangedOnDisk(t *testing.T) {
	h := newHarness(t)
	c, dir := wsCard(h)
	p := filepath.Join(dir, "main.go")
	tb := h.u.wsOpen(h.u.wsOf(c.ID), p)
	tb.dirty = true
	h.u.quitting()
	os.Chtimes(p, time.Now(), tb.mtime.Add(time.Hour))
	h.reopen()
	if back := h.u.wsOf(c.ID).tabs[0]; !back.disk || !back.dirty {
		t.Fatalf("disk %v dirty %v", back.disk, back.dirty)
	}
}

// TestWorkspaceUnsavedOtherCards: another card's kept tabs wait for its own
// workspace, and stay kept meanwhile.
func TestWorkspaceUnsavedOtherCards(t *testing.T) {
	h := newHarness(t)
	c, dir := wsCard(h)
	h.u.unsaved = []wsUnsaved{{Card: "other", Path: filepath.Join(dir, "gone.go"), Text: "x", Mode: 0o644}}
	tb := h.u.wsOpen(h.u.wsOf(c.ID), filepath.Join(dir, "main.go"))
	if len(h.u.wsOf(c.ID).tabs) != 1 || len(h.u.unsaved) != 1 {
		t.Fatal("put another card's tab back here")
	}
	tb.dirty = true
	h.u.wsStash()
	h.reopen()
	if len(h.u.unsaved) != 2 {
		t.Fatalf("kept %d, want both cards' tabs", len(h.u.unsaved))
	}
	h.u.ws = map[string]*wsState{}
	if back := h.u.wsOf("other").tabs; len(back) != 1 || back[0].ed.Text() != "x" || back[0].disk {
		t.Fatal("a file gone from disk didn't come back as kept") // nothing on disk to compare: no bar
	}
}

// TestWorkspaceUnsavedNotWritten: a stash that can't be written says so.
func TestWorkspaceUnsavedNotWritten(t *testing.T) {
	h := newHarness(t)
	c, dir := wsCard(h)
	h.u.wsOpen(h.u.wsOf(c.ID), filepath.Join(dir, "main.go")).dirty = true
	h.u.path = filepath.Join(t.TempDir(), "missing", "board.json")
	h.u.wsStash()
	if !h.u.toastBad || !strings.Contains(h.u.toast, "weren't kept") {
		t.Fatalf("toast %q", h.u.toast)
	}
	if loadUnsaved(h.u.path) != nil {
		t.Fatal("read something from no file")
	}
	os.MkdirAll(filepath.Dir(h.u.path), 0o755)
	os.WriteFile(unsavedFile(h.u.path), []byte("not json"), 0o600)
	if loadUnsaved(h.u.path) != nil {
		t.Fatal("read something from a broken file")
	}
}
