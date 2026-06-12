package info

import (
	"context"
	"image/color"
	"strings"
	"sync"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/monetarium/skarb-wallet/app"
	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	libutils "github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/page/components"
	"github.com/monetarium/skarb-wallet/ui/page/transaction"
	"github.com/monetarium/skarb-wallet/ui/values"
	"github.com/monetarium/monetarium-node/dcrutil"
)

const InfoID = "Info"

type (
	C = layout.Context
	D = layout.Dimensions
)

type WalletInfo struct {
	*load.Load
	// GenericPageModal defines methods such as ID() and OnAttachedToNavigator()
	// that helps this Page satisfy the app.Page interface. It also defines
	// helper methods for accessing the PageNavigator that displayed this page
	// and the root WindowNavigator.
	*app.GenericPageModal
	wallet sharedW.Asset

	container *widget.List

	// txMu guards transactions, stakes and showMaterialLoader: loadTransactions
	// / loadStakes run on the wallet notification goroutine (ListenForNewTx)
	// and on `go`-launched calls from OnNavigatedTo, while Layout and
	// HandleUserInteractions read these on the UI thread. Without the lock the
	// slice-header write races with the reads, and swapping in a shorter slice
	// between a frame and a click could index out of range.
	txMu               sync.RWMutex
	transactions       []*sharedW.Transaction
	recentTransactions *cryptomaterial.ClickableList

	stakes       []*sharedW.Transaction
	recentStakes *cryptomaterial.ClickableList

	mixerInfoButton,
	mixerRedirectButton cryptomaterial.IconButton
	unmixedBalance sharedW.AssetAmount

	viewAllTxButton,
	viewAllStakeButton cryptomaterial.Button

	walletSyncInfo *components.WalletSyncInfo

	materialLoader     material.LoaderStyle
	showMaterialLoader bool

	// blockAgeTickerCancel stops the goroutine that nudges the page to
	// re-layout every minute so the "Last block — X minutes ago" line
	// in WalletSyncInfo refreshes without user interaction. Without
	// this, the page reads GetBestBlockTimeStamp on every layout but
	// nothing triggers a layout between block-attached notifications,
	// so the relative-time string only ticks forward when the user
	// happens to click or scroll. Cancelled in OnNavigatedFrom.
	blockAgeTickerCancel context.CancelFunc
}

func NewInfoPage(l *load.Load, wallet sharedW.Asset, backup func(sharedW.Asset)) *WalletInfo {
	pg := &WalletInfo{
		Load:             l,
		GenericPageModal: app.NewGenericPageModal(InfoID),
		wallet:           wallet,
		container: &widget.List{
			List: layout.List{Axis: layout.Vertical},
		},
		recentTransactions: l.Theme.NewClickableList(layout.Vertical),
		recentStakes:       l.Theme.NewClickableList(layout.Vertical),
		materialLoader:     material.Loader(l.Theme.Base),
	}
	pg.walletSyncInfo = components.NewWalletSyncInfo(l, wallet, pg.reload, backup)
	pg.recentTransactions.Radius = cryptomaterial.Radius(14)
	pg.recentTransactions.IsShadowEnabled = true
	pg.recentStakes.Radius = cryptomaterial.Radius(14)
	pg.recentStakes.IsShadowEnabled = true

	pg.viewAllTxButton = pg.Theme.OutlineButton(values.String(values.StrViewAll))
	pg.viewAllTxButton.Font.Weight = font.Medium
	pg.viewAllTxButton.TextSize = values.TextSize16
	pg.viewAllTxButton.Inset = layout.UniformInset(0)
	pg.viewAllTxButton.HighlightColor = color.NRGBA{}

	pg.viewAllStakeButton = pg.Theme.OutlineButton(values.String(values.StrViewAll))
	pg.viewAllStakeButton.Font.Weight = font.Medium
	pg.viewAllStakeButton.TextSize = values.TextSize16
	pg.viewAllStakeButton.Inset = layout.UniformInset(0)
	pg.viewAllStakeButton.HighlightColor = color.NRGBA{}

	pg.mixerRedirectButton, pg.mixerInfoButton = components.SubpageHeaderButtons(l)
	pg.mixerRedirectButton.Icon = pg.Theme.Icons.NavigationArrowForward
	pg.mixerRedirectButton.Size = values.MarginPadding20

	return pg
}

