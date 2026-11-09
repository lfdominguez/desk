package ui

import (
	"errors"
	"image"
	"math"
	"slices"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/draw"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// Suggestions (drawing_suggest) wait beside the drawing (drawSidecar) and
// float over the canvas as ghosts in their Claude's color: what they add
// or change drawn see-through as it would be, what they delete crossed
// out. A card for each, bottom right, approves (one undo step), sends it
// back with a comment (Rework) or dismisses it.

// drawGhost is a suggestion as drawn over the drawing as it is now.
type drawGhost struct {
	sg    drawSuggestion
	after []*draw.Element // what it adds or changes, as it would be (themed, see-through)
	gone  []*draw.Element // what it deletes
	err   string          // why it no longer applies, "" it does
}

// ghostID keeps a ghost's picture apart from its element's (rasters are by id).
const ghostID = "ghost:"

// drawGhosts are st's suggestions over its drawing now, worked out again
// when either changes.
func (st *drawState) drawGhosts() []drawGhost {
	if st.ghostGen == st.ed.Gen && st.ghosts != nil {
		return st.ghosts
	}
	st.ghostGen, st.ghosts = st.ed.Gen, []drawGhost{}
	for _, sg := range st.side.Suggestions {
		g := drawGhost{sg: sg}
		c, ids, err := draw.Preview(st.ed.Scene, sg.Patch)
		if err != nil {
			g.err = err.Error()
			st.ghosts = append(st.ghosts, g)
			continue
		}
		touched := map[string]bool{}
		for _, id := range ids {
			touched[id] = true
		}
		c = themed(c, nil)
		for _, e := range c.Elements {
			if e.IsDeleted || !(touched[e.ID] || (e.ContainerID != nil && touched[*e.ContainerID])) {
				continue
			}
			t := *e
			t.ID, t.Opacity = ghostID+sg.ID+":"+e.ID, e.Opacity*.5
			g.after = append(g.after, &t)
		}
		for _, id := range sg.Patch.Delete {
			if e := st.ed.Scene.Find(id); e != nil {
				g.gone = append(g.gone, e)
			}
		}
		st.ghosts = append(st.ghosts, g)
	}
	return st.ghosts
}

// paintGhosts draws the suggestions over the scene (under the scene's
// transform, s pixels a unit), each outlined in its Claude's color.
func (st *drawState) paintGhosts(gtx layout.Context, s float32) {
	used := map[string]bool{}
	sc := &draw.Scene{AppState: st.shown.AppState, Files: st.ed.Scene.Files}
	for _, g := range st.drawGhosts() {
		c := labelColor(g.sg.Name)
		for _, e := range g.after {
			used[e.ID] = true
			st.paintRaster(gtx.Ops, sc, e, s)
		}
		w := 1.5 / s * gtx.Metric.PxPerDp
		for _, e := range g.gone {
			x0, y0, x1, y1 := e.Bounds()
			for _, l := range [][2]f32.Point{{f32.Pt(float32(x0), float32(y0)), f32.Pt(float32(x1), float32(y1))}, {f32.Pt(float32(x1), float32(y0)), f32.Pt(float32(x0), float32(y1))}} {
				var p clip.Path
				p.Begin(gtx.Ops)
				p.MoveTo(l[0])
				p.LineTo(l[1])
				paint.FillShape(gtx.Ops, c, clip.Stroke{Path: p.End(), Width: w}.Op())
			}
		}
	}
	for id := range st.rasters {
		if strings.HasPrefix(id, ghostID) && !used[id] {
			delete(st.rasters, id)
		}
	}
}

// ghostBox is a ghost's elements' box in pane pixels, empty when it has none.
func (st *drawState) ghostBox(g drawGhost, px float32) image.Rectangle {
	var r image.Rectangle
	for _, e := range append(slices.Clone(g.after), g.gone...) {
		x0, y0, x1, y1 := e.Bounds()
		pad := float64(6 / st.zoom)
		a, b := st.toPane(draw.Pt{X: x0 - pad, Y: y0 - pad}, px), st.toPane(draw.Pt{X: x1 + pad, Y: y1 + pad}, px)
		r = r.Union(image.Rect(int(a.X), int(a.Y), int(math.Ceil(float64(b.X))), int(math.Ceil(float64(b.Y)))))
	}
	return r
}

// drawGhostMarks outlines each suggestion and names it, in pane pixels.
func (u *ui) drawGhostMarks(gtx layout.Context, st *drawState, px float32) {
	for _, g := range st.drawGhosts() {
		r := st.ghostBox(g, px)
		if r.Empty() {
			continue
		}
		c := labelColor(g.sg.Name)
		paint.FillShape(gtx.Ops, c, clip.Stroke{Path: clip.UniformRRect(r, int(3*px)).Path(gtx.Ops), Width: px}.Op())
		u.drawTag(gtx, r.Min, "Suggestion · "+g.sg.Name, c, false)
	}
}

// drawSuggestCards are the suggestions' cards, bottom right of the canvas.
func (u *ui) drawSuggestCards(gtx layout.Context, st *drawState, size image.Point, act *func()) {
	gs := st.drawGhosts()
	if len(gs) == 0 {
		return
	}
	k := u.k
	w, pad := gtx.Dp(300), gtx.Dp(12)
	g := gtx
	g.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, size.Y)}
	m := op.Record(gtx.Ops)
	var kids []layout.FlexChild
	for i, gh := range gs {
		gh := gh
		if i > 0 {
			kids = append(kids, layout.Rigid(kit.VGap(8)))
		}
		kids = append(kids, layout.Rigid(kit.Boxed(kit.CPanel, kit.CRim, kit.RCard, layout.UniformInset(12), func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			id := gh.sg.ID
			rows := []layout.FlexChild{
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							r := gtx.Dp(4)
							paint.FillShape(gtx.Ops, labelColor(gh.sg.Name), clip.Ellipse(image.Rect(0, 0, 2*r, 2*r)).Op(gtx.Ops))
							return layout.Dimensions{Size: image.Pt(2*r, 2*r)}
						}),
						layout.Rigid(kit.HGap(8)),
						layout.Flexed(1, k.LblFill(gh.sg.Name, 12, kit.CText2, kit.Semi(kit.Sans))),
						layout.Rigid(k.Lbl(ago(gh.sg.At, time.Now()), 11, kit.CText3, kit.Sans)))
				}),
				layout.Rigid(kit.VGap(6)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return k.Label(gtx, gh.sg.Note, 13, kit.CText, kit.Sans, 0)
				}),
			}
			if gh.err != "" {
				rows = append(rows, layout.Rigid(kit.VGap(6)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return k.Label(gtx, "It no longer fits the drawing: "+gh.err, 12, kit.COrange, kit.Sans, 0)
				}))
			}
			if gh.sg.Sent {
				rows = append(rows, layout.Rigid(kit.VGap(6)), layout.Rigid(k.Lbl("Sent back: waiting for a new one", 12, kit.CText3, kit.Sans)))
			}
			if st.rework == id {
				rows = append(rows, layout.Rigid(kit.VGap(8)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.drawReworkBox(gtx, st, gh.sg, act)
				}))
			} else {
				rows = append(rows, layout.Rigid(kit.VGap(10)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					var btns []layout.FlexChild
					if gh.err == "" {
						btns = append(btns, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							d, ok := k.KBtn(gtx, "sgOK"+id, kit.Primary, "Approve", "")
							if ok {
								*act = func() { u.drawApprove(st, id) }
							}
							return d
						}), layout.Rigid(kit.HGap(6)))
					}
					if drawCanRework(gh.sg.Who) {
						btns = append(btns, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							d, ok := k.KBtn(gtx, "sgRe"+id, kit.Secondary, "Rework", "")
							if ok {
								*act = func() { st.rework = id; st.reworkIn.SetText(""); u.focusLater = &st.reworkIn }
							}
							return d
						}), layout.Rigid(kit.HGap(6)))
					}
					btns = append(btns, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d, ok := k.KBtn(gtx, "sgNo"+id, kit.Ghost, "Dismiss", "")
						if ok {
							*act = func() { u.drawDismiss(st, id) }
						}
						return d
					}))
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx, btns...)
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
		})))
	}
	d := layout.Flex{Axis: layout.Vertical}.Layout(g, kids...)
	call := m.Stop()
	defer op.Offset(image.Pt(size.X-w-pad, max(size.Y-d.Size.Y-pad, pad))).Push(gtx.Ops).Pop()
	block := clip.Rect{Max: d.Size}.Push(gtx.Ops) // a press on a card isn't the canvas's
	event.Op(gtx.Ops, &st.rework)
	block.Pop()
	call.Add(gtx.Ops)
}

