package draw

import (
	"cmp"
	"image"
	"image/color"
	"math"
	"strings"
	"sync"

	"github.com/lfdominguez/desk/internal/fonts"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/f64"
	"golang.org/x/image/math/fixed"

	xdraw "golang.org/x/image/draw"
)

const frameNameSize = 14

// Face files for Excalidraw's font families: hand-drawn ones get Excalifont,
// sans ones desk's Manrope, code ones Geist Mono (Nunito, Lilita and
// Cascadia aren't embedded).
var (
	fontOnce  sync.Once
	fontFiles = map[string]*sfnt.Font{}
)

func fontFile(name string) *sfnt.Font {
	fontOnce.Do(func() {
		for _, n := range []string{"Excalifont-Regular", "Manrope-Regular", "Manrope-Bold", "GeistMono-Regular"} {
			b, _ := fonts.FS.ReadFile(n + ".ttf")
			if f, err := opentype.Parse(b); err == nil {
				fontFiles[n] = f
			}
		}
	})
	return fontFiles[name]
}

// FontFile is the embedded font a family draws with, by file name.
func FontFile(family int) string {
	switch family {
	case FontHelvetica, FontNunito:
		return "Manrope-Regular"
	case FontLilita:
		return "Manrope-Bold"
	case FontCascadia, FontComic:
		return "GeistMono-Regular"
	}
	return "Excalifont-Regular"
}

// faces is a family's face at size, then the others for glyphs it lacks.
type faces struct {
	fonts []*sfnt.Font
	faces []font.Face
	buf   sfnt.Buffer
}

func newFaces(family int, size float64) *faces {
	f := &faces{}
	main := FontFile(family)
	for _, n := range []string{main, "Manrope-Regular", "Excalifont-Regular", "GeistMono-Regular"} {
		if n == main && len(f.fonts) > 0 {
			continue
		}
		ff := fontFile(n) // embedded: never missing
		face, err := opentype.NewFace(ff, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
		if err == nil {
			f.fonts, f.faces = append(f.fonts, ff), append(f.faces, face)
		}
	}
	return f
}

// pick is the face that has r.
func (f *faces) pick(r rune) font.Face {
	for i, ff := range f.fonts {
		if g, err := ff.GlyphIndex(&f.buf, r); err == nil && g != 0 {
			return f.faces[i]
		}
	}
	return f.faces[0]
}

func (f *faces) measure(s string) float64 {
	var w fixed.Int26_6
	for _, r := range s {
		a, _ := f.pick(r).GlyphAdvance(r)
		w += a
	}
	return float64(w) / 64
}

func (f *faces) draw(dst *image.RGBA, s string, x, baseline float64, c color.NRGBA) {
	d := font.Drawer{Dst: dst, Src: image.NewUniform(c), Dot: fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(baseline * 64)}}
	for _, r := range s {
		d.Face = f.pick(r)
		d.DrawString(string(r))
	}
}

// TextLine is one line of a text element in its own units: where it starts
// and its baseline, for a renderer that has the same fonts.
type TextLine struct {
	Text        string
	X, Baseline float64
}

// Lines is e's text laid out as Excalidraw does: the stored line breaks,
// lineHeight × fontSize apart, the font's ascent and descent centered in
// each line, aligned in the element's width.
func Lines(e *Element) []TextLine {
	return linesAt(e, newFaces(e.FontFamily, e.FontSize), 1)
}

func linesAt(e *Element, f *faces, sc float64) []TextLine {
	lh := e.LineHeight
	if lh == 0 {
		lh = 1.25
	}
	lhPx := e.FontSize * lh * sc
	met := f.faces[0].Metrics()
	asc, desc := float64(met.Ascent)/64, float64(met.Descent)/64
	var out []TextLine
	for i, l := range strings.Split(e.Text, "\n") {
		x := 0.0
		switch e.TextAlign {
		case "center":
			x = (e.Width*sc - f.measure(l)) / 2
		case "right":
			x = e.Width*sc - f.measure(l)
		}
		out = append(out, TextLine{Text: l, X: x, Baseline: float64(i)*lhPx + (lhPx-asc-desc)/2 + asc})
	}
	return out
}

