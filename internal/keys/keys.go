// Package keys is every keyboard shortcut desk and its infra workspace
// have: the registry the handlers match against, what the buttons' key caps
// read, and the user's rebindings (settings.toml's [keys]). It imports no
// GUI code beyond Gio's key names, so ui and infra both use it.
package keys

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"gioui.org/io/event"
	"gioui.org/io/key"
)

// Chord is one key with its modifiers.
// ponytail: one chord per press, no sequences (Ctrl+K then S); add a
// pending-prefix state to Is when someone asks for them.
type Chord struct {
	Name key.Name
	Mods key.Modifiers
}

// Binding is a shortcut: its ID (the [keys] name), where it works, what it
// does and its default chords (the first is the one key caps show). A
// binding with Fixed set can't change: Fixed says its keys.
type Binding struct {
	ID, Scope, What string
	Def             []Chord
	Bare            bool   // a bare printable key may run it (it fires only outside text fields)
	Fixed           string // the keys of a binding that can't change ("↑ ↓", "1…9")
}

var (
	mu   sync.RWMutex
	over = map[string]Chord{}
	byID = map[string]*Binding{}
)

func init() {
	for i := range All {
		byID[All[i].ID] = &All[i]
	}
}

// Get is binding id, or nil.
func Get(id string) *Binding { return byID[id] }

// Of is id's chords now: the user's one, else the defaults.
func Of(id string) []Chord {
	mu.RLock()
	c, ok := over[id]
	mu.RUnlock()
	if ok {
		return []Chord{c}
	}
	b := byID[id]
	if b == nil {
		panic("keys: no binding " + id)
	}
	return b.Def
}

// Filters are id's chords as Gio filters, on focus (nil: focus-less).
func Filters(focus event.Tag, ids ...string) []event.Filter {
	var fs []event.Filter
	for _, id := range ids {
		for _, c := range Of(id) {
			f := key.Filter{Focus: focus, Name: c.Name, Required: c.Mods}
			if loose(c) {
				f.Optional = key.ModShift
			}
			fs = append(fs, f)
			if c.Name == key.NameReturn { // the keypad's Enter too
				f.Name = key.NameEnter
				fs = append(fs, f)
			}
		}
	}
	return fs
}

// Is reports whether e is a press of one of id's chords.
func Is(id string, e key.Event) bool {
	if e.State != key.Press {
		return false
	}
	for _, c := range Of(id) {
		if c.Match(e) {
			return true
		}
	}
	return false
}

// Match reports whether e is c, whatever its state. A symbol or digit
// takes Shift as part of making it (Ctrl++ is Ctrl+Shift+= on many
// layouts, and Gio names it by what Shift makes), so Shift doesn't count
// there unless c asks for it.
func (c Chord) Match(e key.Event) bool {
	if e.Name == key.NameEnter { // the keypad's Enter is Return
		e.Name = key.NameReturn
	}
	if e.Name != c.Name {
		return false
	}
	m := e.Modifiers
	if loose(c) {
		m &^= key.ModShift
	}
	return m == c.Mods
}

// loose is a chord whose Shift is the layout's business: a symbol or digit
// with no Shift asked for.
func loose(c Chord) bool {
	r, n := utf8.DecodeRuneInString(string(c.Name))
	return n == len(c.Name) && r != utf8.RuneError && !unicode.IsLetter(r) && c.Mods&key.ModShift == 0 && c.Name != key.NameSpace && !named(c.Name)
}

// Set makes c id's one chord (the defaults' when it's one of them).
func Set(id string, c Chord) {
	mu.Lock()
	defer mu.Unlock()
	if d := byID[id].Def; len(d) == 1 && d[0] == c {
		delete(over, id)
		return
	}
	over[id] = c
}

// Reset puts id back to its defaults.
func Reset(id string) {
	mu.Lock()
	delete(over, id)
	mu.Unlock()
}

// Changed reports whether the user rebound id.
func Changed(id string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := over[id]
	return ok
}

// Overrides are the rebindings, as settings.toml's [keys] keeps them.
func Overrides() map[string]string {
	mu.RLock()
	defer mu.RUnlock()
	m := map[string]string{}
	for id, c := range over {
		m[id] = c.String()
	}
	return m
}

