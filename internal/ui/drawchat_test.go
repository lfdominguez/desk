package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/draw"
	"github.com/lfdominguez/desk/internal/kanban"
)

// drawClaudes are the drawings' Claudes the harness started.
func (h *harness) drawClaudes() (out []*fakeClaude) {
	for _, f := range h.claudes {
		if strings.Contains(f.opts.System, "You are the Claude of the drawing") {
			out = append(out, f)
		}
	}
	return out
}

// chatting opens drawing D with its chat shown.
func chatting(h *harness, els ...*draw.Element) *drawState {
	st, _ := h.drawing("D", els...)
	st.claude.hide = false
	h.frame()
	return st
}

func TestDrawChatSends(t *testing.T) {
	h := newHarness(t)
	h.u.cardSock = "/run/desk/cards.sock"
	r := rect(0, 0, 40, 40, "#ffffff")
	st := chatting(h, r)
	if _, ok := h.shown("Ask it to draw or change something"); !ok {
		t.Error("the empty chat says what it does")
	}
	if _, ok := h.shown("↵ sends with just your words"); !ok {
		t.Error("nothing selected")
	}
	h.clickButton("drawSend") // nothing typed: nothing sent
	if len(h.drawClaudes()) != 0 {
		t.Fatal("started for nothing")
	}
	st.ed.Select(r.ID)
	h.frame()
	if _, ok := h.shown("↵ sends with 1 selected"); !ok {
		t.Error("the selection goes along")
	}
	h.u.focusLater = &st.claude.in
	h.frame()
	if !h.u.drawTyping(layout.Context{Source: h.r.Source()}) {
		t.Error("focused on request: typing, the canvas's keys off")
	}
	h.send(key.EditEvent{Text: "make it red"}, press(key.NameReturn, 0))
	fs := h.drawClaudes()
	if len(fs) != 1 {
		t.Fatalf("%d drawing Claudes", len(fs))
	}
	f := fs[0]
	if !strings.HasPrefix(f.prompts[0], "<selected>\n"+r.ID+" rectangle") || !strings.HasSuffix(f.prompts[0], "</selected>\n\nmake it red") {
		t.Errorf("prompt %q", f.prompts[0])
	}
	o := f.opts
	j := strings.Join(o.Args, " ")
	if !strings.Contains(o.System, `drawing "D" (of organization Acme)`) || o.Dir != filepath.Join(h.u.root, "Acme") || o.New == "" || o.Resume != "" ||
		!strings.Contains(j, "draw="+st.path) || !strings.Contains(j, "mcp__desk__drawing_edit") || !strings.Contains(j, "--disallowedTools=Edit,Write,NotebookEdit,Bash") {
		t.Errorf("options %+v", o)
	}
	if loadSide(st.path).Session != o.New {
		t.Error("its session kept beside the drawing")
	}
	if st.claude.in.Text() != "" || st.claude.busy != 1 {
		t.Error("sent, working")
	}
	if _, ok := h.shown("Working…"); !ok {
		t.Error("says it works")
	}
	if b, _ := os.ReadFile(drawSide(st.path, "chat.jsonl")); !strings.Contains(string(b), `"make it red"`) {
		t.Errorf("chat beside the drawing: %s", b)
	}
	h.clickButton("drawSend") // empty box: nothing
	st.claude.in.SetText("and bigger")
	h.clickButton("drawSend")
	if len(f.prompts) != 2 || f.prompts[1] != "<selected>\n"+draw.Summary(&draw.Scene{Elements: []*draw.Element{st.ed.Scene.Find(r.ID)}})+"</selected>\n\nand bigger" {
		t.Errorf("the same process: %q", f.prompts)
	}
}

