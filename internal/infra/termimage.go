package infra

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// A terminal as a picture, with the window's colors: the camera's frames
// (gui_camera.go, Linux) and Term.PNG (Desk Control's /screen).

// camRender draws a terminal with the window's colors, sizing the font so
// the whole screen fits the frame; it redraws only when the screen changed.
type camRender struct {
	term       *Term
	runs       [][]Run
	cx, cy     int
	cursor     bool
	cols, rows int
	frame      image.Point  // the image size the font was fitted to
	faces      [2]font.Face // regular, bold
	adv, lh    int          // cell width and height, px
	asc        int
	glyphs     map[camGlyph]camMask
}

type camGlyph struct {
	r    rune
	bold bool
}

type camMask struct {
	m  *image.Alpha
	at image.Point // offset from the dot
}

// camFonts: JetBrains Mono when fontconfig has it (as loadFonts), else Go Mono.
var camFonts = sync.OnceValue(func() [2]*opentype.Font {
	var fs [2]*opentype.Font
	for i, style := range []string{"Regular", "Bold"} {
		b := [][]byte{gomono.TTF, gomonobold.TTF}[i]
		if out, err := exec.Command("fc-match", "-f", "%{file}", "JetBrainsMono Nerd Font:style="+style).Output(); err == nil && strings.Contains(string(out), "JetBrains") {
			if s, err := os.ReadFile(string(out)); err == nil {
				b = s
			}
		}
		fs[i], _ = opentype.Parse(b)
		if fs[i] == nil {
			fs[i], _ = opentype.Parse([][]byte{gomono.TTF, gomonobold.TTF}[i])
		}
	}
	return fs
})

// fit picks the biggest font size whose cells fit cols×rows in a w×h frame.
func (r *camRender) fit(cols, rows int, frame image.Point) {
	r.cols, r.rows, r.frame, r.glyphs = cols, rows, frame, map[camGlyph]camMask{}
	fs := camFonts()
	for size := float64(frame.Y / max(rows, 1)); size >= 4; size-- {
		for i := range r.faces {
			r.faces[i], _ = opentype.NewFace(fs[i], &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
		}
		m := r.faces[0].Metrics()
		a, _ := r.faces[0].GlyphAdvance('M')
		r.adv, r.lh, r.asc = a.Ceil(), (m.Ascent+m.Descent).Ceil()+int(size/8), m.Ascent.Ceil()
		if r.adv*cols <= frame.X && r.lh*rows <= frame.Y {
			return
		}
	}
}

func (r *camRender) glyph(g camGlyph) camMask {
	if m, ok := r.glyphs[g]; ok {
		return m
	}
	f := r.faces[0]
	if g.bold {
		f = r.faces[1]
	}
	var m camMask
	if dr, mask, mp, _, ok := f.Glyph(fixed.Point26_6{}, g.r); ok && !dr.Empty() {
		a := image.NewAlpha(image.Rect(0, 0, dr.Dx(), dr.Dy()))
		draw.Draw(a, a.Bounds(), mask, mp, draw.Src) // the face reuses its mask buffer
		m = camMask{a, dr.Min}
	}
	r.glyphs[g] = m
	return m
}

// draw paints t into img and reports whether anything changed.
func (r *camRender) draw(img *image.RGBA, t *Term) bool {
	var runs [][]Run
	var cx, cy int
	var cursor bool
	if t != nil {
		runs, _, cx, cy, cursor = t.Grid(0)
	}
	frame := img.Rect.Size()
	if t == r.term && cx == r.cx && cy == r.cy && cursor == r.cursor && frame == r.frame && reflect.DeepEqual(runs, r.runs) {
		return false
	}
	r.term, r.runs, r.cx, r.cy, r.cursor = t, runs, cx, cy, cursor
	draw.Draw(img, img.Bounds(), image.NewUniform(cPane), image.Point{}, draw.Src)
	if t == nil {
		r.frame, r.cols, r.rows = frame, 0, 0 // unchanged until a terminal shows; fit again for it
		return true
	}
	if cols, rows := t.Size(); cols != r.cols || rows != r.rows || frame != r.frame {
		r.fit(cols, rows, frame)
	}
	ox, oy := (frame.X-r.adv*r.cols)/2, (frame.Y-r.lh*r.rows)/2
	for y, row := range runs {
		py := oy + y*r.lh
		for _, run := range row {
			fg, bg := cellColor(run.Style.Fg, cText), cellColor(run.Style.Bg, color.NRGBA{})
			if run.Style.Attrs&uv.AttrReverse != 0 {
				fg, bg = cellColor(run.Style.Bg, cPane), cellColor(run.Style.Fg, cText)
			}
			if run.Style.Attrs&uv.AttrFaint != 0 {
				fg.A = 0x90
			}
			x := ox + run.X*r.adv
			if bg.A != 0 {
				draw.Draw(img, image.Rect(x, py, x+run.W*r.adv, py+r.lh), image.NewUniform(bg), image.Point{}, draw.Over)
			}
			src := image.NewUniform(fg)
			for _, ch := range run.Text {
				w := ansi.StringWidth(string(ch))
				if ch != ' ' && w > 0 {
					if m := r.glyph(camGlyph{ch, run.Style.Attrs&uv.AttrBold != 0}); m.m != nil {
						at := image.Pt(x, py+r.asc).Add(m.at)
						draw.DrawMask(img, m.m.Bounds().Add(at), src, image.Point{}, m.m, image.Point{}, draw.Over)
					}
				}
				x += w * r.adv
			}
		}
	}
	if cursor && cy < len(runs) {
		x, y := ox+cx*r.adv, oy+cy*r.lh
		draw.Draw(img, image.Rect(x, y, x+r.adv, y+r.lh), image.NewUniform(cAcc), image.Point{}, draw.Over)
	}
	return true
}

// PNG is the terminal's screen now as a picture, 11×22 px a cell at most.
// Any goroutine.
func (t *Term) PNG() []byte {
	cols, rows := t.Size()
	img := image.NewRGBA(image.Rect(0, 0, max(cols, 1)*11, max(rows, 1)*22))
	var r camRender
	r.draw(img, t)
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}
