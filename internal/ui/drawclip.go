package ui

import (
	"bytes"
	"io"
	"strings"

	"gioui.org/io/clipboard"
	"gioui.org/io/transfer"
	"gioui.org/layout"

	"github.com/lfdominguez/desk/internal/draw"
)

// Copy and paste in a drawing, as excalidraw.com does: Ctrl+C puts the
// selection on the clipboard as its clipboard JSON (text), so it pastes
// there too; Ctrl+V takes an image first (clipImage: Gio reads only
// text), then that JSON, then plain text as a text element.

// drawCopy puts st's selection on the clipboard; false with none.
func drawCopy(gtx layout.Context, st *drawState) bool {
	b := st.ed.Clipboard()
	if b == nil {
		return false
	}
	gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(bytes.NewReader(b))})
	return true
}

// drawPaste is Ctrl+V on the canvas: the clipboard's image if it holds
// one, else its text, read next frame (drawClipEvents).
func (u *ui) drawPaste(path string) {
	go func() {
		b, _, err := clipImage()
		u.later(func() {
			st := u.draws[path]
			switch {
			case st == nil:
			case err == nil && b != nil:
				u.drawAddImage(st, b)
			default:
				st.pasteText = true
			}
		})
	}()
}

// drawClipEvents asks for the clipboard's text when a paste wants it,
// and puts what comes in the middle of the view.
func (u *ui) drawClipEvents(gtx layout.Context, st *drawState) {
	if st.pasteText {
		st.pasteText = false
		gtx.Execute(clipboard.ReadCmd{Tag: &st.pasteText})
	}
	for {
		ev, ok := gtx.Event(transfer.TargetFilter{Target: &st.pasteText, Type: "application/text"})
		if !ok {
			return
		}
		if d, ok := ev.(transfer.DataEvent); ok {
			r := d.Open()
			b, _ := io.ReadAll(io.LimitReader(r, 50<<20))
			r.Close()
			u.drawPasteText(st, b)
		}
	}
}

// drawPasteText puts clipboard text b in st: excalidraw.com's copy as its
// elements, other text as a text element.
func (u *ui) drawPasteText(st *drawState, b []byte) {
	st.ed.EndText()
	if els, files, ok := draw.ParseClipboard(b); ok {
		st.ed.Insert(els, files, st.drawCenter())
		return
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return
	}
	t := draw.New("text")
	t.Text, t.OriginalText = s, s
	t.FitText()
	st.ed.Insert([]*draw.Element{t}, nil, st.drawCenter())
}
