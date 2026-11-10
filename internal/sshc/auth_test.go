package sshc

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"github.com/lfdominguez/desk/internal/sshc/sshtest"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func dialT(t *testing.T, o Options) (*Client, error) {
	t.Helper()
	c, err := Dial(context.Background(), "srv", o)
	if c != nil {
		t.Cleanup(func() { c.Close() })
	}
	return c, err
}

func mustDial(t *testing.T, o Options) *Client {
	t.Helper()
	c, err := dialT(t, o)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (s *server) authLog() []string { return s.Auths() }

func TestPasswordPrompted(t *testing.T) {
	w := newWorld(t)
	s := newServer(t, sshtest.Opts{Password: "hunter2"})
	w.ans = []string{"wrong", "hunter2"}
	mustDial(t, w.opts(base("  NumberOfPasswordPrompts 2\n"), s))
	if len(w.asks) != 2 || w.asks[0] != "me@127.0.0.1's password: " || w.echos[0] {
		t.Fatalf("asked %q %v", w.asks, w.echos)
	}
	// The identity file that isn't there is no news.
	if w.logged("Load key") || w.logged("IdentityFile:") {
		t.Fatalf("logs %q", w.logs)
	}
}

func TestUnreadableKeysLogged(t *testing.T) {
	w := newWorld(t)
	write(t, w.path("id"), "not a key")
	s := newServer(t, sshtest.Opts{Password: "pw", Keys: []ssh.PublicKey{newSigner(t).PublicKey()}})
	w.ans = []string{"pw"}
	mustDial(t, w.opts(base(""), s))
	if !w.logged("Load key \"" + w.path("id") + "\"") {
		t.Fatalf("logs %q", w.logs)
	}
	// A missing one is no news.
	os.Remove(w.path("id"))
	w.logs, w.ans = nil, []string{"pw"}
	mustDial(t, w.opts(base(""), s))
	if w.logged("Load key") || w.logged("IdentityFile:") {
		t.Fatalf("logs %q", w.logs)
	}
}

func TestLegacyEncryptedKey(t *testing.T) {
	w := newWorld(t)
	ek, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalECPrivateKey(ek)
	b, err := x509.EncryptPEMBlock(rand.Reader, "EC PRIVATE KEY", der, []byte("secret"), x509.PEMCipherAES128) //nolint:staticcheck // the old format is the point
	if err != nil {
		t.Fatal(err)
	}
	write(t, w.path("id"), string(pem.EncodeToMemory(b)))
	pub, _ := ssh.NewPublicKey(&ek.PublicKey)
	s := newServer(t, sshtest.Opts{Keys: []ssh.PublicKey{pub}})
	// No public half anywhere: the passphrase is asked up front.
	w.ans = []string{"secret"}
	mustDial(t, w.opts(base(""), s))
	if len(w.asks) != 1 || w.echos[0] {
		t.Fatalf("asked %q", w.asks)
	}
	w.ans = []string{"wrong"}
	if _, err := dialT(t, w.opts(base("  NumberOfPasswordPrompts 1\n"), s)); err == nil || !w.logged("Load key") {
		t.Fatalf("err %v, logs %q", err, w.logs)
	}
}

func TestPasswordPromptsRunOut(t *testing.T) {
	w := newWorld(t)
	s := newServer(t, sshtest.Opts{Password: "hunter2"})
	w.ans = []string{"wrong", "hunter2"}
	if _, err := dialT(t, w.opts(base("  NumberOfPasswordPrompts 1\n"), s)); err == nil {
		t.Fatal("logged in with one wrong try")
	}
}

func TestPasswordHookFirst(t *testing.T) {
	w := newWorld(t)
	s := newServer(t, sshtest.Opts{Password: "vault"})
	o := w.opts(base(""), s)
	o.Password = func() (string, bool) { return "vault", true }
	mustDial(t, o)
	if len(w.asks) != 0 {
		t.Fatalf("asked %q", w.asks)
	}
}

func TestPasswordHookThenPrompt(t *testing.T) {
	w := newWorld(t)
	s := newServer(t, sshtest.Opts{Password: "typed"})
	o := w.opts(base("  NumberOfPasswordPrompts 1\n"), s)
	o.Password = func() (string, bool) { return "stale", true }
	w.ans = []string{"typed"}
	mustDial(t, o)
	if len(w.asks) != 1 {
		t.Fatalf("asked %q", w.asks)
	}
}

// TestPasswordHookNotLookedUpForKey: a key that logs in never calls the
// Password hook (a vault lookup).
func TestPasswordHookNotLookedUpForKey(t *testing.T) {
	w := newWorld(t)
	k := writeKey(t, w.path("id"), "")
	s := newServer(t, sshtest.Opts{Keys: []ssh.PublicKey{k.PublicKey()}, Password: "vault"})
	o := w.opts(base(""), s)
	looked := 0
	o.Password = func() (string, bool) { looked++; return "vault", true }
	mustDial(t, o)
	if looked != 0 {
		t.Fatalf("looked up %d times", looked)
	}
}

// TestPasswordHookEmptyAsksNoMore: a hook with nothing leaves the user
// NumberOfPasswordPrompts questions, not one more, for either method.
func TestPasswordHookEmptyAsksNoMore(t *testing.T) {
	for _, c := range []struct {
		method string
		srv    sshtest.Opts
	}{{"password", sshtest.Opts{Password: "right"}}, {"keyboard-interactive", sshtest.Opts{Kbd: []string{"right"}}}} {
		w := newWorld(t)
		s := newServer(t, c.srv)
		w.ans = []string{"a", "b", "right"}
		o := w.opts(base("  NumberOfPasswordPrompts 2\n  PreferredAuthentications "+c.method+"\n"), s)
		o.Password = func() (string, bool) { return "", false }
		_, err := dialT(t, o)
		if err == nil || len(w.asks) != 2 || !strings.Contains(err.Error(), "unable to authenticate") {
			t.Errorf("%s: asked %d, err %v", c.method, len(w.asks), err)
		}
	}
}

func TestPasswordOff(t *testing.T) {
	w := newWorld(t)
	s := newServer(t, sshtest.Opts{Password: "x"})
	w.ans = []string{"x"}
	if _, err := dialT(t, w.opts(base("  PasswordAuthentication no\n"), s)); err == nil {
		t.Fatal("password used while off")
	}
	if _, err := dialT(t, w.opts(base("  NumberOfPasswordPrompts 0\n"), s)); err == nil {
		t.Fatal("password asked with no prompts")
	}
}

func TestBatchModeAsksNothing(t *testing.T) {
	w := newWorld(t)
	s := newServer(t, sshtest.Opts{Password: "x"})
	w.ans = []string{"x"}
	if _, err := dialT(t, w.opts(base("  BatchMode yes\n"), s)); err == nil || len(w.asks) != 0 {
		t.Fatalf("err %v, asked %q", err, w.asks)
	}
}

func TestKeyboardInteractive(t *testing.T) {
	w := newWorld(t)
	s := newServer(t, sshtest.Opts{Kbd: []string{"one", "two"}})
	w.ans = []string{"one", "two"}
	o := w.opts(base(""), s)
	o.Password = func() (string, bool) { return "vault", true } // not for two questions
	mustDial(t, o)
	if len(w.asks) != 2 || w.asks[0] != "Login\nAnswer them\nQuestion 1: " || w.asks[1] != "Question 2: " || w.echos[0] || !w.echos[1] {
		t.Fatalf("asked %q %v", w.asks, w.echos)
	}
}

func TestKeyboardInteractiveOneTry(t *testing.T) {
	// Without a Password hook, the lone question is asked straight away.
	w := newWorld(t)
	s := newServer(t, sshtest.Opts{Kbd: []string{"one"}})
	w.ans = []string{"one"}
	mustDial(t, w.opts(base(""), s))
	if l := s.authLog(); !slices.Equal(l, []string{"keyboard-interactive:ok"}) {
		t.Fatalf("auths %q", l)
	}
}

func TestKeyboardInteractiveTakesPassword(t *testing.T) {
	w := newWorld(t)
	s := newServer(t, sshtest.Opts{Kbd: []string{"vault"}})
	o := w.opts(base(""), s)
	o.Password = func() (string, bool) { return "vault", true }
	mustDial(t, o)
	if len(w.asks) != 0 {
		t.Fatalf("asked %q", w.asks)
	}
}

func TestKeyboardInteractiveOff(t *testing.T) {
	w := newWorld(t)
	s := newServer(t, sshtest.Opts{Kbd: []string{"one"}})
	w.ans = []string{"one"}
	if _, err := dialT(t, w.opts(base("  KbdInteractiveAuthentication no\n"), s)); err == nil {
		t.Fatal("keyboard-interactive used while off")
	}
}

func TestPubkeyFile(t *testing.T) {
	w := newWorld(t)
	k := writeKey(t, w.path("id"), "")
	s := newServer(t, sshtest.Opts{Keys: []ssh.PublicKey{k.PublicKey()}})
	mustDial(t, w.opts(base(""), s))
	if l := s.authLog(); !slices.Equal(l, []string{"publickey:ok"}) {
		t.Fatalf("auths %q", l)
	}
}

func TestPubkeyOff(t *testing.T) {
	w := newWorld(t)
	k := writeKey(t, w.path("id"), "")
	s := newServer(t, sshtest.Opts{Keys: []ssh.PublicKey{k.PublicKey()}})
	if _, err := dialT(t, w.opts(base("  PubkeyAuthentication no\n"), s)); err == nil {
		t.Fatal("key used while off")
	}
}

func TestPassphraseKey(t *testing.T) {
	w := newWorld(t)
	k := writeKey(t, w.path("id"), "secret")
	s := newServer(t, sshtest.Opts{Keys: []ssh.PublicKey{k.PublicKey()}})
	w.ans = []string{"bad", "secret"}
	mustDial(t, w.opts(base("  NumberOfPasswordPrompts 2\n"), s))
	if len(w.asks) != 2 || w.asks[0] != "Enter passphrase for key '"+w.path("id")+"': " || w.echos[0] {
		t.Fatalf("asked %q", w.asks)
	}
}

func TestPassphraseNotAskedForUnwantedKey(t *testing.T) {
	w := newWorld(t)
	writeKey(t, w.path("id"), "secret")
	s := newServer(t, sshtest.Opts{Password: "pw"})
	w.ans = []string{"pw"}
	mustDial(t, w.opts(base(""), s))
	if len(w.asks) != 1 || !strings.HasSuffix(w.asks[0], "password: ") {
		t.Fatalf("asked %q", w.asks)
	}
}

func TestPassphraseEmptyGivesUp(t *testing.T) {
	w := newWorld(t)
	k := writeKey(t, w.path("id"), "secret")
	s := newServer(t, sshtest.Opts{Keys: []ssh.PublicKey{k.PublicKey()}})
	w.ans = []string{""}
	if _, err := dialT(t, w.opts(base(""), s)); err == nil || !strings.Contains(err.Error(), "no passphrase") {
		t.Fatalf("err %v", err)
	}
}

func TestEncryptedKeyWithoutPub(t *testing.T) {
	w := newWorld(t)
	k := writeKey(t, w.path("id"), "secret")
	// An encrypted key's public half is in the file (openssh format), so
	// a missing .pub costs no question until the server takes it.
	write(t, w.path("id.pub"), "garbage")
	s := newServer(t, sshtest.Opts{Keys: []ssh.PublicKey{k.PublicKey()}})
	w.ans = []string{"secret"}
	mustDial(t, w.opts(base(""), s))
	s = newServer(t, sshtest.Opts{Password: "pw"})
	w.asks, w.ans = nil, []string{"pw"}
	mustDial(t, w.opts(base(""), s))
	if len(w.asks) != 1 || !strings.HasSuffix(w.asks[0], "password: ") {
		t.Fatalf("asked %q", w.asks)
	}
}

// agentSock serves a keyring holding keys on a Unix socket.
func agentSock(t *testing.T, keys ...any) (string, agent.Agent) {
	t.Helper()
	noUnix(t)
	kr := agent.NewKeyring()
	for _, k := range keys {
		if err := kr.Add(agent.AddedKey{PrivateKey: k}); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(t.TempDir(), "agent")
	ln, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go agent.ServeAgent(kr, c)
		}
	}()
	return p, kr
}

