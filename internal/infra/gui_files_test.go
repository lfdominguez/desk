package infra

import (
	"context"
	"errors"
	"fmt"
	"github.com/lfdominguez/desk/internal/testwait"
	"image"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"github.com/lfdominguez/desk/internal/keys"
)

// --- gui_files.go: the directory split, driven through Gio's router on the
// "local" host (the server side is this machine, through sh) ---

// gfH runs a tab's directory split headlessly: the server side starts in
// srv, this machine's in loc (under HOME, so it shows as ~).
type gfH struct {
	t        *testing.T
	u        *ui
	fv       *filesView
	tab      *Tab
	term     *Term
	r        input.Router
	ops      op.Ops
	size     image.Point
	now      time.Duration
	home     string
	srv, loc string
	mu       sync.Mutex
	opened   []string
	openErr  error
	y0, rowH int
}

func gfWrite(t *testing.T, p, s string, mode os.FileMode) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), mode); err != nil {
		t.Fatal(err)
	}
	os.Chmod(p, mode)
}

// gfNew makes srv (alpha/, beta/inner.txt, notes.txt, run.sh, .hidden) and
// loc (ldir/, local.txt) and opens the split on them.
func gfNew(t *testing.T) *gfH {
	h := &gfH{t: t, size: image.Pt(1000, 600)}
	h.home = t.TempDir()
	t.Chdir(t.TempDir()) // the tests that unset HOME make the cache dir relative: keep it out of the package
	t.Setenv("HOME", h.home)
	t.Setenv("USERPROFILE", h.home) // Windows' home
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	h.srv, h.loc = t.TempDir(), filepath.Join(h.home, "loc")
	os.Mkdir(filepath.Join(h.srv, "alpha"), 0o755)
	gfWrite(t, filepath.Join(h.srv, "beta", "inner.txt"), "in", 0o644)
	gfWrite(t, filepath.Join(h.srv, "notes.txt"), "hello", 0o644)
	gfWrite(t, filepath.Join(h.srv, "run.sh"), "#!/bin/sh\n", 0o755)
	gfWrite(t, filepath.Join(h.srv, ".hidden"), "h", 0o600)
	os.MkdirAll(filepath.Join(h.loc, "ldir"), 0o755)
	gfWrite(t, filepath.Join(h.loc, "local.txt"), "local!", 0o644)

	oldOpen, oldLast := openLocal, lastLocalDir
	openLocal = func(p string) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.opened = append(h.opened, p)
		return h.openErr
	}
	lastLocalDir = h.loc
	h.term = &Term{Host: "local", cwd: h.srv}
	h.tab = &Tab{}
	h.u = newUI(&driver{}, guiLayout{}, func() {})
	h.u.st = &guiState{Tabs: []guiTab{{Key: h.tab, Panes: []guiPane{{Term: h.term, Focused: true, Name: "web1"}}}}}
	t.Cleanup(func() {
		h.settle()
		openLocal, lastLocalDir = oldOpen, oldLast
	})
	h.u.openFiles()
	h.fv = h.u.files[h.tab]
	if h.fv == nil {
		t.Fatal("no split opened")
	}
	h.until("both sides listed", func() bool { return h.fv.sides[0].path == h.srv && h.fv.sides[1].path == h.loc })
	h.frame()
	return h
}

// settle waits for listings and copies still running, so temp dirs go away
// after them.
func (h *gfH) settle() {
	if h.fv == nil {
		return
	}
	for end := time.Now().Add(testwait.Long); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		h.fv.mu.Lock()
		busy := h.fv.sides[0].loading || h.fv.sides[1].loading || h.fv.job != nil
		h.fv.mu.Unlock()
		if !busy {
			return
		}
	}
}

func (h *gfH) until(what string, f func() bool) {
	h.t.Helper()
	for end := time.Now().Add(testwait.Long); ; time.Sleep(5 * time.Millisecond) {
		h.fv.mu.Lock()
		ok := f()
		h.fv.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(end) {
			h.t.Fatalf("timed out waiting for %s (toast %q)", what, h.u.toast)
		}
	}
}

// listed waits until side shows dir, done loading, with every name in it.
func (h *gfH) listed(side int, dir string, names ...string) {
	h.t.Helper()
	h.until(fmt.Sprintf("side %d at %s with %v", side, dir, names), func() bool {
		s := h.fv.sides[side]
		if s.loading || s.path != dir {
			return false
		}
		for _, n := range names {
			if !slices.ContainsFunc(s.entries, func(e fileEntry) bool { return e.Name == n }) {
				return false
			}
		}
		return true
	})
}

// names doesn't lock: until's conditions call it with mu held.
func (h *gfH) names(side int) []string {
	var out []string
	for _, e := range h.fv.sides[side].entries {
		out = append(out, e.Name)
	}
	return out
}

func (h *gfH) sel(side int) string {
	h.fv.mu.Lock()
	defer h.fv.mu.Unlock()
	s := h.fv.sides[side]
	if s.sel >= len(s.entries) {
		return ""
	}
	return s.entries[s.sel].Name
}

// selectName moves side's selection onto name and makes it active.
func (h *gfH) selectName(side int, name string) {
	h.t.Helper()
	h.fv.mu.Lock()
	defer h.fv.mu.Unlock()
	h.fv.active = side
	s := h.fv.sides[side]
	for i, e := range s.entries {
		if e.Name == name {
			s.sel, s.anchor = i, i
			return
		}
	}
	h.t.Fatalf("no %s on side %d: %+v", name, side, s.entries)
}

func (h *gfH) toast() string { return h.u.toast }

func (h *gfH) waitToast(sub string) {
	h.t.Helper()
	h.until("toast "+sub, func() bool { return strings.Contains(h.u.toast, sub) })
}

