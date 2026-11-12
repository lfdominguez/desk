package infra

import (
	"bytes"
	"context"
	"fmt"
	"github.com/lfdominguez/desk/internal/testwait"
	"image"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"golang.org/x/image/math/fixed"

	"github.com/lfdominguez/desk/internal/keys"
	"github.com/lfdominguez/desk/internal/sshc"
)

// --- helpers (prefix gm: this file's) ---

// gmH drives the window's ui headlessly: a driver whose actions queue up
// for the test to run against a model of its own, and a Router for events.
type gmH struct {
	t    *testing.T
	u    *ui
	d    *driver
	r    *input.Router
	ops  op.Ops
	size image.Point
}

func gmEnv(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows' home
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("DESK_ROOT", filepath.Join(home, "Work"))
	runtimeA(t)
	return home
}

func newGM(t *testing.T) *gmH {
	gmEnv(t)
	d := &driver{in: make(chan any, 4096)}
	d.state.Store(&guiState{})
	u := newUI(d, guiLayout{}, func() {})
	d.redraw, d.clip = u.redraw, u.setClipboard
	return &gmH{t: t, u: u, d: d, r: new(input.Router), size: image.Pt(1200, 800)}
}

// set publishes st as the driver's snapshot (frame reads it) and the ui's.
func (h *gmH) set(st *guiState) *guiState {
	h.d.state.Store(st)
	h.u.st = st
	return st
}

func (h *gmH) gtx() layout.Context {
	h.ops.Reset()
	return layout.Context{Ops: &h.ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Now: time.Now(),
		Source: h.r.Source(), Constraints: layout.Exact(h.size)}
}

func (h *gmH) draw(w func(gtx layout.Context)) {
	gtx := h.gtx()
	w(gtx)
	h.r.Frame(&h.ops)
}

func (h *gmH) frame(org string) { h.draw(func(gtx layout.Context) { h.u.frame(gtx, org) }) }

// acts drains the actions sent to the driver.
func (h *gmH) acts() []func(*model) {
	var out []func(*model)
	for {
		select {
		case x := <-h.d.in:
			if f, ok := x.(func(*model)); ok {
				out = append(out, f)
			}
		default:
			return out
		}
	}
}

// runActs drains the actions and runs them on m; it returns how many ran.
func (h *gmH) runActs(m *model) int {
	fs := h.acts()
	for _, f := range fs {
		f(m)
	}
	return len(fs)
}

func (h *gmH) press(x, y float32, mods key.Modifiers) {
	h.r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: f32.Pt(x, y), Modifiers: mods})
}

func (h *gmH) release(x, y float32) {
	h.r.Queue(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: f32.Pt(x, y)})
}

// drag moves the pointer with the button held (Gio makes it a Drag).
func (h *gmH) drag(x, y float32) {
	h.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: f32.Pt(x, y)})
}

func (h *gmH) move(x, y float32) {
	h.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(x, y)})
}

func (h *gmH) key(name key.Name, mods key.Modifiers) {
	h.r.Queue(key.Event{Name: name, Modifiers: mods, State: key.Press})
}

// hover moves the pointer over the region until c reports hovered, drawing
// with w after each move; it fails when nothing there hovers c.
func (h *gmH) hover(c *widget.Clickable, area image.Rectangle, step int, w func(gtx layout.Context)) image.Point {
	h.t.Helper()
	for y := area.Min.Y; y < area.Max.Y; y += step {
		for x := area.Min.X; x < area.Max.X; x += step {
			h.move(float32(x), float32(y))
			h.draw(w)
			if c.Hovered() {
				h.draw(w)
				return image.Pt(x, y)
			}
		}
	}
	h.t.Fatalf("nothing in %v hovers the clickable", area)
	return image.Point{}
}

// gmSink collects what a fake terminal's emulator sends to its program.
type gmSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *gmSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *gmSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *gmSink) reset() {
	s.mu.Lock()
	s.buf.Reset()
	s.mu.Unlock()
}

// wait polls until what was sent contains want.
func (s *gmSink) wait(t *testing.T, want string) {
	t.Helper()
	for end := time.Now().Add(testwait.Long); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if strings.Contains(s.String(), want) {
			return
		}
	}
	t.Fatalf("sent %q, want %q in it", s.String(), want)
}

// gmTerm is a Term with no program: OSC 133 marks make blocks, keys and
// writes to the pty land in the sink.
func gmTerm(t *testing.T, host string, w, h int) (*Term, *gmSink) {
	r, wp, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	term := &Term{Host: host, emu: vt.NewEmulator(w, h), pty: pipeProc{wp}}
	term.emu.RegisterOscHandler(133, term.osc133)
	sink := &gmSink{}
	go io.Copy(sink, term.emu)
	go io.Copy(sink, r)
	// ponytail: the emulator's reader stays blocked for the run (Emulator.Close races its Read)
	t.Cleanup(func() { wp.Close(); r.Close() })
	return term, sink
}

// gmOut is the program's output reaching the terminal.
func gmOut(term *Term, s string) {
	term.mu.Lock()
	term.emu.WriteString(s)
	term.ver.Add(1) // as the reader does
	term.mu.Unlock()
}

// gmBlock is one finished command block in the shell integration's marks.
func gmBlock(cmd, out string, exit string) string {
	return "$ \x1b]133;B\x07" + cmd + "\r\n\x1b]133;C\x07" + out + "\x1b]133;D;" + exit + "\x07"
}

// gmFiles gives tab k an empty directory split without listing anything.
func gmFiles(u *ui, k *Tab) *filesView {
	fv := &filesView{host: "web1", name: "web1", ctx: context.Background(), tab: k}
	for i := range fv.sides {
		fv.sides[i] = &fileSide{remote: i == 0, marked: map[string]bool{}, rows: map[string]*fileRow{}}
		fv.sides[i].list.Axis = layout.Vertical
	}
	u.files[k] = fv
	return fv
}

// gmClip is the next text queued for the clipboard, "<none>" if nothing is.
func gmClip(u *ui) string {
	select {
	case s := <-u.clipOut:
		return s
	default:
		return "<none>"
	}
}

func gmProbe(t *testing.T) (*model, *Tab) {
	tb := &Tab{org: "Acme", name: "web1", busy: true, pinned: map[int]bool{}}
	return &model{tabs: []*Tab{tb}}, tb
}

// --- gui.go ---

func TestGMKeyOf(t *testing.T) {
	for s, want := range map[string]tea.KeyPressMsg{
		"enter":  {Code: tea.KeyEnter},
		"esc":    {Code: tea.KeyEscape},
		"ctrl+t": {Code: 't', Mod: tea.ModCtrl},
		"alt+p":  {Code: 'p', Mod: tea.ModAlt},
		"y":      {Code: 'y', Text: "y"},
	} {
		if got := keyOf(s); got != want {
			t.Errorf("keyOf(%q) = %+v, want %+v", s, got, want)
		}
	}
}

func TestGMTabState(t *testing.T) {
	cases := []struct {
		t    *Tab
		want string
	}{
		{&Tab{pending: []*approval{{}}, busy: true}, "approval"},
		{&Tab{busy: true}, "working"},
		{&Tab{chat: []*chatEntry{{who: "claude"}}}, "your turn"},
		{&Tab{chat: []*chatEntry{{who: "you"}}}, "idle"},
		{&Tab{}, "idle"},
	}
	for i, c := range cases {
		if got := tabState(c.t); got != c.want {
			t.Errorf("case %d: %q, want %q", i, got, c.want)
		}
	}
}

func TestGMDeliverAndEmit(t *testing.T) {
	d := &driver{in: make(chan any, 16)}
	var clip string
	quit, redraws := 0, 0
	d.clip, d.quit, d.redraw = func(s string) { clip = s }, func() { quit++ }, func() { redraws++ }
	d.deliver(nil)
	d.deliver(tea.QuitMsg{})
	if quit != 1 || len(d.in) != 0 {
		t.Errorf("quit %d, queued %d", quit, len(d.in))
	}
	d.deliver(tea.SetClipboard("secret-ish")())
	if clip != "secret-ish" || len(d.in) != 0 {
		t.Errorf("clipboard %q, queued %d", clip, len(d.in))
	}
	d.deliver(redrawMsg{})
	if x := <-d.in; x != (redrawMsg{}) {
		t.Errorf("queued %v", x)
	}
	d.deliver(tea.BatchMsg{func() tea.Msg { return redrawMsg{} }, nil})
	select {
	case x := <-d.in:
		if x != (redrawMsg{}) {
			t.Errorf("batch gave %v", x)
		}
	case <-time.After(testwait.Long):
		t.Fatal("the batch's command never delivered")
	}
	d.run(nil) // nothing to run

	m := &model{flash: "one"}
	d.in <- redrawMsg{}
	d.last = time.Now()
	d.emit(m) // a burst: wait
	if redraws != 0 || d.state.Load() != nil {
		t.Errorf("emitted during a burst: %d", redraws)
	}
	<-d.in
	d.emit(m)
	if redraws != 1 || d.state.Load().Flash != "one" {
		t.Errorf("redraws %d, state %+v", redraws, d.state.Load())
	}
}

func TestGMLoop(t *testing.T) {
	gmEnv(t)
	d := &driver{in: make(chan any, 64)}
	redraws := make(chan struct{}, 64)
	d.redraw = func() { redraws <- struct{}{} }
	d.quit = func() {}
	go d.loop(model{})
	d.act(func(m *model) { m.flash = "from act" })
	d.in <- tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl} // a message: Update runs it (no tab: nothing)
	d.act(func(m *model) { m.errText = "after msg" })
	for end := time.Now().Add(testwait.Long); ; {
		if s := d.state.Load(); s != nil && s.Err == "after msg" {
			if s.Flash != "" {
				t.Errorf("the key cleared no flash: %q", s.Flash)
			}
			break
		}
		if time.Now().After(end) {
			t.Fatal("the loop never published")
		}
		time.Sleep(5 * time.Millisecond)
	}
	d.act(func(*model) { runtime.Goexit() })
}

func TestGMStart(t *testing.T) {
	gmEnv(t)
	oldD, oldR, oldG := dispatch, repaint, guiReplies
	t.Cleanup(func() { dispatch, repaint, guiReplies = oldD, oldR, oldG })
	m := model{tree: []node{{depth: 0, label: "Acme", org: "Acme"}}}
	u, sock := start(m, func() {})
	defer os.Remove(sock)
	defer u.d.act(func(*model) { runtime.Goexit() })
	if !guiReplies || dispatch == nil || repaint == nil {
		t.Error("start leaves the globals unset")
	}
	if st := u.d.state.Load(); len(st.Tree) != 1 || st.Tree[0].Label != "Acme" {
		t.Errorf("first snapshot: %+v", st)
	}
	dispatch(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}) // reaches the loop
	u.d.quit()                                             // ctrl+q is the TUI's: nothing
	u.d.clip("x")
	if s := gmClip(u); s != "x" {
		t.Errorf("clip %q", s)
	}
}

func TestGMSnapshot(t *testing.T) {
	gmEnv(t)
	term, _ := gmTerm(t, "web1", 40, 6)
	gmOut(term, gmBlock("ls", "a\r\n", "0"))
	other, _ := gmTerm(t, "db1", 40, 6)
	tb := &Tab{org: "Acme", group: "web", dir: "/d", account: "work", name: "web1", suggest: "uptime", orgOnly: false, busy: true,
		pinScreen: true, broadcast: true, model: "opus", compacting: true, ctxTokens: 10, ctxWindow: 100, term: term,
		pinned: map[int]bool{1: true}, chat: []*chatEntry{{who: "you", text: "hi", ctx: []int{1}, screen: true, steps: []step{{"ls", "lists"}}}},
		panes:   []*Pane{{id: 1, term: term, name: "web1", addr: "10.0.0.1", dir: "/d", password: &SecretRef{}}, {id: 2, term: other, name: "db1", claude: true}},
		pending: []*approval{{cmd: "rm x", reason: "r", why: "w", keys: true, secret: true, blocked: true, pane: 2, explain: []step{{"rm", "removes"}}}}}
	tb2 := &Tab{org: "Globex", name: "g", pinned: map[int]bool{}}
	m := &model{group: "", flash: "f", errText: "e", tabs: []*Tab{tb, tb2},
		tree: []node{{depth: 2, label: "web1", org: "Acme", group: "web", host: "web1", addr: "10.0.0.1", dir: "/d", desc: "front", tags: "nginx", pin: true}},
		asks: []*question{{title: "Vault passphrase", masked: true}}}
	d := &driver{palOpen: true}
	st := d.snapshot(m)
	if camTerm.Load() != term {
		t.Error("the camera follows the active tab's focused pane")
	}
	if len(st.Tree) != 1 || st.Tree[0].Note != "front nginx" || !st.Tree[0].Pin || st.Tree[0].Addr != "10.0.0.1" {
		t.Errorf("tree %+v", st.Tree)
	}
	g := st.Tabs[0]
	if g.State != "approval" || !g.Busy || !g.Broadcast || !g.PinScreen || g.Model != "opus" || !g.Compacting || g.Ctx != 10 || g.CtxWindow != 100 || g.Suggest != "uptime" {
		t.Errorf("tab %+v", g)
	}
	if len(g.Panes) != 2 || !g.Panes[0].Focused || g.Panes[1].Focused || !g.Panes[0].Password || g.Panes[1].Password || !g.Panes[1].Claude {
		t.Errorf("panes %+v", g.Panes)
	}
	if b, ok := g.Panes[0].Blocks[1]; !ok || b.Output != "" || b.Cmd != "ls" {
		t.Errorf("blocks %+v", g.Panes[0].Blocks)
	}
	if len(g.Chat) != 1 || g.Chat[0].Text != "hi" || !g.Chat[0].Screen || g.Chat[0].Ctx[0] != 1 || !g.Pinned[1] {
		t.Errorf("chat %+v pinned %v", g.Chat, g.Pinned)
	}
	if st.Tabs[1].Live {
		t.Error("a tab without a terminal isn't live")
	}
	if a := st.Approval; a == nil || a.Host != "db1" || a.Cmd != "rm x" || !a.Keys || !a.Secret || !a.Blocked || a.Org != "Acme" || a.Tab != "web1" {
		t.Errorf("approval %+v", st.Approval)
	}
	if st.Ask != "Vault passphrase" || !st.AskMasked || !st.PalOpen || len(st.Pal) == 0 || st.Flash != "f" || st.Err != "e" {
		t.Errorf("ask %q %v pal %v %d", st.Ask, st.AskMasked, st.PalOpen, len(st.Pal))
	}
	tb.pending[0].pane = 9 // a pane that is gone: no host
	if st := (&driver{}).snapshot(m); st.Approval.Host != "" || st.PalOpen || st.Pal != nil {
		t.Errorf("approval %+v pal %v", st.Approval, st.PalOpen)
	}
}

