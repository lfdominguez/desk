package ui

import (
	"errors"
	"strconv"
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// servers_form.go: the server picker's form, a server added to or changed in
// the organization's servers file by the user (infra's ServerSave, the
// change the Claudes' add_server and edit_server make).

// The form's fields, in order.
const (
	sfName = iota
	sfAddr
	sfUser
	sfPort
	sfKey
	sfGroup
	sfDesc
	sfPass
	sfCount
)

var sfLabels = [sfCount][2]string{
	{"Name", "Its Host alias: web-01"},
	{"Address", "HostName: 192.0.2.10 or web-01.example.com"},
	{"User", "root"},
	{"Port", "22"},
	{"Key file", "IdentityFile: ~/.ssh/id_ed25519"},
	{"Group", "web, or eu/web"},
	{"Description", "What it is, in a few words"},
	{"Password", "A vault item: \"{user}@{host}\", then a field if not password"},
}

// srvForm is the form open over the picker: a new server (was nil) or the
// one was holds the lines of.
type srvForm struct {
	org   string
	was   *infra.ServerArgs
	ed    [sfCount]widget.Editor
	err   string
	focus *widget.Editor
	act   func() // a footer button's, run after the frame
	list  widget.List
}

// serverSave writes the form's change; tests replace it.
var serverSave = infra.ServerSave

// openServerForm opens the form over org's picker: a new server named name
// in group, or, with was, that server's lines to change.
func (u *ui) openServerForm(org string, was *infra.ServerArgs, name, group string) {
	f := &srvForm{org: org, was: was}
	for i := range f.ed {
		f.ed[i].SingleLine, f.ed[i].Submit = true, true
	}
	v := infra.ServerArgs{Name: name, Group: group}
	if was != nil {
		v = *was
		f.ed[sfName].ReadOnly = true // edit_server finds it by its name
	}
	for i, s := range [sfCount]string{v.Name, v.HostName, v.User, portText(v.Port), v.IdentityFile, v.Group, v.Desc, v.Options["#@ password"]} {
		f.ed[i].SetText(s)
	}
	f.focus = &f.ed[sfName]
	if was != nil || name != "" {
		f.focus = &f.ed[sfAddr]
	}
	u.srvForm = f
}

func portText(p int) string {
	if p == 0 {
		return ""
	}
	return strconv.Itoa(p)
}

// editServer opens the form on org's server name, as its block says.
func (u *ui) editServer(org, name string) {
	a, err := infra.ServerFields(orgs.Dir(u.root, org, ""), name)
	if err != nil {
		u.showToast(err.Error(), true)
		return
	}
	u.openServerForm(org, &a, "", "")
}

// args is what the form holds, as add_server's arguments.
func (f *srvForm) args() (infra.ServerArgs, error) {
	t := func(i int) string { return strings.TrimSpace(f.ed[i].Text()) }
	a := infra.ServerArgs{Name: t(sfName), HostName: t(sfAddr), User: t(sfUser), IdentityFile: t(sfKey), Group: t(sfGroup), Desc: t(sfDesc)}
	if p := t(sfPort); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return a, errors.New("port: a number, 1 to 65535")
		}
		a.Port = n
	}
	if p := t(sfPass); p != "" {
		a.Options = map[string]string{"#@ password": p}
	}
	if a.Name == "" {
		return a, errors.New("name: the server needs one")
	}
	return a, nil
}

// none is whether ServerDiff's change changes nothing.
func none(d infra.ServerArgs) bool {
	return d.HostName == "" && d.User == "" && d.Port == 0 && d.IdentityFile == "" && d.Desc == "" && d.Group == "" && len(d.Options) == 0
}

