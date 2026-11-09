package ui

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/lfdominguez/desk/internal/draw"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// An organization's drawings are .excalidraw files in <Org>/.desk/drawings:
// the dr:<org> view lists them, draw:<path> edits one (drawview.go).

// drawItem is a drawing on disk.
type drawItem struct {
	name, path string
	mod        time.Time
}

// drawThumb is a drawing's picture for the list, for the file as it was at mod.
type drawThumb struct {
	mod  time.Time
	img  paint.ImageOp
	size image.Point
}

func (u *ui) drawingsDir(org string) string {
	return filepath.Join(orgs.Dir(u.root, org, ""), orgs.DeskDir, "drawings")
}

// drawings are org's drawings, newest first, read once and after desk
// changes them (drawRead).
func (u *ui) drawings(org string) []drawItem { return u.drawingsIn(u.drawingsDir(org)) }

// drawingsIn are the drawings in dir, newest first, read once and after
// desk changes them (drawRead).
func (u *ui) drawingsIn(dir string) []drawItem {
	if l, ok := u.drawList[dir]; ok {
		return l
	}
	l := readDrawings(dir)
	u.drawList[dir] = l
	return l
}

// drawPlace is a list of drawings: an organization's (dr:<org>), or a
// card's own (drc:<card id>), in a folder inside its organization's.
type drawPlace struct{ org, card string }

// placeOf is view's list of drawings; ok false: view isn't one.
func (u *ui) placeOf(view string) (drawPlace, bool) {
	if org, ok := strings.CutPrefix(view, "dr:"); ok {
		return drawPlace{org: org}, true
	}
	if id, ok := strings.CutPrefix(view, "drc:"); ok {
		if c := u.b.Card(id); c != nil {
			return drawPlace{org: c.Org, card: id}, true
		}
	}
	return drawPlace{}, false
}

func (u *ui) placeDir(p drawPlace) string {
	if p.card != "" {
		return u.cardDrawingsDir(p.org, p.card)
	}
	return u.drawingsDir(p.org)
}

func (p drawPlace) view() string {
	if p.card != "" {
		return "drc:" + p.card
	}
	return "dr:" + p.org
}

// readDrawings are the drawings in dir, newest first.
func readDrawings(dir string) []drawItem {
	var l []drawItem
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		name, ok := strings.CutSuffix(e.Name(), ".excalidraw")
		if fi, err := e.Info(); ok && err == nil && !e.IsDir() && !strings.HasPrefix(name, ".") {
			l = append(l, drawItem{name, filepath.Join(dir, e.Name()), fi.ModTime()})
		}
	}
	slices.SortFunc(l, func(a, b drawItem) int { return b.mod.Compare(a.mod) })
	return l
}

// drawRead forgets the lists read, read again when next shown.
func (u *ui) drawRead() {
	clear(u.drawList)
	u.drawGen++
}

// drawingName is a name no drawing in dir has yet: base, then "base 2"…
func drawingName(dir, base string) string {
	name := base
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, name+".excalidraw")); err != nil { // free (or unreadable: Save says why)
			return name
		}
		name = fmt.Sprintf("%s %d", base, i)
	}
}

// newDrawing makes an empty drawing in p and opens it.
func (u *ui) newDrawing(p drawPlace) {
	s := draw.NewScene()
	s.AppState["theme"] = "dark" // desk is dark; the toggle keeps either
	dir := u.placeDir(p)
	path := filepath.Join(dir, drawingName(dir, "Untitled")+".excalidraw")
	if err := s.Save(path); err != nil {
		u.showToast("Couldn't make the drawing: "+err.Error(), true)
		return
	}
	u.drawRead()
	u.openDrawing(p.org, path)
}

