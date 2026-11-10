package infra

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/x/styledtext"

	"github.com/lfdominguez/desk/internal/keys"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

func keyFocus(tag event.Tag) key.FocusCmd { return key.FocusCmd{Tag: tag} }

// sheets draws whichever modal is up: a question, an approval, the palette.
func (u *ui) sheets(gtx layout.Context) {
	st := u.st
	kind := ""
	switch {
	case st.Ask != "":
		kind = "ask"
	case st.Approval != nil && st.Approval.Org == u.org:
		kind = "approval"
	case u.pal:
		kind = "pal"
	}
	sig := kind
	if kind == "ask" {
		sig += st.Ask
	} else if kind == "approval" {
		sig += st.Approval.Tab + st.Approval.Cmd
	}
	opened := sig != u.sheetSig
	if opened {
		if u.sheetSig != "" && kind == "" {
			u.wantTerm = true
		}
		u.sheetSig, u.editing = sig, false
	}
	if kind == "" {
		return
	}
	// the scrim swallows clicks; it closes the palette
	full := image.Rectangle{Max: gtx.Constraints.Max}
	paint.FillShape(gtx.Ops, cDlgScrim, clip.Rect(full).Op())
	scrimTag := &u.sheetSig
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: scrimTag, Kinds: pointer.Press})
		if !ok {
			break
		}
		if _, ok := ev.(pointer.Event); ok && kind == "pal" {
			u.closePalette()
		}
	}
	s := clip.Rect(full).Push(gtx.Ops)
	event.Op(gtx.Ops, scrimTag)
	s.Pop()

	top := op.Offset(image.Pt(0, gtx.Dp(map[string]unit.Dp{"ask": 120, "approval": 80, "pal": 84}[kind]))).Push(gtx.Ops)
	cgtx := gtx
	cgtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, 0) // N centers within Min
	layout.N.Layout(cgtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X-gtx.Dp(32), gtx.Dp(map[string]unit.Dp{"ask": 440, "approval": 720, "pal": 700}[kind]))
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		switch kind {
		case "ask":
			return u.askSheet(gtx, opened)
		case "approval":
			return u.approvalSheet(gtx, opened)
		}
		return u.paletteSheet(gtx, opened)
	})
	top.Pop()
}

// askView is how the ask sheet shows a question's title. Vault unlocks
// ("Master password · acme-vault") read as Unlock acme-vault with the kind
// above the field; an SSH connection's question ("ssh web-01 · deploy@web-01's password:
// (the vault password was rejected)") as what the host asks, its
// parenthesized note as the error. A one-time code gets the code boxes.
type askView struct {
	title, sub, caption, err, note, ok string
	ssh, code                          bool
}

func askFor(q string) askView {
	v := askView{title: q, sub: "desk needs this to continue", ok: "Continue"}
	what := q
	if rest, ok := strings.CutPrefix(q, "ssh "); ok && strings.Contains(rest, " · ") {
		host, prompt, _ := strings.Cut(rest, " · ")
		if i := strings.Index(prompt, "  ("); i >= 0 && len(prompt) > i+4 && strings.HasSuffix(prompt, ")") {
			prompt, v.err = prompt[:i], strings.ToUpper(prompt[i+3:i+4])+prompt[i+4:len(prompt)-1]
		}
		v.title, v.sub, v.ssh, v.note = "ssh "+host+" asks", prompt, true, "Sent to this ssh session only, not stored."
		what = prompt
	} else if kind, vault, ok := strings.Cut(q, " · "); ok {
		v.title, v.sub, v.caption, v.ok, v.note = "Unlock "+vault, "", kind, "Unlock", "Kept in memory only, never written to disk."
		what = kind
	}
	l := strings.ToLower(what)
	v.code = strings.Contains(l, "code") || strings.Contains(l, "otp") || strings.Contains(l, "2fa")
	return v
}

