package infra

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/keys"
	deskorgs "github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// The directory split: a split in the tab, at most one per tab, with the
// focused pane's server on the left and this machine on the right, Total
// Commander style. Listing and copying go through files.go,
// in goroutines; the panels redraw as results and progress come in. Rows drag
// onto the other panel (or one of its folders) to copy.

const filesMime = "application/x-desk-files" // in-window drag and drop only; the payload lives in filesView.drag

var lastLocalDir string // where the local panel was, for the next time the panels open

type fileSide struct {
	remote    bool
	native    bool        // remote, but with this OS's paths: the local host under localGo
	path      string      // absolute, once listed
	all       []fileEntry // the listing as it came
	entries   []fileEntry // what is shown: "..", then the (non-hidden) entries sorted
	sel       int
	anchor    int // where a shift-click range starts
	marked    map[string]bool
	list      layout.List
	follow    bool // keep sel in view after a keyboard move
	loading   bool
	err       string // the first listing failed; later failures are toasts
	seq       int    // the latest listing asked for; slower earlier ones are dropped
	sortBy    int    // 0 name, 1 size, 2 modified
	desc      bool
	rows      map[string]*fileRow // per entry widget state, reset with the directory
	dropHover bool                // the pointer is over this panel
}

type fileRow struct {
	click widget.Clickable
	drag  widget.Draggable
	hover bool // the pointer is over this row (folders: a drop lands inside)
	moved bool // pressed and moved: a real drag (Draggable says dragging from the press on)
}

type copyJob struct {
	verb     string       // "Copying", "Moving", "Opening"
	label    string       // the item moving now
	n        atomic.Int64 // its bytes so far
	total    int64        // its size, -1 for a folder
	done     int64        // bytes of the items already finished
	batch    int64        // bytes of the whole batch, -1 when it has folders
	i, count int
	started  time.Time
}

type dragInfo struct {
	from  int
	items []fileEntry
}

type filesView struct {
	mu              sync.Mutex // the sides' listings and the job, which goroutines fill in
	host, dir, name string
	sides           [2]*fileSide // server, local
	active          int
	job             *copyJob // the batch running now
	queue           []func() // batches asked for while one runs, started in order
	cancel          context.CancelFunc
	prompt          string      // "mkdir", "rename", "chmod", "delete", "conflict", "overwrite" or ""
	conflicts       []string    // names already in the destination, for the conflict prompt
	conflictOf      int         // ... out of how many the batch copies
	conflictTo      string      // ... and where to: "dir on host"
	choice          chan string // the conflict answer: "replace", "skip" or "" (cancel)
	hidden          bool        // show dotfiles
	search          string      // type-ahead find
	searchAt        time.Time
	focusNext       bool
	drag            *dragInfo // the last drag started; kept until its drop lands
	dragging        bool      // a row was being dragged last frame
	dragSeen        bool      // ... and this frame
	after           []func()  // actions picked up while drawing, run once mu is free
	ctx             context.Context
	tab             *Tab // the tab it belongs to
}

// tabFiles is the active tab's directory split, if it has one.
func (u *ui) tabFiles() *filesView {
	if t := u.tab(); t != nil {
		return u.files[t.Key]
	}
	return nil
}

// toggleFiles adds the directory split to the active tab, or closes it.
func (u *ui) toggleFiles() {
	if fv := u.tabFiles(); fv != nil {
		u.closeFiles(fv)
		return
	}
	u.openFiles()
}

// openFiles adds the directory split to the active tab, on its focused pane's
// server, the remote side in the shell's directory. A tab has one at most:
// asking again focuses it.
func (u *ui) openFiles() {
	if fv := u.tabFiles(); fv != nil {
		fv.focusNext = true
		u.showToast("This tab already has its directory split", false)
		return
	}
	t := u.tab()
	var p *guiPane
	if t != nil {
		for i := range t.Panes {
			if t.Panes[i].Focused {
				p = &t.Panes[i]
			}
		}
	}
	if p == nil || p.Term == nil {
		u.showToast("Open a server first: the panels browse the focused pane's server", true)
		return
	}
	local := lastLocalDir
	if local == "" {
		local, _ = os.UserHomeDir()
	}
	fv := &filesView{host: p.Term.Host, dir: p.Dir, name: p.Name, focusNext: true, ctx: context.Background(), tab: t.Key}
	for i := range fv.sides {
		fv.sides[i] = &fileSide{remote: i == 0, native: i == 0 && localGo && fv.host == "local", marked: map[string]bool{}, rows: map[string]*fileRow{}}
		fv.sides[i].list.Axis = layout.Vertical
	}
	u.files[t.Key] = fv
	u.loadSide(fv, fv.sides[0], p.Term.Cwd(), "")
	u.loadSide(fv, fv.sides[1], local, "")
}

// closeFiles removes the split; a running copy keeps going (it reports with
// a toast), a pending conflict question is answered "cancel".
func (u *ui) closeFiles(fv *filesView) {
	fv.mu.Lock()
	if fv.prompt == "conflict" || fv.prompt == "overwrite" {
		fv.answer("")
	}
	fv.mu.Unlock()
	delete(u.files, fv.tab)
	u.wantTerm = true
}

// answer resolves the conflict prompt; mu held.
func (fv *filesView) answer(c string) {
	select {
	case fv.choice <- c:
	default:
	}
	fv.prompt = ""
}

// loadSide lists p on side s in the background, then selects name if given
// (on a refresh of the same directory, keeps the selection).
func (u *ui) loadSide(fv *filesView, s *fileSide, p, name string) {
	fv.mu.Lock()
	s.loading = true
	s.seq++
	seq := s.seq
	fv.mu.Unlock()
	u.redraw()
	go func() {
		var abs string
		var es []fileEntry
		var err error
		if s.remote {
			abs, es, err = listRemote(fv.ctx, fv.host, fv.dir, p)
		} else {
			abs, es, err = listLocal(p)
		}
		fv.mu.Lock()
		if seq != s.seq {
			fv.mu.Unlock()
			return
		}
		s.loading = false
		switch {
		case err != nil && s.path == "" && p != "" && s.remote:
			fv.mu.Unlock()
			u.loadSide(fv, s, "", "") // the shell's directory is gone or unreadable: start at home
			return
		case err != nil && s.path == "":
			s.err = err.Error()
		case err != nil:
			fv.mu.Unlock()
			u.showToast(err.Error(), true) // keep showing where we were
			u.redraw()
			return
		default:
			if abs != s.path {
				s.marked, s.rows, s.list.Position = map[string]bool{}, map[string]*fileRow{}, layout.Position{}
			} else if name == "" && s.sel < len(s.entries) {
				name = s.entries[s.sel].Name
			}
			s.path, s.all, s.err = abs, es, ""
			if !s.remote {
				lastLocalDir = abs
			}
			fv.refilter(s, name)
		}
		fv.mu.Unlock()
		u.redraw()
	}()
}

// refilter rebuilds what s shows (hidden files, sort order) and selects keep; mu held.
func (fv *filesView) refilter(s *fileSide, keep string) {
	var es []fileEntry
	if s.path != "/" {
		es = append(es, fileEntry{Name: "..", Dir: true, Size: -1})
	}
	n := len(es)
	for _, e := range s.all {
		if fv.hidden || !strings.HasPrefix(e.Name, ".") {
			es = append(es, e)
		}
	}
	sortEntries(es[n:], s.sortBy, s.desc)
	s.entries, s.sel, s.anchor, s.follow = es, 0, 0, true
	for i, e := range es {
		if e.Name == keep {
			s.sel, s.anchor = i, i
		}
	}
}

// posix is whether the side's paths are a server's (path, not filepath).
func (s *fileSide) posix() bool { return s.remote && !s.native }

func (s *fileSide) join(name string) string {
	if s.posix() {
		return path.Join(s.path, name)
	}
	return filepath.Join(s.path, name)
}

func (s *fileSide) parent() string {
	if s.posix() {
		return path.Dir(s.path)
	}
	return filepath.Dir(s.path)
}

func (s *fileSide) hiddenCount() int {
	n := 0
	for _, e := range s.all {
		if strings.HasPrefix(e.Name, ".") {
			n++
		}
	}
	return n
}

// jump selects the first entry starting with q, else the first containing it; mu held.
func (s *fileSide) jump(q string) {
	for _, contains := range []bool{false, true} {
		for i, e := range s.entries {
			n := strings.ToLower(e.Name)
			if e.Name != ".." && (strings.HasPrefix(n, q) || contains && strings.Contains(n, q)) {
				s.sel, s.anchor, s.follow = i, i, true
				return
			}
		}
	}
}

