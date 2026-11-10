package infra

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lfdominguez/desk/internal/claude"
	deskorgs "github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/sshc"
)

// node is one row of the sidebar tree: org, group or host.
type node struct {
	depth             int
	label, org, group string
	dir               string
	host              string // set on host rows: what ssh connects to
	addr              string // HostName from ssh_config, for display and search
	wol               string // Wake-on-LAN target ("#@ wol"), sent before connecting
	nagios            string // its name in Nagios ("#@ nagios"), "" the alias or address
	desc, tags        string // "#@ desc", "#@ tags" (comma separated): for search and Claude's host list
	pin               bool   // "#@ pin": always in the pinned section
}

// groupKey names a group for the group view: a servers file's groups share
// the org's infra dir.
func groupKey(dir, group string) string { return dir + "\x00" + group }

type chatEntry struct {
	who, text string
	ctx       []int  // blocks sent with a question
	screen    bool   // a screen snapshot went with it
	steps     []step // tool calls: what each piece of the command does
	lines     int
	md        string // rendered markdown cache
	mdW       int
	tgts      []target // copy buttons in md
}

// Pane is one shell of a tab, on any server of the tab's org. Panes on the
// same server share one SSH connection, so a split opens at once.
type Pane struct {
	id         int
	term       *Term
	claude     bool   // Claude opened it (and may close it)
	dir        string // the org's infra dir (its servers file: sshConfigFor)
	name, addr string // the server's name in the sidebar, its address if known
	password   *SecretRef
	waitUntil  time.Time // connect deadline: after it, the timeout notice
	closed     bool      // closed on purpose (guarded by the tab's mu)
	lost       time.Time // when its link dropped; zero while up (redial)
	kept       paneState // what it had before the drop, given to each retry
	retry      time.Time // next redial attempt
	tries      int
}

// Tab is one Claude session of an org. A host tab starts with a pane on that
// host; an org tab (orgOnly) starts with Claude alone and Claude opens panes.
type Tab struct {
	org, group, dir, account string
	name                     string     // shown in the tab bar
	orgOnly                  bool       // stays open with no panes
	card                     string     // the card (DESK-12) whose server work this is, its own Claude; "" the org's
	hosts                    []node     // the org's servers, for connect
	term                     *Term      // the focused pane's terminal; nil with no panes
	mu                       sync.Mutex // guards panes and cur, read by tool calls
	panes                    []*Pane
	cur, nextID              int
	claude                   *Claude
	chat                     []*chatEntry
	busy                     bool
	suggest                  string
	pinned                   map[int]bool    // picked with space, sent with the next question
	lastCtx                  map[int]bool    // sent with the last question
	pinScreen                bool            // ^s: send a screen snapshot with the next question
	pending                  []*approval     // commands waiting for the user, oldest first
	orgCfg                   *orgConfig      // the org's infra config (vaults, nagios, metrics)
	peerCtx                  []string        // context from peers, sent with the next prompt
	queue                    []*peerTask     // peer tasks waiting for Claude to be free
	asked                    []string        // the user's questions typed while Claude was busy
	session                  string          // Claude Code's session id, to resume after a restart
	broadcast                bool            // what the user types goes to every pane of the tab
	notesSent                map[string]bool // servers whose notes this Claude process has (guarded by mu)
	opened                   time.Time       // when the tab opened (report.go reads the audit log from here)
	model                    string          // claude --model for this tab ("" the account's default)
	health                   bool            // the running turn is the daily health check: notify when done
	ctxTokens, ctxWindow     int             // Claude's context: tokens in it now, the model's window (0: not known yet)
	compacting               bool            // a /compact runs (busy is set too)
}

// claudeModels are the palette's model choices.
var claudeModels = []string{"opus", "sonnet", "haiku"}

// setModel switches t's Claude to model ("" default): the process restarts on
// the next question and resumes the same conversation.
func (m *model) setModel(t *Tab, name string) {
	if t.busy {
		m.flash = "Claude is busy; switch models when it's done"
		return
	}
	t.model = name
	if t.claude != nil {
		t.claude.Close() // its exit no longer matches t.claude, so the session id stays
		t.claude = nil
	}
	if name == "" {
		name = "default"
	}
	t.chat = append(t.chat, &chatEntry{who: "note", text: "model: " + name + " (from the next question)"})
}

// ctxText is t's context use for people: "37.1k / 1M tokens (4%)".
func ctxText(t *Tab) string {
	switch {
	case t == nil || t.ctxTokens == 0:
		return "context: nothing yet"
	case t.ctxWindow == 0:
		return fmtNum(float64(t.ctxTokens)) + " tokens"
	}
	return fmt.Sprintf("%s / %s tokens (%d%%)", fmtNum(float64(t.ctxTokens)), fmtNum(float64(t.ctxWindow)), t.ctxTokens*100/t.ctxWindow)
}

// compactContext has Claude Code summarize t's conversation so far (/compact):
// the tab's process stops, a one-off claude compacts the session, and the
// next question resumes the shorter one.
func (m *model) compactContext(t *Tab) {
	switch {
	case t.busy:
		m.flash = "Claude is busy; compact when it's done"
		return
	case t.session == "":
		m.flash = "no conversation to compact yet"
		return
	}
	if t.claude != nil {
		t.claude.Close()
		t.claude = nil
	}
	t.busy, t.compacting = true, true
	t.chat = append(t.chat, &chatEntry{who: "note", text: "compacting the context…"})
	go compactSession(t.dir, t.model, t.session, func(cm claudeMsg) { dispatch(cm) }, t)
}

// clearContext starts Claude's next question in a new conversation; the
// chat stays on screen.
func (m *model) clearContext(t *Tab) {
	if t.busy {
		m.flash = "Claude is busy; clear when it's done"
		return
	}
	if t.claude != nil {
		t.claude.Close()
		t.claude = nil
	}
	t.session, t.ctxTokens = "", 0
	t.chat = append(t.chat, &chatEntry{who: "note", text: "— context cleared: Claude starts a new conversation with your next question —"})
}

