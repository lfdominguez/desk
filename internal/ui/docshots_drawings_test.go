package ui

// The Drawings screenshots: an organization's drawings, one open in the
// editor, a shape drawn and selected with its style panel, the drawing's
// Claude with its live edit and a suggestion, the library, Export.

import (
	"encoding/json"
	"path/filepath"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"

	"github.com/lfdominguez/desk/internal/draw"
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
	st := s.u.draws[path]
	st.ed.Select()
	st.ed.Undo() // the arrow and the box: the login flow as it was
	st.ed.Undo()

	// its Claude: asked for a cache, drew it (marked as its own), and
	// suggested an audit log besides
	st.claude.hide = false
	sessions := ""
	for _, e := range st.ed.Scene.Elements {
		if e.Type == "ellipse" {
			sessions = e.ID
		}
	}
	w, err := s.u.drawWhoOf("draw:" + path)
	if err != nil {
		s.t.Fatal(err)
	}
	chat := drawChatKey(path)
	s.u.say(chat, "you", "Put a cache in front of Sessions")
	if _, err := s.u.drawEditFor(w, "", draw.Patch{Add: []json.RawMessage{
		json.RawMessage(`{"type":"rectangle","id":"cache","x":640,"y":300,"width":180,"height":90,"backgroundColor":"#d0bfff","fillStyle":"solid","roughness":1,"seed":11,"label":"Cache"}`),
		json.RawMessage(`{"type":"arrow","seed":12,"from":"` + sessions + `","to":"cache"}`)}}); err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.u.drawSuggest(w, "", "An audit log for the API: who logged in, and when.", draw.Patch{Add: []json.RawMessage{
		json.RawMessage(`{"type":"rectangle","id":"audit","x":40,"y":440,"width":180,"height":90,"backgroundColor":"#ffd8a8","fillStyle":"solid","seed":13,"label":"Audit log"}`)}}); err != nil {
		s.t.Fatal(err)
	}
	s.u.say(chat, "claude", "Added **Cache** under Sessions, wired from it: Ctrl+Z takes it back.\n\nI also suggested an audit log; approve it if you want it.")
	s.settle()
	s.move(f32.Pt(5, 5))
	s.shot("drawings-claude")

	// the library, one item in it; then Export's choices
	st.ed.Select("cache")
	s.click("drawLib")
	s.click("libAdd")
	st.ed.Select()
	s.move(f32.Pt(5, 5))
	s.shot("drawings-library")
	s.click("drawExport")
	s.move(f32.Pt(5, 5))
	s.shot("drawings-export")
	s.key(key.NameEscape, 0)
}
