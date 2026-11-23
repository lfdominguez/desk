package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/company"
)

// leadClaudes are the department leads' sessions h started.
func (h *harness) leadClaudes() []*fakeClaude {
	var out []*fakeClaude
	for _, f := range h.claudes {
		if strings.Contains(f.opts.System, " lead of ") {
			out = append(out, f)
		}
	}
	return out
}

// TestAskLead: ask_lead starts the organization's lead (read-only, in the
// organization's folder, its session kept in the company folder), hands it
// the brief, and its answer comes back into the card's session; both are
// in the company log, and a second brief goes to the same lead.
func TestAskLead(t *testing.T) {
	h := newHarness(t)
	id := acmeCard(h).ID
	h.u.send(id, "start") // the card moves to In progress: its old pointer goes stale
	c := h.u.b.Card(id)
	if text, ok := h.tool(c.ID, "ask_lead", `{"department":"security","objective":"May we store the token in .env?","context":"api/config.go","done_when":"yes or no"}`); !ok || !strings.HasPrefix(text, "Briefed the Security lead") {
		t.Fatalf("ask: %q %v", text, ok)
	}
	ls := h.leadClaudes()
	if len(ls) != 1 {
		t.Fatalf("%d leads", len(ls))
	}
	f, org := ls[0], filepath.Join(h.u.root, "Acme")
	args := strings.Join(f.opts.Args, " ")
	if f.opts.Dir != org || !strings.Contains(args, "--tools Read,Glob,Grep,Skill") || !f.opts.Strict || f.opts.Artifact || f.opts.New == "" || company.Sessions(org)["security"] != f.opts.New {
		t.Fatalf("options %+v", f.opts)
	}
	brief := "Brief from " + c.Key() + "'s Claude\nObjective: May we store the token in .env?\nWhere to look: api/config.go\nDone when: yes or no"
	if !slices.Equal(f.prompts, []string{brief}) {
		t.Fatalf("prompts %q", f.prompts)
	}
	main := h.mainClaudes()[0]
	sent := len(main.prompts)
	f.on(claude.Event{Kind: claude.KindInit, Session: "lead-sess"})
	f.on(claude.Event{Kind: claude.KindTool, Tool: "Read", ToolID: "t1", Text: "api/config.go"})
	f.on(claude.Event{Kind: claude.KindText, Text: "No: the vault.\nVERDICT: READY"})
	f.on(claude.Event{Kind: claude.KindAsk, Ask: &claude.Request{ID: "r1", Tool: "Bash", Line: "Bash ls"}})
	f.on(claude.Event{Kind: claude.KindResult})
	h.frame()
	if got := main.prompts[sent:]; len(got) != 1 || got[0] != "From the Security lead:\nNo: the vault.\nVERDICT: READY" {
		t.Fatalf("main got %q", got)
	}
	if ms := h.u.chat(c.ID); ms[len(ms)-1].Who != "lead" || ms[len(ms)-1].Name != "Security lead" || ms[len(ms)-1].Text != "No: the vault.\nVERDICT: READY" {
		t.Fatalf("card chat %+v", ms[len(ms)-1])
	}
	if m := h.u.chat(leadKey("Acme", "security")); len(m) < 2 || m[1].Who != "tool" {
		t.Errorf("the lead's own chat: %+v", m)
	}
	h.u.view = "sess:" + id // the answer drawn in the card's conversation, under the lead's name
	h.frame()
	if company.Sessions(org)["security"] != "lead-sess" || !slices.Equal(f.replies, []string{"r1 deny A department lead only reads: recommend instead."}) {
		t.Errorf("session %q, replies %q", company.Sessions(org)["security"], f.replies)
	}
	log := company.Read(org)
	if len(log) != 2 || log[0].Kind != "brief" || log[0].To != "Security lead" || log[1].Kind != "result" || log[1].Verdict != "READY" || log[1].To != c.Key() {
		t.Fatalf("log %+v", log)
	}
	h.tool(c.ID, "ask_lead", `{"department":"Security","objective":"And the key?"}`)
	f.on(claude.Event{Kind: claude.KindResult, Error: true, Text: "overloaded"})
	h.frame()
	if len(h.leadClaudes()) != 1 || len(f.prompts) != 2 || !strings.HasSuffix(main.prompts[len(main.prompts)-1], "(no answer: overloaded)") {
		t.Errorf("second brief: %d leads, prompts %q, main %q", len(h.leadClaudes()), f.prompts, main.prompts[len(main.prompts)-1])
	}
	f.on(claude.Event{Kind: claude.KindExit, Text: "boom"})
	h.frame()
	if h.u.leads[leadKey("Acme", "security")].proc != nil {
		t.Error("exited, still kept")
	}
	for args, want := range map[string]string{
		`{"department":"legal","objective":"x"}`: "no department legal",
		`{"department":"finance"}`:               "say the objective",
	} {
		if text, ok := h.tool(c.ID, "ask_lead", args); ok || !strings.HasPrefix(text, want) {
			t.Errorf("%s: %q", args, text)
		}
	}
	if text, ok := h.tool("ws:"+c.ID, "ask_lead", `{}`); ok || text != "no tool ask_lead" {
		t.Errorf("the workspace Claude: %q", text)
	}
}

