// desk is a personal work manager: a Kanban board of cards, in a Gio window.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/x/notify"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/telemetry"
	"github.com/lfdominguez/desk/internal/ui"
)

// runUI, appMain and exit are the window and the process; tests replace them.
var (
	runUI   = ui.Run
	appMain = app.Main
	exit    = os.Exit
	// tellRunning says so where a desk started from a launcher shows it,
	// which has no terminal: a desktop notification.
	tellRunning = func() { notify.Push("desk is already running", "Its window is open: switch to it.") }
)

// held is the board's lock, kept for the life of the process: a lost
// reference would let the GC close the file and the lock with it.
var held *os.File

// version is set at build time (-ldflags "-X main.version=2026.11.03");
// a plain go build says its commit instead (versionOf).
var version = ""

func main() {
	attachConsole()
	exit(run(os.Args))
}

// versionOf is v, else the commit the binary was built from ("dev-1a2b3c4",
// "-dirty" with local changes), else "dev".
func versionOf(v string, bi *debug.BuildInfo) string {
	if v != "" {
		return v
	}
	rev, dirty := "", ""
	for _, s := range bi.Settings {
		switch {
		case s.Key == "vcs.revision" && len(s.Value) >= 7:
			rev = "-" + s.Value[:7]
		case s.Key == "vcs.modified" && s.Value == "true":
			dirty = "-dirty"
		}
	}
	return "dev" + rev + dirty
}

// fixShell makes SHELL a shell that is there. desk hands it to all it runs,
// and a launcher's /bin/bash (NixOS has none) broke them: ssh runs a host's
// ProxyCommand or ProxyJump with it, Claude Code its Bash tool. The shell of
// that name on PATH takes its place, else bash; none found leaves it as is.
func fixShell() {
	s := os.Getenv("SHELL")
	if s == "" {
		return
	}
	if fi, err := os.Stat(s); err == nil && !fi.IsDir() {
		return
	}
	for _, name := range []string{filepath.Base(s), "bash"} {
		if p, err := exec.LookPath(name); err == nil {
			os.Setenv("SHELL", p)
			return
		}
	}
}

// loginPath, on macOS, takes PATH from the login shell: an app started from
// Finder or the Dock gets only /usr/bin:/bin:/usr/sbin:/sbin, where claude
// and the rest aren't. A shell that fails or takes over 3 s leaves PATH.
func loginPath(darwin bool) {
	if !darwin {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, cmp.Or(os.Getenv("SHELL"), "/bin/zsh"), "-lc", `printf %s "$PATH"`).Output()
	if p := strings.TrimSpace(string(out)); err == nil && p != "" {
		os.Setenv("PATH", p)
	}
}

// exeOnPath puts desk's own folder first on PATH, so what desk starts finds
// `desk peer` (Claude's Bash tool, the plugin's skill) when desk runs from
// a folder PATH doesn't name: the Windows zip, desk.app.
func exeOnPath(exe string, err error) {
	if err != nil {
		return
	}
	dir, path := filepath.Dir(exe), os.Getenv("PATH")
	if first, _, _ := strings.Cut(path, string(os.PathListSeparator)); first != dir {
		os.Setenv("PATH", dir+string(os.PathListSeparator)+path)
	}
}

// run is desk with args (os.Args); it returns the exit code, or never
// returns once the window is up (app.Main).
func run(args []string) int {
	bi, _ := debug.ReadBuildInfo()
	if bi == nil {
		bi = &debug.BuildInfo{}
	}
	ui.Version = versionOf(version, bi)
	fixShell()
	claude.BwrapOnPath()
	if len(args) == 2 && (args[1] == "--version" || args[1] == "version") {
		fmt.Println("desk", ui.Version)
		return 0
	}
	// desk runs itself for the infra workspace: as a tab Claude's MCP server,
	// and as the peer CLI other Claude sessions use.
	if len(args) >= 2 {
		switch args[1] {
		case "mcp":
			infra.MCPMain(args[2:])
			return 0
		case "peer":
			return infra.PeerMain(args[2:])
		case "servers": // import-asbru: Ásbrú's SSH hosts as a servers file
			return infra.ServersMain(args[2:], os.Stdout, os.Stderr)
		case "demo": // made-up organizations and scripted sessions, for recordings
			dir, err := ui.Demo()
			if err != nil {
				fmt.Fprintln(os.Stderr, "desk demo:", err)
				return 1
			}
			fmt.Fprintln(os.Stderr, "desk demo: in", dir)
		case "demo-infra": // the demo's scripted infra Claude, run by its workspace
			if len(args) > 2 {
				ui.DemoInfra(args[2], os.Stdin, os.Stdout)
			}
			return 0
		}
	}
	loginPath(runtime.GOOS == "darwin")
	exeOnPath(infra.Self())
	os.Unsetenv("DESK_SELF") // read by infra already; not for the programs desk starts
	path := kanban.Path()
	lock, err := kanban.Lock(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "desk:", err)
		if errors.Is(err, kanban.ErrLocked) {
			tellRunning()
		}
		return 1
	}
	held = lock
	kanban.Private(filepath.Dir(path))
	b, err := kanban.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "desk:", err)
		return 1
	}
	// A crash leaves its trace next to the board: desk runs detached, with no
	// terminal to print it on.
	crash := filepath.Join(filepath.Dir(path), "crash.log")
	if f, err := os.OpenFile(crash, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		debug.SetCrashOutput(f, debug.CrashOptions{})
		f.Close()
	}
	// Sentry, when the user gave a DSN: the last run's crashes, then this run's errors.
	if err := telemetry.Start(orgs.Telemetry().DSN, ui.Version); err != nil {
		fmt.Fprintln(os.Stderr, "desk: Sentry:", err)
	}
	telemetry.Crashes(crash)
	telemetry.Started(crash, ui.Version)
	go func() {
		code := 0
		if err := runUI(b, path); err != nil {
			fmt.Fprintln(os.Stderr, "desk:", err)
			code = 1
		}
		telemetry.Stop()
		exit(code)
	}()
	appMain()
	return 0 // app.Main never returns
}
