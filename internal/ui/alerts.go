package ui

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"strings"
	"time"

	"gioui.org/layout"

	"github.com/lfdominguez/desk/internal/fzf"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// nagiosAlerts asks an org's Nagios; tests replace it.
var nagiosAlerts = links.Alerts

// checkAlerts asks every organization's Nagios for its warnings and
// errors, off the UI goroutine. Orgs without one have no Alerts view.
func (u *ui) checkAlerts() {
	for _, org := range orgs.List(u.root) {
		cfg, err := orgs.Load(u.root, org)
		if err == nil && len(cfg.Nagios) == 0 {
			u.later(func() { delete(u.alerts, org); u.tkGen++ })
			continue
		}
		var as []links.Alert
		if err == nil {
			done := u.busy(org)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			as, err = nagiosAlerts(ctx, cfg)
			cancel()
			done()
		}
		msg := u.fetchErr(err)
		u.later(func() {
			u.alerts[org], u.alertErr[org] = as, msg
			u.tkGen++
		})
	}
}

// alertQuiet is an alert nobody needs to look at: acknowledged, or snoozed.
func (u *ui) alertQuiet(org string, a links.Alert) bool {
	_, snoozed := u.snoozedUntil(org, a)
	return a.Acked || snoozed
}

// alertCount is an org's alerts nobody has acknowledged or snoozed.
func (u *ui) alertCount(org string) int {
	n := 0
	for _, a := range u.alerts[org] {
		if !u.alertQuiet(org, a) {
			n++
		}
	}
	return n
}

// alertColor is the worst of an org's alerts not acknowledged or snoozed.
func (u *ui) alertColor(org string) color.NRGBA {
	c := kit.CText3
	for _, a := range u.alerts[org] {
		switch {
		case u.alertQuiet(org, a):
		case a.Critical():
			return kit.CRed
		default:
			c = kit.COrange
		}
	}
	return c
}

// alertsSentence is the header for an org's alerts: "2 errors · 3 warnings".
func (u *ui) alertsSentence(org string) string {
	var errs, warns, acked, snoozed int
	for _, a := range u.alerts[org] {
		_, sn := u.snoozedUntil(org, a)
		switch {
		case sn:
			snoozed++
		case a.Acked:
			acked++
		case a.Critical():
			errs++
		default:
			warns++
		}
	}
	s := plural(errs, "error", "errors") + " · " + plural(warns, "warning", "warnings")
	if acked > 0 {
		s += fmt.Sprintf(" · %d acknowledged", acked)
	}
	if snoozed > 0 {
		s += fmt.Sprintf(" · %d snoozed", snoozed)
	}
	return s
}

// alertsView is an organization's Nagios alerts that match the search,
// errors first; a click opens the alert in Nagios.
func (u *ui) alertsView(gtx layout.Context) layout.Dimensions {
	k := u.k
	org := strings.TrimPrefix(u.view, "al:")
	gtx.Constraints.Min = gtx.Constraints.Max
	kit.Fill(gtx, kit.CPane)
	q := u.search.Text()
	var rows []layout.Widget
	if e := u.alertErr[org]; e != "" {
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 16, Left: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, e, 12, kit.CRed, kit.Sans, 3) })
		})
	}
	head := ""
	var first, later []links.Alert // snoozed ones last
	for _, a := range u.alerts[org] {
		if _, sn := u.snoozedUntil(org, a); sn {
			later = append(later, a)
		} else {
			first = append(first, a)
		}
	}
	for _, a := range append(first, later...) {
		if _, ok := fzf.Match(q, a.Monitor+" "+a.Host+" "+a.Service+" "+a.Output); !ok {
			continue
		}
		h := map[bool]string{true: "Errors", false: "Warnings"}[a.Critical()]
		if _, sn := u.snoozedUntil(org, a); sn {
			h = "Snoozed"
		}
		if h != head {
			head = h
			rows = append(rows, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 16, Bottom: 6, Left: 4}.Layout(gtx, k.Lbl(h, 12, kit.CText2, kit.Semi(kit.Sans)))
			})
		}
		rows = append(rows, func(gtx layout.Context) layout.Dimensions { return u.alertRow(gtx, org, a) })
	}
	if len(rows) == 0 {
		msg := "No warnings or errors."
		if _, ok := u.alerts[org]; !ok {
			msg = "Asking Nagios…"
		}
		return layout.Center.Layout(gtx, k.Lbl(msg, 13, kit.CText3, kit.Sans))
	}
	w := min(gtx.Constraints.Max.X-gtx.Dp(48), gtx.Dp(820))
	return layout.N.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints = layout.Constraints{Min: image.Pt(w, gtx.Constraints.Max.Y), Max: image.Pt(w, gtx.Constraints.Max.Y)}
		return u.alList.Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions { return rows[i](gtx) })
	})
}

