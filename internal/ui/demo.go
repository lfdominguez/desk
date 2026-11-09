package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
)

// demo is `desk demo`: made-up organizations, a seeded board and scripted
// Claude sessions in a throwaway folder, every provider faked. For screen
// recordings: nothing real is read, called or written.
var demo bool

// demoPace is a scripted millisecond: tests make it nothing.
var demoPace = time.Millisecond

// Demo makes the demo's world in a new temp folder and points desk at it
// (the board, settings, organizations and HOME); main then opens the board
// as usual. It returns the folder.
func Demo() (string, error) {
	dir, err := os.MkdirTemp("", "desk-demo-")
	if err != nil {
		return "", err
	}
	absWayland()
	for k, v := range map[string]string{"XDG_DATA_HOME": "home/.local/share", "XDG_CONFIG_HOME": "home/.config", "XDG_STATE_HOME": "home/.local/state", "XDG_RUNTIME_DIR": "run", "DESK_ROOT": "home/Work", "HOME": "home", "USERPROFILE": "home"} { // all in HOME (USERPROFILE: Windows'): shown as ~/Work/…, ~/.config/…; its sockets apart from a real desk's
		os.Setenv(k, filepath.Join(dir, v))
		os.MkdirAll(filepath.Join(dir, v), 0o700)
	}
	os.Setenv("DESK_DEMO_TOKEN", "demo") // the accounts' token: the providers are fakes
	work := filepath.Join(dir, "home", "Work")
	for org, toml := range demoOrgs {
		for _, d := range append(demoDirs[org], orgs.DeskDir) {
			os.MkdirAll(filepath.Join(work, org, d), 0o755)
		}
		if err := os.WriteFile(filepath.Join(work, org, orgs.DeskDir, "desk.toml"), []byte(toml), 0o644); err != nil {
			return "", err
		}
	}
	for name, text := range demoFiles { // what the workspace shows; desk.toml's write above says whether the folder takes files
		p := filepath.Join(work, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(text), 0o644)
	}
	if err := demoDrawings(work); err != nil {
		return "", err
	}
	path := kanban.Path()
	b := kanban.Default()
	for _, s := range demoBoard() {
		c := b.Add(b.Column(s.col), s.card)
		for _, m := range s.chat {
			kanban.AppendChat(kanban.ChatPath(path, c.ID), m)
		}
	}
	for _, s := range demoArchived() {
		c := b.Add(b.Column("Done"), s.card)
		b.ArchiveCard(c.ID)
		at := time.Now().Add(-s.ago)
		b.Archive[0].Finished, b.Archive[0].Archived = at, at.Add(time.Hour)
	}
	if err := b.Save(path); err != nil {
		return "", err
	}
	if err := demoInfra(dir); err != nil {
		return "", err
	}
	demo = true
	demoFakes()
	return dir, nil
}

// absWayland makes WAYLAND_DISPLAY a path: the window finds its socket in
// XDG_RUNTIME_DIR, which the demo moves.
func absWayland() {
	if w := os.Getenv("WAYLAND_DISPLAY"); w != "" && !filepath.IsAbs(w) {
		os.Setenv("WAYLAND_DISPLAY", filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), w))
	}
}

// demoFiles are a few files of Acme's api, under ~/Work: login.go as the
// demo's fix leaves it (demoFix's Edit).
var demoFiles = map[string]string{
	"Acme/api/go.mod":    "module example.com/api\n\ngo 1.26\n",
	"Acme/api/README.md": "# api\n\nAcme's public API: auth, orders and the audit log.\n",
	"Acme/api/internal/auth/login.go": `package auth

import (
	"context"
	"net/http"

	"example.com/api/internal/auth/refresh"
)

// login sends req with the session's token, trying a refused one twice more.
func (c *Client) login(ctx context.Context, req *http.Request, s *Session) (*http.Response, error) {
	var resp *http.Response
	var err error
	for try := 0; try < 3; try++ {
		if s.Expired() {
			if err := refresh.Token(ctx, s); err != nil {
				return nil, err
			}
		}
		resp, err = c.do(req, s.Token)
		if err == nil && resp.StatusCode != http.StatusUnauthorized {
			return resp, nil
		}
	}
	return resp, err
}
`,
	"Acme/api/internal/auth/refresh/refresh.go": "package refresh\n\n// Token refreshes s's access token and stores it.\nfunc Token(ctx context.Context, s Session) error { return s.Refresh(ctx) }\n",
}

