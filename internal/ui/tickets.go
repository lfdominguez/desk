package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"slices"
	"strings"
	"time"

	"gioui.org/layout"

	"github.com/lfdominguez/desk/internal/fzf"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/ui/kit"
	"github.com/lfdominguez/desk/internal/vault"
)

// assigned asks an org's trackers; tests replace it.
var assigned = links.Assigned

// checkTickets asks every organization's trackers for the open tickets
// assigned to the user, off the UI goroutine. Orgs without a Jira, GitHub
// or GitLab account have no Tickets view.
func (u *ui) checkTickets() {
	for _, org := range orgs.List(u.root) {
		cfg, err := orgs.Load(u.root, org)
		if err == nil && len(cfg.Jira)+len(cfg.Linear)+len(cfg.GitHub)+len(cfg.GitLab) == 0 {
			u.later(func() { delete(u.tickets, org); u.tkGen++ }) // its trackers were removed
			continue
		}
		var ts []links.Ticket
		if err == nil {
			done := u.busy(org)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			ts, err = assigned(ctx, cfg)
			cancel()
			done()
		}
		msg := u.fetchErr(err)
		u.later(func() {
			u.tickets[org], u.ticketErr[org] = ts, msg
			u.tkGen++
		})
	}
}

// fetchErr is what a view says for a background fetch's err: a locked
// vault asks to be unlocked; "" none.
func (u *ui) fetchErr(err error) string {
	var locked *vault.LockedError
	switch {
	case errors.As(err, &locked):
		u.later(func() { u.vaultLocked(locked) })
		return "Locked: unlock " + locked.Vault.Name + " for the rest"
	case err != nil:
		return "⚠ " + firstLine(err.Error())
	}
	return ""
}

// reloadConfig looks for organizations again (the folders of the root and
// the nested ones one level down) and asks every link and tracker anew with
// their desk.toml as it is now.
func (u *ui) reloadConfig() {
	u.orgList = orgs.List(u.root)
	for org := range u.tickets {
		if !slices.Contains(u.orgList, org) {
			delete(u.tickets, org)
		}
	}
	for org := range u.alerts {
		if !slices.Contains(u.orgList, org) {
			delete(u.alerts, org)
		}
	}
	clear(u.locked) // vaults may have changed; the next check finds what is still locked
	if u.infra != nil {
		u.infra.Reload()
	}
	u.tkGen++
	u.showToast(fmt.Sprintf("Reloaded %d organizations", len(u.orgList)), false)
	ids := u.linkedIDs()
	go func() { u.checkLinks(ids); u.checkTickets(); u.checkAlerts(); u.checkCI() }()
}

// ticketCard is the board card linking t, if any.
func (u *ui) ticketCard(t links.Ticket) *kanban.Card {
	for _, c := range u.boardCards() {
		if slices.ContainsFunc(c.Links, func(l kanban.Link) bool { return l.URL == t.URL || (tracked(l) && l.Ref == t.Key) }) {
			return u.b.Card(c.ID)
		}
	}
	return nil
}

// workOn opens t's card, or the new-card dialog for it in To do.
func (u *ui) workOn(org string, t links.Ticket) {
	if c := u.ticketCard(t); c != nil {
		u.openPanel(c.ID)
		return
	}
	u.newCard(min(1, len(u.b.Columns)-1))
	u.edit.org.SetText(org)
	u.edit.title.SetText(t.Key + " " + t.Title)
	u.edit.links.SetText(t.URL)
}

