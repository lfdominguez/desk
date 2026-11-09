package infra

import (
	"encoding/base64"
	"strings"
)

// Drawings: every Claude desk runs reads and draws the organization's
// drawings (Excalidraw files) through these tools, answered by desk's
// board (Cards.Draw for the infra Claudes, ServeCards' handler for the
// rest). A drawing's own Claude gets them through `desk mcp <sock>
// draw=<path>`, its drawing the default.

var drawingProp = map[string]any{"type": "string", "description": "The drawing's name (see drawings). " +
	"A drawing's own Claude may leave it out: its drawing."}

var elementsDoc = "Excalidraw elements, with only what differs from the defaults (stroke #1e1e1e, " +
	"no background, roughness 1 hand-drawn, 0 clean; strokeWidth 2; fontSize 20): type (rectangle, ellipse, diamond, arrow, line, text, freedraw, frame), " +
	"x, y, width, height (scene units, y down), strokeColor, backgroundColor, fillStyle (solid, hachure, cross-hatch), strokeStyle " +
	"(solid, dashed, dotted), roughness, text, fontSize, points (lines and arrows: [[0,0],[dx,dy],…] from x, y), name (frames), " +
	"frameId (the frame it sits in: drawn clipped to it, moves with it), groupIds (innermost first: a click selects the outermost whole). " +
	"desk's own keys: label (text centered in a shape), from and to (an arrow's ends attach to these shapes' ids, " +
	"drawn straight between them when it has no bends)."

var patchProps = map[string]any{
	"drawing": drawingProp,
	"add": map[string]any{"type": "array", "items": map[string]any{"type": "object"},
		"description": "New elements: " + elementsDoc + " An id is made unless given."},
	"update": map[string]any{"type": "array", "items": map[string]any{"type": "object"},
		"description": "Changes: each an id and the keys to set (label \"\" takes a label away)."},
	"delete": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Ids to delete."},
}

var drawingTools = []map[string]any{
	{
		"name":        "drawings",
		"description": "The drawings you can read: the organization's and, for a card, the card's own.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		"annotations": map[string]any{"readOnlyHint": true},
	},
	{
		"name": "drawing_read",
		"description": "A drawing as a picture and its elements, one a line (id, type, box, colors, label, an arrow's ends), " +
			"to change them by id. The open view's edits are in it.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"drawing": drawingProp}},
		"annotations": map[string]any{"readOnlyHint": true},
	},
	{
		"name": "drawing_edit",
		"description": "Draw: add, change and delete elements, as one step the user can undo (shown live, marked as yours, " +
			"if they have it open). A name not taken makes a new drawing. Only when the user asked you to draw; " +
			"for what they didn't ask for, drawing_suggest. Read the drawing first; returns the ids it touched.",
		"inputSchema": map[string]any{"type": "object", "properties": patchProps},
	},
	{
		"name": "drawing_suggest",
		"description": "Propose a change instead of making it: it floats over the drawing as a ghost for the user to approve, " +
			"send back with a comment (it comes to you; your next suggestion replaces this one) or dismiss. " +
			"For improvements nobody asked you to draw.",
		"inputSchema": map[string]any{"type": "object", "properties": withNote(patchProps), "required": []string{"note"}},
	},
}

func withNote(m map[string]any) map[string]any {
	out := map[string]any{"note": map[string]any{"type": "string", "description": "One or two sentences: what it changes and why."}}
	for k, v := range m {
		out[k] = v
	}
	return out
}

// readDrawingTools are those a Claude that only reads gets: it may still suggest.
func readDrawingTools() []map[string]any {
	return []map[string]any{drawingTools[0], drawingTools[1], drawingTools[3]}
}

// imageMark leads a tool answer that carries a picture: ImageResult's,
// split again by resultOf so the handler's (text, ok) needn't change.
const imageMark = "\x00desk-png\x00"

// ImageResult is a tool answer of text with a PNG for Claude to see.
func ImageResult(png []byte, text string) string {
	return imageMark + base64.StdEncoding.EncodeToString(png) + "\n" + text
}

// resultOf is a handler's answer as a result: its picture apart, its text redacted.
func resultOf(text string, ok bool) toolResult {
	r := toolResult{IsError: !ok}
	if rest, found := strings.CutPrefix(text, imageMark); found {
		r.Image, text, _ = strings.Cut(rest, "\n")
	}
	r.Text = redact(text)
	return r
}

// content is a result as MCP content blocks.
func (r toolResult) content() []map[string]any {
	out := []map[string]any{{"type": "text", "text": r.Text}}
	if r.Image != "" {
		out = append(out, map[string]any{"type": "image", "data": r.Image, "mimeType": "image/png"})
	}
	return out
}

// DrawMCP is CardMCP for the Claude of the drawing at path.
func DrawMCP(sock, path string) (string, []string) { return toolsMCP(sock, "draw="+path, drawingTools) }

// drawTool answers an infra Claude's drawing tool through desk's board.
func (t *Tab) drawTool(c toolCall) toolResult {
	hub.Lock()
	draw := cards.Draw
	hub.Unlock()
	if draw == nil {
		return toolResult{Text: "no desk board here", IsError: true}
	}
	return resultOf(draw("infra:"+t.org, c.Name, c.Args))
}
