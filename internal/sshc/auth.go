package sshc

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/lfdominguez/desk/internal/sshconfig"
)

// auth is sshconnect2.c's userauth for one connection.
type auth struct {
	cfg *sshconfig.Config
	o   Options
	hc  *hostCheck
	ag  agent.ExtendedAgent
	agc io.Closer

	unsupported []string                       // what the config wants that desk can't do
	loaded      map[string]ssh.AlgorithmSigner // private keys read, by file
	pwTried     bool                           // Options.Password had its try
	asked       map[string]int                 // questions put to the user, per method
}

// errNoTries ends a method whose questions ran out: the vault's spare try
// (methods) went unused, so the user isn't asked one time more.
var errNoTries = errors.New("ssh: unable to authenticate: no more password prompts")

// ask counts a question for method against NumberOfPasswordPrompts.
func (a *auth) ask(method string) error {
	if a.asked[method]++; a.asked[method] > a.cfg.Int("numberofpasswordprompts") {
		return errNoTries
	}
	return nil
}

func newAuth(cfg *sshconfig.Config, o Options, hc *hostCheck) *auth {
	a := &auth{cfg: cfg, o: o, hc: hc, loaded: map[string]ssh.AlgorithmSigner{}, asked: map[string]int{}}
	if conn := openAgent(cfg, o.Logf); conn != nil {
		a.ag, a.agc = agent.NewClient(conn), conn
	}
	return a
}

func (a *auth) close() {
	if a.agc != nil {
		a.agc.Close()
	}
}

// agentPath is IdentityAgent's socket: SSH_AUTH_SOCK when unset (or set
// to that name), "" for none.
func agentPath(cfg *sshconfig.Config) string {
	switch p := cfg.Get("identityagent"); p {
	case "none":
		return ""
	case "", "SSH_AUTH_SOCK":
		return defaultAgent()
	default:
		return p
	}
}

func openAgent(cfg *sshconfig.Config, logf func(string, ...any)) io.ReadWriteCloser {
	p := agentPath(cfg)
	if p == "" {
		return nil
	}
	conn, err := dialAgent(p)
	if err != nil {
		logf("Error connecting to agent %s: %v", p, err)
		return nil
	}
	return conn
}

// methods are the auth methods in PreferredAuthentications order, those
// the config turns off left out. x/crypto tries each once in that order
// among those the server still accepts.
func (a *auth) methods() []ssh.AuthMethod {
	cfg := a.cfg
	prefs := cfg.Get("preferredauthentications")
	if prefs == "" {
		prefs = "gssapi-with-mic,hostbased,publickey,keyboard-interactive,password"
	}
	// A spare try for the Password hook's, which isn't looked up (a vault)
	// until a method wants it: a key that works never needs it.
	tries := cfg.Int("numberofpasswordprompts")
	if !a.o.noPassword {
		tries++
	}
	var out []ssh.AuthMethod
	for _, m := range strings.Split(prefs, ",") {
		switch m {
		case "publickey":
			if cfg.Get("pubkeyauthentication") != "false" {
				out = append(out, ssh.PublicKeysCallback(a.signers))
			}
		case "keyboard-interactive":
			if cfg.Bool("kbdinteractiveauthentication") && tries > 0 {
				out = append(out, ssh.RetryableAuthMethod(ssh.KeyboardInteractive(a.challenge), tries))
			}
		case "password":
			if cfg.Bool("passwordauthentication") && tries > 0 {
				out = append(out, ssh.RetryableAuthMethod(ssh.PasswordCallback(a.password), tries))
			}
		case "hostbased":
			if cfg.Bool("hostbasedauthentication") {
				a.unsupported = append(a.unsupported, "host-based authentication")
			}
		}
	}
	if cfg.Get("pkcs11provider") != "" {
		a.unsupported = append(a.unsupported, "PKCS11Provider keys")
	}
	if p := cfg.Get("securitykeyprovider"); p != "" && p != "internal" {
		a.unsupported = append(a.unsupported, "SecurityKeyProvider "+p)
	}
	return out
}

