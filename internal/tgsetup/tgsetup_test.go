package tgsetup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// fake answers MTProto requests by type; got lists them in order.
type fake struct {
	got    []string
	answer func(req bin.Encoder) (bin.Encoder, error)
}

func (f *fake) Invoke(_ context.Context, in bin.Encoder, out bin.Decoder) error {
	f.got = append(f.got, strings.TrimPrefix(strings.TrimPrefix(typeName(in), "*tg."), "tg."))
	r, err := f.answer(in)
	if err != nil {
		return err
	}
	b := &bin.Buffer{}
	if err := r.Encode(b); err != nil {
		return err
	}
	return out.Decode(b)
}

func typeName(v any) string {
	type named interface{ TypeName() string }
	if n, ok := v.(named); ok {
		return n.TypeName()
	}
	return "?"
}

// use makes run call f with the fake instead of connecting.
func use(t *testing.T, f *fake) Account {
	old := run
	run = func(ctx context.Context, a Account, fn func(context.Context, *tg.Client) error) error {
		return fn(ctx, tg.NewClient(f))
	}
	t.Cleanup(func() { run = old })
	return Account{ID: 1, Hash: "h", Session: filepath.Join(t.TempDir(), "s.json")}
}

var theBot = &tg.ContactsResolvedPeer{Peer: &tg.PeerUser{UserID: 7}, Users: []tg.UserClass{&tg.User{ID: 7, AccessHash: 70, Bot: true, Username: "desk_test_bot"}}}

// TestForum: the bot is found, a forum supergroup made, the bot invited
// and made an admin that manages topics; a failing step says which.
func TestForum(t *testing.T) {
	var admin *tg.ChannelsEditAdminRequest
	var made *tg.ChannelsCreateChannelRequest
	fail := ""
	f := &fake{}
	f.answer = func(req bin.Encoder) (bin.Encoder, error) {
		if fail != "" && strings.Contains(typeName(req), fail) {
			return nil, tgerr.New(400, "CHAT_ADMIN_REQUIRED")
		}
		switch r := req.(type) {
		case *tg.ContactsResolveUsernameRequest:
			return theBot, nil
		case *tg.ChannelsCreateChannelRequest:
			made = r
			return &tg.Updates{Chats: []tg.ChatClass{&tg.Channel{ID: 55, AccessHash: 550, Forum: true, Megagroup: true, Photo: &tg.ChatPhotoEmpty{}}}}, nil
		case *tg.ChannelsInviteToChannelRequest:
			return &tg.MessagesInvitedUsers{Updates: &tg.Updates{}}, nil
		case *tg.ChannelsEditAdminRequest:
			admin = r
			return &tg.Updates{}, nil
		}
		return nil, errors.New("unexpected " + typeName(req))
	}
	a := use(t, f)
	c, err := a.Forum(context.Background(), "Acme", "desk's cards", "desk_test_bot")
	if err != nil || c.ID != -1000000000055 || c.Channel != 55 || c.Access != 550 {
		t.Fatalf("%+v %v", c, err)
	}
	if !made.Forum || !made.Megagroup || made.Title != "Acme" || !admin.AdminRights.ManageTopics || !admin.AdminRights.PinMessages {
		t.Fatalf("%+v %+v", made, admin)
	}
	for _, step := range []string{"contacts.resolveUsername", "channels.createChannel", "channels.inviteToChannel", "channels.editAdmin"} {
		fail = step
		if _, err := a.Forum(context.Background(), "Acme", "", "desk_test_bot"); err == nil || !strings.Contains(err.Error(), "CHAT_ADMIN_REQUIRED") {
			t.Fatalf("%s: %v", step, err)
		}
	}
	fail = ""
	f.answer = func(req bin.Encoder) (bin.Encoder, error) {
		switch req.(type) {
		case *tg.ContactsResolveUsernameRequest:
			return theBot, nil
		}
		return &tg.UpdatesCombined{}, nil
	}
	if _, err := a.Forum(context.Background(), "Acme", "", "desk_test_bot"); err == nil || !strings.Contains(err.Error(), "no group") {
		t.Fatal(err)
	}
	f.answer = func(bin.Encoder) (bin.Encoder, error) {
		return &tg.ContactsResolvedPeer{Peer: &tg.PeerUser{UserID: 8}, Users: []tg.UserClass{&tg.User{ID: 8}}}, nil
	}
	if _, err := a.Forum(context.Background(), "Acme", "", "someone"); err == nil || !strings.Contains(err.Error(), "not a bot") {
		t.Fatal(err)
	}
	if channelOf(&tg.UpdatesCombined{Chats: []tg.ChatClass{&tg.Chat{ID: 1}, &tg.Channel{ID: 2}}}).ID != 2 || channelOf(&tg.UpdateShort{}) != nil {
		t.Fatal("channelOf")
	}
}

