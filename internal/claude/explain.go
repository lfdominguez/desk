package claude

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// explainBrief is what Haiku is asked for: the command taken apart for
// someone watching over Claude's shoulder, as explainSchema.
const explainBrief = `Explain this shell command to someone watching an AI agent run it, who wants to follow every piece.
- summary: one sentence on what the whole command does and why someone would run it.
- steps: one per command the shell runs, in order: split on ; && || and newlines, and treat a wrapper that runs a quoted script (nix-shell --run, bash -c, ssh host '…', docker exec …) as one step, its code the wrapper with the script shortened to "…" (nix-shell -p pnpm --run "…"), the quoted commands its own steps after it. For each: code (that command exactly, pipes included), what it does in plain words, and parts: every flag, argument that matters, pipe stage (| tail -2), redirection (2>&1, > file), variable or substitution in it, each with code (exactly as written) and what it does. Skip parts that are obvious (a plain file name).
- warnings: at most 3, one sentence each, only real surprises in the command itself (an exit code that reports the wrong command, output thrown away, something run with privileges, a destructive flag); empty when none. No guesses about files you weren't shown.
- effects: one sentence on what it changes (files, network, system) or that it only reads.
Write about the command, never about yourself or what you did or didn't check.
Inside the text, put any command, flag, path or variable in backticks.

Command:
`

const explainSchema = `{"type":"object","additionalProperties":false,
"required":["summary","steps","warnings","effects"],
"properties":{
 "summary":{"type":"string"},
 "steps":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["code","what","parts"],"properties":{
  "code":{"type":"string"},"what":{"type":"string"},
  "parts":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["code","what"],"properties":{"code":{"type":"string"},"what":{"type":"string"}}}}}}},
 "warnings":{"type":"array","items":{"type":"string"}},
 "effects":{"type":"string"}}}`

// Explanation is Haiku's breakdown of a shell command.
type Explanation struct {
	Summary string `json:"summary"`
	Steps   []struct {
		Code  string `json:"code"`
		What  string `json:"what"`
		Parts []struct {
			Code string `json:"code"`
			What string `json:"what"`
		} `json:"parts"`
	} `json:"steps"`
	Warnings []string `json:"warnings"`
	Effects  string   `json:"effects"`
}

// Markdown is e to read: the summary, the steps numbered with their parts
// under them, the warnings as alerts, what it changes.
func (e Explanation) Markdown() string {
	var b strings.Builder
	b.WriteString(e.Summary + "\n\n")
	for i, s := range e.Steps {
		fmt.Fprintf(&b, "%d. %s — %s\n", i+1, tick(s.Code), s.What)
		for _, p := range s.Parts {
			fmt.Fprintf(&b, "   - %s — %s\n", tick(p.Code), p.What)
		}
	}
	for _, w := range e.Warnings { // an alert each: one joins its lines into a paragraph
		b.WriteString("\n> [!WARNING]\n> " + w + "\n")
	}
	if e.Effects != "" {
		b.WriteString("\n**Effects:** " + e.Effects + "\n")
	}
	return strings.TrimSpace(b.String())
}

// tick is s as inline code, with a fence long enough for the backticks in it.
func tick(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	fence := "`"
	for strings.Contains(s, fence) {
		fence += "`"
	}
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return fence + s + fence
}

// Explain asks Haiku (in dir, under account's login, no tools) what shell
// command cmd does, part by part, as markdown; env is more environment
// (ProviderEnv).
func Explain(ctx context.Context, home, account, dir, cmd string, env ...string) (string, error) {
	c, err := Command(home, account, "-p", "--model", "haiku", "--output-format", "json", "--json-schema", explainSchema,
		"--no-session-persistence", "--permission-mode", "dontAsk", "--tools", "", "--strict-mcp-config", explainBrief+cmd)
	if err != nil {
		return "", err
	}
	cc := exec.CommandContext(ctx, c.Path, c.Args[1:]...) // the same, ended by ctx
	// a command's words need no project's instructions (CLAUDE.md)
	cc.Dir, cc.Env = dir, append(append(c.Env, env...), "CLAUDE_CODE_DISABLE_CLAUDE_MDS=1")
	out, err := cc.Output()
	if e, perr := parseExplanation(out); perr == nil {
		return e.Markdown(), nil
	} else if err == nil {
		err = perr
	}
	return "", err
}

func parseExplanation(out []byte) (Explanation, error) {
	var r struct {
		IsError bool         `json:"is_error"`
		Result  string       `json:"result"`
		E       *Explanation `json:"structured_output"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return Explanation{}, err
	}
	if r.IsError || r.E == nil || r.E.Summary == "" {
		return Explanation{}, errors.New(cmp.Or(r.Result, "Haiku said nothing"))
	}
	return *r.E, nil
}
