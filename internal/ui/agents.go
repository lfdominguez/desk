package ui

import (
	"image/color"
	"path/filepath"
	"slices"
	"strings"

	"gioui.org/layout"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/company"
	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// agentsDir is where org's agent files are: its desk.toml's agents, from
// the organization's folder, else its .claude/agents.
func (u *ui) agentsDir(org string, cfg orgs.Config) string {
	d := cfg.Agents
	if d == "" {
		d = filepath.Join(".claude", "agents")
	}
	return filepath.Join(orgs.Dir(u.root, org, ""), d)
}

// agentArgs are the flags c's session takes beyond the usual: the folder of
// images pasted in its notes, desk's card tools (cardtools.go) and desk
// peer for an org with servers, allowed without asking, and its agent.
func (u *ui) agentArgs(c *kanban.Card) ([]string, error) {
	var args []string
	if strings.Contains(c.Notes, u.imagesDir()) { // outside where it works: Claude may read them
		args = []string{"--add-dir=" + u.imagesDir()} // = form: one value, never the prompt after it
	}
	roster, rerr := u.roster(c)
	var off []string // card tools its organization can't use: their schemas would ride along on every turn
	if u.infraOrg(c.Org) == nil {
		off = append(off, "servers")
	}
	if !company.HasRoles(roster) {
		off = append(off, "lead")
	}
	mcp, allow := infra.CardMCP(u.cardSock, c.ID, off...)
	if c.Pilot != nil { // an autopilot runs its cards: it reads, never writes
		mcp, allow = infra.PilotMCP(u.cardSock, c.ID)
		args = append(args, "--disallowedTools=Edit,Write,NotebookEdit")
	} else if u.pilotOf(c) != nil { // moving a ticket changes something outside: it asks, and its autopilot needs the user's grant
		allow = slices.DeleteFunc(allow, func(t string) bool { return t == "mcp__desk__ticket_status" })
	}
	if mcp != "" {
		args = append(args, "--mcp-config", mcp)
	}
	if u.infraOrg(c.Org) != nil { // server work goes to the org's infra Claude
		allow = append(allow, "Bash(desk peer:*)")
	}
	if len(allow) > 0 {
		args = append(args, "--allowedTools", strings.Join(allow, ","))
	}
	if rerr != nil && c.Agent != "" { // its agent's file is unreadable; without one, desk's roles still come
		return nil, rerr
	}
	a, err := claude.AgentArgs(roster, c.Agent)
	return append(args, a...), err
}

// roster is what c's session can hand work to: its organization's agents
// and desk's roles (internal/company); err when its desk.toml can't be read.
func (u *ui) roster(c *kanban.Card) ([]claude.Agent, error) {
	cfg, err := orgs.Load(u.root, c.Org)
	return company.Roster(claude.Agents(u.agentsDir(c.Org, cfg)), cfg.Company), err
}

// aboutMe is who the user is in org (orgs.AboutMe), for a Claude's system
// prompt; "" when they said nothing.
func (u *ui) aboutMe(org string) string { return claude.AboutUser(orgs.AboutMe(u.root, org)) }

// companyPrompt is company.PromptFor its organization when c's session
// has desk's roles.
func (u *ui) companyPrompt(c *kanban.Card) string {
	if r, _ := u.roster(c); company.HasRoles(r) {
		cfg, _ := orgs.Load(u.root, c.Org)
		return company.PromptFor(cfg.Company)
	}
	return ""
}

// sheetAgents reads the dialog's organization's agents when it changes; a
// new card (or one moved to another organization) takes its default agent.
func (u *ui) sheetAgents(s *sheet) {
	org := strings.TrimSpace(s.org.Text())
	if !slices.Contains(u.orgList, org) {
		org = ""
	}
	if s.agentsOf == org && s.agentsRead {
		return
	}
	s.agentsOf, s.agentsRead, s.agents = org, true, nil
	if org == "" {
		return
	}
	cfg, _ := orgs.Load(u.root, org)
	s.agents = claude.Agents(u.agentsDir(org, cfg))
	if c := u.b.Card(s.id); c == nil || c.Org != org {
		s.agent = cfg.Agent
	}
}

// agentRow picks the agent the card's session runs as, and says what it does.
func (u *ui) agentRow(gtx layout.Context, s *sheet) layout.Dimensions {
	k := u.k
	names, cur, desc := []string{"None"}, 0, "Claude Code as it is, no agent"
	for i, a := range s.agents {
		names = append(names, a.Name)
		if a.Name == s.agent {
			cur, desc = i+1, a.Description
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.choice(gtx, "agent", names, cur, func(int) color.NRGBA { return color.NRGBA{} }, func(i int) {
				s.agent = ""
				if i > 0 {
					s.agent = s.agents[i-1].Name
				}
			})
		}),
		layout.Rigid(kit.VGap(5)),
		layout.Rigid(k.Lbl(desc, 11, kit.CText3, kit.Sans)))
}
