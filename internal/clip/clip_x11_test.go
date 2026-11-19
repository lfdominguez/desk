//go:build linux

package clip

import (
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lfdominguez/desk/internal/testwait"
)

// fakeX is an X server for one client: its clipboard offers types and holds
// data (in INCR chunks of 3 bytes with mode "incr"). A cookie, when set, is
// required. cut closes the connection in place of handling that request
// (counted from 0; -1 never). Other modes misbehave: "no-owner",
// "refuse-image", "x-error", "huge", "short-reply", "incr-hang", and the
// setup's "refuse-setup", "cut-head", "cut-more", "bad-setup", "bad-screen".
// A client that takes PRIMARY is asked for TARGETS, UTF8_STRING, STRING, TEXT
// (as an obsolete client: no property) and an image it can't give; what it
// puts in properties and tells comes on got, then it loses the selection.
type fakeX struct {
	types        []string
	data, cookie string
	mode         string
	cut          int
	got          chan string
}

// serve answers l's first connection as f.
func (f fakeX) serve(t *testing.T, l net.Listener) {
	t.Cleanup(func() { l.Close() })
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		f.run(c)
	}()
}

// listenX serves f on the local display :7 and points DISPLAY at it.
func listenX(t *testing.T, f fakeX) {
	dir := t.TempDir()
	old := xUnixDir
	xUnixDir = dir
	t.Cleanup(func() { xUnixDir = old })
	l, err := net.Listen("unix", filepath.Join(dir, "X7"))
	if err != nil {
		t.Fatal(err)
	}
	f.serve(t, l)
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", ":7.0")
	t.Setenv("XAUTHORITY", filepath.Join(dir, "none"))
}

