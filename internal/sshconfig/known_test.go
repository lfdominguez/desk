package sshconfig

import "testing"

// TestKnown: a keyword ssh uses, in any case, is known; one it ignores,
// has deprecated or doesn't support, or never had, isn't.
func TestKnown(t *testing.T) {
	for kw, want := range map[string]bool{"IdentityFile": true, "proxyjump": true, "PubkeyAcceptedKeyTypes": true,
		"Protocol": false, "UseRoaming": false, "GSSAPIAuthentication": false, "Bogus": false} {
		if Known(kw) != want {
			t.Errorf("Known(%q) = %v", kw, !want)
		}
	}
}
