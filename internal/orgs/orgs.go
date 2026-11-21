// Package orgs knows the organizations: the folders of ~/Work, and each
// one's desk.toml with the GitHub, Jira, Bitbucket and GitLab accounts its
// cards' links go through (any number of each).
//
//	# ~/Work/Globex/.desk/desk.toml (a folder one level down that says org = "Name"
//	# in its own desk.toml is an organization apart, under Globex's account;
//	# relative paths in it are from ~/Work/Globex)
//	[[vault]]
//	name = "globex"
//	kind = "passbolt"                     # or "bitwarden", for Bitwarden and Vaultwarden
//	url = "https://passbolt.globex.com"
//	key_file = "~/Secure/passbolt-globex.asc"
//
//	[[github]]
//	name = "globex"
//	owners = ["globex-org"]               # repos it answers for ("" all)
//	vault = "globex"                      # the token is that vault's item
//	item = "GitHub token"                 # or token_env / token_cmd; without a token, desk asks GitHub nothing
//
//	[[jira]]
//	name = "globex"
//	url = "https://globex.atlassian.net"
//	email = "me@globex.com"
//	projects = ["ABC"]                    # bare keys like ABC-12 link here
//	token_env = "JIRA_GLOBEX_TOKEN"
//
//	[[linear]]
//	name = "globex"
//	workspace = "globex"                  # linear.app/globex
//	teams = ["ENG"]                       # bare keys like ENG-12 link here; cards create tickets in the first
//	token_env = "LINEAR_API_KEY"          # a personal API key
//
//	[[bitbucket]]
//	workspace = "globex"
//	user = "me"                           # with an app password; without, token is a bearer token
//	token_cmd = "pass bitbucket/globex"
//
//	[[gitlab]]
//	url = "https://gitlab.com"
//	groups = ["globex"]
//	token_env = "GITLAB_TOKEN"
//
//	[[nagios]]                            # its warnings and errors: the Alerts view
//	name = "prod"
//	url = "https://mon.globex.com/nagios"
//	user = "me"
//	vault = "globex"
//	item = "Nagios"
package orgs

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

// Secret is where a token comes from: an item of one of the org's vaults, a
// variable, a command that prints it, or, last, the file itself.
type Secret struct {
	Vault    string `toml:"vault"` // a [[vault]]'s name
	Item     string `toml:"item"`  // the item's name or id there
	Field    string `toml:"field"` // password (default), username, notes, or a custom field's name
	Token    string `toml:"token"`
	TokenEnv string `toml:"token_env"`
	TokenCmd string `toml:"token_cmd"`
}

// Vault is a password manager the org's secrets come from. One unlocked
// for an org is unlocked for every org that names the same account.
type Vault struct {
	Name         string `toml:"name"`
	Kind         string `toml:"kind"`      // "bitwarden" (also Vaultwarden) or "passbolt"
	URL          string `toml:"url"`       // bitwarden: default https://vault.bitwarden.com
	Email        string `toml:"email"`     // bitwarden: the account
	ClientID     string `toml:"client_id"` // bitwarden: the account's API key, which skips 2FA and new-device mails
	ClientSecret Secret `toml:"client_secret"`
	KeyFile      string `toml:"key_file"`    // passbolt: your armored private key
	ConfigFile   string `toml:"config_file"` // passbolt: a go-passbolt-cli config (server, key, passphrase, TOTP); fields above win
	Password     Secret `toml:"password"`    // master password or key passphrase; none set, desk asks
	// kind = "hashicorp" (the infra Claude's vaults only, internal/infra): KV v2 over HTTP
	Mount    string `toml:"mount"`        // hashicorp: the KV v2 mount (default "secret")
	TokenEnv string `toml:"token_env"`    // hashicorp: the variable with the token (default VAULT_TOKEN)
	Insecure bool   `toml:"insecure_tls"` // hashicorp: skip TLS verification
}

