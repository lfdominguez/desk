package ui

import (
	"fmt"
	"image"
	"io"
	"path/filepath"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/lfdominguez/desk/internal/draw"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// Images and the library in a drawing. An image comes from a file (the
// toolbar's Image, 9: the system's file chooser) or the clipboard (Ctrl+V)
// and goes in the middle of the view. The library is the organization's
// .excalidrawlib beside its drawings (excalidraw.com opens it too): the
// selection added as an item, an item put in the middle of the view.

// pickImage asks the user for a picture file; nil, nil: they cancelled.
// The window sets it (a file chooser needs it); tests replace it.
var pickImage = func() (io.ReadCloser, error) { return nil, fmt.Errorf("no file chooser here") }

// drawMaxImage is how big an image goes in, in scene units a side at most.
const drawMaxImage = 600

// drawCenter is the middle of st's view, in scene units.
func (st *drawState) drawCenter() draw.Pt { return st.toScene(layout.FPt(st.size.Div(2)), st.px) }

// drawAddImage puts picture b in the middle of st's view.
func (u *ui) drawAddImage(st *drawState, b []byte) {
	st.ed.EndText()
	if _, err := st.ed.AddImage(b, st.drawCenter(), drawMaxImage); err != nil {
		u.showToast("Couldn't add the image: "+err.Error(), true)
	}
}

// drawPickImage asks for a picture file, off the UI goroutine, and adds it
// to the drawing at path.
func (u *ui) drawPickImage(path string) {
	go func() {
		r, err := pickImage()
		var b []byte
		if err == nil && r != nil {
			b, err = io.ReadAll(io.LimitReader(r, 50<<20))
			r.Close()
		}
		u.later(func() {
			st := u.draws[path]
			switch {
			case err != nil:
				u.showToast("Couldn't open the image: "+err.Error(), true)
			case st != nil && b != nil:
				u.drawAddImage(st, b)
			}
		})
	}()
}

// drawPaste is Ctrl+V on the canvas: the clipboard's image, if it holds one.
func (u *ui) drawPaste(path string) {
	go func() {
		b, _, err := clipImage()
		u.later(func() {
			if st := u.draws[path]; st != nil && err == nil && b != nil {
				u.drawAddImage(st, b)
			}
		})
	}()
}

// drawLib is an organization's library as desk shows it.
type drawLib struct {
	lib    *draw.Library
	err    error
	thumbs map[string]drawThumb // by item id
	list   layout.List
}

func (u *ui) libPath(org string) string {
	return filepath.Join(u.drawingsDir(org), "library.excalidrawlib")
}

// library is org's library, read once.
func (u *ui) library(org string) *drawLib {
	if l := u.libs[org]; l != nil {
		return l
	}
	l := &drawLib{thumbs: map[string]drawThumb{}}
	l.lib, l.err = draw.LoadLibrary(u.libPath(org))
	l.list.Axis = layout.Vertical
	u.libs[org] = l
	return l
}

// libAdd puts st's selection in its organization's library.
func (u *ui) libAdd(st *drawState) {
	l := u.library(st.org)
	cs := st.ed.Copies()
	if l.err != nil || len(cs) == 0 {
		return
	}
	l.lib.Add(cs)
	u.libSave(st.org, l)
}

// libRemove takes item id out of org's library, with an undo.
func (u *ui) libRemove(org, id string) {
	l := u.library(org)
	for i, it := range l.lib.Items {
		if it.ID == id {
			l.lib.Items = append(l.lib.Items[:i:i], l.lib.Items[i+1:]...)
			u.libSave(org, l)
			u.showUndo("Removed from the library", func() {
				l.lib.Items = append(l.lib.Items[:i:i], append([]draw.LibraryItem{it}, l.lib.Items[i:]...)...)
				u.libSave(org, l)
			})
			return
		}
	}
}

func (u *ui) libSave(org string, l *drawLib) {
	if err := l.lib.Save(u.libPath(org)); err != nil {
		u.showToast("Couldn't keep the library: "+err.Error(), true)
	}
	u.drawGen++
}

// libThumb is item it's picture, drawn once.
func (l *drawLib) libThumb(it draw.LibraryItem, dark bool) drawThumb {
	key := it.ID + fmt.Sprint(dark)
	if t, ok := l.thumbs[key]; ok {
		return t
	}
	s := draw.NewScene()
	s.Elements = it.Elements
	if dark {
		s.AppState["theme"] = "dark"
	}
	img := draw.Render(themed(s, nil), draw.Options{Padding: 8, MaxSide: 200, Transparent: true})
	t := drawThumb{img: paint.NewImageOp(img), size: img.Bounds().Size()}
	l.thumbs[key] = t
	return t
}

// drawLibrary is the library panel, over the canvas on the right: Add the
// selection, then the items; a click puts one in the middle of the view.
func (u *ui) drawLibrary(gtx layout.Context, st *drawState, size image.Point, act *func()) {
	k := u.k
	l := u.library(st.org)
	w, pad := gtx.Dp(240), gtx.Dp(12)
	top := pad + gtx.Dp(50)
	g := gtx
	g.Constraints = layout.Exact(image.Pt(w, max(size.Y-top-pad, gtx.Dp(120))))
	m := op.Record(gtx.Ops)
	d := kit.Boxed(kit.CPanel, kit.CRim, kit.RCard, layout.UniformInset(10), func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = gtx.Constraints.Max
		head := func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, k.LblFill("Library", 13, kit.CText, kit.Semi(kit.Sans))),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					kind := kit.Secondary
					if len(st.ed.Sel) == 0 || l.err != nil {
						kind = kit.Off
					}
					d, ok := k.KBtn(gtx, "libAdd", kind, "Add selection", "")
					if ok {
						*act = func() { u.libAdd(st) }
					}
					return d
				}))
		}
		body := func(gtx layout.Context) layout.Dimensions {
			msg := ""
			switch {
			case l.err != nil:
				msg = "Couldn't read the library: " + l.err.Error()
			case len(l.lib.Items) == 0:
				msg = "Nothing here yet: select something and Add selection. It is kept for the organization's drawings, in its library.excalidrawlib."
			}
			if msg != "" {
				return k.Label(gtx, msg, 12, kit.CText3, kit.Sans, 0)
			}
			items := l.lib.Items
			return l.list.Layout(gtx, len(items), func(gtx layout.Context, i int) layout.Dimensions {
				it := items[i]
				return layout.Inset{Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, gtx.Dp(90)))
					d, ok := k.RimButton(gtx, "libItem"+it.ID, kit.CPane, kit.CHover, kit.CLine, kit.RCard, layout.UniformInset(1), func(gtx layout.Context) layout.Dimensions {
						size := gtx.Constraints.Max
						t := l.libThumb(it, st.dark())
						f := min(float32(size.X-16)/float32(max(t.size.X, 1)), float32(size.Y-16)/float32(max(t.size.Y, 1)), 1)
						tw, th := float32(t.size.X)*f, float32(t.size.Y)*f
						tr := op.Affine(f32.Affine2D{}.Scale(f32.Point{}, f32.Pt(f, f)).Offset(f32.Pt((float32(size.X)-tw)/2, (float32(size.Y)-th)/2))).Push(gtx.Ops)
						t.img.Add(gtx.Ops)
						cl := clip.Rect{Max: t.size}.Push(gtx.Ops)
						paint.PaintOp{}.Add(gtx.Ops)
						cl.Pop()
						tr.Pop()
						return layout.Dimensions{Size: size}
					})
					if ok && *act == nil {
						*act = func() { st.ed.EndText(); st.ed.Insert(it.Elements, nil, st.drawCenter()) }
					}
					bin := op.Offset(image.Pt(d.Size.X-gtx.Dp(30), gtx.Dp(4))).Push(gtx.Ops)
					bd, ok := k.IconBtn(gtx, "libDel"+it.ID, kit.IcoTrash, false)
					k.Tip(gtx, "libDel"+it.ID, bd, "Remove from the library", true)
					if ok {
						*act = func() { u.libRemove(st.org, it.ID) }
					}
					bin.Pop()
					return d
				})
			})
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, layout.Rigid(head), layout.Rigid(kit.VGap(10)), layout.Flexed(1, body))
	})(g)
	call := m.Stop()
	defer op.Offset(image.Pt(size.X-w-pad, top)).Push(gtx.Ops).Pop()
	block := clip.Rect{Max: d.Size}.Push(gtx.Ops) // a press on the panel isn't the canvas's
	event.Op(gtx.Ops, l)
	block.Pop()
	call.Add(gtx.Ops)
}
