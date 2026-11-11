package ui

import (
	"bytes"
	"errors"
	"github.com/lfdominguez/desk/internal/testwait"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/io/key"
	"gioui.org/io/transfer"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/draw"
)

// pasted presses Ctrl+V with no image on the clipboard and answers its
// text read with s.
func (h *harness) pasted(s string) {
	h.t.Helper()
	clipImage = func() ([]byte, string, error) { return nil, "", errors.New("text") }
	h.send(press("V", key.ModShortcut))
	req := false // ClipboardRequested says so once
	for end := time.Now().Add(testwait.Long); !req && time.Now().Before(end); {
		h.frame()
		req = h.r.ClipboardRequested()
	}
	if !req {
		h.t.Fatal("the clipboard's text was never asked for")
	}
	h.r.Queue(transfer.DataEvent{Type: "application/text", Open: func() io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }})
	h.frame()
}

func TestDrawCopyPaste(t *testing.T) {
	h := newHarness(t)
	a := rect(0, 0, 50, 50, "#ffc9c9")
	st, _ := h.drawing("D", a)
	h.send(press("C", key.ModShortcut))
	if _, _, ok := h.r.WriteClipboard(); ok {
		t.Fatal("nothing selected: nothing copied")
	}
	st.ed.Select(a.ID)
	h.send(press("C", key.ModShortcut))
	_, b, ok := h.r.WriteClipboard()
	if els, _, isCopy := draw.ParseClipboard(b); !ok || !isCopy || len(els) != 1 || els[0].ID != a.ID {
		t.Fatalf("excalidraw.com's clipboard: %s", b)
	}
	h.pasted(string(b))
	if n := len(st.ed.Scene.Elements); n != 2 {
		t.Fatalf("pasted: %d", n)
	}
	if p := st.ed.Scene.Elements[1]; p.ID == a.ID || !st.ed.Sel[p.ID] {
		t.Error("a copy, selected")
	}
	h.send(press("X", key.ModShortcut))
	if _, b, ok := h.r.WriteClipboard(); !ok || len(st.ed.Scene.Elements) != 1 || !bytes.Contains(b, []byte("excalidraw/clipboard")) {
		t.Errorf("cut: copied and gone (%d left)", len(st.ed.Scene.Elements))
	}
	st.ed.Select()
	h.send(press("X", key.ModShortcut)) // nothing to cut
	if len(st.ed.Scene.Elements) != 1 {
		t.Error("cut nothing")
	}
}

func TestDrawPasteText(t *testing.T) {
	h := newHarness(t)
	st, _ := h.drawing("D")
	h.pasted("  ")
	if len(st.ed.Scene.Elements) != 0 {
		t.Fatal("blank: nothing")
	}
	h.pasted(" Hello there ")
	els := st.ed.Scene.Elements
	if len(els) != 1 || els[0].Type != "text" || els[0].Text != "Hello there" || els[0].Width == 0 {
		t.Fatalf("a text: %+v", els)
	}
	c := st.drawCenter()
	if e := els[0]; e.X+e.Width/2 != c.X || e.Y+e.Height/2 != c.Y {
		t.Error("in the middle of the view")
	}
	st.pasteText = true // the drawing left before the clipboard answered
	delete(h.u.draws, st.path)
	h.u.drawPaste(st.path)
	done := false
	h.u.later(func() { done = true })
	h.pumped(func() bool { return done })
}

// saved replaces saveFile with one writing into a buffer.
func saved(t *testing.T) (*bytes.Buffer, *string) {
	var b bytes.Buffer
	name := ""
	was := saveFile
	saveFile = func(n string) (io.WriteCloser, error) { name = n; return nopWriter{&b}, nil }
	t.Cleanup(func() { saveFile = was })
	return &b, &name
}

type nopWriter struct{ io.Writer }

func (nopWriter) Close() error { return nil }

type badWriter struct{}

func (badWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }
func (badWriter) Close() error              { return nil }

