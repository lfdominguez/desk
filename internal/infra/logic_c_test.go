package infra

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/lfdominguez/desk/internal/testwait"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"gioui.org/io/key"
	"github.com/charmbracelet/x/ansi"

	deskorgs "github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/sshc"
	"github.com/lfdominguez/desk/internal/sshc/sshtest"
)

// --- helpers (suffix C: this file's) ---

// The real desktop notifier and opener, kept before TestMain swaps them out.
var realNotifyC, realOpenLocalC = desktopNotify, openLocal

// fakeBinC puts an executable name running body first on PATH; it returns
// the bin dir.
func fakeBinC(t *testing.T, name, body string) string {
	t.Helper()
	bin := t.TempDir()
	p := filepath.Join(bin, name)
	write(t, p, "#!/bin/sh\n"+body)
	os.Chmod(p, 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin
}

// waitFileC waits for p to have content and returns it.
func waitFileC(t *testing.T, p string) string {
	t.Helper()
	for range 200 {
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			return string(b)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never written", p)
	return ""
}

// stateC points XDG_STATE_HOME at a fresh temp dir.
func stateC(t *testing.T) string {
	d := t.TempDir()
	t.Setenv("XDG_STATE_HOME", d)
	return d
}

type exitC int

// catchExitC runs f with osExit caught: the code it exited with, or -1.
func catchExitC(t *testing.T, f func()) (code int) {
	t.Helper()
	old := osExit
	osExit = func(c int) { panic(exitC(c)) }
	defer func() {
		osExit = old
		code = -1
		if r := recover(); r != nil {
			e, ok := r.(exitC)
			if !ok {
				panic(r)
			}
			code = int(e)
		}
	}()
	f()
	return -1
}

// bwKeyC is a Bitwarden symmetric key (AES-256-CBC key, HMAC-SHA256 key).
type bwKeyC struct{ enc, mac []byte }

// encrypt makes a type 2 EncString, as Bitwarden clients do.
func (k bwKeyC) encrypt(plain string) string {
	n := aes.BlockSize - len(plain)%aes.BlockSize
	data := append([]byte(plain), bytes.Repeat([]byte{byte(n)}, n)...)
	iv := make([]byte, aes.BlockSize)
	rand.Read(iv)
	blk, _ := aes.NewCipher(k.enc)
	cipher.NewCBCEncrypter(blk, iv).CryptBlocks(data, data)
	h := hmac.New(sha256.New, k.mac)
	h.Write(iv)
	h.Write(data)
	b := base64.StdEncoding.EncodeToString
	return "2." + b(iv) + "|" + b(data) + "|" + b(h.Sum(nil))
}

// fakeBWC is a Vaultwarden whose account (master password "hunter2-fake")
// holds one login, "web1 root" (id c1): user root, password pw-fake. Any
// token request passes; a wrong password fails at the account key.
func fakeBWC(t *testing.T) deskorgs.Vault {
	const email, password, iter = "me@example.com", "hunter2-fake", 1000
	master, _ := pbkdf2.Key(sha256.New, password, []byte(email), iter, 32)
	enc, _ := hkdf.Expand(sha256.New, master, "enc", 32)
	mac, _ := hkdf.Expand(sha256.New, master, "mac", 32)
	raw := make([]byte, 64)
	rand.Read(raw)
	user := bwKeyC{raw[:32], raw[32:]}
	sync := map[string]any{
		"profile": map[string]any{"key": bwKeyC{enc, mac}.encrypt(string(raw))},
		"ciphers": []map[string]any{{"id": "c1", "name": user.encrypt("web1 root"),
			"login": map[string]string{"username": user.encrypt("root"), "password": user.encrypt("pw-fake")}},
			{"id": "c2", "name": user.encrypt("broken user"), // its username doesn't decrypt
				"login": map[string]string{"username": "2.AAAA|AAAA|AAAA", "password": user.encrypt("pw2-fake")}}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/identity/accounts/prelogin":
			json.NewEncoder(w).Encode(map[string]int{"kdf": 0, "kdfIterations": iter})
		case "/identity/connect/token":
			json.NewEncoder(w).Encode(map[string]any{"access_token": "T", "expires_in": 3600})
		case "/api/sync":
			json.NewEncoder(w).Encode(sync)
		default:
			http.Error(w, "unexpected", 404)
		}
	}))
	t.Cleanup(srv.Close)
	return deskorgs.Vault{Name: "bw", Kind: "vaultwarden", URL: srv.URL, Email: email}
}

// --- audit.go ---

func TestStateDirDefaultC(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows' home
	if got := stateDir(); got != filepath.Join(home, ".local", "state", "desk", "infra") {
		t.Errorf("stateDir: %s", got)
	}
}

func TestAuditDropsWriteErrorsC(t *testing.T) {
	st := stateC(t)
	write(t, filepath.Join(st, "desk", "infra", "audit", "Acme"), "not a dir")
	audit(auditEntry{Org: "Acme", Event: "command", Cmd: "ls"}) // must not panic
	if es := auditSince("Acme", time.Now().Add(-time.Hour)); len(es) != 0 {
		t.Errorf("nothing can be logged: %v", es)
	}
}

func TestBlockDoneLongNotifiesC(t *testing.T) {
	stateC(t)
	got := make(chan blockDoneMsg, 2)
	setDispatchA(t, func(m tea.Msg) {
		if b, ok := m.(blockDoneMsg); ok {
			got <- b
		}
	})
	tab := &Tab{org: "Acme"}
	blockDone(tab, "web1", Block{Cmd: "true", Dur: longCommand - time.Second})
	blockDone(tab, "web1", Block{Cmd: "make", Dur: longCommand})
	select {
	case m := <-got:
		if m.tab != tab || m.host != "web1" || m.block.Cmd != "make" {
			t.Errorf("msg: %+v", m)
		}
	case <-time.After(testwait.Long):
		t.Fatal("a long command must be dispatched")
	}
	select {
	case m := <-got:
		t.Errorf("a short command dispatched too: %+v", m)
	case <-time.After(50 * time.Millisecond):
	}
	if es := auditSince("Acme", time.Now().Add(-time.Minute)); len(es) != 2 || es[1].Cmd != "make" {
		t.Errorf("both logged: %+v", es)
	}
}

func TestTailLinesCutsC(t *testing.T) {
	if got := tailLines("a\nb\nc\n", 2, 100); got != "b\nc" {
		t.Errorf("lines: %q", got)
	}
	if got := tailLines("abcdef", 5, 3); got != "def" {
		t.Errorf("bytes: %q", got)
	}
	if got := tailLines("ab", 2, 2); got != "ab" {
		t.Errorf("fits: %q", got)
	}
}

func TestRecorderErrorsAndPruneSkipsDirsC(t *testing.T) {
	st := stateC(t)
	f := filepath.Join(st, "file")
	write(t, f, "x")
	if _, err := newRecorder(filepath.Join(f, "sub", "a.cast"), 80, 24); err == nil {
		t.Error("a recording under a file must fail")
	}
	sub := filepath.Join(st, "desk", "infra", "recordings", "Acme", "sub")
	os.MkdirAll(sub, 0o700)
	old := time.Now().Add(-30 * 24 * time.Hour)
	os.Chtimes(sub, old, old)
	pruneRecordings(time.Now())
	if _, err := os.Stat(sub); err != nil {
		t.Error("a directory among recordings is not one: kept")
	}
}

// --- notes.go ---

func TestNotesErrorsAndCapC(t *testing.T) {
	st := stateC(t)
	write(t, filepath.Join(st, "desk", "infra", "notes", "Acme"), "not a dir")
	if err := addNote("Acme", "web1", "configs in /srv"); err == nil {
		t.Error("an unwritable notes dir must fail")
	}
	long := strings.Repeat("- old note\n", maxNotes/11+10) + "- newest\n"
	write(t, notesPath("Globex", "web1"), long)
	tab := &Tab{org: "Globex"}
	got := tab.notesFor("web1")
	if !strings.HasPrefix(got, "<notes server=\"web1\">\n…\n") || !strings.HasSuffix(got, "- newest\n</notes>\n") ||
		len(got) > maxNotes+60 {
		t.Errorf("capped notes: %d %q…", len(got), got[:min(60, len(got))])
	}
}

func TestChangesToolC(t *testing.T) {
	needSh(t)
	stateC(t)
	w := newSSHW(t, sshtest.Opts{}, "    SetEnv PATH="+stubPath(t)+"\n")
	term, _ := fakeTermA(t, "web-01")
	p := &Pane{id: 1, term: term, name: "web-01", dir: w.dir}
	tab := &Tab{org: "Acme"}
	r := tab.changes(p, 0)
	if r.IsError || !strings.HasPrefix(r.Text, "changes on web-01 in the last 1h:\n== packages\n== files changed under /etc\n") {
		t.Errorf("changes: %+v", r)
	}
	if cs := w.srv.Commands(); len(cs) != 1 || !strings.HasPrefix(cs[0], "sh -c 'sh -c '") || !strings.HasSuffix(cs[0], " changes 1'") {
		t.Errorf("ran %q", cs)
	}
	if es := auditSince("Acme", time.Now().Add(-time.Minute)); len(es) != 1 || es[0].Cmd != "(changes tool, last 1h)" || es[0].By != "claude" {
		t.Errorf("audited: %+v", es)
	}
	if r := tab.changes(p, 5000); !strings.Contains(r.Text, "last 720h") {
		t.Errorf("capped at 30 days: %q", r.Text)
	}
	w.config("    SetEnv PATH=" + t.TempDir() + "\n") // no sh there: the script can't run
	sshPool = &sshc.Pool{}
	if r := tab.changes(p, 2); !r.IsError || !strings.HasPrefix(r.Text, "changes: ") || !strings.Contains(r.Text, "not found") {
		t.Errorf("failed: %+v", r)
	}
}

// --- notify.go ---

