package ui

import (
	"fmt"
	"image"
	"image/color"
	"path/filepath"
	"strings"

	"gioui.org/io/key"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"github.com/oligo/gvcode"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// openWS shows card id's workspace; its open files check the disk.
func (u *ui) openWS(id string) {
	if u.b.Card(id) == nil {
		return
	}
	u.view, u.panel, u.sel = "ws:"+id, "", id
	st := u.wsOf(id)
	for _, t := range st.tabs {
		u.wsCheck(t)
	}
	u.wsRefresh(st, "") // what changed while it wasn't shown (a shell's work, say)
}

// wsSend sends the user's message to the workspace Claude, with what they
// are looking at.
func (u *ui) wsSend(id, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	st := u.wsOf(id)
	prompt := text
	if ctx := wsContext(st, text); ctx != "" {
		prompt = ctx + "\n\n" + text
	}
	if err := u.wsAsk(id, prompt, "you"); err != nil {
		u.showToast("Workspace Claude: "+err.Error(), true)
		return
	}
	u.say(wsKey(id), "you", text)
	st.in.SetText("")
}

// wsTyping is whether a workspace editor (a file's or the composer) has the keyboard.
func (u *ui) wsTyping(gtx layout.Context) bool {
	for _, st := range u.ws {
		if gtx.Focused(&st.in) {
			return true
		}
		for _, t := range st.tabs {
			if gtx.Focused(t.ed) {
				return true
			}
		}
	}
	return false
}

// wsView is card id's workspace over the main area: the projects' tree,
// the open files, the workspace Claude's chat.
func (u *ui) wsView(gtx layout.Context) layout.Dimensions {
	id := strings.TrimPrefix(u.view, "ws:")
	gtx.Constraints.Min = gtx.Constraints.Max
	kit.Fill(gtx, kit.CColumn)
	c := u.b.Card(id)
	if c == nil {
		return layout.Center.Layout(gtx, u.k.Lbl("This card is gone.", 13, kit.CText3, kit.Sans))
	}
	st := u.wsOf(id)
	var act func() // what a click changes that the rest draws from: run once all is drawn
	defer func() {
		if act != nil {
			act()
		}
	}()
	k := u.k
	header := func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		kit.Fill(gtx, kit.CPanel)
		return layout.Inset{Top: 8, Bottom: 8, Left: 16, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(k.Lbl(c.Key(), 12, kit.CText3, kit.Mono)), layout.Rigid(kit.HGap(10)),
				layout.Flexed(1, k.LblFill(c.Title, 14, kit.CText, kit.Semi(kit.Sans))),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.IconBtn(gtx, "wsBack", kit.IcoChat, false)
					k.Tip(gtx, "wsBack", d, "Back to the session (Esc)", true)
					if ok {
						act = func() { u.view = "sess:" + id } // no blur: Gio drops the focus of editors no longer laid out
					}
					return d
				}))
		})
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(header),
		layout.Rigid(kit.VGap(1)),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			tw, cw := u.wsPanes(gtx)
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.wsTree(gtx, c, st, tw, &act) }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.wsEdge(gtx, 0, tw) }),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return u.wsCenter(gtx, id, st, &act) }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.wsEdge(gtx, 1, cw) }),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.wsChat(gtx, id, st, cw) }))
		}))
}

// wsRow is a line of the tree.
type wsRow struct {
	e     wsEntry
	depth int
	root  int // a project's own row: 1 the card's folder, 2 one added; 0 inside one
}

// wsRows is the tree as shown: each project, and inside the folders open.
func (u *ui) wsRows(c *kanban.Card, st *wsState) []wsRow {
	var rows []wsRow
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		for _, e := range st.kids[dir] {
			rows = append(rows, wsRow{e: e, depth: depth})
			if st.open[e.path] { // only folders open
				walk(e.path, depth+1)
			}
		}
	}
	for i, p := range u.projects(c) {
		rows = append(rows, wsRow{e: wsEntry{filepath.Base(p), p, true}, root: min(i, 1) + 1})
		if st.open[p] {
			walk(p, 1)
		}
	}
	return rows
}