// openDrawing shows the drawing at path, read once while desk runs.
func (u *ui) openDrawing(org, path string) {
	if _, ok := u.draws[path]; !ok {
		s, err := draw.Load(path)
		if err != nil {
			u.showToast("Couldn't open the drawing: "+err.Error(), true)
			return
		}
		st := &drawState{org: org, path: path, ed: draw.NewEditor(s), zoom: 1, side: loadSide(path)}
		if dir := filepath.Dir(path); dir != u.drawingsDir(org) { // a card's own
			st.card = filepath.Base(dir)
		}
		st.written, _ = s.Marshal()
		st.title.SingleLine, st.title.Submit = true, true
		st.claude.in.Submit, st.reworkIn.Submit = true, true // Enter sends; Shift+Enter a new line
		st.title.SetText(drawName(path))
		u.draws[path] = st
	}
	u.view = "draw:" + path
}

// drawSides are the kinds of desk's files beside a drawing (drawSide).
var drawSides = []string{"desk.json", "chat.jsonl"}

func drawName(path string) string { return strings.TrimSuffix(filepath.Base(path), ".excalidraw") }

// deleteDrawing removes a drawing, with an undo that writes it back.
func (u *ui) deleteDrawing(path string) {
	b, err := os.ReadFile(path)
	if err == nil {
		err = os.Remove(path)
	}
	if err != nil {
		u.showToast("Couldn't delete the drawing: "+err.Error(), true)
		return
	}
	sides := map[string][]byte{} // its sidecars, back with the undo
	for _, kind := range drawSides {
		if b, err := os.ReadFile(drawSide(path, kind)); err == nil && os.Remove(drawSide(path, kind)) == nil {
			sides[kind] = b
		}
	}
	if st := u.draws[path]; st != nil {
		st.drawStop()
	}
	delete(u.draws, path)
	delete(u.chats, drawChatKey(path))
	u.drawRead()
	u.showUndo("Deleted "+drawName(path), func() {
		err := os.WriteFile(path, b, 0o644)
		for kind, b := range sides {
			if e := os.WriteFile(drawSide(path, kind), b, 0o644); err == nil {
				err = e
			}
		}
		if err != nil {
			u.showToast("Couldn't restore the drawing: "+err.Error(), true)
		}
		u.drawRead()
	})
}

// renameDrawing gives the open drawing st a new name, its file renamed.
func (u *ui) renameDrawing(st *drawState, name string) {
	name = strings.TrimSpace(strings.NewReplacer("/", "-", "\\", "-").Replace(name))
	if name == "" || name == drawName(st.path) {
		st.title.SetText(drawName(st.path))
		return
	}
	to := filepath.Join(filepath.Dir(st.path), name+".excalidraw")
	if _, err := os.Stat(to); err == nil {
		u.showToast("A drawing named "+name+" exists already", true)
		st.title.SetText(drawName(st.path))
		return
	}
	if err := os.Rename(st.path, to); err != nil {
		u.showToast("Couldn't rename the drawing: "+err.Error(), true)
		return
	}
	for _, kind := range drawSides {
		os.Rename(drawSide(st.path, kind), drawSide(to, kind)) // none is fine
	}
	st.drawStop() // its tools name the old file: the next message resumes it on the new one
	delete(u.chats, drawChatKey(st.path))
	delete(u.draws, st.path)
	st.path = to
	u.draws[to] = st
	u.view = "draw:" + to
	u.drawRead()
}

// thumb is item's picture, rendered again when its file changed.
func (u *ui) thumb(it drawItem) (drawThumb, bool) {
	if t, ok := u.thumbs[it.path]; ok && t.mod.Equal(it.mod) {
		return t, t.size != image.Point{}
	}
	t := drawThumb{mod: it.mod}
	if s, err := draw.Load(it.path); err == nil && len(s.Elements) > 0 {
		img := draw.Render(themed(s, nil), draw.Options{Padding: 16, MaxSide: 520})
		t.img, t.size = paint.NewImageOp(img), img.Bounds().Size()
	}
	u.thumbs[it.path] = t
	return t, t.size != image.Point{}
}

