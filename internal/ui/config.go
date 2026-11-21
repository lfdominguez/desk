package ui

import (
	"cmp"
	"fmt"
	"image"
	"image/color"
	"maps"
	"slices"
	"strconv"
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/keys"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// cfgField is one setting of an account: a text, a comma-separated list, or
// a secret (where its token comes from).
type cfgField struct {
	key, label, hint string
	list, toggle     bool
	num              bool     // a whole number
	choices          []string // one of these words, picked
	tier             bool     // a provider's model in place of Claude's key (config_models.go)
	secret           string   // "." the account's own token (keys at its top), else the sub-table's key
	only             string   // a vault's: shown for this kind only
}

type cfgKind struct {
	key, title string
	fields     []cfgField
}

// cfgKinds are the accounts desk.toml has, in the dialog's order.
var cfgKinds = []cfgKind{
	{"vault", "Vault", []cfgField{
		{key: "name", label: "Name", hint: "what tokens below call it, e.g. globex"},
		{key: "kind"},
		{key: "url", label: "URL", hint: "https://vault.bitwarden.com, or your server"},
		{key: "email", label: "Email", hint: "the account", only: "bitwarden"},
		{key: "client_id", label: "API key client ID", hint: "optional: user.…, skips 2FA", only: "bitwarden"},
		{key: "client_secret", label: "API key secret", secret: "client_secret", only: "bitwarden"},
		{key: "config_file", label: "go-passbolt-cli config", hint: "~/Secure/passbolt_globex_config.toml: server, key, passphrase and TOTP from it", only: "passbolt"},
		{key: "key_file", label: "Private key file", hint: "~/Secure/passbolt.asc (armored); empty with a CLI config", only: "passbolt"},
		{key: "password", label: "Password", secret: "password"},
	}},
	{"github", "GitHub", []cfgField{
		{key: "name", label: "Name", hint: "optional"},
		{key: "host", label: "Host", hint: "github.com, or your Enterprise host"},
		{key: "owners", label: "Owners", hint: "users or orgs it answers for, comma separated; empty: all", list: true},
		{key: "token", label: "Token", secret: "."},
	}},
	{"jira", "Jira", []cfgField{
		{key: "name", label: "Name", hint: "optional"},
		{key: "url", label: "URL", hint: "https://you.atlassian.net"},
		{key: "email", label: "Email", hint: "the account the API token belongs to"},
		{key: "projects", label: "Projects", hint: "keys like ABC, comma separated: bare keys link here", list: true},
		{key: "token", label: "API token", secret: "."},
		{key: "worklog", label: "Log work time on the issue at Done", toggle: true},
	}},
	{"linear", "Linear", []cfgField{
		{key: "name", label: "Name", hint: "optional"},
		{key: "workspace", label: "Workspace", hint: "acme in linear.app/acme: bare keys link there"},
		{key: "teams", label: "Teams", hint: "keys like ENG, comma separated: bare keys link here, new tickets go in the first", list: true},
		{key: "token", label: "API key", secret: "."},
		{key: "worklog", label: "Work time as a comment at Done", toggle: true},
	}},
	{"bitbucket", "Bitbucket", []cfgField{
		{key: "name", label: "Name", hint: "optional"},
		{key: "workspace", label: "Workspace", hint: "empty: all"},
		{key: "user", label: "User", hint: "with an app password; empty: the token is a bearer token"},
		{key: "token", label: "Token", secret: "."},
	}},
	{"gitlab", "GitLab", []cfgField{
		{key: "name", label: "Name", hint: "optional"},
		{key: "url", label: "URL", hint: "https://gitlab.com, or your server"},
		{key: "groups", label: "Groups", hint: "comma separated; empty: all", list: true},
		{key: "token", label: "Token", secret: "."},
	}},
	{"nagios", "Nagios", []cfgField{
		{key: "name", label: "Name", hint: "what its alerts say they come from, e.g. prod"},
		{key: "url", label: "URL", hint: "its web root: https://mon.example.com/nagios"},
		{key: "user", label: "User", hint: "the CGIs' login; empty: none"},
		{key: "token", label: "Password", secret: "."},
	}},
	{"provider", "Model provider", []cfgField{
		{key: "name", label: "Name", hint: "what Models below call it, e.g. openrouter"},
		{key: "base_url", label: "Base URL", hint: "https://openrouter.ai/api, or your proxy's: Anthropic's API, not OpenAI's"},
		{key: "token", label: "API key", secret: "."},
		{key: "opus", label: "In place of Opus", tier: true},
		{key: "sonnet", label: "In place of Sonnet", tier: true},
		{key: "haiku", label: "In place of Haiku", tier: true},
	}},
	{"trigger", "Trigger", []cfgField{
		{key: "name", label: "Name", hint: "what it is called; what it handled is kept by it"},
		{key: "on", label: "On", choices: []string{"ticket", "ci_failed", "alert", "schedule", "digest"}},
		{key: "project", label: "Project", hint: "ticket: only this project or team"},
		{key: "match", label: "Match", hint: "ticket: in its title; alert: in its host or service"},
		{key: "at", label: "At", hint: "schedule, digest: 09:00"},
		{key: "days", label: "Days", hint: "schedule, digest: mon, thu; empty: every day", list: true},
		{key: "after", label: "After minutes", hint: "alert: how long it has lasted first", num: true},
		{key: "column", label: "Column", hint: "where its cards go; empty: To do"},
		{key: "title", label: "Card title", hint: "schedule"},
		{key: "notes", label: "Card notes", hint: "schedule: what the card is to do"},
		{key: "start", label: "Start its Claude", toggle: true},
		{key: "warnings", label: "Alert: warnings too", toggle: true},
		{key: "off", label: "Off", toggle: true},
	}},
}

// cfgAbbr is each kind's tile on its card.
var cfgAbbr = map[string]string{"bitwarden": "BW", "passbolt": "PB", "github": "GH", "jira": "JI", "linear": "LN", "bitbucket": "BB", "gitlab": "GL", "nagios": "NG", "provider": "AI", "trigger": "TR"}

var vaultKinds = []string{"bitwarden", "passbolt"}

// Token sources, as the secret choice shows them, and the keys each writes.
const (
	srcNone = iota
	srcVault
	srcEnv
	srcCmd
	srcFile
)

var srcKeys = [][]string{srcVault: {"vault", "item", "field"}, srcEnv: {"token_env"}, srcCmd: {"token_cmd"}, srcFile: {"token"}}

// An organization's health in the settings' nav, read when they open.
const (
	orgEmpty  = iota // no accounts
	orgOK            // its desk.toml reads and has accounts
	orgBroken        // its desk.toml doesn't read
)

// cfgDialog is one page of the settings: an organization's desk.toml, or
// the search folders (org = cfgFolders). The pages visited share a cfgSet,
// so switching pages keeps their edits until Save or Discard.
type cfgDialog struct {
	org     string
	file    string         // its desk.toml, found (and moved to .desk/) once on load: not per frame
	raw     map[string]any // the file; tables desk doesn't edit are kept
	entries []*cfgEntry
	langs   *cfgEntry // cfgFolders only: the languages ("card", "chat"), saved with the folders
	opts    *cfgEntry // an org page only: its top-level switches (on["keep_login"]: clear_login = false)
	models  *cfgEntry // an org page only: its Models part (config_models.go)
	wf      *cfgEntry // an org page only: its Ticket workflow (config_workflow.go)
	wfBusy  bool      // Learn runs
	wfErr   string    // why Learn didn't, or Stopped
	wfStop  func()    // stops Learn
	me      *cfgEntry // an org page only: its Who am I (config_me.go)
	git     *cfgEntry // an org page only: [git] (on["worktrees"]); comp its [company] budgets (config_pages.go)
	comp    *cfgEntry
	phone   *cfgEntry // cfgFolders only: settings.toml's [telegram] bot, its account in tgacct, and [newrelic] in diag, saved with the folders
	tgacct  *cfgEntry
	diag    *cfgEntry
	list    layout.List
	err     string
	errAt   string // the part err is about, shown when Save is refused
	broken  bool   // the file didn't parse: saving would lose it
	act     func() // a button's action, run after the dialog is drawn
	open    *cfgEntry
	warn    bool // esc with unsaved changes: the bar says so, esc again drops them
	dirty   int  // unsaved changes of every page, after the last frame: in sig()
	n0      int  // the accounts or folders as read
	set     *cfgSet
	// the search folders page's preview, scanned again when the folders change
	prevKey     string
	prev        []orgs.Found
	skipped     []string
	showSkipped bool
	asbru       []string // an org page's Import from Ásbrú: its groups while the picker is open (nil: closed)
	srvFile     string   // an org page's servers file line, read on its first frame
	// the Keyboard shortcuts page (config_keys.go)
	kdraft map[string]*keys.Chord // unsaved rebindings by ID; nil: back to the defaults
	rec    string                 // the shortcut recording its next key
	recErr string                 // why the last key recorded was refused
}

// sig is what of d the frame draws from, for ui.sig: printing d itself
// would print its whole file and editors, twice a frame.
func (d *cfgDialog) sig() string {
	if d == nil {
		return ""
	}
	return fmt.Sprintf("%p %s %d %p %v %d %s %v %v %d %v %d %s %s %d %v %s", d, d.org, len(d.entries), d.open, d.warn, d.dirty, d.err, d.broken, d.showSkipped, len(d.prev), d.asbru != nil, len(d.asbru), d.rec, d.recErr, len(d.kdraft), d.wfBusy, d.wfErr) + fmt.Sprint(d.set.sub, d.set.open, d.set.q.Text())
}

type cfgSet struct {
	pages  map[string]*cfgDialog
	state  map[string]int    // orgEmpty, orgOK or orgBroken, by organization
	parent map[string]string // a nested organization's outer one
	nav    layout.List
	sub    map[string]string // each page's part shown (config_pages.go), by page; "" its first
	open   map[string]bool   // the organizations open in the nav
	q      widget.Editor     // the nav's search
}

// cfgEntry is one account (or search folder): its table as read, an editor
// per field (secret parts as "<secret>.<key>"), each secret's source, a
// vault's kind; old and base are whether it was read and how it was then.
type cfgEntry struct {
	kind   string
	raw    map[string]any
	eds    map[string]*widget.Editor
	src    map[string]int
	on     map[string]bool // toggles
	custom map[string]bool // a provider's model picked as Custom, by tier: how it is shown, not what is saved
	vkind  int
	old    bool
	base   string
}

func (e *cfgEntry) ed(key string) *widget.Editor {
	if e.eds[key] == nil {
		e.eds[key] = &widget.Editor{SingleLine: true}
	}
	return e.eds[key]
}

func (e *cfgEntry) text(key string) string { return strings.TrimSpace(e.ed(key).Text()) }

// snap is what the user can change of e, to tell an edited entry; editors
// made empty by drawing don't count.
func (e *cfgEntry) snap() string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(e.eds)) {
		if t := e.eds[k].Text(); t != "" {
			b.WriteString(k + "=" + t + "\x00")
		}
	}
	on := maps.Clone(e.on) // a switch turned on and off again is no change
	maps.DeleteFunc(on, func(_ string, v bool) bool { return !v })
	return fmt.Sprint(b.String(), e.src, on, e.vkind)
}

