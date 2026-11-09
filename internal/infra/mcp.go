package infra

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	deskorgs "github.com/lfdominguez/desk/internal/orgs"
)

// Claude reaches the shared terminal through a tiny MCP server: claude spawns
// `desk mcp <socket>` (stdio JSON-RPC) and that process forwards each tool call
// over a unix socket to the desk that owns the PTY.

var paneProp = map[string]any{"type": "integer", "description": "Which pane (see <panes>). Default: the one the user is looking at."}

var mcpTools = []map[string]any{
	{
		"name": "run",
		"description": "Run a shell command in the user's live SSH terminal (the same shell they see), wait for it " +
			"to finish and return its exit code and output. Use for inspection and for changes the user asked for. " +
			"For your own reading prefer non-interactive forms (top -bn1, tail -n 100). To start a full-screen or " +
			"never-ending program for the user or for watching (htop, tail -f), run it with a short timeout_seconds " +
			"(2-3): it stays running in the pane and you get its screen. The command shows up in the user's terminal. " +
			"Read-only commands run right away; anything else waits for the user to approve it.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "A single shell command line."},
				"effect": map[string]any{"type": "string", "enum": []string{"read", "write"},
					"description": "read: changes nothing anywhere (no files, services, packages, users, network state). " +
						"Anything else, or if you are not sure: write."},
				"reason": map[string]any{"type": "string", "description": "One short sentence for the user: why this command."},
				"explain": map[string]any{"type": "array",
					"description": "Required when the command chains anything (| && || ; $( ) backticks, redirections): " +
						"split it into its pieces in order and say in a few plain words what each one does, so the user " +
						"can check it before it runs. Cover the whole command.",
					"items": map[string]any{"type": "object", "properties": map[string]any{
						"part": map[string]any{"type": "string", "description": "A piece of the command, verbatim."},
						"does": map[string]any{"type": "string", "description": "What it does."},
					}, "required": []string{"part", "does"}}},
				"timeout_seconds": map[string]any{"type": "integer", "description": "How long to wait (default 30, max 300). " +
					"A command still running then keeps running in its pane: use a short timeout to start load or a tail and look at another pane meanwhile."},
				"pane": paneProp,
			},
			"required": []string{"command", "effect", "reason"},
		},
		"annotations": map[string]any{"destructiveHint": true, "openWorldHint": true},
	},
	{
		"name": "send_keys",
		"description": "Press keys in the program that owns the user's terminal right now (htop, less, a pager, a y/n " +
			"or password-less prompt of a running command) and get the screen back. Text is typed as is; special keys " +
			"go in angle brackets: <enter> <esc> <tab> <space> <bs> <up> <down> <left> <right> <pgup> <pgdn> <home> " +
			"<end> <f1>..<f12> <ctrl-c>. Not for running commands at a shell prompt: use run.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"keys": map[string]any{"type": "string", "description": "e.g. \"P\", \"q\", \"<ctrl-c>\", \"y<enter>\"."},
				"effect": map[string]any{"type": "string", "enum": []string{"read", "write"},
					"description": "read: only changes the view (sorting, scrolling, quitting a viewer). " +
						"write: anything the program may act on (kill a process, answer yes, save). Not sure: write."},
				"reason": map[string]any{"type": "string", "description": "One short sentence for the user: why these keys."},
				"pane":   paneProp,
			},
			"required": []string{"keys", "effect", "reason"},
		},
		"annotations": map[string]any{"destructiveHint": true},
	},
	{
		"name":        "screen",
		"description": "Snapshot of what a pane shows right now (a running program, htop, a pager).",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"pane": paneProp}},
		"annotations": map[string]any{"readOnlyHint": true},
	},
	{
		"name": "split",
		"description": "Open another pane on the same server as pane, over the same SSH connection (like a tmux split), " +
			"shown to the user next to the others. Use it to run something while a pane is busy, e.g. generate load while " +
			"htop is open in the user's pane. Returns the new pane number.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"pane":   paneProp,
			"reason": map[string]any{"type": "string", "description": "One short sentence for the user: what the pane is for."}}},
	},
	{
		"name": "connect",
		"description": "Open a pane with an SSH session to one of the organization's servers (see hosts), shown to the " +
			"user. Logs in with the server's configured key or vault password. Returns the new pane number once its " +
			"shell is ready, or its screen if it is not. When the server can't be reached (a VPN down, a key refused), " +
			"the user gets a \"can't connect\" box and this waits while they fix it and retry; if it then says it failed, " +
			"don't call connect again on your own: ask the user, and connect once they say it's fixed.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"host":   map[string]any{"type": "string", "description": "Server name as hosts lists it: name, or group/name when names repeat."},
			"reason": map[string]any{"type": "string", "description": "One short sentence for the user: why this server."}},
			"required": []string{"host"}},
	},
	{
		"name":        "hosts",
		"description": "List the organization's servers as group/name (address). query filters them (fzf syntax: fuzzy, 'exact, ^prefix, suffix$, !not).",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}},
		"annotations": map[string]any{"readOnlyHint": true},
	},
	{
		"name": "ask_peer",
		"description": "Ask the peer Claude that sent you context or a task (it works on the code of the org's services) " +
			"about that code: what a service does, its database schema and queries, configs, how to deploy it. Waits for the answer.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"question": map[string]any{"type": "string"},
			"peer":     map[string]any{"type": "string", "description": "Peer name (default: the last one that contacted you)."}},
			"required": []string{"question"}},
	},
	{
		"name":        "task_done",
		"description": "Report the result of a <task> a peer gave you, once it is finished, failed or the user stopped it.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"task":    map[string]any{"type": "integer"},
			"peer":    map[string]any{"type": "string", "description": "The task's from; needed only when desk restarted since it came."},
			"status":  map[string]any{"type": "string", "enum": []string{"done", "failed", "rejected by user"}},
			"summary": map[string]any{"type": "string", "description": "What you did, what you found, relevant output, anything left to do."}},
			"required": []string{"task", "status", "summary"}},
	},
	{
		"name": "message_card",
		"description": "Write to the Claude of a card on the user's desk board (a key like DESK-12, as in a task's from): " +
			"a finding it should act on in the code, or a question that can wait for its reply (ask_peer waits).",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"card": map[string]any{"type": "string"},
			"text": map[string]any{"type": "string"}},
			"required": []string{"card", "text"}},
	},
	{
		"name": "create_card",
		"description": "Put follow-up work on the user's desk board, in To do, for the user to start: a finding that " +
			"needs a fix or a change in the code (from a check, an alert, a task). Not for what you finish yourself.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"title": map[string]any{"type": "string", "description": "Short, what to do."},
			"notes": map[string]any{"type": "string", "description": "Markdown: what you found, the evidence (hosts, output), what to do."}},
			"required": []string{"title", "notes"}},
	},
	{
		"name": "save_runbook",
		"description": "Save a runbook the user can start again from the palette: markdown with a '# Title', a line on " +
			"when to use it, then numbered steps, each with its command in a sh fence. Write {{name}} (or {{name|default}}) " +
			"for what changes between runs (host, service, version). Use it when the user asks to save a procedure.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"name":     map[string]any{"type": "string", "description": "Short file name, e.g. 'restart php-fpm'."},
			"markdown": map[string]any{"type": "string"}},
			"required": []string{"name", "markdown"}},
	},
	{
		"name": "changes",
		"description": "What changed recently on a pane's server: packages installed/upgraded/removed, files changed " +
			"under /etc, services started/stopped/failed, logins, reboots, containers. Read-only, runs in the background " +
			"over the same connection. Start here when something broke and nobody knows why.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"pane":  paneProp,
			"hours": map[string]any{"type": "integer", "description": "How far back (default 24, max 720)."}}},
		"annotations": map[string]any{"readOnlyHint": true},
	},
	{
		"name": "note",
		"description": "Save a durable fact about a pane's server for future sessions: where its configs and logs live, " +
			"quirks, how its services are restarted, what a cron job does. One fact per call, one line. Not transient state " +
			"(load, a current error). Notes come back in <notes> when the server shows up again.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"pane": paneProp,
			"text": map[string]any{"type": "string"}},
			"required": []string{"text"}},
	},
	{
		"name":        "close_pane",
		"description": "Close a pane you opened with split (whatever runs in it is hung up). Close your panes when done.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"pane": paneProp}, "required": []string{"pane"}},
	},
}

type toolCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"arguments"`
	Card string          `json:"card,omitempty"` // a card session's call (cardTools)
}

type toolResult struct {
	Text    string `json:"text"`
	IsError bool   `json:"isError"`
	Image   string `json:"image,omitempty"` // a PNG, base64
}

// orgTools are offered only when the org's desk.toml configures that group.
var orgTools = map[string][]map[string]any{
	"secrets": {
		{
			"name": "secrets_search",
			"description": "Search the organization's secret vaults (e.g. Passbolt) by name, username or URI. " +
				"Returns ids and metadata only, never passwords.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Substring; empty lists everything."}}},
			"annotations": map[string]any{"readOnlyHint": true},
		},
		{
			"name": "secret_get",
			"description": "Read one secret (password and description) from a vault. The user approves every read. " +
				"Only when you really need the value; never repeat it back unless asked.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"vault":  map[string]any{"type": "string", "description": "Vault name (default: the first)."},
				"id":     map[string]any{"type": "string", "description": "Item id from secrets_search."},
				"reason": map[string]any{"type": "string", "description": "One short sentence for the user: why you need it."},
			}, "required": []string{"id", "reason"}},
		},
	},
	"nagios": {{
		"name": "nagios",
		"description": "Nagios status. Without host: every host and service that is not OK, worst first " +
			"(with [acked], [downtime], [soft n/m] flags). With host (substring): all its services; service narrows them.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"source":  map[string]any{"type": "string", "description": "Nagios name (default: the first)."},
			"host":    map[string]any{"type": "string"},
			"service": map[string]any{"type": "string"},
		}},
		"annotations": map[string]any{"readOnlyHint": true},
	}},
	"metrics": {{
		"name": "metrics",
		"description": "PromQL against the org's Prometheus/VictoriaMetrics. Without start: instant query at end. " +
			"With start: range query, summarized per series (min/avg/max/last and sampled points). " +
			"Without query: list metric names containing find.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"source": map[string]any{"type": "string", "description": "Metrics source name (default: the first)."},
			"query":  map[string]any{"type": "string", "description": "PromQL."},
			"start":  map[string]any{"type": "string", "description": "-1h, -30m, RFC 3339 or unix seconds."},
			"end":    map[string]any{"type": "string", "description": "Default now."},
			"step":   map[string]any{"type": "string", "description": "e.g. 60s (default: ~120 points)."},
			"find":   map[string]any{"type": "string", "description": "Metric name substring, used when query is empty."},
		}},
		"annotations": map[string]any{"readOnlyHint": true},
	}},
}

// mcpMain is `desk mcp <socket> [groups]`: stdio MCP server for one tab.
func mcpMain(sock, groups string) { mcpServe(sock, groups, os.Stdin, os.Stdout) }

func mcpServe(sock, groups string, r io.Reader, w io.Writer) {
	tools := append(slices.Clone(mcpTools), drawingTools...)
	for _, g := range strings.Split(groups, ",") {
		tools = append(tools, orgTools[g]...)
	}
	card, isCard := strings.CutPrefix(groups, "card=")
	if isCard {
		tools = append(slices.Clone(cardTools), drawingTools...)
	}
	if id, ok := strings.CutPrefix(groups, "ws="); ok { // a card's workspace Claude: reads, and suggests to the card's
		tools, card = workspaceTools, "ws:"+id
	}
	if path, ok := strings.CutPrefix(groups, "draw="); ok { // a drawing's own Claude
		tools, card = drawingTools, "draw:"+path
	}
	in := bufio.NewScanner(r)
	in.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	out := json.NewEncoder(w)
	for in.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil || req.ID == nil {
			continue // notifications need no answer
		}
		var result any
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			json.Unmarshal(req.Params, &p)
			result = map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "desk", "version": "0.1"},
			}
		case "tools/list":
			result = map[string]any{"tools": tools}
		case "tools/call":
			var c toolCall
			json.Unmarshal(req.Params, &c)
			c.Card = card
			r := forward(sock, c)
			result = map[string]any{"content": r.content(), "isError": r.IsError}
		case "ping":
			result = map[string]any{}
		default:
			out.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32601, "message": "method not found: " + req.Method}})
			continue
		}
		out.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
}

func forward(sock string, c toolCall) toolResult {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return toolResult{Text: "desk is not reachable: " + err.Error(), IsError: true}
	}
	defer conn.Close()
	json.NewEncoder(conn).Encode(c)
	var r toolResult
	if err := json.NewDecoder(conn).Decode(&r); err != nil {
		return toolResult{Text: "desk closed the call: " + err.Error(), IsError: true}
	}
	return r
}

// cardTools are what a card's Claude reads through desk (`desk mcp <sock>
// card=<id>`): its organization's integrations, answered by the board
// (ServeCards), so vaults unlock as for the rest of desk.
var cardTools = []map[string]any{
	{
		"name":        "alerts",
		"description": "The card organization's monitoring alerts now (Nagios): host or service, state, since when, the check's output.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		"annotations": map[string]any{"readOnlyHint": true},
	},
	{
		"name":        "tickets",
		"description": "The user's open tickets assigned in the card organization's trackers (Jira, GitHub, GitLab): key, title, status, URL.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		"annotations": map[string]any{"readOnlyHint": true},
	},
	{
		"name": "link",
		"description": "A ticket, issue, pull or merge request or commit (URL, or a Jira key): its state now (review, CI) and, " +
			"for Jira and GitHub, its title and description. Without url, every link on the card.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"url": map[string]any{"type": "string"}}},
		"annotations": map[string]any{"readOnlyHint": true},
	},
	{
		"name": "ci",
		"description": "The CI of a pull or merge request or commit (GitHub Actions workflow runs, GitLab pipelines) with every job's " +
			"status and id. Without url, those linked on the card.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"url": map[string]any{"type": "string"}}},
		"annotations": map[string]any{"readOnlyHint": true},
	},
	{
		"name":        "ci_log",
		"description": "The end of a CI job's log (the job id from ci), to see why it failed.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"url":   map[string]any{"type": "string", "description": "The pull or merge request or commit the job ran for."},
			"job":   map[string]any{"type": "integer"},
			"bytes": map[string]any{"type": "integer", "description": "How much of the end (default 20000)."}},
			"required": []string{"url", "job"}},
		"annotations": map[string]any{"readOnlyHint": true},
	},
	{
		"name": "add_project",
		"description": "Add a folder to the card's workspace (the user's IDE view of the card): call it when you start working in a project root " +
			"(a repository or a folder with its own build) that isn't the card's own folder. desk uses the repository root above path.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "An absolute folder."}}, "required": []string{"path"}},
	},
	{
		"name": "ask_workspace",
		"description": "Ask the card's workspace Claude (it reads the workspace's projects with the user, never writes) a question. " +
			"Returns at once; its answer comes into this conversation as a message from the workspace Claude.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"question": map[string]any{"type": "string"}}, "required": []string{"question"}},
	},
}