// drawingsView is a list of drawings (dr:<org>, drc:<card>): New
// drawing, then the drawings as cards.
func (u *ui) drawingsView(gtx layout.Context) layout.Dimensions {
	place, _ := u.placeOf(u.view)
	org := place.org
	gtx.Constraints.Min = gtx.Constraints.Max
	kit.Fill(gtx, kit.CColumn)
	k := u.k
	var act func()
	defer func() {
		if act != nil {
			act()
		}
	}()
	items := u.drawingsIn(u.placeDir(place))
	const cw, ch, gap = 260, 210, 16
	return layout.UniformInset(20).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		cols := max(1, (gtx.Constraints.Max.X+gtx.Dp(gap))/gtx.Dp(cw+gap))
		rows := (len(items) + cols) / cols // the New card first
		return u.drList.Layout(gtx, rows, func(gtx layout.Context, r int) layout.Dimensions {
			var kids []layout.FlexChild
			for c := 0; c < cols; c++ {
				i := r*cols + c - 1
				if i >= len(items) {
					break
				}
				if c > 0 {
					kids = append(kids, layout.Rigid(kit.HGap(gap)))
				}
				kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints = layout.Exact(image.Pt(gtx.Dp(cw), gtx.Dp(ch)))
					if i < 0 {
						d, ok := k.RimButton(gtx, "drNew"+org, kit.CPane, kit.CHover, kit.CLine, kit.RCard, layout.UniformInset(0), func(gtx layout.Context) layout.Dimensions {
							return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
								return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
									layout.Rigid(kit.Icon(28, kit.CAccText, kit.IcoPlus)), layout.Rigid(kit.VGap(8)),
									layout.Rigid(k.Lbl("New drawing", 13, kit.CText2, kit.Semi(kit.Sans))))
							})
						})
						if ok {
							act = func() { u.newDrawing(place) }
						}
						return d
					}
					return u.drawCard(gtx, org, items[i], &act)
				}))
			}
			return layout.Inset{Bottom: gap}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{}.Layout(gtx, kids...)
			})
		})
	})
}

// drawCard is one drawing in the list: its picture, name and age; a
// click opens it, its bin deletes it.
func (u *ui) drawCard(gtx layout.Context, org string, it drawItem, act *func()) layout.Dimensions {
	k := u.k
	d, ok := k.RimButton(gtx, "drOpen"+it.path, kit.CPane, kit.CHover, kit.CLine, kit.RCard, layout.UniformInset(1), func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = gtx.Constraints.Max
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				size := gtx.Constraints.Max
				t, ok := u.thumb(it)
				if !ok {
					return layout.Center.Layout(gtx, k.Lbl("Empty", 12, kit.CText3, kit.Sans))
				}
				f := min(float32(size.X)/float32(t.size.X), float32(size.Y)/float32(t.size.Y), 1)
				w, h := float32(t.size.X)*f, float32(t.size.Y)*f
				st := op.Affine(f32.Affine2D{}.Scale(f32.Point{}, f32.Pt(f, f)).Offset(f32.Pt((float32(size.X)-w)/2, (float32(size.Y)-h)/2))).Push(gtx.Ops)
				t.img.Add(gtx.Ops)
				cl := clip.Rect{Max: t.size}.Push(gtx.Ops)
				paint.PaintOp{}.Add(gtx.Ops)
				cl.Pop()
				st.Pop()
				return layout.Dimensions{Size: size}
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				kit.Fill(gtx, kit.CPanel)
				return layout.Inset{Top: 8, Bottom: 8, Left: 12, Right: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
								layout.Rigid(k.Lbl(it.name, 13, kit.CText, kit.Semi(kit.Sans))),
								layout.Rigid(k.Lbl(ago(it.mod, gtx.Now), 11, kit.CText3, kit.Sans)))
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							d, ok := k.IconBtn(gtx, "drDel"+it.path, kit.IcoTrash, false)
							k.Tip(gtx, "drDel"+it.path, d, "Delete", true)
							if ok {
								*act = func() { u.deleteDrawing(it.path) }
							}
							return d
						}))
				})
			}))
	})
	if ok && *act == nil {
		*act = func() { u.openDrawing(org, it.path) }
	}
	return d
}
