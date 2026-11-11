package term

import (
	"github.com/lfdominguez/desk/internal/testwait"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
	"github.com/lfdominguez/desk/internal/tty"
)

// bare is a Term with no program: the test writes the screen itself, and
// what the terminal sends back (keys, mouse) arrives on out.
func bare(w, h int) (*Term, <-chan string) {
	t := &Term{emu: vt.NewEmulator(w, h)}
	out := make(chan string, 64)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := t.emu.Read(buf)
			if err != nil {
				return
			}
			out <- string(buf[:n])
		}
	}()
	return t, out
}

// next is the next thing the terminal sent, or "" after a moment.
func next(out <-chan string) string {
	select {
	case s := <-out:
		return s
	case <-time.After(testwait.Long):
		return ""
	}
}

func rowText(row []Run) string {
	var b strings.Builder
	for _, r := range row {
		b.WriteString(r.Text)
	}
	return strings.TrimRight(b.String(), " ")
}

// TestGridSynchronized: inside a synchronized update the last screen stays;
// its end shows the whole new one.
func TestGridSynchronized(t *testing.T) {
	tm, _ := bare(6, 2)
	tm.emu.WriteString("old")
	tm.Grid(0)
	tm.emu.WriteString("\x1b[?2026h\x1b[2J\x1b[Hne")
	tm.ver++
	if runs, _, _, _ := tm.Grid(0); rowText(runs[0]) != "old" {
		t.Errorf("mid update: %q", rowText(runs[0]))
	}
	tm.emu.WriteString("w\x1b[?2026l")
	tm.ver++
	if runs, _, _, _ := tm.Grid(0); rowText(runs[0]) != "new" {
		t.Errorf("after: %q", rowText(runs[0]))
	}
}

// TestGridStyles: a style change opens a new run at its column, and runs
// carry their text and width.
func TestGridStyles(t *testing.T) {
	tm, _ := bare(6, 2)
	tm.emu.WriteString("a\x1b[1mb\x1b[0mc")
	runs, _, _, _ := tm.Grid(0)
	row := runs[0]
	if len(row) != 3 {
		t.Fatalf("runs %+v", row)
	}
	want := []struct {
		x, w int
		text string
	}{{0, 1, "a"}, {1, 1, "b"}, {2, 4, "c   "}}
	for i, r := range row {
		if r.X != want[i].x || r.W != want[i].w || r.Text != want[i].text {
			t.Errorf("run %d = %d/%d %q, want %+v", i, r.X, r.W, r.Text, want[i])
		}
	}
	if row[0].Style.Equal(&row[1].Style) {
		t.Error("bold run has the plain style")
	}
}

// TestGridWide: a wide char is one cell of width 2; its trailing half adds
// nothing.
func TestGridWide(t *testing.T) {
	tm, _ := bare(5, 1)
	tm.emu.WriteString("世x")
	runs, _, _, _ := tm.Grid(0)
	if len(runs[0]) != 1 {
		t.Fatalf("runs %+v", runs[0])
	}
	r := runs[0][0]
	if r.Text != "世x  " || r.W != 5 {
		t.Fatalf("run %q width %d", r.Text, r.W)
	}
}

// TestGridEmptyCell: a cell with no content but a width is a blank, not
// a wide char's trailing half.
func TestGridEmptyCell(t *testing.T) {
	tm, _ := bare(3, 1)
	tm.emu.WriteString("abc")
	tm.emu.SetCell(1, 0, &uv.Cell{Width: 1})
	runs, _, _, _ := tm.Grid(0)
	if got := runs[0][0]; got.Text != "a c" || got.W != 3 {
		t.Fatalf("run %q width %d", got.Text, got.W)
	}
}

