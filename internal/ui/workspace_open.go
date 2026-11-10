package ui

import (
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/lfdominguez/desk/internal/ui/kit"
)

// Quick open: in a card's workspace, Ctrl+P opens the palette on its Files
// tab, the workspace's files matched by fzf as every search box is; picking
// one opens it in a tab.

// wsOpenMax bounds the files listed.
// ponytail: walked on the UI goroutine when the palette opens, at most
// wsOpenMax files; walk in the background if projects that big are common.
const wsOpenMax = 20000

// wsFileItems are card id's workspace files as palette items (none for a
// card gone): its projects walked without wsHidden's folders.
func (u *ui) wsFileItems(id string) []*palItem {
	c := u.b.Card(id)
	if c == nil {
		return nil
	}
	st := u.wsOf(id)
	var out []*palItem
	for _, root := range u.projects(c) {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			switch {
			case err != nil: // unreadable: what's below it is left out
				return nil
			case len(out) >= wsOpenMax:
				return filepath.SkipAll
			case d.IsDir() && p != root && wsHidden[d.Name()]:
				return filepath.SkipDir
			case d.IsDir():
				return nil
			}
			rel, _ := filepath.Rel(root, filepath.Dir(p))
			out = append(out, &palItem{id: "file:" + p, kind: palFile, title: d.Name(), sub: "· " + filepath.Join(filepath.Base(root), rel),
				ico: kit.IcoCode, preview: [][2]string{{"Path", strings.Replace(p, u.home, "~", 1)}}, run: func() { u.wsOpen(st, p) }})
			return nil
		})
	}
	return out
}

// openFiles opens the palette on the workspace's files (openPalette walks
// them, in a workspace).
func (u *ui) openFiles() {
	u.openPalette()
	u.pal.scope = palFile
}
