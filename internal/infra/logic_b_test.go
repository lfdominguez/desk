package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"

	deskorgs "github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/sshc"
	deskvault "github.com/lfdominguez/desk/internal/vault"
)

// lbFakeAsk stands in for the workspace's ask sheet: every question goes to
// answer (false cancels it); the questions asked come back in order.
func lbFakeAsk(t *testing.T, answer func(i int, q *question) (string, bool)) *[]*question {
	t.Helper()
	var mu sync.Mutex
	var qs []*question
	old := dispatch
	dispatch = func(m tea.Msg) {
		qm, ok := m.(questionMsg)
		if !ok {
			return
		}
		mu.Lock()
		qs = append(qs, qm.q)
		i := len(qs) - 1
		mu.Unlock()
		if s, ok := answer(i, qm.q); ok {
			qm.q.done <- s
		} else {
			close(qm.q.done)
		}
	}
	t.Cleanup(func() { dispatch = old })
	return &qs
}

// lbPipeTerm is a Term whose pty is a pipe the test reads.
func lbPipeTerm(t *testing.T, w, h int) (*Term, *os.File) {
	t.Helper()
	r, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); wr.Close() })
	return &Term{emu: vt.NewEmulator(w, h), pty: pipeProc{wr}}, r
}

func lbReadPipe(t *testing.T, r *os.File, n int) string {
	t.Helper()
	r.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, n)
	got := 0
	for got < n {
		m, err := r.Read(buf[got:])
		got += m
		if err != nil {
			break
		}
	}
	return string(buf[:got])
}

// lbEmuOut collects what the emulator sends to the program (its pipe blocks
// until read).
func lbEmuOut(t *testing.T, emu *vt.Emulator) func(want string) string {
	ch := make(chan string, 64)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := emu.Read(buf)
			if n > 0 {
				ch <- string(buf[:n])
			}
			if err != nil {
				close(ch)
				return
			}
		}
	}()
	t.Cleanup(func() { emu.Close() })
	var all string
	return func(want string) string {
		deadline := time.After(2 * time.Second)
		for !strings.Contains(all, want) {
			select {
			case s, ok := <-ch:
				if !ok {
					return all
				}
				all += s
			case <-deadline:
				return all
			}
		}
		return all
	}
}

// --- files.go: the file panels' operations, run on the "local" host ---

func TestFilesOverLocalHost(t *testing.T) {
	skipOnWindows(t, "it checks a link and mode bits; TestLocalGo has Windows' local host")
	ctx := context.Background()
	src := t.TempDir()
	os.Mkdir(filepath.Join(src, "sub"), 0o755)
	os.WriteFile(filepath.Join(src, "sub", "inner.txt"), []byte("in"), 0o644)
	os.WriteFile(filepath.Join(src, "a b.txt"), []byte("hello"), 0o640)
	os.Symlink("sub", filepath.Join(src, "ln"))

	check := func(how, abs string, es []fileEntry, err error) {
		t.Helper()
		if err != nil || abs != src || len(es) != 3 {
			t.Fatalf("%s: %v %q %+v", how, err, abs, es)
		}
		if es[0].Name != "ln" || !es[0].Dir || !es[0].Link || es[1].Name != "sub" || !es[1].Dir || es[1].Link {
			t.Errorf("%s dirs: %+v", how, es[:2])
		}
		if f := es[2]; f.Name != "a b.txt" || f.Dir || f.Link || f.Size != 5 || f.Mode != 0o640 || f.Mod.IsZero() {
			t.Errorf("%s file: %+v", how, f)
		}
	}
	abs, es, err := listRemote(ctx, "local", "", src)
	check("remote", abs, es, err)
	abs, les, err := listLocal(src)
	check("local", abs, les, err)
	if _, _, err := listLocal(filepath.Join(src, "nope")); err == nil {
		t.Error("listLocal of a missing dir must fail")
	}
	if _, _, err := listRemote(ctx, "local", "", filepath.Join(src, "nope")); err == nil || !strings.Contains(err.Error(), "no such directory") {
		t.Errorf("missing dir: %v", err)
	}
	t.Setenv("HOME", src)
	t.Setenv("USERPROFILE", src) // Windows' home
	if abs, _, err := listRemote(ctx, "local", "", ""); err != nil || abs != src {
		t.Errorf("home listing: %q %v", abs, err)
	}

	// copy a file down: content, mode, count
	dst := t.TempDir()
	var n atomic.Int64
	calls := 0
	if err := copyFromRemote(ctx, "local", "", src, dst, es[2], &n, func() { calls++ }); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dst, "a b.txt"))
	fi, _ := os.Stat(filepath.Join(dst, "a b.txt"))
	if string(b) != "hello" || fi.Mode().Perm() != 0o640 || n.Load() != 5 || calls == 0 {
		t.Errorf("down: %q %o %d %d", b, fi.Mode().Perm(), n.Load(), calls)
	}
	err = copyFromRemote(ctx, "local", "", src, dst, fileEntry{Name: "ghost"}, &n, func() {})
	if err == nil || !strings.Contains(err.Error(), "ghost") || fileExists(filepath.Join(dst, "ghost")) {
		t.Errorf("a failed copy must error and leave no file: %v", err)
	}
	if err := copyFromRemote(ctx, "local", "", src, dst, fileEntry{Name: "sub", Dir: true}, &n, func() {}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "sub", "inner.txt")); string(b) != "in" {
		t.Errorf("dir down: %q", b)
	}

	// copy up
	up := t.TempDir()
	os.WriteFile(filepath.Join(src, "run.sh"), []byte("#!/bin/sh\n"), 0o644)
	n.Store(0)
	if err := copyToRemote(ctx, "local", "", src, up, fileEntry{Name: "run.sh", Mode: 0o700}, &n, func() {}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(up, "run.sh")); err != nil || fi.Mode().Perm() != 0o700 || n.Load() != 10 {
		t.Errorf("up: %v %v %d", fi, err, n.Load())
	}
	if err := copyToRemote(ctx, "local", "", src, filepath.Join(up, "no", "such"), fileEntry{Name: "run.sh"}, &n, func() {}); err == nil {
		t.Error("copying into a missing dir must fail")
	}
	if err := copyToRemote(ctx, "local", "", src, up, fileEntry{Name: "ghost"}, &n, func() {}); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing local file: %v", err)
	}
	if err := copyToRemote(ctx, "local", "", src, up, fileEntry{Name: "sub", Dir: true}, &n, func() {}); err != nil {
		t.Fatal(err)
	}
	if !fileExists(filepath.Join(up, "sub", "inner.txt")) {
		t.Error("dir up not copied")
	}

	// mkdir, rename, remove
	if err := mkdirRemote(ctx, "local", "", filepath.Join(up, "x y", "z")); err != nil || !fileExists(filepath.Join(up, "x y", "z")) {
		t.Errorf("mkdir: %v", err)
	}
	if err := renameRemote(ctx, "local", "", filepath.Join(up, "run.sh"), filepath.Join(up, "it's.sh")); err != nil ||
		fileExists(filepath.Join(up, "run.sh")) || !fileExists(filepath.Join(up, "it's.sh")) {
		t.Errorf("rename: %v", err)
	}
	if err := removeRemote(ctx, "local", "", []string{filepath.Join(up, "it's.sh"), filepath.Join(up, "x y")}); err != nil ||
		fileExists(filepath.Join(up, "it's.sh")) || fileExists(filepath.Join(up, "x y")) || !fileExists(filepath.Join(up, "sub")) {
		t.Errorf("remove: %v", err)
	}
}

