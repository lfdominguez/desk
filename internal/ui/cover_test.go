package ui

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lfdominguez/desk/internal/testwait"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/x/richtext"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/term"
	"github.com/lfdominguez/desk/internal/ui/kit"
	"github.com/lfdominguez/desk/internal/vault"
)

// The real defaults, before any harness replaces them.
var (
	realStartClaude = startClaude
	realRunShell    = runShell
)

// pumpUntil runs frames until ok, failing after 5s.
func (h *harness) pumpUntil(what string, ok func() bool) {
	h.t.Helper()
	for end := time.Now().Add(testwait.Long); !ok(); time.Sleep(2 * time.Millisecond) {
		if time.Now().After(end) {
			h.t.Fatalf("timed out waiting for %s", what)
		}
		h.frame()
	}
}

// off runs f off the UI goroutine, running frames (its u.later work) until it returns.
func (h *harness) off(f func()) {
	h.t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	h.pumpUntil("background work", func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
	h.frame()
}

// orgToml writes org's desk.toml.
func (h *harness) orgToml(org, s string) {
	h.t.Helper()
	if err := writeAt(orgs.File(h.u.root, org), []byte(s), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

// lockedToml is a desk.toml whose Jira token is in a vault desk has no password for.
const lockedToml = `[[vault]]
name = "vw"
kind = "bitwarden"
url = "http://127.0.0.1:1"
email = "me@example.com"
[[jira]]
url = "https://globex.atlassian.net"
projects = ["ABC"]
vault = "vw"
item = "jira"
`

// fakeJira answers issue ABC-1 (in progress, with a description), and
// fails every other issue.
func fakeJira(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/3/issue/ABC-1"):
			fmt.Fprint(w, `{"fields":{"status":{"name":"In Progress","statusCategory":{"key":"indeterminate"}}}}`)
		case strings.HasSuffix(r.URL.Path, "/api/2/issue/ABC-1"):
			fmt.Fprint(w, `{"fields":{"summary":"Fix login","description":"It breaks.","status":{"name":"In Progress"},"issuetype":{"name":"Bug"}}}`)
		default:
			http.Error(w, "boom", 500)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func jiraLink(srv *httptest.Server, key string) kanban.Link {
	return kanban.Link{URL: srv.URL + "/browse/" + key, Kind: links.KindJira, Ref: key}
}

// fakeBin puts an executable script name on a PATH of its own. Windows has
// no sh: there name.exe is a copy of this test binary, made once per run in
// fakeDir (Windows can't remove a running program's file, so not in the
// test's own folder), which TestMain plays the script with (fakeScript).
func fakeBin(t *testing.T, name, script string) {
	dir := t.TempDir()
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	} else {
		dir = filepath.Join(fakeDir, name)
		if _, err := os.Stat(filepath.Join(dir, name+".exe")); err != nil {
			b, err := os.ReadFile(os.Args[0])
			if err == nil {
				os.MkdirAll(dir, 0o755)
				err = os.WriteFile(filepath.Join(dir, name+".exe"), b, 0o755)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("DESK_UI_FAKE_"+name, script)
	}
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", dir+sep+"/usr/bin"+sep+"/bin"+sep+os.Getenv("PATH"))
}

// fakeDir holds fakeBin's programs on Windows for the run (TestMain).
var fakeDir string

// fakeScript is fakeBin's script played by the test binary on Windows,
// for the few the tests use: exit N, cat >/dev/null, exec cat, and
// echo "$1" > or >> a file.
func fakeScript(script string, args []string) int {
	switch {
	case strings.HasPrefix(script, "exit "):
		n, _ := strconv.Atoi(strings.TrimPrefix(script, "exit "))
		return n
	case script == "cat >/dev/null":
		io.Copy(io.Discard, os.Stdin)
		return 0
	case script == "exec cat":
		io.Copy(os.Stdout, os.Stdin)
		return 0
	case strings.HasPrefix(script, `echo "$1" >`):
		rest := strings.TrimPrefix(script, `echo "$1" >`)
		flag := os.O_TRUNC
		if strings.HasPrefix(rest, ">") {
			rest, flag = rest[1:], os.O_APPEND
		}
		f, err := os.OpenFile(strings.TrimSpace(rest), os.O_CREATE|os.O_WRONLY|flag, 0o644)
		if err != nil || len(args) == 0 {
			return 1
		}
		fmt.Fprintln(f, args[0])
		f.Close()
		return 0
	}
	fmt.Fprintln(os.Stderr, "fakeScript: unknown script", script)
	return 2
}

// ---- sessions ----

// TestCoverStartClaude: the real start runs the claude on PATH (a fake
// here) and reports its exit; with none there it fails.
func TestCoverStartClaude(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := realStartClaude(claude.Options{Home: t.TempDir(), Dir: t.TempDir()}, func(claude.Event) {}); err == nil {
		t.Fatal("started with no claude on PATH")
	}
	fakeBin(t, "claude", "cat >/dev/null")
	exits := make(chan claude.Event, 1)
	p, err := realStartClaude(claude.Options{Home: t.TempDir(), Dir: t.TempDir()}, func(e claude.Event) {
		if e.Kind == claude.KindExit {
			exits <- e
		}
	})
	if err != nil || p == nil {
		t.Fatalf("start: %v", err)
	}
	p.Close()
	select {
	case <-exits:
	case <-time.After(testwait.Long):
		t.Fatal("no exit event after Close")
	}
}

// TestCoverOpenURL: a link goes to xdg-open.
func TestCoverOpenURL(t *testing.T) {
	out := filepath.Join(t.TempDir(), "opened")
	fakeBin(t, infra.Opener, `echo "$1" > `+out)
	openURL("https://example.com/x")
	for end := time.Now().Add(testwait.Long); ; time.Sleep(5 * time.Millisecond) {
		if b, _ := os.ReadFile(out); string(b) == "https://example.com/x\n" {
			return
		} else if time.Now().After(end) {
			t.Fatalf("xdg-open got %q", b)
		}
	}
}

// TestCoverSendOptions: a session's options: an agent that isn't there
// stops it with why; an org with servers gets server work; a session that
// exists is resumed; Stop interrupts it.
func TestCoverSendOptions(t *testing.T) {
	h := newHarness(t)
	h.u.home = t.TempDir()
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme", Agent: "nobody"})
	h.u.send(c.ID, "hi")
	if len(h.claudes) != 0 || !strings.Contains(h.u.toast, c.Key()+": ") {
		t.Fatalf("a missing agent started a session; toast %q", h.u.toast)
	}
	card := h.u.b.Card(c.ID)
	card.Agent = ""
	h.u.tree = append(h.u.tree, infra.Org{Name: "Acme"})
	os.MkdirAll(filepath.Join(h.u.home, ".claude", "projects", "p"), 0o755)
	os.WriteFile(filepath.Join(h.u.home, ".claude", "projects", "p", card.Session.ID+".jsonl"), nil, 0o644)
	h.u.send(c.ID, "hi")
	if len(h.claudes) != 1 {
		t.Fatal("no session")
	}
	o, card := h.claudes[0].opts, h.u.b.Card(c.ID) // the card moved to In progress
	if o.Resume != card.Session.ID || o.New != "" || !strings.Contains(o.System, "--from "+card.Key()) {
		t.Fatalf("resume %q new %q, system %q", o.Resume, o.New, o.System)
	}
	intr := &countInterrupt{}
	h.u.procs[c.ID] = intr
	h.u.stop(c.ID)
	h.u.stop("gone")
	if intr.n != 1 {
		t.Fatalf("interrupted %d times", intr.n)
	}
}

type countInterrupt struct {
	fakeClaude
	n int
}

func (c *countInterrupt) Interrupt() error { c.n++; return nil }

// TestCoverSaveFailures: a board, a chat line or a desk.toml that can't be
// read or written says so.
func TestCoverSaveFailures(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	os.WriteFile(filepath.Join(filepath.Dir(h.u.path), "chats"), nil, 0o644) // a file where the folder goes
	h.u.say(c.ID, "you", "hi")
	if !strings.HasPrefix(h.u.toast, "Chat not saved: ") || len(h.u.chat(c.ID)) != 1 {
		t.Fatalf("toast %q, chat %+v", h.u.toast, h.u.chat(c.ID))
	}
	h.u.path = filepath.Join(filepath.Dir(h.u.path), "chats", "board.json") // under a file: not writable
	h.u.save()
	if !strings.HasPrefix(h.u.toast, "Not saved: ") {
		t.Fatalf("toast %q", h.u.toast)
	}
	h.orgToml("Acme", "[[jira]\n")
	h.u.orgConfig("Acme")
	if !strings.Contains(h.u.toast, "desk.toml") {
		t.Fatalf("toast %q", h.u.toast)
	}
}

// TestCoverMakeTicketGone: a card deleted while its ticket is made is left alone.
func TestCoverMakeTicketGone(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	createTicket = func(context.Context, orgs.Config, string, string) (kanban.Link, error) {
		<-release
		return kanban.Link{Ref: "ABC-1"}, nil
	}
	defer func() { createTicket = links.Create }()
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	h.u.makeTicket(c.ID, "t", "b")
	h.u.b.Delete(c.ID)
	close(release)
	h.u.makeTicket("gone", "t", "b")
	for end := time.Now().Add(time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		h.frame()
	}
	if h.u.toast != "" || h.cards() != 0 {
		t.Fatalf("toast %q, %d cards", h.u.toast, h.cards())
	}
}

// TestCoverNewCardsNoTodo: a board without To do splits cards into its first column.
func TestCoverNewCardsNoTodo(t *testing.T) {
	h := newHarness(t)
	src := h.u.b.Add(1, kanban.Card{Title: "x", Org: "Acme"})
	for i := range h.u.b.Columns {
		if h.u.b.Columns[i].Title == "To do" {
			h.u.b.Columns[i].Title = "Later"
		}
	}
	h.u.newCards(src.ID, []string{"split"})
	if cs := h.u.b.Columns[0].Cards; len(cs) == 0 || cs[len(cs)-1].Title != "split" {
		t.Fatalf("first column %+v", cs)
	}
}

// TestCoverPollLinks: a round checks the board's links (here a Jira
// issue) and keeps their state.
func TestCoverPollLinks(t *testing.T) {
	h := newHarness(t)
	srv := fakeJira(t)
	h.orgToml("Acme", fmt.Sprintf("[[jira]]\nurl = %q\ntoken = \"t\"\n", srv.URL))
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme", Links: []kanban.Link{jiraLink(srv, "ABC-1"), {URL: "https://example.com", Kind: links.KindURL}}})
	go h.u.pollLinks()
	h.pumpUntil("the link checked", func() bool { return h.u.b.Card(c.ID).Links[0].State != "" })
	if l := h.u.b.Card(c.ID).Links; l[0].State != "In Progress" || l[0].Tone != links.ToneWait || l[1].State != "" {
		t.Fatalf("links %+v", l)
	}
}

// TestCoverCheckLinks: a card that's gone is skipped, one deleted while
// its link is checked too; a locked vault says so on the link and asks
// for its password.
func TestCoverCheckLinks(t *testing.T) {
	h := newHarness(t)
	h.orgToml("Globex", lockedToml)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Globex", Links: []kanban.Link{{URL: "https://globex.atlassian.net/browse/ABC-1", Kind: links.KindJira, Ref: "ABC-1"}}})
	h.off(func() { h.u.checkLinks([]string{"gone", c.ID}) })
	h.frame()
	if l := h.u.b.Card(c.ID).Links[0]; l.State != "locked: unlock vw" || l.Tone != links.ToneWait || h.u.unlock == nil {
		t.Fatalf("link %+v, asked %v", l, h.u.unlock != nil)
	}

	asked, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(asked)
		<-release
		http.Error(w, "late", 500)
	}))
	defer srv.Close()
	h.orgToml("Acme", fmt.Sprintf("[[jira]]\nurl = %q\n", srv.URL))
	d := h.u.b.Add(0, kanban.Card{Title: "y", Org: "Acme", Links: []kanban.Link{jiraLink(srv, "ABC-9")}})
	done := make(chan struct{})
	go func() { h.u.checkLinks([]string{d.ID}); close(done) }()
	h.pumpUntil("the check", func() bool {
		select {
		case <-asked:
			return true
		default:
			return false
		}
	})
	h.u.b.Delete(d.ID)
	close(release)
	h.pumpUntil("the check's end", func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
	h.frame()
	if h.u.b.Card(d.ID) != nil {
		t.Fatal("a gone card's link result brought it back")
	}
}

// TestCoverCheckLinksNoAccount: a link whose org has no account for its
// provider isn't asked: its chip says so, waiting, not an error.
func TestCoverCheckLinksNoAccount(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme", Links: []kanban.Link{{URL: "https://github.com/acme/api/pull/1", Kind: links.KindGitHubPR, Ref: "PR #1"}}})
	h.off(func() { h.u.checkLinks([]string{c.ID}) })
	h.frame()
	if l := h.u.b.Card(c.ID).Links[0]; l.State != "no GitHub account" || l.Tone != links.ToneWait {
		t.Fatalf("link %+v", l)
	}
}

// ---- cardtools ----

// TestCoverCardToolLinks: link says each link's state and description (an
// error instead when its provider fails); ci_log is the end of a job's
// log; tickets lists the assigned ones or fails.
func TestCoverCardToolLinks(t *testing.T) {
	h := newHarness(t)
	jira := fakeJira(t)
	var gotPath string
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		fmt.Fprint(w, "line one\nline two\nfailed!")
	}))
	defer gl.Close()
	cfg := orgs.Config{Jira: []orgs.Jira{{URL: jira.URL}}, GitLab: []orgs.GitLab{{URL: gl.URL, Secret: orgs.Secret{Token: "t"}}}}
	c := kanban.Card{Title: "x", Org: "Acme", Links: []kanban.Link{jiraLink(jira, "ABC-1"), jiraLink(jira, "ABC-2")}}
	ctx := context.Background()
	s, err := h.u.cardToolRun(ctx, c, cfg, "link", "", 0, 0)
	if err != nil || !strings.HasPrefix(s, "ABC-1 "+jiraLink(jira, "ABC-1").URL+": In Progress\nABC-1 (Bug, In Progress, labels ): Fix login\nIt breaks.\n\n") ||
		!strings.Contains(s, "ABC-2 "+jiraLink(jira, "ABC-2").URL+": error: ") || strings.Contains(s, "ABC-2 (") {
		t.Fatalf("link: %q %v", s, err)
	}
	if _, err := h.u.cardToolRun(ctx, kanban.Card{Org: "Acme"}, cfg, "link", "", 0, 0); err == nil {
		t.Fatal("link with no links")
	}
	mr := kanban.Card{Org: "Acme", Links: []kanban.Link{{URL: gl.URL + "/acme/api/-/merge_requests/1", Kind: links.KindGitLabMR, Ref: "!1"}}}
	if s, err := h.u.cardToolRun(ctx, mr, cfg, "ci_log", "", 7, 0); err != nil || s != "line one\nline two\nfailed!" || gotPath != "/api/v4/projects/acme%2Fapi/jobs/7/trace" {
		t.Fatalf("ci_log: %q %v at %s", s, err, gotPath)
	}
	if s, err := h.u.cardToolRun(ctx, mr, cfg, "ci_log", "", 7, 7); err != nil || s != "…failed!" {
		t.Fatalf("ci_log, 7 bytes: %q %v", s, err)
	}

	tk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "jql") {
			fmt.Fprint(w, `{"issues":[{"key":"ABC-3","fields":{"summary":"Ship it","status":{"name":"To Do"},"project":{"key":"ABC"}}}]}`)
			return
		}
		http.Error(w, "no", 500)
	}))
	defer tk.Close()
	tcfg := orgs.Config{Jira: []orgs.Jira{{URL: tk.URL, Secret: orgs.Secret{Token: "t"}}}}
	if s, err := h.u.cardToolRun(ctx, c, tcfg, "tickets", "", 0, 0); err != nil || !strings.HasPrefix(s, "ABC ABC-3 [To Do] Ship it ") {
		t.Fatalf("tickets: %q %v", s, err)
	}
	tk.Close()
	if s, err := h.u.cardToolRun(ctx, c, tcfg, "tickets", "", 0, 0); err == nil {
		t.Fatalf("tickets with the tracker down: %q", s)
	}
}

