package load

import (
	"fmt"
	"strconv"

	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
)

func MixedAccountNumber(w sharedW.Asset) int32 {
	if asset, ok := w.(*dcr.Asset); ok {
		return asset.MixedAccountNumber()
	}
	return -1
}

// SetAPIFeeRate is a no-op stub for Monetarium (DCR-style wallets manage their
// own fees via the wallet API; the public-API rate setter is BTC/LTC-only).
func SetAPIFeeRate(w sharedW.Asset, feerate string) (int64, error) {
	rate, err := strconv.ParseInt(feerate, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("(%v) not valid tx fee rate", feerate)
	}
	return rate, nil
}

// GetAPIFeeRate is a no-op stub for Monetarium (no public BTC/LTC fee API).
func GetAPIFeeRate(w sharedW.Asset) ([]sharedW.FeeEstimate, error) {
	return nil, nil
}
