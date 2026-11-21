// Package tgsetup is what desk does as the user's own Telegram account
// (MTProto, through gotd), which a bot can't: log in, make an
// organization's forum supergroup with the bot as an admin that manages
// topics, and keep the "Desk Control" folder holding the bot's chat and
// those groups. Only setup goes through here; desk's messages are the
// bot's (internal/tgbot). The session file is the account's key: 0600.
package tgsetup

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

// Account is the user's Telegram account as an app (api_id and api_hash
// from my.telegram.org) and the session file a login leaves.
type Account struct {
	ID      int
	Hash    string
	Session string
}

// run connects as a, calls f, and disconnects; tests replace it.
// ponytail: one connection per action; setup is rare, so no kept client.
var run = func(ctx context.Context, a Account, f func(context.Context, *tg.Client) error) error {
	if err := os.MkdirAll(filepath.Dir(a.Session), 0o700); err != nil {
		return err
	}
	c := telegram.NewClient(a.ID, a.Hash, telegram.Options{SessionStorage: &session.FileStorage{Path: a.Session}, NoUpdates: true})
	return c.Run(ctx, func(ctx context.Context) error { return f(ctx, c.API()) })
}

// Asker asks the user what a login needs, in desk's own dialog: the code
// Telegram sent, then the 2FA password when the account has one.
type Asker struct {
	Code     func(ctx context.Context) (string, error)
	Password func(ctx context.Context) (string, error)
}

// Login logs a in as phone's account, unless its session already is.
func (a Account) Login(ctx context.Context, phone string, ask Asker) error {
	return run(ctx, a, func(ctx context.Context, api *tg.Client) error {
		err := auth.NewClient(api, rand.Reader, a.ID, a.Hash).IfNecessary(ctx, auth.NewFlow(user{phone, ask}, auth.SendCodeOptions{}))
		os.Chmod(a.Session, 0o600)
		return err
	})
}

// LoggedIn is whether a login left a session (not whether Telegram still
// takes it: that shows on the next action).
func (a Account) LoggedIn() bool {
	_, err := os.Stat(a.Session)
	return err == nil
}

// Logout ends the session at Telegram and removes its file.
func (a Account) Logout(ctx context.Context) error {
	err := run(ctx, a, func(ctx context.Context, api *tg.Client) error {
		_, err := api.AuthLogOut(ctx)
		return err
	})
	if rm := os.Remove(a.Session); err == nil && !errors.Is(rm, os.ErrNotExist) {
		err = rm
	}
	return err
}

// user answers gotd's login flow through an Asker.
type user struct {
	phone string
	ask   Asker
}

func (u user) Phone(context.Context) (string, error) { return u.phone, nil }
func (u user) Password(ctx context.Context) (string, error) {
	return u.ask.Password(ctx)
}
func (u user) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	return u.ask.Code(ctx)
}
func (u user) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	return errors.New("this account has new terms to accept: open Telegram, then log in again")
}
func (u user) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("no Telegram account has this phone")
}

// Chat is a supergroup desk made: the Bot API's id for it and what
// MTProto needs to name it again (for the folder).
type Chat struct {
	ID      int64 // -100… as the Bot API counts
	Channel int64
	Access  int64
}

// Forum makes a private forum supergroup titled title, with the bot (by
// its username) as an admin that manages topics, pins and deletes.
func (a Account) Forum(ctx context.Context, title, about, bot string) (c Chat, err error) {
	err = run(ctx, a, func(ctx context.Context, api *tg.Client) error {
		c, err = forum(ctx, api, title, about, bot)
		return err
	})
	return c, err
}

