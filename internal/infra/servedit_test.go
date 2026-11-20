package infra

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// serversOrg is an organization folder with servers file text ("" none).
func serversOrg(t *testing.T, text string) (org, file string) {
	t.Helper()
	org = filepath.Join(t.TempDir(), "Acme")
	file = filepath.Join(org, ".desk", "ssh_config")
	os.MkdirAll(filepath.Dir(file), 0o700)
	if text != "" {
		os.WriteFile(file, []byte(text), 0o644)
	}
	return org, file
}

const someServers = `#@ vault acme
Host gw
    HostName 192.0.2.1

#@ group eu/web
Host web1
    HostName 192.0.2.10
    User deploy
    IdentityFile ~/.ssh/old
    IdentityFile ~/.ssh/older
    #@ desc the proxy

Host web2 web3
    HostName 192.0.2.11

#@ group db
Host db1
    HostName 192.0.2.20
`

func change(t *testing.T, org, tool string, a ServerArgs) string {
	t.Helper()
	_, _, next, err := serverChange(org, tool, a)
	if err != nil {
		t.Fatalf("%s %s: %v", tool, a.Name, err)
	}
	return next
}

// TestAddServer: a new server goes at the end of its group's section, a new
// group at the end, no group before the first section; its key comes with
// IdentitiesOnly; a missing file is made.
func TestAddServer(t *testing.T) {
	org, _ := serversOrg(t, someServers)
	next := change(t, org, "add_server", ServerArgs{Name: "web4", HostName: "192.0.2.12", User: "admin", Port: 2222,
		IdentityFile: "~/.ssh/acme", Group: "/eu/web/", Desc: "new one", Options: map[string]string{"ProxyJump": "gw", "#@ tags": "web"}})
	want := "Host web2 web3\n    HostName 192.0.2.11\n\nHost web4\n    HostName 192.0.2.12\n    User admin\n    Port 2222\n" +
		"    IdentityFile ~/.ssh/acme\n    IdentitiesOnly yes\n    #@ desc new one\n    #@ tags web\n    ProxyJump gw\n\n#@ group db\n"
	if !strings.Contains(next, want) {
		t.Errorf("in group:\n%s", next)
	}
	if next := change(t, org, "add_server", ServerArgs{Name: "cache", Group: "eu/cache"}); !strings.HasSuffix(next, "HostName 192.0.2.20\n\n#@ group eu/cache\nHost cache\n") {
		t.Errorf("new group:\n%s", next)
	}
	if next := change(t, org, "add_server", ServerArgs{Name: "solo", IdentityFile: "k", Options: map[string]string{"identitiesonly": "no"}}); !strings.Contains(next, "HostName 192.0.2.1\n\nHost solo\n    IdentityFile k\n    identitiesonly no\n\n#@ group eu/web") {
		t.Errorf("no group:\n%s", next)
	}
	org2, file := serversOrg(t, "")
	text, err := ServerApply(org2, "add_server", json.RawMessage(`{"name":"a","hostname":"192.0.2.5"}`))
	if b, _ := os.ReadFile(file); err != nil || string(b) != "Host a\n    HostName 192.0.2.5\n" || !strings.Contains(text, "added a to "+file) {
		t.Errorf("new file: %q %v %q", b, err, text)
	}
	if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	org3, _ := serversOrg(t, "Host a\n")
	if next := change(t, org3, "add_server", ServerArgs{Name: "b"}); next != "Host a\n\nHost b\n" {
		t.Errorf("no groups: %q", next)
	}
}

