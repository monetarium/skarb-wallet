package wallet

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/gen2brain/beeep"
	"github.com/monetarium/monetarium-node/cointype"
	"github.com/monetarium/skarb-wallet/app"
	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	libutils "github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/modal"
	"github.com/monetarium/skarb-wallet/ui/page/accounts"
	"github.com/monetarium/skarb-wallet/ui/page/components"
	"github.com/monetarium/skarb-wallet/ui/page/info"
	"github.com/monetarium/skarb-wallet/ui/page/receive"
	"github.com/monetarium/skarb-wallet/ui/page/seedbackup"
	"github.com/monetarium/skarb-wallet/ui/page/send"
	"github.com/monetarium/skarb-wallet/ui/page/staking"
	"github.com/monetarium/skarb-wallet/ui/page/transaction"
	"github.com/monetarium/skarb-wallet/ui/utils"
	"github.com/monetarium/skarb-wallet/ui/values"
)

const (
	MainPageID = "Main"
)

var selectedTab = map[int]string{}

type (
	C = layout.Context
	D = layout.Dimensions
)

var PageNavigationMap = map[string]string{
	values.StrInfo:         info.InfoID,
	values.StrSend:         send.SendPageID,
	values.StrReceive:      receive.ReceivePageID,
	values.StrTransactions: transaction.TransactionsPageID,
	values.StrStaking:      staking.OverviewPageID,
	values.StrSettings:     WalletSettingsPageID,
}

// SingleWalletMasterPage is a master page for interacting with a single wallet.
// It has sub pages for viewing a wallet's info, sending and receiving funds for
// a wallet, viewing a wallet's transactions, etc.
type SingleWalletMasterPage struct {
	*app.MasterPage
	*load.Load

	selectedWallet sharedW.Asset
	// walletBalance is cached here to avoid repeatedly fetching the balance
	// from the wallet on each layout. TODO: Ensure this is updated on new
	// blocks and txs, or read realtime balance directly from wallet and don't
	// cache.
	walletBalance sharedW.AssetAmount

	PageNavigationTab      *cryptomaterial.SegmentedControl
	hideBalanceButton      *cryptomaterial.Clickable
	refreshExchangeRateBtn *cryptomaterial.Clickable
	openWalletSelector     cryptomaterial.IconButton
	checkBox               cryptomaterial.CheckBoxStyle
	navigateToSyncBtn      cryptomaterial.Button
	walletDropdown         *cryptomaterial.DropDown
	allWallets             []sharedW.Asset

	// usdExchangeRate + usdExchangeSet are written by the fetchExchangeRate
	// goroutine and read by LayoutUSDBalance/updateBalance on the UI thread —
	// guarded by balanceMu. isFetchingExchangeRate is the in-flight guard: an
	// atomic.Bool so the click-refresh and the settings-refresh can't both pass
	// a non-atomic read-modify-write check and launch two concurrent fetches.
	usdExchangeRate        float64
	usdExchangeSet         bool
	isFetchingExchangeRate atomic.Bool
	isBalanceHidden        bool

	totalBalanceUSD string

	// skaBalanceLines holds pre-formatted SKA token balance strings
	// ("1.5 SKA1") for this wallet, aggregated across accounts. The VAR
	// total above comes from the legacy int64 Balance path, which has no
	// SKA fields; these lines surface the wallet's real token holdings so a
	// 0-VAR / SKA-only wallet no longer shows just "0 VAR" in the header.
	// Guarded by balanceMu (computed off-thread in updateBalance).
	skaBalanceLines []string

	// varHidden mirrors the wallet's coin-visibility filter for VAR.
	// Cached here (recomputed in updateBalance) so Layout doesn't read the
	// wallet config every frame. Guarded by balanceMu.
	varHidden bool

	// balanceMu guards walletBalance + totalBalanceUSD because
	// updateBalance() is called both from the UI thread (initial load,
	// language/theme refresh) AND from sync-notification goroutines
	// (OnSyncCompleted, OnTransaction, OnBlockAttached). Without it the
	// goroutine writes race with Layout's reads of these fields (lines
	// ~662, 670, 686, 693). Plain mutex is simpler than the atomic-flag-
	// drain pattern here because balance updates are infrequent and the
	// reads are tight — no risk of UI-thread starvation.
	balanceMu sync.RWMutex

	activeTab         map[string]string
	PageNavigationMap map[string]string

	showNavigationFunc func()
}

func NewSingleWalletMasterPage(l *load.Load, wallet sharedW.Asset, showNavigationFunc func()) *SingleWalletMasterPage {
	swmp := &SingleWalletMasterPage{
		Load:               l,
		MasterPage:         app.NewMasterPage(MainPageID),
		selectedWallet:     wallet,
		checkBox:           l.Theme.CheckBox(new(widget.Bool), values.String(values.StrAwareOfRisk)),
		navigateToSyncBtn:  l.Theme.Button(values.String(values.StrStartSync)),
		showNavigationFunc: showNavigationFunc,
	}
	swmp.walletDropdown = swmp.createWalletDropdown()

	swmp.activeTab = make(map[string]string)
	swmp.hideBalanceButton = swmp.Theme.NewClickable(false)
	swmp.openWalletSelector = swmp.Theme.IconButton(swmp.Theme.Icons.NavigationArrowBack)
	swmp.refreshExchangeRateBtn = swmp.Theme.NewClickable(true)

	swmp.openWalletSelector = components.GetBackButton(l)

	swmp.initTabOptions()

	return swmp
}

