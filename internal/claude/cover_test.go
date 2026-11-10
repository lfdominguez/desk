package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAnsweredNoInput: answers go in even when the input was none.
func TestAnsweredNoInput(t *testing.T) {
	if got := string(Answered(nil, map[string]string{"q": "a"})); got != `{"answers":{"q":"a"}}` {
		t.Fatalf("%s", got)
	}
}

// TestParseMore: other control requests, id-less tasks and non-result user
// content are none; SendMessage names its addressee; an empty Write is 0 lines;
// a long input is cut to 90 runes.
func TestParseMore(t *testing.T) {
	if e := Parse([]byte(`{"type":"control_request","request":{"subtype":"interrupt"}}`)); len(e) != 0 {
		t.Fatalf("interrupt: %+v", e)
	}
	if e := Parse([]byte(`{"type":"system","subtype":"task_started"}`)); len(e) != 0 {
		t.Fatalf("no id: %+v", e)
	}
	e := Parse([]byte(`{"type":"user","message":{"content":[{"type":"text","text":"hi"},{"type":"tool_result","tool_use_id":"t9"}]}}`))
	if len(e) != 1 || e[0].Kind != KindToolDone || e[0].ToolID != "t9" {
		t.Fatalf("user: %+v", e)
	}
	long := strings.Repeat("x", 100)
	e = Parse([]byte(`{"type":"assistant","message":{"content":[` +
		`{"type":"tool_use","id":"s","name":"SendMessage","input":{"to":"a1"}},` +
		`{"type":"tool_use","id":"w","name":"Write","input":{"file_path":"/x/e","content":""}},` +
		`{"type":"tool_use","id":"b","name":"Bash","input":{"command":"` + long + `"}}]}}`))
	if len(e) != 3 || e[0].To != "a1" || e[1].Lines != 0 || e[1].Bytes != 0 || e[1].Path != "/x/e" {
		t.Fatalf("%+v", e)
	}
	if want := "Bash  " + strings.Repeat("x", 89) + "…"; e[2].Text != want {
		t.Fatalf("long: %q", e[2].Text)
	}
}

// TestStartSession: --resume beats --session-id; a new id alone goes as --session-id.
func TestStartSession(t *testing.T) {
	fakeClaude(t, false)
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_ARGS", args)
	for _, c := range []struct {
		o    Options
		want string
		not  string
	}{
		{Options{Resume: "r1", New: "n1"}, "--resume r1", "--session-id"},
		{Options{New: "n1"}, "--session-id n1", "--resume"},
	} {
		got := make(chan Event, 10)
		c.o.Home, c.o.Dir = t.TempDir(), t.TempDir()
		if _, err := Start(c.o, func(e Event) { got <- e }); err != nil {
			t.Fatal(err)
		}
		for e := range got {
			if e.Kind == KindExit {
				break
			}
		}
		b, _ := os.ReadFile(args)
		if !strings.Contains(string(b), c.want) || strings.Contains(string(b), c.not) {
			t.Errorf("%+v: %s", c.o, b)
		}
	}
}

