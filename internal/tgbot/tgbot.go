// Package tgbot is the part of Telegram's Bot API desk uses: long polling
// for updates (no port opened), plain and rich messages (Bot API 10.1's
// sendRichMessage, edited in place by editMessageText), streamed drafts in
// the bot's private chat, forum topics, buttons, files, reactions and
// command menus. Plain net/http. Calls to one chat are paced and a 429's
// retry_after is waited out, so callers can just call.
package tgbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"sync"
	"time"
)

// API is Telegram's Bot API; tests point it at a fake.
var API = "https://api.telegram.org"

// Poll is how long one getUpdates waits for something to happen.
var Poll = 50 * time.Second

// Bot is one bot, by its token (from @BotFather).
type Bot struct {
	Token  string
	Client *http.Client // nil: one that outlasts a poll

	mu   sync.Mutex
	next map[int64]time.Time // a chat's next free slot (pace)
}

// Chat is where a message is.
type Chat struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"` // private, group, supergroup, channel
	Title   string `json:"title"`
	IsForum bool   `json:"is_forum"`
}

// User is who sent a message or pressed a button.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	IsBot    bool   `json:"is_bot"`
}

// File is a photo size, document or voice note: what File downloads.
type File struct {
	ID   string `json:"file_id"`
	Name string `json:"file_name"`
	Mime string `json:"mime_type"`
	Size int64  `json:"file_size"`
}

// Message is a chat message.
type Message struct {
	ID       int64    `json:"message_id"`
	Thread   int64    `json:"message_thread_id"` // its forum topic
	IsTopic  bool     `json:"is_topic_message"`
	Chat     Chat     `json:"chat"`
	From     User     `json:"from"`
	Text     string   `json:"text"`
	Caption  string   `json:"caption"`
	Photo    []File   `json:"photo"` // sizes, smallest first
	Document *File    `json:"document"`
	Voice    *File    `json:"voice"`
	Reply    *Message `json:"reply_to_message"`
}

// Update is something that happened: a message, or a button pressed.
type Update struct {
	ID       int64        `json:"update_id"`
	Message  *Message     `json:"message"`
	Callback *Callback    `json:"callback_query"`
	Widget   *WidgetQuery `json:"widget_query"`
}

// WidgetQuery is a tap in one of the bot's HTML widgets: the data its
// TelegramWidget.request sent (1-64 bytes), answered by AnswerWidget
// within 15 seconds.
type WidgetQuery struct {
	ID   string `json:"id"`
	From User   `json:"from"`
	Data string `json:"data"`
}

// Callback is a button pressed under one of the bot's messages.
type Callback struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Data    string   `json:"data"`
	Message *Message `json:"message"`
}

// Button is an inline button; Data comes back in its Callback. Style is
// "primary", "success" or "danger" (Bot API 9.4), URL opens a link instead.
type Button struct {
	Text  string `json:"text"`
	Data  string `json:"callback_data,omitempty"`
	URL   string `json:"url,omitempty"`
	Style string `json:"style,omitempty"`
}

// Out is a message to send or the new content of one: Markdown or HTML
// make it a rich message, else Text is plain.
type Out struct {
	Chat, Thread   int64
	Markdown, HTML string
	Text           string
	Rows           [][]Button
	Silent         bool // no sound on the phone
}

// Error is what Telegram said no with.
type Error struct {
	Method, Description string
	Code                int
}

func (e *Error) Error() string { return "telegram " + e.Method + ": " + e.Description }

// NotModified is an edit to what the message already says, which callers
// that re-render on every change can ignore.
func NotModified(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == 400 && bytes.Contains([]byte(e.Description), []byte("not modified"))
}

// Gap is how often one chat takes a message: Telegram allows about one a
// second in a private chat and twenty a minute in a group.
// ponytail: per chat only; Telegram's global 30/s doesn't bind one user's
// handful of chats.
var Gap = func(chat int64) time.Duration {
	if chat < 0 {
		return 3 * time.Second
	}
	return time.Second
}

