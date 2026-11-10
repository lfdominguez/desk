package infra

import (
	"image"
	"reflect"
	"strings"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"github.com/lfdominguez/desk/internal/keys"
)

// opsLen is how much a frame drew.
func opsLen(o *op.Ops) int {
	return reflect.ValueOf(&o.Internal).Elem().FieldByName("data").Len()
}

// TestToolbarTips: each toolbar icon has a tip, with its key when it has
// one, drawn only while the pointer is on it.
func TestToolbarTips(t *testing.T) {
	h := newGM(t)
	u := h.u
	h.set(&guiState{Tabs: gmTabs("Acme", 2), Active: 1})
	u.org = "Acme"
	w := func(gtx layout.Context) { u.toolbar(gtx) }
	h.move(600, 400) // off the bar
	h.draw(w)
	plain := opsLen(&h.ops)
	for id, keyed := range map[string]bool{"tbPane": true, "tbBroadcast": false, "tbFiles": true, "tbGroup": true, "tbClaude": true} {
		if s := tbTips(id); s == "" || strings.Contains(s, keys.Chord{Mods: keys.Mod}.Short()) != keyed {
			t.Errorf("%s: tip %q", id, s)
		}
		h.hover(u.click(id), image.Rect(600, 0, 1200, 34), 3, w)
		if n := opsLen(&h.ops); n <= plain {
			t.Errorf("%s hovered: no tip drawn (%d ≤ %d)", id, n, plain)
		}
		h.move(600, 400)
		h.draw(w)
		if n := opsLen(&h.ops); n != plain {
			t.Errorf("%s left: the tip stays (%d ≠ %d)", id, n, plain)
		}
	}
	if s := tbTips("tbNone"); s != "" {
		t.Errorf("an unknown button's tip %q", s)
	}
}
