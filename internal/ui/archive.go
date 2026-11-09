package ui

import (
	"fmt"
	"image"
	"image/color"

	"gioui.org/layout"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// doneCards is the Done column's cards: All cards leaves them out, the Done
// archive shows them above the archived ones.
func (u *ui) doneCards() []kanban.Card {
	if d := u.b.Column("Done"); d >= 0 {
		return u.b.Columns[d].Cards
	}
	return nil
}

// isDone is whether card id sits in the Done column.
func (u *ui) isDone(id string) bool {
	c, _ := u.b.Find(id)
	return c >= 0 && c == u.b.Column("Done")
}

// archive is the Done column's cards, then the archived ones newest first,
// in one centered column that matches the search; a click opens a card's
// session to read or restore it.
func (u *ui) archive(gtx layout.Context) layout.Dimensions {
	k := u.k
	gtx.Constraints.Min = gtx.Constraints.Max
	kit.Fill(gtx, kit.CPane)
	type entry struct {
		c    kanban.Card
		note string
	}
	var es []entry
	for _, c := range u.doneCards() {
		if c.Matches(u.search.Text()) {
			es = append(es, entry{c, "Done " + c.Updated.Format("Jan 2 15:04") + " · not archived yet"})
		}
	}
	for _, c := range u.b.Archive {
		if c.Matches(u.search.Text()) {
			es = append(es, entry{c, fmt.Sprintf("Archived %s from %s", c.Archived.Format("Jan 2 15:04"), c.From)})
		}
	}
	if len(es) == 0 {
		return layout.Center.Layout(gtx, k.Lbl("No done cards. Cards you move to Done show here.", 13, kit.CText3, kit.Sans))
	}
	w := min(gtx.Constraints.Max.X-gtx.Dp(48), gtx.Dp(720))
	return layout.N.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints = layout.Constraints{Min: image.Pt(w, gtx.Constraints.Max.Y), Max: image.Pt(w, gtx.Constraints.Max.Y)}
		return u.archList.Layout(gtx, len(es), func(gtx layout.Context, n int) layout.Dimensions {
			c := es[n].c
			in := layout.Inset{Bottom: 10}
			if n == 0 {
				in.Top = 16
			}
			return in.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Left: 4, Bottom: 4}.Layout(gtx, k.Lbl(es[n].note, 11, kit.CText3, kit.Sans))
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d, ok := k.Button(gtx, "arch"+c.ID, color.NRGBA{}, color.NRGBA{}, kit.RCard, layout.Inset{}, func(gtx layout.Context) layout.Dimensions {
							return u.cardBody(gtx, c, c.ID == u.panel, k.Click("arch"+c.ID).Hovered(), true)
						})
						if ok {
							u.openPanel(c.ID)
						}
						return d
					}))
			})
		})
	})
}