func (h *gfH) gtx() layout.Context {
	return layout.Context{Ops: &h.ops, Source: h.r.Source(), Now: time.Now(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(h.size)}
}

func (h *gfH) frame() {
	h.ops.Reset()
	h.u.filesPanels(h.gtx(), h.fv)
	h.r.Frame(&h.ops)
}

func (h *gfH) send(evs ...event.Event) {
	h.r.Queue(evs...)
	h.frame()
	h.frame()
}

func (h *gfH) key(n key.Name, m key.Modifiers) {
	h.send(key.Event{Name: n, Modifiers: m, State: key.Press}, key.Event{Name: n, Modifiers: m, State: key.Release})
}

// click presses a button by id, as if clicked.
func (h *gfH) click(id string) {
	h.u.click(id).Click()
	h.frame()
}

func (h *gfH) tick() time.Duration { h.now += time.Second; return h.now }

func (h *gfH) ptr(k pointer.Kind, at f32.Point, b pointer.Buttons, m key.Modifiers) pointer.Event {
	return pointer.Event{Kind: k, Source: pointer.Mouse, Position: at, Buttons: b, Modifiers: m, Time: h.tick()}
}

// calibrate finds where the rows are by hovering down the server side.
func (h *gfH) calibrate() {
	h.t.Helper()
	if h.rowH != 0 {
		return
	}
	s := h.fv.sides[0]
	hovered := func(i int) bool {
		rs := s.rows[s.entries[i].Name]
		return rs != nil && rs.click.Hovered()
	}
	for y := 0; y < h.size.Y && h.rowH == 0; y++ {
		h.r.Queue(h.ptr(pointer.Move, f32.Pt(100, float32(y)), 0, 0))
		h.frame()
		switch {
		case h.y0 == 0 && hovered(0):
			h.y0 = y
		case h.y0 != 0 && hovered(1):
			h.rowH = y - h.y0
		}
	}
	if h.rowH == 0 {
		h.t.Fatal("rows not found")
	}
}

func (h *gfH) rowPt(side, i int) f32.Point {
	h.calibrate()
	x := 100
	if side == 1 {
		x += h.size.X / 2
	}
	return f32.Pt(float32(x), float32(h.y0+i*h.rowH+h.rowH/2))
}

func (h *gfH) clickRow(side, i int, m key.Modifiers) {
	p := h.rowPt(side, i)
	h.send(h.ptr(pointer.Press, p, pointer.ButtonPrimary, m), h.ptr(pointer.Release, p, 0, m))
}

func (h *gfH) index(side int, name string) int {
	return slices.Index(h.names(side), name)
}

// --- opening and closing ---

func TestFilesNeedAFocusedServer(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	u := newUI(&driver{}, guiLayout{}, func() {})
	if u.tabFiles() != nil {
		t.Fatal("no tab, no split")
	}
	u.openFiles()
	if len(u.files) != 0 || !strings.Contains(u.toast, "Open a server first") {
		t.Errorf("no tab: %v %q", u.files, u.toast)
	}
	tab := &Tab{}
	u.st = &guiState{Tabs: []guiTab{{Key: tab, Panes: []guiPane{{Term: &Term{Host: "local"}}, {Focused: true}}}}}
	u.toast = ""
	u.toggleFiles()
	if len(u.files) != 0 || !strings.Contains(u.toast, "Open a server first") {
		t.Errorf("focused pane without a terminal: %v %q", u.files, u.toast)
	}
}

func TestFilesOpenAtHomeWithoutLastDir(t *testing.T) {
	h := gfNew(t)
	h.u.closeFiles(h.fv)
	lastLocalDir = ""
	h.u.toggleFiles()
	h.fv = h.u.tabFiles()
	if h.fv == nil {
		t.Fatal("toggle didn't open")
	}
	h.listed(1, h.home, "loc")
	h.listed(0, h.srv, "alpha")
	if lastLocalDir != h.home {
		t.Errorf("lastLocalDir %q", lastLocalDir)
	}
}

func TestFilesToggleAndCloseAnswersConflict(t *testing.T) {
	h := gfNew(t)
	h.fv.focusNext = false
	h.u.openFiles() // again: focuses it
	if !h.fv.focusNext || !strings.Contains(h.toast(), "already has") || h.u.files[h.tab] != h.fv {
		t.Errorf("second open: %v %q", h.fv.focusNext, h.toast())
	}
	ch := make(chan string, 1)
	h.fv.prompt, h.fv.choice = "conflict", ch
	h.u.wantTerm = false
	h.u.toggleFiles()
	if len(h.u.files) != 0 || !h.u.wantTerm || h.fv.prompt != "" {
		t.Errorf("close: %v %v %q", h.u.files, h.u.wantTerm, h.fv.prompt)
	}
	if c := <-ch; c != "" {
		t.Errorf("conflict answered %q, want cancel", c)
	}
	// an overwrite question too; a full channel doesn't block
	h.u.files[h.tab] = h.fv
	ch <- "x"
	h.fv.prompt = "overwrite"
	h.u.closeFiles(h.fv)
	if h.fv.prompt != "" || <-ch != "x" {
		t.Error("overwrite not answered")
	}
	// another prompt is left alone
	h.u.files[h.tab] = h.fv
	h.fv.prompt = "mkdir"
	h.u.closeFiles(h.fv)
	if h.fv.prompt != "mkdir" {
		t.Error("closing dropped a name prompt")
	}
}

func TestFilesCloseButtonAndEscape(t *testing.T) {
	h := gfNew(t)
	h.click(fmt.Sprintf("closefiles%p", h.fv))
	if len(h.u.files) != 0 {
		t.Fatal("close button left the split")
	}
	h.u.files[h.tab] = h.fv
	h.key(key.NameEscape, 0)
	if len(h.u.files) != 0 {
		t.Fatal("esc left the split")
	}
}

// --- listing ---

func TestFilesListingFallbacks(t *testing.T) {
	h := gfNew(t)
	// the shell's directory is gone: the server side starts at home
	h.u.closeFiles(h.fv)
	h.term.cwd = filepath.Join(h.srv, "gone")
	lastLocalDir = filepath.Join(h.home, "missing")
	h.u.openFiles()
	h.fv = h.u.tabFiles()
	h.listed(0, h.home, "loc")
	h.until("local error", func() bool { return h.fv.sides[1].err != "" && !h.fv.sides[1].loading })
	if s := h.fv.sides[1]; s.path != "" || !strings.Contains(s.err, "missing") {
		t.Errorf("local side: %q %q", s.path, s.err)
	}
	h.frame() // draws the error
	h.u.up(h.fv, h.fv.sides[1])
	if h.fv.sides[1].loading {
		t.Error("up from nowhere listed something")
	}
	// a later failure keeps the listing and toasts
	h.u.loadSide(h.fv, h.fv.sides[0], filepath.Join(h.home, "nope"), "")
	h.waitToast("no such directory")
	h.listed(0, h.home, "loc")
}

func TestFilesSlowerListingDropped(t *testing.T) {
	h := gfNew(t)
	s := h.fv.sides[0]
	h.u.loadSide(h.fv, s, filepath.Join(h.srv, "beta"), "")
	h.u.loadSide(h.fv, s, h.srv, "notes.txt")
	h.listed(0, h.srv, "notes.txt")
	time.Sleep(100 * time.Millisecond)
	if h.sel(0) != "notes.txt" || s.path != h.srv {
		t.Errorf("an older listing won: %q %q", s.path, h.sel(0))
	}
}

func TestFilesRefreshKeepsSelectionAndMarks(t *testing.T) {
	h := gfNew(t)
	h.selectName(0, "run.sh")
	h.fv.sides[0].marked["notes.txt"] = true
	gfWrite(t, filepath.Join(h.srv, "zzz.txt"), "", 0o644)
	h.key("R", key.ModShortcut)
	h.listed(0, h.srv, "zzz.txt")
	if h.sel(0) != "run.sh" || !h.fv.sides[0].marked["notes.txt"] {
		t.Errorf("refresh: sel %q marks %v", h.sel(0), h.fv.sides[0].marked)
	}
	// another directory drops the marks
	h.selectName(0, "beta")
	h.key(key.NameReturn, 0)
	h.listed(0, filepath.Join(h.srv, "beta"), "inner.txt")
	if len(h.fv.sides[0].marked) != 0 || h.sel(0) != ".." {
		t.Errorf("new dir: marks %v sel %q", h.fv.sides[0].marked, h.sel(0))
	}
}

func TestFilesRefilterAndHelpers(t *testing.T) {
	fv := &filesView{}
	s := &fileSide{path: "/", all: []fileEntry{{Name: "b"}, {Name: ".a"}, {Name: "d", Dir: true}}}
	fv.refilter(s, "b")
	if got := fmt.Sprint(s.entries); !strings.HasPrefix(got, "[{d") || len(s.entries) != 2 || s.sel != 1 || s.anchor != 1 || !s.follow {
		t.Errorf("root: %v sel %d", s.entries, s.sel)
	}
	s.path = "/x"
	fv.hidden = true
	fv.refilter(s, "nope")
	if len(s.entries) != 4 || s.entries[0].Name != ".." || s.sel != 0 || s.hiddenCount() != 1 {
		t.Errorf("hidden shown: %v", s.entries)
	}
	if s.join("y") != filepath.FromSlash("/x/y") || s.parent() != filepath.FromSlash("/") {
		t.Error("local join/parent")
	}
	r := &fileSide{remote: true, path: "/srv/a"}
	if r.join("b") != "/srv/a/b" || r.parent() != "/srv" {
		t.Error("remote join/parent")
	}
	s.jump("d")
	if s.entries[s.sel].Name != "d" {
		t.Errorf("prefix jump: %q", s.entries[s.sel].Name)
	}
	s.jump(".")
	if s.entries[s.sel].Name != ".a" {
		t.Errorf("never '..': %q", s.entries[s.sel].Name)
	}
	if c, n := lastRune(""); c != 0 || n != 0 {
		t.Error("lastRune empty")
	}
	if c, n := lastRune("añ"); c != 'ñ' || n != 2 {
		t.Error("lastRune")
	}
	if plural(1) != "1 item" || plural(3) != "3 items" {
		t.Error("plural")
	}
	if !(&filesView{prompt: "chmod"}).editing() || (&filesView{prompt: "delete"}).editing() {
		t.Error("editing")
	}
}

// --- keyboard ---

func TestFilesKeysMoveAndMark(t *testing.T) {
	h := gfNew(t)
	// .., alpha, beta, notes.txt, run.sh
	if got := h.names(0); strings.Join(got, ",") != "..,alpha,beta,notes.txt,run.sh" {
		t.Fatalf("entries %v", got)
	}
	h.key(key.NameDownArrow, 0)
	h.key(key.NameDownArrow, 0)
	if h.sel(0) != "beta" {
		t.Errorf("down twice: %q", h.sel(0))
	}
	h.key(key.NameUpArrow, 0)
	if h.sel(0) != "alpha" {
		t.Errorf("up: %q", h.sel(0))
	}
	h.key(key.NameEnd, 0)
	if h.sel(0) != "run.sh" {
		t.Errorf("end: %q", h.sel(0))
	}
	h.key(key.NameHome, 0)
	if h.sel(0) != ".." {
		t.Errorf("home: %q", h.sel(0))
	}
	h.key(key.NamePageDown, 0)
	if h.sel(0) == ".." {
		t.Error("page down didn't move")
	}
	h.key(key.NamePageUp, 0)
	if h.sel(0) != ".." {
		t.Errorf("page up: %q", h.sel(0))
	}
	// shift+down only moves: Shift alone is no shortcut
	h.key(key.NameDownArrow, key.ModShift)
	if h.sel(0) != ".." || len(trueKeys(h.fv.sides[0].marked)) != 0 {
		t.Errorf("shift+down: sel %q marks %v", h.sel(0), h.fv.sides[0].marked)
	}
	// ctrl+down marks what it passes, never ".."
	h.key(key.NameDownArrow, key.ModShortcut)
	h.key(key.NameDownArrow, key.ModShortcut)
	m := h.fv.sides[0].marked
	if !m["alpha"] || !m["beta"] || m[".."] || m["notes.txt"] || h.fv.sides[0].anchor != 2 {
		t.Errorf("ctrl marks %v", m)
	}
	// space toggles and moves on; rebound to a Ctrl chord, still without
	// marking the next one
	keys.Set("files.mark", keys.Chord{Name: "M", Mods: key.ModShortcut})
	t.Cleanup(func() { keys.Load(nil) })
	h.frame() // the new filters
	h.key(key.NameSpace, 0)
	if !m["beta"] || h.sel(0) != "beta" {
		t.Errorf("space rebound away still marks: %v sel %q", m, h.sel(0))
	}
	h.key("M", key.ModShortcut)
	keys.Load(nil)
	h.frame()
	if m["beta"] || m["notes.txt"] || h.sel(0) != "notes.txt" {
		t.Errorf("mark: %v sel %q", m, h.sel(0))
	}
	h.key(key.NameHome, 0)
	h.key(key.NameSpace, 0) // on "..": nothing
	if h.sel(0) != ".." || len(trueKeys(m)) != 1 {
		t.Errorf("space on ..: %v", m)
	}
	// ctrl+A marks all, again unmarks all
	h.key("A", key.ModShortcut)
	if got := trueKeys(h.fv.sides[0].marked); strings.Join(got, ",") != "alpha,beta,notes.txt,run.sh" {
		t.Errorf("all: %v", got)
	}
	h.key("A", key.ModShortcut)
	if got := trueKeys(h.fv.sides[0].marked); len(got) != 0 {
		t.Errorf("none: %v", got)
	}
	// tab switches sides
	h.key(key.NameTab, 0)
	if h.fv.active != 1 {
		t.Error("tab")
	}
	h.key(key.NameTab, 0)
	// ctrl+H shows dotfiles on both sides, keeping the selection
	h.selectName(0, "run.sh")
	h.key("H", key.ModShortcut)
	if !h.fv.hidden || h.index(0, ".hidden") < 0 || h.sel(0) != "run.sh" {
		t.Errorf("hidden: %v %v", h.fv.hidden, h.names(0))
	}
	h.key(".", key.ModShortcut)
	if h.fv.hidden || h.index(0, ".hidden") >= 0 {
		t.Error("ctrl+. hides again")
	}
	// keys on an empty side
	h.fv.sides[0].entries = nil
	h.key(key.NameDownArrow, key.ModShortcut)
	h.key("H", key.ModShortcut)
	if h.fv.sides[0].sel != 0 {
		t.Error("empty side moved")
	}
}

func trueKeys(m map[string]bool) []string {
	var out []string
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func TestFilesTypeAhead(t *testing.T) {
	h := gfNew(t)
	h.send(key.EditEvent{Text: "No"})
	if h.sel(0) != "notes.txt" || h.fv.search != "no" {
		t.Errorf("prefix: %q %q", h.sel(0), h.fv.search)
	}
	h.frame() // the "find:" hint
	h.fv.searchAt = time.Now().Add(-2 * time.Second)
	h.send(key.EditEvent{Text: "lph"}) // a pause: a new search, matching inside
	if h.sel(0) != "alpha" || h.fv.search != "lph" {
		t.Errorf("contains: %q %q", h.sel(0), h.fv.search)
	}
	h.send(key.EditEvent{Text: "\t"}, key.EditEvent{Text: " "})
	if h.fv.search != "lph" {
		t.Errorf("control or blank typed: %q", h.fv.search)
	}
	h.key(key.NameDeleteBackward, 0)
	if h.fv.search != "lp" || h.sel(0) != "alpha" {
		t.Errorf("backspace in search: %q", h.fv.search)
	}
	h.key(key.NameEscape, 0) // clears the search, keeps the split
	if h.fv.search != "" || len(h.u.files) != 1 {
		t.Errorf("esc: %q %d", h.fv.search, len(h.u.files))
	}
	h.fv.prompt = "delete"
	h.u.typeAhead(h.fv, "r")
	if h.fv.search != "" {
		t.Error("typed into a prompt")
	}
}

func TestFilesEnterUpAndRoot(t *testing.T) {
	h := gfNew(t)
	h.selectName(0, "beta")
	h.key(key.NameEnter, 0)
	h.listed(0, filepath.Join(h.srv, "beta"), "inner.txt")
	h.key(key.NameReturn, 0) // on "..": back, on beta
	h.listed(0, h.srv, "beta")
	if h.sel(0) != "beta" {
		t.Errorf("came back on %q", h.sel(0))
	}
	h.selectName(0, "alpha")
	h.key(key.NameF3, 0)
	h.listed(0, filepath.Join(h.srv, "alpha"))
	h.key(key.NameDeleteBackward, 0)
	h.listed(0, h.srv, "alpha")
	if h.sel(0) != "alpha" {
		t.Errorf("backspace: %q", h.sel(0))
	}
	if exeSuffix != "" {
		return // no one / on Windows: a drive's root each
	}
	h.u.loadSide(h.fv, h.fv.sides[0], "/", "")
	h.listed(0, "/")
	if h.index(0, "..") >= 0 {
		t.Error("/ has a parent row")
	}
	h.u.up(h.fv, h.fv.sides[0])
	if h.fv.sides[0].loading {
		t.Error("up from /")
	}
	h.fv.sides[0].entries = nil
	h.u.open(h.fv, h.fv.sides[0])
	h.u.edit(h.fv, h.fv.sides[0])
	if h.fv.sides[0].loading || h.fv.job != nil {
		t.Error("open with nothing selected did something")
	}
}

// --- opening and editing files ---

func TestFilesOpenFiles(t *testing.T) {
	h := gfNew(t)
	h.selectName(1, "local.txt")
	h.key(key.NameReturn, 0)
	want := filepath.Join(h.loc, "local.txt")
	if len(h.opened) != 1 || h.opened[0] != want {
		t.Fatalf("local open: %v", h.opened)
	}
	h.openErr = errors.New("no opener")
	h.key(key.NameReturn, 0)
	if !strings.Contains(h.toast(), "Open failed: no opener") {
		t.Errorf("toast %q", h.toast())
	}
	// a server file is downloaded first, then opened
	h.openErr = nil
	h.selectName(0, "notes.txt")
	h.key(key.NameReturn, 0)
	cached := filepath.Join(os.Getenv("XDG_CACHE_HOME"), "desk", "files", "local", "notes.txt")
	h.until("opened download", func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.opened) == 3 })
	if h.opened[2] != cached {
		t.Errorf("opened %v", h.opened)
	}
	if b, _ := os.ReadFile(cached); string(b) != "hello" {
		t.Errorf("download %q", b)
	}
	h.settle()
	h.openErr = errors.New("still none")
	h.key(key.NameReturn, 0)
	h.waitToast("Open failed: still none")
	h.settle()
	// no cache dir
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h.loc, "local.txt"))
	h.key(key.NameReturn, 0)
	if !strings.Contains(h.toast(), "Open failed") || h.fv.job != nil {
		t.Errorf("cache in a file: %q", h.toast())
	}
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "") // Windows' home
	h.u.toast = ""
	h.u.open(h.fv, h.fv.sides[0])
	if !strings.Contains(h.toast(), "Open failed") {
		t.Errorf("no cache dir: %q", h.toast())
	}
}