// Get reads the token, when it is not in a vault (see links.secret); ""
// without an error means none is configured.
func (s Secret) Get() (string, error) {
	switch {
	case s.TokenEnv != "":
		if v := os.Getenv(s.TokenEnv); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("$%s is not set", s.TokenEnv)
	case s.TokenCmd != "":
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, Shell[0], Shell[1], s.TokenCmd).Output()
		if err != nil {
			return "", fmt.Errorf("token_cmd: %w", err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	return s.Token, nil
}

type GitHub struct {
	Name   string   `toml:"name"`
	Host   string   `toml:"host"` // default github.com
	Owners []string `toml:"owners"`
	Sync   *bool    `toml:"sync"` // false: cards' column moves don't close or reopen its issues
	Repo   string   `toml:"repo"` // owner/name: where cards create their issues, "" not here
	Secret
}

type Jira struct {
	Name     string   `toml:"name"`
	URL      string   `toml:"url"`
	Email    string   `toml:"email"`
	Projects []string `toml:"projects"`
	Sync     *bool    `toml:"sync"` // false: cards' column moves don't transition its issues
	// Status names the workflow status a column moves issues to ("Done" =
	// "In Review" where the owner closes them), over the category guess.
	Status map[string]string `toml:"status"`
	// Ignore is the statuses sync leaves an issue in, whatever its card's
	// column: others move it on from there ("Testing", "Done").
	Ignore []string `toml:"ignore"`
	// IssueType is what a card's new ticket is created as, in the first of
	// Projects; "" Task (or Tarea), else the project's first non-subtask.
	IssueType string `toml:"issue_type"`
	Secret
}

// Linear is a linear.app workspace, read and written with a personal API key.
type Linear struct {
	Name      string   `toml:"name"`
	Workspace string   `toml:"workspace"` // its URL key (linear.app/<workspace>): where bare keys link
	Teams     []string `toml:"teams"`     // team keys: bare keys of theirs link here, cards create tickets in the first
	Sync      *bool    `toml:"sync"`      // false: cards' column moves don't move its issues
	// Status names the workflow state a column moves issues to, over the
	// state type guess; Ignore the states sync leaves an issue in. As Jira's.
	Status map[string]string `toml:"status"`
	Ignore []string          `toml:"ignore"`
	Secret
}

// Nagios is a monitor whose warnings and errors the org's Alerts view
// shows: its web root (https://mon.example.com/nagios) and the login of its
// CGIs (basic auth), the password the secret.
type Nagios struct {
	Name     string `toml:"name"`
	URL      string `toml:"url"`
	User     string `toml:"user"`
	Alerts   bool   `toml:"alerts"`       // infra: polled every minute, new problems notify and offer an investigation
	Insecure bool   `toml:"insecure_tls"` // infra: skip TLS verification
	Secret
}

// Metrics is a Prometheus or VictoriaMetrics the infra Claude queries
// (PromQL over the HTTP API): basic auth with a user, else the secret is a
// bearer token.
type Metrics struct {
	Name     string `toml:"name"`
	Type     string `toml:"type"` // "prometheus" or "victoriametrics"
	URL      string `toml:"url"`
	User     string `toml:"user"`
	Insecure bool   `toml:"insecure_tls"`
	Secret
}

// Approvals are the infra Claude's command rules: allow never asks (single
// commands only), deny always asks, and beats allow. `*` matches any text.
type Approvals struct {
	Allow []string `toml:"allow"`
	Deny  []string `toml:"deny"`
}

// Schedule is the infra Claude's daily work: Health "08:30" runs a read-only
// health check then.
type Schedule struct {
	Health string `toml:"health"`
}

// Servers is where the infra workspace's servers are: File, an OpenSSH config
// relative to the organization's folder (default .desk/ssh_config). Without that
// file the organization has no servers.
type Servers struct {
	File string `toml:"file"`
}

type Bitbucket struct {
	Name      string `toml:"name"`
	Workspace string `toml:"workspace"`
	User      string `toml:"user"`
	Secret
}

type GitLab struct {
	Name   string   `toml:"name"`
	URL    string   `toml:"url"` // default https://gitlab.com
	Groups []string `toml:"groups"`
	Secret
}

// Company is how desk's roles (internal/company) join the organization's
// own agents: Roles maps a role (tl, architect…) to one of its agents
// doing that job, so desk leaves its own out, as it does Skip's; Off
// leaves them all out.
// Decides maps a kind of decision to who takes it (a role, an agent of
// the organization's, or "user"), and the budgets are in US dollars:
// CardBudget a card's session between two of the user's messages,
// DayBudget all the organization's sessions in a day.
type Company struct {
	Off        bool              `toml:"off"`
	Roles      map[string]string `toml:"roles"`
	Skip       []string          `toml:"skip"`
	Decides    map[string]string `toml:"decides"`
	CardBudget float64           `toml:"card_budget"`
	DayBudget  float64           `toml:"day_budget"`
	// Models is the model each role runs on, by role (tl, qa…, as Roles):
	// opus, sonnet, haiku or inherit (the session's); unset, the role's own.
	Models map[string]string `toml:"models"`
}

// OrgMe is who the user is in one organization (their role there, what
// they care about), told to its Claudes with the global Who am I, or in
// its place when Extend is false (unset: true).
type OrgMe struct {
	About  string `toml:"about"`
	Extend *bool  `toml:"extend"`
}

// Provider is an Anthropic-compatible endpoint desk Claudes can run on in
// place of the Claude subscription (OpenRouter, a proxy in front of an
// OpenAI-style API): its token, and the models it answers for Opus, Sonnet
// and Haiku ("" keeps Claude's own name).
type Provider struct {
	Name    string `toml:"name"`
	BaseURL string `toml:"base_url"`
	Secret
	Opus   string `toml:"opus"`
	Sonnet string `toml:"sonnet"`
	Haiku  string `toml:"haiku"`
}

// Tasks are the desk Claudes [models] picks a provider for, in the
// settings' order.
var Tasks = []string{"card", "improve", "explain", "workspace", "lead", "drawing", "infra", "workflow"}

// Trigger is a [[trigger]]: what desk does by itself when something
// happens in the organization (ui's triggers.go).
type Trigger struct {
	Name string `toml:"name"` // what it is called; what it has handled is kept by it
	// On is what it watches: "ticket" (a new ticket assigned to the user
	// makes its card), "ci_failed" (a card's pull request or commit fails
	// its CI: its Claude is told), "alert" (a monitoring alert makes an
	// investigation card), "schedule" (a card at a time of day).
	On       string   `toml:"on"`
	Project  string   `toml:"project"`  // ticket: only this project (a Jira project, a Linear team, a repository)
	Match    string   `toml:"match"`    // ticket: in its title; alert: in its host or service
	Warnings bool     `toml:"warnings"` // alert: warnings too, not only errors
	After    int      `toml:"after"`    // alert: minutes it has lasted first
	At       string   `toml:"at"`       // schedule: "09:00", local time
	Days     []string `toml:"days"`     // schedule: "mon"…"sun"; none: every day
	Title    string   `toml:"title"`    // schedule: the card's (ticket: its key and title)
	Notes    string   `toml:"notes"`    // schedule: what the card is to do
	Column   string   `toml:"column"`   // where its cards go (default To do; an alert's In progress)
	Start    bool     `toml:"start"`    // its card's Claude starts at once (an alert's always does)
	Off      bool     `toml:"off"`
}

// Git is how the organization's cards use their git repositories.
type Git struct {
	// Worktrees gives each new card whose folder is in a git repository its
	// own worktree and branch (<repo>/.worktrees/desk-n) to work in.
	Worktrees bool `toml:"worktrees"`
}

// Workflow is how the organization's tickets move, as its people use
// them, which may differ from what desk's columns would guess: written by
// the user (Instructions) and learned from them by Haiku (the rest), then
// checked in the settings.
type Workflow struct {
	Instructions string `toml:"instructions"` // the user's: how tickets move here, or where to read it
	Summary      string `toml:"summary"`      // what was understood, for the user
	Rules        string `toml:"rules"`        // told to every card's Claude
	Learned      string `toml:"learned"`      // the day it was learned
	// Status is the status a ticket goes to when its card moves to a
	// column, by column title, over the account's guess.
	Status map[string]string `toml:"status"`
	// Follow is the column a card goes to when someone else puts its
	// ticket in a status, by status name.
	Follow map[string]string `toml:"follow"`
}

// On says the organization has a workflow: tickets then lead their cards
// in the background check instead of being moved back, since its people
// (and the card's Claude, by its rules) move them on purpose.
func (w Workflow) On() bool { return w.Rules != "" || len(w.Status)+len(w.Follow) > 0 }

// ColumnOf is the column a ticket in status puts its card, in column cur,
// in: Follow's; else, of the columns Status sends to it (several can
// share one status), cur when it is one, else the first in the board's
// order cols; "" none.
func (w Workflow) ColumnOf(status, cur string, cols []string) string {
	for s, col := range w.Follow {
		if strings.EqualFold(s, status) {
			return col
		}
	}
	to := func(col string) bool { return strings.EqualFold(w.Status[col], status) }
	if to(cur) {
		return cur
	}
	if i := slices.IndexFunc(cols, to); i >= 0 {
		return cols[i]
	}
	return ""
}

// ProviderFor is the provider task runs on: nil on the Claude subscription;
// an error when [models] names one desk.toml doesn't have.
func (c Config) ProviderFor(task string) (*Provider, error) {
	name := c.Models[task]
	if name == "" {
		return nil, nil
	}
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			return &c.Providers[i], nil
		}
	}
	return nil, fmt.Errorf("[models] %s = %q: no [[provider]] of that name", task, name)
}

