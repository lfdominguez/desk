package infra

import (
	"github.com/lfdominguez/desk/internal/testwait"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestKeepsLoginDefault: panes clear their login output unless the org's
// desk.toml says clear_login = false.
func TestKeepsLoginDefault(t *testing.T) {
	for toml, want := range map[string]bool{"": false, "clear_login = true\n": false, "clear_login = false\n": true} {
		dir := filepath.Join(t.TempDir(), "Acme", infraDir)
		write(t, filepath.Join(orgOfInfra(dir), ".desk", "desk.toml"), toml)
		if o, err := orgFor(dir); err != nil || o.keepsLogin() != want {
			t.Errorf("%q: keepsLogin() = %v, want %v (%v)", toml, o.keepsLogin(), want, err)
		}
	}
	if (*orgConfig)(nil).keepsLogin() {
		t.Error("no config: want the clear")
	}
}

// TestLoginKeptInject: with keepLogin, the injection keeps what came above
// the prompt (no trailing blank lines) and asks for the replay mark, on
// POSIX shells and PowerShell; without, neither.
func TestLoginKeptInject(t *testing.T) {
	for _, keep := range []bool{false, true} {
		for _, prompt := range []string{"$ ", `PS C:\> `} {
			term, r := fakeTermA(t, "web1")
			term.replay(paneState{keepLogin: keep})
			term.emu.Write([]byte("Welcome\r\n  motd  \r\n\r\n" + prompt))
			term.maybeInject()
			got := readPipeA(r)
			if strings.Contains(got, "6974") != keep {
				t.Errorf("keep %v, %q: replay mark in %q", keep, prompt, got)
			}
			want := ""
			if keep {
				want = "Welcome\n  motd"
			}
			if term.banner != want {
				t.Errorf("keep %v, %q: banner %q", keep, prompt, term.banner)
			}
		}
	}
	term, _ := fakeTermA(t, "web1") // nothing above the prompt: nothing to put back
	term.keepLogin = true
	term.emu.Write([]byte("\r\n$ "))
	term.maybeInject()
	if term.banner != "" {
		t.Errorf("blank lines kept: %q", term.banner)
	}
}

// TestLoginTextCap: only the last keptLines lines above the prompt come back.
func TestLoginTextCap(t *testing.T) {
	term, _ := fakeTermA(t, "web1")
	for range keptLines + 10 {
		term.emu.Write([]byte("x\r\n"))
	}
	term.emu.Write([]byte("last\r\n"))
	end := term.emu.ScrollbackLen() + term.emu.CursorPosition().Y
	got := strings.Split(term.loginText(end), "\n")
	if len(got) != keptLines || got[len(got)-1] != "last" {
		t.Errorf("%d lines, last %q", len(got), got[len(got)-1])
	}
}

// TestWriteHistoryBanner: the restored history goes into the scrollback,
// the login output on screen above the prompt, once.
func TestWriteHistoryBanner(t *testing.T) {
	term := markedTermC(t)
	term.pending = &paneState{Text: "old"}
	term.banner = "Welcome\nmotd"
	term.writeHistory()
	sb := term.emu.ScrollbackLen()
	l := func(i int) string { return strings.TrimRight(term.line(i), " ") }
	if l(sb) != "Welcome" || l(sb+1) != "motd" || term.pending != nil || term.banner != "" {
		t.Errorf("screen %q %q, pending %v, banner %q", l(sb), l(sb+1), term.pending, term.banner)
	}
	if !strings.Contains(term.line(0), "old") {
		t.Errorf("the history isn't above: %q", term.line(0))
	}
	term.writeHistory() // nothing left: nothing written
	if term.emu.ScrollbackLen() != sb || term.emu.CursorPosition().Y != 2 {
		t.Error("a second writeHistory wrote again")
	}
}

// TestLoginKeptLive: a shell that prints a motd, then wipes the screen
// when it reads the injection, shows the motd again above its prompt.
func TestLoginKeptLive(t *testing.T) {
	needSh(t)
	sh := `printf 'Welcome to web1\n$ '; read l; printf '\033[H\033[2J\033[3J\033]6974\007$ '; sleep 5`
	term, err := startCmd("web1", exec.Command("sh", "-c", sh), 60, 10, func() {}, paneState{keepLogin: true})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	shown := func() string {
		term.mu.Lock()
		defer term.mu.Unlock()
		sb := term.emu.ScrollbackLen()
		return strings.TrimRight(term.line(sb), " ") + "|" + strings.TrimRight(term.line(sb+1), " ")
	}
	for end := time.Now().Add(testwait.Long); time.Now().Before(end) && shown() != "Welcome to web1|$"; time.Sleep(20 * time.Millisecond) {
	}
	if got := shown(); got != "Welcome to web1|$" {
		t.Fatalf("screen %q", got)
	}
}

// TestDialKeepsLogin: a pane's terminal takes its org's clear_login.
func TestDialKeepsLogin(t *testing.T) {
	needSh(t)
	f, tr := false, true
	for _, clear := range []*bool{nil, &tr, &f} {
		tab := &Tab{org: "Acme", orgCfg: &orgConfig{ClearLogin: clear, Record: &f}}
		term := (&model{}).dial(tab, "local", t.TempDir(), "local", 40, 6, paneState{})
		term.mu.Lock()
		got := term.keepLogin
		term.mu.Unlock()
		term.Close()
		if want := clear == &f; got != want {
			t.Errorf("clear_login %v: keepLogin %v", clear, got)
		}
	}
}
