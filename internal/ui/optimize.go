package ui

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/vault"
)

// optimize is Prompt Optimization: Haiku argues a request out into a card;
// tests replace it.
var optimize = claude.Optimize

// describe reads what a linked ticket says, for the optimizer; tests replace it.
var describe = links.Describe

// optAsk is what the optimizer gets for request req in org's folder dir
// ("" or not a folder: it picks one of the org's), written in lang.
func (u *ui) optAsk(org, dir, req, lang string) claude.Ask {
	var folders, labels []string
	dir = strings.Trim(strings.TrimSpace(dir), "/")
	if fi, err := os.Stat(orgs.Dir(u.root, org, dir)); dir != "" && (err != nil || !fi.IsDir()) {
		dir = "" // not a folder (yet): Improve picks one
	}
	if dir == "" {
		ents, _ := os.ReadDir(orgs.Dir(u.root, org, ""))
		for _, e := range ents {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				folders = append(folders, e.Name())
			}
		}
	}
	for _, c := range u.boardCards() {
		for _, l := range c.Labels {
			if !slices.Contains(labels, l) {
				labels = append(labels, l)
			}
		}
	}
	return claude.Ask{Home: u.home, Account: orgs.Account(u.root, org), Org: org, Dir: orgs.Dir(u.root, org, dir), Root: orgs.Dir(u.root, org, ""), Folder: dir, Request: req,
		Folders: folders, Labels: labels, Today: time.Now().Format(kanban.DateLayout), Lang: lang, About: orgs.AboutMe(u.root, org), UserMCP: orgs.UserMCP()}
}