// TestDesktopNotifyC: the notifier gets the title and the body's first
// line, off the caller's goroutine (no notify-send: D-Bus, as desk's own).
func TestDesktopNotifyC(t *testing.T) {
	got := make(chan string, 1)
	defer func(f func(string, string)) { pushNotify = f }(pushNotify)
	pushNotify = func(title, body string) { got <- title + "|" + body }
	realNotifyC("Approval needed", "  systemctl restart nginx\nsecond line")
	select {
	case g := <-got:
		if g != "Approval needed|systemctl restart nginx" {
			t.Errorf("pushed %q", g)
		}
	case <-time.After(testwait.Long):
		t.Fatal("nothing pushed")
	}
}

func TestOpenLocalRunsXdgOpenC(t *testing.T) {
	needSh(t)
	out := filepath.Join(t.TempDir(), "out")
	fakeBinC(t, Opener, `printf '%s' "$1" > `+out+".tmp; mv "+out+".tmp "+out+"\n")
	if err := realOpenLocalC("/tmp/report.md"); err != nil {
		t.Fatal(err)
	}
	if got := waitFileC(t, out); got != "/tmp/report.md" {
		t.Errorf("opened: %q", got)
	}
}

// --- report.go ---

func TestIncidentReportEntriesC(t *testing.T) {
	st := stateC(t)
	since := time.Now().Add(-24 * time.Hour) // yesterday's file is missing: skipped
	exit := 2
	audit(auditEntry{Org: "Acme", Host: "web1", Event: "approval", Cmd: "systemctl restart nginx", Verdict: "approved"})
	audit(auditEntry{Org: "Acme", Host: "web1", Event: "command", By: "claude", Cmd: "false", Exit: &exit, Output: "boom"})
	tab := &Tab{org: "Acme", name: "web1", orgOnly: true, opened: since,
		chat: []*chatEntry{{who: "tool", text: "run uptime"}, {who: "error", text: "it broke"}}}
	r := incidentReport(tab, time.Now())
	for _, want := range []string{"# Session report: Acme\n", "- ▸ `run uptime`", "> ✗ it broke",
		"· web1 · approval **approved**: `systemctl restart nginx`", "· web1 · claude · exit 2 · 0s: `false`", "  boom\n"} {
		if !strings.Contains(r, want) {
			t.Errorf("missing %q in:\n%s", want, r)
		}
	}
	write(t, filepath.Join(st, "desk", "infra", "reports"), "not a dir")
	if _, err := saveReport(tab); err == nil {
		t.Error("an unwritable reports dir must fail")
	}
}

// --- runbook.go ---

func TestSaveRunbookUnwritableC(t *testing.T) {
	infra := filepath.Join(t.TempDir(), "Acme", infraDir)
	write(t, filepath.Join(infra, "runbooks"), "not a dir")
	if _, err := saveRunbook(&Tab{dir: infra}, "Deploy", "# Deploy\n"); err == nil {
		t.Error("an unwritable runbooks dir must fail")
	}
}

// --- vaults.go ---

func TestHashiVaultErrorsC(t *testing.T) {
	ctx := context.Background()
	t.Setenv("VAULT_TOKEN", "")
	if _, err := (hashiVault{VaultCfg{Name: "hv"}}).search(ctx, ""); err == nil || !strings.Contains(err.Error(), "no token") {
		t.Errorf("no token: %v", err)
	}
	t.Setenv("VAULT_TOKEN", "tok")
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(403)
	}))
	h := hashiVault{VaultCfg{Name: "hv", URL: srv.URL}}
	if _, err := h.get(ctx, "db/root"); err == nil || !strings.Contains(err.Error(), "vault hv: 403 Forbidden for secret/data/db/root") {
		t.Errorf("get: %v", err)
	}
	if _, err := h.search(ctx, ""); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("search: %v", err)
	}
	if len(paths) != 2 || paths[0] != "/v1/secret/data/db/root" || paths[1] != "/v1/secret/metadata/" {
		t.Errorf("default mount is secret: %v", paths)
	}
	srv.Close()
	if err := h.call(ctx, "x", nil); err == nil {
		t.Error("a closed server must fail")
	}
	if err := (hashiVault{VaultCfg{Name: "hv", URL: "http://bad\x7f"}}).call(ctx, "x", nil); err == nil {
		t.Error("a bad URL must fail")
	}
}

// --- claude.go ---

// profileHomeC is a HOME with an Acme Claude profile, DESK_ROOT inside it,
// and PATH = a bin dir holding only the named fake programs.
func profileHomeC(t *testing.T, progs ...string) (home, bin string) {
	home, bin = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows' home
	t.Setenv("CLAUDE_PROFILE", "")
	t.Setenv("DESK_ROOT", filepath.Join(home, "Work"))
	t.Setenv("PATH", bin)
	for _, n := range progs {
		write(t, filepath.Join(bin, n), "#!/bin/sh\n")
		os.Chmod(filepath.Join(bin, n), 0o755)
	}
	os.MkdirAll(filepath.Join(home, "Secure", "Claudes", "Acme"), 0o755)
	return home, bin
}

func TestClaudeCmdNixBwrapC(t *testing.T) {
	skipOnWindows(t, "bwrap is Linux's: Windows takes NoBwrap's (TestClaudeCmdNoBwrap)")
	home, _ := profileHomeC(t, "claude")
	dir := filepath.Join(home, "Work", "Acme", infraDir)
	store := t.TempDir()
	bw := filepath.Join(store, "abc-bubblewrap-0.11", "bin", "bwrap")
	write(t, bw, "#!/bin/sh\n")
	old := bwrapGlob
	t.Cleanup(func() { bwrapGlob = old })
	bwrapGlob = filepath.Join(store, "*-bubblewrap-*", "bin", "bwrap")
	cmd, err := claudeCmd(dir, "-p")
	if err != nil || cmd.Path != bw {
		t.Fatalf("store bwrap: %v %v", cmd, err)
	}
	bwrapGlob = filepath.Join(store, "none-*")
	if _, err := claudeCmd(dir, "-p"); err == nil || err.Error() != "profile Acme needs bwrap (bubblewrap) in PATH" {
		t.Errorf("no bwrap anywhere: %v", err)
	}
}

func TestClaudeCmdSeedUnwritableC(t *testing.T) {
	home, _ := profileHomeC(t, "claude", "bwrap")
	profiles := filepath.Join(home, "Secure", "Claudes")
	os.Chmod(profiles, 0o500)
	t.Cleanup(func() { os.Chmod(profiles, 0o755) })
	if _, err := claudeCmd(filepath.Join(home, "Work", "Acme", infraDir)); err == nil {
		t.Error("an unwritable profiles dir must fail")
	}
}

func TestStartClaudeFailuresC(t *testing.T) {
	noUnix(t)
	f := filepath.Join(t.TempDir(), "file")
	write(t, f, "x")
	t.Setenv("XDG_RUNTIME_DIR", f)
	tab := &Tab{org: "Acme", orgCfg: &orgConfig{}}
	if c, err := StartClaude(t.TempDir(), "", func(claudeMsg) {}, tab); err == nil || c != nil {
		t.Error("no socket for the tools must fail")
	}
	runtimeA(t)
	home, _ := profileHomeC(t) // a profile, and no claude on PATH
	dir := filepath.Join(home, "Work", "Acme", infraDir)
	if c, err := StartClaude(dir, "", func(claudeMsg) {}, tab); err == nil || c != nil || !strings.Contains(err.Error(), "claude") {
		t.Errorf("no claude: %v", err)
	}
	var got claudeMsg
	compactSession(dir, "", "sess-1", func(m claudeMsg) { got = m }, tab)
	if got.tab != tab || !got.done || got.err == nil || !strings.Contains(got.err.Error(), "claude") {
		t.Errorf("compact without claude: %+v", got)
	}
}

// --- files.go ---

func TestFilesErrorsC(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := listRemote(ctx, "local", "", t.TempDir()); err == nil || !strings.HasPrefix(err.Error(), "listing ") || !strings.Contains(err.Error(), " on local: ") {
		t.Errorf("a failed listing: %v", err)
	}
	gone := t.TempDir()
	t.Chdir(gone)
	os.Remove(gone)
	if _, _, err := listLocal("rel"); err == nil {
		t.Error("a relative path with no working directory must fail")
	}
	var n atomic.Int64
	err := copyFromRemote(context.Background(), "local", "", t.TempDir(), filepath.Join(t.TempDir(), "missing"), fileEntry{Name: "a"}, &n, func() {})
	if err == nil {
		t.Error("copying into a missing folder must fail")
	}
}

// --- servers.go ---

func TestServersParseErrorsC(t *testing.T) {
	needSh(t)
	org := filepath.Join(t.TempDir(), "Acme")
	infra := filepath.Join(org, infraDir)
	os.MkdirAll(infra, 0o755)
	if hs := serverHosts(infra); hs != nil {
		t.Error("an org without a servers file has no server hosts")
	}
	self := filepath.Join(org, "loop.conf")
	write(t, self, "Include "+self+"\nHost a\n")
	hs, err := parseServers(self, org, false)
	if err == nil || !strings.Contains(err.Error(), "includes nest too deep") || len(hs) != 1 || hs[0].alias != "a" {
		t.Errorf("self include: %v %v", hs, err)
	}
	os.MkdirAll(filepath.Join(org, "conf.d"), 0o755)
	write(t, filepath.Join(org, "fail.sh"), "#!/bin/sh\nexit 1\n")
	os.Chmod(filepath.Join(org, "fail.sh"), 0o755)
	write(t, filepath.Join(org, "gen.sh"), "#!/bin/sh\nprintf 'Host gen1\\n  HostName 10.0.0.1\\n'\n")
	os.Chmod(filepath.Join(org, "gen.sh"), 0o755)
	file := filepath.Join(org, ".desk", "ssh_config")
	write(t, file, "Include "+filepath.Join(org, "conf.d")+"\n#@ hosts-cmd ./fail.sh\n#@ group gen\n#@ hosts-cmd ./gen.sh\nHost b\n")
	_, err = parseServers(file, org, true)
	if err == nil || !strings.Contains(err.Error(), "is a directory") || !strings.Contains(err.Error(), "hosts-cmd fail.sh: exit status 1") {
		t.Errorf("errors: %v", err)
	}
	if b, _ := os.ReadFile(dynamicFile(org, "gen")); string(b) != "Host gen1\n  HostName 10.0.0.1\n" {
		t.Errorf("ssh_config output kept as is: %q", b)
	}
	tree := serverTree("Acme", infra, file)
	if len(tree) < 2 || !strings.HasPrefix(tree[1].label, "(ssh_config: ") {
		t.Errorf("the error shows in the tree: %+v", tree)
	}
}

