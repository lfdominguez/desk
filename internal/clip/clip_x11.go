//go:build linux

// Under an X server (no WAYLAND_DISPLAY), Image speaks the X11 core protocol
// itself on the display's socket, as clip.go speaks Wayland: the CLIPBOARD
// selection's TARGETS, then its image converted into a property of a window
// of ours (never mapped) and read whole, or in INCR chunks when it's large
// (ICCCM). Primary reads the PRIMARY selection's text the same way, and
// SetPrimary owns it (ownX11). No libraries, no xclip.

package clip

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var xle = binary.LittleEndian // we open the connection as 'l': little-endian

// xUnixDir holds the X servers' sockets; xMax bounds a reply and an INCR
// transfer (maxImage; tests shrink it).
var (
	xUnixDir = "/tmp/.X11-unix"
	xMax     = maxImage
)

// X11 opcodes, events and constants Image uses.
const (
	xCreateWindow      = 1
	xGetProperty       = 20
	xInternAtom        = 16
	xGetAtomName       = 17
	xChangeProperty    = 18
	xSetSelectionOwner = 22
	xConvertSelection  = 24
	xSendEvent         = 25
	xPropertyNotify    = 28
	xSelectionClear    = 29
	xSelectionRequest  = 30
	xSelectionNotify   = 31
	xAtom              = 4      // the ATOM type
	xPropMax           = 256000 // what one ChangeProperty carries (no BIG-REQUESTS)
)

// ownX11 makes text the PRIMARY selection on the X display disp and answers
// the clients that ask for it until another takes it or done closes; a done
// closed before it takes it keeps it from doing so (stopped).
// ponytail: text past xPropMax is cut (at a character); INCR would carry the rest.
func ownX11(disp, text string, done <-chan struct{}) error {
	x, err := dialX(disp)
	if err != nil {
		return err
	}
	defer x.c.Close()
	a := map[string]uint32{}
	for _, n := range []string{"PRIMARY", "TARGETS", "UTF8_STRING", "STRING", "TEXT"} {
		if a[n], err = x.atom(n); err != nil {
			return err
		}
	}
	if stopped(done) {
		return nil
	}
	win := x.base // as readX11's: an InputOnly window of ours, never mapped
	x.send(xCreateWindow, 0, words(win, x.root, 0, 1|1<<16, 2<<16, 0, 0x800, 0x400000))
	x.send(xSetSelectionOwner, 0, words(win, a["PRIMARY"], 0))
	x.c.SetDeadline(time.Time{}) // it waits for readers now, as long as it owns it
	defer closeOn(done, x.c)()
	data := []byte(text)
	if len(data) > xPropMax {
		cut := xPropMax
		for !utf8.RuneStart(data[cut]) {
			cut--
		}
		data = data[:cut]
	}
	latin1, isLatin1 := []byte{}, true // STRING is ISO Latin-1 (ICCCM)
	for _, r := range string(data) {
		isLatin1 = isLatin1 && r < 0x100
		latin1 = append(latin1, byte(r))
	}
	for {
		m := make([]byte, 32)
		if _, err := io.ReadFull(x.c, m); err != nil {
			return err
		}
		if n := int(xle.Uint32(m[4:])) * 4; m[0] == 1 && n > 0 { // a reply's tail: not ours to read
			if _, err := io.CopyN(io.Discard, x.c, int64(n)); err != nil {
				return err
			}
		}
		switch m[0] & 0x7f {
		case xSelectionClear:
			return nil
		case xSelectionRequest:
			req, sel, target, prop := xle.Uint32(m[12:]), xle.Uint32(m[16:]), xle.Uint32(m[20:]), xle.Uint32(m[24:])
			if prop == 0 { // an obsolete client: the target names the property
				prop = target
			}
			switch target {
			case a["TARGETS"]:
				x.send(xChangeProperty, 0, append(words(req, prop, xAtom, 32, 4), words(a["TARGETS"], a["UTF8_STRING"], a["STRING"], a["TEXT"])...))
			case a["UTF8_STRING"], a["TEXT"]: // TEXT: the owner's choice of encoding
				x.send(xChangeProperty, 0, append(words(req, prop, a["UTF8_STRING"], 8, uint32(len(data))), data...))
			case a["STRING"]:
				if !isLatin1 { // it can't say it: refused, the client asks for UTF8_STRING
					prop = 0
					break
				}
				x.send(xChangeProperty, 0, append(words(req, prop, a["STRING"], 8, uint32(len(latin1))), latin1...))
			default:
				prop = 0 // refused
			}
			ev := make([]byte, 32)
			ev[0] = xSelectionNotify
			copy(ev[4:8], m[4:8]) // the request's time
			copy(ev[8:], words(req, sel, target, prop))
			x.send(xSendEvent, 0, append(words(req, 0), ev...))
		}
	}
}

