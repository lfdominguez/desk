package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
)

// A message that starts with "!" is a shell command the user runs directly,
// as in Claude Code: no Claude, no permission prompt, in the card's folder.
// The card is the user's while it runs (out of Blocked) and the message box
// is locked; its output shows as it comes, and when it ends goes to Claude
// (Claude Code's <bash-input>/<bash-stdout> lines) with its exit status and
// time: unlike Claude Code, the session carries on from it by itself.

// bang is the command of a "!" message, "" when text is a message.
func bang(text string) string {
	cmd, ok := strings.CutPrefix(strings.TrimSpace(text), "!")
	if !ok {
		return ""
	}
	return strings.TrimSpace(cmd)
}

const (
	shellFor  = 30 * time.Minute // a stuck one ends; Stop ends it sooner
	shellKept = 32 << 10         // what of its output the chat and Claude get
)

// Where a shell entry's output stands with Claude (its Path, otherwise unused by "shell").
const (
	toClaudeNext = "next" // Claude gets it with the next message
	toClaudeSent = "sent"
)

// runShell runs a command, its output into w; tests replace it.
var runShell = func(ctx context.Context, dir, cmd string, w io.Writer) error {
	sh := infra.ShellArgs(runtime.GOOS, cmd)
	c := exec.CommandContext(ctx, sh[0], sh[1:]...)
	c.Dir, c.Stdout, c.Stderr = dir, w, w
	c.WaitDelay = 2 * time.Second // a Stop doesn't wait for what the command left running
	return c.Run()
}

// shellBuf is a running command's output so far: its end, all that's
// shown (a verbose command would fill memory over its 30 minutes).
type shellBuf struct {
	mu  sync.Mutex
	b   []byte
	cut bool // its start was dropped
	n   int  // writes so far, to tell new output
}

func (s *shellBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b, s.n = append(s.b, p...), s.n+1
	if len(s.b) > 2*shellKept { // past the slack: keep what text shows, and room for trailing newlines
		s.b, s.cut = append(s.b[:0:0], s.b[len(s.b)-shellKept-shellKept/2:]...), true
	}
	return len(p), nil
}

// since is text when something was written after the first n writes: the
// ticker sends nothing (no frame) when the command was quiet.
func (s *shellBuf) since(n int) (string, int, bool) {
	t, m := s.text()
	return t, m, m != n
}

// text is the output to show, and its writes so far.
func (s *shellBuf) text() (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := strings.TrimRight(string(s.b), "\n")
	if len(t) > shellKept || s.cut {
		t = "…" + t[max(0, len(t)-shellKept):] // the end is what matters
	}
	return t, s.n
}

// shell runs card id's "!" command cmd, its output into the chat as it comes.
func (u *ui) shell(id, cmd string) {
	c := u.b.Card(id)
	if c == nil {
		return
	}
	if u.shellStop[id] != nil {
		u.showToast(c.Key()+": a command is still running: Stop it, or wait", true)
		return
	}
	dir := orgs.Dir(u.root, c.Org, c.Dir)
	before := c.Status
	c.Status, c.Note, c.Updated = kanban.StatusWorking, "Running your command: "+firstLine(cmd), time.Now() // the user acts: not blocked, not Claude's
	u.moveTo(id, "In progress")
	if c := u.b.Card(id); c != nil {
		u.notifyChange(c, before)
	}
	u.save()
	u.chats[id] = append(u.chat(id), kanban.ChatMsg{Who: "shell", Cmd: cmd, Detail: "running…", At: time.Now()}) // kept once it's done
	n := len(u.chat(id)) - 1
	ctx, cancel := context.WithTimeout(context.Background(), shellFor)
	u.shellStop[id] = cancel
	out := &shellBuf{}
	entry := func() *kanban.ChatMsg {
		if ms := u.chat(id); n < len(ms) && ms[n].Who == "shell" && ms[n].Cmd == cmd {
			return &ms[n]
		}
		return nil
	}
	done := make(chan struct{})
	go func() { // the output so far, twice a second
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		sent := 0
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if s, n, ok := out.since(sent); ok { // quiet: no frame
					sent = n
					u.later(func() {
						if m := entry(); m != nil && m.Detail == "running…" {
							m.Text = s
						}
					})
				}
			}
		}
	}()
	go func() {
		start := time.Now()
		err := runShell(ctx, dir, cmd, out)
		close(done)
		took := time.Since(start).Round(100 * time.Millisecond)
		stopped := ctx.Err()
		cancel()
		status := "exit 0"
		var ee *exec.ExitError
		switch {
		case errors.Is(stopped, context.Canceled):
			status = "stopped"
		case stopped != nil:
			status = fmt.Sprintf("stopped after %s", shellFor)
		case errors.As(err, &ee):
			status = fmt.Sprintf("exit %d", ee.ExitCode())
		case err != nil:
			status = err.Error()
		}
		text, _ := out.text()
		u.later(func() {
			delete(u.shellStop, id)
			stderr := ""
			if status != "exit 0" {
				stderr = status
			}
			u.shellOut[id] += fmt.Sprintf("<bash-input>%s</bash-input>\n<bash-stdout>%s</bash-stdout>\n<bash-stderr>%s</bash-stderr>\n", cmd, text, stderr)
			if m := entry(); m != nil {
				m.Text, m.Detail, m.Path = text, status+" · "+took.String(), toClaudeNext
			}
			u.deliver(id, fmt.Sprintf("My command ended: %s after %s.", status, took), true) // Claude carries on from it; the chat shows the box, not this line
			if c := u.b.Card(id); c != nil && u.shellOut[id] != "" {                         // not sent (its terminal is open): the user's turn
				c.Status, c.Note = kanban.StatusWaiting, "Your command ended: "+status
				u.save()
			}
			if m := entry(); m != nil {
				kept := *m
				kept.Path = "" // where it stands with Claude lasts as long as u.shellOut: this run
				if err := kanban.AppendChat(kanban.ChatPath(u.path, id), kept); err != nil {
					u.showToast("Couldn't keep the command's output: "+err.Error(), true)
				}
			}
		})
	}()
}

// shellSent marks card id's shell entries waiting for Claude as sent.
func (u *ui) shellSent(id string) {
	for i, m := range u.chat(id) {
		if m.Who == "shell" && m.Path == toClaudeNext {
			u.chats[id][i].Path = toClaudeSent
		}
	}
}
