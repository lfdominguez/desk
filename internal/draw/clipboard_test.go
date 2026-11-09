package draw

import (
	"encoding/json"
	"testing"
)

func TestClipboardRoundTrip(t *testing.T) {
	ed, a, b, _ := shapes()
	if ed.Clipboard() != nil {
		t.Fatal("nothing selected: nothing to copy")
	}
	img, _ := ed.AddImage(png1(10, 10), pt(0, 0), 500)
	ed.Select(a.ID, img.ID)
	raw := ed.Clipboard()
	var c map[string]any
	json.Unmarshal(raw, &c)
	if c["type"] != "excalidraw/clipboard" || len(c["elements"].([]any)) != 2 {
		t.Fatalf("excalidraw.com's shape: %s", raw)
	}
	els, files, ok := ParseClipboard(raw)
	if !ok || len(els) != 2 || files[*img.FileID] == nil || files[*img.FileID].DataURL == "" {
		t.Fatalf("read back: %v %v %v", ok, els, files)
	}
	ed.Select(b.ID) // no image: no files
	if _, files, _ := ParseClipboard(ed.Clipboard()); files != nil {
		t.Error("no images, no files")
	}
}

func TestParseLibrary(t *testing.T) {
	l, err := ParseLibrary([]byte(`{"type":"excalidrawlib","version":2,"libraryItems":[{"id":"i","elements":[{"id":"e","type":"ellipse"}]}]}`))
	if err != nil || len(l.Items) != 1 || l.Items[0].Elements[0].Type != "ellipse" {
		t.Fatalf("%v %+v", err, l)
	}
	for _, s := range []string{"{", `{"type":"excalidraw","elements":[]}`} {
		if _, err := ParseLibrary([]byte(s)); err == nil {
			t.Errorf("not a library: %s", s)
		}
	}
}

func TestParseClipboardOthers(t *testing.T) {
	for _, s := range []string{"hello", `{"type":"excalidraw","elements":[{"id":"a","type":"rectangle"}]}`,
		`{"type":"excalidraw/clipboard","elements":[]}`,
		`{"type":"excalidraw/clipboard","elements":[null,{"id":"a","type":"rectangle","isDeleted":true}]}`} {
		if _, _, ok := ParseClipboard([]byte(s)); ok {
			t.Errorf("not a copy: %s", s)
		}
	}
	els, _, ok := ParseClipboard([]byte(`{"type":"excalidraw/clipboard","elements":[{"id":"a","type":"rectangle","isDeleted":true},{"id":"b","type":"ellipse"}]}`))
	if !ok || len(els) != 1 || els[0].ID != "b" {
		t.Errorf("deleted ones left out: %v", els)
	}
}
