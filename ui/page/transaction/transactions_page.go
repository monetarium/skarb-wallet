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

	// pageSize is the scroll component's window unit. fetchTransactions no longer
	// pages against it: it loads the COMPLETE post-filtered set and hands it back
	// on the first page (see fetchTransactions / loadAllTransactions), so the
	// component's slide/reposition stays disengaged (window anchored at
	// idxStart==0) and Gio virtualizes the full list. Kept at 100 because the
	// component still uses it for internal scroll-view bookkeeping.
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

// txDecodedCache memoizes the COMPLETE post-filtered transaction set for the
// current view key. fetchTransactions loads the whole set once (paging the DB
// in chunks via loadAllTransactions) and serves it from here on every
// subsequent page request, so reopening the page or scrolling within it never
// re-reads & re-decodes the already-validated local DB. A notification marks it
// dirty (atomic) and the next UI-thread fetch rebuilds; any key change
// (tab/status/coin/order/search/wallet) is an automatic miss.
type txDecodedCache struct {
	key   txCacheKey
	valid bool
	all   []*multiWalletTx
}

// txTabs holds the transaction-category tabs: "Regular Transactions" and
// "Staking Transactions" (as in Cryptopower). The staking tab is only shown for
// DCR wallets (see Layout) and surfaces ticket/vote/revocation txs via the
// tabIndex==1 filter set in components.TxPageDropDownFields. The tab bar hides
// itself when only one category is present (len(txTabs) == 1).
// The Staking Activities tab was removed from this page — staking lives on
// the dedicated Staking page. Position 1 is now the Reward tab; tableTab
// maps page positions back onto TxPageDropDownFields' semantic indices.
var txTabs = []string{
	values.StrTxRegular,
	values.StrRewardTx,
}