func (e *cfgEntry) edited() bool { return e.old && e.snap() != e.base }

// changes is how many entries of d were added, edited or removed.
func (d *cfgDialog) changes() int {
	if d.org == cfgKeys {
		return len(d.kdraft)
	}
	n := d.n0
	if d.langs != nil && d.langs.edited() {
		n++
	}
	if d.opts != nil && d.opts.edited() {
		n++
	}
	if d.models != nil && d.models.edited() {
		n++
	}
	if d.wf != nil && d.wf.edited() {
		n++
	}
	for _, x := range []*cfgEntry{d.me, d.phone, d.tgacct, d.diag, d.git, d.comp} {
		if x != nil && x.edited() {
			n++
		}
	}
	for _, e := range d.entries {
		switch {
		case !e.old:
			n++
		case e.edited():
			// counted by the n0 it stands for
		default:
			n--
		}
	}
	return n
}

// folderChanges is d's changes to the search folders alone: the rest of
// desk's pages don't change which organizations there are.
func (d *cfgDialog) folderChanges() int {
	n := d.changes()
	for _, x := range []*cfgEntry{d.langs, d.phone, d.tgacct, d.diag} {
		if x != nil && x.edited() {
			n--
		}
	}
	return n
}

// total is the unsaved changes of every page visited.
func (d *cfgDialog) total() (n int) {
	for _, p := range d.set.pages {
		n += p.changes()
	}
	return n
}