// open acts on the selected entry: enters a directory ("..": the parent,
// selecting where we came from), opens a file with the desktop's default
// application (a server's file is downloaded first).
func (u *ui) open(fv *filesView, s *fileSide) {
	fv.mu.Lock()
	if s.sel >= len(s.entries) {
		fv.mu.Unlock()
		return
	}
	e, to := s.entries[s.sel], s.join(s.entries[s.sel].Name)
	side := map[bool]int{true: 0, false: 1}[s.remote]
	fv.mu.Unlock()
	switch {
	case e.Name == "..":
		u.up(fv, s)
	case e.Dir:
		u.loadSide(fv, s, to, "")
	case !s.remote:
		if err := openLocal(to); err != nil {
			u.showToast("Open failed: "+err.Error(), true)
		}
	default:
		cache, err := deskorgs.XDGDir("XDG_CACHE_HOME", ".cache")
		if err == nil {
			cache = filepath.Join(cache, "desk", "files", fv.host)
			err = os.MkdirAll(cache, 0o700)
		}
		if err != nil {
			u.showToast("Open failed: "+err.Error(), true)
			return
		}
		u.transfer(fv, side, []fileEntry{e}, cache, false, true)
	}
}

// editFor is how long F4 watches a downloaded file for saves.
const editFor = time.Hour

// edit (F4) opens the selected file in the desktop's editor. A server file is
// downloaded first and uploaded again each time it's saved there; if the
// server's copy changed meanwhile, the bar asks before overwriting it.
// ponytail: polls the local copy's mtime every second, for editFor.
func (u *ui) edit(fv *filesView, s *fileSide) {
	fv.mu.Lock()
	if s.sel >= len(s.entries) {
		fv.mu.Unlock()
		return
	}
	e, dir := s.entries[s.sel], s.path
	fv.mu.Unlock()
	if e.Dir || e.Name == ".." {
		return
	}
	if !s.remote {
		if err := openLocal(filepath.Join(dir, e.Name)); err != nil {
			u.showToast("Edit failed: "+err.Error(), true)
		}
		return
	}
	go func() {
		cache, err := deskorgs.XDGDir("XDG_CACHE_HOME", ".cache")
		if err == nil {
			cache = filepath.Join(cache, "desk", "edit", fv.host, strings.ReplaceAll(strings.Trim(dir, "/"), "/", "_"))
			err = os.MkdirAll(cache, 0o700)
		}
		var n atomic.Int64
		if err == nil {
			err = copyFromRemote(fv.ctx, fv.host, fv.dir, dir, cache, e, &n, func() {})
		}
		local := filepath.Join(cache, e.Name)
		fi, serr := os.Stat(local)
		if err == nil && serr != nil {
			err = serr
		}
		if err == nil {
			err = openLocal(local)
		}
		if err != nil {
			u.showToast("Edit failed: "+err.Error(), true)
			return
		}
		u.showToast("Editing "+e.Name+": each save uploads it to "+fv.name, false)
		saved, serverMod := fi.ModTime(), e.Mod
		for end := time.Now().Add(editFor); time.Now().Before(end) && fv.ctx.Err() == nil; time.Sleep(time.Second) {
			fi, err := os.Stat(local)
			if err != nil || !fi.ModTime().After(saved) {
				continue
			}
			saved = fi.ModTime()
			if cur, ok := remoteEntry(fv, dir, e.Name); ok && cur.Mod.After(serverMod.Add(time.Second)) && !u.askOverwrite(fv, e.Name) {
				u.showToast("Kept the server's "+e.Name+"; your edit is in "+local, true)
				serverMod = cur.Mod
				continue
			}
			up := fileEntry{Name: e.Name, Mode: e.Mode}
			if err := copyToRemote(fv.ctx, fv.host, fv.dir, cache, dir, up, &n, func() {}); err != nil {
				u.showToast("Upload of "+e.Name+" failed: "+err.Error(), true)
				continue
			}
			if cur, ok := remoteEntry(fv, dir, e.Name); ok {
				serverMod = cur.Mod
			}
			u.showToast("Saved "+e.Name+" to "+fv.name, false)
			fv.mu.Lock()
			srv := fv.sides[0]
			reload := srv.path == dir
			fv.mu.Unlock()
			if reload {
				u.loadSide(fv, srv, dir, e.Name)
			}
		}
	}()
}

// remoteEntry looks name up in the server's dir.
func remoteEntry(fv *filesView, dir, name string) (fileEntry, bool) {
	ctx, cancel := context.WithTimeout(fv.ctx, 20*time.Second)
	defer cancel()
	_, es, err := listRemote(ctx, fv.host, fv.dir, dir)
	if err != nil {
		return fileEntry{}, false
	}
	for _, e := range es {
		if e.Name == name {
			return e, true
		}
	}
	return fileEntry{}, false
}

// askOverwrite asks in the bar whether to replace a server file that changed
// since it was opened for editing.
func (u *ui) askOverwrite(fv *filesView, name string) bool {
	ch := make(chan string, 1)
	fv.mu.Lock()
	fv.prompt, fv.conflicts, fv.choice = "overwrite", []string{name}, ch
	fv.focusNext = true
	fv.mu.Unlock()
	u.redraw()
	select {
	case c := <-ch:
		return c == "replace"
	case <-fv.ctx.Done():
		return false
	}
}

func (u *ui) up(fv *filesView, s *fileSide) {
	fv.mu.Lock()
	cur, parent := s.path, s.parent()
	fv.mu.Unlock()
	if cur == "" || cur == "/" {
		return
	}
	u.loadSide(fv, s, parent, filepath.Base(cur))
}

// chosen is what an action applies to: the marked entries, else the selected one.
func (s *fileSide) chosen() []fileEntry {
	var out []fileEntry
	for _, e := range s.entries {
		if s.marked[e.Name] {
			out = append(out, e)
		}
	}
	if len(out) == 0 && s.sel < len(s.entries) && s.entries[s.sel].Name != ".." {
		out = append(out, s.entries[s.sel])
	}
	return out
}

// transfer copies items from side from's directory into dstDir on the other
// machine, one after another in the background. It asks first when names
// already exist there. move deletes each source once it's copied; open opens
// the (single, downloaded) file afterwards.
// A batch asked for while another runs waits in fv.queue.
func (u *ui) transfer(fv *filesView, from int, items []fileEntry, dstDir string, move, open bool) {
	fv.mu.Lock()
	srcDir := fv.sides[from].path
	fv.mu.Unlock()
	u.transferFrom(fv, from, srcDir, items, dstDir, move, open)
}

func (u *ui) transferFrom(fv *filesView, from int, srcDir string, items []fileEntry, dstDir string, move, open bool) {
	fv.mu.Lock()
	src, dst := fv.sides[from], fv.sides[1-from]
	if len(items) == 0 || dstDir == "" || srcDir == "" {
		fv.mu.Unlock()
		return
	}
	if fv.job != nil {
		fv.queue = append(fv.queue, func() { u.transferFrom(fv, from, srcDir, items, dstDir, move, open) })
		n := len(fv.queue)
		fv.mu.Unlock()
		u.showToast(fmt.Sprintf("Queued after the running copy (%d waiting)", n), false)
		return
	}
	if fv.host == "local" && srcDir == dstDir { // both panels on this machine: cat would truncate the file it reads
		fv.mu.Unlock()
		u.showToast("Source and destination are the same folder", true)
		return
	}
	ctx, cancel := context.WithCancel(fv.ctx)
	job := &copyJob{verb: "Copying", count: len(items), started: time.Now()}
	switch {
	case open:
		job.verb = "Opening"
	case move:
		job.verb = "Moving"
	}
	for _, e := range items {
		if e.Dir {
			job.batch = -1
		} else if job.batch >= 0 {
			job.batch += max(0, e.Size)
		}
	}
	fv.job, fv.cancel = job, cancel
	fv.mu.Unlock()
	u.redraw()
	go func() {
		defer cancel()
		if !open {
			items = u.resolveConflicts(ctx, fv, !src.remote, items, dstDir)
		}
		var failed []string
		var copied []fileEntry
		for i, e := range items {
			if ctx.Err() != nil {
				break
			}
			fv.mu.Lock()
			job.label, job.total, job.i = e.Name, e.Size, i
			if e.Dir {
				job.total = -1
			}
			job.n.Store(0)
			fv.mu.Unlock()
			u.redraw()
			var err error
			if src.remote {
				err = copyFromRemote(ctx, fv.host, fv.dir, srcDir, dstDir, e, &job.n, u.redraw)
			} else {
				err = copyToRemote(ctx, fv.host, fv.dir, srcDir, dstDir, e, &job.n, u.redraw)
			}
			if err == nil && move {
				if src.remote {
					err = removeRemote(ctx, fv.host, fv.dir, []string{path.Join(srcDir, e.Name)})
				} else {
					err = os.RemoveAll(filepath.Join(srcDir, e.Name))
				}
			}
			fv.mu.Lock()
			job.done += job.n.Load()
			fv.mu.Unlock()
			switch {
			case ctx.Err() != nil:
			case err != nil:
				failed = append(failed, e.Name+": "+err.Error())
			default:
				copied = append(copied, e)
			}
		}
		cancelled := ctx.Err() != nil
		fv.mu.Lock()
		fv.job, fv.cancel = nil, nil
		var next func()
		if len(fv.queue) > 0 && fv.ctx.Err() == nil {
			next, fv.queue = fv.queue[0], fv.queue[1:]
		}
		defer func() {
			if next != nil {
				next()
			}
		}()
		for _, e := range copied {
			delete(src.marked, e.Name)
		}
		dstPath := dst.path
		fv.mu.Unlock()
		where := map[bool]string{true: "this machine", false: fv.name}[src.remote]
		done := map[string]string{"Copying": "Copied", "Moving": "Moved", "Opening": "Opened"}[job.verb]
		switch {
		case cancelled && len(items) > 0:
			u.showToast(fmt.Sprintf("Cancelled: %d of %d done; a folder that was copying may be partial", len(copied), len(items)), true)
		case cancelled:
		case len(failed) > 0:
			u.showToast(fmt.Sprintf("%s %d, failed %d: ", done, len(copied), len(failed))+strings.Join(failed, "; "), true)
		case open && len(copied) == 1:
			if err := openLocal(filepath.Join(dstDir, copied[0].Name)); err != nil {
				u.showToast("Open failed: "+err.Error(), true)
			}
		case len(copied) > 0:
			u.showToast(fmt.Sprintf("%s %s to %s", done, plural(len(copied)), where), false)
		}
		if open {
			return
		}
		sel := ""
		if dstDir == dstPath && len(copied) > 0 {
			sel = copied[0].Name
		}
		u.loadSide(fv, dst, dstPath, sel)
		if move {
			u.loadSide(fv, src, srcDir, "")
		}
	}()
}