// workspaceTools are the card's workspace Claude's (`desk mcp <sock>
// ws=<id>`): the card tools that only read, and suggest_to_card.
var workspaceTools = append(append(slices.Clone(cardTools[:5]), readDrawingTools()...), map[string]any{
	"name": "suggest_to_card",
	"description": "Send a suggestion (a change as a diff or snippet, with why) to the card's main Claude, which does the work. " +
		"Only when the user asks you to pass it on.",
	"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
		"text": map[string]any{"type": "string"}}, "required": []string{"text"}},
})

// ServeCards answers the card sessions' tool calls with handle (card is
// the card's id; ok false: an error), on a socket of desk's; the socket's
// path. Calls run on their own goroutines.
func ServeCards(handle func(card, name string, args json.RawMessage) (text string, ok bool)) (string, error) {
	sock := filepath.Join(runtimeDir(), fmt.Sprintf("cards-%d.sock", os.Getpid()))
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return "", err
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var c toolCall
				if json.NewDecoder(conn).Decode(&c) == nil {
					json.NewEncoder(conn).Encode(resultOf(handle(c.Card, c.Name, c.Args)))
				}
			}()
		}
	}()
	return sock, nil
}

// CardMCP is claude's --mcp-config that gives card id's session the card
// tools through sock (ServeCards'), and the tools to allow without asking:
// they only read. "" without a socket.
func CardMCP(sock, id string) (string, []string) {
	return toolsMCP(sock, "card="+id, append(slices.Clone(cardTools), drawingTools...))
}

