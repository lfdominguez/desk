package claude

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"hook_started","session_id":"s1"}`,
		`{"type":"system","subtype":"init","session_id":"s1"}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"Looking."},{"type":"tool_use","name":"Bash","input":{"command":"go   test ./...","description":"run"}}]}}`,
		`{"type":"rate_limit_event"}`,
		`{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.25,"session_id":"s1","usage":{"input_tokens":2,"cache_creation_input_tokens":100,"cache_read_input_tokens":50,"output_tokens":8},"permission_denials":[{"tool_name":"Write"}]}`,
		`not json`,
	}
	var got []Event
	for _, l := range lines {
		got = append(got, Parse([]byte(l))...)
	}
	if len(got) != 4 {
		t.Fatalf("%+v", got)
	}
	if got[0].Kind != KindInit || got[0].Session != "s1" || got[1].Text != "Looking." || got[2].Text != "Bash  go test ./..." {
		t.Fatalf("%+v", got[:3])
	}
	if r := got[3]; r.Kind != KindResult || r.Tokens != 160 || r.Cost != 0.25 || r.Error || !slices.Equal(r.Denied, []string{"Write"}) {
		t.Fatalf("result %+v", r)
	}
	e := Parse([]byte(`{"type":"result","is_error":true,"result":"API overloaded"}`))
	if !e[0].Error || e[0].Text != "API overloaded" {
		t.Fatalf("error result %+v", e)
	}
}

// TestParseUsage: the context comes from each call's usage, the window
// from the result, artifacts from any line that links one.
func TestParseUsage(t *testing.T) {
	e := Parse([]byte(`{"type":"assistant","message":{"usage":{"input_tokens":3,"cache_creation_input_tokens":1000,"cache_read_input_tokens":40000,"output_tokens":50},"content":[]}}`))
	if len(e) != 1 || e[0].Kind != KindUsage || e[0].Context != 41003 {
		t.Fatalf("%+v", e)
	}
	e = Parse([]byte(`{"type":"result","modelUsage":{"m":{"contextWindow":200000},"h":{"contextWindow":1000}}}`))
	if e[0].Window != 200000 {
		t.Fatalf("%+v", e)
	}
	e = Parse([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu9","content":"Published: https://claude.ai/code/artifact/0b1c-2d and https://claude.ai/code/artifact/0b1c-2d"}]}}`))
	if len(e) != 2 || e[0].Kind != KindToolDone || e[1].Kind != KindArtifact || e[1].Text != "https://claude.ai/code/artifact/0b1c-2d" || e[1].ToolID != "tu9" {
		t.Fatalf("%+v", e)
	}
	if e := Parse([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"see https://claude.ai/code/artifact/0b1c-2d"}]}}`)); len(e) != 1 || e[0].Kind != KindText {
		t.Fatalf("Claude's text publishes nothing: %+v", e)
	}
}

// TestParseCompact: a compaction's start (status compacting) and end
// (compact_boundary, with its sizes); other statuses are no news.
func TestParseCompact(t *testing.T) {
	if e := Parse([]byte(`{"type":"system","subtype":"status","status":"compacting"}`)); len(e) != 1 || e[0].Kind != KindCompact || e[0].Text != "" {
		t.Fatalf("start %+v", e)
	}
	if e := Parse([]byte(`{"type":"system","subtype":"status","status":null,"compact_result":"success"}`)); len(e) != 0 {
		t.Fatalf("status null %+v", e)
	}
	e := Parse([]byte(`{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"manual","pre_tokens":120000,"post_tokens":9000}}`))
	if len(e) != 1 || e[0].Kind != KindCompact || e[0].Text != "done" || e[0].Tokens != 120000 || e[0].Context != 9000 {
		t.Fatalf("end %+v", e)
	}
}

// TestParseShellOutWindows: a background shell's output file on Windows
// (backslashes, doubled in the JSON) comes whole; the sentence's period
// isn't part of it.
func TestParseShellOutWindows(t *testing.T) {
	e := Parse([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","content":"Command running in background with ID: b2. Output is being written to: C:\\Users\\me\\tasks\\b2.output."}]}}`))
	if len(e) != 2 || e[1].Task == nil || e[1].Task.ID != "b2" || e[1].Task.Output != `C:\Users\me\tasks\b2.output` {
		t.Fatalf("%+v", e)
	}
}

