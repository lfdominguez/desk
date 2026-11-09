package draw

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// Groups, frames, images and the library, as Excalidraw has them: an
// element's GroupIDs run innermost first, so its last is the group a click
// selects whole; a frame holds the elements whose FrameID is its id (they
// move and go with it, and draw clipped to it); a shape's label belongs to
// it (ContainerID); an image is a File in the scene, by a hash of its bytes.

// unit is what a click on e selects: its shape when it is a label, then
// all of the outermost group it is in.
func (ed *Editor) unit(e *Element) []string {
	if e.ContainerID != nil {
		if c := ed.Scene.Find(*e.ContainerID); c != nil {
			e = c
		}
	}
	if len(e.GroupIDs) == 0 {
		return []string{e.ID}
	}
	g := e.GroupIDs[len(e.GroupIDs)-1]
	var ids []string
	for _, x := range ed.Scene.Elements {
		if !x.IsDeleted && slices.Contains(x.GroupIDs, g) {
			ids = append(ids, x.ID)
		}
	}
	return ids
}

// selectInside selects what lies wholly in box b: whole groups only, a
// label through its shape.
func (ed *Editor) selectInside(b [4]float64) {
	in := map[string]bool{}
	for _, e := range ed.Scene.Elements {
		if x0, y0, x1, y1 := e.Bounds(); !e.IsDeleted && !e.Locked && x0 >= b[0] && y0 >= b[1] && x1 <= b[2] && y1 <= b[3] {
			in[e.ID] = true
		}
	}
	ed.Sel = map[string]bool{}
	for id := range in {
		u := ed.unit(ed.Scene.Find(id))
		if !slices.ContainsFunc(u, func(x string) bool { return !in[x] }) {
			for _, x := range u {
				ed.Sel[x] = true
			}
		}
	}
	ed.Gen++
}

// Group makes the selection one group (Ctrl+G): a new outermost group id
// on each.
func (ed *Editor) Group() {
	if len(ed.Selected()) < 2 {
		return
	}
	ed.change(func() {
		g := NewID()
		for _, e := range ed.labelled() {
			e.GroupIDs = append(slices.Clone(e.GroupIDs), g)
		}
	})
}

// Ungroup takes the outermost group off the selection (Ctrl+Shift+G).
func (ed *Editor) Ungroup() {
	ed.change(func() {
		for _, e := range ed.labelled() {
			if n := len(e.GroupIDs); n > 0 {
				e.GroupIDs = slices.Clone(e.GroupIDs[:n-1])
			}
		}
	})
}

// labelled is the selection and its shapes' labels, which share their groups.
func (ed *Editor) labelled() []*Element {
	var out []*Element
	for _, e := range ed.Scene.Elements {
		if !e.IsDeleted && (ed.Sel[e.ID] || (e.ContainerID != nil && ed.Sel[*e.ContainerID])) {
			out = append(out, e)
		}
	}
	return out
}

// with is ids and what goes with them: shapes' labels, frames' elements
// (and theirs), in drawing order.
func (ed *Editor) with(ids map[string]bool) []*Element {
	all := map[string]bool{}
	for id := range ids {
		all[id] = true
	}
	for grew := true; grew; {
		grew = false
		for _, e := range ed.Scene.Elements {
			if all[e.ID] {
				continue
			}
			if (e.ContainerID != nil && all[*e.ContainerID]) || (e.FrameID != nil && all[*e.FrameID]) {
				all[e.ID], grew = true, true
			}
		}
	}
	var out []*Element
	for _, e := range ed.Scene.Elements {
		if all[e.ID] && !e.IsDeleted {
			out = append(out, e)
		}
	}
	return out
}

// Copies are deep copies of the selection with what goes with it (see
// with): what Duplicate, the library and the clipboard take.
func (ed *Editor) Copies() []*Element {
	var out []*Element
	for _, e := range ed.with(ed.Sel) {
		b, _ := json.Marshal(e)
		c := new(Element)
		_ = json.Unmarshal(b, c)
		out = append(out, c)
	}
	return out
}