// TestEditServer: what's given replaces every line of its keyword where
// the first was, "" removes, new ones go at the block's end, group moves it.
func TestEditServer(t *testing.T) {
	org, _ := serversOrg(t, someServers)
	next := change(t, org, "edit_server", ServerArgs{Name: "WEB1", User: "root", IdentityFile: "~/.ssh/new",
		Options: map[string]string{"#@ desc": "", "Port": "2200"}})
	if !strings.Contains(next, "Host web1\n    HostName 192.0.2.10\n    User root\n    IdentityFile ~/.ssh/new\n    IdentitiesOnly yes\n    Port 2200\n\nHost web2") {
		t.Errorf("edit:\n%s", next)
	}
	next = change(t, org, "edit_server", ServerArgs{Name: "db1", Group: "eu/web"})
	if !strings.Contains(next, "HostName 192.0.2.11\n\nHost db1\n    HostName 192.0.2.20\n\n#@ group db\n") {
		t.Errorf("move:\n%s", next)
	}
	if next := change(t, org, "edit_server", ServerArgs{Name: "web1", Group: "eu/web", Desc: "x"}); !strings.Contains(next, "#@ desc x\n\nHost web2") {
		t.Errorf("same group:\n%s", next)
	}
}

// TestRemoveServer: its block goes, or only its name from a shared Host line.
func TestRemoveServer(t *testing.T) {
	org, _ := serversOrg(t, someServers)
	if next := change(t, org, "remove_server", ServerArgs{Name: "web1"}); !strings.Contains(next, "#@ group eu/web\nHost web2 web3") {
		t.Errorf("remove:\n%s", next)
	}
	if next := change(t, org, "remove_server", ServerArgs{Name: "web3"}); !strings.Contains(next, "\nHost web2\n") {
		t.Errorf("shared:\n%s", next)
	}
}

// TestServerChangeRefused: what can't be done says why.
func TestServerChangeRefused(t *testing.T) {
	org, file := serversOrg(t, someServers+"Host pat*\n#@ hosts-cmd list.sh\nInclude servers-dynamic.conf\n")
	os.WriteFile(filepath.Join(org, ".desk", "servers-dynamic.conf"), []byte("Host dyn\n"), 0o644)
	for _, c := range []struct {
		tool string
		a    ServerArgs
		want string
	}{
		{"add_server", ServerArgs{Name: "bad name"}, "letters, digits"},
		{"add_server", ServerArgs{Name: "x", Group: "a\nb"}, "group: one line"},
		{"add_server", ServerArgs{Name: "x", Port: 70000}, "port 70000"},
		{"add_server", ServerArgs{Name: "x", Options: map[string]string{"#@ bogus": "1"}}, "desk's keys are"},
		{"add_server", ServerArgs{Name: "x", Options: map[string]string{"Bogus": "1"}}, "not an ssh_config keyword"},
		{"edit_server", ServerArgs{Name: "web1", Options: map[string]string{"#@ Password-Raw": "guess"}}, "the user's to write"},
		{"add_server", ServerArgs{Name: "x", Options: map[string]string{"Host": "y"}}, "not an ssh_config keyword"},
		{"add_server", ServerArgs{Name: "x", Options: map[string]string{"Protocol": "2"}}, "not an ssh_config keyword"},
		{"add_server", ServerArgs{Name: "x", User: "a\nb"}, "User: one line"},
		{"add_server", ServerArgs{Name: "Web1"}, "already a server"},
		{"edit_server", ServerArgs{Name: "nope", User: "a"}, "no server nope"},
		{"edit_server", ServerArgs{Name: "dyn", User: "a"}, "hosts-cmd"},
		{"edit_server", ServerArgs{Name: "web1"}, "nothing to change"},
		{"edit_server", ServerArgs{Name: "web1", User: "deploy"}, "already is that way"},
	} {
		if _, _, _, err := serverChange(org, c.tool, c.a); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %+v: %v, want %q", c.tool, c.a, err, c.want)
		}
	}
	os.Remove(file)
	os.Mkdir(file, 0o700) // unreadable as a file
	if _, _, _, err := serverChange(org, "add_server", ServerArgs{Name: "x"}); err == nil {
		t.Error("an unreadable file adds")
	}
	if _, err := ServerApply(org, "add_server", json.RawMessage(`[`)); err == nil {
		t.Error("bad arguments apply")
	}
	if _, _, err := ServerPreview(org, "add_server", json.RawMessage(`[`)); err == nil {
		t.Error("bad arguments preview")
	}
	if _, _, err := ServerPreview(org, "add_server", json.RawMessage(`{"name":"-"}`)); err == nil {
		t.Error("bad name previews")
	}
	if _, err := ServerApply(org, "add_server", json.RawMessage(`{"name":"-"}`)); err == nil {
		t.Error("bad name applies")
	}
	if _, err := ServersText(org); err == nil {
		t.Error("an unreadable file reads")
	}
}

