package kit

import (
	"bytes"
	"image"
	"image/color"
	"reflect"
	"runtime"
	"testing"
	"time"

	"gioui.org/widget"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

// TestButtonRedraws: a click asks for another frame, since what it changes
// may have been drawn earlier in this one.
func TestButtonRedraws(t *testing.T) {
	k := New()
	var r input.Router
	var ops op.Ops
	clicks := 0
	frame := func() {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(200, 100))}
		if _, ok := k.Button(gtx, "b", CPanel, CHover, RCtl, layout.UniformInset(10), k.Lbl("Go", 12, CText, Sans)); ok {
			clicks++
		}
		r.Frame(&ops)
	}
	frame()
	if _, ok := r.WakeupTime(); ok {
		t.Fatal("redraw asked for before any click")
	}
	p := f32.Pt(5, 5)
	r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
	frame()
	if _, ok := r.WakeupTime(); clicks != 1 || !ok {
		t.Fatalf("clicks %d, redraw asked: %v", clicks, ok)
	}
}

// opBytes is the serialized ops: what a widget drew, to compare.
func opBytes(o *op.Ops) []byte {
	return append([]byte(nil), reflect.ValueOf(&o.Internal).Elem().FieldByName("data").Bytes()...)
}

func ctx(ops *op.Ops, w, h int) layout.Context {
	return layout.Context{Ops: ops, Metric: unit.Metric{PxPerDp: 2, PxPerSp: 2}, Constraints: layout.Constraints{Max: image.Pt(w, h)}, Now: time.Unix(100, 0)}
}

// TestIconsDistinct: every icon draws, sized to its box, and no two draw the same shape.
func TestIconsDistinct(t *testing.T) {
	icons := map[string]IconPath{"plus": IcoPlus, "x": IcoX, "search": IcoSearch, "clock": IcoClock, "chat": IcoChat,
		"warn": IcoWarn, "check": IcoCheck, "spark": IcoSpark, "terminal": IcoTerminal, "archive": IcoArchive, "edit": IcoEdit,
		"link": IcoLink, "send": IcoSend, "stop": IcoStop, "side": IcoSide, "star": IcoStar, "board": IcoBoard, "code": IcoCode,
		"lock": IcoLock, "down": IcoDown, "trash": IcoTrash,
		"pointer": IcoPointer, "hand": IcoHand, "square": IcoSquare, "diamond": IcoDiamond, "circle": IcoCircle, "arrow": IcoArrow,
		"line": IcoLine, "pencil": IcoPencil, "letter": IcoLetter, "eraser": IcoEraser, "undo": IcoUndo, "redo": IcoRedo, "moon": IcoMoon,
		"copy": IcoCopy, "front": IcoFront, "forward": IcoForward, "backward": IcoBackward, "back": IcoBack, "fit": IcoFit, "minus": IcoMinus, "shapes": IcoShapes}
	seen := map[string]string{}
	for name, ico := range icons {
		var ops op.Ops
		d := Icon(15, CText, ico)(ctx(&ops, 100, 100))
		if d.Size != image.Pt(30, 30) {
			t.Errorf("%s: size %v, want 30x30", name, d.Size)
		}
		b := string(opBytes(&ops))
		if other, ok := seen[b]; ok {
			t.Errorf("%s draws the same as %s", name, other)
		}
		seen[b] = name
	}
}

func TestFillRRect(t *testing.T) {
	var ops op.Ops
	FillRRect(&ops, image.Rect(0, 0, 10, 10), 4, color.NRGBA{})
	FillRRect(&ops, image.Rectangle{}, 4, CText)
	if len(opBytes(&ops)) != 0 {
		t.Fatal("a clear or empty rect drew")
	}
	FillRRect(&ops, image.Rect(0, 0, 100, 100), 8, CText)
	small := len(opBytes(&ops))
	ops.Reset()
	FillRRect(&ops, image.Rect(0, 0, 3000, 100), 8, CText)
	if big := len(opBytes(&ops)); small == 0 || big <= small {
		t.Fatalf("small %d bytes, big %d: big ones are pieced from rects and corners", small, big)
	}
}

func TestPanelRim(t *testing.T) {
	var a, b op.Ops
	Panel(ctx(&a, 100, 100), image.Rect(0, 0, 50, 50), RCtl, CPanel, color.NRGBA{})
	Panel(ctx(&b, 100, 100), image.Rect(0, 0, 50, 50), RCtl, CPanel, CRim)
	if len(opBytes(&b)) <= len(opBytes(&a)) {
		t.Fatal("a rim drew nothing more")
	}
}

