package links

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
)

// Push moves ticket l to where a card in desk's column (its title) is:
// a Jira issue takes the transition to that column's status category
// (Blocked: a status named so, if its workflow has one), a GitHub issue
// closes in Done and reopens out of it. It says what the ticket is now,
// or "" when there was nothing to do: in step already, not a ticket, or an
// account that opts out (sync = false) or can't be told apart. The
// organization's workflow names a column's status where its account
// doesn't.
func Push(ctx context.Context, l kanban.Link, column string, cfg orgs.Config) (string, error) {
	s, err := push(ctx, l, column, cfg, false)
	return s.Now, err
}

// Synced is what the background check did and found: Now what desk moved
// the ticket to ("" nothing), Seen its status as read ("" unread: kept as
// the link's Seen), and Moved that someone changed it since the link's
// Seen to a status out of step with its card, which then follows.
type Synced struct {
	Now, Seen string
	Moved     bool
}

// Sync is the background check's Push. A ticket of an organization with a
// workflow is never moved back when it is out of step with its card
// (someone moved it on purpose); Moved says the card should follow. A
// status unchanged since the last check (a move of the card's own that
// couldn't move the ticket), or one read for the first time, isn't
// followed.
func Sync(ctx context.Context, l kanban.Link, column string, cfg orgs.Config) (Synced, error) {
	return push(ctx, l, column, cfg, cfg.Workflow.On())
}

func push(ctx context.Context, l kanban.Link, column string, cfg orgs.Config, follow bool) (Synced, error) {
	u, err := url.Parse(l.URL)
	if err != nil {
		return Synced{}, err
	}
	switch l.Kind {
	case KindJira:
		j, ok := jiraFor(cfg, u.Hostname(), l.Ref)
		if !ok || off(j.Sync) {
			return Synced{}, nil
		}
		return pushJira(ctx, u, l, column, j, cfg, follow)
	case KindLinear:
		a, ok := linearAcct(cfg, l)
		if !ok || off(a.Sync) {
			return Synced{}, nil
		}
		return pushLinear(ctx, l, column, a, cfg, follow)
	case KindGitHubIssue:
		p, ok := githubIssue(u, cfg)
		if !ok {
			return Synced{}, nil
		}
		if follow {
			state, err := githubState(ctx, u, p, cfg)
			return Synced{Seen: state, Moved: changed(l.Seen, state) && (state == "closed") != (column == "Done")}, err
		}
		now, err := pushGitHub(ctx, u, p, column, cfg)
		return Synced{Now: now}, err
	}
	return Synced{}, nil
}

// changed says a ticket's status now differs from the one last seen; never
// on a first look.
func changed(seen, now string) bool { return seen != "" && now != "" && !strings.EqualFold(seen, now) }

// githubState is a GitHub issue's state: open or closed.
func githubState(ctx context.Context, u *url.URL, p []string, cfg orgs.Config) (string, error) {
	repo, a, err := githubRepo(ctx, u, p, cfg)
	if err != nil {
		return "", err
	}
	var is struct {
		State string `json:"state"`
	}
	err = getJSON(ctx, repo+"/issues/"+p[3], a, &is)
	return is.State, err
}

// githubIssue is a GitHub issue URL's path parts, when one of the
// organization's accounts answers for it and moves its issues.
func githubIssue(u *url.URL, cfg orgs.Config) ([]string, bool) {
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	i := slices.IndexFunc(cfg.GitHub, func(g orgs.GitHub) bool {
		return (g.Host == "" || g.Host == u.Hostname()) && (len(g.Owners) == 0 || slices.Contains(g.Owners, p[0]))
	})
	return p, len(p) >= 4 && i >= 0 && !off(cfg.GitHub[i].Sync) // no account of the user's: nothing to write with
}

func off(b *bool) bool { return b != nil && !*b }

// blocked tells a Blocked status by its name (English or Spanish).
func blocked(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "block") || strings.Contains(n, "bloque")
}

// category is the Jira status category a desk column stands for.
func category(column string) string {
	switch column {
	case "Backlog", "To do":
		return "new"
	case "Done":
		return "done"
	}
	return "indeterminate" // In progress, Blocked
}

type jiraStatus struct {
	Name     string `json:"name"`
	Category struct {
		Key string `json:"key"`
	} `json:"statusCategory"`
}

type jiraTransition struct {
	ID string     `json:"id"`
	To jiraStatus `json:"to"`
}

// jiraTicket is a Jira issue as read for moving it: where it is, how to
// ask, its status and assignee.
type jiraTicket struct {
	key, base, issue string
	a                func(*http.Request)
	status           jiraStatus
	assignee         string // its account id, "" nobody
}

