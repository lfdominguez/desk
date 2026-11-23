package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
)

// Triggers: an organization's [[trigger]]s act by themselves on what the
// link poll brought in (every 3 minutes: the tickets, CI and alerts views'
// data) and on the time of day. What each has handled is kept in
// <Org>/.desk/triggers.json, so nothing is done twice, and a trigger's
// first look only notes what is already there: turning one on doesn't
// flood the board with the backlog.

// trigKeep is how many handled keys a trigger keeps.
const trigKeep = 500

// trigSeen is what one trigger has handled: tickets, CI runs, alerts by
// key, or the schedule's last day.
type trigSeen struct {
	Keys []string `json:"keys,omitempty"`
	Last string   `json:"last,omitempty"` // schedule: the day it last ran, 2006-01-02
}

// trigNow is the clock triggers read; tests set it.
var trigNow = time.Now

// trigFile is org's triggers.json.
func (u *ui) trigFile(org string) string { return filepath.Join(orgs.Dir(u.root, org, ""), orgs.DeskDir, "triggers.json") }

// runTriggers runs every organization's triggers on what desk knows now;
// on the UI goroutine, after each poll.
func (u *ui) runTriggers() {
	for _, org := range u.orgList {
		cfg, err := orgs.Load(u.root, org)
		if err != nil || len(cfg.Triggers) == 0 {
			continue
		}
		seen := map[string]*trigSeen{}
		b, _ := os.ReadFile(u.trigFile(org))
		json.Unmarshal(b, &seen)
		changed := false
		for _, t := range cfg.Triggers {
			if t.Off || t.Name == "" {
				continue
			}
			s, first := seen[t.Name], seen[t.Name] == nil
			if first {
				s = &trigSeen{}
				seen[t.Name] = s
			}
			changed = u.runTrigger(org, t, s, first) || changed
		}
		if changed {
			b, _ := json.MarshalIndent(seen, "", "  ")
			if err := os.WriteFile(u.trigFile(org), b, 0o600); err != nil {
				u.showToast("Triggers: "+err.Error(), true)
			}
		}
	}
}

// runTrigger runs t once over what it watches, noting what it handled in s
// (first: only noting); whether s changed.
func (u *ui) runTrigger(org string, t orgs.Trigger, s *trigSeen, first bool) bool {
	why := "(Made by the trigger " + t.Name + ".)"
	fire := func(key string, do func()) bool {
		if slices.Contains(s.Keys, key) {
			return false
		}
		s.Keys = append(s.Keys, key)
		if len(s.Keys) > trigKeep {
			s.Keys = s.Keys[len(s.Keys)-trigKeep:]
		}
		if !first {
			do()
		}
		return true
	}
	changed := first // a first look is kept, even of nothing
	switch t.On {
	case "ticket":
		for _, tk := range u.tickets[org] {
			if t.Project != "" && !strings.EqualFold(tk.Project, t.Project) || !strings.Contains(strings.ToLower(tk.Title), strings.ToLower(t.Match)) || u.ticketCard(tk) != nil {
				continue
			}
			changed = fire(tk.Key, func() {
				l, ok := links.Parse(tk.URL, u.orgConfig(org))
				var ls []kanban.Link
				if ok {
					ls = []kanban.Link{l}
				}
				u.trigCard(org, t, tk.Key+" "+tk.Title, tk.URL+"\n\n"+why, ls)
			}) || changed
		}
	case "ci_failed":
		for _, it := range u.ci[org] {
			c := u.b.Card(it.id)
			if c == nil || u.isDone(it.id) {
				continue
			}
			var failed []string // this round's new failures of the link: one message for them all
			for _, r := range it.runs {
				if r.Status == links.CIFailed {
					changed = fire(fmt.Sprint(it.link.URL, "#", r.ID), func() { failed = append(failed, r.Name+" ("+r.URL+")") }) || changed
				}
			}
			if len(failed) > 0 {
				u.ciTell(c, fmt.Sprintf("(desk) CI failed on %s: %s. Read it with the ci and ci_log tools, find why and fix it. %s",
					it.link.Ref, strings.Join(failed, ", "), why))
			}
		}
	case "alert":
		for _, a := range u.alerts[org] {
			if u.alertQuiet(org, a) || !a.Critical() && !t.Warnings || trigNow().Sub(a.Since) < time.Duration(t.After)*time.Minute ||
				!strings.Contains(strings.ToLower(a.Host+" "+a.Service), strings.ToLower(t.Match)) {
				continue
			}
			changed = fire(alertKey(org, a)+"|"+a.Since.Format(time.RFC3339), func() {
				u.investigation(org, a.Host, a.Service, a.State, a.Output, a.URL, a.Since, why)
			}) || changed
		}
	case "schedule", "digest":
		if !trigDue(t, s) {
			break
		}
		switch {
		case first:
		case t.On == "schedule":
			u.trigCard(org, t, t.Title, strings.TrimSpace(t.Notes+"\n\n"+why), nil)
		case u.phone != nil && u.phone.chat != 0:
			u.phoneSendIn(u.gtThread("digest"), u.digestText(org, digestSince(1, trigNow())), nil, "")
		default:
			u.showToast(t.Name+": a digest goes to the phone, and none is paired ([phone] in settings.toml)", true)
		}
		return true
	default:
		u.showToast("Trigger "+t.Name+": on = "+t.On+"? It takes ticket, ci_failed, alert, schedule or digest", true)
		return false
	}
	return changed
}

