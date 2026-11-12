package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBinInstalled: without claude on PATH, the native installer's one in
// ~/.local/bin is found, and Command runs it.
func TestBinInstalled(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	os.MkdirAll(bin, 0o755)
	writeFake(t, bin, "claude", "#!/bin/sh\n", "")
	t.Setenv("PATH", t.TempDir())
	got, err := Bin(home)
	if err != nil || got != filepath.Join(bin, "claude"+exe) {
		t.Fatalf("Bin = %q, %v", got, err)
	}
	if c, err := Command(home, "", "-p"); err != nil || c.Path != got {
		t.Errorf("Command runs %v, %v; want %s", c, err, got)
	}
}

// TestBinPathWins: claude on PATH goes before the installed one.
func TestBinPathWins(t *testing.T) {
	home, path := t.TempDir(), t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	os.MkdirAll(bin, 0o755)
	writeFake(t, bin, "claude", "#!/bin/sh\n", "")
	writeFake(t, path, "claude", "#!/bin/sh\n", "")
	t.Setenv("PATH", path)
	if got, err := Bin(home); err != nil || filepath.Dir(got) != path {
		t.Errorf("Bin = %q, %v; want PATH's", got, err)
	}
}

// TestBinNone: neither gives an error naming claude and where it looked;
// a folder named claude doesn't count.
func TestBinNone(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(installed("linux", home)+exe, 0o755)
	t.Setenv("PATH", t.TempDir())
	_, err := Bin(home)
	if err == nil || !strings.Contains(err.Error(), "claude") || !strings.Contains(err.Error(), filepath.Join(home, ".local", "bin")) {
		t.Errorf("Bin: %v", err)
	}
}

// TestInstalledWindows: claude.exe under the profile folder, never the
// desktop app's in WindowsApps.
func TestInstalledWindows(t *testing.T) {
	p := installed("windows", "home")
	if p != filepath.Join("home", ".local", "bin", "claude.exe") || strings.Contains(p, "WindowsApps") {
		t.Errorf("windows: %s", p)
	}
	if p := installed("darwin", "home"); filepath.Base(p) != "claude" {
		t.Errorf("darwin: %s", p)
	}
}
