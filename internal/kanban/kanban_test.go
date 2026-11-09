package kanban

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func ids(cs []Card) (out []string) {
	for _, c := range cs {
		out = append(out, c.Title)
	}
	return out
}

func TestMoveBefore(t *testing.T) {
	b := Default()
	a := b.Add(0, Card{Title: "a"})
	b.Add(0, Card{Title: "c"})
	x := b.Add(2, Card{Title: "x"})
	b.MoveBefore(a.ID, 2, x.ID)
	if got := ids(b.Columns[0].Cards); !slices.Equal(got, []string{"c"}) {
		t.Fatalf("source column: %v", got)
	}
	if got := ids(b.Columns[2].Cards); !slices.Equal(got, []string{"a", "x"}) {
		t.Fatalf("before x: %v", got)
	}
	b.MoveBefore(a.ID, 2, "") // "" appends
	if got := ids(b.Columns[2].Cards); !slices.Equal(got, []string{"x", "a"}) {
		t.Fatalf("append: %v", got)
	}
	b.Delete(a.ID)
	if b.Card(a.ID) != nil {
		t.Fatal("deleted card still found")
	}
}

// TestMoveReviewToDone: a person moving a card in review to Done marks it
// done; another column, or a card not in review, keeps its status.
func TestMoveReviewToDone(t *testing.T) {
	b := Default()
	done := b.Column("Done")
	r := b.Add(0, Card{Title: "r", Status: StatusReview, Note: "Ready for review"})
	w := b.Add(0, Card{Title: "w", Status: StatusWorking})
	b.MoveBefore(r.ID, 1, "")
	if c := b.Card(r.ID); c.Status != StatusReview {
		t.Fatalf("moved to column 1: %q", c.Status)
	}
	b.MoveBefore(r.ID, done, "")
	b.MoveBefore(w.ID, done, "")
	if c := b.Card(r.ID); c.Status != StatusDone || c.Note != "" {
		t.Fatalf("review card in Done: %q %q", c.Status, c.Note)
	}
	if c := b.Card(w.ID); c.Status != StatusWorking {
		t.Fatalf("working card in Done: %q", c.Status)
	}
}

// TestFinished: moving a card into Done stamps when; moving it within Done
// keeps that, and out of Done clears it.
func TestFinished(t *testing.T) {
	b := Default()
	done := b.Column("Done")
	a := b.Add(0, Card{Title: "a"})
	x := b.Add(done, Card{Title: "x"})
	b.MoveBefore(a.ID, done, "")
	at := b.Card(a.ID).Finished
	if at.IsZero() {
		t.Fatal("no finished time in Done")
	}
	b.MoveBefore(a.ID, done, x.ID) // reordered inside Done
	if got := b.Card(a.ID).Finished; !got.Equal(at) {
		t.Fatalf("reorder in Done changed finished: %v → %v", at, got)
	}
	b.MoveBefore(a.ID, 1, "")
	if got := b.Card(a.ID).Finished; !got.IsZero() {
		t.Fatalf("left Done, still finished %v", got)
	}
}

// TestFinishedAt: the finished time, else the archived one, else the last
// change, else the creation.
func TestFinishedAt(t *testing.T) {
	t1, t2, t3, t4 := time.Unix(100, 0), time.Unix(200, 0), time.Unix(300, 0), time.Unix(400, 0)
	for _, tc := range []struct {
		c    Card
		want time.Time
	}{
		{Card{Created: t1, Updated: t2, Archived: t3, Finished: t4}, t4},
		{Card{Created: t1, Updated: t2, Archived: t3}, t3},
		{Card{Created: t1, Updated: t2}, t2},
		{Card{Created: t1}, t1},
	} {
		if got := tc.c.FinishedAt(); !got.Equal(tc.want) {
			t.Errorf("%+v: %v, want %v", tc.c, got, tc.want)
		}
	}
}

