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
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/values"
)

type AccountDropdown struct {
	*load.Load
	selectedAccount        *sharedW.Account
	selectedWallet         sharedW.Asset
	dropdown               *cryptomaterial.DropDown
	allAccounts            []*sharedW.Account
	accountChangedCallback func(*sharedW.Account)
	accountIsValid         func(*sharedW.Account) bool

	// coinType controls which coin's balance the dropdown displays per
	// account. Defaults to VAR (CoinTypeVAR). When set to a SKAn type via
	// SetCoinType, getAccountItemLayout fetches the per-account SKA
	// balance and formats it under that coin's symbol instead of
	// silently falling through to the dcrutil.Amount VAR formatter.
	//
	// The legacy code path showed "0 VAR" on the send page even when the
	// user had selected SKA1 in the asset picker, which made the wallet
	// look like it had no funds available to send — a classic source of
	// "the wallet is broken" support tickets.
	coinType cointype.CoinType

	// pendingRefresh is set from the tx/block notification goroutine and
	// drained in Handle (UI thread). accountChangedCallback rebuilds dropdown
	// items and (on the send page) re-validates recipients / touches editors —
	// not safe to run off the UI thread.
	pendingRefresh atomic.Bool
}

func NewAccountDropdown(l *load.Load) *AccountDropdown {
	d := &AccountDropdown{
		Load:            l,
		dropdown:        l.Theme.NewCommonDropDown([]cryptomaterial.DropDownItem{}, nil, cryptomaterial.MatchParent, values.AccountsDropdownGroup, false),
		allAccounts:     make([]*sharedW.Account, 0),
		selectedAccount: nil,
	}
	d.dropdown.BorderColor = &l.Theme.Color.Gray2
	return d
}

func (d *AccountDropdown) Setup(w sharedW.Asset, args ...*sharedW.Account) *AccountDropdown {
	if w == nil {
		return d
	}
	if len(args) > 0 {
		d.selectedAccount = args[0]
		if d.selectedAccount == nil {
			args = []*sharedW.Account{}
		}
	}

	d.selectedWallet = w
	items := []cryptomaterial.DropDownItem{}
	d.allAccounts = make([]*sharedW.Account, 0)
	accounts, err := d.selectedWallet.GetAccountsRaw()
	if err != nil {
		d.selectedAccount = nil
		d.dropdown.SetItems(items)
		return d
	}
	isFirst := true
	for _, account := range accounts.Accounts {
		if d.accountIsValid == nil || d.accountIsValid(account) {
			item := cryptomaterial.DropDownItem{
				Text:      fmt.Sprint(account.Number),
				Icon:      d.Theme.Icons.AccountIcon,
				DisplayFn: d.getAccountItemLayout(account),
			}
			items = append(items, item)
			d.allAccounts = append(d.allAccounts, account)
			if len(args) < 1 { // select the first valid wallet
				if isFirst {
					isFirst = false
					d.selectedAccount = account
					if d.accountChangedCallback != nil {
						d.accountChangedCallback(d.selectedAccount)
					}
				}
			} else {
				acc := args[0]
				if account != nil && account.AccountNumber == acc.AccountNumber {
					d.selectedAccount = account
					if d.accountChangedCallback != nil {
						d.accountChangedCallback(d.selectedAccount)
					}
				}
			}
		}
	}
	d.dropdown.SetItems(items)
	// Same desync as in SetCoinType: SetItems resets the visible selection to
	// index 0 even when a previously selected account was re-applied above.
	if d.selectedAccount != nil {
		d.dropdown.SetSelectedValue(fmt.Sprint(d.selectedAccount.Number))
	}
	return d
}

func (d *AccountDropdown) ResetAccount() {
	d.selectedAccount = nil
}

func (d *AccountDropdown) AccountValidator(accountIsValid func(*sharedW.Account) bool) *AccountDropdown {
	d.accountIsValid = accountIsValid
	return d
}

