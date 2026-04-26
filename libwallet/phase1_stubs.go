// Phase-1 stubs for methods/fields that other Cryptopower-derived UI code still
// calls but whose implementation was removed when stripping out BTC/LTC,
// DCRDEX, instantswap, FX rate (ext) and staking VSP support.
//
// They are explicit no-ops so the build stays green while Phase 2 redesigns
// the UI around the multi-coin (VAR + SKAn) Monetarium model.

package libwallet

import (
	sharedW "github.com/monetarium/monetarium-cryptopower/libwallet/assets/wallet"
	"github.com/monetarium/monetarium-cryptopower/libwallet/utils"
)

// rateSourceStub satisfies the surface area that the UI calls on the original
// ext.RateSource. All methods are no-ops returning safe zero values.
type rateSourceStub struct{}

func (rateSourceStub) Name() string                                  { return "" }
func (rateSourceStub) ToggleSource(_ string) error                   { return nil }
func (rateSourceStub) ToggleStatus(_ bool)                           {}
func (rateSourceStub) Refresh(_ bool)                                {}
func (rateSourceStub) IsRateListenerExist(_ string) bool             { return true }
func (rateSourceStub) AddRateListener(_ interface{}, _ string) error { return nil }
func (rateSourceStub) RemoveRateListener(_ string)                   {}
func (rateSourceStub) IsWarningMsgListenerExist(_ string) bool       { return true }
func (rateSourceStub) AddWarningMsgListener(_ interface{}, _ string) error {
	return nil
}
func (rateSourceStub) RemoveWarningMsgListener(_ string) {}
func (rateSourceStub) Ready() bool                        { return false }
func (rateSourceStub) GetTicker(_ interface{}, _ ...bool) *tickerStub {
	return nil
}

// dexClientStub is the no-op DCRDEX client returned by DexClient().
type dexClientStub struct{}

func (dexClientStub) InitializedWithPassword() bool                 { return false }
func (dexClientStub) ExportSeed(_ []byte) (string, error)           { return "", nil }
func (dexClientStub) SetWalletPassword(_ ...interface{}) error      { return nil }
func (dexClientStub) Login(_ ...interface{}) error                  { return nil }
func (dexClientStub) WalletIDForAsset(_ ...interface{}) (*uint32, error) {
	return nil, nil
}
func (dexClientStub) Active() bool { return false }

// politeiaStub satisfies the surface area that the UI calls on the original
// politeia.Politeia. All methods are no-ops returning zero values.
type politeiaStub struct{}

func (politeiaStub) IsSyncing() bool                                       { return false }
func (politeiaStub) StartSync() error                                      { return nil }
func (politeiaStub) Sync(_ ...interface{}) error                           { return nil }
func (politeiaStub) StopSync()                                             {}
func (politeiaStub) AddNotificationListener(_ interface{}, _ string) error { return nil }
func (politeiaStub) RemoveNotificationListener(_ string)                   {}
func (politeiaStub) AddSyncCallback(_ interface{}, _ string) error         { return nil }
func (politeiaStub) RemoveSyncCallback(_ string)                           {}

// tickerStub is the no-op ticker returned by RateSource.GetTicker.
type tickerStub struct {
	LastTradePrice float64
}

// AllBTCWallets returns nothing; BTC support is not part of the Monetarium fork.
func (mgr *AssetsManager) AllBTCWallets() []sharedW.Asset { return nil }

// AllLTCWallets returns nothing; LTC support is not part of the Monetarium fork.
func (mgr *AssetsManager) AllLTCWallets() []sharedW.Asset { return nil }

// BTCBadWallets returns nothing; BTC support is not part of the Monetarium fork.
func (mgr *AssetsManager) BTCBadWallets() map[int]*sharedW.Wallet { return nil }

// LTCBadWallets returns nothing; LTC support is not part of the Monetarium fork.
func (mgr *AssetsManager) LTCBadWallets() map[int]*sharedW.Wallet { return nil }

// DEXTestAddr is empty in v1; DCRDEX integration was removed.
func (mgr *AssetsManager) DEXTestAddr() string { return "" }

// UpdateDEXCtx is a no-op; DCRDEX integration was removed in v1.
func (mgr *AssetsManager) UpdateDEXCtx(_ interface{}) {}

// CalculateAssetsUSDBalance returns an empty map; FX rate sourcing is disabled
// in v1. UI callers should treat the result as "no rates available".
func (mgr *AssetsManager) CalculateAssetsUSDBalance(_ map[utils.AssetType]sharedW.AssetAmount) (map[utils.AssetType]float64, error) {
	return map[utils.AssetType]float64{}, nil
}

// BTCHDPrefix returns an empty path; BTC is not part of the Monetarium fork.
func (mgr *AssetsManager) BTCHDPrefix() string { return "" }

// LTCHDPrefix returns an empty path; LTC is not part of the Monetarium fork.
func (mgr *AssetsManager) LTCHDPrefix() string { return "" }

// DEXCInitialized always reports false; DCRDEX support was removed in v1.
func (mgr *AssetsManager) DEXCInitialized() bool { return false }

// DexClient returns a no-op DCRDEX client stub.
func (mgr *AssetsManager) DexClient() dexClientStub { return dexClientStub{} }

// DeleteDEXData is a no-op; there is no DCRDEX state to delete.
func (mgr *AssetsManager) DeleteDEXData() error { return nil }