func TestBoxSizes(t *testing.T) {
	var ops op.Ops
	gtx := ctx(&ops, 400, 400)
	box := func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: image.Pt(20, 10)} }
	if d := Boxed(CPanel, CRim, RCtl, layout.UniformInset(5), box)(gtx); d.Size != image.Pt(40, 30) {
		t.Errorf("Boxed %v, want 40x30", d.Size)
	}
	if d := HGap(7)(gtx); d.Size != image.Pt(14, 0) {
		t.Errorf("HGap %v", d.Size)
	}
	if d := VGap(7)(gtx); d.Size != image.Pt(0, 14) {
		t.Errorf("VGap %v", d.Size)
	}
	if d := Dot(CRed)(gtx); d.Size != image.Pt(14, 14) {
		t.Errorf("Dot %v", d.Size)
	}
	_ = Mat(gtx, CRed)
	n := len(opBytes(&ops))
	Fill(gtx, CRed)
	if len(opBytes(&ops)) == n {
		t.Error("Fill drew nothing")
	}
}

// TestWrap: widgets go left to right and start a new row when one doesn't fit.
func TestWrap(t *testing.T) {
	var ops op.Ops
	w := func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: image.Pt(60, 20)} }
	gtx := ctx(&ops, 150, 400)
	if d := Wrap(gtx, 5, []layout.Widget{w, w, w}); d.Size != image.Pt(130, 50) {
		t.Fatalf("wrap %v, want 130x50 (two rows)", d.Size)
	}
	if d := Wrap(gtx, 5, []layout.Widget{w, w}); d.Size != image.Pt(130, 20) {
		t.Fatalf("wrap %v, want 130x20 (one row)", d.Size)
	}
}

func TestLabels(t *testing.T) {
	k := New()
	var ops op.Ops
	gtx := ctx(&ops, 500, 100)
	gtx.Constraints.Min = image.Pt(500, 100)
	lbl := k.Lbl("Acme", 12, CText, Sans)(gtx)
	fill := k.LblFill("Acme", 12, CText, Sans)(gtx)
	if lbl.Size.X == 0 || lbl.Size.X >= 500 || fill.Size.X != 500 || lbl.Size.Y >= 100 {
		t.Fatalf("Lbl %v, LblFill %v", lbl.Size, fill.Size)
	}
	gtx.Constraints.Min = image.Point{}
	pill := k.Pill("Acme", CText, CPanel)(gtx)
	if in := k.Lbl("Acme", 11, CText, Semi(Sans))(gtx); pill.Size.X != in.Size.X+24 {
		t.Fatalf("pill %v: label plus 6dp each side", pill.Size)
	}
}

func TestIconBtnOn(t *testing.T) {
	k := New()
	var a, b op.Ops
	da, _ := k.IconBtn(ctx(&a, 100, 100), "a", IcoPlus, false)
	k.IconBtn(ctx(&b, 100, 100), "b", IcoPlus, true)
	if da.Size != image.Pt(50, 50) || bytes.Equal(opBytes(&a), opBytes(&b)) {
		t.Fatalf("size %v; on and off draw alike", da.Size)
	}
	if k.Btn(ctx(&a, 100, 100), "c", "Go", CText, CPanel) {
		t.Fatal("Btn clicked unprompted")
	}
}

// TestTipOnHover: the tip is drawn only while the pointer is on its button,
// right-aligned on request.
func TestTipOnHover(t *testing.T) {
	k := New()
	var r input.Router
	var ops op.Ops
	frame := func(right bool) []byte {
		ops.Reset()
		gtx := ctx(&ops, 300, 300)
		gtx.Source = r.Source()
		d, _ := k.IconBtn(gtx, "t", IcoPlus, false)
		before := len(opBytes(&ops))
		k.Tip(gtx, "t", d, "Add a card", right)
		r.Frame(&ops)
		return opBytes(&ops)[before:]
	}
	if len(frame(false)) != 0 {
		t.Fatal("tip drawn with no hover")
	}
	r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(10, 10)})
	frame(false)
	left := frame(false)
	if len(left) == 0 {
		t.Fatal("no tip on hover")
	}
	if bytes.Equal(left, frame(true)) {
		t.Fatal("right-aligned tip drawn in the same place")
	}
}

// TestSpinnerAsksFrames: a spinner keeps asking for the next frame.
func TestSpinnerAsksFrames(t *testing.T) {
	var r input.Router
	var ops op.Ops
	gtx := ctx(&ops, 100, 100)
	gtx.Source = r.Source()
	if d := Spinner(10, CAcc)(gtx); d.Size != image.Pt(20, 20) {
		t.Fatalf("size %v", d.Size)
	}
	r.Frame(&ops)
	if at, ok := r.WakeupTime(); !ok || !at.Equal(gtx.Now.Add(50*time.Millisecond)) {
		t.Fatalf("wakeup %v %v", at, ok)
	}
}