// TestCoverCardToolLocked: a tool whose token is in a locked vault says
// so and asks the user to unlock it; another error is the tool's error.
func TestCoverCardToolLocked(t *testing.T) {
	h := newHarness(t)
	h.orgToml("Globex", lockedToml)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Globex", Links: []kanban.Link{{URL: "https://globex.atlassian.net/browse/ABC-1", Kind: links.KindJira, Ref: "ABC-1"}}})
	var text string
	var ok bool
	h.off(func() { text, ok = h.u.cardTool(c.ID, "tickets", nil) })
	h.frame()
	if ok || !strings.HasSuffix(text, "(desk asks the user to unlock it; try again after)") || h.u.unlock == nil {
		t.Fatalf("%v %q, asked %v", ok, text, h.u.unlock != nil)
	}
	h.off(func() { text, ok = h.u.cardTool(c.ID, "frobnicate", nil) })
	if ok || text != "no tool frobnicate" {
		t.Fatalf("%v %q", ok, text)
	}
}

// ---- unlock ----

// encString is a Bitwarden type 2 EncString of plain under enc/mac.
func encString(enc, mac, plain []byte) string {
	n := aes.BlockSize - len(plain)%aes.BlockSize
	data := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(n)}, n)...)
	iv := make([]byte, aes.BlockSize)
	rand.Read(iv)
	blk, _ := aes.NewCipher(enc)
	cipher.NewCBCEncrypter(blk, iv).CryptBlocks(data, data)
	m := hmac.New(sha256.New, mac)
	m.Write(iv)
	m.Write(data)
	b := base64.StdEncoding.EncodeToString
	return "2." + b(iv) + "|" + b(data) + "|" + b(m.Sum(nil))
}

// fakeVaultwarden is an account email/password that opens, with no items.
func fakeVaultwarden(t *testing.T, email, password string) *httptest.Server {
	master, _ := pbkdf2.Key(sha256.New, password, []byte(email), 1, 32)
	enc, _ := hkdf.Expand(sha256.New, master, "enc", 32)
	mac, _ := hkdf.Expand(sha256.New, master, "mac", 32)
	user := make([]byte, 64)
	rand.Read(user)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/identity/accounts/prelogin":
			json.NewEncoder(w).Encode(map[string]int{"kdf": 0, "kdfIterations": 1})
		case "/identity/connect/token":
			json.NewEncoder(w).Encode(map[string]any{"access_token": "T", "expires_in": 3600})
		case "/api/sync":
			json.NewEncoder(w).Encode(map[string]any{"profile": map[string]any{"key": encString(enc, mac, user)}, "ciphers": []any{}})
		default:
			http.Error(w, "unexpected", 404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestCoverUnlock: a vault already waiting isn't asked twice; the right
// password unlocks it, closes the dialog and checks the links again.
func TestCoverUnlock(t *testing.T) {
	h := newHarness(t)
	srv := fakeVaultwarden(t, "me@example.com", "hunter2")
	v := orgs.Vault{Name: "acme-vw", Kind: "vaultwarden", URL: srv.URL, Email: "me@example.com"}
	h.u.vaultLocked(&vault.LockedError{Vault: v})
	d := h.u.unlock
	h.u.vaultLocked(&vault.LockedError{Vault: v})
	if d == nil || h.u.unlock != d || len(h.u.locked) != 1 {
		t.Fatal("asked twice")
	}
	h.frame()
	h.u.submitUnlock() // no password yet: nothing
	if d.busy {
		t.Fatal("unlocking with no password")
	}
	d.pass.SetText("hunter2")
	h.u.submitUnlock()
	h.u.submitUnlock() // busy: once
	h.pumpUntil("the unlock", func() bool { return !d.busy })
	if h.u.unlock != nil || len(h.u.locked) != 0 || h.u.toast != "Unlocked acme-vw" || h.u.toastBad || d.pass.Text() != "" || !vault.Unlocked(v) {
		t.Fatalf("open %v, locked %d, toast %q, err %q", h.u.unlock != nil, len(h.u.locked), h.u.toast, d.err)
	}
}

// TestCoverUnlockCode: a vault that wants a code asks only for it; enter in
// the password field of one that wants both goes on to the code; the
// dialog's Unlock button submits.
func TestCoverUnlockCode(t *testing.T) {
	h := newHarness(t)
	h.size = image.Pt(1200, 600)
	h.u.askUnlock(&vault.LockedError{Vault: orgs.Vault{Name: "pb", Kind: "nope"}, Code: true})
	d := h.u.unlock
	if d.needPass || !d.needCode || d.focus != &d.code {
		t.Fatalf("pass %v code %v", d.needPass, d.needCode)
	}
	h.frame()
	d.needPass, d.focus = true, &d.pass // both asked: enter in the password, the code empty, goes on to the code
	h.frame()
	h.send(key.EditEvent{Text: "pw"}, press(key.NameReturn, 0))
	if d.busy || h.u.unlock != d || d.pass.Text() != "pw" {
		t.Fatalf("enter in the password with no code submitted (busy %v, pass %q)", d.busy, d.pass.Text())
	}
	h.send(key.EditEvent{Text: "123456"})
	if d.code.Text() != "123456" {
		t.Fatalf("the code field did not take the keyboard: %q", d.code.Text())
	}
	d.needPass = false // the code alone: the configured password
	h.clickButton("ulOK")
	h.pumpUntil("the unlock", func() bool { return !d.busy })
	if d.err != `vault "pb" is locked` || d.code.Text() != "" || h.u.unlock != d { // no password configured: still locked
		t.Fatalf("err %q, code %q", d.err, d.code.Text())
	}
}

// ---- terminal ----

// TestCoverTerminalCells: bold, faint, italic, reverse and background
// cells draw; the cursor dims when the terminal hasn't the keyboard; a
// press takes it; Chat leaves the terminal running; a closed one says so.
func TestCoverTerminalCells(t *testing.T) {
	h := newHarness(t)
	startTerm = func(cmd *exec.Cmd, w, ht int, notify func()) (*term.Term, error) {
		return term.Start(prog("out:\033[1mB\033[0m \033[2mF\033[0m \033[3mI\033[0m \033[7mR\033[0m \033[41mX\033[0m\n", "cat"), w, ht, notify)
	}
	defer func() { startTerm = term.Start }()
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	h.u.terminal(c.ID)
	tt := h.u.terms[c.ID]
	t.Cleanup(func() { h.u.closeTerm(c.ID) })
	h.pumpUntil("the styled line", func() bool {
		runs, _, _, _ := tt.t.Grid(0)
		n := 0
		for _, row := range runs {
			for _, r := range row {
				if r.Style.Attrs != 0 || r.Style.Bg != nil {
					n++
				}
			}
		}
		return n >= 5
	})
	h.pumpUntil("the cursor", func() bool { _, _, _, on := tt.t.Grid(0); return on })
	h.u.blur = true // the keyboard elsewhere: the cursor dims
	h.frame()
	h.frame()
	at := f32.Pt(900, 400)
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: at},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: at})
	h.send(key.EditEvent{Text: "z"})
	h.pumpUntil("typing after a press", func() bool {
		runs, _, _, _ := tt.t.Grid(0)
		for _, row := range runs {
			for _, r := range row {
				if strings.Contains(r.Text, "z") {
					return true
				}
			}
		}
		return false
	})
	h.clickButton("termChat")
	if h.u.view != "sess:"+c.ID || h.u.terms[c.ID] == nil {
		t.Fatalf("Chat: view %q", h.u.view)
	}
	h.u.view = "term:gone"
	h.frame()
}

// TestCoverTerminalPaste: Ctrl+Shift+V with no image reads the clipboard's
// text and types it; plain Ctrl+V with no image goes to the program; an
// image that can't be kept says why.
func TestCoverTerminalPaste(t *testing.T) {
	h := newHarness(t)
	startTerm = func(cmd *exec.Cmd, w, ht int, notify func()) (*term.Term, error) {
		return term.Start(prog("cat"), w, ht, notify)
	}
	defer func() { startTerm = term.Start }()
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	h.u.terminal(c.ID)
	tt := h.u.terms[c.ID]
	t.Cleanup(func() { h.u.closeTerm(c.ID) })
	h.frame()
	h.frame()
	h.send(press("V", key.ModCtrl|key.ModShift))
	h.pumpUntil("the clipboard read", h.r.ClipboardRequested)
	h.send(transfer.DataEvent{Type: "application/text", Open: func() io.ReadCloser { return io.NopCloser(strings.NewReader("pasted")) }})
	h.pumpUntil("the pasted text", func() bool {
		runs, _, _, _ := tt.t.Grid(0)
		for _, row := range runs {
			for _, r := range row {
				if strings.Contains(r.Text, "pasted") {
					return true
				}
			}
		}
		return false
	})
	h.send(press("V", key.ModCtrl)) // no image, no shift: the program gets ^V
	for end := time.Now().Add(300 * time.Millisecond); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		h.frame()
	}
	if tt.pasteText {
		t.Fatal("plain Ctrl+V read the clipboard's text")
	}
	h.send(press("V", key.ModCommand)) // macOS's ⌘V: the text, as Ctrl+Shift+V
	h.pumpUntil("⌘V's clipboard read", func() bool { return tt.pasteText || h.r.ClipboardRequested() })
	clipImage = func() ([]byte, string, error) { return []byte("img"), "image/svg+xml", nil }
	os.WriteFile(h.u.imagesDir(), nil, 0o644) // a file where the folder goes
	h.send(press("V", key.ModCtrl))
	h.pumpUntil("the toast", func() bool { return strings.HasPrefix(h.u.toast, "Couldn't keep the image: ") })
}

// TestCoverSaveImageExt: a type with a suffix (svg+xml) keeps the plain extension.
func TestCoverSaveImageExt(t *testing.T) {
	h := newHarness(t)
	p, err := h.u.saveImage([]byte("<svg/>"), "image/svg+xml")
	if err != nil || filepath.Ext(p) != ".svg" {
		t.Fatalf("%q %v", p, err)
	}
}

// ---- small helpers ----

func TestCoverAgoTokens(t *testing.T) {
	now := time.Date(2026, 5, 10, 15, 0, 0, 0, time.Local)
	for d, want := range map[time.Duration]string{
		5 * time.Minute: "5 min. ago", 3 * time.Hour: "3 hr. ago", 20 * time.Hour: "yesterday", 30 * 24 * time.Hour: "Apr 10",
	} {
		if got := ago(now.Add(-d), now); got != want {
			t.Errorf("%v ago: %q, want %q", d, got, want)
		}
	}
	if got := tokens(6_300_000); got != "6.3M" {
		t.Errorf("tokens %q", got)
	}
	if got := tokens(2_000_000); got != "2M" {
		t.Errorf("tokens %q", got)
	}
}

func TestCoverStatusLineColors(t *testing.T) {
	_, s, c, ok := statusLine(kanban.Card{Status: kanban.StatusFailed, Note: "exit 1"})
	if !ok || s != "Failed: exit 1" || c != kit.CRed {
		t.Errorf("failed: %q %v", s, c)
	}
	if _, s, c, ok := statusLine(kanban.Card{Status: kanban.StatusDone}); !ok || s != "Done" || c != kit.CGreen {
		t.Errorf("done: %q %v", s, c)
	}
}

func TestCoverUsageHelpers(t *testing.T) {
	if resets(time.Time{}) != "—" {
		t.Error("no reset time")
	}
	soon := time.Now().Add(time.Hour)
	if got := resets(soon); got != soon.Local().Format("15:04") {
		t.Errorf("soon %q", got)
	}
	later := time.Now().Add(72 * time.Hour)
	if got := resets(later); got != later.Local().Format("Mon 15:04") {
		t.Errorf("later %q", got)
	}
	if got := artifactName("https://claude.ai/artifact/0123456789abcdef"); got != "Artifact 01234567" {
		t.Errorf("long id %q", got)
	}
	if got := artifactName("https://claude.ai/artifact/abc"); got != "Artifact abc" {
		t.Errorf("short id %q", got)
	}
	gtx := gtxOf(400)
	if d := meter(50)(gtx); d.Size != image.Pt(44, 5) {
		t.Errorf("meter %v", d.Size)
	}
	meter(0)(gtx)
}

// TestCoverUsageStrip: the session view's foot shows the context (of its
// window, or alone), the plan limits, or why they couldn't be read.
func TestCoverUsageStrip(t *testing.T) {
	h := newHarness(t)
	var fail error
	planLimits = func(context.Context, string, string) ([]claude.Limit, error) {
		if fail != nil {
			return nil, fail
		}
		return []claude.Limit{{Name: "5h", Percent: 40, Resets: time.Now().Add(time.Hour)}, {Name: "7d", Percent: 95}}, nil
	}
	c := kanban.Card{Org: "Acme", Session: kanban.Session{Context: 50_000, Window: 200_000}}
	w := func(gtx layout.Context) layout.Dimensions { return h.u.usageStrip(gtx, &c) }
	h.draw(image.Pt(1200, 100), w)
	h.pumpUntil("the limits", func() bool { return len(h.u.limits[orgs.Account(h.u.root, "Acme")].l) == 2 })
	if d := h.draw(image.Pt(1200, 100), w); d.Size.Y == 0 {
		t.Fatal("nothing drawn")
	}
	c.Session.Window = 0
	h.draw(image.Pt(1200, 100), w)
	fail = errors.New("401 Unauthorized\nmore")
	p := h.u.limits[orgs.Account(h.u.root, "Acme")]
	p.at, p.l = time.Time{}, nil
	h.draw(image.Pt(1200, 100), w)
	h.pumpUntil("the error", func() bool { return p.err == "401 Unauthorized" })
	c.Session.Context = 0
	if d := h.draw(image.Pt(1200, 100), w); d.Size.Y == 0 {
		t.Fatal("the error is not shown")
	}
}