// TestFolder: a new folder takes the next free id after the user's own,
// holds the bot's chat (pinned) and the groups; an existing id is kept; a
// title over 12 characters is refused before anything is asked.
func TestFolder(t *testing.T) {
	var made *tg.MessagesUpdateDialogFilterRequest
	fail := ""
	f := &fake{}
	f.answer = func(req bin.Encoder) (bin.Encoder, error) {
		if fail != "" && strings.Contains(typeName(req), fail) {
			return nil, tgerr.New(400, "FILTER_INCLUDE_EMPTY")
		}
		switch r := req.(type) {
		case *tg.ContactsResolveUsernameRequest:
			return theBot, nil
		case *tg.MessagesStartBotRequest:
			return &tg.Updates{}, nil
		case *tg.MessagesGetDialogFiltersRequest:
			return &tg.MessagesDialogFilters{Filters: []tg.DialogFilterClass{&tg.DialogFilterDefault{}, &tg.DialogFilter{ID: 4}, &tg.DialogFilter{ID: 2}}}, nil
		case *tg.MessagesUpdateDialogFilterRequest:
			made = r
			return &tg.BoolTrue{}, nil
		}
		return nil, errors.New("unexpected " + typeName(req))
	}
	a := use(t, f)
	ctx := context.Background()
	chats := []Chat{{ID: -1000000000055, Channel: 55, Access: 550}}
	id, err := a.Folder(ctx, 0, "Desk Control", "desk_test_bot", chats)
	if err != nil || id != 5 || made.ID != 5 {
		t.Fatalf("%d %v", id, err)
	}
	d := made.Filter.(*tg.DialogFilter)
	if d.Title.Text != "Desk Control" || len(d.IncludePeers) != 2 || len(d.PinnedPeers) != 1 {
		t.Fatalf("%+v", d)
	}
	if ch := d.IncludePeers[1].(*tg.InputPeerChannel); ch.ChannelID != 55 || ch.AccessHash != 550 {
		t.Fatalf("%+v", ch)
	}
	f.got = nil
	if id, err := a.Folder(ctx, 9, "Desk", "desk_test_bot", nil); err != nil || id != 9 || strings.Contains(strings.Join(f.got, ","), "getDialogFilters") {
		t.Fatalf("%d %v %v", id, err, f.got)
	}
	f.got = nil
	if _, err := a.Folder(ctx, 0, "Desk Control (x)", "desk_test_bot", nil); err == nil || len(f.got) != 0 {
		t.Fatalf("long title: %v %v", err, f.got)
	}
	for _, step := range []string{"contacts.resolveUsername", "messages.startBot", "messages.getDialogFilters", "messages.updateDialogFilter"} {
		fail = step
		if _, err := a.Folder(ctx, 0, "Desk", "desk_test_bot", nil); err == nil || !strings.Contains(err.Error(), "FILTER_INCLUDE_EMPTY") {
			t.Fatalf("%s: %v", step, err)
		}
	}
	if randID() == randID() {
		t.Fatal("randID repeats")
	}
}

