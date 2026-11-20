package ui

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/claude"
	"github.com/lfdominguez/desk/internal/company"
	"github.com/lfdominguez/desk/internal/infra"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/links"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/vault"
)

// session is a running Claude, as the UI uses it.
type session interface {
	Send(prompt string, images ...string) error
	Respond(id string, allow bool, input json.RawMessage, msg string) error
	Interrupt() error
	Close()
}

// linkCheck reads a link's state from its provider; the demo replaces it.
var linkCheck = links.Check

// startClaude starts a card's session; tests replace it.
var startClaude = func(o claude.Options, on func(claude.Event)) (session, error) {
	p, err := claude.Start(o, on)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (u *ui) chat(id string) []kanban.ChatMsg {
	m, ok := u.chats[id]
	if !ok {
		m = kanban.LoadChat(u.chatFile(id))
		u.chats[id] = m
	}
	return m
}

// chatFile is where conversation id is kept: a card's among the board's
// chats, a drawing's Claude's beside the drawing.
func (u *ui) chatFile(id string) string {
	if p, ok := strings.CutPrefix(id, "draw:"); ok {
		return drawSide(p, "chat.jsonl")
	}
	return kanban.ChatPath(u.path, id)
}

// say adds a message to card id's conversation, in memory and on disk.
func (u *ui) say(id, who, text string) { u.sayMsg(id, kanban.ChatMsg{Who: who, Text: text}) }

// sayMsg adds m to card id's conversation, stamped now.
func (u *ui) sayMsg(id string, m kanban.ChatMsg) {
	m.At = time.Now()
	u.chats[id] = append(u.chat(id), m)
	if err := kanban.AppendChat(u.chatFile(id), m); err != nil {
		u.showToast("Chat not saved: "+err.Error(), true)
	}
}

// moveTo puts card id in the column titled t, if there is one and it isn't
// there already.
func (u *ui) moveTo(id, t string) {
	if col, _ := u.b.Find(id); col >= 0 && col != u.b.Column(t) && u.b.Column(t) >= 0 {
		u.b.MoveBefore(id, u.b.Column(t), "")
	}
}

func (u *ui) orgConfig(org string) orgs.Config {
	c, err := orgs.Load(u.root, org)
	if err != nil {
		u.showToast(err.Error(), true)
	}
	return c
}

// cardLang is the card's languages for its Claude: its own, else the
// settings' (cards made before languages existed).
func cardLang(c *kanban.Card) string {
	card, chat := orgs.Langs()
	return claude.Lang(cmp.Or(c.Lang, card), cmp.Or(c.ChatLang, chat))
}

// firstPrompt is what a new session hears before the user's words: the
// card itself.
func firstPrompt(c *kanban.Card, text string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Card %s: %s\n", c.Key(), c.Title)
	if c.Notes != "" {
		fmt.Fprintf(&b, "\nNotes:\n%s\n", c.Notes)
		if imageRef.MatchString(c.Notes) {
			b.WriteString(imageHint + "\n")
		}
	}
	for i, l := range c.Links {
		if i == 0 {
			b.WriteString("\nLinks:\n")
		}
		fmt.Fprintf(&b, "- %s %s\n", l.Ref, l.URL)
	}
	if c.Ticket && ticketOf(c) == nil {
		b.WriteString("\nBefore any work, this card's ticket has to exist: reply with only its description (what is to be done and why, from the card) and end with DESK-TICKET: <short title>. desk tells you once it is created; start the work then.\n")
	}
	fmt.Fprintf(&b, "\n%s", text)
	return b.String()
}

// startWork is the first message a card sends by itself: on Create, or
// its Start working button.
const startWork = "Start working on this card."

// imageHint goes with a message holding pasted images.
const imageHint = "(The ![image](path) lines are screenshots the user pasted: Read them.)"

// ticketOf is the ticket card c tracks (a Jira issue or a GitHub issue), nil none.
func ticketOf(c *kanban.Card) *kanban.Link {
	for i, l := range c.Links {
		if l.Kind == links.KindJira || l.Kind == links.KindGitHubIssue {
			return &c.Links[i]
		}
	}
	return nil
}

// createTicket opens the ticket card id's Claude asked for (DESK-TICKET),
// title and body its words, links it and tells Claude: the work starts
// then if the card was waiting on it. A card with a ticket gets none: one
// card, one ticket.
var createTicket = links.Create

func (u *ui) makeTicket(id, title, body string) {
	c := u.b.Card(id)
	if c == nil {
		return
	}
	if t := ticketOf(c); t != nil {
		u.send(id, "No ticket created: this card already tracks "+t.Ref+" ("+t.URL+"), and a card has one ticket. Ask the user whether that work is a new card.")
		return
	}
	cfg, first := u.orgConfig(c.Org), c.Ticket
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		l, err := createTicket(ctx, cfg, title, body)
		u.later(func() {
			c := u.b.Card(id)
			if c == nil {
				return
			}
			if err != nil {
				c.Status, c.Note = kanban.StatusBlocked, "Ticket not created: "+firstLine(err.Error())
				u.showToast(c.Key()+": ticket not created: "+firstLine(err.Error()), true)
				u.save()
				return
			}
			c.Ticket = false
			c.Links = append(c.Links, l)
			go u.checkLinks([]string{id})
			u.showToast(c.Key()+": created "+l.Ref, false)
			msg := "Ticket " + l.Ref + " created and linked to the card: " + l.URL
			if first {
				msg += ". Now start working on the card."
			}
			u.send(id, msg)
		})
	}()
}

