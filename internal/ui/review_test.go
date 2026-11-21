package ui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gioui.org/io/key"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
)

// gitRepo makes org's folder a git repository with one commit on main,
// pushed to a bare origin beside it; git kept off the user's config.
func gitRepo(t *testing.T, h *harness, org string) (dir, origin string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "me", "GIT_AUTHOR_EMAIL": "me@globex.com", "GIT_COMMITTER_NAME": "me", "GIT_COMMITTER_EMAIL": "me@globex.com"} {
		t.Setenv(k, v)
	}
	dir, origin = orgs.Dir(h.u.root, org, ""), filepath.Join(t.TempDir(), "origin.git")
	git(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644)
	git(t, dir, "add", "a.txt")
	git(t, dir, "commit", "-q", "-m", "first")
	git(t, dir, "init", "-q", "--bare", "-b", "main", origin)
	git(t, dir, "remote", "add", "origin", origin)
	git(t, dir, "push", "-q", "-u", "origin", "main")
	return dir, origin
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// worked starts card id's session with a message and waits for its Claude.
func worked(h *harness, id string) {
	h.u.send(id, "go")
	h.pumpUntil("its Claude", func() bool { return h.u.procs[id] != nil })
}

// TestWorktreeFirst: with [git] worktrees, a new card's first message
// waits for its own worktree and branch, and its Claude runs there; an
// organization without it, or a card whose folder isn't in a repository,
// works in its folder.
func TestWorktreeFirst(t *testing.T) {
	h := newHarness(t)
	dir, _ := gitRepo(t, h, "Acme")
	h.orgToml("Acme", "[git]\nworktrees = true\n")
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme", Dir: "api"})
	worked(h, c.ID)
	got, key := h.u.b.Card(c.ID), strings.ToLower(c.Key())
	want := filepath.Join(dir, ".worktrees", key, "api")
	if got.Worktree != want || got.Branch != key || got.Base != "main" || h.claudeOf(c.ID).opts.Dir != want || h.u.cardDir(got) != want {
		t.Fatalf("%+v dir %s", got, h.claudeOf(c.ID).opts.Dir)
	}
	if b := git(t, dir, "branch", "--list", key); !strings.Contains(b, key) {
		t.Fatalf("branch %q", b)
	}

	h.orgToml("Globex", "[git]\nworktrees = true\n") // a folder outside any repository
	t.Setenv("GIT_CEILING_DIRECTORIES", h.u.root)
	g := h.u.b.Add(0, kanban.Card{Title: "y", Org: "Globex"})
	worked(h, g.ID)
	if got := h.u.b.Card(g.ID); got.Worktree != "" || h.claudeOf(g.ID).opts.Dir != orgs.Dir(h.u.root, "Globex", "") {
		t.Fatalf("no repo %+v", got)
	}

	h.orgToml("Acme", "")
	p := h.u.b.Add(0, kanban.Card{Title: "z", Org: "Acme"})
	worked(h, p.ID)
	if got := h.u.b.Card(p.ID); got.Worktree != "" {
		t.Fatalf("off %+v", got)
	}
}

// TestWorktreeFails: git refusing (a branch name taken by a file) leaves
// the card in its folder, said in a toast.
func TestWorktreeFails(t *testing.T) {
	h := newHarness(t)
	dir, _ := gitRepo(t, h, "Acme")
	h.orgToml("Acme", "[git]\nworktrees = true\n")
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	os.MkdirAll(filepath.Join(dir, ".worktrees"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "refs", "heads", strings.ToLower(c.Key())), []byte("not a sha\n"), 0o644)
	worked(h, c.ID)
	if got := h.u.b.Card(c.ID); got.Worktree != "" || !strings.Contains(h.u.toast, "no worktree") {
		t.Fatalf("%+v toast %q", got, h.u.toast)
	}
}

