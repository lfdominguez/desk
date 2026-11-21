package links

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
)

// fakeLin is Linear's GraphQL API over one issue, ENG-1, of team ENG.
type fakeLin struct {
	state    string // ENG-1's state, by name
	assignee string // "me", someone else's id, or "" nobody
	fail     string // a query holding this answers with an error
	auth     []string
	moves    []string
	made     map[string]any
	noTeam   bool
	notMade  bool
	noMove   bool
	comment  string // commentCreate's body
}

var linStates = []linearState{{"s3", "In Progress", "started", 2}, {"s1", "Backlog", "backlog", 0}, {"s2", "Todo", "unstarted", 1},
	{"s4", "Blocked", "started", 3}, {"s5", "In Review", "started", 4}, {"s6", "Done", "completed", 5}, {"s7", "Canceled", "canceled", 6}}

func (f *fakeLin) stateOf(name string) linearState {
	for _, s := range linStates {
		if s.Name == name {
			return s
		}
	}
	return linearState{}
}

func (f *fakeLin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	var in struct {
		Query     string
		Variables map[string]any
	}
	json.NewDecoder(r.Body).Decode(&in)
	q, data := in.Query, map[string]any{}
	if f.fail != "" && strings.Contains(q, f.fail) {
		json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"message": "Entity not found"}}})
		return
	}
	switch {
	case strings.Contains(q, "commentCreate"):
		f.comment = fmt.Sprint(in.Variables["body"])
		data["commentCreate"] = map[string]bool{"success": !f.notMade && in.Variables["id"] == "uuid-1"}
	case strings.Contains(q, "issueCreate"):
		f.made = in.Variables["input"].(map[string]any)
		data["issueCreate"] = map[string]any{"success": !f.notMade, "issue": map[string]string{"url": "https://linear.app/acme/issue/ENG-9/new"}}
	case strings.Contains(q, "issueUpdate"):
		for _, s := range linStates {
			if s.ID == in.Variables["state"] {
				f.state = s.Name
				f.moves = append(f.moves, s.Name)
			}
		}
		data["issueUpdate"] = map[string]bool{"success": !f.noMove && in.Variables["id"] == "uuid-1"}
	case strings.Contains(q, "assignedIssues"):
		data["viewer"] = map[string]any{"assignedIssues": map[string]any{"nodes": []map[string]any{
			{"identifier": "ENG-1", "title": "Fix login", "url": "https://linear.app/acme/issue/ENG-1/fix-login", "state": map[string]string{"name": f.state}, "team": map[string]string{"key": "ENG"}},
			{"identifier": "OPS-4", "title": "Rotate keys", "url": "https://linear.app/acme/issue/OPS-4", "state": map[string]string{"name": "Todo"}, "team": map[string]string{"key": "OPS"}},
		}}}
	case strings.Contains(q, "workflowStates"):
		var ns []map[string]any
		for _, s := range linStates {
			ns = append(ns, map[string]any{"name": s.Name, "type": s.Type, "position": s.Position, "team": map[string]string{"key": "ENG"}})
		}
		data["workflowStates"] = map[string]any{"nodes": ns}
	case strings.Contains(q, "team(id"):
		data["team"] = map[string]any{"states": map[string]any{"nodes": linStates}}
	case strings.Contains(q, "teams(filter"):
		nodes := []map[string]string{{"id": "team-1"}}
		if f.noTeam {
			nodes = nil
		}
		data["viewer"], data["teams"] = map[string]string{"id": "me"}, map[string]any{"nodes": nodes}
	case strings.Contains(q, "issue(id"):
		is := map[string]any{"id": "uuid-1", "identifier": "ENG-1", "title": "Fix login", "description": "It **fails**", "priorityLabel": "High",
			"state": f.stateOf(f.state), "team": map[string]string{"id": "team-1", "key": "ENG"},
			"labels": map[string]any{"nodes": []map[string]string{{"name": "bug"}}}}
		if f.assignee != "" {
			is["assignee"] = map[string]string{"id": f.assignee}
		}
		data["viewer"], data["issue"] = map[string]string{"id": "me"}, is
	}
	json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// useLinear points linearAPI at a fake over ENG-1 in state, assigned to
