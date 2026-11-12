package kit

import (
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/keys"
)

// ZoomKey is a zoom shortcut's steps: zoom.in (Ctrl+= or Ctrl++) +1,
// zoom.out (Ctrl+-) -1, zoom.reset (Ctrl+0) 0, back to 100%. ok is false
// for any other key or a release.
func ZoomKey(e key.Event) (steps int, ok bool) {
	switch {
	case keys.Is("zoom.in", e):
		return 1, true
	case keys.Is("zoom.out", e):
		return -1, true
	case keys.Is("zoom.reset", e):
		return 0, true
	}
	return 0, false
}

// ZoomFilters are ZoomKey's keys, for a focus-less listener.
func ZoomFilters() []event.Filter { return keys.Filters(nil, "zoom.in", "zoom.out", "zoom.reset") }

// Newline puts a line break in ed on text.newline (Alt+Enter), and takes
// Shift+Enter, which Gio's editor would make one: a modifier comes with Ctrl
// or Alt. Call it before ed is laid out, so its keys come here first.
func Newline(gtx layout.Context, ed *widget.Editor) {
	fs := append(keys.Filters(ed, "text.newline"), key.Filter{Focus: ed, Name: key.NameReturn, Required: key.ModShift},
		key.Filter{Focus: ed, Name: key.NameEnter, Required: key.ModShift})
	for {
		ev, ok := gtx.Event(fs...)
		if !ok {
			return
		}
		if e, ok := ev.(key.Event); ok && keys.Is("text.newline", e) && !ed.ReadOnly {
			ed.Insert("\n")
		}
	}
}

// Zoomed is gtx with its text at pct percent of its size: sizes in sp
// scale, sizes in dp don't.
func Zoomed(gtx layout.Context, pct int) layout.Context {
	gtx.Metric.PxPerSp *= float32(pct) / 100
	return gtx
}