// TestLogin: a session already logged in asks nothing; an unknown one runs
// the flow: the code comes from the Asker and signs in, the session file
// kept 0600; Logout ends it and removes the file.
func TestLogin(t *testing.T) {
	authorized := true
	var signed *tg.AuthSignInRequest
	f := &fake{}
	f.answer = func(req bin.Encoder) (bin.Encoder, error) {
		switch r := req.(type) {
		case *tg.UsersGetUsersRequest:
			if !authorized {
				return nil, tgerr.New(401, "AUTH_KEY_UNREGISTERED")
			}
			return &tg.UserClassVector{Elems: []tg.UserClass{&tg.User{ID: 1, Self: true}}}, nil
		case *tg.AuthSendCodeRequest:
			return &tg.AuthSentCode{Type: &tg.AuthSentCodeTypeApp{Length: 5}, PhoneCodeHash: "hash"}, nil
		case *tg.AuthSignInRequest:
			signed = r
			return &tg.AuthAuthorization{User: &tg.User{ID: 1}}, nil
		case *tg.AuthLogOutRequest:
			return &tg.AuthLoggedOut{}, nil
		}
		return nil, errors.New("unexpected " + typeName(req))
	}
	a := use(t, f)
	ctx := context.Background()
	asked := 0
	ask := Asker{Code: func(context.Context) (string, error) { asked++; return "12345", nil },
		Password: func(context.Context) (string, error) { return "pw", nil }}
	if err := a.Login(ctx, "+10000000000", ask); err != nil || asked != 0 {
		t.Fatalf("already in: %v %d", err, asked)
	}
	authorized = false
	os.WriteFile(a.Session, []byte("{}"), 0o644)
	if err := a.Login(ctx, "+10000000000", ask); err != nil || asked != 1 || signed.PhoneCode != "12345" || signed.PhoneCodeHash != "hash" {
		t.Fatalf("login: %v %d %+v", err, asked, signed)
	}
	if fi, _ := os.Stat(a.Session); fi.Mode().Perm() != 0o600 || !a.LoggedIn() {
		t.Fatalf("session %v", fi.Mode())
	}
	if err := a.Logout(ctx); err != nil || a.LoggedIn() {
		t.Fatalf("logout %v", err)
	}
	if err := a.Logout(ctx); err != nil {
		t.Fatalf("logout without a file: %v", err)
	}
	u := user{"+1", ask}
	if p, _ := u.Phone(ctx); p != "+1" {
		t.Fatal(p)
	}
	if pw, _ := u.Password(ctx); pw != "pw" || u.AcceptTermsOfService(ctx, tg.HelpTermsOfService{}) == nil {
		t.Fatal("password or terms")
	}
	if _, err := u.SignUp(ctx); err == nil {
		t.Fatal("sign up")
	}
	os.MkdirAll(a.Session, 0o700) // a folder where the file goes: it can't be removed as one
	os.WriteFile(filepath.Join(a.Session, "x"), nil, 0o600)
	if err := a.Logout(ctx); err == nil {
		t.Fatal("a session that can't be removed")
	}
}

// TestRun: the real run makes the session's folder first, and gives up
// on a canceled context without reaching Telegram.
// ponytail: connecting for real is a live check, left to the spike.
func TestRun(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "desk")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := run(ctx, Account{ID: 1, Hash: "h", Session: filepath.Join(dir, "telegram.session")}, func(context.Context, *tg.Client) error { called = true; return nil })
	if called {
		t.Fatalf("reached Telegram on a canceled context: %v", err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatal("no session folder", err)
	}
	file := filepath.Join(t.TempDir(), "f")
	os.WriteFile(file, nil, 0o600)
	if err := run(ctx, Account{Session: filepath.Join(file, "x", "s")}, nil); err == nil {
		t.Fatal("a session under a file")
	}
}
