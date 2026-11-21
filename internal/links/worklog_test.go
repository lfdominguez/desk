package links

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
)

// TestLogWorkJira: an account with worklog logs whole minutes, one at
// least, with the note; one without logs nothing.
func TestLogWorkJira(t *testing.T) {
	var got map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		json.NewDecoder(r.Body).Decode(&got)
		if strings.Contains(path, "ABC-2") {
			http.Error(w, `{"errorMessages":["no"]}`, 403)
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	cfg := orgs.Config{Jira: []orgs.Jira{{URL: srv.URL + "/", Projects: []string{"ABC"}, Worklog: true, Secret: orgs.Secret{Token: "t"}}}}
	ctx := context.Background()
	l := kanban.Link{URL: srv.URL + "/browse/ABC-1", Kind: KindJira, Ref: "ABC-1"}
	ok, err := LogWork(ctx, l, 90*time.Minute+20*time.Second, "DESK-7: Fix login", cfg)
	if !ok || err != nil || path != "/rest/api/3/issue/ABC-1/worklog" || got["timeSpentSeconds"] != float64(5400) || !strings.Contains(mustJSON(got["comment"]), "DESK-7: Fix login") {
		t.Fatalf("%v %v %s %v", ok, err, path, got)
	}
	if LogWork(ctx, l, 10*time.Second, "", cfg); got["timeSpentSeconds"] != float64(60) {
		t.Fatalf("a minute at least: %v", got["timeSpentSeconds"])
	}
	if ok, err := LogWork(ctx, kanban.Link{URL: srv.URL + "/browse/ABC-2", Kind: KindJira, Ref: "ABC-2"}, time.Hour, "", cfg); !ok || err == nil {
		t.Fatalf("refused %v %v", ok, err)
	}
	cfg.Jira[0].URL = "" // the ticket's own site
	if _, err := LogWork(ctx, l, time.Hour, "", cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Jira[0].Worklog = false
	if ok, _ := LogWork(ctx, l, time.Hour, "", cfg); ok {
		t.Fatal("logged without worklog")
	}
	if ok, _ := LogWork(ctx, kanban.Link{URL: "https://github.com/a/b/pull/1", Kind: KindGitHubPR}, time.Hour, "", cfg); ok {
		t.Fatal("logged on a pull request")
	}
	if _, err := LogWork(ctx, kanban.Link{URL: "%zz", Kind: KindJira}, time.Hour, "", cfg); err == nil {
		t.Fatal("a bad url")
	}
	bad := orgs.Config{Jira: []orgs.Jira{{URL: srv.URL, Projects: []string{"ABC"}, Worklog: true, Secret: orgs.Secret{TokenCmd: "exit 3"}}}}
	if _, err := LogWork(ctx, l, time.Hour, "", bad); err == nil {
		t.Fatal("a token that fails")
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// TestLogWorkLinear: Linear has no worklog: the time goes in a comment.
func TestLogWorkLinear(t *testing.T) {
	f, cfg, l := useLinear(t, "Done", "me")
	ctx := context.Background()
	if ok, _ := LogWork(ctx, l, time.Hour, "", cfg); ok {
		t.Fatal("logged without worklog")
	}
	cfg.Linear[0].Worklog = true
	if ok, err := LogWork(ctx, l, 80*time.Minute, "DESK-7", cfg); !ok || err != nil || f.comment != "Time spent: 1h 20m. DESK-7" {
		t.Fatalf("%v %v %q", ok, err, f.comment)
	}
	f.notMade = true
	if _, err := LogWork(ctx, l, time.Hour, "", cfg); err == nil || !strings.Contains(err.Error(), "wasn't made") {
		t.Fatalf("not made %v", err)
	}
	f.fail = "issue(id"
	if _, err := LogWork(ctx, l, time.Hour, "", cfg); err == nil {
		t.Fatal("an issue that can't be read")
	}
}

// TestSpent: durations as people write them.
func TestSpent(t *testing.T) {
	for d, want := range map[time.Duration]string{45 * time.Minute: "45m", 2 * time.Hour: "2h", 80 * time.Minute: "1h 20m", 20 * time.Second: "0m"} {
		if got := Spent(d); got != want {
			t.Errorf("%v: %q", d, got)
		}
	}
}