func (u *ui) askSheet(gtx layout.Context, opened bool) layout.Dimensions {
	if opened {
		u.askIn.SetText("")
		u.askIn.Mask = map[bool]rune{true: '•', false: 0}[u.st.AskMasked]
		gtx.Execute(keyFocus(&u.askIn))
	}
	v := askFor(u.st.Ask)
	for {
		ev, ok := u.askIn.Update(gtx)
		if !ok {
			break
		}
		if s, ok := ev.(widget.SubmitEvent); ok {
			u.d.reply(s.Text, true)
		}
	}
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &u.askIn, Name: key.NameEscape})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			u.d.reply("", false)
		}
	}
	ico := kit.IcoLock
	if v.ssh {
		ico = kit.IcoTerminal
	}
	head := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return u.label(gtx, v.title, 16, cText, fBold(sansFont), 2)
	})}
	if v.sub != "" {
		f := sansFont
		if v.ssh {
			f = monoFont
		}
		head = append(head, layout.Rigid(vgap(3)), layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.label(gtx, v.sub, 12, cText3, f, 3) }))
	}
	body := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(iconTile(36, 18, rSheet, cPanelCard, cText, ico)),
				layout.Rigid(hgap(12)),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx, head...)
				}))
		}),
		layout.Rigid(vgap(18)),
	}
	if v.caption != "" {
		body = append(body, layout.Rigid(u.lbl(v.caption, 12, cText2, fSemi(sansFont))), layout.Rigid(vgap(6)))
	}
	if v.code && u.askIn.Len() <= 6 {
		body = append(body, layout.Rigid(u.codeBoxes(v.err != "")))
	} else {
		body = append(body, layout.Rigid(u.askField(gtx.Focused(&u.askIn), v.err != "")))
	}
	if v.err != "" {
		body = append(body, layout.Rigid(vgap(7)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: 2}.Layout(gtx, icon(13, cRedText, kit.IcoWarn))
				}),
				layout.Rigid(hgap(6)),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return u.label(gtx, v.err, 12, cRedText, sansFont, 0) }))
		}))
	}
	if v.note != "" {
		body = append(body, layout.Rigid(vgap(16)), layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.label(gtx, v.note, 12, cText3, sansFont, 0) }))
	}
	return boxed(cPanel, cRim, rSheet, layout.Inset{}, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 20, Left: 20, Right: 20}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx, body...)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 14, Bottom: 16, Left: 20, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						spacer(),
						u.kb("askNo", kit.Ghost, "Not now", "esc", func() { u.d.reply("", false) }),
						layout.Rigid(hgap(8)),
						u.kb("askOk", kit.Primary, v.ok, "↵", func() { u.d.reply(u.askIn.Text(), true) }))
				})
			}))
	})(gtx)
}

// askField is the ask sheet's one-line field, with Show/Hide on a secret.
func (u *ui) askField(focused, bad bool) layout.Widget {
	rim := cLine
	switch {
	case bad:
		rim = cRed
	case focused:
		rim = cAcc
	}
	return boxed(cPane, rim, rCtl, layout.Inset{Top: 3, Bottom: 3, Left: 10, Right: 3}, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		kids := []layout.FlexChild{
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: image.Pt(0, gtx.Dp(30))} }), // the field's height
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return u.editor(gtx, &u.askIn, "", 14, sansFont, cText) }),
		}
		if u.st.AskMasked {
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d, ok := u.button(gtx, "askShow", color.NRGBA{}, cHover, rCtl, layout.Inset{Top: 6, Bottom: 6, Left: 10, Right: 10},
					u.lbl(map[bool]string{true: "Show", false: "Hide"}[u.askIn.Mask != 0], 12, cText2, fSemi(sansFont)))
				if ok {
					u.askIn.Mask = map[bool]rune{true: 0, false: '•'}[u.askIn.Mask != 0]
					gtx.Execute(keyFocus(&u.askIn))
					gtx.Execute(op.InvalidateCmd{}) // the editor was drawn with the old mask
				}
				return d
			}))
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
	})
}

// codeBoxes shows a one-time code as six boxes over the ask editor, which is
// laid out invisibly on top of them so it keeps the keyboard and the clicks.
func (u *ui) codeBoxes(bad bool) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		rs := []rune(u.askIn.Text())
		focused := gtx.Focused(&u.askIn)
		w, h, gap, dash := gtx.Dp(40), gtx.Dp(44), gtx.Dp(6), gtx.Dp(14)
		x := 0
		for i := range 6 {
			if i == 3 {
				paint.FillShape(gtx.Ops, cIdle, clip.Rect(image.Rect(x+dash/2-gtx.Dp(4), h/2, x+dash/2+gtx.Dp(4), h/2+gtx.Dp(1))).Op())
				x += dash + gap
			}
			rim := cLine
			switch {
			case bad:
				rim = cRed
			case focused && i == min(len(rs), 5):
				rim = cAcc
			}
			panel(gtx, image.Rect(x, 0, x+w, h), rCtl, cPane, rim)
			if i < len(rs) {
				c := string(rs[i])
				if u.askIn.Mask != 0 {
					c = string(u.askIn.Mask)
				}
				st := op.Offset(image.Pt(x, 0)).Push(gtx.Ops)
				cg := gtx
				cg.Constraints = layout.Exact(image.Pt(w, h))
				layout.Center.Layout(cg, u.lbl(c, 18, cText, fMedium(monoFont)))
				st.Pop()
			}
			x += w + gap
		}
		size := image.Pt(x-gap, h)
		eg := gtx
		eg.Constraints = layout.Exact(size)
		clear := mat(gtx, color.NRGBA{})
		u.askIn.Layout(eg, u.sh, monoFont, 18, clear, clear)
		return layout.Dimensions{Size: size}
	}
}