func (f fakeX) run(c net.Conn) {
	head := make([]byte, 12)
	if _, err := io.ReadFull(c, head); err != nil {
		return
	}
	nl, dl := int(xle.Uint16(head[6:])), int(xle.Uint16(head[8:]))
	pad := func(n int) int { return (n + 3) &^ 3 }
	auth := make([]byte, pad(nl)+pad(dl))
	io.ReadFull(c, auth)
	if f.cookie != "" && string(auth[pad(nl):][:dl]) != f.cookie || f.mode == "refuse-setup" {
		why := xpad([]byte("No protocol specified\n"))
		c.Write(append([]byte{0, 22, 11, 0, 0, 0, byte(len(why) / 4), 0}, why...))
		return
	}
	more := make([]byte, 32)
	xle.PutUint32(more[4:], 0x400000) // our ids' base
	xle.PutUint16(more[16:], 4)       // vendor
	more[21] = 1                      // pixmap formats
	more = append(more, "fake"...)
	more = append(more, make([]byte, 8)...)
	more = append(more, words(0x100)...) // the root
	switch f.mode {
	case "cut-head":
		c.Write([]byte{1, 0})
		return
	case "cut-more":
		c.Write([]byte{1, 0, 11, 0, 0, 0, 20, 0})
		return
	case "bad-setup":
		more = more[:8]
	case "bad-screen":
		more = more[:40]
	}
	c.Write(append([]byte{1, 0, 11, 0, 0, 0, byte(len(more) / 4), 0}, more...))

	atoms, names := map[string]uint32{}, map[uint32]string{xAtom: "ATOM"} // a predefined one
	atom := func(n string) uint32 {
		if a, ok := atoms[n]; ok {
			return a
		}
		a := uint32(100 + len(atoms))
		atoms[n], names[a] = a, n
		return a
	}
	ev := func(code byte, w ...uint32) {
		m := append([]byte{code, 0, 0, 0}, words(w...)...)
		c.Write(append(m, make([]byte, 32-len(m))...))
	}
	reply := func(b1 byte, head, extra []byte) {
		extra = xpad(extra)
		m := xle.AppendUint32([]byte{1, b1, 0, 0}, uint32(len(extra)/4))
		m = append(append(m, head...), make([]byte, 24-len(head))...)
		c.Write(append(m, extra...))
	}
	type prop struct {
		typ    uint32
		format byte
		val    []byte
	}
	props := map[uint32]prop{}
	var chunks []string
	incr, ended := false, false
	answered := 0
	for n := 0; n != f.cut; n++ {
		h := make([]byte, 4)
		if _, err := io.ReadFull(c, h); err != nil {
			return
		}
		body := make([]byte, int(xle.Uint16(h[2:]))*4-4)
		io.ReadFull(c, body)
		w := func(i int) uint32 { return xle.Uint32(body[4*i:]) }
		switch h[0] {
		case xSetSelectionOwner:
			if f.mode == "cut-tail" { // a reply, and gone before its tail
				c.Write(append(xle.AppendUint32([]byte{1, 0, 0, 0}, 1), make([]byte, 24)...))
				return
			}
			c.Write(append(xle.AppendUint32([]byte{1, 0, 0, 0}, 1), make([]byte, 28)...)) // a stray reply, its tail too
			for _, tg := range []string{"TARGETS", "UTF8_STRING", "STRING", "TEXT", "image/png"} {
				p := atom("P")
				if tg == "TEXT" {
					p = 0
				}
				ev(xSelectionRequest, 0, 0, 0x200, w(1), atom(tg), p)
			}
		case xChangeProperty:
			var v string
			if body[12] == 32 {
				for i := 20; i+4 <= len(body); i += 4 {
					v += names[xle.Uint32(body[i:])] + " "
				}
			} else {
				v = string(body[20:][:w(4)])
			}
			f.got <- names[w(1)] + " " + names[w(2)] + ": " + strings.TrimSpace(v)
		case xSendEvent:
			f.got <- "notify " + names[xle.Uint32(body[8+20:])]
			if answered++; answered == 5 && f.mode != "keep" { // all asked: the selection goes elsewhere
				ev(xSelectionClear, 0, 0, 0)
			}
		case xInternAtom:
			if f.mode == "x-error" {
				c.Write(append([]byte{0, 11}, make([]byte, 30)...))
				continue
			}
			reply(0, words(atom(string(body[4:][:xle.Uint16(body)]))), nil)
		case xGetAtomName:
			reply(0, binary.LittleEndian.AppendUint16(nil, uint16(len(names[w(0)]))), []byte(names[w(0)]))
		case xConvertSelection:
			win, sel, target, p := w(0), w(1), w(2), w(3)
			switch {
			case f.mode == "no-owner" || f.mode == "refuse-image" && target != atom("TARGETS"):
				p = 0
			case target == atom("TARGETS"):
				var v []byte
				for _, ty := range f.types {
					v = xle.AppendUint32(v, atom(ty))
				}
				props[p] = prop{4, 32, v}
			case strings.HasPrefix(f.mode, "incr"):
				props[p] = prop{atom("INCR"), 32, words(uint32(len(f.data)))}
				for d := f.data; d != ""; d = d[min(3, len(d)):] {
					chunks = append(chunks, d[:min(3, len(d))])
				}
				incr = true
			default:
				props[p] = prop{target, 8, []byte(f.data)}
			}
			if p != 0 {
				ev(xPropertyNotify, win, p, 0, 0)
			}
			ev(xSelectionNotify|0x80, 0, win, sel, target, p)
		case xGetProperty:
			win, p := w(0), w(1)
			v := props[p]
			delete(props, p)
			switch {
			case f.mode == "huge":
				c.Write(append(xle.AppendUint32([]byte{1, 8, 0, 0}, 1<<28), make([]byte, 24)...))
				continue
			case f.mode == "short-reply":
				c.Write(append(xle.AppendUint32([]byte{1, 8, 0, 0}, 4), make([]byte, 30)...))
				return
			}
			vlen := 0
			if v.format > 0 {
				vlen = len(v.val) / int(v.format/8)
			}
			reply(v.format, words(v.typ, 0, uint32(vlen)), v.val)
			if !incr {
				continue
			}
			if f.mode == "incr-hang" {
				return
			}
			ev(xPropertyNotify, win, p, 0, 1) // deleted: not a new chunk
			switch {
			case len(chunks) > 0:
				props[p] = prop{atom("image/png"), 8, []byte(chunks[0])}
				chunks = chunks[1:]
			case !ended:
				props[p], ended = prop{atom("image/png"), 8, nil}, true
			default:
				incr = false
				continue
			}
			ev(99, 0)                         // some other event
			ev(xPropertyNotify, win, 1, 0, 0) // another property
			ev(xPropertyNotify, win, p, 0, 0)
		}
	}
}

