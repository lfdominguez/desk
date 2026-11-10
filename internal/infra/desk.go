package infra

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
)

// Workspace is desk's infrastructure workspace: the shared SSH
// terminals, the infra Claude of each tab, approvals, the palette and the
// directory split, for every organization with a servers file (servers.go).
//
// Threading: one model goroutine owns the sessions (the model loop); the window
// side (Tree, Layout, Shortcut, Open*, TogglePin, ActiveOrg) runs on desk's
// UI goroutine only, reading the model's latest snapshot. invalidate is
// called from any goroutine (terminal output, model changes) and must only
// ask the window for a frame (app.Window.Invalidate). There is one
// Workspace per process: the model loop, peers and SSH connections are global.
type Workspace struct {
	u    *ui
	sock string // the peer socket, removed by Close

	tasks    []Task // Tasks' last answer, as of hub.taskGen tasksGen (hub's lock)
	tasksGen int
}

// State is a host's (or a folded org's) dot in the sidebar.
type State string

const (
	Idle      State = "idle"      // no session on it
	Connected State = "connected" // a pane is up
	Working   State = "working"   // its tab's Claude is on a turn
	Waiting   State = "waiting"   // connecting, or a command waits for approval
	Alert     State = "alert"     // a Nagios problem on the host
)

// Org is an organization with infra: its groups, and its worst state.
type Org struct {
	Name   string
	State  State
	Claude bool // its org-wide Claude session (OpenOrg) is open
	Groups []Group
}

// Group is a group of the org's servers file (Name its full path, "eu/web";
// "servers" for hosts outside any): its hosts. A servers file that failed
// shows as a group named after the error, with no hosts.
type Group struct {
	Name  string
	Hosts []Host
}

// Host is one server. Meta is what the sidebar prints on its right:
// the address, "approve", "2 panes" or the alert.
type Host struct {
	Name, Addr, Meta string
	State            State
	Open             bool // it has a session
	Pinned           bool
}

// New starts the workspace for the organizations under root (desk's
// orgs.Root): the host tree, the saved layout and theme, the last session
// (tabs, panes, chats) reopened, the peer socket, Nagios alert polling and
// the model loop.
func New(root string, invalidate func()) *Workspace {
	m := model{tree: loadTree(root), root: root}
	if b, err := os.ReadFile(uiPath()); err == nil && json.Unmarshal(b, &m.saved) == nil {
		m.guiLayout = m.saved.GUI
		setTheme(m.saved.Theme)
	}
	for m.cursor < len(m.tree)-1 && m.tree[m.cursor].host == "" {
		m.cursor++
	}
	u, sock := start(m, invalidate)
	return &Workspace{u: u, sock: sock, tasksGen: -1}
}

// Tree is the organizations, groups and hosts for desk's sidebar, as of the
// model's latest snapshot. UI goroutine.
func (w *Workspace) Tree() []Org {
	u := w.u
	u.st = u.d.state.Load()
	if u.treeOf == u.st {
		return u.tree // desk asks every frame; a snapshot and the pins change far less
	}
	u.treeOf, u.tree = u.st, w.tree()
	return u.tree
}

func (w *Workspace) tree() []Org {
	u := w.u
	var out []Org
	rank := map[State]int{Alert: 4, Waiting: 3, Working: 2, Connected: 1}
	for i, n := range u.st.Tree {
		switch {
		case n.Depth == 0:
			o := Org{Name: n.Org, State: Idle}
			for _, t := range u.st.Tabs {
				o.Claude = o.Claude || t.OrgOnly && t.Org == n.Org
			}
			out = append(out, o)
		case len(out) == 0:
		case n.Depth == 1:
			o := &out[len(out)-1]
			o.Groups = append(o.Groups, Group{Name: n.Label})
		case n.Host != "" && len(out[len(out)-1].Groups) > 0:
			o := &out[len(out)-1]
			g := &o.Groups[len(o.Groups)-1]
			h := hostOf(u.hostRow(i, n))
			h.Pinned = u.pins[hostKey(n)] || n.Pin // ponytail: a servers file's pin can't be unpinned from the UI
			g.Hosts = append(g.Hosts, h)
			if rank[h.State] > rank[o.State] {
				o.State = h.State
			}
		}
	}
	return out
}

// hostOf reads a sidebar row's dot as a State.
func hostOf(r sideRow) Host {
	h := Host{Name: r.label, Meta: r.meta, Open: r.open, State: Idle}
	switch {
	case r.dot == cRed:
		h.State = Alert
	case r.wait || r.dot == cOrange:
		h.State = Waiting
	case r.dot == cPurple:
		h.State = Working
	case r.dot == cGreen:
		h.State = Connected
	}
	return h
}

// OpenHost goes to the session on host (as Tree names it), opening one if
// there is none. It reports whether the host exists. UI goroutine.
func (w *Workspace) OpenHost(org, group, host string) bool {
	u := w.u
	u.st = u.d.state.Load()
	for i, n := range u.st.Tree {
		if n.Host != "" && n.Org == org && n.Group == group && n.Label == host {
			u.openHost(i)
			return true
		}
	}
	return false
}

// OpenOrg goes to org's Claude session (Claude alone, connecting to the
// servers it's asked about), opening it if needed. UI goroutine.
func (w *Workspace) OpenOrg(org string) bool {
	u := w.u
	u.st = u.d.state.Load()
	for i, n := range u.st.Tree {
		if n.Depth == 0 && n.Org == org {
			u.d.open(i)
			u.wantTerm, u.keyTerm = false, nil
			return true
		}
	}
	return false
}