func genRaw(t *testing.T) (ed25519.PrivateKey, ssh.PublicKey) {
	t.Helper()
	_, k, _ := ed25519.GenerateKey(rand.Reader)
	s, _ := ssh.NewSignerFromKey(k)
	return k, s.PublicKey()
}

var rsaOnce = sync.OnceValue(func() *rsa.PrivateKey {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	return k
})

// genRSA is the run's one RSA key (they are slow to make).
func genRSA(t *testing.T) ssh.Signer {
	s, err := ssh.NewSignerFromKey(rsaOnce())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func writeRSA(t *testing.T, path string) ssh.Signer {
	b, err := ssh.MarshalPrivateKey(rsaOnce(), "")
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(pem.EncodeToMemory(b)))
	return genRSA(t)
}

func TestAgentKey(t *testing.T) {
	w := newWorld(t)
	raw, pub := genRaw(t)
	sock, _ := agentSock(t, raw)
	s := newServer(t, sshtest.Opts{Keys: []ssh.PublicKey{pub}})
	mustDial(t, w.opts(base("  IdentityAgent "+sock+"\n"), s))
	if w.logged("Error reading") {
		t.Fatalf("logs %q", w.logs)
	}
}

func TestAgentFromEnv(t *testing.T) {
	w := newWorld(t)
	raw, pub := genRaw(t)
	sock, _ := agentSock(t, raw)
	t.Setenv("SSH_AUTH_SOCK", sock)
	s := newServer(t, sshtest.Opts{Keys: []ssh.PublicKey{pub}})
	mustDial(t, w.opts(strings.Replace(base(""), "  IdentityAgent none\n", "", 1), s))
}

