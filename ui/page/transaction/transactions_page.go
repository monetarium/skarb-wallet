package transaction

import (
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/text"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/monetarium/monetarium-node/cointype"
	"github.com/monetarium/skarb-wallet/app"
	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/libwallet/txhelper"
	"github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/modal"
	"github.com/monetarium/skarb-wallet/ui/page/components"
	"github.com/monetarium/skarb-wallet/ui/values"
)

const (
	TransactionsPageID = "Transactions"

	// pageSize defines the number of transactions that can be fetched at ago.
	// 100 (was 30): the infinite-scroll component slides a 2×pageSize window over
	// the tx list and force-repositions on each page fetch, which is janky on
	// wallets with many txs. A larger page means the whole list fits in the
	// initial 2×pageSize load for typical wallets, so the slide/reposition never
	// engages and the native (virtualized) list scrolls smoothly to the top.
	pageSize = int32(100)
)

type (
	C = layout.Context
	D = layout.Dimensions
)

type multiWalletTx struct {
	*sharedW.Transaction
	walletID int
}

// txCacheKey identifies a distinct view of the transaction list. Any change to
// the selected wallets, the category tab, the status filter, the coin-type
// filter, the sort order or the search term produces a different key, so
// switching any of them is an automatic cache miss and triggers a fresh DB
// fetch (no stale cross-tab/cross-filter data is ever served).
//
// walletIDs is a comma-joined, ascending-sorted list of the selected wallet
// IDs ("" for the multi-wallet "All wallets" view, since that fans out over
// pg.assetWallets); statusLabel is the trailing-" (N)"-stripped status-dropdown
// label (it fully determines the backend txFilter without us having to fetch
// first); coinSymbol is the coin-type dropdown label ("" == "All assets");
// orderNewest mirrors the sort direction; searchKey is the search editor text.
type txCacheKey struct {
	walletIDs   string
	categoryTab int
	statusLabel string
	coinSymbol  string
	orderNewest bool
	searchKey   string
}

// txCacheWindow is a single cached (offset,pageSize) page result, holding the
// already coin-/regular-filtered slice exactly as fetchTransactions would
// return it for that window. Caching the post-filter slice (rather than the raw
// GetTransactionsRaw output) means a hit reproduces the uncached path verbatim,
// so paging/infinite-scroll semantics are preserved.
type txCacheWindow struct {
	txs []*multiWalletTx
}

// txDecodedCache memoizes the decoded tx windows for the current view key. It
// lives only on the UI thread: every read/write happens inside
// fetchTransactions (driven by the scroll component) or the notification path's
// dirty-flag handshake, never concurrently. Notification goroutines therefore
// never touch the slice Layout reads — they only set txCacheDirty (atomic), and
// the next UI-thread fetch rebuilds from scratch.
type txDecodedCache struct {
	key     txCacheKey
	valid   bool
	windows map[txCacheWindowKey]txCacheWindow
}

// txCacheWindowKey indexes a cached page by its (offset,pageSize) so the scroll
// component's exact request can be served back verbatim.
type txCacheWindowKey struct {
	offset   int32
	pageSize int32
}

// txTabs holds the transaction-category tabs: "Regular Transactions" and
// "Staking Transactions" (as in Cryptopower). The staking tab is only shown for
// DCR wallets (see Layout) and surfaces ticket/vote/revocation txs via the
// tabIndex==1 filter set in components.TxPageDropDownFields. The tab bar hides
// itself when only one category is present (len(txTabs) == 1).
var txTabs = []string{
	values.StrTxRegular,
	values.StrStakingTx,
	values.StrRewardTx,
}

// TransactionsPage shows transactions for a specific wallet or for all wallets.
// TODO: Add support for showing all wallet txs.
type TransactionsPage struct {
	*load.Load
	// GenericPageModal defines methods such as ID() and OnAttachedToNavigator()
	// that helps this Page satisfy the app.Page interface. It also defines
	// helper methods for accessing the PageNavigator that displayed this page
	// and the root WindowNavigator.
	*app.GenericPageModal

	separator cryptomaterial.Line

	selectedTxCategoryTab int

	statusDropDown   *cryptomaterial.DropDown
	orderDropDown    *cryptomaterial.DropDown
	walletDropDown   *cryptomaterial.DropDown
	coinTypeDropDown *cryptomaterial.DropDown
	filterBtn        *cryptomaterial.Clickable
	exportBtn        *cryptomaterial.Clickable
	isFilterOpen     bool
	searchEditor     cryptomaterial.Editor

	transactionList *cryptomaterial.ClickableList
	txFilter,
	previousTxFilter int32
	scroll *components.Scroll[*multiWalletTx]

	// txRefreshInFlight coalesces the burst of tx/block notifications a new
	// block produces (OnTransactionConfirmed fires per confirmed tx, each in its
	// own goroutine) into a single in-flight list refresh. Without it, the
	// concurrent loadNewItem resets race the scroll component's data / offset /
	// list.Position and break scrolling until the app is restarted.
	txRefreshInFlight atomic.Bool

	// txCache memoizes already-decoded tx windows so reopening the Transactions
	// page (and paging within it) doesn't re-read & re-decode the full set from
	// the local, already-validated DB on every open. It is read/written only on
	// the UI thread (inside fetchTransactions). §3: notification goroutines must
	// NOT mutate it; they set txCacheDirty instead and the next UI-thread fetch
	// discards the cache, so the slice Layout reads is never raced.
	txCache txDecodedCache

	// txCacheMu guards txCache. fetchTransactions is the scroll component's
	// queryFunc and is invoked from scroll goroutines (items_scroll.go), NOT
	// only the UI thread, so two fetches can overlap and race the windows map.
	txCacheMu sync.Mutex

	// txCacheDirty is set from the tx/block notification goroutines to mark the
	// memoized windows stale (new tx => append/changed rows; new block => reorg
	// safe full invalidate). It is cleared on the UI thread in fetchTransactions,
	// which then rebuilds the cache from the freshly fetched DB rows.
	txCacheDirty atomic.Bool

	txCategoryTab *cryptomaterial.SegmentedControl

	materialLoader material.LoaderStyle

	multiWalletLayout bool
	assetWallets      []sharedW.Asset
	selectedWallet    sharedW.Asset

	showLoader,
	dcrWalletExists,
	isShowTitle bool
}

