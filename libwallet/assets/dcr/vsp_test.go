package dcr

import (
	"testing"

	"github.com/monetarium/skarb-wallet/libwallet/utils"
)

func TestRecoverVSPHostCandidates(t *testing.T) {
	got := recoverVSPHostCandidates(utils.Mainnet, []string{
		"https://vsp.monetarium.online/",
		"https://extra.example",
		"",
	})
	if len(got) < 2 {
		t.Fatalf("candidates = %v, want builtin + extra", got)
	}
	if got[0] != "https://vsp.monetarium.online" {
		t.Fatalf("builtin should be first, got %q", got[0])
	}
	if !containsHost(got, "https://extra.example") {
		t.Fatalf("saved extra missing: %v", got)
	}
	// builtin is not duplicated from the saved list
	n := 0
	for _, h := range got {
		if normalizeVSPHost(h) == "https://vsp.monetarium.online" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("builtin duplicated: %v", got)
	}
}

func TestWithoutHost(t *testing.T) {
	list := []string{
		"https://vsp.monetarium.online",
		"https://vsp.testnet.monetarium.online/",
		"https://other.example",
	}
	got := withoutHost(list, "https://vsp.testnet.monetarium.online")
	if len(got) != 2 {
		t.Fatalf("withoutHost len = %d, want 2: %v", len(got), got)
	}
	if containsHost(got, "https://vsp.testnet.monetarium.online/") {
		t.Fatal("removed host still present")
	}
	if !containsHost(got, "https://vsp.monetarium.online") {
		t.Fatal("kept host missing")
	}
	if containsHost(nil, "https://vsp.monetarium.online") {
		t.Fatal("empty list should not contain a host")
	}
}
