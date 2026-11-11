package ui

import (
	"errors"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/io/event"
	"gioui.org/op"
	"gioui.org/unit"
)

type wakeup struct{} // stands in for Gio's own, unexported

func (wakeup) ImplementsEvent() {}

func TestWindowEvents(t *testing.T) {
	h := newHarness(t)
	ran, framed := 0, false
	fe := app.FrameEvent{Now: time.Now(), Size: image.Pt(1200, 800), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Source: h.r.Source(),
		Frame: func(*op.Ops) { framed = true }}
	evs := []event.Event{wakeup{}, app.ConfigEvent{Config: app.Config{Focused: true}}, fe, app.DestroyEvent{}, wakeup{}}
	checks := []func(){
		func() { h.u.later(func() { ran++ }) },
		func() {
			if ran != 1 {
				t.Errorf("a wakeup runs the queued work without a frame: %d", ran)
			}
		},
		func() {
			if !h.u.focused {
				t.Error("focus followed")
			}
			h.u.later(func() { h.u.termGen.Add(1) }) // terminal output during the frame: one more is asked for
		},
		func() {
			if !framed {
				t.Error("a frame draws")
			}
		},
	}
	i := 0
	err := h.u.loop(func() event.Event {
		checks[i]()
		i++
		return evs[i-1]
	})
	if err != nil || i != 4 {
		t.Errorf("closed: %v after %d", err, i)
	}
}

func TestWindowReopens(t *testing.T) {
	h := newHarness(t)
	ended := make(chan time.Time)
	defer func(w func() <-chan time.Time) { reopenWait = w }(reopenWait)
	reopenWait = func() <-chan time.Time { return ended }
	ran, reopened := 0, 0
	h.u.reopen = func() { reopened++ }
	lost := errors.New("wayland: protocol error 0 on wl_surface@3")
	fe := app.FrameEvent{Now: time.Now(), Size: image.Pt(1200, 800), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Source: h.r.Source(),
		Frame: func(*op.Ops) {}}
	evs := []event.Event{app.DestroyEvent{Err: lost}, fe, app.DestroyEvent{}}
	i := 0
	h.u.later(func() { ran++; close(ended) }) // work queued while the window is gone runs during the wait
	err := h.u.loop(func() event.Event { i++; return evs[i-1] })
	if err != nil || i != 3 || ran != 1 || reopened != 1 {
		t.Errorf("lost then closed: %v after %d events, work ran %d, reopened %d", err, i, ran, reopened)
	}
	log, _ := os.ReadFile(filepath.Join(filepath.Dir(h.u.path), "crash.log"))
	if !strings.Contains(string(log), "window lost, reopening: "+lost.Error()) {
		t.Errorf("crash.log: %q", log)
	}
}

func TestWindowReopenDefaults(t *testing.T) {
	h := newHarness(t)
	h.u.reopen() // a window not open yet keeps the options for when it opens
	if reopenWait() == nil {
		t.Error("the wait before reopening is a timer")
	}
}

func TestWindowReopenGivesUp(t *testing.T) {
	h := newHarness(t)
	defer func(w func() <-chan time.Time) { reopenWait = w }(reopenWait)
	ended := make(chan time.Time)
	close(ended)
	reopenWait = func() <-chan time.Time { return ended }
	lost := errors.New("wayland: no outputs available")
	n := 0
	err := h.u.loop(func() event.Event { n++; return app.DestroyEvent{Err: lost} })
	if err != lost || n != reopenTries+1 {
		t.Errorf("a window that never comes back: %v after %d", err, n)
	}
	log, _ := os.ReadFile(filepath.Join(filepath.Dir(h.u.path), "crash.log"))
	if strings.Count(string(log), "\n") != 2 || !strings.Contains(string(log), "window lost for good: "+lost.Error()) {
		t.Errorf("crash.log notes the first loss and the end only: %q", log)
	}
}