func TestDrawExport(t *testing.T) {
	h := newHarness(t)
	st, _ := h.drawing("Plan", rect(0, 0, 50, 50, "#ffc9c9"))
	b, name := saved(t)
	h.clickButton("drawExport")
	if !st.exportOpen {
		t.Fatal("Export shows its choices")
	}
	for _, c := range []struct{ kind, head string }{{"png", "\x89PNG"}, {"svg", "<svg"}, {"excalidraw", `{`}} {
		b.Reset()
		st.exportOpen = true
		h.frame()
		h.clickButton("drawExport" + c.kind)
		h.pumped(func() bool { return h.u.toast != "" })
		if *name != "Plan."+c.kind || !strings.HasPrefix(b.String(), c.head) || h.u.toast != "Exported Plan."+c.kind || st.exportOpen {
			t.Errorf("%s: %q %q %q", c.kind, *name, b.String()[:min(8, b.Len())], h.u.toast)
		}
		h.u.toast = ""
	}
	if s, err := draw.Parse(mustExport(t, st, "excalidraw")); err != nil || len(s.Elements) != 1 {
		t.Error("the .excalidraw reads back")
	}
	saveFile = func(string) (io.WriteCloser, error) { return nil, nil } // cancelled
	h.u.drawExport(st, "svg")
	done := false
	h.u.later(func() { done = true })
	h.pumped(func() bool { return done })
	if h.u.toast != "" {
		t.Errorf("cancelled: quiet, %q", h.u.toast)
	}
	saveFile = func(string) (io.WriteCloser, error) { return badWriter{}, nil }
	h.u.drawExport(st, "png")
	h.pumped(func() bool { return h.u.toast != "" })
	if h.u.toast != "Couldn't export the drawing: disk full" || !h.u.toastBad {
		t.Errorf("%q", h.u.toast)
	}
	st.exportOpen = true
	h.send(press(key.NameEscape, 0))
	if st.exportOpen || h.u.view != "draw:"+st.path {
		t.Error("Esc closes the choices first")
	}
	st.exportOpen = true
	h.clickButton("drawLib")
	if st.exportOpen || !st.libOpen {
		t.Error("the library takes the corner")
	}
	h.clickButton("drawExport")
	if !st.exportOpen || st.libOpen {
		t.Error("and Export takes it back")
	}
}

func mustExport(t *testing.T, st *drawState, kind string) []byte {
	b, err := drawExportBytes(st, kind)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// picked replaces pickFile with one giving file f.
func picked(t *testing.T, f func() (io.ReadCloser, error)) {
	was := pickFile
	pickFile = func(...string) (io.ReadCloser, error) { return f() }
	t.Cleanup(func() { pickFile = was })
}

func TestDrawImport(t *testing.T) {
	h := newHarness(t)
	src := draw.NewScene()
	src.Elements = []*draw.Element{rect(0, 0, 50, 50, "#ffc9c9")}
	b, _ := src.Marshal()
	p := filepath.Join(t.TempDir(), "From the web.excalidraw")
	os.WriteFile(p, b, 0o644)
	picked(t, func() (io.ReadCloser, error) { return os.Open(p) })
	h.u.view = "dr:Acme"
	h.frame()
	h.clickButton("drImportAcme")
	h.pumped(func() bool { return strings.HasPrefix(h.u.view, "draw:") })
	if st := h.u.drawOpen(); drawName(st.path) != "From the web" || len(st.ed.Scene.Elements) != 1 {
		t.Fatalf("a new drawing, opened: %s", h.u.view)
	}
	h.u.drawImport(drawPlace{org: "Acme"}) // the same again: a name of its own
	h.pumped(func() bool { return drawName(h.u.drawOpen().path) == "From the web 2" })

	picked(t, func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(string(b))), nil }) // no file name
	h.u.drawImport(drawPlace{org: "Acme"})
	h.pumped(func() bool { return drawName(h.u.drawOpen().path) == "Imported" })

	picked(t, func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("{}")), nil })
	h.u.drawImport(drawPlace{org: "Acme"})
	h.pumped(func() bool { return h.u.toast != "" })
	if !strings.HasPrefix(h.u.toast, "Couldn't import Imported: not an excalidraw file") {
		t.Errorf("%q", h.u.toast)
	}
	h.u.toast = ""
	picked(t, func() (io.ReadCloser, error) { return nil, errors.New("no portal") })
	h.u.drawImport(drawPlace{org: "Acme"})
	h.pumped(func() bool { return h.u.toast != "" })
	if h.u.toast != "Couldn't open the file: no portal" {
		t.Errorf("%q", h.u.toast)
	}
	h.u.toast = ""
	picked(t, func() (io.ReadCloser, error) { return nil, nil }) // cancelled
	h.u.drawImport(drawPlace{org: "Acme"})
	done := false
	h.u.later(func() { done = true })
	h.pumped(func() bool { return done })
	if h.u.toast != "" {
		t.Errorf("cancelled: quiet, %q", h.u.toast)
	}
	os.MkdirAll(h.u.placeDir(drawPlace{org: "Acme", card: "c9"}), 0o755)
	os.WriteFile(filepath.Join(h.u.placeDir(drawPlace{org: "Acme", card: "c9"}), "Imported.excalidraw"), nil, 0o000)
	os.Chmod(h.u.placeDir(drawPlace{org: "Acme", card: "c9"}), 0o500) // read-only: it can't be written
	defer os.Chmod(h.u.placeDir(drawPlace{org: "Acme", card: "c9"}), 0o755)
	h.u.drawImported(drawPlace{org: "Acme", card: "c9"}, "Imported", b)
	if !strings.HasPrefix(h.u.toast, "Couldn't import Imported: ") {
		t.Errorf("unwritable: %q", h.u.toast)
	}
}

