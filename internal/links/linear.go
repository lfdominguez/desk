package links

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
)

// Linear (linear.app) over its GraphQL API, with an account's personal API
// key (or an OAuth token). Its issues are TEAM-123, like Jira's keys: a
// bare key goes to the Linear account that lists its team first.

// linearAPI is Linear's GraphQL endpoint; tests point it at a fake.
var linearAPI = "https://api.linear.app/graphql"

// linearTeam is the Linear account that lists team, with a workspace to
// link a bare key in.
func linearTeam(cfg orgs.Config, team string) (orgs.Linear, bool) {
	i := slices.IndexFunc(cfg.Linear, func(a orgs.Linear) bool { return a.Workspace != "" && slices.Contains(a.Teams, team) })
	if i < 0 {
		return orgs.Linear{}, false
	}
	return cfg.Linear[i], true
}

// linearFor picks the Linear account for an issue of workspace ws (""
// unknown) and key's team; with one account and no better match, that one.
func linearFor(cfg orgs.Config, ws, key string) (orgs.Linear, bool) {
	team, _, _ := strings.Cut(key, "-")
	for _, pick := range []func(orgs.Linear) bool{
		func(a orgs.Linear) bool { return ws != "" && strings.EqualFold(a.Workspace, ws) },
		func(a orgs.Linear) bool { return slices.Contains(a.Teams, team) },
		func(orgs.Linear) bool { return len(cfg.Linear) == 1 },
	} {
		if i := slices.IndexFunc(cfg.Linear, pick); i >= 0 {
			return cfg.Linear[i], true
		}
	}
	return orgs.Linear{}, false
}

// linearAcct is the Linear account for link l.
func linearAcct(cfg orgs.Config, l kanban.Link) (orgs.Linear, bool) {
	ws := ""
	if u, err := url.Parse(l.URL); err == nil {
		ws, _ = linearWorkspace(u.Hostname(), strings.Split(strings.Trim(u.Path, "/"), "/"))
	}
	return linearFor(cfg, ws, l.Ref)
}

// linearLink is a bare key's link in a's workspace.
func linearLink(a orgs.Linear, key string) kanban.Link {
	return kanban.Link{URL: "https://linear.app/" + a.Workspace + "/issue/" + key, Kind: KindLinear, Ref: key}
}

// linearWorkspace is a Linear issue URL's workspace and key: linear.app/<ws>/issue/KEY/slug.
func linearWorkspace(host string, p []string) (ws, key string) {
	if host != "linear.app" || len(p) < 3 || p[1] != "issue" || !jiraKey.MatchString(strings.ToUpper(p[2])) {
		return "", ""
	}
	return p[0], strings.ToUpper(p[2])
}

// linear is how to ask Linear as a: its key as is (a personal API key),
// Bearer for an OAuth token. None is a NoAccountError.
func linear(ctx context.Context, cfg orgs.Config, a orgs.Linear, key string) (func(*http.Request), error) {
	tok, err := secret(ctx, cfg, a.Secret)
	if err != nil {
		return nil, err
	}
	if tok == "" {
		return nil, &NoAccountError{"Linear", key}
	}
	if !strings.HasPrefix(tok, "lin_api_") {
		tok = "Bearer " + tok
	}
	return func(r *http.Request) { r.Header.Set("Authorization", tok) }, nil
}

// gql runs query with vars and decodes its data into v. Linear answers most
// errors (not found, bad input) with 200 and an errors list.
func gql(ctx context.Context, a func(*http.Request), query string, vars map[string]any, v any) error {
	var out struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := doJSON(ctx, "POST", linearAPI, a, map[string]any{"query": query, "variables": vars}, &out); err != nil {
		return err
	}
	if len(out.Errors) > 0 {
		return fmt.Errorf("linear: %s", out.Errors[0].Message)
	}
	return json.Unmarshal(out.Data, v)
}

type linearState struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Type     string  `json:"type"` // triage, backlog, unstarted, started, completed, canceled, duplicate
	Position float64 `json:"position"`
}

