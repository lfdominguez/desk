package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/diff"
	"github.com/lfdominguez/desk/internal/draw"
	"github.com/lfdominguez/desk/internal/gitw"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/tgbot"
	"github.com/lfdominguez/desk/internal/tgfmt"
)

// Desk Control's pictures and reviews: drawings as PNGs (/drawings), a
// Claude's drawing suggestion with its after as a picture and Approve or
// Dismiss, and a card's Review (/review): its branch, the diff as a file,
// Merge, Push and Open PR, each asked twice.

// tgDrawMax caps the drawings /drawings offers.
var tgDrawMax = 20

// tgSendFile sends data to chat's thread off the UI goroutine.
func (u *ui) tgSendFile(chat, thread int64, kind, name string, data []byte, caption string, rows [][]tgbot.Button) {
	bot := u.phone.bot
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := bot.SendFile(ctx, chat, thread, kind, name, data, caption, rows); err != nil {
			u.later(func() { u.showToast("Telegram: "+firstLine(err.Error()), true) })
		}
	}()
}

// tgDrawings offers drawings as buttons in chat's thread: card's own and
// its organization's, or org's ("" every organization's).
func (u *ui) tgDrawings(chat, thread int64, org, card string) {
	var items []drawItem
	switch {
	case card != "":
		items = append(readDrawings(u.cardDrawingsDir(org, card)), u.drawings(org)...)
	case org != "":
		o, ok := u.gnOrg(org)
		if !ok {
			u.gnPostAt(chat, thread, "desk has no organization called "+tgfmt.Escape(org)+".", nil, "")
			return
		}
		items = u.drawings(o)
	default:
		for _, o := range u.orgList {
			items = append(items, u.drawings(o)...)
		}
	}
	if len(items) == 0 {
		u.gnPostAt(chat, thread, "No drawings yet.", nil, "")
		return
	}
	var rows [][]tgbot.Button
	for _, it := range items[:min(len(items), tgDrawMax)] {
		rows = append(rows, []tgbot.Button{u.gnButton("🖼 "+it.name, "", func() string {
			u.tgDrawing(chat, thread, it.path)
			return "Sending " + it.name
		})})
	}
	u.gnPostAt(chat, thread, "**Drawings**\n\nPick one to see it.", rows, "")
}

// tgDrawing sends the drawing at path as a picture, as it is now.
func (u *ui) tgDrawing(chat, thread int64, path string) {
	s, err := u.drawScene(path)
	if err != nil {
		u.gnPostAt(chat, thread, tgfmt.Escape("Couldn't read it: "+err.Error()), nil, "")
		return
	}
	c, _, _ := draw.Preview(s, draw.Patch{}) // a copy: the open view's editor stays the UI goroutine's
	name := drawName(path)
	go func() {
		png, _ := draw.PNG(c, draw.Options{Scale: 2, Padding: 20, MaxSide: 2560}) // encoding to memory: no error
		u.later(func() {
			u.tgSendFile(chat, thread, "photo", name+".png", png, "🖼 "+name, nil)
		})
	}()
}

