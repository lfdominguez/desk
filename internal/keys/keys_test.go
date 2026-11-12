package keys

import (
	"slices"
	"strings"
	"testing"

	"gioui.org/io/key"
)

// TestDefaultsKeepTheRule is the guard: every default carries Ctrl or Alt,
// is a key that doesn't type, or is a bare key of a binding that fires only
// outside text fields.
func TestDefaultsKeepTheRule(t *testing.T) {
	for _, b := range All {
		for _, c := range b.Def {
			if err := Check(&b, c); err != nil {
				t.Errorf("%s: %s: %v", b.ID, c, err)
			}
		}
		if (b.Fixed == "") == (len(b.Def) == 0) {
			t.Errorf("%s: a binding has defaults or Fixed, not both", b.ID)
		}
		if !slices.Contains(Scopes, b.Scope) {
			t.Errorf("%s: scope %q isn't in Scopes", b.ID, b.Scope)
		}
	}
}

// TestDefaultsDontClash: no two shortcuts of a scope share a default key.
func TestDefaultsDontClash(t *testing.T) {
	for i := range All {
		b := &All[i]
		for _, c := range b.Def {
			if o := Clash(b, c, Of); o != nil {
				t.Errorf("%s and %s both default to %s", b.ID, o.ID, c)
			}
		}
	}
}

func TestCheckRefusesShiftAlone(t *testing.T) {
	b := &Binding{ID: "x", Def: []Chord{{"2", key.ModShift}}}
	for _, c := range []Chord{{"2", key.ModShift}, {"1", key.ModShift}, {key.NameUpArrow, key.ModShift}, {key.NameReturn, key.ModShift}, {"F6", key.ModShift}} {
		if err := Check(b, c); err == nil || !strings.Contains(err.Error(), "Shift") {
			t.Errorf("%s: %v, want Shift refused", c, err)
		}
	}
	if err := Check(b, Chord{"A", 0}); err == nil {
		t.Error("a bare letter passed where text is typed")
	}
	if err := Check(b, Chord{"A", key.ModSuper}); err == nil {
		t.Error("Super alone passed")
	}
	if err := Check(b, Chord{key.NameShift, key.ModShift}); err == nil {
		t.Error("a modifier on its own passed")
	}
	if err := Check(b, Chord{"", key.ModCtrl}); err == nil {
		t.Error("no key passed")
	}
	if c, _ := Parse("Ctrl+↵"); c.Name != key.NameReturn {
		t.Errorf("↵ is %q", c.Name)
	}
	for _, c := range []Chord{{"A", key.ModCtrl}, {"A", key.ModAlt}, {"A", key.ModCommand}, {"Z", key.ModCtrl | key.ModShift}, {"F2", 0}, {key.NameDeleteForward, 0}} {
		if err := Check(b, c); err != nil {
			t.Errorf("%s: %v", c, err)
		}
	}
	if err := Check(&Binding{Bare: true}, Chord{"E", 0}); err != nil {
		t.Errorf("a bare binding's letter: %v", err)
	}
	if err := Check(&Binding{}, Chord{key.NameSpace, 0}); err == nil {
		t.Error("Space types; it needs a bare binding")
	}
}

func TestParseRoundTrip(t *testing.T) {
	for _, b := range All {
		for _, c := range b.Def {
			got, err := Parse(c.String())
			if err != nil || got != c && !(c.Mods&key.ModShortcut == key.ModShortcut && got == c) {
				t.Errorf("%s: Parse(%q) = %v, %v", b.ID, c.String(), got, err)
			}
		}
	}
	for s, want := range map[string]Chord{
		"ctrl+shift+t": {"T", key.ModCtrl | key.ModShift}, "Cmd+K": {"K", key.ModCommand}, "Alt+Return": {key.NameReturn, key.ModAlt},
		"Ctrl++": {"+", key.ModCtrl}, "+": {"+", 0}, "f12": {"F12", 0}, "Shortcut+N": {"N", key.ModShortcut}, "Del": {key.NameDeleteForward, 0},
		"option+⌫": {key.NameDeleteBackward, key.ModAlt}, "e": {"E", 0}, "Control+PgDn": {key.NamePageDown, key.ModCtrl},
	} {
		if got, err := Parse(s); err != nil || got != want {
			t.Errorf("Parse(%q) = %v, %v; want %v", s, got, err, want)
		}
	}
	for _, s := range []string{"", "Ctrl+", "Hyper+K", "Ctrl+Nope", "F13", "F0", "F1x"} {
		if c, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) = %v, want an error", s, c)
		}
	}
}

