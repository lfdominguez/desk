package draw

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRoundTripKeepsExcalidrawFile(t *testing.T) {
	b, err := os.ReadFile("testdata/archimate.excalidraw")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var want, got struct{ Elements []map[string]any }
	json.Unmarshal(b, &want)
	json.Unmarshal(out, &got)
	if len(got.Elements) != len(want.Elements) || len(want.Elements) < 100 {
		t.Fatalf("%d elements, want %d", len(got.Elements), len(want.Elements))
	}
	for i := range want.Elements {
		if !reflect.DeepEqual(got.Elements[i], want.Elements[i]) {
			for k, v := range want.Elements[i] {
				if !reflect.DeepEqual(got.Elements[i][k], v) {
					t.Errorf("element %d %s: %v, want %v", i, k, got.Elements[i][k], v)
				}
			}
			for k := range got.Elements[i] {
				if _, ok := want.Elements[i][k]; !ok {
					t.Errorf("element %d: added %s", i, k)
				}
			}
		}
	}
}

func TestParseRefuses(t *testing.T) {
	if _, err := Parse([]byte(`{"type":"excalidrawlib"}`)); err == nil || !strings.Contains(err.Error(), "not an excalidraw") {
		t.Errorf("a library parsed as a scene: %v", err)
	}
	if _, err := Parse([]byte(`{`)); err == nil {
		t.Error("broken JSON parsed")
	}
	if _, err := Parse([]byte(`{"type":"excalidraw","elements":[{"x":"no"}]}`)); err == nil {
		t.Error("a bad element parsed")
	}
	s, err := Parse([]byte(`{"type":"excalidraw","elements":[]}`))
	if err != nil || s.Files == nil {
		t.Errorf("no files map: %v", err)
	}
}

func keys(t *testing.T, e *Element) map[string]any {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	return m
}

func TestMarshalWritesItsTypesKeys(t *testing.T) {
	r := keys(t, New("rectangle"))
	for _, k := range []string{"text", "points", "fileId", "name", "customData"} {
		if _, ok := r[k]; ok {
			t.Errorf("a rectangle wrote %s", k)
		}
	}
	if _, ok := r["frameId"]; !ok {
		t.Error("a rectangle has no frameId: null")
	}
	tx := New("text")
	tx.Text = "<a> & b"
	s := NewScene()
	s.Elements = append(s.Elements, tx)
	if b, _ := s.Marshal(); !strings.Contains(string(b), `"text": "<a> & b"`) {
		t.Error("text is HTML-escaped in the file")
	}
	tx.Text = ""
	tx.CustomData = map[string]any{"by": "claude"}
	m := keys(t, tx)
	if m["text"] != "" || m["fontFamily"] != float64(FontExcalifont) || m["customData"] == nil {
		t.Errorf("text keys: %v", m)
	}
	if _, ok := m["points"]; ok {
		t.Error("a text wrote points")
	}
	if a := keys(t, New("arrow")); a["endArrowhead"] != "arrow" || a["startArrowhead"] != nil || a["elbowed"] != false {
		t.Errorf("arrow heads: %v %v", a["startArrowhead"], a["endArrowhead"])
	}
	var e Element
	json.Unmarshal([]byte(`{"type":"line","points":[[0,0]],"polygon":true,"customData":{"a":1},"text":"x"}`), &e)
	e.CustomData = nil
	m = keys(t, &e)
	if m["polygon"] != true {
		t.Error("an unknown key was dropped")
	}
	if _, ok := m["customData"]; ok {
		t.Error("customData came back after clearing it")
	}
	if _, ok := m["text"]; ok {
		t.Error("a line kept a text key")
	}
}

func TestNewDefaults(t *testing.T) {
	for typ, check := range map[string]func(*Element) bool{
		"rectangle": func(e *Element) bool { return e.Roundness.Type == RoundAdaptive },
		"diamond":   func(e *Element) bool { return e.Roundness.Type == RoundAdaptive },
		"line": func(e *Element) bool {
			return e.Roundness.Type == RoundProportional && len(e.Points) == 1 && e.EndArrowhead == nil
		},
		"arrow":    func(e *Element) bool { return *e.EndArrowhead == "arrow" },
		"freedraw": func(e *Element) bool { return e.SimulatePressure && len(e.Points) == 1 },
		"text":     func(e *Element) bool { return e.FontSize == 20 && e.LineHeight == 1.25 && e.AutoResize },
		"image":    func(e *Element) bool { return e.Status == "saved" && e.Scale[0] == 1 },
		"frame":    func(e *Element) bool { return e.Roughness == 0 },
		"ellipse":  func(e *Element) bool { return e.Roundness == nil },
	} {
		e := New(typ)
		if e.Type != typ || e.Opacity != 100 || e.Seed == 0 || e.Version != 1 || !check(e) {
			t.Errorf("New(%q) = %+v", typ, e)
		}
	}
}

func TestTouchAndIDs(t *testing.T) {
	e := New("rectangle")
	nonce := e.VersionNonce
	e.Touch()
	if e.Version != 2 || e.VersionNonce == nonce || e.Updated == 0 {
		t.Errorf("Touch: %+v", e)
	}
	for range 100 {
		if n := randInt(); n <= 0 || n >= 1<<31 || n%2 == 0 {
			t.Fatalf("seed %d", n)
		}
	}
	seen := map[string]bool{}
	for range 200 {
		id := NewID()
		if len(id) != 21 || strings.Trim(id, idChars) != "" || seen[id] {
			t.Fatalf("id %q", id)
		}
		seen[id] = true
	}
}

func TestSaveLoadFind(t *testing.T) {
	s := NewScene()
	a, b := New("rectangle"), New("ellipse")
	b.IsDeleted = true
	s.Elements = append(s.Elements, a, b)
	path := filepath.Join(t.TempDir(), "org", "plan.excalidraw")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("the temporary file was left")
	}
	got, err := Load(path)
	if err != nil || len(got.Elements) != 2 || got.Source != "desk" {
		t.Fatalf("Load: %v %+v", err, got)
	}
	if got.Find(a.ID) == nil || got.Find(b.ID) != nil || got.Find("nope") != nil {
		t.Error("Find: deleted elements are gone, live ones found")
	}
	if _, err := Load(path + ".missing"); err == nil {
		t.Error("a missing file loaded")
	}
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o644)
	if err := s.Save(filepath.Join(file, "x.excalidraw")); err == nil {
		t.Error("saved under a file")
	}
	dir := filepath.Join(t.TempDir(), "d")
	os.MkdirAll(filepath.Join(dir, "x.excalidraw.tmp"), 0o755)
	if err := s.Save(filepath.Join(dir, "x.excalidraw")); err == nil {
		t.Error("saved over a directory")
	}
	a.CustomData = map[string]any{"bad": func() {}}
	if err := s.Save(path); err == nil {
		t.Error("saved an element that doesn't marshal")
	}
	a.CustomData = nil
	s.AppState["bad"] = func() {}
	if err := s.Save(path); err == nil {
		t.Error("saved what doesn't marshal")
	}
}
