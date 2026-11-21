package gitw

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// repo makes an origin (bare) and a clone of it with one commit on main,
// git kept away from the user's own config; the clone's path.
func repo(t *testing.T) (clone, origin string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "me", "GIT_AUTHOR_EMAIL": "me@globex.com", "GIT_COMMITTER_NAME": "me", "GIT_COMMITTER_EMAIL": "me@globex.com"} {
		t.Setenv(k, v)
	}
	dir := t.TempDir()
	origin, clone = filepath.Join(dir, "origin.git"), filepath.Join(dir, "api")
	must(t, dir, "init", "-q", "--bare", "-b", "main", origin)
	must(t, dir, "clone", "-q", origin, clone)
	must(t, clone, "checkout", "-q", "-b", "main")
	write(t, clone, "a.txt", "one\n")
	must(t, clone, "add", ".")
	must(t, clone, "commit", "-q", "-m", "first")
	must(t, clone, "push", "-q", "-u", "origin", "main")
	must(t, clone, "remote", "set-head", "origin", "main")
	return clone, origin
}

func must(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := run(dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func write(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWorktreeFlow: a card's worktree off origin's main, its commits and
// changes against main, a fast-forward merge, a push, and its removal.
func TestWorktreeFlow(t *testing.T) {
	clone, origin := repo(t)
	if b := DefaultBranch(clone); b != "main" {
		t.Fatalf("default %q", b)
	}
	write(t, filepath.Join(clone, ".git", "info"), "exclude", "# mine") // no newline at its end
	wt, err := AddWorktree(clone, "desk-7", "main")
	if err != nil || wt != filepath.Join(clone, ".worktrees", "desk-7") {
		t.Fatalf("%s %v", wt, err)
	}
	if again, err := AddWorktree(clone, "desk-7", "main"); err != nil || again != wt {
		t.Fatalf("again %s %v", again, err)
	}
	if st := must(t, clone, "status", "--porcelain"); st != "" {
		t.Fatalf(".worktrees not excluded: %q", st)
	}
	exclude(clone) // once only
	ex, _ := os.ReadFile(filepath.Join(clone, ".git", "info", "exclude"))
	if strings.Count(string(ex), ".worktrees/") != 1 || !strings.HasPrefix(string(ex), "# mine\n") {
		t.Fatalf("exclude %q", ex)
	}
	if r, _ := Root(filepath.Join(wt)); r != wt {
		t.Fatalf("root %s", r)
	}
	if m, _ := Main(wt); m != clone {
		t.Fatalf("main %s", m)
	}

	write(t, wt, "a.txt", "one\ntwo\n")
	must(t, wt, "commit", "-q", "-am", "two")
	write(t, wt, "b.txt", "new\n")
	write(t, wt, "bin", "a\x00b")
	s, err := State(wt, "main")
	if err != nil || s.Branch != "desk-7" || s.Ahead != 1 || s.Behind != 0 || len(s.Commits) != 1 || s.Commits[0].Subject != "two" || len(s.Dirty) != 2 || s.Merged {
		t.Fatalf("%+v %v", s, err)
	}
	fs, err := Files(wt, "main", 10)
	if err != nil || len(fs) != 3 || fs[0].Path != "a.txt" || fs[0].Old != "one" || fs[0].New != "one\ntwo" || fs[1].Path != "b.txt" || fs[1].Old != "" || !fs[2].Binary {
		t.Fatalf("%+v %v", fs, err)
	}
	if fs, _ := Files(wt, "main", 1); len(fs) != 1 {
		t.Fatalf("max %+v", fs)
	}
	if _, err := State(wt, "nope"); err == nil {
		t.Fatal("state against no branch")
	}
	if err := RemoveWorktree(clone, "main"); err == nil {
		t.Fatal("the main checkout removed")
	}
	if err := RemoveWorktree(wt, "desk-7"); !errors.Is(err, ErrDirty) {
		t.Fatalf("dirty removed: %v", err)
	}
	os.Remove(filepath.Join(wt, "b.txt"))
	os.Remove(filepath.Join(wt, "bin"))

	if err := Push(wt, "desk-7"); err != nil {
		t.Fatal(err)
	}
	if out := must(t, origin, "branch", "--list", "desk-7"); !strings.Contains(out, "desk-7") {
		t.Fatalf("not pushed: %q", out)
	}
	if err := Merge(wt, "desk-7", "main"); err != nil {
		t.Fatal(err)
	}
	if s, _ := State(wt, "main"); !s.Merged || s.Ahead != 0 {
		t.Fatalf("after merge %+v", s)
	}
	if u, _ := Origin(clone); u != origin {
		t.Fatalf("origin %s", u)
	}
	if err := RemoveWorktree(wt, "desk-7"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatal("worktree still there")
	}
	if out := must(t, clone, "branch", "--list", "desk-7"); out != "" {
		t.Fatalf("merged branch kept: %q", out)
	}
}

// TestMergeRefused: a main checkout on another branch, or with changes,
// or a branch behind base, is never merged.
func TestMergeRefused(t *testing.T) {
	clone, _ := repo(t)
	wt, _ := AddWorktree(clone, "desk-8", "main")
	write(t, wt, "c.txt", "c\n")
	must(t, wt, "add", ".")
	must(t, wt, "commit", "-q", "-m", "c")

	write(t, clone, "a.txt", "changed\n")
	if err := Merge(wt, "desk-8", "main"); err == nil || !strings.Contains(err.Error(), "not committed") {
		t.Fatalf("dirty main: %v", err)
	}
	must(t, clone, "commit", "-q", "-am", "main moved")
	if err := Merge(wt, "desk-8", "main"); err == nil || !strings.Contains(err.Error(), "rebase it on main") {
		t.Fatalf("diverged: %v", err)
	}
	must(t, clone, "checkout", "-q", "-b", "other")
	if err := Merge(wt, "desk-8", "main"); err == nil || !strings.Contains(err.Error(), `on "other"`) {
		t.Fatalf("other branch: %v", err)
	}
}

// TestNoOrigin: without an origin the worktree starts from the local base,
// and the default branch comes from the branches there.
func TestNoOrigin(t *testing.T) {
	clone, _ := repo(t)
	must(t, clone, "remote", "remove", "origin")
	if b := DefaultBranch(clone); b != "main" {
		t.Fatalf("default %q", b)
	}
	must(t, clone, "branch", "-m", "main", "trunk")
	if b := DefaultBranch(clone); b != "trunk" {
		t.Fatalf("checked out %q", b)
	}
	must(t, clone, "branch", "taken")
	wt, err := AddWorktree(clone, "taken", "trunk") // the branch exists: checked out as it is
	if err != nil || filepath.Base(wt) != "taken" {
		t.Fatalf("%s %v", wt, err)
	}
	if _, err := AddWorktree(clone, "bad name..", "trunk"); err == nil {
		t.Fatal("a bad branch name made a worktree")
	}
	if _, err := Origin(clone); err == nil {
		t.Fatal("an origin without one")
	}
}

// TestNotARepo: outside a repository every step says git's error.
func TestNotARepo(t *testing.T) {
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
	dir := t.TempDir()
	if _, err := Root(dir); err == nil || !strings.HasPrefix(err.Error(), "git rev-parse: ") {
		t.Fatalf("root %v", err)
	}
	if _, err := Main(dir); err == nil {
		t.Fatal("main")
	}
	if _, err := State(dir, "main"); err == nil {
		t.Fatal("state")
	}
	if _, err := Files(dir, "main", 5); err == nil {
		t.Fatal("files")
	}
	if Merge(dir, "x", "main") == nil || RemoveWorktree(dir, "x") == nil {
		t.Fatal("merge/remove")
	}
	exclude(dir) // nothing to write into
	old := Timeout
	Timeout = time.Nanosecond
	defer func() { Timeout = old }()
	if _, err := run(dir, "version"); err == nil {
		t.Fatal("a command past its time")
	}
}
