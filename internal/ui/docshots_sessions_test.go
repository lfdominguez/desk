package ui

// The Sessions section's screenshots: a card's panel and full window, its
// questions and permissions, "!" commands, subagents, the canvas, diffs,
// the terminal's way in and the usage figures.

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/io/pointer"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/gitw"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
)

const (
	sessBilling = "Migrate the billing exports to S3"
	sessE2E     = "Fix the flaky checkout e2e test"
	sessRate    = "Add rate limiting to the public API"
)

// sessID is the id of the demo card titled title.
func sessID(s *shooter, title string) string {
	s.t.Helper()
	for _, col := range s.u.b.Columns {
		for _, c := range col.Cards {
			if c.Title == title {
				return c.ID
			}
		}
	}
	s.t.Fatalf("no card %q", title)
	return ""
}

// sessPanelX is where the side panel starts, in px (it is 480 dp wide).
var sessPanelX = scaled(image.Pt(shotSize.X-480, 0)).X

// sessRows is the band of the window from y0 to y1 (px) between x0 and the
// right edge (px).
func sessRows(x0, y0, y1 int) image.Rectangle {
	return image.Rect(x0, y0, scaled(shotSize).X, y1).Intersect(image.Rectangle{Max: scaled(shotSize)})
}

// sessBoardOff is where the board's own coordinates start in the window:
// after the sidebar and under the toolbar.
var sessBoardOff = scaled(image.Pt(233, 45))

// sessTop is where the main area starts under the toolbar, in px.
var sessTop = scaled(image.Pt(0, 45)).Y

// sessExplain is Haiku's take on the commands the scenes ask about, in
// the shape claude.Explain asks it for (steps numbered, code in ticks).
func sessExplain(_ context.Context, _, _, _, cmd string, _ ...string) (string, error) {
	switch cmd {
	case "go test ./internal/auth/...":
		return "1. `go test` builds the packages named and runs their tests.\n2. `./internal/auth/...` is the `internal/auth` package and every package under it.", nil
	case "go test -tags integration ./export/...":
		return "1. `go test` builds the packages named and runs their tests.\n2. `-tags integration` also builds the files behind the `integration` build tag: the tests that talk to a real bucket.\n3. `./export/...` is the `export` package and every package under it.", nil
	}
	return "`" + cmd + "` runs the command.", nil
}

// sessCardRect is card id's rectangle on the board, in window px.
func sessCardRect(s *shooter, id string) image.Rectangle {
	s.t.Helper()
	for _, h := range s.u.lay.cards {
		if h.id == id {
			return h.r.Add(sessBoardOff)
		}
	}
	s.t.Fatalf("card %s is not on the board", id)
	return image.Rectangle{}
}

// sessPt is a point in px.
func sessPt(x, y float32) f32.Point { return f32.Pt(x, y) }

// sessEvents feeds stream-json lines to card id's session as Claude's.
func sessEvents(s *shooter, id string, lines ...string) {
	for _, l := range lines {
		for _, e := range claude.Parse([]byte(l)) {
			s.u.onEvent(id, 0, e)
		}
	}
	s.settle()
}

// sessReview gives the demo's checkout card its own worktree in a real git
// repository (Acme's web folder), with a commit and a change not committed,
// and opens its Review.
func sessReview(s *shooter) {
	s.t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	s.t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "me", "GIT_AUTHOR_EMAIL": "me@globex.com", "GIT_COMMITTER_NAME": "me", "GIT_COMMITTER_EMAIL": "me@globex.com"} {
		s.t.Setenv(k, v)
	}
	id := orgsCard(s, "Fix the flaky checkout e2e test")
	repo := orgs.Dir(s.u.root, "Acme", "web")
	run := func(dir string, args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			s.t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	os.MkdirAll(filepath.Join(repo, "e2e"), 0o755)
	os.WriteFile(filepath.Join(repo, "e2e", "checkout.spec.ts"), []byte("test('checkout', async ({ page }) => {\n  await page.goto('/cart')\n  await page.click('#pay')\n  await page.waitForTimeout(2000)\n  await expect(page.locator('.done')).toBeVisible()\n})\n"), 0o644)
	run(repo, "init", "-q", "-b", "main")
	run(repo, "add", ".")
	run(repo, "commit", "-q", "-m", "Checkout e2e test")
	wt, err := gitw.AddWorktree(repo, "desk-6", "main")
	if err != nil {
		s.t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wt, "e2e", "checkout.spec.ts"), []byte("test('checkout', async ({ page }) => {\n  await page.goto('/cart')\n  await page.click('#pay')\n  await page.waitForResponse('**/api/orders')\n  await expect(page.locator('.done')).toBeVisible()\n})\n"), 0o644)
	run(wt, "commit", "-q", "-am", "Wait for the order instead of a fixed sleep")
	os.WriteFile(filepath.Join(wt, "e2e", "README.md"), []byte("Run with `npm run e2e`.\n"), 0o644)
	c := s.u.b.Card(id)
	c.Worktree, c.Branch, c.Base = wt, "desk-6", "main"
	s.u.openReview(id)
	s.wait("the branch", 10*time.Second, func() bool { return !s.u.rvOf(id).loading })
	orgsAway(s)
	s.shot("sess-review", cut{"", dp(412, 52, 1420, 150), true})
	s.click("rvMerge" + id)
	orgsAway(s)
	s.shot("sess-review-merge", cut{"head", dp(412, 52, 1420, 150), true})
}