// TestGMSnapshotConn: a failed pane carries its last line (its connection's
// error when it has one) and whether Fix can help, a dropped one when it
// dropped and its redials, a waiting one since when it waits.
func TestGMSnapshotConn(t *testing.T) {
	gmEnv(t)
	failed, _ := gmTerm(t, "web1", 100, 6)
	gmOut(failed, "ssh: connect to host 10.0.0.9 port 22: Connection timed out\r\n")
	failed.exited = true
	lost, _ := gmTerm(t, "db1", 100, 6)
	waiting, _ := gmTerm(t, "db2", 100, 6)
	at := time.Now().Add(-time.Minute)
	tb := &Tab{org: "Acme", name: "web1", pinned: map[int]bool{}, panes: []*Pane{
		{id: 1, term: failed, name: "web1", tries: 3},
		{id: 2, term: lost, name: "db1", lost: at, retry: at.Add(time.Hour), tries: 2, waitUntil: time.Now().Add(time.Hour)},
		{id: 3, term: waiting, name: "db2", waitUntil: at},
	}}
	snap := func() []guiPane { return (&driver{}).snapshot(&model{tabs: []*Tab{tb}}).Tabs[0].Panes }
	ps := snap()
	f, r, w := ps[0], ps[1], ps[2]
	if f.Conn != "failed" || f.Err != "ssh: connect to host 10.0.0.9 port 22: Connection timed out" || f.CanFix || f.Tries != 3 {
		t.Errorf("failed %+v", f)
	}
	if r.Conn != "reconnecting" || !r.Since.Equal(at) || !r.Retry.Equal(at.Add(time.Hour)) || r.Tries != 2 || r.Err != "" {
		t.Errorf("reconnecting %+v", r)
	}
	if w.Conn != "timeout" || !w.Since.Equal(at.Add(-connectWait)) || w.Err != "" {
		t.Errorf("timeout %+v", w)
	}
	failed.err = &sshc.NegotiationError{What: "HostKeyAlgorithms", Theirs: []string{"ssh-rsa"}}
	if f := snap()[0]; !f.CanFix || f.Err != "Unable to negotiate: no matching HostKeyAlgorithms found. Their offer: ssh-rsa" || f.KeyChange != nil {
		t.Errorf("old algorithms %+v", f)
	}
	failed.err = &sshc.HostKeyChangedError{Host: "web1", Type: "ED25519", Fingerprint: "SHA256:new",
		Known: []sshc.KnownLine{{File: "/home/me/.ssh/known_hosts", Line: 3, Type: "ED25519", Fingerprint: "SHA256:old"}}}
	if f := snap()[0]; f.CanFix || f.KeyChange == nil || f.KeyChange.Remote != "SHA256:new" || len(f.KeyChange.Old) != 1 || f.KeyChange.Old[0] != (oldKey{"/home/me/.ssh/known_hosts", 3, "ED25519", "SHA256:old"}) {
		t.Errorf("changed key %+v", f)
	}
}

// TestConnText: what the connection box says in each state.
func TestConnText(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		p                 guiPane
		title, sub, guide string
	}{
		{guiPane{Name: "web1", Since: now.Add(-3 * time.Second)}, "Connecting to web1…", "Waiting for the server · 3s", ""},
		{guiPane{Name: "web1", Conn: "timeout", Since: now.Add(-15 * time.Second)}, "web1 isn't answering yet", "15s and counting. A VPN may be down.", ""},
		{guiPane{Name: "web1", Conn: "timeout", Since: now.Add(time.Second)}, "web1 isn't answering yet", "0s and counting. A VPN may be down.", ""},
		{guiPane{Name: "db1", Conn: "reconnecting", Since: now.Add(-4 * time.Second)}, "Reconnecting to db1…", "The connection dropped 4s ago", ""},
		{guiPane{Name: "db1", Conn: "reconnecting", Since: now.Add(-4 * time.Second), Tries: 2, Retry: now.Add(8 * time.Second)},
			"Reconnecting to db1…", "The connection dropped 4s ago · tried 2 times, next in 8s", ""},
		{guiPane{Name: "db1", Conn: "failed"}, "Couldn't connect to db1", "", "The connection ended before the shell started. If the server is up, check the VPN; the scrollback stays as it was."},
		{guiPane{Name: "db1", Conn: "failed", Tries: 5, CanFix: true}, "Couldn't connect to db1", "Tried 5 times",
			"The server offers only algorithms modern SSH turns off. Fix adds them to its ssh_config and reconnects."},
	} {
		c.p.Retry = c.p.Retry.Add(500 * time.Millisecond) // whole seconds read the same a moment later
		c.p.Since = c.p.Since.Add(-500 * time.Millisecond)
		title, sub, guide := connText(c.p, now)
		if title != c.title || sub != c.sub || guide != c.guide {
			t.Errorf("%s: %q / %q / %q", c.p.Conn, title, sub, guide)
		}
	}
	if s := lastLine("banner\nssh: connect to host x port 22: No route to host  \n\n"); s != "ssh: connect to host x port 22: No route to host" {
		t.Errorf("lastLine %q", s)
	}
}

func TestGMDriverActions(t *testing.T) {
	h := newGM(t)
	d := h.d
	// open, activate, closeTab: in range only
	m := &model{tree: []node{{depth: 1, label: "web"}, {depth: 1, label: "db"}}, tabs: []*Tab{{name: "a"}, {name: "b"}}, group: "g"}
	d.open(5)
	d.open(1)
	d.activate(7)
	d.activate(1)
	h.runActs(m)
	if m.cursor != 1 || m.active() != 1 || m.group != "" {
		t.Errorf("cursor %d active %d group %q", m.cursor, m.active(), m.group)
	}
	d.open(-1)
	d.activate(-1)
	d.closeTab(-1)
	d.closeTab(2)
	h.runActs(m)
	if m.cursor != 1 || m.active() != 1 || len(m.tabs) != 2 {
		t.Errorf("out of range moved: cursor %d active %d tabs %d", m.cursor, m.active(), len(m.tabs))
	}
	d.closeTab(0)
	h.runActs(m)
	if len(m.tabs) != 1 || m.tabs[0].name != "b" {
		t.Errorf("closeTab: %d tabs", len(m.tabs))
	}

	// press, focusPane, closePane
	t1, _ := gmTerm(t, "web1", 40, 6)
	t2, _ := gmTerm(t, "web1", 40, 6)
	t1.b, t2.b = &pos{}, &pos{}
	tb := &Tab{org: "Acme", pinned: map[int]bool{}, panes: []*Pane{{id: 1, term: t1, waitUntil: time.Now().Add(time.Hour)}, {id: 2, term: t2, waitUntil: time.Now().Add(time.Hour)}}}
	tb.focusPane(0)
	m = &model{tabs: []*Tab{tb}}
	d.press("ctrl+s")
	h.runActs(m)
	if !tb.pinScreen {
		t.Error("press ctrl+s pins the screen")
	}
	d.focusPane(5)
	d.focusPane(-1)
	d.focusPane(1)
	h.runActs(m)
	if tb.focused() != tb.panes[1] {
		t.Error("focusPane: the second pane isn't focused")
	}
	d.closePane(-1)
	d.closePane(9)
	h.runActs(m)
	if len(tb.panes) != 2 {
		t.Error("closePane out of range closed one")
	}
	d.closePane(0)
	h.runActs(m)
	if len(tb.panes) != 1 || tb.panes[0].term != t2 {
		t.Errorf("closePane(0) left %d panes", len(tb.panes))
	}
	none := &model{}
	d.focusPane(0)
	d.closePane(0)
	d.compact()
	d.clearContext()
	d.pin(1)
	d.answer("y", "")
	d.reply("x", true)
	if h.runActs(none) != 7 {
		t.Error("actions without a tab")
	}

	// ask, stop, compact, clearContext, toggleBroadcast on a busy tab
	pm, pt := gmProbe(t)
	c, rec := claudeA(t)
	pt.claude = c
	d.ask("  uptime  ")
	h.runActs(pm)
	if len(pt.asked) != 1 || pt.asked[0] != "uptime" {
		t.Errorf("ask queued %v", pt.asked)
	}
	d.compact()
	h.runActs(pm)
	if !strings.Contains(pm.flash, "compact") {
		t.Errorf("compact: %q", pm.flash)
	}
	d.clearContext()
	h.runActs(pm)
	if !strings.Contains(pm.flash, "clear") {
		t.Errorf("clear: %q", pm.flash)
	}
	d.toggleBroadcast()
	h.runActs(pm)
	if !strings.Contains(pm.flash, "broadcast needs") {
		t.Errorf("broadcast: %q", pm.flash)
	}
	d.stop()
	h.runActs(pm)
	if lastChatA(pt) != "■ stopped" || rec.String() == "" {
		t.Errorf("stop: %q %q", lastChatA(pt), rec.String())
	}
	d.pin(3)
	h.runActs(pm)
	if !pt.pinned[3] {
		t.Error("pin pins")
	}
	d.pin(3)
	h.runActs(pm)
	if pt.pinned[3] {
		t.Error("pin again unpins")
	}

	// group, broadcast
	d.group("g1")
	h.runActs(pm)
	if pm.group != "g1" {
		t.Errorf("group %q", pm.group)
	}
	d.broadcast("q")
	h.runActs(pm)
	if pm.input != "" && pm.input != "q" {
		t.Errorf("broadcast input %q", pm.input)
	}
	pm.group, pm.input = "", ""
	d.broadcast("q")
	h.runActs(pm)
	if pm.input != "" {
		t.Errorf("broadcast without a group view set %q", pm.input)
	}

	// palette, palPick
	q := "zz-no-such-item"
	d.palette(&q)
	h.runActs(pm)
	if !d.palOpen || pm.palQ != q {
		t.Errorf("palette open %v q %q", d.palOpen, pm.palQ)
	}
	d.palette(nil)
	h.runActs(pm)
	if d.palOpen || pm.palQ != q {
		t.Errorf("palette closed %v q %q", d.palOpen, pm.palQ)
	}
	pm.palQ = ""
	d.palOpen = true
	d.palPick(1000)
	h.runActs(pm)
	if pm.pal || d.palOpen {
		t.Error("palPick closes the palette")
	}
	two := &model{tabs: []*Tab{{name: "one", org: "Acme", pinned: map[int]bool{}}, {name: "two", org: "Acme", pinned: map[int]bool{}}}}
	idx := -1
	for j, it := range two.palItemsFor() {
		if it.kind == "sessions" && it.tab == 1 {
			idx = j
		}
	}
	if idx < 0 {
		t.Fatal("no session item")
	}
	d.palPick(idx)
	h.runActs(two)
	if two.active() != 1 {
		t.Errorf("palPick(%d) active %d", idx, two.active())
	}
}

// palItemsFor is the palette list as palPick sees it.
func (m *model) palItemsFor() []palItem {
	m.pal = true
	defer func() { m.pal = false }()
	return m.palItems()
}

func TestGMAnswerAndReply(t *testing.T) {
	h := newGM(t)
	done := make(chan approvalResult, 2)
	a := &approval{cmd: "rm -rf /tmp/x", done: done}
	tb := &Tab{org: "Acme", pinned: map[int]bool{}, pending: []*approval{a}}
	m := &model{tabs: []*Tab{tb}}
	h.d.answer("y", "rm -rf /tmp/y") // edited first
	h.runActs(m)
	if r := <-done; !r.ok || r.cmd != "rm -rf /tmp/y" {
		t.Errorf("edited answer %+v", r)
	}
	s := &approval{cmd: "secret", secret: true, done: done}
	tb.pending = []*approval{s}
	h.d.answer("n", "changed") // a secret can't be edited
	h.runActs(m)
	if r := <-done; r.ok || s.cmd != "secret" {
		t.Errorf("secret answer %+v cmd %q", r, s.cmd)
	}
	p := &approval{cmd: "ls", done: done}
	tb.pending = []*approval{p}
	h.d.answer("y", "ls") // the same command: no edit
	h.runActs(m)
	if r := <-done; !r.ok || p.editing {
		t.Errorf("same command %+v", r)
	}

	q := &question{title: "pass", done: make(chan string, 1)}
	m.asks = []*question{q}
	h.d.reply("hunter2", true)
	h.runActs(m)
	if v, ok := <-q.done; !ok || v != "hunter2" || len(m.asks) != 0 {
		t.Errorf("reply %q %v", v, ok)
	}
	q2 := &question{title: "pass", done: make(chan string, 1)}
	m.asks = []*question{q2}
	h.d.reply("x", false)
	h.runActs(m)
	if _, ok := <-q2.done; ok || len(m.asks) != 0 {
		t.Error("cancel sends nothing")
	}
}

