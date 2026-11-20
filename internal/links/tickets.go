package links

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/lfdominguez/desk/internal/orgs"
)

// Ticket is an open issue assigned to the user, in one of the org's Jira,
// Linear, GitHub or GitLab accounts.
type Ticket struct {
	Project string // Jira's project key, Linear's team key, GitHub's owner/repo, GitLab's group/project
	Key     string // ABC-12, #34
	Title   string
	Status  string
	URL     string
}

// Assigned is every open ticket assigned to the user in cfg's accounts,
// by project; what some accounts gave when others failed (a locked vault
// comes back as a *vault.LockedError among the errors). Accounts without
// a token have no "me" and are skipped.
// ponytail: the first 100 of each account; pages when someone has more.
func Assigned(ctx context.Context, cfg orgs.Config) ([]Ticket, error) {
	var out []Ticket
	var errs []error
	add := func(ts []Ticket, err error) {
		out = append(out, ts...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	for _, j := range cfg.Jira {
		add(jiraAssigned(ctx, j, cfg))
	}
	for _, a := range cfg.Linear {
		add(linearAssigned(ctx, a, cfg))
	}
	for _, g := range cfg.GitHub {
		add(githubAssigned(ctx, g, cfg))
	}
	for _, g := range cfg.GitLab {
		add(gitlabAssigned(ctx, g, cfg))
	}
	slices.SortStableFunc(out, func(a, b Ticket) int { return strings.Compare(a.Project, b.Project) })
	return out, errors.Join(errs...)
}

func jiraAssigned(ctx context.Context, j orgs.Jira, cfg orgs.Config) ([]Ticket, error) {
	if j.URL == "" || j.Secret == (orgs.Secret{}) {
		return nil, nil
	}
	a, err := auth(ctx, cfg, j.Email, j.Secret)
	if err != nil {
		return nil, err
	}
	// ponytail: Jira Cloud's search; Server/Data Center has /rest/api/2/search instead.
	base := strings.TrimRight(j.URL, "/")
	q := url.Values{"jql": {"assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC"},
		"fields": {"summary,status,project"}, "maxResults": {"100"}}
	var v struct {
		Issues []struct {
			Key    string
			Fields struct {
				Summary string
				Status  struct{ Name string }
				Project struct{ Key string }
			}
		}
	}
	if err := getJSON(ctx, base+"/rest/api/3/search/jql?"+q.Encode(), a, &v); err != nil {
		return nil, err
	}
	var out []Ticket
	for _, i := range v.Issues {
		out = append(out, Ticket{i.Fields.Project.Key, i.Key, i.Fields.Summary, i.Fields.Status.Name, base + "/browse/" + i.Key})
	}
	return out, nil
}

func githubAssigned(ctx context.Context, g orgs.GitHub, cfg orgs.Config) ([]Ticket, error) {
	if g.Secret == (orgs.Secret{}) {
		return nil, nil
	}
	a, err := auth(ctx, cfg, "", g.Secret)
	if err != nil {
		return nil, err
	}
	api := githubAPI
	if g.Host != "" && g.Host != "github.com" {
		api = "https://" + g.Host + "/api/v3"
	}
	var v []struct {
		Number      int
		Title       string
		HTMLURL     string `json:"html_url"`
		PullRequest any    `json:"pull_request"`
		Repository  struct {
			FullName string `json:"full_name"`
			Owner    struct{ Login string }
		}
	}
	if err := getJSON(ctx, api+"/issues?filter=assigned&state=open&per_page=100", a, &v); err != nil {
		return nil, err
	}
	var out []Ticket
	for _, i := range v {
		if i.PullRequest != nil || (len(g.Owners) > 0 && !slices.Contains(g.Owners, i.Repository.Owner.Login)) {
			continue
		}
		out = append(out, Ticket{i.Repository.FullName, fmt.Sprint("#", i.Number), i.Title, "open", i.HTMLURL})
	}
	return out, nil
}

func gitlabAssigned(ctx context.Context, g orgs.GitLab, cfg orgs.Config) ([]Ticket, error) {
	tok, err := secret(ctx, cfg, g.Secret)
	if err != nil || tok == "" {
		return nil, err
	}
	a := func(r *http.Request) { r.Header.Set("PRIVATE-TOKEN", tok) }
	var v []struct {
		IID        int `json:"iid"`
		Title      string
		WebURL     string `json:"web_url"`
		References struct{ Full string }
	}
	base := strings.TrimRight(cmp.Or(g.URL, "https://gitlab.com"), "/")
	if err := getJSON(ctx, base+"/api/v4/issues?scope=assigned_to_me&state=opened&per_page=100", a, &v); err != nil {
		return nil, err
	}
	var out []Ticket
	for _, i := range v {
		project, _, _ := strings.Cut(i.References.Full, "#")
		if len(g.Groups) > 0 && !slices.ContainsFunc(g.Groups, func(gr string) bool { return strings.HasPrefix(project, gr+"/") }) {
			continue
		}
		out = append(out, Ticket{project, fmt.Sprint("#", i.IID), i.Title, "open", i.WebURL})
	}
	return out, nil
}
