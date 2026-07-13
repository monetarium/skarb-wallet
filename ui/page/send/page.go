package send

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync/atomic"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/monetarium/monetarium-node/cointype"
	"github.com/monetarium/skarb-wallet/app"
	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	libUtil "github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/modal"
	"github.com/monetarium/skarb-wallet/ui/page/components"
	txpage "github.com/monetarium/skarb-wallet/ui/page/transaction"
	"github.com/monetarium/skarb-wallet/ui/utils"
	"github.com/monetarium/skarb-wallet/ui/values"
)

const (
	SendPageID = "Send"

	// MaxTxLabelSize defines the maximum number of characters to be allowed on
	MaxTxLabelSize = 100
)

var (
	automaticCoinSelection = values.String(values.StrAutomatic)
	manualCoinSelection    = values.String(values.StrManual)
)

type Page struct {
	*load.Load
	// GenericPageModal defines methods such as ID() and OnAttachedToNavigator()
	// that helps this Page satisfy the app.Page interface. It also defines
	// helper methods for accessing the PageNavigator that displayed this page
	// and the root WindowNavigator.
	*app.GenericPageModal
	// modalLayout is initialized if this page will be displayed as a modal
	// rather than a full page. A modal display is used and a wallet selector is
	// displayed if this send page is opened from the home page.
	modalLayout *cryptomaterial.Modal

	pageContainer *widget.List

	walletDropdown   *components.WalletDropdown
	accountDropdown  *components.AccountDropdown
	coinTypeDropdown *components.CoinTypeDropdown

	hideWalletDropdown, hideAdvancedOptions bool

	// recipient  *recipient
	recipients []*recipient

	infoButton cryptomaterial.IconButton
	// retryExchange cryptomaterial.Button // TODO not included in design
	nextButton      cryptomaterial.Button
	closeButton     cryptomaterial.Button
	addRecipientBtn *cryptomaterial.Clickable

	isFetchingExchangeRate bool

	exchangeRate   float64
	usdExchangeSet bool
	confirmTxModal *sendConfirmModal

	*authoredTxData
	selectedWallet  sharedW.Asset
	feeRateSelector *components.FeeRateSelector

	toCoinSelection *cryptomaterial.Clickable
	advanceOptions  *cryptomaterial.Collapsible

	selectedUTXOs      selectedUTXOsInfo
	navigateToSyncBtn  cryptomaterial.Button
	currentIDRecipient int

	// pendingBroadcastReset flips to 1 from the broadcast goroutine the
	// instant Broadcast() returns success. The next HandleUserInteractions
	// frame (woken via ParentWindow().Invalidate()) drains the flag and
	// runs resetRecipientsFields + clearEstimates + autoDefaultCoinType-
	// FromBalance on the UI thread, so the editors / display strings get
	// mutated under their normal single-goroutine invariant. Without this
	// indirection the form behind the "Transaction sent!" success modal
	// keeps the typed amount, re-validates against the now-zero balance,
	// and flashes "Недостатньо коштів" + "- SKA1" under the green check.
	pendingBroadcastReset atomic.Bool

	// pendingExchangeRate / pendingExchangeRateApply hand the freshly-fetched
	// USD rate from the fetchExchangeRate goroutine to the UI thread. The
	// goroutine writes pendingExchangeRate then Store(true) (release); the next
	// HandleUserInteractions frame CompareAndSwap-drains it (acquire) and runs
	// updateRecipientExchangeRate + validateAndConstructTx — both of which call
	// Editor.SetText — on the UI thread. Writing editor buffers straight from
	// the goroutine is the race CLAUDE.md §3 forbids.
	pendingExchangeRate      float64
	pendingExchangeRateApply atomic.Bool

	// Custom fee override (user-typed relay-fee rate in atoms/KB). The
	// backend hooks (SetFeeRateOverride / ClearFeeRateOverride) live on
	// the DCR asset and validate against per-coin chainparams MinRelayTxFee.
	// UI flow: user toggles the section open, types a rate in the editor,
	// clicks "Застосувати" → the rate is parsed via dcr.ParseAmountToAtomsBig
	// (coin-aware big.Int parser — SKA atoms are 1e18/coin, way over int64),
	// validated against FeeRateBounds, and pushed to the asset. "Скинути"
	// clears the override and the next construct cycle reverts to the
	// chainparams default. customFeeStatus carries the last result message
	// (success or validation error) to render under the editor.
	customFeeEditor      cryptomaterial.Editor
	customFeeApplyBtn    cryptomaterial.Button
	customFeeClearBtn    cryptomaterial.Button
	customFeeStatus      string
	customFeeStatusIsErr bool

	// feeRateOverride holds the user's applied custom fee at the PAGE
	// level. Each constructTx cycle calls NewUnsignedTx which mints a
	// fresh TxAuthoredInfo and wipes the asset-side override — without
	// this page-level mirror, every keystroke (which re-runs constructTx)
	// resets the user's choice back to the chainparams default. After
	// every NewUnsignedTx + SetTxCoinType in constructTx we re-apply
	// this via SetFeeRateOverride so the next EstimateFeeAndSize sees
	// the override.
	feeRateOverride cointype.SKAAmount

	// subtractFeeFromRecipient mirrors the advanced-options checkbox: when
	// true the transaction fee is deducted from the (single) recipient's
	// output instead of the sender's change, so the sender spends exactly
	// the entered amount and the recipient receives amount-minus-fee. Wired
	// to the wallet via AddSendDestinationBig's subtractFeeFromAmount arg.
	subtractFeeFromRecipient bool
	// subtractFeeCheckbox is the widget backing that toggle.
	subtractFeeCheckbox cryptomaterial.CheckBoxStyle
}

type getPageFields func() pageFields

type pageFields struct {
	exchangeRate           float64
	usdExchangeSet         bool
	isFetchingExchangeRate bool
}

type authoredTxData struct {
	destinationAddress  []string
	destinationAccount  []*sharedW.Account
	sourceAccount       *sharedW.Account
	txFee               string
	txFeeUSD            string
	totalCost           string
	totalCostUSD        string
	balanceAfterSend    string
	balanceAfterSendUSD string
	sendAmount          string
	sendAmountUSD       string
	// Big.Int companions for SKA flows whose atom totals overflow int64.
	// Populated by addSendDestination only when needed; empty string falls
	// back to the int64 fields above for display. constructTx then chooses
	// the right channel via dcr.FormatTxAmountBig.
	totalCostBig        string
	balanceAfterSendBig string
	totalSendAmountBig  string
}

type selectedUTXOsInfo struct {
	sourceAccount    *sharedW.Account
	selectedUTXOs    []*sharedW.UnspentOutput
	totalUTXOsAmount int64
	// totalUTXOsAmountBig is the lossless atom sum of the selected UTXOs.
	// SKA UTXOs carry their value in SKAAmountAtoms with the int64 Amount
	// == 0, so totalUTXOsAmount reads 0 for a SKA selection — the big.Int
	// total is the real spendable for both coin types.
	totalUTXOsAmountBig *big.Int
	// coinType is the active coin type when the selection was made. A
	// selection is only valid for the coin it was picked for — switching the
	// VAR/SKAn dropdown must not feed VAR outpoints into a SKA tx (or vice
	// versa), so manualSelectionFor rejects a selection whose coinType differs
	// from the currently-selected dropdown coin.
	coinType cointype.CoinType
}

func NewSendPage(l *load.Load, wallet sharedW.Asset) *Page {
	pg := &Page{
		Load:              l,
		authoredTxData:    &authoredTxData{},
		exchangeRate:      -1,
		navigateToSyncBtn: l.Theme.Button(values.String(values.StrStartSync)),
		addRecipientBtn:   l.Theme.NewClickable(false),
		recipients:        make([]*recipient, 0),
	}

	if wallet == nil {
		// When this page is opened from the home page, the wallet to use is not
		// specified. This page will be opened as a modal and a wallet selector
		// will be displayed.
		pg.modalLayout = l.Theme.ModalFloatTitle(values.String(values.StrSend), pg.IsMobileView(), nil)
		pg.GenericPageModal = pg.modalLayout.GenericPageModal
		pg.hideWalletDropdown = false
		pg.hideAdvancedOptions = true
	} else {
		pg.GenericPageModal = app.NewGenericPageModal(SendPageID)
		pg.selectedWallet = wallet
		pg.hideWalletDropdown = true
		pg.hideAdvancedOptions = false
	}
	pg.initModalWalletSelector(wallet) // will auto select the first wallet in the dropdown as pg.selectedWallet
	callbackFunc := func() libUtil.AssetType {
		if pg.selectedWallet == nil {
			return libUtil.NilAsset
		}
		return pg.selectedWallet.GetAssetType()
	}
	pg.feeRateSelector = components.NewFeeRateSelector(l, callbackFunc).ShowSizeAndCost()
	pg.coinTypeDropdown = components.NewCoinTypeDropdown(l).
		SetChangedCallback(func(ct cointype.CoinType) {
			pg.applyCoinType(ct)
		})
	if dcrAsset, ok := pg.selectedWallet.(*dcr.Asset); ok {
		pg.coinTypeDropdown.Setup(dcrAsset)
	}
	pg.customFeeEditor = l.Theme.Editor(new(widget.Editor), values.String(values.StrFeeRatePerKbHint))
	pg.customFeeEditor.Editor.SingleLine = true
	pg.customFeeApplyBtn = l.Theme.Button(values.String(values.StrApply))
	pg.customFeeApplyBtn.TextSize = values.TextSize14
	pg.customFeeApplyBtn.Inset = layout.UniformInset(values.MarginPadding8)
	pg.customFeeClearBtn = l.Theme.OutlineButton(values.String(values.StrReset))
	pg.customFeeClearBtn.TextSize = values.TextSize14
	pg.customFeeClearBtn.Inset = layout.UniformInset(values.MarginPadding8)

	// Label is empty: the section title (contentWrapper) already shows
	// "Відняти комісію із суми отримувача"; the checkbox sits under it with
	// the explanatory caption, so a second copy of the label would be
	// redundant.
	pg.subtractFeeCheckbox = l.Theme.CheckBox(new(widget.Bool), values.String(values.StrSubtractFeeFromRecipientToggle))
	pg.subtractFeeCheckbox.TextSize = values.TextSize14

	pg.addRecipient()
	pg.initLayoutWidgets()
	pg.setAssetTypeForRecipients()
	return pg
}