var demoDirs = map[string][]string{"Acme": {"api", "web"}, "Globex": {"billing", "infra"}, "Initech": {"portal"}}

var demoOrgs = map[string]string{
	"Acme": `[[github]]
name = "acme"
owners = ["acme-corp"]
token_env = "DESK_DEMO_TOKEN"

[[jira]]
name = "acme"
url = "https://acme.atlassian.net"
email = "me@acme.example.com"
projects = ["ABC"]
token_env = "DESK_DEMO_TOKEN"
`,
	"Globex": `[[github]]
name = "globex"
owners = ["globex-org"]
token_env = "DESK_DEMO_TOKEN"

[[nagios]]
name = "prod"
url = "https://mon.globex.example.com/nagios"
user = "me"
token_env = "DESK_DEMO_TOKEN"
`,
	"Initech": `[[github]]
name = "initech"
owners = ["initech"]
token_env = "DESK_DEMO_TOKEN"
`,
}

type demoCard struct {
	col  string
	card kanban.Card
	chat []kanban.ChatMsg
}

func demoBoard() []demoCard {
	now := time.Now()
	day := func(n int) string { return now.AddDate(0, 0, n).Format(kanban.DateLayout) }
	ago := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	pr := demoPR
	jira := func(key, state, tone string) kanban.Link {
		return kanban.Link{URL: "https://acme.atlassian.net/browse/" + key, Kind: links.KindJira, Ref: key, State: state, Tone: tone, Checked: now}
	}
	return []demoCard{
		{col: "Backlog", card: kanban.Card{Title: "Rotate the staging TLS certificates", Org: "Initech", Dir: "portal", Labels: []string{"infra"}, Priority: 1}},
		{col: "Backlog", card: kanban.Card{Title: "Evaluate the Postgres 17 upgrade", Org: "Globex", Dir: "billing", Labels: []string{"db"}}},
		{col: "To do", card: kanban.Card{Title: "Add rate limiting to the public API", Org: "Acme", Dir: "api", Labels: []string{"api"}, Priority: 2, Due: day(1),
			Links: []kanban.Link{jira("ABC-131", "To Do", links.ToneWait)}}},
		{col: "To do", card: kanban.Card{Title: "Dark mode for the dashboard", Org: "Globex", Labels: []string{"web"}, Due: day(5)}},
		{col: "In progress", card: kanban.Card{Title: "Migrate the billing exports to S3", Org: "Globex", Dir: "billing", Labels: []string{"billing"}, Priority: 2,
			Status: kanban.StatusWaiting, Note: "Which bucket region should the exports use?", Last: "The exporter now streams to S3. Which bucket region should the exports use: eu-west-1 like the rest of billing, or us-east-1?",
			Session: kanban.Session{ID: "demo-billing", Turns: 6, Tokens: 182000, Cost: 1.84, Context: 71000, Window: 200000}, Updated: ago(4),
			Links: []kanban.Link{demoPR("globex-org/billing", 88, "open · CI passing", links.ToneGood)}},
			chat: []kanban.ChatMsg{
				{Who: "you", Text: "Move the nightly billing exports from the local disk to S3.", At: ago(31)},
				{Who: "tool", Text: "Read  billing/export/writer.go", At: ago(30)},
				{Who: "tool", Text: "Bash  go test ./export/...", Cmd: "go test ./export/...", Detail: "Run the exporter's tests before changing it", At: ago(29)},
				{Who: "claude", Text: "The exporter now streams to S3 through a multipart upload, and the tests pass.\n\nWhich bucket region should the exports use: **eu-west-1** like the rest of billing, or **us-east-1**?", At: ago(4)},
			}},
		{col: "In progress", card: kanban.Card{Title: "Fix the flaky checkout e2e test", Org: "Acme", Dir: "web", Labels: []string{"tests"}, Priority: 1,
			Status: kanban.StatusWaiting, Note: "CI failed on e2e (checkout): let's look", Session: kanban.Session{ID: "demo-e2e", Turns: 3, Tokens: 64000, Cost: 0.71, Context: 38000, Window: 200000}, Updated: ago(12),
			Links: []kanban.Link{pr("acme-corp/web", 604, "open · CI failed", links.ToneBad)}}},
		{col: "Blocked", card: kanban.Card{Title: "SSO for the admin panel", Org: "Initech", Dir: "portal", Labels: []string{"auth"}, Priority: 2,
			Status: kanban.StatusBlocked, Note: "Waiting on the IdP metadata from IT", Session: kanban.Session{ID: "demo-sso", Turns: 4, Cost: 0.93}}},
		{col: "Done", card: kanban.Card{Title: "Upgrade Go to 1.26", Org: "Acme", Dir: "api", Labels: []string{"deps"},
			Status: kanban.StatusDone, Session: kanban.Session{ID: "demo-go", Turns: 5, Cost: 1.12},
			Links: []kanban.Link{pr("acme-corp/api", 597, "merged", links.ToneDone)}}},
	}
}