func openJira(ctx context.Context, u *url.URL, key string, j orgs.Jira, cfg orgs.Config) (jiraTicket, error) {
	a, err := auth(ctx, cfg, j.Email, j.Secret)
	if err != nil {
		return jiraTicket{}, err
	}
	base := strings.TrimRight(j.URL, "/")
	if base == "" {
		base = u.Scheme + "://" + u.Host
	}
	ji := jiraTicket{key: key, base: base, issue: base + "/rest/api/3/issue/" + url.PathEscape(key), a: a}
	var now struct {
		Fields struct {
			Status   jiraStatus `json:"status"`
			Assignee *struct {
				AccountID string `json:"accountId"`
			} `json:"assignee"`
		} `json:"fields"`
	}
	if err := getJSON(ctx, ji.issue+"?fields=status,assignee", a, &now); err != nil {
		return jiraTicket{}, err
	}
	ji.status = now.Fields.Status
	if now.Fields.Assignee != nil {
		ji.assignee = now.Fields.Assignee.AccountID
	}
	return ji, nil
}

// mine says the issue is assigned to the account's user: desk moves only theirs.
func (ji jiraTicket) mine(ctx context.Context) (bool, error) {
	if ji.assignee == "" {
		return false, nil
	}
	me, err := jiraMe(ctx, ji.base, ji.a)
	return err == nil && ji.assignee == me, err
}

func (ji jiraTicket) transitions(ctx context.Context) ([]jiraTransition, error) {
	var ts struct {
		Transitions []jiraTransition `json:"transitions"`
	}
	err := getJSON(ctx, ji.issue+"/transitions", ji.a, &ts)
	return ts.Transitions, err
}

func (ji jiraTicket) move(ctx context.Context, t jiraTransition) error {
	return doJSON(ctx, "POST", ji.issue+"/transitions", ji.a, map[string]any{"transition": map[string]string{"id": t.ID}}, nil)
}

func pushJira(ctx context.Context, u *url.URL, l kanban.Link, column string, j orgs.Jira, cfg orgs.Config, follow bool) (Synced, error) {
	key := l.Ref
	ji, err := openJira(ctx, u, key, j, cfg)
	if err != nil {
		return Synced{}, err
	}
	cur, want := ji.status, category(column)
	seen := Synced{Seen: cur.Name}
	if ignored(j, cur.Name) {
		return seen, nil // in someone else's hands now
	}
	name := cmp.Or(j.Status[column], cfg.Workflow.Status[column]) // the account, else the workflow, says which status, by name
	if name != "" && strings.EqualFold(cur.Name, name) || name == "" && column == "Blocked" && blocked(cur.Name) {
		return seen, nil
	}
	if follow { // out of step: someone moved it, and the card follows; or the card's move couldn't, and both stay
		seen.Moved = changed(l.Seen, cur.Name) && (name != "" || cur.Category.Key != want || column != "Blocked" && blocked(cur.Name))
		return seen, nil
	}
	if mine, err := ji.mine(ctx); err != nil || !mine {
		return Synced{}, err // not the user's
	}
	ts, err := ji.transitions(ctx)
	if err != nil {
		return Synced{}, err
	}
	pick := -1
	if name != "" {
		pick = slices.IndexFunc(ts, func(t jiraTransition) bool { return strings.EqualFold(t.To.Name, name) })
		if pick < 0 {
			return Synced{}, fmt.Errorf("%s: no transition from %s to %s", key, cur.Name, name)
		}
	} else if column == "Blocked" { // its own status when the workflow has one
		pick = slices.IndexFunc(ts, func(t jiraTransition) bool { return blocked(t.To.Name) })
	}
	if pick < 0 {
		if cur.Category.Key == want && !(column != "Blocked" && blocked(cur.Name)) {
			return seen, nil
		}
		for i, t := range ts {
			if t.To.Category.Key == want && !blocked(t.To.Name) {
				pick = i
				break
			}
		}
	}
	if pick < 0 {
		return Synced{}, fmt.Errorf("%s: no transition from %s to a %s status", key, cur.Name, want)
	}
	if err := ji.move(ctx, ts[pick]); err != nil {
		return Synced{}, err
	}
	return Synced{Now: ts[pick].To.Name, Seen: ts[pick].To.Name}, nil
}

// ignored says status is one the account leaves to others (ignore).
func ignored(j orgs.Jira, status string) bool {
	return slices.ContainsFunc(j.Ignore, func(s string) bool { return strings.EqualFold(s, status) })
}

