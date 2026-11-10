package ui

import (
	"os"
	"strings"
	"testing"

	"gioui.org/f32"
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
