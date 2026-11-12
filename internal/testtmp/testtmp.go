// Package testtmp is a TestMain's own temp folder (and qa mutate's). What
// follows m.Run removes it, but a panic, go test's -timeout or a kill skips
// that, so each new one first removes those left by processes now gone:
// mutation runs crash test binaries by the thousand and would fill the disk.
package testtmp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// Dir makes <TMPDIR>/<prefix>-<pid>-<random> and removes every
// <prefix>-<pid>-* there whose process has ended.
func Dir(prefix string) (string, error) {
	tmp := os.TempDir()
	old, _ := filepath.Glob(filepath.Join(tmp, prefix+"-*-*"))
	for _, d := range old {
		pid, _, _ := strings.Cut(strings.TrimPrefix(filepath.Base(d), prefix+"-"), "-")
		if n, err := strconv.Atoi(pid); err == nil && !alive(n) {
			os.RemoveAll(d)
		}
	}
	return os.MkdirTemp(tmp, fmt.Sprintf("%s-%d-", prefix, os.Getpid()))
}

// alive: signal 0 checks a process without touching it (EPERM: someone
// else's, alive); Windows has no signal 0, but FindProcess fails there for
// a process that has ended.
func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err == nil && runtime.GOOS != "windows" {
		err = p.Signal(syscall.Signal(0))
	}
	return err == nil || errors.Is(err, syscall.EPERM)
}
