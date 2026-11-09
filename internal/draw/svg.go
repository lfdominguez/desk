package draw

import (
	"encoding/base64"
	"fmt"
	"html"
	"image/color"
	"math"
	"strconv"
	"strings"

	"github.com/lfdominguez/desk/internal/fonts"
)

// SVG is the scene as a self-contained SVG: the same geometry as Render,
// text as text with its fonts embedded, images inline. It is what a drawing
// is shared as.
func SVG(s *Scene, o Options) []byte {
	x0, y0, x1, y1 := s.Bounds()
	if x0 > x1 {
		x0, y0, x1, y1 = 0, 0, 0, 0
	}
	sc := o.Scale
	if sc <= 0 {
		sc = 1
	}
	w, h := x1-x0+2*o.Padding, y1-y0+2*o.Padding
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %s %s" width="%s" height="%s">`, num(w), num(h), num(w*sc), num(h*sc))
	used, clips := map[string]bool{}, map[string]bool{}
	var body strings.Builder
	canvas := s.Background()
	if !o.Transparent {
		fmt.Fprintf(&body, `<rect width="100%%" height="100%%" fill="%s"%s/>`, hex(canvas), opacityAttr("fill", canvas))
	}
	view := Translate(o.Padding-x0, o.Padding-y0)
	for _, e := range s.Elements {
		if e.IsDeleted {
			continue
		}
		m := view.Mul(e.Transform())
		if f := s.FrameOf(e); f != nil { // clipped to its frame
			fx0, fy0, fx1, fy1 := f.Bounds()
			p := view.Apply(pt(fx0, fy0))
			if !clips[f.ID] {
				clips[f.ID] = true
				fmt.Fprintf(&body, `<clipPath id="frame-%s"><rect x="%s" y="%s" width="%s" height="%s"/></clipPath>`, html.EscapeString(f.ID), num(p.X), num(p.Y), num(fx1-fx0), num(fy1-fy0))
			}
			fmt.Fprintf(&body, `<g clip-path="url(#frame-%s)">`, html.EscapeString(f.ID))
		}
		fmt.Fprintf(&body, `<g transform="matrix(%s %s %s %s %s %s)">`, num(m.A), num(m.B), num(m.C), num(m.D), num(m.E), num(m.F))
		for _, set := range Shape(e, canvas) {
			svgSet(&body, set)
		}
		switch e.Type {
		case "text":
			if c, ok := Color(e.StrokeColor, e.Opacity); ok && e.Text != "" {
				file := FontFile(e.FontFamily)
				used[file] = true
				for _, l := range Lines(e) {
					fmt.Fprintf(&body, `<text x="%s" y="%s" font-family="%s" font-size="%s" fill="%s"%s style="white-space:pre">%s</text>`,
						num(l.X), num(l.Baseline), file, num(e.FontSize), hex(c), opacityAttr("fill", c), html.EscapeString(l.Text))
				}
			}
		case "image":
			if e.FileID != nil && s.Files[*e.FileID] != nil {
				flip := ""
				if e.Scale != nil && (e.Scale[0] < 0 || e.Scale[1] < 0) {
					flip = fmt.Sprintf(` transform="translate(%s %s) scale(%s %s)"`, num(math.Max(0, -e.Scale[0])*e.Width), num(math.Max(0, -e.Scale[1])*e.Height), num(math.Copysign(1, e.Scale[0])), num(math.Copysign(1, e.Scale[1])))
				}
				fmt.Fprintf(&body, `<image href="%s" width="%s" height="%s" preserveAspectRatio="none" opacity="%s"%s/>`,
					html.EscapeString(s.Files[*e.FileID].DataURL), num(e.Width), num(e.Height), num(e.Opacity/100), flip)
			}
		}
		body.WriteString(`</g>`)
		if s.FrameOf(e) != nil {
			body.WriteString(`</g>`)
		}
		if e.Type == "frame" || e.Type == "magicframe" {
			name := "Frame"
			if e.Name != nil && *e.Name != "" {
				name = *e.Name
			}
			used["Manrope-Regular"] = true
			p := view.Apply(pt(e.X, e.Y-6))
			fmt.Fprintf(&body, `<text x="%s" y="%s" font-family="Manrope-Regular" font-size="%d" fill="#999999">%s</text>`, num(p.X), num(p.Y), frameNameSize, html.EscapeString(name))
		}
	}
	if len(used) > 0 {
		b.WriteString(`<defs><style>`)
		for _, f := range []string{"Excalifont-Regular", "Manrope-Regular", "Manrope-Bold", "GeistMono-Regular"} {
			if used[f] {
				ttf, _ := fonts.FS.ReadFile(f + ".ttf")
				fmt.Fprintf(&b, `@font-face{font-family:"%s";src:url(data:font/ttf;base64,%s)}`, f, base64.StdEncoding.EncodeToString(ttf))
			}
		}
		b.WriteString(`</style></defs>`)
	}
	b.WriteString(body.String())
	b.WriteString(`</svg>`)
	return []byte(b.String())
}

func svgSet(b *strings.Builder, set Set) {
	if set.Color.A == 0 || len(set.Ops) == 0 {
		return
	}
	var d strings.Builder
	for _, o := range set.Ops {
		switch o.Kind {
		case 'M', 'L':
			fmt.Fprintf(&d, "%c%s %s", o.Kind, num(o.P[0].X), num(o.P[0].Y))
		case 'C':
			fmt.Fprintf(&d, "C%s %s %s %s %s %s", num(o.P[0].X), num(o.P[0].Y), num(o.P[1].X), num(o.P[1].Y), num(o.P[2].X), num(o.P[2].Y))
		}
	}
	if set.Kind == Fill {
		fmt.Fprintf(b, `<path d="%s" fill="%s"%s/>`, d.String(), hex(set.Color), opacityAttr("fill", set.Color))
		return
	}
	dash := ""
	if len(set.Dash) > 0 {
		parts := make([]string, len(set.Dash))
		for i, v := range set.Dash {
			parts[i] = num(v)
		}
		dash = ` stroke-dasharray="` + strings.Join(parts, " ") + `"`
	}
	fmt.Fprintf(b, `<path d="%s" fill="none" stroke="%s" stroke-width="%s" stroke-linecap="round" stroke-linejoin="round"%s%s/>`,
		d.String(), hex(set.Color), num(set.Width), opacityAttr("stroke", set.Color), dash)
}

func hex(c color.NRGBA) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// opacityAttr is the attribute for c's alpha, or nothing when opaque.
func opacityAttr(what string, c color.NRGBA) string {
	if c.A == 0xff {
		return ""
	}
	return fmt.Sprintf(` %s-opacity="%s"`, what, num(float64(c.A)/255))
}

func num(v float64) string { return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64) }
