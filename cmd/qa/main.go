// Command qa checks desk's tests themselves, with only the Go toolchain:
//
//	go run ./cmd/qa cover            each package's coverage against its floor
//	go run ./cmd/qa cover -base REF  ... and the new lines since REF covered >= 98%
//	go run ./cmd/qa mutate [-j N] [-file S] ./internal/fzf ...
//
// mutate makes one small change at a time to a package's code (== to !=,
// < to >=, && to ||, + to -, a ! dropped, true to false, ++ to --), runs
// the package's tests on it through go test -overlay (the tree is never
// touched), and lists the changes no test noticed: the code's behavior
// its tests don't pin down. Run it under the nix-shell CLAUDE.md gives.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// floors is each package's coverage (%) that cover refuses to go under:
// what it was when the floor was set, rounded down. Raise one when its
// tests grow; never lower one to let a change in.
var floors = map[string]float64{
	"github.com/lfdominguez/desk/internal/claude":    99,
	"github.com/lfdominguez/desk/internal/clip":      100,
	"github.com/lfdominguez/desk/internal/draw":      100,
	"github.com/lfdominguez/desk/internal/fzf":       100,
	"github.com/lfdominguez/desk/internal/infra":     99,
	"github.com/lfdominguez/desk/internal/kanban":    100,
	"github.com/lfdominguez/desk/internal/keys":      100,
	"github.com/lfdominguez/desk/internal/links":     99,
	"github.com/lfdominguez/desk/internal/orgs":      99,
	"github.com/lfdominguez/desk/internal/sshc":      95,
	"github.com/lfdominguez/desk/internal/sshconfig": 97,
	"github.com/lfdominguez/desk/internal/term":      98,
	"github.com/lfdominguez/desk/internal/testwait":  100,
	"github.com/lfdominguez/desk/internal/tty":       100,
	"github.com/lfdominguez/desk/internal/ui":        99,
	"github.com/lfdominguez/desk/internal/ui/kit":    99,
	"github.com/lfdominguez/desk/internal/vault":     100,
}

var tags = []string{"-tags", "nox11"}

// exit is os.Exit; tests replace it.
var exit = os.Exit

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: qa cover [-base REF] [-min PCT] [-report FILE [-link URL]] | qa mutate [-j N] [-file S] PKGDIR...")
		exit(2)
		return
	}
	var err error
	switch os.Args[1] {
	case "cover":
		err = cover(os.Args[2:])
	case "mutate":
		err = mutate(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "qa:", err)
		exit(1)
	}
}

var coverLine = regexp.MustCompile(`^ok\s+(\S+)\s.*coverage: ([\d.]+)% of statements`)