func NewTransactionsPage(l *load.Load, wallet sharedW.Asset) *TransactionsPage {
	pg := &TransactionsPage{
		Load:             l,
		GenericPageModal: app.NewGenericPageModal(TransactionsPageID),
		separator:        l.Theme.Separator(),
		transactionList:  l.Theme.NewClickableList(layout.Vertical),
		txCategoryTab:    l.Theme.SegmentedControl(txTabs, cryptomaterial.SegmentTypeGroup),
		selectedWallet:   wallet,
		isShowTitle:      true,
	}

	pg.searchEditor = l.Theme.SearchEditor(new(widget.Editor), values.String(values.StrSearch), l.Theme.Icons.SearchIcon)
	pg.searchEditor.Editor.SingleLine = true
	pg.searchEditor.TextSize = pg.ConvertTextSize(l.Theme.TextSize)

	// init the wallet selector if no wallet was pre-selected
	if pg.selectedWallet == nil {
		pg.multiWalletLayout = true
		pg.initWalletSelector()
		pg.isShowTitle = false
	}

	pg.scroll = components.NewScroll(l, pageSize, pg.fetchTransactions)
	pg.filterBtn = l.Theme.NewClickable(false)
	pg.exportBtn = l.Theme.NewClickable(false)
	pg.transactionList.Radius = cryptomaterial.Radius(14)
	pg.transactionList.IsShadowEnabled = true

	pg.orderDropDown = l.Theme.DropdownWithCustomPos([]cryptomaterial.DropDownItem{
		{Text: values.String(values.StrNewest)},
		{Text: values.String(values.StrOldest)},
	}, values.ProposalDropdownGroup, 1, 0, false)
	pg.orderDropDown.Width = values.MarginPadding180 // 140 clipped "Найстаріші" in the expanded list
	pg.materialLoader = material.Loader(pg.Theme.Base)
	pg.orderDropDown.CollapsedLayoutTextDirection = layout.E
	settingCommonDropdown(pg.Theme, pg.orderDropDown)
	pg.orderDropDown.SetConvertTextSize(pg.ConvertTextSize)

	pg.initCoinTypeDropdown()

	return pg
}

// initCoinTypeDropdown rebuilds the asset filter dropdown from the selected
// wallet's active coin types. Index 0 is always the "All assets" sentinel
// (filter disabled); subsequent items are pulled from
// chaincfg.Params.GetActiveSKATypes() (plus VAR), so the dropdown reflects
// whatever is currently active on chain.
//
// Two correctness traps it threads:
//
//  1. Preserve current selection across rebuild. The legacy code rebuilt
//     a fresh DropDown on every walletDropDown.Changed event, defaulting
//     selectedIndex back to "All assets" — silently wiping any coin
//     filter the user had set whenever they switched wallets. We grab
//     the prior coin-type via prevCoinType and re-apply it after the
//     rebuild so the user's filter survives a wallet swap.
//
//  2. "All assets" sentinel survives localization. The label is now
//     `values.String(StrAllAssets)` — "Усі активи" in Ukrainian. The
//     downstream filter (filterByCoinType) uses SelectedIndex()==0 to
//     detect the sentinel, NOT a string comparison against "All assets".
//     A string match would silently break the moment the locale label
//     differs from the en-literal sentinel (the tx list would become
//     empty in non-EN locales).
func (pg *TransactionsPage) initCoinTypeDropdown() {
	prevCoinType := pg.selectedCoinType()

	items := []cryptomaterial.DropDownItem{{Text: values.String(values.StrAllAssets)}}
	if dcrAsset, ok := pg.selectedWallet.(*dcr.Asset); ok {
		// DisplayableCoinTypes filters by wallet activity so users don't
		// see SKA-n entries they've never received (bug #7).
		for _, ct := range dcrAsset.VisibleCoinTypes() {
			items = append(items, cryptomaterial.DropDownItem{Text: dcr.CoinSymbol(ct)})
		}
	} else {
		// Multi-wallet view (no specific wallet selected) — fall back to a
		// minimal list with VAR + SKA1 which is what is active on testnet and
		// mainnet at the moment. Refresh once a wallet is picked.
		items = append(items,
			cryptomaterial.DropDownItem{Text: dcr.CoinSymbol(cointype.CoinTypeVAR)},
			cryptomaterial.DropDownItem{Text: dcr.CoinSymbol(cointype.CoinType(1))},
		)
	}

	// Re-apply the user's prior coin filter when the new wallet still has
	// activity for it. Default to 0 (All assets) when the prior coin
	// isn't displayable for this wallet — better to show all txs than to
	// silently filter to a coin the user never had.
	selectedIndex := 0
	if prevCoinType != nil {
		want := dcr.CoinSymbol(*prevCoinType)
		for i, item := range items {
			if item.Text == want {
				selectedIndex = i
				break
			}
		}
	}

	pg.coinTypeDropDown = pg.Theme.DropdownWithCustomPos(items, values.CoinTypeDropdownGroup, 2, 0, false)
	pg.coinTypeDropDown.Width = values.MarginPadding180 // 130 clipped "Усі активи" in the expanded list
	pg.coinTypeDropDown.CollapsedLayoutTextDirection = layout.E
	pg.coinTypeDropDown.SetConvertTextSize(pg.ConvertTextSize)
	settingCommonDropdown(pg.Theme, pg.coinTypeDropDown)
	if selectedIndex > 0 {
		pg.coinTypeDropDown.SetSelectedValue(items[selectedIndex].Text)
	}
}

// selectedCoinType returns the currently filter-selected coin type, or nil
// when the "All assets" sentinel (index 0) is active. Used to preserve the
// user's choice across dropdown rebuilds and to drive filterByCoinType
// without depending on the localized label text matching.
func (pg *TransactionsPage) selectedCoinType() *cointype.CoinType {
	if pg.coinTypeDropDown == nil {
		return nil
	}
	idx := pg.coinTypeDropDown.SelectedIndex()
	if idx <= 0 {
		return nil
	}
	// Reverse-map the label back to a CoinType by walking the active
	// list in display order. The first item is the sentinel; subsequent
	// items mirror DisplayableCoinTypes()'s ordering (VAR first, then
	// SKA-n numeric). We keep the mapping label-driven (not index-direct)
	// because the dropdown items slice isn't preserved on rebuild.
	picked := pg.coinTypeDropDown.Selected()
	if picked == "" {
		return nil
	}
	if dcrAsset, ok := pg.selectedWallet.(*dcr.Asset); ok {
		for _, ct := range dcrAsset.VisibleCoinTypes() {
			if dcr.CoinSymbol(ct) == picked {
				ct := ct // capture loop var
				return &ct
			}
		}
	} else {
		// Multi-wallet fallback list mirrors initCoinTypeDropdown.
		for _, ct := range []cointype.CoinType{cointype.CoinTypeVAR, cointype.CoinType(1)} {
			if dcr.CoinSymbol(ct) == picked {
				ct := ct
				return &ct
			}
		}
	}
	return nil
}