func TestFilesEditLocal(t *testing.T) {
	h := gfNew(t)
	h.selectName(1, "ldir")
	h.key(key.NameF4, 0)
	if len(h.opened) != 0 {
		t.Error("edited a folder")
	}
	h.selectName(1, "local.txt")
	h.key(key.NameF4, 0)
	if len(h.opened) != 1 || h.opened[0] != filepath.Join(h.loc, "local.txt") {
		t.Errorf("edit: %v", h.opened)
	}
	h.openErr = errors.New("no editor")
	h.key(key.NameF4, 0)
	if !strings.Contains(h.toast(), "Edit failed: no editor") {
		t.Errorf("toast %q", h.toast())
	}
}

func TestFilesEditRemoteFailures(t *testing.T) {
	h := gfNew(t)
	h.selectName(0, "notes.txt")
	os.Remove(filepath.Join(h.srv, "notes.txt")) // gone since the listing
	h.key(key.NameF4, 0)
	h.waitToast("Edit failed")
	h.u.toast = ""
	h.openErr = errors.New("no editor")
	gfWrite(t, filepath.Join(h.srv, "notes.txt"), "back", 0o644)
	h.key(key.NameF4, 0)
	h.waitToast("Edit failed: no editor")
	h.u.toast = ""
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "") // Windows' home
	h.key(key.NameF4, 0)
	h.waitToast("Edit failed")
}

