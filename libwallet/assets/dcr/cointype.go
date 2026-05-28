package dcr

import (
	"context"
	"fmt"
	"math/big"
	"sort"

	"github.com/monetarium/monetarium-node/cointype"
	"github.com/monetarium/monetarium-node/dcrutil"
	dcrW "github.com/monetarium/monetarium-wallet/wallet"
)

// ActiveCoinTypes returns the list of coin types currently active on the
// chain the wallet is connected to. CoinTypeVAR is always first; SKAn types
// follow in numeric order. The list mirrors chaincfg.Params: the wallet does
// not invent new coin types; activation is a consensus-level decision.
//
// This is the "all coins the chain can carry" view — useful for tx
// authoring validation (SetTxCoinType refuses anything not in this set)
// and for smoke tests. UI selectors should use DisplayableCoinTypes
// instead so the user only sees coins they actually hold.
func (asset *Asset) ActiveCoinTypes() []cointype.CoinType {
	// chainParams.GetActiveSKATypes() ranges over a map[CoinType]Config
	// internally, so its output order is randomised on every call (Go
	// map iteration is randomised by design). Sort numerically so the
	// returned slice is stable across calls — without this every UI
	// Layout pass (including every hover/scroll event, since each
	// triggers a redraw) would reorder the coin rows, which is bug #1
	// in the v1 bug report ("hovering buttons flips the coin sort
	// order").
	skaTypes := asset.chainParams.GetActiveSKATypes()
	sort.Slice(skaTypes, func(i, j int) bool { return skaTypes[i] < skaTypes[j] })
	out := make([]cointype.CoinType, 0, 1+len(skaTypes))
	out = append(out, cointype.CoinTypeVAR)
	out = append(out, skaTypes...)
	return out
}

// DisplayableCoinTypes filters ActiveCoinTypes to the subset the wallet
// has any visible activity for. VAR is always included; an SKA-n coin
// type is included only when the wallet's aggregate balance across all
// accounts is non-zero (confirmed or unconfirmed). Use this in coin
// selectors and balance breakdowns so users don't see SKA-n entries
// they have never received — that was bug #7 in the v1 bug report
// ("coin lists show coins that aren't emitted or circulating").
//
// Note this is a *wallet-side* filter, not a chain-side one. A coin
// that is emitted on chain but the user never received still won't
// appear here — that's intentional: the user has nothing to do with
// it until they receive a tx for it, and any wallet address can
// receive any active coin type (coin type is a tx-level attribute,
// not an address-level one). When they first receive SKA-n, the next
// balance fetch will surface it.
//
// On balance-query failure this falls back to ActiveCoinTypes so the
// UI isn't stuck on VAR-only forever — better to show extra coins
// than to silently hide everything.
func (asset *Asset) DisplayableCoinTypes() []cointype.CoinType {
	out := []cointype.CoinType{cointype.CoinTypeVAR}
	balances, err := asset.GetWalletCoinBalances()
	if err != nil {
		log.Warnf("DisplayableCoinTypes: balance query failed, "+
			"falling back to ActiveCoinTypes: %v", err)
		return asset.ActiveCoinTypes()
	}
	// Sort SKA types numerically: see the comment in ActiveCoinTypes
	// for why this is mandatory (Go map iteration is randomised, so the
	// raw output of GetActiveSKATypes shuffles every call and would
	// reshuffle the UI on every redraw).
	skaTypes := asset.chainParams.GetActiveSKATypes()
	sort.Slice(skaTypes, func(i, j int) bool { return skaTypes[i] < skaTypes[j] })
	for _, ct := range skaTypes {
		bal, ok := balances[ct]
		if !ok {
			continue
		}
		// SKA atoms are exact in the *big.Int side; the int64 Total /
		// Unconfirmed fields are display approximations and would
		// undercount sub-atom-clamp amounts. Check both: any sign of
		// activity → show it.
		hasFunds := bal.SKATotal.Sign() > 0 ||
			bal.SKASpendable.Sign() > 0 ||
			bal.SKAUnconfirmed.Sign() > 0 ||
			bal.Total > 0 ||
			bal.Spendable > 0 ||
			bal.Unconfirmed > 0
		if hasFunds {
			out = append(out, ct)
		}
	}
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
		return padDecimalsMin2(dcrutil.Amount(atoms).String())
	}
	amt := cointype.NewSKAAmount(big.NewInt(atoms))
	return padDecimalsMin2(amt.ToDecimalString(cointype.AtomsPerSKACoin)) + " " + CoinSymbol(ct)
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
	if !ct.IsValid() {
		return FormatTxAmount(atomsInt, coinType)
	}
	if ct.IsVAR() {
		// VAR atom count fits in int64 by definition of the 21M*1e8 supply
		// cap, BUT callers that lift values through cointype.SKAAmount
		// (e.g. FeeRateBounds, SetFeeRateOverride error mapping) pass the
		// real atom count via atomsStr with atomsInt=0 as a sentinel —
		// before this branch existed the VAR fallback used atomsInt=0
		// directly and displayed "0.00 VAR" for every fee-rate bound or
		// error message, regardless of the actual atom count. Parse the
		// string first; fall back to atomsInt only if the string is
		// missing or unparseable.
		if atomsStr != "" {
			if atoms, ok := new(big.Int).SetString(atomsStr, 10); ok && atoms.IsInt64() {
				return FormatTxAmount(atoms.Int64(), coinType)
			}
		}
		return FormatTxAmount(atomsInt, coinType)
	}
	if atomsStr == "" {
		return FormatTxAmount(atomsInt, coinType)
	}
	atoms, ok := new(big.Int).SetString(atomsStr, 10)
	if !ok {
		return FormatTxAmount(atomsInt, coinType)
	}
	amt := cointype.NewSKAAmount(atoms)
	return padDecimalsMin2(amt.ToDecimalString(cointype.AtomsPerSKACoin)) + " " + CoinSymbol(ct)
}

