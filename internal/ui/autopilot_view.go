package ui

import (
	"fmt"
	"image/color"
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// pilotDialog is the new autopilot: the tickets checked in an org's
// Tickets view, its name, level, how many cards work at once, and what the
// user lets it change outside without asking.
type pilotDialog struct {
	org     string
	tickets []links.Ticket
	title   widget.Editor
	grants  widget.Editor
	level   int // into pilotLevels
	max     int
	budget  int  // into pilotBudgets
	focus   bool // the name takes the keyboard next frame
	list    widget.List
	act     func() // a button's, run once the dialog is drawn
}

func (d *pilotDialog) sig() string {
	if d == nil {
		return ""
	}
	return fmt.Sprint(d.level, d.max, d.budget)
}

// noTint is a choice's color for options with none of their own.
func noTint(int) color.NRGBA { return color.NRGBA{} }

// pilotBudgets are the dialog's group budgets in USD, 0 none.
var pilotBudgets = []float64{0, 5, 10, 25, 50}

// pilotTickets is the org's checked tickets without a card yet (one made
// after it was checked drops out), in the view's order.
func (u *ui) pilotTickets(org string) []links.Ticket {
	var ts []links.Ticket
	for _, t := range u.tickets[org] {
		if u.tkSel[t.URL] && u.ticketCard(t) == nil {
			ts = append(ts, t)
		}
	}
	return ts
}

// openPilot opens the new autopilot's dialog over org's checked tickets.
func (u *ui) openPilot(org string) {
	ts := u.pilotTickets(org)
	if len(ts) == 0 {
		return
	}
	d := &pilotDialog{org: org, tickets: ts, level: 1, max: 3, focus: true}
	d.list.Axis = layout.Vertical
	d.title.SingleLine, d.title.Submit = true, true
	var keys []string
	for i, t := range ts {
		if i == 4 {
			keys = append(keys, fmt.Sprintf("+%d", len(ts)-4))
			break
		}
		keys = append(keys, t.Key)
	}
	d.title.SetText("Autopilot: " + strings.Join(keys, ", "))
	u.pilotDlg = d
}

// submitPilot starts the dialog's autopilot and opens its session.
func (u *ui) submitPilot() {
	d := u.pilotDlg
	title := strings.TrimSpace(d.title.Text())
	if title == "" {
		title = "Autopilot"
	}
	key := u.startPilot(d.org, title, d.tickets, kanban.Pilot{Level: pilotLevels[d.level].key, Grants: strings.TrimSpace(d.grants.Text()), Max: d.max, Budget: pilotBudgets[d.budget]})
	u.pilotDlg = nil
	clear(u.tkSel)
	if c := u.cardOfKey(key); c != nil {
		u.openPanel(c.ID)
	}
	u.showToast(key+" runs "+fmt.Sprint(len(d.tickets))+" tickets on autopilot", false)
}

func (u *ui) pilotDialog(gtx layout.Context) {
	d := u.pilotDlg
	if d == nil {
		return
	}
	k := u.k
	if d.focus {
		gtx.Execute(key.FocusCmd{Tag: &d.title})
		d.focus = false
	}
	for {
		ev, ok := d.title.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			d.act = u.submitPilot
		}
	}
	k.Modal(gtx, d, 90, 560, nil, func(gtx layout.Context) layout.Dimensions {
		body := func(gtx layout.Context) layout.Dimensions {
			kids := []layout.FlexChild{
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Start}.Layout(gtx,
						layout.Rigid(iconTile(kit.IcoSpark, kit.CPurple, kit.CPanelCard)),
						layout.Rigid(kit.HGap(12)),
						layout.Flexed(1, k.Title("Autopilot", fmt.Sprintf("%d tickets of %s, a card each, run by one Claude that only brings you what matters", len(d.tickets), d.org))))
				}),
				layout.Rigid(kit.VGap(16)), layout.Rigid(k.Caption("Name")), layout.Rigid(kit.VGap(6)),
				layout.Rigid(k.Field(&d.title, "", 0)),
				layout.Rigid(kit.VGap(14)), layout.Rigid(k.Caption("Tickets")), layout.Rigid(kit.VGap(4)),
			}
			for _, t := range d.tickets {
				kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(k.Lbl(t.Key, 12, kit.CAccText, kit.Mono)), layout.Rigid(kit.HGap(8)),
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, t.Title, 12, kit.CText2, kit.Sans, 1) }))
					})
				}))
			}
			var names []string
			for _, l := range pilotLevels {
				names = append(names, l.name)
			}
			kids = append(kids,
				layout.Rigid(kit.VGap(14)), layout.Rigid(k.Caption("Level")), layout.Rigid(kit.VGap(6)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.choice(gtx, "apLevel", names, d.level, noTint, func(i int) { d.level = i })
				}),
				layout.Rigid(kit.VGap(5)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return k.Label(gtx, pilotLevels[d.level].desc, 12, kit.CText3, kit.Sans, 3)
				}),
				layout.Rigid(kit.VGap(14)), layout.Rigid(k.Caption("Allowed without asking")), layout.Rigid(kit.VGap(6)),
				layout.Rigid(k.Field(&d.grants, "e.g. the staging server can be changed freely; push to feature branches", 60)),
				layout.Rigid(kit.VGap(5)),
				layout.Rigid(k.Lbl("Changes outside the cards' folders always ask you, except what you write here.", 12, kit.CText3, kit.Sans)),
				layout.Rigid(kit.VGap(14)), layout.Rigid(k.Caption("Working at once")), layout.Rigid(kit.VGap(6)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.choice(gtx, "apMax", []string{"1", "2", "3", "4", "5"}, d.max-1, noTint, func(i int) { d.max = i + 1 })
				}),
				layout.Rigid(kit.VGap(14)), layout.Rigid(k.Caption("Budget for the whole group")), layout.Rigid(kit.VGap(6)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					names := []string{"None"}
					for _, b := range pilotBudgets[1:] {
						names = append(names, fmt.Sprintf("$%g", b))
					}
					return u.choice(gtx, "apBudget", names, d.budget, noTint, func(i int) { d.budget = i })
				}),
				layout.Rigid(kit.VGap(5)),
				layout.Rigid(k.Lbl("Past it, every card pauses until you tell the autopilot to go on.", 12, kit.CText3, kit.Sans)))
			return layout.Inset{Top: 20, Bottom: 18, Left: 20, Right: 20}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
			})
		}
		foot := func(gtx layout.Context) layout.Dimensions {
			return dialogFoot(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, k.Lbl("Only the autopilot's card notifies you", 12, kit.CText3, kit.Sans)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d0, ok := k.KBtn(gtx, "apCancel", kit.Ghost, "Cancel", "esc")
						if ok {
							d.act = func() { u.pilotDlg = nil }
						}
						return d0
					}), layout.Rigid(kit.HGap(8)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d0, ok := k.KBtn(gtx, "apStart", kit.Primary, "Start", "↵")
						if ok {
							d.act = u.submitPilot
						}
						return d0
					}))
			})
		}
		return kit.Pinned(gtx, &d.list, body, foot)
	})
	if act := d.act; act != nil { // after drawing: both close the dialog
		d.act = nil
		act()
	}
}

