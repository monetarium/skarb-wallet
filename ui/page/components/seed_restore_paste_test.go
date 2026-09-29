package components

import "testing"

func TestPastedSeedWords(t *testing.T) {
	words := pastedSeedWords("  abandon  ability\nabout  ")
	if len(words) != 3 || words[0] != "abandon" || words[1] != "ability" || words[2] != "about" {
		t.Fatalf("got %#v", words)
	}
	words = pastedSeedWords("1. abandon 2) ability\n3.about")
	if len(words) != 3 || words[0] != "abandon" || words[1] != "ability" || words[2] != "about" {
		t.Fatalf("numbered paste got %#v", words)
	}
	if got := pastedSeedWords("abandon"); len(got) != 1 {
		t.Fatalf("single word must stay one field, got %#v", got)
	}
	if got := pastedSeedWords("   "); len(got) != 0 {
		t.Fatalf("blank paste got %#v", got)
	}
}
