package ui

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

func (u *ui) openPanel(id string) {
	if u.panel != id {
		u.chatSwitch(id)
		u.chatList.Position = layout.Position{BeforeEnd: false}
	}
	u.panel, u.sel, u.chatFocus, u.sub, u.rail = id, id, true, "", false
	u.w.Invalidate()
}

// openSession shows card id's session full window ("sess:<id>"), the side
// panel closed: the same conversation, room to read it.
func (u *ui) openSession(id string) {
	if u.b.Card(id) == nil {
		return
	}
	u.chatSwitch(id)
	u.chatList.Position = layout.Position{BeforeEnd: false}
	u.view, u.panel, u.sel, u.chatFocus, u.sub, u.rail = "sess:"+id, "", id, true, "", false
}

// sessionView is the card's session over the whole main area.
func (u *ui) sessionView(gtx layout.Context) layout.Dimensions {
	id := strings.TrimPrefix(u.view, "sess:")
	gtx.Constraints.Min = gtx.Constraints.Max
	kit.Fill(gtx, kit.CColumn)
	if u.b.Card(id) == nil {
		return layout.Center.Layout(gtx, u.k.Lbl("This card is gone.", 13, kit.CText3, kit.Sans))
	}
	vline := layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		paint.FillShape(gtx.Ops, kit.CLine, clip.Rect(image.Rect(0, 0, gtx.Dp(1), gtx.Constraints.Max.Y)).Op())
		return layout.Dimensions{Size: image.Pt(gtx.Dp(1), gtx.Constraints.Max.Y)}
	})
	chat := func(gtx layout.Context) layout.Dimensions {
		if !u.rail || len(u.tasks[id]) == 0 {
			return u.chatPane(gtx, id, gtx.Constraints.Max.X, true)
		}
		var act func() // the rail's clicks: run once both are drawn
		defer func() {
			if act != nil {
				act()
			}
		}()
		return layout.Flex{}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return u.chatPane(gtx, id, gtx.Constraints.Max.X, true)
			}),
			vline,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.taskRail(gtx, id, &act) }))
	}
	org := u.splitOrg(id)
	if org == "" {
		return chat(gtx)
	}
	// its server work beside it, in the card's own infra session: the whole
	// investigation in one view. Asked once per card and org: OpenCard is
	// async, so asking until ActiveOrg says so redrew at full speed
	// (forever, if the panes moved elsewhere); infra redraws itself once it
	// has switched.
	if k := id + "\x00" + org; u.splitFor != k {
		u.splitFor = k
		if c := u.b.Card(id); c != nil {
			u.infra.OpenCard(org, c.Key())
		}
	}
	u.splitShown = true
	u.infra.SetShown(true) // now, not at the next frame's infraFrame: its output must redraw this one
	return layout.Flex{}.Layout(gtx,
		layout.Flexed(.42, chat),
		vline,
		layout.Flexed(.58, func(gtx layout.Context) layout.Dimensions { return u.infra.Layout(gtx, org) }))
}

// splitOrg is the org whose infra session the card's session view shows
// beside its chat: the one its Claude handed server work to, unless the
// user asked for the chat alone; "" none.
func (u *ui) splitOrg(id string) string {
	if u.infra == nil || u.noSplit[id] || u.infraOrg(u.cardInfra[id]) == nil {
		return ""
	}
	return u.cardInfra[id]
}

// chatPanel is the open card's session beside the board. The frame picks
// it before the sidebar is laid out, whose clicks (an Agent Sessions row)
// can close it meanwhile: then it draws nothing.
func (u *ui) chatPanel(gtx layout.Context) layout.Dimensions {
	if u.b.Card(u.panel) == nil {
		return layout.Dimensions{}
	}
	return u.chatPane(gtx, u.panel, gtx.Dp(480), false)
}

