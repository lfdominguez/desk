package infra

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/lfdominguez/desk/internal/claude"
	deskorgs "github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/sshc"
	"github.com/lfdominguez/desk/internal/sshconfig"
)

// TestKeepBackwardsBlock: a block whose lines run backwards (the screen
// cleared under it) is left out of what a pane keeps, not a crash.
func TestKeepBackwardsBlock(t *testing.T) {
	term := &Term{emu: vt.NewEmulator(20, 4)}
	term.emu.WriteString("a\r\nb\r\nc\r\nd")
	term.blocks = []Block{{N: 1, Cmd: "clear", Start: 9, End: 2}, {N: 2, Cmd: "ls", Start: 1, End: 3}}
	if st := term.Keep(); len(st.Blocks) != 1 || st.Blocks[0].Cmd != "ls" {
		t.Fatalf("%+v", st.Blocks)
	}
}

func TestBlocksFromOSC133(t *testing.T) {
	term := &Term{emu: vt.NewEmulator(20, 4)}
	go func() {
		b := make([]byte, 64)
		for {
			term.emu.Read(b)
		}
	}()
	term.emu.RegisterOscHandler(133, term.osc133)
	// 8 output lines on a 4-row screen: the block spans scrollback and screen.
	term.emu.WriteString("$ \x1b]133;B\x07seq 8\r\n\x1b]133;C\x071\r\n2\r\n3\r\n4\r\n5\r\n6\r\n7\r\n8\r\n\x1b]133;D;0\x07" +
		"$ \x1b]133;B\x07\r\n\x1b]133;C\x07\x1b]133;D;0\x07" + // empty enter: no block
		"$ \x1b]133;B\x07false\r\n\x1b]133;C\x07\x1b]133;D;1\x07$ ")
	bs := term.blocks
	if len(bs) != 2 {
		t.Fatalf("want 2 blocks, got %+v", bs)
	}
	if bs[0].Cmd != "seq 8" || bs[0].Output != "1\n2\n3\n4\n5\n6\n7\n8" || bs[0].Exit != 0 {
		t.Errorf("block 1: %+v", bs[0])
	}
	if bs[1].Cmd != "false" || bs[1].Output != "" || bs[1].Exit != 1 || bs[1].N != 2 {
		t.Errorf("block 2: %+v", bs[1])
	}
}

// TestLoadTreeEmpty: orgs without a servers file have no servers, the old
// Infra-Claude/<group>/ folders included.
func TestLoadTreeEmpty(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "Other", "src"), 0o755)
	os.MkdirAll(filepath.Join(root, "Acme", oldInfraDir, "prod"), 0o755)
	os.WriteFile(filepath.Join(root, "Acme", oldInfraDir, "prod", "hosts"), []byte("db1\n"), 0o644)
	if tr := loadTree(root); len(tr) != 0 {
		t.Errorf("tree: %+v", tr)
	}
	if !fileExists(filepath.Join(root, "Acme", oldInfraDir)) {
		t.Error("an org without a servers file keeps its old folder")
	}
}

// TestInfraMoves: an org's old Infra-Claude folder moves to .desk/infra once,
// with what it holds; an existing .desk/infra is left alone.
func TestInfraMoves(t *testing.T) {
	org := filepath.Join(t.TempDir(), "Acme")
	write(t, filepath.Join(org, oldInfraDir, "CLAUDE.md"), "notes")
	write(t, filepath.Join(org, ".desk", "ssh_config"), "Host web1\n")
	dir := makeInfra(org)
	if dir != filepath.Join(org, ".desk", "infra") || !isInfraDir(dir) || orgOfInfra(dir) != org {
		t.Fatalf("infra dir %s", dir)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md")); string(b) != "notes" || fileExists(filepath.Join(org, oldInfraDir)) {
		t.Errorf("not moved: %q", b)
	}
	write(t, filepath.Join(org, oldInfraDir, "CLAUDE.md"), "again")
	makeInfra(org)
	if b, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md")); string(b) != "notes" || !fileExists(filepath.Join(org, oldInfraDir)) {
		t.Errorf("moved over the new one: %q", b)
	}
	other := filepath.Join(t.TempDir(), "Globex")
	os.MkdirAll(filepath.Join(other, infraDir), 0o755) // made already, still empty
	os.MkdirAll(filepath.Join(other, oldInfraDir), 0o755)
	makeInfra(other)
	if !fileExists(filepath.Join(other, oldInfraDir)) {
		t.Error("an existing infra dir, even empty, takes no move")
	}
	if isInfraDir(filepath.Join(org, "infra")) || isInfraDir(filepath.Join(org, ".desk")) {
		t.Error("only .desk/infra is an infra dir")
	}
	for old, want := range map[string]string{
		filepath.Join(org, oldInfraDir):       dir,
		filepath.Join(org, oldInfraDir, "eu"): dir,
		dir:                                   dir,
		filepath.Join(org, "src", oldInfraDir+"x"): filepath.Join(org, "src", oldInfraDir+"x"),
	} {
		if got := movedInfra(old); got != want {
			t.Errorf("movedInfra(%s) = %s, want %s", old, got, want)
		}
	}
}

// TestRestoreOldInfraDirs: a session saved with Infra-Claude dirs comes back
// in the org's infra dir.
func TestRestoreOldInfraDirs(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	org := filepath.Join(t.TempDir(), "Acme")
	b, _ := json.Marshal(savedSession{Tabs: []savedTab{{Org: "Acme", Dir: filepath.Join(org, oldInfraDir, "eu"), OrgOnly: true}},
		Forwards: []savedFwd{{Host: "web1", Dir: filepath.Join(org, oldInfraDir), Kind: "L", Spec: "1:localhost:1"}}})
	write(t, sessionPath(), string(b))
	m := model{}
	m.restore()
	want := filepath.Join(org, infraDir)
	if len(m.tabs) != 1 || m.tabs[0].dir != want || len(m.forwards) != 1 || m.forwards[0].dir != want {
		t.Fatalf("restored: %d tabs, %+v", len(m.tabs), m.forwards)
	}
}

func TestProfileFor(t *testing.T) {
	t.Setenv("CLAUDE_PROFILE", "")
	home := t.TempDir()
	t.Setenv("DESK_ROOT", filepath.Join(home, "Work"))
	os.MkdirAll(filepath.Join(home, "Secure/Claudes/Company"), 0o755)
	for dir, want := range map[string]string{
		"Work/Company/.desk/infra":    "Company",
		"Work/Company":                "Company",
		"Work/Personal/.desk/infra":   "",
		"Work/Companyish/.desk/infra": "",
		"Other/Company":               "",
	} {
		name, cfg, js := profileFor(home, filepath.Join(home, dir))
		if name != want {
			t.Errorf("%s: got %q want %q", dir, name, want)
		}
		if want == "Company" && (cfg != filepath.Join(home, "Secure/Claudes/Company") || js != filepath.Join(home, "Secure/Claudes/claude-company.json")) {
			t.Errorf("%s: cfg %s json %s", dir, cfg, js)
		}
	}
}

func TestReferenced(t *testing.T) {
	bs := []Block{{N: 1}, {N: 2}, {N: 3}, {N: 4}}
	ns := func(q string) (s string) {
		pin := map[int]bool{}
		if strings.HasPrefix(q, "pin1 ") {
			pin[1] = true
		}
		for _, b := range referenced(q, bs, pin) {
			s += string(rune('0' + b.N))
		}
		return
	}
	for q, want := range map[string]string{
		"read the last 3 prompts": "234",
		"lee los últimos 2":       "34",
		"what failed in #1?":      "1",
		"#1 and #3":               "13",
		"why?":                    "234",
		"pin1 and #3":             "13",
	} {
		if got := ns(q); got != want {
			t.Errorf("%q: got %s want %s", q, got, want)
		}
	}
	if s := suggestion("try:\n```sh\nsudo nginx -t\n```\n"); s != "sudo nginx -t" {
		t.Errorf("suggestion: %q", s)
	}
	bs2 := []Block{{N: 1, Cmd: "ls", Output: "a"}, {N: 2, Cmd: "df"}}
	if p := buildPrompt("#1", "h", bs2, bs2[:1], nil); !strings.Contains(p, "<block n=1 exit=0 by=you>\n$ ls\na\n</block>") ||
		!strings.Contains(p, "#2 exit=0 by=you $ df  (output omitted") || strings.Contains(p, "<block n=2") {
		t.Errorf("prompt: %s", p)
	}
}

func TestScreenSnapshot(t *testing.T) {
	term := &Term{emu: vt.NewEmulator(30, 4)}
	go func() {
		b := make([]byte, 64)
		for {
			term.emu.Read(b)
		}
	}()
	term.emu.RegisterOscHandler(133, term.osc133)
	term.emu.WriteString("$ \x1b]133;B\x07htop\r\n\x1b]133;C\x07\x1b[?1049h\x1b[H  PID USER  CPU%\r\n    1 root   0.3")
	s := term.Screen()
	if !s.Alt || s.Running != "htop" || s.Text != "  PID USER  CPU%\n    1 root   0.3" {
		t.Fatalf("screen: %+v", s)
	}
	if rows, _, _, _ := term.Rows(5, func(int) color.Color { return nil }); rows[0].Running {
		t.Error("alt screen must not get block decoration")
	}
	p := buildPrompt("what is using cpu?", "h", nil, nil, &s)
	if !strings.Contains(p, "<screen alt=true running=\"htop\">\n  PID USER  CPU%") {
		t.Errorf("prompt: %s", p)
	}
	for q, want := range map[string]bool{"look at the screen": true, "mira la pantalla": true, "why is htop red?": true, "stop it": false} {
		if refScreen.MatchString(q) != want {
			t.Errorf("refScreen %q", q)
		}
	}
}

func TestFencesAndPrompt(t *testing.T) {
	reply := "Config:\n```nginx\nlisten 80;\n```\nRun:\n```sh\nls -la\n```\nand ```\nplain\n``` done"
	if s := suggestion(reply); s != "plain" {
		t.Errorf("suggestion: %q", s)
	}
	if s := suggestion("```nginx\nlisten 80;\n```"); s != "" {
		t.Errorf("non-shell fence must not be a suggestion: %q", s)
	}
	md := ansi.Strip(md0(reply, 40)) // used to panic on fenced code
	for _, want := range []string{"▌ nginx", "▌ listen 80;", "▌ sh", "▌ ls -la"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q in:\n%s", want, md)
		}
	}

	term := &Term{emu: vt.NewEmulator(30, 4)}
	go func() {
		b := make([]byte, 64)
		for {
			term.emu.Read(b)
		}
	}()
	term.emu.RegisterOscHandler(133, term.osc133)
	if term.AtPrompt() {
		t.Error("no marks yet: not a known prompt")
	}
	term.emu.WriteString("$ \x1b]133;B\x07")
	if !term.AtPrompt() {
		t.Error("idle prompt")
	}
	term.emu.WriteString("top\r\n\x1b]133;C\x07")
	if term.AtPrompt() {
		t.Error("command running")
	}
}

