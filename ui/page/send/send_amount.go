package send

import (
	"fmt"
	"math"
	"math/big"
	"strconv"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"
	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	libUtil "github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/utils"
	"github.com/monetarium/skarb-wallet/ui/values"
	"github.com/monetarium/monetarium-node/cointype"
	"github.com/monetarium/monetarium-node/dcrutil"
)

// unitLabel returns the user-facing coin symbol ("VAR" or "SKA1"…"SKA255")
// for the selected coin type. The legacy hint string used
// fmt.Sprintf("Сума (%s)", string(assetType)) which baked in the WALLET
// type ("DCR") regardless of the selected coin. dcr.CoinSymbol already
// covers VAR — keep this one-liner so callers don't have to import the
// dcr package for a tiny dispatch, and so future translations can hook
// in here without touching every callsite.
func unitLabel(ct cointype.CoinType) string {
	return dcr.CoinSymbol(ct)
}

type sendAmount struct {
	theme *cryptomaterial.Theme

	assetType       libUtil.AssetType
	amountEditor    cryptomaterial.Editor
	usdAmountEditor cryptomaterial.Editor

	// coinType is the in-progress tx's coin type (VAR / SKA-n). It controls
	// the float→atoms scale: VAR uses 1e8 (dcrutil.NewAmount), SKA uses 1e18.
	// Defaults to VAR; updated via setCoinType when the user picks a
	// different asset in the page-level CoinType dropdown.
	coinType cointype.CoinType

	SendMax               bool
	sendMaxChangeEvent    bool
	usdSendMaxChangeEvent bool
	amountChanged         func()

	amountErrorText string

	exchangeRate float64
}

func newSendAmount(theme *cryptomaterial.Theme, assetType libUtil.AssetType) *sendAmount {
	sa := &sendAmount{
		theme:        theme,
		exchangeRate: -1,
		assetType:    assetType,
		coinType:     cointype.CoinTypeVAR,
	}

	// Initial placeholder defaults to VAR; setCoinType refreshes the hint
	// when the user picks a SKAn token via the page-level dropdown.
	hit := fmt.Sprintf("%s (%s)", values.String(values.StrAmount), unitLabel(sa.coinType))
	sa.amountEditor = theme.Editor(new(widget.Editor), hit)
	sa.amountEditor.Editor.SetText("")
	sa.amountEditor.HasCustomButton = true
	sa.amountEditor.Editor.SingleLine = true
	sa.amountEditor.IsTitleLabel = false
	sa.amountEditor.AlwaysShowHint()

	sa.amountEditor.CustomButton.Inset = layout.UniformInset(values.MarginPadding2)
	sa.amountEditor.CustomButton.Text = values.String(values.StrMax)
	sa.amountEditor.CustomButton.CornerRadius = values.MarginPadding0
	sa.amountEditor.CustomButton.DisableHoverColor()

	sa.usdAmountEditor = theme.Editor(new(widget.Editor), values.String(values.StrAmount)+" (USD)")
	sa.usdAmountEditor.Editor.SetText("")
	sa.usdAmountEditor.HasCustomButton = true
	sa.usdAmountEditor.Editor.SingleLine = true
	sa.usdAmountEditor.IsTitleLabel = false
	sa.usdAmountEditor.AlwaysShowHint()

	sa.usdAmountEditor.CustomButton.Inset = layout.UniformInset(values.MarginPadding2)
	sa.usdAmountEditor.CustomButton.Text = values.String(values.StrMax)
	sa.usdAmountEditor.CustomButton.CornerRadius = values.MarginPadding0
	sa.usdAmountEditor.CustomButton.DisableHoverColor()

	sa.styleWidgets()

	return sa
}

// styleWidgets sets the appropriate colors for the amount widgets.
func (sa *sendAmount) styleWidgets() {
	sa.amountEditor.CustomButton.Background = sa.theme.Color.Gray1
	sa.amountEditor.CustomButton.Color = sa.theme.Color.Surface
	sa.amountEditor.EditorStyle.Color = sa.theme.Color.Text

	sa.usdAmountEditor.CustomButton.Background = sa.theme.Color.Gray1
	sa.usdAmountEditor.CustomButton.Color = sa.theme.Color.Surface
	sa.usdAmountEditor.EditorStyle.Color = sa.theme.Color.Text
}