func TestGMSaveLayout(t *testing.T) {
	h := newGM(t)
	h.u.claude, h.u.claudeW = false, 512
	h.u.saveLayout()
	m := &model{}
	h.runActs(m)
	if m.guiLayout != (guiLayout{NoClaude: true, ClaudeW: 512}) {
		t.Errorf("layout %+v", m.guiLayout)
	}
	if _, err := os.Stat(uiPath()); err != nil {
		t.Errorf("layout.json: %v", err)
	}
	u2 := newUI(h.d, guiLayout{NoSidebar: true, ClaudeW: 333}, func() {})
	if u2.side || !u2.claude || u2.claudeW != 333 {
		t.Errorf("newUI from layout: side %v claude %v w %v", u2.side, u2.claude, u2.claudeW)
	}
}

func gmTabs(org string, n int) []guiTab {
	var ts []guiTab
	for i := range n {
		ts = append(ts, guiTab{Key: &Tab{}, Name: "h" + string(rune('a'+i)), Org: org, Group: "web", Dir: "/d", Account: "work"})
	}
	return ts
}

func TestGMShortcut(t *testing.T) {
	h := newGM(t)
	u := h.u
	tabs := append(gmTabs("Acme", 3), gmTabs("Globex", 1)...)
	h.set(&guiState{Tabs: tabs})
	u.org = "Acme"
	mod := keys.Mod
	sc := func(name key.Name) bool { return u.shortcut(key.Event{Name: name, Modifiers: mod, State: key.Press}) }
	if u.shortcut(key.Event{Name: "K", Modifiers: mod, State: key.Release}) || u.shortcut(key.Event{Name: "K", Modifiers: key.ModCtrl, State: key.Press}) {
		t.Error("a release or a plain ctrl isn't a shortcut")
	}
	if sc("Q") {
		t.Error("Q isn't one")
	}
	h.acts()
	if !sc("K") || !u.pal {
		t.Error("K opens the palette")
	}
	h.acts()
	pm, pt := gmProbe(t)
	pt.busy = false
	for _, n := range []key.Name{"T", "W", "Y", "P", "S"} {
		if !sc(n) || len(h.acts()) != 1 {
			t.Errorf("%s presses a key", n)
		}
	}
	u.wantTerm = false
	if sc("Y"); len(h.acts()) != 1 || !u.wantTerm {
		t.Error("Y types the suggestion into the terminal, which takes the keys")
	}
	sc("S")
	h.runActs(pm)
	if !pt.pinScreen {
		t.Error("S pins the screen")
	}
	claude := u.claude
	sc("J")
	if u.claude == claude || len(h.acts()) != 1 {
		t.Error("J toggles the Claude panel and saves")
	}
	// G: the group view on and off
	sc("G")
	gm := &model{}
	h.runActs(gm)
	if gm.group != groupKey("/d", "web") {
		t.Errorf("G group %q", gm.group)
	}
	u.st.Group = "x"
	sc("G")
	h.runActs(gm)
	if gm.group != "" {
		t.Errorf("G again %q", gm.group)
	}
	u.st.Group = ""
	u.st.Tabs[0].OrgOnly = true
	sc("G")
	if len(h.acts()) != 0 {
		t.Error("an org tab has no group view")
	}
	u.st.Tabs[0].OrgOnly = false
	u.toggleComposer = false
	sc("L")
	if !u.toggleComposer {
		t.Error("L toggles the composer")
	}
	sc("F") // a tab without panes: a toast, no split
	if u.toast == "" || u.files[tabs[0].Key] != nil {
		t.Errorf("F: toast %q", u.toast)
	}
	sc("H")
	sc(key.NameUpArrow)
	sc(key.NameDownArrow)
	u.copySelection = false
	sc("C")
	if !u.copySelection {
		t.Error("C copies the terminal's selection")
	}
	u.chatSel = &selText{}
	u.copySelection = false
	sc("C")
	if !u.copySelection {
		t.Error("C with an empty chat selection copies the terminal's")
	}
	u.chatSel = &selText{src: []rune("uptime ok"), sel: [2]int{0, 6}}
	u.copySelection = false
	sc("C")
	if u.copySelection || len(u.clipOut) != 1 || gmClip(u) != "uptime" {
		t.Error("C copies the chat's selection")
	}
	u.chatSel = nil
	sc("V")
	if !u.pasteWanted {
		t.Error("V pastes")
	}
	u.wantTerm = false
	sc("3")
	am := &model{tabs: tabsA(4)}
	h.runActs(am)
	if am.active() != 2 || !u.wantTerm {
		t.Errorf("3 activates the org's third tab: %d", am.active())
	}
	sc("4") // Acme has three
	if len(h.acts()) != 0 {
		t.Error("4 is past the org's tabs")
	}
	if n := map[bool]int{true: 24, false: 26}[keys.Mod == key.ModCommand]; len(shortcutFilters()) != n || len(ShortcutFilters()) != n { // ⌘C, ⌘V are the editors'
		t.Error("filters")
	}
	if u.shortcut(key.Event{Name: "3", Modifiers: mod, State: key.Release}) || u.shortcut(key.Event{Name: "3", Modifiers: key.ModCtrl, State: key.Press}) || u.shortcut(key.Event{Name: "0", Modifiers: mod, State: key.Press}) {
		t.Error("a digit's release, plain Ctrl or 0 isn't a tab")
	}
	if u.shortcut(key.Event{Name: "K", Modifiers: mod | key.ModAlt, State: key.Press}) {
		t.Error("an extra Alt makes another chord")
	}
}

// TestGMShortcutRebound: a rebinding in keys moves the workspace's key,
// its filter and the tip that names it.
func TestGMShortcutRebound(t *testing.T) {
	h := newGM(t)
	u := h.u
	keys.Set("infra.pane", keys.Chord{Name: "N", Mods: key.ModAlt})
	t.Cleanup(func() { keys.Load(nil) })
	if u.shortcut(key.Event{Name: "T", Modifiers: keys.Mod, State: key.Press}) {
		t.Error("the old chord still opens a pane")
	}
	if !u.shortcut(key.Event{Name: "N", Modifiers: key.ModAlt, State: key.Press}) || len(h.acts()) != 1 {
		t.Error("Alt+N opens a pane")
	}
	found := false
	for _, f := range shortcutFilters() {
		if kf, ok := f.(key.Filter); ok && kf.Name == "N" && kf.Required == key.ModAlt {
			found = true
		}
	}
	if !found || !strings.Contains(tbTips("tbPane"), "⌥N") {
		t.Errorf("filter %v, tip %q", found, tbTips("tbPane"))
	}
}

func TestGMCycleTab(t *testing.T) {
	h := newGM(t)
	u := h.u
	tabs := append(append(gmTabs("Globex", 1), gmTabs("Acme", 3)...), gmTabs("Initech", 1)...)
	u.org = "Acme"
	step := func(active int, name key.Name) int {
		t.Helper()
		h.set(&guiState{Tabs: tabs, Active: active})
		u.wantTerm = false
		if !u.shortcut(key.Event{Name: name, Modifiers: keys.Mod, State: key.Press}) || !u.wantTerm {
			t.Fatalf("%s from %d isn't handled", name, active)
		}
		m := &model{}
		for range tabs {
			m.tabs = append(m.tabs, &Tab{})
		}
		h.runActs(m)
		return slices.Index(m.tabs, m.focus) // -1: none activated
	}
	for _, c := range []struct {
		from int
		name key.Name
		want int
	}{
		{1, key.NamePageDown, 2}, {2, key.NamePageDown, 3}, {3, key.NamePageDown, 1}, // wraps
		{3, key.NamePageUp, 2}, {2, key.NamePageUp, 1}, {1, key.NamePageUp, 3},
		{0, key.NamePageDown, 1}, {0, key.NamePageUp, 3}, // from another org's tab
	} {
		if got := step(c.from, c.name); got != c.want {
			t.Errorf("%s from %d: %d, want %d", c.name, c.from, got, c.want)
		}
	}
	u.org = "Umbrella" // no tabs
	h.set(&guiState{Tabs: tabs})
	u.shortcut(key.Event{Name: key.NamePageDown, Modifiers: keys.Mod, State: key.Press})
	if len(h.acts()) != 0 {
		t.Error("an org without tabs has none to go to")
	}
}

func TestGMFollow(t *testing.T) {
	h := newGM(t)
	u := h.u
	tabs := append(gmTabs("Acme", 2), gmTabs("Globex", 2)...)
	st := h.set(&guiState{Tabs: tabs, Active: 1})
	u.org = "Acme"
	u.follow("Acme") // same org: remembers the active tab
	if u.lastTab["Acme"] != tabs[1].Key || len(h.acts()) != 0 {
		t.Error("follow in place")
	}
	u.follow("Globex") // no tab used there yet: its first
	m := &model{tabs: tabsA(4)}
	h.runActs(m)
	if m.active() != 2 || u.org != "Globex" {
		t.Errorf("to Globex: active %d", m.active())
	}
	st.Active = 3
	u.follow("Globex")
	u.follow("Acme") // back: the tab last used there
	h.runActs(m)
	if m.active() != 1 || u.lastTab["Globex"] != tabs[3].Key {
		t.Errorf("back to Acme: active %d", m.active())
	}
	st.Active = 1
	u.follow("Initech") // no tabs, a group view: leave it
	st.Group = "g"
	u.org = ""
	u.follow("Initech")
	h.runActs(m)
	if m.group != "" {
		t.Errorf("group %q", m.group)
	}
	st.Group = ""
	u.org = "x"
	u.follow("Initech")
	if len(h.acts()) != 0 {
		t.Error("nothing to do")
	}
	st.Active = 2 // the active tab is already the org's
	u.org = "x"
	u.follow("Globex")
	if len(h.acts()) != 0 {
		t.Error("already there")
	}
}

func TestGMGroupOrgAndTabs(t *testing.T) {
	h := newGM(t)
	u := h.u
	st := h.set(&guiState{Tree: []guiNode{{Depth: 0, Label: "Acme", Org: "Acme"}, {Depth: 2, Label: "web1", Org: "Acme", Group: "web", Host: "web1", Dir: "/d"}}})
	if u.groupOrg() != "" {
		t.Error("no group view")
	}
	st.Group = groupKey("/d", "web")
	if u.groupOrg() != "Acme" {
		t.Errorf("groupOrg %q", u.groupOrg())
	}
	st.Group = groupKey("/d", "db")
	if u.groupOrg() != "" {
		t.Error("another group")
	}
	if u.activeTab() != nil || u.tab() != nil {
		t.Error("no tabs")
	}
	st.Tabs = gmTabs("Globex", 1)
	u.org = "Acme"
	if u.activeTab() == nil || u.tab() != nil {
		t.Error("another org's tab isn't shown")
	}
	u.org = "Globex"
	if u.tab() == nil {
		t.Error("the org's tab")
	}
	gtx := h.gtx()
	if hgap(5)(gtx).Size != image.Pt(5, 0) || vgap(3)(gtx).Size != image.Pt(0, 3) {
		t.Error("gaps")
	}
}

func TestGMFrame(t *testing.T) {
	h := newGM(t)
	u := h.u
	t.Cleanup(func() { applyTheme("desk") })
	tabs := gmTabs("Acme", 1)
	st := &guiState{Tabs: tabs, Theme: "dracula"}
	if _, ok := themes["dracula"]; !ok {
		for n := range themes {
			if n != "desk" {
				st.Theme = n
				break
			}
		}
	}
	h.set(st)
	h.frame("Acme")
	if guiTheme != st.Theme {
		t.Errorf("theme %q, want %q", guiTheme, st.Theme)
	}
	// the window-wide shortcut handler
	h.key("J", keys.Mod)
	claude := u.claude
	h.frame("Acme")
	if u.claude == claude {
		t.Error("ctrl+shift+J reached shortcut")
	}
	h.acts()
	// a change while drawing asks for another frame: the directory split's
	// deferred actions run inside it
	fv := gmFiles(u, tabs[0].Key)
	fv.after = []func(){u.redraw}
	gen := u.gen.Load()
	h.frame("Acme")
	if u.gen.Load() == gen || fv.after != nil {
		t.Error("the split's action ran in the frame")
	}
	// the group view replaces the panes when it is the org's
	st.Tree = []guiNode{{Depth: 2, Label: "web1", Org: "Acme", Group: "web", Host: "web1", Dir: "/d"}}
	st.Group = groupKey("/d", "web")
	u.claude = false
	h.frame("Acme")
}

// --- gui_draw.go ---

