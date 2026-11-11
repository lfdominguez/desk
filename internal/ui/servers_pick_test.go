package ui

import (
	"fmt"
	"github.com/lfdominguez/desk/internal/testwait"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/io/key"

	"github.com/lfdominguez/desk/internal/infra"
)

// TestPickAt: a picker row's list index counts the group headers above it.
func TestPickAt(t *testing.T) {
	rows := []pickRow{{}, {"web", infra.Host{Name: "a"}}, {"web", infra.Host{Name: "b"}}, {"db", infra.Host{Name: "c"}}}
	for i, want := range []int{0, 2, 3, 5} {
		if got := pickAt(rows, i); got != want {
			t.Errorf("row %d: list index %d, want %d", i, got, want)
		}
	}
}

// pickerHarness opens Acme's server picker over a servers file of n hosts
// in group web.
func pickerHarness(t *testing.T, n int) *harness {
	t.Helper()
	for _, v := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "XDG_CACHE_HOME"} {
		t.Setenv(v, t.TempDir()) // never the user's infra session
	}
	h := newHarness(t)
	var b strings.Builder
	b.WriteString("#@ group web\n")
	for i := range n {
		fmt.Fprintf(&b, "Host h%02d\n", i)
	}
	os.MkdirAll(filepath.Join(h.u.root, "Acme", ".desk"), 0o755)
	if err := os.WriteFile(filepath.Join(h.u.root, "Acme", ".desk", "ssh_config"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	h.u.infra = infra.New(h.u.root, func() {})
	t.Cleanup(h.u.infra.Close)
	h.frame()
	h.u.pickSel = 5 // a picker opened anew starts at the top
	now := time.Now()
	h.u.serversClicked("Acme", now)
	h.u.serversClicked("Acme", now)
	h.frame()
	if h.u.pick != "Acme" || h.u.pickSel != 0 {
		t.Fatalf("picker %q, sel %d", h.u.pick, h.u.pickSel)
	}
	return h
}

// TestPickerKeys: up and down move the picker's selection within its rows,
// page up and down by a page, and a new search starts it at the top.
func TestPickerKeys(t *testing.T) {
	h := pickerHarness(t, 30)
	h.send(press(key.NameDownArrow, 0), press(key.NameDownArrow, 0))
	if h.u.pickSel != 2 {
		t.Fatalf("down twice: %d", h.u.pickSel)
	}
	h.send(press(key.NameUpArrow, 0), press(key.NameUpArrow, 0), press(key.NameUpArrow, 0))
	if h.u.pickSel != 0 {
		t.Fatalf("up past the top: %d", h.u.pickSel)
	}
	h.send(press(key.NamePageDown, 0))
	if h.u.pickSel != pickPage {
		t.Fatalf("page down: %d", h.u.pickSel)
	}
	h.send(press(key.NamePageUp, 0))
	if h.u.pickSel != 0 {
		t.Fatalf("page up: %d", h.u.pickSel)
	}
	for range 40 {
		h.r.Queue(press(key.NameDownArrow, 0))
	}
	h.frame()
	if h.u.pickSel != 30 { // the org's Claude and 30 hosts
		t.Fatalf("down past the end: %d", h.u.pickSel)
	}
	h.send(key.Event{Name: key.NameUpArrow, State: key.Release})
	if h.u.pickSel != 30 {
		t.Fatalf("a release moved it: %d", h.u.pickSel)
	}
	h.u.pickQ.SetText("h1")
	h.frame()
	if h.u.pickSel != 0 {
		t.Fatalf("a new search kept the selection: %d", h.u.pickSel)
	}
	h.u.pickSel = 99 // fewer rows than it was at: kept inside them
	h.frame()
	if n := len(h.u.pickRows("Acme", "h1")); h.u.pickSel != n-1 {
		t.Fatalf("sel %d of %d rows", h.u.pickSel, n)
	}
}

// TestPickerReveal: moving down keeps the selected row in the list's view,
// and back at the top shows the first row again.
func TestPickerReveal(t *testing.T) {
	h := pickerHarness(t, 60)
	if h.u.pickList.Position.First != 0 {
		t.Fatalf("opened scrolled: %d", h.u.pickList.Position.First)
	}
	rows := h.u.pickRows("Acme", "")
	for range 50 {
		h.send(press(key.NameDownArrow, 0))
		at, pos := pickAt(rows, h.u.pickSel), h.u.pickList.Position
		if at < pos.First || at >= pos.First+pos.Count {
			t.Fatalf("sel %d (row %d) out of view: rows %d..%d", h.u.pickSel, at, pos.First, pos.First+pos.Count)
		}
	}
	if h.u.pickList.Position.First == 0 {
		t.Fatal("the list didn't scroll")
	}
	first := h.u.pickList.Position.First
	h.send(press(key.NameUpArrow, 0))
	if h.u.pickList.Position.First != first {
		t.Fatal("the list scrolled for a row in view")
	}
	h.send(press(key.NameHome, 0)) // not the picker's: nothing moves
	for h.u.pickSel > 0 {
		h.send(press(key.NameUpArrow, 0))
	}
	if h.u.pickList.Position.First != 0 {
		t.Fatalf("back at the top, the list starts at %d", h.u.pickList.Position.First)
	}
}

// TestPickerEnter: enter opens the selected server, not the first.
func TestPickerEnter(t *testing.T) {
	for _, k := range []key.Name{key.NameReturn, key.NameEnter} {
		h := pickerHarness(t, 3)
		h.send(press(key.NameDownArrow, 0), press(key.NameDownArrow, 0))
		h.send(press(k, 0))
		if h.u.pick != "" || h.u.view != "srv:Acme" {
			t.Fatalf("%s: picker %q, view %q", k, h.u.pick, h.u.view)
		}
		open := func() bool {
			for _, o := range h.u.infra.Tree() {
				for _, g := range o.Groups {
					for _, x := range g.Hosts {
						if x.Open {
							return x.Name == "h01"
						}
					}
				}
			}
			return false
		}
		for end := time.Now().Add(testwait.Long); time.Now().Before(end) && !open(); time.Sleep(50 * time.Millisecond) {
			h.frame()
		}
		if !open() {
			t.Fatalf("%s: h01 isn't the one open", k)
		}
	}
}