func (swmp *SingleWalletMasterPage) createWalletDropdown() *cryptomaterial.DropDown {
	swmp.allWallets = swmp.AssetsManager.AssetWallets()
	items := []cryptomaterial.DropDownItem{}
	selectedItem := cryptomaterial.DropDownItem{}
	for _, w := range swmp.allWallets {
		item := cryptomaterial.DropDownItem{
			Text:      fmt.Sprint(w.GetWalletID()),
			Icon:      components.CoinImageBySymbol(swmp.Load, w.GetAssetType(), w.IsWatchingOnlyWallet()),
			DisplayFn: swmp.getWalletItemLayout(w),
		}
		if w.GetWalletID() == swmp.selectedWallet.GetWalletID() {
			selectedItem = item
		}
		items = append(items, item)
	}
	dropdown := swmp.Theme.NewCommonDropDown(items, &selectedItem, cryptomaterial.WrapContent, values.WalletsDropdownGroup, false)
	color := values.TransparentColor(values.TransparentWhite, 1)
	dropdown.Background = &color
	return dropdown
}

func (swmp *SingleWalletMasterPage) getWalletItemLayout(wallet sharedW.Asset) layout.Widget {
	return func(gtx C) D {
		lbl := swmp.Theme.SemiBoldLabel(wallet.GetWalletName())
		lbl.MaxLines = 1
		lbl.TextSize = values.TextSizeTransform(swmp.IsMobileView(), values.TextSize20)
		return lbl.Layout(gtx)
	}
}

// ID is a unique string that identifies the page and may be used
// to differentiate this page from other pages.
// Part of the load.Page interface.
func (swmp *SingleWalletMasterPage) ID() string {
	return MainPageID
}

// OnNavigatedTo is called when the page is about to be displayed and
// may be used to initialize page features that are only relevant when
// the page is displayed.
// Part of the load.Page interface.
func (swmp *SingleWalletMasterPage) OnNavigatedTo() {
	// Rebuild the header wallet-selector so a wallet created since this page was
	// constructed appears immediately (createWalletDropdown re-reads
	// AssetWallets()). Without this a freshly created wallet only showed up in
	// the header after navigating away and back via Overview.
	swmp.walletDropdown = swmp.createWalletDropdown()

	// load wallet account balance first before rendering page contents.
	// It loads balance for the current selected wallet.
	swmp.updateBalance()
	swmp.isBalanceHidden = swmp.AssetsManager.IsTotalBalanceVisible()
	// updateExchangeSetting also calls updateBalance() but because of the API
	// call it may take a while before the balance and USD conversion is updated.
	// updateBalance() is called above first to prevent crash when balance value
	// is required before updateExchangeSetting() returns.
	swmp.updateExchangeSetting()

	backupLater := swmp.selectedWallet.ReadBoolConfigValueForKey(sharedW.SeedBackupNotificationConfigKey, false)
	// reset the checkbox
	swmp.checkBox.CheckBox.Value = false

	needBackup := !swmp.selectedWallet.IsWalletBackedUp()

	walletID := swmp.selectedWallet.GetWalletID()
	if tab, ok := selectedTab[walletID]; ok {
		swmp.PageNavigationTab.SetSelectedSegment(tab)
		swmp.navigateToSelectedTab()
	} else if swmp.CurrentPage() == nil {
		swmp.Display(info.NewInfoPage(swmp.Load, swmp.selectedWallet, swmp.backup)) // TODO: Should pagestack have a start page? YES!
	} else {
		swmp.CurrentPage().OnNavigatedTo()
	}

	if needBackup && !backupLater {
		swmp.showBackupInfo()
	}
	// set active tab value
	swmp.activeTab[swmp.PageNavigationTab.SelectedSegment()] = swmp.CurrentPageID()

	swmp.listenForNotifications(func(walletID int) {
		go swmp.ListenNewTxForSubPage(walletID)
	}) // ntfn listeners are stopped in OnNavigatedFrom().

	if swmp.selectedWallet.GetAssetType() == libutils.DCRWalletAsset {
		if swmp.selectedWallet.ReadBoolConfigValueForKey(sharedW.FetchProposalConfigKey, false) && swmp.isGovernanceAPIAllowed() {
			if swmp.AssetsManager.Politeia.IsSyncing() {
				return
			}
			go func() {
				_ = swmp.AssetsManager.Politeia.Sync(context.TODO()) // TODO: Politeia should be given a ctx when initialized.
			}()
		}
	}
}

// Call the subpage component update functions when there is a new tx
func (swmp *SingleWalletMasterPage) ListenNewTxForSubPage(walletID int) {
	switch swmp.CurrentPageID() {
	case transaction.TransactionsPageID:
		swmp.CurrentPage().(*transaction.TransactionsPage).ListenForTxNotification(walletID)
		return
	case info.InfoID:
		swmp.CurrentPage().(*info.WalletInfo).ListenForNewTx(walletID)
	default:
		return
	}
}