func (u *ui) wsTree(gtx layout.Context, c *kanban.Card, st *wsState, w int, act *func()) layout.Dimensions {
	k := u.k
	gtx.Constraints = layout.Exact(image.Pt(w, gtx.Constraints.Max.Y))
	kit.Fill(gtx, kit.CPanel)
	rows := u.wsRows(c, st)
	cur := ""
	if t := st.tab(); t != nil {
		cur = t.path
	}
	return layout.Inset{Top: 6, Bottom: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return st.tree.Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
			r := rows[i]
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			fill, fg, face := color.NRGBA{}, kit.CText2, kit.Sans
			on := r.e.path == cur
			if on {
				fill, fg = kit.CAccSoft, kit.CText
			}
			if r.root > 0 {
				fg, face = kit.CText, kit.Semi(kit.Sans)
			}
			mark, icon := "", k.FileIcon(r.e.name, 16)
			if r.e.dir {
				open := st.open[r.e.path]
				mark, icon = "▸", kit.FolderIcon(16, kit.CAccText, open)
				if open {
					mark = "▾"
				}
			}
			row := layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				d, ok := k.Button(gtx, "wsRow"+r.e.path, fill, kit.CHover, kit.RCtl, layout.Inset{Top: 3, Bottom: 3, Left: unit.Dp(8 + 12*r.depth), Right: 6}, func(gtx layout.Context) layout.Dimensions {
					semantic.SelectedOp(on).Add(gtx.Ops) // the file shown, for screen readers
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(kit.IconBox(14, k.Lbl(mark, 12, kit.CText3, kit.Mono))), // a fixed width: ▸ comes from a fallback font
						layout.Rigid(icon), layout.Rigid(kit.HGap(5)),
						layout.Flexed(1, k.LblFill(r.e.name, 12, fg, face)))
				})
				if ok {
					e := r.e
					*act = func() {
						if e.dir {
							u.wsToggle(st, e.path)
						} else {
							u.wsOpen(st, e.path)
						}
					}
				}
				return d
			})
			kids := []layout.FlexChild{row}
			if r.root == 2 { // an added project leaves the workspace
				kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.Button(gtx, "wsDrop"+r.e.path, color.NRGBA{}, kit.CHover, kit.RCtl, layout.UniformInset(3), kit.Icon(12, kit.CText3, kit.IcoX))
					k.Tip(gtx, "wsDrop"+r.e.path, d, "Remove from the workspace", true)
					if ok {
						p := r.e.path
						*act = func() { u.removeProject(c.ID, p) }
					}
					return d
				}))
			}
			return layout.Inset{Left: 6, Right: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
			})
		})
	})
}