// assignee; cfg has one Linear account for workspace acme, team ENG.
func useLinear(t *testing.T, state, assignee string) (*fakeLin, orgs.Config, kanban.Link) {
	f := &fakeLin{state: state, assignee: assignee}
	srv := httptest.NewServer(f)
	old := linearAPI
	linearAPI = srv.URL
	t.Cleanup(func() { linearAPI = old; srv.Close() })
	cfg := orgs.Config{Linear: []orgs.Linear{{Name: "acme", Workspace: "acme", Teams: []string{"ENG"}, Secret: orgs.Secret{Token: "lin_api_x"}}}}
	return f, cfg, kanban.Link{URL: "https://linear.app/acme/issue/ENG-1/fix-login", Kind: KindLinear, Ref: "ENG-1"}
}

// TestParseLinear: an issue URL is Linear's whatever the slug; a bare key
// is the Linear account's that lists its team, else Jira's, else the only
// Linear account's.
func TestParseLinear(t *testing.T) {
	lin := orgs.Linear{Workspace: "acme", Teams: []string{"ENG"}}
	jira := orgs.Jira{URL: "https://acme.atlassian.net", Projects: []string{"ABC"}}
	for _, c := range []struct {
		in        string
		cfg       orgs.Config
		url, kind string
	}{
		{"https://linear.app/acme/issue/eng-12/fix-it", orgs.Config{}, "https://linear.app/acme/issue/eng-12/fix-it", KindLinear},
		{"https://linear.app/acme/team/ENG/active", orgs.Config{}, "https://linear.app/acme/team/ENG/active", KindURL},
		{"ENG-12", orgs.Config{Linear: []orgs.Linear{lin}, Jira: []orgs.Jira{jira}}, "https://linear.app/acme/issue/ENG-12", KindLinear},
		{"ABC-3", orgs.Config{Linear: []orgs.Linear{lin}, Jira: []orgs.Jira{jira}}, "https://acme.atlassian.net/browse/ABC-3", KindJira},
		{"OPS-3", orgs.Config{Linear: []orgs.Linear{lin}}, "https://linear.app/acme/issue/OPS-3", KindLinear},
		{"OPS-3", orgs.Config{Linear: []orgs.Linear{{Teams: []string{"OPS"}}}}, "", ""},
	} {
		l, ok := Parse(c.in, c.cfg)
		if l.URL != c.url || l.Kind != c.kind || ok != (c.kind != "") {
			t.Errorf("%s: %+v %v", c.in, l, ok)
		}
	}
	if l, _ := Parse("https://linear.app/acme/issue/eng-12/fix-it", orgs.Config{}); l.Ref != "ENG-12" {
		t.Errorf("ref %q", l.Ref)
	}
}

// TestLinearFor: the workspace's account, else the team's, else the only one.
func TestLinearFor(t *testing.T) {
	a, b := orgs.Linear{Name: "a", Workspace: "acme", Teams: []string{"ENG"}}, orgs.Linear{Name: "b", Workspace: "globex", Teams: []string{"OPS"}}
	cfg := orgs.Config{Linear: []orgs.Linear{a, b}}
	for _, c := range []struct{ ws, key, want string }{{"GLOBEX", "ENG-1", "b"}, {"", "OPS-1", "b"}, {"", "XYZ-1", ""}} {
		if got, _ := linearFor(cfg, c.ws, c.key); got.Name != c.want {
			t.Errorf("%s %s: %q", c.ws, c.key, got.Name)
		}
	}
	if got, ok := linearAcct(orgs.Config{Linear: []orgs.Linear{a}}, kanban.Link{URL: "%zz", Ref: "Q-1"}); !ok || got.Name != "a" {
		t.Errorf("only one: %+v", got)
	}
}