func TestDrawChatEvents(t *testing.T) {
	h := newHarness(t)
	st := chatting(h)
	st.claude.in.SetText("hi")
	h.clickButton("drawSend")
	f := h.drawClaudes()[0]
	key := drawChatKey(st.path)
	play := func(e claude.Event) { f.on(e); h.frame() }
	gen := st.ed.Gen
	play(claude.Event{Kind: claude.KindInit})
	if st.side.Session == "" || st.ed.Gen <= gen {
		t.Error("an init without an id keeps the one it had; every event redraws")
	}
	play(claude.Event{Kind: claude.KindInit, Session: "s2"})
	if st.side.Session != "s2" || loadSide(st.path).Session != "s2" {
		t.Error("its session id, kept")
	}
	play(claude.Event{Kind: claude.KindText, Text: "  Drawing it.  "})
	play(claude.Event{Kind: claude.KindText, Text: "sub's", Parent: "t1"})
	play(claude.Event{Kind: claude.KindText, Text: "  "})
	play(claude.Event{Kind: claude.KindTool, Tool: "mcp__desk__drawing_edit", Detail: "x"})
	play(claude.Event{Kind: claude.KindTool, Tool: "Read", Parent: "t1"})
	play(claude.Event{Kind: claude.KindAsk, Ask: &claude.Request{ID: "r1", Line: "Bash ls"}})
	play(claude.Event{Kind: claude.KindResult, Error: true, Text: "boom"})
	m := h.u.chat(key)
	var got []string
	for _, x := range m {
		got = append(got, x.Who+":"+x.Text)
	}
	if strings.Join(got, "|") != "you:hi|claude:Drawing it.|tool:"+m[2].Text+"|tool:refused Bash ls|error:boom" {
		t.Errorf("chat %q", got)
	}
	if f.replies[0] != "r1 deny The drawing's Claude only reads files and draws." || st.claude.busy != 0 {
		t.Errorf("refused, done: %v %d", f.replies, st.claude.busy)
	}
	play(claude.Event{Kind: claude.KindResult, Text: "done"})
	if st.claude.busy != 0 || len(h.u.chat(key)) != len(m) {
		t.Error("never below 0; a result's text is no error")
	}
	play(claude.Event{Kind: claude.KindResult, Error: true})
	if len(h.u.chat(key)) != len(m) {
		t.Error("an error without words says nothing")
	}
	other := &fakeClaude{}
	h.u.drawEvent(st.path, other, claude.Event{Kind: claude.KindText, Text: "stale"})
	h.u.drawEvent("/gone.excalidraw", f, claude.Event{Kind: claude.KindText, Text: "gone"})
	if len(h.u.chat(key)) != len(m) {
		t.Error("a replaced process's, a closed drawing's: dropped")
	}
	play(claude.Event{Kind: claude.KindExit, Text: "crashed"})
	if st.claude.proc != nil || h.u.chat(key)[len(h.u.chat(key))-1].Text != "crashed" {
		t.Error("exit: said, the next message starts it again")
	}
	st.claude.in.SetText("again")
	h.clickButton("drawSend")
	if fs := h.drawClaudes(); len(fs) != 2 || fs[1].opts.New != "s2" {
		t.Errorf("a new process with its session: %+v", fs[len(fs)-1].opts)
	}
	play2 := h.drawClaudes()[1]
	h.u.drawEvent(st.path, play2, claude.Event{Kind: claude.KindExit})
	if n := len(h.u.chat(key)); h.u.chat(key)[n-1].Who == "error" && h.u.chat(key)[n-1].Text == "" {
		t.Error("a quiet exit says nothing")
	}
}

func TestDrawChatShows(t *testing.T) {
	h := newHarness(t)
	st := chatting(h)
	key := drawChatKey(st.path)
	for i := range 40 {
		h.u.say(key, "claude", fmt.Sprintf("line %d", i))
	}
	h.u.say(key, "error", "the last")
	h.frame()
	if _, ok := h.exact("Error"); !ok {
		t.Error("the newest shows")
	}
	if _, ok := h.exact("Claude"); ok {
		t.Error("a run of its replies has one name, at its start (scrolled away)")
	}
	gen := st.ed.Gen
	if err := h.u.drawAsk(st, "x"); err != nil || st.ed.Gen <= gen {
		t.Error("asking redraws its status")
	}
}

func TestDrawChatResumes(t *testing.T) {
	h := newHarness(t)
	st := chatting(h)
	st.side.Session = "abc"
	os.MkdirAll(filepath.Join(h.u.home, ".claude", "projects", "p"), 0o755)
	os.WriteFile(filepath.Join(h.u.home, ".claude", "projects", "p", "abc.jsonl"), nil, 0o644)
	st.claude.in.SetText("hi")
	h.clickButton("drawSend")
	if o := h.drawClaudes()[0].opts; o.Resume != "abc" || o.New != "" {
		t.Errorf("its kept session, resumed: %+v", o)
	}
}

func TestDrawChatFails(t *testing.T) {
	h := newHarness(t)
	st := chatting(h)
	startClaude = func(claude.Options, func(claude.Event)) (session, error) { return nil, errors.New("no claude") }
	st.claude.in.SetText("hi")
	h.clickButton("drawSend")
	if h.u.toast != "Drawing's Claude: no claude" || !h.u.toastBad || st.claude.in.Text() != "hi" || len(h.u.chat(drawChatKey(st.path))) != 0 {
		t.Errorf("toast %q, kept %q", h.u.toast, st.claude.in.Text())
	}
	startClaude = func(o claude.Options, on func(claude.Event)) (session, error) { return &failSend{}, nil }
	h.clickButton("drawSend")
	if h.u.toast != "Drawing's Claude: send failed" {
		t.Errorf("toast %q", h.u.toast)
	}
	os.Remove(drawSide(st.path, "desk.json"))
	os.Mkdir(drawSide(st.path, "desk.json"), 0o755)
	os.WriteFile(filepath.Join(drawSide(st.path, "desk.json"), "x"), nil, 0o644)
	st.side.Session = ""
	h.u.drawKeepSide(st)
	if !strings.HasPrefix(h.u.toast, "Couldn't keep the drawing's notes: ") || !h.u.toastBad {
		t.Errorf("toast %q", h.u.toast)
	}
}

