package infra

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/color"
	"io"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
	"github.com/lfdominguez/desk/internal/tty"
)

// Block is one prompt: the command line and its output, delimited by OSC 133 marks.
type Block struct {
	N          int
	Cmd        string
	Output     string
	Exit       int
	ByClaude   bool
	Dur        time.Duration
	Start, End int  // absolute lines [Start, End) on screen, for decoration
	PX         int  // column where the command starts on its first line (after the prompt)
	stale      bool // its lines were wiped (clear); keep the text, drop the decoration
}

type pos struct{ line, x int } // line is absolute: scrollback + screen

// Term is a shell on a server (an SSH session, sshconn.go) or here (local
// bash in a PTY) rendered through a vt emulator.
type Term struct {
	Host string

	mu         sync.Mutex
	emu        *vt.Emulator
	pty        proc
	notify     func()
	blocks     []Block
	b, c       *pos // start of command / start of output of the open block
	cAt        time.Time
	cmd        string // command line of the open block, read when it starts
	typed      bool   // Claude wrote the current prompt line
	byClaude   bool   // Claude typed into the current prompt
	injected   bool
	reached    bool       // the server asked something (password, host key): it answered
	up         bool       // the SSH session started: authenticated, the shell running
	err        error      // why the SSH connection couldn't be made
	steps      []connStep // the dial's steps (connsteps.go); none when the pool had the connection
	started    time.Time
	integrated bool // saw at least one OSC 133 mark
	exited     bool
	hup        bool              // Close hung it up (not a dropped link)
	dropped    bool              // it ended with the link, not by itself
	code       int               // the program's exit status, once exited (-1: signaled)
	cwd        string            // the shell's directory, from OSC 7 at each prompt
	env, env0  map[string]string // exported environment now, and as the shell started (OSC 6973)
	restore    string            // commands run after the shell integration (cd, exports of a restored session)
	psRestore  string            // restore's environment for PowerShell ("; $env:K = 'v'"…)
	pending    *paneState        // restored history, written once the injection's clear ran (replayMark)
	keepLogin  bool              // put what came before the first prompt back after the clear (banner)
	banner     string            // that text, waiting for the replayMark
	rev        int               // bumps when something paneState keeps changes (session saves)
	onBlock    func(Block)       // called (with mu held) as each command finishes
	rec        *recorder         // records the output (unless desk.toml says record = false)
	ver        atomic.Uint64     // bumps on output and resize: the GUI redraws a pane's cells only then
}

// Version changes whenever what Grid or SudoPrompt return may have. Lock-free.
func (t *Term) Version() uint64 { return t.ver.Load() }

// Synchronized: the program is mid synchronized update (vt's Synchronized).
func (t *Term) Synchronized() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.emu.Synchronized()
}

// Record starts writing the terminal's output to an asciicast file at p.
func (t *Term) Record(p string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, err := newRecorder(p, t.emu.Width(), t.emu.Height())
	if err == nil {
		t.rec = r
	}
	return err
}

// OnBlock sets what runs as each command finishes (the audit log).
func (t *Term) OnBlock(f func(Block)) {
	t.mu.Lock()
	t.onBlock = f
	t.mu.Unlock()
}

// Row is one rendered terminal line plus the block it belongs to (N=0: none).
type Row struct {
	Text    string
	N       int
	First   bool // the block's command line
	Running bool // inside the command still running
	Typed   bool // prompt line holding a command Claude wrote
	PX      int  // on a block's first line: where the command starts (Grid only)
}

// shellIntegration makes bash, zsh and fish emit OSC 133 marks: B after the
// prompt, C before the command runs, D;exit when it finishes, plus OSC 7 with
// the directory for the file panels. One line every
// shell parses; each runs only its own eval'd part. Leading space keeps it out
// of history (HISTCONTROL=ignorespace, zsh hist_ignore_space, fish always).
// ponytail: zsh themes that rebuild PS1 on every precmd (p10k) drop the B mark.
const shellIntegration = ` test -n "$BASH_VERSION" && eval '__desk_d(){ printf "\033]133;D;%s\007\033]7;%s\007" $? "$PWD"; __desk_e; }; ` + shEnvFuncs +
	`PROMPT_COMMAND="__desk_d${PROMPT_COMMAND:+;$PROMPT_COMMAND}"; PS1="$PS1\[\033]133;B\007\]"; PS0="\033]133;C\007"'; ` +
	`test -n "$ZSH_VERSION" && eval '__desk_d(){ printf "\033]133;D;%s\007\033]7;%s\007" $? "$PWD"; __desk_e; }; __desk_c(){ printf "\033]133;C\007"; }; ` + shEnvFuncs +
	`precmd_functions=(__desk_d $precmd_functions); preexec_functions+=(__desk_c); PS1="$PS1%{$(printf "\033]133;B\007")%}"'; ` +
	`test -n "$FISH_VERSION" && eval 'function __desk_c --on-event fish_preexec; printf "\e]133;C\a"; end; ` +
	`function __desk_d --on-event fish_postexec; printf "\e]133;D;%s\a" $status; end; ` +
	`function __desk_e; set -l e (begin; env -0 2>/dev/null; or env; end | base64 | string join ""); if test "$e" != "$__desk_le"; set -g __desk_le $e; printf "\e]6973;%s\a" $e; end; end; ` +
	`function __desk_u; set -e $argv; end; ` +
	`functions -q __desk_p; or functions -c fish_prompt __desk_p; function fish_prompt; printf "\e]7;%s\a" $PWD; __desk_e; __desk_p; printf "\e]133;B\a"; end'; clear` + "\r"

