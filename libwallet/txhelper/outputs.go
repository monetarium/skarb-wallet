package txhelper

import (
	"github.com/monetarium/monetarium-cryptopower/libwallet/addresshelper"
	dcrutil "github.com/monetarium/monetarium-node/dcrutil"
	"github.com/monetarium/monetarium-node/wire"
)

func MakeTxOutput(address string, amountInAtom int64, net dcrutil.AddressParams) (*wire.TxOut, error) {
	pkScript, err := addresshelper.PkScript(address, net)
	if err != nil {
		return nil, err
	}
	return &wire.TxOut{
		Value:    amountInAtom,
		Version:  scriptVersion,
		PkScript: pkScript,
	}, nil
}
