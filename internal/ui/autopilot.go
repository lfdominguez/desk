package ui

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
)

// Autopilot: one card (Card.Pilot) whose Claude runs a card per ticket
// (their Card.Parent). The cards' questions, permission requests and turn
// ends go to it, never to the user; it answers them through its pilot tools
// (infra.PilotMCP), or ends its own turn asking the user, and only it
// notifies. What it may decide is its level and the user's grants
// (pilotPrompt): changes outside the cards' folders are the user's unless
// granted.

// pilotOf is the autopilot running card c on the board, nil none.
func (u *ui) pilotOf(c *kanban.Card) *kanban.Card {
	if c == nil || c.Parent == "" {
		return nil
	}
	if col, i := u.b.Find(c.Parent); col >= 0 && u.b.Columns[col].Cards[i].Pilot != nil {
		return &u.b.Columns[col].Cards[i]
	}
	return nil
}

// pilotHeld says whether card c's tickets wait for its autopilot: moving a
// ticket changes something outside, so they follow their cards only once
// the user moves the autopilot to Done (or it leaves the board).
func (u *ui) pilotHeld(c *kanban.Card) bool {
	pl := u.pilotOf(c)
	if pl == nil {
		return false
	}
	col, _ := u.b.Find(pl.ID)
	return col != u.b.Column("Done")
}

// pilotFollow takes the cards of every autopilot in Done there too, done:
// an autopilot and its cards are one piece of work, so its Done is theirs
// (however it got there: a drag, a key, its Claude's status). save runs it.
func (u *ui) pilotFollow() {
	done := u.b.Column("Done")
	if done < 0 {
		return
	}
	var ids []string
	for _, c := range u.b.Columns[done].Cards {
		if c.Pilot != nil {
			ids = append(ids, c.ID)
		}
	}
	for _, p := range ids {
		for _, c := range u.boardCards() {
			if col, _ := u.b.Find(c.ID); c.Parent == p && col != done {
				u.b.MoveBefore(c.ID, done, "")
				k := u.b.Card(c.ID)
				k.Status, k.Note = kanban.StatusDone, ""
			}
		}
	}
}

// pilotSpent is what autopilot id and its cards' sessions have cost.
func (u *ui) pilotSpent(id string) float64 {
	n := u.b.Card(id).Session.Cost
	for _, k := range u.pilotKids(id) {
		n += u.b.Card(k).Session.Cost
	}
	return n
}

// pilotOver is overBudget's part for card c of an autopilot's group: its
// budget's name once the group spent it since the user's last go-ahead.
func (u *ui) pilotOver(c *kanban.Card) string {
	pl := c
	if c.Pilot == nil {
		pl = u.pilotOf(c)
	}
	if pl == nil || pl.Pilot.Budget <= 0 || u.pilotSpent(pl.ID)-pl.Pilot.From < pl.Pilot.Budget {
		return ""
	}
	return fmt.Sprintf("the autopilot's $%.2f budget", pl.Pilot.Budget)
}

// granted says whether quote is part of the user's grants: the autopilot
// allows a change outside only by quoting the words that allow it.
func granted(grants, quote string) bool {
	q := strings.ToLower(strings.TrimSpace(quote))
	return len(q) >= 4 && strings.Contains(strings.ToLower(grants), q)
}

// youNote starts the note of an autopilot's card whose request went to the
// user: those notify, the rest of its cards' don't.
const youNote = "Needs you: "

// pilotToUser hands card id's open permission request to the user, as any
// card's: a change outside its folders the user didn't grant.
func (u *ui) pilotToUser(id string) {
	c := u.b.Card(id)
	c.Status, c.Note = kanban.StatusWaiting, youNote+"wants to run "+u.asks[id].r.Line+": allow or deny"
	u.say(id, "tool", "asks you to run "+u.asks[id].r.Line+" (outside its folders, not granted)")
}

// pilotKids is autopilot id's cards on the board, in board order. Kept
// until the board changes: the board's cards ask every frame.
func (u *ui) pilotKids(id string) []string {
	if k := fmt.Sprint(u.gen, u.cardCount()); k != u.kidsKey {
		u.kidsKey, u.kids = k, map[string][]string{}
		for _, c := range u.boardCards() {
			if c.Parent != "" {
				u.kids[c.Parent] = append(u.kids[c.Parent], c.ID)
			}
		}
	}
	return u.kids[id]
}

// pilotDone is how many of autopilot id's cards are finished (review or done).
func (u *ui) pilotDone(id string) (done, all int) {
	for _, k := range u.pilotKids(id) {
		if c := u.b.Card(k); c != nil && (c.Status == kanban.StatusReview || c.Status == kanban.StatusDone) {
			done++
		}
	}
	return done, len(u.pilotKids(id))
}