type xconn struct {
	c          net.Conn
	root, base uint32 // the first screen's root window; our resource ids' base
}

// readX11 reads selection sel (CLIPBOARD, PRIMARY) on the X display disp,
// as the type pick chooses among its targets; nil when there's none.
func readX11(disp, sel string, pick func([]string) string) ([]byte, string, error) {
	x, err := dialX(disp)
	if err != nil {
		return nil, "", err
	}
	defer x.c.Close()
	a := map[string]uint32{}
	for _, n := range []string{"TARGETS", "INCR", "DESK_CLIP"} {
		if a[n], err = x.atom(n); err != nil {
			return nil, "", err
		}
	}
	if a["SEL"], err = x.atom(sel); err != nil {
		return nil, "", err
	}
	// an InputOnly 1×1 child of the root that hears its properties change
	win := x.base
	x.send(xCreateWindow, 0, words(win, x.root, 0, 1|1<<16, 2<<16, 0, 0x800, 0x400000))
	targets, err := x.convert(win, a, a["TARGETS"])
	if err != nil {
		return nil, "", err
	}
	types, atoms := []string{}, map[string]uint32{}
	for i := 0; i+4 <= len(targets); i += 4 {
		t := xle.Uint32(targets[i:])
		name, err := x.atomName(t)
		if err != nil {
			return nil, "", err
		}
		types, atoms[name] = append(types, name), t
	}
	typ := pick(types)
	if typ == "" {
		return nil, "", nil
	}
	data, err := x.convert(win, a, atoms[typ])
	if err != nil || data == nil {
		return nil, "", err
	}
	return data, typ, nil
}

// dialX connects to disp ("[host]:N[.S]"; no host or "unix" is the local
// socket, else TCP port 6000+N) and does the setup.
func dialX(disp string) (*xconn, error) {
	host, rest, _ := strings.Cut(disp, ":")
	num, _, _ := strings.Cut(rest, ".")
	tcp := host != "" && host != "unix"
	var c net.Conn
	var err error
	if tcp {
		n, _ := strconv.Atoi(num)
		c, err = net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(6000+n)))
	} else {
		c, err = net.Dial("unix", filepath.Join(xUnixDir, "X"+num))
	}
	if err != nil {
		return nil, fmt.Errorf("no X11 clipboard: %w", err)
	}
	c.SetDeadline(time.Now().Add(5 * time.Second))
	x := &xconn{c: c}
	if err := x.setup(xauth(num, tcp)); err != nil {
		c.Close()
		return nil, err
	}
	return x, nil
}

// setup opens the connection with the cookie and reads the root and our ids.
func (x *xconn) setup(name string, cookie []byte) error {
	b := []byte{'l', 0, 11, 0, 0, 0}
	b = xle.AppendUint16(b, uint16(len(name)))
	b = xle.AppendUint16(b, uint16(len(cookie)))
	b = xpad(append(append(b, 0, 0), name...))
	x.c.Write(xpad(append(b, cookie...)))
	head := make([]byte, 8)
	if _, err := io.ReadFull(x.c, head); err != nil {
		return fmt.Errorf("X11 setup: %w", err)
	}
	more := make([]byte, int(xle.Uint16(head[6:]))*4)
	if _, err := io.ReadFull(x.c, more); err != nil {
		return fmt.Errorf("X11 setup: %w", err)
	}
	if head[0] != 1 { // failed or authenticate: the reason follows
		return fmt.Errorf("X11 refused the connection: %s", strings.TrimRight(string(more), "\x00 \n"))
	}
	// vendor, pixmap formats, then the first screen, its root window first
	if len(more) < 32 {
		return errors.New("X11 setup: bad reply")
	}
	at := 32 + (int(xle.Uint16(more[16:]))+3)&^3 + 8*int(more[21])
	if at+4 > len(more) {
		return errors.New("X11 setup: bad reply")
	}
	x.base, x.root = xle.Uint32(more[4:]), xle.Uint32(more[at:])
	return nil
}

