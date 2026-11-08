// Package kit is the window's look, shared with internal/infra: design tokens, fonts and
// the drawing primitives every view builds on. The style is dense and flat
// (IDE-like): chrome (the toolbar) is flush, separated by 1dp gaps over
// CLine; corners only from RCtl/RCard/RSheet.
package kit

import (
	"image/color"
	"runtime"

	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/font/opentype"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/fonts"
)

var (
	Sans = font.Font{Typeface: "Manrope"}
	Mono = font.Font{Typeface: "Geist Mono"} // card keys, tool lines, code, the terminal
	Hand = font.Font{Typeface: "Excalifont"} // drawings' hand-drawn text
)

func Semi(f font.Font) font.Font   { f.Weight = font.SemiBold; return f }
func Bold(f font.Font) font.Font   { f.Weight = font.Bold; return f }
func Medium(f font.Font) font.Font { f.Weight = font.Medium; return f }
func Italic(f font.Font) font.Font { f.Style = font.Italic; return f }

// Design tokens (dark), opaque: Gio has no backdrop blur, so the panels are
// solid tints of the window background.
var (
	CPanel      = RGB(0x22232a)
	CRim        = RGB(0x31323a)
	CPane       = RGB(0x0e0f13)
	CCard       = RGB(0x1c1d23)
	CPanelCard  = RGB(0x2b2c33)
	CHover      = RGB(0x30313a)
	CLine       = RGB(0x2c2d34)
	CText       = RGB(0xf5f5f7)
	CText2      = RGB(0xb8b8c0)
	CText3      = RGB(0x85858e)
	CAcc        = RGB(0x0a84ff)
	CAccText    = RGB(0x64b5ff)
	CAccSoft    = RGB(0x14325a)
	CGreen      = RGB(0x30d158)
	COrange     = RGB(0xff9f0a)
	COrangeSoft = RGB(0x3d2c12)
	CPurple     = RGB(0xbf5af2)
	CPurpleSoft = RGB(0x33204a)
	CRed        = RGB(0xff6961)
	CIdle       = RGB(0x5a5a62)
	CWhite      = RGB(0xffffff)
	CScrim      = color.NRGBA{0, 0, 0, 0x88}
	CColumn     = RGB(0x17181d) // a board column, between CPane and CCard
)

// Accents are the hues columns and labels pick from: slate, blue, orange,
// green, purple, teal, pink, yellow.
var Accents = []color.NRGBA{RGB(0x8e8e9a), CAcc, COrange, CGreen, CPurple, RGB(0x40c8e0), RGB(0xff6fa8), RGB(0xffd60a)}

// Mix is a + (b-a)·f, per channel.
func Mix(a, b color.NRGBA, f float64) color.NRGBA {
	m := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*f) }
	return color.NRGBA{m(a.R, b.R), m(a.G, b.G), m(a.B, b.B), 0xff}
}

const (
	RCtl   unit.Dp = 4
	RCard  unit.Dp = 6
	RSheet unit.Dp = 8
)

func RGB(h uint32) color.NRGBA { return color.NRGBA{uint8(h >> 16), uint8(h >> 8), uint8(h), 0xff} }

// Kit holds what the primitives share: the text shaper, and the clickables
// kept by id across frames.
type Kit struct {
	Sh     *text.Shaper
	clicks map[string]*widget.Clickable // ponytail: never pruned, fine for a personal board's few hundred ids
}

// New loads Manrope and Geist Mono (embedded); past their glyphs (the
// symbols Claude Code's TUI draws, emoji) the system's fonts fill in.
func New() *Kit {
	var faces []font.FontFace
	for file, f := range map[string]font.Font{
		"Manrope-Regular": Sans, "Manrope-Medium": Medium(Sans), "Manrope-SemiBold": Semi(Sans), "Manrope-Bold": Bold(Sans),
		"GeistMono-Regular": Mono, "GeistMono-Bold": Bold(Mono),
		"GeistMono-Italic": Italic(Mono), "GeistMono-BoldItalic": Italic(Bold(Mono)),
		"seti": Seti, "Excalifont-Regular": Hand,
	} {
		b, _ := fonts.FS.ReadFile(file + ".ttf")
		if face, err := opentype.Parse(b); err == nil {
			faces = append(faces, font.FontFace{Font: f, Face: face})
		}
	}
	faces = append(faces, gofont.Collection()...)
	return &Kit{Sh: text.NewShaper(text.WithCollection(faces)), clicks: map[string]*widget.Clickable{}}
}

// ModText is how shortcut k reads on this platform.
func ModText(k string) string {
	if runtime.GOOS == "darwin" {
		return "⌘" + k
	}
	return "Ctrl+" + k
}
