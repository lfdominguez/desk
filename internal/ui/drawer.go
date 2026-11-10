package ui

import (
	"image"
	"image/color"
	"time"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/keys"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// drawer edits an existing card in a panel on the window's right, the
// board still in view beside it. Closing it (esc, ×, a click beside it)
// saves; a field that doesn't check out keeps it open and says why.
func (u *ui) drawer(gtx layout.Context, s *sheet) {
	kit.Fill(gtx, color.NRGBA{5, 6, 8, 0x90}) // lighter than a modal's: the board stays readable beside it
	pressed := false
	for {
		e, ok := gtx.Event(pointer.Filter{Target: s, Kinds: pointer.Press})
		if !ok {
			break
		}
		if p, ok := e.(pointer.Event); ok && p.Kind == pointer.Press {
			pressed = true
		}
	}
	if pressed && s.act == nil {
		s.act = u.commit // a click beside the drawer closes it, saving
	}
	full := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	event.Op(gtx.Ops, s)
	full.Pop()

	w := min(gtx.Dp(520), gtx.Constraints.Max.X-gtx.Dp(40))
	defer op.Offset(image.Pt(gtx.Constraints.Max.X-w, 0)).Push(gtx.Ops).Pop()
	r := image.Rectangle{Max: image.Pt(w, gtx.Constraints.Max.Y)}
	kit.FillRRect(gtx.Ops, r, 0, kit.CRim)
	kit.FillRRect(gtx.Ops, image.Rect(gtx.Dp(1), 0, w, r.Max.Y), 0, kit.CPanel)
	cl := clip.Rect(r).Push(gtx.Ops)
	event.Op(gtx.Ops, &s.list) // presses on the drawer stay in it
	cl.Pop()
	dgtx := gtx
	dgtx.Constraints = layout.Exact(image.Pt(w-gtx.Dp(1), r.Max.Y))
	defer op.Offset(image.Pt(gtx.Dp(1), 0)).Push(gtx.Ops).Pop()
	u.pinned(dgtx, u.drawerRows(s), u.drawerFoot)
}

// drawerRows are the drawer's sections: header, title, the card's
// properties as name and value, its links with their state, its notes.
func (u *ui) drawerRows(s *sheet) []sheetRow {
	k := u.k
	c := u.b.Card(s.id)
	if c == nil {
		return nil
	}
	id := s.id
	pad := func(top, bottom unit.Dp, w layout.Widget) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: top, Bottom: bottom, Left: 20, Right: 20}.Layout(gtx, w)
		}
	}
	iconBtn := func(bid string, ico kit.IconPath, tip string, f func()) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			d, ok := k.IconBtn(gtx, bid, ico, false)
			k.Tip(gtx, bid, d, tip, true)
			if ok {
				s.act = f
			}
			return d
		})
	}
	head := sheetRow{w: func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 12, Bottom: 6, Left: 20, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(k.Lbl(c.Key(), 13, kit.CText2, kit.Mono)),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
				iconBtn("shSess", kit.IcoChat, "Open session", func() { u.commit(); u.openPanel(id) }),
				iconBtn("shTerm", kit.IcoTerminal, "Claude Code terminal", func() { u.commit(); u.terminal(id) }),
				iconBtn("shDel", kit.IcoTrash, "Delete ("+keys.Cap("sheet.delete")+"), undo with "+keys.Cap("undo"), u.remove),
				iconBtn("shClose", kit.IcoX, "Close and save (esc)", u.commit))
		})
	}}
	title := sheetRow{w: pad(8, 16, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(k.Caption("Title")), layout.Rigid(kit.VGap(6)),
			layout.Rigid(kit.Boxed(kit.CPane, kit.CLine, kit.RCtl, layout.Inset{Top: 9, Bottom: 9, Left: 12, Right: 12}, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return k.Editor(gtx, &s.title, "What needs doing", 16, kit.Semi(kit.Sans), kit.CText)
			})))
	}), eds: []*widget.Editor{&s.title}}
	prop := func(name string, eds []*widget.Editor, val layout.Widget) sheetRow {
		return sheetRow{w: pad(0, 0, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: 7, Bottom: 7}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								gtx.Constraints.Min.X, gtx.Constraints.Max.X = gtx.Dp(100), gtx.Dp(100)
								return k.Label(gtx, name, 13, kit.CText3, kit.Sans, 1)
							}),
							layout.Flexed(1, val))
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					sz := image.Pt(gtx.Constraints.Max.X, gtx.Dp(1))
					kit.FillRRect(gtx.Ops, image.Rectangle{Max: sz}, 0, kit.CLine)
					return layout.Dimensions{Size: sz}
				}))
		}), eds: eds}
	}
	flat := func(e *widget.Editor, hint string, face font.Font) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			rim := kit.NRGBA0
			if gtx.Focused(e) {
				rim = kit.CAcc
			}
			return kit.Boxed(kit.NRGBA0, rim, kit.RCtl, layout.Inset{Top: 4, Bottom: 4, Left: 6, Right: 6}, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return k.Editor(gtx, e, hint, 13, face, kit.CText)
			})(gtx)
		}
	}
	var cols []string
	for _, cl := range u.b.Columns {
		cols = append(cols, cl.Title)
	}
	rows := []sheetRow{head, title,
		prop("Status", nil, func(gtx layout.Context) layout.Dimensions {
			return u.choice(gtx, "col", cols, s.col, u.colAccent, func(i int) { s.col = i })
		}),
		prop("Organization", []*widget.Editor{&s.org}, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(flat(&s.org, "Type to filter", kit.Sans)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.placePicker(gtx, s) }))
		}),
		prop("Folder", []*widget.Editor{&s.dir}, flat(&s.dir, "inside it (optional)", kit.Mono)),
	}
	if u.sheetAgents(s); len(s.agents) > 0 {
		rows = append(rows, prop("Agent", nil, func(gtx layout.Context) layout.Dimensions { return u.agentRow(gtx, s) }))
	}
	rows = append(rows,
		prop("Priority", nil, func(gtx layout.Context) layout.Dimensions {
			return u.choice(gtx, "prio", prioNames, s.prio, prioColor, func(i int) { s.prio = i })
		}),
		prop("Due", []*widget.Editor{&s.due}, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, flat(&s.due, "YYYY-MM-DD", kit.Mono)),
				layout.Rigid(k.Lbl(dueDay(s.due.Text(), time.Now()), 12, kit.CText3, kit.Sans)))
		}),
		prop("Labels", []*widget.Editor{&s.labels}, flat(&s.labels, "infra, client-x", kit.Sans)),
		sheetRow{w: pad(18, 0, func(gtx layout.Context) layout.Dimensions {
			kids := []layout.FlexChild{layout.Rigid(k.Caption("Links")), layout.Rigid(kit.VGap(6))}
			for _, l := range c.Links {
				kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Bottom: 6}.Layout(gtx, kit.Boxed(kit.CCard, kit.CLine, kit.RCard, layout.Inset{Top: 8, Bottom: 8, Left: 12, Right: 12}, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						ref := l.Ref
						if ref == "" {
							ref = l.URL
						}
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Flexed(1, k.LblFill(ref, 13, kit.CText, kit.Mono)),
							layout.Rigid(k.Lbl(l.State, 12, toneColor(l.Tone), kit.Medium(kit.Sans))))
					}))
				}))
			}
			kids = append(kids, layout.Rigid(k.Field(&s.links, "+ Add a link or a ticket key, one per line", 0)))
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
		}), eds: []*widget.Editor{&s.links}},
		sheetRow{w: pad(18, 18, func(gtx layout.Context) layout.Dimensions {
			body := k.Field(&s.notes, "Details, next steps…", 96)
			if !s.editNotes {
				body = func(gtx layout.Context) layout.Dimensions {
					d, ok := k.RimButton(gtx, "shNotes", kit.CCard, kit.Mix(kit.CCard, kit.CWhite, .03), kit.CLine, kit.RCard, layout.Inset{Top: 10, Bottom: 10, Left: 14, Right: 14}, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, gtx.Dp(48))
						if s.notes.Len() == 0 {
							return k.Label(gtx, "No notes yet: click to write some", 13, kit.CText3, kit.Sans, 1)
						}
						return u.markdown(gtx, "drawerNotes", s.notes.Text())
					})
					if ok {
						s.editNotes, u.focusLater = true, &s.notes
					}
					return d
				}
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					hint := map[bool]string{false: "click to edit", true: "Markdown"}[s.editNotes]
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx, layout.Rigid(k.Caption("Notes")), layout.Rigid(kit.HGap(8)),
						layout.Rigid(k.Lbl(hint, 12, kit.CText3, kit.Sans)))
				}),
				layout.Rigid(kit.VGap(6)),
				layout.Rigid(body))
		}), eds: []*widget.Editor{&s.notes}},
	)
	return rows
}

// drawerFoot says how the drawer saves and its keys.
func (u *ui) drawerFoot(gtx layout.Context) layout.Dimensions {
	k := u.k
	return dialogFoot(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 2, Bottom: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(kit.Icon(14, kit.CGreen, kit.IcoCheck)), layout.Rigid(kit.HGap(8)),
				layout.Flexed(1, k.LblFill("Changes save when you close", 12, kit.CText3, kit.Sans)),
				layout.Rigid(k.Hint("esc", "close")), layout.Rigid(kit.HGap(14)),
				layout.Rigid(k.Hint(keys.Cap("sheet.delete"), "delete")))
		})
	})
}

// dueDay is a due date as the drawer shows it beside the field: the day,
// and whether it is past; "" while the field isn't a date.
func dueDay(due string, now time.Time) string {
	d, err := time.Parse(kanban.DateLayout, due)
	if err != nil {
		return ""
	}
	in := d.Format("Mon, Jan 2")
	if n, ok := (kanban.Card{Due: due}).DueIn(now); ok && n < 0 {
		in += " · overdue"
	}
	return in
}
