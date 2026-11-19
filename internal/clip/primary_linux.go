package clip

// readPrimary reads the primary selection's text: the X server's under one
// without Wayland, else the compositor's.
func readPrimary() ([]byte, error) {
	var b []byte
	var err error
	if d := x11(); d != "" {
		b, _, err = readX11(d, "PRIMARY", pickText)
	} else {
		b, _, err = readWayland(true, pickText)
	}
	return b, err
}

// ownPrimary serves text as the primary selection until done closes or
// another takes it.
func ownPrimary(text string, done <-chan struct{}) error {
	if d := x11(); d != "" {
		return ownX11(d, text, done)
	}
	return ownWayland(text, done)
}