// demoPR is a made-up GitHub pull request link, just checked.
func demoPR(repo string, n int, state, tone string) kanban.Link {
	return kanban.Link{URL: fmt.Sprintf("https://github.com/%s/pull/%d", repo, n), Kind: links.KindGitHubPR, Ref: fmt.Sprintf("PR #%d", n), State: state, Tone: tone, Checked: time.Now()}
}

// demoArchived is the demo's archived cards and how long ago each was
// finished, so the Done archive has sections to show.
func demoArchived() []struct {
	card kanban.Card
	ago  time.Duration
} {
	day := 24 * time.Hour
	return []struct {
		card kanban.Card
		ago  time.Duration
	}{
		{kanban.Card{Title: "Dashboard for the audit log", Org: "Initech", Dir: "portal", Labels: []string{"ui"}, Status: kanban.StatusDone,
			Session: kanban.Session{Turns: 9, Cost: 2.41}, Links: []kanban.Link{demoPR("initech/portal", 88, "merged", links.ToneDone)}}, 41 * day},
		{kanban.Card{Title: "Move the billing cron to the queue", Org: "Globex", Dir: "billing", Labels: []string{"billing", "infra"}, Status: kanban.StatusDone,
			Session: kanban.Session{Turns: 12, Cost: 3.05}, Links: []kanban.Link{demoPR("globex-org/billing", 212, "merged", links.ToneDone)}}, 19 * day},
		{kanban.Card{Title: "Fix the flaky checkout test", Org: "Globex", Dir: "shop", Labels: []string{"tests"}, Status: kanban.StatusDone,
			Session: kanban.Session{Turns: 4, Cost: 0.58}, Links: []kanban.Link{demoPR("globex-org/shop", 431, "merged", links.ToneDone)}}, 3 * day},
		{kanban.Card{Title: "Rotate the API keys", Org: "Acme", Dir: "api", Labels: []string{"security"}, Status: kanban.StatusDone,
			Session: kanban.Session{Turns: 3, Cost: 0.64}}, day + 2*time.Hour},
	}
}

