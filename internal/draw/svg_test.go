package draw

import (
	"encoding/xml"
	"image"
	"image/color"
	"strings"
	"testing"
)

func svgOf(t *testing.T, s *Scene, o Options) string {
	t.Helper()
	out := string(SVG(s, o))
	d := xml.NewDecoder(strings.NewReader(out))
	for {
		if _, err := d.Token(); err != nil {
			if err.Error() != "EOF" {
				t.Fatalf("not XML: %v\n%.300s", err, out)
			}
			return out
		}
	}
}

func TestSVGShapes(t *testing.T) {
	r := el("rectangle", 100, 60)
	r.Roughness, r.BackgroundColor, r.StrokeStyle, r.Opacity = 0, "#a5d8ff", "dashed", 50
	r.X, r.Y = 10, 20
	out := svgOf(t, scene(r), Options{Padding: 5, Scale: 2})
	for _, want := range []string{
		`viewBox="0 0 112 72" width="224" height="144"`,
		`<rect width="100%" height="100%" fill="#ffffff"/>`,
		`<g transform="matrix(1 0 0 1 6 6)">`,
		`fill="#a5d8ff" fill-opacity="0.5"`,
		`stroke="#1e1e1e" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" stroke-opacity="0.5" stroke-dasharray="8 10"`,
		`d="M15 0C`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %s in\n%.600s", want, out)
		}
	}
	if strings.Contains(out, "@font-face") {
		t.Error("fonts embedded without text")
	}
	if !strings.HasSuffix(out, "</svg>") {
		t.Error("unclosed")
	}
	s := scene(r)
	s.AppState["viewBackgroundColor"] = "#000"
	if out := svgOf(t, s, Options{}); !strings.Contains(out, `fill="#000000"/>`) {
		t.Error("the scene's background")
	}
	s.AppState["viewBackgroundColor"] = "bogus"
	if out := svgOf(t, s, Options{Transparent: true}); strings.Contains(out, "<rect") {
		t.Error("a transparent SVG has a background")
	}
	if out := svgOf(t, s, Options{}); !strings.Contains(out, `fill="#ffffff"/>`) {
		t.Error("a bad background isn't white")
	}
	r.IsDeleted = true
	if out := svgOf(t, s, Options{}); !strings.Contains(out, `viewBox="0 0 0 0"`) || strings.Contains(out, "<g") {
		t.Errorf("nothing live: %.200s", out)
	}
	var b strings.Builder
	svgSet(&b, Set{Kind: Fill, Ops: []Op{mv(pt(0, 0))}})
	svgSet(&b, Set{Kind: Fill, Color: ink})
	if b.Len() != 0 {
		t.Error("an invisible or empty set wrote a path")
	}
	if got := num(1.005); got != "1.01" && got != "1" {
		t.Errorf("num %s", got)
	}
}

func TestSVGTextImagesFrames(t *testing.T) {
	tx := el("text", 120, 25)
	tx.Text, tx.Opacity = "a < b & \"c\"", 40
	hidden := el("text", 10, 10)
	hidden.Text, hidden.StrokeColor = "x", "transparent"
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.White)
	im := el("image", 40, 20)
	id := "f"
	im.FileID, im.Scale = &id, &[2]float64{-1, 1}
	lost := el("image", 5, 5)
	gone := "gone"
	lost.FileID = &gone
	fr := el("frame", 100, 50)
	s := scene(tx, hidden, im, lost, fr)
	s.Files[id] = &File{DataURL: pngURL(t, img)}
	out := svgOf(t, s, Options{})
	for _, want := range []string{
		`font-family="Excalifont-Regular" font-size="20" fill="#1e1e1e" fill-opacity="0.4" style="white-space:pre">a &lt; b &amp; &#34;c&#34;</text>`,
		`@font-face{font-family:"Excalifont-Regular";src:url(data:font/ttf;base64,`,
		`@font-face{font-family:"Manrope-Regular"`,
		`<image href="data:image/png;base64,`,
		`width="40" height="20" preserveAspectRatio="none" opacity="1" transform="translate(40 0) scale(-1 1)"/>`,
		`fill="#999999">Frame</text>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %s", want)
		}
	}
	if strings.Count(out, "<image") != 1 || strings.Count(out, "<text") != 2 || strings.Contains(out, "GeistMono") {
		t.Errorf("images %d, texts %d", strings.Count(out, "<image"), strings.Count(out, "<text"))
	}
	if !strings.Contains(out, `<text x="1" y="15" font-family="Manrope-Regular"`) {
		t.Error("the frame's name isn't at its left")
	}
	name := "Plan"
	fr.Name = &name
	im.Scale = &[2]float64{1, 1}
	out = svgOf(t, s, Options{})
	if !strings.Contains(out, ">Plan</text>") || strings.Contains(out, "scale(") {
		t.Error("a named frame, an image not flipped")
	}
}
