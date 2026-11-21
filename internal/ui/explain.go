package ui

import (
	"context"
	"image/color"
	"time"

	"gioui.org/layout"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// explainCmd is claude.Explain; tests replace it.
var explainCmd = claude.Explain

// explained is Haiku's take on a shell command: asked once per command.
type explained struct {
	text, err string
	busy      bool
}

// explain asks Haiku about cmd, off the UI goroutine, in card's folder
// under its account, unless it was asked already (or is being).
func (u *ui) explain(card, cmd string) {
	if e := u.explains[cmd]; e != nil && (e.busy || e.err == "") {
		return
	}
	c := u.b.Card(card)
	if c == nil {
		return
	}
	e := &explained{busy: true}
	u.explains[cmd] = e // ponytail: never pruned; one short text per command asked about
	u.exGen++
	org, acct, dir := c.Org, orgs.Account(u.root, c.Org), u.cardDir(c)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		env, err := u.goEnv(org, "explain")
		s := ""
		if err == nil {
			s, err = explainCmd(ctx, u.home, acct, dir, cmd, env...)
		}
		cancel()
		u.later(func() {
			e.busy, e.text = false, s
			if err != nil {
				e.err = firstLine(err.Error())
			}
			u.exGen++
		})
	}()
}

// explainBox is what Haiku says cmd does: asked for now when auto (the
// shell view), else once the user asks; while it reads, a line says so.
func (u *ui) explainBox(gtx layout.Context, card, cmd string, auto bool) layout.Dimensions {
	k := u.k
	e := u.explains[cmd]
	if e == nil && auto {
		u.explain(card, cmd)
		e = u.explains[cmd]
	}
	if e == nil {
		return layout.Dimensions{}
	}
	return kit.Boxed(kit.Mix(kit.CPanel, kit.CAcc, .07), color.NRGBA{}, kit.RCard, layout.Inset{Top: 8, Bottom: 8, Left: 10, Right: 10}, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		kids := []layout.FlexChild{layout.Rigid(k.Lbl("What it does · Haiku", 11, kit.CAccText, kit.Semi(kit.Sans))), layout.Rigid(kit.VGap(4))}
		switch {
		case e.busy:
			kids = append(kids, layout.Rigid(k.Lbl("Reading the command…", 12, kit.CText3, kit.Sans)))
		case e.err != "":
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return k.Label(gtx, "Couldn't explain it: "+e.err, 12, kit.CRed, kit.Sans, 2)
			}), layout.Rigid(kit.VGap(6)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d, ok := k.Button(gtx, "explainRetry"+cmd, kit.CPanelCard, kit.CHover, kit.RCtl, layout.Inset{Top: 3, Bottom: 3, Left: 8, Right: 8}, k.Lbl("Try again", 12, kit.CText2, kit.Semi(kit.Sans)))
				if ok {
					u.explain(card, cmd)
				}
				return d
			}))
		default:
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.markdown(gtx, "explain"+cmd, e.text) // steps numbered, parts under them, code in ticks
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})(gtx)
}

// explainBtn is the small Explain beside a shell command in the chat,
// until Haiku has been asked about it.
func (u *ui) explainBtn(gtx layout.Context, card, cmd string) layout.Dimensions {
	if u.explains[cmd] != nil {
		return layout.Dimensions{}
	}
	k := u.k
	bid := "explain" + card + cmd
	d, ok := k.Button(gtx, bid, color.NRGBA{}, kit.CHover, kit.RCtl, layout.Inset{Top: 1, Bottom: 1, Left: 6, Right: 6}, k.Lbl("Explain", 12, kit.CAccText, kit.Semi(kit.Sans)))
	k.Tip(gtx, bid, d, "Ask Haiku what this command does, part by part", true)
	if ok {
		u.explain(card, cmd)
	}
	return d
}