func NewTransactionsPageWithType(l *load.Load, selectedTab int, wallet sharedW.Asset) *TransactionsPage {
	pg := &TransactionsPage{
		Load:             l,
		GenericPageModal: app.NewGenericPageModal(TransactionsPageID),
		separator:        l.Theme.Separator(),
		transactionList:  l.Theme.NewClickableList(layout.Vertical),
		txCategoryTab:    l.Theme.SegmentedControl(txTabs, cryptomaterial.SegmentTypeGroup),
		selectedWallet:   wallet,
		isShowTitle:      true,
	}
	// Guard against an out-of-range selectedTab: txTabs shrank to a single
	// entry when staking was removed, and callers may still pass a stale index.
	if selectedTab < 0 || selectedTab >= len(txTabs) {
		selectedTab = 0
	}
	pg.selectedTxCategoryTab = selectedTab
	pg.txCategoryTab.SetSelectedSegment(txTabs[selectedTab])
	pg.searchEditor = l.Theme.SearchEditor(new(widget.Editor), values.String(values.StrSearch), l.Theme.Icons.SearchIcon)
	pg.searchEditor.Editor.SingleLine = true
	pg.searchEditor.TextSize = pg.ConvertTextSize(l.Theme.TextSize)
	// init the wallet selector if no wallet was pre-selected
	if pg.selectedWallet == nil {
		pg.multiWalletLayout = true
		pg.initWalletSelector()
		pg.isShowTitle = false
	}
	pg.scroll = components.NewScroll(l, pageSize, pg.fetchTransactions)
	pg.filterBtn = l.Theme.NewClickable(false)
	pg.exportBtn = l.Theme.NewClickable(false)
	pg.transactionList.Radius = cryptomaterial.Radius(14)
	pg.transactionList.IsShadowEnabled = true
	pg.orderDropDown = l.Theme.DropdownWithCustomPos([]cryptomaterial.DropDownItem{
		{Text: values.String(values.StrNewest)},
		{Text: values.String(values.StrOldest)},
	}, values.ProposalDropdownGroup, 1, 0, false)
	pg.orderDropDown.Width = values.MarginPadding180 // 140 clipped "Найстаріші" in the expanded list
	pg.materialLoader = material.Loader(pg.Theme.Base)
	pg.orderDropDown.CollapsedLayoutTextDirection = layout.E
	settingCommonDropdown(pg.Theme, pg.orderDropDown)
	pg.orderDropDown.SetConvertTextSize(pg.ConvertTextSize)
	return pg
}

func (pg *TransactionsPage) DisableUniformTab() {
	pg.txCategoryTab.DisableUniform(true)
}

// OnNavigatedTo is called when the page is about to be displayed and
// may be used to initialize page features that are only relevant when
// the page is displayed.
// Part of the load.Page interface.
func (pg *TransactionsPage) OnNavigatedTo() {
	pg.refreshAvailableTxType()

	go pg.scroll.FetchScrollData(false, pg.ParentWindow(), false)
}

// initWalletSelector initializes the wallet selector dropdown to enable
// filtering transactions for a specific wallet when this page is used to
// display transactions for multiple wallets.
func (pg *TransactionsPage) initWalletSelector() {
	pg.assetWallets = pg.AssetsManager.AllWallets()
	if pg.txCategoryTab.SelectedSegment() != values.StrTxRegular {
		pg.assetWallets = pg.AssetsManager.AllDCRWallets()
	}

	if len(pg.assetWallets) > 1 {
		items := []cryptomaterial.DropDownItem{
			{
				Icon: pg.Theme.Icons.WalletIcon,
				Text: values.String(values.StrAllWallets),
			},
		}
		for _, wal := range pg.assetWallets {
			if !pg.dcrWalletExists && wal.GetAssetType() == utils.DCRWalletAsset {
				pg.dcrWalletExists = true
			}
			item := cryptomaterial.DropDownItem{
				Text: wal.GetWalletName(),
				Icon: pg.Theme.AssetIcon(wal.GetAssetType()),
			}
			items = append(items, item)
		}

		pg.walletDropDown = pg.Theme.DropDown(items, nil, values.WalletsDropdownGroup, false)
		settingCommonDropdown(pg.Theme, pg.walletDropDown)
		pg.walletDropDown.Width = values.MarginPadding150
		pg.walletDropDown.SetConvertTextSize(pg.ConvertTextSize)
	} else {
		pg.selectedWallet = pg.assetWallets[0]
	}
}

func (pg *TransactionsPage) getAssetType() utils.AssetType {
	if pg.selectedWallet == nil {
		return utils.DCRWalletAsset
	}
	return pg.selectedWallet.GetAssetType()
}

func (pg *TransactionsPage) refreshAvailableTxType() {
	items := []cryptomaterial.DropDownItem{}
	_, keysInfo := components.TxPageDropDownFields(pg.getAssetType(), pg.selectedTxCategoryTab)
	for _, name := range keysInfo {
		items = append(items, cryptomaterial.DropDownItem{Text: name})
	}
	pg.statusDropDown = pg.Theme.DropdownWithCustomPos(items, values.TxDropdownGroup, 0, 2, false)
	// Wide enough for the longest localized status label, incl. the "(NNN)" count
	// suffix on the regular tab ("Переказано (123)"). At explicit widths the
	// expanded list is drawn at exactly this width, so a too-narrow value clips
	// the option text (Gio MaxLines=1) — the truncation the user kept seeing.
	pg.statusDropDown.Width = values.MarginPadding218
	pg.statusDropDown.CollapsedLayoutTextDirection = layout.E
	pg.statusDropDown.SetConvertTextSize(pg.ConvertTextSize)
	settingCommonDropdown(pg.Theme, pg.statusDropDown)

	// Per-status "(N)" count badges were removed with the 3-tab reclassification:
	// the displayed list is now a UI post-filter (reclassifyByTab) over a COARSE
	// DB fetch, so CountTransactions — which runs the logical filter through
	// prepareTxQuery — no longer matches what's shown, and the badge would lie.
	// Plain labels only.
}

// currentTxCacheKey builds the cache key for the current view from the same
// inputs that determine the DB query and post-filtering. statusLabel mirrors the
// trailing-" (N)"-stripping loadTransactions applies before resolving the
// backend txFilter, so the label fully identifies the filter without first
// touching the DB.
func (pg *TransactionsPage) currentTxCacheKey(orderNewest bool) txCacheKey {
	var walletIDs string
	if pg.selectedWallet != nil {
		walletIDs = strconv.Itoa(pg.selectedWallet.GetWalletID())
	} else {
		ids := make([]int, 0, len(pg.assetWallets))
		for _, wal := range pg.assetWallets {
			ids = append(ids, wal.GetWalletID())
		}
		sort.Ints(ids)
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = strconv.Itoa(id)
		}
		walletIDs = strings.Join(parts, ",")
	}

	var statusLabel string
	if pg.statusDropDown != nil {
		statusLabel = pg.statusDropDown.Selected()
		if i := strings.LastIndex(statusLabel, " ("); i != -1 {
			statusLabel = statusLabel[:i]
		}
	}

	var coinSymbol string
	if pg.coinTypeDropDown != nil {
		coinSymbol = pg.coinTypeDropDown.Selected()
	}

	return txCacheKey{
		walletIDs:   walletIDs,
		categoryTab: pg.selectedTxCategoryTab,
		statusLabel: statusLabel,
		coinSymbol:  coinSymbol,
		orderNewest: orderNewest,
		searchKey:   pg.searchEditor.Editor.Text(),
	}
}

