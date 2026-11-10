package infra

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/keys"
)

// Alerts: a [[nagios]] source with alerts = true is polled every minute. Hard,
// unacknowledged problems outside downtime (host DOWN/UNREACHABLE, service
// CRITICAL) become alerts: new ones notify the desktop, all of them are in the
// palette as "investigate" items, and their hosts' sidebar dots turn red.
// Investigating opens the org's Claude tab with the alert as the question.

const alertEvery = time.Minute

type nagiosAlert struct {
	Org, Dir      string // the org and its infra dir (to open its tab)
	Host, Service string // Service "": the host itself
	State, Output string
	Since         time.Time
}

func (a nagiosAlert) key() string { return a.Org + "\x00" + a.Host + "\x00" + a.Service }

func (a nagiosAlert) name() string {
	if a.Service == "" {
		return a.Host
	}
	return a.Host + " / " + a.Service
}

// alertsMsg is one poll's alerts of one source: they replace that source's
// previous ones.
type alertsMsg struct {
	src    string // org + source name
	alerts []nagiosAlert
	first  bool // the first poll: list them, don't notify each one
}

// startAlerts polls every source that asks for it, for all orgs in tree.
func startAlerts(tree []node) {
	for _, n := range tree {
		if n.depth != 0 || n.dir == "" {
			continue
		}
		o, err := orgFor(n.dir)
		if err != nil {
			continue
		}
		for _, s := range o.Nagios {
			if s.Alerts {
				go pollAlerts(o, s.Name, n.org, n.dir)
			}
		}
	}
}

func pollAlerts(o *orgConfig, src, org, dir string) {
	for first := true; ; first = false {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute) // includes a vault passphrase
		hosts, svcs, err := o.nagiosStatus(ctx, src)
		cancel()
		if err == nil {
			dispatch(alertsMsg{src: org + "/" + src, alerts: nagiosAlerts(org, dir, hosts, svcs), first: first})
		}
		time.Sleep(alertEvery)
	}
}