// SetCoinType switches the dropdown's per-account balance display to the
// given coin type. Pass cointype.CoinTypeVAR to restore default behaviour.
// Callers should invoke this *after* Setup so the dropdown items are
// already populated; the next layout pass will pick up the new coin.
func (d *AccountDropdown) SetCoinType(ct cointype.CoinType) {
	if d.coinType == ct {
		return
	}
	d.coinType = ct
	// Rebuild the DisplayFn for each existing item so the inline
	// SKA-balance lookup is bound against the new coin type immediately.
	// Without this rebuild the dropdown keeps the previous closure and
	// would only update after a fresh Setup() call.
	if d.selectedWallet == nil {
		return
	}
	items := make([]cryptomaterial.DropDownItem, 0, len(d.allAccounts))
	for _, account := range d.allAccounts {
		items = append(items, cryptomaterial.DropDownItem{
			Text:      fmt.Sprint(account.Number),
			Icon:      d.Theme.Icons.AccountIcon,
			DisplayFn: d.getAccountItemLayout(account),
		})
	}
	d.dropdown.SetItems(items)
	// SetItems hard-resets the dropdown's selectedIndex to 0, which visually
	// snaps the collapsed control to the first ("default") account while
	// d.selectedAccount silently keeps the user's choice — the authored tx
	// then spends from an account the UI no longer displays. Restore the
	// visible selection (no-op if the account vanished), mirroring
	// CoinTypeDropdown.Setup.
	if d.selectedAccount != nil {
		d.dropdown.SetSelectedValue(fmt.Sprint(d.selectedAccount.Number))
	}
}

func (d *AccountDropdown) getAccountItemLayout(account *sharedW.Account) layout.Widget {
	// Compute the balance labels ONCE here — getAccountItemLayout is called at
	// build time (Setup / SetCoinType, on the UI thread), not per frame. The
	// SKA branch issues a walletdb query (GetCoinBalance); doing it inside the
	// returned closure ran one DB transaction per dropdown item EVERY frame,
	// causing scroll/redraw jank. The dropdown is rebuilt (via Setup) on
	// tx/block notifications, so these cached labels stay fresh.
	//
	// totalLabel / spendableLabel are the two strings rendered on the right
	// side of the dropdown row. By default we use the account's own VAR-shaped
	// Balance fields. When SetCoinType was called with a SKAn type, we look up
	// the per-account SKA balance and format it under FormatCoinAmount, which
	// puts the right unit ("SKA1") + decimals (1e18 atoms/coin).
	totalLabel := account.Balance.Total.String()
	spendableLabel := account.Balance.Spendable.String()
	if d.coinType.IsSKA() {
		if dcrAsset, ok := d.selectedWallet.(*dcr.Asset); ok {
			if bal, err := dcrAsset.GetCoinBalance(account.Number, d.coinType); err == nil {
				totalLabel = dcr.FormatCoinAmount(bal)
				// CoinBalance carries Spendable as a sub-field; map
				// the same shape so SKA users see "X SKA1" instead of
				// the always-zero VAR Balance.Spendable.
				spendBal := bal
				spendBal.Total = bal.Spendable
				spendBal.SKATotal = bal.SKASpendable
				spendableLabel = dcr.FormatCoinAmount(spendBal)
			}
		}
	}
	if d.selectedWallet != nil && d.selectedWallet.IsWatchingOnlyWallet() {
		// Show zero spendable for watching-only wallets WITHOUT
		// mutating the shared account.Balance — that pointer is
		// reused by other dropdowns and by the send page's
		// selectedAccount, so mutating it would poison every other
		// caller's view of the balance.
		//
		// Render the zero in the *selected coin's* unit, not always
		// VAR. wal.ToAmount() is the legacy VAR-only formatter; for
		// a watch-only SKA1 dropdown it would lie "0 VAR" while the
		// rest of the row is talking about SKA1. dcr.FormatTxAmount
		// dispatches on coin type and emits the right suffix.
		spendableLabel = dcr.FormatTxAmount(0, uint8(d.coinType))
	}

	return func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Axis: layout.Horizontal, Spacing: layout.SpaceBetween}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						lbl := d.Theme.SemiBoldLabel(account.AccountName)
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