// newCards makes the cards card id's Claude split off (DESK-CARD), in its
// organization and folder, each to create its own ticket when started.
func (u *ui) newCards(id string, todo []string) {
	if len(todo) == 0 {
		return
	}
	src := u.b.Card(id)
	if src == nil {
		return
	}
	from := *src // a copy: adding cards moves the board's slots
	col := u.b.Column("To do")
	if col < 0 {
		col = 0
	}
	for _, t := range todo {
		title := t
		if r := []rune(title); len(r) > 80 {
			title = string(r[:79]) + "…"
		}
		n := u.b.Add(col, kanban.Card{Title: title, Notes: t + "\n\n(Split off " + from.Key() + ".)", Org: from.Org, Dir: from.Dir, Ticket: true, Lang: from.Lang, ChatLang: from.ChatLang})
		if c := u.b.Card(n.ID); c != nil {
			u.showToast(from.Key()+" made "+c.Key()+": "+title, false)
		}
	}
	u.save()
}

// send hands text to card id's Claude, starting (or resuming) the session;
// text with "!" in front is the user's own command (shell.go).
func (u *ui) send(id, text string) { u.deliver(id, text, false) }

// heard notes what card id's Claude just got: the user's own words from
// the message box (sendBox sets fromBox) or anything else. Only a turn the
// user's words started may close the card (DESK-STATUS: closed), never
// desk's own sends or a turn Claude takes by itself.
func (u *ui) heard(id string) {
	if u.fromBox {
		u.said[id] = true
	} else {
		delete(u.said, id)
	}
}

// compactingNote is the card's note while Claude Code compacts its conversation.
const compactingNote = "Compacting the conversation…"

// compact has card id's Claude summarize its conversation so far (the
// composer's Compact): /compact as a turn, noted at once since Claude Code
// says it started only once it has the session loaded.
func (u *ui) compact(id string) {
	u.send(id, "/compact")
	if c := u.b.Card(id); c != nil && c.Status == kanban.StatusWorking {
		c.Note = compactingNote
		u.save()
	}
}

// cameIn is what desk sends a session when what other Claudes sent it
// while busy (peerOut) can go in.
const cameIn = "(desk) These came in while you were busy."

// overBudget says why c's session is over its organization's budgets
// (company.Over); "" when it isn't, or the organization has none.
func (u *ui) overBudget(c *kanban.Card) string {
	if why := u.pilotOver(c); why != "" {
		return why
	}
	cfg, _ := orgs.Load(u.root, c.Org)
	if cfg.Company.CardBudget <= 0 && cfg.Company.DayBudget <= 0 {
		return ""
	}
	today := company.Spent(orgs.Dir(u.root, c.Org, ""), time.Now())
	return company.Over(cfg.Company, c.Session.Cost, c.Allowed, today)
}

