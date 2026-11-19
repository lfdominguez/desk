package vt

// desk patch: the cursor, editing and function keys with Shift, Alt, Ctrl or
// Meta held are sent as xterm sends them (CSI 1;m A, CSI 3;m ~…). vt's own
// table matches only bare keys, so Ctrl+← (a word left in bash, zsh and
// readline) and its kin sent nothing.

import (
	"strconv"

	uv "github.com/charmbracelet/ultraviolet"
)

// xtermFinal is a key xterm sends as CSI 1;m <final>.
var xtermFinal = map[rune]byte{
	KeyUp: 'A', KeyDown: 'B', KeyRight: 'C', KeyLeft: 'D', KeyHome: 'H', KeyEnd: 'F',
	KeyF1: 'P', KeyF2: 'Q', KeyF3: 'R', KeyF4: 'S',
}

// xtermTilde is a key xterm sends as CSI n;m ~.
var xtermTilde = map[rune]int{
	KeyInsert: 2, KeyDelete: 3, KeyPgUp: 5, KeyPgDown: 6,
	KeyF5: 15, KeyF6: 17, KeyF7: 18, KeyF8: 19, KeyF9: 20, KeyF10: 21, KeyF11: 23, KeyF12: 24,
}

// modifiedKey is k's xterm sequence when it is one of those keys with a
// modifier held; false for anything else, which SendKey's table handles.
func modifiedKey(k uv.KeyPressEvent) (string, bool) {
	m := 1
	if k.Mod&ModShift != 0 {
		m++
	}
	if k.Mod&ModAlt != 0 {
		m += 2
	}
	if k.Mod&ModCtrl != 0 {
		m += 4
	}
	if k.Mod&ModMeta != 0 {
		m += 8
	}
	if m == 1 {
		return "", false
	}
	if f, ok := xtermFinal[k.Code]; ok {
		return "\x1b[1;" + strconv.Itoa(m) + string(f), true
	}
	if n, ok := xtermTilde[k.Code]; ok {
		return "\x1b[" + strconv.Itoa(n) + ";" + strconv.Itoa(m) + "~", true
	}
	return "", false
}