func init() {
	scenes = append(scenes,
		scene{"sess-panel", sessPanel},
		scene{"sess-review", sessReview},
		scene{"sess-flow", sessFlow},
		scene{"sess-question", sessQuestion},
		scene{"sess-shell", sessShell},
		scene{"sess-held", sessHeld},
		scene{"sess-canvas", sessCanvas},
		scene{"sess-diffs", sessDiffs},
		scene{"sess-terminal", sessTerminal},
		scene{"sess-full", sessFull},
		scene{"sess-image", sessImage},
	)
}

// sessPanel is the billing card's side panel: its conversation, link, the
// account and the message box.
func sessPanel(s *shooter) {
	id := sessID(s, sessBilling)
	s.u.openPanel(id)
	s.settle()
	c := s.u.b.Card(id)
	link := s.at("pnLink" + id + c.Links[0].URL)
	icons := union(s.at("pnFull"), s.at("pnClose"))
	copyBtn := s.at("msgCopy" + id + "-3")
	explain := s.at("explain" + id + "go test ./export/...")
	send := s.at("pnSend")
	s.move(sessPt(10, 10))
	s.settle()
	s.shot("sess-panel",
		cut{"header", sessRows(sessPanelX, scaled(image.Pt(0, 41)).Y, link.Max.Y+scaled(image.Pt(0, 34)).Y), false},
		cut{"icons", around(icons, 4), true},
		cut{"tools", sessRows(sessPanelX, explain.Min.Y-scaled(image.Pt(0, 40)).Y, explain.Max.Y+scaled(image.Pt(0, 30)).Y), false},
		cut{"reply", sessRows(sessPanelX, copyBtn.Min.Y-scaled(image.Pt(0, 8)).Y, copyBtn.Max.Y+scaled(image.Pt(0, 120)).Y), false},
		cut{"", around(copyBtn, 3), true},
		cut{"composer", sessRows(sessPanelX, send.Min.Y-scaled(image.Pt(0, 92)).Y, send.Max.Y+scaled(image.Pt(0, 12)).Y), false},
	)
	// a drag over the reply selects its words
	from := center(copyBtn).Add(sessPt(-374, 34))
	to := from.Add(sessPt(340, 0))
	s.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: from})
	s.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: from})
	for i := float32(1); i <= 4; i++ {
		s.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: from.Add(to.Sub(from).Mul(i / 4))})
	}
	s.send(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: to})
	s.shot("sess-select",
		cut{"reply", sessRows(sessPanelX, copyBtn.Min.Y-scaled(image.Pt(0, 8)).Y, copyBtn.Max.Y+scaled(image.Pt(0, 120)).Y), false})
}