func TestRunErrAndPipeErrors(t *testing.T) {
	needSh(t)
	if err := runErr(exec.Command("sh", "-c", "exit 3")); err == nil || err.Error() != "exit status 3" {
		t.Errorf("no stderr: %v", err)
	}
	if err := runErr(exec.Command("sh", "-c", "echo '  boom ' >&2; exit 1")); err == nil || err.Error() != "boom" {
		t.Errorf("stderr: %v", err)
	}
	if err := runErr(exec.Command("sh", "-c", "echo noise >&2")); err != nil {
		t.Errorf("success with stderr: %v", err)
	}
	var n atomic.Int64
	c := counter{&n, func() {}}
	sh := func(script string) func(io.Writer) error {
		return func(w io.Writer) error { return remoteSh(context.Background(), "local", "", script, nil, w) }
	}
	read := func(script string) func(io.Reader) error {
		return func(r io.Reader) error { return remoteSh(context.Background(), "local", "", script, r, nil) }
	}
	if err := through(sh("echo bad >&2; exit 2"), read("cat >/dev/null"), c); err == nil || err.Error() != "bad" {
		t.Errorf("from fails: %v", err)
	}
	if err := through(sh("echo hi"), read("cat >/dev/null; echo nope >&2; exit 1"), c); err == nil || err.Error() != "nope" {
		t.Errorf("to fails: %v", err)
	}
	if err := through(sh("yes | head -c 300000"), read("exit 4"), c); err == nil || err.Error() != "exit status 4" {
		t.Errorf("to stops early: %v", err) // the producer's broken pipe is its fallout
	}
	n.Store(0)
	if err := through(sh("printf hello"), read("cat >/dev/null"), c); err != nil || n.Load() != 5 {
		t.Errorf("counted %d: %v", n.Load(), err)
	}
}

func TestHumanSizeUnits(t *testing.T) {
	for n, want := range map[int64]string{-1: "", 0: "0 B", 1023: "1023 B", 1024: "1.0 KB", 10 * 1024: "10 KB",
		3 << 20: "3.0 MB", 1 << 62: "4096 PB"} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

// --- monitor.go ---

func TestSourceAndGet(t *testing.T) {
	o := &orgConfig{Nagios: []SourceCfg{{Name: "a"}, {Name: "b"}}}
	if s, err := o.source(o.Nagios, "", "nagios"); err != nil || s.Name != "a" {
		t.Errorf("default: %v %v", s, err)
	}
	if s, err := o.source(o.Nagios, "b", "nagios"); err != nil || s.Name != "b" {
		t.Errorf("named: %v %v", s, err)
	}
	if _, err := o.source(o.Nagios, "c", "nagios"); err == nil || err.Error() != `no nagios source "c"` {
		t.Errorf("unknown: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/missing":
			w.WriteHeader(404)
		case "/broken":
			w.WriteHeader(500)
			w.Write([]byte("kaput"))
		case "/digest":
			if strings.HasPrefix(auth, "Digest ") && strings.Contains(auth, `username="admin"`) {
				w.Write([]byte("ok-digest"))
				return
			}
			w.Header().Set("WWW-Authenticate", `Digest realm="nagios", nonce="abc", qop="auth"`)
			w.WriteHeader(401)
		case "/j":
			if r.URL.RawQuery != "a=1" {
				w.WriteHeader(400)
				return
			}
			w.Write([]byte(`{"v":7}`))
		case "/notjson":
			w.Write([]byte(`<html>`))
		default:
			w.Write([]byte(auth))
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	plain := SourceCfg{Name: "n", URL: srv.URL}
	if _, err := o.get(ctx, plain, srv.URL+"/missing"); !errors.Is(err, errNoPage) || !strings.Contains(err.Error(), "n: /missing") {
		t.Errorf("404: %v", err)
	}
	if _, err := o.get(ctx, plain, srv.URL+"/broken"); err == nil || err.Error() != "n: HTTP 500: kaput" {
		t.Errorf("500: %v", err)
	}
	if _, err := o.get(ctx, plain, srv.URL+"/digest"); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("digest without a login must stay 401: %v", err)
	}
	o.logins = map[string][2]string{"dg": {"admin", "fake-pw"}, "bt": {"", "tok"}, "bs": {"u", "p"}, "none": {"", ""}}
	dg := SourceCfg{Name: "dg", URL: srv.URL, secret: &deskorgs.Secret{Vault: "v", Item: "i"}}
	if b, err := o.get(ctx, dg, srv.URL+"/digest"); err != nil || string(b) != "ok-digest" {
		t.Errorf("digest: %q %v", b, err)
	}
	if b, _ := o.get(ctx, SourceCfg{Name: "bt", secret: &deskorgs.Secret{}}, srv.URL+"/echo"); string(b) != "Bearer tok" {
		t.Errorf("bearer: %q", b)
	}
	if b, _ := o.get(ctx, SourceCfg{Name: "bs", secret: &deskorgs.Secret{}}, srv.URL+"/echo"); !strings.HasPrefix(string(b), "Basic ") {
		t.Errorf("basic: %q", b)
	}
	if b, _ := o.get(ctx, SourceCfg{Name: "none", secret: &deskorgs.Secret{}}, srv.URL+"/echo"); string(b) != "" {
		t.Errorf("an empty login sends no header: %q", b)
	}
	if _, err := o.get(ctx, SourceCfg{Name: "x", secret: &deskorgs.Secret{Vault: "nope", Item: "i"}}, srv.URL+"/echo"); err == nil || !strings.HasPrefix(err.Error(), "login for x: ") {
		t.Errorf("login error: %v", err)
	}
	if _, err := o.get(ctx, plain, "://bad"); err == nil {
		t.Error("a bad URL must fail")
	}
	var v struct{ V int }
	if err := o.getJSON(ctx, SourceCfg{Name: "j", URL: srv.URL + "/"}, "j", url.Values{"a": {"1"}}, &v); err != nil || v.V != 7 {
		t.Errorf("getJSON: %v %v", v, err)
	}
	if err := o.getJSON(ctx, plain, "notjson", url.Values{}, &v); err == nil {
		t.Error("bad JSON must fail")
	}
	if err := o.getJSON(ctx, plain, "missing", url.Values{}, &v); !errors.Is(err, errNoPage) {
		t.Errorf("getJSON 404: %v", err)
	}
}

func TestNagios3Fallback(t *testing.T) {
	services := `<table><tr><td><a href="extinfo.cgi?type=2&amp;host=web1&amp;service=HTTP">HTTP</a></td><td>CRITICAL</td><td>01-01-2024 00:00:00</td><td>0d 1h 0m 0s</td><td>3/3</td><td>down hard</td></tr></table>`
	hosts := `<table><tr><td><a href="extinfo.cgi?type=1&amp;host=web1">web1</a></td><td>UP</td><td>0d 0h 5m 0s</td><td>PING OK</td></tr>` +
		`<tr><td><a href="extinfo.cgi?type=1&amp;host=db1">db1</a></td><td>DOWN</td><td>0d 2h 0m 0s</td><td>PING CRITICAL</td></tr></table>`
	fail := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "statusjson.cgi"):
			w.WriteHeader(404)
		case fail != "" && r.URL.Query().Get(fail) != "":
			w.WriteHeader(500)
		case r.URL.Query().Get("host") == "all":
			w.Write([]byte(services))
		case r.URL.Query().Get("hostgroup") == "all":
			w.Write([]byte(hosts))
		}
	}))
	defer srv.Close()
	o := &orgConfig{Nagios: []SourceCfg{{Name: "n3", URL: srv.URL + "/nagios/"}}}
	out, err := o.nagios(context.Background(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 3 || lines[0] != "problems among 2 hosts and 1 services:" || !strings.HasPrefix(lines[1], "DOWN        db1 for 2h0m0s") ||
		!strings.HasPrefix(lines[2], "CRITICAL    web1 / HTTP for 1h0m0s") || !strings.HasSuffix(lines[2], "— down hard") {
		t.Errorf("nagios 3:\n%s", out)
	}
	fail = "hostgroup"
	if _, err := o.nagios(context.Background(), "", "", ""); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("a failing page: %v", err)
	}
	if _, err := o.nagios(context.Background(), "zz", "", ""); err == nil || !strings.Contains(err.Error(), `no nagios source "zz"`) {
		t.Errorf("unknown source: %v", err)
	}
}

func TestNagiosServicelistFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") == "hostlist" {
			w.Write([]byte(`{"data":{"hostlist":{}}}`))
			return
		}
		w.WriteHeader(503)
	}))
	defer srv.Close()
	o := &orgConfig{Nagios: []SourceCfg{{Name: "n", URL: srv.URL}}}
	if _, err := o.nagios(context.Background(), "", "", ""); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Errorf("servicelist error: %v", err)
	}
	srv.Close()
	if _, err := o.nagios(context.Background(), "", "", ""); err == nil {
		t.Error("an unreachable source must fail")
	}
}

func TestFormatNagiosEdges(t *testing.T) {
	now := time.Unix(1700000000, 0)
	hosts := map[string]nagiosObj{"a": {Status: 2}, "b": {Status: 2}}
	svcs := map[string]map[string]nagiosObj{"a": {"ping": {Status: 2, CurrentAttempt: 1, MaxAttempts: 3, Output: "fine"}}}
	if out := formatNagios(hosts, svcs, "", "", now); out != "problems among 2 hosts and 1 services: none, everything is OK/UP" {
		t.Errorf("all ok: %q", out)
	}
	if out := formatNagios(hosts, svcs, "zzz", "", now); out != `no host matches "zzz"; hosts: a, b` {
		t.Errorf("no match: %q", out)
	}
	out := formatNagios(hosts, svcs, "A", "PING", now)
	if out != "1 hosts, 1 services checked\nOK          a / ping — fine" {
		t.Errorf("host+service view (no host line, no soft flag on OK): %q", out)
	}
	if out := formatNagios(hosts, svcs, "a", "", now); !strings.Contains(out, "UP          a — ") || !strings.Contains(out, "a / ping") {
		t.Errorf("host view lists the host: %q", out)
	}
	down := map[string]nagiosObj{"b": {Status: 4, Downtime: 1, LastChange: now.Add(-90 * time.Minute).UnixMilli(), Output: " x "}}
	if out := formatNagios(down, nil, "", "", now); out != "problems among 1 hosts and 0 services:\nDOWN        b for 1h30m0s [downtime] — x" {
		t.Errorf("downtime: %q", out)
	}
	many := map[string]nagiosObj{}
	for i := range 301 {
		many[fmt.Sprintf("h%03d", i)] = nagiosObj{Status: 4}
	}
	lines := strings.Split(formatNagios(many, nil, "", "", now), "\n")
	if len(lines) != 302 || lines[301] != "… 1 more" || !strings.Contains(lines[1], "h000") {
		t.Errorf("cap: %d lines, last %q", len(lines), lines[len(lines)-1])
	}
}

