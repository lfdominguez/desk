package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ProtonMail/gopenpgp/v3/crypto"

	"github.com/lfdominguez/desk/internal/orgs"
)

// TestCLIConfig: a go-passbolt-cli config (keys lowercased, as its viper
// writes them) gives the server, key and passphrase; desk.toml's password
// wins over it.
func TestCLIConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passbolt_globex_config.toml")
	os.WriteFile(path, []byte(`debug = false
mfamode = 'interactive-totp'
mfatotptoken = ''
serveraddress = 'https://passbolt.globex.com'
userpassword = 'pass phrase'
userprivatekey = "-----BEGIN PGP PRIVATE KEY BLOCK-----\n\nxyz\n-----END PGP PRIVATE KEY BLOCK-----\n"
`), 0o600)
	c, err := loadCLIConfig(path)
	if err != nil || c.ServerAddress != "https://passbolt.globex.com" || c.UserPrivateKey[:5] != "-----" || c.MFATotpToken != "" {
		t.Fatalf("%+v %v", c, err)
	}
	v := orgs.Vault{Name: "globex", Kind: "passbolt", ConfigFile: path}
	if p, err := configured(v); err != nil || p != "pass phrase" {
		t.Fatalf("password from the config: %q %v", p, err)
	}
	v.Password.Token = "from desk.toml"
	if p, _ := configured(v); p != "from desk.toml" {
		t.Fatalf("desk.toml's password should win: %q", p)
	}
}

// fakePassbolt is a Passbolt v4 server: GPGAuth login, a TOTP challenge on
// users/me (while mfa, until a remembered code), and resources whose
// secrets are encrypted to the user's key. expire ends the session;
// forget drops the remembered code.
type fakePassbolt struct {
	*httptest.Server
	mu             sync.Mutex
	session, mfaOK string
	mfa            bool
	logins         int
	deny           bool // resources.json always refuses
	noMFACookie    bool // a TOTP accepted without the passbolt_mfa cookie
	resources      []map[string]any
	lists, bulk    int // metadata lists; lists with every secret (never wanted)
}

const (
	pbToken = "gpgauthv1.3.0|36|00000000-0000-4000-8000-000000000000|gpgauthv1.3.0"
	pbVPN   = "9b2c5f0e-1d2a-4c3b-8e4f-5a6b7c8d9e0f"
)