// explain adds to an authentication failure what desk skipped.
func (a *auth) explain(err error) error {
	if err == nil || !strings.Contains(err.Error(), "unable to authenticate") {
		return err
	}
	if len(a.unsupported) > 0 {
		return fmt.Errorf("%w; this host may need %s, which desk doesn't support", err, strings.Join(a.unsupported, " or "))
	}
	if strings.Contains(err.Error(), "attempted methods [none]") {
		return fmt.Errorf("%w; the server offers no method desk can use (it may need GSSAPI or host-based authentication, which desk doesn't support)", err)
	}
	return err
}

// first is Options.Password's one try, for the first password asked.
func (a *auth) first() (string, bool) {
	if a.pwTried {
		return "", false
	}
	a.pwTried = true
	return a.o.Password()
}

func (a *auth) password() (string, error) {
	a.o.Step("Authenticating: password", false)
	if a.hc.unsafe {
		return "", errors.New("Password authentication is disabled to avoid man-in-the-middle attacks.")
	}
	if p, ok := a.first(); ok {
		return p, nil
	}
	if err := a.ask("password"); err != nil {
		return "", err
	}
	return a.o.Prompt(fmt.Sprintf("%s@%s's password: ", a.cfg.User(), a.cfg.HostName()), false)
}

// challenge answers keyboard-interactive: a lone hidden question takes
// Options.Password first (it is the password, on most servers).
func (a *auth) challenge(name, instruction string, qs []string, echos []bool) ([]string, error) {
	a.o.Step("Authenticating: keyboard-interactive", false)
	if a.hc.unsafe {
		return nil, errors.New("Keyboard-interactive authentication is disabled to avoid man-in-the-middle attacks.")
	}
	if len(qs) == 1 && !echos[0] {
		if p, ok := a.first(); ok {
			return []string{p}, nil
		}
	}
	if len(qs) > 0 {
		if err := a.ask("keyboard-interactive"); err != nil {
			return nil, err
		}
	}
	var head []string
	for _, s := range []string{name, instruction} {
		if s = strings.TrimSpace(s); s != "" {
			head = append(head, s)
		}
	}
	ans := make([]string, len(qs))
	for i, q := range qs {
		if i == 0 && head != nil {
			q = strings.Join(head, "\n") + "\n" + q
		}
		v, err := a.o.Prompt(q, echos[i])
		if err != nil {
			return nil, err
		}
		ans[i] = v
	}
	return ans, nil
}

// id is one key to offer and how to get its private half.
type id struct {
	pub   ssh.PublicKey
	plain ssh.PublicKey // pub without its certificate
	file  string        // the private key's file, "" for the agent's
}

