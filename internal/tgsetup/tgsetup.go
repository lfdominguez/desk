// Package tgsetup is what desk does as the user's own Telegram account
// (MTProto, through gotd), which a bot can't: log in, make an
// organization's forum supergroup with the bot as an admin that manages
// topics, and keep the "Desk Control" folder holding the bot's chat and
// those groups, and transcribe the voice notes the user sends desk
// (Telegram's own transcription, a Premium feature). Desk's messages are
// the bot's (internal/tgbot). The session file is the account's key: 0600.
package tgsetup

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// Account is the user's Telegram account as an app (api_id and api_hash
// from my.telegram.org) and the session file a login leaves.
type Account struct {
	ID      int
	Hash    string
	Session string
}

// run calls f on a's kept connection (keep); tests replace it.
var run = func(ctx context.Context, a Account, f func(context.Context, *tg.Client) error) error {
	if err := os.MkdirAll(filepath.Dir(a.Session), 0o700); err != nil {
		return err
	}
	k, err := keep(ctx, a)
	if err != nil {
		return err
	}
	defer k.release()
	return f(ctx, k.api)
}

// floodMax is the longest FLOOD_WAIT waitFlood sits out; a longer one
// comes back as the error.
var floodMax = 2 * time.Minute

// waitFlood retries a call Telegram asked to slow down (FLOOD_WAIT, as
// making several groups in a row gets) after the wait it names. Only that
// call: retrying a whole step would make a second group.
func waitFlood(next tg.Invoker) telegram.InvokeFunc {
	return func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		for {
			err := next.Invoke(ctx, in, out)
			d, ok := tgerr.AsFloodWait(err)
			if !ok || d > floodMax {
				return err
			}
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return err
			}
		}
	}
}

// act runs f on a's connection. A session Telegram refuses (a login never
// finished, or ended from another device) is dropped, so LoggedIn turns
// false and Settings asks for a new login.
func act(ctx context.Context, a Account, f func(context.Context, *tg.Client) error) error {
	err := run(ctx, a, f)
	if auth.IsUnauthorized(err) {
		drop(a)
		return fmt.Errorf("Telegram no longer takes this login, log in again (Settings → Telegram): %w", err)
	}
	return err
}

