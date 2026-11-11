package ui

import (
	"image/color"
	"io"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// selText is text s that the mouse can select (drag, double-click a word)
// and Ctrl+C copy, kept by id across frames. Under a message box (u.selBack)
// the keyboard stays in the box: a press hands it straight back, and a
// selection is copied as the button comes up, as the rich text does.
func (u *ui) selText(gtx layout.Context, id, s string, sp unit.Sp, c color.NRGBA, f font.Font, maxLines int) layout.Dimensions {
	l := u.sels[id]
	if l == nil {
		l = &widget.Selectable{LineHeightScale: chatLead, PaintSelectionWhenUnfocused: true}
		u.sels[id] = l // pruned by sweepSels once out of sight a while
	}
	u.selSeen[id] = gtx.Now
	if l.Text() != s {
		l.SetText(s)
	}
	l.MaxLines = maxLines
	m := op.Record(gtx.Ops)
	d := l.Layout(gtx, u.k.Sh, f, sp, kit.Mat(gtx, c), kit.Mat(gtx, alpha(kit.CAcc, 0x70)))
	text := m.Stop()
	tag := "selcopy:" + id // an area around the text's own: its presses reach both
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: tag, Kinds: pointer.Press | pointer.Release})
		if !ok {
			break
		}
		switch e := ev.(pointer.Event); e.Kind {
		case pointer.Press: // one selection at a time, this text's or a reply's
			if u.selLast != nil && u.selLast != l {
				u.selLast.ClearSelection()
			}
			if u.chatSel != nil {
				u.chatSel.sel = [2]int{}
			}
			u.selLast = l
		case pointer.Release:
			if s := l.SelectedText(); s != "" && u.selBack != nil {
				gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(s))})
				u.showToast("Copied", false)
			}
		}
	}
	if u.selBack != nil && gtx.Focused(l) {
		gtx.Execute(key.FocusCmd{Tag: u.selBack})
	}
	a := clip.Rect{Max: d.Size}.Push(gtx.Ops)
	event.Op(gtx.Ops, tag)
	text.Add(gtx.Ops)
	a.Pop()
	if l.Focused() {
		u.selFocus = l // for typing: one to ask, not every one ever shown
	}
	return d
}

// selKeep is how long chat text's selection state outlives its last
// drawing: a long session's every block would otherwise stay in memory.
const selKeep = 10 * time.Minute

// sweepSels drops the selectable text not drawn for selKeep, at most once
// a minute.
func (u *ui) sweepSels(now time.Time) {
	if now.Sub(u.selSwept) < time.Minute {
		return
	}
	u.selSwept = now
	for id, at := range u.selSeen {
		if now.Sub(at) > selKeep {
			delete(u.selSeen, id)
			delete(u.sels, id)
			if st := u.rsels[id]; st != nil && st != u.chatSel {
				delete(u.rsels, id)
			}
		}
	}
}

// claudeMsg is a reply of Claude's: its markdown, selectable as it reads.
func (u *ui) claudeMsg(gtx layout.Context, id string, m kanban.ChatMsg) layout.Dimensions {
	return u.markdown(gtx, id, m.Text)
}

// msgTools is a Claude reply's Copy (its whole Markdown): a quiet text
// button at the end of its name line, always there, so nothing pops up over
// the text while reading. Parts are selected in the text itself.
func (u *ui) msgTools(gtx layout.Context, id string, m kanban.ChatMsg) layout.Dimensions {
	k := u.k
	bd, ok := k.Button(gtx, "msgCopy"+id, color.NRGBA{}, kit.CHover, kit.RCtl, layout.Inset{Top: 1, Bottom: 1, Left: 6, Right: 6}, k.Lbl("Copy", 12, kit.CText3, kit.Sans))
	if ok {
		gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(m.Text))})
		u.showToast("Copied Claude's reply", false)
	}
	return bd
}
