package ui

import (
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/draw"
	"github.com/lfdominguez/desk/internal/kanban"
)

// exact is the label drawn that reads s.
func (h *harness) exact(s string) (label, bool) {
	for _, l := range h.labels() {
		if l.text == s {
			return l, true
		}
	}
	return label{}, false
}

// hasButton reports button id on screen.
func (h *harness) hasButton(id string) bool {
	h.frame()
	for _, n := range h.r.AppendSemantics(nil) {
		if n.Desc.Gestures&input.ClickGesture == 0 {
			continue
		}
		c := n.Desc.Bounds.Min.Add(n.Desc.Bounds.Max).Div(2)
		h.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(float32(c.X), float32(c.Y))})
		h.frame()
		if h.u.k.Click(id).Hovered() {
			return true
		}
	}
	return false
}

// pictured splits a tool's answer into its PNG and text; ok false: no picture.
func pictured(t *testing.T, s string) (string, bool) {
	t.Helper()
	rest, ok := strings.CutPrefix(s, "\x00desk-png\x00")
	if !ok {
		return s, false
	}
	b64, text, _ := strings.Cut(rest, "\n")
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(strings.NewReader(string(b))); err != nil {
		t.Fatal(err)
	}
	return text, true
}

func acmeCard(h *harness) *kanban.Card {
	return h.u.b.Card(h.u.b.Add(0, kanban.Card{Title: "Plan the login", Org: "Acme"}).ID)
}

func TestDrawToolsList(t *testing.T) {
	h := newHarness(t)
	c := acmeCard(h)
	if text, ok := h.tool(c.ID, "drawings", `{}`); !ok || !strings.HasPrefix(text, "No drawings yet") {
		t.Fatalf("%v %q", ok, text)
	}
	h.drawing("Org one")
	os.MkdirAll(h.u.cardDrawingsDir("Acme", c.ID), 0o755)
	draw.NewScene().Save(filepath.Join(h.u.cardDrawingsDir("Acme", c.ID), "Mine.excalidraw"))
	draw.NewScene().Save(filepath.Join(h.u.cardDrawingsDir("Acme", c.ID), ".hidden.excalidraw"))
	h.u.drawRead()
	text, ok := h.tool(c.ID, "drawings", ``)
	if !ok || !strings.Contains(text, "The card's drawings:\n- Mine (changed ") || !strings.Contains(text, "Acme's drawings:\n- Org one (") || strings.Contains(text, "hidden") {
		t.Fatalf("%q", text)
	}
	if text, _ := h.tool("infra:Acme", "drawings", ``); strings.Contains(text, "Mine") || !strings.Contains(text, "Org one") {
		t.Errorf("an infra Claude reads the organization's: %q", text)
	}
	if text, ok := h.tool("nope", "drawings", ``); ok || text != "the card is gone from the board" {
		t.Errorf("gone card: %v %q", ok, text)
	}
	if text, ok := h.tool(c.ID, "drawings", `[`); ok || !strings.HasPrefix(text, "the arguments don't read") {
		t.Errorf("bad args: %v %q", ok, text)
	}
	if text, ok := h.tool(c.ID, "drawing_bogus", `{}`); ok || text != "no tool drawing_bogus" {
		t.Errorf("%v %q", ok, text)
	}
}

