package ui

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/orgs"
)

// serverTool is a card Claude's servers, add_server, edit_server and
// remove_server on its organization's servers file. A change runs once the
// user allowed it in the card's permission box, which showed serverPreview.
func (u *ui) serverTool(id, name string, raw json.RawMessage) (string, bool) {
	org, err := onBoard(u, func() (string, error) {
		c := u.b.Card(id)
		switch {
		case c == nil:
			return "", errors.New("the card is gone from the board")
		case c.Org == "":
			return "", errors.New("the card has no organization, so no servers file")
		}
		return orgs.Dir(u.root, c.Org, ""), nil
	})
	if err != nil {
		return err.Error(), false
	}
	var text string
	if name == "servers" {
		text, err = infra.ServersText(org)
	} else if text, err = infra.ServerApply(org, name, raw); err == nil && u.infra != nil {
		u.infra.Reload() // the sidebar lists it, and its panes connect with it
	}
	if err != nil {
		return err.Error(), false
	}
	return text, true
}

// serverPreview is what a permission for a server tool shows: the file and
// the change, or why it won't work; "" for other tools.
func (u *ui) serverPreview(org string, r *claude.Request) string {
	tool, ok := strings.CutPrefix(r.Tool, "mcp__desk__")
	if !ok || !slices.Contains(infra.ServerTools, tool) || org == "" {
		return ""
	}
	path, change, err := infra.ServerPreview(orgs.Dir(u.root, org, ""), tool, r.Input)
	if err != nil {
		return "It won't work: " + err.Error()
	}
	return path + "\n" + change
}
