package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"

	"github.com/lfdominguez/desk/internal/orgs"
)

// TestWorkspacePanesDrag: the tree's and the chat's edges drag their widths,
// kept in their bounds and in the settings on release.
func TestWorkspacePanesDrag(t *testing.T) {
	h := newHarness(t)
	c, dir := wsCard(h)
	h.u.openWS(c.ID)
	row := h.areaOf("wsRow" + dir)
	tree := float32(row.Max.X + 6) // the tree's right edge: its rows' inset
	y := float32(row.Max.Y + 200)
	drag := func(x, dx float32) { // over two frames: the edge moves under the pointer between them
		a, mid, b := f32.Pt(x, y), f32.Pt(x+dx/2, y), f32.Pt(x+dx, y)
		h.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: a},
			pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: a},
			pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: mid})
		h.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: b},
			pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: b})
	}
	drag(tree-2, 100) // inside the grab, left of the line
	if h.u.wsEd.Tree != 360 || orgs.EditorSettings().Tree != 360 || h.u.wsGrips[0].dragging {
		t.Fatalf("tree %d, saved %d", h.u.wsEd.Tree, orgs.EditorSettings().Tree)
	}
	if r := h.areaOf("wsRow" + dir); float32(r.Max.X+6) != tree+100 {
		t.Fatalf("the tree drawn %d wide, not as dragged", r.Max.X+6)
	}
	chat := float32(h.size.X - wsChatDef - 1) // the chat's left edge
	drag(chat+3, 50)                          // right of the line: over the chat
	if h.u.wsEd.Chat != 350 || orgs.EditorSettings().Chat != 350 {
		t.Fatalf("chat %d, saved %d", h.u.wsEd.Chat, orgs.EditorSettings().Chat)
	}
	drag(tree+100, 2000)
	drag(chat+50, -2000)
	if h.u.wsEd.Tree != wsTreeMax || h.u.wsEd.Chat != wsChatMax {
		t.Fatalf("past the bounds: tree %d, chat %d", h.u.wsEd.Tree, h.u.wsEd.Chat)
	}
	drag(tree+100+float32(wsTreeMax-360), -2000)
	if h.u.wsEd.Tree != wsTreeMin {
		t.Fatalf("under the least: tree %d", h.u.wsEd.Tree)
	}
}

// TestWorkspacePanesNarrow: in a window too narrow for all three, the chat
// and then the tree give way, the lines between them counted.
func TestWorkspacePanesNarrow(t *testing.T) {
	h := newHarness(t)
	c, dir := wsCard(h)
	h.u.openWS(c.ID)
	left := h.areaOf("wsRow"+dir).Min.X - 6 // the tree's left edge: what's left of it doesn't change
	h.size.X = left + 200 + wsChatMin + wsCenterMin + 2
	if r := h.areaOf("wsRow" + dir); r.Max.X+6-left != 200 {
		t.Fatalf("the tree %d wide, want 200 (the chat at its least, 300)", r.Max.X+6-left)
	}
}

// TestWorkspacePanesSaved: the widths come from the settings at start, and a
// setting that can't be written says so.
func TestWorkspacePanesSaved(t *testing.T) {
	h := newHarness(t)
	orgs.SetEditor(orgs.Editor{Tree: 300})
	if u := newUI(h.u.b, h.u.path); u.wsEd.Tree != 300 {
		t.Fatalf("read %d", u.wsEd.Tree)
	}
	os.WriteFile(orgs.SettingsFile(), []byte("folder = [broken"), 0o644)
	h.u.wsSaveEditor()
	if !h.u.toastBad || !strings.Contains(h.u.toast, "layout wasn't saved") {
		t.Fatalf("toast %q", h.u.toast)
	}
}

// TestWorkspaceWrap: Alt+Z in the editor wraps every tab (new ones too) and
// keeps it; the foot's Wrap turns it off again.
func TestWorkspaceWrap(t *testing.T) {
	h := newHarness(t)
	c, dir := wsCard(h)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\n"), 0o644)
	h.u.openWS(c.ID)
	st := h.u.wsOf(c.ID)
	a := h.u.wsOpen(st, filepath.Join(dir, "main.go"))
	h.frame()
	if a.ed.TextView().WrapLine {
		t.Fatal("wraps before asked")
	}
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: editorAt},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: editorAt})
	text := a.ed.Text()
	h.send(press("Z", key.ModAlt))
	if !h.u.wsEd.Wrap || !a.ed.TextView().WrapLine || !orgs.EditorSettings().Wrap || a.ed.Text() != text {
		t.Fatalf("Alt+Z: wrap %v, tab %v, saved %v, text changed %v", h.u.wsEd.Wrap, a.ed.TextView().WrapLine, orgs.EditorSettings().Wrap, a.ed.Text() != text)
	}
	b := h.u.wsOpen(st, filepath.Join(dir, "b.go"))
	if !b.ed.TextView().WrapLine {
		t.Fatal("a tab opened after doesn't wrap")
	}
	h.clickButton("wsWrap")
	if h.u.wsEd.Wrap || a.ed.TextView().WrapLine || b.ed.TextView().WrapLine || orgs.EditorSettings().Wrap {
		t.Fatal("the foot's Wrap didn't turn it off everywhere")
	}
}

