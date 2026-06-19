package components

import (
	"fmt"
	"strconv"
	"sync/atomic"

	"gioui.org/font"
	"gioui.org/layout"
	"github.com/monetarium/monetarium-node/cointype"
	"github.com/monetarium/skarb-wallet/app"
	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/values"
)

const WalletAndAccountSelectorID = "WalletAndAccountSelector"

type WalletDropdown struct {
	*load.Load
	selectedWallet        sharedW.Asset
	dropdown              *cryptomaterial.DropDown
	allWallets            []sharedW.Asset
	walletChangedCallback func(sharedW.Asset)
	walletIsValid         func(sharedW.Asset) bool
	isWatchOnlyEnabled    bool
	assetTypes            []utils.AssetType
	// pendingRefresh is set from the tx/block notification goroutine and
	// drained in Handle (UI thread). The callback rebuilds dropdowns and
	// touches editors, which must not run off the UI thread.
	pendingRefresh atomic.Bool

	// coinType controls which coin's balance each wallet row displays.
	// Defaults to VAR. SetCoinType(SKAn) switches the rows to that token's
	// per-wallet balance — on the send page's destination selector the rows
	// previously always showed VAR even when the user was sending SKA1.
	coinType cointype.CoinType
}

func NewWalletDropdown(l *load.Load, assetType ...utils.AssetType) *WalletDropdown {
	wd := &WalletDropdown{
		Load:     l,
		dropdown: l.Theme.NewCommonDropDown([]cryptomaterial.DropDownItem{}, nil, cryptomaterial.MatchParent, values.WalletsDropdownGroup, false),
	}
	wd.dropdown.BorderColor = &l.Theme.Color.Gray2
	wd.assetTypes = assetType
	return wd
}

func (d *WalletDropdown) Setup(args ...sharedW.Asset) *WalletDropdown {
	if len(args) > 0 {
		d.selectedWallet = args[0]
		if d.selectedWallet == nil {
			args = []sharedW.Asset{}
		}
	}
	d.allWallets = make([]sharedW.Asset, 0)
	wallets := d.AssetsManager.AssetWallets(d.assetTypes...)
	items := []cryptomaterial.DropDownItem{}
	if len(wallets) > 0 {
		isFirst := true
		for _, w := range wallets {
			if w.IsWatchingOnlyWallet() && !d.isWatchOnlyEnabled || d.walletIsValid != nil && !d.walletIsValid(w) {
				continue
			}
			item := cryptomaterial.DropDownItem{
				Text:      fmt.Sprint(w.GetWalletID()),
				Icon:      d.Theme.AssetIcon(w.GetAssetType()),
				DisplayFn: d.getWalletItemLayout(w),
			}
			items = append(items, item)
			d.allWallets = append(d.allWallets, w)
			if len(args) < 1 {
				if isFirst { // select the first valid wallet
					isFirst = false
					d.selectedWallet = w
				}
			} else {
				wallet := args[0]
				if w != nil && wallet.GetWalletID() == w.GetWalletID() {
					d.selectedWallet = w
				}
			}
		}
	}
	d.dropdown.SetItems(items)
	return d
}

// EnableWatchOnlyWallets enables selection of watchOnly wallets and their accounts.
func (d *WalletDropdown) EnableWatchOnlyWallets(isEnable bool) *WalletDropdown {
	d.isWatchOnlyEnabled = isEnable
	return d
}

func (d *WalletDropdown) SetSelectedWallet(wallet sharedW.Asset) {
	if wallet == nil {
		return
	}
	d.selectedWallet = wallet
	d.dropdown.SetSelectedValue(fmt.Sprint(wallet.GetWalletID()))
}

