package telemetry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"

	"github.com/lfdominguez/desk/internal/tgfmt"
)

// What the switches beyond crashes send. Every text goes through tgfmt.Redact
// first, and none carries a card's words, a prompt or a reply.

var (
	mu       sync.Mutex
	seen     = map[string]time.Time{} // Report's last send of each kind and message
	crumbs   []sentry.Breadcrumb      // the run's last crumbMax, also kept in crumbFile
	crumbLog string                   // crumbFile: set by Started
	turns    = map[string]*turn{}     // open card turns, by card id
)

// again is how long Report keeps quiet about one failure: a poll that fails
// every 3 minutes is one issue, not 20 an hour.
const again = time.Hour

// crumbMax is how many breadcrumbs a crash brings.
const crumbMax = 50

// Report sends err as an error of kind (Errors): a failure desk only showed
// as a toast, or dropped.
func Report(kind string, err error) {
	if !on() || !cfg.Errors || err == nil {
		return
	}
	msg := tgfmt.Redact(err.Error())
	mu.Lock()
	last, ok := seen[kind+"\x00"+msg]
	fresh := !ok || time.Since(last) > again
	if fresh {
		seen[kind+"\x00"+msg] = time.Now()
	}
	mu.Unlock()
	Count("desk.errors", 1, "kind", kind)
	log(sentry.LevelError, kind, msg)
	if !fresh {
		return
	}
	ev := sentry.NewEvent()
	ev.Level, ev.Message = sentry.LevelError, kind+": "+msg
	ev.Tags = map[string]string{"kind": kind}
	ev.Fingerprint = []string{"report", kind, msg}
	sentry.CaptureEvent(ev)
}

// Stall sends a window frozen for d doing what (the frame's step: drawing,
// an event) as a warning (Stalls), every goroutine's stack attached.
func Stall(window, what string, d time.Duration, stacks string) {
	if !on() || !cfg.Stalls {
		return
	}
	Count("desk.stalls", 1, "window", window)
	Measure("desk.stall.duration", d.Seconds(), "second", "what", what)
	ev := sentry.NewEvent()
	ev.Level, ev.Message = sentry.LevelWarning, "window froze: "+what
	ev.Tags = map[string]string{"window": window, "what": what}
	ev.Contexts = map[string]sentry.Context{"stall": {"seconds": d.Seconds()}}
	ev.Fingerprint = []string{"stall", what}
	ev.Attachments = []*sentry.Attachment{{Filename: "stacks.txt", ContentType: "text/plain", Payload: []byte(stacks)}}
	sentry.CaptureEvent(ev)
}

// Crumb notes a user action (Breadcrumbs): what led up to an error now, or
// to a crash at the next start (crumbFile keeps them past the crash).
func Crumb(category, msg string) {
	if !on() || !cfg.Breadcrumbs {
		return
	}
	b := sentry.Breadcrumb{Category: category, Message: tgfmt.Redact(msg), Timestamp: time.Now()}
	sentry.AddBreadcrumb(&b)
	mu.Lock()
	crumbs = append(crumbs, b)
	if len(crumbs) > crumbMax {
		crumbs = crumbs[len(crumbs)-crumbMax:]
	}
	data, _ := json.Marshal(crumbs)
	path := crumbLog
	mu.Unlock()
	if path != "" {
		os.WriteFile(path, data, 0o600)
	}
}

// crumbFile is the breadcrumbs file beside crash log.
func crumbFile(log string) string { return filepath.Join(filepath.Dir(log), "crumbs.json") }

// lastCrumbs is the last run's breadcrumbs, read once at start, the file then
// gone: they go with that run's crashes.
func lastCrumbs(log string) []*sentry.Breadcrumb {
	data, err := os.ReadFile(crumbFile(log))
	os.Remove(crumbFile(log))
	var bs []*sentry.Breadcrumb
	if err != nil || json.Unmarshal(data, &bs) != nil {
		return nil
	}
	return bs
}

// log sends msg to Sentry's logs (Logs), kind as its attribute.
func log(level sentry.Level, kind, msg string) {
	if !on() || !cfg.Logs {
		return
	}
	l := sentry.NewLogger(context.Background())
	e := l.Info()
	switch level {
	case sentry.LevelWarning:
		e = l.Warn()
	case sentry.LevelError:
		e = l.Error()
	}
	e.String("kind", kind).Emit(tgfmt.Redact(msg))
}