type Config struct {
	Agent     string      `toml:"agent"`  // the Claude Code agent new cards run as, "" none
	Agents    string      `toml:"agents"` // its folder of agent files, from the organization's ("" .claude/agents)
	Company   Company     `toml:"company"`
	GitHub    []GitHub    `toml:"github"`
	Jira      []Jira      `toml:"jira"`
	Linear    []Linear    `toml:"linear"`
	Bitbucket []Bitbucket `toml:"bitbucket"`
	GitLab    []GitLab    `toml:"gitlab"`
	Nagios    []Nagios    `toml:"nagios"`
	Vaults    []Vault     `toml:"vault"`
	Providers []Provider  `toml:"provider"`
	Workflow  Workflow    `toml:"workflow"`
	Git       Git         `toml:"git"`
	Triggers  []Trigger   `toml:"trigger"`
	Me        OrgMe       `toml:"me"`
	// Models maps a desk Claude (Tasks) to the provider it runs on, by name;
	// unset, the Claude subscription.
	Models map[string]string `toml:"models"`
	// The infra workspace's (internal/infra).
	Servers Servers `toml:"servers"`
	Record  *bool   `toml:"record"` // false: don't record its terminals as asciicasts (on by default)
	// ClearLogin false keeps what a pane printed before its first prompt
	// (the login banner, the motd); on by default.
	ClearLogin *bool     `toml:"clear_login"`
	Approvals  Approvals `toml:"approvals"`
	Schedule   Schedule  `toml:"schedule"`
	Metrics    []Metrics `toml:"metrics"`
}

