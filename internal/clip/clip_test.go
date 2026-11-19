//go:build linux

package clip

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lfdominguez/desk/internal/testwait"
)

// fakeCompositor serves one client on a socket in dir: a seat, the wlroots
// data-control manager, and a selection offering types; a receive of an
// image type gets data written into its fd.
// A mode makes it misbehave: "error-registry" and "error-device" answer
// that request with a protocol error, "no-manager" offers no data-control,
// "hangup" stops reading once the selection is sent, "silent" keeps the
// receive's fd open without writing, "noisy" mixes in events of other
// objects and opcodes that look like the ones Image reads.
// Its primary selection offers the same types, and reads as "primary:"+data.
// A client that takes the primary selection (set_primary_selection, request
// 2 in both protocols) gets another's offer, is asked for it (a send with a
// pipe), and what it writes comes on the channel with how many offers it had
// destroyed when it took it; then it is cancelled.
// "v1" binds wlroots' manager at version 1 (no primary selection), "ext" offers
// ext-data-control instead, "serve-error" answers the take with an error,
// "silent-take" lets it take, says "taken" and never asks, "bad-send" sends a pipe for
// another object and then a malformed message.
func fakeCompositor(t *testing.T, dir string, types []string, data string, mode ...string) <-chan string {
	m := ""
	if len(mode) > 0 {
		m = mode[0]
	}
	served := make(chan string, 1)
	protoErr := func(ev func(uint32, uint32, args)) { ev(1, 0, args{}.u(1).u(7).s("boom")) }
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "wl"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		c, err := l.AcceptUnix()
		if err != nil {
			return
		}
		defer c.Close()
		ev := func(id, op uint32, a args) {
			m := le.AppendUint32(nil, id)
			m = le.AppendUint32(m, uint32(8+len(a))<<16|op)
			c.Write(append(m, a...))
		}
		ifaces := map[uint32]string{1: "wl_display"}
		var reg, src, dev uint32
		var devSent bool
		destroyed := 0
		const offer, primary, later = 0xff000001, 0xff000002, 0xff000003
		mgr, mver := "zwlr_data_control_manager_v1", uint32(2)
		switch m {
		case "v1":
			mver = 1
		case "ext":
			mgr, mver = "ext_data_control_manager_v1", 1
		}
		buf, oob := make([]byte, 4096), make([]byte, 64)
		for {
			n, on, _, _, err := c.ReadMsgUnix(buf, oob)
			if err != nil {
				return
			}
			var fds []int
			if msgs, _ := syscall.ParseSocketControlMessage(oob[:on]); len(msgs) > 0 {
				fds, _ = syscall.ParseUnixRights(&msgs[0])
			}
			for b := body(buf[:n]); len(b) >= 8; {
				id, word := b.u(), b.u()
				op, rest := word&0xffff, body(b[:word>>16-8])
				b = b[word>>16-8:]
				switch {
				case id == 1 && op == 1:
					reg = rest.u()
					if m == "error-registry" {
						protoErr(ev)
						continue
					}
					ev(reg, 0, args{}.u(1).s("wl_seat").u(7))
					if m != "no-manager" {
						ev(reg, 0, args{}.u(2).s(mgr).u(mver))
					}
					if m == "noisy" { // another object's event, shaped like a global
						ev(99, 0, args{}.u(9).s("zwlr_data_control_manager_v1").u(1))
					}
				case id == 1 && op == 0:
					if devSent && m == "hangup" {
						c.CloseRead() // the client's next request fails
					}
					ev(rest.u(), 0, args{}.u(0))
				case (id == offer || id == primary || id == later) && op == 1: // offer.destroy
					destroyed++
				case ifaces[id] == "manager" && op == 0: // create_data_source
					src = rest.u()
					ifaces[src] = "source"
				case id == dev && op == 2 && rest.u() == src: // set_primary_selection: a reader asks for it
					if m == "silent-take" {
						served <- "taken"
						continue
					}
					if m == "serve-error" {
						protoErr(ev)
						continue
					}
					if m == "bad-send" { // a pipe for some other object, then a message too short to be one
						r, w, _ := os.Pipe()
						c.WriteMsgUnix(append(le.AppendUint32(nil, 99), le.AppendUint32(nil, 8<<16)...), syscall.UnixRights(int(w.Fd())), nil)
						w.Close()
						r.Close()
						c.Write(append(le.AppendUint32(nil, src), le.AppendUint32(nil, 4<<16)...))
						continue
					}
					ev(dev, 0, args{}.u(later)) // meanwhile another's selection: an offer to destroy
					r, w, _ := os.Pipe()
					msg := le.AppendUint32(nil, src)
					a := args{}.s("text/plain;charset=utf-8")
					msg = le.AppendUint32(msg, uint32(8+len(a))<<16|0)
					c.WriteMsgUnix(append(msg, a...), syscall.UnixRights(int(w.Fd())), nil)
					w.Close()
					n := destroyed // the first selections', before it took this one
					go func() {
						b, _ := io.ReadAll(r)
						r.Close()
						served <- fmt.Sprintf("%s|%d destroyed", b, n)
						ev(src, 1, args{}) // cancelled: another took it
					}()
				case ifaces[id] == "manager" && op == 1 && m == "error-device":
					protoErr(ev)
				case id == reg && op == 0:
					name, _, _, nid := rest.u(), rest.s(), rest.u(), rest.u()
					ifaces[nid] = map[uint32]string{1: "seat", 2: "manager"}[name]
				case ifaces[id] == "manager" && op == 1:
					dev = rest.u()
					if mver >= 2 || m == "ext" {
						ev(dev, 0, args{}.u(primary))
						for _, ty := range types {
							ev(primary, 0, args{}.s(ty))
						}
						ev(dev, 3, args{}.u(primary))
					}
					ev(dev, 0, args{}.u(offer))
					for _, ty := range types {
						ev(offer, 0, args{}.s(ty))
					}
					if m == "noisy" { // the offer's op 1 is no type
						ev(offer, 1, args{}.s("image/png"))
					}
					ev(dev, 1, args{}.u(offer))
					if m == "noisy" { // another object's op 1 is no selection
						ev(99, 1, args{}.u(0))
					}
					devSent = true
				case (id == offer || id == primary) && op == 0 && len(fds) > 0:
					f := os.NewFile(uintptr(fds[0]), "pipe")
					if m == "silent" {
						t.Cleanup(func() { f.Close() })
						continue
					}
					if id == primary {
						f.WriteString("primary:")
					}
					f.WriteString(data + "|" + rest.s())
					f.Close()
				}
			}
		}
	}()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("WAYLAND_DISPLAY", "wl")
	return served
}