func TestDrawToolsReadAndEditClosed(t *testing.T) {
	h := newHarness(t)
	c := acmeCard(h)
	text, ok := h.tool(c.ID, "drawing_edit", `{"drawing":"Flow","add":[{"id":"a","type":"rectangle","width":100,"height":50,"label":"Start"}]}`)
	if !ok || text != "Made Flow; touched a. The user can undo it." {
		t.Fatalf("%v %q", ok, text)
	}
	path := filepath.Join(h.u.cardDrawingsDir("Acme", c.ID), "Flow.excalidraw")
	s, err := draw.Load(path)
	if err != nil || s.Find("a") == nil || s.AppState["theme"] != "dark" {
		t.Fatalf("made in the card's folder: %v", err)
	}
	if text, ok = h.tool(c.ID, "drawing_edit", `{"drawing":"Flow","update":[{"id":"a","x":40}]}`); !ok || !strings.HasPrefix(text, "Changed Flow; touched a.") {
		t.Fatalf("%v %q", ok, text)
	}
	if s, _ := draw.Load(path); s.Find("a").X != 40 {
		t.Error("saved")
	}
	text, ok = h.tool(c.ID, "drawing_read", `{"drawing":"Flow"}`)
	body, pic := pictured(t, text)
	if !ok || !pic || !strings.HasPrefix(body, "Flow: elements, one a line (scene units):\na rectangle x=40 y=0 w=100 h=50 label=\"Start\"\n") {
		t.Fatalf("%v %v %q", ok, pic, body)
	}
	for _, c2 := range []struct{ name, args, err string }{
		{"drawing_read", `{"drawing":"Nope"}`, `no drawing "Nope" (see drawings)`},
		{"drawing_read", `{}`, `no drawing "" (see drawings)`},
		{"drawing_edit", `{}`, "name the drawing"},
		{"drawing_edit", `{"drawing":"Flow","delete":["zz"]}`, `delete: no element "zz"`},
		{"drawing_edit", `{"drawing":"New","add":[{"type":"star"}]}`, `add 1: type "star"`},
	} {
		if text, ok := h.tool(c.ID, c2.name, c2.args); ok || !strings.HasPrefix(text, c2.err) {
			t.Errorf("%s %s: %v %q", c2.name, c2.args, ok, text)
		}
	}
	if _, err := os.Stat(filepath.Join(h.u.cardDrawingsDir("Acme", c.ID), "New.excalidraw")); err == nil {
		t.Error("a refused patch makes no drawing")
	}
	os.WriteFile(filepath.Join(h.u.drawingsDir("Acme"), "Broken.excalidraw"), []byte("{"), 0o644)
	for _, name := range []string{"drawing_read", "drawing_edit", "drawing_suggest"} {
		if _, ok := h.tool(c.ID, name, `{"drawing":"Broken","note":"n"}`); ok {
			t.Errorf("%s of an unreadable file", name)
		}
	}
	os.WriteFile(filepath.Join(h.u.drawingsDir("Acme"), "RO.excalidraw"), []byte(`{"type":"excalidraw","elements":[]}`), 0o644)
	os.Chmod(h.u.drawingsDir("Acme"), 0o555)
	defer os.Chmod(h.u.drawingsDir("Acme"), 0o755)
	if _, ok := h.tool("infra:Acme", "drawing_edit", `{"drawing":"RO","add":[{"type":"ellipse"}]}`); ok && os.Getuid() != 0 {
		t.Error("an unwritable folder fails the edit")
	}
}

func TestDrawToolsEditLive(t *testing.T) {
	h := newHarness(t)
	tx := draw.New("text")
	tx.Text = "typing"
	st, _ := h.drawing("Live", rect(0, 0, 50, 50, "#ffffff"), tx)
	st.ed.EditText(tx.ID) // the user typing: the edit ends it first
	text, ok := h.tool("infra:Acme", "drawing_edit", `{"drawing":"Live","add":[{"id":"z","type":"ellipse","width":30,"height":30}]}`)
	if !ok || !strings.HasPrefix(text, "Changed Live; touched z.") {
		t.Fatalf("%v %q", ok, text)
	}
	if st.ed.Editing != "" || st.ed.Scene.Find("z") == nil || st.marks["z"] != "Infra Claude" {
		t.Fatalf("live and marked: %v %v", st.ed.Editing, st.marks)
	}
	if s, _ := draw.Load(st.path); s.Find("z") == nil {
		t.Error("saved at once")
	}
	if _, ok := h.tool("infra:Acme", "drawing_edit", `{"drawing":"Live","delete":["nope"]}`); ok {
		t.Error("a wrong edit of an open drawing errs")
	}
	h.frame()
	st.ed.Undo()
	if st.ed.Scene.Find("z") != nil {
		t.Error("one undo step takes it back")
	}
	if text, _ := h.tool("infra:Acme", "drawing_read", `{"drawing":"Live"}`); !strings.Contains(text, "rectangle") || strings.Contains(text, "ellipse") {
		t.Errorf("reads the open view's scene: %q", text)
	}
	st.marks = map[string]string{st.ed.Scene.Elements[0].ID: "Infra Claude"}
	h.frame()
	sig := h.u.drawSig()
	h.send(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: f32.Pt(900, 600)},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: f32.Pt(900, 600)})
	if st.marks != nil || h.u.drawSig() == sig {
		t.Error("a press clears the marks, and redraws")
	}
}