func TestAgentUnreachable(t *testing.T) {
	w := newWorld(t)
	s := newServer(t, sshtest.Opts{Password: "pw"})
	w.ans = []string{"pw"}
	mustDial(t, w.opts(base("  IdentityAgent %HOME/nothing\n"), s))
	if !w.logged("Error connecting to agent") {
		t.Fatal("no log")
	}
}

func TestIdentitiesOnly(t *testing.T) {
	w := newWorld(t)
	raw, pub := genRaw(t)
	sock, _ := agentSock(t, raw)
	writeKey(t, w.path("id"), "")
	s := newServer(t, sshtest.Opts{Keys: []ssh.PublicKey{pub}})
	if _, err := dialT(t, w.opts(base("  IdentityAgent "+sock+"\n  IdentitiesOnly yes\n"), s)); err == nil {
		t.Fatal("agent key used with IdentitiesOnly")
	}
	// The config's key held by the agent is used through it.
	write(t, w.path("id.pub"), string(ssh.MarshalAuthorizedKey(pub)))
	mustDial(t, w.opts(base("  IdentityAgent "+sock+"\n  IdentitiesOnly yes\n"), s))
}

func TestPubOnlyThroughAgent(t *testing.T) {
	w := newWorld(t)
	raw, pub := genRaw(t)
	sock, _ := agentSock(t, raw)
	write(t, w.path("id.pub"), string(ssh.MarshalAuthorizedKey(pub)))
	s := newServer(t, sshtest.Opts{Keys: []ssh.PublicKey{pub}})
	mustDial(t, w.opts(base("  IdentityAgent "+sock+"\n  IdentitiesOnly yes\n"), s))
}