// TestImageX11: under X (no WAYLAND_DISPLAY), the CLIPBOARD's PNG comes
// whole, or in INCR chunks; text only, no owner or a refusal is no image.
func TestImageX11(t *testing.T) {
	png := fakeX{types: []string{"TARGETS", "text/plain", "image/jpeg", "image/png"}, data: "PNG-DATA-LONGER", cut: -1}
	for _, mode := range []string{"", "incr"} {
		png.mode = mode
		listenX(t, png)
		b, typ, err := Image()
		if err != nil || typ != "image/png" || string(b) != png.data {
			t.Errorf("%q: %q %q %v", mode, b, typ, err)
		}
	}
	for _, f := range []fakeX{
		{types: []string{"TARGETS", "UTF8_STRING"}, cut: -1},
		{types: []string{"image/png"}, mode: "no-owner", cut: -1},
		{types: []string{"image/png"}, mode: "refuse-image", cut: -1},
	} {
		listenX(t, f)
		if b, typ, err := Image(); b != nil || typ != "" || err != nil {
			t.Errorf("%+v: %q %q %v", f, b, typ, err)
		}
	}
}

// TestImageX11Cut: the server going away at any request is an error, and
// never a wrong image.
func TestImageX11Cut(t *testing.T) {
	for _, mode := range []string{"", "incr"} {
		for cut := 0; cut < 20; cut++ {
			f := fakeX{types: []string{"image/png"}, data: "1234567", mode: mode, cut: cut}
			listenX(t, f)
			b, typ, err := Image()
			if err == nil && (string(b) != f.data || typ != "image/png") {
				t.Errorf("%q cut %d: %q %q", mode, cut, b, typ)
			}
			if err != nil && (b != nil || !strings.HasPrefix(err.Error(), "X11")) {
				t.Errorf("%q cut %d: %q %v", mode, cut, b, err)
			}
		}
	}
}

// TestImageX11Failures: each way the server can fail Image is an error.
func TestImageX11Failures(t *testing.T) {
	for mode, want := range map[string]string{
		"refuse-setup": "X11 refused the connection: No protocol specified",
		"cut-head":     "X11 setup: unexpected EOF",
		"cut-more":     "X11 setup: EOF",
		"bad-setup":    "X11 setup: bad reply",
		"bad-screen":   "X11 setup: bad reply",
		"x-error":      "X11 error 11",
		"huge":         "X11: reply too long",
		"short-reply":  "X11: unexpected EOF",
		"incr-hang":    "X11: EOF",
	} {
		listenX(t, fakeX{types: []string{"image/png"}, data: "PNG", mode: mode, cut: -1})
		if b, typ, err := Image(); err == nil || err.Error() != want || b != nil || typ != "" {
			t.Errorf("%s: %q %q %v, want %q", mode, b, typ, err, want)
		}
	}
}

// TestImageX11TooLarge: an INCR transfer past xMax stops with an error.
func TestImageX11TooLarge(t *testing.T) {
	old := xMax
	xMax = 4
	defer func() { xMax = old }()
	listenX(t, fakeX{types: []string{"image/png"}, data: "1234567890", mode: "incr", cut: -1})
	if b, _, err := Image(); err == nil || !strings.Contains(err.Error(), "too large") || b != nil {
		t.Fatalf("%q %v", b, err)
	}
}