// TestGridScroll: scroll shows lines from the scrollback above the screen,
// and the cursor only on the live screen while it is visible.
func TestGridScroll(t *testing.T) {
	tm, _ := bare(4, 3)
	tm.emu.WriteString("1\r\n2\r\n3\r\n4\r\n5\r\n6")
	if sb := tm.Scrollback(); sb != 3 {
		t.Fatalf("scrollback %d", sb)
	}
	for _, tc := range []struct {
		scroll int
		rows   []string
		cursor bool
	}{
		{0, []string{"4", "5", "6"}, true},
		{1, []string{"3", "4", "5"}, false},
		{3, []string{"1", "2", "3"}, false},
	} {
		runs, cx, cy, cursor := tm.Grid(tc.scroll)
		for i, want := range tc.rows {
			if got := rowText(runs[i]); got != want {
				t.Errorf("scroll %d row %d = %q, want %q", tc.scroll, i, got, want)
			}
		}
		if cursor != tc.cursor || cx != 1 || cy != 2 {
			t.Errorf("scroll %d cursor %v at %d,%d", tc.scroll, cursor, cx, cy)
		}
	}
	tm.put("\x1b[?25l")
	if _, _, _, cursor := tm.Grid(0); cursor {
		t.Error("hidden cursor shown")
	}
}

// put writes s to the screen as the reader does: the screen changed.
func (t *Term) put(s string) {
	t.emu.WriteString(s)
	t.ver++
}

// TestGridKept: the same screen gives the same runs back, until output or
// a resize changes it, or another scroll is asked for.
func TestGridKept(t *testing.T) {
	tm, _ := bare(4, 2)
	tm.put("a")
	r1, _, _, _ := tm.Grid(0)
	if r2, _, _, _ := tm.Grid(0); &r2[0] != &r1[0] {
		t.Fatal("worked out again for the same screen")
	}
	if r2, _, _, _ := tm.Grid(1); &r2[0] == &r1[0] {
		t.Fatal("another scroll answered from the last")
	}
	tm.Grid(0)
	tm.put("b")
	if r2, _, _, _ := tm.Grid(0); rowText(r2[0]) != "ab" {
		t.Fatalf("after output: %q", rowText(r2[0]))
	}
	tm.pty = sleeper(t) // Resize sets the pty's size
	r3, _, _, _ := tm.Grid(0)
	tm.Resize(3, 2)
	if r4, _, _, _ := tm.Grid(0); &r4[0] == &r3[0] || len(r4[0]) == 0 || r4[0][0].W != 3 {
		t.Fatal("a resize answered from the last")
	}
}