// WorkspaceMCP is CardMCP for card id's workspace Claude.
func WorkspaceMCP(sock, id string) (string, []string) {
	return toolsMCP(sock, "ws="+id, workspaceTools)
}

func toolsMCP(sock, group string, tools []map[string]any) (string, []string) {
	exe, err := os.Executable()
	if err != nil || sock == "" {
		return "", nil
	}
	cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"desk": map[string]any{
		"type": "stdio", "command": exe, "args": []string{"mcp", sock, group}}}})
	var allow []string
	for _, t := range tools {
		allow = append(allow, "mcp__desk__"+t["name"].(string))
	}
	return string(cfg), allow
}

// runtimeDir is desk's folder for sockets: $XDG_RUNTIME_DIR/desk (user only).
// macOS has none, and its $TMPDIR may not be the one a session running
// `desk peer` sees, so there it is ~/.cache/desk/run (short enough for a
// socket's 104 bytes); Windows too (AF_UNIX since 10 1803, 108 bytes; the
// profile folder is the user's alone by its ACL, where 0o700 means nothing).
func runtimeDir() string {
	d := os.Getenv("XDG_RUNTIME_DIR")
	switch {
	case d != "":
		d = filepath.Join(d, "desk")
	case macOS || runtime.GOOS == "windows":
		d, _ = deskorgs.XDGDir("XDG_CACHE_HOME", ".cache")
		d = filepath.Join(d, "desk", "run")
	default:
		d = filepath.Join(os.TempDir(), "desk")
	}
	os.MkdirAll(d, 0o700)
	return d
}

// macOS is runtime.GOOS == "darwin", a variable so tests reach its ways.
var macOS = runtime.GOOS == "darwin"

// serveTools listens on a per-tab socket and runs tool calls on its terminal.
func serveTools(t *Tab) (string, net.Listener, error) {
	sock := filepath.Join(runtimeDir(), fmt.Sprintf("infra-%d-%p.sock", os.Getpid(), t))
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return "", nil, err
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var c toolCall
				if json.NewDecoder(conn).Decode(&c) == nil {
					r := t.callTool(c)
					if c.Name != "secret_get" { // the one the user approved handing over
						r.Text = redact(r.Text)
					}
					json.NewEncoder(conn).Encode(r)
				}
			}()
		}
	}()
	return sock, ln, nil
}