// TestImageX11Cookie: the display's cookie from XAUTHORITY, or from
// ~/.Xauthority without it, opens a server that wants one.
func TestImageX11Cookie(t *testing.T) {
	f := fakeX{types: []string{"image/png"}, data: "PNG", cookie: "s3cret", cut: -1}
	listenX(t, f)
	file := filepath.Join(t.TempDir(), "auth")
	os.WriteFile(file, xauthEntry(0xffff, "", "7", "MIT-MAGIC-COOKIE-1", "s3cret"), 0o600)
	t.Setenv("XAUTHORITY", file)
	if b, _, err := Image(); err != nil || string(b) != "PNG" {
		t.Fatalf("XAUTHORITY: %q %v", b, err)
	}
	listenX(t, f)
	home := t.TempDir()
	os.Rename(file, filepath.Join(home, ".Xauthority"))
	t.Setenv("XAUTHORITY", "")
	t.Setenv("HOME", home)
	if b, _, err := Image(); err != nil || string(b) != "PNG" {
		t.Fatalf("~/.Xauthority: %q %v", b, err)
	}
}

// TestImageX11TCP: host:N is TCP port 6000+N.
func TestImageX11TCP(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	fakeX{types: []string{"image/png"}, data: "PNG", cut: -1}.serve(t, l)
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "127.0.0.1:"+strconv.Itoa(port-6000))
	t.Setenv("XAUTHORITY", filepath.Join(t.TempDir(), "none"))
	if b, typ, err := Image(); err != nil || string(b) != "PNG" || typ != "image/png" {
		t.Fatalf("%q %q %v", b, typ, err)
	}
}

// TestDialX: no server is an error naming the X11 clipboard.
func TestDialX(t *testing.T) {
	old := xUnixDir
	xUnixDir = t.TempDir()
	defer func() { xUnixDir = old }()
	for _, d := range []string{":3", "unix:3", "127.0.0.1:-5999"} {
		if _, err := dialX(d); err == nil || !strings.Contains(err.Error(), "no X11 clipboard") {
			t.Errorf("%s: %v", d, err)
		}
	}
}

// TestXauth: the first MIT-MAGIC-COOKIE-1 for the display, from this host
// or a wildcard (any host over TCP); a cut file or no match is none.
func TestXauth(t *testing.T) {
	me, _ := os.Hostname()
	var file []byte
	for _, e := range [][]byte{
		xauthEntry(256, "otherhost", "7", "MIT-MAGIC-COOKIE-1", "other"),
		xauthEntry(256, me, "8", "MIT-MAGIC-COOKIE-1", "eight"),
		xauthEntry(256, me, "7", "XDM-AUTHORIZATION-1", "xdm"),
		xauthEntry(256, me, "", "MIT-MAGIC-COOKIE-1", "mine"),
		xauthEntry(0xffff, "", "7", "MIT-MAGIC-COOKIE-1", "wild"),
	} {
		file = append(file, e...)
	}
	path := filepath.Join(t.TempDir(), "auth")
	t.Setenv("XAUTHORITY", path)
	for _, tc := range []struct {
		file      []byte
		num       string
		tcp       bool
		want      string
		wantFound bool
	}{
		{file, "7", false, "mine", true},
		{file[len(xauthEntry(256, "otherhost", "7", "MIT-MAGIC-COOKIE-1", "other")):], "9", false, "mine", true},
		{file, "7", true, "other", true},
		{file, "8", false, "eight", true},
		{file[:30], "7", false, "", false},
		{xauthEntry(256, "otherhost", "7", "MIT-MAGIC-COOKIE-1", "x"), "7", false, "", false},
		{nil, "7", false, "", false},
	} {
		os.WriteFile(path, tc.file, 0o600)
		name, data := xauth(tc.num, tc.tcp)
		if string(data) != tc.want || (name != "") != tc.wantFound {
			t.Errorf("display %s tcp %v: %q %q, want %q", tc.num, tc.tcp, name, data, tc.want)
		}
	}
}

