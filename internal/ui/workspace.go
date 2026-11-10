package ui

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
)

// A card's workspace (`ws:<id>`): its projects' files beside a second
// Claude that reads them with the user and answers the card's main Claude.
// The view is workspace_view.go, the editor workspace_editor.go.

// wsState is a card's workspace as the window keeps it while desk runs.
type wsState struct {
	id     string               // its card
	open   map[string]bool      // folders expanded in the tree
	kids   map[string][]wsEntry // their entries, read when expanded
	tabs   []*wsTab             // the files open
	cur    int                  // the tab shown
	in     widget.Editor        // the composer
	chat   layout.List
	tree   layout.List
	tabBar layout.List // the tabs, scrolling sideways when they don't fit
	shown  int         // the tab tabBar last brought into view
	find   *wsFind     // the find bar over the editor, nil closed (workspace_find.go)
	proc   session     // its Claude: nil until first used
	from   []string    // who asked each turn queued, in order: "you" or "card"
	last   string      // the running turn's latest reply
	stale  bool        // the projects changed: the next question restarts it (resumed, with them)
}

// wsEntry is a file or folder in the tree.
type wsEntry struct {
	name, path string
	dir        bool
}

// wsKey is where the workspace Claude's conversation is kept among the chats.
func wsKey(id string) string { return "ws-" + id }

// wsOf is card id's workspace, made on first use with its own folder open,
// the files it had open (those still readable) and the tabs with changes
// not saved that the last quit kept.
func (u *ui) wsOf(id string) *wsState {
	if st := u.ws[id]; st != nil {
		return st
	}
	st := &wsState{id: id, open: map[string]bool{}, kids: map[string][]wsEntry{}}
	st.in.Submit = true
	st.chat.Axis, st.chat.ScrollToEnd, st.tree.Axis, st.shown = layout.Vertical, true, layout.Vertical, -1
	u.ws[id] = st
	if c := u.b.Card(id); c != nil {
		u.wsToggle(st, u.projects(c)[0])
		for _, p := range c.Tabs {
			if b, fi, err := readText(p); err == nil {
				u.wsAddTab(st, p, string(b), fi.ModTime(), fi.Mode().Perm())
			}
		}
	}
	u.wsRestore(id, st)
	u.wsKeepTabs(st)
	return st
}

// wsKeepTabs keeps st's open files on its card, to open again next time.
func (u *ui) wsKeepTabs(st *wsState) {
	c := u.b.Card(st.id)
	if c == nil {
		return
	}
	var ps []string
	for _, t := range st.tabs {
		ps = append(ps, t.path)
	}
	if !slices.Equal(ps, c.Tabs) {
		c.Tabs = ps
		u.save()
	}
}

// projects are card c's workspace folders: its own first, then those added.
func (u *ui) projects(c *kanban.Card) []string {
	return append([]string{orgs.Dir(u.root, c.Org, c.Dir)}, c.Projects...)
}

// wsRedraw marks a workspace change for sig, so the frame draws it again.
func (u *ui) wsRedraw() { u.wsGen++ }

// wsToggle expands folder dir in the tree (reading it) or collapses it.
func (u *ui) wsToggle(st *wsState, dir string) {
	u.wsRedraw()
	if st.open[dir] {
		delete(st.open, dir)
		delete(st.kids, dir) // read again when expanded: the folder may have changed
		return
	}
	st.open[dir] = true
	st.kids[dir] = readTree(dir)
}

// wsRefresh reads again the open folders above path (a file just written
// may be new to them), or every open folder for "".
func (u *ui) wsRefresh(st *wsState, path string) {
	for d := range st.open {
		if path == "" || strings.HasPrefix(path, d+string(filepath.Separator)) {
			st.kids[d] = readTree(d)
		}
	}
	u.wsRedraw()
}

// wsHidden are the folders the tree leaves out: no one reads them, and
// .worktrees holds whole copies of the repository.
var wsHidden = map[string]bool{".git": true, "node_modules": true, ".worktrees": true, "__pycache__": true, ".venv": true}