// TestImage: the selection's PNG comes through the pipe; a clipboard of
// text only is no image.
func TestImage(t *testing.T) {
	fakeCompositor(t, t.TempDir(), []string{"text/html", "image/jpeg", "image/png"}, "PNG")
	b, typ, err := Image()
	if err != nil || typ != "image/png" || string(b) != "PNG|image/png" {
		t.Fatalf("%q %q %v", b, typ, err)
	}
	fakeCompositor(t, t.TempDir(), []string{"text/plain"}, "")
	if b, typ, err := Image(); b != nil || typ != "" || err != nil {
		t.Fatalf("text clipboard: %q %q %v", b, typ, err)
	}
}

// TestImageIgnoresOtherEvents: only the registry's globals, the device's
// offer and selection and the offer's types count.
func TestImageIgnoresOtherEvents(t *testing.T) {
	fakeCompositor(t, t.TempDir(), []string{"image/jpeg"}, "JPG", "noisy")
	b, typ, err := Image()
	if err != nil || typ != "image/jpeg" || string(b) != "JPG|image/jpeg" {
		t.Fatalf("%q %q %v", b, typ, err)
	}
}

// TestPrimaryWayland: a middle click's text is the primary selection's,
// UTF-8 first; one desk takes is handed to the reader that asks, until
// another takes it. ext-data-control numbers its requests apart.
func TestPrimaryWayland(t *testing.T) {
	fakeCompositor(t, t.TempDir(), []string{"image/png", "STRING", "text/plain;charset=utf-8"}, "hi")
	if got := Primary(); got != "primary:hi|text/plain;charset=utf-8" {
		t.Errorf("primary: %q", got)
	}
	fakeCompositor(t, t.TempDir(), []string{"image/png"}, "")
	if got, err := readPrimary(); got != nil || err != nil {
		t.Errorf("no text in it: %q %v", got, err)
	}
	defer func(n int) { serveRead = n }(serveRead)
	for i, mode := range []string{"", "ext"} {
		serveRead = []int{4096, 6}[i] // the second in pieces: a message is read whole first
		served := fakeCompositor(t, t.TempDir(), nil, "", mode)
		done := make(chan struct{})
		ended := make(chan error, 1)
		go func() { ended <- ownWayland("ls -la", done) }()
		select {
		case got := <-served:
			if got != "ls -la|2 destroyed" { // the clipboard's and the primary selection's offers
				t.Errorf("%q: served %q", mode, got)
			}
		case <-time.After(testwait.Long):
			t.Fatalf("%q: nothing served", mode)
		}
		if err := <-ended; err != nil { // cancelled: another took it
			t.Errorf("%q: %v", mode, err)
		}
	}
}

