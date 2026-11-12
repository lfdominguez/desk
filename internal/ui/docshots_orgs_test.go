package ui

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"time"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/vault"
)

// The Organizations section's scenes: settings, accounts, vaults, and each
// org's Tickets, CI and Alerts views over the demo's fakes.

// orgsFetch asks the demo's trackers, CI and Nagios once, as desk's link
// poll does at start, and waits until every view has its data.
func orgsFetch(s *shooter) {
	go func() { s.u.checkTickets(); s.u.checkAlerts(); s.u.checkCI() }()
	s.wait("tickets, alerts and CI", 10*time.Second, func() bool {
		_, tk := s.u.tickets["Acme"]
		_, al := s.u.alerts["Globex"]
		_, ci := s.u.ci["Acme"]
		return tk && al && ci
	})
}

// orgsEntryID is the Click id prefix of settings entry i on the page shown.
func orgsEntryID(s *shooter, i int) string { return fmt.Sprintf("cfg%p", s.u.cfg.entries[i]) }

// orgsAway moves the pointer off every button (the board's empty bottom
// right), so no hover shows in the shot.
func orgsAway(s *shooter) {
	s.move(f32.Pt(float32(1590*shotScale), float32(940*shotScale)))
	s.settle()
}

// orgsCard is the id of the demo card titled title.
func orgsCard(s *shooter, title string) string {
	for _, c := range s.u.boardCards() {
		if c.Title == title {
			return c.ID
		}
	}
	s.t.Fatalf("no card %q", title)
	return ""
}

