package ui

// Pictures of the drawing views on a headless GPU, against small goldens:
// what tests can't see otherwise (which option shows picked, the panel's
// glyphs, the selection's outline and grips, tooltips' sides, the list's
// cards). Skipped without a GPU, as the docs' screenshots are.

import (
	"bytes"
	"flag"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/pointer"

	"github.com/lfdominguez/desk/internal/draw"
)

// -update-drawings rewrites testdata/draw-*.png from what desk draws now;
// look at them before committing.
var updateDrawings = flag.Bool("update-drawings", false, "rewrite the drawing views' golden pictures")

// picture checks the window's r (px) against testdata/draw-<name>.png,
// at most slack pixels off.
func (h *harness) picture(win *headless.Window, name string, r image.Rectangle, slack int) {
	h.t.Helper()
	h.frame()
	if err := win.Frame(&h.ops); err != nil {
		h.t.Fatal(err)
	}
	full := image.NewRGBA(image.Rectangle{Max: win.Size()})
	if err := win.Screenshot(full); err != nil {
		h.t.Fatal(err)
	}
	got := full.SubImage(r).(*image.RGBA)
	file := filepath.Join("testdata", "draw-"+name+".png")
	if *updateDrawings {
		var b bytes.Buffer
		png.Encode(&b, got)
		os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(file, b.Bytes(), 0o644); err != nil {
			h.t.Fatal(err)
		}
	}
	b, err := os.ReadFile(file)
	if err != nil {
		h.t.Fatal(err)
	}
	want, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		h.t.Fatal(err)
	}
	if want.Bounds().Size() != r.Size() {
		h.t.Errorf("%s: %v, golden %v", name, r.Size(), want.Bounds().Size())
		return
	}
	off := 0
	for y := range r.Dy() {
		for x := range r.Dx() {
			g := got.RGBAAt(r.Min.X+x, r.Min.Y+y)
			wr, wg, wb, _ := want.At(want.Bounds().Min.X+x, want.Bounds().Min.Y+y).RGBA()
			for _, d := range []int{int(g.R) - int(wr>>8), int(g.G) - int(wg>>8), int(g.B) - int(wb>>8)} {
				if d > 8 || d < -8 {
					off++
					break
				}
			}
		}
	}
	if off > slack {
		var b bytes.Buffer
		png.Encode(&b, got)
		os.WriteFile(filepath.Join(os.TempDir(), "draw-"+name+"-got.png"), b.Bytes(), 0o644)
		h.t.Errorf("%s: %d pixels differ from %s (-update-drawings, then look)", name, off, file)
	}
}

func TestDrawingPictures(t *testing.T) {
	h := newHarness(t)
	h.size = image.Pt(1100, 760)
	win, err := headless.NewWindow(h.size.X, h.size.Y)
	if err != nil {
		t.Skipf("no headless GPU: %v", err)
	}
	defer win.Release()
	r := rect(380, 60, 160, 90, "#ffc9c9")
	r.FillStyle = "hachure"
	el := draw.New("ellipse")
	el.X, el.Y, el.Width, el.Height, el.BackgroundColor = 580, 60, 120, 90, "#a5d8ff"
	tx := draw.New("text")
	tx.X, tx.Y, tx.Text = 500, 120, "Hello" // over the rectangle's corner: its picture is see-through
	tx.FitText()
	ar := draw.New("arrow")
	ar.X, ar.Y, ar.Points, ar.Roundness = 380, 260, [][2]float64{{0, 0}, {200, 60}}, nil
	head := "arrow"
	ar.EndArrowhead = &head
	for i, e := range []*draw.Element{r, el, tx, ar} {
		e.Seed = int64(1000 + i*7919) // the same rough strokes every run
	}
	st, o := h.drawing("D", r, el, tx, ar)
	st.ed.Scene.AppState["theme"] = "dark"
	st.ed.Gen++
	canvas := image.Rect(int(o.X), int(o.Y), h.size.X, h.size.Y)
	park := func() { // the pointer off every button
		h.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(float32(h.size.X-200), float32(h.size.Y-100))})
	}
	for _, c := range []struct {
		name string
		sel  []string
	}{{"rect", []string{r.ID}}, {"arrow", []string{ar.ID}}, {"text", []string{tx.ID}}, {"several", []string{r.ID, ar.ID}}} {
		st.ed.Select(c.sel...)
		park()
		h.picture(win, c.name, canvas, 0)
	}
	st.ed.Select()
	a := o.Add(f32.Pt(700, 300))
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: a},
		pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: a.Add(f32.Pt(120, 80))})
	h.picture(win, "box", image.Rectangle{Min: image.Pt(int(a.X)-10, int(a.Y)-10), Max: image.Pt(int(a.X)+140, int(a.Y)+100)}, 0)
	h.send(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: a.Add(f32.Pt(120, 80))})

	st.ed.Select(r.ID)
	for _, id := range []string{"drawFit", "drawTheme", "drawTool" + draw.ToolRectangle, "drawToBack"} {
		b := h.areaOf(id) // leaves the pointer on it
		h.frame()
		h.picture(win, "tip-"+id, b.Inset(-90).Intersect(image.Rectangle{Max: h.size}), 120) // a tooltip's edge pixels move under load (seen: 57)
	}

	empty := draw.NewScene()
	empty.Save(filepath.Join(h.u.drawingsDir("Acme"), "Empty.excalidraw"))
	wide := draw.NewScene()
	wide.Elements = []*draw.Element{rect(0, 0, 600, 60, "#b2f2bb")}
	wide.Elements[0].Seed = 7
	wide.Save(filepath.Join(h.u.drawingsDir("Acme"), "Wide.excalidraw"))
	day := time.Date(2026, 1, 2, 10, 0, 0, 0, time.Local)
	for _, n := range []string{"D", "Empty", "Wide"} {
		os.Chtimes(filepath.Join(h.u.drawingsDir("Acme"), n+".excalidraw"), day, day)
		day = day.Add(time.Hour)
	}
	h.u.drawRead()
	h.u.view = "dr:Acme"
	park()
	list := image.Rect(int(o.X), int(o.Y)-40, h.size.X, int(o.Y)+260)
	h.picture(win, "list", list, 0)
	bin := h.areaOf("drDel" + filepath.Join(h.u.drawingsDir("Acme"), "Wide.excalidraw"))
	h.frame()
	h.picture(win, "list-tip", bin.Inset(-90).Intersect(image.Rectangle{Max: h.size}), 120)
}