// tableTab maps this page's tab position to the semantic tab index used by
// components.TxPageDropDownFields (0 Regular, 1 Staking, 2 Reward).
func tableTab(categoryTab int) int {
	if categoryTab == 1 {
		return 2
	}
	return 0
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
	exportBtn        *cryptomaterial.Clickable
	searchEditor     cryptomaterial.Editor

	// lastViewedTxHash marks the row whose details card was opened most
	// recently, so after closing the card the user can still see which
	// transaction they were just looking at. Replaced when another card
	// is opened; empty until the first one.
	lastViewedTxHash string

	transactionList *cryptomaterial.ClickableList
	scroll          *components.Scroll[*multiWalletTx]

	// txRefreshInFlight coalesces the burst of tx/block notifications a new
	// block produces (OnTransactionConfirmed fires per confirmed tx, each in its
	// own goroutine) into a single in-flight list refresh. Without it, the
	// concurrent loadNewItem resets race the scroll component's data / offset /
	// list.Position and break scrolling until the app is restarted.
	txRefreshInFlight atomic.Bool

	// txCache memoizes the complete decoded+post-filtered tx set so reopening the
	// Transactions page (and scrolling within it) doesn't re-read & re-decode the
	// set from the local, already-validated DB on every open. §3: notification
	// goroutines must NOT mutate it; they set txCacheDirty instead and the next
	// UI-thread fetch discards the cache, so the slice Layout reads is never raced.
	txCache txDecodedCache

	// txCacheMu guards txCache. fetchTransactions is the scroll component's
	// queryFunc and is invoked from scroll goroutines (items_scroll.go), NOT
	// only the UI thread, so two fetches can overlap and race the cached slice.
	txCacheMu sync.Mutex

	// txCacheDirty is set from the tx/block notification goroutines to mark the
	// memoized set stale (new tx => changed rows; new block => reorg-safe full
	// invalidate). It is cleared in fetchTransactions, which then rebuilds the
	// cache from the freshly fetched DB rows.
	txCacheDirty atomic.Bool

	// fetchMu serializes fetchTransactions. The scroll component does NOT
	// serialize its queryFunc (items_scroll.go never sets isLoadingItems=true), so
	// a notification refresh and a scroll-triggered load can both call it at
	// offset 0; without this each would return the full set and the component
	// would append BOTH, duplicating the whole list. Serializing also removes any
	// cross-fetch cache clobber. fetchTransactions runs only on goroutines (never
	// the UI thread), so holding this never blocks rendering.
	fetchMu sync.Mutex

	// pendingFetchErr carries a loadAllTransactions failure from the fetch
	// goroutine to the UI thread, which shows it as a modal in
	// HandleUserInteractions. The scroll component swallows a (nil,err) result
	// (its empty-slice early return precedes its error-modal path), so the page
	// surfaces the error itself. Guarded by fetchErrMu.
	fetchErrMu      sync.Mutex
	pendingFetchErr error

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
	// Without this the Assets filter never exists on pages opened via the
	// Info page "View All" buttons (rightDropdown nil-checks it away), and
	// with a pre-selected wallet no walletDropDown.Changed event can ever
	// rebuild it either.
	pg.initCoinTypeDropdown()
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
	_, keysInfo := components.TxPageDropDownFields(pg.getAssetType(), tableTab(pg.selectedTxCategoryTab))
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

	// The Regular tab opens on "All without Split": splits are ticket
	// plumbing, not payments, so the plain-transfers view is the useful
	// default — "All types" stays one click away. (No-op for non-DCR
	// assets, whose filter set has no such label.)
	if tableTab(pg.selectedTxCategoryTab) == 0 {
		pg.statusDropDown.SetSelectedValue(values.String(values.StrAllWithoutSplit))
	}

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
func currentTxCacheKey(v txView) txCacheKey {
	var walletIDs string
	if v.selectedWallet != nil {
		walletIDs = strconv.Itoa(v.selectedWallet.GetWalletID())
	} else {
		ids := make([]int, 0, len(v.assetWallets))
		for _, wal := range v.assetWallets {
			ids = append(ids, wal.GetWalletID())
		}
		sort.Ints(ids)
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = strconv.Itoa(id)
		}
		walletIDs = strings.Join(parts, ",")
	}
	return txCacheKey{
		walletIDs:   walletIDs,
		categoryTab: v.categoryTab,
		statusLabel: v.statusLabel,
		coinSymbol:  v.coinSymbol,
		orderNewest: v.orderNewest,
		searchKey:   v.searchKey,
	}
}

// txView is an immutable snapshot of the page state that defines the current
// transaction view (which wallet(s), tab, status filter, coin filter, sort
// order, search). fetchTransactions captures it ONCE per fetch — on the calling
// scroll/notification goroutine — and threads it through the whole build, so a
// concurrent fetch (the scroll component does NOT serialize its queryFunc) or a
// UI-thread mutation in HandleUserInteractions cannot change the view mid-build.
// Reading pg.* repeatedly deep in the scan instead would race those fields and
// could cache wrong-tab rows (CLAUDE.md §3).
type txView struct {
	selectedWallet sharedW.Asset
	assetWallets   []sharedW.Asset // copy; populated only for the multi-wallet view
	categoryTab    int
	txFilter       int32 // logical filter resolved from (assetType, tab, statusLabel)
	coinType       *cointype.CoinType
	orderNewest    bool
	statusLabel    string
	coinSymbol     string
	searchKey      string
}

// snapshotView reads every view-defining field exactly once and returns an
// immutable snapshot. See txView.
func (pg *TransactionsPage) snapshotView() txView {
	v := txView{
		selectedWallet: pg.selectedWallet,
		categoryTab:    pg.selectedTxCategoryTab,
		orderNewest:    pg.orderDropDown.Selected() != values.String(values.StrOldest),
		coinType:       pg.selectedCoinType(),
		searchKey:      pg.searchEditor.Editor.Text(),
	}
	if pg.selectedWallet == nil {
		v.assetWallets = append([]sharedW.Asset(nil), pg.assetWallets...)
	}
	if pg.statusDropDown != nil {
		v.statusLabel = pg.statusDropDown.Selected()
		// Strip the trailing " (N)" count suffix the regular tab used to append,
		// keeping internal spaces so multi-word labels still match a mapInfo key.
		if i := strings.LastIndex(v.statusLabel, " ("); i != -1 {
			v.statusLabel = v.statusLabel[:i]
		}
	}
	if pg.coinTypeDropDown != nil {
		v.coinSymbol = pg.coinTypeDropDown.Selected()
	}
	v.txFilter = pg.resolveTxFilter(v)
	return v
}