var (
	errNoDrawing = errors.New("the drawing is closed")
	errNoCard    = errors.New("the card is gone from the board")
)

// drawReworkBox is the comment that sends suggestion sg back.
func (u *ui) drawReworkBox(gtx layout.Context, st *drawState, sg drawSuggestion, act *func()) layout.Dimensions {
	k := u.k
	for {
		ev, ok := st.reworkIn.Update(gtx)
		if !ok {
			break
		}
		if s, ok := ev.(widget.SubmitEvent); ok {
			text := s.Text
			*act = func() { u.drawRework(st, sg.ID, text) }
		}
	}
	if u.focusLater == &st.reworkIn {
		gtx.Execute(key.FocusCmd{Tag: &st.reworkIn})
		u.focusLater = nil
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(kit.Boxed(kit.CPane, kit.CAcc, kit.RCtl, layout.Inset{Top: 8, Bottom: 8, Left: 10, Right: 10}, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			gtx.Constraints.Max.Y = gtx.Dp(120)
			return k.Editor(gtx, &st.reworkIn, "What should change?", 13, kit.Sans, kit.CText)
		})),
		layout.Rigid(kit.VGap(8)),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.KBtn(gtx, "sgSend"+sg.ID, kit.Primary, "Send back", "")
					if ok {
						text := st.reworkIn.Text()
						*act = func() { u.drawRework(st, sg.ID, text) }
					}
					return d
				}),
				layout.Rigid(kit.HGap(6)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.KBtn(gtx, "sgCancel"+sg.ID, kit.Ghost, "Cancel", "")
					if ok {
						*act = func() { st.rework = "" }
					}
					return d
				}))
		}))
}

