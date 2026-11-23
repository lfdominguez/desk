package infra

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/lfdominguez/desk/internal/diff"
	"github.com/lfdominguez/desk/internal/sshconfig"
)

// servedit.go: the organization's servers file changed by its Claudes:
// servers reads it, add_server, edit_server and remove_server change it, each
// change approved by the user as a diff. A card's Claude asks through Claude
// Code's permission (ui shows ServerPreview's diff in it; CardMCP leaves
// these tools out of what runs unasked); the infra Claude through its
// approval sheet (Tab.serverTool).

// ServerTools are the tools that change the servers file.
var ServerTools = []string{"add_server", "edit_server", "remove_server"}

// ServerArgs are add_server's, edit_server's and remove_server's arguments.
type ServerArgs struct {
	Name         string            `json:"name"`
	HostName     string            `json:"hostname"`
	User         string            `json:"user"`
	Port         int               `json:"port"`
	IdentityFile string            `json:"identity_file"`
	Group        string            `json:"group"`
	Desc         string            `json:"desc"`
	Options      map[string]string `json:"options"`
	Reason       string            `json:"reason"`
}

// deskKeys are the "#@" keys a Claude may set in a Host block (servers.go).
// password-raw isn't one: a password stays out of Claude's hands, and its
// "nothing changes" would tell a right guess.
var deskKeys = []string{"desc", "tags", "password", "vault", "wol", "nagios", "pin", "hidden", "on", "via"}

func init() {
	cardTools = append(cardTools, serverToolDefs...)
	mcpTools = append(mcpTools, serverToolDefs...)
}

var serverProps = map[string]any{
	"name":     map[string]any{"type": "string", "description": "The server's name, its Host alias: letters, digits, '.', '_' and '-'."},
	"hostname": map[string]any{"type": "string", "description": "Its address or DNS name (HostName)."},
	"user":     map[string]any{"type": "string", "description": "The user to log in as."},
	"port":     map[string]any{"type": "integer", "description": "The ssh port, when not 22."},
	"identity_file": map[string]any{"type": "string", "description": "The private key to log in with, e.g. ~/.ssh/acme_ed25519. " +
		"desk adds IdentitiesOnly yes, so only that key is offered (set options IdentitiesOnly to \"\" not to)."},
	"group": map[string]any{"type": "string", "description": "The #@ group it is listed under, e.g. eu/web (made when missing). On edit_server, moves it there."},
	"desc":  map[string]any{"type": "string", "description": "What it is, in a few words."},
	"options": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"},
		"description": "Any other ssh_config keyword (ProxyJump, ForwardAgent, LocalForward…) or desk key, written \"#@ tags\" " +
			"(#@ tags, #@ password, #@ wol, #@ nagios, #@ pin, #@ hidden…), to its value. On edit_server an empty value removes it."},
	"reason": map[string]any{"type": "string", "description": "One short sentence for the user: why."},
}

var serverToolDefs = []map[string]any{
	{
		"name": "servers",
		"description": "The organization's servers file (an OpenSSH config with desk's \"#@\" lines: groups, descriptions, passwords from vaults), " +
			"where it is and what is in it. Read it before changing a server.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		"annotations": map[string]any{"readOnlyHint": true},
	},
	{
		"name": "add_server",
		"description": "Add a server to the organization's servers file, once the user approves the change: desk lists it under its group, " +
			"and connects to it (as does ssh -F with the file). Use it whenever the user wants a server added; never edit the file another way.",
		"inputSchema": map[string]any{"type": "object", "properties": serverProps, "required": []string{"name", "reason"}},
	},
	{
		"name": "edit_server",
		"description": "Change a server of the organization's servers file, once the user approves the change: only what you give changes " +
			"(a keyword given replaces every line of it in the server's block), options with an empty value are removed, group moves it.",
		"inputSchema": map[string]any{"type": "object", "properties": serverProps, "required": []string{"name", "reason"}},
	},
	{
		"name":        "remove_server",
		"description": "Remove a server from the organization's servers file, once the user approves the change.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"name": serverProps["name"], "reason": serverProps["reason"]}, "required": []string{"name", "reason"}},
		"annotations": map[string]any{"destructiveHint": true},
	},
}