// startPilot makes autopilot card title in org over tickets: a card per
// ticket (its own, when the board has one) under the autopilot's, then
// starts the autopilot's Claude. Its key.
func (u *ui) startPilot(org, title string, ts []links.Ticket, p kanban.Pilot) string {
	cfg := u.orgConfig(org)
	todo := max(0, u.b.Column("To do"))
	var b strings.Builder
	fmt.Fprintf(&b, "Run these %d tickets to done, one card each:\n", len(ts))
	pc := u.b.Add(todo, kanban.Card{Title: title, Org: org, Pilot: &p, Labels: []string{"autopilot"}})
	for _, t := range ts {
		c := u.ticketCard(t)
		if c == nil {
			nc := kanban.Card{Title: t.Key + " " + t.Title, Org: org}
			if l, ok := links.Parse(t.URL, cfg); ok {
				nc.Links = []kanban.Link{l}
			}
			added := u.b.Add(todo, nc)
			c = u.b.Card(added.ID)
		}
		c.Parent = pc.ID
		fmt.Fprintf(&b, "- %s: %s %s (%s)\n", c.Key(), t.Key, t.Title, t.URL)
	}
	u.b.Card(pc.ID).Notes = b.String()
	u.save()
	u.send(pc.ID, startWork)
	return pc.Key()
}

// pilotLevels is how the dialog names each level.
var pilotLevels = []struct{ key, name, desc string }{
	{kanban.PilotSupervised, "Supervised", "It answers questions about the work; anything past reading and editing the cards' folders asks you."},
	{kanban.PilotAuto, "Autopilot", "It decides everything inside the cards' folders; changes outside them ask you, unless you allow them below."},
}

// pilotPrompt is an autopilot card's Claude's part of its system prompt.
func pilotPrompt(c *kanban.Card) string {
	p := c.Pilot
	rule := "Allow only what reads, edits inside the cards' folders, and local builds and tests. Everything else goes to the user."
	if p.Level == kanban.PilotAuto {
		rule = "Decide everything inside the cards' folders yourself: edits, builds, tests, local commits and branches. Anything that changes " +
			"something outside them (pushing, opening, commenting on or merging pull requests, moving or editing tickets, deploying, " +
			"touching servers, sending anything) goes to the user, unless the user's grants below clearly allow it."
	}
	grants := cmp.Or(strings.TrimSpace(p.Grants), "nothing beyond the level")
	budget := ""
	if p.Budget > 0 {
		budget = fmt.Sprintf("\n- You and your cards may spend $%.2f together; past it desk pauses all until the user's go-ahead.", p.Budget)
	}
	return fmt.Sprintf(`

You are %s, an autopilot: you run other cards to finish their tickets, and never do their work yourself (you read only to judge).
- Start a card with pilot_tell, at most %d working at once: brief it with its ticket, what done means and what you know of the codebase. pilot_cards lists them.
- Their questions, permission requests and finished turns come to you as messages from them, not to the user. Answer requests with pilot_answer; steer with pilot_tell.
- %s
- The user allows, without asking: %s.
- desk holds you to it: allowing a request that changes something outside the cards' folders needs grant=<the user's words above that allow it>, else it goes to the user; with no grants such requests go to the user straight. Leave any request to the user with pilot_answer user=true.
- The cards' tickets move only once the user moves you to Done.%s
- To ask the user, end your turn with the question and the card it is about; their answer is your next message. Ask rarely, and batch what you can.
- A card is finished when it is ready for review. When all are, end with DESK-STATUS: done and what each card did.
`, c.Key(), cmp.Or(p.Max, 3), rule, grants, budget)
}

// pilotChildPrompt is a card's part of its system prompt under autopilot pl.
func pilotChildPrompt(pl *kanban.Card) string {
	return "\n\nAn autopilot, " + pl.Key() + ", runs this card for the user: your questions and permission requests go to it, and messages " +
		"\"From the autopilot\" are its. Work on without waiting for the user; say plainly when you are done or stuck.\n"
}

