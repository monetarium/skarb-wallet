package txhelper

import (
	"math/big"

	"github.com/monetarium/skarb-wallet/libwallet/addresshelper"
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
//
// SKA outputs have a different in-memory shape than VAR outputs: the atom
// value lives in the variable-length SKAValue (*big.Int) field, and the
// fixed Value field MUST be 0. This is required both by the V13 wire
// encoder (writeTxOutV13 ignores Value for SKA coin types and serializes
// SKAValue.Bytes()) and by monetarium-wallet's validateAuthoredCoinTypes,
// which loudly fails before the tx ever reaches the network if a SKA
// output has Value != 0 or SKAValue == nil. Producing a "Value=N,
// SKAValue=nil" output as the old code did would serialize SKA outputs as
// zero-value on the wire, and the receiving node would reject the tx.
func MakeCoinTypeTxOutput(address string, amountInAtom int64, ct cointype.CoinType, net dcrutil.AddressParams) (*wire.TxOut, error) {
	pkScript, err := addresshelper.PkScript(address, net)
	if err != nil {
		return nil, err
	}
	out := &wire.TxOut{
		Version:  scriptVersion,
		PkScript: pkScript,
		CoinType: ct,
	}
	if ct.IsSKA() {
		out.Value = 0
		out.SKAValue = big.NewInt(amountInAtom)
	} else {
		out.Value = amountInAtom
	}
	return out, nil
}