// applyCoinType is fired when the user picks a different asset (VAR / SKA-n)
// in the CoinType dropdown. It tells the wallet authoring layer about the new
// coin type, propagates it to every recipient's amount editor (so the
// float→atoms conversion uses the right base), and re-validates the form so
// fee/balance estimates refresh.
func (pg *Page) applyCoinType(ct cointype.CoinType) {
	dcrAsset, ok := pg.selectedWallet.(*dcr.Asset)
	if !ok {
		return
	}
	if dcrAsset.IsUnsignedTxExist() {
		if err := dcrAsset.SetTxCoinType(ct); err != nil {
			log.Errorf("SetTxCoinType(%s): %v", ct, err)
		}
	}
	// Drop any user-set fee-rate override on coin switch. The override is
	// in the previous coin's atoms (e.g. 33 SKA1 = 3.3e19 SKA atoms);
	// re-applying it under the new coin's bounds (VAR max ≈ 1e7 atoms)
	// always fails validation and emits a misleading "0.00 VAR exceeds
	// safety cap 0.10 VAR" message because the SKA-atoms value doesn't
	// fit int64 for VAR display. Clear here AND in the asset so the next
	// construct cycle uses the new coin's chainparams default.
	pg.feeRateOverride = cointype.Zero()
	dcrAsset.ClearFeeRateOverride()
	pg.customFeeEditor.Editor.SetText("")
	pg.customFeeStatus = ""
	pg.customFeeStatusIsErr = false
	for _, rc := range pg.recipients {
		rc.setCoinType(ct)
	}
	// Switch the source-account dropdown to show the SKA balance for the
	// selected coin (instead of always showing VAR), so a user with 0 VAR
	// and 550 SKA1 stops seeing "0 VAR" and concluding the wallet is
	// empty.
	if pg.accountDropdown != nil {
		pg.accountDropdown.SetCoinType(ct)
	}
	// Source-wallet rows follow the active coin too (visible in multi-wallet
	// view); destination wallet/account rows follow via rc.setCoinType above.
	if pg.walletDropdown != nil {
		pg.walletDropdown.SetCoinType(ct)
	}
	// A manual UTXO selection is coin-specific: VAR outpoints can't fund a SKA
	// tx (and vice versa). Drop it on a coin switch so the wallet auto-selects
	// from the new coin's UTXO set instead of carrying stale outpoints (the
	// coinSelectionSection also returns to "Автоматично").
	pg.selectedUTXOs = selectedUTXOsInfo{totalUTXOsAmountBig: new(big.Int)}
	pg.validateAndConstructTx()
}

// autoDefaultCoinTypeFromBalance picks a coin type that actually has a
// spendable balance on the current source account, so a wallet with 0 VAR
// + some SKA isn't stuck on a VAR-default dropdown that immediately errors.
//
// Decision tree:
//   - selected coin has spendable atoms → keep it (no-op);
//   - otherwise iterate DisplayableCoinTypes (already filtered by activity
//     on this wallet) and pick the first with spendable > 0;
//   - if no coin has any spendable, leave the selection alone — the user
//     genuinely has nothing to send, and downstream messaging will say so.
//
// Propagation goes through applyCoinType so SetTxCoinType, the recipient
// amount editors, and the account-dropdown's coin filter all stay in sync —
// otherwise we'd flip the dropdown label but leave TxAuthoredInfo.coinType
// at VAR and reproduce the very bug we're trying to avoid.
func (pg *Page) autoDefaultCoinTypeFromBalance() {
	if pg.coinTypeDropdown == nil || pg.accountDropdown == nil {
		return
	}
	dcrAsset, ok := pg.selectedWallet.(*dcr.Asset)
	if !ok {
		return
	}
	sourceAccount := pg.accountDropdown.SelectedAccount()
	if sourceAccount == nil {
		return
	}

	hasSpendable := func(ct cointype.CoinType) bool {
		bal, err := dcrAsset.GetCoinBalance(sourceAccount.Number, ct)
		if err != nil {
			return false
		}
		if ct.IsVAR() {
			return bal.Spendable > 0
		}
		return bal.SKASpendable.Sign() > 0
	}

	if hasSpendable(pg.coinTypeDropdown.Selected()) {
		return
	}
	// Iterate VISIBLE coins only — never auto-select a coin the user hid via
	// the visibility filter. DisplayableCoinTypes ignores that filter, so it
	// could force-select a hidden coin: the dropdown (built from
	// VisibleCoinTypes) then falls back to VAR while applyCoinType drives the
	// whole form + authored tx in the hidden coin — a leak AND a coin/label
	// desync that can mis-denominate a send.
	for _, ct := range dcrAsset.VisibleCoinTypes() {
		if ct == pg.coinTypeDropdown.Selected() {
			continue
		}
		if hasSpendable(ct) {
			// Re-Setup with explicit override flips both the visible
			// dropdown label and d.selected; applyCoinType then mirrors
			// the change to the rest of the form.
			pg.coinTypeDropdown.Setup(dcrAsset, ct)
			pg.applyCoinType(ct)
			return
		}
	}
}

func (pg *Page) addRecipient() {
	if pg.selectedWallet == nil {
		return
	}
	rc := newRecipient(pg.Load, pg.selectedWallet, pg.pageFields, pg.currentIDRecipient, pg.ParentWindow())
	rc.onAddressChanged(func() {
		pg.validateAndConstructTx()
	})

	rc.onAmountChanged(func() {
		pg.validateAndConstructTx()
	})

	rc.onDeleteRecipient(func(id int) {
		pg.removeRecipient(id)
	})

	if pg.accountDropdown != nil && pg.accountDropdown.SelectedAccount() != nil {
		rc.initializeAccountSelectors(pg.accountDropdown.SelectedAccount())
	}
	rc.amount.setExchangeRate(pg.exchangeRate)
	// Seed the recipient's amount editor with the page-level coin type so
	// the very first amount the user types is converted using the right
	// atoms/coin scale even if they never touch the CoinType dropdown.
	if pg.coinTypeDropdown != nil {
		rc.setCoinType(pg.coinTypeDropdown.Selected())
	}
	pg.recipients = append(pg.recipients, rc)
	pg.currentIDRecipient++
}

func (pg *Page) removeRecipient(id int) {
	for i, re := range pg.recipients {
		if re.id == id {
			pg.recipients = append(pg.recipients[:i], pg.recipients[i+1:]...)
			break
		}
	}

	pg.selectedWallet.RemoveSendDestination(id)
}

func (pg *Page) pageFields() pageFields {
	return pageFields{
		exchangeRate:           pg.exchangeRate,
		usdExchangeSet:         pg.usdExchangeSet,
		isFetchingExchangeRate: pg.isFetchingExchangeRate,
	}
}

