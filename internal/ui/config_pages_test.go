package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lfdominguez/desk/internal/orgs"
)

// navTitles is the nav's rows as their titles.
func (h *harness) navTitles() (out []string) {
	for _, r := range h.u.navRows() {
		out = append(out, r.title)
	}
	return out
}

// TestConfigNav2: desk's pages, then each organization, the open one with
// its parts; a part's row shows it, the open organization's row folds it;
// the search lists the parts that match, across organizations.
func TestConfigNav2(t *testing.T) {
	h := newHarness(t)
	h.u.openConfig("Acme")
	sz, all, w := cfgHarness(h)
	h.draw(sz, w)
	got := strings.Join(h.navTitles(), ",")
	if !strings.HasPrefix(got, "desk,General,Search folders,Telegram,Keyboard shortcuts,Diagnostics,Organizations · 2,Acme,Accounts,Ticket workflow,Git & review,Triggers") ||
		!strings.HasSuffix(got, "About me,Globex") {
		t.Fatalf("nav %s", got)
	}
	h.clickIn(sz, all, w, "cfgSubAcme/triggers")
	if h.u.cfg.page() != "triggers" || !h.has("Acme · Triggers") {
		t.Fatalf("page %q", h.u.cfg.page())
	}
	h.clickIn(sz, all, w, "cfgOrgAcme") // folds it
	if h.u.cfg.set.open["Acme"] || strings.Contains(strings.Join(h.navTitles(), ","), "Triggers") {
		t.Fatal("not folded")
	}
	h.clickIn(sz, all, w, "cfgOrgGlobex")
	if h.u.cfg.org != "Globex" || h.u.cfg.page() != "accounts" || !h.u.cfg.set.open["Globex"] {
		t.Fatalf("Globex %q %q", h.u.cfg.org, h.u.cfg.page())
	}
	h.clickIn(sz, all, w, "cfgSub"+cfgFolders+"/telegram")
	if h.u.cfg.org != cfgFolders || h.u.cfg.page() != "telegram" {
		t.Fatal("phone")
	}
	h.clickIn(sz, all, w, "cfgOrg"+cfgKeys)
	if h.u.cfg.org != cfgKeys || h.u.cfg.page() != "" {
		t.Fatal("keys")
	}
	h.u.cfg.set.q.SetText("worktree")
	if got := strings.Join(h.navTitles(), ","); got != "Acme › Git & review,Globex › Git & review" {
		t.Fatalf("search %s", got)
	}
	h.u.cfg.set.q.SetText("keyboard")
	if got := h.navTitles(); len(got) != 1 || got[0] != "Keyboard shortcuts" {
		t.Fatalf("search %v", got)
	}
	h.u.cfg.set.q.SetText("telegram")
	h.draw(sz, w)
	h.clickIn(sz, all, w, "cfgSub"+cfgFolders+"/telegram")
	h.u.cfg.set.q.SetText("zzzq")
	h.draw(sz, w)
	if !h.has("Nothing matches.") {
		t.Fatal("no match not said")
	}
}

// TestConfigPartsDraw: every part of an organization and of desk draws,
// its title and what it is above it; a broken desk.toml says so on each.
func TestConfigPartsDraw(t *testing.T) {
	h := newHarness(t)
	h.u.openConfig("Acme")
	sz, _, w := cfgHarness(h)
	for _, p := range orgParts {
		h.u.cfg.set.sub["Acme"] = p.key
		h.draw(sz, w)
		if !h.has("Acme · " + p.title) {
			t.Fatalf("%s not shown", p.key)
		}
	}
	h.u.cfgPage(cfgFolders)
	for _, p := range deskParts {
		h.u.cfg.set.sub[cfgFolders] = p.key
		h.draw(sz, w)
		if !h.has(p.about) {
			t.Fatalf("%s not shown", p.key)
		}
	}
	h.orgToml("Globex", "not toml [")
	h.u.openConfig("Globex")
	for _, p := range []string{"git", "triggers"} {
		h.u.cfg.set.sub["Globex"] = p
		h.draw(sz, w)
		if !h.has("Fix the file by hand") {
			t.Fatalf("%s: broken not said", p)
		}
	}
}

