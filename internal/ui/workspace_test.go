package ui

import (
	"image"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"

	"github.com/alecthomas/chroma/v2"
	"github.com/oligo/gvcode/textstyle/syntax"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/ui/kit"
	"github.com/oligo/gvcode/gutter/providers"
)

// wsCard puts a card of Acme's api on the board, with a file in its folder;
// in progress already, so a send doesn't move it (and the pointer stays its).
func wsCard(h *harness) (*kanban.Card, string) {
	dir := filepath.Join(h.u.root, "Acme", "api")
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"), 0o640)
	c := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Fix it", Org: "Acme", Dir: "api"})
	return h.u.b.Card(c.ID), dir
}

// wsClaudes are the workspace Claudes the harness started.
func (h *harness) wsClaudes() (out []*fakeClaude) {
	for _, f := range h.claudes {
		if strings.Contains(f.opts.System, "workspace Claude of card") {
			out = append(out, f)
		}
	}
	return out
}

// mainClaudes are the cards' own sessions it started.
func (h *harness) mainClaudes() (out []*fakeClaude) {
	for _, f := range h.claudes {
		if !strings.Contains(f.opts.System, "workspace Claude of card") {
			out = append(out, f)
		}
	}
	return out
}

func (h *harness) tool(id, name, args string) (text string, ok bool) {
	h.off(func() { text, ok = h.u.cardTool(id, name, []byte(args)) })
	return text, ok
}

// TestAddProject: the main Claude's add_project takes the repository root
// above the path, once, never the card's own folder; the toast undoes it;
// the workspace Claude has no such tool.
func TestAddProject(t *testing.T) {
	h := newHarness(t)
	c, own := wsCard(h)
	repo := filepath.Join(h.u.root, "Globex", "lib")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.MkdirAll(filepath.Join(repo, "pkg", "x"), 0o755)
	if text, ok := h.tool(c.ID, "add_project", `{"path":`+strconv.Quote(filepath.Join(repo, "pkg", "x"))+`}`); !ok || !strings.Contains(text, "Added "+repo) {
		t.Fatalf("add: %q %v", text, ok)
	}
	if got := h.u.b.Card(c.ID).Projects; !slices.Equal(got, []string{repo}) {
		t.Fatalf("projects %v", got)
	}
	if !strings.Contains(h.u.toast, "Added lib to "+c.Key()+"'s workspace") || h.u.undo == nil {
		t.Fatalf("toast %q", h.u.toast)
	}
	for _, p := range []string{repo, own, filepath.Join(own, "v2")} {
		if text, ok := h.tool(c.ID, "add_project", `{"path":`+strconv.Quote(p)+`}`); !ok || !strings.Contains(text, "already") {
			t.Errorf("%s again: %q %v", p, text, ok)
		}
	}
	if len(h.u.b.Card(c.ID).Projects) != 1 {
		t.Fatalf("a duplicate was added: %v", h.u.b.Card(c.ID).Projects)
	}
	beside := own + "x" // starts like the card's folder, isn't in it
	os.MkdirAll(beside, 0o755)
	if h.tool(c.ID, "add_project", `{"path":`+strconv.Quote(beside)+`}`); !slices.Contains(h.u.b.Card(c.ID).Projects, beside) {
		t.Fatal("a folder beside the card's taken for one inside it")
	}
	h.u.removeProject(c.ID, beside)
	plain := filepath.Join(h.u.root, "Globex")
	if _, ok := h.tool(c.ID, "add_project", `{"path":`+strconv.Quote(plain)+`}`); !ok || !slices.Contains(h.u.b.Card(c.ID).Projects, plain) {
		t.Fatalf("a folder with no repository above: %v", h.u.b.Card(c.ID).Projects)
	}
	h.u.runUndo()
	if got := h.u.b.Card(c.ID).Projects; !slices.Equal(got, []string{repo}) {
		t.Fatalf("undo: %v", got)
	}
	for _, args := range []string{`{"path":"rel/dir"}`, `{"path":"` + filepath.Join(own, "main.go") + `"}`, `{"path":"/no/such"}`} {
		if text, ok := h.tool(c.ID, "add_project", args); ok || !strings.Contains(text, "not") {
			t.Errorf("%s: added (%q)", args, text)
		}
	}
	if text, ok := h.tool("ws:"+c.ID, "add_project", `{"path":`+strconv.Quote(plain)+`}`); ok || text != "no tool add_project" {
		t.Errorf("the workspace Claude's: %q %v", text, ok)
	}
	if text, ok := h.tool(c.ID, "suggest_to_card", `{"text":"x"}`); ok || text != "no tool suggest_to_card" {
		t.Errorf("the main Claude's suggest: %q %v", text, ok)
	}
	if text, ok := h.tool("nope", "add_project", `{"path":`+strconv.Quote(plain)+`}`); ok {
		t.Errorf("a card not on the board: %q", text)
	}
}

