package tgfmt

import (
	"strings"
	"testing"
)

// fences is how many fence lines s has: even when every block is closed.
func fences(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			n++
		}
	}
	return n
}

// TestSplit: a short text stays whole; a long one is cut at paragraphs,
// each part under the limit; a code block cut in two closes and opens again
// with its language; a line longer than a part is cut; nothing is lost.
func TestSplit(t *testing.T) {
	if got := Split("short", 100); len(got) != 1 || got[0] != "short" {
		t.Fatal(got)
	}
	para := strings.Repeat("word ", 15) // 75
	md := para + "\n\n" + para + "\n\n" + para
	parts := Split(md, 100)
	if len(parts) != 3 {
		t.Fatalf("%d parts %q", len(parts), parts)
	}
	for _, p := range parts {
		if Len(p) > 100 || strings.TrimSpace(p) != strings.TrimSpace(para) {
			t.Fatalf("part %q", p)
		}
	}

	var code strings.Builder
	for i := range 20 {
		code.WriteString("line := " + strings.Repeat("x", i) + "\n")
	}
	md = "Before.\n\n```go\n" + code.String() + "```\n\nAfter."
	parts = Split(md, 120)
	if len(parts) < 3 {
		t.Fatalf("%d parts", len(parts))
	}
	var body strings.Builder
	for i, p := range parts {
		if Len(p) > 120 || fences(p)%2 != 0 {
			t.Fatalf("part %d (%d) unbalanced or long:\n%s", i, Len(p), p)
		}
		if i > 0 && i < len(parts)-1 && !strings.HasPrefix(p, "```go\n") {
			t.Fatalf("part %d doesn't reopen the block:\n%s", i, p)
		}
		for _, l := range strings.Split(p, "\n") {
			if strings.HasPrefix(l, "line := ") {
				body.WriteString(l + "\n")
			}
		}
	}
	if body.String() != code.String() {
		t.Fatalf("lines lost:\n%s", body.String())
	}

	long := strings.Repeat("é", 250)
	parts = Split("intro\n"+long, 100)
	if strings.Join(parts, "") != "intro"+strings.Repeat("é", 250) && Len(strings.Join(parts, "")) < 250 {
		t.Fatalf("%q", parts)
	}
	for _, p := range parts {
		if Len(p) > 100 {
			t.Fatalf("long part %d", Len(p))
		}
	}
	if got := strings.ReplaceAll(strings.Join(parts, ""), "\n", ""); got != "intro"+long {
		t.Fatalf("long line lost runes: %d", len(got))
	}

	inBlock := "```\n" + strings.Repeat("a long line in a block\n", 3)
	for _, p := range Split(inBlock+strings.Repeat("y", 30), 40) {
		if Len(p) > 40 {
			t.Fatalf("cut in a block: %d %q", Len(p), p)
		}
	}
	for _, p := range Split("```\n"+strings.Repeat("z", 100)+"\n```", 40) {
		if Len(p) > 40 || fences(p) != 2 {
			t.Fatalf("a long line in a block: %d %q", Len(p), p)
		}
	}
	open := Split("```sh\n"+strings.Repeat("echo hi\n", 20), 60)
	if last := open[len(open)-1]; strings.HasSuffix(last, "```") {
		t.Fatalf("an unclosed block got closed: %q", last)
	}
	if got := Split("a\n\n\n\n"+strings.Repeat("b", 6), 8); len(got) != 2 || got[0] != "a" || got[1] != "bbbbbb" {
		t.Fatalf("blank parts kept: %q", got)
	}
}

// TestLen: Telegram's count is UTF-16: an emoji outside the BMP is two.
func TestLen(t *testing.T) {
	if Len("a😀é") != 4 {
		t.Fatal(Len("a😀é"))
	}
	if fit("😀😀", 1) != 4 || fit("abc", 2) != 2 {
		t.Fatal(fit("😀😀", 1), fit("abc", 2))
	}
}

// TestRedact: each kind of secret is masked, the words around it kept;
// a password's key keeps its name.
func TestRedact(t *testing.T) {
	for _, s := range []string{
		"ghp_" + strings.Repeat("a", 36),
		"github_pat_" + strings.Repeat("b", 40),
		"glpat-" + strings.Repeat("c", 20),
		"xoxb-" + strings.Repeat("1", 20),
		"sk-ant-" + strings.Repeat("d", 40),
		"AKIA" + strings.Repeat("A", 16),
		"eyJhbGciOiJIUzI1.eyJzdWIiOiIxMjM0.abcDEF",
		"123456789:" + strings.Repeat("e", 35),
		"-----BEGIN OPENSSH PRIVATE KEY-----\nb3Blbn\n-----END OPENSSH PRIVATE KEY-----",
	} {
		if got := Redact("before " + s + " after"); got != "before "+Masked+" after" {
			t.Fatalf("%q → %q", s, got)
		}
	}
	if got := Redact(`password="hunter2hunter2" and TOKEN: abcdef123`); got != `password="`+Masked+`" and TOKEN: `+Masked {
		t.Fatal(got)
	}
	if got := Redact("ABC-123 is fixed, see sk-short and 12:34"); got != "ABC-123 is fixed, see sk-short and 12:34" {
		t.Fatal(got)
	}
}

// TestQuote: Escape makes punctuation literal; Fence outgrows backticks
// inside; Clip keeps one line and marks the cut.
func TestQuote(t *testing.T) {
	if got := Escape("**not bold** _x_ é"); got != `\*\*not bold\*\* \_x\_ é` {
		t.Fatal(got)
	}
	if got := Fence("go", "x := 1\n"); got != "```go\nx := 1\n```" {
		t.Fatal(got)
	}
	if got := Fence("", "a ```` b"); got != "`````\na ```` b\n`````" {
		t.Fatal(got)
	}
	if got := Clip("Fix  the\nlogin redirect", 12); got != "Fix the log…" {
		t.Fatal(got)
	}
	if got := Clip("short", 12); got != "short" {
		t.Fatal(got)
	}
}

// TestRedactAlso: the user's own pattern is masked too, a bad one changes
// nothing, "" takes it away.
func TestRedactAlso(t *testing.T) {
	t.Cleanup(func() { Also("") })
	if err := Also(`ACME-\d+|hunter2`); err != nil || Redact("ticket ACME-42, pw hunter2") != "ticket "+Masked+", pw "+Masked {
		t.Fatal(err, Redact("ticket ACME-42, pw hunter2"))
	}
	if err := Also("(oops"); err == nil || Redact("ACME-1") != Masked {
		t.Fatal(err)
	}
	if Also(""); Redact("ACME-1") != "ACME-1" {
		t.Fatal("still masked")
	}
}

// TestDetails: a folded block, its body trimmed, Markdown kept inside.
func TestDetails(t *testing.T) {
	if d := Details("🔧 2 tools", "\n```\nls\n```\n"); d != "<details><summary>🔧 2 tools</summary>\n\n```\nls\n```\n\n</details>" {
		t.Fatalf("%q", d)
	}
}