func TestPromTimeForms(t *testing.T) {
	now := time.Unix(1700000000, 0)
	for in, want := range map[string]time.Time{
		"": now, "now": now, "-30m": now.Add(-30 * time.Minute), "2024-01-02T03:04:05Z": time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
		"1700000123.5": time.Unix(1700000123, 5e8),
	} {
		if got, err := promTime(in, now); err != nil || !got.Equal(want) {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := promTime("-x", now); err == nil {
		t.Error("bad duration must fail")
	}
	if _, err := promTime("tomorrow", now); err == nil || !strings.Contains(err.Error(), `bad time "tomorrow"`) {
		t.Errorf("bad time: %v", err)
	}
}

func TestMetricsQueries(t *testing.T) {
	var names []string
	var last url.Values
	var lastPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last, lastPath = r.URL.Query(), r.URL.Path
		switch {
		case r.URL.Path == "/api/v1/label/__name__/values":
			b, _ := json.Marshal(names)
			fmt.Fprintf(w, `{"status":"success","data":%s}`, b)
		case last.Get("query") == "bad":
			w.Write([]byte(`{"status":"error","error":"parse error"}`))
		case r.URL.Path == "/api/v1/query":
			w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"__name__":"up","job":"a"},"value":[1700000000,"1"]}]},"warnings":["partial","slow"]}`))
		case r.URL.Path == "/api/v1/query_range":
			w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"up"},"values":[[1700000000,"1"]]}]}}`))
		}
	}))
	defer srv.Close()
	o := &orgConfig{Metrics: []SourceCfg{{Name: "m", URL: srv.URL}}}
	ctx := context.Background()

	names = []string{"node_cpu", "go_gc", "node_mem"}
	if out, err := o.metrics(ctx, "", "", "", "", "", "node"); err != nil || out != "2 metrics match \"node\":\nnode_cpu\nnode_mem" {
		t.Errorf("find: %q %v", out, err)
	}
	names = nil
	for i := range 401 {
		names = append(names, fmt.Sprint("m", i))
	}
	out, _ := o.metrics(ctx, "", "", "", "", "", "m")
	if lines := strings.Split(out, "\n"); len(lines) != 401 || lines[0] != `401 metrics match "m"; narrow find. First ones:` || lines[400] != "m399" {
		t.Errorf("find cap: %d %q", len(lines), lines[0])
	}

	out, err := o.metrics(ctx, "m", "up", "", "1700000000", "", "")
	if err != nil || out != "vector, 1 series\nup{job=\"a\"} 1\nwarnings: partial; slow" || lastPath != "/api/v1/query" || last.Get("time") != "1700000000" {
		t.Errorf("instant: %q %v %s %v", out, err, lastPath, last)
	}
	span := func() int64 {
		s, _ := strconv.ParseInt(last.Get("start"), 10, 64)
		e, _ := strconv.ParseInt(last.Get("end"), 10, 64)
		return e - s
	}
	if out, err := o.metrics(ctx, "", "up", "-1h", "now", "", ""); err != nil || !strings.HasPrefix(out, "matrix, 1 series") || last.Get("step") != "30s" || span() != 3600 || lastPath != "/api/v1/query_range" {
		t.Errorf("range: %q %v %v", out, err, last)
	}
	o.metrics(ctx, "", "up", "-10s", "", "", "")
	if last.Get("step") != "1s" {
		t.Errorf("short range step: %v", last.Get("step"))
	}
	o.metrics(ctx, "", "up", "-1h", "", "5m", "")
	if last.Get("step") != "5m" {
		t.Errorf("explicit step: %v", last.Get("step"))
	}
	if _, err := o.metrics(ctx, "", "bad", "", "", "", ""); err == nil || err.Error() != "m: parse error" {
		t.Errorf("error status: %v", err)
	}
	if _, err := o.metrics(ctx, "", "up", "", "never", "", ""); err == nil {
		t.Error("bad end must fail")
	}
	if _, err := o.metrics(ctx, "", "up", "never", "", "", ""); err == nil {
		t.Error("bad start must fail")
	}
	if _, err := o.metrics(ctx, "zz", "up", "", "", "", ""); err == nil || !strings.Contains(err.Error(), `no metrics source "zz"`) {
		t.Errorf("unknown source: %v", err)
	}
}

func TestFormatPromShapes(t *testing.T) {
	if s := formatProm("scalar", json.RawMessage(`[1700000000,"3.5"]`)); s != "scalar: 3.5" {
		t.Errorf("scalar %q", s)
	}
	if s := formatProm("string", json.RawMessage(`[1700000000,"hi"]`)); s != "string: hi" {
		t.Errorf("string %q", s)
	}
	if s := formatProm("vector", json.RawMessage(`[]`)); s != "no data (empty result)" {
		t.Errorf("empty %q", s)
	}
	m := `[{"metric":{"__name__":"x","b":"2","a":"1"},"values":[]},{"metric":{"__name__":"x"},"values":[[1700000000,"1"],[1700000060,"2"],[1700000120,"3"]]}]`
	if s := formatProm("matrix", json.RawMessage(m)); s != "matrix, 2 series\nx{}  points=3 min=1 avg=2 max=3 last=3\n  22:13=1 22:14=2 22:15=3" {
		t.Errorf("matrix %q", s)
	}
	var vals []string
	for i := range 40 {
		vals = append(vals, fmt.Sprintf(`[%d,"%d"]`, 1700000000+60*i, i))
	}
	s := formatProm("matrix", json.RawMessage(`[{"metric":{},"values":[`+strings.Join(vals, ",")+`]}]`))
	if lines := strings.Split(s, "\n"); len(lines) != 3 || len(strings.Fields(lines[2])) != 20 || !strings.Contains(lines[1], "min=0 avg=19.5 max=39 last=39") {
		t.Errorf("sampled: %q", s)
	}
	var series []string
	for i := range 201 {
		series = append(series, fmt.Sprintf(`{"metric":{"i":"%d"},"value":[0,"1"]}`, i))
	}
	lines := strings.Split(formatProm("vector", json.RawMessage("["+strings.Join(series, ",")+"]")), "\n")
	if len(lines) != 202 || lines[0] != "vector, 201 series" || lines[201] != "… 1 more series" {
		t.Errorf("series cap: %d %q", len(lines), lines[len(lines)-1])
	}
	if l := labels(map[string]string{"b": "2", "__name__": "m", "a": `q"`}); l != `m{a="q\"", b="2"}` {
		t.Errorf("labels %s", l)
	}
	if num("2.5") != 2.5 || num("x") == num("x") || num(3.0) == num(3.0) {
		t.Error("num: strings parse, anything else is NaN")
	}
}

// --- org.go ---

func TestReloadOrgsAndBrokenConfigs(t *testing.T) {
	root := t.TempDir()
	infra := filepath.Join(root, "Acme", infraDir)
	os.MkdirAll(infra, 0o755)
	toml := filepath.Join(root, "Acme", ".desk", "desk.toml")
	os.WriteFile(toml, []byte("[[vault]\n"), 0o644)
	if _, err := orgFor(infra); err == nil || !strings.Contains(err.Error(), toml) {
		t.Fatalf("broken desk.toml: %v", err)
	}
	os.WriteFile(toml, []byte(`[[vault]]
name = "pb"
kind = "passbolt"
url = "https://pb.example.com"
key_file = "/keys/k.asc"
password = { token = "fake-pass" }
[[vault]]
name = "hv"
kind = "hashicorp"
mount = "kv"
`), 0o644)
	o, err := orgFor(infra)
	if err != nil {
		t.Fatalf("a failed load must not be cached: %v", err)
	}
	want := deskorgs.Vault{Name: "pb", Kind: "passbolt", URL: "https://pb.example.com", KeyFile: "/keys/k.asc", Password: deskorgs.Secret{Token: "fake-pass"}}
	if o.Vaults[0].desk != want || o.Vaults[0].Type != "passbolt" || o.Vaults[1].Type != "hashicorp" || o.Vaults[1].Mount != "kv" {
		t.Errorf("vaults: %+v", o.Vaults)
	}
	if o2, _ := orgFor(infra); o2 != o {
		t.Error("orgAt must cache")
	}
	reloadOrgs()
	if o3, _ := orgFor(infra); o3 == o {
		t.Error("reloadOrgs must drop the cache")
	}

	g := filepath.Join(root, "Globex", infraDir)
	os.MkdirAll(g, 0o755)
	os.WriteFile(filepath.Join(root, "Globex", ".desk", "desk.toml"), []byte("record = [\n"), 0o644)
	if _, err := orgFor(g); err == nil {
		t.Error("a broken desk.toml must fail")
	}
	var nilOrg *orgConfig
	f := false
	if !nilOrg.records() || !(&orgConfig{}).records() || (&orgConfig{Record: &f}).records() {
		t.Error("records: on unless record = false")
	}
}