// OnNavigatedTo is called when the page is about to be displayed and
// may be used to initialize page features that are only relevant when
// the page is displayed.
// Part of the load.Page interface.
func (pg *WalletInfo) OnNavigatedTo() {
	pg.walletSyncInfo.Init()
	pg.walletSyncInfo.ListenForNotifications() // stopped in OnNavigatedFrom()

	// Prime the block-age cache so first frame shows real "X ago"
	// instead of empty string — see WalletSyncInfo.blockAgeText().
	pg.walletSyncInfo.RefreshBlockAge()
	pg.startBlockAgeTicker()

	go pg.loadTransactions()

	if pg.wallet.GetAssetType() == libutils.DCRWalletAsset {
		go pg.loadStakes()

		if pg.wallet.(*dcr.Asset).IsAccountMixerActive() {
			pg.listenForMixerNotifications()
			pg.reloadMixerBalances()
		}
	}
}

func (pg *WalletInfo) reload() {
	pg.ParentWindow().Reload()
}

// Layout draws the page UI components into the provided layout context
// to be eventually drawn on screen.
// Part of the load.Page interface.
// Layout lays out the widgets for the main wallets pg.
// snapshotTxs / snapshotStakes / loaderShown return the slice headers / flag
// under the read lock. loadTransactions/loadStakes assign each slice header in
// a single locked write and never mutate the backing array afterward, so a
// header snapshot is internally consistent for its own len()+index use.
func (pg *WalletInfo) snapshotTxs() []*sharedW.Transaction {
	pg.txMu.RLock()
	defer pg.txMu.RUnlock()
	return pg.transactions
}

func (pg *WalletInfo) snapshotStakes() []*sharedW.Transaction {
	pg.txMu.RLock()
	defer pg.txMu.RUnlock()
	return pg.stakes
}

func (pg *WalletInfo) loaderShown() bool {
	pg.txMu.RLock()
	defer pg.txMu.RUnlock()
	return pg.showMaterialLoader
}

func (pg *WalletInfo) Layout(gtx C) D {
	return pg.Theme.List(pg.container).Layout(gtx, 1, func(gtx C, _ int) D {
		items := []layout.FlexChild{layout.Rigid(pg.walletSyncInfo.WalletInfoLayout)}

		items = append(items, layout.Rigid(layout.Spacer{Height: values.MarginPadding16}.Layout))

		if pg.wallet.GetAssetType() == libutils.DCRWalletAsset && pg.wallet.(*dcr.Asset).IsAccountMixerActive() {
			items = append(items, layout.Rigid(pg.mixerLayout))
		}
		if pg.loaderShown() {
			items = append(items, layout.Rigid(func(gtx C) D {
				return layout.Center.Layout(gtx, pg.materialLoader.Layout)
			}))
		}
		if len(pg.snapshotTxs()) > 0 {
			items = append(items, layout.Rigid(pg.recentTransactionLayout))
		}

		if len(pg.snapshotStakes()) > 0 {
			items = append(items, layout.Rigid(pg.recentStakeLayout))
		}

		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, items...)
	})
}

func (pg *WalletInfo) mixerLayout(gtx C) D {
	return layout.Inset{
		Bottom: values.MarginPadding16,
	}.Layout(gtx, func(gtx C) D {
		return components.MixerComponent{
			Load:           pg.Load,
			WalletName:     pg.wallet.GetWalletName(),
			UnmixedBalance: pg.unmixedBalance.String(),
			ForwardButton:  pg.mixerRedirectButton,
			InfoButton:     pg.mixerInfoButton,
			Width:          cryptomaterial.MatchParent,
			Height:         cryptomaterial.WrapContent,
		}.MixerLayout(gtx)
	})
}

