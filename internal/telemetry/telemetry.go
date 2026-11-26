// Package telemetry reports desk's own crashes and errors to Sentry, once the
// user gives a DSN (settings.toml's [sentry], or SENTRY_DSN). Without one the
// SDK has no client and every call here does nothing. Crashes and errors
// only: no tracing (a desktop app serves no requests), no user data.
package telemetry

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
)

// Config is what desk reports: crashes with a DSN, the rest each by its switch.
type Config struct {
	DSN                                                string
	Errors, Stalls, Breadcrumbs, Logs, Metrics, Agents bool
}

// cfg is Start's Config: the switches every call here checks.
var cfg Config

// Start sets Sentry up for c's DSN, labelled with version (opts: tests'
// transport). No DSN, no client.
func Start(c Config, version string, opts ...func(*sentry.ClientOptions)) error {
	c.DSN = cmp.Or(c.DSN, os.Getenv("SENTRY_DSN"))
	if c.DSN == "" {
		return nil
	}
	o := sentry.ClientOptions{Dsn: c.DSN, Release: release(version), Environment: "production",
		ServerName: "desk"} // not the machine's hostname, which events and metrics carry by default
	if strings.HasPrefix(version, "dev") {
		o.Environment = "development"
	}
	if c.Agents { // every card turn is a transaction: few, and each one counts
		o.EnableTracing, o.TracesSampleRate = true, 1
	}
	for _, f := range opts {
		f(&o)
	}
	if err := sentry.Init(o); err != nil {
		return err
	}
	started, cfg = true, c
	return nil
}

// release is version as Sentry's release name; "" leaves the client's.
func release(version string) string {
	if version == "" {
		return ""
	}
	return "desk@" + version
}

// started says Start set a client up: the hub always has one, a no-op without a DSN.
var started bool

func on() bool { return started }

// Stop sends what's waiting, for up to 5 s, before desk exits.
func Stop() {
	if on() {
		sentry.Flush(5 * time.Second)
	}
}

// Error reports msg as an error of class: what crash.log notes as it runs.
func Error(class, msg string) {
	log(sentry.LevelError, class, msg)
	ev := sentry.NewEvent()
	ev.Level, ev.Message = sentry.LevelError, msg
	ev.Tags = map[string]string{"class": class}
	sentry.CaptureEvent(ev)
}

// Started notes in log that desk version started, so a crash after it is
// reported with the version that crashed, not the one reporting it.
func Started(log, version string) {
	mu.Lock()
	crumbLog, crumbs = crumbFile(log), nil
	mu.Unlock()
	if f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintf(f, "%s desk %s started\n", time.Now().Format(time.RFC3339), version)
		f.Close()
	}
}

// Crashes reports the crashes the runtime wrote to log (debug.SetCrashOutput)
// since the last call, kept in log+".reported": a crash ends the process
// before anything can be sent, so it goes at the next start. The first call
// ever starts at the log's end, leaving older history out.
func Crashes(log string) {
	if !on() {
		return
	}
	data, err := os.ReadFile(log)
	if err != nil {
		return
	}
	mark := log + ".reported"
	before := lastCrumbs(log) // the last run's: its crashes' lead-up
	for _, c := range crashes(string(data[since(mark, len(data)):])) {
		ev := crashEvent(c)
		ev.Breadcrumbs = before
		sentry.CaptureEvent(ev)
	}
	os.WriteFile(mark, []byte(strconv.Itoa(len(data))), 0o600)
}

// crashEvent is crash c as Sentry's fatal exception: "panic" and its text,
// the crashing goroutine's frames, released as the version that crashed.
func crashEvent(c crash) *sentry.Event {
	head, _, _ := strings.Cut(c.trace, "\n")
	kind, value, ok := strings.Cut(head, ": ")
	if !ok {
		kind, value = "crash", head
	}
	ev := sentry.NewEvent()
	ev.Level, ev.Release = sentry.LevelFatal, release(c.version)
	ev.Exception = []sentry.Exception{{Type: kind, Value: value, Stacktrace: frames(c.trace)}}
	ev.Tags = map[string]string{"where": where(c.trace)}
	ev.Attachments = []*sentry.Attachment{{Filename: "crash.txt", ContentType: "text/plain", Payload: []byte(c.trace)}} // every goroutine, as the runtime wrote it
	return ev
}

