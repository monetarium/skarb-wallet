package cryptomaterial

import "testing"

func TestLatinShortcut(t *testing.T) {
	cases := map[string]string{
		"Ф": "A",
		"С": "C",
		"М": "V",
		"Ч": "X",
		"Я": "Z",
	}
	for got, want := range cases {
		latin, ok := latinShortcut(got)
		if !ok || latin != want {
			t.Fatalf("%s: got %q ok=%v, want %q", got, latin, ok, want)
		}
	}
	if _, ok := latinShortcut("V"); ok {
		t.Fatal("latin V must stay with widget.Editor")
	}
}