// TestCheckLinear: a state's name, done when closed; the key as is, an
// OAuth token as Bearer; no account or no key asks nothing.
func TestCheckLinear(t *testing.T) {
	f, cfg, l := useLinear(t, "In Progress", "me")
	ctx := context.Background()
	if s, tone, err := Check(ctx, l, cfg); s != "In Progress" || tone != ToneWait || err != nil {
		t.Errorf("open: %q %q %v", s, tone, err)
	}
	f.state = "Canceled"
	cfg.Linear[0].Token = "oauth-token"
	if s, tone, _ := Check(ctx, l, cfg); s != "Canceled" || tone != ToneDone {
		t.Errorf("closed: %q %q", s, tone)
	}
	if f.auth[0] != "lin_api_x" || f.auth[1] != "Bearer oauth-token" {
		t.Errorf("auth %q", f.auth)
	}
	var na *NoAccountError
	if _, _, err := Check(ctx, l, orgs.Config{}); !errors.As(err, &na) {
		t.Errorf("no account: %v", err)
	}
	if _, _, err := Check(ctx, l, orgs.Config{Linear: []orgs.Linear{{Workspace: "acme"}}}); !errors.As(err, &na) {
		t.Errorf("no key: %v", err)
	}
	if _, _, err := Check(ctx, l, orgs.Config{Linear: []orgs.Linear{{Secret: badSecret}}}); err == nil {
		t.Error("a missing vault checks")
	}
	f.fail = "issue(id"
	if _, _, err := Check(ctx, l, cfg); err == nil || !strings.Contains(err.Error(), "Entity not found") {
		t.Errorf("errors: %v", err)
	}
	linearAPI = dead()
	if _, _, err := Check(ctx, l, cfg); err == nil {
		t.Error("nothing answered")
	}
}

// TestDescribeLinear: the issue's state, priority, labels, title and Markdown.
func TestDescribeLinear(t *testing.T) {
	f, cfg, l := useLinear(t, "Todo", "")
	ctx := context.Background()
	if s, err := Describe(ctx, l, cfg); err != nil || s != "ENG-1 (Todo, priority High, labels bug): Fix login\nIt **fails**" {
		t.Errorf("%q %v", s, err)
	}
	if _, err := Describe(ctx, l, orgs.Config{}); err == nil {
		t.Error("no account describes")
	}
	f.fail = "issue(id"
	if _, err := Describe(ctx, l, cfg); err == nil {
		t.Error("an error describes")
	}
}

// TestAssignedLinear: the user's open issues, of the account's teams.
func TestAssignedLinear(t *testing.T) {
	f, cfg, _ := useLinear(t, "Todo", "me")
	ctx := context.Background()
	ts, err := Assigned(ctx, cfg)
	if err != nil || len(ts) != 1 || ts[0] != (Ticket{"ENG", "ENG-1", "Fix login", "Todo", "https://linear.app/acme/issue/ENG-1/fix-login"}) {
		t.Errorf("%+v %v", ts, err)
	}
	cfg.Linear[0].Teams = nil
	if ts, _ := Assigned(ctx, cfg); len(ts) != 2 {
		t.Errorf("all teams: %+v", ts)
	}
	if ts, err := Assigned(ctx, orgs.Config{Linear: []orgs.Linear{{}}}); ts != nil || err != nil {
		t.Errorf("no key: %+v %v", ts, err)
	}
	if _, err := Assigned(ctx, orgs.Config{Linear: []orgs.Linear{{Secret: badSecret}}}); err == nil {
		t.Error("a missing vault lists")
	}
	f.fail = "assignedIssues"
	if _, err := Assigned(ctx, cfg); err == nil {
		t.Error("an error lists")
	}
}

