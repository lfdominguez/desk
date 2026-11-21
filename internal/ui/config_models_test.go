package ui

import (
	"fmt"
	"image"
	"os"
	"strings"
	"testing"

	"gioui.org/layout"

	"github.com/lfdominguez/desk/internal/orgs"
)

// modelsHarness draws the settings alone in a window tall enough for an
// open provider card and the whole Models part.
func modelsHarness(h *harness) (image.Point, image.Rectangle, layout.Widget) {
	sz := image.Pt(1100, 5000)
	return sz, image.Rectangle{Max: sz}, func(gtx layout.Context) layout.Dimensions {
		h.u.configDialog(gtx)
		return layout.Dimensions{Size: sz}
	}
}

// TestConfigModels: a provider added in the settings, its models picked
// from the near ones, Not replaced and Custom; Explain moved onto it and a
// role onto Sonnet; Save writes them, keeping the rest of [company], and
// they read back.
func TestConfigModels(t *testing.T) {
	h := newHarness(t)
	h.orgToml("Acme", "[company]\nday_budget = 5.0\n")
	h.u.openConfig("Acme")
	sz, all, w := modelsHarness(h)
	h.draw(sz, w)
	d := h.u.cfg
	if d.changes() != 0 {
		t.Fatalf("changed on open: %d", d.changes())
	}
	h.clickIn(sz, all, w, "cfgAddprovider")
	e := d.entries[len(d.entries)-1]
	if e.kind != "provider" || d.open != e {
		t.Fatalf("added %q, open %v", e.kind, d.open == e)
	}
	e.ed("name").SetText("router")
	e.ed("base_url").SetText("https://openrouter.ai/api")
	e.src["."] = srcEnv
	e.ed("..token_env").SetText("ROUTER_KEY") // the account's own secret: "." + "." + its key
	id := fmt.Sprintf("cfg%p", e)
	near := tierInfo["haiku"].near
	h.clickIn(sz, all, w, id+"haiku2") // the second near one
	if got := e.text("haiku"); got != near[1].id {
		t.Fatalf("near: %q", got)
	}
	h.clickIn(sz, all, w, id+"opus"+fmt.Sprint(len(tierInfo["opus"].near)+1)) // Custom: its field shows
	if !e.custom["opus"] {
		t.Fatal("custom not picked")
	}
	e.ed("opus").SetText("acme/own-model")
	h.draw(sz, w)
	h.clickIn(sz, all, w, id+"sonnet1")
	h.clickIn(sz, all, w, id+"sonnet0") // Not replaced
	if e.text("sonnet") != "" || e.custom["sonnet"] {
		t.Fatalf("not replaced: %q", e.text("sonnet"))
	}
	d.set.sub["Acme"] = "models"
	h.clickIn(sz, all, w, "cfgTaskexplain1")
	d.set.sub["Acme"] = "company"
	h.clickIn(sz, all, w, "cfgRoletl2")
	h.clickIn(sz, all, w, "cfgRoleqa4")
	if d.changes() != 2 { // the provider, the Models part
		t.Fatalf("changes %d", d.changes())
	}
	h.u.saveConfig()
	if h.u.cfg != nil {
		t.Fatalf("not saved: %s", h.u.cfg.err)
	}
	c, err := orgs.Load(h.u.root, "Acme")
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.ProviderFor("explain")
	if err != nil || p == nil || p.BaseURL != "https://openrouter.ai/api" || p.TokenEnv != "ROUTER_KEY" || p.Opus != "acme/own-model" || p.Haiku != near[1].id || p.Sonnet != "" {
		t.Fatalf("provider %+v %v", p, err)
	}
	if p, _ := c.ProviderFor("card"); p != nil {
		t.Fatal("the card stays on the subscription")
	}
	if c.Company.Models["tl"] != "sonnet" || c.Company.Models["qa"] != "inherit" || c.Company.DayBudget != 5 {
		t.Fatalf("company %+v", c.Company)
	}

	// read back: nothing changed; a model not near any shows as Custom;
	// a role back to its own drops its key, and the empty table goes
	h.u.openConfig("Acme")
	h.draw(sz, w)
	d = h.u.cfg
	if d.changes() != 0 {
		t.Fatalf("changed on reopen: %d", d.changes())
	}
	d.open = d.entries[0]
	d.open.ed("base_url").SetText("https://llm.example.com") // not OpenRouter: its names may differ
	h.draw(sz, w)
	d.set.sub["Acme"] = "company"
	h.clickIn(sz, all, w, "cfgRoletl0")
	h.clickIn(sz, all, w, "cfgRoleqa0")
	d.set.sub["Acme"] = "models"
	h.clickIn(sz, all, w, "cfgTaskexplain0")
	h.u.saveConfig()
	b, _ := os.ReadFile(orgs.File(h.u.root, "Acme"))
	if s := string(b); strings.Contains(s, "models") || !strings.Contains(s, "day_budget") || !strings.Contains(s, "acme/own-model") {
		t.Fatalf("saved:\n%s", s)
	}
}

// TestConfigModelsRefused: Save refuses a provider without a name or a
// base URL, two of one name, and a task on a provider that isn't there;
// [models]' keys desk doesn't know are kept.
func TestConfigModelsRefused(t *testing.T) {
	h := newHarness(t)
	h.orgToml("Acme", "[models]\ncard = \"gone\"\nlater = \"x\"\n\n[[provider]]\nname = \"router\"\nbase_url = \"https://openrouter.ai/api\"\n")
	h.u.openConfig("Acme")
	sz, _, w := modelsHarness(h)
	h.draw(sz, w)
	d := h.u.cfg
	if h.u.saveOrg(d); d.err != "Card sessions run on gone, which isn't a model provider here" {
		t.Fatalf("missing: %q", d.err)
	}
	d.models.ed("task.card").SetText("router")
	add := newEntry("provider", map[string]any{})
	d.entries = append(d.entries, add)
	for _, tc := range []struct{ name, url, want string }{
		{"", "", "A model provider needs a name"},
		{"other", "", "other needs its base URL"},
		{"router", "https://example.com", "Two model providers are called router"},
	} {
		add.ed("name").SetText(tc.name)
		add.ed("base_url").SetText(tc.url)
		if d.err = ""; h.u.saveOrg(d) || d.err != tc.want {
			t.Fatalf("%+v: %q", tc, d.err)
		}
	}
	d.entries = d.entries[:len(d.entries)-1]
	if !h.u.saveOrg(d) {
		t.Fatalf("refused: %q", d.err)
	}
	raw, _ := orgs.LoadRaw(h.u.root, "Acme")
	if m, _ := raw["models"].(map[string]any); m["later"] != "x" || m["card"] != "router" {
		t.Fatalf("models %v", raw["models"])
	}
}
