package ui

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"time"

	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/lfdominguez/desk/internal/ui/kit"
)

// boardLayout is where the last frame put things, in board coordinates:
// pointer events and keys are hit-tested against it.
type boardLayout struct {
	cols  []image.Rectangle // each column's card area
	cards []hit             // the cards drawn, column by column, top down
}

type hit struct {
	id  string
	col int
	r   image.Rectangle
}

// colAccent is column i's color: by its name for the usual ones, so Blocked
// reads red and Done green wherever they sit; by place otherwise.
func (u *ui) colAccent(i int) color.NRGBA {
	if c, ok := columnColors[strings.ToLower(u.b.Columns[i].Title)]; ok {
		return c
	}
	return kit.Accents[i%len(kit.Accents)]
}

var columnColors = map[string]color.NRGBA{"backlog": kit.Accents[0], "to do": kit.CAcc, "in progress": kit.Accents[7], "blocked": kit.CRed, "review": kit.Accents[5], "done": kit.CGreen}

// hidden is whether the board leaves column col out: no view has the Done
// column, its cards show in the Done archive.
func (u *ui) hidden(col int) bool { return col == u.b.Column("Done") }

// visible is column col's card indices in the view that match the search.
// They are kept until the board, the search, the view or the day changes:
// the board, its header and the keys ask several times a frame, and a
// search runs fzf over every card's notes. Callers don't change the slice.
func (u *ui) visible(col int) []int {
	if u.hidden(col) {
		return nil
	}
	now := time.Now()
	q := u.search.Text()
	if k := fmt.Sprint(u.gen, u.cardCount(), q, u.view, now.Format(time.DateOnly)); k != u.visKey {
		u.visKey, u.vis = k, map[int][]int{}
	}
	if out, ok := u.vis[col]; ok {
		return out
	}
	out := []int{}
	for j, c := range u.b.Columns[col].Cards {
		if inView(u.view, c, now) && c.Matches(q) {
			out = append(out, j)
		}
	}
	u.vis[col] = out
	return out
}

// cardCount is how many cards the board's columns hold.
func (u *ui) cardCount() (n int) {
	for _, c := range u.b.Columns {
		n += len(c.Cards)
	}
	return n
}

// board lays the columns out by hand (equal widths, 10dp apart) so every
// card's rectangle is known for dragging, scrolling and the keys.
func (u *ui) board(gtx layout.Context) layout.Dimensions {
	sz := gtx.Constraints.Max
	n := len(u.b.Columns)
	for len(u.scroll) < n {
		u.scroll, u.maxScroll = append(u.scroll, 0), append(u.maxScroll, 0)
	}
	kit.Fill(gtx, kit.CPane)
	area := clip.Rect{Max: sz}.Push(gtx.Ops)
	defer area.Pop()
	u.pointer(gtx)
	pad, gap := gtx.Dp(12), gtx.Dp(10)
	shown := 0
	for i := range n {
		if !u.hidden(i) {
			shown++
		}
	}
	w := (sz.X - 2*pad - (shown-1)*gap) / max(shown, 1) // ponytail: columns shrink without limit; scroll sideways if boards get wide
	var lay boardLayout
	x := pad
	for i := range n {
		if u.hidden(i) { // an empty slot: lay.cols stays indexed by column
			lay.cols = append(lay.cols, image.Rectangle{})
			continue
		}
		u.column(gtx, i, image.Rect(x, pad, x+w, sz.Y-pad), &lay)
		x += w + gap
	}
	u.lay = lay
	if u.drag.on {
		u.dragOverlay(gtx)
	}
	if u.menu != nil {
		u.drawCardMenu(gtx)
	}
	return layout.Dimensions{Size: sz}
}

