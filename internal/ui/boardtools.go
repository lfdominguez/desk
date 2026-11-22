package ui

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/fzf"
	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
)

// Board tools (infra's boardToolDefs): a card's Claude sees its
// organization's board and adds to it through calls that answer, in place
// of the DESK-CARD and DESK-TICKET lines, which fail unseen.

// boardReadMsgs is how many of a card's last messages card_read gives.
const boardReadMsgs = 6

// boardTool answers card id's board tool call (ws: its workspace Claude's,
// which only reads), off the UI goroutine.
func (u *ui) boardTool(id string, ws bool, name string, raw json.RawMessage) (string, bool) {
	var a struct {
		Query, Column, Key, Title, Notes, Description string
		Ticket                                        *bool
	}
	json.Unmarshal(raw, &a)
	if ws && (name == "card_create" || name == "ticket_create") {
		return "no tool " + name, false
	}
	if name == "ticket_create" {
		return u.ticketCreate(id, a.Title, a.Description)
	}
	text, err := onBoard(u, func() (string, error) {
		c := u.b.Card(id)
		if c == nil {
			return "", errors.New("the card is gone from the board")
		}
		switch name {
		case "cards":
			return u.boardList(c.Org, a.Query, a.Column), nil
		case "card_read":
			return u.boardRead(c.Org, strings.ToUpper(strings.TrimSpace(a.Key)))
		}
		return u.boardCreate(*c, a.Title, a.Notes, a.Column, a.Ticket == nil || *a.Ticket)
	})
	if err != nil {
		return err.Error(), false
	}
	return text, true
}

// boardList is org's cards ("" every organization's, each line naming
// its) matching query in column (either may be ""), one line each.
func (u *ui) boardList(org, query, column string) string {
	var b strings.Builder
	for _, col := range u.b.Columns {
		if column != "" && !strings.EqualFold(col.Title, column) {
			continue
		}
		for _, c := range col.Cards {
			t := ""
			if l := ticketOf(&c); l != nil {
				t = " " + l.Ref
			}
			if org != "" && c.Org != org || query != "" && !matches(query, c.Title+" "+c.Notes+t) {
				continue
			}
			who := ""
			if org == "" {
				who = c.Org + " "
			}
			fmt.Fprintf(&b, "%s %s[%s%s]%s %s\n", c.Key(), who, col.Title, statusOf(c), t, c.Title)
		}
	}
	if b.Len() == 0 {
		return "No cards match."
	}
	return b.String()
}

// matches says whether every word of query fuzzy-matches hay.
func matches(query, hay string) bool {
	_, ok := fzf.Match(query, hay)
	return ok
}

// statusOf is c's status for a list line, "" when idle.
func statusOf(c kanban.Card) string {
	if c.Status == kanban.StatusIdle {
		return ""
	}
	return ", " + c.Status
}

// boardRead is card key of org, in full; another organization's is as
// missing as one that isn't there.
func (u *ui) boardRead(org, key string) (string, error) {
	c := u.cardOfKey(key)
	if c == nil || c.Org != org {
		return "", fmt.Errorf("no card %s in %s", key, org)
	}
	col, _ := u.b.Find(c.ID)
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\nColumn: %s\n", c.Key(), c.Title, u.b.Columns[col].Title)
	if c.Status != kanban.StatusIdle {
		fmt.Fprintf(&b, "Status: %s %s\n", c.Status, c.Note)
	}
	for _, l := range c.Links {
		fmt.Fprintf(&b, "Link: %s %s\n", l.URL, l.State)
	}
	if c.Notes != "" {
		fmt.Fprintf(&b, "\nNotes:\n%s\n", c.Notes)
	}
	var said []kanban.ChatMsg
	for _, m := range u.chat(c.ID) {
		if m.Who == "you" || m.Who == "claude" {
			said = append(said, m)
		}
	}
	if len(said) > boardReadMsgs {
		said = said[len(said)-boardReadMsgs:]
	}
	if len(said) > 0 {
		b.WriteString("\nLast messages:\n")
	}
	for _, m := range said {
		fmt.Fprintf(&b, "[%s] %s\n", m.Who, m.Text)
	}
	return b.String(), nil
}

// boardCreate splits a card off from (card_create).
func (u *ui) boardCreate(from kanban.Card, title, notes, column string, ticket bool) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", errors.New("a card needs a title")
	}
	col := u.b.Column(cmp.Or(column, "To do"))
	if col < 0 {
		return "", fmt.Errorf("no column %q", column)
	}
	n := u.splitOff(from, title, cmp.Or(notes, title), col, ticket)
	u.save()
	how := "It waits there until the user starts it."
	if ticket {
		how = "Its ticket is created when the user starts it."
	}
	return fmt.Sprintf("Made %s in %s: %s. %s", n.Key(), u.b.Columns[col].Title, n.Title, how), nil
}

// ticketCreate is ticket_create: card id's ticket made now, linked, and
// said in the answer (makeTicket's work, answered instead of sent).
func (u *ui) ticketCreate(id, title, body string) (string, bool) {
	if strings.TrimSpace(title) == "" {
		return "a ticket needs a title", false
	}
	cfg, err := onBoard(u, func() (orgs.Config, error) {
		c := u.b.Card(id)
		if c == nil {
			return orgs.Config{}, errors.New("the card is gone from the board")
		}
		if t := ticketOf(c); t != nil {
			return orgs.Config{}, fmt.Errorf("no ticket created: this card already tracks %s (%s), and a card has one ticket. Ask the user whether that work is a new card (card_create)", t.Ref, t.URL)
		}
		return u.orgConfig(c.Org), nil
	})
	if err != nil {
		return err.Error(), false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	l, err := createTicket(ctx, cfg, title, body)
	if err != nil {
		return "ticket not created: " + err.Error(), false
	}
	if _, err := onBoard(u, func() (bool, error) {
		c := u.b.Card(id)
		if c == nil {
			return false, errors.New("the card left the board; the ticket is " + l.URL)
		}
		c.Ticket = false
		c.Links = append(c.Links, l)
		go u.checkLinks([]string{id})
		u.showToast(c.Key()+": created "+l.Ref, false)
		u.save()
		return true, nil
	}); err != nil {
		return err.Error(), false
	}
	return "Created " + l.Ref + " and linked it to the card: " + l.URL, true
}