// shEnvFuncs (bash and zsh): __desk_e reports the exported environment
// (OSC 6973, base64 of env -0) whenever it changed since the last prompt, so a
// restored session can set again what the user exported; __desk_u unsets.
// fish has its own above.
const shEnvFuncs = `__desk_e(){ local e; e=$( (env -0 2>/dev/null || env) | base64 | tr -d "\n"); [ "$e" = "$__desk_le" ] || { __desk_le=$e; printf "\033]6973;%s\007" "$e"; }; }; __desk_u(){ unset "$@"; }; `

// psIntegration is shellIntegration for PowerShell (5.1 and 7), whose
// default prompt is "PS C:\path> ": the prompt function wraps the one it had
// with D;exit, OSC 7 (the directory, a Windows path; a non-file location's
// isn't absPath) and B, and with PSReadLine (always there when interactive)
// its ReadLine is followed by C. __deskE is shEnvFuncs' __desk_e: the
// environment (Env:, NUL-separated, base64 of UTF-8) as OSC 6973 when it
// changed. Then Clear-Host; maybeInject adds the first report and the
// restored environment and directory (psRestore).
// ponytail: older conhosts (before Windows 11 22H2) may swallow the marks,
// which leaves the pane without blocks or directory.
const psIntegration = `function global:__deskE { $b = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes(((Get-ChildItem Env:) | ForEach-Object { $_.Name + '=' + $_.Value }) -join [char]0)); if ($b -ne $global:__deskLE) { $global:__deskLE = $b; "$([char]27)]6973;$b$([char]7)" } }; ` +
	`$global:__deskP = $function:prompt; function global:prompt { $e = if ($?) { 0 } elseif ($global:LASTEXITCODE) { $global:LASTEXITCODE } else { 1 }; ` +
	`"$([char]27)]133;D;$e$([char]7)$([char]27)]7;$($PWD.ProviderPath)$([char]7)" + (__deskE) + (& $global:__deskP) + "$([char]27)]133;B$([char]7)" }; ` +
	`if (Get-Module PSReadLine) { function global:PSConsoleHostReadLine { $l = [Microsoft.PowerShell.PSConsoleReadLine]::ReadLine($host.Runspace, $ExecutionContext); [Console]::Write("$([char]27)]133;C$([char]7)"); $l } }; Clear-Host`

// promptEnd is how a shell prompt ends: bash $ #, zsh %, fish > ❯.
var promptEnd = []string{"$", "#", "%", ">", "❯"}

// HostCmd, when set, is the shell a host's pane runs instead of an SSH
// session (nil: SSH after all): desk demo's made-up servers.
var HostCmd func(host string) *exec.Cmd

// proc is what a Term runs: a program here in a PTY (ptyProc) or a shell on
// an SSH session (sshProc).
type proc interface {
	io.ReadWriter
	Resize(w, h int)
	Hangup() // SIGHUP, and the PTY or session closed
	// Wait is the exit status once Read failed; dropped says the link went
	// rather than the program ending.
	Wait() (code int, dropped bool)
}

// ptyProc is a local program in a PTY.
type ptyProc struct{ *tty.PTY }

// Wait: a program here ending with 255 counts as a dropped link, as ssh's
// 255 did (HostCmd's shells stand in for servers).
func (p ptyProc) Wait() (int, bool) {
	code := p.PTY.Wait()
	return code, code == 255
}

// NewTerm starts a shell on host (an SSH session over the workspace's
// shared connection, or a local bash). prelude, when set, is what the pane
// had before (a reconnect, a restored session): its text and blocks go
// above, its directory and environment into the new shell.
func NewTerm(host, dir string, w, h int, notify func(), prelude paneState) (*Term, error) {
	var cmd *exec.Cmd
	switch {
	case host == "local":
		sh := localShell(runtime.GOOS)
		cmd = exec.Command(sh[0], sh[1:]...)
	case HostCmd != nil:
		cmd = HostCmd(host)
	}
	if cmd == nil { // HostCmd may leave a host to SSH
		p := newSSHProc(w, h)
		t := startTerm(host, p, w, h, notify, prelude)
		go t.connect(p, host, dir)
		return t, nil
	}
	cmd.Dir = dir
	return startCmd(host, cmd, w, h, notify, prelude)
}

// localShell is the local pane's shell, without rc files: bash, but on
// macOS only a bash ≥ 4.4 on PATH (/bin/bash is 3.2, without the PS0 that
// marks a command's start), else zsh, which shellIntegration speaks too;
// PowerShell on Windows (psIntegration).
func localShell(goos string) []string {
	switch {
	case goos == "windows":
		return []string{powerShell(), "-NoLogo", "-NoProfile"}
	case goos != "darwin" || exec.Command("bash", "-c", "((BASH_VERSINFO[0]*100+BASH_VERSINFO[1] >= 404))").Run() == nil:
		return []string{"bash", "--norc", "-i"}
	}
	return []string{"zsh", "-f", "-i"}
}

// powerShell is PowerShell 7 (pwsh) when it's on PATH, else the Windows
// PowerShell every Windows has.
func powerShell() string {
	if _, err := exec.LookPath("pwsh"); err == nil {
		return "pwsh"
	}
	return "powershell"
}

// ShellArgs is how a one-off command line runs here: bash -c, or
// PowerShell's -Command on Windows.
func ShellArgs(goos, cmd string) []string {
	if goos == "windows" {
		return []string{powerShell(), "-NoLogo", "-NoProfile", "-Command", cmd}
	}
	return []string{"bash", "-c", cmd}
}

// startCmd starts cmd here in a PTY of w×h.
func startCmd(host string, cmd *exec.Cmd, w, h int, notify func(), prelude paneState) (*Term, error) {
	cmd.Env = append(append(os.Environ(), cmd.Env...), "TERM=xterm-256color")
	f, err := tty.Start(cmd, w, h)
	if err != nil {
		return nil, err
	}
	return startTerm(host, ptyProc{f}, w, h, notify, prelude), nil
}

