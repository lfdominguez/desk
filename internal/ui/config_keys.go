package ui

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/lfdominguez/desk/internal/keys"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// cfgKeys is the settings' Keyboard shortcuts page: every shortcut of
// internal/keys by scope, each with Change (records the next key) and
// Reset; saved with the other pages (Ctrl+S) into settings.toml's [keys].
// It is a settings page, not a dialog of its own: it shares their Save,
// Discard and the warning about unsaved changes.
const cfgKeys = "\x00keys"

// loadKeys binds the shortcuts settings.toml rebinds, at start; one it can't
// use is skipped and named in a toast.
func (u *ui) loadKeys() {
	if err := keys.Load(orgs.Keys()); err != nil {
		u.showToast("Keyboard shortcuts: "+firstLine(err.Error())+" (Settings → Keyboard shortcuts)", true)
	}
}

// keysOf is binding id's chords as the page has them: its draft (nil: back
// to the defaults), else as bound now.
func (d *cfgDialog) keysOf(id string) []keys.Chord {
	if c, ok := d.kdraft[id]; ok {
		if c == nil {
			return keys.Get(id).Def
		}
		return []keys.Chord{*c}
	}
	return keys.Of(id)
}

// setKey takes chord c, recorded for d.rec: refused (with why) when it
// breaks the rule, else drafted; a clash is named on the row until Save.
func (d *cfgDialog) setKey(c keys.Chord) {
	b := keys.Get(d.rec)
	if err := keys.Check(b, c); err != nil {
		d.recErr = c.Text() + ": " + err.Error()
		return
	}
	d.rec, d.recErr = "", ""
	switch {
	case !keys.Changed(b.ID) && slices.Equal(b.Def, []keys.Chord{c}), keys.Changed(b.ID) && keys.Of(b.ID)[0] == c:
		delete(d.kdraft, b.ID) // as it is now
	default:
		d.kdraft[b.ID] = &c
	}
}

// resetKey drafts binding id back to its defaults.
func (d *cfgDialog) resetKey(id string) {
	if keys.Changed(id) {
		d.kdraft[id] = nil
	} else {
		delete(d.kdraft, id)
	}
}

// clash is what binding b's chords also run in its scope, as drafted.
func (d *cfgDialog) clash(b *keys.Binding) *keys.Binding {
	for _, c := range d.keysOf(b.ID) {
		if o := keys.Clash(b, c, d.keysOf); o != nil {
			return o
		}
	}
	return nil
}

// saveKeys binds the draft and writes the rebindings to settings.toml;
// a clash keeps the page open and says which.
func (u *ui) saveKeys(d *cfgDialog) bool {
	for _, id := range slices.Sorted(maps.Keys(d.kdraft)) {
		if b := keys.Get(id); d.clash(b) != nil {
			d.err = fmt.Sprintf("%s runs both “%s” and “%s” — change one", d.keysOf(id)[0].Text(), b.What, d.clash(b).What)
			return false
		}
	}
	for id, c := range d.kdraft {
		if c == nil {
			keys.Reset(id)
		} else {
			keys.Set(id, *c)
		}
	}
	d.kdraft = map[string]*keys.Chord{}
	u.wsRekey()
	if err := orgs.SaveKeys(keys.Overrides()); err != nil {
		d.err = "Bound until desk quits, but not saved: " + firstLine(err.Error())
		return false
	}
	return true
}

// keyRecord takes the next key press for the row being changed: esc
// cancels, any other key with its modifiers is the new chord. Every key is
// the page's while it records (u.keys and Ctrl+S stand aside).
func (u *ui) keyRecord(gtx layout.Context, d *cfgDialog) {
	all := key.ModCtrl | key.ModShift | key.ModAlt | key.ModCommand | key.ModSuper
	for {
		ev, ok := gtx.Event(key.Filter{Optional: all}, key.Filter{Name: key.NameTab, Optional: all}) // Tab is a system key: named
		if !ok {
			return
		}
		e, ok := ev.(key.Event)
		if !ok {
			continue
		}
		if e.Name == key.NameEscape && e.Modifiers == 0 { // a release too: harmless, it records nothing
			d.rec, d.recErr = "", ""
			continue
		}
		if c, ok := keys.Record(e); ok {
			d.setKey(c)
		}
	}
}

