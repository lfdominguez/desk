package ui

import (
	"encoding/json"
	"fmt"
	"image"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// askState is a card's open request from Claude: an interview
// (AskUserQuestion) or a tool waiting for permission. The turn waits on it.
type askState struct {
	r       *claude.Request
	picks   [][]bool        // per question, per option
	other   []widget.Editor // per question, a free answer
	cur     int             // the interview's question on screen
	cmd     widget.Editor   // the command, while edited before allowing it
	editing bool
	more    bool // the other ways to allow are listed
	focus   bool // Edit was asked: fill and focus the command next frame
}

// tick is a checkbox's check mark.
var tick kit.IconPath = func(p *clip.Path, s float32) {
	p.MoveTo(f32.Pt(s*.26, s*.52))
	p.LineTo(f32.Pt(s*.43, s*.69))
	p.LineTo(f32.Pt(s*.75, s*.33))
}

func newAsk(r *claude.Request) *askState {
	a := &askState{r: r, picks: make([][]bool, len(r.Questions)), other: make([]widget.Editor, len(r.Questions))}
	for i, q := range r.Questions {
		a.picks[i] = make([]bool, len(q.Options))
		a.other[i].SingleLine, a.other[i].Submit = true, true
	}
	a.cmd.Submit = true
	return a
}

// command is the Bash command a permission asks for, "" for other tools:
// only a command can be edited before it runs.
func (a *askState) command() string {
	if a.r.Tool != "Bash" {
		return ""
	}
	var in struct{ Command string }
	json.Unmarshal(a.r.Input, &in)
	return in.Command
}

// interviewText is the questions as the chat keeps them.
func interviewText(qs []claude.Question) string {
	var b strings.Builder
	for i, q := range qs {
		var ls []string
		for _, o := range q.Options {
			ls = append(ls, o.Label)
		}
		fmt.Fprintf(&b, "%d. %s (%s)\n", i+1, q.Question, strings.Join(ls, " / "))
	}
	return strings.TrimSpace(b.String())
}

// answered is question i's answer: its picks, then the free one.
func (a *askState) answered(i int) []string {
	var got []string
	for j, on := range a.picks[i] {
		if on {
			got = append(got, a.r.Questions[i].Options[j].Label)
		}
	}
	if s := strings.TrimSpace(a.other[i].Text()); s != "" {
		got = append(got, s)
	}
	return got
}

// next moves the interview on, or sends it from the last question.
func (u *ui) next(id string) {
	a := u.asks[id]
	if a == nil {
		return
	}
	if len(a.r.Questions) == 0 || a.cur == len(a.r.Questions)-1 {
		u.answerAsk(id, true)
		return
	}
	if len(a.answered(a.cur)) == 0 {
		u.showToast("Pick an option or write your own", true)
		return
	}
	a.cur++
}

// allowAll answers card id's permission and lets every later use of the
// same tool through while desk runs.
func (u *ui) allowAll(id string) {
	a := u.asks[id]
	if a == nil {
		return
	}
	if u.allowed[id] == nil {
		u.allowed[id] = map[string]bool{}
	}
	u.allowed[id][a.r.Tool] = true // ponytail: until desk quits, per tool; a rule per command if it proves too wide
	u.answerAsk(id, true)
}

// answerAsk sends what the box holds: the interview's answers, or the
// permission (allow, with the command as edited); Claude's turn goes on.
func (u *ui) answerAsk(id string, allow bool) {
	a, p, c := u.asks[id], u.procs[id], u.b.Card(id)
	if a == nil || c == nil {
		return
	}
	if p == nil { // the process is gone: nothing waits for it
		delete(u.asks, id)
		return
	}
	input, said := a.r.Input, map[bool]string{true: "Allowed: ", false: "Denied: "}[allow]+a.r.Line
	if a.editing && allow {
		var in map[string]any
		json.Unmarshal(a.r.Input, &in)
		in["command"] = a.cmd.Text()
		input, _ = json.Marshal(in)
		said = "Allowed, edited: " + a.cmd.Text()
	}
	if len(a.r.Questions) > 0 {
		answers := map[string]string{}
		var lines []string
		for i, q := range a.r.Questions {
			got := a.answered(i)
			if len(got) == 0 {
				a.cur = i
				u.showToast("Answer every question (pick an option or write one)", true)
				return
			}
			answers[q.Question] = strings.Join(got, ", ")
			lines = append(lines, q.Question+" → "+answers[q.Question])
		}
		input, said, allow = claude.Answered(a.r.Input, answers), strings.Join(lines, "\n"), true
	}
	if err := p.Respond(a.r.ID, allow, json.RawMessage(input), "The user denied it."); err != nil {
		u.showToast("Claude: "+err.Error(), true)
		return
	}
	delete(u.asks, id)
	u.say(id, "you", said)
	c.Status, c.Note, c.Updated = kanban.StatusWorking, "", time.Now()
	u.moveTo(id, "In progress")
	u.save()
}

// askKeys answers with keys while no editor has them: Y allow, N deny,
// E edit, S allow the tool from now on; an interview's digits pick and
// enter goes on. Its own fields submit with enter too.
func (u *ui) askKeys(gtx layout.Context, id string, a *askState, act *func()) {
	kit.Newline(gtx, &a.cmd)
	for _, e := range append([]*widget.Editor{&a.cmd}, editorsOf(a.other)...) {
		for {
			ev, ok := e.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				*act = func() { u.next(id) }
			}
		}
	}
	if u.edit != nil || u.unlock != nil || u.cfg != nil || u.pick != "" || u.typing(gtx) || gtx.Focused(&a.cmd) {
		return
	}
	names := []key.Name{key.NameReturn, key.NameEnter}
	if len(a.r.Questions) == 0 {
		names = append(names, "Y", "N", "S")
		if a.command() != "" {
			names = append(names, "E")
		}
	} else {
		for j := range len(a.r.Questions[a.cur].Options) + 1 {
			names = append(names, key.Name(fmt.Sprint(j+1)))
		}
	}
	var fs []event.Filter
	for _, n := range names {
		fs = append(fs, key.Filter{Name: n})
	}
	for {
		ev, ok := gtx.Event(fs...)
		if !ok {
			break
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		gtx.Execute(op.InvalidateCmd{})
		switch n := e.Name; {
		case n == key.NameReturn || n == key.NameEnter || n == "Y":
			*act = func() { u.next(id) }
		case n == "N":
			*act = func() { u.answerAsk(id, false) }
		case n == "S":
			*act = func() { u.allowAll(id) }
		case n == "E":
			a.editing, a.focus = true, true
		default: // a digit: that option, or past them the free answer
			q, j := a.r.Questions[a.cur], int(n[0]-'1')
			if j == len(q.Options) {
				gtx.Execute(key.FocusCmd{Tag: &a.other[a.cur]})
				continue
			}
			pick(a, a.cur, j)
		}
	}
}

func editorsOf(es []widget.Editor) []*widget.Editor {
	var ps []*widget.Editor
	for i := range es {
		ps = append(ps, &es[i])
	}
	return ps
}

// pick toggles option j of question i; one answer clears the others.
func pick(a *askState, i, j int) {
	on := a.picks[i][j]
	if !a.r.Questions[i].Multi {
		clear(a.picks[i])
	}
	a.picks[i][j] = !on
}

// askBox draws card id's open request docked above the composer, where it
// can't scroll away; act gets what a button does, run once the pane is drawn.
func (u *ui) askBox(gtx layout.Context, id string, a *askState, act *func()) layout.Dimensions {
	u.askKeys(gtx, id, a, act)
	if a.focus {
		a.focus = false
		a.cmd.SetText(a.command())
		gtx.Execute(key.FocusCmd{Tag: &a.cmd})
	}
	return layout.Inset{Top: 10, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		rim := kit.Mix(kit.COrange, kit.CPanel, .55)
		if len(a.r.Questions) > 0 {
			rim = kit.Mix(kit.CAcc, kit.CPanel, .5)
		}
		return kit.Boxed(kit.CPanel, rim, kit.RSheet, layout.Inset{}, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			if len(a.r.Questions) > 0 {
				return u.interview(gtx, id, a, act)
			}
			return u.permission(gtx, id, a, act)
		})(gtx)
	})
}