// resolveTxFilter maps the snapshot's (tab, statusLabel) to the logical tx
// filter via the same dropdown-field table the dropdown was built from. The
// asset type comes from the selected wallet (DCR for the multi-wallet view,
// where the only tabs that consult txFilter — Staking/Reward — are DCR-only).
func (pg *TransactionsPage) resolveTxFilter(v txView) int32 {
	assetType := utils.DCRWalletAsset
	if v.selectedWallet != nil {
		assetType = v.selectedWallet.GetAssetType()
	}
	mapInfo, _ := components.TxPageDropDownFields(assetType, tableTab(v.categoryTab))
	if f, ok := mapInfo[v.statusLabel]; ok {
		return f
	}
	return utils.TxFilterAll
}

func (pg *TransactionsPage) fetchTransactions(offset, pageSize int32) (txs []*multiWalletTx, totalTxs int, isReset bool, err error) {
	// Serialize fetches (see fetchMu): the scroll component does not serialize its
	// queryFunc, so a notification refresh and a scroll-triggered load can both
	// run at offset 0 and the component would append BOTH full sets, duplicating
	// the list. This runs on goroutines only, so it never blocks rendering.
	pg.fetchMu.Lock()
	defer pg.fetchMu.Unlock()

	// Snapshot the whole view ONCE so nothing downstream reads pg.* (which the UI
	// thread mutates) deep in the scan — that would race and could cache wrong-tab
	// rows. Everything downstream reads v.
	v := pg.snapshotView()
	key := currentTxCacheKey(v)

	// Cache lookup (see txDecodedCache). A pending notification marks the cache
	// dirty; clearing the flag here forces a rebuild from the freshly validated
	// DB rows. A key mismatch (tab/status/coin/order/search/wallet changed) is an
	// automatic miss.
	pg.txCacheMu.Lock()
	if pg.txCacheDirty.CompareAndSwap(true, false) {
		pg.txCache.valid = false
		pg.txCache.all = nil
	}
	hit := pg.txCache.valid && pg.txCache.key == key
	var full []*multiWalletTx
	if hit {
		full = pg.txCache.all
	}
	pg.txCacheMu.Unlock()

	if !hit {
		// Load the COMPLETE post-filtered set for this view (the DB is paged in
		// chunks, decoded, coin-filtered and reclassified). Done OUTSIDE txCacheMu.
		var lerr error
		full, lerr = pg.loadAllTransactions(v)
		if lerr != nil {
			// The scroll component swallows a (nil,err) result, so surface the
			// failure ourselves on the UI thread (see pendingFetchErr) instead of
			// silently rendering an empty "No transactions" list.
			pg.stageFetchError(lerr)
			return nil, 0, false, nil
		}
		pg.txCacheMu.Lock()
		// Commit unless a notification invalidated the data mid-scan (then leave
		// the cache invalid so the next fetch rebuilds). We still render `full`.
		if !pg.txCacheDirty.Load() {
			pg.txCache = txDecodedCache{key: key, valid: true, all: full}
		} else {
			pg.txCache.valid = false
		}
		pg.txCacheMu.Unlock()
	}

	// Deliver the COMPLETE set on EVERY offset-0 query, cache hit or miss.
	// An offset-0 query only ever comes from (a) an initial load with an
	// empty window (fetchScrollData queries at 0 only when s.data is
	// nil/empty) or (b) refreshInPlace, which REPLACES the window rather
	// than appending — so returning the cached set can never double-append.
	// Returning nil on a hit (the old rule) let a concurrent fetch consume
	// txCacheDirty first and this refresh then swap the visible list to
	// EMPTY even though the cache held the full fresh set. offset>0 always
	// returns nil, so the component's window stays anchored at idxStart==0
	// (its slide/reposition math never engages — tasks 1 & 2). Gio
	// virtualizes the full slice, so it's cheap.
	if offset == 0 {
		return full, len(full), false, nil
	}
	return nil, 0, false, nil
}

