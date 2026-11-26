package orgs

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPhone: settings.toml's [phone] reads its token flat beside chat_id,
// and pairing keeps the rest of the settings.
func TestPhone(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	os.MkdirAll(filepath.Dir(SettingsFile()), 0o700)
	os.WriteFile(SettingsFile(), []byte("me = \"a dev\"\n[phone]\ntoken_env = \"TG_TOKEN\"\nalways = true\n"), 0o600)
	t.Setenv("TG_TOKEN", "123:abc")
	p := PhoneSettings()
	if tok, err := p.Get(); err != nil || tok != "123:abc" || !p.Always || p.Chat != 0 {
		t.Fatalf("%+v %q %v", p, tok, err)
	}
	if err := SetPhoneChat(42); err != nil {
		t.Fatal(err)
	}
	if p := PhoneSettings(); p.Chat != 42 || p.TokenEnv != "TG_TOKEN" || Me() != "a dev" {
		t.Fatalf("after pairing %+v %q", p, Me())
	}
	if err := SavePhone(Phone{Secret: Secret{TokenCmd: "pass tg"}, Always: true}); err != nil {
		t.Fatal(err)
	}
	if err := SaveTelemetry(Sentry{DSN: "https://k@o1.ingest.sentry.io/1"}); err != nil {
		t.Fatal(err)
	}
	if p, nr := PhoneSettings(), Telemetry(); p.TokenCmd != "pass tg" || p.Chat != 0 || nr.DSN != "https://k@o1.ingest.sentry.io/1" || Me() != "a dev" {
		t.Fatalf("%+v %+v", p, nr)
	}
}