// cover runs every package's tests with coverage and fails when one is
// under its floor, or has no floor yet. With -base REF it also fails when
// the code lines changed since REF (its merge base with HEAD, plus what isn't
// committed) are covered under -min percent: a pull request's new code.
func cover(args []string) error {
	fs := flag.NewFlagSet("cover", flag.ContinueOnError)
	base := fs.String("base", "", "also check the lines changed since this git ref")
	min := fs.Float64("min", 98, "the changed lines' least coverage (%), with -base")
	report := fs.String("report", "", "also write a Markdown report here (a pull request's comment), pass or fail")
	link := fs.String("link", "", "with -report, a blob URL at the checked commit (https://github.com/o/r/blob/<sha>) for the lines not run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	mod, _ := modulePath()
	rep := &covReport{mod: mod, link: strings.TrimSuffix(*link, "/"), base: *base, min: *min}
	defer rep.write(*report)
	targs := append([]string{"test", "-count=1", "-cover"}, tags...)
	prof := ""
	if *base != "" {
		f, err := os.CreateTemp("", "qa-cover-*.out")
		if err != nil {
			return err
		}
		f.Close()
		prof = f.Name()
		defer os.Remove(prof)
		targs = append(targs, "-coverprofile="+prof)
	}
	cmd := exec.Command("go", append(targs, "./internal/...")...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	os.Stdout.Write(out)
	rep.failed = failures(string(out))
	var bad []string
	for _, l := range strings.Split(string(out), "\n") {
		m := coverLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		got, _ := strconv.ParseFloat(m[2], 64)
		floor, ok := floors[m[1]]
		rep.pkgs = append(rep.pkgs, pkgCov{name: m[1], pct: got, floor: floor, hasFloor: ok})
		switch {
		case !ok:
			bad = append(bad, fmt.Sprintf("%s: %.1f%%, no floor: add it to floors in cmd/qa", m[1], got))
		case got < floor:
			bad = append(bad, fmt.Sprintf("%s: %.1f%% is under its floor %.0f%%", m[1], got, floor))
		}
	}
	if err != nil {
		return fmt.Errorf("tests failed")
	}
	if len(bad) > 0 {
		return fmt.Errorf("coverage:\n  %s", strings.Join(bad, "\n  "))
	}
	fmt.Println("qa: every package at or over its coverage floor")
	if *base != "" {
		return newCover(*base, prof, *min, rep)
	}
	return nil
}

// newCover checks the changed lines' coverage: of the lines changed since
// base that hold statements (in prof, a go test -coverprofile), at least min
// percent ran. Lines outside ./internal (cmd/) and in files this OS doesn't
// build count for nothing.
func newCover(base, prof string, min float64, rep *covReport) error {
	changed, err := changedLines(base)
	if err != nil {
		return err
	}
	stmts, ran, err := profileLines(prof)
	if err != nil {
		return err
	}
	var total, hit int
	var missed []string
	rep.checked = true
	for _, f := range slices.Sorted(maps.Keys(changed)) {
		fc := fileCov{path: f}
		for _, l := range changed[f] {
			if !stmts[f][l] {
				continue
			}
			total++
			fc.total++
			if ran[f][l] {
				hit++
				fc.hit++
			} else {
				missed = append(missed, fmt.Sprintf("%s:%d", f, l))
				fc.missed = append(fc.missed, l)
			}
		}
		if fc.total > 0 {
			rep.files = append(rep.files, fc)
		}
	}
	if total == 0 {
		fmt.Println("qa: no changed lines with statements since " + base)
		return nil
	}
	pct := 100 * float64(hit) / float64(total)
	if pct < min {
		return fmt.Errorf("changed lines since %s: %.1f%% covered (%d of %d), under %.0f%%; not run:\n  %s",
			base, pct, hit, total, min, strings.Join(missed, "\n  "))
	}
	fmt.Printf("qa: changed lines since %s: %.1f%% covered (%d of %d)\n", base, pct, hit, total)
	return nil
}

var hunk = regexp.MustCompile(`^@@ -\S+ \+(\d+)(?:,(\d+))? @@`)

// changedLines is, by file, the lines of non-test Go files added or changed
// since base's merge base with HEAD, the working tree included.
func changedLines(base string) (map[string][]int, error) {
	mb, err := exec.Command("git", "merge-base", base, "HEAD").Output()
	if err != nil {
		return nil, fmt.Errorf("git merge-base %s HEAD: %v", base, err)
	}
	out, err := exec.Command("git", "diff", "-U0", "--no-color", "--no-renames", strings.TrimSpace(string(mb)), "--", "*.go").Output()
	if err != nil {
		return nil, fmt.Errorf("git diff: %v", err)
	}
	lines := map[string][]int{}
	file := ""
	for _, l := range strings.Split(string(out), "\n") {
		if f, ok := strings.CutPrefix(l, "+++ "); ok {
			file = ""
			if f, ok := strings.CutPrefix(f, "b/"); ok && !strings.HasSuffix(f, "_test.go") {
				file = f
			}
			continue
		}
		m := hunk.FindStringSubmatch(l)
		if m == nil || file == "" {
			continue
		}
		start, _ := strconv.Atoi(m[1])
		n := 1
		if m[2] != "" {
			n, _ = strconv.Atoi(m[2])
		}
		for i := range n {
			lines[file] = append(lines[file], start+i)
		}
	}
	return lines, nil
}

// profileLines reads a coverprofile: by file (relative to the module), the
// lines inside a statement block, and those inside one that ran.
func profileLines(prof string) (stmts, ran map[string]map[int]bool, err error) {
	mod, err := modulePath()
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(prof)
	if err != nil {
		return nil, nil, err
	}
	stmts, ran = map[string]map[int]bool{}, map[string]map[int]bool{}
	for _, l := range strings.Split(string(data), "\n") {
		// path/file.go:L1.C1,L2.C2 statements count
		var file string
		var l1, c1, l2, c2, n, count int
		i := strings.LastIndex(l, ".go:")
		if i < 0 {
			continue
		}
		file = strings.TrimPrefix(l[:i+3], mod+"/")
		if _, err := fmt.Sscanf(l[i+4:], "%d.%d,%d.%d %d %d", &l1, &c1, &l2, &c2, &n, &count); err != nil {
			continue
		}
		if stmts[file] == nil {
			stmts[file], ran[file] = map[int]bool{}, map[int]bool{}
		}
		for x := l1; x <= l2; x++ {
			stmts[file][x] = true
			if count > 0 {
				ran[file][x] = true
			}
		}
	}
	return stmts, ran, nil
}

// modulePath is go.mod's module.
func modulePath() (string, error) {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(data), "\n") {
		if m, ok := strings.CutPrefix(strings.TrimSpace(l), "module "); ok {
			return strings.TrimSpace(m), nil
		}
	}
	return "", fmt.Errorf("go.mod has no module line")
}

// mutant is one change: in file, the bytes [off, off+n) become to.
type mutant struct {
	file, from, to string
	off, n, line   int
	fnFrom, fnTo   int // lines of the func it's in; 0 outside one
}

var swap = map[token.Token]string{
	token.EQL: "!=", token.NEQ: "==", token.LSS: ">=", token.GEQ: "<", token.GTR: "<=", token.LEQ: ">",
	token.LAND: "||", token.LOR: "&&", token.ADD: "-", token.SUB: "+",
}

// mutants is every change mutate makes to the package in dir.
func mutants(dir, only string) ([]mutant, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	var out []mutant
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || !strings.Contains(f, only) {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		fs := token.NewFileSet()
		af, err := parser.ParseFile(fs, f, src, 0)
		if err != nil {
			return nil, err
		}
		abs, _ := filepath.Abs(f)
		add := func(pos token.Pos, from, to string) {
			p := fs.Position(pos)
			m := mutant{file: abs, from: from, to: to, off: p.Offset, n: len(from), line: p.Line}
			for _, d := range af.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Pos() <= pos && pos < fd.End() {
					m.fnFrom, m.fnTo = fs.Position(fd.Pos()).Line, fs.Position(fd.End()).Line
				}
			}
			out = append(out, m)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BinaryExpr:
				if to, ok := swap[n.Op]; ok && !isString(n) {
					add(n.OpPos, n.Op.String(), to)
				}
			case *ast.UnaryExpr:
				if n.Op == token.NOT {
					add(n.OpPos, "!", "")
				}
			case *ast.IncDecStmt:
				add(n.TokPos, n.Tok.String(), map[token.Token]string{token.INC: "--", token.DEC: "++"}[n.Tok])
			case *ast.Ident:
				if n.Name == "true" || n.Name == "false" {
					add(n.Pos(), n.Name, map[string]string{"true": "false", "false": "true"}[n.Name])
				}
			case *ast.GenDecl:
				return n.Tok != token.IMPORT && n.Tok != token.CONST // constants' values: tables, not logic
			}
			return true
		})
	}
	return out, nil
}