// TestConfigTriggers: + Trigger opens one to fill; its choice, number and
// switches save as desk.toml's [[trigger]], which desk reads back; a name
// is needed and After is a whole number.
func TestConfigTriggers(t *testing.T) {
	h := newHarness(t)
	h.u.openConfig("Acme")
	h.u.cfg.set.sub["Acme"] = "triggers"
	sz, all, w := cfgHarness(h)
	h.draw(sz, w)
	if !h.has("No triggers yet") {
		t.Fatal("empty not said")
	}
	h.clickIn(sz, all, w, "cfgAddtrigger")
	d := h.u.cfg
	e := d.entries[len(d.entries)-1]
	if e.kind != "trigger" || d.open != e || e.text("on") != "ticket" {
		t.Fatalf("%+v", e)
	}
	id := fmt.Sprintf("cfg%p", e)
	h.clickIn(sz, all, w, id+"on3") // schedule
	h.clickIn(sz, all, w, id+"start")
	h.u.saveConfig()
	if !strings.Contains(d.err, "trigger needs a name") {
		t.Fatalf("err %q", d.err)
	}
	e.ed("name").SetText("audit")
	e.ed("at").SetText("09:00")
	e.ed("days").SetText("mon, thu")
	e.ed("after").SetText("ten")
	d.err = ""
	h.u.saveConfig()
	if !strings.Contains(d.err, "whole number") {
		t.Fatalf("err %q", d.err)
	}
	e.ed("after").SetText("10")
	d.err = ""
	h.u.saveConfig()
	c, err := orgs.Load(h.u.root, "Acme")
	if err != nil || len(c.Triggers) != 1 {
		t.Fatalf("%+v %v", c.Triggers, err)
	}
	if tr := c.Triggers[0]; tr.Name != "audit" || tr.On != "schedule" || !tr.Start || tr.After != 10 || len(tr.Days) != 2 || tr.Off {
		t.Fatalf("%+v", tr)
	}
	h.u.openConfig("Acme") // reads back unchanged, its summary as it is
	if d := h.u.cfg; d.changes() != 0 || !strings.Contains(d.kinds(true)[0].summary(), "schedule") || !strings.Contains(d.kinds(true)[0].summary(), "start its claude") {
		t.Fatalf("reopened %d %q", d.changes(), d.kinds(true)[0].summary())
	}
	if len(h.u.cfg.kinds(false)) != 0 {
		t.Fatal("a trigger among the accounts")
	}
}