func (t *Tab) callTool(c toolCall) toolResult {
	var a struct {
		Command string `json:"command"`
		Keys    string `json:"keys"`
		Effect  string `json:"effect"`
		Reason  string `json:"reason"`
		Timeout int    `json:"timeout_seconds"`
		Explain []step `json:"explain"`
		orgArgs
		Pane     int    `json:"pane"`
		Peer     string `json:"peer"`
		Question string `json:"question"`
		Task     int    `json:"task"`
		Status   string `json:"status"`
		Summary  string `json:"summary"`
		Card     string `json:"card"`
		Title    string `json:"title"`
		Notes    string `json:"notes"`
		Text     string `json:"text"`
		Name     string `json:"name"`
		Markdown string `json:"markdown"`
		Hours    int    `json:"hours"`
	}
	json.Unmarshal(c.Args, &a)
	if orgTool[c.Name] {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute) // includes typing a passphrase
		defer cancel()
		out, err := t.orgCall(ctx, c.Name, a.orgArgs, a.Reason)
		if err != nil {
			return toolResult{Text: err.Error(), IsError: true}
		}
		return toolResult{Text: out}
	}
	if slices.ContainsFunc(drawingTools, func(d map[string]any) bool { return d["name"] == c.Name }) {
		return t.drawTool(c)
	}
	switch c.Name {
	case "hosts":
		var out []string
		for _, n := range t.hosts {
			line := n.group + "/" + n.label
			if n.addr != "" {
				line += " (" + n.addr + ")"
			}
			if n.desc != "" {
				line += " — " + n.desc
			}
			if n.tags != "" {
				line += " [" + n.tags + "]"
			}
			if _, ok := fzfScore(a.Query, line); ok {
				out = append(out, line)
			}
		}
		n := len(out)
		if n > 300 {
			out = append(out[:300], fmt.Sprintf("… %d more, narrow the query", n-300))
		}
		return toolResult{Text: fmt.Sprintf("%d servers in %s\n%s", n, t.org, strings.Join(out, "\n"))}
	case "ask_peer":
		return t.askPeer(a.Peer, a.Question)
	case "task_done":
		return t.taskDone(a.Task, a.Peer, a.Status, a.Summary)
	case "message_card":
		return t.tellCard(a.Card, a.Text)
	case "create_card":
		return t.newCard(a.Title, a.Notes)
	case "save_runbook":
		p, err := saveRunbook(t, a.Name, a.Markdown)
		if err != nil {
			return toolResult{Text: err.Error(), IsError: true}
		}
		return toolResult{Text: "saved " + p + "; the user starts it from the palette (" + modText("K") + ")"}
	case "connect":
		n, err := t.findHost(a.Host)
		if err != nil {
			return toolResult{Text: err.Error(), IsError: true}
		}
		return t.openPane(n)
	}
	p, err := t.pane(a.Pane)
	if err != nil {
		return toolResult{Text: err.Error(), IsError: true}
	}
	switch c.Name {
	case "split":
		return t.openPane(p.node())
	case "screen":
		s := p.term.Screen()
		return toolResult{Text: fmt.Sprintf("pane %d alt=%t running=%q\n%s", p.id, s.Alt, s.Running, s.Text)}
	case "changes":
		if a.Hours == 0 {
			a.Hours = 24
		}
		return t.changes(p, a.Hours)
	case "note":
		if err := addNote(t.org, p.name, a.Text); err != nil {
			return toolResult{Text: err.Error(), IsError: true}
		}
		t.mu.Lock()
		if t.notesSent == nil {
			t.notesSent = map[string]bool{}
		}
		t.notesSent[p.name] = true // it knows its own note
		t.mu.Unlock()
		return toolResult{Text: "noted for " + p.name}
	case "close_pane":
		if !p.claude {
			return toolResult{Text: fmt.Sprintf("pane %d is the user's; you can only close panes you opened", p.id), IsError: true}
		}
		t.closePane(p)
		notify() // the redraw drops it
		return toolResult{Text: fmt.Sprintf("closed pane %d", p.id)}
	case "run":
		if strings.TrimSpace(a.Command) == "" {
			return toolResult{Text: "empty command", IsError: true}
		}
		if a.Timeout <= 0 {
			a.Timeout = 30
		}
		if compound.MatchString(a.Command) && len(a.Explain) == 0 {
			return toolResult{Text: "not run: this command chains several pieces; call run again with explain, one entry per piece", IsError: true}
		}
		cmd, note, denied := t.gate(a.Command, a.Reason, needsApproval(a.Effect, a.Command), false, p.id, a.Explain...)
		if denied != nil {
			return *denied
		}
		res := p.term.Run(cmd, time.Duration(min(a.Timeout, 300))*time.Second)
		res.Text = fmt.Sprintf("pane %d · ", p.id) + note + res.Text
		return res
	case "send_keys":
		if a.Keys == "" {
			return toolResult{Text: "no keys", IsError: true}
		}
		if p.term.ShellReads() { // refuse before asking the user to approve it
			return toolResult{IsError: true, Text: fmt.Sprintf("pane %d is at an idle shell prompt: send_keys only talks to running programs. "+
				"Use run instead; for htop or tail -f give it a short timeout_seconds so it keeps running.", p.id)}
		}
		why := ""
		if a.Effect != "read" {
			why = "Claude marked these keys as a write"
		}
		keys, note, denied := t.gate(a.Keys, a.Reason, why, true, p.id)
		if denied != nil {
			return *denied
		}
		res := p.term.SendKeys(keys)
		res.Text = fmt.Sprintf("pane %d · ", p.id) + note + res.Text
		return res
	}
	return toolResult{Text: "unknown tool " + c.Name, IsError: true}
}