// initTabOptions initializes the page navigation tabs
func (swmp *SingleWalletMasterPage) initTabOptions() {
	commonTabs := []string{
		values.StrInfo,
		values.StrReceive,
		values.StrTransactions,
		values.StrAccounts,
		values.StrSettings,
	}

	if !swmp.selectedWallet.IsWatchingOnlyWallet() {
		// Add 'Send' to the tabs for non-watching-only wallets.
		sendTab := []string{values.StrSend}
		// Insert 'Send' after 'StrInfo'.
		commonTabs = append(commonTabs[:1], append(sendTab, commonTabs[1:]...)...)
	}

	// Staking tab for DCR wallets that can sign (not watch-only). Inserted just
	// before 'Accounts', mirroring Cryptopower's tab order. CoinShuffle++ mixing
	// (StakeShuffle) stays out — only PoS staking is restored.
	if swmp.selectedWallet.GetAssetType() == libutils.DCRWalletAsset && !swmp.selectedWallet.IsWatchingOnlyWallet() {
		withStaking := make([]string, 0, len(commonTabs)+1)
		for _, t := range commonTabs {
			if t == values.StrAccounts {
				withStaking = append(withStaking, values.StrStaking)
			}
			withStaking = append(withStaking, t)
		}
		commonTabs = withStaking
	}

	// SegmentTypeGroupMax distributes width evenly across all tabs
	// instead of laying them out in a horizontal scroller. Split mode
	// (the previous setting) padded each tab with 32px LR + a chevron
	// nav on either side, so the 6 Ukrainian-localised tabs
	// (Інформація / Надіслати / Отримати / Транзакції / Акаунти /
	// Налаштування) didn't fit on a typical desktop width and the
	// user had to scroll the strip horizontally. GroupMax computes
	// per-tab Width = (layoutSize − 8) / len(tabs) — every tab gets
	// an equal slice, no overflow, no scroll buttons.
	swmp.PageNavigationTab = swmp.Theme.SegmentedControl(commonTabs, cryptomaterial.SegmentTypeGroupMax)
	swmp.PageNavigationTab.SetEnableSwipe(false)
	dp5 := values.MarginPadding5
	swmp.PageNavigationTab.ContentPadding = layout.Inset{
		Left:  dp5,
		Right: dp5,
		Top:   values.MarginPaddingTransform(swmp.IsMobileView(), values.MarginPadding16),
	}
}

func (swmp *SingleWalletMasterPage) isGovernanceAPIAllowed() bool {
	return swmp.AssetsManager.IsHTTPAPIPrivacyModeOff(libutils.GovernanceHTTPAPI)
}

func (swmp *SingleWalletMasterPage) updateExchangeSetting() {
	swmp.balanceMu.Lock()
	swmp.usdExchangeSet = false
	swmp.balanceMu.Unlock()
	if swmp.AssetsManager.ExchangeRateFetchingEnabled() {
		go swmp.fetchExchangeRate()
	}
}

func (swmp *SingleWalletMasterPage) fetchExchangeRate() {
	// Atomic CAS guard: if a fetch is already in flight, bail. A plain
	// read-then-set let two callers (refresh button + settings change) both
	// pass the check and launch concurrent fetches that raced on the rate.
	if !swmp.isFetchingExchangeRate.CompareAndSwap(false, true) {
		return
	}
	defer swmp.isFetchingExchangeRate.Store(false)

	market, err := utils.USDMarketFromAsset(swmp.selectedWallet.GetAssetType())
	if err != nil {
		log.Errorf("Asset type %q is not supported for exchange rate fetching", swmp.selectedWallet.GetAssetType())
		return
	}

	rate := swmp.AssetsManager.RateSource.GetTicker(market, false)
	if rate == nil || rate.LastTradePrice <= 0 {
		return
	}

	swmp.balanceMu.Lock()
	swmp.usdExchangeRate = rate.LastTradePrice
	swmp.balanceMu.Unlock()
	swmp.updateBalance()
	swmp.balanceMu.Lock()
	swmp.usdExchangeSet = true
	swmp.balanceMu.Unlock()
	swmp.ParentWindow().Reload()
}

func (swmp *SingleWalletMasterPage) updateBalance() {
	// Heavy work (RPC + USD math) runs lock-free, the final swap into
	// the cached fields is the only critical section. This minimises
	// the Layout-thread RLock wait window when a tx-notification or
	// sync-completed callback fires under load.
	totalBalance, err := components.CalculateTotalWalletsBalance(swmp.selectedWallet)
	if err != nil {
		log.Error(err)
		return
	}
	swmp.balanceMu.RLock()
	usdRate := swmp.usdExchangeRate
	swmp.balanceMu.RUnlock()
	balanceInUSD := totalBalance.Total.MulF64(usdRate).ToCoin()
	usdStr := utils.FormatAsUSDString(swmp.Printer, balanceInUSD)

	// Compute SKA token balances off-thread too, so the header can show
	// them alongside the VAR total. Done before the critical section to
	// keep the lock window tight.
	skaLines := swmp.computeSKABalanceLines()

	varHidden := false
	if dcrAsset, ok := swmp.selectedWallet.(*dcr.Asset); ok {
		varHidden = dcrAsset.HiddenCoinTypes()[cointype.CoinTypeVAR]
	}

	swmp.balanceMu.Lock()
	swmp.walletBalance = totalBalance.Total
	swmp.totalBalanceUSD = usdStr
	swmp.skaBalanceLines = skaLines
	swmp.varHidden = varHidden
	swmp.balanceMu.Unlock()
}

// computeSKABalanceLines aggregates this wallet's SKA token balances across
// accounts and returns one pre-formatted "amount SYMBOL" string per coin
// with a non-zero balance. VAR is shown via the int64 total elsewhere; this
// fills the SKA gap the legacy Balance struct leaves. Safe to call from a
// notification goroutine (no UI-thread state touched).
func (swmp *SingleWalletMasterPage) computeSKABalanceLines() []string {
	dcrAsset, ok := swmp.selectedWallet.(*dcr.Asset)
	if !ok {
		return nil
	}
	balances, err := dcrAsset.GetWalletCoinBalances()
	if err != nil {
		log.Errorf("single-wallet: GetWalletCoinBalances: %v", err)
		return nil
	}
	var lines []string
	for _, ct := range dcrAsset.VisibleCoinTypes() {
		if !ct.IsSKA() {
			continue
		}
		bal, ok := balances[ct]
		if !ok {
			continue
		}
		// Only non-zero balances, matching this function's doc and the account-
		// details page — a visible-but-empty SKA coin shouldn't add a "0.00
		// SKAx" line under the VAR total.
		if bal.SKATotal.Sign() <= 0 && bal.SKASpendable.Sign() <= 0 && bal.SKAUnconfirmed.Sign() <= 0 {
			continue
		}
		bal.CoinType = ct // ensure FormatCoinAmount picks the SKA branch
		lines = append(lines, dcr.FormatCoinAmount(bal))
	}
	return lines
}