// typingTo is where the user's keys typed into term go: term, plus every
// other pane of t while broadcast is on.
func (t *Tab) typingTo(term *Term) []*Term {
	if !t.broadcast {
		return []*Term{term}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := []*Term{term}
	for _, p := range t.panes {
		if p.term != term {
			out = append(out, p.term)
		}
	}
	return out
}

// toggleBroadcast turns typing into all of t's panes on or off.
func (m *model) toggleBroadcast(t *Tab) {
	if t == nil || len(t.panes) < 2 && !t.broadcast {
		m.flash = "broadcast needs two or more panes (" + modText("T") + " opens one)"
		return
	}
	t.broadcast = !t.broadcast
	m.flash = map[bool]string{true: "typing into every pane of this tab · the toolbar's fan button stops", false: "typing into one pane"}[t.broadcast]
}

type redrawMsg struct{}

// model is the workspace's state, owned by the driver's goroutine (gui.go):
// model is the workspace's Bubble Tea model, run headless (gui.go draws it).
type model struct {
	tree    []node
	cursor  int // the tree row the last open acted on
	tabs    []*Tab
	active  int
	input   string // the group view's broadcast question
	errText string
	group   string // group view: the groupKey whose sessions are shown as cards
	pal     bool   // the palette is picking (palItems' query is palQ)
	palQ    string
	palSel  int
	saved   uiState
	asks    []*question // integrations waiting for a passphrase or code
	flash   string      // a short note shown as a toast

	onInvestigate func(org, host, service, state, output string, since time.Time) // desk's: an investigation is a card

	guiLayout guiLayout // the window's panels
	away      bool      // the user isn't looking at the workspace: notify on the desktop

	alerts       map[string][]nagiosAlert // current alerts by source (alerts.go)
	forwards     []portFwd                // port forwards added from the palette (kept in session.json)
	fwdOn        map[portFwd]bool         // added on the server's current connection
	root         string                   // where the tree comes from (desk's orgs.Root)
	restored     bool                     // the last session was reopened (or there was none): saving may start
	sessionSaved string                   // sessionSig of what session.json holds
	kept         map[*Term]keptPane       // each pane's last saved Keep (session.go)
	alertWorst   map[string]string        // worst alert state by org + host (alerts.go), nil: rebuild
}

// uiState is what the workspace remembers between runs.
type uiState struct {
	Theme string
	GUI   guiLayout
}

// guiLayout is the window's part of layout.json; zero values are its defaults.
type guiLayout struct {
	NoSidebar, NoClaude bool
	ClaudeW             int // dp
}

func (m *model) ui() uiState { return uiState{themeName, m.guiLayout} }

func uiPath() string {
	d, _ := deskorgs.XDGDir("XDG_CONFIG_HOME", ".config")
	return filepath.Join(d, "desk", "infra-layout.json")
}

func (m *model) saveUI() {
	if u := m.ui(); u != m.saved {
		m.saved = u
		b, _ := json.Marshal(u)
		os.MkdirAll(filepath.Dir(uiPath()), 0o755)
		os.WriteFile(uiPath(), b, 0o644)
	}
}

var (
	pending atomic.Bool
	// dispatch hands a message from any goroutine to the model loop (gui.go).
	dispatch func(tea.Msg)
	// repaint, when set, redraws the GUI as soon as a terminal has output,
	// without waiting for the model loop.
	repaint func()
)

func notify() {
	if repaint != nil {
		repaint()
	}
	if dispatch != nil && pending.CompareAndSwap(false, true) {
		go dispatch(redrawMsg{})
	}
}

// loadTree reads each org's servers file (<root>/<org>/.desk/ssh_config, see
// servers.go), creating its infra dir, where all its tabs work. Orgs without
// one are skipped.
func loadTree(root string) []node {
	var tree []node
	for _, org := range deskorgs.List(root) { // desk's organizations, in all its folders
		odir := deskorgs.Dir(root, org, "")
		if f := serversPath(odir); f != "" {
			tree = append(tree, serverTree(org, makeInfra(odir), f)...)
		}
	}
	return tree
}

func (m *model) tab() *Tab {
	if len(m.tabs) == 0 {
		return nil
	}
	return m.tabs[m.active]
}

// chatWidth is how wide Claude's reply is drawn, so its pictures fit.
const chatWidth = 90

// hostLabel is the name and, when it differs, where it connects.
func (p *Pane) hostLabel() string {
	switch {
	case p.addr != "":
		return p.name + " (" + p.addr + ")"
	case p.name != p.term.Host:
		return p.name + " (" + p.term.Host + ")"
	}
	return p.name
}

// focused is the pane the user looks at, or nil (UI goroutine).
func (t *Tab) focused() *Pane {
	if len(t.panes) == 0 {
		return nil
	}
	return t.panes[t.cur]
}

// paneName is the server name of pane id ("" if there is none).
func (t *Tab) paneName(id int) string {
	if p, err := t.pane(id); err == nil {
		return p.name
	}
	return ""
}

// pane finds a pane by id for a tool call (0: the one the user looks at).
func (t *Tab) pane(id int) (*Pane, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.panes) == 0 {
		return nil, fmt.Errorf("no pane is open: use connect to open one on a server")
	}
	if id == 0 {
		return t.panes[t.cur], nil
	}
	for _, p := range t.panes {
		if p.id == id {
			return p, nil
		}
	}
	return nil, fmt.Errorf("there is no pane %d; open panes: %s", id, t.describePanes())
}

// describePanes is the pane list Claude gets. Caller holds mu.
func (t *Tab) describePanes() string {
	var ds []string
	for i, p := range t.panes {
		d := fmt.Sprintf("pane %d on %s: %s", p.id, p.name, p.term.Status())
		if i == t.cur {
			d += ", the user's focus"
		}
		if p.claude {
			d += ", opened by you"
		}
		ds = append(ds, d)
	}
	return strings.Join(ds, "; ")
}

// paneMsg asks the UI goroutine to open a pane on n for Claude.
type paneMsg struct {
	tab  *Tab
	n    node
	done chan *Pane
}

// paneNode is the server a pane is on, to open another pane there.
func (p *Pane) node() node {
	return node{host: p.term.Host, dir: p.dir, label: p.name, addr: p.addr}
}

// addPane opens a pane on server n in t (UI goroutine only). The first pane
// of a tab gets the keyboard.
func (m *model) addPane(t *Tab, n node, claude bool) *Pane {
	return m.addPaneFrom(t, n, claude, paneState{})
}

