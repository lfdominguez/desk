package telemetry

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

// sent is a transport that keeps the events instead of sending them.
type sent struct{ events []*sentry.Event }

func (s *sent) Flush(time.Duration) bool              { return true }
func (s *sent) FlushWithContext(context.Context) bool { return true }
func (s *sent) Configure(sentry.ClientOptions)        {}
func (s *sent) SendEvent(e *sentry.Event)             { s.events = append(s.events, e) }
func (s *sent) Close()                                {}

const trace = "panic: runtime error: index out of range [3] with length 3\n\n" +
	"goroutine 1 [running]:\n" +
	"panic({0x1, 0x2})\n\t/go/src/runtime/panic.go:787 +0x132\n" +
	"github.com/acme/desk/internal/ui.(*ui).frame(0xc000010000, {0x0})\n\t/src/internal/ui/app.go:470 +0x1d\n" +
	"main.main()\n\t/src/main.go:3 +0x1"

func TestCrashes(t *testing.T) {
	text := "2026-11-19T09:00:00Z desk 2026.11.19.3 started\n" +
		"2026-11-19T10:00:00Z window lost, reopening: x\n" + trace + "\n\n" +
		"2026-11-19T10:30:00Z desk 2026.11.19.4 started\n" +
		"fatal error: concurrent map writes\n\ngoroutine 7:\n" +
		"SIGSEGV: segmentation violation\nPC=0x0 m=0\n" +
		"2026-11-19T11:00:00Z window main back at its events\n"
	got := crashes(text)
	want := []crash{
		{trace, "2026.11.19.3"},
		{"fatal error: concurrent map writes\n\ngoroutine 7:", "2026.11.19.4"},
		{"SIGSEGV: segmentation violation\nPC=0x0 m=0", "2026.11.19.4"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("crashes = %q, want %q", got, want)
	}
	if got := crashes("2026-11-19T10:00:00Z only a note\n"); got != nil {
		t.Fatalf("notes only: %q", got)
	}
}

func TestWhere(t *testing.T) {
	for in, want := range map[string]string{
		trace: "ui.(*ui).frame (app.go:470)",
		"fatal error: x\n\ngoroutine 7 [running]:\nruntime.throw()\n\t/r/panic.go:1 +0x1\nmain.f\n\t/s/m.go:9": "main.f (m.go:9)",
		"panic: x\n\ngoroutine 1 [running]:\nruntime.throw()\n\t/r/panic.go:1 +0x1\n":                         "",
		"panic: x":     "",
		"SIGSEGV: x\n": "",
	} {
		if got := where(in); got != want {
			t.Errorf("where(%q) = %q, want %q", in, got, want)
		}
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

func TestStarted(t *testing.T) {
	log := filepath.Join(t.TempDir(), "crash.log")
	Started(log, "2026.11.20")
	b, _ := os.ReadFile(log)
	if m := startLine.FindStringSubmatch(strings.TrimSpace(string(b))); m == nil || m[1] != "2026.11.20" {
		t.Fatalf("log %q", b)
	}
	Started(filepath.Join(log, "not-a-dir", "x"), "v") // can't write: nothing
}

// TestOff: without a DSN nothing starts and every call is a no-op.
func TestOff(t *testing.T) {
	t.Setenv("SENTRY_DSN", "")
	started = false
	if err := Start("", "v"); err != nil || on() {
		t.Fatalf("Start = %v, on %v", err, on())
	}
	Error("x", "y")
	log := filepath.Join(t.TempDir(), "crash.log")
	os.WriteFile(log, []byte(trace), 0o600)
	Crashes(log)
	if _, err := os.Stat(log + ".reported"); err == nil {
		t.Fatal("marked while off")
	}
	Stop()
}

// TestOn: errors and the log's new crashes reach Sentry as events, a crash
// as the version that crashed; a bad DSN doesn't start.
func TestOn(t *testing.T) {
	if err := Start("not a dsn", "v"); err == nil {
		t.Fatal("a malformed DSN started")
	}
	tr := &sent{}
	if err := Start("https://key@o1.ingest.sentry.io/1", "dev-1a2b3c4", func(o *sentry.ClientOptions) { o.Transport = tr }); err != nil || !on() {
		t.Fatalf("Start = %v", err)
	}
	t.Cleanup(func() { sentry.CurrentHub().BindClient(nil); started = false })
	if o := sentry.CurrentHub().Client().Options(); o.Environment != "development" || o.Release != "desk@dev-1a2b3c4" {
		t.Fatalf("options %q %q", o.Environment, o.Release)
	}
	Error("window", "lost")
	if len(tr.events) != 1 || tr.events[0].Message != "lost" || tr.events[0].Tags["class"] != "window" || tr.events[0].Level != sentry.LevelError {
		t.Fatalf("error events %+v", tr.events)
	}

	log := filepath.Join(t.TempDir(), "crash.log")
	Crashes(log) // no log yet: nothing, no mark
	if _, err := os.Stat(log + ".reported"); err == nil {
		t.Fatal("marked a missing log")
	}
	os.WriteFile(log, []byte("old\n"), 0o600)
	Crashes(log) // first run: starts at the end
	if got := since(log+".reported", 100); got != 4 {
		t.Fatalf("first run's mark %d, want 4", got)
	}
	data := "old\n2026-11-19T09:00:00Z desk 2026.11.19.3 started\n" + trace + "\n"
	os.WriteFile(log, []byte(data), 0o600)
	Crashes(log)
	Stop()
	if got := since(log+".reported", len(data)); got != len(data) {
		t.Fatalf("mark %d, want %d", got, len(data))
	}
	ev := tr.events[len(tr.events)-1]
	if len(tr.events) != 2 || ev.Level != sentry.LevelFatal || ev.Release != "desk@2026.11.19.3" || ev.Tags["where"] != "ui.(*ui).frame (app.go:470)" {
		t.Fatalf("crash event %+v", ev)
	}
}

// TestCrashEvent: the panic's kind and text, the crashing goroutine's frames
// caller first (desk's in the app, the runtime's not), none for a signal in C.
func TestCrashEvent(t *testing.T) {
	ex := crashEvent(crash{trace, ""}).Exception[0]
	if ex.Type != "panic" || ex.Value != "runtime error: index out of range [3] with length 3" {
		t.Fatalf("exception %q %q", ex.Type, ex.Value)
	}
	fs := ex.Stacktrace.Frames
	if len(fs) != 3 || fs[0].Function != "main.main" || fs[1].Module != "github.com/acme/desk/internal/ui" ||
		fs[1].Function != "github.com/acme/desk/internal/ui.(*ui).frame" || fs[1].Filename != "app.go" || fs[1].Lineno != 470 ||
		!fs[1].InApp || fs[2].InApp {
		t.Fatalf("frames %+v", fs)
	}
	e := crashEvent(crash{"SIGSEGV\nPC=0x0", "v"})
	if e.Exception[0].Type != "crash" || e.Exception[0].Value != "SIGSEGV" || e.Exception[0].Stacktrace != nil || e.Release != "desk@v" {
		t.Fatalf("signal %+v", e.Exception[0])
	}
}
