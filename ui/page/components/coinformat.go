package components

import (
	"image/color"
	"regexp"
	"strings"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/unit"
	"github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/values"
)

const defaultScale = .7

var (
	doubleOrMoreDecimalPlaces = regexp.MustCompile(`(([0-9]{1,3},*)*\.)\d{2,}`)
	oneDecimalPlace           = regexp.MustCompile(`(([0-9]{1,3},*)*\.)\d`)
	noDecimal                 = regexp.MustCompile(`([0-9]{1,3},*)+`)
)

func formatBalance(gtx C, l *load.Load, amount string, mainTextSize unit.Sp, col color.NRGBA, isBoldText, displayUnitText bool) D {

	startIndex := 0
	stopIndex := 0

	if doubleOrMoreDecimalPlaces.MatchString(amount) {
		decimalIndex := strings.Index(amount, ".")
		startIndex = decimalIndex + 3
	} else if oneDecimalPlace.MatchString(amount) {
		decimalIndex := strings.Index(amount, ".")
		startIndex = decimalIndex + 2
	} else if noDecimal.MatchString(amount) {
		loc := noDecimal.FindStringIndex(amount)
		startIndex = loc[1] // start scaling from the end
	}

	// stopIndex marks the boundary between value digits and the " UNIT"
	// suffix. Upstream logic had its conditional inverted: it overrode
	// stopIndex with len(amount) ONLY when a unit was found, which made
	// the unit suffix invisible AND, when no unit was found, left
	// stopIndex at -1 so the guard below returned an empty layout for
	// the entire row. With getIndexUnit() now falling back to "last
	// space" we usually get a real index here, but for amounts that are
	// pure digits ("0", "0.00000000") we still need the "no unit found
	// → use whole string as value" fallback. Renaming + fixing the
	// condition makes the intent explicit.
	stopIndex = getIndexUnit(amount)
	noUnit := stopIndex == -1
	if noUnit {
		stopIndex = len(amount)
	}

	if startIndex > stopIndex || stopIndex <= 0 || stopIndex > len(amount) {
		return D{}
	}

	mainText, subText, unitText := amount[:startIndex], amount[startIndex:stopIndex], amount[stopIndex:]

	subTextSize := unit.Sp(float32(mainTextSize) * defaultScale)

	lblWidget := func(size unit.Sp, text string) D {
		lbl := l.Theme.Label(size, text)
		lbl.Color = col

		if isBoldText {
			lbl.Font.Weight = font.SemiBold
		}

		return lbl.Layout(gtx)
	}

	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Baseline}.Layout(gtx,
		layout.Rigid(func(_ C) D {
			return lblWidget(mainTextSize, mainText)
		}),
		layout.Rigid(func(_ C) D {
			return lblWidget(subTextSize, subText)
		}),
		layout.Rigid(func(_ C) D {
			if displayUnitText {
				return lblWidget(mainTextSize, unitText)
			}
			return lblWidget(subTextSize, unitText)
		}),
	)
}

func formatBalanceWithHidden(gtx C, l *load.Load, amount string, mainTextSize unit.Sp, textFont font.Weight, col color.NRGBA, isUSD bool) D {
	isBalanceHidden := l.AssetsManager.IsTotalBalanceVisible()
	txt := l.Theme.Label(mainTextSize, amount)
	if isUSD {
		if !l.AssetsManager.ExchangeRateFetchingEnabled() {
			txt.Text = "$ --"
		}
	}
	if isBalanceHidden {
		unit := ""
		if !isUSD {
			stopIndex := getIndexUnit(amount)
			isUnitExist := stopIndex == -1
			if isUnitExist {
				stopIndex = len(amount)
			}
			unit = amount[stopIndex:]
		}
		txt.Text = "****** " + unit
	}
	txt.Color = col
	txt.Font.Weight = textFont
	return txt.Layout(gtx)
}

