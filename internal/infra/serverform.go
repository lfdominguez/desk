package infra

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// serverform.go: the server picker's form (ui), the user's own way to add a
// server or change one, through the same change the Claudes' add_server and
// edit_server make (servedit.go), without the approval: the user wrote it.

// ServerFields is server name's own lines in org folder's servers file, as
// the form shows them: HostName, User, Port, IdentityFile, its group, #@ desc
// and #@ password (Options["#@ password"]). What the file's defaults or a
// Host * block give it isn't its own, so it stays out.
func ServerFields(org, name string) (ServerArgs, error) {
	a := ServerArgs{Name: name}
	f := serversPath(org)
	if f == "" {
		return a, errors.New("the organization has no servers file")
	}
	hs, _ := parseServers(f, org, false)
	var h *serverHost
	for i := range hs {
		if strings.EqualFold(hs[i].alias, name) {
			h = &hs[i]
		}
	}
	switch {
	case h == nil:
		return a, fmt.Errorf("no server %s in the servers file", name)
	case strings.HasPrefix(filepath.Base(h.file), "servers-dynamic"):
		return a, fmt.Errorf("%s comes from the file's hosts-cmd: change what that prints", name)
	}
	data, err := os.ReadFile(h.file)
	if err != nil {
		return a, err
	}
	lines := strings.Split(string(data), "\n")
	start, end := blockLines(lines, h.alias)
	if start < 0 {
		return a, fmt.Errorf("no Host %s line in %s: change it by hand", h.alias, h.file)
	}
	a.Name, a.Group = h.alias, h.group
	for _, l := range lines[start+1 : end] {
		l = strings.TrimSpace(l)
		if rest, ok := strings.CutPrefix(l, "#@"); ok {
			switch key, val := cutWord(strings.TrimSpace(rest)); strings.ToLower(key) {
			case "desc":
				a.Desc = unquote(val)
			case "password":
				a.Options = map[string]string{"#@ password": val}
			}
			continue
		}
		kw, val := sshKeyword(l)
		switch { // ssh keeps a keyword's first value
		case kw == "hostname" && a.HostName == "":
			a.HostName = val
		case kw == "user" && a.User == "":
			a.User = val
		case kw == "port" && a.Port == 0:
			a.Port, _ = strconv.Atoi(val)
		case kw == "identityfile" && a.IdentityFile == "":
			a.IdentityFile = val
		}
	}
	return a, nil
}

// ServerDiff is the edit_server change from the form's was to now: only
// what changed, a field emptied as its keyword's removal.
func ServerDiff(was, now ServerArgs) ServerArgs {
	d := ServerArgs{Name: was.Name}
	gone := func(k string) {
		if d.Options == nil {
			d.Options = map[string]string{}
		}
		d.Options[k] = ""
	}
	for _, f := range []struct {
		key      string
		was, now string
		set      *string
	}{
		{"HostName", was.HostName, now.HostName, &d.HostName},
		{"User", was.User, now.User, &d.User},
		{"IdentityFile", was.IdentityFile, now.IdentityFile, &d.IdentityFile},
		{"#@ desc", was.Desc, now.Desc, &d.Desc},
	} {
		switch {
		case f.was == f.now:
		case f.now == "":
			gone(f.key)
		default:
			*f.set = f.now
		}
	}
	switch {
	case was.Port == now.Port:
	case now.Port == 0:
		gone("Port")
	default:
		d.Port = now.Port
	}
	if p, q := was.Options["#@ password"], now.Options["#@ password"]; p != q {
		if d.Options == nil {
			d.Options = map[string]string{}
		}
		d.Options["#@ password"] = q
	}
	if now.Group != was.Group { // ponytail: "" keeps its group; out of every group is a change by hand
		d.Group = now.Group
	}
	return d
}

// ServerSave makes the form's change to org folder's servers file: tool is
// add_server (a, the new server) or edit_server (a, ServerDiff's change).
// What it says is for the toast; the caller reloads the servers.
func ServerSave(org, tool string, a ServerArgs) (string, error) {
	path, _, next, err := serverChange(org, tool, a)
	if err != nil {
		return "", err
	}
	if err := writeServers(path, next); err != nil {
		return "", err
	}
	return serverDone(tool, a.Name, path), nil
}
