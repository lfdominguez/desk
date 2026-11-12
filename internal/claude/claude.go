// Package claude runs a card's Claude session: one long-lived `claude -p`
// process fed stream-json on stdin, its stream-json output turned into
// Events. Each organization with a profile in ~/Secure/Claudes/<Org> runs
// under that account (bwrap binds it over ~/.claude; on macOS and Windows,
// CLAUDE_CONFIG_DIR points at it); others use the personal login.
package claude

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
)

// Event kinds.
const (
	KindInit     = "init"     // Session is known
	KindText     = "text"     // Claude wrote Text
	KindTool     = "tool"     // Claude used a tool: Text is a one-line summary
	KindResult   = "result"   // the turn ended
	KindExit     = "exit"     // the process ended; Text is why, if it failed
	KindAsk      = "ask"      // Claude waits on the user: Ask is its questions, or a tool it wants to run
	KindUsage    = "usage"    // Context is how full the context window is now
	KindArtifact = "artifact" // Text is the URL of an artifact Claude published
	KindTask     = "task"     // a subagent or background shell started or changed: Task
	KindToolDone = "tooldone" // a tool use (ToolID, a subagent's when Parent) has its result
)

// shellOut is where a background shell's tool result says its output goes.
// It reads the raw JSON, where Windows' backslashes come doubled.
var shellOut = regexp.MustCompile(`with ID: (\w+)\. Output is being written to: ((?:[^\s"\\]|\\\\)+)`)

// artifactURL is a published claude.ai artifact's link, taken only from the
// Artifact tool's result (Start): any other tool's output (a grep over tests)
// or Claude's text may quote one that was never published.
var artifactURL = regexp.MustCompile(`https://claude\.ai/(?:code/)?artifact/[A-Za-z0-9-]+`)

type Event struct {
	Kind    string
	Text    string
	Session string
	Tokens  int      // result: the turn's input, cache and output tokens
	Cost    float64  // result: the process's cost so far, USD (cumulative)
	Error   bool     // result: the turn failed (Text says how); an interrupt has no Text
	Denied  []string // result: tool calls the permission mode refused
	Ask     *Request // ask
	Context int      // usage: tokens in the context (input and cache, the last call's)
	Window  int      // result: the model's context window, tokens
	Parent  string   // text, tool: the subagent's (its Agent tool use id) when not the session's own
	ToolID  string   // tool: the tool use's id; artifact: the tool use whose result has it
	Tool    string   // tool: the tool's name
	To      string   // tool: SendMessage's addressee, a subagent (its task id) it resumes: its messages then name this tool use
	Cmd     string   // tool: a Bash call's whole command (Text cuts it short)
	Detail  string   // tool: a Bash call's description, Claude's words for what it does
	Path    string   // tool: the file an Edit or Write changed
	Edits   []Edit   // tool: an Edit's (or MultiEdit's) changes
	Lines   int      // tool: a Write's size: lines and bytes (its text stays out)
	Bytes   int
	Task    *Task // task
}

// Edit is one change an Edit tool use made: Old replaced by New.
type Edit struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// Task is a subagent or a background shell of the session, as Claude Code
// reports it (task_started, _progress, _updated, _notification). An update
// carries only what changed: zero fields keep what was known.
type Task struct {
	ID      string
	ToolUse string // the Agent or Bash tool use that started it: a subagent's messages name it as their Parent
	Shell   bool   // a background shell, else a subagent
	Name    string // its description
	Agent   string // subagent_type
	Model   string // the model it runs on, from its messages
	Prompt  string
	Status  string // running, completed, failed, killed
	Note    string // what it is on now
	Tokens  int
	Tools   int
	Summary string
	Output  string // the file its output goes to
}

// Request is Claude asking before a tool runs (--permission-prompt-tool
// stdio): its AskUserQuestion interview, or permission for another tool.
// The turn waits until Proc.Respond answers it.
type Request struct {
	ID        string          // the control request's, to answer
	Tool      string          // AskUserQuestion, Bash…
	Input     json.RawMessage // the tool's input, sent back (with the answers) on allow
	Line      string          // the tool and its most telling input, for permission
	Questions []Question      // AskUserQuestion's
}