// TestProjectRootStopsAtHome: a .git in the home folder (dotfiles) isn't a project.
func TestProjectRootStopsAtHome(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".git"), 0o755)
	d := filepath.Join(home, "Work", "x")
	os.MkdirAll(d, 0o755)
	if got, err := projectRoot(d, home); err != nil || got != d {
		t.Errorf("got %q %v", got, err)
	}
	os.MkdirAll(filepath.Join(home, "Work", ".git"), 0o755)
	if got, _ := projectRoot(d, home); got != filepath.Join(home, "Work") {
		t.Errorf("repo: %q", got)
	}
}

// TestAskWorkspace: ask_workspace starts the workspace Claude (read-only,
// the projects added, its session kept), hands it the question, and its
// answer comes into the main session as a workspace entry; the user's own
// questions stay in the workspace.
func TestAskWorkspace(t *testing.T) {
	h := newHarness(t)
	c, own := wsCard(h)
	extra := t.TempDir()
	c.Projects = []string{extra}
	h.u.send(c.ID, "start")
	if text, ok := h.tool(c.ID, "ask_workspace", `{"question":"What is open?"}`); !ok || !strings.HasPrefix(text, "Asked") {
		t.Fatalf("ask: %q %v", text, ok)
	}
	ws := h.wsClaudes()
	if len(ws) != 1 {
		t.Fatalf("%d workspace Claudes", len(ws))
	}
	f := ws[0]
	o := f.opts
	args := strings.Join(o.Args, " ")
	if o.Dir != own || !strings.Contains(args, "--add-dir="+extra) || !strings.Contains(args, "--allowedTools=Read,Glob,Grep,Skill") ||
		!strings.Contains(args, "--disallowedTools=Edit,Write,NotebookEdit,Bash") || o.New == "" || o.New != h.u.b.Card(c.ID).WSSession {
		t.Fatalf("options %+v", o)
	}
	if !slices.Equal(f.prompts, []string{"The card's main Claude asks:\nWhat is open?"}) {
		t.Fatalf("prompts %q", f.prompts)
	}
	if m := h.u.chat(wsKey(c.ID)); len(m) != 1 || m[0].Who != "main" || m[0].Text != "What is open?" {
		t.Fatalf("workspace chat %+v", m)
	}
	main := h.mainClaudes()[0]
	sent := len(main.prompts)
	f.on(claude.Event{Kind: claude.KindInit, Session: "ws-sess"})
	f.on(claude.Event{Kind: claude.KindText, Text: "main.go, line 3."})
	f.on(claude.Event{Kind: claude.KindResult})
	h.frame()
	if got := main.prompts[sent:]; len(got) != 1 || got[0] != "From the workspace Claude:\nmain.go, line 3." {
		t.Fatalf("main got %q", got)
	}
	if ms := h.u.chat(c.ID); ms[len(ms)-1].Who != "workspace" || ms[len(ms)-1].Text != "main.go, line 3." {
		t.Fatalf("main chat %+v", ms[len(ms)-1])
	}
	if h.u.b.Card(c.ID).WSSession != "ws-sess" {
		t.Errorf("session %q", h.u.b.Card(c.ID).WSSession)
	}
	h.u.wsSend(c.ID, "Is it right?")
	f.on(claude.Event{Kind: claude.KindText, Text: "Yes."})
	f.on(claude.Event{Kind: claude.KindAsk, Ask: &claude.Request{ID: "r1", Tool: "Bash", Line: "Bash rm x"}})
	f.on(claude.Event{Kind: claude.KindResult})
	h.frame()
	if len(main.prompts) != sent+1 {
		t.Errorf("the user's question went to the main Claude: %q", main.prompts[sent:])
	}
	if !slices.Equal(f.replies, []string{"r1 deny The workspace Claude only reads: propose the change instead."}) {
		t.Errorf("replies %q", f.replies)
	}
	if text, ok := h.tool("ws:"+c.ID, "suggest_to_card", `{"text":"Refresh first."}`); !ok || !strings.HasPrefix(text, "Sent") {
		t.Fatalf("suggest: %q %v", text, ok)
	}
	if got := main.prompts[len(main.prompts)-1]; got != "From the workspace Claude:\nRefresh first." {
		t.Errorf("suggestion: %q", got)
	}
	if text, ok := h.tool(c.ID, "ask_workspace", `{}`); ok || text != "nothing to send" {
		t.Errorf("empty question: %q", text)
	}
	f.on(claude.Event{Kind: claude.KindExit, Text: "boom"})
	h.frame()
	if h.u.ws[c.ID].proc != nil {
		t.Fatal("exited, still kept")
	}
	h.u.wsSend(c.ID, "again")
	ws = h.wsClaudes()
	if len(ws) != 2 || ws[1].opts.New != "" && ws[1].opts.Resume != "" {
		t.Fatalf("restart: %d", len(ws))
	}
	ws[1].on(claude.Event{Kind: claude.KindResult})
	h.frame()
	if h.u.ws[c.ID].proc != ws[1] {
		t.Fatal("stopped after a turn, with no project change")
	}
}