// Root is the folder of organizations: $DESK_ROOT, else ~/Work.
func Root() string {
	if r := os.Getenv("DESK_ROOT"); r != "" {
		return r
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Work")
}

// Folder is where organizations are searched: its subfolders are
// organizations, and further down, to Depth levels, the folders with a
// desk.toml; with Root, the folder itself when it has one.
type Folder struct {
	Path     string `toml:"path"`
	Root     bool   `toml:"root"`
	Depth    int    `toml:"depth"`
	OnlyToml bool   `toml:"only_toml"` // its subfolders too need a desk.toml to be organizations
}

// Found is an organization a search folder gives: its name, its folder, and
// the index of the search folder that found it.
type Found struct {
	Name, Dir string
	Folder    int
}

// SettingsFile is desk's own settings: the folders to search.
func SettingsFile() string {
	d, _ := XDGDir("XDG_CONFIG_HOME", ".config")
	return filepath.Join(d, "desk", "settings.toml")
}

// XDGDir is $env, else ~/home (".config", ".cache"), as os.UserConfigDir
// and UserCacheDir are on Linux: desk keeps the XDG layout on every OS,
// where those would be ~/Library's on macOS. No $HOME is an error.
func XDGDir(env, home string) (string, error) {
	if d := os.Getenv(env); d != "" {
		return d, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, home), nil
}

// Folders is where root's organizations are searched: for desk's root
// (Root()) the settings' folders, if any; else root, two levels down.
func Folders(root string) []Folder {
	def := []Folder{{Path: root, Depth: 2}}
	if root != Root() {
		return def
	}
	if s := loadSettings(); len(s.Folder) > 0 {
		return s.Folder
	}
	return def
}

// settings is settings.toml: the search folders and the languages.
type settings struct {
	Folder   []Folder          `toml:"folder"`
	CardLang string            `toml:"card_lang,omitempty"` // what cards (and Improve, and tickets) are written in
	ChatLang string            `toml:"chat_lang,omitempty"` // what Claude speaks with the user in
	Zoom     int               `toml:"zoom,omitempty"`      // the terminals' and session panel's text, in percent (zoom.go)
	Editor   Editor            `toml:"editor,omitempty"`    // the workspace's panes and editor (editor.go)
	Keys     map[string]string `toml:"keys,omitempty"`      // shortcuts the user rebound, by internal/keys ID
	Me       string            `toml:"me,omitempty"`        // who the user is, told to every Claude desk starts (AboutMe)
	NewRelic NewRelic          `toml:"newrelic,omitempty"`  // where desk reports its own crashes and performance (internal/telemetry)
	Phone    Phone             `toml:"phone,omitempty"`     // a Telegram bot that brings cards' questions to the user's phone (ui's phone.go)
}

// Phone is settings.toml's [phone]: the Telegram bot desk talks to the
// user's phone through, by its token (token, token_env or token_cmd; no
// vault: these settings belong to no organization), and the chat it's
// paired with (0: the first /start pairs it).
type Phone struct {
	Secret
	Chat   int64 `toml:"chat_id,omitempty"`
	Always bool  `toml:"always,omitempty"` // also while a desk window has the focus
}

// PhoneSettings is settings.toml's [phone].
func PhoneSettings() Phone { return loadSettings().Phone }

// SetPhoneChat pairs the phone's chat, the rest as it is.
func SetPhoneChat(chat int64) error {
	s := loadSettings()
	s.Phone.Chat = chat
	return saveSettings(s)
}

// NewRelic is settings.toml's [newrelic]: desk's own crashes, errors and
// performance go to that account. No license key, nothing is sent.
type NewRelic struct {
	LicenseKey string `toml:"license_key,omitempty"`
	AppName    string `toml:"app_name,omitempty"` // "desk" when empty
}

// Telemetry is settings.toml's [newrelic].
func Telemetry() NewRelic { return loadSettings().NewRelic }

// Me is who the user is, in their own words: the settings' Who am I.
func Me() string { return loadSettings().Me }

// SaveMe keeps who the user is, the rest as it is.
func SaveMe(me string) error {
	s := loadSettings()
	s.Me = me
	return saveSettings(s)
}

// AboutMe is what org's Claudes are told about the user: the settings' Who
// am I, then the organization's own ([me] about), or the organization's
// alone when it doesn't extend the global one (extend = false). A
// desk.toml that doesn't read leaves the global one.
func AboutMe(root, org string) string {
	global := Me()
	c, err := Load(root, org)
	if err != nil {
		return global
	}
	if c.Me.Extend != nil && !*c.Me.Extend {
		return c.Me.About
	}
	return strings.TrimSpace(global + "\n\n" + c.Me.About)
}

func loadSettings() (s settings) {
	toml.DecodeFile(SettingsFile(), &s) // missing or unreadable: the defaults
	return s
}

// Langs is what cards are written in and what Claude speaks in, English
// unless the settings say otherwise.
func Langs() (card, chat string) {
	s := loadSettings()
	return cmp.Or(s.CardLang, "English"), cmp.Or(s.ChatLang, "English")
}

// SaveLangs keeps the languages, the folders as they are.
func SaveLangs(card, chat string) error {
	s := loadSettings()
	s.CardLang, s.ChatLang = card, chat
	return saveSettings(s)
}

// Keys are the shortcuts the user rebound (internal/keys IDs to chords).
func Keys() map[string]string { return loadSettings().Keys }

// SaveKeys writes m as the rebound shortcuts, the rest as it is. A file that
// doesn't parse isn't rewritten: it would lose its folders.
func SaveKeys(m map[string]string) error {
	var s settings
	if _, err := toml.DecodeFile(SettingsFile(), &s); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	s.Keys = m
	return saveSettings(s)
}

// SaveFolders writes fs as the folders to search, the languages as they are.
func SaveFolders(fs []Folder) error {
	s := loadSettings()
	s.Folder = fs
	return saveSettings(s)
}

// saveSettings writes s through a temp file and a rename.
func saveSettings(s settings) error {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(s); err != nil {
		return err
	}
	path := SettingsFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", b.Bytes(), 0o644); err != nil {
		return err
	}
	err := os.Rename(path+".tmp", path)
	foundMu.Lock()
	clear(accounts) // after the rename: an Account meanwhile read the old ones
	foundMu.Unlock()
	return err
}

