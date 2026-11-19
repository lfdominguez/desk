//go:build linux

// On Linux, Image speaks the Wayland wire protocol itself, over the
// compositor's socket: the data-control extension (ext-data-control-v1, or
// wlroots' zwlr-data-control-v1) hands the selection to a client without a
// window or focus. No libraries, no wl-paste. The same device reads the
// primary selection (readWayland) and owns it (ownWayland: a data source
// that answers each reader's pipe).

package clip

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

var le = binary.NativeEndian // the wire is in the host's byte order

// managers are the data-control globals, preferred first; their requests
// and events have the same opcodes.
var managers = []string{"ext_data_control_manager_v1", "zwlr_data_control_manager_v1"}

// maxImage bounds what a clipboard owner may hand over.
const maxImage = 64 << 20

// pipe is os.Pipe; tests make it fail.
var pipe = os.Pipe

// serveRead is how much serve reads at a time; tests shrink it so messages
// come in pieces.
var serveRead = 4096

// Image is the clipboard's image and its type (image/png preferred), or
// nil when it holds no image: the X server's (clip_x11.go) when desk runs
// under one without Wayland, else the compositor's.
func Image() ([]byte, string, error) {
	if d := x11(); d != "" {
		return readX11(d, "CLIPBOARD", pick)
	}
	return readWayland(false, pick)
}

// x11 is the X display desk reads selections on: set only under an X server
// without Wayland.
func x11() string {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return ""
	}
	return os.Getenv("DISPLAY")
}

// wl is a connection with the seat's data-control device. Both managers
// number their requests and events alike.
type wl struct {
	*conn
	mgr, dev uint32
}

// errNoPrimary: the compositor's data-control has no primary selection.
var errNoPrimary = errors.New("the compositor has no primary selection")

// openDevice connects, binds the seat and a data-control manager (version 2
// at most) and gets the seat's device.
func openDevice() (*wl, error) {
	w, err := dial()
	if err != nil {
		return nil, err
	}
	w.c.SetDeadline(time.Now().Add(3 * time.Second))
	reg := w.newID()
	w.send(1, 1, args{}.u(reg), -1) // wl_display.get_registry
	type global struct{ name, version uint32 }
	globals := map[string]global{}
	if err := w.roundtrip(func(id, op uint32, b body) {
		if id == reg && op == 0 { // wl_registry.global
			name, iface, ver := b.u(), b.s(), b.u()
			globals[iface] = global{name, ver}
		}
	}); err != nil {
		w.c.Close()
		return nil, err
	}
	seat, ok := globals["wl_seat"]
	i := slices.IndexFunc(managers, func(m string) bool { _, ok := globals[m]; return ok })
	if !ok || i < 0 {
		w.c.Close()
		return nil, errors.New("the compositor has no clipboard data-control protocol")
	}
	bind := func(iface string, name, ver uint32) uint32 {
		id := w.newID()
		w.send(reg, 0, args{}.u(name).s(iface).u(ver).u(id), -1) // wl_registry.bind
		return id
	}
	g := globals[managers[i]]
	d := &wl{conn: w}
	seatID := bind("wl_seat", seat.name, 1)
	d.mgr = bind(managers[i], g.name, min(g.version, 2)) // wlroots' has the primary selection from 2
	d.dev = w.newID()
	w.send(d.mgr, 1, args{}.u(d.dev).u(seatID), -1) // get_data_device
	return d, nil
}

// selections reads the device's first events: its offers and their types,
// the clipboard's offer and the primary selection's (0: empty), and whether
// it has a primary selection at all (it says so then, empty or not).
func (w *wl) selections() (offers map[uint32][]string, clip, primary uint32, hasPrimary bool, err error) {
	offers = map[uint32][]string{}
	err = w.roundtrip(func(id, op uint32, b body) {
		switch {
		case id == w.dev && op == 0: // data_offer: a new offer, its types follow
			offers[b.u()] = nil
		case id == w.dev && op == 1: // selection
			clip = b.u()
		case id == w.dev && op == 3: // primary_selection
			primary, hasPrimary = b.u(), true
		case op == 0 && hasKey(offers, id): // offer.offer: one of its types
			offers[id] = append(offers[id], b.s())
		}
	})
	return offers, clip, primary, hasPrimary, err
}

