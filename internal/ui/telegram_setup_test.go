package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/tgbot"
	"github.com/lfdominguez/desk/internal/tgbot/tgfake"
	"github.com/lfdominguez/desk/internal/tgsetup"
)

// tgFake points the bot's calls at a fake Bot API.
func tgFake(t *testing.T) *tgfake.Server {
	f := tgfake.New()
	t.Cleanup(f.Close)
	old := tgbot.API
	tgbot.API = f.URL
	t.Cleanup(func() { tgbot.API = old })
	return f
}

func settingsFile(t *testing.T, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(orgs.SettingsFile()), 0o700)
	if err := os.WriteFile(orgs.SettingsFile(), []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// replace sets *p to v for the test.
func replace[T any](t *testing.T, p *T, v T) {
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// TestTelegramPart: the part shows the pairing, the account and each
// organization's mirror; its fields save into [telegram], an api_id that
// isn't a number and a folder name over 12 characters refused on the part.
func TestTelegramPart(t *testing.T) {
	h := newHarness(t)
	settingsFile(t, "[telegram]\ntoken = \"T0K\"\nchat_id = 42\n")
	h.u.openConfig(cfgFolders)
	h.u.cfg.set.sub[cfgFolders] = "telegram"
	sz, all, w := cfgHarness(h)
	h.draw(sz, w)
	for _, s := range []string{"Paired with Telegram chat 42: the General chat", "Not logged in", "Acme", "Globex", "no group yet"} {
		if !h.has(s) {
			t.Fatalf("%q not shown", s)
		}
	}
	d := h.u.cfg
	a := d.tgacct
	a.ed("api_id").SetText("abc")
	d.set.sub[cfgFolders] = "general"
	h.u.saveConfig()
	if !strings.Contains(d.err, "api_id is a number") || d.page() != "telegram" {
		t.Fatalf("err %q on %q", d.err, d.page())
	}
	a.ed("api_id").SetText("123")
	a.ed("folder").SetText("Desk Control (mine)")
	d.err = ""
	h.u.saveConfig()
	if !strings.Contains(d.err, "12 characters") {
		t.Fatalf("err %q", d.err)
	}
	a.ed("folder").SetText("Desk")
	a.src["api_hash"] = srcFile
	a.ed("api_hash.token").SetText("abc123")
	a.ed("phone_number").SetText("+10000000000")
	h.draw(sz, w)
	h.clickIn(sz, all, w, "tgMirrorAcme3") // Full
	h.clickIn(sz, all, w, "tgMirrorGlobex1")
	d.err = ""
	h.u.saveConfig()
	tg := orgs.TelegramSettings()
	if tg.APIID != 123 || tg.APIHash.Token != "abc123" || tg.Number != "+10000000000" || tg.FolderName() != "Desk" || tg.Token != "T0K" || tg.Chat != 42 ||
		tg.Orgs["Acme"].Mirror != orgs.MirrorFull || tg.Orgs["Globex"].Mirror != orgs.MirrorNotify {
		t.Fatalf("%+v", tg)
	}
	h.u.openConfig(cfgFolders) // reads back as saved
	if n := h.u.cfg.changes(); n != 0 || h.u.cfg.tgacct.text("mirror.Acme") != orgs.MirrorFull {
		t.Fatalf("reopened: %d changes", n)
	}
	h.u.orgList = nil
	h.u.cfg.set.sub[cfgFolders] = "telegram"
	h.draw(sz, w)
	if !h.has("No organizations found") {
		t.Fatal("no orgs not said")
	}
}

// TestTelegramLogin: Log in asks the code, then the 2FA password (masked),
// in place, and hands each to the login; Cancel stops it; a failure says
// why; Log out ends the session. Without a saved account it says what to
// save.
func TestTelegramLogin(t *testing.T) {
	h := newHarness(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	settingsFile(t, "[telegram]\ntoken = \"T0K\"\n")
	h.u.openConfig(cfgFolders)
	h.u.cfg.set.sub[cfgFolders] = "telegram"
	sz, all, w := cfgHarness(h)
	h.draw(sz, w)
	h.clickIn(sz, all, w, "tgLogin")
	if !strings.Contains(h.u.tg.note, "api_id") || !h.u.tg.bad {
		t.Fatalf("note %q", h.u.tg.note)
	}
	settingsFile(t, "[telegram]\ntoken = \"T0K\"\napi_id = 5\n[telegram.api_hash]\ntoken = \"h\"\n")
	h.clickIn(sz, all, w, "tgLogin")
	if !strings.Contains(h.u.tg.note, "phone number") {
		t.Fatalf("note %q", h.u.tg.note)
	}
	settingsFile(t, "[telegram]\ntoken = \"T0K\"\napi_id = 5\nphone_number = \"+1\"\n[telegram.api_hash]\ntoken = \"h\"\n")

	var got []string
	replace(t, &tgAccountLogin, func(ctx context.Context, a tgsetup.Account, phone string, ask tgsetup.Asker) error {
		if a.ID != 5 || a.Hash != "h" || phone != "+1" {
			return errors.New("wrong account")
		}
		code, err := ask.Code(ctx)
		if err != nil {
			return err
		}
		pw, err := ask.Password(ctx)
		if err != nil {
			return err
		}
		got = append(got, code, pw)
		os.MkdirAll(filepath.Dir(a.Session), 0o700)
		return os.WriteFile(a.Session, nil, 0o600)
	})
	h.clickIn(sz, all, w, "tgLogin")
	h.pumpUntil("the code asked", func() bool { return h.u.tg.login != nil && h.u.tg.login.step == "code" })
	h.draw(sz, w)
	if !h.has("The code Telegram just sent you") {
		t.Fatal("code not asked")
	}
	h.clickIn(sz, all, w, "tgSend") // nothing typed: nothing sent
	if h.u.tg.login.step != "code" {
		t.Fatal("sent empty")
	}
	h.u.tg.login.ed.SetText(" 12345 ")
	h.clickIn(sz, all, w, "tgSend")
	h.pumpUntil("the password asked", func() bool { return h.u.tg.login != nil && h.u.tg.login.step == "password" })
	if h.u.tg.login.ed.Mask != '•' {
		t.Fatal("password not masked")
	}
	h.draw(sz, w)
	h.u.tg.login.ed.SetText("pw")
	h.clickIn(sz, all, w, "tgSend")
	h.pumpUntil("logged in", func() bool { return h.u.tg.login == nil })
	if strings.Join(got, ",") != "12345,pw" || !strings.Contains(h.u.tg.note, "Logged in") {
		t.Fatalf("%v %q", got, h.u.tg.note)
	}
	h.draw(sz, w)
	if !h.has("Logged in.") {
		t.Fatal("logged in not shown")
	}

	replace(t, &tgAccountLogout, func(ctx context.Context, a tgsetup.Account) error { return os.Remove(a.Session) })
	h.clickIn(sz, all, w, "tgLogout")
	h.pumpUntil("logged out", func() bool { return !h.u.tg.busy })
	if h.u.tg.note != "Logged out." {
		t.Fatal(h.u.tg.note)
	}
	replace(t, &tgAccountLogout, func(context.Context, tgsetup.Account) error { return errors.New("AUTH_KEY_UNREGISTERED") })
	h.u.tgLogout()
	h.pumpUntil("logged out", func() bool { return !h.u.tg.busy })
	if !strings.Contains(h.u.tg.note, "Telegram said: AUTH_KEY_UNREGISTERED") {
		t.Fatal(h.u.tg.note)
	}

	h.u.tgLoginStart() // canceled while it waits on the code
	h.pumpUntil("the code asked", func() bool { return h.u.tg.login != nil && h.u.tg.login.step == "code" })
	h.draw(sz, w)
	h.clickIn(sz, all, w, "tgCancel")
	h.pumpUntil("canceled", func() bool { return h.u.tg.login == nil })
	if h.u.tg.note != "Login canceled." {
		t.Fatal(h.u.tg.note)
	}
	release := make(chan struct{})
	replace(t, &tgAccountLogin, func(ctx context.Context, a tgsetup.Account, phone string, ask tgsetup.Asker) error {
		<-release
		return errors.New("PHONE_NUMBER_INVALID")
	})
	h.u.tgLoginStart()
	h.draw(sz, w)
	if !h.has("Talking to Telegram") {
		t.Fatal("busy not shown")
	}
	close(release)
	h.pumpUntil("failed", func() bool { return h.u.tg.login == nil })
	if h.u.tg.note != "Login failed: PHONE_NUMBER_INVALID" || !h.u.tg.bad {
		t.Fatal(h.u.tg.note)
	}
	settingsFile(t, "[telegram]\nphone_number = \"+1\"\n")
	h.u.tgLogout() // no app saved: the session goes all the same
	h.pumpUntil("logged out", func() bool { return !h.u.tg.busy })
}

// TestTelegramSetup: Set up makes each mirrored organization's group
// (once), then the folder with the groups desk made; a group linked by
// hand is counted apart; without topics in the bot's chats it says how to
// turn them on; each missing piece is named before anything starts, and a
// step that fails keeps the groups already made.
func TestTelegramSetup(t *testing.T) {
	h := newHarness(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := tgFake(t)
	base := "[telegram]\ntoken = \"T0K\"\napi_id = 5\nfolder = \"Desk\"\n[telegram.api_hash]\ntoken = \"h\"\n[telegram.orgs.Acme]\nmirror = \"full\"\n[telegram.orgs.Globex]\nmirror = \"notify\"\nchat_id = -100777\n"
	settingsFile(t, "[telegram]\napi_id = 5\n[telegram.api_hash]\ntoken = \"h\"\n")
	h.u.tgSetup()
	if !strings.Contains(h.u.tg.note, "bot's token") {
		t.Fatal(h.u.tg.note)
	}
	settingsFile(t, base)
	h.u.tgSetup()
	if !strings.Contains(h.u.tg.note, "log in first") {
		t.Fatal(h.u.tg.note)
	}
	settingsFile(t, "[telegram]\ntoken = \"T0K\"\n")
	h.u.tgSetup()
	if !strings.Contains(h.u.tg.note, "api_id") {
		t.Fatal(h.u.tg.note)
	}
	settingsFile(t, "[telegram]\ntoken = \"T0K\"\napi_id = 5\n")
	h.u.tgSetup()
	if !strings.Contains(h.u.tg.note, "api_hash") {
		t.Fatal(h.u.tg.note)
	}
	settingsFile(t, base)
	os.MkdirAll(filepath.Dir(orgs.TelegramSession()), 0o700)
	os.WriteFile(orgs.TelegramSession(), nil, 0o600)

	forums := 0
	var folded []tgsetup.Chat
	replace(t, &tgForum, func(ctx context.Context, a tgsetup.Account, title, about, bot string) (tgsetup.Chat, error) {
		forums++
		if bot != "desk_test_bot" || title != "Acme" {
			return tgsetup.Chat{}, errors.New("wrong group")
		}
		return tgsetup.Chat{ID: -1000000000055, Channel: 55, Access: 550}, nil
	})
	replace(t, &tgFolder, func(ctx context.Context, a tgsetup.Account, id int, title, bot string, chats []tgsetup.Chat) (int, error) {
		folded = chats
		if title != "Desk" {
			return 0, errors.New("wrong folder")
		}
		return 9, nil
	})
	f.Me["has_topics_enabled"] = false
	h.u.openConfig(cfgFolders)
	h.u.cfg.set.sub[cfgFolders] = "telegram"
	sz, all, w := cfgHarness(h)
	h.draw(sz, w)
	h.clickIn(sz, all, w, "tgSetup")
	h.pumpUntil("set up", func() bool { return !h.u.tg.busy })
	tg := orgs.TelegramSettings()
	if tg.Orgs["Acme"].Channel != 55 || tg.Orgs["Acme"].Mirror != orgs.MirrorFull || tg.FolderID != 9 || len(folded) != 1 || forums != 1 {
		t.Fatalf("%+v %v %d", tg, folded, forums)
	}
	for _, s := range []string{"Desk folder holds @desk_test_bot's chat and 1 groups (1 new)", "1 linked by hand", "@BotFather"} {
		if !strings.Contains(h.u.tg.note, s) {
			t.Fatalf("note %q lacks %q", h.u.tg.note, s)
		}
	}
	h.draw(sz, w)
	if !h.has("group ready") || !h.has("linked by hand") {
		t.Fatal("group states not shown")
	}
	f.Me["has_topics_enabled"] = true
	h.u.tgSetup() // again: nothing new to make
	h.pumpUntil("set up", func() bool { return !h.u.tg.busy })
	if forums != 1 || strings.Contains(h.u.tg.note, "BotFather") || !strings.Contains(h.u.tg.note, "(0 new)") {
		t.Fatalf("%d %q", forums, h.u.tg.note)
	}

	settingsFile(t, base)
	replace(t, &tgForum, func(context.Context, tgsetup.Account, string, string, string) (tgsetup.Chat, error) {
		return tgsetup.Chat{ID: -1000000000056, Channel: 56, Access: 560}, errors.New("CHAT_ADMIN_REQUIRED")
	})
	h.u.tgSetup()
	h.pumpUntil("stopped", func() bool { return !h.u.tg.busy })
	if !strings.Contains(h.u.tg.note, "Acme's group: CHAT_ADMIN_REQUIRED") || orgs.TelegramSettings().Orgs["Acme"].Channel != 56 {
		t.Fatalf("%q %+v", h.u.tg.note, orgs.TelegramSettings().Orgs["Acme"])
	}
	replace(t, &tgFolder, func(context.Context, tgsetup.Account, int, string, string, []tgsetup.Chat) (int, error) {
		return 0, errors.New("FILTER_INCLUDE_EMPTY")
	})
	h.u.tgSetup()
	h.pumpUntil("stopped", func() bool { return !h.u.tg.busy })
	if !strings.Contains(h.u.tg.note, "FILTER_INCLUDE_EMPTY") {
		t.Fatal(h.u.tg.note)
	}
	f.Fail("getMe", "Unauthorized")
	h.u.tgSetup()
	h.pumpUntil("stopped", func() bool { return !h.u.tg.busy })
	if !strings.Contains(h.u.tg.note, "the bot: telegram getMe: Unauthorized") {
		t.Fatal(h.u.tg.note)
	}
	h.draw(sz, w)
	if !h.has("Set up stopped") {
		t.Fatal("error not shown")
	}
}

// TestPhoneLink: /link Acme from the paired user in a forum group links
// it to Acme, mirrored fully; it says what's wrong otherwise; anyone else's
// /link and a group's /start do nothing.
func TestPhoneLink(t *testing.T) {
	h := newHarness(t)
	f := tgFake(t)
	settingsFile(t, "[telegram]\ntoken = \"T0K\"\nchat_id = 42\n")
	h.u.phone = &phoneState{bot: &tgbot.Bot{Token: "T0K"}, chat: 42, asks: map[string]phoneAsk{}, cards: map[int64]string{}}
	link := func(from int64, text string, forum bool) {
		m := &tgbot.Message{ID: 3, Thread: 1, Text: text, Chat: tgbot.Chat{ID: -100555, Type: "supergroup", IsForum: forum}, From: tgbot.User{ID: from}}
		h.u.phoneUpdate(tgbot.Update{Message: m})
	}
	said := func(s string) {
		t.Helper()
		h.pumpUntil("reply "+s, func() bool {
			p := f.Last("sendMessage")
			return p != nil && strings.Contains(p["text"].(string), s)
		})
	}
	link(42, "/link@desk_test_bot acme", true)
	said("Linked: this group is Acme's")
	if o := orgs.TelegramSettings().Orgs["Acme"]; o.Chat != -100555 || o.Mirror != orgs.MirrorFull || !strings.Contains(h.u.toast, "linked to Acme") {
		t.Fatalf("%+v %q", o, h.u.toast)
	}
	if p := f.Last("sendMessage"); p["chat_id"] == nil || p["message_thread_id"] == nil {
		t.Fatalf("reply not in the group's topic: %v", p)
	}
	link(42, "/link", true)
	said("Say which organization")
	link(42, "/link Nope", true)
	said("no organization called Nope. It has: Acme, Globex")
	link(42, "/link Acme", false)
	said("Turn on Topics")
	n := len(f.Calls("sendMessage"))
	link(7, "/link Acme", true)
	h.u.phoneUpdate(tgbot.Update{Message: &tgbot.Message{Text: "/start", Chat: tgbot.Chat{ID: -100555, Type: "group"}, From: tgbot.User{ID: 42}}})
	h.frame()
	if len(f.Calls("sendMessage")) != n {
		t.Fatal("answered someone else")
	}
	os.Remove(orgs.SettingsFile())
	os.MkdirAll(orgs.SettingsFile()+".tmp", 0o700) // the save's temp file can't be written
	link(42, "/link Globex", true)
	said("Not linked")
}
