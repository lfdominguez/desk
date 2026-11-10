package infra

import (
	"fmt"
	"image"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"github.com/lfdominguez/desk/internal/keys"
)

// The window drives the workspace's Bubble Tea model: one goroutine owns it and runs
// Update on every message, so approvals, asks, peers and Claude keep their
// logic. It publishes a snapshot after each change; the window (gui_*.go)
// draws the latest one and sends actions back. Terminals are drawn straight
// from their Term, which is safe from any goroutine.

type driver struct {
	in      chan any // tea.Msg, or func(*model) to run on the loop
	state   atomic.Pointer[guiState]
	redraw  func()
	clip    func(string)
	quit    func()
	palOpen bool
	last    time.Time
	dirty   bool   // something besides output or the clock changed since the last snapshot
	sig     string // the last snapshot's viewSig
}

// start runs m at startup: peers, the last session
// reopened, alert polling, old recordings cleaned, then the model loop. It
// returns the window state and the peer socket (removed by Close).
func start(m model, invalidate func()) (*ui, string) {
	guiReplies = true
	d := &driver{in: make(chan any, 256)}
	d.state.Store(&guiState{})
	dispatch = func(msg tea.Msg) { d.in <- msg }
	sock, _ := servePeers()
	m.restore()
	startAlerts(m.tree)
	go cleanRecordings()
	u := newUI(d, m.guiLayout, invalidate)
	d.redraw, d.clip = u.redraw, u.setClipboard
	d.quit = func() {} // ctrl+q is the TUI's; desk's window decides when to quit
	repaint = u.termRedraw
	d.state.Store(d.snapshot(&m)) // the tree and restored tabs, before any message
	go d.loop(m)
	return u, sock
}

func (d *driver) loop(m model) {
	d.run(m.Init())
	for x := range d.in {
		switch x := x.(type) {
		case func(*model):
			x(&m)
		case redrawMsg, tickMsg: // terminal output and the clock: a snapshot only if viewSig moved
			d.update(&m, x)
			d.emit(&m)
			continue
		default:
			d.update(&m, x)
		}
		d.dirty = true
		d.emit(&m)
	}
}

func (d *driver) update(m *model, msg tea.Msg) {
	r, cmd := m.Update(msg)
	*m = r.(model)
	d.run(cmd)
}

// key presses k as the TUI would, for actions whose logic lives in key.
func (d *driver) key(m *model, k string) { d.update(m, keyOf(k)) }

func keyOf(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	if c, ok := strings.CutPrefix(s, "ctrl+"); ok {
		return tea.KeyPressMsg{Code: rune(c[0]), Mod: tea.ModCtrl}
	}
	if c, ok := strings.CutPrefix(s, "alt+"); ok {
		return tea.KeyPressMsg{Code: rune(c[0]), Mod: tea.ModAlt}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

// run executes a tea.Cmd off the loop and brings its message back. (Not
// `go d.deliver(cmd())`: that runs cmd, e.g. a 1s tick, on the loop itself.)
func (d *driver) run(cmd tea.Cmd) {
	if cmd != nil {
		go func() { d.deliver(cmd()) }()
	}
}

func (d *driver) deliver(msg tea.Msg) {
	switch msg := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			d.run(c)
		}
	case tea.QuitMsg:
		d.quit()
	default:
		// ponytail: tea.SetClipboard's message type is unexported; matched by name
		if v := reflect.ValueOf(msg); v.Kind() == reflect.String && strings.HasSuffix(fmt.Sprintf("%T", msg), "setClipboardMsg") {
			d.clip(v.String())
			return
		}
		d.in <- msg
	}
}

// emit publishes the state right away, so clicks and keys show on the next
// frame. During a burst (more messages queued) it waits for the burst to
// end, but never more than 50ms. Unless an action or a message other than
// redrawMsg and tickMsg came (dirty), it publishes only when viewSig moved:
// a pane's output alone needs no snapshot (the terminals draw from Term).
func (d *driver) emit(m *model) {
	if len(d.in) > 0 && time.Since(d.last) < 50*time.Millisecond {
		return
	}
	sig := m.viewSig()
	if !d.dirty && sig == d.sig {
		return
	}
	d.last, d.dirty, d.sig = time.Now(), false, sig
	d.state.Store(d.snapshot(m))
	d.redraw()
}

