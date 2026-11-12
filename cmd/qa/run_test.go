package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lfdominguez/desk/internal/testwait"
)

// stdout returns what f printed.
func stdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	f()
	w.Close()
	return <-done
}

// module writes a throwaway module "desk" with internal/a (Add untested,
// a string +) and internal/b (one ignored file that doesn't parse), and
// makes it the working directory.
func module(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("qa drives the go command, not on PATH here:", err)
	}
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module desk\n\ngo 1.26\n",
		"internal/a/a.go": `package a

func Max(x, y int) int {
	if x > y {
		return x
	}
	return y
}

func Add(x, y int) int { return x + y }

func Cat(x, y string) string { return x + y }
`,
		"internal/a/a_test.go": `package a

import "testing"

func TestMax(t *testing.T) {
	if Max(2, 1) != 2 || Max(1, 2) != 2 || Cat("a", "b") != "ab" {
		t.Fatal("max")
	}
}
`,
		"internal/b/b.go":      "package b\n\nfunc One() int { return 1 }\n",
		"internal/b/bad.go":    "//go:build ignore\n\npackage b\n\nfunc {\n",
		"internal/b/b_test.go": "package b\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) { One() }\n",
	}
	for name, src := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	return dir
}

// failing adds internal/c, whose test fails.
func failing(t *testing.T, dir string) {
	os.MkdirAll(filepath.Join(dir, "internal/c"), 0o755)
	os.WriteFile(filepath.Join(dir, "internal/c/c_test.go"), []byte("package c\n\nimport \"testing\"\n\nfunc TestFail(t *testing.T) { t.Fatal(\"no\") }\n"), 0o644)
}

// TestCover: under a floor and without one fail, at or over passes, failing
// tests fail.
func TestCover(t *testing.T) {
	dir := module(t)
	old := floors
	defer func() { floors = old }()

	floors = map[string]float64{"desk/internal/a": 90}
	var err error
	stdout(t, func() { err = cover(nil) })
	if err == nil || !strings.Contains(err.Error(), "desk/internal/a: 80.0% is under its floor 90%") ||
		!strings.Contains(err.Error(), "desk/internal/b: 100.0%, no floor") {
		t.Errorf("under floor: %v", err)
	}

	floors = map[string]float64{"desk/internal/a": 80, "desk/internal/b": 100}
	out := stdout(t, func() { err = cover(nil) })
	if err != nil || !strings.Contains(out, "every package at or over its coverage floor") {
		t.Errorf("at floor: %v %q", err, out)
	}

	failing(t, dir)
	rep := filepath.Join(t.TempDir(), "r.md")
	stdout(t, func() { err = cover([]string{"-report", rep}) })
	if err == nil || err.Error() != "tests failed" {
		t.Errorf("failing tests: %v", err)
	}
	if b, _ := os.ReadFile(rep); !strings.Contains(string(b), "- `TestFail`\n- `internal/c`\n") || !strings.Contains(string(b), "**2 of 2** at their floor") {
		t.Errorf("a failing run's report:\n%s", b)
	}
}

