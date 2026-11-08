// Package draw is desk's free-draw canvas without any GUI: the .excalidraw
// model, the clean and hand-drawn (rough.js) geometry, and the PNG and SVG
// renderers. The window (ui) draws the same geometry with Gio; Claude reads
// drawings as the PNG plus a compact element list.
package draw

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// Scene is an .excalidraw file. Fields desk doesn't know (on the scene and on
// each element) are kept as they were, so excalidraw.com's newer keys survive
// a desk edit.
type Scene struct {
	Type     string           `json:"type"`
	Version  int              `json:"version"`
	Source   string           `json:"source"`
	Elements []*Element       `json:"elements"`
	AppState map[string]any   `json:"appState"`
	Files    map[string]*File `json:"files"`
}

// File is an image an element shows, as a data: URL.
type File struct {
	MimeType      string `json:"mimeType"`
	ID            string `json:"id"`
	DataURL       string `json:"dataURL"`
	Created       int64  `json:"created"`
	LastRetrieved int64  `json:"lastRetrieved,omitempty"`
}

// Element is one shape, line, text, image or frame. Coordinates are scene
// units; Points (lines, arrows, freedraw) are relative to X, Y.
type Element struct {
	ID              string         `json:"id"`
	Type            string         `json:"type"`
	X               float64        `json:"x"`
	Y               float64        `json:"y"`
	Width           float64        `json:"width"`
	Height          float64        `json:"height"`
	Angle           float64        `json:"angle"`
	StrokeColor     string         `json:"strokeColor"`
	BackgroundColor string         `json:"backgroundColor"`
	FillStyle       string         `json:"fillStyle"`
	StrokeWidth     float64        `json:"strokeWidth"`
	StrokeStyle     string         `json:"strokeStyle"`
	Roughness       float64        `json:"roughness"`
	Opacity         float64        `json:"opacity"`
	GroupIDs        []string       `json:"groupIds"`
	FrameID         *string        `json:"frameId"`
	Roundness       *Roundness     `json:"roundness"`
	Seed            int64          `json:"seed"`
	Version         int            `json:"version"`
	VersionNonce    int64          `json:"versionNonce"`
	IsDeleted       bool           `json:"isDeleted"`
	BoundElements   []Bound        `json:"boundElements"`
	Updated         int64          `json:"updated"`
	Link            *string        `json:"link"`
	Locked          bool           `json:"locked"`
	CustomData      map[string]any `json:"customData"`

	// text
	Text          string  `json:"text"`
	OriginalText  string  `json:"originalText"`
	FontSize      float64 `json:"fontSize"`
	FontFamily    int     `json:"fontFamily"`
	TextAlign     string  `json:"textAlign"`
	VerticalAlign string  `json:"verticalAlign"`
	ContainerID   *string `json:"containerId"`
	AutoResize    bool    `json:"autoResize"`
	LineHeight    float64 `json:"lineHeight"`

	// line, arrow, freedraw
	Points           [][2]float64 `json:"points"`
	StartBinding     *Binding     `json:"startBinding"`
	EndBinding       *Binding     `json:"endBinding"`
	StartArrowhead   *string      `json:"startArrowhead"`
	EndArrowhead     *string      `json:"endArrowhead"`
	Elbowed          bool         `json:"elbowed"`
	Pressures        []float64    `json:"pressures"`
	SimulatePressure bool         `json:"simulatePressure"`

	// image
	FileID *string     `json:"fileId"`
	Status string      `json:"status"`
	Scale  *[2]float64 `json:"scale"`

	// frame
	Name *string `json:"name"`

	extra map[string]json.RawMessage
}

type Roundness struct {
	Type  int      `json:"type"`
	Value *float64 `json:"value,omitempty"`
}

type Bound struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type Binding struct {
	ElementID  string      `json:"elementId"`
	Focus      float64     `json:"focus"`
	Gap        float64     `json:"gap"`
	FixedPoint *[2]float64 `json:"fixedPoint,omitempty"`
	Mode       string      `json:"mode,omitempty"`
}

// Excalidraw's roundness types and font families.
const (
	RoundLegacy       = 1
	RoundProportional = 2
	RoundAdaptive     = 3

	FontVirgil     = 1
	FontHelvetica  = 2
	FontCascadia   = 3
	FontExcalifont = 5
	FontNunito     = 6
	FontLilita     = 7
	FontComic      = 8
)

// keys each type writes besides the common ones.
var typeKeys = map[string][]string{
	"text":     {"text", "originalText", "fontSize", "fontFamily", "textAlign", "verticalAlign", "containerId", "autoResize", "lineHeight"},
	"line":     {"points", "startBinding", "endBinding", "startArrowhead", "endArrowhead"},
	"arrow":    {"points", "startBinding", "endBinding", "startArrowhead", "endArrowhead", "elbowed"},
	"freedraw": {"points", "pressures", "simulatePressure"},
	"image":    {"fileId", "status", "scale"},
	"frame":    {"name"},
}