// readTree is folder dir's entries, folders first, without wsHidden's.
func readTree(dir string) []wsEntry {
	es, _ := os.ReadDir(dir)
	var dirs, files []wsEntry // each by name (ReadDir's order)
	for _, e := range es {
		if wsHidden[e.Name()] {
			continue
		}
		p, isDir := filepath.Join(dir, e.Name()), e.IsDir()
		if e.Type()&os.ModeSymlink != 0 { // followed: a linked folder reads like one
			fi, err := os.Stat(p)
			isDir = err == nil && fi.IsDir()
		}
		if isDir {
			dirs = append(dirs, wsEntry{e.Name(), p, true})
		} else {
			files = append(files, wsEntry{e.Name(), p, false})
		}
	}
	return append(dirs, files...)
}

// projectRoot is the project folder p belongs to: the nearest folder above
// it (or itself) with a .git, below home; else p.
func projectRoot(p, home string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%q is not an absolute path", p)
	}
	p = filepath.Clean(p)
	if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s is not a folder", p)
	}
	for d := p; d != home && filepath.Dir(d) != d; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d, nil
		}
	}
	return p, nil
}

// addProject adds folder dir to card id's workspace (the main Claude's
// add_project); the toast undoes it.
func (u *ui) addProject(id, dir string) (string, error) {
	c := u.b.Card(id)
	if c == nil {
		return "", errors.New("the card is gone from the board")
	}
	for _, p := range u.projects(c) {
		if dir == p || strings.HasPrefix(dir, p+string(filepath.Separator)) { // its tree shows it already
			return dir + " is in the workspace already.", nil
		}
	}
	c.Projects = append(c.Projects, dir)
	u.save()
	u.wsChanged(id)
	u.showUndo("Added "+filepath.Base(dir)+" to "+c.Key()+"'s workspace", func() { u.removeProject(id, dir) })
	return "Added " + dir + " to the card's workspace.", nil
}

// removeProject takes folder dir out of card id's workspace.
func (u *ui) removeProject(id, dir string) {
	if c := u.b.Card(id); c != nil {
		c.Projects = slices.DeleteFunc(c.Projects, func(p string) bool { return p == dir })
		u.save()
		u.wsChanged(id)
	}
}

// wsChanged marks card id's workspace Claude to restart with its new
// projects before its next question.
func (u *ui) wsChanged(id string) {
	if st := u.ws[id]; st != nil && st.proc != nil {
		st.stale = true
	}
	u.wsRedraw()
}

// wsStart starts card id's workspace Claude: the card's account, in its
// folder with the other projects added, tools that only read; its session
// kept on the card and resumed.
func (u *ui) wsStart(id string) (session, error) {
	c := u.b.Card(id)
	if c == nil {
		return nil, errors.New("the card is gone from the board")
	}
	acct := orgs.Account(u.root, c.Org)
	o := claude.Options{Home: u.home, Org: acct, Dir: u.projects(c)[0], Root: orgs.Dir(u.root, c.Org, ""), System: claude.Workspace(c.Key(), c.Title) + cardLang(c)}
	for _, p := range c.Projects {
		o.Args = append(o.Args, "--add-dir="+p) // = form: the flag is variadic
	}
	mcp, allow := infra.WorkspaceMCP(u.cardSock, id)
	if mcp != "" {
		o.Args = append(o.Args, "--mcp-config", mcp)
	}
	o.Args = append(o.Args, "--allowedTools="+strings.Join(append([]string{"Read", "Glob", "Grep", "Skill"}, allow...), ","),
		"--disallowedTools=Edit,Write,NotebookEdit,Bash")
	if c.WSSession == "" {
		c.WSSession = claude.NewSessionID()
		u.save()
	}
	if claude.SessionExists(u.home, acct, c.WSSession) {
		o.Resume = c.WSSession
	} else {
		o.New = c.WSSession
	}
	var p session
	p, err := startClaude(o, func(e claude.Event) { u.later(func() { u.wsEvent(id, p, e) }) })
	return p, err
}

// wsAsk hands prompt to card id's workspace Claude, starting it first;
// from is who asked ("you", "card"): the card's main Claude gets its answer.
func (u *ui) wsAsk(id, prompt, from string) error {
	st := u.wsOf(id)
	if st.stale && st.proc != nil && len(st.from) == 0 {
		st.proc.Close()
		st.proc, st.stale = nil, false
	}
	if st.proc == nil {
		p, err := u.wsStart(id)
		if err != nil {
			return err
		}
		st.proc = p
	}
	if err := st.proc.Send(prompt); err != nil {
		return err
	}
	st.from = append(st.from, from)
	u.wsRedraw()
	return nil
}