func TestMCPServer(t *testing.T) {
	noUnix(t)
	sock := filepath.Join(t.TempDir(), "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			var call toolCall
			json.NewDecoder(c).Decode(&call)
			json.NewEncoder(c).Encode(toolResult{Text: call.Name + ":" + string(call.Args)})
			c.Close()
		}
	}()
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"run","arguments":{"command":"uptime"}}}`,
	}, "\n")
	var out strings.Builder
	mcpServe(sock, "nagios", strings.NewReader(in), &out)
	got := out.String()
	for _, want := range []string{`"protocolVersion":"2025-06-18"`, `"name":"run"`, `"name":"screen"`, `"name":"nagios"`, `"text":"run:{\"command\":\"uptime\"}"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"name":"metrics"`) || strings.Contains(got, `"name":"secret_get"`) {
		t.Error("tools of unconfigured groups must not be listed")
	}
	if n := strings.Count(got, "\n"); n != 3 {
		t.Errorf("want 3 responses (notification unanswered), got %d", n)
	}
}

func TestNeedsApproval(t *testing.T) {
	for cmd, ask := range map[string]bool{
		"uptime; df -h /":                                   false,
		"sudo tail -n 50 /var/log/nginx/error.log":          false,
		"ls -la /etc 2>&1 | head":                           false,
		"grep -r foo /etc >/dev/null":                       false,
		"systemctl status nginx":                            false,
		"journalctl -u nginx --since '1h ago'":              false,
		"docker ps -a":                                      false,
		"kubectl get pods -A":                               false,
		"rm -rf /tmp/x":                                     true,
		"sudo systemctl restart nginx":                      true,
		"echo hi > /etc/motd":                               true,
		"cat a >> b":                                        true,
		"sudo apt-get install -y htop":                      true,
		"sed -i 's/a/b/' f":                                 true,
		"cd /tmp && mkdir x":                                true,
		"docker compose up -d":                              true,
		"kubectl delete pod web-1":                          true,
		"sudo -u postgres psql -c 'select 1' | tee out.txt": true,
	} {
		if got := needsApproval("read", cmd) != ""; got != ask {
			t.Errorf("%q: ask=%v want %v (%s)", cmd, got, ask, needsApproval("read", cmd))
		}
	}
	if needsApproval("write", "uptime") == "" || needsApproval("", "uptime") == "" {
		t.Error("write or unlabeled must always ask")
	}
}

// needBash44 skips t when bash is older than 4.4 (macOS's 3.2): without PS0
// the shell integration can't mark where a command starts, which t needs.
// desk's own local panes take zsh then (localShell).
func needBash44(t *testing.T) {
	t.Helper()
	if localShell("darwin")[0] != "bash" {
		t.Skip("bash < 4.4: no PS0 for the command marks")
	}
}

// TestShellIntegration runs the injected line in each real shell available and
// checks that blocks come out with command, output and exit code.
func TestShellIntegration(t *testing.T) {
	for name, args := range map[string][]string{
		"bash": {"--norc", "-i"}, "zsh": {"-f", "-i"}, "fish": {"--no-config", "-i"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := exec.LookPath(name); err != nil {
				t.Skip(name + " not installed")
			}
			if name == "bash" {
				needBash44(t)
			}
			cmd := exec.Command(name, args...)
			if name == "zsh" {
				cmd.Env = append(os.Environ(), "PS1=%m%# ")
			}
			term, err := startCmd("local", cmd, 80, 24, func() {}, paneState{})
			if err != nil {
				t.Fatal(err)
			}
			defer term.Close()
			wait := func(what string, ok func() bool) {
				for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
					if ok() {
						return
					}
				}
				t.Fatalf("timed out waiting for %s; screen:\n%s", what, term.Screen().Text)
			}
			wait("prompt", term.AtPrompt)
			r := term.Run("echo hi; false", 10*time.Second)
			if !strings.HasPrefix(r.Text, "block #1 exit=1\nhi") {
				t.Fatalf("run: %q\nblocks: %+v", r.Text, term.Blocks())
			}
			if b := term.Blocks()[0]; b.Cmd != "echo hi; false" || !b.ByClaude {
				t.Errorf("block: %+v", b)
			}
			wait("prompt after run", term.AtPrompt) // fish marks it a moment after the block ends
			if r := term.SendKeys("x"); !r.IsError {
				t.Errorf("send_keys at a prompt should refuse: %q", r.Text)
			}
		})
	}
}

// TestPaneStateRestore: a pane's text, blocks, directory and exported
// environment come back in a new shell, in each real shell available.
func TestPaneStateRestore(t *testing.T) {
	for name, sh := range map[string][]string{
		"bash": {"--norc", "-i", "unset DESK_GONE"}, "zsh": {"-f", "-i", "unset DESK_GONE"}, "fish": {"--no-config", "-i", "set -e DESK_GONE"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := exec.LookPath(name); err != nil {
				t.Skip(name + " not installed")
			}
			if name == "bash" {
				needBash44(t)
			}
			dir := filepath.Join(t.TempDir(), "my dir")
			os.Mkdir(dir, 0o755)
			start := func(st paneState, w, h int) *Term {
				cmd := exec.Command(name, sh[:2]...)
				cmd.Dir = filepath.Dir(dir) // outside the repo: fish's default prompt runs git there, slow in a CI checkout
				cmd.Env = append(os.Environ(), "PS1=$ ", "DESK_GONE=1")
				term, err := startCmd("local", cmd, w, h, func() {}, st)
				if err != nil {
					t.Fatal(err)
				}
				for end := time.Now().Add(10 * time.Second); time.Now().Before(end) && !term.AtPrompt(); time.Sleep(50 * time.Millisecond) {
				}
				return term
			}
			a := start(paneState{}, 80, 24)
			defer a.Close()
			a.Run("cd '"+dir+"'; export DESK_T='a b'; "+sh[2]+"; echo hello", 10*time.Second)
			time.Sleep(300 * time.Millisecond) // the prompt's env report
			st := a.Keep()
			if st.Cwd != dir || st.Env["DESK_T"] != "a b" || !slices.Contains(st.Unset, "DESK_GONE") || len(st.Blocks) != 1 || !strings.Contains(st.Text, "hello") {
				t.Fatalf("kept: %+v", st)
			}
			if _, ok := st.Env["PWD"]; ok {
				t.Error("PWD is the shell's own")
			}
			b := start(st, 40, 5) // narrow: the injected line wraps and scrolls
			defer b.Close()
			b.Resize(80, 24)
			// printenv through cat: coreutils 9.12 shell-quotes values on a tty ('a b')
			r := b.Run("printenv DESK_T | cat; pwd; printenv DESK_GONE || echo none", 10*time.Second)
			if !strings.Contains(strings.ReplaceAll(r.Text, "\n", ""), "a b"+dir+"none") { // a long $TMPDIR (macOS) wraps the path

				t.Errorf("restored shell: %q", r.Text)
			}
			if bs := b.Blocks(); len(bs) != 2 || !strings.Contains(bs[0].Cmd, "echo hello") || bs[0].Output != "hello" {
				t.Errorf("blocks: %+v", bs)
			}
			if len(b.Find("hello")) == 0 {
				t.Error("old text not in the scrollback")
			}
			if len(b.Find("__desk_e")) > 0 {
				t.Errorf("the injected line shows: %q", b.Unwrapped())
			}
		})
	}
}

// TestParseChart: the mermaid charts the GUI draws natively.
func TestParseChart(t *testing.T) {
	c, ok := parseChart("xychart-beta horizontal\n title \"Disk (%)\"\n x-axis [\"/\", /boot]\n y-axis \"%\" 0 --> 100\n bar [83, 23]\n line \"avg\" [50, x]")
	if !ok || !c.horizontal || c.title != "Disk (%)" || !c.ranged || c.hi != 100 || len(c.labels) != 2 || len(c.series) != 2 ||
		c.series[1].name != "avg" || !c.series[1].line || !math.IsNaN(c.series[1].vals[1]) {
		t.Fatalf("xychart: %+v %v", c, ok)
	}
	c, ok = parseChart("pie title Memory\n \"pg\" : 4.2\n \"free\" : 8\n \"zero\" : 0")
	if !ok || !c.pie || c.title != "Memory" || len(c.labels) != 2 || c.series[0].vals[1] != 8 {
		t.Fatalf("pie: %+v %v", c, ok)
	}
	if _, ok := parseChart("flowchart TD\n A --> B"); ok {
		t.Error("a flowchart is not a chart")
	}
	if lo, hi, step := niceTicks(0, 4.1, 5); lo != 0 || hi != 5 || step != 1 {
		t.Errorf("ticks %v %v %v", lo, hi, step)
	}
	for v, want := range map[float64]string{83: "83", 1.25: "1.25", 12345: "12.3k", 2500000: "2.5M"} {
		if got := fmtNum(v); got != want {
			t.Errorf("fmtNum(%v) = %q", v, got)
		}
	}
	if w := fitColumns([]int{50, 400, 60}, 300); w[0] != 50 || w[2] != 60 || w[1] != 190 {
		t.Errorf("shrink: %v", w)
	}
	if w := fitColumns([]int{100, 100}, 400); w[0] != 200 || w[1] != 200 {
		t.Errorf("stretch: %v", w)
	}
}

func TestParseKeys(t *testing.T) {
	evs, err := parseKeys("qP<enter><ctrl-c><Up>")
	if err != nil || len(evs) != 5 || evs[1].Text != "P" || evs[2].Code != uv.KeyEnter ||
		evs[3].Code != 'c' || evs[3].Mod != uv.ModCtrl || evs[4].Code != uv.KeyUp {
		t.Errorf("%+v %v", evs, err)
	}
	if _, err := parseKeys("<nope>"); err == nil {
		t.Error("want an error for an unknown key")
	}
}

// infraHelper is the test binary as a program doing what (TestMain).
func infraHelper(what string) *exec.Cmd {
	c := exec.Command(os.Args[0])
	c.Env = []string{"DESK_INFRA_HELPER=" + what} // startCmd adds desk's
	return c
}

func TestTermClose(t *testing.T) {
	term, err := startCmd("local", infraHelper("sleep"), 80, 24, func() {}, paneState{})
	if err != nil {
		t.Fatal(err)
	}
	term.Close()
	for i := 0; i < 100 && term.code == 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	// Signaled; on Windows STATUS_CONTROL_C_EXIT, but under Wine 0.
	if runtime.GOOS != "windows" && term.code == 0 || term.Lost() {
		t.Error("the program should have been hung up")
	}
	if _, exited := term.State(); !exited {
		t.Error("closed term must report exited")
	}
}

// TestRunStartsFullScreen: run with a short timeout leaves htop-like programs
// running (here a plain sleep) and send_keys can then talk to them.
func TestRunLeavesProgramRunning(t *testing.T) {
	needBash44(t)
	term, err := startCmd("local", exec.Command("bash", "--norc", "-i"), 80, 24, func() {}, paneState{})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end) && !term.AtPrompt(); time.Sleep(50 * time.Millisecond) {
	}
	if r := term.Run("sleep 30", time.Second); !strings.Contains(r.Text, "still running") {
		t.Fatalf("run: %q", r.Text)
	}
	if r := term.SendKeys("<ctrl-c>"); r.IsError {
		t.Fatalf("send_keys to a running program: %q", r.Text)
	}
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end) && !term.AtPrompt(); time.Sleep(50 * time.Millisecond) {
	}
	if bs := term.Blocks(); len(bs) != 1 || bs[0].Cmd != "sleep 30" || bs[0].Exit != 130 {
		t.Errorf("blocks: %+v", bs)
	}
}

// TestShellGaps: between a command's end and the next prompt keys still go
// to the shell, so send_keys refuses there; a restored pane isn't at its
// prompt until the history is back in.
func TestShellGaps(t *testing.T) {
	term, _ := fakeTermA(t, "local")
	term.b, term.integrated = nil, true // D seen, B not yet
	if r := term.SendKeys("x<enter>"); !r.IsError || !term.ShellReads() {
		t.Errorf("between D and B: %q", r.Text)
	}
	term.c = &pos{} // a program runs
	if term.ShellReads() {
		t.Error("a running program reads the keys")
	}
	term.b, term.c, term.integrated = nil, nil, false // no marks yet: a host-key or password prompt
	if term.ShellReads() {
		t.Error("before the integration, keys answer the login")
	}
	term.b, term.pending = &pos{}, &paneState{Text: "old"}
	if term.AtPrompt() {
		t.Error("at a prompt before the restored history is in")
	}
	if r := term.Run("uptime", time.Millisecond); !r.IsError || !strings.Contains(r.Text, "busy") {
		t.Errorf("run before the history: %q", r.Text)
	}
}

func TestCompoundNeedsExplain(t *testing.T) {
	for cmd, want := range map[string]bool{
		"uptime": false, "df -h /": false, "ps aux | sort -k4 -nr | head": true,
		"cd /x && ls": true, "echo $(date)": true, "ls 2>/dev/null": true,
	} {
		if compound.MatchString(cmd) != want {
			t.Errorf("%q: want compound=%t", cmd, want)
		}
	}
	tab := &Tab{panes: []*Pane{{id: 1}}}
	r := tab.callTool(toolCall{Name: "run", Args: json.RawMessage(`{"command":"ps aux | head","effect":"read","reason":"x"}`)})
	if !r.IsError || !strings.Contains(r.Text, "explain") {
		t.Errorf("unexplained pipe must be refused: %q", r.Text)
	}
}

func TestOrgConfig(t *testing.T) {
	root := t.TempDir()
	group := filepath.Join(root, "Company", infraDir)
	os.MkdirAll(group, 0o755)
	os.WriteFile(filepath.Join(root, "Company", ".desk", "desk.toml"), []byte(`
