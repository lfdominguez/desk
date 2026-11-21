package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lfdominguez/desk/internal/infra"
)

// TestConfigAsbru: an org page's Servers part says where its servers file
// goes; Import from Ásbrú lists Ásbrú's groups, and one adds its hosts (the
// file made, the line read again); importing again adds nothing; no Ásbrú
// file, or a broken one, says so.
func TestConfigAsbru(t *testing.T) {
	h := newHarness(t)
	h.orgToml("Acme", "")
	src := filepath.Join(t.TempDir(), "asbru.yml")
	f0 := asbruFile
	asbruFile = func() string { return src }
	defer func() { asbruFile = f0 }()
	h.u.openConfig("Acme")
	h.u.cfg.set.sub["Acme"] = "servers"
	h.frame()
	c0 := asbruCurrent
	asbruCurrent = func(f string) error {
		if f != src {
			t.Errorf("checked %q", f)
		}
		return infra.ErrAsbruOpen
	}
	h.clickButton("cfgAsbru")
	if h.u.toast != infra.ErrAsbruOpen.Error() || !h.u.toastBad || h.u.cfg.asbru != nil {
		t.Fatalf("Ásbrú open: %q", h.u.toast)
	}
	asbruCurrent = c0
	if !strings.HasPrefix(h.u.cfg.srvFile, "no servers file yet: an import makes ") {
		t.Fatalf("line %q", h.u.cfg.srvFile)
	}
	h.clickButton("cfgAsbru")
	if !strings.HasPrefix(h.u.toast, "No Ásbrú connections: ") || !h.u.toastBad || h.u.cfg.asbru != nil || !strings.HasPrefix(h.u.cfg.asbruHint(), "SSH connections") {
		t.Fatalf("no file: %q", h.u.toast)
	}
	os.WriteFile(src, []byte("- a\n"), 0o644)
	h.clickButton("cfgAsbru")
	h.u.toastBad = false
	h.clickButton("cfgAsbru")
	if !strings.HasPrefix(h.u.toast, "not Ásbrú's YAML") || !h.u.toastBad || h.u.cfg.asbru != nil {
		t.Fatalf("broken: %q", h.u.toast)
	}
	os.WriteFile(src, []byte(`environments:
  g: {_is_group: 1, name: Prod, parent: __PAC__ROOT__}
  a: {name: web-01, ip: 192.0.2.11, method: SSH, parent: g}
  b: {name: win-01, ip: 192.0.2.12, method: VNC, parent: g}
  c: {name: lab, ip: 192.0.2.13, method: SSH}
`), 0o644)
	h.clickButton("cfgAsbru")
	if len(h.u.cfg.asbru) != 1 || h.u.cfg.asbru[0] != "Prod" || !strings.HasPrefix(h.u.cfg.asbruHint(), "Which of Ásbrú's groups are Acme's?") {
		t.Fatalf("groups %q", h.u.cfg.asbru)
	}
	h.clickButton("cfgAsbruNo")
	if h.u.cfg.asbru != nil {
		t.Fatal("cancel")
	}
	h.clickButton("cfgAsbru")
	h.clickButton("cfgAsbru:Prod")
	file := filepath.Join(h.u.root, "Acme", ".desk", "ssh_config")
	b, _ := os.ReadFile(file)
	if !strings.Contains(string(b), "Host web-01") || strings.Contains(string(b), "lab") || h.u.cfg.asbru != nil || h.u.toastBad ||
		!strings.HasPrefix(h.u.toast, "Added 1 servers to ") || !strings.HasSuffix(h.u.toast, "(1 skipped: Prod/win-01: VNC, not SSH)") {
		t.Fatalf("import: %q\n%s", h.u.toast, b)
	}
	h.frame()
	if strings.HasPrefix(h.u.cfg.srvFile, "no servers") {
		t.Fatalf("read again: %q", h.u.cfg.srvFile)
	}
	h.clickButton("cfgAsbru")
	h.clickButton("cfgAsbru:Prod")
	if h.u.toast != "Nothing to import from Ásbrú (2 skipped)" || !h.u.toastBad {
		t.Fatalf("again: %q", h.u.toast)
	}
	h.clickButton("cfgAsbru")
	h.clickButton("cfgAsbruAll")
	if !strings.HasPrefix(h.u.toast, "Added 1 servers") {
		t.Fatalf("all: %q", h.u.toast)
	}
	os.Remove(src)
	h.u.importAsbru("Acme", "")
	if !strings.HasPrefix(h.u.toast, "Ásbrú import: ") || !h.u.toastBad {
		t.Fatalf("gone: %q", h.u.toast)
	}
}