// TestParseTasks: subagents and background shells, as Claude Code streams
// them; a subagent's messages carry its Agent tool use as their parent.
func TestParseTasks(t *testing.T) {
	var got []Event
	for _, l := range []string{
		`{"type":"system","subtype":"task_started","task_id":"b1","tool_use_id":"tu1","description":"Sleep then echo","is_backgrounded":true,"task_type":"local_bash"}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","content":"Command running in background with ID: b1. Output is being written to: /tmp/x/tasks/b1.output"}]}}`,
		`{"type":"system","subtype":"task_started","task_id":"a1","tool_use_id":"tu2","description":"Echo hi","subagent_type":"general-purpose","task_type":"local_agent","prompt":"Run echo"}`,
		`{"type":"assistant","parent_tool_use_id":"tu2","message":{"model":"claude-opus-5-5","usage":{"input_tokens":9},"content":[{"type":"tool_use","id":"tu3","name":"Bash","input":{"command":"echo hi"}}]}}`,
		`{"type":"system","subtype":"task_progress","task_id":"a1","description":"Running Echo","usage":{"total_tokens":25796,"tool_uses":1}}`,
		`{"type":"system","subtype":"task_updated","task_id":"a1","patch":{"status":"completed"}}`,
		`{"type":"system","subtype":"task_notification","task_id":"b1","status":"completed","output_file":"/tmp/x/tasks/b1.output","summary":"Background command completed"}`,
	} {
		got = append(got, Parse([]byte(l))...)
	}
	if d := got[1]; d.Kind != KindToolDone || d.ToolID != "tu1" {
		t.Fatalf("tool done %+v", d)
	}
	got = append(got[:1], got[2:]...)
	if m := got[4].Task; m == nil || m.ToolUse != "tu2" || m.Model != "claude-opus-5-5" {
		t.Fatalf("model %+v", got[4])
	}
	got = append(got[:4], got[5:]...)
	if len(got) != 7 {
		t.Fatalf("%d events: %+v", len(got), got)
	}
	if s := got[0].Task; !s.Shell || s.Status != "running" || s.ToolUse != "tu1" || got[1].Task.ID != "b1" || got[1].Task.Output != "/tmp/x/tasks/b1.output" {
		t.Fatalf("shell %+v %+v", s, got[1].Task)
	}
	if a := got[2].Task; a.Shell || a.Agent != "general-purpose" || a.Prompt != "Run echo" {
		t.Fatalf("agent %+v", a)
	}
	if e := got[3]; e.Kind != KindTool || e.Parent != "tu2" || e.ToolID != "tu3" { // and no usage: the subagent's context isn't the session's
		t.Fatalf("subagent tool %+v", e)
	}
	if p := got[4].Task; p.Note != "Running Echo" || p.Tokens != 25796 || p.Tools != 1 || got[5].Task.Status != "completed" || got[6].Task.Summary == "" {
		t.Fatalf("progress %+v %+v %+v", p, got[5].Task, got[6].Task)
	}
}