// settings are a's lines for the Host block, as (key, value) in the order
// written: ssh keywords as given, desk's as "#@ key". An empty value removes.
func (a ServerArgs) settings() ([][2]string, error) {
	var kv [][2]string
	add := func(k, v string) { kv = append(kv, [2]string{k, strings.TrimSpace(v)}) }
	if a.HostName != "" {
		add("HostName", a.HostName)
	}
	if a.User != "" {
		add("User", a.User)
	}
	if a.Port < 0 || a.Port > 65535 {
		return nil, fmt.Errorf("port %d: 1 to 65535", a.Port)
	}
	if a.Port != 0 {
		add("Port", strconv.Itoa(a.Port))
	}
	given := map[string]bool{}
	for k := range a.Options {
		given[strings.ToLower(strings.TrimSpace(k))] = true
	}
	if a.IdentityFile != "" {
		add("IdentityFile", a.IdentityFile)
		if !given["identitiesonly"] { // else ssh offers the agent's keys first, and a server may close after too many
			add("IdentitiesOnly", "yes")
		}
	}
	if a.Desc != "" {
		add("#@ desc", a.Desc)
	}
	keys := make([]string, 0, len(a.Options))
	for k := range a.Options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		name := strings.TrimSpace(k)
		if d, ok := strings.CutPrefix(name, "#@"); ok {
			d = strings.ToLower(strings.TrimSpace(d))
			if d == "password-raw" {
				return nil, errors.New("#@ password-raw is the user's to write: a password stays out of your hands. Use a vault item (#@ password ITEM), or ask the user to add it")
			}
			if !slices.Contains(deskKeys, d) {
				return nil, fmt.Errorf("%q: desk's keys are #@ %s", k, strings.Join(deskKeys, ", #@ "))
			}
			name = "#@ " + d
		} else if lk := strings.ToLower(name); !sshconfig.Known(name) || lk == "host" || lk == "match" || lk == "include" {
			return nil, fmt.Errorf("%q is not an ssh_config keyword a server's block takes (desk's keys are written \"#@ tags\")", k)
		}
		add(name, a.Options[k])
	}
	for _, p := range kv {
		if strings.ContainsAny(p[1], "\r\n") {
			return nil, fmt.Errorf("%s: one line only", p[0])
		}
	}
	return kv, nil
}

// lineKey is a servers file line's key as settings names it, lower case:
// "user", "#@ desc"; "" for a blank or comment line.
func lineKey(l string) string {
	l = strings.TrimSpace(l)
	if rest, ok := strings.CutPrefix(l, "#@"); ok {
		key, _ := cutWord(strings.TrimSpace(rest))
		return "#@ " + strings.ToLower(key)
	}
	kw, _ := sshKeyword(l)
	return kw
}

// setLines sets kv in a Host block (its Host line first): each key's lines
// make way for one, where the first was (else at the end); "" removes them.
func setLines(block []string, kv [][2]string) []string {
	for _, p := range kv {
		key, at, out := strings.ToLower(p[0]), -1, []string{block[0]}
		for _, l := range block[1:] {
			if lineKey(l) == key {
				if at < 0 {
					at = len(out)
				}
				continue
			}
			out = append(out, l)
		}
		if p[1] != "" {
			if at < 0 {
				at = len(out)
			}
			out = slices.Insert(out, at, "    "+p[0]+" "+p[1])
		}
		block = out
	}
	return block
}

// groupOf is the group a "#@ group" line starts, and whether it is one.
func groupOf(l string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(l), "#@")
	if !ok {
		return "", false
	}
	key, val := cutWord(strings.TrimSpace(rest))
	return strings.Trim(unquote(val), "/"), key == "group"
}

func blank(l string) bool { return strings.TrimSpace(l) == "" }