// alertRow is one alert: its state, host and service, since when, the
// monitor; under them, what the check said. Acknowledged ones are dimmed.
// With servers, Investigate makes it a card (investigate).
func (u *ui) alertRow(gtx layout.Context, org string, a links.Alert) layout.Dimensions {
	k := u.k
	sc := kit.COrange
	if a.Critical() {
		sc = kit.CRed
	}
	fg := kit.CText
	key := alertKey(org, a)
	until, snoozed := u.snoozedUntil(org, a)
	if a.Acked || snoozed {
		sc, fg = kit.CText3, kit.CText2
	}
	what := a.Host
	if a.Service != "" {
		what += " · " + a.Service
	}
	row := func(gtx layout.Context) layout.Dimensions {
		d, ok := k.Button(gtx, "al"+a.URL, kit.CPanelCard, kit.CHover, kit.RCard, layout.Inset{Top: 9, Bottom: 10, Left: 12, Right: 12}, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			kids := []layout.FlexChild{
				layout.Rigid(k.Pill(strings.ToUpper(a.State), sc, kit.Mix(kit.CPanel, sc, .15))),
				layout.Rigid(kit.HGap(10)),
				layout.Flexed(1, k.LblFill(what, 13, fg, kit.Medium(kit.Sans))),
			}
			if a.Acked {
				kids = append(kids, layout.Rigid(kit.HGap(8)), layout.Rigid(k.Pill("acknowledged", kit.CText3, kit.CPanel)))
			}
			if snoozed {
				kids = append(kids, layout.Rigid(kit.HGap(8)), layout.Rigid(k.Pill("snoozed until "+until.Format("Mon Jan 2 15:04"), kit.CText3, kit.CPanel)))
			}
			kids = append(kids,
				layout.Rigid(kit.HGap(8)),
				layout.Rigid(k.Lbl(ago(a.Since, gtx.Now), 12, kit.CText3, kit.Sans)),
				layout.Rigid(kit.HGap(8)),
				layout.Rigid(k.Pill(a.Monitor, kit.CText2, kit.CPanel)))
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
				}),
				layout.Rigid(kit.VGap(5)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return k.Label(gtx, a.Output, 12, kit.CText2, kit.Mono, 2)
				}))
		})
		if ok {
			openURL(a.URL)
		}
		return d
	}
	btn := func(id, label string, fill color.NRGBA, act func()) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			d, ok := k.Button(gtx, id+a.URL, fill, kit.Mix(fill, kit.CWhite, .08), kit.RCtl, layout.Inset{Top: 7, Bottom: 7, Left: 10, Right: 10}, k.Lbl(label, 12, kit.CText, kit.Semi(kit.Sans)))
			if ok {
				act()
			}
			return d
		})
	}
	line := []layout.FlexChild{layout.Flexed(1, row), layout.Rigid(kit.HGap(6))}
	if snoozed {
		line = append(line, btn("alSnz", "Unsnooze", kit.CPanelCard, func() { u.snooze(key, 0) }))
	} else {
		line = append(line, btn("alSnz", "Snooze", kit.CPanelCard, func() {
			if u.snoozing == key {
				u.snoozing = ""
			} else {
				u.snoozing = key
			}
			u.tkGen++
		}))
	}
	if u.infraOrg(org) != nil {
		fill := kit.CPanelCard
		if a.Critical() && !a.Acked && !snoozed {
			fill = kit.CAcc
		}
		line = append(line, layout.Rigid(kit.HGap(6)), btn("alInv", "Investigate", fill, func() {
			u.investigate(org, a.Host, a.Service, a.State, a.Output, a.URL, a.Since)
		}))
	}
	return layout.Inset{Bottom: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		row := func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, line...)
		}
		d := row(gtx)
		if u.snoozing == key {
			u.snoozePopover(gtx, d.Size.X, d.Size.Y+gtx.Dp(4), alertName(key), func(d time.Duration) {
				u.snoozing = ""
				if d > 0 {
					u.snooze(key, d)
				}
				u.tkGen++
			})
		}
		return d
	})
}

// investigate makes a monitoring alert (service "": the host itself) a card
// in its org, in progress, and has the card's Claude look into it: the
// server checks go to the org's infra Claude with desk peer, as any server
// work. An alert that has an open card (not Done) shows that one instead.
func (u *ui) investigate(org, host, service, state, output, url string, since time.Time) {
	u.openSession(u.investigation(org, host, service, state, output, url, since, ""))
}

// investigation is investigate without opening the card: its id (the
// open one's, or a new one started on it; why ends its notes when given).
func (u *ui) investigation(org, host, service, state, output, url string, since time.Time, why string) string {
	name := host
	if service != "" {
		name += " / " + service
	}
	title := "Investigate " + strings.ToUpper(state) + " " + name
	for i, col := range u.b.Columns {
		for _, c := range col.Cards {
			if c.Org == org && c.Title == title && i != u.b.Column("Done") {
				return c.ID
			}
		}
	}
	col := u.b.Column("In progress")
	if col < 0 {
		col = 0
	}
	notes := fmt.Sprintf("Monitoring alert: %s %s since %s.\n\n```\n%s\n```", state, name, since.Format("Jan 2 15:04"), output)
	if url != "" {
		notes += "\n\n" + url
	}
	if why != "" {
		notes += "\n\n" + why
	}
	c := u.b.Add(col, kanban.Card{Title: title, Notes: notes, Org: org, Alert: alertKey(org, links.Alert{Host: host, Service: service})})
	u.save()
	u.send(c.ID, "Investigate this alert: check the monitoring and metrics and have the infra Claude look on the servers with "+
		"read-only commands; find the likely cause and propose a fix. Nothing changes on the servers without my approval.")
	return c.ID
}