// TestWorkspaceTabsScroll: more tabs than fit scroll sideways, the one shown
// brought into view; a scroll by hand stays until another tab is shown.
func TestWorkspaceTabsScroll(t *testing.T) {
	h := newHarness(t)
	c, dir := wsCard(h)
	h.u.openWS(c.ID)
	st := h.u.wsOf(c.ID)
	var last string
	for i := range 40 {
		last = filepath.Join(dir, fmt.Sprintf("file%02d.go", i))
		os.WriteFile(last, []byte("package main\n"), 0o644)
		h.u.wsOpen(st, last)
	}
	h.frame()
	if st.tabBar.Position.First == 0 {
		t.Fatal("the last tab opened isn't scrolled into view")
	}
	h.areaOf("wsTab" + last) // on screen, clickable
	st.tabBar.Position.First = 3
	h.frame()
	if st.tabBar.Position.First != 3 {
		t.Fatalf("a scroll by hand undone: first %d", st.tabBar.Position.First)
	}
	st.cur = 0
	h.frame()
	if st.tabBar.Position.First != 0 {
		t.Fatalf("the first tab shown, the bar at %d", st.tabBar.Position.First)
	}
	st.cur = 1 // in view already: the bar stays
	h.frame()
	if st.tabBar.Position.First != 0 || st.shown != 1 {
		t.Fatalf("first %d, shown %d", st.tabBar.Position.First, st.shown)
	}
}

// TestWorkspaceTreeFollows: a file the card's Claude writes shows in the open
// folders above it (only those), and opening the workspace reads them all
// again.
func TestWorkspaceTreeFollows(t *testing.T) {
	h := newHarness(t)
	c, dir := wsCard(h)
	pkg, other := filepath.Join(dir, "pkg"), filepath.Join(dir, "other")
	os.MkdirAll(pkg, 0o755)
	os.MkdirAll(other, 0o755)
	h.u.openWS(c.ID)
	st := h.u.wsOf(c.ID)
	h.u.wsToggle(st, pkg)
	h.u.wsToggle(st, other)
	names := func(d string) (out []string) {
		for _, e := range st.kids[d] {
			out = append(out, e.name)
		}
		return out
	}
	os.WriteFile(filepath.Join(pkg, "new.go"), nil, 0o644)
	os.WriteFile(filepath.Join(other, "later.go"), nil, 0o644)
	h.u.wsTouched(c.ID, filepath.Join(pkg, "new.go"))
	if !slices.Contains(names(pkg), "new.go") {
		t.Fatalf("pkg %v", names(pkg))
	}
	if len(names(other)) != 0 {
		t.Fatal("read a folder the file isn't in")
	}
	h.u.openSession(c.ID)
	h.u.openWS(c.ID)
	if !slices.Contains(names(other), "later.go") {
		t.Fatalf("other %v after opening the workspace", names(other))
	}
	h.u.wsTouched("gone", filepath.Join(pkg, "x.go")) // no workspace: nothing to do
}

// TestWorkspaceSaveLink: saving a file opened through a symlink writes the
// file it points at and leaves the link a link.
func TestWorkspaceSaveLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	h := newHarness(t)
	c, dir := wsCard(h)
	target, link := filepath.Join(dir, "main.go"), filepath.Join(dir, "link.go")
	os.Symlink("main.go", link)
	tb := h.u.wsOpen(h.u.wsOf(c.ID), link)
	tb.ed.SetText("package linked\n")
	h.u.wsSave(tb)
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link was replaced by a file")
	}
	if b, _ := os.ReadFile(target); string(b) != "package linked\n" {
		t.Fatalf("the linked file holds %q", b)
	}
}

// TestWsFit: the chat gives way first, down to its least, then the tree,
// so the editor keeps its least while it can.
func TestWsFit(t *testing.T) {
	for _, c := range []struct{ total, tree, chat, wantTree, wantChat int }{
		{2000, 260, 400, 260, 400}, // room for all
		{980, 260, 400, 260, 400},  // just enough
		{900, 260, 400, 260, 320},  // the chat gives
		{800, 260, 400, 180, 300},  // the chat at its least, the tree gives
		{600, 260, 400, 160, 300},  // both at their least: the editor gets less
		{700, 300, 290, 160, 290},  // a chat already under its least isn't widened: the tree gives
	} {
		if tr, ch := wsFit(c.total, c.tree, c.chat, 160, 300, 320); tr != c.wantTree || ch != c.wantChat {
			t.Errorf("wsFit(%d, %d, %d) = %d, %d; want %d, %d", c.total, c.tree, c.chat, tr, ch, c.wantTree, c.wantChat)
		}
	}
	if wsSide(0, 260, 160, 480) != 260 || wsSide(100, 260, 160, 480) != 160 || wsSide(900, 260, 160, 480) != 480 || wsSide(300, 260, 160, 480) != 300 {
		t.Error("wsSide")
	}
}