func (pg *WalletInfo) recentTransactionLayout(gtx C) D {
	txs := pg.snapshotTxs()
	return pg.pageContentWrapper(gtx, values.String(values.StrRecentTransactions), pg.viewAllTxButton.Layout, func(gtx C) D {
		return pg.recentTransactions.Layout(gtx, len(txs), func(gtx C, index int) D {
			tx := txs[index]
			isHiddenSeparator := index == len(txs)-1
			return pg.walletTxWrapper(gtx, tx, isHiddenSeparator)
		})
	})
}

func (pg *WalletInfo) recentStakeLayout(gtx C) D {
	stakes := pg.snapshotStakes()
	return pg.pageContentWrapper(gtx, values.String(values.StrStakingActivity), pg.viewAllStakeButton.Layout, func(gtx C) D {
		return pg.recentStakes.Layout(gtx, len(stakes), func(gtx C, index int) D {
			tx := stakes[index]
			isHiddenSeparator := index == len(stakes)-1
			return pg.walletTxWrapper(gtx, tx, isHiddenSeparator)
		})
	})
}

func (pg *WalletInfo) pageContentWrapper(gtx C, sectionTitle string, redirectBtn, body layout.Widget) D {
	return layout.Inset{
		Bottom: values.MarginPadding16,
	}.Layout(gtx, func(gtx C) D {
		return pg.Theme.Card().Layout(gtx, func(gtx C) D {
			return layout.UniformInset(values.MarginPadding16).Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						return layout.Inset{
							Bottom: values.MarginPadding16,
						}.Layout(gtx, func(gtx C) D {
							return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
								layout.Rigid(func(gtx C) D {
									if sectionTitle == "" {
										return D{}
									}

									txt := pg.Theme.Body1(sectionTitle)
									txt.Font.Weight = font.SemiBold
									return txt.Layout(gtx)
								}),
								layout.Flexed(1, func(gtx C) D {
									if redirectBtn != nil {
										return layout.E.Layout(gtx, redirectBtn)
									}
									return D{}
								}),
							)
						})
					}),
					layout.Rigid(body),
				)
			})
		})
	})
}

func (pg *WalletInfo) walletTxWrapper(gtx C, tx *sharedW.Transaction, isHiddenSeparator bool) D {
	if !isHiddenSeparator {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		separator := pg.Theme.Separator()
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return components.LayoutTransactionRow(gtx, pg.Load, pg.wallet, tx, true)
			}),
			layout.Rigid(func(gtx C) D {
				return layout.E.Layout(gtx, func(gtx C) D {
					// Show bottom divider for all rows except last
					return layout.Inset{Left: values.MarginPadding32}.Layout(gtx, separator.Layout)
				})
			}),
		)
	}

	return components.LayoutTransactionRow(gtx, pg.Load, pg.wallet, tx, true)
}

// HandleUserInteractions is called just before Layout() to determine
// if any user interaction recently occurred on the page and may be
// used to update the page's UI components shortly before they are
// displayed.
// Part of the load.Page interface.
func (pg *WalletInfo) HandleUserInteractions(gtx C) {
	// Process subpage events too.
	pg.walletSyncInfo.HandleUserInteractions(gtx)

	if clicked, selectedItem := pg.recentTransactions.ItemClicked(); clicked {
		// Bounds-check: the slice can be swapped for a shorter one by a
		// notification-goroutine reload between the rendered frame and this
		// click handler.
		if txs := pg.snapshotTxs(); selectedItem >= 0 && selectedItem < len(txs) {
			pg.ParentNavigator().Display(transaction.NewTransactionDetailsPage(pg.Load, pg.wallet, txs[selectedItem]))
		}
	}

	if clicked, selectedItem := pg.recentStakes.ItemClicked(); clicked {
		if stakes := pg.snapshotStakes(); selectedItem >= 0 && selectedItem < len(stakes) {
			pg.ParentNavigator().Display(transaction.NewTransactionDetailsPage(pg.Load, pg.wallet, stakes[selectedItem]))
		}
	}

	// Mixer & staking pages are not part of the v1 Monetarium wallet.

	if pg.viewAllTxButton.Button.Clicked(gtx) {
		pg.ParentNavigator().Display(transaction.NewTransactionsPage(pg.Load, pg.wallet))
	}
}