// trigDue says whether schedule t is due now (its time passed today, on
// one of its days, not run today), noting today's run in s when it is.
func trigDue(t orgs.Trigger, s *trigSeen) bool {
	now := trigNow()
	day := now.Format("2006-01-02")
	at, err := time.ParseInLocation("15:04", t.At, now.Location())
	due := err == nil && s.Last != day && now.Hour()*60+now.Minute() >= at.Hour()*60+at.Minute() &&
		(len(t.Days) == 0 || slices.ContainsFunc(t.Days, func(d string) bool { return strings.EqualFold(d, now.Weekday().String()[:3]) }))
	if due {
		s.Last = day
	}
	return due
}

// trigCard makes a card t asked for in org, started when t says so.
func (u *ui) trigCard(org string, t orgs.Trigger, title, notes string, ls []kanban.Link) {
	col := u.b.Column(t.Column)
	if col < 0 {
		col = max(u.b.Column("To do"), 0)
	}
	if title == "" {
		title = t.Name
	}
	c := u.b.Add(col, kanban.Card{Title: title, Notes: notes, Org: org, Links: ls})
	u.save()
	u.showToast(t.Name+" made "+c.Key()+": "+title, false)
	if t.Start {
		u.send(c.ID, startWork)
	}
}

// ciDay is how many times a day a ci_failed trigger tells one card's
// Claude: a fix that fails CI again would otherwise loop, fix, push, fail.
const ciDay = 3

// ciTell tells c's Claude its CI failed, at most ciDay times a day, as
// another Claude's message: it doesn't count as the user's go-ahead past a
// budget, and over one it waits for the user.
func (u *ui) ciTell(c *kanban.Card, text string) {
	key := c.ID + "|" + trigNow().Format(time.DateOnly)
	if u.ciTold[key] >= ciDay {
		u.showToast(c.Key()+": CI failed again; its Claude was told "+strconv.Itoa(ciDay)+" times today, so not again", false)
		return
	}
	if u.ciTold == nil {
		u.ciTold = map[string]int{}
	}
	u.ciTold[key]++
	if u.overBudget(c) == "" && u.shellStop[c.ID] == nil && u.terms[c.ID] == nil { // shown as sent when deliver sends it now
		u.say(c.ID, "you", text)
	}
	u.deliver(c.ID, text, true)
}
