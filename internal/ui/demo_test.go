package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
)

// TestDemo: desk demo makes its world in a temp folder (orgs, board, chats,
// every provider faked) and its scripted session fixes, asks, then pushes.
func TestDemo(t *testing.T) {
	noTMPDIR(t)
	h := newHarness(t)
	for _, k := range []string{"XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "DESK_ROOT", "HOME", "DESK_DEMO_TOKEN"} {
		t.Setenv(k, os.Getenv(k)) // put back after
	}
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/7")
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")
	d0, l0, c0, t0 := describe, linkCheck, ciRuns, createTicket // newHarness doesn't set these
	defer func() {
		describe, linkCheck, ciRuns, createTicket = d0, l0, c0, t0
		demo, demoPace = false, time.Millisecond
		infra.HostCmd, infra.ClaudeCmd = nil, nil
	}()
	dir, err := Demo()
	if err != nil || !strings.HasPrefix(dir, os.Getenv("TMPDIR")) || !demo {
		t.Fatalf("dir %q, %v", dir, err)
	}
	if os.Getenv("WAYLAND_DISPLAY") != "/run/user/7/wayland-1" || os.Getenv("XDG_RUNTIME_DIR") != filepath.Join(dir, "run") {
		t.Fatalf("the window's socket %q, the demo's %q", os.Getenv("WAYLAND_DISPLAY"), os.Getenv("XDG_RUNTIME_DIR"))
	}
	if _, err := os.Stat(filepath.Join(dir, "home", "Work", "Globex", ".desk", "ssh_config")); err != nil || infra.HostCmd == nil {
		t.Fatal("Globex's servers", err)
	}
	demoPace = 0
	path := kanban.Path()
	b, err := kanban.Load(path)
	if err != nil || len(b.Columns) != 6 {
		t.Fatal(err)
	}
	h.u = newUI(b, path)
	if !slices.Equal(h.u.orgList, []string{"Acme", "Globex", "Initech"}) || h.cards() != 8 {
		t.Fatalf("orgs %v, %d cards", h.u.orgList, h.cards())
	}
	billing := b.Columns[b.Column("In progress")].Cards[0]
	if len(kanban.LoadChat(kanban.ChatPath(path, billing.ID))) != 4 {
		t.Fatal("the seeded conversation")
	}
	if tk, _ := assigned(context.Background(), h.u.orgConfig("Acme")); len(tk) != 3 {
		t.Fatalf("tickets %v", tk)
	}
	if tk, _ := assigned(context.Background(), h.u.orgConfig("Globex")); tk != nil {
		t.Fatalf("Globex has no Jira: %v", tk)
	}
	if al, _ := nagiosAlerts(context.Background(), h.u.orgConfig("Globex")); len(al) != 2 || al[0].State != "critical" {
		t.Fatalf("alerts %v", al)
	}
	if r, _ := ciRuns(context.Background(), kanban.Link{URL: "https://github.com/acme-corp/web/pull/604"}, orgs.Config{}); r[0].Status != links.CIFailed || len(r[0].Jobs) != 3 {
		t.Fatalf("runs %v", r)
	}
	if r, _ := ciRuns(context.Background(), kanban.Link{URL: "https://github.com/acme-corp/api/pull/612"}, orgs.Config{}); r[0].Status != links.CIRunning || len(r[0].Jobs) != 3 {
		t.Fatalf("runs %v", r)
	}
	if r, _ := ciRuns(context.Background(), kanban.Link{URL: "https://github.com/globex-org/billing/pull/88"}, orgs.Config{}); r[0].Status != links.CISuccess {
		t.Fatalf("runs %v", r)
	}
	if !strings.HasPrefix(h.u.root, h.u.home+"/") {
		t.Fatalf("the orgs %q not under HOME %q", h.u.root, h.u.home)
	}
	for _, c := range []struct {
		l           kanban.Link
		state, tone string
	}{{kanban.Link{State: "merged", Tone: links.ToneDone}, "merged", links.ToneDone}, {kanban.Link{Kind: links.KindJira}, "In Progress", links.ToneWait}, {kanban.Link{Kind: links.KindGitHubPR}, "open · CI running", links.ToneWait}} {
		if s, tone, _ := linkCheck(context.Background(), c.l, orgs.Config{}); s != c.state || tone != c.tone {
			t.Fatalf("%+v: %q %q", c.l, s, tone)
		}
	}
	if d, _ := describe(context.Background(), kanban.Link{Ref: "ABC-123"}, orgs.Config{}); !strings.HasPrefix(d, "ABC-123: ") {
		t.Fatal(d)
	}
	if l, _ := createTicket(context.Background(), orgs.Config{}, "x", ""); l.Ref != "ABC-152" {
		t.Fatal(l)
	}
	if lim, _ := planLimits(context.Background(), "", ""); len(lim) != 2 {
		t.Fatal(lim)
	}
	if s, _ := explainCmd(context.Background(), "", "", "", "go test"); !strings.Contains(s, "`go test`") {
		t.Fatal(s)
	}
	desktopNotify("x", "y")
	if s, _ := pushTicket(context.Background(), kanban.Link{}, "", orgs.Config{}); s != "" {
		t.Fatal(s)
	}

	// Improve, then the card's session: the fix, the push it asks for, the PR
	h.u.newCard(0)
	s := h.u.edit
	s.notes.SetText("Fix the login retry ABC-123 @Acme")
	h.u.optimizeCard()
	h.pumpUntil("the draft", func() bool { return s.optimized })
	if !strings.HasPrefix(s.title.Text(), "Fix login retry") || !strings.Contains(s.notes.Text(), "## Done when") {
		t.Fatalf("draft %q", s.title.Text())
	}
	h.u.commit()
	id := ""
	for _, c := range h.u.b.Columns[h.u.b.Column("In progress")].Cards {
		if strings.HasPrefix(c.Title, "Fix login retry") {
			id = c.ID
		}
	}
	h.pumpUntil("the fix", func() bool { c := h.u.b.Card(id); return c != nil && strings.Contains(c.Last, "Shall I push") })
	if len(h.u.tasks[id]) != 1 || h.u.b.Card(id).Session.Cost != 0.48 {
		t.Fatalf("subagents %d, cost %v", len(h.u.tasks[id]), h.u.b.Card(id).Session.Cost)
	}
	h.u.send(id, "Yes, push it.")
	h.pumpUntil("the question", func() bool { return h.u.asks[id] != nil })
	h.u.procs[id].Respond("p1", true, nil, "")
	h.pumpUntil("the pull request", func() bool {
		return slices.ContainsFunc(h.u.b.Card(id).Links, func(l kanban.Link) bool { return strings.HasSuffix(l.URL, "/pull/612") })
	})

	h.u.send(id, "Thanks!") // pushed already: a third turn is a short answer
	h.pumpUntil("the third answer", func() bool { return strings.HasPrefix(h.u.b.Card(id).Last, "On it") })

	// another session: a short answer; one that says no to the push doesn't push
	h.u.send(billing.ID, "eu-west-1")
	h.pumpUntil("the answer", func() bool { return strings.HasPrefix(h.u.b.Card(billing.ID).Last, "On it") })
	var said []string
	p := &demoSession{on: func(e claude.Event) { said = append(said, e.Kind+":"+e.Text) }, asks: make(chan bool, 1)}
	p.Respond("p1", false, nil, "")
	p.play(demoPush)
	if !slices.Contains(said, "text:OK, I won't push. The fix is committed locally on `fix/login-retry`.") || said[len(said)-1] != "result:" || slices.ContainsFunc(said, func(s string) bool { return strings.Contains(s, "#612") }) {
		t.Fatalf("denied: %q", said)
	}
	p.Interrupt()
	p.Close()

	// the fixed card's workspace: login.go there, its Claude answers the user and the card's
	h.u.openWS(id)
	login := filepath.Join(h.u.root, "Acme", "api", "internal", "auth", "login.go")
	if tb := h.u.wsOpen(h.u.wsOf(id), login); tb == nil || !strings.Contains(tb.ed.Text(), "refresh.Token(ctx, s)") {
		t.Fatal("the demo's login.go")
	}
	h.u.wsSend(id, "Why does the retry fail?")
	h.pumpUntil("the workspace answer", func() bool {
		m := h.u.chat(wsKey(id))
		return len(m) > 0 && strings.HasPrefix(m[len(m)-1].Text, "Yes: `s.Expired()`")
	})
	if err := h.u.wsAskCard(id, "What is the user looking at?"); err != nil {
		t.Fatal(err)
	}
	h.pumpUntil("the answer to the card", func() bool {
		return slices.ContainsFunc(h.u.chat(id), func(m kanban.ChatMsg) bool { return m.Who == "workspace" && strings.HasPrefix(m.Text, "The user has") })
	})
	ws := h.u.ws[id].proc
	ws.Respond("x", false, nil, "")
	ws.Interrupt()
	ws.Close()

	// Stop while Improve reads or writes: no draft
	for _, after := range []time.Duration{0, 3 * time.Millisecond} {
		demoPace = time.Millisecond / 100
		ctx, cancel := context.WithCancel(context.Background())
		steps := 0
		d, err := demoOptimize(ctx, claude.Ask{}, func(p claude.Progress) {
			if steps++; after == 0 || p.Step == "Writing" {
				time.AfterFunc(after, cancel)
			}
		})
		if err == nil || d.Title != "" {
			t.Fatalf("stopped after %v: %+v %v", after, d, err)
		}
		cancel()
	}
}

