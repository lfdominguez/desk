package ui

import (
	"fmt"
	"html"
	"image"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"

	"github.com/lfdominguez/desk/internal/draw"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// Getting drawings in and out: Export writes the drawing as a PNG, an SVG
// or its .excalidraw where the user picks (the system's save dialog);
// Import, in a list of drawings, takes an .excalidraw (a new drawing) or
// an .excalidrawlib (its items into the organization's library); Share
// asks the drawing's Claude to publish it as a claude.ai artifact (desk has
// no claude.ai API), and the link it publishes is kept on the drawing.

// pickFile asks the user for a file with one of exts; nil, nil: they
// cancelled. saveFile asks where to write one named name; nil, nil the
// same. The window sets both (a file chooser needs it); tests replace them.
var (
	pickFile = func(exts ...string) (io.ReadCloser, error) { return nil, fmt.Errorf("no file chooser here") }
	saveFile = func(name string) (io.WriteCloser, error) { return nil, fmt.Errorf("no file chooser here") }
)

// drawExports are Export's choices: the file's extension and what it says.
var drawExports = [][2]string{{"png", "PNG image"}, {"svg", "SVG image"}, {"excalidraw", "Excalidraw file"}}

// drawExportBytes is st's drawing as kind: as it looks (its theme), the
// pictures at twice the scale for sharp screens.
func drawExportBytes(st *drawState, kind string) ([]byte, error) {
	s := themed(st.ed.Scene, nil)
	switch kind {
	case "png":
		return draw.PNG(s, draw.Options{Scale: 2, Padding: 20})
	case "svg":
		return draw.SVG(s, draw.Options{Padding: 20}), nil
	}
	return st.ed.Scene.Marshal()
}

// drawExport writes st's drawing as kind where the user picks, off the UI
// goroutine.
func (u *ui) drawExport(st *drawState, kind string) {
	st.exportOpen = false
	b, err := drawExportBytes(st, kind)
	name := drawName(st.path) + "." + kind
	go func() {
		var w io.WriteCloser
		if err == nil {
			w, err = saveFile(name)
		}
		if w != nil {
			_, err = w.Write(b)
			if e := w.Close(); err == nil {
				err = e
			}
		}
		u.later(func() {
			switch {
			case err != nil:
				u.showToast("Couldn't export the drawing: "+err.Error(), true)
			case w != nil:
				u.showToast("Exported "+name, false)
			}
		})
	}()
}

// drawImport asks for an Excalidraw file and puts it in p, off the UI
// goroutine.
func (u *ui) drawImport(p drawPlace) {
	go func() {
		r, err := pickFile(".excalidraw", ".excalidrawlib", ".json")
		var b []byte
		name := "Imported"
		if err == nil && r != nil {
			if f, ok := r.(interface{ Name() string }); ok { // an *os.File where the chooser gives one
				name = strings.TrimSuffix(filepath.Base(f.Name()), filepath.Ext(f.Name()))
			}
			b, err = io.ReadAll(io.LimitReader(r, 100<<20))
			r.Close()
		}
		u.later(func() {
			switch {
			case err != nil:
				u.showToast("Couldn't open the file: "+err.Error(), true)
			case b != nil:
				u.drawImported(p, name, b)
			}
		})
	}()
}

// drawImported puts file b, named name, in p: a library's items into the
// organization's library, a drawing as a new one, opened.
func (u *ui) drawImported(p drawPlace, name string, b []byte) {
	if in, err := draw.ParseLibrary(b); err == nil {
		l := u.library(p.org)
		if l.err != nil {
			u.showToast("Couldn't read the library: "+l.err.Error(), true)
			return
		}
		l.lib.Items = append(l.lib.Items, in.Items...)
		u.libSave(p.org, l)
		u.showToast(fmt.Sprintf("Added %d to the library", len(in.Items)), false)
		return
	}
	s, err := draw.Parse(b)
	if err != nil {
		u.showToast("Couldn't import "+name+": "+err.Error(), true)
		return
	}
	dir := u.placeDir(p)
	path := filepath.Join(dir, drawingName(dir, name)+".excalidraw")
	if err := s.Save(path); err != nil {
		u.showToast("Couldn't import "+name+": "+err.Error(), true)
		return
	}
	u.drawRead()
	u.openDrawing(p.org, path)
}

// drawSharePage is st's drawing as a page of its own: the SVG on its
// background, what the drawing's Claude publishes.
func drawSharePage(st *drawState) []byte {
	s := themed(st.ed.Scene, nil)
	bg := s.Background()
	return fmt.Appendf(nil, `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s</title>
<style>
:root { --bg: #%02x%02x%02x; }
body { margin: 0; min-height: 100vh; background: var(--bg); display: flex; align-items: center; justify-content: center; padding: 16px; box-sizing: border-box; }
svg { max-width: 100%%; height: auto; }
</style>
</head>
<body>
%s
</body>
</html>
`, html.EscapeString(drawName(st.path)), bg.R, bg.G, bg.B, draw.SVG(s, draw.Options{Padding: 20}))
}

// drawShare writes st's page beside it and asks its Claude to publish it
// (again in place, once shared); drawEvent keeps the link it publishes.
func (u *ui) drawShare(st *drawState) {
	page := drawSide(st.path, "share.html")
	if err := os.WriteFile(page, drawSharePage(st), 0o644); err != nil {
		u.showToast("Couldn't share the drawing: "+err.Error(), true)
		return
	}
	prompt := fmt.Sprintf("Publish the file %s as a claude.ai artifact titled %q: the drawing as a page of its own. "+
		"Read it, then publish it as it is, without changing it.", page, drawName(st.path))
	if st.side.Shared != "" {
		prompt += " It was shared before: update that artifact, " + st.side.Shared + ", in place."
	}
	prompt += " Reply with its link."
	if err := u.drawAsk(st, prompt); err != nil {
		u.showToast("Drawing's Claude: "+err.Error(), true)
		return
	}
	st.claude.hide = false
	u.say(drawChatKey(st.path), "you", "Share this drawing as a claude.ai artifact.")
}

// drawExportMenu is Export's choices, over the canvas at the top right.
func (u *ui) drawExportMenu(gtx layout.Context, st *drawState, size image.Point, act *func()) {
	k := u.k
	w, pad := gtx.Dp(200), gtx.Dp(12)
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, size.Y)}
	d := kit.Boxed(kit.CPanel, kit.CRim, kit.RCard, layout.UniformInset(6), func(gtx layout.Context) layout.Dimensions {
		var rows []layout.FlexChild
		for _, x := range drawExports {
			rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				d, ok := k.Button(gtx, "drawExport"+x[0], kit.CPanel, kit.CHover, kit.RCtl, layout.Inset{Top: 7, Bottom: 7, Left: 10, Right: 10}, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{}.Layout(gtx,
						layout.Flexed(1, k.LblFill(x[1], 12, kit.CText, kit.Sans)),
						layout.Rigid(k.Lbl("."+x[0], 12, kit.CText3, kit.Mono)))
				})
				if ok {
					*act = func() { u.drawExport(st, x[0]) }
				}
				return d
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
	})(g)
	call := m.Stop()
	defer op.Offset(image.Pt(size.X-w-pad, pad+gtx.Dp(50))).Push(gtx.Ops).Pop()
	block := clip.Rect{Max: d.Size}.Push(gtx.Ops) // a press on the menu isn't the canvas's
	event.Op(gtx.Ops, &st.exportOpen)
	block.Pop()
	call.Add(gtx.Ops)
}
