package infra

import (
	"fmt"
	"image"
	"image/color"
	"time"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/lfdominguez/desk/internal/ui/kit"
)

// fetchText is the fetch box's title and the line under it.
func fetchText(name string, ss []connStep, now time.Time) (title, sub string) {
	if fetchFailed(ss) {
		return "Couldn't get " + name + "'s password", "Nothing was typed or copied"
	}
	since := now
	if len(ss) > 0 {
		since = ss[0].Start
	}
	return "Getting " + name + "'s password…", fmt.Sprintf("%ds", int(now.Sub(since).Seconds()))
}

// fetchOverlay is the password fetch's box over the pane: its steps as the
// connection box lists a dial's, the error once it failed.
func (u *ui) fetchOverlay(gtx layout.Context, area image.Rectangle, p guiPane, ss []connStep) {
	failed := fetchFailed(ss)
	if !failed {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(100 * time.Millisecond)}) // the running step's time
	}
	paint.FillShape(gtx.Ops, color.NRGBA{11, 12, 15, 0xc7}, clip.Rect(area).Op())
	title, sub := fetchText(p.Name, ss, gtx.Now)
	kids := []layout.FlexChild{
		layout.Rigid(u.lbl(title, 14, cText, fBold(sansFont))), layout.Rigid(vgap(6)),
		layout.Rigid(u.lbl(sub, 12, cText3, sansFont)),
	}
	if len(ss) > 0 {
		kids = append(kids, layout.Rigid(vgap(10)), layout.Rigid(u.connSteps(ss, gtx.Now)))
	}
	if failed {
		kids = append(kids, layout.Rigid(vgap(12)), layout.Rigid(boxed(cPane, cLine, rCard, layout.Inset{Top: 8, Bottom: 8, Left: 10, Right: 10}, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return u.label(gtx, ss[len(ss)-1].Err, 12, cRedText, monoFont, 4)
		})))
	}
	row := []layout.FlexChild{u.kb(pwButton("C", p.Term), kit.Ghost, map[bool]string{true: "Close", false: "Cancel"}[failed], "C", func() {
		p.Term.fetchHide()
		u.wantTerm = true
	}), spacer()}
	if failed {
		row = append(row, u.kb(pwButton("R", p.Term), kit.Primary, "Try again", "R", func() { u.retryFetch(p.Term) }))
	}
	kids = append(kids, layout.Rigid(vgap(12)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, row...)
	}))
	st := op.Offset(area.Min).Push(gtx.Ops)
	cgtx := gtx
	cgtx.Constraints = layout.Exact(area.Size())
	layout.Center.Layout(cgtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X-gtx.Dp(24), gtx.Dp(460))
		gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, 0)
		return boxed(cPanel, cRim, rSheet, layout.Inset{Top: 14, Bottom: 14, Left: 16, Right: 16}, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
		})(gtx)
	})
	st.Pop()
}

// fetchKey takes the pane's keys while its fetch box is up (running, or
// failed): C or esc close it, stopping a running fetch; R tries again; the rest goes nowhere, so
// nothing lands in the prompt meanwhile.
func (u *ui) fetchKey(p guiPane, text string, name key.Name) bool {
	ss, shown := p.Term.Fetch()
	if !shown {
		return false
	}
	failed := fetchFailed(ss)
	switch {
	case text == "c" || text == "C" || name == key.NameEscape:
		p.Term.fetchHide()
		u.wantTerm = true
	case failed && (text == "r" || text == "R"):
		u.retryFetch(p.Term)
	}
	return true
}

// pwButton is a fetch box's button id, the pane's own: two panes' boxes
// keep their clicks apart.
func pwButton(which string, t *Term) string { return fmt.Sprintf("pw%s%p", which, t) }

// retryFetch fetches term's password again, for its pane: not the focused
// one (alt+p's), which a click on another pane's box needn't be.
func (u *ui) retryFetch(term *Term) {
	u.d.act(func(m *model) {
		for _, t := range m.tabs {
			for _, pn := range t.panes {
				if pn.term == term {
					u.d.run(serverPasswordCmd(pn))
					return
				}
			}
		}
	})
}