func plural(n int) string {
	if n == 1 {
		return "1 item"
	}
	return fmt.Sprintf("%d items", n)
}

// resolveConflicts asks what to do when items already exist in dstDir and
// returns those to copy: all ("replace": files are overwritten, folders
// merged), only the new ones ("skip") or none (cancel).
func (u *ui) resolveConflicts(ctx context.Context, fv *filesView, dstRemote bool, items []fileEntry, dstDir string) []fileEntry {
	var have []fileEntry
	if dstRemote {
		_, have, _ = listRemote(ctx, fv.host, fv.dir, dstDir)
	} else {
		_, have, _ = listLocal(dstDir)
	}
	exists := map[string]bool{}
	for _, e := range have {
		exists[e.Name] = true
	}
	var clash []string
	for _, e := range items {
		if exists[e.Name] {
			clash = append(clash, e.Name)
		}
	}
	if len(clash) == 0 {
		return items
	}
	ch := make(chan string, 1)
	fv.mu.Lock()
	fv.prompt, fv.conflicts, fv.choice = "conflict", clash, ch
	fv.conflictOf, fv.conflictTo = len(items), dstDir+" on "+map[bool]string{true: fv.name, false: "this machine"}[dstRemote]
	fv.focusNext = true
	fv.mu.Unlock()
	u.redraw()
	var c string
	select {
	case c = <-ch:
	case <-ctx.Done():
	}
	switch c {
	case "replace":
		return items
	case "skip":
		var out []fileEntry
		for _, e := range items {
			if !exists[e.Name] {
				out = append(out, e)
			}
		}
		if len(out) == 0 {
			u.showToast("Nothing to copy: every item already exists there", false)
		}
		return out
	}
	return nil
}

func (u *ui) copyChosen(fv *filesView, move bool) {
	fv.mu.Lock()
	from := fv.active
	items, dstDir := fv.sides[from].chosen(), fv.sides[1-from].path
	fv.mu.Unlock()
	u.transfer(fv, from, items, dstDir, move, false)
}

func (u *ui) deleteChosen(fv *filesView) {
	fv.mu.Lock()
	s := fv.sides[fv.active]
	dirPath := s.path
	var paths []string
	for _, e := range s.chosen() {
		paths = append(paths, s.join(e.Name))
	}
	s.marked = map[string]bool{}
	fv.mu.Unlock()
	go func() {
		var err error
		if s.remote {
			err = removeRemote(fv.ctx, fv.host, fv.dir, paths)
		} else {
			for _, p := range paths {
				if e := os.RemoveAll(p); e != nil {
					err = e
				}
			}
		}
		if err != nil {
			u.showToast("Delete failed: "+err.Error(), true)
		}
		u.loadSide(fv, s, dirPath, "")
	}()
}

// validName rejects what can't be a single entry's name, and names already
// in s; mu held.
func (u *ui) validName(s *fileSide, name string) bool {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return false
	}
	for _, e := range s.all {
		if e.Name == name {
			u.showToast(name+" already exists", true)
			return false
		}
	}
	return true
}

func (u *ui) mkdir(fv *filesView, name string) {
	fv.mu.Lock()
	s := fv.sides[fv.active]
	dirPath, ok := s.path, u.validName(s, name)
	fv.mu.Unlock()
	if !ok {
		return
	}
	go func() {
		var err error
		if s.remote {
			err = mkdirRemote(fv.ctx, fv.host, fv.dir, path.Join(dirPath, name))
		} else {
			err = os.MkdirAll(filepath.Join(dirPath, name), 0o755)
		}
		if err != nil {
			u.showToast("New folder failed: "+err.Error(), true)
		}
		u.loadSide(fv, s, dirPath, name)
	}()
}

// editing says the bar's name editor has the keyboard.
func (fv *filesView) editing() bool {
	return fv.prompt == "mkdir" || fv.prompt == "rename" || fv.prompt == "chmod"
}

var chmodMode = regexp.MustCompile(`^([0-7]{3,4}|[ugoa]*[-+=][rwxXst]+(,[ugoa]*[-+=][rwxXst]+)*)$`)

// chmod sets the permissions of the chosen entries on the active side.
func (u *ui) chmod(fv *filesView, mode string) {
	fv.mu.Lock()
	s := fv.sides[fv.active]
	ch, dirPath := s.chosen(), s.path
	var ps []string
	for _, e := range ch {
		ps = append(ps, s.join(e.Name))
	}
	fv.mu.Unlock()
	if !chmodMode.MatchString(mode) {
		u.showToast("Not a mode: use octal like 644 or symbolic like u+x", true)
		return
	}
	host := fv.host
	if !s.remote {
		host = "local" // this machine's side: chmod as for the local host (in Go under localGo)
	}
	go func() {
		err := chmodRemote(fv.ctx, host, fv.dir, mode, ps)
		if err != nil {
			u.showToast("Chmod failed: "+err.Error(), true)
		} else {
			u.showToast("Permissions set to "+mode+" on "+plural(len(ch)), false)
		}
		keep := ""
		if len(ch) > 0 {
			keep = ch[0].Name
		}
		u.loadSide(fv, s, dirPath, keep)
	}()
}

func (u *ui) rename(fv *filesView, name string) {
	fv.mu.Lock()
	s := fv.sides[fv.active]
	var old string
	if s.sel < len(s.entries) {
		old = s.entries[s.sel].Name
	}
	from, to, dirPath := s.join(old), s.join(name), s.path
	ok := old != "" && old != ".." && name != old && u.validName(s, name)
	fv.mu.Unlock()
	if !ok {
		return
	}
	go func() {
		var err error
		if s.remote {
			err = renameRemote(fv.ctx, fv.host, fv.dir, from, to)
		} else {
			err = os.Rename(from, to)
		}
		if err != nil {
			u.showToast("Rename failed: "+err.Error(), true)
		}
		u.loadSide(fv, s, dirPath, name)
	}()
}

// startPrompt opens the name editor for "mkdir" or "rename"; mu held.
func (u *ui) startPrompt(gtx layout.Context, fv *filesView, kind string) {
	s := fv.sides[fv.active]
	name := ""
	if kind == "rename" {
		if s.sel >= len(s.entries) || s.entries[s.sel].Name == ".." {
			return
		}
		name = s.entries[s.sel].Name
	}
	if kind == "chmod" {
		ch := s.chosen()
		if len(ch) == 0 {
			return
		}
		fv.prompt = kind
		u.nameIn.SetText(fmt.Sprintf("%o", ch[0].Mode.Perm()))
		u.nameIn.SetCaret(0, u.nameIn.Len())
		u.focusPrompt(gtx, fv)
		return
	}
	fv.prompt = kind
	u.nameIn.SetText(name)
	stem := len([]rune(strings.TrimSuffix(name, path.Ext(name))))
	if stem == 0 { // ".bashrc"
		stem = len([]rune(name))
	}
	u.nameIn.SetCaret(stem, 0) // the name selected, the extension kept
	u.focusPrompt(gtx, fv)
}

// focusPrompt focuses the prompt's editor now (a key, before the editor is
// drawn) and again on the next frame (a bar button, after it was drawn without).
func (u *ui) focusPrompt(gtx layout.Context, fv *filesView) {
	gtx.Execute(key.FocusCmd{Tag: &u.nameIn})
	fv.focusNext = true
	gtx.Execute(op.InvalidateCmd{})
}

// filesKeyIDs are the files split's rebindable keys (keys' files.*).
var filesKeyIDs = []string{"files.view", "files.edit", "files.copy", "files.move", "files.rename", "files.mkdir", "files.delete",
	"files.chmod", "files.mark", "files.all", "files.hidden", "files.reload"}