// column draws column i in r: header, then its cards and the add button,
// scrolled by u.scroll[i].
func (u *ui) column(gtx layout.Context, i int, r image.Rectangle, lay *boardLayout) {
	k, col, acc := u.k, u.b.Columns[i], u.colAccent(i)
	rad := gtx.Dp(kit.RSheet)
	kit.Panel(gtx, r, kit.RSheet, kit.CColumn, color.NRGBA{})
	top := clip.UniformRRect(r, rad).Push(gtx.Ops) // the accent stripe follows the top corners
	paint.FillShape(gtx.Ops, acc, clip.Rect(image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+gtx.Dp(3))).Op())
	top.Pop()

	hdr := gtx.Dp(42)
	st := op.Offset(r.Min).Push(gtx.Ops)
	hgtx := gtx
	hgtx.Constraints = layout.Exact(image.Pt(r.Dx(), hdr))
	layout.Inset{Top: 3, Left: 14, Right: 6}.Layout(hgtx, func(gtx layout.Context) layout.Dimensions {
		return layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, 0) // W keeps Min.Y: it would stretch the row's children
			n := fmt.Sprint(len(col.Cards))
			if v := len(u.visible(i)); v != len(col.Cards) {
				n = fmt.Sprintf("%d/%d", v, len(col.Cards))
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(kit.Dot(acc)),
				layout.Rigid(kit.HGap(9)),
				layout.Rigid(k.Lbl(col.Title, 13, kit.CText, kit.Semi(kit.Sans))),
				layout.Rigid(kit.HGap(8)),
				layout.Rigid(k.Pill(n, kit.CText2, kit.CPanelCard)),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)}
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					d, ok := k.Button(gtx, fmt.Sprint("add", i), color.NRGBA{}, kit.CHover, kit.RCtl, layout.UniformInset(5), kit.Icon(14, kit.CText3, kit.IcoPlus))
					if ok {
						u.newCard(i)
					}
					return d
				}))
		})
	})
	st.Pop()

	body := image.Rect(r.Min.X, r.Min.Y+hdr, r.Max.X, r.Max.Y)
	lay.cols = append(lay.cols, body)
	cl := clip.Rect(body).Push(gtx.Ops)
	defer cl.Pop()
	u.scroll[i] = min(max(u.scroll[i], 0), u.maxScroll[i])
	scroll := u.scroll[i] // this frame's; the wheel may move u.scroll mid-frame
	in, gap := gtx.Dp(10), gtx.Dp(8)
	cw := body.Dx() - 2*in
	y := body.Min.Y - scroll
	place := func(w layout.Widget, alpha float32) image.Rectangle {
		m := op.Record(gtx.Ops)
		cgtx := gtx
		cgtx.Constraints = layout.Constraints{Min: image.Pt(cw, 0), Max: image.Pt(cw, 1<<20)}
		d := w(cgtx)
		call := m.Stop()
		cr := image.Rectangle{Min: image.Pt(body.Min.X+in, y), Max: image.Pt(body.Min.X+in+d.Size.X, y+d.Size.Y)}
		st := op.Offset(cr.Min).Push(gtx.Ops)
		o := paint.PushOpacity(gtx.Ops, alpha)
		call.Add(gtx.Ops)
		o.Pop()
		st.Pop()
		y += d.Size.Y + gap
		return cr
	}
	done := i == u.b.Column("Done")
	if u.cardHs == nil || len(u.cardHs) > 4*u.cardCount()+64 { // cards gone: start over
		u.cardHs = map[string]cardH{}
	}
	hk := fmt.Sprint(u.gen, cw, time.Now().Unix()/60, len(u.infraTasks), len(u.terms)) // what a card's height draws from, its "ago" included
	for _, j := range u.visible(i) {
		c := col.Cards[j]
		if ch, ok := u.cardHs[c.ID]; ok && ch.key == hk && c.ID != u.sel && (y+ch.h < body.Min.Y || y > body.Max.Y) {
			// scrolled out, and its height known: only its place, for the
			// pointer and the keys (laying ~100 cards out a frame also
			// overflows Gio's text cache, so every label shaped again)
			cr := image.Rect(body.Min.X+in, y, body.Min.X+in+cw, y+ch.h)
			y += ch.h + gap
			lay.cards = append(lay.cards, hit{c.ID, i, cr})
			continue
		}
		alpha := float32(1)
		if u.drag.on && u.drag.press == c.ID {
			alpha = .3 // the card's old place while it is dragged
		}
		cr := place(func(gtx layout.Context) layout.Dimensions {
			d := u.cardBody(gtx, c, c.ID == u.sel, c.ID == u.hover && !u.drag.on, done)
			if !u.drag.on {
				s := clip.Rect{Max: d.Size}.Push(gtx.Ops)
				pointer.CursorPointer.Add(gtx.Ops)
				s.Pop()
			}
			return d
		}, alpha)
		u.cardHs[c.ID] = cardH{hk, cr.Dy()}
		lay.cards = append(lay.cards, hit{c.ID, i, cr})
	}
	place(func(gtx layout.Context) layout.Dimensions { return u.addButton(gtx, i) }, 1)
	u.maxScroll[i] = max(0, y+scroll-gap+in-body.Max.Y)
}

// cardH is a card's height as laid out, and what it was laid out from.
type cardH struct {
	key string
	h   int
}

// addButton is the foot of column i: "+ Add card", which opens the dialog.
func (u *ui) addButton(gtx layout.Context, i int) layout.Dimensions {
	k := u.k
	d, ok := k.Button(gtx, fmt.Sprint("qa", i), color.NRGBA{}, kit.CHover, kit.RCtl, layout.Inset{Top: 7, Bottom: 7, Left: 8, Right: 8}, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(kit.Icon(13, kit.CText3, kit.IcoPlus)),
			layout.Rigid(kit.HGap(8)),
			layout.Rigid(k.Lbl("Add card", 12, kit.CText3, kit.Medium(kit.Sans))))
	})
	if ok {
		u.newCard(i)
	}
	return d
}