// renew gives copies els new ids, seeds and groups, keeping how they point
// at each other (labels, frames, bindings among them) and dropping what
// points outside (but a frame not copied, when same keeps the copies in
// the drawing they came from), moved by dx, dy.
func renew(els []*Element, dx, dy float64, same bool) {
	ids, groups := map[string]string{}, map[string]string{}
	for _, e := range els {
		ids[e.ID] = NewID()
	}
	re := func(p *string) *string {
		if p == nil {
			return nil
		}
		if n, ok := ids[*p]; ok {
			return &n
		}
		return nil
	}
	for _, e := range els {
		e.ID, e.Seed, e.Version, e.VersionNonce, e.Updated = ids[e.ID], randInt(), 1, randInt(), time.Now().UnixMilli()
		e.X, e.Y = e.X+dx, e.Y+dy
		for i, g := range e.GroupIDs {
			if groups[g] == "" {
				groups[g] = NewID()
			}
			e.GroupIDs[i] = groups[g]
		}
		f := re(e.FrameID)
		if f == nil && same {
			f = e.FrameID
		}
		e.ContainerID, e.FrameID = re(e.ContainerID), f
		for _, b := range []**Binding{&e.StartBinding, &e.EndBinding} {
			if *b != nil {
				if n, ok := ids[(*b).ElementID]; ok {
					(*b).ElementID = n
				} else {
					*b = nil
				}
			}
		}
		var bound []Bound
		for _, b := range e.BoundElements {
			if n, ok := ids[b.ID]; ok {
				bound = append(bound, Bound{ID: n, Type: b.Type})
			}
		}
		e.BoundElements = bound
	}
}

// Insert adds copies of els (from the library or the clipboard) centered
// on at, as one undo step, selected; several become one group.
func (ed *Editor) Insert(els []*Element, files map[string]*File, at Pt) []string {
	if len(els) == 0 {
		return nil
	}
	b, _ := json.Marshal(els)
	var cs []*Element
	_ = json.Unmarshal(b, &cs)
	box := boxOf(cs)
	renew(cs, at.X-(box[0]+box[2])/2, at.Y-(box[1]+box[3])/2, false)
	var ids []string
	top := 0
	for _, e := range cs {
		if e.ContainerID == nil && e.FrameID == nil {
			top++
		}
	}
	g := NewID()
	ed.change(func() {
		for _, e := range cs {
			if top > 1 && e.ContainerID == nil {
				e.GroupIDs = append(e.GroupIDs, g)
			}
			if e.FileID != nil && files[*e.FileID] != nil && ed.Scene.Files[*e.FileID] == nil {
				if ed.Scene.Files == nil {
					ed.Scene.Files = map[string]*File{}
				}
				ed.Scene.Files[*e.FileID] = files[*e.FileID]
			}
			ed.Scene.Elements = append(ed.Scene.Elements, e)
			ids = append(ids, e.ID)
		}
		ed.Sel = map[string]bool{}
		for _, e := range cs {
			if e.ContainerID == nil {
				ed.Sel[e.ID] = true
			}
		}
	})
	return ids
}

// AddImage puts the picture b (PNG, JPEG, GIF or WebP) centered on at,
// at most maxSide units a side, as one undo step, selected.
func (ed *Editor) AddImage(b []byte, at Pt, maxSide float64) (*Element, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("not a picture desk reads (PNG, JPEG, GIF or WebP)")
	}
	sum := sha1.Sum(b)
	id := fmt.Sprintf("%x", sum)
	mime := "image/" + format
	w, h := float64(cfg.Width), float64(cfg.Height)
	if f := maxSide / math.Max(w, h); f < 1 {
		w, h = w*f, h*f
	}
	e := New("image")
	e.FileID, e.X, e.Y, e.Width, e.Height = &id, at.X-w/2, at.Y-h/2, w, h
	ed.change(func() {
		if ed.Scene.Files == nil {
			ed.Scene.Files = map[string]*File{}
		}
		if ed.Scene.Files[id] == nil {
			ed.Scene.Files[id] = &File{MimeType: mime, ID: id, DataURL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b), Created: time.Now().UnixMilli()}
		}
		ed.Scene.Elements = append(ed.Scene.Elements, e)
		ed.Select(e.ID)
	})
	return e, nil
}