// sessFlow is a new card's first turn (reads, tests, a subagent, an edit),
// then the push it asks permission for.
func sessFlow(s *shooter) {
	id := sessID(s, sessRate)
	s.u.openPanel(id)
	s.settle()
	start := s.at("pnStart" + id)
	s.shot("sess-start", cut{"empty", sessRows(sessPanelX, start.Min.Y-scaled(image.Pt(0, 24)).Y, start.Max.Y+scaled(image.Pt(0, 70)).Y), false})
	pace := demoPace // slower: the subagent runs long enough to be caught running
	s.t.Cleanup(func() { demoPace = pace })
	demoPace = 6 * time.Millisecond
	explainCmd = sessExplain
	s.click("pnStart" + id)
	s.wait("the subagent", 60*time.Second, func() bool {
		ts := s.u.tasks[id]
		return len(ts) > 0 && ts[0].Status == "running" && len(ts[0].msgs) >= 1
	})
	bar := s.at("taskBar" + id)
	s.move(sessPt(10, 10))
	s.shot("sess-bar", cut{"bar", sessRows(sessPanelX, bar.Min.Y, bar.Max.Y), true})
	s.click("taskBar" + id) // the full window, the rail open
	t := s.u.tasks[id][0]
	card := s.at("taskCard" + t.key())
	s.move(sessPt(10, 10))
	s.shot("sess-rail",
		cut{"", image.Rect(scaled(image.Pt(shotSize.X-int(railW), 0)).X, sessTop, scaled(shotSize).X, scaled(shotSize).Y), true},
		cut{"card", around(card, 6), false})
	s.click("taskCard" + t.key())
	back := s.at("subBack")
	s.move(sessPt(10, 10))
	s.shot("sess-subagent", cut{"back", around(back, 4), true})
	demoPace = pace // the rest at the demo's pace (read by the script's goroutine between steps)
	s.click("subBack")
	s.wait("the fix", 90*time.Second, func() bool {
		c := s.u.b.Card(id)
		return c.Session.Turns == 1 && s.u.asks[id] == nil && c.Status == kanban.StatusWaiting
	})
	s.u.rail = false
	s.settle()
	main := scaled(image.Pt(233, 0)).X
	cmd := "go test ./internal/auth/..."
	ex := s.at("explain" + id + cmd)
	s.move(sessPt(10, 10))
	s.shot("sess-tool", cut{"line", sessRows(main, ex.Min.Y-scaled(image.Pt(0, 8)).Y, ex.Max.Y+scaled(image.Pt(0, 30)).Y), false})
	s.click("explain" + id + cmd)
	s.wait("Haiku", 5*time.Second, func() bool {
		e := s.u.explains[cmd]
		return e != nil && !e.busy
	})
	last := s.at(fmt.Sprintf("msgCopy%s-%d", id, len(s.u.chat(id))-1))
	s.move(sessPt(10, 10))
	s.shot("sess-explain",
		cut{"box", sessRows(main, last.Min.Y-scaled(image.Pt(0, 182)).Y, last.Min.Y-scaled(image.Pt(0, 6)).Y), false})

	s.u.view = ""
	s.u.openPanel(id)
	s.settle()
	s.u.chatIn.SetText("Yes, push it.")
	s.click("pnSend")
	s.wait("the permission", 10*time.Second, func() bool { return s.u.asks[id] != nil })
	allow, deny := s.at("askAllow"+id), s.at("askDeny"+id)
	s.move(sessPt(10, 10))
	box := sessRows(sessPanelX, allow.Min.Y-scaled(image.Pt(0, 110)).Y, allow.Max.Y+scaled(image.Pt(0, 14)).Y)
	s.shot("sess-permission",
		cut{"box", box, false},
		cut{"", around(union(allow, deny, s.at("askMore"+id)), 4), true})
	s.u.openPanel(id)
	s.settle()
	s.click("askMore" + id)
	once, allow2 := s.at("askOnce"+id), s.at("askAllow"+id)
	s.move(sessPt(10, 10))
	s.shot("sess-permission-more",
		cut{"box", sessRows(sessPanelX, once.Min.Y-scaled(image.Pt(0, 90)).Y, allow2.Max.Y+scaled(image.Pt(0, 14)).Y), false})
	s.click("askOnce" + id)
	s.wait("the pull request", 30*time.Second, func() bool {
		c := s.u.b.Card(id)
		return c.Session.Turns == 2 && len(c.Links) == 2
	})
	c := s.u.b.Card(id)
	link := s.at("pnLink" + id + c.Links[1].URL)
	last = s.at(fmt.Sprintf("msgCopy%s-%d", id, len(s.u.chat(id))-1))
	s.move(sessPt(10, 10))
	s.shot("sess-link",
		cut{"link", around(link, 4), true},
		cut{"end", sessRows(sessPanelX, last.Min.Y-scaled(image.Pt(0, 230)).Y, last.Max.Y+scaled(image.Pt(0, 70)).Y), false})
}