// addPaneFrom opens a pane that starts from a kept state (session restore).
func (m *model) addPaneFrom(t *Tab, n node, claude bool, st paneState) *Pane {
	w, h := 120, 36 // until its view sets the real size
	if n.wol != "" {
		go wake(n.wol) // ponytail: no wait for it to boot; the connect timeout box covers a slow one
	}
	term := m.dial(t, n.host, n.dir, n.label, w, h, st)
	if term == nil {
		return nil
	}
	t.mu.Lock()
	t.nextID++
	p := &Pane{id: t.nextID, term: term, claude: claude, dir: n.dir, name: n.label, addr: n.addr,
		password: passwordRef(n.dir, n.host), waitUntil: time.Now().Add(connectWait)}
	t.panes = append(t.panes, p)
	t.mu.Unlock()
	if len(t.panes) == 1 {
		t.focusPane(0)
	}
	return p
}

// dial starts a terminal for a pane of t: audited, and recorded unless the
// org opts out.
func (m *model) dial(t *Tab, host, dir, label string, w, h int, prelude paneState) *Term {
	prelude.keepLogin = t.orgCfg.keepsLogin()
	term, err := NewTerm(host, dir, w, h, notify, prelude)
	if err != nil {
		m.errText = err.Error()
		return nil
	}
	term.OnBlock(func(b Block) { blockDone(t, label, b) })
	if t.orgCfg.records() {
		if err := term.Record(recordingPath(t.org, label, time.Now())); err != nil {
			m.errText = "recording: " + err.Error()
		}
	}
	return term
}

// focusPane moves the keyboard to pane i; block picks belong to the old
// pane's numbering, so they go.
func (t *Tab) focusPane(i int) {
	t.mu.Lock()
	t.cur, t.term = i, t.panes[i].term
	t.mu.Unlock()
	t.pinned, t.lastCtx = map[int]bool{}, map[int]bool{}
}

// closePane hangs up a pane; the next redraw drops it.
func (t *Tab) closePane(p *Pane) {
	t.mu.Lock()
	p.closed = true
	t.mu.Unlock()
	p.term.Close()
}

// dropExited removes panes whose shell ended. One that never connected stays
// until closed, so its tab can say why.
func (m *model) dropExited(t *Tab) {
	focused := t.focused()
	t.mu.Lock()
	var keep []*Pane
	for _, p := range t.panes {
		if _, exited := p.term.State(); !exited || (!p.closed && (!p.term.Connected() || !p.lost.IsZero() || p.term.Lost())) {
			keep = append(keep, p)
		}
	}
	if len(keep) == len(t.panes) {
		t.mu.Unlock()
		return
	}
	t.panes = keep
	if len(keep) == 0 {
		t.cur, t.term = 0, nil
	}
	t.mu.Unlock()
	if len(keep) > 0 {
		i := slices.Index(keep, focused)
		if i < 0 {
			i = len(keep) - 1
		}
		if keep[i] != focused {
			t.focusPane(i)
		}
	}
}

type tickMsg struct{}

// tick drives connect timeouts and closing tabs whose shell ended.
func tick() tea.Cmd { return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} }) }

func (m model) Init() tea.Cmd { return tick() }

// View only makes model a tea.Model: the window draws from snapshots.
func (m model) View() tea.View { return tea.View{} }

const connectWait = 10 * time.Second

// connState is "" once the pane's connection is up, "connecting" while its
// own dial runs (the box shows its steps, a question asked included),
// "timeout" while it hasn't answered past waitUntil, "failed" when it ended
// without ever connecting.
func (p *Pane) connState() string {
	c := p.term
	_, exited := c.State()
	switch {
	case !p.lost.IsZero():
		return "reconnecting"
	case c.connecting():
		return "connecting"
	case c.Connected():
		return ""
	case exited:
		return "failed"
	case time.Now().After(p.waitUntil):
		return "timeout"
	}
	return ""
}

// connState is the focused pane's.
func (t *Tab) connState() string {
	if p := t.focused(); p != nil {
		return p.connState()
	}
	return ""
}

// reap drops ended panes and closes host tabs left without any (the user
// typed exit). Org tabs stay with Claude alone.
func (m *model) reap() {
	for i := len(m.tabs) - 1; i >= 0; i-- {
		t := m.tabs[i]
		m.dropExited(t)
		if len(t.panes) == 0 && !t.orgOnly {
			m.closeTab(i)
		}
	}
}

// reconnect starts the focused pane's connection over (after a fix, or the
// VPN came up).
func (m *model) reconnect(t *Tab) { m.redialPane(t, t.focused()) }

func (m *model) redialPane(t *Tab, p *Pane) {
	w, h := p.term.Size()
	term := m.dial(t, p.term.Host, p.dir, p.name, w, h, p.kept)
	if term == nil {
		return
	}
	p.term.Close()
	t.mu.Lock()
	if t.term == p.term {
		t.term = term
	}
	p.term = term
	t.mu.Unlock()
	p.waitUntil = time.Now().Add(connectWait)
}

// Dropped links come back on their own: ServerAliveInterval (sshOpts) makes
// the client notice a dead one, and redial starts it again, backing off from 2s to
// 30s, for up to redialFor. Then the failure box takes over.
const redialFor = 5 * time.Minute

// redial runs each tick: it notices lost panes, retries them, and re-adds a
// server's port forwards once its connection is back.
func (m *model) redial() {
	now := time.Now()
	for _, t := range m.tabs {
		for _, p := range slices.Clone(t.panes) {
			if p.closed {
				continue
			}
			_, exited := p.term.State()
			switch {
			case p.lost.IsZero() && p.term.Lost():
				p.lost, p.retry, p.tries = now, now, 0
				for f := range m.fwdOn {
					if f.host == p.term.Host && f.dir == p.dir {
						delete(m.fwdOn, f) // they went with the connection
					}
				}
				p.kept = p.term.Keep()
				p.kept.Text += "\n\n── connection lost at " + now.Format("15:04:05") + ", reconnecting ──"
			case p.lost.IsZero():
				continue
			case !exited && p.term.Connected():
				p.lost, p.kept = time.Time{}, paneState{}
				continue
			case now.Sub(p.lost) > redialFor:
				p.lost = time.Time{} // gives up: connState says "failed"
				continue
			}
			if exited && !now.Before(p.retry) {
				m.redialPane(t, p)
				p.tries++
				p.retry = now.Add(min(2*time.Second<<p.tries, 30*time.Second))
			}
		}
	}
}

