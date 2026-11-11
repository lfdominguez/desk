package vt

// desk patch: Home and End follow application cursor mode (DECCKM);
// synchronized output (mode 2026) is known and reported.

import (
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
)

func TestHomeEndCursorMode(t *testing.T) {
	for _, tc := range []struct {
		mode string
		key  rune
		want string
	}{
		{"", uv.KeyHome, "\x1b[H"},
		{"", uv.KeyEnd, "\x1b[F"},
		{"\x1b[?1h", uv.KeyHome, "\x1bOH"},
		{"\x1b[?1h", uv.KeyEnd, "\x1bOF"},
	} {
		e := NewEmulator(10, 3)
		e.WriteString(tc.mode)
		go e.SendKey(uv.KeyPressEvent{Code: tc.key})
		buf := make([]byte, 16)
		n, _ := e.Read(buf)
		if got := string(buf[:n]); got != tc.want {
			t.Errorf("mode %q key %d: %q, want %q", tc.mode, tc.key, got, tc.want)
		}
		e.Close()
	}
}

func TestSynchronized(t *testing.T) {
	e := NewEmulator(10, 3)
	defer e.Close()
	go e.WriteString("\x1b[?2026$p") // DECRQM: reset (2), not unknown (0)
	buf := make([]byte, 32)
	n, _ := e.Read(buf)
	if got := string(buf[:n]); got != "\x1b[?2026;2$y" {
		t.Errorf("DECRQM 2026: %q", got)
	}
	if e.Synchronized() {
		t.Error("synchronized at start")
	}
	e.WriteString("\x1b[?2026h")
	if !e.Synchronized() {
		t.Error("BSU: not synchronized")
	}
	e.WriteString("\x1b[?2026l")
	if e.Synchronized() {
		t.Error("ESU: still synchronized")
	}
	defer func(m time.Duration) { SyncMax = m }(SyncMax)
	SyncMax = 0
	e.WriteString("\x1b[?2026h")
	if e.Synchronized() {
		t.Error("held past SyncMax")
	}
}
