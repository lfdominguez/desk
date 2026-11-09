package ui

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"math"

	"gioui.org/f32"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/lfdominguez/desk/internal/draw"
)

// themed is s as shown: through Excalidraw's dark filter when its theme is
// dark (its colors, not its images), what the eraser went over faded.
// s itself is never changed: the file keeps the colors as drawn.
func themed(s *draw.Scene, erasing map[string]bool) *draw.Scene {
	dark := s.AppState["theme"] == "dark"
	if !dark && len(erasing) == 0 {
		return s
	}
	c := *s
	c.AppState = map[string]any{}
	for k, v := range s.AppState {
		c.AppState[k] = v
	}
	if dark {
		c.AppState["viewBackgroundColor"] = darkHex(hexOf(s.Background()))
	}
	c.Elements = make([]*draw.Element, len(s.Elements))
	for i, e := range s.Elements {
		t := *e
		if dark {
			t.StrokeColor, t.BackgroundColor = darkHex(e.StrokeColor), darkHex(e.BackgroundColor)
		}
		if erasing[e.ID] {
			t.Opacity = e.Opacity * .25
		}
		c.Elements[i] = &t
	}
	return &c
}

func hexOf(c color.NRGBA) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// darkHex is color s through CSS's invert(93%) hue-rotate(180deg), the
// filter Excalidraw's dark theme puts over its canvas; "" and
// transparent stay.
func darkHex(s string) string {
	c, ok := draw.Color(s, 100)
	if !ok {
		return s
	}
	return hexOf(darkColor(c))
}

func darkColor(c color.NRGBA) color.NRGBA {
	inv := func(v uint8) float64 { return .93 - .86*float64(v)/255 }
	r, g, b := inv(c.R), inv(c.G), inv(c.B)
	ch := func(v float64) uint8 { return uint8(math.Round(math.Max(0, math.Min(1, v)) * 255)) }
	return color.NRGBA{ch(-.574*r + 1.43*g + .144*b), ch(.426*r + .43*g + .144*b), ch(.426*r + 1.43*g - .856*b), c.A}
}

// aff is a drawing's map as Gio's.
func aff(m draw.Aff) f32.Affine2D {
	return f32.NewAffine2D(float32(m.A), float32(m.C), float32(m.E), float32(m.B), float32(m.D), float32(m.F))
}

// paintScene draws s's elements in scene units (the caller's transform
// maps them to the window), sc window pixels to a unit; skip (the text
// being typed) draws as the editor instead.
func (st *drawState) paintScene(ops *op.Ops, s *draw.Scene, sc float32, skip string) {
	used := map[string]bool{}
	for _, e := range s.Elements {
		if e.IsDeleted || e.ID == skip {
			continue
		}
		used[e.ID] = true
		st.paintRaster(ops, s, e, sc)
	}
	for id := range st.rasters {
		if !used[id] {
			delete(st.rasters, id)
		}
	}
}

// quarter is a zoom rounded to an eighth of an octave: what is drawn for
// a zoom is kept until it changes that much.
func quarter(sc float32) float32 {
	return float32(math.Pow(2, math.Round(math.Log2(float64(sc))*8)/8))
}

// drawRaster is an element drawn by internal/draw at a zoom, kept while
// what it looks like stays the same.
type drawRaster struct {
	key    string
	img    paint.ImageOp
	size   image.Point
	scaled float32 // its pixels per unit
	dx, dy float32 // its top left from the element's bounds' top left, units
}

// paintRaster draws e as internal/draw renders it (the picture Claude
// reads and PNG export makes), at the zoom shown rounded to an eighth of
// an octave and 4096 pixels a side at most. Gio's own paths can't be used:
// its GPU fill leaks hairlines below rough.js's stroke outlines.
// ponytail: one image per element; tiles if huge shapes at deep zoom need it.
func (st *drawState) paintRaster(ops *op.Ops, s *draw.Scene, e *draw.Element, sc float32) {
	at := *e
	at.X, at.Y = 0, 0 // the place is left out of the key: a move is no render
	b, _ := json.Marshal(&at)
	q := quarter(sc)
	key := fmt.Sprint(string(b), s.Background(), q)
	r := st.rasters[e.ID]
	if r == nil || r.key != key {
		one := &draw.Scene{Elements: []*draw.Element{&at}, Files: s.Files, AppState: s.AppState}
		x0, y0, x1, y1 := one.Bounds()
		pad := 8 + 2*e.StrokeWidth + 4*e.Roughness // rough strokes stray past the box
		side := math.Max(x1-x0, y1-y0) + 2*pad
		q = min(q, float32(4096/side))
		img := draw.Render(one, draw.Options{Scale: float64(q), Padding: pad, Transparent: true})
		r = &drawRaster{key: key, img: paint.NewImageOp(img), size: img.Bounds().Size(), scaled: q, dx: float32(-pad), dy: float32(-pad)}
		st.rasters[e.ID] = r
	}
	x0, y0, _, _ := (&draw.Scene{Elements: []*draw.Element{e}}).Bounds()
	t := op.Affine(f32.Affine2D{}.Scale(f32.Point{}, f32.Pt(1/r.scaled, 1/r.scaled)).Offset(f32.Pt(float32(x0)+r.dx, float32(y0)+r.dy))).Push(ops)
	r.img.Add(ops)
	cl := clip.Rect{Max: r.size}.Push(ops)
	paint.PaintOp{}.Add(ops)
	cl.Pop()
	t.Pop()
}