// FitText sizes a text element to its text: the widest line, and a line
// height per line.
func (e *Element) FitText() {
	lh := e.LineHeight
	if lh == 0 {
		lh = 1.25
	}
	f, w := newFaces(e.FontFamily, e.FontSize), 0.0
	lines := strings.Split(e.Text, "\n")
	for _, l := range lines {
		w = math.Max(w, f.measure(l))
	}
	e.Width, e.Height = w, float64(len(lines))*e.FontSize*lh
}

// boundPad is Excalidraw's BOUND_TEXT_PADDING: a label's room from its shape's edge.
const boundPad = 5

// inner is how much of shape c's side s a label uses (Excalidraw's
// getBoundTextMaxWidth/Height: an ellipse's inscribed box, half a diamond).
func inner(c *Element, s float64) float64 {
	switch c.Type {
	case "ellipse":
		s = math.Round(s / 2 * math.Sqrt2)
	case "diamond":
		s = math.Round(s / 2)
	}
	return s - 2*boundPad
}

// fitLabel lays label t out in its shape c as Excalidraw does: its text
// (OriginalText) wrapped to c's width, c taller when the lines need it,
// t in the middle. An arrow's label isn't wrapped.
func fitLabel(t, c *Element) {
	src := cmp.Or(t.OriginalText, t.Text)
	t.Text = src
	if max := inner(c, c.Width); c.Type != "arrow" && c.Type != "line" && max > 0 {
		t.Text = wrap(src, newFaces(t.FontFamily, t.FontSize), max)
	}
	t.FitText()
	if c.Type != "arrow" && c.Type != "line" {
		need := t.Height + 2*boundPad // inner's inverse
		switch c.Type {
		case "ellipse":
			need *= math.Sqrt2
		case "diamond":
			need *= 2
		}
		c.Height = math.Max(c.Height, need)
	}
	center(t, c)
}

// wrap breaks s's lines at spaces so none is wider than max, and a word
// wider than max between its letters.
func wrap(s string, f *faces, max float64) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		cur := ""
		for _, w := range strings.Fields(line) {
			if cur != "" && f.measure(cur+" "+w) <= max {
				cur += " " + w
				continue
			}
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			for _, r := range w {
				if cur != "" && f.measure(cur+string(r)) > max {
					out = append(out, cur)
					cur = ""
				}
				cur += string(r)
			}
		}
		out = append(out, cur)
	}
	return strings.Join(out, "\n")
}

// drawText paints a text element: drawn upright at pixel size into its own
// image, then placed through m.
func drawText(img *image.RGBA, e *Element, m Aff, sc float64) {
	c, ok := Color(e.StrokeColor, e.Opacity)
	if !ok { // empty or sizeless text draws nothing by itself
		return
	}
	f := newFaces(e.FontFamily, e.FontSize*sc)
	pad := int(e.FontSize * sc)
	tmp := image.NewRGBA(image.Rect(0, 0, int(math.Ceil(e.Width*sc))+2*pad, int(math.Ceil(e.Height*sc))+2*pad))
	for _, l := range linesAt(e, f, sc) {
		f.draw(tmp, l.Text, l.X+float64(pad), l.Baseline+float64(pad), c)
	}
	t := m.Mul(Scale(1 / sc)).Mul(Translate(-float64(pad), -float64(pad)))
	if e.Angle == 0 {
		at := image.Pt(int(math.Round(t.E)), int(math.Round(t.F)))
		xdraw.Draw(img, tmp.Bounds().Add(at), tmp, image.Point{}, xdraw.Over)
		return
	}
	xdraw.BiLinear.Transform(img, f64.Aff3{t.A, t.C, t.E, t.B, t.D, t.F}, tmp, tmp.Bounds(), xdraw.Over, nil)
}

// drawFrameName writes a frame's name above its top left corner.
func drawFrameName(img *image.RGBA, e *Element, view Aff, sc float64) {
	name := "Frame"
	if e.Name != nil && *e.Name != "" {
		name = *e.Name
	}
	f := newFaces(FontNunito, frameNameSize*sc)
	p := view.Apply(pt(e.X, e.Y-4))
	f.draw(img, name, p.X, p.Y-float64(f.faces[0].Metrics().Descent)/64, color.NRGBA{0x99, 0x99, 0x99, 0xff})
}