type failSend struct{ fakeClaude }

func (*failSend) Send(string) error { return errors.New("send failed") }

func TestDrawChatHides(t *testing.T) {
	h := newHarness(t)
	st := chatting(h)
	h.clickButton("drawChat")
	if !st.claude.hide {
		t.Fatal("hidden")
	}
	if _, ok := h.shown("Drawing's Claude"); ok {
		t.Error("gone from the view")
	}
	h.clickButton("drawChat")
	if st.claude.hide {
		t.Error("shown again")
	}
}

func TestDrawChatEscape(t *testing.T) {
	h := newHarness(t)
	st := chatting(h)
	h.u.focusLater = &st.claude.in
	h.frame()
	h.send(press(key.NameEscape, 0))
	if h.u.view != "draw:"+st.path || h.u.drawTyping(layout.Context{Source: h.r.Source()}) {
		t.Errorf("Esc leaves the chat, not the drawing: %q", h.u.view)
	}
	st.rework = "x"
	h.send(press(key.NameEscape, 0))
	if st.rework != "" || h.u.view != "draw:"+st.path {
		t.Error("Esc closes a suggestion's comment")
	}
}

func TestDrawSidecarsFollow(t *testing.T) {
	h := newHarness(t)
	st := chatting(h)
	st.claude.in.SetText("hi")
	h.clickButton("drawSend")
	old := st.path
	f := h.drawClaudes()[0]
	os.WriteFile(drawSide(old, "share.html"), []byte("<p>"), 0o644) // as Share leaves it
	h.u.renameDrawing(st, "E")
	for _, kind := range drawSides {
		if _, err := os.Stat(drawSide(st.path, kind)); err != nil {
			t.Errorf("%s moved: %v", kind, err)
		}
		if _, err := os.Stat(drawSide(old, kind)); err == nil {
			t.Errorf("%s left behind", kind)
		}
	}
	if st.claude.proc != nil || len(h.u.chat(drawChatKey(st.path))) != 1 {
		t.Error("its Claude stopped (it named the old file); the chat read from the new place")
	}
	_ = f
	st.claude.in.SetText("hi")
	h.clickButton("drawSend")
	path := st.path
	h.u.deleteDrawing(path)
	for _, kind := range drawSides {
		if _, err := os.Stat(drawSide(path, kind)); err == nil {
			t.Errorf("%s deleted with it", kind)
		}
	}
	if st.claude.proc != nil {
		t.Error("its Claude stopped")
	}
	h.u.runUndo()
	for _, kind := range drawSides {
		if _, err := os.Stat(drawSide(path, kind)); err != nil {
			t.Errorf("%s back with the undo", kind)
		}
	}
}

func TestDrawChatFile(t *testing.T) {
	h := newHarness(t)
	if h.u.chatFile("DESK-1") != kanban.ChatPath(h.u.path, "DESK-1") || h.u.chatFile("draw:/x/A.excalidraw") != filepath.Join("/x", ".A.chat.jsonl") {
		t.Error(h.u.chatFile("draw:/x/A.excalidraw"))
	}
}

func TestCardDrawings(t *testing.T) {
	h := newHarness(t)
	c := *acmeCard(h)
	h.u.openPanel(c.ID)
	h.frame()
	h.clickButton("pnDraw")
	if h.u.view != "drc:"+c.ID {
		t.Fatalf("view %q", h.u.view)
	}
	if h.u.viewTitle() != c.Key()+" · drawings" {
		t.Errorf("title %q", h.u.viewTitle())
	}
	if _, ok := h.shown("0 drawings of the card"); !ok {
		t.Error("hint")
	}
	h.clickButton("drNew" + "Acme")
	path := filepath.Join(h.u.cardDrawingsDir("Acme", c.ID), "Untitled.excalidraw")
	st := h.u.draws[path]
	if st == nil || st.card != c.ID || h.u.view != "draw:"+path {
		t.Fatalf("made in the card's folder: %v", h.u.view)
	}
	h.send(press(key.NameEscape, 0))
	if h.u.view != "drc:"+c.ID {
		t.Errorf("Esc: back to the card's list, %q", h.u.view)
	}
	if len(h.u.drawingsIn(h.u.placeDir(drawPlace{org: "Acme", card: c.ID}))) != 1 || len(h.u.drawings("Acme")) != 0 {
		t.Error("in the card's list, not the organization's")
	}
	h.send(press(key.NameEscape, 0))
	if h.u.view != "" || h.u.panel != c.ID {
		t.Errorf("Esc: the board, its panel open: %q %q", h.u.view, h.u.panel)
	}
	if _, ok := h.u.placeOf("drc:nope"); ok {
		t.Error("a gone card's list")
	}
	if _, ok := h.u.placeOf("board"); ok {
		t.Error("not a list")
	}
	h.u.view = "drc:nope"
	h.frame() // a gone card's list draws nothing, quietly
}
