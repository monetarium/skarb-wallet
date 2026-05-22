// Backend smoke test for the Monetarium fork of Cryptopower.
//
// Verifies that:
//   1. AssetsManager creates and a Monetarium wallet can be made on testnet.
//   2. HD derivation produces an address with a Monetarium testnet prefix.
//   3. The chaincfg-driven multi-coin API works:
//        - ActiveCoinTypes() lists VAR + every SKA-n active on the chain.
//        - GetAccountCoinBalances(0) returns a CoinBalance map covering them.
//        - GetWalletCoinBalances() aggregates the same across the wallet.
//
// Balances will be zero on a fresh wallet — the point is to prove the API
// chain works end-to-end, not to send funds.
//
// Run with:   go run ./cmd/smoke
package main

import (
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"github.com/monetarium/skarb-wallet/libwallet"
	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/monetarium-node/cointype"
	"github.com/monetarium/monetarium-node/dcrutil"
	dcrW "github.com/monetarium/monetarium-wallet/wallet"
)

func main() {
	tmp, err := os.MkdirTemp("", "monetarium-smoke-")
	if err != nil {
		die("mkdir: %v", err)
	}
	defer os.RemoveAll(tmp)

	rootDir := filepath.Join(tmp, "data")
	logDir := filepath.Join(tmp, "logs")

	fmt.Printf("→ creating AssetsManager in %s\n", rootDir)
	mgr, err := libwallet.NewAssetsManager(rootDir, logDir, utils.Testnet)
	if err != nil {
		die("NewAssetsManager: %v", err)
	}
	defer mgr.Shutdown()

	fmt.Println("→ creating new DCR/Monetarium wallet ...")
	wallet, err := mgr.CreateNewDCRWallet(
		"smoke",
		"hunter2hunter2",
		sharedW.PassphraseTypePass,
		sharedW.WordSeed33,
	)
	if err != nil {
		die("CreateNewDCRWallet: %v", err)
	}

	fmt.Printf("→ wallet ID:        %d\n", wallet.GetWalletID())
	fmt.Printf("→ wallet name:      %s\n", wallet.GetWalletName())
	fmt.Printf("→ asset type:       %s\n", wallet.GetAssetType())

	addr, err := wallet.CurrentAddress(0)
	if err != nil {
		die("CurrentAddress: %v", err)
	}
	fmt.Printf("→ first address:    %s\n", addr)

	// Testnet prefix should be one of Monetarium testnet prefixes:
	// Tk (PubKey), Ts (P2PKH), Te (Edwards P2PKH), TS (Schnorr P2PKH), Tc (P2SH)
	prefixes := []string{"Tk", "Ts", "Te", "TS", "Tc"}
	matched := ""
	for _, p := range prefixes {
		if strings.HasPrefix(addr, p) {
			matched = p
			break
		}
	}
	if matched == "" {
		die("address prefix is not any of %v — got %q", prefixes, addr)
	}
	fmt.Printf("→ prefix %q OK (Monetarium testnet)\n", matched)

	// --- Multi-coin API ----------------------------------------------------
	dcrAsset, ok := wallet.(*dcr.Asset)
	if !ok {
		die("wallet is not a *dcr.Asset, got %T", wallet)
	}

	cts := dcrAsset.ActiveCoinTypes()
	fmt.Printf("→ active coin types: %d  ", len(cts))
	for i, ct := range cts {
		if i > 0 {
			fmt.Print(", ")
		}
		fmt.Print(ct)
	}
	fmt.Println()

	if len(cts) == 0 {
		die("no active coin types reported — chaincfg.GetActiveSKATypes() returned empty AND VAR is missing")
	}

	fmt.Println("→ GetAccountCoinBalances(account=0):")
	acctBalances, err := dcrAsset.GetAccountCoinBalances(0)
	if err != nil {
		die("GetAccountCoinBalances: %v", err)
	}
	for _, ct := range cts {
		bal, ok := acctBalances[ct]
		if !ok {
			die("expected balance entry for %s, got nothing", ct)
		}
		if ct.IsVAR() {
			fmt.Printf("    %-6s total=%s spendable=%s unconfirmed=%s\n",
				ct, bal.Total, bal.Spendable, bal.Unconfirmed)
		} else {
			fmt.Printf("    %-6s total=%s spendable=%s unconfirmed=%s (atoms, 1e18=1 SKA)\n",
				ct,
				bal.SKATotal.String(),
				bal.SKASpendable.String(),
				bal.SKAUnconfirmed.String())
		}
	}

	fmt.Println("→ GetWalletCoinBalances() (sum across all accounts):")
	walletBalances, err := dcrAsset.GetWalletCoinBalances()
	if err != nil {
		die("GetWalletCoinBalances: %v", err)
	}
	for _, ct := range cts {
		bal := walletBalances[ct]
		fmt.Printf("    %s — %s\n", ct, dcr.FormatCoinAmount(bal))
	}

	// --- Decimal formatter ------------------------------------------------
	fmt.Println("→ FormatCoinAmount sanity checks:")
	checkFormat := func(label, want, got string) {
		if want != got {
			die("%s: want %q, got %q", label, want, got)
		}
		fmt.Printf("    %s -> %q\n", label, got)
	}
	// VAR formatter goes through dcrutil.Amount.String() ("1.5 VAR" for 1.5e8 atoms).
	varBal := dcrW.CoinBalance{CoinType: cointype.CoinTypeVAR, Total: dcrutil.Amount(150000000)}
	checkFormat("VAR  1.5e8 atoms", "1.5 VAR", dcr.FormatCoinAmount(varBal))
	// SKA formatter divides by 1e18 and appends the coin label.
	oneAndAHalfSKA := new(big.Int).Mul(big.NewInt(15), new(big.Int).Exp(big.NewInt(10), big.NewInt(17), nil))
	skaBal := dcrW.CoinBalance{CoinType: cointype.CoinType(1), SKATotal: cointype.NewSKAAmount(oneAndAHalfSKA)}
	checkFormat("SKA1 1.5e18 atoms", "1.5 SKA1", dcr.FormatCoinAmount(skaBal))
	// 1 atom should render as the smallest expressible fraction.
	dustBal := dcrW.CoinBalance{CoinType: cointype.CoinType(1), SKATotal: cointype.SKAAmountFromInt64(1)}
	checkFormat("SKA1 1 atom", "0.000000000000000001 SKA1", dcr.FormatCoinAmount(dustBal))

	// --- Tx authoring with CoinType ---------------------------------------
	fmt.Println("→ NewUnsignedTx + SetTxCoinType round-trip:")
	if err := dcrAsset.NewUnsignedTx(0, nil); err != nil {
		die("NewUnsignedTx: %v", err)
	}
	if got := dcrAsset.TxCoinType(); got != cts[0] {
		die("default CoinType after NewUnsignedTx is %s, expected %s", got, cts[0])
	}
	for _, ct := range cts {
		if err := dcrAsset.SetTxCoinType(ct); err != nil {
			die("SetTxCoinType(%s): %v", ct, err)
		}
		if dcrAsset.TxCoinType() != ct {
			die("TxCoinType() returned %s after SetTxCoinType(%s)", dcrAsset.TxCoinType(), ct)
		}
		fmt.Printf("    SetTxCoinType(%s) OK\n", ct)
	}
	if err := dcrAsset.SetTxCoinType(99); err == nil {
		die("SetTxCoinType(SKA-99) should have failed (not active)")
	} else {
		fmt.Printf("    SetTxCoinType(SKA-99) correctly rejected: %v\n", err)
	}

	fmt.Println()
	fmt.Println("✅ Backend smoke test PASSED")
	fmt.Println("   • monetarium-wallet API works through the Cryptopower libwallet shim")
	fmt.Println("   • HD derivation produces a Monetarium-testnet address")
	fmt.Println("   • Multi-coin API: ActiveCoinTypes / GetAccountCoinBalances / GetWalletCoinBalances all return non-error data")
	fmt.Println("   • Tx authoring: SetTxCoinType accepts active coin types and rejects inactive ones")
}

func die(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "❌ "+format+"\n", args...)
	os.Exit(1)
}
