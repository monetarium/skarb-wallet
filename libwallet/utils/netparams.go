package utils

import (
	"fmt"
	"strings"

	dcrcfg "github.com/monetarium/monetarium-node/chaincfg"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

type NetworkType string

const (
	Mainnet    NetworkType = "mainnet"
	Testnet    NetworkType = "testnet"
	Regression NetworkType = "regression"
	Simulation NetworkType = "simulation"
	DEXTest    NetworkType = "dextest"
	Unknown    NetworkType = "unknown"
)

// Display returns the title case network name to be displayed on the app UI.
func (n NetworkType) Display() string {
	caser := cases.Title(language.Und)
	return caser.String(string(n))
}

// ToNetworkType maps the provided network string identifier to the available
// network type constants.
func ToNetworkType(str string) NetworkType {
	switch strings.ToLower(str) {
	case "mainnet":
		return Mainnet
	case "testnet", "testnet3", "test", "testnet4":
		return Testnet
	case "regression", "reg", "regnet":
		return Regression
	case "simulation", "sim", "simnet":
		return Simulation
	case "dextest":
		return DEXTest
	default:
		return Unknown
	}
}

// ChainsParams collectively defines the chain parameters of all assets
// supported by the Monetarium wallet. v1 only ships DCR (i.e. the Monetarium
// chain itself) — BTC/LTC fields were removed when those assets dropped out
// of the Cryptopower fork.
type ChainsParams struct {
	DCR *dcrcfg.Params
}

var (
	DCRmainnetParams = dcrcfg.MainNetParams()
	DCRtestnetParams = dcrcfg.TestNet3Params()
	DCRSimnetParams  = dcrcfg.SimNetParams()
	DCRRegnetParams  = dcrcfg.RegNetParams()
)

// NetDir returns the data directory name for a given asset's type and network.
// Returns "unknown" for any unsupported asset type or network.
func NetDir(assetType AssetType, netType NetworkType) string {
	if assetType != DCRWalletAsset {
		return "unknown"
	}
	params, err := DCRChainParams(netType)
	if err != nil {
		return "unknown"
	}
	return strings.ToLower(params.Name)
}

// DCRChainParams returns the network parameters for the Monetarium chain
// (kept under the DCR-shaped name to limit refactor blast radius from the
// Cryptopower base — the names are historic, the chain is Monetarium).
func DCRChainParams(netType NetworkType) (*dcrcfg.Params, error) {
	switch netType {
	case Mainnet:
		return DCRmainnetParams, nil
	case Testnet:
		return DCRtestnetParams, nil
	case Simulation:
		return DCRSimnetParams, nil
	case Regression:
		return DCRRegnetParams, nil
	default:
		return nil, fmt.Errorf("%v: (%v)", ErrInvalidNet, netType)
	}
}

// GetChainParams returns the network parameters wrapped in a ChainsParams
// struct. Only DCR (Monetarium) is supported in v1.
func GetChainParams(assetType AssetType, netType NetworkType) (*ChainsParams, error) {
	if assetType != DCRWalletAsset {
		return nil, fmt.Errorf("%v: (%v)", ErrAssetUnknown, assetType)
	}
	params, err := DCRChainParams(netType)
	if err != nil {
		return nil, err
	}
	return &ChainsParams{DCR: params}, nil
}
