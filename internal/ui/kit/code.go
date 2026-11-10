package kit

import (
	"gioui.org/unit"
	"github.com/oligo/gvcode"
	gvcolor "github.com/oligo/gvcode/color"
	"github.com/oligo/gvcode/gutter"
	"github.com/oligo/gvcode/textstyle/syntax"
)

// CodeEditor is gvcode's editor in desk's look: Geist Mono, our colours,
// line numbers, no wrap (gvcode.WrapLine turns it on). scheme brings the syntax scopes (its palette is
// set here); gutters are added after the line numbers (a diff's, say).
func CodeEditor(scheme syntax.ColorScheme, gutters ...gutter.GutterProvider) *gvcode.Editor {
	scheme.Foreground = gvcolor.MakeColor(CText)
	scheme.Background = gvcolor.MakeColor(CPane)
	scheme.SelectColor = gvcolor.MakeColor(CAcc).MulAlpha(0x60)
	scheme.LineColor = gvcolor.MakeColor(CPanelCard).MulAlpha(0x80)
	scheme.LineNumberColor = gvcolor.MakeColor(CText3)
	e := &gvcode.Editor{}
	e.WithOptions(
		gvcode.WrapLine(false),
		gvcode.WithFont(Mono),
		gvcode.WithTextSize(13),
		gvcode.WithLineHeight(0, 1.4),
		gvcode.WithTabWidth(4),
		gvcode.WithGutterGap(unit.Dp(12)),
		gvcode.WithColorScheme(scheme),
		gvcode.WithDefaultGutters(),
	)
	for _, g := range gutters {
		e.WithOptions(gvcode.WithGutter(g))
	}
	return e
}