// OnDarkModeChanged is triggered whenever the dark mode setting is changed
// to enable restyling UI elements where necessary.
// Satisfies the load.AppSettingsChangeHandler interface.
func (swmp *SingleWalletMasterPage) OnDarkModeChanged(isDarkModeOn bool) {
	// TODO: currentPage will likely be the Settings page when this method
	// is called. If that page implements the AppSettingsChangeHandler interface,
	// the following code will trigger the OnDarkModeChanged method of that
	// page.
	if currentPage, ok := swmp.CurrentPage().(load.AppSettingsChangeHandler); ok {
		currentPage.OnDarkModeChanged(isDarkModeOn)
	}
}

func (swmp *SingleWalletMasterPage) OnCurrencyChanged() {
	swmp.updateExchangeSetting()
}

func (swmp *SingleWalletMasterPage) changeTab(tab string) {
	selectedTab[swmp.selectedWallet.GetWalletID()] = tab
	swmp.PageNavigationTab.SetSelectedSegment(tab)
	swmp.navigateToSelectedTab()
}

// HandleUserInteractions is called just before Layout() to determine
// if any user interaction recently occurred on the page and may be
// used to update the page's UI components shortly before they are
// displayed.
// Part of the load.Page interface.
func (swmp *SingleWalletMasterPage) HandleUserInteractions(gtx C) {
	// Keep the header wallet-selector in sync with the wallet set. A wallet
	// created via the in-app "create wallet" flow returns here through
	// ClosePagesAfter, which doesn't reliably re-fire OnNavigatedTo, so a fresh
	// wallet otherwise wouldn't appear in the dropdown until the next navigation.
	// Rebuild (cheap length check per frame, like the sidebar's refreshWalletList)
	// whenever the count changes — covers both creation and deletion.
	if len(swmp.AssetsManager.AssetWallets()) != len(swmp.allWallets) {
		swmp.walletDropdown = swmp.createWalletDropdown()
	}

	if swmp.checkBox.CheckBox.Update(gtx) {
		swmp.ParentWindow().Reload()
	}

	if swmp.walletDropdown.Changed(gtx) {
		swmp.OnNavigatedFrom()
		swmp.CloseAllPages()
		swmp.selectedWallet = swmp.allWallets[swmp.walletDropdown.SelectedIndex()]
		swmp.initTabOptions()
		swmp.OnNavigatedTo()
	}

	if swmp.CurrentPage() != nil {
		swmp.CurrentPage().HandleUserInteractions(gtx)
	}

	if swmp.refreshExchangeRateBtn.Clicked(gtx) {
		go swmp.fetchExchangeRate()
	}

	if swmp.openWalletSelector.Button.Clicked(gtx) {
		swmp.showNavigationFunc()
	}

	if swmp.PageNavigationTab.Changed() {
		selectedTab[swmp.selectedWallet.GetWalletID()] = swmp.PageNavigationTab.SelectedSegment()
		swmp.navigateToSelectedTab()
	}

	// update active page tab. This is needed for scenarios where a page is
	// navigated to without using the page navigation tab. An example is
	// the redirection action from the info page to the mixer page.
	if swmp.CurrentPageID() != swmp.activeTab[swmp.PageNavigationTab.SelectedSegment()] {
		for tabTitle, pageID := range PageNavigationMap {
			if swmp.CurrentPageID() == pageID {
				swmp.activeTab[tabTitle] = swmp.CurrentPageID()
				swmp.PageNavigationTab.SetSelectedSegment(tabTitle)
			}
		}
	}

	if swmp.navigateToSyncBtn.Button.Clicked(gtx) {
		swmp.ToggleSync(swmp.selectedWallet, func(b bool) {
			swmp.selectedWallet.SaveUserConfigValue(sharedW.AutoSyncConfigKey, b)
			swmp.Display(info.NewInfoPage(swmp.Load, swmp.selectedWallet, swmp.backup))
		})
	}

	if swmp.hideBalanceButton.Clicked(gtx) {
		swmp.isBalanceHidden = !swmp.isBalanceHidden
		swmp.AssetsManager.SetTotalBalanceVisibility(swmp.isBalanceHidden)
	}
}

func (swmp *SingleWalletMasterPage) navigateToSelectedTab() {
	displayPage := func(pg app.Page) {
		// Load the current wallet balance on page reload.
		swmp.updateBalance()
		swmp.Display(pg)
	}

	var pg app.Page
	switch swmp.PageNavigationTab.SelectedSegment() {
	case values.StrSend:
		pg = send.NewSendPage(swmp.Load, swmp.selectedWallet)
	case values.StrReceive:
		pg = receive.NewReceivePage(swmp.Load, swmp.selectedWallet)
	case values.StrInfo:
		pg = info.NewInfoPage(swmp.Load, swmp.selectedWallet, swmp.backup)
	case values.StrTransactions:
		txPage := transaction.NewTransactionsPage(swmp.Load, swmp.selectedWallet)
		txPage.DisableUniformTab()
		pg = txPage
	case values.StrStaking:
		if dcrW, ok := swmp.selectedWallet.(*dcr.Asset); ok {
			pg = staking.NewStakingPage(swmp.Load, dcrW)
		}
	case values.StrAccounts:
		pg = accounts.NewAccountPage(swmp.Load, swmp.selectedWallet)
	case values.StrSettings:
		pg = NewSettingsPage(swmp.Load, swmp.selectedWallet, swmp.showNavigationFunc, swmp.changeTab).
			SetBalanceRefresher(func() {
				// Recompute the cached header balances (updateBalance reads
				// the coin filter) and redraw — the toggle takes effect in
				// the header immediately. updateBalance locks balanceMu, so
				// running it off the UI thread is safe.
				go func() {
					swmp.updateBalance()
					swmp.ParentWindow().Reload()
				}()
			})
	}

	if pg == nil {
		// No subpage available for this tab (e.g. removed-in-v1 staking or
		// privacy entries triggered through stale state). Bail rather than
		// dereferencing nil — the user stays on the current page.
		log.Warnf("navigateToSelectedTab: no page registered for tab %q",
			swmp.PageNavigationTab.SelectedSegment())
		return
	}
	swmp.activeTab[swmp.PageNavigationTab.SelectedSegment()] = pg.ID()
	swmp.PageNavigationTab.ScrollTo(swmp.PageNavigationTab.SelectedIndex())

	displayPage(pg)
}