// keep adds e as read from the file.
func (d *cfgDialog) keep(e *cfgEntry) {
	e.old, e.base = true, e.snap()
	d.entries = append(d.entries, e)
	d.n0++
}

func kindOf(kind string) (int, cfgKind) {
	switch kind {
	case folderKind.key:
		return len(cfgKinds), folderKind
	case phoneKind.key:
		return len(cfgKinds) + 1, phoneKind
	case diagKind.key:
		return len(cfgKinds) + 2, diagKind
	case tgAcctKind.key:
		return len(cfgKinds) + 3, tgAcctKind
	}
	i := slices.IndexFunc(cfgKinds, func(c cfgKind) bool { return c.key == kind })
	if i < 0 {
		return -1, cfgKind{}
	}
	return i, cfgKinds[i]
}

func fields(kind string) []cfgField { _, k := kindOf(kind); return k.fields }

// sub is the table secret's keys live in.
func sub(raw map[string]any, secret string) map[string]any {
	if secret == "." {
		return raw
	}
	if m, ok := raw[secret].(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func newEntry(kind string, raw map[string]any) *cfgEntry {
	e := &cfgEntry{kind: kind, raw: raw, eds: map[string]*widget.Editor{}, src: map[string]int{}, on: map[string]bool{}}
	str := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }
	for _, f := range fields(kind) {
		switch {
		case f.toggle:
			e.on[f.key], _ = raw[f.key].(bool)
		case f.key == "kind":
			e.vkind = max(0, slices.Index(vaultKinds, str(raw, "kind"))) // "vaultwarden" and new vaults: bitwarden
		case f.secret != "":
			m := sub(raw, f.secret)
			for src, keys := range srcKeys {
				for _, k := range keys {
					if v := str(m, k); v != "" {
						e.ed(f.secret + "." + k).SetText(v)
						if e.src[f.secret] == srcNone {
							e.src[f.secret] = src
						}
					}
				}
			}
			e.ed(f.secret + ".token").Mask = '•'
		case f.num:
			if v, ok := raw[f.key]; ok {
				e.ed(f.key).SetText(fmt.Sprint(v))
			}
		case f.list:
			var parts []string
			if l, ok := raw[f.key].([]any); ok {
				for _, v := range l {
					parts = append(parts, fmt.Sprint(v))
				}
			}
			e.ed(f.key).SetText(strings.Join(parts, ", "))
		default:
			e.ed(f.key).SetText(str(raw, f.key))
		}
	}
	return e
}

// openConfig opens the settings on org: the view's organization, else the
// last used, else the first; none found, the search folders. Every page's
// edits are dropped (Discard comes here too).
func (u *ui) openConfig(org string) {
	u.stopLearning()
	u.orgList = orgs.List(u.root)
	if len(u.orgList) == 0 && org != cfgKeys {
		org = cfgFolders
	}
	if org != cfgFolders && org != cfgKeys && !slices.Contains(u.orgList, org) {
		org = u.orgList[0]
	}
	s := &cfgSet{pages: map[string]*cfgDialog{}, state: map[string]int{}, parent: map[string]string{}, nav: layout.List{Axis: layout.Vertical},
		sub: map[string]string{}, open: map[string]bool{org: true}, q: widget.Editor{SingleLine: true}}
	for _, o := range u.orgList {
		c, err := orgs.Load(u.root, o)
		accounts := len(c.Vaults) + len(c.GitHub) + len(c.Jira) + len(c.Linear) + len(c.Bitbucket) + len(c.GitLab) + len(c.Nagios) + len(c.Providers)
		switch {
		case err != nil:
			s.state[o] = orgBroken
		case accounts > 0:
			s.state[o] = orgOK
		}
		if a := orgs.Account(u.root, o); a != o && slices.Contains(u.orgList, a) {
			s.parent[o] = a
		}
	}
	u.cfg = &cfgDialog{set: s}
	u.cfgPage(org)
}

// cfgPage shows page o (an organization or cfgFolders), as it was left when
// visited before.
func (u *ui) cfgPage(o string) {
	s := u.cfg.set
	d := s.pages[o]
	if d == nil {
		d = &cfgDialog{org: o, set: s, list: layout.List{Axis: layout.Vertical}}
		switch o {
		case cfgFolders:
			u.loadFolders(d)
		case cfgKeys:
			d.kdraft = map[string]*keys.Chord{}
		default:
			u.loadOrg(d)
		}
		s.pages[o] = d
	}
	u.cfg = d
}

func (u *ui) loadOrg(d *cfgDialog) {
	d.file = orgs.File(u.root, d.org)
	raw, err := orgs.LoadRaw(u.root, d.org)
	if err != nil {
		d.err, d.broken, raw = firstLine(err.Error())+" — fix it by hand", true, map[string]any{}
	}
	d.raw = raw
	d.opts = &cfgEntry{kind: "opts", eds: map[string]*widget.Editor{}, src: map[string]int{}, on: map[string]bool{}}
	d.opts.on["keep_login"] = raw["clear_login"] == false
	d.opts.old, d.opts.base = true, d.opts.snap()
	d.models = loadModels(raw)
	d.wf = loadWorkflow(raw, u.boardColumns())
	d.me = loadMe(raw)
	d.git, d.comp = loadGit(raw)
	for _, k := range cfgKinds {
		tables, _ := raw[k.key].([]map[string]any)
		for _, t := range tables {
			d.keep(newEntry(k.key, t))
		}
	}
}

// closeConfig is esc and the close button: with unsaved changes the first
// one only says so.
func (u *ui) closeConfig() {
	if d := u.cfg; d.total() > 0 && !d.warn {
		d.warn = true
		return
	}
	u.dropConfig()
}

func (u *ui) dropConfig() {
	u.stopLearning()
	u.cfg, u.blur = nil, true
}

// vaultNames is the vaults the dialog has, for tokens to pick from.
func (d *cfgDialog) vaultNames() []string {
	var out []string
	for _, e := range d.entries {
		if n := e.text("name"); e.kind == "vault" && n != "" {
			out = append(out, n)
		}
	}
	return out
}

// saveConfig saves every page with changes, the one shown first, and
// closes; a page that can't be saved is shown with why. A broken page has
// none (it can't add accounts), so only the one shown needs the check.
func (u *ui) saveConfig() {
	d := u.cfg
	if d.broken {
		return
	}
	pages := []*cfgDialog{d}
	for _, o := range slices.Sorted(maps.Keys(d.set.pages)) {
		if p := d.set.pages[o]; p != d {
			pages = append(pages, p)
		}
	}
	var saved []string
	folders, deskSaved, keysSaved := false, false, false
	for _, p := range pages {
		if p.changes() == 0 {
			continue
		}
		var ok bool
		p.errAt = ""
		switch p.org {
		case cfgFolders:
			folders = folders || p.folderChanges() > 0 // only these need the organizations found again
			ok, deskSaved = u.saveFolders(p), true
		case cfgKeys:
			ok, keysSaved = u.saveKeys(p), true
		default:
			ok = u.saveOrg(p)
			saved = append(saved, p.org)
		}
		if !ok {
			if p.errAt != "" {
				p.set.sub[p.org] = p.errAt
			}
			u.cfg = p
			return
		}
	}
	u.dropConfig()
	if folders {
		u.reloadConfig()
		return
	}
	if len(saved) == 0 {
		switch {
		case deskSaved:
			u.showToast("Saved desk's settings", false)
		case keysSaved:
			u.showToast("Saved the keyboard shortcuts", false)
		}
		return
	}
	clear(u.locked) // vaults may have changed; the next check finds what is still locked
	u.showToast("Saved "+strings.Join(saved, ", ")+"'s settings", false)
	ids := u.linkedIDs()
	go func() { u.checkLinks(ids); u.checkTickets() }()
}

// saveOrg writes d back to its desk.toml, refusing what can't work: a vault
// without a name, two with one name, a token from a vault not there.
func (u *ui) saveOrg(d *cfgDialog) bool {
	names := d.vaultNames()
	for i, n := range names {
		if slices.Contains(names[:i], n) {
			d.err, d.errAt = "Two vaults are called "+n, "accounts"
			return false
		}
	}
	out := map[string]any{}
	for k, v := range d.raw {
		if !slices.ContainsFunc(cfgKinds, func(c cfgKind) bool { return c.key == k }) {
			out[k] = v
		}
	}
	delete(out, "clear_login") // the switch's: written only when it keeps the login output
	if d.opts.on["keep_login"] {
		out["clear_login"] = false
	}
	if !d.saveModels(out) || !u.saveWorkflow(d, out) {
		return false
	}
	d.saveMe(out)
	if !d.saveGit(out) {
		return false
	}
	for _, e := range d.entries {
		t := e.raw
		if k, _ := t["kind"].(string); e.kind == "vault" && k != "" && k != "vaultwarden" && !slices.Contains(vaultKinds, k) {
			l, _ := out[e.kind].([]map[string]any) // a kind the dialog doesn't edit (hashicorp, the infra Claude's): kept as it is
			out[e.kind] = append(l, t)
			continue
		}
		vk := vaultKinds[e.vkind]
		for _, f := range fields(e.kind) {
			if f.only != "" && f.only != vk {
				delete(t, f.key)
				continue
			}
			switch {
			case f.key == "kind":
				t["kind"] = vk
			case f.secret != "":
				m := sub(t, f.secret)
				for _, keys := range srcKeys {
					for _, k := range keys {
						delete(m, k)
					}
				}
				src := e.src[f.secret]
				for _, k := range srcKeys[src] {
					if v := e.text(f.secret + "." + k); v != "" {
						m[k] = v
					}
				}
				if v := e.text(f.secret + ".vault"); src == srcVault && !slices.Contains(names, v) {
					d.err, d.errAt = fmt.Sprintf("%s's %s: pick one of this organization's vaults", e.kind, strings.ToLower(f.label)), "accounts"
					return false
				}
				if f.secret != "." {
					if len(m) == 0 {
						delete(t, f.secret)
					} else {
						t[f.secret] = m
					}
				}
			case f.toggle:
				if e.on[f.key] {
					t[f.key] = true
				} else {
					delete(t, f.key)
				}
			case f.num:
				switch n, err := strconv.Atoi(e.text(f.key)); {
				case e.text(f.key) == "":
					delete(t, f.key)
				case err != nil:
					d.err, d.errAt = fmt.Sprintf("%s's %s is a whole number", cmp.Or(e.text("name"), e.kind), strings.ToLower(f.label)), partOfKind(e.kind)
					return false
				default:
					t[f.key] = n
				}
			case f.list:
				var l []string
				for _, p := range strings.Split(e.text(f.key), ",") {
					if p = strings.TrimSpace(p); p != "" {
						l = append(l, p)
					}
				}
				if len(l) == 0 {
					delete(t, f.key)
				} else {
					t[f.key] = l
				}
			default:
				if v := e.text(f.key); v == "" {
					delete(t, f.key)
				} else {
					t[f.key] = v
				}
			}
		}
		if (e.kind == "vault" || e.kind == "trigger") && e.text("name") == "" {
			d.err, d.errAt = "A "+e.kind+" needs a name", partOfKind(e.kind)
			return false
		}
		l, _ := out[e.kind].([]map[string]any)
		out[e.kind] = append(l, t)
	}
	if err := orgs.SaveRaw(u.root, d.org, out); err != nil {
		d.err = "Not saved: " + firstLine(err.Error())
		return false
	}
	return true
}

// configDialog is the settings: a large modal, the pages in a nav on its
// left, the page shown on the right over a bar with Discard and Save
// (Ctrl+S). Buttons record their action; it runs after drawing.
func (u *ui) configDialog(gtx layout.Context) {
	d := u.cfg
	if d == nil {
		return
	}
	if d.rec != "" { // the shortcuts page records: every key is its, Ctrl+S too
		u.keyRecord(gtx, d)
	}
	for d.rec == "" {
		ev, ok := gtx.Event(keys.Filters(nil, "settings.save")...)
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && keys.Is("settings.save", e) {
			d.act = u.saveConfig
		}
	}
	w := min(gtx.Metric.PxToDp(gtx.Constraints.Max.X-gtx.Dp(64)), 1200)
	u.k.Modal(gtx, d.set, 32, w, nil, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.Y = gtx.Constraints.Max.Y
		return layout.Flex{}.Layout(gtx, layout.Rigid(u.cfgNav), layout.Flexed(1, u.cfgMain))
	})
	if act := d.act; act != nil { // after drawing: these change or close what the dialog draws from
		d.act = nil
		act()
	}
	if c := u.cfg; c != nil {
		c.dirty = c.total()
	}
}

