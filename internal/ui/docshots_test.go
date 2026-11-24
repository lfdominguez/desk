package ui

// The documentation's screenshots: desk's own window, drawn on a headless
// GPU (Mesa's llvmpipe will do) over the `desk demo` world, scene by scene,
// with zoomed cuts of where each action is. The docs workflow runs it before
// building the site; by hand:
//
//	DESK_DOCSHOTS=site/src/assets/shots go test ./internal/ui -run TestDocShots -count=1
//	DESK_DOCSHOTS=… go test ./internal/ui -run 'TestDocShots/board' -count=1   # one scene
//
// Without DESK_DOCSHOTS it's skipped. Scenes live in docshots_*_test.go,
// each file adding its own in init(); every scene starts from a fresh demo
// world. Everything here is in px of the 1600×950 dp window at shotScale.

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/lfdominguez/desk/internal/kanban"
)

const shotScale = 1.5 // px per dp: crisp on HiDPI screens, and cuts stay sharp

var shotSize = image.Pt(1600, 950) // dp, desk's default window

// scene is one or more shots, made by run from a fresh demo world.
type scene struct {
	name string
	run  func(*shooter)
}

var scenes []scene

// cut is a part of a shot saved on its own as <shot>-<name>.png, r in px;
// ring outlines it on the full shot too. A cut without a name is only the
// ring: what the full shot points at, with no page using it on its own.
type cut struct {
	name string
	r    image.Rectangle
	ring bool
}

type shooter struct {
	t   *testing.T
	dir string
	u   *ui
	r   input.Router
	ops op.Ops
	win *headless.Window
	now time.Time
}

func TestDocShots(t *testing.T) {
	dir := os.Getenv("DESK_DOCSHOTS")
	if dir == "" {
		t.Skip("DESK_DOCSHOTS (the folder for the docs' screenshots) not set")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join("..", "..", dir) // from the repo's root, not this package's folder
	}
	dir, _ = filepath.Abs(dir)
	os.MkdirAll(dir, 0o755)
	px := scaled(shotSize)
	win, err := headless.NewWindow(px.X, px.Y)
	if err != nil {
		t.Skipf("no headless GPU: %v", err)
	}
	defer win.Release()
	slices.SortFunc(scenes, func(a, b scene) int { return strings.Compare(a.name, b.name) })
	for _, sc := range scenes {
		t.Run(sc.name, func(t *testing.T) {
			s := newShooter(t, dir, win)
			defer s.close()
			sc.run(s)
		})
	}
}

// newShooter makes a fresh demo world (its own temp folder, env and fakes)
// and opens desk on its board.
func newShooter(t *testing.T, dir string, win *headless.Window) *shooter {
	for _, v := range []string{"XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "DESK_ROOT", "HOME", "DESK_DEMO_TOKEN", "WAYLAND_DISPLAY"} {
		t.Setenv(v, os.Getenv(v)) // restored after the scene
	}
	world, err := Demo()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(world) })
	b, err := kanban.Load(kanban.Path())
	if err != nil {
		t.Fatal(err)
	}
	s := &shooter{t: t, dir: dir, u: newUI(b, kanban.Path()), win: win}
	s.settle()
	return s
}

func (s *shooter) close() {
	for _, p := range s.u.procs {
		p.Close()
	}
	for _, tt := range s.u.terms {
		tt.t.Close()
	}
	if s.u.infra != nil {
		s.u.infra.Close()
	}
	demo = false
}

// infra starts the servers workspace (Globex's scripted servers and infra
// Claude), as desk's Run does.
func (s *shooter) infra() { s.u.startInfra() }

func scaled(p image.Point) image.Point {
	return image.Pt(int(float64(p.X)*shotScale), int(float64(p.Y)*shotScale))
}

// dp is a rectangle given in dp, in px.
func dp(x0, y0, x1, y1 int) image.Rectangle {
	return image.Rectangle{Min: scaled(image.Pt(x0, y0)), Max: scaled(image.Pt(x1, y1))}
}

// frame lays desk out once and runs its input.
func (s *shooter) frame() {
	s.ops.Reset()
	s.now = time.Now()
	gtx := layout.Context{Ops: &s.ops, Source: s.r.Source(), Now: s.now,
		Metric: unit.Metric{PxPerDp: shotScale, PxPerSp: shotScale}, Constraints: layout.Exact(scaled(shotSize))}
	s.u.frame(gtx)
	s.r.Frame(&s.ops)
}

