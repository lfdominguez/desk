package ui

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/vault"
)

// TestConfigWorkflow: the page reads [workflow]; Learn asks for the
// instructions first, then has Haiku learn them with the Jira statuses and
// fills the fields; Save writes them and they read back; a line that names
// no column is refused.
func TestConfigWorkflow(t *testing.T) {
	h := newHarness(t)
	h.orgToml("Acme", "[workflow]\ninstructions = \"old\"\nlater = 1\n\n[workflow.status]\nGone = \"Kept\"\n")
	var got claude.Study
	jiraStatuses = func(context.Context, orgs.Config) (string, error) { return "ABC: Test (indeterminate)\n", nil }
	learnRun = func(_ context.Context, s claude.Study) (claude.Lesson, error) {
		got = s
		return claude.Lesson{Summary: "Test is deployed.", Rules: "Move to Test after deploying.",
			Status: map[string]string{"Review": "Code Review"}, Follow: map[string]string{"Test": "Review", "Deployed": "Done"}}, nil
	}
	h.u.openConfig("Acme")
	h.u.cfg.set.sub["Acme"] = "workflow"
	sz, all, w := modelsHarness(h)
	h.draw(sz, w)
	d := h.u.cfg
	if d.wf.ed("instructions").Text() != "old" || d.changes() != 0 {
		t.Fatalf("read %q, %d changes", d.wf.ed("instructions").Text(), d.changes())
	}
	d.wf.ed("instructions").SetText(" ")
	h.clickIn(sz, all, w, "cfgLearn")
	if !strings.Contains(d.wfErr, "first") {
		t.Fatalf("no instructions: %q", d.wfErr)
	}
	d.wf.ed("instructions").SetText("Read ./process")
	h.clickIn(sz, all, w, "cfgLearn")
	h.pumpUntil("the lesson", func() bool { return !d.wfBusy })
	h.draw(sz, w)
	if got.Instructions != "Read ./process" || got.Statuses != "ABC: Test (indeterminate)\n" || len(got.Columns) != 6 {
		t.Fatalf("asked %+v", got)
	}
	if d.wf.text("status.Review") != "Code Review" || d.wf.ed("follow").Text() != "Deployed → Done\nTest → Review" || d.wf.text("learned") == "" || d.wfErr != "" {
		t.Fatalf("filled %q %q, err %q", d.wf.text("status.Review"), d.wf.ed("follow").Text(), d.wfErr)
	}
	for _, bad := range []string{"Test → Nowhere", "Test Review", " → Review"} {
		d.wf.ed("follow").SetText(bad)
		if d.err = ""; h.u.saveOrg(d) || !strings.Contains(d.err, "line 1") {
			t.Fatalf("%q: %q", bad, d.err)
		}
	}
	d.wf.ed("follow").SetText("Test -> review\n\nDeployed = Done")
	if !h.u.saveOrg(d) {
		t.Fatalf("refused: %q", d.err)
	}
	c, err := orgs.Load(h.u.root, "Acme")
	if err != nil || c.Workflow.Follow["Test"] != "Review" || c.Workflow.Status["Review"] != "Code Review" || c.Workflow.Status["Gone"] != "Kept" ||
		c.Workflow.Rules != "Move to Test after deploying." || c.Workflow.Summary != "Test is deployed." {
		t.Fatalf("%+v %v", c.Workflow, err)
	}

	// as read, kept as it was; everything emptied, no [workflow]
	h.u.openConfig("Acme")
	h.u.cfg.set.sub["Acme"] = "workflow"
	d = h.u.cfg
	h.u.saveOrg(d)
	if b, _ := os.ReadFile(orgs.File(h.u.root, "Acme")); !strings.Contains(string(b), "later") {
		t.Fatalf("unknown keys lost:\n%s", b)
	}
	for k := range d.wf.eds {
		d.wf.ed(k).SetText("")
	}
	h.orgToml("Acme", "")
	d.raw = map[string]any{}
	h.u.saveOrg(d)
	if b, _ := os.ReadFile(orgs.File(h.u.root, "Acme")); strings.Contains(string(b), "workflow") {
		t.Fatalf("an empty workflow written:\n%s", b)
	}
}