func TestText(t *testing.T) {
	for c, want := range map[Chord][3]string{
		{"T", key.ModCtrl | key.ModShift}: {"Ctrl+Shift+T", "Ctrl+Shift+T", "⌃⇧T"},
		{"K", key.ModCommand}:             {"Cmd+K", "⌘K", "⌘K"},
		{key.NameReturn, key.ModAlt}:      {"Alt+Enter", "Alt+↵", "⌥↵"},
		{"F2", 0}:                         {"F2", "F2", "F2"},
	} {
		if got := [3]string{c.String(), c.Text(), c.Short()}; got != want {
			t.Errorf("%v: %q, want %q", c, got, want)
		}
	}
	if got := Caps("draw.select"); got != "V or 1" {
		t.Errorf("Caps = %q", got)
	}
	if Cap("board.select") != "↑ ↓ ← →" || Caps("arch.move") != "↑ ↓ ↵" {
		t.Error("a fixed binding's caps are its Fixed")
	}
	if Short("files.rename") != "F2" {
		t.Error("Short")
	}
}

func TestMatch(t *testing.T) {
	ev := func(n key.Name, m key.Modifiers) key.Event { return key.Event{Name: n, Modifiers: m, State: key.Press} }
	if !Is("zoom.in", ev("+", key.ModShortcut|key.ModShift)) {
		t.Error("Ctrl++ made with Shift isn't zoom in")
	}
	if Is("board.new", ev("N", key.ModShortcut|key.ModShift)) {
		t.Error("Ctrl+Shift+N ran Ctrl+N: a letter's Shift counts")
	}
	if Is("board.new", key.Event{Name: "N", Modifiers: key.ModShortcut, State: key.Release}) {
		t.Error("a release ran it")
	}
	if Is("arch.check", ev(key.NameSpace, key.ModShift)) {
		t.Error("Shift+Space ran Space")
	}
	if Is("card.archive", ev(key.NameDeleteBackward, key.ModShift)) || Is("arch.delete", ev(key.NameDeleteForward, key.ModShift)) {
		t.Error("Shift+Backspace or Shift+Delete ran the bare key: Gio's one-symbol names aren't symbols")
	}
	if !Is("draw.front", ev("}", key.ModShortcut|key.ModShift)) || Is("draw.forward", ev("Q", key.ModShortcut)) {
		t.Error("draw.front")
	}
	if !Is("sheet.commit", ev(key.NameEnter, key.ModShortcut)) || len(Filters(nil, "text.newline")) != 2 {
		t.Error("the keypad's Enter isn't Return")
	}
	fs := Filters(nil, "zoom.in", "board.new")
	if len(fs) != 3 {
		t.Fatalf("filters %v", fs)
	}
	if f := fs[0].(key.Filter); f.Optional != key.ModShift || f.Required != key.ModShortcut || f.Name != "=" {
		t.Errorf("a symbol's filter %+v", f)
	}
	if f := fs[2].(key.Filter); f.Optional != 0 {
		t.Errorf("a letter's filter %+v", f)
	}
}

