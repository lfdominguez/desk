package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/lfdominguez/desk/internal/telemetry"
)

// stall watches a window's goroutine while it is away from Gio's event
// reading. On Wayland Gio answers the compositor's pings only from there
// (Window.Event dispatches the display), so a few seconds away is "desk is
// not responding" in Hyprland, though nothing is stuck once it returns.
// Past stallAfter, check notes in crash.log what the goroutine was doing,
// and every goroutine's stack in stall.log beside it.
type stall struct {
	u     *ui
	name  string       // the window: "main" or a card's key
	since atomic.Int64 // when it left the event reading, unix nanoseconds; 0 while there
	what  atomic.Pointer[string]
	noted int64 // the since check already noted; check's goroutine only
	quit  chan struct{}
}

// stallAfter is how long away from the events check notes: under the
// seconds a compositor waits for a ping's answer.
var stallAfter = 3 * time.Second

// watchStall starts name's window's watch; stop ends it.
func (u *ui) watchStall(name string) *stall {
	s := &stall{u: u, name: name, quit: make(chan struct{})}
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-s.quit:
				return
			case now := <-t.C:
				s.check(now)
			}
		}
	}()
	return s
}

func (s *stall) stop() { close(s.quit) }

// busy says what the goroutine is doing away from the events, from when
// it first left them.
func (s *stall) busy(what string) {
	s.what.Store(&what)
	if s.since.Load() == 0 {
		s.since.Store(time.Now().UnixNano())
	}
}

// idle is the goroutine back at the events.
func (s *stall) idle() { s.since.Store(0) }

// check notes a stall past stallAfter once, and when it has ended.
func (s *stall) check(now time.Time) {
	since := s.since.Load()
	if s.noted != 0 && since != s.noted { // the stall noted is over
		s.u.crashNote("window %s back at its events", s.name)
		s.noted = 0
	}
	if since == 0 || since == s.noted || now.Sub(time.Unix(0, since)) < stallAfter {
		return
	}
	s.noted = since
	what := "?"
	if w := s.what.Load(); w != nil {
		what = *w
	}
	d := now.Sub(time.Unix(0, since)).Round(time.Second)
	s.u.crashNote("window %s away from its events %s, the compositor's pings unanswered: %s (stacks in stall.log)", s.name, d, what)
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	head := fmt.Sprintf("%s window %s, %s: %s\n\n", now.Format(time.RFC3339), s.name, d, what)
	os.WriteFile(filepath.Join(filepath.Dir(s.u.path), "stall.log"), append([]byte(head), buf...), 0o600)
	telemetry.Stall(s.name, what, d, string(buf))
}
