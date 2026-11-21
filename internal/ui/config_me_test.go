package ui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/term"
)

// TestConfigMe: General's Who am I and an organization's (its text, and
// Extend from global switched off) save and read back; switching it on
// again drops extend; emptied, no [me], other keys kept.
func TestConfigMe(t *testing.T) {
	h := newHarness(t)
	h.u.openConfig(cfgFolders)
	d := h.u.cfg
	d.langs.ed("me").SetText("I write Go.")
	if d.changes() != 1 || !h.u.saveFolders(d) || orgs.Me() != "I write Go." {
		t.Fatalf("general: %d changes, %q, %q", d.changes(), d.err, orgs.Me())
	}

	h.orgToml("Acme", "[me]\nlater = 1\n")
	h.u.openConfig("Acme")
	h.u.cfg.set.sub["Acme"] = "me"
	sz, all, w := modelsHarness(h)
	h.draw(sz, w)
	d = h.u.cfg
	if !d.me.on["extend"] || d.changes() != 0 {
		t.Fatalf("read: extend %v, %d changes", d.me.on["extend"], d.changes())
	}
	d.me.ed("about").SetText("Only a reviewer here.")
	h.clickIn(sz, all, w, "cfgMeExtend")
	if d.me.on["extend"] || d.changes() != 1 {
		t.Fatalf("switched: %v, %d changes", d.me.on["extend"], d.changes())
	}
	h.draw(sz, w) // its line says the global one is left out
	if !h.u.saveOrg(d) || orgs.AboutMe(h.u.root, "Acme") != "Only a reviewer here." {
		t.Fatalf("%q %q", d.err, orgs.AboutMe(h.u.root, "Acme"))
	}
	h.u.openConfig("Acme")
	h.u.cfg.set.sub["Acme"] = "me"
	d = h.u.cfg
	d.me.on["extend"] = true
	if h.u.saveOrg(d); orgs.AboutMe(h.u.root, "Acme") != "I write Go.\n\nOnly a reviewer here." {
		t.Fatalf("extended: %q", orgs.AboutMe(h.u.root, "Acme"))
	}
	h.u.openConfig("Acme")
	h.u.cfg.set.sub["Acme"] = "me"
	d = h.u.cfg
	d.me.ed("about").SetText("")
	h.u.saveOrg(d)
	if b, _ := os.ReadFile(orgs.File(h.u.root, "Acme")); strings.Contains(string(b), "about") || !strings.Contains(string(b), "later") {
		t.Fatalf("emptied:\n%s", b)
	}
	h.orgToml("Acme", "")
	h.u.openConfig("Acme")
	h.u.cfg.set.sub["Acme"] = "me"
	d = h.u.cfg
	d.me.ed("about").SetText("x")
	d.me.ed("about").SetText("")
	d.me.on["extend"] = true
	d.me.base = "" // edited, with nothing to say: no [me] at all
	h.u.saveOrg(d)
	if b, _ := os.ReadFile(orgs.File(h.u.root, "Acme")); strings.Contains(string(b), "[me]") {
		t.Fatalf("an empty [me]:\n%s", b)
	}
}

// TestAboutMeTold: the card's session, the workspace, lead and drawing
// Claudes, the card's terminal and Improve are told who the user is.
func TestAboutMeTold(t *testing.T) {
	h := newHarness(t)
	orgs.SaveMe("I write Go.")
	h.orgToml("Acme", "[me]\nabout = \"Backend lead here.\"\n")
	want := "I write Go.\n\nBackend lead here."
	c := h.u.b.Add(0, kanban.Card{Title: "Fix it", Org: "Acme"})
	h.u.send(c.ID, "go")
	if !strings.Contains(h.claudes[0].opts.System, want) {
		t.Fatal("the card's Claude isn't told")
	}
	if _, err := h.u.wsStart(c.ID); err != nil || !strings.Contains(h.claudes[1].opts.System, want) {
		t.Fatalf("workspace: %v", err)
	}
	if _, err := h.u.leadStart(&leadState{org: "Acme"}, "lead:Acme:x"); err != nil || !strings.Contains(h.claudes[2].opts.System, want) {
		t.Fatalf("lead: %v", err)
	}
	if _, err := h.u.drawStart(&drawState{org: "Acme", path: filepath.Join(t.TempDir(), "x.excalidraw")}); err != nil || !strings.Contains(h.claudes[3].opts.System, want) {
		t.Fatalf("drawing: %v", err)
	}
	var targs []string
	startTerm = func(cmd *exec.Cmd, _, _ int, _ func()) (*term.Term, error) {
		targs = cmd.Args
		return nil, errors.New("no terminal here")
	}
	defer func() { startTerm = term.Start }()
	h.u.terminal(c.ID)
	if !strings.Contains(strings.Join(targs, "\n"), want) {
		t.Fatalf("terminal: %q", targs)
	}
	var asked claude.Ask
	optimize = func(_ context.Context, a claude.Ask, _ func(claude.Progress)) (claude.Draft, error) {
		asked = a
		return claude.Draft{Title: a.Request}, nil
	}
	h.u.newCard(0)
	s := h.u.edit
	s.notes.SetText("do it @Acme")
	h.u.optimizeCard()
	h.pumpUntil("the draft", func() bool { return s.optimized })
	if asked.About != want {
		t.Fatalf("improve: %q", asked.About)
	}
	if !strings.Contains(h.u.aboutMe("Acme"), want) || h.u.aboutMe("Globex") != claude.AboutUser("I write Go.") {
		t.Fatal("aboutMe")
	}
}