// Load replaces the rebindings with m's; an unknown id, a chord that
// doesn't parse or breaks the rule is skipped, and named in the error.
func Load(m map[string]string) error {
	next := map[string]Chord{}
	var errs []error
	for id, s := range m {
		b := byID[id]
		if b == nil || b.Fixed != "" {
			errs = append(errs, fmt.Errorf("keys: no shortcut %q", id))
			continue
		}
		c, err := Parse(s)
		if err == nil {
			err = Check(b, c)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("keys: %s = %q: %w", id, s, err))
			continue
		}
		next[id] = c
	}
	mu.Lock()
	over = next
	mu.Unlock()
	return errors.Join(errs...)
}

// Check is the rule every shortcut keeps: a modifier comes with Ctrl or
// Alt (⌘ counts as Ctrl), since Shift alone types: Shift+2 is "@" on many
// layouts. A bare printable key only runs a binding that fires outside text
// fields (Bare).
func Check(b *Binding, c Chord) error {
	if c.Name == "" || modName(c.Name) {
		return errors.New("press a key with the modifiers")
	}
	if c.Mods&(key.ModCtrl|key.ModAlt|key.ModCommand) != 0 {
		return nil
	}
	if c.Mods&key.ModShift != 0 {
		return errors.New("Shift needs Ctrl or Alt with it: on its own it types")
	}
	if c.Mods != 0 {
		return errors.New("add Ctrl or Alt")
	}
	if printable(c.Name) && !b.Bare {
		return errors.New("a key that types needs Ctrl or Alt here")
	}
	return nil
}

// printable is a key that types a character. Gio names some keys that
// don't by one symbol (⌫, ⏎, ↑), so those are looked up first.
func printable(n key.Name) bool {
	if n == key.NameSpace {
		return true
	}
	r, size := utf8.DecodeRuneInString(string(n))
	return !named(n) && size == len(n) && r != utf8.RuneError
}

// named is one of Gio's names for a key that doesn't type.
func named(n key.Name) bool {
	for _, k := range names {
		if k.n == n {
			return n != key.NameSpace
		}
	}
	return false
}

func modName(n key.Name) bool {
	switch n {
	case key.NameShift, key.NameCtrl, key.NameAlt, key.NameSuper, key.NameCommand:
		return true
	}
	return false
}

// Clash is the binding in b's scope that c already runs, or nil; of is
// each binding's chords (Of, or a dialog's draft over it).
func Clash(b *Binding, c Chord, of func(id string) []Chord) *Binding {
	for i := range All {
		o := &All[i]
		if o == b || o.Scope != b.Scope || o.Fixed != "" {
			continue
		}
		for _, oc := range of(o.ID) {
			if oc == c {
				return o
			}
		}
	}
	return nil
}

// Record is the chord a press in the shortcuts dialog makes: a symbol's
// Shift dropped (it is in the symbol already), ok false for a release or a
// modifier on its own.
func Record(e key.Event) (Chord, bool) {
	if e.State != key.Press || modName(e.Name) {
		return Chord{}, false
	}
	c := Chord{e.Name, e.Modifiers &^ key.ModSuper}
	if c.Name == key.NameEnter {
		c.Name = key.NameReturn
	}
	if loose(Chord{c.Name, 0}) {
		c.Mods &^= key.ModShift
	}
	return c, true
}

// names are how special keys read in settings.toml (first) and on caps.
var names = []struct {
	n      key.Name
	word   string
	cap    string
	others []string
}{
	{key.NameReturn, "Enter", "↵", []string{"return", "↵", "⏎"}},
	{key.NameEnter, "KeypadEnter", "↵", []string{"⌤"}},
	{key.NameEscape, "Esc", "esc", []string{"escape"}},
	{key.NameTab, "Tab", "Tab", nil},
	{key.NameSpace, "Space", "Space", nil},
	{key.NameDeleteBackward, "Backspace", "Backspace", []string{"⌫"}},
	{key.NameDeleteForward, "Delete", "Del", []string{"del", "⌦"}},
	{key.NameUpArrow, "Up", "↑", []string{"↑"}},
	{key.NameDownArrow, "Down", "↓", []string{"↓"}},
	{key.NameLeftArrow, "Left", "←", []string{"←"}},
	{key.NameRightArrow, "Right", "→", []string{"→"}},
	{key.NamePageUp, "PageUp", "PgUp", []string{"pgup"}},
	{key.NamePageDown, "PageDown", "PgDn", []string{"pgdn"}},
	{key.NameHome, "Home", "Home", nil},
	{key.NameEnd, "End", "End", nil},
}

var mods = []struct {
	m         key.Modifiers
	word, sym string
	others    []string
}{
	{key.ModCtrl, "Ctrl", "⌃", []string{"control"}},
	{key.ModAlt, "Alt", "⌥", []string{"option", "opt"}},
	{key.ModShift, "Shift", "⇧", nil},
	{key.ModCommand, "Cmd", "⌘", []string{"command", "⌘"}},
	{key.ModSuper, "Super", "◆", nil},
}