// TestCreateLinear: a card's ticket goes in the first team, assigned to the
// user, after Jira and before GitHub.
func TestCreateLinear(t *testing.T) {
	f, cfg, _ := useLinear(t, "Todo", "me")
	ctx := context.Background()
	if tr := Tracker(cfg); tr != "ENG (Linear)" {
		t.Errorf("tracker %q", tr)
	}
	if tr := Tracker(orgs.Config{Linear: cfg.Linear, Jira: []orgs.Jira{{URL: "https://x", Projects: []string{"ABC"}}}}); tr != "ABC (Jira)" {
		t.Errorf("jira first: %q", tr)
	}
	l, err := Create(ctx, cfg, "New thing", "Do **it**")
	if err != nil || l.Kind != KindLinear || l.Ref != "ENG-9" {
		t.Fatalf("%+v %v", l, err)
	}
	if f.made["teamId"] != "team-1" || f.made["assigneeId"] != "me" || f.made["description"] != "Do **it**" || f.made["title"] != "New thing" {
		t.Errorf("made %v", f.made)
	}
	for _, c := range []struct {
		set  func()
		want string
	}{
		{func() { f.noTeam = true }, "no team ENG"},
		{func() { f.noTeam, f.notMade = false, true }, "didn't create"},
		{func() { f.notMade, f.fail = false, "teams(filter" }, "Entity"},
		{func() { f.fail = "issueCreate" }, "Entity"},
	} {
		c.set()
		if _, err := Create(ctx, cfg, "t", "b"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("want %q: %v", c.want, err)
		}
	}
	if _, err := Create(ctx, orgs.Config{Linear: []orgs.Linear{{Teams: []string{"ENG"}}}}, "t", "b"); err == nil {
		t.Error("no key creates")
	}
}

// TestPushLinear: a card's column moves its issue to the column's state
// type (Blocked: a state named so), or the state its status table names;
// only the user's, not from an ignored state, nor with sync off; with a
// workflow, a state someone moved is followed instead.
func TestPushLinear(t *testing.T) {
	f, cfg, l := useLinear(t, "Todo", "me")
	ctx := context.Background()
	for _, c := range []struct{ column, now string }{
		{"Backlog", ""}, {"In progress", "In Progress"}, {"Blocked", "Blocked"}, {"In progress", "In Progress"}, {"Done", "Done"}, {"To do", "Todo"},
	} {
		if now, err := Push(ctx, l, c.column, cfg); err != nil || now != c.now {
			t.Errorf("%s: %q %v", c.column, now, err)
		}
	}
	cfg.Linear[0].Status = map[string]string{"Done": "In Review"}
	if now, _ := Push(ctx, l, "Done", cfg); now != "In Review" {
		t.Errorf("status table: %q", now)
	}
	if now, err := Push(ctx, l, "Done", cfg); now != "" || err != nil {
		t.Errorf("already there: %q %v", now, err)
	}
	cfg.Linear[0].Status = map[string]string{"Done": "Shipped"}
	if _, err := Push(ctx, l, "Done", cfg); err == nil || !strings.Contains(err.Error(), "no state Shipped") {
		t.Errorf("unknown status: %v", err)
	}
	cfg.Linear[0].Status = nil
	f.assignee = "someone"
	if now, err := Push(ctx, l, "Done", cfg); now != "" || err != nil {
		t.Errorf("not mine: %q %v", now, err)
	}
	f.assignee, cfg.Linear[0].Ignore = "me", []string{"in review"}
	if now, _ := Push(ctx, l, "Done", cfg); now != "" {
		t.Errorf("ignored: %q", now)
	}
	cfg.Linear[0].Ignore = nil
	no := false
	cfg.Linear[0].Sync = &no
	if now, _ := Push(ctx, l, "Done", cfg); now != "" {
		t.Errorf("sync off: %q", now)
	}
	cfg.Linear[0].Sync = nil
	f.fail = "team(id"
	if _, err := Push(ctx, l, "Done", cfg); err == nil {
		t.Error("states unread moves")
	}
	f.fail = "issue(id"
	if _, err := Push(ctx, l, "Done", cfg); err == nil {
		t.Error("issue unread moves")
	}
	f.fail, f.state, f.noMove = "", "Todo", true
	if _, err := Push(ctx, l, "Done", cfg); err == nil || !strings.Contains(err.Error(), "didn't move") {
		t.Errorf("not moved: %v", err)
	}
	f.noMove = false
	if _, err := Push(ctx, l, "Done", cfg); err != nil {
		t.Errorf("moved: %v", err)
	}
	cfg.Workflow = orgs.Workflow{Rules: "follow the ticket"}
	l.Seen = "In Progress"
	if s, err := Sync(ctx, l, "In progress", cfg); err != nil || !s.Moved || s.Seen != "Done" {
		t.Errorf("follow: %+v %v", s, err)
	}
}