func TestGMDrawPrimitives(t *testing.T) {
	h := newGM(t)
	u := h.u
	h.draw(func(gtx layout.Context) {
		fillRRect(gtx.Ops, image.Rect(0, 0, 10, 10), 4, color.NRGBA{}) // transparent: nothing
		fillRRect(gtx.Ops, image.Rectangle{}, 4, cRed)                 // empty: nothing
		fillRRect(gtx.Ops, image.Rect(0, 0, 100, 50), 4, cRed)
		fillRRect(gtx.Ops, image.Rect(0, 0, 3000, 2500), 8, cRed) // past the texture limit: in pieces
		panel(gtx, image.Rect(0, 0, 50, 50), 4, cPane, cLine)
		panel(gtx, image.Rect(0, 0, 50, 50), 4, cPane, color.NRGBA{})
		if d := dot(cRed, true)(gtx); d.Size != image.Pt(13, 13) {
			t.Errorf("halo dot %v", d.Size)
		}
		if d := dot(cRed, false)(gtx); d.Size != image.Pt(7, 7) {
			t.Errorf("dot %v", d.Size)
		}
		for _, ico := range []func(*clip.Path, float32){icoSplit, icoGrid, icoPanel, icoSearch, icoUp, icoBroadcast, icoStop, icoSpark,
			icoShield, icoChevron, icoChevronRight, icoStar, icoPlus, icoX} {
			if d := icon(14, cText, ico)(gtx); d.Size != image.Pt(14, 14) {
				t.Errorf("icon %v", d.Size)
			}
		}
		u.pill("Claude", cPurple, cPurpleSoft)(gtx)
		u.background(gtx)
		var e widget.Editor
		hint := u.editor(gtx, &e, "a hint", 12, sansFont, cText)
		e.SetText("typed")
		typed := u.editor(gtx, &e, "a hint", 12, sansFont, cText)
		if hint.Size.Y == 0 || typed.Size.Y == 0 {
			t.Error("editor sizes")
		}
		if u.btn(gtx, "b1", "Go", cText, cAcc) {
			t.Error("not clicked yet")
		}
	})
	if u.click("same") != u.click("same") || u.click("same") == u.click("other") {
		t.Error("clickables by id")
	}
	u.click("b1").Click()
	h.draw(func(gtx layout.Context) {
		if !u.btn(gtx, "b1", "Go", cText, cAcc) {
			t.Error("a click reports")
		}
	})
	// a hovered button takes the hover tint
	w := func(gtx layout.Context) {
		u.button(gtx, "hv", cPane, cHover, rCtl, layout.UniformInset(5), u.lbl("Hover me", 12, cText, sansFont))
	}
	h.draw(w)
	h.hover(u.click("hv"), image.Rect(2, 2, 40, 20), 4, w)
}

func TestGMClipboardAndToast(t *testing.T) {
	h := newGM(t)
	u := h.u
	for _, s := range []string{"a", "b", "c", "d", "e"} { // the fifth is dropped: the queue holds four
		u.setClipboard(s)
	}
	if len(u.clipOut) != 4 {
		t.Errorf("queued %d", len(u.clipOut))
	}
	h.draw(func(gtx layout.Context) { u.flushClipboard(gtx) })
	if len(u.clipOut) != 0 {
		t.Error("flushed")
	}
	u.showToast("ok", false)
	if u.toastBad || time.Until(u.toastUntil) > 3*time.Second {
		t.Errorf("good toast until %v", time.Until(u.toastUntil))
	}
	u.showToast("bad", true)
	if !u.toastBad || time.Until(u.toastUntil) < 5*time.Second {
		t.Errorf("bad toast until %v", time.Until(u.toastUntil))
	}
}

// --- gui_theme.go ---

func TestGMTheme(t *testing.T) {
	t.Cleanup(func() { applyTheme("desk") })
	if hex("#ff0000", cIdle) != rgb(0xff0000) || hex("zz", cIdle) != cIdle || hex("#fff", cIdle) != cIdle {
		t.Error("hex")
	}
	if c := mix(rgb(0), rgb(0xffffff), 0.5); c.R != 127 || c.A != 0xff {
		t.Errorf("mix %v", c)
	}
	if lookFor(Palette{}) != deskLook {
		t.Error("mono is desk's")
	}
	dark := lookFor(Palette{Rule: "#202020", Accent: "#00ff00", Chroma: "monokai"})
	light := lookFor(Palette{Rule: "#e0e0e0", Light: true})
	if dark.acc != rgb(0x00ff00) || dark.chroma != "monokai" || dark.rim != rgb(0x202020) {
		t.Errorf("dark %+v", dark)
	}
	if light.chroma != deskLook.chroma || light.ansi[0] != light.text2 || light.pane.R <= dark.pane.R {
		t.Errorf("light %+v", light)
	}
	applyTheme("no-such-theme")
	if guiTheme != "desk" {
		t.Error("an unknown theme changes nothing")
	}
	for n := range themes {
		if n == "desk" {
			continue
		}
		applyTheme(n)
		if guiTheme != n || tabDot["working"] != cPurple {
			t.Errorf("theme %q", n)
		}
		break
	}
	applyTheme("desk")
	if cPanel != deskLook.panel || ansi16 != deskLook.ansi {
		t.Error("back to desk")
	}
}

// --- gui_side.go ---

// gmTree is two orgs: Acme (web: web1 web2 web3; db: db1) and Globex (app: app1).
func gmTree() []guiNode {
	return []guiNode{
		{Depth: 0, Label: "Acme", Org: "Acme"},
		{Depth: 1, Label: "web", Org: "Acme"},
		{Depth: 2, Label: "web1", Org: "Acme", Group: "web", Host: "web1", Addr: "10.0.0.1", Dir: "/d"},
		{Depth: 2, Label: "web2", Org: "Acme", Group: "web", Host: "web2", Dir: "/d", Alert: "WARNING"},
		{Depth: 2, Label: "web3", Org: "Acme", Group: "web", Host: "web3.example.com", Dir: "/d", Alert: "CRITICAL", Note: "nginx front"},
		{Depth: 1, Label: "db", Org: "Acme"},
		{Depth: 2, Label: "db1", Org: "Acme", Group: "db", Host: "db1", Addr: "10.0.1.1", Dir: "/d", Pin: true},
		{Depth: 0, Label: "Globex", Org: "Globex"},
		{Depth: 1, Label: "app", Org: "Globex"},
		{Depth: 2, Label: "app1", Org: "Globex", Group: "app", Host: "app1", Addr: "10.1.0.1", Dir: "/g"},
	}
}

// gmSideState: web1 has its own tab (two panes, one up, working); web2 is a
// pane of Acme's org tab, which waits for an approval.
func gmSideState() *guiState {
	return &guiState{Tree: gmTree(), Tabs: []guiTab{
		{Key: &Tab{}, Name: "web1", Org: "Acme", Group: "web", Dir: "/d", State: "working", Account: "work",
			Panes: []guiPane{{Name: "web1", Conn: ""}, {Name: "web1", Conn: "timeout"}}},
		{Key: &Tab{}, Name: "Acme", Org: "Acme", OrgOnly: true, State: "approval", Account: "work",
			Panes: []guiPane{{Name: "web2", Dir: "/d", Conn: "timeout"}}},
	}}
}

func gmRow(rows []sideRow, kind, label string) (sideRow, int) {
	for k, r := range rows {
		if r.kind == kind && r.label == label {
			return r, k
		}
	}
	return sideRow{}, -1
}

func TestGMHostRows(t *testing.T) {
	h := newGM(t)
	u := h.u
	st := h.set(gmSideState())
	tr := st.Tree
	if u.hostTab(tr[2]) != 0 || u.hostTab(tr[3]) != -1 || u.openTab(tr[3]) != 1 || u.openTab(tr[4]) != -1 {
		t.Errorf("hostTab %d %d openTab %d %d", u.hostTab(tr[2]), u.hostTab(tr[3]), u.openTab(tr[3]), u.openTab(tr[4]))
	}
	other := tr[3]
	other.Dir = "/elsewhere"
	if u.openTab(other) != -1 {
		t.Error("the org tab's pane is another dir's")
	}
	r := u.hostRow(2, tr[2])
	if !r.open || !r.sel || r.dot != cPurple || !r.halo || r.meta != "2 panes" || r.wait {
		t.Errorf("web1 %+v", r)
	}
	r = u.hostRow(3, tr[3])
	if !r.open || r.sel || r.dot != cOrange || !r.wait || r.meta != "approve" {
		t.Errorf("web2 (approval beats its alert) %+v", r)
	}
	r = u.hostRow(4, tr[4])
	if r.open || r.dot != cRed || r.halo || r.meta != "CRITICAL" {
		t.Errorf("web3 %+v", r)
	}
	if r := u.hostRow(9, tr[9]); r.dot != cIdle || r.meta != "10.1.0.1" {
		t.Errorf("app1 %+v", r)
	}
	n := guiNode{Label: "x", Host: "x"}
	if r := u.hostRow(0, n); r.meta != "" {
		t.Errorf("no meta %+v", r)
	}
	// a pane still connecting: orange, no halo; one up: green with halo
	st.Tabs[0].State, st.Tabs[0].Panes = "", []guiPane{{Name: "web1", Conn: "timeout"}}
	if r := u.hostRow(2, tr[2]); r.dot != cOrange || r.halo || r.meta != "10.0.0.1" {
		t.Errorf("connecting %+v", r)
	}
	st.Tabs[0].Panes[0].Conn = ""
	if r := u.hostRow(2, tr[2]); r.dot != cGreen || !r.halo {
		t.Errorf("up %+v", r)
	}
	st.Group = "g"
	if r := u.hostRow(2, tr[2]); r.sel {
		t.Error("the group view selects no host")
	}
	if worse(cGreen, cRed) != cRed || worse(cRed, cOrange) != cRed || worse(cIdle, cPurple) != cPurple || worse(cOrange, cPurple) != cOrange || worse(cIdle, cIdle) != cIdle {
		t.Error("worse")
	}
}

func TestGMSideRows(t *testing.T) {
	h := newGM(t)
	u := h.u
	h.set(gmSideState())
	rows := u.sideRows()
	// pinned first (db1 from the servers file), then both orgs folded: Acme
	// keeps its open hosts flat and its worst dot
	if rows[0].kind != "section" || rows[0].label != "PINNED" || rows[0].meta != "1" || rows[1].label != "db1" || !rows[1].pin || rows[1].meta != "db" {
		t.Fatalf("pinned %+v %+v", rows[0], rows[1])
	}
	acme, ak := gmRow(rows, "org", "Acme")
	if !acme.closed || acme.dot != cRed || acme.meta != "2 open · 4" {
		t.Errorf("Acme %+v", acme)
	}
	if rows[ak+1].label != "web1" || !rows[ak+1].flat || rows[ak+2].label != "web2" || !rows[ak+2].flat {
		t.Errorf("flat open hosts %+v %+v", rows[ak+1], rows[ak+2])
	}
	if _, k := gmRow(rows, "host", "web3"); k >= 0 {
		t.Error("a folded org hides its closed hosts")
	}
	if g, k := gmRow(rows, "org", "Globex"); k < 0 || g.meta != "1" || g.dot != cIdle || g.color != seriesColor(1) {
		t.Errorf("Globex %+v", g)
	}
	// unfolded Acme: groups and hosts; the db group folded shows its dot
	u.closed["orgopen:Acme"], u.closed["Acme/db"] = true, true
	rows = u.sideRows()
	web, wk := gmRow(rows, "group", "web")
	if wk < 0 || web.closed || web.meta != "3" || web.color != cRed || rows[wk+1].label != "web1" || rows[wk+3].label != "web3" {
		t.Errorf("web group %+v", web)
	}
	db, dk := gmRow(rows, "group", "db")
	if dk < 0 || !db.closed || rows[dk+1].kind != "org" {
		t.Errorf("db folded %+v next %+v", db, rows[dk+1])
	}
	// search: matches only, everything unfolded, no pinned section
	u.search.SetText("web3")
	rows = u.sideRows()
	if rows[0].kind != "org" || len(rows) != 3 || rows[2].label != "web3" || len(rows[2].match) == 0 {
		t.Errorf("search %+v", rows)
	}
	u.search.SetText("nginx") // the note is searched too
	if rows = u.sideRows(); len(rows) != 3 || rows[2].label != "web3" {
		t.Errorf("note search %+v", rows)
	}
	u.search.SetText("zzqq")
	if rows = u.sideRows(); len(rows) != 1 || rows[0].kind != "empty" || !strings.Contains(rows[0].label, "zzqq") {
		t.Errorf("no match %+v", rows)
	}
	if sidePick(nil, 0) != -1 {
		t.Error("sidePick of nothing")
	}
	rs := []sideRow{{kind: "org"}, {kind: "host"}, {kind: "group"}, {kind: "host"}}
	if sidePick(rs, 0) != 1 || sidePick(rs, 1) != 3 || sidePick(rs, 2) != -1 {
		t.Error("sidePick")
	}
}

func TestGMPins(t *testing.T) {
	h := newGM(t)
	u := h.u
	if len(loadPins()) != 0 {
		t.Error("no pins yet")
	}
	n := guiNode{Org: "Acme", Group: "web", Label: "web1"}
	if hostKey(n) != "Acme/web/web1" {
		t.Errorf("hostKey %q", hostKey(n))
	}
	u.togglePin("Acme/web/web1")
	u.togglePin("Acme/web/web2")
	if p := loadPins(); !p["Acme/web/web1"] || !p["Acme/web/web2"] || len(p) != 2 {
		t.Errorf("pins %v", p)
	}
	u.togglePin("Acme/web/web1")
	if p := loadPins(); p["Acme/web/web1"] || len(p) != 1 {
		t.Errorf("unpinned %v", p)
	}
	if b, _ := os.ReadFile(pinsPath()); string(b) != "Acme/web/web2\n" {
		t.Errorf("pins file %q", b)
	}
}