// TestCoverBase: -base checks the lines changed since a git ref: none, an
// untested new func, then a test for it; test files and lines without
// statements don't count.
func TestCoverBase(t *testing.T) {
	dir := module(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("-base asks git, not on PATH here:", err)
	}
	old := floors
	defer func() { floors = old }()
	floors = map[string]float64{"desk/internal/a": 0, "desk/internal/b": 0}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "base")

	var err error
	out := stdout(t, func() { err = cover([]string{"-base", "HEAD"}) })
	if err != nil || !strings.Contains(out, "no changed lines with statements since HEAD") {
		t.Errorf("no change: %v %q", err, out)
	}

	a := filepath.Join(dir, "internal/a/a.go")
	src, _ := os.ReadFile(a)
	os.WriteFile(a, append(src, "\n// Sub is x-y.\nfunc Sub(x, y int) int {\n\treturn x - y\n}\n"...), 0o644)
	git("commit", "-qam", "sub")
	os.WriteFile(filepath.Join(dir, "internal/a/sub_test.go"), []byte("package a\n\nimport \"testing\"\n\nfunc TestNothing(t *testing.T) {}\n"), 0o644)
	rep := filepath.Join(t.TempDir(), "r.md")
	stdout(t, func() {
		err = cover([]string{"-base", "HEAD~1", "-report", rep, "-link", "https://example.com/blob/x/"})
	})
	if err == nil || !strings.Contains(err.Error(), "0.0% covered (0 of 3), under 98%") || !strings.Contains(err.Error(), "internal/a/a.go:15") {
		t.Errorf("untested: %v", err)
	}
	if b, _ := os.ReadFile(rep); !strings.Contains(string(b), "| `internal/a/a.go` | 0 / 3 · 0.0% |") ||
		!strings.Contains(string(b), "[L15–17](https://example.com/blob/x/internal/a/a.go#L15-L17)") || !strings.Contains(string(b), "since `HEAD~1`") {
		t.Errorf("an untested change's report:\n%s", b)
	}
	stdout(t, func() { err = cover([]string{"-base", "HEAD~1", "-min", "0"}) })
	if err != nil {
		t.Errorf("-min 0: %v", err)
	}

	os.WriteFile(filepath.Join(dir, "internal/a/sub_test.go"), []byte("package a\n\nimport \"testing\"\n\nfunc TestSub(t *testing.T) { Sub(2, 1) }\n"), 0o644)
	out = stdout(t, func() { err = cover([]string{"-base", "HEAD~1"}) })
	if err != nil || !strings.Contains(out, "changed lines since HEAD~1: 100.0% covered (3 of 3)") {
		t.Errorf("tested: %v %q", err, out)
	}

	stdout(t, func() { err = cover([]string{"-base", "nope"}) })
	if err == nil || !strings.Contains(err.Error(), "git merge-base nope HEAD") {
		t.Errorf("bad ref: %v", err)
	}
	if err = cover([]string{"-bogus"}); err == nil {
		t.Error("bad flag: no error")
	}
}

// TestProfileLines: lines that aren't profile blocks are skipped, and a
// go.mod without a module line is an error.
func TestProfileLines(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	prof := filepath.Join(dir, "c.out")
	os.WriteFile(prof, []byte("mode: set\nm/x.go:1.1,2.2 1 0\nm/x.go:junk\n"), 0o644)
	if _, _, err := profileLines(prof); err == nil {
		t.Error("no go.mod: no error")
	}
	os.WriteFile("go.mod", []byte("go 1.26\n"), 0o644)
	if _, err := modulePath(); err == nil || err.Error() != "go.mod has no module line" {
		t.Errorf("no module: %v", err)
	}
	os.WriteFile("go.mod", []byte("module m\n"), 0o644)
	stmts, ran, err := profileLines(prof)
	if err != nil || !stmts["x.go"][1] || !stmts["x.go"][2] || ran["x.go"][1] {
		t.Errorf("profile: %v %v %v", stmts, ran, err)
	}
	if _, _, err := profileLines(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing profile: no error")
	}
}