func startTerm(host string, p proc, w, h int, notify func(), prelude paneState) *Term {
	t := &Term{Host: host, emu: vt.NewEmulator(w, h), pty: p, notify: notify, started: time.Now()}
	t.emu.RegisterOscHandler(133, t.osc133)
	t.emu.RegisterOscHandler(7, t.osc7)
	t.emu.RegisterOscHandler(6973, t.oscEnv)
	t.replay(prelude)

	go io.Copy(p, t.emu) // keys and terminal replies -> the program
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := p.Read(buf)
			t.mu.Lock()
			if err != nil {
				if t.rec != nil {
					t.rec.close()
				}
				t.mu.Unlock()
				code, dropped := p.Wait()
				t.mu.Lock()
				t.exited, t.code, t.dropped = true, code, dropped
				t.mu.Unlock()
				notify()
				return
			}
			data := buf[:n]
			if t.pending != nil || t.banner != "" {
				if i := bytes.Index(data, replayMark); i >= 0 {
					t.emu.Write(data[:i+len(replayMark)])
					t.writeHistory()
					data = data[i+len(replayMark):]
				}
			}
			t.emu.Write(data)
			t.ver.Add(1)
			if t.rec != nil {
				t.rec.event("o", buf[:n])
			}
			t.maybeInject()
			t.mu.Unlock()
			if termTap != nil {
				termTap(t, append([]byte(nil), buf[:n]...)) // buf is reused
			}
			notify()
		}
	}()
	return t
}

// termTap, when set, gets every byte a terminal's program writes, after the
// emulator has read it: the GUI draws them with xterm.js.
var termTap func(*Term, []byte)

// Input writes raw bytes from the GUI's xterm.js (keys, paste) to the program.
func (t *Term) Input(b []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pty.Write(b)
}

// maybeInject sends the shell integration once the first prompt shows up, so it
// never lands in a password or host-key prompt. Caller holds mu.
func (t *Term) maybeInject() {
	if t.injected {
		return
	}
	p := t.emu.CursorPosition()
	l := strings.TrimRight(t.line(t.emu.ScrollbackLen()+p.Y), " ")
	if l == "" && t.b != nil {
		// a prompt as wide as the pane leaves the cursor on the next line;
		// one the shell marked itself (fish's 133;B) is still found there
		l = strings.TrimRight(t.line(t.b.line), " ")
	}
	for _, e := range promptEnd {
		if strings.HasSuffix(l, e) {
			t.injected = true
			if t.keepLogin {
				t.banner = t.loginText(t.emu.ScrollbackLen() + p.Y)
			}
			if e == ">" && strings.HasPrefix(l, "PS ") {
				// the start's environment first, then the restored one
				mark := `; [Console]::Write("$(__deskE)")` + t.psRestore
				if t.cwd != "" { // a restored pane's
					mark += "; Set-Location -LiteralPath " + psQuote(t.cwd)
				}
				if t.pending != nil || t.banner != "" {
					mark += `; [Console]::Write("$([char]27)]6974$([char]7)")` // replayMark
				}
				io.WriteString(t.pty, psIntegration+mark+"\r")
				return
			}
			clear := "clear" // ED 3: the motd and our line go, scrollback too
			if t.pending != nil || t.banner != "" {
				clear += `; printf '\033]6974\007'` // replayMark: the history goes in after the wipe
			}
			io.WriteString(t.pty, strings.TrimSuffix(shellIntegration, "clear\r")+"__desk_e; "+t.restore+clear+"\r")
			return
		}
	}
}

// osc7 records the shell's directory: our own marks send a bare path, other
// shells "file://host/path".
func (t *Term) osc7(data []byte) bool {
	_, p, _ := strings.Cut(string(data), ";")
	if rest, ok := strings.CutPrefix(p, "file://"); ok {
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			p, _ = url.PathUnescape(rest[i:])
		}
	}
	if absPath(p) && p != t.cwd {
		t.cwd = p
		t.rev++
	}
	return true
}

// psQuote quotes s for PowerShell: single quotes, inside which only a
// single quote is special, doubled (PowerShell takes the curly ones for
// single quotes too).
func psQuote(s string) string { return "'" + psQuotes.Replace(s) + "'" }

var psQuotes = strings.NewReplacer("'", "''", "‘", "‘‘", "’", "’’", "‚", "‚‚", "‛", "‛‛")

// absPath: a POSIX path, or a Windows one (C:\x, \\server\share) from
// PowerShell's prompt.
func absPath(p string) bool {
	return strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) || len(p) > 2 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

// oscEnv reads the environment report of the shell integration (base64 of
// env -0, or of env where -0 is missing). The first one is the shell's own
// start: what changed since is what a restore sets again. mu is held.
func (t *Term) oscEnv(data []byte) bool {
	_, b64, _ := strings.Cut(string(data), ";")
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return true
	}
	sep := "\n"
	if strings.Contains(string(raw), "\x00") {
		sep = "\x00"
	}
	env := map[string]string{}
	for _, kv := range strings.Split(string(raw), sep) {
		if k, v, ok := strings.Cut(kv, "="); ok && envName.MatchString(k) {
			env[k] = v
		}
	}
	if t.env0 == nil {
		t.env0 = env
	}
	t.env = env
	t.rev++
	return true
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// envSkip are variables that belong to one shell, never set again on restore.
var envSkip = map[string]bool{"PWD": true, "OLDPWD": true, "SHLVL": true, "_": true, "COLUMNS": true, "LINES": true}