// TestConfigGit: the worktrees switch, an account's worklog and the
// budgets save to desk.toml, keeping what else is in [company]; a budget
// that isn't dollars is refused.
func TestConfigGit(t *testing.T) {
	h := newHarness(t)
	h.orgToml("Acme", "[company]\nskip = [\"qa\"]\n\n[[jira]]\nurl = \"https://globex.atlassian.net\"\n")
	h.u.openConfig("Acme")
	h.u.cfg.set.sub["Acme"] = "git"
	sz, all, w := cfgHarness(h)
	d := h.u.cfg
	h.clickIn(sz, all, w, "cfgWorktrees")
	h.clickIn(sz, all, w, fmt.Sprintf("cfg%p", d.entries[0])+"worklog")
	if !d.git.on["worktrees"] || !d.entries[0].on["worklog"] || d.changes() != 2 {
		t.Fatalf("changes %d", d.changes())
	}
	d.set.sub["Acme"] = "company"
	h.draw(sz, w)
	d.comp.ed("card_budget").SetText("lots")
	h.u.saveConfig()
	if !strings.Contains(d.err, "dollars") {
		t.Fatalf("err %q", d.err)
	}
	d.err = ""
	d.comp.ed("card_budget").SetText("$5")
	h.u.saveConfig()
	c, err := orgs.Load(h.u.root, "Acme")
	if err != nil || !c.Git.Worktrees || !c.Jira[0].Worklog || c.Company.CardBudget != 5 || len(c.Company.Skip) != 1 {
		t.Fatalf("%+v %+v %v", c.Git, c.Company, err)
	}
	h.u.openConfig("Acme") // off again: the tables go
	d = h.u.cfg
	d.git.on["worktrees"], d.entries[0].on["worklog"] = false, false
	d.comp.ed("card_budget").SetText("")
	h.u.saveConfig()
	b, _ := os.ReadFile(orgs.File(h.u.root, "Acme"))
	if s := string(b); strings.Contains(s, "[git]") || strings.Contains(s, "worklog") || strings.Contains(s, "card_budget") || !strings.Contains(s, "skip") {
		t.Fatalf("saved\n%s", s)
	}
	h.orgToml("Globex", "")
	h.u.openConfig("Globex")
	h.u.cfg.set.sub["Globex"] = "git"
	h.draw(sz, w)
	if !h.has("No Jira or Linear account") {
		t.Fatal("no accounts not said")
	}
}

// TestConfigPhone: the Phone part shows the paired chat and unpairs it on
// Save; its token's source, the switch and Diagnostics' key save to
// settings.toml.
func TestConfigPhone(t *testing.T) {
	h := newHarness(t)
	os.MkdirAll(filepath.Dir(orgs.SettingsFile()), 0o700)
	os.WriteFile(orgs.SettingsFile(), []byte("[phone]\nchat_id = 42\ntoken = \"123:abc\"\n"), 0o600)
	h.u.openConfig(cfgFolders)
	h.u.cfg.set.sub[cfgFolders] = "telegram"
	sz, all, w := cfgHarness(h)
	h.draw(sz, w)
	if !h.has("Paired with Telegram chat 42") {
		t.Fatal("pairing not shown")
	}
	d := h.u.cfg
	if d.phone.src["."] != srcFile || d.changes() != 0 {
		t.Fatalf("src %d changes %d", d.phone.src["."], d.changes())
	}
	h.clickIn(sz, all, w, "cfgUnpair")
	if !h.has("Unpaired on Save") {
		t.Fatal("unpair not said")
	}
	h.clickIn(sz, all, w, "cfgUnpair") // Keep paired
	h.clickIn(sz, all, w, "cfgUnpair")
	h.clickIn(sz, all, w, "cfgPhone.src1") // Environment variable: no vault here
	d.phone.ed("..token_env").SetText("DESK_TG")
	h.clickIn(sz, all, w, "cfgPhonealways")
	d.set.sub[cfgFolders] = "diag"
	h.draw(sz, w)
	d.diag.ed("license_key").SetText("nr-key")
	h.u.saveConfig()
	p, nr := orgs.PhoneSettings(), orgs.Telemetry()
	if p.Chat != 0 || p.TokenEnv != "DESK_TG" || p.Token != "" || !p.Always || nr.LicenseKey != "nr-key" {
		t.Fatalf("%+v %+v", p, nr)
	}
	for _, src := range []int{srcCmd, srcFile} { // each source writes its own key
		h.u.openConfig(cfgFolders)
		e := h.u.cfg.phone
		e.src["."] = src
		e.ed("..token_cmd").SetText("pass tg")
		e.ed("..token").SetText("9:z")
		h.u.saveConfig()
	}
	if p := orgs.PhoneSettings(); p.Token != "9:z" || p.TokenCmd != "" {
		t.Fatalf("%+v", p)
	}
}