func (pg *TransactionsPage) fetchTransactions(offset, pageSize int32) (txs []*multiWalletTx, totalTxs int, isReset bool, err error) {
	isReset = pg.previousTxFilter != pg.txFilter
	if isReset {
		// reset the offset to zero
		offset = 0
		pg.previousTxFilter = pg.txFilter
	}

	orderNewest := pg.orderDropDown.Selected() != values.String(values.StrOldest)

	// Cache lookup (UI thread only). A pending notification marks the cache dirty
	// (see ListenForTxNotification); clearing the flag here and invalidating the
	// cache means the next fetch re-queries the freshly validated DB rows. A key
	// mismatch (tab/status/coin/order/search/wallet changed) is an automatic miss
	// and rebuilds, so no stale cross-filter data is served.
	key := pg.currentTxCacheKey(orderNewest)
	pg.txCacheMu.Lock()
	if pg.txCacheDirty.CompareAndSwap(true, false) {
		pg.txCache.valid = false
		pg.txCache.windows = nil
	}
	if pg.txCache.valid && pg.txCache.key == key && pg.txCache.windows != nil {
		if win, ok := pg.txCache.windows[txCacheWindowKey{offset: offset, pageSize: pageSize}]; ok {
			// Serve the memoized window verbatim — identical to the uncached path
			// for this exact (offset,pageSize), so paging semantics are preserved
			// and GetTransactionsRaw is skipped entirely.
			pg.txCacheMu.Unlock()
			return win.txs, len(win.txs), isReset, nil
		}
	} else {
		// Key changed (or invalidated): drop the stale windows and start fresh.
		pg.txCache = txDecodedCache{key: key, valid: true, windows: make(map[txCacheWindowKey]txCacheWindow)}
	}
	pg.txCacheMu.Unlock()

	wal := pg.selectedWallet
	if wal == nil {
		txs, totalTxs, err = pg.multiWalletTxns(offset, pageSize, orderNewest)
	} else {
		txs, totalTxs, err = pg.loadTransactions(wal, offset, pageSize, orderNewest)
	}

	txs = pg.filterByCoinType(txs)
	// Reclassify into the selected sub-tab (Regular / Staking / Reward). The DB
	// fetch above used a COARSE filter (coarseFetchFilter) because the reclass
	// predicates — default->default "split", the {coinbase,SF,vote,revocation}
	// reward union, and "ticket that voted" (spender lookup) — can't be a single
	// storm query. The exact set is cut here, over the decoded rows, for ALL
	// three tabs (the previous code only post-filtered the Regular tab, so the
	// Staking/Reward tabs leaked the full tx set).
	txs = pg.reclassifyByTab(txs)

	// Store the post-filter window under the current key. loadTransactions set
	// pg.txFilter (statusLabel -> txFilter) during the fetch; the label already in
	// the key tracks that 1:1, so the cache entry stays consistent. On error we
	// don't cache (so a later retry re-queries).
	if err == nil {
		pg.txCacheMu.Lock()
		if pg.txCache.valid && pg.txCache.key == key {
			if pg.txCache.windows == nil {
				pg.txCache.windows = make(map[txCacheWindowKey]txCacheWindow)
			}
			pg.txCache.windows[txCacheWindowKey{offset: offset, pageSize: pageSize}] = txCacheWindow{txs: txs}
		}
		pg.txCacheMu.Unlock()
	}

	return txs, len(txs), isReset, err
}

// coarseFetchFilter maps a logical (tab,status) filter to a DB-supported filter
// for the GetTransactionsRaw fetch. prepareTxQuery (walletdata) only understands
// the legacy filters (0-14); the reclassification filters (Split / StakeFee /
// *List / TicketVoted / Missed) fall through to "return everything" there, so we
// fetch a coarse superset and let reclassifyByTab cut the exact set over the
// decoded rows (split = default->default, the reward union, and "ticket that
// voted" via a spender lookup can't be a single storm query).
func coarseFetchFilter(logical int32) int32 {
	switch logical {
	case utils.TxFilterTicketVoted:
		return utils.TxFilterTickets // narrow to ticket purchases; voted-spender refined in UI
	case utils.TxFilterSplit, utils.TxFilterStakeFee, utils.TxFilterRegularList,
		utils.TxFilterStakingList, utils.TxFilterRewardList, utils.TxFilterMissed:
		return utils.TxFilterAll
	default:
		return logical // 0-14 are DB-supported and already exact
	}
}

// reclassifyByTab keeps only the txs that belong in the currently selected
// sub-tab + status. pg.txFilter holds the logical filter set by loadTransactions.
func (pg *TransactionsPage) reclassifyByTab(in []*multiWalletTx) []*multiWalletTx {
	out := in[:0]
	for _, mw := range in {
		if mw.Transaction != nil && pg.keepForTab(mw) {
			out = append(out, mw)
		}
	}
	return out
}

// keepForTab is the per-tab/status membership predicate (the reclassification
// core for tasks 7-12). Regular = everything that isn't a reward or a split;
// Staking = tickets + splits (votes/revocations moved to Reward); Reward =
// coinbase + stake-fee + vote + revocation.
func (pg *TransactionsPage) keepForTab(mw *multiWalletTx) bool {
	tx := mw.Transaction
	switch pg.selectedTxCategoryTab {
	case 0: // Regular: only plain regular/mixed movements. Tickets, votes,
		// revocations and coinbase have their own tx types (Staking/Reward);
		// stake-fees (SF) and split self-transfers are excluded explicitly. This
		// type restriction is what keeps the three tabs mutually exclusive — a
		// ticket must NOT also appear here.
		return (tx.Type == txhelper.TxTypeRegular || tx.Type == txhelper.TxTypeMixed) &&
			!tx.IsStakeFee && !isRegularTabSplitTx(tx)
	case 1: // Staking
		switch pg.txFilter {
		case utils.TxFilterSplit:
			return isRegularTabSplitTx(tx)
		case utils.TxFilterMissed:
			return false // missed tickets aren't detectable over SPV — always empty
		case utils.TxFilterTicketVoted:
			return tx.Type == txhelper.TxTypeTicketPurchase && pg.ticketVoted(mw)
		case utils.TxFilterStakingList: // "All" = tickets + splits
			return tx.Type == txhelper.TxTypeTicketPurchase || isRegularTabSplitTx(tx)
		default: // Unmined / Immature / Live / Expired — coarse filter already exact
			return tx.Type == txhelper.TxTypeTicketPurchase
		}
	case 2: // Reward
		return isRewardTx(tx)
	}
	return true
}

