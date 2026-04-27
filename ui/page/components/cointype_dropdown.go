package components

import (
	"gioui.org/font"
	"gioui.org/layout"

	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/values"
	"github.com/monetarium/monetarium-node/cointype"
)

// CoinTypeDropdown lets the user pick which Monetarium asset (VAR or one of
// the active SKAn types) a transaction is denominated in. The list of choices
// is read from chaincfg via Asset.ActiveCoinTypes() — the wallet does not
// invent coin types.
type CoinTypeDropdown struct {
	*load.Load

	dropdown        *cryptomaterial.DropDown
	wallet          *dcr.Asset
	coinTypes       []cointype.CoinType
	selected        cointype.CoinType
	changedCallback func(cointype.CoinType)
}

// NewCoinTypeDropdown returns an empty dropdown. Call Setup with a wallet to
// populate it with the active coin types for that wallet's chain.
func NewCoinTypeDropdown(l *load.Load) *CoinTypeDropdown {
	d := &CoinTypeDropdown{
		Load: l,
		dropdown: l.Theme.NewCommonDropDown(
			[]cryptomaterial.DropDownItem{},
			nil,
			cryptomaterial.MatchParent,
			values.CoinTypeDropdownGroup,
			false,
		),
		selected: cointype.CoinTypeVAR,
	}
	d.dropdown.BorderColor = &l.Theme.Color.Gray2
	return d
}

// Setup populates the dropdown from Asset.ActiveCoinTypes(). The first option
// (VAR) is selected by default. Pass an explicit `selected` value to override.
func (d *CoinTypeDropdown) Setup(w *dcr.Asset, selected ...cointype.CoinType) *CoinTypeDropdown {
	if w == nil {
		return d
	}
	d.wallet = w
	d.coinTypes = w.ActiveCoinTypes()

	items := make([]cryptomaterial.DropDownItem, 0, len(d.coinTypes))
	for _, ct := range d.coinTypes {
		items = append(items, cryptomaterial.DropDownItem{
			Text:      ct.String(),
			DisplayFn: d.itemLayout(ct),
		})
	}
	d.dropdown.SetItems(items)

	target := cointype.CoinTypeVAR
	if len(selected) > 0 {
		target = selected[0]
	}
	for _, ct := range d.coinTypes {
		if ct == target {
			d.selected = ct
			d.dropdown.SetSelectedValue(ct.String())
			return d
		}
	}
	// Selected coin type isn't active on this chain — fall back to VAR.
	d.selected = cointype.CoinTypeVAR
	d.dropdown.SetSelectedValue(cointype.CoinTypeVAR.String())
	return d
}

func (d *CoinTypeDropdown) itemLayout(ct cointype.CoinType) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		lbl := d.Theme.SemiBoldLabel(ct.String())
		lbl.MaxLines = 1
		return lbl.Layout(gtx)
	}
}

// Selected returns the currently selected coin type. Defaults to VAR if Setup
// hasn't been called.
func (d *CoinTypeDropdown) Selected() cointype.CoinType { return d.selected }

// SetChangedCallback wires a callback fired whenever the user picks a new coin
// type. The callback runs synchronously inside Handle, before the page's
// HandleUserInteractions sees the dropdown click.
func (d *CoinTypeDropdown) SetChangedCallback(cb func(cointype.CoinType)) *CoinTypeDropdown {
	d.changedCallback = cb
	return d
}

// Handle should be called from the page's HandleUserInteractions. It detects
// dropdown changes and fires the callback.
func (d *CoinTypeDropdown) Handle(gtx layout.Context) {
	if !d.dropdown.Changed(gtx) {
		return
	}
	picked := d.dropdown.Selected()
	for _, ct := range d.coinTypes {
		if ct.String() == picked && ct != d.selected {
			d.selected = ct
			if d.changedCallback != nil {
				d.changedCallback(ct)
			}
			return
		}
	}
}

// Layout renders the dropdown with an optional label above it.
func (d *CoinTypeDropdown) Layout(gtx layout.Context, title string) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if title == "" {
				return layout.Dimensions{}
			}
			lbl := d.Theme.H6(title)
			lbl.TextSize = values.TextSizeTransform(d.IsMobileView(), values.TextSize16)
			lbl.Font.Weight = font.SemiBold
			return layout.Inset{Bottom: values.MarginPadding4}.Layout(gtx, lbl.Layout)
		}),
		layout.Rigid(d.dropdown.Layout),
	)
}
