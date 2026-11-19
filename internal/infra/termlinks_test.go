package infra

import (
	"fmt"
	"image"
	"io"
	"strings"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/transfer"
	"gioui.org/layout"
)

func TestURLAt(t *testing.T) {
	for _, c := range []struct {
		line   string
		x      int
		lo, hi int
		url    string
	}{
		{"see https://example.com/a?b=1 now", 6, 4, 29, "https://example.com/a?b=1"},
		{"see https://example.com/a?b=1 now", 28, 4, 29, "https://example.com/a?b=1"},
		{"see https://example.com/a?b=1 now", 29, 0, 0, ""}, // the space after it
		{"see https://example.com/a?b=1 now", 2, 0, 0, ""},
		{"(at https://example.com/x).", 10, 4, 25, "https://example.com/x"},
		{"https://en.example.com/wiki/Go_(lang)", 0, 0, 37, "https://en.example.com/wiki/Go_(lang)"},
		{"[docs: https://example.com/d]", 9, 7, 28, "https://example.com/d"},
		{"«ñ» http://example.com/é, ok", 6, 4, 24, "http://example.com/é"},
		{`url="ftp://192.0.2.7/f"`, 7, 5, 22, "ftp://192.0.2.7/f"},
		{"a https://x.example.com b https://y.example.com", 30, 26, 47, "https://y.example.com"},
		{"no links here", 3, 0, 0, ""},
	} {
		lo, hi, url := urlAt([]rune(c.line), c.x)
		if lo != c.lo || hi != c.hi || url != c.url {
			t.Errorf("%q at %d: %d %d %q, want %d %d %q", c.line, c.x, lo, hi, url, c.lo, c.hi, c.url)
		}
	}
	if trimURL("https://example.com/!?.,;:") != "https://example.com/" || trimURL(".") != "" {
		t.Error("trailing punctuation")
	}
}

func TestPageKey(t *testing.T) {
	for _, c := range []struct {
		e    key.Event
		want int
	}{
		{key.Event{Name: key.NamePageUp, Modifiers: key.ModShift, State: key.Press}, 1},
		{key.Event{Name: key.NamePageDown, Modifiers: key.ModShift, State: key.Press}, -1},
		{key.Event{Name: key.NamePageUp, State: key.Press}, 0},
		{key.Event{Name: key.NamePageUp, Modifiers: key.ModShift | key.ModCtrl, State: key.Press}, 0},
		{key.Event{Name: key.NamePageUp, Modifiers: key.ModShift, State: key.Release}, 0},
		{key.Event{Name: key.NameUpArrow, Modifiers: key.ModShift, State: key.Press}, 0},
	} {
		if got := pageKey(c.e); got != c.want {
			t.Errorf("%v %v %v: %d, want %d", c.e.Name, c.e.Modifiers, c.e.State, got, c.want)
		}
	}
}

// TestLinkWrapped: a URL the terminal wrapped over rows is found from any
// of them, whole, and underlined row by row; a row that ends with a newline
// ends it.
func TestLinkWrapped(t *testing.T) {
	v := &termView{cols: 10}
	for _, s := range []string{"go https:/", "/example.c", "om/a. done", "https://b"} {
		v.runs = append(v.runs, []Run{{Text: s}})
	}
	v.meta = []Row{{Wrap: true}, {Wrap: true}, {}, {}}
	for _, at := range [][2]int{{5, 0}, {0, 1}, {1, 2}} {
		l := v.linkAt(at[0], at[1])
		if l.url != "https://example.com/a" || l.row != 0 || l.lo != 3 || l.hi != 24 {
			t.Errorf("at %v: %+v", at, l)
		}
		if got := l.rows(v.cols); fmt.Sprint(got) != "[[0 3 10] [1 0 10] [2 0 4]]" {
			t.Errorf("rows %v", got)
		}
	}
	for _, at := range [][2]int{{1, 0}, {7, 2}, {0, 9}, {0, -1}} {
		if l := v.linkAt(at[0], at[1]); l.url != "" {
			t.Errorf("at %v: %+v", at, l)
		}
	}
	if l := v.linkAt(2, 3); l.url != "https://b" || l.row != 3 {
		t.Errorf("an unwrapped row: %+v", l)
	}
	v.runs[3] = []Run{{Text: "see"}, {X: 5, Text: "https://c"}} // a run after blank cells
	if l := v.linkAt(6, 3); l.url != "https://c" || l.lo != 5 {
		t.Errorf("after blanks: %+v", l)
	}
	v.runs = [][]Run{{{Text: "界 https:/"}}, {{Text: "/x.org a"}}} // a wide character takes two cells
	v.meta = []Row{{Wrap: true}, {}}
	if l := v.linkAt(1, 1); l.url != "https://x.org" || l.lo != 3 || l.hi != 16 {
		t.Errorf("after a wide character: %+v", l)
	}
	v.runs[0] = []Run{{Text: "go"}} // a wrapped row's blanks count as cells: the line's next starts at its end
	if l := v.linkAt(0, 1); l.url != "" || (linkSpan{hi: 5}).rows(0) != nil {
		t.Errorf("blanks: %+v", l)
	}
}

