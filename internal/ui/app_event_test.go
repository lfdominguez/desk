package ui

import (
	"errors"
	"image"
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
	boom := errors.New("boom")
	fe := app.FrameEvent{Now: time.Now(), Size: image.Pt(1200, 800), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Source: h.r.Source(),
		Frame: func(*op.Ops) { framed = true }}
	evs := []event.Event{wakeup{}, app.ConfigEvent{Config: app.Config{Focused: true}}, fe, app.DestroyEvent{Err: boom}, wakeup{}}
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
	if err != boom || i != 4 {
		t.Errorf("closed: %v after %d", err, i)
	}
}