// signers is sshconnect2.c's pubkey_prepare: CertificateFile certificates,
// then the agent's keys that the config names, the agent's others (unless
// IdentitiesOnly), the config's remaining keys; each IdentityFile followed
// by its -cert.pub; then filtered by PubkeyAcceptedAlgorithms. A key is
// read (its passphrase asked) only once the server would take it.
func (a *auth) signers() ([]ssh.Signer, error) {
	a.o.Step("Authenticating: public key", false)
	cfg := a.cfg
	var agentKeys []ssh.Signer
	if a.ag != nil {
		var err error
		if agentKeys, err = a.ag.Signers(); err != nil {
			a.o.Logf("Error reading the agent's keys: %v", err)
		}
	}
	inAgent := func(k ssh.PublicKey) ssh.Signer {
		for _, s := range agentKeys {
			if keyEqual(s.PublicKey(), k) {
				return s
			}
		}
		return nil
	}
	paths, err := cfg.IdentityFiles()
	if err != nil {
		a.o.Logf("IdentityFile: %v", err)
	}
	var files []id
	for _, p := range paths {
		pub, err := a.publicKey(p)
		if pub == nil {
			if !errors.Is(err, fs.ErrNotExist) {
				a.o.Logf("Load key \"%s\": %v", p, err) // as ssh: no Go escapes (Windows' backslashes)
			}
			continue
		}
		files = append(files, id{pub, pub, p})
		if c := readCert(p + "-cert.pub"); c != nil && keyEqual(c.Key, pub) {
			files = append(files, id{c, pub, p})
		}
	}
	var certs []id
	cps, _ := cfg.CertificateFiles()
	for _, p := range cps {
		c := readCert(p)
		if c == nil || c.CertType != ssh.UserCert {
			a.o.Logf("no user certificate in %s", p)
			continue
		}
		i := id{c, c.Key, ""}
		for _, f := range files {
			if keyEqual(f.plain, c.Key) {
				i.file = f.file
			}
		}
		certs = append(certs, i)
	}
	var named, others []id
	for _, s := range agentKeys {
		i := slices.IndexFunc(files, func(f id) bool { return keyEqual(f.pub, s.PublicKey()) })
		switch {
		case i >= 0:
			named = append(named, files[i])
			files = slices.Delete(files, i, i+1)
		case !cfg.Bool("identitiesonly"):
			others = append(others, id{s.PublicKey(), plainOf(s.PublicKey()), ""})
		}
	}
	accepted := cfg.Algorithms("pubkeyacceptedalgorithms")
	var out []ssh.Signer
	for _, i := range slices.Concat(certs, named, others, files) {
		ag := inAgent(i.plain)
		if ag == nil {
			ag = inAgent(i.pub)
		}
		if ag == nil {
			if _, err := os.Stat(i.file); err != nil { // "" too
				continue // a .pub alone, its key not in the agent
			}
		}
		if ag == nil && strings.HasPrefix(i.plain.Type(), "sk-") {
			a.unsupported = append(a.unsupported, "a security key ("+i.file+") outside an agent")
			continue
		}
		algs := signAlgorithms(i.pub, accepted)
		if algs == nil {
			a.o.Logf("Skipping %s key %s: key type not in PubkeyAcceptedAlgorithms", i.pub.Type(), i.file)
			continue
		}
		if k, ok := i.plain.(ssh.CryptoPublicKey); ok {
			if r, ok := k.CryptoPublicKey().(*rsa.PublicKey); ok && r.N.BitLen() < cfg.Int("requiredrsasize") {
				a.o.Logf("Skipping RSA key %s: shorter than RequiredRSASize", i.file)
				continue
			}
		}
		out = append(out, &lazySigner{pub: i.pub, algs: algs, load: a.loader(ag, i.file)})
	}
	return out, nil
}

func plainOf(k ssh.PublicKey) ssh.PublicKey {
	if c, ok := k.(*ssh.Certificate); ok {
		return c.Key
	}
	return k
}

func (a *auth) loader(ag ssh.Signer, file string) func() (ssh.AlgorithmSigner, error) {
	return func() (ssh.AlgorithmSigner, error) {
		if s, ok := ag.(ssh.AlgorithmSigner); ok {
			return s, nil
		}
		return a.loadFile(file)
	}
}

// signAlgorithms is the signature algorithms pub may sign with that
// PubkeyAcceptedAlgorithms lets in, nil for none.
func signAlgorithms(pub ssh.PublicKey, accepted []string) []string {
	protos := []string{pub.Type()}
	switch pub.Type() {
	case ssh.KeyAlgoRSA:
		protos = []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	case ssh.CertAlgoRSAv01:
		protos = []string{ssh.CertAlgoRSASHA512v01, ssh.CertAlgoRSASHA256v01, ssh.CertAlgoRSAv01}
	}
	var out []string
	for _, p := range protos {
		if slices.Contains(accepted, p) {
			out = append(out, underlying(p))
		}
	}
	return out
}

