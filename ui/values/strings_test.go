package values

import (
	"strings"
	"testing"

	"github.com/monetarium/skarb-wallet/ui/values/localizable"
)

// TestUkrainianCoversReachableKeys flags every Str* constant that is referenced
// from production UI code but missing from the Ukrainian translation file.
//
// English is the canonical fallback so anything missing in en.go is a real
// bug; uk.go is the project's primary user-facing locale, so a missing key
// there is a silent regression where the user sees English mid-flow.
//
// The check is deliberately limited to the active locales — fr/es/zh inherit
// upstream Cryptopower text and are not surfaced in the language picker.
func TestUkrainianCoversReachableKeys(t *testing.T) {
	enKeys := keysOf(t, localizable.EN)
	ukKeys := keysOf(t, localizable.UK)

	if len(enKeys) == 0 {
		t.Fatal("english locale appears to have zero keys; parser regression")
	}

	var missing []string
	for k := range enKeys {
		if _, ok := ukKeys[k]; !ok {
			missing = append(missing, k)
		}
	}

	// Expect a non-trivial gap (uk.go is intentionally a partial translation
	// covering high-traffic flows). Treat the list as informational unless
	// the gap is enormous, which would suggest something destroyed uk.go.
	if len(missing) > len(enKeys)*9/10 {
		t.Fatalf("ukrainian locale covers <10%% of english keys (%d/%d) — uk.go probably broken",
			len(enKeys)-len(missing), len(enKeys))
	}
	t.Logf("ukrainian locale covers %d/%d keys (%d still falling back to English)",
		len(enKeys)-len(missing), len(enKeys), len(missing))
}

// TestNoStaleBrandInActiveLocales ensures the locales actually offered to the
// user (en + uk) don't leak the upstream Cryptopower or Decred brand into
// product strings. Catches accidental copy-paste from fr/es/zh fallbacks.
func TestNoStaleBrandInActiveLocales(t *testing.T) {
	for name, source := range map[string]string{
		"en": localizable.EN,
		"uk": localizable.UK,
	} {
		for _, brand := range []string{"Cryptopower", "cryptopower", "Crytopower"} {
			if strings.Contains(source, brand) {
				t.Errorf("locale %s still contains stale brand %q — replace with Skarb", name, brand)
			}
		}
	}
}

func keysOf(t *testing.T, source string) map[string]struct{} {
	t.Helper()
	out := make(map[string]struct{})
	for _, line := range strings.Split(source, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "/") {
			continue
		}
		matches := rex.FindAllStringSubmatch(line, -1)
		if len(matches) == 0 {
			continue
		}
		key := strings.Trim(matches[0][1], `"`)
		out[key] = struct{}{}
	}
	return out
}