// paneState is what a pane carries over a restart or a reconnect: its text
// and blocks, the shell's directory and the environment the user changed.
// ponytail: text only (no colors), and only exported variables (not shell
// variables, aliases or functions); the shell process itself is new.
type paneState struct {
	Text   string            `json:",omitempty"`
	Blocks []Block           `json:",omitempty"` // Start/End are line indexes into Text
	Cwd    string            `json:",omitempty"`
	Env    map[string]string `json:",omitempty"` // exported or changed since the shell started
	Unset  []string          `json:",omitempty"` // unset since it started

	keepLogin bool // the org's clear_login = false: never saved, set by dial
}

// keptLines caps the text a pane keeps.
const keptLines = 5000

// Keep is the pane's state to carry over: the text up to the live prompt
// (all of it while a command runs), its finished blocks, directory and
// environment changes.
func (t *Term) Keep() paneState {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending != nil {
		return *t.pending // the shell never got to its prompt: keep what it was given
	}
	end := t.emu.ScrollbackLen() + t.emu.Height()
	if t.b != nil && t.c == nil && t.b.line < end {
		end = t.b.line // the waiting prompt comes back by itself
	}
	for end > 0 && strings.TrimRight(t.line(end-1), " ") == "" {
		end--
	}
	start := max(0, end-keptLines)
	rows := make([]string, end-start) // only the kept range is read: the scrollback holds far more
	for abs := start; abs < end; abs++ {
		rows[abs-start] = t.line(abs)
	}
	// Rows the terminal wrapped (vt's wrap mark) join the next one, blanks
	// and all, so a restore wraps the line again at the width it has then.
	sb := t.emu.ScrollbackLen()
	var lines []string
	at := make([]int, end-start+1) // row -> its line in Text
	cur := ""
	for abs := start; abs < end; abs++ {
		at[abs-start] = len(lines)
		r := rows[abs-start]
		if t.wrapped(abs, sb) && abs < end-1 {
			cur += r
			continue
		}
		lines, cur = append(lines, cur+strings.TrimRight(r, " ")), ""
	}
	at[end-start] = len(lines)
	st := paneState{Text: strings.Join(lines, "\n"), Cwd: t.cwd}
	for _, b := range t.blocks {
		if !b.stale && b.Start >= start && b.Start <= b.End && b.End <= end { // a block still open (End before Start) isn't kept
			b.Start, b.End = at[b.Start-start], at[b.End-start]
			if b.End > b.Start {
				st.Blocks = append(st.Blocks, b)
			}
		}
	}
	for k, v := range t.env {
		if !envSkip[k] && t.env0[k] != v {
			if st.Env == nil {
				st.Env = map[string]string{}
			}
			st.Env[k] = v
		}
	}
	for k := range t.env0 {
		if _, ok := t.env[k]; !ok && !envSkip[k] {
			st.Unset = append(st.Unset, k)
		}
	}
	slices.Sort(st.Unset)
	return st
}

// Rev changes whenever something Keep returns may have.
func (t *Term) Rev() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rev + len(t.blocks)
}

// replayMark is what the injected line prints after its clear: the place in
// the output where a restored pane's history goes (wiping it any earlier would
// also keep the echo of the injected line, wrapped at whatever width it had).
var replayMark = []byte("\x1b]6974\x07")

// replay queues a kept state: its text for writeHistory, the cd and exports
// for after the shell integration.
func (t *Term) replay(st paneState) {
	t.keepLogin = st.keepLogin
	if st.Text != "" {
		t.pending = &st
	}
	var r, ps strings.Builder
	for _, k := range slices.Sorted(maps.Keys(st.Env)) {
		if envName.MatchString(k) {
			r.WriteString("export " + k + "=" + shQuote(st.Env[k]) + "; ")
			ps.WriteString("; $env:" + k + " = " + psQuote(st.Env[k]))
		}
	}
	var unset []string
	for _, k := range st.Unset {
		if envName.MatchString(k) {
			unset = append(unset, k)
			ps.WriteString("; Remove-Item -LiteralPath Env:" + k + " -ErrorAction Ignore")
		}
	}
	if len(unset) > 0 {
		r.WriteString("__desk_u " + strings.Join(unset, " ") + "; ")
	}
	t.psRestore = ps.String()
	if strings.HasPrefix(st.Cwd, "/") {
		r.WriteString("cd " + shQuote(st.Cwd) + "; ")
	}
	t.restore = r.String()
	t.cwd = st.Cwd
}

// loginText is what the shell printed above its first prompt (on line
// end): the login banner and motd, without the blank lines at its end.
// Plain text: their colors don't come back. mu is held.
func (t *Term) loginText(end int) string {
	var ls []string
	for i := max(0, end-keptLines); i < end; i++ {
		ls = append(ls, strings.TrimRight(t.line(i), " "))
	}
	for len(ls) > 0 && ls[len(ls)-1] == "" {
		ls = ls[:len(ls)-1]
	}
	return strings.Join(ls, "\n")
}

// writeHistory writes what goes back into the just-cleared emulator: the
// restored history (writeKept) into the scrollback, then the login output
// on screen above the coming prompt. mu is held.
func (t *Term) writeHistory() {
	if t.pending != nil {
		t.writeKept()
	}
	if t.banner != "" {
		t.emu.Write([]byte(strings.ReplaceAll(t.banner, "\n", "\r\n") + "\r\n"))
		t.banner = ""
	}
}

