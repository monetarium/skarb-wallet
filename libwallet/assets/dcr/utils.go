package dcr

import (
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/monetarium/monetarium-node/cointype"
	"github.com/monetarium/monetarium-node/dcrutil"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/libwallet/utils"
)

const (
	// fetchPercentage is used to increase the initial estimate gotten during cfilters stage
	fetchPercentage = 0.38

	// Use 10% of estimated total headers fetch time to estimate rescan time
	rescanPercentage = 0.1

	// Use 80% of estimated total headers fetch time to estimate address discovery time
	discoveryPercentage = 0.8

	TestnetHDPath       = "m / 44' / 1' / "
	LegacyTestnetHDPath = "m / 44’ / 11’ / "
	MainnetHDPath       = "m / 44' / 42' / "
	LegacyMainnetHDPath = "m / 44’ / 20’ / "

	// GenesisTimestampMainnet represents the genesis timestamp for the DCR mainnet.
	GenesisTimestampMainnet = 1454954400
	// GenesisTimestampTestnet represents the genesis timestamp for the DCR testnet.
	GenesisTimestampTestnet = 1533513600
	// TargetTimePerBlockMainnet represents the target time per block in seconds for DCR mainnet.
	TargetTimePerBlockMainnet = 300
	// TargetTimePerBlockTestnet represents the target time per block in seconds for DCR testnet.
	TargetTimePerBlockTestnet = 120
)

// Returns a DCR amount that implements the asset amount interface.
func (asset *Asset) ToAmount(v int64) sharedW.AssetAmount {
	return Amount(dcrutil.Amount(v))
}

func AmountAtom(f float64) int64 {
	amount, err := dcrutil.NewAmount(f)
	if err != nil {
		log.Error(err)
		return -1
	}
	return int64(amount)
}

// AmountAtomForCoinType converts a user-typed float amount to integer atoms
// in the base appropriate for the given coin type:
//
//   - VAR: 1 coin = 1e8 atoms (delegates to dcrutil.NewAmount).
//   - SKA: 1 coin = 1e18 atoms.
//
// Returns -1 on error (negative input, NaN). For SKA amounts > int64 atoms
// the function clamps to MaxInt64 and the caller MUST also fetch the
// lossless big.Int via AmountAtomForCoinTypeBig — UnitAmount on
// TransactionDestination is int64-shaped for backward compat and the
// big.Int companion field is what the authoring path actually consumes
// when it's present.
func AmountAtomForCoinType(f float64, ct cointype.CoinType) int64 {
	if ct.IsSKA() {
		if math.IsNaN(f) || f < 0 {
			log.Errorf("AmountAtomForCoinType(SKA): rejecting non-finite/negative %v", f)
			return -1
		}
		const skaAtomsPerCoin = 1e18
		atoms := f * skaAtomsPerCoin
		if atoms > float64(math.MaxInt64) {
			// Clamp + return MaxInt64 so the legacy int64 channel doesn't
			// poison balance-after-send arithmetic with -1. Callers that
			// care about exact atoms must take the big.Int variant.
			return math.MaxInt64
		}
		return int64(math.Round(atoms))
	}
	return AmountAtom(f)
}