// filesKeys handles the panels' keyboard.
func (u *ui) filesKeys(gtx layout.Context, fv *filesView) {
	if fv.focusNext {
		var tag event.Tag = fv
		if fv.editing() { // a bar button's prompt: its editor wasn't drawn when it asked
			tag = &u.nameIn
		}
		gtx.Execute(key.FocusCmd{Tag: tag})
		fv.focusNext = false
	}
	filters := append([]event.Filter{key.FocusFilter{Target: fv}}, keys.Filters(fv, filesKeyIDs...)...)
	for _, n := range []key.Name{key.NameUpArrow, key.NameDownArrow, key.NamePageUp, key.NamePageDown, key.NameHome, key.NameEnd} {
		filters = append(filters, key.Filter{Focus: fv, Name: n, Optional: key.ModShortcut}) // Ctrl marks what it passes
	}
	for _, n := range []key.Name{key.NameReturn, key.NameEnter, key.NameDeleteBackward, key.NameTab, key.NameEscape, "Y", "N", "O", "S"} {
		filters = append(filters, key.Filter{Focus: fv, Name: n})
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		if ee, ok := ev.(key.EditEvent); ok {
			u.typeAhead(fv, ee.Text)
			continue
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		fv.mu.Lock()
		s := fv.sides[fv.active]
		page := max(1, s.list.Position.Count-1)
		switch fv.prompt {
		case "delete":
			switch e.Name {
			case "Y", key.NameReturn, key.NameEnter:
				fv.prompt = ""
				fv.after = append(fv.after, func() { u.deleteChosen(fv) })
			case "N", key.NameEscape:
				fv.prompt = ""
			}
			fv.mu.Unlock()
			continue
		case "overwrite":
			switch e.Name {
			case "O":
				fv.answer("replace")
			case "N", key.NameEscape:
				fv.answer("")
			}
			fv.mu.Unlock()
			continue
		case "conflict":
			switch e.Name {
			case "O", key.NameReturn, key.NameEnter:
				fv.answer("replace")
			case "S":
				fv.answer("skip")
			case "N", key.NameEscape:
				fv.answer("")
			}
			fv.mu.Unlock()
			continue
		}
		mark := e.Modifiers.Contain(key.ModShortcut)
		move := func(d int) {
			from := s.sel
			s.sel = max(0, min(len(s.entries)-1, s.sel+d))
			s.follow, fv.search = true, ""
			if mark { // Ctrl+arrows mark what they pass over
				for i := min(from, s.sel); i <= max(from, s.sel) && i < len(s.entries); i++ {
					if s.entries[i].Name != ".." {
						s.marked[s.entries[i].Name] = true
					}
				}
			}
			s.anchor = s.sel
		}
		searching := fv.search != "" && time.Since(fv.searchAt) < time.Second
		var after func()
		switch {
		case keys.Is("files.mark", e):
			if s.sel < len(s.entries) && s.entries[s.sel].Name != ".." {
				n := s.entries[s.sel].Name
				s.marked[n] = !s.marked[n]
				mark = false
				move(1)
			}
		case keys.Is("files.all", e):
			all := true
			for _, e := range s.entries {
				all = all && (e.Name == ".." || s.marked[e.Name])
			}
			for _, e := range s.entries {
				if e.Name != ".." {
					s.marked[e.Name] = !all
				}
			}
		case keys.Is("files.hidden", e):
			fv.hidden = !fv.hidden
			for _, side := range fv.sides {
				keep := ""
				if side.sel < len(side.entries) {
					keep = side.entries[side.sel].Name
				}
				fv.refilter(side, keep)
			}
		case keys.Is("files.view", e), e.Name == key.NameReturn, e.Name == key.NameEnter:
			after = func() { u.open(fv, s) }
		case keys.Is("files.edit", e):
			after = func() { u.edit(fv, s) }
		case keys.Is("files.copy", e):
			after = func() { u.copyChosen(fv, false) }
		case keys.Is("files.move", e):
			after = func() { u.copyChosen(fv, true) }
		case keys.Is("files.rename", e):
			u.startPrompt(gtx, fv, "rename")
		case keys.Is("files.mkdir", e):
			u.startPrompt(gtx, fv, "mkdir")
		case keys.Is("files.chmod", e):
			u.startPrompt(gtx, fv, "chmod")
		case keys.Is("files.delete", e):
			if len(s.chosen()) > 0 {
				fv.prompt = "delete"
			}
		case keys.Is("files.reload", e):
			after = func() { u.loadSide(fv, s, s.path, "") }
		case e.Name == key.NameUpArrow:
			move(-1)
		case e.Name == key.NameDownArrow:
			move(1)
		case e.Name == key.NamePageUp:
			move(-page)
		case e.Name == key.NamePageDown:
			move(page)
		case e.Name == key.NameHome:
			move(-len(s.entries))
		case e.Name == key.NameEnd:
			move(len(s.entries))
		case e.Name == key.NameTab:
			fv.active, fv.search = 1-fv.active, ""
		case e.Name == key.NameDeleteBackward:
			if searching {
				_, size := lastRune(fv.search)
				fv.search, fv.searchAt = fv.search[:len(fv.search)-size], time.Now()
				s.jump(fv.search)
			} else {
				after = func() { u.up(fv, s) }
			}
		case e.Name == key.NameEscape:
			switch {
			case searching:
				fv.search = ""
			case fv.cancel != nil:
				fv.cancel()
				fv.queue = nil
			default:
				after = func() { u.closeFiles(fv) }
			}
		}
		fv.mu.Unlock()
		if after != nil {
			after()
		}
	}
	if fv.editing() {
		for {
			ev, ok := u.nameIn.Update(gtx)
			if !ok {
				break
			}
			if sub, ok := ev.(widget.SubmitEvent); ok {
				kind := fv.prompt
				fv.prompt = ""
				name := strings.TrimSpace(sub.Text)
				if kind == "mkdir" {
					u.mkdir(fv, name)
				} else if kind == "chmod" {
					u.chmod(fv, name)
				} else {
					u.rename(fv, name)
				}
				gtx.Execute(key.FocusCmd{Tag: fv})
			}
		}
		for {
			ev, ok := gtx.Event(key.Filter{Focus: &u.nameIn, Name: key.NameEscape})
			if !ok {
				break
			}
			if e, ok := ev.(key.Event); ok && e.State == key.Press {
				fv.prompt = ""
				gtx.Execute(key.FocusCmd{Tag: fv})
			}
		}
	}
}

func lastRune(s string) (rune, int) {
	r := []rune(s)
	if len(r) == 0 {
		return 0, 0
	}
	return r[len(r)-1], len(string(r[len(r)-1]))
}

// typeAhead jumps to the entry whose name starts with what's typed; a pause
// of a second starts a new search.
func (u *ui) typeAhead(fv *filesView, t string) {
	if fv.prompt != "" || strings.TrimSpace(t) == "" || strings.ContainsFunc(t, unicode.IsControl) {
		return
	}
	fv.mu.Lock()
	if time.Since(fv.searchAt) > time.Second {
		fv.search = ""
	}
	fv.search += strings.ToLower(t)
	fv.searchAt = time.Now()
	fv.sides[fv.active].jump(fv.search)
	fv.mu.Unlock()
	time.AfterFunc(1100*time.Millisecond, u.redraw) // to drop the "find" hint
}

// filesPanels draws the split: a title row, both sides and the function-key bar.
func (u *ui) filesPanels(gtx layout.Context, fv *filesView) layout.Dimensions {
	u.filesKeys(gtx, fv)
	// a press anywhere in the panels gives them the keyboard; under the rows
	area := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	event.Op(gtx.Ops, fv)
	area.Pop()
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: fv, Kinds: pointer.Press})
		if !ok {
			break
		}
		if _, ok := ev.(pointer.Event); ok && !fv.editing() {
			gtx.Execute(key.FocusCmd{Tag: fv})
		}
	}
	fv.mu.Lock()
	d := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.filesTitle(gtx, fv) }),
		layout.Rigid(vgap(1)),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return u.fileSidePanel(gtx, fv, 0) }),
				layout.Rigid(hgap(1)),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return u.fileSidePanel(gtx, fv, 1) }))
		}),
		layout.Rigid(vgap(1)),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.filesBar(gtx, fv) }))
	if fv.dragging != fv.dragSeen {
		defer u.redraw() // the drop highlights follow one frame behind
	}
	fv.dragging, fv.dragSeen = fv.dragSeen, false
	after := fv.after
	fv.after = nil
	fv.mu.Unlock()
	for _, f := range after {
		f()
	}
	return d
}