func TestSourceNameDescribeClient(t *testing.T) {
	if sourceName("n", "https://x.example.com") != "n" || sourceName("", "https://vm.example.com:8428/x") != "vm.example.com:8428" ||
		sourceName("", "not a url") != "not a url" {
		t.Error("sourceName")
	}
	if d := (&orgConfig{Metrics: []SourceCfg{{Name: "m"}}}).describe(); d != "metrics, PromQL (metrics): m" {
		t.Errorf("describe %q", d)
	}
	if d := (&orgConfig{}).describe(); d != "" {
		t.Errorf("empty describe %q", d)
	}
	if c := httpClient(true); !c.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify {
		t.Error("insecure client must skip verification")
	}
	if c := httpClient(false); c.Transport.(*http.Transport).TLSClientConfig != nil && c.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify {
		t.Error("secure client must verify")
	}
}

func TestOrgVaultLookup(t *testing.T) {
	o := &orgConfig{Vaults: []VaultCfg{{Name: "hv", Type: "hashicorp"}, {Name: "pb", Type: "passbolt"}, {Name: "x", Type: "1password"}, {Name: "fk", Type: "passbolt"}}}
	o.vaults = map[string]vault{"fk": fakeVault{}}
	if v, c, err := o.vault(""); err != nil || c.Name != "hv" {
		t.Errorf("first: %v %v", c, err)
	} else if _, ok := v.(hashiVault); !ok {
		t.Errorf("hashicorp: %T", v)
	}
	if v, _, err := o.vault("pb"); err != nil {
		t.Error(err)
	} else if _, ok := v.(deskVault); !ok || o.vaults["pb"] == nil {
		t.Errorf("passbolt: %T, cached %v", v, o.vaults["pb"])
	}
	if v, _, _ := o.vault("fk"); v == nil {
		t.Error("cached vault")
	} else if _, ok := v.(fakeVault); !ok {
		t.Errorf("must reuse the opened vault: %T", v)
	}
	if _, _, err := o.vault("x"); err == nil || !strings.Contains(err.Error(), `unknown type "1password"`) {
		t.Errorf("unknown type: %v", err)
	}
	if _, _, err := o.vault("nope"); err == nil || err.Error() != `no vault "nope"` {
		t.Errorf("missing: %v", err)
	}
	fresh := &orgConfig{Vaults: []VaultCfg{{Name: "h", Type: "hashicorp"}}}
	if _, _, err := fresh.vault("h"); err != nil || fresh.vaults["h"] == nil {
		t.Errorf("first open is kept: %v %v", err, fresh.vaults)
	}
}

type lbErrVault struct{}

func (lbErrVault) search(context.Context, string) ([]vaultItem, error) {
	return nil, errors.New("vault down")
}
func (lbErrVault) get(context.Context, string) (vaultItem, error) { return vaultItem{}, nil }

func TestResolveVaultItem(t *testing.T) {
	ctx := context.Background()
	v := fakeVault{"id1": {ID: "id1", Name: "Web", Password: "p1"}, "id2": {ID: "id2", Name: "db", Password: "p2"}, "id3": {ID: "id3", Name: "DB"}}
	for _, q := range []string{"id1", "web", "WEB"} {
		if it, err := resolve(ctx, v, q); err != nil || it.Password != "p1" {
			t.Errorf("%s: %+v %v", q, it, err)
		}
	}
	if _, err := resolve(ctx, v, "db"); err == nil || err.Error() != `2 items named "db"; use the id` {
		t.Errorf("ambiguous: %v", err)
	}
	if _, err := resolve(ctx, v, "zzz"); err == nil || err.Error() != `no item "zzz"` {
		t.Errorf("none: %v", err)
	}
	if _, err := resolve(ctx, lbErrVault{}, "x"); err == nil || err.Error() != "vault down" {
		t.Errorf("search error: %v", err)
	}
}

func TestLoginAndAuthorize(t *testing.T) {
	ctx := context.Background()
	o := &orgConfig{}
	if u, p, err := o.login(ctx, SourceCfg{Name: "none"}); u != "" || p != "" || err != nil {
		t.Errorf("no creds: %q %q %v", u, p, err)
	}
	d := SourceCfg{Name: "d", user: "me", secret: &deskorgs.Secret{Token: "t0k"}}
	if u, p, err := o.login(ctx, d); u != "me" || p != "t0k" || err != nil {
		t.Errorf("desk secret: %q %q %v", u, p, err)
	}
	if o.logins["d"] != [2]string{"me", "t0k"} {
		t.Errorf("not cached: %v", o.logins)
	}
	d.secret = &deskorgs.Secret{Token: "other"} // fetched once per run: the cache answers now
	if _, p, _ := o.login(ctx, d); p != "t0k" {
		t.Errorf("cache: %q", p)
	}
	t.Setenv("LB_UNSET_TOKEN", "")
	if _, _, err := o.login(ctx, SourceCfg{Name: "e", secret: &deskorgs.Secret{TokenEnv: "LB_UNSET_TOKEN"}}); err == nil || !strings.HasPrefix(err.Error(), "login for e: ") {
		t.Errorf("token env unset: %v", err)
	}
	if _, ok := o.logins["e"]; ok {
		t.Error("a failed login must not be cached")
	}

	auth := func(s SourceCfg) *http.Request {
		req := httptest.NewRequest("GET", "/", nil)
		if err := o.authorize(ctx, s, req); err != nil {
			t.Fatal(err)
		}
		return req
	}
	if h := auth(SourceCfg{Name: "b1", secret: &deskorgs.Secret{Token: "tok"}}).Header.Get("Authorization"); h != "Bearer tok" {
		t.Errorf("desk without user is bearer: %q", h)
	}
	if u, p, ok := auth(SourceCfg{Name: "b2", user: "me", secret: &deskorgs.Secret{Token: "tok"}}).BasicAuth(); !ok || u != "me" || p != "tok" {
		t.Errorf("desk with user is basic: %q %q", u, p)
	}
	if h := auth(SourceCfg{Name: "plain"}).Header.Get("Authorization"); h != "" {
		t.Errorf("no creds, no header: %q", h)
	}
	req := httptest.NewRequest("GET", "/", nil)
	if err := o.authorize(ctx, SourceCfg{Name: "n4", secret: &deskorgs.Secret{TokenEnv: "LB_UNSET_TOKEN"}}, req); err == nil || req.Header.Get("Authorization") != "" {
		t.Errorf("authorize error: %v", err)
	}
}