func (u *ui) approvalSheet(gtx layout.Context, opened bool) layout.Dimensions {
	a := u.st.Approval
	tag := &u.editing
	if opened {
		gtx.Execute(keyFocus(tag))
	}
	verdict := func(v string) {
		switch {
		case v == "e" && !a.Secret:
			if !u.editing {
				u.editing = true
				u.apEdit.SetText(a.Cmd)
				gtx.Execute(keyFocus(&u.apEdit))
				gtx.Execute(op.InvalidateCmd{}) // from the button the editor isn't drawn yet: focused again next frame
			}
		case v == "e":
		default:
			cmd := ""
			if u.editing {
				cmd = strings.TrimSpace(u.apEdit.Text())
			}
			u.d.answer(v, cmd)
		}
	}
	for {
		ev, ok := gtx.Event(
			key.FocusFilter{Target: tag}, // without it the tag can't take the keyboard
			key.Filter{Focus: tag, Name: "Y"}, key.Filter{Focus: tag, Name: "A"}, key.Filter{Focus: tag, Name: "E"},
			key.Filter{Focus: tag, Name: "N"}, key.Filter{Focus: tag, Name: key.NameReturn}, key.Filter{Focus: tag, Name: key.NameEscape},
		)
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			verdict(map[key.Name]string{"Y": "y", "A": "a", "E": "e", "N": "n", key.NameReturn: "y", key.NameEscape: "n"}[e.Name])
		}
	}
	if u.editing {
		if !gtx.Focused(&u.apEdit) {
			gtx.Execute(keyFocus(&u.apEdit))
		}
		kit.Newline(gtx, &u.apEdit)
		for {
			ev, ok := u.apEdit.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				verdict("y")
			}
		}
		for {
			ev, ok := gtx.Event(key.Filter{Focus: &u.apEdit, Name: key.NameEscape})
			if !ok {
				break
			}
			if e, ok := ev.(key.Event); ok && e.State == key.Press {
				u.editing = false
				gtx.Execute(keyFocus(tag))
			}
		}
	}
	title, kind, place, where, why := approvalText(a)
	ico, whyC := kit.IcoTerminal, cText3
	if a.Secret {
		ico = kit.IcoLock
	}
	if a.Blocked {
		whyC = cOrange
	}
	m := op.Record(gtx.Ops)
	d := boxed(cPanel, mix(cOrange, cPanel, 0.68), rSheet, layout.Inset{}, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		kids := []layout.FlexChild{
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 12, Left: 16, Right: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(icon(16, cOrange, ico)), layout.Rigid(hgap(10)),
						layout.Rigid(u.lbl(title, 14, cText, fBold(sansFont))), layout.Rigid(hgap(10)),
						layout.Rigid(u.pill(kind, cOrange, cOrangeSoft)),
						spacer(),
						layout.Rigid(u.rich(span(place, 12, cText3, sansFont), span(where, 12, cText2, fBold(sansFont)), span(" · "+a.Org, 12, cText3, sansFont))))
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 10, Left: 16, Right: 16}.Layout(gtx, boxed(cPane, cLine, rCard, layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					if u.editing {
						return u.editor(gtx, &u.apEdit, "", 13, monoFont, cText)
					}
					if a.Keys || a.Secret {
						return u.label(gtx, a.Cmd, 13, cText, monoFont, 0)
					}
					return layout.Flex{}.Layout(gtx, layout.Rigid(u.lbl("$ ", 13, cText3, monoFont)),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return u.label(gtx, a.Cmd, 13, cText, monoFont, 0) }))
				}))
			}),
		}
		text := func(top unit.Dp, w layout.Widget) layout.FlexChild {
			return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: top, Left: 16, Right: 16}.Layout(gtx, w)
			})
		}
		if a.Reason != "" {
			kids = append(kids, text(10, u.rich(span("Why: ", 12, cText, fBold(sansFont)), span(a.Reason, 12, cText2, sansFont))))
		}
		if why != "" {
			kids = append(kids, text(6, func(gtx layout.Context) layout.Dimensions { return u.label(gtx, why, 11, whyC, sansFont, 0) }))
		}
		if len(a.Explain) > 0 {
			kids = append(kids, text(12, u.lbl("Step by step", 11, cText3, fSemi(sansFont))))
			for i, s := range a.Explain {
				kids = append(kids, text(6, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{}.Layout(gtx,
						layout.Rigid(boxed(cPanelCard, color.NRGBA{}, 3, layout.Inset{Top: 1, Bottom: 1, Left: 6, Right: 6}, u.lbl(fmt.Sprint(i+1), 11, cText, fSemi(sansFont)))),
						layout.Rigid(hgap(8)),
						layout.Flexed(1, u.rich(span(s.Part, 12, cText, monoFont), span(" — "+s.Does, 12, cText2, sansFont))))
				}))
			}
		}
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 12, Bottom: 14, Left: 16, Right: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				row := []layout.FlexChild{u.kb("apN", kit.Ghost, "Deny", "N", func() { verdict("n") }), layout.Rigid(hgap(8))}
				if !a.Secret {
					row = append(row, u.kb("apE", kit.Secondary, "Edit", "E", func() { verdict("e") }))
				}
				row = append(row, spacer())
				if !a.Secret {
					row = append(row, u.kb("apA", kit.Secondary, "Always allow in "+a.Org, "A", func() { verdict("a") }), layout.Rigid(hgap(8)))
				}
				row = append(row, u.kb("apY", kit.Primary, "Approve", "Y", func() { verdict("y") }))
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx, row...)
			})
		}))
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})(gtx)
	content := m.Stop()
	// the keyboard handler's area goes under the sheet, so its buttons get
	// the clicks
	st := clip.Rect{Max: d.Size}.Push(gtx.Ops)
	event.Op(gtx.Ops, tag)
	st.Pop()
	content.Add(gtx.Ops)
	return d
}