// demoFakes puts the scripted Claude and the made-up providers in place.
func demoFakes() {
	startClaude = func(o claude.Options, on func(claude.Event)) (session, error) {
		if strings.Contains(o.System, "workspace Claude of card") {
			return &demoWorkspace{on: on}, nil
		}
		return &demoSession{on: on, asks: make(chan bool, 1)}, nil
	}
	optimize = demoOptimize
	describe = func(_ context.Context, l kanban.Link, _ orgs.Config) (string, error) {
		return l.Ref + ": Login retry fails after the token expires. Users are logged out instead of retried; seen since the 2.14 release.", nil
	}
	linkCheck = func(_ context.Context, l kanban.Link, _ orgs.Config) (string, string, error) {
		switch {
		case l.State != "":
			return l.State, l.Tone, nil
		case l.Kind == links.KindJira:
			return "In Progress", links.ToneWait, nil
		}
		return "open · CI running", links.ToneWait, nil
	}
	assigned = func(_ context.Context, cfg orgs.Config) ([]links.Ticket, error) {
		if len(cfg.Jira) == 0 {
			return nil, nil
		}
		t := func(key, title, status string) links.Ticket {
			return links.Ticket{Project: "ABC", Key: key, Title: title, Status: status, URL: "https://acme.atlassian.net/browse/" + key}
		}
		return []links.Ticket{t("ABC-123", "Login retry fails after the token expires", "To Do"), t("ABC-131", "Add rate limiting to the public API", "To Do"), t("ABC-140", "Export the audit log as CSV", "In Progress")}, nil
	}
	nagiosAlerts = func(context.Context, orgs.Config) ([]links.Alert, error) {
		now := time.Now()
		return []links.Alert{
			{Monitor: "prod", Host: "web-02", Service: "HTTP", State: "critical", Output: "HTTP CRITICAL: HTTP/1.1 502 Bad Gateway - 0.012 second response time", Since: now.Add(-7 * time.Minute)},
			{Monitor: "prod", Host: "db-01", Service: "Disk /var", State: "warning", Output: "DISK WARNING - free space: /var 9% (4.1 GB)", Since: now.Add(-52 * time.Minute)},
		}, nil
	}
	ciRuns = func(_ context.Context, l kanban.Link, _ orgs.Config) ([]links.Run, error) {
		switch {
		case strings.HasSuffix(l.URL, "/612"): // the PR the demo opens: its CI just started
			return []links.Run{{ID: 3, Name: "CI", Status: links.CIRunning, URL: l.URL + "/checks", Jobs: []links.Job{
				{ID: 31, Name: "lint", Status: links.CISuccess}, {ID: 32, Name: "unit", Status: links.CIRunning}, {ID: 33, Name: "e2e (checkout)", Status: links.CIPending}}}}, nil
		case !strings.HasSuffix(l.URL, "/604"):
			return []links.Run{{ID: 1, Name: "CI", Status: links.CISuccess, URL: l.URL + "/checks"}}, nil
		}
		return []links.Run{{ID: 2, Name: "CI", Status: links.CIFailed, URL: l.URL + "/checks", Jobs: []links.Job{
			{ID: 21, Name: "lint", Status: links.CISuccess}, {ID: 22, Name: "unit", Status: links.CISuccess}, {ID: 23, Name: "e2e (checkout)", Status: links.CIFailed}}}}, nil
	}
	pushTicket = func(context.Context, kanban.Link, string, orgs.Config) (string, error) { return "", nil }
	createTicket = func(_ context.Context, _ orgs.Config, title, _ string) (kanban.Link, error) {
		return kanban.Link{URL: "https://acme.atlassian.net/browse/ABC-152", Kind: links.KindJira, Ref: "ABC-152"}, nil
	}
	planLimits = func(context.Context, string, string) ([]claude.Limit, error) {
		now := time.Now()
		return []claude.Limit{{Name: "5h", Percent: 34, Resets: now.Add(2*time.Hour + 10*time.Minute)}, {Name: "7d", Percent: 61, Resets: now.Add(3 * 24 * time.Hour)}}, nil
	}
	explainCmd = func(_ context.Context, _, _, _, cmd string) (string, error) {
		return "`" + cmd + "` runs the package's tests verbosely, only those whose name matches the pattern.", nil
	}
	desktopNotify = func(string, string) {}
}

// demoOptimize is Improve with Claude, scripted: a few reading steps, then
// the draft written out bit by bit.
func demoOptimize(ctx context.Context, a claude.Ask, on func(claude.Progress)) (claude.Draft, error) {
	d := claude.Draft{Title: "Fix login retry reusing an expired token", Labels: []string{"auth", "bug"}, Priority: "high", Folder: "api", Links: []string{"ABC-123"},
		Prompt: "## Goal\nA login retry after the access token expires signs the user in again instead of logging them out.\n\n" +
			"## Context\n- ABC-123: seen since 2.14; the retry loop in `internal/auth/login.go` resends the expired token.\n- `internal/auth/refresh.go` already knows how to refresh it.\n\n" +
			"## Steps\n1. Reproduce it with a test for the expired-token retry.\n2. Refresh the token before the retry.\n3. Run the auth tests.\n\n" +
			"## Done when\nThe new test passes and a pull request is open, linked to ABC-123."}
	wait := func(ms int) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(ms) * demoPace):
			return nil
		}
	}
	for _, step := range []string{"Thinking", "Reading CLAUDE.md", "Reading internal/auth/login.go", "Reading internal/auth/refresh.go"} {
		on(claude.Progress{Step: step})
		if err := wait(700); err != nil {
			return claude.Draft{}, err
		}
	}
	runes := []rune(d.Prompt)
	for n := 0; n <= len(runes); n += 24 {
		on(claude.Progress{Step: "Writing", Draft: claude.Draft{Title: d.Title, Prompt: string(runes[:n])}})
		if err := wait(60); err != nil {
			return claude.Draft{}, err
		}
	}
	return d, nil
}

