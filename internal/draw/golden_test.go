package draw

import (
	"bytes"
	"encoding/json"
	"flag"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"testing"
)

// -update rewrites the golden files from what the code draws now; look at
// testdata/showcase.png before committing them.
var update = flag.Bool("update", false, "rewrite testdata's golden files")

// showcase is a drawing with every element type and style, seeded, so its
// geometry and its picture can be pinned.
func showcase(t *testing.T) *Scene {
	s := NewScene()
	n := int64(0)
	add := func(typ string, x, y, w, h float64) *Element {
		n++
		e := New(typ)
		e.ID, e.Seed, e.X, e.Y, e.Width, e.Height = typ+string(rune('a'+n%26)), 1000+n*7919, x, y, w, h
		s.Elements = append(s.Elements, e)
		return e
	}
	str := func(v string) *string { return &v }
	for row, r := range []float64{0, 1, 2} {
		y := 40 + float64(row)*170
		for col, typ := range []string{"rectangle", "diamond", "ellipse"} {
			for k, fs := range []string{"solid", "hachure", "cross-hatch"} {
				e := add(typ, 40+float64(col*3+k)*160, y, 130, 100)
				e.Roughness, e.FillStyle = r, fs
				e.BackgroundColor = []string{"#a5d8ff", "#ffc9c9", "#b2f2bb"}[k]
				e.StrokeStyle = []string{"solid", "dashed", "dotted"}[(k+row)%3]
				if k == 1 {
					e.Roundness = nil
				}
				if row == 2 && k == 2 {
					v := 6.0
					e.Roundness = &Roundness{Type: RoundAdaptive, Value: &v}
				}
			}
		}
	}
	for i, head := range []string{"arrow", "triangle", "bar", "dot", "diamond", "triangle_outline", "circle_outline", "diamond_outline"} {
		e := add("arrow", 40+float64(i)*180, 560, 150, 60)
		e.Points = [][2]float64{{0, 0}, {70, 60}, {150, 20}}
		e.EndArrowhead = str(head)
		e.StrokeColor = []string{"#1e1e1e", "#e03131", "#2f9e44", "#1971c2", "#f08c00", "#6741d9", "#0c8599", "#c2255c"}[i]
		if i%2 == 1 {
			e.StartArrowhead, e.Roundness = str("arrow"), nil
		}
		if i == 6 {
			e.StrokeStyle = "dashed"
		}
	}
	loop := add("line", 40, 680, 120, 100)
	loop.Points, loop.BackgroundColor = [][2]float64{{0, 0}, {120, 10}, {100, 100}, {2, 4}}, "#ffec99"
	sharp := add("line", 200, 680, 120, 100)
	sharp.Points, sharp.BackgroundColor, sharp.Roundness, sharp.FillStyle = loop.Points, "#d0bfff", nil, "hachure"
	open := add("line", 360, 680, 120, 100)
	open.Points, open.StrokeWidth = [][2]float64{{0, 50}, {40, 0}, {80, 100}, {120, 50}}, 4
	elbow := add("arrow", 520, 680, 120, 100)
	elbow.Points, elbow.Elbowed = [][2]float64{{0, 0}, {60, 0}, {60, 100}, {120, 100}}, true
	for i, ff := range []int{FontExcalifont, FontNunito, FontComic} {
		tx := add("text", 700+float64(i)*240, 680, 220, 50)
		tx.FontFamily, tx.FontSize, tx.Text = ff, 18, "Hello, Claude!\nañadir → café"
		tx.TextAlign = []string{"left", "center", "right"}[i]
	}
	pen := add("freedraw", 40, 830, 0, 0)
	pen.SimulatePressure = false
	for i := 0; i <= 40; i++ {
		pen.Points = append(pen.Points, [2]float64{float64(i) * 9, 30 * math.Sin(float64(i)/5)})
		pen.Pressures = append(pen.Pressures, float64(i)/40)
	}
	sim := add("freedraw", 420, 830, 0, 0)
	for i := 0; i <= 30; i++ {
		sim.Points = append(sim.Points, [2]float64{float64(i*i) / 3, 20 * math.Cos(float64(i)/4)})
	}
	fr := add("frame", 760, 800, 400, 140)
	fr.Name = str("Architecture")
	turned := add("rectangle", 820, 840, 120, 60)
	turned.Angle, turned.BackgroundColor, turned.Opacity = 0.4, "#ffec99", 60
	note := add("text", 980, 850, 160, 30)
	note.Text, note.Angle, note.StrokeColor = "turned text", -0.3, "#e03131"
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for x := range 4 {
		img.Set(x, 0, color.RGBA{uint8(60 * x), 120, 200, 255})
		img.Set(x, 1, color.RGBA{200, uint8(60 * x), 80, 255})
	}
	pic := add("image", 1200, 820, 120, 60)
	id := "pic"
	pic.FileID = &id
	s.Files[id] = &File{ID: id, MimeType: "image/png", DataURL: pngURL(t, img)}
	return s
}