func (d *AccountDropdown) getAccountByNumber(accountNumber int32) *sharedW.Account {
	for _, account := range d.allAccounts {
		if account.Number == accountNumber {
			return account
		}
	}
	return nil
}

func (d *AccountDropdown) SelectedAccount() *sharedW.Account {
	if d == nil {
		return nil
	}
	return d.selectedAccount
}

func (d *AccountDropdown) SetSelectedAccount(account *sharedW.Account) {
	d.selectedAccount = account
	d.dropdown.SetSelectedValue(fmt.Sprint(account.Number))
}

func (d *AccountDropdown) onChanged() {
	accountNumber, err := strconv.Atoi(d.dropdown.Selected())
	if err == nil {
		account := d.getAccountByNumber(int32(accountNumber))
		if account != nil {
			d.selectedAccount = account
			if d.accountChangedCallback != nil {
				d.accountChangedCallback(account)
			}
		}
	}
}

func (d *AccountDropdown) SetChangedCallback(callback func(*sharedW.Account)) *AccountDropdown {
	d.accountChangedCallback = callback
	return d
}

func (d *AccountDropdown) Handle(gtx C) {
	// Drain a deferred tx/block-notification refresh on the UI thread.
	if d.pendingRefresh.CompareAndSwap(true, false) {
		if d.accountChangedCallback != nil && d.selectedAccount != nil {
			d.accountChangedCallback(d.selectedAccount)
		}
	}
	if d.dropdown.Changed(gtx) {
		d.onChanged()
	}
}

func (d *AccountDropdown) Layout(gtx C, title string) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			if title == "" {
				return D{}
			}

			lbl := d.Theme.H6(title)
			lbl.TextSize = values.TextSizeTransform(d.IsMobileView(), values.TextSize16)
			lbl.Font.Weight = font.SemiBold
			return layout.Inset{Bottom: values.MarginPadding4}.Layout(gtx, lbl.Layout)
		}),
		layout.Rigid(func(gtx C) D {
			return d.dropdown.Layout(gtx)
		}),
	)
}

// ListenForTxNotifications listens for transaction and block updates and
// updates the selector modal, if the modal is open at the time of the update.
// The tx update listener MUST be unregistered using ws.StopTxNtfnListener()
// when the page using this WalletAndAccountSelector widget is exited.
func (d *AccountDropdown) ListenForTxNotifications(window app.WindowNavigator) {
	txAndBlockNotificationListener := &sharedW.TxAndBlockNotificationListener{
		OnTransaction: func(_ int, _ *sharedW.Transaction) {
			// Defer to the UI thread (see Handle): the callback rebuilds
			// dropdown items and re-validates recipients, racing with Layout.
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
	// AccountSelectorListenerID, NOT the WalletDropdown's shared
	// WalletAndAccountSelectorID: a page that registers both dropdowns on the
	// same wallet would otherwise collide — the second Add fails with
	// ErrListenerAlreadyExist (silently, just a log line) and that dropdown
	// never receives refreshes; worse, whichever Stop runs first would tear
	// down the other's listener.
	err := d.selectedWallet.AddTxAndBlockNotificationListener(txAndBlockNotificationListener, AccountSelectorListenerID)
	if err != nil {
		log.Errorf("AccountDropdown.ListenForTxNotifications error: %v", err)
	}
}

// AccountSelectorListenerID uniquely identifies the AccountDropdown's
// tx/block notification listener registration.
const AccountSelectorListenerID = "AccountSelectorListener"

func (d *AccountDropdown) StopTxNtfnListener() {
	if d.selectedWallet != nil {
		d.selectedWallet.RemoveTxAndBlockNotificationListener(AccountSelectorListenerID)
	}
}