// findHost picks one of the org's servers by name, group/name or address.
func (t *Tab) findHost(q string) (node, error) {
	var hit []node
	for _, n := range t.hosts {
		if strings.EqualFold(q, n.label) || strings.EqualFold(q, n.group+"/"+n.label) || (n.addr != "" && q == n.addr) {
			hit = append(hit, n)
		}
	}
	switch len(hit) {
	case 1:
		return hit[0], nil
	case 0:
		return node{}, fmt.Errorf("no server %q in %s; search with hosts", q, t.org)
	}
	var names []string
	for _, n := range hit {
		names = append(names, n.group+"/"+n.label)
	}
	return node{}, fmt.Errorf("%q is %s: pass group/name", q, strings.Join(names, " or "))
}

// openPane asks the UI for a pane on n and waits for its shell prompt.
// connectHold is how long connect waits on a server it can't reach for the
// user to fix the way there and retry (a variable so tests can shorten it).
var connectHold = 30 * time.Minute

// unreachable is an open pane of t on n's server that never connected.
func (t *Tab) unreachable(n node) *Pane {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, p := range t.panes {
		if !p.closed && p.term.Host == n.host && p.dir == n.dir && !p.term.Connected() {
			return p
		}
	}
	return nil
}

func (t *Tab) openPane(n node) toolResult {
	p := t.unreachable(n) // already stuck in its "can't connect" box: wait on that one, not a second
	if p == nil {
		done := make(chan *Pane, 1)
		dispatch(paneMsg{t, n, done})
		if p = <-done; p == nil {
			return toolResult{Text: "could not open a pane on " + n.label, IsError: true}
		}
	}
	term := func() (*Term, bool) { t.mu.Lock(); defer t.mu.Unlock(); return p.term, p.closed } // Retry swaps in a new one
	ended := func() bool { _, e := p.term.State(); return e }
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end) && !p.term.AtPrompt() && !ended(); time.Sleep(100 * time.Millisecond) {
	}
	if tm, _ := term(); !tm.AtPrompt() && !tm.Connected() {
		// The user sees the box (a VPN to bring up, a key to fix) and retries
		// from it or closes the pane: wait for them, as an approval does.
		for end := time.Now().Add(connectHold); ; time.Sleep(500 * time.Millisecond) {
			tm, closed := term()
			switch {
			case closed:
				return toolResult{IsError: true, Text: fmt.Sprintf("couldn't connect to %s and the user closed pane %d instead of retrying. Don't connect to it again; "+
					"ask the user how to go on.", n.label, p.id)}
			case tm.AtPrompt():
				return toolResult{Text: t.notesFor(n.label) + fmt.Sprintf("opened pane %d on %s, at a shell prompt, after the user fixed the "+
					"connection. Pass pane=%d; close_pane it when done.", p.id, n.label, p.id)}
			case time.Now().After(end):
				return toolResult{IsError: true, Text: fmt.Sprintf("still can't connect to %s after %s; pane %d keeps its \"can't connect\" box. Don't open "+
					"another: ask the user, and connect again once they say it's fixed.", n.label, connectHold, p.id)}
			}
		}
	}
	if !p.term.AtPrompt() {
		return toolResult{IsError: true, Text: fmt.Sprintf("opened pane %d on %s but it has no shell prompt (still connecting, a login prompt, or the "+
			"connection failed; the user sees why). Screen:\n%s", p.id, n.label, p.term.Screen().Text)}
	}
	return toolResult{Text: t.notesFor(n.label) + fmt.Sprintf("opened pane %d on %s, at a shell prompt. Pass pane=%d; close_pane it when done.", p.id, n.label, p.id)}
}

