package ui

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/draw"
	"github.com/lfdominguez/desk/internal/infra"
)

// The drawing tools every Claude desk runs gets (infra's drawingTools):
// drawings, drawing_read, drawing_edit, drawing_suggest. who says whose
// call it is: a card's id, "ws:<id>" its workspace Claude, "draw:<path>"
// a drawing's own Claude, "infra:<org>" an infra Claude.

// drawWho is who a call is from, as drawTool works it out.
type drawWho struct {
	key  string // who, as the call came
	name string // as the user reads it
	org  string
	card string // the card it acts for, "" none
	mine string // a drawing's own Claude's drawing
}

// drawSuggestion is a change a Claude proposed, kept beside the drawing
// (drawSide) until the user approves, sends back or dismisses it.
type drawSuggestion struct {
	ID    string     `json:"id"`
	Who   string     `json:"who"`  // drawWho.key: where a rework goes
	Name  string     `json:"name"` // drawWho.name
	Note  string     `json:"note"`
	Patch draw.Patch `json:"patch"`
	At    time.Time  `json:"at"`
	Sent  bool       `json:"sent,omitempty"` // sent back with a comment: its Claude reworks it
}

// drawSidecar is desk's own file beside a drawing (never in the
// .excalidraw): its Claude's session and the suggestions waiting.
type drawSidecar struct {
	Session     string           `json:"session,omitempty"`
	Suggestions []drawSuggestion `json:"suggestions,omitempty"`
	Shared      string           `json:"shared,omitempty"` // the claude.ai artifact it was last shared as
}

// drawSide is the path of a drawing's file of kind beside it, hidden:
// .<name>.<kind>.
func drawSide(path, kind string) string {
	return filepath.Join(filepath.Dir(path), "."+drawName(path)+"."+kind)
}