// ---- tasks ----

func TestCoverTaskWords(t *testing.T) {
	h := newHarness(t)
	now := time.Now()
	for d, want := range map[time.Duration]string{5*time.Minute + 30*time.Second: "5m 30s", 30 * time.Minute: "30m", 2*time.Hour + 5*time.Minute: "2h 05m"} {
		if got := (&task{start: now.Add(-d)}).took(now); got != want {
			t.Errorf("took %v: %q", d, got)
		}
	}
	if (&task{Task: claude.Task{Status: "failed"}}).color() != kit.CRed {
		t.Error("failed color")
	}
	if (&task{Task: claude.Task{Shell: true}}).title() != "Background shell" || (&task{}).title() != "Subagent" {
		t.Error("titles")
	}
	if got := (&task{Task: claude.Task{Status: "killed"}}).doing(); got != "killed" {
		t.Errorf("doing %q", got)
	}
	id := "c1"
	sh := h.u.newTask(id, claude.Task{ID: "b1", ToolUse: "tu1", Shell: true, Status: "running", Name: "Build"})
	h.u.toolBy["tu1"] = "tu-unknown"
	if got := h.u.by(id, sh); got != "by a subagent" {
		t.Errorf("by %q", got)
	}
	ag := &task{Task: claude.Task{Tools: 3, Tokens: 1500}, start: now}
	if got := h.u.facts(id, ag, now); got != "3 tools · 1.5k · 0s" {
		t.Errorf("facts %q", got)
	}
	if got := h.u.backgrounded(id); got != "Waiting on its background shell: Build" {
		t.Errorf("one shell: %q", got)
	}
	h.u.newTask(id, claude.Task{ID: "a1", Status: "running", Name: "Review"})
	if got := h.u.backgrounded(id); got != "Waiting on 2 background tasks: Build, Review" {
		t.Errorf("two: %q", got)
	}
	done := h.u.newTask(id, claude.Task{ID: "a2", Status: "completed"})
	h.u.stopTasks(id)
	if sh.Status != "stopped" || sh.end.IsZero() || done.Status != "completed" {
		t.Errorf("stopped: %q %v, done %q", sh.Status, sh.end, done.Status)
	}
	h.u.toolLines["tu9"] = "Agent   Look around"
	h.u.subSay(id, "tu9", kanban.ChatMsg{Who: "claude", Text: "hi"})
	if n := h.u.findTask(id, "", "tu9"); n == nil || n.Name != "Agent Look around" || len(n.msgs) != 1 {
		t.Errorf("a subagent seen first by its message: %+v", n)
	}
	gtx := gtxOf(400)
	gtx.Now = now
	if d := h.u.badge(wait{text: "No activity", since: now.Add(-time.Minute), c: kit.CRed})(gtx); d.Size.X == 0 {
		t.Error("badge")
	}
}

// TestCoverTaskTail: a long output is its end from a whole line; one that
// can't be read is the last read.
func TestCoverTaskTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "out")
	os.WriteFile(p, []byte(strings.Repeat("x", 40<<10)+"\nlast line\n"), 0o644)
	tk := &task{Task: claude.Task{Output: p}}
	if s := tk.tail(); !strings.HasPrefix(s, "…\nlast line") {
		t.Fatalf("tail %q", s[:20])
	}
	tk = &task{Task: claude.Task{Output: t.TempDir()}, out: "before"}
	if s := tk.tail(); s != "before" {
		t.Fatalf("a directory: %q", s)
	}
}

// TestCoverTaskBar: what runs, in words, and where a click goes: from the
// side panel to the full window (with the rail, or the history when
// nothing runs); in the full window it opens and closes the rail.
func TestCoverTaskBar(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	id := c.ID
	ag := h.u.newTask(id, claude.Task{ID: "a1", ToolUse: "tu1", Status: "running", Name: "Review", Model: "claude-opus-5-5"})
	ag.act = time.Now().Add(-5 * time.Minute)
	sz := image.Pt(900, 60)
	bar := func(full bool, act *func()) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions { return h.u.taskBar(gtx, id, full, act) }
	}
	click := func(full bool) {
		var act func()
		h.clickIn(sz, image.Rectangle{Max: sz}, bar(full, &act), "taskBar"+id)
		if act == nil {
			t.Fatal("no action")
		}
		act()
	}
	click(false) // running, from the panel: the full window with the rail
	if h.u.view != "sess:"+id || !h.u.rail {
		t.Fatalf("view %q rail %v", h.u.view, h.u.rail)
	}
	click(true) // in the full window: the rail closes
	if h.u.rail {
		t.Fatal("rail still open")
	}
	ag.Status, ag.end = "completed", time.Now().Add(-time.Hour)
	h.u.view = ""
	click(false) // nothing runs, from the panel: the history
	if h.u.view != "sess:"+id || !h.u.rail || h.u.sub != historyKey {
		t.Fatalf("view %q rail %v sub %q", h.u.view, h.u.rail, h.u.sub)
	}
}

// TestCoverTaskRail: the rail's cards (selected, failed, a model, held, a
// result), its Close and History, and the history's shells.
func TestCoverTaskRail(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	id := c.ID
	ag := h.u.newTask(id, claude.Task{ID: "a1", ToolUse: "tu1", Status: "running", Name: "Review", Model: "claude-opus-5-5"})
	ag.act = time.Now().Add(-5 * time.Minute)
	h.u.newTask(id, claude.Task{ID: "a2", Status: "failed", Summary: "It broke.\nMore."}).end = time.Now()
	old := h.u.newTask(id, claude.Task{ID: "b1", Shell: true, Status: "completed", Name: "Build"})
	old.end = time.Now().Add(-time.Hour)
	h.u.sub = "a1"
	sz := image.Pt(400, 900)
	var act func()
	rail := func(gtx layout.Context) layout.Dimensions { return h.u.taskRail(gtx, id, &act) }
	h.clickIn(sz, image.Rectangle{Max: sz}, rail, "taskCarda2")
	if act(); h.u.sub != "a2" {
		t.Fatalf("sub %q", h.u.sub)
	}
	act = nil
	h.clickIn(sz, image.Rectangle{Max: sz}, rail, "railClose")
	h.u.rail = true
	if act(); h.u.rail {
		t.Fatal("Close kept the rail")
	}
	act = nil
	h.clickIn(sz, image.Rectangle{Max: sz}, rail, "railHistory"+id)
	if act(); h.u.sub != historyKey {
		t.Fatalf("sub %q", h.u.sub)
	}
	act = nil
	h.draw(image.Pt(900, 600), func(gtx layout.Context) layout.Dimensions { return h.u.historyBody(gtx, id, &act) })
	h.draw(image.Pt(900, 600), func(gtx layout.Context) layout.Dimensions { return h.u.subBody(gtx, id, ag, &act) })
	// the session view with its rail: a click there runs after both are drawn
	h.u.view, h.u.rail, h.u.sub = "sess:"+id, true, ""
	h.frame()
	h.clickButton("railClose")
	if h.u.rail {
		t.Fatal("the session view's rail did not close")
	}
}

// ---- panel ----

// TestCoverPanelHeader: the header's buttons: links and artifacts open in
// the browser, Full window, Edit, Terminal, Archive, Close; an archived
// card's Restore; Stop and Send in the composer.
func TestCoverPanelHeader(t *testing.T) {
	h := newHarness(t)
	opened := filepath.Join(t.TempDir(), "opened")
	fakeBin(t, infra.Opener, `echo "$1" >> `+opened)
	startTerm = func(cmd *exec.Cmd, w, ht int, notify func()) (*term.Term, error) {
		return term.Start(prog("cat"), w, ht, notify)
	}
	defer func() { startTerm = term.Start }()
	art := "https://claude.ai/artifact/0123456789abcdef"
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme", Status: kanban.StatusWorking,
		Links: []kanban.Link{{URL: "https://example.com/doc", Kind: links.KindURL, Ref: "doc"}}, Artifacts: []string{art}})
	id := c.ID
	intr := &countInterrupt{}
	h.u.procs[id] = intr
	h.u.home = t.TempDir() // the account pill: not the user's login
	sz := image.Pt(700, 900)
	pane := func(full bool) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions { return h.u.chatPane(gtx, id, 700, full) }
	}
	top := image.Rect(0, 0, 700, 160)
	h.clickIn(sz, top, pane(false), "pnLink"+id+"https://example.com/doc")
	h.clickIn(sz, top, pane(false), "pnArt"+id+art)
	h.pumpUntil("xdg-open", func() bool {
		b, _ := os.ReadFile(opened)
		return string(b) == "https://example.com/doc\n"+art+"\n"
	})
	h.clickIn(sz, image.Rect(0, 700, 700, 900), pane(false), "pnStop")
	if intr.n != 1 {
		t.Fatal("Stop did not interrupt")
	}
	h.u.chatIn.SetText("go on")
	h.clickIn(sz, image.Rect(0, 700, 700, 900), pane(false), "pnSend")
	if h.u.chatIn.Text() != "" || len(intr.prompts) != 1 || !strings.HasSuffix(intr.prompts[0], "\ngo on") {
		t.Fatalf("Send: box %q, prompts %q", h.u.chatIn.Text(), intr.prompts)
	}
	stopped := false
	h.u.shellStop[id] = func() { stopped = true }
	h.clickIn(sz, image.Rect(0, 700, 700, 900), pane(false), "pnShellStop")
	delete(h.u.shellStop, id)
	if !stopped {
		t.Fatal("Stop command")
	}
	h.clickIn(sz, top, pane(false), "pnFull")
	if h.u.view != "sess:"+id {
		t.Fatalf("Full: view %q", h.u.view)
	}
	h.clickIn(sz, image.Rectangle{Max: sz}, pane(true), "pnClose")
	if h.u.view != "" {
		t.Fatalf("Close (full): view %q", h.u.view)
	}
	h.u.panel = id
	h.clickIn(sz, top, pane(false), "pnClose")
	if h.u.panel != "" {
		t.Fatal("Close (panel)")
	}
	h.clickIn(sz, top, pane(false), "pnEdit")
	if h.u.edit == nil || h.u.edit.id != id {
		t.Fatal("Edit")
	}
	h.u.edit = nil
	h.clickIn(sz, top, pane(false), "pnTerm")
	if h.u.terms[id] == nil {
		t.Fatal("Terminal")
	}
	h.u.closeTerm(id)
	h.u.view = "sess:" + id
	h.clickIn(sz, top, pane(false), "pnArchive")
	if h.u.b.Card(id).Archived.IsZero() || h.u.view != "" || !strings.HasPrefix(h.u.toast, "Archived ") {
		t.Fatalf("Archive: view %q, toast %q", h.u.view, h.u.toast)
	}
	h.draw(sz, pane(false)) // archived, no conversation: says so
	h.clickIn(sz, top, pane(false), "pnRestore")
	if !h.u.b.Card(id).Archived.IsZero() || !strings.HasPrefix(h.u.toast, "Restored ") {
		t.Fatalf("Restore: toast %q", h.u.toast)
	}
	h.u.openSession("gone")
	h.u.view = "sess:gone"
	h.frame()
}

// clickScan clicks button id in the whole window, scanning area top down.
func (h *harness) clickScan(id string, area image.Rectangle) {
	h.t.Helper()
	c := h.u.k.Click(id)
	h.frame()
	if p, ok := h.seek(id, area, h.frame); ok {
		h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p},
			pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
		return
	}
	for y := area.Min.Y + 2; y < area.Max.Y; y += 6 {
		for x := area.Min.X + 2; x < area.Max.X; x += 12 {
			p := f32.Pt(float32(x), float32(y))
			h.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: p})
			h.frame()
			if c.Hovered() {
				h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p},
					pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
				return
			}
		}
	}
	h.t.Fatalf("button %s is not in %v", id, area)
}

// ---- alerts and snoozes ----

// TestCoverAlertsView: the view's error line, sections (snoozed last),
// search, and its buttons: a row opens the alert, Snooze opens the picker
// (again: closes it), a pick snoozes it, Unsnooze shows it again,
// Investigate makes it a card.
func TestCoverAlertsView(t *testing.T) {
	h := newHarness(t)
	opened := filepath.Join(t.TempDir(), "opened")
	fakeBin(t, infra.Opener, `echo "$1" >> `+opened)
	warn := links.Alert{Monitor: "prod", Host: "web1", Service: "Disk /", State: "warning", Output: "12% free", URL: "https://mon.example.com/b"}
	crit := links.Alert{Monitor: "prod", Host: "db1", State: "down", URL: "https://mon.example.com/c"}
	h.u.alerts["Globex"] = []links.Alert{warn}
	if c := h.u.alertColor("Globex"); c != kit.COrange {
		t.Fatalf("a warning alone: %v", c)
	}
	h.u.alerts["Globex"] = []links.Alert{warn, crit}
	h.u.alertErr["Globex"] = "⚠ one monitor is down"
	h.u.tree = append(h.u.tree, infra.Org{Name: "Globex"})
	h.u.view = "al:Globex"
	sz := image.Pt(900, 700)
	view := func(gtx layout.Context) layout.Dimensions { return h.u.alertsView(gtx) }
	all := image.Rectangle{Max: sz}
	h.clickIn(sz, all, view, "al"+warn.URL)
	h.pumpUntil("xdg-open", func() bool { b, _ := os.ReadFile(opened); return string(b) == warn.URL+"\n" })
	h.clickIn(sz, all, view, "alSnz"+warn.URL)
	if h.u.snoozing != alertKey("Globex", warn) {
		t.Fatalf("snoozing %q", h.u.snoozing)
	}
	h.clickIn(sz, all, view, "alSnz"+warn.URL) // again: closed
	if h.u.snoozing != "" {
		t.Fatal("Snooze again kept the picker")
	}
	h.clickIn(sz, all, view, "alSnz"+warn.URL)
	h.clickIn(sz, all, view, "alPick1 hour")
	if _, ok := h.u.snoozedUntil("Globex", warn); !ok || h.u.snoozing != "" {
		t.Fatal("not snoozed")
	}
	h.u.search.SetText("'db1")
	h.u.alList.Position.Count = -1
	h.draw(sz, view)
	if h.u.alList.Position.Count != 3 { // the error, Errors, db1
		t.Fatalf("searching db1: %d rows", h.u.alList.Position.Count)
	}
	h.u.search.SetText("")
	h.clickIn(sz, all, view, "alSnz"+warn.URL) // Unsnooze
	if _, ok := h.u.snoozedUntil("Globex", warn); ok {
		t.Fatal("still snoozed")
	}
	h.clickIn(sz, all, view, "alInv"+crit.URL)
	if c := h.u.b.Card(h.u.sel); c == nil || c.Title != "Investigate DOWN db1" || h.u.view != "sess:"+c.ID {
		t.Fatalf("investigate: view %q", h.u.view)
	}
	h.u.view = "al:Initech"
	h.draw(sz, view) // asking
	h.u.alerts["Initech"] = nil
	h.draw(sz, view) // none
}