func TestAgentOrder(t *testing.T) {
	// The config's key (in the agent) is offered before the agent's others.
	w := newWorld(t)
	raw1, pub1 := genRaw(t)
	raw2, pub2 := genRaw(t)
	sock, _ := agentSock(t, raw2, raw1)
	write(t, w.path("id.pub"), string(ssh.MarshalAuthorizedKey(pub1)))
	var offered []string
	s := newServer(t, sshtest.Opts{Config: func(c *ssh.ServerConfig) {
		c.PublicKeyCallback = func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			offered = append(offered, ssh.FingerprintSHA256(k))
			return nil, errNoPrompt
		}
	}})
	dialT(t, w.opts(base("  IdentityAgent "+sock+"\n"), s))
	if len(offered) != 2 || offered[0] != ssh.FingerprintSHA256(pub1) || offered[1] != ssh.FingerprintSHA256(pub2) {
		t.Fatalf("offered %q", offered)
	}
}

func TestAddKeysToAgent(t *testing.T) {
	w := newWorld(t)
	k := writeKey(t, w.path("id"), "secret")
	sock, kr := agentSock(t)
	s := newServer(t, sshtest.Opts{Keys: []ssh.PublicKey{k.PublicKey()}})
	w.ans = []string{"secret"}
	mustDial(t, w.opts(base("  IdentityAgent "+sock+"\n  AddKeysToAgent 1h\n"), s))
	if l, _ := kr.List(); len(l) != 1 || !keyEqual(l[0], k.PublicKey()) || !w.logged("Identity added") {
		t.Fatalf("agent holds %v", l)
	}
	// Not by default; nor without an agent (it just logs in).
	sock, kr = agentSock(t)
	w.ans = []string{"secret"}
	mustDial(t, w.opts(base("  IdentityAgent "+sock+"\n"), s))
	if l, _ := kr.List(); len(l) != 0 {
		t.Fatalf("agent holds %v", l)
	}
	w.ans = []string{"secret"}
	mustDial(t, w.opts(base("  AddKeysToAgent yes\n"), s))
}