func TestParseBash(t *testing.T) {
	e := Parse([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"go test ./...","description":"Run every test"}}]}}`))
	if len(e) != 1 || e[0].Cmd != "go test ./..." || e[0].Detail != "Run every test" {
		t.Fatalf("%+v", e)
	}
	if !strings.Contains(System("DESK-1", "x", ""), "description that explains") {
		t.Fatal("the system prompt no longer asks for explained commands")
	}
}

func TestParseFileTools(t *testing.T) {
	e := Parse([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"/x/a.go","old_string":"a := 1","new_string":"a := 2"}},` +
		`{"type":"tool_use","id":"t2","name":"MultiEdit","input":{"file_path":"/x/b.go","edits":[{"old_string":"x","new_string":"y"},{"old_string":"p","new_string":"q"}]}},` +
		`{"type":"tool_use","id":"t3","name":"Write","input":{"file_path":"/x/c.go","content":"one\ntwo\nthree\n"}}]}}`))
	if len(e) != 3 || e[0].Path != "/x/a.go" || len(e[0].Edits) != 1 || e[0].Edits[0].New != "a := 2" || len(e[1].Edits) != 2 || e[1].Edits[1].Old != "p" {
		t.Fatalf("edits %+v", e)
	}
	if w := e[2]; w.Path != "/x/c.go" || w.Lines != 3 || w.Bytes != 14 || len(w.Edits) != 0 {
		t.Fatalf("write %+v", w)
	}
}

