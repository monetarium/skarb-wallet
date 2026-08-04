package components

import "testing"

// TestNormalizeVSPHost pins the shapes a user actually types. The bare-domain
// case is the one that used to be rejected outright as "not a valid IP or URL
// address".
func TestNormalizeVSPHost(t *testing.T) {
	const want = "https://vsp.testnet.monetarium.online"

	for _, input := range []string{
		"vsp.testnet.monetarium.online",
		"https://vsp.testnet.monetarium.online",
		"https://vsp.testnet.monetarium.online/",
		"  vsp.testnet.monetarium.online  ",
	} {
		if got := normalizeVSPHost(input); got != want {
			t.Errorf("normalizeVSPHost(%q) = %q, want %q", input, got, want)
		}
	}

	// An explicit scheme is the user's decision and survives.
	if got := normalizeVSPHost("http://127.0.0.1:8800"); got != "http://127.0.0.1:8800" {
		t.Errorf("explicit scheme not preserved: %q", got)
	}

	for _, input := range []string{"", "   "} {
		if got := normalizeVSPHost(input); got != "" {
			t.Errorf("normalizeVSPHost(%q) = %q, want empty", input, got)
		}
	}
}
