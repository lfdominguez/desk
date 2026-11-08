package draw

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	_ "image/gif"  // images in drawings
	_ "image/jpeg" // images in drawings
	"image/png"
	"math"
	"strings"

	"github.com/andybalholm/stroke"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
	"golang.org/x/image/vector"
	_ "golang.org/x/image/webp" // images in drawings
)

// Options shape a render.
type Options struct {
	Scale       float64 // pixels per scene unit; 0 means 1
	Padding     float64 // scene units around the drawing
	MaxSide     int     // the longest side in pixels at most (Scale shrinks); 0 for no cap
	Transparent bool    // no background (else the scene's viewBackgroundColor, white by default)
}

// Render draws the scene's live elements, fitted to their bounds, without a
// GPU: what Claude sees when it reads a drawing, and PNG export.
func Render(s *Scene, o Options) *image.RGBA {
	x0, y0, x1, y1 := s.Bounds()
	if x0 > x1 {
		x0, y0, x1, y1 = 0, 0, 0, 0
	}
	sc := o.Scale
	if sc <= 0 {
		sc = 1
	}
	w, h := (x1-x0+2*o.Padding)*sc, (y1-y0+2*o.Padding)*sc
	if m := float64(o.MaxSide); m > 0 && math.Max(w, h) > m {
		f := m / math.Max(w, h)
		sc, w, h = sc*f, w*f, h*f
	}
	img := image.NewRGBA(image.Rect(0, 0, max(int(math.Ceil(w)), 1), max(int(math.Ceil(h)), 1)))
	canvas := s.Background()
	if !o.Transparent {
		xdraw.Draw(img, img.Bounds(), image.NewUniform(canvas), image.Point{}, xdraw.Src)
	}
	view := Scale(sc).Mul(Translate(o.Padding-x0, o.Padding-y0))
	for _, e := range s.Elements {
		if e.IsDeleted {
			continue
		}
		m := view.Mul(e.Transform())
		for _, set := range Shape(e, canvas) {
			paint(img, set, m, sc)
		}
		switch e.Type {
		case "text":
			drawText(img, e, m, sc)
		case "image":
			drawImage(img, s, e, m)
		case "frame", "magicframe":
			drawFrameName(img, e, view, sc)
		}
	}
	return img
}

// Background is the canvas color: the scene's viewBackgroundColor, white
// when it has none or it doesn't parse.
func (s *Scene) Background() color.NRGBA {
	if v, ok := s.AppState["viewBackgroundColor"].(string); ok {
		if c, ok := Color(v, 100); ok {
			return c
		}
	}
	return color.NRGBA{0xff, 0xff, 0xff, 0xff}
}

// PNG is Render encoded.
func PNG(s *Scene, o Options) ([]byte, error) {
	var b bytes.Buffer
	err := png.Encode(&b, Render(s, o))
	return b.Bytes(), err
}

// Bounds is the box around the live elements (frame names included); empty
// (x0 > x1) without any.
func (s *Scene) Bounds() (x0, y0, x1, y1 float64) {
	x0, y0, x1, y1 = math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, e := range s.Elements {
		if e.IsDeleted {
			continue
		}
		a, b, c, d := e.Bounds()
		if e.Type == "frame" || e.Type == "magicframe" {
			b -= frameNameSize + 6
		}
		pad := e.StrokeWidth / 2
		x0, y0, x1, y1 = math.Min(x0, a-pad), math.Min(y0, b-pad), math.Max(x1, c+pad), math.Max(y1, d+pad)
	}
	return
}

// paint fills or strokes one set through m (element units → pixels).
func paint(img *image.RGBA, set Set, m Aff, sc float64) {
	path := contours(set.Ops, m)
	if set.Kind == Stroke {
		if len(set.Dash) > 0 {
			pat := make([]float32, len(set.Dash))
			for i, d := range set.Dash {
				pat[i] = float32(d * sc)
			}
			path = stroke.Dash(path, pat, 0)
		}
		path = stroke.Stroke(path, stroke.Options{Width: float32(math.Max(set.Width*sc, 0.5)), Cap: stroke.RoundCap, Join: stroke.RoundJoin})
	}
	fillPath(img, path, set.Color)
}