// writeKept writes the pending text into the just-cleared emulator, puts
// its blocks back where their lines landed (wrapping may move them) and
// pushes it all into the scrollback above the coming prompt. mu is held.
func (t *Term) writeKept() {
	st := t.pending
	t.pending = nil
	lines := strings.Split(st.Text, "\n")
	at := make([]int, len(lines)+1)
	cur := func() int { return t.emu.ScrollbackLen() + t.emu.CursorPosition().Y }
	for i, l := range lines {
		at[i] = cur()
		t.emu.Write([]byte(l + "\r\n"))
	}
	at[len(lines)] = cur()
	for _, b := range st.Blocks {
		if b.Start < 0 || b.End > len(lines) || b.Start >= b.End {
			continue
		}
		b.Start, b.End, b.N = at[b.Start], at[b.End], len(t.blocks)+1
		t.blocks = append(t.blocks, b)
	}
	t.emu.Write([]byte(strings.Repeat("\r\n", t.emu.Height()) + "\x1b[H")) // all of it in the scrollback, the cursor back on top
	t.rev++
}

// Cwd is the shell's directory as of its last prompt, "" until it reported one.
func (t *Term) Cwd() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cwd
}

// osc133 runs inside emu.Write, so mu is already held.
func (t *Term) osc133(data []byte) bool {
	parts := strings.Split(string(data), ";")
	if len(parts) < 2 {
		return true
	}
	t.integrated = true
	p := t.emu.CursorPosition()
	here := &pos{t.emu.ScrollbackLen() + p.Y, p.X}
	switch parts[1] {
	case "B":
		t.b, t.c, t.typed = here, nil, false
		for i := range t.blocks { // anything at or below the new prompt was cleared away
			if t.blocks[i].End > here.line {
				t.blocks[i].stale = true
			}
		}
	case "C":
		t.c, t.cAt, t.typed = here, time.Now(), false
		if t.b != nil {
			t.cmd = strings.TrimSpace(strings.ReplaceAll(t.text(*t.b, *here), "\n", ""))
		}
	case "D":
		if t.c == nil {
			break // no command ran (empty line, or a second D: fish marks natively too)
		}
		if t.b != nil {
			if cmd := t.cmd; cmd != "" && !strings.Contains(cmd, "__desk_d") { // not our own injected line (fish 4 marks it natively)
				exit := 0
				if len(parts) > 2 {
					exit, _ = strconv.Atoi(parts[2])
				}
				end := here.line
				if here.x > 0 {
					end++
				}
				t.blocks = append(t.blocks, Block{
					N: len(t.blocks) + 1, Cmd: cmd,
					Output: t.text(*t.c, *here), Exit: exit, ByClaude: t.byClaude,
					Dur: time.Since(t.cAt), Start: t.b.line, End: end, PX: t.b.x,
				})
				if t.onBlock != nil {
					t.onBlock(t.blocks[len(t.blocks)-1])
				}
			}
		}
		if !strings.Contains(t.cmd, "__desk_d") { // a command Claude typed while our injected line still ran stays Claude's
			t.byClaude = false
		}
		t.b, t.c = nil, nil
	}
	return true
}

func (t *Term) cell(x, abs, sb int) *uv.Cell {
	if abs < sb {
		return t.emu.ScrollbackCellAt(x, abs)
	}
	if x >= t.emu.Width() {
		return nil
	}
	return t.emu.CellAt(x, abs-sb)
}

// ponytail: absolute line numbers drift once scrollback hits its cap (10k lines);
// track trimmed lines if long sessions need exact blocks.
func (t *Term) line(abs int) string {
	sb := t.emu.ScrollbackLen()
	var s strings.Builder
	for x := 0; ; x++ {
		c := t.cell(x, abs, sb)
		if c == nil {
			break
		}
		if c.Content == "" {
			if c.Width == 0 {
				continue // trailing half of a wide char
			}
			s.WriteByte(' ')
			continue
		}
		s.WriteString(c.Content)
	}
	return s.String()
}