// TestMutate: one mutant killed, the untested + survives and is listed,
// the string + doesn't build.
func TestMutate(t *testing.T) {
	dir := module(t)
	var err error
	out := stdout(t, func() { err = mutate([]string{"-j", "2", "./internal/a"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"./internal/a: 3 mutants, 2 at a time",
		"./internal/a: 50.0% killed (1 killed, 0 timed out, 1 survived, 1 didn't build)",
		"survived (no test noticed):\n  internal/a/a.go:10: + → -",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	os.WriteFile(filepath.Join(dir, "flag.go"), []byte("package desk\n\nvar on, off = true, false\n"), 0o644)
	if ms, _ := mutants(dir, "flag.go"); len(ms) != 2 || ms[0].from+">"+ms[0].to != "true>false" || ms[1].from+">"+ms[1].to != "false>true" {
		t.Errorf("true/false mutants: %+v", ms)
	}
	if _, err := mutants("[", ""); err == nil {
		t.Error("bad glob: no error")
	}
	os.Mkdir(filepath.Join(dir, "internal/b/dir.go"), 0o755) // matched, unreadable
	if _, err := mutants(filepath.Join(dir, "internal/b"), "dir.go"); err == nil {
		t.Error("unreadable file: no error")
	}
	os.Remove(filepath.Join(dir, "internal/b/dir.go"))

	if err := mutate(nil); err == nil || !strings.Contains(err.Error(), "give the package folders") {
		t.Errorf("no dirs: %v", err)
	}
	if err := mutate([]string{"./internal/b"}); err == nil || !strings.Contains(err.Error(), "expected") {
		t.Errorf("file that doesn't parse: %v", err)
	}
	failing(t, dir)
	if err := mutate([]string{"./internal/c"}); err == nil || !strings.Contains(err.Error(), "./internal/c: tests fail before any change") {
		t.Errorf("failing tests: %v", err)
	}
	t.Setenv("TMPDIR", filepath.Join(dir, "missing"))
	if err := mutate([]string{"./internal/a"}); err == nil {
		t.Error("no temp dir: no error")
	}
}

// TestTry: a mutant past its time limit times out; one whose file or
// scratch dir is unusable is invalid.
func TestTry(t *testing.T) {
	dir := module(t)
	m := mutant{file: filepath.Join(dir, "internal/a/a.go"), from: "+", to: "-", off: 0, n: 0}
	if r := try("./internal/a", m, t.TempDir(), time.Nanosecond, ""); r != timedOut {
		t.Errorf("past limit: %v, want timedOut", r)
	}
	if r := try("./internal/a", mutant{file: filepath.Join(dir, "none.go")}, t.TempDir(), time.Minute, ""); r != invalid {
		t.Errorf("missing file: %v, want invalid", r)
	}
	if r := try("./internal/a", m, filepath.Join(dir, "go.mod", "sub"), time.Minute, ""); r != invalid {
		t.Errorf("unwritable dir: %v, want invalid", r)
	}
	w := t.TempDir()
	src, _ := os.ReadFile(m.file)
	m = mutant{file: m.file, from: ">", to: "<", off: strings.Index(string(src), "x > y") + 2, n: 1}
	if r := try("./internal/a", m, w, time.Minute, ""); r != killed {
		t.Errorf("mutant: %v, want killed", r)
	} else if _, err := os.Stat(filepath.Join(w, "cache")); err != nil {
		t.Errorf("no worker cache: %v", err)
	} else if _, err := os.Stat(filepath.Join(w, "tmp")); err == nil {
		t.Error("the mutant's temp folder stayed")
	}
	defer func() { exit = os.Exit }()
	code := 0
	exit = func(c int) { code = c }
	os.WriteFile(filepath.Join(dir, "internal/a/full_test.go"), []byte("package a\n\nimport \"testing\"\n\nfunc TestFull(t *testing.T) { t.Fatal(\"no space left on device\") }\n"), 0o644)
	m.to = ">" // builds and passes TestMax, so TestFull runs
	stdout(t, func() { try("./internal/a", m, w, time.Minute, "") })
	if code != 1 {
		t.Errorf("disk full: exit %d, want 1", code)
	}
	if _, err := os.Stat(w); err == nil {
		t.Error("disk full: the run's folder stayed")
	}
}

// TestOnInterrupt: Ctrl+C removes the run's folder and ends qa.
func TestOnInterrupt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no signals to send itself on Windows")
	}
	defer func() { exit = os.Exit }()
	code := make(chan int, 1)
	exit = func(c int) { code <- c }
	tmp := t.TempDir()
	stop := onInterrupt(tmp)
	defer stop()
	self, _ := os.FindProcess(os.Getpid())
	self.Signal(os.Interrupt)
	select {
	case c := <-code:
		if c != 130 {
			t.Errorf("exit %d, want 130", c)
		}
	case <-time.After(testwait.Long):
		t.Fatal("no exit")
	}
	if _, err := os.Stat(tmp); err == nil {
		t.Error("the folder stayed")
	}
}

// TestTestsByLine: each test's executed lines, from running it alone; a
// mutant reruns only the tests on its line, or all when none is.
func TestTestsByLine(t *testing.T) {
	dir := module(t)
	os.WriteFile(filepath.Join(dir, "internal/a/alone_test.go"), []byte("package a\n\nimport \"testing\"\n\nvar ran bool\n\nfunc TestFirst(t *testing.T) { ran = true }\n\nfunc TestNeedsFirst(t *testing.T) {\n\tif !ran {\n\t\tt.Fatal(\"alone\")\n\t}\n\tAdd(1, 2)\n}\n"), 0o644)
	var cov *coverage
	var err error
	out := stdout(t, func() { cov, err = testsByLine("./internal/a", filepath.Join(dir, "internal/a"), t.TempDir(), 2) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "left out, they fail alone: TestNeedsFirst") {
		t.Errorf("out %q", out)
	}
	a := filepath.Join(dir, "internal/a/a.go")
	for line, want := range map[int]string{4: "^(TestMax)$", 10: "", 12: "^(TestMax)$"} {
		if got := cov.run(mutant{file: a, line: line}); got != want {
			t.Errorf("line %d: %q, want %q", line, got, want)
		}
	}
	c := &coverage{}
	c.add("TestB", "mode: set\ndesk/x/f.go:3.1,5.2 1 1\ndesk/x/f.go:7.1,8.2 1 0\nbad\nnocolon 1 1\n")
	c.add("TestA", "desk/x/f.go:4.1,4.9 1 3\n")
	if got := c.run(mutant{file: "/w/f.go", line: 4}); got != "^(TestA|TestB)$" {
		t.Errorf("two tests: %q", got)
	}
	if got := c.run(mutant{file: "/w/f.go", line: 7}); got != "" {
		t.Errorf("unexecuted block: %q", got)
	}
	c.add("TestC", "desk/x/f.go:11.1,11.9 1 1\n")
	if got := c.run(mutant{file: "/w/f.go", line: 10, fnFrom: 9, fnTo: 12}); got != "^(TestC)$" {
		t.Errorf("line no test runs, in a func one does: %q", got)
	}
	if ms, _ := mutants(filepath.Join(dir, "internal/a"), "a.go"); len(ms) == 0 || ms[0].fnFrom != 3 || ms[0].fnTo != 8 {
		t.Errorf("Max's mutant should carry its func's lines 3-8: %+v", ms)
	}
	if _, err := testsByLine("./internal/nope", dir, t.TempDir(), 1); err == nil {
		t.Error("no package: no error")
	}
}

// TestReport: a dropped token reads "(nothing)", survivors sorted, and with
// nothing that built the score is 100%.
func TestReport(t *testing.T) {
	wd := mustWd()
	ms := []mutant{
		{file: filepath.Join(wd, "z.go"), from: "!", to: "", line: 3},
		{file: filepath.Join(wd, "a.go"), from: "<", to: ">=", line: 7},
		{file: filepath.Join(wd, "a.go"), from: "==", to: "!=", line: 1},
		{file: filepath.Join(wd, "a.go"), from: "+", to: "-", line: 2},
	}
	out := stdout(t, func() { report("./p", ms, []result{survived, survived, killed, timedOut}) })
	want := "./p: 50.0% killed (1 killed, 1 timed out, 2 survived, 0 didn't build)\n" +
		"survived (no test noticed):\n  a.go:7: < → >=\n  z.go:3: ! → (nothing)\n"
	if out != want {
		t.Errorf("report:\n%q\nwant\n%q", out, want)
	}
	out = stdout(t, func() { report("./p", ms[:1], []result{invalid}) })
	if out != "./p: 100.0% killed (0 killed, 0 timed out, 0 survived, 1 didn't build)\n" {
		t.Errorf("nothing built: %q", out)
	}
}

// TestMainExit: usage, unknown commands and failures exit non-zero.
func TestMainExit(t *testing.T) {
	old := os.Args
	defer func() { os.Args, exit = old, os.Exit }()
	var code int
	exit = func(c int) { code = c }
	for _, tc := range []struct {
		args []string
		want int
	}{
		{[]string{"qa"}, 2},
		{[]string{"qa", "bogus"}, 1},
		{[]string{"qa", "mutate"}, 1},
	} {
		code = 0
		os.Args = tc.args
		main()
		if code != tc.want {
			t.Errorf("%q: exit %d, want %d", tc.args, code, tc.want)
		}
	}
	module(t)
	oldFloors := floors
	defer func() { floors = oldFloors }()
	floors = map[string]float64{"desk/internal/a": 0, "desk/internal/b": 0}
	code = -1
	os.Args = []string{"qa", "cover"}
	stdout(t, main)
	if code != -1 {
		t.Errorf("cover passing: exit %d", code)
	}
}