// sessQuestion is Claude's interview (AskUserQuestion) on the e2e card.
func sessQuestion(s *shooter) {
	id := sessID(s, sessE2E)
	now := time.Now()
	for _, m := range []kanban.ChatMsg{
		{Who: "you", Text: "CI failed on e2e (checkout): let's look.", At: now.Add(-6 * time.Minute)},
		{Who: "tool", Text: "Read  web/e2e/checkout.spec.ts", At: now.Add(-5 * time.Minute)},
		{Who: "tool", Text: "Bash  npx playwright test checkout --repeat-each 10", Cmd: "npx playwright test checkout --repeat-each 10", Detail: "Run the checkout test ten times to catch the flake", At: now.Add(-4 * time.Minute)},
		{Who: "claude", Text: "It fails 3 times in 10: the test clicks **Pay** before the payment iframe has loaded.", At: now.Add(-2 * time.Minute)},
	} {
		s.u.chats[id] = append(s.u.chat(id), m)
	}
	sessEvents(s, id, `{"type":"control_request","request_id":"q1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[`+
		`{"question":"How should the test wait for the payment form?","header":"Waiting","multiSelect":false,"options":[`+
		`{"label":"Wait for the iframe","description":"Wait until the payment iframe reports it is ready"},`+
		`{"label":"Retry the click","description":"Keep the click, retry it up to three times"}]},`+
		`{"question":"Which browsers should run the checkout test?","header":"Browsers","multiSelect":true,"options":[`+
		`{"label":"Chromium"},{"label":"Firefox"},{"label":"WebKit"}]}]}}}`)
	s.u.openPanel(id)
	s.settle()
	askBox := func() image.Rectangle {
		step, send := s.at(fmt.Sprintf("askStep%s-0", id)), s.at("askSend"+id)
		return sessRows(sessPanelX, step.Min.Y-scaled(image.Pt(0, 24)).Y, send.Max.Y+scaled(image.Pt(0, 14)).Y)
	}
	box := askBox()
	s.move(sessPt(10, 10))
	s.shot("sess-question", cut{"box", box, false})
	s.click(fmt.Sprintf("ask%s-0-0", id))
	s.move(sessPt(10, 10))
	s.shot("sess-question-picked", cut{"box", box, false})
	s.click("askSend" + id)
	s.click(fmt.Sprintf("ask%s-1-0", id))
	s.click(fmt.Sprintf("ask%s-1-1", id))
	box = askBox()
	s.move(sessPt(10, 10))
	s.shot("sess-question-2", cut{"box", box, false})
	s.u.panel = ""
	s.settle()
	s.shot("sess-question-card", cut{"card", around(sessCardRect(s, id), 6), true},
		cut{"side", around(s.at("side"+"sess:"+id), 4), true})
}

// sessShell is a "!" command on the billing card: typed, running, done.
func sessShell(s *shooter) {
	release := make(chan struct{})
	old := runShell
	s.t.Cleanup(func() { runShell = old })
	runShell = func(ctx context.Context, _, _ string, w io.Writer) error {
		for _, l := range []string{"=== RUN   TestStreamToS3", "--- PASS: TestStreamToS3 (0.41s)", "=== RUN   TestMultipartResume", "--- PASS: TestMultipartResume (0.87s)"} {
			fmt.Fprintln(w, l)
			time.Sleep(100 * time.Millisecond)
		}
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		fmt.Fprintln(w, "PASS\nok      globex/billing/export   1.52s")
		return nil
	}
	id := sessID(s, sessBilling)
	s.u.openPanel(id)
	s.settle()
	cmd := "go test -v -run 'S3|Multipart' ./export/..."
	s.u.chatIn.SetText("!" + cmd)
	s.settle()
	send := s.at("pnSend")
	s.move(sessPt(10, 10))
	s.shot("sess-shell-typed", cut{"composer", sessRows(sessPanelX, send.Min.Y-scaled(image.Pt(0, 116)).Y, send.Max.Y+scaled(image.Pt(0, 12)).Y), false})
	s.click("pnSend")
	s.wait("the output", 5*time.Second, func() bool {
		ms := s.u.chat(id)
		return strings.Contains(ms[len(ms)-1].Text, "TestMultipartResume (0.87s)")
	})
	stop := s.at("pnShellStop")
	s.move(sessPt(10, 10))
	s.shot("sess-shell-running", cut{"end", sessRows(sessPanelX, stop.Max.Y-scaled(image.Pt(0, 330)).Y, stop.Max.Y+scaled(image.Pt(0, 12)).Y), false})
	close(release)
	s.wait("Claude's turn", 10*time.Second, func() bool {
		c := s.u.b.Card(id)
		return c.Session.Turns == 7 && c.Status == kanban.StatusWaiting
	})
	send = s.at("pnSend")
	s.move(sessPt(10, 10))
	s.shot("sess-shell-done", cut{"end", sessRows(sessPanelX, send.Min.Y-scaled(image.Pt(0, 430)).Y, send.Min.Y-scaled(image.Pt(0, 80)).Y), false})
}