// since is where in a log of size n the crashes not reported yet start: the
// offset in mark, n without one, 0 when the log is shorter (cleared since).
func since(mark string, n int) int {
	b, err := os.ReadFile(mark)
	if err != nil {
		return n
	}
	off, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || off > n {
		return 0
	}
	return off
}

var (
	crashStart = regexp.MustCompile(`^(panic: |fatal error: |SIG[A-Z]+: )`) // SIG…: a signal in C code (cgo: the GPU driver)
	noteLine   = regexp.MustCompile(`^\d{4}-\d\d-\d\dT`)                     // a crashNote line, reported as it was written
	startLine  = regexp.MustCompile(`^\S+ desk (\S+) started$`)
)

type crash struct{ trace, version string }

// crashes splits the runtime's traces out of crash.log's text: each from a
// "panic:", "fatal error:" or "SIG…:" line to the next such line or
// crashNote line, with the version the last Started line before it named.
func crashes(text string) []crash {
	var out []crash
	var cur []string
	version := ""
	end := func() {
		if cur != nil {
			out = append(out, crash{strings.TrimRight(strings.Join(cur, "\n"), "\n"), version})
		}
		cur = nil
	}
	for l := range strings.SplitSeq(text, "\n") {
		switch {
		case crashStart.MatchString(l):
			end()
			cur = []string{l}
		case noteLine.MatchString(l):
			end()
			if m := startLine.FindStringSubmatch(l); m != nil {
				version = m[1]
			}
		case cur != nil:
			cur = append(cur, l)
		}
	}
	end()
	return out
}

// frames is the crashing goroutine's stack in trace, caller first as Sentry
// lists it; nil without one (a signal in C code has no Go frames).
func frames(trace string) *sentry.Stacktrace {
	var fs []sentry.Frame
	lines := strings.Split(trace, "\n")
	i := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "goroutine ") })
	for j := i + 1; i >= 0 && j+1 < len(lines) && lines[j] != ""; j += 2 { // a function line, then its "\tfile:line +0x…"
		fn := lines[j]
		if k := strings.LastIndex(fn, "("); k > 0 {
			fn = fn[:k]
		}
		file, _, _ := strings.Cut(strings.TrimSpace(lines[j+1]), " +")
		path, line, _ := strings.Cut(file, ":")
		n, _ := strconv.Atoi(line)
		mod := fn[:strings.LastIndex(fn, "/")+1] + strings.Split(fn[strings.LastIndex(fn, "/")+1:], ".")[0]
		fs = append(fs, sentry.Frame{Function: fn, Module: mod, AbsPath: path, Filename: filepath.Base(path), Lineno: n,
			InApp: !strings.HasPrefix(fn, "runtime.") && !strings.HasPrefix(fn, "panic")})
	}
	if fs == nil {
		return nil
	}
	slices.Reverse(fs)
	return &sentry.Stacktrace{Frames: fs}
}

// where is the crashing goroutine's first frame outside the runtime, as
// "ui.(*ui).frame (app.go:470)": where in desk it went wrong.
func where(trace string) string {
	lines := strings.Split(trace, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, "goroutine ") {
			continue
		}
		for j := i + 1; j+1 < len(lines) && lines[j] != ""; j += 2 { // a function line, then its "\tfile:line +0x…"
			fn := lines[j]
			if strings.HasPrefix(fn, "runtime.") || strings.HasPrefix(fn, "panic(") {
				continue
			}
			if k := strings.LastIndex(fn, "("); k > 0 {
				fn = fn[:k]
			}
			file, _, _ := strings.Cut(strings.TrimSpace(lines[j+1]), " +")
			return fn[strings.LastIndex(fn, "/")+1:] + " (" + filepath.Base(file) + ")"
		}
		return ""
	}
	return ""
}
