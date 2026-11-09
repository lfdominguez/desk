package infra

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"

	"github.com/lfdominguez/desk/internal/claude"
)

// --- helpers (suffix A: this file's) ---

// fakeTermA is a Term with no program: an idle prompt, its pty a pipe the
// test reads from r.
func fakeTermA(t *testing.T, host string) (*Term, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	return &Term{Host: host, emu: vt.NewEmulator(40, 6), pty: pipeProc{w}, b: &pos{}}, r
}

// pipeProc is a Term's program as a pipe: what the Term writes goes to it.
type pipeProc struct{ *os.File }

func (pipeProc) Resize(w, h int)   {}
func (p pipeProc) Hangup()         { p.File.Close() }
func (pipeProc) Wait() (int, bool) { return -1, false }

func readPipeA(r *os.File) string {
	r.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	return string(buf[:n])
}

func doneProcA(t *testing.T) *os.Process {
	cmd := exec.Command(os.Args[0], "-test.run=^$") // this test binary, running nothing: on any OS
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process
}

// recA is a Claude's stdin: it keeps what is written and whether it closed.
type recA struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	closed bool
	fail   bool
}

func (c *recA) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		return 0, errors.New("broken pipe")
	}
	return c.buf.Write(p)
}
func (c *recA) Close() error { c.mu.Lock(); c.closed = true; c.mu.Unlock(); return nil }
func (c *recA) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func claudeA(t *testing.T) (*Claude, *recA) {
	r := &recA{}
	return &Claude{stdin: r, proc: doneProcA(t)}, r
}

func setDispatchA(t *testing.T, f func(tea.Msg)) {
	old := dispatch
	dispatch = f
	t.Cleanup(func() { dispatch = old })
}

