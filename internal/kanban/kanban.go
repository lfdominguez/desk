// Package kanban is the board's data: columns of cards, the archive, and
// the JSON file that keeps them. It knows nothing about the window.
package kanban

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lfdominguez/desk/internal/fzf"
)

// Priorities, in Card.Priority.
const (
	PrioNone = iota
	PrioLow
	PrioMedium
	PrioHigh
)

// Statuses of a card's Claude session, in Card.Status.
const (
	StatusIdle    = ""        // no session yet, or nothing going on
	StatusWorking = "working" // Claude is on a turn
	StatusWaiting = "waiting" // Claude answered: your turn
	StatusBlocked = "blocked" // Claude can't go on (Card.Note says why)
	StatusFailed  = "failed"  // the session errored (Card.Note)
	StatusReview  = "review"  // Claude says the work is finished: the user reviews it
	StatusDone    = "done"    // the user moved it to Done: only a person ever does
)

const DateLayout = "2006-01-02"

// Prefix is what card numbers read with: DESK-12.
const Prefix = "DESK"

type Card struct {
	ID        string    `json:"id"`
	Num       int       `json:"num,omitempty"`
	Title     string    `json:"title"`
	Notes     string    `json:"notes,omitempty"`
	Labels    []string  `json:"labels,omitempty"`
	Priority  int       `json:"priority,omitempty"`
	Due       string    `json:"due,omitempty"`       // DateLayout, "" none
	Org       string    `json:"org,omitempty"`       // a folder of ~/Work, "" none
	Dir       string    `json:"dir,omitempty"`       // where Claude runs, inside the org's folder ("" the folder itself)
	Agent     string    `json:"agent,omitempty"`     // the Claude Code agent its session runs as, "" none
	Lang      string    `json:"lang,omitempty"`      // what the card and its ticket are written in, "" the settings'
	ChatLang  string    `json:"chat_lang,omitempty"` // what its Claude speaks with the user in, "" the settings'
	Status    string    `json:"status,omitempty"`
	Note      string    `json:"note,omitempty"` // the status's why
	Last      string    `json:"last,omitempty"` // the latest message, either side
	Links     []Link    `json:"links,omitempty"`
	Session   Session   `json:"session,omitzero"`
	Artifacts []string  `json:"artifacts,omitempty"`  // claude.ai artifacts the session published
	Pushed    string    `json:"pushed,omitempty"`     // the column its tickets were last moved to match
	Ticket    bool      `json:"ticket,omitempty"`     // its Claude creates its ticket before any work`
	Remote    bool      `json:"remote,omitempty"`     // its terminal's Claude runs with Remote Control
	Alert     string    `json:"alert,omitempty"`      // the monitoring alert it investigates: "org|host|service"
	Projects  []string  `json:"projects,omitempty"`   // its workspace's folders beyond its own (absolute)
	Tabs      []string  `json:"tabs,omitempty"`       // the files open in its workspace, in order (absolute)
	WSSession string    `json:"ws_session,omitempty"` // its workspace Claude's session (claude --resume)
	Draft     string    `json:"draft,omitempty"`      // a message to its Claude written but not sent
	Detached  bool      `json:"detached,omitempty"`   // its session has a window of its own (ui's detach.go)
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated,omitzero"` // last change by anyone: the card's age
	Archived  time.Time `json:"archived,omitzero"`
	From      string    `json:"from,omitempty"`    // archived: the column it left
	Finished  time.Time `json:"finished,omitzero"` // when it went to Done
}

// FinishedAt is when card c was finished: its move to Done, else (a card
// from before desk kept that) its archiving, else its last change.
func (c Card) FinishedAt() time.Time {
	for _, t := range []time.Time{c.Finished, c.Archived, c.Updated} {
		if !t.IsZero() {
			return t
		}
	}
	return c.Created
}

// Session is the card's Claude conversation.
type Session struct {
	ID      string  `json:"id,omitempty"` // claude --resume
	Turns   int     `json:"turns,omitempty"`
	Tokens  int     `json:"tokens,omitempty"`  // all turns' input, cache and output
	Cost    float64 `json:"cost,omitempty"`    // USD
	Context int     `json:"context,omitempty"` // tokens in the context window now
	Window  int     `json:"window,omitempty"`  // the context window's size
}