func TestServersSmallHelpersC(t *testing.T) {
	if u, h, p := splitDest("admin@[2001:db8::1]:2222"); u != "admin" || h != "2001:db8::1" || p != "2222" {
		t.Errorf("bracketed: %q %q %q", u, h, p)
	}
	if kw, rest := sshKeyword("Host"); kw != "host" || rest != "" {
		t.Errorf("keyword alone: %q %q", kw, rest)
	}
	if got := unquote(`"a\qb"`); got != `a\qb` {
		t.Errorf("bad escape keeps the text: %q", got)
	}
}

// --- monitor.go ---

func TestMonitorErrorsC(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Authorization"), "Digest ") {
			c, _, _ := w.(http.Hijacker).Hijack()
			c.Close()
			return
		}
		if strings.Contains(r.URL.Path, "nagios") {
			w.Header().Set("WWW-Authenticate", `Digest realm="nagios", nonce="n1", qop="auth"`)
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(500)
	}))
	defer srv.Close()
	o := &orgConfig{Metrics: []SourceCfg{{Name: "m", URL: srv.URL}}, logins: map[string][2]string{"n": {"nagiosadmin", "pw"}}}
	s := SourceCfg{Name: "n", URL: srv.URL, secret: &deskorgs.Secret{Vault: "v", Item: "x"}}
	if _, err := o.get(ctx, s, srv.URL+"/nagios/x"); err == nil {
		t.Error("a dropped digest retry must fail")
	}
	if _, err := o.metrics(ctx, "", "", "", "", "", "up"); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("metric names: %v", err)
	}
	if _, err := o.metrics(ctx, "", "up", "", "", "", ""); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("query: %v", err)
	}
}

func TestReportAndNotifySmallC(t *testing.T) {
	stateC(t)
	r := incidentReport(&Tab{org: "Acme", name: "web1", opened: time.Now(), chat: []*chatEntry{{who: "note", text: "restored"}}}, time.Now())
	if !strings.Contains(r, "# Session report: Acme · web1\n") || !strings.Contains(r, "_restored_\n") || !strings.HasSuffix(r, "_none recorded_\n") {
		t.Errorf("report:\n%s", r)
	}
	if got := firstLine("abcdef", 4); got != "abc…" {
		t.Errorf("cut: %q", got)
	}
}

// --- session.go ---

func TestSaveSessionKeepsC(t *testing.T) {
	stateC(t)
	dropped, _ := fakeTermA(t, "web1")
	dropped.emu.WriteString("a failed retry")
	tab := &Tab{org: "Acme", name: "web1", panes: []*Pane{{id: 1, term: dropped, name: "web1", lost: time.Now(), kept: paneState{Text: "before the drop"}}}}
	for i := range keptChat + 5 {
		tab.chat = append(tab.chat, &chatEntry{who: "you", text: fmt.Sprint(i)})
	}
	fwd := portFwd{host: "web1", dir: "/d", name: "web1", kind: "L", spec: "8080:localhost:80"}
	m := &model{restored: true, tabs: []*Tab{tab}, forwards: []portFwd{fwd}}
	m.saveSession()
	b, err := os.ReadFile(sessionPath())
	if err != nil {
		t.Fatal(err)
	}
	var ss savedSession
	json.Unmarshal(b, &ss)
	if len(ss.Forwards) != 1 || ss.Forwards[0] != (savedFwd{"web1", "/d", "web1", "L", "8080:localhost:80"}) {
		t.Errorf("forwards: %+v", ss.Forwards)
	}
	st := ss.Tabs[0]
	if st.Panes[0].State.Text != "before the drop" || len(st.Chat) != keptChat || st.Chat[0].Text != "5" {
		t.Errorf("pane %q, %d chat from %q", st.Panes[0].State.Text, len(st.Chat), st.Chat[0].Text)
	}
}

func TestRestoreSessionC(t *testing.T) {
	stateC(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir()) // Windows' home
	var mu sync.Mutex
	var hosts []string
	var cmd []string // nil: the test binary sleeping (infraHelper)
	HostCmd = func(h string) *exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		hosts = append(hosts, h)
		if cmd == nil {
			return infraHelper("sleep")
		}
		return exec.Command(cmd[0], cmd[1:]...)
	}
	t.Cleanup(func() { HostCmd = nil })
	dir := t.TempDir()
	saved := time.Date(2026, 1, 2, 15, 4, 0, 0, time.Local)
	ss := savedSession{Saved: saved, Active: 9, Forwards: []savedFwd{{"web1", dir, "web1", "L", "8080:localhost:80"}},
		Tabs: []savedTab{
			{Org: "Acme", Dir: dir, Name: "web1", Chat: []savedChat{{Who: "note", Text: restoredNote + "Jan 1 10:00 —"}, {Who: "you", Text: "uptime?"}},
				Panes: []savedPane{{Host: "web1", Dir: dir, Label: "web1", State: paneState{Text: "$ uptime\nup 3 days\n\n── restored from Jan 1 10:00 ──"}}}},
			{Org: "Acme", Dir: dir, Name: "web2", Panes: []savedPane{{Host: "web2", Dir: dir, Label: "web2"}}},
		}}
	b, _ := json.Marshal(ss)
	write(t, sessionPath(), string(b))
	m := &model{}
	m.restore()
	defer func() {
		for _, tb := range m.tabs {
			for _, p := range tb.panes {
				p.term.Close()
			}
		}
	}()
	if len(m.tabs) != 2 || m.active() != 1 || len(m.forwards) != 1 || m.forwards[0].spec != "8080:localhost:80" {
		t.Fatalf("restored: %d tabs, active %d, forwards %v", len(m.tabs), m.active(), m.forwards)
	}
	t1 := m.tabs[0]
	if len(t1.chat) != 2 || t1.chat[0].text != "uptime?" || !strings.HasPrefix(t1.chat[1].text, restoredNote+"Jan 2 15:04") {
		t.Errorf("one restore note: %+v %+v", t1.chat[0], t1.chat[len(t1.chat)-1])
	}
	if len(t1.panes) != 1 || t1.panes[0].term.Host != "web1" {
		t.Fatalf("panes: %+v", t1.panes)
	}
	if k := t1.panes[0].term.Keep(); k.Text != "$ uptime\nup 3 days\n\n── restored from Jan 2 15:04 ──" {
		t.Errorf("one restored line: %q", k.Text)
	}
	mu.Lock()
	if strings.Join(hosts, ",") != "web1,web2" {
		t.Errorf("dialed %v", hosts)
	}
	cmd = []string{"/no/such/shell"} // the pane can't start
	mu.Unlock()
	m.restore() // once
	if len(m.tabs) != 2 {
		t.Error("restore runs once")
	}
	m2 := &model{}
	m2.restore()
	if len(m2.tabs) != 2 || len(m2.tabs[0].panes) != 0 || !m2.tabs[0].orgOnly || m2.errText == "" {
		t.Errorf("a pane that won't start leaves an org tab: %+v %q", m2.tabs[0], m2.errText)
	}
}

// --- sshcfg.go ---

func TestMCPMainNoSocketC(t *testing.T) {
	if code := catchExitC(t, func() { MCPMain(nil) }); code != 2 {
		t.Errorf("desk mcp without a socket: exit %d", code)
	}
}

// --- term.go ---

// markedTermC is a fake term reading its program's OSC marks.
func markedTermC(t *testing.T) *Term {
	term, _ := fakeTermA(t, "web1")
	term.b = nil
	term.emu.RegisterOscHandler(133, term.osc133)
	term.emu.RegisterOscHandler(7, term.osc7)
	term.emu.RegisterOscHandler(6973, term.oscEnv)
	return term
}

func TestTermMarksC(t *testing.T) {
	term := markedTermC(t)
	term.emu.WriteString("\x1b]6973;!!not base64\x07\x1b]133\x07")
	if term.env != nil || term.integrated {
		t.Fatalf("bad marks are ignored: env %v integrated %v", term.env, term.integrated)
	}
	var done []Block
	term.OnBlock(func(b Block) { done = append(done, b) })
	term.emu.WriteString("$ \x1b]133;B\x07ls\r\n\x1b]133;C\x07a\r\nb\x1b]133;D;3\x07")
	if len(done) != 1 || done[0].Cmd != "ls" || done[0].Exit != 3 || done[0].Output != "a\nb" || done[0].End != 3 {
		t.Fatalf("block: %+v", done)
	}
	term.emu.WriteString("\x1b[H\x1b]133;B\x07") // a prompt above the block: it was cleared away
	if !term.Blocks()[0].stale {
		t.Error("a block below a new prompt goes stale")
	}
	if got := term.text(pos{0, 0}, pos{1, 0}); got != "$ ls\n" && got != "$ ls" {
		t.Errorf("text to a line start: %q", got)
	}
}