func TestGMSidebarKeys(t *testing.T) {
	h := newGM(t)
	u := h.u
	h.set(gmSideState())
	u.closed["orgopen:Acme"] = true
	w := func(gtx layout.Context) { u.sidebar(gtx) }
	h.draw(func(gtx layout.Context) {
		gtx.Execute(key.FocusCmd{Tag: &u.search})
		u.sidebar(gtx)
	})
	h.draw(w)
	h.key(key.NameDownArrow, 0)
	h.key(key.NameDownArrow, 0)
	h.draw(w)
	if u.sideSel != 2 {
		t.Errorf("down twice: %d", u.sideSel)
	}
	h.key(key.NameUpArrow, 0)
	h.draw(w)
	if u.sideSel != 1 {
		t.Errorf("up: %d", u.sideSel)
	}
	for range 10 {
		h.key(key.NameDownArrow, 0)
	}
	h.draw(w)
	if u.sideSel != 6 { // the last host row: db1 pinned, web1-3, db1, app1 (Globex folded hides it) …
		rows, n := u.sideRows(), 0
		for _, r := range rows {
			if r.kind == "host" {
				n++
			}
		}
		if u.sideSel != n-1 {
			t.Errorf("clamped to %d, %d hosts", u.sideSel, n)
		}
	}
	for range 10 {
		h.key(key.NameUpArrow, 0)
	}
	h.draw(w)
	if u.sideSel != 0 {
		t.Errorf("clamped to 0: %d", u.sideSel)
	}
	// enter opens the keyboard row's host: row 0 is the pinned db1
	h.acts()
	h.key(key.NameReturn, 0)
	h.draw(w)
	m := &model{tree: make([]node, 10)}
	for i := range m.tree {
		m.tree[i] = node{depth: 1}
	}
	if h.runActs(m) != 1 || m.cursor != 6 {
		t.Errorf("enter opened row %d", m.cursor)
	}
	// typing resets the pick; esc clears the search
	u.sideSel = 2
	h.r.Queue(key.EditEvent{Text: "web"})
	h.draw(w)
	if u.search.Text() != "web" || u.sideSel != 0 {
		t.Errorf("typed %q sel %d", u.search.Text(), u.sideSel)
	}
	u.wantTerm = false
	h.key(key.NameEscape, 0)
	h.draw(w)
	if u.search.Text() != "" || !u.wantTerm {
		t.Errorf("esc: %q", u.search.Text())
	}
	// enter with no hosts listed does nothing
	u.search.SetText("zzqq")
	h.draw(w)
	h.key(key.NameReturn, 0)
	h.draw(w)
	if len(h.acts()) != 0 {
		t.Error("enter on no host")
	}
}

func TestGMSidebarClicks(t *testing.T) {
	h := newGM(t)
	u := h.u
	st := h.set(gmSideState())
	w := func(gtx layout.Context) { u.sidebar(gtx) }
	h.draw(w)
	// the folded org header unfolds on a click
	u.click("org0").Click()
	h.draw(w)
	if !u.closed["orgopen:Acme"] {
		t.Error("a click unfolds Acme")
	}
	u.click("org0chat").Click()
	h.draw(w)
	m := &model{tree: []node{{depth: 1}}}
	if h.runActs(m) != 1 || m.cursor != 0 {
		t.Error("✦ opens the org's session")
	}
	// a group folds
	u.click("grpAcme/web").Click()
	h.draw(w)
	if !u.closed["Acme/web"] {
		t.Error("the web group folds")
	}
	h.draw(w) // folded, with web3's alert on its dot
	if g, _ := gmRow(u.sideRows(), "group", "web"); !g.closed || g.color != cRed {
		t.Errorf("folded web %+v", g)
	}
	u.closed["Acme/web"] = false
	h.draw(w)
	// a host row opens its session; its tab when it has one
	u.click("host4").Click() // web3: none
	h.draw(w)
	m = &model{tree: make([]node, 10), tabs: tabsA(2)}
	for i := range m.tree {
		m.tree[i] = node{depth: 1}
	}
	h.runActs(m)
	if m.cursor != 4 {
		t.Errorf("web3 opened row %d", m.cursor)
	}
	u.click("host3").Click() // web2: in the org tab
	h.draw(w)
	h.runActs(m)
	if m.active() != 1 {
		t.Errorf("web2 activates tab %d", m.active())
	}
	// + on a host with its own tab adds a pane there, else opens it
	u.click("host2add").Click()
	h.draw(w)
	if fs := h.acts(); len(fs) != 2 {
		t.Errorf("+ on web1: %d actions", len(fs))
	}
	u.click("host4add").Click()
	h.draw(w)
	m.cursor = 0
	h.runActs(m)
	if m.cursor != 4 {
		t.Errorf("+ on web3 opened %d", m.cursor)
	}
	// the star pins
	u.click("host4pin").Click()
	h.draw(w)
	if !u.pins["Acme/web/web3"] {
		t.Error("the star pins web3")
	}
	if r, k := gmRow(u.sideRows(), "host", "web3"); k < 0 || !r.pin {
		t.Error("web3 is in the pinned section")
	}
	u.click("pinnedhost4").Click() // from the pinned section
	h.draw(w)
	m.cursor = 0
	h.runActs(m)
	if m.cursor != 4 {
		t.Errorf("pinned web3 opened %d", m.cursor)
	}
	// a folded org's flat host
	u.closed["orgopen:Acme"] = false
	h.draw(w)
	u.click("flathost2").Click()
	h.draw(w)
	h.runActs(m)
	if m.active() != 0 {
		t.Errorf("flat web1 activates %d", m.active())
	}
	// hovering: the org's ✦ and a host's buttons show
	u.closed["orgopen:Acme"] = true
	h.draw(w)
	h.hover(u.click("org0"), image.Rect(20, 30, 21, 300), 2, w)
	h.hover(u.click("org0chat"), image.Rect(150, 20, 240, 300), 2, w)
	h.hover(u.click("host4"), image.Rect(60, 30, 61, 400), 2, w)
	h.hover(u.click("host4pin"), image.Rect(150, 30, 240, 400), 2, w)
	h.hover(u.click("host4add"), image.Rect(150, 30, 240, 400), 2, w)
	// the account line follows the tab on screen
	u.org = "Acme"
	h.draw(w)
	st.Active = 5
	h.draw(w)
	// a search that matches nothing draws its empty row
	u.search.SetText("zzqq")
	h.draw(w)
	u.search.SetText("web")
	h.draw(func(gtx layout.Context) {
		gtx.Execute(key.FocusCmd{Tag: &u.search})
		u.sidebar(gtx)
	})
	h.draw(w)
}

func TestGMToolbar(t *testing.T) {
	h := newGM(t)
	u := h.u
	tabs := gmTabs("Acme", 10)
	tabs[0].OrgOnly, tabs[0].State = true, "approval"
	tabs[1].Broadcast = true
	st := h.set(&guiState{Tabs: tabs, Active: 1})
	u.org = "Acme"
	w := func(gtx layout.Context) { u.toolbar(gtx) }
	h.draw(w)
	if ts := u.orgTabs(); len(ts) != 10 || ts[9] != 9 {
		t.Errorf("orgTabs %v", ts)
	}
	m := &model{tabs: tabsA(10)}
	u.click("tab3").Click()
	h.draw(w)
	h.runActs(m)
	if m.active() != 3 || !u.wantTerm {
		t.Errorf("tab3 activates %d", m.active())
	}
	ks := make([]*Tab, 10)
	for i := range ks {
		ks[i] = &Tab{name: fmt.Sprint(i)}
	}
	m.tabs = ks
	u.click("tabx2").Click()
	h.draw(w)
	h.runActs(m)
	if len(m.tabs) != 9 || m.tabs[2].name != "3" {
		t.Errorf("tabx2 closes it: %d tabs", len(m.tabs))
	}
	for _, id := range []string{"tbPane", "tbBroadcast"} {
		u.click(id).Click()
		h.draw(w)
		if len(h.acts()) != 1 {
			t.Errorf("%s acts", id)
		}
	}
	u.click("tbFiles").Click() // no pane: a toast
	h.draw(w)
	if !u.toastBad {
		t.Error("tbFiles without a pane")
	}
	gm := &model{}
	u.click("tbGroup").Click()
	h.draw(w)
	h.runActs(gm)
	if gm.group != groupKey("/d", "web") {
		t.Errorf("tbGroup %q", gm.group)
	}
	st.Group = "x"
	u.click("tbGroup").Click()
	h.draw(w)
	h.runActs(gm)
	if gm.group != "" {
		t.Errorf("tbGroup off %q", gm.group)
	}
	st.Group, st.Active = "", 0 // an org tab has no group view
	u.click("tbGroup").Click()
	h.draw(w)
	if len(h.acts()) != 0 {
		t.Error("tbGroup on an org tab")
	}
	c := u.claude
	u.click("tbClaude").Click()
	h.draw(w)
	if u.claude == c || len(h.acts()) != 1 {
		t.Error("tbClaude toggles and saves")
	}
	u.click("tbPal").Click()
	h.draw(w)
	if !u.pal {
		t.Error("tbPal opens the palette")
	}
}

// --- gui_chat.go ---

const gmMarkdown = "# Title\n\n" +
	"Some *em* and **strong** with `code`, [a link](https://example.com) and <https://example.com/x>.\nline two\n\n" +
	"1. one\n2. two\n   - nested\n\n" +
	"- bullet\n\n" +
	"> quote text\n\n" +
	"> [!WARNING]\n> careful **now**\n\n" +
	"```go\nfmt.Println(1)\n```\n\n" +
	"    indented code\n\n" +
	"```mermaid\npie title Pets\n\"Dogs\" : 3\n\"Cats\" : 2\n```\n\n" +
	"```mermaid\ngraph LR\nA --> B\n```\n\n" +
	"```mermaid\nnot a diagram at all\n```\n\n" +
	"```figure\n+--+\n|ok|\n+--+\n```\n\n" +
	"```math\nx^2 \\\\ y\n```\n\n" +
	"$$a+b$$\n\n" +
	"---\n\n" +
	"| a | b |\n|:--|--:|\n| 1 | 2 |\n\n" +
	"<div>raw html: skipped</div>\n"

func TestGMParseMarkdown(t *testing.T) {
	bs := parseMarkdown(gmMarkdown)
	var kinds []string
	for _, b := range bs {
		kinds = append(kinds, b.kind)
	}
	// a nested list's item text shows once, not again after its "2." line
	want := "h p li li li li quote alert code code chart diagram code figure math math hr table"
	if got := strings.Join(kinds, " "); got != want {
		t.Fatalf("kinds\n got %s\nwant %s", got, want)
	}
	if plain(bs[3].spans) != "two" || bs[4].level != 1 {
		t.Errorf("the nested item %q, then level %d", plain(bs[3].spans), bs[4].level)
	}
	if bs[0].level != 1 || plain(bs[0].spans) != "Title" {
		t.Errorf("heading %+v", bs[0])
	}
	p := bs[1].spans
	var em, strong, code, link, auto bool
	for _, s := range p {
		em = em || s.em && s.text == "em"
		strong = strong || s.strong && s.text == "strong"
		code = code || s.code && s.text == "code"
		link = link || s.link && s.text == "a link"
		auto = auto || s.link && s.text == "https://example.com/x"
	}
	if !em || !strong || !code || !link || !auto || !strings.Contains(plain(p), "x>. line two") && !strings.Contains(plain(p), ". line two") {
		t.Errorf("inline spans %+v", p)
	}
	if bs[2].mark != "1." || bs[3].mark != "2." || bs[4].level != 1 || bs[4].mark != "•" || bs[5].level != 0 {
		t.Errorf("lists %+v %+v %+v %+v", bs[2], bs[3], bs[4], bs[5])
	}
	if bs[7].mark != "WARNING" || plain(bs[7].spans) != "careful now" {
		t.Errorf("alert %+v %q", bs[7], plain(bs[7].spans))
	}
	if bs[8].lang != "go" || bs[8].code != "fmt.Println(1)" || bs[9].lang != "" || bs[9].code != "indented code" {
		t.Errorf("code %+v %+v", bs[8], bs[9])
	}
	if bs[10].chart == nil || bs[11].code == "" || bs[12].lang != "mermaid" {
		t.Errorf("mermaid %+v %+v %+v", bs[10], bs[11], bs[12])
	}
	if bs[14].code != texToUnicode("x^2")+"\n"+texToUnicode("y") || bs[15].code != texToUnicode("a+b") {
		t.Errorf("math %q %q", bs[14].code, bs[15].code)
	}
	if len(bs[17].rows) != 2 || len(bs[17].align) != 2 || plain(bs[17].rows[1][1]) != "2" {
		t.Errorf("table %+v", bs[17])
	}
	if again := parseMarkdown(gmMarkdown); &again[0] != &parseMarkdown(gmMarkdown)[0] || again[0].kind != "h" {
		t.Error("parsed once")
	}
	if _, ok := displayMath("$$a$$ and $$b$$"); ok {
		t.Error("two formulas aren't one")
	}
	if _, ok := displayMath("$$$$"); ok {
		t.Error("an empty formula")
	}
	if k, rest, ok := alertSpans([]mdSpan{{text: "[!NOTE]"}, {text: " hi", strong: true}}); !ok || k != "NOTE" || len(rest) != 1 || rest[0].text != "hi" {
		t.Errorf("alert across spans %q %+v", k, rest)
	}
	if _, _, ok := alertSpans([]mdSpan{{text: "plain"}}); ok {
		t.Error("no alert")
	}
}