// Expand is p with a leading ~ as the home folder.
func Expand(p string) string {
	if rest, ok := strings.CutPrefix(p, "~"); ok && (rest == "" || rest[0] == '/') {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, rest) // Windows' separators all the way
	}
	return p
}

// found is the organizations List found, by root, then name: their folder;
// accounts what Account said, by root and org.
var (
	foundMu  sync.Mutex
	found    = map[string]map[string]string{}
	accounts = map[string]string{}
)

func hidden(name string) bool { return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") }

// DeskDir is the folder inside an organization's where desk keeps its files
// (desk.toml, the servers file, the infra dir, caches).
const DeskDir = ".desk"

// Moved is where desk's file name of the organization folder dir is: dir/.desk/name. One
// still at dir/name, where desk kept it before, moves there first, once;
// with both, the new one is used and the old one left alone. When the move
// fails (a read-only folder), the old one is still read.
func Moved(dir, name string) string {
	p, old := filepath.Join(dir, DeskDir, name), filepath.Join(dir, name)
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		return p
	}
	if _, err := os.Lstat(old); err != nil {
		return p
	}
	if os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return old
	}
	if t, err := os.Readlink(old); err == nil && !filepath.IsAbs(t) {
		// a relative link points one folder further up from .desk
		if os.Symlink(filepath.Join("..", t), p) != nil {
			return old
		}
		os.Remove(old)
		return p
	}
	if os.Rename(old, p) != nil {
		return old
	}
	return p
}

