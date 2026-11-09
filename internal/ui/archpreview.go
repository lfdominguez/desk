package ui

import (
	"fmt"
	"image"
	"strings"

	"gioui.org/layout"

	"github.com/lfdominguez/desk/internal/ui/kit"
)

// archPreviewMsgs is how many of a card's last messages its preview shows.
const archPreviewMsgs = 5

// archPreview is the archive's card e read in place: key, kind and
// organization, title, when it finished, Open session / Restore or Archive /
// Delete, labels, links, its session's numbers, notes and last messages
// (read-only). Its buttons' action goes in act.
func (u *ui) archPreview(gtx layout.Context, e archEntry, act *func()) layout.Dimensions {
	k, c := u.k, e.c
	kind, kc := "Archived", kit.CText3
	if e.done {
		kind, kc = "Done", kit.CGreen
	}
	gap := kit.VGap
	caption := func(s string) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 14, Bottom: 6}.Layout(gtx, k.Lbl(s, 11, kit.CText3, kit.Semi(kit.Sans)))
		}
	}
	btn := func(id string, kind kit.Kind, label, key string, f func()) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			d, ok := k.KBtn(gtx, id, kind, label, key)
			if ok {
				*act = f
			}
			return d
		}
	}
	one := []archEntry{e}
	move := btn("archPvRestore", kit.Secondary, "Restore", "", func() { u.archBulk("restore", one) })
	if e.done {
		move = btn("archPvArchive", kit.Secondary, "Archive", "", func() { u.archBulk("archive", one) })
	}
	head := []layout.Widget{k.Lbl(c.Key(), 12, kit.CText3, kit.Mono), k.Pill(kind, kc, kit.Mix(kc, kit.CPanel, .85))}
	if c.Org != "" {
		head = append(head, k.Pill(c.Org, kit.CText3, kit.CPanelCard))
	}
	ws := []layout.Widget{
		func(gtx layout.Context) layout.Dimensions { return kit.Wrap(gtx, 8, head) },
		gap(8),
		func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, c.Title, 17, kit.CText, kit.Semi(kit.Sans), 0) },
		gap(4),
		k.Lbl("Finished "+c.FinishedAt().Format("Jan 2 2006, 15:04"), 12, kit.CText3, kit.Sans),
		gap(12),
		func(gtx layout.Context) layout.Dimensions {
			return kit.Wrap(gtx, 6, []layout.Widget{
				btn("archPvOpen", kit.Primary, "Open session", "↵", func() { u.openSession(c.ID) }),
				move,
				btn("archPvDelete", kit.Danger, "Delete", "Del", func() { u.archBulk("delete", one) }),
			})
		},
	}
	if len(c.Labels) > 0 || len(c.Links) > 0 {
		var chips []layout.Widget
		for _, l := range c.Labels {
			lc := labelColor(l)
			chips = append(chips, k.Pill(l, lc, kit.Mix(lc, kit.CPanel, .82)))
		}
		for _, l := range c.Links {
			chips = append(chips, u.linkChip(l))
		}
		ws = append(ws, gap(14), func(gtx layout.Context) layout.Dimensions { return kit.Wrap(gtx, 6, chips) })
	}
	if s := c.Session; s.Turns > 0 {
		ws = append(ws, gap(10), k.Lbl(fmt.Sprintf("%d turns · %s tokens · $%.2f", s.Turns, tokens(s.Tokens), s.Cost), 12, kit.CText3, kit.Mono))
	}
	if strings.TrimSpace(c.Notes) != "" {
		ws = append(ws, caption("NOTES"), func(gtx layout.Context) layout.Dimensions { return u.markdown(gtx, "archNotes"+c.ID, c.Notes) })
	}
	msgs := u.chat(c.ID)
	if n := len(msgs); n > 0 {
		from := max(0, n-archPreviewMsgs)
		ws = append(ws, caption(fmt.Sprintf("LAST MESSAGES · %d OF %d", n-from, n)))
		for i := from; i < n; i++ {
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Bottom: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return u.message(gtx, c.ID, fmt.Sprintf("arch%s-%d", c.ID, i), msgs[i], i > from && msgs[i-1].Who == msgs[i].Who)
				})
			})
		}
	}
	return kit.Boxed(kit.CPanel, kit.CLine, kit.RCard, layout.UniformInset(0), func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = gtx.Constraints.Max
		u.arch.prev.Axis = layout.Vertical
		return u.arch.prev.Layout(gtx, len(ws), func(gtx layout.Context, i int) layout.Dimensions {
			in := layout.Inset{Left: 18, Right: 18}
			if i == 0 {
				in.Top = 16
			}
			if i == len(ws)-1 {
				in.Bottom = 16
			}
			return in.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min = image.Point{}
					return ws[i](gtx)
				})
			})
		})
	})(gtx)
}