// publicKey is an identity's public half: its .pub, else what its private
// file says unencrypted, else (an old encrypted PEM key) the key itself,
// its passphrase asked now.
func (a *auth) publicKey(p string) (ssh.PublicKey, error) {
	if b, err := os.ReadFile(p + ".pub"); err == nil {
		if k, _, _, _, err := ssh.ParseAuthorizedKey(b); err == nil {
			return k, nil
		}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	s, err := ssh.ParsePrivateKey(b)
	var pm *ssh.PassphraseMissingError
	switch {
	case err == nil:
		return s.PublicKey(), nil
	case errors.As(err, &pm) && pm.PublicKey != nil:
		return pm.PublicKey, nil
	case errors.As(err, &pm):
		k, err := a.loadFile(p)
		if err != nil {
			return nil, err
		}
		return k.PublicKey(), nil
	}
	return nil, err
}

func readCert(p string) *ssh.Certificate {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	k, _, _, _, err := ssh.ParseAuthorizedKey(b)
	if err != nil {
		return nil
	}
	c, _ := k.(*ssh.Certificate)
	return c
}

// loadFile reads a private key, asking its passphrase up to
// NumberOfPasswordPrompts times, then AddKeysToAgent.
func (a *auth) loadFile(p string) (ssh.AlgorithmSigner, error) {
	if s := a.loaded[p]; s != nil {
		return s, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	raw, err := ssh.ParseRawPrivateKey(b)
	var pm *ssh.PassphraseMissingError
	if errors.As(err, &pm) {
		for range max(a.cfg.Int("numberofpasswordprompts"), 1) {
			pass, perr := a.o.Prompt(fmt.Sprintf("Enter passphrase for key '%s': ", p), false)
			if perr != nil {
				return nil, perr
			}
			if pass == "" {
				return nil, fmt.Errorf("no passphrase given for %s", p)
			}
			if raw, err = ssh.ParseRawPrivateKeyWithPassphrase(b, []byte(pass)); err == nil {
				break
			}
			a.o.Logf("Bad passphrase for %s", p)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("Load key \"%s\": %v", p, err)
	}
	s, err := ssh.NewSignerFromKey(raw)
	if err != nil {
		return nil, err
	}
	as := s.(ssh.AlgorithmSigner)
	a.addToAgent(raw, p)
	a.loaded[p] = as
	return as, nil
}

// addToAgent is maybe_add_key_to_agent: AddKeysToAgent yes, ask
// (Prompt), confirm (the agent asks on each use), with its lifetime.
func (a *auth) addToAgent(raw any, file string) {
	f := strings.Fields(a.cfg.Get("addkeystoagent"))
	if a.ag == nil || len(f) == 0 || f[0] == "false" {
		return
	}
	k := agent.AddedKey{PrivateKey: raw, Comment: file}
	mode := f[0]
	if n, err := strconv.Atoi(mode); err == nil {
		mode, k.LifetimeSecs = "true", uint32(n)
	}
	if len(f) > 1 {
		n, _ := strconv.Atoi(f[1])
		k.LifetimeSecs = uint32(n)
	}
	switch mode {
	case "ask":
		if ok, _ := a.hc.confirm(fmt.Sprintf("Add key %s to agent? (yes/no) ", file), ""); !ok {
			return
		}
	case "confirm":
		k.ConfirmBeforeUse = true
	}
	if err := a.ag.Add(k); err != nil {
		a.o.Logf("could not add identity to agent: %s (%v)", file, err)
		return
	}
	a.o.Logf("Identity added: %s", file)
}

// lazySigner offers a key and reads its private half only to sign.
type lazySigner struct {
	pub  ssh.PublicKey
	algs []string
	load func() (ssh.AlgorithmSigner, error)
}

func (s *lazySigner) PublicKey() ssh.PublicKey { return s.pub }
func (s *lazySigner) Algorithms() []string     { return s.algs }

func (s *lazySigner) Sign(r io.Reader, data []byte) (*ssh.Signature, error) {
	return s.SignWithAlgorithm(r, data, "")
}

// SignWithAlgorithm signs with the private key; ponytail: a passphrase
// refused here ends the whole connection attempt (x/crypto can't move on
// to the next key); a retry without that key would fix it.
func (s *lazySigner) SignWithAlgorithm(r io.Reader, data []byte, alg string) (*ssh.Signature, error) {
	k, err := s.load()
	if err != nil {
		return nil, err
	}
	return k.SignWithAlgorithm(r, data, alg)
}