// initWalletSelector is used for the send modal for wallet selection.
func (pg *Page) initModalWalletSelector(wallet sharedW.Asset) {
	pg.walletDropdown = components.NewWalletDropdown(pg.Load).
		SetChangedCallback(func(w sharedW.Asset) {
			pg.selectedWallet = w
			if pg.accountDropdown != nil {
				pg.accountDropdown.Setup(w, pg.sourceAccount)
				go pg.feeRateSelector.UpdatedFeeRate(pg.selectedWallet)
				pg.setAssetTypeForRecipients()
			}
			if pg.coinTypeDropdown != nil {
				if dcrAsset, ok := w.(*dcr.Asset); ok {
					pg.coinTypeDropdown.Setup(dcrAsset)
				}
			}
		}).
		Setup(wallet)
	if pg.selectedWallet == nil {
		pg.selectedWallet = pg.walletDropdown.SelectedWallet()
	}

	pg.accountDropdown = components.NewAccountDropdown(pg.Load).
		SetChangedCallback(func(account *sharedW.Account) {
			pg.initAccountsSelectorForRecipients(account)
			pg.validateAllRecipientsAmount()
			pg.validateAndConstructTx()
		}).
		AccountValidator(func(account *sharedW.Account) bool {
			if pg.selectedWallet == nil {
				return false
			}
			accountIsValid := account.Number != load.MaxInt32 && !pg.selectedWallet.IsWatchingOnlyWallet()

			if pg.selectedWallet.ReadBoolConfigValueForKey(sharedW.AccountMixerConfigSet, false) &&
				!pg.selectedWallet.ReadBoolConfigValueForKey(sharedW.SpendUnmixedFundsKey, false) {
				// Spending unmixed fund isn't permitted for the selected wallet

				// only mixed accounts can send to address/wallets for wallet with privacy setup
				// don't need to check account the same with destination account
				accountIsValid = account.Number == load.MixedAccountNumber(pg.selectedWallet)

				// For an Intra-Accounts transfer to happen the bare minimum expected is that:
				// 1. There is only one recipient instance available.
				// 2. Both (i.e. source and recipient) must use the same wallet.
				// 3. Source account selected must have a spendable balance
				// 4. Recipient's "Wallets" tab option must be active/on display.
				// 5. The destination and source accounts must be different.
				if len(pg.recipients) == 1 && !pg.recipients[0].isSendToAddress() && account.Balance.Spendable.ToInt() > 0 {
					if pg.recipients[0].selectedWallet.GetWalletName() == pg.selectedWallet.GetWalletName() {
						// If it is same wallet, make accounts different from the destination valid.
						accountIsValid = account != pg.recipients[0].destinationAccount()
					}
				}
			}

			return accountIsValid
		}).
		Setup(pg.selectedWallet)
}

// RestyleWidgets restyles select widgets to match the current theme. This is
// especially necessary when the dark mode setting is changed.
func (pg *Page) RestyleWidgets() {
	for i := range pg.recipients {
		recipient := pg.recipients[i]
		recipient.restyleWidgets()
	}
}

func (pg *Page) UpdateSelectedUTXOs(utxos []*sharedW.UnspentOutput) {
	selectionCoinType := cointype.CoinTypeVAR
	if pg.coinTypeDropdown != nil {
		selectionCoinType = pg.coinTypeDropdown.Selected()
	}
	pg.selectedUTXOs = selectedUTXOsInfo{
		selectedUTXOs:       utxos,
		sourceAccount:       pg.accountDropdown.SelectedAccount(),
		totalUTXOsAmountBig: new(big.Int),
		coinType:            selectionCoinType,
	}
	if len(utxos) > 0 {
		for _, elem := range utxos {
			pg.selectedUTXOs.totalUTXOsAmount += elem.Amount.ToInt()
			// Lossless big.Int sum: utxoAtoms reads SKAAmountAtoms for SKA
			// (the int64 Amount is 0 there) and Amount for VAR.
			pg.selectedUTXOs.totalUTXOsAmountBig.Add(pg.selectedUTXOs.totalUTXOsAmountBig, utxoAtoms(elem))
		}
	}

	// Immediately rebuild the authored tx so the new selection is pushed into
	// the wallet's TxAuthoredInfo (via constructTx → NewUnsignedTx) and the
	// form's fee/total reflect it RIGHT NOW. Without this, the selection only
	// sat in pg.selectedUTXOs and did not reach the tx until the next form
	// interaction — so a user who clicked "Done" and then "Send" broadcast the
	// previous AUTO-selected tx, and the hand-picked UTXOs took effect only in
	// the post-broadcast rebuild (too late). See manual-selection log trace:
	// broadcast at .748 used auto, applied=1 appeared at .766.
	pg.validateAndConstructTx()
}

// OnNavigatedTo is called when the page is about to be displayed and
// may be used to initialize page features that are only relevant when
// the page is displayed.
// Part of the load.Page interface.
func (pg *Page) OnNavigatedTo() {
	pg.RestyleWidgets()
	if pg.selectedWallet == nil {
		return
	}

	if !pg.selectedWallet.IsSynced() {
		// Events are disabled until the wallet is fully synced.
		return
	}

	pg.walletDropdown.ListenForTxNotifications(pg.ParentWindow()) // listener is stopped in OnNavigatedFrom()

	// Auto-default the asset dropdown to a coin type that has a spendable
	// balance on the chosen source account. Without this, a wallet holding
	// only SKA-n (zero VAR) lands on the Send tab with the dropdown stuck
	// on VAR (its constructor default), and the first character typed into
	// "Сума" returns "no spendable VAR in this account" — the form refuses
	// to author against the wrong coin type even though SKA UTXOs are
	// right there. Picks the first coin with positive spendable atoms,
	// preserving the current selection when it's already usable.
	pg.autoDefaultCoinTypeFromBalance()

	pg.usdExchangeSet = false
	if pg.AssetsManager.ExchangeRateFetchingEnabled() {
		pg.usdExchangeSet = pg.AssetsManager.RateSource.Ready()
		go pg.fetchExchangeRate()
	} else {
		// If exchange rate is not supported, validate and construct the TX.
		pg.validateAndConstructTx()
	}

	if pg.selectedWallet.GetAssetType() == libUtil.BTCWalletAsset && pg.isFeerateAPIApproved() {
		// This API call may take sometime to return. Call this before and cache
		// results.
		// TODO: @Wisdom Why was this line necessary?
		// go load.GetAPIFeeRate(pg.selectedWallet)
		go pg.feeRateSelector.UpdatedFeeRate(pg.selectedWallet)
	}
}

// OnDarkModeChanged is triggered whenever the dark mode setting is changed
// to enable restyling UI elements where necessary.
// Satisfies the load.DarkModeChangeHandler interface.
func (pg *Page) OnDarkModeChanged(_ bool) {
	pg.RestyleWidgets()
}

func (pg *Page) fetchExchangeRate() {
	if pg.isFetchingExchangeRate {
		return
	}
	pg.isFetchingExchangeRate = true
	market, err := utils.USDMarketFromAsset(pg.selectedWallet.GetAssetType())
	if err != nil {
		log.Errorf("Unsupported asset type: %s", pg.selectedWallet.GetAssetType())
		pg.isFetchingExchangeRate = false
		return
	}

	rate := pg.AssetsManager.RateSource.GetTicker(market, false) // okay to fetch latest rate, this is a goroutine
	if rate == nil || rate.LastTradePrice <= 0 {
		pg.isFetchingExchangeRate = false
		return
	}

	// Hand the rate to the UI thread; applying it touches editor buffers
	// (updateRecipientExchangeRate / validateAndConstructTx → Editor.SetText),
	// which must not run from this goroutine.
	pg.pendingExchangeRate = rate.LastTradePrice
	pg.pendingExchangeRateApply.Store(true)
	pg.isFetchingExchangeRate = false
	pg.ParentWindow().Reload()
}

func (pg *Page) validateAndConstructTx() {
	// delete all the previous errors set earlier.
	pg.cleanAllRecipientErrors()

	if pg.isAllRecipientValidated() {
		pg.constructTx()
	} else {
		pg.clearEstimates()
		pg.showBalanceAfterSend()
	}
}

// manualSelectionFor returns the user's hand-picked UTXOs when they belong to
// the given source account, else nil (→ automatic selection). Matching is by
// account identity (WalletID + Number), NOT *Account pointer equality: the
// account dropdown can hand back a freshly-allocated Account value on each call
// (e.g. after a balance refresh / window Reload), so the old `==` pointer
// comparison would intermittently fail and silently drop a valid manual
// selection — the wallet then picked UTXOs automatically as if nothing was
// chosen (observed symptom: selection on the UTXO page had no effect).
func (pg *Page) manualSelectionFor(sourceAccount *sharedW.Account) []*sharedW.UnspentOutput {
	stored := pg.selectedUTXOs.sourceAccount
	if stored == nil || sourceAccount == nil || len(pg.selectedUTXOs.selectedUTXOs) == 0 {
		return nil
	}
	if stored.WalletID != sourceAccount.WalletID || stored.Number != sourceAccount.Number {
		return nil
	}
	// Reject a selection picked for a different coin type — its outpoints
	// belong to the other coin's UTXO set and would be silently dropped by the
	// coin-type filter in the input source, producing a confusing empty-input
	// "insufficient" failure while the form still claims a manual selection.
	if pg.coinTypeDropdown != nil && pg.selectedUTXOs.coinType != pg.coinTypeDropdown.Selected() {
		return nil
	}
	return pg.selectedUTXOs.selectedUTXOs
}