// TestProcWrites: Send, Respond (both ways) and Interrupt reach stdin as
// stream-json lines; after Close a write fails and the process ends.
func TestProcWrites(t *testing.T) {
	bin := fakeClaude(t, false)
	in := filepath.Join(t.TempDir(), "in")
	writeFake(t, bin, "claude", "#!/bin/sh\nwhile IFS= read -r l; do printf '%s\\n' \"$l\" >> \"$FAKE_IN\"; done\n", "stdin")
	t.Setenv("FAKE_IN", in)
	got := make(chan Event, 10)
	p, err := Start(Options{Home: t.TempDir(), Dir: t.TempDir()}, func(e Event) { got <- e })
	if err != nil {
		t.Fatal(err)
	}
	if p.Send("hello") != nil || p.Respond("r1", true, json.RawMessage(`{"a":1}`), "") != nil ||
		p.Respond("r2", false, nil, "no") != nil || p.Interrupt() != nil {
		t.Fatal("write failed")
	}
	var lines []map[string]any
	for deadline := time.Now().Add(10 * time.Second); len(lines) < 4 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		b, _ := os.ReadFile(in)
		lines = nil
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var m map[string]any
			if json.Unmarshal([]byte(l), &m) == nil {
				lines = append(lines, m)
			}
		}
	}
	if len(lines) != 4 {
		t.Fatalf("%d lines", len(lines))
	}
	if m := lines[0]["message"].(map[string]any); lines[0]["type"] != "user" || m["content"] != "hello" {
		t.Errorf("send %v", lines[0])
	}
	resp := func(i int) map[string]any {
		return lines[i]["response"].(map[string]any)["response"].(map[string]any)
	}
	if r := resp(1); r["behavior"] != "allow" || r["updatedInput"].(map[string]any)["a"] != 1.0 {
		t.Errorf("allow %v", r)
	}
	if r := resp(2); r["behavior"] != "deny" || r["message"] != "no" || lines[2]["response"].(map[string]any)["request_id"] != "r2" {
		t.Errorf("deny %v", lines[2])
	}
	if lines[3]["type"] != "control_request" || lines[3]["request"].(map[string]any)["subtype"] != "interrupt" {
		t.Errorf("interrupt %v", lines[3])
	}
	p.Close()
	if p.Send("again") == nil {
		t.Error("a write after Close should fail")
	}
	select {
	case e := <-got:
		if e.Kind != KindExit {
			t.Errorf("%+v", e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no exit after Close")
	}
}

// TestAccountNameConfigDir: $CLAUDE_CONFIG_DIR's .claude.json names the default login.
func TestAccountNameConfigDir(t *testing.T) {
	d := t.TempDir()
	t.Setenv("CLAUDE_PROFILE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", d)
	os.WriteFile(filepath.Join(d, ".claude.json"), []byte(`{"oauthAccount":{"EmailAddress":"me@example.com"}}`), 0o600)
	if got := AccountName(t.TempDir(), ""); got != "Default account · me@example.com" {
		t.Fatalf("%q", got)
	}
}

// TestCommandSeedFails: a profile whose json can't be seeded is an error.
func TestCommandSeedFails(t *testing.T) {
	if exe != "" {
		t.Skip("Windows folders have no mode bits to make them read-only")
	}
	home := t.TempDir()
	fakeClaude(t, true)
	claudes := filepath.Join(home, "Secure", "Claudes")
	os.MkdirAll(filepath.Join(claudes, "Globex"), 0o700)
	os.Chmod(claudes, 0o500)
	t.Cleanup(func() { os.Chmod(claudes, 0o700) })
	if _, err := Command(home, "Globex", "-p"); err == nil {
		t.Fatal("an unwritable seed should fail")
	}
}

// TestCommandNoBwrap: no bwrap on PATH nor in the store is an error.
func TestCommandNoBwrap(t *testing.T) {
	home := t.TempDir()
	fakeClaude(t, false)
	old := storeBwrap
	storeBwrap = filepath.Join(t.TempDir(), "*-bubblewrap-*", "bin", "bwrap")
	defer func() { storeBwrap = old }()
	os.MkdirAll(filepath.Join(home, "Secure", "Claudes", "Globex"), 0o700)
	if _, err := Command(home, "Globex", "-p"); err == nil || err.Error() != "profile Globex needs bwrap" {
		t.Fatalf("%v", err)
	}
}

// TestSSHConfig: a symlinked config is copied with its ~/ and relative
// Includes inlined; a dangling link or one to a folder is an error.
func TestSSHConfig(t *testing.T) {
	home, _ := filepath.EvalSymlinks(t.TempDir()) // macOS's is under /var, a link to /private/var
	ssh := filepath.Join(home, ".ssh")
	os.MkdirAll(filepath.Join(home, "store"), 0o700)
	os.MkdirAll(ssh, 0o700)
	os.WriteFile(filepath.Join(home, "store", "config"), []byte("Host a\nInclude ~/.ssh/extra rel\n"), 0o600)
	os.WriteFile(filepath.Join(ssh, "extra"), []byte("Host b"), 0o600)
	os.WriteFile(filepath.Join(ssh, "rel"), []byte("Host c"), 0o600)
	src := filepath.Join(ssh, "config")
	os.Symlink(filepath.Join(home, "store", "config"), src)
	dst := filepath.Join(t.TempDir(), "copy")
	real, dup, err := sshConfig(src, dst)
	if err != nil || real != filepath.Join(home, "store", "config") || dup != dst {
		t.Fatalf("%q %q %v", real, dup, err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "Host a\nHost b\nHost c\n" {
		t.Fatalf("%q", b)
	}
	os.Remove(src)
	os.Symlink(filepath.Join(home, "gone"), src)
	if _, _, err := sshConfig(src, dst); err == nil {
		t.Error("dangling link")
	}
	os.Remove(src)
	os.Symlink(filepath.Join(home, "store"), src)
	if _, _, err := sshConfig(src, dst); err == nil {
		t.Error("link to a folder")
	}
}

// TestInteractiveResume: an existing session resumes; no claude is an error.
func TestInteractiveResume(t *testing.T) {
	home := t.TempDir()
	fakeClaude(t, false)
	os.MkdirAll(filepath.Join(home, ".claude", "projects", "-w"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", "projects", "-w", "id1.jsonl"), nil, 0o600)
	c, err := Interactive(home, "", "/w", "", "id1", "sys")
	if err != nil || !strings.Contains(strings.Join(c.Args, " "), "--resume id1") || strings.Contains(strings.Join(c.Args, " "), "--session-id") {
		t.Fatalf("%v %v", c, err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := Interactive(home, "", "/w", "", "id1", "sys"); err == nil {
		t.Fatal("no claude should be an error")
	}
}

// TestDirectivesMore: ServerWork names the key and org; System the org;
// Take reads tickets and cards, skipping an empty card.
func TestDirectivesMore(t *testing.T) {
	if s := ServerWork("ABC-123", "Acme"); !strings.Contains(s, "--from ABC-123") || !strings.Contains(s, "Acme organization's servers") {
		t.Fatalf("%s", s)
	}
	if s := System("DESK-1", "t", "Acme"); !strings.Contains(s, "board for the Acme organization") {
		t.Fatalf("%s", s)
	}
	if s := Workspace("DESK-7", "Fix it"); !strings.Contains(s, `workspace Claude of card DESK-7 ("Fix it")`) || !strings.Contains(s, "suggest_to_card") || !strings.HasSuffix(s, DrawingTools) {
		t.Fatalf("%s", s)
	}
	if !strings.HasSuffix(CardTools, DrawingTools) {
		t.Fatal("a card's Claude draws too")
	}
	if s := Drawing("Flow", "Acme", "", []string{"/a"}); !strings.Contains(s, `drawing "Flow" (of organization Acme)`) || !strings.HasSuffix(s, DrawingTools) {
		t.Fatalf("%s", s)
	}
	if s := Drawing("Flow", "Acme", "DESK-3", []string{"/a/b", "/c"}); !strings.Contains(s, `drawing "Flow" (of card DESK-3 of organization Acme)`) || !strings.Contains(s, "these folders: /a/b, /c.") {
		t.Fatalf("%s", s)
	}
	clean, d := Take("hi\nDESK-TICKET: Fix login\nDESK-CARD: write docs\nDESK-CARD:\n")
	if clean != "hi" || d.Ticket != "Fix login" || len(d.Cards) != 1 || d.Cards[0] != "write docs" {
		t.Fatalf("%q %+v", clean, d)
	}
}

// TestOptimizeMore: a cancelled run says so; broken json is an error; a
// bad date has no weekday.
func TestOptimizeMore(t *testing.T) {
	fakeClaude(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Optimize(ctx, Ask{Home: t.TempDir(), Dir: t.TempDir()}, nil); err != context.Canceled {
		t.Fatalf("%v", err)
	}
	if _, err := parseDraft([]byte(`nope`)); err == nil {
		t.Fatal("broken json")
	}
	if weekday("soon") != "" || weekday("2026-10-30") != "Friday" {
		t.Fatal("weekday")
	}
}

// TestOptimizeCancelledMidRun: a run cancelled while Claude works says it
// was cancelled, not that the killed process failed.
func TestOptimizeCancelledMidRun(t *testing.T) {
	bin := fakeClaude(t, false)
	writeFake(t, bin, "claude", "#!/bin/sh\nwhile :; do :; done\n", "spin")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := Optimize(ctx, Ask{Home: t.TempDir(), Dir: t.TempDir()}, nil); err != context.DeadlineExceeded {
		t.Fatalf("%v", err)
	}
}

// TestLimitsErrors: an unreachable endpoint, a non-200 and a broken body are errors.
func TestLimitsErrors(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"tok"}}`), 0o600)
	old := usageURL
	defer func() { usageURL = old }()
	body, code := "", 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
		w.Write([]byte(body))
	}))
	usageURL = srv.URL
	code = 500
	if _, err := Limits(context.Background(), home, ""); err == nil || !strings.HasPrefix(err.Error(), "usage: 500") {
		t.Errorf("500: %v", err)
	}
	code, body = 200, "nope"
	if _, err := Limits(context.Background(), home, ""); err == nil {
		t.Error("broken body")
	}
	srv.Close()
	if _, err := Limits(context.Background(), home, ""); err == nil {
		t.Error("closed server")
	}
}

// TestAgentsUnreadable: a *.md that can't be read is skipped.
func TestAgentsUnreadable(t *testing.T) {
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "dir.md"), 0o700)
	os.WriteFile(filepath.Join(d, "a.md"), []byte("---\nname: a\n---\nhi"), 0o600)
	if as := Agents(d); len(as) != 1 || as[0].Name != "a" {
		t.Fatalf("%+v", as)
	}
}