// filesTitle is the split's header, the height of a pane's: what it is, and
// its close button.
func (u *ui) filesTitle(gtx layout.Context, fv *filesView) layout.Dimensions {
	c := cText2
	if gtx.Focused(fv) {
		c = cAccText
	}
	gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, gtx.Dp(28)))
	paint.FillShape(gtx.Ops, cPane, clip.Rect{Max: gtx.Constraints.Max}.Op())
	return layout.Inset{Left: 10, Right: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(icon(14, c, icoFolder)),
			layout.Rigid(hgap(8)),
			layout.Rigid(u.lbl("Directory", 12, c, fSemi(sansFont))),
			layout.Rigid(hgap(8)),
			layout.Rigid(u.lbl(fv.name+" · this machine", 12, cText3, sansFont)),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }),
			layout.Rigid(u.lbl("esc closes", 11, cText3, sansFont)),
			layout.Rigid(hgap(6)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d, ok := u.button(gtx, fmt.Sprintf("closefiles%p", fv), color.NRGBA{}, cHover, rCtl, layout.UniformInset(4), icon(13, cText2, icoX))
				if ok {
					fv.after = append(fv.after, func() { u.closeFiles(fv) })
				}
				return d
			}))
	})
}

// fileKind gives a file's type badge text and color, by extension (or name).
func fileKind(e fileEntry) (string, color.NRGBA) {
	name := strings.ToLower(e.Name)
	ext := strings.TrimPrefix(path.Ext(name), ".")
	if strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".tar.xz") || strings.HasSuffix(name, ".tar.zst") {
		ext = "tgz"
	}
	switch name {
	case "dockerfile", "containerfile":
		return "DKR", kindConfig
	case "makefile", "justfile":
		return "MK", kindCode
	case ".bashrc", ".zshrc", ".profile", ".bash_profile", ".bash_aliases":
		return "SH", kindScript
	case "authorized_keys", "known_hosts", "id_rsa", "id_ed25519", "id_ecdsa":
		return "KEY", kindKey
	}
	short := map[string]string{"yaml": "YML", "jpeg": "JPG", "markdown": "MD", "docx": "DOC", "xlsx": "XLS", "pptx": "PPT",
		"sqlite3": "DB", "sqlite": "DB", "service": "UNIT", "timer": "UNIT", "socket": "UNIT", "properties": "PROP"}
	tag := strings.ToUpper(ext)
	if s, ok := short[ext]; ok {
		tag = s
	}
	if r := []rune(tag); len(r) > 4 {
		tag = string(r[:4])
	}
	for _, k := range fileKinds {
		if strings.Contains(" "+k.exts+" ", " "+ext+" ") {
			return tag, k.c
		}
	}
	if e.Mode&0o111 != 0 && ext == "" {
		return "EXE", kindScript
	}
	return tag, cText3
}

var (
	kindCode   = rgb(0x30d158)
	kindScript = rgb(0xa3e635)
	kindConfig = rgb(0xbf5af2)
	kindKey    = rgb(0xff9f0a)
	fileKinds  = []struct {
		exts string
		c    color.NRGBA
	}{
		{"go rs c h cc cpp hpp java kt swift py rb php js mjs ts tsx jsx vue svelte lua pl ex exs erl hs scala cs dart zig nim r jl", kindCode},
		{"sh bash zsh fish ps1 bat cmd", kindScript},
		{"yml yaml toml json jsonc ini conf cfg env xml nix hcl tf tfvars properties plist lock service timer socket mount rules", kindConfig},
		{"pem key crt cer csr pub p12 pfx gpg asc jks kdbx", kindKey},
		{"md markdown txt rst org adoc tex rtf", rgb(0xd1d1d6)},
		{"pdf", rgb(0xff6961)},
		{"doc docx odt xls xlsx ods ppt pptx odp csv tsv", rgb(0x5ac8fa)},
		{"sql sqlite sqlite3 db dump parquet bak", rgb(0x64d2ff)},
		{"png jpg jpeg gif svg webp bmp ico tif tiff heic avif psd", rgb(0xff6fa8)},
		{"mp3 wav flac ogg m4a aac mp4 mkv mov avi webm", rgb(0xff8a65)},
		{"zip tar gz tgz bz2 xz zst 7z rar deb rpm apk iso img dmg jar war whl", rgb(0xffd60a)},
		{"log out err pid", rgb(0x98989d)},
	}
)

func withAlpha(c color.NRGBA, a uint8) color.NRGBA { c.A = a; return c }

// fillStroke fills draw's outline and strokes it.
func fillStroke(gtx layout.Context, px int, fill, stroke color.NRGBA, draw func(p *clip.Path, s float32)) {
	var p clip.Path
	p.Begin(gtx.Ops)
	draw(&p, float32(px))
	spec := p.End()
	paint.FillShape(gtx.Ops, fill, clip.Outline{Path: spec}.Op())
	paint.FillShape(gtx.Ops, stroke, clip.Stroke{Path: spec, Width: float32(gtx.Dp(1.3))}.Op())
}

// fileIcon draws an entry's icon: a folder, or a page with a colored
// badge of its type, the way Finder labels documents.
func (u *ui) fileIcon(e fileEntry) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		px := gtx.Dp(22)
		dim := uint8(0xff)
		if strings.HasPrefix(e.Name, ".") && e.Name != ".." {
			dim = 0x99
		}
		switch {
		case e.Name == "..":
			icon(22, cText3, icoUpDir)(gtx)
		case e.Dir:
			c := withAlpha(cAccText, dim)
			fillStroke(gtx, px, withAlpha(cAccText, dim/4), c, icoFolder)
		default:
			tag, c := fileKind(e)
			fillStroke(gtx, px, withAlpha(cHover, dim), withAlpha(c, dim*2/3), icoPage)
			if tag != "" {
				gtx := gtx
				gtx.Constraints.Min = image.Point{}
				m := op.Record(gtx.Ops)
				ld := u.label(gtx, tag, 6.5, rgb(0x111214), fBold(sansFont), 1)
				call := m.Stop()
				w, h := ld.Size.X+gtx.Dp(4), ld.Size.Y
				r := image.Rect((px-w)/2, px-h-gtx.Dp(2), (px+w)/2, px-gtx.Dp(2))
				fillRRect(gtx.Ops, r, gtx.Dp(2), withAlpha(c, dim))
				t := op.Offset(image.Pt(r.Min.X+gtx.Dp(2), r.Min.Y)).Push(gtx.Ops)
				call.Add(gtx.Ops)
				t.Pop()
			}
		}
		if e.Link {
			t := op.Offset(image.Pt(0, px/2)).Push(gtx.Ops)
			icon(11, cWhite, icoLinkArrow)(gtx)
			t.Pop()
		}
		return layout.Dimensions{Size: image.Pt(px, px)}
	}
}

var (
	icoFolder = func(p *clip.Path, s float32) {
		p.MoveTo(f32.Pt(s*.1, s*.24))
		p.LineTo(f32.Pt(s*.4, s*.24))
		p.LineTo(f32.Pt(s*.5, s*.34))
		p.LineTo(f32.Pt(s*.9, s*.34))
		p.LineTo(f32.Pt(s*.9, s*.8))
		p.LineTo(f32.Pt(s*.1, s*.8))
		p.Close()
	}
	icoPage = func(p *clip.Path, s float32) {
		p.MoveTo(f32.Pt(s*.2, s*.06))
		p.LineTo(f32.Pt(s*.6, s*.06))
		p.LineTo(f32.Pt(s*.8, s*.26))
		p.LineTo(f32.Pt(s*.8, s*.94))
		p.LineTo(f32.Pt(s*.2, s*.94))
		p.Close()
	}
	icoUpDir = func(p *clip.Path, s float32) { // ↰
		p.MoveTo(f32.Pt(s*.75, s*.8))
		p.LineTo(f32.Pt(s*.75, s*.4))
		p.LineTo(f32.Pt(s*.3, s*.4))
		p.MoveTo(f32.Pt(s*.45, s*.24))
		p.LineTo(f32.Pt(s*.28, s*.4))
		p.LineTo(f32.Pt(s*.45, s*.56))
	}
	icoLinkArrow = func(p *clip.Path, s float32) { // ↗
		p.MoveTo(f32.Pt(s*.2, s*.8))
		p.LineTo(f32.Pt(s*.8, s*.2))
		p.MoveTo(f32.Pt(s*.4, s*.2))
		p.LineTo(f32.Pt(s*.8, s*.2))
		p.LineTo(f32.Pt(s*.8, s*.6))
	}
)

const (
	colSize = 76  // dp
	colDate = 118 // dp
)

// col is a right-aligned label of width w.
func (u *ui) col(s string, w unit.Dp, c color.NRGBA) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X, gtx.Constraints.Max.X, gtx.Constraints.Min.Y = gtx.Dp(w), gtx.Dp(w), 0
		return widget.Label{MaxLines: 1, Alignment: text.End}.Layout(gtx, u.sh, monoFont, 12, s, mat(gtx, c))
	}
}

// fileDate is a modification time the way ls shows it: the time this year, the year before.
func fileDate(t time.Time) string {
	switch {
	case t.IsZero():
		return ""
	case t.Year() == time.Now().Year():
		return t.Format("Jan _2 15:04")
	}
	return t.Format("Jan _2  2006")
}

