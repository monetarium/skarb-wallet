package dcr

import (
	"context"
	"fmt"
	"math/big"

	"github.com/monetarium/monetarium-node/cointype"
	"github.com/monetarium/monetarium-node/dcrutil"
	dcrW "github.com/monetarium/monetarium-wallet/wallet"
)

// ActiveCoinTypes returns the list of coin types currently active on the
// chain the wallet is connected to. CoinTypeVAR is always first; SKAn types
// follow in numeric order. The list mirrors chaincfg.Params: the wallet does
// not invent new coin types; activation is a consensus-level decision.
func (asset *Asset) ActiveCoinTypes() []cointype.CoinType {
	out := []cointype.CoinType{cointype.CoinTypeVAR}
	out = append(out, asset.chainParams.GetActiveSKATypes()...)
	return out
}

// GetCoinBalance returns the balance of a single coin type on a single account.
// For VAR the int64 atom fields (Spendable, Total, ...) are populated.
// For SKA the *big.Int SKA* fields are populated; the atom fields hold a
// truncated approximation safe for display only.
func (asset *Asset) GetCoinBalance(accountNumber int32, coinType cointype.CoinType) (dcrW.CoinBalance, error) {
	if !asset.WalletOpened() {
		return dcrW.CoinBalance{}, fmt.Errorf("wallet not opened")
	}
	ctx, _ := asset.ShutdownContextWithCancel()
	return asset.Internal().DCR.AccountBalanceByCoinType(
		ctx,
		uint32(accountNumber),
		coinType,
		asset.RequiredConfirmations(),
	)
}

// GetAccountCoinBalances returns the per-coin balance breakdown for a single
// account. The result includes only coin types currently active on the chain.
func (asset *Asset) GetAccountCoinBalances(accountNumber int32) (map[cointype.CoinType]dcrW.CoinBalance, error) {
	if !asset.WalletOpened() {
		return nil, fmt.Errorf("wallet not opened")
	}

	ctx, _ := asset.ShutdownContextWithCancel()
	confirms := asset.RequiredConfirmations()

	out := make(map[cointype.CoinType]dcrW.CoinBalance)
	for _, ct := range asset.ActiveCoinTypes() {
		bal, err := asset.Internal().DCR.AccountBalanceByCoinType(ctx, uint32(accountNumber), ct, confirms)
		if err != nil {
			return nil, fmt.Errorf("AccountBalanceByCoinType(account=%d coinType=%s): %w",
				accountNumber, ct, err)
		}
		out[ct] = bal
	}
	return out, nil
}

// GetWalletCoinBalances aggregates balances across every account in the wallet.
// The result is a map of coin type to a *summed* CoinBalance — only Total,
// Spendable, Unconfirmed and the SKA equivalents are aggregated; per-stake
// fields are not summed (they are staking-era leftovers, not relevant to v1).
func (asset *Asset) GetWalletCoinBalances() (map[cointype.CoinType]dcrW.CoinBalance, error) {
	if !asset.WalletOpened() {
		return nil, fmt.Errorf("wallet not opened")
	}

	accounts, err := asset.GetAccountsRaw()
	if err != nil {
		return nil, fmt.Errorf("GetAccountsRaw: %w", err)
	}

	totals := make(map[cointype.CoinType]dcrW.CoinBalance)
	for _, a := range accounts.Accounts {
		acctBalances, err := asset.GetAccountCoinBalances(int32(a.AccountNumber))
		if err != nil {
			return nil, err
		}
		for ct, bal := range acctBalances {
			running := totals[ct]
			running.CoinType = ct
			running.Total += bal.Total
			running.Spendable += bal.Spendable
			running.Unconfirmed += bal.Unconfirmed
			if ct.IsSKA() {
				running.SKATotal = running.SKATotal.Add(bal.SKATotal)
				running.SKASpendable = running.SKASpendable.Add(bal.SKASpendable)
				running.SKAUnconfirmed = running.SKAUnconfirmed.Add(bal.SKAUnconfirmed)
			}
			totals[ct] = running
		}
	}
	return totals, nil
}