// Question is one of an interview's.
type Question struct {
	Question string `json:"question"`
	Header   string `json:"header"`
	Multi    bool   `json:"multiSelect"`
	Options  []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"options"`
}

// Answered is AskUserQuestion's input with the user's answers, by
// question: what allowing it takes.
func Answered(input json.RawMessage, answers map[string]string) json.RawMessage {
	var m map[string]any
	json.Unmarshal(input, &m)
	if m == nil {
		m = map[string]any{}
	}
	m["answers"] = answers
	b, _ := json.Marshal(m)
	return b
}

// Parse turns one line of stream-json into events; anything it doesn't
// know is none.
func Parse(line []byte) []Event {
	var ev struct {
		Type, Subtype string
		Session       string  `json:"session_id"`
		IsError       bool    `json:"is_error"`
		Result        string  `json:"result"`
		Cost          float64 `json:"total_cost_usd"`
		Usage         usage   `json:"usage"`
		Denials       []struct {
			Tool string `json:"tool_name"`
		} `json:"permission_denials"`
		Parent  string `json:"parent_tool_use_id"`
		Message struct {
			Model   string
			Usage   usage
			Content []struct {
				Type, Text, Name, ID string
				Input                map[string]any
				ToolUseID            string          `json:"tool_use_id"`
				Content              json.RawMessage `json:"content"`
			}
		}
		TaskID      string `json:"task_id"`
		ToolUseID   string `json:"tool_use_id"`
		Description string `json:"description"`
		TaskType    string `json:"task_type"`
		Subagent    string `json:"subagent_type"`
		Prompt      string `json:"prompt"`
		Status      string `json:"status"`
		Summary     string `json:"summary"`
		OutputFile  string `json:"output_file"`
		Patch       struct {
			Status string `json:"status"`
		} `json:"patch"`
		Models map[string]struct {
			Window int `json:"contextWindow"`
		} `json:"modelUsage"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype string          `json:"subtype"`
			Tool    string          `json:"tool_name"`
			Input   json.RawMessage `json:"input"`
		} `json:"request"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return nil
	}
	out := func() []Event {
		switch ev.Type {
		case "control_request":
			if ev.Request.Subtype != "can_use_tool" {
				return nil
			}
			r := &Request{ID: ev.RequestID, Tool: ev.Request.Tool, Input: ev.Request.Input}
			var in map[string]any
			json.Unmarshal(r.Input, &in)
			r.Line = toolLine(r.Tool, in)
			if r.Tool == "AskUserQuestion" {
				var q struct{ Questions []Question }
				json.Unmarshal(r.Input, &q)
				r.Questions = q.Questions
			}
			return []Event{{Kind: KindAsk, Ask: r}}
		case "system":
			t := &Task{ID: ev.TaskID}
			switch ev.Subtype {
			case "init":
				return []Event{{Kind: KindInit, Session: ev.Session}}
			case "task_started":
				t.ToolUse, t.Shell, t.Name, t.Agent, t.Prompt, t.Status = ev.ToolUseID, ev.TaskType == "local_bash", ev.Description, ev.Subagent, ev.Prompt, "running"
			case "task_progress":
				t.Note, t.Tokens, t.Tools = ev.Description, ev.Usage.Tokens, ev.Usage.ToolUses
			case "task_updated":
				t.Status = ev.Patch.Status
			case "task_notification":
				t.Status, t.Summary, t.Output = ev.Status, ev.Summary, ev.OutputFile
			default:
				return nil
			}
			if t.ID == "" {
				return nil
			}
			return []Event{{Kind: KindTask, Task: t}}
		case "user": // tool results: a background shell's says where its output goes
			var out []Event
			for _, c := range ev.Message.Content {
				if c.Type != "tool_result" {
					continue
				}
				out = append(out, Event{Kind: KindToolDone, ToolID: c.ToolUseID, Parent: ev.Parent})
				for _, u := range slices.Compact(artifactURL.FindAllString(string(c.Content), -1)) {
					out = append(out, Event{Kind: KindArtifact, Text: u, ToolID: c.ToolUseID})
				}
				if m := shellOut.FindStringSubmatch(string(c.Content)); m != nil {
					out = append(out, Event{Kind: KindTask, Task: &Task{ID: m[1], Output: strings.ReplaceAll(strings.TrimSuffix(m[2], "."), `\\`, `\`)}})
				}
			}
			return out
		case "assistant":
			var out []Event
			for _, c := range ev.Message.Content {
				switch c.Type {
				case "text":
					if strings.TrimSpace(c.Text) != "" {
						out = append(out, Event{Kind: KindText, Text: c.Text, Parent: ev.Parent})
					}
				case "tool_use":
					e := Event{Kind: KindTool, Text: toolLine(c.Name, c.Input), Parent: ev.Parent, ToolID: c.ID, Tool: c.Name}
					switch c.Name {
					case "SendMessage":
						e.To, _ = c.Input["to"].(string)
					case "Bash":
						e.Cmd, _ = c.Input["command"].(string)
						e.Detail, _ = c.Input["description"].(string)
					case "Edit", "MultiEdit":
						e.Path, _ = c.Input["file_path"].(string)
						edits := []any{c.Input}
						if es, ok := c.Input["edits"].([]any); ok {
							edits = es
						}
						for _, x := range edits {
							m, _ := x.(map[string]any)
							o, _ := m["old_string"].(string)
							n, _ := m["new_string"].(string)
							if o != "" || n != "" {
								e.Edits = append(e.Edits, Edit{o, n})
							}
						}
					case "Write":
						e.Path, _ = c.Input["file_path"].(string)
						body, _ := c.Input["content"].(string)
						e.Lines, e.Bytes = strings.Count(strings.TrimSuffix(body, "\n"), "\n")+1, len(body)
						if body == "" {
							e.Lines = 0
						}
					}
					out = append(out, e)
				}
			}
			if ev.Parent != "" && ev.Message.Model != "" { // a subagent's: its model
				out = append(out, Event{Kind: KindTask, Task: &Task{ToolUse: ev.Parent, Model: ev.Message.Model}})
			}
			if u := ev.Message.Usage; u.total() > 0 && ev.Parent == "" { // a subagent's context is its own
				out = append(out, Event{Kind: KindUsage, Context: u.total() - u.Output})
			}
			return out
		case "result":
			e := Event{Kind: KindResult, Session: ev.Session, Cost: ev.Cost, Tokens: ev.Usage.total(), Error: ev.IsError}
			if ev.IsError {
				e.Text = ev.Result
			}
			for _, d := range ev.Denials {
				e.Denied = append(e.Denied, d.Tool)
			}
			for _, m := range ev.Models {
				e.Window = max(e.Window, m.Window)
			}
			return []Event{e}
		}
		return nil
	}()
	return out
}

type usage struct {
	Input      int `json:"input_tokens"`
	CacheWrite int `json:"cache_creation_input_tokens"`
	CacheRead  int `json:"cache_read_input_tokens"`
	Output     int `json:"output_tokens"`
	Tokens     int `json:"total_tokens"` // a task's progress
	ToolUses   int `json:"tool_uses"`
}

func (u usage) total() int { return u.Input + u.CacheWrite + u.CacheRead + u.Output }

// toolLine is "Bash  go test ./..." : the tool and its most telling input.
func toolLine(name string, in map[string]any) string {
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "query", "description", "prompt"} {
		if s, ok := in[k].(string); ok && s != "" {
			s = strings.Join(strings.Fields(s), " ")
			if r := []rune(s); len(r) > 90 {
				s = string(r[:89]) + "…"
			}
			return name + "  " + s
		}
	}
	return name
}

// addRoot is --add-dir root when a session works in a folder inside it:
// Claude Code loads .claude/skills only from where it starts, so an
// organization's own skills (and the files beside them) would be missing.
func addRoot(dir, root string) []string {
	if root == "" || filepath.Clean(dir) == filepath.Clean(root) {
		return nil
	}
	return []string{"--add-dir=" + root} // = form: the flag is variadic and would swallow a prompt after it
}

// Options start a session.
type Options struct {
	Home   string   // the user's home, where ~/Secure/Claudes lives
	Org    string   // picks the account
	Dir    string   // where Claude works
	Root   string   // the organization's folder, when Dir is one inside it: added, so its skills load
	Resume string   // a session id to continue, "" a new one
	New    string   // the id a new session takes (--session-id), so the card knows it from the start
	System string   // appended to Claude's system prompt
	Args   []string // more flags: AgentArgs
}

// Proc is a running session.
type Proc struct {
	mu    sync.Mutex
	stdin io.WriteCloser
	cmd   *exec.Cmd
}

// Start runs Claude for o; on gets every event, from another goroutine,
// last a KindExit.
func Start(o Options, on func(Event)) (*Proc, error) {
	args := []string{"-p"}
	if o.Resume != "" {
		args = append(args, "--resume", o.Resume)
	} else if o.New != "" {
		args = append(args, "--session-id", o.New)
	}
	// stdio: AskUserQuestion and tools the mode would ask about come to desk as control requests
	args = append(args, "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--permission-mode", "auto", "--permission-prompt-tool", "stdio", "--append-system-prompt", o.System)
	args = append(append(args, o.Args...), addRoot(o.Dir, o.Root)...)
	cmd, err := Command(o.Home, o.Org, args...)
	if err != nil {
		return nil, err
	}
	cmd.Dir = o.Dir
	// CLAUDE_CODE_ARTIFACT: stream-json makes Claude Code an SDK session
	// (entrypoint sdk-cli), whose claude.ai Artifact tools are off unless asked for.
	cmd.Env = append(cmd.Env, "CLAUDE_CODE_ARTIFACT=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr tail
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 16<<20)
		published := map[string]bool{} // the Artifact tool's uses: only their results publish
		for sc.Scan() {
			for _, e := range Parse(sc.Bytes()) {
				if e.Kind == KindTool && e.Tool == "Artifact" {
					published[e.ToolID] = true
				}
				if e.Kind == KindArtifact && !published[e.ToolID] {
					continue
				}
				on(e)
			}
		}
		why := ""
		if err := cmd.Wait(); err != nil {
			why = strings.TrimSpace(err.Error() + ": " + stderr.String())
		}
		on(Event{Kind: KindExit, Text: why})
	}()
	return &Proc{stdin: stdin, cmd: cmd}, nil
}

func (p *Proc) write(v any) error {
	b, _ := json.Marshal(v)
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.stdin.Write(append(b, '\n'))
	return err
}

// Send starts a turn with prompt.
func (p *Proc) Send(prompt string) error {
	return p.write(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": prompt}})
}

// Respond answers request id: allow runs the tool with input (for
// AskUserQuestion, Answered's), else it is refused with msg, which Claude reads.
func (p *Proc) Respond(id string, allow bool, input json.RawMessage, msg string) error {
	r := map[string]any{"behavior": "deny", "message": msg}
	if allow {
		r = map[string]any{"behavior": "allow", "updatedInput": input}
	}
	return p.write(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": id, "response": r}})
}

// Interrupt stops the turn; the process stays for the next one.
func (p *Proc) Interrupt() error {
	return p.write(map[string]any{"type": "control_request", "request_id": "desk-stop", "request": map[string]any{"subtype": "interrupt"}})
}

// Close ends the process.
func (p *Proc) Close() {
	p.stdin.Close()
	p.cmd.Process.Kill()
}

// tail keeps the last 2KB written to it: the end of Claude's stderr.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if len(t.buf) > 2048 {
		t.buf = t.buf[len(t.buf)-2048:]
	}
	return len(b), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(bytes.TrimSpace(t.buf))
}

// AuthFailed says whether msg is Claude Code failing on its login (an
// expired or revoked OAuth session, no login yet): signing in again fixes it.
func AuthFailed(msg string) bool {
	m := strings.ToLower(msg)
	for _, s := range []string{"failed to authenticate", "oauth", "/login", "authentication_error", "not logged in"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// Login is `claude auth login` under org's account (Command's profile); it
// may ask for the code the sign-in page shows, so it runs in a terminal.
// For an org's own account (own) it can never open a browser: the default
// one may be signed in to another Claude account (people keep one browser
// container per account), and signing in there would tie the org's profile
// to that account. So it runs with no way to the desktop (no display, no
// D-Bus, an empty runtime dir), and the user opens the URL it prints
// (LoginURL) where that account lives. The default account opens the
// default browser, as `claude` does. On macOS and Windows (NoBwrap) there
// is no namespace to hide the desktop in: BROWSER=true alone keeps it from
// opening one (on Windows a browser command that isn't there, so it only
// prints the URL).
func Login(home, org string) (cmd *exec.Cmd, own bool, err error) {
	cmd, err = Command(home, org, "auth", "login")
	if err == nil && NoBwrap && slices.Contains(cmd.Env, "CLAUDE_PROFILE="+org) {
		cmd.Env = append(cmd.Env, "BROWSER=true")
		return cmd, true, nil
	}
	if err != nil || filepath.Base(cmd.Path) != "bwrap" { // no profile: the default account
		return cmd, false, err
	}
	run := os.Getenv("XDG_RUNTIME_DIR")
	cmd.Env = append(slices.DeleteFunc(cmd.Env, func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return slices.Contains([]string{"DISPLAY", "WAYLAND_DISPLAY", "WAYLAND_SOCKET", "DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR", "BROWSER"}, k)
	}), "BROWSER=true")
	hide := []string{"--die-with-parent", "--tmpfs", "/tmp/.X11-unix"} // the desktop's sockets, gone inside; no sign-in outlives its terminal
	if run != "" {
		hide = append(hide, "--tmpfs", run)
	}
	at := slices.Index(cmd.Args, "--")
	cmd.Args = slices.Concat(cmd.Args[:at], hide, cmd.Args[at:])
	return cmd, true, nil
}

// LoginURL is the sign-in URL in what `claude auth login` printed, "" until it has.
func LoginURL(screen string) string {
	i := strings.Index(screen, "https://")
	if i < 0 {
		return ""
	}
	u, _, _ := strings.Cut(screen[i:], " ")
	return strings.TrimSpace(strings.SplitN(u, "\n", 2)[0])
}

// Command is `claude args…` under org's account: with a profile folder in
// ~/Secure/Claudes/<org>, bwrap binds it over ~/.claude and
// claude-<org>.json over ~/.claude.json (off Linux, NoBwrap: CLAUDE_CONFIG_DIR
// is the folder, and its own .claude.json the other); else plain claude.
// Inside a profile already ($CLAUDE_PROFILE) it never nests.
func Command(home, org string, args ...string) (*exec.Cmd, error) {
	c, err := command(home, org, args...)
	if err == nil {
		c.Env = slices.DeleteFunc(append(os.Environ(), c.Env...), func(kv string) bool {
			k, _, _ := strings.Cut(kv, "=")
			return slices.Contains(inherited, k)
		})
	}
	return c, err
}

// inherited is what a Claude Code that started desk (a session's shell)
// leaves in the environment: passed on, the card's Claude takes itself for
// that session's child and keeps no transcript, so it can't be resumed.
var inherited = []string{"CLAUDECODE", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_EXECPATH",
	"CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN", "CLAUDE_CODE_SESSION_ATTENDED", "CLAUDE_CODE_SESSION_ID",
	"CLAUDE_EFFORT", "CLAUDE_PID", "AI_AGENT"}

// AccountName says which Claude account command runs org's sessions under:
// its own (~/Secure/Claudes/<org>) or the default one, with the login's
// email when its .claude.json has it.
func AccountName(home, org string) string {
	name, js := "Default account", filepath.Join(home, ".claude.json")
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		js = filepath.Join(d, ".claude.json")
	}
	if fi, err := os.Stat(filepath.Join(home, "Secure", "Claudes", org)); org != "" && err == nil && fi.IsDir() && os.Getenv("CLAUDE_PROFILE") == "" {
		name, js = org+" account", filepath.Join(home, "Secure", "Claudes", "claude-"+strings.ToLower(org)+".json")
		if NoBwrap {
			js = filepath.Join(home, "Secure", "Claudes", org, ".claude.json")
		}
	}
	var v struct {
		OauthAccount struct{ EmailAddress string } `json:"oauthAccount"`
	}
	if b, err := os.ReadFile(js); err == nil && json.Unmarshal(b, &v) == nil && v.OauthAccount.EmailAddress != "" {
		name += " · " + v.OauthAccount.EmailAddress
	}
	return name
}

func command(home, org string, args ...string) (*exec.Cmd, error) {
	bin, err := Bin(home)
	if err != nil {
		return nil, err
	}
	cfg := filepath.Join(home, "Secure", "Claudes", org)
	if fi, err := os.Stat(cfg); org == "" || err != nil || !fi.IsDir() || os.Getenv("CLAUDE_PROFILE") != "" {
		return exec.Command(bin, args...), nil
	}
	if NoBwrap {
		c := exec.Command(bin, args...)
		c.Env = []string{"CLAUDE_CONFIG_DIR=" + cfg, "CLAUDE_PROFILE=" + org}
		return c, nil
	}
	js := filepath.Join(home, "Secure", "Claudes", "claude-"+strings.ToLower(org)+".json")
	if _, err := os.Stat(js); err != nil { // seed it, as the profile's first run would
		seed, err := os.ReadFile(filepath.Join(cfg, ".claude.json"))
		if err != nil {
			seed = []byte("{}")
		}
		if err := os.WriteFile(js, seed, 0o600); err != nil {
			return nil, err
		}
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil { // NixOS: not on PATH, but in the store
		m, _ := filepath.Glob(storeBwrap)
		if len(m) == 0 {
			return nil, fmt.Errorf("profile %s needs bwrap", org)
		}
		bwrap = m[len(m)-1]
	}
	binds := []string{"--dev-bind", "/", "/",
		"--bind", cfg, filepath.Join(home, ".claude"),
		"--bind", js, filepath.Join(home, ".claude.json")}
	for i, src := range []string{filepath.Join(home, ".ssh", "config"), "/etc/ssh/ssh_config"} {
		if real, c, err := sshConfig(src, filepath.Join(cfg, fmt.Sprintf(".ssh-config-%d", i))); err == nil {
			binds = append(binds, "--ro-bind", c, real)
		}
	}
	return exec.Command(bwrap, append(append(binds,
		"--unsetenv", "CLAUDE_CONFIG_DIR", "--setenv", "CLAUDE_PROFILE", org,
		"--", bin), args...)...), nil
}

// NoBwrap: bwrap is Linux's (namespaces); macOS and Windows have none, so
// a profile runs with CLAUDE_CONFIG_DIR at its folder instead; Claude Code
// then keeps its state there, .claude.json included. A variable so tests
// reach both ways on any OS.
var NoBwrap = runtime.GOOS != "linux"

// storeBwrap is where NixOS keeps bwrap off PATH; tests point it elsewhere.
var storeBwrap = "/nix/store/*-bubblewrap-*/bin/bwrap"

// sshConfig copies ssh config src to dst, its Includes inlined, when src
// is a symlink (NixOS and home-manager link both configs into the
// root-owned /nix/store): in bwrap's user namespace root shows as nobody,
// and ssh refuses a config, or a file it includes, that nobody owns ("Bad
// owner or permissions"), so git over ssh fails. real is src's target,
// where the copy goes (bwrap can't mount on a symlink). Copied each start.
// ponytail: one level of Include; nested ones would need recursing.
func sshConfig(src, dst string) (real, dup string, err error) {
	if fi, err := os.Lstat(src); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return "", "", errors.New("not a symlink")
	}
	if real, err = filepath.EvalSymlinks(src); err != nil {
		return "", "", err
	}
	b, err := os.ReadFile(real)
	if err != nil {
		return "", "", err
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || !strings.EqualFold(f[0], "include") {
			out = append(out, l)
			continue
		}
		for _, pat := range f[1:] {
			if strings.HasPrefix(pat, "~/") {
				pat = filepath.Join(filepath.Dir(filepath.Dir(src)), pat[2:]) // src is <home>/.ssh/config
			} else if !filepath.IsAbs(pat) {
				pat = filepath.Join(filepath.Dir(src), pat)
			}
			m, _ := filepath.Glob(pat)
			for _, inc := range m {
				if b, err := os.ReadFile(inc); err == nil {
					out = append(out, string(b))
				}
			}
		}
	}
	return real, dst, os.WriteFile(dst, []byte(strings.Join(out, "\n")), 0o600)
}

// Interactive is the full Claude Code TUI on card session id, in dir under
// org's account: resumed when the session exists, else started with that
// id. The caller closes its own -p process first, so two never share it.
func Interactive(home, org, dir, root, id, system string, extra ...string) (*exec.Cmd, error) {
	args := append(append([]string{"--permission-mode", "auto", "--append-system-prompt", system}, extra...), addRoot(dir, root)...)
	if SessionExists(home, org, id) {
		args = append(args, "--resume", id)
	} else {
		args = append(args, "--session-id", id)
	}
	c, err := Command(home, org, args...)
	if err != nil {
		return nil, err
	}
	c.Dir = dir
	return c, nil
}

// NewSessionID is a random UUID (v4) for --session-id.
func NewSessionID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// SessionExists reports whether Claude has a transcript for session id
// under org's account (its profile's projects, else ~/.claude's): only
// then can it be resumed.
func SessionExists(home, org, id string) bool {
	if id == "" {
		return false
	}
	base := filepath.Join(home, ".claude")
	if p := filepath.Join(home, "Secure", "Claudes", org); org != "" {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() && os.Getenv("CLAUDE_PROFILE") == "" {
			base = p
		}
	}
	m, _ := filepath.Glob(filepath.Join(base, "projects", "*", id+".jsonl"))
	return len(m) > 0
}