func fill(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Min} }

// orgDots are the nav's dot colors, by org state.
var orgDots = []color.NRGBA{orgEmpty: kit.CIdle, orgOK: kit.CGreen, orgBroken: kit.COrange}

// order is the organizations as the nav lists them: each nested one right
// after its outer one.
func (s *cfgSet) order(list []string) []string {
	var out []string
	for _, o := range list {
		if s.parent[o] == "" {
			out = append(out, o)
			for _, n := range list {
				if s.parent[n] == o {
					out = append(out, n)
				}
			}
		}
	}
	return out
}

// note is what page o's nav row says at its right: edited, else how its
// desk.toml reads, else the org it is in.
func (s *cfgSet) note(o string) (string, color.NRGBA) {
	switch p := s.pages[o]; {
	case p != nil && p.changes() > 0:
		return "edited", kit.CAccText
	case o == cfgFolders, o == cfgKeys:
		return "", kit.CText3
	case s.state[o] == orgBroken:
		return "can't read", kit.COrange
	case s.state[o] == orgEmpty:
		return "no accounts", kit.CText3
	case s.parent[o] != "":
		return "in " + s.parent[o], kit.CText3
	}
	return "", kit.CText3
}

// barText is the footer's message, its color, and the note after it.
func (u *ui) barText(d *cfgDialog) (msg string, tone color.NRGBA, note string) {
	file := d.file
	if d.org == cfgFolders || d.org == cfgKeys {
		file = orgs.SettingsFile()
	}
	switch n := d.total(); {
	case d.err != "":
		return d.err, kit.CRedText, ""
	case d.warn:
		return "Unsaved changes: Save or Discard them, or esc again to close without them", kit.COrange, ""
	case n == 1:
		msg, tone = "1 unsaved change", kit.CText
	case n > 1:
		msg, tone = fmt.Sprintf("%d unsaved changes", n), kit.CText
	default:
		msg, tone = "No changes", kit.CText3
	}
	return msg, tone, "· saves " + strings.Replace(file, u.home, "~", 1) + "; comments in it are not kept"
}