func newFakePassbolt(t *testing.T, key *crypto.Key, mfa bool) *fakePassbolt {
	pgp := crypto.PGP()
	enc := func(s string) string {
		h, _ := pgp.Encryption().Recipient(key).New()
		m, err := h.Encrypt([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		a, _ := m.Armor()
		return a
	}
	typ := map[string]any{"id": "t1", "slug": "password-string", "definition": map[string]any{"resource": map[string]any{}, "secret": map[string]any{"type": "string"}}}
	resources := []map[string]any{
		{"id": "r0", "name": "Folder link"}, // no secret of the user's
		{"id": "r1", "name": "Acme mail", "username": "me", "uri": "https://mail.acme.example.com", "description": "the note", "secrets": []map[string]string{{"data": enc("pw1")}}},
		{"id": pbVPN, "name": "Globex VPN", "username": "vpn-me", "secrets": []map[string]string{{"data": enc("pw2")}}},
		{"id": "r3", "name": "Broken", "secrets": []map[string]string{{"data": "not pgp"}}},
		// v5: metadata encrypted to the user's key, with a custom field
		{"id": "r4", "metadata_key_type": "user_key", "secrets": []map[string]string{{"data": enc("pw4")}},
			"metadata": enc(`{"name":"Initech API","custom_fields":[{"id":"f1","metadata_key":"pin","metadata_value":"4242"}]}`)},
		{"id": "r5", "metadata_key_type": "user_key", "metadata": "not pgp"}, // unreadable metadata
	}
	f := &fakePassbolt{mfa: mfa, resources: resources}
	reply := func(w http.ResponseWriter, code int, body any) {
		h := map[string]any{"status": "success", "code": code}
		if code != 200 {
			h["status"] = "error"
		}
		if code == 403 && body == nil {
			h["url"] = "/mfa/verify/error.json"
		}
		json.NewEncoder(w).Encode(map[string]any{"header": h, "body": body})
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		cookie := func(name string) string {
			c, _ := r.Cookie(name)
			if c == nil {
				return ""
			}
			return c.Value
		}
		switch r.URL.Path {
		case "/auth/login.json":
			var in struct {
				Auth struct {
					Token string `json:"user_token_result"`
				} `json:"gpg_auth"`
			}
			json.NewDecoder(r.Body).Decode(&in)
			if in.Auth.Token == "" {
				w.Header().Set("X-GPGAuth-User-Auth-Token", url.QueryEscape(enc(pbToken)))
			} else if in.Auth.Token == pbToken {
				f.logins++
				f.session = fmt.Sprint("s", f.logins)
				http.SetCookie(w, &http.Cookie{Name: "passbolt_session", Value: f.session})
			}
			reply(w, 200, nil)
		case "/users/me.json":
			if f.mfa && (f.mfaOK == "" || cookie("passbolt_mfa") != f.mfaOK) {
				reply(w, 403, nil)
				return
			}
			reply(w, 200, map[string]string{"id": "u1"})
		case "/mfa/verify/totp.json":
			var in struct {
				TOTP     string
				Remember bool
			}
			json.NewDecoder(r.Body).Decode(&in)
			if in.TOTP != "123456" || !in.Remember {
				reply(w, 400, map[string]string{"error": "bad code"})
				return
			}
			f.mfaOK = fmt.Sprint("m", f.logins)
			if !f.noMFACookie {
				http.SetCookie(w, &http.Cookie{Name: "passbolt_mfa", Value: f.mfaOK})
			}
			reply(w, 200, nil)
		case "/settings.json":
			reply(w, 200, map[string]any{"passbolt": map[string]any{"plugins": map[string]any{}}})
		case "/resources.json":
			if f.deny || f.session == "" || cookie("passbolt_session") != f.session {
				reply(w, 403, map[string]string{"error": "signed out"})
				return
			}
			q := r.URL.Query()
			ids := q["filter[has-id][]"]
			if len(ids) == 0 && q.Get("contain[secret]") != "" {
				f.bulk++
			} else if len(ids) == 0 {
				f.lists++
			}
			out := []map[string]any{}
			for _, res := range f.resources {
				if len(ids) > 0 && !slices.Contains(ids, res["id"].(string)) {
					continue
				}
				o := map[string]any{}
				for k, v := range res {
					if k != "secrets" || q.Get("contain[secret]") != "" {
						o[k] = v
					}
				}
				if q.Get("contain[resource-type]") != "" {
					o["resource_type"] = typ
				}
				out = append(out, o)
			}
			reply(w, 200, out)
		default:
			reply(w, 404, map[string]string{"error": r.URL.Path})
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakePassbolt) expire() { f.mu.Lock(); f.session = "gone"; f.mu.Unlock() }
func (f *fakePassbolt) forget() { f.mu.Lock(); f.mfaOK = ""; f.mu.Unlock() }

// pbKey is a new PGP key, armored and locked with passphrase.
func pbKey(t *testing.T, passphrase string) (*crypto.Key, string) {
	pgp := crypto.PGP()
	key, err := pgp.KeyGeneration().AddUserId("Acme", "me@example.com").New().GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	locked, _ := pgp.LockKey(key, []byte(passphrase))
	armored, _ := locked.Armor()
	return key, armored
}

// TestPassbolt unlocks a fake Passbolt through a CLI config with a TOTP
// challenge, reads resources by name and id, logs in again when the
// session ends (the remembered code skipping the challenge) and asks for
// a code again when the server forgets it.
func TestPassbolt(t *testing.T) {
	key, armored := pbKey(t, "pass phrase")
	f := newFakePassbolt(t, key, true)
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte(fmt.Sprintf("serveraddress = %q\nuserpassword = 'pass phrase'\nuserprivatekey = %q\n", f.URL, armored)), 0o600)
	v := orgs.Vault{Name: "acme", Kind: "passbolt", ConfigFile: path}
	ctx := context.Background()

	var locked *LockedError
	if err := Unlock(ctx, v, "", ""); !errors.As(err, &locked) || !locked.Code {
		t.Fatalf("no code: want locked for a code, got %v", err)
	}
	if err := Unlock(ctx, v, "", "000000"); err == nil || !strings.Contains(err.Error(), "one-time code") {
		t.Fatalf("wrong code: %v", err)
	}
	if err := Unlock(ctx, v, "", "123456"); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][3]string{
		{"Acme mail", "", "pw1"}, {"Globex VPN", "password", "pw2"}, {pbVPN, "username", "vpn-me"},
		{"Acme mail", "uri", "https://mail.acme.example.com"}, {"Acme mail", "notes", "the note"},
		{"Acme mail", "description", "the note"}, {"Initech API", "", "pw4"}, {"Initech API", "pin", "4242"},
	} {
		if got, err := Get(ctx, v, c[0], c[1]); err != nil || got != c[2] {
			t.Errorf("%s %s: %q %v", c[0], c[1], got, err)
		}
	}
	for _, c := range [][3]string{
		{"Acme mail", "pin", `has no field "pin"`}, {"Initech", "", `no such item "Initech"`}, {"Broken", "", "decrypt"},
	} {
		if _, err := Get(ctx, v, c[0], c[1]); err == nil || !strings.Contains(err.Error(), c[2]) {
			t.Errorf("%s %s: %v", c[0], c[1], err)
		}
	}
	names := func() string {
		es, err := List(ctx, v)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		var ns []string
		for _, e := range es {
			ns = append(ns, e.ID+":"+e.Name+":"+e.Username+":"+e.Notes)
		}
		return strings.Join(ns, ",")
	}
	const all = "r0:Folder link::,r1:Acme mail:me:the note," + pbVPN + ":Globex VPN:vpn-me:,r3:Broken::,r4:Initech API::"
	if got := names(); got != all {
		t.Errorf("list: %s", got)
	}

	f.expire()
	if got, err := Get(ctx, v, "r1", "username"); err != nil || got != "me" {
		t.Fatalf("after the session ended: %q %v", got, err)
	}
	f.expire()
	if got := names(); got != all {
		t.Errorf("list after the session ended: %s", got)
	}

	f.expire()
	f.forget()
	if _, err := Get(ctx, v, pbVPN, ""); !errors.As(err, &locked) || !locked.Code {
		t.Errorf("forgotten code: want locked for a code, got %v", err)
	}
	f.expire()
	if _, err := List(ctx, v); !errors.As(err, &locked) || !locked.Code {
		t.Errorf("list, forgotten code: want locked for a code, got %v", err)
	}
}

// TestPassboltSetup: a key file with url and passphrase unlocks without a
// challenge; a wrong passphrase, a missing key file or config, or no url
// or key fails.
func TestPassboltSetup(t *testing.T) {
	key, armored := pbKey(t, "secret")
	f := newFakePassbolt(t, key, false)
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key.asc")
	os.WriteFile(keyFile, []byte(armored), 0o600)
	ctx := context.Background()
	v := orgs.Vault{Name: "globex", Kind: "passbolt", URL: f.URL, KeyFile: keyFile}
	if err := Unlock(ctx, v, "wrong", ""); err == nil {
		t.Fatal("wrong passphrase unlocked")
	}
	if err := Unlock(ctx, v, "secret", ""); err != nil {
		t.Fatal(err)
	}
	if got, err := Get(ctx, v, "Globex VPN", ""); err != nil || got != "pw2" {
		t.Fatalf("%q %v", got, err)
	}
	for _, c := range []struct {
		v   orgs.Vault
		err string
	}{
		{orgs.Vault{URL: f.URL, KeyFile: filepath.Join(dir, "none.asc")}, "none.asc"},
		{orgs.Vault{ConfigFile: filepath.Join(dir, "none.toml")}, "config_file:"},
		{orgs.Vault{URL: f.URL}, "needs url"},
		{orgs.Vault{KeyFile: keyFile}, "needs url"},
	} {
		if _, err := openPassbolt(ctx, c.v, "secret", ""); err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%+v: %v", c.v, err)
		}
	}
}

// TestPassboltMFAEdges: a CLI config's TOTP secret answers the challenge
// itself (so no LockedError asks for a code), and a code accepted without
// the MFA cookie fails the unlock.
func TestPassboltMFAEdges(t *testing.T) {
	key, armored := pbKey(t, "secret")
	f := newFakePassbolt(t, key, true)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte(fmt.Sprintf("serveraddress = %q\nuserprivatekey = %q\nmfatotptoken = 'JBSWY3DPEHPK3PXP'\n", f.URL, armored)), 0o600)
	var locked *LockedError
	err := Unlock(ctx, orgs.Vault{Name: "acme", Kind: "passbolt", ConfigFile: path}, "secret", "")
	if err == nil || errors.As(err, &locked) || !strings.Contains(err.Error(), "TOTP") {
		t.Errorf("TOTP secret: want the helper's own challenge error, got %v", err)
	}

	f.mu.Lock()
	f.noMFACookie = true
	f.mu.Unlock()
	keyFile := filepath.Join(t.TempDir(), "key.asc")
	os.WriteFile(keyFile, []byte(armored), 0o600)
	v := orgs.Vault{Name: "globex", Kind: "passbolt", URL: f.URL, KeyFile: keyFile}
	if _, err := openPassbolt(ctx, v, "secret", "123456"); err == nil || !strings.Contains(err.Error(), "no MFA cookie") {
		t.Errorf("no cookie: %v", err)
	}
}