[[vault]]
name = "passbolt"
kind = "passbolt"
url = "https://passbolt.example.com"
key_file = "~/k.asc"

[[nagios]]
name = "nagios1"
url = "https://nagios.example.com/nagios4/"
user = "nagiosadmin"
vault = "passbolt"
item = "nagios web"

[[metrics]]
name = "vm"
type = "victoriametrics"
url = "https://vm.example.com/"
token_env = "VM_TOKEN"
`), 0o644)
	o, err := orgFor(group)
	if err != nil {
		t.Fatal(err)
	}
	if g := strings.Join(o.toolGroups(), ","); g != "secrets,nagios,metrics" {
		t.Errorf("groups %s", g)
	}
	if o.Nagios[0].secret.Item != "nagios web" || o.Nagios[0].user != "nagiosadmin" || o.Metrics[0].secret.TokenEnv != "VM_TOKEN" || o.Vaults[0].desk.KeyFile != "~/k.asc" {
		t.Errorf("%+v", o)
	}
	if d := o.describe(); !strings.Contains(d, "nagios1") || !strings.Contains(d, "vm (victoriametrics)") {
		t.Errorf("describe: %s", d)
	}
	if o2, _ := orgFor(group); o2 != o {
		t.Error("tabs of one org must share its Org (one passbolt login)")
	}
	if e, err := orgFor(filepath.Join(t.TempDir(), "x", infraDir)); err != nil || len(e.toolGroups()) != 0 {
		t.Error("no desk.toml is an empty org")
	}
}

func TestNagiosAndMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); r.URL.Path != "/api/v1/query_range" && (u != "nagiosadmin" || p != "pw") && strings.Contains(r.URL.Path, "statusjson") {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Query().Get("query") == "hostlist":
			w.Write([]byte(`{"data":{"hostlist":{"web1":{"status":2,"plugin_output":"PING OK"},"db1":{"status":4,"plugin_output":"PING CRITICAL","last_state_change":0}}}}`))
		case r.URL.Query().Get("query") == "servicelist":
			w.Write([]byte(`{"data":{"servicelist":{"web1":{"HTTP":{"status":2,"plugin_output":"200 OK"},"Disk":{"status":4,"plugin_output":"DISK WARNING 85%","problem_has_been_acknowledged":true,"current_attempt":3,"max_attempts":3}},"db1":{"MySQL":{"status":16,"plugin_output":"connection refused","current_attempt":1,"max_attempts":3}}}}}`))
		case r.URL.Path == "/api/v1/query_range":
			w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"up","job":"node"},"values":[[1700000000,"1"],[1700000060,"0"],[1700000120,"1"]]}]}}`))
		}
	}))
	defer srv.Close()
	o := &orgConfig{Nagios: []SourceCfg{{Name: "n", URL: srv.URL + "/nagios4/", secret: &deskorgs.Secret{Vault: "v", Item: "x"}}},
		Metrics: []SourceCfg{{Name: "m", URL: srv.URL}}}
	o.logins = map[string][2]string{"n": {"nagiosadmin", "pw"}} // as if fetched from the vault
	out, err := o.nagios(context.Background(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[1], "DOWN        db1") || !strings.Contains(lines[2], "CRITICAL    db1 / MySQL") ||
		!strings.Contains(lines[2], "[soft 1/3]") || !strings.Contains(lines[3], "web1 / Disk") || !strings.Contains(lines[3], "[acked]") {
		t.Errorf("problems:\n%s", out)
	}
	if out, _ := o.nagios(context.Background(), "", "web", "http"); !strings.Contains(out, "OK          web1 / HTTP") || strings.Contains(out, "Disk") {
		t.Errorf("host view:\n%s", out)
	}
	out, err = o.metrics(context.Background(), "", "up", "-1h", "", "", "")
	if err != nil || !strings.Contains(out, `up{job="node"}  points=3 min=0`) || !strings.Contains(out, "last=1") {
		t.Errorf("metrics: %v\n%s", err, out)
	}
	if _, err := promTime("-90m", time.Now()); err != nil {
		t.Error(err)
	}
}

type fakeVault map[string]vaultItem

func (f fakeVault) search(context.Context, string) ([]vaultItem, error) {
	var out []vaultItem
	for _, it := range f {
		out = append(out, it)
	}
	return out, nil
}
func (f fakeVault) get(_ context.Context, id string) (vaultItem, error) { return f[id], nil }