// KeysToHandle returns a Filter's slice that describes a set of key combinations
// that this page wishes to capture. The HandleKeyPress() method will only be
// called when any of these key combinations is pressed.
// Satisfies the load.KeyEventHandler interface for receiving key events.
func (swmp *SingleWalletMasterPage) KeysToHandle() []event.Filter {
	if currentPage := swmp.CurrentPage(); currentPage != nil {
		if keyEvtHandler, ok := currentPage.(load.KeyEventHandler); ok {
			return keyEvtHandler.KeysToHandle()
		}
	}
	return nil
}

// HandleKeyPress is called when one or more keys are pressed on the current
// window that match any of the key combinations returned by KeysToHandle().
// Satisfies the load.KeyEventHandler interface for receiving key events.
func (swmp *SingleWalletMasterPage) HandleKeyPress(gtx C, evt *key.Event) {
	if currentPage := swmp.CurrentPage(); currentPage != nil {
		if keyEvtHandler, ok := currentPage.(load.KeyEventHandler); ok {
			keyEvtHandler.HandleKeyPress(gtx, evt)
		}
	}
}

// OnNavigatedFrom is called when the page is about to be removed from
// the displayed window. This method should ideally be used to disable
// features that are irrelevant when the page is NOT displayed.
// NOTE: The page may be re-displayed on the app's window, in which case
// OnNavigatedTo() will be called again. This method should not destroy UI
// components unless they'll be recreated in the OnNavigatedTo() method.
// Part of the load.Page interface.
func (swmp *SingleWalletMasterPage) OnNavigatedFrom() {
	// Also disappear all child pages.
	if swmp.CurrentPage() != nil {
		swmp.CurrentPage().OnNavigatedFrom()
	}

	// The encrypted seed exists by default and is cleared after wallet is backed up.
	// Activate the modal requesting the user to backup their current wallet on
	// every wallet open request until the encrypted seed is cleared (backup happens).
	if !swmp.selectedWallet.IsWalletBackedUp() {
		swmp.selectedWallet.SaveUserConfigValue(sharedW.SeedBackupNotificationConfigKey, false)
	}

	swmp.stopNtfnListeners()
}

// Layout draws the page UI components into the provided layout context
// to be eventually drawn on screen.
// Part of the load.Page interface.
func (swmp *SingleWalletMasterPage) Layout(gtx C) D {
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx C) D {
			return cryptomaterial.LinearLayout{
				Width:       cryptomaterial.MatchParent,
				Height:      cryptomaterial.MatchParent,
				Orientation: layout.Vertical,
				Alignment:   layout.Middle,
			}.Layout(gtx,
				layout.Rigid(swmp.LayoutTopBar),
				layout.Rigid(func(gtx C) D {
					return layout.Inset{
						Top:    values.MarginPadding0,
						Bottom: values.MarginPadding0,
					}.Layout(gtx, func(gtx C) D {
						return swmp.PageNavigationTab.Layout(gtx, func(gtx C) D {
							if swmp.CurrentPage() == nil {
								return D{}
							}
							switch swmp.CurrentPage().ID() {
							case receive.ReceivePageID, send.SendPageID,
								transaction.TransactionsPageID, accounts.AccountsPageID:
								// Disable page functionality if a page is not synced or rescanning is in progress.
								if swmp.selectedWallet.IsSyncing() {
									syncInfo := components.NewWalletSyncInfo(swmp.Load, swmp.selectedWallet, func() {}, func(_ sharedW.Asset) {})
									blockHeightFetched := values.StringF(values.StrBlockHeaderFetchedCount, swmp.selectedWallet.GetBestBlock().Height, syncInfo.FetchSyncProgress().HeadersToFetchOrScan())
									title := values.String(values.StrFunctionUnavailable)
									subTitle := fmt.Sprintf("%s "+blockHeightFetched, values.String(values.StrBlockHeaderFetched))
									return components.DisablePageWithOverlay(swmp.Load, swmp.CurrentPage(), gtx,
										title, subTitle, nil)
								}
								if !swmp.selectedWallet.IsSynced() || swmp.selectedWallet.IsRescanning() {
									return components.DisablePageWithOverlay(swmp.Load, swmp.CurrentPage(), gtx,
										values.String(values.StrFunctionUnavailable), "", &swmp.navigateToSyncBtn)
								}
								fallthrough
							default:
								return swmp.CurrentPage().Layout(gtx)
							}
						}, swmp.IsMobileView())
					})
				}),
			)
		}),
	)
}