// FormatCoinAmount renders a CoinBalance.Total value as a human-readable
// decimal string with the right number of decimal places for the given coin
// type. Uses int64 atoms for VAR (1e8 atoms/coin) and *big.Int SKA atoms
// (1e18 atoms/coin by default) for SKA coins.
//
// Use this everywhere balances are shown to users — never divide manually,
// the VAR (1e8) vs SKA (1e18) magnitude difference is too easy to mix up.
//
// Example outputs:
//
//	VAR  total=150000000  -> "1.5 VAR"
//	SKA-1 total=1500000000000000000 -> "1.5 SKA-1"
//	SKA-1 total=1                   -> "0.000000000000000001 SKA-1"
func FormatCoinAmount(bal dcrW.CoinBalance) string {
	if bal.CoinType.IsVAR() {
		return dcrutil.Amount(bal.Total).String()
	}
	// SKA: render the *big.Int SKATotal with the SKA-default 1e18 divisor.
	// Per-coin AtomsPerCoin overrides live in chaincfg.SKACoinConfig but are
	// 1e18 for every active SKA today; switch to a Params-driven lookup when
	// that changes.
	atomsStr := bal.SKATotal.ToDecimalString(cointype.AtomsPerSKACoin)
	return atomsStr + " " + CoinSymbol(bal.CoinType)
}

// FormatTxAmount renders a transaction-side int64 atom value with the correct
// scale and unit suffix for the given coin type. Use this anywhere we display
// a tx amount (history list, tx details, notification text) so an SKA tx
// stops being labeled "X.XXXXXXXX VAR" by the legacy dcrutil.Amount.String().
//
// VAR amounts go through dcrutil.Amount.String() unchanged (1e8 atoms/coin).
// SKA amounts use big.Int math against AtomsPerSKACoin (1e18 by default).
//
// Use FormatTxAmountBig when the atom value may exceed int64 — the int64
// channel here gets clamped at decode time and the row would display the
// MaxInt64 / 1e18 = 9.223... SKA1 ceiling forever.
//
// coinType is a uint8 because sharedW.Transaction.CoinType is uint8 to stay
// stable across the storm-DB schema; we coerce to cointype.CoinType inside.
func FormatTxAmount(atoms int64, coinType uint8) string {
	ct := cointype.CoinType(coinType)
	if !ct.IsValid() || ct.IsVAR() {
		return dcrutil.Amount(atoms).String()
	}
	amt := cointype.NewSKAAmount(big.NewInt(atoms))
	return amt.ToDecimalString(cointype.AtomsPerSKACoin) + " " + CoinSymbol(ct)
}

// FormatTxAmountBig is the lossless variant for SKA amounts that exceed
// int64. Pass the decimal-string atoms field from TxInput / TxOutput /
// Transaction.AmountAtoms (populated by the tx decoder when the big.Int
// value would otherwise be clamped to MaxInt64). When the string is empty,
// it falls back to the int64 path so callers can write a single dispatch:
//
//	FormatTxAmountBig(in.AmountAtoms, in.Amount, tx.CoinType)
//
// VAR coin type ignores the big-int path entirely (VAR fits in int64 by
// definition of its 21M*1e8 supply cap); for SKA we render the big.Int
// directly. Returns "X.YZ Unit" — same suffix grammar as FormatTxAmount.
func FormatTxAmountBig(atomsStr string, atomsInt int64, coinType uint8) string {
	ct := cointype.CoinType(coinType)
	if !ct.IsValid() || ct.IsVAR() || atomsStr == "" {
		return FormatTxAmount(atomsInt, coinType)
	}
	atoms, ok := new(big.Int).SetString(atomsStr, 10)
	if !ok {
		return FormatTxAmount(atomsInt, coinType)
	}
	amt := cointype.NewSKAAmount(atoms)
	return amt.ToDecimalString(cointype.AtomsPerSKACoin) + " " + CoinSymbol(ct)
}

// CoinSymbol returns the user-facing symbol for a coin type. Wraps
// cointype.CoinType.String() to drop the hyphen — upstream renders SKA tokens
// as "SKA-1" / "SKA-2" / …, but the product brand format is "SKA1" / "SKA2".
// VAR is unchanged.
func CoinSymbol(ct cointype.CoinType) string {
	if ct.IsVAR() {
		return ct.String()
	}
	// "SKA-1" -> "SKA1". Fast path that avoids the strings package import in
	// hot UI render paths.
	s := ct.String()
	if len(s) >= 5 && s[:4] == "SKA-" {
		return s[:3] + s[4:]
	}
	return s
}

// IsCoinTypeActive reports whether the given coin type is active on the
// chain the wallet is connected to. Always true for VAR.
func (asset *Asset) IsCoinTypeActive(ct cointype.CoinType) bool {
	if ct.IsVAR() {
		return true
	}
	return asset.chainParams.IsSKACoinTypeActive(ct)
}

// dummyContext keeps `context` an explicit dependency in case future revisions
// want to thread cancellation through the helpers here.
var _ = context.Background