func (d *WalletDropdown) walletBalance(wal sharedW.Asset) (totalBalance, spendableBalance int64) {
	accountsResult, err := wal.GetAccountsRaw()
	if err != nil {
		log.Errorf("Error getting accounts: %s", err)
		return 0, 0
	}
	// VAR-only aggregation. account.Balance.Total / .Spendable are int64
	// VAR atoms (1e8/coin); per-wallet sum is bounded by VAR's
	// 21M × 1e8 = 2.1e15-atom supply cap (well under int64). The
	// dropdown shows ONLY the wallet's primary-coin (VAR) total — SKA
	// balances are not included here. If they were, the int64 sum
	// would need to move to big.Int because SKA atoms are 1e18/coin
	// and a single UTXO can exceed int64.
	var tBal, sBal int64
	for _, account := range accountsResult.Accounts {
		// If the wallet is watching-only, the spendable balance is zero.
		if wal.IsWatchingOnlyWallet() {
			account.Balance.Spendable = wal.ToAmount(0)
		}
		tBal += account.Balance.Total.ToInt()
		sBal += account.Balance.Spendable.ToInt()
	}
	return tBal, sBal
}

func (d *WalletDropdown) getWalletItemLayout(wallet sharedW.Asset) layout.Widget {
	// Compute the balance labels ONCE at build time (Setup/SetCoinType, UI
	// thread) — the old closure called walletBalance → GetAccountsRaw (a DB
	// scan) per wallet row on EVERY frame. The dropdown is rebuilt via Setup
	// on tx/block notifications, so the cached labels stay fresh.
	//
	// The displayed balance follows d.coinType: VAR uses the legacy int64
	// aggregation; an SKA coin reads the wallet-wide big.Int balance for that
	// token, so the send page's destination rows show the balance of the coin
	// actually being sent instead of always VAR.
	var totalLabel, spendableLabel string
	if d.coinType.IsSKA() {
		if dcrAsset, ok := wallet.(*dcr.Asset); ok {
			if balances, err := dcrAsset.GetWalletCoinBalances(); err == nil {
				bal := balances[d.coinType]
				bal.CoinType = d.coinType // ensure FormatCoinAmount picks the SKA branch
				totalLabel = dcr.FormatCoinAmount(bal)
				spendBal := bal
				spendBal.Total = bal.Spendable
				spendBal.SKATotal = bal.SKASpendable
				spendableLabel = dcr.FormatCoinAmount(spendBal)
			} else {
				log.Errorf("WalletDropdown: GetWalletCoinBalances(%s): %v", wallet.GetWalletName(), err)
			}
		}
	}
	if totalLabel == "" {
		totalBal, spendable := d.walletBalance(wallet)
		totalLabel = wallet.ToAmount(totalBal).String()
		spendableLabel = wallet.ToAmount(spendable).String()
	}
	if wallet.IsWatchingOnlyWallet() {
		// Zero spendable, rendered in the active coin's unit.
		spendableLabel = dcr.FormatTxAmount(0, uint8(d.coinType))
	}

	return func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Axis: layout.Horizontal, Spacing: layout.SpaceBetween}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						lbl := d.Theme.SemiBoldLabel(wallet.GetWalletName())
						lbl.MaxLines = 1
						lbl.TextSize = values.TextSizeTransform(d.IsMobileView(), values.TextSize16)
						return lbl.Layout(gtx)
					}),
					layout.Rigid(func(gtx C) D {
						return d.Theme.Label(values.TextSizeTransform(d.IsMobileView(), values.TextSize16), totalLabel).Layout(gtx)
					}),
				)
			}),
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Axis: layout.Horizontal, Spacing: layout.SpaceBetween}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						spendableText := d.Theme.Label(values.TextSize14, values.String(values.StrLabelSpendable))
						spendableText.Color = d.Theme.Color.GrayText2
						return spendableText.Layout(gtx)
					}),
					layout.Rigid(func(gtx C) D {
						return d.Theme.Label(values.TextSizeTransform(d.IsMobileView(), values.TextSize14), spendableLabel).Layout(gtx)
					}),
				)
			}),
		)
	}
}