// TestCoverInvestigateNoColumn: with no In progress column the card goes first.
func TestCoverInvestigateNoColumn(t *testing.T) {
	h := newHarness(t)
	for i := range h.u.b.Columns {
		if h.u.b.Columns[i].Title == "In progress" {
			h.u.b.Columns[i].Title = "Doing"
		}
	}
	h.u.investigate("Acme", "web1", "", "down", "", "", time.Now())
	if cs := h.u.b.Columns[0].Cards; len(cs) != 1 || cs[0].Title != "Investigate DOWN web1" || alertName(cs[0].Alert) != "web1" {
		t.Fatalf("first column %+v", cs)
	}
}

// TestCoverSnooze: expired snoozes go; one that can't be saved says so.
func TestCoverSnooze(t *testing.T) {
	h := newHarness(t)
	h.u.snoozed["Acme|old|"] = time.Now().Add(-time.Hour)
	h.u.snooze("Acme|web1|", time.Hour)
	if _, ok := h.u.snoozed["Acme|old|"]; ok {
		t.Fatal("an expired snooze stayed")
	}
	os.Remove(h.u.snoozePath())
	os.Mkdir(h.u.snoozePath(), 0o755)
	h.u.snooze("Acme|web1|", 0)
	if !strings.HasPrefix(h.u.toast, "Snooze not saved: ") {
		t.Fatalf("toast %q", h.u.toast)
	}
}

// TestCoverSnoozePicker: a custom time snoozes by enter or its button; a
// bad one says why; Cancel picks 0.
func TestCoverSnoozePicker(t *testing.T) {
	h := newHarness(t)
	got := time.Duration(-1)
	sz := image.Pt(800, 420)
	w := func(gtx layout.Context) layout.Dimensions {
		return h.u.snoozePicker(gtx, "p", func(d time.Duration) { got = d })
	}
	h.u.snoozeEd.SetText("nope")
	h.clickIn(sz, image.Rectangle{Max: sz}, w, "pcustom")
	if got != -1 || !strings.Contains(h.u.toast, "is not a time") {
		t.Fatalf("got %v, toast %q", got, h.u.toast)
	}
	h.u.snoozeEd.SetText("2h")
	h.clickIn(sz, image.Rectangle{Max: sz}, w, "pcustom")
	if got != 2*time.Hour || h.u.snoozeEd.Text() != "" {
		t.Fatalf("custom: %v", got)
	}
	h.clickIn(sz, image.Rectangle{Max: sz}, w, "pcancel")
	if got != 0 {
		t.Fatalf("cancel: %v", got)
	}
	got = -1
	h.u.snoozeEd.SetText("3d")
	h.draw(sz, w)
	h.r.Queue(key.FocusEvent{})
	gtx := gtxOf(800)
	gtx.Source = h.r.Source()
	gtx.Execute(key.FocusCmd{Tag: &h.u.snoozeEd})
	h.draw(sz, w)
	h.r.Queue(press(key.NameReturn, 0))
	h.draw(sz, w)
	h.draw(sz, w)
	if got != 72*time.Hour {
		t.Fatalf("enter: %v", got)
	}
}

// TestCoverSnoozeBar: a card whose Claude suggests snoozing its alert
// offers the picker over the composer; a pick snoozes the alert.
func TestCoverSnoozeBar(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme", Alert: "Acme|web1|HTTP"})
	h.u.snoozeAsk[c.ID] = "Stopped on purpose."
	sz := image.Pt(700, 900)
	pane := func(gtx layout.Context) layout.Dimensions { return h.u.chatPane(gtx, c.ID, 700, false) }
	h.clickIn(sz, image.Rect(0, 500, 700, 900), pane, "snz"+c.ID+"1 day")
	if _, ok := h.u.snoozeAsk[c.ID]; ok || h.u.snoozed["Acme|web1|HTTP"].Before(time.Now().Add(23*time.Hour)) {
		t.Fatalf("asks %v, snoozed %v", h.u.snoozeAsk, h.u.snoozed)
	}
	h.u.snoozeAsk[c.ID] = "Again."
	h.clickIn(sz, image.Rect(0, 500, 700, 900), pane, "snz"+c.ID+"cancel")
	if _, ok := h.u.snoozeAsk[c.ID]; ok {
		t.Fatal("Cancel kept the question")
	}
}

// ---- tickets ----

// TestCoverTicketsView: the view's error line, search, Asking/none, and a
// row's buttons: the ticket's card opens, or the dialog makes one; the
// link opens in the browser.
func TestCoverTicketsView(t *testing.T) {
	h := newHarness(t)
	opened := filepath.Join(t.TempDir(), "opened")
	fakeBin(t, infra.Opener, `echo "$1" >> `+opened)
	tk1 := links.Ticket{Project: "ABC", Key: "ABC-1", Title: "Fix login", Status: "To Do", URL: "https://globex.atlassian.net/browse/ABC-1"}
	tk2 := links.Ticket{Project: "ABC", Key: "ABC-2", Title: "Ship it", Status: "To Do", URL: "https://globex.atlassian.net/browse/ABC-2"}
	h.u.tickets["Globex"] = []links.Ticket{tk1, tk2}
	h.u.ticketErr["Globex"] = "⚠ GitHub said no"
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Globex", Links: []kanban.Link{{URL: "https://globex.atlassian.net/browse/ABC-1?x", Kind: links.KindJira, Ref: "ABC-1"}}})
	h.u.view = "tk:Globex"
	sz := image.Pt(900, 600)
	view := func(gtx layout.Context) layout.Dimensions { return h.u.ticketsView(gtx) }
	all := image.Rectangle{Max: sz}
	h.clickIn(sz, all, view, "tk"+tk1.URL)
	if h.u.panel != c.ID {
		t.Fatalf("its card: panel %q", h.u.panel)
	}
	h.clickIn(sz, all, view, "tk"+tk2.URL)
	if h.u.edit == nil || h.u.edit.title.Text() != "ABC-2 Ship it" || h.u.edit.links.Text() != tk2.URL {
		t.Fatal("no dialog for the new card")
	}
	h.u.edit = nil
	h.clickIn(sz, all, view, "tkOpen"+tk2.URL)
	h.pumpUntil("xdg-open", func() bool { b, _ := os.ReadFile(opened); return string(b) == tk2.URL+"\n" })
	h.u.search.SetText("ship")
	h.u.tkList.Position.Count = -1
	h.draw(sz, view)
	if h.u.tkList.Position.Count != 3 { // the error, ABC, ABC-2
		t.Fatalf("searching: %d rows", h.u.tkList.Position.Count)
	}
	h.u.search.SetText("")
	h.u.view = "tk:Initech"
	h.draw(sz, view)
	h.u.tickets["Initech"] = nil
	h.draw(sz, view)
}

// TestCoverReloadConfig: orgs no longer there lose their tickets and alerts.
func TestCoverReloadConfig(t *testing.T) {
	h := newHarness(t)
	h.u.tickets["Gone"] = nil
	h.u.tickets["Acme"] = nil
	h.u.alerts["Gone"] = nil
	h.u.reloadConfig()
	_, tk := h.u.tickets["Gone"]
	_, al := h.u.alerts["Gone"]
	_, acme := h.u.tickets["Acme"]
	if tk || al || !acme {
		t.Fatalf("tickets gone %v, alerts gone %v, acme %v", tk, al, acme)
	}
	for end := time.Now().Add(300 * time.Millisecond); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		h.frame()
	}
}

// TestCoverFetchErr: a locked vault asks to be unlocked.
func TestCoverFetchErr(t *testing.T) {
	h := newHarness(t)
	msg := h.u.fetchErr(fmt.Errorf("wrapped: %w", &vault.LockedError{Vault: orgs.Vault{Name: "vw"}}))
	h.frame()
	if msg != "Locked: unlock vw for the rest" || len(h.u.locked) != 1 {
		t.Fatalf("%q, locked %d", msg, len(h.u.locked))
	}
}

// TestCoverPushLocked: a ticket whose vault is locked asks to unlock it and
// leaves the card out of step, to try again; a desk.toml that doesn't read
// says the tickets weren't moved.
func TestCoverPushLocked(t *testing.T) {
	h := newHarness(t)
	pushTicket = func(context.Context, kanban.Link, string, orgs.Config) (string, error) {
		return "", &vault.LockedError{Vault: orgs.Vault{Name: "vw"}}
	}
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme", Pushed: "Done"})
	l := []kanban.Link{{URL: "https://globex.atlassian.net/browse/ABC-1", Kind: links.KindJira, Ref: "ABC-1"}}
	h.off(func() { h.u.push(c.ID, "DESK-1", "Acme", "Backlog", l) })
	h.frame()
	if h.u.b.Card(c.ID).Pushed != "" || len(h.u.locked) != 1 {
		t.Fatalf("pushed %q, locked %d", h.u.b.Card(c.ID).Pushed, len(h.u.locked))
	}
	h.u.b.Delete(c.ID)
	h.off(func() { h.u.push(c.ID, "DESK-1", "Acme", "Backlog", l) }) // gone meanwhile: nothing to mark
	h.orgToml("Acme", "[[jira]\n")
	h.off(func() { h.u.push(c.ID, "DESK-1", "Acme", "Backlog", l) })
	h.frame()
	if !strings.HasPrefix(h.u.toast, "DESK-1: tickets not moved: ") {
		t.Fatalf("toast %q", h.u.toast)
	}
}

// TestCoverPushNoAccount: a ticket with no account to move it with is
// skipped: no toast, not a failure (the last one is forgotten), the card in
// step.
func TestCoverPushNoAccount(t *testing.T) {
	h := newHarness(t)
	pushTicket = func(context.Context, kanban.Link, string, orgs.Config) (string, error) {
		return "", &links.NoAccountError{Provider: "GitHub", Owner: "acme"}
	}
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme", Pushed: "Done"})
	h.u.pushErr[c.ID] = "old"
	l := []kanban.Link{{URL: "https://github.com/acme/api/issues/1", Kind: links.KindGitHubIssue, Ref: "#1"}}
	h.off(func() { h.u.push(c.ID, "DESK-1", "Acme", "Backlog", l) })
	h.frame()
	if _, failed := h.u.pushErr[c.ID]; h.u.toast != "" || failed || h.u.b.Card(c.ID).Pushed != "Backlog" {
		t.Fatalf("toast %q, failed %v, pushed %q", h.u.toast, failed, h.u.b.Card(c.ID).Pushed)
	}
}

// ---- menu, archive, toolbar, views ----

// TestCoverCardMenu: every item of a card's menu does what it says.
func TestCoverCardMenu(t *testing.T) {
	h := newHarness(t)
	startTerm = func(cmd *exec.Cmd, w, ht int, notify func()) (*term.Term, error) {
		return term.Start(prog("cat"), w, ht, notify)
	}
	defer func() { startTerm = term.Start }()
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	id := c.ID
	sz := image.Pt(600, 700)
	sub := false
	w := func(gtx layout.Context) layout.Dimensions {
		if h.u.menu != nil {
			h.u.menu.sub = h.u.menu.sub || sub // the pointer's sweep crosses other rows, which close the columns
			h.u.drawCardMenu(gtx)
		}
		return layout.Dimensions{Size: sz}
	}
	pick := func(label string) {
		sub = strings.HasPrefix(label, "Move to ")
		h.u.menu = &cardMenu{id: id, at: image.Pt(10, 10)}
		h.clickIn(sz, image.Rectangle{Max: sz}, w, "cardMenu"+label)
		if h.u.menu != nil {
			t.Fatalf("%s left the menu open", label)
		}
	}
	pick("Open full window")
	if h.u.view != "sess:"+id {
		t.Fatalf("view %q", h.u.view)
	}
	pick("Claude Code terminal")
	if h.u.terms[id] == nil {
		t.Fatal("no terminal")
	}
	h.u.closeTerm(id)
	pick("Edit card")
	if h.u.edit == nil {
		t.Fatal("no dialog")
	}
	h.u.edit = nil
	pick("Copy key DESK-1")
	if _, b, ok := h.r.WriteClipboard(); !ok || string(b) != "DESK-1" {
		t.Fatalf("clipboard %q", b)
	}
	pick("Move to Done")
	if col, _ := h.u.b.Find(id); col != h.u.b.Column("Done") {
		t.Fatalf("column %d", col)
	}
	h.u.procs[id] = &fakeClaude{}
	h.u.panel, h.u.view = id, "sess:"+id
	pick("Archive")
	if h.u.b.Card(id).Archived.IsZero() || h.u.procs[id] != nil || h.u.panel != "" || h.u.view != "" {
		t.Fatal("Archive")
	}
	h.u.archiveCard("gone")
	h.u.menu = &cardMenu{id: "gone"}
	h.draw(sz, w)
	if h.u.menu != nil {
		t.Fatal("a gone card's menu stayed")
	}
}

// TestCoverArchiveView: Done cards then archived ones, by the search; a
// click previews one; none says so.
func TestCoverArchiveView(t *testing.T) {
	h := newHarness(t)
	done := h.u.b.Add(h.u.b.Column("Done"), kanban.Card{Title: "Ship login", Org: "Acme"})
	gone := h.u.b.Add(0, kanban.Card{Title: "Old idea", Org: "Acme"})
	h.u.b.ArchiveCard(gone.ID)
	h.u.view = viewArchive
	if s := h.u.sentence(); s != "1 done · 1 archived" {
		t.Fatalf("sentence %q", s)
	}
	h.frame()
	sz := image.Pt(900, 600)
	view := func(gtx layout.Context) layout.Dimensions { return h.u.archive(gtx) }
	h.clickIn(sz, image.Rectangle{Max: sz}, view, "arch"+gone.ID)
	if h.u.arch.cur != gone.ID || h.u.panel != "" {
		t.Fatalf("previewed %q, panel %q", h.u.arch.cur, h.u.panel)
	}
	h.u.search.SetText("login")
	h.u.archList.Position.Count = -1
	h.draw(sz, view)
	if h.u.archList.Position.Count != 2 { // its section and itself
		t.Fatalf("searching: %d", h.u.archList.Position.Count)
	}
	h.u.search.SetText("nothing like it")
	h.u.archList.Position.Count = -1
	h.draw(sz, view)
	if h.u.archList.Position.Count != -1 {
		t.Fatal("a list drawn with no matches")
	}
	_ = done
	for i := range h.u.b.Columns {
		if h.u.b.Columns[i].Title == "Done" {
			h.u.b.Columns[i].Title = "Shipped"
		}
	}
	if h.u.doneCards() != nil {
		t.Fatal("no Done column, yet done cards")
	}
}