// cfgMain is the page shown: its header, its body scrolling, the bar.
func (u *ui) cfgMain(gtx layout.Context) layout.Dimensions {
	k, d := u.k, u.cfg
	gtx.Constraints.Min = gtx.Constraints.Max
	title, sub, body := u.cfgShown()
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 18, Bottom: 14, Left: 24, Right: 20}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{}.Layout(gtx,
					layout.Flexed(1, k.Title(title, sub)),
					layout.Rigid(kit.HGap(10)),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						dim, ok := k.IconBtn(gtx, "cfgClose", kit.IcoX, false)
						if ok {
							d.act = u.closeConfig
						}
						return dim
					}))
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = gtx.Constraints.Max
			return layout.Inset{Left: 24, Right: 20}.Layout(gtx, body)
		}),
		layout.Rigid(u.cfgBar))
}

// cfgBar is the sticky footer: how many changes aren't saved (or what went
// wrong), Discard, Save.
func (u *ui) cfgBar(gtx layout.Context) layout.Dimensions {
	k, d := u.k, u.cfg
	n := d.total()
	msg, tone, note := u.barText(d)
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Background{}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		sz := gtx.Constraints.Min
		if d.warn {
			kit.FillRRect(gtx.Ops, image.Rect(0, 0, sz.X-1, sz.Y-gtx.Dp(kit.RSheet)), 0, kit.COrangeSoft)
			kit.FillRRect(gtx.Ops, image.Rect(0, 0, sz.X-1, sz.Y-1), gtx.Dp(kit.RSheet)-1, kit.COrangeSoft)
			kit.FillRRect(gtx.Ops, image.Rect(0, 0, sz.X/2, sz.Y-1), 0, kit.COrangeSoft)
		}
		kit.FillRRect(gtx.Ops, image.Rect(0, 0, sz.X, gtx.Dp(1)), 0, kit.CLine)
		return layout.Dimensions{Size: sz}
	}, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 12, Bottom: 12, Left: 24, Right: 20}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			kids := []layout.FlexChild{}
			if n > 0 && d.err == "" {
				kids = append(kids, layout.Rigid(kit.Dot(kit.COrange)), layout.Rigid(kit.HGap(10)))
			}
			kids = append(kids, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, msg, 13, tone, kit.Sans, 2) }),
					layout.Rigid(kit.HGap(6)),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, note, 12, kit.CText3, kit.Sans, 1) }))
			}))
			if n > 0 {
				kids = append(kids, layout.Rigid(kit.HGap(10)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					dim, ok := k.KBtn(gtx, "cfgDiscard", kit.Ghost, "Discard", "")
					if ok {
						d.act = func() { // the files again, on the same part
							s := d.set
							u.openConfig(d.org)
							u.cfg.set.sub, u.cfg.set.open = s.sub, s.open
							u.cfg.set.q.SetText(s.q.Text())
						}
					}
					return dim
				}))
			}
			if !d.broken {
				kids = append(kids, layout.Rigid(kit.HGap(8)), layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					dim, ok := k.KBtn(gtx, "cfgSave", kit.Primary, "Save", keys.Cap("settings.save"))
					if ok {
						d.act = u.saveConfig
					}
					return dim
				}))
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
		})
	})
}