// TestServerChangeOdd: a pattern's Host block is no server and stays; an
// unwritable file says so.
func TestServerChangeOdd(t *testing.T) {
	org, file := serversOrg(t, "Host *.example.com\n    User x\nHost a\n#@ group g\nHost=c\n")
	if _, _, _, err := serverChange(org, "edit_server", ServerArgs{Name: "a", User: "b"}); err != nil {
		t.Fatal(err)
	}
	if next := change(t, org, "add_server", ServerArgs{Name: "b"}); !strings.Contains(next, "Host a\n\nHost b\n\n#@ group g\n") {
		t.Errorf("before a group right under a host:\n%s", next)
	}
	if _, _, _, err := serverChange(org, "edit_server", ServerArgs{Name: "c", User: "b"}); err == nil || !strings.Contains(err.Error(), "by hand") {
		t.Errorf("Host=c: %v", err)
	}
	if err := writeServers(filepath.Join(file, "x", "y"), ""); err == nil {
		t.Error("written under a file")
	}
	os.WriteFile(file, []byte("Host a\n"), 0o644)
	os.Chmod(filepath.Dir(file), 0o500)
	t.Cleanup(func() { os.Chmod(filepath.Dir(file), 0o700) })
	if os.WriteFile(filepath.Join(filepath.Dir(file), "probe"), nil, 0o600) == nil {
		t.Skip("running as root: folders don't refuse writes")
	}
	os.Chmod(file, 0o400)
	if _, err := ServerApply(org, "edit_server", json.RawMessage(`{"name":"a","user":"b"}`)); err == nil {
		t.Error("an unwritable file applies")
	}
}

// TestServersText: where the file is and what it holds, or where it goes.
func TestServersText(t *testing.T) {
	org, file := serversOrg(t, "")
	if s, err := ServersText(org); err != nil || s != "no servers file yet: add_server makes "+file {
		t.Errorf("%q %v", s, err)
	}
	os.WriteFile(file, []byte("#@ password-raw top secret\nHost a\n    #@password-raw \"s3 cr3t\"\n"), 0o644)
	if s, _ := ServersText(org); s != file+":\n#@ password-raw [hidden]\nHost a\n    #@password-raw [hidden]\n" {
		t.Errorf("%q", s)
	}
	if p, d, err := ServerPreview(org, "edit_server", json.RawMessage(`{"name":"a","user":"me"}`)); err != nil || p != file || !strings.Contains(d, "+    User me") || strings.Contains(d, "cr3t") {
		t.Errorf("preview %q %q %v", p, d, err)
	}
}

// TestCardServerTools: a card's Claude has the server tools, and only
// servers runs without asking; the infra Claude has them too.
func TestCardServerTools(t *testing.T) {
	t.Setenv("DESK_SELF", "/bin/desk")
	_, allow := CardMCP("/s.sock", "c1")
	if !slices.Contains(allow, "mcp__desk__servers") || slices.Contains(allow, "mcp__desk__add_server") || slices.Contains(allow, "mcp__desk__remove_server") {
		t.Errorf("allow %v", allow)
	}
	var names []string
	for _, tl := range append(slices.Clone(cardTools), mcpTools...) {
		names = append(names, tl["name"].(string))
	}
	for _, n := range []string{"servers", "add_server", "edit_server", "remove_server"} {
		if c := strings.Count(strings.Join(names, " ")+" ", n+" "); c < 2 {
			t.Errorf("%s offered %d times", n, c)
		}
	}
	if toolLine("mcp__desk__edit_server", "", "", "", "web1", 0) != "changing server web1" || toolLine("mcp__desk__servers", "", "", "", "", 0) != "reading the servers file" {
		t.Error("tool lines")
	}
}

