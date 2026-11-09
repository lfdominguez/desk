package ui

import (
	"fmt"
	"image"
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/draw"
	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// A drawing's own Claude: the chat beside the canvas in its view, a
// session of its own (kept in the drawing's sidecar, resumed), which
// reads files, draws through the drawing tools and never writes or runs.
// Its conversation is the chat "draw:<path>", in a file beside the
// drawing (chatFile).

// drawClaude is an open drawing's Claude and its chat panel.
type drawClaude struct {
	in   widget.Editor
	list layout.List
	proc session
	busy int // turns asked and not finished
	hide bool
}

func drawChatKey(path string) string { return "draw:" + path }

// drawStart starts st's Claude: the organization's account, in its folder.
func (u *ui) drawStart(st *drawState) (session, error) {
	acct := orgs.Account(u.root, st.org)
	dir := orgs.Dir(u.root, st.org, "")
	card := ""
	if c := u.b.Card(st.card); c != nil {
		card = c.Key()
	}
	o := claude.Options{Home: u.home, Org: acct, Dir: dir, Root: dir, System: claude.Drawing(drawName(st.path), st.org, card)}
	mcp, allow := infra.DrawMCP(u.cardSock, st.path)
	if mcp != "" {
		o.Args = append(o.Args, "--mcp-config", mcp)
	}
	o.Args = append(o.Args, "--allowedTools="+strings.Join(append([]string{"Read", "Glob", "Grep"}, allow...), ","),
		"--disallowedTools=Edit,Write,NotebookEdit,Bash")
	if st.side.Session == "" {
		st.side.Session = claude.NewSessionID()
		u.drawKeepSide(st)
	}
	if claude.SessionExists(u.home, acct, st.side.Session) {
		o.Resume = st.side.Session
	} else {
		o.New = st.side.Session
	}
	path := st.path
	var p session
	p, err := startClaude(o, func(e claude.Event) { u.later(func() { u.drawEvent(path, p, e) }) })
	return p, err
}

// drawKeepSide writes st's sidecar, saying so when it can't.
func (u *ui) drawKeepSide(st *drawState) {
	if err := saveSide(st.path, st.side); err != nil {
		u.showToast("Couldn't keep the drawing's notes: "+err.Error(), true)
	}
}

// drawAsk hands prompt to st's Claude, starting it first.
func (u *ui) drawAsk(st *drawState, prompt string) error {
	if st.claude.proc == nil {
		p, err := u.drawStart(st)
		if err != nil {
			return err
		}
		st.claude.proc = p
	}
	if err := st.claude.proc.Send(prompt); err != nil {
		return err
	}
	st.claude.busy++
	st.ed.Gen++ // the status line
	return nil
}

// drawSend is the user's message to st's Claude, with what they selected.
func (u *ui) drawSend(st *drawState, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	prompt := text
	if sel := st.ed.Selected(); len(sel) > 0 {
		s := draw.NewScene()
		s.Elements = sel
		prompt = "<selected>\n" + draw.Summary(s) + "</selected>\n\n" + text
	}
	if err := u.drawAsk(st, prompt); err != nil {
		u.showToast("Drawing's Claude: "+err.Error(), true)
		return
	}
	u.say(drawChatKey(st.path), "you", text)
	st.claude.in.SetText("")
}

// drawEvent applies one of the drawing at path's Claude's events (p: that
// process; a replaced one's are dropped).
func (u *ui) drawEvent(path string, p session, e claude.Event) {
	st := u.draws[path]
	if st == nil || st.claude.proc != p {
		return
	}
	st.ed.Gen++
	key := drawChatKey(path)
	switch e.Kind {
	case claude.KindInit:
		if e.Session != "" && st.side.Session != e.Session {
			st.side.Session = e.Session
			u.drawKeepSide(st)
		}
	case claude.KindText:
		if t := strings.TrimSpace(e.Text); t != "" && e.Parent == "" {
			u.say(key, "claude", t)
		}
	case claude.KindTool:
		if e.Parent == "" {
			u.sayMsg(key, toolMsg(e))
		}
	case claude.KindAsk: // it only reads and draws
		p.Respond(e.Ask.ID, false, nil, "The drawing's Claude only reads files and draws.")
		u.say(key, "tool", "refused "+e.Ask.Line)
	case claude.KindResult:
		st.claude.busy = max(st.claude.busy-1, 0)
		if e.Error && e.Text != "" {
			u.say(key, "error", e.Text)
		}
	case claude.KindExit:
		st.claude.proc, st.claude.busy = nil, 0
		if e.Text != "" {
			u.say(key, "error", e.Text)
		}
	}
}

// drawStop ends st's Claude (the drawing renamed, deleted): the next
// message starts it again, resumed.
func (st *drawState) drawStop() {
	if st.claude.proc != nil {
		st.claude.proc.Close()
		st.claude.proc, st.claude.busy = nil, 0
	}
}

// drawChat is the drawing's Claude's conversation and composer, beside the canvas.
func (u *ui) drawChat(gtx layout.Context, st *drawState) layout.Dimensions {
	k := u.k
	c := &st.claude
	gtx.Constraints = layout.Exact(image.Pt(gtx.Dp(360), gtx.Constraints.Max.Y))
	kit.Fill(gtx, kit.CPanel)
	for {
		ev, ok := c.in.Update(gtx)
		if !ok {
			break
		}
		if s, ok := ev.(widget.SubmitEvent); ok {
			u.drawSend(st, s.Text)
		}
	}
	if u.focusLater == &c.in {
		gtx.Execute(key.FocusCmd{Tag: &c.in})
		u.focusLater = nil
	}
	key := drawChatKey(st.path)
	msgs := u.chat(key)
	status := "Draws when you ask; suggests the rest"
	if c.busy > 0 {
		status = "Working…"
	}
	head := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 10, Bottom: 10, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(k.Lbl("Drawing's Claude", 14, kit.CText, kit.Semi(kit.Sans))),
				layout.Rigid(kit.VGap(2)),
				layout.Rigid(k.Lbl(status, 11, kit.CText3, kit.Sans)))
		})
	}
	body := func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = gtx.Constraints.Max
		kit.Fill(gtx, kit.CColumn)
		if len(msgs) == 0 {
			return layout.UniformInset(16).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return k.Label(gtx, "Ask it to draw or change something: what you have selected goes with your message. Its changes show marked and undo with Ctrl+Z; what you didn't ask for comes as a suggestion to approve.", 13, kit.CText3, kit.Sans, 0)
			})
		}
		c.list.Axis, c.list.ScrollToEnd = layout.Vertical, true // newest at the bottom, followed
		return c.list.Layout(gtx, len(msgs), func(gtx layout.Context, i int) layout.Dimensions {
			return layout.Inset{Top: 6, Bottom: 6, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return u.message(gtx, key, fmt.Sprintf("dc%s-%d", st.path, i), msgs[i], i > 0 && msgs[i-1].Who == msgs[i].Who)
			})
		})
	}
	with := "just your words"
	if n := len(st.ed.Selected()); n > 0 {
		with = fmt.Sprintf("%d selected", n)
	}
	composer := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			rim := kit.CLine
			if gtx.Focused(&c.in) {
				rim = kit.CAcc
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(kit.Boxed(kit.CPane, rim, kit.RCard, layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, gtx.Dp(44))
					gtx.Constraints.Max.Y = gtx.Dp(200)
					return k.Editor(gtx, &c.in, "Ask the drawing's Claude…", chatSp-1, kit.Sans, kit.CText)
				})),
				layout.Rigid(kit.VGap(6)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, k.LblFill("↵ sends with "+with, 11, kit.CText3, kit.Sans)),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							d, ok := k.Button(gtx, "drawSend", kit.CAcc, kit.CAccText, kit.RCtl, layout.Inset{Top: 5, Bottom: 5, Left: 10, Right: 12}, func(gtx layout.Context) layout.Dimensions {
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx, layout.Rigid(kit.Icon(12, kit.CWhite, kit.IcoSend)), layout.Rigid(kit.HGap(6)), layout.Rigid(k.Lbl("Send", 12, kit.CWhite, kit.Semi(kit.Sans))))
							})
							if ok {
								u.drawSend(st, c.in.Text())
							}
							return d
						}))
				}))
		})
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, layout.Rigid(head), layout.Rigid(kit.VGap(1)), layout.Flexed(1, body), layout.Rigid(composer))
}
