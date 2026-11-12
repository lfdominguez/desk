package kit

import (
	"testing"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/op"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/lfdominguez/desk/internal/keys"
)

// TestNewline: in a message box Alt+Enter puts a line in, Shift+Enter does
// nothing (no line, no send), Enter still sends; a rebinding moves it.
func TestNewline(t *testing.T) {
	t.Cleanup(func() { keys.Load(nil) })
	var r input.Router
	var ops op.Ops
	ed := &widget.Editor{Submit: true}
	th := material.NewTheme()
	sent := 0
	frame := func() {
		gtx := routed(&ops, &r, 300, 100)
		Newline(gtx, ed)
		for {
			ev, ok := ed.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				sent++
			}
		}
		material.Editor(th, ed, "").Layout(gtx)
		gtx.Execute(key.FocusCmd{Tag: ed})
		r.Frame(&ops)
	}
	press := func(n key.Name, m key.Modifiers) {
		r.Queue(key.Event{Name: n, Modifiers: m, State: key.Press}, key.Event{Name: n, Modifiers: m, State: key.Release})
		frame()
	}
	frame()
	frame()
	ed.SetText("a")
	ed.SetCaret(1, 1)
	press(key.NameReturn, key.ModAlt)
	if ed.Text() != "a\n" || sent != 0 {
		t.Fatalf("Alt+Enter: %q, sent %d", ed.Text(), sent)
	}
	press(key.NameReturn, key.ModShift)
	if ed.Text() != "a\n" || sent != 0 {
		t.Fatalf("Shift+Enter: %q, sent %d", ed.Text(), sent)
	}
	press(key.NameReturn, 0)
	if sent != 1 {
		t.Fatalf("Enter didn't send: %d", sent)
	}
	keys.Set("text.newline", keys.Chord{Name: "J", Mods: key.ModCtrl})
	frame()
	press(key.NameReturn, key.ModAlt)
	press("J", key.ModCtrl)
	if ed.Text() != "a\n\n" {
		t.Errorf("rebound to Ctrl+J: %q", ed.Text())
	}
	ed.ReadOnly = true
	press("J", key.ModCtrl)
	if ed.Text() != "a\n\n" {
		t.Errorf("a read-only box took a line: %q", ed.Text())
	}
}
