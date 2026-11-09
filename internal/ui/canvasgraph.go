package ui

import (
	"cmp"
	"fmt"
	"image/color"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// cardGraph is card c's process as desk knows it now, for the canvas: the
// card and its tickets (and the workspace Claude, when it has one) feed the
// session; the session hands work to its subagents (one node per kind, as
// the team was), they and it run shells; its pull requests and commits go to
// their CI, and all of it to Done. Only what desk knows: nothing here runs.
func (u *ui) cardGraph(c *kanban.Card, now time.Time) cvGraph {
	var g cvGraph
	ms, earlier := u.onCanvas(c.ID, u.members(c.ID), now)
	g.earlier = earlier
	wire := func(from, out, to, in string) { g.wires = append(g.wires, cvWire{from, out, to, in}) }

	// the card, its tickets and the workspace Claude: what the session starts from
	card := cvNode{id: "card", title: "Card · " + c.Key(), fam: famDesk, open: "edit", rows: []cvRow{{name: "title", value: c.Title}}}
	if len(c.Labels) > 0 {
		card.rows = append(card.rows, cvRow{name: "labels", value: strings.Join(c.Labels, ", ")})
	}
	if c.Org != "" {
		card.rows = append(card.rows, cvRow{name: "folder", value: strings.Trim(c.Org+"/"+c.Dir, "/"), mono: true})
	}
	card.rows = append(card.rows, cvRow{name: "Start", out: true}, cvRow{name: "Context", out: true, sock: sockContext})
	if c.Org != "" {
		card.rows = append(card.rows, cvRow{name: "Repo", out: true, sock: sockRepo})
	}
	g.nodes = append(g.nodes, card)

	sess := cvNode{id: "main", title: "Claude · " + cmp.Or(c.Agent, "session"), fam: famClaude, col: 1, frame: "Session", open: "convo",
		rows: []cvRow{{name: "Run", in: true}, {name: "Context", in: true, sock: sockContext}}}
	wire("card", "Start", "main", "Run")
	wire("card", "Context", "main", "Context")
	if c.Org != "" {
		sess.rows = append(sess.rows, cvRow{name: "Repo", in: true, sock: sockRepo})
		wire("card", "Repo", "main", "Repo")
	}
	var prs, others []kanban.Link
	tickets := false
	for _, l := range c.Links {
		switch l.Kind {
		case links.KindJira, links.KindGitHubIssue:
			g.nodes = append(g.nodes, cvNode{id: "link:" + l.URL, title: "Ticket · " + cmp.Or(l.Ref, l.URL), fam: famDesk, open: "url:" + l.URL,
				rows: []cvRow{{name: "state", value: cmp.Or(l.State, "unknown"), tone: toneColor(l.Tone)}, {name: "Ticket", out: true, sock: sockLink}}})
			wire("link:"+l.URL, "Ticket", "main", "Ticket")
			tickets = true
		case links.KindURL:
			others = append(others, l)
		default: // pull and merge requests, commits
			prs = append(prs, l)
		}
	}
	if tickets {
		sess.rows = append(sess.rows, cvRow{name: "Ticket", in: true, sock: sockLink})
	}
	if ws := u.ws[c.ID]; c.WSSession != "" || ws != nil && ws.proc != nil {
		busy := ws != nil && len(ws.from) > 0
		status := "Idle"
		if busy {
			status = "Answering"
		}
		g.nodes = append(g.nodes, cvNode{id: "ws", title: "Claude · workspace", badge: "read-only", fam: famClaude, open: "ws",
			who:  &cvWho{initials: "WS", col: wsColor, status: status, busy: busy},
			rows: []cvRow{{name: "tools", value: "Read, Glob, Grep"}, {name: "projects", value: fmt.Sprint(len(u.projects(c)))}, {name: "Answers", out: true, sock: sockText}}})
		sess.rows = append(sess.rows, cvRow{name: "Workspace", in: true, sock: sockText})
		wire("ws", "Answers", "main", "Workspace")
	}

	// the session itself
	if s, ok := statusName[c.Status]; ok {
		sess.rows = append(sess.rows, cvRow{name: "status", value: s, tone: statusColor[c.Status]})
	}
	if s := c.Session; s.Turns > 0 {
		sess.rows = append(sess.rows, cvRow{name: "usage", value: fmt.Sprintf("%s · %s tokens · $%.2f", plural(s.Turns, "turn", "turns"), tokens(s.Tokens), s.Cost)})
	}
	if s := c.Session; s.Window > 0 {
		sess.rows = append(sess.rows, cvRow{name: "context", value: fmt.Sprintf("%d%%", s.Context*100/s.Window)})
	}
	sess.rows = append(sess.rows, cvRow{name: "Finish", out: true})
	sess.who = &cvWho{initials: initials(cmp.Or(c.Agent, "Claude")), col: pastels[0], status: u.mainStatus(c, ms), busy: c.Status == kanban.StatusWorking}

	// its subagents, one node per kind, and the shells
	shells := map[string][]*task{} // by who ran them: a subagent's node id, or "main"
	for _, t := range u.tasks[c.ID] {
		if !t.Shell || t.Status != "running" && !t.end.IsZero() && now.Sub(t.end) > settled && !u.earlier[c.ID] {
			continue
		}
		owner := "main"
		if a := u.findTask(c.ID, "", u.toolBy[t.ToolUse]); a != nil {
			name := cmp.Or(a.Agent, a.title())
			if slices.ContainsFunc(ms, func(m member) bool { return m.name == name }) {
				owner = "agent:" + name
			}
		}
		shells[owner] = append(shells[owner], t)
	}
	if len(ms) > 0 {
		sess.rows = append(sess.rows, cvRow{name: "Agents", out: true})
	}
	for _, m := range ms {
		t := m.last
		w, held := u.waiting(c.ID, t, now)
		state, tone := "Working", color.NRGBA{}
		switch {
		case t.Status == "running" && held:
			state, tone = "Waiting", kit.COrange
		case t.Status == "running":
		case t.Status == "completed":
			state, tone = "Done · "+t.took(now), kit.CGreen
		default:
			state, tone = t.Status, kit.CRed
		}
		n := cvNode{id: "agent:" + m.name, title: "Claude · " + m.name, fam: famClaude, col: 2, frame: "Session", open: "sub:" + t.key(), dim: t.Status != "running",
			rows: []cvRow{{name: "Run", in: true}, {name: "state", value: state, tone: tone}}}
		var badge []string // its model and runs, in the header
		if t.Model != "" {
			badge = append(badge, modelName(t.Model))
		}
		if m.runs > 1 {
			badge = append(badge, fmt.Sprintf("×%d", m.runs))
		}
		n.badge = strings.Join(badge, " · ")
		if t.Prompt != "" {
			n.rows = append(n.rows, cvRow{name: "brief", value: oneParagraph(t.Prompt)})
		}
		if t.Summary != "" && t.Status != "running" {
			n.rows = append(n.rows, cvRow{name: "result", value: oneParagraph(t.Summary)})
		}
		n.rows = append(n.rows, cvRow{name: "work", value: teamFacts(t, now)})
		line := t.doing()
		if held {
			line = w.text + " · " + w.held(now)
		}
		if line == "running" {
			line = "Working"
		}
		n.who = &cvWho{initials: initials(m.name), col: m.col, status: line, busy: t.Status == "running" && !held}
		g.nodes = append(g.nodes, n)
		wire("main", "Agents", n.id, "Run")
	}
	owners := []string{"main"}
	for _, m := range ms {
		owners = append(owners, "agent:"+m.name)
	}
	for _, owner := range owners {
		ts := shells[owner]
		if len(ts) == 0 {
			continue
		}
		on := &sess
		if owner != "main" {
			on = g.node(owner)
		}
		on.rows = append(on.rows, cvRow{name: "Shells", out: true})
		col := on.col // on points into g.nodes, which the shells grow
		for _, s := range ts {
			state, tone := "running · "+s.took(now), color.NRGBA{}
			if s.Status != "running" {
				state, tone = s.Status+" · "+s.took(now), s.color()
			}
			g.nodes = append(g.nodes, cvNode{id: "shell:" + s.key(), title: "Shell", fam: famCheck, col: col + 1, frame: "Session", open: "sub:" + s.key(), dim: s.Status != "running",
				rows: []cvRow{{name: "Run", in: true}, {name: "$", value: cmp.Or(s.cmd, s.title()), mono: true}, {name: "state", value: state, tone: tone}}})
			wire(owner, "Shells", "shell:"+s.key(), "Run")
		}
	}

	// what it delivers: pull requests and commits with their CI, other links
	done := cvNode{id: "done", title: "Done", fam: famDesk, col: 4, rows: []cvRow{{name: "Finish", in: true}}}
	wire("main", "Finish", "done", "Finish")
	if len(prs) > 0 {
		sess.rows = append(sess.rows, cvRow{name: "Diff", out: true, sock: sockDiff})
	}
	if len(others)+len(c.Artifacts) > 0 {
		sess.rows = append(sess.rows, cvRow{name: "Links", out: true, sock: sockLink})
	}
	verdict, pr := false, false
	for _, l := range prs {
		kind := "PR"
		if l.Kind == links.KindGitHubCommit || l.Kind == links.KindGitLabCommit {
			kind = "Commit"
		}
		n := cvNode{id: "link:" + l.URL, title: kind + " · " + cmp.Or(l.Ref, l.URL), fam: famDesk, col: 2, band: 1, frame: "Delivery", open: "url:" + l.URL,
			rows: []cvRow{{name: "Diff", in: true, sock: sockDiff}, {name: "state", value: cmp.Or(l.State, "unknown"), tone: toneColor(l.Tone)}}}
		wire("main", "Diff", n.id, "Diff")
		if runs := u.ciRunsOf(c, l); len(runs) > 0 {
			n.rows = append(n.rows, cvRow{name: "CI", out: true, sock: sockLink})
			state, tone := links.Summary(runs)
			ci := cvNode{id: "ci:" + l.URL, title: state, fam: famCheck, col: 3, band: 1, frame: "Delivery", rows: []cvRow{{name: "Run", in: true, sock: sockLink}}}
			open := runs[0].URL // the first failed run, else the first
			if i := slices.IndexFunc(runs, func(r links.Run) bool { return r.Status == links.CIFailed }); i >= 0 {
				open = runs[i].URL
			}
			var jobs []cvRow // each run's jobs, or the run itself without
			for _, r := range runs {
				if len(r.Jobs) == 0 {
					jobs = append(jobs, cvRow{name: r.Name, value: r.Status, tone: ciTone(r.Status)})
				}
				for _, j := range r.Jobs {
					jobs = append(jobs, cvRow{name: j.Name, value: j.Status, tone: ciTone(j.Status)})
				}
			}
			if len(jobs) > 5 {
				jobs = append(jobs[:4], cvRow{name: fmt.Sprintf("and %d more", len(jobs)-4)})
			}
			ci.rows = append(ci.rows, jobs...)
			ci.open, ci.tone = "url:"+open, toneColor(tone)
			ci.rows = append(ci.rows, cvRow{name: "Verdict", out: true, sock: sockVerdict})
			g.nodes = append(g.nodes, n, ci)
			wire(n.id, "CI", ci.id, "Run")
			wire(ci.id, "Verdict", "done", "Verdict")
			verdict = true
			continue
		}
		n.rows = append(n.rows, cvRow{name: "Link", out: true, sock: sockLink})
		g.nodes = append(g.nodes, n)
		wire(n.id, "Link", "done", "PR")
		pr = true
	}
	for _, l := range others {
		g.nodes = append(g.nodes, cvNode{id: "link:" + l.URL, title: "Link · " + cmp.Or(l.Ref, hostOf(l.URL)), fam: famDesk, col: 2, band: 1, frame: "Delivery", open: "url:" + l.URL,
			rows: []cvRow{{name: "Link", in: true, sock: sockLink}, {name: "url", value: l.URL, mono: true}}})
		wire("main", "Links", "link:"+l.URL, "Link")
	}
	for _, a := range c.Artifacts {
		g.nodes = append(g.nodes, cvNode{id: "link:" + a, title: artifactName(a), fam: famDesk, col: 2, band: 1, frame: "Delivery", open: "url:" + a,
			rows: []cvRow{{name: "Link", in: true, sock: sockLink}, {name: "page", value: "published by the session"}}})
		wire("main", "Links", "link:"+a, "Link")
	}
	if verdict {
		done.rows = append(done.rows, cvRow{name: "Verdict", in: true, sock: sockVerdict})
	}
	if pr {
		done.rows = append(done.rows, cvRow{name: "PR", in: true, sock: sockLink})
	}
	column := "Archived"
	if ci, _ := u.b.Find(c.ID); ci >= 0 {
		column = u.b.Columns[ci].Title
	}
	done.rows = append(done.rows, cvRow{name: "column", value: column})
	switch column {
	case "Done":
		done.tone = kit.CGreen
	case "Review":
		done.tone = kit.Accents[5]
	default:
		done.dim = true
	}
	g.nodes = slices.Insert(g.nodes, 1, sess) // after the card; built last, as its outputs depend on the rest
	g.nodes = append(g.nodes, done)
	return g
}

// wsColor is the workspace Claude's avatar.
var wsColor = kit.RGB(0x9aa4b1)

// mainStatus is the session's line under its avatar: the tool it runs or
// its note while it works, else what it waits on.
func (u *ui) mainStatus(c *kanban.Card, ms []member) string {
	if c.Status == kanban.StatusWorking {
		if msgs := u.chat(c.ID); len(msgs) > 0 && msgs[len(msgs)-1].Who == "tool" {
			return strings.Join(strings.Fields(msgs[len(msgs)-1].Text), " ")
		}
		return cmp.Or(c.Note, "Working")
	}
	s := leadStatus(c, ms)
	if c.Note != "" && !strings.HasPrefix(s, "Waiting on") {
		s += ": " + c.Note
	}
	return s
}

// ciRunsOf is link l's CI runs as the last CI poll found them.
func (u *ui) ciRunsOf(c *kanban.Card, l kanban.Link) []links.Run {
	for _, it := range u.ci[c.Org] {
		if it.id == c.ID && it.link.URL == l.URL {
			return it.runs
		}
	}
	return nil
}

// ciTone is how a CI run's status colors.
func ciTone(s string) color.NRGBA {
	switch s {
	case links.CISuccess:
		return kit.CGreen
	case links.CIFailed:
		return kit.CRed
	}
	return kit.CText2
}

// hostOf is a URL's host, or the URL.
func hostOf(s string) string {
	if p, err := url.Parse(s); err == nil && p.Host != "" {
		return p.Host
	}
	return s
}