// Link is a pull request, merge request or ticket the card tracks.
type Link struct {
	URL     string    `json:"url"`
	Kind    string    `json:"kind,omitempty"`  // links.Kind*
	Ref     string    `json:"ref,omitempty"`   // "PR #609", "ABC-1317"
	State   string    `json:"state,omitempty"` // provider words: "approved", "In Review"
	Tone    string    `json:"tone,omitempty"`  // good, wait, bad, done: how the chip colors
	Checked time.Time `json:"checked,omitzero"`
}

func (c Card) Key() string { return fmt.Sprintf("%s-%d", Prefix, c.Num) }

type Column struct {
	Title string `json:"title"`
	Cards []Card `json:"cards"`
}

type Board struct {
	Columns []Column `json:"columns"`
	Archive []Card   `json:"archive,omitempty"` // newest first
	Next    int      `json:"next"`              // the last card number given
}

// Default is the board a first run starts with.
func Default() *Board {
	b := &Board{}
	for _, t := range []string{"Backlog", "To do", "In progress", "Blocked", "Review", "Done"} {
		b.Columns = append(b.Columns, Column{Title: t})
	}
	return b
}

// migrate brings older boards up to date: Blocked and Review columns before
// Done, numbers for cards that have none.
func (b *Board) migrate() {
	for _, t := range []string{"Blocked", "Review"} {
		if b.Column(t) < 0 {
			at := len(b.Columns)
			if d := b.Column("Done"); d >= 0 {
				at = d
			}
			b.Columns = slices.Insert(b.Columns, at, Column{Title: t})
		}
	}
	for c := range b.Columns {
		for i := range b.Columns[c].Cards {
			if p := &b.Columns[c].Cards[i]; p.Num == 0 {
				b.Next++
				p.Num = b.Next
			}
		}
	}
}

// Column is the index of the column titled t (any case), or -1.
func (b *Board) Column(t string) int {
	return slices.IndexFunc(b.Columns, func(c Column) bool { return strings.EqualFold(c.Title, t) })
}

// lastID is the last newID's nanoseconds.
var lastID atomic.Int64

// newID is the time in nanoseconds, base 36, kept increasing: Windows'
// clock can tick slower than two cards are added, which made twins.
func newID() string {
	for {
		last, n := lastID.Load(), time.Now().UnixNano()
		if n <= last {
			n = last + 1
		}
		if lastID.CompareAndSwap(last, n) {
			return strconv.FormatInt(n, 36)
		}
	}
}

// Find returns the column and index of card id, or -1, -1.
func (b *Board) Find(id string) (col, i int) {
	for c := range b.Columns {
		for i := range b.Columns[c].Cards { // by index: a Card is a few hundred bytes to copy
			if b.Columns[c].Cards[i].ID == id {
				return c, i
			}
		}
	}
	return -1, -1
}

// Card is card id to edit in place, on the board or in the archive, or nil.
func (b *Board) Card(id string) *Card {
	if c, i := b.Find(id); c >= 0 {
		return &b.Columns[c].Cards[i]
	}
	if i := slices.IndexFunc(b.Archive, func(c Card) bool { return c.ID == id }); i >= 0 {
		return &b.Archive[i]
	}
	return nil
}

// Add puts c at the end of column col, with a fresh ID, number and times.
func (b *Board) Add(col int, c Card) Card {
	b.Next++
	c.ID, c.Num, c.Created, c.Updated = newID(), b.Next, time.Now(), time.Now()
	b.Columns[col].Cards = append(b.Columns[col].Cards, c)
	return c
}

// MoveBefore takes card id to column col, just before card before ("" or a
// card not in col: the end).
func (b *Board) MoveBefore(id string, col int, before string) {
	c, i := b.Find(id)
	if c < 0 || col < 0 || col >= len(b.Columns) || id == before {
		return
	}
	card := b.Columns[c].Cards[i]
	done := b.Column("Done")
	if col == done && card.Status == StatusReview { // desk never moves a card to Done: a person approved it
		card.Status, card.Note = StatusDone, ""
	}
	switch {
	case col == done && c != done:
		card.Finished = time.Now()
	case col != done:
		card.Finished = time.Time{}
	}
	b.Columns[c].Cards = slices.Delete(b.Columns[c].Cards, i, i+1)
	dst := &b.Columns[col].Cards
	at := slices.IndexFunc(*dst, func(x Card) bool { return x.ID == before })
	if at < 0 {
		at = len(*dst)
	}
	*dst = slices.Insert(*dst, at, card)
}

