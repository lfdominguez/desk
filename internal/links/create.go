package links

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	adf "github.com/nyactl/go-adf"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
)

// Tracker is where a card of cfg's organization creates its ticket: the
// first Jira account's first project, else the first Linear account's first
// team, else the first GitHub account with a repo; "" when it has none.
func Tracker(cfg orgs.Config) string {
	if j, ok := jiraTracker(cfg); ok {
		return j.Projects[0] + " (Jira)"
	}
	if a, ok := linearTracker(cfg); ok {
		return a.Teams[0] + " (Linear)"
	}
	if g, ok := githubTracker(cfg); ok {
		return g.Repo + " (GitHub)"
	}
	return ""
}

func jiraTracker(cfg orgs.Config) (orgs.Jira, bool) {
	i := slices.IndexFunc(cfg.Jira, func(j orgs.Jira) bool { return j.URL != "" && len(j.Projects) > 0 })
	if i < 0 {
		return orgs.Jira{}, false
	}
	return cfg.Jira[i], true
}

func githubTracker(cfg orgs.Config) (orgs.GitHub, bool) {
	i := slices.IndexFunc(cfg.GitHub, func(g orgs.GitHub) bool { return strings.Count(g.Repo, "/") == 1 })
	if i < 0 {
		return orgs.GitHub{}, false
	}
	return cfg.GitHub[i], true
}

// Create opens a ticket titled title, body its description, in the
// organization's Tracker, assigned to the user (so sync moves it), and
// returns the link to it.
func Create(ctx context.Context, cfg orgs.Config, title, body string) (kanban.Link, error) {
	var at string
	var err error
	if j, ok := jiraTracker(cfg); ok {
		at, err = createJira(ctx, cfg, j, title, body)
	} else if a, ok := linearTracker(cfg); ok {
		at, err = createLinear(ctx, cfg, a, title, body)
	} else if g, ok := githubTracker(cfg); ok {
		at, err = createGitHub(ctx, cfg, g, title, body)
	} else {
		return kanban.Link{}, fmt.Errorf("no tracker to create it in: give a Jira account projects, a Linear account teams, or a GitHub account a repo, in desk.toml")
	}
	if err != nil {
		return kanban.Link{}, err
	}
	l, ok := Parse(at, cfg)
	if !ok {
		return kanban.Link{}, fmt.Errorf("created %s, but it isn't a link desk tracks", at)
	}
	return l, nil
}

// jiraMe is the account id the token belongs to.
func jiraMe(ctx context.Context, base string, a func(*http.Request)) (string, error) {
	var me struct {
		AccountID string `json:"accountId"`
	}
	err := getJSON(ctx, base+"/rest/api/3/myself", a, &me)
	return me.AccountID, err
}

func createJira(ctx context.Context, cfg orgs.Config, j orgs.Jira, title, body string) (string, error) {
	a, err := auth(ctx, cfg, j.Email, j.Secret)
	if err != nil {
		return "", err
	}
	base, project := strings.TrimRight(j.URL, "/"), j.Projects[0]
	type issueType struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Subtask bool   `json:"subtask"`
	}
	var meta struct {
		IssueTypes []issueType `json:"issueTypes"`
		Values     []issueType `json:"values"` // what some Jira versions call it
	}
	if err := getJSON(ctx, base+"/rest/api/3/issue/createmeta/"+url.PathEscape(project)+"/issuetypes", a, &meta); err != nil {
		return "", err
	}
	types := append(meta.IssueTypes, meta.Values...)
	pick := -1
	for _, want := range []string{j.IssueType, "Task", "Tarea"} {
		if pick = slices.IndexFunc(types, func(t issueType) bool { return want != "" && strings.EqualFold(t.Name, want) }); pick >= 0 {
			break
		}
	}
	if pick < 0 && j.IssueType == "" {
		pick = slices.IndexFunc(types, func(t issueType) bool { return !t.Subtask })
	}
	if pick < 0 {
		return "", fmt.Errorf("%s has no issue type %q", project, j.IssueType)
	}
	me, err := jiraMe(ctx, base, a)
	if err != nil {
		return "", err
	}
	desc, err := adf.FromMarkdown(body) // Jira Cloud takes only its own document format: Claude writes Markdown
	if err != nil {
		return "", fmt.Errorf("description: %w", err)
	}
	var made struct {
		Key string `json:"key"`
	}
	fields := map[string]any{
		"project":     map[string]string{"key": project},
		"summary":     title,
		"issuetype":   map[string]string{"id": types[pick].ID},
		"description": desc,
		"assignee":    map[string]string{"accountId": me},
	}
	if err := doJSON(ctx, "POST", base+"/rest/api/3/issue", a, map[string]any{"fields": fields}, &made); err != nil {
		return "", err
	}
	return base + "/browse/" + made.Key, nil
}

func createGitHub(ctx context.Context, cfg orgs.Config, g orgs.GitHub, title, body string) (string, error) {
	a, err := auth(ctx, cfg, "", g.Secret)
	if err != nil {
		return "", err
	}
	api := githubAPI
	if g.Host != "" && g.Host != "github.com" {
		api = "https://" + g.Host + "/api/v3"
	}
	var me struct {
		Login string `json:"login"`
	}
	if err := getJSON(ctx, api+"/user", a, &me); err != nil {
		return "", err
	}
	var made struct {
		URL string `json:"html_url"`
	}
	in := map[string]any{"title": title, "body": body, "assignees": []string{me.Login}}
	if err := doJSON(ctx, "POST", api+"/repos/"+g.Repo+"/issues", a, in, &made); err != nil {
		return "", err
	}
	return made.URL, nil
}