// TestEditorHint: an empty editor draws its hint; one with text doesn't.
func TestEditorHint(t *testing.T) {
	k := New()
	var e widget.Editor
	var a, b op.Ops
	k.Field(&e, "Title", 20)(ctx(&a, 300, 300))
	e.SetText("Globex")
	d := k.Field(&e, "Title", 20)(ctx(&b, 300, 300))
	if d.Size.X != 300 || d.Size.Y < 40+36 {
		t.Fatalf("field %v: full width, at least minH plus insets", d.Size)
	}
	if bytes.Equal(opBytes(&a), opBytes(&b)) {
		t.Fatal("hint and text draw alike")
	}
}

// render draws ops in a w×h headless GPU window and reads the pixels back.
func render(t *testing.T, w, h int, ops *op.Ops) *image.RGBA {
	t.Helper()
	win, err := headless.NewWindow(w, h)
	if err != nil {
		t.Skipf("no headless GPU: %v", err)
	}
	defer win.Release()
	if err := win.Frame(ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	return img
}

// near is whether pixel c is want, give or take rounding.
func near(c color.Color, want color.NRGBA) bool {
	r, g, b, a := c.RGBA()
	d := func(x uint32, y uint8) bool { return int(x>>8)-int(y) <= 2 && int(y)-int(x>>8) <= 2 }
	return d(r, want.R) && d(g, want.G) && d(b, want.B) && d(a, want.A)
}

// ink is the bounding box of the pixels that differ between a and b.
func ink(a, b *image.RGBA) image.Rectangle {
	var box image.Rectangle
	for y := a.Rect.Min.Y; y < a.Rect.Max.Y; y++ {
		for x := a.Rect.Min.X; x < a.Rect.Max.X; x++ {
			if a.RGBAAt(x, y) != b.RGBAAt(x, y) {
				box = box.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return box
}

// TestFillRRectBigCorners: a rect too big for one rounded shape still gets
// its four rounded corners and straight full edges.
func TestFillRRectBigCorners(t *testing.T) {
	var ops op.Ops
	const w, h = 3000, 64
	FillRRect(&ops, image.Rect(0, 0, w, h), 8, CWhite)
	img := render(t, w, h, &ops)
	for _, p := range []image.Point{{1, 1}, {w - 2, 1}, {1, h - 2}, {w - 2, h - 2}} {
		if a := img.RGBAAt(p.X, p.Y).A; a != 0 {
			t.Errorf("corner %v painted (alpha %d), want rounded off", p, a)
		}
	}
	for _, p := range []image.Point{{3, 3}, {w - 4, 3}, {3, h - 4}, {w - 4, h - 4}, {w / 2, 0}, {w / 2, h - 1}, {0, h / 2}, {w - 1, h / 2}, {w / 2, h / 2}} {
		if c := img.RGBAAt(p.X, p.Y); !near(c, CWhite) {
			t.Errorf("%v = %v, want filled", p, c)
		}
	}
}

// TestPanelInnerRadius: the fill sits 1dp inside the rim with a radius 1dp
// smaller, so the rim keeps its width around the corners.
func TestPanelInnerRadius(t *testing.T) {
	var a, b op.Ops
	r := image.Rect(0, 0, 50, 50)
	Panel(ctx(&a, 100, 100), r, RCtl, CPanel, CRim)
	FillRRect(&b, r, 8, CRim)
	FillRRect(&b, r.Inset(2), 6, CPanel)
	if !bytes.Equal(opBytes(&a), opBytes(&b)) {
		t.Fatal("Panel isn't the rim's 8px rect plus the fill's 6px one, 2px in")
	}
}

// TestButtonHoverTint: the hover color shows only while the pointer is on
// the button.
func TestButtonHoverTint(t *testing.T) {
	k := New()
	var r input.Router
	var ops op.Ops
	frame := func() *image.RGBA {
		ops.Reset()
		gtx := ctx(&ops, 60, 40)
		gtx.Source = r.Source()
		k.Button(gtx, "b", CPanel, CRed, RCtl, layout.Inset{}, func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(40, 20)}
		})
		r.Frame(&ops)
		return render(t, 60, 40, &ops)
	}
	if c := frame().At(20, 10); !near(c, CPanel) {
		t.Errorf("unhovered %v, want the fill", c)
	}
	r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(10, 10)})
	frame()
	if c := frame().At(20, 10); !near(c, CRed) {
		t.Errorf("hovered %v, want the hover tint", c)
	}
}

