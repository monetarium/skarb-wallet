package addresshelper

import (
	"fmt"

	"github.com/monetarium/monetarium-node/chaincfg"
	"github.com/monetarium/monetarium-node/dcrutil"
	"github.com/monetarium/monetarium-node/txscript"
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

// SigScriptSenderAddress derives the P2PKH address that signed a P2PKH
// transaction input from its signature script. Used to surface a "From"
// address on received transactions: the input's sigScript necessarily reveals
// the spender's pubkey (the signer of the prior UTXO), and the prior UTXO's
// address is hash160(pubkey) — which is precisely the sender of the inbound
// transfer from the recipient's point of view.
//
// Format for a standard P2PKH input on Decred is two pushes:
//
//	PUSH(<DER-encoded signature + sighash byte>)
//	PUSH(<compressed (33B) or uncompressed (65B) secp256k1 pubkey>)
//
// We walk the tokenized script and take the LAST data push as the pubkey, so
// any future sigScript shapes that prepend extra opaque pushes (e.g. for
// custom signers) still resolve as long as the trailing push is the pubkey.
// Returns "" without an error when the script is not a standard P2PKH input
// (multisig P2SH, OP_RETURN-spending edge cases, malformed scripts) — those
// cases simply aren't surfaceable as a single sender address.
func SigScriptSenderAddress(sigScript []byte, params *chaincfg.Params) (string, error) {
	if len(sigScript) == 0 {
		return "", nil
	}
	tokenizer := txscript.MakeScriptTokenizer(scriptVersion, sigScript)
	var lastPush []byte
	for tokenizer.Next() {
		if data := tokenizer.Data(); len(data) > 0 {
			lastPush = data
		}
	}
	if err := tokenizer.Err(); err != nil {
		return "", fmt.Errorf("tokenize sigScript: %w", err)
	}
	// Only secp256k1 pubkey shapes (33 / 65 bytes) are recognized here.
	// Schnorr / Ed25519 pubkeys travel with a different sigScript shape;
	// they aren't on the Monetarium P2PKH receive path today.
	if len(lastPush) != 33 && len(lastPush) != 65 {
		return "", nil
	}
	pkHash := dcrutil.Hash160(lastPush)
	addr, err := stdaddr.NewAddressPubKeyHashEcdsaSecp256k1V0(pkHash, params)
	if err != nil {
		return "", fmt.Errorf("derive P2PKH from pubkey hash: %w", err)
	}
	return addr.String(), nil
}