// sleep waits d or until ctx ends; tests replace it.
var sleep = func(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

var now = time.Now

// pace waits for chat's next slot and takes it.
func (b *Bot) pace(ctx context.Context, chat int64) error {
	b.mu.Lock()
	if b.next == nil {
		b.next = map[int64]time.Time{}
	}
	t := now()
	at := b.next[chat]
	if at.Before(t) {
		at = t
	}
	b.next[chat] = at.Add(Gap(chat))
	b.mu.Unlock()
	return sleep(ctx, at.Sub(t))
}

// retries is how many 429s one call waits out before giving up.
const retries = 3

// call posts method's params (paced by their chat_id) and decodes its
// result into v (nil: none).
func (b *Bot) call(ctx context.Context, method string, params map[string]any, v any) error {
	if chat, ok := params["chat_id"].(int64); ok && method != "sendRichMessageDraft" && method != "sendMessageDraft" {
		if err := b.pace(ctx, chat); err != nil {
			return err
		}
	}
	body, _ := json.Marshal(params)
	for try := 0; ; try++ {
		err := b.post(ctx, method, "application/json", bytes.NewReader(body), v)
		var e *retryError
		if !errors.As(err, &e) {
			return err
		}
		if try == retries {
			return &Error{Method: method, Code: 429, Description: "too many requests"}
		}
		if err := sleep(ctx, e.after); err != nil {
			return err
		}
	}
}

type retryError struct{ after time.Duration }

func (e *retryError) Error() string { return "retry after " + e.after.String() }

func (b *Bot) post(ctx context.Context, method, ctype string, body io.Reader, v any) error {
	req, err := http.NewRequestWithContext(ctx, "POST", API+"/bot"+b.Token+"/"+method, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ctype)
	resp, err := b.client().Do(req)
	if err != nil {
		return fmt.Errorf("telegram %s: unreachable", method) // its text holds the URL, and the URL the token
	}
	defer resp.Body.Close()
	var out struct {
		OK          bool            `json:"ok"`
		Code        int             `json:"error_code"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return &Error{Method: method, Code: resp.StatusCode, Description: resp.Status}
	}
	if out.Code == 429 {
		return &retryError{time.Duration(max(out.Parameters.RetryAfter, 1)) * time.Second}
	}
	if !out.OK {
		return &Error{Method: method, Code: out.Code, Description: out.Description}
	}
	if v != nil {
		return json.Unmarshal(out.Result, v)
	}
	return nil
}

func (b *Bot) client() *http.Client {
	if b.Client != nil {
		return b.Client
	}
	return &http.Client{Timeout: Poll + 20*time.Second}
}

// Updates waits up to Poll for the updates after offset (the last one's
// ID plus one).
func (b *Bot) Updates(ctx context.Context, offset int64) ([]Update, error) {
	var us []Update
	err := b.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": int(Poll.Seconds()),
		"allowed_updates": []string{"message", "callback_query", "widget_query"}}, &us)
	return us, err
}

// Me is the bot as getMe tells it.
type Me struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Topics   bool   `json:"has_topics_enabled"` // topics in its private chats, BotFather's switch
}

// Me is who the bot is.
func (b *Bot) Me(ctx context.Context) (Me, error) {
	var m Me
	err := b.call(ctx, "getMe", map[string]any{}, &m)
	return m, err
}

// content is o's text as a send or edit takes it.
func (o Out) content(p map[string]any) map[string]any {
	switch {
	case o.Markdown != "":
		p["rich_message"] = map[string]any{"markdown": o.Markdown}
	case o.HTML != "":
		p["rich_message"] = map[string]any{"html": o.HTML}
	default:
		p["text"] = o.Text
	}
	if len(o.Rows) > 0 {
		p["reply_markup"] = map[string]any{"inline_keyboard": o.Rows}
	}
	return p
}

func (o Out) rich() bool { return o.Markdown != "" || o.HTML != "" }

// Post sends o; the message's ID.
func (b *Bot) Post(ctx context.Context, o Out) (int64, error) {
	p := o.content(map[string]any{"chat_id": o.Chat})
	if o.Thread != 0 {
		p["message_thread_id"] = o.Thread
	}
	if o.Silent {
		p["disable_notification"] = true
	}
	method := "sendMessage"
	if o.rich() {
		method = "sendRichMessage"
	}
	var m Message
	err := b.call(ctx, method, p, &m)
	return m.ID, err
}

// Change replaces message msg's content with o's (its chat is o.Chat); a
// rich message stays rich. Without Rows its buttons go.
func (b *Bot) Change(ctx context.Context, msg int64, o Out) error {
	return b.call(ctx, "editMessageText", o.content(map[string]any{"chat_id": o.Chat, "message_id": msg}), nil)
}

// SendWidget sends an HTML widget (Bot API 10.4: private chats only, at
// most 4096 bytes zlib-compressed) to chat's thread with chatData for it
// to read; its taps may query the bot. It returns the message's id.
func (b *Bot) SendWidget(ctx context.Context, chat, thread int64, html, chatData string) (int64, error) {
	p := map[string]any{"chat_id": chat, "html": html, "chat_data": chatData, "allow_queries": true}
	if thread != 0 {
		p["message_thread_id"] = thread
	}
	var m Message
	err := b.call(ctx, "sendWidget", p, &m)
	return m.ID, err
}

// WidgetData replaces widget message msg's chat data (at most 1024 bytes):
// the widget redraws from it.
func (b *Bot) WidgetData(ctx context.Context, chat, msg int64, chatData string) error {
	return b.call(ctx, "editMessageWidgetData", map[string]any{"chat_id": chat, "message_id": msg, "chat_data": chatData}, nil)
}

// AnswerWidget answers widget query id with data (only that widget sees it).
func (b *Bot) AnswerWidget(ctx context.Context, id, data string) error {
	return b.call(ctx, "answerWidgetQuery", map[string]any{"widget_query_id": id, "data": data}, nil)
}

// Buttons replaces message msg's buttons only (none: rows nil).
func (b *Bot) Buttons(ctx context.Context, chat, msg int64, rows [][]Button) error {
	if rows == nil {
		rows = [][]Button{}
	}
	return b.call(ctx, "editMessageReplyMarkup", map[string]any{"chat_id": chat, "message_id": msg,
		"reply_markup": map[string]any{"inline_keyboard": rows}}, nil)
}

// Draft streams markdown as draft id into a private chat (thread: its
// topic, or 0), Telegram's way of showing a reply as it's written; the
// final Post replaces it. Groups don't take drafts: edit a message there.
func (b *Bot) Draft(ctx context.Context, chat, thread, id int64, markdown string) error {
	p := map[string]any{"chat_id": chat, "draft_id": id, "rich_message": map[string]any{"markdown": markdown}}
	if thread != 0 {
		p["message_thread_id"] = thread
	}
	return b.call(ctx, "sendRichMessageDraft", p, nil)
}

// Topic colors, the six createForumTopic takes.
const (
	Blue   = 0x6FB9F0
	Yellow = 0xFFD67E
	Violet = 0xCB86DB
	Green  = 0x8EEE98
	Rose   = 0xFF93B2
	Red    = 0xFB6F5F
)

// NewTopic makes a forum topic in chat; its thread's ID.
func (b *Bot) NewTopic(ctx context.Context, chat int64, name string, color int) (int64, error) {
	var t struct {
		Thread int64 `json:"message_thread_id"`
	}
	p := map[string]any{"chat_id": chat, "name": name}
	if color != 0 {
		p["icon_color"] = color
	}
	err := b.call(ctx, "createForumTopic", p, &t)
	return t.Thread, err
}

// Rename renames a topic.
func (b *Bot) Rename(ctx context.Context, chat, thread int64, name string) error {
	return b.call(ctx, "editForumTopic", map[string]any{"chat_id": chat, "message_thread_id": thread, "name": name}, nil)
}

// Topic acts on a topic: "close", "reopen" or "delete".
func (b *Bot) Topic(ctx context.Context, chat, thread int64, act string) error {
	return b.call(ctx, act+"ForumTopic", map[string]any{"chat_id": chat, "message_thread_id": thread}, nil)
}

// Pin pins a message, quietly.
func (b *Bot) Pin(ctx context.Context, chat, msg int64) error {
	return b.call(ctx, "pinChatMessage", map[string]any{"chat_id": chat, "message_id": msg, "disable_notification": true}, nil)
}

// React puts emoji under a message as the bot's reaction ("": none).
func (b *Bot) React(ctx context.Context, chat, msg int64, emoji string) error {
	r := []map[string]any{}
	if emoji != "" {
		r = append(r, map[string]any{"type": "emoji", "emoji": emoji})
	}
	return b.call(ctx, "setMessageReaction", map[string]any{"chat_id": chat, "message_id": msg, "reaction": r}, nil)
}

// Command is a slash command in a chat's menu.
type Command struct {
	Name string `json:"command"`
	What string `json:"description"`
}

// Commands sets the menu of chat's commands (0: every chat's default).
func (b *Bot) Commands(ctx context.Context, chat int64, cs []Command) error {
	scope := map[string]any{"type": "default"}
	if chat != 0 {
		scope = map[string]any{"type": "chat", "chat_id": chat}
	}
	return b.call(ctx, "setMyCommands", map[string]any{"commands": cs, "scope": scope}, nil)
}

// SendFile sends data as kind "photo" or "document" named name, with a
// caption and buttons under it; the message's ID.
func (b *Bot) SendFile(ctx context.Context, chat, thread int64, kind, name string, data []byte, caption string, rows [][]Button) (int64, error) {
	if err := b.pace(ctx, chat); err != nil {
		return 0, err
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	w.WriteField("chat_id", fmt.Sprint(chat))
	if thread != 0 {
		w.WriteField("message_thread_id", fmt.Sprint(thread))
	}
	if caption != "" {
		w.WriteField("caption", caption)
	}
	if len(rows) > 0 {
		k, _ := json.Marshal(map[string]any{"inline_keyboard": rows})
		w.WriteField("reply_markup", string(k))
	}
	f, _ := w.CreateFormFile(kind, name)
	f.Write(data)
	w.Close()
	var m Message
	method := map[string]string{"photo": "sendPhoto"}[kind]
	if method == "" {
		method = "sendDocument"
	}
	err := b.post(ctx, method, w.FormDataContentType(), &body, &m)
	return m.ID, err
}

// MaxFile is the most File downloads: the Bot API's own limit.
const MaxFile = 20 << 20

// Download is a file's bytes, by its ID.
func (b *Bot) Download(ctx context.Context, id string) ([]byte, error) {
	var f struct {
		Path string `json:"file_path"`
	}
	if err := b.call(ctx, "getFile", map[string]any{"file_id": id}, &f); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", API+"/file/bot"+b.Token+"/"+f.Path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.client().Do(req)
	if err != nil {
		return nil, errors.New("telegram file: unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("telegram file: " + resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxFile+1))
	if len(data) > MaxFile {
		return nil, errors.New("telegram file: over 20 MB")
	}
	return data, err
}

// Send sends plain text to chat with rows of buttons under it (none:
// nil); the message's ID.
func (b *Bot) Send(ctx context.Context, chat int64, text string, rows [][]Button) (int64, error) {
	return b.Post(ctx, Out{Chat: chat, Text: text, Rows: rows})
}

// Edit replaces message msg's text, its buttons gone.
func (b *Bot) Edit(ctx context.Context, chat, msg int64, text string) error {
	return b.Change(ctx, msg, Out{Chat: chat, Text: text})
}

// Answer answers a button's press, with text as a short notice on the phone.
func (b *Bot) Answer(ctx context.Context, callback, text string) error {
	return b.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": callback, "text": text}, nil)
}
