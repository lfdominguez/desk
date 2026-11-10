package orgs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestKeys: the rebound shortcuts round-trip through settings.toml under
// [keys], the other settings keep them, and they keep the other settings.
func TestKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if len(Keys()) != 0 {
		t.Fatal("unset")
	}
	SaveLangs("Spanish", "French")
	if err := SaveKeys(map[string]string{"board.new": "Alt+B", "zoom.in": "Ctrl++"}); err != nil {
		t.Fatal(err)
	}
	if k := Keys(); len(k) != 2 || k["board.new"] != "Alt+B" || k["zoom.in"] != "Ctrl++" {
		t.Errorf("Keys = %v", k)
	}
	if c, _ := Langs(); c != "Spanish" {
		t.Error("SaveKeys lost the languages")
	}
	SaveFolders([]Folder{{Path: "/home/me/Work", Depth: 2}})
	if Keys()["board.new"] != "Alt+B" {
		t.Error("SaveFolders lost the keys")
	}
	b, _ := os.ReadFile(SettingsFile())
	if want := "[keys]"; !strings.Contains(string(b), want) {
		t.Errorf("no %s table:\n%s", want, b)
	}
	SaveKeys(nil)
	if b, _ := os.ReadFile(SettingsFile()); strings.Contains(string(b), "[keys]") {
		t.Errorf("an empty [keys] written:\n%s", b)
	}
}

// TestKeysBrokenFile: a settings file that doesn't parse isn't rewritten.
func TestKeysBrokenFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	file := filepath.Join(dir, "desk", "settings.toml")
	os.MkdirAll(filepath.Dir(file), 0o755)
	broken := "[[folder]]\npath = \n"
	os.WriteFile(file, []byte(broken), 0o644)
	if err := SaveKeys(map[string]string{"board.new": "Alt+B"}); err == nil {
		t.Error("no error")
	}
	if b, _ := os.ReadFile(file); string(b) != broken {
		t.Errorf("rewritten: %q", b)
	}
}
