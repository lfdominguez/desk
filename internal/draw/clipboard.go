package draw

import "encoding/json"

// clipboard is what excalidraw.com puts on the clipboard as text when it
// copies, so a copy goes between desk and excalidraw.com both ways.
type clipboard struct {
	Type     string           `json:"type"`
	Elements []*Element       `json:"elements"`
	Files    map[string]*File `json:"files,omitempty"`
}

const clipboardType = "excalidraw/clipboard"

// Clipboard is the selection (with what goes with it) as excalidraw.com's
// clipboard text, the images it shows included; nil with nothing selected.
func (ed *Editor) Clipboard() []byte {
	cs := ed.Copies()
	if len(cs) == 0 {
		return nil
	}
	c := clipboard{Type: clipboardType, Elements: cs}
	for _, e := range cs {
		if e.FileID == nil || ed.Scene.Files[*e.FileID] == nil {
			continue
		}
		if c.Files == nil {
			c.Files = map[string]*File{}
		}
		c.Files[*e.FileID] = ed.Scene.Files[*e.FileID]
	}
	b, _ := json.Marshal(c) // elements and files always marshal
	return b
}

// ParseClipboard reads excalidraw.com's clipboard text; false when b is
// something else.
func ParseClipboard(b []byte) ([]*Element, map[string]*File, bool) {
	var c clipboard
	if json.Unmarshal(b, &c) != nil || c.Type != clipboardType {
		return nil, nil, false
	}
	var live []*Element
	for _, e := range c.Elements {
		if e != nil && !e.IsDeleted {
			live = append(live, e)
		}
	}
	return live, c.Files, len(live) > 0
}
