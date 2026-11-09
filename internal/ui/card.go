package ui

import (
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// labelColor gives a label the same accent everywhere, from its name.
func labelColor(l string) color.NRGBA {
	h := fnv.New32a()
	h.Write([]byte(l))
	return kit.Accents[1+int(h.Sum32()%uint32(len(kit.Accents)-1))] // not slate: too close to the text
}

var prioNames = []string{"None", "Low", "Medium", "High"}

func prioColor(p int) color.NRGBA {
	return map[int]color.NRGBA{kanban.PrioLow: kit.CAccText, kanban.PrioMedium: kit.COrange, kanban.PrioHigh: kit.CRed}[p]
}

func toneColor(t string) color.NRGBA {
	return map[string]color.NRGBA{links.ToneGood: kit.CGreen, links.ToneWait: kit.CText2, links.ToneBad: kit.CRed, links.ToneDone: kit.CPurple}[t]
}

// dueText is how a due date reads on a card and its colors; done cards
// are never flagged late.
func dueText(c kanban.Card, now time.Time, done bool) (s string, fg, bg color.NRGBA, ok bool) {
	days, ok := c.DueIn(now)
	if !ok {
		return "", fg, bg, false
	}
	d, _ := time.ParseInLocation(kanban.DateLayout, c.Due, now.Location())
	s, fg, bg = d.Format("Jan 2"), kit.CText2, kit.CPanelCard
	switch {
	case done:
	case days < 0:
		s, fg, bg = "Overdue · "+s, kit.CRed, kit.Mix(kit.CRed, kit.CPanel, .82)
	case days == 0:
		s, fg, bg = "Today", kit.COrange, kit.COrangeSoft
	case days == 1:
		s, fg, bg = "Tomorrow", kit.COrange, kit.COrangeSoft
	case days < 7:
		s = d.Format("Mon")
	}
	return s, fg, bg, true
}

// ago is a card's age, the way the eye wants it: "now", "5 min. ago",
// "3 hr. ago", "yesterday", "Oct 20".
func ago(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%d min. ago", int(d.Minutes()))
	case d < 24*time.Hour && t.Day() == now.Day():
		return fmt.Sprintf("%d hr. ago", int(d.Hours()))
	case d < 48*time.Hour && t.Day() == now.AddDate(0, 0, -1).Day():
		return "yesterday"
	}
	return t.Format("Jan 2")
}

// tokens reads 782_700 as "782.7k" and 6_300_000 as "6.3M".
func tokens(n int) string {
	switch {
	case n >= 1_000_000:
		return strings.Replace(fmt.Sprintf("%.1fM", float64(n)/1e6), ".0M", "M", 1)
	case n >= 1000:
		return strings.Replace(fmt.Sprintf("%.1fk", float64(n)/1e3), ".0k", "k", 1)
	}
	return fmt.Sprint(n)
}

// statusLine is the card's state in one line: icon, words, color.
func statusLine(c kanban.Card) (kit.IconPath, string, color.NRGBA, bool) {
	note := func(head string) string {
		if c.Note == "" {
			return head
		}
		return head + ": " + c.Note
	}
	switch c.Status {
	case kanban.StatusWorking:
		return kit.IcoSpark, note("Working"), kit.CPurple, true
	case kanban.StatusWaiting:
		return kit.IcoChat, note("Your turn"), kit.CAccText, true
	case kanban.StatusBlocked:
		return kit.IcoWarn, note("Blocked by agent"), kit.COrange, true
	case kanban.StatusFailed:
		return kit.IcoWarn, note("Failed"), kit.CRed, true
	case kanban.StatusReview:
		return kit.IcoCheck, note("Ready for review"), kit.Accents[5], true
	case kanban.StatusDone:
		return kit.IcoCheck, note("Done"), kit.CGreen, true
	}
	return nil, "", color.NRGBA{}, false
}

// linkChip is a link as a pill: its ref and state, tinted by tone.
func (u *ui) linkChip(l kanban.Link) layout.Widget {
	k := u.k
	c := toneColor(l.Tone)
	if c.A == 0 {
		c = kit.CText3
	}
	ico := kit.IcoLink
	switch l.Tone {
	case links.ToneGood, links.ToneDone:
		ico = kit.IcoCheck
	case links.ToneWait:
		ico = kit.IcoClock
	case links.ToneBad:
		ico = kit.IcoWarn
	}
	s := l.Ref
	if l.State != "" {
		s += " · " + l.State
	}
	return kit.Boxed(kit.Mix(c, kit.CPanel, .85), color.NRGBA{}, 3, layout.Inset{Top: 2, Bottom: 2, Left: 5, Right: 7}, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(kit.Icon(11, c, ico)),
			layout.Rigid(kit.HGap(5)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, s, 11, c, kit.Semi(kit.Sans), 1) }))
	})
}