var orgTool = map[string]bool{"secrets_search": true, "secret_get": true, "nagios": true, "metrics": true}

type orgArgs struct {
	Source, Host, Service, Query, Start, End, Step, Find, Vault, ID string
}

func (t *Tab) orgCall(ctx context.Context, name string, a orgArgs, reason string) (string, error) {
	o := t.orgCfg
	switch name {
	case "nagios":
		return o.nagios(ctx, a.Source, a.Host, a.Service)
	case "metrics":
		return o.metrics(ctx, a.Source, a.Query, a.Start, a.End, a.Step, a.Find)
	case "secrets_search":
		var out []string
		for _, vc := range o.Vaults {
			v, _, err := o.vault(vc.Name)
			if err != nil {
				return "", err
			}
			items, err := v.search(ctx, a.Query)
			if err != nil {
				return "", fmt.Errorf("%s: %w", vc.Name, err)
			}
			for _, it := range items {
				out = append(out, fmt.Sprintf("%s · id=%s · %s · user=%s · %s", vc.Name, it.ID, it.Name, it.Username, it.URI))
			}
		}
		n := len(out)
		if n > 200 {
			out = append(out[:200], fmt.Sprintf("… %d more, narrow the query", n-200))
		}
		return fmt.Sprintf("%d items\n%s", n, strings.Join(out, "\n")), nil
	case "secret_get":
		v, vc, err := o.vault(a.Vault)
		if err != nil {
			return "", err
		}
		label := a.ID
		if items, err := v.search(ctx, ""); err == nil {
			for _, it := range items {
				if it.ID == a.ID {
					label = fmt.Sprintf("%s (user %s)", it.Name, it.Username)
				}
			}
		}
		// ponytail: always asks, no "always": secrets reach Claude's transcript
		r := t.approve(&approval{cmd: vc.Name + " · " + label, reason: reason, why: "Claude will see this password", secret: true})
		if !r.ok {
			return "", fmt.Errorf("the user denied reading this secret; don't retry, ask them")
		}
		it, err := v.get(ctx, a.ID)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("name: %s\nusername: %s\nuri: %s\npassword: %s\ndescription: %s", it.Name, it.Username, it.URI, it.Password, it.Description), nil
	}
	return "", fmt.Errorf("unknown tool %s", name)
}

// gate asks the user when why is set, unless they already said "always" to
// this exact command in this org, or desk.toml's [approvals] allows it; its
// deny patterns ask even then. It returns what to run (maybe edited).
func (t *Tab) gate(cmd, reason, why string, keys bool, pane int, explain ...step) (string, string, *toolResult) {
	if p := t.orgCfg.denies(cmd); p != "" && !keys {
		why = "[approvals] always asks for " + p
	} else if why == "" || always.has(t.org, allowKey(cmd, keys)) || !keys && t.orgCfg.allows(cmd) {
		return cmd, "", nil
	}
	r := t.approve(&approval{cmd: cmd, reason: reason, why: why, keys: keys, pane: pane, explain: explain, task: taskFrom(t)})
	if !r.ok {
		return "", "", &toolResult{Text: "the user denied this" + r.note + ". Do not retry it; ask them what they want instead.", IsError: true}
	}
	if r.cmd != cmd {
		return r.cmd, fmt.Sprintf("(the user edited it to: %s)\n", r.cmd), nil
	}
	return cmd, "", nil
}

// allows says desk.toml's [approvals] allow list lets cmd run unasked. Never
// for compound commands: "journalctl *" must not let "journalctl; rm -rf /" in.
func (o *orgConfig) allows(cmd string) bool {
	if o == nil || compound.MatchString(cmd) {
		return false
	}
	for _, p := range o.Approvals.Allow {
		if globMatch(p, cmd) {
			return true
		}
	}
	return false
}

// denies gives the [approvals] deny pattern that matches cmd, if any.
func (o *orgConfig) denies(cmd string) string {
	if o == nil {
		return ""
	}
	for _, p := range o.Approvals.Deny {
		if globMatch(p, cmd) {
			return p
		}
	}
	return ""
}

