package claude

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Bin finds the claude executable: on PATH, else where Claude Code's
// native installer puts it (installed), which desk started from the Start
// menu or a launcher often doesn't have on PATH (the installer only adds it
// to the user's PATH for new shells, and some never do).
func Bin(home string) (string, error) {
	if p, err := exec.LookPath("claude"); err == nil {
		return p, nil
	}
	p := installed(runtime.GOOS, home)
	if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
		return p, nil
	}
	return "", fmt.Errorf("claude not found on PATH nor at %s", p)
}

// installed is the native installer's claude under home: ~/.local/bin on
// every OS, claude.exe on Windows (%USERPROFILE%\.local\bin). Never
// C:\Program Files\WindowsApps: its claude.exe is the desktop app, not the CLI.
func installed(goos, home string) string {
	name := "claude"
	if goos == "windows" {
		name += ".exe"
	}
	return filepath.Join(home, ".local", "bin", name)
}