// viewSig is what a snapshot shows that may change with no message of its
// own: the panes' connections, blocks and live state as their output comes,
// and what the redraw and tick handlers change (reap, redial, forwards, the
// health check).
func (m *model) viewSig() string {
	var b strings.Builder
	fmt.Fprint(&b, m.active(), len(m.tabs), m.flash, "\x00", m.errText)
	for _, t := range m.tabs {
		fmt.Fprintf(&b, "|%p %d %d %t %t", t, len(t.chat), len(t.pending), t.busy, t.term != nil && t.term.Live())
		t.mu.Lock()
		panes, focus := slices.Clone(t.panes), t.focused()
		t.mu.Unlock()
		fmt.Fprintf(&b, " %p", focus)
		for _, p := range panes {
			fmt.Fprintf(&b, ",%p %s %d %d %d %d %d", p.term, p.connState(), p.term.Rev(), p.tries, p.retry.UnixNano(), p.lost.UnixNano(), p.waitUntil.UnixNano())
		}
	}
	return b.String()
}

// act runs f on the model's goroutine; the next snapshot shows the result.
func (d *driver) act(f func(*model)) { d.in <- f }

type guiNode struct {
	Depth                              int
	Label, Org, Group, Host, Addr, Dir string
	Alert                              string // worst Nagios alert on the host ("" none)
	Note                               string // its desc and tags, searched with the rest
	Pin                                bool   // pinned in the servers file
}

type guiPane struct {
	Term            *Term
	N               int // the number Claude uses
	Name, Addr, Dir string
	Claude, Focused bool
	Conn            string
	Blocks          map[int]Block
	Password        bool // the server has a vault password (alt+p can answer sudo)
	// for the connection box: since when it waits (or dropped), the redials
	// so far and the next one, why it failed, and whether
	// that names old algorithms Fix can add
	Since, Retry time.Time
	Tries        int
	Err          string
	CanFix       bool
	KeyChange    *hostKeyChange // the server sent another host key: the one sent and the one known
}

type guiChat struct {
	Who, Text string
	Ctx       []int
	Screen    bool
	Steps     []step
}

type guiTab struct {
	Key                                            *Tab // which model tab, for state the window keeps per tab
	Name, Org, Group, Dir, Account, State, Suggest string
	Model                                          string // "" the account's default
	OrgOnly, Busy, PinScreen, Live, Broadcast      bool
	Card                                           string // the card whose own session this is ("" the org's)
	Compacting                                     bool
	Ctx, CtxWindow                                 int // Claude's context: tokens in it, the model's window (0 unknown)
	Panes                                          []guiPane
	Chat                                           []guiChat
	Pinned                                         map[int]bool
}

type guiApproval struct {
	Tab, Host, Cmd, Reason, Why string
	Keys, Secret, Blocked       bool
	Explain                     []step
	Org                         string // the tab's org: the sheet shows only over that org's workspace
}

// guiPal is a palette row, with what its preview looks up: a session's tab,
// a host's (or org's) node by Host and Dir.
type guiPal struct {
	Kind, Label, Hint string
	Tab               int
	Host, Dir         string
}

type guiState struct {
	Tree       []guiNode
	Tabs       []guiTab
	Active     int
	Group      string // dir of the group view, "" when off
	Approval   *guiApproval
	Ask        string // title of the question waiting for the user
	AskMasked  bool
	Pal        []guiPal
	PalOpen    bool // Pal is loaded for the open palette
	Flash, Err string
	Theme      string // themes.go name; the window follows it (gui_theme.go)
}

