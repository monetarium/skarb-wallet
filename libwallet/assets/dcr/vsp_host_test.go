package dcr

import "testing"

func TestNormalizeVSPHost(t *testing.T) {
	pairs := []struct {
		in, want string
	}{
		{"https://vsp.monetarium.online", "https://vsp.monetarium.online"},
		{"https://vsp.monetarium.online/", "https://vsp.monetarium.online"},
		{"https://VSP.Monetarium.online/", "https://vsp.monetarium.online"},
		{" https://vsp.monetarium.online/ ", "https://vsp.monetarium.online"},
	}
	for _, tc := range pairs {
		if got := normalizeVSPHost(tc.in); got != tc.want {
			t.Fatalf("normalizeVSPHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if normalizeVSPHost("https://vsp.monetarium.online") != normalizeVSPHost("https://vsp.monetarium.online/") {
		t.Fatal("trailing slash must not change the cache key")
	}
}