// TestWorkspaceHeldAnswer: an answer for a card whose terminal is up waits
// with the infra Claude's, each saying whose it is.
func TestWorkspaceHeldAnswer(t *testing.T) {
	h := newHarness(t)
	c, _ := wsCard(h)
	h.u.send(c.ID, "start")
	h.u.shellStop[c.ID] = func() {} // a "!" command running holds it too
	h.u.wsTell(c.ID, "early")
	if !strings.Contains(h.u.peerOut[c.ID], "early") {
		t.Fatal("sent over a running command")
	}
	delete(h.u.shellStop, c.ID)
	h.u.peerOut[c.ID] = ""
	h.u.asks[c.ID] = &askState{}
	h.u.wsTell(c.ID, "hello")
	if h.u.peerOut[c.ID] != "From the workspace Claude:\nhello\n\n" {
		t.Fatalf("held %q", h.u.peerOut[c.ID])
	}
	delete(h.u.asks, c.ID)
	main := h.mainClaudes()[0]
	h.u.send(c.ID, "next")
	if got := main.prompts[len(main.prompts)-1]; got != "From the workspace Claude:\nhello\n\nnext" {
		t.Fatalf("sent %q", got)
	}
}

// TestWorkspaceRestartsWithNewProjects: a project added while the
// workspace Claude runs restarts it (resumed) before the next question.
func TestWorkspaceRestartsWithNewProjects(t *testing.T) {
	h := newHarness(t)
	c, _ := wsCard(h)
	h.u.wsSend(c.ID, "one")
	extra := t.TempDir()
	h.u.addProject(c.ID, extra)
	f := h.wsClaudes()[0]
	f.on(claude.Event{Kind: claude.KindResult})
	h.frame()
	if h.u.ws[c.ID].proc != nil {
		t.Fatal("not stopped after its turn")
	}
	h.u.wsSend(c.ID, "two")
	ws := h.wsClaudes()
	if len(ws) != 2 || !strings.Contains(strings.Join(ws[1].opts.Args, " "), "--add-dir="+extra) {
		t.Fatalf("restarted %d", len(ws))
	}
	ws[1].on(claude.Event{Kind: claude.KindResult})
	h.frame()
	h.u.addProject(c.ID, t.TempDir())
	h.u.wsSend(c.ID, "three") // idle: restarts at once
	if len(h.wsClaudes()) != 3 {
		t.Fatal("not restarted when idle")
	}
	old := ws[1]
	old.on(claude.Event{Kind: claude.KindExit}) // the replaced one's last words
	h.frame()
	if h.u.ws[c.ID].proc == nil {
		t.Fatal("an old process's exit dropped the new one")
	}
	third := h.wsClaudes()[2]
	third.on(claude.Event{Kind: claude.KindInit})                    // no session id: the card's stays
	third.on(claude.Event{Kind: claude.KindResult, Text: "the end"}) // a plain end, not an error
	h.frame()
	if h.u.ws[c.ID].proc != third || h.u.b.Card(c.ID).WSSession == "" {
		t.Fatal("restarted fresh, then stopped again or lost its session")
	}
	for _, m := range h.u.chat(wsKey(c.ID)) {
		if m.Who == "error" {
			t.Fatalf("a plain result shown as an error: %q", m.Text)
		}
	}
	if st := h.u.ws[c.ID]; !st.chat.ScrollToEnd || st.chat.Axis != layout.Vertical || st.tree.Axis != layout.Vertical {
		t.Error("lists")
	}
}

// TestWorkspaceView: the session header's button and the card menu's W
// open it; Esc goes back to the session; its title says so.
func TestWorkspaceView(t *testing.T) {
	h := newHarness(t)
	c, _ := wsCard(h)
	h.u.openSession(c.ID)
	h.clickButton("pnWS")
	if h.u.view != "ws:"+c.ID || h.u.viewTitle() != c.Key()+" · workspace" || !strings.Contains(h.u.sentence(), "Esc goes back") {
		t.Fatalf("view %q", h.u.view)
	}
	h.send(press(key.NameEscape, 0))
	if h.u.view != "sess:"+c.ID {
		t.Fatalf("esc: %q", h.u.view)
	}
	h.u.view = ""
	h.u.menu = &cardMenu{id: c.ID, at: image.Pt(400, 200)}
	h.frame() // draws the items; the next frame listens for their keys
	h.frame()
	h.send(press("W", 0))
	if h.u.view != "ws:"+c.ID {
		t.Fatalf("menu W: %q", h.u.view)
	}
	h.clickButton("wsBack")
	if h.u.view != "sess:"+c.ID {
		t.Fatalf("back: %q", h.u.view)
	}
	h.u.archiveCard(c.ID)
	if h.u.view != "" {
		t.Errorf("archived, still shown: %q", h.u.view)
	}
}

