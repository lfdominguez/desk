package ui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"maps"
	"slices"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/ui/kit"
	"github.com/lfdominguez/desk/internal/vault"
)

// learnRun and jiraStatuses are claude.Learn and links.Statuses; tests
// replace them.
var (
	learnRun     = claude.Learn
	jiraStatuses = links.Statuses
)

// wfMulti are the workflow's editors of several lines.
var wfMulti = []string{"instructions", "rules", "follow"}

// loadWorkflow is an org page's Ticket workflow as read ([workflow]):
// an editor per key, a column's status as "status.<column>", the statuses
// others own as "status → column" lines.
func loadWorkflow(raw map[string]any, cols []string) *cfgEntry {
	e := &cfgEntry{kind: "workflow", eds: map[string]*widget.Editor{}, src: map[string]int{}, on: map[string]bool{}}
	for _, k := range wfMulti {
		e.eds[k] = &widget.Editor{}
	}
	w, _ := raw["workflow"].(map[string]any)
	str := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }
	for _, k := range []string{"instructions", "summary", "rules", "learned"} {
		e.ed(k).SetText(str(w, k))
	}
	status, _ := w["status"].(map[string]any)
	for _, c := range cols {
		e.ed("status." + c).SetText(str(status, c))
	}
	follow, _ := w["follow"].(map[string]any)
	var lines []string
	for _, s := range slices.Sorted(maps.Keys(follow)) {
		lines = append(lines, s+" → "+str(follow, s))
	}
	e.ed("follow").SetText(strings.Join(lines, "\n"))
	e.old, e.base = true, e.snap()
	return e
}

// boardColumns is the board's column titles, in order.
func (u *ui) boardColumns() []string {
	var out []string
	for _, c := range u.b.Columns {
		out = append(out, c.Title)
	}
	return out
}

// saveWorkflow writes the page's Ticket workflow into out, refusing a line
// of the statuses others own that names no column; nothing set, no
// [workflow]. Columns not on the board keep what they had.
func (u *ui) saveWorkflow(d *cfgDialog, out map[string]any) bool {
	e, cols := d.wf, u.boardColumns()
	if !e.edited() { // as read: kept as it was, keys desk doesn't know too, and a line it can't read doesn't hold up the rest
		return true
	}
	old, _ := out["workflow"].(map[string]any)
	w := maps.Clone(old) // keys desk doesn't know stay
	if w == nil {
		w = map[string]any{}
	}
	for _, k := range []string{"instructions", "summary", "rules", "learned"} {
		set(w, k, e.text(k))
	}
	status, _ := old["status"].(map[string]any)
	status = maps.Clone(status)
	if status == nil {
		status = map[string]any{}
	}
	for _, c := range cols {
		set(status, c, e.text("status."+c))
	}
	set(w, "status", status)
	follow := map[string]any{}
	for i, l := range strings.Split(e.ed("follow").Text(), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		s, c, ok := cutArrow(l)
		at := slices.IndexFunc(cols, func(x string) bool { return strings.EqualFold(x, c) })
		if !ok || s == "" || at < 0 {
			d.err, d.errAt = fmt.Sprintf("Ticket workflow, line %d of the statuses others move to: write it as Status → Column, a column of the board", i+1), "workflow"
			return false
		}
		follow[s] = cols[at]
	}
	set(w, "follow", follow)
	set(out, "workflow", w)
	return true
}

// cutArrow splits "Status → Column" (or ->, =).
func cutArrow(l string) (status, col string, ok bool) {
	for _, sep := range []string{"→", "->", "="} {
		if s, c, found := strings.Cut(l, sep); found {
			return strings.TrimSpace(s), strings.TrimSpace(c), true
		}
	}
	return "", "", false
}

// learnWorkflow has Haiku learn the page's instructions (learnRun), with
// the organization's Jira statuses: what it understood fills the page's
// fields, to check before Save.
func (u *ui) learnWorkflow(d *cfgDialog) {
	ins := strings.TrimSpace(d.wf.ed("instructions").Text())
	if ins == "" {
		d.wfErr = "Write how tickets move here, or where to read it, first"
		return
	}
	cfg, err := orgs.Load(u.root, d.org)
	if err != nil {
		d.wfErr = firstLine(err.Error())
		return
	}
	org, cols := d.org, u.boardColumns()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	d.wfBusy, d.wfErr, d.wfStop = true, "", cancel
	go func() {
		defer cancel()
		var l claude.Lesson
		st, err := jiraStatuses(ctx, cfg)
		if le := (*vault.LockedError)(nil); errors.As(err, &le) {
			u.later(func() { u.vaultLocked(le) })
		}
		var env []string
		if err == nil {
			env, err = u.goEnv(org, "workflow") // asks for a locked vault itself
		}
		if err == nil {
			l, err = learnRun(ctx, claude.Study{Home: u.home, Account: orgs.Account(u.root, org), Dir: orgs.Dir(u.root, org, ""),
				Env: env, Instructions: ins, Columns: cols, Statuses: st})
		}
		stopped := ctx.Err() == context.Canceled
		u.later(func() {
			d.wfBusy, d.wfStop = false, nil
			switch {
			case stopped:
				d.wfErr = "Stopped"
			case err != nil:
				d.wfErr = firstLine(err.Error())
			default:
				u.taught(d, l, cols)
			}
		})
	}()
}

