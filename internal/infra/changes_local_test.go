package infra

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestChangesLocal: under localGo the local host's changes tool runs in Go:
// dpkg's logs (gzipped too) since the hours asked, files changed under etc,
// and each tool on PATH run directly, its output cut as the script cuts it.
func TestChangesLocal(t *testing.T) {
	logs, etc, bin := t.TempDir(), t.TempDir(), t.TempDir()
	oldL, oldE, oldGo := changesLogs, changesEtc, localGo
	changesLogs, changesEtc, localGo = logs, etc, true
	defer func() { changesLogs, changesEtc, localGo = oldL, oldE, oldGo }()
	now := time.Now() // one clock reading: the log lines and what's wanted of them must agree
	at := func(d time.Duration) string { return now.Add(-d).Format("2006-01-02 15:04:05") }
	write(t, filepath.Join(logs, "dpkg.log"), at(30*time.Minute)+" install foo:amd64 <none> 1.0\n"+
		at(30*time.Minute)+" status installed foo:amd64 1.0\n"+at(3*time.Hour)+" upgrade bar 1 2\n"+at(10*time.Minute)+" remove baz 1 <none>\n"+at(5*time.Minute)+" upgrade new 1 2\nshort line\n")
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(at(20*time.Minute) + " purge qux 1 <none>\n"))
	zw.Close()
	write(t, filepath.Join(logs, "dpkg.log.2.gz"), gz.String())
	write(t, filepath.Join(logs, "dpkg.log.3.gz"), "not gzip")
	os.Symlink(filepath.Join(logs, "gone"), filepath.Join(logs, "dpkg.log.4")) // can't open: skipped
	write(t, filepath.Join(etc, "new.conf"), "x")
	write(t, filepath.Join(etc, "sub", "inner.conf"), "x")
	write(t, filepath.Join(etc, "old.conf"), "x")
	long := time.Now().Add(-3 * time.Hour)
	os.Chtimes(filepath.Join(etc, "old.conf"), long, long)
	path := os.Getenv("PATH")
	t.Setenv("PATH", bin)
	tools := runtime.GOOS != "windows" // the fakes are sh scripts
	if tools {
		for name, body := range map[string]string{
			"rpm":        `echo "pkg-1 $*"`,
			"journalctl": `[ "$2" = "1 hours ago" ] && echo "2026-11-07T10:00:00+0000 h systemd[1]: Started a.service." && echo "2026-11-07T10:05:00+0000 h systemd[1]: Reached x."`,
			"last":       `if [ "$1" = -x ]; then n=6; else n=16; fi; i=0; while [ $i -lt $n ]; do echo "$1 $i"; i=$((i+1)); done`,
			"docker":     `echo "web  Up  nginx $4"; exit 1`,
		} {
			write(t, filepath.Join(bin, name), "#!/bin/sh\n"+body+"\n")
			os.Chmod(filepath.Join(bin, name), 0o755)
		}
	}
	got := changesLocal(context.Background(), 1)
	want := "== packages\n" + at(30*time.Minute) + " install foo:amd64 <none> 1.0\n" // sorted: the oldest first
	if !strings.HasPrefix(got, want) || !strings.Contains(got, " purge qux") || !strings.Contains(got, " remove baz") || !strings.Contains(got, " upgrade new") || strings.Contains(got, "upgrade bar") || strings.Contains(got, "status") {
		t.Errorf("packages: %q", got)
	}
	if !strings.Contains(got, "== files changed under "+etc+"\n") || !strings.Contains(got, filepath.Join(etc, "new.conf")) ||
		!strings.Contains(got, filepath.Join(etc, "sub", "inner.conf")) || strings.Contains(got, "old.conf") {
		t.Errorf("files: %q", got)
	}
	if tools {
		for _, s := range []string{
			"pkg-1 -qa --last\n== files",
			"== services started, stopped, failed\n2026-11-07T10:00:00+0000  Started a.service.\n== logins\n-F 0\n",
			"-F 14\n== reboots\n-x 0\n", "-x 4\n== containers\nweb  Up  nginx {{.Names}}  {{.Status}}  {{.Image}}\n",
		} {
			if !strings.Contains(got, s) {
				t.Errorf("lacks %q in %q", s, got)
			}
		}
	}
	// through the tool
	term, _ := fakeTermA(t, "local")
	r := (&Tab{org: "Acme"}).changes(&Pane{term: term, name: "here"}, 1)
	if r.IsError || !strings.HasPrefix(r.Text, "changes on here in the last 1h:\n"+want) {
		t.Errorf("tool: %+v", r)
	}
	if tools { // without localGo the local host runs the script
		localGo = false
		os.Setenv("PATH", path) // sh; t.Setenv puts it back after
		if r := (&Tab{org: "Acme"}).changes(&Pane{term: term, name: "here"}, 1); !strings.Contains(r.Text, "== files changed under /etc\n") {
			t.Errorf("script: %+v", r)
		}
	}
}

// TestServiceEvents: journal lines that start, stop, reload or fail
// something, once each with their last time and a count past one.
func TestServiceEvents(t *testing.T) {
	got := serviceEvents([]string{
		"2026-11-07T10:00:00+0000 h systemd[1]: Started nginx.service.",
		"2026-11-07T10:05:00+0000 h systemd[1]: Reached target x.",
		"2026-11-07T10:06:00+0000 h systemd[1]: foo.service: Failed with result 'exit-code'.",
		"2026-11-07T10:07:00+0000 h systemd[1]: Started nginx.service.",
	})
	want := []string{"2026-11-07T10:07:00+0000  2x Started nginx.service.", "2026-11-07T10:06:00+0000  foo.service: Failed with result 'exit-code'."}
	if !slices.Equal(got, want) {
		t.Errorf("%q", got)
	}
}

// TestNewerFiles: at most n files, and nothing from a folder that isn't.
func TestNewerFiles(t *testing.T) {
	d := t.TempDir()
	write(t, filepath.Join(d, "a"), "")
	write(t, filepath.Join(d, "b"), "")
	if ls := newerFiles(d, time.Now().Add(-time.Hour), 1); len(ls) != 1 {
		t.Errorf("capped: %q", ls)
	}
	if ls := newerFiles(filepath.Join(d, "none"), time.Time{}, 5); ls != nil {
		t.Errorf("missing: %q", ls)
	}
}