// TestTaskLog: a subagent's brief and result go to the company log once
// each; background shells and cards that are gone don't.
func TestTaskLog(t *testing.T) {
	h := newHarness(t)
	c := acmeCard(h)
	h.u.onTask(c.ID, &claude.Task{ID: "a1", ToolUse: "tu1", Agent: "desk-qa", Status: "running"})
	h.u.onTask(c.ID, &claude.Task{ID: "a1", Prompt: "Check the login.", Status: "running"})
	h.u.onTask(c.ID, &claude.Task{ID: "a1", Status: "completed", Summary: "Fine.\nVERDICT: READY"})
	h.u.onTask(c.ID, &claude.Task{ID: "a1", Status: "completed"})
	h.u.onTask(c.ID, &claude.Task{ID: "s1", ToolUse: "tu2", Shell: true, Status: "completed"})
	h.u.onTask("gone", &claude.Task{ID: "a2", Prompt: "x"})
	log := company.Read(filepath.Join(h.u.root, "Acme"))
	if len(log) != 2 || log[0].Kind != "brief" || log[0].To != "desk-qa" || log[0].Text != "Check the login." ||
		log[1].Kind != "result" || log[1].From != "desk-qa" || log[1].Verdict != "READY" {
		t.Fatalf("log %+v", log)
	}
}

// TestAskLeadHeld: a lead's answer waits while the card's terminal is up,
// and none comes for a card that's gone; a lead that can't start says so.
func TestAskLeadHeld(t *testing.T) {
	h := newHarness(t)
	c := acmeCard(h)
	h.tool(c.ID, "ask_lead", `{"department":"engineering","objective":"Which ORM?"}`)
	f := h.leadClaudes()[0]
	h.u.terms[c.ID] = &termTab{}
	f.on(claude.Event{Kind: claude.KindText, Text: "The one in go.mod."})
	f.on(claude.Event{Kind: claude.KindResult})
	h.u.drain() // no frame: it would reap the stand-in terminal
	delete(h.u.terms, c.ID)
	if !strings.Contains(h.u.peerOut[c.ID], "From the Engineering lead:\nThe one in go.mod.") {
		t.Fatalf("held: %q", h.u.peerOut[c.ID])
	}
	st := h.u.leads[leadKey("Acme", "engineering")]
	h.u.leadTell("gone", st, "x")
	if err := h.u.leadAsk("gone", st.dept, company.Brief{Objective: "x"}); err == nil {
		t.Error("a card that's gone: an error")
	}
	st.proc = nil
	os.RemoveAll(company.Dir(filepath.Join(h.u.root, "Acme")))
	os.WriteFile(company.Dir(filepath.Join(h.u.root, "Acme")), nil, 0o644) // a file where the folder goes: the log can't be written
	h.u.logCompany("Acme", company.Entry{Text: "x"})
	h.frame()
	if !strings.HasPrefix(h.u.toast, "Company log: ") {
		t.Errorf("an unwritable log: %q", h.u.toast)
	}
	startClaude = func(claude.Options, func(claude.Event)) (session, error) { return nil, os.ErrPermission }
	if err := h.u.leadAsk(c.ID, st.dept, company.Brief{Objective: "x"}); err == nil {
		t.Error("a lead that can't start: an error")
	}
}