// approvalText is the approval sheet's header: what Claude wants, its pill,
// where ("on "/"in " and the host, or the tab without one) and what sent it
// to the user ("" when it just needs approval).
func approvalText(a *guiApproval) (title, kind, place, where, why string) {
	title, kind, where, why = "Claude wants to run a command", "changes the server", a.Tab, a.Why
	switch {
	case a.Keys:
		title, kind = "Claude wants to press keys", "keystrokes"
	case a.Secret:
		title, kind = "Claude wants to read a secret", "secret"
	}
	if a.Host != "" && !a.Secret {
		where, place = a.Host, "on "
		if a.Keys {
			place = "in "
		}
	}
	if a.Blocked {
		why = "Blocked by Claude Code's permission classifier"
	}
	return title, kind, place, where, why
}

func (u *ui) openPalette() {
	u.pal, u.palSel, u.palQ, u.palScope = true, 0, "", ""
	u.palIn.SetText("")
	q := ""
	u.d.palette(&q)
}

func (u *ui) closePalette() {
	u.pal = false
	u.d.palette(nil)
}

func (u *ui) paletteSheet(gtx layout.Context, opened bool) layout.Dimensions {
	if opened {
		gtx.Execute(keyFocus(&u.palIn))
		u.palList.Position = layout.Position{}
	}
	items := append(u.guiPal(), u.st.Pal...)
	scopes, shown := palScopes(items, u.palScope)
	setScope := func(s string) {
		u.palScope, u.palSel = s, 0
		u.palList.Position = layout.Position{}
		scopes, shown = palScopes(items, s)
	}
	for {
		ev, ok := gtx.Event(append(keys.Filters(&u.palIn, "pal.next", "pal.prev"),
			key.Filter{Focus: &u.palIn, Name: key.NameUpArrow}, key.Filter{Focus: &u.palIn, Name: key.NameDownArrow},
			key.Filter{Focus: &u.palIn, Name: key.NameEscape})...)
		if !ok {
			break
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		switch {
		case keys.Is("pal.next", e):
			setScope(scopes[(slices.Index(scopes, u.palScope)+1)%len(scopes)])
		case keys.Is("pal.prev", e):
			setScope(scopes[(slices.Index(scopes, u.palScope)+len(scopes)-1)%len(scopes)])
		case e.Name == key.NameUpArrow:
			u.palSel = max(0, u.palSel-1)
		case e.Name == key.NameDownArrow:
			u.palSel = min(len(shown)-1, u.palSel+1)
		case e.Name == key.NameEscape:
			u.closePalette()
		}
	}
	pick := func(k int) { // k-th shown item
		if k >= 0 && k < len(shown) {
			u.pick(shown[k], len(items))
		}
	}
	for {
		ev, ok := u.palIn.Update(gtx)
		if !ok {
			break
		}
		switch ev.(type) {
		case widget.ChangeEvent:
			if q := u.palIn.Text(); q != u.palQ {
				u.palQ, u.palSel = q, 0
				u.palList.Position = layout.Position{}
				u.d.palette(&q)
			}
		case widget.SubmitEvent:
			pick(u.palSel)
		}
	}
	line := func(gtx layout.Context) {
		paint.FillShape(gtx.Ops, cLine, clip.Rect(image.Rect(0, 0, gtx.Constraints.Max.X, gtx.Dp(1))).Op())
	}
	list := func(gtx layout.Context) layout.Dimensions {
		var kids []layout.Widget // kind headers and items, in a list that scrolls
		last, selAt := "", -1
		for k, i := range shown {
			it := items[i]
			if it.Kind != last {
				last = it.Kind
				kids = append(kids, func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: 8, Bottom: 4, Left: 12}.Layout(gtx, u.lbl(palNames[it.Kind], 11, cText3, fSemi(sansFont)))
				})
			}
			sel := k == u.palSel
			if sel {
				selAt = len(kids)
			}
			kids = append(kids, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				fill, hover, tile, ink := color.NRGBA{}, cHover, cPanelCard, cText2
				if sel {
					fill, hover, tile, ink = cSel, cSel, cAccSoft, cAccText
				}
				d, ok := u.button(gtx, fmt.Sprint("pal", i), fill, hover, rCtl, layout.Inset{Top: 7, Bottom: 7, Left: 12, Right: 12}, func(gtx layout.Context) layout.Dimensions {
					row := []layout.FlexChild{
						layout.Rigid(iconTile(22, 12, 5, tile, ink, palIcons[it.Kind])), layout.Rigid(hgap(10)),
						layout.Rigid(u.rich(palSpans(it.Label, matchRunes(u.palQ, it.Label))...)), layout.Rigid(hgap(8)),
						layout.Flexed(1, u.lblFill(it.Hint, 12, cText3, sansFont)),
					}
					if sel {
						row = append(row, layout.Rigid(hgap(8)), layout.Rigid(u.keyCap("↵", false)))
					}
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx, row...)
				})
				if ok {
					pick(k)
				}
				return d
			})
		}
		if len(shown) == 0 && u.st.PalOpen {
			kids = append(kids, func(gtx layout.Context) layout.Dimensions {
				return layout.UniformInset(16).Layout(gtx, u.lbl("Nothing matches.", 13, cText3, sansFont))
			})
		}
		if u.palSel != u.palShown && selAt >= 0 { // the keyboard moved: bring the selection into view
			u.palShown = u.palSel
			l := &u.palList
			if u.palSel == 0 {
				l.Position = layout.Position{} // its kind header too
			} else if selAt < l.Position.First || selAt == l.Position.First && l.Position.Offset > 0 {
				l.ScrollTo(selAt)
			} else if l.Position.Count > 0 && selAt >= l.Position.First+l.Position.Count-1 {
				l.Position.First, l.Position.Offset = selAt-l.Position.Count+2, 0
			}
		}
		return layout.Inset{Top: 2, Bottom: 6, Left: 4, Right: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.Y = min(gtx.Constraints.Max.Y, gtx.Dp(480))
			return u.palList.Layout(gtx, len(kids), func(gtx layout.Context, k int) layout.Dimensions { return kids[k](gtx) })
		})
	}
	return boxed(cPanel, cRim, rSheet, layout.Inset{}, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 16, Bottom: 14, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(icon(16, cText3, icoSearch)),
						layout.Rigid(hgap(10)),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return u.editor(gtx, &u.palIn, "Sessions, hosts, actions…", 16, fMedium(sansFont), cText)
						}),
						layout.Rigid(u.keyCap("esc", false)))
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { // the scope tabs
				return layout.Inset{Bottom: 8, Left: 12, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					var row []layout.FlexChild
					for _, s := range scopes {
						on := s == u.palScope
						row = append(row, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							fill, fg := color.NRGBA{}, cText3
							if on {
								fill, fg = cHover, cText
							}
							d, ok := u.button(gtx, "palScope"+s, fill, cHover, rCtl, layout.Inset{Top: 4, Bottom: 4, Left: 9, Right: 9}, u.lbl(palNames[s], 12, fg, fSemi(sansFont)))
							if ok {
								setScope(s)
								gtx.Execute(keyFocus(&u.palIn))
								gtx.Execute(op.InvalidateCmd{}) // the tabs before it were drawn already
							}
							return d
						}), layout.Rigid(hgap(2)))
					}
					row = append(row, spacer(), layout.Rigid(u.hint(keys.Cap("pal.next"), "next scope")))
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx, row...)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { // the results, and the selected one's preview beside them
				line(gtx)
				pw := 0
				if gtx.Constraints.Max.X >= gtx.Dp(560) && u.palSel >= 0 && u.palSel < len(shown) {
					pw = gtx.Dp(230)
				}
				lg := gtx
				lg.Constraints.Min = image.Pt(gtx.Constraints.Max.X-pw, 0)
				lg.Constraints.Max.X -= pw
				ld := list(lg)
				if pw == 0 {
					return ld
				}
				st := op.Offset(image.Pt(ld.Size.X, 0)).Push(gtx.Ops)
				pg := gtx
				pg.Constraints = layout.Constraints{Max: image.Pt(pw, gtx.Constraints.Max.Y)}
				pd := layout.Inset{Top: 14, Bottom: 14, Left: 16, Right: 16}.Layout(pg, u.palPreview(items[shown[u.palSel]]))
				h := max(ld.Size.Y, pd.Size.Y)
				paint.FillShape(gtx.Ops, cLine, clip.Rect(image.Rect(0, 0, gtx.Dp(1), h)).Op())
				st.Pop()
				return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, h)}
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Background{}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					// the footer's own tint, rounded with the sheet's bottom corners
					sz, in, rad := gtx.Constraints.Min, gtx.Dp(1), gtx.Dp(rSheet)-gtx.Dp(1)
					paint.FillShape(gtx.Ops, mix(cPanel, cPane, 0.15), clip.RRect{Rect: image.Rect(in, 0, sz.X-in, sz.Y-in), SE: rad, SW: rad}.Op(gtx.Ops))
					line(gtx)
					return layout.Dimensions{Size: sz}
				}, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return layout.Inset{Top: 9, Bottom: 9, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(u.keyCap("↑", false)), layout.Rigid(hgap(3)), layout.Rigid(u.hint("↓", "move")),
							layout.Rigid(hgap(14)), layout.Rigid(u.hint("↵", "open")),
							layout.Rigid(hgap(14)), layout.Rigid(u.hint("esc", "close")))
					})
				})
			}))
	})(gtx)
}