// TestWorkspaceTree: a folder is read when expanded (not before, not per
// frame), .git, node_modules, .worktrees, __pycache__ and .venv stay out,
// folders come first, a click on a file opens it.
func TestWorkspaceTree(t *testing.T) {
	h := newHarness(t)
	c, dir := wsCard(h)
	for _, d := range []string{".git", "node_modules", ".worktrees", "__pycache__", ".venv", "pkg"} {
		os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	os.WriteFile(filepath.Join(dir, "pkg", "a.go"), []byte("package pkg\n"), 0o644)
	h.u.openWS(c.ID)
	h.frame()
	st := h.u.ws[c.ID]
	var names []string
	for _, e := range st.kids[dir] {
		names = append(names, e.name)
	}
	if !slices.Equal(names, []string{"pkg", "v2", "main.go"}) {
		t.Fatalf("entries %v", names)
	}
	if _, ok := st.kids[filepath.Join(dir, "pkg")]; ok {
		t.Fatal("read before it was expanded")
	}
	h.clickButton("wsRow" + filepath.Join(dir, "pkg"))
	if len(st.kids[filepath.Join(dir, "pkg")]) != 1 {
		t.Fatal("not read when expanded")
	}
	for _, r := range h.u.wsRows(h.u.b.Card(c.ID), st) {
		if r.e.name == "a.go" && r.depth != 2 || r.e.path == dir && (r.depth != 0 || r.root != 1 || !r.e.dir) {
			t.Errorf("row %+v", r)
		}
	}
	h.clickButton("wsRow" + filepath.Join(dir, "pkg", "a.go"))
	if tb := st.tab(); tb == nil || tb.path != filepath.Join(dir, "pkg", "a.go") || tb.ed.Text() != "package pkg\n" {
		t.Fatalf("not opened: %+v", tb)
	}
	h.clickButton("wsRow" + filepath.Join(dir, "pkg"))
	if _, ok := st.kids[filepath.Join(dir, "pkg")]; ok || st.open[filepath.Join(dir, "pkg")] {
		t.Fatal("collapsed, still kept")
	}
	h.clickButton("wsRow" + dir) // the project's own row folds it
	if st.open[dir] || len(st.tabs) != 1 {
		t.Fatal("the project row didn't fold")
	}
	extra := t.TempDir()
	h.u.addProject(c.ID, extra)
	h.frame()
	h.clickButton("wsDrop" + extra)
	if len(h.u.b.Card(c.ID).Projects) != 0 {
		t.Fatal("not removed from the tree")
	}
}

// editorAt is a point inside the workspace's editor.
var editorAt = f32.Pt(900, 400)

// layoutCtx is a context over the harness's input, for what reads focus.
func layoutCtx(h *harness) layout.Context { return layout.Context{Source: h.r.Source()} }

// TestWorkspaceEditSave: typing marks the tab, Ctrl+S writes the file in
// place (its mode kept) and clears the mark.
func TestWorkspaceEditSave(t *testing.T) {
	h := newHarness(t)
	c, dir := wsCard(h)
	p := filepath.Join(dir, "main.go")
	h.u.openWS(c.ID)
	tb := h.u.wsOpen(h.u.wsOf(c.ID), p)
	h.frame()
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: editorAt},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: editorAt})
	tb.ed.SetCaret(0, 0)
	tb.closing = true
	h.send(key.EditEvent{Text: "// x\n"})
	if !tb.dirty || tb.closing || !h.u.wsTyping(layoutCtx(h)) {
		t.Fatalf("dirty %v closing %v", tb.dirty, tb.closing)
	}
	if tb.gen == 0 {
		t.Fatal("the edit didn't bump the text's gen")
	}
	h.pumpUntil("the edit's tokens", func() bool { return tb.tokGen == tb.gen })
	if !h.u.quiet(layoutCtx(h)) {
		t.Fatal("board keys on in the workspace")
	}
	tb.disk, tb.closing = true, true
	h.send(press("S", key.ModShortcut))
	b, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(b), "// x\npackage main") || tb.dirty || tb.disk || tb.closing || h.u.toastBad || h.u.toast != "Saved main.go" {
		t.Fatalf("saved %q dirty %v toast %q", b, tb.dirty, h.u.toast)
	}
	if fi, _ := os.Stat(p); !fi.ModTime().Equal(tb.mtime) {
		t.Error("the saved file's time not kept: it would read as changed on disk")
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o640 && runtime.GOOS != "windows" { // no mode bits there
		t.Errorf("mode %v", fi.Mode())
	}
	if ents, _ := os.ReadDir(dir); slices.ContainsFunc(ents, func(e os.DirEntry) bool { return strings.HasPrefix(e.Name(), ".main.go") }) {
		t.Error("temp file left")
	}
}

