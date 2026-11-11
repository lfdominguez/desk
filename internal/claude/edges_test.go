package claude

import (
	"context"
	"github.com/lfdominguez/desk/internal/testwait"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeClaude puts a `claude` on PATH that prints $FAKE_OUT, writes $FAKE_ERR
// to stderr, saves its args to $FAKE_ARGS and exits $FAKE_EXIT; bwrap too.
func fakeClaude(t *testing.T, bwrap bool) string {
	bin := t.TempDir()
	script := "#!/bin/sh\n[ -n \"$FAKE_ARGS\" ] && printf '%s' \"$*\" > \"$FAKE_ARGS\"\nprintf '%s\\n' \"$FAKE_OUT\"\nprintf '%s' \"$FAKE_ERR\" >&2\nexit ${FAKE_EXIT:-0}\n"
	writeFake(t, bin, "claude", script, "args")
	if bwrap {
		writeFake(t, bin, "bwrap", "#!/bin/sh\n", "")
	}
	t.Setenv("PATH", bin)
	t.Setenv("CLAUDE_PROFILE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	for _, k := range []string{"FAKE_OUT", "FAKE_ERR", "FAKE_ARGS", "FAKE_EXIT"} {
		t.Setenv(k, "")
	}
	return bin
}

// TestParseAgentUnclosed: frontmatter without its closing --- is no agent.
func TestParseAgentUnclosed(t *testing.T) {
	if _, ok := parseAgent("---\nname: x\n"); ok {
		t.Fatal("unclosed frontmatter read as an agent")
	}
}

// TestParseEdges: one-sided edits count, a top-level model is no subagent,
// an empty input falls through to the next telling one.
func TestParseEdges(t *testing.T) {
	e := Parse([]byte(`{"type":"assistant","message":{"model":"claude-opus-5-5","content":[` +
		`{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"/x/a.go","old_string":"","new_string":"package a"}},` +
		`{"type":"tool_use","id":"t2","name":"MultiEdit","input":{"file_path":"/x/b.go","edits":[{"old_string":"x","new_string":""},{"old_string":"","new_string":""}]}},` +
		`{"type":"tool_use","id":"t3","name":"Bash","input":{"command":"","description":"run"}}]}}`))
	if len(e) != 3 {
		t.Fatalf("a session's own model made a task: %+v", e)
	}
	if len(e[0].Edits) != 1 || e[0].Edits[0].New != "package a" || len(e[1].Edits) != 1 || e[1].Edits[0].Old != "x" {
		t.Fatalf("edits %+v %+v", e[0].Edits, e[1].Edits)
	}
	if e[2].Text != "Bash  run" {
		t.Fatalf("line %q", e[2].Text)
	}
}

// TestTail: stderr keeps only its last 2KB.
func TestTail(t *testing.T) {
	var s tail
	s.Write([]byte("short"))
	if s.String() != "short" {
		t.Fatalf("%q", s.String())
	}
	s.Write([]byte(strings.Repeat("a", 3000) + "end"))
	if got := s.String(); len(got) != 2048 || !strings.HasSuffix(got, "aend") {
		t.Fatalf("%d %q", len(got), got[len(got)-8:])
	}
}

// TestStart: events come from stdout, and the exit carries the stderr's end.
func TestStart(t *testing.T) {
	fakeClaude(t, false)
	t.Setenv("FAKE_OUT", `{"type":"system","subtype":"init","session_id":"s1"}`)
	t.Setenv("FAKE_ERR", "boom")
	t.Setenv("FAKE_EXIT", "3")
	got := make(chan Event, 10)
	p, err := Start(Options{Home: t.TempDir(), Dir: t.TempDir(), System: "sys"}, func(e Event) { got <- e })
	if err != nil || p == nil {
		t.Fatalf("%v %v", p, err)
	}
	if !slices.Contains(p.cmd.Env, "CLAUDE_CODE_ARTIFACT=1") {
		t.Fatal("a card's session should have the Artifact tools")
	}
	if e := <-got; e.Kind != KindInit || e.Session != "s1" {
		t.Fatalf("%+v", e)
	}
	select {
	case e := <-got:
		if e.Kind != KindExit || !strings.Contains(e.Text, "exit status 3: boom") {
			t.Fatalf("%+v", e)
		}
	case <-time.After(testwait.Long):
		t.Fatal("no exit")
	}

	t.Setenv("FAKE_EXIT", "0")
	p, _ = Start(Options{Home: t.TempDir(), Dir: t.TempDir()}, func(e Event) { got <- e })
	for e := range got {
		if e.Kind == KindExit {
			if e.Text != "" {
				t.Fatalf("a clean exit has no reason: %q", e.Text)
			}
			break
		}
	}
	if _, err := Start(Options{Home: t.TempDir(), Dir: "/nonexistent-dir"}, func(Event) {}); err == nil {
		t.Fatal("a missing folder should fail to start")
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := Start(Options{Home: t.TempDir()}, func(Event) {}); err == nil {
		t.Fatal("no claude should be an error")
	}
}

// TestStartArtifacts: only the Artifact tool's result publishes; a link in
// another tool's output (a grep over test fixtures) is not an artifact.
func TestStartArtifacts(t *testing.T) {
	fakeClaude(t, false)
	t.Setenv("FAKE_OUT", `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"b1","name":"Bash","input":{}},{"type":"tool_use","id":"a1","name":"Artifact","input":{}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"b1","content":"https://claude.ai/code/artifact/fake"},{"type":"tool_result","tool_use_id":"a1","content":"https://claude.ai/code/artifact/real"}]}}`)
	got := make(chan Event, 20)
	if _, err := Start(Options{Home: t.TempDir(), Dir: t.TempDir()}, func(e Event) { got <- e }); err != nil {
		t.Fatal(err)
	}
	var arts []string
	for e := range got {
		if e.Kind == KindArtifact {
			arts = append(arts, e.Text)
		}
		if e.Kind == KindExit {
			break
		}
	}
	if !slices.Equal(arts, []string{"https://claude.ai/code/artifact/real"}) {
		t.Fatalf("%q", arts)
	}
}

// TestAccountName: an org's profile names it, unless there is none or
// desk already runs inside one; the login's email when its json has one.
func TestAccountName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CLAUDE_PROFILE", "")
	claudes := filepath.Join(home, "Secure", "Claudes")
	os.MkdirAll(filepath.Join(claudes, "Globex"), 0o700)
	os.WriteFile(filepath.Join(claudes, "Initech"), nil, 0o600) // a file, not a profile
	os.WriteFile(filepath.Join(claudes, "claude-globex.json"), []byte(`{"oauthAccount":{"EmailAddress":"me@globex.com"}}`), 0o600)
	for _, c := range []struct{ org, want string }{
		{"Globex", "Globex account · me@globex.com"},
		{"", "Default account"},
		{"Acme", "Default account"},
		{"Initech", "Default account"},
	} {
		if got := AccountName(home, c.org); got != c.want {
			t.Errorf("%q: %q, want %q", c.org, got, c.want)
		}
	}
	t.Setenv("CLAUDE_PROFILE", "Globex")
	if got := AccountName(home, "Globex"); got != "Default account" {
		t.Errorf("inside a profile: %q", got)
	}
	t.Setenv("CLAUDE_PROFILE", "")
	for js, want := range map[string]string{
		`not json`: "Default account",
		`{"oauthAccount":{"EmailAddress":"me@globex.com","EmailAddress":5}}`: "Default account", // broken: no email
		`{"oauthAccount":{}}`:                                "Default account",
		`{"oauthAccount":{"EmailAddress":"me@example.com"}}`: "Default account · me@example.com",
	} {
		os.WriteFile(filepath.Join(home, ".claude.json"), []byte(js), 0o600)
		if got := AccountName(home, ""); got != want {
			t.Errorf("%s: %q, want %q", js, got, want)
		}
	}
}

// TestCommandPlain: no org, no profile folder, a file in its place or a
// profile already: plain claude.
func TestCommandPlain(t *testing.T) {
	home := t.TempDir()
	bin := fakeClaude(t, true)
	claudes := filepath.Join(home, "Secure", "Claudes")
	os.MkdirAll(filepath.Join(claudes, "Globex"), 0o700)
	os.WriteFile(filepath.Join(claudes, "Initech"), nil, 0o600)
	for _, org := range []string{"", "Acme", "Initech"} {
		if c, err := Command(home, org, "-p"); err != nil || c.Path != filepath.Join(bin, "claude"+exe) {
			t.Errorf("%q: %v %v", org, c, err)
		}
	}
	t.Setenv("CLAUDE_PROFILE", "Globex")
	if c, _ := Command(home, "Globex", "-p"); c.Path != filepath.Join(bin, "claude"+exe) {
		t.Errorf("nested profile: %s", c.Path)
	}
}

// TestCommandSeed: the profile's json starts as its own .claude.json, and
// the bwrap on PATH runs it.
func TestCommandSeed(t *testing.T) {
	home := t.TempDir()
	bin := fakeClaude(t, true)
	p := filepath.Join(home, "Secure", "Claudes", "Globex")
	os.MkdirAll(p, 0o700)
	os.WriteFile(filepath.Join(p, ".claude.json"), []byte(`{"seeded":1}`), 0o600)
	c, err := Command(home, "Globex", "-p")
	if err != nil || c.Path != filepath.Join(bin, "bwrap"+exe) {
		t.Fatalf("%v %v", c, err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "Secure", "Claudes", "claude-globex.json")); string(b) != `{"seeded":1}` {
		t.Fatalf("seed %q", b)
	}
	os.MkdirAll(filepath.Join(home, "Secure", "Claudes", "Acme"), 0o700)
	Command(home, "Acme", "-p")
	if b, _ := os.ReadFile(filepath.Join(home, "Secure", "Claudes", "claude-acme.json")); string(b) != "{}" {
		t.Fatalf("empty seed %q", b)
	}
}

// TestCommandStoreBwrap: without bwrap on PATH, the store's newest; none: an error.
func TestCommandStoreBwrap(t *testing.T) {
	home := t.TempDir()
	fakeClaude(t, false)
	os.MkdirAll(filepath.Join(home, "Secure", "Claudes", "Globex"), 0o700)
	c, err := Command(home, "Globex", "-p")
	m, _ := filepath.Glob("/nix/store/*-bubblewrap-*/bin/bwrap")
	if len(m) == 0 {
		if err == nil {
			t.Fatal("no bwrap anywhere should be an error")
		}
		return
	}
	if err != nil || c.Path != m[len(m)-1] {
		t.Fatalf("%v %v, want %s", c, err, m[len(m)-1])
	}
}

// TestSessionExistsProfile: a profile's transcripts are its own; without
// one (or inside one already) they are ~/.claude's.
func TestSessionExistsProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_PROFILE", "")
	if SessionExists(home, "", "") {
		t.Fatal("no id, no session")
	}
	claudes := filepath.Join(home, "Secure", "Claudes")
	os.MkdirAll(filepath.Join(claudes, "Globex", "projects", "-w"), 0o700)
	os.WriteFile(filepath.Join(claudes, "Globex", "projects", "-w", "g1.jsonl"), nil, 0o600)
	os.WriteFile(filepath.Join(claudes, "Initech"), nil, 0o600)
	os.MkdirAll(filepath.Join(home, ".claude", "projects", "-w"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", "projects", "-w", "d1.jsonl"), nil, 0o600)
	for _, c := range []struct {
		org, id string
		want    bool
	}{
		{"Globex", "g1", true},
		{"Globex", "d1", false},
		{"Acme", "d1", true},
		{"Initech", "d1", true},
		{"", "g1", false},
	} {
		if got := SessionExists(home, c.org, c.id); got != c.want {
			t.Errorf("%q %q: %v", c.org, c.id, got)
		}
	}
	t.Setenv("CLAUDE_PROFILE", "Globex")
	if !SessionExists(home, "Globex", "d1") {
		t.Error("inside a profile: ~/.claude's")
	}
}

// TestTickEdges: a backtick at either end gets spaces inside the fence.
func TestTickEdges(t *testing.T) {
	for in, want := range map[string]string{"`a": "`` `a ``", "a`": "`` a` ``", "a": "`a`"} {
		if got := tick(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// TestParseExplanationEmpty: no explanation, or one without a summary, is an error.
func TestParseExplanationEmpty(t *testing.T) {
	for _, s := range []string{`{}`, `{"structured_output":{"summary":""}}`} {
		if _, err := parseExplanation([]byte(s)); err == nil || err.Error() != "Haiku said nothing" {
			t.Errorf("%s: %v", s, err)
		}
	}
}

// TestExplain: the reply's explanation as markdown; a broken reply or a
// failed run is an error.
func TestExplain(t *testing.T) {
	fakeClaude(t, false)
	home := t.TempDir()
	t.Setenv("FAKE_OUT", `{"structured_output":{"summary":"Lists files."}}`)
	if md, err := Explain(context.Background(), home, "", t.TempDir(), "ls"); err != nil || md != "Lists files." {
		t.Fatalf("%q %v", md, err)
	}
	t.Setenv("FAKE_OUT", `garbage`)
	if _, err := Explain(context.Background(), home, "", t.TempDir(), "ls"); err == nil {
		t.Fatal("a broken reply should be an error")
	}
	t.Setenv("FAKE_OUT", ``)
	t.Setenv("FAKE_EXIT", "1")
	if _, err := Explain(context.Background(), home, "", t.TempDir(), "ls"); err == nil || !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("failed run: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := Explain(context.Background(), home, "", t.TempDir(), "ls"); err == nil {
		t.Fatal("no claude should be an error")
	}
}

// TestAuthFailed: Claude's login errors, whatever their wording, and not others.
// TestLang: the conversation in one language, what lands on the card in the other.
func TestLang(t *testing.T) {
	s := Lang("Spanish", "French")
	if !strings.Contains(s, "Speak with the user in French") || !strings.Contains(s, "DESK-CARD lines, commit messages and pull request text unless the repository says otherwise) in Spanish.") {
		t.Fatal(s)
	}
}

func TestAuthFailed(t *testing.T) {
	for msg, want := range map[string]bool{
		"Failed to authenticate: OAuth session expired":  true,
		"Invalid API key · Please run /login":            true,
		`API Error: 401 {"type":"authentication_error"}`: true,
		"Not logged in":                   true,
		"Prompt is too long":              false,
		"API Error: 529 overloaded_error": false,
	} {
		if AuthFailed(msg) != want {
			t.Errorf("AuthFailed(%q) = %v", msg, !want)
		}
	}
	fakeClaude(t, false)
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_ARGS", args)
	t.Setenv("DISPLAY", ":0")
	c, own, err := Login(t.TempDir(), "")
	if err != nil || own {
		t.Fatalf("default account: own %v, %v", own, err)
	}
	c.Run()
	if b, _ := os.ReadFile(args); string(b) != "auth login" || !slices.Contains(c.Env, "DISPLAY=:0") {
		t.Fatalf("login ran %q, env %v", b, c.Env)
	}
}

// TestLoginOwnAccount: an org's own account signs in with no way to the
// desktop, so no browser opens (it may be signed in to another account).
func TestLoginOwnAccount(t *testing.T) {
	if exe != "" {
		t.Skip("bwrap's sign-in is Linux's: Windows has NoBwrap's (TestNoBwrap)")
	}
	fakeClaude(t, true)
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "Secure", "Claudes", "Acme"), 0o755)
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	for _, kv := range [][2]string{{"DISPLAY", ":0"}, {"WAYLAND_DISPLAY", "wayland-1"}, {"WAYLAND_SOCKET", "3"}, {"DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus"}, {"BROWSER", "firefox"}} {
		t.Setenv(kv[0], kv[1])
	}
	c, own, err := Login(home, "Acme")
	if err != nil || !own || filepath.Base(c.Path) != "bwrap"+exe {
		t.Fatalf("own account: %v %v %v", c, own, err)
	}
	for _, kv := range c.Env {
		k, _, _ := strings.Cut(kv, "=")
		if slices.Contains([]string{"DISPLAY", "WAYLAND_DISPLAY", "WAYLAND_SOCKET", "DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR"}, k) || k == "BROWSER" && kv != "BROWSER=true" {
			t.Errorf("the sign-in keeps %s", kv)
		}
	}
	if !slices.Contains(c.Env, "BROWSER=true") {
		t.Error("no BROWSER=true")
	}
	a := strings.Join(c.Args, " ")
	if !strings.Contains(a, "--die-with-parent --tmpfs /tmp/.X11-unix --tmpfs /run/user/1000 -- ") || !strings.HasSuffix(a, " auth login") {
		t.Fatalf("args %s", a)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	c, _, _ = Login(home, "Acme")
	if a := strings.Join(c.Args, " "); !strings.Contains(a, "--tmpfs /tmp/.X11-unix -- ") {
		t.Fatalf("no runtime dir: %s", a)
	}
}

// TestLoginURL: the sign-in URL out of what `claude auth login` printed.
func TestLoginURL(t *testing.T) {
	for screen, want := range map[string]string{
		"Opening browser to sign in…\nIf the browser didn't open, visit: https://claude.com/cai/oauth/authorize?code=true&x=1   \nPaste code here if prompted >": "https://claude.com/cai/oauth/authorize?code=true&x=1",
		"visit: https://example.com/a\n": "https://example.com/a",
		"Opening browser to sign in…\n":  "",
	} {
		if got := LoginURL(screen); got != want {
			t.Errorf("LoginURL(%q) = %q, want %q", screen, got, want)
		}
	}
}

// TestOptimize: the request (and its linked items, when any) go in; the
// draft comes out; a failed run says its stderr.
func TestOptimize(t *testing.T) {
	fakeClaude(t, false)
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_ARGS", args)
	t.Setenv("FAKE_OUT", strings.Join([]string{
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"thinking"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/x/CLAUDE.md"}}]}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","name":"StructuredOutput"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"title\": \"Fix lo"}}}`,
		`not json`,
		`{"type":"result","structured_output":{"title":" Fix login "}}`,
	}, "\n"))
	a := Ask{Home: t.TempDir(), Org: "Acme", Today: "2026-10-30", Request: "fix login", Dir: t.TempDir()}
	var ps []Progress
	d, err := Optimize(context.Background(), a, func(p Progress) { ps = append(ps, p) })
	if err != nil || d.Title != "Fix login" {
		t.Fatalf("%+v %v", d, err)
	}
	if len(ps) != 4 || ps[0].Step != "Thinking" || ps[1].Step != "Reading CLAUDE.md" || ps[2].Step != "Writing" || ps[3].Draft.Title != "Fix lo" {
		t.Fatalf("progress: %+v", ps)
	}
	t.Setenv("FAKE_OUT", "{\"type\":\"system\"}\nnot json")
	if _, err := Optimize(context.Background(), a, nil); err == nil || err.Error() != "no draft in the reply" {
		t.Fatalf("no result: %v", err)
	}
	t.Setenv("FAKE_OUT", `{"type":"result","structured_output":{"title":"x"}}`)
	if b, _ := os.ReadFile(args); !strings.Contains(string(b), "Today: 2026-10-30 (Friday)") || strings.Contains(string(b), "Linked items, as") {
		t.Fatalf("in: %s", b)
	}
	if b, _ := os.ReadFile(args); strings.Contains(string(b), "Write the title and the prompt in") {
		t.Fatalf("a language unasked: %s", b)
	}
	if b, _ := os.ReadFile(args); !strings.Contains(string(b), "\nIts folders: ") || strings.Contains(string(b), "--add-dir") {
		t.Fatalf("no folder picked: %s", b)
	}
	root := t.TempDir()
	a2 := a
	a2.Root, a2.Dir, a2.Folder, a2.Folders = root, filepath.Join(root, "car_platform"), "car_platform", nil
	os.Mkdir(a2.Dir, 0o755)
	Optimize(context.Background(), a2, nil)
	if b, _ := os.ReadFile(args); !strings.Contains(string(b), "Folder: car_platform (picked by the user") || strings.Contains(string(b), "\nIts folders:") ||
		!strings.Contains(string(b), "--add-dir="+root+" ") {
		t.Fatalf("a folder picked: %s", b)
	}
	a.Linked, a.Lang = []string{"ABC-123: login broken"}, "Spanish"
	Optimize(context.Background(), a, nil)
	if b, _ := os.ReadFile(args); !strings.Contains(string(b), "Linked items, as") || !strings.Contains(string(b), "ABC-123: login broken") || !strings.Contains(string(b), "the prompt in Spanish") {
		t.Fatalf("linked: %s", b)
	}
	t.Setenv("FAKE_EXIT", "1")
	// how the CLI fails: exit 1, stderr empty, the reason in its result line
	t.Setenv("FAKE_OUT", `{"type":"result","subtype":"success","is_error":true,"result":"Prompt is too long"}`)
	if _, err := Optimize(context.Background(), a, nil); err == nil || err.Error() != "Prompt is too long" {
		t.Fatalf("failed run's result: %v", err)
	}
	t.Setenv("FAKE_OUT", `{"type":"result","subtype":"error_max_turns","is_error":true}`)
	if _, err := Optimize(context.Background(), a, nil); err == nil || err.Error() != "error_max_turns" {
		t.Fatalf("failed run's subtype: %v", err)
	}
	t.Setenv("FAKE_OUT", `{"type":"result","is_error":true}`)
	if _, err := Optimize(context.Background(), a, nil); err == nil || err.Error() != "Claude failed" {
		t.Fatalf("failed run, no reason: %v", err)
	}
	t.Setenv("FAKE_OUT", `{"type":"system"}`)
	if _, err := Optimize(context.Background(), a, nil); err == nil || err.Error() != "exit status 1: Claude said nothing" {
		t.Fatalf("failed run, no result, no stderr: %v", err)
	}
	t.Setenv("FAKE_ERR", "boom")
	if _, err := Optimize(context.Background(), a, nil); err == nil || err.Error() != "exit status 1: boom" {
		t.Fatalf("failed run's stderr: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := Optimize(context.Background(), a, nil); err == nil {
		t.Fatal("no claude should be an error")
	}
	if _, err := parseDraft([]byte(`{}`)); err == nil || err.Error() != "no draft in the reply" {
		t.Fatalf("no draft: %v", err)
	}
}

// TestOptLine: each tool's read says what it reads; text and unknown
// events show nothing.
func TestOptLine(t *testing.T) {
	var js strings.Builder
	for in, want := range map[string]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"pattern":"a/b"}}]}}`:       "Searching for a/b",
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Glob","input":{"pattern":"*.go"}}]}}`:      "Looking for *.go",
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Skill","input":{"skill":"x:y"}}]}}`:        "Loading skill x:y",
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{}}]}}`:                      "Using Read",
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"WebFetch","input":{"url":"u"}}]}}`:         "Using WebFetch",
		`{"type":"assistant","message":{"content":[{"type":"text"},{"type":"tool_use","name":"StructuredOutput"}]}}`:     "",
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"text"}}}`:                 "",
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","partial_json":""}}}`: "",
	} {
		if p, ok := optLine([]byte(in), &js); ok || p.Step != want {
			t.Errorf("%s: %q %v, want %q", in, p.Step, ok, want)
		}
	}
	js.WriteString("stale")
	optLine([]byte(`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","name":"StructuredOutput"}}}`), &js)
	if js.Len() != 0 {
		t.Fatalf("a new draft starts empty: %q", js.String())
	}
}

