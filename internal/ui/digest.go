package ui

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"gioui.org/io/clipboard"
	"gioui.org/layout"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// The Digest (the sidebar's Digest, under All cards): what was done and
// what is in flight, by organization, as a standup reads it, from the
// board alone. A [[trigger]] on = "digest" sends an organization's to the
// phone.

const viewDigest = "digest"

// digestSpans are the Digest's choices of since when.
var digestSpans = []string{"Today", "Since yesterday", "Last 7 days"}

// digestSince is when span (digestSpans' index) starts, from now.
func digestSince(span int, now time.Time) time.Time {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return day.AddDate(0, 0, -[]int{0, 1, 7}[span])
}

// digestDone is org's cards ("" every organization's) finished since,
// the archived ones too, oldest first.
func (u *ui) digestDone(org string, since time.Time) []kanban.Card {
	var out []kanban.Card
	cands := slices.Clone(u.b.Archive)
	if d := u.b.Column("Done"); d >= 0 {
		cands = append(cands, u.b.Columns[d].Cards...)
	}
	for _, c := range cands {
		if (org == "" || c.Org == org) && !c.FinishedAt().Before(since) {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b kanban.Card) int { return a.FinishedAt().Compare(b.FinishedAt()) })
	return out
}

// digestText is the digest of org ("" all) since then, as Markdown.
func (u *ui) digestText(org string, since time.Time) string {
	line := func(c kanban.Card) string {
		var refs []string
		for _, l := range c.Links {
			if l.Ref != "" {
				refs = append(refs, l.Ref)
			}
		}
		s := "- " + c.Key() + " " + c.Title
		if len(refs) > 0 {
			s += " · " + strings.Join(refs, ", ")
		}
		if c.Session.Work > 0 {
			s += " · " + links.Spent(time.Duration(c.Session.Work)*time.Second)
		}
		if c.Status == kanban.StatusBlocked && c.Note != "" {
			s += " — " + c.Note
		}
		return s + "\n"
	}
	by := map[string]map[string][]kanban.Card{} // org → part → cards
	add := func(part string, c kanban.Card) {
		if by[c.Org] == nil {
			by[c.Org] = map[string][]kanban.Card{}
		}
		by[c.Org][part] = append(by[c.Org][part], c)
	}
	for _, c := range u.digestDone(org, since) {
		add("Done", c)
	}
	for _, col := range []string{"Review", "Blocked", "In progress"} {
		i := u.b.Column(col)
		if i < 0 {
			continue
		}
		for _, c := range u.b.Columns[i].Cards {
			if (org == "" || c.Org == org) && !c.Updated.Before(since) && c.Parent == "" {
				add(col, c)
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**Digest since %s**\n", since.Format("Mon Jan 2"))
	for _, o := range slices.Sorted(maps.Keys(by)) {
		fmt.Fprintf(&b, "\n### %s\n", cmpName(o))
		for _, part := range [][2]string{{"Done", "Done"}, {"Review", "In review"}, {"Blocked", "Blocked"}, {"In progress", "In progress"}} {
			if cs := by[o][part[0]]; len(cs) > 0 {
				fmt.Fprintf(&b, "\n**%s**\n", part[1])
				for _, c := range cs {
					b.WriteString(line(c))
				}
			}
		}
	}
	if len(by) == 0 {
		b.WriteString("\nNothing done or moving since then.\n")
	}
	return b.String()
}

// cmpName is an organization's name in the digest, "" being none.
func cmpName(org string) string {
	if org == "" {
		return "No organization"
	}
	return org
}

// digestView is the Digest: the span, Copy, and the text.
func (u *ui) digestView(gtx layout.Context) layout.Dimensions {
	k := u.k
	gtx.Constraints.Min = gtx.Constraints.Max
	kit.Fill(gtx, kit.CPane)
	text := u.digestText("", digestSince(u.dgSpan, time.Now()))
	head := func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.choice(gtx, "dgSpan", digestSpans, u.dgSpan, func(int) color.NRGBA { return color.NRGBA{} }, func(i int) { u.dgSpan = i })
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)} }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d, ok := k.Button(gtx, "dgCopy", kit.CPanelCard, kit.CHover, kit.RCtl, layout.Inset{Top: 6, Bottom: 6, Left: 12, Right: 12}, k.Lbl("Copy", 13, kit.CText, kit.Semi(kit.Sans)))
				if ok {
					gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(bytes.NewReader([]byte(text)))})
					u.showToast("Digest copied", false)
				}
				return d
			}))
	}
	rows := []layout.Widget{head, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return u.markdown(gtx, "digest", text) })
	}}
	w := min(gtx.Constraints.Max.X-gtx.Dp(48), gtx.Dp(820))
	return layout.Inset{Top: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.N.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, gtx.Constraints.Max.Y)}
			return u.dgList.Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions { return rows[i](gtx) })
		})
	})
}