// sessHeld is the billing card's subagents, three of them held, one shell
// and one finished, in the full window with the rail open.
func sessHeld(s *shooter) {
	id := sessID(s, sessBilling)
	now := time.Now()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	mk := func(t claude.Task, start time.Time) *task {
		n := s.u.newTask(id, t)
		n.start, n.act = start, start
		return n
	}
	done := mk(claude.Task{ID: "h0", ToolUse: "u0", Name: "Find the export's callers", Agent: "Explore", Model: "claude-haiku-4-5", Status: "completed", Tokens: 14200, Tools: 6,
		Prompt: "List every caller of the billing exporter and what each expects of its output.", Summary: "Two callers: the nightly job and the admin's Export now button. Both only read the file name back."}, ago(12*time.Minute))
	done.end = ago(9 * time.Minute)
	limits := mk(claude.Task{ID: "h1", ToolUse: "u1", Name: "Check the S3 multipart limits", Agent: "Explore", Model: "claude-haiku-4-5", Status: "running", Tokens: 8100, Tools: 3}, ago(3*time.Minute))
	limits.act = ago(50 * time.Second)
	limits.pending["p1"] = pend{line: "Bash  aws s3api list-multipart-uploads --bucket billing-exports", since: ago(48 * time.Second)}
	integ := mk(claude.Task{ID: "h2", ToolUse: "u2", Name: "Run the export integration tests", Agent: "general-purpose", Model: "claude-sonnet-4-5", Status: "running", Tokens: 22400, Tools: 9}, ago(4*time.Minute))
	integ.act = ago(40 * time.Second)
	sh := mk(claude.Task{ID: "h3", ToolUse: "b1", Shell: true, Name: "Integration tests", Status: "running"}, ago(70*time.Second))
	sh.cmd, sh.out = "go test -tags integration ./export/...", "=== RUN   TestExportToBucket"
	s.u.toolBy["b1"] = "u2"
	notes := mk(claude.Task{ID: "h4", ToolUse: "u4", Name: "Draft the migration notes", Agent: "general-purpose", Model: "claude-sonnet-4-5", Status: "running", Tokens: 5300, Tools: 2}, ago(6*time.Minute))
	notes.act = ago(3 * time.Minute)
	c := s.u.b.Card(id) // the turn ended, the work didn't: as onEvent leaves such a card
	c.Status, c.Note = kanban.StatusWorking, s.u.backgrounded(id)
	s.u.save()
	explainCmd = sessExplain
	s.u.openSession(id)
	s.u.rail = true
	s.settle()
	r1, r2, r4 := s.at("taskCard"+limits.key()), s.at("taskCard"+integ.key()), s.at("taskCard"+notes.key())
	s.move(sessPt(10, 10))
	s.shot("sess-held",
		cut{"running", around(r1, 4), false},
		cut{"waiting", around(r2, 4), false},
		cut{"idle", around(r4, 4), false})
	s.click("railHistory" + id)
	s.move(sessPt(10, 10))
	s.shot("sess-history")
	s.click("taskCard" + sh.key())
	s.wait("Haiku", 5*time.Second, func() bool { e := s.u.explains[sh.cmd]; return e != nil && !e.busy })
	s.move(sessPt(10, 10))
	s.shot("sess-shell-task")
}