func TestGMSpans(t *testing.T) {
	h := newGM(t)
	u := h.u
	ss := u.spans([]mdSpan{{text: "c", code: true}, {text: "l", link: true}, {text: "b", strong: true}, {text: "i", em: true}, {text: "n"}}, 13, cText, sansFont)
	if ss[0].Font != monoFont || ss[0].Size != 12 || ss[0].Color != cAccText {
		t.Errorf("code %+v", ss[0])
	}
	if ss[1].Color != cAccText || ss[1].Font != sansFont {
		t.Errorf("link %+v", ss[1])
	}
	if ss[2].Font != fSemi(sansFont) || ss[3].Font.Style != 1 || ss[4].Color != cText || ss[4].Size != 13 {
		t.Errorf("strong/em/plain %+v %+v %+v", ss[2], ss[3], ss[4])
	}
	if e := u.spans(nil, 12, cText, sansFont); len(e) != 1 || e[0].Content != " " {
		t.Errorf("empty %+v", e)
	}
	a := u.highlight("x := 1", "go")
	b := u.highlight("x := 1", "go")
	if len(a) == 0 || &a[0] != &b[0] {
		t.Error("highlighted once")
	}
	if toks := u.tokens("whatever", "no-such-lexer"); len(toks) == 0 {
		t.Error("fallback lexer")
	}
}

// gmChatState is Acme's tab on web1 with a conversation of every kind.
func gmChatState(term *Term) *guiState {
	return &guiState{Tabs: []guiTab{{Key: &Tab{}, Name: "web1", Org: "Acme", Group: "web", Account: "work", Model: "opus",
		Suggest: "uptime", Ctx: 50000, CtxWindow: 200000, Pinned: map[int]bool{1: true, 2: true},
		Panes: []guiPane{{Term: term, Name: "web1", Focused: true}, {Term: term, Name: "db1"}},
		Chat: []guiChat{
			{Who: "you", Text: "check disk", Ctx: []int{1, 2}, Screen: true},
			{Who: "you", Text: "plain question"},
			{Who: "claude", Text: gmMarkdown + "\n```sh\nuptime\n```\n"},
			{Who: "tool", Text: "run df -h", Steps: []step{{"df -h", "disk usage"}}},
			{Who: "error", Text: "boom"},
			{Who: "peer", Text: "From DESK-1\nplease **check**"},
			{Who: "note", Text: "a note"},
		}}}}
}

func TestGMClaudePanel(t *testing.T) {
	h := newGM(t)
	u := h.u
	h.size = image.Pt(500, 4000)
	u.chatList.ScrollToEnd = false // the long reply stays laid out
	term, _ := gmTerm(t, "web1", 40, 6)
	w := func(gtx layout.Context) { u.claudePanel(gtx) }
	h.set(&guiState{})
	h.draw(w) // no tab: the explanation
	st := h.set(gmChatState(term))
	u.org = "Acme"
	h.draw(w)
	for _, id := range []string{"e0-0", "e0-1", "e0-3", "e0-4", "e0-6", "c0-2-0", "p0-5-0"} {
		if u.sels[id] == nil {
			t.Errorf("entry %s not drawn", id)
		}
	}
	// the code block's Copy and, for the suggestion, Type at prompt
	goBlock := -1
	for i, b := range parseMarkdown(gmMarkdown) {
		if b.lang == "go" {
			goBlock = i
		}
	}
	u.click(fmt.Sprintf("c0-2-%dcopy", goBlock)).Click()
	h.draw(w)
	if len(u.clipOut) != 1 || gmClip(u) != "fmt.Println(1)" || u.toast != "Copied" {
		t.Errorf("Copy copies the block %d: %d queued, toast %q, %v", goBlock, len(u.clipOut), u.toast, u.clicks[fmt.Sprintf("c0-2-%dcopy", goBlock)] != nil)
	}
	if u.clicks[fmt.Sprintf("c0-2-%dtype", goBlock)] != nil {
		t.Error("only the suggestion offers to type")
	}
	n := len(parseMarkdown(gmMarkdown + "\n```sh\nuptime\n```\n"))
	tid := fmt.Sprintf("c0-2-%dtype", n-1)
	if u.clicks[tid] == nil {
		t.Fatalf("no %s", tid)
	}
	u.wantTerm = false
	u.click(tid).Click()
	h.draw(w)
	if len(h.acts()) != 1 || !u.wantTerm {
		t.Error("Type at prompt presses ctrl+y")
	}
	// busy and compacting, an org tab, no model
	st.Tabs[0].Busy, st.Tabs[0].Compacting = true, true
	h.draw(w)
	st.Tabs[0].Compacting = false
	h.draw(w)
	st.Tabs[0].OrgOnly, st.Tabs[0].Model = true, ""
	h.draw(w)
	// every theme's alert colors
	for _, k := range []string{"NOTE", "TIP", "IMPORTANT", "WARNING", "CAUTION"} {
		if alertLooks[k].c() == (color.NRGBA{}) {
			t.Errorf("alert %s", k)
		}
	}
}

func TestGMCtxBar(t *testing.T) {
	h := newGM(t)
	u := h.u
	tb := &guiTab{}
	w := func(gtx layout.Context) { u.ctxBar(gtx, tb) }
	h.draw(w)
	tb.Ctx = 1200
	h.draw(w)
	tb.CtxWindow, tb.Busy = 1000, true // past the window: the bar stops at full
	h.draw(w)
	u.click("ctxCompact").Click()
	h.draw(w)
	pm, _ := gmProbe(t)
	h.runActs(pm)
	if !strings.Contains(pm.flash, "compact") {
		t.Errorf("Compact: %q", pm.flash)
	}
	u.click("ctxClear").Click()
	h.draw(w)
	h.runActs(pm)
	if !strings.Contains(pm.flash, "clear") {
		t.Errorf("Clear: %q", pm.flash)
	}
}

func TestGMResizeGrip(t *testing.T) {
	h := newGM(t)
	u := h.u
	h.set(&guiState{})
	w := func(gtx layout.Context) { u.claudePanel(gtx) }
	h.draw(w)
	h.press(2, 100, 0)
	h.drag(-48, 100)
	h.draw(w)
	if u.claudeW != 450 || !u.grip.dragging {
		t.Errorf("dragged 50 left: %v", u.claudeW)
	}
	h.drag(-2000, 100)
	h.draw(w)
	if u.claudeW != 800 {
		t.Errorf("clamped to 800: %v", u.claudeW)
	}
	h.release(-2000, 100)
	h.draw(w)
	m := &model{}
	h.runActs(m)
	if u.grip.dragging || m.guiLayout.ClaudeW != 800 {
		t.Errorf("release saves: %+v", m.guiLayout)
	}
	h.press(2, 100, 0)
	h.drag(3000, 100)
	h.draw(w)
	if u.claudeW != 300 {
		t.Errorf("clamped to 300: %v", u.claudeW)
	}
	h.release(3000, 100)
	h.draw(w)
	if u.grip.dragging {
		t.Error("release ends the drag")
	}
	// a drag that never pressed, a release without a drag
	h.drag(1, 1)
	h.draw(w)
	if u.claudeW != 300 || len(h.acts()) != 1 {
		t.Errorf("stray drag: %v", u.claudeW)
	}
}

// TestGMResizeGripRightAligned: the panel sits at the window's right edge, so
// its frame moves as it widens; several drags in one frame still follow the
// pointer, and that frame draws the new width.
func TestGMResizeGripRightAligned(t *testing.T) {
	h := newGM(t)
	u := h.u
	h.set(&guiState{})
	w := func(gtx layout.Context) { // as gui.go: panes, then the panel at the right
		layout.Flex{}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
			layout.Rigid(u.claudePanel))
	}
	h.draw(w)
	h.press(802, 100, 0)
	h.drag(752, 100)
	h.drag(702, 100)
	h.draw(w)
	if u.claudeW != 500 || u.grip.drawnW != 500 {
		t.Errorf("dragged 100 left: width %v, drawn %v", u.claudeW, u.grip.drawnW)
	}
	h.drag(652, 100)
	h.draw(w)
	if u.claudeW != 550 {
		t.Errorf("next frame: %v", u.claudeW)
	}
}

func TestGMComposer(t *testing.T) {
	h := newGM(t)
	u := h.u
	term, _ := gmTerm(t, "web1", 40, 6)
	st := h.set(gmChatState(term))
	u.org = "Acme"
	tb := &st.Tabs[0]
	w := func(gtx layout.Context) { u.composer(gtx, u.tab()) }
	h.draw(w)
	u.toggleComposer = true // focus the box
	h.draw(w)
	h.draw(w)
	h.r.Queue(key.EditEvent{Text: "  uptime  "})
	h.key(key.NameReturn, 0)
	h.draw(w)
	pm, pt := gmProbe(t)
	h.runActs(pm)
	if len(pt.asked) != 1 || pt.asked[0] != "uptime" || u.input.Text() != "" {
		t.Errorf("enter asks %v, box %q", pt.asked, u.input.Text())
	}
	h.key(key.NameReturn, 0) // empty: nothing
	h.draw(w)
	if len(h.acts()) != 0 {
		t.Error("an empty question")
	}
	// Alt+Enter is a new line; Shift+Enter neither sends nor breaks the line
	h.key(key.NameReturn, key.ModShift)
	h.draw(w)
	h.key(key.NameReturn, key.ModAlt)
	h.draw(w)
	if u.input.Text() != "\n" || len(h.acts()) != 0 {
		t.Errorf("new line: box %q", u.input.Text())
	}
	u.input.SetText("")
	u.wantTerm = false
	u.toggleComposer = true // focused: back to the terminal
	h.draw(w)
	if !u.wantTerm || u.toggleComposer {
		t.Error("toggle from the box gives the terminal the keys")
	}
	u.input.SetText("df -h")
	u.click("send").Click()
	h.draw(w)
	h.runActs(pm)
	if len(pt.asked) != 2 || pt.asked[1] != "df -h" || u.input.Text() != "" {
		t.Errorf("send asks %v", pt.asked)
	}
	u.click("send").Click() // nothing typed
	h.draw(w)
	if len(h.acts()) != 0 {
		t.Error("send with nothing typed")
	}
	tb.Busy = true
	h.draw(w)
	c, rec := claudeA(t)
	pt.claude = c
	u.click("stop").Click()
	h.draw(w)
	h.runActs(pm)
	if rec.String() == "" {
		t.Error("stop interrupts")
	}
	// the chips: pinned blocks unpin, the screen toggle
	u.click("chip1").Click()
	h.draw(w)
	pt.pinned[1] = true
	h.runActs(pm)
	if pt.pinned[1] {
		t.Error("a chip unpins its block")
	}
	pt.busy = false
	u.click("chipScreen").Click()
	h.draw(w)
	h.runActs(pm)
	if !pt.pinScreen {
		t.Error("the screen chip presses ctrl+s")
	}
	tb.PinScreen = true
	h.draw(w)
	tb.PinScreen, tb.Live = false, true
	h.draw(w)
	tb.Panes[0].Focused = false // no focused pane: no chips
	h.draw(w)
	// without a tab: nothing is asked
	u.org = "Globex"
	u.input.SetText("hello")
	u.click("send").Click()
	h.draw(w)
	h.key(key.NameReturn, 0)
	h.draw(w)
	if len(h.acts()) != 0 {
		t.Error("no tab, no question")
	}
}

// --- gui_term.go ---