// demoSession is a card's Claude, scripted: the first turn of a new card
// fixes the login bug (reads, tests, a subagent, an edit), the next pushes
// it after asking; anything else gets a short answer.
type demoSession struct {
	on   func(claude.Event)
	asks chan bool
	mu   sync.Mutex
	turn int
	fix  bool // its first turn was the fix: the second pushes it
}

func (s *demoSession) Send(prompt string) error {
	s.mu.Lock()
	s.turn++
	turn := s.turn
	s.mu.Unlock()
	script := demoReply
	switch {
	case turn == 1 && strings.Contains(prompt, startWork): // a new card's start
		script, s.fix = demoFix, true
	case turn == 2 && s.fix:
		script = demoPush
	}
	go s.play(script)
	return nil
}

func (s *demoSession) Respond(_ string, allow bool, _ json.RawMessage, _ string) error {
	select {
	case s.asks <- allow:
	default:
	}
	return nil
}

func (s *demoSession) Interrupt() error { return nil }
func (s *demoSession) Close()           {}

// demoWorkspace is a card's workspace Claude, scripted: it reads the file
// and answers (the main Claude's question, or the user's).
type demoWorkspace struct{ on func(claude.Event) }

func (s *demoWorkspace) Send(prompt string) error {
	answer := demoWSAnswer
	if strings.HasPrefix(prompt, "The card's main Claude asks:") {
		answer = demoWSCard
	}
	go (&demoSession{on: s.on}).play([]demoStep{
		{400, demoTool("w1", "Read", map[string]any{"file_path": "internal/auth/login.go"}, ""), false},
		{300, demoDone("w1"), false},
		{900, demoText(answer), false},
		{100, `{"type":"result","subtype":"success","total_cost_usd":0.03}`, false},
	})
	return nil
}

func (s *demoWorkspace) Respond(string, bool, json.RawMessage, string) error { return nil }
func (s *demoWorkspace) Interrupt() error                                    { return nil }
func (s *demoWorkspace) Close()                                              {}

const (
	demoWSAnswer = "Yes: `s.Expired()` is checked on every try, so a token that expires between attempts is refreshed before the next `c.do`. One gap: a token the server refuses before it expires is sent again unchanged. Expire it on a 401:\n\n" +
		"```diff\n--- internal/auth/login.go\n+++ internal/auth/login.go\n@@ login @@\n \t\tif err == nil && resp.StatusCode != http.StatusUnauthorized {\n \t\t\treturn resp, nil\n \t\t}\n+\t\ts.Expire() // refused: the next try refreshes it first\n \t}\n```\n\n" +
		"`refresh.Token` already stores the new token in `s`, so nothing else changes."
	demoWSCard = "The user has `internal/auth/login.go` open at the retry loop. They expect the refresh before the post, and a test for an expired token in `login_test.go`."
)

// demoStep is a stream-json line after a pause (ms); ask waits for the
// user's answer to the permission request it carries.
type demoStep struct {
	ms   int
	line string
	ask  bool
}

func (s *demoSession) play(script []demoStep) {
	for _, st := range script {
		time.Sleep(time.Duration(st.ms) * demoPace)
		for _, e := range claude.Parse([]byte(st.line)) {
			s.on(e)
		}
		if st.ask {
			if !<-s.asks {
				for _, e := range claude.Parse([]byte(demoText("OK, I won't push. The fix is committed locally on `fix/login-retry`."))) {
					s.on(e)
				}
				s.on(claude.Event{Kind: claude.KindResult, Cost: 0.12})
				return
			}
		}
	}
}

func demoText(t string) string {
	b, _ := json.Marshal(t)
	return `{"type":"assistant","message":{"content":[{"type":"text","text":` + string(b) + `}]}}`
}

func demoTool(id, name string, in map[string]any, parent string) string {
	b, _ := json.Marshal(in)
	p := ""
	if parent != "" {
		p = `"parent_tool_use_id":"` + parent + `",`
	}
	return `{"type":"assistant",` + p + `"message":{"content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + string(b) + `}]}}`
}

func demoDone(id string) string {
	return `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"ok"}]}}`
}

func demoUsage(ctx int) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"usage":{"input_tokens":%d,"output_tokens":1},"content":[]}}`, ctx)
}

