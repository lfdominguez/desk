package claude

import (
	"bufio"
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Draft is a request worked into a card by Optimize.
type Draft struct {
	Title    string   `json:"title"`
	Prompt   string   `json:"prompt"` // the request, argued out: the card's notes and the session's first words
	Tools    []Tool   `json:"tools"`  // the skills, agents, MCP tools or commands the prompt has the session use
	Labels   []string `json:"labels"`
	Priority string   `json:"priority"` // none, low, medium, high
	Folder   string   `json:"folder"`   // inside the org, "" its root
	Links    []string `json:"links"`
	Due      string   `json:"due"` // YYYY-MM-DD when the request says when
}

// Tool is a capability of the org's Claude the draft recommends.
type Tool struct {
	Kind string `json:"kind"` // skill, agent, mcp, command
	Name string `json:"name"` // as Claude lists it: team-tools:work-ticket
	Use  string `json:"use"`  // how and why the session uses it
}

// Ask is what Optimize works from.
type Ask struct {
	Home, Account string // whose Claude: its skills, plugins and MCP servers are what the session will have
	Org, Dir      string // the organization and the folder the session starts in, where Claude reads (CLAUDE.md, code)
	Root          string // the organization's folder, added with --add-dir when Dir is inside it (its skills)
	Folder        string // the folder inside the org the user picked, "" none: then Folders are to pick from
	Request       string
	Folders       []string // the org's folders, to pick from
	Labels        []string // labels already on the board, to reuse
	Linked        []string // what the tickets and PRs the request names say, read by desk
	Today         string
	Lang          string   // what the title and prompt are written in, "" the request's
	Env           []string // more environment: ProviderEnv
	About         string   // who the user is (AboutUser's), "" not said
	UserMCP       bool     // the session will have the user's MCP servers: it sees them too (else --strict-mcp-config)
}

const draftSchema = `{"type":"object","additionalProperties":false,
"required":["title","prompt","tools","labels","priority","folder","links","due"],
"properties":{
"title":{"type":"string","description":"imperative, at most 70 characters"},
"prompt":{"type":"string"},
"tools":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["kind","name","use"],
 "properties":{"kind":{"type":"string","enum":["skill","agent","mcp","command"]},"name":{"type":"string"},"use":{"type":"string"}}}},
"labels":{"type":"array","items":{"type":"string"}},
"priority":{"type":"string","enum":["none","low","medium","high"]},
"folder":{"type":"string"},
"links":{"type":"array","items":{"type":"string"}},
"due":{"type":"string"}}}`

// optimizer is the brief Optimize gives Haiku, on top of Claude Code's own
// system prompt (which lists the account's skills, agents and MCP tools).
//
//go:embed optimizer.md
var optimizer string

// Progress is what Optimize is doing while it runs: its step ("Thinking",
// "Reading main.go", "Writing") and the title and prompt written so far.
type Progress struct {
	Step  string
	Draft Draft
}

// Optimize asks the latest Haiku, thinking hard, to argue a request out
// into a card, with the org's own Claude context; it may take a minute.
// It sees every skill, agent and MCP tool the session will, but may only
// read files and load skills' instructions: dontAsk denies the rest.
// on, when not nil, hears its progress as it streams.
func Optimize(ctx context.Context, a Ask, on func(Progress)) (Draft, error) {
	where := "Its folders: " + strings.Join(a.Folders, ", ")
	if a.Folder != "" { // the user's pick: the work is there, not in a folder elsewhere with a similar name
		where = "Folder: " + a.Folder + " (picked by the user: the session runs in it, your current folder; the request is about what is in it)"
	}
	in := fmt.Sprintf("Organization: %s\nToday: %s (%s)\n%s\nLabels on the board: %s\n\nRequest:\n%s\n",
		a.Org, a.Today, weekday(a.Today), where, strings.Join(a.Labels, ", "), a.Request)
	if len(a.Linked) > 0 {
		in += "\nLinked items, as their trackers say now:\n\n" + strings.Join(a.Linked, "\n\n---\n\n")
	}
	if a.Lang != "" {
		in += "\nWrite the title and the prompt in " + a.Lang + ", whatever language the request is in.\n"
	}
	args := append([]string{"-p", "--model", "haiku", "--effort", "high", "--output-format", "stream-json", "--verbose",
		"--include-partial-messages", "--json-schema", draftSchema, "--permission-mode", "dontAsk", "--tools", "Read,Glob,Grep,Skill", "--allowedTools", "Read,Glob,Grep,Skill",
		"--no-session-persistence", "--append-system-prompt", optimizer + AboutUser(a.About)}, addRoot(a.Dir, a.Root)...)
	if !a.UserMCP { // it picks among the tools it sees: the card's session won't have the user's
		args = append(args, "--strict-mcp-config")
	}
	c, err := Command(a.Home, a.Account, append(args, in)...)
	if err != nil {
		return Draft{}, err
	}
	c = Jail(c, a.Home, a.Dir)
	cmd := exec.CommandContext(ctx, c.Path, c.Args[1:]...)
	cmd.Dir, cmd.Env = a.Dir, append(c.Env, a.Env...) // Command's environment: the profile's, without a parent Claude Code's markers
	var stderr Tail
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Draft{}, err
	}
	if err := cmd.Start(); err != nil {
		return Draft{}, err
	}
	var result []byte
	var js strings.Builder // the StructuredOutput call's input as it streams
	sc := bufio.NewScanner(stdout)
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		if r, ok := optLine(sc.Bytes(), &js); ok {
			result = slices.Clone(sc.Bytes())
		} else if r.Step != "" && on != nil {
			on(r)
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return Draft{}, ctx.Err()
		}
		if result != nil { // a failed run says why in its result line (API error, login, prompt too long), not on stderr
			return parseDraft(result)
		}
		return Draft{}, fmt.Errorf("%w: %s", err, cmp.Or(strings.TrimSpace(stderr.String()), "Claude said nothing"))
	}
	if result == nil {
		return Draft{}, errors.New("no draft in the reply")
	}
	return parseDraft(result)
}

