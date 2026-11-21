package ui

import (
	"fmt"
	"image"
	"image/color"
	"path/filepath"
	"strings"

	"gioui.org/layout"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// reviewView is the rv:<id> view: the card's branch against its base (its
// state, commits and each changed file as a diff) under the actions that
// ship it. Clicks run after the frame.
func (u *ui) reviewView(gtx layout.Context) layout.Dimensions {
	k := u.k
	id := strings.TrimPrefix(u.view, "rv:")
	gtx.Constraints.Min = gtx.Constraints.Max
	kit.Fill(gtx, kit.CPane)
	c := u.b.Card(id)
	if c == nil || c.Worktree == "" {
		return layout.Center.Layout(gtx, k.Lbl("This card has no branch of its own to review.", 13, kit.CText3, kit.Sans))
	}
	st := u.rvOf(id)
	var act func()
	defer func() {
		if act != nil {
			act()
		}
	}()
	btn := func(bid, text string, primary bool, f func()) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			bg, fg := kit.CPanelCard, kit.CText
			if primary {
				bg, fg = kit.CAcc, kit.CWhite
			}
			d, ok := k.Button(gtx, bid+id, bg, kit.Mix(bg, kit.CText, .12), kit.RCtl, layout.Inset{Top: 6, Bottom: 6, Left: 12, Right: 12}, k.Lbl(text, 13, fg, kit.Semi(kit.Sans)))
			if ok && st.busy == "" {
				act = f
			}
			return d
		}
	}
	merge := "Merge into " + c.Base
	if st.armed == "merge" {
		merge = "Confirm: fast-forward " + c.Base
	}
	head := func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(k.Lbl(c.Branch+" → "+c.Base, 15, kit.CText, kit.Semi(kit.Mono))),
			layout.Rigid(kit.HGap(10)),
			layout.Rigid(k.Pill(fmt.Sprintf("%d ahead · %d behind", st.st.Ahead, st.st.Behind), kit.CText2, kit.CPanel)),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)} }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d, ok := k.IconBtn(gtx, "rvReload"+id, kit.IcoReload, false)
				k.Tip(gtx, "rvReload"+id, d, "Read the branch again", false)
				if ok {
					act = func() { u.rvLoad(id) }
				}
				return d
			}),
			layout.Rigid(kit.HGap(8)),
			layout.Rigid(btn("rvPush", "Push", false, func() { u.rvAct(id, "push") })),
			layout.Rigid(kit.HGap(6)),
			layout.Rigid(btn("rvPR", "Open request", false, func() { u.rvAct(id, "pr") })),
			layout.Rigid(kit.HGap(6)),
			layout.Rigid(btn("rvMerge", merge, true, func() { u.rvAct(id, "merge") })))
	}
	note := func(s string, col color.NRGBA) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, s, 12, col, kit.Sans, 4) })
		}
	}
	rows := []layout.Widget{head}
	switch {
	case st.busy != "":
		rows = append(rows, note(st.busy, kit.CText2))
	case st.armed == "merge":
		rows = append(rows, note("Click again to fast-forward "+c.Base+" to "+c.Branch+" in the main checkout. The card then goes to Done.", kit.COrange))
	case st.loading:
		rows = append(rows, note("Reading the branch…", kit.CText3))
	}
	if st.err != "" {
		rows = append(rows, note(st.err, kit.CRed))
	}
	if st.st.Merged && st.st.Behind == 0 && st.err == "" {
		rows = append(rows, note(c.Base+" has every commit of "+c.Branch+".", kit.CGreen))
	}
	if n := len(st.st.Dirty); n > 0 {
		rows = append(rows, note(fmt.Sprintf("%d changes not committed: below, but left out of a merge or push until committed.", n), kit.COrange))
	}
	for _, cm := range st.st.Commits {
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(k.Lbl(cm.Hash, 12, kit.CText3, kit.Mono)),
					layout.Rigid(kit.HGap(8)),
					layout.Flexed(1, k.LblFill(cm.Subject, 13, kit.CText, kit.Sans)))
			})
		})
	}
	for _, f := range st.files {
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			path := filepath.Join(st.root, f.Path)
			if f.Binary {
				return note(f.Path+": binary, not shown", kit.CText3)(gtx)
			}
			m := kanban.ChatMsg{Path: path, Edits: []kanban.Edit{{Old: f.Old, New: f.New}}}
			return layout.Inset{Top: 10}.Layout(gtx, kit.Boxed(kit.CPanel, kit.CLine, kit.RCard, layout.Inset{}, func(gtx layout.Context) layout.Dimensions {
				return u.diffBlock(gtx, id, fmt.Sprint("rv:", id, ":", st.gen, ":", f.Path), m)
			}))
		})
	}
	if !st.loading && st.err == "" && len(st.files) == 0 && len(st.st.Commits) == 0 {
		rows = append(rows, note("Nothing changed against "+c.Base+" yet.", kit.CText3))
	}
	w := min(gtx.Constraints.Max.X-gtx.Dp(48), gtx.Dp(980))
	return layout.Inset{Top: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.N.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, gtx.Constraints.Max.Y)}
			return st.list.Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions { return rows[i](gtx) })
		})
	})
}
