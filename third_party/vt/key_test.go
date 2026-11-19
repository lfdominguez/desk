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

func TestModifiedKeys(t *testing.T) {
	for _, tc := range []struct {
		key  uv.KeyPressEvent
		want string
	}{
		{uv.KeyPressEvent{Code: uv.KeyLeft, Mod: uv.ModCtrl}, "\x1b[1;5D"},
		{uv.KeyPressEvent{Code: uv.KeyRight, Mod: uv.ModCtrl}, "\x1b[1;5C"},
		{uv.KeyPressEvent{Code: uv.KeyUp, Mod: uv.ModShift}, "\x1b[1;2A"},
		{uv.KeyPressEvent{Code: uv.KeyDown, Mod: uv.ModAlt}, "\x1b[1;3B"},
		{uv.KeyPressEvent{Code: uv.KeyLeft, Mod: uv.ModCtrl | uv.ModShift}, "\x1b[1;6D"},
		{uv.KeyPressEvent{Code: uv.KeyHome, Mod: uv.ModCtrl}, "\x1b[1;5H"},
		{uv.KeyPressEvent{Code: uv.KeyEnd, Mod: uv.ModShift}, "\x1b[1;2F"},
		{uv.KeyPressEvent{Code: uv.KeyF1, Mod: uv.ModCtrl}, "\x1b[1;5P"},
		{uv.KeyPressEvent{Code: uv.KeyDelete, Mod: uv.ModCtrl}, "\x1b[3;5~"},
		{uv.KeyPressEvent{Code: uv.KeyPgUp, Mod: uv.ModShift}, "\x1b[5;2~"},
		{uv.KeyPressEvent{Code: uv.KeyF5, Mod: uv.ModAlt | uv.ModCtrl}, "\x1b[15;7~"},
		{uv.KeyPressEvent{Code: uv.KeyF12, Mod: uv.ModMeta}, "\x1b[24;9~"},
		{uv.KeyPressEvent{Code: uv.KeyBackspace, Mod: uv.ModCtrl}, "\x08"},
		{uv.KeyPressEvent{Code: uv.KeyBackspace, Mod: uv.ModAlt}, "\x1b\x7f"},
		{uv.KeyPressEvent{Code: uv.KeyLeft}, "\x1b[D"}, // bare: the table's
		{uv.KeyPressEvent{Code: 'b', Mod: uv.ModAlt, Text: "b"}, "\x1bb"},
	} {
		e := NewEmulator(10, 3)
		go e.SendKey(tc.key)
		buf := make([]byte, 16)
		n, _ := e.Read(buf)
		if got := string(buf[:n]); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.key, got, tc.want)
		}
		e.Close()
	}
}
