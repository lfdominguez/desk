package orgs

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTelemetry: settings.toml's [newrelic], empty without one.
func TestTelemetry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := Telemetry(); got != (NewRelic{}) {
		t.Fatalf("no settings: %+v", got)
	}
	os.MkdirAll(filepath.Dir(SettingsFile()), 0o700)
	os.WriteFile(SettingsFile(), []byte("[newrelic]\nlicense_key = \"k\"\napp_name = \"desk-test\"\n"), 0o600)
	if got := Telemetry(); got != (NewRelic{LicenseKey: "k", AppName: "desk-test"}) {
		t.Fatalf("Telemetry = %+v", got)
	}
}