// permission is the command Claude wants to run, with what can be done.
func (u *ui) permission(gtx layout.Context, id string, a *askState, act *func()) layout.Dimensions {
	k := u.k
	head := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 12, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(kit.Icon(15, kit.COrange, kit.IcoWarn)), layout.Rigid(kit.HGap(8)),
				layout.Rigid(k.Lbl("Claude wants to run "+map[bool]string{true: "a command", false: "a tool"}[a.r.Tool == "Bash"], 13, kit.CText, kit.Bold(kit.Sans))),
				layout.Rigid(kit.HGap(8)),
				layout.Rigid(k.Pill(a.r.Tool, kit.COrange, kit.COrangeSoft)))
		})
	}
	cmd := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 10, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			rim := kit.CLine
			if a.editing {
				rim = kit.CAcc
			}
			return kit.Boxed(kit.CPane, rim, kit.RCard, layout.Inset{Top: 9, Bottom: 9, Left: 12, Right: 12}, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				if a.editing {
					return k.Editor(gtx, &a.cmd, "", 12, kit.Mono, kit.CText)
				}
				line := a.r.Line
				if c := a.command(); c != "" {
					line = "$ " + c
				}
				return k.Label(gtx, line, 12, kit.CText, kit.Mono, 8)
			})(gtx)
		})
	}
	more := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 8, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return kit.Boxed(kit.CPanelCard, kit.CKeyRim, kit.RCard, layout.UniformInset(4), func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				item := func(bid, s, cap string, run func()) layout.FlexChild {
					return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d, ok := k.Button(gtx, bid, kit.NRGBA0, kit.CHover, kit.RCtl, layout.Inset{Top: 7, Bottom: 7, Left: 8, Right: 8}, func(gtx layout.Context) layout.Dimensions {
							gtx.Constraints.Min.X = gtx.Constraints.Max.X
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Flexed(1, k.LblFill(s, 13, kit.CText, kit.Sans)), layout.Rigid(k.KeyCap(cap, false)))
						})
						if ok {
							*act = run
						}
						return d
					})
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					item("askOnce"+id, "Allow once", "Y", func() { u.answerAsk(id, true) }),
					item("askAll"+id, "Allow every "+a.r.Tool+" on this card until desk quits", "S", func() { u.allowAll(id) }))
			})(gtx)
		})
	}
	foot := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 12, Bottom: 12, Left: 16, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			btn := func(bid string, kind kit.Kind, s, cap string, run func()) layout.FlexChild {
				return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.KBtn(gtx, bid, kind, s, cap)
					if ok {
						*act = run
					}
					return d
				})
			}
			row := []layout.FlexChild{btn("askDeny"+id, kit.Secondary, "Deny", "N", func() { u.answerAsk(id, false) })}
			if a.command() != "" && !a.editing {
				row = append(row, layout.Rigid(kit.HGap(6)), btn("askEdit"+id, kit.Ghost, "Edit", "E", func() { a.editing, a.focus = true, true }))
			}
			row = append(row, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.E.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min = image.Point{}
					return layout.Inset{Right: 12}.Layout(gtx, k.Lbl("or reply below", 12, kit.CText3, kit.Sans))
				})
			}))
			allow := map[bool]string{false: "Allow once", true: "Allow as edited"}[a.editing]
			row = append(row, btn("askAllow"+id, kit.Primary, allow, map[bool]string{false: "Y", true: "↵"}[a.editing], func() { u.answerAsk(id, true) }),
				layout.Rigid(kit.HGap(1)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.Button(gtx, "askMore"+id, kit.CAcc, kit.Mix(kit.CAcc, kit.CWhite, .12), kit.RCtl, layout.Inset{Top: 8, Bottom: 8, Left: 7, Right: 7}, kit.Icon(16, kit.CWhite, kit.IcoDown))
					if ok {
						a.more = !a.more
					}
					return d
				}))
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, row...)
		})
	}
	kids := []layout.FlexChild{layout.Rigid(head), layout.Rigid(cmd)}
	if a.more {
		kids = append(kids, layout.Rigid(more))
	}
	kids = append(kids, layout.Rigid(foot))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

