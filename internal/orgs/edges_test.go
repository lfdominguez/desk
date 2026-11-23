package orgs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSecretGet: token_env, then token_cmd, then the plain token.
func TestSecretGet(t *testing.T) {
	t.Setenv("DESK_TEST_TOKEN", "from-env")
	t.Setenv("DESK_TEST_EMPTY", "")
	for _, tc := range []struct {
		name string
		s    Secret
		want string
		err  string
	}{
		{"env", Secret{TokenEnv: "DESK_TEST_TOKEN", TokenCmd: "echo cmd", Token: "plain"}, "from-env", ""},
		{"env unset", Secret{TokenEnv: "DESK_TEST_EMPTY", Token: "plain"}, "", "$DESK_TEST_EMPTY is not set"},
		{"cmd", Secret{TokenCmd: "echo   from-cmd  ", Token: "plain"}, "from-cmd", ""}, // sh's echo drops the spaces, cmd's keeps them
		{"cmd fails", Secret{TokenCmd: "exit 3"}, "", "token_cmd: "},
		{"plain", Secret{Token: "plain"}, "plain", ""},
		{"none", Secret{}, "", ""},
	} {
		got, err := tc.s.Get()
		if got != tc.want || (tc.err == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.err)) {
			t.Errorf("%s: %q, %v", tc.name, got, err)
		}
	}
}

// TestSaveFoldersErrors: an unwritable settings folder or temp file fails.
func TestSaveFoldersErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	os.WriteFile(file, nil, 0o644)
	t.Setenv("XDG_CONFIG_HOME", file)
	if err := SaveFolders([]Folder{{Path: "/x"}}); err == nil {
		t.Error("settings folder under a file")
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
	os.MkdirAll(SettingsFile()+".tmp", 0o755)
	if err := SaveFolders([]Folder{{Path: "/x"}}); err == nil {
		t.Error("temp path taken by a directory")
	}
}

// TestLangs: English until set; saving the folders keeps the languages and
// saving the languages keeps the folders.
func TestLangs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DESK_ROOT", t.TempDir())
	if c, s := Langs(); c != "English" || s != "English" {
		t.Fatalf("defaults %q %q", c, s)
	}
	if err := SaveLangs("Spanish", "French"); err != nil {
		t.Fatal(err)
	}
	if err := SaveFolders([]Folder{{Path: "/x", Depth: 1}}); err != nil {
		t.Fatal(err)
	}
	if c, s := Langs(); c != "Spanish" || s != "French" {
		t.Fatalf("after the folders: %q %q", c, s)
	}
	if err := SaveLangs("", "German"); err != nil {
		t.Fatal(err)
	}
	if c, s := Langs(); c != "English" || s != "German" || Folders(Root())[0].Path != "/x" {
		t.Fatalf("after the languages: %q %q %v", c, s, Folders(Root()))
	}
}