// TestReview: the Review shows the branch's commits and changes; Push
// pushes it, Open request links what rvOpenPR opened, Merge asks a second
// click and then fast-forwards main and takes the card to Done; archiving
// removes the worktree.
func TestReview(t *testing.T) {
	h := newHarness(t)
	dir, origin := gitRepo(t, h, "Acme")
	h.orgToml("Acme", "[git]\nworktrees = true\n")
	c := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Fix login", Org: "Acme"})
	worked(h, c.ID)
	wt := h.u.b.Card(c.ID).Worktree
	os.WriteFile(filepath.Join(wt, "a.txt"), []byte("one\ntwo\n"), 0o644)
	git(t, wt, "commit", "-q", "-am", "Add two")
	os.WriteFile(filepath.Join(wt, "bin"), []byte("a\x00b"), 0o644)

	h.u.view = "sess:" + c.ID
	h.clickTop("pnReview")
	if h.u.view != "rv:"+c.ID {
		t.Fatalf("view %q", h.u.view)
	}
	h.pumpUntil("the branch", func() bool { return !h.u.rvOf(c.ID).loading })
	h.frame()
	for _, s := range []string{"Add two", "1 ahead · 0 behind", "bin: binary, not shown", "a.txt"} {
		if !h.has(s) {
			t.Fatalf("no %q", s)
		}
	}
	if !strings.HasSuffix(h.u.viewTitle(), " · review") || !strings.Contains(h.u.sentence(), "merge, push") {
		t.Fatalf("toolbar %q %q", h.u.viewTitle(), h.u.sentence())
	}
	os.Remove(filepath.Join(wt, "bin"))

	h.clickTop("rvPush" + c.ID)
	h.pumpUntil("the push", func() bool { return h.u.rvOf(c.ID).busy == "" && strings.Contains(h.u.toast, "pushed") })
	if b := git(t, origin, "branch", "--list", strings.ToLower(c.Key())); b == "" {
		t.Fatal("not pushed")
	}

	defer func(f func(orgs.Config, string, string, string, string, string) (kanban.Link, error)) { rvOpenPR = f }(rvOpenPR)
	var asked string
	rvOpenPR = func(_ orgs.Config, _, branch, base, title, _ string) (kanban.Link, error) {
		asked = branch + ">" + base + ":" + title
		return kanban.Link{URL: "https://github.com/acme/web/pull/7"}, nil
	}
	h.clickTop("rvPR" + c.ID)
	h.pumpUntil("the request", func() bool { return len(h.u.b.Card(c.ID).Links) == 1 })
	if asked != strings.ToLower(c.Key())+">main:Fix login" {
		t.Fatalf("asked %q", asked)
	}
	rvOpenPR = func(orgs.Config, string, string, string, string, string) (kanban.Link, error) {
		return kanban.Link{}, errors.New("422 a request already exists")
	}
	h.clickTop("rvPR" + c.ID)
	h.pumpUntil("the refusal", func() bool { return strings.Contains(h.u.toast, "422") })

	h.pumpUntil("idle", func() bool { st := h.u.rvOf(c.ID); return st.busy == "" && !st.loading })
	h.clickTop("rvMerge" + c.ID)
	if h.u.rvOf(c.ID).armed != "merge" || !h.has("Confirm: fast-forward main") {
		t.Fatal("merged on one click")
	}
	h.clickTop("rvMerge" + c.ID)
	h.pumpUntil("the merge", func() bool { return h.u.b.Card(c.ID).Status == kanban.StatusDone })
	if col, _ := h.u.b.Find(c.ID); h.u.b.Columns[col].Title != "Done" || git(t, dir, "log", "-1", "--format=%s") != "Add two" {
		t.Fatal("not merged into main")
	}
	h.pumpUntil("reread", func() bool { return !h.u.rvOf(c.ID).loading && h.u.rvOf(c.ID).st.Merged })
	h.frame()
	if !h.has("main has every commit of " + strings.ToLower(c.Key()) + ".") {
		t.Fatal("not said merged")
	}

	h.send(press(key.NameEscape, 0))
	if h.u.view != "sess:"+c.ID {
		t.Fatalf("esc %q", h.u.view)
	}
	h.u.archiveCard(c.ID)
	h.pumpUntil("removed", func() bool { _, err := os.Stat(wt); return os.IsNotExist(err) })
	h.pumpUntil("cleared", func() bool { return h.u.b.Card(c.ID).Worktree == "" })
}