// linearIssue is an issue as read to check, describe or move it.
type linearIssue struct {
	ID            string      `json:"id"`
	Identifier    string      `json:"identifier"`
	Title         string      `json:"title"`
	Description   string      `json:"description"`
	PriorityLabel string      `json:"priorityLabel"`
	State         linearState `json:"state"`
	Assignee      *struct {
		ID string `json:"id"`
	} `json:"assignee"`
	Team struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	} `json:"team"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
}

// linearTicket is an issue, with how to ask about it and whether it is the
// account's user's.
type linearTicket struct {
	linearIssue
	a    func(*http.Request)
	mine bool
}

func openLinear(ctx context.Context, cfg orgs.Config, acct orgs.Linear, key string) (linearTicket, error) {
	a, err := linear(ctx, cfg, acct, key)
	if err != nil {
		return linearTicket{}, err
	}
	var v struct {
		Issue  linearIssue `json:"issue"`
		Viewer struct {
			ID string `json:"id"`
		} `json:"viewer"`
	}
	q := `query($id: String!) { viewer { id } issue(id: $id) { id identifier title description priorityLabel
		state { id name type } assignee { id } team { id key } labels(first: 20) { nodes { name } } } }`
	if err := gql(ctx, a, q, map[string]any{"id": key}, &v); err != nil {
		return linearTicket{}, err
	}
	return linearTicket{v.Issue, a, v.Issue.Assignee != nil && v.Issue.Assignee.ID == v.Viewer.ID}, nil
}

// closed says a state type is done with: completed, canceled or a duplicate.
func closed(typ string) bool { return typ == "completed" || typ == "canceled" || typ == "duplicate" }

// linearCategory is the Jira status category a Linear state type stands
// for, as desk's columns map to them (category).
func linearCategory(typ string) string {
	switch {
	case closed(typ):
		return "done"
	case typ == "started":
		return "indeterminate"
	}
	return "new" // triage, backlog, unstarted
}

func checkLinear(ctx context.Context, l kanban.Link, acct orgs.Linear, cfg orgs.Config) (string, string, error) {
	t, err := openLinear(ctx, cfg, acct, l.Ref)
	if err != nil {
		return "", "", err
	}
	if closed(t.State.Type) {
		return t.State.Name, ToneDone, nil
	}
	return t.State.Name, ToneWait, nil
}

func describeLinear(ctx context.Context, l kanban.Link, acct orgs.Linear, cfg orgs.Config) (string, error) {
	t, err := openLinear(ctx, cfg, acct, l.Ref)
	if err != nil {
		return "", err
	}
	var labels []string
	for _, n := range t.Labels.Nodes {
		labels = append(labels, n.Name)
	}
	return fmt.Sprintf("%s (%s, priority %s, labels %s): %s\n%s", l.Ref, t.State.Name, cmp.Or(t.PriorityLabel, "none"),
		strings.Join(labels, ", "), t.Title, clip(t.Description)), nil
}

// linearAssigned is the open issues assigned to the account's user, of its
// teams when it lists them. ponytail: the first 100; pages when someone has more.
func linearAssigned(ctx context.Context, acct orgs.Linear, cfg orgs.Config) ([]Ticket, error) {
	if acct.Secret == (orgs.Secret{}) {
		return nil, nil
	}
	a, err := linear(ctx, cfg, acct, acct.Name)
	if err != nil {
		return nil, err
	}
	var v struct {
		Viewer struct {
			AssignedIssues struct {
				Nodes []struct {
					Identifier, Title, URL string
					State                  linearState
					Team                   struct{ Key string }
				}
			} `json:"assignedIssues"`
		}
	}
	q := `{ viewer { assignedIssues(first: 100, filter: { state: { type: { nin: ["completed", "canceled", "duplicate"] } } }) {
		nodes { identifier title url state { name } team { key } } } } }`
	if err := gql(ctx, a, q, nil, &v); err != nil {
		return nil, err
	}
	var out []Ticket
	for _, i := range v.Viewer.AssignedIssues.Nodes {
		if len(acct.Teams) == 0 || slices.Contains(acct.Teams, i.Team.Key) {
			out = append(out, Ticket{i.Team.Key, i.Identifier, i.Title, i.State.Name, i.URL})
		}
	}
	return out, nil
}

// linearTracker is the first Linear account with a team: where cards create tickets.
func linearTracker(cfg orgs.Config) (orgs.Linear, bool) {
	i := slices.IndexFunc(cfg.Linear, func(a orgs.Linear) bool { return len(a.Teams) > 0 })
	if i < 0 {
		return orgs.Linear{}, false
	}
	return cfg.Linear[i], true
}

// createLinear makes an issue in the account's first team, assigned to its user.
func createLinear(ctx context.Context, cfg orgs.Config, acct orgs.Linear, title, body string) (string, error) {
	team := acct.Teams[0]
	a, err := linear(ctx, cfg, acct, team)
	if err != nil {
		return "", err
	}
	var who struct {
		Viewer struct{ ID string }
		Teams  struct{ Nodes []struct{ ID string } }
	}
	if err := gql(ctx, a, `query($key: String!) { viewer { id } teams(filter: { key: { eq: $key } }) { nodes { id } } }`,
		map[string]any{"key": team}, &who); err != nil {
		return "", err
	}
	if len(who.Teams.Nodes) == 0 {
		return "", fmt.Errorf("linear has no team %s that this key sees", team)
	}
	var made struct {
		IssueCreate struct {
			Success bool
			Issue   struct{ URL string }
		} `json:"issueCreate"`
	}
	in := map[string]any{"teamId": who.Teams.Nodes[0].ID, "title": title, "description": body, "assigneeId": who.Viewer.ID}
	if err := gql(ctx, a, `mutation($input: IssueCreateInput!) { issueCreate(input: $input) { success issue { url } } }`,
		map[string]any{"input": in}, &made); err != nil {
		return "", err
	}
	if !made.IssueCreate.Success {
		return "", fmt.Errorf("linear didn't create it in %s", team)
	}
	return made.IssueCreate.Issue.URL, nil
}

// states is the issue's team's workflow states, in Linear's order.
func (t linearTicket) states(ctx context.Context) ([]linearState, error) {
	var v struct {
		Team struct {
			States struct{ Nodes []linearState } `json:"states"`
		} `json:"team"`
	}
	err := gql(ctx, t.a, `query($id: String!) { team(id: $id) { states(first: 100) { nodes { id name type position } } } }`,
		map[string]any{"id": t.Team.ID}, &v)
	s := v.Team.States.Nodes
	slices.SortStableFunc(s, func(a, b linearState) int { return cmp.Compare(a.Position, b.Position) })
	return s, err
}

func (t linearTicket) move(ctx context.Context, s linearState) error {
	var v struct {
		IssueUpdate struct{ Success bool } `json:"issueUpdate"`
	}
	err := gql(ctx, t.a, `mutation($id: String!, $state: String!) { issueUpdate(id: $id, input: { stateId: $state }) { success } }`,
		map[string]any{"id": t.ID, "state": s.ID}, &v)
	if err == nil && !v.IssueUpdate.Success {
		err = fmt.Errorf("linear didn't move %s to %s", t.Identifier, s.Name)
	}
	return err
}

// columnTypes are the state types a desk column moves an issue to, best first.
func columnTypes(column string) []string {
	switch column {
	case "Backlog":
		return []string{"backlog", "unstarted", "triage"}
	case "To do":
		return []string{"unstarted", "backlog", "triage"}
	case "Done":
		return []string{"completed"}
	}
	return []string{"started"} // In progress, Blocked
}

func ignores(list []string, status string) bool {
	return slices.ContainsFunc(list, func(s string) bool { return strings.EqualFold(s, status) })
}

// pushLinear is pushJira for a Linear issue: a state by name (the account's
// or the workflow's), a Blocked one, else the column's state type.
func pushLinear(ctx context.Context, l kanban.Link, column string, acct orgs.Linear, cfg orgs.Config, follow bool) (Synced, error) {
	t, err := openLinear(ctx, cfg, acct, l.Ref)
	if err != nil {
		return Synced{}, err
	}
	cur, want := t.State, category(column)
	seen := Synced{Seen: cur.Name}
	if ignores(acct.Ignore, cur.Name) {
		return seen, nil
	}
	name := cmp.Or(acct.Status[column], cfg.Workflow.Status[column])
	if name != "" && strings.EqualFold(cur.Name, name) || name == "" && column == "Blocked" && blocked(cur.Name) {
		return seen, nil
	}
	if follow {
		seen.Moved = changed(l.Seen, cur.Name) && (name != "" || linearCategory(cur.Type) != want || column != "Blocked" && blocked(cur.Name))
		return seen, nil
	}
	if !t.mine {
		return Synced{}, nil
	}
	if name == "" && column != "Blocked" && linearCategory(cur.Type) == want && !blocked(cur.Name) {
		return seen, nil
	}
	states, err := t.states(ctx)
	if err != nil {
		return Synced{}, err
	}
	pick := -1
	switch {
	case name != "":
		if pick = slices.IndexFunc(states, func(s linearState) bool { return strings.EqualFold(s.Name, name) }); pick < 0 {
			return Synced{}, fmt.Errorf("%s: its team has no state %s", l.Ref, name)
		}
	case column == "Blocked":
		if pick = slices.IndexFunc(states, func(s linearState) bool { return blocked(s.Name) }); pick < 0 && linearCategory(cur.Type) == want {
			return seen, nil
		}
	}
	for _, typ := range columnTypes(column) {
		if pick < 0 {
			pick = slices.IndexFunc(states, func(s linearState) bool { return s.Type == typ && !blocked(s.Name) })
		}
	}
	if pick < 0 {
		return Synced{}, fmt.Errorf("%s: its team has no %s state", l.Ref, strings.Join(columnTypes(column), " or "))
	}
	if err := t.move(ctx, states[pick]); err != nil {
		return Synced{}, err
	}
	return Synced{Now: states[pick].Name, Seen: states[pick].Name}, nil
}

// transitionLinear moves l to the state named status, as Transition.
func transitionLinear(ctx context.Context, l kanban.Link, status string, acct orgs.Linear, cfg orgs.Config) (string, error) {
	if off(acct.Sync) {
		return "", fmt.Errorf("%s: its Linear account has sync = false: desk moves none of its tickets", l.Ref)
	}
	t, err := openLinear(ctx, cfg, acct, l.Ref)
	if err != nil {
		return "", err
	}
	switch {
	case strings.EqualFold(t.State.Name, status):
		return t.State.Name, nil
	case ignores(acct.Ignore, t.State.Name):
		return "", fmt.Errorf("%s is %s, a status its account leaves to others (ignore): desk doesn't move it from there", l.Ref, t.State.Name)
	case !t.mine:
		return "", fmt.Errorf("%s isn't assigned to the user: desk moves only their tickets", l.Ref)
	}
	states, err := t.states(ctx)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(states, func(s linearState) bool { return strings.EqualFold(s.Name, status) })
	if i < 0 {
		var names []string
		for _, s := range states {
			names = append(names, s.Name)
		}
		return "", fmt.Errorf("%s's team has no state %s; it has: %s", l.Ref, status, strings.Join(names, ", "))
	}
	if err := t.move(ctx, states[i]); err != nil {
		return "", err
	}
	return states[i].Name, nil
}

// linearStatuses is Statuses' lines for the account's teams: "ENG: Todo (unstarted), …".
func linearStatuses(ctx context.Context, acct orgs.Linear, cfg orgs.Config, b *strings.Builder) error {
	a, err := linear(ctx, cfg, acct, acct.Name)
	if err != nil {
		return err
	}
	type teamState struct {
		linearState
		Team struct{ Key string } `json:"team"`
	}
	var v struct {
		WorkflowStates struct{ Nodes []teamState } `json:"workflowStates"`
	}
	q := `query($teams: [String!]) { workflowStates(first: 250, filter: { team: { key: { in: $teams } } }) { nodes { name type position team { key } } } }`
	if err := gql(ctx, a, q, map[string]any{"teams": acct.Teams}, &v); err != nil {
		return err
	}
	ns := v.WorkflowStates.Nodes
	slices.SortStableFunc(ns, func(x, y teamState) int { return cmp.Compare(x.Position, y.Position) })
	for _, team := range acct.Teams {
		var seen []string
		for _, s := range ns {
			if s.Team.Key == team {
				seen = append(seen, s.Name+" ("+s.Type+")")
			}
		}
		fmt.Fprintf(b, "%s: %s\n", team, strings.Join(seen, ", "))
	}
	return nil
}
