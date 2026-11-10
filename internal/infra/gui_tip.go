package infra

import (
	"image"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"github.com/lfdominguez/desk/internal/keys"
)

// tbTips is what each toolbar button does, with its key: the buttons are
// icons only. Read per call, so a rebinding shows.
func tbTips(id string) string {
	switch id {
	case "tbPane":
		return "New pane (" + keys.Short("infra.pane") + ")"
	case "tbBroadcast":
		return "Broadcast: type into every pane of the tab"
	case "tbFiles":
		return "Directory: this server's files beside this machine's (" + keys.Short("infra.files") + ")"
	case "tbGroup":
		return "Group view: every session of the group on one page (" + keys.Short("infra.group") + ")"
	case "tbClaude":
		return "Claude panel (" + keys.Short("infra.claude") + ")"
	}
	return ""
}

// tip shows s under button id (laid out as d) while the pointer is on it,
// over everything else, its right edge on the button's: the toolbar's
// buttons sit at the window's right. As kit.Tip, read after the button is
// laid out, so the hover is this frame's.
func (u *ui) tip(gtx layout.Context, id string, d layout.Dimensions, s string) {
	u.tipW(gtx, id, d, 360, 120, u.lbl(s, 11, cText, sansFont))
}

// tipW is tip with any content, up to w×h dp.
func (u *ui) tipW(gtx layout.Context, id string, d layout.Dimensions, w, h unit.Dp, content layout.Widget) {
	if !u.click(id).Hovered() {
		return
	}
	m := op.Record(gtx.Ops)
	tgtx := gtx
	tgtx.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(w), gtx.Dp(h))}
	td := boxed(cPanelCard, cRim, rCtl, layout.Inset{Top: 4, Bottom: 4, Left: 8, Right: 8}, content)(tgtx)
	call := m.Stop()
	m = op.Record(gtx.Ops)
	op.Offset(image.Pt(d.Size.X-td.Size.X, d.Size.Y+gtx.Dp(4))).Add(gtx.Ops)
	call.Add(gtx.Ops)
	op.Defer(gtx.Ops, m.Stop())
}