// TestPrimaryX11: under X, a middle click's text is PRIMARY's, as
// UTF8_STRING; taken, desk answers each client that asks (its targets, the
// text, TEXT as UTF8_STRING into the target-named property, an image
// refused) until another takes it.
func TestPrimaryX11(t *testing.T) {
	listenX(t, fakeX{types: []string{"TARGETS", "image/png", "UTF8_STRING"}, data: "uptime", cut: -1})
	if got := Primary(); got != "uptime" {
		t.Errorf("primary: %q", got)
	}
	long := strings.Repeat("a", xPropMax-1) + "é" // its second byte past what fits: cut before the é
	for text, string_ := range map[string][]string{
		"café": {"P STRING: caf\xe9", "notify P"}, // Latin-1, as ICCCM's STRING is
		"✓ ok": {"notify "},                       // no Latin-1 for it: refused
		long:   {"P STRING: " + long[:xPropMax-1], "notify P"},
	} {
		got := make(chan string, 16)
		listenX(t, fakeX{cut: -1, got: got})
		ended := make(chan error, 1)
		go func() { ended <- ownPrimary(text, make(chan struct{})) }()
		utf := text
		if text == long {
			utf = long[:xPropMax-1]
		}
		want := append(append([]string{
			"P ATOM: TARGETS UTF8_STRING STRING TEXT", "notify P",
			"P UTF8_STRING: " + utf, "notify P",
		}, string_...), "TEXT UTF8_STRING: "+utf, "notify TEXT",
			"notify ", // the image: refused, no property
		)
		for i, w := range want {
			select {
			case g := <-got:
				if g != w {
					t.Errorf("%.12q %d: %.40q, want %.40q", text, i, g, w)
				}
			case <-time.After(testwait.Long):
				t.Fatalf("%.12q %d: nothing, want %.40q", text, i, w)
			}
		}
		if err := <-ended; err != nil { // the selection went elsewhere
			t.Error(err)
		}
	}
	got := make(chan string, 16)
	listenX(t, fakeX{cut: -1, got: got})
	done := make(chan struct{})
	close(done)
	if err := ownPrimary("stale", done); err != nil {
		t.Errorf("a stopped call: %v", err)
	}
	select {
	case g := <-got:
		t.Errorf("a stopped call took it: %q", g)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestPrimaryX11Fails: no server, a refused atom or the server going away
// ends serving with an error.
func TestPrimaryX11Fails(t *testing.T) {
	listenX(t, fakeX{mode: "x-error", cut: -1})
	if err := ownPrimary("x", nil); err == nil {
		t.Error("an X error on the atoms")
	}
	got := make(chan string, 16)
	listenX(t, fakeX{cut: -1, got: got, mode: "keep"})
	done := make(chan struct{})
	ended := make(chan error, 1)
	go func() { ended <- ownPrimary("x", done) }()
	<-got // it serves
	close(done)
	if err := <-ended; err == nil {
		t.Error("stopped: no error from the closed connection")
	}
	for _, f := range []fakeX{{cut: 7}, {mode: "cut-tail", cut: -1}} { // gone after the take, or inside a reply's tail
		f.got = make(chan string, 16)
		listenX(t, f)
		if err := ownPrimary("x", make(chan struct{})); err == nil {
			t.Errorf("%+v: no error", f)
		}
	}
	t.Setenv("DISPLAY", ":9") // nobody there
	if err := ownPrimary("x", nil); err == nil {
		t.Error("no server")
	}
}

// xauthEntry is one Xauthority entry.
func xauthEntry(fam uint16, addr, num, name, data string) []byte {
	be := binary.BigEndian
	b := be.AppendUint16(nil, fam)
	for _, s := range []string{addr, num, name, data} {
		b = append(be.AppendUint16(b, uint16(len(s))), s...)
	}
	return b
}