// reframe settles which frame each of ids is in after it moved: the
// topmost frame wholly around it, or none.
func (ed *Editor) reframe(ids map[string]*Element) {
	for id := range ids {
		e := ed.Scene.Find(id)
		if e == nil || e.Type == "frame" || e.ContainerID != nil {
			continue
		}
		if e.FrameID != nil && ids[*e.FrameID] != nil { // moved with its frame
			continue
		}
		e.FrameID = nil
		x0, y0, x1, y1 := e.Bounds()
		for i := len(ed.Scene.Elements) - 1; i >= 0; i-- {
			f := ed.Scene.Elements[i]
			if fx0, fy0, fx1, fy1 := f.Bounds(); f.Type == "frame" && !f.IsDeleted && x0 >= fx0 && y0 >= fy0 && x1 <= fx1 && y1 <= fy1 {
				fid := f.ID
				e.FrameID = &fid
				break
			}
		}
	}
}

// adopt puts what lies wholly in frame f in it.
func (ed *Editor) adopt(f *Element) {
	fx0, fy0, fx1, fy1 := f.Bounds()
	for _, e := range ed.Scene.Elements {
		if x0, y0, x1, y1 := e.Bounds(); e != f && !e.IsDeleted && e.Type != "frame" && e.ContainerID == nil && e.FrameID == nil && x0 >= fx0 && y0 >= fy0 && x1 <= fx1 && y1 <= fy1 {
			id := f.ID
			e.FrameID = &id
		}
	}
}

// FrameOf is the live frame e is in (a label: its shape's), nil none.
func (s *Scene) FrameOf(e *Element) *Element {
	if e.ContainerID != nil {
		if c := s.Find(*e.ContainerID); c != nil {
			e = c
		}
	}
	if e.FrameID == nil {
		return nil
	}
	if f := s.Find(*e.FrameID); f != nil && f.Type == "frame" {
		return f
	}
	return nil
}

// Library is an .excalidrawlib file: items to put in drawings again.
type Library struct {
	Type    string        `json:"type"`
	Version int           `json:"version"`
	Source  string        `json:"source"`
	Items   []LibraryItem `json:"libraryItems"`
}

// LibraryItem is one of a library's: its elements, as copied.
type LibraryItem struct {
	ID       string     `json:"id"`
	Status   string     `json:"status"`
	Elements []*Element `json:"elements"`
	Created  int64      `json:"created"`
	Name     string     `json:"name,omitempty"`
}

// LoadLibrary reads the library at path; none there is an empty one.
func LoadLibrary(path string) (*Library, error) {
	l := &Library{Type: "excalidrawlib", Version: 2, Source: "desk"}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, l); err != nil {
		return nil, err
	}
	return l, nil
}

// ParseLibrary reads an .excalidrawlib file's bytes; an error when b is
// something else. ponytail: version 2 only (libraryItems); excalidraw.com's
// old version 1 (library: element lists) reads as empty.
func ParseLibrary(b []byte) (*Library, error) {
	var l Library
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	if l.Type != "excalidrawlib" {
		return nil, fmt.Errorf("not an excalidraw library (type %q)", l.Type)
	}
	return &l, nil
}

// Save writes the library at path, atomically.
func (l *Library) Save(path string) error {
	b, _ := marshal(l) // plain data: it marshals
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Add puts els (Copies) in the library as a new item, first.
func (l *Library) Add(els []*Element) {
	l.Items = append([]LibraryItem{{ID: NewID(), Status: "unpublished", Elements: els, Created: time.Now().UnixMilli()}}, l.Items...)
}