func loadSide(path string) drawSidecar {
	var s drawSidecar
	if b, err := os.ReadFile(drawSide(path, "desk.json")); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

// saveSide writes s beside the drawing at path; nothing to keep, no file.
func saveSide(path string, s drawSidecar) error {
	f := drawSide(path, "desk.json")
	if s.Session == "" && len(s.Suggestions) == 0 {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f)
}

// cardDrawingsDir is card id's own drawings, inside its organization's.
func (u *ui) cardDrawingsDir(org, id string) string { return filepath.Join(u.drawingsDir(org), id) }

// drawWhoOf works out who's call key is (UI goroutine).
func (u *ui) drawWhoOf(key string) (drawWho, error) {
	w := drawWho{key: key}
	switch {
	case strings.HasPrefix(key, "infra:"):
		w.org, w.name = strings.TrimPrefix(key, "infra:"), "Infra Claude"
	case strings.HasPrefix(key, "draw:"):
		w.mine, w.name = strings.TrimPrefix(key, "draw:"), "Drawing's Claude"
		st := u.draws[w.mine]
		if st == nil {
			return w, errors.New("the drawing is closed")
		}
		w.org, w.card = st.org, st.card
	default:
		id, ws := strings.CutPrefix(key, "ws:")
		c := u.b.Card(id)
		if c == nil {
			return w, errors.New("the card is gone from the board")
		}
		w.org, w.card, w.name = c.Org, id, "Claude ("+c.Key()+")"
		if ws {
			w.name = "Workspace Claude (" + c.Key() + ")"
		}
	}
	return w, nil
}

// drawFind is the path of drawing name for w: the card's own first, then
// the organization's; "" with mine, w's own drawing. ok false: none.
func (u *ui) drawFind(w drawWho, name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return w.mine, w.mine != ""
	}
	var dirs []string
	if w.card != "" {
		dirs = append(dirs, u.cardDrawingsDir(w.org, w.card))
	}
	for _, d := range append(dirs, u.drawingsDir(w.org)) {
		p := filepath.Join(d, name+".excalidraw")
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return filepath.Join(cmp.Or(append(dirs, u.drawingsDir(w.org))...), name+".excalidraw"), false
}

// drawTool answers who's drawing tool call, off the UI goroutine.
func (u *ui) drawTool(who, name string, raw json.RawMessage) (string, bool) {
	var a struct {
		Drawing string `json:"drawing"`
		Note    string `json:"note"`
		draw.Patch
	}
	if err := json.Unmarshal(raw, &a); err != nil && len(raw) > 0 {
		return "the arguments don't read: " + err.Error(), false
	}
	text, err := onBoard(u, func() (string, error) {
		w, err := u.drawWhoOf(who)
		if err != nil {
			return "", err
		}
		switch name {
		case "drawings":
			return u.drawListFor(w), nil
		case "drawing_read":
			return u.drawReadFor(w, a.Drawing)
		case "drawing_edit":
			return u.drawEditFor(w, a.Drawing, a.Patch)
		case "drawing_suggest":
			return u.drawSuggest(w, a.Drawing, strings.TrimSpace(a.Note), a.Patch)
		}
		return "", errors.New("no tool " + name)
	})
	if err != nil {
		return err.Error(), false
	}
	return text, true
}

// drawListFor lists the drawings w reads.
func (u *ui) drawListFor(w drawWho) string {
	var b strings.Builder
	list := func(title string, l []drawItem) {
		if len(l) == 0 {
			return
		}
		fmt.Fprintf(&b, "%s:\n", title)
		for _, it := range l {
			fmt.Fprintf(&b, "- %s (changed %s)\n", it.name, it.mod.Format("2006-01-02 15:04"))
		}
	}
	if w.card != "" {
		list("The card's drawings", readDrawings(u.cardDrawingsDir(w.org, w.card)))
	}
	list(w.org+"'s drawings", u.drawings(w.org))
	if w.mine != "" {
		fmt.Fprintf(&b, "Yours: %s\n", drawName(w.mine))
	}
	if b.Len() == 0 {
		return "No drawings yet. drawing_edit with a new name makes one."
	}
	return b.String()
}

// drawScene is the drawing at path as it is now: the open view's, else the file.
func (u *ui) drawScene(path string) (*draw.Scene, error) {
	if st := u.draws[path]; st != nil {
		return st.ed.Scene, nil
	}
	return draw.Load(path)
}

func (u *ui) drawReadFor(w drawWho, name string) (string, error) {
	path, ok := u.drawFind(w, name)
	if !ok {
		return "", fmt.Errorf("no drawing %q (see drawings)", name)
	}
	s, err := u.drawScene(path)
	if err != nil {
		return "", err
	}
	png, err := draw.PNG(s, draw.Options{Scale: 1, Padding: 20, MaxSide: 1600})
	if err != nil {
		return "", err
	}
	text := fmt.Sprintf("%s: elements, one a line (scene units):\n%s", drawName(path), draw.Summary(s))
	if l := u.drawSuggestions(path); len(l) > 0 {
		text += fmt.Sprintf("%d suggestion(s) wait for the user.\n", len(l))
	}
	return infra.ImageResult(png, text), nil
}

// drawEditFor applies w's patch to drawing name, made when it isn't there:
// live and marked as w's when it is open.
func (u *ui) drawEditFor(w drawWho, name string, p draw.Patch) (string, error) {
	path, ok := u.drawFind(w, name)
	if !ok && strings.TrimSpace(name) == "" {
		return "", errors.New("name the drawing")
	}
	var ids []string
	var err error
	if st := u.draws[path]; st != nil {
		st.ed.EndText()
		if ids, err = st.ed.Apply(p); err != nil {
			return "", err
		}
		st.mark(ids, w.name)
		u.drawSave(st)
	} else {
		s := draw.NewScene()
		if ok {
			if s, err = draw.Load(path); err != nil {
				return "", err
			}
		} else {
			s.AppState["theme"] = "dark"
		}
		if ids, err = draw.NewEditor(s).Apply(p); err != nil {
			return "", err
		}
		if err = s.Save(path); err != nil {
			return "", err
		}
	}
	u.drawRead()
	verb := "Changed"
	if !ok {
		verb = "Made"
	}
	return fmt.Sprintf("%s %s; touched %s. The user can undo it.", verb, drawName(path), strings.Join(ids, ", ")), nil
}

// drawSuggestions are those waiting on the drawing at path.
func (u *ui) drawSuggestions(path string) []drawSuggestion {
	if st := u.draws[path]; st != nil {
		return st.side.Suggestions
	}
	return loadSide(path).Suggestions
}

// drawSuggest keeps w's proposed change for the user, in place of the
// one w had waiting there.
func (u *ui) drawSuggest(w drawWho, name, note string, p draw.Patch) (string, error) {
	path, ok := u.drawFind(w, name)
	if !ok {
		return "", fmt.Errorf("no drawing %q (see drawings)", name)
	}
	if note == "" {
		return "", errors.New("say what it changes in note")
	}
	s, err := u.drawScene(path)
	if err != nil {
		return "", err
	}
	if _, _, err := draw.Preview(s, p); err != nil {
		return "", err
	}
	sg := drawSuggestion{ID: draw.NewID(), Who: w.key, Name: w.name, Note: note, Patch: p, At: time.Now()}
	side := loadSide(path)
	st := u.draws[path]
	if st != nil {
		side = st.side
	}
	side.Suggestions = append(slices.DeleteFunc(slices.Clone(side.Suggestions), func(o drawSuggestion) bool { return o.Who == w.key }), sg)
	if err := saveSide(path, side); err != nil {
		return "", err
	}
	if st != nil {
		st.side = side
		st.ed.Gen++
	}
	return "Suggested on " + drawName(path) + ": it waits for the user to approve, send back or dismiss.", nil
}
