package telemetry

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

// startAll starts Sentry with every switch on, its events kept in the
// returned transport.
func startAll(t *testing.T, c Config) *sent {
	t.Helper()
	tr := &sent{}
	c.DSN = "https://key@o1.ingest.sentry.io/1"
	if err := Start(c, "2026.11.26", func(o *sentry.ClientOptions) { o.Transport = tr }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sentry.CurrentHub().BindClient(nil)
		started, cfg = false, Config{}
		mu.Lock()
		seen, crumbs, crumbLog, turns = map[string]time.Time{}, nil, "", map[string]*turn{}
		mu.Unlock()
	})
	return tr
}

var all = Config{Errors: true, Stalls: true, Breadcrumbs: true, Logs: true, Metrics: true, Agents: true}

func kinds(tr *sent) (out []string) {
	for _, e := range tr.events {
		out = append(out, cmpOr(e.Type, "event")+":"+e.Message)
	}
	return out
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// TestSwitchesOff: with a DSN but no switch, only crashes go: nothing here sends.
func TestSwitchesOff(t *testing.T) {
	tr := startAll(t, Config{})
	Report("link", errors.New("x"))
	Stall("main", "drawing", 4*time.Second, "goroutine 1")
	Crumb("view", "board")
	Log("k", "m")
	Count("c", 1)
	Measure("m", 1, "second")
	TurnStart("c1")
	ToolStart("c1", "t1", "Bash")
	ToolEnd("c1", "t1", false)
	TurnEnd("c1", Usage{Model: "m"})
	Stop()
	if len(tr.events) != 0 {
		t.Fatalf("sent with every switch off: %q", kinds(tr))
	}
	if sentry.CurrentHub().Client().Options().EnableTracing {
		t.Fatal("tracing on without Agents")
	}
}

// TestReport: a failure goes once an hour per kind and message, its secrets
// masked; nil and a stopped Sentry send nothing.
func TestReport(t *testing.T) {
	tr := startAll(t, Config{Errors: true})
	Report("link", nil)
	Report("ticket move", errors.New("400 Bad Request token=hunter2secret"))
	Report("ticket move", errors.New("400 Bad Request token=hunter2secret"))
	Report("ticket move", errors.New("422 transition"))
	Report("link jira", errors.New("globex.atlassian.net: 401 Unauthorized")) // the user's account: not desk's
	if len(tr.events) != 2 || strings.Contains(tr.events[0].Message, "hunter2") || tr.events[0].Tags["kind"] != "ticket move" {
		t.Fatalf("events %q", kinds(tr))
	}
	mu.Lock()
	for k := range seen {
		seen[k] = time.Now().Add(-2 * again)
	}
	mu.Unlock()
	Report("ticket move", errors.New("422 transition"))
	if len(tr.events) != 3 {
		t.Fatalf("an hour later: %q", kinds(tr))
	}
}

// TestStall: a freeze goes as a warning by what it was doing, stacks attached.
func TestStall(t *testing.T) {
	tr := startAll(t, Config{Stalls: true})
	Stall("main", "drawing the frame", 4*time.Second, "goroutine 1 [running]:")
	e := tr.events[0]
	if e.Level != sentry.LevelWarning || e.Tags["what"] != "drawing the frame" || e.Attachments[0].Filename != "stacks.txt" {
		t.Fatalf("stall %+v", e)
	}
}

// TestCrumbs: actions go with an error now and, kept on disk, with the
// next start's report of a crash; the file is read once.
func TestCrumbs(t *testing.T) {
	tr := startAll(t, Config{Breadcrumbs: true, Errors: true})
	dir := t.TempDir()
	log := filepath.Join(dir, "crash.log")
	Crumb("before", "no file yet")
	Started(log, "2026.11.26")
	for i := range crumbMax + 5 {
		Crumb("view", "sess:"+strings.Repeat("x", i%3))
	}
	if b, _ := os.ReadFile(crumbFile(log)); !strings.Contains(string(b), "sess:") {
		t.Fatalf("crumbs file %q", b)
	}
	Report("ssh", errors.New("refused"))
	if n := len(tr.events[0].Breadcrumbs); n == 0 {
		t.Fatal("an error without its breadcrumbs")
	}
	os.WriteFile(log, []byte("2026-11-19T09:00:00Z desk 2026.11.26 started\n"+trace+"\n"), 0o600)
	os.WriteFile(log+".reported", []byte("0"), 0o600)
	sentry.CurrentHub().Scope().ClearBreadcrumbs() // a new run's: only the file's go with its crash
	Crashes(log)
	if bs := tr.events[len(tr.events)-1].Breadcrumbs; len(bs) != crumbMax || bs[0].Category != "view" {
		t.Fatalf("crash breadcrumbs %d", len(bs))
	}
	if _, err := os.Stat(crumbFile(log)); err == nil {
		t.Fatal("crumbs file kept after the crash went")
	}
	os.WriteFile(crumbFile(log), []byte("not json"), 0o600)
	if lastCrumbs(log) != nil {
		t.Fatal("a broken crumbs file")
	}
}

// TestLogsMetrics: logs at each level and metrics only send behind their switches.
func TestLogsMetrics(t *testing.T) {
	startAll(t, Config{Logs: true, Metrics: true, Errors: true, Stalls: true})
	Log("start", "desk started")
	log(sentry.LevelWarning, "k", "careful")
	Error("window", "lost")
	Report("telegram poll", errors.New("timeout"))
	Stall("main", "event", time.Second, "")
	Count("desk.x", 2, "a", "b", "odd")
	Measure("desk.y", 1.5, "second")
	TurnStart("c1") // Metrics without Agents: numbers, no span
	TurnEnd("c1", Usage{Model: "m", Out: 1})
	if got := pairs([]string{"a", "b", "odd"}); len(got) != 1 {
		t.Fatalf("pairs %d", len(got))
	}
	Stop()
}

// TestTurn: a card turn becomes an AI agent transaction, a span per tool
// (one failed, one never ended), numbers only; a turn never started sends nothing.
func TestTurn(t *testing.T) {
	tr := startAll(t, all)
	if o := sentry.CurrentHub().Client().Options(); !o.EnableTracing || o.TracesSampleRate != 1 {
		t.Fatal("tracing off with Agents")
	}
	TurnEnd("never", Usage{})
	ToolStart("never", "t0", "Read")
	ToolEnd("never", "t0", false)
	TurnStart("c1")
	ToolStart("c1", "t1", "Bash")
	ToolEnd("c1", "t1", true)
	ToolEnd("c1", "other", false)
	ToolStart("c1", "t2", "Read")
	TurnEnd("c1", Usage{Model: "claude-opus", In: 3, Out: 50, CacheRead: 400, CacheNew: 10, Cost: 0.25, Failed: true})
	TurnStart("c2")
	TurnEnd("c2", Usage{Model: "claude-opus"})
	Stop()
	var tx []*sentry.Event
	for _, e := range tr.events {
		if e.Type == "transaction" {
			tx = append(tx, e)
		}
	}
	if len(tx) != 2 {
		t.Fatalf("transactions %q", kinds(tr))
	}
	e := tx[0]
	if e.Contexts["trace"]["op"] != "gen_ai.invoke_agent" || len(e.Spans) != 2 {
		t.Fatalf("transaction %+v", e.Contexts["trace"])
	}
	if d := e.Contexts["trace"]["data"].(map[string]any); d["gen_ai.usage.output_tokens"] != 50 || d["gen_ai.request.model"] != "claude-opus" {
		t.Fatalf("data %v", d)
	}
	if s := e.Spans[0]; s.Data["gen_ai.tool.name"] != "Bash" || s.Status != sentry.SpanStatusInternalError {
		t.Fatalf("tool span %+v", s)
	}
	if s := e.Spans[1]; s.EndTime.IsZero() {
		t.Fatal("an unended tool without an end")
	}
}

// TestExpected: failures of the user's setup, a server or the network aren't
// desk's bugs; a bad request, an unreadable reply or a crashed Claude are.
func TestExpected(t *testing.T) {
	for msg, want := range map[string]bool{
		"globex.atlassian.net: 401 Unauthorized":               true,
		"403 Forbidden":                                        true,
		"GET /issue/ABC-1: 404 Not Found":                      true,
		"429 Too Many Requests":                                true,
		"502 Bad Gateway":                                      true,
		"dial tcp: lookup jira.example.com: no such host":      true,
		"dial tcp 192.0.2.1:22: connect: connection refused":   true,
		"ssh: handshake failed: ssh: unable to authenticate":   true,
		"x509: certificate signed by unknown authority":        true,
		"unexpected EOF":                                       true,
		"Bad Request: can't parse entities: unclosed tag":      false,
		"422 Unprocessable Entity: transition 31 is not valid": false,
		"invalid character '<' looking for beginning of value": false,
		"claude exited: signal: segmentation fault":            false,
	} {
		if got := expected(errors.New(msg)); got != want {
			t.Errorf("expected(%q) = %v, want %v", msg, got, want)
		}
	}
	if !expected(&net.DNSError{Err: "x", Name: "h"}) || !expected(context.DeadlineExceeded) {
		t.Error("a net.Error or a deadline is expected")
	}
}