func (d *driver) snapshot(m *model) *guiState {
	s := &guiState{Active: m.active(), Group: m.group, Flash: m.flash, Err: m.errText, Theme: themeName}
	var cam *Term // the active tab's focused pane, for the camera and share window
	defer func() { camTerm.Store(cam) }()
	for _, n := range m.tree {
		s.Tree = append(s.Tree, guiNode{n.depth, n.label, n.org, n.group, n.host, n.addr, n.dir, m.hostAlert(n), strings.TrimSpace(n.desc + " " + n.tags), n.pin})
	}
	for _, t := range m.tabs {
		gt := guiTab{Key: t, Name: t.name, Org: t.org, Group: t.group, Dir: t.dir, Account: t.account, State: tabState(t),
			Suggest: t.suggest, OrgOnly: t.orgOnly, Card: t.card, Busy: t.busy, PinScreen: t.pinScreen, Broadcast: t.broadcast, Model: t.model, Pinned: map[int]bool{},
			Compacting: t.compacting, Ctx: t.ctxTokens, CtxWindow: t.ctxWindow}
		t.mu.Lock()
		panes, focus := slices.Clone(t.panes), t.focused()
		t.mu.Unlock()
		for _, p := range panes {
			if p == focus && t == m.tab() {
				cam = p.term
			}
			gp := guiPane{Term: p.term, N: p.id, Name: p.name, Addr: p.addr, Dir: p.dir, Claude: p.claude, Focused: p == focus, Conn: p.connState(),
				Blocks: map[int]Block{}, Password: p.password != nil, Since: p.waitUntil.Add(-connectWait), Retry: p.retry, Tries: p.tries}
			switch gp.Conn {
			case "reconnecting":
				gp.Since = p.lost
			case "failed":
				err := p.term.Err()
				gp.Err = lastLine(p.term.Unwrapped()) // what the shell said, for HostCmd's
				if err != nil {
					gp.Err = err.Error()
				}
				gp.CanFix = len(legacyFixes(err)) > 0
				gp.KeyChange = hostKeyChangeOf(err)
			}
			for _, b := range p.term.Blocks() {
				b.Output = "" // the snapshot only needs the badges
				gp.Blocks[b.N] = b
			}
			gt.Panes = append(gt.Panes, gp)
		}
		for _, e := range t.chat {
			gt.Chat = append(gt.Chat, guiChat{e.who, e.text, e.ctx, e.screen, e.steps})
		}
		if t.term != nil {
			gt.Live = t.term.Live()
		}
		for n := range t.pinned {
			gt.Pinned[n] = true
		}
		s.Tabs = append(s.Tabs, gt)
	}
	if t := m.approvalTab(); t != nil {
		a := t.pending[0]
		host := ""
		if p, err := t.pane(a.pane); err == nil {
			host = p.name
		}
		s.Approval = &guiApproval{t.name, host, a.cmd, a.reason, a.why, a.keys, a.secret, a.blocked, a.explain, t.org}
	}
	if len(m.asks) > 0 {
		s.Ask, s.AskMasked = m.asks[0].title, m.asks[0].masked
	}
	if d.palOpen {
		s.PalOpen = true
		for _, it := range m.palItems() {
			s.Pal = append(s.Pal, guiPal{Kind: it.kind, Label: it.label, Hint: it.hint, Tab: it.tab, Host: it.n.host, Dir: it.n.dir})
		}
	}
	return s
}

// tabState is what the tab's dot says: approval, working, your turn or idle.
func tabState(t *Tab) string {
	switch {
	case len(t.pending) > 0:
		return "approval"
	case t.busy:
		return "working"
	case len(t.chat) > 0 && t.chat[len(t.chat)-1].who != "you":
		return "your turn"
	}
	return "idle"
}

// Actions the window sends. Each runs on the model's goroutine.

// open is enter on sidebar row i: a host's session, an org's, or a group view.
func (d *driver) open(i int) {
	d.act(func(m *model) {
		if i >= 0 && i < len(m.tree) {
			m.cursor = i
			m.sideEnter()
		}
	})
}