func TestExplanation(t *testing.T) {
	e, err := parseExplanation([]byte(`{"is_error":false,"structured_output":{"summary":"Runs the checks.","steps":[` +
		`{"code":"pnpm lint 2>&1 | tail -3","what":"Lints, showing the end.","parts":[{"code":"2>&1","what":"merges stderr"},{"code":"| tail -3","what":"keeps 3 lines"}]},` +
		`{"code":"echo EXIT=\\$?","what":"Prints an exit code.","parts":[]}],"warnings":["` + "`$?`" + ` is tail's status."],"effects":"Only reads."}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "Runs the checks.\n\n1. `pnpm lint 2>&1 | tail -3` — Lints, showing the end.\n   - `2>&1` — merges stderr\n   - `| tail -3` — keeps 3 lines\n" +
		"2. `echo EXIT=\\$?` — Prints an exit code.\n\n> [!WARNING]\n> `$?` is tail's status.\n\n**Effects:** Only reads."
	if got := e.Markdown(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if tick("a`b") != "``a`b``" {
		t.Fatalf("tick: %s", tick("a`b"))
	}
	if _, err := parseExplanation([]byte(`{"is_error":true,"result":"overloaded"}`)); err == nil || err.Error() != "overloaded" {
		t.Fatalf("error: %v", err)
	}
}

// TestAddRoot: a session in a folder inside its organization adds the
// organization's folder, whose .claude/skills Claude Code wouldn't load.
func TestAddRoot(t *testing.T) {
	if a := addRoot("/w/Org/app", "/w/Org"); !slices.Equal(a, []string{"--add-dir=/w/Org"}) {
		t.Fatalf("subfolder: %q", a)
	}
	if a := addRoot("/w/Org/", "/w/Org"); a != nil {
		t.Fatalf("the root itself: %q", a)
	}
	bin := t.TempDir()
	writeFake(t, bin, "claude", "#!/bin/sh\n", "")
	t.Setenv("PATH", bin)
	c, err := Interactive(t.TempDir(), "", "/w/Org/app", "/w/Org", "id1", "sys")
	if err != nil || !strings.Contains(strings.Join(c.Args, " "), "--add-dir=/w/Org") {
		t.Fatalf("%v %q", err, c.Args)
	}
}

func TestTake(t *testing.T) {
	clean, d := Take("Opened the PR.\n\nDESK-LINK: https://github.com/a/b/pull/3\n**DESK-STATUS: blocked — waiting for PR #3 review**")
	if clean != "Opened the PR." || d.Status != "blocked" || d.Note != "waiting for PR #3 review" || d.Links[0] != "https://github.com/a/b/pull/3" {
		t.Fatalf("%q %+v", clean, d)
	}
	if clean, d := Take("Leaving it stopped.\nDESK-SNOOZE: stopped by hand on purpose"); clean != "Leaving it stopped." || d.Snooze != "stopped by hand on purpose" {
		t.Errorf("snooze: %q %+v", clean, d)
	}
	if _, d := Take("All set.\nDESK-STATUS: done"); d.Status != "done" || d.Note != "" {
		t.Fatalf("%+v", d)
	}
	if clean, d := Take("plain answer"); clean != "plain answer" || d.Status != "" {
		t.Fatalf("%q %+v", clean, d)
	}
}

// TestCommandProfile: an org with a profile folder runs under bwrap with
// it bound over ~/.claude; one without runs plain claude.
func TestCommandProfile(t *testing.T) {
	home, _ := filepath.EvalSymlinks(t.TempDir()) // macOS's is under /var, a link to /private/var
	os.MkdirAll(filepath.Join(home, "Secure", "Claudes", "Globex"), 0o755)
	bin := t.TempDir()
	for _, n := range []string{"claude", "bwrap"} {
		writeFake(t, bin, n, "#!/bin/sh\n", "")
	}
	t.Setenv("PATH", bin)
	t.Setenv("CLAUDE_PROFILE", "")
	c, err := Command(home, "Globex", "-p")
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(c.Args, " ")
	if !strings.HasSuffix(c.Path, "bwrap"+exe) || !strings.Contains(args, "--bind "+filepath.Join(home, "Secure", "Claudes", "Globex")+" "+filepath.Join(home, ".claude")) || !strings.HasSuffix(args, "claude"+exe+" -p") {
		t.Fatalf("%s", args)
	}
	if _, err := os.Stat(filepath.Join(home, "Secure", "Claudes", "claude-globex.json")); err != nil {
		t.Fatal("profile json not seeded")
	}
	if strings.Contains(args, ".ssh-config-0") {
		t.Fatalf("a plain ssh config needs no copy: %s", args)
	}
	// home-manager's ssh config is a symlink into the store: the sandbox gets a copy
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(home, "proxy.conf"), []byte("ProxyCommand none"), 0o444)
	os.WriteFile(filepath.Join(home, "store-config"), []byte("Host *\nInclude "+filepath.Join(home, "*.conf")+"\n"), 0o444)
	if c, _ := Command(home, "Initech", "-p"); !strings.HasSuffix(c.Path, "claude"+exe) {
		t.Fatalf("no profile should be plain claude: %s", c.Path)
	}
	if err := os.Symlink(filepath.Join(home, "store-config"), filepath.Join(home, ".ssh", "config")); err != nil {
		t.Skip("no symlinks here (Windows without Developer Mode, Wine):", err)
	}
	c, _ = Command(home, "Globex", "-p")
	cp := filepath.Join(home, "Secure", "Claudes", "Globex", ".ssh-config-0")
	if b, _ := os.ReadFile(cp); string(b) != "Host *\nProxyCommand none\n" || !strings.Contains(strings.Join(c.Args, " "), "--ro-bind "+cp+" "+filepath.Join(home, "store-config")) {
		t.Fatalf("ssh config not copied: %s", c.Args)
	}
}

// TestCommandEnv: a desk started from a Claude Code session doesn't pass
// that session's markers on (its children would keep no transcript).
func TestCommandEnv(t *testing.T) {
	bin := t.TempDir()
	writeFake(t, bin, "claude", "#!/bin/sh\n", "")
	t.Setenv("PATH", bin)
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
	t.Setenv("DESK_KEEP", "yes")
	c, err := Command(t.TempDir(), "", "-p")
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(c.Env, func(kv string) bool { return strings.HasPrefix(kv, "CLAUDE_CODE_CHILD_SESSION=") }) || !slices.Contains(c.Env, "DESK_KEEP=yes") {
		t.Fatalf("env %v", c.Env)
	}
}

func TestSessionID(t *testing.T) {
	home := t.TempDir()
	id := NewSessionID()
	if len(id) != 36 || id[14] != '4' || id == NewSessionID() {
		t.Fatalf("id %q", id)
	}
	if SessionExists(home, "", id) {
		t.Fatal("exists before its transcript")
	}
	os.MkdirAll(filepath.Join(home, ".claude", "projects", "-w"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", "projects", "-w", id+".jsonl"), nil, 0o600)
	if !SessionExists(home, "", id) {
		t.Fatal("transcript not found")
	}
}