// TestSpotPut: spots taken before a change to several cards put each back
// where it was, as it was: on the board or in the archive, at its index.
func TestSpotPut(t *testing.T) {
	b := Default()
	done := b.Column("Done")
	a := b.Add(done, Card{Title: "a"})
	m := b.Add(done, Card{Title: "m"})
	z := b.Add(done, Card{Title: "z"})
	b.ArchiveCard(m.ID)
	old := b.Archive[0]
	var spots []Spot
	for _, id := range []string{a.ID, z.ID, m.ID} {
		s, ok := b.Spot(id)
		if !ok {
			t.Fatalf("no spot for %s", id)
		}
		spots = append(spots, s)
	}
	if spots[2].Col != -1 || spots[2].At != 0 || spots[1].Col != done || spots[1].At != 1 {
		t.Fatalf("spots: %+v", spots)
	}
	b.ArchiveCard(a.ID)
	b.Delete(z.ID)
	b.Restore(m.ID)
	for _, s := range spots {
		b.Put(s)
	}
	if got := ids(b.Columns[done].Cards); !slices.Equal(got, []string{"a", "z"}) {
		t.Fatalf("Done after undo: %v", got)
	}
	if len(b.Archive) != 1 || b.Archive[0].ID != m.ID || !b.Archive[0].Archived.Equal(old.Archived) || b.Archive[0].From != old.From {
		t.Fatalf("archive after undo: %+v", b.Archive)
	}
	if _, ok := b.Spot("nope"); ok {
		t.Fatal("spot for an unknown card")
	}
}

// TestMigrate: a board from before Blocked, Review and numbers gets them.
func TestMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.json")
	old := `{"columns":[{"title":"To do","cards":[{"id":"a","title":"a"},{"id":"b","title":"b"}]},{"title":"Done","cards":[]}]}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if b.Column("blocked") != 1 || b.Column("review") != 2 || b.Column("Done") != 3 {
		t.Fatalf("columns: %+v", b.Columns)
	}
	if c := b.Card("b"); c.Num != 2 || c.Key() != "DESK-2" || b.Add(0, Card{Title: "c"}).Num != 3 {
		t.Fatalf("numbers: %+v next %d", c, b.Next)
	}
}

func TestArchive(t *testing.T) {
	b := Default()
	done := b.Column("Done")
	x := b.Add(done, Card{Title: "x"})
	b.Add(done, Card{Title: "y"})
	if n := b.ArchiveDone(); n != 2 || len(b.Columns[done].Cards) != 0 || b.Archive[0].Title != "y" {
		t.Fatalf("archived %d: %+v", n, b.Archive)
	}
	if c := b.Card(x.ID); c == nil || c.Title != "x" {
		t.Fatalf("archived card: %+v", c)
	}
	b.Restore(x.ID)
	if got := b.Columns[done].Cards; len(got) != 1 || got[0].ID != x.ID || !got[0].Archived.IsZero() {
		t.Fatalf("restore: %+v", got)
	}
}

func TestChat(t *testing.T) {
	path := ChatPath(filepath.Join(t.TempDir(), "board.json"), "a")
	AppendChat(path, ChatMsg{Who: "you", Text: "hi\nthere"})
	AppendChat(path, ChatMsg{Who: "claude", Text: "hello"})
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"who":"claude","te`) // a torn last line
	f.Close()
	if got := LoadChat(path); len(got) != 2 || got[0].Text != "hi\nthere" {
		t.Fatalf("%+v", got)
	}
}

func TestParseLabels(t *testing.T) {
	if got := ParseLabels(" infra, #Client X,,infra "); !slices.Equal(got, []string{"infra", "Client X"}) {
		t.Fatal(got)
	}
}

func TestDueIn(t *testing.T) {
	now := time.Date(2026, 10, 27, 23, 30, 0, 0, time.Local)
	for due, want := range map[string]int{"2026-10-27": 0, "2026-10-28": 1, "2026-10-20": -7, "2026-11-12": 16} {
		if got, ok := (Card{Due: due}).DueIn(now); !ok || got != want {
			t.Errorf("%s: %d %v, want %d", due, got, ok, want)
		}
	}
	if _, ok := (Card{}).DueIn(now); ok {
		t.Error("no due date reported as due")
	}
}

func TestMatches(t *testing.T) {
	c := Card{Title: "Renew TLS", Notes: "lb-01", Labels: []string{"infra"}}
	for q, want := range map[string]bool{"": true, "tls": true, "rnw tls": true, "lb-01": true, "LB-01": false /* smart case */, "#infra": true, "#dev": false, "#dev | tls": true} {
		if c.Matches(q) != want {
			t.Errorf("%q: want %v", q, want)
		}
	}
}

func TestSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "board.json")
	if b, err := Load(path); err != nil || len(b.Columns) != 6 {
		t.Fatalf("missing file should give the default board: %v %v", b, err)
	}
	b := Default()
	c := b.Add(1, Card{Title: "write tests", Notes: "notes\nmore", Labels: []string{"dev"}, Priority: PrioHigh, Due: "2026-11-01"})
	if err := b.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if g := got.Card(c.ID); g == nil || g.Notes != "notes\nmore" || g.Priority != PrioHigh || g.Due != "2026-11-01" || g.Labels[0] != "dev" {
		t.Fatalf("round trip: %+v", got.Columns[1])
	}
}
