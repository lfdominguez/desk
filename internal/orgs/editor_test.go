package orgs

import (
	"os"
	"strings"
	"testing"
)

// TestEditorSettings: unset is the zero Editor (and isn't written); SetEditor
// keeps it beside the other settings, which keep it in turn.
func TestEditorSettings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if e := EditorSettings(); e != (Editor{}) {
		t.Fatalf("unset: %+v", e)
	}
	SaveLangs("Spanish", "English")
	if err := SetEditor(Editor{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(SettingsFile()); strings.Contains(string(b), "editor") {
		t.Fatalf("the default written:\n%s", b)
	}
	want := Editor{Tree: 300, Chat: 520, Wrap: true}
	if err := SetEditor(want); err != nil {
		t.Fatal(err)
	}
	if e, card := EditorSettings(), loadSettings().CardLang; e != want || card != "Spanish" {
		t.Fatalf("%+v, card language %q", e, card)
	}
	SaveLangs("English", "English")
	if e := EditorSettings(); e != want {
		t.Fatalf("SaveLangs lost it: %+v", e)
	}
}

// TestSetEditorBrokenFile: a settings file that doesn't parse is left as it is.
func TestSetEditorBrokenFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	SaveLangs("Spanish", "English")
	p := SettingsFile()
	os.WriteFile(p, []byte("folder = [broken"), 0o644)
	if err := SetEditor(Editor{Wrap: true}); err == nil {
		t.Fatal("no error")
	}
	if b, _ := os.ReadFile(p); string(b) != "folder = [broken" {
		t.Fatalf("rewritten: %s", b)
	}
}
