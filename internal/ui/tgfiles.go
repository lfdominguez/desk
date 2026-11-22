package ui

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/tgbot"
)

// tgPics are the pictures a Claude takes as images; any other file is
// handed over by its path.
var tgPics = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

// tgTake downloads the file m carries (its largest photo, or a document)
// beside the board (imagesDir, which card sessions can read) and calls
// then on the UI goroutine with the message's text: a picture as its
// "[Image 1]", in pic, another file named by its path in the text, in
// file. fail gets why it couldn't. False when m carries no file.
func (u *ui) tgTake(m *tgbot.Message, fail func(string), then func(text, pic, file string)) bool {
	var f tgbot.File
	switch {
	case len(m.Photo) > 0:
		f = m.Photo[len(m.Photo)-1] // the largest size
	case m.Document != nil:
		f = *m.Document
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