// tgSuggest shows a Claude's suggestion sg for the drawing at path (after
// it, as a picture) with Approve and Dismiss: in card's topic when it has
// one, else the private chat (by phoneTell's rule on focus).
func (u *ui) tgSuggest(path string, w drawWho, sg drawSuggestion, after *draw.Scene) {
	ph := u.phone
	if ph == nil || ph.chat == 0 {
		return
	}
	chat, thread := ph.chat, u.gtThread("inbox")
	t := (*rmTopic)(nil)
	if u.rm != nil {
		t = u.rm.topics[w.card]
	}
	if c := u.b.Card(w.card); c != nil && t != nil && t.Thread != 0 {
		if mode, _ := u.rmMirror(c); mode != orgs.MirrorOff {
			chat, thread = t.Chat, t.Thread
		}
	}
	if chat == ph.chat && !ph.always && u.anyFocused() {
		return
	}
	row := []tgbot.Button{u.gnButton("Approve", "success", func() string { return u.drawApproveAt(path, sg.ID) })}
	if !strings.HasPrefix(sg.Who, "infra:") { // as in desk: the infra Claude has no conversation to send it to
		row = append(row, u.gnButton("Rework", "", func() string {
			ph.rework = &tgRework{chat: chat, thread: thread, org: w.org, path: path, id: sg.ID}
			u.gnPostAt(chat, thread, tgfmt.Escape("What should "+sg.Name+" change? Your next message here goes back to it; a command cancels."), nil, "")
			return "Write your comment"
		}))
	}
	rows := [][]tgbot.Button{append(row, u.gnButton("Dismiss", "danger", func() string { return u.drawDismissAt(path, sg.ID) }))}
	caption := tgfmt.Clip(tgfmt.Redact(sg.Name+" suggests on "+drawName(path)+": "+sg.Note), 1000)
	go func() {
		png, _ := draw.PNG(after, draw.Options{Scale: 2, Padding: 20, MaxSide: 2560})
		u.later(func() {
			u.tgSendFile(chat, thread, "photo", drawName(path)+".png", png, caption, rows)
		})
	}()
}

// drawApproveAt approves suggestion id of the drawing at path: through its
// open view, else on the file. What happened, for the button's answer.
func (u *ui) drawApproveAt(path, id string) string {
	if st := u.draws[path]; st != nil {
		n := len(st.side.Suggestions)
		u.drawApprove(st, id)
		if len(st.side.Suggestions) == n {
			return "It's gone, or no longer fits"
		}
		return "Approved"
	}
	side := loadSide(path)
	i := slices.IndexFunc(side.Suggestions, func(s drawSuggestion) bool { return s.ID == id })
	if i < 0 {
		return "Already gone"
	}
	s, err := draw.Load(path)
	if err == nil {
		_, err = draw.NewEditor(s).Apply(side.Suggestions[i].Patch)
	}
	if err == nil {
		err = s.Save(path)
	}
	if err != nil {
		return "Couldn't: " + firstLine(err.Error())
	}
	side.Suggestions = slices.Delete(side.Suggestions, i, i+1)
	saveSide(path, side)
	u.drawRead()
	return "Approved"
}

// tgRework is a suggestion waiting for the user's comment in chat's
// thread, after its Rework.
type tgRework struct {
	chat, thread int64
	org, path    string
	id           string
}

// tgReworkTake takes m as the comment for the suggestion whose Rework was
// tapped in m's chat and thread; a command (or a message elsewhere) drops
// the wait and goes on as usual.
func (u *ui) tgReworkTake(m *tgbot.Message) bool {
	rw := u.phone.rework
	if rw == nil || rw.chat != m.Chat.ID || rw.thread != m.Thread {
		return false
	}
	u.phone.rework = nil
	text := strings.TrimSpace(m.Text)
	if text == "" || strings.HasPrefix(text, "/") {
		return false
	}
	u.gnPostAt(rw.chat, rw.thread, tgfmt.Escape(u.drawReworkAt(rw.org, rw.path, rw.id, text)), nil, "")
	return true
}

// drawReworkAt sends suggestion id of the drawing at path back to its
// Claude with comment, through drawRework (the drawing is loaded, not
// shown, when it isn't open: its Claude lives on its state).
func (u *ui) drawReworkAt(org, path, id, comment string) string {
	st := u.draws[path]
	if st == nil {
		var err error
		if st, err = u.drawLoad(org, path); err != nil {
			return "Couldn't: " + firstLine(err.Error())
		}
	}
	i := slices.IndexFunc(st.side.Suggestions, func(s drawSuggestion) bool { return s.ID == id })
	if i < 0 {
		return "That suggestion is gone."
	}
	if err := u.drawRework(st, id, comment); err != nil {
		return "Couldn't send it back: " + firstLine(err.Error())
	}
	return "Sent back to " + st.side.Suggestions[i].Name + ": its next suggestion replaces this one."
}

