// Package tgfmt shapes text for Telegram's rich messages, which take
// Markdown as is (Bot API 10.1): Split keeps a long reply under a
// message's limit without cutting a code block, Redact masks what looks
// like a secret before it leaves the machine, Escape and Fence quote text
// so Markdown doesn't read it, Clip shortens a topic name or a button.
// No network, no GUI.
package tgfmt

import (
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Max is a message's limit: Telegram counts UTF-16 code units.
const Max = 4096

// Len is s's length as Telegram counts it.
func Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// Split cuts md into parts of at most max (Len), at blank lines when it
// can, then at line ends; a code block cut in two is closed at the end of
// one part and opened again, with its language, at the start of the next.
func Split(md string, max int) []string {
	if Len(md) <= max {
		return []string{md}
	}
	var parts []string
	var cur strings.Builder
	fence := "" // the open block's fence line, "" outside one
	flush := func() {
		s := strings.TrimRight(cur.String(), "\n")
		if fence != "" {
			s += "\n" + fenceOf(fence)
		}
		if strings.TrimSpace(s) != "" {
			parts = append(parts, s)
		}
		cur.Reset()
		if fence != "" {
			cur.WriteString(fence + "\n")
		}
	}
	room := func(extra int) bool {
		close := 0
		if fence != "" {
			close = Len(fenceOf(fence)) + 1
		}
		return Len(cur.String())+extra+close <= max
	}
	lines := strings.SplitAfter(md, "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if !room(Len(l)) {
			flush()
		}
		for !room(Len(l)) { // a line longer than a part: cut it
			n := fit(l, max-Len(cur.String())-closeLen(fence)-1)
			cur.WriteString(l[:n] + "\n")
			l = l[n:]
			flush()
		}
		cur.WriteString(l)
		switch {
		case fence == "" && strings.HasPrefix(t, "```"):
			fence = t
		case fence != "" && t == fenceOf(fence):
			fence = ""
		case fence == "" && t == "" && i < len(lines)-1 && Len(cur.String()) > max*3/4:
			flush() // a paragraph ends near the limit: start the next part with the next one
		}
	}
	fence = "" // an unclosed block stays as it came
	flush()
	return parts
}

// fenceOf is the fence that closes the block opened by line.
func fenceOf(open string) string {
	return open[:len(open)-len(strings.TrimLeft(open, "`"))]
}

func closeLen(fence string) int {
	if fence == "" {
		return 0
	}
	return Len(fenceOf(fence)) + 1
}

// fit is how many bytes of s fit in n (Len), at least one rune.
func fit(s string, n int) int {
	used, i := 0, 0
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if used+utf16.RuneLen(r) > n && i > 0 {
			break
		}
		used += utf16.RuneLen(r)
		i += size
	}
	return i
}

// secrets are shapes of tokens and keys: providers' prefixed tokens, AWS
// key ids, JWTs, Telegram bot tokens, private key blocks, and a value
// after password=, token: and the like.
var secrets = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----|$)`),
	regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`\bsk-(ant-)?[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]+`),
	regexp.MustCompile(`\b\d{8,10}:[A-Za-z0-9_-]{35}\b`),
	regexp.MustCompile(`(?i)((?:password|passwd|secret|token|api[_-]?key)["']?\s*[:=]\s*["']?)[^\s"']{6,}`),
}

// Masked replaces what Redact hides.
const Masked = "‹redacted›"

// Redact masks what looks like a secret in s.
func Redact(s string) string {
	for i, re := range secrets {
		if i == len(secrets)-1 {
			s = re.ReplaceAllString(s, "${1}"+Masked) // keep the key's name
			continue
		}
		s = re.ReplaceAllString(s, Masked)
	}
	return s
}

// Escape makes s literal in Markdown: every ASCII punctuation mark gets a
// backslash, which CommonMark allows before any of them.
func Escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 128 && strings.ContainsRune("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Fence is body as a code block in lang, its fence longer than any run of
// backticks inside.
func Fence(lang, body string) string {
	run, most := 0, 0
	for _, r := range body {
		if r == '`' {
			run++
			most = max(most, run)
		} else {
			run = 0
		}
	}
	f := strings.Repeat("`", max(3, most+1))
	return f + lang + "\n" + strings.TrimSuffix(body, "\n") + "\n" + f
}

// Clip is s on one line, at most n runes, with … where it was cut.
func Clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:n-1]), " ") + "…"
}