// stopLearning stops every page's Learn as the settings close or start
// over: the fields it would fill are gone.
func (u *ui) stopLearning() {
	if u.cfg == nil {
		return
	}
	for _, d := range u.cfg.set.pages {
		if d.wfStop != nil {
			d.wfStop()
		}
	}
}

// taught puts lesson l in the page's fields: each column's status, the
// statuses others own, the rules, what was understood and when.
func (u *ui) taught(d *cfgDialog, l claude.Lesson, cols []string) {
	e := d.wf
	e.ed("summary").SetText(l.Summary)
	e.ed("rules").SetText(l.Rules)
	e.ed("learned").SetText(time.Now().Format(kanban.DateLayout))
	for _, c := range cols {
		e.ed("status." + c).SetText(l.Status[c])
	}
	var lines []string
	for _, s := range slices.Sorted(maps.Keys(l.Follow)) {
		lines = append(lines, s+" → "+l.Follow[s])
	}
	e.ed("follow").SetText(strings.Join(lines, "\n"))
}

// cfgWorkflow is an org page's Ticket workflow, under its servers: the
// user's instructions, Learn, and what it understood, to check.
func (u *ui) cfgWorkflow(gtx layout.Context) layout.Dimensions {
	k, d := u.k, u.cfg
	e := d.wf
	text := func(s string, size unit.Sp, c color.NRGBA) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, s, size, c, kit.Sans, 0) }
	}
	labeled := func(label string, w layout.Widget) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, layout.Rigid(k.Caption(label)), layout.Rigid(kit.VGap(5)), layout.Rigid(w))
			})
		})
	}
	learn := func(gtx layout.Context) layout.Dimensions {
		kids := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			label := "Learn"
			if d.wfBusy {
				label = "Learning…"
			}
			dim, ok := k.KBtn(gtx, "cfgLearn", kit.Secondary, label, "")
			if ok && !d.wfBusy {
				d.act = func() { u.learnWorkflow(d) }
			}
			return dim
		})}
		if d.wfBusy {
			kids = append(kids, layout.Rigid(kit.HGap(8)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				dim, ok := k.KBtn(gtx, "cfgLearnStop", kit.Ghost, "Stop", "")
				if ok && d.wfStop != nil {
					d.wfStop()
				}
				return dim
			}))
		}
		note, tone := "", kit.CText3
		switch {
		case d.wfBusy:
			note = "Haiku reads the instructions, what they point at and the Jira projects' and Linear teams' statuses…"
		case d.wfErr != "":
			note, tone = d.wfErr, kit.CRedText
		case e.text("learned") != "":
			note = "Learned " + e.text("learned") + ". Check it below, then Save."
		}
		if note != "" {
			kids = append(kids, layout.Rigid(kit.HGap(12)), layout.Flexed(1, text(note, 12, tone)))
		}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
	}
	kids := []layout.FlexChild{
		layout.Rigid(kit.VGap(18)), layout.Rigid(k.Caption("Ticket workflow")), layout.Rigid(kit.VGap(4)),
		layout.Rigid(text("How this organization's tickets move, as its people use them: what each status means, who moves tickets where. "+
			"Write it, or say where to read it; Learn has Haiku read it with the Jira projects' and Linear teams' statuses and fills in what's below. "+
			"Check it, then Save: card sessions follow the rules, and tickets someone else moves lead their cards.", 12, kit.CText3)),
		layout.Rigid(kit.VGap(12)),
		labeled("Instructions", k.Field(e.ed("instructions"), "e.g. Read ./docs/process. \"Test\" means deployed to the testing environment; only QA moves a ticket to Done.", 60)),
		layout.Rigid(learn), layout.Rigid(kit.VGap(12)),
	}
	if s := e.text("summary"); s != "" {
		kids = append(kids, labeled("What Haiku understood", text(s, 12, kit.CText2)))
	}
	for _, c := range u.boardColumns() {
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min.X = gtx.Dp(130)
						return k.Label(gtx, "Card in "+c, 12, kit.CText2, kit.Sans, 1)
					}),
					layout.Flexed(1, k.Field(e.ed("status."+c), "its ticket's status; empty: the status category's guess", 0)))
			})
		}))
	}
	kids = append(kids, layout.Rigid(kit.VGap(6)),
		labeled("Statuses others move tickets to: the card follows", k.Field(e.ed("follow"), "one per line, Status → Column: Test → Review", 40)),
		labeled("Rules for the card's Claude", k.Field(e.ed("rules"), "what each status means here, when Claude moves a ticket itself (ticket_status), what it never does", 60)))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}