func TestGMTermHelpers(t *testing.T) {
	if cellColor(nil, cRed) != cRed || cellColor(ansi.BasicColor(1), cText) != ansi16[1] || cellColor(ansi.IndexedColor(9), cText) != ansi16[9] {
		t.Error("basic colors")
	}
	if c := cellColor(ansi.IndexedColor(196), cText); c == cText || c.A != 0xff {
		t.Errorf("indexed 196 %v", c)
	}
	if c := cellColor(color.NRGBA{1, 2, 3, 0xff}, cText); c != (color.NRGBA{1, 2, 3, 0xff}) {
		t.Errorf("rgb %v", c)
	}
	a, b := image.Pt(5, 1), image.Pt(2, 1)
	if lo, hi := order(a, b); lo != b || hi != a {
		t.Error("order on a row")
	}
	if lo, hi := order(image.Pt(0, 3), image.Pt(9, 2)); lo.Y != 2 || hi.Y != 3 {
		t.Error("order across rows")
	}
	if lo, _ := order(b, a); lo != b {
		t.Error("in order")
	}
	if dur(250*time.Millisecond) != "250ms" || dur(1500*time.Millisecond) != "1.5s" || dur(125*time.Second) != "2m 5s" {
		t.Errorf("dur %s %s %s", dur(250*time.Millisecond), dur(1500*time.Millisecond), dur(125*time.Second))
	}
	press := func(n key.Name, m key.Modifiers) key.Event { return key.Event{Name: n, Modifiers: m, State: key.Press} }
	cases := []struct {
		e    key.Event
		want uv.KeyPressEvent
		ok   bool
	}{
		{key.Event{Name: key.NameReturn, State: key.Release}, uv.KeyPressEvent{}, false},
		{press(key.NameReturn, key.ModShift), uv.KeyPressEvent{Code: uv.KeyEnter}, true},
		{press(key.NameTab, key.ModShift), uv.KeyPressEvent{Code: uv.KeyTab, Mod: uv.ModShift}, true},
		{press(key.NameSpace, 0), uv.KeyPressEvent{}, false},
		{press(key.NameSpace, key.ModCtrl), uv.KeyPressEvent{Code: uv.KeySpace, Mod: uv.ModCtrl}, true},
		{press("A", 0), uv.KeyPressEvent{}, false},
		{press("C", key.ModCtrl|key.ModShift), uv.KeyPressEvent{Code: 'c', Mod: uv.ModCtrl}, true},
		{press("B", key.ModAlt), uv.KeyPressEvent{Code: 'b', Mod: uv.ModAlt, Text: "b"}, true},
		{press("Fx", key.ModCtrl), uv.KeyPressEvent{}, false},
		{press(key.NameF5, 0), uv.KeyPressEvent{Code: uv.KeyF5}, true},
	}
	for i, c := range cases {
		if got, ok := termKey(c.e); ok != c.ok || got != c.want {
			t.Errorf("case %d: %+v %v, want %+v %v", i, got, ok, c.want, c.ok)
		}
	}
	t1, t2 := &Term{}, &Term{}
	tb := &guiTab{Panes: []guiPane{{Term: t1}, {Term: t2}}}
	if ts := typingTo(tb, tb.Panes[1]); len(ts) != 1 || ts[0] != t2 {
		t.Error("one pane")
	}
	tb.Broadcast = true
	if ts := typingTo(tb, tb.Panes[1]); len(ts) != 2 || ts[0] != t2 || ts[1] != t1 {
		t.Error("broadcast: the pane first, then the others")
	}
	v := &termView{runs: [][]Run{{{X: 2, Text: "ab"}, {X: 6, Text: "cd  "}}, {{X: 0, Text: "next line"}}}}
	if string(v.line(0)) != "  ab  cd" || v.line(-1) != nil || v.line(5) != nil {
		t.Errorf("line %q", string(v.line(0)))
	}
	if v.selected() != "" {
		t.Error("no selection")
	}
	v.sel = [2]image.Point{{4, 1}, {4, 0}} // backwards, across rows
	if s := v.selected(); s != "  cd\nnext" {
		t.Errorf("selected %q", s)
	}
	v.sel = [2]image.Point{{50, 0}, {60, 0}} // past the text
	if s := v.selected(); s != "" {
		t.Errorf("past the end %q", s)
	}
}

func TestGMTextDrawing(t *testing.T) {
	h := newGM(t)
	u := h.u
	var gl []text.Glyph
	size := fixed.I(13)
	cw, ch, asc := cellMetrics(u.sh, monoFont, size, &gl)
	if cw <= 0 || ch <= 0 || asc <= 0 {
		t.Fatalf("metrics %d %d %d", cw, ch, asc)
	}
	if w := textWidth(u.sh, monoFont, size, "MM", &gl); w < cw*2-1 || w > cw*2+1 {
		t.Errorf("width %d, cell %d", w, cw)
	}
	if textWidth(u.sh, monoFont, size, "", &gl) != 0 {
		t.Error("empty width")
	}
	h.draw(func(gtx layout.Context) {
		drawRun(gtx.Ops, u.sh, monoFont, size, strings.Repeat("x", 300), 0, 20, cw, cText, &gl) // past 128 runes: in pieces
		drawText(gtx.Ops, u.sh, monoFont, size, "#12", 0, 20, cText, &gl)
		paintGlyphs(gtx.Ops, u.sh, 0, 0, cText, nil)
	})
}

// gmTermState is Acme's tab with two panes: web1 (focused) and db1.
func gmTermState(t *testing.T) (*guiState, [2]*Term, [2]*gmSink) {
	var ts [2]*Term
	var ss [2]*gmSink
	for i, host := range []string{"web1", "db1"} {
		ts[i], ss[i] = gmTerm(t, host, 80, 24)
		ts[i].b = &pos{} // at a prompt
	}
	st := &guiState{Tabs: []guiTab{{Key: &Tab{}, Name: "web1", Org: "Acme", Group: "web", Pinned: map[int]bool{},
		Panes: []guiPane{
			{Term: ts[0], N: 1, Name: "web1", Addr: "10.0.0.1", Focused: true, Blocks: map[int]Block{}},
			{Term: ts[1], N: 2, Name: "db1", Claude: true, Blocks: map[int]Block{}},
		}}}}
	return st, ts, ss
}

// gmSyncBlocks copies the terms' blocks into the snapshot, as snapshot does.
func gmSyncBlocks(st *guiState) {
	for i := range st.Tabs[0].Panes {
		p := &st.Tabs[0].Panes[i]
		p.Blocks = map[int]Block{}
		for _, b := range p.Term.Blocks() {
			p.Blocks[b.N] = b
		}
	}
}

func TestGMTerminalsPlaceholders(t *testing.T) {
	h := newGM(t)
	u := h.u
	w := func(gtx layout.Context) { u.terminals(gtx) }
	h.set(&guiState{})
	h.draw(w) // no tab
	tabs := gmTabs("Acme", 1)
	st := h.set(&guiState{Tabs: tabs})
	u.org = "Acme"
	h.draw(w) // a tab with no pane
	// the directory split alone; views of closed tabs and terminals go
	gone := &Tab{}
	gmFiles(u, tabs[0].Key)
	gmFiles(u, gone)
	stale := &Term{}
	u.terms[stale] = &termView{}
	h.draw(w)
	if u.files[gone] != nil || u.terms[stale] != nil || u.files[tabs[0].Key] == nil {
		t.Error("forgets what is gone, keeps the tab's split")
	}
	// panes and the split
	term, _ := gmTerm(t, "web1", 80, 24)
	st.Tabs[0].Panes = []guiPane{{Term: term, Name: "web1", Focused: true}}
	h.draw(w)
	if u.terms[term] == nil {
		t.Error("the pane's view")
	}
	u.selectBlock(true) // no blocks: nothing
	if u.terms[term].blockSel != 0 {
		t.Error("no blocks to pick")
	}
	u.org = "Globex"
	u.selectBlock(true)
	u.openFind()
	if u.terms[term].finding {
		t.Error("another org's pane")
	}
}

func TestGMTermTyping(t *testing.T) {
	h := newGM(t)
	u := h.u
	h.size = image.Pt(1000, 700)
	st, ts, ss := gmTermState(t)
	h.set(st)
	u.org = "Acme"
	w := func(gtx layout.Context) { u.terminals(gtx) }
	h.draw(w)
	h.draw(w)
	v := u.terms[ts[0]]
	if v == nil || v.cols < 20 || v.rows < 3 || u.wantTerm {
		t.Fatalf("view %+v wantTerm %v", v, u.wantTerm)
	}
	if c, r := ts[0].Size(); c != v.cols || r != v.rows {
		t.Errorf("resized to %dx%d, view %dx%d", c, r, v.cols, v.rows)
	}
	cols := v.cols
	h.draw(w) // the same size: no resize
	if v.cols != cols {
		t.Error("size changed")
	}
	v.scroll, v.sel = 3, [2]image.Point{{0, 0}, {2, 0}}
	h.r.Queue(key.EditEvent{Text: "ls"})
	h.key(key.NameReturn, 0)
	h.draw(w)
	ss[0].wait(t, "ls\r")
	if v.scroll != 0 || v.sel != [2]image.Point{} || ss[1].String() != "" {
		t.Errorf("typing resets scroll %d sel %v; db1 got %q", v.scroll, v.sel, ss[1].String())
	}
	// broadcast: both panes
	st.Tabs[0].Broadcast = true
	h.r.Queue(key.EditEvent{Text: "w"})
	h.key(key.NameDeleteBackward, 0)
	h.draw(w)
	ss[1].wait(t, "w\x7f")
	st.Tabs[0].Broadcast = false
	// keys that aren't the terminal's: a plain letter as a key event
	ss[0].reset()
	h.key("A", 0)
	h.draw(w)
	// a workspace shortcut pressed in the terminal
	h.key("K", keys.Mod)
	h.draw(w)
	if !u.pal {
		t.Error("the terminal hands shortcuts over")
	}
	u.pal = false
	// copy the selection, paste from the clipboard
	v.sel = [2]image.Point{{0, 0}, {3, 0}}
	v.runs = [][]Run{{{X: 0, Text: "abcdef"}}}
	u.copySelection = true
	h.draw(w)
	v.sel = [2]image.Point{{0, 0}, {3, 0}}
	u.copySelection, u.toast = true, ""
	h.draw(func(gtx layout.Context) {
		u.terminals(gtx)
	})
	u.pasteWanted = true
	h.draw(w)
	if u.pasteWanted {
		t.Error("paste asked")
	}
	h.r.Queue(transfer.DataEvent{Type: "application/text", Open: func() io.ReadCloser { return io.NopCloser(strings.NewReader("pasted!")) }})
	h.draw(w)
	ss[0].wait(t, "pasted!")
	if ss[0].String() == "A" {
		t.Error("a plain letter key isn't typed (its EditEvent is)")
	}
	// an empty selection copies nothing
	v.sel = [2]image.Point{}
	u.copySelection, u.toast = true, ""
	h.draw(w)
	if u.copySelection || u.toast != "" {
		t.Error("nothing to copy")
	}
}

func TestGMTermCopy(t *testing.T) {
	h := newGM(t)
	u := h.u
	h.size = image.Pt(1000, 700)
	st, ts, _ := gmTermState(t)
	h.set(st)
	u.org = "Acme"
	gmOut(ts[0], "hello world\r\n")
	w := func(gtx layout.Context) { u.terminals(gtx) }
	h.draw(w)
	h.draw(w)
	v := u.terms[ts[0]]
	v.sel = [2]image.Point{{0, 0}, {5, 0}}
	u.copySelection = true
	h.draw(w)
	if mime, b, ok := h.r.WriteClipboard(); !ok || mime != "application/text" || string(b) != "hello" || u.toast != "Copied" {
		t.Errorf("copied %q %q %v toast %q", mime, b, ok, u.toast)
	}
}

func TestGMTermMouse(t *testing.T) {
	h := newGM(t)
	u := h.u
	h.size = image.Pt(1000, 700)
	st, ts, ss := gmTermState(t)
	h.set(st)
	u.org = "Acme"
	for i := range 60 {
		gmOut(ts[0], fmt.Sprintf("line %d some/path:80 word\r\n", i))
	}
	gmOut(ts[0], "$ ")
	w := func(gtx layout.Context) { u.terminals(gtx) }
	h.draw(w)
	h.draw(w)
	v := u.terms[ts[0]]
	if len(v.rowY) == 0 {
		t.Fatal("no rows drawn")
	}
	// pane 0 fills the top half: a row in its grid
	y := float32(v.rowY[3] + 4)
	x0 := float32(60)
	h.press(x0, y, 0)
	h.drag(x0+80, y+float32(v.rowY[4]-v.rowY[3]))
	h.release(x0+80, y+float32(v.rowY[4]-v.rowY[3]))
	h.draw(w)
	if !strings.Contains(gmClip(u), "\n") {
		t.Error("a drag over two rows copies both")
	}
	// double: a word; triple: the line
	h.press(x0+40, y, 0)
	h.release(x0+40, y)
	h.press(x0+40, y, 0)
	h.release(x0+40, y)
	h.draw(w)
	word := gmClip(u)
	h.press(x0+40, y, 0)
	h.release(x0+40, y)
	h.draw(w)
	line := gmClip(u)
	if word == "" || strings.Contains(word, " ") || !strings.HasPrefix(line, "line ") || len(line) <= len(word) {
		t.Errorf("word %q line %q", word, line)
	}
	// a press in the other pane focuses it
	h.acts()
	v2 := u.terms[ts[1]]
	y2 := float32(360 + v2.rowY[0] + 4) // db1's pane starts halfway down; rowY is the pane's own
	h.press(x0, y2, 0)
	h.release(x0, y2)
	h.draw(w)
	m := &model{tabs: []*Tab{{panes: []*Pane{{term: ts[0]}, {term: ts[1]}}}}}
	h.runActs(m)
	if m.tabs[0].focused() != m.tabs[0].panes[1] {
		t.Error("the press focused db1")
	}
	h.draw(w) // the copy of that click (nothing selected)
	// the wheel scrolls back, and forward to the live screen
	h.r.Queue(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: f32.Pt(x0, y), Scroll: f32.Pt(0, -100)})
	h.draw(w)
	if v.scroll <= 0 {
		t.Errorf("scrolled back %d", v.scroll)
	}
	back := v.scroll
	h.r.Queue(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: f32.Pt(x0, y), Scroll: f32.Pt(0, 1)})
	h.draw(w)
	if v.scroll != back-1 {
		t.Errorf("a small wheel step is one line: %d → %d", back, v.scroll)
	}
	h.r.Queue(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: f32.Pt(x0, y), Scroll: f32.Pt(0, 1e4)})
	h.draw(w)
	if v.scroll != 0 {
		t.Errorf("forward to live %d", v.scroll)
	}
	// a full-screen program takes the wheel
	gmOut(ts[0], "\x1b[?1049h\x1b[?1000h")
	ss[0].reset()
	h.r.Queue(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: f32.Pt(x0, y), Scroll: f32.Pt(0, -100)})
	h.draw(w)
	if v.scroll != 0 {
		t.Error("the program scrolls, not the view")
	}
	ss[0].wait(t, "\x1b[M")
}