// The palette's kinds, in the order its scope tabs show them.
var (
	palKinds = []string{"sessions", "hosts", "groups", "orgs", "runbooks", "alerts", "actions", "themes"}
	palNames = map[string]string{"": "All", "sessions": "Sessions", "groups": "Groups", "hosts": "Hosts", "orgs": "Orgs",
		"actions": "Actions", "runbooks": "Runbooks", "alerts": "Alerts", "themes": "Themes"}
	palIcons = map[string]func(p *clip.Path, s float32){"sessions": kit.IcoTerminal, "hosts": icoServer, "groups": icoGrid, "orgs": icoSpark,
		"runbooks": kit.IcoArchive, "alerts": kit.IcoWarn, "actions": icoChevronRight, "themes": icoStar}
)

// palScopes is the scope tabs (All, then each kind the items have, and the
// current scope even once nothing of it matches) and which items scope shows.
func palScopes(items []guiPal, scope string) (scopes []string, shown []int) {
	scopes = []string{""}
	for _, k := range palKinds {
		if k == scope || slices.ContainsFunc(items, func(it guiPal) bool { return it.Kind == k }) {
			scopes = append(scopes, k)
		}
	}
	for i, it := range items {
		if scope == "" || it.Kind == scope {
			shown = append(shown, i)
		}
	}
	return scopes, shown
}