// insertBlock puts a Host block into a servers file's text: at the end of
// group's section (one made at the end when there is none), or with no group
// before the first section, where hosts belong to no group.
func insertBlock(text, group string, block []string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if text == "" {
		lines = nil
	}
	at, in, first := len(lines), -1, -1
	for i, l := range lines {
		g, ok := groupOf(l)
		if !ok {
			continue
		}
		if in >= 0 { // the section after group's
			at = i
			break
		}
		if first < 0 {
			first = i
		}
		if group != "" && g == group {
			in = i
		}
	}
	switch {
	case group == "" && first >= 0:
		at = first
	case group != "" && in < 0:
		block = append([]string{"#@ group " + group}, block...)
	}
	for at > 0 && blank(lines[at-1]) {
		at--
	}
	if at > 0 {
		block = append([]string{""}, block...)
	}
	if at < len(lines) && !blank(lines[at]) {
		block = append(block, "")
	}
	return strings.Join(slices.Concat(lines[:at], block, lines[at:]), "\n") + "\n"
}

// serverChange is tool's change to org's servers: the file it changes, and
// its text before and after.
func serverChange(org, tool string, a ServerArgs) (path, old, next string, err error) {
	if !hostName.MatchString(a.Name) {
		return "", "", "", fmt.Errorf("name %q: letters, digits, '.', '_' and '-' only", a.Name)
	}
	a.Group = strings.Trim(strings.TrimSpace(a.Group), "/")
	if strings.ContainsAny(a.Group, "\r\n") {
		return "", "", "", errors.New("group: one line only")
	}
	kv, err := a.settings()
	if err != nil {
		return "", "", "", err
	}
	var h *serverHost
	if f := serversPath(org); f != "" {
		hs, _ := parseServers(f, org, false)
		for i := range hs {
			if strings.EqualFold(hs[i].alias, a.Name) {
				h = &hs[i]
			}
		}
	}
	if tool == "add_server" {
		if h != nil {
			return "", "", "", fmt.Errorf("%s is already a server: edit_server changes it", a.Name)
		}
		path = serversTarget(org)
		data, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", "", "", err
		}
		block := []string{"Host " + a.Name}
		for _, p := range kv {
			if p[1] != "" {
				block = append(block, "    "+p[0]+" "+p[1])
			}
		}
		return path, string(data), insertBlock(string(data), a.Group, block), nil
	}
	if h == nil {
		return "", "", "", fmt.Errorf("no server %s in the organization's servers file: servers shows them", a.Name)
	}
	if strings.HasPrefix(filepath.Base(h.file), "servers-dynamic") {
		return "", "", "", fmt.Errorf("%s comes from the file's hosts-cmd: change what that prints", a.Name)
	}
	data, err := os.ReadFile(h.file)
	if err != nil {
		return "", "", "", err
	}
	old = string(data)
	lines := strings.Split(old, "\n")
	start, end := blockLines(lines, h.alias)
	if start < 0 { // its Host line names it by a pattern
		return "", "", "", fmt.Errorf("no Host %s line in %s: change it by hand", h.alias, h.file)
	}
	e := end
	for e > start+1 && blank(lines[e-1]) {
		e--
	}
	switch {
	case tool == "remove_server":
		if names := strings.Fields(lines[start])[1:]; len(names) > 1 { // one Host line for several: only this name goes
			lines[start] = "Host " + strings.Join(slices.DeleteFunc(names, func(n string) bool { return n == h.alias }), " ")
		} else {
			lines = slices.Delete(lines, start, end)
		}
		next = strings.Join(lines, "\n")
	case len(kv) == 0 && a.Group == "":
		return "", "", "", errors.New("nothing to change: give what changes")
	default:
		block := setLines(slices.Clone(lines[start:e]), kv)
		if a.Group != "" && a.Group != h.group {
			next = insertBlock(strings.Join(slices.Concat(lines[:start], lines[end:]), "\n"), a.Group, block)
		} else {
			next = strings.Join(slices.Concat(lines[:start], block, lines[e:]), "\n")
		}
	}
	if next == old {
		return "", "", "", fmt.Errorf("%s already is that way: nothing changes", a.Name)
	}
	return h.file, old, next, nil
}