func TestAddKeysToAgentAsk(t *testing.T) {
	for _, ans := range []string{"yes", "no"} {
		w := newWorld(t)
		k := writeKey(t, w.path("id"), "")
		sock, kr := agentSock(t)
		s := newServer(t, sshtest.Opts{Keys: []ssh.PublicKey{k.PublicKey()}})
		w.ans = []string{ans}
		mustDial(t, w.opts(base("  IdentityAgent "+sock+"\n  AddKeysToAgent ask\n"), s))
		if l, _ := kr.List(); len(l) != map[string]int{"yes": 1, "no": 0}[ans] {
			t.Fatalf("%s: agent holds %v", ans, l)
		}
	}
}

func TestAddKeysToAgentConfirm(t *testing.T) {
	// The keyring refuses confirm constraints: the key still logs in, and
	// the refusal is logged.
	w := newWorld(t)
	k := writeKey(t, w.path("id"), "")
	sock, _ := agentSock(t)
	s := newServer(t, sshtest.Opts{Keys: []ssh.PublicKey{k.PublicKey()}})
	mustDial(t, w.opts(base("  IdentityAgent "+sock+"\n  AddKeysToAgent confirm 60\n"), s))
	if !w.logged("could not add identity") || w.logged("Identity added") {
		t.Fatalf("logs %q", w.logs)
	}
}

func TestUserCertificateFile(t *testing.T) {
	w := newWorld(t)
	ca := newSigner(t)
	k := writeKey(t, w.path("id"), "")
	c := cert(t, ca, k.PublicKey(), ssh.UserCert, "me")
	write(t, w.path("cert"), string(ssh.MarshalAuthorizedKey(c)))
	s := newServer(t, sshtest.Opts{CA: ca.PublicKey()})
	mustDial(t, w.opts(base("  CertificateFile %HOME/cert\n"), s))
}

func TestUserCertificateNextToKey(t *testing.T) {
	w := newWorld(t)
	ca := newSigner(t)
	k := writeKey(t, w.path("id"), "")
	write(t, w.path("id-cert.pub"), string(ssh.MarshalAuthorizedKey(cert(t, ca, k.PublicKey(), ssh.UserCert, "me"))))
	s := newServer(t, sshtest.Opts{CA: ca.PublicKey()})
	mustDial(t, w.opts(base(""), s))
}

func TestUserCertificateThroughAgent(t *testing.T) {
	w := newWorld(t)
	ca := newSigner(t)
	raw, pub := genRaw(t)
	sock, _ := agentSock(t, raw)
	write(t, w.path("cert"), string(ssh.MarshalAuthorizedKey(cert(t, ca, pub, ssh.UserCert, "me"))))
	s := newServer(t, sshtest.Opts{CA: ca.PublicKey()})
	mustDial(t, w.opts(base("  IdentityAgent "+sock+"\n  IdentitiesOnly yes\n  CertificateFile %HOME/cert\n"), s))
}

func TestHostCertAsUserCertSkipped(t *testing.T) {
	w := newWorld(t)
	ca := newSigner(t)
	k := writeKey(t, w.path("id"), "")
	write(t, w.path("cert"), string(ssh.MarshalAuthorizedKey(cert(t, ca, k.PublicKey(), ssh.HostCert, "me"))))
	s := newServer(t, sshtest.Opts{CA: ca.PublicKey()})
	if _, err := dialT(t, w.opts(base("  CertificateFile %HOME/cert\n"), s)); err == nil {
		t.Fatal("host certificate used to log in")
	}
	if !w.logged("no user certificate") {
		t.Fatal("not logged")
	}
}

func TestPreferredAuthenticationsOrder(t *testing.T) {
	for _, tc := range []struct{ prefs, first string }{
		{"password,publickey", "password:fail"},
		{"publickey,password", "publickey:fail"},
	} {
		w := newWorld(t)
		writeKey(t, w.path("id"), "")
		s := newServer(t, sshtest.Opts{Password: "pw", Keys: []ssh.PublicKey{newSigner(t).PublicKey()}})
		w.ans = []string{"bad", "bad", "bad"}
		dialT(t, w.opts(base("  PreferredAuthentications "+tc.prefs+"\n"), s))
		if l := s.authLog(); len(l) == 0 || l[0] != tc.first {
			t.Fatalf("%s: auths %q", tc.prefs, l)
		}
	}
}