// TestExpand: only a leading ~ alone or before a slash is the home folder.
func TestExpand(t *testing.T) {
	t.Setenv("HOME", "/home/acme")
	t.Setenv("USERPROFILE", "/home/acme")
	for in, want := range map[string]string{"~": filepath.FromSlash("/home/acme"), "~/Work": filepath.FromSlash("/home/acme/Work"), "~globex/x": "~globex/x", "/a/~": "/a/~"} {
		if got := Expand(in); got != want {
			t.Errorf("Expand(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDirUnknown: an org List didn't find still has root/org; no org is root.
func TestDirUnknown(t *testing.T) {
	root := t.TempDir()
	if got := Dir(root, "Initech", "src"); got != filepath.Join(root, "Initech", "src") {
		t.Errorf("Dir = %q", got)
	}
	if got := Dir(root, "", "src"); got != root {
		t.Errorf("Dir without org = %q", got)
	}
}

// TestAccountAtSearchFolder: the search folder itself is no organization
// without root and a desk.toml.
func TestAccountAtSearchFolder(t *testing.T) {
	root := t.TempDir()
	writeAt(filepath.Join(root, ".desk", "desk.toml"), []byte(`org = "Acme"`), 0o644)
	if got := AccountAt(root, root); got != "" {
		t.Errorf("AccountAt(root) = %q, want none", got)
	}
	if got := AccountAt(root, filepath.Dir(root)); got != "" {
		t.Errorf("AccountAt(parent) = %q, want none", got)
	}
	if got := List(root); len(got) != 0 {
		t.Errorf("List = %v, want none", got)
	}
}

// TestFoldersEmptySettings: settings without folders search the default.
func TestFoldersEmptySettings(t *testing.T) {
	work := t.TempDir()
	t.Setenv("DESK_ROOT", work)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	os.MkdirAll(filepath.Dir(SettingsFile()), 0o755)
	os.WriteFile(SettingsFile(), []byte("# no folders\n"), 0o644)
	if got := Folders(work); len(got) != 1 || got[0] != (Folder{Path: work, Depth: 2}) {
		t.Errorf("Folders = %v", got)
	}
}

// TestLoadEdges: no org is an empty config; a broken desk.toml names the file.
func TestLoadEdges(t *testing.T) {
	root := t.TempDir()
	if c, err := Load(root, ""); err != nil || c.Agent != "" {
		t.Fatalf("no org: %+v %v", c, err)
	}
	os.MkdirAll(filepath.Join(root, "Globex"), 0o755)
	writeAt(filepath.Join(root, "Globex", ".desk", "desk.toml"), []byte("agent = "), 0o644)
	if _, err := Load(root, "Globex"); err == nil || !strings.Contains(err.Error(), "desk.toml") {
		t.Fatalf("broken file: %v", err)
	}
}

// TestSaveRawErrors: a value TOML can't hold, a table that wouldn't read
// back as a Config, or a missing org folder fail without writing.
func TestSaveRawErrors(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "Acme"), 0o755)
	if err := SaveRaw(root, "Acme", map[string]any{"x": make(chan int)}); err == nil {
		t.Error("channel encoded")
	}
	if err := SaveRaw(root, "Acme", map[string]any{"github": "nope"}); err == nil || !strings.Contains(err.Error(), "would not read back") {
		t.Errorf("bad shape: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Acme", ".desk", "desk.toml")); err == nil {
		t.Error("failed SaveRaw wrote the file")
	}
	if err := SaveRaw(root, "Missing", map[string]any{"agent": "a"}); err == nil {
		t.Error("missing org folder")
	}
}

// TestXDGDir: the variable, else under HOME (on macOS too); no HOME is an error.
func TestXDGDir(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/x/cache")
	if d, err := XDGDir("XDG_CACHE_HOME", ".cache"); d != "/x/cache" || err != nil {
		t.Errorf("set: %q %v", d, err)
	}
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "/home/me")
	t.Setenv("USERPROFILE", "/home/me") // Windows' home
	if d, err := XDGDir("XDG_CACHE_HOME", ".cache"); d != filepath.FromSlash("/home/me/.cache") || err != nil {
		t.Errorf("unset: %q %v", d, err)
	}
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Chdir(t.TempDir())
	if d, err := XDGDir("XDG_CACHE_HOME", ".cache"); d != "" || err == nil {
		t.Errorf("no home: %q %v", d, err)
	}
}

// TestUserMCP: off until set, kept with the rest of the settings.
func TestUserMCP(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DESK_ROOT", t.TempDir())
	if UserMCP() {
		t.Fatal("on by default")
	}
	SaveLangs("Spanish", "")
	if err := SaveUserMCP(true); err != nil || !UserMCP() {
		t.Fatalf("saved: %v %v", err, UserMCP())
	}
	if c, _ := Langs(); c != "Spanish" {
		t.Fatalf("the languages went: %q", c)
	}
}