// xauth is the MIT-MAGIC-COOKIE-1 for display num in $XAUTHORITY
// (~/.Xauthority): this host's entry, a wildcard one, or any for a TCP
// display; none when there's no such entry.
func xauth(num string, tcp bool) (string, []byte) {
	f := os.Getenv("XAUTHORITY")
	if f == "" {
		f = filepath.Join(os.Getenv("HOME"), ".Xauthority")
	}
	b, _ := os.ReadFile(f)
	me, _ := os.Hostname()
	be := binary.BigEndian
	for len(b) >= 2 {
		fam := be.Uint16(b)
		b = b[2:]
		var field [4][]byte // address, display number, auth name, data
		for i := range field {
			if len(b) < 2 || len(b) < 2+int(be.Uint16(b)) {
				return "", nil
			}
			n := 2 + int(be.Uint16(b))
			field[i], b = b[2:n], b[n:]
		}
		dnum, name := string(field[1]), string(field[2])
		if name == "MIT-MAGIC-COOKIE-1" && (dnum == "" || dnum == num) &&
			(fam == 0xffff || string(field[0]) == me || tcp) {
			return name, field[3]
		}
	}
	return "", nil
}

// convert asks the selection's (a["SEL"]) owner for target in our property
// and reads it: nil when there's no owner or it refuses.
func (x *xconn) convert(win uint32, a map[string]uint32, target uint32) ([]byte, error) {
	prop := a["DESK_CLIP"]
	x.send(xConvertSelection, 0, words(win, a["SEL"], target, prop, 0))
	m, err := x.wait(func(m []byte) bool { return m[0]&0x7f == xSelectionNotify })
	if err != nil || xle.Uint32(m[20:]) == 0 {
		return nil, err
	}
	typ, data, err := x.property(win, prop)
	if err != nil || typ != a["INCR"] {
		return data, err
	}
	// INCR: each chunk is a new value of the property, until an empty one
	var all []byte
	for {
		_, err := x.wait(func(m []byte) bool {
			return m[0]&0x7f == xPropertyNotify && xle.Uint32(m[8:]) == prop && m[16] == 0
		})
		if err != nil {
			return nil, err
		}
		_, chunk, err := x.property(win, prop)
		if err != nil || len(chunk) == 0 {
			return all, err
		}
		if all = append(all, chunk...); len(all) > xMax {
			return nil, errors.New("the clipboard image is too large")
		}
		x.c.SetDeadline(time.Now().Add(5 * time.Second))
	}
}

// property reads our property and deletes it: its type and value.
func (x *xconn) property(win, prop uint32) (uint32, []byte, error) {
	x.send(xGetProperty, 1, words(win, prop, 0, 0, uint32(xMax/4)))
	m, err := x.wait(isReply)
	if err != nil {
		return 0, nil, err
	}
	n := int(xle.Uint32(m[16:])) * int(m[1]/8) // its length is in format units
	return xle.Uint32(m[8:]), m[32:][:min(n, len(m)-32)], nil
}

// atom interns name.
func (x *xconn) atom(name string) (uint32, error) {
	x.send(xInternAtom, 0, append(xle.AppendUint32(nil, uint32(len(name))), name...))
	m, err := x.wait(isReply)
	if err != nil {
		return 0, err
	}
	return xle.Uint32(m[8:]), nil
}

// atomName is atom a's name.
func (x *xconn) atomName(a uint32) (string, error) {
	x.send(xGetAtomName, 0, words(a))
	m, err := x.wait(isReply)
	if err != nil {
		return "", err
	}
	return string(m[32:][:min(int(xle.Uint16(m[8:])), len(m)-32)]), nil
}

func isReply(m []byte) bool { return m[0] == 1 }

// send writes a request: opcode, its one-byte argument, the body padded to
// words. A failed write shows as the read that follows failing.
func (x *xconn) send(op, arg byte, body []byte) {
	body = xpad(body)
	b := xle.AppendUint16([]byte{op, arg}, uint16(1+len(body)/4))
	x.c.Write(append(b, body...))
}

// wait reads messages until one that is; an X error ends it.
func (x *xconn) wait(is func([]byte) bool) ([]byte, error) {
	for {
		m := make([]byte, 32)
		if _, err := io.ReadFull(x.c, m); err != nil {
			return nil, fmt.Errorf("X11: %w", err)
		}
		if m[0] == 0 {
			return nil, fmt.Errorf("X11 error %d", m[1])
		}
		if n := int(xle.Uint32(m[4:])) * 4; m[0] == 1 && n > 0 {
			if n > xMax+32 {
				return nil, errors.New("X11: reply too long")
			}
			m = append(m, make([]byte, n)...)
			if _, err := io.ReadFull(x.c, m[32:]); err != nil {
				return nil, fmt.Errorf("X11: %w", err)
			}
		}
		if is(m) {
			return m, nil
		}
	}
}

func words(w ...uint32) []byte {
	var b []byte
	for _, v := range w {
		b = xle.AppendUint32(b, v)
	}
	return b
}

func xpad(b []byte) []byte { return append(b, make([]byte, -len(b)&3)...) }
