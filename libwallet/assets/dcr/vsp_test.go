package dcr

import "testing"

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