func (pg *Page) constructTx() {
	if pg.accountDropdown == nil {
		return
	}
	sourceAccount := pg.accountDropdown.SelectedAccount()
	if sourceAccount == nil {
		return
	}
	selectedUTXOs := pg.manualSelectionFor(sourceAccount)

	err := pg.selectedWallet.NewUnsignedTx(sourceAccount.Number, selectedUTXOs)
	if err != nil {
		pg.setRecipientsAmountErr(err)
		pg.clearEstimates()
		return
	}

	// Tag the in-progress transaction with the selected coin type. SetTxCoinType
	// is a no-op when the choice hasn't changed.
	if dcrAsset, ok := pg.selectedWallet.(*dcr.Asset); ok && pg.coinTypeDropdown != nil {
		ct := pg.coinTypeDropdown.Selected()
		if err := dcrAsset.SetTxCoinType(ct); err != nil {
			pg.setRecipientsAmountErr(err)
			pg.clearEstimates()
			return
		}
		// Defensive re-sync: the AccountDropdown's coin type ought to
		// have been switched by applyCoinType when the asset selector
		// changed, but that callback can race with construct cycles
		// triggered from other dropdowns (account, amount). If they
		// drift the user sees the SKA balance row while the asset
		// selector says VAR (or vice versa) and the form refuses to
		// send because spendable comes from the wrong coin. Pushing the
		// current asset selection through every construct pass keeps
		// both dropdowns and the in-memory TxCoinType pointing at the
		// same thing.
		if pg.accountDropdown != nil {
			pg.accountDropdown.SetCoinType(ct)
		}
		// Also keep every recipient's amount editor in sync — same
		// drift problem on the float→atoms conversion side.
		for _, rc := range pg.recipients {
			rc.setCoinType(ct)
		}
		// Re-apply the user's custom fee override (if any) AFTER
		// NewUnsignedTx wiped TxAuthoredInfo and SetTxCoinType reset
		// its coin. The page-level pg.feeRateOverride is the source of
		// truth across construct cycles; this line is what makes the
		// "Apply" button stick beyond the very next keystroke.
		// SetFeeRateOverride re-validates against the (potentially
		// just-switched) coin's bounds — if the user changed coin
		// after applying a SKA1 rate to e.g. VAR, the validator will
		// reject it and the construct falls back to default. Errors
		// here are status-bar noise, not fatal; log + carry on so the
		// rest of the send flow still works.
		if !pg.feeRateOverride.IsZero() {
			if err := dcrAsset.SetFeeRateOverride(pg.feeRateOverride); err != nil {
				log.Warnf("constructTx: re-apply fee override failed (coin=%s): %v — falling back to default", ct, err)
				// Drop the override so subsequent cycles don't keep
				// trying to apply an invalid rate.
				pg.feeRateOverride = cointype.Zero()
				pg.customFeeStatus = err.Error()
				pg.customFeeStatusIsErr = true
			}
		}
	}

	totalCost, balanceAfterSend, totalAmount, err := pg.addSendDestination()
	if err != nil {
		return
	}

	feeAndSize, err := pg.selectedWallet.EstimateFeeAndSize()
	if err != nil {
		pg.setRecipientsAmountErr(err)
		pg.clearEstimates()
		return
	}

	feeAtom := feeAndSize.Fee.UnitValue
	wal := pg.selectedWallet

	// Coin-type-aware display strings. wal.ToAmount(N).String() and the
	// AssetAmount.String() returned from addSendDestination both
	// hard-code the VAR suffix via dcrutil.Amount.String() — so a SKA
	// send was rendering "X.XX VAR" in the fee, total, balance-after, and
	// send-amount rows. dcr.FormatTxAmount dispatches on the selected
	// CoinType and emits the correct unit ("VAR" / "SKA1" / "SKA2"…).
	displayCoinType := uint8(cointype.CoinTypeVAR)
	if pg.coinTypeDropdown != nil {
		displayCoinType = uint8(pg.coinTypeDropdown.Selected())
	}

	// populate display data
	// Route fee through FormatTxAmountBig with feeAndSize.Fee.UnitValueBig
	// so a SKA fee that exceeds int64 (large custom-fee rates) renders
	// with full atom precision instead of the clamped MaxInt64 placeholder.
	pg.txFee = dcr.FormatTxAmountBig(feeAndSize.Fee.UnitValueBig, feeAtom, displayCoinType)

	pg.feeRateSelector.EstSignedSize = fmt.Sprintf("%d Bytes", feeAndSize.EstimatedSignedSize)
	pg.feeRateSelector.TxFee = pg.txFee
	pg.feeRateSelector.SetFeerate(feeAndSize.FeeRate)
	// Use big.Int strings when populated (SKA flows above int64); else
	// fall back to the int64 channel. addSendDestination clears the big
	// fields on the in-range path so this stays a pure dispatch.
	pg.totalCost = dcr.FormatTxAmountBig(pg.totalCostBig, totalCost.ToInt(), displayCoinType)
	pg.balanceAfterSend = dcr.FormatTxAmountBig(pg.balanceAfterSendBig, balanceAfterSend.ToInt(), displayCoinType)
	pg.sendAmount = dcr.FormatTxAmountBig(pg.totalSendAmountBig, totalAmount, displayCoinType)
	pg.destinationAddress = pg.getDestinationAddresses()
	pg.destinationAccount = pg.getDestinationAccounts()
	pg.sourceAccount = sourceAccount

	// SKA tokens have no USD pairing in v1. .ToCoin()/Fee.CoinValue divide
	// atoms by 1e8 (the VAR scale) — applied to 1e18-scaled SKA atoms and then
	// multiplied by the VAR-USD rate, that yields dollar figures inflated by
	// ~1e10 (a 5 SKA send showed billions). Only compute USD for VAR, matching
	// the per-recipient suppression already in send_amount.go (!IsSKA()). For
	// SKA we blank every USD string so neither the totals rows nor the confirm
	// modal render a bogus value.
	isSKASend := pg.coinTypeDropdown != nil && pg.coinTypeDropdown.Selected().IsSKA()
	if pg.exchangeRate != -1 && pg.usdExchangeSet && !isSKASend {
		pg.feeRateSelector.USDExchangeSet = true
		pg.txFeeUSD = fmt.Sprintf("$%.4f", utils.CryptoToUSD(pg.exchangeRate, feeAndSize.Fee.CoinValue))
		pg.feeRateSelector.TxFeeUSD = pg.txFeeUSD
		pg.totalCostUSD = utils.FormatAsUSDString(pg.Printer, utils.CryptoToUSD(pg.exchangeRate, totalCost.ToCoin()))
		pg.balanceAfterSendUSD = utils.FormatAsUSDString(pg.Printer, utils.CryptoToUSD(pg.exchangeRate, balanceAfterSend.ToCoin()))

		usdAmount := utils.CryptoToUSD(pg.exchangeRate, wal.ToAmount(totalAmount).ToCoin())
		pg.sendAmountUSD = utils.FormatAsUSDString(pg.Printer, usdAmount)
	} else {
		pg.feeRateSelector.USDExchangeSet = false
		pg.txFeeUSD = ""
		pg.feeRateSelector.TxFeeUSD = ""
		pg.totalCostUSD = ""
		pg.balanceAfterSendUSD = ""
		pg.sendAmountUSD = ""
	}

	pg.checkAssetCoverage(sourceAccount, totalAmount, feeAtom, feeAndSize.Fee.UnitValueBig, pg.totalSendAmountBig)
}

// checkAssetCoverage validates that the selected CoinType has enough balance to
// cover (amount + fee) on the source account. Monetarium pays the fee in the
// SAME asset as the transfer, so a SKA-1 transfer with no SKA-1 in the wallet
// fails even when the user has plenty of VAR.
func (pg *Page) checkAssetCoverage(sourceAccount *sharedW.Account, totalAmount, feeAtom int64, feeBigStr, totalAmountBigStr string) {
	dcrAsset, ok := pg.selectedWallet.(*dcr.Asset)
	if !ok || pg.coinTypeDropdown == nil {
		return
	}
	ct := pg.coinTypeDropdown.Selected()
	if ct.IsVAR() {
		// Existing account.Balance.Spendable already reflects VAR; the form's
		// per-recipient amount validation handles the over-spend case.
		return
	}
	bal, err := dcrAsset.GetCoinBalance(sourceAccount.Number, ct)
	if err != nil {
		log.Errorf("checkAssetCoverage: GetCoinBalance(%s): %v", ct, err)
		return
	}
	// Fee component: prefer the lossless big-string. A high custom SKA fee
	// rate (≳33 SKA1/KB) produces a per-tx fee above int64, so feeAtom is the
	// MaxInt64 placeholder — using it here would compute a nonsensical
	// `amount + 9.22 SKA1` requirement and either falsely pass or falsely
	// fail. feeBigStr carries the real atom count.
	feeBig := big.NewInt(feeAtom)
	if feeBigStr != "" {
		if parsed, ok := new(big.Int).SetString(feeBigStr, 10); ok {
			feeBig = parsed
		}
	}
	// Build the required-atom big.Int from the lossless big-string when the
	// SKA amount overflowed int64 (constructTx populated totalAmountBigStr
	// in that case). Otherwise lift the int64 totalAmount. Add the (real) fee.
	var required *big.Int
	if totalAmountBigStr != "" {
		if parsed, ok := new(big.Int).SetString(totalAmountBigStr, 10); ok {
			required = new(big.Int).Add(parsed, feeBig)
		}
	}
	if required == nil {
		required = new(big.Int).Add(big.NewInt(totalAmount), feeBig)
	}
	available := bal.SKASpendable.BigInt()
	if available == nil {
		available = new(big.Int)
	}
	if available.Cmp(required) < 0 {
		// Surface as the localized "insufficient funds" so it shows
		// translated; the detailed atom-by-atom error goes to the log
		// for diagnosis.
		log.Warnf("checkAssetCoverage: %s shortfall — have %s atoms, need %s atoms",
			ct, available.String(), required.String())
		pg.setRecipientsAmountErr(fmt.Errorf("%s", libUtil.ErrInsufficientBalance))
	}
}