func TestDrawWho(t *testing.T) {
	h := newHarness(t)
	c := acmeCard(h)
	for _, k := range []struct{ key, name, card string }{
		{"infra:Acme", "Infra Claude", ""},
		{c.ID, "Claude (" + c.Key() + ")", c.ID},
		{"ws:" + c.ID, "Workspace Claude (" + c.Key() + ")", c.ID},
	} {
		w, err := h.u.drawWhoOf(k.key)
		if err != nil || w.name != k.name || w.org != "Acme" || w.card != k.card {
			t.Errorf("%s: %+v %v", k.key, w, err)
		}
	}
	if _, err := h.u.drawWhoOf("draw:/nope.excalidraw"); err == nil {
		t.Error("a closed drawing's Claude")
	}
	st, _ := h.drawing("Mine")
	st.card = c.ID
	w, err := h.u.drawWhoOf("draw:" + st.path)
	if err != nil || w.mine != st.path || w.card != c.ID || w.name != "Drawing's Claude" {
		t.Fatalf("%+v %v", w, err)
	}
	if text, ok := h.tool("draw:"+st.path, "drawings", `{}`); !ok || !strings.Contains(text, "Yours: Mine\n") {
		t.Errorf("%q", text)
	}
	if text, ok := h.tool("draw:"+st.path, "drawing_edit", `{"add":[{"type":"ellipse"}]}`); !ok || !strings.HasPrefix(text, "Changed Mine;") {
		t.Errorf("its own drawing by default: %v %q", ok, text)
	}
	if text, ok := h.tool("draw:"+st.path, "drawing_read", `{"drawing":"Mine"}`); !ok || !strings.Contains(text, "ellipse") {
		t.Errorf("its own by name: %v %q", ok, text)
	}
}

func TestDrawSuggest(t *testing.T) {
	h := newHarness(t)
	c := acmeCard(h)
	h.drawing("S", rect(0, 0, 50, 50, "#ffffff"))
	path := filepath.Join(h.u.drawingsDir("Acme"), "S.excalidraw")
	delete(h.u.draws, path) // closed: kept in the sidecar
	for _, c2 := range []struct{ args, err string }{
		{`{"drawing":"Nope","note":"n"}`, `no drawing "Nope"`},
		{`{"drawing":"S","note":"  "}`, "say what it changes in note"},
		{`{"drawing":"S","note":"n","delete":["zz"]}`, `delete: no element "zz"`},
	} {
		if text, ok := h.tool(c.ID, "drawing_suggest", c2.args); ok || !strings.HasPrefix(text, c2.err) {
			t.Errorf("%s: %v %q", c2.args, ok, text)
		}
	}
	text, ok := h.tool(c.ID, "drawing_suggest", `{"drawing":"S","note":"first","add":[{"type":"ellipse"}]}`)
	if !ok || text != "Suggested on S: it waits for the user to approve, send back or dismiss." {
		t.Fatalf("%v %q", ok, text)
	}
	h.tool(c.ID, "drawing_suggest", `{"drawing":"S","note":"second","add":[{"type":"diamond"}]}`)
	h.tool("infra:Acme", "drawing_suggest", `{"drawing":"S","note":"infra's","add":[{"type":"line"}]}`)
	l := loadSide(path).Suggestions
	if len(l) != 2 || l[0].Note != "second" || l[0].Name != "Claude ("+c.Key()+")" || l[1].Who != "infra:Acme" {
		t.Fatalf("one per Claude, the newest: %+v", l)
	}
	if text, _ := h.tool(c.ID, "drawing_read", `{"drawing":"S"}`); !strings.HasSuffix(text, "2 suggestion(s) wait for the user.\n") {
		t.Errorf("%q", text)
	}
	h.u.openDrawing("Acme", path)
	st := h.u.draws[path]
	if len(st.side.Suggestions) != 2 {
		t.Fatal("read on open")
	}
	gen := st.ed.Gen
	h.tool("infra:Acme", "drawing_suggest", `{"drawing":"S","note":"again","delete":["`+st.ed.Scene.Elements[0].ID+`"]}`)
	if len(st.side.Suggestions) != 2 || st.side.Suggestions[1].Note != "again" || st.ed.Gen <= gen {
		t.Errorf("open: in its state, redrawn: %+v", st.side.Suggestions)
	}
	if loadSide(path).Suggestions[1].Note != "again" {
		t.Error("and on disk")
	}
	os.Remove(drawSide(path, "desk.json"))
	os.Mkdir(drawSide(path, "desk.json"), 0o755) // a folder in its place: unwritable
	if _, ok := h.tool("infra:Acme", "drawing_suggest", `{"drawing":"S","note":"x"}`); ok {
		t.Error("an unwritable sidecar fails")
	}
}

