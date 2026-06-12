// utxodump opens the upstream bbolt wallet.db read-only and lists every
// unspent output bucketed by coin type (u:0 = VAR, u:1..255 = SKAn). Used to
// answer "where do the UTXOs in the manual selector come from?" by showing
// exactly what the underlying store has — independent of the UI's
// account-filter / coin-filter / confirmation-depth logic.
//
// Requires the wallet to be QUIT (bbolt is single-writer exclusive-lock).
//
// Run: go run ./cmd/utxodump -db "<path-to-wallet.db>" [-coin N]
package main

import (
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"math/big"
	"os"

	"go.etcd.io/bbolt"
)

var wtxmgrNS = []byte("wtxmgr")

func unspentBucketName(ct int) []byte {
	return []byte(fmt.Sprintf("u:%d", ct))
}

// unspent key format (from monetarium-wallet/udb/txdb): 36 bytes
//
//	[0..32]   = tx hash (32 bytes)
//	[32..36]  = output index (uint32 BE)
//
// unspent value format: serialised credit row that we don't fully decode
// here — we just print the raw block-height + amount prefix when it's
// readable. The actual atom values live in the "credits" bucket linked
// from the unspent index; for diagnostic purposes the existence + outpoint
// is what matters.
func decodeOutPoint(k []byte) (string, uint32, bool) {
	if len(k) < 36 {
		return "", 0, false
	}
	h := make([]byte, 32)
	for i := 0; i < 32; i++ {
		h[i] = k[31-i] // reverse for human-readable display
	}
	idx := binary.BigEndian.Uint32(k[32:36])
	return hex.EncodeToString(h), idx, true
}

func main() {
	defaultDB := os.ExpandEnv("$HOME/Library/Application Support/skarb/testnet-bdb/testnet3/dcr/1/wallet.db")
	dbPath := flag.String("db", defaultDB, "path to upstream wallet.db (bbolt)")
	coinFilter := flag.Int("coin", -1, "limit to a single coin type (0=VAR, 1=SKA1, ...); -1 = all")
	flag.Parse()

	db, err := bbolt.Open(*dbPath, 0o600, &bbolt.Options{ReadOnly: true})
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", *dbPath, err)
		os.Exit(1)
	}
	defer db.Close()

	err = db.View(func(tx *bbolt.Tx) error {
		root := tx.Bucket(wtxmgrNS)
		if root == nil {
			return fmt.Errorf("namespace %q not found — wrong DB?", wtxmgrNS)
		}
		grandTotal := 0
		for ct := 0; ct <= 10; ct++ { // VAR + first 10 SKA types is plenty
			if *coinFilter >= 0 && ct != *coinFilter {
				continue
			}
			b := root.Bucket(unspentBucketName(ct))
			if b == nil {
				continue
			}
			label := fmt.Sprintf("SKA%d", ct)
			if ct == 0 {
				label = "VAR"
			}
			count := 0
			fmt.Printf("\n== coin type %d (%s) — bucket %q ==\n", ct, label, unspentBucketName(ct))
			_ = b.ForEach(func(k, v []byte) error {
				hash, idx, ok := decodeOutPoint(k)
				if !ok {
					fmt.Printf("  key=%x value-len=%d (unexpected key length)\n", k, len(v))
					return nil
				}
				// Try to peek the first 8 bytes of the value as a possible
				// atom amount (little-endian int64) — works for VAR. For
				// SKA the credit-row layout stores SKAValue as a length-
				// prefixed big.Int blob, so we just show the byte length.
				amountHint := ""
				if len(v) >= 8 {
					raw := int64(binary.LittleEndian.Uint64(v[:8]))
					if ct == 0 {
						amountHint = fmt.Sprintf("≈%.8f VAR (raw int64 %d)", float64(raw)/1e8, raw)
					} else {
						// SKA value is in the credit row, not the unspent
						// index — but if we find a big.Int blob in v,
						// surface its length so the human can confirm
						// "yes, this is a SKA UTXO with payload".
						bi := new(big.Int).SetBytes(v)
						amountHint = fmt.Sprintf("value-blob %d bytes (decimal preview: %s)", len(v), bi.String())
					}
				}
				fmt.Printf("  %s:%d  %s\n", hash, idx, amountHint)
				count++
				return nil
			})
			fmt.Printf("  -> %d UTXO(s) in coin type %d\n", count, ct)
			grandTotal += count
		}
		fmt.Printf("\n== total unspent across all listed coin types: %d ==\n", grandTotal)
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "view: %v\n", err)
		os.Exit(1)
	}
}
