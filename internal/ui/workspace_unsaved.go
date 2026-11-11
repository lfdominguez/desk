package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// wsUnsaved is a workspace tab's text not saved, kept over a quit: desk
// writes them beside the board as it closes (or is killed: quitOn) and puts
// them back when their card's workspace opens again (wsRestore).
// ponytail: written only on a clean close or a SIGTERM/Ctrl+C, not on a
// crash or kill -9; stash on a timer as well if that ever loses work.
type wsUnsaved struct {
	Card  string      `json:"card"`
	Path  string      `json:"path"`
	Text  string      `json:"text"`
	Mtime time.Time   `json:"mtime"` // the file's when read or saved: a newer one on disk asks Reload or Keep mine
	Mode  os.FileMode `json:"mode"`
}

// unsavedFile is where they are kept: beside the board.
func unsavedFile(board string) string { return filepath.Join(filepath.Dir(board), "unsaved.json") }

// loadUnsaved reads the ones the last quit kept; none without the file.
func loadUnsaved(board string) []wsUnsaved {
	var out []wsUnsaved
	if b, err := os.ReadFile(unsavedFile(board)); err == nil {
		json.Unmarshal(b, &out)
	}
	return out
}

// wsStash writes every open tab with changes not saved, and those not put
// back yet; with none, it removes the file. 0600: a buffer can hold secrets.
func (u *ui) wsStash() {
	out := slices.Clone(u.unsaved)
	for id, st := range u.ws {
		for _, t := range st.tabs {
			if t.dirty {
				out = append(out, wsUnsaved{id, t.path, t.ed.Text(), t.mtime, t.mode})
			}
		}
	}
	p := unsavedFile(u.path)
	if len(out) == 0 {
		os.Remove(p)
		return
	}
	b, _ := json.Marshal(out)
	if err := writeAtomic(p, string(b), 0o600); err != nil {
		u.showToast("Changes not saved weren't kept: "+err.Error(), true)
	}
}

// wsRestore opens card id's tabs the last quit kept, with their changes
// not saved, in st (already in u.ws, so stashing again keeps them).
func (u *ui) wsRestore(id string, st *wsState) {
	n := 0
	u.unsaved = slices.DeleteFunc(u.unsaved, func(e wsUnsaved) bool {
		if e.Card != id {
			return false
		}
		i := slices.IndexFunc(st.tabs, func(t *wsTab) bool { return t.path == e.Path })
		var t *wsTab
		if i < 0 {
			t = u.wsAddTab(st, e.Path, e.Text, e.Mtime, e.Mode)
		} else { // reopened from the card's tabs, as on disk: the kept text over it
			t, st.cur = st.tabs[i], i
			t.ed.SetText(e.Text)
			t.mtime, t.mode = e.Mtime, e.Mode
			t.gen++
		}
		t.dirty = true
		if fi, err := os.Stat(e.Path); err == nil && !fi.ModTime().Equal(e.Mtime) {
			t.disk = true // changed on disk since: the bar asks
		}
		n++
		return true
	})
	if n == 0 {
		return
	}
	u.wsStash()
	u.showToast(fmt.Sprintf("Put back %d file(s) with changes not saved", n), false)
}

// quitting writes what must outlive the window: the board, the workspaces'
// changes not saved.
func (u *ui) quitting() {
	u.keepDraft()
	u.flushPending()
	u.wsStash()
}
