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

	"github.com/monetarium/skarb-wallet/app"
	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/values"
)

// OverviewPageID is the unique ID of the overview page.
const OverviewPageID = "Overview"

// OverviewPage shows per-CoinType balance cards for every loaded Monetarium wallet.
type OverviewPage struct {
	*app.MasterPage
	*load.Load

	showNavigationFunc func()
	onWalletClicked    func(sharedW.Asset)
	walletsList        layout.List

	// Per-wallet clickable, keyed by wallet ID. Refreshed on every Layout
	// call so wallets that come and go (added/removed) get fresh state, and
	// existing entries reuse their Clickable so click events aren't dropped.
	walletClicks map[int]*cryptomaterial.Clickable
}

// NewOverviewPage returns the overview page. onWalletClicked is invoked when
// the user clicks a wallet card; HomePage uses this to swap in the per-wallet
// detail subpage.
func NewOverviewPage(l *load.Load, showNavigationFunc func(), onWalletClicked ...func(sharedW.Asset)) *OverviewPage {
	op := &OverviewPage{
		MasterPage:         app.NewMasterPage(OverviewPageID),
		Load:               l,
		showNavigationFunc: showNavigationFunc,
		walletsList:        layout.List{Axis: layout.Vertical},
		walletClicks:       make(map[int]*cryptomaterial.Clickable),
	}
	if len(onWalletClicked) > 0 {
		op.onWalletClicked = onWalletClicked[0]
	}
	return op
}

// ID returns the page ID.
func (op *OverviewPage) ID() string { return OverviewPageID }

// OnNavigatedTo is a no-op; balances are read on every Layout call.
func (op *OverviewPage) OnNavigatedTo() {}

// OnNavigatedFrom is a no-op.
func (op *OverviewPage) OnNavigatedFrom() {}

// HandleUserInteractions checks each wallet card's Clickable and invokes the
// onWalletClicked callback when one fires. Empty-callback case (the
// HomePage-less call sites that pass only showNavigationFunc) is a no-op.
func (op *OverviewPage) HandleUserInteractions(gtx layout.Context) {
	if op.onWalletClicked == nil {
		return
	}
	for _, w := range op.AssetsManager.AllWallets() {
		click := op.walletClicks[w.GetWalletID()]
		if click != nil && click.Clicked(gtx) {
			op.onWalletClicked(w)
			return
		}
	}
}

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
	title := op.Theme.H4(values.String(values.StrnoValidWalletFound))
	title.Alignment = text.Middle
	subtitle := op.Theme.Body1(values.String(values.StrCreateOrRestoreToSeeBalances))
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
	title := op.Theme.H5(values.String(values.StrOverview))
	title.Font.Weight = font.Bold
	subtitle := op.Theme.Caption(fmt.Sprintf(
		"%s: %s · %d %s",
		values.String(values.StrNetwork),
		op.AssetsManager.NetType(),
		op.AssetsManager.LoadedWalletsCount(),
		values.String(values.StrWallets),
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
		return op.Theme.Body1(values.StringF(values.StrUnsupportedWallet, w.GetWalletName())).Layout(gtx)
	}

	walletTitle := op.Theme.H6(w.GetWalletName())
	walletTitle.Font.Weight = font.Bold
	walletMeta := op.Theme.Caption(fmt.Sprintf("ID #%d", w.GetWalletID()))

	balancesByCoin, err := dcrAsset.GetWalletCoinBalances()
	if err != nil {
		errLine := op.Theme.Body2(values.StringF(values.StrBalanceError, err))
		errLine.Color = op.Theme.Color.Danger
		return op.cardWrap(gtx,
			layout.Rigid(walletTitle.Layout),
			layout.Rigid(walletMeta.Layout),
			layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
			layout.Rigid(errLine.Layout),
		)
	}

	// Only enumerate coin types the wallet has actual activity for —
	// otherwise every Overview row would list every chain-active SKA-n
	// with "0" balance (bug #7).
	cts := dcrAsset.DisplayableCoinTypes()
	var coinChildren []layout.FlexChild
	coinChildren = append(coinChildren,
		layout.Rigid(walletTitle.Layout),
		layout.Rigid(walletMeta.Layout),
		layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
	)
	for _, ct := range cts {
		bal := balancesByCoin[ct]
		bal.CoinType = ct // ensure FormatCoinAmount picks the right branch
		row := op.layoutBalanceRow(dcr.CoinSymbol(ct), dcr.FormatCoinAmount(bal))
		coinChildren = append(coinChildren, layout.Rigid(row))
	}

	// Wrap the whole card in a Clickable so clicking anywhere on it opens
	// the wallet detail page. Reuse an existing Clickable per wallet ID so
	// gioui's event tracking stays continuous between Layout calls.
	id := w.GetWalletID()
	click, ok := op.walletClicks[id]
	if !ok || click == nil {
		click = op.Theme.NewClickable(true)
		op.walletClicks[id] = click
	}
	return cryptomaterial.LinearLayout{
		Width:       cryptomaterial.MatchParent,
		Height:      cryptomaterial.WrapContent,
		Background:  op.Theme.Color.Surface,
		Border:      cryptomaterial.Border{Radius: cryptomaterial.Radius(8)},
		Padding:     layout.UniformInset(unit.Dp(12)),
		Margin:      layout.Inset{Bottom: unit.Dp(8)},
		Orientation: layout.Vertical,
		Clickable:   click,
	}.Layout(gtx, coinChildren...)
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
