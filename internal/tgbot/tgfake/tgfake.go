// Package tgfake is a fake Telegram Bot API for tests: it answers every
// method tgbot calls, records each call, numbers messages and topics, and
// hands out the updates a test pushes. Point tgbot.API at URL.
package tgfake

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Call is one request the fake answered: its method and parameters (a
// multipart upload's fields, its file under "file").
type Call struct {
	Method string
	Params map[string]any
}

// Server is the fake.
type Server struct {
	URL string
	srv *httptest.Server

	mu      sync.Mutex
	calls   []Call
	updates []json.RawMessage
	wake    chan struct{}
	id      int64             // the last message or topic id handed out
	fail    map[string]string // a method's next answer: ok false with this description
	limit   map[string]int    // a method's next n answers: 429
	Files   map[string][]byte // what Download gets, by file id
	Me      map[string]any    // getMe's answer
}

// New starts a fake; Close ends it.
func New() *Server {
	s := &Server{wake: make(chan struct{}, 1), fail: map[string]string{}, limit: map[string]int{}, Files: map[string][]byte{},
		Me: map[string]any{"id": 7, "is_bot": true, "username": "desk_test_bot", "has_topics_enabled": true}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	s.URL = s.srv.URL
	return s
}

// Close stops the fake.
func (s *Server) Close() { s.srv.Close() }

// Push queues an update (its update_id is set) for the next getUpdates.
func (s *Server) Push(update map[string]any) {
	s.mu.Lock()
	update["update_id"] = len(s.updates) + 1
	b, _ := json.Marshal(update)
	s.updates = append(s.updates, b)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Fail makes method's next answer a refusal saying why.
func (s *Server) Fail(method, why string) {
	s.mu.Lock()
	s.fail[method] = why
	s.mu.Unlock()
}

// Limit makes method's next n answers 429 Too Many Requests.
func (s *Server) Limit(method string, n int) {
	s.mu.Lock()
	s.limit[method] = n
	s.mu.Unlock()
}

// Calls is every call to method so far ("": all).
func (s *Server) Calls(method string) []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Call
	for _, c := range s.calls {
		if method == "" || c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// Last is the last call to method's parameters (nil: none).
func (s *Server) Last(method string) map[string]any {
	cs := s.Calls(method)
	if len(cs) == 0 {
		return nil
	}
	return cs[len(cs)-1].Params
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/file/") {
		s.mu.Lock()
		data, ok := s.Files[r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
		return
	}
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	params := map[string]any{}
	if mt, mp, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "multipart/form-data" {
		mr := multipart.NewReader(r.Body, mp["boundary"])
		for p, err := mr.NextPart(); err == nil; p, err = mr.NextPart() {
			b, _ := io.ReadAll(p)
			if p.FileName() != "" {
				params["file"], params["file_name"] = string(b), p.FileName()
			} else if _, err := strconv.ParseInt(string(b), 10, 64); err == nil {
				params[p.FormName()] = json.Number(b) // as a JSON body's numbers
			} else {
				params[p.FormName()] = string(b)
			}
		}
	} else {
		d := json.NewDecoder(r.Body)
		d.UseNumber()
		d.Decode(&params)
	}
	if method == "getUpdates" {
		s.updatesFor(w, params)
		return
	}
	s.mu.Lock()
	s.calls = append(s.calls, Call{method, params})
	if n := s.limit[method]; n > 0 {
		s.limit[method] = n - 1
		s.mu.Unlock()
		fmt.Fprint(w, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 1","parameters":{"retry_after":1}}`)
		return
	}
	if why, ok := s.fail[method]; ok {
		delete(s.fail, method)
		s.mu.Unlock()
		b, _ := json.Marshal(map[string]any{"ok": false, "error_code": 400, "description": why})
		w.Write(b)
		return
	}
	var result any = true
	switch method {
	case "sendMessage", "sendRichMessage", "sendPhoto", "sendDocument", "sendWidget":
		s.id++
		result = map[string]any{"message_id": s.id, "chat": map[string]any{"id": params["chat_id"]}, "message_thread_id": params["message_thread_id"]}
	case "createForumTopic":
		s.id++
		result = map[string]any{"message_thread_id": s.id, "name": params["name"]}
	case "getMe":
		result = s.Me
	case "getFile":
		result = map[string]any{"file_path": "files/" + fmt.Sprint(params["file_id"])}
	}
	s.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"ok": true, "result": result})
	w.Write(b)
}

// updatesFor answers getUpdates with the updates from its offset on,
// waiting a little for one when there are none.
func (s *Server) updatesFor(w http.ResponseWriter, params map[string]any) {
	off := 0
	if n, ok := params["offset"].(json.Number); ok {
		o, _ := n.Int64()
		off = int(o)
	}
	for range 2 {
		s.mu.Lock()
		var out []json.RawMessage
		if off < 1 {
			off = 1
		}
		if off-1 < len(s.updates) {
			out = s.updates[off-1:]
		}
		s.mu.Unlock()
		if len(out) > 0 {
			b, _ := json.Marshal(map[string]any{"ok": true, "result": out})
			w.Write(b)
			return
		}
		select {
		case <-s.wake:
		case <-time.After(200 * time.Millisecond):
		}
	}
	fmt.Fprint(w, `{"ok":true,"result":[]}`)
}