func (swmp *SingleWalletMasterPage) LayoutTopBar(gtx C) D {
	assetType := swmp.selectedWallet.GetAssetType()
	return cryptomaterial.LinearLayout{
		Width:       cryptomaterial.MatchParent,
		Height:      cryptomaterial.WrapContent,
		Orientation: layout.Vertical,
	}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			h := values.MarginPadding24
			v := values.MarginPadding8
			return cryptomaterial.LinearLayout{
				Width:       cryptomaterial.MatchParent,
				Height:      cryptomaterial.WrapContent,
				Orientation: layout.Horizontal,
				Alignment:   layout.Middle,
				Padding: layout.Inset{
					Right:  h,
					Left:   values.MarginPadding10,
					Top:    v,
					Bottom: v,
				},
			}.GradientLayout(gtx, assetType,
				layout.Rigid(func(gtx C) D {
					return cryptomaterial.LinearLayout{
						Width:       cryptomaterial.WrapContent,
						Height:      cryptomaterial.WrapContent,
						Orientation: layout.Horizontal,
					}.Layout2(gtx, swmp.openWalletSelector.Layout)
				}),
				layout.Flexed(1, func(gtx C) D {
					return layout.Center.Layout(gtx, func(gtx C) D {
						return cryptomaterial.LinearLayout{
							Width:       cryptomaterial.WrapContent,
							Height:      cryptomaterial.WrapContent,
							Orientation: layout.Horizontal,
							Alignment:   layout.Middle,
						}.Layout(gtx,
							layout.Rigid(func(gtx C) D {
								return swmp.walletDropdownLayout(gtx)
							}),
							layout.Flexed(1, func(gtx C) D {
								gtx.Constraints.Min.X = gtx.Constraints.Max.X
								layoutPosition := layout.E
								return layoutPosition.Layout(gtx, func(gtx C) D {
									return layout.Flex{}.Layout(gtx,
										layout.Rigid(func(gtx C) D {
											icon := swmp.Theme.Icons.VisibilityOffIcon
											if swmp.isBalanceHidden {
												icon = swmp.Theme.Icons.VisibilityIcon
											}
											return layout.Inset{
												Top:   values.MarginPadding5,
												Right: values.MarginPadding9,
											}.Layout(gtx, func(gtx C) D {
												return swmp.hideBalanceButton.Layout(gtx, swmp.Theme.NewIcon(icon).Layout20dp)
											})
										}),
										layout.Rigid(func(gtx C) D {
											orientation := layout.Horizontal
											if swmp.IsMobileView() {
												orientation = layout.Vertical
											}
											// VAR total + USD on the top row; SKA token
											// balances stacked beneath so a multi-coin
											// (or 0-VAR / SKA-only) wallet shows all
											// its holdings, not just the VAR figure.
											return layout.Flex{Axis: layout.Vertical, Alignment: layout.End}.Layout(gtx,
												layout.Rigid(func(gtx C) D {
													return cryptomaterial.LinearLayout{
														Width:       cryptomaterial.WrapContent,
														Height:      cryptomaterial.WrapContent,
														Orientation: orientation,
													}.Layout(gtx,
														layout.Rigid(swmp.totalAssetBalance),
														layout.Rigid(func(gtx C) D {
															if !swmp.isBalanceHidden {
																return swmp.LayoutUSDBalance(gtx)
															}
															return D{}
														}),
													)
												}),
												layout.Rigid(swmp.skaBalancesLayout),
											)
										}),
									)
								})
							}),
						)
					})
				}),
			)
		}),
		layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return swmp.Theme.Separator().Layout(gtx)
		}),
	)
}

func (swmp *SingleWalletMasterPage) walletDropdownLayout(gtx C) D {
	return layout.Flex{
		Axis:      layout.Horizontal,
		Alignment: layout.Middle,
	}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Inset{
				Left: values.MarginPadding10,
			}.Layout(gtx, swmp.walletDropdown.Layout)
		}),
		layout.Rigid(func(gtx C) D {
			if !swmp.selectedWallet.IsWatchingOnlyWallet() || swmp.IsMobileView() {
				return D{}
			}

			return layout.Inset{
				Left: values.MarginPadding10,
			}.Layout(gtx, func(gtx C) D {
				textSize := values.TextSizeTransform(swmp.Load.IsMobileView(), values.TextSize16)
				return components.WalletHighlightLabel(swmp.Theme, gtx, textSize, values.String(values.StrWatchOnly))
			})
		}),
	)
}

func (swmp *SingleWalletMasterPage) LayoutUSDBalance(gtx C) D {
	// Snapshot all balanceMu-guarded fields once so a concurrent
	// fetchExchangeRate/updateBalance goroutine can't tear the reads mid-frame.
	swmp.balanceMu.RLock()
	usdExchangeSet := swmp.usdExchangeSet
	usdExchangeRate := swmp.usdExchangeRate
	totalBalanceUSD := swmp.totalBalanceUSD
	varHidden := swmp.varHidden
	swmp.balanceMu.RUnlock()
	fetching := swmp.isFetchingExchangeRate.Load()
	// USD is derived from the VAR balance — hide it together with VAR.
	if !usdExchangeSet || varHidden {
		return D{}
	}
	switch {
	case fetching && usdExchangeRate == 0:
		gtx.Constraints.Max.Y = gtx.Dp(values.MarginPadding18)
		gtx.Constraints.Max.X = gtx.Constraints.Max.Y
		return layout.Inset{
			Top:  values.MarginPadding8,
			Left: values.MarginPadding5,
		}.Layout(gtx, func(gtx C) D {
			loader := material.Loader(swmp.Theme.Base)
			return loader.Layout(gtx)
		})
	case !fetching && usdExchangeRate == 0:
		return layout.Inset{
			Top:  values.MarginPadding7,
			Left: values.MarginPadding5,
		}.Layout(gtx, func(gtx C) D {
			return swmp.refreshExchangeRateBtn.Layout(gtx, swmp.Theme.NewIcon(swmp.Theme.Icons.NavigationRefresh).Layout16dp)
		})
	case len(totalBalanceUSD) > 0:
		textSize := values.TextSize20
		if swmp.Load.IsMobileView() {
			textSize = values.TextSize16
		}
		lbl := swmp.Theme.Label(textSize, fmt.Sprintf("/ %s", totalBalanceUSD))
		marginLeft := values.MarginPadding8
		if swmp.IsMobileView() {
			lbl = swmp.Theme.Label(textSize, totalBalanceUSD)
			marginLeft = 0
		}
		lbl.Color = swmp.Theme.Color.PageNavText
		inset := layout.Inset{Left: marginLeft}
		return inset.Layout(gtx, lbl.Layout)
	default:
		return D{}
	}
}