func TestGoldenShapes(t *testing.T) {
	s := showcase(t)
	type shape struct {
		ID   string
		Sets []Set
	}
	var got []shape
	for _, e := range s.Elements {
		got = append(got, shape{e.ID, Shape(e, s.Background())})
	}
	const file = "testdata/showcase.shapes.json"
	if *update {
		b, _ := json.Marshal(got)
		if err := os.WriteFile(file, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var want []shape
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("%d elements, golden %d", len(got), len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if len(g.Sets) != len(w.Sets) {
			t.Errorf("%s: %d sets, golden %d", w.ID, len(g.Sets), len(w.Sets))
			continue
		}
	sets:
		for j := range w.Sets {
			gs, ws := g.Sets[j], w.Sets[j]
			if gs.Kind != ws.Kind || gs.Color != ws.Color || gs.Width != ws.Width || len(gs.Dash) != len(ws.Dash) || len(gs.Ops) != len(ws.Ops) {
				t.Errorf("%s set %d: %v %v %v %v %d ops, golden %v %v %v %v %d", w.ID, j, gs.Kind, gs.Color, gs.Width, gs.Dash, len(gs.Ops), ws.Kind, ws.Color, ws.Width, ws.Dash, len(ws.Ops))
				continue
			}
			for k := range ws.Dash {
				if gs.Dash[k] != ws.Dash[k] {
					t.Errorf("%s set %d dash %v, golden %v", w.ID, j, gs.Dash, ws.Dash)
				}
			}
			for k, op := range ws.Ops {
				for p := range op.P {
					if gs.Ops[k].Kind != op.Kind || math.Abs(gs.Ops[k].P[p].X-op.P[p].X) > 1e-9 || math.Abs(gs.Ops[k].P[p].Y-op.P[p].Y) > 1e-9 {
						t.Errorf("%s set %d op %d: %c %v, golden %c %v", w.ID, j, k, gs.Ops[k].Kind, gs.Ops[k].P, op.Kind, op.P)
						continue sets
					}
				}
			}
		}
	}
}

// TestGoldenPicture pins the showcase's pixels (text, images, frames,
// rotation, the rasterizer) within a channel tolerance for the vector
// package's per-arch accumulators.
func TestGoldenPicture(t *testing.T) {
	got := Render(showcase(t), Options{Padding: 20})
	const file = "testdata/showcase.png"
	if *update {
		b, _ := PNG(showcase(t), Options{Padding: 20})
		if err := os.WriteFile(file, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	w, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if w.Bounds() != got.Bounds() {
		t.Fatalf("size %v, golden %v", got.Bounds(), w.Bounds())
	}
	off := 0
	for y := range got.Bounds().Dy() {
		for x := range got.Bounds().Dx() {
			g := got.RGBAAt(x, y)
			r, gg, bb, a := w.At(x, y).RGBA()
			for _, d := range []int{int(g.R) - int(r>>8), int(g.G) - int(gg>>8), int(g.B) - int(bb>>8), int(g.A) - int(a>>8)} {
				if d > 8 || d < -8 {
					off++
					break
				}
			}
		}
	}
	if off > 0 {
		t.Errorf("%d pixels differ from %s (run with -update and look at it if the change is meant)", off, file)
	}
}