// nagiosAlerts picks the problems worth an alert, worst and newest first.
func nagiosAlerts(org, dir string, hosts map[string]nagiosObj, svcs map[string]map[string]nagiosObj) []nagiosAlert {
	var out []nagiosAlert
	hard := func(o nagiosObj) bool { return o.CurrentAttempt >= o.MaxAttempts && !o.Acked && o.Downtime == 0 }
	down := map[string]bool{}
	for h, o := range hosts {
		if st := hostState[o.Status]; (st == "DOWN" || st == "UNREACHABLE") && hard(o) {
			down[h] = true
			out = append(out, nagiosAlert{org, dir, h, "", st, strings.TrimSpace(o.Output), time.UnixMilli(o.LastChange)})
		}
	}
	for h, m := range svcs {
		if down[h] {
			continue // its services fail because the host is down
		}
		for name, o := range m {
			if svcState[o.Status] == "CRITICAL" && hard(o) {
				out = append(out, nagiosAlert{org, dir, h, name, "CRITICAL", strings.TrimSpace(o.Output), time.UnixMilli(o.LastChange)})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if severity[out[i].State] != severity[out[j].State] {
			return severity[out[i].State] < severity[out[j].State]
		}
		return out[i].Since.After(out[j].Since)
	})
	return out
}

// setAlerts takes a poll's result and notifies the alerts that are new.
func (m *model) setAlerts(msg alertsMsg) {
	if m.alerts == nil {
		m.alerts = map[string][]nagiosAlert{}
	}
	old := map[string]bool{}
	for _, a := range m.alerts[msg.src] {
		old[a.key()] = true
	}
	m.alerts[msg.src] = msg.alerts
	m.alertWorst = nil
	if msg.first {
		return
	}
	for _, a := range msg.alerts {
		if !old[a.key()] {
			desktopNotify(a.State+" · "+a.name(), a.Org+": "+a.Output)
			m.flash = "▲ " + a.State + " " + a.name() + " · " + keys.Short("infra.palette") + " → investigate"
		}
	}
}

// allAlerts is every current alert, worst first.
func (m *model) allAlerts() []nagiosAlert {
	var out []nagiosAlert
	for _, as := range m.alerts {
		out = append(out, as...)
	}
	sort.SliceStable(out, func(i, j int) bool { return severity[out[i].State] < severity[out[j].State] })
	return out
}

// hostAlert is the worst alert state of tree host n ("" for none). Nagios
// names may be the ssh alias, its address, or a FQDN starting with the alias;
// a host's "#@ nagios" name is the only one it answers to.
func (m *model) hostAlert(n node) string {
	w, org := m.worstAlerts(), n.org+"\x00"
	if n.nagios != "" {
		return w[org+strings.ToLower(n.nagios)]
	}
	l := strings.ToLower(n.label)
	return worseAlert(worseAlert(w[org+l], w[org+strings.ToLower(n.addr)]), w[org+"."+l])
}

// worseAlert is the worse of two alert states ("" none).
func worseAlert(a, b string) string {
	if a == "" || b != "" && severity[b] < severity[a] {
		return b
	}
	return a
}

// worstAlerts is the worst alert state by org + "\x00" + lower-case host,
// and by org + "\x00." + each name a host's FQDN starts with ("web-01" for
// web-01.acme.lan), built once per change of the alerts (setAlerts drops it):
// the snapshot asks for every host of the tree.
func (m *model) worstAlerts() map[string]string {
	if m.alertWorst != nil {
		return m.alertWorst
	}
	w := map[string]string{}
	for _, as := range m.alerts {
		for _, a := range as {
			k, h := a.Org+"\x00", strings.ToLower(a.Host)
			w[k+h] = worseAlert(w[k+h], a.State)
			for i := range len(h) {
				if h[i] == '.' {
					w[k+"."+h[:i]] = worseAlert(w[k+"."+h[:i]], a.State)
				}
			}
		}
	}
	m.alertWorst = w
	return w
}

// investigate opens a's org tab and asks its Claude to look into it.
func (m *model) investigate(a nagiosAlert) {
	if m.onInvestigate != nil {
		m.onInvestigate(a.Org, a.Host, a.Service, a.State, a.Output, a.Since)
		return
	}
	for _, n := range m.tree {
		if n.depth == 0 && n.org == a.Org {
			m.group = ""
			m.openOrg(n, "")
			break
		}
	}
	t := m.tab()
	if t == nil || t.org != a.Org {
		return
	}
	q := fmt.Sprintf("(desk) Nagios alert: %s %s since %s: %s\nInvestigate: check the monitoring and metrics, connect to the "+
		"server if needed, find the likely cause with read-only commands, and propose a fix. Change nothing without approval.",
		a.State, a.name(), a.Since.Format("Jan 2 15:04"), a.Output)
	if t.busy {
		t.asked = append(t.asked, q)
		t.chat = append(t.chat, &chatEntry{who: "note", text: "queued: investigate " + a.name()})
		return
	}
	if err := m.send(t, q, &chatEntry{who: "you", text: "▲ investigate " + a.State + " " + a.name()}); err != nil {
		t.chat = append(t.chat, &chatEntry{who: "error", text: err.Error()})
	}
}

// Daily health check: [schedule] health = "08:30" in desk.toml has the org's
// Claude run a read-only check at that time (or as soon as desk runs after
// it), in the org tab, opened in the background if needed. The result waits
// in the tab and a desktop notification says it's ready.

const healthPrompt = "(desk) Daily health check of %s. Use only read-only tools: monitoring and metrics first, then connect " +
	"to servers only where they point to something. Report, worst first: open problems, anything trending badly (disk, " +
	"memory, load, error rates, certificates or backups), and what needs attention today. Keep it short; say so if all is well."

func healthStamp(org string) string {
	return filepath.Join(stateDir(), "health", orgDirName(org)+".last")
}

// dueHealth gives the orgs whose daily check is due at now.
func (m *model) dueHealth(now time.Time) []node {
	var due []node
	for _, n := range m.tree {
		if n.depth != 0 || n.dir == "" {
			continue
		}
		o, err := orgFor(n.dir)
		if err != nil || o.Schedule.Health == "" {
			continue
		}
		at, err := time.ParseInLocation("15:04", o.Schedule.Health, now.Location())
		if err != nil {
			continue
		}
		at = time.Date(now.Year(), now.Month(), now.Day(), at.Hour(), at.Minute(), 0, 0, now.Location())
		last, _ := os.ReadFile(healthStamp(n.org))
		if !now.Before(at) && strings.TrimSpace(string(last)) != now.Format("2006-01-02") {
			due = append(due, n)
		}
	}
	return due
}

// runHealth starts the due checks (checked every few seconds from tickMsg).
func (m *model) runHealth(now time.Time) {
	for _, n := range m.dueHealth(now) {
		t, err := m.peerTab(n.org, "")
		if err != nil || t.busy {
			continue // next tick
		}
		os.MkdirAll(filepath.Dir(healthStamp(n.org)), 0o700)
		os.WriteFile(healthStamp(n.org), []byte(now.Format("2006-01-02")), 0o600)
		if err := m.send(t, fmt.Sprintf(healthPrompt, n.org), &chatEntry{who: "you", text: "☀ daily health check"}); err != nil {
			t.chat = append(t.chat, &chatEntry{who: "error", text: err.Error()})
			continue
		}
		t.health = true
	}
}