// saveServer writes the form's server and closes the form, back to the
// picker; what's wrong stays in it, under the fields.
func (u *ui) saveServer() {
	f := u.srvForm
	a, err := f.args()
	if err != nil {
		f.err = err.Error()
		return
	}
	dir, what := orgs.Dir(u.root, f.org, ""), "Added "+a.Name+" to the servers file"
	if f.was == nil {
		_, err = serverSave(dir, "add_server", a)
	} else if d := infra.ServerDiff(*f.was, a); !none(d) {
		_, err = serverSave(dir, "edit_server", d)
		what = "Saved " + a.Name
	} else {
		u.srvForm = nil
		return
	}
	if err != nil {
		f.err = err.Error()
		return
	}
	u.srvForm = nil
	if u.infra != nil {
		u.infra.Reload() // the picker and the sidebar list it, and panes connect with it
	}
	u.showToast(what, false)
}

// serverForm draws the form: the fields, what went wrong, Cancel and Save.
// Enter in any field saves; Tab moves between them.
func (u *ui) serverForm(gtx layout.Context) {
	f := u.srvForm
	if f == nil {
		return
	}
	for i := range f.ed {
		for {
			ev, ok := f.ed[i].Update(gtx)
			if !ok {
				break
			}
			switch ev.(type) {
			case widget.SubmitEvent:
				f.act = u.saveServer
			case widget.ChangeEvent:
				f.err = ""
			}
		}
	}
	if f.focus != nil {
		gtx.Execute(key.FocusCmd{Tag: f.focus})
		f.focus = nil
	}
	k := u.k
	title, sub := "New server", f.org+" · added to its servers file"
	if f.was != nil {
		title, sub = "Edit "+f.was.Name, f.org+" · its lines in the servers file"
	}
	field := func(i int) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(k.Caption(sfLabels[i][0])), layout.Rigid(kit.VGap(6)),
				layout.Rigid(k.Field(&f.ed[i], sfLabels[i][1], 0)))
		}
	}
	pair := func(a, b int, wa float32) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx, layout.Flexed(wa, field(a)), layout.Rigid(kit.HGap(10)), layout.Flexed(1-wa, field(b)))
		}
	}
	k.Modal(gtx, f, 90, 520, nil, func(gtx layout.Context) layout.Dimensions {
		body := func(gtx layout.Context) layout.Dimensions {
			kids := []layout.FlexChild{
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Start}.Layout(gtx,
						layout.Rigid(iconTile(kit.IcoTerminal, kit.CText, kit.CPanelCard)),
						layout.Rigid(kit.HGap(12)),
						layout.Flexed(1, k.Title(title, sub)))
				}),
				layout.Rigid(kit.VGap(18)), layout.Rigid(pair(sfName, sfGroup, 0.5)),
				layout.Rigid(kit.VGap(12)), layout.Rigid(pair(sfAddr, sfPort, 0.75)),
				layout.Rigid(kit.VGap(12)), layout.Rigid(pair(sfUser, sfKey, 0.4)),
				layout.Rigid(kit.VGap(12)), layout.Rigid(field(sfDesc)),
				layout.Rigid(kit.VGap(12)), layout.Rigid(field(sfPass)),
			}
			if f.err != "" {
				kids = append(kids, layout.Rigid(kit.VGap(10)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(kit.Icon(13, kit.CRedText, kit.IcoWarn)), layout.Rigid(kit.HGap(6)),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, f.err, 12, kit.CRedText, kit.Sans, 3) }))
				}))
			}
			return layout.Inset{Top: 20, Bottom: 18, Left: 20, Right: 20}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
			})
		}
		foot := func(gtx layout.Context) layout.Dimensions {
			return dialogFoot(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, k.Lbl("Other keywords: the file itself", 12, kit.CText3, kit.Sans)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d, ok := k.KBtn(gtx, "sfCancel", kit.Ghost, "Cancel", "esc")
						if ok {
							f.act = func() { u.srvForm = nil }
						}
						return d
					}), layout.Rigid(kit.HGap(8)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d, ok := k.KBtn(gtx, "sfSave", kit.Primary, "Save", "↵")
						if ok {
							f.act = u.saveServer
						}
						return d
					}))
			})
		}
		return kit.Pinned(gtx, &f.list, body, foot)
	})
	if act := f.act; act != nil { // after drawing: both close the form
		f.act = nil
		act()
	}
}

// sig is what of the form shows besides its editors, for u.sig.
func (f *srvForm) sig() string {
	if f == nil {
		return ""
	}
	return f.err
}
