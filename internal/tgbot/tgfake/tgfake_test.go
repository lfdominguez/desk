package tgfake

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

func post(t *testing.T, s *Server, method string, body any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(s.URL+"/botT/"+method, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return out
}

// TestFake: messages and topics are numbered, calls kept, getMe and
// getFile answered, files served; Fail and Limit answer the next calls;
// pushed updates come from their offset on, and none after a short wait.
func TestFake(t *testing.T) {
	s := New()
	defer s.Close()
	if r := post(t, s, "sendMessage", map[string]any{"chat_id": 5, "text": "hi"}); r["result"].(map[string]any)["message_id"] != float64(1) {
		t.Fatal(r)
	}
	if r := post(t, s, "createForumTopic", map[string]any{"chat_id": -5, "name": "x"}); r["result"].(map[string]any)["message_thread_id"] != float64(2) {
		t.Fatal(r)
	}
	if r := post(t, s, "getMe", nil); r["result"].(map[string]any)["username"] != "desk_test_bot" {
		t.Fatal(r)
	}
	if r := post(t, s, "getFile", map[string]any{"file_id": "a"}); r["result"].(map[string]any)["file_path"] != "files/a" {
		t.Fatal(r)
	}
	if r := post(t, s, "pinChatMessage", map[string]any{}); r["result"] != true {
		t.Fatal(r)
	}
	if len(s.Calls("")) != 5 || s.Last("sendMessage")["text"] != "hi" || s.Last("nothing") != nil {
		t.Fatal(s.Calls(""))
	}
	s.Files["a"] = []byte("data")
	resp, _ := http.Get(s.URL + "/file/botT/files/a")
	if b, _ := io.ReadAll(resp.Body); string(b) != "data" {
		t.Fatal(string(b))
	}
	if resp, _ := http.Get(s.URL + "/file/botT/files/b"); resp.StatusCode != 404 {
		t.Fatal(resp.Status)
	}
	s.Fail("sendMessage", "Forbidden")
	if r := post(t, s, "sendMessage", nil); r["ok"] != false || r["description"] != "Forbidden" {
		t.Fatal(r)
	}
	s.Limit("sendMessage", 1)
	if r := post(t, s, "sendMessage", nil); r["error_code"] != float64(429) {
		t.Fatal(r)
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	w.WriteField("chat_id", "-5")
	w.WriteField("caption", "a picture")
	f, _ := w.CreateFormFile("photo", "p.png")
	f.Write([]byte("PNG"))
	w.Close()
	http.Post(s.URL+"/botT/sendPhoto", w.FormDataContentType(), &body)
	if p := s.Last("sendPhoto"); p["file"] != "PNG" || p["file_name"] != "p.png" || p["caption"] != "a picture" || p["chat_id"].(json.Number) != "-5" {
		t.Fatal(p)
	}

	if r := post(t, s, "getUpdates", map[string]any{"offset": 0}); len(r["result"].([]any)) != 0 {
		t.Fatal(r)
	}
	s.Push(map[string]any{"message": map[string]any{"text": "a"}})
	s.Push(map[string]any{"message": map[string]any{"text": "b"}}) // the wake already waiting: dropped, the update kept
	r := post(t, s, "getUpdates", map[string]any{"offset": 2})
	if us := r["result"].([]any); len(us) != 1 || !strings.Contains(js(us[0]), `"text":"b"`) {
		t.Fatal(r)
	}
	if r := post(t, s, "getUpdates", map[string]any{}); len(r["result"].([]any)) != 2 {
		t.Fatal(r)
	}
}

func js(v any) string { b, _ := json.Marshal(v); return string(b) }