// TestConfigReviewFixes: settings.toml stays private; a pairing made while
// Settings is open survives its Save, Unpair reaches the running bot; a
// switch turned on and off is no change; a refused Save shows the part it
// is about; Discard stays on its part; NaN isn't dollars; desk.toml's own
// top-level phone key is kept.
func TestConfigReviewFixes(t *testing.T) {
	h := newHarness(t)
	os.MkdirAll(filepath.Dir(orgs.SettingsFile()), 0o700)
	os.WriteFile(orgs.SettingsFile(), []byte("[phone]\ntoken = \"1:a\"\n"), 0o600)
	h.u.phone = &phoneState{chat: 0}
	h.u.openConfig(cfgFolders)
	h.u.cfg.set.sub[cfgFolders] = "telegram"
	sz, all, w := cfgHarness(h)
	h.draw(sz, w)
	h.clickIn(sz, all, w, "cfgPhonealways")
	h.clickIn(sz, all, w, "cfgPhonealways")
	if n := h.u.cfg.changes(); n != 0 {
		t.Fatalf("on and off: %d changes", n)
	}
	orgs.SetPhoneChat(77) // a /start meanwhile
	h.clickIn(sz, all, w, "cfgPhonealways")
	h.u.saveConfig()
	if p := orgs.PhoneSettings(); p.Chat != 77 || !p.Always {
		t.Fatalf("%+v", p)
	}
	if h.u.phone.chat != 77 || !h.u.phone.always || h.u.toast != "Saved desk's settings" {
		t.Fatalf("bot %+v toast %q", h.u.phone, h.u.toast)
	}
	if fi, err := os.Stat(orgs.SettingsFile()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", fi.Mode(), err)
	}
	h.u.openConfig(cfgFolders)
	h.u.cfg.set.sub[cfgFolders] = "telegram"
	h.draw(sz, w)
	h.clickIn(sz, all, w, "cfgUnpair")
	h.u.saveConfig()
	if h.u.phone.chat != 0 || !h.u.phone.pairUntil.After(time.Now()) || orgs.PhoneSettings().Chat != 0 {
		t.Fatalf("unpair %+v", h.u.phone)
	}

	h.orgToml("Acme", "phone = \"kept\"\n\n[[trigger]]\non = \"ticket\"\n")
	h.u.openConfig("Acme")
	h.u.cfg.set.sub["Acme"] = "git"
	h.draw(sz, w)
	h.clickIn(sz, all, w, "cfgWorktrees")
	h.clickIn(sz, all, w, "cfgWorktrees")
	if n := h.u.cfg.changes(); n != 0 {
		t.Fatalf("worktrees on and off: %d", n)
	}
	h.u.cfg.set.sub["Acme"] = "company"
	h.draw(sz, w)
	h.u.cfg.comp.ed("day_budget").SetText("NaN")
	h.u.cfg.set.sub["Acme"] = "accounts"
	h.u.saveConfig()
	if d := h.u.cfg; !strings.Contains(d.err, "dollars") || d.page() != "company" {
		t.Fatalf("err %q on %q", d.err, d.page())
	}
	h.u.cfg.comp.ed("day_budget").SetText("5")
	h.u.cfg.set.sub["Acme"] = "accounts"
	h.u.saveConfig() // the trigger has no name
	if d := h.u.cfg; !strings.Contains(d.err, "needs a name") || d.page() != "triggers" {
		t.Fatalf("err %q on %q", d.err, d.page())
	}
	h.draw(sz, w)
	h.clickIn(sz, all, w, "cfgDiscard")
	if h.u.cfg.page() != "triggers" || h.u.cfg.changes() != 0 {
		t.Fatalf("discard on %q", h.u.cfg.page())
	}
	h.u.cfg.kinds(true)[0].ed("name").SetText("t")
	h.u.saveConfig()
	if b, _ := os.ReadFile(orgs.File(h.u.root, "Acme")); !strings.Contains(string(b), "kept") {
		t.Fatalf("saved\n%s", b)
	}
}