func TestGMTermBlocks(t *testing.T) {
	h := newGM(t)
	u := h.u
	h.size = image.Pt(1600, 900)
	st, ts, ss := gmTermState(t)
	h.set(st)
	u.org = "Acme"
	term := ts[0]
	w := func(gtx layout.Context) { u.terminals(gtx) }
	h.draw(w) // sized first, as in the window
	v := u.terms[term]
	cols := v.cols
	gmOut(term, gmBlock("ls", "a b\r\n", "0"))
	term.mu.Lock()
	term.byClaude = true
	term.mu.Unlock()
	gmOut(term, gmBlock("false", "", "1"))
	// rows filled to the edge: the badges get a line of their own
	gmOut(term, gmBlock(strings.Repeat("x", cols-2), strings.Repeat("y", 2*cols)+"\r\n", "0"))
	gmOut(term, "\x1b[7mrev\x1b[0m \x1b[2mfaint\x1b[0m \x1b[1mbold\x1b[0m \x1b[41mbg\x1b[0m \x1b[31mred\x1b[0m\r\n")
	gmOut(term, "abc: \x1b]133;B\x07sleep 9\r\n\x1b]133;C\x07") // running, after a prompt with no sign
	gmSyncBlocks(st)
	tb := &st.Tabs[0]
	tb.Pinned[1] = true
	h.draw(w)
	h.draw(w)
	if len(tb.Panes[0].Blocks) != 3 || !tb.Panes[0].Blocks[2].ByClaude || tb.Panes[0].Blocks[2].Exit != 1 {
		t.Fatalf("blocks %+v", tb.Panes[0].Blocks)
	}
	// the gutter number pins its block; in another pane it focuses that first
	u.click(fmt.Sprintf("pin%p-%d", term, 2)).Click()
	h.draw(w)
	pm, pt := gmProbe(t)
	h.runActs(pm)
	if !pt.pinned[2] {
		t.Error("#2 pinned")
	}
	// shift-click copies its output
	c := u.click(fmt.Sprintf("pin%p-%d", term, 1))
	at := h.hover(c, image.Rect(4, 30, 60, 450), 3, w)
	h.press(float32(at.X), float32(at.Y), key.ModShift)
	h.r.Queue(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: f32.Pt(float32(at.X), float32(at.Y)), Modifiers: key.ModShift})
	h.draw(w)
	if got := gmClip(u); got != "a b" || !strings.Contains(u.toast, "#1") {
		t.Errorf("shift-click copied %q, toast %q", got, u.toast)
	}
	// keyboard block picking (the workspace's block keys): newest, older,
	// newer, past the newest ends
	blk := func(n key.Name) { u.shortcut(key.Event{Name: n, Modifiers: keys.Mod, State: key.Press}) }
	blk(key.NameUpArrow)
	if v.blockSel != 3 || !u.wantTerm {
		t.Errorf("up picks the newest: %d", v.blockSel)
	}
	h.draw(w)
	u.selectBlock(true)
	u.selectBlock(true)
	u.selectBlock(true)
	if v.blockSel != 1 {
		t.Errorf("up stops at #1: %d", v.blockSel)
	}
	blk(key.NameDownArrow)
	if v.blockSel != 2 {
		t.Errorf("down: %d", v.blockSel)
	}
	h.draw(w)
	u.selectBlock(false)
	u.selectBlock(false)
	if v.blockSel != 0 || v.scroll != 0 {
		t.Errorf("past the newest ends: %d", v.blockSel)
	}
	u.selectBlock(false) // none picked: down does nothing
	if v.blockSel != 0 {
		t.Error("down with none picked")
	}
	// keys while a block is picked
	gmBlockKeys := func(f func()) { v.blockSel = 2; f(); h.draw(w) }
	gmBlockKeys(func() { h.r.Queue(key.EditEvent{Text: " "}) })
	h.runActs(pm)
	if pt.pinned[2] {
		t.Error("space toggles the pin")
	}
	gmBlockKeys(func() { h.r.Queue(key.EditEvent{Text: "y"}) })
	if got := gmClip(u); got != "" { // false printed nothing
		t.Errorf("y copied %q", got)
	}
	v.blockSel = 1
	h.r.Queue(key.EditEvent{Text: "Y"})
	h.draw(w)
	if got := gmClip(u); got != "a b" || v.blockSel != 1 {
		t.Errorf("Y copied %q", got)
	}
	gmBlockKeys(func() { h.key(key.NameUpArrow, 0) })
	if v.blockSel != 1 {
		t.Errorf("↑ in block mode: %d", v.blockSel)
	}
	gmBlockKeys(func() { h.key(key.NameDownArrow, 0) })
	if v.blockSel != 3 {
		t.Errorf("↓ in block mode: %d", v.blockSel)
	}
	gmBlockKeys(func() { h.key(key.NameEscape, 0) })
	if v.blockSel != 0 {
		t.Error("esc leaves")
	}
	ss[0].reset()
	gmBlockKeys(func() { h.r.Queue(key.EditEvent{Text: "q"}) })
	if v.blockSel != 0 {
		t.Error("another key leaves block mode")
	}
	ss[0].wait(t, "q")
	if !u.blockKey(guiPane{}, &termView{blockSel: 9}, "", "y") { // a block that is gone: taken, nothing copied
		t.Error("y on a gone block")
	}
	if len(u.clipOut) != 0 {
		t.Error("nothing to copy")
	}
	// the header's block label, a non-focused pane's pin, Claude's pill
	v.blockSel = 2
	h.draw(w)
	v.blockSel = 0
	u.click(fmt.Sprintf("pin%p-%d", term, 1)).Click()
	tb.Panes[0].Focused, tb.Panes[1].Focused = false, true
	h.draw(w)
	m := &model{tabs: []*Tab{{pinned: map[int]bool{}, panes: []*Pane{{term: ts[0]}, {term: ts[1]}}}}}
	h.runActs(m)
	if m.tabs[0].focus != m.tabs[0].panes[0] || !m.tabs[0].pinned[1] {
		t.Errorf("pinning in another pane focuses it first: pinned %v", m.tabs[0].pinned)
	}
	// a block with no decoration known yet (the snapshot lags the term)
	delete(tb.Panes[0].Blocks, 3)
	h.draw(w)
}

func TestGMTermHeader(t *testing.T) {
	h := newGM(t)
	u := h.u
	h.size = image.Pt(1000, 700)
	st, ts, _ := gmTermState(t)
	h.set(st)
	u.org = "Acme"
	tb := &st.Tabs[0]
	w := func(gtx layout.Context) { u.terminals(gtx) }
	h.draw(w)
	pm, pt := gmProbe(t)
	pt.busy = false
	u.click(fmt.Sprintf("scr%p", ts[0])).Click()
	h.draw(w)
	h.runActs(pm)
	if !pt.pinScreen {
		t.Error("the screen button presses ctrl+s")
	}
	tb.PinScreen = true
	h.draw(w)
	tb.PinScreen, tb.Live, tb.Broadcast = false, true, true
	h.draw(w)
	u.click(fmt.Sprintf("close%p", ts[1])).Click()
	h.draw(w)
	t1, _ := gmTerm(t, "web1", 40, 6)
	t2, _ := gmTerm(t, "web1", 40, 6)
	cm := &model{tabs: []*Tab{{pinned: map[int]bool{}, panes: []*Pane{{term: t1, waitUntil: time.Now().Add(time.Hour)}, {term: t2, waitUntil: time.Now().Add(time.Hour)}}}}}
	t1.b, t2.b = &pos{}, &pos{}
	cm.tabs[0].focusPane(0)
	h.runActs(cm)
	if len(cm.tabs[0].panes) != 1 || cm.tabs[0].panes[0].term != t1 {
		t.Error("× closes the second pane")
	}
}

func TestGMConnOverlay(t *testing.T) {
	h := newGM(t)
	u := h.u
	h.size = image.Pt(1000, 700)
	st, _, _ := gmTermState(t)
	h.set(st)
	u.org = "Acme"
	tb := &st.Tabs[0]
	w := func(gtx layout.Context) { u.terminals(gtx) }
	for _, c := range []struct {
		state  string
		canFix bool
		ids    []string
	}{
		{"connecting", false, []string{"connC"}},
		{"timeout", false, []string{"connW", "connC"}},
		{"reconnecting", false, []string{"connR", "connC"}},
		{"failed", false, []string{"connR", "connC"}}, // Fix only when ssh named old algorithms
		{"failed", true, []string{"connR", "connF", "connC"}},
	} {
		tb.Panes[0].Conn, tb.Panes[0].CanFix = c.state, c.canFix
		u.clicks = map[string]*widget.Clickable{}
		h.draw(w)
		for _, id := range []string{"connW", "connR", "connF", "connC"} {
			if (u.clicks[id] != nil) != slices.Contains(c.ids, id) {
				t.Errorf("%s: button %s shown %v", c.state, id, u.clicks[id] != nil)
			}
		}
		for _, id := range c.ids {
			u.click(id).Click()
			h.draw(w)
			if len(h.acts()) != 1 {
				t.Errorf("%s: %s presses a key", c.state, id)
			}
		}
	}
	// the close button closes the stuck pane
	tb.Panes[0].Conn = "timeout"
	h.draw(w)
	term, _ := gmTerm(t, "web1", 40, 6)
	cm := &model{tabs: []*Tab{{pinned: map[int]bool{}, orgOnly: true, panes: []*Pane{{term: term, waitUntil: time.Now().Add(-time.Second)}}}}}
	cm.tabs[0].focusPane(0)
	u.click("connC").Click()
	h.draw(w)
	h.runActs(cm)
	if len(cm.tabs[0].panes) != 0 {
		t.Error("Close presses c")
	}
}

func TestGMFindBar(t *testing.T) {
	h := newGM(t)
	u := h.u
	h.size = image.Pt(1000, 700)
	st, ts, _ := gmTermState(t)
	h.set(st)
	u.org = "Acme"
	w := func(gtx layout.Context) { u.terminals(gtx) }
	h.draw(w) // sized first, as in the window
	for i := range 40 {
		gmOut(ts[0], fmt.Sprintf("row %d\r\n", i))
	}
	gmOut(ts[0], "needle one\r\nhay\r\nneedle two\r\n$ ")
	u.openFind()
	v := u.terms[ts[0]]
	if !v.finding || !v.focusFind || u.terms[ts[1]] != nil && u.terms[ts[1]].finding {
		t.Fatal("find opens on the focused pane")
	}
	h.draw(w)
	h.draw(w)
	if v.focusFind {
		t.Error("the bar took the keyboard")
	}
	h.r.Queue(key.EditEvent{Text: "needle"})
	h.draw(w)
	if len(v.hits) != 2 || v.hit != 1 || v.findQ != "needle" {
		t.Fatalf("hits %v hit %d, query %q", v.hits, v.hit, v.find.Text())
	}
	h.draw(w) // the match highlighted
	h.key(key.NameReturn, 0)
	h.draw(w)
	if v.hit != 0 {
		t.Errorf("enter: the older match %d", v.hit)
	}
	h.key(key.NameReturn, 0)
	h.draw(w)
	if v.hit != 1 {
		t.Errorf("enter wraps %d", v.hit)
	}
	// scrolled far back: the hit row is off the grid
	v.scroll = ts[0].Scrollback()
	h.draw(w)
	h.r.Queue(key.EditEvent{Text: "zzz"})
	h.draw(w)
	if len(v.hits) != 0 {
		t.Errorf("no match %v", v.hits)
	}
	h.key(key.NameReturn, 0) // no hits: nothing
	h.draw(w)
	u.wantTerm = false
	h.key(key.NameEscape, 0)
	h.draw(w)
	if v.finding || v.hits != nil || v.findQ != "" || v.scroll != 0 || !u.wantTerm {
		t.Error("esc closes the bar")
	}
	u.openFind() // again: the old query selected
	h.draw(w)
	h.draw(w)
	v.find.SetText("")
	h.draw(w)
}

func TestGMSudoBar(t *testing.T) {
	h := newGM(t)
	u := h.u
	h.size = image.Pt(1000, 700)
	st, ts, _ := gmTermState(t)
	h.set(st)
	u.org = "Acme"
	tb := &st.Tabs[0]
	w := func(gtx layout.Context) { u.terminals(gtx) }
	gmOut(ts[0], "[sudo] password for deploy: ")
	h.draw(w)
	if u.clicks["sudoPw"] != nil {
		t.Error("no #@ password: no button")
	}
	tb.Panes[0].Password = true
	h.draw(w)
	u.wantTerm = false
	u.click("sudoPw").Click()
	h.draw(w)
	if len(h.acts()) != 1 || !u.wantTerm {
		t.Error("the button presses alt+p")
	}
	gmOut(ts[0], "\r\nPassword: ") // su: no user
	h.draw(w)
	tb.Panes[0].Focused = false // only the focused pane offers it
	h.draw(w)
}