// isString is a + whose side is a string literal: mutated it wouldn't build.
func isString(b *ast.BinaryExpr) bool {
	for _, e := range []ast.Expr{b.X, b.Y} {
		if l, ok := e.(*ast.BasicLit); ok && l.Kind == token.STRING {
			return true
		}
	}
	return false
}

type result int

const (
	killed result = iota
	survived
	invalid // didn't build: not a test's business
	timedOut
)

func mutate(args []string) error {
	fl := flag.NewFlagSet("mutate", flag.ExitOnError)
	jobs := fl.Int("j", max(1, runtime.NumCPU()/2), "mutants tested at once")
	only := fl.String("file", "", "only files whose path contains this")
	fl.Parse(args)
	if fl.NArg() == 0 {
		return fmt.Errorf("give the package folders, e.g. ./internal/fzf")
	}
	tmp, err := os.MkdirTemp("", "qa-mutate")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for _, dir := range fl.Args() {
		pkg := "./" + filepath.Clean(dir)
		start := time.Now()
		if out, err := gotest(context.Background(), pkg, "", ""); err != nil {
			return fmt.Errorf("%s: tests fail before any change:\n%s", pkg, out)
		}
		// a mutant that loops forever; the floor covers a worker's first,
		// cold-cache build of a package that links the whole app (cmd/desk)
		limit := 10*time.Since(start) + 3*time.Minute
		ms, err := mutants(dir, *only)
		if err != nil {
			return err
		}
		cov, err := testsByLine(pkg, dir, tmp, *jobs)
		if err != nil {
			return err
		}
		fmt.Printf("%s: %d mutants, %d at a time\n", pkg, len(ms), *jobs)
		res := make([]result, len(ms))
		var wg sync.WaitGroup
		next := make(chan int)
		for w := range *jobs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				wdir, n := filepath.Join(tmp, strconv.Itoa(w)), 0
				for i := range next {
					// Every mutant's build lands in the cache: emptied now and then, or
					// infra's 3700 fill tens of GB.
					if n++; n%100 == 0 {
						os.RemoveAll(filepath.Join(wdir, "cache"))
					}
					res[i] = try(pkg, ms[i], wdir, limit, cov.run(ms[i]))
				}
			}()
		}
		for i := range ms {
			next <- i
		}
		close(next)
		wg.Wait()
		report(pkg, ms, res)
	}
	return nil
}