// Log sends a line of desk's own (Logs): what it would print with a terminal.
func Log(kind, msg string) { log(sentry.LevelInfo, kind, msg) }

// Count adds n to the counter name (Metrics), attrs as key, value pairs.
func Count(name string, n int64, attrs ...string) {
	if on() && cfg.Metrics {
		sentry.NewMeter(context.Background()).Count(name, n, sentry.WithAttributes(pairs(attrs)...))
	}
}

// Measure records v (in unit) for the distribution name (Metrics).
func Measure(name string, v float64, unit string, attrs ...string) {
	if on() && cfg.Metrics {
		sentry.NewMeter(context.Background()).Distribution(name, v, sentry.WithUnit(unit), sentry.WithAttributes(pairs(attrs)...))
	}
}

func pairs(kv []string) []attribute.Builder {
	var out []attribute.Builder
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, attribute.String(kv[i], kv[i+1]))
	}
	return out
}

// turn is a card's Claude turn in the making: when it began, its tools.
type turn struct {
	start time.Time
	tools []*tool
}

type tool struct {
	id, name   string
	start, end time.Time
	failed     bool
}

// Usage is what a turn's result says it took.
type Usage struct {
	Model                        string
	In, Out, CacheRead, CacheNew int
	Cost                         float64 // USD, this turn's
	Failed                       bool
}

// TurnStart notes card id's Claude starting a turn (Agents, Metrics).
func TurnStart(id string) {
	if !on() || !(cfg.Agents || cfg.Metrics) {
		return
	}
	mu.Lock()
	turns[id] = &turn{start: time.Now()}
	mu.Unlock()
}

// ToolStart and ToolEnd note a tool the turn ran, by its use id.
func ToolStart(id, use, name string) {
	mu.Lock()
	defer mu.Unlock()
	if t := turns[id]; t != nil {
		t.tools = append(t.tools, &tool{id: use, name: name, start: time.Now()})
	}
}

func ToolEnd(id, use string, failed bool) {
	mu.Lock()
	defer mu.Unlock()
	if t := turns[id]; t != nil {
		for _, tl := range t.tools {
			if tl.id == use && tl.end.IsZero() {
				tl.end, tl.failed = time.Now(), failed
			}
		}
	}
}

// TurnEnd sends the turn as Sentry's AI agent span with a span per tool
// (Agents) and its duration, tokens and cost as metrics (Metrics): numbers
// and tool names only.
func TurnEnd(id string, u Usage) {
	mu.Lock()
	t := turns[id]
	delete(turns, id)
	mu.Unlock()
	if t == nil {
		return
	}
	now := time.Now()
	Measure("desk.turn.duration", now.Sub(t.start).Seconds(), "second", "model", u.Model)
	Measure("desk.turn.tokens", float64(u.In+u.Out+u.CacheRead+u.CacheNew), "none", "model", u.Model)
	Measure("desk.turn.cost", u.Cost, "none", "model", u.Model)
	if !cfg.Agents {
		return
	}
	tx := sentry.StartTransaction(context.Background(), "invoke_agent card", sentry.WithOpName("gen_ai.invoke_agent"))
	tx.StartTime = t.start
	for k, v := range map[string]any{
		"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "card", "gen_ai.system": "anthropic",
		"gen_ai.request.model": u.Model, "gen_ai.response.model": u.Model,
		"gen_ai.usage.input_tokens": u.In + u.CacheRead + u.CacheNew, "gen_ai.usage.input_tokens.cached": u.CacheRead,
		"gen_ai.usage.output_tokens": u.Out, "gen_ai.usage.total_tokens": u.In + u.Out + u.CacheRead + u.CacheNew,
		"gen_ai.usage.cost": u.Cost,
	} {
		tx.SetData(k, v)
	}
	if u.Failed {
		tx.Status = sentry.SpanStatusInternalError
	}
	for _, tl := range t.tools {
		s := tx.StartChild("gen_ai.execute_tool", sentry.WithDescription("execute_tool "+tl.name))
		s.StartTime, s.EndTime = tl.start, tl.end
		if tl.end.IsZero() {
			s.EndTime = now
		}
		s.SetData("gen_ai.operation.name", "execute_tool")
		s.SetData("gen_ai.tool.name", tl.name)
		if tl.failed {
			s.Status = sentry.SpanStatusInternalError
		}
		s.Finish()
	}
	tx.EndTime = now
	tx.Finish()
}