func (d *driver) activate(i int) {
	d.act(func(m *model) {
		if i >= 0 && i < len(m.tabs) {
			m.activate(i)
			m.group = ""
		}
	})
}

func (d *driver) closeTab(i int) {
	d.act(func(m *model) {
		if i >= 0 && i < len(m.tabs) {
			m.closeTab(i)
		}
	})
}

// press runs one of the workspace's shortcuts on the active tab: "ctrl+t" new
// pane, "ctrl+w" close pane, "ctrl+y" type the suggestion, "alt+p" password,
// "ctrl+s" pin the screen, or w/c/r/f on a connection that isn't up.
func (d *driver) press(k string) { d.act(func(m *model) { d.key(m, k) }) }

func (d *driver) focusPane(i int) {
	d.act(func(m *model) {
		if t := m.tab(); t != nil && i >= 0 && i < len(t.panes) {
			t.focusPane(i)
		}
	})
}

// closePane closes pane i of the active tab.
func (d *driver) closePane(i int) {
	d.act(func(m *model) {
		if t := m.tab(); t != nil && i >= 0 && i < len(t.panes) {
			t.focusPane(i)
			d.key(m, "ctrl+w")
		}
	})
}

func (d *driver) ask(q string) {
	d.act(func(m *model) {
		m.input = q
		m.ask()
	})
}

// saveLayout stores the panels' state in layout.json, through the model.
func (u *ui) saveLayout() {
	l := guiLayout{NoSidebar: !u.side, NoClaude: !u.claude, ClaudeW: int(u.claudeW)}
	u.d.act(func(m *model) { m.guiLayout = l; m.saveUI() })
}

func (d *driver) toggleBroadcast() { d.act(func(m *model) { m.toggleBroadcast(m.tab()) }) }

func (d *driver) stop() { d.act(func(m *model) { m.stop(m.tab()) }) }

func (d *driver) compact() {
	d.act(func(m *model) {
		if t := m.tab(); t != nil {
			m.compactContext(t)
		}
	})
}

func (d *driver) clearContext() {
	d.act(func(m *model) {
		if t := m.tab(); t != nil {
			m.clearContext(t)
		}
	})
}

func (d *driver) pin(n int) {
	d.act(func(m *model) {
		if t := m.tab(); t != nil {
			if t.pinned[n] {
				delete(t.pinned, n)
			} else {
				t.pinned[n] = true
			}
		}
	})
}

// answer decides the approval on screen: y, a (always, this tab) or n; cmd,
// when it differs, is the user's edit of the command.
func (d *driver) answer(verdict, cmd string) {
	d.act(func(m *model) {
		t := m.approvalTab()
		if t == nil {
			return
		}
		if a := t.pending[0]; cmd != "" && cmd != a.cmd && !a.secret {
			a.edit, a.editing = cmd, true
			d.key(m, "enter")
		}
		d.key(m, verdict)
	})
}

// reply answers the question on screen (a vault passphrase…); !ok cancels.
func (d *driver) reply(text string, ok bool) {
	d.act(func(m *model) {
		if len(m.asks) == 0 {
			return
		}
		if ok {
			m.asks[0].text = text
			d.key(m, "enter")
		} else {
			d.key(m, "esc")
		}
	})
}

// group shows the group view of dir ("" leaves it).
func (d *driver) group(dir string) { d.act(func(m *model) { m.group = dir }) }

// broadcast asks every session of the group on screen.
func (d *driver) broadcast(q string) {
	d.act(func(m *model) {
		if m.group != "" {
			m.input = q
			d.key(m, "enter")
		}
	})
}

// palette opens (q != nil) or closes the palette with query *q.
func (d *driver) palette(q *string) {
	d.act(func(m *model) {
		d.palOpen = q != nil
		if q != nil {
			m.palQ = *q
		}
	})
}

// palPick runs item i of the palette's current list.
func (d *driver) palPick(i int) {
	d.act(func(m *model) {
		m.pal, m.palSel = true, 0
		for j := range m.palItems() {
			if i == 0 {
				m.palSel = j
				d.key(m, "enter")
				break
			}
			i--
		}
		m.pal, d.palOpen = false, false
	})
}