func TestDrawImportLibrary(t *testing.T) {
	h := newHarness(t)
	lib := `{"type":"excalidrawlib","version":2,"libraryItems":[{"id":"i1","status":"published","elements":[{"id":"e","type":"rectangle","width":10,"height":10}]},{"id":"i2","status":"published","elements":[]}]}`
	h.u.drawImported(drawPlace{org: "Acme"}, "Shapes", []byte(lib))
	if l := h.u.library("Acme"); len(l.lib.Items) != 2 || h.u.toast != "Added 2 to the library" {
		t.Fatalf("into the library: %q", h.u.toast)
	}
	if m, _ := draw.LoadLibrary(h.u.libPath("Acme")); len(m.Items) != 2 {
		t.Error("kept")
	}
	h.u.libs["Acme"].err, h.u.libs["Acme"].lib = errors.New("broken"), nil
	h.u.drawImported(drawPlace{org: "Acme"}, "Shapes", []byte(lib))
	if h.u.toast != "Couldn't read the library: broken" {
		t.Errorf("an unreadable library is left alone: %q", h.u.toast)
	}
}

func TestDrawShare(t *testing.T) {
	h := newHarness(t)
	h.u.cardSock = "/run/desk/cards.sock"
	st, _ := h.drawing("Plan", rect(0, 0, 50, 50, "#ffc9c9"))
	st.claude.hide = true
	h.frame()
	if _, ok := h.exact("Share"); !ok {
		t.Fatal("Share in the header")
	}
	if h.hasButton("drawShared") {
		t.Error("not shared yet: no link")
	}
	h.clickButton("drawShare")
	cs := h.drawClaudes()
	if len(cs) != 1 {
		t.Fatal("asks the drawing's Claude")
	}
	page := drawSide(st.path, "share.html")
	f := cs[0]
	if !strings.Contains(f.prompts[0], page) || strings.Contains(f.prompts[0], "update that artifact") || st.claude.hide {
		t.Errorf("publish the page, the chat shown: %q", f.prompts[0])
	}
	if !strings.Contains(strings.Join(f.opts.Args, " "), "Artifact,mcp__claude-artifacts") {
		t.Errorf("may publish: %v", f.opts.Args)
	}
	b, _ := os.ReadFile(page)
	if !bytes.Contains(b, []byte("<title>Plan</title>")) || !bytes.Contains(b, []byte("<svg")) || !bytes.Contains(b, []byte("--bg: #ffffff")) {
		t.Errorf("the page: %.300s", b)
	}
	url := "https://claude.ai/artifact/abc-123"
	h.u.drawEvent(st.path, f, claude.Event{Kind: claude.KindArtifact, Text: url})
	if st.side.Shared != url || loadSide(st.path).Shared != url {
		t.Fatal("the link kept on the drawing")
	}
	var opened []string
	was := openURL
	openURL = func(s string) { opened = append(opened, s) }
	defer func() { openURL = was }()
	h.frame()
	h.clickButton("drawShared")
	if len(opened) != 1 || opened[0] != url {
		t.Errorf("opens the page: %v", opened)
	}
	if _, ok := h.exact("Share again"); !ok {
		t.Error("shared: Share again")
	}
	h.clickButton("drawShare")
	if p := f.prompts[len(f.prompts)-1]; !strings.Contains(p, "update that artifact, "+url) {
		t.Errorf("in place: %q", p)
	}
	h.u.renameDrawing(st, "Roadmap")
	if _, err := os.Stat(drawSide(st.path, "share.html")); err != nil {
		t.Error("the page goes with a rename")
	}
	os.Remove(drawSide(st.path, "share.html"))
	os.Mkdir(drawSide(st.path, "share.html"), 0o755) // can't be written
	h.u.drawShare(st)
	if !strings.HasPrefix(h.u.toast, "Couldn't share the drawing: ") {
		t.Errorf("%q", h.u.toast)
	}
	os.Remove(drawSide(st.path, "share.html"))
	startClaude = func(claude.Options, func(claude.Event)) (session, error) { return nil, errors.New("no claude") }
	st.drawStop()
	h.u.drawShare(st)
	if h.u.toast != "Drawing's Claude: no claude" {
		t.Errorf("%q", h.u.toast)
	}
}