// TestPrimaryWaylandFallback: without a primary selection to read (an old
// wlroots manager, no compositor at all), desk's own last selection stands
// in; taking one fails the same way, as does the compositor's error.
func TestPrimaryWaylandFallback(t *testing.T) {
	fakeCompositor(t, t.TempDir(), []string{"STRING"}, "theirs", "v1")
	owner.Lock()
	owner.text = "mine"
	owner.Unlock()
	if got := Primary(); got != "mine" {
		t.Errorf("v1: %q", got)
	}
	fakeCompositor(t, t.TempDir(), nil, "", "v1")
	if err := ownWayland("x", nil); err != errNoPrimary {
		t.Errorf("own on v1: %v", err)
	}
	fakeCompositor(t, t.TempDir(), nil, "", "error-device")
	if err := ownWayland("x", nil); err == nil {
		t.Error("own on a device error")
	}
	fakeCompositor(t, t.TempDir(), nil, "", "serve-error")
	if err := ownWayland("x", make(chan struct{})); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("an error while serving: %v", err)
	}
	fakeCompositor(t, t.TempDir(), nil, "", "bad-send")
	if err := ownWayland("x", make(chan struct{})); err == nil || !strings.Contains(err.Error(), "bad message") {
		t.Errorf("a bad message while serving: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // no compositor
	if err := ownWayland("x", nil); err == nil {
		t.Error("own with no compositor")
	}
	if _, err := readPrimary(); err == nil {
		t.Error("read with no compositor")
	}
}

// TestPrimaryStops: done ends serving; SetPrimary replaces the selection
// it serves, stopping the last.
func TestPrimaryStops(t *testing.T) {
	taken := fakeCompositor(t, t.TempDir(), nil, "", "silent-take")
	done := make(chan struct{})
	ended := make(chan error, 1)
	go func() { ended <- ownWayland("x", done) }()
	<-taken
	close(done)
	select {
	case err := <-ended:
		if err == nil {
			t.Error("stopped: no error from the closed connection")
		}
	case <-time.After(testwait.Long):
		t.Fatal("done didn't stop it")
	}
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // nowhere to serve: owner.text stays the fallback
	SetPrimary("one")
	SetPrimary("two")
	if got := Primary(); got != "two" {
		t.Errorf("fallback: %q", got)
	}
	owner.serving.Wait() // none left to dial another test's compositor
	owner.Lock()
	close(owner.stop)
	owner.stop = nil
	owner.Unlock()
}

// TestPrimaryOrder: a call already stopped never takes the selection, and
// an older SetPrimary waiting its turn gives way to the newer one.
func TestPrimaryOrder(t *testing.T) {
	served := fakeCompositor(t, t.TempDir(), nil, "")
	done := make(chan struct{})
	close(done)
	ownWayland("stale", done) // closed or not yet, it returns without taking it
	select {
	case got := <-served:
		t.Errorf("a stopped call took it: %q", got)
	case <-time.After(100 * time.Millisecond):
	}
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // nowhere to serve
	owner.turn.Lock()                        // an owner still letting go
	SetPrimary("older")
	SetPrimary("newer") // stops the older before it had its turn
	owner.turn.Unlock()
	owner.serving.Wait() // the newer tries, finds nowhere to serve, ends
	owner.Lock()
	close(owner.stop)
	owner.stop = nil
	owner.Unlock()
	if got := Primary(); got != "newer" {
		t.Errorf("fallback: %q", got)
	}
}

// TestNewID: ids count up from where the connection left off.
func TestNewID(t *testing.T) {
	w := &conn{next: 2}
	if a, b := w.newID(), w.newID(); a != 2 || b != 3 {
		t.Errorf("ids %d, %d, want 2, 3", a, b)
	}
}