func TestUnlockingAsksTheUser(t *testing.T) {
	ctx := context.Background()
	if s, ok := askUser("x", true); s != "" || ok {
		t.Error("no workspace: nobody to ask")
	}
	locked := func() (string, error) { return "", &deskvault.LockedError{} }

	qs := lbFakeAsk(t, func(int, *question) (string, bool) { return "", false })
	if _, err := unlocking(ctx, deskorgs.Vault{Name: "pb", Kind: "passbolt"}, locked); err == nil || err.Error() != "the user cancelled the vault unlock" {
		t.Errorf("cancel: %v", err)
	}
	if _, err := unlocking(ctx, deskorgs.Vault{Name: "bw", Kind: "bitwarden"}, locked); err == nil {
		t.Error("cancel must fail")
	}
	if len(*qs) != 2 || (*qs)[0].title != "Passbolt passphrase · pb" || !(*qs)[0].masked || (*qs)[1].title != "Master password · bw" {
		t.Errorf("titles: %+v %+v", (*qs)[0], (*qs)[len(*qs)-1])
	}

	// a one-time code, then the vault answers
	qs = lbFakeAsk(t, func(int, *question) (string, bool) { return " 123456 ", true })
	calls := 0
	got, err := unlocking(ctx, deskorgs.Vault{Name: "v"}, func() (string, error) {
		if calls++; calls == 1 {
			return "", &deskvault.LockedError{Code: true}
		}
		return "secret", nil
	})
	if err != nil || got != "secret" || len(*qs) != 1 || (*qs)[0].title != "One-time code · v" || (*qs)[0].masked {
		t.Errorf("code: %q %v %+v", got, err, *qs)
	}
	lbFakeAsk(t, func(int, *question) (string, bool) { return "", false })
	if _, err := unlocking(ctx, deskorgs.Vault{Name: "v"}, func() (string, error) { return "", &deskvault.LockedError{Code: true} }); err == nil || err.Error() != "the user cancelled the one-time code" {
		t.Errorf("code cancel: %v", err)
	}

	// an empty answer leaves it locked: five tries, then give up
	qs = lbFakeAsk(t, func(int, *question) (string, bool) { return "", true })
	if _, err := unlocking(ctx, deskorgs.Vault{Name: "v"}, locked); err == nil || err.Error() != `vault "v" stayed locked` || len(*qs) != 5 {
		t.Errorf("stays locked: %v after %d asks", err, len(*qs))
	}
	// Unlock's own failure ends it
	lbFakeAsk(t, func(int, *question) (string, bool) { return "fake-pw", true })
	if _, err := unlocking(ctx, deskorgs.Vault{Name: "v"}, locked); err == nil || !strings.Contains(err.Error(), "is not bitwarden or passbolt") {
		t.Errorf("unlock error: %v", err)
	}
	// anything else passes through without asking
	qs = lbFakeAsk(t, func(int, *question) (string, bool) { return "", true })
	if _, err := unlocking(ctx, deskorgs.Vault{Name: "v"}, func() (int, error) { return 0, errors.New("boom") }); err == nil || err.Error() != "boom" || len(*qs) != 0 {
		t.Errorf("other error: %v", err)
	}

	// a locked desk vault asks too, and the cancel reaches search, get and login
	lbFakeAsk(t, func(int, *question) (string, bool) { return "", false })
	d := deskVault{deskorgs.Vault{Name: "lbpb", Kind: "passbolt", URL: "https://pb.example.com"}}
	if _, err := d.search(ctx, ""); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("search: %v", err)
	}
	if _, err := d.get(ctx, "id"); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("get: %v", err)
	}
	o := &orgConfig{Vaults: []VaultCfg{{Name: "lbpb", Type: "passbolt", desk: d.v}}}
	if _, _, err := o.login(ctx, SourceCfg{Name: "s", secret: &deskorgs.Secret{Vault: "lbpb", Item: "i"}}); err == nil || err.Error() != "login for s: the user cancelled the vault unlock" {
		t.Errorf("login: %v", err)
	}
}

// --- sshcfg.go ---

func TestHostFileAndPasswordRef(t *testing.T) {
	root := t.TempDir()
	org := filepath.Join(root, "Acme")
	infra := filepath.Join(org, infraDir)
	os.MkdirAll(infra, 0o755)
	inc := filepath.Join(org, "servers.d", "extra.conf")
	write(t, inc, "Host web2\n    HostName 10.0.0.2\n    #@ password web2-item\n")
	write(t, filepath.Join(org, ".desk", "ssh_config"), "#@ vault pb\nHost web1\n    HostName 10.0.0.1\n    #@ password web1-item\nInclude "+inc+"\n")
	if f := hostFile(infra, "web2"); f != inc {
		t.Errorf("included host: %s", f)
	}
	if f := hostFile(infra, "web1"); f != filepath.Join(org, ".desk", "ssh_config") {
		t.Errorf("main host: %s", f)
	}
	if f := hostFile(infra, "ghost"); f != filepath.Join(org, ".desk", "ssh_config") {
		t.Errorf("unknown host: %s", f)
	}
	if r := passwordRef(infra, "web2"); r == nil || r.Vault != "pb" || r.Item != "web2-item" {
		t.Errorf("servers file ref: %+v", r)
	}
	if r := passwordRef(infra, "ghost"); r != nil {
		t.Errorf("unknown host has no ref: %+v", r)
	}
	if passwordRef(filepath.Join(org, "src", "x"), "web1") != nil || passwordRef(filepath.Join(root, "Globex", infraDir), "web1") != nil {
		t.Error("a dir that isn't an infra dir, or an org without a servers file: no ref")
	}
}

func TestServerPasswordSources(t *testing.T) {
	g := filepath.Join(t.TempDir(), "Acme", infraDir)
	if _, err := serverPassword(g, nil); err == nil || !strings.Contains(err.Error(), filepath.Join(orgOfInfra(g), ".desk", serversFile)) || !strings.Contains(err.Error(), "#@ password") {
		t.Errorf("nil ref: %v", err)
	}
	if pw, err := serverPassword(g, &SecretRef{Raw: "fake-raw"}); pw != "fake-raw" || err != nil {
		t.Errorf("raw: %q %v", pw, err)
	}
	orgsMu.Lock()
	orgs[g] = &orgConfig{Vaults: []VaultCfg{{Name: "pb", Type: "passbolt"}},
		vaults: map[string]vault{"pb": fakeVault{"id1": {ID: "id1", Name: "srv", Password: "fake-s3cret"}}}}
	orgsMu.Unlock()
	if pw, err := serverPassword(g, &SecretRef{Vault: "pb", Item: "SRV", Field: "username"}); pw != "fake-s3cret" || err != nil {
		t.Errorf("vault: %q %v", pw, err)
	}
	if _, err := serverPassword(g, &SecretRef{Vault: "zz", Item: "srv"}); err == nil || err.Error() != `no vault "zz"` {
		t.Errorf("unknown vault: %v", err)
	}
	if _, err := serverPassword(g, &SecretRef{Vault: "pb", Item: "nope"}); err == nil {
		t.Error("missing item must fail")
	}
	bad := filepath.Join(t.TempDir(), "Globex", infraDir)
	write(t, filepath.Join(orgOfInfra(bad), ".desk", "desk.toml"), "[[nagios]\n")
	if _, err := serverPassword(bad, &SecretRef{Vault: "pb", Item: "x"}); err == nil {
		t.Error("a broken org config must fail")
	}
}