// ticketsView is an organization's tickets, under a heading per project,
// that match the search; a click opens the ticket's card, or makes one.
func (u *ui) ticketsView(gtx layout.Context) layout.Dimensions {
	k := u.k
	org := strings.TrimPrefix(u.view, "tk:")
	gtx.Constraints.Min = gtx.Constraints.Max
	kit.Fill(gtx, kit.CPane)
	q := u.search.Text()
	var rows []layout.Widget
	var act func() // the bar's buttons change what the rows draw from: run once drawn
	defer func() {
		if act != nil {
			act()
		}
	}()
	if len(u.pilotTickets(org)) > 0 {
		rows = append(rows, func(gtx layout.Context) layout.Dimensions { return u.pilotBar(gtx, org, &act) })
	}
	if e := u.ticketErr[org]; e != "" {
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 16, Left: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, e, 12, kit.CRed, kit.Sans, 3) })
		})
	}
	project := "\x00"
	for _, t := range u.tickets[org] {
		if _, ok := fzf.Match(q, t.Project+" "+t.Key+" "+t.Title); !ok {
			continue
		}
		if t.Project != project {
			project = t.Project
			rows = append(rows, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 16, Bottom: 6, Left: 4}.Layout(gtx, k.Lbl(t.Project, 12, kit.CText2, kit.Semi(kit.Sans)))
			})
		}
		rows = append(rows, func(gtx layout.Context) layout.Dimensions { return u.ticketRow(gtx, org, t) })
	}
	if len(rows) == 0 {
		msg := "Nothing assigned to you."
		if _, ok := u.tickets[org]; !ok {
			msg = "Asking the trackers…"
		}
		return layout.Center.Layout(gtx, k.Lbl(msg, 13, kit.CText3, kit.Sans))
	}
	w := min(gtx.Constraints.Max.X-gtx.Dp(48), gtx.Dp(820))
	return layout.N.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints = layout.Constraints{Min: image.Pt(w, gtx.Constraints.Max.Y), Max: image.Pt(w, gtx.Constraints.Max.Y)}
		return u.tkList.Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions { return rows[i](gtx) })
	})
}

// ticketRow is one ticket: key, title, status, its card's key when it has
// one; the link button opens it in the browser.
func (u *ui) ticketRow(gtx layout.Context, org string, t links.Ticket) layout.Dimensions {
	k := u.k
	c := u.ticketCard(t)
	return layout.Inset{Bottom: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { // beside the row, not in it: a click is one or the other
				if c != nil { // already worked on: no autopilot for it, the box's room kept so rows line up
					return layout.Spacer{Width: 30, Height: 30}.Layout(gtx)
				}
				return u.archBox(gtx, "tkSel"+t.URL, u.tkSel[t.URL], func() {
					if u.tkSel[t.URL] {
						delete(u.tkSel, t.URL)
					} else {
						u.tkSel[t.URL] = true
					}
				})
			}),
			layout.Rigid(kit.HGap(4)),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				d, ok := k.Button(gtx, "tk"+t.URL, kit.CPanelCard, kit.CHover, kit.RCard, layout.Inset{Top: 9, Bottom: 9, Left: 12, Right: 12}, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					kids := []layout.FlexChild{
						layout.Rigid(k.Lbl(t.Key, 12, kit.CAccText, kit.Mono)),
						layout.Rigid(kit.HGap(10)),
						layout.Flexed(1, k.LblFill(t.Title, 13, kit.CText, kit.Medium(kit.Sans))),
						layout.Rigid(kit.HGap(8)),
						layout.Rigid(k.Pill(t.Status, kit.CText2, kit.CPanel)),
					}
					if c != nil {
						kids = append(kids, layout.Rigid(kit.HGap(6)), layout.Rigid(k.Pill(c.Key(), kit.CAccText, kit.CAccSoft)))
					}
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
				})
				if ok {
					u.workOn(org, t)
				}
				return d
			}),
			layout.Rigid(kit.HGap(4)),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d, ok := k.Button(gtx, "tkOpen"+t.URL, color.NRGBA{}, kit.CHover, kit.RCtl, layout.UniformInset(7), kit.Icon(14, kit.CText3, kit.IcoLink))
				if ok {
					openURL(t.URL)
				}
				return d
			}))
	})
}

// ticketsSentence is the header for an org's tickets: "5 tickets in 1 project".
func (u *ui) ticketsSentence(org string) string {
	ts := u.tickets[org]
	var ps []string
	for _, t := range ts {
		if !slices.Contains(ps, t.Project) {
			ps = append(ps, t.Project)
		}
	}
	n := func(i int, s string) string { return fmt.Sprintf("%d %s%s", i, s, map[bool]string{true: "s"}[i != 1]) }
	return n(len(ts), "ticket") + " in " + n(len(ps), "project")
}