func (u *ui) fileSidePanel(gtx layout.Context, fv *filesView, side int) layout.Dimensions {
	s := fv.sides[side]
	// the whole panel is a drop target: copies land in its directory
	for {
		ev, ok := gtx.Event(transfer.TargetFilter{Target: s, Type: filesMime}, pointer.Filter{Target: s, Kinds: pointer.Enter | pointer.Leave})
		if !ok {
			break
		}
		switch ev := ev.(type) {
		case pointer.Event:
			s.dropHover = ev.Kind == pointer.Enter
		case transfer.CancelEvent:
			s.dropHover = false
		case transfer.DataEvent:
			ev.Open().Close()
			s.dropHover = false
			u.drop(fv, side, s.path)
		}
	}
	dropping := fv.dragging && fv.drag != nil && fv.drag.from != side
	active := side == fv.active && gtx.Focused(fv)
	rim := color.NRGBA{}
	switch {
	case dropping && s.dropHover:
		rim = cAcc
	case side == fv.active:
		rim = cAccSoft
	}
	sz := gtx.Constraints.Max
	panel(gtx, image.Rectangle{Max: sz}, 0, cPane, rim)
	pass := pointer.PassOp{}.Push(gtx.Ops) // presses still reach filesPanels' focus handler below
	area := clip.Rect{Max: sz}.Push(gtx.Ops)
	event.Op(gtx.Ops, s)
	pass.Pop()
	defer area.Pop()
	wide := sz.X > gtx.Dp(460)
	title, tc, tbg := fv.name, cOrange, cOrangeSoft
	if !s.remote {
		title, tc, tbg = "this machine", cAccText, cAccSoft
	}
	d := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 5, Bottom: 3, Left: 10, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				info := fmt.Sprintf("%d items", max(0, len(s.all)))
				if h := s.hiddenCount(); h > 0 && !fv.hidden {
					info += fmt.Sprintf(" · %d hidden", h)
				}
				ic := cText3
				if side == fv.active && fv.search != "" && time.Since(fv.searchAt) < time.Second {
					info, ic = "find: "+fv.search, cAccText
				}
				if s.loading {
					info = "loading…"
				}
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(u.pill(title, tc, tbg)),
					layout.Rigid(hgap(8)),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return u.crumbs(gtx, fv, side) }),
					layout.Rigid(hgap(8)),
					layout.Rigid(u.lbl(info, 11, ic, sansFont)))
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.fileHeader(gtx, fv, side, wide) }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			paint.FillShape(gtx.Ops, cLine, clip.Rect(image.Rect(0, 0, gtx.Constraints.Max.X, gtx.Dp(1))).Op())
			return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, gtx.Dp(1))}
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			if s.err != "" {
				return layout.UniformInset(14).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return u.label(gtx, s.err, 12, cRed, sansFont, 0)
				})
			}
			if s.follow && s.list.Position.Count > 0 {
				if s.sel < s.list.Position.First {
					s.list.Position.First, s.list.Position.Offset = s.sel, 0
				} else if last := s.list.Position.First + s.list.Position.Count - 2; s.sel > last {
					s.list.Position.First += s.sel - last
				}
				s.follow = false
			}
			return layout.Stack{}.Layout(gtx,
				layout.Expanded(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: 2, Bottom: 2, Left: 4, Right: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return s.list.Layout(gtx, len(s.entries), func(gtx layout.Context, i int) layout.Dimensions {
							return u.fileRowLayout(gtx, fv, side, i, active, wide)
						})
					})
				}),
				layout.Stacked(func(gtx layout.Context) layout.Dimensions {
					hint := ""
					switch {
					case s.loading && len(s.entries) == 0:
						hint = "Loading…"
					case !s.loading && s.path != "" && len(s.entries) <= 1:
						hint = "Empty folder"
						if len(s.all) > 0 {
							hint = "Only hidden files: " + keys.Short("files.hidden") + " shows them"
						}
					}
					if hint == "" {
						return layout.Dimensions{}
					}
					gtx.Constraints.Min = gtx.Constraints.Max
					return layout.Center.Layout(gtx, u.lbl(hint, 13, cText3, sansFont))
				}))
		}))
	if dropping && s.dropHover {
		fillRRect(gtx.Ops, image.Rectangle{Max: sz}, 0, withAlpha(cAcc, 0x14))
	}
	return d
}

// drop copies what's being dragged into dir on side to; mu held.
func (u *ui) drop(fv *filesView, to int, dir string) {
	dr := fv.drag
	if dr == nil || dr.from == to || dir == "" {
		return
	}
	fv.after = append(fv.after, func() { u.transfer(fv, dr.from, dr.items, dir, false, false) })
}

// crumbs is the clickable path: each folder jumps there.
func (u *ui) crumbs(gtx layout.Context, fv *filesView, side int) layout.Dimensions {
	s := fv.sides[side]
	type seg struct{ name, to string }
	var segs []seg
	if s.path != "" && !s.posix() { // this machine's path: C:\Users\me on Windows
		for p := filepath.Clean(s.path); ; p = filepath.Dir(p) {
			if filepath.Dir(p) == p {
				segs = append([]seg{{p, p}}, segs...)
				break
			}
			segs = append([]seg{{filepath.Base(p), p}}, segs...)
		}
	} else if s.path != "" {
		segs = append(segs, seg{"/", "/"})
		cur := ""
		for _, part := range strings.Split(strings.Trim(s.path, "/"), "/") {
			if part != "" {
				cur += "/" + part
				segs = append(segs, seg{part, cur})
			}
		}
	}
	if home, _ := os.UserHomeDir(); !s.remote && home != "" && home != "/" {
		for i, sg := range segs {
			if sg.to == home {
				segs = append([]seg{{"~", home}}, segs[i+1:]...)
				break
			}
		}
	}
	if len(segs) > 5 {
		segs = append([]seg{{"…", ""}}, segs[len(segs)-4:]...)
	}
	defer clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops).Pop()
	var kids []layout.FlexChild
	for i, sg := range segs {
		if i > 0 && segs[i-1].name != "/" {
			kids = append(kids, layout.Rigid(u.lbl(" › ", 12, cText3, sansFont)))
		}
		last := i == len(segs)-1
		c, f := cText2, monoFont
		if last {
			c, f = cText, fSemi(monoFont)
		}
		if sg.to == "" || last {
			kids = append(kids, layout.Rigid(u.lbl(sg.name, 12, c, f)))
			continue
		}
		child := segs[i+1].name
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			d, clicked := u.button(gtx, fmt.Sprintf("crumb%d-%d", side, i), color.NRGBA{}, cHover, rCtl, layout.Inset{Top: 1, Bottom: 1, Left: 3, Right: 3}, u.lbl(sg.name, 12, c, f))
			if clicked {
				to := sg.to
				fv.after = append(fv.after, func() { u.loadSide(fv, s, to, child) })
			}
			return d
		}))
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
}

// fileHeader is the column titles; clicking one sorts by it (again: reversed).
func (u *ui) fileHeader(gtx layout.Context, fv *filesView, side int, wide bool) layout.Dimensions {
	s := fv.sides[side]
	head := func(by int, title string, w unit.Dp) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			c := cText3
			if s.sortBy == by {
				c = cText2
				title += map[bool]string{true: " ↓", false: " ↑"}[s.desc]
			}
			lw := u.lblFill(title, 11, c, fSemi(sansFont))
			if w > 0 {
				lw = func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X, gtx.Constraints.Max.X, gtx.Constraints.Min.Y = gtx.Dp(w), gtx.Dp(w), 0
					return widget.Label{MaxLines: 1, Alignment: text.End}.Layout(gtx, u.sh, fSemi(sansFont), 11, title, mat(gtx, c))
				}
			}
			d, clicked := u.button(gtx, fmt.Sprintf("fsort%d-%d", side, by), color.NRGBA{}, color.NRGBA{}, 0, layout.Inset{Top: 2, Bottom: 3}, lw)
			if clicked {
				if s.sortBy == by {
					s.desc = !s.desc
				} else {
					s.sortBy, s.desc = by, by != 0 // biggest and newest first
				}
				keep := ""
				if s.sel < len(s.entries) {
					keep = s.entries[s.sel].Name
				}
				fv.refilter(s, keep)
			}
			return d
		}
	}
	kids := []layout.FlexChild{layout.Rigid(hgap(30)), layout.Flexed(1, head(0, "Name", 0)), layout.Rigid(head(1, "Size", colSize))}
	if wide {
		kids = append(kids, layout.Rigid(head(2, "Modified", colDate)))
	}
	return layout.Inset{Left: 14, Right: 16}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
	})
}