func (sa *sendAmount) setExchangeRate(exchangeRate float64) {
	sa.exchangeRate = exchangeRate
	sa.validateAmount() // convert dcr input to usd
}

func (sa *sendAmount) setAmount(amount int64) {
	// int64 callers route through the lossless big.Int path so SKA values
	// never see a float64 round-trip, even when they fit in int64. A 5 SKA
	// value (5e18 atoms < MaxInt64) already loses ~3 decimal digits when
	// rendered via "%.18f" because float64's 53-bit mantissa cannot
	// represent every 1-atom step at that magnitude — and the editor text
	// is then re-parsed losslessly by validAmountBig, baking the
	// truncated value into the broadcast atoms.
	sa.setAmountBig(big.NewInt(amount))
}

// setAmountBig writes the editor with the lossless decimal representation
// of `atoms` — the SendMax precision-safe entry point. SKA uses
// SKAAmount.ToDecimalString (full 18-decimal precision, no float in the
// path); VAR uses dcrutil.Amount.ToCoin since 21M VAR * 1e8 = 2.1e15 atoms
// fits int64 with room to spare and float64 is exact there. The USD
// mirror, when shown, is still float64-shaped — USD has 2 decimals and is
// derived from a float exchange rate, so a few-atom drift on the displayed
// USD value is harmless and not part of the broadcast amount.
//
// Nil or non-positive atoms clear both editors. Always sets
// sendMaxChangeEvent so the handle() path knows to swallow the synthetic
// Changed event we're about to emit by writing the text.
func (sa *sendAmount) setAmountBig(atoms *big.Int) {
	sa.sendMaxChangeEvent = sa.SendMax

	if atoms == nil || atoms.Sign() <= 0 {
		sa.amountEditor.Editor.SetText("")
		sa.usdAmountEditor.Editor.SetText("")
		return
	}

	var displayText string
	var coinValue float64
	if sa.coinType.IsSKA() {
		displayText = cointype.NewSKAAmount(atoms).ToDecimalString(cointype.AtomsPerSKACoin)
		// USD display path requires a float — we accept the float drift
		// here because it never feeds back into the editor text the
		// authoring code parses. Skip USD entirely when atoms overflow
		// int64 (a >9.22 SKA SendMax) since the float conversion would
		// be meaningless.
		if atoms.IsInt64() {
			const skaAtomsPerCoin = 1e18
			coinValue = float64(atoms.Int64()) / skaAtomsPerCoin
		}
	} else {
		// VAR fits int64 by construction (21M * 1e8 = 2.1e15 < float64's
		// 2^53 ≈ 9.0e15 mantissa exact-integer range), so ToCoin's float
		// is lossless. Keep the legacy "%.8f" formatting so the editor
		// looks identical to what the user typed/saw before.
		amt := dcrutil.Amount(atoms.Int64())
		coinValue = amt.ToCoin()
		displayText = fmt.Sprintf("%.8f", coinValue)
	}
	sa.amountEditor.Editor.SetText(displayText)

	// No USD for SKA: applying the VAR-USD exchange rate to a SKA coin value is
	// a category error (same reason validateAmount suppresses it for SKA). A
	// SendMax click would otherwise leave a meaningless VAR-rate dollar figure
	// stuck next to the SKA amount, since the synthetic Changed event is
	// swallowed and validateAmount never re-runs to clear it.
	if sa.exchangeRate != -1 && atoms.IsInt64() && !sa.coinType.IsSKA() {
		usdAmount := utils.CryptoToUSD(sa.exchangeRate, coinValue)
		sa.usdSendMaxChangeEvent = true
		sa.usdAmountEditor.Editor.SetText(fmt.Sprintf("%.2f", usdAmount))
	} else if sa.coinType.IsSKA() {
		sa.usdAmountEditor.Editor.SetText("")
	}
}

func (sa *sendAmount) amountIsValid() bool {
	txt := sa.amountEditor.Editor.Text()
	amount, err := strconv.ParseFloat(txt, 64)
	if (err == nil && amount <= 0) || (err != nil && sa.amountErrorText == "" && len(txt) > 0) {
		// do not overwrite existing errors
		sa.amountErrorText = values.String(values.StrInvalidAmount)
	}
	amountEditorErrors := sa.amountErrorText == ""
	return err == nil && amountEditorErrors || sa.SendMax
}