// settle runs frames until desk stops asking for more and its background
// work has landed, at most 1.5 s (a blinking caret always asks for more).
func (s *shooter) settle() {
	quiet := 0
	for end := time.Now().Add(1500 * time.Millisecond); time.Now().Before(end) && quiet < 3; {
		s.frame()
		if _, again := s.r.WakeupTime(); again || len(s.u.events) > 0 {
			quiet = 0
		} else {
			quiet++
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// wait runs frames until ok, failing the scene after timeout.
func (s *shooter) wait(what string, timeout time.Duration, ok func() bool) {
	s.t.Helper()
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		s.frame()
		if ok() {
			s.settle()
			return
		}
	}
	s.t.Fatalf("waited %v for %s", timeout, what)
}

// send queues events and settles.
func (s *shooter) send(evs ...any) {
	for _, e := range evs {
		s.r.Queue(e.(interface{ ImplementsEvent() }))
	}
	s.settle()
}

// key presses name with mods (key.ModShortcut is Ctrl).
func (s *shooter) key(name key.Name, mods key.Modifiers) {
	s.send(key.Event{Name: name, Modifiers: mods, State: key.Press})
}

// typ types text into the field that has the keyboard.
func (s *shooter) typ(text string) { s.send(key.EditEvent{Text: text}) }

// find is where button id (a kit.Click id) is on screen: the clickable area
// the pointer hovers it in. It leaves the pointer there.
func (s *shooter) find(id string) (image.Rectangle, bool) {
	c := s.u.k.Click(id)
	s.frame()
	var areas []image.Rectangle
	for _, n := range s.r.AppendSemantics(nil) {
		if n.Desc.Gestures&input.ClickGesture != 0 {
			areas = append(areas, n.Desc.Bounds)
		}
	}
	slices.SortStableFunc(areas, func(a, b image.Rectangle) int { return b.Min.Y - a.Min.Y }) // dialogs' buttons are low
	for _, a := range areas {
		s.move(center(a))
		if c.Hovered() {
			return a, true
		}
	}
	return image.Rectangle{}, false
}

// at is find that fails the scene when id isn't on screen.
func (s *shooter) at(id string) image.Rectangle {
	s.t.Helper()
	r, ok := s.find(id)
	if !ok {
		s.t.Fatalf("button %q is not on screen", id)
	}
	return r
}

func center(r image.Rectangle) f32.Point {
	c := r.Min.Add(r.Max).Div(2)
	return f32.Pt(float32(c.X), float32(c.Y))
}

// move puts the pointer at p (px): hover states show in the next shot.
func (s *shooter) move(p f32.Point) {
	s.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: p})
	s.frame()
}

// click clicks button id; hover leaves the pointer over it after.
func (s *shooter) click(id string) { s.clickAt(center(s.at(id)), pointer.ButtonPrimary) }

// rightClick right-clicks button id (cards open their menu so).
func (s *shooter) rightClick(id string) { s.clickAt(center(s.at(id)), pointer.ButtonSecondary) }

// hover moves the pointer over button id and settles.
func (s *shooter) hover(id string) { s.at(id); s.settle() }

func (s *shooter) clickAt(p f32.Point, b pointer.Buttons) {
	s.send(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: p},
		pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: b, Position: p},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
}

// around is r grown by pad dp on every side, kept inside the window.
func around(r image.Rectangle, pad int) image.Rectangle {
	p := int(float64(pad) * shotScale)
	return r.Inset(-p).Intersect(image.Rectangle{Max: scaled(shotSize)})
}

// modal is a kit.Modal dialog width dp wide, 90 dp down, to its footer's
// button foot (a Click id), with a little of the scrim around it.
func (s *shooter) modal(width int, foot string) image.Rectangle {
	s.t.Helper()
	f := s.at(foot)
	x0 := int(float64(shotSize.X-width)/2*shotScale) - int(8*shotScale)
	return image.Rect(x0, int(82*shotScale), x0+int(float64(width+16)*shotScale), f.Max.Y+int(26*shotScale))
}

// union is the smallest rectangle holding all of rs.
func union(rs ...image.Rectangle) image.Rectangle {
	u := rs[0]
	for _, r := range rs[1:] {
		u = u.Union(r)
	}
	return u
}

// shot draws the current frame to <name>.png and each cut to <name>-<cut>.png.
func (s *shooter) shot(name string, cuts ...cut) {
	s.t.Helper()
	s.frame()
	if err := s.win.Frame(&s.ops); err != nil {
		s.t.Fatal(err)
	}
	img := image.NewRGBA(image.Rectangle{Max: s.win.Size()})
	if err := s.win.Screenshot(img); err != nil {
		s.t.Fatal(err)
	}
	full := image.NewRGBA(img.Bounds())
	draw.Draw(full, full.Bounds(), img, image.Point{}, draw.Src)
	for _, c := range cuts {
		r := c.r.Intersect(img.Bounds())
		if r.Empty() {
			s.t.Fatalf("cut %s-%s is off the window: %v", name, c.name, c.r)
		}
		if c.name != "" {
			part := image.NewRGBA(image.Rectangle{Max: r.Size()})
			draw.Draw(part, part.Bounds(), img, r.Min, draw.Src)
			s.save(name+"-"+c.name, part)
		}
		if c.ring {
			ring(full, r, 4)
		}
	}
	s.save(name, full)
}

// ring outlines r in desk's accent, w px thick, just outside it.
func ring(img *image.RGBA, r image.Rectangle, w int) {
	r = r.Inset(-w)
	acc := image.NewUniform(color.NRGBA{0x0a, 0x84, 0xff, 0xff})
	for _, e := range []image.Rectangle{
		{r.Min, image.Pt(r.Max.X, r.Min.Y+w)}, {image.Pt(r.Min.X, r.Max.Y-w), r.Max},
		{r.Min, image.Pt(r.Min.X+w, r.Max.Y)}, {image.Pt(r.Max.X-w, r.Min.Y), r.Max},
	} {
		draw.Draw(img, e.Intersect(img.Bounds()), acc, image.Point{}, draw.Over)
	}
}

// shotsSaved is every PNG written this run: a cut named like another shot
// (<shot>-<cut>) would silently replace it.
var shotsSaved = map[string]string{}

func (s *shooter) save(name string, img image.Image) {
	s.t.Helper()
	if by, ok := shotsSaved[name]; ok {
		s.t.Fatalf("%s.png is written twice (first by %s)", name, by)
	}
	shotsSaved[name] = s.t.Name()
	f, err := os.Create(filepath.Join(s.dir, name+".png"))
	if err != nil {
		s.t.Fatal(err)
	}
	defer f.Close()
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(f, img); err != nil {
		s.t.Fatal(err)
	}
}
