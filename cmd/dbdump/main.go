// dbdump opens the wallet's storm-backed walletData.db and dumps the saved
// Transaction row for a given hash. Used to confirm whether the post-reindex
// schema (SenderAddress on inputs, full SKA amount fields, etc.) is actually
// persisted, vs. lost between decoder and storage.
//
// Closes immediately — does NOT lock against a running wallet, so only run
// while Skarb is QUIT (storm uses bbolt, which is exclusive-lock single-writer).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/asdine/storm"
	"github.com/asdine/storm/q"

	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
)

func main() {
	dbPath := flag.String("db", os.ExpandEnv("$HOME/Library/Application Support/Skarb/testnet-bdb/testnet3/dcr/1/walletData.db"), "path to walletData.db")
	txHash := flag.String("tx", "9ce4ff5189adbcd5248afe83696ec438e7c1eacc9367f6ce8126ddacb0d5f6d8", "tx hash to dump")
	all := flag.Bool("all", false, "dump every saved tx instead of a specific one")
	flag.Parse()

	db, err := storm.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", *dbPath, err)
		os.Exit(1)
	}
	defer db.Close()

	if *all {
		var txs []sharedW.Transaction
		if err := db.All(&txs); err != nil {
			fmt.Fprintf(os.Stderr, "All: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Saved transactions: %d\n", len(txs))
		for i, tx := range txs {
			fmt.Printf("  [%d] %s direction=%d coin=%d inputs=%d outputs=%d amount=%d fee=%d\n",
				i, tx.Hash, tx.Direction, tx.CoinType,
				len(tx.Inputs), len(tx.Outputs), tx.Amount, tx.Fee)
		}
		return
	}

	var tx sharedW.Transaction
	if err := db.Select(q.Eq("Hash", *txHash)).First(&tx); err != nil {
		fmt.Fprintf(os.Stderr, "find %s: %v\n", *txHash, err)
		os.Exit(1)
	}
	pretty, _ := json.MarshalIndent(tx, "", "  ")
	fmt.Println(string(pretty))
}