// TestPartialString: a value as far as it has arrived, a half escape
// held back, nothing before its key or for a non-string.
func TestPartialString(t *testing.T) {
	for _, c := range []struct{ js, want string }{
		{`{"title": "Fix lo`, "Fix lo"},
		{`{"title" : "done", "prompt": "a\"b`, "done"},
		{`{"title":"tab\`, "tab"},
		{`{"title":"caf\u00e`, "caf"},
		{`{"title":"café!"`, "café!"},
		{`{"title":"a\nb"}`, "a\nb"},
		{`{"title":`, ""},
		{`{"title": 3}`, ""},
		{`{"titl`, ""},
		{`{"title" "x"}`, ""},
		{`{"title":"\qwertyuiop`, ""}, // a bad escape no trimming mends
		{`{"title":"ab\q`, "ab"},
	} {
		if got := partialString(c.js, "title"); got != c.want {
			t.Errorf("%s: %q, want %q", c.js, got, c.want)
		}
	}
	if got := partialString(`{"title":"x \"prompt\" y","prompt":"go`, "prompt"); got != "go" {
		t.Fatalf("a key quoted inside a value: %q", got)
	}
}

// TestLimitsEdges: a login without a token is none, and only a weekly
// scoped limit takes its model's name.
func TestLimitsEdges(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"limits":[{"kind":"session","percent":1,"scope":{"model":{"display_name":"Fable"}}},
			{"kind":"weekly_scoped","percent":2,"scope":{}},{"kind":"weekly_scoped","percent":3,"scope":null}]}`))
	}))
	defer srv.Close()
	old := usageURL
	usageURL = srv.URL
	defer func() { usageURL = old }()
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	cred := filepath.Join(home, ".claude", ".credentials.json")
	os.WriteFile(cred, []byte(`{"claudeAiOauth":{}}`), 0o600)
	if _, err := Limits(context.Background(), home, ""); err == nil || !strings.HasPrefix(err.Error(), "no Claude login") {
		t.Fatalf("no token: %v", err)
	}
	os.WriteFile(cred, []byte(`{"claudeAiOauth":{"accessToken":"tok"}}`), 0o600)
	l, err := Limits(context.Background(), home, "")
	if err != nil || len(l) != 1 || l[0].Name != "5h" {
		t.Fatalf("%+v %v", l, err)
	}
}