// TestTabServerTool: the infra Claude's change waits for the approval sheet's
// diff, is audited, and reloads; denied, or changed meanwhile, it isn't written.
func TestTabServerTool(t *testing.T) {
	stateC(t)
	org, file := serversOrg(t, "Host a\n    #@ password-raw pw\n")
	ok, reloads := true, 0
	var seen []*approval
	var during func()
	setDispatchA(t, func(msg tea.Msg) {
		switch m := msg.(type) {
		case approvalMsg:
			seen = append(seen, m.a)
			if during != nil {
				during()
			}
			m.a.done <- approvalResult{ok: ok}
		case func(*model):
			reloads++
			m(&model{})
		}
	})
	tab := &Tab{org: "Acme", dir: filepath.Join(org, ".desk", "infra")}
	if r := tab.callTool(toolCall{Name: "servers"}); r.IsError || r.Text != file+":\nHost a\n    #@ password-raw [hidden]\n" {
		t.Errorf("servers: %+v", r)
	}
	r := tab.callTool(toolCall{Name: "add_server", Args: json.RawMessage(`{"name":"b","hostname":"192.0.2.2","reason":"asked"}`)})
	if b, _ := os.ReadFile(file); r.IsError || string(b) != "Host a\n    #@ password-raw pw\n\nHost b\n    HostName 192.0.2.2\n" || reloads != 1 {
		t.Fatalf("add: %+v %q %d", r, b, reloads)
	}
	if a := seen[0]; a.title != "Claude wants to add a server" || a.file != file || a.reason != "asked" || !strings.Contains(a.cmd, "+Host b") ||
		!strings.Contains(a.cmd, "password-raw [hidden]") {
		t.Errorf("approval %+v", a)
	}
	ok = false
	if r := tab.callTool(toolCall{Name: "remove_server", Args: json.RawMessage(`{"name":"b"}`)}); !r.IsError || !strings.Contains(r.Text, "denied") {
		t.Errorf("denied: %+v", r)
	}
	ok, during = true, func() { os.WriteFile(file, []byte("Host b\n"), 0o644) }
	if r := tab.callTool(toolCall{Name: "edit_server", Args: json.RawMessage(`{"name":"b","user":"x"}`)}); !r.IsError || !strings.Contains(r.Text, "changed while") {
		t.Errorf("raced: %+v", r)
	}
	during = nil
	for _, c := range []toolCall{{Name: "edit_server", Args: json.RawMessage(`[`)}, {Name: "edit_server", Args: json.RawMessage(`{"name":"zz","user":"x"}`)}} {
		if r := tab.callTool(c); !r.IsError {
			t.Errorf("%s: %+v", c.Args, r)
		}
	}
	os.Remove(file)
	os.Mkdir(file, 0o700)
	if r := tab.callTool(toolCall{Name: "servers"}); !r.IsError {
		t.Errorf("unreadable: %+v", r)
	}
	os.Remove(file)
	os.Chmod(filepath.Dir(file), 0o500)
	defer os.Chmod(filepath.Dir(file), 0o700)
	if os.WriteFile(filepath.Join(filepath.Dir(file), "probe"), nil, 0o600) == nil {
		return // root writes anyway
	}
	if r := tab.callTool(toolCall{Name: "add_server", Args: json.RawMessage(`{"name":"c"}`)}); !r.IsError {
		t.Errorf("unwritable: %+v", r)
	}
}
