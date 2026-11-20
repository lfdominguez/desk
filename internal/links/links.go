// Package links turns what a card points at (a pull or merge request, a
// ticket, any URL) into a kanban.Link, and asks its provider for its state
// through the card's organization's accounts.
package links

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
)

const (
	KindGitHubPR     = "github-pr"
	KindGitHubIssue  = "github-issue"
	KindJira         = "jira"
	KindLinear       = "linear"
	KindBitbucketPR  = "bitbucket-pr"
	KindGitLabMR     = "gitlab-mr"
	KindGitHubCommit = "github-commit"
	KindGitLabCommit = "gitlab-commit"
	KindURL          = "url"
)

// Tones say how a link's state colors.
const (
	ToneGood = "good" // approved, ready to merge
	ToneWait = "wait" // open, in review, draft
	ToneBad  = "bad"  // changes requested, declined, failed
	ToneDone = "done" // merged, closed as done
)

var jiraKey = regexp.MustCompile(`^[A-Z][A-Z0-9]+-[0-9]+$`)

// Parse reads s, a URL or a bare Jira or Linear key, as a link; ok is false
// for anything else. A bare key is the Linear account's that lists its
// team, else the Jira account's (one listing its project, or the only
// one), else the only Linear account's.
func Parse(s string, cfg orgs.Config) (kanban.Link, bool) {
	s = strings.TrimSpace(s)
	if jiraKey.MatchString(s) {
		team, _, _ := strings.Cut(s, "-")
		if a, ok := linearTeam(cfg, team); ok {
			return linearLink(a, s), true
		}
		if j, ok := jiraFor(cfg, "", s); ok {
			return kanban.Link{URL: strings.TrimRight(j.URL, "/") + "/browse/" + s, Kind: KindJira, Ref: s}, true
		}
		if len(cfg.Linear) == 1 && cfg.Linear[0].Workspace != "" {
			return linearLink(cfg.Linear[0], s), true
		}
		return kanban.Link{}, false
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return kanban.Link{}, false
	}
	l := kanban.Link{URL: s, Kind: KindURL, Ref: strings.TrimPrefix(u.Host, "www.") + strings.TrimSuffix(u.Path, "/")}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	host := u.Hostname()
	_, lkey := linearWorkspace(host, p)
	switch {
	case isGitHub(cfg, host) && len(p) >= 4 && p[2] == "pull":
		l.Kind, l.Ref = KindGitHubPR, "PR #"+p[3]
	case isGitHub(cfg, host) && len(p) >= 4 && p[2] == "issues":
		l.Kind, l.Ref = KindGitHubIssue, "#"+p[3]
	case isGitHub(cfg, host) && len(p) >= 4 && p[2] == "commit":
		l.Kind, l.Ref = KindGitHubCommit, "commit "+short(p[3])
	case slices.Index(p, "-") > 0 && slices.Index(p, "commit") == slices.Index(p, "-")+1 && len(p) > slices.Index(p, "commit")+1:
		l.Kind, l.Ref = KindGitLabCommit, "commit "+short(p[slices.Index(p, "commit")+1])
	case lkey != "":
		l.Kind, l.Ref = KindLinear, lkey
	case host == "bitbucket.org" && len(p) >= 4 && p[2] == "pull-requests":
		l.Kind, l.Ref = KindBitbucketPR, "PR #"+p[3]
	case slices.Contains(p, "merge_requests") && slices.Index(p, "-") >= 0:
		if i := slices.Index(p, "merge_requests"); i+1 < len(p) {
			l.Kind, l.Ref = KindGitLabMR, "MR !"+p[i+1]
		}
	default:
		if k := jiraIssue(u, p); k != "" && (strings.HasSuffix(host, ".atlassian.net") || hasJira(cfg, host)) {
			l.Kind, l.Ref = KindJira, k
		}
	}
	if len(l.Ref) > 48 {
		l.Ref = l.Ref[:47] + "…"
	}
	return l, true
}

func short(sha string) string { return sha[:min(len(sha), 7)] }

// jiraIssue finds the key in /browse/KEY or ?selectedIssue=KEY.
func jiraIssue(u *url.URL, p []string) string {
	if k := u.Query().Get("selectedIssue"); jiraKey.MatchString(k) {
		return k
	}
	if i := slices.Index(p, "browse"); i >= 0 && i+1 < len(p) && jiraKey.MatchString(p[i+1]) {
		return p[i+1]
	}
	return ""
}

func isGitHub(cfg orgs.Config, host string) bool {
	return host == "github.com" || slices.ContainsFunc(cfg.GitHub, func(g orgs.GitHub) bool { return g.Host == host })
}

func hasJira(cfg orgs.Config, host string) bool {
	_, ok := jiraFor(cfg, host, "")
	return ok
}

// jiraFor picks the Jira account for host, or for key's project; with one
// account and no better match, that one.
func jiraFor(cfg orgs.Config, host, key string) (orgs.Jira, bool) {
	proj, _, _ := strings.Cut(key, "-")
	for _, j := range cfg.Jira {
		if u, err := url.Parse(j.URL); err == nil && host != "" && u.Hostname() == host {
			return j, true
		}
		if proj != "" && slices.Contains(j.Projects, proj) {
			return j, true
		}
	}
	if len(cfg.Jira) == 1 && (host == "" || strings.HasSuffix(host, ".atlassian.net")) {
		return cfg.Jira[0], true
	}
	return orgs.Jira{}, false
}

// Check asks l's provider for its state now.
// A pull or merge request still open says its CI too; a commit, only that.
func Check(ctx context.Context, l kanban.Link, cfg orgs.Config) (state, tone string, err error) {
	if l.Kind == KindGitHubCommit || l.Kind == KindGitLabCommit {
		runs, err := CI(ctx, l, cfg)
		if err != nil {
			return "", "", err
		}
		if state, tone = Summary(runs); state == "" {
			return "no CI", ToneWait, nil
		}
		return state, tone, nil
	}
	state, tone, err = check(ctx, l, cfg)
	if err != nil || !HasCI(l) || tone == ToneDone || state == "closed" {
		return state, tone, err
	}
	runs, cerr := CI(ctx, l, cfg)
	ci, ciTone := Summary(runs)
	if cerr != nil || ci == "" { // ponytail: a token without the actions scope just shows no CI
		return state, tone, nil
	}
	if ciTone == ToneBad || (ciTone == ToneWait && tone == ToneGood) {
		tone = ciTone
	}
	return state + " · " + ci, tone, nil
}

func check(ctx context.Context, l kanban.Link, cfg orgs.Config) (state, tone string, err error) {
	u, err := url.Parse(l.URL)
	if err != nil {
		return "", "", err
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch l.Kind {
	case KindGitHubPR, KindGitHubIssue:
		return checkGitHub(ctx, l, u, p, cfg)
	case KindJira:
		j, ok := jiraFor(cfg, u.Hostname(), l.Ref)
		if !ok {
			return "", "", fmt.Errorf("no [[jira]] for %s in desk.toml", u.Host)
		}
		return checkJira(ctx, u, l.Ref, j, cfg)
	case KindLinear:
		a, ok := linearAcct(cfg, l)
		if !ok {
			return "", "", &NoAccountError{"Linear", l.Ref}
		}
		return checkLinear(ctx, l, a, cfg)
	case KindBitbucketPR:
		return checkBitbucket(ctx, p, cfg)
	case KindGitLabMR:
		return checkGitLab(ctx, u, p, cfg)
	}
	return "", "", nil
}