func (sa *sendAmount) validAmount() (int64, bool, error) {
	atoms, _, sendMax, err := sa.validAmountBig()
	return atoms, sendMax, err
}

// validAmountBig is the lossless companion: returns the int64 atoms (clamped
// for SKA overflow), the big.Int atoms as a decimal string (empty for VAR
// and for SKA amounts that fit in int64), and the SendMax flag. UI callers
// that author SKA sends > ~9.22 SKA must use the big-string and pass it to
// AddSendDestinationBig.
//
// The parsing goes through ParseAmountToAtomsBig (string → big.Int)
// rather than ParseFloat → float64 → 1e18 multiply, because float64's
// 53-bit mantissa silently drops the last ~3 decimal digits of an
// 18-decimal SKA amount. With the float path "1.234567890123456789" was
// truncating to "1.234567890123456700"-ish before being broadcast, which
// is bug #3 of the v1 bug report ("sent amount doesn't match entered").
func (sa *sendAmount) validAmountBig() (int64, string, bool, error) {
	if sa.SendMax {
		return 0, "", sa.SendMax, nil
	}

	text := sa.amountEditor.Editor.Text()
	atomsBig, err := dcr.ParseAmountToAtomsBig(text, sa.coinType)
	if err != nil {
		return -1, "", sa.SendMax, err
	}
	if atomsBig.Sign() <= 0 {
		return -1, "", sa.SendMax, fmt.Errorf("amount %q must be greater than zero", text)
	}

	// VAR fits in int64 by construction (21M coins × 1e8 atoms = 2.1e15
	// < MaxInt64 = 9.22e18). Return the exact value via the int64
	// channel; bigStr stays empty since the legacy path is sufficient.
	if !sa.coinType.IsSKA() {
		if !atomsBig.IsInt64() {
			return -1, "", sa.SendMax, fmt.Errorf("VAR amount overflows int64: %s atoms", atomsBig.String())
		}
		return atomsBig.Int64(), "", sa.SendMax, nil
	}

	// SKA: int64 atoms are advisory only above ~9.22 SKA — we clamp so
	// balance-after-send arithmetic (which is still int64-shaped in
	// older code paths) doesn't crash, but the authoring path consumes
	// the big-string and broadcasts the exact atoms the user typed.
	var atoms int64 = math.MaxInt64
	if atomsBig.IsInt64() {
		atoms = atomsBig.Int64()
	}
	bigStr := ""
	if !atomsBig.IsInt64() {
		bigStr = atomsBig.String()
	}
	return atoms, bigStr, sa.SendMax, nil
}

func (sa *sendAmount) validateAmount() {
	sa.amountErrorText = ""
	if sa.inputsNotEmpty(sa.amountEditor.Editor) {
		// Validate shape via ParseAmountToAtomsBig (lossless): handles
		// 18-decimal SKA inputs that strconv.ParseFloat would silently
		// truncate at the 53-bit float64 mantissa. For VAR this still
		// catches non-numeric input the same way.
		atomsBig, err := dcr.ParseAmountToAtomsBig(sa.amountEditor.Editor.Text(), sa.coinType)
		if err != nil {
			// empty usd input
			sa.usdAmountEditor.Editor.SetText("")
			sa.amountErrorText = values.String(values.StrInvalidAmount)
			return
		}
		// USD live-preview only fires when an exchange rate is set, AND
		// only for VAR — sa.exchangeRate comes from the wallet's primary
		// asset type (Decred-fork, == VAR-USD). Applying a VAR-USD rate
		// to a SKA amount would be a category error: SKA tokens have no
		// USD pairing in v1. Hide the preview for SKA instead of
		// emitting a misleading number.
		if sa.exchangeRate != -1 && !sa.coinType.IsSKA() && atomsBig.IsInt64() {
			coinValue := dcrutil.Amount(atomsBig.Int64()).ToCoin()
			usdAmount := utils.CryptoToUSD(sa.exchangeRate, coinValue)
			sa.usdAmountEditor.Editor.SetText(fmt.Sprintf("%.2f", usdAmount)) // 2 decimal places
		} else if sa.coinType.IsSKA() {
			sa.usdAmountEditor.Editor.SetText("")
		}

		return
	}

	// empty usd input since this is empty
	sa.usdAmountEditor.Editor.SetText("")
}