func TestTermWideAndAltC(t *testing.T) {
	term := markedTermC(t)
	term.emu.WriteString("\x1b]133;B\x07$ 世界\x1b]133;C\x07") // a command running
	if l := strings.TrimRight(term.line(0), " "); l != "$ 世界" {
		t.Errorf("wide line: %q", l)
	}
	if r := term.renderLine(0, 0, 40, nil); !strings.Contains(r, "世界") || strings.Count(r, " ") > 40 {
		t.Errorf("rendered: %q", r)
	}
	runs, rows, _, _, _ := term.Grid(0)
	if !rows[0].Running || !rows[0].First {
		t.Errorf("the running command's first row: %+v", rows[0])
	}
	var row0 strings.Builder
	for _, r := range runs[0] {
		row0.WriteString(r.Text)
	}
	if !strings.HasPrefix(row0.String(), "$ 世界") {
		t.Errorf("grid row: %q", row0.String())
	}
	term.emu.WriteString("\x1b[?1049hhtop")
	if _, rows, _, _, _ := term.Grid(3); rows[0].Running {
		t.Error("the alt screen has no block decoration")
	}
}

func TestTermHistoryAndTapC(t *testing.T) {
	needSh(t)
	term := markedTermC(t)
	term.pending = &paneState{Text: "$ x\nout", Blocks: []Block{{Start: -1, End: 1}, {Start: 1, End: 1}, {N: 1, Cmd: "x", Start: 0, End: 2}}}
	term.writeHistory()
	if bs := term.Blocks(); len(bs) != 1 || bs[0].Cmd != "x" {
		t.Errorf("bad block ranges are dropped: %+v", bs)
	}
	got := make(chan string, 8)
	old := termTap
	termTap = func(_ *Term, b []byte) { got <- string(b) }
	defer func() { termTap = old }()
	tt, err := startCmd("local", exec.Command("sh", "-c", "printf tapped; sleep 1"), 40, 6, func() {}, paneState{})
	if err != nil {
		t.Fatal(err)
	}
	defer tt.Close()
	select {
	case s := <-got:
		if !strings.Contains(s, "tapped") {
			t.Errorf("tap: %q", s)
		}
	case <-time.After(testwait.Long):
		t.Fatal("termTap never called")
	}
}

func TestTermRunCutsLongOutputC(t *testing.T) {
	term, r := fakeTermA(t, "web1")
	go func() {
		readPipeA(r) // the command typed
		var out []string
		for i := range 400 {
			out = append(out, fmt.Sprint(i))
		}
		term.mu.Lock()
		term.blocks = append(term.blocks, Block{N: 1, Exit: 1, Output: strings.Join(out, "\n")})
		term.mu.Unlock()
	}()
	res := term.Run("seq 400", 5*time.Second)
	if !res.IsError || !strings.HasPrefix(res.Text, "block #1 exit=1\n[…100 lines cut]\n100\n") || !strings.HasSuffix(res.Text, "\n399") {
		t.Errorf("run: %q…", res.Text[:min(60, len(res.Text))])
	}
}

// --- alerts.go ---

func TestStartAlertsPollsC(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("query") {
		case "hostlist":
			w.Write([]byte(`{"data":{"hostlist":{"db1":{"status":4,"plugin_output":"PING CRITICAL"}}}}`))
		default:
			w.Write([]byte(`{"data":{"servicelist":{}}}`))
		}
	}))
	defer srv.Close()
	bad := orgWith(t, "[[[ broken")
	good := orgWith(t, "[[nagios]]\nname = \"mon\"\nurl = \""+srv.URL+"\"\nalerts = true\n[[nagios]]\nname = \"quiet\"\nurl = \"http://192.0.2.1\"\n")
	got := make(chan alertsMsg, 4)
	setDispatchA(t, func(m tea.Msg) {
		if a, ok := m.(alertsMsg); ok {
			got <- a
		}
	})
	startAlerts([]node{{depth: 0, label: "(none)"}, {depth: 1, label: "g", dir: good}, {depth: 0, org: "Bad", dir: bad}, {depth: 0, org: "Acme", dir: good}})
	select {
	case a := <-got:
		if a.src != "Acme/mon" || !a.first || len(a.alerts) != 1 || a.alerts[0].Host != "db1" || a.alerts[0].State != "DOWN" || a.alerts[0].Dir != good {
			t.Errorf("alerts: %+v", a)
		}
	case <-time.After(testwait.Long):
		t.Fatal("no poll")
	}
	select {
	case a := <-got:
		t.Errorf("only sources with alerts = true poll: %+v", a)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestNagiosAlertsNewestFirstC(t *testing.T) {
	as := nagiosAlerts("Acme", "/d", map[string]nagiosObj{"old": {Status: 4, LastChange: 1000}, "new": {Status: 4, LastChange: 2000}}, nil)
	if len(as) != 2 || as[0].Host != "new" || as[1].Host != "old" {
		t.Errorf("same state: newest first: %+v", as)
	}
}

func TestHostAlertOtherOrgC(t *testing.T) {
	m := &model{alerts: map[string][]nagiosAlert{"x": {{Org: "Globex", Host: "web1", State: "DOWN"}, {Org: "Acme", Host: "web1", State: "CRITICAL"}}}}
	if got := m.hostAlert(node{org: "Acme", label: "web1"}); got != "CRITICAL" {
		t.Errorf("another org's alert counts: %q", got)
	}
}

func TestInvestigateC(t *testing.T) {
	noUnix(t)
	stateC(t)
	runtimeA(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir()) // Windows' home
	t.Setenv("PATH", t.TempDir())        // no claude
	a := nagiosAlert{Org: "Acme", Host: "db1", State: "DOWN", Output: "PING CRITICAL", Since: time.Now()}
	var seen []string
	m := &model{onInvestigate: func(org, host, service, state, output string, _ time.Time) {
		seen = append(seen, org+" "+host+" "+state)
	}}
	m.investigate(a)
	if len(seen) != 1 || seen[0] != "Acme db1 DOWN" || len(m.tabs) != 0 {
		t.Errorf("desk's handler: %v", seen)
	}
	m = &model{tree: []node{{depth: 0, label: "Acme", org: "Acme", dir: t.TempDir()}}}
	m.investigate(nagiosAlert{Org: "Globex"})
	if len(m.tabs) != 0 {
		t.Error("an alert of an org not in the tree opens nothing")
	}
	m.investigate(a)
	tab := m.tab()
	if tab == nil || !tab.orgOnly || len(tab.chat) != 1 || tab.chat[0].who != "error" || !strings.Contains(tab.chat[0].text, "claude") {
		t.Fatalf("no claude: %+v", tab)
	}
	tab.busy = true
	m.investigate(a)
	if len(tab.asked) != 1 || !strings.Contains(tab.asked[0], "Nagios alert: DOWN db1 since ") || lastChatA(tab) != "queued: investigate db1" {
		t.Errorf("busy: %q %q", tab.asked, lastChatA(tab))
	}
}

func TestHealthSkipsAndRunsC(t *testing.T) {
	stateC(t)
	runtimeA(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir()) // Windows' home
	c, rec := claudeA(t)
	t.Setenv("PATH", t.TempDir()) // no claude
	bad, none, odd := orgWith(t, "[[[ broken"), orgWith(t, ""), orgWith(t, "[schedule]\nhealth = \"half past eight\"\n")
	acme, globex := orgWith(t, "[schedule]\nhealth = \"08:30\"\n"), orgWith(t, "[schedule]\nhealth = \"08:30\"\n")
	m := &model{tree: []node{{depth: 0, org: "Bad", dir: bad}, {depth: 1, org: "Acme", dir: acme}, {depth: 0, org: "None", dir: none},
		{depth: 0, org: "Odd", dir: odd}, {depth: 0, org: "Acme", label: "Acme", dir: acme}, {depth: 0, org: "Globex", label: "Globex", dir: globex}}}
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.Local)
	if due := m.dueHealth(now); len(due) != 2 || due[0].org != "Acme" || due[1].org != "Globex" {
		t.Fatalf("due: %+v", due)
	}
	busy := &Tab{org: "Globex", orgOnly: true, dir: globex, busy: true}
	acmeTab := &Tab{org: "Acme", orgOnly: true, dir: acme, claude: c, orgCfg: &orgConfig{}}
	m.tabs = []*Tab{acmeTab, busy}
	m.runHealth(now)
	if !acmeTab.health || !strings.Contains(rec.String(), "Daily health check of Acme") || lastChatA(acmeTab) != "☀ daily health check" {
		t.Errorf("acme: health %v, sent %q", acmeTab.health, rec.String())
	}
	if busy.health || len(busy.chat) != 0 {
		t.Error("a busy tab waits for the next tick")
	}
	if b, _ := os.ReadFile(healthStamp("Acme")); string(b) != "2026-10-26" {
		t.Errorf("stamp: %q", b)
	}
	busy.busy, busy.orgCfg = false, &orgConfig{} // no claude to start
	m.runHealth(now)
	if busy.health || len(busy.chat) == 0 || lastChatA(busy) == "" || busy.chat[len(busy.chat)-1].who != "error" {
		t.Errorf("a failed send shows: %+v", busy.chat)
	}
}

// --- mcp.go ---