// TestCoverToolbar: the sidebar toggle, Archive all done (and nothing to
// archive), New card, the search's clear button and its enter.
func TestCoverToolbar(t *testing.T) {
	h := newHarness(t)
	bar := image.Rect(0, 0, h.size.X, 44)
	h.u.b.Add(0, kanban.Card{Title: "Call Globex", Org: "Globex"})
	h.send(press("/", 0))
	h.send(key.EditEvent{Text: "globex"})
	if h.u.search.Text() != "globex" {
		t.Fatalf("search %q", h.u.search.Text())
	}
	h.send(press(key.NameReturn, 0))
	if c := h.u.b.Card(h.u.sel); c == nil || c.Title != "Call Globex" {
		t.Fatalf("enter: sel %q", h.u.sel)
	}
	h.clickScan("tbClear", bar)
	if h.u.search.Text() != "" {
		t.Fatal("Clear")
	}
	h.clickScan("tbSide", bar)
	if h.u.side {
		t.Fatal("the sidebar still shows")
	}
	h.clickScan("tbArchive", bar)
	if h.u.toast != "Nothing in Done to archive" {
		t.Fatalf("toast %q", h.u.toast)
	}
	h.u.b.Add(h.u.b.Column("Done"), kanban.Card{Title: "x", Org: "Acme"})
	h.clickScan("tbArchive", bar)
	if h.u.toast != "Archived 1 done cards" || len(h.u.b.Archive) != 1 {
		t.Fatalf("toast %q", h.u.toast)
	}
	h.clickScan("tbNew", bar)
	if h.u.edit == nil {
		t.Fatal("New card")
	}
}

// TestCoverFrameViews: a session that was working when desk closed is
// interrupted; a panel on a gone card closes; the archive and CI views draw.
func TestCoverFrameViews(t *testing.T) {
	b := kanban.Default()
	b.Add(0, kanban.Card{Title: "x", Status: kanban.StatusWorking})
	u := newUI(b, filepath.Join(t.TempDir(), "board.json"))
	if c := u.b.Columns[0].Cards[0]; c.Status != kanban.StatusWaiting || c.Note != "Interrupted: desk was closed" {
		t.Fatalf("%+v", c)
	}
	h := newHarness(t)
	h.u.panel = "gone"
	h.frame()
	if h.u.panel != "" {
		t.Fatal("a gone card's panel")
	}
	h.u.view = "ci:Acme"
	h.frame()
	if h.u.viewTitle() != "Acme · CI" || h.u.sentence() != h.u.ciSentence("Acme") {
		t.Fatalf("%q %q", h.u.viewTitle(), h.u.sentence())
	}
	h.u.view = viewArchive
	h.frame()
}

// TestCoverViews: a label's title, an unknown link filter, the overdue count.
func TestCoverViews(t *testing.T) {
	h := newHarness(t)
	h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme", Labels: []string{"infra"}, Due: time.Now().AddDate(0, 0, -2).Format(kanban.DateLayout)})
	if inView("ln:other", kanban.Card{Links: []kanban.Link{{Kind: links.KindJira}}}, time.Now()) {
		t.Error("an unknown link filter matched")
	}
	h.u.view = "lab:infra"
	if got := h.u.viewTitle(); got != "#infra" {
		t.Errorf("title %q", got)
	}
	h.u.view = ""
	if got := h.u.sentence(); got != "1 overdue" {
		t.Errorf("sentence %q", got)
	}
}

// ---- the card dialog ----

// TestCoverNewCardFromView: a new card takes the view's organization or label.
func TestCoverNewCardFromView(t *testing.T) {
	h := newHarness(t)
	h.u.view = "org:Globex"
	h.u.newCard(0)
	if h.u.edit.org.Text() != "Globex" {
		t.Fatalf("org %q", h.u.edit.org.Text())
	}
	h.u.view = "lab:infra"
	h.u.newCard(0)
	if h.u.edit.labels.Text() != "infra" {
		t.Fatalf("labels %q", h.u.edit.labels.Text())
	}
	stopped := false
	h.u.edit.optStop = func() { stopped = true }
	h.u.edit.title.SetText("Half written")
	h.u.closeSheet() // Improve running: the dialog stays, untouched, and so does the run
	h.u.commit()
	h.frame()
	for _, id := range []string{"shSave", "shCancel"} { // dimmed: nothing there to click
		if _, ok := h.seek(id, image.Rectangle{Max: h.size}, h.frame); ok {
			t.Fatalf("%s takes clicks while Improve runs", id)
		}
	}
	if stopped || h.u.edit == nil || !h.u.toastBad || h.cards() != 0 {
		t.Fatalf("closed or created while Improve ran: stopped %v, cards %d, toast %q", stopped, h.cards(), h.u.toast)
	}
	h.u.edit.optStop = nil
	h.u.closeSheet()
	if h.u.edit != nil {
		t.Fatal("closing after Stop kept the dialog")
	}
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	h.u.b.ArchiveCard(c.ID)
	h.u.open(c.ID)
	if h.u.edit != nil {
		t.Fatal("an archived card opened in the dialog")
	}
}

// TestCoverCommit: what Save refuses (no title, a bad date, an unknown
// organization or folder, a word that isn't a link), what it keeps (a link's
// last state), a move to another column, and a card gone meanwhile.
func TestCoverCommit(t *testing.T) {
	h := newHarness(t)
	l := kanban.Link{URL: "https://example.com/pr", Kind: links.KindURL, Ref: "pr", State: "open"}
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme", Links: []kanban.Link{l}})
	h.u.open(c.ID)
	s := h.u.edit
	for _, f := range []struct {
		set  func()
		want string
	}{
		{func() { s.title.SetText(" ") }, "A card needs a title"},
		{func() { s.title.SetText("x"); s.due.SetText("tomorrow") }, "Due date: use YYYY-MM-DD"},
		{func() { s.due.SetText(""); s.org.SetText("Nowhere") }, "Pick the organization"},
		{func() { s.org.SetText("Acme"); s.dir.SetText("missing") }, "Folder: no missing inside Acme"},
		{func() { s.dir.SetText(""); s.links.SetText("https://example.com/pr notalink") }, "Not a link: notalink"},
	} {
		f.set()
		h.u.commit()
		if h.u.edit != s || !strings.HasPrefix(h.u.toast, f.want) {
			t.Fatalf("toast %q, want %q", h.u.toast, f.want)
		}
	}
	s.links.SetText("https://example.com/pr")
	s.col = 2
	h.u.commit()
	got := h.u.b.Card(c.ID)
	if col, _ := h.u.b.Find(c.ID); h.u.edit != nil || col != 2 || got.Links[0].State != "open" {
		t.Fatalf("saved: col %d, links %+v", col, got.Links)
	}
	h.u.open(c.ID)
	h.u.b.Delete(c.ID)
	h.u.commit()
	if h.u.edit != nil || h.cards() != 0 {
		t.Fatal("a gone card's dialog")
	}
}

// TestCoverNotesEnter: enter in an optimized new card's notes creates it.
func TestCoverNotesEnter(t *testing.T) {
	h := newHarness(t)
	h.u.newCard(0)
	h.frame()
	s := h.u.edit
	s.optimized, s.preview = true, false
	s.title.SetText("Ship it")
	s.org.SetText("Acme")
	h.frame()
	for i := 0; i < 40 && !h.focusedNotes(s); i++ { // tab to the notes
		h.r.MoveFocus(key.FocusForward)
		h.frame()
	}
	if !h.focusedNotes(s) {
		t.Fatal("the notes never took the keyboard")
	}
	h.send(press(key.NameReturn, key.ModShortcut))
	if h.u.edit != nil || h.cards() != 1 {
		t.Fatalf("Ctrl+Enter in the notes: edit %v, %d cards", h.u.edit != nil, h.cards())
	}
}