func (pg *Page) addSendDestination() (sharedW.AssetAmount, sharedW.AssetAmount, int64, error) {
	var totalCost int64

	sourceAccount := pg.accountDropdown.SelectedAccount()
	selectedUTXOs := pg.manualSelectionFor(sourceAccount)

	// Note: we no longer call EstimateFeeAndSize up here. The legacy code
	// did it BEFORE adding the destinations to TxAuthoredInfo, which made
	// the wallet build an empty tx for the estimate; that worked for VAR
	// with positive balance but errored for SKA-only accounts (the wallet
	// falls back to txCoinType=VAR when outputs is empty, then tries
	// VAR input selection against a zero VAR balance and fails — which
	// returned a "no spendable VAR" error that clearEstimates+early-return
	// then wiped to a "- SKA1" placeholder with no diagnostic shown to
	// the user). The recipient destinations are added first (loop below),
	// and EstimateFeeAndSize moves to the end so it operates on a real
	// outputs slice.
	var feeAtom int64
	spendableAmount := sourceAccount.Balance.Spendable.ToInt()
	// spendableBig holds the lossless big.Int spendable (atoms) for the
	// active coin. Used to compute balanceAfterSend without int64
	// clamping, and to detect the case where the user has > int64 atoms
	// available and SendMax would otherwise silently send only ~9.22 SKA.
	var spendableBig *big.Int
	if pg.coinTypeDropdown != nil && pg.coinTypeDropdown.Selected().IsSKA() {
		if dcrAsset, ok := pg.selectedWallet.(*dcr.Asset); ok {
			if bal, err := dcrAsset.GetCoinBalance(sourceAccount.Number, pg.coinTypeDropdown.Selected()); err == nil {
				// SKAAmount is a value type; BigInt() returns the
				// underlying *big.Int (never nil for a real balance).
				// Fall back to the int64-clamped Spendable if the
				// SKA accessor somehow yields a nil internal — shouldn't
				// happen for active balances but defensive.
				spendableBig = bal.SKASpendable.BigInt()
				if spendableBig == nil {
					spendableBig = big.NewInt(int64(bal.Spendable))
				}
				// Mirror the big-int spendable into the int64 channel so
				// VAR-style arithmetic below behaves sanely for SKA
				// balances that fit. For balances that overflow int64
				// we clamp here, but the SendMax path below detects this
				// and refuses to silently truncate the request — the
				// authoring path is still int64-capped in phase 1
				// (see MakeCoinTypeTxOutput).
				if spendableBig.IsInt64() {
					spendableAmount = spendableBig.Int64()
				} else {
					spendableAmount = int64(bal.Spendable) // already clamped to MaxInt64 upstream
				}
			}
		}
	}
	if len(selectedUTXOs) > 0 {
		// Spendable for a manual selection is the sum of the hand-picked
		// UTXOs, not the whole account. Use the lossless big.Int total (the
		// int64 totalUTXOsAmount is 0 for SKA, whose value lives in
		// SKAAmountAtoms) so balanceAfterSend and Max are correct for SKA.
		if pg.selectedUTXOs.totalUTXOsAmountBig != nil {
			spendableBig = new(big.Int).Set(pg.selectedUTXOs.totalUTXOsAmountBig)
		} else {
			spendableBig = big.NewInt(pg.selectedUTXOs.totalUTXOsAmount)
		}
		if spendableBig.IsInt64() {
			spendableAmount = spendableBig.Int64()
		} else {
			spendableAmount = int64(^uint64(0) >> 1) // MaxInt64 placeholder; big channel carries truth
		}
	}

	wal := pg.selectedWallet
	var totalSendAmount int64
	// Big.Int accumulators populated alongside the int64 ones; we use the
	// big channel when totals overflow int64 (SKA balances > 9.22 SKA).
	totalCostBig := new(big.Int)
	totalSendAmountBig := new(big.Int)

	// First pass: add destinations so EstimateFeeAndSize below builds the
	// real tx, not an empty one. Fee defaults to 0 inside this pass
	// because we don't know the actual fee yet; it gets added to the
	// totals after the post-loop EstimateFeeAndSize call.
	for _, recipient := range pg.recipients {
		destinationAddress := recipient.destinationAddress()
		amountAtom, amountAtomBig, SendMax := recipient.validAmountBig()
		var err error
		if dcrAsset, ok := pg.selectedWallet.(*dcr.Asset); ok {
			// Only attach the subtract-fee flag to a non-SendMax recipient;
			// SendMax already sweeps the whole balance via changeSource so
			// the notion of "fee from recipient" doesn't apply there.
			subtractFee := pg.subtractFeeFromRecipient && !SendMax
			err = dcrAsset.AddSendDestinationBig(recipient.id, destinationAddress, amountAtom, amountAtomBig, SendMax, subtractFee)
		} else {
			err = pg.selectedWallet.AddSendDestination(recipient.id, destinationAddress, amountAtom, SendMax)
		}
		if err != nil {
			if strings.Contains(err.Error(), "amount") {
				recipient.amountValidationError(err.Error())
			} else {
				recipient.addressValidationError(err.Error())
			}
			pg.clearEstimates()
			// Skip folding this destination into the totals: validAmountBig
			// returns amountAtom == -1 on a parse/validation error, and the
			// wallet's destinations map does not contain it (AddSendDestination
			// failed). Folding -1 atoms would make the page totals diverge from
			// the actual output set the estimate is built against.
			continue
		}

		// Build the recipient's atom contribution as big.Int.
		var amountBig *big.Int
		if amountAtomBig != "" {
			if parsed, ok := new(big.Int).SetString(amountAtomBig, 10); ok {
				amountBig = parsed
			}
		}
		if amountBig == nil {
			amountBig = big.NewInt(amountAtom)
		}
		totalSendAmountBig.Add(totalSendAmountBig, amountBig)
		totalCostBig.Add(totalCostBig, amountBig)

		totalSendAmount += amountAtom
		totalCost += amountAtom
	}

	// Now that destinations are in place, ask the wallet for a real
	// fee+size estimate against the actual outputs. For the SKA path the
	// wallet picks UTXOs of the matching coin type via
	// MakeInputSourceWithCoinType — so this works regardless of VAR
	// balance, which is what the legacy pre-loop call could not handle.
	feeAndSize, feeErr := pg.selectedWallet.EstimateFeeAndSize()
	if feeErr != nil {
		// Surface the real reason instead of failing silently — a high
		// custom fee whose amount exceeds the spendable balance makes the
		// wallet's input selection return InsufficientBalance here, and the
		// form would otherwise just blank ("- SKA1") with no diagnostic.
		log.Errorf("addSendDestination: EstimateFeeAndSize failed: %v", feeErr)
		pg.setRecipientsAmountErr(feeErr)
		return nil, nil, 0, feeErr
	}
	feeAtom = feeAndSize.Fee.UnitValue
	// Prefer the lossless big-string when EstimateFeeAndSize populated it
	// (SKA fees can exceed int64 once custom-fee rates approach the
	// 1000× cap — see txauthor.go comment). big.NewInt(feeAtom) on the
	// clamped int64 value would wrap the totalCostBig math back into
	// "fee == MaxInt64 atoms ≈ 9.22 SKA1" no matter what the real rate
	// computed, exactly the symptom from bug #1 of this batch where
	// 33 SKA1/KB displayed ~0.002 SKA1 fee.
	var feeBig *big.Int
	if s := feeAndSize.Fee.UnitValueBig; s != "" {
		if parsed, ok := new(big.Int).SetString(s, 10); ok {
			feeBig = parsed
		}
	}
	if feeBig == nil {
		feeBig = big.NewInt(feeAtom)
	}
	// When the fee is subtracted from the recipient (the advanced-options
	// toggle), the sender spends EXACTLY the entered amount — the fee comes
	// out of the recipient's output, so it must NOT be added to
	// "Загальна сума" or reduce "Баланс після відправлення" beyond the
	// amount itself. This applies to SendMax too: a Max sweep with the
	// checkbox on shows the FULL spendable in the amount field (the receiver
	// gets spendable − fee, which is what the sweep's changeSource authoring
	// already delivers), so the fee must not be folded into the totals a
	// second time.
	sffaApplies := pg.subtractFeeFromRecipient
	if !sffaApplies {
		// EstimateFeeAndSize returns ONE fee for the whole serialized tx, not a
		// per-output fee. Add it exactly once — the old `feeAtom * len(recipients)`
		// over-charged a multi-recipient send (e.g. 3× the real fee in "Загальна
		// сума") and could falsely trip the insufficient-funds path.
		totalCost += feeAtom
		totalCostBig.Add(totalCostBig, feeBig)
	}

	// SendMax pass — runs AFTER fee estimate so we can subtract a real
	// fee from the spendable. Two correctness traps it has to thread:
	//
	//  1. Float round-trip. Editor text is the broadcast source of truth
	//     (validAmountBig re-parses it via the lossless decimal parser).
	//     A SKA send that goes via int64 → float64 / 1e18 → "%.18f" drops
	//     the last ~3 decimal digits at any magnitude (float64 mantissa
	//     is 53 bits) — a 5 SKA Max click broadcasts 4.999…567 atoms,
	//     leaving dust. Compute max in big.Int and write the editor with
	//     ToDecimalString so the user's "send everything" actually sends
	//     everything.
	//
	//  2. Underflow when fee > spendable (dust account, paid-down balance).
	//     The legacy `spendableAmount - feeAtom` int64 subtraction returned
	//     a negative number that fed into setAmount and silently became a
	//     bogus editor value. Detect insufficient funds explicitly and
	//     surface the localized error to the recipient instead of
	//     proceeding into clearEstimates limbo.
	//
	// Phase-1 caveat: AddSendDestinationBig pipes the big amount through
	// AddSendDestination's int64 channel internally, so a single output
	// > MaxInt64 atoms (~9.22 SKA) still can't be authored. We refuse
	// SendMax in that case rather than silently emitting a clamped tx.
	for _, recipient := range pg.recipients {
		_, _, SendMax := recipient.validAmountBig()
		if !SendMax {
			continue
		}
		// Compute the Max fill losslessly in big.Int. With "subtract fee
		// from recipient" on, the fill is the FULL spendable (the fee comes
		// out of the swept amount — the sweep authoring already delivers
		// spendable − fee to the receiver); otherwise it is spendable − fee.
		// Fall through to the int64 path only when no big-int spendable was
		// computed (defensive — addSendDestination always sets spendableBig).
		var maxBig *big.Int
		if spendableBig != nil {
			if pg.subtractFeeFromRecipient {
				maxBig = new(big.Int).Set(spendableBig)
			} else {
				maxBig = new(big.Int).Sub(spendableBig, feeBig)
			}
		} else if pg.subtractFeeFromRecipient {
			maxBig = big.NewInt(spendableAmount)
		} else {
			maxBig = big.NewInt(spendableAmount - feeAtom)
		}
		// A SendMax recipient sweeps only what is LEFT after the fee AND any
		// fixed-amount recipients in the same tx. The first pass already
		// accumulated those fixed amounts into totalSendAmountBig (the SendMax
		// recipient itself contributed 0 there, and only one SendMax recipient
		// is allowed), so subtracting it gives the true sweepable remainder.
		// Without this, a Max+fixed tx overstated totalCost and rendered a
		// negative "Баланс після відправлення".
		maxBig.Sub(maxBig, totalSendAmountBig)
		if maxBig.Sign() <= 0 {
			recipient.amountValidationError(values.String(values.StrInsufficientFund))
			pg.clearEstimates()
			continue
		}
		// No int64 ceiling here. SendMax authoring uses
		// OutputSelectionAlgorithmAll + a changeSource (see
		// constructTransaction) — the wallet sweeps the whole account and
		// there is no fixed output to overflow int64. The editor value is
		// display-only for SendMax, and setAmountBig renders it losslessly
		// via ToDecimalString even above int64. The old `!maxBig.IsInt64()`
		// refusal made Max unusable for any SKA balance over ~9.22 SKA
		// (i.e. essentially all real SKA balances) — that was the reported
		// bug: VAR Max worked (VAR ≤ 2.1e15 atoms always fits int64) but
		// SKA Max errored instead of filling spendable-minus-fee.
		recipient.setAmountBig(maxBig)
		// maxBig (spendable − fee) IS the amount this SendMax recipient
		// sends. The first pass above added 0 for it (validAmountBig returns
		// 0 while SendMax is set), and the fee-add block already folded the
		// fee into the totals — so without folding maxBig in here, totalCost
		// would equal just the fee and "Баланс після відправлення" would
		// render the full untouched balance instead of ~0. After this:
		// totalSendAmount == swept amount, totalCost == swept + fee ==
		// spendable, hence balanceAfterSend == spendable − totalCost ≈ 0
		// (a Max send sweeps everything; the change output is empty).
		totalSendAmountBig.Add(totalSendAmountBig, maxBig)
		totalCostBig.Add(totalCostBig, maxBig)
		if maxBig.IsInt64() {
			totalSendAmount += maxBig.Int64()
			totalCost += maxBig.Int64()
		}
	}
	// Compute balance-after-send and totals in big.Int when the SKA
	// balance OR the cumulative cost has overflowed int64. The int64
	// channel stays populated for VAR and in-range SKA paths so the
	// returned AssetAmount value is meaningful when small; the
	// big.Int companions feed the lossless display formatters.
	var balanceAfterSend sharedW.AssetAmount
	useBig := (spendableBig != nil && !spendableBig.IsInt64()) || !totalCostBig.IsInt64()
	if useBig && spendableBig != nil {
		remainBig := new(big.Int).Sub(spendableBig, totalCostBig)
		// Stash the big.Int remainder where constructTx can read it for
		// the display string; the int64 wrapper here is just a
		// placeholder for the legacy AssetAmount return shape.
		pg.balanceAfterSendBig = remainBig.String()
		pg.totalCostBig = totalCostBig.String()
		pg.totalSendAmountBig = totalSendAmountBig.String()
		clamp := remainBig
		if !clamp.IsInt64() {
			clamp = big.NewInt(int64(^uint64(0) >> 1)) // MaxInt64 sentinel
		}
		balanceAfterSend = wal.ToAmount(clamp.Int64())
	} else {
		pg.balanceAfterSendBig = ""
		pg.totalCostBig = ""
		pg.totalSendAmountBig = ""
		balanceAfterSend = wal.ToAmount(spendableAmount - totalCost)
	}
	return wal.ToAmount(totalCost), balanceAfterSend, totalSendAmount, nil

}

