package infra

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Sessions survive a restart: the open tabs, their panes' servers, text,
// blocks, directories and exported environment, the chat and Claude's session
// id go to $XDG_STATE_HOME/desk/infra/session.json whenever they change (a command
// finishes, cd, export) and on quit, and the next start reopens them: new
// shells that cd and export as before, the old text and blocks above; Claude
// resumes its conversation with --resume.

type savedSession struct {
	Saved    time.Time
	Active   int
	Tabs     []savedTab
	Forwards []savedFwd `json:",omitempty"`
}

type savedFwd struct{ Host, Dir, Name, Kind, Spec string }

type savedTab struct {
	Org, Group, Dir, Name string
	OrgOnly               bool
	Card                  string `json:",omitempty"` // the card whose server work the tab is
	Panes                 []savedPane
	Chat                  []savedChat
	Claude                string    // Claude Code session id, for --resume
	Opened                time.Time // the report reads the audit log from here
	Model                 string    `json:",omitempty"`
	Ctx, CtxWindow        int       `json:",omitempty"` // Claude's context use, for the meter until the next turn
}

type savedPane struct {
	Host, Dir, Label, Addr string
	State                  paneState `json:",omitempty"`
}

type savedChat struct {
	Who, Text string
	Steps     []step `json:",omitempty"`
}

const restoredNote = "— restored from "

// restoredLine is a restore's line in a pane's text when it is the last one.
var restoredLine = regexp.MustCompile(`\n*── restored from [^\n]* ──\s*$`)

// keptChat is how many chat entries per tab are saved.
const keptChat = 300

func sessionPath() string { return filepath.Join(stateDir(), "session.json") }

// sessionSig changes whenever something saveSession writes changes.
func (m *model) sessionSig() string {
	s := fmt.Sprint(m.active())
	for _, t := range m.tabs {
		s += fmt.Sprintf("|%p:%d:%d:%s:%s:%d", t, len(t.panes), len(t.chat), t.session, t.model, t.ctxTokens)
		t.mu.Lock()
		for _, p := range t.panes {
			s += fmt.Sprintf(",%p:%d", p.term, p.term.Rev())
		}
		t.mu.Unlock()
	}
	return s + fmt.Sprint(m.forwards)
}

// saveSession writes the session file if anything in it changed.
func (m *model) saveSession() {
	if !m.restored {
		return // don't overwrite the last session before it's reopened
	}
	sig := m.sessionSig()
	if sig == m.sessionSaved {
		return
	}
	force := m.sessionSaved == "" // Close: every pane's last output
	m.sessionSaved = sig
	kept := map[*Term]keptPane{}
	ss := savedSession{Saved: time.Now(), Active: m.active()}
	for _, f := range m.forwards {
		ss.Forwards = append(ss.Forwards, savedFwd{f.host, f.dir, f.name, f.kind, f.spec})
	}
	for _, t := range m.tabs {
		st := savedTab{Org: t.org, Group: t.group, Dir: t.dir, Name: t.name, OrgOnly: t.orgOnly, Card: t.card, Claude: t.session, Opened: t.opened, Model: t.model, Ctx: t.ctxTokens, CtxWindow: t.ctxWindow}
		t.mu.Lock()
		for _, p := range t.panes {
			ps := p.kept // the dropped connection's, not a failed retry's
			if p.lost.IsZero() {
				ps = m.keepPane(p.term, force, kept)
			}
			st.Panes = append(st.Panes, savedPane{Host: p.term.Host, Dir: p.dir, Label: p.name, Addr: p.addr, State: ps})
		}
		t.mu.Unlock()
		chat := t.chat
		if len(chat) > keptChat {
			chat = chat[len(chat)-keptChat:]
		}
		for _, e := range chat {
			st.Chat = append(st.Chat, savedChat{Who: e.who, Text: e.text, Steps: e.steps})
		}
		ss.Tabs = append(ss.Tabs, st)
	}
	m.kept = kept // panes gone since drop out
	b, _ := json.Marshal(ss)
	os.MkdirAll(stateDir(), 0o700)
	tmp := sessionPath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, sessionPath())
	}
}

// keptPane is a pane's Keep as of its Rev.
type keptPane struct {
	rev int
	st  paneState
}

// keepPane is term's state for the session file, Kept again only when its Rev
// moved (or force): a save comes whenever any pane's Rev moves, and Keep reads
// up to keptLines lines. The result goes into next.
// ponytail: a pane whose Rev stayed keeps its older text (a running command's
// newest output) until its next block or the save on Close.
func (m *model) keepPane(term *Term, force bool, next map[*Term]keptPane) paneState {
	rev := term.Rev()
	k, ok := m.kept[term]
	if !ok || force || k.rev != rev {
		k = keptPane{rev, term.Keep()}
	}
	next[term] = k
	return k.st
}

// restore reopens the last session's tabs, once, at startup.
func (m *model) restore() {
	if m.restored {
		return
	}
	m.restored = true
	b, err := os.ReadFile(sessionPath())
	var ss savedSession
	if err != nil || json.Unmarshal(b, &ss) != nil {
		return
	}
	for _, st := range ss.Tabs {
		t := m.newTab(st.Org, st.Group, movedInfra(st.Dir), st.Name)
		t.orgOnly, t.card, t.session, t.model = st.OrgOnly, st.Card, st.Claude, st.Model
		t.ctxTokens, t.ctxWindow = st.Ctx, st.CtxWindow
		if !st.Opened.IsZero() {
			t.opened = st.Opened
		}
		for _, c := range st.Chat {
			if c.Who == "note" && strings.HasPrefix(c.Text, restoredNote) {
				continue // one restore note, not one per restart
			}
			t.chat = append(t.chat, &chatEntry{who: c.Who, text: c.Text, steps: c.Steps})
		}
		if len(t.chat) > 0 {
			t.chat = append(t.chat, &chatEntry{who: "note", text: restoredNote + ss.Saved.Format("Jan 2 15:04") + "; new shells, Claude remembers the conversation —"})
		}
		for _, p := range st.Panes {
			ps := p.State
			if ps.Text != "" {
				ps.Text = strings.TrimRight(restoredLine.ReplaceAllString(ps.Text, ""), "\n") // nothing ran since the last restore: one line, not a stack
				ps.Text += "\n\n── restored from " + ss.Saved.Format("Jan 2 15:04") + " ──"
			}
			m.addPaneFrom(t, node{host: p.Host, dir: movedInfra(p.Dir), label: p.Label, addr: p.Addr, org: st.Org, group: st.Group}, false, ps)
		}
		if len(t.panes) == 0 && !t.orgOnly { // its server is gone from the tree, or won't start
			t.orgOnly = true
		}
	}
	for _, f := range ss.Forwards { // added once a pane on the server is up (keepForwards)
		m.forwards = append(m.forwards, portFwd{f.Host, movedInfra(f.Dir), f.Name, f.Kind, f.Spec})
	}
	m.activate(max(0, min(ss.Active, len(m.tabs)-1)))
	m.sessionSaved = m.sessionSig()
}
