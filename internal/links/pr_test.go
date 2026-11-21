package links

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lfdominguez/desk/internal/orgs"
)

// TestOpenPR: a pull request on GitHub or a merge request on GitLab, from
// the repository's remote, becomes the card's link.
func TestOpenPR(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		switch r.Method + " " + r.URL.EscapedPath() {
		case "POST /repos/acme/web/pulls":
			fmt.Fprint(w, `{"html_url":"https://github.com/acme/web/pull/7"}`)
		case "POST /api/v4/projects/grp%2Fapp/merge_requests":
			fmt.Fprintf(w, `{"web_url":"http://%s/grp/app/-/merge_requests/4"}`, r.Host)
		case "POST /repos/acme/odd/pulls":
			fmt.Fprint(w, `{"html_url":"not a url"}`)
		default:
			http.Error(w, "unexpected "+r.URL.String(), 422)
		}
	}))
	defer srv.Close()
	githubAPI = srv.URL
	cfg := orgs.Config{GitHub: []orgs.GitHub{{Secret: tokT}}, GitLab: []orgs.GitLab{{URL: srv.URL, Secret: tokT}}}
	ctx := context.Background()

	l, err := OpenPR(ctx, cfg, "git@github.com:acme/web.git", "desk-7", "main", "Fix login", "Why.")
	if err != nil || l.Kind != KindGitHubPR || l.Ref != "PR #7" || got["head"] != "desk-7" || got["base"] != "main" || got["body"] != "Why." {
		t.Fatalf("github %+v %v %v", l, err, got)
	}
	l, err = OpenPR(ctx, cfg, srv.URL+"/grp/app.git", "desk-7", "main", "Fix login", "Why.")
	if err != nil || l.Kind != KindGitLabMR || got["source_branch"] != "desk-7" || got["description"] != "Why." {
		t.Fatalf("gitlab %+v %v %v", l, err, got)
	}
	if _, err := OpenPR(ctx, cfg, "https://github.com/acme/odd", "a", "b", "t", ""); err == nil || !strings.Contains(err.Error(), "isn't one desk reads") {
		t.Fatalf("odd url %v", err)
	}
	if _, err := OpenPR(ctx, cfg, "https://github.com/acme/none", "a", "b", "t", ""); err == nil {
		t.Fatal("a refused request")
	}
	if _, err := OpenPR(ctx, cfg, srv.URL+"/grp/none.git", "a", "b", "t", ""); err == nil {
		t.Fatal("a refused merge request")
	}
	if _, err := OpenPR(ctx, orgs.Config{}, "https://github.com/acme/web", "a", "b", "t", ""); err == nil {
		t.Fatal("no account")
	}
	if _, err := OpenPR(ctx, orgs.Config{}, "https://gitlab.com/grp/app", "a", "b", "t", ""); err == nil {
		t.Fatal("no GitLab account")
	}
	for _, bad := range []string{"", "/local/path", "https://github.com/solo"} {
		if _, err := OpenPR(ctx, cfg, bad, "a", "b", "t", ""); err == nil {
			t.Fatalf("%q opened", bad)
		}
	}
}

// TestRemoteRepo: every remote form names the same web repository.
func TestRemoteRepo(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:acme/web.git":         "https://github.com/acme/web",
		"github.com:acme/web":                 "https://github.com/acme/web",
		"ssh://git@gitlab.com:22/grp/sub/app": "https://gitlab.com/grp/sub/app",
		"https://github.com/acme/web.git":     "https://github.com/acme/web",
		"http://127.0.0.1:8080/grp/app.git":   "http://127.0.0.1:8080/grp/app",
	} {
		if u, _, err := remoteRepo(in); err != nil || u.String() != want {
			t.Errorf("%s: %v %v", in, u, err)
		}
	}
}
