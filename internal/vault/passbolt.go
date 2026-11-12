package vault

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/passbolt/go-passbolt/api"
	"github.com/passbolt/go-passbolt/helper"

	"github.com/lfdominguez/desk/internal/orgs"
)

// passbolt is a logged-in Passbolt client; its session is renewed on a
// failure, since the key and passphrase stay in it.
type passbolt struct {
	mu  sync.Mutex
	c   *api.Client
	ids map[string]string // a resource's name or id → its id: learn
}

// cliConfig is the part of a go-passbolt-cli config desk uses (the CLI's
// viper writes its keys lowercased).
type cliConfig struct {
	ServerAddress  string `toml:"serveraddress"`
	UserPrivateKey string `toml:"userprivatekey"`
	UserPassword   string `toml:"userpassword"`
	MFATotpToken   string `toml:"mfatotptoken"`
	MFATotpOffset  string `toml:"mfatotpoffset"`
}

func loadCLIConfig(path string) (cliConfig, error) {
	var c cliConfig
	_, err := toml.DecodeFile(home(path), &c)
	return c, err
}

func openPassbolt(ctx context.Context, v orgs.Vault, passphrase, code string) (opened, error) {
	var cli cliConfig
	if v.ConfigFile != "" {
		var err error
		if cli, err = loadCLIConfig(v.ConfigFile); err != nil {
			return nil, fmt.Errorf("config_file: %w", err)
		}
	}
	url, key := cmp.Or(v.URL, cli.ServerAddress), cli.UserPrivateKey
	if v.KeyFile != "" {
		b, err := os.ReadFile(home(v.KeyFile))
		if err != nil {
			return nil, err
		}
		key = string(b)
	}
	if url == "" || key == "" {
		return nil, fmt.Errorf("passbolt needs url and key_file, or config_file")
	}
	c, err := api.NewClient(client, "desk", url, key, passphrase)
	if err != nil {
		return nil, err // a wrong passphrase fails here, unlocking the key
	}
	if cli.MFATotpToken != "" { // the CLI's noninteractive TOTP: desk makes the codes too
		offset, _ := time.ParseDuration(cli.MFATotpOffset)
		helper.AddMFACallbackTOTP(c, 3, 10*time.Second, offset, cli.MFATotpToken)
	} else {
		c.MFACallback = askedTOTP(&code)
	}
	if err := c.Login(ctx); err != nil { // its users/me is where Passbolt asks for MFA
		return nil, fmt.Errorf("login: %w", err)
	}
	return &passbolt{c: c}, nil
}

// askedTOTP answers Passbolt's MFA challenge with the code the user typed,
// once (a code is single-use), asking the server to remember this device;
// with no code left it fails with errNeedCode, for desk to ask.
func askedTOTP(code *string) func(context.Context, *api.Client, *api.APIResponse) (http.Cookie, error) {
	return func(ctx context.Context, c *api.Client, res *api.APIResponse) (http.Cookie, error) {
		if *code == "" {
			return http.Cookie{}, errNeedCode
		}
		body := struct {
			TOTP     string `json:"totp"`
			Remember bool   `json:"remember"`
		}{*code, true}
		*code = ""
		raw, _, err := c.DoCustomRequestAndReturnRawResponseV5(ctx, "POST", "mfa/verify/totp.json", body, nil)
		if err != nil {
			return http.Cookie{}, fmt.Errorf("one-time code: %w", err)
		}
		for _, ck := range raw.Cookies() {
			if ck.Name == "passbolt_mfa" {
				return *ck, nil
			}
		}
		return http.Cookie{}, errors.New("one-time code: no MFA cookie in Passbolt's reply")
	}
}

func (p *passbolt) item(ctx context.Context, name, field string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, err := p.find(ctx, name, field)
	if err != nil && !errors.Is(err, errNoItem) { // the session may have expired: log in again (the remembered MFA cookie skips the code)
		if lerr := p.c.Login(ctx); errors.Is(lerr, errNeedCode) {
			return "", lerr
		} else if lerr == nil {
			s, err = p.find(ctx, name, field)
		}
	}
	return s, err
}