// globMatch matches a whole command line against a pattern where * is any
// text (slashes and spaces included).
func globMatch(pattern, s string) bool {
	parts := strings.Split(strings.TrimSpace(pattern), "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	ok, _ := regexp.MatchString("^"+strings.Join(parts, ".*")+"$", strings.TrimSpace(s))
	return ok
}

// always holds the "always" answers, per org, kept in
// $XDG_STATE_HOME/desk/infra/always/<org>.json (a list of allowKeys; edit it to revoke).
var always = &alwaysStore{orgs: map[string]map[string]bool{}}

type alwaysStore struct {
	sync.Mutex
	orgs map[string]map[string]bool
}

func alwaysPath(org string) string {
	return filepath.Join(stateDir(), "always", orgDirName(org)+".json")
}

// load reads org's answers once; called with the lock held.
func (s *alwaysStore) load(org string) map[string]bool {
	if m, ok := s.orgs[org]; ok {
		return m
	}
	m := map[string]bool{}
	var keys []string
	if b, err := os.ReadFile(alwaysPath(org)); err == nil && json.Unmarshal(b, &keys) == nil {
		for _, k := range keys {
			m[k] = true
		}
	}
	s.orgs[org] = m
	return m
}

func (s *alwaysStore) has(org, key string) bool {
	s.Lock()
	defer s.Unlock()
	return s.load(org)[key]
}

func (s *alwaysStore) add(org, key string) {
	s.Lock()
	defer s.Unlock()
	m := s.load(org)
	m[key] = true
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b, _ := json.MarshalIndent(keys, "", "  ")
	os.MkdirAll(filepath.Dir(alwaysPath(org)), 0o700)
	os.WriteFile(alwaysPath(org), b, 0o600)
}

func allowKey(cmd string, keys bool) string {
	if keys {
		return "keys:" + cmd
	}
	return "run:" + cmd
}

// step is one piece of a compound command and what it does.
type step struct {
	Part string `json:"part"`
	Does string `json:"does"`
}

// compound spots commands built from several pieces, which Claude must
// explain piece by piece. ponytail: also fires on | ; > inside quotes; erring
// towards an explanation is fine.
var compound = regexp.MustCompile("[|;&<>`]|\\$\\(")

// writeCmd spots commands that change something, as a backstop for when
// Claude labels a write as "read". ponytail: a word list, not a shell parser;
// it errs towards asking.
var writeCmd = regexp.MustCompile(`(?:^|[;&|(]\s*|\bsudo\s+(?:-\S+\s+)*)(rm|rmdir|mv|cp|dd|mkfs\S*|chmod|chown|chgrp|ln|touch|mkdir|truncate|tee|shred|kill|pkill|killall|reboot|shutdown|poweroff|halt|useradd|userdel|usermod|passwd|crontab|mount|umount|iptables|ufw|nft|install)\b` +
	`|\bsed\s+(?:-\S+\s+)*-i|\bsystemctl\s+(?:--\S+\s+)*(start|stop|restart|reload|enable|disable|mask|unmask|daemon-reload|kill)\b` +
	`|\bservice\s+\S+\s+(start|stop|restart|reload)\b|\b(apt|apt-get|yum|dnf|zypper|apk|pacman|snap|pip3?|npm|gem)\s+(?:-\S+\s+)*(install|remove|purge|upgrade|dist-upgrade|autoremove|update|add|del|uninstall|-S\S*|-R\S*)\b` +
	`|\bdocker\s+(compose\s+)?(rm|rmi|stop|kill|restart|run|exec|up|down|prune|pull|build)\b|\bkubectl\s+(apply|delete|edit|scale|rollout|patch|create|replace|drain|cordon|exec)\b` +
	`|\bgit\s+(push|commit|reset|checkout|clean|merge|rebase|pull)\b`)

// redirect is output redirection to anything but /dev/null or another fd.
var redirect = regexp.MustCompile(`(?:^|[^0-9&>])>>?\s*([^&\s>][^\s;|&]*)`)

// needsApproval says why a command must be confirmed ("" = run it).
func needsApproval(effect, cmd string) string {
	if effect != "read" {
		return "Claude marked it as " + map[bool]string{true: "a write", false: "unknown"}[effect == "write"]
	}
	if m := writeCmd.FindStringSubmatch(cmd); m != nil {
		return "looks like it changes something: " + strings.TrimSpace(m[0])
	}
	for _, m := range redirect.FindAllStringSubmatch(cmd, -1) {
		if m[1] != "/dev/null" {
			return "writes to " + m[1]
		}
	}
	return ""
}

// approval is a command waiting for the user in the TUI.
type approval struct {
	cmd, reason, why string
	keys             bool // send_keys, not a command line
	secret           bool // secret_get: shown differently, never "always"
	explain          []step
	pane             int
	task             string // the peer task it is part of
	blocked          bool   // stopped by Claude Code's classifier; desk runs it after the fact
	edit             string // being edited (e)
	editing          bool
	done             chan approvalResult
}

type approvalResult struct {
	ok   bool
	cmd  string
	note string
}

type approvalMsg struct {
	tab *Tab
	a   *approval
}

// approve shows the prompt and blocks the tool call until the user answers.
func (t *Tab) approve(a *approval) approvalResult {
	a.done = make(chan approvalResult, 1)
	dispatch(approvalMsg{t, a})
	return <-a.done
}