// text returns the screen text between two positions, trailing spaces trimmed per line.
func (t *Term) text(from, to pos) string {
	var lines []string
	for l := from.line; l <= to.line; l++ {
		s := []rune(t.line(l))
		start, end := 0, len(s)
		if l == from.line {
			start = min(from.x, len(s))
		}
		if l == to.line {
			if to.x == 0 {
				break
			}
			end = min(to.x, len(s))
		}
		lines = append(lines, strings.TrimRight(string(s[start:end]), " "))
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// renderLine draws one absolute line w cells wide, painting tint under cells
// that have no background of their own.
func (t *Term) renderLine(abs, sb, w int, tint color.Color) string {
	var out, run strings.Builder
	var cur uv.Style
	flush := func() {
		if run.Len() > 0 {
			out.WriteString(cur.Styled(run.String()))
			run.Reset()
		}
	}
	for x := 0; x < w; x++ {
		c := t.cell(x, abs, sb)
		content, st := " ", uv.Style{}
		if c != nil {
			if c.Content == "" && c.Width == 0 {
				continue
			}
			if c.Content != "" {
				content = c.Content
			}
			st = c.Style
		}
		if tint != nil && st.Bg == nil {
			st.Bg = tint
		}
		if !st.Equal(&cur) {
			flush()
			cur = st
		}
		run.WriteString(content)
	}
	flush()
	return out.String()
}

// Rows renders the h visible lines, scroll lines above the live screen.
func (t *Term) Rows(scroll int, tint func(n int) color.Color) (rows []Row, cx, cy int, cursor bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	sb, h, w := t.emu.ScrollbackLen(), t.emu.Height(), t.emu.Width()
	alt := t.emu.IsAltScreen() // htop, less, vim…: no block decoration, no scrolling
	if alt {
		scroll = 0
	}
	top := max(0, sb-scroll)
	for i := 0; i < h; i++ {
		abs := top + i
		var r Row
		if !alt {
			r.Running = t.c != nil && abs >= t.b.line
			r.Typed = t.typed && t.c == nil && abs == sb+t.emu.CursorPosition().Y
			for _, b := range t.blocks {
				if !b.stale && abs >= b.Start && abs < b.End {
					r.N, r.First = b.N, abs == b.Start
				}
			}
		}
		r.Text = t.renderLine(abs, sb, w, tint(r.N))
		rows = append(rows, r)
	}
	p := t.emu.CursorPosition()
	return rows, p.X, p.Y, scroll == 0 && !t.emu.CursorHidden()
}

// Run is cells of one style on a row, for GUI renderers.
type Run struct {
	X, W  int // first column and width, in cells
	Text  string
	Style uv.Style
}

// Grid is the visible screen, scroll lines up, as styled runs per row, with
// each row's block info (as in Rows) and the cursor.
func (t *Term) Grid(scroll int) (runs [][]Run, rows []Row, cx, cy int, cursor bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	sb, h, w := t.emu.ScrollbackLen(), t.emu.Height(), t.emu.Width()
	alt := t.emu.IsAltScreen()
	if alt {
		scroll = 0
	}
	top := max(0, sb-scroll)
	runs, rows = make([][]Run, h), make([]Row, h)
	for i := range h {
		abs := top + i
		if !alt {
			r := &rows[i]
			r.Running = t.c != nil && abs >= t.b.line
			if r.Running && abs == t.b.line {
				r.First, r.PX = true, t.b.x
			}
			for _, b := range t.blocks {
				if !b.stale && abs >= b.Start && abs < b.End {
					r.N, r.First, r.PX = b.N, abs == b.Start, b.PX
				}
			}
		}
		cur := -1 // index of the open run in runs[i]
		var text strings.Builder
		for x := 0; x < w; x++ {
			c := t.cell(x, abs, sb)
			content, st, cw := " ", uv.Style{}, 1
			if c != nil {
				if c.Content == "" && c.Width == 0 {
					continue // trailing half of a wide char
				}
				if c.Content != "" {
					content, cw = c.Content, max(1, c.Width)
				}
				st = c.Style
			}
			if cur < 0 || !st.Equal(&runs[i][cur].Style) {
				if cur >= 0 {
					runs[i][cur].Text = text.String()
					text.Reset()
				}
				runs[i] = append(runs[i], Run{X: x, Style: st})
				cur = len(runs[i]) - 1
			}
			text.WriteString(content)
			runs[i][cur].W += cw
		}
		if cur >= 0 {
			runs[i][cur].Text = text.String()
		}
	}
	p := t.emu.CursorPosition()
	return runs, rows, p.X, p.Y, scroll == 0 && !t.emu.CursorHidden()
}

// Screen is a text snapshot of what the terminal shows right now, so Claude can
// "see" full-screen programs (htop, less, a running tail -f) that never become blocks.
type Screen struct {
	Text    string
	Alt     bool   // a full-screen program owns the terminal
	Running string // command still running, if any
}

func (t *Term) Screen() Screen {
	t.mu.Lock()
	defer t.mu.Unlock()
	sb := t.emu.ScrollbackLen()
	var lines []string
	for y := 0; y < t.emu.Height(); y++ {
		lines = append(lines, strings.TrimRight(t.line(sb+y), " "))
	}
	s := Screen{Text: strings.TrimRight(strings.Join(lines, "\n"), "\n"), Alt: t.emu.IsAltScreen()}
	if t.c != nil {
		s.Running = t.cmd
	}
	return s
}

// Unwrapped is the screen text with the rows the terminal wrapped (autowrap,
// not a newline) joined back, so messages can be matched.
func (t *Term) Unwrapped() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	sb := t.emu.ScrollbackLen()
	var out strings.Builder
	for y := 0; y < t.emu.Height(); y++ {
		if t.emu.Wrapped(y) {
			out.WriteString(t.line(sb + y))
			continue
		}
		out.WriteString(strings.TrimRight(t.line(sb+y), " ") + "\n")
	}
	return out.String()
}

// wrapped reports whether absolute line abs continues on the next (autowrap).
func (t *Term) wrapped(abs, sb int) bool {
	if abs < sb {
		return t.emu.ScrollbackWrapped(abs)
	}
	return t.emu.Wrapped(abs - sb)
}

// Live reports, cheaply, whether a snapshot would be sent automatically.
func (t *Term) Live() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.emu.IsAltScreen() || t.c != nil
}

// WheelToApp hands a wheel event to a full-screen program (htop, less, vim)
// when it owns the terminal; false means desk should scroll its own buffer.
// The emulator only encodes it if the program asked for mouse reports.
func (t *Term) WheelToApp(ev uv.MouseWheelEvent) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.emu.IsAltScreen() {
		return false
	}
	t.emu.SendMouse(ev)
	return true
}

// Find gives the absolute lines (scrollback, then screen) that match search
// q (fzf's syntax), oldest first.
func (t *Term) Find(q string) []int {
	if strings.TrimSpace(q) == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var hits []int
	for abs := range t.emu.ScrollbackLen() + t.emu.Height() {
		if _, ok := fzfScore(q, t.line(abs)); ok {
			hits = append(hits, abs)
		}
	}
	return hits
}

// ScrollFor is the scroll offset that shows absolute line abs a third of the
// way down the screen (0: the live screen).
func (t *Term) ScrollFor(abs int) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	sb := t.emu.ScrollbackLen()
	return max(0, min(sb, sb-(abs-t.emu.Height()/3)))
}

// Scrollback is how many lines sit above the live screen.
func (t *Term) Scrollback() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.emu.ScrollbackLen()
}