// TestPushLinearOdd: a team without the column's state type, or without a
// Blocked state, says so or stays.
func TestPushLinearOdd(t *testing.T) {
	f, cfg, l := useLinear(t, "In Review", "me")
	ctx := context.Background()
	old := linStates
	t.Cleanup(func() { linStates = old })
	linStates = []linearState{{"s5", "In Review", "started", 4}, {"s6", "Done", "completed", 5}}
	if now, err := Push(ctx, l, "Blocked", cfg); now != "" || err != nil {
		t.Errorf("no Blocked state: %q %v", now, err)
	}
	if _, err := Push(ctx, l, "Backlog", cfg); err == nil || !strings.Contains(err.Error(), "no backlog or unstarted or triage state") {
		t.Errorf("no state type: %v", err)
	}
	f.state = "Done"
	if now, err := Push(ctx, l, "Blocked", cfg); now != "In Review" || err != nil {
		t.Errorf("no Blocked state, from Done: %q %v", now, err)
	}
}

// TestTransitionLinear: ticket_status moves the user's issue to a state of
// its team by name, or says what states there are.
func TestTransitionLinear(t *testing.T) {
	f, cfg, l := useLinear(t, "Todo", "me")
	ctx := context.Background()
	if s, err := Transition(ctx, l, "in review", cfg); err != nil || s != "In Review" || f.state != "In Review" {
		t.Errorf("%q %v", s, err)
	}
	if s, err := Transition(ctx, l, "In Review", cfg); err != nil || s != "In Review" || len(f.moves) != 1 {
		t.Errorf("there already: %q %v", s, err)
	}
	if _, err := Transition(ctx, l, "Shipped", cfg); err == nil || !strings.Contains(err.Error(), "it has: Backlog, Todo, In Progress") {
		t.Errorf("unknown: %v", err)
	}
	f.assignee = ""
	if _, err := Transition(ctx, l, "Done", cfg); err == nil || !strings.Contains(err.Error(), "isn't assigned") {
		t.Errorf("not mine: %v", err)
	}
	f.assignee, cfg.Linear[0].Ignore = "me", []string{"In Review"}
	if _, err := Transition(ctx, l, "Done", cfg); err == nil || !strings.Contains(err.Error(), "leaves to others") {
		t.Errorf("ignored: %v", err)
	}
	cfg.Linear[0].Ignore = nil
	for _, fail := range []string{"team(id", "issue(id", "issueUpdate"} {
		f.fail = fail
		if _, err := Transition(ctx, l, "Done", cfg); err == nil {
			t.Errorf("%s failing moves", fail)
		}
	}
	f.fail = ""
	no := false
	cfg.Linear[0].Sync = &no
	if _, err := Transition(ctx, l, "Done", cfg); err == nil || !strings.Contains(err.Error(), "sync = false") {
		t.Errorf("sync off: %v", err)
	}
	var na *NoAccountError
	if _, err := Transition(ctx, l, "Done", orgs.Config{}); !errors.As(err, &na) {
		t.Errorf("no account: %v", err)
	}
}

// TestStatusesLinear: each team's states with their types, in order.
func TestStatusesLinear(t *testing.T) {
	f, cfg, _ := useLinear(t, "Todo", "me")
	ctx := context.Background()
	cfg.Linear = append(cfg.Linear, orgs.Linear{})
	s, err := Statuses(ctx, cfg)
	if err != nil || !strings.HasPrefix(s, "ENG: Backlog (backlog), Todo (unstarted), In Progress (started), Blocked (started)") {
		t.Errorf("%q %v", s, err)
	}
	f.fail = "workflowStates"
	if _, err := Statuses(ctx, cfg); err == nil {
		t.Error("an error lists")
	}
	if _, err := Statuses(ctx, orgs.Config{Linear: []orgs.Linear{{Teams: []string{"X"}, Secret: badSecret}}}); err == nil {
		t.Error("a missing vault lists")
	}
}
