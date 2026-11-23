package ui

import (
	"cmp"
	"encoding/json"
	"errors"
	"strings"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/company"
	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
)

// leadState is one organization's department lead (company.Department):
// its Claude, while it runs, and the cards whose briefs it is answering,
// first first.
type leadState struct {
	org  string
	dept company.Department
	proc session
	from []string // card ids, one per brief sent
	last string   // its last words this turn: the answer
}

// leadKey is the lead's chat: lead:<org>:<department>.
func leadKey(org, dept string) string { return "lead:" + org + ":" + dept }

// leadTool is a card's Claude's ask_lead, off the UI goroutine.
func (u *ui) leadTool(id string, raw json.RawMessage) (string, bool) {
	var b company.Brief
	json.Unmarshal(raw, &b)
	d, ok := company.DepartmentOf(b.Department)
	if !ok {
		return "no department " + strings.TrimSpace(b.Department) + ": engineering, security or finance", false
	}
	if strings.TrimSpace(b.Objective) == "" {
		return "say the objective: what you need from the " + d.Lead, false
	}
	if _, err := onBoard(u, func() (string, error) { return "", u.leadAsk(id, d, b) }); err != nil {
		return err.Error(), false
	}
	return "Briefed the " + d.Lead + ". Its answer comes into this conversation as a message.", true
}

// leadAsk sends card id's brief b to its organization's lead of d,
// starting it first, and logs it.
func (u *ui) leadAsk(id string, d company.Department, b company.Brief) error {
	c := u.b.Card(id)
	if c == nil {
		return errors.New("the card is gone from the board")
	}
	key := leadKey(c.Org, d.Name)
	if u.leads == nil {
		u.leads = map[string]*leadState{}
	}
	st := u.leads[key]
	if st == nil {
		st = &leadState{org: c.Org, dept: d}
		u.leads[key] = st
	}
	if st.proc == nil {
		p, err := u.leadStart(st, key)
		if err != nil {
			return err
		}
		st.proc = p
	}
	text := b.Text(c.Key())
	if err := st.proc.Send(text); err != nil {
		return err
	}
	st.from = append(st.from, id)
	u.say(key, "main", text)
	u.logCompany(c.Org, company.Entry{Kind: "brief", From: c.Key(), To: d.Lead, Card: c.Key(), Text: text})
	return nil
}

// leadStart starts st's Claude in its organization's folder, under its
// account, with tools that only read; its session kept in the company
// folder and resumed, so it remembers.
func (u *ui) leadStart(st *leadState, key string) (session, error) {
	dir, acct := orgs.Dir(u.root, st.org, ""), orgs.Account(u.root, st.org)
	o := claude.Options{Home: u.home, Org: acct, Dir: dir, System: company.LeadPrompt(st.org, st.dept) + u.aboutMe(st.org),
		Args: []string{"--allowedTools=Read,Glob,Grep,Skill", "--tools", "Read,Glob,Grep,Skill"}, Strict: true} // only what it uses: each tool's schema goes with every turn
	sid := company.Sessions(dir)[st.dept.Name]
	if sid == "" {
		sid = claude.NewSessionID()
		company.SetSession(dir, st.dept.Name, sid)
	}
	if claude.SessionExists(u.home, acct, sid) {
		o.Resume = sid
	} else {
		o.New = sid
	}
	var err error
	if o.Env, _, err = u.modelEnv(st.org, "lead"); err != nil {
		return nil, err
	}
	var p session
	p, err = startClaude(o, func(e claude.Event) { u.later(func() { u.leadEvent(key, p, e) }) })
	if err == nil {
		own(p, infra.Owner{Org: st.org}) // no card's tools, no desk peer
	}
	return p, err
}

// leadEvent applies one of the lead at key's events (p: its process; a
// replaced one's are dropped).
func (u *ui) leadEvent(key string, p session, e claude.Event) {
	st := u.leads[key]
	if st == nil || st.proc != p {
		return
	}
	switch e.Kind {
	case claude.KindInit:
		if dir := orgs.Dir(u.root, st.org, ""); e.Session != "" && company.Sessions(dir)[st.dept.Name] != e.Session {
			company.SetSession(dir, st.dept.Name, e.Session)
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
	case claude.KindAsk: // it only reads
		p.Respond(e.Ask.ID, false, nil, "A department lead only reads: recommend instead.")
		u.say(key, "tool", "refused "+e.Ask.Line)
	case claude.KindResult:
		if e.Error && e.Text != "" {
			u.say(key, "error", e.Text)
		}
		if len(st.from) > 0 {
			id := st.from[0]
			st.from = st.from[1:]
			u.leadTell(id, st, cmp.Or(st.last, "(no answer: "+cmp.Or(firstLine(e.Text), "it stopped")+")"))
		}
		st.last = ""
	case claude.KindExit:
		st.proc, st.from = nil, nil
		if e.Text != "" {
			u.say(key, "error", e.Text)
		}
	}
}

// leadTell brings st's answer into card id's session, as the workspace
// Claude's come (wsTell), and logs it.
func (u *ui) leadTell(id string, st *leadState, text string) {
	c := u.b.Card(id)
	if c == nil {
		return
	}
	u.logCompany(st.org, company.Entry{Kind: "result", From: st.dept.Lead, To: c.Key(), Card: c.Key(), Text: text})
	u.sayMsg(id, kanban.ChatMsg{Who: "lead", Name: st.dept.Lead, Text: text})
	msg := "From the " + st.dept.Lead + ":\n" + text
	if u.terms[id] != nil || u.shellStop[id] != nil || u.asks[id] != nil {
		u.peerOut[id] += msg + "\n\n"
		return
	}
	u.deliver(id, msg, true)
}

// logCompany adds e to org's company log; a failure is a toast, the work
// goes on. Any goroutine.
func (u *ui) logCompany(org string, e company.Entry) {
	if err := company.Log(orgs.Dir(u.root, org, ""), e); err != nil {
		u.later(func() { u.showToast("Company log: "+err.Error(), true) })
	}
}