// wsCenter is the open files: their tabs, the bars about the one shown
// and its editor.
func (u *ui) wsCenter(gtx layout.Context, id string, st *wsState, act *func()) layout.Dimensions {
	k := u.k
	gtx.Constraints.Min = gtx.Constraints.Max
	kit.Fill(gtx, kit.CPane)
	t := st.tab()
	if t == nil {
		return layout.Center.Layout(gtx, k.Lbl("Open a file from the tree on the left.", 13, kit.CText3, kit.Sans))
	}
	for {
		ev, ok := t.ed.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(gvcode.ChangeEvent); ok {
			t.dirty, t.closing = true, false
			t.gen++
			u.wsRedraw()
		}
	}
	u.wsLex(t)
	tabs := func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		kit.Fill(gtx, kit.CPanel)
		if p := st.tabBar.Position; st.cur != st.shown { // a tab just shown scrolls into view; a scroll by hand stays
			if st.cur < p.First || st.cur >= p.First+p.Count {
				st.tabBar.ScrollTo(st.cur)
			}
			st.shown = st.cur
		}
		return st.tabBar.Layout(gtx, len(st.tabs), func(gtx layout.Context, i int) layout.Dimensions {
			tb, on := st.tabs[i], i == st.cur
			fill, fg := kit.CPanel, kit.CText3
			if on {
				fill, fg = kit.CPane, kit.CText
			}
			name := filepath.Base(tb.path)
			if tb.dirty {
				name += " ●"
			}
			d, ok := k.Button(gtx, "wsTab"+tb.path, fill, kit.CHover, 0, layout.Inset{Top: 7, Bottom: 7, Left: 12, Right: 4}, func(gtx layout.Context) layout.Dimensions {
				semantic.SelectedOp(on).Add(gtx.Ops) // the tab shown, for screen readers
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(k.FileIcon(tb.path, 16)), layout.Rigid(kit.HGap(4)),
					layout.Rigid(k.Lbl(name, 12, fg, kit.Medium(kit.Sans))), layout.Rigid(kit.HGap(6)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d, ok := k.Button(gtx, "wsTabX"+tb.path, color.NRGBA{}, kit.CHover, kit.RCtl, layout.UniformInset(2), kit.Icon(11, kit.CText3, kit.IcoX))
						if ok {
							n := i
							*act = func() { u.wsCloseTab(st, n) }
						}
						return d
					}))
			})
			if ok && *act == nil {
				n := i
				*act = func() {
					st.cur = n
					u.wsCheck(st.tabs[n])
					u.wsRedraw()
				}
			}
			return d
		})
	}
	bar := func(gtx layout.Context, fg color.NRGBA, msg string, btns ...layout.FlexChild) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		kit.Fill(gtx, kit.Mix(fg, kit.CPanel, .86))
		return layout.Inset{Top: 5, Bottom: 5, Left: 12, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, append([]layout.FlexChild{layout.Flexed(1, k.LblFill(msg, 12, fg, kit.Medium(kit.Sans)))}, btns...)...)
		})
	}
	btn := func(bid, label string, f func()) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			d, ok := k.Button(gtx, bid, kit.CPanelCard, kit.CHover, kit.RCtl, layout.Inset{Top: 4, Bottom: 4, Left: 10, Right: 10}, k.Lbl(label, 12, kit.CText, kit.Semi(kit.Sans)))
			if ok {
				*act = f
			}
			return layout.Inset{Left: 6}.Layout(gtx, func(layout.Context) layout.Dimensions { return d })
		})
	}
	kids := []layout.FlexChild{layout.Rigid(tabs), layout.Rigid(kit.VGap(1))}
	if t.disk {
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return bar(gtx, kit.COrange, "Changed on disk while you edit it.",
				btn("wsReload", "Reload", func() { u.wsReload(t) }), btn("wsKeep", "Keep mine", func() { u.wsKeepMine(t) }))
		}))
	}
	if t.edit != nil {
		msg := "The change to " + filepath.Base(t.path) + " from the chat, selected"
		if t.note != "" {
			msg = t.note
		}
		e := *t.edit
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return bar(gtx, kit.CAccText, msg,
				btn("wsAskChange", "Ask about this change", func() {
					st.in.SetText(changeText(t.path, e) + "\n\n")
					st.in.SetCaret(st.in.Len(), st.in.Len())
					u.focusLater = &st.in
					u.wsRedraw()
				}),
				btn("wsDoneChange", "Done", func() {
					t.edit, t.note = nil, ""
					t.diff.ClearDiff()
					u.wsRedraw()
				}))
		}))
	}
	kids = append(kids, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			d := t.ed.Layout(gtx, k.Sh)
			if w := t.want; w != nil { // now that its text is shaped; the next frame scrolls to it
				t.want = nil
				t.ed.SetCaret(w[0], w[1])
				u.wsRedraw()
			}
			return d
		})
	}), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		line, col := t.ed.CaretPos()
		return layout.Inset{Top: 4, Bottom: 4, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			wrap := kit.CText3
			if u.wsEd.Wrap {
				wrap = kit.CAccText
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, k.LblFill(strings.Replace(t.path, u.home, "~", 1), 11, kit.CText3, kit.Mono)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.Button(gtx, "wsWrap", color.NRGBA{}, kit.CHover, kit.RCtl, layout.Inset{Left: 6, Right: 6}, k.Lbl("Wrap", 11, wrap, kit.Medium(kit.Sans)))
					k.Tip(gtx, "wsWrap", d, "Wrap long lines, in every tab (Alt+Z)", true)
					if ok {
						*act = u.wsToggleWrap
					}
					return d
				}),
				layout.Rigid(kit.HGap(10)),
				layout.Rigid(k.Lbl(fmt.Sprintf("Ln %d, Col %d   %s saves", line+1, col+1, kit.ModText("S")), 11, kit.CText3, kit.Sans)))
		})
	}))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