// sessCanvas is the rate-limit card run as an agent: its canvas, with its
// ticket, the workspace Claude, three agents and a shell, a pull request
// whose CI failed.
func sessCanvas(s *shooter) {
	id := sessID(s, sessRate)
	c := s.u.b.Card(id)
	c.Agent, c.Status, c.Session = "planner", kanban.StatusWorking, kanban.Session{ID: "demo-team", Turns: 3, Tokens: 96000, Cost: 1.27, Context: 64000, Window: 200000}
	pr := kanban.Link{URL: "https://github.com/acme-org/api/pull/604", Kind: links.KindGitHubPR, Ref: "PR #604", State: "open · CI failed", Tone: links.ToneBad}
	c.Links, c.WSSession = append(c.Links, pr), "demo-ws"
	runs, _ := ciRuns(context.Background(), pr, orgs.Config{})
	s.u.ci["Acme"] = []ciItem{{id: id, key: c.Key(), title: c.Title, link: pr, runs: runs}}
	s.u.moveTo(id, "In progress") // where a session's first message takes it (c is stale after)
	s.u.save()
	now := time.Now()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	s.u.chats[id] = []kanban.ChatMsg{
		{Who: "you", Text: "Start working on this card.", At: ago(14 * time.Minute)},
		{Who: "claude", Text: "I'll map where the limiter goes, then have the tests written and the middleware reviewed.", At: ago(13 * time.Minute)},
	}
	mk := func(t claude.Task, start time.Time) *task {
		n := s.u.newTask(id, t)
		n.start, n.act = start, now
		return n
	}
	ex := mk(claude.Task{ID: "m1", ToolUse: "v1", Name: "Map the API's entry points", Agent: "Explore", Model: "claude-haiku-4-5", Status: "completed", Tokens: 18400, Tools: 11,
		Prompt: "Find every public API route and the middleware chain each goes through.", Summary: "All 23 public routes go through api/middleware.Chain; the limiter fits right after auth, keyed by the API key."}, ago(12*time.Minute))
	ex.end = ago(8 * time.Minute)
	mk(claude.Task{ID: "m2", ToolUse: "v2", Name: "Write the limiter tests", Agent: "test-writer", Model: "claude-sonnet-4-5", Status: "running", Note: "Writing TestLimitPerKey", Tokens: 21000, Tools: 7,
		Prompt: "Write table tests for a 100 requests a minute per key limiter: under, at and over the limit, and the reset."}, ago(6*time.Minute))
	sh := mk(claude.Task{ID: "m3", ToolUse: "b2", Shell: true, Name: "Limiter tests", Status: "running"}, ago(40*time.Second))
	sh.cmd = "go test ./internal/ratelimit/..."
	s.u.toolBy["b2"] = "v2"
	mk(claude.Task{ID: "m4", ToolUse: "v4", Name: "Review the middleware", Agent: "reviewer", Model: "claude-sonnet-4-5", Status: "running", Note: "Reading api/middleware/limit.go", Tokens: 9800, Tools: 4,
		Prompt: "Review api/middleware/limit.go for races and for keys that never expire."}, ago(3*time.Minute))
	s.u.openSession(id)
	s.settle()
	mode, send := s.at("pnMode1"), s.at("pnSend")
	s.move(sessPt(10, 10))
	s.settle()
	s.shot("sess-canvas",
		cut{"switch", around(union(s.at("pnMode0"), mode), 4), true},
		cut{"agents", around(union(s.node(id, "main"), s.node(id, "agent:Explore"), s.node(id, "agent:reviewer"), s.node(id, "shell:m3")), 30), false},
		cut{"delivery", around(union(s.node(id, "link:"+pr.URL), s.node(id, "ci:"+pr.URL), s.node(id, "done")), 30), false})
	s.click("cvHand")
	s.move(sessPt(10, 10))
	s.shot("sess-canvas-team",
		cut{"handoffs", image.Rect(scaled(image.Pt(shotSize.X-380, 0)).X, mode.Max.Y+scaled(image.Pt(0, 92)).Y, scaled(shotSize).X, send.Min.Y-scaled(image.Pt(0, 112)).Y), false})
	s.clickAt(center(s.node(id, "agent:Explore")), pointer.ButtonPrimary)
	s.move(sessPt(10, 10))
	s.shot("sess-canvas-agent")
}