// TestTipPlace: the tip sits 4dp under its button, at its left edge or,
// right-aligned, ending at its right edge.
func TestTipPlace(t *testing.T) {
	k := New()
	var r input.Router
	var ops op.Ops
	var d layout.Dimensions
	frame := func(tip, right bool) *image.RGBA {
		ops.Reset()
		gtx := ctx(&ops, 400, 200)
		gtx.Source = r.Source()
		st := op.Offset(image.Pt(200, 0)).Push(gtx.Ops)
		d, _ = k.IconBtn(gtx, "t", IcoPlus, false)
		if tip {
			k.Tip(gtx, "t", d, "Add a card", right)
		}
		st.Pop()
		r.Frame(&ops)
		return render(t, 400, 200, &ops)
	}
	frame(false, false)
	r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(210, 10)})
	frame(false, false)
	plain := frame(false, false)
	left, right := ink(plain, frame(true, false)), ink(plain, frame(true, true))
	top := d.Size.Y + 8
	if left.Min != image.Pt(200, top) {
		t.Errorf("tip at %v, want %v", left.Min, image.Pt(200, top))
	}
	if right.Min.Y != top || right.Max.X != 200+d.Size.X || right.Dx() != left.Dx() {
		t.Errorf("right-aligned tip %v, want %d wide ending at x %d from y %d", right, left.Dx(), 200+d.Size.X, top)
	}
}

// TestSpinnerInBox: the ring, stroke included, stays inside its box.
func TestSpinnerInBox(t *testing.T) {
	var ops op.Ops
	gtx := ctx(&ops, 60, 60)
	st := op.Offset(image.Pt(20, 20)).Push(gtx.Ops)
	d := Spinner(10, CWhite)(gtx)
	st.Pop()
	img := render(t, 60, 60, &ops)
	var empty op.Ops
	box := ink(render(t, 60, 60, &empty), img)
	if box.Empty() || !box.In(image.Rect(19, 19, 21+d.Size.X, 21+d.Size.Y)) {
		t.Fatalf("ring drawn over %v, want inside %v", box, image.Rectangle{Min: image.Pt(20, 20), Max: image.Pt(20, 20).Add(d.Size)})
	}
}

// TestEditorHintOnlyEmpty: an empty editor draws its hint over itself; one
// with text draws just the editor.
func TestEditorHintOnlyEmpty(t *testing.T) {
	k := New()
	for _, text := range []string{"", "Globex"} {
		var e, plain widget.Editor
		e.SetText(text)
		plain.SetText(text)
		var a, b op.Ops
		k.Editor(ctx(&a, 300, 100), &e, "Title", 14, Sans, CText)
		plain.Layout(ctx(&b, 300, 100), k.Sh, Sans, 14, Mat(ctx(&b, 300, 100), CText), Mat(ctx(&b, 300, 100), CAccSoft))
		if hint := len(opBytes(&a)) > len(opBytes(&b)); hint != (text == "") {
			t.Errorf("text %q: hint drawn %v", text, hint)
		}
	}
}

// TestStarPointsUp: the star's top point reaches the top of its box.
func TestStarPointsUp(t *testing.T) {
	var ops, empty op.Ops
	Icon(50, CWhite, IcoStar)(ctx(&ops, 100, 100))
	box := ink(render(t, 100, 100, &empty), render(t, 100, 100, &ops))
	if box.Min.Y < 5 || box.Min.Y > 12 || box.Max.Y > 95 {
		t.Fatalf("star spans %v, want its point near y 10", box)
	}
}

// TestMonoFont: the embedded Geist Mono is loaded, so Mono is monospaced.
func TestMonoFont(t *testing.T) {
	k := New()
	var ops op.Ops
	gtx := ctx(&ops, 1000, 100)
	if i, m := k.Lbl("iiii", 14, CText, Mono)(gtx), k.Lbl("MMMM", 14, CText, Mono)(gtx); i.Size.X != m.Size.X {
		t.Fatalf("Mono iiii %d wide, MMMM %d", i.Size.X, m.Size.X)
	}
}

func TestModText(t *testing.T) {
	want := "Ctrl+K"
	if runtime.GOOS == "darwin" {
		want = "⌘K"
	}
	if got := ModText("K"); got != want {
		t.Fatalf("ModText %q", got)
	}
}

func TestMix(t *testing.T) {
	if got := Mix(RGB(0x000000), RGB(0xc8c8c8), .5); got != RGB(0x646464) {
		t.Fatalf("Mix %v", got)
	}
	if got := Mix(RGB(0x202020), RGB(0x606060), .5); got != RGB(0x404040) {
		t.Fatalf("Mix from a non-black %v", got)
	}
}