func (swmp *SingleWalletMasterPage) totalAssetBalance(gtx C) D {
	textSize := values.TextSize20
	if swmp.Load.IsMobileView() {
		textSize = values.TextSize16
	}
	// Snapshot under RLock — same reason as LayoutUSDBalance above:
	// updateBalance() can swap walletBalance from a sync-notification
	// goroutine while Layout reads it here.
	swmp.balanceMu.RLock()
	walletBalance := swmp.walletBalance
	varHidden := swmp.varHidden
	swmp.balanceMu.RUnlock()
	// The coin-visibility filter hides VAR entirely (a privacy-minded user
	// may hide every coin so the header reveals nothing).
	if varHidden {
		return D{}
	}
	if swmp.isBalanceHidden || walletBalance == nil {
		hiddenBalanceText := swmp.Theme.Label(textSize*0.8, "****************")
		return layout.Inset{Bottom: values.MarginPadding0, Top: values.MarginPadding5}.Layout(gtx, func(gtx C) D {
			hiddenBalanceText.Color = swmp.Theme.Color.PageNavText
			return hiddenBalanceText.Layout(gtx)
		})
	}
	// FormatTxAmount pads to ≥2 decimals so an empty wallet reads
	// "0.00 VAR" instead of a bare "0 VAR".
	return components.LayoutBalanceWithUnitSize(gtx, swmp.Load,
		dcr.FormatTxAmount(walletBalance.ToInt(), uint8(cointype.CoinTypeVAR)), textSize)
}

// skaBalancesLayout renders the wallet's SKA token balances (one per line)
// beneath the VAR total in the header. Hidden when the balance-privacy
// toggle is on, and a no-op when the wallet holds no SKA tokens.
func (swmp *SingleWalletMasterPage) skaBalancesLayout(gtx C) D {
	if swmp.isBalanceHidden {
		return D{}
	}
	swmp.balanceMu.RLock()
	lines := swmp.skaBalanceLines
	swmp.balanceMu.RUnlock()
	if len(lines) == 0 {
		return D{}
	}
	// Match the VAR total's font sizing exactly (see totalAssetBalance):
	// TextSize20 on desktop, TextSize16 on mobile. Using a smaller size here
	// made the SKA digits (both the big integer part and the small decimals)
	// visibly smaller than the VAR balance above them.
	textSize := values.TextSize20
	if swmp.Load.IsMobileView() {
		textSize = values.TextSize16
	}
	children := make([]layout.FlexChild, 0, len(lines))
	for _, line := range lines {
		line := line
		children = append(children, layout.Rigid(func(gtx C) D {
			// Same big/small split as the VAR total (formatBalance): integer
			// part + first two decimals at `textSize`, the remaining decimals
			// at 70%, and the " SKA1" unit at full size. LayoutBalanceWithUnitSize
			// uses Color.PageNavText — the same colour as the previous plain label.
			return layout.E.Layout(gtx, func(gtx C) D {
				return components.LayoutBalanceWithUnitSize(gtx, swmp.Load, line, textSize)
			})
		}))
	}
	// Alignment End right-aligns every coin line within the block — without
	// it a short line ("0.00 SKA2") sat left-ragged under a long SKA1 line.
	return layout.Flex{Axis: layout.Vertical, Alignment: layout.End}.Layout(gtx, children...)
}

func (swmp *SingleWalletMasterPage) postTransactionNotification(t *sharedW.Transaction) {
	var notification string
	wal := swmp.selectedWallet
	switch t.Type {
	case dcr.TxTypeRegular:
		if t.Direction != dcr.TxDirectionReceived {
			return
		}
		// Render the received amount via the lossless big-string channel
		// when present (SKA tx > ~9.22 SKA hits int64 clamp on t.Amount;
		// the AmountAtoms string carries the real value). FormatTxAmountBig
		// returns "X.YZ SYMBOL" — the translation key StrAmountReceived
		// expects amount and symbol as separate %s args ("Отримано %s %s"),
		// so split on the last space. Falls through to "<formatted>" + ""
		// if the symbol is somehow missing — still better than the legacy
		// strconv.FormatFloat(ToCoin()) which clamped SKA to MaxInt64/1e8
		// and emitted a garbage VAR-coin number.
		formatted := dcr.FormatTxAmountBig(t.AmountAtoms, t.Amount, t.CoinType)
		amountStr, symbolStr := formatted, ""
		if idx := strings.LastIndex(formatted, " "); idx >= 0 {
			amountStr = formatted[:idx]
			symbolStr = formatted[idx+1:]
		}
		notification = values.StringF(values.StrAmountReceived, amountStr, symbolStr)
	case dcr.TxTypeVote:
		reward := strconv.FormatFloat(wal.ToAmount(t.VoteReward).ToCoin(), 'f', -1, 64)
		notification = values.StringF(values.StrTicketVoted, reward)
	case dcr.TxTypeRevocation:
		notification = values.String(values.StrTicketRevoked)
	default:
		return
	}

	if swmp.AssetsManager.OpenedWalletsCount() > 1 {
		notification = fmt.Sprintf("[%s] %s", wal.GetWalletName(), notification)
	}

	initializeBeepNotification(notification)
}