func (u *ui) fileRowLayout(gtx layout.Context, fv *filesView, side, i int, active, wide bool) layout.Dimensions {
	s := fv.sides[side]
	e := s.entries[i]
	rs := s.rows[e.Name]
	if rs == nil {
		rs = &fileRow{}
		rs.drag.Type = filesMime
		s.rows[e.Name] = rs
	}
	for {
		cl, ok := rs.click.Update(gtx)
		if !ok {
			break
		}
		fv.active, fv.search = side, ""
		gtx.Execute(key.FocusCmd{Tag: fv})
		switch {
		case cl.Modifiers.Contain(key.ModShortcut) && e.Name != "..":
			s.marked[e.Name] = !s.marked[e.Name]
			s.sel, s.anchor = i, i
		case cl.Modifiers.Contain(key.ModShift):
			for j := min(s.anchor, i); j <= max(s.anchor, i) && j < len(s.entries); j++ {
				if s.entries[j].Name != ".." {
					s.marked[s.entries[j].Name] = true
				}
			}
			s.sel = i
		default:
			s.sel, s.anchor = i, i
			if cl.NumClicks >= 2 {
				fv.after = append(fv.after, func() { u.open(fv, s) })
			}
		}
	}
	// a folder (or "..") takes drops into itself
	for e.Dir {
		ev, ok := gtx.Event(transfer.TargetFilter{Target: rs, Type: filesMime}, pointer.Filter{Target: rs, Kinds: pointer.Enter | pointer.Leave})
		if !ok {
			break
		}
		switch ev := ev.(type) {
		case pointer.Event:
			rs.hover = ev.Kind == pointer.Enter
		case transfer.CancelEvent:
			rs.hover = false
		case transfer.DataEvent:
			ev.Open().Close()
			rs.hover = false
			to := s.join(e.Name)
			if e.Name == ".." {
				to = s.parent()
			}
			u.drop(fv, side, to)
		}
	}
	if mime, ok := rs.drag.Update(gtx); ok {
		rs.drag.Offer(gtx, mime, io.NopCloser(strings.NewReader("")))
	}
	// the drag gesture grabs the pointer once it moves past its slop, which
	// cancels everyone else's press: that's when a drag starts
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &rs.moved, Kinds: pointer.Press | pointer.Release | pointer.Cancel})
		if !ok {
			break
		}
		if pe, ok := ev.(pointer.Event); ok {
			rs.moved = pe.Kind == pointer.Cancel && rs.drag.Dragging()
		}
	}
	if !rs.drag.Dragging() {
		rs.moved = false
	}
	if rs.moved && e.Name != ".." {
		if !fv.dragging && !fv.dragSeen { // a new drag: the marked rows if it started on one, else this row
			items := []fileEntry{e}
			if s.marked[e.Name] {
				items = s.chosen()
			}
			fv.drag = &dragInfo{from: side, items: items}
			s.sel, fv.active = i, side
		}
		fv.dragSeen = true
	}
	dropInto := fv.dragging && fv.drag != nil && fv.drag.from != side && e.Dir && rs.hover
	fill := color.NRGBA{}
	switch {
	case dropInto:
		fill = cAcc
	case i == s.sel && active:
		fill = cAccSoft
	case i == s.sel:
		fill = cHover
	case rs.click.Hovered():
		fill = cCard
	}
	row := func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return rs.click.Layout(gtx, boxed(fill, color.NRGBA{}, 3, layout.Inset{Top: 2, Bottom: 2, Left: 6, Right: 8}, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			nc, nf := cText, monoFont
			_, kc := fileKind(e)
			switch {
			case s.marked[e.Name]:
				nc, nf = cOrange, fBold(monoFont)
			case strings.HasPrefix(e.Name, ".") && e.Name != "..":
				nc = cText3
			case !e.Dir && e.Mode&0o111 != 0 && kc == kindScript:
				nc = cGreen // executables, as ls colors them
			}
			if dropInto {
				nc = cWhite
			}
			size := humanSize(e.Size)
			if e.Dir {
				size = ""
			}
			kids := []layout.FlexChild{
				layout.Rigid(u.fileIcon(e)),
				layout.Rigid(hgap(8)),
				layout.Flexed(1, u.lblFill(e.Name, 13, nc, nf)),
				layout.Rigid(u.col(size, colSize, cText3)),
			}
			if wide {
				date := ""
				if e.Name != ".." {
					date = fileDate(e.Mod)
				}
				kids = append(kids, layout.Rigid(u.col(date, colDate, cText3)))
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
		}))
	}
	if e.Name == ".." {
		d := row(gtx)
		u.dropArea(gtx, rs, d.Size)
		return d
	}
	ghost := func(gtx layout.Context) layout.Dimensions {
		if !rs.moved || fv.drag == nil {
			return layout.Dimensions{}
		}
		gtx.Constraints.Min = image.Point{}
		what := e.Name
		if n := len(fv.drag.items); n > 1 {
			what = plural(n)
		}
		to := map[bool]string{true: "this machine", false: fv.name}[s.remote]
		return boxed(withAlpha(cAcc, 0xe8), color.NRGBA{}, rCtl, layout.Inset{Top: 6, Bottom: 6, Left: 10, Right: 12}, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(u.fileIcon(e)),
				layout.Rigid(hgap(8)),
				layout.Rigid(u.lbl(what, 13, cWhite, fSemi(sansFont))),
				layout.Rigid(hgap(10)),
				layout.Rigid(u.lbl("copy to "+to, 11, withAlpha(cWhite, 0xbb), sansFont)))
		})(gtx)
	}
	pass := pointer.PassOp{}.Push(gtx.Ops) // the row's clicks still reach its Clickable under the drag handle
	d := rs.drag.Layout(gtx, row, ghost)
	if e.Dir {
		u.dropArea(gtx, rs, d.Size)
	}
	st := clip.Rect{Max: d.Size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &rs.moved)
	st.Pop()
	pass.Pop()
	return d
}

// dropArea registers row rs as a drop target the size of sz.
func (u *ui) dropArea(gtx layout.Context, rs *fileRow, sz image.Point) {
	pass := pointer.PassOp{}.Push(gtx.Ops)
	st := clip.Rect{Max: sz}.Push(gtx.Ops)
	event.Op(gtx.Ops, rs)
	st.Pop()
	pass.Pop()
}

// fkey is one of the bar's function-key buttons.
func (u *ui) fkey(gtx layout.Context, fv *filesView, k, title string, act func()) layout.Dimensions {
	d, clicked := u.button(gtx, "fkey"+k, cPanelCard, cHover, rCtl, layout.Inset{Top: 3, Bottom: 3, Left: 7, Right: 8}, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
			layout.Rigid(u.lbl(k, 10, cText3, fSemi(monoFont))),
			layout.Rigid(hgap(6)),
			layout.Rigid(u.lbl(title, 12, cText, fMedium(sansFont))))
	})
	if clicked {
		fv.after = append(fv.after, act)
	}
	return d
}

// filesBar is the F-key bar, with a delete, conflict or overwrite question
// as a banner above it.
func (u *ui) filesBar(gtx layout.Context, fv *filesView) layout.Dimensions {
	b := u.filesBanner(fv)
	if b == nil {
		return u.fkeyBar(gtx, fv)
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(b), layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.fkeyBar(gtx, fv) }))
}

// filesBanner is the question fv.prompt asks, nil for none (or one asked in
// the bar). Its buttons' actions run once mu is free, as the F-keys' do.
func (u *ui) filesBanner(fv *filesView) layout.Widget {
	s := fv.sides[fv.active]
	later := func(f func()) func() { return func() { fv.after = append(fv.after, f) } }
	answer := func(c string) func() { return later(func() { fv.mu.Lock(); fv.answer(c); fv.mu.Unlock() }) }
	orange := []color.NRGBA{mix(cPanel, cPane, 0.15), cLine, cOrangeSoft, mix(cOrange, cWhite, 0.35), cText3}
	switch fv.prompt {
	case "delete":
		title, sub := deleteText(s, fv.name)
		return u.banner([]color.NRGBA{cRedSoft, mix(cRed, cPane, 0.6), mix(cRed, cPane, 0.72), cRedText, mix(cRedText, cText3, 0.4)}, kit.IcoTrash, title, sub,
			u.kb("fkeyN", kit.Secondary, "Cancel", "esc", later(func() { fv.mu.Lock(); fv.prompt = ""; fv.mu.Unlock() })), layout.Rigid(hgap(8)),
			u.kb("fkeyY", kit.Danger, "Delete "+plural(len(s.chosen())), "Y", later(func() { fv.mu.Lock(); fv.prompt = ""; fv.mu.Unlock(); u.deleteChosen(fv) })))
	case "overwrite":
		return u.banner(orange, kit.IcoWarn, fv.conflicts[0]+" changed on "+fv.name+" since you opened it", "Upload yours over it, or keep the server's copy.",
			u.kb("fkeyesc", kit.Ghost, "Keep the server's", "esc", answer("")), layout.Rigid(hgap(8)),
			u.kb("fkeyO", kit.Primary, "Overwrite", "O", answer("replace")))
	case "conflict":
		n := max(fv.conflictOf, len(fv.conflicts))
		title, sub := fv.conflictText()
		seg := func(id, label, k string, on bool, c string) layout.FlexChild {
			return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				fill, fg := color.NRGBA{}, cText2
				if on {
					fill, fg = cHover, cText
				}
				d, ok := u.button(gtx, id, fill, cHover, 3, layout.Inset{Top: 4, Bottom: 4, Left: 10, Right: 10}, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx, layout.Rigid(u.lbl(label, 12, fg, fSemi(sansFont))), layout.Rigid(hgap(6)), layout.Rigid(u.keyCap(k, false)))
				})
				if ok {
					answer(c)()
				}
				return d
			})
		}
		return u.banner(orange, kit.IcoWarn, title, sub,
			// Replace is what enter does, so it shows chosen
			layout.Rigid(boxed(cPane, cLine, rCtl, layout.UniformInset(2), func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{}.Layout(gtx, seg("fkeyO", "Replace", "O", true, "replace"), seg("fkeyS", "Skip them", "S", false, "skip"))
			})), layout.Rigid(hgap(8)),
			u.kb("fkeyesc", kit.Ghost, "Cancel", "esc", answer("")), layout.Rigid(hgap(8)),
			u.kb("fileCopy", kit.Primary, "Copy "+plural(n), "↵", answer("replace")))
	}
	return nil
}