// TestWorkspaceCloseDirtyTab: × on a tab with edits asks once, then drops it.
func TestWorkspaceCloseDirtyTab(t *testing.T) {
	h := newHarness(t)
	c, dir := wsCard(h)
	st := h.u.wsOf(c.ID)
	a := h.u.wsOpen(st, filepath.Join(dir, "main.go"))
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644)
	h.u.wsOpen(st, filepath.Join(dir, "b.txt"))
	a.dirty = true
	h.u.wsCloseTab(st, 0)
	if len(st.tabs) != 2 || !a.closing || h.u.toastBad || !strings.Contains(h.u.toast, "changes not saved") {
		t.Fatal("closed with edits, unasked")
	}
	h.u.wsCloseTab(st, 0)
	if len(st.tabs) != 1 || st.cur != 0 || st.tab().path != filepath.Join(dir, "b.txt") {
		t.Fatalf("tabs %d cur %d", len(st.tabs), st.cur)
	}
	if h.u.wsOpen(st, filepath.Join(dir, "b.txt")) != st.tabs[0] || len(st.tabs) != 1 {
		t.Error("opened twice")
	}
	os.WriteFile(filepath.Join(dir, "bin"), []byte{0, 1, 2}, 0o644)
	if h.u.wsOpen(st, filepath.Join(dir, "bin")) != nil || !strings.Contains(h.u.toast, "not a text file") {
		t.Errorf("binary: %q", h.u.toast)
	}
}

// TestWorkspaceReload: the main Claude's edit to an open file reloads a
// clean tab; over edits not saved the bar asks, Keep mine keeps them,
// Reload takes the disk's.
func TestWorkspaceReload(t *testing.T) {
	h := newHarness(t)
	c, dir := wsCard(h)
	p := filepath.Join(dir, "main.go")
	h.u.openWS(c.ID)
	tb := h.u.wsOpen(h.u.wsOf(c.ID), p)
	h.u.send(c.ID, "start")
	main := h.mainClaudes()[0]
	edit := func(text string) {
		os.WriteFile(p, []byte(text), 0o640)
		os.Chtimes(p, time.Now(), time.Now().Add(time.Duration(len(text))*time.Second))
		main.on(claude.Event{Kind: claude.KindTool, ToolID: "e" + text, Text: "Edit main.go", Path: p})
		main.on(claude.Event{Kind: claude.KindToolDone, ToolID: "e" + text})
		h.frame()
		h.frame()
	}
	edit("package one\n")
	if tb.ed.Text() != "package one\n" || tb.disk {
		t.Fatalf("not reloaded: %q", tb.ed.Text())
	}
	tb.ed.SetText("mine\n")
	tb.dirty = true
	edit("package two\n")
	if tb.ed.Text() != "mine\n" || !tb.disk {
		t.Fatalf("edits lost or not asked: %q %v", tb.ed.Text(), tb.disk)
	}
	h.clickButton("wsKeep")
	if tb.disk || tb.ed.Text() != "mine\n" {
		t.Fatal("Keep mine")
	}
	h.u.wsCheck(tb)
	if tb.disk {
		t.Fatal("asked again for the same change")
	}
	edit("package three\n")
	tb.closing = true
	h.clickButton("wsReload")
	if tb.ed.Text() != "package three\n" || tb.dirty || tb.disk || tb.closing {
		t.Fatalf("Reload: %q", tb.ed.Text())
	}
}

// TestOpenInWorkspace: an Edit's Open in workspace finds its new text in
// the file, selects it and marks its hunk; a change no longer there opens
// at the top with a note; Ask about this change fills the composer.
func TestOpenInWorkspace(t *testing.T) {
	h := newHarness(t)
	c, dir := wsCard(h)
	p := filepath.Join(dir, "main.go")
	h.u.sayMsg(c.ID, kanban.ChatMsg{Who: "tool", Text: "Edit main.go", Path: p, Edits: []kanban.Edit{{Old: "\tprintln(\"bye\")\n", New: "\tprintln(\"hi\")\n"}}})
	h.u.openSession(c.ID)
	h.clickButton("diffWS" + c.ID + "-0")
	if h.u.view != "ws:"+c.ID {
		t.Fatalf("view %q", h.u.view)
	}
	tb := h.u.ws[c.ID].tab()
	hs := tb.diff.GetAllHunks()
	if len(hs) != 1 || hs[0].StartLine != 3 || hs[0].EndLine != 3 || hs[0].Type != providers.DiffModified || tb.ed.SelectedText() != "\tprintln(\"hi\")\n" {
		t.Fatalf("hunks %+v selected %q", hs, tb.ed.SelectedText())
	}
	h.clickButton("wsAskChange")
	if in := h.u.ws[c.ID].in.Text(); !strings.Contains(in, "-\tprintln(\"bye\")\n+\tprintln(\"hi\")") {
		t.Fatalf("composer %q", in)
	}
	if !h.r.Source().Focused(&h.u.ws[c.ID].in) || !h.u.wsTyping(layoutCtx(h)) {
		t.Fatal("the composer doesn't have the keyboard")
	}
	h.send(key.EditEvent{Range: key.Range{Start: h.u.ws[c.ID].in.Len(), End: h.u.ws[c.ID].in.Len()}, Text: "Right?"}, press(key.NameReturn, 0))
	if f := h.wsClaudes(); len(f) != 1 || !strings.HasPrefix(f[0].prompts[0], "I'm viewing a change to "+p+".\n\n```diff") || !strings.HasSuffix(f[0].prompts[0], "Right?") {
		t.Fatalf("enter sent %d", len(f))
	}
	h.clickButton("wsDoneChange")
	if tb.edit != nil || len(tb.diff.GetAllHunks()) != 0 {
		t.Fatal("Done kept the change")
	}
	h.u.wsOpenEdit(c.ID, p, []kanban.Edit{{Old: "a", New: "not there"}})
	if tb.note == "" || len(tb.diff.GetAllHunks()) != 0 {
		t.Fatalf("not found: note %q", tb.note)
	}
	h.frame() // the note in the bar
	h.u.wsOpenEdit(c.ID, p, nil)
	if tb.edit == nil || tb.note == "" {
		t.Fatal("no edits replaced the change shown")
	}
	h.u.wsOpenEdit(c.ID, p, []kanban.Edit{{New: "nope"}, {New: "func main() {\n"}})
	if hs := tb.diff.GetAllHunks(); len(hs) != 1 || hs[0].Type != providers.DiffAdded || hs[0].StartLine != 2 {
		t.Fatalf("second edit: %+v", hs)
	}
}

