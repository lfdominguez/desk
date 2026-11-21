package tgbot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBot: each call posts its method with the token and reads Telegram's
// answer; ok false and a body that isn't JSON say why.
func TestBot(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		json.NewDecoder(r.Body).Decode(&got)
		switch r.URL.Path {
		case "/botT0K/getUpdates":
			fmt.Fprint(w, `{"ok":true,"result":[{"update_id":5,"message":{"message_id":9,"chat":{"id":42},"from":{"username":"me"},"text":"hi","reply_to_message":{"message_id":3}}},
				{"update_id":6,"callback_query":{"id":"cb","data":"a1","message":{"message_id":4,"chat":{"id":42}}}}]}`)
		case "/botT0K/sendMessage":
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":77}}`)
		case "/botT0K/editMessageText", "/botT0K/answerCallbackQuery":
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		case "/botBAD/sendMessage":
			fmt.Fprint(w, `{"ok":false,"description":"Unauthorized"}`)
		default:
			http.Error(w, "nope", 502)
		}
	}))
	defer srv.Close()
	API = srv.URL
	b, ctx := &Bot{Token: "T0K"}, context.Background()

	us, err := b.Updates(ctx, 5)
	if err != nil || len(us) != 2 || us[0].Message.Text != "hi" || us[0].Message.Chat.ID != 42 || us[0].Message.Reply.ID != 3 || us[1].Callback.Data != "a1" || got["offset"] != float64(5) {
		t.Fatalf("%+v %v %v", us, err, got)
	}
	id, err := b.Send(ctx, 42, "Allow?", [][]Button{{{Text: "Allow", Data: "a1"}}})
	kb, _ := json.Marshal(got["reply_markup"])
	if err != nil || id != 77 || !strings.Contains(string(kb), `"callback_data":"a1"`) {
		t.Fatalf("%d %v %s", id, err, kb)
	}
	if _, err := b.Send(ctx, 42, "plain", nil); err != nil || got["reply_markup"] != nil {
		t.Fatalf("plain %v %v", err, got)
	}
	if err := b.Edit(ctx, 42, 77, "done"); err != nil || got["message_id"] != float64(77) {
		t.Fatalf("edit %v", err)
	}
	if err := b.Answer(ctx, "cb", "ok"); err != nil || got["callback_query_id"] != "cb" {
		t.Fatalf("answer %v", err)
	}
	if _, err := (&Bot{Token: "BAD"}).Send(ctx, 1, "x", nil); err == nil || err.Error() != "telegram sendMessage: Unauthorized" {
		t.Fatalf("refused %v", err)
	}
	if err := (&Bot{Token: "NONE"}).Edit(ctx, 1, 1, "x"); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("not json %v", err)
	}
	API = "http://127.0.0.1:1"
	if _, err := b.Updates(ctx, 0); err == nil {
		t.Fatal("no server")
	}
	API = "://bad"
	if err := b.Answer(ctx, "cb", ""); err == nil {
		t.Fatal("bad url")
	}
}