// OpenCard goes to card's own infra session in org (the one its "desk peer"
// requests went to: its own Claude, apart from the org's and other cards'),
// opening it if needed. UI goroutine.
func (w *Workspace) OpenCard(org, card string) bool {
	u := w.u
	u.st = u.d.state.Load()
	for _, n := range u.st.Tree {
		if n.Depth == 0 && n.Org == org {
			u.d.act(func(m *model) {
				if t, err := m.peerTab(org, card); err == nil {
					m.focus, m.group = t, ""
				}
			})
			u.wantTerm, u.keyTerm = false, nil
			return true
		}
	}
	return false
}

// Gen counts the workspace's redraw requests. desk's frame compares it
// before and after drawing: one that came meanwhile had its Invalidate lost.
func (w *Workspace) Gen() uint64 { return w.u.gen.Load() }

// TogglePin pins host on top of the host list, or unpins it (kept in the
// state dir's pins file). UI goroutine.
func (w *Workspace) TogglePin(org, group, host string) {
	w.u.togglePin(hostKey(guiNode{Org: org, Group: group, Label: host}))
	w.u.inv()
}

// Layout draws org's workspace in the space given: its tabs, panes, the
// Claude panel, the directory split, and over them the approval, question
// and palette sheets and toasts. It handles the workspace's shortcuts
// (Ctrl+Shift+K…, ShortcutFilters) itself. UI goroutine.
func (w *Workspace) Layout(gtx layout.Context, org string) layout.Dimensions {
	return w.u.frame(gtx, org)
}

// ActiveOrg is the org of the active tab (or the group view), "" none. A
// tab picked in the palette, or opened by a peer or the daily health check,
// may be another org's than the one Layout draws: desk shows that org then.
// UI goroutine.
func (w *Workspace) ActiveOrg() string {
	u := w.u
	if g := u.groupOrg(); g != "" {
		return g
	}
	if t := u.activeTab(); t != nil {
		return t.Org
	}
	return ""
}

// OnInvestigate has the palette's investigate items call f (service "": the
// host itself) instead of asking the org's Claude tab: desk makes each
// investigation a card. f runs on the workspace's goroutine. Any goroutine.
func (w *Workspace) OnInvestigate(f func(org, host, service, state, output string, since time.Time)) {
	w.u.d.act(func(m *model) { m.onInvestigate = f })
}

// Task is a peer's task ("desk peer do") an org's infra Claude works on or
// has queued, until it reports it done.
type Task struct {
	ID              int
	Org, From, Text string
}

// Tasks is the open peer tasks, oldest first; desk's cards show theirs
// (From is the card's key). Any goroutine.
func (w *Workspace) Tasks() []Task {
	hub.Lock()
	defer hub.Unlock()
	if w.tasksGen == hub.taskGen {
		return w.tasks // desk asks every frame; tasks come and go far less
	}
	var ts []Task
	for _, k := range hub.tasks {
		ts = append(ts, Task{ID: k.id, Org: k.tab.org, From: k.from, Text: k.text})
	}
	slices.SortFunc(ts, func(a, b Task) int { return a.ID - b.ID })
	w.tasks, w.tasksGen = ts, hub.taskGen
	return ts
}

// SetShown tells the workspace whether desk draws it (calls Layout each
// frame). Hidden, terminal output asks desk's window for no frame; changes to
// the sessions (what Tree shows) still do. Shown is the default. Any goroutine.
func (w *Workspace) SetShown(on bool) { w.u.hidden.Store(!on) }

// ShortcutFilters are the keys Shortcut takes (Ctrl+Shift+letter on Linux,
// ⌘ on macOS), for a window-wide handler that sees them first.
func ShortcutFilters() []event.Filter { return shortcutFilters() }

// Shortcut runs the workspace's own key e (palette, new pane, Claude panel
// …); it reports whether e was one. Layout already reads these filters;
// call it only for events desk's own handler took. UI goroutine.
func (w *Workspace) Shortcut(e key.Event) bool { return w.u.shortcut(e) }

// SetFocused tells the workspace whether the user is looking at it (desk's
// window focused and the workspace on screen): away, approvals, answers and
// long commands notify on the desktop. Any goroutine.
// Reload rereads the organizations' configs and servers, as in desk's
// folders now.
func (w *Workspace) Reload() { w.u.d.act(func(m *model) { m.reloadConfig() }) }

func (w *Workspace) SetFocused(on bool) { w.u.d.act(func(m *model) { m.away = !on }) }

// Close saves the session (each pane's last output too) and stops the
// terminal share; the terminals end with desk. Any goroutine.
func (w *Workspace) Close() {
	saved := make(chan struct{})
	w.u.d.act(func(m *model) { m.sessionSaved = ""; m.saveSession(); close(saved) })
	select {
	case <-saved:
	case <-time.After(3 * time.Second):
	}
	shareQuit()
	if w.sock != "" {
		os.Remove(w.sock)
	}
}

// MCPMain is `desk mcp <socket> [groups…]`: the stdio MCP server a tab's
// Claude starts, forwarding its tool calls to the workspace's socket.
func MCPMain(args []string) {
	if len(args) == 0 {
		osExit(2)
	}
	mcpMain(args[0], strings.Join(args[1:], ","))
}

// PeerMain is `desk peer …`, for another Claude Code session to hand server
// work to the running desk; it returns the exit code.
func PeerMain(args []string) int { return peerMain(args) }

// osExit ends the process (a variable so tests can catch it).
var osExit = os.Exit