// wsAskCard is the main Claude's ask_workspace.
func (u *ui) wsAskCard(id, q string) error {
	if err := u.wsAsk(id, "The card's main Claude asks:\n"+q, "card"); err != nil {
		return err
	}
	u.say(wsKey(id), "main", q)
	return nil
}

// wsEvent applies one of card id's workspace Claude's events (p: that
// process; a replaced one's last events are dropped).
func (u *ui) wsEvent(id string, p session, e claude.Event) {
	st := u.ws[id]
	if st == nil || st.proc != p {
		return
	}
	u.wsRedraw()
	key := wsKey(id)
	switch e.Kind {
	case claude.KindInit:
		if c := u.b.Card(id); c != nil && e.Session != "" && c.WSSession != e.Session {
			c.WSSession = e.Session
			u.save()
		}
	case claude.KindText:
		if t := strings.TrimSpace(e.Text); t != "" && e.Parent == "" {
			u.say(key, "claude", t)
			st.last = t
		}
	case claude.KindTool:
		if e.Parent == "" {
			u.sayMsg(key, toolMsg(e))
		}
	case claude.KindAsk: // it only reads: anything it would ask to run is refused
		p.Respond(e.Ask.ID, false, nil, "The workspace Claude only reads: propose the change instead.")
		u.say(key, "tool", "refused "+e.Ask.Line)
	case claude.KindResult:
		who := ""
		if len(st.from) > 0 {
			who, st.from = st.from[0], st.from[1:]
		}
		if e.Error && e.Text != "" {
			u.say(key, "error", e.Text)
		}
		if who == "card" {
			u.wsTell(id, cmp.Or(st.last, "(no answer: "+cmp.Or(firstLine(e.Text), "it stopped")+")"))
		}
		st.last = ""
		if st.stale && len(st.from) == 0 {
			p.Close()
			st.proc, st.stale = nil, false
		}
	case claude.KindExit:
		st.proc, st.from, st.stale = nil, nil, false
		if e.Text != "" {
			u.say(key, "error", e.Text)
		}
	}
}

// wsTell brings the workspace Claude's words into card id's main session,
// as the infra Claude's come (cardTell): held while it can't take them.
func (u *ui) wsTell(id, text string) {
	if u.b.Card(id) == nil {
		return
	}
	u.say(id, "workspace", text)
	msg := "From the workspace Claude:\n" + text
	if u.terms[id] != nil || u.shellStop[id] != nil || u.asks[id] != nil {
		u.peerOut[id] += msg + "\n\n"
		return
	}
	u.deliver(id, msg, true)
}

// wsTool answers the workspace tools, off the UI goroutine: the main
// Claude's add_project and ask_workspace, the workspace Claude's (ws)
// suggest_to_card. done false: not one of them.
func (u *ui) wsTool(id string, ws bool, name string, raw json.RawMessage) (text string, ok, done bool) {
	var a struct {
		Path, Question, Text string
	}
	json.Unmarshal(raw, &a)
	mine := map[string]bool{"add_project": !ws, "ask_workspace": !ws, "suggest_to_card": ws}
	on, known := mine[name]
	switch {
	case !known:
		return "", false, false
	case !on:
		return "no tool " + name, false, true
	case name == "add_project":
		dir, err := projectRoot(a.Path, u.home)
		if err == nil {
			text, err = onBoard(u, func() (string, error) { return u.addProject(id, dir) })
		}
		if err != nil {
			return err.Error(), false, true
		}
		return text, true, true
	}
	q := strings.TrimSpace(a.Question + a.Text)
	if q == "" {
		return "nothing to send", false, true
	}
	_, err := onBoard(u, func() (string, error) {
		if name == "suggest_to_card" {
			u.wsTell(id, q)
			return "", nil
		}
		return "", u.wsAskCard(id, q)
	})
	if err != nil {
		return err.Error(), false, true
	}
	if name == "suggest_to_card" {
		return "Sent to the card's main Claude.", true, true
	}
	return "Asked. The workspace Claude's answer comes into this conversation as a message.", true, true
}