// cardBody draws card c, top down: key, status dot, org and age; title;
// status line; links; the last message; pills and session numbers. A
// priority stripe runs down its left edge.
func (u *ui) cardBody(gtx layout.Context, c kanban.Card, sel, hover, done bool) layout.Dimensions {
	k, now := u.k, time.Now()
	m := op.Record(gtx.Ops)
	d := layout.Inset{Top: 9, Bottom: 10, Left: 14, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		gap := func(dp unit.Dp) layout.FlexChild { return layout.Rigid(kit.VGap(dp)) }
		line := func(ico kit.IconPath, ic color.NRGBA, s string, sc color.NRGBA, lines int) layout.FlexChild {
			return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Top: 1, Right: 7}.Layout(gtx, kit.Icon(12, ic, ico))
					}),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return k.Label(gtx, s, 12, sc, kit.Medium(kit.Sans), lines)
					}))
			})
		}
		kids := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			row := []layout.FlexChild{layout.Rigid(k.Lbl(c.Key(), 11, kit.CText3, kit.Mono))}
			if sc, ok := statusColor[c.Status]; ok {
				row = append(row, layout.Rigid(kit.HGap(6)), layout.Rigid(kit.Dot(sc)))
			}
			if c.Org != "" {
				row = append(row, layout.Rigid(kit.HGap(8)), layout.Rigid(k.Pill(c.Org, kit.CText3, kit.CPanelCard)))
			}
			if t, ok := u.cardTask(c.Key()); ok { // server work handed to the org's infra Claude
				row = append(row, layout.Rigid(kit.HGap(6)), layout.Rigid(k.Pill("⇄ "+t.Org+" infra", kit.COrange, kit.Mix(kit.COrange, kit.CPanel, .84))))
			}
			row = append(row, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)}
			}),
				layout.Rigid(k.Lbl(ago(c.Updated, now), 11, kit.CText3, kit.Medium(kit.Sans))))
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, row...)
		}), gap(4)}
		fg := kit.CText
		if done {
			fg = kit.CText2
		}
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return k.Label(gtx, c.Title, 13, fg, kit.Semi(kit.Sans), 3)
		}))
		if ico, s, sc, ok := statusLine(c); ok {
			kids = append(kids, gap(5), line(ico, sc, s, sc, 1))
		}
		for n, l := range c.Links {
			if n == 3 { // ponytail: past three links the card says so; the panel lists them all
				kids = append(kids, gap(4), layout.Rigid(k.Lbl(fmt.Sprintf("+%d more links", len(c.Links)-3), 11, kit.CText3, kit.Sans)))
				break
			}
			kids = append(kids, gap(4), layout.Rigid(u.linkChip(l)))
		}
		if c.Last != "" {
			kids = append(kids, gap(5), line(kit.IcoChat, kit.CText3, strings.Join(strings.Fields(c.Last), " "), kit.CText2, 2))
		} else if c.Notes != "" {
			kids = append(kids, gap(3), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return k.Label(gtx, c.Notes, 12, kit.CText3, kit.Sans, 2)
			}))
		}
		var foot []layout.FlexChild
		for n, l := range c.Labels {
			if n == 3 { // ponytail: pills don't wrap; past three they fold into +n
				foot = append(foot, layout.Rigid(k.Pill(fmt.Sprintf("+%d", len(c.Labels)-3), kit.CText3, kit.CPanelCard)), layout.Rigid(kit.HGap(4)))
				break
			}
			lc := labelColor(l)
			foot = append(foot, layout.Rigid(k.Pill(l, lc, kit.Mix(lc, kit.CPanel, .82))), layout.Rigid(kit.HGap(4)))
		}
		if s, fg, bg, ok := dueText(c, now, done); ok {
			foot = append(foot, layout.Rigid(kit.Boxed(bg, color.NRGBA{}, 3, layout.Inset{Top: 1, Bottom: 1, Left: 5, Right: 6}, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(kit.Icon(11, fg, kit.IcoClock)),
					layout.Rigid(kit.HGap(4)),
					layout.Rigid(k.Lbl(s, 11, fg, kit.Semi(kit.Sans))))
			})))
		}
		if s := c.Session; s.Turns > 0 {
			foot = append(foot, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)}
			}),
				layout.Rigid(kit.Icon(11, kit.CText3, kit.IcoStar)), layout.Rigid(kit.HGap(4)),
				layout.Rigid(k.Lbl(fmt.Sprintf("%d   %s tokens · $%.2f", s.Turns, tokens(s.Tokens), s.Cost), 11, kit.CText3, kit.Medium(kit.Sans))))
		}
		if len(foot) > 0 {
			kids = append(kids, gap(8), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx, foot...)
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})
	call := m.Stop()

	r := image.Rectangle{Max: d.Size}
	rim := kit.CRim
	switch {
	case sel:
		rim = kit.CAcc
	case hover:
		rim = kit.CIdle
	}
	fill := kit.CPanel
	if hover || sel {
		fill = kit.Mix(kit.CPanel, kit.CHover, .5)
	}
	kit.Panel(gtx, r, kit.RCard, fill, rim)
	if c.Priority != kanban.PrioNone {
		in := r.Inset(gtx.Dp(1))
		s := clip.UniformRRect(in, gtx.Dp(kit.RCard)-gtx.Dp(1)).Push(gtx.Ops)
		paint.FillShape(gtx.Ops, prioColor(c.Priority), clip.Rect(image.Rect(in.Min.X, in.Min.Y, in.Min.X+gtx.Dp(3), in.Max.Y)).Op())
		s.Pop()
	}
	call.Add(gtx.Ops)
	return d
}