// TestWorkspaceContext: the user's message carries the change viewed, else
// the selection with its lines, else the file and line; nothing open, none.
func TestWorkspaceContext(t *testing.T) {
	h := newHarness(t)
	c, dir := wsCard(h)
	st := h.u.wsOf(c.ID)
	if wsContext(st, "q") != "" {
		t.Fatal("context with nothing open")
	}
	tb := h.u.wsOpen(st, filepath.Join(dir, "main.go"))
	h.u.openWS(c.ID)
	h.frame() // the caret goes on shaped text
	tb.ed.SetCaret(14, 14)
	if got := wsContext(st, "q"); got != "I have "+tb.path+" open, at line 3." {
		t.Errorf("file: %q", got)
	}
	tb.ed.SetCaret(14, 36)
	if got := wsContext(st, "q"); got != "I have lines 3-4 of "+tb.path+" selected:\n```\nfunc main() {\n\tprintln\n```" {
		t.Errorf("selection: %q", got)
	}
	tb.ed.SetCaret(36, 14)
	if !strings.HasPrefix(wsContext(st, "q"), "I have lines 3-4") {
		t.Error("a backward selection")
	}
	e := kanban.Edit{Old: "a\n", New: "b\n"}
	tb.edit = &e
	d := changeText(tb.path, e)
	if got := wsContext(st, "q"); got != "I'm viewing this change to "+tb.path+":\n"+d {
		t.Errorf("change: %q", got)
	}
	if got := wsContext(st, d+"\n\nwhy?"); got != "I'm viewing a change to "+tb.path+"." {
		t.Errorf("change already in the message: %q", got)
	}
	tb.edit = nil
	tb.ed.SetCaret(0, 0)
	st.in.SetText("What does it do?")
	h.clickButton("wsSend")
	f := h.wsClaudes()[0]
	if !slices.Equal(f.prompts, []string{"I have " + tb.path + " open, at line 1.\n\nWhat does it do?"}) || st.in.Text() != "" {
		t.Fatalf("prompts %q", f.prompts)
	}
	if m := h.u.chat(wsKey(c.ID)); len(m) != 1 || m[0].Who != "you" || m[0].Text != "What does it do?" {
		t.Fatalf("chat %+v", m)
	}
}

// TestChromaTokens: tokens in runes (past a multibyte character), scopes
// the scheme knows; no lexer, none.
func TestChromaTokens(t *testing.T) {
	src := "// é\npackage main\n"
	toks := chromaTokens(src, "x.go")
	var kw string
	for _, tk := range toks {
		if tk.Scope == "KeywordNamespace" {
			kw = string([]rune(src)[tk.Start:tk.End])
		}
	}
	if kw != "package" {
		t.Fatalf("tokens %+v", toks)
	}
	for _, tk := range toks {
		if s := string([]rune(src)[tk.Start:tk.End]); strings.TrimSpace(s) == "" {
			t.Errorf("a token of only spaces: %+v", tk)
		}
	}
	if chromaTokens("x", "file.unknownext") != nil {
		t.Error("tokens without a lexer")
	}
	if cs := wsScheme(); len(cs.Scopes()) < 10 {
		t.Error("few scopes")
	}
	if textStyle(chroma.StyleEntry{Bold: chroma.Yes, Italic: chroma.Yes}) != syntax.Bold|syntax.Italic || textStyle(chroma.StyleEntry{}) != 0 {
		t.Error("bold, italic")
	}
}