// node is where card id's canvas drew node n last frame, in the window's
// px: the canvas knows its own px, and where the pointer was in them.
func (s *shooter) node(id, n string) image.Rectangle {
	s.t.Helper()
	mid := center(image.Rectangle{Max: scaled(shotSize)})
	s.move(mid)
	v := s.u.cv[id]
	off := mid.Sub(v.ptr).Round()
	for _, x := range v.nodes {
		if x.id == n {
			z := v.zoom * shotScale
			at := func(p image.Point) image.Point {
				return image.Pt(int(v.pan.X+float32(p.X)*z), int(v.pan.Y+float32(p.Y)*z))
			}
			return image.Rectangle{Min: at(x.r.Min), Max: at(x.r.Max)}.Add(off)
		}
	}
	s.t.Fatalf("no node %s on the canvas", n)
	return image.Rectangle{}
}

// sessDiffs is a turn's file changes on the rate-limit card: an edit of a
// few words, a rewritten block, and a new file.
func sessDiffs(s *shooter) {
	id := sessID(s, sessRate)
	c := s.u.b.Card(id)
	c.Status, c.Session = kanban.StatusWaiting, kanban.Session{ID: "demo-diffs", Turns: 2, Tokens: 71000, Cost: 0.86, Context: 52000, Window: 200000}
	s.u.moveTo(id, "In progress") // where a session's first message takes it (c is stale after)
	s.u.save()
	dir := s.u.home + "/Work/Acme/api/"
	now := time.Now()
	s.u.chats[id] = []kanban.ChatMsg{
		{Who: "you", Text: "Limit the public API to 100 requests a minute per key.", At: now.Add(-9 * time.Minute)},
		{Who: "tool", Text: "Edit  internal/config/config.go", Path: dir + "internal/config/config.go", At: now.Add(-7 * time.Minute), Edits: []kanban.Edit{{
			Old: "type Limits struct {\n\tBurst int `toml:\"burst\"`\n\tPerMinute int `toml:\"per_minute\"` // 0: unlimited\n}\n",
			New: "type Limits struct {\n\tBurst int `toml:\"burst\"`\n\tPerMinute int `toml:\"per_minute\"` // default 100 per key\n}\n"}}},
		{Who: "tool", Text: "Edit  internal/api/router.go", Path: dir + "internal/api/router.go", At: now.Add(-6 * time.Minute), Edits: []kanban.Edit{{
			Old: "func routes(mux *http.ServeMux, h *handlers) {\n\tmux.Handle(\"/v1/\", auth(h.v1))\n\tmux.Handle(\"/health\", h.health)\n}\n",
			New: "func routes(mux *http.ServeMux, h *handlers, l *ratelimit.Limiter) {\n\tv1 := auth(l.Wrap(h.v1))\n\tmux.Handle(\"/v1/\", v1)\n\tmux.Handle(\"/health\", h.health) // never limited\n}\n"}}},
		{Who: "tool", Text: "Write  internal/ratelimit/limiter.go", Path: dir + "internal/ratelimit/limiter.go", Lines: 84, Bytes: 2310, At: now.Add(-5 * time.Minute)},
		{Who: "claude", Text: "The limiter is in `internal/ratelimit`, wired after auth so it counts per API key.", At: now.Add(-4 * time.Minute)},
	}
	s.u.openSession(id)
	s.settle()
	m1, m2 := id+"-1", id+"-2"
	a1, a2 := s.at("diffMode"+m1+"3"), s.at("diffMode"+m2+"3")
	main := scaled(image.Pt(233, 0)).X
	block := func(btn image.Rectangle, h int) image.Rectangle {
		return sessRows(main, btn.Min.Y-scaled(image.Pt(0, 34)).Y, btn.Max.Y+scaled(image.Pt(0, h)).Y)
	}
	s.move(sessPt(10, 10))
	s.shot("sess-diffs",
		cut{"words", block(a1, 90), false},
		cut{"split", block(a2, 120), false},
		cut{"modes", around(union(s.at("diffMode"+m2+"0"), a2), 4), true},
		cut{"write", sessRows(main, a2.Max.Y+scaled(image.Pt(0, 120)).Y, a2.Max.Y+scaled(image.Pt(0, 190)).Y), false})
	s.click("diffMode" + m2 + "1")
	a2 = s.at("diffMode" + m2 + "3")
	s.move(sessPt(10, 10))
	s.shot("sess-diffs-unified", cut{"block", block(a2, 200), false})
	s.click("diffMode" + m2 + "3")
	a2 = s.at("diffMode" + m2 + "3")
	s.move(sessPt(10, 10))
	s.shot("sess-diffs-wordsview", cut{"block", block(a2, 135), false}) // not sess-diffs-words: that's a cut of sess-diffs
}

