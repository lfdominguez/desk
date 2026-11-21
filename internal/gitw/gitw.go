// Package gitw is what desk does with a card's git repository: its own
// worktree and branch, what the branch changed against its base, and
// shipping it (a fast-forward merge, a push). It runs the user's git: the
// repository, its remotes and their credentials are theirs.
package gitw

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Timeout bounds a git command; a fetch or push over a slow network gets
// netTimeout.
var Timeout, netTimeout = 30 * time.Second, 2 * time.Minute

// run is git args in dir, its output trimmed; a failure says git's own words.
func run(dir string, args ...string) (string, error) { return runFor(Timeout, dir, args...) }

func runFor(d time.Duration, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// Root is the top of the repository dir is in (a worktree's own top).
func Root(dir string) (string, error) { return run(dir, "rev-parse", "--show-toplevel") }

// Main is the main checkout of the repository dir is in, a worktree's
// too: where its base branch is checked out.
func Main(dir string) (string, error) {
	common, err := run(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.Dir(common), nil
}

// DefaultBranch is the repository's default branch: origin's HEAD, else
// main or master, else the branch checked out.
func DefaultBranch(root string) string {
	if ref, err := run(root, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimPrefix(ref, "origin/")
	}
	for _, b := range []string{"main", "master"} {
		if _, err := run(root, "rev-parse", "--verify", "--quiet", "refs/heads/"+b); err == nil {
			return b
		}
	}
	b, _ := run(root, "branch", "--show-current")
	return b
}

// AddWorktree makes branch's worktree at <root>/.worktrees/<branch>, off
// origin's base after a fetch (base itself when there's no origin or the
// fetch fails), and keeps .worktrees out of git status through the
// repository's info/exclude. It returns the worktree's path.
func AddWorktree(root, branch, base string) (string, error) {
	path := filepath.Join(root, ".worktrees", branch)
	if _, err := os.Stat(path); err == nil {
		return path, nil // made before: a card's session started again
	}
	from := base
	if _, err := runFor(netTimeout, root, "fetch", "origin", base); err == nil {
		from = "origin/" + base
	}
	if _, err := run(root, "worktree", "add", "-b", branch, path, from); err != nil {
		if _, err2 := run(root, "worktree", "add", path, branch); err2 != nil { // the branch is there already
			return "", err
		}
	}
	exclude(root)
	return path, nil
}

// exclude adds .worktrees/ to root's info/exclude once.
func exclude(root string) {
	p, err := run(root, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err != nil {
		return
	}
	b, _ := os.ReadFile(p)
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == ".worktrees/" {
			return
		}
	}
	os.MkdirAll(filepath.Dir(p), 0o755)
	if len(b) > 0 && !bytes.HasSuffix(b, []byte("\n")) {
		b = append(b, '\n')
	}
	os.WriteFile(p, append(b, ".worktrees/\n"...), 0o644)
}

// RemoveWorktree removes the worktree at path and deletes its branch when
// it's merged; a worktree with uncommitted changes stays (ErrDirty).
func RemoveWorktree(path, branch string) error {
	main, err := Main(path)
	if err != nil {
		return err
	}
	if dirty, _ := run(path, "status", "--porcelain"); dirty != "" {
		return ErrDirty
	}
	if _, err := run(main, "worktree", "remove", path); err != nil {
		return err
	}
	run(main, "branch", "-d", branch) // kept when not merged: its commits are the user's
	return nil
}

// ErrDirty: the worktree has changes not committed.
var ErrDirty = errors.New("it has changes not committed")

// Commit is one commit of a branch.
type Commit struct{ Hash, Subject string }

// Status is a card's branch against its base.
type Status struct {
	Branch, Base  string
	Ahead, Behind int      // commits the branch has that base hasn't, and the other way
	Commits       []Commit // the branch's own, newest first
	Dirty         []string // git status --porcelain lines: changes not committed
	Merged        bool     // base has every commit of the branch (and it has some, or base moved past it)
}

// State is the branch checked out at dir against base (a local branch).
func State(dir, base string) (Status, error) {
	s := Status{Base: base}
	var err error
	if s.Branch, err = run(dir, "branch", "--show-current"); err != nil {
		return s, err
	}
	counts, err := run(dir, "rev-list", "--left-right", "--count", base+"...HEAD")
	if err != nil {
		return s, err
	}
	if f := strings.Fields(counts); len(f) == 2 {
		s.Behind, _ = strconv.Atoi(f[0])
		s.Ahead, _ = strconv.Atoi(f[1])
	}
	log, _ := run(dir, "log", "--format=%h %s", base+"..HEAD")
	for _, l := range lines(log) {
		h, subj, _ := strings.Cut(l, " ")
		s.Commits = append(s.Commits, Commit{h, subj})
	}
	st, _ := run(dir, "status", "--porcelain")
	s.Dirty = lines(st)
	s.Merged = s.Ahead == 0 && s.Branch != base
	return s, nil
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// File is one file the branch changed: its text at the merge base and now
// (committed and not), "" where it didn't exist; Binary when it isn't text.
type File struct {
	Path     string
	Old, New string
	Binary   bool
}

// Files are what the branch at dir changed since it left base, the
// changes not committed included, at most max files.
func Files(dir, base string, max int) ([]File, error) {
	mb, err := run(dir, "merge-base", base, "HEAD")
	if err != nil {
		return nil, err
	}
	names, _ := run(dir, "diff", "--name-only", "-z", mb) // mb is a commit: this works
	untracked, _ := run(dir, "ls-files", "--others", "--exclude-standard", "-z")
	var fs []File
	for _, p := range append(strings.Split(names, "\x00"), strings.Split(untracked, "\x00")...) {
		if p == "" || len(fs) >= max {
			continue
		}
		old, _ := run(dir, "show", mb+":"+p)
		now, _ := os.ReadFile(filepath.Join(dir, p))
		f := File{Path: p, Old: old, New: strings.TrimRight(string(now), "\n")}
		f.Binary = bytes.IndexByte(now, 0) >= 0 || strings.IndexByte(old, 0) >= 0
		if f.Binary {
			f.Old, f.New = "", ""
		}
		fs = append(fs, f)
	}
	return fs, nil
}

// Merge fast-forwards base to branch in the repository's main checkout,
// which must have base checked out and nothing uncommitted: never a merge
// commit, so a branch behind base is to be rebased first.
func Merge(dir, branch, base string) error {
	main, err := Main(dir)
	if err != nil {
		return err
	}
	if cur, _ := run(main, "branch", "--show-current"); cur != base {
		return fmt.Errorf("the main checkout %s is on %q, not %s", main, cur, base)
	}
	if dirty, _ := run(main, "status", "--porcelain", "--untracked-files=no"); dirty != "" {
		return fmt.Errorf("the main checkout %s has changes not committed", main)
	}
	if _, err := run(main, "merge", "--ff-only", branch); err != nil {
		return fmt.Errorf("%s can't fast-forward to %s (rebase it on %s first): %w", base, branch, base, err)
	}
	return nil
}

// Push pushes branch from dir's repository to origin, setting it upstream.
func Push(dir, branch string) error {
	_, err := runFor(netTimeout, dir, "push", "-u", "origin", branch)
	return err
}

// Origin is the repository's origin URL.
func Origin(dir string) (string, error) { return run(dir, "remote", "get-url", "origin") }