// chatPane is card id's session, w wide: header, links, the conversation
// (and Claude's open question) and the composer; full is the session view.
func (u *ui) chatPane(gtx layout.Context, id string, w int, full bool) layout.Dimensions {
	gtx = kit.Zoomed(gtx, orgs.Zoom()) // its text; the pane's width stays
	k := u.k
	c := u.b.Card(id)
	gtx.Constraints = layout.Exact(image.Pt(w, gtx.Constraints.Max.Y))
	kit.Fill(gtx, kit.CPanel)
	var act func() // a button that changes what this pane draws from: run once it is drawn
	defer func() {
		if act != nil {
			act()
		}
	}()
	archived := !c.Archived.IsZero()
	if !archived { // the keyboard stays in the message box: a press anywhere in the pane brings it back, a field's own press then takes it
		for {
			if _, ok := gtx.Event(pointer.Filter{Target: &u.chatTap, Kinds: pointer.Press}); !ok {
				break
			}
			if u.asks[id] == nil { // Claude's open question answers keys while no field has them: its buttons' clicks leave them there
				gtx.Execute(key.FocusCmd{Tag: &u.chatIn})
			}
		}
		defer clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops).Pop()
		event.Op(gtx.Ops, &u.chatTap)
		u.selBack = &u.chatIn
		defer func() { u.selBack = nil }()
	}
	u.pasteImage(gtx, &u.chatIn, &u.chatPaste, func() bool { return true }) // one box for every card, the panel's and the session view's
	for {
		ev, ok := u.chatIn.Update(gtx)
		if !ok {
			break
		}
		if s, ok := ev.(widget.SubmitEvent); ok && strings.TrimSpace(s.Text) != "" {
			u.send(id, s.Text)
			u.chatIn.SetText("")
			c = u.b.Card(id)
		}
	}
	if u.chatFocus && !archived {
		gtx.Execute(key.FocusCmd{Tag: &u.chatIn})
		u.chatFocus = false
	}
	ib := func(bid string, ico kit.IconPath, tip string, f func()) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			d, ok := k.IconBtn(gtx, bid, ico, false)
			k.Tip(gtx, bid, d, tip, true)
			if ok {
				act = f
			}
			return d
		})
	}
	header := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 10, Bottom: 12, Left: 16, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			top := []layout.FlexChild{layout.Rigid(k.Lbl(c.Key(), 12, kit.CText3, kit.Mono))}
			if sc, ok := statusColor[c.Status]; ok {
				top = append(top, layout.Rigid(kit.HGap(8)), layout.Rigid(k.Pill(statusName[c.Status], sc, kit.Mix(sc, kit.CPanel, .82))))
			}
			if c.Agent != "" {
				top = append(top, layout.Rigid(kit.HGap(8)), layout.Rigid(k.Pill("Agent · "+c.Agent, pastels[0], kit.Mix(pastels[0], kit.CPanel, .84))))
			}
			if t, ok := u.cardTask(c.Key()); ok { // its server work: a click shows the infra session on it
				top = append(top, layout.Rigid(kit.HGap(8)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.Button(gtx, "pnTask"+id, kit.Mix(kit.COrange, kit.CPanel, .84), kit.Mix(kit.COrange, kit.CPanel, .74), kit.RCtl, layout.Inset{Top: 3, Bottom: 3, Left: 8, Right: 8},
						k.Lbl(fmt.Sprintf("⇄ %s infra · task %d", t.Org, t.ID), 12, kit.COrange, kit.Medium(kit.Sans)))
					if ok {
						act = func() { u.showTask(t) }
					}
					return d
				}))
			}
			if org := u.cardInfra[id]; full && u.infra != nil && u.infraOrg(org) != nil { // the infra session beside the chat, or not
				label := "Chat only"
				if u.noSplit[id] {
					label = "Split with " + org + " servers"
				}
				top = append(top, layout.Rigid(kit.HGap(8)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.Button(gtx, "pnSplit"+id, kit.CPanelCard, kit.CHover, kit.RCtl, layout.Inset{Top: 3, Bottom: 3, Left: 8, Right: 8}, k.Lbl(label, 12, kit.CText2, kit.Medium(kit.Sans)))
					if ok {
						act = func() { u.noSplit[id] = !u.noSplit[id]; u.tkGen++ }
					}
					return d
				}))
			}
			top = append(top, layout.Rigid(kit.HGap(8)), layout.Rigid(k.Pill("Claude · "+u.accountName(c.Org), kit.CText2, kit.CPanelCard))) // last: the one to cut
			// pills: what's left after the buttons, clipped: a long account name never pushes them off
			pills := top
			top = []layout.FlexChild{layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				defer clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops).Pop()
				w := gtx.Constraints.Max.X
				gtx.Constraints.Min.X = 0
				d := layout.Flex{Alignment: layout.Middle}.Layout(gtx, pills...)
				d.Size.X = w
				return d
			})}
			if full && u.started(c) { // the canvas, or the session's own conversation
				top = append(top, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					cur := 1
					if u.canvasOn(c) {
						cur = 0
					}
					return u.choice(gtx, "pnMode", []string{"Canvas", "Conversation"}, cur, func(int) color.NRGBA { return color.NRGBA{} }, func(i int) {
						act = func() { u.sub, u.canvas[id] = "", i == 0 }
					})
				}), layout.Rigid(kit.HGap(8)))
			}
			if archived {
				top = append(top, ib("pnRestore", kit.IcoArchive, "Restore to the board", func() {
					u.b.Restore(id)
					u.save()
					u.view = ""
					u.showToast("Restored "+c.Key(), false)
				}))
			} else {
				if !full {
					top = append(top, ib("pnFull", kit.IcoChat, "Open full window (Agent Sessions)", func() { u.openSession(id) }))
				}
				top = append(top,
					ib("pnWS", kit.IcoCode, "Open the workspace: its files and a Claude that reads them", func() { u.openWS(id) }),
					ib("pnDraw", kit.IcoShapes, "The card's drawings", func() { u.view = "drc:" + id }),
					ib("pnEdit", kit.IcoEdit, "Edit the card (E)", func() { u.open(id) }),
					ib("pnTerm", kit.IcoTerminal, "Open in the Claude Code terminal (T)", func() { u.terminal(id) }),
					ib("pnArchive", kit.IcoArchive, "Archive the card", func() { u.archiveCard(id) }))
			}
			if full {
				top = append(top, ib("pnClose", kit.IcoX, "Back to the board (Esc)", func() { u.view, u.blur = "", true }))
			} else {
				top = append(top, ib("pnClose", kit.IcoX, "Close the panel (Esc)", func() { u.panel, u.blur = "", true }))
			}
			where := "~/Work"
			if c.Org != "" {
				where = strings.Replace(orgs.Dir(u.root, c.Org, c.Dir), u.home, "~", 1)
			}
			kids := []layout.FlexChild{
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx, top...)
				}),
				layout.Rigid(kit.VGap(6)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return k.Label(gtx, c.Title, 16, kit.CText, kit.Semi(kit.Sans), 3)
				}),
				layout.Rigid(kit.VGap(4)),
				layout.Rigid(k.Lbl(where, 12, kit.CText3, kit.Mono)),
			}
			if ico, s, sc, ok := statusLine(*c); ok && c.Note != "" {
				kids = append(kids, layout.Rigid(kit.VGap(8)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Top: 1, Right: 7}.Layout(gtx, kit.Icon(13, sc, ico))
						}),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, s, 12, sc, kit.Medium(kit.Sans), 3) }))
				}))
			}
			for _, l := range c.Links {
				kids = append(kids, layout.Rigid(kit.VGap(5)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.Button(gtx, "pnLink"+c.ID+l.URL, color.NRGBA{}, color.NRGBA{}, 3, layout.Inset{}, u.linkChip(l))
					if ok {
						openURL(l.URL)
					}
					return d
				}))
			}
			for _, a := range c.Artifacts {
				kids = append(kids, layout.Rigid(kit.VGap(5)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.Button(gtx, "pnArt"+c.ID+a, color.NRGBA{}, color.NRGBA{}, 3, layout.Inset{}, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(kit.Icon(13, kit.CPurple, kit.IcoLink)), layout.Rigid(kit.HGap(6)),
							layout.Rigid(k.Lbl(artifactName(a)+" · open in the browser", 12, kit.CAccText, kit.Sans)))
					})
					k.Tip(gtx, "pnArt"+c.ID+a, d, a, false)
					if ok {
						openURL(a)
					}
					return d
				}))
			}
			if s := c.Session; s.Turns > 0 {
				kids = append(kids, layout.Rigid(kit.VGap(8)), layout.Rigid(k.Lbl(
					fmt.Sprintf("%d turns · %s tokens · $%.2f", s.Turns, tokens(s.Tokens), s.Cost), 11, kit.CText3, kit.Sans)))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
		})
	}
	msgs, ask := u.chat(id), u.asks[id]
	body := func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = gtx.Constraints.Max
		kit.Fill(gtx, kit.CColumn)
		n := len(msgs)
		if n == 0 && ask == nil {
			if archived {
				return layout.UniformInset(24).Layout(gtx, k.Lbl("This card never had a session.", 13, kit.CText3, kit.Sans))
			}
			return layout.UniformInset(24).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d, ok := k.Button(gtx, "pnStart"+id, kit.CAcc, kit.Mix(kit.CAcc, kit.CWhite, .12), kit.RCtl, layout.Inset{Top: 7, Bottom: 7, Left: 14, Right: 14},
							k.Lbl("Start working", 14, kit.CWhite, kit.Semi(kit.Sans)))
						if ok {
							act = func() { u.send(id, startWork) }
						}
						return d
					}),
					layout.Rigid(kit.VGap(10)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return k.Label(gtx, "Claude starts on the card: its title, notes and links go with the first message. Or tell it what to do below.", 13, kit.CText3, kit.Sans, 0)
					}))
			})
		}
		return u.chatList.Layout(gtx, n, func(gtx layout.Context, i int) layout.Dimensions {
			in := layout.Inset{Top: 6, Bottom: 6, Left: 16, Right: 16}
			if i == 0 {
				in.Top = 14
			}
			if i == n-1 {
				in.Bottom = 14
			}
			return in.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return u.message(gtx, id, fmt.Sprintf("%s-%d", id, i), msgs[i], i > 0 && msgs[i-1].Who == msgs[i].Who)
			})
		})
	}
	if t := u.subOf(id); t != nil {
		body = func(gtx layout.Context) layout.Dimensions { return u.subBody(gtx, id, t, &act) }
	} else if u.sub == historyKey && len(u.tasks[id]) > 0 {
		body = func(gtx layout.Context) layout.Dimensions { return u.historyBody(gtx, id, &act) }
	}
	canvas := u.canvasMode(c, full)
	if canvas {
		body = func(gtx layout.Context) layout.Dimensions { return u.canvasBody(gtx, c, &act) }
	}
	kids := []layout.FlexChild{layout.Rigid(header)}
	if len(u.tasks[id]) > 0 && !canvas { // the canvas shows them all
		u.tick(gtx, id)
		kids = append(kids, layout.Rigid(kit.VGap(1)), layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.taskBar(gtx, id, full, &act) }))
	}
	kids = append(kids, layout.Rigid(kit.VGap(1)), layout.Flexed(1, body))
	if full {
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.usageStrip(gtx, c) }))
	}
	if why := u.snoozeAsk[id]; why != "" && !archived {
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.snoozeBar(gtx, c, why) }))
	}
	if ask != nil && !archived { // docked: it can't scroll out of view, over the canvas too
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.askBox(gtx, id, ask, &act) }))
	}
	if !archived {
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.composer(gtx, c) }))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