// touch gives p new content and a modification time d from now.
func gfTouch(t *testing.T, p, s string, d time.Duration) {
	t.Helper()
	os.Chmod(p, 0o644)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(d)
	os.Chtimes(p, at, at)
}

func TestFilesEditRemoteUploadsSaves(t *testing.T) {
	h := gfNew(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.fv.ctx = ctx
	defer cancel()
	h.selectName(0, "notes.txt")
	h.key(key.NameF4, 0)
	h.waitToast("Editing notes.txt")
	h.mu.Lock()
	local := h.opened[0]
	h.mu.Unlock()
	srvFile := filepath.Join(h.srv, "notes.txt")

	// a save uploads it
	gfTouch(t, local, "v2", 2*time.Second)
	h.waitToast("Saved notes.txt to web1")
	if b, _ := os.ReadFile(srvFile); string(b) != "v2" {
		t.Fatalf("server has %q", b)
	}

	// the server's copy changed meanwhile: the bar asks; N keeps it
	gfTouch(t, srvFile, "theirs", time.Hour)
	gfTouch(t, local, "v3", 4*time.Second)
	h.until("overwrite question", func() bool { return h.fv.prompt == "overwrite" })
	h.frame()
	h.key("N", 0)
	h.waitToast("Kept the server's notes.txt")
	if b, _ := os.ReadFile(srvFile); string(b) != "theirs" {
		t.Errorf("kept: server has %q", b)
	}

	// changed again: O overwrites
	gfTouch(t, srvFile, "theirs2", 2*time.Hour)
	gfTouch(t, local, "v4", 6*time.Second)
	h.until("overwrite question", func() bool { return h.fv.prompt == "overwrite" })
	h.key(key.NameF5, 0) // other keys are ignored meanwhile
	if h.fv.prompt != "overwrite" || h.fv.job != nil {
		t.Error("a key went past the question")
	}
	h.key("O", 0)
	h.until("overwritten", func() bool { b, _ := os.ReadFile(srvFile); return string(b) == "v4" })

	// an upload that fails says so
	h.waitToast("Saved notes.txt")
	if exeSuffix != "" {
		return // the failure is made with mode bits
	}
	os.Chmod(srvFile, 0o444) // read-only now: cat can't write it
	gfTouch(t, local, "v5", 8*time.Second)
	h.waitToast("Upload of notes.txt failed")
	if fileExists(srvFile) {
		t.Error("a failed upload left its half")
	}

	// gone from the server: uploaded again, no question
	gfTouch(t, local, "v6", 10*time.Second)
	h.until("re-uploaded", func() bool { b, _ := os.ReadFile(srvFile); return string(b) == "v6" })
	h.waitToast("Saved notes.txt")

	// the server's folder gone: no question, the upload fails
	h.u.toast = ""
	os.Rename(h.srv, h.srv+".away")
	gfTouch(t, local, "v7", 12*time.Second)
	h.until("second failure", func() bool { return strings.Contains(h.u.toast, "Upload of notes.txt failed") && h.fv.prompt == "" })
	os.Rename(h.srv+".away", h.srv)

	// a question pending when the split's context ends: kept
	gfTouch(t, srvFile, "theirs3", 3*time.Hour)
	gfTouch(t, local, "v8", 14*time.Second)
	h.until("overwrite question", func() bool { return h.fv.prompt == "overwrite" })
	cancel()
	h.waitToast("Kept the server's notes.txt")
}

// --- copying ---

func TestFilesCopyBothWays(t *testing.T) {
	h := gfNew(t)
	h.selectName(0, "notes.txt")
	h.key(key.NameF5, 0)
	h.waitToast("Copied 1 item to this machine")
	h.listed(1, h.loc, "notes.txt")
	if h.sel(1) != "notes.txt" {
		t.Errorf("copied file not selected: %q", h.sel(1))
	}
	if b, _ := os.ReadFile(filepath.Join(h.loc, "notes.txt")); string(b) != "hello" {
		t.Errorf("copy %q", b)
	}
	// up: a folder and a file, marked
	h.fv.mu.Lock()
	h.fv.active = 1
	h.fv.sides[1].marked["ldir"], h.fv.sides[1].marked["local.txt"] = true, true
	h.fv.mu.Unlock()
	h.key(key.NameF5, 0)
	h.waitToast("Copied 2 items to web1")
	h.listed(0, h.srv, "ldir", "local.txt")
	if len(trueKeys(h.fv.sides[1].marked)) != 0 {
		t.Errorf("copied items still marked: %v", h.fv.sides[1].marked)
	}
	// move down: gone from the server
	h.selectName(0, "run.sh")
	h.key(key.NameF6, 0)
	h.waitToast("Moved 1 item to this machine")
	h.listed(1, h.loc, "run.sh")
	h.until("source gone", func() bool { return h.index(0, "run.sh") < 0 && !h.fv.sides[0].loading })
	if fileExists(filepath.Join(h.srv, "run.sh")) {
		t.Error("move left the source")
	}
	// move up
	h.selectName(1, "run.sh")
	h.key(key.NameF6, 0)
	h.waitToast("Moved 1 item to web1")
	h.listed(0, h.srv, "run.sh")
	if fileExists(filepath.Join(h.loc, "run.sh")) {
		t.Error("move up left the source")
	}
}

func TestFilesCopyRefusals(t *testing.T) {
	h := gfNew(t)
	// both panels on the same folder of this machine
	h.u.loadSide(h.fv, h.fv.sides[1], h.srv, "")
	h.listed(1, h.srv, "notes.txt")
	h.selectName(0, "notes.txt")
	h.key(key.NameF5, 0)
	if !strings.Contains(h.toast(), "same folder") || h.fv.job != nil {
		t.Errorf("same folder: %q", h.toast())
	}
	// nothing chosen, or nowhere to go
	h.u.toast = ""
	h.u.transferFrom(h.fv, 0, h.srv, nil, h.loc, false, false)
	h.u.transferFrom(h.fv, 0, h.srv, []fileEntry{{Name: "notes.txt"}}, "", false, false)
	h.u.transferFrom(h.fv, 0, "", []fileEntry{{Name: "notes.txt"}}, h.loc, false, false)
	if h.fv.job != nil || h.toast() != "" {
		t.Error("an empty transfer ran")
	}
}

func TestFilesCopyFailsAndCancels(t *testing.T) {
	skipOnWindows(t, "the failures are made with mode bits")
	h := gfNew(t)
	ro := filepath.Join(h.loc, "ldir")
	os.Chmod(ro, 0o555)
	defer os.Chmod(ro, 0o755)
	h.u.transfer(h.fv, 0, []fileEntry{{Name: "notes.txt", Size: 5}}, ro, false, false)
	h.waitToast("Copied 0, failed 1: notes.txt")

	// the split's context already done: nothing copied
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.settle()
	h.fv.ctx = ctx
	h.u.transfer(h.fv, 0, []fileEntry{{Name: "notes.txt", Size: 5}}, h.loc, false, false)
	h.waitToast("Cancelled: 0 of 1 done")
	h.settle()
	// ... and a conflict question then ends with nothing to copy, quietly
	gfWrite(t, filepath.Join(h.loc, "notes.txt"), "mine", 0o644)
	h.u.toast = ""
	h.u.transfer(h.fv, 0, []fileEntry{{Name: "notes.txt", Size: 5}}, h.loc, false, false)
	h.settle()
	if h.toast() != "" {
		t.Errorf("toast %q", h.toast())
	}
	if b, _ := os.ReadFile(filepath.Join(h.loc, "notes.txt")); string(b) != "mine" {
		t.Error("copied over after a cancel")
	}
}

func TestFilesConflictsAndQueue(t *testing.T) {
	h := gfNew(t)
	gfWrite(t, filepath.Join(h.loc, "notes.txt"), "mine", 0o644)
	h.fv.mu.Lock()
	h.fv.sides[0].marked["notes.txt"], h.fv.sides[0].marked["run.sh"] = true, true
	h.fv.mu.Unlock()
	h.key(key.NameF5, 0)
	h.until("conflict", func() bool { return h.fv.prompt == "conflict" })
	if strings.Join(h.fv.conflicts, ",") != "notes.txt" || h.fv.conflictOf != 2 || h.fv.conflictTo != h.loc+" on this machine" {
		t.Errorf("conflicts %v of %d to %q", h.fv.conflicts, h.fv.conflictOf, h.fv.conflictTo)
	}
	h.frame() // "Waiting for an answer…"
	// another batch waits behind it
	h.selectName(0, "alpha")
	h.fv.mu.Lock()
	h.fv.sides[0].marked = map[string]bool{}
	h.fv.mu.Unlock()
	h.u.transfer(h.fv, 0, []fileEntry{{Name: "notes.txt", Size: 5}}, h.loc, false, false)
	if !strings.Contains(h.toast(), "Queued after the running copy (1 waiting)") || len(h.fv.queue) != 1 {
		t.Fatalf("queue: %q", h.toast())
	}
	h.key("S", 0) // skip existing: run.sh only
	h.until("run.sh copied", func() bool { return fileExists(filepath.Join(h.loc, "run.sh")) })
	if b, _ := os.ReadFile(filepath.Join(h.loc, "notes.txt")); string(b) != "mine" {
		t.Error("skip overwrote")
	}
	// the queued one starts and asks too: replace
	h.until("queued conflict", func() bool { return h.fv.prompt == "conflict" && len(h.fv.queue) == 0 })
	h.key("O", 0)
	h.until("replaced", func() bool { b, _ := os.ReadFile(filepath.Join(h.loc, "notes.txt")); return string(b) == "hello" })
	h.settle()
	// skipping everything says so
	h.selectName(0, "notes.txt")
	h.key(key.NameF5, 0)
	h.until("conflict", func() bool { return h.fv.prompt == "conflict" })
	h.key(key.NameEnter, 0) // enter replaces
	h.settle()
	h.key(key.NameF5, 0)
	h.until("conflict", func() bool { return h.fv.prompt == "conflict" })
	h.key("S", 0)
	h.waitToast("Nothing to copy")
	h.settle()
	h.key(key.NameF5, 0)
	h.until("conflict", func() bool { return h.fv.prompt == "conflict" })
	h.key(key.NameEscape, 0)
	if h.fv.prompt != "" || len(h.u.files) != 1 {
		t.Error("esc on the question closed the split")
	}
	h.settle()
	// the bar's buttons answer too
	for _, b := range []struct{ id, want string }{{"fkeyO", "replace"}, {"fkeyS", "skip"}, {"fkeyesc", ""}, {"fileCopy", "replace"}} {
		ch := make(chan string, 1)
		h.fv.prompt, h.fv.choice, h.fv.conflicts = "conflict", ch, []string{"a", "b", "c", "d", "e"}
		h.frame()
		h.click(b.id)
		if c := <-ch; c != b.want || h.fv.prompt != "" {
			t.Errorf("%s answered %q", b.id, c)
		}
	}
	for _, b := range []struct {
		id   string
		want string
	}{{"fkeyO", "replace"}, {"fkeyesc", ""}} {
		ch := make(chan string, 1)
		h.fv.prompt, h.fv.choice, h.fv.conflicts = "overwrite", ch, []string{"notes.txt"}
		h.frame()
		h.click(b.id)
		if c := <-ch; c != b.want || h.fv.prompt != "" {
			t.Errorf("overwrite %s answered %q", b.id, c)
		}
	}
	ch := make(chan string, 1)
	h.fv.prompt, h.fv.choice = "overwrite", ch
	h.key(key.NameEscape, 0)
	if <-ch != "" {
		t.Error("esc on overwrite")
	}
}

// TestFilesQuestionText: the delete banner says where and lists the items
// (folders with "/", files with sizes), the conflict one how many of the
// batch are there and where it goes.
func TestFilesQuestionText(t *testing.T) {
	s := &fileSide{remote: true, path: "/var/www", sel: 0, marked: map[string]bool{"a.js": true, "logs": true, "x": true},
		entries: []fileEntry{{Name: ".."}, {Name: "a.js", Size: 2048}, {Name: "logs", Dir: true}, {Name: "x", Size: -1}}}
	title, sub := deleteText(s, "web-01")
	if title != "Delete 3 items from /var/www on web-01?" || sub != "a.js (2.0 KB), logs/ and x. The server has no trash: this can't be undone." {
		t.Errorf("remote: %q / %q", title, sub)
	}
	s.remote, s.marked = false, map[string]bool{"a.js": true}
	if title, sub := deleteText(s, "web-01"); title != "Delete 1 item from /var/www on this machine?" || sub != "a.js (2.0 KB). It skips the trash: this can't be undone." {
		t.Errorf("local: %q / %q", title, sub)
	}
	fv := &filesView{conflicts: []string{"a", "b", "c", "d", "e"}, conflictOf: 12, conflictTo: "/srv on web-01"}
	if title, sub := fv.conflictText(); title != "5 of 12 items already there" || sub != "a, b, c and 2 more. Copying to /srv on web-01." {
		t.Errorf("conflict: %q / %q", title, sub)
	}
	fv = &filesView{conflicts: []string{"a"}}
	if title, sub := fv.conflictText(); title != "1 of 1 item already there" || sub != "a." {
		t.Errorf("conflict, no batch size: %q / %q", title, sub)
	}
	for _, c := range []struct {
		ns   []string
		want string
	}{{nil, ""}, {[]string{"a"}, "a"}, {[]string{"a", "b"}, "a and b"}, {[]string{"a", "b", "c"}, "a, b and c"}, {[]string{"a", "b", "c", "d"}, "a, b, c and 1 more"}} {
		if got := andList(c.ns, 3); got != c.want {
			t.Errorf("andList %v: %q", c.ns, got)
		}
	}
}

func TestFilesEscCancelsTheJob(t *testing.T) {
	h := gfNew(t)
	cancelled := 0
	h.fv.cancel = func() { cancelled++ }
	h.fv.queue = []func(){func() {}}
	h.key(key.NameEscape, 0)
	if cancelled != 1 || h.fv.queue != nil || len(h.u.files) != 1 {
		t.Errorf("esc: %d %v", cancelled, h.fv.queue)
	}
	// the progress row and its Cancel
	h.fv.queue = []func(){func() {}}
	j := &copyJob{verb: "Copying", label: "big.iso", total: 100, batch: 1000, count: 3, i: 1, done: 200, started: time.Now().Add(-3 * time.Second)}
	j.n.Store(50)
	h.fv.job = j
	h.frame()
	h.click("fkeyesc")
	if cancelled != 2 || h.fv.queue != nil {
		t.Errorf("cancel button: %d %v", cancelled, h.fv.queue)
	}
	h.fv.cancel = nil
	h.fv.queue = []func(){func() {}}
	h.click("fkeyesc")
	if h.fv.queue != nil {
		t.Error("cancel without a job kept the queue")
	}
	j.batch, j.count = -1, 1
	h.frame()
	j.total = -1
	h.frame()
	h.fv.job = nil
}

// --- delete, new folder, rename, chmod ---

func TestFilesDelete(t *testing.T) {
	h := gfNew(t)
	h.key(key.NameF8, 0) // on "..": nothing to delete
	if h.fv.prompt != "" {
		t.Error("asked to delete ..")
	}
	h.selectName(0, "notes.txt")
	h.key(key.NameDeleteForward, 0)
	if h.fv.prompt != "delete" {
		t.Fatal("no question")
	}
	h.frame()
	h.key("N", 0)
	if h.fv.prompt != "" || !fileExists(filepath.Join(h.srv, "notes.txt")) {
		t.Error("N")
	}
	h.key(key.NameF8, 0)
	h.key(key.NameF5, 0) // ignored while asking
	h.key("N", key.ModShift)
	if h.fv.prompt != "delete" {
		t.Error("shift+N answered the question")
	}
	h.key("Y", 0)
	h.until("deleted", func() bool { return h.index(0, "notes.txt") < 0 && !h.fv.sides[0].loading })
	if fileExists(filepath.Join(h.srv, "notes.txt")) {
		t.Error("still on the server")
	}
	// local, three marked, through the bar
	gfWrite(t, filepath.Join(h.loc, "b.txt"), "", 0o644)
	h.u.loadSide(h.fv, h.fv.sides[1], h.loc, "")
	h.listed(1, h.loc, "b.txt")
	h.fv.mu.Lock()
	h.fv.active = 1
	for _, n := range []string{"ldir", "local.txt", "b.txt"} {
		h.fv.sides[1].marked[n] = true
	}
	h.fv.mu.Unlock()
	h.click("fkeyF8")
	if h.fv.prompt != "delete" {
		t.Fatal("bar's Delete")
	}
	h.frame()
	h.click("fkeyN")
	if h.fv.prompt != "" {
		t.Error("bar's Cancel")
	}
	h.click("fkeyF8")
	h.frame()
	h.click("fkeyY")
	h.until("deleted locally", func() bool { return len(h.fv.sides[1].entries) == 1 && !h.fv.sides[1].loading })
	if fileExists(filepath.Join(h.loc, "local.txt")) || fileExists(filepath.Join(h.loc, "ldir")) {
		t.Error("local files left")
	}
	h.fv.mu.Lock()
	h.fv.sides[1].sel = 0
	h.fv.mu.Unlock()
	h.click("fkeyF8") // only "..": no question
	if h.fv.prompt != "" {
		t.Error("asked with nothing chosen")
	}
	if exeSuffix != "" {
		return // the failure below is made with mode bits
	}
	// a delete that fails
	ro := filepath.Join(h.loc, "ro")
	gfWrite(t, filepath.Join(ro, "f"), "", 0o644)
	os.Chmod(ro, 0o555)
	defer os.Chmod(ro, 0o755)
	h.u.loadSide(h.fv, h.fv.sides[1], ro, "f")
	h.listed(1, ro, "f")
	h.key(key.NameF8, 0)
	h.key(key.NameReturn, 0)
	h.waitToast("Delete failed")
}

// prompt opens the name editor with k, types name and submits it.
func (h *gfH) prompt(k key.Name, m key.Modifiers, name string) {
	h.t.Helper()
	h.key(k, m)
	if !h.fv.editing() {
		h.t.Fatalf("%s opened no prompt (%q)", k, h.fv.prompt)
	}
	h.u.nameIn.SetText(name)
	h.key(key.NameReturn, 0)
	if h.fv.prompt != "" {
		h.t.Fatalf("submit left prompt %q", h.fv.prompt)
	}
}

func TestFilesMkdir(t *testing.T) {
	h := gfNew(t)
	h.prompt(key.NameF7, 0, " made ")
	h.listed(0, h.srv, "made")
	if h.sel(0) != "made" || !fileExists(filepath.Join(h.srv, "made")) {
		t.Errorf("remote mkdir: %q", h.sel(0))
	}
	h.fv.active = 1
	h.prompt(key.NameF7, 0, "lmade")
	h.listed(1, h.loc, "lmade")
	for _, bad := range []string{"", ".", "..", "a/b"} {
		h.u.mkdir(h.fv, bad)
	}
	h.u.toast = ""
	h.u.mkdir(h.fv, "ldir")
	if !strings.Contains(h.toast(), "ldir already exists") {
		t.Errorf("existing: %q", h.toast())
	}
	if exeSuffix != "" {
		return // the failure below is made with mode bits
	}
	os.Chmod(h.loc, 0o555)
	defer os.Chmod(h.loc, 0o755)
	h.u.mkdir(h.fv, "nope")
	h.waitToast("New folder failed")
	// the bar's button, then esc
	h.click("fkeyF7")
	if h.fv.prompt != "mkdir" {
		t.Fatal("bar's New folder")
	}
	h.frame()
	h.fv.prompt, h.fv.focusNext = "", true
	h.frame()
	h.key(key.NameF7, 0)
	if !h.r.Source().Focused(&h.u.nameIn) {
		t.Fatal("F7 didn't focus the editor")
	}
	h.key(key.NameEscape, 0)
	if h.fv.prompt != "" || len(h.u.files) != 1 {
		t.Errorf("esc in the editor: %q %d", h.fv.prompt, len(h.u.files))
	}
}

func TestFilesRename(t *testing.T) {
	h := gfNew(t)
	h.key(key.NameF2, 0) // ".." can't be renamed
	if h.fv.prompt != "" {
		t.Error("rename ..")
	}
	h.selectName(0, "notes.txt")
	h.key(key.NameF2, 0)
	if h.u.nameIn.Text() != "notes.txt" {
		t.Errorf("editor %q", h.u.nameIn.Text())
	}
	if s, e := h.u.nameIn.Selection(); s != 5 || e != 0 {
		t.Errorf("selection %d-%d, want the stem", s, e)
	}
	h.u.nameIn.SetText("memo.txt")
	h.key(key.NameReturn, 0)
	h.listed(0, h.srv, "memo.txt")
	if h.sel(0) != "memo.txt" || fileExists(filepath.Join(h.srv, "notes.txt")) {
		t.Errorf("remote rename: %q", h.sel(0))
	}
	// F2 here locally (Shift+F6 no more); a dotfile's whole name is selected
	gfWrite(t, filepath.Join(h.loc, ".bashrc"), "", 0o644)
	h.key("H", key.ModShortcut)
	h.u.loadSide(h.fv, h.fv.sides[1], h.loc, ".bashrc")
	h.listed(1, h.loc, ".bashrc")
	h.fv.active = 1
	h.key(key.NameF6, key.ModShift)
	if h.fv.prompt != "" || h.fv.job != nil || len(h.fv.queue) != 0 {
		t.Errorf("shift+F6: prompt %q", h.fv.prompt)
	}
	h.key(key.NameF2, 0)
	if s, e := h.u.nameIn.Selection(); h.fv.prompt != "rename" || s != 7 || e != 0 {
		t.Errorf("dotfile selection %d-%d", s, e)
	}
	h.u.nameIn.SetText(".zshrc")
	h.key(key.NameReturn, 0)
	h.listed(1, h.loc, ".zshrc")
	// the same name, an existing one, nothing selected: nothing happens
	h.selectName(1, ".zshrc")
	h.u.rename(h.fv, ".zshrc")
	h.u.rename(h.fv, "ldir")
	if !strings.Contains(h.toast(), "already exists") {
		t.Errorf("existing: %q", h.toast())
	}
	h.fv.sides[1].sel = 99
	h.u.rename(h.fv, "x")
	if h.fv.sides[1].loading {
		t.Error("renamed nothing")
	}
	// gone meanwhile: fails
	h.selectName(1, "local.txt")
	os.Remove(filepath.Join(h.loc, "local.txt"))
	h.u.rename(h.fv, "other.txt")
	h.waitToast("Rename failed")
	// the bar's button
	h.selectName(0, "alpha")
	h.click("fkeyF2")
	if h.fv.prompt != "rename" || h.u.nameIn.Text() != "alpha" {
		t.Errorf("bar's Rename: %q", h.fv.prompt)
	}
	h.frame()
	if !h.r.Source().Focused(&h.u.nameIn) {
		t.Error("bar's Rename: the editor isn't focused")
	}
}

func TestFilesChmod(t *testing.T) {
	skipOnWindows(t, "Windows files have no mode bits to show or set")
	h := gfNew(t)
	h.key(key.NameF9, 0) // ".." only
	if h.fv.prompt != "" {
		t.Error("chmod ..")
	}
	h.selectName(0, "notes.txt")
	h.key(key.NameF9, 0)
	if h.fv.prompt != "chmod" || h.u.nameIn.Text() != "644" {
		t.Fatalf("prompt %q %q", h.fv.prompt, h.u.nameIn.Text())
	}
	h.frame()
	h.u.nameIn.SetText("600")
	h.key(key.NameReturn, 0)
	h.waitToast("Permissions set to 600 on 1 item")
	if fi, _ := os.Stat(filepath.Join(h.srv, "notes.txt")); fi.Mode().Perm() != 0o600 {
		t.Errorf("remote mode %o", fi.Mode().Perm())
	}
	h.settle()
	// locally: octal, then symbolic
	h.selectName(1, "local.txt")
	h.prompt(key.NameF9, 0, "640")
	h.waitToast("Permissions set to 640")
	h.settle()
	h.prompt(key.NameF9, 0, "u+x")
	h.waitToast("Permissions set to u+x")
	h.settle()
	if fi, _ := os.Stat(filepath.Join(h.loc, "local.txt")); fi.Mode().Perm() != 0o740 {
		t.Errorf("local mode %o", fi.Mode().Perm())
	}
	h.u.chmod(h.fv, "rwx")
	if !strings.Contains(h.toast(), "Not a mode") {
		t.Errorf("bad mode: %q", h.toast())
	}
	// failures: octal and symbolic on a file gone meanwhile
	os.Remove(filepath.Join(h.loc, "local.txt"))
	h.u.chmod(h.fv, "600")
	h.waitToast("Chmod failed")
	h.settle()
	h.u.toast = ""
	h.fv.sides[1].sel = 99 // nothing selected, but local.txt marked (still listed, though gone)
	h.fv.sides[1].entries = append(h.fv.sides[1].entries, fileEntry{Name: "local.txt"})
	h.fv.sides[1].marked["local.txt"] = true
	h.u.chmod(h.fv, "g-w")
	h.waitToast("Chmod failed")
	h.settle()
	// nothing chosen: reported on no items
	h.fv.sides[1].sel, h.fv.sides[1].marked = 0, map[string]bool{}
	h.u.chmod(h.fv, "644")
	h.waitToast("on 0 items")
	h.settle()
	// the bar's button
	h.selectName(0, "run.sh")
	h.click("fkeyF9")
	if h.fv.prompt != "chmod" || h.u.nameIn.Text() != "755" {
		t.Errorf("bar's Chmod: %q %q", h.fv.prompt, h.u.nameIn.Text())
	}
}

// --- the bar, the header, the path ---

func TestFilesBarActions(t *testing.T) {
	h := gfNew(t)
	h.selectName(1, "local.txt")
	h.click("fkeyF3")
	if len(h.opened) != 1 {
		t.Error("F3 button")
	}
	h.click("fkeyF4")
	if len(h.opened) != 2 {
		t.Error("F4 button")
	}
	h.fv.sides[1].marked["local.txt"] = true
	h.frame() // the marked summary
	h.click("fkeyF5")
	h.waitToast("Copied 1 item to web1")
	h.listed(0, h.srv, "local.txt")
	h.settle()
	os.Remove(filepath.Join(h.srv, "local.txt"))
	h.selectName(1, "local.txt")
	h.click("fkeyF6")
	h.waitToast("Moved 1 item to web1")
	h.settle()
	if fileExists(filepath.Join(h.loc, "local.txt")) {
		t.Error("F6 button didn't move")
	}
}

func TestFilesSortHeader(t *testing.T) {
	h := gfNew(t)
	gfWrite(t, filepath.Join(h.srv, "big.bin"), strings.Repeat("x", 100), 0o644)
	old := time.Now().Add(-400 * 24 * time.Hour)
	os.Chtimes(filepath.Join(h.srv, "big.bin"), old, old)
	h.u.loadSide(h.fv, h.fv.sides[0], h.srv, "")
	h.listed(0, h.srv, "big.bin")
	h.selectName(0, "notes.txt")
	files := func() string {
		var out []string
		for _, n := range h.names(0)[3:] { // after .., alpha, beta
			out = append(out, n)
		}
		return strings.Join(out, ",")
	}
	h.click("fsort0-1") // size: biggest first
	if s := h.fv.sides[0]; s.sortBy != 1 || !s.desc || files() != "big.bin,run.sh,notes.txt" || h.sel(0) != "notes.txt" {
		t.Errorf("size: %v %v %s", s.sortBy, s.desc, files())
	}
	h.click("fsort0-1") // again: reversed
	if files() != "notes.txt,run.sh,big.bin" {
		t.Errorf("size asc: %s", files())
	}
	h.click("fsort0-2") // modified: newest first
	if s := h.fv.sides[0]; s.sortBy != 2 || !s.desc || !strings.HasSuffix(files(), "big.bin") {
		t.Errorf("date: %s", files())
	}
	h.click("fsort0-0")
	if s := h.fv.sides[0]; s.sortBy != 0 || s.desc || files() != "big.bin,notes.txt,run.sh" {
		t.Errorf("name: %s", files())
	}
	h.fv.sides[0].sel = 99
	h.click("fsort0-0")
	if !h.fv.sides[0].desc {
		t.Error("name reversed")
	}
	// narrow: no Modified column
	h.size = image.Pt(800, 600)
	h.frame()
}

func TestFilesCrumbs(t *testing.T) {
	h := gfNew(t)
	h.frame()
	// this machine's path starts at ~: ~ › loc
	h.click("crumb1-0")
	h.listed(1, h.home, "loc")
	if h.sel(1) != "loc" {
		t.Errorf("~ crumb selected %q", h.sel(1))
	}
	// the server's: / then each folder; deep paths start with …
	deep := filepath.Join(h.srv, "alpha", "a", "b", "c")
	os.MkdirAll(deep, 0o755)
	h.u.loadSide(h.fv, h.fv.sides[0], deep, "")
	h.listed(0, deep)
	h.frame()
	h.click("crumb0-3") // …, alpha, a, b, c: b
	h.listed(0, filepath.Join(h.srv, "alpha", "a", "b"), "c")
	if h.sel(0) != "c" {
		t.Errorf("crumb selected %q", h.sel(0))
	}
	if exeSuffix != "" {
		return // below: a POSIX root's crumbs
	}
	first := strings.Split(strings.Trim(h.srv, "/"), "/")[0]
	h.u.loadSide(h.fv, h.fv.sides[0], "/"+first, "") // shallow, so its first crumb is / (macOS's $TMPDIR is deep)
	h.listed(0, "/"+first)
	h.frame()
	h.click("crumb0-0")
	h.listed(0, "/")
	if h.sel(0) != first {
		t.Errorf("/ crumb selected %q", h.sel(0))
	}
}

// --- pointer: rows, drag and drop ---

func TestFilesRowClicks(t *testing.T) {
	h := gfNew(t)
	i := h.index(0, "beta")
	h.clickRow(0, i, 0)
	if h.sel(0) != "beta" || h.fv.active != 0 {
		t.Fatalf("click: %q", h.sel(0))
	}
	h.clickRow(0, h.index(0, "notes.txt"), key.ModShortcut)
	if m := trueKeys(h.fv.sides[0].marked); strings.Join(m, ",") != "notes.txt" || h.sel(0) != "notes.txt" {
		t.Errorf("ctrl+click: %v", m)
	}
	h.clickRow(0, 0, key.ModShortcut) // ".." isn't marked: a plain click
	if len(trueKeys(h.fv.sides[0].marked)) != 1 || h.sel(0) != ".." {
		t.Errorf("ctrl+click ..: %v", h.fv.sides[0].marked)
	}
	h.clickRow(0, h.index(0, "beta"), key.ModShift) // from ".." to beta
	if m := trueKeys(h.fv.sides[0].marked); strings.Join(m, ",") != "alpha,beta,notes.txt" || h.sel(0) != "beta" {
		t.Errorf("shift+click: %v", m)
	}
	// the other side takes the keyboard
	h.clickRow(1, h.index(1, "local.txt"), 0)
	if h.fv.active != 1 || h.sel(1) != "local.txt" {
		t.Errorf("other side: %d %q", h.fv.active, h.sel(1))
	}
	h.frame()
	// a double click opens
	rs := h.fv.sides[0].rows["alpha"]
	rs.click.Click()
	rs.click.Click()
	h.frame()
	h.listed(0, filepath.Join(h.srv, "alpha"))
	// a press on the panels' empty space focuses them
	h.fv.focusNext = false
	h.send(h.ptr(pointer.Press, f32.Pt(300, 400), pointer.ButtonPrimary, 0), h.ptr(pointer.Release, f32.Pt(300, 400), 0, 0))
	h.fv.prompt = "mkdir"
	h.send(h.ptr(pointer.Press, f32.Pt(300, 400), pointer.ButtonPrimary, 0), h.ptr(pointer.Release, f32.Pt(300, 400), 0, 0))
	h.fv.prompt = ""
}

// drag presses at from, moves past the slop, to to, and releases.
func (h *gfH) drag(from, to f32.Point) {
	h.send(h.ptr(pointer.Press, from, pointer.ButtonPrimary, 0))
	h.send(h.ptr(pointer.Move, from.Add(f32.Pt(30, 0)), pointer.ButtonPrimary, 0))
	h.send(h.ptr(pointer.Move, to.Sub(f32.Pt(5, 0)), pointer.ButtonPrimary, 0))
	h.send(h.ptr(pointer.Move, to, pointer.ButtonPrimary, 0))
	h.send(h.ptr(pointer.Release, to, 0, 0))
	h.frame()
}

func TestFilesDragAndDrop(t *testing.T) {
	h := gfNew(t)
	h.calibrate()
	// a file onto this machine's panel: copied into its folder
	h.drag(h.rowPt(0, h.index(0, "notes.txt")), f32.Pt(float32(h.size.X/2+200), 400))
	h.waitToast("Copied 1 item to this machine")
	h.listed(1, h.loc, "notes.txt")
	h.settle()
	// marked rows onto a folder row
	h.fv.mu.Lock()
	h.fv.sides[0].marked["run.sh"], h.fv.sides[0].marked["alpha"] = true, true
	h.fv.mu.Unlock()
	h.drag(h.rowPt(0, h.index(0, "run.sh")), h.rowPt(1, h.index(1, "ldir")))
	h.waitToast("Copied 2 items to this machine")
	h.settle()
	if !fileExists(filepath.Join(h.loc, "ldir", "run.sh")) || !fileExists(filepath.Join(h.loc, "ldir", "alpha")) {
		t.Error("not copied into the folder")
	}
	// a local file onto the server's "..": its parent
	h.selectName(1, "local.txt")
	h.drag(h.rowPt(1, h.index(1, "local.txt")), h.rowPt(0, 0))
	h.waitToast("Copied 1 item to web1")
	h.settle()
	if !fileExists(filepath.Join(filepath.Dir(h.srv), "local.txt")) {
		t.Error("not copied into the parent")
	}
	os.Remove(filepath.Join(filepath.Dir(h.srv), "local.txt"))
	// onto its own side, or nowhere: nothing
	h.u.toast = ""
	h.drag(h.rowPt(1, h.index(1, "local.txt")), f32.Pt(float32(h.size.X/2+200), 400))
	h.drag(h.rowPt(1, h.index(1, "local.txt")), f32.Pt(300, float32(h.size.Y-5)))
	h.settle()
	if h.toast() != "" || h.fv.job != nil {
		t.Errorf("a drop on its own side copied: %q", h.toast())
	}
	// ".." doesn't drag
	h.drag(h.rowPt(1, 0), h.rowPt(0, 1))
	h.settle()
	if h.toast() != "" {
		t.Errorf(".. dragged: %q", h.toast())
	}
	// drop with nothing dragged, or nowhere to go
	h.fv.drag = nil
	h.u.drop(h.fv, 0, h.srv)
	h.fv.drag = &dragInfo{from: 1}
	h.u.drop(h.fv, 0, "")
	if len(h.fv.after) != 0 {
		t.Error("a drop with nothing to copy")
	}
}

// --- drawing ---

func TestFilesPanelStates(t *testing.T) {
	h := gfNew(t)
	empty := filepath.Join(h.loc, "ldir")
	h.u.loadSide(h.fv, h.fv.sides[1], empty, "")
	h.listed(1, empty)
	h.frame() // "Empty folder"
	gfWrite(t, filepath.Join(empty, ".only"), "", 0o644)
	os.Symlink(filepath.Join(h.srv, "notes.txt"), filepath.Join(empty, ".link"))
	h.u.loadSide(h.fv, h.fv.sides[1], empty, "")
	h.until("hidden listed", func() bool { return len(h.fv.sides[1].all) == 2 && !h.fv.sides[1].loading })
	h.frame() // "Only hidden files"
	h.key("H", key.ModShortcut)
	h.frame() // dimmed dotfiles, a link arrow
	h.fv.mu.Lock()
	h.fv.sides[1].loading, h.fv.sides[1].entries = true, nil
	h.fv.mu.Unlock()
	h.frame() // "Loading…"
	h.fv.sides[1].loading = false
	h.u.loadSide(h.fv, h.fv.sides[1], h.loc, "")
	h.listed(1, h.loc, "local.txt")
	h.settle()

	// a long folder keeps the selection in view
	long := filepath.Join(h.srv, "beta")
	for i := range 80 {
		gfWrite(t, filepath.Join(long, fmt.Sprintf("f%02d.go", i)), "", 0o755)
	}
	h.u.loadSide(h.fv, h.fv.sides[0], long, "")
	h.listed(0, long, "f79.go")
	h.fv.active = 0
	h.frame()
	h.key(key.NameEnd, 0)
	h.frame()
	if h.fv.sides[0].list.Position.First == 0 {
		t.Error("end didn't scroll")
	}
	h.key(key.NameHome, 0)
	h.frame()
	if h.fv.sides[0].list.Position.First != 0 {
		t.Errorf("home didn't scroll back: %d", h.fv.sides[0].list.Position.First)
	}
	// a drag over the other side lights it up
	h.fv.drag, h.fv.dragging = &dragInfo{from: 0}, true
	h.fv.sides[1].dropHover = true
	if rs := h.fv.sides[1].rows["ldir"]; rs != nil {
		rs.hover = true
	}
	h.frame()
	h.fv.drag, h.fv.dragging = nil, false
}

func TestFileKindBadges(t *testing.T) {
	for _, c := range []struct {
		name string
		mode os.FileMode
		tag  string
		col  any
	}{
		{"Dockerfile", 0, "DKR", kindConfig},
		{"Makefile", 0, "MK", kindCode},
		{".bashrc", 0, "SH", kindScript},
		{"id_ed25519", 0, "KEY", kindKey},
		{"main.go", 0, "GO", kindCode},
		{"deploy.yaml", 0, "YML", kindConfig},
		{"app.service", 0, "UNIT", kindConfig},
		{"backup.tar.gz", 0, "TGZ", rgb(0xffd60a)},
		{"site.properties", 0, "PROP", kindConfig},
		{"notes.markdown", 0, "MD", rgb(0xd1d1d6)},
		{"data.parquet", 0, "PARQ", rgb(0x64d2ff)},
		{"tool", 0o755, "EXE", kindScript},
		{"README", 0o644, "", cText3},
		{"x.weird", 0, "WEIR", cText3},
		{"x.ñañañ", 0, "ÑAÑA", cText3}, // cut by character, not byte
	} {
		tag, col := fileKind(fileEntry{Name: c.name, Mode: c.mode})
		if tag != c.tag || col != c.col {
			t.Errorf("%s: %q %v, want %q %v", c.name, tag, col, c.tag, c.col)
		}
	}
	if withAlpha(cAcc, 7).A != 7 {
		t.Error("withAlpha")
	}
}

func TestFileDate(t *testing.T) {
	if fileDate(time.Time{}) != "" {
		t.Error("zero")
	}
	now := time.Now()
	if got := fileDate(now); got != now.Format("Jan _2 15:04") {
		t.Errorf("this year %q", got)
	}
	old := time.Date(2001, 3, 4, 5, 6, 0, 0, time.Local)
	if got := fileDate(old); got != "Mar  4  2001" {
		t.Errorf("old %q", got)
	}
}
