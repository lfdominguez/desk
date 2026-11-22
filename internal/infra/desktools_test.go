package infra

import (
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

// TestDeskMCP: the desk Claude's server lists the whole board's tools, none
// of a card's own, and calls as "desk:"; its config names the group.
func TestDeskMCP(t *testing.T) {
	noUnix(t)
	sock := filepath.Join(runtimeA(t), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		var tc toolCall
		json.NewDecoder(c).Decode(&tc)
		json.NewEncoder(c).Encode(toolResult{Text: tc.Card + "|" + tc.Name})
	}()
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"card_message","arguments":{}}}`
	var out strings.Builder
	mcpServe(sock, "desk", strings.NewReader(in), &out)
	if s := out.String(); !strings.Contains(s, `"digest"`) || strings.Contains(s, `"ticket_create"`) || !strings.Contains(s, "desk:|card_message") {
		t.Errorf("served: %s", s)
	}
	cfg, allow := DeskMCP("/run/x.sock")
	if !strings.Contains(cfg, `"desk"]`) || strings.Join(allow, ",") != "mcp__desk__cards,mcp__desk__card_read,mcp__desk__digest,mcp__desk__card_create,mcp__desk__card_message" {
		t.Errorf("config %s allows %v", cfg, allow)
	}
}