// sessTerminal is the ways into a card's terminal: its menu and the panel.
func sessTerminal(s *shooter) {
	id := sessID(s, sessBilling)
	s.settle()
	s.clickAt(center(sessCardRect(s, id)), pointer.ButtonSecondary)
	s.at("cardMenuClaude Code terminal") // the menu is open
	s.move(sessPt(10, 10))
	s.shot("sess-menu", cut{"menu", around(s.u.menu.r, 6), false})
	s.u.menu = nil
	s.u.openPanel(id)
	s.settle()
	s.hover("pnTerm")
	r := s.at("pnTerm")
	s.settle()
	s.shot("sess-term-btn", cut{"btn", image.Rect(r.Min.X-scaled(image.Pt(330, 0)).X, r.Min.Y-scaled(image.Pt(0, 8)).Y, r.Max.X+scaled(image.Pt(60, 0)).X, r.Max.Y+scaled(image.Pt(0, 40)).Y), false})
}

// sessFull is the billing card's full window: Agent Sessions, the context
// meter and the plan limits (a model's week among them), and the card's
// figures on the board.
func sessFull(s *shooter) {
	planLimits = func(context.Context, string, string) ([]claude.Limit, error) {
		now := time.Now()
		return []claude.Limit{{Name: "5h", Percent: 34, Resets: now.Add(2*time.Hour + 10*time.Minute)}, {Name: "7d", Percent: 61, Resets: now.Add(3 * 24 * time.Hour)}, {Name: "7d Opus", Percent: 83, Resets: now.Add(3 * 24 * time.Hour)}}, nil
	}
	id := sessID(s, sessBilling)
	s.settle()
	s.shot("sess-board-card", cut{"card", around(sessCardRect(s, id), 6), false})
	s.click("side" + "sess:" + id)
	s.settle()
	var rows []image.Rectangle
	for _, col := range s.u.b.Columns {
		for _, c := range col.Cards {
			if r, ok := s.find("side" + "sess:" + c.ID); ok {
				rows = append(rows, r)
			}
		}
	}
	send := s.at("pnSend")
	s.move(sessPt(10, 10))
	s.settle()
	sr := union(rows...)
	s.shot("sess-full",
		cut{"sessions", image.Rect(0, sr.Min.Y-scaled(image.Pt(0, 44)).Y, scaled(image.Pt(232, 0)).X, sr.Max.Y+scaled(image.Pt(0, 6)).Y), true},
		cut{"usage", image.Rect(scaled(image.Pt(233, 0)).X, send.Min.Y-scaled(image.Pt(0, 110)).Y, scaled(image.Pt(1100, 0)).X, send.Min.Y-scaled(image.Pt(0, 84)).Y), true})
}

// sessImage is the billing card's message box with a pasted screenshot: its
// [Image 1] in the text and its thumbnail above the box.
func sessImage(s *shooter) {
	id := sessID(s, sessBilling)
	s.u.openPanel(id)
	s.settle()
	pic := image.NewRGBA(image.Rect(0, 0, 320, 200)) // a made-up chart: bars on a light page
	draw.Draw(pic, pic.Bounds(), image.NewUniform(color.RGBA{0xf4, 0xf1, 0xea, 0xff}), image.Point{}, draw.Src)
	for i, h := range []int{60, 120, 90, 150, 40} {
		draw.Draw(pic, image.Rect(30+i*55, 180-h, 70+i*55, 180), image.NewUniform(color.RGBA{0x7c, 0x9c, 0xe8, 0xff}), image.Point{}, draw.Src)
	}
	var b bytes.Buffer
	png.Encode(&b, pic)
	path, err := s.u.saveImage(b.Bytes(), "image/png")
	if err != nil {
		s.t.Fatal(err)
	}
	s.u.chatIn.SetText("The export chart has a gap on Thursday: ")
	s.u.chatIn.SetCaret(s.u.chatIn.Len(), s.u.chatIn.Len())
	s.u.attach(path)
	s.settle()
	send := s.at("pnSend")
	x := s.at("pnImgX1")
	s.move(sessPt(10, 10))
	s.settle()
	s.shot("sess-image", cut{"composer", sessRows(sessPanelX, x.Min.Y-scaled(image.Pt(0, 16)).Y, send.Max.Y+scaled(image.Pt(0, 12)).Y), false})
}