func TestConnectStatesAndExit(t *testing.T) {
	needSh(t)
	mk := func(cmd *exec.Cmd) *Tab {
		term, err := startCmd("x", cmd, 80, 24, func() {}, paneState{})
		if err != nil {
			t.Fatal(err)
		}
		return &Tab{term: term, panes: []*Pane{{id: 1, term: term, waitUntil: time.Now().Add(300 * time.Millisecond)}}}
	}
	waitFor := func(what string, ok func() bool) {
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
			if ok() {
				return
			}
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	shell, silent, dead := mk(exec.Command("bash", "--norc", "-i")), mk(exec.Command("sleep", "30")), mk(exec.Command("false"))
	m := model{tabs: []*Tab{shell, silent, dead}}
	waitFor("prompt", shell.term.AtPrompt)
	if s := shell.connState(); s != "" {
		t.Errorf("connected shell: %q", s)
	}
	waitFor("timeout", func() bool { return silent.connState() == "timeout" })
	silent.panes[0].waitUntil = time.Now().Add(connectWait) // "keep waiting"
	if s := silent.connState(); s != "" {
		t.Errorf("after keep waiting: %q", s)
	}
	waitFor("failed", func() bool { return dead.connState() == "failed" })
	shell.term.Run("exit", time.Second)
	waitFor("shell exit", func() bool { _, e := shell.term.State(); return e })
	m.reap()
	if len(m.tabs) != 2 || m.tabs[0] != silent || m.tabs[1] != dead {
		t.Errorf("exit must close only the connected tab; tabs left: %d", len(m.tabs))
	}
	m.closeTab(0)
	m.closeTab(0)
	if len(m.tabs) != 0 || m.active() != 0 {
		t.Error("closing every tab leaves none")
	}
}

func TestLegacyFixes(t *testing.T) {
	fixes := legacyFixes(&sshc.NegotiationError{What: "HostKeyAlgorithms", Theirs: []string{"ssh-rsa"}})
	cfg := filepath.Join(t.TempDir(), "ssh_config")
	os.WriteFile(cfg, []byte("Host a\n    HostName 1.1.1.1\n\nHost old\n    HostName 2.2.2.2\n    KexAlgorithms +diffie-hellman-group1-sha1\n\nMatch all\n    Include ~/.ssh/config\n"), 0o600)
	if err := applyFixes(cfg, "old", append(fixes, legacyFix{"KexAlgorithms", []string{"diffie-hellman-group14-sha1"}})); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(cfg)
	want := "Host a\n    HostName 1.1.1.1\n\nHost old\n    # old server: algorithms modern OpenSSH disables (added by desk)\n    HostKeyAlgorithms +ssh-rsa\n" +
		"    PubkeyAcceptedAlgorithms +ssh-rsa\n    HostName 2.2.2.2\n    KexAlgorithms +diffie-hellman-group1-sha1,diffie-hellman-group14-sha1\n\nMatch all\n    Include ~/.ssh/config\n"
	if string(got) != want {
		t.Errorf("got:\n%s", got)
	}
	if st, _ := os.Stat(cfg); st.Mode().Perm() != 0o600 && exeSuffix == "" { // no mode bits on Windows
		t.Error("keep the file's permissions")
	}
	c, err := sshconfig.Resolve("old", sshconfig.Options{File: cfg, System: "none", Home: t.TempDir()})
	if err != nil || !slices.Contains(c.Algorithms("hostkeyalgorithms"), "ssh-rsa") || !slices.Contains(c.Algorithms("kexalgorithms"), "diffie-hellman-group14-sha1") {
		t.Errorf("the fixed config doesn't read back: %v", err)
	}
}

func TestUnwrapped(t *testing.T) {
	term := &Term{emu: vt.NewEmulator(20, 4)}
	term.emu.WriteString("Their offer: ssh-rsa,ssh-dss\r\nnext")
	if u := term.Unwrapped(); !strings.HasPrefix(u, "Their offer: ssh-rsa,ssh-dss\nnext\n") {
		t.Errorf("%q", u)
	}
}

func TestFzfScore(t *testing.T) {
	ok := func(q, hay string) bool { _, m := fzfScore(q, hay); return m }
	for _, c := range []struct {
		q, hay string
		want   bool
	}{
		{"wp1", "webproxy-1 eu-west Company 192.0.2.10", true},
		{"wp eust", "webproxy-1 eu-west Company", true},
		{"eustwp", "webproxy-1 eu-west Company", false}, // fuzzy keeps order
		{"'proxy-1", "webproxy-1 eu-west", true},
		{"'pxy", "webproxy-1", false},
		{"^web", "webproxy-1 eu-west", true},
		{"^west", "webproxy-1 eu-west", false},
		{"company$", "webproxy-1 eu-west company", true},
		{"web !staging", "webproxy-staging eu-west", false},
		{"web !staging", "webproxy-1 eu-west", true},
		{"WEB", "webproxy-1", false}, // smart case
	} {
		if got := ok(c.q, c.hay); got != c.want {
			t.Errorf("%q in %q: got %t", c.q, c.hay, got)
		}
	}
	tight, _ := fzfScore("ledg", "ledger us-east")
	loose, _ := fzfScore("ledg", "lb-remote-edge-gw eu-lb")
	if tight <= loose {
		t.Errorf("a contiguous match must score higher: %d vs %d", tight, loose)
	}
}

func TestPrettyBlocks(t *testing.T) {
	reply := "Disk on db1 grows fast.\n\n> [!WARNING]\n> Do not delete WAL files by hand.\n\n" +
		"```mermaid\nflowchart TD\n  A[disk full] --> B{WAL?}\n  B -->|yes| C[check slots]\n```\n\n" +
		"```mermaid\nxychart-beta\n  title \"/var used %\"\n  x-axis [12:00, 12:30, 13:00]\n  bar [80, 89, 98]\n```\n\n" +
		"```figure\n{accent:▏───▏} {warn:98%} {ok:✓ up}\n```\n"
	out, tgts := markdown(reply, 60)
	if len(tgts) != 3 { // two diagrams and the figure
		t.Errorf("copy buttons: %+v", tgts)
	}
	plain := ansi.Strip(out)
	for _, want := range []string{"▲ Warning", "Do not delete WAL files by hand.", "disk full", "check slots", "/var used %", "12:30", "98", "▏───▏ 98% ✓ up"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "{warn:") || strings.Contains(plain, "[!WARNING]") || strings.Contains(plain, "flowchart TD") {
		t.Errorf("markup leaked:\n%s", plain)
	}
	for _, ln := range strings.Split(out, "\n") {
		if ansi.StringWidth(ln) > 60 {
			t.Errorf("wider than the panel: %q", ansi.Strip(ln))
		}
	}
	if _, ok := mermaid("pie title x\n \"a\": 1", 60); ok {
		t.Error("unsupported mermaid must fall back to its source")
	}
}

func md0(s string, w int) string { out, _ := markdown(s, w); return out }

func TestTablesMathSideBySideCopy(t *testing.T) {
	reply := "Two views:\n\n| host | disk | state |\n|:--|--:|:-:|\n| db1 | 97% | `crit` |\n| web1 | 41% | ok |\n\n" +
		"```mermaid\nxychart-beta\n  x-axis [a, b]\n  bar [1, 2]\n```\n\n$$\\frac{a}{b} \\leq \\sqrt{x^2 + y_1}$$\n\n```sh\ndf -h /var\n```\n"
	out, tgts := markdown(reply, 100)
	plain := ansi.Strip(out)
	lines := strings.Split(plain, "\n")
	var side string
	for _, l := range lines {
		if strings.Contains(l, "db1") {
			side = l
		}
	}
	if !strings.Contains(side, "db1") || !strings.Contains(side, "97%") || !strings.Contains(plain, "a/b ≤ √(x² + y₁)") {
		t.Errorf("table/math:\n%s", plain)
	}
	header := ""
	for _, l := range lines {
		if strings.Contains(l, "⧉ md") {
			header = l
		}
	}
	if !strings.Contains(header, "⧉ art") {
		t.Errorf("table and chart should sit side by side:\n%s", plain)
	}
	want := map[string]bool{"md": false, "art": false, "tex": false, "copy": false}
	for _, tg := range tgts {
		ln := []rune(lines[tg.line])
		label := strings.TrimSpace(string(ln[tg.col+2 : min(len(ln), tg.col+tg.w)]))
		want[label] = true
		if label == "copy" && tg.text != "df -h /var" || label == "md" && !strings.HasPrefix(tg.text, "| host") {
			t.Errorf("%s copies %q", label, tg.text)
		}
	}
	for k, ok := range want {
		if !ok {
			t.Errorf("no %s button at its spot", k)
		}
	}
	if !setTheme("nord") || setTheme("nope") || pal.Accent != "#88c0d0" {
		t.Error("theme switch")
	}
	setTheme("desk")
}

func TestTableCells(t *testing.T) {
	if c := strings.Join(cells("|a|b\\|c| --: |"), ","); c != "a,b|c,--:" {
		t.Errorf("%q", c)
	}
	out := ansi.Strip(md0("| h | n |\n|:--|--:|\n| x | 7 |\n| yy | 100 |\n", 60))
	if !strings.Contains(out, "│ x  │   7 │") {
		t.Errorf("right alignment:\n%s", out)
	}
}

func TestSudoPrompt(t *testing.T) {
	term := &Term{emu: vt.NewEmulator(60, 5)}
	term.emu.WriteString("$ sudo systemctl restart nginx\r\n[sudo] password for deploy: ")
	if u, ok := term.SudoPrompt(); !ok || u != "deploy" {
		t.Errorf("prompt not seen: %q %t", u, ok)
	}
	term.emu.WriteString("\r\n$ ")
	if _, ok := term.SudoPrompt(); ok {
		t.Error("an old prompt line above the cursor must not count")
	}
	term.emu.WriteString("echo '[sudo] password for x: '")
	if _, ok := term.SudoPrompt(); ok {
		t.Error("text typed at a shell prompt is not sudo asking")
	}
	for line, user := range map[string]string{
		"[sudo: authenticate] Password: ": "",
		"doas (deploy@web-1) password: ":  "",
		"Password: ":                      "",
		"[sudo] contraseña para deploy: ": "deploy",
		"Contraseña: ":                    "",
		"Enter password: ":                "-",
		"$ grep password: /etc/x":         "-",
	} {
		term := &Term{emu: vt.NewEmulator(60, 5)}
		term.emu.WriteString("$ sudo -i\r\n" + line)
		got, ok := term.SudoPrompt()
		if ok != (user != "-") || ok && got != user {
			t.Errorf("%q: %q %t", line, got, ok)
		}
	}
}

// TestSudoFill runs a real sudo-like prompt (read -s) and checks that the
// vault password is typed only while it waits.
func TestSudoFill(t *testing.T) {
	needSh(t)
	g := filepath.Join(t.TempDir(), "Org", infraDir)
	orgsMu.Lock()
	orgs[g] = &orgConfig{Vaults: []VaultCfg{{Name: "pb", Type: "passbolt"}},
		vaults: map[string]vault{"pb": fakeVault{"id9": {ID: "id9", Password: "hunter2"}}}}
	orgsMu.Unlock()
	term, err := startCmd("local", exec.Command("bash", "--norc", "-i"), 80, 24, func() {}, paneState{})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end) && !term.AtPrompt(); time.Sleep(50 * time.Millisecond) {
	}
	pane := &Pane{dir: g, term: term, password: &SecretRef{Vault: "pb", Item: "id9"}}
	m := &model{}
	// no sudo prompt: alt+p copies instead of typing
	if cmd := m.usePassword(serverPasswordCmd(pane)().(passwordMsg)); cmd == nil || m.flash != "⧉ server password copied" {
		t.Errorf("copy: %q", m.flash)
	}
	go term.Run(`read -rs -p "[sudo] password for deploy: " pw; echo; [ "$pw" = "$(printf hunt%s er2)" ] && echo GO""OD`, 10*time.Second)
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if _, ok := term.SudoPrompt(); ok {
			break
		}
	}
	m.usePassword(serverPasswordCmd(pane)().(passwordMsg))
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end) && !strings.Contains(term.Screen().Text, "GOOD"); time.Sleep(50 * time.Millisecond) {
	}
	if s := term.Screen().Text; !strings.Contains(s, "GOOD") || strings.Contains(s, "hunter2") {
		t.Errorf("password not typed, or echoed:\n%s", s)
	}
}

// TestConnectUnreachable: connect on a server already stuck in its "can't
// connect" box waits on that pane, not a new one, until the user retries
// or, here, closes it; then Claude is told not to connect again on its own.
func TestConnectUnreachable(t *testing.T) {
	needSh(t)
	dir := t.TempDir()
	term, err := startCmd("web1", exec.Command("false"), 80, 24, func() {}, paneState{})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	tab := &Tab{}
	p := &Pane{term: term, dir: dir, id: 3, name: "web1"}
	tab.panes = []*Pane{p}
	go func() {
		time.Sleep(700 * time.Millisecond)
		tab.mu.Lock()
		p.closed = true
		tab.mu.Unlock()
	}()
	r := tab.openPane(node{host: "web1", dir: dir, label: "web1"})
	if !r.IsError || !strings.Contains(r.Text, "closed pane 3") || len(tab.panes) != 1 {
		t.Fatalf("%+v", r)
	}
}

// TestOrgTab: an org tab starts with Claude alone, gets panes on its servers,
// and stays open when they close; a host tab closes with its last pane.
func TestOrgTab(t *testing.T) {
	dir := t.TempDir()
	m := model{tree: []node{{depth: 0, label: "Acme", org: "Acme", dir: dir},
		{depth: 1, label: "g"}, {depth: 2, label: "local", org: "Acme", group: "g", dir: dir, host: "local"},
		{depth: 2, label: "web1", org: "Acme", group: "g", dir: dir, host: "web1", addr: "192.0.2.10"}}}
	m.openOrg(m.tree[0], "")
	tab := m.tab()
	if !tab.orgOnly || len(tab.panes) != 0 {
		t.Fatalf("org tab: panes=%d", len(tab.panes))
	}
	(&driver{}).snapshot(&m) // no panes: must not panic
	if r := tab.callTool(toolCall{Name: "screen"}); !r.IsError || !strings.Contains(r.Text, "connect") {
		t.Errorf("no pane: %q", r.Text)
	}
	if r := tab.callTool(toolCall{Name: "hosts", Args: json.RawMessage(`{"query":"192.0.2"}`)}); !strings.Contains(r.Text, "g/web1 (192.0.2.10)") || strings.Contains(r.Text, "local") {
		t.Errorf("hosts: %q", r.Text)
	}
	if _, err := tab.findHost("nope"); err == nil {
		t.Error("unknown host must fail")
	}
	n, _ := tab.findHost("g/local")
	p := m.addPane(tab, n, true)
	if p == nil || tab.term != p.term || len(tab.panes) != 1 {
		t.Fatal("first pane gets the keyboard")
	}
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end) && !p.term.AtPrompt(); time.Sleep(50 * time.Millisecond) {
	}
	(&driver{}).snapshot(&m)
	tab.closePane(p)
	m.reap()
	if len(m.tabs) != 1 || len(tab.panes) != 0 || tab.term != nil {
		t.Errorf("org tab must stay with no panes: tabs=%d panes=%d", len(m.tabs), len(tab.panes))
	}
	m.open(n) // a host tab
	host := m.tab()
	host.closePane(host.panes[0])
	m.reap()
	if len(m.tabs) != 1 || m.tabs[0] != tab {
		t.Errorf("host tab must close with its last pane: tabs=%d", len(m.tabs))
	}
	m.openOrg(m.tree[0], "")
	if len(m.tabs) != 1 {
		t.Error("opening the org again goes back to its tab")
	}
}