// isRewardTx reports whether tx belongs to the Reward tab: PoW coinbase,
// stake-fee (SF), vote, or revocation.
func isRewardTx(tx *sharedW.Transaction) bool {
	if tx.IsStakeFee {
		return true
	}
	switch tx.Type {
	case txhelper.TxTypeCoinBase, txhelper.TxTypeVote, txhelper.TxTypeRevocation:
		return true
	}
	return false
}

// ticketVoted reports whether a ticket-purchase tx's spender is a vote (the
// ticket voted, as opposed to revoked). One indexed DB lookup per ticket — used
// only by the "Voted" filter, and the result rides along in the tx cache.
func (pg *TransactionsPage) ticketVoted(mw *multiWalletTx) bool {
	_, wal := pg.txAndWallet(mw)
	dcrAsset, ok := wal.(*dcr.Asset)
	if !ok {
		return false
	}
	spender, err := dcrAsset.TicketSpender(mw.Transaction.Hash)
	if err != nil || spender == nil {
		return false
	}
	return spender.Type == txhelper.TxTypeVote
}

// isRegularTabSplitTx reports whether tx is a split transaction: mined and with
// every input and output on the wallet's default account (number 0). Mirrors the
// backend isSplitTx classifier used by TxFilterSplit / TxFilterRegularList.
func isRegularTabSplitTx(tx *sharedW.Transaction) bool {
	if tx.Type != txhelper.TxTypeRegular || tx.BlockHeight < 0 {
		return false
	}
	if len(tx.Inputs) == 0 || len(tx.Outputs) == 0 {
		return false
	}
	for _, in := range tx.Inputs {
		if in.AccountNumber != 0 {
			return false
		}
	}
	for _, out := range tx.Outputs {
		if out.AccountNumber != 0 {
			return false
		}
	}
	return true
}

// filterByCoinType drops any transactions whose CoinType differs from the
// one selected in the coin-type filter dropdown. The "All assets"
// sentinel (index 0) skips the filter entirely.
//
// Sentinel detection is index-based, not text-based: comparing against the
// literal "All assets" used to silently break in non-EN locales (the
// label is `values.String(StrAllAssets)`, which is "Усі активи" in
// Ukrainian — the string comparison would never match the sentinel and
// the list would render as empty).
func (pg *TransactionsPage) filterByCoinType(in []*multiWalletTx) []*multiWalletTx {
	picked := pg.selectedCoinType()
	// "All assets" still excludes coins the user hid via the visibility
	// filter — hidden coins must not surface anywhere, including here. Build
	// the visible set for the selected wallet (single-wallet view); when no
	// single wallet is selected (multi-wallet) we can't scope per coin
	// reliably, so fall through to no filtering.
	var visible map[cointype.CoinType]bool                 // single-wallet visible set
	var visibleByWallet map[int]map[cointype.CoinType]bool // multi-wallet, keyed by walletID
	if picked == nil {
		if dcrAsset, ok := pg.selectedWallet.(*dcr.Asset); ok {
			visible = make(map[cointype.CoinType]bool)
			for _, ct := range dcrAsset.VisibleCoinTypes() {
				visible[ct] = true
			}
		} else {
			// Multi-wallet "All wallets" view: the hide filter is per-wallet,
			// so scope each tx against its OWN wallet's visible set.
			visibleByWallet = make(map[int]map[cointype.CoinType]bool)
			for _, wal := range pg.assetWallets {
				dcrAsset, ok := wal.(*dcr.Asset)
				if !ok {
					continue
				}
				set := make(map[cointype.CoinType]bool)
				for _, ct := range dcrAsset.VisibleCoinTypes() {
					set[ct] = true
				}
				visibleByWallet[wal.GetWalletID()] = set
			}
		}
	}
	out := in[:0]
	for _, mw := range in {
		if mw.Transaction == nil {
			continue
		}
		ct := cointype.CoinType(mw.CoinType)
		if picked != nil {
			if ct == *picked {
				out = append(out, mw)
			}
			continue
		}
		if visible != nil {
			if visible[ct] {
				out = append(out, mw)
			}
			continue
		}
		// multi-wallet: a wallet with no known visible set (non-DCR) passes
		// through; otherwise the tx must be a visible coin of its own wallet.
		if set, ok := visibleByWallet[mw.walletID]; !ok || set[ct] {
			out = append(out, mw)
		}
	}
	return out
}

func (pg *TransactionsPage) multiWalletTxns(offset, pageSize int32, newestFirst bool) ([]*multiWalletTx, int, error) {
	allTxs := make([]*multiWalletTx, 0)
	for _, wal := range pg.assetWallets {
		if !wal.IsSynced() {
			continue // skip wallets that are not synced
		}

		txs, _, err := pg.loadTransactions(wal, offset, pageSize, newestFirst)
		if err != nil {
			return nil, 0, err
		}
		allTxs = append(allTxs, txs...)
	}

	sort.Slice(allTxs, func(i, j int) bool {
		if newestFirst {
			return allTxs[i].Timestamp > allTxs[j].Timestamp
		}
		return allTxs[i].Timestamp < allTxs[j].Timestamp
	})

	// TODO: remove after PR 268  // Improve infinite scroll list functionality is merged
	if len(allTxs) > int(pageSize) {
		allTxs = allTxs[:int(pageSize)]
	}

	return allTxs, len(allTxs), nil
}

func (pg *TransactionsPage) loadTransactions(wal sharedW.Asset, offset, pageSize int32, newestFirst bool) ([]*multiWalletTx, int, error) {
	mapInfo, _ := components.TxPageDropDownFields(wal.GetAssetType(), pg.selectedTxCategoryTab)
	if len(mapInfo) < 1 {
		err := fmt.Errorf("unable to resolve asset filters for asset type (%v)", wal.GetAssetType())
		return nil, -1, err
	}

	// Strip only the trailing " (N)" count suffix (regular tab appends it); keep
	// internal spaces so multi-word status labels like the staking "Не дозріле"
	// (Immature) still match a mapInfo key. A plain Cut on the first space broke
	// those labels -> "unsupported field" and an empty list.
	selectedVal := pg.statusDropDown.Selected()
	if i := strings.LastIndex(selectedVal, " ("); i != -1 {
		selectedVal = selectedVal[:i]
	}
	txFilter, ok := mapInfo[selectedVal]
	if !ok {
		err := fmt.Errorf("unsupported field(%v) for asset type(%v) and txCategoryTab index(%d) found",
			selectedVal, wal.GetAssetType(), pg.selectedTxCategoryTab)
		return nil, -1, err
	}
	pg.txFilter = txFilter // logical filter (drives reclassifyByTab + cache key)
	searchKey := pg.searchEditor.Editor.Text()
	// Fetch a DB-supported coarse superset; reclassifyByTab refines it (the
	// reclassification filters aren't understood by prepareTxQuery).
	walletTxs, err := wal.GetTransactionsRaw(offset, pageSize, coarseFetchFilter(txFilter), newestFirst, searchKey)
	if err != nil {
		err = fmt.Errorf("error loading transactions: %v", err)
	}

	txs := make([]*multiWalletTx, 0)
	for i := range walletTxs {
		txs = append(txs, &multiWalletTx{walletTxs[i], wal.GetWalletID()})
	}

	return txs, len(txs), err
}