// shortcut handles the app's own keys (keys' infra.* bindings); it reports
// whether e was one.
func (u *ui) shortcut(e key.Event) bool {
	d, st := u.d, u.st
	switch name := string(e.Name); {
	case keys.Is("infra.palette", e):
		u.openPalette()
	case keys.Is("infra.pane", e):
		d.press("ctrl+t")
	case keys.Is("infra.close", e):
		d.press("ctrl+w")
	case keys.Is("infra.claude", e):
		u.claude = !u.claude
		u.saveLayout()
	case keys.Is("infra.group", e):
		if st.Group != "" {
			d.group("")
		} else if t := u.tab(); t != nil && !t.OrgOnly {
			d.group(groupKey(t.Dir, t.Group))
		}
	case keys.Is("infra.suggest", e):
		d.press("ctrl+y")
		u.wantTerm = true
	case keys.Is("infra.password", e):
		d.press("alt+p")
	case keys.Is("infra.pin", e):
		d.press("ctrl+s")
	case keys.Is("infra.composer", e):
		u.toggleComposer = true
	case keys.Is("infra.files", e):
		u.toggleFiles()
	case keys.Is("infra.find", e):
		u.openFind()
	case keys.Is("infra.blockup", e):
		u.selectBlock(true)
	case keys.Is("infra.blockdown", e):
		u.selectBlock(false)
	case keys.Is("infra.copy", e):
		if u.chatSel != nil && u.chatSel.selected() != "" {
			u.copyOnSelect(u.chatSel.selected())
		} else {
			u.copySelection = true
		}
	case keys.Is("infra.paste", e):
		u.pasteWanted = true
	case keys.Is("infra.next", e):
		u.cycleTab(true)
	case keys.Is("infra.prev", e):
		u.cycleTab(false)
	// ponytail: on Linux this never matches: Gio's xkb names a key by its
	// shifted keysym, so Ctrl+Shift+1 arrives as "!" (US layout), and a
	// non-ASCII one (ES Shift+3 "·") as no key.Event at all. Upgrade: a
	// patched Gio in third_party naming a digit key by its level-0 keysym
	// while Ctrl+Shift is held.
	case e.State == key.Press && e.Modifiers&keys.Mod == keys.Mod && len(name) == 1 && name[0] >= '1' && name[0] <= '9':
		if ts, n := u.orgTabs(), int(name[0]-'1'); n < len(ts) {
			d.activate(ts[n])
			u.wantTerm = true
		}
	default:
		return false
	}
	u.inv()
	return true
}

// cycleTab activates the org's next tab (or previous), wrapping around; from a
// tab of another org, its first (or last).
func (u *ui) cycleTab(next bool) {
	ts := u.orgTabs()
	if len(ts) == 0 {
		return
	}
	n := map[bool]int{true: 0, false: len(ts) - 1}[next]
	for j, i := range ts {
		if i == u.st.Active {
			n = (j + map[bool]int{true: 1, false: len(ts) - 1}[next]) % len(ts)
		}
	}
	u.d.activate(ts[n])
	u.wantTerm = true
}

// shortcutFilters are the keys shortcut handles, for the window-wide handler.
// On macOS ⌘C and ⌘V stay out: a focus-less filter beats the focused
// editor, whose copy and paste they are there; a focused terminal still
// hands them to shortcut (termEvents).
func shortcutFilters() []event.Filter {
	ids := []string{"infra.palette", "infra.pane", "infra.close", "infra.claude", "infra.group", "infra.suggest", "infra.password", "infra.pin",
		"infra.composer", "infra.files", "infra.find", "infra.blockup", "infra.blockdown", "infra.next", "infra.prev"}
	if keys.Mod != key.ModCommand {
		ids = append(ids, "infra.copy", "infra.paste")
	}
	fs := keys.Filters(nil, ids...)
	for n := '1'; n <= '9'; n++ {
		fs = append(fs, key.Filter{Name: key.Name(n), Required: keys.Mod})
	}
	return fs
}