func TestPeer(t *testing.T) {
	m := model{tree: []node{{depth: 0, label: "Acme", org: "Acme", dir: t.TempDir()}}}
	if r := m.peer(peerReq{Op: "tell", Org: "nope", From: "dev", Text: "x"}); r.Err == "" {
		t.Error("unknown org must fail")
	}
	if r := m.peer(peerReq{Op: "tell", Org: "acme", From: "dev", Text: "db is postgres"}); r.Err != "" {
		t.Fatal(r.Err)
	}
	tab := m.tab()
	if !tab.orgOnly || len(tab.peerCtx) != 1 || !strings.Contains(m.peerList(), "tab 1") {
		t.Fatalf("tell must open the org tab and queue context: %v", tab.peerCtx)
	}
	got := make(chan toolResult)
	go func() { got <- tab.askPeer("", "which tables?") }()
	evs := waitEvents("dev", 5*time.Second)
	if len(evs) != 1 || evs[0].Kind != "question" {
		t.Fatalf("question: %+v", evs)
	}
	if r := m.peer(peerReq{Op: "answer", ID: evs[0].ID, From: "dev", Text: "events, positions"}); r.Err != "" {
		t.Fatal(r.Err)
	}
	if r := <-got; !strings.Contains(r.Text, "events, positions") {
		t.Errorf("answer: %q", r.Text)
	}
	k := &peerTask{id: 99, from: "dev", tab: tab}
	hub.Lock()
	hub.tasks[99], hub.running[tab] = k, k
	hub.Unlock()
	if taskFrom(tab) == "" {
		t.Error("approvals must name the running task")
	}
	tab.taskDone(99, "", "done", "deployed")
	if evs := waitEvents("dev", time.Second); len(evs) != 1 || evs[0].Kind != "result" || taskFrom(tab) != "" {
		t.Errorf("result: %+v", evs)
	}
	if r := tab.taskDone(99, "", "done", ""); !r.IsError {
		t.Error("a task reports once")
	}
	tab.taskDone(7, "dev", "done", "after a restart") // desk forgot the task: its from says where
	if evs := waitEvents("dev", time.Second); len(evs) != 1 || !strings.Contains(evs[0].Text, "after a restart") {
		t.Errorf("forgotten task: %+v", evs)
	}
}

// TestPeerCards: a card's events go into its session, not its inbox, and
// an infra Claude writes to a card or makes one.
func TestPeerCards(t *testing.T) {
	m := model{tree: []node{{depth: 0, label: "Acme", org: "Acme", dir: t.TempDir()}}}
	told := make(chan string, 4)
	var w Workspace
	w.SetCards(Cards{Is: func(n string) bool { return strings.HasPrefix(n, "DESK-") },
		Tell: func(key, text string) error { told <- key + ": " + text; return nil },
		New:  func(org, title, notes string) (string, error) { return "DESK-5", nil }})
	defer w.SetCards(Cards{})
	if r := m.peer(peerReq{Op: "tell", Org: "acme", From: "DESK-3", Text: "x"}); r.Err != "" {
		t.Fatal(r.Err)
	}
	tab := m.tab()
	got := make(chan toolResult)
	go func() { got <- tab.askPeer("", "which tables?") }()
	var q string
	select {
	case q = <-told:
	case <-time.After(5 * time.Second):
		t.Fatal("the question didn't reach the card")
	}
	hub.Lock()
	inbox := len(hub.inbox["DESK-3"])
	hub.Unlock()
	if !strings.HasPrefix(q, "DESK-3: question ") || !strings.Contains(q, "desk peer answer") || inbox != 0 {
		t.Fatalf("question %q, inbox %d", q, inbox)
	}
	var id int
	fmt.Sscanf(strings.TrimPrefix(q, "DESK-3: question "), "%d", &id)
	m.peer(peerReq{Op: "answer", ID: id, From: "DESK-3", Text: "events"})
	if r := <-got; !strings.Contains(r.Text, "events") {
		t.Errorf("answer: %q", r.Text)
	}
	if r := tab.tellCard("desk-8", "disk fills from /var/log/app"); r.IsError || !strings.HasPrefix(<-told, "DESK-8: message from Acme") {
		t.Errorf("message_card: %+v", r)
	}
	if r := tab.tellCard("dev", "x"); !r.IsError {
		t.Error("message_card is for cards")
	}
	if r := tab.newCard("Rotate app logs", "found"); r.IsError || !strings.Contains(r.Text, "DESK-5") {
		t.Errorf("create_card: %+v", r)
	}
}

// TestPeerTabPerCard: two cards of one org get a session each, apart from
// the org's own; the same card comes back to its own, after a restart too.
func TestPeerTabPerCard(t *testing.T) {
	m := model{tree: []node{{depth: 0, label: "Acme", org: "Acme", dir: t.TempDir()}}}
	var w Workspace
	w.SetCards(Cards{Is: func(n string) bool { return strings.HasPrefix(n, "DESK-") }})
	defer w.SetCards(Cards{})
	tab := func(from string) *Tab {
		t.Helper()
		if r := m.peer(peerReq{Op: "tell", Org: "acme", From: from, Text: "x"}); r.Err != "" {
			t.Fatal(r.Err)
		}
		return m.tabs[len(m.tabs)-1]
	}
	a, b := tab("DESK-3"), tab("DESK-4")
	own := tab("dev") // not a card: the org's own session
	if a == b || own == a || own == b || a.card != "DESK-3" || b.card != "DESK-4" || own.card != "" || a.name != "Acme · DESK-3" {
		t.Fatalf("tabs %+v %+v %+v", a, b, own)
	}
	if m.peer(peerReq{Op: "tell", Org: "Acme", From: "DESK-3", Text: "y"}); len(m.tabs) != 3 || len(a.peerCtx) != 2 || len(b.peerCtx) != 1 {
		t.Errorf("DESK-3 again: %d tabs, ctx %d/%d", len(m.tabs), len(a.peerCtx), len(b.peerCtx))
	}
	m.openOrg(m.tree[0], "")
	if m.tab() != own {
		t.Error("the org's own session from the sidebar")
	}
	m.activate(0)
	if _, err := m.peerTab("Acme", "DESK-4"); err != nil || m.active() != 0 {
		t.Error("an open card session is found, not shown")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m.restored = true
	m.saveSession()
	var r model
	r.tree = m.tree
	r.restore()
	if len(r.tabs) != 3 || r.tabs[0].card != "DESK-3" || r.tabs[1].card != "DESK-4" || r.tabs[2].card != "" {
		t.Fatalf("restored %d tabs", len(r.tabs))
	}
}

func TestSendKeyIgnoresLockMods(t *testing.T) {
	term := &Term{emu: vt.NewEmulator(20, 4)}
	got := make(chan string, 1)
	go func() { b := make([]byte, 16); n, _ := term.emu.Read(b); got <- string(b[:n]) }()
	term.SendKey(uv.KeyPressEvent{Code: uv.KeyUp, Mod: uv.ModNumLock, IsRepeat: true})
	if s := <-got; s != "\x1b[A" {
		t.Errorf("%q", s)
	}
	go func() { b := make([]byte, 16); n, _ := term.emu.Read(b); got <- string(b[:n]) }()
	term.SendKey(uv.KeyPressEvent{Code: 'c', Mod: uv.ModCtrl, Text: "c"})
	if s := <-got; s != "\x03" {
		t.Errorf("ctrl+c: %q", s)
	}
}

func TestGridPromptColumn(t *testing.T) {
	term := &Term{emu: vt.NewEmulator(30, 5)}
	term.emu.RegisterOscHandler(133, term.osc133)
	term.emu.WriteString("$ \x1b]133;B\x07ls\r\n\x1b]133;C\x07a\r\n\x1b]133;D;0\x07")
	_, rows, _, _, _ := term.Grid(0)
	if !rows[0].First || rows[0].N != 1 || rows[0].PX != 2 {
		t.Errorf("rows %+v", rows[:2])
	}
}

func TestGridRuns(t *testing.T) {
	term := &Term{emu: vt.NewEmulator(10, 2)}
	term.emu.WriteString("ab\x1b[31mcd\x1b[m")
	runs, _, cx, cy, _ := term.Grid(0)
	r := runs[0]
	if len(r) != 3 || r[0].Text != "ab" || r[1].Text != "cd" || r[1].X != 2 || r[1].Style.Fg == nil || r[2].W != 6 || cx != 4 || cy != 0 {
		t.Errorf("runs %+v cursor %d,%d", r, cx, cy)
	}
}

func TestParseListing(t *testing.T) {
	abs, es := parseListing("/srv/app\nf\tf\t120\t1700000000.5\t755\tb.txt\nd\td\t4096\t1\t755\tlogs\nl\td\t9\t1\t777\tcurrent\nf\tf\t3\t1\t644\tA.md\n")
	if abs != "/srv/app" || len(es) != 4 || es[0].Name != "current" || !es[0].Dir || !es[0].Link || es[1].Name != "logs" ||
		es[2].Name != "A.md" || es[3].Size != 120 || es[3].Mode != 0o755 || es[3].Mod.Unix() != 1700000000 {
		t.Errorf("find: %q %+v", abs, es)
	}
	_, es = parseListing("/home/x\nnotes\nsrc/\n")
	if len(es) != 2 || es[0].Name != "src" || !es[0].Dir || es[1].Size != -1 {
		t.Errorf("ls: %+v", es)
	}
	if q := shQuote("it's"); q != `'it'\''s'` {
		t.Errorf("quote %s", q)
	}
	if humanSize(1536) != "1.5 KB" || humanSize(5<<30) != "5.0 GB" || humanSize(10) != "10 B" {
		t.Error(humanSize(1536), humanSize(5<<30))
	}
}

func TestLocalCopyPipeline(t *testing.T) {
	// the directory path on the local host: tar out, tar in, through the counter
	src, dst := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(src, "d", "sub"), 0o755)
	os.WriteFile(filepath.Join(src, "d", "sub", "f"), []byte("hello"), 0o644)
	var n atomic.Int64
	err := copyFromRemote(context.Background(), "local", t.TempDir(), src, dst, fileEntry{Name: "d", Dir: true}, &n, func() {})
	if b, _ := os.ReadFile(filepath.Join(dst, "d", "sub", "f")); err != nil || string(b) != "hello" || n.Load() == 0 {
		t.Errorf("err %v, got %q, %d bytes", err, b, n.Load())
	}
}

// needGNUFind skips t without GNU find: BSD's (macOS) has no -printf, so
// listScript's listing has names only there.
func needGNUFind(t *testing.T) {
	t.Helper()
	if exec.Command("find", ".", "-maxdepth", "0", "-printf", "").Run() != nil {
		t.Skip("no GNU find: the shell listing has names only")
	}
}

func TestListScript(t *testing.T) {
	needGNUFind(t)
	d := t.TempDir()
	os.Mkdir(filepath.Join(d, "sub dir"), 0o755)
	os.WriteFile(filepath.Join(d, "it's.txt"), []byte("12345"), 0o644)
	out, err := exec.Command("sh", "-c", listScript(d)).Output()
	abs, es := parseListing(string(out))
	if err != nil || abs != d || len(es) != 2 || es[0].Name != "sub dir" || !es[0].Dir || es[1].Name != "it's.txt" || es[1].Size != 5 {
		t.Errorf("err %v abs %q %+v", err, abs, es)
	}
}

func TestSortEntries(t *testing.T) {
	now := time.Now()
	es := []fileEntry{{Name: "b", Size: 1, Mod: now}, {Name: "D", Dir: true}, {Name: "a", Size: 9, Mod: now.Add(-time.Hour)}, {Name: "c", Dir: true}}
	names := func() string {
		var s []string
		for _, e := range es {
			s = append(s, e.Name)
		}
		return strings.Join(s, "")
	}
	if sortEntries(es, 0, false); names() != "cDab" {
		t.Error("name", names())
	}
	if sortEntries(es, 1, true); names() != "cDab" { // folders stay first, by name
		t.Error("size", names())
	}
	if sortEntries(es, 2, true); names() != "Dcba" {
		t.Error("date", names())
	}
}

