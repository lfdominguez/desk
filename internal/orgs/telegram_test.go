package orgs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTelegram: an older [phone] moves into [telegram] on the next save,
// whichever saves; [telegram] keeps the account, folder and each org's
// mirror and group; the folder is "Desk Control" unless named; the
// session lives in desk's state folder.
func TestTelegram(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", "/state")
	os.MkdirAll(filepath.Dir(SettingsFile()), 0o700)
	os.WriteFile(SettingsFile(), []byte("[phone]\ntoken_env = \"TG\"\nchat_id = 42\n"), 0o600)
	if p := PhoneSettings(); p.TokenEnv != "TG" || p.Chat != 42 {
		t.Fatalf("%+v", p)
	}
	if err := SaveKeys(map[string]string{"a": "b"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(SettingsFile())
	if s := string(b); strings.Contains(s, "[phone]") || !strings.Contains(s, "[telegram]") || !strings.Contains(s, "chat_id = 42") {
		t.Fatalf("not moved:\n%s", s)
	}
	tg := TelegramSettings()
	if tg.FolderName() != "Desk Control" || tg.TokenEnv != "TG" {
		t.Fatalf("%+v", tg)
	}
	tg.APIID, tg.APIHash, tg.Number, tg.Folder = 123, Secret{TokenEnv: "TG_HASH"}, "+10000000000", "Desk"
	if err := SaveTelegram(tg); err != nil {
		t.Fatal(err)
	}
	if err := SetTelegramOrg("Acme", TelegramOrg{Mirror: MirrorFull, Chat: -1000000000055, Channel: 55, Access: 550}); err != nil {
		t.Fatal(err)
	}
	SetTelegramOrg("Globex", TelegramOrg{Mirror: MirrorNotify})
	tg = TelegramSettings()
	if tg.APIID != 123 || tg.APIHash.TokenEnv != "TG_HASH" || tg.FolderName() != "Desk" || tg.Chat != 42 ||
		tg.Orgs["Acme"].Access != 550 || tg.Orgs["Globex"].Mirror != MirrorNotify || len(Mirrors) != 4 {
		t.Fatalf("%+v", tg)
	}
	if TelegramSession() != filepath.Join("/state", "desk", "telegram.session") {
		t.Fatal(TelegramSession())
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if s := TelegramSession(); !strings.HasSuffix(s, filepath.Join("desk", "telegram.session")) {
		t.Fatal(s)
	}
}
