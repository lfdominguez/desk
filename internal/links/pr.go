package links

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
)

// OpenPR opens a pull request (GitHub) or merge request (GitLab) from head
// into base on the repository whose git remote is remote, through the
// organization's account for it, and gives it as the card's link.
func OpenPR(ctx context.Context, cfg orgs.Config, remote, head, base, title, body string) (kanban.Link, error) {
	u, p, err := remoteRepo(remote)
	if err != nil {
		return kanban.Link{}, err
	}
	var web string
	if gitlabHost(cfg, u.Hostname()) {
		api, a, err := gitlabProject(ctx, u, append(p, "-"), cfg)
		if err != nil {
			return kanban.Link{}, err
		}
		var made struct {
			URL string `json:"web_url"`
		}
		in := map[string]any{"source_branch": head, "target_branch": base, "title": title, "description": body, "remove_source_branch": true}
		if err := doJSON(ctx, "POST", api+"/merge_requests", a, in, &made); err != nil {
			return kanban.Link{}, err
		}
		web = made.URL
	} else {
		repo, a, err := githubRepo(ctx, u, p, cfg)
		if err != nil {
			return kanban.Link{}, err
		}
		var made struct {
			URL string `json:"html_url"`
		}
		in := map[string]any{"head": head, "base": base, "title": title, "body": body}
		if err := doJSON(ctx, "POST", repo+"/pulls", a, in, &made); err != nil {
			return kanban.Link{}, err
		}
		web = made.URL
	}
	l, ok := Parse(web, cfg)
	if !ok {
		return kanban.Link{}, fmt.Errorf("the new request's URL %q isn't one desk reads", web)
	}
	return l, nil
}

// gitlabHost says whether host is a GitLab: gitlab.com or one of the
// organization's [[gitlab]] URLs.
func gitlabHost(cfg orgs.Config, host string) bool {
	if host == "gitlab.com" {
		return true
	}
	for _, g := range cfg.GitLab {
		if u, err := url.Parse(g.URL); err == nil && u.Hostname() == host {
			return true
		}
	}
	return false
}

// remoteRepo is a git remote (https://host/a/b.git, git@host:a/b.git,
// ssh://git@host:22/a/b.git) as the web URL of its repository and the
// repository's path segments.
func remoteRepo(remote string) (*url.URL, []string, error) {
	r := strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	if !strings.Contains(r, "://") {
		if at, path, ok := strings.Cut(r, ":"); ok { // scp-like: [user@]host:path
			_, host, _ := strings.Cut(at, "@")
			if host == "" {
				host = at
			}
			r = "ssh://" + host + "/" + path
		}
	}
	u, err := url.Parse(r)
	if err != nil || u.Hostname() == "" {
		return nil, nil, fmt.Errorf("%q is not a remote desk can open a request on", remote)
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(p) < 2 {
		return nil, nil, fmt.Errorf("%q names no repository", remote)
	}
	web := &url.URL{Scheme: "https", Host: u.Hostname(), Path: "/" + strings.Join(p, "/")}
	if u.Scheme == "http" || u.Scheme == "https" { // the web is where the remote is, port and all
		web.Scheme, web.Host = u.Scheme, u.Host
	}
	return web, p, nil
}