func TestOSC7Cwd(t *testing.T) {
	tm := &Term{emu: vt.NewEmulator(20, 5)}
	tm.emu.RegisterOscHandler(7, tm.osc7)
	tm.emu.Write([]byte("\x1b]7;/srv/my app\x07"))
	if tm.cwd != "/srv/my app" {
		t.Errorf("bare: %q", tm.cwd)
	}
	tm.emu.Write([]byte("\x1b]7;file://web1/var/log%20x\x07"))
	if tm.cwd != "/var/log x" {
		t.Errorf("url: %q", tm.cwd)
	}
}

type bufCloser struct{ strings.Builder }

func (*bufCloser) Close() error { return nil }

func TestQuestionsQueueWhileBusy(t *testing.T) {
	in := &bufCloser{}
	tab := &Tab{claude: &Claude{stdin: in}, busy: true, pinned: map[int]bool{}}
	m := model{tabs: []*Tab{tab}}
	m.input = "second question"
	m.ask()
	if len(tab.asked) != 1 || in.Len() != 0 || m.input != "" {
		t.Fatalf("asked %q, sent %q, input %q", tab.asked, in.String(), m.input)
	}
	r, _ := m.update(claudeMsg{tab: tab, done: true})
	m = r.(model)
	if len(tab.asked) != 0 || !tab.busy || !strings.Contains(in.String(), "second question") {
		t.Fatalf("after the turn: asked %q, busy %v, sent %q", tab.asked, tab.busy, in.String())
	}
}

func TestStopDeniesPending(t *testing.T) {
	in := &bufCloser{}
	a := &approval{cmd: "rm -rf /tmp/x", done: make(chan approvalResult, 1)}
	tab := &Tab{claude: &Claude{stdin: in}, busy: true, pending: []*approval{a}, asked: []string{"later"}}
	m := model{tabs: []*Tab{tab}}
	m.stop(tab)
	if !strings.Contains(in.String(), `"subtype":"interrupt"`) {
		t.Fatalf("no interrupt sent: %q", in.String())
	}
	if res := <-a.done; res.ok || len(tab.pending) != 0 || len(tab.asked) != 0 {
		t.Fatalf("pending command not denied: %+v, pending %d, asked %d", res, len(tab.pending), len(tab.asked))
	}
}