// drawCanRework says whether who's suggestions can go back to it: not an
// infra Claude's, which desk has no way to write to.
func drawCanRework(who string) bool { return !strings.HasPrefix(who, "infra:") }

// drawTake removes suggestion id from st, kept on disk; it says which it was.
func (u *ui) drawTake(st *drawState, id string) (drawSuggestion, bool) {
	i := slices.IndexFunc(st.side.Suggestions, func(s drawSuggestion) bool { return s.ID == id })
	if i < 0 {
		return drawSuggestion{}, false
	}
	sg := st.side.Suggestions[i]
	st.side.Suggestions = slices.Delete(slices.Clone(st.side.Suggestions), i, i+1)
	u.drawKeepSide(st)
	st.ed.Gen++
	return sg, true
}

// drawApprove applies suggestion id as one undo step, marked as its Claude's.
func (u *ui) drawApprove(st *drawState, id string) {
	i := slices.IndexFunc(st.side.Suggestions, func(s drawSuggestion) bool { return s.ID == id })
	if i < 0 {
		return
	}
	sg := st.side.Suggestions[i]
	st.ed.EndText()
	ids, err := st.ed.Apply(sg.Patch)
	if err != nil {
		u.showToast("The suggestion no longer fits: "+err.Error(), true)
		return
	}
	u.drawTake(st, id)
	st.mark(ids, sg.Name)
}

// drawDismiss drops suggestion id.
func (u *ui) drawDismiss(st *drawState, id string) { u.drawTake(st, id) }

// drawRework sends suggestion id back to its Claude with the user's
// comment; it waits, marked sent, for the one that replaces it.
func (u *ui) drawRework(st *drawState, id, comment string) {
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return
	}
	i := slices.IndexFunc(st.side.Suggestions, func(s drawSuggestion) bool { return s.ID == id })
	if i < 0 {
		return
	}
	sg := st.side.Suggestions[i]
	msg := "The user sent back your suggestion for the drawing \"" + drawName(st.path) + "\" (" + sg.Note + ") with:\n" + comment +
		"\nRead the drawing and suggest again with drawing_suggest: your new one replaces it."
	if err := u.drawTellBack(sg.Who, msg); err != nil {
		u.showToast("Couldn't send it back: "+err.Error(), true)
		return
	}
	st.side.Suggestions = slices.Clone(st.side.Suggestions)
	st.side.Suggestions[i].Sent = true
	u.drawKeepSide(st)
	st.rework = ""
	st.ed.Gen++
}

// drawTellBack brings msg to the Claude who: a drawing's, a card's
// workspace Claude, a card's own (held as the infra Claude's are while it
// can't take it).
func (u *ui) drawTellBack(who, msg string) error {
	switch {
	case strings.HasPrefix(who, "draw:"):
		p := strings.TrimPrefix(who, "draw:")
		st := u.draws[p]
		if st == nil {
			return errNoDrawing
		}
		if err := u.drawAsk(st, msg); err != nil {
			return err
		}
		u.say(drawChatKey(p), "you", msg)
	case strings.HasPrefix(who, "ws:"):
		id := strings.TrimPrefix(who, "ws:")
		if err := u.wsAsk(id, msg, "you"); err != nil {
			return err
		}
		u.say(wsKey(id), "you", msg)
	default:
		if u.b.Card(who) == nil {
			return errNoCard
		}
		u.say(who, "you", msg)
		if u.terms[who] != nil || u.shellStop[who] != nil || u.asks[who] != nil {
			u.peerOut[who] += msg + "\n\n"
			return nil
		}
		u.deliver(who, msg, true)
	}
	return nil
}
