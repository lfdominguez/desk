package testwait

import (
	"testing"
	"time"
)

func TestWait(t *testing.T) {
	for s, want := range map[string]time.Duration{"": 2 * time.Minute, "nope": 2 * time.Minute, "-1s": 2 * time.Minute, "0s": 2 * time.Minute, "15s": 15 * time.Second} {
		if got := wait(s); got != want {
			t.Errorf("%q: %v, want %v", s, got, want)
		}
	}
}
