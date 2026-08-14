package wallet

import "testing"

func TestSameVSPHost(t *testing.T) {
	pairs := []struct {
		a, b string
		want bool
	}{
		{"https://vsp.monetarium.online", "https://vsp.monetarium.online/", true},
		{"https://VSP.Monetarium.online", "https://vsp.monetarium.online", true},
		{"https://vsp.monetarium.online", "https://vsp.testnet.monetarium.online", false},
		{"https://vsp.monetarium.online/api", "https://vsp.monetarium.online/api/", true},
	}
	for _, tc := range pairs {
		if got := sameVSPHost(tc.a, tc.b); got != tc.want {
			t.Fatalf("sameVSPHost(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