// drop ends a's connection and removes its session file.
func drop(a Account) error {
	forget(a)
	if err := os.Remove(a.Session); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// connect connects as a until ctx ends, handing Telegram's updates to up
// and calling f once connected; tests replace it.
var connect = func(ctx context.Context, a Account, up telegram.UpdateHandler, f func(context.Context, *tg.Client) error) error {
	c := telegram.NewClient(a.ID, a.Hash, telegram.Options{SessionStorage: &session.FileStorage{Path: a.Session}, UpdateHandler: up,
		Middlewares: []telegram.Middleware{telegram.MiddlewareFunc(waitFlood)}})
	return c.Run(ctx, func(ctx context.Context) error { return f(ctx, c.API()) })
}

// conn is an account's connection, kept between actions (connecting takes
// about a second, and a transcription's end comes on it as an update)
// until connIdle passes unused.
type conn struct {
	api   *tg.Client
	ready chan struct{} // closed once connected
	done  chan struct{} // closed once it ended, err why
	err   error
	stop  context.CancelFunc
	users int
	idle  *time.Timer
}

var (
	connMu   sync.Mutex
	conns    = map[string]*conn{} // by account and session file
	connIdle = 5 * time.Minute
)

// keep is a's connection, made when there's none; release it after use.
func keep(ctx context.Context, a Account) (*conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := fmt.Sprint(a.ID, "\x00", a.Hash, "\x00", a.Session)
	connMu.Lock()
	k := conns[key]
	if k == nil {
		k = &conn{ready: make(chan struct{}), done: make(chan struct{})}
		var cctx context.Context
		cctx, k.stop = context.WithCancel(context.Background())
		conns[key] = k
		go func() {
			err := connect(cctx, a, telegram.UpdateHandlerFunc(heard), func(ctx context.Context, api *tg.Client) error {
				k.api = api
				close(k.ready)
				<-ctx.Done()
				return nil
			})
			connMu.Lock()
			if conns[key] == k {
				delete(conns, key)
			}
			k.err = cmp.Or(err, errors.New("the connection to Telegram ended"))
			connMu.Unlock()
			close(k.done)
		}()
	}
	k.users++
	if k.idle != nil {
		k.idle.Stop()
	}
	connMu.Unlock()
	select {
	case <-k.ready:
		return k, nil
	case <-k.done:
		k.release()
		return nil, k.err
	case <-ctx.Done():
		k.release()
		return nil, ctx.Err()
	}
}

// release ends a use of k; the last one starts its idle wait.
func (k *conn) release() {
	connMu.Lock()
	defer connMu.Unlock()
	if k.users--; k.users == 0 {
		k.idle = time.AfterFunc(connIdle, func() {
			connMu.Lock()
			defer connMu.Unlock()
			if k.users == 0 {
				k.stop()
			}
		})
	}
}

// forget ends a's connection, if one is kept (after logging out).
func forget(a Account) {
	key := fmt.Sprint(a.ID, "\x00", a.Hash, "\x00", a.Session)
	connMu.Lock()
	defer connMu.Unlock()
	if k := conns[key]; k != nil {
		delete(conns, key)
		k.stop()
	}
}

// Asker asks the user what a login needs, in desk's own dialog: the code
// Telegram sent, then the 2FA password when the account has one.
type Asker struct {
	Code     func(ctx context.Context) (string, error)
	Password func(ctx context.Context) (string, error)
}

// Login logs a in as phone's account, unless its session already is. A
// login that doesn't finish (canceled, a wrong code or password) drops its
// session: gotd writes the file on connecting, before the account is in.
func (a Account) Login(ctx context.Context, phone string, ask Asker) error {
	err := run(ctx, a, func(ctx context.Context, api *tg.Client) error {
		err := auth.NewClient(api, rand.Reader, a.ID, a.Hash).IfNecessary(ctx, auth.NewFlow(user{phone, ask}, auth.SendCodeOptions{}))
		os.Chmod(a.Session, 0o600)
		return err
	})
	if err != nil {
		drop(a)
	}
	return err
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
	if rm := drop(a); err == nil {
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
	err = act(ctx, a, func(ctx context.Context, api *tg.Client) error {
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

// PinTopics pins topics (by id; the General topic is 1) at the top of
// forum c's topic list: a bot can't, the group's owner can.
func (a Account) PinTopics(ctx context.Context, c Chat, topics ...int) error {
	return act(ctx, a, func(ctx context.Context, api *tg.Client) error {
		peer := &tg.InputPeerChannel{ChannelID: c.Channel, AccessHash: c.Access}
		for _, id := range topics {
			if _, err := api.MessagesUpdatePinnedForumTopic(ctx, &tg.MessagesUpdatePinnedForumTopicRequest{Peer: peer, TopicID: id, Pinned: true}); err != nil {
				return fmt.Errorf("pin topic %d: %w", id, err)
			}
		}
		return nil
	})
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
	err = act(ctx, a, func(ctx context.Context, api *tg.Client) error {
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

// Voice is a voice note the user sent desk, named as the account sees
// it: in the bot's chat (Bot, its username; the account counts that
// chat's messages apart from the bot, so it's found by Date among the
// account's own voice notes) or in a group desk made (Chat, whose message
// ids everyone shares).
type Voice struct {
	Bot  string
	Chat Chat
	Msg  int64
	Date int64
}

// transcribeEvery is how often a transcription under way is asked again,
// should its update not come.
var transcribeEvery = 15 * time.Second

// Transcriptions Telegram finished, by id, as its updates said; each
// change closes heardNew.
var (
	heardMu  sync.Mutex
	finished = map[int64]string{}
	heardNew = make(chan struct{})
)

// heard takes the finished transcriptions out of Telegram's updates.
func heard(_ context.Context, u tg.UpdatesClass) error {
	var ups []tg.UpdateClass
	switch v := u.(type) {
	case *tg.Updates:
		ups = v.Updates
	case *tg.UpdatesCombined:
		ups = v.Updates
	case *tg.UpdateShort:
		ups = []tg.UpdateClass{v.Update}
	}
	heardMu.Lock()
	defer heardMu.Unlock()
	for _, up := range ups {
		if t, ok := up.(*tg.UpdateTranscribedAudio); ok && !t.Pending {
			if len(finished) >= 100 { // others' never claimed (the account's own apps transcribe too)
				clear(finished)
			}
			finished[t.TranscriptionID] = t.Text
			close(heardNew)
			heardNew = make(chan struct{})
		}
	}
	return nil
}

// await is transcription id's text once heard, or "" and false once
// transcribeEvery passes.
func await(ctx context.Context, id int64) (string, bool, error) {
	timer := time.NewTimer(transcribeEvery)
	defer timer.Stop()
	for {
		heardMu.Lock()
		text, ok := finished[id]
		delete(finished, id)
		news := heardNew
		heardMu.Unlock()
		if ok {
			return text, true, nil
		}
		select {
		case <-ctx.Done():
			return "", false, fmt.Errorf("transcribe: %w", ctx.Err())
		case <-timer.C:
			return "", false, nil
		case <-news:
		}
	}
}

// Transcribe is the text Telegram's transcription makes of v (the
// account needs Premium, or the free trials left), once done.
func (a Account) Transcribe(ctx context.Context, v Voice) (text string, err error) {
	err = act(ctx, a, func(ctx context.Context, api *tg.Client) error {
		text, err = transcribe(ctx, api, v)
		return err
	})
	return text, err
}

func transcribe(ctx context.Context, api *tg.Client, v Voice) (string, error) {
	var peer tg.InputPeerClass = &tg.InputPeerChannel{ChannelID: v.Chat.Channel, AccessHash: v.Chat.Access}
	msg := int(v.Msg)
	if v.Chat.Channel == 0 {
		b, err := botUser(ctx, api, v.Bot)
		if err != nil {
			return "", err
		}
		peer = &tg.InputPeerUser{UserID: b.UserID, AccessHash: b.AccessHash}
		if msg, err = mine(ctx, api, peer, v.Date); err != nil {
			return "", err
		}
	}
	for {
		res, err := api.MessagesTranscribeAudio(ctx, &tg.MessagesTranscribeAudioRequest{Peer: peer, MsgID: msg})
		if err != nil {
			return "", fmt.Errorf("transcribe: %w", err)
		}
		if !res.Pending {
			return res.Text, nil
		}
		text, ok, err := await(ctx, res.TranscriptionID)
		if ok || err != nil {
			return text, err
		}
	}
}

// mine is the account's id for its own voice note sent at date in peer.
func mine(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, date int64) (int, error) {
	res, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peer, Limit: 20})
	if err != nil {
		return 0, fmt.Errorf("find the voice note: %w", err)
	}
	if m, ok := res.AsModified(); ok {
		for _, c := range m.GetMessages() {
			m, ok := c.(*tg.Message)
			if !ok || !m.Out || int64(m.Date) != date {
				continue
			}
			if d, ok := m.Media.(*tg.MessageMediaDocument); ok && d.Voice {
				return m.ID, nil
			}
		}
	}
	return 0, errors.New("find the voice note: it isn't among the chat's last messages")
}
