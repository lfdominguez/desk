// Package telemetry reports desk's own crashes and errors to New Relic, once
// the user gives a license key (settings.toml's [newrelic], or
// NEW_RELIC_LICENSE_KEY). Without one app stays nil and every call here does
// nothing: the agent's methods are nil-safe. Crashes only: no app metrics.
package telemetry

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/newrelic/go-agent/v3/newrelic"
)

var app *newrelic.Application

// Start connects to New Relic in the background as name (default "desk"),
// labelled with version. No license, no app.
func Start(license, name, version string, opts ...newrelic.ConfigOption) error {
	license = cmp.Or(license, os.Getenv("NEW_RELIC_LICENSE_KEY"))
	if license == "" {
		return nil
	}
	a, err := newrelic.NewApplication(append([]newrelic.ConfigOption{
		newrelic.ConfigAppName(cmp.Or(name, "desk")),
		newrelic.ConfigLicense(license),
		newrelic.ConfigAppLogForwardingEnabled(true), // a crash's whole trace goes as a log
		func(c *newrelic.Config) {
			c.Labels = map[string]string{"version": version}
			c.RuntimeSampler.Enabled = false // the Go runtime's metrics: not wanted
		},
	}, opts...)...)
	if err != nil {
		return err
	}
	app = a
	return nil
}

// Stop sends what's waiting, for up to 5 s, before desk exits.
func Stop() {
	if app != nil {
		app.Shutdown(5 * time.Second)
	}
}

// Error reports msg as an error of class: what crash.log notes as it runs.
func Error(class, msg string) { notice(class, msg, nil) }

func notice(class, msg string, attrs map[string]any) {
	t := app.StartTransaction("desk/" + class)
	t.NoticeError(newrelic.Error{Message: msg, Class: class, Attributes: attrs})
	t.End()
}

// Started notes in log that desk version started, so a crash after it is
// reported with the version that crashed, not the one reporting it.
func Started(log, version string) {
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
	if app == nil {
		return
	}
	data, err := os.ReadFile(log)
	if err != nil {
		return
	}
	mark := log + ".reported"
	for _, c := range crashes(string(data[since(mark, len(data)):])) {
		head, _, _ := strings.Cut(c.trace, "\n")
		at := where(c.trace)
		msg := head
		if at != "" {
			msg += " at " + at // the same panic text from two places is two errors
		}
		notice("crash", msg, map[string]any{"where": at, "crashedVersion": c.version})
		app.RecordLog(newrelic.LogData{Severity: "FATAL", Message: c.trace,
			Attributes: map[string]any{"where": at, "crashedVersion": c.version}})
	}
	os.WriteFile(mark, []byte(strconv.Itoa(len(data))), 0o600)
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