func demoResult(cost float64) string {
	return fmt.Sprintf(`{"type":"result","total_cost_usd":%.2f,"usage":{"input_tokens":52000,"output_tokens":3100},"modelUsage":{"claude-opus":{"contextWindow":200000}}}`, cost)
}

var demoFix = []demoStep{
	{400, `{"type":"system","subtype":"init","session_id":"demo-login"}`, false},
	{600, demoText("I'll reproduce the failure first, then look at how the token is refreshed."), false},
	{700, demoTool("t1", "Read", map[string]any{"file_path": "internal/auth/login.go"}, ""), false},
	{300, demoDone("t1"), false},
	{900, demoTool("t2", "Bash", map[string]any{"command": "go test ./internal/auth -run TestLoginRetry -v", "description": "Run the login retry test to see the failure"}, ""), false},
	{1500, demoDone("t2"), false},
	{400, demoUsage(41000), false},
	{700, demoText("It fails: the retry resends the expired token. I'll have a subagent check how the other services refresh it."), false},
	{700, demoTool("t3", "Agent", map[string]any{"description": "Find how tokens are refreshed", "subagent_type": "Explore"}, ""), false},
	{200, `{"type":"system","subtype":"task_started","task_id":"a1","tool_use_id":"t3","description":"Find how tokens are refreshed","subagent_type":"Explore","task_type":"local_agent","prompt":"Find every place that refreshes an access token and how."}`, false},
	{700, demoTool("s1", "Grep", map[string]any{"pattern": "RefreshToken"}, "t3"), false},
	{500, `{"type":"system","subtype":"task_progress","task_id":"a1","description":"Reading internal/auth/refresh.go","usage":{"total_tokens":9200,"tool_uses":2}}`, false},
	{700, demoTool("s2", "Read", map[string]any{"file_path": "internal/auth/refresh.go"}, "t3"), false},
	{900, `{"type":"system","subtype":"task_notification","task_id":"a1","status":"completed","summary":"refresh.Token(ctx, s) refreshes and stores it; billing and api already call it before retrying."}`, false},
	{100, demoDone("t3"), false},
	{700, demoTool("t4", "Edit", map[string]any{"file_path": "internal/auth/login.go",
		"old_string": "\tfor try := 0; try < 3; try++ {\n\t\tresp, err = c.do(req, s.Token)\n",
		"new_string": "\tfor try := 0; try < 3; try++ {\n\t\tif s.Expired() {\n\t\t\tif err := refresh.Token(ctx, s); err != nil {\n\t\t\t\treturn nil, err\n\t\t\t}\n\t\t}\n\t\tresp, err = c.do(req, s.Token)\n"}, ""), false},
	{300, demoDone("t4"), false},
	{900, demoTool("t5", "Bash", map[string]any{"command": "go test ./internal/auth/...", "description": "Run all the auth tests to confirm the fix"}, ""), false},
	{1400, demoDone("t5"), false},
	{300, demoUsage(58000), false},
	{600, demoText("Fixed: the retry refreshes an expired token before trying again, and all the auth tests pass.\n\nShall I push the branch and open the pull request?"), false},
	{200, demoResult(0.48), false},
}

var demoPush = []demoStep{
	{500, demoText("Pushing the branch."), false},
	{500, `{"type":"control_request","request_id":"p1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"git push -u origin fix/login-retry","description":"Push the fix branch to open its pull request"}}}`, true},
	{300, demoTool("t6", "Bash", map[string]any{"command": "git push -u origin fix/login-retry", "description": "Push the fix branch to open its pull request"}, ""), false},
	{900, demoDone("t6"), false},
	{700, demoText("Pushed and opened pull request #612, linked to ABC-123. CI is running.\nDESK-LINK: https://github.com/acme-corp/api/pull/612"), false},
	{200, demoResult(0.61), false},
}

var demoReply = []demoStep{
	{500, `{"type":"system","subtype":"init","session_id":"demo-reply"}`, false},
	{1200, demoText("On it: I've noted that and will carry on from there."), false},
	{200, demoResult(0.08), false},
}