// list is every resource's metadata; no secret is decrypted.
func (p *passbolt) list(ctx context.Context) ([]Entry, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	rs, err := p.c.GetResources(ctx, &api.GetResourcesOptions{ContainResourceType: true})
	if err != nil { // the session may have expired: log in again
		if lerr := p.c.Login(ctx); lerr != nil {
			return nil, lerr
		}
		if rs, err = p.c.GetResources(ctx, &api.GetResourcesOptions{ContainResourceType: true}); err != nil {
			return nil, err
		}
	}
	var out []Entry
	for _, r := range rs {
		_, meta, _, err := helper.GetResourceFieldMaps(p.c, r, api.Secret{}, r.ResourceType, false)
		if err != nil {
			continue // ponytail: unreadable metadata is skipped, not reported
		}
		out = append(out, Entry{ID: r.ID, Name: helper.GetStringField(meta, "name"), Username: helper.GetStringField(meta, "username"),
			URI: helper.GetStringField(meta, "uri"), Notes: helper.GetStringField(meta, "description")})
	}
	return out, nil
}

// isUUID is a Passbolt resource id, which can be fetched without a list.
var isUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// learn maps every resource's id and name to its id, from the metadata
// list (no secrets: fetching them all took seconds on a large vault).
func (p *passbolt) learn(ctx context.Context) error {
	rs, err := p.c.GetResources(ctx, &api.GetResourcesOptions{ContainResourceType: true})
	if err != nil {
		return err
	}
	p.ids = map[string]string{}
	for _, r := range rs {
		p.ids[r.ID] = r.ID
		if _, meta, _, err := helper.GetResourceFieldMaps(p.c, r, api.Secret{}, r.ResourceType, false); err == nil {
			if n := helper.GetStringField(meta, "name"); p.ids[n] == "" { // the first of equal names, as before
				p.ids[n] = r.ID
			}
		}
	}
	return nil
}

// one is resource id with its secret and type, in one request.
func (p *passbolt) one(ctx context.Context, id string) (*api.Resource, error) {
	rs, err := p.c.GetResources(ctx, &api.GetResourcesOptions{FilterHasID: []string{id}, ContainSecret: true, ContainResourceType: true})
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 || len(rs[0].Secrets) == 0 {
		return nil, nil
	}
	return &rs[0], nil
}

// find reads field of the resource named (or with id) name: its id from
// p.ids (learnt once a session, again when a name is missing or moved),
// then only that resource's secret.
func (p *passbolt) find(ctx context.Context, name, field string) (string, error) {
	id, known := p.ids[name]
	if !known && isUUID.MatchString(name) {
		id, known = name, true
	}
	var r *api.Resource
	var meta, sec map[string]any
	for try := 0; ; try++ {
		if !known {
			if err := p.learn(ctx); err != nil {
				return "", err
			}
			if id = p.ids[name]; id == "" {
				return "", fmt.Errorf("%w %q", errNoItem, name)
			}
		}
		var err error
		if r, err = p.one(ctx, id); err != nil {
			return "", err
		}
		if r != nil {
			if _, meta, sec, err = helper.GetResourceFieldMaps(p.c, *r, r.Secrets[0], r.ResourceType, true); err != nil {
				return "", err
			}
			if r.ID == name || helper.GetStringField(meta, "name") == name {
				break
			}
		}
		if !known || try > 0 { // gone, renamed, or no secret of ours even after a fresh list
			return "", fmt.Errorf("%w %q", errNoItem, name)
		}
		known = false
	}
	switch field {
	case "", "password":
		return helper.GetStringField(sec, "password"), nil
	case "username", "uri", "description":
		return helper.GetStringField(meta, field), nil
	case "notes":
		return helper.GetStringField(meta, "description"), nil
	}
	if v, ok := helper.ParseCustomFields(meta, sec).Map()[field]; ok {
		return v, nil
	}
	return "", fmt.Errorf("%q has no field %q", name, field)
}