// Spot is where a card sits and what it was: column Col at index At, or
// (Col -1) index At of the archive. Put takes a card back there, which is
// how a change to many cards undoes in one step.
type Spot struct {
	Card    Card
	Col, At int
}

// Spot is where card id sits now.
func (b *Board) Spot(id string) (Spot, bool) {
	if c, i := b.Find(id); c >= 0 {
		return Spot{b.Columns[c].Cards[i], c, i}, true
	}
	if i := slices.IndexFunc(b.Archive, func(c Card) bool { return c.ID == id }); i >= 0 {
		return Spot{b.Archive[i], -1, i}, true
	}
	return Spot{}, false
}

// Put puts s's card back where s was, as it was, wherever it is now. Spots
// taken together go back in the order they were taken, lowest index
// first, so each lands at its old index.
func (b *Board) Put(s Spot) {
	b.Delete(s.Card.ID)
	dst := &b.Archive
	if s.Col >= 0 {
		dst = &b.Columns[s.Col].Cards
	}
	*dst = slices.Insert(*dst, min(s.At, len(*dst)), s.Card)
}

func (b *Board) Delete(id string) {
	if c, i := b.Find(id); c >= 0 {
		b.Columns[c].Cards = slices.Delete(b.Columns[c].Cards, i, i+1)
	}
	b.Archive = slices.DeleteFunc(b.Archive, func(c Card) bool { return c.ID == id })
}

// ArchiveCard takes card id off the board, remembering its column.
func (b *Board) ArchiveCard(id string) {
	c, i := b.Find(id)
	if c < 0 {
		return
	}
	card := b.Columns[c].Cards[i]
	card.Archived, card.From = time.Now(), b.Columns[c].Title
	b.Columns[c].Cards = slices.Delete(b.Columns[c].Cards, i, i+1)
	b.Archive = slices.Insert(b.Archive, 0, card)
}

// ArchiveDone archives every card in the Done column; it returns how many.
func (b *Board) ArchiveDone() int {
	d := b.Column("Done")
	if d < 0 {
		return 0
	}
	n := len(b.Columns[d].Cards)
	for _, c := range slices.Clone(b.Columns[d].Cards) {
		b.ArchiveCard(c.ID)
	}
	return n
}

// Restore puts archived card id back at the end of its old column (or the
// first one).
func (b *Board) Restore(id string) {
	i := slices.IndexFunc(b.Archive, func(c Card) bool { return c.ID == id })
	if i < 0 {
		return
	}
	card := b.Archive[i]
	b.Archive = slices.Delete(b.Archive, i, i+1)
	col := max(b.Column(card.From), 0)
	card.Archived, card.From = time.Time{}, ""
	b.Columns[col].Cards = append(b.Columns[col].Cards, card)
}

// ParseLabels splits "infra, Client X,infra" into distinct trimmed labels.
func ParseLabels(s string) []string {
	var out []string
	for _, l := range strings.Split(s, ",") {
		if l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "#")); l != "" && !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out
}

// DueIn is how many days from now's date until the card is due (negative:
// overdue); ok is false without a due date.
func (c Card) DueIn(now time.Time) (days int, ok bool) {
	d, err := time.ParseInLocation(DateLayout, c.Due, now.Location())
	if err != nil {
		return 0, false
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return int(math.Round(d.Sub(today).Hours() / 24)), true // rounding absorbs DST days of 23 or 25 hours
}

// Matches reports whether the card matches search q (fzf's syntax) in its
// key, title, notes, org, links or labels ("#label" too).
func (c Card) Matches(q string) bool {
	if strings.TrimSpace(q) == "" {
		return true
	}
	hay := c.Key() + "\n" + c.Title + "\n" + c.Notes + "\n" + c.Org + "\n" + c.Last
	for _, l := range c.Labels {
		hay += "\n#" + l
	}
	for _, l := range c.Links {
		hay += "\n" + l.Ref + "\n" + l.URL
	}
	_, ok := fzf.Match(q, hay)
	return ok
}
