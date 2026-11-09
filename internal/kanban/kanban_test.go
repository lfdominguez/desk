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
