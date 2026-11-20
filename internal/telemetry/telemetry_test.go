package telemetry

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/newrelic/go-agent/v3/newrelic"
)

const trace = "panic: boom\n\ngoroutine 1 [running]:\nmain.main()\n\t/src/main.go:3 +0x1"

func TestCrashes(t *testing.T) {
	text := "2026-11-19T10:00:00Z window lost, reopening: x\n" + trace + "\n\n" +
		"fatal error: concurrent map writes\n\ngoroutine 7:\n" +
		"2026-11-19T11:00:00Z window main back at its events\n"
	got := crashes(text)
	want := []string{trace, "fatal error: concurrent map writes\n\ngoroutine 7:"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("crashes = %q, want %q", got, want)
	}
	if got := crashes("2026-11-19T10:00:00Z only a note\n"); got != nil {
		t.Fatalf("notes only: %q", got)
	}
}

func TestSince(t *testing.T) {
	mark := filepath.Join(t.TempDir(), "crash.log.reported")
	if got := since(mark, 50); got != 50 {
		t.Fatalf("no mark: %d, want the end", got)
	}
	os.WriteFile(mark, []byte("20\n"), 0o600)
	if got := since(mark, 50); got != 20 {
		t.Fatalf("mark: %d, want 20", got)
	}
	if got := since(mark, 10); got != 0 {
		t.Fatalf("log cleared: %d, want 0", got)
	}
	os.WriteFile(mark, []byte("x"), 0o600)
	if got := since(mark, 10); got != 0 {
		t.Fatalf("bad mark: %d, want 0", got)
	}
}

// TestOff: without a license nothing starts and every call is a no-op.
func TestOff(t *testing.T) {
	t.Setenv("NEW_RELIC_LICENSE_KEY", "")
	if err := Start("", "", "v"); err != nil || app != nil {
		t.Fatalf("Start = %v, app %v", err, app)
	}
	Error("x", "y")
	Track("t")(errors.New("e"))
	Frame(time.Millisecond)
	Crashes(filepath.Join(t.TempDir(), "crash.log"))
	Stop()
}

// TestOn runs every call against an agent that never connects.
func TestOn(t *testing.T) {
	if err := Start("bad key", "", "v"); err == nil {
		t.Fatal("a malformed license started")
	}
	key := "0123456789012345678901234567890123456789"
	if err := Start(key, "desk-test", "v", newrelic.ConfigEnabled(false)); err != nil || app == nil {
		t.Fatalf("Start = %v", err)
	}
	t.Cleanup(func() { Stop(); app = nil })
	Error("x", "y")
	Track("t")(errors.New("e"))
	Track("t")(nil)
	Frame(time.Millisecond)

	dir := t.TempDir()
	log := filepath.Join(dir, "crash.log")
	Crashes(log) // no log yet: nothing, no mark
	if _, err := os.Stat(log + ".reported"); err == nil {
		t.Fatal("marked a missing log")
	}
	os.WriteFile(log, []byte("old\n"), 0o600)
	Crashes(log) // first run: starts at the end
	if got := since(log+".reported", 100); got != 4 {
		t.Fatalf("first run's mark %d, want 4", got)
	}
	data := "old\n" + trace + "\n"
	os.WriteFile(log, []byte(data), 0o600)
	Crashes(log)
	if got := since(log+".reported", 100); got != len(data) {
		t.Fatalf("mark %d, want %d", got, len(data))
	}
}