func TestRebind(t *testing.T) {
	t.Cleanup(func() { Load(nil) })
	Set("board.new", Chord{"B", key.ModAlt})
	if !Changed("board.new") || Cap("board.new") != "Alt+B" || Is("board.new", key.Event{Name: "N", Modifiers: key.ModShortcut, State: key.Press}) {
		t.Error("the rebinding didn't take over")
	}
	if got := Overrides(); len(got) != 1 || got["board.new"] != "Alt+B" {
		t.Errorf("Overrides = %v", got)
	}
	Set("board.new", ctl("N")) // back to the default: no override kept
	if Changed("board.new") {
		t.Error("setting the default kept an override")
	}
	Set("draw.select", Chord{"V", 0}) // one of two defaults: an override
	if !Changed("draw.select") || len(Of("draw.select")) != 1 {
		t.Error("one of several defaults")
	}
	Reset("draw.select")
	if Changed("draw.select") || len(Of("draw.select")) != 2 {
		t.Error("Reset")
	}
}

func TestLoad(t *testing.T) {
	t.Cleanup(func() { Load(nil) })
	err := Load(map[string]string{"board.new": "Alt+B", "nope": "Ctrl+X", "escape": "Ctrl+Q", "undo": "Shift+2", "palette": "Hyper+K"})
	if err == nil || !strings.Contains(err.Error(), "nope") || !strings.Contains(err.Error(), "escape") || !strings.Contains(err.Error(), "undo") || !strings.Contains(err.Error(), "palette") {
		t.Errorf("Load error %v", err)
	}
	if Cap("board.new") != "Alt+B" || Changed("undo") || Changed("palette") {
		t.Error("Load kept the bad ones or lost the good one")
	}
	if Load(map[string]string{}) != nil || Changed("board.new") {
		t.Error("Load replaces the rebindings")
	}
}

func TestClash(t *testing.T) {
	t.Cleanup(func() { Load(nil) })
	if c := Clash(Get("card.edit"), ctl("N"), Of); c == nil || c.ID != "board.new" {
		t.Errorf("Clash = %v", c)
	}
	if c := Clash(Get("card.edit"), ctl("S"), Of); c != nil {
		t.Errorf("another scope's Ctrl+S clashed: %v", c.ID)
	}
	if c := Clash(Get("board.new"), ctl("N"), Of); c != nil {
		t.Error("a binding clashed with itself")
	}
	if c := Clash(Get("card.edit"), Chord{"1", 0}, Of); c != nil {
		t.Error("a fixed binding clashed")
	}
	Set("undo", Chord{"Q", key.ModAlt})
	if c := Clash(Get("board.new"), Chord{"Q", key.ModAlt}, Of); c == nil || c.ID != "undo" {
		t.Error("a clash with a rebinding")
	}
	draft := func(id string) []Chord { // a draft moves board.new away: Ctrl+N is free
		if id == "board.new" {
			return []Chord{{"B", key.ModAlt}}
		}
		return Of(id)
	}
	if c := Clash(Get("card.edit"), ctl("N"), draft); c != nil {
		t.Error("the draft wasn't read")
	}
}

func TestRecord(t *testing.T) {
	for _, tc := range []struct {
		e    key.Event
		want Chord
		ok   bool
	}{
		{key.Event{Name: "@", Modifiers: key.ModShift, State: key.Press}, Chord{"@", 0}, true},
		{key.Event{Name: "T", Modifiers: key.ModCtrl | key.ModShift, State: key.Press}, Chord{"T", key.ModCtrl | key.ModShift}, true},
		{key.Event{Name: "T", Modifiers: key.ModCtrl | key.ModSuper, State: key.Press}, Chord{"T", key.ModCtrl}, true},
		{key.Event{Name: key.NameShift, Modifiers: key.ModShift, State: key.Press}, Chord{}, false},
		{key.Event{Name: key.NameEnter, Modifiers: key.ModAlt, State: key.Press}, Chord{key.NameReturn, key.ModAlt}, true},
		{key.Event{Name: "T", State: key.Release}, Chord{}, false},
	} {
		if got, ok := Record(tc.e); got != tc.want || ok != tc.ok {
			t.Errorf("Record(%v) = %v, %v", tc.e, got, ok)
		}
	}
}

func TestGetUnknown(t *testing.T) {
	if Get("nope") != nil {
		t.Error("Get")
	}
	defer func() {
		if recover() == nil {
			t.Error("Of an unknown id didn't panic")
		}
	}()
	Of("nope")
}