func (pg *Page) isAllRecipientValidated() bool {
	isValid := true
	for i := range pg.recipients {
		recipient := pg.recipients[i]
		isValid = isValid && recipient.isValidated()
	}
	return isValid
}

func (pg *Page) cleanAllRecipientErrors() {
	for i := range pg.recipients {
		recipient := pg.recipients[i]
		recipient.cleanAllErrors()
	}
}

func (pg *Page) updateRecipientExchangeRate() {
	for i := range pg.recipients {
		recipient := pg.recipients[i]
		recipient.amount.setExchangeRate(pg.exchangeRate)
	}
}

func (pg *Page) setAssetTypeForRecipients() {
	for i := range pg.recipients {
		recipient := pg.recipients[i]
		recipient.setDestinationAssetType(pg.selectedWallet.GetAssetType())
	}
}

func (pg *Page) initAccountsSelectorForRecipients(account *sharedW.Account) {
	for i := range pg.recipients {
		recipient := pg.recipients[i]
		recipient.initializeAccountSelectors(account)
	}
}

func (pg *Page) setRecipientsAmountErr(err error) {
	// Route through TranslateErr so libwallet-side English errors like
	// "no spendable VAR in this account (need confirmed UTXOs >= 2 blocks
	// deep)" surface as the localized "Недостатньо коштів" instead of
	// leaking the raw English + technical confirmation count to the
	// recipient amount label.
	msg := values.TranslateErr(err.Error())
	for i := range pg.recipients {
		recipient := pg.recipients[i]
		recipient.amountValidationError(msg)
	}
	pg.clearEstimates()
}

func (pg *Page) allRecipientsIsValid() bool {
	isValid := pg.selectedWallet != nil && pg.selectedWallet.IsSynced()
	for i := range pg.recipients {
		recipient := pg.recipients[i]
		isValid = isValid && recipient.isValidated()
	}
	return isValid
}

func (pg *Page) validateAllRecipientsAmount() bool {
	isValid := true
	for i := range pg.recipients {
		recipient := pg.recipients[i]
		recipient.validateAmount()
	}
	return isValid
}

func (pg *Page) resetRecipientsFields() {
	for i := range pg.recipients {
		recipient := pg.recipients[i]
		recipient.resetFields()
	}
}

func (pg *Page) getDestinationAccounts() []*sharedW.Account {
	accounts := make([]*sharedW.Account, 0)
	for i := range pg.recipients {
		recipient := pg.recipients[i]
		destinationAccount := recipient.destinationAccount()
		if destinationAccount != nil && !recipient.isSendToAddress() {
			accounts = append(accounts, destinationAccount)
		}
	}
	return accounts
}

func (pg *Page) getDestinationAddresses() []string {
	addresses := make([]string, 0)
	for i := range pg.recipients {
		recipient := pg.recipients[i]
		destinationAddress := recipient.destinationAddress()
		if destinationAddress != "" {
			addresses = append(addresses, destinationAddress)
		}
	}
	return addresses
}