// cfgAccounts is an organization's page: a card per account, then the
// buttons that add one.
func (u *ui) cfgAccounts(gtx layout.Context) layout.Dimensions {
	k, d := u.k, u.cfg
	es := d.kinds(false)
	n := len(es)
	return d.list.Layout(gtx, n+1, func(gtx layout.Context, i int) layout.Dimensions {
		if i < n {
			return layout.Inset{Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return u.cfgEntry(gtx, es[i]) })
		}
		if d.broken {
			return layout.Inset{Top: 12, Bottom: 20}.Layout(gtx, k.Lbl("Fix the file by hand, then open the settings again.", 13, kit.CText3, kit.Sans))
		}
		var ws []layout.Widget
		for _, c := range cfgKinds {
			if c.key == "trigger" {
				continue // the Triggers page's
			}
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				dim, ok := k.RimButton(gtx, "cfgAdd"+c.key, kit.CPanelCard, kit.CHover, kit.CBtnRim, kit.RCtl, layout.Inset{Top: 6, Bottom: 6, Left: 10, Right: 10}, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(kit.Icon(12, kit.CText2, kit.IcoPlus)),
						layout.Rigid(kit.HGap(6)),
						layout.Rigid(k.Lbl(c.title, 12, kit.CText, kit.Semi(kit.Sans))))
				})
				if ok {
					d.act = func() {
						e := newEntry(c.key, map[string]any{})
						d.entries = append(d.entries, e)
						d.open = e
						d.list.ScrollTo(len(d.kinds(false)) - 1)
					}
				}
				return dim
			})
		}
		return layout.Inset{Top: 8, Bottom: 20}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			kids := []layout.FlexChild{}
			if n == 0 {
				kids = append(kids, layout.Rigid(k.Lbl("No accounts yet: add a vault, GitHub, Jira, Linear, Bitbucket, GitLab, Nagios or a model provider.", 13, kit.CText3, kit.Sans)), layout.Rigid(kit.VGap(12)))
			}
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return kit.Wrap(gtx, 6, ws) }))
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
		})
	})
}

// summary is an account in a line: where it points, what it covers, where
// its token comes from.
func (e *cfgEntry) summary() string {
	var parts []string
	vk := vaultKinds[e.vkind]
	for _, f := range fields(e.kind) {
		if f.only != "" && f.only != vk {
			continue
		}
		switch v := e.text(f.key); {
		case f.secret == "." || f.secret == "password": // the main one: an API key's secret is detail
			parts = append(parts, e.source(f))
		case f.secret != "":
		case f.toggle:
			if e.on[f.key] && (f.key == "off" || f.key == "worklog" || f.key == "start") {
				parts = append(parts, strings.ToLower(f.label))
			}
		case v == "" || !slices.Contains([]string{"host", "url", "base_url", "email", "workspace", "user", "owners", "projects", "teams", "groups", "on", "at", "match", "days"}, f.key):
		case f.list:
			parts = append(parts, strings.ToLower(f.label)+" "+v)
		default:
			parts = append(parts, strings.TrimPrefix(strings.TrimPrefix(v, "https://"), "http://"))
		}
	}
	return strings.Join(parts, " · ")
}

