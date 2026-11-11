// Package testwait is how long a test waits for something that should
// happen (a prompt, a reply, a toast): imported by tests only. The wait ends
// as soon as the thing happens, so the limit costs a passing test nothing;
// it only stops a broken one. It is long so a busy machine is no failure.
package testwait

import (
	"os"
	"time"
)

// Long is every such wait's limit: DESK_TEST_WAIT when set (qa mutate
// shortens it: a mutant that breaks a wait is killed either way), else
// two minutes, under go test's own ten.
var Long = wait(os.Getenv("DESK_TEST_WAIT"))

func wait(s string) time.Duration {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return 2 * time.Minute
}
