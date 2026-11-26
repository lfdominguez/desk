package orgs

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// writeAt writes a fixture, its folders first (a .desk among them).
func writeAt(path string, b []byte, perm os.FileMode) error {
	os.MkdirAll(filepath.Dir(path), 0o755)
	return os.WriteFile(path, b, perm)
}

func TestLoadAndList(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"Globex", "_h5ai", ".git", "acme"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	writeAt(filepath.Join(root, "Globex", ".desk", "desk.toml"), []byte(`
[[github]]
owners = ["globex-org"]
token_env = "X_TOKEN"
[[jira]]
url = "https://a.atlassian.net"
projects = ["TB"]
[[jira]]
url = "https://b.atlassian.net"
token_cmd = "echo  s3cret "
[[linear]]
workspace = "globex"
teams = ["ENG"]
token_env = "LINEAR_API_KEY"
[linear.status]
Done = "In Review"
`), 0o644)
	if got := List(root); !slices.Equal(got, []string{"acme", "Globex"}) {
		t.Fatalf("orgs: %v", got)
	}
	c, err := Load(root, "Globex")
	if err != nil || len(c.Jira) != 2 || c.GitHub[0].TokenEnv != "X_TOKEN" || c.Jira[0].Projects[0] != "TB" {
		t.Fatalf("%+v %v", c, err)
	}
	if l := c.Linear; len(l) != 1 || l[0].Workspace != "globex" || l[0].Teams[0] != "ENG" || l[0].TokenEnv != "LINEAR_API_KEY" || l[0].Status["Done"] != "In Review" {
		t.Fatalf("%+v %v", c, err)
	}
	if tok, err := c.Jira[1].Get(); err != nil || tok != "s3cret" {
		t.Fatalf("token_cmd: %q %v", tok, err)
	}
	if c, err := Load(root, "acme"); err != nil || len(c.Jira) != 0 {
		t.Fatalf("missing file: %+v %v", c, err)
	}
	if d := Dir(root, "Globex", "../../etc"); d != filepath.Join(root, "Globex", "etc") {
		t.Fatalf("sub escaped the org: %s", d)
	}
}

// TestNested: a folder inside an org whose desk.toml names an org is one,
// its own config and folder, under the outer org's account.
func TestNested(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"Personal/billing", "Personal/notes", "Personal/.cache"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	writeAt(filepath.Join(root, "Personal/billing/.desk/desk.toml"), []byte("org = \"Billing\"\n[[jira]]\nurl = \"https://initech.atlassian.net\"\n"), 0o600)
	writeAt(filepath.Join(root, "Personal/.cache/.desk/desk.toml"), []byte(`org = "Hidden"`), 0o600)
	writeAt(filepath.Join(root, "Personal/notes/.desk/desk.toml"), []byte("[[jira]]\nname = \"not the org\"\n"), 0o600)
	if got := List(root); !slices.Equal(got, []string{"Billing", "notes", "Personal"}) {
		t.Fatalf("orgs: %v", got)
	}
	if d := Dir(root, "Billing", "api"); d != filepath.Join(root, "Personal/billing/api") {
		t.Fatalf("dir %s", d)
	}
	if a := Account(root, "Billing"); a != "Personal" {
		t.Fatalf("account %s", a)
	}
	if c, err := Load(root, "Billing"); err != nil || len(c.Jira) != 1 {
		t.Fatalf("%+v %v", c, err)
	}
	if c, _ := Load(root, "Personal"); len(c.Jira) != 0 {
		t.Fatal("the outer org got the nested one's accounts")
	}
	in := func(name string) string { return filepath.Join(root, name) }
	if w := Walls(root, in("Personal/billing/api")); !slices.Equal(w, []string{in("Personal/notes")}) {
		t.Errorf("a nested org sees the one around it, not its sibling: %v", w)
	}
	if w := Walls(root, in("Personal")); !slices.Equal(w, []string{in("Personal/billing"), in("Personal/notes")}) {
		t.Errorf("the outer org doesn't see the ones nested in it: %v", w)
	}
}