// deliver is send; quiet keeps text out of the chat (desk's own note, as
// a command's end, which the chat shows its own way). A quiet one is
// another Claude's or desk's, held while the session is over budget.
// images are the files of the [Image n] in text (attach.go).
func (u *ui) deliver(id, text string, quiet bool, images ...string) {
	c := u.b.Card(id)
	text = strings.TrimSpace(text)
	if c == nil || text == "" || !c.Archived.IsZero() {
		return
	}
	if u.shellStop[id] != nil { // the box is locked while one runs; anything else waits for it too
		u.showToast(c.Key()+": your command is still running: Stop it, or wait for it", false)
		return
	}
	if cmd := bang(text); cmd != "" && !quiet && len(images) == 0 { // the user's own command, run now: Claude gets it when it ends
		u.shell(id, cmd)
		return
	}
	if u.terms[id] != nil {
		u.showToast(c.Key()+" is open in its terminal: write there, or close it first", false)
		u.showTerm(id)
		return
	}
	if !quiet { // the user's own message: the go-ahead past a budget
		c.Allowed = c.Session.Cost
		if c.Pilot != nil { // and past its group's
			c.Pilot.From = u.pilotSpent(id)
		}
	} else if why := u.overBudget(c); why != "" { // another Claude's: held for the user's go-ahead
		if text != cameIn {
			u.peerOut[id] += text + "\n\n"
		}
		c.Status, c.Note = kanban.StatusWaiting, "Paused: over "+why
		u.save()
		return
	}
	if a, p := u.asks[id], u.procs[id]; a != nil && p != nil && len(images) > 0 { // an answer can't carry images: it comes as the next message
		p.Respond(a.r.ID, false, nil, "The user answers in their next message, with images.")
		delete(u.asks, id)
	} else if a != nil && p != nil { // typed instead of picking: Claude reads it as the reply
		p.Respond(a.r.ID, false, nil, "The user answered in their own words instead: "+text)
		delete(u.asks, id)
		u.say(id, "you", text)
		c.Status, c.Note, c.Last, c.Updated = kanban.StatusWorking, "", text, time.Now()
		u.heard(id)
		u.moveTo(id, "In progress")
		u.save()
		return
	}
	if c.Session.ID == "" { // the card knows its session from the start: the terminal opens the same one
		c.Session.ID = claude.NewSessionID()
	}
	acct := orgs.Account(u.root, c.Org)
	exists := claude.SessionExists(u.home, acct, c.Session.ID)
	prompt := text
	if !exists && !slices.ContainsFunc(u.chat(id), func(m kanban.ChatMsg) bool {
		return !slices.Contains([]string{"shell", "peer", "workspace", "lead", "pilot"}, m.Who)
	}) { // the user's "!" commands, the infra, workspace and lead Claudes' notes aren't the conversation
		prompt = firstPrompt(c, text)
	} else if imageRef.MatchString(text) {
		prompt += "\n" + imageHint
	}
	if ran := u.shellOut[id]; ran != "" { // what the user ran with "!" since the last message
		prompt = "The user ran these commands directly in the card's folder, not through you:\n" + ran + "\n" + prompt
	}
	if in := u.peerOut[id]; in != "" { // the infra and workspace Claudes', kept while the card couldn't take them (each says whose)
		prompt = in + prompt
	}
	p := u.procs[id]
	if p == nil {
		base := c.Session.Cost // the process reports its own total; the card adds earlier ones
		o := claude.Options{Home: u.home, Org: acct, Dir: orgs.Dir(u.root, c.Org, c.Dir), Root: orgs.Dir(u.root, c.Org, ""), System: claude.System(c.Key(), c.Title, c.Org)}
		var err error
		if o.Args, err = u.agentArgs(c); err == nil {
			o.Env, u.via[id], err = u.modelEnv(c.Org, "card")
		}
		if err != nil {
			u.showToast(c.Key()+": "+err.Error(), true)
			return
		}
		o.System += cardLang(c) + u.companyPrompt(c) + u.workflowPrompt(c) + u.aboutMe(c.Org)
		if c.Alert != "" {
			o.System += claude.AlertSnooze
		}
		if c.Pilot != nil {
			o.System += pilotPrompt(c)
		} else if pl := u.pilotOf(c); pl != nil {
			o.System += pilotChildPrompt(pl)
		}
		if u.infraOrg(c.Org) != nil { // server work goes to the org's infra Claude
			o.System += claude.ServerWork(c.Key(), c.Org) // desk peer is allowed in agentArgs
		}
		if u.cardSock != "" {
			o.System += claude.CardTools
		}
		if exists {
			o.Resume = c.Session.ID
		} else {
			o.New = c.Session.ID
		}
		p, err = startClaude(o, func(e claude.Event) {
			u.later(func() {
				if u.procs[id] == p { // one desk let go of (its terminal, archive): its "signal: killed" is no failure
					u.onEvent(id, base, e)
				}
			})
		})
		if err != nil {
			u.showToast("Claude: "+err.Error(), true)
			return
		}
		own(p, infra.Owner{Card: id, Peer: c.Key(), Org: c.Org})
		u.procs[id] = p
	}
	if err := p.Send(prompt, images...); err != nil {
		u.showToast("Claude: "+err.Error(), true)
		return
	}
	u.heard(id)
	delete(u.shellOut, id)
	delete(u.peerOut, id)
	u.shellSent(id)
	if !quiet {
		u.say(id, "you", text)
		c.Last = text
	}
	c.Status, c.Note, c.Updated = kanban.StatusWorking, "", time.Now()
	u.moveTo(id, "In progress") // work going on is never in Review or Done
	u.save()
}

