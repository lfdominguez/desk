package ui

import (
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/company"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
)

// The Company view's scene: the demo's cards at work in their
// organizations, a lead on a brief, an executive consulted, hand-offs in
// flight.
func init() {
	scenes = append(scenes, scene{"orgs-company", func(s *shooter) {
		s.infra() // Globex's servers: its Infra lead
		// an autopilot on two of Acme's tickets: tethered to its cards in the sky
		todo := s.u.b.Column("To do")
		pilot := s.u.b.Add(todo, kanban.Card{Title: "Autopilot: ABC-123, ABC-140", Org: "Acme", Labels: []string{"autopilot"},
			Pilot: &kanban.Pilot{Level: kanban.PilotAuto, Max: 2}}).ID
		for _, t := range []string{"ABC-123 Login retry fails after the token expires", "ABC-140 Export the audit log as CSV"} {
			s.u.b.Add(todo, kanban.Card{Title: t, Org: "Acme", Parent: pilot})
		}
		states := []string{kanban.StatusWorking, kanban.StatusWaiting, kanban.StatusWorking, kanban.StatusBlocked, kanban.StatusReview}
		keys := map[string]string{}
		n, done := 0, s.u.b.Column("Done")
		for ci := range s.u.b.Columns {
			for j := range s.u.b.Columns[ci].Cards {
				c := &s.u.b.Columns[ci].Cards[j]
				if ci == done || c.Org == "" {
					continue
				}
				if c.Session.ID == "" {
					c.Session.ID = claude.NewSessionID()
				}
				if c.Pilot != nil || c.Parent != "" { // the autopilot's: at work, one card ready
					c.Status = kanban.StatusWorking
					if strings.HasPrefix(c.Title, "ABC-140") {
						c.Status = kanban.StatusReview
					}
					continue
				}
				c.Status, c.Note = states[n%len(states)], ""
				if n == 1 {
					c.Note = "Paused: over its $5.00 budget since your last message"
				}
				if keys[c.Org] == "" {
					keys[c.Org] = c.Key()
				}
				n++
			}
		}
		s.u.leads = map[string]*leadState{leadKey("Acme", "security"): {org: "Acme", dept: company.Departments[1], from: []string{"x"}}}
		now := time.Now()
		acme, globex := orgs.Dir(s.u.root, "Acme", ""), orgs.Dir(s.u.root, "Globex", "")
		company.Log(acme, company.Entry{At: now, Kind: "brief", From: keys["Acme"], To: "Security lead", Text: "May the token live in .env?"})
		company.Log(acme, company.Entry{At: now, Kind: "brief", From: keys["Acme"], To: "desk-cto", Text: "Split the API?"})
		company.Log(globex, company.Entry{At: now, Kind: "brief", From: keys["Globex"], To: "Infra lead", Text: "Restart nginx on web-1"})
		s.u.save()
		s.u.view = viewCompany
		s.settle()
		s.shot("orgs-company")
	}})
}