// focusedNotes is whether the dialog's notes have the keyboard.
func (h *harness) focusedNotes(s *sheet) bool {
	h.ops.Reset()
	gtx := layout.Context{Ops: &h.ops, Source: h.r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	return gtx.Focused(&s.notes)
}

// TestCoverPlacePicker: the organization chips (none matching says so),
// stepping into folders and up, a long folder's overflow, and changing the
// organization.
func TestCoverPlacePicker(t *testing.T) {
	h := newHarness(t)
	h.u.newCard(0)
	s := h.u.edit
	sz := image.Pt(1000, 400) // room for "change organization" after macOS's long $TMPDIR path
	w := func(gtx layout.Context) layout.Dimensions { return h.u.placePicker(gtx, s) }
	all := image.Rectangle{Max: sz}
	s.org.SetText("zzzz")
	h.draw(sz, w)
	s.org.SetText("")
	h.clickIn(sz, all, w, "orgAcme")
	if s.org.Text() != "Acme" {
		t.Fatalf("org %q", s.org.Text())
	}
	h.clickIn(sz, all, w, "dirapi")
	s.dir.SetText("api/") // its subfolders, asked for
	h.clickIn(sz, all, w, "dirv2")
	if s.dir.Text() != "api/v2" {
		t.Fatalf("dir %q", s.dir.Text())
	}
	h.clickIn(sz, all, w, "dirUp")
	if s.dir.Text() != "api" {
		t.Fatalf("up: %q", s.dir.Text())
	}
	h.clickIn(sz, all, w, "dirUp")
	if s.dir.Text() != "" {
		t.Fatalf("up to the org: %q", s.dir.Text())
	}
	h.clickIn(sz, all, w, "orgChange")
	if s.org.Text() != "" {
		t.Fatal("change organization")
	}
	for i := range 41 {
		os.Mkdir(filepath.Join(h.u.root, "Globex", fmt.Sprintf("d%02d", i)), 0o755)
	}
	s.org.SetText("Globex")
	h.draw(sz, w)
	if len(s.dirs) != 41 {
		t.Fatalf("%d folders", len(s.dirs))
	}
}

// TestFolderFilter: the folder field filters its parent's subfolders by
// what's typed after the last "/"; a picked folder lists nothing until a "/"
// follows it (enter's first match: TestDialogTab).
func TestFolderFilter(t *testing.T) {
	h := newHarness(t)
	for _, d := range []string{"car_platform/logs", "cache", "web/api", "web/app"} {
		os.MkdirAll(filepath.Join(h.u.root, "Acme", d), 0o755)
	}
	h.u.newCard(0)
	s := h.u.edit
	s.org.SetText("Acme")
	for text, want := range map[string]string{
		"":              "api cache car_platform web",
		"ca":            "cache car_platform",
		"car":           "car_platform",
		"car_platform":  "",
		"car_platform/": "logs",
		"web/ap":        "api app",
		"/web/":         "api app",
		"zz":            "",
	} {
		s.dir.SetText(text)
		_, _, dirs := h.u.dirChoices(s, "Acme")
		slices.Sort(dirs)
		if got := strings.Join(dirs, " "); got != want {
			t.Errorf("%q lists %q, want %q", text, got, want)
		}
	}
	s.dir.SetText("web/ap")
	if dir, q, _ := h.u.dirChoices(s, "Acme"); dir != "web" || q != "ap" {
		t.Errorf("web/ap: in %q, filter %q", dir, q)
	}
}

// TestOptimizeInFolder: Improve runs where the session will, in the folder
// picked (with the org's folder for its skills), and offers the org's folders
// only when none is picked or what's typed isn't one.
func TestOptimizeInFolder(t *testing.T) {
	h := newHarness(t)
	asks := make(chan claude.Ask, 3)
	optimize = func(_ context.Context, a claude.Ask, _ func(claude.Progress)) (claude.Draft, error) {
		asks <- a
		return claude.Draft{}, nil
	}
	h.u.newCard(0)
	s := h.u.edit
	s.org.SetText("Acme")
	s.title.SetText("Review the web app")
	root := orgs.Dir(h.u.root, "Acme", "")
	for _, c := range []struct{ typed, folder, dir string }{{"api", "api", filepath.Join(root, "api")}, {"", "", root}, {"ap", "", root}} {
		s.dir.SetText(c.typed)
		h.u.optimizeCard()
		a := <-asks
		h.pumpUntil("the draft", func() bool { return s.optStop == nil })
		if a.Folder != c.folder || a.Dir != c.dir || a.Root != root || (c.folder == "") != slices.Contains(a.Folders, "api") {
			t.Errorf("folder %q: asked in %q (root %q) for %q, folders %v", c.typed, a.Dir, a.Root, a.Folder, a.Folders)
		}
	}
}

// TestCoverSheetRows: the due shortcuts, priority, column and the ticket
// choice of an org with a tracker.
func TestCoverSheetRows(t *testing.T) {
	h := newHarness(t)
	h.orgToml("Acme", "[[jira]]\nurl = \"https://globex.atlassian.net\"\nprojects = [\"ABC\"]\ntoken = \"t\"\n")
	h.u.newCard(0)
	s := h.u.edit
	s.optimized = true
	s.org.SetText("Acme")
	sz := image.Pt(800, 1400)
	w := func(gtx layout.Context) layout.Dimensions {
		var kids []layout.FlexChild
		for _, r := range h.u.sheetRows() {
			kids = append(kids, layout.Rigid(r.w))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	}
	all := image.Rectangle{Max: sz}
	h.clickIn(sz, all, w, "ticket1")
	if !s.ticket || s.tracker == "" {
		t.Fatalf("ticket %v, tracker %q", s.ticket, s.tracker)
	}
	h.clickIn(sz, all, w, "dueTomorrow")
	if want := time.Now().AddDate(0, 0, 1).Format(kanban.DateLayout); s.due.Text() != want {
		t.Fatalf("due %q", s.due.Text())
	}
	h.clickIn(sz, all, w, "dueClear")
	if s.due.Text() != "" {
		t.Fatal("Clear")
	}
	h.clickIn(sz, all, w, "prio3")
	h.clickIn(sz, all, w, "col2")
	if s.prio != 3 || s.col != 2 {
		t.Fatalf("prio %d col %d", s.prio, s.col)
	}
	s.optNote = "⚠ Haiku failed"
	h.draw(image.Pt(800, 60), func(gtx layout.Context) layout.Dimensions { return h.u.sheetFooter(gtx) })
}

// ---- Prompt Optimization ----

// TestCoverOptimizeEdges: nothing to optimize says so; the title stands in
// for empty notes; again while it runs stops it; a ticket whose vault is
// locked, or that fails, is noted for the optimizer; a dialog closed
// meanwhile is left alone; an error shows; the tools it picked are named.
func TestCoverOptimizeEdges(t *testing.T) {
	h := newHarness(t)
	h.orgToml("Acme", "[[jira]]\nurl = \"https://globex.atlassian.net\"\nprojects = [\"ABC\"]\n")
	h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme", Labels: []string{"infra"}})
	var ask claude.Ask
	release := make(chan struct{})
	optimize = func(ctx context.Context, a claude.Ask, _ func(claude.Progress)) (claude.Draft, error) {
		ask = a
		select {
		case <-release:
		case <-ctx.Done():
			return claude.Draft{}, ctx.Err()
		}
		return claude.Draft{Tools: []claude.Tool{{Kind: "skill", Name: "team-tools:work-ticket"}}}, nil
	}
	describe = func(_ context.Context, l kanban.Link, _ orgs.Config) (string, error) {
		if l.Ref == "ABC-1" {
			return "", &vault.LockedError{Vault: orgs.Vault{Name: "vw"}}
		}
		return "", errors.New("404 Not Found")
	}
	defer func() { describe = links.Describe }()
	h.u.newCard(0)
	s := h.u.edit
	s.org.SetText("Acme")
	h.u.optimizeCard()
	if h.u.toast != "Write what to do first" || s.optStop != nil {
		t.Fatalf("toast %q", h.u.toast)
	}
	s.title.SetText("Look at ABC-1 and ABC-2.")
	h.u.optimizeCard()
	if s.optStop == nil {
		t.Fatal("not optimizing")
	}
	h.u.optimizeCard() // again: stops it
	h.pumpUntil("the stop", func() bool { return s.optStop == nil })
	if s.optNote != "Stopped" || len(h.u.locked) != 1 || ask.Request != "Look at ABC-1 and ABC-2." ||
		len(ask.Linked) != 2 || ask.Linked[0] != "ABC-1: not read (its vault is locked)" || ask.Linked[1] != "ABC-2: not read (404 Not Found)" || len(ask.Labels) != 1 {
		t.Fatalf("note %q, asked %+v", s.optNote, ask)
	}
	h.u.optimizeCard()
	close(release)
	h.pumpUntil("the draft", func() bool { return s.optStop == nil })
	if s.optNote != "Claude's suggestions, after reading team-tools:work-ticket: look them over, then Create" || s.title.Text() != "Look at ABC-1 and ABC-2." {
		t.Fatalf("note %q, title %q", s.optNote, s.title.Text())
	}
	optimize = func(context.Context, claude.Ask, func(claude.Progress)) (claude.Draft, error) {
		return claude.Draft{}, errors.New("rate limited\nmore")
	}
	h.u.optimizeCard()
	h.pumpUntil("the error", func() bool { return s.optStop == nil })
	if !strings.HasPrefix(s.optNote, "⚠") || s.optErr != "rate limited\nmore" { // an error, not the user's Stop: whole, in the body
		t.Fatalf("note %q, error %q", s.optNote, s.optErr)
	}
	h.u.optimizeCard()
	h.u.edit = nil // closed meanwhile
	for end := time.Now().Add(200 * time.Millisecond); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		h.frame()
	}
	if s.optStop == nil {
		t.Fatal("a closed dialog was updated")
	}
}

// TestCoverApplyDraftTitle: a draft without a title takes the request's first line.
func TestCoverApplyDraftTitle(t *testing.T) {
	h := newHarness(t)
	h.u.newCard(0)
	s := h.u.edit
	s.notes.SetText("Renew the cert\nbefore Friday")
	h.u.applyDraft(s, "Acme", claude.Draft{})
	if s.title.Text() != "Renew the cert" {
		t.Fatalf("title %q", s.title.Text())
	}
}

// ---- organization settings ----

func TestCoverConfigHelpers(t *testing.T) {
	if i, _ := kindOf("nope"); i != -1 {
		t.Error("an unknown kind")
	}
	pw := map[string]any{"token_env": "PW"}
	if m := sub(map[string]any{"password": pw}, "password"); m["token_env"] != "PW" {
		t.Errorf("sub %v", m)
	}
	h := newHarness(t)
	h.u.view = "org:Globex"
	if h.u.configOrg() != "Globex" {
		t.Error("the view's org")
	}
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	h.u.view, h.u.sel = "", c.ID
	if h.u.configOrg() != "Acme" {
		t.Error("the selected card's org")
	}
}

// TestCoverConfigBroken: a desk.toml that doesn't parse opens read-only:
// it says why and Save is neither shown nor done.
func TestCoverConfigBroken(t *testing.T) {
	h := newHarness(t)
	h.orgToml("Acme", "[[jira]\n")
	h.u.openConfig("Acme")
	d := h.u.cfg
	if !d.broken || !strings.HasSuffix(d.err, "fix it by hand") {
		t.Fatalf("broken %v, err %q", d.broken, d.err)
	}
	h.frame()
	h.u.saveConfig()
	if b, _ := os.ReadFile(orgs.File(h.u.root, "Acme")); string(b) != "[[jira]\n" || h.u.cfg == nil {
		t.Fatal("a broken file was saved over")
	}
}

// TestCoverConfigSave: what Save refuses (two vaults of one name, a vault
// without one, a file it can't write) and what it writes (keys desk
// doesn't edit kept, an empty list dropped, a vault's password source).
func TestCoverConfigSave(t *testing.T) {
	h := newHarness(t)
	h.orgToml("Acme", "agent = \"reviewer\"\n[[gitlab]]\ngroups = [\"acme\"]\n")
	h.u.openConfig("Acme")
	d := h.u.cfg
	v1, v2 := newEntry("vault", map[string]any{}), newEntry("vault", map[string]any{})
	v1.ed("name").SetText("vw")
	v2.ed("name").SetText("vw")
	d.entries = append(d.entries, v1, v2)
	h.u.saveConfig()
	if d.err != "Two vaults are called vw" {
		t.Fatalf("err %q", d.err)
	}
	v2.ed("name").SetText("")
	h.u.saveConfig()
	if d.err != "A vault needs a name" {
		t.Fatalf("err %q", d.err)
	}
	d.entries = d.entries[:2]
	d.entries[0].ed("groups").SetText(" , ")
	v1.src["password"] = srcEnv
	v1.ed("password.token_env").SetText("VW_PASSWORD")
	file := orgs.File(h.u.root, "Acme")
	os.Remove(file)
	os.Mkdir(file, 0o755) // a folder where the file goes
	h.u.saveConfig()
	if !strings.HasPrefix(d.err, "Not saved: ") {
		t.Fatalf("err %q", d.err)
	}
	os.Remove(file)
	h.u.saveConfig()
	if h.u.cfg != nil {
		t.Fatalf("not saved: %q", d.err)
	}
	raw, err := orgs.LoadRaw(h.u.root, "Acme")
	cfg, _ := orgs.Load(h.u.root, "Acme")
	if err != nil || raw["agent"] != "reviewer" || len(cfg.GitLab) != 1 || cfg.GitLab[0].Groups != nil || cfg.Vaults[0].Password.TokenEnv != "VW_PASSWORD" {
		t.Fatalf("saved %v %+v %v", raw, cfg, err)
	}
}

// TestCoverFoldersSave: a depth that isn't one, no folders, and settings
// that can't be written are refused; a folder under home shows as ~.
func TestCoverFoldersSave(t *testing.T) {
	h := newHarness(t)
	h.u.home = filepath.Dir(h.u.root)
	h.u.openConfig(cfgFolders)
	d := h.u.cfg
	if p := d.entries[0].text("path"); p != "~/"+filepath.Base(h.u.root) { // ~/ on Windows too
		t.Fatalf("path %q", p)
	}
	d.entries[0].ed("path").SetText(h.u.root)
	d.entries[0].ed("depth").SetText("9")
	h.u.saveConfig()
	if !strings.HasSuffix(d.err, "its depth is a number from 0 to 6") {
		t.Fatalf("err %q", d.err)
	}
	d.entries[0].ed("depth").SetText("2")
	all := d.entries
	d.entries = nil
	h.u.saveConfig()
	if d.err != "Add a folder: desk needs one to find organizations" {
		t.Fatalf("err %q", d.err)
	}
	d.entries = all
	if d.changes() != 1 {
		t.Fatalf("a folder edited: %d changes", d.changes())
	}
	d.langs.ed("chat").SetText("Spanish") // the languages: one more change, saved with the folders
	if d.changes() != 2 {
		t.Fatalf("the languages edited: %d changes", d.changes())
	}
	h.u.saveConfig()
	if card, chat := orgs.Langs(); card != "English" || chat != "Spanish" {
		t.Fatalf("languages %q %q", card, chat)
	}
	h.u.cfg = d // saved, it closed: the same page again
	d.langs.ed("card").SetText("French")
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, nil, 0o644)
	t.Setenv("XDG_CONFIG_HOME", blocker)
	h.u.saveConfig()
	if !strings.HasPrefix(d.err, "Not saved: ") || h.u.cfg != d {
		t.Fatalf("err %q", d.err)
	}
}

// TestCoverConfigDialog: the dialog's own buttons: another organization's
// tab, an account's Remove, a vault's kind, a token's sources (a vault
// item: pick one of the vaults, or none to pick), the Folders tab's toggle,
// Cancel.
func TestCoverConfigDialog(t *testing.T) {
	h := newHarness(t)
	h.u.openConfig("Acme")
	d := h.u.cfg
	sz := image.Pt(900, 3000)
	w := func(gtx layout.Context) layout.Dimensions {
		h.u.configDialog(gtx)
		return layout.Dimensions{Size: sz}
	}
	all := image.Rectangle{Max: sz}
	h.clickIn(sz, all, w, "cfgAddjira")
	h.clickIn(sz, all, w, "cfgAddvault")
	jira, v := d.entries[0], d.entries[1]
	jid, vid := fmt.Sprintf("cfg%p", jira), fmt.Sprintf("cfg%p", v)
	if d.open != v {
		t.Fatal("the account added is not the one open")
	}
	h.clickIn(sz, all, w, jid+"hd")
	if d.open != jira {
		t.Fatal("a click on its header did not open the account")
	}
	h.clickIn(sz, all, w, jid+".src1") // a vault item: none yet
	if jira.src["."] != srcVault {
		t.Fatalf("source %d", jira.src["."])
	}
	v.ed("name").SetText("vw")
	h.clickIn(sz, all, w, jid+".vvw")
	if jira.text("..vault") != "vw" {
		t.Fatalf("vault %q", jira.text("..vault"))
	}
	for i, want := range []int{srcEnv, srcCmd, srcFile} {
		h.clickIn(sz, all, w, fmt.Sprint(jid, ".src", i+2))
		if jira.src["."] != want {
			t.Fatalf("source %d, want %d", jira.src["."], want)
		}
	}
	h.clickIn(sz, all, w, vid+"hd")
	h.clickIn(sz, all, w, vid+"kind1")
	if v.vkind != 1 {
		t.Fatal("vault kind")
	}
	h.clickIn(sz, all, w, vid+"hd")
	if d.open != nil {
		t.Fatal("a click on the open account's header did not close it")
	}
	h.clickIn(sz, all, w, jid+"hd")
	h.clickIn(sz, all, w, jid+"rm")
	if len(d.entries) != 1 || d.entries[0] != v {
		t.Fatalf("%d entries", len(d.entries))
	}
	h.clickIn(sz, all, w, "cfgOrgGlobex")
	if h.u.cfg == d || h.u.cfg.org != "Globex" {
		t.Fatal("Globex's tab")
	}
	h.clickIn(sz, all, w, "cfgOrg"+cfgFolders)
	f := h.u.cfg.entries[0]
	h.clickIn(sz, all, w, fmt.Sprintf("cfg%p", f)+"root")
	if !f.on["root"] {
		t.Fatal("root toggle")
	}
	h.clickIn(sz, all, w, "cfgOrgAcme")
	if h.u.cfg != d || len(d.entries) != 1 {
		t.Fatal("going back to Acme lost its edits")
	}
	h.clickIn(sz, all, w, "cfgOrg"+cfgFolders)
	h.u.cfg.entries = nil
	h.draw(sz, w) // no folders: says so
	h.clickIn(sz, all, w, "cfgClose")
	if h.u.cfg == nil || !h.u.cfg.warn {
		t.Fatal("close dropped unsaved changes")
	}
	h.clickIn(sz, all, w, "cfgClose")
	if h.u.cfg != nil {
		t.Fatal("close, asked again")
	}
}

// ---- keys ----

// TestCoverEscape: esc backs out of the picker, the search, an answer
// field; with nothing open it drops the selection. A key's release does nothing.
func TestCoverEscape(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	h.u.pick = "Acme"
	h.send(press(key.NameEscape, 0))
	if h.u.pick != "" {
		t.Fatal("the picker")
	}
	h.u.search.SetText("x")
	h.send(press(key.NameEscape, 0))
	if h.u.search.Text() != "" {
		t.Fatal("the search")
	}
	h.u.sel = c.ID
	h.send(key.Event{Name: key.NameEscape, State: key.Release})
	if h.u.sel != c.ID {
		t.Fatal("a release backed out")
	}
	h.send(press(key.NameEscape, 0))
	if h.u.sel != "" {
		t.Fatal("the selection")
	}
	var qs []claude.Question
	json.Unmarshal([]byte(`[{"question":"Which?","options":[{"label":"A"}]}]`), &qs)
	h.u.asks[c.ID] = newAsk(&claude.Request{ID: "r1", Questions: qs})
	h.u.openSession(c.ID)
	h.frame()
	h.r.Source().Execute(key.FocusCmd{Tag: &h.u.asks[c.ID].other[0]})
	h.frame()
	h.send(press(key.NameEscape, 0))
	if h.u.view != "sess:"+c.ID || !h.u.blur && h.typingAnswer(c.ID) {
		t.Fatalf("esc in an answer left the session: view %q", h.u.view)
	}
}

// TestCoverTypingInSelection: with chat text selected (its selectable has
// the keyboard) the board's letter keys stay off: Ctrl+C is for the text.
func TestCoverTypingInSelection(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	h.u.say(c.ID, "you", "hello there")
	h.u.openPanel(c.ID)
	h.frame()
	h.u.chatFocus = false
	var sel *widget.Selectable
	for _, s := range h.u.sels {
		sel = s
	}
	if sel == nil {
		t.Fatal("no selectable text")
	}
	h.r.Source().Execute(key.FocusCmd{Tag: sel})
	h.frame()
	h.frame()
	if !sel.Focused() {
		t.Fatal("the text did not take the keyboard")
	}
	h.send(press("E", 0))
	if h.u.edit != nil {
		t.Fatal("E opened the dialog while text was selected")
	}
}