func forum(ctx context.Context, api *tg.Client, title, about, bot string) (Chat, error) {
	b, err := botUser(ctx, api, bot)
	if err != nil {
		return Chat{}, err
	}
	up, err := api.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Megagroup: true, Forum: true, Title: title, About: about})
	if err != nil {
		return Chat{}, fmt.Errorf("make the group: %w", err)
	}
	ch := channelOf(up)
	if ch == nil {
		return Chat{}, errors.New("make the group: Telegram sent no group back")
	}
	in := &tg.InputChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash}
	c := Chat{ID: -1000000000000 - ch.ID, Channel: ch.ID, Access: ch.AccessHash}
	if _, err := api.ChannelsInviteToChannel(ctx, &tg.ChannelsInviteToChannelRequest{Channel: in, Users: []tg.InputUserClass{b}}); err != nil {
		return c, fmt.Errorf("add the bot: %w", err)
	}
	_, err = api.ChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{Channel: in, UserID: b, Rank: "desk",
		AdminRights: tg.ChatAdminRights{ChangeInfo: true, DeleteMessages: true, PinMessages: true, ManageTopics: true, Other: true}})
	if err != nil {
		return c, fmt.Errorf("make the bot an admin: %w", err)
	}
	return c, nil
}

// botUser is the bot as a user the account can add, its chat started so
// it can go in a folder.
func botUser(ctx context.Context, api *tg.Client, bot string) (*tg.InputUser, error) {
	res, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: bot})
	if err != nil {
		return nil, fmt.Errorf("find @%s: %w", bot, err)
	}
	for _, u := range res.Users {
		if u, ok := u.(*tg.User); ok && u.Bot {
			return &tg.InputUser{UserID: u.ID, AccessHash: u.AccessHash}, nil
		}
	}
	return nil, fmt.Errorf("@%s is not a bot", bot)
}

func channelOf(u tg.UpdatesClass) *tg.Channel {
	var chats []tg.ChatClass
	switch v := u.(type) {
	case *tg.Updates:
		chats = v.Chats
	case *tg.UpdatesCombined:
		chats = v.Chats
	}
	for _, c := range chats {
		if ch, ok := c.(*tg.Channel); ok {
			return ch
		}
	}
	return nil
}

// FolderMax is the longest folder title Telegram takes.
const FolderMax = 12

// Folder makes or updates (id, 0: a new one) the folder titled title
// holding the bot's chat and chats; its id.
func (a Account) Folder(ctx context.Context, id int, title, bot string, chats []Chat) (n int, err error) {
	err = run(ctx, a, func(ctx context.Context, api *tg.Client) error {
		n, err = folder(ctx, api, id, title, bot, chats)
		return err
	})
	return n, err
}

func folder(ctx context.Context, api *tg.Client, id int, title, bot string, chats []Chat) (int, error) {
	if title == "" || utf8.RuneCountInString(title) > FolderMax {
		return 0, fmt.Errorf("a folder's name is 1 to %d characters", FolderMax)
	}
	b, err := botUser(ctx, api, bot)
	if err != nil {
		return 0, err
	}
	peer := &tg.InputPeerUser{UserID: b.UserID, AccessHash: b.AccessHash}
	if _, err := api.MessagesStartBot(ctx, &tg.MessagesStartBotRequest{Bot: b, Peer: peer, RandomID: randID(), StartParam: "desk"}); err != nil {
		return 0, fmt.Errorf("start the bot's chat: %w", err)
	}
	if id == 0 {
		fs, err := api.MessagesGetDialogFilters(ctx)
		if err != nil {
			return 0, fmt.Errorf("read your folders: %w", err)
		}
		id = 2 // 0 and 1 are Telegram's own
		for _, f := range fs.Filters {
			if d, ok := f.(*tg.DialogFilter); ok && d.ID >= id {
				id = d.ID + 1
			}
		}
	}
	peers := []tg.InputPeerClass{peer}
	for _, c := range chats {
		peers = append(peers, &tg.InputPeerChannel{ChannelID: c.Channel, AccessHash: c.Access})
	}
	_, err = api.MessagesUpdateDialogFilter(ctx, &tg.MessagesUpdateDialogFilterRequest{ID: id, Filter: &tg.DialogFilter{
		ID: id, Title: tg.TextWithEntities{Text: title}, Emoticon: "🤖",
		IncludePeers: peers, PinnedPeers: []tg.InputPeerClass{peer}, ExcludePeers: []tg.InputPeerClass{},
	}})
	if err != nil {
		return 0, fmt.Errorf("make the folder: %w", err)
	}
	return id, nil
}

func randID() int64 {
	var b [8]byte
	rand.Read(b[:])
	var n int64
	for _, x := range b {
		n = n<<8 | int64(x)
	}
	return n
}