func TestMCPMainAndListenErrorsC(t *testing.T) {
	r, w, _ := os.Pipe()
	w.WriteString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}` + "\n")
	w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	_, out, _ := captureA(t, func() int { MCPMain([]string{"/nonexistent.sock", "nagios"}); return 0 })
	if !strings.Contains(out, `"id":1`) || !strings.Contains(out, "2025-06-18") {
		t.Errorf("initialize answered on stdout: %q", out)
	}
	f := filepath.Join(t.TempDir(), "file")
	write(t, f, "x")
	t.Setenv("XDG_RUNTIME_DIR", f)
	if _, err := ServeCards(func(string, string, json.RawMessage) (string, bool) { return "", true }); err == nil {
		t.Error("no runtime dir: no cards socket")
	}
}

type getErrVaultC struct{}

func (getErrVaultC) search(context.Context, string) ([]vaultItem, error) {
	return []vaultItem{{ID: "i1", Name: "db", Username: "root"}}, nil
}
func (getErrVaultC) get(context.Context, string) (vaultItem, error) {
	return vaultItem{}, fmt.Errorf("item gone")
}

func TestCallToolDispatchC(t *testing.T) {
	needSh(t)
	stateC(t)
	hub.Lock()
	oldCards := cards
	cards = Cards{}
	hub.Unlock()
	defer func() { hub.Lock(); cards = oldCards; hub.Unlock() }()
	var asked []tea.Msg
	setDispatchA(t, func(m tea.Msg) {
		asked = append(asked, m)
		switch m := m.(type) {
		case paneMsg:
			m.done <- nil
		case approvalMsg:
			m.a.done <- approvalResult{ok: true, cmd: m.a.cmd}
		}
	})
	term, _ := fakeTermA(t, "web1")
	term.emu.WriteString("$ ")
	term.reached = true
	many := fakeVault{}
	for i := range 201 {
		many[fmt.Sprint(i)] = vaultItem{ID: fmt.Sprint(i), Name: "item"}
	}
	tab := &Tab{org: "Acme", term: term, panes: []*Pane{{id: 1, term: term, name: "web1", dir: t.TempDir()}},
		hosts: []node{{host: "web2", label: "web2", group: "g", dir: "/d"}},
		orgCfg: &orgConfig{Vaults: []VaultCfg{{Name: "many", Type: "passbolt"}, {Name: "gone", Type: "passbolt"}, {Name: "cli", Type: "bw-cli"}},
			vaults: map[string]vault{"many": many, "gone": getErrVaultC{}}}}
	call := func(name, args string) toolResult {
		return tab.callTool(toolCall{Name: name, Args: json.RawMessage(args)})
	}
	for _, c := range []struct{ name, args, want string }{
		{"ask_peer", `{}`, "no peer Claude has contacted this session, so there is nobody to ask"},
		{"message_card", `{"card":"DESK-1","text":"hi"}`, "no desk board here"},
		{"create_card", `{"title":"x"}`, "no desk board here"},
		{"connect", `{"host":"web2"}`, "could not open a pane on web2"},
		{"split", `{"pane":1}`, "could not open a pane on web1"},
		{"nagios", `{}`, `no nagios source ""`},
		{"metrics", `{"query":"up"}`, `no metrics source ""`},
		{"secrets_search", `{"query":""}`, `vault cli: unknown type "bw-cli"`},
		{"secret_get", `{"vault":"gone","id":"i1"}`, "item gone"},
	} {
		if r := call(c.name, c.args); !r.IsError || !strings.Contains(r.Text, c.want) {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
	if r := call("task_done", `{"task":987654}`); !r.IsError {
		t.Errorf("task_done of no task: %+v", r)
	}
	if r := call("screen", `{"pane":1}`); r.IsError || !strings.HasPrefix(r.Text, "pane 1 alt=false running=\"\"\n$") {
		t.Errorf("screen: %+v", r)
	}
	tab.orgCfg.Vaults = []VaultCfg{{Name: "down", Type: "passbolt"}}
	tab.orgCfg.vaults["down"] = lbErrVault{}
	if r := call("secrets_search", `{"query":""}`); !r.IsError || r.Text != "down: vault down" {
		t.Errorf("search error: %+v", r)
	}
	tab.orgCfg.Vaults = []VaultCfg{{Name: "many", Type: "passbolt"}}
	if r := call("secrets_search", `{"query":""}`); r.IsError || !strings.HasPrefix(r.Text, "201 items\n") || !strings.HasSuffix(r.Text, "… 1 more, narrow the query") {
		t.Errorf("many items: %.40q…", r.Text)
	}
	w := newSSHW(t, sshtest.Opts{}, "    SetEnv PATH="+stubPath(t)+"\n")
	term.Host, tab.panes[0].dir = "web-01", w.dir
	if r := call("changes", `{"pane":1}`); r.IsError || !strings.HasPrefix(r.Text, "changes on web1 in the last 24h:\n== packages") {
		t.Errorf("changes default: %+v", r)
	}
}

func TestOpenPaneWaitsC(t *testing.T) {
	stateC(t)
	old := connectHold
	defer func() { connectHold = old }()
	stuck := func() *Term {
		tm, _ := fakeTermA(t, "web1")
		tm.b, tm.exited = nil, true
		return tm
	}
	n := node{host: "web1", dir: "/d", label: "web1"}
	p := &Pane{id: 3, term: stuck(), dir: "/d"}
	tab := &Tab{org: "Acme", panes: []*Pane{p}}
	go func() { time.Sleep(200 * time.Millisecond); tab.mu.Lock(); p.closed = true; tab.mu.Unlock() }()
	if r := tab.openPane(n); !r.IsError || !strings.HasPrefix(r.Text, "couldn't connect to web1 and the user closed pane 3") {
		t.Errorf("closed: %+v", r)
	}
	p.closed, p.term = false, stuck()
	fixed, _ := fakeTermA(t, "web1")
	go func() { time.Sleep(200 * time.Millisecond); tab.mu.Lock(); p.term = fixed; tab.mu.Unlock() }()
	if r := tab.openPane(n); r.IsError || !strings.HasSuffix(r.Text, "opened pane 3 on web1, at a shell prompt, after the user fixed the connection. Pass pane=3; close_pane it when done.") {
		t.Errorf("fixed: %+v", r)
	}
	p.term = stuck()
	connectHold = 0
	if r := tab.openPane(n); !r.IsError || !strings.HasPrefix(r.Text, "still can't connect to web1 after 0s; pane 3") {
		t.Errorf("gave up: %+v", r)
	}
	tab.panes = nil
	noPrompt := stuck()
	noPrompt.reached = true
	setDispatchA(t, func(m tea.Msg) {
		if pm, ok := m.(paneMsg); ok {
			pm.done <- &Pane{id: 4, term: noPrompt, dir: "/d", name: "web1"}
		}
	})
	if r := tab.openPane(n); !r.IsError || !strings.HasPrefix(r.Text, "opened pane 4 on web1 but it has no shell prompt") {
		t.Errorf("no prompt: %+v", r)
	}
}

// --- peer.go ---

func TestAskPeerTimesOutC(t *testing.T) {
	old := peerAnswerWait
	peerAnswerWait = 50 * time.Millisecond
	defer func() { peerAnswerWait = old }()
	tab := &Tab{org: "Acme"}
	r := tab.askPeer("peer-c", "which port?")
	if !r.IsError || r.Text != "peer-c did not answer in 15 minutes (it may not be running desk peer wait)" {
		t.Errorf("timeout: %+v", r)
	}
	hub.Lock()
	defer hub.Unlock()
	for _, q := range hub.questions {
		if q.tab == tab {
			t.Error("the question is dropped")
		}
	}
}

func TestServePeersErrorsC(t *testing.T) {
	noUnix(t)
	f := filepath.Join(t.TempDir(), "file")
	write(t, f, "x")
	t.Setenv("XDG_RUNTIME_DIR", f)
	if _, err := servePeers(); err == nil {
		t.Error("no peer dir must fail")
	}
	runtimeA(t)
	write(t, filepath.Join(peerDir(), fmt.Sprintf("%d.sock", os.Getpid()), "keep"), "x") // a dir where the socket goes
	if _, err := servePeers(); err == nil {
		t.Error("a socket path that can't be freed must fail")
	}
	runtimeA(t)
	sock, err := servePeers()
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("not json\n"))
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err := c.Read(make([]byte, 16)); n != 0 || err == nil {
		t.Errorf("a bad request gets no answer: %d %v", n, err)
	}
}

func TestPeerMainClosedConnC(t *testing.T) {
	noUnix(t)
	runtimeA(t)
	dir := peerDir()
	os.MkdirAll(dir, 0o700)
	os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, "0.sock")) // stats as nothing
	old, err := net.Listen("unix", filepath.Join(dir, "2.sock"))         // older, refused once closed: removed
	if err != nil {
		t.Fatal(err)
	}
	old.(*net.UnixListener).SetUnlinkOnClose(false)
	old.Close()
	past := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(dir, "2.sock"), past, past)
	ln, err := net.Listen("unix", filepath.Join(dir, "1.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	code, _, errOut := captureA(t, func() int { return peerMain([]string{"list"}) })
	if code != 1 || !strings.HasPrefix(errOut, "desk closed the connection:") {
		t.Errorf("closed: %d %q", code, errOut)
	}
	if _, err := os.Lstat(filepath.Join(dir, "2.sock")); err != nil {
		t.Error("the newest desk is tried first: the older socket isn't reached")
	}
}

// --- model.go ---

// closePanesC closes every pane of m's tabs when the test ends.
func closePanesC(t *testing.T, m *model) {
	t.Cleanup(func() {
		for _, tb := range m.tabs {
			for _, p := range tb.panes {
				p.term.Close()
			}
		}
	})
}

// palPickC runs palette item label for query q, failing if it isn't listed.
func palPickC(t *testing.T, m *model, q, label string) tea.Cmd {
	t.Helper()
	m.pal, m.palQ = true, q
	for i, it := range m.palItems() {
		if it.label == label {
			m.palSel = i
			return m.palKey(keyA("enter"))
		}
	}
	t.Fatalf("no palette item %q for %q", label, q)
	return nil
}

func TestLoadTreeEdgesC(t *testing.T) {
	if tree := loadTree(t.TempDir()); len(tree) != 0 {
		t.Errorf("no orgs: %+v", tree)
	}
	m := &model{tree: []node{{depth: 1, label: "g"}, {depth: 1, label: "h"}}}
	if m.groupDir(0) != "" {
		t.Error("a group with no hosts has no dir")
	}
	if _, ok := any(model{}.View()).(tea.View); !ok {
		t.Error("View")
	}
}

func TestPanesDialC(t *testing.T) {
	needSh(t)
	stateC(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir()) // Windows' home
	var mu sync.Mutex
	var hosts []string
	shell := "printf 'web1$ '; sleep 30"
	HostCmd = func(h string) *exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		hosts = append(hosts, h)
		return exec.Command("bash", "-c", shell)
	}
	t.Cleanup(func() { HostCmd = nil })
	dir := t.TempDir()
	m := &model{}
	closePanesC(t, m)
	tab := m.newTab("Acme", "g", dir, "web1")
	n := node{host: "web1", dir: dir, label: "web1", wol: "52:54:00:12:34:56 127.0.0.1:9"}
	p := m.addPane(tab, n, false)
	if p == nil || tab.focused() != p {
		t.Fatalf("pane: %v %q", p, m.errText)
	}
	p.term.onBlock(Block{Cmd: "uptime", Exit: 0})
	if es := auditSince("Acme", time.Now().Add(-time.Minute)); len(es) != 1 || es[0].Host != "web1" || es[0].Cmd != "uptime" {
		t.Errorf("the block goes to the audit log: %+v", es)
	}
	r, _ := m.key(keyA("ctrl+t"))
	*m = r.(model)
	if len(tab.panes) != 2 || tab.focused() != tab.panes[1] {
		t.Errorf("ctrl+t: %d panes, the new one not focused", len(tab.panes))
	}
	mu.Lock()
	if strings.Join(hosts, ",") != "web1,web1" {
		t.Errorf("dialed %v", hosts)
	}
	mu.Unlock()
	t.Setenv("PATH", t.TempDir()) // no bash: the shell can't start
	before := p.term
	m.redialPane(tab, p)
	if p.term != before || m.errText == "" {
		t.Errorf("a failed redial keeps the pane: %q", m.errText)
	}
	m.errText = ""
	if m.addPane(tab, n, false) != nil || m.errText == "" {
		t.Error("no shell: no pane")
	}
	done := make(chan *Pane, 1)
	r, _ = m.update(paneMsg{tab, n, done})
	*m = r.(model)
	if <-done != nil {
		t.Error("paneMsg answers the pane, nil here")
	}
	m.open(node{org: "Acme", host: "web9", dir: dir, label: "web9"})
	if len(m.tabs) != 1 {
		t.Errorf("a host that won't open closes its tab: %d tabs", len(m.tabs))
	}
}

func TestKeepForwardsC(t *testing.T) {
	w := newSSHW(t, sshtest.Opts{}, "")
	dir := w.dir
	up, _ := fakeTermA(t, "web-01")
	up.reached = true
	closed, _ := fakeTermA(t, "web-01")
	closed.exited = true
	tab := &Tab{panes: []*Pane{{id: 1, term: up, dir: dir}}}
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	port := strconv.Itoa(freePort(t))
	takenPort := strconv.Itoa(taken.Addr().(*net.TCPAddr).Port)
	good := portFwd{host: "web-01", dir: dir, name: "web-01", kind: "L", spec: port + ":localhost:80"}
	bad := portFwd{host: "web-01", dir: dir, name: "web-01", kind: "L", spec: takenPort + ":localhost:80"}
	on := portFwd{host: "web-01", dir: dir, name: "web-01", kind: "D", spec: "1080"}
	other := portFwd{host: "web2", dir: dir, name: "web2", kind: "L", spec: "1:localhost:1"}
	m := &model{tabs: []*Tab{tab}, forwards: []portFwd{good, bad, other}}
	m.keepForwards()
	if !m.fwdOn[good] || !m.fwdOn[bad] || m.fwdOn[other] {
		t.Errorf("on: %v", m.fwdOn)
	}
	if !strings.HasPrefix(m.flash, "forward localhost:"+takenPort+" → localhost:80 via web-01: ") || !strings.Contains(m.flash, "in use") && exeSuffix == "" { // Windows words it otherwise
		t.Errorf("flash: %q", m.flash)
	}
	if c, err := net.Dial("tcp", "127.0.0.1:"+port); err != nil {
		t.Errorf("the forward listens: %v", err)
	} else {
		c.Close()
	}
	m.forwards, m.fwdOn[on] = append(m.forwards, on), true // up already
	m.keepForwards()
	fwdStops.Lock()
	n := len(fwdStops.m)
	fwdStops.Unlock()
	if n != 1 {
		t.Errorf("once per connection: %d running", n)
	}
	p := &Pane{id: 2, term: closed, dir: dir, closed: true}
	tab.panes = append(tab.panes, p)
	m.redial() // a closed pane is left alone
	if !p.lost.IsZero() {
		t.Error("closed panes aren't redialed")
	}
}

func TestUpdateMessagesC(t *testing.T) {
	stateC(t)
	runtimeA(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir()) // Windows' home
	t.Setenv("PATH", t.TempDir())        // no claude
	tab := &Tab{org: "Acme", orgOnly: true, orgCfg: &orgConfig{}, dir: t.TempDir(), asked: []string{"and the disk?"}, busy: true}
	m := model{tabs: []*Tab{tab}}
	r, cmd := m.update(passwordMsg{err: fmt.Errorf("vault locked")})
	if m = r.(model); m.flash != "! vault locked" || cmd != nil {
		t.Errorf("password error: %q", m.flash)
	}
	r, _ = m.update(alertsMsg{src: "Acme/mon", alerts: []nagiosAlert{{Org: "Acme", Host: "db1", State: "DOWN"}}, first: true})
	if m = r.(model); len(m.alerts["Acme/mon"]) != 1 {
		t.Errorf("alerts: %v", m.alerts)
	}
	r, _ = m.update(claudeMsg{tab: tab, done: true})
	m = r.(model)
	if len(tab.asked) != 0 || lastChatA(tab) == "" || tab.chat[len(tab.chat)-1].who != "error" {
		t.Errorf("a queued question that can't be sent: %+v", tab.chat)
	}
	m.input = "   "
	m.ask()
	n := len(tab.chat)
	m.input = "uptime?"
	m.ask()
	if len(tab.chat) != n+1 || tab.chat[n].who != "error" || m.input != "uptime?" {
		t.Errorf("ask without claude: %+v", tab.chat[n:])
	}
	if res := m.usePassword(passwordMsg{err: fmt.Errorf("x")}); res != nil || m.flash != "! x" {
		t.Error("usePassword error")
	}
}

func TestTickRunsHealthC(t *testing.T) {
	stateC(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir()) // Windows' home
	dir := orgWith(t, "[schedule]\nhealth = \"00:00\"\n")
	c, _ := claudeA(t)
	m := model{restored: true, tree: []node{{depth: 0, org: "Acme", label: "Acme", dir: dir}},
		tabs: []*Tab{{org: "Acme", orgOnly: true, dir: dir, claude: c, orgCfg: &orgConfig{}}}}
	for time.Now().Second()%10 != 0 || time.Now().Nanosecond() > 5e8 {
		time.Sleep(20 * time.Millisecond)
	}
	r, cmd := m.update(tickMsg{})
	if m = r.(model); cmd == nil || !m.tabs[0].health {
		t.Error("a tick on a tenth second runs the due health checks")
	}
}

func TestFixLegacyKeyC(t *testing.T) {
	needSh(t)
	stateC(t)
	HostCmd = func(string) *exec.Cmd { return exec.Command("sleep", "30") } // the reconnect
	t.Cleanup(func() { HostCmd = nil })
	dir := filepath.Join(t.TempDir(), "Acme", infraDir)
	os.MkdirAll(dir, 0o755) // the pane's shell starts in it
	failed, _ := fakeTermA(t, "web1")
	failed.b, failed.exited, failed.code = nil, true, 255
	failed.err = &sshc.NegotiationError{What: "HostKeyAlgorithms", Theirs: []string{"ssh-rsa"}}
	p := &Pane{id: 1, term: failed, dir: dir, name: "web1"}
	tab := &Tab{org: "Acme", term: failed, panes: []*Pane{p}}
	m := model{tabs: []*Tab{tab}}
	closePanesC(t, &m)
	r, _ := m.key(keyA("f"))
	if m = r.(model); !strings.Contains(m.errText, "no such file") || p.term != failed {
		t.Errorf("no ssh_config: %q", m.errText)
	}
	write(t, filepath.Join(orgOfInfra(dir), ".desk", serversFile), "Host web1\n    HostName 192.0.2.1\n")
	m.errText = ""
	r, _ = m.key(keyA("f"))
	m = r.(model)
	b, _ := os.ReadFile(filepath.Join(orgOfInfra(dir), ".desk", serversFile))
	if m.errText != "" || !strings.Contains(string(b), "HostKeyAlgorithms +ssh-rsa") || p.term == failed {
		t.Errorf("fixed and reconnected: %q\n%s", m.errText, b)
	}
}

func TestPaletteActionsC(t *testing.T) {
	noUnix(t)
	st := stateC(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir()) // Windows' home
	w := newSSHW(t, sshtest.Opts{}, "")
	dir := w.dir
	term, _ := fakeTermA(t, "web1")
	term.reached = true
	tab := &Tab{org: "Acme", name: "web1", term: term, dir: dir, panes: []*Pane{{id: 1, term: term, dir: dir, name: "web1"}},
		orgCfg: &orgConfig{}, pinned: map[int]bool{}, lastCtx: map[int]bool{}, opened: time.Now()}
	var invest []string
	m := &model{tabs: []*Tab{tab}, onInvestigate: func(org, host, _, _, _ string, _ time.Time) { invest = append(invest, org+"/"+host) },
		alerts: map[string][]nagiosAlert{"Acme/mon": {{Org: "Acme", Host: "db1", State: "DOWN"}}}}
	closePanesC(t, m)

	if items := m.palItems(); !slices.ContainsFunc(items, func(it palItem) bool { return it.label == "investigate DOWN db1" }) {
		t.Fatal("alerts listed")
	}
	m.palQ = "so"
	if !slices.ContainsFunc(m.palItems(), func(it palItem) bool { return it.label == "socks PORT" }) {
		t.Error("socks hint")
	}
	palPickC(t, m, "investigate", "investigate DOWN db1")
	if len(invest) != 1 || invest[0] != "Acme/db1" {
		t.Errorf("investigate: %v", invest)
	}

	port := strconv.Itoa(freePort(t))
	palPickC(t, m, "forward "+port, "forward "+port)
	fwd := portFwd{host: "web1", dir: dir, name: "web1", kind: "L", spec: port + ":localhost:" + port}
	if m.flash != "forwarding "+fwd.String() || !m.fwdOn[fwd] || len(m.forwards) != 1 {
		t.Errorf("forward: %q %v", m.flash, m.forwards)
	}
	taken, _ := net.Listen("tcp", "127.0.0.1:0")
	defer taken.Close()
	tp := strconv.Itoa(taken.Addr().(*net.TCPAddr).Port)
	palPickC(t, m, "forward "+tp, "forward "+tp)
	if !strings.HasPrefix(m.flash, "forward: ") || !strings.Contains(m.flash, "in use") || len(m.forwards) != 1 {
		t.Errorf("failed forward: %q", m.flash)
	}
	palPickC(t, m, "stop forward", "stop forward "+fwd.String())
	if m.flash != "stopped "+fwd.String() || len(m.forwards) != 0 || m.fwdOn[fwd] {
		t.Errorf("stop: %q", m.flash)
	}
	if c, err := net.Dial("tcp", "127.0.0.1:"+port); err == nil {
		c.Close()
		t.Error("a stopped forward listens no more")
	}
	bad := portFwd{host: "web1", dir: dir, name: "web1", kind: "L", spec: "7777:localhost:7777"}
	m.forwards, m.fwdOn = []portFwd{bad}, map[portFwd]bool{bad: true}
	palPickC(t, m, "stop forward", "stop forward "+bad.String())
	if m.flash != "stop forward: no such forward" || len(m.forwards) != 0 {
		t.Errorf("failed stop: %q", m.flash)
	}

	var opened []string
	oldOpen := openLocal
	openLocal = func(p string) error { opened = append(opened, p); return nil }
	defer func() { openLocal = oldOpen }()
	palPickC(t, m, "export session report", "export session report")
	if len(opened) != 1 || m.flash != "report saved: "+opened[0] || !strings.HasPrefix(opened[0], filepath.Join(st, "desk", "infra", "reports")) {
		t.Errorf("report: %q %v", m.flash, opened)
	}
	os.RemoveAll(filepath.Join(st, "desk", "infra", "reports"))
	write(t, filepath.Join(st, "desk", "infra", "reports"), "x")
	palPickC(t, m, "export session report", "export session report")
	if len(opened) != 1 || !strings.Contains(m.flash, "not a directory") {
		t.Errorf("report error: %q", m.flash)
	}

	if cmd := palPickC(t, m, "copy ssh command", "copy ssh command"); cmd == nil || m.flash != "ssh command copied" {
		t.Errorf("copy ssh: %q", m.flash)
	}
	cmd := palPickC(t, m, "copy server password", "copy server password")
	if pm, ok := cmd().(passwordMsg); !ok || pm.err == nil || !strings.HasPrefix(pm.err.Error(), "no password for this server") {
		t.Errorf("copy password: %+v", pm)
	}
	r, cmd := m.key(keyA("alt+p"))
	*m = r.(model)
	if pm, ok := cmd().(passwordMsg); !ok || pm.term != term {
		t.Errorf("alt+p: %+v", pm)
	}
	term.Host = "local"
	if cmd := palPickC(t, m, "copy ssh command", "copy ssh command"); cmd != nil {
		t.Error("no ssh command for the local machine")
	}

	cameraPalC(t, m)

	runtimeA(t)
	t.Setenv("PATH", t.TempDir()) // no claude
	palPickC(t, m, "draft a postmortem", "draft a postmortem with Claude")
	if m.flash == "" || !strings.Contains(m.flash, "claude") {
		t.Errorf("postmortem without claude: %q", m.flash)
	}
	write(t, filepath.Join(orgOfInfra(dir), ".desk", serversFile), "Host web2\n")
	m.tree = []node{{depth: 0, org: "Acme", label: "Acme", dir: dir}, {depth: 1, label: "g"}, {depth: 2, org: "Acme", group: "g", host: "web2", label: "web2", dir: dir}}
	HostCmd = func(string) *exec.Cmd { return exec.Command("/no/such/shell") }
	t.Cleanup(func() { HostCmd = nil })
	palPickC(t, m, "Acme › g › web2", "Acme › g › web2")
	if len(m.tabs) != 1 || m.group != "" {
		t.Errorf("a host that can't open leaves no tab: %d", len(m.tabs))
	}
}

func TestApproveBlockedRunsC(t *testing.T) {
	stateC(t)
	term, r := fakeTermA(t, "web1")
	tab := &Tab{org: "Acme", term: term, panes: []*Pane{{id: 1, term: term, name: "web1"}}}
	a := &approval{cmd: "systemctl restart nginx", pane: 1, blocked: true, done: make(chan approvalResult, 1)}
	tab.pending = []*approval{a}
	m := &model{tabs: []*Tab{tab}}
	cmd := m.answer(tab, keyA("y"))
	go func() {
		readPipeA(r)
		term.mu.Lock()
		term.blocks = append(term.blocks, Block{N: 1, Output: "TOKEN=abc ok"})
		term.mu.Unlock()
	}()
	fu, ok := cmd().(followUpMsg)
	if !ok || !strings.HasSuffix(fu.text, "the user approved it and desk ran it:\nblock #1 exit=0\nTOKEN=[redacted] ok") {
		t.Errorf("follow-up: %+v", fu)
	}
}

func TestNewTabAndReloadErrorsC(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir()) // Windows' home
	bad := filepath.Join(t.TempDir(), "Bad", infraDir)
	write(t, filepath.Join(orgOfInfra(bad), ".desk", "desk.toml"), "[[[ broken")
	m := &model{}
	tab := m.newTab("Bad", "", bad, "Bad")
	if m.errText == "" || tab.orgCfg == nil {
		t.Errorf("a broken config: %q", m.errText)
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "Acme", ".desk", serversFile), "#@ group g\nHost web1\n")
	m.errText, m.root, m.cursor = "", root, 99
	m.reloadConfig()
	if len(m.tree) != 3 || m.cursor != 2 || m.errText == "" {
		t.Errorf("reload: %d rows, cursor %d, err %q", len(m.tree), m.cursor, m.errText)
	}
}

// --- pretty.go ---

func TestTexToUnicodeC(t *testing.T) {
	for in, want := range map[string]string{
		`\frac{a+b}{2}`: "(a+b)/2", `\frac{{a}}{b}`: "a/b", `\sqrt{xy}`: "√(xy)", `\mathbb{R}`: "ℝ", `\mathbb{X}`: "X",
		`\foo`: "foo", `\text{hi}`: "hi", `x^ {2}`: "x²", `x^`: "x", `e^\alpha`: "e^α", `x_{ab}`: "x_(ab)", `x^\,`: "x^ ",
	} {
		if got := texToUnicode(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestPrettyHelpersC(t *testing.T) {
	if got := ansi.Strip(fit("abcdef", 3)); got != "ab…" {
		t.Errorf("fit: %q", got)
	}
	if got := ansi.Strip(spread("left", "right", 5)); got != "left " {
		t.Errorf("spread without room: %q", got)
	}
	if got := fg("").Render("x"); got != "x" {
		t.Errorf("no color: %q", got)
	}
	if got := ansi.Strip(cellText("**b** `c`")); got != "b c" {
		t.Errorf("cell: %q", got)
	}
	for role, c := range map[string]string{"note": pal.Link, "dim": pal.Quote, "warn": pal.DiffRemoved, "ok": pal.DiffAdded} {
		if got := roleStyle(role).GetForeground(); got != lipgloss.Color(c) {
			t.Errorf("%s: %v", role, got)
		}
	}
	if !roleStyle("strong").GetBold() {
		t.Error("strong is bold")
	}
	if fmtNum(math.NaN()) != "" {
		t.Error("NaN prints nothing")
	}
	for _, c := range []struct{ lo, hi, step float64 }{{5, 5, 0.2}, {0, 4, 1}, {0, 10, 2}, {0, 20, 5}, {0, 32, 10}} {
		if _, _, step := niceTicks(c.lo, c.hi, 4); step != c.step {
			t.Errorf("niceTicks(%v, %v): step %v, want %v", c.lo, c.hi, step, c.step)
		}
	}
	if _, ok := mermaid("graph LR\nAlpha-->Bravo-->Charlie-->Delta", 12); ok {
		t.Error("a diagram too wide shows its source")
	}
	if _, ok := xychart("xychart-beta\nbar [1, 2]", 60); ok {
		t.Error("no x-axis: no chart")
	}
	out, ok := xychart("xychart-beta\nx-axis [a, b, c]\ny-axis 0 --> 100\nbar [50, x, 100]\nline [25]", 60)
	if !ok {
		t.Fatal("chart")
	}
	lines := strings.Split(ansi.Strip(out), "\n")
	if len(lines) < 4 || !strings.Contains(lines[1], "NaN") || strings.Count(lines[2], "█") != 2*strings.Count(lines[0], "█") || !strings.Contains(out, "●") {
		t.Errorf("chart:\n%s", ansi.Strip(out))
	}
	if c, ok := parseChart("xychart-beta\nbar [1, 2, 3]"); !ok || strings.Join(c.labels, ",") != "1,2,3" {
		t.Errorf("default labels: %v", c.labels)
	}
	if md, _ := markdown("```math\nx^2\n```", 60); !strings.Contains(ansi.Strip(md), "x²") {
		t.Errorf("math block:\n%s", md)
	}
	tbl := "| " + strings.Repeat("wide header ", 8) + "| b |\n|---|---|\n| " + strings.Repeat("cell ", 20) + "| 2 |"
	if p := tablePiece(tbl, 40); p.width() > 40 {
		t.Errorf("a table wider than the panel wraps: %d", p.width())
	}
}

func TestProseThemesC(t *testing.T) {
	old := themeName
	defer setTheme(old)
	setTheme("github-light")
	if _, seq := codeBack(); seq != "\x1b[48;2;238;240;243m" {
		t.Errorf("light code background: %q", seq)
	}
	if got := ansi.Strip(prose("**bold** text", 41)); !strings.Contains(got, "bold text") {
		t.Errorf("light prose: %q", got)
	}
	setTheme("mono")
	if got := prose("**bold** text", 43); !strings.Contains(got, "**bold** text") || strings.Contains(got, "\x1b[") {
		t.Errorf("mono prose: %q", got)
	}
}

// --- desk.go ---

func TestHostOfStatesC(t *testing.T) {
	for _, c := range []struct {
		r    sideRow
		want State
	}{{sideRow{dot: cRed}, Alert}, {sideRow{wait: true}, Waiting}, {sideRow{dot: cOrange}, Waiting}, {sideRow{dot: cPurple}, Working},
		{sideRow{dot: cGreen}, Connected}, {sideRow{}, Idle}} {
		if got := hostOf(c.r).State; got != c.want {
			t.Errorf("%+v: %s, want %s", c.r, got, c.want)
		}
	}
}

func TestDeskMainsC(t *testing.T) {
	if code, out, _ := captureA(t, func() int { return PeerMain([]string{"help"}) }); code != 0 || !strings.Contains(out, "desk peer") {
		t.Errorf("peer help: %d %q", code, out)
	}
}

func TestWorkspaceAPIC(t *testing.T) {
	for _, v := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "HOME"} {
		t.Setenv(v, t.TempDir())
	}
	HostCmd = func(string) *exec.Cmd { return exec.Command("sleep", "30") }
	t.Cleanup(func() { HostCmd = nil })
	root := t.TempDir()
	write(t, filepath.Join(root, "Acme", ".desk", serversFile), "#@ group g\nHost web1\n")
	write(t, filepath.Join(root, "Acme", ".desk", "desk.toml"), "record = false\n")
	write(t, uiPath(), `{"Theme":"`+themeName+`"}`)
	defer func() { dispatch, repaint, guiReplies = nil, nil, false }()
	w := New(root, func() {})
	defer w.Close()
	if !w.OpenOrg("Acme") || w.OpenOrg("Globex") {
		t.Fatal("OpenOrg")
	}
	for end := time.Now().Add(testwait.Long); time.Now().Before(end) && !w.Tree()[0].Claude; time.Sleep(20 * time.Millisecond) {
	}
	if tr := w.Tree(); !tr[0].Claude || w.ActiveOrg() != "Acme" {
		t.Fatalf("org session: %+v %q", tr, w.ActiveOrg())
	}
	w.SetCards(Cards{Is: func(n string) bool { return n == "DESK-7" }})
	if !w.OpenCard("Acme", "DESK-7") || w.OpenCard("Globex", "DESK-7") {
		t.Fatal("OpenCard")
	}
	card := make(chan string, 1)
	w.u.d.act(func(m *model) { card <- fmt.Sprintf("%d %s", len(m.tabs), m.tab().card) })
	if c := <-card; c != "2 DESK-7" {
		t.Errorf("the card's own session, active: %s", c)
	}
	w.SetCards(Cards{})
	w.TogglePin("Acme", "g", "web1")
	if h := w.Tree()[0].Groups[0].Hosts[0]; !h.Pinned {
		t.Error("pinned")
	}
	w.TogglePin("Acme", "g", "web1")
	if h := w.Tree()[0].Groups[0].Hosts[0]; h.Pinned {
		t.Error("unpinned")
	}
	w.OnInvestigate(func(string, string, string, string, string, time.Time) {})
	w.SetFocused(false)
	w.Reload()
	got := make(chan bool, 1)
	w.u.d.act(func(m *model) { got <- m.onInvestigate != nil && m.away && len(m.tree) == 3 })
	select {
	case ok := <-got:
		if !ok {
			t.Error("OnInvestigate, SetFocused or Reload didn't reach the model")
		}
	case <-time.After(testwait.Long):
		t.Fatal("the model loop never ran the actions")
	}
	tab := &Tab{org: "Acme"}
	hub.Lock()
	hub.tasks[900002] = &peerTask{id: 900002, from: "DESK-2", text: "b", tab: tab}
	hub.tasks[900001] = &peerTask{id: 900001, from: "DESK-1", text: "a", tab: tab}
	hub.Unlock()
	defer func() { hub.Lock(); delete(hub.tasks, 900001); delete(hub.tasks, 900002); hub.Unlock() }()
	var mine []Task
	for _, k := range w.Tasks() {
		if k.ID >= 900001 {
			mine = append(mine, k)
		}
	}
	if len(mine) != 2 || mine[0] != (Task{900001, "Acme", "DESK-1", "a"}) || mine[1].ID != 900002 {
		t.Errorf("tasks: %+v", mine)
	}
	if len(ShortcutFilters()) == 0 || w.Shortcut(key.Event{Name: "K"}) {
		t.Error("a plain K is not the workspace's")
	}
	w.u.st = &guiState{Tree: []guiNode{{Depth: 2, Host: "web1", Org: "Acme", Group: "g", Dir: "/d"}}, Group: groupKey("/d", "g")}
	if w.ActiveOrg() != "Acme" {
		t.Error("the group view's org is active")
	}
	w.u.st = &guiState{}
	if w.ActiveOrg() != "" {
		t.Error("nothing open: no org")
	}
	w.u.d.state.Store(&guiState{Tree: []guiNode{{Depth: 2, Host: "stray"}}})
	if tr := w.Tree(); len(tr) != 0 {
		t.Errorf("rows under no org are no org: %+v", tr)
	}
	w.u.d.state.Store(&guiState{})
	if tr := w.Tree(); len(tr) != 0 {
		t.Errorf("an empty tree has no orgs: %+v", tr)
	}
}

// --- org.go: desk's vaults, unlocked through the ask sheet ---

func TestDeskVaultUnlockedC(t *testing.T) {
	ctx := context.Background()
	dv := fakeBWC(t)
	asked := lbFakeAsk(t, func(i int, q *question) (string, bool) { return "hunter2-fake", true })
	o := &orgConfig{Vaults: []VaultCfg{{Name: "bw", Type: "vaultwarden", desk: dv}}}
	v, _, err := o.vault("bw")
	if err != nil {
		t.Fatal(err)
	}
	items, err := v.search(ctx, "WEB")
	if err != nil || len(items) != 1 || items[0] != (vaultItem{ID: "c1", Name: "web1 root", Username: "root"}) {
		t.Fatalf("search: %+v %v", items, err)
	}
	if len(*asked) != 1 || (*asked)[0].title != "Master password · bw" || !(*asked)[0].masked {
		t.Errorf("asked: %d", len(*asked))
	}
	if items, _ := v.search(ctx, "db"); len(items) != 0 {
		t.Errorf("no match: %+v", items)
	}
	it, err := v.get(ctx, "c1")
	if err != nil || it.Password != "pw-fake" || it.Username != "root" {
		t.Errorf("get: %+v %v", it, err)
	}
	if _, err := v.get(ctx, "c9"); err == nil || err.Error() != `no item "c9"` {
		t.Errorf("missing: %v", err)
	}
	g := filepath.Join(t.TempDir(), "Acme", infraDir)
	orgsMu.Lock()
	orgs[g] = o
	orgsMu.Unlock()
	if u, err := serverPassword(g, &SecretRef{Vault: "bw", Item: "web1 root", Field: "username"}); err != nil || u != "root" {
		t.Errorf("a field of the item: %q %v", u, err)
	}
	if p, err := serverPassword(g, &SecretRef{Vault: "bw", Item: "web1 root"}); err != nil || p != "pw-fake" {
		t.Errorf("the password: %q %v", p, err)
	}
	s := SourceCfg{Name: "n", secret: &deskorgs.Secret{Vault: "bw", Item: "web1 root"}}
	if u, p, err := o.login(ctx, s); err != nil || u != "" || p != "pw-fake" {
		t.Errorf("the item's password, no user: %q %q %v", u, p, err)
	}
	s = SourceCfg{Name: "n2", secret: &deskorgs.Secret{Vault: "bw", Item: "nope"}}
	if _, _, err := o.login(ctx, s); err == nil || !strings.HasPrefix(err.Error(), "login for n2: ") {
		t.Errorf("a missing item: %v", err)
	}
}

// TestTermRunWaitsForPromptC: run returns once the next prompt is up, not at
// the block's end, so the next run doesn't find the pane busy; a shell gone
// after the block gives the block too.
func TestTermRunWaitsForPromptC(t *testing.T) {
	term, r := fakeTermA(t, "web1")
	go func() {
		readPipeA(r)
		term.mu.Lock()
		term.blocks, term.b = append(term.blocks, Block{N: 1}), nil // D: the prompt not back yet
		term.mu.Unlock()
		time.Sleep(300 * time.Millisecond) // the gap before B; Run must outwait it
		term.mu.Lock()
		term.b = &pos{} // B
		term.mu.Unlock()
	}()
	if res := term.Run("true", testwait.Long); res.Text != "block #1 exit=0\n" || !term.AtPrompt() {
		t.Errorf("run: %q, at prompt %v", res.Text, term.AtPrompt())
	}
	go func() {
		readPipeA(r)
		term.mu.Lock()
		term.blocks, term.b, term.exited = append(term.blocks, Block{N: 2, Exit: 3}), nil, true // "exit 3" after a block
		term.mu.Unlock()
	}()
	if res := term.Run("false; exit 3", testwait.Long); res.Text != "block #2 exit=3\n" || !res.IsError {
		t.Errorf("run before an exit: %q", res.Text)
	}
}
