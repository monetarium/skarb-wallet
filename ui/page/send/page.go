package send

import (
	"fmt"
	"math/big"
	"strings"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/widget"

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
	"github.com/monetarium/monetarium-node/cointype"
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
	pg.validateAndConstructTx()
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
	pg.selectedUTXOs = selectedUTXOsInfo{
		selectedUTXOs: utxos,
		sourceAccount: pg.accountDropdown.SelectedAccount(),
	}
	if len(utxos) > 0 {
		for _, elem := range utxos {
			pg.selectedUTXOs.totalUTXOsAmount += elem.Amount.ToInt()
		}
	}
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

	pg.exchangeRate = rate.LastTradePrice
	pg.updateRecipientExchangeRate()
	pg.validateAndConstructTx() // convert estimates to usd

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

func (pg *Page) constructTx() {
	if pg.accountDropdown == nil {
		return
	}
	sourceAccount := pg.accountDropdown.SelectedAccount()
	if sourceAccount == nil {
		return
	}
	selectedUTXOs := make([]*sharedW.UnspentOutput, 0)
	if sourceAccount == pg.selectedUTXOs.sourceAccount {
		selectedUTXOs = pg.selectedUTXOs.selectedUTXOs
	}

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
	pg.txFee = dcr.FormatTxAmount(feeAtom, displayCoinType)

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

	if pg.exchangeRate != -1 && pg.usdExchangeSet {
		pg.feeRateSelector.USDExchangeSet = true
		pg.txFeeUSD = fmt.Sprintf("$%.4f", utils.CryptoToUSD(pg.exchangeRate, feeAndSize.Fee.CoinValue))
		pg.feeRateSelector.TxFeeUSD = pg.txFeeUSD
		pg.totalCostUSD = utils.FormatAsUSDString(pg.Printer, utils.CryptoToUSD(pg.exchangeRate, totalCost.ToCoin()))
		pg.balanceAfterSendUSD = utils.FormatAsUSDString(pg.Printer, utils.CryptoToUSD(pg.exchangeRate, balanceAfterSend.ToCoin()))

		usdAmount := utils.CryptoToUSD(pg.exchangeRate, wal.ToAmount(totalAmount).ToCoin())
		pg.sendAmountUSD = utils.FormatAsUSDString(pg.Printer, usdAmount)
	}

	pg.checkAssetCoverage(sourceAccount, totalAmount, feeAtom, pg.totalSendAmountBig)
}

// checkAssetCoverage validates that the selected CoinType has enough balance to
// cover (amount + fee) on the source account. Monetarium pays the fee in the
// SAME asset as the transfer, so a SKA-1 transfer with no SKA-1 in the wallet
// fails even when the user has plenty of VAR.
func (pg *Page) checkAssetCoverage(sourceAccount *sharedW.Account, totalAmount, feeAtom int64, totalAmountBigStr string) {
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
	// Build the required-atom big.Int from the lossless big-string when the
	// SKA amount overflowed int64 (constructTx populated totalAmountBigStr
	// in that case). Otherwise lift the int64 totalAmount + feeAtom.
	var required *big.Int
	if totalAmountBigStr != "" {
		if parsed, ok := new(big.Int).SetString(totalAmountBigStr, 10); ok {
			required = new(big.Int).Add(parsed, big.NewInt(feeAtom))
		}
	}
	if required == nil {
		required = big.NewInt(totalAmount + feeAtom)
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
	selectedUTXOs := make([]*sharedW.UnspentOutput, 0)
	if sourceAccount == pg.selectedUTXOs.sourceAccount {
		selectedUTXOs = pg.selectedUTXOs.selectedUTXOs
	}

	feeAndSize, err := pg.selectedWallet.EstimateFeeAndSize()
	if err != nil {
		pg.setRecipientsAmountErr(err)
		return nil, nil, 0, err
	}
	feeAtom := feeAndSize.Fee.UnitValue
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
		spendableAmount = pg.selectedUTXOs.totalUTXOsAmount
		spendableBig = big.NewInt(spendableAmount)
	}

	wal := pg.selectedWallet
	var totalSendAmount int64
	// Parallel big.Int accumulators for SKA flows where the per-output
	// or cumulative amount exceeds int64. We always populate them
	// (initialised to zero); the int64 channel is the source of truth
	// when totalCostBig stays in-range and the big.Int channel takes
	// over once it overflows. Keeping both populated is cheaper than
	// branching every iteration and avoids forking the loop body.
	totalCostBig := new(big.Int)
	totalSendAmountBig := new(big.Int)
	feeBig := big.NewInt(feeAtom)
	for _, recipient := range pg.recipients {
		destinationAddress := recipient.destinationAddress()
		amountAtom, amountAtomBig, SendMax := recipient.validAmountBig()
		// AddSendDestinationBig uses the big.Int string when set,
		// falls back to amountAtom otherwise. VAR sends and small-SKA
		// sends naturally pass "" and stay on the legacy fast path.
		var err error
		if dcrAsset, ok := pg.selectedWallet.(*dcr.Asset); ok {
			err = dcrAsset.AddSendDestinationBig(recipient.id, destinationAddress, amountAtom, amountAtomBig, SendMax)
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
		}

		if SendMax {
			// SendMax with an SKA balance whose true atom count exceeds
			// int64 would silently send only the clamped 9.22 SKA and
			// strand the rest. Refuse — phase-1 SendMax still goes
			// through the int64 channel (no sweep API plumbed yet), so
			// we can't honour SendMax losslessly here. Surface a clear
			// error instead of burning the user's funds.
			if spendableBig != nil && !spendableBig.IsInt64() {
				recipient.amountValidationError(values.String(values.StrInvalidAmount))
				log.Warnf("SendMax refused: %s balance %s atoms exceeds int64; phase-1 author can only emit up to ~9.22 SKA in one tx",
					pg.coinTypeDropdown.Selected(), spendableBig.String())
				pg.clearEstimates()
				continue
			}
			amountAtom = spendableAmount - feeAtom
			recipient.setAmount(amountAtom)
		}
		// Build the recipient's atom contribution as big.Int so multi-SKA
		// totals don't overflow int64. amountAtomBig (when set) wins;
		// otherwise we lift amountAtom into big.Int.
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
		totalCostBig.Add(totalCostBig, feeBig)

		totalSendAmount += amountAtom
		cost := amountAtom + feeAtom
		totalCost += cost
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
	for i := range pg.recipients {
		recipient := pg.recipients[i]
		recipient.amountValidationError(err.Error())
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
			pg.confirmTxModal.exchangeRateSet = pg.exchangeRate != -1 && pg.usdExchangeSet
			// TODO handle if there are many description texts
			// this workaround shows the description text when there is only one recipient and does not show when have more than one recipient
			descriptionText := ""
			if len(pg.recipients) == 1 {
				descriptionText = pg.recipients[0].descriptionText()
			}
			pg.confirmTxModal.txLabel = descriptionText
			pg.confirmTxModal.txSent = func() {
				pg.resetRecipientsFields()
				pg.clearEstimates()
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