// pilotBar is the Tickets view's bar while tickets are checked.
func (u *ui) pilotBar(gtx layout.Context, org string, act *func()) layout.Dimensions {
	k := u.k
	n := len(u.pilotTickets(org))
	return layout.Inset{Top: 12, Bottom: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return kit.Boxed(kit.CAccSoft, kit.NRGBA0, kit.RCard, layout.Inset{Top: 6, Bottom: 6, Left: 12, Right: 6}, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, k.Lbl(fmt.Sprintf("%d selected", n), 13, kit.CAccText, kit.Semi(kit.Sans))),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.KBtn(gtx, "tkClear", kit.Ghost, "Clear", "")
					if ok {
						*act = func() { clear(u.tkSel) }
					}
					return d
				}), layout.Rigid(kit.HGap(6)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.KBtn(gtx, "tkPilot", kit.Primary, "Autopilot…", "")
					if ok {
						*act = func() { u.openPilot(org) }
					}
					return d
				}))
		})(gtx)
	})
}

// pilotRows is what an autopilot card shows of its cards: how many are
// ready, then each with its state.
func (u *ui) pilotRows(id string) layout.Widget {
	k := u.k
	return func(gtx layout.Context) layout.Dimensions {
		done, all := u.pilotDone(id)
		kids := []layout.FlexChild{layout.Rigid(k.Lbl(fmt.Sprintf("Autopilot · %d of %d ready", done, all), 11, kit.CPurple, kit.Semi(kit.Sans)))}
		for n, kid := range u.pilotKids(id) {
			if n == 8 { // ponytail: past eight the card says how many; the panel lists them all
				kids = append(kids, layout.Rigid(k.Lbl(fmt.Sprintf("+%d more", all-8), 11, kit.CText3, kit.Sans)))
				break
			}
			c := u.b.Card(kid)
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 3}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return u.pilotLine(gtx, c) })
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	}
}

