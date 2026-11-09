package infra

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Server notes: durable facts Claude learns about a server (where configs
// live, quirks, how to restart things), saved with the note tool in
// $XDG_STATE_HOME/desk/infra/notes/<org>/<server>.md and handed back the first time
// the server comes up in a conversation. Plain markdown: edit or delete freely.

// maxNotes caps what a server's notes put into a prompt.
const maxNotes = 6000

func notesPath(org, server string) string {
	return filepath.Join(stateDir(), "notes", orgDirName(org), strings.ReplaceAll(server, "/", "_")+".md")
}

// addNote appends one dated line to server's notes.
func addNote(org, server, text string) error {
	text = strings.Join(strings.Fields(redact(text)), " ")
	if text == "" {
		return fmt.Errorf("empty note")
	}
	p := notesPath(org, server)
	os.MkdirAll(filepath.Dir(p), 0o700)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "- %s %s\n", time.Now().Format("2006-01-02"), text)
	return err
}

// notesFor gives server's notes as a <notes> block the first time this
// Claude process sees the server, else "".
func (t *Tab) notesFor(server string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.notesSent[server] {
		return ""
	}
	b, err := os.ReadFile(notesPath(t.org, server))
	if err != nil || len(strings.TrimSpace(string(b))) == 0 {
		return ""
	}
	if t.notesSent == nil {
		t.notesSent = map[string]bool{}
	}
	t.notesSent[server] = true
	s := string(b)
	if len(s) > maxNotes { // the newest notes matter most
		s = "…\n" + s[len(s)-maxNotes:]
	}
	return fmt.Sprintf("<notes server=%q>\n%s</notes>\n", server, s)
}

// changesScript lists what changed on a server in the last $1 hours, read-only
// and tolerant of whatever tools the server lacks (GNU find/date assumed).
const changesScript = `H=$1
echo "== packages"
since=$(date -d "$H hours ago" '+%Y-%m-%d %H:%M:%S' 2>/dev/null)
zcat -f /var/log/dpkg.log* 2>/dev/null | awk -v d="$since" '($3=="install"||$3=="upgrade"||$3=="remove"||$3=="purge") && $1" "$2>=d' | sort | tail -40
rpm -qa --last 2>/dev/null | head -15
echo "== files changed under /etc"
find /etc -xdev -type f -newermt "$H hours ago" 2>/dev/null | head -60
echo "== services started, stopped, failed"
journalctl --since "$H hours ago" -q --no-pager -o short-iso _PID=1 2>/dev/null | grep -E 'Started|Stopped|Reloaded|Failed|failed' |
  awk '{ts=$1; sub(/^[^ ]+ [^ ]+ [^ ]+ /, ""); if (!($0 in n)) order[++k]=$0; n[$0]++; last[$0]=ts}
       END {for (i=1; i<=k; i++) {m=order[i]; printf "%s  %s%s\n", last[m], (n[m]>1 ? n[m] "x " : ""), m}}' | sort | tail -40
echo "== logins"
last -F -n 15 2>/dev/null | head -15
echo "== reboots"
last -x -F reboot shutdown -n 5 2>/dev/null | head -5
echo "== containers"
docker ps -a --format '{{.Names}}  {{.Status}}  {{.Image}}' 2>/dev/null | head -20
true`

// changes runs changesScript on pane p's server over the shared connection
// (not in the user's terminal: it's read-only and long), or in Go on the
// local host under localGo (changesLocal).
func (t *Tab) changes(p *Pane, hours int) toolResult {
	hours = max(1, min(720, hours))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var b bytes.Buffer
	var err error
	if p.term.Host == "local" && localGo {
		b.WriteString(changesLocal(ctx, hours))
	} else {
		err = remoteSh(ctx, p.term.Host, p.dir, "sh -c "+shQuote(changesScript)+" changes "+fmt.Sprint(hours), nil, &b)
	}
	out := b.Bytes()
	audit(auditEntry{Org: t.org, Group: t.group, Host: p.name, Event: "command", By: "claude", Cmd: fmt.Sprintf("(changes tool, last %dh)", hours)})
	if err != nil && len(out) == 0 {
		return toolResult{Text: "changes: " + err.Error(), IsError: true}
	}
	return toolResult{Text: fmt.Sprintf("changes on %s in the last %dh:\n%s", p.name, hours, tailLines(string(out), 250, 30000))}
}