// source says where secret f comes from.
func (e *cfgEntry) source(f cfgField) string {
	what := strings.ToLower(f.label)
	switch e.src[f.secret] {
	case srcVault:
		return what + " from " + cmp.Or(e.text(f.secret+".vault"), "a vault")
	case srcEnv:
		return what + " from $" + e.text(f.secret+".token_env")
	case srcCmd:
		return what + " from a command"
	case srcFile:
		return what + " in desk.toml"
	}
	if f.secret == "password" {
		return "asks for its " + what
	}
	return "no " + what
}

// cfgEntry draws one account as a card: its tile, name and summary, and,
// open (one at a time), its fields two by two and Remove.
func (u *ui) cfgEntry(gtx layout.Context, e *cfgEntry) layout.Dimensions {
	k, d := u.k, u.cfg
	id := fmt.Sprintf("cfg%p", e)
	open := d.open == e
	_, kind := kindOf(e.kind)
	abbr := cfgAbbr[e.kind]
	if e.kind == "vault" {
		abbr = cfgAbbr[vaultKinds[e.vkind]]
	}
	status, tone := e.status()
	header := func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		dim, ok := k.Button(gtx, id+"hd", color.NRGBA{}, kit.CHover, kit.RCard, layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			kids := []layout.FlexChild{
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					sz := gtx.Dp(28)
					kit.FillRRect(gtx.Ops, image.Rect(0, 0, sz, sz), gtx.Dp(kit.RCard), kit.CPanelCard)
					gtx.Constraints = layout.Exact(image.Pt(sz, sz))
					return layout.Center.Layout(gtx, k.Lbl(abbr, 11, kit.CText2, kit.Bold(kit.Sans)))
				}),
				layout.Rigid(kit.HGap(10)),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					name := []layout.FlexChild{layout.Rigid(k.Lbl(kind.title, 13, kit.CText, kit.Semi(kit.Sans)))}
					if n := e.text("name"); n != "" {
						name = []layout.FlexChild{layout.Rigid(k.Lbl(n, 13, kit.CText, kit.Semi(kit.Sans))), layout.Rigid(k.Lbl(" · "+kind.title, 13, kit.CText3, kit.Sans))}
					}
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions { return layout.Flex{}.Layout(gtx, name...) }),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return k.Label(gtx, e.summary(), 12, kit.CText3, kit.Sans, 1)
						}))
				}),
			}
			if status != "" {
				kids = append(kids, layout.Rigid(kit.HGap(10)), layout.Rigid(kit.Dot(tone)), layout.Rigid(kit.HGap(6)), layout.Rigid(k.Lbl(status, 12, tone, kit.Sans)))
			}
			more := "Edit"
			if open {
				more = "Collapse"
			}
			kids = append(kids, layout.Rigid(kit.HGap(14)), layout.Rigid(k.Lbl(more, 12, kit.CText2, kit.Semi(kit.Sans))))
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
		})
		if ok {
			d.act = func() {
				if d.open = e; open {
					d.open = nil
				}
			}
		}
		return dim
	}
	kids := []layout.FlexChild{layout.Rigid(header)}
	if open {
		kids = append(kids,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				kit.FillRRect(gtx.Ops, image.Rect(0, 0, gtx.Constraints.Max.X, gtx.Dp(1)), 0, kit.CLine)
				return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, gtx.Dp(1))}
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 12, Bottom: 12, Left: 12, Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return u.cfgFields(gtx, id, e) })
			}))
	}
	rim := kit.CLine
	if open {
		rim = kit.CRim
	}
	return kit.Boxed(kit.CCard, rim, kit.RCard, layout.Inset{}, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})(gtx)
}

// formRows is e's fields as the open card lays them out: text fields two
// by two, a choice (the vault's kind) or a secret alone across the row.
func (e *cfgEntry) formRows() [][]cfgField {
	var rows [][]cfgField
	vk := vaultKinds[e.vkind]
	pair := false // the last row has one text field, room for another
	for _, f := range fields(e.kind) {
		wide := f.key == "kind" || f.secret != "" || f.tier || f.choices != nil
		switch {
		case f.only != "" && f.only != vk:
		case pair && !wide:
			rows[len(rows)-1] = append(rows[len(rows)-1], f)
			pair = false
		default:
			rows = append(rows, []cfgField{f})
			pair = !wide
		}
	}
	return rows
}

// status is what an account's card says at its right: New, Edited or "".
func (e *cfgEntry) status() (string, color.NRGBA) {
	switch {
	case !e.old:
		return "New", kit.CAccText
	case e.edited():
		return "Edited", kit.COrange
	}
	return "", kit.CText3
}