// ParseAmountToAtomsBig converts the user-typed amount STRING (not float)
// directly to *big.Int atoms. This is the lossless replacement for the
// AmountAtomForCoinTypeBig path that used to go through float64: float
// has only ~15-17 significant decimal digits, so "1.234567890123456789"
// SKA lost its last ~3 digits before atomization and the broadcast tx
// differed from what the user typed (bug #3 in v1 bug report).
//
// Accepts decimals with either '.' or ',' separator. Rejects: empty
// strings, negative numbers, non-digit characters, more digits after the
// separator than the coin's atom resolution allows (8 for VAR, 18 for
// SKA). Returns nil with a non-nil error in all rejection cases.
//
// For VAR returns big.NewInt(atoms) where atoms = whole*1e8 + frac8.
// For SKA returns big.Int(whole*1e18 + frac18). The result is
// authoritative — it is what the authoring path should encode into the
// tx output's SKAValue / Value field.
func ParseAmountToAtomsBig(s string, ct cointype.CoinType) (*big.Int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("amount is empty")
	}
	// Allow comma as decimal separator (Ukrainian locale convention).
	s = strings.Replace(s, ",", ".", 1)
	if strings.HasPrefix(s, "-") {
		return nil, fmt.Errorf("amount cannot be negative")
	}

	decimals := 8
	if ct.IsSKA() {
		decimals = 18
	}

	wholePart, fracPart := s, ""
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		wholePart, fracPart = s[:dot], s[dot+1:]
	}
	if wholePart == "" {
		wholePart = "0"
	}
	// Strip leading zeros so SetString doesn't reject "0123" on
	// hypothetical strict-parse backends (math/big is permissive but be
	// explicit). Reject obvious garbage early.
	if !isAllDigits(wholePart) {
		return nil, fmt.Errorf("amount contains non-digit characters: %q", s)
	}
	if fracPart != "" && !isAllDigits(fracPart) {
		return nil, fmt.Errorf("amount contains non-digit characters: %q", s)
	}
	if len(fracPart) > decimals {
		// Truncating silently would change the broadcast amount; refuse
		// and let the UI surface the limit instead.
		return nil, fmt.Errorf("amount has %d fractional digits, max %d for this coin",
			len(fracPart), decimals)
	}
	// Pad fractional part to the coin's atom resolution, then concatenate
	// into a single integer atom string.
	if pad := decimals - len(fracPart); pad > 0 {
		fracPart += strings.Repeat("0", pad)
	}
	combined := wholePart + fracPart
	// Trim leading zeros so the resulting big.Int has no surprises;
	// math/big.SetString(.., 10) handles it either way but keep
	// representation tidy.
	combined = strings.TrimLeft(combined, "0")
	if combined == "" {
		combined = "0"
	}
	atoms, ok := new(big.Int).SetString(combined, 10)
	if !ok {
		return nil, fmt.Errorf("amount parse failed: %q", s)
	}
	return atoms, nil
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// AmountAtomForCoinTypeBig is the lossless variant: converts a user-typed
// float to *big.Int atoms without int64 truncation. Returns nil on
// rejection (negative, NaN). For VAR the result is a fast big.Int wrap of
// the int64 path; for SKA it scales by 1e18 via big.Int arithmetic
// against the raw fractional representation so amounts up to the per-coin
// supply cap survive without precision loss at the integer step.
//
// Float input still has 53-bit mantissa so amounts beyond ~9 quadrillion
// SKA atoms (~9 PB SKA, well beyond any plausible balance) lose precision
// before this function runs — that's a UI-layer problem (text input) not
// a math problem here. For phase 1, plumbing the editor text directly
// through a decimal-string parser would be the next step.
//
// Deprecated: prefer ParseAmountToAtomsBig — it accepts the raw user
// string and avoids the float64 round-trip entirely. Kept for callers
// that already have a float in hand (USD/exchange-rate conversion).
func AmountAtomForCoinTypeBig(f float64, ct cointype.CoinType) *big.Int {
	if math.IsNaN(f) || f < 0 {
		return nil
	}
	if ct.IsVAR() {
		return big.NewInt(AmountAtom(f))
	}
	// SKA path: split the float into integer and fractional parts and
	// scale each separately to avoid losing precision at the 1e18 multiply.
	const skaAtomsPerCoin = 1e18
	intPart, fracPart := math.Modf(f)
	intAtoms := new(big.Int).Mul(
		new(big.Int).SetInt64(int64(intPart)),
		new(big.Int).SetInt64(skaAtomsPerCoin),
	)
	// fracPart * 1e18 — fits in int64 because fracPart < 1, so
	// fracPart * 1e18 < 1e18 < MaxInt64.
	fracAtoms := big.NewInt(int64(math.Round(fracPart * skaAtomsPerCoin)))
	return new(big.Int).Add(intAtoms, fracAtoms)
}

func calculateTotalTimeRemaining(timeRemainingInSeconds time.Duration) string {
	minutes := timeRemainingInSeconds / 60
	if minutes > 0 {
		return fmt.Sprintf("%d min", minutes)
	}
	return fmt.Sprintf("%d sec", timeRemainingInSeconds)
}

func secondsToDuration(secs float64) time.Duration {
	return time.Duration(secs) * time.Second
}

func roundUp(n float64) int32 {
	return int32(math.Round(n))
}

// GetGenesisTimestamp returns the genesis timestamp for the provided network.
func GetGenesisTimestamp(network utils.NetworkType) int64 {
	switch network {
	case utils.Mainnet:
		return GenesisTimestampMainnet
	case utils.Testnet:
		return GenesisTimestampTestnet
	}
	return 0
}

// GetTargetTimePerBlock returns the target time per block for the provided network.
func GetTargetTimePerBlock(network utils.NetworkType) int64 {
	switch network {
	case utils.Mainnet:
		return TargetTimePerBlockMainnet
	case utils.Testnet:
		return TargetTimePerBlockTestnet
	}
	return 0
}