var typeOnly = map[string]bool{}

func init() {
	for _, ks := range typeKeys {
		for _, k := range ks {
			typeOnly[k] = true
		}
	}
}

type elementJSON Element

func (e *Element) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, (*elementJSON)(e)); err != nil {
		return err
	}
	return json.Unmarshal(b, &e.extra)
}

// MarshalJSON writes the common keys, the keys of e's type, and every key read
// from the file that desk doesn't model.
func (e *Element) MarshalJSON() ([]byte, error) {
	b, err := marshal((*elementJSON)(e))
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m) // b was just marshaled from a struct: an object
	mine := map[string]bool{}
	for _, k := range typeKeys[e.Type] {
		mine[k] = true
	}
	for k := range m {
		if typeOnly[k] && !mine[k] {
			delete(m, k)
		}
	}
	if e.CustomData == nil {
		delete(m, "customData")
	}
	for k, v := range e.extra {
		if _, ok := m[k]; !ok && !typeOnly[k] && k != "customData" {
			m[k] = v
		}
	}
	return marshal(m)
}

// marshal is json.Marshal without HTML escaping: text keeps its < and &.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	err := enc.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), err
}

// Parse reads an .excalidraw file's bytes.
func Parse(b []byte) (*Scene, error) {
	var s Scene
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.Type != "excalidraw" {
		return nil, fmt.Errorf("not an excalidraw file (type %q)", s.Type)
	}
	if s.Files == nil {
		s.Files = map[string]*File{}
	}
	return &s, nil
}

// Load reads an .excalidraw file.
func Load(path string) (*Scene, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Marshal is s as an .excalidraw file.
func (s *Scene) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	err := enc.Encode(s)
	return buf.Bytes(), err
}

// Save writes s to path through a temporary file, so a crash never leaves half
// a drawing.
func (s *Scene) Save(path string) error {
	b, err := s.Marshal()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// NewScene is an empty drawing.
func NewScene() *Scene {
	return &Scene{Type: "excalidraw", Version: 2, Source: "desk",
		Elements: []*Element{}, AppState: map[string]any{"viewBackgroundColor": "#ffffff", "gridSize": 20},
		Files: map[string]*File{}}
}

// Find is the live element with id, or nil.
func (s *Scene) Find(id string) *Element {
	for _, e := range s.Elements {
		if e.ID == id && !e.IsDeleted {
			return e
		}
	}
	return nil
}

// New is an element of typ with Excalidraw's defaults.
func New(typ string) *Element {
	e := &Element{ID: NewID(), Type: typ, StrokeColor: "#1e1e1e", BackgroundColor: "transparent",
		FillStyle: "solid", StrokeWidth: 2, StrokeStyle: "solid", Roughness: 1, Opacity: 100,
		GroupIDs: []string{}, Seed: randInt(), Version: 1, VersionNonce: randInt(), Updated: time.Now().UnixMilli()}
	switch typ {
	case "rectangle", "diamond":
		e.Roundness = &Roundness{Type: RoundAdaptive}
	case "line", "arrow":
		e.Roundness = &Roundness{Type: RoundProportional}
		e.Points = [][2]float64{{0, 0}}
		if typ == "arrow" {
			a := "arrow"
			e.EndArrowhead = &a
		}
	case "freedraw":
		e.SimulatePressure = true
		e.Points = [][2]float64{{0, 0}}
	case "text":
		e.FontSize, e.FontFamily, e.TextAlign, e.VerticalAlign, e.LineHeight, e.AutoResize = 20, FontExcalifont, "left", "top", 1.25, true
	case "image":
		e.Status, e.Scale = "saved", &[2]float64{1, 1}
	case "frame":
		e.Roughness, e.StrokeWidth = 0, 2
	}
	return e
}

// Touch marks e changed, the way Excalidraw reconciles concurrent edits.
func (e *Element) Touch() {
	e.Version++
	e.VersionNonce = randInt()
	e.Updated = time.Now().UnixMilli()
}

const idChars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz_-"

// NewID is a 21-character random id, like Excalidraw's nanoid.
func NewID() string {
	b := make([]byte, 21)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = idChars[b[i]&63]
	}
	return string(b)
}

func randInt() int64 {
	// odd, so never 0 (rough.js's Math.random), and under 2³¹ like Excalidraw's
	n, _ := rand.Int(rand.Reader, big.NewInt(math.MaxInt32))
	return n.Int64() | 1
}