// keepForwards adds forwards not on their server's connection yet (restored
// from the last session, or the link came back) once a pane there is up.
func (m *model) keepForwards() {
	for _, f := range m.forwards {
		if m.fwdOn[f] {
			continue
		}
		for _, t := range m.tabs {
			for _, p := range t.panes {
				if _, exited := p.term.State(); p.term.Host == f.host && p.dir == f.dir && !exited && p.term.Connected() && !m.fwdOn[f] {
					if m.fwdOn == nil {
						m.fwdOn = map[portFwd]bool{}
					}
					m.fwdOn[f] = true // once per connection, even if it fails (port taken)
					if err := f.run(false); err != nil {
						m.flash = "forward " + f.String() + ": " + err.Error()
					}
				}
			}
		}
	}
}

func (m *model) closeTab(i int) {
	t := m.tabs[i]
	for _, p := range t.panes {
		p.term.Close()
	}
	if t.claude != nil {
		t.claude.Close()
	}
	for _, a := range t.pending { // unblock tool calls waiting on it
		a.done <- approvalResult{}
	}
	hub.Lock()
	for _, k := range hub.tasks {
		if k.tab == t {
			delete(hub.tasks, k.id)
			hub.taskGen++
			post(k.from, peerEvent{Kind: "result", ID: k.id, Org: t.org, Text: "aborted: the user closed the session"})
		}
	}
	delete(hub.running, t)
	delete(hub.last, t)
	hub.Unlock()
	m.tabs = append(m.tabs[:i], m.tabs[i+1:]...)
	if i < m.active {
		m.active--
	}
	m.active = max(0, min(m.active, len(m.tabs)-1))
	if len(m.tabs) == 0 {
		m.group = ""
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	r, cmd := m.update(msg)
	mm := r.(model)
	mm.saveUI()
	mm.saveSession()
	return mm, cmd
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case redrawMsg:
		pending.Store(false)
		m.reap()
	case tickMsg:
		m.redial()
		m.keepForwards()
		m.reap()
		if m.restored && time.Now().Second()%10 == 0 {
			m.runHealth(time.Now())
		}
		return m, tick()
	case paneMsg:
		msg.done <- m.addPane(msg.tab, msg.n, true)
	case peerMsg:
		msg.reply <- m.peer(msg.req)
	case passwordMsg:
		return m, m.usePassword(msg)
	case questionMsg:
		m.asks = append(m.asks, msg.q)
	case approvalMsg:
		msg.tab.pending = append(msg.tab.pending, msg.a)
		if m.elsewhere(msg.tab) {
			desktopNotify("Approval needed · "+msg.tab.name, msg.a.cmd)
		}
	case alertsMsg:
		m.setAlerts(msg)
	case blockDoneMsg:
		if m.elsewhere(msg.tab) || msg.tab.term == nil || msg.tab.focused().name != msg.host {
			mark := "✓"
			if msg.block.Exit != 0 {
				mark = fmt.Sprintf("✗ exit %d", msg.block.Exit)
			}
			desktopNotify("Command finished · "+msg.host, fmt.Sprintf("%s %s (%s)", mark, msg.block.Cmd, msg.block.Dur.Round(time.Second)))
		}
	case followUpMsg:
		t := msg.tab
		if t.claude != nil && t.claude.Ask(msg.text) == nil {
			t.busy = true
		}
	case claudeMsg:
		t := msg.tab
		if msg.exited != nil && t.claude != msg.exited {
			return m, nil // a process desk closed (model switch, compact, clear): its end is no news
		}
		if msg.ctx > 0 {
			t.ctxTokens = msg.ctx
		}
		if msg.window > 0 {
			t.ctxWindow = msg.window
		}
		if c := msg.compacted; c != [2]int{} {
			t.ctxTokens = c[1]
			t.chat = append(t.chat, &chatEntry{who: "note", text: "context compacted: " + fmtNum(float64(c[0])) + " → " + fmtNum(float64(c[1])) + " tokens"})
		}
		if msg.done {
			t.compacting = false
		}
		if msg.tool != "" {
			t.chat = append(t.chat, &chatEntry{who: "tool", text: msg.tool, steps: msg.steps})
		}
		for _, b := range msg.blocked {
			if b.ToolName != "mcp__desk__run" || b.ToolInput.Command == "" {
				t.chat = append(t.chat, &chatEntry{who: "error", text: "blocked by Claude Code's classifier: " + b.ToolName})
				continue
			}
			a := &approval{cmd: b.ToolInput.Command, reason: b.ToolInput.Reason, why: "blocked by Claude Code's permission classifier",
				pane: b.ToolInput.Pane, blocked: true, done: make(chan approvalResult, 1)}
			t.pending = append(t.pending, a)
			if m.elsewhere(t) {
				desktopNotify("Approval needed · "+t.name, a.cmd)
			}
		}
		if msg.err != nil {
			t.chat = append(t.chat, &chatEntry{who: "error", text: msg.err.Error()})
		}
		if msg.session != "" {
			t.session = msg.session
		}
		if msg.exited != nil && t.claude == msg.exited {
			t.claude, t.session = nil, "" // a session that won't resume must not keep failing
			t.chat = append(t.chat, &chatEntry{who: "note", text: "Claude starts again with your next question (a new conversation)"})
		}
		if msg.text != "" {
			t.chat = append(t.chat, &chatEntry{who: "claude", text: msg.text})
			if s := suggestion(msg.text); s != "" {
				t.suggest = s
				if !strings.Contains(s, "\n") && t.term != nil && t.term.AtPrompt() {
					t.term.TypeForClaude(s) // attach it to the console, unrun
					t.suggest = ""
					t.chat = append(t.chat, &chatEntry{who: "note", text: "↳ typed at your prompt (not run) · esc → terminal, enter runs it"})
				}
			}
		}
		if msg.done && t.health && len(t.chat) > 0 {
			t.health = false
			desktopNotify("Daily check ready · "+t.org, t.chat[len(t.chat)-1].text)
		} else if msg.done && msg.exited == nil && m.elsewhere(t) && len(t.chat) > 0 {
			if last := t.chat[len(t.chat)-1]; last.who == "claude" {
				desktopNotify("Claude answered · "+t.name, last.text)
			}
		}
		if msg.done {
			t.busy = false
			if len(t.asked) > 0 { // questions typed meanwhile go before peer tasks
				q := t.asked[0]
				t.asked = t.asked[1:]
				if err := m.send(t, q, &chatEntry{who: "you", text: q}); err != nil {
					t.chat = append(t.chat, &chatEntry{who: "error", text: err.Error()})
				}
			}
			m.turnEnded(t)
		}
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

// key runs the keys the window presses (driver.key) in the model, so the logic stays in one place. Typing itself goes from the window
// straight to the terminals and editors.
func (m model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.flash = ""
	t := m.tab()
	k := msg.String()
	if len(m.asks) > 0 {
		if q := m.asks[0]; k == "enter" || k == "esc" {
			if k == "enter" {
				q.done <- q.text
			}
			close(q.done)
			m.asks = m.asks[1:]
		}
		return m, nil
	}
	if at := m.approvalTab(); at != nil && !strings.HasPrefix(k, "alt+") {
		return m, m.answer(at, msg)
	}
	if m.pal { // the palette is over everything, a pane's connection box too: its keys are its own
		return m, m.palKey(msg)
	}
	if t != nil && t.connState() != "" && !strings.HasPrefix(k, "alt+") {
		switch k {
		case "w", "enter":
			if t.connState() == "timeout" {
				t.focused().waitUntil = time.Now().Add(connectWait)
			}
		case "c", "esc", "ctrl+w":
			t.closePane(t.focused())
			m.reap()
		case "r":
			if s := t.connState(); s == "failed" || s == "reconnecting" {
				m.reconnect(t)
			}
		case "a":
			if c := hostKeyChangeOf(t.term.Err()); c != nil && t.connState() == "failed" {
				if err := sshc.AcceptNewKey(t.term.Err()); err != nil {
					m.errText = "accepting the new host key: " + err.Error()
				} else {
					m.reconnect(t)
				}
			}
		case "f":
			if fixes := legacyFixes(t.term.Err()); len(fixes) > 0 && t.connState() == "failed" {
				if err := applyFixes(hostFile(t.focused().dir, t.term.Host), t.term.Host, fixes); err != nil {
					m.errText = err.Error()
				} else {
					m.reconnect(t)
				}
			}
		}
		return m, nil
	}
	switch {
	case k == "alt+p" && t != nil && t.term != nil:
		return m, serverPasswordCmd(t.focused())
	case k == "ctrl+y" && t != nil && t.term != nil && t.suggest != "":
		t.term.TypeForClaude(t.suggest)
		t.suggest = ""
	case k == "ctrl+t" && t != nil && t.term != nil:
		if p := m.addPane(t, t.focused().node(), false); p != nil {
			t.focusPane(len(t.panes) - 1)
		}
	case k == "ctrl+w" && t != nil && (len(t.panes) > 1 || t.orgOnly && len(t.panes) == 1):
		t.closePane(t.focused())
		m.dropExited(t)
	case k == "ctrl+w" && t != nil:
		m.closeTab(m.active)
	case k == "ctrl+s" && t != nil:
		t.pinScreen = !t.pinScreen
	case k == "enter" && m.group != "":
		m.groupAsk()
	}
	return m, nil
}

type followUpMsg struct {
	tab  *Tab
	text string
}

// sideEnter opens tree row m.cursor: a host's session, the org's Claude, or
// a group's view.
func (m *model) sideEnter() {
	if n := m.tree[m.cursor]; n.host != "" {
		m.open(n)
	} else if n.depth == 0 && n.dir != "" {
		m.openOrg(n, "")
	} else if n.depth == 1 {
		m.group = m.groupDir(m.cursor)
	}
}

// approvalTab is the tab whose approval prompt is on screen: the active one,
// or in group view any session of the group.
func (m *model) approvalTab() *Tab {
	if t := m.tab(); t != nil && len(t.pending) > 0 {
		return t
	}
	for _, t := range m.groupTabs() {
		if len(t.pending) > 0 {
			return t
		}
	}
	return nil
}

// groupTabs are the open sessions shown in group view.
func (m *model) groupTabs() []*Tab {
	var ts []*Tab
	for _, t := range m.tabs {
		if m.group != "" && groupKey(t.dir, t.group) == m.group {
			ts = append(ts, t)
		}
	}
	return ts
}

// groupDir is the groupKey of the group row i of the sidebar (from its
// first host).
func (m *model) groupDir(i int) string {
	for _, n := range m.tree[i+1:] {
		if n.depth < 2 {
			break
		}
		return groupKey(n.dir, n.group)
	}
	return ""
}

// groupAsk asks every session of the group view m.input, or with nothing
// typed goes back to the active tab.
func (m *model) groupAsk() {
	if strings.TrimSpace(m.input) == "" {
		m.group = ""
		return
	}
	active, q := m.active, m.input
	for i, t := range m.tabs {
		if groupKey(t.dir, t.group) == m.group {
			m.active, m.input = i, q
			m.ask()
		}
	}
	m.active, m.input = active, ""
}

// palItem is one row of the ^k palette.
type palItem struct {
	kind, label, hint string
	tab               int // kind "session"
	theme             string
	n                 node // kind "host"
	dir               string
	runbook           runbook     // kind "runbooks"
	alert             nagiosAlert // kind "alerts"
	fwd               portFwd     // a port forward to add or stop
}

func (m *model) palItems() []palItem {
	var all []palItem
	for _, a := range m.allAlerts() {
		all = append(all, palItem{kind: "alerts", label: "investigate " + a.State + " " + a.name(), hint: a.Org + " · " + firstLine(a.Output, 60), alert: a})
	}
	for i, t := range m.tabs {
		hint := "tab " + fmt.Sprint(i+1)
		if len(t.pending) > 0 {
			hint = "▲ needs approval"
		}
		label := t.org + " › " + t.group + " › " + t.name
		if t.orgOnly {
			label = t.org + " › Claude"
		}
		all = append(all, palItem{kind: "sessions", label: label, hint: hint, tab: i})
	}
	if t := m.tab(); t != nil {
		for _, rb := range runbooks(t) {
			all = append(all, palItem{kind: "runbooks", label: rb.title, hint: "run with Claude · " + filepath.Base(rb.path), runbook: rb})
		}
	}
	for _, n := range m.tree {
		if n.depth == 0 && n.dir != "" {
			all = append(all, palItem{kind: "orgs", label: n.label, hint: "Claude for the org", n: n})
		}
	}
	for i, n := range m.tree {
		if n.depth == 1 && m.groupDir(i) != "" {
			org := ""
			for j := i; j >= 0; j-- {
				if m.tree[j].depth == 0 {
					org = m.tree[j].label
					break
				}
			}
			all = append(all, palItem{kind: "groups", label: org + " › " + n.label, hint: "group view", dir: m.groupDir(i)})
		}
	}
	for _, n := range m.tree {
		if n.host != "" {
			all = append(all, palItem{kind: "hosts", label: n.org + " › " + n.group + " › " + n.label, hint: "open ssh tab", n: n})
		}
	}
	if strings.TrimSpace(m.palQ) == "" {
		return all
	}
	all = append(all, palItem{kind: "actions", label: "reload config", hint: "desk.toml files and the host tree"})
	for _, f := range m.forwards {
		all = append(all, palItem{kind: "actions", label: "stop forward " + f.String(), hint: "-" + f.kind + " " + f.spec, fwd: f})
	}
	if q := strings.TrimSpace(m.palQ); len(m.tabs) > 0 {
		if strings.HasPrefix(q, "f") {
			all = append(all, palItem{kind: "actions", label: "forward PORT[:HOST]:PORT", hint: "type e.g. forward 5432 or forward 9000:db:5432"})
		}
		if strings.HasPrefix(q, "r") {
			all = append(all, palItem{kind: "actions", label: "rforward PORT[:HOST]:PORT", hint: "a server port to this machine: rforward 3000"})
		}
		if strings.HasPrefix(q, "so") {
			all = append(all, palItem{kind: "actions", label: "socks PORT", hint: "a SOCKS proxy through the server: socks 1080"})
		}
	}
	if len(m.tabs) > 0 {
		all = append(all, palItem{kind: "actions", label: "copy server password", hint: "alt+p"},
			palItem{kind: "actions", label: "copy ssh command", hint: "this server, as desk reaches it, for another terminal"},
			palItem{kind: "actions", label: "broadcast typing to all panes", hint: "on/off for this tab"},
			palItem{kind: "actions", label: "draft a postmortem with Claude", hint: "from this session"},
			palItem{kind: "actions", label: "export session report", hint: "markdown: conversation, commands, approvals"})
		if p := m.tab().focused(); p != nil && p.term.Host != "local" {
			if kind, spec, ok := parseForward(m.palQ); ok {
				f := portFwd{host: p.term.Host, dir: p.dir, name: p.name, kind: kind, spec: spec}
				all = append(all, palItem{kind: "actions", label: strings.TrimSpace(m.palQ), hint: f.String(), fwd: f})
			}
		}
		for _, md := range append(claudeModels, "default") {
			all = append(all, palItem{kind: "actions", label: "model: " + md, hint: "Claude model for this tab"})
		}
		all = append(all, palItem{kind: "actions", label: "compact Claude's context", hint: "summarize the conversation so far (/compact) · " + ctxText(m.tab())},
			palItem{kind: "actions", label: "clear Claude's context", hint: "a new conversation from the next question; the chat stays"})
		if cameraOn() {
			all = append(all, palItem{kind: "actions", label: "stop the terminal camera", hint: "remove the \"desk terminal\" camera"})
		} else if cameraBuilt {
			all = append(all, palItem{kind: "actions", label: "share the terminal as a camera", hint: "a webcam, \"desk terminal\", showing the focused pane"})
		}
		if shareOn() {
			all = append(all, palItem{kind: "actions", label: "stop sharing the terminal as a screen", hint: "close the share window"})
		} else if cameraBuilt {
			all = append(all, palItem{kind: "actions", label: "share the terminal as a screen", hint: "a window (on Hyprland a screen, DESK-SHARE) showing the focused pane"})
		}
	}
	names := slices.Sorted(maps.Keys(themes)) // only with a query: "theme nord"
	for _, n := range names {
		hint := "dark theme"
		if themes[n].Light {
			hint = "light theme"
		}
		if n == themeName {
			hint = "current theme"
		}
		all = append(all, palItem{kind: "themes", label: "theme: " + n, hint: hint, theme: n})
	}
	var out []palItem
	var scores []int
	for _, it := range all {
		if s, ok := fzfScore(m.palQ, it.label); ok {
			out = append(out, it)
			scores = append(scores, s)
		}
	}
	idx := make([]int, len(out))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int { return scores[b] - scores[a] })
	sorted := make([]palItem, len(out))
	for i, j := range idx {
		sorted[i] = out[j]
	}
	return sorted
}

// palKey runs palette item m.palSel on enter (the window keeps the query
// and the selection itself).
func (m *model) palKey(msg tea.KeyPressMsg) tea.Cmd {
	items := m.palItems()
	switch msg.String() {
	case "esc":
		m.pal = false
	case "enter":
		if m.palSel >= len(items) {
			return nil
		}
		m.pal = false
		switch it := items[m.palSel]; it.kind {
		case "sessions":
			m.active, m.group = it.tab, ""
		case "groups":
			m.group = it.dir
		case "hosts":
			m.group = ""
			m.open(it.n)
		case "orgs":
			m.group = ""
			m.openOrg(it.n, "")
		case "runbooks":
			m.runRunbook(it.runbook)
		case "alerts":
			m.investigate(it.alert)
		case "actions":
			t := m.tab()
			switch {
			case it.fwd.spec != "" && strings.HasPrefix(it.label, "stop forward "):
				if err := it.fwd.run(true); err != nil && m.fwdOn[it.fwd] {
					m.flash = "stop forward: " + err.Error()
				} else {
					m.flash = "stopped " + it.fwd.String()
				}
				m.forwards = slices.DeleteFunc(m.forwards, func(f portFwd) bool { return f == it.fwd })
				delete(m.fwdOn, it.fwd)
			case it.fwd.spec != "":
				if err := it.fwd.run(false); err != nil {
					m.flash = "forward: " + err.Error()
				} else if !slices.Contains(m.forwards, it.fwd) {
					if m.fwdOn == nil {
						m.fwdOn = map[portFwd]bool{}
					}
					m.forwards, m.fwdOn[it.fwd] = append(m.forwards, it.fwd), true
					m.flash = "forwarding " + it.fwd.String()
				}
			case it.label == "forward PORT[:HOST]:PORT", it.label == "rforward PORT[:HOST]:PORT", it.label == "socks PORT":
				m.flash = "type the ports: forward 5432, rforward 3000, socks 1080, or forward 9000:db.internal:5432"
			case it.label == "reload config":
				m.reloadConfig()
			case it.label == "broadcast typing to all panes":
				m.toggleBroadcast(t)
			case it.label == "draft a postmortem with Claude" && t != nil:
				if t.busy {
					m.flash = "Claude is busy"
				} else if err := m.send(t, postmortemPrompt, &chatEntry{who: "you", text: "Draft a postmortem of this session"}); err != nil {
					m.flash = err.Error()
				}
			case strings.HasPrefix(it.label, "model: ") && t != nil:
				md := strings.TrimPrefix(it.label, "model: ")
				m.setModel(t, map[bool]string{true: "", false: md}[md == "default"])
			case it.label == "compact Claude's context" && t != nil:
				m.compactContext(t)
			case it.label == "clear Claude's context" && t != nil:
				m.clearContext(t)
			case it.label == "share the terminal as a camera" || it.label == "stop the terminal camera":
				m.flash = cameraToggle()
			case it.label == "share the terminal as a screen" || it.label == "stop sharing the terminal as a screen":
				m.flash = shareToggle()
			case it.label == "export session report" && t != nil:
				if p, err := saveReport(t); err != nil {
					m.flash = err.Error()
				} else {
					m.flash = "report saved: " + p
					openLocal(p)
				}
			case it.label == "copy ssh command" && t != nil && t.term != nil:
				if p := t.focused(); p.term.Host != "local" {
					m.flash = "ssh command copied"
					return tea.SetClipboard(sshCommand(p.term.Host, p.dir))
				}
			case it.label == "copy server password" && t != nil && t.term != nil:
				return serverPasswordCmd(t.focused())
			}
		case "themes":
			setTheme(it.theme)
			for _, t := range m.tabs {
				for _, e := range t.chat {
					e.mdW = 0 // redraw replies in the new colors
				}
			}
			m.flash = "theme: " + it.theme
		}
	}
	return nil
}

// answer handles keys while a command waits for approval: y allow, a always,
// e edit, n deny; enter takes an edit (a.edit, set by the window). A call the
// classifier blocked has already returned to Claude, so desk runs it now and
// tells Claude the result.
func (m *model) answer(t *Tab, msg tea.KeyPressMsg) tea.Cmd {
	a := t.pending[0]
	k := msg.String()
	if a.editing {
		switch k {
		case "enter":
			a.cmd, a.editing = a.edit, false
		case "esc":
			a.editing = false
		}
		return nil
	}
	var res approvalResult
	switch k {
	case "y", "enter":
		res = approvalResult{ok: true, cmd: a.cmd}
	case "a":
		if a.secret {
			return nil
		}
		res = approvalResult{ok: true, cmd: a.cmd}
		always.add(t.org, allowKey(a.cmd, a.keys))
	case "e":
		if a.secret {
			return nil
		}
		a.edit, a.editing = a.cmd, true
		return nil
	case "n", "esc":
		res = approvalResult{}
	default:
		return nil
	}
	t.pending = t.pending[1:]
	a.done <- res
	verdict, logged := "✓ approved", "approved"
	if k == "a" {
		verdict, logged = "✓ always allowed (this org)", "always"
	}
	if !res.ok {
		verdict, logged = "✗ denied", "denied"
	}
	audit(auditEntry{Org: t.org, Group: t.group, Host: t.paneName(a.pane), Event: "approval", By: "you", Cmd: a.cmd, Verdict: logged})
	t.chat = append(t.chat, &chatEntry{who: "note", text: verdict + ": " + a.cmd})
	if !a.blocked {
		return nil
	}
	if !res.ok {
		return func() tea.Msg {
			return followUpMsg{t, "(desk) The permission classifier blocked `" + a.cmd + "` and the user also declined it. Don't retry; ask them."}
		}
	}
	p, err := t.pane(a.pane)
	return func() tea.Msg {
		if err != nil {
			return followUpMsg{t, "(desk) The permission classifier blocked `" + res.cmd + "`; the user approved it but desk could not run it: " + err.Error()}
		}
		r := p.term.Run(res.cmd, 60*time.Second)
		return followUpMsg{t, "(desk) The permission classifier blocked `" + res.cmd + "`, but the user approved it and desk ran it:\n" + redact(r.Text)}
	}
}

// passwordMsg brings a server password fetched off the UI goroutine.
type passwordMsg struct {
	term *Term
	pw   string
	err  error
}

// serverPasswordCmd fetches the pane's server password (the vault may ask for
// its passphrase in the ask sheet, so not on the UI goroutine).
func serverPasswordCmd(p *Pane) tea.Cmd {
	return func() tea.Msg {
		pw, err := serverPassword(p.dir, p.password)
		return passwordMsg{p.term, pw, err}
	}
}

// usePassword types the password into a sudo prompt that is still waiting,
// or else copies it to the clipboard.
func (m *model) usePassword(msg passwordMsg) tea.Cmd {
	switch {
	case msg.err != nil:
		m.flash = "! " + msg.err.Error()
		return nil
	case func() bool { _, ok := msg.term.SudoPrompt(); return ok }():
		msg.term.TypeSecret(msg.pw) // checked again now: never into a shell
		m.flash = "✓ sudo password sent"
		return nil
	}
	m.flash = "⧉ server password copied"
	return tea.SetClipboard(msg.pw)
}

// newTab adds an empty session of org working in dir (the org's infra dir)
// and makes it active.
func (m *model) newTab(org, group, dir, name string) *Tab {
	cfg, err := orgFor(dir)
	if err != nil {
		m.errText = err.Error()
		cfg = &orgConfig{}
	}
	home, _ := os.UserHomeDir()
	profile, _, _ := profileFor(home, dir)
	account := claude.AccountName(home, profile) // ponytail: read per new tab; cache by profile if .claude.json reads show up
	var hosts []node
	for _, n := range m.tree {
		if n.host != "" && n.org == org {
			hosts = append(hosts, n)
		}
	}
	t := &Tab{org: org, group: group, dir: dir, account: account, name: name, hosts: hosts, orgCfg: cfg,
		pinned: map[int]bool{}, lastCtx: map[int]bool{}, opened: time.Now()}
	m.tabs = append(m.tabs, t)
	m.active = len(m.tabs) - 1
	return t
}

// open starts a session on host n.
func (m *model) open(n node) {
	t := m.newTab(n.org, n.group, n.dir, n.label)
	if m.addPane(t, n, false) == nil {
		m.closeTab(m.active)
	}
}

// openOrg starts (or goes back to) the org's Claude-only session: no server
// yet, Claude connects to the ones the user asks for. card "" is the org's
// own; a card's key is that card's, with a Claude of its own: two tickets'
// server work never share a session.
func (m *model) openOrg(n node, card string) {
	for i, t := range m.tabs {
		if t.orgOnly && t.dir == n.dir && t.card == card {
			m.active = i
			return
		}
	}
	name := n.label
	if card != "" {
		name += " · " + card
	}
	t := m.newTab(n.org, "", n.dir, name)
	t.orgOnly, t.card = true, card
}

func (m *model) ask() {
	t, q := m.tab(), strings.TrimSpace(m.input)
	if t == nil || q == "" {
		return
	}
	if t.busy { // sent when the turn ends, with the terminal as it is then
		t.asked = append(t.asked, q)
		t.chat = append(t.chat, &chatEntry{who: "note", text: "queued: " + q})
		m.input = ""
		return
	}
	if err := m.send(t, q, &chatEntry{who: "you", text: q}); err != nil {
		t.chat = append(t.chat, &chatEntry{who: "error", text: err.Error()})
		return
	}
	t.pinned, t.suggest, t.pinScreen = map[int]bool{}, "", false
	m.input = ""
}

// reloadConfig rereads the orgs' configs and the host tree; open tabs keep
// their panes and pick up their org's new config. ponytail: alert pollers
// keep the config they started with until desk restarts.
func (m *model) reloadConfig() {
	reloadOrgs()
	if m.root != "" {
		m.tree = loadTree(m.root)
		m.cursor = min(m.cursor, max(0, len(m.tree)-1))
	}
	for _, t := range m.tabs {
		cfg, err := orgFor(t.dir)
		if err != nil {
			m.errText = err.Error()
			continue
		}
		t.orgCfg = cfg
		t.hosts = nil
		for _, n := range m.tree {
			if n.host != "" && n.org == t.org {
				t.hosts = append(t.hosts, n)
			}
		}
	}
	m.flash = "config reloaded"
}

// runRunbook hands rb to the current tab's Claude.
func (m *model) runRunbook(rb runbook) {
	t := m.tab()
	if t == nil {
		return
	}
	prompt, err := runbookPrompt(rb)
	if err == nil {
		if t.busy {
			err = fmt.Errorf("Claude is busy; start the runbook when it's done (or stop it)")
		} else {
			err = m.send(t, prompt, &chatEntry{who: "you", text: "▶ runbook: " + rb.title})
		}
	}
	if err != nil {
		t.chat = append(t.chat, &chatEntry{who: "error", text: err.Error()})
	}
}

// stop interrupts t's turn, drops the questions queued behind it and denies
// the commands it left waiting for approval, so none runs after the stop.
func (m *model) stop(t *Tab) {
	if t == nil || !t.busy || t.claude == nil {
		return
	}
	if err := t.claude.Interrupt(); err != nil {
		t.chat = append(t.chat, &chatEntry{who: "error", text: err.Error()})
		return
	}
	for _, a := range t.pending {
		a.done <- approvalResult{note: " (they stopped the turn)"}
		audit(auditEntry{Org: t.org, Group: t.group, Host: t.paneName(a.pane), Event: "approval", By: "you", Cmd: a.cmd, Verdict: "stopped"})
	}
	t.pending, t.asked = nil, nil
	t.chat = append(t.chat, &chatEntry{who: "note", text: "■ stopped"})
}

// send gives q to t's Claude (starting it if needed) with the terminal
// context, and shows e in the chat.
func (m *model) send(t *Tab, q string, e *chatEntry) error {
	if t.claude == nil {
		where := "" // org session
		if p := t.focused(); !t.orgOnly && p != nil {
			where = p.hostLabel()
		}
		c, err := StartClaude(t.dir, where, func(cm claudeMsg) { dispatch(cm) }, t)
		if err != nil {
			return err
		}
		t.claude = c
		t.mu.Lock()
		t.notesSent = nil // a new process knows nothing yet
		t.mu.Unlock()
	}
	var blocks, ctx []Block
	var scr *Screen
	prompt := q
	if p := t.focused(); p != nil {
		blocks = t.term.Blocks()
		ctx = referenced(q, blocks, t.pinned)
		if t.pinScreen || t.term.Live() || refScreen.MatchString(q) {
			s := t.term.Screen()
			scr = &s
		}
		prompt = buildPrompt(q, p.name, blocks, ctx, scr)
	}
	if len(t.panes) > 1 || t.orgOnly {
		t.mu.Lock()
		panes := "<panes>none open</panes>\n"
		if len(t.panes) > 0 {
			panes = fmt.Sprintf("<panes>%s</panes>\nBlocks and screen are from pane %d.\n", t.describePanes(), t.panes[t.cur].id)
		}
		prompt = panes + prompt
		t.mu.Unlock()
	}
	if len(t.peerCtx) > 0 {
		prompt = strings.Join(t.peerCtx, "\n") + "\n" + prompt
	}
	t.mu.Lock()
	var servers []string
	for _, p := range t.panes {
		servers = append(servers, p.name)
	}
	t.mu.Unlock()
	for _, s := range servers {
		prompt = t.notesFor(s) + prompt
	}
	prompt = fmt.Sprintf("(chat panel: %d columns)\n", chatWidth) + redact(prompt)
	if err := t.claude.Ask(prompt); err != nil {
		return err
	}
	t.peerCtx = nil
	e.screen = scr != nil
	t.lastCtx = map[int]bool{}
	for _, b := range ctx {
		e.ctx = append(e.ctx, b.N)
		e.lines += strings.Count(b.Output, "\n") + 2
		t.lastCtx[b.N] = true
	}
	t.chat = append(t.chat, e)
	t.busy = true
	return nil
}