// external says whether a permission request's line looks like a change
// outside the card's folders: the autopilot allows such only quoting the
// user's grants (pilotAnswer). ponytail: a list of commands, not what they
// do; a sandbox of the cards' network is the upgrade.
func external(line string) bool {
	l := strings.ToLower(line)
	for _, s := range []string{"git push", "gh ", "glab ", "curl ", "wget ", "ssh ", "scp ", "rsync ", "kubectl", "helm ", "terraform", "ansible", "docker push", "npm publish", "desk peer", "mcp__"} {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

// pilotAsk hands card id's open request to its autopilot.
func (u *ui) pilotAsk(id string) {
	c, a := u.b.Card(id), u.asks[id]
	pl := u.pilotOf(c)
	if pl == nil || a == nil {
		return
	}
	var msg string
	if qs := a.r.Questions; len(qs) > 0 {
		c.Note = "Asked the autopilot: " + qs[0].Question
		msg = "asks:\n" + interviewText(qs) + "\nAnswer with pilot_answer card=" + c.Key() + " message=<the answer>."
	} else {
		c.Note = "Asked the autopilot to run " + a.r.Line
		msg = "asks to run: " + a.r.Line
		switch {
		case external(a.r.Line) && strings.TrimSpace(pl.Pilot.Grants) == "": // nothing it could quote: the user's
			u.pilotToUser(id)
			msg = "asks the user to run: " + a.r.Line + "\nIt changes something outside the card's folders and the user granted nothing; they answer it."
		case external(a.r.Line):
			msg += "\nThis changes something outside the card's folders: allow it only with grant=<the user's words that allow it>, " +
				"else deny it or leave it to the user with pilot_answer card=" + c.Key() + " user=true."
		default:
			msg += "\nAnswer with pilot_answer card=" + c.Key() + " allow=true|false."
		}
	}
	u.save()
	u.pilotTell(pl.ID, c.Key(), msg)
}

// pilotEnded tells card id's autopilot, if it has one, that its turn
// ended, and how; unless it works on (its background work).
func (u *ui) pilotEnded(id string) {
	c := u.b.Card(id)
	pl := u.pilotOf(c)
	if pl == nil || c.Status == kanban.StatusWorking {
		return
	}
	st := cmp.Or(statusName[c.Status], c.Status)
	if c.Status == kanban.StatusWaiting {
		st = "waits for you"
	}
	msg := "ended its turn: " + st
	if c.Note != "" {
		msg += " (" + c.Note + ")"
	}
	if c.Last != "" {
		msg += ". Its last message:\n" + c.Last
	}
	u.pilotTell(pl.ID, c.Key(), msg)
}

// pilotTell brings what card from says into autopilot id's session, held
// while it can't take it, as the infra Claude's come (cardTell).
func (u *ui) pilotTell(id, from, text string) {
	u.sayMsg(id, kanban.ChatMsg{Who: "pilot", Name: from, Text: text})
	msg := "From " + from + ": " + text
	if u.terms[id] != nil || u.shellStop[id] != nil || u.asks[id] != nil {
		u.peerOut[id] += msg + "\n\n"
		return
	}
	u.deliver(id, msg, true)
}

// pilotTool answers autopilot card id's pilot tools, off the UI goroutine.
func (u *ui) pilotTool(id, name string, raw json.RawMessage) (string, bool) {
	var a struct {
		Card, Text, Message, Title, Notes, Grant string
		Allow, User                              bool
	}
	json.Unmarshal(raw, &a)
	text, err := onBoard(u, func() (string, error) {
		p := u.b.Card(id)
		if p == nil || p.Pilot == nil {
			return "", errors.New("this card runs no autopilot")
		}
		switch name {
		case "pilot_cards":
			return u.pilotCards(id), nil
		case "pilot_add":
			return u.pilotAdd(id, a.Title, a.Notes)
		}
		c := u.cardOfKey(strings.TrimSpace(a.Card))
		if c == nil || c.Parent != id {
			return "", fmt.Errorf("%s is not one of your cards", a.Card)
		}
		if name == "pilot_tell" {
			return u.pilotSend(id, c.ID, a.Text)
		}
		return u.pilotAnswer(id, c.ID, a.Allow, a.User, a.Message, a.Grant)
	})
	if err != nil {
		return err.Error(), false
	}
	return text, true
}

// pilotWorking is how many of autopilot id's cards are working now.
func (u *ui) pilotWorking(id string) (n int) {
	for _, k := range u.pilotKids(id) {
		if c := u.b.Card(k); c != nil && c.Status == kanban.StatusWorking {
			n++
		}
	}
	return n
}

// pilotCards is the pilot_cards tool: autopilot id's cards as its Claude reads them.
func (u *ui) pilotCards(id string) string {
	p := u.b.Card(id)
	var b strings.Builder
	fmt.Fprintf(&b, "Up to %d may work at once; %d do now.\n", cmp.Or(p.Pilot.Max, 3), u.pilotWorking(id))
	if bud := p.Pilot.Budget; bud > 0 {
		fmt.Fprintf(&b, "Spent $%.2f of $%.2f since the user's go-ahead.\n", u.pilotSpent(id)-p.Pilot.From, bud)
	}
	for _, k := range u.pilotKids(id) {
		c := u.b.Card(k)
		st := cmp.Or(statusName[c.Status], "not started")
		if c.Note != "" {
			st += ": " + c.Note
		}
		fmt.Fprintf(&b, "\n%s %s [%s]", c.Key(), c.Title, st)
		if u.asks[k] != nil && !strings.HasPrefix(c.Note, youNote) {
			b.WriteString(" (waits on your pilot_answer)")
		}
		for _, l := range c.Links {
			fmt.Fprintf(&b, "\n  %s %s %s", l.Ref, l.URL, l.State)
		}
		if c.Last != "" {
			b.WriteString("\n  last: " + firstLine(c.Last))
		}
	}
	return b.String()
}

// pilotSend is pilot_tell: autopilot id's message to its card kid. The
// first one starts it, while fewer than its Max work.
func (u *ui) pilotSend(id, kid, text string) (string, error) {
	p, c := u.b.Card(id), u.b.Card(kid)
	if strings.TrimSpace(text) == "" {
		return "", errors.New("say something")
	}
	if why := u.pilotOver(p); why != "" {
		return "", fmt.Errorf("your cards spent %s: end your turn telling the user; their go-ahead continues", why)
	}
	if c.Session.Turns == 0 && c.Status != kanban.StatusWorking {
		if n, mx := u.pilotWorking(id), cmp.Or(p.Pilot.Max, 3); n >= mx {
			return "", fmt.Errorf("%d of your cards already work, as many as allowed: start %s once one ends its turn", n, c.Key())
		}
	}
	key, who := c.Key(), "Autopilot "+p.Key()
	u.sayMsg(kid, kanban.ChatMsg{Who: "pilot", Name: who, Text: text})
	msg := "From the autopilot (" + p.Key() + "):\n" + text
	if u.terms[kid] != nil || u.shellStop[kid] != nil || u.asks[kid] != nil {
		u.peerOut[kid] += msg + "\n\n"
		return "Sent to " + key + "; it reads it once its open request is answered.", nil
	}
	u.deliver(kid, msg, true)
	return "Sent to " + key + ".", nil
}

// pilotAnswer is pilot_answer: autopilot id answers card kid's open
// request, or with user leaves a permission one to the user. A change
// outside the card's folders it allows only quoting the user's grant.
func (u *ui) pilotAnswer(id, kid string, allow, user bool, msg, grant string) (string, error) {
	p, c := u.b.Card(id), u.b.Card(kid)
	a, pr := u.asks[kid], u.procs[kid]
	if a == nil || pr == nil || strings.HasPrefix(c.Note, youNote) {
		return "", fmt.Errorf("%s has no open request for you", c.Key())
	}
	if len(a.r.Questions) == 0 && (user || allow && external(a.r.Line) && !granted(p.Pilot.Grants, grant)) {
		before := c.Status
		u.pilotToUser(kid)
		u.save()
		u.notifyChange(c, before)
		if user {
			return "Left to the user: " + c.Key() + " waits for them.", nil
		}
		return "Not allowed: that changes something outside " + c.Key() + "'s folders and grant doesn't quote the user's grants. " +
			"It went to the user.", nil
	}
	delete(u.asks, kid)
	who := "Autopilot " + p.Key()
	if len(a.r.Questions) > 0 {
		pr.Respond(a.r.ID, false, nil, "The autopilot answers: "+msg)
		u.sayMsg(kid, kanban.ChatMsg{Who: "pilot", Name: who, Text: msg})
	} else {
		verdict := map[bool]string{true: "allowed", false: "denied"}[allow]
		pr.Respond(a.r.ID, allow, a.r.Input, msg)
		line := verdict + " by the autopilot: " + a.r.Line
		if msg != "" {
			line += " (" + msg + ")"
		}
		u.say(kid, "tool", line)
	}
	c.Status, c.Note = kanban.StatusWorking, ""
	u.save()
	return "Answered " + c.Key() + ".", nil
}

// pilotAdd is pilot_add: a card for work autopilot id found, in To do
// beside its others, in its organization and folder.
func (u *ui) pilotAdd(id, title, notes string) (string, error) {
	p := u.b.Card(id)
	title = strings.TrimSpace(title)
	if title == "" {
		return "", errors.New("the card needs a title")
	}
	from := *p // a copy: adding moves the board's slots
	c := u.b.Add(max(0, u.b.Column("To do")), kanban.Card{Title: title, Notes: strings.TrimSpace(notes + "\n\n(Added by the autopilot " + from.Key() + ".)"),
		Org: from.Org, Dir: from.Dir, Lang: from.Lang, ChatLang: from.ChatLang, Parent: id})
	u.save()
	u.showToast("Autopilot "+from.Key()+" added "+c.Key()+": "+title, false)
	return "Added " + c.Key() + "; start it with pilot_tell.", nil
}