func (swmp *SingleWalletMasterPage) postProposalNotification(propName string, status libutils.ProposalStatus) {
	proposalNotification := swmp.selectedWallet.ReadBoolConfigValueForKey(sharedW.ProposalNotificationConfigKey, false) ||
		!swmp.AssetsManager.IsPrivacyModeOn()
	if !proposalNotification {
		return
	}

	var notification string
	switch status {
	case libutils.ProposalStatusNewProposal:
		notification = values.StringF(values.StrProposalAddedNotif, propName)
	case libutils.ProposalStatusVoteStarted:
		notification = values.StringF(values.StrVoteStartedNotif, propName)
	case libutils.ProposalStatusVoteFinished:
		notification = values.StringF(values.StrVoteEndedNotif, propName)
	default:
		notification = values.StringF(values.StrNewProposalUpdate, propName)
	}
	initializeBeepNotification(notification)
}

func initializeBeepNotification(n string) {
	absoluteWdPath, err := utils.GetAbsolutePath()
	if err != nil {
		log.Error(err.Error())
	}

	err = beeep.Notify(values.String(values.StrAppWallet), n,
		filepath.Join(absoluteWdPath, "ui/assets/decredicons/ic_dcr_qr.png"))
	if err != nil {
		log.Info("could not initiate desktop notification, reason:", err.Error())
	}
}

// listenForNotifications starts a goroutine to watch for notifications
// and update the UI accordingly.
func (swmp *SingleWalletMasterPage) listenForNotifications(listenForSubpage func(int)) {
	syncProgressListener := &sharedW.SyncProgressListener{
		OnSyncCompleted: func() {
			swmp.updateBalance()
			swmp.ParentWindow().Reload()
		},
	}
	err := swmp.selectedWallet.AddSyncProgressListener(syncProgressListener, MainPageID)
	if err != nil {
		log.Errorf("Error adding sync progress listener: %v", err)
		return
	}

	txAndBlockNotificationListener := &sharedW.TxAndBlockNotificationListener{
		OnTransaction: func(walletID int, transaction *sharedW.Transaction) {
			swmp.updateBalance()
			if swmp.AssetsManager.IsTransactionNotificationsOn() {
				// TODO: SPV wallets only receive mempool tx ntfn for txs that
				// were broadcast by the wallet. We should probably be posting
				// desktop ntfns for txs received from external parties, which
				// will can be gotten from the OnTransactionConfirmed callback.
				swmp.postTransactionNotification(transaction)
			}
			swmp.ParentWindow().Reload()
			listenForSubpage(walletID)
		},
		OnTransactionConfirmed: func(walletID int, _ string, _ int32) {
			listenForSubpage(walletID)
		},
		// OnBlockAttached is also called whenever OnTransactionConfirmed is
		// called, so use OnBlockAttached. Also, OnTransactionConfirmed may be
		// called multiple times whereas OnBlockAttached is only called once.
		OnBlockAttached: func(_ int, _ int32) {
			beep := swmp.selectedWallet.ReadBoolConfigValueForKey(sharedW.BeepNewBlocksConfigKey, false)
			if beep {
				err := beeep.Beep(5, 1)
				if err != nil {
					log.Error(err.Error)
				}
			}

			swmp.updateBalance()
			swmp.ParentWindow().Reload()
		},
	}
	err = swmp.selectedWallet.AddTxAndBlockNotificationListener(txAndBlockNotificationListener, MainPageID)
	if err != nil {
		log.Errorf("Error adding tx and block notification listener: %v", err)
		return
	}

	if swmp.isGovernanceAPIAllowed() {
		proposalSyncCallback := func(propName string, status libutils.ProposalStatus) {
			// Post desktop notification for all events except the synced event.
			if status != libutils.ProposalStatusSynced {
				swmp.postProposalNotification(propName, status)
			}
		}
		err = swmp.AssetsManager.Politeia.AddSyncCallback(proposalSyncCallback, MainPageID)
		if err != nil {
			log.Errorf("Error adding politeia notification listener: %v", err)
			return
		}
	}

	// TODO: Register trade order ntfn listener and post desktop ntfns for all
	// events except the synced event.
}

func (swmp *SingleWalletMasterPage) stopNtfnListeners() {
	swmp.selectedWallet.RemoveSyncProgressListener(MainPageID)
	swmp.selectedWallet.RemoveTxAndBlockNotificationListener(MainPageID)
	swmp.AssetsManager.Politeia.RemoveSyncCallback(MainPageID)
}

func (swmp *SingleWalletMasterPage) showBackupInfo() {
	backupNowOrLaterModal := modal.NewCustomModal(swmp.Load).
		SetupWithTemplate(modal.WalletBackupInfoTemplate).
		SetCancelable(false).
		SetContentAlignment(layout.W, layout.W, layout.Center).
		CheckBox(swmp.checkBox, true).
		SetNegativeButtonText(values.String(values.StrBackupLater)).
		SetNegativeButtonCallback(func() {
			swmp.selectedWallet.SaveUserConfigValue(sharedW.SeedBackupNotificationConfigKey, true)
		}).
		PositiveButtonStyle(swmp.Load.Theme.Color.Primary, swmp.Load.Theme.Color.InvText).
		SetPositiveButtonText(values.String(values.StrBackupNow)).
		SetPositiveButtonCallback(func(_ bool, _ *modal.InfoModal) bool {
			swmp.backup(swmp.selectedWallet)
			return true
		})
	swmp.ParentWindow().ShowModal(backupNowOrLaterModal)
}

func (swmp *SingleWalletMasterPage) backup(wallet sharedW.Asset) {
	currentPage := swmp.ParentWindow().CurrentPageID()
	swmp.ParentWindow().Display(seedbackup.NewBackupInstructionsPage(swmp.Load, wallet, func(_ *load.Load, navigator app.WindowNavigator) {
		wallet.SaveUserConfigValue(sharedW.SeedBackupNotificationConfigKey, true)
		navigator.ClosePagesAfter(currentPage)
	}))
}