// stageFetchError records a background load failure for the UI thread to surface
// (HandleUserInteractions shows it once as a modal) and requests a redraw. §3:
// the modal is shown on the UI thread, not from this fetch goroutine.
func (pg *TransactionsPage) stageFetchError(err error) {
	pg.fetchErrMu.Lock()
	pg.pendingFetchErr = err
	pg.fetchErrMu.Unlock()
	pg.ParentWindow().Reload()
}

// loadAllTransactions reads, decodes and post-filters the COMPLETE transaction
// set for the current view. The DB is paged in fixed chunks until exhausted
// (loadTransactions -> GetTransactionsRaw with the coarse DB filter), then the
// accumulated rows are coin-filtered and reclassified into the selected sub-tab
// exactly once. For the multi-wallet ("All wallets") view it scans every synced
// wallet and merges by timestamp. fetchTransactions caches the result.
//
// This deliberately walks the whole wallet rather than a single page: the
// reclassification post-filter (split/reward-union/voted) can shrink a DB page
// to a handful of rows, which the scroll component would misread as end-of-list
// — so older matching txs would be unreachable (tasks 1 & 3). Validation/SPV
// sync is untouched; this only reads already-validated local DB rows.
func (pg *TransactionsPage) loadAllTransactions(v txView) ([]*multiWalletTx, error) {
	const dbChunk = int32(200)
	var raw []*multiWalletTx
	scan := func(wal sharedW.Asset) error {
		var dbOffset int32
		for {
			chunk, _, err := pg.loadTransactions(v, wal, dbOffset, dbChunk)
			if err != nil {
				return err
			}
			raw = append(raw, chunk...)
			if int32(len(chunk)) < dbChunk {
				break // DB exhausted for this wallet
			}
			dbOffset += int32(len(chunk))
		}
		return nil
	}

	if v.selectedWallet != nil {
		if err := scan(v.selectedWallet); err != nil {
			return nil, err
		}
	} else {
		for _, wal := range v.assetWallets {
			if !wal.IsSynced() {
				continue // skip wallets that are not synced
			}
			if err := scan(wal); err != nil {
				return nil, err
			}
		}
	}

	// Price the split rows (Amount = the outputs their tickets consumed) while
	// the coarse superset still holds the ticket rows — the coin-type filter
	// and reclassification below may drop them.
	all := make([]*sharedW.Transaction, 0, len(raw))
	for _, mw := range raw {
		if mw.Transaction != nil {
			all = append(all, mw.Transaction)
		}
	}
	dcr.ApplySplitAmounts(all)

	// A hash search bypasses the coarse superset (the DB returns only the
	// matched tx), so a searched split has no ticket rows to price against
	// and would show its fee. Re-derive from the ticket index directly.
	if v.searchKey != "" {
		for _, mw := range raw {
			if mw.Transaction != nil {
				_, wal := pg.txAndWallet(mw)
				ensureSplitAmount(wal, mw.Transaction)
			}
		}
	}

	raw = pg.filterByCoinType(v, raw)
	// Reclassify into the selected sub-tab (Regular / Staking / Reward) over the
	// decoded rows. The DB fetch used a COARSE filter (coarseFetchFilter) because
	// the reclass predicates — default->default "split", the reward union, the
	// PoW/PoS split and "ticket that voted" (spender lookup) — can't be a single
	// storm query.
	raw = pg.reclassifyByTab(v, raw)

	// Multi-wallet view: rows were appended wallet-by-wallet, so re-establish the
	// global newest/oldest ordering across wallets.
	if v.selectedWallet == nil {
		sort.SliceStable(raw, func(i, j int) bool {
			if v.orderNewest {
				return raw[i].Timestamp > raw[j].Timestamp
			}
			return raw[i].Timestamp < raw[j].Timestamp
		})
	}
	return raw, nil
}