func (h *harness) typingAnswer(id string) bool {
	gtx := layout.Context{Ops: new(op.Ops), Source: h.r.Source()}
	return gtx.Focused(&h.u.asks[id].other[0])
}

// TestCoverNavMove: arrows skip empty columns; Ctrl+arrows move a card up,
// down and across, and nothing past the ends; a card out of view scrolls in.
func TestCoverNavMove(t *testing.T) {
	h := newHarness(t)
	a := h.u.b.Add(0, kanban.Card{Title: "a", Org: "Acme"})
	b := h.u.b.Add(0, kanban.Card{Title: "b", Org: "Acme"})
	c := h.u.b.Add(0, kanban.Card{Title: "c", Org: "Acme"})
	d := h.u.b.Add(2, kanban.Card{Title: "d", Org: "Acme"})
	h.frame()
	h.u.moveSel(0, 1) // nothing selected
	h.u.sel = a.ID
	h.u.nav(1, 0) // column 1 is empty: on to 2
	if h.u.sel != d.ID {
		t.Fatalf("nav right: %q", h.u.sel)
	}
	titles := func() string {
		var s []string
		for _, x := range h.u.b.Columns[0].Cards {
			s = append(s, x.Title)
		}
		return strings.Join(s, "")
	}
	h.u.sel = a.ID
	h.u.moveSel(0, -1) // the top: no move
	h.u.moveSel(0, 1)
	if titles() != "bac" {
		t.Fatalf("down: %s", titles())
	}
	h.u.moveSel(0, 1)
	if titles() != "bca" {
		t.Fatalf("down to the end: %s", titles())
	}
	h.u.moveSel(0, -1)
	if titles() != "bac" {
		t.Fatalf("up: %s", titles())
	}
	h.u.sel = b.ID
	h.u.moveSel(2, 0) // beside, at the same height: before d
	if col, _ := h.u.b.Find(b.ID); col != 2 || h.u.b.Columns[2].Cards[0].ID != b.ID {
		t.Fatalf("across: column %d", col)
	}
	_ = c
	h.frame()
	for i, x := range h.u.lay.cards {
		if x.id == a.ID {
			body := h.u.lay.cols[x.col]
			h.u.sel = a.ID
			h.u.lay.cards[i].r = image.Rect(x.r.Min.X, body.Min.Y-100, x.r.Max.X, body.Min.Y-20)
			s := h.u.scroll[x.col]
			h.u.reveal()
			if h.u.scroll[x.col] >= s {
				t.Fatal("a card above did not scroll in")
			}
			h.u.lay.cards[i].r = image.Rect(x.r.Min.X, body.Max.Y+20, x.r.Max.X, body.Max.Y+100)
			s = h.u.scroll[x.col]
			h.u.reveal()
			if h.u.scroll[x.col] <= s {
				t.Fatal("a card below did not scroll in")
			}
		}
	}
	h.u.sel = "gone"
	h.u.nav(0, 1)
	if h.u.sel != b.ID && h.u.sel != c.ID && h.u.sel != a.ID {
		t.Fatalf("from nothing: %q", h.u.sel)
	}
}

// ---- the board's pointer ----

// TestCoverBoardPointer: the wheel scrolls a column; a press outside the
// card menu closes it; another button does nothing; the column's + and Add
// card open the dialog; the drop marker past the last card, and nothing to
// draw for a gone card or no columns.
func TestCoverBoardPointer(t *testing.T) {
	h := newHarness(t)
	for i := range 6 {
		h.u.b.Add(0, kanban.Card{Title: fmt.Sprint("card ", i), Org: "Acme", Labels: []string{"a", "b", "c", "d"},
			Links: []kanban.Link{{Ref: "1", Tone: links.ToneGood}, {Ref: "2"}, {Ref: "3"}, {Ref: "4"}}})
	}
	h.size = image.Pt(1700, 500)
	h.frame()
	col0 := h.u.lay.cols[0]
	mid := f32.Pt(float32(boardAt.X+col0.Min.X+40), float32(boardAt.Y+col0.Min.Y+60))
	h.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: mid},
		pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: mid, Scroll: f32.Pt(0, 80)})
	if h.u.scroll[0] <= 0 {
		t.Fatalf("scroll %d", h.u.scroll[0])
	}
	h.u.menu = &cardMenu{id: h.u.b.Columns[0].Cards[0].ID, at: image.Pt(900, 100)}
	h.frame()
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: mid},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: mid})
	if h.u.menu != nil {
		t.Fatal("a press outside kept the menu")
	}
	h.u.panel = ""
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonTertiary, Position: mid},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: mid})
	if h.u.panel != "" {
		t.Fatal("the middle button opened a card")
	}
	h.clickScan("add1", image.Rect(boardAt.X, boardAt.Y, h.size.X, boardAt.Y+60))
	if h.u.edit == nil || h.u.edit.col != 1 {
		t.Fatal("the column's +")
	}
	h.u.edit = nil
	h.clickScan("qa1", image.Rect(boardAt.X, boardAt.Y, h.size.X, h.size.Y))
	if h.u.edit == nil || h.u.edit.col != 1 {
		t.Fatal("Add card")
	}
	h.u.edit = nil
	h.frame()
	sz := image.Pt(1400, 500)
	first := h.u.lay.cards[0]
	h.u.drag = dragState{press: first.id, at: image.Pt(first.r.Min.X+5, 1<<20), w: first.r.Dx()}
	if col, before := h.u.lay.dropAt(h.u.drag.at, first.id); col != 0 || before != "" {
		t.Fatalf("drop past the end: %d %q", col, before)
	}
	overlay := func(gtx layout.Context) layout.Dimensions { h.u.dragOverlay(gtx); return layout.Dimensions{} }
	h.draw(sz, overlay)
	h.u.drag.press = "gone"
	h.draw(sz, overlay)
	h.u.lay = boardLayout{}
	h.draw(sz, overlay)
	if h.u.colAccent(1) != kit.CAcc {
		t.Error("To do's color")
	}
	h.u.b.Columns[1].Title = "QA"
	if h.u.colAccent(1) != kit.Accents[1] {
		t.Error("an unnamed column's color by place")
	}
}

// ---- Claude's questions ----

// TestCoverAnswerAsk: a gone card or process; an answer written in; an
// unanswered question; a reply that can't be sent; a box with a multiple
// choice and option descriptions draws.
func TestCoverAnswerAsk(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	var qs []claude.Question
	json.Unmarshal([]byte(`[{"question":"Which DB?","multiSelect":true,"options":[{"label":"Postgres","description":"the usual"},{"label":"SQLite"}]}]`), &qs)
	h.u.answerAsk("gone", true)
	h.u.asks[c.ID] = newAsk(&claude.Request{ID: "r1", Input: json.RawMessage(`{}`), Questions: qs})
	h.u.answerAsk(c.ID, true) // no process
	if h.u.asks[c.ID] != nil {
		t.Fatal("an ask with no process stayed")
	}
	fake := &fakeClaude{}
	h.u.procs[c.ID] = fake
	h.u.asks[c.ID] = newAsk(&claude.Request{ID: "r1", Input: json.RawMessage(`{}`), Questions: qs})
	var act func()
	d := h.draw(image.Pt(600, 600), func(gtx layout.Context) layout.Dimensions { return h.u.askBox(gtx, c.ID, h.u.asks[c.ID], &act) })
	if d.Size.Y == 0 {
		t.Fatal("no box")
	}
	h.u.answerAsk(c.ID, true)
	if h.u.toast != "Answer every question (pick an option or write one)" || len(fake.replies) != 0 {
		t.Fatalf("toast %q", h.u.toast)
	}
	h.u.asks[c.ID].other[0].SetText("DuckDB")
	h.u.answerAsk(c.ID, true)
	if len(fake.replies) != 1 || !strings.Contains(fake.replies[0], `"Which DB?":"DuckDB"`) {
		t.Fatalf("replies %q", fake.replies)
	}
	h.u.procs[c.ID] = &respondFails{}
	h.u.asks[c.ID] = newAsk(&claude.Request{ID: "r2", Line: "Bash rm -rf build"})
	h.u.answerAsk(c.ID, false)
	if h.u.toast != "Claude: pipe closed" || h.u.asks[c.ID] == nil {
		t.Fatalf("toast %q", h.u.toast)
	}
}

type respondFails struct{ fakeClaude }

func (*respondFails) Respond(string, bool, json.RawMessage, string) error {
	return errors.New("pipe closed")
}

// ---- explain ----

// TestCoverExplain: asked once (not again while it reads or after it
// answered), not for a gone card; a failure offers Try again; Explain in
// the chat asks.
func TestCoverExplain(t *testing.T) {
	h := newHarness(t)
	calls := 0
	explainCmd = func(context.Context, string, string, string, string) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("overloaded")
		}
		return "1. Lists files.", nil
	}
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	h.u.explain("gone", "ls")
	if h.u.explains["ls"] != nil {
		t.Fatal("asked for a gone card")
	}
	sz := image.Pt(600, 300)
	box := func(gtx layout.Context) layout.Dimensions { return h.u.explainBox(gtx, c.ID, "ls", false) }
	if d := h.draw(sz, box); d.Size != (image.Point{}) {
		t.Fatal("a box before asking")
	}
	btn := func(gtx layout.Context) layout.Dimensions { return h.u.explainBtn(gtx, c.ID, "ls") }
	h.clickIn(sz, image.Rectangle{Max: sz}, btn, "explain"+c.ID+"ls")
	h.u.explain(c.ID, "ls") // busy: not again
	h.pumpUntil("the answer", func() bool { return !h.u.explains["ls"].busy })
	if calls != 1 || h.u.explains["ls"].err != "overloaded" {
		t.Fatalf("calls %d, err %q", calls, h.u.explains["ls"].err)
	}
	h.clickIn(sz, image.Rectangle{Max: sz}, box, "explainRetry"+"ls")
	h.pumpUntil("the retry", func() bool { return !h.u.explains["ls"].busy })
	h.u.explain(c.ID, "ls") // answered: not again
	if calls != 2 || h.u.explains["ls"].text != "1. Lists files." {
		t.Fatalf("calls %d, text %q", calls, h.u.explains["ls"].text)
	}
}

// ---- servers ----

// TestCoverServersNoInfra: without the workspace, server rows aren't
// clicks, the view says it isn't running, the picker and sentence are empty.
func TestCoverServersNoInfra(t *testing.T) {
	h := newHarness(t)
	if h.u.serverClick("srvc:Acme") || h.u.pickRows("Acme", "") != nil || h.u.serversSentence("Acme") != "" || h.u.cardOfKey("nope") != nil {
		t.Fatal("a server without the workspace")
	}
	h.u.view = "srv:Acme"
	h.frame()
	if _, err := h.u.cardNew("Acme", " ", ""); err == nil {
		t.Fatal("a card without a title")
	}
	for i := range h.u.b.Columns {
		if h.u.b.Columns[i].Title == "To do" {
			h.u.b.Columns[i].Title = "Next"
		}
	}
	key, err := h.u.cardNew("Acme", "Rotate logs", "")
	if c := h.u.cardOfKey(key); err != nil || c == nil || h.u.b.Columns[0].Cards[0].ID != c.ID {
		t.Fatalf("%q %v", key, err)
	}
}

// TestCoverOnBoardTimeout: a board that doesn't answer in time is an error.
func TestCoverOnBoardTimeout(t *testing.T) {
	defer func(d time.Duration) { boardWait = d }(boardWait)
	boardWait = 10 * time.Millisecond
	u := newUI(kanban.Default(), filepath.Join(t.TempDir(), "board.json"))
	if _, err := onBoard(u, func() (int, error) { return 1, nil }); err == nil || !strings.HasPrefix(err.Error(), "desk's board didn't answer in time") {
		t.Fatalf("%v", err)
	}
}

// TestCoverServersInfra: with the workspace: the focused servers view
// tells it; the org's Claude row and a host row open them; the picker's
// enter, a press outside it, and no match; the view follows the workspace
// to another org and lists the cards whose server work it is; a reload
// reloads it.
func TestCoverServersInfra(t *testing.T) {
	for _, v := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "XDG_CACHE_HOME", "HOME"} {
		t.Setenv(v, t.TempDir())
	}
	fakeBin(t, "claude", "exec cat")
	h := newHarness(t)
	for _, org := range []string{"Acme", "Globex"} {
		localServer(t, h.u.root, org)
	}
	h.u.infra = infra.New(h.u.root, func() {})
	defer h.u.infra.Close()
	h.u.focused = true
	h.u.view = "srv:Acme"
	h.frame()
	if !h.u.infraOn {
		t.Fatal("the workspace wasn't told it's in view")
	}
	if h.u.serverClick("side") {
		t.Fatal("not a server row")
	}
	if !h.u.serverClick("srvc:Globex") || h.u.view != "srv:Globex" {
		t.Fatalf("the org's Claude row: view %q", h.u.view)
	}
	h.pumpUntil("Globex's tab", func() bool { return h.u.infra.ActiveOrg() == "Globex" })
	h.u.view = "srv:Acme"
	h.frame() // follows the workspace to Globex
	if h.u.view != "srv:Globex" {
		t.Fatalf("view %q", h.u.view)
	}
	split := h.u.b.Add(0, kanban.Card{Title: "Check web1", Org: "Acme"}) // its server work is Acme's: the session view opens Acme's beside it
	h.u.cardInfra[split.ID] = "Acme"
	h.u.view = "sess:" + split.ID
	h.frame()
	h.pumpUntil("Acme's tab beside the chat", func() bool { return h.u.infra.ActiveOrg() == "Acme" })
	h.u.serverClick("srvc:Globex")
	h.pumpUntil("Globex's tab again", func() bool { return h.u.infra.ActiveOrg() == "Globex" })
	c := h.u.b.Add(0, kanban.Card{Title: "Run migration", Org: "Globex"})
	h.u.b.Add(h.u.b.Column("Done"), kanban.Card{Title: "old", Org: "Globex"})
	h.u.cardInfra[c.ID] = "Globex"
	h.u.cardInfra["other"] = "Acme"
	for _, x := range h.u.b.Columns[h.u.b.Column("Done")].Cards {
		h.u.cardInfra[x.ID] = "Globex"
	}
	h.frame()
	h.clickScan("srvBack"+c.ID, image.Rect(boardAt.X, boardAt.Y, h.size.X, boardAt.Y+40))
	if h.u.view != "sess:"+c.ID {
		t.Fatalf("back to the card: view %q", h.u.view)
	}
	h.u.pickServer("Acme", pickRow{})
	if h.u.view != "srv:Acme" {
		t.Fatalf("the org's Claude: view %q", h.u.view)
	}
	h.u.serversClicked("Acme", time.Now())
	h.u.serversClicked("Acme", time.Now())
	h.frame()
	h.send(key.EditEvent{Text: "local"})
	h.send(press(key.NameReturn, 0))
	if h.u.pick != "" {
		t.Fatal("enter didn't pick the first match")
	}
	h.u.pick = "Acme"
	h.u.pickQ.SetText("zzzz")
	h.frame()
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: f32.Pt(5, 5)},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: f32.Pt(5, 5)})
	if h.u.pick != "" {
		t.Fatal("a press outside kept the picker")
	}
	if h.u.serversSentence("Initech") != "" {
		t.Fatal("an org without servers")
	}
	h.u.reloadConfig()
	h.frame()
}

