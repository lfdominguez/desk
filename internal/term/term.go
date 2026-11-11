// Package term runs a program in a pseudo-terminal behind a VT emulator
// (charmbracelet/x/vt), for the window to draw: what a card's Claude Code
// TUI runs in. A slimmer internal/infra pane, on the patched vt/ultraviolet
// (third_party: scroll and scrollback speed); no GUI imports.
package term

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
	"github.com/lfdominguez/desk/internal/tty"
)

// Term is a program in a pseudo-terminal and its screen. Safe for use
// from the UI and the reader goroutine.
type Term struct {
	mu     sync.Mutex
	emu    *vt.Emulator
	pty    *tty.PTY
	exited bool
	code   int
	ver    int  // bumps whenever the screen may have changed
	last   grid // what Grid last said, while ver and scroll stay
}

// grid is a Grid answer and what it was worked out for.
type grid struct {
	ver, scroll int
	ok          bool
	runs        [][]Run
	cx, cy      int
	cursor      bool
}

// Start runs cmd in a w×h terminal (its Env, else desk's, with TERM set);
// notify is called (from another goroutine) whenever the screen changes and
// once when cmd exits.
func Start(cmd *exec.Cmd, w, h int, notify func()) (*Term, error) {
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, "TERM=xterm-256color", "COLORTERM=truecolor")
	f, err := tty.Start(cmd, w, h)
	if err != nil {
		return nil, err
	}
	t := &Term{emu: vt.NewEmulator(w, h), pty: f}
	go io.Copy(f, t.emu) // keys and the terminal's replies -> the program
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := f.Read(buf)
			if err != nil {
				code := f.Wait()
				t.mu.Lock()
				t.exited, t.code = true, code
				t.mu.Unlock()
				notify()
				return
			}
			t.mu.Lock()
			t.emu.Write(buf[:n])
			t.ver++
			t.mu.Unlock()
			notify()
		}
	}()
	return t, nil
}

// Run is cells of one style on a row.
type Run struct {
	X, W  int // first column and width, in cells
	Text  string
	Style uv.Style
}

// Grid is the screen, scroll lines up into the scrollback, as styled runs
// per row, and the cursor (shown only on the live screen). The window asks
// every frame: the same answer is given again until the screen changes,
// so callers mustn't change the runs.
func (t *Term) Grid(scroll int) (runs [][]Run, cx, cy int, cursor bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// mid synchronized update (Claude Code's redraws) the last screen stays,
	// so a half-drawn one never flickers through
	if l := t.last; l.ok && l.scroll == scroll && (l.ver == t.ver || t.emu.Synchronized()) {
		return l.runs, l.cx, l.cy, l.cursor
	}
	runs, cx, cy, cursor = t.grid(scroll)
	t.last = grid{t.ver, scroll, true, runs, cx, cy, cursor}
	return runs, cx, cy, cursor
}

func (t *Term) grid(scroll int) (runs [][]Run, cx, cy int, cursor bool) {
	sb, h, w := t.emu.ScrollbackLen(), t.emu.Height(), t.emu.Width()
	if t.emu.IsAltScreen() {
		scroll = 0
	}
	top := max(0, sb-scroll)
	runs = make([][]Run, h)
	for i := range h {
		abs := top + i
		cur := -1 // the open run in runs[i]
		var text strings.Builder
		for x := 0; x < w; x++ {
			c := t.cell(x, abs, sb)
			content, st, cw := " ", uv.Style{}, 1
			if c != nil {
				if c.Content == "" && c.Width == 0 {
					continue // the trailing half of a wide char
				}
				if c.Content != "" {
					content, cw = c.Content, max(1, c.Width)
				}
				st = c.Style
			}
			if cur < 0 || !st.Equal(&runs[i][cur].Style) {
				if cur >= 0 {
					runs[i][cur].Text = text.String()
					text.Reset()
				}
				runs[i] = append(runs[i], Run{X: x, Style: st})
				cur = len(runs[i]) - 1
			}
			text.WriteString(content)
			runs[i][cur].W += cw
		}
		if cur >= 0 {
			runs[i][cur].Text = text.String()
		}
	}
	p := t.emu.CursorPosition()
	return runs, p.X, p.Y, scroll == 0 && !t.emu.CursorHidden()
}

func (t *Term) cell(x, abs, sb int) *uv.Cell {
	if abs < sb {
		return t.emu.ScrollbackCellAt(x, abs)
	}
	if x >= t.emu.Width() {
		return nil
	}
	return t.emu.CellAt(x, abs-sb)
}

// SendKey types k. vt matches keys by struct equality, so lock modifiers,
// the repeat flag and the text attached to ctrl+letter are dropped first:
// with them ↑ ↓ ^C send nothing.
func (t *Term) SendKey(k uv.KeyPressEvent) {
	k.Mod &^= uv.ModCapsLock | uv.ModNumLock | uv.ModScrollLock
	k.IsRepeat = false
	if k.Mod&uv.ModCtrl != 0 {
		k.Text = ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.emu.SendKey(k)
}

// Paste sends s as a paste (bracketed when the program asked for it).
func (t *Term) Paste(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.emu.Paste(s)
}

// WheelToApp hands a wheel event to a full-screen program when it owns
// the terminal; false means the caller scrolls the scrollback instead.
func (t *Term) WheelToApp(ev uv.MouseWheelEvent) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.emu.IsAltScreen() {
		return false
	}
	t.emu.SendMouse(ev)
	return true
}

// Scrollback is how many lines sit above the live screen.
func (t *Term) Scrollback() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.emu.ScrollbackLen()
}

func (t *Term) Resize(w, h int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if w == t.emu.Width() && h == t.emu.Height() {
		return
	}
	t.emu.Resize(w, h)
	t.ver++
	t.pty.Resize(w, h)
}

// Exited reports whether the program ended, and its exit code.
func (t *Term) Exited() (bool, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exited, t.code
}

// Close hangs the program up (tty's Hangup).
func (t *Term) Close() { t.pty.Hangup() }