// ensureSplitAmount prices a single split transaction's Amount (the sum of
// outputs its ticket purchases consumed) when the tx arrives from a path that
// carries no ticket rows alongside it — a hash search, a direct details open,
// or a single-tx refresh. It pages the newest tickets down to the split's
// timestamp (a spender is never older than its split) and runs
// dcr.ApplySplitAmounts over {split + tickets}. No-op for non-splits; a split
// whose tickets can't be found keeps its stored Amount (the fee).
func ensureSplitAmount(wal sharedW.Asset, tx *sharedW.Transaction) {
	if wal == nil || tx == nil || !dcr.IsSplitTx(tx) {
		return
	}
	const ticketPage = int32(200)
	supplement := []*sharedW.Transaction{tx}
	for page := int32(0); page < 10; page++ {
		tickets, err := wal.GetTransactionsRaw(page*ticketPage, ticketPage, utils.TxFilterTickets, true, "")
		if err != nil || len(tickets) == 0 {
			break
		}
		supplement = append(supplement, tickets...)
		if tickets[len(tickets)-1].Timestamp < tx.Timestamp || int32(len(tickets)) < ticketPage {
			break
		}
	}
	dcr.ApplySplitAmounts(supplement)
}

// coarseFetchFilter maps a logical (tab,status) filter to a DB-supported filter
// for the GetTransactionsRaw fetch. prepareTxQuery (walletdata/filter.go) only
// understands the legacy filters (0-14); the reclassification filters (15-23)
// are unknown to it and hit its `default: q.True()` branch, which returns EVERY
// stored row — the true all-types superset. reclassifyByTab then cuts the exact
// set over the decoded rows.
//
// CRITICAL: the stake-bearing reclass filters must NOT be routed onto
// TxFilterAll. In filter.go TxFilterAll is `Type ∈ {Regular, Mixed, Coinbase}` —
// it EXCLUDES ticket purchases, votes and revocations. Collapsing
// StakingList/RewardList/RewardPoS onto it silently drops every ticket from the
// Staking tab and every vote/revocation from the Reward tab (the original
// "Staking All shows only Split" bug). Those filters fall through to q.True().
func coarseFetchFilter(logical int32) int32 {
	switch logical {
	case utils.TxFilterTicketVoted:
		return utils.TxFilterTickets // voted ⊆ tickets; spender==vote refined in UI
	case utils.TxFilterStakingNoSplit:
		return utils.TxFilterTickets // tickets only — the DB Tickets filter is already exact
	case utils.TxFilterStakeFee:
		// Stake-fee rows are Regular/Mixed-typed (an SSFee keeps
		// Type=Regular), fully covered by TxFilterAll — no need to scan
		// the stake rows.
		return utils.TxFilterAll
	case utils.TxFilterRegularNoSplit:
		// Splits are excluded from this view, so the ticket rows
		// ApplySplitAmounts would need aren't required — TxFilterAll
		// (Regular+Mixed+Coinbase) is a cheap indexed superset.
		return utils.TxFilterAll
	case utils.TxFilterSplit, utils.TxFilterRegularList, utils.TxFilterStakingList,
		utils.TxFilterRewardList, utils.TxFilterRewardPoW,
		utils.TxFilterRewardPoS, utils.TxFilterMissed:
		// TxFilterSplit and TxFilterRegularList (which now includes split
		// rows) are deliberately NOT collapsed onto TxFilterAll even
		// though splits are Regular-typed: dcr.ApplySplitAmounts needs the
		// ticket rows alongside the splits to price them, and TxFilterAll
		// excludes tickets.
		// The reward filters need vote rows that TxFilterAll omits. Pass
		// the logical value through so prepareTxQuery's default returns
		// q.True().
		return logical
	default:
		return logical // 0-14 are DB-supported and already exact
	}
}