// drawDismissAt drops suggestion id of the drawing at path.
func (u *ui) drawDismissAt(path, id string) string {
	if st := u.draws[path]; st != nil {
		if _, ok := u.drawTake(st, id); !ok {
			return "Already gone"
		}
		return "Dismissed"
	}
	side := loadSide(path)
	n := len(side.Suggestions)
	side.Suggestions = slices.DeleteFunc(side.Suggestions, func(s drawSuggestion) bool { return s.ID == id })
	if len(side.Suggestions) == n {
		return "Already gone"
	}
	saveSide(path, side)
	return "Dismissed"
}

// tgReview sends card id's Review to chat's thread: its branch against
// its base, the files changed, the diff as a file, and buttons to merge,
// push or open a request, each asked again before it runs.
func (u *ui) tgReview(chat, thread int64, id string) {
	c := u.b.Card(id)
	if c == nil || c.Worktree == "" {
		u.gnPostAt(chat, thread, tgfmt.Escape("It has no worktree: Review works on cards started with [git] worktrees on."), nil, "")
		return
	}
	wt, base, key := c.Worktree, c.Base, c.Key()
	bot := u.phone.bot
	go func() {
		s, err := gitw.State(wt, base)
		var fs []gitw.File
		if err == nil {
			fs, err = gitw.Files(wt, base, rvMaxFiles)
		}
		u.later(func() {
			if err != nil {
				u.gnPostAt(chat, thread, tgfmt.Escape(key+": couldn't read its branch: "+firstLine(err.Error())), nil, "")
				return
			}
			md, patch := rvText(key, s, fs)
			var rows [][]tgbot.Button
			if !s.Merged {
				ask := func(what, label, style string) tgbot.Button {
					return u.gnButton(label, style, func() string { return u.tgConfirm(chat, thread, id, what, label) })
				}
				rows = [][]tgbot.Button{{ask("merge", "Merge", "success"), ask("push", "Push", ""), ask("pr", "Open PR", "primary")}}
			}
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				_, err := bot.Post(ctx, tgbot.Out{Chat: chat, Thread: thread, Markdown: clipLines(tgfmt.Redact(md), tgfmt.Max-96), Rows: rows})
				if err == nil && patch != "" {
					_, err = bot.SendFile(ctx, chat, thread, "document", key+".diff", []byte(tgfmt.Redact(patch)), "", nil)
				}
				if err != nil {
					u.later(func() { u.showToast("Telegram: "+firstLine(err.Error()), true) })
				}
			}()
		})
	}()
}

// rvText is a Review as Markdown for Telegram, and its diff as a patch.
func rvText(key string, s gitw.Status, fs []gitw.File) (md, patch string) {
	var b, p strings.Builder
	fmt.Fprintf(&b, "**%s · `%s`** into `%s`\n\n", tgfmt.Escape(key), s.Branch, s.Base)
	switch {
	case s.Merged:
		b.WriteString("Merged already.\n")
	default:
		fmt.Fprintf(&b, "- %d commit%s ahead, %d behind\n", s.Ahead, map[bool]string{true: "", false: "s"}[s.Ahead == 1], s.Behind)
	}
	if len(s.Dirty) > 0 {
		fmt.Fprintf(&b, "- %d change%s not committed\n", len(s.Dirty), map[bool]string{true: "", false: "s"}[len(s.Dirty) == 1])
	}
	for _, c := range s.Commits[:min(len(s.Commits), 10)] {
		b.WriteString("- " + tgfmt.Escape(c.Subject) + "\n")
	}
	if len(fs) > 0 {
		b.WriteString("\n### Files\n")
	}
	for i, f := range fs {
		if f.Binary {
			fmt.Fprintf(&p, "Binary file %s changed\n", f.Path)
			if i < 30 {
				fmt.Fprintf(&b, "- `%s` binary\n", f.Path)
			}
			continue
		}
		text, add, del := diff.Unified(f.Old, f.New, 3)
		fmt.Fprintf(&p, "--- a/%s\n+++ b/%s\n%s", f.Path, f.Path, text)
		if i < 30 {
			fmt.Fprintf(&b, "- `%s` \\+%d −%d\n", f.Path, add, del)
		}
	}
	if len(fs) > 30 {
		fmt.Fprintf(&b, "- … %d more\n", len(fs)-30)
	}
	return b.String(), p.String()
}