// readWayland reads the clipboard, or with primary the primary selection,
// as the type pick chooses among those offered; nil when there's none.
func readWayland(primary bool, pick func([]string) string) ([]byte, string, error) {
	w, err := openDevice()
	if err != nil {
		return nil, "", err
	}
	defer w.c.Close()
	offers, sel, psel, hasPrimary, err := w.selections()
	if err != nil {
		return nil, "", err
	}
	if primary {
		if !hasPrimary {
			return nil, "", errNoPrimary
		}
		sel = psel
	}
	typ := pick(offers[sel])
	if sel == 0 || typ == "" {
		return nil, "", nil
	}
	pr, pw, err := pipe()
	if err != nil {
		return nil, "", err
	}
	defer pr.Close()
	err = w.send(sel, 0, args{}.s(typ), int(pw.Fd())) // offer.receive: the owner writes into the pipe
	pw.Close()
	if err != nil {
		return nil, "", err
	}
	pr.SetReadDeadline(time.Now().Add(5 * time.Second))
	data, err := io.ReadAll(io.LimitReader(pr, maxImage))
	if err != nil {
		return nil, "", fmt.Errorf("reading the clipboard: %w", err)
	}
	return data, typ, nil
}

// pickText is the text type to ask for, UTF-8 first.
func pickText(types []string) string {
	for _, t := range textTypes {
		if slices.Contains(types, t) {
			return t
		}
	}
	return ""
}

// textTypes are the names text goes by in a selection, preferred first.
var textTypes = []string{"text/plain;charset=utf-8", "UTF8_STRING", "text/plain", "STRING", "TEXT"}

// ownWayland makes text the primary selection and hands it to whoever asks
// until another takes the selection or done closes; a done closed before it
// takes it keeps it from doing so (stopped).
func ownWayland(text string, done <-chan struct{}) error {
	w, err := openDevice()
	if err != nil {
		return err
	}
	defer w.c.Close()
	offers, _, _, hasPrimary, err := w.selections()
	if err != nil {
		return err
	}
	if !hasPrimary {
		return errNoPrimary
	}
	for id := range offers { // others' selections: not ours to keep
		w.send(id, 1, args{}, -1) // offer.destroy
	}
	if stopped(done) {
		return nil
	}
	src := w.newID()
	w.send(w.mgr, 0, args{}.u(src), -1) // create_data_source
	for _, t := range textTypes {
		w.send(src, 0, args{}.s(t), -1) // source.offer
	}
	w.send(w.dev, 2, args{}.u(src), -1) // set_primary_selection
	w.c.SetDeadline(time.Time{})        // it waits for readers now, as long as it owns it
	defer closeOn(done, w.c)()
	return w.serve(src, text)
}

// closeOn closes c when done closes, until the returned func is called.
func closeOn(done <-chan struct{}, c io.Closer) func() {
	ended := make(chan struct{})
	go func() {
		select {
		case <-done:
			c.Close()
		case <-ended:
		}
	}()
	return func() { close(ended) }
}

// serve answers source src's events: each send writes text into the
// reader's pipe; cancelled (another took the selection) ends it. The offers
// of others' selections meanwhile are destroyed as they come.
func (w *wl) serve(src uint32, text string) error {
	var buf []byte
	var fds []int
	defer func() {
		for _, fd := range fds {
			syscall.Close(fd)
		}
	}()
	b, oob := make([]byte, serveRead), make([]byte, 256)
	for {
		n, on, _, _, err := w.c.ReadMsgUnix(b, oob)
		if err != nil {
			return err
		}
		if msgs, err := syscall.ParseSocketControlMessage(oob[:on]); err == nil {
			for _, m := range msgs {
				f, _ := syscall.ParseUnixRights(&m)
				fds = append(fds, f...)
			}
		}
		buf = append(buf, b[:n]...)
		for len(buf) >= 8 {
			id, op, size := le.Uint32(buf), le.Uint32(buf[4:])&0xffff, int(le.Uint32(buf[4:])>>16)
			if size < 8 {
				return errors.New("wayland: bad message")
			}
			if len(buf) < size {
				break
			}
			ev := body(buf[8:size])
			buf = buf[size:]
			switch {
			case id == 1 && op == 0: // wl_display.error
				_, code := ev.u(), ev.u()
				return fmt.Errorf("wayland error %d: %s", code, ev.s())
			case id == w.dev && op == 0: // data_offer: another's selection
				w.send(ev.u(), 1, args{}, -1) // offer.destroy
			case id == src && op == 0 && len(fds) > 0: // send: a reader's pipe
				fd := fds[0]
				fds = fds[1:]
				syscall.SetNonblock(fd, true) // pollable: the deadline holds even on a reader that stops reading
				f := os.NewFile(uintptr(fd), "pipe")
				go func() { // a slow reader holds no one else up
					f.SetWriteDeadline(time.Now().Add(5 * time.Second))
					f.WriteString(text)
					f.Close()
				}()
			case id == src && op == 1: // cancelled: another took the selection
				return nil
			}
		}
	}
}

