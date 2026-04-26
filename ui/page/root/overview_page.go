// Phase-2 OverviewPage rendering per-coin balances for every Monetarium wallet.
//
// The previous version (Cryptopower master) wired in FX rate cards, instantswap
// orders, and Decred staking — all gone for this fork. The new design lists each
// loaded wallet and, under it, a card per active CoinType (VAR + active SKAn).
package root

import (
	"fmt"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/text"
	"gioui.org/unit"

	"github.com/monetarium/monetarium-cryptopower/app"
	"github.com/monetarium/monetarium-cryptopower/libwallet/assets/dcr"
	sharedW "github.com/monetarium/monetarium-cryptopower/libwallet/assets/wallet"
	"github.com/monetarium/monetarium-cryptopower/ui/cryptomaterial"
	"github.com/monetarium/monetarium-cryptopower/ui/load"
)

// OverviewPageID is the unique ID of the overview page.
const OverviewPageID = "Overview"

// OverviewPage shows per-CoinType balance cards for every loaded Monetarium wallet.
type OverviewPage struct {
	*app.MasterPage
	*load.Load

	showNavigationFunc func()
	walletsList        layout.List
}

// NewOverviewPage returns the overview page.
func NewOverviewPage(l *load.Load, showNavigationFunc func()) *OverviewPage {
	return &OverviewPage{
		MasterPage:         app.NewMasterPage(OverviewPageID),
		Load:               l,
		showNavigationFunc: showNavigationFunc,
		walletsList:        layout.List{Axis: layout.Vertical},
	}
}

// ID returns the page ID.
func (op *OverviewPage) ID() string { return OverviewPageID }

// OnNavigatedTo is a no-op; balances are read on every Layout call.
func (op *OverviewPage) OnNavigatedTo() {}

// OnNavigatedFrom is a no-op.
func (op *OverviewPage) OnNavigatedFrom() {}

// HandleUserInteractions has nothing to do for now — the page is read-only.
func (op *OverviewPage) HandleUserInteractions(_ layout.Context) {}

// ListenForNewTx is kept as a stub so existing call-sites in HomePage compile.
// Re-implement with a tx notification listener once HomePage is rewired.
func (op *OverviewPage) ListenForNewTx() {}

// Layout renders the page.
func (op *OverviewPage) Layout(gtx layout.Context) layout.Dimensions {
	wallets := op.AssetsManager.AllWallets()
	if len(wallets) == 0 {
		return op.layoutEmpty(gtx)
	}

	return cryptomaterial.LinearLayout{
		Width:       cryptomaterial.MatchParent,
		Height:      cryptomaterial.MatchParent,
		Padding:     layout.UniformInset(unit.Dp(16)),
		Orientation: layout.Vertical,
	}.Layout(gtx,
		layout.Rigid(op.layoutHeader),
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return op.walletsList.Layout(gtx, len(wallets), func(gtx layout.Context, i int) layout.Dimensions {
				return op.layoutWalletCard(gtx, wallets[i])
			})
		}),
	)
}

func (op *OverviewPage) layoutEmpty(gtx layout.Context) layout.Dimensions {
	title := op.Theme.H4("No wallets yet")
	title.Alignment = text.Middle
	subtitle := op.Theme.Body1("Create or restore a Monetarium wallet to see VAR and SKA balances here.")
	subtitle.Alignment = text.Middle

	return cryptomaterial.LinearLayout{
		Width:       cryptomaterial.MatchParent,
		Height:      cryptomaterial.MatchParent,
		Orientation: layout.Vertical,
		Alignment:   layout.Middle,
		Direction:   layout.Center,
	}.Layout(gtx,
		layout.Rigid(title.Layout),
		layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
		layout.Rigid(subtitle.Layout),
	)
}

func (op *OverviewPage) layoutHeader(gtx layout.Context) layout.Dimensions {
	title := op.Theme.H5("Overview")
	title.Font.Weight = font.Bold
	subtitle := op.Theme.Caption(fmt.Sprintf(
		"Network: %s · %d wallet(s)",
		op.AssetsManager.NetType(),
		op.AssetsManager.LoadedWalletsCount(),
	))
	return cryptomaterial.LinearLayout{
		Width:       cryptomaterial.MatchParent,
		Height:      cryptomaterial.WrapContent,
		Orientation: layout.Vertical,
	}.Layout(gtx,
		layout.Rigid(title.Layout),
		layout.Rigid(subtitle.Layout),
	)
}

func (op *OverviewPage) layoutWalletCard(gtx layout.Context, w sharedW.Asset) layout.Dimensions {
	dcrAsset, ok := w.(*dcr.Asset)
	if !ok {
		// Should not happen in v1 — Monetarium wallets are always *dcr.Asset.
		return op.Theme.Body1(fmt.Sprintf("(unsupported wallet: %s)", w.GetWalletName())).Layout(gtx)
	}

	walletTitle := op.Theme.H6(w.GetWalletName())
	walletTitle.Font.Weight = font.Bold
	walletMeta := op.Theme.Caption(fmt.Sprintf("ID #%d", w.GetWalletID()))

	balancesByCoin, err := dcrAsset.GetWalletCoinBalances()
	if err != nil {
		errLine := op.Theme.Body2(fmt.Sprintf("balance error: %v", err))
		errLine.Color = op.Theme.Color.Danger
		return op.cardWrap(gtx,
			layout.Rigid(walletTitle.Layout),
			layout.Rigid(walletMeta.Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
			layout.Rigid(errLine.Layout),
		)
	}

	cts := dcrAsset.ActiveCoinTypes()
	var coinChildren []layout.FlexChild
	coinChildren = append(coinChildren,
		layout.Rigid(walletTitle.Layout),
		layout.Rigid(walletMeta.Layout),
		layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
	)
	for _, ct := range cts {
		bal := balancesByCoin[ct]
		bal.CoinType = ct // ensure FormatCoinAmount picks the right branch
		row := op.layoutBalanceRow(ct.String(), dcr.FormatCoinAmount(bal))
		coinChildren = append(coinChildren, layout.Rigid(row))
	}

	return op.cardWrap(gtx, coinChildren...)
}

func (op *OverviewPage) layoutBalanceRow(coin, amount string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		coinLbl := op.Theme.Body1(coin)
		coinLbl.Font.Weight = font.SemiBold
		amountLbl := op.Theme.Body1(amount)
		return cryptomaterial.LinearLayout{
			Width:       cryptomaterial.MatchParent,
			Height:      cryptomaterial.WrapContent,
			Orientation: layout.Horizontal,
			Padding:     layout.Inset{Top: unit.Dp(2), Bottom: unit.Dp(2)},
		}.Layout(gtx,
			layout.Rigid(coinLbl.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.E.Layout(gtx, amountLbl.Layout)
			}),
		)
	}
}

func (op *OverviewPage) cardWrap(gtx layout.Context, children ...layout.FlexChild) layout.Dimensions {
	return cryptomaterial.LinearLayout{
		Width:       cryptomaterial.MatchParent,
		Height:      cryptomaterial.WrapContent,
		Orientation: layout.Vertical,
		Background:  op.Theme.Color.Surface,
		Border:      cryptomaterial.Border{Radius: cryptomaterial.Radius(8)},
		Margin:      layout.Inset{Bottom: unit.Dp(10)},
		Padding:     layout.UniformInset(unit.Dp(14)),
	}.Layout(gtx, children...)
}
