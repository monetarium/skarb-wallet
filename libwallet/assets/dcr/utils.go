package dcr

import (
	"fmt"
	"math"
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
// Returns -1 on error (negative input, NaN, overflow). For SKA, the int64
// return ceiling caps the per-output amount at ~9.22 SKA (= floor(math.MaxInt64 /
// 1e18)). That is enough for the testnet flows we currently exercise; the
// upper layers should treat -1 as a hard rejection and surface a clear error
// to the user. Lifting that cap requires plumbing *big.Int (cointype.SKAAmount)
// through the entire send pipeline — not done in phase 1.
func AmountAtomForCoinType(f float64, ct cointype.CoinType) int64 {
	if ct.IsSKA() {
		// Reject negatives, NaN, and amounts that would overflow int64.
		if math.IsNaN(f) || f < 0 {
			log.Errorf("AmountAtomForCoinType(SKA): rejecting non-finite/negative %v", f)
			return -1
		}
		const skaAtomsPerCoin = 1e18
		atoms := f * skaAtomsPerCoin
		if atoms > float64(math.MaxInt64) {
			log.Errorf("AmountAtomForCoinType(SKA): %v overflows int64; max single-output amount in phase 1 is %v SKA",
				f, math.MaxInt64/skaAtomsPerCoin)
			return -1
		}
		// math.Round rather than truncate, otherwise float jitter
		// (e.g. 1.0 → 0.9999999…) silently drops one atom.
		return int64(math.Round(atoms))
	}
	return AmountAtom(f)
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