// covBlock is a run of lines a test executed.
type covBlock struct {
	file     string // base name
	from, to int
	test     string
}

type coverage struct{ blocks []covBlock }

// testsByLine runs each of the package's tests alone with coverage, so a
// mutant only reruns the tests that execute its line: on infra or ui the whole
// suite per mutant would take hours. A test that fails alone (it leans on
// another's state) is left out of the map.
func testsByLine(pkg, dir, tmp string, jobs int) (*coverage, error) {
	bin, _ := filepath.Abs(filepath.Join(tmp, "pkg.test"))
	if out, err := exec.Command("go", append(append([]string{"test", "-c", "-cover", "-o", bin}, tags...), pkg)...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%s: building its tests: %s", pkg, out)
	}
	list := exec.Command(bin, "-test.list", ".")
	list.Dir = dir
	out, err := list.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: listing its tests: %w", pkg, err)
	}
	names := strings.Fields(string(out))
	cov := &coverage{}
	var mu sync.Mutex
	var alone []string
	var wg sync.WaitGroup
	next := make(chan int)
	for range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				prof := fmt.Sprintf("%s.%d.cov", bin, i)
				cmd := exec.Command(bin, "-test.run", "^"+names[i]+"$", "-test.count=1", "-test.coverprofile="+prof)
				cmd.Dir = dir
				err := cmd.Run()
				data, _ := os.ReadFile(prof)
				mu.Lock()
				if err != nil {
					alone = append(alone, names[i])
				} else {
					cov.add(names[i], string(data))
				}
				mu.Unlock()
			}
		}()
	}
	for i := range names {
		next <- i
	}
	close(next)
	wg.Wait()
	if len(alone) > 0 {
		sort.Strings(alone)
		fmt.Printf("%s: left out, they fail alone: %s\n", pkg, strings.Join(alone, " "))
	}
	return cov, nil
}

// add reads a coverage profile's executed blocks ("pkg/file.go:12.3,14.5 2 1").
func (c *coverage) add(test, profile string) {
	for _, l := range strings.Split(profile, "\n") {
		f := strings.Fields(l)
		if len(f) != 3 || f[2] == "0" {
			continue
		}
		file, span, ok := strings.Cut(f[0], ":")
		a, b, ok2 := strings.Cut(span, ",")
		if !ok || !ok2 {
			continue
		}
		from, _ := strconv.Atoi(strings.Split(a, ".")[0])
		to, _ := strconv.Atoi(strings.Split(b, ".")[0])
		c.blocks = append(c.blocks, covBlock{filepath.Base(file), from, to, test})
	}
}