// tomlOrg is the organization dir is, if it has a desk.toml: named by its
// org = "Billing", else after the folder.
func tomlOrg(dir string) (string, bool) {
	var c struct {
		Org string `toml:"org"`
	}
	if _, err := toml.DecodeFile(Moved(dir, "desk.toml"), &c); errors.Is(err, fs.ErrNotExist) {
		return "", false
	} // a broken file is still an org, named after its folder; Load says what's wrong
	if c.Org == "" {
		c.Org = filepath.Base(dir)
	}
	return c.Org, true
}

// Scan is the organizations fs give, in the order found: each folder itself
// (with Root, and a desk.toml), its subfolders minus hidden ones (with
// OnlyToml, those with a desk.toml; the others are skipped), and further
// down the folders with a desk.toml, level by level (named by its org =).
// A name found twice is the first one.
// ponytail: a deep Depth reads every folder down there (node_modules too).
func Scan(fs []Folder) (out []Found, skipped []string) {
	seen := map[string]bool{}
	for i, f := range fs {
		add := func(name, dir string) {
			if !seen[name] {
				seen[name] = true
				out = append(out, Found{name, dir, i})
			}
		}
		base := Expand(f.Path)
		if f.Root { // only then: tomlOrg moves an old desk.toml, and base isn't an org
			if n, ok := tomlOrg(base); ok {
				add(n, base)
			}
		}
		dirs := []string{base}
		for level := 1; level <= f.Depth; level++ {
			var next []string
			for _, d := range dirs {
				ents, _ := os.ReadDir(d)
				for _, e := range ents {
					if !e.IsDir() || hidden(e.Name()) {
						continue
					}
					sub := filepath.Join(d, e.Name())
					switch {
					case level > 1:
						if n, ok := tomlOrg(sub); ok {
							add(n, sub)
						}
					case !f.OnlyToml || hasToml(sub): // a subfolder goes by its folder's name
						add(e.Name(), sub)
					default:
						skipped = append(skipped, e.Name())
					}
					next = append(next, sub)
				}
			}
			dirs = next
		}
	}
	return out, skipped
}