// reclassifyByTab keeps only the txs that belong in the selected sub-tab +
// status. v.txFilter holds the logical filter resolved for this view.
func (pg *TransactionsPage) reclassifyByTab(v txView, in []*multiWalletTx) []*multiWalletTx {
	out := in[:0]
	for _, mw := range in {
		if mw.Transaction != nil && pg.keepForTab(v, mw) {
			out = append(out, mw)
		}
	}
	return out
}

// keepForTab is the per-tab/status membership predicate (the reclassification
// core). Regular = plain regular/mixed movements INCLUDING splits (a split is
// an ordinary self-transfer that funds tickets; the dedicated Split filter
// isolates them); Reward = mining rewards (coinbase + MF) and staking rewards
// (votes + SF).
func (pg *TransactionsPage) keepForTab(v txView, mw *multiWalletTx) bool {
	tx := mw.Transaction
	switch v.categoryTab {
	case 0: // Regular
		switch v.txFilter {
		case utils.TxFilterSplit:
			return dcr.IsSplitTx(tx)
		case utils.TxFilterRegularList: // "All types" includes split rows
			// Tickets, votes and coinbase have their own tx types
			// (Staking page / Reward tab); stake-fees are excluded
			// explicitly. This type restriction keeps the tabs mutually
			// exclusive — a ticket must NOT also appear here.
			return (tx.Type == txhelper.TxTypeRegular || tx.Type == txhelper.TxTypeMixed) &&
				!tx.IsStakeFee
		case utils.TxFilterRegularNoSplit: // "All without Split" — the default view
			// An explicit search must still find a split by its hash even
			// under the no-split default — hiding an exact match the user
			// asked for reads as "transaction lost".
			if v.searchKey != "" {
				return (tx.Type == txhelper.TxTypeRegular || tx.Type == txhelper.TxTypeMixed) &&
					!tx.IsStakeFee
			}
			return (tx.Type == txhelper.TxTypeRegular || tx.Type == txhelper.TxTypeMixed) &&
				!tx.IsStakeFee && !dcr.IsSplitTx(tx)
		default: // Sent / Received / Transferred — direction views.
			// Splits stay out of these: they have their own filter, and
			// these statuses' exact DB fetch lacks the ticket rows
			// ApplySplitAmounts needs, so a split here would display its
			// fee instead of the split amount.
			return (tx.Type == txhelper.TxTypeRegular || tx.Type == txhelper.TxTypeMixed) &&
				!tx.IsStakeFee && !dcr.IsSplitTx(tx)
		}
	case 1: // Reward
		switch v.txFilter {
		case utils.TxFilterRewardPoW:
			return isPoWReward(tx)
		case utils.TxFilterRewardPoS:
			return isPoSReward(tx)
		default: // "All rewards"
			return isRewardTx(tx)
		}
	}
	return true
}

// isRewardTx reports whether tx belongs to the Reward tab: a consensus reward —
// PoW coinbase, a stake-fee distribution (SF or MF), or a vote. Revocations
// return the ticket price without any reward, so they are not rewards.
func isRewardTx(tx *sharedW.Transaction) bool {
	if tx.IsStakeFee {
		return true
	}
	switch tx.Type {
	case txhelper.TxTypeCoinBase, txhelper.TxTypeVote:
		return true
	}
	return false
}

// isPoWReward reports whether tx is a mining reward: the coinbase block
// reward, or a miner-fee (MF) stake-fee distribution.
func isPoWReward(tx *sharedW.Transaction) bool {
	return tx.Type == txhelper.TxTypeCoinBase || (tx.IsStakeFee && tx.StakeFeeKind == "MF")
}