// TestPassboltDenied: when resources stay refused after logging in again,
// list and item fail rather than returning nothing.
func TestPassboltDenied(t *testing.T) {
	key, armored := pbKey(t, "secret")
	f := newFakePassbolt(t, key, false)
	keyFile := filepath.Join(t.TempDir(), "key.asc")
	os.WriteFile(keyFile, []byte(armored), 0o600)
	ctx := context.Background()
	o, err := openPassbolt(ctx, orgs.Vault{URL: f.URL, KeyFile: keyFile}, "secret", "")
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.deny = true
	f.mu.Unlock()
	if es, err := o.list(ctx); err == nil || es != nil {
		t.Errorf("list: %v %v", es, err)
	}
	for _, name := range []string{"Globex VPN", pbVPN} { // the list, then the one resource by id
		if s, err := o.item(ctx, name, ""); err == nil || s != "" {
			t.Errorf("item %s: %q %v", name, s, err)
		}
	}
}

// TestPassboltFetchesOne: an item is read without fetching every secret:
// an id directly, a name through one metadata list a session, learnt again
// when the name is gone or moved to another resource; a resource without
// a secret of the user's is no item.
func TestPassboltFetchesOne(t *testing.T) {
	key, armored := pbKey(t, "secret")
	f := newFakePassbolt(t, key, false)
	keyFile := filepath.Join(t.TempDir(), "key.asc")
	os.WriteFile(keyFile, []byte(armored), 0o600)
	ctx := context.Background()
	o, err := openPassbolt(ctx, orgs.Vault{URL: f.URL, KeyFile: keyFile}, "secret", "")
	if err != nil {
		t.Fatal(err)
	}
	get := func(name, want string, lists int) {
		t.Helper()
		got, err := o.item(ctx, name, "")
		if want == "" && (err == nil || !strings.Contains(err.Error(), "no such item")) || want != "" && got != want {
			t.Errorf("%s: %q %v", name, got, err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.lists != lists || f.bulk != 0 {
			t.Errorf("%s: %d lists (want %d), %d with every secret", name, f.lists, lists, f.bulk)
		}
	}
	edit := func(fn func()) { f.mu.Lock(); fn(); f.mu.Unlock() }
	get(pbVPN, "pw2", 0)
	get("Acme mail", "pw1", 1)
	get("Globex VPN", "pw2", 1)
	get("Folder link", "", 2)
	edit(func() { f.resources[2]["name"] = "Globex VPN 2" })
	get("Globex VPN", "", 3)
	get("Globex VPN 2", "pw2", 3)
	edit(func() { // Acme mail deleted, its name given to the VPN
		f.resources = slices.Delete(f.resources, 1, 2)
		f.resources[1]["name"] = "Acme mail"
	})
	get("Acme mail", "pw2", 4)
}