// interview is one of Claude's questions at a time: steps on top, options
// picked by click or digit, circles for one answer and squares for several.
func (u *ui) interview(gtx layout.Context, id string, a *askState, act *func()) layout.Dimensions {
	k, qs := u.k, a.r.Questions
	a.cur = min(a.cur, len(qs)-1)
	i, q := a.cur, a.r.Questions[a.cur]
	head := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 12, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			row := []layout.FlexChild{layout.Rigid(kit.Icon(15, kit.CAccText, kit.IcoChat)), layout.Rigid(kit.HGap(8)),
				layout.Rigid(k.Lbl("Claude asks", 13, kit.CText, kit.Bold(kit.Sans)))}
			if len(qs) > 1 {
				for s, sq := range qs {
					name := sq.Header
					if name == "" {
						name = fmt.Sprintf("Question %d", s+1)
					}
					fg, fill := kit.CText3, kit.NRGBA0
					if s == i {
						fg, fill = kit.CText, kit.CHover
					}
					row = append(row, layout.Rigid(kit.HGap(6)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						d, ok := k.Button(gtx, fmt.Sprintf("askStep%s-%d", id, s), fill, kit.CHover, kit.RCtl, layout.Inset{Top: 3, Bottom: 3, Left: 8, Right: 8},
							k.Lbl(fmt.Sprintf("%d · %s", s+1, name), 12, fg, kit.Semi(kit.Sans)))
						if ok {
							a.cur = s
						}
						return d
					}))
				}
				row = append(row, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.E.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min = image.Point{}
						return k.Label(gtx, fmt.Sprintf("%d of %d", i+1, len(qs)), 12, kit.CText3, kit.Sans, 1)
					})
				}))
			} else if q.Header != "" {
				row = append(row, layout.Rigid(kit.HGap(8)), layout.Rigid(k.Pill(q.Header, kit.CAccText, kit.CAccSoft)))
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, row...)
		})
	}
	body := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 12, Left: 16, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			kids := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return k.Label(gtx, q.Question, 14, kit.CText, kit.Semi(kit.Sans), 0)
			})}
			if q.Multi {
				kids = append(kids, layout.Rigid(kit.VGap(2)), layout.Rigid(k.Lbl("Pick any", 12, kit.CText3, kit.Sans)))
			}
			for j, o := range q.Options {
				kids = append(kids, layout.Rigid(kit.VGap(6)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					on := a.picks[i][j]
					fill, rim := kit.CPane, kit.CBtnRim
					if on {
						fill, rim = kit.CSel, kit.CAcc
					}
					d, ok := k.RimButton(gtx, fmt.Sprintf("ask%s-%d-%d", id, i, j), fill, kit.Mix(fill, kit.CWhite, .04), rim, kit.RCard, layout.Inset{Top: 9, Bottom: 9, Left: 12, Right: 10}, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						text := []layout.FlexChild{layout.Rigid(k.Lbl(o.Label, 13, kit.CText, kit.Semi(kit.Sans)))}
						if o.Description != "" {
							text = append(text, layout.Rigid(kit.VGap(2)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return k.Label(gtx, o.Description, 12, kit.CText3, kit.Sans, 0)
							}))
						}
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(mark(on, q.Multi)), layout.Rigid(kit.HGap(12)),
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								return layout.Flex{Axis: layout.Vertical}.Layout(gtx, text...)
							}),
							layout.Rigid(k.KeyCap(fmt.Sprint(j+1), false)))
					})
					if ok {
						pick(a, i, j)
					}
					return d
				}))
			}
			kids = append(kids, layout.Rigid(kit.VGap(6)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				own := a.other[i].Len() > 0
				rim := kit.CBtnRim
				if gtx.Focused(&a.other[i]) {
					rim = kit.CAcc
				}
				return kit.Boxed(kit.CPane, rim, kit.RCard, layout.Inset{Top: 9, Bottom: 9, Left: 12, Right: 10}, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(mark(own, q.Multi)), layout.Rigid(kit.HGap(12)),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return k.Editor(gtx, &a.other[i], "Something else: type your answer", 13, kit.Sans, kit.CText)
						}),
						layout.Rigid(k.KeyCap(fmt.Sprint(len(q.Options)+1), false)))
				})(gtx)
			}))
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
		})
	}
	foot := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 12, Bottom: 12, Left: 16, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			row := []layout.FlexChild{
				layout.Rigid(k.Hint(fmt.Sprintf("1–%d", len(q.Options)+1), "pick")),
				layout.Rigid(kit.HGap(12)),
				layout.Flexed(1, k.LblFill("or reply below", 12, kit.CText3, kit.Sans)),
			}
			if i > 0 {
				row = append(row, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.KBtn(gtx, "askBack"+id, kit.Ghost, "Back", "")
					if ok {
						a.cur--
					}
					return d
				}), layout.Rigid(kit.HGap(6)))
			}
			label := "Next question"
			if i == len(qs)-1 {
				label = "Send answers"
			}
			row = append(row, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d, ok := k.KBtn(gtx, "askSend"+id, kit.Primary, label, "↵")
				if ok {
					*act = func() { u.next(id) }
				}
				return d
			}))
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, row...)
		})
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, layout.Rigid(head), layout.Rigid(body), layout.Rigid(foot))
}

// mark is an option's picked state: a radio circle, or a checkbox square
// when several answers are allowed.
func mark(on, multi bool) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		s := gtx.Dp(16)
		r := image.Rectangle{Max: image.Pt(s, s)}
		rad := s / 2
		if multi {
			rad = gtx.Dp(3)
		}
		if !on {
			kit.FillRRect(gtx.Ops, r, rad, kit.CText3)
			kit.FillRRect(gtx.Ops, r.Inset(gtx.Dp(1.5)), max(0, rad-gtx.Dp(1.5)), kit.CPane)
			return layout.Dimensions{Size: r.Max}
		}
		kit.FillRRect(gtx.Ops, r, rad, kit.CAcc)
		if multi {
			st := clip.Rect(r).Push(gtx.Ops)
			kit.Icon(16, kit.CWhite, tick)(gtx)
			st.Pop()
		} else {
			kit.FillRRect(gtx.Ops, r.Inset(gtx.Dp(5)), s, kit.CWhite)
		}
		return layout.Dimensions{Size: r.Max}
	}
}