// onEvent applies one of card id's session events; base is the card's
// cost before this process.
func (u *ui) onEvent(id string, base float64, e claude.Event) {
	c := u.b.Card(id)
	if c == nil {
		return
	}
	c.Updated = time.Now()
	before := c.Status
	defer func() { // looked up again: a column move leaves c pointing at another card's slot
		if c := u.b.Card(id); c != nil {
			u.notifyChange(c, before)
		}
	}()
	switch e.Kind {
	case claude.KindInit: // also a turn Claude starts on its own, when a background task ends
		c.Session.ID = e.Session
		if c.Status != kanban.StatusWorking {
			c.Status, c.Note = kanban.StatusWorking, ""
		}
	case claude.KindText:
		if e.Parent != "" { // a subagent's
			u.subSay(id, e.Parent, kanban.ChatMsg{Who: "claude", Text: e.Text})
			break
		}
		clean, d := claude.Take(e.Text)
		p := u.pending[id]
		if d.Status != "" {
			p.Status, p.Note = d.Status, d.Note
		}
		p.Links = append(p.Links, d.Links...)
		if d.Ticket != "" {
			p.Ticket, p.Body = d.Ticket, clean
		}
		p.Cards = append(p.Cards, d.Cards...)
		p.Snooze = cmp.Or(d.Snooze, p.Snooze)
		u.pending[id] = p
		if clean != "" {
			u.say(id, "claude", clean)
			c.Last = clean
		}
	case claude.KindTool:
		if e.ToolID != "" {
			u.toolLines[e.ToolID] = e.Text // ponytail: never pruned; a line per tool use
			if e.Cmd != "" {
				u.toolCmds[e.ToolID] = e.Cmd
			}
			if e.Parent != "" {
				u.toolBy[e.ToolID] = e.Parent
			}
			if e.Path != "" { // an Edit or Write: its file's open tab looks again once it's done
				u.wsPaths[e.ToolID] = e.Path
			}
		}
		if e.Parent != "" {
			u.subSay(id, e.Parent, toolMsg(e))
			if t := u.findTask(id, "", e.Parent); t != nil && e.ToolID != "" {
				t.pending[e.ToolID] = pend{line: e.Text, since: time.Now()}
			}
			break
		}
		if e.To != "" {
			u.resume(id, e.To, e.ToolID, e.Text)
		}
		u.sayMsg(id, toolMsg(e))
		c.Note = e.Text // working: what on
	case claude.KindAsk:
		if p := u.procs[id]; p != nil && len(e.Ask.Questions) == 0 && u.allowed[id][e.Ask.Tool] { // allowed until desk quits
			p.Respond(e.Ask.ID, true, e.Ask.Input, "")
			u.say(id, "tool", "allowed "+e.Ask.Line)
			break
		}
		u.asks[id] = newAsk(e.Ask)
		if pl := u.pilotOf(c); pl != nil { // its autopilot answers, not the user
			u.say(id, "tool", "asks the autopilot: "+cmp.Or(e.Ask.Line, "a question"))
			c.Status = kanban.StatusWorking
			defer u.pilotAsk(id) // telling the autopilot moves cards
			break
		}
		if qs := e.Ask.Questions; len(qs) > 0 {
			u.say(id, "claude", interviewText(qs))
			c.Status, c.Note = kanban.StatusWaiting, "Claude asks: "+qs[0].Question
		} else {
			u.say(id, "tool", "asks to run "+e.Ask.Line)
			c.Status, c.Note = kanban.StatusWaiting, "Wants to run "+e.Ask.Line+": allow or deny"
		}
	case claude.KindTask:
		u.onTask(id, e.Task)
	case claude.KindToolDone:
		if p, ok := u.wsPaths[e.ToolID]; ok {
			delete(u.wsPaths, e.ToolID)
			u.wsTouched(id, p)
		}
		if t := u.findTask(id, "", e.Parent); t != nil && e.Parent != "" {
			delete(t.pending, e.ToolID)
			t.act = time.Now()
		}
		return // nothing on the card changed
	case claude.KindUsage:
		c.Session.Context = e.Context
	case claude.KindCompact: // its progress: the card's note while it runs, a line once it's done
		if e.Text != "done" {
			c.Status, c.Note = kanban.StatusWorking, compactingNote
			break
		}
		line := "compacted the conversation"
		if e.Tokens > 0 && e.Context > 0 {
			line += ": " + tokens(e.Tokens) + " → " + tokens(e.Context) + " tokens"
			c.Session.Context = e.Context
		}
		u.say(id, "tool", line)
		if c.Note == compactingNote {
			c.Note = ""
		}
	case claude.KindArtifact:
		if !slices.Contains(c.Artifacts, e.Text) {
			c.Artifacts = append(c.Artifacts, e.Text)
		}
	case claude.KindResult:
		delete(u.asks, id) // a request the turn left unanswered is void
		if u.peerOut[id] != "" {
			defer u.deliver(id, cameIn, true)
		}
		c.Session.Window = max(c.Session.Window, e.Window)
		c.Session.Turns++
		c.Session.Tokens += e.Tokens
		if spent := base + e.Cost - c.Session.Cost; spent > 0 { // what this turn cost: the organization's day
			if err := company.Spend(orgs.Dir(u.root, c.Org, ""), time.Now(), spent); err != nil {
				u.showToast("Company spend: "+err.Error(), true)
			}
		}
		c.Session.Cost = base + e.Cost
		d := u.pending[id]
		delete(u.pending, id)
		u.addLinks(c, d.Links)
		if d.Snooze != "" && c.Alert != "" {
			u.snoozeAsk[id] = d.Snooze // the panel offers it
		}
		defer u.newCards(id, d.Cards) // after: adding cards moves the board's slots under c
		if d.Ticket != "" && !e.Error {
			defer u.makeTicket(id, d.Ticket, d.Body) // once the card's status below is set: it sends again
		}
		switch {
		case e.Error && e.Text != "":
			c.Status, c.Note = kanban.StatusFailed, firstLine(e.Text)
			u.say(id, "error", e.Text)
		case e.Error:
			c.Status, c.Note = kanban.StatusWaiting, "Stopped"
		case len(e.Denied) > 0:
			c.Status, c.Note = kanban.StatusBlocked, "Needs permission for "+strings.Join(slices.Compact(e.Denied), ", ")+": open in terminal"
			u.moveTo(id, "Blocked")
		case d.Status == kanban.StatusBlocked:
			c.Status, c.Note = kanban.StatusBlocked, d.Note
			u.moveTo(id, "Blocked")
		case u.backgrounded(id) != "": // the turn ended, the work didn't: Claude takes a turn of its own when they finish
			c.Status, c.Note = kanban.StatusWorking, u.backgrounded(id)
			if d.Status == kanban.StatusDone || d.Status == claude.Closed { // never up for review while work runs: held for the turn that ends idle, unless it says otherwise
				u.pending[id] = claude.Directives{Status: d.Status, Note: d.Note}
			}
		case d.Status == claude.Closed && u.said[id]: // the user said it's done, in their own words
			c.Status, c.Note = kanban.StatusDone, ""
			u.moveTo(id, "Done")
		case d.Status == kanban.StatusDone || d.Status == claude.Closed: // Claude's done is a review: only the user moves a card to Done
			c.Status, c.Note = kanban.StatusReview, d.Note
			u.moveTo(id, "Review")
		default:
			c.Status, c.Note = kanban.StatusWaiting, ""
		}
		if why := u.overBudget(c); why != "" && c.Status != kanban.StatusFailed {
			c.Status, c.Note = kanban.StatusWaiting, "Paused: over "+why
			u.say(id, "error", "Paused: this session is over "+why+". What other Claudes send it waits; your next message continues it.")
		}
		if c.Status != kanban.StatusWorking { // the user's word holds through the turns Claude takes while its work runs, no longer
			delete(u.said, id)
		}
		defer u.pilotEnded(id) // once the card is saved as it ends: telling the autopilot moves cards
	case claude.KindExit:
		u.stopTasks(id)
		delete(u.procs, id)
		delete(u.asks, id)
		if c.Status == kanban.StatusWorking {
			c.Status, c.Note = kanban.StatusFailed, "Claude exited"
		}
		if e.Text != "" {
			c.Status, c.Note = kanban.StatusFailed, firstLine(e.Text)
			u.say(id, "error", e.Text)
		}
	}
	if c := u.b.Card(id); c != nil && c.Status == kanban.StatusWorking { // a turn, its own included, or work still running: never in Review or Done, whoever put it there
		u.moveTo(id, "In progress")
	}
	u.save()
}

