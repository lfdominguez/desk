package ui

import (
	"os"
	"strings"
	"testing"

	"github.com/lfdominguez/desk/internal/orgs"
)

// TestConfigKeepLogin: an org page's "Show the server's login message"
// reads clear_login = false, counts as a change when flipped, and Save
// writes clear_login = false (on) or drops the key (off), the other keys kept.
func TestConfigKeepLogin(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"record = false\n", "clear_login = false"},   // off → on
		{"record = false\nclear_login = false\n", ""}, // on → off: the key goes
		{"record = false\nclear_login = true\n", "clear_login = false"},
	} {
		h := newHarness(t)
		h.orgToml("Acme", tc.file)
		h.u.openConfig("Acme")
		h.u.cfg.set.sub["Acme"] = "servers"
		h.frame()
		was := h.u.cfg.opts.on["keep_login"]
		if was != strings.Contains(tc.file, "clear_login = false") {
			t.Fatalf("%q: read as %v", tc.file, was)
		}
		if h.u.cfg.changes() != 0 {
			t.Fatalf("%q: changed on open", tc.file)
		}
		h.clickButton("cfgKeepLogin")
		if h.u.cfg.opts.on["keep_login"] == was || h.u.cfg.changes() != 1 {
			t.Fatalf("%q: flip: %v, %d changes", tc.file, h.u.cfg.opts.on["keep_login"], h.u.cfg.changes())
		}
		h.u.saveConfig()
		b, _ := os.ReadFile(orgs.File(h.u.root, "Acme"))
		got := string(b)
		if !strings.Contains(got, "record = false") {
			t.Errorf("%q: record lost:\n%s", tc.file, got)
		}
		if tc.want == "" && strings.Contains(got, "clear_login") || tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("%q: saved\n%s", tc.file, got)
		}
		if c, err := orgs.Load(h.u.root, "Acme"); err != nil || (c.ClearLogin != nil && !*c.ClearLogin) == was {
			t.Errorf("%q: reads back %v %v", tc.file, c.ClearLogin, err)
		}
	}
}