func hasToml(dir string) bool { _, ok := tomlOrg(dir); return ok }

// List is root's organizations, sorted: what Scan finds in its Folders.
func List(root string) []string {
	m := map[string]string{}
	var out []string
	fs, _ := Scan(Folders(root))
	for _, f := range fs {
		m[f.Name] = f.Dir
		out = append(out, f.Name)
	}
	foundMu.Lock()
	found[root] = m
	clear(accounts)
	foundMu.Unlock()
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return out
}

// where is org's folder: where List found it, else root/org.
func where(root, org string) string {
	foundMu.Lock()
	m, known := found[root]
	foundMu.Unlock()
	if !known {
		List(root)
		return where(root, org)
	}
	if d, ok := m[org]; ok {
		return d
	}
	return filepath.Join(root, org)
}

// Account is the organization whose Claude profile org's sessions run
// under: AccountAt its folder. It is remembered until List looks again or
// the folders are saved: desk asks every frame, and AccountAt reads the
// settings.
func Account(root, org string) string {
	k := root + "\x00" + org
	foundMu.Lock()
	a, ok := accounts[k]
	foundMu.Unlock()
	if !ok {
		a = AccountAt(root, where(root, org))
		foundMu.Lock()
		accounts[k] = a
		foundMu.Unlock()
	}
	return a
}

// AccountAt is the organization whose Claude profile runs what's in dir:
// the subfolder of a search folder dir is in (itself, or for a nested org
// the one around it), or a searched folder's own organization; "" outside
// them all.
func AccountAt(root, dir string) string {
	for _, f := range Folders(root) {
		base := Expand(f.Path)
		rel, err := filepath.Rel(base, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if rel == "." {
			if f.Root {
				if n, ok := tomlOrg(base); ok {
					return n
				}
			}
			continue
		}
		top, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
		return top
	}
	return ""
}

// Dir is where a card of org runs: the org's folder, or sub inside it.
// Without an org it is root itself.
func Dir(root, org, sub string) string {
	if org == "" {
		return root
	}
	return filepath.Join(where(root, org), filepath.Clean("/"+sub)) // Clean("/"+sub) keeps sub from climbing out
}

// File is org's desk.toml, <Org>/.desk/desk.toml (Moved there from the
// org's folder). Paths inside it stay relative to the org's folder.
func File(root, org string) string { return Moved(where(root, org), "desk.toml") }

// LoadRaw reads org's desk.toml as tables, for the settings dialog to edit
// without losing keys it doesn't show; a missing file is empty.
func LoadRaw(root, org string) (map[string]any, error) {
	raw := map[string]any{}
	_, err := toml.DecodeFile(File(root, org), &raw)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, nil
	}
	return raw, err
}

// SaveRaw writes raw as org's desk.toml, after checking that it reads back
// as a Config, through a temp file and a rename. ponytail: comments in the
// file are lost; a hand-kept desk.toml is edited by hand.
func SaveRaw(root, org string, raw map[string]any) error {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(raw); err != nil {
		return err
	}
	if _, err := toml.Decode(b.String(), &Config{}); err != nil {
		return fmt.Errorf("would not read back: %w", err)
	}
	path := File(root, org)
	tmp := path + ".tmp"
	// a new org's .desk (not its folder); a failure shows in the write
	os.Mkdir(filepath.Dir(path), 0o755)
	if err := os.WriteFile(tmp, b.Bytes(), 0o600); err != nil { // 0600: it may hold a token
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads org's desk.toml; a missing file is an empty config.
func Load(root, org string) (Config, error) {
	var c Config
	if org == "" {
		return c, nil
	}
	path := File(root, org)
	_, err := toml.DecodeFile(path, &c)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Walls are the organizations' folders a session working in dir mustn't
// see: every one but those dir lies in (its own, and an organization
// around a nested one), so an organization nested in dir's is walled too.
// ponytail: an organization around dir's stays seen, since dir is inside it.
func Walls(root, dir string) []string {
	var out []string
	for _, n := range List(root) {
		d := where(root, n)
		if rel, err := filepath.Rel(d, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue // dir is in it
		}
		out = append(out, d)
	}
	return out
}