// toolMsg is a tool use as the conversation keeps it: its line, and a
// shell command's text and description, or a file change's diff or size.
func toolMsg(e claude.Event) kanban.ChatMsg {
	m := kanban.ChatMsg{Who: "tool", Text: e.Text, Cmd: e.Cmd, Detail: e.Detail, Path: e.Path, Lines: e.Lines, Bytes: e.Bytes}
	for _, x := range e.Edits {
		m.Edits = append(m.Edits, kanban.Edit(x))
	}
	return m
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if r := []rune(s); len(r) > 120 {
		s = string(r[:119]) + "…"
	}
	return s
}

// addLinks attaches what Claude reported (or the user typed) to c, once
// each, and checks the new ones.
func (u *ui) addLinks(c *kanban.Card, raw []string) {
	if len(raw) == 0 {
		return
	}
	cfg := u.orgConfig(c.Org)
	added := false
	for _, s := range raw {
		l, ok := links.Parse(s, cfg)
		if ok && !slices.ContainsFunc(c.Links, func(x kanban.Link) bool { return x.URL == l.URL }) {
			c.Links = append(c.Links, l)
			added = true
		}
	}
	if added {
		go u.checkLinks([]string{c.ID})
	}
}

// stop interrupts card id's turn.
func (u *ui) stop(id string) {
	if p := u.procs[id]; p != nil {
		p.Interrupt()
	}
}

