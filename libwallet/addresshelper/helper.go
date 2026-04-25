package addresshelper

import (
	"fmt"

	"github.com/monetarium/monetarium-node/chaincfg"
	"github.com/monetarium/monetarium-node/dcrutil"
	"github.com/monetarium/monetarium-node/txscript/stdaddr"
	"github.com/monetarium/monetarium-node/txscript/stdscript"
)

const scriptVersion = 0

// PkScript returns the public key payment script for the given address.
func PkScript(address string, net dcrutil.AddressParams) ([]byte, error) {
	addr, err := stdaddr.DecodeAddress(address, net)
	if err != nil {
		return nil, fmt.Errorf("error decoding address '%s': %s", address, err.Error())
	}

	_, pkScript := addr.PaymentScript()
	return pkScript, nil
}

// PkScriptAddresses returns the addresses for the given public key script.
func PkScriptAddresses(params *chaincfg.Params, pkScript []byte) []string {
	_, addresses := stdscript.ExtractAddrs(scriptVersion, pkScript, params)
	encodedAddresses := make([]string, len(addresses))
	for i, address := range addresses {
		encodedAddresses[i] = address.String()
	}
	return encodedAddresses
}
