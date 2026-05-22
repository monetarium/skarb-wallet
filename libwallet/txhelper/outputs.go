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

// MakeCoinTypeTxOutput builds a wire.TxOut tagged with the given CoinType
// from an int64 atom count. Convenience wrapper around MakeCoinTypeTxOutputBig
// for the (VAR, small-SKA) hot paths where int64 is enough — VAR is
// int64-bounded by definition (21M*1e8 < MaxInt64), and SKA outputs up to
// ~9.22 SKA per output fit in int64 atoms.
//
// For SKA outputs above int64 use MakeCoinTypeTxOutputBig directly with the
// caller's *big.Int so the value isn't silently truncated.
func MakeCoinTypeTxOutput(address string, amountInAtom int64, ct cointype.CoinType, net dcrutil.AddressParams) (*wire.TxOut, error) {
	var amountBig *big.Int
	if ct.IsSKA() {
		amountBig = big.NewInt(amountInAtom)
	}
	return MakeCoinTypeTxOutputBig(address, amountInAtom, amountBig, ct, net)
}

// MakeCoinTypeTxOutputBig is the lossless variant. Pass amountBig (non-nil)
// for SKA outputs whose atom count exceeds int64; amountInAtom is then a
// don't-care (still set on out.Value to zero per V13 wire format for SKA).
// For VAR, amountBig is ignored and amountInAtom is used directly.
//
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
// output has Value != 0 or SKAValue == nil.
func MakeCoinTypeTxOutputBig(address string, amountInAtom int64, amountBig *big.Int, ct cointype.CoinType, net dcrutil.AddressParams) (*wire.TxOut, error) {
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
		if amountBig != nil {
			// Copy to avoid downstream mutation of the caller's bigint.
			out.SKAValue = new(big.Int).Set(amountBig)
		} else {
			out.SKAValue = big.NewInt(amountInAtom)
		}
	} else {
		out.Value = amountInAtom
	}
	return out, nil
}
