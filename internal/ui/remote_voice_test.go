package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/tgbot"
	"github.com/lfdominguez/desk/internal/tgsetup"
)

// TestRemoteVoice: a voice note reaches the card's Claude as the words
// Telegram's transcription heard, asked as the user's account; without
// the account logged in, in a group desk didn't make, or with nothing
// heard, the topic says why. In the bot's chat it's named by the bot.
func TestRemoteVoice(t *testing.T) {
	h := newHarness(t)
	f := remoteOn(t, h, "full")
	f.Me = map[string]any{"id": 1, "is_bot": true, "username": "desk_test_bot"}
	c := h.u.b.Add(h.u.b.Column("In progress"), kanban.Card{Title: "Fix login", Org: "Acme", Status: kanban.StatusWorking})
	fk := &fakeClaude{}
	h.u.procs[c.ID] = fk
	h.u.say(c.ID, "you", "fix the login")
	h.u.save()
	th := h.topicOf(c.ID).Thread

	var got []tgsetup.Voice
	heard, fail := "run the tests", error(nil)
	replace(t, &tgTranscribe, func(_ context.Context, a tgsetup.Account, v tgsetup.Voice) (string, error) {
		got = append(got, v)
		return heard, fail
	})
	voice := func() tgbot.Update {
		m := groupMsg(42, th, "")
		m.Message.Voice, m.Message.Date = &tgbot.File{ID: "v"}, 100
		return m
	}
	said := func(what, want string) {
		t.Helper()
		h.u.phoneUpdate(voice())
		h.pumpUntil(what, func() bool {
			p := f.Last("sendMessage")
			return p != nil && strings.Contains(p["text"].(string), want)
		})
	}

	said("not logged in", "Couldn't transcribe the voice note: save the app's api_id")
	acct := "[telegram]\ntoken = \"T0K\"\nchat_id = 42\napi_id = 5\n[telegram.api_hash]\ntoken = \"h\"\n[telegram.orgs.Acme]\nmirror = \"full\"\nchat_id = -100555\n"
	settingsFile(t, acct)
	said("no session", "log your Telegram account in")
	os.MkdirAll(filepath.Dir(orgs.TelegramSession()), 0o700)
	os.WriteFile(orgs.TelegramSession(), nil, 0o600)
	said("linked by hand", "names only the groups it made")

	settingsFile(t, acct+"channel = 555\naccess_hash = 5550\n")
	n := len(fk.prompts)
	h.u.phoneUpdate(voice())
	h.pumpUntil("the words sent", func() bool { return len(fk.prompts) == n+1 })
	if p := fk.prompts[n]; p != "🎙 run the tests" {
		t.Fatalf("prompt %q", p)
	}
	if v := got[len(got)-1]; v.Chat.Channel != 555 || v.Chat.Access != 5550 || v.Msg != 9 || v.Bot != "" {
		t.Fatalf("voice %+v", v)
	}
	heard = " "
	said("nothing heard", "heard no words")
	heard, fail = "", errors.New("transcribe: PREMIUM_ACCOUNT_REQUIRED\nmore")
	said("refused", "PREMIUM_ACCOUNT_REQUIRED")

	n = len(got)
	d := dm("")
	d.Message.Voice, d.Message.Date = &tgbot.File{ID: "v"}, 200
	h.u.phoneUpdate(d)
	h.pumpUntil("asked in the bot's chat", func() bool { return len(got) > n })
	if v := got[n]; v.Bot != "desk_test_bot" || v.Date != 200 || v.Chat.Channel != 0 {
		t.Fatalf("bot's chat voice %+v", v)
	}
}