// padDecimalsMin2 guarantees the input string (either "X" or "X.YZ…")
// shows at least two fractional digits. Cosmetic-only — for display
// uniformity ("4 VAR" → "4.00 VAR", "4.5 SKA1" → "4.50 SKA1"). Strings
// with ≥2 fractional digits pass through unchanged; the function does
// NOT truncate. Standalone helper instead of inlining: both
// FormatTxAmount and FormatTxAmountBig need identical behavior, and
// dcrutil.Amount.String() / SKAAmount.ToDecimalString() strip trailing
// zeros independently so we have to post-process either way.
//
// Caller passes the bare numeric portion (no unit suffix). For
// FormatTxAmount's VAR branch the dcrutil.Amount.String already
// emits "X.YZ VAR" — pass the whole thing through; the function
// preserves anything after a space.
func padDecimalsMin2(s string) string {
	// Locate the numeric vs unit boundary, if any. dcrutil.Amount
	// emits "0.5 VAR"; SKA path concatenates the unit separately so
	// this string is unit-less. Handle both shapes.
	num, unit := s, ""
	if idx := indexLastSpace(s); idx >= 0 {
		num, unit = s[:idx], s[idx:]
	}
	dot := indexByte(num, '.')
	switch {
	case dot < 0:
		num += ".00"
	case len(num)-dot-1 == 0:
		num += "00"
	case len(num)-dot-1 == 1:
		num += "0"
	}
	return num + unit
}

// indexLastSpace / indexByte are inline copies of strings.LastIndex /
// strings.IndexByte. The cointype file already avoids the strings
// import in hot UI render paths (see CoinSymbol comment below); keep
// the same convention here so this helper costs nothing extra.
func indexLastSpace(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ' ' {
			return i
		}
	}
	return -1
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
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