// TestSSHAskPrompts: each SSH question goes to the ask sheet as
// "ssh <host> · <prompt>", masked unless it may echo; a raw #@ password
// answers the first password, then the user is told it was rejected; a
// vault that fails says why with the question; every question marks the
// server as answering.
func TestSSHAskPrompts(t *testing.T) {
	qs := lbFakeAsk(t, func(i int, q *question) (string, bool) { return fmt.Sprint("answer", i), i < 4 })
	dir := filepath.Join(t.TempDir(), "Acme", infraDir)
	write(t, filepath.Join(orgOfInfra(dir), ".desk", serversFile), "Host srv\n    #@ password-raw fake-raw\nHost vlt\n    #@ vault nope\n    #@ password x\n")
	asked := 0
	var o sshc.Options
	sshAsk(&o, "srv", dir, func() { asked++ })
	if a, err := o.Prompt("Are you sure you want to continue connecting (yes/no/[fingerprint])? ", true); a != "answer0" || err != nil ||
		(*qs)[0].title != "ssh srv · Are you sure you want to continue connecting (yes/no/[fingerprint])?" || (*qs)[0].masked {
		t.Errorf("host key: %q %v %+v", a, err, (*qs)[0])
	}
	o.Prompt("Enter passphrase for key '/k': ", false)
	if !(*qs)[1].masked || strings.Contains((*qs)[1].title, "vault") {
		t.Errorf("passphrase: %+v", (*qs)[1])
	}
	if pw, ok := o.Password(); pw != "fake-raw" || !ok || len(*qs) != 2 {
		t.Errorf("raw first try: %q %v", pw, ok)
	}
	o.Prompt("srv's Password: ", false)
	if q := (*qs)[2]; q.title != "ssh srv · srv's Password:  (the vault password was rejected)" || !q.masked {
		t.Errorf("retry: %+v", q)
	}
	if asked != 3 {
		t.Errorf("asked %d", asked)
	}
	sshAsk(&o, "vlt", dir, func() {})
	if _, ok := o.Password(); ok {
		t.Error("a vault that fails has no password")
	}
	o.Prompt("password:", false)
	if q := (*qs)[3]; q.title != `ssh vlt · password:  (vault: no vault "nope")` {
		t.Errorf("vault error: %q", q.title)
	}
	o.Prompt("Password:", false)
	if q := (*qs)[4]; q.title != "ssh vlt · Password:" {
		t.Errorf("the vault's error is said once: %q", q.title)
	}
	if _, err := o.Prompt("Password:", false); err == nil {
		t.Error("a cancelled question is an error")
	}
}

func TestWakeLegacyApplyErrors(t *testing.T) {
	if err := wake(""); err == nil || err.Error() != "wol: no MAC" {
		t.Errorf("empty: %v", err)
	}
	if err := wake("02:00:00:00:00:00:00:01"); err == nil || !strings.Contains(err.Error(), "bad MAC") {
		t.Errorf("EUI-64: %v", err)
	}
	if err := wake("52:54:00:12:34:56 127.0.0.1:notaport"); err == nil {
		t.Error("bad address must fail")
	}
	if fixes := legacyFixes(&sshc.NegotiationError{What: "Ciphers", Theirs: []string{"lb-bogus-cipher", "lb-other"}}); len(fixes) != 0 {
		t.Errorf("unsupported: %v", fixes)
	}
	cfg := filepath.Join(t.TempDir(), "ssh_config")
	if err := applyFixes(cfg, "a", nil); err == nil {
		t.Error("missing file must fail")
	}
	os.WriteFile(cfg, []byte("Host a\n    HostName 1.1.1.1\n"), 0o600)
	if err := applyFixes(cfg, "ghost", nil); err == nil || !strings.Contains(err.Error(), "no Host ghost in") {
		t.Errorf("missing host: %v", err)
	}
}

func TestPortForwardText(t *testing.T) {
	for f, want := range map[portFwd]string{
		{name: "web1", kind: "L", spec: "8080:localhost:80"}:   "localhost:8080 → localhost:80 via web1",
		{name: "web1", kind: "R", spec: "9000:localhost:3000"}: "web1:9000 → localhost:3000 here",
		{name: "web1", kind: "D", spec: "1080"}:                "socks localhost:1080 via web1",
	} {
		if s := f.String(); s != want {
			t.Errorf("%q, want %q", s, want)
		}
	}
}

// --- term.go ---

func TestTermWritesToProgram(t *testing.T) {
	tm, r := lbPipeTerm(t, 20, 4)
	tm.Input([]byte("ls\r"))
	if s := lbReadPipe(t, r, 3); s != "ls\r" {
		t.Errorf("input %q", s)
	}
	tm.TypeForClaude("uptime")
	if s := lbReadPipe(t, r, 8); s != "\x05\x15uptime" || !tm.byClaude || !tm.typed {
		t.Errorf("type %q %v %v", s, tm.byClaude, tm.typed)
	}
	tm.TypeSecret("fake-pw")
	if s := lbReadPipe(t, r, 8); s != "fake-pw\r" {
		t.Errorf("secret %q", s)
	}
	out := lbEmuOut(t, tm.emu)
	tm.byClaude, tm.typed = false, false
	tm.TypeForClaude("a\nb")
	if s := out("a\nb"); !strings.Contains(s, "a\nb") || !tm.typed || !tm.byClaude {
		t.Errorf("several lines go as a paste: %q", s)
	}
	tm.emu.Write([]byte("\x1b[?2004h"))
	tm.Paste("xyz")
	if s := out("\x1b[201~"); !strings.Contains(s, "\x1b[200~xyz\x1b[201~") {
		t.Errorf("bracketed paste: %q", s)
	}
	// Windows' pipes take no deadline, so a read there would wait forever:
	// the check is the other OSes'
	if r.SetReadDeadline(time.Now().Add(50*time.Millisecond)) == nil {
		if n, _ := r.Read(make([]byte, 8)); n != 0 {
			t.Error("a paste must not go straight to the pty")
		}
	}

	if tm.WheelToApp(uv.MouseWheelEvent{X: 1, Y: 1, Button: uv.MouseWheelUp}) {
		t.Error("the main screen scrolls itself")
	}
	tm.emu.Write([]byte("\x1b[?1049h\x1b[?1000h\x1b[?1006h"))
	if !tm.WheelToApp(uv.MouseWheelEvent{X: 1, Y: 1, Button: uv.MouseWheelUp}) {
		t.Error("a full-screen program gets the wheel")
	}
	if s := out("\x1b[<64;"); !strings.Contains(s, "\x1b[<64;") {
		t.Errorf("wheel report: %q", s)
	}

	cast := filepath.Join(t.TempDir(), "p.cast")
	if err := tm.Record(cast); err != nil {
		t.Fatal(err)
	}
	tm.Resize(30, 6)
	if w, h := tm.Size(); w != 30 || h != 6 {
		t.Errorf("resize %dx%d", w, h)
	}
	tm.Resize(30, 6)
	tm.rec.close()
	if b, _ := os.ReadFile(cast); strings.Count(string(b), `"r","30x6"`) != 1 {
		t.Errorf("one resize event: %s", b)
	}

	// refusals that need no program
	if r := tm.Run("a\nb", time.Second); !r.IsError || r.Text != "one command line only" {
		t.Errorf("run: %+v", r)
	}
	if r := tm.SendKeys("<nope>"); !r.IsError || r.Text != "unknown key <nope>" {
		t.Errorf("keys: %+v", r)
	}
	if tm.Find("  ") != nil {
		t.Error("a blank search finds nothing")
	}
	tm.emu.WriteString("Password: ")
	if _, ok := tm.SudoPrompt(); ok {
		t.Error("no sudo prompt on the alt screen")
	}
}