func (u *ui) composer(gtx layout.Context, c *kanban.Card) layout.Dimensions {
	k := u.k
	running := u.shellStop[c.ID] != nil // the user's "!" command: the box is locked until it ends and Claude has its output
	working := c.Status == kanban.StatusWorking && !running
	u.chatIn.ReadOnly = running
	shell := bang(u.chatIn.Text()) != "" && !running // "!": the user's own command, run directly: the box warns
	return layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		rim, fill, face, fg := kit.CLine, kit.CPane, kit.Sans, kit.CText
		if gtx.Focused(&u.chatIn) {
			rim = kit.CAcc
		}
		if shell {
			rim, fill, face, fg = kit.COrange, kit.COrangeSoft, kit.Mono, kit.COrange
		}
		if running {
			rim, fill = kit.CLine, kit.CPanel
		}
		var kids []layout.FlexChild
		if shell {
			where := strings.Replace(orgs.Dir(u.root, c.Org, c.Dir), u.home, "~", 1)
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Bottom: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(kit.Icon(13, kit.COrange, kit.IcoWarn)),
						layout.Rigid(kit.HGap(6)),
						layout.Flexed(1, k.LblFill("WARNING: runs directly in "+where+", now: no Claude, no approval", 12, kit.COrange, kit.Semi(kit.Sans))))
				})
			}))
		}
		kids = append(kids,
			layout.Rigid(kit.Boxed(fill, rim, kit.RCard, layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, gtx.Dp(44))
				gtx.Constraints.Max.Y = gtx.Dp(200)
				hint := "Message Claude… (! runs a shell command)"
				switch {
				case running:
					hint = "Your command is running: Claude gets its output, exit code and time when it ends"
				case working:
					hint = "Claude is working; your message goes next"
				}
				return k.Editor(gtx, &u.chatIn, hint, chatSp, face, fg)
			})),
			layout.Rigid(kit.VGap(8)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				keys := "↵ send   ⇧↵ newline   esc leaves"
				switch {
				case running:
					keys = "the box opens again when your command ends"
				case shell:
					keys = "↵ runs it   Claude gets its output when it ends"
				}
				kids := []layout.FlexChild{
					layout.Flexed(1, k.LblFill(keys, 11, kit.CText3, kit.Sans)),
				}
				if stop := u.shellStop[c.ID]; stop != nil {
					kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d, ok := k.Button(gtx, "pnShellStop", kit.CPanelCard, kit.CHover, kit.RCtl, layout.Inset{Top: 5, Bottom: 5, Left: 10, Right: 12}, func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx, layout.Rigid(kit.Icon(12, kit.COrange, kit.IcoStop)), layout.Rigid(kit.HGap(6)), layout.Rigid(k.Lbl("Stop command", 12, kit.CText, kit.Semi(kit.Sans))))
						})
						if ok {
							stop()
						}
						return d
					}), layout.Rigid(kit.HGap(6)))
				}
				if working {
					kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d, ok := k.Button(gtx, "pnStop", kit.CPanelCard, kit.CHover, kit.RCtl, layout.Inset{Top: 5, Bottom: 5, Left: 10, Right: 12}, func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx, layout.Rigid(kit.Icon(12, kit.CRed, kit.IcoStop)), layout.Rigid(kit.HGap(6)), layout.Rigid(k.Lbl("Stop", 12, kit.CText, kit.Semi(kit.Sans))))
						})
						if ok {
							u.stop(c.ID)
						}
						return d
					}), layout.Rigid(kit.HGap(6)))
				}
				if running {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...) // no Send: Stop command, or wait
				}
				kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					bg, hov, label := kit.CAcc, kit.CAccText, "Send"
					if shell {
						bg, hov, label = kit.COrange, kit.Mix(kit.COrange, kit.CWhite, .8), "Run"
					}
					d, ok := k.Button(gtx, "pnSend", bg, hov, kit.RCtl, layout.Inset{Top: 5, Bottom: 5, Left: 10, Right: 12}, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx, layout.Rigid(kit.Icon(12, kit.CWhite, kit.IcoSend)), layout.Rigid(kit.HGap(6)), layout.Rigid(k.Lbl(label, 12, kit.CWhite, kit.Semi(kit.Sans))))
					})
					if ok && strings.TrimSpace(u.chatIn.Text()) != "" {
						u.send(c.ID, u.chatIn.Text())
						u.chatIn.SetText("")
					}
					return d
				}))
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
			}))
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})
}