// TestWorkspaceResumes: a workspace session that exists is resumed; with
// desk's tools socket it gets them, suggest_to_card among them.
func TestWorkspaceResumes(t *testing.T) {
	h := newHarness(t)
	c, _ := wsCard(h)
	h.u.home, h.u.cardSock = t.TempDir(), "/run/desk-x.sock"
	os.MkdirAll(filepath.Join(h.u.home, ".claude", "projects", "p"), 0o755)
	os.WriteFile(filepath.Join(h.u.home, ".claude", "projects", "p", "s1.jsonl"), nil, 0o644)
	c.WSSession = "s1"
	h.u.wsSend(c.ID, "hi")
	o := h.wsClaudes()[0].opts
	if o.Resume != "s1" || o.New != "" || !slices.Contains(o.Args, "--mcp-config") || !strings.Contains(strings.Join(o.Args, " "), "mcp__desk__suggest_to_card") {
		t.Fatalf("options %+v", o)
	}
	startClaude = func(claude.Options, func(claude.Event)) (session, error) { return &sendFails{}, nil }
	h.u.ws[c.ID].proc = nil
	h.u.wsSend(c.ID, "again")
	if h.u.toast != "Workspace Claude: pipe closed" || !h.u.toastBad {
		t.Errorf("toast %q", h.u.toast)
	}
}

// TestWorkspaceFailures: what can't open, save or start says so, and a
// card gone leaves nothing behind.
func TestWorkspaceFailures(t *testing.T) {
	h := newHarness(t)
	c, dir := wsCard(h)
	st := h.u.wsOf(c.ID)
	big := filepath.Join(dir, "big.txt")
	os.WriteFile(big, make([]byte, wsMaxFile+1), 0o644)
	if h.u.wsOpen(st, big) != nil || !strings.Contains(h.u.toast, "too big") {
		t.Errorf("big: %q", h.u.toast)
	}
	if h.u.wsOpen(st, filepath.Join(dir, "none")) != nil || !h.u.toastBad {
		t.Error("a missing file opened")
	}
	if _, _, err := readText(dir); err == nil {
		t.Error("a folder read as text")
	}
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0o755)
	p := filepath.Join(sub, "a.txt")
	os.WriteFile(p, []byte("a"), 0o644)
	tb := h.u.wsOpen(st, p)
	os.RemoveAll(sub)
	h.u.wsSave(tb)
	if !strings.HasPrefix(h.u.toast, "Not saved: ") {
		t.Errorf("save into a folder gone: %q", h.u.toast)
	}
	h.u.wsReload(tb)
	if !h.u.toastBad {
		t.Error("reload of a file gone")
	}
	h.u.wsCheck(tb) // gone: nothing to compare
	h.u.wsKeepMine(tb)
	os.MkdirAll(filepath.Join(dir, "adir"), 0o755) // a rename onto a folder fails after the temp file is written
	onDir := &wsTab{path: filepath.Join(dir, "adir"), ed: kit.CodeEditor(wsScheme()), mode: 0o644}
	h.u.wsSave(onDir)
	if !h.u.toastBad || !strings.HasPrefix(h.u.toast, "Not saved: ") {
		t.Errorf("rename failure: %q", h.u.toast)
	}
	if ents, _ := os.ReadDir(dir); slices.ContainsFunc(ents, func(e os.DirEntry) bool { return strings.HasPrefix(e.Name(), ".adir.") }) {
		t.Error("a failed save left its temp file")
	}
	os.WriteFile(filepath.Join(dir, "x.sh"), []byte("echo"), 0o755)
	os.Symlink(sub+"-none", filepath.Join(dir, "dangling"))
	os.Symlink(filepath.Join(h.u.root, "Globex"), filepath.Join(dir, "linked"))
	for _, e := range readTree(dir) {
		if e.name == "linked" && !e.dir || e.name == "dangling" && e.dir {
			t.Errorf("link %+v", e)
		}
	}
	startClaude = func(claude.Options, func(claude.Event)) (session, error) { return nil, os.ErrPermission }
	h.u.wsSend(c.ID, "hi")
	if !strings.HasPrefix(h.u.toast, "Workspace Claude: ") || len(h.u.chat(wsKey(c.ID))) != 0 {
		t.Errorf("start failure: %q", h.u.toast)
	}
	if h.u.wsAskCard(c.ID, "q") == nil {
		t.Error("ask with no Claude")
	}
	if text, ok := h.tool(c.ID, "ask_workspace", `{"question":"q"}`); ok || text != os.ErrPermission.Error() {
		t.Errorf("ask_workspace with no Claude: %q %v", text, ok)
	}
	h.u.wsSend(c.ID, "  ")
	h.u.wsTell("gone", "x")
	h.u.wsEvent("gone", nil, claude.Event{Kind: claude.KindText, Text: "x"})
	if _, err := h.u.wsStart("gone"); err == nil {
		t.Error("started for no card")
	}
	if _, err := h.u.addProject("gone", dir); err == nil {
		t.Error("a project for no card")
	}
	h.u.openWS("gone")
	h.u.view = "ws:gone"
	h.frame()
	if h.u.view != "ws:gone" {
		t.Error("view")
	}
}