// contours are the ops through m as cubic segments, one contour per move.
func contours(ops []Op, m Aff) [][]stroke.Segment {
	var out [][]stroke.Segment
	var cur []stroke.Segment
	var at stroke.Point
	p := func(q Pt) stroke.Point { r := m.Apply(q); return stroke.Pt(float32(r.X), float32(r.Y)) }
	for _, o := range ops {
		switch o.Kind {
		case 'M':
			if len(cur) > 0 {
				out = append(out, cur)
			}
			cur, at = nil, p(o.P[0])
		case 'L':
			end := p(o.P[0])
			cur = append(cur, stroke.LinearSegment(at, end))
			at = end
		case 'C':
			s := stroke.Segment{Start: at, CP1: p(o.P[0]), CP2: p(o.P[1]), End: p(o.P[2])}
			cur = append(cur, s)
			at = s.End
		}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// fillPath fills the contours (closed implicitly) over only the pixels they
// cover, since a rasterizer the size of the image costs its whole area.
func fillPath(img *image.RGBA, path [][]stroke.Segment, c color.NRGBA) {
	if len(path) == 0 {
		return
	}
	x0, y0, x1, y1 := float32(math.Inf(1)), float32(math.Inf(1)), float32(math.Inf(-1)), float32(math.Inf(-1))
	for _, cs := range path {
		for _, s := range cs {
			for _, q := range [4]stroke.Point{s.Start, s.CP1, s.CP2, s.End} {
				x0, y0, x1, y1 = min(x0, q.X), min(y0, q.Y), max(x1, q.X), max(y1, q.Y)
			}
		}
	}
	r := image.Rect(int(math.Floor(float64(x0))), int(math.Floor(float64(y0))), int(math.Ceil(float64(x1)))+1, int(math.Ceil(float64(y1)))+1).Intersect(img.Bounds())
	if r.Empty() {
		return
	}
	z := vector.NewRasterizer(r.Dx(), r.Dy())
	ox, oy := float32(r.Min.X), float32(r.Min.Y)
	for _, cs := range path {
		z.MoveTo(cs[0].Start.X-ox, cs[0].Start.Y-oy)
		for _, s := range cs {
			z.CubeTo(s.CP1.X-ox, s.CP1.Y-oy, s.CP2.X-ox, s.CP2.Y-oy, s.End.X-ox, s.End.Y-oy)
		}
		z.ClosePath()
	}
	z.Draw(img, r, image.NewUniform(c), image.Point{})
}

// drawImage paints an image element's file into its box.
func drawImage(img *image.RGBA, s *Scene, e *Element, m Aff) {
	src := fileImage(s, e)
	if src == nil {
		c := color.NRGBA{0x99, 0x99, 0x99, 0xff}
		box := []Op{mv(pt(0, 0)), ln(pt(e.Width, 0)), ln(pt(e.Width, e.Height)), ln(pt(0, e.Height)), ln(pt(0, 0))}
		paint(img, Set{Kind: Stroke, Ops: box, Color: c, Width: 1, Dash: []float64{4, 4}}, m, 1)
		return
	}
	b := src.Bounds()
	fit := m
	if e.Scale != nil && e.Scale[0] < 0 {
		fit = fit.Mul(Aff{A: -1, D: 1, E: e.Width})
	}
	if e.Scale != nil && e.Scale[1] < 0 {
		fit = fit.Mul(Aff{A: 1, D: -1, F: e.Height})
	}
	fit = fit.Mul(Aff{A: e.Width / float64(b.Dx()), D: e.Height / float64(b.Dy()), E: -float64(b.Min.X), F: -float64(b.Min.Y)})
	var mask image.Image
	if e.Opacity < 100 {
		mask = image.NewUniform(color.Alpha{uint8(255 * math.Max(e.Opacity, 0) / 100)})
	}
	xdraw.CatmullRom.Transform(img, f64.Aff3{fit.A, fit.C, fit.E, fit.B, fit.D, fit.F}, src, b, xdraw.Over, &xdraw.Options{SrcMask: mask})
}

// fileImage decodes the data: URL of e's file; nil when missing or not a
// raster image.
func fileImage(s *Scene, e *Element) image.Image {
	if e.FileID == nil || s.Files[*e.FileID] == nil {
		return nil
	}
	url := s.Files[*e.FileID].DataURL
	i := strings.Index(url, ";base64,")
	if i < 0 {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(url[i+8:])
	if err != nil {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	return img
}