// String is c as settings.toml keeps it: "Ctrl+Shift+T", "Alt+Enter".
func (c Chord) String() string {
	var b strings.Builder
	for _, m := range mods {
		if c.Mods.Contain(m.m) {
			b.WriteString(m.word + "+")
		}
	}
	return b.String() + name(c.Name, false)
}

// Text is c on a key cap: "Ctrl+↵", "⌘K" on macOS.
func (c Chord) Text() string {
	var b strings.Builder
	for _, m := range mods {
		if !c.Mods.Contain(m.m) {
			continue
		}
		if m.m == key.ModCommand {
			b.WriteString(m.sym)
		} else {
			b.WriteString(m.word + "+")
		}
	}
	return b.String() + name(c.Name, true)
}

// Short is c in modifier symbols: "⌃⇧T".
func (c Chord) Short() string {
	var b strings.Builder
	for _, m := range mods {
		if c.Mods.Contain(m.m) {
			b.WriteString(m.sym)
		}
	}
	return b.String() + name(c.Name, true)
}

func name(n key.Name, cap bool) string {
	for _, k := range names {
		if k.n == n {
			if cap {
				return k.cap
			}
			return k.word
		}
	}
	return string(n)
}

// Cap is id's first chord on a key cap; a fixed binding's keys.
func Cap(id string) string {
	if b := byID[id]; b != nil && b.Fixed != "" {
		return b.Fixed
	}
	return Of(id)[0].Text()
}

// Short is id's first chord in symbols, for tight spots.
func Short(id string) string { return Of(id)[0].Short() }

// Caps is all of id's chords on caps: "V or 1".
func Caps(id string) string {
	if b := byID[id]; b != nil && b.Fixed != "" {
		return b.Fixed
	}
	var s []string
	for _, c := range Of(id) {
		s = append(s, c.Text())
	}
	return strings.Join(s, " or ")
}

// Parse reads a chord as String writes it; it takes other spellings too
// ("ctrl+shift+t", "Cmd+K", "Return"). Shortcut is Ctrl, or ⌘ on macOS.
func Parse(s string) (Chord, error) {
	s = strings.TrimSpace(s)
	var c Chord
	parts := strings.Split(s, "+")
	if strings.HasSuffix(s, "++") || s == "+" { // the + key itself
		parts = append(strings.Split(strings.TrimSuffix(s, "++"), "+"), "+")
		if s == "+" {
			parts = []string{"+"}
		}
	}
	last := parts[len(parts)-1]
	for _, p := range parts[:len(parts)-1] {
		m, ok := mod(p)
		if !ok {
			return Chord{}, fmt.Errorf("%q is no modifier", p)
		}
		c.Mods |= m
	}
	if last == "" {
		return Chord{}, errors.New("no key")
	}
	c.Name = keyName(last)
	if c.Name == "" {
		return Chord{}, fmt.Errorf("%q is no key", last)
	}
	return c, nil
}

func mod(p string) (key.Modifiers, bool) {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "shortcut" {
		return key.ModShortcut, true
	}
	for _, m := range mods {
		if p == strings.ToLower(m.word) || p == m.sym {
			return m.m, true
		}
		for _, o := range m.others {
			if p == o {
				return m.m, true
			}
		}
	}
	return 0, false
}

func keyName(s string) key.Name {
	l := strings.ToLower(s)
	for _, k := range names {
		if l == strings.ToLower(k.word) || s == k.cap { // "↵" is Return's: it comes first
			return k.n
		}
		for _, o := range k.others {
			if l == o {
				return k.n
			}
		}
	}
	if len(l) >= 2 && len(l) <= 3 && l[0] == 'f' && l[1] >= '1' && l[1] <= '9' {
		var n int
		if _, err := fmt.Sscanf(l[1:], "%d", &n); err == nil && n >= 1 && n <= 12 && fmt.Sprint(n) == l[1:] {
			return key.Name(fmt.Sprintf("F%d", n))
		}
	}
	if utf8.RuneCountInString(s) == 1 {
		return key.Name(strings.ToUpper(s))
	}
	return ""
}

// Mod is the infra workspace's modifier: ⌘ on macOS, Ctrl+Shift elsewhere,
// so plain Ctrl keys keep going to the shell.
var Mod = map[bool]key.Modifiers{true: key.ModCommand, false: key.ModCtrl | key.ModShift}[runtime.GOOS == "darwin"]
