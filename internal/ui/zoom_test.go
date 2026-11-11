package ui

import (
	"github.com/lfdominguez/desk/internal/testwait"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/io/key"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/term"
)

// TestZoomKeys: Ctrl+=, Ctrl+- and Ctrl+0 size the text from the board and
// over a focused editor, and say the size.
func TestZoomKeys(t *testing.T) {
	h := newHarness(t)
	h.send(press("=", key.ModShortcut))
	if orgs.Zoom() != 110 || h.u.toast != "Text size 110%" || h.u.toastBad {
		t.Fatalf("Ctrl+=: %d, toast %q", orgs.Zoom(), h.u.toast)
	}
	h.r.Source().Execute(key.FocusCmd{Tag: &h.u.search})
	h.frame()
	if !h.r.Source().Focused(&h.u.search) {
		t.Fatal("the search box has no focus")
	}
	h.send(press("-", key.ModShortcut), press("-", key.ModShortcut), press("-", key.ModShortcut))
	if orgs.Zoom() != 80 {
		t.Fatalf("Ctrl+- in the search box: %d", orgs.Zoom())
	}
	h.send(press("0", key.ModShortcut))
	if orgs.Zoom() != 100 || h.u.toast != "Text size 100%" {
		t.Fatalf("Ctrl+0: %d, toast %q", orgs.Zoom(), h.u.toast)
	}
}

// TestZoomServers: in the servers' view, which keeps desk's other keys off,
// the zoom keys still work with no pane focused.
func TestZoomServers(t *testing.T) {
	h := newHarness(t)
	h.u.view = "srv:Acme"
	h.frame()
	h.send(press("=", key.ModShortcut))
	if orgs.Zoom() != 110 {
		t.Fatalf("Ctrl+= in the servers' view: %d", orgs.Zoom())
	}
	h.send(press(key.NameEscape, 0)) // esc stays the panes'
	if h.u.view != "srv:Acme" {
		t.Fatalf("esc left the servers: %q", h.u.view)
	}
}

// TestZoomUnwritable: a settings file that can't be written says so.
func TestZoomUnwritable(t *testing.T) {
	h := newHarness(t)
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o644)
	t.Setenv("XDG_CONFIG_HOME", file)
	h.send(press("=", key.ModShortcut))
	if !h.u.toastBad || h.u.toast == "Text size 110%" {
		t.Fatalf("toast %q bad %v", h.u.toast, h.u.toastBad)
	}
}

// TestZoomSettings: the General page's Text size stepper sizes at once.
func TestZoomSettings(t *testing.T) {
	h := newHarness(t)
	h.u.openConfig(cfgFolders)
	h.clickButton("zoom+")
	if orgs.Zoom() != 110 {
		t.Fatalf("+: %d", orgs.Zoom())
	}
	h.clickButton("zoom-")
	h.clickButton("zoom-")
	if orgs.Zoom() != 90 || h.u.cfg == nil {
		t.Fatalf("−: %d, dialog open %v", orgs.Zoom(), h.u.cfg != nil)
	}
}

// TestZoomTerminal: the zoom keys reach a card's terminal before its
// program, and the bigger text gives it fewer columns.
func TestZoomTerminal(t *testing.T) {
	h := newHarness(t)
	startTerm = func(cmd *exec.Cmd, w, ht int, notify func()) (*term.Term, error) {
		return term.Start(prog("cat"), w, ht, notify)
	}
	defer func() { startTerm = term.Start }()
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	h.u.sel = c.ID
	h.send(press("T", 0))
	tt := h.u.terms[c.ID]
	if tt == nil {
		t.Fatal("no terminal")
	}
	h.frame()
	cols := tt.cols
	h.send(press("=", key.ModShortcut), press("=", key.ModShortcut))
	for end := time.Now().Add(testwait.Long); tt.cols == cols && time.Now().Before(end); {
		h.frame()
	}
	if orgs.Zoom() != 120 || tt.cols >= cols || h.u.view != "term:"+c.ID {
		t.Fatalf("zoom %d, columns %d → %d, view %q", orgs.Zoom(), cols, tt.cols, h.u.view)
	}
	h.u.sel = c.ID
	h.send(press(key.NameEscape, 0)) // only the zoom keys are desk's there: esc stays Claude Code's
	if h.u.view != "term:"+c.ID || h.u.sel != c.ID {
		t.Fatalf("esc was desk's: view %q, selection %q", h.u.view, h.u.sel)
	}
}