// Transition moves ticket l to status by name, as a card's Claude asks
// when the organization's workflow says to: a Jira issue through the
// transition to it (none: the error says where it can go), a GitHub issue
// "closed" or "open". Only the user's own tickets move. It says the
// status the ticket is in after.
func Transition(ctx context.Context, l kanban.Link, status string, cfg orgs.Config) (string, error) {
	u, err := url.Parse(l.URL)
	if err != nil {
		return "", err
	}
	switch l.Kind {
	case KindJira:
		j, ok := jiraFor(cfg, u.Hostname(), l.Ref)
		if !ok {
			return "", &NoAccountError{Provider: "Jira", Owner: l.Ref}
		}
		if off(j.Sync) {
			return "", fmt.Errorf("%s: its Jira account has sync = false: desk moves none of its tickets", l.Ref)
		}
		ji, err := openJira(ctx, u, l.Ref, j, cfg)
		if err != nil {
			return "", err
		}
		if strings.EqualFold(ji.status.Name, status) {
			return ji.status.Name, nil
		}
		if ignored(j, ji.status.Name) {
			return "", fmt.Errorf("%s is %s, a status its account leaves to others (ignore): desk doesn't move it from there", l.Ref, ji.status.Name)
		}
		if mine, err := ji.mine(ctx); err != nil || !mine {
			return "", cmp.Or(err, fmt.Errorf("%s isn't assigned to the user: desk moves only their tickets", l.Ref))
		}
		ts, err := ji.transitions(ctx)
		if err != nil {
			return "", err
		}
		i := slices.IndexFunc(ts, func(t jiraTransition) bool { return strings.EqualFold(t.To.Name, status) })
		if i < 0 {
			var to []string
			for _, t := range ts {
				to = append(to, t.To.Name)
			}
			return "", fmt.Errorf("%s can't go from %s to %s; from %s it can go to: %s", l.Ref, ji.status.Name, status, ji.status.Name, strings.Join(to, ", "))
		}
		if err := ji.move(ctx, ts[i]); err != nil {
			return "", err
		}
		return ts[i].To.Name, nil
	case KindLinear:
		a, ok := linearAcct(cfg, l)
		if !ok {
			return "", &NoAccountError{Provider: "Linear", Owner: l.Ref}
		}
		return transitionLinear(ctx, l, status, a, cfg)
	case KindGitHubIssue:
		p, ok := githubIssue(u, cfg)
		col := map[string]string{"closed": "Done", "open": "In progress"}[strings.ToLower(status)]
		if !ok || col == "" {
			return "", fmt.Errorf("%s: a GitHub issue is open or closed, through one of the organization's accounts", l.Ref)
		}
		if _, err := pushGitHub(ctx, u, p, col, cfg); err != nil {
			return "", err
		}
		return strings.ToLower(status), nil
	}
	return "", fmt.Errorf("%s is no ticket: a Jira, Linear or GitHub issue", l.URL)
}

// Statuses lists, for Haiku learning the organization's workflow, every
// status of its Jira projects (the first account's of each) and Linear
// teams, with its category or state type: one line per project or team,
// "" without any.
func Statuses(ctx context.Context, cfg orgs.Config) (string, error) {
	var b strings.Builder
	for _, a := range cfg.Linear {
		if len(a.Teams) == 0 {
			continue
		}
		if err := linearStatuses(ctx, a, cfg, &b); err != nil {
			return "", err
		}
	}
	for _, j := range cfg.Jira {
		if j.URL == "" || len(j.Projects) == 0 {
			continue
		}
		a, err := auth(ctx, cfg, j.Email, j.Secret)
		if err != nil {
			return "", err
		}
		for _, p := range j.Projects {
			var types []struct {
				Statuses []jiraStatus `json:"statuses"`
			}
			if err := getJSON(ctx, strings.TrimRight(j.URL, "/")+"/rest/api/3/project/"+url.PathEscape(p)+"/statuses", a, &types); err != nil {
				return "", err
			}
			var seen []string
			for _, t := range types {
				for _, s := range t.Statuses {
					if n := s.Name + " (" + s.Category.Key + ")"; !slices.Contains(seen, n) {
						seen = append(seen, n)
					}
				}
			}
			fmt.Fprintf(&b, "%s: %s\n", p, strings.Join(seen, ", "))
		}
	}
	return b.String(), nil
}

func pushGitHub(ctx context.Context, u *url.URL, p []string, column string, cfg orgs.Config) (string, error) {
	repo, a, err := githubRepo(ctx, u, p, cfg)
	if err != nil {
		return "", err
	}
	type user struct {
		Login string `json:"login"`
	}
	var is struct {
		State     string `json:"state"`
		Assignees []user `json:"assignees"`
	}
	issue := repo + "/issues/" + p[3]
	if err := getJSON(ctx, issue, a, &is); err != nil {
		return "", err
	}
	if len(is.Assignees) == 0 {
		return "", nil // nobody's: not the user's to move
	}
	var me user
	api, _, _ := strings.Cut(repo, "/repos/")
	if err := getJSON(ctx, api+"/user", a, &me); err != nil {
		return "", err
	}
	if !slices.ContainsFunc(is.Assignees, func(x user) bool { return strings.EqualFold(x.Login, me.Login) }) {
		return "", nil // someone else's
	}
	want := "open"
	if column == "Done" {
		want = "closed"
	}
	if is.State == want {
		return "", nil
	}
	body := map[string]string{"state": want}
	if want == "closed" {
		body["state_reason"] = "completed"
	}
	if err := doJSON(ctx, "PATCH", issue, a, body, nil); err != nil {
		return "", err
	}
	return map[string]string{"open": "reopened", "closed": "closed"}[want], nil
}
