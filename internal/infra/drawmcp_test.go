package infra

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestImageResult(t *testing.T) {
	s := ImageResult([]byte("PNG"), "two\nlines TOKEN=abc")
	r := resultOf(s, true)
	if r.Image != base64.StdEncoding.EncodeToString([]byte("PNG")) || r.Text != "two\nlines TOKEN=[redacted]" || r.IsError {
		t.Errorf("%+v", r)
	}
	c := r.content()
	if len(c) != 2 || c[0]["text"] != r.Text || c[1]["type"] != "image" || c[1]["data"] != r.Image || c[1]["mimeType"] != "image/png" {
		t.Errorf("%v", c)
	}
	if r := resultOf("plain", false); r.Image != "" || r.Text != "plain" || !r.IsError || len(r.content()) != 1 {
		t.Errorf("%+v", r)
	}
}

func TestDrawMCPServes(t *testing.T) {
	noUnix(t)
	runtimeA(t)
	sock, err := ServeCards(func(card, name string, args json.RawMessage) (string, bool) {
		return ImageResult([]byte{1, 2}, card+"|"+name), true
	})
	if err != nil {
		t.Fatal(err)
	}
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"drawing_read","arguments":{}}}`
	var out strings.Builder
	mcpServe(sock, "draw=/o/.desk/drawings/A b.excalidraw", strings.NewReader(in), &out)
	s := out.String()
	if !strings.Contains(s, `"drawing_suggest"`) || strings.Contains(s, `"alerts"`) {
		t.Errorf("a drawing's Claude gets the drawing tools only: %s", s)
	}
	if !strings.Contains(s, `"text":"draw:/o/.desk/drawings/A b.excalidraw|drawing_read"`) || !strings.Contains(s, `{"data":"AQI=","mimeType":"image/png","type":"image"}`) {
		t.Errorf("its calls, with the picture: %s", s)
	}
	out.Reset()
	mcpServe(sock, "", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), &out)
	if !strings.Contains(out.String(), `"drawing_edit"`) || !strings.Contains(out.String(), `"run"`) {
		t.Error("an infra Claude has them beside its own")
	}
	cfg, allow := DrawMCP("/run/x.sock", "/o/D.excalidraw")
	if !strings.Contains(cfg, `"draw=/o/D.excalidraw"`) || strings.Join(allow, ",") != "mcp__desk__drawings,mcp__desk__drawing_read,mcp__desk__drawing_edit,mcp__desk__drawing_suggest" {
		t.Errorf("%s %v", cfg, allow)
	}
	for i, ro := range []bool{true, true, false, false} {
		_, has := drawingTools[i]["annotations"]
		if has != ro {
			t.Errorf("%s: read-only %v", drawingTools[i]["name"], has)
		}
		if ro && drawingTools[i]["annotations"].(map[string]any)["readOnlyHint"] != true {
			t.Errorf("%s: hint", drawingTools[i]["name"])
		}
	}
	if len(readDrawingTools()) != 3 || readDrawingTools()[2]["name"] != "drawing_suggest" {
		t.Error("a reader may suggest, never edit")
	}
}

func TestInfraDrawTool(t *testing.T) {
	defer func(c Cards) { cards = c }(cards)
	cards = Cards{}
	tab := &Tab{org: "Acme"}
	if r := tab.callTool(toolCall{Name: "drawings"}); !r.IsError || r.Text != "no desk board here" {
		t.Errorf("%+v", r)
	}
	var who, name string
	var args []byte
	cards.Draw = func(w, n string, a []byte) (string, bool) {
		who, name, args = w, n, a
		return ImageResult([]byte{7}, "seen"), true
	}
	r := tab.callTool(toolCall{Name: "drawing_read", Args: json.RawMessage(`{"drawing":"X"}`)})
	if r.IsError || r.Text != "seen" || r.Image != "Bw==" || who != "infra:Acme" || name != "drawing_read" || string(args) != `{"drawing":"X"}` {
		t.Errorf("%+v %s %s %s", r, who, name, args)
	}
}
