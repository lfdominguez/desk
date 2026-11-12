package testtmp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestDir: a folder of an ended process goes, a live one's and another
// prefix's stay, and the new one is named by this process.
func TestDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	gone := exec.Command(os.Args[0], "-test.run=^$")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(tmp, fmt.Sprintf("x-%d-1", gone.Process.Pid))
	live := filepath.Join(tmp, fmt.Sprintf("x-%d-1", os.Getppid()))
	other := filepath.Join(tmp, fmt.Sprintf("y-%d-1", gone.Process.Pid))
	odd := filepath.Join(tmp, "x-notapid-1")
	for _, d := range []string{dead, live, other, odd} {
		os.Mkdir(d, 0o755)
	}
	d, err := Dir("x")
	if err != nil || filepath.Dir(d) != tmp || filepath.Base(d)[:len(fmt.Sprint("x-", os.Getpid(), "-"))] != fmt.Sprint("x-", os.Getpid(), "-") {
		t.Fatalf("made %q", d)
	}
	if _, err := os.Stat(dead); err == nil {
		t.Error("an ended process's folder stayed")
	}
	for _, k := range []string{live, other, odd, d} {
		if _, err := os.Stat(k); err != nil {
			t.Errorf("%s went: %v", k, err)
		}
	}
}

func TestAlive(t *testing.T) {
	if !alive(os.Getpid()) || !alive(1) { // 1: someone else's (EPERM) or ours in a container
		t.Error("a live process")
	}
	gone := exec.Command(os.Args[0], "-test.run=^$")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	if alive(gone.Process.Pid) {
		t.Error("an ended process")
	}
}