func (pg *WalletInfo) listenForMixerNotifications() {
	accountMixerNotificationListener := &dcr.AccountMixerNotificationListener{
		OnAccountMixerStarted: func(_ int) {
			pg.reloadMixerBalances()
			pg.ParentWindow().Reload()
		},
		OnAccountMixerEnded: func(_ int) {
			pg.reloadMixerBalances()
			pg.ParentWindow().Reload()
		},
	}
	err := pg.wallet.(*dcr.Asset).AddAccountMixerNotificationListener(accountMixerNotificationListener, InfoID)
	if err != nil {
		log.Errorf("Error adding account mixer notification listener: %+v", err)
		return
	}

	// this is needed to refresh the UI on every block
	txAndBlockNotificationListener := &sharedW.TxAndBlockNotificationListener{
		OnBlockAttached: func(_ int, _ int32) {
			pg.reloadMixerBalances()
			pg.ParentWindow().Reload()
		},
	}
	err = pg.wallet.(*dcr.Asset).AddTxAndBlockNotificationListener(txAndBlockNotificationListener, InfoID)
	if err != nil {
		log.Errorf("Error adding tx and block notification listener: %v", err)
		return
	}
}

func (pg *WalletInfo) reloadMixerBalances() {
	accounts, _ := pg.wallet.GetAccountsRaw()
	for _, acct := range accounts.Accounts {
		if acct.Number == pg.wallet.(*dcr.Asset).UnmixedAccountNumber() {
			bal := acct.Balance.Total
			// to prevent NPE set default amount 0 if asset amount is nil
			if bal == nil {
				bal = dcr.Amount(dcrutil.Amount(0))
			}
			pg.unmixedBalance = bal
		}
	}
}

// Reload tx list when there is new tx. Called from parent page
func (pg *WalletInfo) ListenForNewTx(walletID int) {
	if walletID != pg.wallet.GetWalletID() {
		return
	}
	pg.loadTransactions()
}

func (pg *WalletInfo) loadTransactions() {
	pg.txMu.Lock()
	pg.showMaterialLoader = true
	pg.txMu.Unlock()
	mapInfo, _ := components.TxPageDropDownFields(pg.wallet.GetAssetType(), 0)
	if len(mapInfo) == 0 {
		log.Errorf("no tx filters for asset type (%v)", pg.wallet.GetAssetType())
		return
	}

	txs, err := pg.wallet.GetTransactionsRaw(0, 3, mapInfo[values.String(values.StrAll)], true, "")
	if err != nil {
		log.Errorf("error loading transactions: %v", err)
		return
	}
	// Diagnostic log: surfaces what's in storm DB at the moment Info
	// page (re)mounts. If the user reports "I sent a tx but it doesn't
	// appear", grep for "InfoPage.loadTransactions" in the wallet log
	// to see the count + hashes returned. Pairing this with the
	// "Broadcast: storm-DB save OK" line proves whether storm has the
	// tx or not.
	if len(txs) > 0 {
		hashes := make([]string, 0, len(txs))
		for _, t := range txs {
			if len(t.Hash) >= 12 {
				hashes = append(hashes, t.Hash[:12]+"…")
			} else {
				hashes = append(hashes, t.Hash)
			}
		}
		log.Infof("InfoPage.loadTransactions: %d tx(s) returned, hashes=[%s]", len(txs), strings.Join(hashes, ", "))
	} else {
		log.Infof("InfoPage.loadTransactions: 0 tx returned")
	}
	pg.txMu.Lock()
	pg.transactions = txs
	pg.showMaterialLoader = false
	pg.txMu.Unlock()
	pg.ParentWindow().Reload()
}