func TestPubkeyAcceptedAlgorithms(t *testing.T) {
	w := newWorld(t)
	k := writeKey(t, w.path("id"), "")
	s := newServer(t, sshtest.Opts{Keys: []ssh.PublicKey{k.PublicKey()}})
	if _, err := dialT(t, w.opts(base("  PubkeyAcceptedAlgorithms ecdsa-sha2-nistp256\n"), s)); err == nil {
		t.Fatal("ed25519 key offered")
	}
	if !w.logged("not in PubkeyAcceptedAlgorithms") {
		t.Fatal("not logged")
	}
}

func TestSignAlgorithms(t *testing.T) {
	rsaKey := genRSA(t)
	got := signAlgorithms(rsaKey.PublicKey(), []string{"rsa-sha2-256", "ssh-rsa", "ssh-ed25519"})
	if !slices.Equal(got, []string{"rsa-sha2-256", "ssh-rsa"}) {
		t.Fatalf("rsa: %q", got)
	}
	c := &ssh.Certificate{Key: rsaKey.PublicKey(), Signature: &ssh.Signature{}}
	got = signAlgorithms(c, []string{"rsa-sha2-512-cert-v01@openssh.com", "rsa-sha2-256"})
	if !slices.Equal(got, []string{"rsa-sha2-512"}) {
		t.Fatalf("rsa cert: %q", got)
	}
	if got := signAlgorithms(newSigner(t).PublicKey(), []string{"rsa-sha2-256"}); got != nil {
		t.Fatalf("ed25519: %q", got)
	}
}

func TestRequiredRSASize(t *testing.T) {
	w := newWorld(t)
	k := writeRSA(t, w.path("id"))
	s := newServer(t, sshtest.Opts{Keys: []ssh.PublicKey{k.PublicKey()}})
	mustDial(t, w.opts(base(""), s))
	if _, err := dialT(t, w.opts(base("  RequiredRSASize 4096\n"), s)); err == nil || !w.logged("RequiredRSASize") {
		t.Fatalf("short RSA key used: %v", err)
	}
}

func TestUnsupportedExplained(t *testing.T) {
	w := newWorld(t)
	s := newServer(t, sshtest.Opts{GSSAPIOnly: true})
	_, err := dialT(t, w.opts(base(""), s))
	if err == nil || !strings.Contains(err.Error(), "no method desk can use") {
		t.Fatalf("err %v", err)
	}
	_, err = dialT(t, w.opts(base("  HostbasedAuthentication yes\n  PKCS11Provider /lib/p11.so\n  SecurityKeyProvider /lib/sk.so\n"), s))
	if err == nil || !strings.Contains(err.Error(), "may need host-based authentication or PKCS11Provider keys or SecurityKeyProvider /lib/sk.so, which desk doesn't support") {
		t.Fatalf("err %v", err)
	}
}

func TestSecurityKeyFileUnsupported(t *testing.T) {
	w := newWorld(t)
	sk, err := ssh.ParsePublicKey(ssh.Marshal(struct {
		Name, Key, App string
	}{"sk-ssh-ed25519@openssh.com", strings.Repeat("k", 32), "ssh:"}))
	if err != nil {
		t.Fatal(err)
	}
	write(t, w.path("id.pub"), string(ssh.MarshalAuthorizedKey(sk)))
	write(t, w.path("id"), "a key handle x/crypto can't read")
	s := newServer(t, sshtest.Opts{Keys: []ssh.PublicKey{newSigner(t).PublicKey()}})
	_, err = dialT(t, w.opts(base(""), s))
	if err == nil || !strings.Contains(err.Error(), "a security key ("+w.path("id")+") outside an agent") {
		t.Fatalf("err %v", err)
	}
}

func TestExplainPassesOthers(t *testing.T) {
	a := &auth{}
	if err := a.explain(nil); err != nil {
		t.Fatal(err)
	}
	e := errNoPrompt
	if err := a.explain(e); err != e {
		t.Fatal(err)
	}
	a.unsupported = []string{"x"}
	if err := a.explain(e); err != e {
		t.Fatal(err)
	}
}