// optLine reads one stream-json line of Optimize: the result (ok), or the
// progress it shows, Step "" for none. js gathers the draft's JSON.
func optLine(line []byte, js *strings.Builder) (p Progress, ok bool) {
	var e struct {
		Type  string `json:"type"`
		Event struct {
			Type         string `json:"type"`
			ContentBlock struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		} `json:"event"`
		Message struct {
			Content []struct {
				Type  string         `json:"type"`
				Name  string         `json:"name"`
				Input map[string]any `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &e) != nil {
		return p, false
	}
	switch {
	case e.Type == "result":
		return p, true
	case e.Event.Type == "content_block_start" && e.Event.ContentBlock.Type == "thinking":
		p.Step = "Thinking"
	case e.Event.Type == "content_block_start" && e.Event.ContentBlock.Name == "StructuredOutput":
		js.Reset()
		p.Step = "Writing"
	case e.Event.Delta.Type == "input_json_delta":
		js.WriteString(e.Event.Delta.PartialJSON)
		p.Step = "Writing"
		p.Draft.Title, p.Draft.Prompt = partialString(js.String(), "title"), partialString(js.String(), "prompt")
	case e.Type == "assistant":
		for _, c := range e.Message.Content { // a read it asked for, now that its input is whole
			if c.Type != "tool_use" || c.Name == "StructuredOutput" {
				continue
			}
			what, _ := c.Input["pattern"].(string)
			if f, _ := c.Input["file_path"].(string); f != "" {
				what = filepath.Base(f)
			} else if sk, _ := c.Input["skill"].(string); sk != "" {
				what = sk
			}
			p.Step = "Using " + c.Name
			if verb := map[string]string{"Read": "Reading", "Grep": "Searching for", "Glob": "Looking for", "Skill": "Loading skill"}[c.Name]; verb != "" && what != "" {
				p.Step = verb + " " + what
			}
		}
	}
	return p, false
}

// partialString is key's string value in js, a JSON object still being
// written: what of it has arrived, "" before its key.
func partialString(js, key string) string {
	i := strings.Index(js, `"`+key+`"`)
	if i < 0 {
		return ""
	}
	rest := strings.TrimLeft(js[i+len(key)+2:], " \t\n\r")
	rest, ok := strings.CutPrefix(rest, ":")
	if rest = strings.TrimLeft(rest, " \t\n\r"); !ok || !strings.HasPrefix(rest, `"`) {
		return ""
	}
	rest = rest[1:]
	end := len(rest)
	for j := 0; j < len(rest); j++ {
		if rest[j] == '\\' {
			j++
			continue
		}
		if rest[j] == '"' {
			end = j
			break
		}
	}
	seg := rest[:end]
	for range 6 { // an escape cut in half (\ or \u00e) waits for the rest
		var s string
		if json.Unmarshal([]byte(`"`+seg+`"`), &s) == nil {
			return s
		}
		seg = seg[:len(seg)-1]
	}
	return ""
}

// parseDraft reads claude's json result: the schema's object is in
// structured_output.
func parseDraft(out []byte) (Draft, error) {
	var r struct {
		IsError bool   `json:"is_error"`
		Subtype string `json:"subtype"` // error_max_turns, error_during_execution: no result text
		Result  string `json:"result"`
		Draft   *Draft `json:"structured_output"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return Draft{}, err
	}
	if r.IsError {
		return Draft{}, errors.New(cmp.Or(r.Result, r.Subtype, "Claude failed"))
	}
	if r.Draft == nil {
		return Draft{}, errors.New("no draft in the reply")
	}
	r.Draft.Title = strings.TrimSpace(r.Draft.Title)
	return *r.Draft, nil
}

// weekday names day (YYYY-MM-DD), so "Friday" resolves right.
func weekday(day string) string {
	t, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return ""
	}
	return t.Weekday().String()
}