func (pg *WalletInfo) loadStakes() {
	// Build into a local slice and publish it with one locked assignment —
	// mutating pg.stakes across append/reslice while Layout reads it on the UI
	// thread is the race this avoids.
	stakes := make([]*sharedW.Transaction, 0)

	txs, err := pg.wallet.GetTransactionsRaw(0, 10, libutils.TxFilterStaking, true, "")
	if err != nil {
		log.Errorf("error loading staking activities: %v", err)
		return
	}
	for _, stakeTx := range txs {
		if (stakeTx.Type == dcr.TxTypeTicketPurchase) || (stakeTx.Type == dcr.TxTypeRevocation) {
			stakes = append(stakes, stakeTx)
		}
	}
	if len(stakes) > 3 {
		stakes = stakes[:3]
	}
	pg.txMu.Lock()
	pg.stakes = stakes
	pg.txMu.Unlock()
	pg.ParentWindow().Reload()
}

// OnNavigatedFrom is called when the page is about to be removed from
// the displayed window. This method should ideally be used to disable
// features that are irrelevant when the page is NOT displayed.
// NOTE: The page may be re-displayed on the app's window, in which case
// OnNavigatedTo() will be called again. This method should not destroy UI
// components unless they'll be recreated in the OnNavigatedTo() method.
// Part of the load.Page interface.
func (pg *WalletInfo) OnNavigatedFrom() {
	pg.walletSyncInfo.StopListeningForNotifications()
	if pg.blockAgeTickerCancel != nil {
		pg.blockAgeTickerCancel()
		pg.blockAgeTickerCancel = nil
	}
	if pg.wallet.GetAssetType() == libutils.DCRWalletAsset {
		pg.wallet.(*dcr.Asset).RemoveAccountMixerNotificationListener(InfoID)
	}
}

// startBlockAgeTicker fires a ParentWindow().Reload() periodically so the
// "Last block — X ago" line in WalletSyncInfo updates without requiring a
// user click. The 15s cadence is short enough that users see the minute
// counter tick over without a noticeable lag, long enough that the
// re-layout isn't a battery drain. TimeFormat() rounds to minutes so
// finer intervals wouldn't change the display anyway.
//
// Defensively cancels any prior ticker first — OnNavigatedTo can fire
// repeatedly (back/forward navigation, tab switches), and stacking
// goroutines would otherwise leak one ticker per navigation cycle.
//
// `defer t.Stop()` AND `ctx.Done()` are both needed: ctx.Done() handles
// the OnNavigatedFrom cancel path, t.Stop() releases the runtime timer
// resource when the goroutine exits via either path.
func (pg *WalletInfo) startBlockAgeTicker() {
	if pg.blockAgeTickerCancel != nil {
		pg.blockAgeTickerCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	pg.blockAgeTickerCancel = cancel
	log.Infof("InfoPage: starting block-age ticker (5s)")
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				log.Infof("InfoPage: block-age ticker stopped")
				return
			case <-t.C:
				// Refresh the cached "X ago" string BEFORE Reload() —
				// Layout will then read the freshly stored value. Without
				// this the cache stays at the value computed on
				// OnNavigatedTo and the displayed time never advances
				// (cursor-driven re-layouts read the same cached string).
				pg.walletSyncInfo.RefreshBlockAge()
				win := pg.ParentWindow()
				if win != nil {
					win.Reload()
				} else {
					log.Warnf("InfoPage: ticker fired but ParentWindow() is nil")
				}
			}
		}
	}()
}
