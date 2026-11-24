package ui

// The Sessions section's workspace screenshots: a card's IDE view, opened
// from an Edit's diff, and its Claude answering about the change.

import (
	"image"
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/kanban"
)

func init() {
	scenes = append(scenes, scene{"sess-workspace", sessWorkspace}, scene{"sess-workspace-find", sessWorkspaceFind})
}

// sessWorkspace opens the rate-limit card's login.go change in its
// workspace (the demo world's Acme/api has the file), then asks about it.
func sessWorkspace(s *shooter) {
	id := sessWSChange(s)
	open, ws := s.at("diffWS"+id+"-1"), s.at("pnWS")
	s.move(sessPt(10, 10))
	s.shot("sess-workspace-open", cut{"diff", around(open, 4), true}, cut{"btn", around(ws, 4), true})
	s.click("diffWS" + id + "-1")
	s.settle()
	s.u.wsSend(id, "Is refreshing inside the loop right?")
	s.wait("the workspace Claude's answer", 10*time.Second, func() bool {
		m := s.u.chat(wsKey(id))
		return len(m) > 0 && strings.HasPrefix(m[len(m)-1].Text, "Yes: `s.Expired()`")
	})
	s.settle()
	ask := s.at("wsAskChange")
	s.move(sessPt(10, 10))
	s.shot("sess-workspace",
		cut{"tree", image.Rect(scaled(image.Pt(233, 45)).X, sessTop, scaled(image.Pt(233+262, 0)).X, scaled(shotSize).Y), false},
		cut{"bar", sessRows(scaled(image.Pt(233+262, 0)).X, ask.Min.Y-scaled(image.Pt(0, 6)).Y, ask.Max.Y+scaled(image.Pt(0, 6)).Y).Intersect(image.Rect(0, 0, scaled(image.Pt(shotSize.X-401, 0)).X, scaled(shotSize).Y)), false},
		cut{"chat", image.Rect(scaled(image.Pt(shotSize.X-400, 0)).X, sessTop, scaled(shotSize).X, scaled(shotSize).Y), false})
}

// sessWorkspaceFind is the workspace's find and replace bar over login.go,
// then quick open (Ctrl+P) on its files.
func sessWorkspaceFind(s *shooter) {
	id := sessWSChange(s)
	s.click("diffWS" + id + "-1")
	st := s.u.wsOf(id)
	s.u.wsFindOpen(st, st.tab(), findReplace)
	s.settle()
	s.typ("token")
	st.find.rep.SetText("accessToken")
	s.settle()
	bar := union(s.at("wsFindX"), s.at("wsReplaceAll"))
	s.move(sessPt(10, 10))
	editor := image.Rect(scaled(image.Pt(233+262, 0)).X, 0, scaled(image.Pt(shotSize.X-401, 0)).X, scaled(shotSize).Y)
	s.shot("sess-workspace-find", cut{"bar", image.Rect(editor.Min.X, bar.Min.Y-scaled(image.Pt(0, 14)).Y, editor.Max.X, bar.Max.Y+scaled(image.Pt(0, 420)).Y), false})
	s.u.wsFindClose(st)
	s.settle()
	s.u.openFiles()
	s.settle()
	s.typ("go")
	s.shot("sess-workspace-files", cut{"box", dp(444, 78, 1156, 392), false})
}

// sessWSChange is the rate-limit card with its login.go change in the
// chat, open in the full window; its id.
func sessWSChange(s *shooter) string {
	id := sessID(s, sessRate)
	c := s.u.b.Card(id)
	c.Status, c.Session = kanban.StatusWaiting, kanban.Session{ID: "demo-ws", Turns: 1, Tokens: 30000, Cost: 0.31}
	s.u.moveTo(id, "In progress") // c is stale after
	s.u.save()
	login := s.u.home + "/Work/Acme/api/internal/auth/login.go"
	now := time.Now()
	s.u.chats[id] = []kanban.ChatMsg{
		{Who: "you", Text: "The login retry logs users out once their token expires.", At: now.Add(-6 * time.Minute)},
		{Who: "tool", Text: "Edit  internal/auth/login.go", Path: login, At: now.Add(-4 * time.Minute), Edits: []kanban.Edit{{
			Old: "\tfor try := 0; try < 3; try++ {\n\t\tresp, err = c.do(req, s.Token)\n",
			New: "\tfor try := 0; try < 3; try++ {\n\t\tif s.Expired() {\n\t\t\tif err := refresh.Token(ctx, s); err != nil {\n\t\t\t\treturn nil, err\n\t\t\t}\n\t\t}\n\t\tresp, err = c.do(req, s.Token)\n"}}},
		{Who: "claude", Text: "The retry refreshes an expired token first now.", At: now.Add(-3 * time.Minute)},
	}
	s.u.openSession(id)
	s.settle()
	return id
}