// palSpans is a palette label with the runes fzf matched (at) in the accent.
func palSpans(s string, at []int) []styledtext.SpanStyle {
	var out []styledtext.SpanStyle
	rs := []rune(s)
	for i := 0; i < len(rs); {
		on := slices.Contains(at, i)
		j := i + 1
		for j < len(rs) && slices.Contains(at, j) == on {
			j++
		}
		sp := span(string(rs[i:j]), 13, cText, sansFont)
		if on {
			sp.Color, sp.Font = cAccText, fBold(sansFont)
		}
		out = append(out, sp)
		i = j
	}
	return out
}

// palField is a line of the palette's preview: k over v ("" k: just v).
type palField struct {
	k, v string
	mono bool
	c    color.NRGBA
}

// palInfo is what the window knows about palette item it: a host's address,
// group, alert, notes and open tabs; a session's org, group, panes and
// state; an org's servers and open tabs. Anything else shows its hint.
func (u *ui) palInfo(it guiPal) (title string, rows []palField) {
	title = it.Label
	add := func(k, v string, mono bool, c color.NRGBA) {
		if v != "" && v != "0" {
			rows = append(rows, palField{k, v, mono, c})
		}
	}
	tabs := func(match func(t guiTab) bool) string {
		n := 0
		for _, t := range u.st.Tabs {
			if match(t) {
				n++
			}
		}
		return fmt.Sprint(n)
	}
	switch it.Kind {
	case "hosts":
		for _, n := range u.st.Tree {
			if n.Host == it.Host && n.Dir == it.Dir {
				title = n.Label
				add("Address", n.Addr, true, cText2)
				add("Group", n.Org+" / "+n.Group, false, cText2)
				add("Alert", strings.ToLower(n.Alert), false, map[bool]color.NRGBA{true: cRed, false: cOrange}[n.Alert == "CRITICAL" || n.Alert == "DOWN"])
				add("Notes", n.Note, false, cText2)
				add("Open tabs", tabs(func(t guiTab) bool {
					return slices.ContainsFunc(t.Panes, func(p guiPane) bool { return p.Name == n.Label && p.Dir == n.Dir })
				}), false, cText2)
				break
			}
		}
	case "sessions":
		if it.Tab >= 0 && it.Tab < len(u.st.Tabs) {
			t := u.st.Tabs[it.Tab]
			title = t.Name
			add("Organization", t.Org, false, cText2)
			add("Group", t.Group, false, cText2)
			var names []string
			for _, p := range t.Panes {
				names = append(names, p.Name)
			}
			add("Panes", strings.Join(names, ", "), false, cText2)
			add("State", t.State, false, cText2)
		}
	case "orgs":
		n := 0
		for _, h := range u.st.Tree {
			if h.Org == it.Label && h.Host != "" {
				n++
			}
		}
		add("Servers", fmt.Sprint(n), false, cText2)
		add("Open tabs", tabs(func(t guiTab) bool { return t.Org == it.Label }), false, cText2)
	}
	if len(rows) == 0 {
		add("", it.Hint, false, cText2)
	}
	return title, rows
}