// TestReviewRefusals: a merge git refuses (main has moved), a worktree
// with changes kept on archive, and a card without a branch.
func TestReviewRefusals(t *testing.T) {
	h := newHarness(t)
	dir, _ := gitRepo(t, h, "Acme")
	h.orgToml("Acme", "[git]\nworktrees = true\n")
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	worked(h, c.ID)
	wt := h.u.b.Card(c.ID).Worktree
	os.WriteFile(filepath.Join(wt, "b.txt"), []byte("b\n"), 0o644)
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-q", "-m", "b")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("moved\n"), 0o644)
	git(t, dir, "commit", "-q", "-am", "main moved")

	h.u.openReview(c.ID)
	h.pumpUntil("the branch", func() bool { return !h.u.rvOf(c.ID).loading })
	h.u.rvAct(c.ID, "merge")
	h.u.rvAct(c.ID, "merge")
	h.u.rvAct(c.ID, "push") // one at a time: ignored while the merge runs
	h.pumpUntil("the refusal", func() bool { return strings.Contains(h.u.toast, "rebase it on main") })
	if col, _ := h.u.b.Find(c.ID); h.u.b.Columns[col].Title == "Done" {
		t.Fatal("a refused merge went to Done")
	}

	os.WriteFile(filepath.Join(wt, "c.txt"), []byte("c\n"), 0o644)
	h.pumpUntil("idle", func() bool { st := h.u.rvOf(c.ID); return st.busy == "" && !st.loading })
	h.u.rvLoad(c.ID)
	h.pumpUntil("reread", func() bool { return !h.u.rvOf(c.ID).loading })
	h.frame()
	if !h.has("1 changes not committed: below, but left out of a merge or push until committed.") {
		t.Fatal("changes not said")
	}
	h.u.archiveCard(c.ID)
	h.pumpUntil("kept", func() bool { return strings.Contains(h.u.toast, "kept its worktree") })

	h.u.b.Card(c.ID).Worktree = filepath.Join(t.TempDir(), "gone")
	h.u.dropWorktree(*h.u.b.Card(c.ID))
	h.pumpUntil("not removed", func() bool { return strings.Contains(h.u.toast, "worktree not removed") })

	n := h.u.b.Add(0, kanban.Card{Title: "plain", Org: "Acme"})
	h.u.view = "rv:" + n.ID
	h.frame()
	if !h.has("This card has no branch of its own to review.") {
		t.Fatal("no branch not said")
	}
	h.u.rvLoad(n.ID)
	h.u.rvAct(n.ID, "push")
	h.u.dropWorktree(n)
	if h.u.rvOf(n.ID).loading || h.u.rvOf(n.ID).busy != "" {
		t.Fatal("a card without a branch ran git")
	}
}

// TestReviewEmpty: a fresh branch has nothing to show, and its state
// error (its base gone) says git's words.
func TestReviewEmpty(t *testing.T) {
	h := newHarness(t)
	gitRepo(t, h, "Acme")
	h.orgToml("Acme", "[git]\nworktrees = true\n")
	c := h.u.b.Add(0, kanban.Card{Title: "x", Org: "Acme"})
	worked(h, c.ID)
	h.u.openReview(c.ID)
	h.pumpUntil("the branch", func() bool { return !h.u.rvOf(c.ID).loading })
	h.frame()
	if !h.has("Nothing changed against main yet.") {
		t.Fatal("empty not said")
	}
	h.u.b.Card(c.ID).Base = "nope"
	h.u.rvLoad(c.ID)
	h.pumpUntil("error", func() bool { return h.u.rvOf(c.ID).err != "" })
	h.frame()
	if !h.has(h.u.rvOf(c.ID).err) {
		t.Fatal("error not shown")
	}
	h.u.view = "rv:" + c.ID
	h.u.rvAct(c.ID, "merge") // arms only
	h.u.b.Delete(c.ID)
	h.u.rvAct(c.ID, "merge")
	h.frame()
}

// has says whether a label drawn last frame contains s.
func (h *harness) has(s string) bool { _, ok := h.shown(s); return ok }
