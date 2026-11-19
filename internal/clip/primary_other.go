//go:build !linux

package clip

import "errors"

// readPrimary: no primary selection here; desk's own selection stands in.
func readPrimary() ([]byte, error) { return nil, errors.New("no primary selection") }

// ownPrimary: nothing to serve to.
func ownPrimary(string, <-chan struct{}) error { return nil }