func settingCommonDropdown(t *cryptomaterial.Theme, dropdown *cryptomaterial.DropDown) {
	dropdown.FontWeight = font.SemiBold
	dropdown.Hoverable = false
	dropdown.SelectedItemIconColor = &t.Color.Primary
	dropdown.ExpandedLayoutInset = layout.Inset{Top: values.MarginPadding35}
	dropdown.MakeCollapsedLayoutVisibleWhenExpanded = true
	dropdown.Background = &t.Color.Gray4
}

// Layout draws the page UI components into the provided layout context
// to be eventually drawn on screen.
// Part of the load.Page interface.
func (pg *TransactionsPage) Layout(gtx C) D {
	isDCRAssetSelected := pg.selectedWallet != nil && pg.selectedWallet.GetAssetType() == utils.DCRWalletAsset
	// The category tab bar is only meaningful when there is more than one
	// category. Staking was removed in Skarb v1 (Monetarium has no staking),
	// leaving a single "regular" category, so the bar is hidden entirely.
	if len(txTabs) > 1 && (isDCRAssetSelected || (pg.dcrWalletExists && pg.selectedWallet == nil)) {
		// Only show tx category navigation txCategoryTab for DCR wallets.
		return pg.txCategoryTab.Layout(gtx, pg.layoutContent, pg.IsMobileView())
	}

	return cryptomaterial.UniformPadding(gtx, func(gtx C) D {
		return pg.layoutContent(gtx)
	}, pg.IsMobileView())
}

func (pg *TransactionsPage) walletNotReady() bool {
	return pg.selectedWallet != nil && (!pg.selectedWallet.IsSynced() || pg.selectedWallet.IsRescanning())
}

func (pg *TransactionsPage) layoutContent(gtx C) D {
	if pg.walletNotReady() && pg.walletDropDown == nil {
		return pg.txListLayout(gtx) // nothing else to display on this page at this time
	}

	pageElements := []layout.StackChild{
		layout.Expanded(pg.txListLayout),
	}

	pageElements = append(pageElements, layout.Expanded(pg.dropdownLayout))

	// NOTE: a per-frame pg.ParentWindow().Reload() used to live here "to display
	// the updated txType dropdown". It forced a relayout EVERY frame, and since
	// txListLayout calls scroll.OnScrollChangeListener each frame — and the scroll
	// component force-sets list.Position.Offset to mid-list on every fetch — the
	// two fed each other: the user was yanked deeper and the page "reloaded"
	// endlessly, making it impossible to reach the newest (top) transactions. The
	// status-dropdown counts already trigger their own Reload from the counting
	// goroutine (refreshAvailableTxType), so this per-frame Reload was redundant.

	return layout.Stack{}.Layout(gtx, pageElements...)
}

func (pg *TransactionsPage) dropdownLayout(gtx C) D {
	return layout.Stack{}.Layout(gtx,
		layout.Stacked(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Inset{Top: values.MarginPadding40}.Layout(gtx, pg.rightDropdown)
		}),
		layout.Expanded(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return pg.leftDropdown(gtx)
		}),
	)
}

func (pg *TransactionsPage) leftDropdown(gtx C) D {
	showOverlay := pg.walletNotReady() && pg.multiWalletLayout
	return layout.Flex{Spacing: layout.SpaceBetween}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			if pg.isShowTitle && pg.IsMobileView() {
				lbl := pg.Theme.Label(values.TextSize16, values.String(values.StrTransactions))
				lbl.Font.Weight = font.Bold
				return layout.Inset{Top: values.MarginPadding4}.Layout(gtx, lbl.Layout)
			}
			if pg.walletDropDown == nil {
				return D{}
			}
			return layout.W.Layout(gtx, pg.walletDropDown.Layout)

		}),
		layout.Rigid(func(gtx C) D {
			if showOverlay {
				return D{}
			}
			icon := pg.Theme.Icons.FilterOffImgIcon
			if pg.isFilterOpen {
				icon = pg.Theme.Icons.FilterImgIcon
			}
			return layout.Inset{Top: values.MarginPadding8}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						margin := values.MarginPadding20
						if pg.IsMobileView() {
							margin = values.MarginPadding12
						}
						return layout.Inset{Right: margin}.Layout(gtx, func(gtx C) D {
							return pg.buttonWrap(gtx, pg.filterBtn, icon, values.String(values.StrFilter))
						})
					}),
					layout.Rigid(func(gtx C) D {
						// TODO: Enable on mobile
						if pg.IsMobileView() {
							return D{}
						}
						return pg.buttonWrap(gtx, pg.exportBtn, pg.Theme.Icons.ShareIcon, values.String(values.StrExport))
					}),
				)
			})
		}),
	)
}

func (pg *TransactionsPage) buttonWrap(gtx C, clickable *cryptomaterial.Clickable, icon *cryptomaterial.Image, title string) D {
	return cryptomaterial.LinearLayout{
		Width:      cryptomaterial.WrapContent,
		Height:     cryptomaterial.WrapContent,
		Background: pg.Theme.Color.DefaultThemeColors().SurfaceHighlight,
		Clickable:  clickable,
		Alignment:  layout.Middle,
	}.Layout(gtx,
		layout.Rigid(icon.Layout16dp),
		layout.Rigid(func(gtx C) D {
			if pg.IsMobileView() {
				return D{}
			}
			lbl := pg.Theme.Label(pg.ConvertTextSize(values.TextSize14), title)
			lbl.Font.Weight = font.Bold
			lbl.Color = pg.Theme.Color.GrayText1
			return layout.Inset{Left: values.MarginPadding2}.Layout(gtx, lbl.Layout)
		}),
	)
}

func (pg *TransactionsPage) rightDropdown(gtx C) D {
	if !pg.isFilterOpen {
		return D{}
	}
	return layout.E.Layout(gtx, func(gtx C) D {
		return layout.Flex{}.Layout(gtx,
			layout.Rigid(pg.statusDropDown.Layout),
			layout.Rigid(func(gtx C) D {
				if pg.coinTypeDropDown == nil {
					return D{}
				}
				return pg.coinTypeDropDown.Layout(gtx)
			}),
			layout.Rigid(pg.orderDropDown.Layout),
		)
	})
}

