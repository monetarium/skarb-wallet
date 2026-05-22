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

// SigScriptSenderAddress derives the ECDSA-secp256k1 P2PKH address that
// signed a P2PKH transaction input from its signature script. Used to
// surface a "From" address on received transactions: the input's sigScript
// reveals the spender's pubkey, and hash160(pubkey) is the P2PKH address
// that owned the previous output — the sender of the inbound transfer from
// the recipient's POV.
//
// Standard P2PKH-ECDSA input on Decred is exactly two data pushes:
//
//	OP_DATA_N   <DER-encoded ECDSA signature + 1-byte sighash flag>
//	OP_DATA_M   <compressed (33B) or uncompressed (65B) secp256k1 pubkey>
//
// We validate this exact shape (two pushes, nothing else) before deriving.
// Any other shape returns ("", nil) — addresses derived from non-P2PKH-ECDSA
// scripts would be wrong:
//
//   - P2SH-multisig: trailing push is the redeem script, not a pubkey;
//     hash160 of it has no relation to the spender's address.
//   - Schnorr-secp256k1 P2PKH (Decred STSchnorrSecp256k1): same 33-byte
//     pubkey shape, but the on-chain address class uses a different
//     hash algorithm — emitting an ECDSA P2PKH would lie about who signed.
//   - Ed25519 P2PKH: 32-byte pubkey, won't match the 33/65 length check
//     anyway, but the shape check above catches it earlier.
//   - Coinbase inputs: caller must skip via PreviousOutPoint check before
//     calling us; we accept them as "not standard P2PKH" and return "".
func SigScriptSenderAddress(sigScript []byte, params *chaincfg.Params) (string, error) {
	if len(sigScript) == 0 {
		return "", nil
	}
	tokenizer := txscript.MakeScriptTokenizer(scriptVersion, sigScript)
	pushes := make([][]byte, 0, 2)
	for tokenizer.Next() {
		// Any non-data opcode disqualifies (e.g. OP_0, OP_1, anything
		// that would suggest multisig / redeem-script-encoded P2SH).
		if tokenizer.Opcode() > txscript.OP_PUSHDATA4 {
			return "", nil
		}
		data := tokenizer.Data()
		if len(data) == 0 {
			// Empty push (OP_0) — not part of canonical P2PKH-ECDSA.
			return "", nil
		}
		pushes = append(pushes, data)
		if len(pushes) > 2 {
			// More than two pushes — definitely not standard P2PKH.
			return "", nil
		}
	}
	if err := tokenizer.Err(); err != nil {
		return "", fmt.Errorf("tokenize sigScript: %w", err)
	}
	if len(pushes) != 2 {
		return "", nil
	}
	sig, pubKey := pushes[0], pushes[1]
	// Sanity-check signature shape: DER signatures are 71-72 bytes
	// typically (sometimes 70 or 73 for edge-case r/s lengths). Anything
	// outside [64, 73] with a leading 0x30 is suspect for ECDSA-DER.
	if len(sig) < 64 || len(sig) > 73 || sig[0] != 0x30 {
		return "", nil
	}
	// Pubkey shape: compressed (33 bytes, 0x02/0x03 prefix) or
	// uncompressed (65 bytes, 0x04 prefix).
	switch len(pubKey) {
	case 33:
		if pubKey[0] != 0x02 && pubKey[0] != 0x03 {
			return "", nil
		}
	case 65:
		if pubKey[0] != 0x04 {
			return "", nil
		}
	default:
		return "", nil
	}
	pkHash := dcrutil.Hash160(pubKey)
	addr, err := stdaddr.NewAddressPubKeyHashEcdsaSecp256k1V0(pkHash, params)
	if err != nil {
		return "", fmt.Errorf("derive P2PKH from pubkey hash: %w", err)
	}
	return addr.String(), nil
}
