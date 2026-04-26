package txhelper

import (
	"github.com/monetarium/monetarium-cryptopower/libwallet/addresshelper"
	"github.com/monetarium/monetarium-node/cointype"
	dcrutil "github.com/monetarium/monetarium-node/dcrutil"
	"github.com/monetarium/monetarium-node/wire"
)

// MakeTxOutput builds a wire.TxOut with CoinType=VAR. Use MakeCoinTypeTxOutput
// when sending a SKA-n asset.
func MakeTxOutput(address string, amountInAtom int64, net dcrutil.AddressParams) (*wire.TxOut, error) {
	return MakeCoinTypeTxOutput(address, amountInAtom, cointype.CoinTypeVAR, net)
}

// MakeCoinTypeTxOutput builds a wire.TxOut tagged with the given CoinType.
// All TxOuts in a single transaction must share the same CoinType — this is
// enforced by monetarium-node consensus, not by the wallet, but mixing them
// here will produce a tx that the network will reject.
func MakeCoinTypeTxOutput(address string, amountInAtom int64, ct cointype.CoinType, net dcrutil.AddressParams) (*wire.TxOut, error) {
	pkScript, err := addresshelper.PkScript(address, net)
	if err != nil {
		return nil, err
	}
	return &wire.TxOut{
		Value:    amountInAtom,
		Version:  scriptVersion,
		PkScript: pkScript,
		CoinType: ct,
	}, nil
}