func (pg *TransactionsPage) txListLayout(gtx C) D {
	pg.scroll.OnScrollChangeListener(pg.ParentWindow())
	txListWidget := func(gtx C) D {
		marginTop := values.MarginPadding50
		if pg.isFilterOpen {
			marginTop = values.MarginPadding80
		}
		return layout.Inset{Top: marginTop}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					if !pg.isFilterOpen {
						return D{}
					}
					return layout.Inset{Bottom: values.MarginPadding16}.Layout(gtx, pg.searchEditor.Layout)
				}),
				layout.Rigid(func(gtx C) D {
					itemCount := pg.scroll.ItemsCount()
					card := pg.Theme.Card()
					// return "No transactions yet" text if there are no transactions
					if itemCount == 0 {
						padding := values.MarginPadding16
						txt := pg.Theme.Body1(values.String(values.StrNoTransactions))
						txt.TextSize = pg.ConvertTextSize(values.TextSize14)
						txt.Color = pg.Theme.Color.GrayText3
						return card.Layout(gtx, func(gtx C) D {
							gtx.Constraints.Min.X = gtx.Constraints.Max.X
							return layout.Center.Layout(gtx, func(gtx C) D {
								return layout.Inset{Top: padding, Bottom: padding}.Layout(gtx, txt.Layout)
							})
						})
					}

					// Show the list as soon as the transactions are fetched
					// (itemCount >= 0). Don't also wait on pg.showLoader: that stays
					// true until the background per-status count computation
					// (refreshAvailableTxType) finishes, which only feeds the "(N)"
					// numbers in the status dropdown — making the list appear to open
					// slowly even when nothing changed. The counts fill in on their
					// own Reload a moment later.
					if itemCount == -1 {
						gtx.Constraints.Min.X = gtx.Constraints.Max.X
						return layout.Center.Layout(gtx, pg.materialLoader.Layout)
					}

					return pg.scroll.List().Layout(gtx, 1, func(gtx C, _ int) D {
						return layout.Inset{Right: values.MarginPadding2}.Layout(gtx, func(gtx C) D {
							return card.Layout(gtx, func(gtx C) D {
								padding := values.MarginPaddingTransform(pg.IsMobileView(), values.MarginPadding16)
								return layout.UniformInset(padding).Layout(gtx, func(gtx C) D {
									wallTxs := pg.scroll.FetchedData()
									return pg.transactionList.Layout(gtx, len(wallTxs), func(gtx C, index int) D {
										tx, wal := pg.txAndWallet(wallTxs[index])
										return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
											layout.Rigid(func(gtx C) D {
												hideAssetInfo := pg.selectedWallet != nil
												return components.LayoutTransactionRow(gtx, pg.Load, wal, tx, hideAssetInfo)
											}),
											layout.Rigid(func(gtx C) D {
												// No divider for last row
												if index == len(wallTxs)-1 {
													return D{}
												}

												gtx.Constraints.Min.X = gtx.Constraints.Max.X
												separator := pg.Theme.Separator()
												return layout.E.Layout(gtx, func(gtx C) D {
													// Show bottom divider for all rows except last
													return layout.Inset{Left: values.MarginPadding32}.Layout(gtx, separator.Layout)
												})
											}),
										)
									})
								})
							})
						})
					})
				}),
			)
		})
	}

	showOverlay := pg.walletNotReady() && pg.multiWalletLayout
	if !showOverlay {
		return txListWidget(gtx)
	}

	gtx = gtx.Disabled()
	return layout.Stack{Alignment: layout.S}.Layout(gtx,
		layout.Expanded(txListWidget),
		layout.Stacked(func(gtx C) D {
			gtx = gtx.Disabled()
			overlayColor := pg.Theme.Color.Gray3
			overlayColor.A = 220
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			gtx.Constraints.Min.Y = gtx.Constraints.Max.Y - gtx.Dp(values.MarginPadding60)
			cryptomaterial.Fill(gtx, overlayColor)

			lbl := pg.Theme.Label(pg.ConvertTextSize(values.TextSize20), values.String(values.StrFunctionUnavailable))
			lbl.Font.Weight = font.SemiBold
			lbl.Color = pg.Theme.Color.PageNavText
			lbl.Alignment = text.Middle
			return cryptomaterial.CentralizeWidget(gtx, lbl.Layout)
		}),
	)
}

func (pg *TransactionsPage) txAndWallet(mtx *multiWalletTx) (*sharedW.Transaction, sharedW.Asset) {
	return mtx.Transaction, pg.AssetsManager.WalletWithID(mtx.walletID)
}

// HandleUserInteractions is called just before Layout() to determine
// if any user interaction recently occurred on the page and may be
// used to update the page's UI components shortly before they are
// displayed.
// Part of the load.Page interface.
func (pg *TransactionsPage) HandleUserInteractions(gtx C) {
	if pg.statusDropDown.Changed(gtx) {
		go pg.scroll.FetchScrollData(false, pg.ParentWindow(), true)
	}

	if pg.walletDropDown != nil && pg.walletDropDown.Changed(gtx) {
		assetIndex := pg.walletDropDown.SelectedIndex()
		// The "All Wallets" dropdown item is the first in the dropdown list.
		if assetIndex == 0 {
			pg.selectedWallet = nil
		} else {
			// Assets added as dropdown items have an index of
			// actuallyIndex+1 due to the "All Wallets" dropdown item.
			assetIndex--
			pg.selectedWallet = pg.assetWallets[assetIndex]
		}
		pg.refreshAvailableTxType()
		pg.initCoinTypeDropdown() // refresh asset filter for the newly selected wallet
		go pg.scroll.FetchScrollData(false, pg.ParentWindow(), true)
	}

	if pg.coinTypeDropDown != nil && pg.coinTypeDropDown.Changed(gtx) {
		go pg.scroll.FetchScrollData(false, pg.ParentWindow(), true)
	}

	if clicked, selectedItem := pg.transactionList.ItemClicked(); clicked {
		transactions := pg.scroll.FetchedData()
		tx, wal := pg.txAndWallet(transactions[selectedItem])
		pg.ParentNavigator().Display(NewTransactionDetailsPage(pg.Load, wal, tx))
	}

	dropDownList := []*cryptomaterial.DropDown{pg.statusDropDown}
	if pg.walletDropDown != nil {
		dropDownList = append(dropDownList, pg.walletDropDown)
	}
	if pg.coinTypeDropDown != nil {
		dropDownList = append(dropDownList, pg.coinTypeDropDown)
	}
	cryptomaterial.DisplayOneDropdown(gtx, dropDownList...)

	if pg.txCategoryTab.Changed() {
		pg.selectedTxCategoryTab = pg.txCategoryTab.SelectedIndex()
		if pg.multiWalletLayout {
			pg.initWalletSelector()
		}

		if pg.walletDropDown != nil && pg.walletDropDown.SelectedIndex() <= 0 {
			pg.selectedWallet = nil
		}

		pg.refreshAvailableTxType()
		go pg.scroll.FetchScrollData(false, pg.ParentWindow(), true)
	}

	if pg.filterBtn.Clicked(gtx) {
		pg.isFilterOpen = !pg.isFilterOpen
	}

	if pg.exportBtn.Clicked(gtx) {
		exportModal := modal.NewCustomModal(pg.Load).
			Title(values.String(values.StrExportTransaction)).
			Body(values.String(values.StrExportTransactionsMsg)).
			SetNegativeButtonText(values.String(values.StrCancel)).
			SetPositiveButtonText(values.String(values.StrExport)).
			SetPositiveButtonCallback(func(_ bool, _ *modal.InfoModal) bool {
				assets := []sharedW.Asset{pg.selectedWallet}
				if pg.selectedWallet == nil {
					assets = pg.assetWallets
				}
				go func() {
					fileName := filepath.Join(pg.AssetsManager.RootDir(), "exports", fmt.Sprintf("transaction_export_%d.csv", time.Now().Unix()))
					err := exportTxs(assets, fileName)
					if err != nil {
						errModal := modal.NewErrorModal(pg.Load, fmt.Errorf("error exporting your wallet(s) transactions: %v", err).Error(), modal.DefaultClickFunc())
						pg.ParentWindow().ShowModal(errModal)
						return
					}

					infoModal := modal.NewSuccessModal(pg.Load, values.StringF(values.StrExportTransactionSuccessMsg, fileName), modal.DefaultClickFunc())
					pg.ParentWindow().ShowModal(infoModal)
				}()
				return true
			})
		pg.ParentWindow().ShowModal(exportModal)
	}

	if pg.orderDropDown.Changed(gtx) {
		pg.scroll.FetchScrollData(false, pg.ParentWindow(), true)
	}

	// When focus on search editor
	if gtx.Source.Focused(pg.searchEditor.Editor) {
		if pg.searchEditor.Changed() {
			pg.scroll.FetchScrollData(false, pg.ParentWindow(), true)
		}
	}
}