// palPreview draws palInfo beside the palette's results.
func (u *ui) palPreview(it guiPal) layout.Widget {
	title, rows := u.palInfo(it)
	return func(gtx layout.Context) layout.Dimensions {
		kids := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.label(gtx, title, 14, cText, fBold(sansFont), 2) })}
		for _, r := range rows {
			kids = append(kids, layout.Rigid(vgap(8)))
			if r.k != "" {
				kids = append(kids, layout.Rigid(u.lbl(r.k, 12, cText3, sansFont)))
			}
			f := sansFont
			if r.mono {
				f = monoFont
			}
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.label(gtx, r.v, 12, r.c, f, 3) }))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	}
}

// guiCommands are palette actions the window runs itself, ahead of the model's.
var guiCommands = []struct {
	label, hint, key string // key: its shortcut's id, read when shown
	run              func(u *ui)
}{
	{"New directory split (files)", "browse this server and copy to this machine", "infra.files", (*ui).openFiles},
	{"Open server notes", "what Claude remembers about each server (markdown, editable)", "", func(u *ui) {
		d := filepath.Join(stateDir(), "notes")
		os.MkdirAll(d, 0o700)
		openLocal(d)
	}},
	{"Open recordings", "terminal recordings, one per pane (asciinema play <file>)", "", func(u *ui) {
		d := filepath.Join(stateDir(), "recordings")
		os.MkdirAll(d, 0o700)
		openLocal(d)
	}},
	{"Open audit log", "every command and approval, per org and day (JSON lines)", "", func(u *ui) {
		os.MkdirAll(auditDir(), 0o700)
		openLocal(auditDir())
	}},
}

// guiPal lists the guiCommands matching the query (none without one, like the model's actions).
func (u *ui) guiPal() []guiPal {
	var out []guiPal
	for _, c := range guiCommands {
		if _, ok := fzfScore(u.palQ, c.label); ok && strings.TrimSpace(u.palQ) != "" {
			if c.key != "" {
				c.hint += " · " + keys.Short(c.key)
			}
			out = append(out, guiPal{Kind: "actions", Label: c.label, Hint: c.hint})
		}
	}
	return out
}

func (u *ui) pick(i, n int) {
	if i < 0 || i >= n {
		return
	}
	if local := u.guiPal(); i < len(local) {
		u.closePalette()
		for _, c := range guiCommands {
			if c.label == local[i].Label {
				c.run(u)
			}
		}
		return
	}
	u.pal = false
	u.d.palPick(i - len(u.guiPal()))
	u.wantTerm = true
}

// toasts show the model's flash and error lines for a moment.
func (u *ui) toasts(gtx layout.Context) {
	if f := u.st.Flash; f != "" && f != u.lastFlash {
		u.showToast(strings.TrimLeft(f, "!✓⧉ "), strings.HasPrefix(f, "!"))
	}
	if e := u.st.Err; e != "" && e != u.lastErr {
		u.showToast(e, true)
	}
	u.lastFlash, u.lastErr = u.st.Flash, u.st.Err
	left := time.Until(u.toastUntil)
	if left <= 0 || u.toast == "" {
		return
	}
	gtx.Execute(op.InvalidateCmd{At: u.toastUntil})
	c := cText
	if u.toastBad {
		c = cRed
	}
	m := op.Record(gtx.Ops)
	cgtx := gtx
	cgtx.Constraints.Min = image.Point{}
	cgtx.Constraints.Max.X = min(cgtx.Constraints.Max.X, gtx.Dp(700))
	d := boxed(cPanelCard, cRim, rCard, layout.Inset{Top: 7, Bottom: 7, Left: 12, Right: 12}, func(gtx layout.Context) layout.Dimensions {
		return u.label(gtx, u.toast, 13, c, fMedium(sansFont), 2)
	})(cgtx)
	call := m.Stop()
	st := op.Offset(image.Pt((gtx.Constraints.Max.X-d.Size.X)/2, gtx.Constraints.Max.Y-d.Size.Y-gtx.Dp(24))).Push(gtx.Ops)
	call.Add(gtx.Ops)
	st.Pop()
}