func init() {
	scenes = append(scenes,
		scene{"orgs-sidebar", func(s *shooter) {
			orgsFetch(s)
			r := around(union(s.at("sideorg:Acme"), s.at("sideOrgCfg"), s.at("sidetk:Initech")), 6)
			orgsAway(s)
			s.shot("orgs-sidebar", cut{"orgs", r, true})
		}},
		scene{"orgs-account", func(s *shooter) {
			// a Claude profile for Globex, as ~/Secure/Claudes/Globex would be
			sec := filepath.Join(os.Getenv("HOME"), "Secure", "Claudes")
			os.MkdirAll(filepath.Join(sec, "Globex"), 0o700)
			os.WriteFile(filepath.Join(sec, "claude-globex.json"), []byte(`{"oauthAccount":{"emailAddress":"me@globex.example.com"}}`), 0o600)
			s.u.openSession(orgsCard(s, "Migrate the billing exports to S3"))
			s.settle()
			s.shot("orgs-account", cut{"header", dp(236, 52, 900, 128), true})
		}},
		scene{"orgs-settings-general", func(s *shooter) {
			// a nested organization (Acme's billing, with its own desk.toml) and
			// a folder that isn't one (no desk.toml), for the preview to show
			home := os.Getenv("HOME")
			s.t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config")) // the footer's path from ~
			os.MkdirAll(filepath.Join(home, "Work", "Acme", "billing"), 0o755)
			writeAt(filepath.Join(home, "Work", "Acme", "billing", ".desk", "desk.toml"), []byte("org = \"Billing\"\n"), 0o644)
			os.MkdirAll(filepath.Join(home, "Work", "scratch"), 0o755)
			s.click("sideOrgCfg")
			s.click("cfgOrg" + cfgFolders)
			id := orgsEntryID(s, 0)
			s.shot("orgs-settings-general",
				cut{"nav", dp(204, 58, 428, 320), true},
				cut{"langs", dp(446, 100, 1388, 194), false},
				cut{"zoom", dp(446, 198, 1388, 268), false},
				cut{"folder", dp(446, 308, 1388, 414), false}, // under the 72dp Text size row
			)
			s.click(id + "only_toml")
			s.shot("orgs-settings-only", cut{"preview", dp(446, 308, 1388, 574), false})
		}},
		scene{"orgs-settings-keys", func(s *shooter) {
			s.click("sideOrgCfg")
			s.click("cfgOrg" + cfgKeys)
			s.shot("orgs-settings-keys", cut{"nav", dp(204, 58, 428, 320), true})
			s.click("keyChangeboard.new")
			page := dp(446, 0, 1388, 0)
			r := s.at("keyChangeboard.new")
			s.shot("orgs-settings-keys-change", cut{"row", image.Rect(page.Min.X, r.Min.Y-int(8*shotScale), page.Max.X, r.Max.Y+int(8*shotScale)), true})
			s.key("Z", key.ModShortcut) // undo's key: the clash shows under the row
			r = s.at("keyChangeboard.new")
			s.shot("orgs-settings-keys-clash", cut{"row", image.Rect(page.Min.X, r.Min.Y-int(14*shotScale), page.Max.X, r.Max.Y+int(34*shotScale)), true})
		}},
		scene{"orgs-settings-org", func(s *shooter) {
			s.click("sideOrgCfg")
			s.click("cfgOrgAcme")
			add := around(union(s.at("cfgAddvault"), s.at("cfgAddnagios")), 8)
			orgsAway(s)
			s.shot("orgs-settings-org", cut{"add", add, true}, cut{"bar", dp(430, 868, 1400, 916), false})
			id := orgsEntryID(s, 1) // the Jira account
			s.click(id + "hd")
			orgsAway(s)
			s.shot("orgs-settings-account",
				cut{"token", dp(458, 386, 1376, 488), true})
		}},
		scene{"orgs-vault", func(s *shooter) {
			s.click("sideOrgCfg")
			s.click("cfgOrgGlobex")
			s.click("cfgAddvault")
			v := s.u.cfg.entries[len(s.u.cfg.entries)-1]
			v.ed("name").SetText("globex")
			v.ed("email").SetText("me@globex.example.com")
			orgsAway(s)
			s.shot("orgs-vault-form", cut{"card", dp(454, 168, 1380, 690), false})
			gh := orgsEntryID(s, 0) // the GitHub account
			s.u.cfg.list.ScrollTo(0)
			s.settle()
			s.click(gh + "hd")
			s.click(gh + ".src1") // Vault item
			s.click(gh + ".vglobex")
			s.u.cfg.entries[0].ed("..item").SetText("GitHub token") // the token's ("." secret) item
			orgsAway(s)
			s.shot("orgs-vault-item", cut{"token", dp(458, 322, 1376, 454), true})
		}},
		scene{"orgs-noaccount", func(s *shooter) {
			// a PR of an owner Initech has no GitHub account for: desk never asks
			url := "https://github.com/initech-labs/certs/pull/14"
			check := linkCheck
			linkCheck = func(ctx context.Context, l kanban.Link, cfg orgs.Config) (string, string, error) {
				if l.URL == url {
					return links.Check(ctx, l, cfg) // the real one: it stops at the missing account
				}
				return check(ctx, l, cfg)
			}
			id := orgsCard(s, "Rotate the staging TLS certificates")
			c := s.u.b.Card(id)
			c.Links = append(c.Links, kanban.Link{URL: url, Kind: links.KindGitHubPR, Ref: "PR #14"})
			s.u.save()
			go s.u.checkLinks([]string{id})
			s.wait("the link's check", 5*time.Second, func() bool { return s.u.b.Card(id).Links[0].State != "" })
			orgsAway(s)
			s.shot("orgs-noaccount", cut{"card", dp(250, 96, 570, 206), true})
		}},
		scene{"orgs-unlock", func(s *shooter) {
			v := orgs.Vault{Name: "globex", Kind: "bitwarden", Email: "me@globex.example.com"}
			s.u.vaultLocked(&vault.LockedError{Vault: v})
			s.settle()
			s.shot("orgs-unlock", cut{"dialog", dp(570, 120, 1030, 383), false})
			s.click("ulCancel")
			b := around(s.at("tbUnlock"), 8)
			orgsAway(s)
			s.shot("orgs-unlock-toolbar", cut{"button", b, true})
		}},
		scene{"orgs-unlock-code", func(s *shooter) {
			v := orgs.Vault{Name: "acme", Kind: "passbolt", URL: "https://passbolt.acme.example.com"}
			s.u.vaultLocked(&vault.LockedError{Vault: v, Code: true})
			s.settle()
			s.shot("orgs-unlock-code", cut{"dialog", dp(570, 120, 1030, 387), false})
		}},
		scene{"orgs-tickets", func(s *shooter) {
			orgsFetch(s)
			s.click("sidetk:Acme")
			row := around(s.at("tkhttps://acme.atlassian.net/browse/ABC-131"), 6)
			orgsAway(s)
			s.shot("orgs-tickets", cut{"rows", dp(500, 56, 1334, 216), false}, cut{"card", row, true})
			s.click("tkhttps://acme.atlassian.net/browse/ABC-123")
			s.shot("orgs-tickets-new", cut{"dialog", dp(456, 60, 1144, 420), false})
		}},
		scene{"orgs-ci", func(s *shooter) {
			orgsFetch(s)
			s.click("sideci:Acme")
			orgsAway(s)
			s.shot("orgs-ci", cut{"runs", dp(496, 56, 1340, 164), false})
			s.click("side") // All cards
			orgsAway(s)
			s.shot("orgs-ci-chip", cut{"cards", dp(928, 98, 1244, 420), true})
		}},
		scene{"orgs-alerts", func(s *shooter) {
			s.infra()
			orgsFetch(s)
			s.click("sideal:Globex")
			orgsAway(s)
			s.shot("orgs-alerts",
				cut{"row", dp(500, 80, 1334, 150), false},
				cut{"actions", dp(1166, 94, 1334, 136), true})
			s.u.snoozing = "Globex|web-02|HTTP"
			s.settle()
			s.shot("orgs-alerts-snooze", cut{"picker", dp(990, 144, 1332, 474), false})
			s.u.snoozing = ""
			s.u.snooze("Globex|web-02|HTTP", 4*time.Hour)
			s.settle()
			s.shot("orgs-alerts-snoozed", cut{"list", dp(500, 56, 1334, 300), false})
		}},
		scene{"orgs-investigate", func(s *shooter) {
			s.infra()
			orgsFetch(s)
			s.click("sideal:Globex")
			s.clickAt(f32.Pt(float32(1283*shotScale), float32(115*shotScale)), pointer.ButtonPrimary) // web-02's Investigate
			s.wait("the investigation's card", 5*time.Second, func() bool { return len(s.u.chat(s.u.sel)) > 1 })
			s.shot("orgs-investigate")
		}},
		scene{"orgs-cardtools", func(s *shooter) {
			id := orgsCard(s, "Fix the flaky checkout e2e test")
			url := "https://github.com/acme-corp/web/pull/604"
			s.u.sayMsg(id, kanban.ChatMsg{Who: "you", Text: "Why did CI fail on the PR?"})
			for _, line := range []string{
				demoTool("m1", "mcp__desk__ci", map[string]any{"url": url}, ""),
				demoTool("m2", "mcp__desk__ci_log", map[string]any{"url": url, "job": 23}, ""),
			} {
				for _, e := range claude.Parse([]byte(line)) {
					if e.Kind == claude.KindTool {
						s.u.sayMsg(id, toolMsg(e))
					}
				}
			}
			s.u.sayMsg(id, kanban.ChatMsg{Who: "claude", Text: "The **e2e (checkout)** job failed on PR #604: the payment iframe took 12 s to load and the test waits 10. I'll wait for the iframe's ready event instead of a fixed timeout."})
			s.u.openSession(id)
			s.settle()
			s.shot("orgs-cardtools", cut{"calls", dp(240, 596, 1596, 780), true})
		}},
	)
}