// openURL hands a link to the desktop's browser (xdg-open, macOS's open).
var openURL = func(s string) { exec.Command(infra.Opener, s).Start() } // a var tests replace

// pollLinks checks every board card's links, and the orgs' tickets, now
// and every 3 minutes, and moves back in step a card's ticket that drifted
// from its column.
func (u *ui) pollLinks() {
	for {
		u.later(func() { u.syncTickets(true) })
		done := make(chan []string, 1)
		u.later(func() { done <- u.linkedIDs() })
		u.checkLinks(<-done)
		u.checkTickets()
		u.checkAlerts()
		u.checkCI()
		time.Sleep(3 * time.Minute)
	}
}

// linkedIDs is the board's cards that have links; on the UI goroutine.
func (u *ui) linkedIDs() []string {
	var ids []string
	for _, c := range u.boardCards() {
		if len(c.Links) > 0 {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// checkLinks asks the providers about cards ids' links, off the UI
// goroutine, and stores what they say.
func (u *ui) checkLinks(ids []string) {
	type job struct {
		id, org string
		l       kanban.Link
		cfg     orgs.Config
	}
	jobs := make(chan []job, 1)
	u.later(func() {
		var js []job
		cfgs := map[string]orgs.Config{}
		for _, id := range ids {
			c := u.b.Card(id)
			if c == nil {
				continue
			}
			if _, ok := cfgs[c.Org]; !ok {
				cfgs[c.Org] = u.orgConfig(c.Org)
			}
			for _, l := range c.Links {
				if l.Kind != links.KindURL {
					js = append(js, job{id, c.Org, l, cfgs[c.Org]})
				}
			}
		}
		jobs <- js
	})
	for _, j := range <-jobs { // ponytail: one at a time; fine for tens of links, a worker pool past that
		done := u.busy(j.org)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		state, tone, err := linkCheck(ctx, j.l, j.cfg)
		cancel()
		done()
		var locked *vault.LockedError
		var na *links.NoAccountError
		switch {
		case errors.As(err, &locked):
			state, tone = "locked: unlock "+locked.Vault.Name, links.ToneWait
			u.later(func() { u.vaultLocked(locked) })
		case errors.As(err, &na): // not asked: no account to ask with
			state, tone = "no "+na.Provider+" account", links.ToneWait
		case err != nil:
			state, tone = "⚠ "+firstLine(err.Error()), links.ToneBad
		}
		u.later(func() {
			c := u.b.Card(j.id)
			if c == nil {
				return
			}
			for i := range c.Links {
				if l := &c.Links[i]; l.URL == j.l.URL {
					l.State, l.Tone, l.Checked = state, tone, time.Now()
				}
			}
			u.save()
		})
	}
}