// ---- team ----

// TestCoverTeam: a kind run twice, one that failed, the earlier chip both
// ways, a handoff's click, the canvas alone when narrow.
func TestCoverTeam(t *testing.T) {
	h := newHarness(t)
	c := teamCard(h)
	h.u.onTask(c.ID, &claude.Task{ID: "a3", ToolUse: "tu4", Name: "Parse again", Agent: "api", Status: "completed"})
	h.u.onTask(c.ID, &claude.Task{ID: "a4", ToolUse: "tu5", Name: "Migrate", Agent: "db", Status: "failed"})
	h.u.findTask(c.ID, "a2", "").end = time.Now().Add(-settled - time.Minute)
	if ms := h.u.members(c.ID); ms[0].runs != 2 {
		t.Fatalf("api runs %d", ms[0].runs)
	}
	sz := image.Pt(1400, 800)
	var act func()
	body := func(gtx layout.Context) layout.Dimensions { return h.u.canvasBody(gtx, c, &act) }
	all := image.Rectangle{Max: sz}
	h.clickIn(sz, all, body, "cvEarlier"+c.ID)
	if act(); !h.u.earlier[c.ID] {
		t.Fatal("earlier: shown")
	}
	h.clickIn(sz, all, body, "cvEarlier"+c.ID)
	if act(); h.u.earlier[c.ID] {
		t.Fatal("earlier: hidden again")
	}
	h.u.handoffsOn = true
	h.clickIn(sz, all, body, "teamHand1-a1") // after web's result, which ended long ago
	if act(); h.u.sub != "a1" {
		t.Fatalf("handoff: sub %q", h.u.sub)
	}
	act = nil
	h.draw(image.Pt(700, 600), body) // narrow: the canvas alone
	if act != nil || len(h.labels()) == 0 {
		t.Fatal("narrow: nothing drawn")
	}
	if s := leadStatus(&kanban.Card{}, nil); s != "Idle" {
		t.Errorf("lead %q", s)
	}
	if s := leadStatus(&kanban.Card{Status: kanban.StatusWaiting}, nil); s != statusName[kanban.StatusWaiting] {
		t.Errorf("lead %q", s)
	}
	if got := oneParagraph("First line\nsame paragraph.\n\nSecond."); got != "First line same paragraph." {
		t.Errorf("oneParagraph %q", got)
	}
}

// ---- markdown, rich text, copy ----

// TestCoverCodeCopy: a code block's Copy puts its code on the clipboard; a
// Claude reply's Copy its Markdown.
func TestCoverCodeCopy(t *testing.T) {
	h := newHarness(t)
	sz := image.Pt(600, 300)
	md := func(gtx layout.Context) layout.Dimensions { return h.u.markdown(gtx, "m", "```go\nx := 1\n```") }
	h.clickIn(sz, image.Rectangle{Max: sz}, md, "m-0copy")
	if _, b, ok := h.r.WriteClipboard(); !ok || string(b) != "x := 1" || h.u.toast != "Copied" || h.u.toastBad {
		t.Fatalf("clipboard %q, toast %q (bad %v)", b, h.u.toast, h.u.toastBad)
	}
	m := kanban.ChatMsg{Who: "claude", Text: "**done**"}
	tools := func(gtx layout.Context) layout.Dimensions { return h.u.msgTools(gtx, "r1", m) }
	h.clickIn(sz, image.Rectangle{Max: sz}, tools, "msgCopyr1")
	if _, b, ok := h.r.WriteClipboard(); !ok || string(b) != "**done**" {
		t.Fatalf("clipboard %q", b)
	}
}

// TestCoverMarkdownEdges: entities, a thematic break and a short table row parse and draw.
func TestCoverMarkdownEdges(t *testing.T) {
	h := newHarness(t)
	src := "a &amp; b &copy;\n\n---\n\n<div>raw</div>\n\n| a | b |\n|---|---|\n| 1 |\n"
	bs := parseMarkdown(src)
	if len(bs) == 0 {
		t.Fatal("nothing parsed")
	}
	if d := h.draw(image.Pt(600, 600), func(gtx layout.Context) layout.Dimensions { return h.u.markdown(gtx, "e", src) }); d.Size.Y == 0 {
		t.Fatal("nothing drawn")
	}
}

// TestCoverRichClicks: a single click on a link opens it; a double click
// past a word's end selects the word.
func TestCoverRichClicks(t *testing.T) {
	h := newHarness(t)
	opened := filepath.Join(t.TempDir(), "opened")
	fakeBin(t, infra.Opener, `echo "$1" >> `+opened)
	sp := span{SpanStyle: richtext.SpanStyle{Font: kit.Sans, Size: 14, Color: kit.CText, Content: "docs"}, url: "https://example.com/docs"}
	sz := image.Pt(600, 100)
	w := func(gtx layout.Context) layout.Dimensions { return h.u.rich(gtx, "l", sp) }
	h.draw(sz, w)
	st := h.u.rsels["l"]
	y := float32(st.lines[0].top + st.lines[0].h/2)
	var clock time.Duration // the events' own times: double clicks count by them
	at := func(k pointer.Kind, x float32) pointer.Event {
		clock += 10 * time.Millisecond
		e := pointer.Event{Kind: k, Source: pointer.Mouse, Position: f32.Pt(x, y), Time: clock}
		if k == pointer.Press {
			e.Buttons = pointer.ButtonPrimary
		}
		return e
	}
	h.r.Queue(at(pointer.Press, 3), at(pointer.Release, 3))
	h.draw(sz, w)
	h.pumpUntil("xdg-open", func() bool { b, _ := os.ReadFile(opened); return string(b) == sp.url+"\n" })
	clock += time.Second // not a double click of the link's click
	h.draw(sz, w)        // the router hit-tests against the last frame: this one, not the window's
	last := st.pieces[len(st.pieces)-1]
	n := len(last.xs)
	end := float32(last.x + (last.xs[n-2]+last.xs[n-1])/2 + 1) // the right half of the last letter: nearest the text's end
	h.r.Queue(at(pointer.Press, end), at(pointer.Release, end), at(pointer.Press, end), at(pointer.Release, end))
	h.draw(sz, w)
	if st.selected() != "docs" {
		t.Fatalf("double click past the end: %q, sel %v, src %q, at %v, clicks %d", st.selected(), st.sel, string(st.src), end, st.clicks.n)
	}
	ops := new(op.Ops)
	paintGlyphs(ops, h.u.k.Sh, 0, 0, kit.CText, nil)
	if len(opsBytes(ops)) != 0 {
		t.Fatal("no glyphs painted something")
	}
}

// opsBytes is what was drawn into o, serialized.
func opsBytes(o *op.Ops) []byte {
	return reflect.ValueOf(&o.Internal).Elem().FieldByName("data").Bytes()
}

// ---- shell ----

// TestCoverRunShell: the real runner runs bash (PowerShell on Windows) in
// the folder, output and exit code.
func TestCoverRunShell(t *testing.T) {
	dir := t.TempDir()
	var b bytes.Buffer
	cmd := "echo hi; pwd"
	if onWindows {
		if wine() {
			t.Skip("Wine's powershell.exe is a stub that runs nothing")
		}
		cmd = "echo hi; (Get-Location).Path" // pwd prints a table there
	}
	if err := realRunShell(context.Background(), dir, cmd, &b); err != nil || strings.ReplaceAll(b.String(), "\r\n", "\n") != "hi\n"+dir+"\n" {
		t.Fatalf("%q %v", b.String(), err)
	}
	var ee *exec.ExitError
	if err := realRunShell(context.Background(), dir, "exit 3", &b); !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Fatalf("%v", err)
	}
}

// TestCoverShellEnds: a gone card runs nothing; a second command waits for
// the first; an exit code and a runner error say so; a long output keeps
// its end; a command ending while the terminal is open waits for the
// user's turn; output that can't be kept says so.
func TestCoverShellEnds(t *testing.T) {
	h := newHarness(t)
	release := make(chan error)
	runShell = func(ctx context.Context, dir, cmd string, w io.Writer) error {
		w.Write([]byte(strings.Repeat("x", shellKept+10) + "END"))
		return <-release
	}
	defer func() { runShell = realRunShell }()
	h.u.shell("gone", "ls")
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	h.u.shell(c.ID, "make")
	h.u.shell(c.ID, "make again")
	if !strings.Contains(h.u.toast, "a command is still running") {
		t.Fatalf("toast %q", h.u.toast)
	}
	exit := prog("exit:4").Run()
	release <- exit
	h.pumpUntil("the end", func() bool { return h.u.shellStop[c.ID] == nil })
	ms := h.u.chat(c.ID)
	if m := ms[len(ms)-1]; !strings.HasPrefix(m.Detail, "exit 4") || !strings.HasPrefix(m.Text, "…") || !strings.HasSuffix(m.Text, "END") {
		t.Fatalf("detail %q, text starts %q", m.Detail, m.Text[:3])
	}
	startTerm = func(cmd *exec.Cmd, w, ht int, notify func()) (*term.Term, error) {
		return term.Start(prog("cat"), w, ht, notify)
	}
	defer func() { startTerm = term.Start }()
	h.u.shell(c.ID, "make")
	h.u.terminal(c.ID) // open meanwhile: Claude can't take the output now
	t.Cleanup(func() { h.u.closeTerm(c.ID) })
	os.WriteFile(filepath.Join(filepath.Dir(h.u.path), "chats"), nil, 0o644) // and the chat can't be kept
	os.RemoveAll(filepath.Join(filepath.Dir(h.u.path), "chats"))
	os.WriteFile(filepath.Join(filepath.Dir(h.u.path), "chats"), nil, 0o644)
	release <- errors.New("no bash")
	h.pumpUntil("the end", func() bool { return h.u.shellStop[c.ID] == nil })
	if card := h.u.b.Card(c.ID); card.Note != "Your command ended: no bash" || !strings.HasPrefix(h.u.toast, "Couldn't keep the command's output: ") {
		t.Fatalf("note %q, toast %q", card.Note, h.u.toast)
	}
	h.u.closeTerm(c.ID)
	h.u.shell(c.ID, "make")
	h.u.chats[c.ID] = nil // its entry gone meanwhile
	release <- nil
	h.pumpUntil("the end", func() bool { return h.u.shellStop[c.ID] == nil })
}

// ---- odds and ends ----

// TestCoverPastes: an image whose dialog closed before the clipboard
// answered is dropped; one that can't be kept says why.
func TestCoverPastes(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	clipImage = func() ([]byte, string, error) { <-release; return []byte("\x89PNG"), "image/png", nil }
	h.u.newCard(0)
	h.frame()
	h.frame()
	h.send(press("V", key.ModShortcut))
	h.u.edit = nil
	close(release)
	for end := time.Now().Add(200 * time.Millisecond); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		h.frame()
	}
	if ents, _ := os.ReadDir(h.u.imagesDir()); len(ents) != 0 {
		t.Fatal("an image kept for a closed dialog")
	}
	clipImage = func() ([]byte, string, error) { return []byte("\x89PNG"), "image/png", nil }
	os.WriteFile(h.u.imagesDir(), nil, 0o644)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	h.u.openPanel(c.ID)
	h.frame()
	h.frame()
	h.send(press("V", key.ModShortcut))
	h.pumpUntil("the toast", func() bool { return strings.HasPrefix(h.u.toast, "Couldn't keep the image: ") })
}

// TestCoverOdds: an archived card that never had a session; the history's
// finished subagents; a CI run's click opens it; an agent card whose
// desk.toml doesn't read.
func TestCoverOdds(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	h.u.b.ArchiveCard(c.ID)
	h.draw(image.Pt(600, 600), func(gtx layout.Context) layout.Dimensions { return h.u.chatPane(gtx, c.ID, 600, false) })
	d := h.u.b.Add(0, kanban.Card{Title: "y", Org: "Acme", Agent: "planner"})
	h.u.newTask(d.ID, claude.Task{ID: "a1", Status: "completed", Name: "Review"}).end = time.Now().Add(-time.Hour)
	var act func()
	h.draw(image.Pt(900, 600), func(gtx layout.Context) layout.Dimensions { return h.u.historyBody(gtx, d.ID, &act) })
	opened := filepath.Join(t.TempDir(), "opened")
	fakeBin(t, infra.Opener, `echo "$1" >> `+opened)
	sz := image.Pt(600, 100)
	row := h.u.ciRow("run1", "build", links.CIFailed, "https://example.com/run/1", 0)
	h.clickIn(sz, image.Rectangle{Max: sz}, row, "run1")
	h.pumpUntil("xdg-open", func() bool { b, _ := os.ReadFile(opened); return string(b) == "https://example.com/run/1\n" })
	h.orgToml("Acme", "[[jira]\n")
	if _, err := h.u.agentArgs(h.u.b.Card(d.ID)); err == nil {
		t.Fatal("a broken desk.toml read")
	}
}

// TestCoverMessages: each kind of chat entry draws: a command whose output
// waits for Claude, a tool with its description, an Edit's change and a
// Write's size, the infra Claude's note, an error.
func TestCoverMessages(t *testing.T) {
	h := newHarness(t)
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	for _, m := range []kanban.ChatMsg{
		{Who: "shell", Cmd: "ls", Text: "a\nb", Detail: "exit 0 · 0.1s", Path: toClaudeNext},
		{Who: "tool", Text: "Bash  ls -la", Detail: "List the folder"},
		{Who: "tool", Text: "Edit  main.go", Edits: []kanban.Edit{{Old: "a := 1", New: "a := 2"}}},
		{Who: "tool", Text: "Write  notes.md", Path: "notes.md", Detail: ""},
		{Who: "peer", Text: "Restarted web1."},
		{Who: "error", Text: "exit 1"},
	} {
		h.u.chats[c.ID] = append(h.u.chats[c.ID], m)
	}
	h.u.chats[c.ID][3].Edits = nil
	h.u.chats[c.ID][3].Text = "Write  notes.md (12 lines)"
	d := h.draw(image.Pt(480, 3000), func(gtx layout.Context) layout.Dimensions { return h.u.chatPane(gtx, c.ID, 480, false) })
	if d.Size.Y == 0 || h.u.chatList.Position.Count != 6 {
		t.Fatalf("drew %v, %d entries", d.Size, h.u.chatList.Position.Count)
	}
}
