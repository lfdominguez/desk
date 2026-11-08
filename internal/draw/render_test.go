package draw

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func scene(es ...*Element) *Scene {
	s := NewScene()
	s.Elements = es
	return s
}

func rgba(img *image.RGBA, x, y int) color.RGBA { return img.RGBAAt(x, y) }

func dark(c color.RGBA) bool { return c.A > 200 && int(c.R)+int(c.G)+int(c.B) < 300 }

func TestRenderFillsAndFits(t *testing.T) {
	r := el("rectangle", 100, 60)
	r.Roughness, r.BackgroundColor, r.Roundness = 0, "#ff0000", nil
	s := scene(r)
	img := Render(s, Options{Padding: 10})
	if b := img.Bounds(); b.Dx() != 122 || b.Dy() != 82 {
		t.Errorf("size %v: the box, its stroke and the padding", b)
	}
	if c := rgba(img, 61, 41); c != (color.RGBA{0xff, 0, 0, 0xff}) {
		t.Errorf("inside %v", c)
	}
	if c := rgba(img, 3, 3); c != (color.RGBA{0xff, 0xff, 0xff, 0xff}) {
		t.Errorf("background %v", c)
	}
	if c := rgba(img, 11, 41); !dark(c) {
		t.Errorf("the left edge %v", c)
	}
	if c := rgba(Render(s, Options{Padding: 10, Transparent: true}), 3, 3); c.A != 0 {
		t.Errorf("transparent background %v", c)
	}
	s.AppState["viewBackgroundColor"] = "#000000"
	if c := rgba(Render(s, Options{Padding: 10}), 3, 3); c != (color.RGBA{0, 0, 0, 0xff}) {
		t.Errorf("the scene's background %v", c)
	}
	s.AppState["viewBackgroundColor"] = "nonsense"
	if c := rgba(Render(s, Options{Padding: 10}), 3, 3); c.R != 0xff {
		t.Errorf("a bad background isn't white: %v", c)
	}
	if b := Render(s, Options{Padding: 10, Scale: 2}).Bounds(); b.Dx() != 244 {
		t.Errorf("scale 2: %v", b)
	}
	if b := Render(s, Options{Padding: 10, Scale: 2, MaxSide: 61}).Bounds(); b.Dx() != 61 || b.Dy() != 41 {
		t.Errorf("max side: %v", b)
	}
	r.IsDeleted = true
	if b := Render(s, Options{}).Bounds(); b.Dx() != 1 || b.Dy() != 1 {
		t.Errorf("nothing live: %v", b)
	}
	if b := Render(NewScene(), Options{Padding: 5}).Bounds(); b.Dx() != 10 {
		t.Errorf("empty with padding: %v", b)
	}
	png1, err := PNG(scene(el("ellipse", 50, 50)), Options{})
	if _, err2 := png.Decode(bytes.NewReader(png1)); err != nil || err2 != nil {
		t.Errorf("PNG: %v %v", err, err2)
	}
}

func TestRenderIsStable(t *testing.T) {
	s, _ := Load("testdata/archimate.excalidraw")
	a, b := Render(s, Options{MaxSide: 600}), Render(s, Options{MaxSide: 600})
	if !bytes.Equal(a.Pix, b.Pix) {
		t.Error("two renders differ")
	}
	inked := 0
	for i := 0; i < len(a.Pix); i += 4 {
		if a.Pix[i] < 200 || a.Pix[i+2] < 200 {
			inked++
		}
	}
	if inked < len(a.Pix)/4/50 {
		t.Errorf("only %d inked pixels", inked)
	}
}

func TestPaintSkips(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	paint(img, Set{Kind: Fill, Ops: []Op{mv(pt(0, 0)), ln(pt(9, 0)), ln(pt(9, 9))}}, Identity(), 1)
	paint(img, Set{Kind: Fill, Ops: []Op{mv(pt(50, 50)), ln(pt(90, 50)), ln(pt(90, 90))}, Color: ink}, Identity(), 1)
	paint(img, Set{Kind: Fill, Color: ink}, Identity(), 1)
	for _, p := range img.Pix {
		if p != 0 {
			t.Fatal("painted a transparent, an off-canvas or an empty set")
		}
	}
	paint(img, Set{Kind: Stroke, Ops: []Op{mv(pt(0, 5.5)), ln(pt(10, 5.5))}, Color: ink, Width: 1, Dash: []float64{2, 4}}, Identity(), 1)
	if !dark(img.RGBAAt(0, 5)) || img.RGBAAt(3, 5).A != 0 || img.RGBAAt(4, 5).A != 0 || !dark(img.RGBAAt(7, 5)) {
		t.Error("a dashed stroke has no gaps")
	}
}

func TestRenderText(t *testing.T) {
	tx := el("text", 120, 25)
	tx.Text = "Hello 😀"
	img := Render(scene(tx), Options{})
	inked := 0
	for y := range 25 {
		for x := range 120 {
			if dark(img.RGBAAt(x, y)) {
				inked++
			}
		}
	}
	if inked < 30 {
		t.Errorf("text drew %d pixels", inked)
	}
	tx.Angle = 0.5
	turned := Render(scene(tx), Options{})
	if bytes.Equal(turned.Pix, img.Pix) {
		t.Error("turned text looks the same")
	}
	for _, mod := range []func(){
		func() { tx.Angle, tx.Text = 0, "" },
		func() { tx.Text, tx.FontSize = "x", 0 },
		func() { tx.FontSize, tx.StrokeColor = 20, "transparent" },
	} {
		mod()
		for _, p := range Render(scene(tx), Options{Transparent: true}).Pix {
			if p != 0 {
				t.Fatalf("empty, sizeless or invisible text drew: %+v", tx)
			}
		}
	}
}

