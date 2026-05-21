package send

import (
	"fmt"
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

	hit := fmt.Sprintf("%s (%s)", values.String(values.StrAmount), string(assetType))
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
	// TODO: this workaround ignores the change events from the
	// amount input to avoid construct tx cycle.
	sa.sendMaxChangeEvent = sa.SendMax

	// Convert int64 atoms back to a coin-shaped float for display. VAR has
	// 1e8 atoms/coin (dcrutil.Amount.ToCoin), SKA has 1e18 — pick the right
	// divisor for the current coin type. The editor format string also
	// widens for SKA: 8 decimals would silently truncate sub-millicoin SKA
	// values, so we use 18 (the full atoms/coin resolution) and let the
	// editor display whatever non-zero suffix the user actually has.
	if sa.coinType.IsSKA() {
		const skaAtomsPerCoin = 1e18
		amountSet := float64(amount) / skaAtomsPerCoin
		sa.amountEditor.Editor.SetText(fmt.Sprintf("%.18f", amountSet))
		if sa.exchangeRate != -1 {
			usdAmount := utils.CryptoToUSD(sa.exchangeRate, amountSet)
			sa.usdSendMaxChangeEvent = true
			sa.usdAmountEditor.Editor.SetText(fmt.Sprintf("%.2f", usdAmount))
		}
		return
	}

	amountSet := dcrutil.Amount(amount).ToCoin()
	sa.amountEditor.Editor.SetText(fmt.Sprintf("%.8f", amountSet))

	if sa.exchangeRate != -1 {
		usdAmount := utils.CryptoToUSD(sa.exchangeRate, amountSet)
		sa.usdSendMaxChangeEvent = true
		sa.usdAmountEditor.Editor.SetText(fmt.Sprintf("%.2f", usdAmount))
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
	if sa.SendMax {
		return 0, sa.SendMax, nil
	}

	amount, err := strconv.ParseFloat(sa.amountEditor.Editor.Text(), 64)
	if err != nil {
		return -1, sa.SendMax, err
	}

	// AmountAtomForCoinType picks the right atoms/coin scale: 1e8 for VAR,
	// 1e18 for SKA. For SKA, the int64 result caps per-output sends at
	// ~9.22 SKA in phase 1 (see AmountAtomForCoinType for the rationale).
	atoms := dcr.AmountAtomForCoinType(amount, sa.coinType)
	if atoms < 0 {
		return -1, sa.SendMax, fmt.Errorf("amount %v is out of range for %s", amount, sa.coinType)
	}
	return atoms, sa.SendMax, nil
}

func (sa *sendAmount) validateAmount() {
	sa.amountErrorText = ""
	if sa.inputsNotEmpty(sa.amountEditor.Editor) {
		amount, err := strconv.ParseFloat(sa.amountEditor.Editor.Text(), 64)
		if err != nil {
			// empty usd input
			sa.usdAmountEditor.Editor.SetText("")
			sa.amountErrorText = values.String(values.StrInvalidAmount)
			return
		}
		if sa.exchangeRate != -1 {
			usdAmount := utils.CryptoToUSD(sa.exchangeRate, amount)
			sa.usdAmountEditor.Editor.SetText(fmt.Sprintf("%.2f", usdAmount)) // 2 decimal places
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
// user-typed float into integer atoms. Called by recipient.setCoinType, which
// is in turn driven by the page's CoinType dropdown.
func (sa *sendAmount) setCoinType(ct cointype.CoinType) {
	sa.coinType = ct
}