func TestClaudeExitRestarts(t *testing.T) {
	c := &Claude{stdin: &bufCloser{}}
	tab := &Tab{claude: c, busy: true}
	m := model{tabs: []*Tab{tab}}
	r, _ := m.update(claudeMsg{tab: tab, err: os.ErrClosed, done: true, exited: &Claude{}})
	m = r.(model)
	if tab.claude != c {
		t.Fatal("an older process's exit dropped the live one")
	}
	m.update(claudeMsg{tab: tab, done: true, exited: c})
	if tab.claude != nil || tab.busy {
		t.Fatalf("claude %v busy %v: the next question can't start a new process", tab.claude, tab.busy)
	}
}

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{
		"DB_PASSWORD=hunter2 next":      "DB_PASSWORD=[redacted] next",
		`"api_key": "abc123",`:          `"api_key": "[redacted]",`,
		"mysql://app:s3cret@db:3306/x":  "mysql://app:[redacted]@db:3306/x",
		"Authorization: Bearer abc.def": "Authorization: Bearer [redacted]",
		"key AKIAIOSFODNN7EXAMPLE here": "key [redacted] here",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----": "-----BEGIN RSA PRIVATE KEY----- [redacted]",
		"[sudo] password for deploy: ":                                         "[sudo] password for deploy: ",
		"Password:\nuptime":                                                    "Password:\nuptime",
		"load average: 0.10, 0.20":                                             "load average: 0.10, 0.20",
	} {
		if got := redact(in); got != want {
			t.Errorf("redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAuditLog(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	tab := &Tab{org: "Acme", group: "web"}
	auditBlock(tab, "web-01", Block{Cmd: "export TOKEN=abc", Output: "a\nb\n", Exit: 1, ByClaude: true, Dur: 1500 * time.Millisecond})
	audit(auditEntry{Org: "Acme", Event: "approval", By: "you", Cmd: "systemctl restart nginx", Verdict: "denied"})
	b, err := os.ReadFile(filepath.Join(os.Getenv("XDG_STATE_HOME"), "desk", "infra", "audit", "Acme", time.Now().Format("2006-01-02")+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var e auditEntry
	if len(lines) != 2 || json.Unmarshal([]byte(lines[0]), &e) != nil {
		t.Fatalf("log: %q", b)
	}
	if e.By != "claude" || e.Host != "web-01" || *e.Exit != 1 || e.DurMS != 1500 || e.Cmd != "export TOKEN=[redacted]" || e.Output != "a\nb" {
		t.Fatalf("entry: %+v", e)
	}
	if !strings.Contains(lines[1], `"verdict":"denied"`) {
		t.Fatalf("approval: %s", lines[1])
	}
}

func TestApprovalPolicy(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	o := &orgConfig{}
	o.Approvals.Allow = []string{"systemctl status *", "journalctl *"}
	o.Approvals.Deny = []string{"* --force*"}
	for cmd, want := range map[string]bool{
		"systemctl status nginx":    true,
		"journalctl -u x -n 50":     true,
		"journalctl -u x; rm -rf /": false, // compound: never allowed by pattern
		"systemctl restart nginx":   false,
		"journalctl --force-rotate": true, // allows ignores deny; gate checks deny first
	} {
		if got := o.allows(cmd); got != want {
			t.Errorf("allows(%q) = %v", cmd, got)
		}
	}
	if o.denies("git push --force origin") != "* --force*" || o.denies("git push") != "" {
		t.Error("deny pattern")
	}
	tab := &Tab{org: "Acme", orgCfg: o}
	if cmd, _, denied := tab.gate("systemctl status nginx", "", "Claude marked it as a write", false, 1); denied != nil || cmd == "" {
		t.Error("allow list didn't skip the prompt")
	}
	always.add("Acme", allowKey("uptime", false))
	always.orgs = map[string]map[string]bool{} // reread from disk
	if !always.has("Acme", allowKey("uptime", false)) || always.has("Other", allowKey("uptime", false)) {
		t.Error("always answers not kept per org")
	}
}

func TestNotifyOnlyWhenElsewhere(t *testing.T) {
	var got []string
	defer func(f func(string, string)) { desktopNotify = f }(desktopNotify)
	desktopNotify = func(title, body string) { got = append(got, title+": "+body) }
	seen, other := &Tab{name: "web-01"}, &Tab{name: "db-01"}
	m := model{tabs: []*Tab{seen, other}}
	for _, tab := range []*Tab{seen, other} {
		r, _ := m.update(approvalMsg{tab, &approval{cmd: "systemctl restart nginx"}})
		m = r.(model)
	}
	if len(got) != 1 || got[0] != "Approval needed · db-01: systemctl restart nginx" {
		t.Fatalf("notifications: %q", got)
	}
	m.away = true // desk's window lost focus (Workspace.SetFocused)
	seen.chat = []*chatEntry{{who: "claude", text: "nginx is up\nmore"}}
	m.update(claudeMsg{tab: seen, done: true})
	if len(got) != 2 || got[1] != "Claude answered · web-01: nginx is up\nmore" {
		t.Fatalf("after blur: %q", got)
	}
}

func TestMain(m *testing.M) {
	switch os.Getenv("DESK_INFRA_HELPER") { // the test binary as a pane's program: no shell needed, on any OS
	case "sleep":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "drop":
		fmt.Println("before the drop")
		time.Sleep(300 * time.Millisecond)
		os.Exit(255)
	}
	desktopNotify = func(string, string) {} // tests never reach the desktop
	state, _ := os.MkdirTemp("", "desk-test-state")
	os.Setenv("XDG_STATE_HOME", state)  // nor the user's state: recordings, audit, session
	os.Setenv("XDG_CONFIG_HOME", state) // nor desk's settings: the folders to search
	os.Setenv("HOME", state)            // nor the user's home: ~/.claude.json, ~/Secure
	os.Setenv("USERPROFILE", state)     // Windows' home
	sshBase.System = "none"             // nor the machine's ssh_config
	claude.NoBwrap = false              // bwrap's profiles on every OS; TestClaudeCmdNoBwrap has macOS's
	code := m.Run()
	os.RemoveAll(state)
	os.Exit(code)
}

func TestSessionRestore(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := model{restored: true}
	tab := m.newTab("Acme", "", t.TempDir(), "Acme")
	tab.orgOnly, tab.session = true, "sess-1"
	tab.chat = []*chatEntry{{who: "you", text: "disk on web-01?"}, {who: "claude", text: "82% used"}}
	m.saveSession()
	if m.saveSession(); m.sessionSaved == "" {
		t.Fatal("nothing saved")
	}
	var m2 model
	m2.restore()
	if len(m2.tabs) != 1 {
		t.Fatalf("tabs: %d", len(m2.tabs))
	}
	r := m2.tabs[0]
	if r.name != "Acme" || !r.orgOnly || r.session != "sess-1" || len(r.chat) != 3 || r.chat[1].text != "82% used" || r.chat[2].who != "note" {
		t.Fatalf("restored: %+v", r)
	}
	var m3 model // a fresh model must not save over the session before restoring it
	m3.saveSession()
	if _, err := os.Stat(sessionPath()); err != nil {
		t.Fatal(err)
	}
}

func TestBroadcastTyping(t *testing.T) {
	a, b := &Term{}, &Term{}
	tab := &Tab{panes: []*Pane{{id: 1, term: a}, {id: 2, term: b}}}
	if got := tab.typingTo(a); len(got) != 1 || got[0] != a {
		t.Fatalf("off: %v", got)
	}
	var m model
	m.toggleBroadcast(tab)
	if got := tab.typingTo(b); !tab.broadcast || len(got) != 2 || got[0] != b || got[1] != a {
		t.Fatalf("on: %v", got)
	}
	one := &Tab{panes: []*Pane{{id: 1, term: a}}}
	if m.toggleBroadcast(one); one.broadcast {
		t.Fatal("broadcast with a single pane")
	}
}

func TestServerNotes(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := addNote("Acme", "web-01", "nginx config in /srv/conf;\nreload php-fpm after changes, password=hunter2"); err != nil {
		t.Fatal(err)
	}
	tab := &Tab{org: "Acme"}
	n := tab.notesFor("web-01")
	if !strings.Contains(n, `<notes server="web-01">`) || !strings.Contains(n, "nginx config in /srv/conf; reload php-fpm") || strings.Contains(n, "hunter2") {
		t.Fatalf("notes: %q", n)
	}
	if tab.notesFor("web-01") != "" || tab.notesFor("db-01") != "" {
		t.Fatal("notes sent twice, or for a server without notes")
	}
}

func TestRunbooks(t *testing.T) {
	org := t.TempDir()
	tab := &Tab{dir: org, group: "web"}
	p, err := saveRunbook(tab, "Restart PHP-FPM!", "# Restart php-fpm\n\n1. Restart\n```sh\nsudo systemctl restart php{{version|8.2}}-fpm on {{host}}\n```\n")
	if err != nil || p != filepath.Join(org, "runbooks", "restart-php-fpm.md") {
		t.Fatalf("saved %q, %v", p, err)
	}
	if p2, _ := saveRunbook(tab, "restart php-fpm", "# again"); p2 != filepath.Join(org, "runbooks", "restart-php-fpm-2.md") {
		t.Fatalf("overwrote: %q", p2)
	}
	rbs := runbooks(tab)
	if len(rbs) != 2 || rbs[1].title != "Restart php-fpm" {
		t.Fatalf("runbooks: %+v", rbs)
	}
	prompt, err := runbookPrompt(rbs[1])
	if err != nil || !strings.Contains(prompt, "parameters are version, host") || !strings.Contains(prompt, "<runbook>") {
		t.Fatalf("prompt: %q %v", prompt, err)
	}
}

func TestNagiosAlerts(t *testing.T) {
	hosts := map[string]nagiosObj{
		"web-01.acme.lan": {Status: 2, CurrentAttempt: 1, MaxAttempts: 3},
		"db-01":           {Status: 4, Output: "PING CRITICAL", CurrentAttempt: 3, MaxAttempts: 3},
	}
	svcs := map[string]map[string]nagiosObj{
		"web-01.acme.lan": {
			"disk": {Status: 16, Output: "DISK CRITICAL 95%", CurrentAttempt: 3, MaxAttempts: 3},
			"load": {Status: 16, CurrentAttempt: 1, MaxAttempts: 3}, // soft
			"ntp":  {Status: 16, CurrentAttempt: 3, MaxAttempts: 3, Acked: true},
			"swap": {Status: 4, CurrentAttempt: 3, MaxAttempts: 3}, // warning
		},
		"db-01": {"mysql": {Status: 16, CurrentAttempt: 3, MaxAttempts: 3}}, // its host is down
	}
	as := nagiosAlerts("Acme", "/dir", hosts, svcs)
	if len(as) != 2 || as[0].name() != "db-01" || as[1].name() != "web-01.acme.lan / disk" {
		t.Fatalf("alerts: %+v", as)
	}
	var got []string
	defer func(f func(string, string)) { desktopNotify = f }(desktopNotify)
	desktopNotify = func(title, body string) { got = append(got, title) }
	m := model{}
	m.setAlerts(alertsMsg{src: "Acme/n", alerts: as[:1], first: true})
	m.setAlerts(alertsMsg{src: "Acme/n", alerts: as})
	if len(got) != 1 || got[0] != "CRITICAL · web-01.acme.lan / disk" {
		t.Fatalf("notified: %q", got)
	}
	if st := m.hostAlert(node{org: "Acme", label: "web-01", host: "web-01"}); st != "CRITICAL" {
		t.Fatalf("host alert: %q", st)
	}
	if len(m.palItems()) < 2 || m.palItems()[0].kind != "alerts" {
		t.Fatal("alerts not first in the palette")
	}
}

func TestSessionReport(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	tab := &Tab{org: "Acme", name: "web-01", group: "web", opened: time.Now().Add(-time.Minute)}
	tab.chat = []*chatEntry{{who: "you", text: "why is nginx down?"}, {who: "tool", text: "read · systemctl status nginx"}, {who: "claude", text: "Config error on line 12."}}
	auditBlock(tab, "web-01", Block{Cmd: "nginx -t", Output: "emerg: unknown directive\n", Exit: 1, ByClaude: true})
	audit(auditEntry{Org: "Acme", Host: "web-01", Event: "approval", Cmd: "systemctl restart nginx", Verdict: "approved"})
	p, err := saveReport(tab)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	for _, want := range []string{"# Session report: Acme · web-01", "**You:** why is nginx down?", "Config error on line 12.",
		"claude · exit 1", "`nginx -t`", "emerg: unknown directive", "approval **approved**: `systemctl restart nginx`"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("report lacks %q:\n%s", want, b)
		}
	}
}

func TestSetModel(t *testing.T) {
	tab := &Tab{}
	m := model{tabs: []*Tab{tab}}
	m.setModel(tab, "haiku")
	if tab.model != "haiku" || len(tab.chat) != 1 {
		t.Fatalf("model %q chat %d", tab.model, len(tab.chat))
	}
	tab.busy = true
	if m.setModel(tab, "opus"); tab.model != "haiku" {
		t.Fatal("switched models mid-turn")
	}
}

func TestChangesScript(t *testing.T) {
	needSh(t)
	out, err := exec.Command("sh", "-c", changesScript, "changes", "2").CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for _, h := range []string{"== packages", "== files changed under /etc", "== services", "== logins", "== containers"} {
		if !strings.Contains(string(out), h) {
			t.Errorf("no %q in:\n%s", h, out)
		}
	}
}

func TestParseForward(t *testing.T) {
	for q, want := range map[string]string{
		"forward 8080":                  "L 8080:localhost:8080",
		"fwd 9000:5432":                 "L 9000:localhost:5432",
		"forward 9000:db.internal:5432": "L 9000:db.internal:5432",
		"rforward 3000":                 "R 3000:localhost:3000",
		"rforward 9000:10.0.0.5:3000":   "R 9000:10.0.0.5:3000",
		"socks 1080":                    "D 1080",
		"socks 1080:x":                  "",
		"forward db:5432":               "",
		"forward 9000:db":               "",
		"forward 70000":                 "",
		"open web-01":                   "",
	} {
		kind, spec, ok := parseForward(q)
		if got := kind + " " + spec; ok != (want != "") || ok && got != want {
			t.Errorf("parseForward(%q) = %q, %v", q, got, ok)
		}
	}
	for f, want := range map[portFwd]string{
		{kind: "R", spec: "8080:localhost:80"}:     "{8080 localhost 80}",
		{kind: "L", spec: "9000:db.internal:5432"}: "{9000 db.internal 5432}",
		{kind: "D", spec: "1080"}:                  "{1080 socks 0}",
		{kind: "D", spec: "1080:x:1"}:              "",
		{kind: "L", spec: "1080"}:                  "",
		{kind: "L", spec: "x:a:1"}:                 "",
		{kind: "L", spec: "1:a:x"}:                 "",
	} {
		fw, err := f.forward()
		got := fmt.Sprintf("{%d %s %d}", fw.ListenPort, fw.ConnectHost, fw.ConnectPort)
		if (err == nil) != (want != "") || err == nil && got != want || fw.ListenHost != "" {
			t.Errorf("%v: %s %v", f, got, err)
		}
	}
}

// TestChmodRemote: the file panel's chmod, through the "local" transport.
func TestChmodRemote(t *testing.T) {
	needSh(t)
	dir := t.TempDir()
	f := filepath.Join(dir, "it's.sh")
	os.WriteFile(f, nil, 0o644)
	if err := chmodRemote(context.Background(), "local", dir, "u+x", []string{f}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(f); fi.Mode().Perm() != 0o744 {
		t.Errorf("mode %o", fi.Mode().Perm())
	}
}

// TestWakeOnLAN: "#@ wol" reaches the host and the packet is the standard
// magic one.
func TestWakeOnLAN(t *testing.T) {
	if wine() {
		t.Skip("Wine's UDP sockets take no broadcast option (WSAEOPNOTSUPP)")
	}
	org := filepath.Join(t.TempDir(), "Acme")
	dir := filepath.Join(org, infraDir)
	write(t, filepath.Join(org, ".desk", serversFile), "Host nas\n    HostName 10.0.0.9\n    #@ wol 52:54:00:12:34:56 127.0.0.1:0\n    ServerAliveInterval 60\n")
	hs := serverHosts(dir)
	if len(hs) != 1 || hs[0].wol != "52:54:00:12:34:56 127.0.0.1:0" {
		t.Fatalf("hosts %+v", hs)
	}
	if o := sshOpts("nas", dir); o.Overrides != nil {
		t.Error("the config's own ServerAliveInterval must win")
	}
	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := wake("52:54:00:12:34:56 " + ln.LocalAddr().String()); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 200)
	ln.SetDeadline(time.Now().Add(2 * time.Second))
	n, _, err := ln.ReadFrom(buf)
	mac, _ := net.ParseMAC("52:54:00:12:34:56")
	if err != nil || n != 102 || !bytes.Equal(buf[:6], []byte{255, 255, 255, 255, 255, 255}) || !bytes.Equal(buf[96:102], mac) {
		t.Errorf("packet %d %x %v", n, buf[:n], err)
	}
	if wake("nope") == nil {
		t.Error("bad MAC must fail")
	}
}

// TestRedial: a pane whose ssh drops (exit 255 after it was up) is kept,
// shows "reconnecting" with its history, and dials again.
func TestRedial(t *testing.T) {
	term, err := startCmd("local", infraHelper("drop"), 80, 24, func() {}, paneState{})
	if err != nil {
		t.Fatal(err)
	}
	term.markReached()
	for i := 0; i < 100 && !term.Lost(); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if !term.Lost() {
		t.Fatal("exit 255 after connecting must count as lost")
	}
	m := &model{}
	tab := m.newTab("o", "g", t.TempDir(), "srv")
	p := &Pane{term: term, dir: t.TempDir(), name: "srv", waitUntil: time.Now().Add(time.Hour)}
	tab.panes, tab.term = []*Pane{p}, term
	m.redial()
	m.reap()
	if len(m.tabs) != 1 || len(tab.panes) != 1 || p.connState() != "reconnecting" {
		t.Fatalf("lost pane dropped or wrong state: %d %q", len(tab.panes), p.connState())
	}
	if p.term == term || !strings.Contains(p.kept.Text, "before the drop") {
		t.Errorf("no redial, or history lost: %q", p.kept.Text)
	}
	p.term.Close()
}

func TestFindInScrollback(t *testing.T) {
	term := &Term{emu: vt.NewEmulator(40, 5)}
	for i := range 20 {
		fmt.Fprintf(term.emu, "line %d %s\r\n", i, map[bool]string{true: "ERROR disk", false: "ok"}[i%7 == 3])
	}
	hits := term.Find("error")
	if len(hits) != 3 || hits[0] != 3 || hits[2] != 17 {
		t.Fatalf("hits: %v", hits)
	}
	if newest, older := term.ScrollFor(hits[2]), term.ScrollFor(hits[1]); older <= newest {
		t.Fatalf("an older match scrolls further: %d vs %d", older, newest)
	}
}

func TestRecording(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	p := recordingPath("Acme", "web-01", time.Now())
	r, err := newRecorder(p, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	r.event("o", []byte("héllo \xe2\x82")) // "€" split across two reads
	r.event("o", []byte("\xac done\r\n"))
	r.event("r", []byte("100x30"))
	r2, err := newRecorder(p, 80, 24) // a second pane on web-01 the same second
	if err != nil || r2.f.Name() != strings.TrimSuffix(p, ".cast")+"-2.cast" {
		t.Fatalf("second recording: %v", err)
	}
	r2.close()
	r.close()                   // flushes what the writer still buffers
	r.close()                   // a second close is nothing
	r.event("r", []byte("1x1")) // after close (a resize of an ended pane): dropped
	if _, open := recordingsOpen.Load(p); open {
		t.Error("a closed recording is still listed as being written")
	}
	b, _ := os.ReadFile(p)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var hdr map[string]any
	if len(lines) != 4 || json.Unmarshal([]byte(lines[0]), &hdr) != nil || hdr["version"] != 2.0 || hdr["width"] != 80.0 {
		t.Fatalf("cast:\n%s", b)
	}
	var ev1, ev2 []any
	json.Unmarshal([]byte(lines[1]), &ev1)
	json.Unmarshal([]byte(lines[2]), &ev2)
	if ev1[1] != "o" || ev1[2] != "héllo " || ev2[2] != "€ done\r\n" || !strings.Contains(lines[3], `"r","100x30"`) {
		t.Fatalf("events:\n%s", b)
	}
}

// TestRecordingFlushes: a recording reaches the disk while the pane is still
// open, within a second or so.
func TestRecordingFlushes(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	p := recordingPath("Acme", "web-02", time.Now())
	r, err := newRecorder(p, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	r.event("o", []byte("uptime\r\n"))
	for end := time.Now().Add(3 * recFlush); ; time.Sleep(20 * time.Millisecond) {
		if b, _ := os.ReadFile(p); strings.Contains(string(b), `"o","uptime\r\n"`) {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the event never reached the file before close")
		}
	}
}

func TestPruneRecordings(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Now()
	mk := func(org, name string, size int64, age time.Duration) string {
		p := filepath.Join(stateDir(), "recordings", org, name)
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, nil, 0o600)
		os.Truncate(p, size) // sparse: sizes without the disk
		os.Chtimes(p, now.Add(-age), now.Add(-age))
		return p
	}
	old := mk("Acme", "old.cast", 10, 8*24*time.Hour)
	live := mk("Acme", "live.cast", 10, 9*24*time.Hour) // a pane still writing it
	recent := mk("Acme", "recent.cast", 10, time.Hour)
	big1 := mk("Globex", "1.cast", 400<<20, 3*time.Hour) // 1.2 GiB in all: the oldest goes
	big2 := mk("Globex", "2.cast", 400<<20, 2*time.Hour)
	big3 := mk("Globex", "3.cast", 400<<20, time.Hour)
	recordingsOpen.Store(live, true)
	defer recordingsOpen.Delete(live)
	pruneRecordings(now)
	for p, want := range map[string]bool{old: false, live: true, recent: true, big1: false, big2: true, big3: true} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s: kept = %v, want %v", filepath.Base(p), err == nil, want)
		}
	}
}

func TestRecordingDefault(t *testing.T) {
	for toml, want := range map[string]bool{"": true, "record = true\n": true, "record = false\n": false} {
		dir := filepath.Join(t.TempDir(), "Acme", infraDir)
		write(t, filepath.Join(orgOfInfra(dir), ".desk", "desk.toml"), toml)
		if o, err := orgFor(dir); err != nil || o.records() != want {
			t.Errorf("%q: records() = %v, want %v (%v)", toml, o.records(), want, err)
		}
	}
	if !(*orgConfig)(nil).records() {
		t.Error("no config: want recording")
	}
}

func TestDailyHealthDue(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "Acme", infraDir)
	write(t, filepath.Join(orgOfInfra(dir), ".desk", "desk.toml"), "[schedule]\nhealth = \"08:30\"\n")
	m := model{tree: []node{{depth: 0, label: "Acme", org: "Acme", dir: dir}}}
	day := time.Date(2026, 10, 26, 0, 0, 0, 0, time.Local)
	if len(m.dueHealth(day.Add(8*time.Hour))) != 0 {
		t.Fatal("due before its time")
	}
	if due := m.dueHealth(day.Add(9 * time.Hour)); len(due) != 1 || due[0].org != "Acme" {
		t.Fatalf("not due after its time: %v", due)
	}
	os.MkdirAll(filepath.Dir(healthStamp("Acme")), 0o700)
	os.WriteFile(healthStamp("Acme"), []byte("2026-10-26"), 0o600)
	if len(m.dueHealth(day.Add(10*time.Hour))) != 0 || len(m.dueHealth(day.Add(33*time.Hour))) != 1 {
		t.Fatal("ran twice a day, or not the next day")
	}
}

// TestDeskTomlConfig: the infra parts come from the org's desk.toml; its
// nagios login is its user and secret.
func TestDeskTomlConfig(t *testing.T) {
	root := t.TempDir()
	infra := filepath.Join(root, "Globex", infraDir)
	os.MkdirAll(infra, 0o755)
	os.WriteFile(filepath.Join(root, "Globex", ".desk", "desk.toml"), []byte(`record = false
[[vault]]
name = "hv"
kind = "hashicorp"
url = "https://vault.example.com"
mount = "kv"
[[nagios]]
url = "https://mon.example.com/nagios"
user = "me"
token_env = "NAGIOS_PW"
alerts = true
[[metrics]]
name = "vm"
type = "victoriametrics"
url = "https://vm.example.com"
[approvals]
allow = ["uptime"]
[schedule]
health = "08:30"
`), 0o644)
	o, err := orgFor(infra)
	if err != nil {
		t.Fatal(err)
	}
	if o.records() || o.Schedule.Health != "08:30" || !o.allows("uptime") || o.Nagios[0].Name != "mon.example.com" || !o.Nagios[0].Alerts ||
		o.Vaults[0].Mount != "kv" || o.Metrics[0].Type != "victoriametrics" {
		t.Fatalf("%+v", o)
	}
	if g := strings.Join(o.toolGroups(), ","); g != "secrets,nagios,metrics" {
		t.Errorf("groups %s", g)
	}
	t.Setenv("NAGIOS_PW", "pw")
	req := httptest.NewRequest("GET", "/", nil)
	if err := o.authorize(context.Background(), o.Nagios[0], req); err != nil {
		t.Fatal(err)
	}
	if u, p, ok := req.BasicAuth(); !ok || u != "me" || p != "pw" {
		t.Errorf("nagios login %q %q", u, p)
	}
	if _, _, err := (&orgConfig{Vaults: []VaultCfg{{Name: "x", Type: "1password"}}}).vault("x"); err == nil {
		t.Error("CLI vaults are gone: want an error")
	}
}

func TestHashiVault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "tok" {
			w.WriteHeader(403)
			return
		}
		switch r.URL.Path {
		case "/v1/kv/metadata/":
			w.Write([]byte(`{"data":{"keys":["servers/","top"]}}`))
		case "/v1/kv/metadata/servers/":
			w.Write([]byte(`{"data":{"keys":["web-01"]}}`))
		case "/v1/kv/data/servers/web-01":
			w.Write([]byte(`{"data":{"data":{"username":"root","password":"pw3"}}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	t.Setenv("MY_TOKEN", "tok")
	h := hashiVault{cfg: VaultCfg{Name: "hv", URL: srv.URL, Mount: "kv", TokenEnv: "MY_TOKEN"}}
	ctx := context.Background()
	if items, err := h.search(ctx, "web"); err != nil || len(items) != 1 || items[0].ID != "servers/web-01" {
		t.Fatalf("search: %+v %v", items, err)
	}
	if it, err := h.get(ctx, "servers/web-01"); err != nil || it.Password != "pw3" {
		t.Fatalf("get: %+v %v", it, err)
	}
}

// TestMatchRunes: the sidebar bolds where the search's first word lands.
func TestMatchRunes(t *testing.T) {
	if got := matchRunes("Wb x", "web-01"); !slices.Equal(got, []int{0, 2}) {
		t.Errorf("got %v", got)
	}
	if matchRunes("zz", "web-01") != nil || matchRunes("", "web") != nil {
		t.Error("matched what isn't there")
	}
}

// TestKeepJoinsWrappedRows: a line the terminal wrapped is kept as one line
// (in the scrollback too), so a restore at another width wraps it again.
func TestKeepJoinsWrappedRows(t *testing.T) {
	term := &Term{emu: vt.NewEmulator(10, 3)}
	term.emu.Write([]byte("abcdefghijklmnopqrstuv\r\nshort\r\n1\r\n2\r\n3\r\n4"))
	if got := term.Keep().Text; got != "abcdefghijklmnopqrstuv\nshort\n1\n2\n3\n4" {
		t.Errorf("kept %q", got)
	}
}

// TestContextMeter: usage and compaction move the tab's context count; the
// exit of a process desk closed itself is ignored.
func TestContextMeter(t *testing.T) {
	if n := (tokenUsage{Input: 2, CacheWrite: 37046, CacheRead: 10}).total(); n != 37058 {
		t.Fatalf("total %d", n)
	}
	tab := &Tab{}
	if s := ctxText(tab); s != "context: nothing yet" {
		t.Errorf("empty: %q", s)
	}
	m := model{tabs: []*Tab{tab}}
	r, _ := m.update(claudeMsg{tab: tab, ctx: 37058})
	r, _ = r.(model).update(claudeMsg{tab: tab, done: true, window: 1000000})
	if s := ctxText(tab); s != "37.1k / 1M tokens (3%)" {
		t.Errorf("after a turn: %q", s)
	}
	r, _ = r.(model).update(claudeMsg{tab: tab, done: true, compacted: [2]int{37058, 4380}})
	if tab.ctxTokens != 4380 || !strings.Contains(tab.chat[len(tab.chat)-1].text, "37.1k → 4380") {
		t.Errorf("compacted: %d %q", tab.ctxTokens, tab.chat[len(tab.chat)-1].text)
	}
	n := len(tab.chat)
	tab.busy = true
	r.(model).update(claudeMsg{tab: tab, done: true, err: fmt.Errorf("claude exited: signal: killed"), exited: &Claude{}})
	if len(tab.chat) != n || !tab.busy {
		t.Error("a closed process's exit reached the tab")
	}
}

func TestWordAt(t *testing.T) {
	line := []rune("$ ssh root@db1:22 (see /var/log/x.log)  ok")
	for _, c := range []struct {
		at   int
		want string
	}{{8, "root@db1:22"}, {25, "/var/log/x.log"}, {18, "("}, {39, "  "}, {41, "ok"}} {
		lo, hi := wordAt(line, c.at)
		if got := string(line[lo:hi]); got != c.want {
			t.Errorf("at %d: %q, want %q", c.at, got, c.want)
		}
	}
	if lo, hi := wordAt(line, 99); lo != hi {
		t.Error("past the end selected something")
	}
}

// TestWorkspace: desk's API opens a host, lists it in the tree with its
// state, draws only its org's tabs, and says which org is active.
func TestWorkspace(t *testing.T) {
	for _, v := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(v, t.TempDir())
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "Acme", ".desk", serversFile), "#@ group g\nHost local\n")
	os.WriteFile(filepath.Join(root, "Acme", ".desk", "desk.toml"), []byte("record = false\n"), 0o644)
	defer func() { dispatch, repaint, guiReplies = nil, nil, false }() // other tests run without a loop
	var frames atomic.Int32
	w := New(root, func() { frames.Add(1) })
	defer w.Close()
	if tr := w.Tree(); len(tr) != 1 || tr[0].Name != "Acme" || len(tr[0].Groups) != 1 || tr[0].Groups[0].Hosts[0].State != Idle {
		t.Fatalf("tree %+v", tr)
	}
	if !w.OpenHost("Acme", "g", "local") || w.OpenHost("Acme", "g", "nope") {
		t.Fatal("OpenHost")
	}
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end) && w.Tree()[0].State != Connected; time.Sleep(50 * time.Millisecond) {
	}
	if h := w.Tree()[0].Groups[0].Hosts[0]; h.State != Connected || !h.Open {
		t.Fatalf("host %+v", h)
	}
	gtx := layout.Context{Ops: new(op.Ops), Constraints: layout.Exact(image.Pt(1200, 800)), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	if d := w.Layout(gtx, "Acme"); d.Size.X != 1200 || w.ActiveOrg() != "Acme" || len(w.u.orgTabs()) != 1 || w.u.tab() == nil {
		t.Fatalf("layout %v, active %q", d.Size, w.ActiveOrg())
	}
	w.Layout(gtx, "Globex") // another org: none of Acme's tabs
	if len(w.u.orgTabs()) != 0 || w.u.tab() != nil || w.ActiveOrg() != "Acme" {
		t.Error("another org's workspace shows Acme's tab")
	}
	if frames.Load() == 0 {
		t.Error("never asked for a frame")
	}
}
