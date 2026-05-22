package dcr

import (
	"fmt"
	"math"
	"math/big"
	"time"

	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/monetarium-node/cointype"
	"github.com/monetarium/monetarium-node/dcrutil"
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
