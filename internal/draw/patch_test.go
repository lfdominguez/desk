package draw

import (
	"encoding/json"
	"strings"
	"testing"
)

func patch(t *testing.T, s string) Patch {
	t.Helper()
	var p Patch
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestApplyAdds(t *testing.T) {
	ed := NewEditor(NewScene())
	ids, err := ed.Apply(patch(t, `{"add":[
		{"id":"a","type":"rectangle","x":0,"y":0,"width":100,"height":60,"backgroundColor":"#a5d8ff","label":"Login"},
		{"id":"b","type":"ellipse","x":300,"y":0,"width":100,"height":60},
		{"type":"arrow","from":"a","to":"b"},
		{"type":"line","x":0,"y":200,"width":50,"height":10},
		{"type":"text","x":0,"y":300,"text":"two\nlines"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 5 || ids[0] != "a" || ids[1] != "b" || ids[2] == "" || ids[2] == ids[3] {
		t.Fatalf("touched %v", ids)
	}
	s := ed.Scene
	a := s.Find("a")
	if a.BackgroundColor != "#a5d8ff" || a.StrokeColor != "#1e1e1e" || a.Roundness == nil || a.Version != 1 {
		t.Errorf("defaults under what was given: %+v", a)
	}
	var label *Element
	for _, e := range s.Elements {
		if e.ContainerID != nil && *e.ContainerID == "a" {
			label = e
		}
	}
	if label == nil || label.Text != "Login" || label.TextAlign != "center" || label.VerticalAlign != "middle" {
		t.Fatalf("label %+v", label)
	}
	if cx, cy := label.X+label.Width/2, label.Y+label.Height/2; cx != 50 || cy != 30 {
		t.Errorf("label centered at %v,%v", cx, cy)
	}
	ar := s.Find(ids[2])
	if ar.StartBinding == nil || ar.StartBinding.ElementID != "a" || ar.EndBinding == nil || ar.EndBinding.ElementID != "b" {
		t.Fatalf("arrow bindings %+v %+v", ar.StartBinding, ar.EndBinding)
	}
	if ar.X != 108 || ar.Y != 30 || ar.Points[1] != [2]float64{184, 0} {
		t.Errorf("arrow routed between outlines: %v,%v %v", ar.X, ar.Y, ar.Points)
	}
	if len(s.Find("b").BoundElements) != 1 {
		t.Errorf("b knows its arrow: %v", s.Find("b").BoundElements)
	}
	if ln := s.Find(ids[3]); len(ln.Points) != 2 || ln.Points[1] != [2]float64{50, 10} {
		t.Errorf("a line without points spans its box: %v", ln.Points)
	}
	tx := s.Find(ids[4])
	if tx.OriginalText != "two\nlines" || tx.Height != 50 || tx.Width == 0 {
		t.Errorf("text fitted: %+v", tx)
	}
	ed.Undo()
	if len(s.Elements) != 0 {
		t.Errorf("one undo step: %d left", len(s.Elements))
	}
}

func TestApplyUpdatesAndDeletes(t *testing.T) {
	ed := NewEditor(NewScene())
	if _, err := ed.Apply(patch(t, `{"add":[
		{"id":"a","type":"rectangle","x":0,"y":0,"width":100,"height":60,"label":"A"},
		{"id":"b","type":"rectangle","x":300,"y":0,"width":100,"height":60},
		{"id":"c","type":"arrow","from":"a","to":"b"},
		{"id":"t","type":"text","x":0,"y":300,"text":"hi"}]}`)); err != nil {
		t.Fatal(err)
	}
	s := ed.Scene
	w := s.Find("t").Width
	ids, err := ed.Apply(patch(t, `{"update":[
		{"id":"a","type":"ellipse","y":200,"strokeColor":"#e03131","label":"Moved"},
		{"id":"t","text":"hello there"}]}`))
	if err != nil || len(ids) != 2 {
		t.Fatal(ids, err)
	}
	a := s.Find("a")
	if a.Type != "rectangle" || a.Y != 200 || a.X != 0 || a.StrokeColor != "#e03131" || a.Version != 2 {
		t.Errorf("updated %+v", a)
	}
	var labels []*Element
	for _, e := range s.Elements {
		if e.ContainerID != nil {
			labels = append(labels, e)
		}
	}
	if len(labels) != 1 || labels[0].Text != "Moved" || labels[0].Y+labels[0].Height/2 != 230 || labels[0].StrokeColor != "#e03131" {
		t.Fatalf("one label, replaced and centered: %+v", labels)
	}
	if c := s.Find("c"); c.Y+c.Points[0][1] <= 60 {
		t.Errorf("the arrow followed its moved shape: %v %v", c.Y, c.Points)
	}
	if tx := s.Find("t"); tx.Width <= w || tx.OriginalText != "hello there" {
		t.Errorf("text refitted: %v", tx.Width)
	}
	if _, err := ed.Apply(patch(t, `{"update":[{"id":"a","label":""}]}`)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(Summary(s), "label=") {
		t.Error("an empty label takes it away")
	}
	if _, err := ed.Apply(patch(t, `{"update":[{"id":"a","label":"Again"}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := ed.Apply(patch(t, `{"delete":["a"]}`)); err != nil {
		t.Fatal(err)
	}
	for _, e := range s.Elements {
		if e.ID == "a" || e.ContainerID != nil {
			t.Errorf("deleted with its label: %s", e.ID)
		}
	}
	if c := s.Find("c"); c.StartBinding != nil {
		t.Error("the arrow let go")
	}
	ed.Undo()
	if s.Find("a") == nil {
		t.Error("the delete is an undo step")
	}
}

func TestApplyRefusesWholly(t *testing.T) {
	ed := NewEditor(NewScene())
	ed.Apply(patch(t, `{"add":[{"id":"a","type":"rectangle","width":10,"height":10},{"id":"l","type":"line"}]}`))
	for _, c := range []struct{ p, err string }{
		{`{"add":[{"type":"rectangle"},{"type":"star"}]}`, "add 2: type \"star\""},
		{`{"add":[{"type":"ellipse"},{"type":"rectangle","id":"a"}]}`, "add 2: id a is taken"},
		{`{"add":[{"type":"rectangle","x":"far"}]}`, "add 1:"},
		{`{"add":[{"type":"rectangle","from":"a"}]}`, "from and to are for arrows"},
		{`{"add":[{"type":"arrow","to":"nope"}]}`, `no shape "nope"`},
		{`{"add":[{"type":"arrow","to":"l"}]}`, `no shape "l"`},
		{`{"update":[{"id":"nope"}]}`, `update 1: no element "nope"`},
		{`{"update":[{"id":"a","x":"far"}]}`, "update 1:"},
		{`{"update":[{"id":7}]}`, "update 1:"},
		{`{"add":[{"type":7}]}`, "add 1:"},
		{`{"delete":["nope"]}`, `delete: no element "nope"`},
		{`{"add":[{"type":"ellipse"}],"delete":["nope"]}`, "no element"},
	} {
		before, _ := ed.Scene.Marshal()
		_, err := ed.Apply(patch(t, c.p))
		if err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: %v", c.p, err)
		}
		if after, _ := ed.Scene.Marshal(); string(after) != string(before) {
			t.Errorf("%s changed the scene", c.p)
		}
	}
	if len(ed.undo) != 1 {
		t.Errorf("refused patches leave no undo step: %d", len(ed.undo))
	}
}

func TestApplyArrowKeepsItsBends(t *testing.T) {
	ed := NewEditor(NewScene())
	ed.Apply(patch(t, `{"add":[
		{"id":"a","type":"rectangle","x":0,"y":0,"width":100,"height":100},
		{"id":"c","type":"arrow","x":200,"y":200,"points":[[0,0],[0,100],[100,100]],"from":"a"}]}`))
	c := ed.Scene.Find("c")
	if len(c.Points) != 3 || c.StartBinding == nil || c.EndBinding != nil {
		t.Fatalf("%v %+v", c.Points, c.EndBinding)
	}
	if end := pt(c.X+c.Points[2][0], c.Y+c.Points[2][1]); end != pt(300, 300) {
		t.Errorf("the free end stays: %v", end)
	}
}

func TestApplyArrowFromOnly(t *testing.T) {
	ed := NewEditor(NewScene())
	ed.Apply(patch(t, `{"add":[
		{"id":"a","type":"rectangle","x":0,"y":0,"width":100,"height":100},
		{"id":"c","type":"arrow","x":300,"y":50,"points":[[0,0],[100,0]],"from":"a"}]}`))
	c := ed.Scene.Find("c")
	if start, end := pt(c.X+c.Points[0][0], c.Y+c.Points[0][1]), pt(c.X+c.Points[1][0], c.Y+c.Points[1][1]); !nearPt(start, pt(108, 50)) || !nearPt(end, pt(400, 50)) {
		t.Errorf("from a's outline to where its free end was: %v %v", start, end)
	}
}

func TestLabelFollowsItsShape(t *testing.T) {
	ed := NewEditor(NewScene())
	ed.Apply(patch(t, `{"add":[{"id":"a","type":"rectangle","x":0,"y":0,"width":100,"height":60,"label":"L"}]}`))
	ed.Select("a")
	ed.Nudge(100, 0)
	for _, e := range ed.Scene.Elements {
		if e.ContainerID != nil && e.X+e.Width/2 != 150 {
			t.Errorf("label at %v", e.X+e.Width/2)
		}
	}
}

func TestPreview(t *testing.T) {
	s := NewScene()
	ed := NewEditor(s)
	ed.Apply(patch(t, `{"add":[{"id":"a","type":"rectangle","width":10,"height":10}]}`))
	c, ids, err := Preview(s, patch(t, `{"update":[{"id":"a","x":50}],"add":[{"id":"b","type":"ellipse"}]}`))
	if err != nil || len(ids) != 2 || c.Find("a").X != 50 || c.Find("b") == nil {
		t.Fatal(ids, err)
	}
	if s.Find("a").X != 0 || s.Find("b") != nil {
		t.Error("the original is untouched")
	}
	if _, _, err := Preview(s, patch(t, `{"delete":["z"]}`)); err == nil {
		t.Error("a wrong patch errs")
	}
}

func TestSummary(t *testing.T) {
	s := NewScene()
	if Summary(s) != "(empty)\n" {
		t.Error(Summary(s))
	}
	ed := NewEditor(s)
	ed.Apply(patch(t, `{"add":[
		{"id":"a","type":"rectangle","x":0.4,"y":0,"width":100,"height":60,"backgroundColor":"#a5d8ff","fillStyle":"hachure","label":"Login","angle":0.123,"strokeColor":"#e03131","strokeStyle":"dashed","roughness":0},
		{"id":"b","type":"ellipse","x":300,"y":0,"width":100,"height":60,"backgroundColor":"transparent"},
		{"id":"c","type":"arrow","from":"a","to":"b"},
		{"id":"t","type":"text","x":0,"y":300,"text":"say \"hi\""},
		{"id":"f","type":"frame","x":0,"y":0,"width":500,"height":400,"name":"Flow"},
		{"id":"g","type":"frame","x":0,"y":0,"width":5,"height":4}]}`))
	got := Summary(s)
	for _, want := range []string{
		"a rectangle x=0 y=0 w=100 h=60 angle=0.12 stroke=#e03131 bg=#a5d8ff fill=hachure style=dashed roughness=0 label=\"Login\"\n",
		"b ellipse x=300 y=0 w=100 h=60\n",
		"c arrow x=",
		" from=a to=b\n",
		"t text x=0 y=300 w=",
		" size=20 text=\"say \\\"hi\\\"\"\n",
		"f frame x=0 y=0 w=500 h=400 roughness=0 name=\"Flow\"\n",
		"g frame x=0 y=0 w=5 h=4 roughness=0\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Count(got, "\n") != 6 {
		t.Errorf("labels aren't lines of their own:\n%s", got)
	}
	if strings.Contains(got, "c arrow x=0") || !strings.Contains(got, "points=[[0,0],[") {
		t.Errorf("arrow line: %s", got)
	}
}
