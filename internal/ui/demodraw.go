package ui

import (
	"path/filepath"

	"github.com/lfdominguez/desk/internal/draw"
	"github.com/lfdominguez/desk/internal/orgs"
)

// demoDrawings are Acme's drawings in the demo world: a login flow drawn
// through the editor itself (so its arrows bind as a user's would), and a
// small whiteboard.
func demoDrawings(work string) error {
	dir := filepath.Join(work, "Acme", orgs.DeskDir, "drawings")
	s := draw.NewScene()
	s.AppState["theme"] = "dark"
	ed := draw.NewEditor(s)
	shape := func(tool string, x, y, w, h float64, fill string) {
		ed.Style.BackgroundColor = fill
		ed.SetTool(tool)
		ed.Down(draw.Pt{X: x, Y: y}, 4, false)
		ed.Drag(draw.Pt{X: x + w, Y: y + h}, 4, false)
		ed.Up()
	}
	label := func(x, y float64, text string, size float64) {
		ed.Style.FontSize = size
		ed.SetTool(draw.ToolText)
		ed.Down(draw.Pt{X: x, Y: y}, 4, false)
		ed.Up()
		ed.SetText(text)
		ed.EndText()
	}
	arrow := func(x0, y0, x1, y1 float64) {
		ed.SetTool(draw.ToolArrow)
		ed.Down(draw.Pt{X: x0, Y: y0}, 4, false)
		ed.Drag(draw.Pt{X: x1, Y: y1}, 4, false)
		ed.Up()
	}
	label(40, 20, "Login flow", 36)
	shape(draw.ToolRectangle, 40, 120, 180, 90, "#a5d8ff")
	label(78, 165, "Browser", 24)
	shape(draw.ToolRectangle, 340, 120, 180, 90, "#b2f2bb")
	label(395, 165, "API", 24)
	shape(draw.ToolDiamond, 340, 300, 180, 130, "#ffec99")
	label(392, 365, "Token?", 20)
	shape(draw.ToolEllipse, 640, 120, 180, 90, "#ffc9c9")
	label(680, 165, "Sessions", 24)
	arrow(220, 165, 340, 165)
	arrow(520, 165, 640, 165)
	arrow(430, 210, 430, 300)
	ed.Style.StrokeColor = "#e03131"
	arrow(340, 365, 130, 210)
	label(150, 300, "expired: refresh", 20)
	ed.Style.StrokeColor = "#1e1e1e"
	for i, e := range s.Elements { // the same picture every run
		e.Seed = int64(1000 + i*7919)
	}
	if err := s.Save(filepath.Join(dir, "Login flow.excalidraw")); err != nil {
		return err
	}

	w := draw.NewScene()
	w.AppState["theme"] = "dark"
	ed = draw.NewEditor(w)
	ed.Style.FillStyle, ed.Style.Roughness = "hachure", 2
	shape(draw.ToolRectangle, 0, 0, 260, 160, "#ffec99")
	label(24, 40, "Ideas:\n- retry budget\n- status page", 22)
	shape(draw.ToolEllipse, 320, 20, 140, 120, "#d0bfff")
	for i, e := range w.Elements {
		e.Seed = int64(500 + i*104729)
	}
	return w.Save(filepath.Join(dir, "Whiteboard.excalidraw"))
}