func TestTextLines(t *testing.T) {
	tx := el("text", 200, 50)
	tx.Text, tx.FontSize, tx.LineHeight = "ab\nabcd", 20, 0
	ls := Lines(tx)
	if len(ls) != 2 || ls[0].X != 0 || ls[1].Baseline-ls[0].Baseline != 25 {
		t.Fatalf("lines %+v", ls)
	}
	if ls[0].Baseline < 10 || ls[0].Baseline > 25 {
		t.Errorf("first baseline %v", ls[0].Baseline)
	}
	tx.TextAlign = "center"
	c := Lines(tx)
	tx.TextAlign = "right"
	r := Lines(tx)
	if !(c[0].X > c[1].X && r[0].X > c[0].X && near(r[0].X-c[0].X, c[0].X)) {
		t.Errorf("center %+v, right %+v", c, r)
	}
	for fam, want := range map[int]string{FontVirgil: "Excalifont-Regular", FontExcalifont: "Excalifont-Regular", 0: "Excalifont-Regular",
		FontHelvetica: "Manrope-Regular", FontNunito: "Manrope-Regular", FontLilita: "Manrope-Bold",
		FontCascadia: "GeistMono-Regular", FontComic: "GeistMono-Regular"} {
		if got := FontFile(fam); got != want {
			t.Errorf("family %d: %s", fam, got)
		}
	}
	if f := newFaces(FontNunito, 20); len(f.faces) != 3 || f.pick('a') != f.faces[0] || f.pick('\U0010FFFD') != f.faces[0] {
		t.Errorf("fallback faces: %d", len(f.faces))
	}
	if f := newFaces(FontExcalifont, 20); len(f.faces) != 3 || f.pick('\u2192') == f.faces[0] {
		t.Error("Excalifont has no arrow: another face draws it")
	}
}

func pngURL(t *testing.T, img image.Image) string {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
}

func TestRenderImage(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	red, blue := color.RGBA{0xff, 0, 0, 0xff}, color.RGBA{0, 0, 0xff, 0xff}
	src.SetRGBA(0, 0, red)
	src.SetRGBA(1, 0, blue)
	src.SetRGBA(0, 1, red)
	src.SetRGBA(1, 1, red)
	im := el("image", 100, 100)
	id := "f1"
	im.FileID = &id
	s := scene(im)
	s.Files[id] = &File{ID: id, MimeType: "image/png", DataURL: pngURL(t, src)}
	at := func(x, y int) color.RGBA { return rgba(Render(s, Options{}), x, y) }
	if at(10, 10) != red || at(90, 10) != blue || at(90, 90) != red {
		t.Errorf("image: %v %v %v", at(10, 10), at(90, 10), at(90, 90))
	}
	im.Scale = &[2]float64{-1, 1}
	if at(10, 10) != blue {
		t.Errorf("mirrored: %v", at(10, 10))
	}
	im.Scale = &[2]float64{1, -1}
	if at(90, 90) != blue {
		t.Errorf("flipped: %v", at(90, 90))
	}
	im.Scale, im.Opacity = nil, 50
	if c := at(90, 10); c.B == 0xff && c.R == 0 || c.R < 100 {
		t.Errorf("half opaque over white: %v", c)
	}
	im.Opacity = 100
	for _, url := range []string{"data:image/png,raw", "data:image/png;base64,!!!", "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("no png"))} {
		s.Files[id].DataURL = url
		img, edge := Render(s, Options{}), 0
		for y := range 100 {
			if c := img.RGBAAt(1, y); c.R < 0xf0 && c.R == c.B {
				edge++
			}
		}
		if edge < 20 {
			t.Errorf("%.30s: no placeholder edge (%d)", url, edge)
		}
	}
	delete(s.Files, id)
	if fileImage(s, im) != nil {
		t.Error("a missing file decoded")
	}
	im.FileID = nil
	if fileImage(s, im) != nil {
		t.Error("no file decoded")
	}
}

func TestRenderFrameName(t *testing.T) {
	fr := el("frame", 200, 100)
	name := "Plan"
	fr.Name = &name
	img := Render(scene(fr), Options{})
	labelled := func(img *image.RGBA) int {
		n := 0
		for y := range 16 {
			for x := range 60 {
				if c := img.RGBAAt(x, y); c.R < 0xe0 {
					n++
				}
			}
		}
		return n
	}
	if labelled(img) < 10 {
		t.Error("no name over the frame")
	}
	if x0, y0, _, _ := scene(fr).Bounds(); x0 != -1 || y0 != -21 {
		t.Errorf("a frame's bounds hold its name: %v %v", x0, y0)
	}
	fr.Name = nil
	if n := labelled(Render(scene(fr), Options{})); n < 10 {
		t.Error("an unnamed frame isn't called Frame")
	}
}
