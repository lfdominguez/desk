package ui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"gioui.org/layout"

	"github.com/lfdominguez/desk/internal/gitw"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
)

// Review & ship: with an organization's [git] worktrees, a new card's
// session runs in its own worktree and branch (Card.Worktree, .Branch,
// .Base); the card's Review view (rv:<id>) shows what the branch changed
// against its base and ships it: a fast-forward merge in the main
// checkout, a push, a pull or merge request.

// rvMaxFiles is how many changed files the Review shows.
const rvMaxFiles = 200

// rvState is one card's Review: what git said last, and what runs now.
type rvState struct {
	loading bool
	gen     int // each load's: the files' diffs are cached under it
	st      gitw.Status
	files   []gitw.File
	root    string // the worktree's top, the files' paths from it
	err     string
	busy    string // "Merging…" while an action runs
	armed   string // the action waiting for its second click
	list    layout.List
}

// cardDir is where card c's Claude works: its worktree's folder, else its
// folder in the organization.
func (u *ui) cardDir(c *kanban.Card) string {
	if c.Worktree != "" {
		return c.Worktree
	}
	return orgs.Dir(u.root, c.Org, c.Dir)
}

// wantsWorktree says whether card c's new session (exists false) first
// gets a worktree: its organization asks for them and it has none yet.
func (u *ui) wantsWorktree(c *kanban.Card, exists bool) bool {
	return !exists && c.Worktree == "" && !u.wtTried[c.ID] && c.Pilot == nil && u.orgConfig(c.Org).Git.Worktrees
}

// makeWorktree gives card id its worktree off the UI goroutine (git may
// fetch), then runs then: the message it held. A folder outside any git
// repository, or git failing, leaves the card in its folder.
func (u *ui) makeWorktree(id string, then func()) {
	c := u.b.Card(id)
	u.wtTried[id] = true
	c.Status, c.Note = kanban.StatusWorking, "Making its worktree…"
	u.save()
	dir, branch := orgs.Dir(u.root, c.Org, c.Dir), strings.ToLower(c.Key())
	go func() {
		var wt, base string
		root, err := gitw.Root(dir)
		if err == nil {
			base = gitw.DefaultBranch(root)
			if wt, err = gitw.AddWorktree(root, branch, base); err == nil {
				rel, _ := filepath.Rel(root, dir)
				wt = filepath.Join(wt, rel)
			}
		} else {
			err = nil // not a repository: nothing to isolate
		}
		u.later(func() {
			c := u.b.Card(id)
			if c == nil {
				return
			}
			c.Note = ""
			if err != nil {
				u.showToast(c.Key()+": no worktree, working in its folder: "+firstLine(err.Error()), true)
			} else if wt != "" {
				c.Worktree, c.Branch, c.Base = wt, branch, base
				u.showToast(c.Key()+" works on its own branch "+branch, false)
			}
			u.save()
			then()
		})
	}()
}

// openReview shows card id's Review and reads its branch.
func (u *ui) openReview(id string) {
	u.view, u.panel, u.sel = "rv:"+id, "", id
	u.rvLoad(id)
}

// rvOf is card id's Review state, made on first use.
func (u *ui) rvOf(id string) *rvState {
	st := u.rv[id]
	if st == nil {
		st = &rvState{}
		st.list.Axis = layout.Vertical
		u.rv[id] = st
	}
	return st
}

// rvLoad reads card id's branch against its base, off the UI goroutine.
func (u *ui) rvLoad(id string) {
	c := u.b.Card(id)
	st := u.rvOf(id)
	if c == nil || c.Worktree == "" || st.loading {
		return
	}
	st.loading, st.err = true, ""
	u.rvGen++
	wt, base := c.Worktree, c.Base
	go func() {
		root, err := gitw.Root(wt)
		var s gitw.Status
		var fs []gitw.File
		if err == nil {
			s, err = gitw.State(wt, base)
		}
		if err == nil {
			fs, err = gitw.Files(wt, base, rvMaxFiles)
		}
		u.later(func() {
			st.loading = false
			st.gen++
			st.st, st.files, st.root = s, fs, root
			if err != nil {
				st.err = err.Error()
			}
			u.rvGen++
		})
	}()
}

// rvAct runs a Review's action for card id: "merge" (fast-forward its base
// in the main checkout; the card goes to Done), "push" (its branch) or "pr"
// (push, then a pull or merge request linked to the card). Merge asks a
// second click first.
func (u *ui) rvAct(id, what string) {
	c := u.b.Card(id)
	st := u.rvOf(id)
	if c == nil || c.Worktree == "" || st.busy != "" {
		return
	}
	if what == "merge" && st.armed != "merge" {
		st.armed = "merge"
		u.rvGen++
		return
	}
	st.armed = ""
	st.busy = map[string]string{"merge": "Merging…", "push": "Pushing…", "pr": "Opening the request…"}[what]
	u.rvGen++
	wt, branch, base, key := c.Worktree, c.Branch, c.Base, c.Key()
	title, body, cfg := c.Title, strings.TrimSpace(c.Notes+"\n\n"+key), u.orgConfig(c.Org)
	go func() {
		var err error
		var l kanban.Link
		switch what {
		case "merge":
			err = gitw.Merge(wt, branch, base)
		case "push":
			err = gitw.Push(wt, branch)
		case "pr":
			l, err = rvOpenPR(cfg, wt, branch, base, title, body)
		}
		u.later(func() {
			st.busy = ""
			u.rvGen++
			c := u.b.Card(id)
			if c == nil {
				return
			}
			if err != nil {
				u.showToast(key+": "+firstLine(err.Error()), true)
				u.rvLoad(id)
				return
			}
			switch what {
			case "merge":
				u.showToast(key+": "+branch+" merged into "+base, false)
				c.Status, c.Note = kanban.StatusDone, ""
				u.moveTo(id, "Done")
			case "push":
				u.showToast(key+": pushed "+branch, false)
			case "pr":
				u.addLinks(c, []string{l.URL})
				go u.checkLinks([]string{id})
				u.showToast(key+": opened "+l.Ref, false)
			}
			u.save()
			u.rvLoad(id)
		})
	}()
}

// rvOpenPR pushes branch and opens its request into base, on origin's
// repository through the organization's account.
var rvOpenPR = func(cfg orgs.Config, wt, branch, base, title, body string) (kanban.Link, error) {
	remote, err := gitw.Origin(wt)
	if err != nil {
		return kanban.Link{}, err
	}
	if err := gitw.Push(wt, branch); err != nil {
		return kanban.Link{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return links.OpenPR(ctx, cfg, remote, branch, base, title, body)
}

// dropWorktree removes archived card c's worktree, off the UI goroutine;
// one with changes not committed stays, and says so.
func (u *ui) dropWorktree(c kanban.Card) {
	if c.Worktree == "" {
		return
	}
	go func() {
		root, err := gitw.Root(c.Worktree)
		if err == nil {
			err = gitw.RemoveWorktree(root, c.Branch)
		}
		u.later(func() {
			switch {
			case errors.Is(err, gitw.ErrDirty):
				u.showToast(c.Key()+": kept its worktree "+c.Worktree+": "+err.Error(), true)
			case err != nil:
				u.showToast(c.Key()+": worktree not removed: "+firstLine(err.Error()), true)
			default:
				if a := u.b.Card(c.ID); a != nil {
					a.Worktree = ""
					u.save()
				}
			}
		})
	}()
}
