package ui

import (
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/pointer"

	"github.com/lfdominguez/desk/internal/kanban"
)

// TestSessionKeepsFocus: in a card's session the keyboard stays in the
// message box: a click on plain chat text (an error here) hands it straight
// back, a drag over that text copies it as the button comes up, and a press
// anywhere in the pane (a reply) brings the keyboard back after it left.
func TestSessionKeepsFocus(t *testing.T) {
	h := newHarness(t)
	id := h.u.b.Add(0, kanban.Card{Title: "a"}).ID
	h.u.chats[id] = []kanban.ChatMsg{{Who: "error", Text: strings.Repeat("something went wrong here ", 300), At: time.Now()}}
	h.u.openSession(id)
	h.frame()
	h.frame()
	src := h.r.Source()
	if !src.Focused(&h.u.chatIn) {
		t.Fatal("the message box has the keyboard on opening")
	}
	var at f32.Point // where the error's text is: the first press it takes
	for y := float32(200); y < 600 && h.u.selLast == nil; y += 20 {
		for _, x := range []float32{500, 700, 900} {
			if h.clickAt(f32.Pt(x, y)); h.u.selLast != nil {
				at = f32.Pt(x, y)
				break
			}
		}
	}
	if h.u.selLast == nil {
		t.Fatal("no press reached the error's text")
	}
	if !h.r.Source().Focused(&h.u.chatIn) {
		t.Error("a click on chat text keeps the keyboard in the message box")
	}
	h.drag(at, at.Add(f32.Pt(200, 0)), pointer.ButtonPrimary, 0)
	if _, b, ok := h.r.WriteClipboard(); !ok || len(b) == 0 || !strings.Contains(h.u.selLast.Text(), string(b)) || h.u.toast != "Copied" {
		t.Errorf("a drag copies what it selected: %q %v, toast %q", b, ok, h.u.toast)
	}
	if !h.r.Source().Focused(&h.u.chatIn) {
		t.Error("a drag over chat text keeps the keyboard in the message box")
	}
	h.u.chats[id] = []kanban.ChatMsg{{Who: "claude", Text: strings.Repeat("all good now ", 400), At: time.Now()}} // a reply never takes the keyboard
	h.u.blur = true                                                                                               // the keyboard left (Esc)
	h.frame()
	if h.r.Source().Focused(&h.u.chatIn) {
		t.Fatal("blurred")
	}
	h.clickAt(at) // on the reply
	if !h.r.Source().Focused(&h.u.chatIn) {
		t.Error("a press in the pane brings the keyboard back to the message box")
	}
	first := h.u.selLast
	h.drag(at, at.Add(f32.Pt(200, 0)), pointer.ButtonPrimary, 0) // a selection in the reply: the error's goes
	if h.u.chatSel == nil || h.u.chatSel.selected() == "" || first.SelectionLen() != 0 {
		t.Fatalf("one selection at a time: the reply's %v, the error's %d", h.u.chatSel != nil, first.SelectionLen())
	}
	h.u.chats[id] = []kanban.ChatMsg{{Who: "claude", Text: "ok", At: time.Now()}, {Who: "error", Text: strings.Repeat("failed again ", 3000), At: time.Now()}}
	h.frame()
	h.drag(at, at.Add(f32.Pt(200, 0)), pointer.ButtonPrimary, 0) // a selection in another text: the reply's goes
	if h.u.selLast == first || h.u.selLast.SelectionLen() == 0 || h.u.chatSel.selected() != "" {
		t.Errorf("one selection at a time: the new text's %d, the reply's %q", h.u.selLast.SelectionLen(), h.u.chatSel.selected())
	}
}