// Connected reports that the shell got going: its SSH session started or a
// prompt showed up, or the server asked something (the user is answering:
// it isn't a timeout; a connection that fails after all clears it).
func (t *Term) Connected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.up || t.injected || t.integrated || t.reached
}

// Lost says the link dropped (the session ended with the connection, not by
// the remote shell's exit) after the shell had got going, and desk didn't
// hang it up.
func (t *Term) Lost() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exited && !t.hup && t.dropped && (t.up || t.injected || t.integrated || t.reached)
}

// Err is why the SSH connection couldn't be made (a *sshc.HostKeyChangedError,
// a *sshc.NegotiationError…), nil otherwise.
func (t *Term) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

func (t *Term) markReached() {
	t.mu.Lock()
	t.reached = true
	t.mu.Unlock()
}

func (t *Term) State() (integrated, exited bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.integrated, t.exited
}

func (t *Term) Blocks() []Block {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Block(nil), t.blocks...)
}

func (t *Term) SendKey(k uv.KeyEvent) {
	// vt matches keys by struct equality: a lock modifier (kitty reports
	// NumLock/CapsLock), a repeat flag or the text kitty attaches to ctrl+c
	// makes ↑ ↓ ^C and friends send nothing
	if p, ok := k.(uv.KeyPressEvent); ok {
		p.Mod &^= uv.ModCapsLock | uv.ModNumLock | uv.ModScrollLock
		p.IsRepeat = false
		if p.Mod&uv.ModCtrl != 0 {
			p.Text = ""
		}
		k = p
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.emu.SendKey(k)
}

func (t *Term) Paste(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.emu.Paste(s)
}

// Run executes a command for Claude at the shared prompt and waits for its
// block. If it outlives timeout, Claude gets the screen instead and the
// command keeps running in front of the user.
func (t *Term) Run(cmd string, timeout time.Duration) toolResult {
	if strings.Contains(cmd, "\n") {
		return toolResult{Text: "one command line only", IsError: true}
	}
	t.mu.Lock()
	idle := t.idle()
	n := len(t.blocks)
	if idle {
		t.byClaude = true
		io.WriteString(t.pty, "\x05\x15"+cmd+"\r")
	}
	t.mu.Unlock()
	if !idle {
		s := t.Screen()
		return toolResult{Text: fmt.Sprintf("this pane is busy (running %q); not run. Use split to run it in another pane. Screen:\n%s", s.Running, s.Text), IsError: true}
	}
	var b Block
	found := false
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		t.mu.Lock()
		if found = len(t.blocks) > n; found {
			b = t.blocks[len(t.blocks)-1]
		}
		// done once the next prompt is up too (or the shell is gone): a run
		// straight after the block would otherwise find the pane busy
		done := found && (t.idle() || t.exited)
		t.mu.Unlock()
		if done {
			break
		}
	}
	if found {
		out := b.Output
		if lines := strings.Split(out, "\n"); len(lines) > 300 {
			out = fmt.Sprintf("[…%d lines cut]\n", len(lines)-300) + strings.Join(lines[len(lines)-300:], "\n")
		}
		return toolResult{Text: fmt.Sprintf("block #%d exit=%d\n%s", b.N, b.Exit, out), IsError: b.Exit != 0}
	}
	s := t.Screen()
	return toolResult{Text: fmt.Sprintf("still running after %s (left running for the user). Screen:\n%s", timeout, s.Text), IsError: true}
}

// Status is one line on what the pane is doing, for Claude's pane list.
func (t *Term) Status() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case t.c != nil && t.emu.IsAltScreen():
		return "running " + t.cmd + " (full screen)"
	case t.c != nil:
		return "running " + t.cmd
	case t.emu.IsAltScreen():
		return "full-screen program"
	case t.b != nil:
		return "idle prompt"
	}
	return "starting"
}

// sudoPrompt is a password prompt ending the cursor line: sudo ("[sudo]
// password for deploy:"), sudo-rs ("[sudo: authenticate] Password:"), doas
// ("doas (deploy@web) password:"), su and sudo -p ("Password:"), and the
// Spanish locale ("[sudo] contraseña para deploy:", "Contraseña:").
var sudoPrompt = regexp.MustCompile(`(?i)(?:^|\]\s*|\)\s*)(?:password|contraseña)(?:\s+(?:for|para)\s+([^:\s]+))?\s*:\s*$`)

// SudoPrompt reports sudo (or su, doas) asking for a password on the cursor
// line, and for which user when it says ("" otherwise).
func (t *Term) SudoPrompt() (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.emu.IsAltScreen() {
		return "", false
	}
	p := t.emu.CursorPosition()
	l := []rune(t.line(t.emu.ScrollbackLen() + p.Y))
	m := sudoPrompt.FindStringSubmatch(string(l[:min(p.X, len(l))]))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// TypeSecret answers a hidden prompt (sudo) for the user: straight to the
// pty, never echoed, never marked as Claude's.
func (t *Term) TypeSecret(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	io.WriteString(t.pty, s+"\r")
}

// AtPrompt reports an idle shell prompt (marks seen, nothing running, no
// full-screen program): the only moment typing for Claude is safe.
func (t *Term) AtPrompt() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.idle()
}

// idle is AtPrompt with mu held. A restored pane isn't idle until its history
// is back in: fish marks a prompt before the replay, and a command run then
// would take the restored blocks for its own.
func (t *Term) idle() bool {
	return t.b != nil && t.c == nil && t.pending == nil && !t.emu.IsAltScreen()
}

// ShellReads reports that keys typed now would go to the shell, not to a
// program: at its prompt, or between a command's end and the next prompt,
// where an Enter would run them without run's approval.
func (t *Term) ShellReads() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.shellReads()
}

