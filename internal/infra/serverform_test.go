package infra

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestServerFields: a server's own lines, the first of each keyword as ssh
// reads them, its group, desc and vault password; not the file's defaults.
func TestServerFields(t *testing.T) {
	org, _ := serversOrg(t, someServers+"\nHost pw\n    Port 2222\n    #@ password \"pw root\" secret\n    #@ desc \"a b\"\n")
	a, err := ServerFields(org, "web1")
	want := ServerArgs{Name: "web1", HostName: "192.0.2.10", User: "deploy", IdentityFile: "~/.ssh/old", Group: "eu/web", Desc: "the proxy"}
	if err != nil || !reflect.DeepEqual(a, want) {
		t.Errorf("web1: %+v %v", a, err)
	}
	a, err = ServerFields(org, "pw")
	if err != nil || a.Port != 2222 || a.Desc != "a b" || a.Options["#@ password"] != `"pw root" secret` || a.Group != "db" {
		t.Errorf("pw: %+v %v", a, err)
	}
	if _, err := ServerFields(org, "nope"); err == nil {
		t.Error("a server that isn't there")
	}
	if _, err := ServerFields(filepath.Join(t.TempDir(), "Empty"), "web1"); err == nil {
		t.Error("an org without a servers file")
	}
}

// TestServerFieldsRefused: a hosts-cmd's server and one named by a pattern
// have no block the form can change.
func TestServerFieldsRefused(t *testing.T) {
	org, file := serversOrg(t, "Host web-*\n    User x\n")
	os.WriteFile(filepath.Join(filepath.Dir(file), "servers-dynamic.conf"), []byte("Host dyn\n    HostName 192.0.2.9\n"), 0o600)
	os.WriteFile(file, []byte("Include servers-dynamic.conf\nHost web-*\n    User x\nHost a\n"), 0o600)
	if _, err := ServerFields(org, "dyn"); err == nil || !strings.Contains(err.Error(), "hosts-cmd") {
		t.Errorf("dynamic: %v", err)
	}
	os.WriteFile(file, []byte("#@ group g\nHost *.example.com web\n    User x\n"), 0o600)
	if a, err := ServerFields(org, "web"); err != nil || a.User != "x" {
		t.Errorf("a name beside a pattern: %+v %v", a, err)
	}
}

// TestServerDiff: only what changed goes to edit_server; an emptied field
// removes its keyword; the same form changes nothing.
func TestServerDiff(t *testing.T) {
	was := ServerArgs{Name: "web1", HostName: "192.0.2.10", User: "deploy", Port: 22, IdentityFile: "k", Group: "eu/web", Desc: "d",
		Options: map[string]string{"#@ password": "item"}}
	if d := ServerDiff(was, was); !reflect.DeepEqual(d, ServerArgs{Name: "web1"}) {
		t.Errorf("same: %+v", d)
	}
	now := ServerArgs{Name: "web1", HostName: "192.0.2.11", Port: 2222, IdentityFile: "k", Group: "db", Desc: "d"}
	want := ServerArgs{Name: "web1", HostName: "192.0.2.11", Port: 2222, Group: "db", Options: map[string]string{"User": "", "#@ password": ""}}
	if d := ServerDiff(was, now); !reflect.DeepEqual(d, want) {
		t.Errorf("changed: %+v", d)
	}
	now = ServerArgs{Name: "web1", HostName: "192.0.2.10", User: "deploy", Group: "eu/web", Options: map[string]string{"#@ password": "other"}}
	want = ServerArgs{Name: "web1", User: "", IdentityFile: "", Options: map[string]string{"Port": "", "IdentityFile": "", "#@ desc": "", "#@ password": "other"}}
	if d := ServerDiff(was, now); !reflect.DeepEqual(d, want) {
		t.Errorf("emptied: %+v", d)
	}
	if d := ServerDiff(ServerArgs{Name: "a"}, ServerArgs{Name: "a", User: "u", Desc: "x"}); d.User != "u" || d.Desc != "x" || d.Options != nil {
		t.Errorf("set: %+v", d)
	}
}

// TestServerSave: the form's add and edit write the file, the edit only
// what changed (its second key stays).
func TestServerSave(t *testing.T) {
	org, file := serversOrg(t, someServers)
	if _, err := ServerSave(org, "add_server", ServerArgs{Name: "db2", HostName: "192.0.2.21", Group: "db"}); err != nil {
		t.Fatal(err)
	}
	was, _ := ServerFields(org, "web1")
	now := was
	now.User, now.Desc = "root", ""
	if _, err := ServerSave(org, "edit_server", ServerDiff(was, now)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	for _, s := range []string{"Host web1\n    HostName 192.0.2.10\n    User root\n    IdentityFile ~/.ssh/old\n    IdentityFile ~/.ssh/older\n\nHost web2",
		"Host db1\n    HostName 192.0.2.20\n\nHost db2\n    HostName 192.0.2.21\n"} {
		if !strings.Contains(string(b), s) {
			t.Errorf("want %q in:\n%s", s, b)
		}
	}
	if _, err := ServerSave(org, "add_server", ServerArgs{Name: "db2"}); err == nil {
		t.Error("added twice")
	}
}
