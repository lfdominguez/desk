package clip

import "sync"

// The primary selection is what was last selected with the mouse, in any
// window: what a middle click pastes in X terminals. desk owns it while the
// text selected in it is the newest (SetPrimary), and reads it for a middle
// click (Primary). Where the system has none desk can reach (macOS, Windows,
// a compositor without data-control's primary selection), both fall back to
// the text last selected in desk.

// owner is desk's own primary selection: its text, how to stop serving it,
// the goroutines serving (tests wait for them), and the one turn they take:
// each claims only once the last let go, and only if not stopped by then, so
// an older selection never claims over a newer one.
var owner struct {
	sync.Mutex
	text    string
	stop    chan struct{}
	serving sync.WaitGroup
	turn    sync.Mutex
}

// SetPrimary makes text the primary selection, served off the caller's
// goroutine until another window's selection replaces it.
func SetPrimary(text string) {
	owner.Lock()
	defer owner.Unlock()
	if owner.stop != nil {
		close(owner.stop)
	}
	stop := make(chan struct{})
	owner.text, owner.stop = text, stop
	owner.serving.Go(func() {
		owner.turn.Lock()
		defer owner.turn.Unlock()
		if !stopped(stop) { // else a newer one came while it waited
			ownPrimary(text, stop) //nolint:errcheck // unreachable: desk's own text is the fallback
		}
	})
}

// stopped says done is closed: a newer selection came, so this one never
// takes it. ownWayland and ownX11 check it last before they do; a stop
// after that closes their connection.
func stopped(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// Primary is the primary selection's text: the system's, or desk's own last
// selection when the system's can't be read.
func Primary() string {
	b, err := readPrimary()
	if err != nil {
		owner.Lock()
		defer owner.Unlock()
		return owner.text
	}
	return string(b)
}