func TestSidecar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "A b.excalidraw")
	if drawSide(path, "desk.json") != filepath.Join(dir, ".A b.desk.json") {
		t.Error(drawSide(path, "desk.json"))
	}
	if saveSide(path, drawSidecar{}) != nil {
		t.Error("nothing to keep, nothing to remove")
	}
	if err := saveSide(path, drawSidecar{Session: "s1"}); err != nil || loadSide(path).Session != "s1" {
		t.Fatal(err)
	}
	if err := saveSide(path, drawSidecar{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(drawSide(path, "desk.json")); err == nil {
		t.Error("an empty sidecar is removed")
	}
	os.MkdirAll(filepath.Join(drawSide(path, "desk.json"), "x"), 0o755)
	if saveSide(path, drawSidecar{}) == nil {
		t.Error("can't remove: an error")
	}
	os.Chmod(dir, 0o555)
	defer os.Chmod(dir, 0o755)
	if saveSide(filepath.Join(dir, "C.excalidraw"), drawSidecar{Session: "x"}) == nil && os.Getuid() != 0 {
		t.Error("can't write: an error")
	}
}

// suggested opens drawing D with a rectangle and one suggestion from who.
func suggested(h *harness, who, name string) (*drawState, *draw.Element) {
	r := rect(100, 100, 80, 60, "#ffffff")
	st, o := h.drawing("D", r)
	h.origin = o
	st.side.Suggestions = []drawSuggestion{{ID: "sg1", Who: who, Name: name, Note: "Add a circle", Patch: draw.Patch{
		Add: []json.RawMessage{json.RawMessage(`{"id":"c","type":"ellipse","x":300,"y":100,"width":60,"height":60}`)}, Delete: []string{r.ID}}}}
	st.ed.Gen++
	h.frame()
	return st, r
}

func TestSuggestionGhost(t *testing.T) {
	h := newHarness(t)
	st, r := suggested(h, "infra:Acme", "Infra Claude")
	gs := st.drawGhosts()
	if len(gs) != 1 || len(gs[0].after) != 1 || gs[0].after[0].ID != "ghost:sg1:c" || gs[0].after[0].Opacity != 50 || len(gs[0].gone) != 1 || gs[0].gone[0].ID != r.ID {
		t.Fatalf("%+v", gs)
	}
	if st.ed.Scene.Find("c") != nil {
		t.Error("a ghost isn't in the drawing")
	}
	if st.rasters["ghost:sg1:c"] == nil {
		t.Error("drawn")
	}
	if &st.drawGhosts()[0] != &gs[0] {
		t.Error("kept while nothing changed")
	}
	if h.hasButton("sgResg1") {
		t.Error("an infra Claude's can't go back")
	}
	st.side.Suggestions = nil
	st.ed.Gen++
	h.frame()
	if len(st.rasters) != 1 {
		t.Errorf("ghost pictures dropped with it: %d", len(st.rasters))
	}
}

func TestSuggestionStale(t *testing.T) {
	h := newHarness(t)
	st, r := suggested(h, "infra:Acme", "Infra Claude")
	st.ed.Select(r.ID)
	st.ed.Delete()
	h.frame()
	if g := st.drawGhosts(); len(g) != 1 || !strings.Contains(g[0].err, "no element") {
		t.Fatalf("%+v", g)
	}
	if h.hasButton("sgOKsg1") || !h.hasButton("sgNosg1") {
		t.Error("no Approve once it doesn't fit")
	}
	h.u.drawApprove(st, "sg1")
	if len(st.side.Suggestions) != 1 {
		t.Error("an approve that doesn't fit keeps it")
	}
	h.u.drawApprove(st, "nope")
	h.u.drawRework(st, "nope", "x")
	if _, ok := h.u.drawTake(st, "nope"); ok {
		t.Error("no such")
	}
}

func TestSuggestionApprove(t *testing.T) {
	h := newHarness(t)
	st, r := suggested(h, "infra:Acme", "Infra Claude")
	h.clickTop("sgOKsg1")
	if st.ed.Scene.Find("c") == nil || st.ed.Scene.Find(r.ID) != nil || len(st.side.Suggestions) != 0 || st.marks["c"] != "Infra Claude" {
		t.Fatalf("applied and marked: %v %v", st.side.Suggestions, st.marks)
	}
	st.ed.Undo()
	if st.ed.Scene.Find("c") != nil || st.ed.Scene.Find(r.ID) == nil {
		t.Error("one undo step")
	}
	if _, err := os.Stat(drawSide(st.path, "desk.json")); err == nil {
		t.Error("no suggestions left: no sidecar")
	}
}

func TestSuggestionDismiss(t *testing.T) {
	h := newHarness(t)
	st, _ := suggested(h, "infra:Acme", "Infra Claude")
	h.clickTop("sgNosg1")
	if len(st.side.Suggestions) != 0 || st.ed.Scene.Find("c") != nil {
		t.Fatal("dismissed")
	}
}

func TestSuggestionReworkCard(t *testing.T) {
	h := newHarness(t)
	c := *acmeCard(h) // a copy: the card moves columns as it starts
	h.u.send(c.ID, "start")
	fake := h.claudes[len(h.claudes)-1]
	st, _ := suggested(h, c.ID, "Claude ("+c.Key()+")")
	h.clickTop("sgResg1")
	if st.rework != "sg1" || h.u.focusLater != nil {
		t.Fatalf("the comment box opens, focused: %q", st.rework)
	}
	h.clickTop("sgCancelsg1")
	if st.rework != "" {
		t.Fatal("cancel")
	}
	h.clickTop("sgResg1")
	h.clickTop("sgSendsg1") // nothing written: nothing sent
	if st.rework != "sg1" {
		t.Fatal("an empty comment stays")
	}
	st.reworkIn.SetText("bigger, please")
	h.clickTop("sgSendsg1")
	if st.rework != "" || !st.side.Suggestions[0].Sent || !loadSide(st.path).Suggestions[0].Sent {
		t.Fatalf("sent back, waiting: %+v", st.side.Suggestions)
	}
	last := fake.prompts[len(fake.prompts)-1]
	if !strings.Contains(last, `your suggestion for the drawing "D" (Add a circle) with:`+"\nbigger, please\n") {
		t.Errorf("to the card's Claude: %q", last)
	}
	if m := h.u.chat(c.ID); m[len(m)-1].Who != "you" {
		t.Error("in its chat")
	}
	if _, ok := h.shown("Sent back: waiting for a new one"); !ok {
		t.Error("shown")
	}
	h.u.terms[c.ID] = &termTab{}
	if err := h.u.drawTellBack(c.ID, "held"); err != nil || !strings.Contains(h.u.peerOut[c.ID], "held") {
		t.Errorf("held while its terminal is up: %v %q", err, h.u.peerOut[c.ID])
	}
	delete(h.u.terms, c.ID)
	if h.u.drawTellBack("nope", "x") == nil {
		t.Error("a gone card")
	}
}

func TestSuggestionReworkByEnter(t *testing.T) {
	h := newHarness(t)
	st, _ := suggested(h, "draw:"+filepath.Join(h.u.drawingsDir("Acme"), "D.excalidraw"), "Drawing's Claude")
	h.clickTop("sgResg1")
	h.frame()
	h.send(key.EditEvent{Text: "rounder"}, press(key.NameReturn, 0))
	if st.rework != "" || !st.side.Suggestions[0].Sent {
		t.Fatal("Enter sends it")
	}
	f := h.claudes[len(h.claudes)-1]
	if !strings.Contains(f.prompts[0], "rounder") || st.claude.busy != 1 {
		t.Errorf("to the drawing's own Claude: %q", f.prompts)
	}
	if h.u.drawTellBack("draw:/gone.excalidraw", "x") == nil {
		t.Error("a closed drawing")
	}
}

func TestSuggestionReworkWorkspace(t *testing.T) {
	h := newHarness(t)
	c, _ := wsCard(h)
	if err := h.u.drawTellBack("ws:"+c.ID, "again"); err != nil {
		t.Fatal(err)
	}
	if ws := h.wsClaudes(); len(ws) != 1 || ws[0].prompts[0] != "again" || h.u.chat(wsKey(c.ID))[0].Text != "again" {
		t.Fatal("to the workspace Claude")
	}
	startClaude = func(claude.Options, func(claude.Event)) (session, error) { return nil, errNoCard }
	h.u.ws[c.ID].proc = nil
	if h.u.drawTellBack("ws:"+c.ID, "x") == nil {
		t.Error("its start failing")
	}
	st, _ := suggested(h, "ws:"+c.ID, "Workspace Claude")
	st.reworkIn.SetText("x")
	h.u.drawRework(st, "sg1", "x")
	if st.side.Suggestions[0].Sent {
		t.Error("not sent: not marked")
	}
	if !strings.HasPrefix(h.u.toast, "Couldn't send it back: ") {
		t.Error("said")
	}
}

func TestMarkBoxes(t *testing.T) {
	h := newHarness(t)
	a, b := rect(100, 100, 80, 60, "#ffffff"), rect(300, 100, 20, 20, "#ffffff")
	gone := rect(0, 0, 10, 10, "#ffffff")
	gone.IsDeleted = true
	st, o := h.drawing("M", a, b, gone)
	st.ed.Apply(draw.Patch{Update: []json.RawMessage{json.RawMessage(`{"id":"` + a.ID + `","label":"in"}`)}})
	var label string
	for _, e := range st.ed.Scene.Elements {
		if e.ContainerID != nil {
			label = e.ID
		}
	}
	st.marks = map[string]string{a.ID: "A", label: "A", b.ID: "B", gone.ID: "A"}
	outlines, tags := st.markBoxes(1)
	want := image.Rect(94, 94, 186, 166)
	if len(outlines) != 2 || outlines[0] != (markBox{want, "A"}) || outlines[1].who != "B" {
		t.Fatalf("a shape's label and a deleted one draw none: %+v", outlines)
	}
	if len(tags) != 2 || tags[0].r != want || tags[1].r != image.Rect(294, 94, 326, 126) {
		t.Fatalf("%+v", tags)
	}
	st.marks = map[string]string{a.ID: "A", b.ID: "A"}
	if _, tags := st.markBoxes(1); len(tags) != 1 || tags[0].r != image.Rect(94, 94, 326, 166) {
		t.Errorf("one Claude's run is one tag: %+v", tags)
	}
	st.marks = map[string]string{label: "A"}
	if out, _ := st.markBoxes(1); len(out) != 1 {
		t.Error("a label marked alone draws")
	}
	st.marks = map[string]string{a.ID: "A"}
	h.frame()
	l, ok := h.exact("A")
	if !ok {
		t.Fatal("named")
	}
	if l.at.Min.X != int(o.X)+94+6 || l.at.Min.Y != int(o.Y)+166+3+2 {
		t.Errorf("its name under its box: %v (box %v at %v)", l.at, want, o)
	}
}

func TestGhostPlaces(t *testing.T) {
	h := newHarness(t)
	st, r := suggested(h, "infra:Acme", "Infra Claude")
	gs := st.drawGhosts()
	if b := st.ghostBox(gs[0], 1); b != image.Rect(94, 94, 366, 166) {
		t.Errorf("box %v", b)
	}
	if b := st.ghostBox(drawGhost{}, 1); !b.Empty() {
		t.Error("none")
	}
	l, ok := h.exact("Suggestion · Infra Claude")
	if top := int(h.origin.Y) + 94; !ok || l.at.Min.X != int(h.origin.X)+94+6 || l.at.Min.Y >= top-5 || l.at.Min.Y < top-30 { // a label's box is its room: its top left is what's placed
		t.Errorf("named above its box: %v", l.at)
	}
	st.side.Suggestions[0].Patch.Add = []json.RawMessage{json.RawMessage(`{"id":"c","type":"rectangle","x":300,"y":100,"width":60,"height":60,"label":"hi"}`)}
	st.ed.Gen++
	if g := st.drawGhosts()[0]; len(g.after) != 2 {
		t.Errorf("a shape's label goes with it: %d", len(g.after))
	}
	_ = r
}

func TestSuggestionCardsStack(t *testing.T) {
	h := newHarness(t)
	st, _ := suggested(h, "infra:Acme", "Infra Claude")
	two := st.side.Suggestions[0]
	two.ID, two.Name = "sg2", "Other Claude"
	st.side.Suggestions = append(st.side.Suggestions, two)
	st.ed.Gen++
	h.frame()
	n1, _ := h.exact("Infra Claude")
	n2, _ := h.exact("Other Claude")
	ok2 := h.areaOf("sgOKsg2")
	height := ok2.Max.Y + 12 - (n2.at.Min.Y - 12) // a card: its padding round its name row to its buttons
	if d := n2.at.Min.Y - n1.at.Min.Y; d != height+8 {
		t.Errorf("cards %d apart, %d tall: 8 between", d, height)
	}
}

func TestSuggestionRedraws(t *testing.T) {
	h := newHarness(t)
	st, _ := suggested(h, "draw:"+filepath.Join(h.u.drawingsDir("Acme"), "D.excalidraw"), "Drawing's Claude")
	gen := st.ed.Gen
	h.u.drawRework(st, "sg1", "again")
	if st.ed.Gen <= gen {
		t.Error("sent back: redrawn")
	}
	if m := h.u.chat(drawChatKey(st.path)); len(m) != 1 || m[0].Who != "you" {
		t.Errorf("in the drawing's chat: %+v", m)
	}
	gen = st.ed.Gen
	h.u.drawTake(st, "sg1")
	if st.ed.Gen <= gen {
		t.Error("taken: redrawn")
	}
	st.side.Suggestions = []drawSuggestion{{ID: "x", Who: "infra:Acme", Note: "n", Patch: draw.Patch{Delete: []string{"nope"}}}}
	h.u.drawApprove(st, "x")
	if !h.u.toastBad || !strings.HasPrefix(h.u.toast, "The suggestion no longer fits: ") {
		t.Errorf("toast %q", h.u.toast)
	}
	h.u.toastBad = false
	h.u.drawRework(st, "x", "y")
	if !h.u.toastBad {
		t.Error("can't send back an infra Claude's: an error")
	}
}

func TestTellBackHolds(t *testing.T) {
	h := newHarness(t)
	c := *acmeCard(h)
	h.u.send(c.ID, "start")
	f := h.claudes[len(h.claudes)-1]
	h.u.asks[c.ID] = &askState{r: &claude.Request{ID: "r"}}
	h.u.drawTellBack(c.ID, "held for the question")
	if !strings.Contains(h.u.peerOut[c.ID], "held for the question") {
		t.Error("held while it asks")
	}
	delete(h.u.asks, c.ID)
	delete(h.u.peerOut, c.ID)
	h.u.drawTellBack(c.ID, "now")
	if f.prompts[len(f.prompts)-1] != "now" {
		t.Errorf("%q", f.prompts)
	}
	n := 0
	for _, m := range h.u.chat(c.ID) {
		if m.Text == "now" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("said once: %d", n)
	}
}