// TestFolders: desk's settings say where to search: a folder that is an
// organization itself, how deep; desk's root is then just a name.
func TestFolders(t *testing.T) {
	work, clients := t.TempDir(), t.TempDir()
	t.Setenv("DESK_ROOT", work)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, d := range []string{"Acme/a/b", "Initech"} {
		os.MkdirAll(filepath.Join(work, d), 0o755)
	}
	writeAt(filepath.Join(work, "Acme/a/b/.desk/desk.toml"), []byte(`org = "Deep"`), 0o600)
	os.MkdirAll(filepath.Join(clients, "Globex"), 0o755)
	writeAt(filepath.Join(clients, ".desk", "desk.toml"), []byte(`org = "Clients"`), 0o600)
	if got := List(work); !slices.Equal(got, []string{"Acme", "Initech"}) {
		t.Fatalf("default, two levels: %v", got)
	}
	if err := SaveFolders([]Folder{{Path: work, Depth: 3}, {Path: clients, Root: true, Depth: 1}}); err != nil {
		t.Fatal(err)
	}
	if got := List(work); !slices.Equal(got, []string{"Acme", "Clients", "Deep", "Globex", "Initech"}) {
		t.Fatalf("folders: %v", got)
	}
	for org, want := range map[string][2]string{"Deep": {"Acme/a/b", "Acme"}, "Clients": {"", "Clients"}, "Globex": {"Globex", "Globex"}} {
		base := work
		if org != "Deep" {
			base = clients
		}
		if d, a := Dir(work, org, ""), Account(work, org); d != filepath.Join(base, want[0]) || a != want[1] {
			t.Errorf("%s: dir %s account %s", org, d, a)
		}
	}
	if a := AccountAt(work, t.TempDir()); a != "" {
		t.Errorf("outside the folders: %q", a)
	}
	// Account is remembered: a settings change reaches it once saved
	// through SaveFolders, or once List looks again.
	// a literal string: Windows' backslashes aren't escapes
	os.WriteFile(SettingsFile(), []byte("[[folder]]\npath = '"+clients+"'\ndepth = 1\n"), 0o644)
	if a := Account(work, "Deep"); a != "Acme" {
		t.Fatalf("remembered: %q", a)
	}
	List(work)
	if a := Account(work, "Deep"); a != "" {
		t.Fatalf("after List: %q", a)
	}
	SaveFolders([]Folder{{Path: work, Depth: 3}})
	if a := Account(work, "Deep"); a != "Deep" { // work/Deep: where List, which didn't see it, puts it
		t.Fatalf("after SaveFolders: %q", a)
	}
	List(work)
	if a := Account(work, "Deep"); a != "Acme" {
		t.Fatalf("after SaveFolders: %q", a)
	}
}

// TestScanOnlyToml: with only_toml a folder's subfolders without a
// desk.toml are skipped (and said), deeper ones still found; Scan says
// which folder found each, and a name found twice is the first folder's.
func TestScanOnlyToml(t *testing.T) {
	work, other := t.TempDir(), t.TempDir()
	for _, d := range []string{"Acme/x/deep", "Globex", ".hidden", "Initech"} {
		os.MkdirAll(filepath.Join(work, d), 0o755)
	}
	writeAt(filepath.Join(work, "Acme/.desk/desk.toml"), []byte(`org = "Named"`), 0o600)
	writeAt(filepath.Join(work, "Acme/x/deep/.desk/desk.toml"), nil, 0o600)
	os.WriteFile(filepath.Join(work, "loose.txt"), nil, 0o600)
	os.MkdirAll(filepath.Join(other, "Acme"), 0o755)
	os.MkdirAll(filepath.Join(other, "Umbrella"), 0o755)
	found, skipped := Scan([]Folder{{Path: work, Depth: 3, OnlyToml: true}, {Path: other, Depth: 1}})
	var got []string
	for _, f := range found {
		got = append(got, fmt.Sprint(f.Name, f.Folder))
	}
	if !slices.Equal(got, []string{"Acme0", "deep0", "Umbrella1"}) || !slices.Equal(skipped, []string{"Globex", "Initech"}) {
		t.Fatalf("found %v, skipped %v", got, skipped)
	}
	if found[0].Dir != filepath.Join(work, "Acme") {
		t.Fatalf("dir %s", found[0].Dir)
	}
	t.Setenv("DESK_ROOT", work)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	SaveFolders([]Folder{{Path: work, Depth: 1, OnlyToml: true}})
	if l := List(work); !slices.Equal(l, []string{"Acme"}) || !slices.Equal(Skipped(work), []string{"Globex", "Initech"}) {
		t.Fatalf("list %v, skipped %v", l, Skipped(work))
	}
}

// TestSaveRaw: what the settings dialog writes reads back as a Config, a
// vault's secrets as sub-tables, keys it doesn't know kept.
func TestSaveRaw(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "Globex"), 0o755)
	raw, err := LoadRaw(root, "Globex")
	if err != nil || len(raw) != 0 {
		t.Fatalf("missing file: %v %v", raw, err)
	}
	raw["vault"] = []map[string]any{{"name": "globex", "kind": "bitwarden", "email": "me@b.com", "client_id": "user.1",
		"client_secret": map[string]any{"token_env": "BW_SECRET"}, "future": "kept"}}
	raw["github"] = []map[string]any{{"owners": []string{"globex-org"}, "vault": "globex", "item": "GitHub token"}}
	if err := SaveRaw(root, "Globex", raw); err != nil {
		t.Fatal(err)
	}
	c, err := Load(root, "Globex")
	if err != nil || c.Vaults[0].ClientSecret.TokenEnv != "BW_SECRET" || c.GitHub[0].Vault != "globex" || c.GitHub[0].Item != "GitHub token" || c.GitHub[0].Owners[0] != "globex-org" {
		t.Fatalf("%+v %v", c, err)
	}
	if back, _ := LoadRaw(root, "Globex"); back["vault"].([]map[string]any)[0]["future"] != "kept" {
		t.Fatalf("an unknown key was dropped: %v", back["vault"])
	}
	if fi, _ := os.Stat(filepath.Join(root, "Globex", ".desk", "desk.toml")); fi.Mode().Perm() != 0o600 && runtime.GOOS != "windows" { // no mode bits there
		t.Fatalf("mode %v", fi.Mode())
	}
}