// getIndexUnit returns the index of the leading space before the unit suffix
// inside a "<value> <unit>"-shaped amount string, or -1 if no unit can be
// found.
//
// Historically this only recognized the wallet-TYPE identifiers ("BTC",
// "DCR", "LTC") — i.e., the names of the upstream Cryptopower coin
// kinds. Monetarium prints its amounts with the COIN-LEVEL unit instead
// ("VAR" for the base asset, "SKA1"…"SKA255" for the per-token coins),
// so neither of the original tokens ever matched a real Skarb amount and
// formatBalance silently bailed via its stopIndex<=0 guard — that's why
// the Recent Transactions feed showed every SKA1 row with the asset
// label visible but no amount text at all.
//
// We now fall back to a generic "last space" rule: if no recognized
// hard-coded prefix matches but the string ends with " SOMETHING", treat
// that final whitespace as the value/unit boundary. Covers VAR, every
// SKA1…SKA255 token, USD, and any future coin we haven't enumerated.
func getIndexUnit(amount string) int {
	if strings.Contains(amount, string(utils.BTCWalletAsset)) {
		return strings.Index(amount, " "+string(utils.BTCWalletAsset))
	} else if strings.Contains(amount, string(utils.DCRWalletAsset)) {
		return strings.Index(amount, " "+string(utils.DCRWalletAsset))
	} else if strings.Contains(amount, string(utils.LTCWalletAsset)) {
		return strings.Index(amount, " "+string(utils.LTCWalletAsset))
	}
	// Generic suffix: "<digits>.<digits> <UNIT>" or "<digits> <UNIT>".
	// Use the LAST space so a future format like "1,234.56 SKA1" still
	// splits on the right whitespace.
	if idx := strings.LastIndex(amount, " "); idx > 0 {
		return idx
	}
	return -1
}

// LayoutBalance aligns the main and sub DCR balances horizontally, putting the sub
// balance at the baseline of the row.
func LayoutBalance(gtx layout.Context, l *load.Load, amount string) layout.Dimensions {
	return formatBalance(gtx, l, amount, values.TextSize20, l.Theme.Color.Text, false, false)
}

func LayoutBalanceWithUnit(gtx layout.Context, l *load.Load, amount string) layout.Dimensions {
	return formatBalance(gtx, l, amount, values.TextSize20, l.Theme.Color.PageNavText, false, true)
}

func LayoutBalanceWithUnitSize(gtx layout.Context, l *load.Load, amount string, mainTextSize unit.Sp) layout.Dimensions {
	return formatBalance(gtx, l, amount, mainTextSize, l.Theme.Color.PageNavText, false, true)
}

func LayoutBalanceWithUnitSizeBoldText(gtx layout.Context, l *load.Load, amount string, mainTextSize unit.Sp) layout.Dimensions {
	return formatBalance(gtx, l, amount, mainTextSize, l.Theme.Color.PageNavText, true, true)
}

func LayoutBalanceSize(gtx layout.Context, l *load.Load, amount string, mainTextSize unit.Sp) layout.Dimensions {
	return formatBalance(gtx, l, amount, mainTextSize, l.Theme.Color.Text, false, false)
}

func LayoutBalanceCustom(gtx layout.Context, l *load.Load, amount string, mainTextSize unit.Sp, isBold bool) layout.Dimensions {
	return formatBalance(gtx, l, amount, mainTextSize, l.Theme.Color.Text, isBold, false)
}

func LayoutBalanceColor(gtx layout.Context, l *load.Load, amount string, color color.NRGBA) layout.Dimensions {
	return formatBalance(gtx, l, amount, values.TextSize20, color, false, false)
}

func LayoutBalanceWithState(gtx layout.Context, l *load.Load, amount string) layout.Dimensions {
	return formatBalanceWithHidden(gtx, l, amount, values.TextSize16, font.Normal, l.Theme.Color.Text, false)
}

func LayoutBalanceColorWithState(gtx layout.Context, l *load.Load, amount string, color color.NRGBA) layout.Dimensions {
	return formatBalanceWithHidden(gtx, l, amount, values.TextSize20, font.Normal, color, false)
}

func LayoutBalanceWithStateSemiBold(gtx layout.Context, l *load.Load, amount string) layout.Dimensions {
	return formatBalanceWithHidden(gtx, l, amount, values.TextSize16, font.SemiBold, l.Theme.Color.Text, false)
}

func LayoutBalanceWithStateUSD(gtx layout.Context, l *load.Load, amount string) layout.Dimensions {
	return formatBalanceWithHidden(gtx, l, amount, values.TextSize16, font.Normal, l.Theme.Color.Text, true)
}

func LayoutBalanceColorWithStateUSD(gtx layout.Context, l *load.Load, amount string, color color.NRGBA) layout.Dimensions {
	return formatBalanceWithHidden(gtx, l, amount, values.TextSize16, font.Normal, color, true)
}
