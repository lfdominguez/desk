package orgs

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTelemetry: settings.toml's [sentry], empty without one.
func TestTelemetry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := Telemetry(); got != (Sentry{}) {
		t.Fatalf("no settings: %+v", got)
	}
	os.MkdirAll(filepath.Dir(SettingsFile()), 0o700)
	os.WriteFile(SettingsFile(), []byte("[sentry]\ndsn = \"https://k@o1.ingest.sentry.io/1\"\n"), 0o600)
	if got := Telemetry(); got != (Sentry{DSN: "https://k@o1.ingest.sentry.io/1"}) {
		t.Fatalf("Telemetry = %+v", got)
	}
}