// TestConfigWorkflowFails: Learn says why it failed (a locked vault asks to
// unlock), and Stop stops it.
func TestConfigWorkflowFails(t *testing.T) {
	h := newHarness(t)
	h.u.openConfig("Acme")
	h.u.cfg.set.sub["Acme"] = "workflow"
	sz, all, w := modelsHarness(h)
	h.draw(sz, w)
	d := h.u.cfg
	d.wf.ed("instructions").SetText("Read ./process")
	jiraStatuses = func(context.Context, orgs.Config) (string, error) {
		return "", &vault.LockedError{Vault: orgs.Vault{Name: "pw", Kind: "bitwarden"}}
	}
	h.clickIn(sz, all, w, "cfgLearn")
	h.pumpUntil("the failure", func() bool { return !d.wfBusy })
	if !strings.Contains(d.wfErr, "locked") || len(h.u.locked) != 1 {
		t.Fatalf("%q, locked %d", d.wfErr, len(h.u.locked))
	}
	jiraStatuses = func(context.Context, orgs.Config) (string, error) { return "", nil }
	learnRun = func(ctx context.Context, _ claude.Study) (claude.Lesson, error) {
		<-ctx.Done()
		return claude.Lesson{}, ctx.Err()
	}
	h.clickIn(sz, all, w, "cfgLearn")
	h.draw(sz, w) // Learning…, with Stop
	h.clickIn(sz, all, w, "cfgLearnStop")
	h.pumpUntil("the stop", func() bool { return !d.wfBusy })
	if d.wfErr != "Stopped" {
		t.Fatalf("%q", d.wfErr)
	}
	learnRun = func(context.Context, claude.Study) (claude.Lesson, error) {
		return claude.Lesson{}, errors.New("overloaded")
	}
	h.clickIn(sz, all, w, "cfgLearn")
	h.pumpUntil("the failure", func() bool { return !d.wfBusy })
	if d.wfErr != "overloaded" {
		t.Fatalf("%q", d.wfErr)
	}
	h.orgToml("Acme", "not toml [")
	h.u.learnWorkflow(d)
	if d.wfBusy || d.wfErr == "" {
		t.Fatalf("a desk.toml that doesn't read: %q", d.wfErr)
	}
}

// TestConfigWorkflowClosed: closing the settings, or opening them over,
// stops Learn; a hand-written line Save can't read doesn't hold up a page
// whose workflow wasn't touched.
func TestConfigWorkflowClosed(t *testing.T) {
	h := newHarness(t)
	h.u.stopLearning() // no settings open: nothing to stop
	learnRun = func(ctx context.Context, _ claude.Study) (claude.Lesson, error) {
		<-ctx.Done()
		return claude.Lesson{}, ctx.Err()
	}
	for _, end := range []func(){h.u.dropConfig, func() { h.u.openConfig("Acme") }} {
		h.u.openConfig("Acme")
		h.u.cfg.set.sub["Acme"] = "workflow"
	h.u.cfg.set.sub["Acme"] = "workflow"
		d := h.u.cfg
		d.wf.ed("instructions").SetText("Read ./process")
		h.u.learnWorkflow(d)
		end()
		h.pumpUntil("the stop", func() bool { return !d.wfBusy })
		if d.wfErr != "Stopped" {
			t.Fatalf("%q", d.wfErr)
		}
	}
	h.orgToml("Acme", "[workflow.follow]\nTest = \"Nowhere\"\n")
	h.u.openConfig("Acme")
	h.u.cfg.set.sub["Acme"] = "workflow"
	if d := h.u.cfg; !h.u.saveOrg(d) || d.err != "" {
		t.Fatalf("held up: %q", d.err)
	}
}