// groupView is every session of a group as a card, and one line that asks
// all of them.
func (u *ui) groupView(gtx layout.Context) layout.Dimensions {
	var idx []int
	for i, t := range u.st.Tabs {
		if groupKey(t.Dir, t.Group) == u.st.Group {
			idx = append(idx, i)
		}
	}
	for {
		ev, ok := u.broadcastIn.Update(gtx)
		if !ok {
			break
		}
		if s, ok := ev.(widget.SubmitEvent); ok && strings.TrimSpace(s.Text) != "" {
			u.d.broadcast(strings.TrimSpace(s.Text))
			u.broadcastIn.SetText("")
		}
	}
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &u.broadcastIn, Name: key.NameEscape})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			u.d.group("")
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			if len(idx) == 0 {
				return layout.Center.Layout(gtx, u.lbl("No open sessions in this group. Open hosts from the sidebar.", 13, cText3, sansFont))
			}
			cols := max(1, gtx.Constraints.Max.X/gtx.Dp(340))
			rows := (len(idx) + cols - 1) / cols
			return u.cardList.Layout(gtx, rows, func(gtx layout.Context, r int) layout.Dimensions {
				var kids []layout.FlexChild
				for c := 0; c < cols; c++ {
					if c > 0 {
						kids = append(kids, layout.Rigid(hgap(14)))
					}
					k := r*cols + c
					kids = append(kids, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						if k >= len(idx) {
							return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, 0)}
						}
						return u.card(gtx, idx[k])
					}))
				}
				return layout.Inset{Bottom: 14}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return layout.Flex{}.Layout(gtx, kids...) })
			})
		}),
		layout.Rigid(vgap(14)),
		layout.Rigid(boxed(cPanelCard, cAcc, rCard, layout.Inset{Top: 8, Bottom: 8, Left: 18, Right: 8}, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return u.editor(gtx, &u.broadcastIn, fmt.Sprintf("Ask all %d sessions…", len(idx)), 14, sansFont, cText)
				}),
				layout.Rigid(u.lbl("esc back", 11, cText3, sansFont)),
				layout.Rigid(hgap(12)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if u.btn(gtx, "bcast", "Ask all  ↵", cWhite, cAcc) && strings.TrimSpace(u.broadcastIn.Text()) != "" {
						u.d.broadcast(strings.TrimSpace(u.broadcastIn.Text()))
						u.broadcastIn.SetText("")
					}
					return layout.Dimensions{Size: image.Pt(gtx.Dp(110), gtx.Dp(34))}
				}))
		})))
}

func (u *ui) card(gtx layout.Context, i int) layout.Dimensions {
	t := u.st.Tabs[i]
	var you, ans string
	for _, e := range t.Chat {
		switch e.Who {
		case "you":
			you = e.Text
		case "claude":
			ans = e.Text
		}
	}
	var blocks []string
	for _, p := range t.Panes {
		if !p.Focused {
			continue
		}
		for n := len(p.Blocks); n > 0 && len(blocks) < 4; n-- {
			if b, ok := p.Blocks[n]; ok {
				s := "$ " + b.Cmd
				if b.Exit != 0 {
					s += fmt.Sprintf("  ✗%d", b.Exit)
				}
				blocks = append([]string{s}, blocks...)
			}
		}
	}
	rim := cRim
	if t.State == "your turn" {
		rim = cAcc
	}
	pillC := map[string][2]color.NRGBA{"approval": {cOrange, cOrangeSoft}, "working": {cPurple, cPurpleSoft}, "your turn": {cAccText, cAccSoft}}
	pc, ok := pillC[t.State]
	if !ok {
		pc = [2]color.NRGBA{cText2, cPanelCard}
	}
	d, clicked := u.button(gtx, fmt.Sprint("card", i), color.NRGBA{}, color.NRGBA{}, rCard, layout.Inset{}, boxed(cPanel, rim, rCard, layout.Inset{Top: 14, Bottom: 14, Left: 16, Right: 16}, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		kids := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(dot(cGreen, true)), layout.Rigid(hgap(10)),
				layout.Flexed(1, u.lblFill(t.Name, 15, cText, fSemi(sansFont))),
				layout.Rigid(u.pill(t.State, pc[0], pc[1])))
		})}
		if you != "" {
			kids = append(kids, layout.Rigid(vgap(10)), layout.Rigid(u.lbl("YOU ASKED", 11, cText3, fSemi(sansFont))),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.label(gtx, you, 13, cText2, sansFont, 3) }))
		}
		if ans != "" {
			kids = append(kids, layout.Rigid(vgap(8)), layout.Rigid(u.lbl("CLAUDE", 11, cText3, fSemi(sansFont))),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.label(gtx, plainText(ans), 13, cText, sansFont, 4)
				}))
		}
		if len(blocks) > 0 {
			kids = append(kids, layout.Rigid(vgap(10)), layout.Rigid(boxed(cPane, color.NRGBA{}, rCtl, layout.Inset{Top: 8, Bottom: 8, Left: 10, Right: 10}, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return u.label(gtx, strings.Join(blocks, "\n"), 11, cText2, monoFont, 4)
			})))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	}))
	if clicked {
		u.d.activate(i)
		u.wantTerm = true
	}
	return d
}

// plainText strips markdown down to a readable line for the cards.
func plainText(s string) string {
	var b strings.Builder
	for _, blk := range parseMarkdown(s) {
		if blk.kind == "code" {
			continue
		}
		for _, sp := range blk.spans {
			b.WriteString(sp.text)
		}
		b.WriteString(" ")
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
