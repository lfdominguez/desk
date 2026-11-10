package draw

import (
	"math"
	"strings"
	"testing"
)

func labelOf(s *Scene, id string) *Element {
	for _, e := range s.Elements {
		if e.ContainerID != nil && *e.ContainerID == id && !e.IsDeleted {
			return e
		}
	}
	return nil
}

func TestWrap(t *testing.T) {
	f := newFaces(FontVirgil, 20)
	w := f.measure("aaaa")
	if got := wrap("aaaa aaaa\nb", f, w*1.5); got != "aaaa\naaaa\nb" {
		t.Errorf("at spaces, own lines kept: %q", got)
	}
	if got := wrap("aa aa", f, w*2); got != "aa aa" {
		t.Errorf("what fits stays on its line: %q", got)
	}
	if got := wrap("aaaaaaaa", f, w); got != "aaaa\naaaa" {
		t.Errorf("a word too wide breaks between letters: %q", got)
	}
	if got := wrap("", f, w); got != "" {
		t.Errorf("empty: %q", got)
	}
}

func TestLabelWraps(t *testing.T) {
	ed := NewEditor(NewScene())
	if _, err := ed.Apply(patch(t, `{"add":[{"id":"a","type":"rectangle","x":0,"y":0,"width":200,"height":70,"label":"U-Boot 2015.01\nlogo, SD upgrade check and more"}]}`)); err != nil {
		t.Fatal(err)
	}
	a, l := ed.Scene.Find("a"), labelOf(ed.Scene, "a")
	if l.OriginalText != "U-Boot 2015.01\nlogo, SD upgrade check and more" || strings.Count(l.Text, "\n") < 2 {
		t.Fatalf("wrapped, typed text kept: %q", l.Text)
	}
	if l.Width > 190 || l.X < a.X+5 {
		t.Errorf("inside the padding: x=%v w=%v", l.X, l.Width)
	}
	if a.Height != l.Height+10 || l.Y != a.Y+5 {
		t.Errorf("the shape grew to hold it: h=%v label h=%v y=%v", a.Height, l.Height, l.Y)
	}
	before := a.Height
	ed.Apply(patch(t, `{"update":[{"id":"`+l.ID+`","text":"short"}]}`))
	if l.Text != "short" || a.Height != before || l.Y+l.Height/2 != a.Y+a.Height/2 {
		t.Errorf("a label's text changed fits again: %q %v", l.Text, a.Height)
	}
	ed.Apply(patch(t, `{"update":[{"id":"a","width":30,"height":30}]}`))
	if !strings.Contains(l.Text, "\n") {
		t.Errorf("a narrower shape wraps its label again: %q", l.Text)
	}
}

func TestLabelInShapes(t *testing.T) {
	ed := NewEditor(NewScene())
	if _, err := ed.Apply(patch(t, `{"add":[
		{"id":"e","type":"ellipse","x":0,"y":0,"width":100,"height":10,"label":"x"},
		{"id":"d","type":"diamond","x":0,"y":0,"width":100,"height":10,"label":"x"},
		{"id":"r","type":"rectangle","x":0,"y":0,"width":100,"height":100,"label":"x"},
		{"id":"ar","type":"arrow","x":0,"y":0,"width":100,"height":0,"label":"far too long for it"}]}`)); err != nil {
		t.Fatal(err)
	}
	th := labelOf(ed.Scene, "e").Height
	if h := ed.Scene.Find("e").Height; math.Abs(h-(th+10)*math.Sqrt2) > 1e-9 {
		t.Errorf("ellipse %v", h)
	}
	if h := ed.Scene.Find("d").Height; h != (th+10)*2 {
		t.Errorf("diamond %v", h)
	}
	if h := ed.Scene.Find("r").Height; h != 100 {
		t.Errorf("tall enough stays: %v", h)
	}
	if l := labelOf(ed.Scene, "ar"); l.Text != "far too long for it" || ed.Scene.Find("ar").Height != 0 {
		t.Errorf("an arrow's label isn't wrapped: %q", l.Text)
	}
	if inner(&Element{Type: "ellipse"}, 100) != 61 || inner(&Element{Type: "diamond"}, 100) != 40 || inner(&Element{Type: "rectangle"}, 100) != 90 {
		t.Error("inner")
	}
	r := ed.Scene.Find("r")
	r.Width = 5 // no room at all: the text as typed
	fitLabel(labelOf(ed.Scene, "r"), r)
	if labelOf(ed.Scene, "r").Text != "x" {
		t.Error("no room")
	}
}

func TestLabelTypedAndRestyled(t *testing.T) {
	ed := NewEditor(NewScene())
	ed.Apply(patch(t, `{"add":[{"id":"a","type":"rectangle","x":0,"y":0,"width":100,"height":40,"label":"x"}]}`))
	l := labelOf(ed.Scene, "a")
	ed.EditText(l.ID)
	ed.SetText("one two three four five six")
	if l.OriginalText != "one two three four five six" || !strings.Contains(l.Text, "\n") || l.Width > 90 {
		t.Errorf("typed into a label: %q", l.Text)
	}
	ed.EndText()
	ed.Select(l.ID)
	ed.Restyle(func(e *Element) { e.FontSize = 40 })
	if l.Width > 90 || ed.Scene.Find("a").Height != l.Height+10 {
		t.Errorf("restyled, wrapped again: %v %v", l.Width, ed.Scene.Find("a").Height)
	}
	gone := New("text") // a label whose shape is gone fits as plain text
	gone.ContainerID, gone.Text = new(string), "a b c d e f g h"
	ed.fit(gone)
	if gone.Text != "a b c d e f g h" || gone.Width == 0 {
		t.Errorf("plain: %q %v", gone.Text, gone.Width)
	}
}

func TestResizeWrapsLabel(t *testing.T) {
	ed := NewEditor(NewScene())
	ed.Apply(patch(t, `{"add":[{"id":"a","type":"rectangle","x":0,"y":0,"width":400,"height":60,"label":"one two three four"}]}`))
	l := labelOf(ed.Scene, "a")
	if strings.Contains(l.Text, "\n") {
		t.Fatalf("fits at first: %q", l.Text)
	}
	ed.Select("a")
	drag(ed, "", pt(400, 30), pt(60, 30), false) // the east grip
	if w := ed.Scene.Find("a").Width; w != 60 {
		t.Fatalf("resized to %v", w)
	}
	if !strings.Contains(l.Text, "\n") || l.Width > 50 {
		t.Errorf("narrowed: %q", l.Text)
	}
}

func TestApplyBentArrowsSharp(t *testing.T) {
	ed := NewEditor(NewScene())
	ids, err := ed.Apply(patch(t, `{"add":[
		{"type":"arrow","points":[[0,0],[0,40],[100,40]]},
		{"type":"arrow","points":[[0,0],[0,40],[100,40]],"roundness":{"type":2}},
		{"type":"arrow","points":[[0,0],[100,40]]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if ed.Scene.Find(ids[0]).Roundness != nil || ed.Scene.Find(ids[1]).Roundness == nil || ed.Scene.Find(ids[2]).Roundness == nil {
		t.Error("bends sharp unless asked round; a straight arrow keeps Excalidraw's default")
	}
}
