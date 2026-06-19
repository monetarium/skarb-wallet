package components

import (
	"gioui.org/font"
	"gioui.org/layout"

	"github.com/monetarium/monetarium-node/cointype"
	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/values"
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

// Setup populates the dropdown from Asset.ActiveCoinTypes(). On the very
// first call the default selection is VAR; on subsequent calls (e.g. the
// wallet-dropdown re-Setup we fire on every new block, see
// WalletDropdown.ListenForTxNotifications) the *current* user selection
// is preserved. Pass an explicit `selected` value to override either
// default.
//
// Preserving the current selection across rebuilds matters because
// otherwise an SKA1 send-in-progress flips back to VAR every time a new
// block arrives — the user types an amount, ~10s pass, OnBlockAttached
// fires, walletChangedCallback rebuilds this dropdown, target=VAR
// silently wins, and the in-flight tx is now denominated in the wrong
// coin. That's bug #2 in the v1 bug report ("SKA randomly switches to
// VAR while typing").
func (d *CoinTypeDropdown) Setup(w *dcr.Asset, selected ...cointype.CoinType) *CoinTypeDropdown {
	if w == nil {
		return d
	}
	d.wallet = w
	// VisibleCoinTypes = every coin emitted on chain minus the user's
	// settings-filter. Unlike the old balance-based DisplayableCoinTypes
	// this lists zero-balance coins too, so a freshly emitted SKA-n can be
	// selected (e.g. to receive it) the moment the chain reaches its
	// emission height; unwanted coins are hidden via wallet settings.
	d.coinTypes = w.VisibleCoinTypes()

	items := make([]cryptomaterial.DropDownItem, 0, len(d.coinTypes))
	for _, ct := range d.coinTypes {
		items = append(items, cryptomaterial.DropDownItem{
			Text:      dcr.CoinSymbol(ct),
			DisplayFn: d.itemLayout(ct),
		})
	}
	d.dropdown.SetItems(items)

	// target priority: explicit arg > preserved selection > VAR.
	target := d.selected
	if len(selected) > 0 {
		target = selected[0]
	}
	for _, ct := range d.coinTypes {
		if ct == target {
			d.selected = ct
			d.dropdown.SetSelectedValue(dcr.CoinSymbol(ct))
			return d
		}
	}
	// Target coin type isn't active on this chain — fall back to VAR.
	// This is the only path that should ever silently change the
	// user-visible selection; everything else preserves d.selected.
	d.selected = cointype.CoinTypeVAR
	d.dropdown.SetSelectedValue(dcr.CoinSymbol(cointype.CoinTypeVAR))
	return d
}

func (d *CoinTypeDropdown) itemLayout(ct cointype.CoinType) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		lbl := d.Theme.SemiBoldLabel(dcr.CoinSymbol(ct))
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
		if dcr.CoinSymbol(ct) == picked && ct != d.selected {
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
