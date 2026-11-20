package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/kanban"
)

// TestServerTools: a card's Claude reads its organization's servers file
// and, once allowed, changes it; the workspace Claude can't; a card without
// an organization has none.
func TestServerTools(t *testing.T) {
	for _, v := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "XDG_CACHE_HOME"} {
		t.Setenv(v, t.TempDir()) // never the user's infra session
	}
	h := newHarness(t)
	c, _ := wsCard(h)
	localServer(t, h.u.root, "Acme")
	file := filepath.Join(h.u.root, "Acme", ".desk", "ssh_config")
	h.u.infra = infra.New(h.u.root, func() {})
	t.Cleanup(h.u.infra.Close)
	if text, ok := h.tool(c.ID, "servers", `{}`); !ok || text != file+":\n#@ group web\nHost local\n" {
		t.Fatalf("servers: %q %v", text, ok)
	}
	if text, ok := h.tool(c.ID, "add_server", `{"name":"db","hostname":"192.0.2.7","identity_file":"~/.ssh/db","group":"web"}`); !ok || !strings.Contains(text, "added db") {
		t.Fatalf("add: %q %v", text, ok)
	}
	if b, _ := os.ReadFile(file); !strings.HasSuffix(string(b), "Host local\n\nHost db\n    HostName 192.0.2.7\n    IdentityFile ~/.ssh/db\n    IdentitiesOnly yes\n") {
		t.Errorf("file:\n%s", b)
	}
	if text, ok := h.tool(c.ID, "remove_server", `{"name":"nope"}`); ok || !strings.Contains(text, "no server nope") {
		t.Errorf("missing: %q %v", text, ok)
	}
	if text, ok := h.tool("ws:"+c.ID, "add_server", `{"name":"x"}`); ok || text != "no tool add_server" {
		t.Errorf("workspace: %q %v", text, ok)
	}
	if text, ok := h.tool("gone", "servers", `{}`); ok || !strings.Contains(text, "gone from the board") {
		t.Errorf("gone: %q %v", text, ok)
	}
	bare := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "No org"})
	if text, ok := h.tool(bare.ID, "servers", `{}`); ok || !strings.Contains(text, "no organization") {
		t.Errorf("no org: %q %v", text, ok)
	}
}

// TestServerPermission: the permission box for a server tool shows the
// file and the change it makes, or why it won't work.
func TestServerPermission(t *testing.T) {
	h := newHarness(t)
	c, _ := wsCard(h)
	localServer(t, h.u.root, "Acme")
	h.u.procs[c.ID] = &fakeClaude{}
	h.u.view = "sess:" + c.ID
	ask := func(tool, in string) string {
		h.u.onEvent(c.ID, 0, claude.Event{Kind: claude.KindAsk, Ask: &claude.Request{ID: "r", Tool: tool, Line: tool, Input: json.RawMessage(in)}})
		return h.u.asks[c.ID].preview
	}
	if p := ask("mcp__desk__edit_server", `{"name":"local","user":"me"}`); !strings.Contains(p, filepath.Join("Acme", ".desk", "ssh_config")+"\n") || !strings.Contains(p, "+    User me") {
		t.Errorf("preview %q", p)
	}
	h.frame() // draws it
	if p := ask("mcp__desk__add_server", `{"name":"local"}`); !strings.HasPrefix(p, "It won't work: local is already a server") {
		t.Errorf("refused %q", p)
	}
	if p := ask("mcp__desk__servers", `{}`); p != "" {
		t.Errorf("read tool %q", p)
	}
	if p := h.u.serverPreview("", &claude.Request{Tool: "mcp__desk__add_server"}); p != "" {
		t.Errorf("no org %q", p)
	}
}
