package ui

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/tgbot"
	"github.com/lfdominguez/desk/internal/tgsetup"
)

// tgTranscribe transcribes a voice note as the user's account; tests
// replace it.
var tgTranscribe = func(ctx context.Context, a tgsetup.Account, v tgsetup.Voice) (string, error) {
	return a.Transcribe(ctx, v)
}

// tgPics are the pictures a Claude takes as images; any other file is
// handed over by its path.
var tgPics = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

// tgTake downloads the file m carries (its largest photo, or a document)
// beside the board (imagesDir, which card sessions can read) and calls
// then on the UI goroutine with the message's text: a picture as its
// "[Image 1]", in pic, another file named by its path in the text, in
// file. A voice note comes as its words (tgVoice). fail gets why it
// couldn't. False when m carries no file.
func (u *ui) tgTake(m *tgbot.Message, fail func(string), then func(text, pic, file string)) bool {
	var f tgbot.File
	switch {
	case len(m.Photo) > 0:
		f = m.Photo[len(m.Photo)-1] // the largest size
	case m.Document != nil:
		f = *m.Document
	case m.Voice != nil:
		u.tgVoice(m, fail, then)
		return true
	default:
		return false
	}
	bot, caption := u.phone.bot, strings.TrimSpace(m.Caption)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		data, err := bot.Download(ctx, f.ID)
		u.later(func() {
			if err != nil {
				fail("Couldn't take the file: " + err.Error())
				return
			}
			if typ := http.DetectContentType(data); slices.Contains(tgPics, typ) {
				path, err := u.saveImage(data, typ)
				if err != nil {
					fail("Couldn't take the file: " + err.Error())
					return
				}
				then(strings.TrimSpace(caption+" "+imgTag(1)), path, "")
				return
			}
			path, err := u.saveFile(data, f.Name)
			if err != nil {
				fail("Couldn't take the file: " + err.Error())
				return
			}
			then(strings.TrimSpace(caption+"\n\nI sent you a file: "+path), "", path)
		})
	}()
	return true
}

// tgVoice hands then voice note m's words, as Telegram's own
// transcription hears them: the bot can't ask for it, the user's account
// can (with Premium), in the bot's chat or a group desk made.
func (u *ui) tgVoice(m *tgbot.Message, fail func(string), then func(text, pic, file string)) {
	t := orgs.TelegramSettings()
	v := tgsetup.Voice{Msg: m.ID, Date: m.Date}
	for _, o := range t.Orgs {
		if o.Chat != 0 && o.Chat == m.Chat.ID {
			v.Chat = tgsetup.Chat{ID: o.Chat, Channel: o.Channel, Access: o.Access}
		}
	}
	bot, private, caption := u.phone.bot, m.Chat.Type == "private", strings.TrimSpace(m.Caption)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		var text string
		a, err := tgAccount(t)
		switch {
		case err != nil:
		case !a.LoggedIn():
			err = errors.New("log your Telegram account in (Settings → Telegram)")
		case v.Chat.Channel == 0 && !private:
			err = errors.New("desk names only the groups it made to your account")
		case v.Chat.Channel == 0:
			var me tgbot.Me
			if me, err = bot.Me(ctx); err == nil {
				v.Bot = me.Username
				text, err = tgTranscribe(ctx, a, v)
			}
		default:
			text, err = tgTranscribe(ctx, a, v)
		}
		u.later(func() {
			if text = strings.TrimSpace(text); err == nil && text == "" {
				err = errors.New("Telegram heard no words in it")
			}
			if err != nil {
				fail("Couldn't transcribe the voice note: " + firstLine(err.Error()))
				return
			}
			then(strings.TrimSpace(caption+"\n\n🎙 "+text), "", "")
		})
	}()
}

// saveFile writes a file sent from Telegram, named name, to imagesDir, by
// time.
func (u *ui) saveFile(b []byte, name string) (string, error) {
	if err := os.MkdirAll(u.imagesDir(), 0o700); err != nil {
		return "", err
	}
	switch name = filepath.Base(name); {
	case name == "." || name == "/":
		name = "file"
	case strings.HasPrefix(name, "."):
		name = "file" + name
	}
	p := filepath.Join(u.imagesDir(), time.Now().Format("20060102-150405.000")+"-"+name)
	return p, os.WriteFile(p, b, 0o600)
}
