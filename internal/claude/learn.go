package claude

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
)

// Lesson is what Learn understood of an organization's workflow: its
// orgs.Workflow without the user's own instructions.
type Lesson struct {
	Summary string            `json:"summary"`
	Rules   string            `json:"rules"`
	Status  map[string]string `json:"-"` // column → status
	Follow  map[string]string `json:"-"` // status → column
}

// Study is what Learn works from.
type Study struct {
	Home, Account string   // whose Claude
	Dir, Root     string   // the organization's folder, where the instructions' paths start; Root added for its skills
	Env           []string // more environment: ProviderEnv
	Instructions  string   // the user's
	Columns       []string // the board's, in order
	Statuses      string   // the trackers' statuses, read by desk (links.Statuses)
}

//go:embed learn.md
var learner string

// Learn has Haiku read the user's instructions on how the organization's
// tickets move, and what they point at (Read, Glob and Grep in its
// folder, nothing written), and say it as desk uses it: each column's
// status, the statuses others move tickets to and the card's column then,
// and the rules for every card's Claude.
func Learn(ctx context.Context, s Study) (Lesson, error) {
	cols, _ := json.Marshal(s.Columns)
	schema := strings.ReplaceAll(learnSchema, "COLUMNS", string(cols))
	in := "Columns: " + strings.Join(s.Columns, ", ") + "\n\nThe trackers' statuses (project: status (category), …):\n" +
		cmp.Or(s.Statuses, "(none read: no Jira project or Linear team in desk.toml)\n") + "\nThe user's instructions:\n" + s.Instructions
	args := append([]string{"-p", "--model", "haiku", "--output-format", "json", "--json-schema", schema,
		"--permission-mode", "dontAsk", "--tools", "Read,Glob,Grep", "--allowedTools", "Read,Glob,Grep", "--strict-mcp-config", "--no-session-persistence",
		"--append-system-prompt", learner}, addRoot(s.Dir, s.Root)...)
	c, err := Command(s.Home, s.Account, append(args, in)...)
	if err != nil {
		return Lesson{}, err
	}
	c = Jail(c, s.Home, s.Dir)
	cmd := exec.CommandContext(ctx, c.Path, c.Args[1:]...) // the same, ended by ctx
	cmd.Dir, cmd.Env = s.Dir, append(c.Env, s.Env...)
	out, err := cmd.Output()
	if l, perr := parseLesson(out); perr == nil {
		return l, nil
	} else if err == nil {
		err = perr
	}
	return Lesson{}, err
}

// learnSchema is the lesson's shape; COLUMNS becomes the board's.
const learnSchema = `{"type":"object","properties":{
"summary":{"type":"string","description":"For the user, 2-5 sentences: what you understood, and what was unclear or missing."},
"rules":{"type":"string","description":"For the card's Claude: what each status means here, when it moves a ticket itself (ticket_status) and to which status, what it must never do."},
"status":{"type":"array","items":{"type":"object","properties":{"column":{"enum":COLUMNS},"status":{"type":"string"}},"required":["column","status"]}},
"follow":{"type":"array","items":{"type":"object","properties":{"status":{"type":"string"},"column":{"enum":COLUMNS}},"required":["status","column"]}}},
"required":["summary","rules","status","follow"]}`

func parseLesson(out []byte) (Lesson, error) {
	type pair struct{ Column, Status string }
	var r struct {
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
		L       *struct {
			Summary, Rules string
			Status, Follow []pair
		} `json:"structured_output"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return Lesson{}, err
	}
	if r.IsError || r.L == nil || r.L.Summary == "" {
		return Lesson{}, errors.New(cmp.Or(r.Result, "Haiku said nothing"))
	}
	l := Lesson{Summary: r.L.Summary, Rules: r.L.Rules, Status: map[string]string{}, Follow: map[string]string{}}
	for _, p := range r.L.Status {
		if p.Status != "" {
			l.Status[p.Column] = p.Status
		}
	}
	for _, p := range r.L.Follow {
		if p.Status != "" {
			l.Follow[p.Status] = p.Column
		}
	}
	return l, nil
}

// WorkflowPrompt tells a card's Claude the organization's workflow, when
// it has rules: appended to its system prompt.
func WorkflowPrompt(rules string) string {
	if strings.TrimSpace(rules) == "" {
		return ""
	}
	return "\n\nThis organization's ticket workflow, as its people use it (it wins over what desk's columns suggest):\n" + rules +
		"\nWhen it says a ticket should move and it's yours to move, use desk's ticket_status tool (the status by its name); desk moves the card to match. " +
		"Never move a ticket into or out of a status it gives to someone else."
}