func serverArgs(raw json.RawMessage) (ServerArgs, error) {
	var a ServerArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, fmt.Errorf("arguments: %w", err)
	}
	return a, nil
}

// rawPassword is a "#@ password-raw" line's value: a password in the file,
// which neither Claude (servers) nor a screen showing a change gets to see.
var rawPassword = regexp.MustCompile(`(#@[ \t]*password-raw[ \t]+)\S.*`)

// hideRaw is servers file text (or a diff of it) with its raw passwords hidden.
func hideRaw(s string) string { return rawPassword.ReplaceAllString(s, "${1}[hidden]") }

// ServerPreview is tool's change to org's servers file (raw its arguments),
// as the file and a diff for the user to approve.
func ServerPreview(org, tool string, raw json.RawMessage) (path, change string, err error) {
	a, err := serverArgs(raw)
	if err != nil {
		return "", "", err
	}
	path, old, next, err := serverChange(org, tool, a)
	if err != nil {
		return "", "", err
	}
	change, _, _ = diff.Unified(old, next, 2)
	return path, hideRaw(change), nil
}

// ServerApply makes tool's change to org's servers file: what is said back
// to Claude. The caller reloads the servers.
func ServerApply(org, tool string, raw json.RawMessage) (string, error) {
	a, err := serverArgs(raw)
	if err != nil {
		return "", err
	}
	return ServerSave(org, tool, a)
}

// writeServers writes a servers file: a new one 0600 (it may hold a
// password-raw), an existing one keeps its mode.
func writeServers(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o600)
}

func serverDone(tool, name, path string) string {
	return map[string]string{
		"add_server":    "added " + name + " to " + path + ": desk lists it, and connects to it by that name",
		"edit_server":   "changed " + name + " in " + path,
		"remove_server": "removed " + name + " from " + path,
	}[tool]
}

// ServersText is servers' answer for org: where its servers file is and
// what it holds.
func ServersText(org string) (string, error) {
	path := serversTarget(org)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "no servers file yet: add_server makes " + path, nil
	}
	if err != nil {
		return "", err
	}
	return path + ":\n" + hideRaw(string(data)), nil
}

// serverTool is the infra Claude's servers, add_server, edit_server and
// remove_server: a change shows in the approval sheet as a diff.
func (t *Tab) serverTool(name string, raw json.RawMessage) toolResult {
	fail := func(err error) toolResult { return toolResult{Text: err.Error(), IsError: true} }
	org := orgOfInfra(t.dir)
	if name == "servers" {
		text, err := ServersText(org)
		if err != nil {
			return fail(err)
		}
		return toolResult{Text: text}
	}
	a, err := serverArgs(raw)
	if err != nil {
		return fail(err)
	}
	path, old, next, err := serverChange(org, name, a)
	if err != nil {
		return fail(err)
	}
	text, _, _ := diff.Unified(old, next, 2)
	title := map[string]string{"add_server": "Claude wants to add a server", "edit_server": "Claude wants to change a server",
		"remove_server": "Claude wants to remove a server"}[name]
	r := t.approve(&approval{cmd: hideRaw(text), reason: a.Reason, file: path, title: title, task: taskFrom(t)})
	verdict := "approved"
	if !r.ok {
		verdict = "denied"
	}
	audit(auditEntry{Org: t.org, Group: t.group, Host: a.Name, Event: "file", By: "claude", Cmd: name + " " + a.Name, Verdict: verdict})
	if !r.ok {
		return fail(errors.New("the user denied it" + r.note + ". Ask them what they want."))
	}
	if now, _ := os.ReadFile(path); string(now) != old {
		return fail(fmt.Errorf("%s changed while the user looked at it: call %s again", path, name))
	}
	if err := writeServers(path, next); err != nil {
		return fail(err)
	}
	done := make(chan struct{})
	dispatch(func(m *model) { m.reloadConfig(); close(done) }) // the sidebar shows it, and connect finds it
	<-done
	return toolResult{Text: serverDone(name, a.Name, path)}
}