// The conversation's type: body size and line height (the rest of the
// window keeps its own, smaller sizes).
const (
	chatSp   unit.Sp = 15
	chatLead         = 1.3
)

// message draws chat entry id; follow (same speaker as the one before)
// drops the name line. Claude's replies are markdown; the rest selectable.
func (u *ui) message(gtx layout.Context, card, id string, m kanban.ChatMsg, follow bool) layout.Dimensions {
	k := u.k
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	head := func(who string, c color.NRGBA) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if follow {
				return layout.Dimensions{}
			}
			return layout.Inset{Bottom: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(k.Lbl(who, chatSp-1, c, kit.Semi(kit.Sans))),
					layout.Rigid(kit.HGap(8)),
					layout.Rigid(k.Lbl(ago(m.At, time.Now()), chatSp-2, kit.CText3, kit.Sans)))
			})
		})
	}
	switch m.Who {
	case "shell": // a "!" command the user ran directly: its line and status, then what it printed
		status := m.Detail
		switch m.Path {
		case toClaudeNext:
			status += " · Claude gets it with your next message"
		case toClaudeSent:
			status += " · sent to Claude"
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, head("You ran", kit.COrange),
			layout.Rigid(kit.Boxed(kit.CPane, kit.COrange, kit.RCard, layout.Inset{Top: 8, Bottom: 8, Left: 10, Right: 10}, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				kids := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return u.selText(gtx, "msg"+id+"cmd", "! "+m.Cmd, chatSp-1, kit.COrange, kit.Mono, 0)
						}),
						layout.Rigid(kit.HGap(8)),
						layout.Rigid(k.Lbl(status, chatSp-3, kit.CText3, kit.Sans)))
				})}
				if m.Text != "" {
					kids = append(kids, layout.Rigid(kit.VGap(6)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return u.selText(gtx, "msg"+id, m.Text, chatSp-2, kit.CText2, kit.Mono, 0)
					}))
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
			})))
	case "tool": // a shell command also says what it does: Claude's description, Haiku's take on asking
		row := []layout.FlexChild{
			layout.Rigid(k.Lbl(">", chatSp-2, kit.CPurple, kit.Mono)),
			layout.Rigid(kit.HGap(6)),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return u.selText(gtx, "msg"+id, m.Text, chatSp-2, kit.CText3, kit.Mono, 1)
			}),
		}
		if m.Cmd != "" {
			row = append(row, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.explainBtn(gtx, card, m.Cmd) }))
		}
		line := func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, row...)
		}
		wrote1 := len(m.Edits) == 0 && m.Path != "" && strings.HasPrefix(m.Text, "Write")
		if m.Detail == "" && (m.Cmd == "" || u.explains[m.Cmd] == nil) && len(m.Edits) == 0 && !wrote1 {
			return line(gtx)
		}
		kids := []layout.FlexChild{layout.Rigid(line)}
		if m.Detail != "" {
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: 14, Top: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return k.Label(gtx, m.Detail, chatSp-2, kit.CText2, kit.Sans, 2)
				})
			}))
		}
		if len(m.Edits) > 0 { // what an Edit changed
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: 14, Top: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return u.diffBlock(gtx, card, id, m) })
			}))
		}
		if wrote1 { // a Write: its size, not its text
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: 14, Top: 2}.Layout(gtx, k.Lbl(wrote(m), chatSp-2, kit.CText2, kit.Sans))
			}))
		}
		if m.Cmd != "" && u.explains[m.Cmd] != nil {
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: 14, Top: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return u.explainBox(gtx, card, m.Cmd, false) })
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	case "you", "prompt", "peer", "workspace", "main": // prompt: what the session asked a subagent; peer: what the infra Claude sent; workspace: the workspace Claude's to the card's; main: the card's to it
		who, wc := "You", kit.CAccText
		switch m.Who {
		case "prompt":
			who, wc = "Prompt", kit.CPurple
		case "peer":
			who, wc = "Infra Claude", kit.CGreen
		case "workspace":
			who, wc = "Workspace Claude", kit.Accents[5]
		case "main":
			who, wc = "Card's Claude", kit.COrange
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, head(who, wc),
			layout.Rigid(kit.Boxed(kit.CPanelCard, color.NRGBA{}, kit.RCard, layout.Inset{Top: 8, Bottom: 8, Left: 10, Right: 10}, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return u.selText(gtx, "msg"+id, m.Text, chatSp, kit.CText, kit.Sans, 0)
			})))
	case "error":
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, head("Error", kit.CRed),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.selText(gtx, "msg"+id, m.Text, chatSp-1, kit.CRed, kit.Sans, 0)
			}))
	}
	tools := func(gtx layout.Context) layout.Dimensions { return u.msgTools(gtx, id, m) }
	top := layout.Rigid(func(gtx layout.Context) layout.Dimensions { // name and time, then Copy and Select at the far end
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		kids := []layout.FlexChild{layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)}
		}), layout.Rigid(tools)}
		if !follow {
			kids = append([]layout.FlexChild{
				layout.Rigid(k.Lbl("Claude", chatSp-1, kit.COrange, kit.Semi(kit.Sans))),
				layout.Rigid(kit.HGap(8)),
				layout.Rigid(k.Lbl(ago(m.At, time.Now()), chatSp-2, kit.CText3, kit.Sans)),
			}, kids...)
		}
		return layout.Inset{Bottom: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
		})
	})
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, top,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.claudeMsg(gtx, id, m) }))
}
