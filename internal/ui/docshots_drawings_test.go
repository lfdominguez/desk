package ui

// The Drawings screenshots: an organization's drawings, one open in the
// editor, and a shape drawn and selected with its style panel.

import (
	"path/filepath"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
)

func init() {
	scenes = append(scenes, scene{"drawings", drawingsScene})
}

func drawingsScene(s *shooter) {
	s.u.view = "dr:Acme"
	s.settle()
	s.move(f32.Pt(5, 5))
	s.shot("drawings-list")
	path := filepath.Join(s.u.drawingsDir("Acme"), "Login flow.excalidraw")
	s.u.openDrawing("Acme", path) // not s.click: its search hovers the card's bin first, which a press there deletes
	s.settle()
	s.move(f32.Pt(5, 5))
	s.shot("drawings-editor")

	s.key("R", 0)
	drag := func(a, b f32.Point) {
		s.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: a},
			pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: a},
			pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: a.Add(b.Sub(a).Mul(.5))},
			pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: b},
			pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: b})
	}
	px := func(x, y float32) f32.Point { return f32.Pt(x*shotScale, y*shotScale) }
	drag(px(1180, 640), px(1420, 760))
	s.key("A", 0)
	drag(px(1170, 520), px(1290, 640))
	s.shot("drawings-draw")
	s.key(key.NameEscape, 0)
}