// wsChat is the workspace Claude's conversation and its composer.
func (u *ui) wsChat(gtx layout.Context, id string, st *wsState, w int) layout.Dimensions {
	k := u.k
	gtx.Constraints = layout.Exact(image.Pt(w, gtx.Constraints.Max.Y))
	kit.Fill(gtx, kit.CPanel)
	for {
		ev, ok := st.in.Update(gtx)
		if !ok {
			break
		}
		if s, ok := ev.(widget.SubmitEvent); ok {
			u.wsSend(id, s.Text)
		}
	}
	if u.focusLater == &st.in {
		gtx.Execute(key.FocusCmd{Tag: &st.in})
		u.focusLater = nil
	}
	msgs := u.chat(wsKey(id))
	status := "Reads the projects with you; never writes"
	if len(st.from) > 0 {
		status = "Working…"
	}
	head := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 10, Bottom: 10, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(k.Lbl("Workspace Claude", 14, kit.CText, kit.Semi(kit.Sans))),
				layout.Rigid(kit.VGap(2)),
				layout.Rigid(k.Lbl(status, 11, kit.CText3, kit.Sans)))
		})
	}
	body := func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = gtx.Constraints.Max
		kit.Fill(gtx, kit.CColumn)
		if len(msgs) == 0 {
			return layout.UniformInset(16).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return k.Label(gtx, "Ask about the code: what you have open or selected goes with your message. It suggests changes as diffs; the card's Claude can ask it too.", 13, kit.CText3, kit.Sans, 0)
			})
		}
		return st.chat.Layout(gtx, len(msgs), func(gtx layout.Context, i int) layout.Dimensions {
			return layout.Inset{Top: 6, Bottom: 6, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return u.message(gtx, id, fmt.Sprintf("ws%s-%d", id, i), msgs[i], i > 0 && msgs[i-1].Who == msgs[i].Who)
			})
		})
	}
	with := "nothing open: just your words"
	if t := st.tab(); t != nil {
		with = filepath.Base(t.path)
		switch s, e := t.ed.Selection(); {
		case t.edit != nil:
			with = "the change to " + with
		case s != e:
			with = "the selection in " + with
		}
	}
	composer := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			rim := kit.CLine
			if gtx.Focused(&st.in) {
				rim = kit.CAcc
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(kit.Boxed(kit.CPane, rim, kit.RCard, layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, gtx.Dp(44))
					gtx.Constraints.Max.Y = gtx.Dp(200)
					return k.Editor(gtx, &st.in, "Ask the workspace Claude…", chatSp-1, kit.Sans, kit.CText)
				})),
				layout.Rigid(kit.VGap(6)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, k.LblFill("↵ sends with "+with, 11, kit.CText3, kit.Sans)),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							d, ok := k.Button(gtx, "wsSend", kit.CAcc, kit.CAccText, kit.RCtl, layout.Inset{Top: 5, Bottom: 5, Left: 10, Right: 12}, func(gtx layout.Context) layout.Dimensions {
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx, layout.Rigid(kit.Icon(12, kit.CWhite, kit.IcoSend)), layout.Rigid(kit.HGap(6)), layout.Rigid(k.Lbl("Send", 12, kit.CWhite, kit.Semi(kit.Sans))))
							})
							if ok {
								u.wsSend(id, st.in.Text())
							}
							return d
						}))
				}))
		})
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, layout.Rigid(head), layout.Rigid(kit.VGap(1)), layout.Flexed(1, body), layout.Rigid(composer))
}
