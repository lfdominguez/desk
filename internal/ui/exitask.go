package ui

import (
	"fmt"
	"image"
	"strings"

	"gioui.org/app"
	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// exitAsk is the question asked before desk closes while cards' Claudes
// are at work; act is the answer, run after the dialog is drawn.
type exitAsk struct {
	act func()
}

// exitShown is how many busy cards the dialog names before "and N more".
const exitShown = 4

// running is the cards with something running: a Claude turn, its
// background work or a "!" command. Working is reset when desk starts, so
// it is never left over from an earlier run.
func (u *ui) running() (out []kanban.Card) {
	for _, col := range u.b.Columns {
		for _, c := range col.Cards {
			if c.Status == kanban.StatusWorking {
				out = append(out, c)
			}
		}
	}
	return out
}

// closeAsked is the main window's close request (its close button, the
// compositor's close key): with cards at work it waits for the user's
// answer instead. A kill (SIGTERM, Ctrl+C) doesn't come here.
func (u *ui) closeAsked(e *app.ClosingEvent) {
	if u.exitOK || len(u.running()) == 0 {
		return
	}
	e.Abort()
	if u.exit == nil {
		u.exit, u.blur = &exitAsk{}, true // no editor keeps the keys meanwhile
		u.w.Invalidate()
	}
}

// exitAnyway closes the window for good: the user said so.
func (u *ui) exitAnyway() {
	u.exit, u.exitOK = nil, true
	closeWindow(u.w)
}

// exitKeys are the dialog's: esc stays, enter exits. They take every other key off.
func (u *ui) exitKeys(gtx layout.Context) {
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameEscape}, key.Filter{Name: key.NameReturn}, key.Filter{Name: key.NameEnter})
		if !ok {
			return
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			if e.Name == key.NameEscape {
				u.exit = nil
			} else {
				u.exitAnyway()
			}
			return
		}
	}
}

// exitDialog draws the question while it's asked.
func (u *ui) exitDialog(gtx layout.Context) {
	d := u.exit
	if d == nil {
		return
	}
	k, busy := u.k, u.running()
	if len(busy) == 0 { // they finished meanwhile: nothing left to ask
		u.exit = nil
		return
	}
	names := make([]string, 0, exitShown)
	for i, c := range busy {
		if i == exitShown {
			names = append(names, fmt.Sprintf("and %d more", len(busy)-exitShown))
			break
		}
		names = append(names, c.Key()+" · "+c.Title)
	}
	what := "1 card has Claude at work"
	if len(busy) > 1 {
		what = fmt.Sprintf("%d cards have Claude at work", len(busy))
	}
	k.Modal(gtx, d, 120, 480, nil, func(gtx layout.Context) layout.Dimensions {
		body := func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 20, Bottom: 18, Left: 20, Right: 20}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Start}.Layout(gtx,
							layout.Rigid(iconTile(kit.IcoWarn, kit.COrange, kit.CPanelCard)),
							layout.Rigid(kit.HGap(12)),
							layout.Flexed(1, k.Title("Exit desk?", what+". Exiting stops it.")))
					}),
					layout.Rigid(kit.VGap(14)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return k.Label(gtx, strings.Join(names, "\n"), 13, kit.CText2, kit.Sans, exitShown+1)
					}))
			})
		}
		foot := func(gtx layout.Context) layout.Dimensions {
			return dialogFoot(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)} }),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d0, ok := k.KBtn(gtx, "exitCancel", kit.Ghost, "Cancel", "esc")
						if ok {
							d.act = func() { u.exit = nil }
						}
						return d0
					}),
					layout.Rigid(kit.HGap(8)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d0, ok := k.KBtn(gtx, "exitOK", kit.Danger, "Exit anyway", "↵")
						if ok {
							d.act = u.exitAnyway
						}
						return d0
					}))
			})
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, layout.Rigid(body), layout.Rigid(foot))
	})
	if act := d.act; act != nil { // after drawing: both answers clear u.exit
		d.act = nil
		act()
	}
}