// TestDemoFails: a demo whose temp folder can't be made says so.
// TestDemoDaysBack: the demo's archived cards land in the sections their
// days ask for at any hour, 1 a.m. too (26 hours back was two days then).
func TestDemoDaysBack(t *testing.T) {
	for _, h := range []int{1, 12, 23} {
		now := time.Date(2026, 3, 1, h, 30, 0, 0, time.UTC) // the 1st: yesterday is in the month before
		var es []archEntry
		for _, n := range []int{1, 3} {
			es = append(es, archEntry{c: kanban.Card{Title: fmt.Sprint(n), Finished: daysBack(now, n)}})
		}
		var got []string
		for _, g := range archGroups(es, false, now) {
			got = append(got, g.name)
		}
		if want := []string{"Yesterday", "This week"}; !slices.Equal(got, want) {
			t.Errorf("at %d:30: %v, want %v", h, got, want)
		}
	}
}

func TestDemoFails(t *testing.T) {
	noTMPDIR(t)
	newHarness(t)
	for _, k := range []string{"XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "DESK_ROOT", "HOME", "DESK_DEMO_TOKEN"} {
		t.Setenv(k, os.Getenv(k))
	}
	defer func() { demo = false }()
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o644)
	t.Setenv("TMPDIR", file)
	if _, err := Demo(); err == nil || demo {
		t.Fatal("a temp folder under a file")
	}
}

// TestAbsWayland: a socket name gets XDG_RUNTIME_DIR's path; a path, or
// none, stays.
func TestAbsWayland(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Wayland is Linux's")
	}
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/7")
	for in, want := range map[string]string{"wayland-1": "/run/user/7/wayland-1", "/tmp/w": "/tmp/w", "": ""} {
		t.Setenv("WAYLAND_DISPLAY", in)
		if absWayland(); os.Getenv("WAYLAND_DISPLAY") != want {
			t.Errorf("%q: %q", in, os.Getenv("WAYLAND_DISPLAY"))
		}
	}
}

// TestLoginWhy: claude's last line, past the code prompt it is printed on.
func TestLoginWhy(t *testing.T) {
	for in, want := range map[string]string{
		"Opening browser…\nPaste code here if prompted > Login failed: That code wasn't accepted.\n\n": "Login failed: That code wasn't accepted.",
		"Login failed: bad code\n": "Login failed: bad code",
		"  \n":                     "",
	} {
		if got := loginWhy(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}