func runtimeA(t *testing.T) string {
	d, err := os.MkdirTemp("", "rt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	t.Setenv("XDG_RUNTIME_DIR", d)
	return d
}

// fakeClaudeBinA puts a `claude` running body first on PATH, with HOME and
// DESK_ROOT in temp dirs (no profile); it writes its arguments to args.
func fakeClaudeBinA(t *testing.T, body string) (args string) {
	needSh(t)
	bin, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows' home
	t.Setenv("DESK_ROOT", filepath.Join(home, "Work"))
	t.Setenv("CLAUDE_PROFILE", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	args = filepath.Join(bin, "args")
	p := filepath.Join(bin, "claude")
	write(t, p, "#!/bin/sh\nprintf '%s\\n' \"$@\" > "+args+"\n"+body)
	os.Chmod(p, 0o755)
	return args
}

func keyA(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	if k, ok := strings.CutPrefix(s, "ctrl+"); ok {
		return tea.KeyPressMsg{Code: rune(k[0]), Mod: tea.ModCtrl}
	}
	if k, ok := strings.CutPrefix(s, "alt+"); ok {
		return tea.KeyPressMsg{Code: rune(k[0]), Mod: tea.ModAlt}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

func lastChatA(t *Tab) string {
	if len(t.chat) == 0 {
		return ""
	}
	return t.chat[len(t.chat)-1].text
}

func captureA(t *testing.T, f func() int) (int, string, string) {
	t.Helper()
	or, ow, _ := os.Pipe()
	er, ew, _ := os.Pipe()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = ow, ew
	code := f()
	os.Stdout, os.Stderr = oldOut, oldErr
	ow.Close()
	ew.Close()
	o, _ := io.ReadAll(or)
	e, _ := io.ReadAll(er)
	return code, string(o), string(e)
}

// --- claude.go ---

func TestToolLineA(t *testing.T) {
	for _, c := range []struct {
		name, effect, cmd, keys, host string
		pane                          int
		want                          string
	}{
		{"mcp__desk__run", "read", "uptime", "", "", 2, "read · pane 2 · uptime"},
		{"mcp__desk__run", "write", "rm x", "", "", 0, "write · rm x"},
		{"mcp__desk__send_keys", "write", "", "q", "", 1, "write · pane 1 · keys q"},
		{"mcp__desk__screen", "", "", "", "", 0, "looking at the screen"},
		{"mcp__desk__screen", "", "", "", "", 3, "pane 3 · looking at the screen"},
		{"mcp__desk__split", "", "", "", "", 1, "opening a pane"},
		{"mcp__desk__connect", "", "", "", "web1", 0, "connecting to web1"},
		{"mcp__desk__hosts", "", "", "", "", 0, "listing servers"},
		{"mcp__desk__ask_peer", "", "which db?", "", "", 0, "asking the peer Claude: which db?"},
		{"mcp__desk__task_done", "", "", "", "", 0, "reporting the task result"},
		{"mcp__desk__message_card", "", "", "", "", 0, "writing to a card's Claude"},
		{"mcp__desk__create_card", "", "", "", "", 0, "putting a card on the board"},
		{"mcp__desk__close_pane", "", "", "", "", 4, "closing pane 4"},
		{"mcp__desk__changes", "", "", "", "", 0, "looking at what changed recently"},
		{"mcp__desk__save_runbook", "", "", "", "", 0, "saving a runbook"},
		{"mcp__desk__note", "", "logs in /srv", "", "", 2, "pane 2 · noting: logs in /srv"},
		{"mcp__desk__nagios", "", "", "", "", 0, "nagios"},
		{"mcp__desk__metrics", "", "", "", "", 0, "metrics"},
		{"mcp__desk__secrets_search", "", "", "", "", 0, "searching secrets"},
		{"mcp__desk__secret_get", "", "", "", "", 0, "asking for a secret"},
		{"mcp__other__x", "", "", "", "", 0, ""},
	} {
		if got := toolLine(c.name, c.effect, c.cmd, c.keys, c.host, c.pane); got != c.want {
			t.Errorf("%s pane %d: %q, want %q", c.name, c.pane, got, c.want)
		}
	}
}

func TestSystemPromptA(t *testing.T) {
	old := guiReplies
	defer func() { guiReplies = old }()
	guiReplies = false
	h := systemPrompt("Acme", "web1", "")
	if !strings.Contains(h, `terminal to host "web1" of the`+"\norganization Acme.") || strings.Contains(h, "starts with no terminal") ||
		strings.Contains(h, "This organization's systems") || strings.Contains(h, guiHint) {
		t.Errorf("host prompt:\n%s", h)
	}
	o := systemPrompt("Acme", "", "Nagios (nagios): n1")
	if !strings.Contains(o, "for the organization Acme. The session starts with no terminal") ||
		!strings.Contains(o, "use them before poking at servers by hand: Nagios (nagios): n1.") {
		t.Errorf("org prompt:\n%s", o)
	}
	guiReplies = true
	if !strings.Contains(systemPrompt("Acme", "web1", ""), guiHint) {
		t.Error("the window's hint is missing")
	}
}

func TestBuildPromptA(t *testing.T) {
	var lines []string
	for i := 1; i <= 250; i++ {
		lines = append(lines, "line"+string(rune('a'+i%26))+strings.Repeat("x", i%3))
	}
	lines[0], lines[249] = "FIRST", "LAST"
	var blocks []Block
	for i := 1; i <= 12; i++ {
		blocks = append(blocks, Block{N: i, Cmd: "c" + string(rune('a'+i))})
	}
	big := Block{N: 12, Cmd: "journalctl", Output: strings.Join(lines, "\n"), Exit: 3, ByClaude: true}
	blocks[11] = big
	p := buildPrompt("why?", "web1", blocks, []Block{big}, &Screen{Text: "htop here", Alt: true, Running: "htop"})
	for _, want := range []string{
		"<terminal host=\"web1\">\nrecent:\n#3 ",
		"#12 exit=3 by=claude $ journalctl  (output below)",
		"#11 exit=0 by=you $ cl  (output omitted, ask the user to cite #11)",
		"<block n=12 exit=3 by=claude>\n$ journalctl\n[…50 lines cut]\n",
		"LAST\n</block>",
		"<screen alt=true running=\"htop\">\nhtop here\n</screen>\n</terminal>\n\nwhy?",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("missing %q in:\n%s", want, p)
		}
	}
	if strings.Contains(p, "#2 ") || strings.Contains(p, "FIRST") {
		t.Errorf("only the last 10 blocks and the last 200 lines go:\n%s", p)
	}
}

func TestClaudeCloseA(t *testing.T) {
	needSh(t)
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r := &recA{}
	(&Claude{stdin: r, proc: cmd.Process}).Close()
	if err := cmd.Wait(); err == nil || !strings.Contains(err.Error(), "killed") || !r.closed {
		t.Errorf("close: wait %v, stdin closed %v", err, r.closed)
	}
}

func TestStartClaudeStreamA(t *testing.T) {
	noUnix(t)
	runtimeA(t)
	args := fakeClaudeBinA(t, `cat <<'EOF'
{"type":"system","subtype":"init","session_id":"sess-9"}
not json
{"type":"system","subtype":"compact_boundary","compact_metadata":{"pre_tokens":5000,"post_tokens":800}}
{"type":"assistant","message":{"usage":{"input_tokens":10,"cache_creation_input_tokens":20,"cache_read_input_tokens":30},"content":[{"type":"text","text":"hello"},{"type":"tool_use","name":"mcp__desk__run","input":{"command":"uptime","effect":"read","pane":2,"explain":[{"part":"uptime","does":"load"}]}},{"type":"tool_use","name":"mcp__other__x","input":{}}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"no usage"}]}}
{"type":"result","is_error":true,"result":"boom","permission_denials":[{"tool_name":"mcp__desk__run","tool_input":{"command":"rm x"}}],"modelUsage":{"a":{"contextWindow":200000},"b":{"contextWindow":1000000}}}
{"type":"result","is_error":true,"result":""}
EOF
exit 3
`)
	dbg := filepath.Join(t.TempDir(), "raw.jsonl")
	t.Setenv("DESK_INFRA_DEBUG", dbg)
	tab := &Tab{org: "Acme", model: "sonnet", session: "s0", orgCfg: &orgConfig{Nagios: []SourceCfg{{Name: "n1"}}}}
	ch := make(chan claudeMsg, 32)
	c, err := StartClaude(t.TempDir(), "web1", func(m claudeMsg) { ch <- m }, tab)
	if err != nil {
		t.Fatal(err)
	}
	var got []claudeMsg
	for {
		select {
		case m := <-ch:
			got = append(got, m)
			if m.exited != nil {
				goto done
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no exit; got %d messages", len(got))
		}
	}
done:
	if len(got) != 10 {
		t.Fatalf("messages: %+v", got)
	}
	if got[0].session != "sess-9" || got[1].compacted != [2]int{5000, 800} || got[2].ctx != 60 || got[3].text != "hello" {
		t.Errorf("init/compact/usage/text: %+v", got[:4])
	}
	if got[4].tool != "read · pane 2 · uptime" || len(got[4].steps) != 1 || got[4].steps[0].Does != "load" {
		t.Errorf("tool: %+v", got[4])
	}
	if got[5].text != "no usage" || got[5].ctx != 0 {
		t.Errorf("an assistant message without usage: %+v", got[5])
	}
	if got[6].err == nil || got[6].err.Error() != "boom" || got[6].done {
		t.Errorf("error result: %+v", got[6])
	}
	if !got[7].done || got[7].window != 1000000 || len(got[7].blocked) != 1 || got[7].blocked[0].ToolInput.Command != "rm x" {
		t.Errorf("turn end: %+v", got[7])
	}
	if !got[8].done || got[8].err != nil || got[8].window != 0 {
		t.Errorf("an interrupted turn has no error: %+v", got[8])
	}
	if got[9].exited != c || !got[9].done || got[9].err == nil || got[9].err.Error() != "claude exited: exit status 3" {
		t.Errorf("exit: %+v", got[9])
	}
	b, _ := os.ReadFile(args)
	a := string(b)
	for _, want := range []string{"-p\n--model\nsonnet\n--resume\ns0\n", "--permission-mode\nauto", `"nagios"`, "Nagios (nagios): n1", `host "web1"`} {
		if !strings.Contains(a, want) {
			t.Errorf("args miss %q:\n%s", want, a)
		}
	}
	if raw, _ := os.ReadFile(dbg); !strings.Contains(string(raw), `"subtype":"init"`) {
		t.Errorf("debug stream: %q", raw)
	}
}

func TestCompactSessionA(t *testing.T) {
	needSh(t)
	run := func(body, model string) (claudeMsg, string) {
		args := fakeClaudeBinA(t, body)
		var got claudeMsg
		tab := &Tab{}
		compactSession(t.TempDir(), model, "sess-1", func(m claudeMsg) { got = m }, tab)
		if got.tab != tab || !got.done {
			t.Errorf("reports like a finished turn: %+v", got)
		}
		b, _ := os.ReadFile(args)
		return got, string(b)
	}
	m, a := run("cat >/dev/null\ncat <<'EOF'\n{\"type\":\"system\",\"subtype\":\"compact_boundary\",\"compact_metadata\":{\"pre_tokens\":50000,\"post_tokens\":800}}\n{\"type\":\"result\",\"result\":\"done\"}\nEOF\n", "haiku")
	if m.compacted != [2]int{50000, 800} || m.err != nil || !strings.Contains(a, "--resume\nsess-1\n") || !strings.Contains(a, "--model\nhaiku") {
		t.Errorf("compacted: %+v %q", m, a)
	}
	m, a = run("cat <<'EOF'\n{\"type\":\"result\",\"result\":\"Not enough messages to compact.\\nmore\"}\nEOF\n", "")
	if m.err == nil || m.err.Error() != "compact: Not enough messages to compact." || strings.Contains(a, "--model") {
		t.Errorf("Claude Code's reason: %v %q", m.err, a)
	}
	m, _ = run("exit 1\n", "")
	if m.err == nil || m.err.Error() != "compact failed: exit status 1" {
		t.Errorf("failure: %v", m.err)
	}
}

func TestClaudeCmdProfileA(t *testing.T) {
	skipOnWindows(t, "bwrap is Linux's: Windows takes NoBwrap's (TestClaudeCmdNoBwrap)")
	home, bin := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows' home
	t.Setenv("CLAUDE_PROFILE", "")
	t.Setenv("DESK_ROOT", filepath.Join(home, "Work"))
	t.Setenv("PATH", bin)
	for _, n := range []string{"claude", "bwrap"} {
		write(t, filepath.Join(bin, n), "#!/bin/sh\n")
		os.Chmod(filepath.Join(bin, n), 0o755)
	}
	cfg := filepath.Join(home, "Secure", "Claudes", "Acme")
	write(t, filepath.Join(cfg, ".claude.json"), `{"seed":1}`)
	os.MkdirAll(filepath.Join(home, "Secure", "Claudes", "Globex"), 0o755)
	cmd, err := claudeCmd(filepath.Join(home, "Work", "Acme", infraDir), "-p", "x")
	if err != nil {
		t.Fatal(err)
	}
	js := filepath.Join(home, "Secure", "Claudes", "claude-acme.json")
	a := strings.Join(cmd.Args, " ")
	if cmd.Path != filepath.Join(bin, "bwrap") || !strings.Contains(a, "--bind "+cfg+" "+filepath.Join(home, ".claude")+" ") ||
		!strings.Contains(a, "--bind "+js+" "+filepath.Join(home, ".claude.json")) ||
		!strings.Contains(a, "--setenv CLAUDE_PROFILE Acme") || !strings.HasSuffix(a, "-- "+filepath.Join(bin, "claude")+" -p x") {
		t.Errorf("bwrap command: %s %s", cmd.Path, a)
	}
	if b, _ := os.ReadFile(js); string(b) != `{"seed":1}` {
		t.Errorf("seeded from the profile's .claude.json: %q", b)
	}
	if _, err := claudeCmd(filepath.Join(home, "Work", "Globex", infraDir)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "Secure", "Claudes", "claude-globex.json")); string(b) != "{}" {
		t.Errorf("no .claude.json seeds {}: %q", b)
	}
	cmd, _ = claudeCmd(filepath.Join(home, "Elsewhere"), "-p")
	if filepath.Base(cmd.Path) != "claude" {
		t.Errorf("no profile runs claude as is: %s", cmd.Path)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := claudeCmd(filepath.Join(home, "Work", "Acme", infraDir)); err == nil {
		t.Error("a profile without claude on PATH must fail")
	}
}

// TestClaudeCmdNoBwrap: on macOS a profile runs claude itself with
// CLAUDE_CONFIG_DIR at its folder, and seeds no json.
func TestClaudeCmdNoBwrap(t *testing.T) {
	claude.NoBwrap = true
	t.Cleanup(func() { claude.NoBwrap = false })
	home, bin := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows' home
	t.Setenv("CLAUDE_PROFILE", "")
	t.Setenv("DESK_ROOT", filepath.Join(home, "Work"))
	t.Setenv("PATH", bin)
	write(t, filepath.Join(bin, "claude"+exeSuffix), "#!/bin/sh\n")
	os.Chmod(filepath.Join(bin, "claude"+exeSuffix), 0o755)
	cfg := filepath.Join(home, "Secure", "Claudes", "Acme")
	os.MkdirAll(cfg, 0o755)
	cmd, err := claudeCmd(filepath.Join(home, "Work", "Acme", infraDir), "-p", "x")
	if err != nil || cmd.Path != filepath.Join(bin, "claude"+exeSuffix) || strings.Join(cmd.Args[1:], " ") != "-p x" {
		t.Fatalf("%v %v", cmd, err)
	}
	if env := cmd.Environ(); !slices.Contains(env, "CLAUDE_CONFIG_DIR="+cfg) || !slices.Contains(env, "CLAUDE_PROFILE=Acme") {
		t.Errorf("env %v", env)
	}
	if _, err := os.Stat(filepath.Join(home, "Secure", "Claudes", "claude-acme.json")); err == nil {
		t.Error("seeded a json bwrap would bind")
	}
}

// TestLocalShell: bash without rc files; on macOS only a bash ≥ 4.4,
// else zsh without them.
func TestLocalShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("its fake bash is a shell script")
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	write(t, filepath.Join(bin, "bash"), "#!/bin/sh\nexit 1\n") // 3.2: the check fails
	os.Chmod(filepath.Join(bin, "bash"), 0o755)
	if s := strings.Join(localShell("linux"), " "); s != "bash --norc -i" {
		t.Errorf("linux: %s", s)
	}
	if s := strings.Join(localShell("darwin"), " "); s != "zsh -f -i" {
		t.Errorf("an old bash: %s", s)
	}
	write(t, filepath.Join(bin, "bash"), "#!/bin/sh\nexit 0\n")
	if s := strings.Join(localShell("darwin"), " "); s != "bash --norc -i" {
		t.Errorf("a new bash: %s", s)
	}
}

// TestLocalShellWindows: Windows PowerShell, or pwsh once it's on PATH;
// one-off commands through its -Command, bash -c elsewhere.
func TestLocalShellWindows(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	if s := strings.Join(localShell("windows"), " "); s != "powershell -NoLogo -NoProfile" {
		t.Errorf("no pwsh: %s", s)
	}
	pwsh := filepath.Join(bin, "pwsh")
	if runtime.GOOS == "windows" {
		pwsh += ".exe" // LookPath goes by PATHEXT there
	}
	write(t, pwsh, "#!/bin/sh\n")
	os.Chmod(pwsh, 0o755)
	if s := strings.Join(localShell("windows"), " "); s != "pwsh -NoLogo -NoProfile" {
		t.Errorf("pwsh: %s", s)
	}
	if s := ShellArgs("windows", "ls"); strings.Join(s, " ") != "pwsh -NoLogo -NoProfile -Command ls" {
		t.Errorf("windows: %q", s)
	}
	if s := ShellArgs("linux", "ls; pwd"); !slices.Equal(s, []string{"bash", "-c", "ls; pwd"}) {
		t.Errorf("linux: %q", s)
	}
}

// TestPowerShellIntegration: at a PowerShell prompt the PowerShell line is
// typed, not the POSIX one; with a restored history, the replay mark too.
func TestPowerShellIntegration(t *testing.T) {
	for _, pending := range []bool{false, true} {
		term, r := fakeTermA(t, "local")
		if pending {
			term.pending = &paneState{Text: "before"}
		}
		term.emu.Write([]byte(`PS C:\Users\me> `))
		term.maybeInject()
		got := readPipeA(r)
		if !strings.HasPrefix(got, psIntegration) || !strings.HasSuffix(got, "\r") || strings.Contains(got, "BASH_VERSION") {
			t.Fatalf("typed %q", got)
		}
		if strings.Contains(got, "]6974") != pending || strings.Contains(got, "Set-Location") {
			t.Errorf("pending %v: %q", pending, got)
		}
	}
	// a restored directory goes back, quoted the PowerShell way
	term, r := fakeTermA(t, "local")
	term.cwd = `C:\Users\me's`
	term.emu.Write([]byte(`PS C:\> `))
	term.maybeInject()
	if got := readPipeA(r); !strings.HasSuffix(got, `; Set-Location -LiteralPath 'C:\Users\me''s'`+"\r") {
		t.Errorf("restored dir: %q", got)
	}
}

// TestInjectFullWidthPrompt: a prompt that fills the pane leaves the cursor on
// the next, empty line; the shell's own B mark still finds it (fish's default
// prompt with a long git branch). Unmarked, an empty line is no prompt.
func TestInjectFullWidthPrompt(t *testing.T) {
	for _, marked := range []bool{false, true} {
		term, r := fakeTermA(t, "local")
		term.emu.Resize(10, 5)
		term.b = nil // fakeTermA's own; the shell's B mark is set by osc133, not wired here
		if marked {
			term.b = &pos{0, 9}
		}
		term.emu.Write([]byte("~/a (br)> \r\n"))
		term.maybeInject()
		if term.injected != marked {
			t.Fatalf("marked %v: injected %v", marked, term.injected)
		}
		if got := readPipeA(r); marked && !strings.Contains(got, "BASH_VERSION") {
			t.Errorf("typed %q", got)
		}
	}
}

// TestPowerShellRestoreEnv: a restored PowerShell pane reports its start's
// environment first, then sets the kept variables with $env: (quoted the
// PowerShell way, curly quotes too) and removes the unset ones; names a
// shell can't take are dropped, as for bash.
func TestPowerShellRestoreEnv(t *testing.T) {
	term, r := fakeTermA(t, "local")
	term.replay(paneState{Cwd: `C:\w`, Env: map[string]string{"B": "it's ‘x’", "A": "1", "bad-name": "x"}, Unset: []string{"OLD", "no way"}})
	term.emu.Write([]byte(`PS C:\> `))
	term.maybeInject()
	want := `; [Console]::Write("$(__deskE)"); $env:A = '1'; $env:B = 'it''s ‘‘x’’'; Remove-Item -LiteralPath Env:OLD -ErrorAction Ignore; Set-Location -LiteralPath 'C:\w'` + "\r"
	if got := readPipeA(r); !strings.HasPrefix(got, psIntegration) || got[len(psIntegration):] != want {
		t.Errorf("typed %q", got)
	}
	// the prompt reports the environment the way oscEnv reads it
	for _, s := range []string{`]6973;$b`, `-join [char]0`, `UTF8.GetBytes`, `(__deskE) + (& $global:__deskP)`} {
		if !strings.Contains(psIntegration, s) {
			t.Errorf("psIntegration lacks %q", s)
		}
	}
}

// TestOsc7Windows: PowerShell's directory is a Windows path; a location
// that isn't a folder (a registry drive) is ignored.
func TestOsc7Windows(t *testing.T) {
	term, _ := fakeTermA(t, "local")
	for _, tc := range []struct{ in, want string }{
		{`7;C:\Users\me`, `C:\Users\me`},
		{`7;HKEY_CURRENT_USER\Software`, `C:\Users\me`},
		{`7;\\server\share\x`, `\\server\share\x`},
		{`7;D:/data`, `D:/data`},
		{`7;C:`, `D:/data`},
		{`7;/home/me`, `/home/me`},
	} {
		term.osc7([]byte(tc.in))
		if term.cwd != tc.want {
			t.Errorf("%q: cwd %q, want %q", tc.in, term.cwd, tc.want)
		}
	}
}

// TestListLocalHostMac: on macOS the local host lists in Go (BSD find has
// no -printf): sizes and modes, "" the home folder, a missing one an error.
func TestListLocalHostMac(t *testing.T) {
	defer func(m bool) { macOS = m }(macOS)
	macOS = true
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.txt"), "abc")
	abs, es, err := listRemote(context.Background(), "local", "", dir)
	if err != nil || abs != dir || len(es) != 1 || es[0].Size != 3 || es[0].Mode == 0 {
		t.Fatalf("%q %+v %v", abs, es, err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir) // Windows' home
	if abs, _, err := listRemote(context.Background(), "local", "", ""); abs != dir || err != nil {
		t.Errorf("home: %q %v", abs, err)
	}
	if _, _, err := listRemote(context.Background(), "local", "", filepath.Join(dir, "gone")); err == nil || !strings.HasSuffix(err.Error(), "gone: no such directory") {
		t.Errorf("missing: %v", err)
	}
}

// --- mcp.go ---

func TestServeCardsMCPA(t *testing.T) {
	noUnix(t)
	runtimeA(t)
	sock, err := ServeCards(func(card, name string, args json.RawMessage) (string, bool) {
		return card + "|" + name + "|" + string(args) + " TOKEN=abc", name != "ci_log"
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sock, runtimeDir()) {
		t.Errorf("socket %s outside %s", sock, runtimeDir())
	}
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"alerts","arguments":{"x":1}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"ci_log","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":6,"method":"bogus"}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	}, "\n")
	var out strings.Builder
	mcpServe(sock, "card=DESK-4", strings.NewReader(in), &out)
	type resp struct {
		ID     int `json:"id"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	rs := map[int]resp{}
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var r resp
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatal(err)
		}
		rs[r.ID] = r
	}
	if len(rs) != 6 {
		t.Fatalf("responses: %s", out.String())
	}
	var names []string
	for _, tl := range rs[2].Result.Tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "alerts,tickets,link,ci,ci_log,add_project,ask_workspace,drawings,drawing_read,drawing_edit,drawing_suggest" {
		t.Errorf("a card gets only the card tools: %v", names)
	}
	if r := rs[3].Result; r.IsError || len(r.Content) != 1 || r.Content[0].Text != `DESK-4|alerts|{"x":1} TOKEN=[redacted]` {
		t.Errorf("call: %+v", r)
	}
	if !rs[4].Result.IsError {
		t.Error("ok=false is an error")
	}
	if rs[5].Error != nil || rs[6].Error == nil || rs[6].Error.Code != -32601 || rs[6].Error.Message != "method not found: bogus" {
		t.Errorf("ping %+v, bogus %+v", rs[5].Error, rs[6].Error)
	}
}

func TestForwardFailuresA(t *testing.T) {
	noUnix(t)
	if r := forward(filepath.Join(t.TempDir(), "none.sock"), toolCall{Name: "x"}); !r.IsError || !strings.HasPrefix(r.Text, "desk is not reachable: ") {
		t.Errorf("no socket: %+v", r)
	}
	sock := filepath.Join(runtimeA(t), "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.Close()
		}
	}()
	if r := forward(sock, toolCall{Name: "x"}); !r.IsError || !strings.HasPrefix(r.Text, "desk closed the call: ") {
		t.Errorf("hung up: %+v", r)
	}
}

func TestCardMCPA(t *testing.T) {
	if cfg, allow := CardMCP("", "1"); cfg != "" || allow != nil {
		t.Errorf("no socket: %q %v", cfg, allow)
	}
	cfg, allow := CardMCP("/run/x.sock", "DESK-2")
	var c struct {
		MCPServers map[string]struct {
			Type string   `json:"type"`
			Args []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(cfg), &c); err != nil {
		t.Fatal(err)
	}
	if d := c.MCPServers["desk"]; d.Type != "stdio" || strings.Join(d.Args, " ") != "mcp /run/x.sock card=DESK-2" {
		t.Errorf("config: %s", cfg)
	}
	if strings.Join(allow, ",") != "mcp__desk__alerts,mcp__desk__tickets,mcp__desk__link,mcp__desk__ci,mcp__desk__ci_log,mcp__desk__add_project,mcp__desk__ask_workspace,"+
		"mcp__desk__drawings,mcp__desk__drawing_read,mcp__desk__drawing_edit,mcp__desk__drawing_suggest" {
		t.Errorf("allowed: %v", allow)
	}
	cfg, allow = WorkspaceMCP("/run/x.sock", "DESK-2")
	if !strings.Contains(cfg, `"ws=DESK-2"`) || strings.Join(allow, ",") != "mcp__desk__alerts,mcp__desk__tickets,mcp__desk__link,mcp__desk__ci,mcp__desk__ci_log,"+
		"mcp__desk__drawings,mcp__desk__drawing_read,mcp__desk__drawing_suggest,mcp__desk__suggest_to_card" {
		t.Errorf("workspace: %s %v", cfg, allow)
	}
}

// TestWorkspaceMCPA: a workspace Claude's server lists its own tools and
// calls as "ws:<id>", so desk tells it from the card's main Claude.
func TestWorkspaceMCPA(t *testing.T) {
	noUnix(t)
	sock := filepath.Join(runtimeA(t), "w.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		var tc toolCall
		json.NewDecoder(c).Decode(&tc)
		json.NewEncoder(c).Encode(toolResult{Text: tc.Card + "|" + tc.Name})
	}()
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"suggest_to_card","arguments":{}}}`
	var out strings.Builder
	mcpServe(sock, "ws=c9", strings.NewReader(in), &out)
	if s := out.String(); !strings.Contains(s, `"suggest_to_card"`) || strings.Contains(s, `"add_project"`) || !strings.Contains(s, "ws:c9|suggest_to_card") {
		t.Errorf("served: %s", s)
	}
}

func TestRuntimeDirA(t *testing.T) {
	d := runtimeA(t)
	if runtimeDir() != filepath.Join(d, "desk") {
		t.Errorf("XDG: %s", runtimeDir())
	}
	tmp := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMPDIR", tmp)
	defer func(m bool) { macOS = m }(macOS)
	macOS = false
	if exeSuffix == "" { // Windows takes the cache, as macOS
		if got := runtimeDir(); got != filepath.Join(tmp, "desk") {
			t.Errorf("fallback: %s", got)
		}
		if fi, err := os.Stat(filepath.Join(tmp, "desk")); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("made user only: %v %v", fi, err)
		}
	}
	macOS = true // no XDG_RUNTIME_DIR, a $TMPDIR of its own per process: the cache
	t.Setenv("XDG_CACHE_HOME", tmp)
	if got := runtimeDir(); got != filepath.Join(tmp, "desk", "run") {
		t.Errorf("macOS: %s", got)
	}
}

func approverA(t *testing.T, seen *[]*approval, answer func(a *approval) approvalResult) {
	setDispatchA(t, func(msg tea.Msg) {
		if am, ok := msg.(approvalMsg); ok {
			*seen = append(*seen, am.a)
			am.a.done <- answer(am.a)
		}
	})
}

func TestServeToolsRedactsA(t *testing.T) {
	noUnix(t)
	runtimeA(t)
	var seen []*approval
	approverA(t, &seen, func(a *approval) approvalResult { return approvalResult{ok: true, cmd: a.cmd} })
	tab := &Tab{org: "Acme", hosts: []node{{group: "g", label: "db1", desc: "DB_PASSWORD=hunter2"}},
		orgCfg: &orgConfig{Vaults: []VaultCfg{{Name: "pb", Type: "passbolt"}},
			vaults: map[string]vault{"pb": fakeVault{"i1": {ID: "i1", Name: "db", Description: "DB_PASSWORD=hunter2"}}}}}
	sock, ln, err := serveTools(tab)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if r := forward(sock, toolCall{Name: "hosts"}); strings.Contains(r.Text, "hunter2") || !strings.Contains(r.Text, "DB_PASSWORD=[redacted]") {
		t.Errorf("tool output must be redacted: %q", r.Text)
	}
	if r := forward(sock, toolCall{Name: "secret_get", Args: json.RawMessage(`{"id":"i1","reason":"migrate"}`)}); !strings.Contains(r.Text, "DB_PASSWORD=hunter2") {
		t.Errorf("the approved secret goes as is: %q", r.Text)
	}
	if len(seen) != 1 || !seen[0].secret || seen[0].reason != "migrate" || seen[0].cmd != "pb · db (user )" {
		t.Errorf("approval: %+v", seen)
	}
}

func TestCallToolHostsA(t *testing.T) {
	tab := &Tab{org: "Acme", hosts: []node{
		{group: "g", label: "web1", addr: "192.0.2.10", desc: "frontend", tags: "nginx,prod"},
		{group: "g2", label: "db1"},
	}}
	if r := tab.callTool(toolCall{Name: "hosts"}); r.Text != "2 servers in Acme\ng/web1 (192.0.2.10) — frontend [nginx,prod]\ng2/db1" {
		t.Errorf("all: %q", r.Text)
	}
	if r := tab.callTool(toolCall{Name: "hosts", Args: json.RawMessage(`{"query":"nginx"}`)}); r.Text != "1 servers in Acme\ng/web1 (192.0.2.10) — frontend [nginx,prod]" {
		t.Errorf("query: %q", r.Text)
	}
	many := &Tab{org: "Acme"}
	for i := 0; i < 305; i++ {
		many.hosts = append(many.hosts, node{group: "g", label: "h"})
	}
	lines := strings.Split(many.callTool(toolCall{Name: "hosts"}).Text, "\n")
	if len(lines) != 302 || lines[0] != "305 servers in Acme" || lines[301] != "… 5 more, narrow the query" {
		t.Errorf("cut at 300: %d lines, last %q", len(lines), lines[len(lines)-1])
	}
}

func TestCallToolConnectAndRunbookA(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Acme", infraDir)
	tab := &Tab{org: "Acme", dir: dir, hosts: []node{{group: "a", label: "web1", addr: "192.0.2.1"}, {group: "b", label: "web1"}}}
	if r := tab.callTool(toolCall{Name: "connect", Args: json.RawMessage(`{"host":"web1"}`)}); !r.IsError || r.Text != `"web1" is a/web1 or b/web1: pass group/name` {
		t.Errorf("ambiguous: %+v", r)
	}
	if r := tab.callTool(toolCall{Name: "connect", Args: json.RawMessage(`{"host":"nope"}`)}); !r.IsError || r.Text != `no server "nope" in Acme; search with hosts` {
		t.Errorf("unknown: %+v", r)
	}
	if n, err := tab.findHost("192.0.2.1"); err != nil || n.group != "a" {
		t.Errorf("by address: %+v %v", n, err)
	}
	if n, err := tab.findHost("B/WEB1"); err != nil || n.group != "b" {
		t.Errorf("by group/name: %+v %v", n, err)
	}
	save := func(name string) toolResult {
		b, _ := json.Marshal(map[string]string{"name": name, "markdown": "# Restart PHP\n1. ```sh\nsystemctl restart php-fpm\n```"})
		return tab.callTool(toolCall{Name: "save_runbook", Args: b})
	}
	r1, r2 := save("Restart PHP"), save("Restart PHP")
	if r1.IsError || r1.Text != "saved "+filepath.Join(dir, "runbooks", "restart-php.md")+"; the user starts it from the palette ("+modText("K")+")" ||
		!strings.Contains(r2.Text, "restart-php-2.md") {
		t.Errorf("save: %+v %+v", r1, r2)
	}
	if r := save("!!"); !r.IsError || r.Text != "a runbook needs a name and content" {
		t.Errorf("no name: %+v", r)
	}
}

func TestCallToolPaneToolsA(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	tab := &Tab{org: "PaneOrg"}
	if r := tab.callTool(toolCall{Name: "screen"}); !r.IsError || r.Text != "no pane is open: use connect to open one on a server" {
		t.Errorf("no pane: %+v", r)
	}
	term, pipe := fakeTermA(t, "web1")
	term.emu.WriteString("hello")
	p1 := &Pane{id: 1, term: term, name: "web1"}
	term2, _ := fakeTermA(t, "web1")
	p2 := &Pane{id: 2, term: term2, name: "web1", claude: true}
	tab.panes, tab.term = []*Pane{p1, p2}, term
	call := func(name, args string) toolResult {
		return tab.callTool(toolCall{Name: name, Args: json.RawMessage(args)})
	}
	if r := call("screen", `{"pane":9}`); !r.IsError || r.Text != "there is no pane 9; open panes: pane 1 on web1: idle prompt, the user's focus; pane 2 on web1: idle prompt, opened by you" {
		t.Errorf("unknown pane: %q", r.Text)
	}
	if r := call("screen", `{}`); r.IsError || r.Text != "pane 1 alt=false running=\"\"\nhello" {
		t.Errorf("screen: %q", r.Text)
	}
	if r := call("note", `{"text":"nginx  logs in\n/srv/log"}`); r.Text != "noted for web1" || !tab.notesSent["web1"] {
		t.Errorf("note: %+v", r)
	}
	if b, _ := os.ReadFile(notesPath("PaneOrg", "web1")); !strings.Contains(string(b), " nginx logs in /srv/log\n") {
		t.Errorf("note file: %q", b)
	}
	if r := call("note", `{"text":"  "}`); !r.IsError || r.Text != "empty note" {
		t.Errorf("empty note: %+v", r)
	}
	if r := call("close_pane", `{"pane":1}`); !r.IsError || r.Text != "pane 1 is the user's; you can only close panes you opened" || p1.closed {
		t.Errorf("user's pane: %+v", r)
	}
	if r := call("close_pane", `{"pane":2}`); r.IsError || r.Text != "closed pane 2" || !p2.closed {
		t.Errorf("own pane: %+v", r)
	}
	if r := call("run", `{"command":"  "}`); !r.IsError || r.Text != "empty command" {
		t.Errorf("empty: %+v", r)
	}
	if r := call("run", `{"command":"ls | wc -l","effect":"read"}`); !r.IsError || !strings.HasPrefix(r.Text, "not run: this command chains") {
		t.Errorf("compound: %+v", r)
	}
	var seen []*approval
	approverA(t, &seen, func(a *approval) approvalResult { return approvalResult{} })
	if r := call("run", `{"command":"systemctl restart nginx","effect":"write","reason":"reload"}`); !r.IsError || !strings.HasPrefix(r.Text, "the user denied this. Do not retry") {
		t.Errorf("denied: %+v", r)
	}
	if len(seen) != 1 || seen[0].why != "Claude marked it as a write" || seen[0].pane != 1 || seen[0].keys || seen[0].reason != "reload" {
		t.Errorf("approval: %+v", seen)
	}
	approverA(t, &seen, func(a *approval) approvalResult { return approvalResult{ok: true, cmd: "uptime -p"} })
	r := call("run", `{"command":"uptime","effect":"write","timeout_seconds":1}`)
	if !strings.HasPrefix(r.Text, "pane 1 · (the user edited it to: uptime -p)\nstill running after 1s") {
		t.Errorf("edited: %q", r.Text)
	}
	if got := readPipeA(pipe); got != "\x05\x15uptime -p\r" {
		t.Errorf("typed %q", got)
	}
	if r := call("send_keys", `{}`); !r.IsError || r.Text != "no keys" {
		t.Errorf("no keys: %+v", r)
	}
	if r := call("send_keys", `{"keys":"q","effect":"read"}`); !r.IsError || !strings.HasPrefix(r.Text, "pane 1 is at an idle shell prompt") {
		t.Errorf("at prompt: %+v", r)
	}
	term.b = nil // a program owns the terminal
	go io.Copy(io.Discard, term.emu)
	seen = nil
	approverA(t, &seen, func(a *approval) approvalResult { return approvalResult{note: " (nope)"} })
	if r := call("send_keys", `{"keys":"k","effect":"write"}`); !r.IsError || !strings.HasPrefix(r.Text, "the user denied this (nope).") ||
		len(seen) != 1 || !seen[0].keys || seen[0].why != "Claude marked these keys as a write" {
		t.Errorf("write keys: %+v %+v", r, seen)
	}
	seen = nil
	if r := call("send_keys", `{"keys":"q","effect":"read"}`); r.IsError || !strings.HasPrefix(r.Text, "pane 1 · sent. alt=false") || len(seen) != 0 {
		t.Errorf("read keys: %+v, asked %d", r, len(seen))
	}
	if r := call("bogus", `{}`); !r.IsError || r.Text != "unknown tool bogus" {
		t.Errorf("unknown: %+v", r)
	}
}

func TestOpenPaneA(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	tab := &Tab{org: "OpenOrg"}
	n := node{host: "web1", dir: "/d", label: "web1"}
	setDispatchA(t, func(msg tea.Msg) {
		if pm, ok := msg.(paneMsg); ok {
			pm.done <- nil
		}
	})
	if r := tab.openPane(n); !r.IsError || r.Text != "could not open a pane on web1" {
		t.Errorf("no pane: %+v", r)
	}
	addNote("OpenOrg", "web1", "php-fpm restarts with systemctl")
	term, _ := fakeTermA(t, "web1")
	setDispatchA(t, func(msg tea.Msg) {
		if pm, ok := msg.(paneMsg); ok {
			if pm.tab != tab || pm.n != n {
				t.Errorf("pane asked for %+v", pm.n)
			}
			pm.done <- &Pane{id: 5, term: term, dir: "/d", name: "web1"}
		}
	})
	r := tab.openPane(n)
	if r.IsError || !strings.Contains(r.Text, "php-fpm restarts with systemctl") ||
		!strings.HasSuffix(r.Text, "opened pane 5 on web1, at a shell prompt. Pass pane=5; close_pane it when done.") {
		t.Errorf("opened: %+v", r)
	}
	stuck, _ := fakeTermA(t, "web1")
	stuck.b = nil
	p := &Pane{id: 3, term: stuck, dir: "/d"}
	tab.panes = []*Pane{p}
	if tab.unreachable(n) != p {
		t.Error("a pane on the server that never connected is the one to wait on")
	}
	if tab.unreachable(node{host: "web1", dir: "/other"}) != nil || tab.unreachable(node{host: "web2", dir: "/d"}) != nil {
		t.Error("another server's pane")
	}
	stuck.reached = true
	if tab.unreachable(n) != nil {
		t.Error("a connected pane isn't unreachable")
	}
	stuck.reached, p.closed = false, true
	if tab.unreachable(n) != nil {
		t.Error("a closed pane isn't waited on")
	}
}

func TestGateA(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	o := &orgConfig{}
	o.Approvals.Deny = []string{"* --force*"}
	o.Approvals.Allow = []string{"df *"}
	tab := &Tab{org: "GateOrg", orgCfg: o}
	var seen []*approval
	approverA(t, &seen, func(a *approval) approvalResult { return approvalResult{} })
	if cmd, _, denied := tab.gate("git push --force", "", "", false, 1); denied == nil || cmd != "" ||
		len(seen) != 1 || seen[0].why != "[approvals] always asks for * --force*" {
		t.Errorf("deny pattern asks even unasked: %q %v %+v", cmd, denied, seen)
	}
	if cmd, note, denied := tab.gate("x --force", "", "", true, 1); denied != nil || cmd != "x --force" || note != "" || len(seen) != 1 {
		t.Error("deny patterns are for commands, not keys")
	}
	if _, _, denied := tab.gate("df -h", "", "a write", false, 1); denied != nil || len(seen) != 1 {
		t.Error("the allow list skips the prompt")
	}
	if _, _, denied := tab.gate("df -h", "", "a write", true, 1); denied == nil || len(seen) != 2 {
		t.Error("the allow list is for commands, not keys")
	}
	always.add("GateOrg", allowKey("free -m", false))
	if _, _, denied := tab.gate("free -m", "", "a write", false, 1); denied != nil || len(seen) != 2 {
		t.Error("an always answer skips the prompt")
	}
	if _, _, denied := tab.gate("free -m", "", "a write", true, 1); denied == nil {
		t.Error("an always for a command isn't one for keys")
	}
	k := &peerTask{id: 77, from: "gate-dev", tab: tab}
	hub.Lock()
	hub.running[tab] = k
	hub.Unlock()
	defer func() { hub.Lock(); delete(hub.running, tab); hub.Unlock() }()
	approverA(t, &seen, func(a *approval) approvalResult { return approvalResult{ok: true, cmd: a.cmd} })
	if cmd, note, denied := tab.gate("reboot", "why", "a write", false, 2, step{"reboot", "restarts"}); denied != nil || cmd != "reboot" || note != "" {
		t.Errorf("approved as is: %q %q", cmd, note)
	}
	if a := seen[len(seen)-1]; a.task != "gate-dev · task 77" || a.pane != 2 || len(a.explain) != 1 || a.reason != "why" {
		t.Errorf("approval: %+v", a)
	}
}

func TestOrgCallSecretsA(t *testing.T) {
	var seen []*approval
	approverA(t, &seen, func(a *approval) approvalResult { return approvalResult{} })
	fv := fakeVault{"i1": {ID: "i1", Name: "srv", Username: "deploy", URI: "https://example.com"}}
	tab := &Tab{org: "Acme", orgCfg: &orgConfig{Vaults: []VaultCfg{{Name: "pb", Type: "passbolt"}}, vaults: map[string]vault{"pb": fv}}}
	if r := tab.callTool(toolCall{Name: "secrets_search", Args: json.RawMessage(`{"query":"srv"}`)}); r.IsError || r.Text != "1 items\npb · id=i1 · srv · user=deploy · https://example.com" {
		t.Errorf("search: %+v", r)
	}
	if r := tab.callTool(toolCall{Name: "secret_get", Args: json.RawMessage(`{"id":"i1"}`)}); !r.IsError || r.Text != "the user denied reading this secret; don't retry, ask them" {
		t.Errorf("denied: %+v", r)
	}
	if len(seen) != 1 || seen[0].cmd != "pb · srv (user deploy)" || seen[0].why != "Claude will see this password" {
		t.Errorf("approval: %+v", seen)
	}
	if r := tab.callTool(toolCall{Name: "secret_get", Args: json.RawMessage(`{"id":"i1","vault":"other"}`)}); !r.IsError || r.Text != `no vault "other"` {
		t.Errorf("unknown vault: %+v", r)
	}
	bad := &Tab{orgCfg: &orgConfig{Vaults: []VaultCfg{{Name: "op", Type: "1password"}}}}
	if r := bad.callTool(toolCall{Name: "secrets_search"}); !r.IsError || !strings.Contains(r.Text, `unknown type "1password"`) {
		t.Errorf("unknown type: %+v", r)
	}
	if _, err := tab.orgCall(context.Background(), "zzz", orgArgs{}, ""); err == nil || err.Error() != "unknown tool zzz" {
		t.Errorf("unknown tool: %v", err)
	}
}

// --- peer.go ---

func TestFormatEventsA(t *testing.T) {
	for _, c := range []struct {
		ev   peerEvent
		want string
	}{
		{peerEvent{Kind: "question", ID: 4, Org: "Acme", Text: "which db?"}, "question 4 from Acme's Claude:\nwhich db?\n→ reply: desk peer answer 4 --from svc <<'EOF' … EOF"},
		{peerEvent{Kind: "result", ID: 5, Org: "Acme", Text: "done"}, "result of task 5 (Acme):\ndone"},
		{peerEvent{Kind: "turn", ID: 6, Org: "Acme", Text: "waiting"}, "task 6 (Acme) is not reported done yet; Acme's Claude stopped and said:\nwaiting"},
		{peerEvent{Kind: "message", Org: "Acme", Text: "hi"}, "message from Acme's Claude:\nhi\n→ reply, if it needs one: desk peer tell Acme --from svc <<'EOF' … EOF"},
	} {
		if got := formatEvent("svc", c.ev); got != c.want {
			t.Errorf("%s: %q", c.ev.Kind, got)
		}
	}
	if got := formatEvents("svc", []peerEvent{{Kind: "result", ID: 1, Org: "A", Text: "x"}, {Kind: "result", ID: 2, Org: "A", Text: "y"}}); got !=
		"result of task 1 (A):\nx\n\nresult of task 2 (A):\ny\n\nRun desk peer wait again for more." {
		t.Errorf("events: %q", got)
	}
}

func TestCardToolErrorsA(t *testing.T) {
	var w Workspace
	w.SetCards(Cards{})
	defer w.SetCards(Cards{})
	tab := &Tab{org: "Acme"}
	if r := tab.tellCard("DESK-1", "x"); !r.IsError || r.Text != "no desk board here" {
		t.Errorf("no board: %+v", r)
	}
	if r := tab.newCard("t", "n"); !r.IsError || r.Text != "no desk board here" {
		t.Errorf("no board: %+v", r)
	}
	w.SetCards(Cards{Is: func(n string) bool { return strings.HasPrefix(n, "DESK-") },
		Tell: func(key, text string) error { return errors.New("board busy") },
		New:  func(org, title, notes string) (string, error) { return "", errors.New("no org " + org) }})
	if r := tab.tellCard("desk-1", "x"); !r.IsError || r.Text != "board busy" {
		t.Errorf("tell fails: %+v", r)
	}
	if r := tab.newCard("t", "n"); !r.IsError || r.Text != "no org Acme" {
		t.Errorf("new fails: %+v", r)
	}
	done := make(chan toolResult, 1)
	go func() { done <- tab.askPeer("DESK-9", "which db?") }()
	select {
	case r := <-done:
		if r.Text != "DESK-9 answered:\n(desk couldn't reach DESK-9: board busy)" {
			t.Errorf("unreachable card: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a question the card can't get must not wait")
	}
	if r := (&Tab{}).askPeer("", "q"); !r.IsError || !strings.Contains(r.Text, "nobody to ask") {
		t.Errorf("no peer: %+v", r)
	}
}

func TestTaskDoneOtherTabA(t *testing.T) {
	mine, other := &Tab{org: "A"}, &Tab{org: "B"}
	hub.Lock()
	hub.tasks[6001] = &peerTask{id: 6001, from: "x", tab: other}
	hub.Unlock()
	defer func() { hub.Lock(); delete(hub.tasks, 6001); hub.Unlock() }()
	if r := mine.taskDone(6001, "", "done", "s"); !r.IsError || !strings.HasPrefix(r.Text, "no open task 6001 in this session") {
		t.Errorf("another tab's task: %+v", r)
	}
	hub.Lock()
	still := hub.tasks[6001] != nil
	hub.Unlock()
	if !still {
		t.Error("the task must stay")
	}
}

func dropTasksA(tabs ...*Tab) {
	hub.Lock()
	defer hub.Unlock()
	for id, k := range hub.tasks {
		for _, t := range tabs {
			if k.tab == t {
				delete(hub.tasks, id)
			}
		}
	}
	for _, t := range tabs {
		delete(hub.running, t)
		delete(hub.last, t)
	}
}

func TestPeerOpsA(t *testing.T) {
	m := &model{tree: []node{{depth: 0, label: "Acme", org: "Acme", dir: t.TempDir()}, {depth: 1, label: "g"},
		{depth: 0, label: "Globex", org: "Globex", dir: t.TempDir()}}}
	if r := m.peer(peerReq{Op: "list"}); !strings.Contains(r.Text, "org Acme\n  its Claude tab is not open") || !strings.HasSuffix(r.Text, peerHelp) {
		t.Errorf("list: %q", r.Text)
	}
	if r := m.peer(peerReq{Op: "answer", ID: 987654}); r.Err != "no open question 987654 (answered already or it timed out)" {
		t.Errorf("answer: %+v", r)
	}
	if r := m.peer(peerReq{Op: "tell", Org: "Initech"}); r.Err != `no org "Initech"; orgs: Acme, Globex` {
		t.Errorf("unknown org: %+v", r)
	}
	if r := m.peer(peerReq{Op: "suggest", Org: "acme", From: "dev", Cmd: "systemctl reload nginx", Why: "config changed"}); r.Text != "Acme's Claude gets it with its next question or task" {
		t.Errorf("suggest: %+v", r)
	}
	acme := m.tab()
	defer dropTasksA(acme)
	if len(acme.peerCtx) != 1 || acme.peerCtx[0] != "<peer from=\"dev\">\nSuggested command: systemctl reload nginx\nWhy: config changed\n</peer>" ||
		lastChatA(acme) != "dev · suggest\nSuggested command: systemctl reload nginx\nWhy: config changed" {
		t.Errorf("suggest context: %q / %q", acme.peerCtx, lastChatA(acme))
	}
	if r := m.peer(peerReq{Op: "zap", Org: "acme"}); r.Err != "unknown op zap" {
		t.Errorf("unknown op: %+v", r)
	}
	m.peer(peerReq{Op: "tell", Org: "GLOBEX", From: "dev", Text: "x"})
	globex := m.tabs[1]
	defer dropTasksA(globex)
	if m.active != 0 || m.flash != "a peer opened Globex's Claude (tab 2)" || !globex.orgOnly {
		t.Errorf("a peer must not pull the user away: active %d, flash %q", m.active, m.flash)
	}
	acme.busy = true
	r := m.peer(peerReq{Op: "do", Org: "acme", From: "dev", Text: "deploy v2"})
	hub.Lock()
	id := hub.seq
	queued := hub.tasks[id]
	hub.Unlock()
	if !strings.HasPrefix(r.Text, "task ") || !strings.Contains(r.Text, "queued: Acme's Claude is busy") || len(acme.queue) != 1 || acme.queue[0] != queued {
		t.Errorf("busy: %+v, queue %d", r, len(acme.queue))
	}
	c, rec := claudeA(t)
	globex.claude = c
	r = m.peer(peerReq{Op: "do", Org: "globex", From: "dev", Text: "rotate logs"})
	hub.Lock()
	id = hub.seq
	running := hub.running[globex]
	hub.Unlock()
	if !strings.Contains(r.Text, "started in Globex") || running == nil || running.id != id || !globex.busy ||
		!strings.Contains(rec.String(), "task id=") || !strings.Contains(rec.String(), "rotate logs") {
		t.Errorf("started: %+v; stdin %q", r, rec.String())
	}
	acme.busy = false
	fc, frec := claudeA(t)
	frec.fail = true
	acme.claude = fc
	r = m.peer(peerReq{Op: "do", Org: "acme", From: "dev", Text: "x"})
	hub.Lock()
	id = hub.seq
	left, run := hub.tasks[id], hub.running[acme]
	hub.Unlock()
	if r.Err != "broken pipe" || left != nil || run != nil {
		t.Errorf("failed start must forget the task: %+v %v %v", r, left, run)
	}
	term, _ := fakeTermA(t, "web1")
	globex.panes, globex.term = []*Pane{{id: 1, term: term, name: "web1"}}, term
	globex.chat = append(globex.chat, &chatEntry{who: "you", text: "disk?"}, &chatEntry{who: "claude", text: "fine"})
	l := m.peerList()
	if !strings.Contains(l, `tab 2 "Globex" · working · pane 1 on web1: idle prompt, the user's focus`) || !strings.Contains(l, `user's last question: "disk?"`) ||
		!strings.Contains(l, `tab 1 "Acme" · idle · no panes`) || strings.Contains(l, "not open") {
		t.Errorf("list:\n%s", l)
	}
}

func TestTurnEndedA(t *testing.T) {
	m := &model{}
	c, rec := claudeA(t)
	tab := &Tab{org: "Acme", claude: c, chat: []*chatEntry{{who: "claude", text: "first"}, {who: "you", text: "q"}, {who: "claude", text: "deployed v2"}, {who: "note", text: "n"}}}
	defer dropTasksA(tab)
	k1 := &peerTask{id: 8001, from: "turn-a", tab: tab}
	k2 := &peerTask{id: 8002, from: "turn-b", text: "next", tab: tab}
	hub.Lock()
	hub.tasks[8001], hub.tasks[8002], hub.running[tab] = k1, k2, k1
	hub.Unlock()
	tab.queue = []*peerTask{k2}
	m.turnEnded(tab)
	if evs := waitEvents("turn-a", time.Second); len(evs) != 1 || evs[0].Kind != "turn" || evs[0].ID != 8001 || evs[0].Text != "deployed v2" {
		t.Errorf("turn: %+v", evs)
	}
	hub.Lock()
	run := hub.running[tab]
	hub.Unlock()
	if run != k2 || len(tab.queue) != 0 || !strings.Contains(rec.String(), "task id=8002") {
		t.Errorf("next task not started: %v %d", run, len(tab.queue))
	}
	k3 := &peerTask{id: 8003, from: "turn-c", tab: tab}
	tab.queue = []*peerTask{k3}
	m.turnEnded(tab) // busy now: k3 waits
	if len(tab.queue) != 1 {
		t.Error("a busy tab keeps its queue")
	}
	waitEvents("turn-b", time.Second)
	tab.busy = false
	rec.fail = true
	m.turnEnded(tab)
	if evs := waitEvents("turn-c", time.Second); len(evs) != 1 || evs[0].Kind != "result" || evs[0].Text != "failed to start: broken pipe" {
		t.Errorf("failed start: %+v", evs)
	}
}

func TestPeerSocketA(t *testing.T) {
	noUnix(t)
	runtimeA(t)
	m := &model{tree: []node{{depth: 0, label: "Acme", org: "Acme", dir: t.TempDir()}}}
	setDispatchA(t, func(msg tea.Msg) {
		if pm, ok := msg.(peerMsg); ok {
			pm.reply <- m.peer(pm.req)
		}
	})
	if _, err := servePeers(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(peerDir()); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("peer dir: %v %v", fi, err)
	}
	stdin := func(s string) {
		r, w, _ := os.Pipe()
		w.WriteString(s)
		w.Close()
		old := os.Stdin
		os.Stdin = r
		t.Cleanup(func() { os.Stdin = old })
	}
	code, out, _ := captureA(t, func() int { return peerMain([]string{"tell", "acme", "--from", "svc-a", "--text", "db is pg"}) })
	if code != 0 || out != "Acme's Claude gets it with its next question or task\n" {
		t.Errorf("tell: %d %q", code, out)
	}
	defer dropTasksA(m.tabs...)
	stdin("  from stdin \n")
	captureA(t, func() int { return peerMain([]string{"tell", "acme", "--from", "svc-a"}) })
	if ctx := m.tabs[0].peerCtx; len(ctx) != 2 || ctx[0] != "<peer from=\"svc-a\">\ndb is pg\n</peer>" || ctx[1] != "<peer from=\"svc-a\">\nfrom stdin\n</peer>" {
		t.Errorf("context: %q", ctx)
	}
	for _, c := range []struct {
		args []string
		code int
		err  string
	}{
		{[]string{"answer"}, 2, "desk peer answer: missing ID"},
		{[]string{"tell"}, 2, "desk peer tell: missing ORG"},
		{[]string{"do", "acme"}, 2, "desk peer do: no text"},
		{[]string{"answer", "123456", "--text", "x"}, 1, "no open question 123456"},
		{[]string{"wait", "--from", "nobody-a", "--timeout", "1"}, 1, "nothing in 1s"},
		{[]string{"--bogus"}, 2, ""},
	} {
		stdin("")
		code, _, errOut := captureA(t, func() int { return peerMain(c.args) })
		if code != c.code || !strings.Contains(errOut, c.err) {
			t.Errorf("%v: %d %q", c.args, code, errOut)
		}
	}
	hub.Lock()
	post("waiter-a", peerEvent{Kind: "result", ID: 3, Org: "Acme", Text: "ok"})
	hub.Unlock()
	if code, out, _ := captureA(t, func() int { return peerMain([]string{"wait", "--from", "waiter-a", "--timeout", "1"}) }); code != 0 ||
		out != "result of task 3 (Acme):\nok\n\nRun desk peer wait again for more.\n" {
		t.Errorf("wait: %d %q", code, out)
	}
	if code, out, _ := captureA(t, func() int { return peerMain([]string{"help"}) }); code != 0 || out != peerHelp+"\n" {
		t.Errorf("help: %d", code)
	}
	if code, out, _ := captureA(t, func() int { return peerMain(nil) }); code != 0 || !strings.Contains(out, "org Acme") {
		t.Errorf("list: %d %q", code, out)
	}
	runtimeA(t) // no desk here
	os.MkdirAll(peerDir(), 0o700)
	stale := filepath.Join(peerDir(), "1.sock")
	ln, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	if code, _, errOut := captureA(t, func() int { return peerMain([]string{"list"}) }); code != 1 || !strings.Contains(errOut, "desk is not running") {
		t.Errorf("no desk: %d %q", code, errOut)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a dead desk's socket must go")
	}
}

// --- model.go ---

func TestSetModelA(t *testing.T) {
	m := &model{}
	tab := &Tab{busy: true}
	m.setModel(tab, "opus")
	if m.flash != "Claude is busy; switch models when it's done" || tab.model != "" {
		t.Errorf("busy: %q %q", m.flash, tab.model)
	}
	c, rec := claudeA(t)
	tab.busy, tab.claude = false, c
	m.setModel(tab, "opus")
	if tab.model != "opus" || tab.claude != nil || !rec.closed || lastChatA(tab) != "model: opus (from the next question)" {
		t.Errorf("switch: %+v", tab)
	}
	m.setModel(tab, "")
	if tab.model != "" || lastChatA(tab) != "model: default (from the next question)" {
		t.Errorf("default: %q", lastChatA(tab))
	}
}

func TestCtxTextA(t *testing.T) {
	for _, c := range []struct {
		t    *Tab
		want string
	}{
		{nil, "context: nothing yet"},
		{&Tab{ctxWindow: 100}, "context: nothing yet"},
		{&Tab{ctxTokens: 1500}, "1500 tokens"},
		{&Tab{ctxTokens: 37100, ctxWindow: 1000000}, "37.1k / 1M tokens (3%)"},
		{&Tab{ctxTokens: 150000, ctxWindow: 200000}, "150k / 200k tokens (75%)"},
	} {
		if got := ctxText(c.t); got != c.want {
			t.Errorf("%q, want %q", got, c.want)
		}
	}
}

func TestCompactAndClearContextA(t *testing.T) {
	m := &model{}
	busy := &Tab{busy: true, session: "s"}
	m.compactContext(busy)
	if m.flash != "Claude is busy; compact when it's done" || busy.compacting {
		t.Errorf("busy: %q", m.flash)
	}
	m.compactContext(&Tab{})
	if m.flash != "no conversation to compact yet" {
		t.Errorf("no session: %q", m.flash)
	}
	fakeClaudeBinA(t, "cat <<'EOF'\n{\"type\":\"system\",\"subtype\":\"compact_boundary\",\"compact_metadata\":{\"pre_tokens\":50000,\"post_tokens\":800}}\nEOF\n")
	c, rec := claudeA(t)
	tab := &Tab{dir: t.TempDir(), session: "s1", claude: c, ctxTokens: 50000}
	ch := make(chan claudeMsg, 4)
	setDispatchA(t, func(msg tea.Msg) {
		if cm, ok := msg.(claudeMsg); ok && cm.tab == tab {
			ch <- cm
		}
	})
	m.flash = ""
	m.tabs = []*Tab{tab}
	m.compactContext(tab)
	if !tab.busy || !tab.compacting || tab.claude != nil || !rec.closed || lastChatA(tab) != "compacting the context…" {
		t.Fatalf("compacting: %+v", tab)
	}
	var cm claudeMsg
	select {
	case cm = <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("compact never reported")
	}
	m.update(cm)
	if tab.busy || tab.compacting || tab.ctxTokens != 800 || !strings.Contains(lastChatA(tab), "context compacted: 50k → 800 tokens") {
		t.Errorf("after: busy %v compacting %v tokens %d chat %q", tab.busy, tab.compacting, tab.ctxTokens, lastChatA(tab))
	}
	tab.busy = true
	m.clearContext(tab)
	if m.flash != "Claude is busy; clear when it's done" || tab.session != "s1" {
		t.Errorf("clear while busy: %q", m.flash)
	}
	c2, rec2 := claudeA(t)
	tab.busy, tab.claude = false, c2
	m.clearContext(tab)
	if tab.session != "" || tab.ctxTokens != 0 || tab.claude != nil || !rec2.closed || !strings.Contains(lastChatA(tab), "context cleared") {
		t.Errorf("clear: %+v", tab)
	}
}

func TestPaneHelpersA(t *testing.T) {
	t1, _ := fakeTermA(t, "10.0.0.5")
	t2, _ := fakeTermA(t, "db1")
	p1 := &Pane{id: 1, term: t1, name: "web1", dir: "/d"}
	p2 := &Pane{id: 4, term: t2, name: "db1", claude: true}
	if p1.hostLabel() != "web1 (10.0.0.5)" || p2.hostLabel() != "db1" {
		t.Errorf("labels: %q %q", p1.hostLabel(), p2.hostLabel())
	}
	p1.addr = "192.0.2.1"
	if p1.hostLabel() != "web1 (192.0.2.1)" {
		t.Errorf("addr: %q", p1.hostLabel())
	}
	if n := p1.node(); n != (node{host: "10.0.0.5", dir: "/d", label: "web1", addr: "192.0.2.1"}) {
		t.Errorf("node: %+v", n)
	}
	tab := &Tab{panes: []*Pane{p1, p2}, cur: 1}
	if p, _ := tab.pane(0); p != p2 {
		t.Error("0 is the focused pane")
	}
	if p, _ := tab.pane(1); p != p1 {
		t.Error("by id")
	}
	if tab.paneName(1) != "web1" || tab.paneName(7) != "" {
		t.Error("paneName")
	}
	if d := tab.describePanes(); d != "pane 1 on web1: idle prompt; pane 4 on db1: idle prompt, the user's focus, opened by you" {
		t.Errorf("describe: %q", d)
	}
	if (&Tab{}).connState() != "" {
		t.Error("no pane, no state")
	}
}

func TestDropExitedA(t *testing.T) {
	mk := func(exited, reached, closed bool, lost bool) *Pane {
		term, _ := fakeTermA(t, "h")
		term.exited, term.reached = exited, reached
		p := &Pane{term: term, closed: closed}
		if lost {
			p.lost = time.Now()
		}
		return p
	}
	p1 := mk(true, true, true, false)   // closed: goes
	p2 := mk(false, true, false, false) // alive
	p3 := mk(true, false, false, false) // never connected: stays to say why
	p4 := mk(true, true, false, true)   // redialing: stays
	p5 := mk(true, true, false, false)  // the shell ended: goes
	tab := &Tab{panes: []*Pane{p1, p2, p3, p4, p5}, term: p1.term}
	m := &model{}
	m.dropExited(tab)
	if len(tab.panes) != 3 || tab.panes[0] != p2 || tab.panes[1] != p3 || tab.panes[2] != p4 || tab.cur != 2 || tab.term != p4.term {
		t.Fatalf("kept %d, cur %d", len(tab.panes), tab.cur)
	}
	tab.cur = 0
	m.dropExited(tab) // nothing ended: focus stays
	if tab.cur != 0 || len(tab.panes) != 3 {
		t.Error("nothing to drop")
	}
	for _, p := range tab.panes {
		p.term.exited, p.closed = true, true
	}
	m.dropExited(tab)
	if len(tab.panes) != 0 || tab.term != nil || tab.cur != 0 {
		t.Errorf("all closed: %d", len(tab.panes))
	}
}

func TestUpdateClaudeMsgA(t *testing.T) {
	var notes []string
	defer func(f func(string, string)) { desktopNotify = f }(desktopNotify)
	desktopNotify = func(title, body string) { notes = append(notes, title+": "+body) }
	term, pipe := fakeTermA(t, "web-01")
	c, _ := claudeA(t)
	tab := &Tab{name: "web-01", org: "Acme", term: term, claude: c, panes: []*Pane{{id: 1, term: term, name: "web-01"}}}
	other := &Tab{name: "db"}
	m := model{tabs: []*Tab{other, tab}}
	var blocked []blockedCall
	for _, b := range []struct {
		tool, cmd, reason string
		pane              int
	}{{"mcp__desk__send_keys", "", "", 0}, {"mcp__desk__run", "", "", 0}, {"mcp__desk__run", "rm -rf /tmp/x", "cleanup", 2}} {
		var bc blockedCall
		bc.ToolName, bc.ToolInput.Command, bc.ToolInput.Reason, bc.ToolInput.Pane = b.tool, b.cmd, b.reason, b.pane
		blocked = append(blocked, bc)
	}
	m.update(claudeMsg{tab: tab, ctx: 1200, window: 200000, tool: "read · uptime", steps: []step{{"uptime", "load"}},
		blocked: blocked, err: errors.New("boom"), session: "sess-2"})
	if tab.ctxTokens != 1200 || tab.ctxWindow != 200000 || tab.session != "sess-2" {
		t.Errorf("ctx %d window %d session %q", tab.ctxTokens, tab.ctxWindow, tab.session)
	}
	var who []string
	for _, e := range tab.chat {
		who = append(who, e.who+":"+e.text)
	}
	if strings.Join(who, "|") != "tool:read · uptime|error:blocked by Claude Code's classifier: mcp__desk__send_keys|error:blocked by Claude Code's classifier: mcp__desk__run|error:boom" ||
		len(tab.chat[0].steps) != 1 {
		t.Errorf("chat: %q", who)
	}
	if len(tab.pending) != 1 || tab.pending[0].cmd != "rm -rf /tmp/x" || tab.pending[0].reason != "cleanup" || tab.pending[0].pane != 2 || !tab.pending[0].blocked ||
		len(notes) != 1 || notes[0] != "Approval needed · web-01: rm -rf /tmp/x" {
		t.Errorf("blocked run: %+v, notes %q", tab.pending, notes)
	}
	m.update(claudeMsg{tab: tab, text: "check:\n```sh\nsystemctl status nginx\n```"})
	if got := readPipeA(pipe); got != "\x05\x15systemctl status nginx" || tab.suggest != "" || !strings.HasPrefix(lastChatA(tab), "↳ typed at your prompt") {
		t.Errorf("one-line suggestion: typed %q, suggest %q", got, tab.suggest)
	}
	m.update(claudeMsg{tab: tab, text: "```sh\ncd /srv\nls\n```"})
	if tab.suggest != "cd /srv\nls" || lastChatA(tab) != "```sh\ncd /srv\nls\n```" {
		t.Errorf("multi-line suggestion stays: %q", tab.suggest)
	}
	tab.health, tab.busy = true, true
	m.update(claudeMsg{tab: tab, done: true})
	if tab.health || tab.busy || notes[len(notes)-1] != "Daily check ready · Acme: ```sh\ncd /srv\nls\n```" {
		t.Errorf("health: %q", notes)
	}
	n := len(tab.chat)
	m.update(claudeMsg{tab: tab, done: true, exited: &Claude{}, err: errors.New("old")})
	if len(tab.chat) != n || tab.claude != c {
		t.Error("an older process's end is no news")
	}
	m.update(claudeMsg{tab: tab, done: true, exited: c})
	if tab.claude != nil || tab.session != "" || !strings.HasPrefix(lastChatA(tab), "Claude starts again") {
		t.Errorf("exit: %q", lastChatA(tab))
	}
}

func TestUpdateMiscMsgsA(t *testing.T) {
	var notes []string
	defer func(f func(string, string)) { desktopNotify = f }(desktopNotify)
	desktopNotify = func(title, body string) { notes = append(notes, title+": "+body) }
	term, _ := fakeTermA(t, "web1")
	c, rec := claudeA(t)
	tab := &Tab{name: "web1", org: "Acme", term: term, claude: c, panes: []*Pane{{id: 1, term: term, name: "web1"}}}
	m := model{tabs: []*Tab{tab}}
	m.update(followUpMsg{tab, "(desk) ran it"})
	if !tab.busy || !strings.Contains(rec.String(), "(desk) ran it") {
		t.Error("follow-up goes to Claude")
	}
	m.update(blockDoneMsg{tab, "web1", Block{Cmd: "make", Exit: 2, Dur: 3 * time.Second}})
	if len(notes) != 0 {
		t.Errorf("the pane in view needs no notification: %q", notes)
	}
	m.update(blockDoneMsg{tab, "db1", Block{Cmd: "make", Exit: 2, Dur: 3 * time.Second}})
	m.away = true
	m.update(blockDoneMsg{tab, "web1", Block{Cmd: "make", Dur: 2500 * time.Millisecond}})
	if len(notes) != 2 || notes[0] != "Command finished · db1: ✗ exit 2 make (3s)" || notes[1] != "Command finished · web1: ✓ make (3s)" {
		t.Errorf("notes: %q", notes)
	}
	m.away = false
	m.update(approvalMsg{tab, &approval{cmd: "x"}})
	if len(tab.pending) != 1 || len(notes) != 2 {
		t.Error("an approval in view: pending, no notification")
	}
	tab.pending = nil
	reply := make(chan peerResp, 1)
	m.update(peerMsg{peerReq{Op: "list"}, reply})
	if r := <-reply; !strings.HasPrefix(r.Text, "desk ") {
		t.Errorf("peer: %q", r.Text)
	}
	q1 := &question{title: "passphrase", done: make(chan string, 1)}
	r, _ := m.update(questionMsg{q1})
	m = r.(model)
	q1.text = "s3cret"
	r, _ = m.update(keyA("x"))
	m = r.(model)
	if len(m.asks) != 1 {
		t.Fatal("other keys leave the question")
	}
	r, _ = m.update(keyA("enter"))
	m = r.(model)
	if s, ok := <-q1.done; !ok || s != "s3cret" || len(m.asks) != 0 {
		t.Errorf("enter answers: %q %v", s, ok)
	}
	q2 := &question{done: make(chan string, 1)}
	r, _ = m.update(questionMsg{q2})
	m = r.(model)
	r, _ = m.update(keyA("esc"))
	m = r.(model)
	if _, ok := <-q2.done; ok || len(m.asks) != 0 {
		t.Error("esc cancels")
	}
}

func TestKeyAnswerA(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	term, _ := fakeTermA(t, "web1")
	tab := &Tab{org: "AnsOrg", term: term, panes: []*Pane{{id: 1, term: term, name: "web1"}}}
	m := model{tabs: []*Tab{tab}}
	press := func(k string) tea.Cmd {
		r, cmd := m.update(keyA(k))
		m = r.(model)
		return cmd
	}
	a := &approval{cmd: "systemctl restart nginx", done: make(chan approvalResult, 1)}
	tab.pending = []*approval{a}
	press("alt+x")
	press("z")
	if len(tab.pending) != 1 || a.editing {
		t.Fatal("alt keys and others leave the approval")
	}
	press("e")
	if !a.editing || a.edit != a.cmd {
		t.Fatal("e edits")
	}
	a.edit = "systemctl reload nginx"
	press("y") // typing while editing is the editor's
	press("enter")
	if a.cmd != "systemctl reload nginx" || a.editing || len(tab.pending) != 1 {
		t.Fatalf("edit taken: %+v", a)
	}
	press("e")
	a.edit = "x"
	press("esc")
	if a.editing || a.cmd != "systemctl reload nginx" {
		t.Fatal("esc drops the edit")
	}
	press("y")
	if res := <-a.done; !res.ok || res.cmd != "systemctl reload nginx" || len(tab.pending) != 0 || lastChatA(tab) != "✓ approved: systemctl reload nginx" {
		t.Errorf("approved: %+v %q", res, lastChatA(tab))
	}
	a2 := &approval{cmd: "uptime", done: make(chan approvalResult, 1)}
	tab.pending = []*approval{a2}
	press("a")
	if res := <-a2.done; !res.ok || !always.has("AnsOrg", "run:uptime") || lastChatA(tab) != "✓ always allowed (this org): uptime" {
		t.Errorf("always: %+v %q", res, lastChatA(tab))
	}
	a3 := &approval{cmd: "pb · db", secret: true, done: make(chan approvalResult, 1)}
	tab.pending = []*approval{a3}
	press("a")
	press("e")
	if len(tab.pending) != 1 || a3.editing {
		t.Fatal("a secret has no always and no edit")
	}
	press("n")
	if res := <-a3.done; res.ok || lastChatA(tab) != "✗ denied: pb · db" || always.has("AnsOrg", "run:pb · db") {
		t.Errorf("denied: %+v", res)
	}
	a4 := &approval{cmd: "rm x", blocked: true, done: make(chan approvalResult, 1)}
	tab.pending = []*approval{a4}
	cmd := press("esc")
	if fu, ok := cmd().(followUpMsg); !ok || fu.tab != tab || !strings.Contains(fu.text, "blocked `rm x` and the user also declined it") {
		t.Errorf("declined follow-up: %+v", cmd())
	}
	a5 := &approval{cmd: "rm y", blocked: true, pane: 9, done: make(chan approvalResult, 1)}
	tab.pending = []*approval{a5}
	cmd = press("enter")
	if fu := cmd().(followUpMsg); !strings.Contains(fu.text, "the user approved it but desk could not run it: there is no pane 9") {
		t.Errorf("approved, no pane: %q", fu.text)
	}
}

func TestKeyConnStateA(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	term, _ := fakeTermA(t, "local")
	term.b = nil
	p := &Pane{id: 1, term: term, name: "local", dir: t.TempDir(), waitUntil: time.Now().Add(-time.Second)}
	tab := &Tab{term: term, panes: []*Pane{p}}
	m := model{tabs: []*Tab{tab}}
	press := func(k string) { r, _ := m.update(keyA(k)); m = r.(model) }
	if tab.connState() != "timeout" {
		t.Fatalf("state %q", tab.connState())
	}
	press("r") // only for failed or reconnecting
	if p.term != term {
		t.Fatal("r redials only a failed pane")
	}
	press("w")
	if tab.connState() != "" || time.Until(p.waitUntil) < connectWait-time.Second {
		t.Errorf("keep waiting: %q", tab.connState())
	}
	term.exited = true
	if tab.connState() != "failed" {
		t.Fatalf("state %q", tab.connState())
	}
	press("w")
	press("r")
	if p.term == term || tab.term != p.term || term.hup != true || time.Until(p.waitUntil) < connectWait-time.Second {
		t.Fatal("r starts the connection over")
	}
	p.term.Close()
	p.term.exited, p.term.reached = true, false
	for i := 0; i < 250 && tab.connState() != "failed"; i++ { // up to 5 s: 1 s ran out on a busy CI runner
		time.Sleep(20 * time.Millisecond)
	}
	press("c")
	if len(m.tabs) != 0 {
		t.Error("c closes the failed pane and its host tab")
	}
}

func TestKeyShortcutsA(t *testing.T) {
	t1, pipe := fakeTermA(t, "web1")
	t2, _ := fakeTermA(t, "web1")
	p1, p2 := &Pane{id: 1, term: t1, name: "web1"}, &Pane{id: 2, term: t2, name: "web1"}
	t1.reached, t2.reached = true, true
	tab := &Tab{term: t1, panes: []*Pane{p1, p2}, suggest: "ls -la"}
	m := model{tabs: []*Tab{tab}}
	press := func(k string) { r, _ := m.update(keyA(k)); m = r.(model) }
	press("ctrl+y")
	if got := readPipeA(pipe); got != "\x05\x15ls -la" || tab.suggest != "" {
		t.Errorf("ctrl+y: %q", got)
	}
	press("ctrl+s")
	if !tab.pinScreen {
		t.Error("ctrl+s pins the screen")
	}
	press("ctrl+s")
	if tab.pinScreen {
		t.Error("ctrl+s again unpins")
	}
	press("ctrl+w")
	if len(m.tabs) != 1 || len(tab.panes) != 1 || tab.panes[0] != p2 || tab.term != t2 {
		t.Fatalf("ctrl+w closes the pane: %d panes", len(tab.panes))
	}
	press("ctrl+w")
	if len(m.tabs) != 0 {
		t.Error("ctrl+w on a host tab's last pane closes the tab")
	}
	t3, _ := fakeTermA(t, "web1")
	t3.reached = true
	org := &Tab{orgOnly: true, term: t3, panes: []*Pane{{id: 1, term: t3}}}
	m.tabs = []*Tab{org}
	press("ctrl+w")
	if len(m.tabs) != 1 || len(org.panes) != 0 || org.term != nil {
		t.Error("an org tab stays without panes")
	}
	m.group = "g"
	press("enter")
	if m.group != "" {
		t.Error("enter with nothing typed leaves the group view")
	}
	m.pal = true
	press("esc")
	if m.pal {
		t.Error("esc closes the palette")
	}
}

func treeA(dir string) []node {
	return []node{{depth: 0, label: "Acme", org: "Acme", dir: dir}, {depth: 1, label: "g", org: "Acme", group: "g"},
		{depth: 2, label: "web1", org: "Acme", group: "g", dir: dir, host: "web1"},
		{depth: 1, label: "empty", org: "Acme", group: "empty"}}
}

func labelsA(items []palItem) map[string]palItem {
	out := map[string]palItem{}
	for _, it := range items {
		out[it.label] = it
	}
	return out
}

func TestPalItemsA(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Acme", infraDir)
	write(t, filepath.Join(dir, "runbooks", "nginx.md"), "intro\n# Restart nginx\n")
	term, _ := fakeTermA(t, "web1")
	org := &Tab{org: "Acme", dir: dir, orgOnly: true, pending: []*approval{{}}}
	host := &Tab{org: "Acme", group: "g", name: "web1", dir: dir, term: term, panes: []*Pane{{id: 1, term: term, name: "web1", dir: dir}}}
	m := &model{tree: treeA(dir), tabs: []*Tab{org, host}, active: 1}
	items := m.palItems()
	var kinds []string
	for _, it := range items {
		kinds = append(kinds, it.kind+":"+it.label+":"+it.hint)
	}
	if strings.Join(kinds, "|") != "sessions:Acme › Claude:▲ needs approval|sessions:Acme › g › web1:tab 2|runbooks:Restart nginx:run with Claude · nginx.md|"+
		"orgs:Acme:Claude for the org|groups:Acme › g:group view|hosts:Acme › g › web1:open ssh tab" {
		t.Errorf("no query:\n%s", strings.Join(kinds, "\n"))
	}
	m.palQ = "model"
	items = m.palItems()
	all := labelsA(items)
	if !strings.HasPrefix(items[0].label, "model: ") || all["model: opus"].kind != "actions" || all["model: default"].hint != "Claude model for this tab" {
		t.Errorf("model query: %+v", items[:3])
	}
	m.palQ = "forward 5432"
	if f := labelsA(m.palItems())["forward 5432"].fwd; f.host != "web1" || f.kind != "L" || f.spec != "5432:localhost:5432" || f.dir != dir {
		t.Errorf("forward: %+v", f)
	}
	m.palQ = "theme"
	if it := labelsA(m.palItems())["theme: "+themeName]; it.hint != "current theme" || it.theme != themeName {
		t.Errorf("current theme: %+v", it)
	}
	m.forwards = []portFwd{{host: "web1", kind: "L", spec: "5432:localhost:5432"}}
	m.palQ = "stop"
	if it, ok := labelsA(m.palItems())["stop forward "+m.forwards[0].String()]; !ok || it.fwd != m.forwards[0] {
		t.Error("a forward can be stopped")
	}
}

func TestPalKeyA(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "Acme", infraDir)
	write(t, filepath.Join(dir, "runbooks", "nginx.md"), "# Restart nginx\n```sh\nsystemctl restart {{svc|nginx}}\n```\n")
	c, rec := claudeA(t)
	org := &Tab{org: "Acme", dir: dir, orgOnly: true, claude: c, pinned: map[int]bool{}}
	host := &Tab{org: "Acme", group: "g", name: "web1", dir: dir, chat: []*chatEntry{{who: "claude", text: "x", mdW: 80}}}
	m := &model{tree: treeA(dir), tabs: []*Tab{org, host}, active: 1}
	pick := func(q, label string) {
		t.Helper()
		m.palQ, m.pal = q, true
		for i, it := range m.palItems() {
			if it.label == label {
				m.palSel = i
				m.palKey(keyA("enter"))
				return
			}
		}
		t.Fatalf("no %q for %q", label, q)
	}
	m.palQ, m.pal, m.palSel = "", true, 99
	if m.palKey(keyA("enter")) != nil || !m.pal {
		t.Error("enter past the items does nothing")
	}
	pick("", "Acme › Claude")
	if m.active != 0 || m.pal {
		t.Errorf("session: active %d", m.active)
	}
	pick("", "Acme › g")
	if m.group != groupKey(dir, "g") {
		t.Errorf("group: %q", m.group)
	}
	m.active = 1
	pick("", "Acme")
	if m.active != 0 || m.group != "" || len(m.tabs) != 2 {
		t.Errorf("org goes back to its tab: active %d tabs %d", m.active, len(m.tabs))
	}
	pick("haiku", "model: haiku")
	if org.model != "haiku" {
		t.Errorf("model: %q", org.model)
	}
	pick("default", "model: default")
	if org.model != "" {
		t.Errorf("default model: %q", org.model)
	}
	pick("broadcast", "broadcast typing to all panes")
	if m.flash != "broadcast needs two or more panes ("+modText("T")+" opens one)" {
		t.Errorf("broadcast: %q", m.flash)
	}
	pick("forward", "forward PORT[:HOST]:PORT")
	if !strings.HasPrefix(m.flash, "type the ports") {
		t.Errorf("forward hint: %q", m.flash)
	}
	org.busy = true
	pick("postmortem", "draft a postmortem with Claude")
	if m.flash != "Claude is busy" {
		t.Errorf("postmortem: %q", m.flash)
	}
	pick("compact", "compact Claude's context")
	if m.flash != "Claude is busy; compact when it's done" {
		t.Errorf("compact: %q", m.flash)
	}
	org.busy = false
	org.session = "s"
	pick("clear", "clear Claude's context")
	if org.session != "" || !strings.Contains(lastChatA(org), "context cleared") {
		t.Errorf("clear: %q", lastChatA(org))
	}
	org.claude = c
	pick("Restart", "Restart nginx")
	if !strings.Contains(rec.String(), "runbook\\u003e\\n# Restart nginx") || !strings.Contains(rec.String(), "Its parameters are svc") || lastChatA(org) != "▶ runbook: Restart nginx" {
		t.Errorf("runbook: %q", lastChatA(org))
	}
	host.hosts = nil
	pick("reload", "reload config")
	if m.flash != "config reloaded" || len(host.hosts) != 1 || host.hosts[0].label != "web1" {
		t.Errorf("reload: %q %v", m.flash, host.hosts)
	}
	old := themeName
	defer setTheme(old)
	var other string
	for n := range themes {
		if n != old {
			other = n
			break
		}
	}
	pick("theme", "theme: "+other)
	if themeName != other || m.flash != "theme: "+other || host.chat[0].mdW != 0 {
		t.Errorf("theme: %q %q", themeName, m.flash)
	}
}

func TestGroupAskSideEnterA(t *testing.T) {
	dir := t.TempDir()
	m := &model{tree: treeA(dir)}
	if m.groupDir(1) != groupKey(dir, "g") || m.groupDir(3) != "" {
		t.Errorf("groupDir: %q %q", m.groupDir(1), m.groupDir(3))
	}
	m.cursor = 1
	m.sideEnter()
	if m.group != groupKey(dir, "g") {
		t.Errorf("group row: %q", m.group)
	}
	m.cursor = 0
	m.sideEnter()
	if len(m.tabs) != 1 || !m.tabs[0].orgOnly {
		t.Fatal("org row opens its Claude")
	}
	c, rec := claudeA(t)
	t1 := &Tab{dir: dir, group: "g", claude: c, pinned: map[int]bool{}}
	t2 := &Tab{dir: dir, group: "g", busy: true}
	t3 := &Tab{dir: dir, group: "other"}
	m.tabs, m.active = []*Tab{t3, t1, t2}, 0
	m.group, m.input = groupKey(dir, "g"), "disk?"
	m.groupAsk()
	if !strings.Contains(rec.String(), "disk?") || len(t2.asked) != 1 || t2.asked[0] != "disk?" || len(t3.chat) != 0 || m.input != "" || m.active != 0 {
		t.Errorf("group ask: asked %q, active %d", t2.asked, m.active)
	}
	if m.approvalTab() != nil {
		t.Error("no approvals yet")
	}
	t2.pending = []*approval{{}}
	if m.approvalTab() != t2 {
		t.Error("the group view shows its sessions' approvals")
	}
	m.group = ""
	if m.approvalTab() != nil {
		t.Error("outside the group view only the active tab's")
	}
	t3.pending = []*approval{{}}
	if m.approvalTab() != t3 {
		t.Error("the active tab's approval")
	}
	m.input = "  "
	m.group = "x"
	m.groupAsk()
	if m.group != "" {
		t.Error("nothing typed leaves the group view")
	}
}

func TestCloseTabHubA(t *testing.T) {
	c, rec := claudeA(t)
	a := &approval{done: make(chan approvalResult, 1)}
	tab := &Tab{org: "Acme", claude: c, pending: []*approval{a}}
	first, last := &Tab{}, &Tab{}
	k := &peerTask{id: 9001, from: "close-peer", tab: tab}
	hub.Lock()
	hub.tasks[9001], hub.running[tab], hub.last[tab] = k, k, "close-peer"
	hub.Unlock()
	m := &model{tabs: []*Tab{first, tab, last}, active: 2, group: "g"}
	m.closeTab(1)
	if len(m.tabs) != 2 || m.active != 1 || m.group != "g" || !rec.closed {
		t.Errorf("tabs %d active %d", len(m.tabs), m.active)
	}
	if res := <-a.done; res.ok {
		t.Error("a waiting approval is denied")
	}
	hub.Lock()
	_, task := hub.tasks[9001]
	_, run := hub.running[tab]
	_, lastPeer := hub.last[tab]
	hub.Unlock()
	if task || run || lastPeer {
		t.Error("the tab's hub state must go")
	}
	if evs := waitEvents("close-peer", time.Second); len(evs) != 1 || evs[0].Kind != "result" || evs[0].Text != "aborted: the user closed the session" {
		t.Errorf("peer told: %+v", evs)
	}
	m.active = 0
	m.closeTab(1)
	m.closeTab(0)
	if len(m.tabs) != 0 || m.active != 0 || m.group != "" {
		t.Errorf("none left: active %d group %q", m.active, m.group)
	}
}

func TestRunRunbookStopA(t *testing.T) {
	m := &model{}
	m.runRunbook(runbook{title: "x"}) // no tab: nothing
	c, rec := claudeA(t)
	tab := &Tab{claude: c, busy: true}
	m.tabs = []*Tab{tab}
	p := filepath.Join(t.TempDir(), "rb.md")
	write(t, p, "# Deploy\n")
	m.runRunbook(runbook{"Deploy", p})
	if lastChatA(tab) != "Claude is busy; start the runbook when it's done (or stop it)" || rec.String() != "" {
		t.Errorf("busy: %q", lastChatA(tab))
	}
	tab.busy = false
	m.runRunbook(runbook{"Gone", p + ".missing"})
	if tab.chat[len(tab.chat)-1].who != "error" || rec.String() != "" {
		t.Errorf("missing file: %q", lastChatA(tab))
	}
	m.stop(tab) // not busy: nothing
	if strings.Contains(rec.String(), "interrupt") {
		t.Error("stop needs a running turn")
	}
	a := &approval{done: make(chan approvalResult, 1)}
	tab.busy, tab.pending = true, []*approval{a}
	rec.fail = true
	m.stop(tab)
	if lastChatA(tab) != "broken pipe" || len(tab.pending) != 1 {
		t.Errorf("a failed interrupt leaves the approvals: %q", lastChatA(tab))
	}
}

func TestSendPromptA(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := &model{}
	c, rec := claudeA(t)
	org := &Tab{org: "SendOrg", orgOnly: true, claude: c, peerCtx: []string{`<peer from="x">ctx</peer>`}}
	e := &chatEntry{who: "you", text: "hi"}
	if err := m.send(org, "hi", e); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	json.Unmarshal(rec.buf.Bytes(), &sent)
	if sent.Message.Content != "(chat panel: 90 columns)\n<peer from=\"x\">ctx</peer>\n<panes>none open</panes>\nhi" ||
		org.peerCtx != nil || !org.busy || lastChatA(org) != "hi" || e.screen {
		t.Errorf("org prompt: %q", sent.Message.Content)
	}
	addNote("SendOrg", "web1", "uses /srv")
	t1, _ := fakeTermA(t, "web1")
	t2, _ := fakeTermA(t, "web1")
	t1.blocks = []Block{{N: 1, Cmd: "ls", Output: "a\nb"}, {N: 2, Cmd: "df"}}
	c2, rec2 := claudeA(t)
	tab := &Tab{org: "SendOrg", claude: c2, term: t1, pinScreen: true, pinned: map[int]bool{},
		panes: []*Pane{{id: 1, term: t1, name: "web1"}, {id: 2, term: t2, name: "web1"}}}
	e = &chatEntry{who: "you"}
	if err := m.send(tab, "#1 why? TOKEN=abc", e); err != nil {
		t.Fatal(err)
	}
	rec2.mu.Lock()
	json.Unmarshal(rec2.buf.Bytes(), &sent)
	rec2.mu.Unlock()
	p := sent.Message.Content
	for _, want := range []string{"uses /srv", "<panes>pane 1 on web1: idle prompt, the user's focus; pane 2 on web1: idle prompt</panes>\nBlocks and screen are from pane 1.\n",
		"<block n=1 exit=0 by=you>\n$ ls\na\nb\n</block>", "<screen alt=false", "#1 why? TOKEN=[redacted]"} {
		if !strings.Contains(p, want) {
			t.Errorf("missing %q in:\n%s", want, p)
		}
	}
	if strings.Count(p, "uses /srv") != 1 || !e.screen || len(e.ctx) != 1 || e.ctx[0] != 1 || e.lines != 3 || !tab.lastCtx[1] || tab.lastCtx[2] {
		t.Errorf("entry: %+v, lastCtx %v", e, tab.lastCtx)
	}
	rec2.fail = true
	n := len(tab.chat)
	if err := m.send(tab, "again", &chatEntry{}); err == nil || len(tab.chat) != n {
		t.Error("a failed send shows nothing")
	}
}

func TestSendStartsClaudeA(t *testing.T) {
	noUnix(t)
	runtimeA(t)
	stdin := filepath.Join(t.TempDir(), "stdin")
	args := fakeClaudeBinA(t, "cat > "+stdin+"\n")
	exited := make(chan struct{})
	setDispatchA(t, func(msg tea.Msg) {
		if cm, ok := msg.(claudeMsg); ok && cm.exited != nil {
			close(exited)
		}
	})
	term, _ := fakeTermA(t, "web1")
	tab := &Tab{org: "Acme", dir: t.TempDir(), orgCfg: &orgConfig{}, term: term, pinned: map[int]bool{},
		notesSent: map[string]bool{"old": true}, panes: []*Pane{{id: 1, term: term, name: "web1", addr: "192.0.2.10"}}}
	m := &model{}
	if err := m.send(tab, "uptime?", &chatEntry{}); err != nil {
		t.Fatal(err)
	}
	defer func() { // its end reaches dispatch: wait for it while ours is set
		tab.claude.Close()
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			t.Error("claude never exited")
		}
	}()
	if tab.claude == nil || tab.notesSent["old"] {
		t.Error("a new process starts with no notes sent")
	}
	for i := 0; i < 100; i++ {
		if b, _ := os.ReadFile(stdin); strings.Contains(string(b), "uptime?") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if b, _ := os.ReadFile(stdin); !strings.Contains(string(b), "uptime?") {
		t.Errorf("stdin: %q", b)
	}
	if b, _ := os.ReadFile(args); !strings.Contains(string(b), `host "web1 (192.0.2.10)"`) {
		t.Errorf("a host tab's Claude is told its server: %s", b)
	}
	t.Setenv("PATH", t.TempDir())
	tab2 := &Tab{org: "Acme", dir: t.TempDir(), orgCfg: &orgConfig{}, orgOnly: true}
	if err := m.send(tab2, "x", &chatEntry{}); err == nil || tab2.claude != nil || tab2.busy {
		t.Errorf("no claude: %v", err)
	}
}

func TestRedialGiveUpA(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	lost, _ := fakeTermA(t, "local")
	lost.emu.WriteString("before the drop")
	lost.exited, lost.code, lost.dropped, lost.reached, lost.b = true, 255, true, true, nil
	p := &Pane{id: 1, term: lost, name: "local", dir: dir}
	tab := &Tab{term: lost, panes: []*Pane{p}}
	mine, other := portFwd{host: "local", dir: dir, kind: "L", spec: "1:localhost:1"}, portFwd{host: "web2", dir: dir, kind: "L", spec: "1:localhost:1"}
	m := &model{tabs: []*Tab{tab}, fwdOn: map[portFwd]bool{mine: true, other: true}}
	m.redial()
	defer func() { p.term.Close() }()
	if p.lost.IsZero() || m.fwdOn[mine] || !m.fwdOn[other] || !strings.Contains(p.kept.Text, "before the drop") ||
		!strings.Contains(p.kept.Text, "── connection lost at ") {
		t.Fatalf("lost: %+v %v", p.kept, m.fwdOn)
	}
	if p.term == lost || p.tries != 1 || time.Until(p.retry) < 3*time.Second || tab.term != p.term {
		t.Errorf("redialed once, backing off: tries %d retry in %s", p.tries, time.Until(p.retry))
	}
	retry, cur := p.retry, p.term
	m.redial() // before retry: nothing
	if p.term != cur || p.retry != retry {
		t.Error("waits for its retry time")
	}
	cur.Close()
	gone, _ := fakeTermA(t, "local")
	gone.exited, gone.code = true, 255 // a redial that never got through
	p.term, p.lost = gone, time.Now().Add(-redialFor-time.Second)
	m.redial()
	if !p.lost.IsZero() || p.connState() != "failed" {
		t.Errorf("gives up after redialFor: %q", p.connState())
	}
	back, _ := fakeTermA(t, "local")
	back.reached = true
	p.term, p.lost, p.kept = back, time.Now(), paneState{Text: "x"}
	m.redial()
	if !p.lost.IsZero() || p.kept.Text != "" {
		t.Error("a link that came back clears the redial")
	}
	p.term = gone // for the deferred Close
}