// redraw asks for a frame after new state or terminal output. Gio drops an
// Invalidate that arrives while a frame is being made, so each frame also
// checks gen and asks again if something changed while it drew.
func (u *ui) redraw() {
	u.gen.Add(1)
	u.inv()
}

// termRedraw is redraw for terminal output (repaint): none while desk doesn't
// show the workspace (SetShown); the snapshot's redraw still comes.
func (u *ui) termRedraw() {
	if !u.hidden.Load() {
		u.redraw()
	}
}

// frame lays out the workspace for org, without a sidebar
// (desk's own sidebar lists the hosts). The newest snapshot is read
// first; a change made while it drew asks for one more frame (Gio drops an
// Invalidate that arrives during a frame).
func (u *ui) frame(gtx layout.Context, org string) layout.Dimensions {
	gen := u.gen.Load()
	u.st = u.d.state.Load()
	if u.st.Theme != "" && u.st.Theme != guiTheme {
		applyTheme(u.st.Theme)
	}
	u.follow(org)
	for _, f := range shortcutFilters() {
		for {
			ev, ok := gtx.Event(f)
			if !ok {
				break
			}
			if ke, ok := ev.(key.Event); ok {
				u.shortcut(ke)
			}
		}
	}
	u.flushClipboard(gtx)
	gtx.Constraints.Min = gtx.Constraints.Max
	u.background(gtx)
	d := u.mainColumn(gtx)
	u.sheets(gtx)
	u.toasts(gtx)
	if u.gen.Load() != gen {
		gtx.Execute(op.InvalidateCmd{})
	}
	return d
}

// follow makes org's tabs the ones shown: when desk moves to another org,
// the tab last used there (else its first) becomes active. A switch made
// from inside (the palette, a peer) is left alone: ActiveOrg tells desk.
func (u *ui) follow(org string) {
	if t := u.activeTab(); t != nil && t.Org == u.org {
		u.lastTab[u.org] = t.Key
	}
	if org == u.org {
		return
	}
	u.org = org
	if t := u.activeTab(); t != nil && t.Org == org {
		return
	}
	first := -1
	for i, t := range u.st.Tabs {
		if t.Org != org {
			continue
		}
		if t.Key == u.lastTab[org] {
			first = i
			break
		}
		if first < 0 {
			first = i
		}
	}
	if first >= 0 {
		u.d.activate(first)
	} else if u.st.Group != "" {
		u.d.group("")
	}
}

// groupOrg is the org of the group view's dir ("" none).
func (u *ui) groupOrg() string {
	for _, n := range u.st.Tree {
		if u.st.Group != "" && n.Host != "" && groupKey(n.Dir, n.Group) == u.st.Group {
			return n.Org
		}
	}
	return ""
}

func (u *ui) mainColumn(gtx layout.Context) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(u.toolbar),
		layout.Rigid(vgap(1)),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			if u.st.Group != "" && u.groupOrg() == u.org {
				return u.groupView(gtx)
			}
			kids := []layout.FlexChild{layout.Flexed(1, u.terminals)}
			if u.claude {
				kids = append(kids, layout.Rigid(hgap(1)), layout.Rigid(u.claudePanel))
			}
			return layout.Flex{}.Layout(gtx, kids...)
		}),
	)
}

// tab is the active tab when it belongs to the org on screen, else nil.
func (u *ui) tab() *guiTab {
	if t := u.activeTab(); t != nil && t.Org == u.org {
		return t
	}
	return nil
}

func (u *ui) activeTab() *guiTab {
	if u.st.Active < len(u.st.Tabs) {
		return &u.st.Tabs[u.st.Active]
	}
	return nil
}

func hgap(dp unit.Dp) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: image.Pt(gtx.Dp(dp), 0)} }
}

func vgap(dp unit.Dp) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: image.Pt(0, gtx.Dp(dp))} }
}