func (pg *Page) showBalanceAfterSend() {
	if pg.accountDropdown != nil {
		sourceAccount := pg.accountDropdown.SelectedAccount()
		if sourceAccount == nil || sourceAccount.Balance == nil {
			return
		}
		// Render the idle "balance after send" in the ACTIVE coin. The legacy
		// path always read the VAR Spendable, so with Актив=SKA1 the row
		// showed "0 VAR" (the account's VAR balance) instead of the SKA
		// spendable in SKA units.
		if pg.coinTypeDropdown != nil && pg.coinTypeDropdown.Selected().IsSKA() {
			ct := pg.coinTypeDropdown.Selected()
			if dcrAsset, ok := pg.selectedWallet.(*dcr.Asset); ok {
				if bal, err := dcrAsset.GetCoinBalance(sourceAccount.Number, ct); err == nil {
					atoms := "0"
					if b := bal.SKASpendable.BigInt(); b != nil {
						atoms = b.String()
					}
					pg.balanceAfterSend = dcr.FormatTxAmountBig(atoms, 0, uint8(ct))
					pg.balanceAfterSendUSD = "" // no USD pairing for SKA
					return
				}
			}
		}
		balanceAfterSend := sourceAccount.Balance.Spendable
		pg.balanceAfterSend = balanceAfterSend.String()
		pg.balanceAfterSendUSD = utils.FormatAsUSDString(pg.Printer, utils.CryptoToUSD(pg.exchangeRate, balanceAfterSend.ToCoin()))
	}
}

// activeAssetSymbol returns the symbol the page should annotate amounts with
// (e.g. "VAR", "SKA-1"). Falls back to the wallet's asset type — which is the
// legacy single-coin display ("DCR" on Decred forks) — only when the
// CoinType dropdown isn't initialised yet.
func (pg *Page) activeAssetSymbol() string {
	if pg.coinTypeDropdown != nil {
		return dcr.CoinSymbol(pg.coinTypeDropdown.Selected())
	}
	return string(pg.selectedWallet.GetAssetType())
}

func (pg *Page) clearEstimates() {
	symbol := pg.activeAssetSymbol()
	pg.txFee = " - " + symbol
	pg.feeRateSelector.TxFee = pg.txFee
	pg.txFeeUSD = " - "
	pg.feeRateSelector.TxFeeUSD = pg.txFeeUSD
	pg.totalCost = " - " + symbol
	pg.totalCostUSD = " - "
	pg.balanceAfterSend = " - " + symbol
	pg.balanceAfterSendUSD = " - "
	pg.sendAmount = " - "
	pg.sendAmountUSD = " - "
	pg.feeRateSelector.SetFeerate(0)
}

// HandleUserInteractions is called just before Layout() to determine
// if any user interaction recently occurred on the page and may be
// used to update the page's UI components shortly before they are
// displayed.
// Part of the load.Page interface.
func (pg *Page) HandleUserInteractions(gtx C) {
	// Drain the broadcast-success flag set from the goroutine. We must
	// do this on the UI thread (Gio editor SetText is racy with Layout
	// reads), and we must do it BEFORE per-frame validation runs below —
	// otherwise validateAllRecipientsAmount sees the stale "1" against
	// the new zero balance, flips the recipient row into the danger
	// state, and clearEstimates writes "- SKA1" placeholders. Doing the
	// reset first means the rest of this frame sees an empty form.
	if pg.pendingBroadcastReset.CompareAndSwap(true, false) {
		pg.resetRecipientsFields()
		pg.clearEstimates()
		// Re-pick the active coin in case the just-sent tx emptied the
		// previously-selected coin (e.g. sent the entire SKA1 balance —
		// next default should slide to the next non-empty coin or back
		// to VAR if nothing has spendable).
		pg.autoDefaultCoinTypeFromBalance()
	}

	// Apply a freshly-fetched USD rate on the UI thread (the fetch ran in a
	// goroutine). updateRecipientExchangeRate + validateAndConstructTx write
	// editor buffers, so they cannot run from the fetch goroutine.
	if pg.pendingExchangeRateApply.CompareAndSwap(true, false) {
		pg.exchangeRate = pg.pendingExchangeRate
		pg.updateRecipientExchangeRate()
		pg.validateAndConstructTx()
	}

	// Custom-fee Apply: parse the editor's number with the coin-type-aware
	// big.Int parser (SKA fees > int64 are pathological but the bounds
	// allow up to 1000× MinRelayTxFee, which on a 1e18-atoms/coin SKA can
	// exceed int64 quickly), push through SetFeeRateOverride, and surface
	// either the localised "applied" string or the validator's error. The
	// editor itself is left intact so the user can tweak and reapply.

	// Subtract-fee-from-recipient toggle. Re-estimate on change so the
	// "Загальна сума" / "Баланс після відправлення" rows reflect whether the
	// fee comes out of the recipient's output (sender spends exactly the
	// entered amount) or the sender's change (the default).
	if pg.subtractFeeCheckbox.CheckBox.Update(gtx) {
		pg.subtractFeeFromRecipient = pg.subtractFeeCheckbox.CheckBox.Value
		pg.validateAndConstructTx()
	}

	// The subtract-fee card is hidden for multiple recipients (see layout.go), so
	// the fee must always come from the sender then. If the toggle was enabled
	// while there was a single recipient and the user then adds another, clear it
	// (and re-estimate). Fires once on the 1->2 transition; idempotent after.
	if len(pg.recipients) > 1 && pg.subtractFeeFromRecipient {
		pg.subtractFeeFromRecipient = false
		pg.subtractFeeCheckbox.CheckBox.Value = false
		pg.validateAndConstructTx()
	}

	if pg.customFeeApplyBtn.Clicked(gtx) {
		if dcrAsset, ok := pg.selectedWallet.(*dcr.Asset); ok {
			text := strings.TrimSpace(pg.customFeeEditor.Editor.Text())
			ct := cointype.CoinTypeVAR
			if pg.coinTypeDropdown != nil {
				ct = pg.coinTypeDropdown.Selected()
			}
			atomsBig, parseErr := dcr.ParseAmountToAtomsBig(text, ct)
			if parseErr != nil {
				pg.customFeeStatus = parseErr.Error()
				pg.customFeeStatusIsErr = true
			} else if atomsBig == nil || atomsBig.Sign() <= 0 {
				pg.customFeeStatus = values.String(values.StrInvalidAmount)
				pg.customFeeStatusIsErr = true
			} else {
				rate := cointype.NewSKAAmount(atomsBig)
				if err := dcrAsset.SetFeeRateOverride(rate); err != nil {
					// Map sentinel errors to localised strings + coin-
					// unit-formatted bounds. The raw err.Error() is
					// English with raw-atom numbers; substitute a
					// translation that talks about "X.YZ SKA1 за КБ"
					// instead of "1000000000000000000 atoms/KB".
					minR, maxR := dcrAsset.FeeRateBounds()
					switch {
					case errors.Is(err, dcr.ErrFeeRateBelowMin):
						pg.customFeeStatus = values.StringF(values.StrFeeRateBelowMin,
							dcr.FormatTxAmountBig(atomsBig.String(), 0, uint8(ct)),
							dcr.FormatTxAmountBig(minR.String(), 0, uint8(ct)))
					case errors.Is(err, dcr.ErrFeeRateAboveMax):
						pg.customFeeStatus = values.StringF(values.StrFeeRateAboveMax,
							dcr.FormatTxAmountBig(atomsBig.String(), 0, uint8(ct)),
							dcr.FormatTxAmountBig(maxR.String(), 0, uint8(ct)))
					case errors.Is(err, dcr.ErrFeeRateNotSupported):
						pg.customFeeStatus = values.String(values.StrFeeRateNotSupported)
					default:
						pg.customFeeStatus = err.Error()
					}
					pg.customFeeStatusIsErr = true
				} else {
					// Persist at the page level so subsequent
					// constructTx cycles (which wipe TxAuthoredInfo
					// via NewUnsignedTx) can re-apply.
					pg.feeRateOverride = rate
					// Re-estimate FIRST so pg.txFee reflects the new rate,
					// then build the status string with BOTH the rate the
					// user typed AND the resulting effective fee. Users
					// confuse "rate per KB" with "the fee I'll pay" (see
					// the "1 + 33 = 10?" feedback) — showing both numbers
					// inline removes the surprise when the broadcast
					// charges rate × tx_size / 1000 instead of the bare
					// rate amount.
					pg.validateAndConstructTx()
					rateStr := dcr.FormatTxAmountBig(atomsBig.String(), 0, uint8(ct))
					effective := pg.txFee
					switch {
					case effective == "":
						// No recipient/amount yet — fee can't be estimated.
						// Show the rate-only fallback so we don't display
						// "Орієнтовна комісія: " with an empty tail.
						pg.customFeeStatus = values.StringF(values.StrCustomFeeApplied, rateStr, "—")
						pg.customFeeStatusIsErr = false
					case strings.Contains(effective, " - "):
						// clearEstimates() ran — the estimate FAILED at this
						// rate (almost always: the resulting fee exceeds the
						// account's spendable balance for this coin; see
						// addSendDestination's insufficient_balance log). The
						// old code still printed the green "applied … - SKA1"
						// success here, which read as "ok" while the totals
						// were blank and the amount row showed an error.
						// Surface a real error in the fee section instead.
						pg.customFeeStatus = values.StringF(values.StrCustomFeeTooHigh, rateStr)
						pg.customFeeStatusIsErr = true
					default:
						pg.customFeeStatus = values.StringF(values.StrCustomFeeApplied, rateStr, effective)
						pg.customFeeStatusIsErr = false
					}
				}
			}
		}
	}
	if pg.customFeeClearBtn.Clicked(gtx) {
		if dcrAsset, ok := pg.selectedWallet.(*dcr.Asset); ok {
			// Reset must be idempotent. It only clears the fee-rate INPUT
			// field; it must not re-estimate the fee on every press. The old
			// code called validateAndConstructTx() unconditionally, and each
			// reconstruct re-runs coin selection (a different input set →
			// different size → different fee), so repeated Reset presses
			// visibly jittered "Загальна сума" — the reported bug. Capture
			// whether a custom fee was actually in effect; only when we're
			// genuinely reverting custom→default do we reconstruct, and then
			// exactly once. With no override active, Reset just wipes the
			// (un-applied) editor text and leaves the total untouched.
			hadOverride := !pg.feeRateOverride.IsZero()

			dcrAsset.ClearFeeRateOverride()
			pg.feeRateOverride = cointype.Zero()
			pg.customFeeEditor.Editor.SetText("")
			// Force the editor back to its initial "placeholder only, no
			// floating label" state. cryptomaterial.Editor.layout swaps
			// TitleLabel ↔ Hint based on focus + content; if Apply was
			// clicked while the editor still held the user's number and
			// then Skinuti fires while the editor is unfocused, the state
			// machine doesn't run the normalising branch (it skips both
			// the "Editor.Len()>0 && Hint!=\"\"" path and the "Hint==\"\""
			// path) so TitleLabel AND Hint both keep the placeholder
			// string — both rendered, the user sees "Ставка комісії за КБ"
			// twice. Hard-reset here. The placeholder string is the
			// localized hint we passed at construction time.
			pg.customFeeEditor.TitleLabel.Text = ""
			pg.customFeeEditor.Hint = values.String(values.StrFeeRatePerKbHint)
			pg.customFeeStatus = values.String(values.StrCustomFeeCleared)
			pg.customFeeStatusIsErr = false
			if hadOverride {
				// Revert the total to the default-fee estimate a single
				// time; subsequent Reset presses are no-ops (no override to
				// clear), so the fee no longer changes on every click.
				pg.validateAndConstructTx()
			}
		}
	}

	pg.walletDropdown.Handle(gtx)
	pg.accountDropdown.Handle(gtx)
	if pg.coinTypeDropdown != nil {
		pg.coinTypeDropdown.Handle(gtx)
	}
	if pg.feeRateSelector.SaveRate.Clicked(gtx) {
		pg.feeRateSelector.OnEditRateClicked(pg.selectedWallet)
	}

	pg.nextButton.SetEnabled(pg.allRecipientsIsValid())

	if pg.infoButton.Button.Clicked(gtx) {
		textWithUnit := values.String(values.StrSend) + " " + pg.activeAssetSymbol()
		info := modal.NewCustomModal(pg.Load).
			Title(textWithUnit).
			Body(values.String(values.StrSendInfo)).
			SetPositiveButtonText(values.String(values.StrGotIt))
		pg.ParentWindow().ShowModal(info)
	}

	//TODO not included in design
	// if pg.retryExchange.Clicked(gtx) {
	// 	go pg.fetchExchangeRate()
	// }

	if pg.toCoinSelection.Clicked(gtx) {
		if (len(pg.getDestinationAddresses()) == len(pg.recipients)) || !pg.recipients[0].isSendToAddress() {
			pg.ParentNavigator().Display(NewManualCoinSelectionPage(pg.Load, pg))
		}
	}

	if pg.nextButton.Clicked(gtx) {
		if pg.selectedWallet.IsUnsignedTxExist() {
			pg.confirmTxModal = newSendConfirmModal(pg.Load, pg.authoredTxData, pg.selectedWallet, func(txHash string) {
				if pg.modalLayout == nil {
					transaction, err := pg.selectedWallet.GetTransactionRaw(txHash)
					if err != nil {
						log.Error("get transaction error: ", err)
					}
					pg.ParentNavigator().Display(txpage.NewTransactionDetailsPage(pg.Load, pg.selectedWallet, transaction))
				}
			})
			// No USD for SKA (no v1 pairing; .ToCoin() is VAR-only) — gating the
			// modal on this keeps the confirm header/fee/total-cost rows from
			// showing a bogus VAR-rate dollar value for a SKA send.
			isSKASend := pg.coinTypeDropdown != nil && pg.coinTypeDropdown.Selected().IsSKA()
			pg.confirmTxModal.exchangeRateSet = pg.exchangeRate != -1 && pg.usdExchangeSet && !isSKASend
			// TODO handle if there are many description texts
			// this workaround shows the description text when there is only one recipient and does not show when have more than one recipient
			descriptionText := ""
			if len(pg.recipients) == 1 {
				descriptionText = pg.recipients[0].descriptionText()
			}
			pg.confirmTxModal.txLabel = descriptionText
			// Fires from the broadcast goroutine the moment Broadcast()
			// succeeds, BEFORE the green-check success modal is shown.
			// We only flip an atomic flag here — the actual editor /
			// estimate / coin-type-dropdown mutations must run on the
			// UI thread (Gio editor buffers are not safe to write from
			// background goroutines while Layout reads them). The
			// Invalidate forces a frame so HandleUserInteractions
			// drains the flag without waiting for user input.
			pg.confirmTxModal.txBroadcastSuccess = func() {
				pg.pendingBroadcastReset.Store(true)
				if pw := pg.ParentWindow(); pw != nil {
					pw.Reload()
				}
			}
			pg.confirmTxModal.txSent = func() {
				if pg.modalLayout != nil {
					pg.modalLayout.Dismiss()
				}
			}

			pg.ParentWindow().ShowModal(pg.confirmTxModal)
		}
	}

	if pg.navigateToSyncBtn.Button.Clicked(gtx) {
		pg.ToggleSync(pg.selectedWallet, func(b bool) {
			pg.selectedWallet.SaveUserConfigValue(sharedW.AutoSyncConfigKey, b)
		})
	}

	if pg.addRecipientBtn.Clicked(gtx) {
		pg.addRecipient()
	}

	// handle recipient user interactions
	for _, re := range pg.recipients {
		re.HandleUserInteractions(gtx)
	}
}