// TestPasteBehindFetch: no paste reaches a pane whose password box is up (its
// prompt waits for the password); once the box is gone, it does.
func TestPasteBehindFetch(t *testing.T) {
	_, ts, ss := gmTermState(t)
	ts[0].fetchStart(nil)
	pasteTo(ts[:1], "rm -rf /tmp/x\n")
	ts[0].fetchEnd(nil) // went well: the box is gone
	pasteTo(ts[:1], "ok")
	ss[0].wait(t, "ok")
	if strings.Contains(ss[0].String(), "rm -rf") {
		t.Errorf("pasted behind the box: %q", ss[0].String())
	}
}

// TestGMTermConsoleKeys: what Konsole and Kitty do with the keys and the
// mouse: Shift+PgUp/PgDn page through the scrollback (not in a full-screen
// program), Shift+Insert pastes and Ctrl+Insert copies, a middle click
// pastes, and Ctrl over a URL underlines it, a Ctrl+click opens it.
func TestGMTermConsoleKeys(t *testing.T) {
	h := newGM(t)
	u := h.u
	h.size = image.Pt(1000, 700)
	st, ts, ss := gmTermState(t)
	h.set(st)
	u.org = "Acme"
	for i := range 60 {
		gmOut(ts[0], fmt.Sprintf("line %d\r\n", i))
	}
	gmOut(ts[0], "docs at https://example.com/runbook.\r\n$ ")
	var opened []string
	old := openLocal
	openLocal = func(p string) error { opened = append(opened, p); return nil }
	t.Cleanup(func() { openLocal = old })
	w := func(gtx layout.Context) { u.terminals(gtx) }
	h.draw(w)
	h.draw(w)
	v := u.terms[ts[0]]

	h.key(key.NamePageUp, key.ModShift)
	h.draw(w)
	if page := max(1, v.rows-1); v.scroll != page {
		t.Fatalf("Shift+PgUp: scroll %d, want a page (%d)", v.scroll, page)
	}
	h.key(key.NamePageUp, key.ModShift)
	h.key(key.NamePageDown, key.ModShift)
	h.draw(w)
	if v.scroll != max(1, v.rows-1) {
		t.Errorf("up, up, down: %d", v.scroll)
	}
	for range 50 {
		h.key(key.NamePageUp, key.ModShift)
	}
	h.draw(w)
	if v.scroll != ts[0].Scrollback() {
		t.Errorf("stops at the top: %d of %d", v.scroll, ts[0].Scrollback())
	}
	for range 50 {
		h.key(key.NamePageDown, key.ModShift)
	}
	h.draw(w)
	if v.scroll != 0 {
		t.Errorf("back to the live screen: %d", v.scroll)
	}
	if ss[0].String() != "" {
		t.Errorf("Shift+PgUp/PgDn aren't sent to the shell: %q", ss[0].String())
	}

	// Insert alone goes to the program
	h.key(key.NameInsert, 0)
	h.draw(w)
	ss[0].wait(t, "\x1b[2~")
	ss[0].reset()

	// Ctrl+Insert copies, Shift+Insert pastes
	v.sel = [2]image.Point{{0, 0}, {4, 0}}
	h.key(key.NameInsert, key.ModCtrl)
	h.draw(w)
	if _, b, ok := h.r.WriteClipboard(); !ok || string(b) != "line" {
		t.Errorf("Ctrl+Insert copied %q %v", b, ok)
	}
	h.key(key.NameInsert, key.ModShift)
	h.draw(w)
	if u.pasteWanted {
		t.Error("Shift+Insert asks for the clipboard")
	}
	h.r.Queue(transfer.DataEvent{Type: "application/text", Open: func() io.ReadCloser { return io.NopCloser(strings.NewReader("pasted")) }})
	h.draw(w)
	ss[0].wait(t, "pasted")
	ss[0].reset()

	// the row with the URL, and a point on it
	row := -1
	for k := range v.runs {
		if strings.Contains(string(v.line(k)), "https://") {
			row = k
		}
	}
	if row < 0 {
		t.Fatal("the URL's row isn't drawn")
	}
	if !strings.Contains(string(v.line(row)), "https://example.com/runbook") {
		t.Fatalf("row %q", string(v.line(row)))
	}
	// Ctrl over the URL: underlined (found by sweeping the row with Ctrl held)
	y := float32(v.rowY[row] + 4)
	inX := float32(-1)
	for x := float32(0); x < 1000 && inX < 0; x += 4 {
		h.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(x, y), Modifiers: key.ModCtrl})
		h.draw(w)
		if v.link.url != "" {
			inX = x + 12 // well inside it
		}
	}
	if inX < 0 {
		t.Fatal("Ctrl over the row never found the URL")
	}

	// a middle click pastes the primary selection, and selects nothing; what
	// a drag selects becomes the primary selection
	var owned string
	setPrimary, readPrimary = func(s string) { owned = s }, func() string { return "middle" }
	t.Cleanup(func() { setPrimary, readPrimary = func(string) {}, func() string { return "" } })
	v.sel = [2]image.Point{}
	h.r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonTertiary, Position: f32.Pt(inX, y)})
	h.r.Queue(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: f32.Pt(inX, y)})
	h.draw(w)
	ss[0].wait(t, "middle")
	if v.sel[0] != v.sel[1] {
		t.Error("a middle click selected")
	}
	h.r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: f32.Pt(inX, y)},
		pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: f32.Pt(inX+40, y)}, // a drag
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: f32.Pt(inX+40, y)})
	h.draw(w)
	if owned == "" || !strings.Contains("https://example.com/runbook", owned) {
		t.Errorf("a drag's text isn't the primary selection: %q", owned)
	}

	// Ctrl over the URL: underlined; without Ctrl: not
	h.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(inX, y), Modifiers: key.ModCtrl})
	h.draw(w)
	if v.link.url != "https://example.com/runbook" || v.link.row != row {
		t.Fatalf("hover with Ctrl: %+v", v.link)
	}
	h.move(inX, y)
	h.draw(w)
	if v.link.url != "" {
		t.Error("no Ctrl, no link")
	}
	h.r.Queue(key.Event{Name: key.NameCtrl, State: key.Press}) // Ctrl pressed where the pointer rests: underlined, released: not
	h.draw(w)
	if v.link.url != "https://example.com/runbook" {
		t.Errorf("Ctrl pressed over it: %+v", v.link)
	}
	h.r.Queue(key.Event{Name: key.NameCtrl, State: key.Release})
	h.draw(w)
	if v.link.url != "" {
		t.Errorf("Ctrl released: %+v", v.link)
	}
	// a plain click on it selects, as before
	h.press(inX, y, 0)
	h.release(inX, y)
	h.draw(w)
	if len(opened) != 0 {
		t.Error("a click without Ctrl opens nothing")
	}
	// Ctrl+click opens it
	h.press(inX, y, key.ModCtrl)
	h.release(inX, y)
	h.draw(w)
	if len(opened) != 1 || opened[0] != "https://example.com/runbook" {
		t.Errorf("opened %q", opened)
	}
	// Ctrl+click off a URL selects as a click does
	h.press(inX, float32(v.rowY[0]+4), key.ModCtrl)
	h.release(inX, float32(v.rowY[0]+4))
	h.draw(w)
	if len(opened) != 1 {
		t.Error("no URL there")
	}
	// a URL the terminal wrapped opens whole from its second row
	long := "https://example.com/" + strings.Repeat("a", 2*v.cols)
	gmOut(ts[0], "\r\n"+long+"\r\n$ ")
	h.draw(w)
	row = -1
	for k := range v.runs {
		if strings.HasPrefix(string(v.line(k)), "aaaa") && k > 0 && v.meta[k-1].Wrap {
			row = k
			break
		}
	}
	if row < 0 {
		t.Fatal("no wrapped row of the long URL")
	}
	h.press(inX, float32(v.rowY[row]+4), key.ModCtrl)
	h.release(inX, float32(v.rowY[row]+4))
	h.draw(w)
	if len(opened) != 2 || opened[1] != long {
		t.Errorf("wrapped: opened %q", opened)
	}

	// a full-screen program keeps Shift+PgUp
	gmOut(ts[0], "\x1b[?1049h")
	h.draw(w)
	ss[0].reset()
	h.key(key.NamePageUp, key.ModShift)
	h.draw(w)
	ss[0].wait(t, "\x1b[5;2~")
	if v.scroll != 0 {
		t.Error("no scrollback paging in a full-screen program")
	}
}
