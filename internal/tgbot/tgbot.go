// Package tgbot is the little of Telegram's Bot API desk uses to reach
// the user's phone: long polling for updates (no port opened), messages
// with buttons, and answering a button's press. Plain net/http.
package tgbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
}

// Update is something that happened: a message, or a button pressed.
type Update struct {
	ID       int64     `json:"update_id"`
	Message  *Message  `json:"message"`
	Callback *Callback `json:"callback_query"`
}

// Message is a chat message.
type Message struct {
	ID   int64 `json:"message_id"`
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	From struct {
		Username string `json:"username"`
	} `json:"from"`
	Text  string   `json:"text"`
	Reply *Message `json:"reply_to_message"`
}

// Callback is a button pressed under one of the bot's messages.
type Callback struct {
	ID      string   `json:"id"`
	Data    string   `json:"data"`
	Message *Message `json:"message"`
}

// Button is an inline button; Data comes back in its Callback.
type Button struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

// call posts method's params and decodes its result into v (nil: none).
func (b *Bot) call(ctx context.Context, method string, params, v any) error {
	body, _ := json.Marshal(params)
	req, err := http.NewRequestWithContext(ctx, "POST", API+"/bot"+b.Token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c := b.Client
	if c == nil {
		c = &http.Client{Timeout: Poll + 20*time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("telegram %s: %s", method, resp.Status)
	}
	if !out.OK {
		return fmt.Errorf("telegram %s: %s", method, out.Description)
	}
	if v != nil {
		return json.Unmarshal(out.Result, v)
	}
	return nil
}

// Updates waits up to Poll for the updates after offset (the last one's
// ID plus one).
func (b *Bot) Updates(ctx context.Context, offset int64) ([]Update, error) {
	var us []Update
	err := b.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": int(Poll.Seconds()),
		"allowed_updates": []string{"message", "callback_query"}}, &us)
	return us, err
}

// Send sends text to chat with rows of buttons under it (none: nil); the
// message's ID.
func (b *Bot) Send(ctx context.Context, chat int64, text string, rows [][]Button) (int64, error) {
	p := map[string]any{"chat_id": chat, "text": text}
	if len(rows) > 0 {
		p["reply_markup"] = map[string]any{"inline_keyboard": rows}
	}
	var m Message
	err := b.call(ctx, "sendMessage", p, &m)
	return m.ID, err
}

// Edit replaces message msg's text, its buttons gone.
func (b *Bot) Edit(ctx context.Context, chat, msg int64, text string) error {
	return b.call(ctx, "editMessageText", map[string]any{"chat_id": chat, "message_id": msg, "text": text}, nil)
}

// Answer answers a button's press, with text as a short notice on the phone.
func (b *Bot) Answer(ctx context.Context, callback, text string) error {
	return b.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": callback, "text": text}, nil)
}