func exportTxs(assets []sharedW.Asset, fileName string) error {
	if err := os.MkdirAll(filepath.Dir(fileName), utils.UserFilePerm); err != nil {
		return fmt.Errorf("os.MkdirAll error: %w", err)
	}

	var success bool
	defer func() {
		if !success {
			os.Remove(fileName)
		}
	}()

	f, err := os.Create(fileName)
	if err != nil {
		return fmt.Errorf("os.Create error: %w", err)
	}
	defer f.Close()

	headers := []string{values.String(values.StrTime), values.String(values.StrHash), values.String(values.StrType), values.String(values.StrDirection), values.String(values.StrFee), values.String(values.StrAmount)}

	writer := csv.NewWriter(f)
	writer.UseCRLF = runtime.GOOS == "windows"
	err = writer.Write(headers)
	if err != nil {
		return fmt.Errorf("csv.Writer.Write error: %w", err)
	}

	for _, a := range assets {
		txs, err := a.GetTransactionsRaw(0, math.MaxInt32, utils.TxFilterAll, true, "")
		if err != nil {
			return fmt.Errorf("wallet.GetTransactionsRaw error: %w", err)
		}

		// Write txs to file.
		for _, tx := range txs {
			err := writer.Write([]string{
				time.Unix(tx.Timestamp, 0).String(),
				tx.Hash,
				tx.Type,
				txhelper.TxDirectionString(tx.Direction),
				// CSV row formatted under the tx's actual CoinType so SKA
				// exports don't silently rebrand to "X.XX VAR" via
				// dcrutil.Amount.String(). We use the *Big formatter so
				// that SKA amounts larger than int64 (a single UTXO past
				// ~9.22 SKA) and SKA mempool fees (where the int64
				// channel can be wire-stripped to zero) export with the
				// lossless decimal-string field rather than the
				// truncated int64 mirror.
				dcr.FormatTxAmountBig(tx.FeeAtoms, tx.Fee, tx.CoinType),
				dcr.FormatTxAmountBig(tx.AmountAtoms, tx.Amount, tx.CoinType),
			})
			if err != nil {
				return fmt.Errorf("csv.Writer.Write error: %v", err)
			}

			writer.Flush()
			if err = writer.Error(); err != nil {
				return fmt.Errorf("csv.Writer error: %w", err)
			}
		}
	}

	success = true
	return nil
}

// Update transaction list when there is new tx or new confirmed status.
//
// This is invoked once per tx/block notification, each in its own goroutine
// (single_wallet_main_page.listenForNotifications -> ListenNewTxForSubPage). A
// new block confirms many txs at once, so this fires in a burst of concurrent
// goroutines. FetchScrollDataHandler(loadNewItem=true) resets and re-fetches
// the whole list; running several of those at once races the scroll
// component's data / offset / list.Position and leaves scrolling broken until
// the app is restarted — exactly the "scroll breaks after a new block" report.
// Coalesce the burst: let one refresh run to completion and drop the rest (the
// surviving refresh re-queries the latest state anyway).
func (pg *TransactionsPage) ListenForTxNotification(walletID int) {
	if pg.selectedWallet != nil && pg.selectedWallet.GetWalletID() != walletID {
		return
	}
	if !pg.txRefreshInFlight.CompareAndSwap(false, true) {
		return
	}
	defer pg.txRefreshInFlight.Store(false)
	// §3: do NOT mutate the cache slice from this goroutine (Layout reads it on
	// the UI thread). Just mark it dirty; the coalesced refresh below re-runs
	// fetchTransactions on the UI thread, which clears the flag and rebuilds from
	// the freshly validated DB rows. This fires for both OnTransaction (new/
	// changed rows) and OnBlockAttached/OnTransactionConfirmed (full invalidate =
	// reorg-safe), exactly the events the scroll refresh already coalesces.
	pg.txCacheDirty.Store(true)
	pg.scroll.FetchScrollDataHandler(false, pg.ParentWindow(), false, true)
}

func (pg *TransactionsPage) stopTxNotificationsListener() {
	for _, w := range pg.assetWallets {
		w.RemoveTxAndBlockNotificationListener(TransactionsPageID)
	}
}

// OnNavigatedFrom is called when the page is about to be removed from
// the displayed window. This method should ideally be used to disable
// features that are irrelevant when the page is NOT displayed.
// NOTE: The page may be re-displayed on the app's window, in which case
// OnNavigatedTo() will be called again. This method should not destroy UI
// components unless they'll be recreated in the OnNavigatedTo() method.
// Part of the load.Page interface.
func (pg *TransactionsPage) OnNavigatedFrom() {
	pg.stopTxNotificationsListener()
}
