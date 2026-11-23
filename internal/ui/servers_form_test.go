package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gioui.org/io/key"
)

// acmeServers is the picker harness's servers file.
func (h *harness) acmeServers() string {
	b, _ := os.ReadFile(filepath.Join(h.u.root, "Acme", ".desk", "ssh_config"))
	return string(b)
}

// TestPickerNewServer: the picker's last row is New server, named by the
// search; enter in a field saves it to the servers file, back to the picker.
func TestPickerNewServer(t *testing.T) {
	h := pickerHarness(t, 3)
	if rows := h.u.pickRows("Acme", ""); len(rows) != 5 || !rows[4].add {
		t.Fatalf("rows %+v", rows)
	}
	h.u.pickQ.SetText("db1")
	h.frame()
	if rows := h.u.pickRows("Acme", "db1"); len(rows) != 1 || !rows[0].add {
		t.Fatalf("no match: %+v", rows)
	}
	h.send(press(key.NameReturn, 0))
	f := h.u.srvForm
	if f == nil || f.was != nil || f.ed[sfName].Text() != "db1" || h.u.pick != "Acme" {
		t.Fatalf("form %+v, picker %q", f, h.u.pick)
	}
	f.ed[sfAddr].SetText("192.0.2.20")
	f.ed[sfGroup].SetText("db")
	f.ed[sfPass].SetText("item")
	h.frame()
	h.send(press(key.NameReturn, 0)) // in the address, focused as the form opened
	if h.u.srvForm != nil || h.u.pick != "Acme" || !strings.Contains(h.u.toast, "Added db1") {
		t.Fatalf("form %v, picker %q, toast %q", h.u.srvForm, h.u.pick, h.u.toast)
	}
	if !strings.Contains(h.acmeServers(), "#@ group db\nHost db1\n    HostName 192.0.2.20\n") || !strings.Contains(h.acmeServers(), "    #@ password item\n") {
		t.Fatalf("file:\n%s", h.acmeServers())
	}
}

// TestPickerNewServerTwoWords: a search of several words names nothing.
func TestPickerNewServerTwoWords(t *testing.T) {
	h := pickerHarness(t, 1)
	h.u.pickQ.SetText("no such")
	h.frame()
	h.send(press(key.NameReturn, 0))
	if f := h.u.srvForm; f == nil || f.ed[sfName].Text() != "" {
		t.Fatalf("form %+v", f)
	}
}

// TestPickerEdit: Ctrl+E on a host opens its lines; Save writes only what
// changed. On the Claude or New server row it does nothing.
func TestPickerEdit(t *testing.T) {
	h := pickerHarness(t, 3)
	h.send(press("E", key.ModShortcut))
	if h.u.srvForm != nil {
		t.Fatal("the org's Claude has nothing to edit")
	}
	h.send(press(key.NameDownArrow, 0), press(key.NameDownArrow, 0))
	h.send(press("E", key.ModShortcut))
	f := h.u.srvForm
	if f == nil || f.was == nil || f.was.Name != "h01" || !f.ed[sfName].ReadOnly || f.ed[sfGroup].Text() != "web" {
		t.Fatalf("form %+v", f)
	}
	f.ed[sfUser].SetText("root")
	f.ed[sfPort].SetText("2222")
	h.clickButton("sfSave")
	if h.u.srvForm != nil || !strings.Contains(h.u.toast, "Saved h01") {
		t.Fatalf("form %v, toast %q", h.u.srvForm, h.u.toast)
	}
	if !strings.Contains(h.acmeServers(), "Host h01\n    User root\n    Port 2222\nHost h02") {
		t.Fatalf("file:\n%s", h.acmeServers())
	}
	h.send(press("E", key.ModShortcut))
	if f := h.u.srvForm; f == nil || f.ed[sfPort].Text() != "2222" {
		t.Fatalf("reopened: %+v", f)
	}
	h.u.srvForm = nil
	h.u.pickSel = 4 // New server
	h.frame()
	h.send(press("E", key.ModShortcut))
	if h.u.srvForm != nil {
		t.Fatal("New server has nothing to edit")
	}
}

// TestPickerEditButton: the selected host's Edit button opens the form;
// saving it unchanged writes nothing; Cancel and esc go back to the picker.
func TestPickerEditButton(t *testing.T) {
	h := pickerHarness(t, 2)
	h.send(press(key.NameDownArrow, 0))
	h.clickButton("pickEdit")
	if f := h.u.srvForm; f == nil || f.was == nil || f.was.Name != "h00" {
		t.Fatalf("form %+v", f)
	}
	before := h.acmeServers()
	h.clickButton("sfSave")
	if h.u.srvForm != nil || h.acmeServers() != before {
		t.Fatalf("unchanged: form %v, file:\n%s", h.u.srvForm, h.acmeServers())
	}
	h.send(press("E", key.ModShortcut))
	h.clickButton("sfCancel")
	if h.u.srvForm != nil || h.u.pick != "Acme" {
		t.Fatalf("cancel: form %v, picker %q", h.u.srvForm, h.u.pick)
	}
	h.send(press("E", key.ModShortcut))
	h.send(press(key.NameEscape, 0))
	if h.u.srvForm != nil || h.u.pick != "Acme" {
		t.Fatalf("esc: form %v, picker %q", h.u.srvForm, h.u.pick)
	}
}

// TestServerFormErrors: what's wrong shows under the fields and the form
// stays; typing again clears it. A server the file can't give opens nothing.
func TestServerFormErrors(t *testing.T) {
	h := pickerHarness(t, 1)
	h.u.openServerForm("Acme", nil, "", "")
	h.frame()
	f := h.u.srvForm
	for _, c := range []struct{ name, port, want string }{
		{"", "", "name:"},
		{"x", "ssh", "port:"},
		{"x", "70000", "port 70000"},
		{"h00", "", "already a server"},
		{"bad name", "", "letters, digits"},
	} {
		f.ed[sfName].SetText(c.name)
		f.ed[sfPort].SetText(c.port)
		h.u.saveServer()
		if h.u.srvForm != f || !strings.Contains(f.err, c.want) {
			t.Errorf("%q %q: err %q", c.name, c.port, f.err)
		}
	}
	h.frame() // the SetTexts' changes clear it
	h.u.saveServer()
	h.frame() // drawn under the fields
	if f.err == "" {
		t.Fatal("a frame cleared the error")
	}
	h.send(key.EditEvent{Text: "a"})
	if f.err != "" {
		t.Errorf("typing keeps %q", f.err)
	}
	h.u.srvForm = nil
	h.u.editServer("Acme", "gone")
	if h.u.srvForm != nil || !h.u.toastBad {
		t.Fatalf("a missing server: form %v, toast %q", h.u.srvForm, h.u.toast)
	}
}