// run is the -run pattern of the tests that execute any of m's func; "" (the
// whole suite) when none does, e.g. a package-level var's initializer. The
// whole func, not m's line: a `case a && b:` line is only in its body's
// block, so tests that skip that case (the ones a mutant there fails) would
// be left out.
func (c *coverage) run(m mutant) string {
	lo, hi := m.line, m.line
	if m.fnTo > 0 {
		lo, hi = m.fnFrom, m.fnTo
	}
	seen := map[string]bool{}
	var names []string
	for _, b := range c.blocks {
		if b.file == filepath.Base(m.file) && b.from <= hi && lo <= b.to && !seen[b.test] {
			seen[b.test] = true
			names = append(names, regexp.QuoteMeta(b.test))
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return "^(" + strings.Join(names, "|") + ")$"
}

// try tests the package with mutant m in place of its file, running only the
// tests run matches ("" all of them).
func try(pkg string, m mutant, dir string, limit time.Duration, run string) result {
	os.MkdirAll(dir, 0o755)
	src, err := os.ReadFile(m.file)
	if err != nil {
		return invalid
	}
	src = append(append(append([]byte{}, src[:m.off]...), m.to...), src[m.off+m.n:]...)
	mf, ov := filepath.Join(dir, filepath.Base(m.file)), filepath.Join(dir, "overlay.json")
	b, _ := json.Marshal(map[string]any{"Replace": map[string]string{m.file: mf}})
	if os.WriteFile(mf, src, 0o644) != nil || os.WriteFile(ov, b, 0o644) != nil {
		return invalid
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	out, err := gotest(ctx, pkg, ov, run)
	switch {
	case ctx.Err() != nil:
		return timedOut
	case err == nil:
		return survived
	case bytes.Contains(out, []byte("no space left on device")):
		fmt.Printf("disk full, stopping:\n%s", out)
		exit(1)
		return invalid
	case bytes.Contains(out, []byte("[build failed]")) || bytes.Contains(out, []byte("[setup failed]")):
		return invalid
	}
	return killed
}

// mutantWait is internal/testwait's limit under a mutant: a test it makes
// wait in vain is killed (or timed out, which counts the same) sooner.
const mutantWait = "20s"

func gotest(ctx context.Context, pkg, overlay, run string) ([]byte, error) {
	args := append([]string{"test", "-count=1", "-failfast"}, tags...)
	cmd := exec.CommandContext(ctx, "go")
	if overlay != "" {
		args = append(args, "-overlay", overlay)
		// a mutant's builds go to its worker's own cache, not the user's;
		// a wait it breaks fails in seconds (internal/testwait), not minutes
		cmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(filepath.Dir(overlay), "cache"), "DESK_TEST_WAIT="+mutantWait)
	}
	if run != "" {
		args = append(args, "-run", run)
	}
	cmd.Args = append(append(cmd.Args, args...), pkg)
	return cmd.CombinedOutput()
}

// report says the score (killed of those that built) and each survivor.
func report(pkg string, ms []mutant, res []result) {
	var n [4]int
	var lost []string
	for i, r := range res {
		n[r]++
		if r == survived {
			m := ms[i]
			rel, _ := filepath.Rel(mustWd(), m.file)
			to := m.to
			if to == "" {
				to = "(nothing)"
			}
			lost = append(lost, fmt.Sprintf("  %s:%d: %s → %s", rel, m.line, m.from, to))
		}
	}
	sort.Strings(lost)
	score := 100.0
	if t := n[killed] + n[survived] + n[timedOut]; t > 0 {
		score = 100 * float64(n[killed]+n[timedOut]) / float64(t)
	}
	fmt.Printf("%s: %.1f%% killed (%d killed, %d timed out, %d survived, %d didn't build)\n", pkg, score, n[killed], n[timedOut], n[survived], n[invalid])
	if len(lost) > 0 {
		fmt.Println("survived (no test noticed):\n" + strings.Join(lost, "\n"))
	}
}

func mustWd() string { wd, _ := os.Getwd(); return wd }