// optimizeCard runs Prompt Optimization on the new card's request (its
// notes, else its title) in the chosen organization, then fills the dialog
// with the draft for the user to look over before Create (which a new card
// can't skip: commit runs this first). Again while it runs stops it.
func (u *ui) optimizeCard() {
	s := u.edit
	if s.optStop != nil {
		s.optStop()
		return
	}
	if !s.optimized { // what the user wrote, for Use mine and Discard suggestions; its @org and the rest fill the fields first
		s.mineTitle, s.mineNotes = s.title.Text(), s.notes.Text()
		u.smartApply(s)
		s.title.SetText(s.mineTitle) // the draft writes the title
	}
	org := strings.TrimSpace(s.org.Text())
	req := strings.TrimSpace(s.notes.Text())
	if req == "" {
		req = strings.TrimSpace(s.title.Text())
	}
	switch {
	case !slices.Contains(u.orgList, org):
		u.showToast("Pick the organization first: optimization reads its Claude context", true)
		return
	case req == "":
		u.showToast("Write what to do first", true)
		return
	}
	ask := u.optAsk(org, s.dir.Text(), req, strings.TrimSpace(s.lang.Text()))
	cfg := u.orgConfig(org)
	var ls []kanban.Link
	for _, w := range strings.FieldsFunc(req+"\n"+s.links.Text(), func(r rune) bool { return strings.ContainsRune(" \t\n,;()<>\"'", r) }) {
		w = strings.TrimRight(w, ".:!?")
		if l, ok := links.Parse(w, cfg); ok && l.Kind != links.KindURL && !slices.ContainsFunc(ls, func(x kanban.Link) bool { return x.URL == l.URL }) {
			ls = append(ls, l)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	s.optStop, s.optNote, s.optErr = cancel, "Optimizing with Haiku: reading "+org+"…", ""
	s.optStep, s.optStage, s.optLive, s.optStart, s.optTickets = "Starting Haiku in "+org, 1, claude.Draft{}, time.Now(), len(ls) > 0
	progress := func(f func()) { // from the run's goroutine; a later run or a closed dialog drops it
		u.later(func() {
			if u.edit == s && s.optStop != nil {
				f()
			}
		})
	}
	go func() {
		for _, l := range ls { // what the tickets say, through the org's accounts: the optimizer can't reach them
			progress(func() { s.optStep, s.optStage = "Reading "+l.Ref, 0 })
			text, err := describe(ctx, l, cfg)
			var locked *vault.LockedError
			switch {
			case errors.As(err, &locked):
				u.later(func() { u.vaultLocked(locked) })
				text = l.Ref + ": not read (its vault is locked)"
			case err != nil:
				text = l.Ref + ": not read (" + firstLine(err.Error()) + ")"
			}
			if text != "" {
				ask.Linked = append(ask.Linked, text)
			}
		}
		progress(func() { s.optStep, s.optStage = "Starting Haiku in "+org, 1 })
		var d claude.Draft
		var err error
		if ask.Env, err = u.goEnv(org, "improve"); err == nil {
			d, err = optimize(ctx, ask, func(p claude.Progress) {
				progress(func() {
					s.optStep = p.Step
					if p.Step == "Writing" {
						s.optStage, s.optLive = 2, p.Draft
					}
				})
			})
		}
		stopped := ctx.Err() == context.Canceled // the user's Stop: read before our own cancel
		cancel()
		u.later(func() {
			if u.edit != s {
				return
			}
			s.optStop, s.optStep, s.optLive = nil, "", claude.Draft{}
			if err != nil {
				s.optNote, s.optErr = "⚠ Improve didn't finish: why is above", strings.TrimSpace(err.Error())
				if stopped {
					s.optNote, s.optErr = "Stopped", ""
				}
				return
			}
			s.optErr = ""
			u.applyDraft(s, org, d)
		})
	}()
}

// applyDraft fills s from d: title and notes replaced, labels and links
// added to, folder, priority and due only where still unset.
func (u *ui) applyDraft(s *sheet, org string, d claude.Draft) {
	if d.Title != "" {
		s.title.SetText(d.Title)
	} else if strings.TrimSpace(s.title.Text()) == "" { // the dialog asked only what to do: its first line, then
		s.title.SetText(firstLine(strings.TrimSpace(s.notes.Text())))
	}
	if d.Prompt != "" {
		s.notes.SetText(keepImages(s.notes.Text(), strings.TrimSpace(d.Prompt)))
	}
	ls := kanban.ParseLabels(s.labels.Text())
	for _, l := range d.Labels {
		if l = strings.ToLower(strings.TrimSpace(l)); l != "" && !slices.Contains(ls, l) {
			ls = append(ls, l)
		}
	}
	s.labels.SetText(strings.Join(ls, ", "))
	if p := slices.IndexFunc(prioNames, func(n string) bool { return strings.EqualFold(n, d.Priority) }); p > 0 && s.prio == 0 {
		s.prio = p
	}
	if f := strings.Trim(d.Folder, "/"); f != "" && strings.TrimSpace(s.dir.Text()) == "" {
		if fi, err := os.Stat(orgs.Dir(u.root, org, f)); err == nil && fi.IsDir() {
			s.dir.SetText(f)
		}
	}
	cfg := u.orgConfig(org)
	have := strings.TrimSpace(s.links.Text())
	for _, l := range d.Links {
		if _, ok := links.Parse(l, cfg); ok && !strings.Contains(have, l) {
			have = strings.TrimSpace(have + "\n" + l)
		}
	}
	s.links.SetText(have)
	if _, err := time.Parse(kanban.DateLayout, d.Due); err == nil && strings.TrimSpace(s.due.Text()) == "" {
		s.due.SetText(d.Due)
	}
	s.optimized, s.preview, s.accepted = true, true, false // the brief is Markdown: shown as it reads, Edit for the source
	s.opened = true                                        // the notes editor is gone: the keys go to the title, where enter creates
	s.optNote = "Claude's suggestions: look them over, then Create"
	if len(d.Tools) > 0 {
		var ns []string
		for _, t := range d.Tools {
			ns = append(ns, t.Name)
		}
		s.optNote = "Claude's suggestions, after reading " + strings.Join(ns, ", ") + ": look them over, then Create"
	}
}