// banner is a question across the files view: colors are its background,
// top edge, icon tile, icon and second line.
func (u *ui) banner(cs []color.NRGBA, ico func(p *clip.Path, s float32), title, sub string, actions ...layout.FlexChild) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Background{}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			sz := gtx.Constraints.Min
			paint.FillShape(gtx.Ops, cs[0], clip.Rect{Max: sz}.Op())
			paint.FillShape(gtx.Ops, cs[1], clip.Rect{Max: image.Pt(sz.X, gtx.Dp(1))}.Op())
			return layout.Dimensions{Size: sz}
		}, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Inset{Top: 14, Bottom: 14, Left: 18, Right: 18}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				row := []layout.FlexChild{
					layout.Rigid(iconTile(30, 15, rCard, cs[2], cs[3], ico)), layout.Rigid(hgap(14)),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.label(gtx, title, 13, cText, fBold(sansFont), 2) }),
							layout.Rigid(vgap(2)),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.label(gtx, sub, 12, cs[4], sansFont, 3) }))
					}),
					layout.Rigid(hgap(14)),
				}
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx, append(row, actions...)...)
			})
		})
	}
}

// deleteText is the delete question for side s's chosen items (host: the
// server's name): where, then the names, folders with "/", files with sizes.
func deleteText(s *fileSide, host string) (title, sub string) {
	ch := s.chosen()
	var ns []string
	for _, e := range ch {
		switch {
		case e.Dir:
			ns = append(ns, e.Name+"/")
		case e.Size >= 0:
			ns = append(ns, e.Name+" ("+humanSize(e.Size)+")")
		default:
			ns = append(ns, e.Name)
		}
	}
	here, trash := "this machine", "It skips the trash: this can't be undone."
	if s.remote {
		here, trash = host, "The server has no trash: this can't be undone."
	}
	return fmt.Sprintf("Delete %s from %s on %s?", plural(len(ch)), s.path, here), andList(ns, 3) + ". " + trash
}

// conflictText is the conflict question: how many of the batch are there
// already, which, and where the batch goes.
func (fv *filesView) conflictText() (title, sub string) {
	title = fmt.Sprintf("%d of %s already there", len(fv.conflicts), plural(max(fv.conflictOf, len(fv.conflicts))))
	sub = andList(fv.conflicts, 3) + "."
	if fv.conflictTo != "" {
		sub += " Copying to " + fv.conflictTo + "."
	}
	return title, sub
}

// andList joins up to n names as "a, b and c", "… and 4 more" past them.
func andList(ns []string, n int) string {
	if len(ns) > n {
		return strings.Join(ns[:n], ", ") + fmt.Sprintf(" and %d more", len(ns)-n)
	}
	if len(ns) < 2 {
		return strings.Join(ns, "")
	}
	return strings.Join(ns[:len(ns)-1], ", ") + " and " + ns[len(ns)-1]
}

// fkeyBar is the F-keys with what's marked, or a name prompt's field.
func (u *ui) fkeyBar(gtx layout.Context, fv *filesView) layout.Dimensions {
	return boxed(cPanel, color.NRGBA{}, 0, layout.Inset{Top: 4, Bottom: 4, Left: 10, Right: 6}, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		s := fv.sides[fv.active]
		other := map[bool]string{true: "this machine", false: fv.name}[s.remote]
		act := func(k, title string, f func()) layout.FlexChild {
			return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return u.fkey(gtx, fv, k, title, f) })
			})
		}
		switch fv.prompt {
		case "mkdir", "rename", "chmod":
			title := map[string]string{"mkdir": "New folder:", "rename": "Rename to:", "chmod": "Permissions (644, 755, u+x…):"}[fv.prompt]
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(u.lbl(title, 13, cText, fSemi(sansFont))),
				layout.Rigid(hgap(10)),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return u.editor(gtx, &u.nameIn, "name", 13, monoFont, cText)
				}),
				layout.Rigid(u.lbl("↵ ok · esc cancel", 12, cText3, sansFont)))
		}
		var rows []layout.FlexChild
		if j := fv.job; j != nil {
			rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.jobProgress(gtx, fv, j) }), layout.Rigid(vgap(8)))
		}
		summary := "Drag rows to the other side to copy · " + keys.Cap("files.mark") + " or ⌃click marks · type to find"
		if runtime.GOOS == "darwin" {
			summary = "Drag rows to the other side to copy · " + keys.Cap("files.mark") + " or ⌘click marks · type to find"
		}
		var n int
		var bytes int64
		for _, e := range s.entries {
			if s.marked[e.Name] {
				n++
				bytes += max(0, e.Size)
			}
		}
		if n > 0 {
			summary = fmt.Sprintf("%s marked · %s of files", plural(n), humanSize(bytes))
		}
		rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, u.lblFill(summary, 12, cText3, sansFont)),
				act(keys.Cap("files.view"), "Open", func() { u.open(fv, fv.sides[fv.active]) }),
				act(keys.Cap("files.edit"), "Edit", func() { u.edit(fv, fv.sides[fv.active]) }),
				act(keys.Cap("files.copy"), "Copy to "+other, func() { u.copyChosen(fv, false) }),
				act(keys.Cap("files.move"), "Move", func() { u.copyChosen(fv, true) }),
				act(keys.Cap("files.rename"), "Rename", func() { fv.mu.Lock(); u.startPrompt(gtx, fv, "rename"); fv.mu.Unlock() }),
				act(keys.Cap("files.mkdir"), "New folder", func() { fv.mu.Lock(); u.startPrompt(gtx, fv, "mkdir"); fv.mu.Unlock() }),
				act(keys.Cap("files.chmod"), "Chmod", func() { fv.mu.Lock(); u.startPrompt(gtx, fv, "chmod"); fv.mu.Unlock() }),
				act(keys.Cap("files.delete"), "Delete", func() {
					fv.mu.Lock()
					if len(fv.sides[fv.active].chosen()) > 0 {
						fv.prompt = "delete"
					}
					fv.mu.Unlock()
				}))
		}))
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
	})(gtx)
}

// jobProgress is the running batch: what's moving, a bar, speed, and cancel.
func (u *ui) jobProgress(gtx layout.Context, fv *filesView, j *copyJob) layout.Dimensions {
	cur := j.n.Load()
	moved := j.done + cur
	frac := -1.0
	switch {
	case j.batch > 0:
		frac = float64(moved) / float64(j.batch)
	case j.total > 0:
		frac = float64(cur) / float64(j.total)
	}
	what := j.verb + " " + j.label
	if j.count > 1 {
		what += fmt.Sprintf("  (%d of %d)", j.i+1, j.count)
	}
	if len(fv.queue) > 0 {
		what += fmt.Sprintf("  · %d queued", len(fv.queue))
	}
	stat := humanSize(moved)
	if j.batch > 0 {
		stat += " of " + humanSize(j.batch)
	}
	if secs := time.Since(j.started).Seconds(); secs > 1 && moved > 0 {
		rate := float64(moved) / secs
		stat += fmt.Sprintf(" · %s/s", humanSize(int64(rate)))
		if j.batch > 0 && rate > 0 {
			stat += fmt.Sprintf(" · %s left", (time.Duration(float64(j.batch-moved)/rate) * time.Second).Round(time.Second))
		}
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(u.lbl(what, 12, cAccText, fSemi(sansFont))),
		layout.Rigid(hgap(14)),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			w, h := gtx.Constraints.Max.X, gtx.Dp(5)
			fillRRect(gtx.Ops, image.Rect(0, 0, w, h), h/2, cLine)
			if frac >= 0 {
				fillRRect(gtx.Ops, image.Rect(0, 0, int(float64(w)*min(1, frac)), h), h/2, cAcc)
			}
			return layout.Dimensions{Size: image.Pt(w, h)}
		}),
		layout.Rigid(hgap(14)),
		layout.Rigid(u.lbl(stat, 12, cText2, monoFont)),
		layout.Rigid(hgap(8)),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.fkey(gtx, fv, "esc", "Cancel", func() {
				fv.mu.Lock()
				if fv.cancel != nil {
					fv.cancel()
				}
				fv.queue = nil
				fv.mu.Unlock()
			})
		}))
}