// cfgKeysPage lists every shortcut by scope: what it does, its keys (as
// drafted), Change and Reset; fixed ones say so. Reset all on top.
func (u *ui) cfgKeysPage(gtx layout.Context) layout.Dimensions {
	k, d := u.k, u.cfg
	var rows []layout.Widget
	rows = append(rows, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return k.Label(gtx, "Change records the next key you press; esc cancels. A key with a modifier needs Ctrl or Alt: Shift alone types (Shift+2 is @ on many layouts).", 12, kit.CText3, kit.Sans, 2)
				}),
				layout.Rigid(kit.HGap(12)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					dim, ok := k.KBtn(gtx, "keysResetAll", kit.Secondary, "Reset all", "")
					if ok {
						d.act = func() {
							for _, b := range keys.All {
								if b.Fixed == "" {
									d.resetKey(b.ID)
								}
							}
							d.rec, d.recErr = "", ""
						}
					}
					return dim
				}))
		})
	})
	for _, sc := range keys.Scopes {
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 14, Bottom: 6}.Layout(gtx, k.Caption(sc))
		})
		for i := range keys.All {
			if b := &keys.All[i]; b.Scope == sc {
				rows = append(rows, func(gtx layout.Context) layout.Dimensions { return u.cfgKeyRow(gtx, d, b) })
			}
		}
	}
	return d.list.Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions { return rows[i](gtx) })
}

// cfgKeyRow is one shortcut: what it does, its caps, Change and Reset, and
// under it why a recorded key was refused or what it clashes with.
func (u *ui) cfgKeyRow(gtx layout.Context, d *cfgDialog, b *keys.Binding) layout.Dimensions {
	k := u.k
	var caps []layout.FlexChild
	switch {
	case d.rec == b.ID:
		caps = append(caps, layout.Rigid(k.Lbl("Press a key…", 12, kit.CAccText, kit.Semi(kit.Sans))))
	case b.Fixed != "":
		caps = append(caps, layout.Rigid(k.KeyCap(b.Fixed, false)), layout.Rigid(kit.HGap(8)), layout.Rigid(k.Lbl("fixed", 11, kit.CText3, kit.Sans)))
	default:
		for j, c := range d.keysOf(b.ID) {
			if j > 0 {
				caps = append(caps, layout.Rigid(kit.HGap(6)), layout.Rigid(k.Lbl("or", 11, kit.CText3, kit.Sans)), layout.Rigid(kit.HGap(6)))
			}
			caps = append(caps, layout.Rigid(k.KeyCap(c.Text(), false)))
		}
	}
	note, tone := "", kit.CText3
	if d.rec == b.ID && d.recErr != "" {
		note, tone = d.recErr, kit.CRedText
	} else if _, drafted := d.kdraft[b.ID]; drafted {
		note, tone = "edited", kit.CAccText
		if o := d.clash(b); o != nil {
			note, tone = "also runs “"+o.What+"” — change one", kit.COrange
		}
	} else if keys.Changed(b.ID) {
		note = "changed from " + strings.Join(chordTexts(b.Def), " or ")
	}
	row := func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		kids := []layout.FlexChild{
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, b.What, 13, kit.CText, kit.Sans, 1) }),
			layout.Rigid(kit.HGap(12)),
		}
		kids = append(kids, caps...)
		if b.Fixed == "" {
			kids = append(kids, layout.Rigid(kit.HGap(12)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				label := "Change"
				if d.rec == b.ID {
					label = "Cancel"
				}
				dim, ok := k.KBtn(gtx, "keyChange"+b.ID, kit.Ghost, label, "")
				if ok {
					d.act = func() {
						if d.rec == b.ID {
							d.rec = ""
						} else {
							d.rec = b.ID
						}
						d.recErr = ""
					}
				}
				return dim
			}))
			_, drafted := d.kdraft[b.ID]
			if keys.Changed(b.ID) || drafted {
				kids = append(kids, layout.Rigid(kit.HGap(4)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					dim, ok := k.KBtn(gtx, "keyReset"+b.ID, kit.Ghost, "Reset", "")
					if ok {
						d.act = func() { d.resetKey(b.ID); d.rec, d.recErr = "", "" }
					}
					return dim
				}))
			}
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
	}
	return layout.Inset{Bottom: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 4, Bottom: 4, Left: unit.Dp(4)}.Layout(gtx, row)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if note == "" {
					return layout.Dimensions{}
				}
				return layout.Inset{Left: 4, Bottom: 4}.Layout(gtx, k.Lbl(note, 11, tone, kit.Sans))
			}))
	})
}

func chordTexts(cs []keys.Chord) []string {
	var s []string
	for _, c := range cs {
		s = append(s, c.Text())
	}
	return s
}