// tgConfirm asks once more before Review's what (merge, push, pr) runs on
// card id: merge, push and PR take a second tap.
func (u *ui) tgConfirm(chat, thread int64, id, what, label string) string {
	c := u.b.Card(id)
	if c == nil {
		return "The card is gone"
	}
	q := map[string]string{
		"merge": "Merge `" + c.Branch + "` into `" + c.Base + "`? The card goes to Done.",
		"push":  "Push `" + c.Branch + "`?",
		"pr":    "Push `" + c.Branch + "` and open a request into `" + c.Base + "`?",
	}[what]
	rows := [][]tgbot.Button{{
		u.gnButton(label, "danger", func() string {
			if what == "merge" {
				u.rvOf(id).armed = "merge" // asked twice already
			}
			u.rvAct(id, what, func(s string) { u.gnPostAt(chat, thread, tgfmt.Escape(s), nil, "") })
			return label + "…"
		}),
		u.gnButton("Cancel", "", func() string { return "Cancelled" }),
	}}
	u.gnPostAt(chat, thread, tgfmt.Escape(c.Key()+": ")+q, rows, "")
	return "Tap " + label + " again to go on"
}

// tgCanvas sends card id's canvas to chat's thread as a picture: its graph
// drawn as a drawing (cardScene), since the canvas itself needs a GPU.
func (u *ui) tgCanvas(chat, thread int64, id string) {
	c := u.b.Card(id)
	s, key := cardScene(u.cardGraph(c, time.Now())), c.Key()
	go func() {
		png, _ := draw.PNG(s, draw.Options{Scale: 2, Padding: 20, MaxSide: 2560}) // encoding to memory: no error
		u.later(func() { u.tgSendFile(chat, thread, "photo", key+".png", png, "🧭 "+key+"'s canvas", nil) })
	}()
}

// cardScene is graph g as a drawing: a box per node (its title, its
// Claude's status, its values), an arrow per wire, laid out as the canvas
// does, spread by half again so the hand-drawn text fits.
func cardScene(g cvGraph) *draw.Scene {
	cvLayout(g.nodes)
	const k = 1.5
	var p draw.Patch
	add := func(m map[string]any) {
		b, _ := json.Marshal(m)
		p.Add = append(p.Add, b)
	}
	for _, n := range g.nodes {
		lines := []string{n.title}
		if n.who != nil && n.who.status != "" {
			lines = append(lines, n.who.status)
		}
		for _, r := range n.rows {
			if r.value != "" { // a bare socket's name says nothing without its wire
				lines = append(lines, r.name+"  "+r.value)
			}
		}
		op := 100
		if n.dim {
			op = 50
		}
		add(map[string]any{"id": n.id, "type": "rectangle", "x": float64(n.r.Min.X) * k, "y": float64(n.r.Min.Y) * k,
			"width": float64(n.r.Dx()) * k, "height": float64(n.r.Dy()) * k, "strokeColor": hexOf(famColors[n.fam]),
			"roughness": 0, "opacity": op, "label": strings.Join(lines, "\n")})
	}
	for _, w := range g.wires {
		if g.node(w.from) != nil && g.node(w.to) != nil {
			add(map[string]any{"type": "arrow", "roughness": 0, "strokeColor": "#868e96", "from": w.from, "to": w.to})
		}
	}
	ed := draw.NewEditor(draw.NewScene())
	ed.Apply(p) // made from the graph: nothing in it is wrong
	return ed.Scene
}
