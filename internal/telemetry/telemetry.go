// Package telemetry reports desk's own crashes, errors and performance to
// New Relic, once the user gives a license key (settings.toml's [newrelic],
// or NEW_RELIC_LICENSE_KEY). Without one app stays nil and every call here
// does nothing: the agent's methods are nil-safe.
package telemetry

import (
	"cmp"
	"os"
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
		func(c *newrelic.Config) { c.Labels = map[string]string{"version": version} },
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
func Error(class, msg string) {
	t := app.StartTransaction("desk/" + class)
	t.NoticeError(newrelic.Error{Message: msg, Class: class})
	t.End()
}

// Track times name as a background transaction; done ends it, with its error.
func Track(name string) (done func(error)) {
	t := app.StartTransaction(name)
	return func(err error) {
		if err != nil {
			t.NoticeError(err)
		}
		t.End()
	}
}

// Frame records how long a window's frame took: Custom/Frame/ms.
func Frame(d time.Duration) {
	app.RecordCustomMetric("Frame/ms", float64(d)/float64(time.Millisecond))
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
		head, _, _ := strings.Cut(c, "\n")
		Error("crash", head)
		app.RecordLog(newrelic.LogData{Severity: "FATAL", Message: c})
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
	crashStart = regexp.MustCompile(`^(panic: |fatal error: )`)
	noteLine   = regexp.MustCompile(`^\d{4}-\d\d-\d\dT`) // a crashNote line, reported as it was written
)

// crashes splits the runtime's traces out of crash.log's text: each from a
// "panic:" or "fatal error:" line to the next such line or crashNote line.
func crashes(text string) []string {
	var out []string
	var cur []string
	end := func() {
		if cur != nil {
			out = append(out, strings.TrimRight(strings.Join(cur, "\n"), "\n"))
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
		case cur != nil:
			cur = append(cur, l)
		}
	}
	end()
	return out
}