// TestWorkspaceEvents: subagents' words stay out; a tool line shows; a
// failed turn says why, and the card's Claude hears it had no answer.
func TestWorkspaceEvents(t *testing.T) {
	h := newHarness(t)
	c, _ := wsCard(h)
	h.u.send(c.ID, "start")
	main := h.mainClaudes()[0]
	if err := h.u.wsAskCard(c.ID, "q"); err != nil {
		t.Fatal(err)
	}
	f := h.wsClaudes()[0]
	f.on(claude.Event{Kind: claude.KindText, Text: "sub", Parent: "t1"})
	f.on(claude.Event{Kind: claude.KindTool, Text: "Read a.go", Parent: "t1"})
	f.on(claude.Event{Kind: claude.KindTool, Text: "Read b.go", ToolID: "r"})
	f.on(claude.Event{Kind: claude.KindResult, Error: true, Text: "overloaded"})
	h.frame()
	var who []string
	for _, m := range h.u.chat(wsKey(c.ID)) {
		who = append(who, m.Who+":"+m.Text)
	}
	if !slices.Equal(who, []string{"main:q", "tool:Read b.go", "error:overloaded"}) {
		t.Fatalf("chat %q", who)
	}
	if got := main.prompts[len(main.prompts)-1]; got != "From the workspace Claude:\n(no answer: overloaded)" {
		t.Fatalf("main got %q", got)
	}
	h.u.openWS(c.ID)
	h.frame()
	h.u.wsSend(c.ID, "still there?")
	h.frame() // working
	id := c.ID
	h.u.archiveCard(id)
	if h.u.ws[id].proc != nil {
		t.Fatal("archived, its workspace Claude kept running")
	}
}

// TestWorkspaceTabs: a tab's click shows it (checking its file), its ×
// closes it; the session's own chat names the workspace Claude's words.
func TestWorkspaceTabs(t *testing.T) {
	h := newHarness(t)
	c, dir := wsCard(h)
	st := h.u.wsOf(c.ID)
	h.u.openWS(c.ID)
	h.frame()
	a, b := filepath.Join(dir, "main.go"), filepath.Join(dir, "b.txt")
	os.WriteFile(b, []byte("b\n"), 0o644)
	h.u.wsOpen(st, a)
	h.u.wsOpen(st, b)
	h.frame()
	os.WriteFile(a, []byte("changed\n"), 0o640)
	os.Chtimes(a, time.Now(), time.Now().Add(time.Hour))
	h.clickButton("wsTab" + a)
	if st.tab().path != a || st.tab().ed.Text() != "changed\n" {
		t.Fatalf("tab %q: %q", st.tab().path, st.tab().ed.Text())
	}
	h.clickButton("wsTabX" + a)
	if len(st.tabs) != 1 || st.tab().path != b {
		t.Fatalf("× left %d", len(st.tabs))
	}
	h.u.say(c.ID, "workspace", "From it.")
	h.u.say(wsKey(c.ID), "main", "asks")
	h.u.say(wsKey(c.ID), "claude", "answers")
	h.u.say(wsKey(c.ID), "claude", "more")
	h.frame()
	h.u.openSession(c.ID)
	h.frame()
}

// TestWorkspaceLexOffFrame: a file's tokens are lexed off the UI goroutine,
// one lex at a time; an edit made while one runs gets the next.
func TestWorkspaceLexOffFrame(t *testing.T) {
	h := newHarness(t)
	defer func(f func(string, string) []syntax.Token) { wsTokens = f }(wsTokens)
	calls, release := make(chan string, 4), make(chan struct{})
	wsTokens = func(src, path string) []syntax.Token {
		calls <- src
		<-release
		return nil
	}
	c, dir := wsCard(h)
	tb := h.u.wsOpen(h.u.wsOf(c.ID), filepath.Join(dir, "main.go"))
	h.u.wsLex(tb)
	if !tb.lexing || <-calls != tb.ed.Text() {
		t.Fatal("no lex started for the opened file")
	}
	tb.gen++
	h.u.wsLex(tb) // one is running: no second
	release <- struct{}{}
	h.pumpUntil("the first lex", func() bool { return !tb.lexing })
	if tb.tokGen != 0 || len(calls) != 0 {
		t.Fatalf("tokens of gen %d, %d more lexes", tb.tokGen, len(calls))
	}
	h.u.wsLex(tb) // the edit's turn
	<-calls
	h.u.wsLex(tb)
	release <- struct{}{}
	h.pumpUntil("the second lex", func() bool { return tb.tokGen == 1 && !tb.lexing })
	if h.u.wsLex(tb); tb.lexing || len(calls) != 0 {
		t.Fatal("lexed again with the tokens current")
	}
}