func hasKey(m map[uint32][]string, k uint32) bool { _, ok := m[k]; return ok }

// pick is the image type to ask for: PNG when offered, else any image.
func pick(types []string) string {
	if slices.Contains(types, "image/png") {
		return "image/png"
	}
	if i := slices.IndexFunc(types, func(t string) bool { return strings.HasPrefix(t, "image/") }); i >= 0 {
		return types[i]
	}
	return ""
}

type conn struct {
	c    *net.UnixConn
	next uint32
}

// dial connects to $WAYLAND_DISPLAY (a name in $XDG_RUNTIME_DIR, or a path).
func dial() (*conn, error) {
	name := os.Getenv("WAYLAND_DISPLAY")
	if name == "" {
		name = "wayland-0"
	}
	if !filepath.IsAbs(name) {
		name = filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), name)
	}
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: name, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("no Wayland clipboard: %w", err)
	}
	return &conn{c: c, next: 2}, nil // 1 is wl_display
}

func (w *conn) newID() uint32 { w.next++; return w.next - 1 }

// send writes request op of object id, with fd (-1 none) alongside.
func (w *conn) send(id, op uint32, a args, fd int) error {
	msg := le.AppendUint32(nil, id)
	msg = le.AppendUint32(msg, uint32(8+len(a))<<16|op)
	msg = append(msg, a...)
	var oob []byte
	if fd >= 0 {
		oob = syscall.UnixRights(fd)
	}
	_, _, err := w.c.WriteMsgUnix(msg, oob, nil)
	return err
}

// roundtrip hands every event to on until the server has handled all that
// was sent (wl_display.sync's callback); a protocol error ends it.
func (w *conn) roundtrip(on func(id, op uint32, b body)) error {
	cb := w.newID()
	if err := w.send(1, 0, args{}.u(cb), -1); err != nil {
		return err
	}
	for {
		var h [8]byte
		if _, err := io.ReadFull(w.c, h[:]); err != nil {
			return err
		}
		id, op, size := le.Uint32(h[:]), le.Uint32(h[4:])&0xffff, le.Uint32(h[4:])>>16
		if size < 8 {
			return errors.New("wayland: bad message")
		}
		b := make(body, size-8)
		if _, err := io.ReadFull(w.c, b); err != nil {
			return err
		}
		switch {
		case id == 1 && op == 0: // wl_display.error
			_, code := b.u(), b.u()
			return fmt.Errorf("wayland error %d: %s", code, b.s())
		case id == cb && op == 0:
			return nil
		case id != 1:
			on(id, op, b)
		}
	}
}

// args builds a request's arguments.
type args []byte

func (a args) u(v uint32) args { return le.AppendUint32(a, v) }

func (a args) s(s string) args {
	a = a.u(uint32(len(s) + 1))
	a = append(append(a, s...), 0)
	for len(a)%4 != 0 {
		a = append(a, 0)
	}
	return a
}

// body reads an event's arguments in order; short ones read as zero.
type body []byte

func (b *body) u() uint32 {
	if len(*b) < 4 {
		return 0
	}
	v := le.Uint32(*b)
	*b = (*b)[4:]
	return v
}

func (b *body) s() string {
	n := int(b.u())
	if n == 0 || n > len(*b) {
		return ""
	}
	s := string((*b)[:n-1])
	*b = (*b)[min((n+3)&^3, len(*b)):]
	return s
}