// Handle is like HandleUserInteractions but Handle is called if this page is
// displayed as a modal while HandleUserInteractions is called if this page
// is displayed as a full page. Either Handle or HandleUserInteractions will
// be called just before Layout() is called to determine if any user interaction
// recently occurred on the modal or page and may be used to update any affected
// UI components shortly before they are displayed by the Layout() method.
func (pg *Page) Handle(gtx C) {
	if pg.modalLayout.BackdropClicked(gtx, true) || pg.closeButton.Clicked(gtx) {
		pg.modalLayout.Dismiss()
	} else {
		pg.HandleUserInteractions(gtx)
	}
}

// OnResume is called to initialize data and get UI elements ready to be
// displayed. This is called just before Handle() and Layout() are called (in
// that order).

// OnResume is like OnNavigatedTo but OnResume is called if this page is
// displayed as a modal while OnNavigatedTo is called if this page is displayed
// as a full page. Either OnResume or OnNavigatedTo is called to initialize
// data and get UI elements ready to be displayed. This is called just before
// Handle() and Layout() are called (in that order).
func (pg *Page) OnResume() {
	pg.OnNavigatedTo()
}

// OnDismiss is like OnNavigatedFrom but OnDismiss is called if this page is
// displayed as a modal while OnNavigatedFrom is called if this page is
// displayed as a full page. Either OnDismiss or OnNavigatedFrom is called
// after the modal is dismissed.
// NOTE: The modal may be re-displayed on the app's window, in which case
// OnResume() will be called again. This method should not destroy UI
// components unless they'll be recreated in the OnResume() method.
func (pg *Page) OnDismiss() {
	pg.OnNavigatedFrom()
}

// KeysToHandle returns a Filter's slice that describes a set of key combinations
// that this page wishes to capture. The HandleKeyPress() method will only be
// called when any of these key combinations is pressed.
// Satisfies the load.KeyEventHandler interface for receiving key events.
func (pg *Page) KeysToHandle() []event.Filter {
	return []event.Filter{key.FocusFilter{Target: pg}, key.Filter{Focus: pg, Name: key.NameTab, Optional: key.ModShift}}
}

// HandleKeyPress is called when one or more keys are pressed on the current
// window that match any of the key combinations returned by KeysToHandle().
// Satisfies the load.KeyEventHandler interface for receiving key events.
func (pg *Page) HandleKeyPress(_ *key.Event) {}

// OnNavigatedFrom is called when the page is about to be removed from
// the displayed window. This method should ideally be used to disable
// features that are irrelevant when the page is NOT displayed.
// NOTE: The page may be re-displayed on the app's window, in which case
// OnNavigatedTo() will be called again. This method should not destroy UI
// components unless they'll be recreated in the OnNavigatedTo() method.
// Part of the load.Page interface.
func (pg *Page) OnNavigatedFrom() {
	pg.walletDropdown.StopTxNtfnListener()
}

func (pg *Page) isFeerateAPIApproved() bool {
	return pg.AssetsManager.IsHTTPAPIPrivacyModeOff(libUtil.FeeRateHTTPAPI)
}
