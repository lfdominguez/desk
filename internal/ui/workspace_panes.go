package ui

import (
	"image"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// The workspace's side panes, the tree and the chat, are as wide as the user
// drags their edges, for every card (the settings' [editor]). x/component's
// Resize doesn't fit: it splits two widgets by a ratio of the whole, and
// these are three panes whose sides keep their width as the window changes;
// this is infra's resizeGrip (gui_chat.go) for two edges.

// The panes' widths in dp: the default, and the bounds a drag keeps to.
const (
	wsTreeDef, wsTreeMin, wsTreeMax = 260, 160, 480
	wsChatDef, wsChatMin, wsChatMax = 400, 300, 720
	wsCenterMin                     = 320 // the editor's least, before the sides give way
	wsGripDp                        = 6   // the width an edge can be grabbed by, over its 1dp line
)

// wsGrip is a pane edge being dragged.
type wsGrip struct {
	dragging bool
	startAt  float32 // where the press was, in a frame that doesn't move with the edge (px)
	startW   float32 // the pane's width then (dp)
}

// wsSide is one pane's width in dp: the user's, else the default, in bounds.
func wsSide(w, def, lo, hi int) int {
	if w == 0 {
		return def
	}
	return min(max(w, lo), hi)
}

// wsFit is the tree's and chat's widths (px) in a view total px wide: the
// chat gives way first, down to its least, then the tree, so the editor keeps
// center px while it can.
func wsFit(total, tree, chat, treeMin, chatMin, center int) (int, int) {
	over := tree + chat + center - total
	if over > 0 {
		d := min(over, max(chat-chatMin, 0))
		chat, over = chat-d, over-d
	}
	if over > 0 {
		tree -= min(over, max(tree-treeMin, 0))
	}
	return tree, chat
}

// wsPanes are the tree's and the chat's widths for gtx's width, in px.
func (u *ui) wsPanes(gtx layout.Context) (tree, chat int) {
	return wsFit(gtx.Constraints.Max.X-2*gtx.Dp(1),
		gtx.Dp(unit.Dp(wsSide(u.wsEd.Tree, wsTreeDef, wsTreeMin, wsTreeMax))), gtx.Dp(unit.Dp(wsSide(u.wsEd.Chat, wsChatDef, wsChatMin, wsChatMax))),
		gtx.Dp(wsTreeMin), gtx.Dp(wsChatMin), gtx.Dp(wsCenterMin))
}

// wsEdge is the 1dp line between two panes, grabbable: dragging it sets
// pane i's width (0 the tree, at its right; 1 the chat, at its left), shown
// w px now. The width is saved on release.
func (u *ui) wsEdge(gtx layout.Context, i, w int) layout.Dimensions {
	g, side := &u.wsGrips[i], float32(1-2*i)                     // the tree grows rightwards, the chat leftwards
	at := func(x float32) float32 { return x + side*float32(w) } // the pointer in a frame the edge doesn't move
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: g, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
		if !ok {
			break
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch e.Kind {
		case pointer.Press:
			g.dragging, g.startAt, g.startW = true, at(e.Position.X), float32(w)/gtx.Metric.PxPerDp
		case pointer.Drag:
			if g.dragging {
				u.wsSetSide(i, int(g.startW+side*(at(e.Position.X)-g.startAt)/gtx.Metric.PxPerDp+.5))
			}
		case pointer.Release, pointer.Cancel:
			if g.dragging {
				u.wsSaveEditor()
			}
			g.dragging = false
		}
	}
	h := gtx.Constraints.Max.Y
	paint.FillShape(gtx.Ops, kit.CLine, clip.Rect(image.Rect(0, 0, gtx.Dp(1), h)).Op())
	half := gtx.Dp(wsGripDp) / 2
	m := op.Record(gtx.Ops) // deferred: over the panes on both sides, the next one laid out after it
	st := clip.Rect(image.Rect(-half, 0, gtx.Dp(1)+half, h)).Push(gtx.Ops)
	event.Op(gtx.Ops, g)
	pointer.CursorColResize.Add(gtx.Ops)
	st.Pop()
	op.Defer(gtx.Ops, m.Stop())
	return layout.Dimensions{Size: image.Pt(gtx.Dp(1), h)}
}

// wsSetSide sets pane i's width in dp, in its bounds.
func (u *ui) wsSetSide(i, w int) {
	if i == 0 {
		u.wsEd.Tree = min(max(w, wsTreeMin), wsTreeMax)
	} else {
		u.wsEd.Chat = min(max(w, wsChatMin), wsChatMax)
	}
}

// wsSaveEditor keeps the editor settings (the panes' widths, wrap).
func (u *ui) wsSaveEditor() {
	if err := orgs.SetEditor(u.wsEd); err != nil {
		u.showToast("The workspace's layout wasn't saved: "+err.Error(), true)
	}
}
