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
	"github.com/monetarium/skarb-wallet/libwallet/txhelper"
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

	// --- Amount-string → atoms parser (bug #3 regression) ----------------
	// Pinning the lossless decimal-string parser. Prior to this fix the
	// send page went through strconv.ParseFloat → float64 × 1e18, and a
	// user-typed 18-digit SKA amount silently lost its last ~3 digits.
	// We deliberately use a value whose representation in float64 would
	// shift to a nearby double; if the parser regresses to a float path
	// these expectations will diverge and the smoke test will fail.
	fmt.Println("→ ParseAmountToAtomsBig (lossless string → atoms):")
	type atomCase struct {
		in        string
		ct        cointype.CoinType
		want      string
		shouldErr bool
	}
	cases := []atomCase{
		{"1", cointype.CoinTypeVAR, "100000000", false},                    // 1 VAR = 1e8 atoms
		{"0.12345678", cointype.CoinTypeVAR, "12345678", false},            // VAR full precision (8 dec)
		{"0.123456789", cointype.CoinTypeVAR, "", true},                    // VAR > 8 frac digits → reject
		{"1", cointype.CoinType(1), "1000000000000000000", false},          // 1 SKA1 = 1e18 atoms
		{"1.234567890123456789", cointype.CoinType(1), "1234567890123456789", false},  // 18-digit lossless
		{"0.000000000000000001", cointype.CoinType(1), "1", false},         // 1 SKA atom
		{"1,5", cointype.CoinType(1), "1500000000000000000", false},        // comma decimal separator (uk locale)
		{"-1", cointype.CoinType(1), "", true},                             // negative → reject
		{"abc", cointype.CoinType(1), "", true},                            // non-digits → reject
		{"", cointype.CoinType(1), "", true},                               // empty → reject
		{"1.2345678901234567890", cointype.CoinType(1), "", true},          // >18 frac digits → reject
	}
	for _, tc := range cases {
		got, err := dcr.ParseAmountToAtomsBig(tc.in, tc.ct)
		if tc.shouldErr {
			if err == nil {
				die("ParseAmountToAtomsBig(%q, %s) = %v, want error", tc.in, tc.ct, got)
			}
			fmt.Printf("    %-25q [%s]  rejected as expected: %v\n", tc.in, tc.ct, err)
			continue
		}
		if err != nil {
			die("ParseAmountToAtomsBig(%q, %s): %v", tc.in, tc.ct, err)
		}
		if got.String() != tc.want {
			die("ParseAmountToAtomsBig(%q, %s) = %s, want %s", tc.in, tc.ct, got.String(), tc.want)
		}
		fmt.Printf("    %-25q [%s] → %s atoms ✓\n", tc.in, tc.ct, got.String())
	}

	// --- TransactionAmountAndDirectionBig (bug #5/#6 regression) ---------
	// A Sent SKA tx that crosses int64 used to misclassify as Received
	// because the int64 totals clamped at MaxInt64 and the subtraction
	// flipped sign. Pin the big.Int classifier on a value that overflows
	// int64 to lock that path.
	fmt.Println("→ TransactionAmountAndDirectionBig (overflow-safe classifier):")
	tenSKA := new(big.Int).Mul(big.NewInt(10), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)) // 10 SKA atoms (>MaxInt64)
	threeSKA := new(big.Int).Mul(big.NewInt(3), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	feeSmall := big.NewInt(100_000_000) // 1e8 atoms fee (any positive)
	// Sent: we owned 10 SKA inputs, change is 3 SKA (wallet output), 7 SKA went external.
	_, dir := txhelper.TransactionAmountAndDirectionBig(tenSKA, threeSKA, feeSmall)
	if dir != txhelper.TxDirectionSent {
		die("Big classifier: Sent SKA tx classified as direction=%d, want %d", dir, txhelper.TxDirectionSent)
	}
	fmt.Printf("    10 SKA in, 3 SKA wallet-out, fee=1e8 → Sent ✓\n")
	// Received: we owned nothing on inputs side, gained 10 SKA on outputs.
	_, dir = txhelper.TransactionAmountAndDirectionBig(big.NewInt(0), tenSKA, big.NewInt(0))
	if dir != txhelper.TxDirectionReceived {
		die("Big classifier: Received SKA tx classified as direction=%d, want %d", dir, txhelper.TxDirectionReceived)
	}
	fmt.Printf("    0 in, 10 SKA out, fee=0 → Received ✓\n")
	// Transferred: in == out + fee.
	withFee := new(big.Int).Add(threeSKA, feeSmall)
	_, dir = txhelper.TransactionAmountAndDirectionBig(withFee, threeSKA, feeSmall)
	if dir != txhelper.TxDirectionTransferred {
		die("Big classifier: Transferred SKA tx classified as direction=%d, want %d", dir, txhelper.TxDirectionTransferred)
	}
	fmt.Printf("    in == out+fee → Transferred ✓\n")

	fmt.Println()
	fmt.Println("✅ Backend smoke test PASSED")
	fmt.Println("   • monetarium-wallet API works through the Cryptopower libwallet shim")
	fmt.Println("   • HD derivation produces a Monetarium-testnet address")
	fmt.Println("   • Multi-coin API: ActiveCoinTypes / GetAccountCoinBalances / GetWalletCoinBalances all return non-error data")
	fmt.Println("   • Tx authoring: SetTxCoinType accepts active coin types and rejects inactive ones")
	fmt.Println("   • ParseAmountToAtomsBig is lossless across the full 18-digit SKA precision range")
	fmt.Println("   • TransactionAmountAndDirectionBig classifies SKA txs correctly above int64")
}

func die(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "❌ "+format+"\n", args...)
	os.Exit(1)
}