// validateUSDAmount is called when usd text changes
func (sa *sendAmount) validateUSDAmount() bool {
	sa.amountErrorText = ""
	if sa.inputsNotEmpty(sa.usdAmountEditor.Editor) {
		usdAmount, err := strconv.ParseFloat(sa.usdAmountEditor.Editor.Text(), 64)
		if err != nil {
			// empty dcr input
			sa.amountEditor.Editor.SetText("")
			sa.amountErrorText = values.String(values.StrInvalidAmount)
			return false
		}

		if sa.exchangeRate != -1 {
			dcrAmount := utils.USDToDCR(sa.exchangeRate, usdAmount)
			sa.amountEditor.Editor.SetText(fmt.Sprintf("%.8f", dcrAmount)) // 8 decimal places
		}

		return true
	}

	// empty dcr input since this is empty
	sa.amountEditor.Editor.SetText("")
	return false
}

func (sa *sendAmount) inputsNotEmpty(editors ...*widget.Editor) bool {
	for _, e := range editors {
		if e.Text() == "" {
			return false
		}
	}
	return true
}

func (sa *sendAmount) setError(err string) {
	sa.amountErrorText = values.TranslateErr(err)
}

func (sa *sendAmount) resetFields() {
	sa.SendMax = false

	sa.clearAmount()
}

func (sa *sendAmount) clearAmount() {
	sa.amountErrorText = ""
	sa.amountEditor.Editor.SetText("")
	sa.usdAmountEditor.Editor.SetText("")
}

func (sa *sendAmount) handle(gtx C) {
	sa.amountEditor.SetError(sa.amountErrorText)

	if sa.amountErrorText != "" {
		sa.amountEditor.LineColor = sa.theme.Color.Danger
		sa.usdAmountEditor.LineColor = sa.theme.Color.Danger
	} else {
		sa.amountEditor.LineColor = sa.theme.Color.Gray2
		sa.usdAmountEditor.LineColor = sa.theme.Color.Gray2
	}

	if sa.SendMax {
		sa.amountEditor.CustomButton.Background = sa.theme.Color.Primary
		sa.usdAmountEditor.CustomButton.Background = sa.theme.Color.Primary
	} else if len(sa.amountEditor.Editor.Text()) < 1 || !sa.SendMax {
		sa.amountEditor.CustomButton.Background = sa.theme.Color.Gray1
		sa.usdAmountEditor.CustomButton.Background = sa.theme.Color.Gray1
	}

	if gtx.Source.Focused(sa.amountEditor.Editor) {
		if sa.amountEditor.Changed() {
			if sa.sendMaxChangeEvent {
				sa.sendMaxChangeEvent = false
			} else {
				sa.SendMax = false
				sa.validateAmount()
				sa.amountChanged()
			}

		}
	}

	if gtx.Source.Focused(sa.usdAmountEditor.Editor) {
		if sa.usdAmountEditor.Changed() {
			if sa.usdSendMaxChangeEvent {
				sa.usdSendMaxChangeEvent = false
			} else {
				sa.SendMax = false
				sa.validateUSDAmount()
				sa.amountChanged()
			}
		}
	}
}

func (sa *sendAmount) IsMaxClicked(gtx C) bool {
	switch {
	case sa.amountEditor.CustomButton.Clicked(gtx):
		gtx.Execute(key.FocusCmd{Tag: sa.amountEditor.Editor})
	case sa.usdAmountEditor.CustomButton.Clicked(gtx):
		gtx.Execute(key.FocusCmd{Tag: sa.usdAmountEditor.Editor})
	default:
		return false
	}
	return true
}

func (sa *sendAmount) setAssetType(assetType libUtil.AssetType) {
	sa.assetType = assetType
}

// setCoinType updates the coin type used by validAmount to convert the
// user-typed float into integer atoms, and refreshes the editor placeholder
// hint so it matches the selected coin's symbol. Called by
// recipient.setCoinType, which is in turn driven by the page's CoinType
// dropdown.
func (sa *sendAmount) setCoinType(ct cointype.CoinType) {
	sa.coinType = ct
	sa.amountEditor.Hint = fmt.Sprintf("%s (%s)", values.String(values.StrAmount), unitLabel(ct))
}
