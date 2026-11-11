package infra

import (
	"github.com/lfdominguez/desk/internal/testwait"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestHostCmd: with HostCmd set, a host's pane runs it (in the pane's dir)
// instead of ssh; local panes keep their bash.
func TestHostCmd(t *testing.T) {
	needSh(t)
	var asked []string
	HostCmd = func(host string) *exec.Cmd {
		asked = append(asked, host)
		return exec.Command("bash", "--norc", "-i")
	}
	defer func() { HostCmd = nil }()
	dir, _ := filepath.EvalSymlinks(t.TempDir()) // as the shell's pwd says it (macOS: /private/var)
	tm, err := NewTerm("web-02", dir, 80, 24, func() {}, paneState{})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close()
	for end := time.Now().Add(testwait.Long); time.Now().Before(end) && tm.Cwd() == ""; time.Sleep(50 * time.Millisecond) {
	}
	if !slices.Equal(asked, []string{"web-02"}) || tm.Host != "web-02" || tm.Cwd() != dir {
		t.Fatalf("asked %q, host %q, cwd %q", asked, tm.Host, tm.Cwd())
	}
	l, err := NewTerm("local", dir, 80, 24, func() {}, paneState{})
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	if len(asked) != 1 {
		t.Errorf("a local pane went through HostCmd: %q", asked)
	}
}

// TestClaudeCmd: with ClaudeCmd set, claude's place goes to it, with the
// same arguments, whatever account owns the folder.
func TestClaudeCmd(t *testing.T) {
	ClaudeCmd = func(args ...string) *exec.Cmd { return exec.Command("fake", args...) }
	defer func() { ClaudeCmd = nil }()
	c, err := claudeCmd(t.TempDir(), "-p", "--verbose")
	if err != nil || !slices.Equal(c.Args, []string{"fake", "-p", "--verbose"}) {
		t.Fatalf("%v %v", c, err)
	}
}

// TestGen: each redraw request bumps the count desk's frame compares.
func TestGen(t *testing.T) {
	w := &Workspace{u: newUI(&driver{}, guiLayout{}, func() {})}
	g := w.Gen()
	w.u.redraw()
	w.u.redraw()
	if w.Gen() != g+2 {
		t.Fatalf("gen %d after %d", w.Gen(), g)
	}
}
