package links

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
)

// Describe is what l says, for Prompt Optimization: a Jira or Linear
// issue's or a GitHub issue's or PR's title, kind, state and description, as text.
// Other kinds give "". ponytail: Jira and GitHub only; GitLab and
// Bitbucket when a request leans on them.
func Describe(ctx context.Context, l kanban.Link, cfg orgs.Config) (string, error) {
	u, err := url.Parse(l.URL)
	if err != nil {
		return "", err
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch l.Kind {
	case KindJira:
		j, ok := jiraFor(cfg, u.Hostname(), l.Ref)
		if !ok {
			return "", fmt.Errorf("no [[jira]] for %s in desk.toml", u.Host)
		}
		a, err := auth(ctx, cfg, j.Email, j.Secret)
		if err != nil {
			return "", err
		}
		base := strings.TrimRight(j.URL, "/")
		if base == "" {
			base = u.Scheme + "://" + u.Host
		}
		var v struct {
			Fields struct {
				Summary, Description string // API v2: the description as wiki text, not v3's document tree
				Status, Issuetype    struct{ Name string }
				Priority             *struct{ Name string }
				Labels               []string
			}
		}
		if err := getJSON(ctx, base+"/rest/api/2/issue/"+url.PathEscape(l.Ref)+"?fields=summary,description,status,issuetype,priority,labels", a, &v); err != nil {
			return "", err
		}
		f := v.Fields
		prio := ""
		if f.Priority != nil {
			prio = ", priority " + f.Priority.Name
		}
		return fmt.Sprintf("%s (%s, %s%s, labels %s): %s\n%s", l.Ref, f.Issuetype.Name, f.Status.Name, prio, strings.Join(f.Labels, ", "), f.Summary, clip(f.Description)), nil
	case KindLinear:
		a, ok := linearAcct(cfg, l)
		if !ok {
			return "", &NoAccountError{"Linear", l.Ref}
		}
		return describeLinear(ctx, l, a, cfg)
	case KindGitHubIssue, KindGitHubPR:
		repo, a, err := githubRepo(ctx, u, p, cfg)
		if err != nil {
			return "", err
		}
		var v struct{ Title, State, Body string }
		if err := getJSON(ctx, repo+"/issues/"+p[3], a, &v); err != nil { // the issues API answers for PRs too
			return "", err
		}
		return fmt.Sprintf("%s/%s %s (%s): %s\n%s", p[0], p[1], l.Ref, v.State, v.Title, clip(v.Body)), nil
	}
	return "", nil
}

// clip keeps a description to what a prompt needs.
func clip(s string) string {
	if r := []rune(strings.TrimSpace(s)); len(r) > 4000 {
		return string(r[:4000]) + "…"
	}
	return strings.TrimSpace(s)
}