// SetCoinType switches the per-wallet balance display to the given coin type
// and rebuilds the dropdown rows so the change is visible immediately. Pass
// cointype.CoinTypeVAR to restore the default.
func (d *WalletDropdown) SetCoinType(ct cointype.CoinType) {
	if d.coinType == ct {
		return
	}
	d.coinType = ct
	// Rebuild items in place, preserving the current selection (Setup keeps
	// d.selectedWallet when passed as the arg).
	d.Setup(d.selectedWallet)
}

func (d *WalletDropdown) WalletValidator(walletIsValid func(sharedW.Asset) bool) *WalletDropdown {
	d.walletIsValid = walletIsValid
	return d
}

func (d *WalletDropdown) getWalletByID(walletID int) sharedW.Asset {
	for _, wallet := range d.allWallets {
		if wallet.GetWalletID() == walletID {
			return wallet
		}
	}
	return nil
}

func (d *WalletDropdown) onChanged() {
	walletID, err := strconv.Atoi(d.dropdown.Selected())
	if err == nil {
		wallet := d.getWalletByID(walletID)
		if wallet != nil {
			d.selectedWallet = wallet
			if d.walletChangedCallback != nil {
				d.walletChangedCallback(wallet)
			}
		}
	}
}

func (d *WalletDropdown) SelectedWallet() sharedW.Asset {
	return d.selectedWallet
}

func (d *WalletDropdown) SetChangedCallback(callback func(sharedW.Asset)) *WalletDropdown {
	d.walletChangedCallback = callback
	return d
}

func (d *WalletDropdown) Handle(gtx C) {
	// Drain a deferred tx/block-notification refresh on the UI thread.
	if d.pendingRefresh.CompareAndSwap(true, false) {
		if d.walletChangedCallback != nil && d.selectedWallet != nil {
			d.walletChangedCallback(d.selectedWallet)
		}
	}
	if d.dropdown.Changed(gtx) {
		d.onChanged()
	}
}

func (d *WalletDropdown) Layout(gtx C, titleKey string) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			lbl := d.Theme.H6(values.String(titleKey))
			lbl.TextSize = values.TextSizeTransform(d.IsMobileView(), values.TextSize16)
			lbl.Font.Weight = font.SemiBold
			return layout.Inset{Bottom: values.MarginPadding4}.Layout(gtx, lbl.Layout)
		}),
		layout.Rigid(d.dropdown.Layout),
	)
}

// ListenForTxNotifications listens for transaction and block updates and
// updates the selector modal, if the modal is open at the time of the update.
// The tx update listener MUST be unregistered using ws.StopTxNtfnListener()
// when the page using this WalletAndAccountSelector widget is exited.
func (d *WalletDropdown) ListenForTxNotifications(window app.WindowNavigator) {
	txAndBlockNotificationListener := &sharedW.TxAndBlockNotificationListener{
		OnTransaction: func(_ int, _ *sharedW.Transaction) {
			// Defer the callback to the UI thread: it rebuilds dropdown items
			// and (on the send page) touches recipient editors, which races
			// with Layout if done here on the notification goroutine.
			d.pendingRefresh.Store(true)
			window.Reload()
		},
		OnBlockAttached: func(_ int, _ int32) {
			d.pendingRefresh.Store(true)
			window.Reload()
		},
	}
	if d.selectedWallet == nil {
		return
	}
	err := d.selectedWallet.AddTxAndBlockNotificationListener(txAndBlockNotificationListener, WalletAndAccountSelectorID)
	if err != nil {
		log.Errorf("WalletAndAccountSelector.ListenForTxNotifications error: %v", err)
	}
}

func (d *WalletDropdown) StopTxNtfnListener() {
	if d.selectedWallet != nil {
		d.selectedWallet.RemoveTxAndBlockNotificationListener(WalletAndAccountSelectorID)
	}
}
