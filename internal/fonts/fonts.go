// Package fonts embeds the fonts desk draws with: Manrope and Geist Mono for
// the window, seti for file icons and Excalifont for drawings' hand-drawn
// text. The window (ui/kit) and the drawing renderer (draw) both read them.
package fonts

import "embed"

//go:embed *.ttf *.txt
var FS embed.FS