// isPoSReward reports whether tx is a staking reward: a vote, or a
// staker-fee (SF) stake-fee distribution.
func isPoSReward(tx *sharedW.Transaction) bool {
	if tx.Type == txhelper.TxTypeVote {
		return true
	}
	return tx.IsStakeFee && tx.StakeFeeKind == "SF"
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

// filterByCoinType drops any transactions whose CoinType differs from the
// one selected in the coin-type filter dropdown. The "All assets"
// sentinel (index 0) skips the filter entirely.
//
// Sentinel detection is index-based, not text-based: comparing against the
// literal "All assets" used to silently break in non-EN locales (the
// label is `values.String(StrAllAssets)`, which is "Усі активи" in
// Ukrainian — the string comparison would never match the sentinel and
// the list would render as empty).
func (pg *TransactionsPage) filterByCoinType(v txView, in []*multiWalletTx) []*multiWalletTx {
	picked := v.coinType
	// "All assets" still excludes coins the user hid via the visibility
	// filter — hidden coins must not surface anywhere, including here. Build
	// the visible set for the selected wallet (single-wallet view); when no
	// single wallet is selected (multi-wallet) we can't scope per coin
	// reliably, so fall through to no filtering.
	var visible map[cointype.CoinType]bool                 // single-wallet visible set
	var visibleByWallet map[int]map[cointype.CoinType]bool // multi-wallet, keyed by walletID
	if picked == nil {
		if dcrAsset, ok := v.selectedWallet.(*dcr.Asset); ok {
			visible = make(map[cointype.CoinType]bool)
			for _, ct := range dcrAsset.VisibleCoinTypes() {
				visible[ct] = true
			}
		} else {
			// Multi-wallet "All wallets" view: the hide filter is per-wallet,
			// so scope each tx against its OWN wallet's visible set.
			visibleByWallet = make(map[int]map[cointype.CoinType]bool)
			for _, wal := range v.assetWallets {
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

func (pg *TransactionsPage) loadTransactions(v txView, wal sharedW.Asset, offset, pageSize int32) ([]*multiWalletTx, int, error) {
	mapInfo, _ := components.TxPageDropDownFields(wal.GetAssetType(), tableTab(v.categoryTab))
	if len(mapInfo) < 1 {
		err := fmt.Errorf("unable to resolve asset filters for asset type (%v)", wal.GetAssetType())
		return nil, -1, err
	}

	// Resolve THIS wallet's coarse DB filter from its own asset type + the view's
	// (already " (N)"-stripped) status label — multi-wallet may mix types. The
	// logical filter that drives reclassify is v.txFilter (resolved once in
	// snapshotView); we deliberately do NOT write it to page state here — that
	// would race a concurrent fetch and could cache wrong-tab rows.
	txFilter, ok := mapInfo[v.statusLabel]
	if !ok {
		err := fmt.Errorf("unsupported field(%v) for asset type(%v) and txCategoryTab index(%d) found",
			v.statusLabel, wal.GetAssetType(), v.categoryTab)
		return nil, -1, err
	}
	// Fetch a DB-supported coarse superset; reclassifyByTab refines it (the
	// reclassification filters aren't understood by prepareTxQuery).
	walletTxs, err := wal.GetTransactionsRaw(offset, pageSize, coarseFetchFilter(txFilter), v.orderNewest, v.searchKey)
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

// dropdownLayout renders the always-visible filter controls (there is no
// Filter toggle anymore — hiding the filters behind an extra click just cost
// a click and shoved the list down when opened). Desktop fits everything on
// one row: wallet selector left, status/coin/order dropdowns + Export right.
// Mobile keeps a permanent second row for the dropdowns — three of them plus
// Export don't fit into 375dp.
func (pg *TransactionsPage) dropdownLayout(gtx C) D {
	if pg.IsMobileView() {
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
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return pg.leftDropdown(gtx)
}

func (pg *TransactionsPage) leftDropdown(gtx C) D {
	showOverlay := pg.walletNotReady() && pg.multiWalletLayout
	return layout.Flex{Spacing: layout.SpaceBetween}.Layout(gtx,
		// LEFT: page title / wallet selector, with Export anchored to the
		// row's left edge right after it.
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
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
					// Export is not enabled on mobile yet (TODO).
					if showOverlay || pg.IsMobileView() {
						return D{}
					}
					inset := layout.Inset{Top: values.MarginPadding8}
					if pg.walletDropDown != nil {
						inset.Left = values.MarginPadding16
					}
					return inset.Layout(gtx, func(gtx C) D {
						return pg.buttonWrap(gtx, pg.exportBtn, pg.Theme.Icons.ShareIcon, values.String(values.StrExport))
					})
				}),
			)
		}),
		// RIGHT: the filter dropdowns, right-aligned. On mobile they live
		// on their own second row (rightDropdown) instead.
		layout.Rigid(func(gtx C) D {
			if showOverlay || pg.IsMobileView() {
				return D{}
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(pg.statusDropDown.Layout),
				layout.Rigid(func(gtx C) D {
					if pg.coinTypeDropDown == nil {
						return D{}
					}
					return pg.coinTypeDropDown.Layout(gtx)
				}),
				layout.Rigid(pg.orderDropDown.Layout),
			)
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

// rightDropdown is the mobile-only second row holding the filter dropdowns
// (desktop inlines them into the top row — see leftDropdown).
func (pg *TransactionsPage) rightDropdown(gtx C) D {
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
		// One header row on desktop, two on mobile (the dropdowns' own row).
		marginTop := values.MarginPadding50
		if pg.IsMobileView() {
			marginTop = values.MarginPadding80
		}
		return layout.Inset{Top: marginTop}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
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
												row := func(gtx C) D {
													return components.LayoutTransactionRow(gtx, pg.Load, wal, tx, hideAssetInfo)
												}
												// The most recently viewed tx keeps a subtle
												// highlight so it's findable after closing
												// its details card.
												if tx.Hash != "" && tx.Hash == pg.lastViewedTxHash {
													return cryptomaterial.LinearLayout{
														Width:      cryptomaterial.MatchParent,
														Height:     cryptomaterial.WrapContent,
														Background: pg.Theme.Color.Gray4,
														Border:     cryptomaterial.Border{Radius: cryptomaterial.Radius(8)},
													}.Layout2(gtx, row)
												}
												return row(gtx)
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
	// Surface any fetch error staged by a background load (the scroll component
	// swallows it). Shown once per failure, on the UI thread.
	pg.fetchErrMu.Lock()
	if ferr := pg.pendingFetchErr; ferr != nil {
		pg.pendingFetchErr = nil
		pg.fetchErrMu.Unlock()
		pg.ParentWindow().ShowModal(modal.NewErrorModal(pg.Load,
			fmt.Errorf("error loading transactions: %v", ferr).Error(), modal.DefaultClickFunc()))
	} else {
		pg.fetchErrMu.Unlock()
	}

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
		if selectedItem >= 0 && selectedItem < len(transactions) {
			tx, wal := pg.txAndWallet(transactions[selectedItem])
			pg.lastViewedTxHash = tx.Hash
			pg.ParentNavigator().Display(NewTransactionDetailsPage(pg.Load, wal, tx))
		}
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
	// §3: do NOT mutate the cache slice from this goroutine (Layout reads it on the
	// UI thread). Just mark it dirty; the next fetch clears the flag and rebuilds
	// from the freshly validated DB rows. Mark dirty BEFORE the coalescing guard:
	// a notification that loses the CAS (its refresh coalesced away) must STILL
	// invalidate the cache, otherwise a just-confirmed vote/coinbase/stake-fee
	// could be committed-out of the cached set and stay missing until an unrelated
	// later event. Marking is cheap and idempotent; only the heavy refresh below
	// needs coalescing.
	pg.txCacheDirty.Store(true)
	if !pg.txRefreshInFlight.CompareAndSwap(false, true) {
		return
	}
	defer pg.txRefreshInFlight.Store(false)
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
