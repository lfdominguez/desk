package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/vault"
)

// Card tools: a card's Claude reads its organization's integrations
// through desk (infra.ServeCards, infra.CardArgs): alerts, tickets, a
// link's state and description, CI runs and job logs; and its workspace
// (add_project, ask_workspace; its workspace Claude's suggest_to_card),
// answered by wsTool.

// cardTool answers card id's tool call, off the UI goroutine.
func (u *ui) cardTool(id, name string, raw json.RawMessage) (string, bool) {
	if strings.HasPrefix(name, "drawing") {
		return u.drawTool(id, name, raw)
	}
	id, ws := strings.CutPrefix(id, "ws:") // the card's workspace Claude's call
	if text, ok, done := u.wsTool(id, ws, name, raw); done {
		return text, ok
	}
	var a struct {
		URL   string `json:"url"`
		Job   int64  `json:"job"`
		Bytes int    `json:"bytes"`
	}
	json.Unmarshal(raw, &a)
	c, err := onBoard(u, func() (kanban.Card, error) {
		if c := u.b.Card(id); c != nil {
			return *c, nil
		}
		return kanban.Card{}, errors.New("the card is gone from the board")
	})
	if err != nil {
		return err.Error(), false
	}
	cfg, err := orgs.Load(u.root, c.Org)
	if err != nil {
		return err.Error(), false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	text, err := u.cardToolRun(ctx, c, cfg, name, a.URL, a.Job, a.Bytes)
	if l := (*vault.LockedError)(nil); errors.As(err, &l) {
		u.later(func() { u.vaultLocked(l) })
		return err.Error() + " (desk asks the user to unlock it; try again after)", false
	}
	if err != nil {
		return err.Error(), false
	}
	return text, true
}

func (u *ui) cardToolRun(ctx context.Context, c kanban.Card, cfg orgs.Config, name, at string, job int64, bytes int) (string, error) {
	// the links asked about: url, else the card's own (kinds keeps those that fit)
	pick := func(kinds func(kanban.Link) bool) ([]kanban.Link, error) {
		if at != "" {
			l, ok := links.Parse(at, cfg)
			if !ok {
				return nil, fmt.Errorf("%q is not a link desk reads", at)
			}
			return []kanban.Link{l}, nil
		}
		var ls []kanban.Link
		for _, l := range c.Links {
			if kinds(l) {
				ls = append(ls, l)
			}
		}
		if len(ls) == 0 {
			return nil, errors.New("the card has no such link: pass url")
		}
		return ls, nil
	}
	var b strings.Builder
	switch name {
	case "alerts":
		if len(cfg.Nagios) == 0 {
			return "", fmt.Errorf("%s has no [[nagios]] in desk.toml", c.Org)
		}
		as, err := nagiosAlerts(ctx, cfg)
		if err != nil && len(as) == 0 {
			return "", err
		}
		for _, a := range as {
			what := a.Host
			if a.Service != "" {
				what += " / " + a.Service
			}
			acked := ""
			if a.Acked {
				acked = " [acknowledged]"
			}
			fmt.Fprintf(&b, "%s %s since %s%s (%s): %s\n", strings.ToUpper(a.State), what, a.Since.Format("Jan 2 15:04"), acked, a.Monitor, a.Output)
		}
		if len(as) == 0 {
			return "No warnings or errors.", nil
		}
	case "tickets":
		ts, err := links.Assigned(ctx, cfg)
		if err != nil && len(ts) == 0 {
			return "", err
		}
		for _, t := range ts {
			fmt.Fprintf(&b, "%s %s [%s] %s %s\n", t.Project, t.Key, t.Status, t.Title, t.URL)
		}
		if len(ts) == 0 {
			return "No open tickets assigned.", nil
		}
	case "link":
		ls, err := pick(func(kanban.Link) bool { return true })
		if err != nil {
			return "", err
		}
		for _, l := range ls {
			state, _, err := links.Check(ctx, l, cfg)
			if err != nil {
				state = "error: " + err.Error()
			}
			fmt.Fprintf(&b, "%s %s: %s\n", l.Ref, l.URL, state)
			if d, err := links.Describe(ctx, l, cfg); err == nil && d != "" {
				b.WriteString(d + "\n")
			}
			b.WriteString("\n")
		}
	case "ci":
		ls, err := pick(links.HasCI)
		if err != nil {
			return "", err
		}
		for _, l := range ls {
			fmt.Fprintf(&b, "%s %s\n", l.Ref, l.URL)
			runs, err := ciRuns(ctx, l, cfg)
			if err != nil {
				fmt.Fprintf(&b, "  error: %s\n", err)
			} else if len(runs) == 0 {
				b.WriteString("  no CI runs\n")
			}
			for _, r := range runs {
				fmt.Fprintf(&b, "  %s: %s %s\n", r.Name, r.Status, r.URL)
				for _, j := range r.Jobs {
					name := j.Name
					if j.Stage != "" {
						name = j.Stage + " / " + name
					}
					fmt.Fprintf(&b, "    job %d %s: %s\n", j.ID, name, j.Status)
				}
			}
		}
	case "ci_log":
		ls, err := pick(links.HasCI)
		if err != nil {
			return "", err
		}
		if !links.HasCI(ls[0]) {
			return "", fmt.Errorf("%s has no CI desk reads", ls[0].Ref)
		}
		if bytes <= 0 {
			bytes = 20000
		}
		return links.JobLog(ctx, ls[0], cfg, job, min(bytes, 200000))
	default:
		return "", fmt.Errorf("no tool %s", name)
	}
	return b.String(), nil
}