// pilotLine is one of an autopilot's cards in a line: its state's dot, key and title.
func (u *ui) pilotLine(gtx layout.Context, c *kanban.Card) layout.Dimensions {
	k := u.k
	sc, ok := statusColor[c.Status]
	if !ok {
		sc = kit.CText3
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(kit.Dot(sc)), layout.Rigid(kit.HGap(6)),
		layout.Rigid(k.Lbl(c.Key(), 11, kit.CText3, kit.Mono)), layout.Rigid(kit.HGap(6)),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, c.Title, 12, kit.CText2, kit.Sans, 1) }))
}

// pilotHead is the session header's part for autopilots: an autopilot's
// cards, each opening its session; a card's autopilot, opening its.
func (u *ui) pilotHead(c *kanban.Card, act *func()) []layout.FlexChild {
	k := u.k
	btn := func(id string, w layout.Widget, f func()) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			d, ok := k.Button(gtx, id, kit.CPanelCard, kit.CHover, kit.RCtl, layout.Inset{Top: 4, Bottom: 4, Left: 8, Right: 8}, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return w(gtx)
			})
			if ok {
				*act = f
			}
			return d
		})
	}
	if pl := u.pilotOf(c); pl != nil {
		pid, key := pl.ID, pl.Key()
		return []layout.FlexChild{layout.Rigid(kit.VGap(8)), btn("pnPilot"+c.ID, k.Lbl("↑ Run by the autopilot "+key+": "+pl.Title, 12, kit.CPurple, kit.Medium(kit.Sans)), func() { u.openPanel(pid) })}
	}
	if c.Pilot == nil {
		return nil
	}
	done, all := u.pilotDone(c.ID)
	level := pilotLevels[0].name
	if c.Pilot.Level == kanban.PilotAuto {
		level = pilotLevels[1].name
	}
	out := []layout.FlexChild{layout.Rigid(kit.VGap(10)), layout.Rigid(k.Lbl(fmt.Sprintf("%s · %d of %d ready · up to %d at once", level, done, all, c.Pilot.Max), 12, kit.CPurple, kit.Semi(kit.Sans)))}
	for _, kid := range u.pilotKids(c.ID) {
		kc := u.b.Card(kid)
		out = append(out, layout.Rigid(kit.VGap(4)), btn("pnKid"+kid, func(gtx layout.Context) layout.Dimensions { return u.pilotLine(gtx, kc) }, func() { u.openPanel(kid) }))
	}
	return out
}