func TestTermStatusAndCwd(t *testing.T) {
	tm := &Term{emu: vt.NewEmulator(20, 5)}
	if s := tm.Status(); s != "starting" {
		t.Error(s)
	}
	tm.b = &pos{}
	if s := tm.Status(); s != "idle prompt" {
		t.Error(s)
	}
	tm.c, tm.cmd = &pos{}, "tail -f x"
	if s := tm.Status(); s != "running tail -f x" {
		t.Error(s)
	}
	tm.emu.Write([]byte("\x1b[?1049h"))
	if s := tm.Status(); s != "running tail -f x (full screen)" {
		t.Error(s)
	}
	tm.c = nil
	if s := tm.Status(); s != "full-screen program" {
		t.Error(s)
	}
	if tm.Cwd() != "" {
		t.Error("no cwd before OSC 7")
	}
	tm.cwd = "/srv"
	if tm.Cwd() != "/srv" {
		t.Error(tm.Cwd())
	}
	wide := &Term{emu: vt.NewEmulator(10, 2)}
	wide.emu.WriteString("漢x")
	if l := wide.line(0); !strings.HasPrefix(l, "漢x ") {
		t.Errorf("a wide char's second cell adds nothing: %q", l)
	}
}

// TestScreenSynchronized: a pane keeps its last screen while the program is
// mid synchronized update, and shows the whole new one once it ends.
func TestScreenSynchronized(t *testing.T) {
	tm, v := &Term{emu: vt.NewEmulator(10, 2)}, &termView{}
	first := func() string {
		_, runs, _, _, _, _ := v.screen(tm)
		var b strings.Builder
		for _, r := range runs[0] {
			b.WriteString(r.Text)
		}
		return strings.TrimSpace(b.String())
	}
	tm.emu.WriteString("old")
	tm.ver.Add(1)
	first()
	tm.emu.WriteString("\x1b[?2026h\x1b[2J\x1b[Hne")
	tm.ver.Add(1)
	if s := first(); s != "old" {
		t.Errorf("mid update: %q", s)
	}
	tm.emu.WriteString("w\x1b[?2026l")
	tm.ver.Add(1)
	if s := first(); s != "new" {
		t.Errorf("after: %q", s)
	}
}

func TestTermRows(t *testing.T) {
	tm := &Term{emu: vt.NewEmulator(10, 4)}
	tm.emu.WriteString("a\r\nb\r\nc")
	tm.blocks = []Block{{N: 1, Start: 0, End: 2}, {N: 2, Start: 2, End: 3, stale: true}}
	tm.b, tm.c = &pos{line: 2}, &pos{line: 2}
	var tinted []int
	tint := func(n int) color.Color {
		tinted = append(tinted, n)
		if n == 1 {
			return color.RGBA{1, 2, 3, 255}
		}
		return nil
	}
	rows, cx, cy, cur := tm.Rows(0, tint)
	if len(rows) != 4 || rows[0].N != 1 || !rows[0].First || rows[1].N != 1 || rows[1].First || rows[2].N != 0 {
		t.Errorf("blocks %+v", rows)
	}
	if rows[1].Running || !rows[2].Running || !rows[3].Running {
		t.Errorf("running %+v", rows)
	}
	if !strings.Contains(rows[0].Text, "48;2;1;2;3") || strings.Contains(rows[3].Text, "48;") || !strings.Contains(rows[1].Text, "b") {
		t.Errorf("tint %q %q", rows[0].Text, rows[3].Text)
	}
	if cx != 1 || cy != 2 || !cur || fmt.Sprint(tinted) != "[1 1 0 0]" {
		t.Errorf("cursor %d,%d %v tinted %v", cx, cy, cur, tinted)
	}
	if _, _, _, cur := tm.Rows(1, tint); cur {
		t.Error("scrolled back: no cursor")
	}
	tm.c, tm.typed = nil, true
	rows, _, _, _ = tm.Rows(0, func(int) color.Color { return nil })
	if rows[1].Typed || !rows[2].Typed || rows[2].Running {
		t.Errorf("typed %+v", rows)
	}
	tm.c = &pos{line: 2}
	tm.emu.WriteString("\x1b[?1049h\x1b[?25l")
	rows, _, _, cur = tm.Rows(5, func(int) color.Color { return nil })
	for _, r := range rows {
		if r.N != 0 || r.Running || r.Typed {
			t.Errorf("alt screen has no decoration: %+v", r)
		}
	}
	if cur {
		t.Error("hidden cursor")
	}
	tm.emu.WriteString("\x1b[?25h")
	if _, _, _, cur := tm.Rows(5, func(int) color.Color { return nil }); !cur {
		t.Error("the alt screen ignores scroll")
	}
}

func TestNewTermLocal(t *testing.T) {
	if wine() {
		t.Skip("Wine's powershell.exe is a stub, not PowerShell: no prompt function, no directory report")
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir()) // as the shell's pwd says it (macOS: /private/var)
	tm, err := NewTerm("local", dir, 80, 24, func() {}, paneState{})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end) && tm.Cwd() == ""; time.Sleep(50 * time.Millisecond) {
	}
	if tm.Host != "local" || tm.Cwd() != dir {
		t.Errorf("host %q cwd %q, want %q", tm.Host, tm.Cwd(), dir)
	}
}