func (t *Term) shellReads() bool {
	return t.c == nil && !t.emu.IsAltScreen() && (t.b != nil || t.integrated)
}

// TypeForClaude puts a command at the prompt without running it. One line goes
// in after ^E^U (readline: end, kill line — ^Y brings back what was there);
// several lines go as a paste so the shell's bracketed paste keeps them unrun.
// ponytail: assumes bash readline in emacs mode; vi mode needs other keys.
func (t *Term) TypeForClaude(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.byClaude, t.typed = true, true
	if strings.Contains(s, "\n") {
		t.emu.Paste(s)
		return
	}
	io.WriteString(t.pty, "\x05\x15"+s)
}

// SendKeys types into whatever owns the terminal (htop, less, a y/n prompt)
// for Claude, then returns the screen. keys is text plus <name> for special
// keys: <enter> <esc> <tab> <space> <bs> <up> <down> <left> <right> <pgup>
// <pgdn> <home> <end> <f1>…<f12> <ctrl-x>. At an idle prompt it refuses: run
// is the gated way to execute commands there.
func (t *Term) SendKeys(keys string) toolResult {
	evs, err := parseKeys(keys)
	if err != nil {
		return toolResult{Text: err.Error(), IsError: true}
	}
	t.mu.Lock()
	if t.shellReads() {
		t.mu.Unlock()
		return toolResult{Text: "the terminal is at an idle shell prompt; use run for commands", IsError: true}
	}
	for _, k := range evs {
		t.emu.SendKey(k)
	}
	t.mu.Unlock()
	time.Sleep(400 * time.Millisecond) // let the program redraw
	s := t.Screen()
	return toolResult{Text: fmt.Sprintf("sent. alt=%t running=%q\n%s", s.Alt, s.Running, s.Text)}
}

var namedKeys = map[string]rune{
	"enter": uv.KeyEnter, "esc": uv.KeyEscape, "tab": uv.KeyTab, "space": uv.KeySpace, "bs": uv.KeyBackspace,
	"up": uv.KeyUp, "down": uv.KeyDown, "left": uv.KeyLeft, "right": uv.KeyRight, "pgup": uv.KeyPgUp,
	"pgdn": uv.KeyPgDown, "home": uv.KeyHome, "end": uv.KeyEnd, "del": uv.KeyDelete,
	"f1": uv.KeyF1, "f2": uv.KeyF2, "f3": uv.KeyF3, "f4": uv.KeyF4, "f5": uv.KeyF5, "f6": uv.KeyF6,
	"f7": uv.KeyF7, "f8": uv.KeyF8, "f9": uv.KeyF9, "f10": uv.KeyF10, "f11": uv.KeyF11, "f12": uv.KeyF12,
}

func parseKeys(s string) ([]uv.KeyPressEvent, error) {
	var evs []uv.KeyPressEvent
	for s != "" {
		if s[0] == '<' {
			if end := strings.IndexByte(s, '>'); end > 1 {
				name := strings.ToLower(s[1:end])
				if c, ok := namedKeys[name]; ok {
					evs = append(evs, uv.KeyPressEvent{Code: c})
				} else if r := []rune(strings.TrimPrefix(name, "ctrl-")); strings.HasPrefix(name, "ctrl-") && len(r) == 1 {
					evs = append(evs, uv.KeyPressEvent{Code: r[0], Mod: uv.ModCtrl})
				} else {
					return nil, fmt.Errorf("unknown key <%s>", name)
				}
				s = s[end+1:]
				continue
			}
		}
		r := []rune(s)[0]
		evs = append(evs, uv.KeyPressEvent{Code: r, Text: string(r)})
		s = s[len(string(r)):]
	}
	return evs, nil
}

// Size is the emulator's columns and rows.
func (t *Term) Size() (w, h int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.emu.Width(), t.emu.Height()
}

func (t *Term) Resize(w, h int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if w == t.emu.Width() && h == t.emu.Height() {
		return
	}
	if mv := t.emu.ResizeMap(w, h); mv != nil {
		t.reflowed(mv)
	}
	t.ver.Add(1)
	if t.pty != nil {
		t.pty.Resize(w, h)
	}
	if t.rec != nil {
		t.rec.event("r", fmt.Appendf(nil, "%dx%d", w, h))
	}
}

// reflowed moves the blocks and the open block's marks to where the emulator
// rewrapped their lines (mv: old absolute position -> new). mu is held.
func (t *Term) reflowed(mv func(line, x int) (int, int)) {
	for i := range t.blocks {
		b := &t.blocks[i]
		start := b.Start
		b.Start, _ = mv(start, 0)
		b.End, _ = mv(b.End, 0)
		_, b.PX = mv(start, b.PX)
	}
	for _, p := range []*pos{t.b, t.c} {
		if p != nil {
			p.line, p.x = mv(p.line, p.x)
		}
	}
}

// Close hangs up the shell (or its SSH session).
func (t *Term) Close() {
	t.mu.Lock()
	t.exited, t.hup = true, true
	t.mu.Unlock()
	t.pty.Hangup()
}

// wordAt is the run of line around i that a double click selects, [lo, hi):
// word characters (paths, URLs, host:port count as one word), else the
// spaces or the other characters around i.
func wordAt(line []rune, i int) (int, int) {
	if i < 0 || i >= len(line) {
		return i, i
	}
	class := func(r rune) int {
		switch {
		case r == ' ' || r == '\t':
			return 0
		case unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-_./~:@%+=?&#", r):
			return 1
		}
		return 2
	}
	c, lo, hi := class(line[i]), i, i+1
	for lo > 0 && class(line[lo-1]) == c {
		lo--
	}
	for hi < len(line) && class(line[hi]) == c {
		hi++
	}
	return lo, hi
}