// cfgFields is an open account's fields in two columns (a choice and a
// token's source across both), then Remove.
func (u *ui) cfgFields(gtx layout.Context, id string, e *cfgEntry) layout.Dimensions {
	k, d := u.k, u.cfg
	labeled := func(label string, w layout.Widget) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, layout.Rigid(k.Caption(label)), layout.Rigid(kit.VGap(5)), layout.Rigid(w))
		}
	}
	cell := func(f cfgField) layout.Widget {
		switch {
		case f.key == "kind":
			return labeled("Kind", func(gtx layout.Context) layout.Dimensions {
				return u.choice(gtx, id+"kind", []string{"Bitwarden / Vaultwarden", "Passbolt"}, e.vkind, func(int) color.NRGBA { return color.NRGBA{} }, func(i int) { e.vkind = i })
			})
		case f.secret != "":
			return labeled(f.label, func(gtx layout.Context) layout.Dimensions { return u.cfgSecret(gtx, id, e, f) })
		case f.tier:
			return labeled(f.label, func(gtx layout.Context) layout.Dimensions { return u.cfgTier(gtx, id, e, f) })
		case f.toggle:
			return func(gtx layout.Context) layout.Dimensions { return u.cfgToggle(gtx, id+f.key, e, f.key, f.label, f.hint) }
		case f.choices != nil:
			return labeled(f.label, func(gtx layout.Context) layout.Dimensions {
				return u.choice(gtx, id+f.key, f.choices, slices.Index(f.choices, e.text(f.key)), func(int) color.NRGBA { return color.NRGBA{} }, func(i int) { e.ed(f.key).SetText(f.choices[i]) })
			})
		}
		return labeled(f.label, k.Field(e.ed(f.key), f.hint, 0))
	}
	var rows []layout.FlexChild
	for _, r := range e.formRows() {
		w := cell(r[0])
		if len(r) == 2 {
			a, b := w, cell(r[1])
			w = func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{}.Layout(gtx, layout.Flexed(1, a), layout.Rigid(kit.HGap(14)), layout.Flexed(1, b))
			}
		}
		rows = append(rows, layout.Rigid(w), layout.Rigid(kit.VGap(12)))
	}
	if deskOwn(e.kind) { // nothing to remove
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
	}
	rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return layout.E.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = image.Point{}
			dim, ok := k.Button(gtx, id+"rm", color.NRGBA{}, kit.CRedSoft, kit.RCtl, layout.Inset{Top: 5, Bottom: 5, Left: 8, Right: 8}, k.Lbl("Remove", 12, kit.CRedText, kit.Semi(kit.Sans)))
			if ok {
				d.act = func() { d.entries = slices.DeleteFunc(d.entries, func(x *cfgEntry) bool { return x == e }) }
			}
			return dim
		})
	}))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
}

// deskOwn is whether kind is one of settings.toml's entries, desk's own
// rather than an organization's.
func deskOwn(kind string) bool {
	return kind == phoneKind.key || kind == tgAcctKind.key || kind == diagKind.key
}

// cfgSecret is where a token comes from: a source to pick, then its fields.
// A vault's own secrets can't come from a vault.
func (u *ui) cfgSecret(gtx layout.Context, id string, e *cfgEntry, f cfgField) layout.Dimensions {
	k, d := u.k, u.cfg
	names := []string{srcNone: "None", srcVault: "Vault item", srcEnv: "Environment variable", srcCmd: "Command", srcFile: "In desk.toml"}
	if f.secret == "password" {
		names[srcNone] = "Ask when needed"
	}
	if deskOwn(e.kind) {
		names[srcFile] = "In settings.toml"
	}
	src := e.src[f.secret]
	opts := []int{srcNone, srcVault, srcEnv, srcCmd, srcFile}
	if f.secret != "." || e.kind == phoneKind.key { // a vault belongs to an organization; desk's own settings have none
		opts = slices.Delete(opts, 1, 2)
	}
	var labels []string
	cur := 0
	for i, o := range opts {
		labels = append(labels, names[o])
		if o == src {
			cur = i
		}
	}
	key := func(k string) *widget.Editor { return e.ed(f.secret + "." + k) }
	row := func(a, b layout.Widget) layout.Widget { // two fields side by side
		return func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx, layout.Flexed(1, a), layout.Rigid(kit.HGap(8)), layout.Flexed(1, b))
		}
	}
	var body layout.Widget
	switch src {
	case srcVault:
		body = func(gtx layout.Context) layout.Dimensions {
			vaults := d.vaultNames()
			var ws []layout.Widget
			for _, n := range vaults {
				fg, bg := kit.CText2, kit.CPane
				if key("vault").Text() == n {
					fg, bg = kit.CAccText, kit.CAccSoft
				}
				ws = append(ws, func(gtx layout.Context) layout.Dimensions {
					dim, ok := k.Button(gtx, id+f.secret+"v"+n, bg, kit.CHover, kit.RCtl, layout.Inset{Top: 3, Bottom: 3, Left: 8, Right: 8}, k.Lbl(n, 12, fg, kit.Medium(kit.Sans)))
					if ok {
						key("vault").SetText(n)
					}
					return dim
				})
			}
			pick := func(gtx layout.Context) layout.Dimensions { return kit.Wrap(gtx, 4, ws) }
			if len(ws) == 0 {
				pick = k.Lbl("Add a vault to this organization first", 11, kit.COrange, kit.Sans)
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(pick),
				layout.Rigid(kit.VGap(6)),
				layout.Rigid(row(k.Field(key("item"), "the item's name (or id)", 0), k.Field(key("field"), "field: password, username, notes, totp, a custom one", 0))))
		}
	case srcEnv:
		body = k.Field(key("token_env"), "VARIABLE_NAME", 0)
	case srcCmd:
		body = k.Field(key("token_cmd"), "a command that prints it", 0)
	case srcFile:
		body = k.Field(key("token"), "the value, stored in plain text (0600)", 0)
	}
	kids := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return u.choice(gtx, id+f.secret+"src", labels, cur, func(int) color.NRGBA { return color.NRGBA{} }, func(i int) { e.src[f.secret] = opts[i] })
	})}
	if body != nil {
		kids = append(kids, layout.Rigid(kit.VGap(8)), layout.Rigid(body))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

// configOrg is the organization the settings open on.
func (u *ui) configOrg() string {
	if o, ok := strings.CutPrefix(u.view, "org:"); ok {
		return o
	}
	if c := u.b.Card(u.sel); c != nil && c.Org != "" {
		return c.Org
	}
	return u.lastOrg
}