// TestSendKeyNormalized: repeats, lock modifiers and ctrl's text don't stop
// a key from being sent.
func TestSendKeyNormalized(t *testing.T) {
	tm, out := bare(10, 2)
	for _, tc := range []struct {
		name string
		k    uv.KeyPressEvent
		want string
	}{
		{"repeat", uv.KeyPressEvent{Code: uv.KeyUp, IsRepeat: true}, "\x1b[A"},
		{"caps lock", uv.KeyPressEvent{Code: uv.KeyDown, Mod: uv.ModCapsLock | uv.ModNumLock}, "\x1b[B"},
		{"ctrl text", uv.KeyPressEvent{Code: 'c', Mod: uv.ModCtrl, Text: "c"}, "\x03"},
		{"plain text", uv.KeyPressEvent{Code: 'a', Text: "a"}, "a"},
	} {
		tm.SendKey(tc.k)
		if got := next(out); got != tc.want {
			t.Errorf("%s sent %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestWheelToApp: the wheel goes to the program only on the alt screen.
func TestWheelToApp(t *testing.T) {
	tm, out := bare(10, 4)
	tm.emu.WriteString("\x1b[?1000h\x1b[?1006h")
	ev := uv.MouseWheelEvent{X: 1, Y: 1, Button: uv.MouseWheelUp}
	if tm.WheelToApp(ev) {
		t.Fatal("wheel taken on the main screen")
	}
	tm.emu.WriteString("\x1b[?1049h")
	if !tm.WheelToApp(ev) {
		t.Fatal("wheel not taken on the alt screen")
	}
	if got := next(out); !strings.HasPrefix(got, "\x1b[<64;") {
		t.Fatalf("sent %q", got)
	}
}

// TestGridAltScreenIgnoresScroll: on the alt screen there is no scrollback
// to show, so a scroll shows the live screen with its cursor.
func TestGridAltScreenIgnoresScroll(t *testing.T) {
	tm, _ := bare(4, 2)
	tm.emu.WriteString("1\r\n2\r\n3\r\n4")
	tm.emu.WriteString("\x1b[?1049h\x1b[Halt")
	runs, _, _, cursor := tm.Grid(2)
	if got := rowText(runs[0]); got != "alt" || !cursor {
		t.Fatalf("row 0 %q cursor %v, want the live alt screen", got, cursor)
	}
}

// TestPaste: a paste is bracketed once the program asks for it, plain before.
func TestPaste(t *testing.T) {
	tm, out := bare(10, 2)
	tm.Paste("hi")
	if got := next(out); got != "hi" {
		t.Fatalf("plain paste sent %q", got)
	}
	tm.emu.WriteString("\x1b[?2004h")
	tm.Paste("hi")
	var got string
	for !strings.Contains(got, "\x1b[201~") {
		s := next(out)
		if s == "" {
			break
		}
		got += s
	}
	if got != "\x1b[200~hi\x1b[201~" {
		t.Fatalf("bracketed paste sent %q", got)
	}
}

// TestStartError: a program that can't start is an error, no Term.
func TestStartError(t *testing.T) {
	tm, err := Start(exec.Command("/nonexistent/desk-term-test"), 10, 2, func() {})
	if err == nil || tm != nil {
		t.Fatalf("Start = %v, %v", tm, err)
	}
}

// sleeper is a pty with a program that waits, hung up after the test.
func sleeper(t *testing.T) *tty.PTY {
	p, err := tty.Start(helper("sleep"), 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Hangup)
	return p
}

// TestResize: either dimension changing resizes the screen (and the pty:
// internal/tty's TestResize); the same size changes nothing.
func TestResize(t *testing.T) {
	tm := &Term{emu: vt.NewEmulator(10, 4), pty: sleeper(t)}
	for _, tc := range []struct {
		w, h, ver int
	}{
		{10, 4, 0}, // same size: nothing
		{10, 6, 1},
		{12, 6, 2},
	} {
		tm.Resize(tc.w, tc.h)
		if tm.emu.Width() != tc.w || tm.emu.Height() != tc.h || tm.ver != tc.ver {
			t.Errorf("Resize(%d, %d): screen %dx%d, version %d", tc.w, tc.h, tm.emu.Width(), tm.emu.Height(), tm.ver)
		}
	}
}

// TestStartEnv: a command's own Env is kept (not replaced by desk's), a nil
// one inherits desk's, and TERM is set either way.
func TestStartEnv(t *testing.T) {
	t.Setenv("DESK_TERM_TEST", "globex")
	for _, tc := range []struct {
		env  []string
		want string
	}{
		{[]string{"FOO=acme"}, "acme::xterm-256color"},
		{nil, ":globex:xterm-256color"},
	} {
		cmd := helper("env")
		if cmd.Env = tc.env; tc.env != nil {
			// GOCOVERDIR: under -cover it would warn on the screen without it.
			cmd.Env = append(tc.env, "DESK_TERM_HELPER=env", "GOCOVERDIR="+os.Getenv("GOCOVERDIR"))
		} else {
			t.Setenv("DESK_TERM_HELPER", "env") // desk's, inherited
		}
		tm, err := Start(cmd, 40, 2, func() {})
		if err != nil {
			t.Fatal(err)
		}
		for end := time.Now().Add(testwait.Long); ; time.Sleep(10 * time.Millisecond) {
			if done, _ := tm.Exited(); done {
				break
			}
			if time.Now().After(end) {
				t.Fatal("exit not seen")
			}
		}
		runs, _, _, _ := tm.Grid(0)
		if got := rowText(runs[0]); got != tc.want {
			t.Errorf("env %v: %q, want %q", tc.env, got, tc.want)
		}
		tm.Close()
	}
}
