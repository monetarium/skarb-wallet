package txrules_test

import (
	"testing"

	. "github.com/monetarium/monetarium-wallet/wallet/txrules"
	"github.com/monetarium/monetarium-node/chaincfg"
	"github.com/monetarium/monetarium-node/dcrutil"
)

func TestStakePoolTicketFee(t *testing.T) {
	params := chaincfg.MainNetParams()
	tests := []struct {
		StakeDiff       dcrutil.Amount
		Fee             dcrutil.Amount
		Height          int32
		PoolFee         float64
		Expected        dcrutil.Amount
		IsDCP0010Active bool
		IsDCP0012Active bool
	}{
		0: {
			StakeDiff:       10 * 1e8,
			Fee:             0.01 * 1e8,
			Height:          25000,
			PoolFee:         1.00,
			Expected:        0.03882666 * 1e8,
			IsDCP0010Active: false,
			IsDCP0012Active: false,
		},
		1: {
			StakeDiff:       20 * 1e8,
			Fee:             0.01 * 1e8,
			Height:          25000,
			PoolFee:         1.00,
			Expected:        0.04814436 * 1e8,
			IsDCP0010Active: false,
			IsDCP0012Active: false,
		},
		2: {
			StakeDiff:       5 * 1e8,
			Fee:             0.05 * 1e8,
			Height:          50000,
			PoolFee:         2.59,
			Expected:        0.07310812 * 1e8,
			IsDCP0010Active: false,
			IsDCP0012Active: false,
		},
		3: {
			StakeDiff:       15 * 1e8,
			Fee:             0.05 * 1e8,
			Height:          50000,
			PoolFee:         2.59,
			Expected:        0.11576278 * 1e8,
			IsDCP0010Active: false,
			IsDCP0012Active: false,
		},
		4: {
			StakeDiff:       15 * 1e8,
			Fee:             0.05 * 1e8,
			Height:          50000,
			PoolFee:         2.59,
			Expected:        0.11576278 * 1e8,
			IsDCP0010Active: true,
			IsDCP0012Active: false,
		},
		5: {
			StakeDiff:       15 * 1e8,
			Fee:             0.05 * 1e8,
			Height:          50000,
			PoolFee:         2.59,
			Expected:        0.11576278 * 1e8,
			IsDCP0010Active: false,
			IsDCP0012Active: true,
		},
		6: {
			StakeDiff:       15 * 1e8,
			Fee:             0.05 * 1e8,
			Height:          50000,
			PoolFee:         2.59,
			Expected:        0.11576278 * 1e8,
			IsDCP0010Active: true,
			IsDCP0012Active: true,
		},
	}
	for i, test := range tests {
		poolFeeAmt := StakePoolTicketFee(test.StakeDiff, test.Fee, test.Height,
			test.PoolFee, params, test.IsDCP0010Active, test.IsDCP0012Active)
		if poolFeeAmt != test.Expected {
			t.Errorf("Test %d: Got %v: Want %v", i, poolFeeAmt, test.Expected)
		}
	}
}
