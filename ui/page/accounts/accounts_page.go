package accounts

import (
	"fmt"
	"math/big"
	"sync/atomic"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/widget"
	"github.com/monetarium/monetarium-node/cointype"
	"github.com/monetarium/skarb-wallet/app"
	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	libutils "github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/modal"
	"github.com/monetarium/skarb-wallet/ui/page/components"
	"github.com/monetarium/skarb-wallet/ui/values"
)

const AccountsPageID = "Accounts"

type (
	C = layout.Context
	D = layout.Dimensions
)

type Page struct {
	*load.Load
	// GenericPageModal defines methods such as ID() and OnAttachedToNavigator()
	// that helps this Page satisfy the app.Page interface. It also defines
	// helper methods for accessing the PageNavigator that displayed this page
	// and the root WindowNavigator.
	*app.GenericPageModal

	wallet sharedW.Asset

	container     *widget.List
	addAccountBtn *cryptomaterial.Clickable
	accounts      []*sharedW.Account
	// accountNameClicks: clicking an account's NAME row opens its detail
	// page. The card itself is no longer one big clickable (that conflicted
	// with the per-coin expand clickables); only the name row navigates.
	accountNameClicks map[int32]*cryptomaterial.Clickable
	// coinBreakdowns caches, per account, one pre-formatted row per VISIBLE
	// coin (VAR + each shown SKA). Computed once in loadWalletAccount — the
	// Layout closure must not query balances per frame. A hidden coin (the
	// settings filter) is simply absent here, so its data appears nowhere on
	// this screen, including VAR.
	coinBreakdowns map[int32][]*coinBreakdown

	// expandedCoins remembers which coin rows are expanded (showing
	// locked/immature). Keyed "<accountNumber>:<coinType>" so the state
	// survives the cache rebuilds in loadWalletAccount.
	expandedCoins map[string]bool

	// pendingBalanceRefresh is set by the tx/block notification listener
	// (a wallet goroutine) and drained on the UI thread in
	// HandleUserInteractions to re-run loadWalletAccount — so the displayed
	// balances update live on new/confirmed txs without leaving the page
	// (CLAUDE.md §3: never rebuild Layout-read caches from a goroutine).
	pendingBalanceRefresh atomic.Bool
}

// coinBreakdown is a pre-formatted per-coin balance line for one account:
// the coin's total + available (always shown) and locked + immature (shown
// when the row is expanded). VAR and SKA coins both carry the full split.
type coinBreakdown struct {
	coinType  cointype.CoinType
	symbol    string
	total     string
	spendable string
	locked    string
	immature  string
	click     *cryptomaterial.Clickable
}

func NewAccountPage(l *load.Load, wallet sharedW.Asset) *Page {
	pg := &Page{
		Load:             l,
		GenericPageModal: app.NewGenericPageModal(AccountsPageID),
		container: &widget.List{
			List: layout.List{Axis: layout.Vertical},
		},
		addAccountBtn:     l.Theme.NewClickable(false),
		wallet:            wallet,
		expandedCoins:     make(map[string]bool),
		accountNameClicks: make(map[int32]*cryptomaterial.Clickable),
	}

	return pg
}

func (pg *Page) loadWalletAccount() {
	walletAccounts := make([]*sharedW.Account, 0)
	accounts, err := pg.wallet.GetAccountsRaw()
	if err != nil {
		log.Errorf("error retrieving wallet accounts: %v", err)
		return
	}

	for _, acct := range accounts.Accounts {
		if acct.Number == dcr.ImportedAccountNumber {
			continue
		}
		walletAccounts = append(walletAccounts, acct)
	}

	pg.accounts = walletAccounts

	// Pre-compute the per-coin breakdown rows once per load (here, not per
	// frame). This is the only place the per-account coin balances are
	// queried. One row per VISIBLE coin (VAR + each shown SKA); a coin hidden
	// via the settings filter is absent, so nothing about it renders here.
	cache := make(map[int32][]*coinBreakdown)
	if dcrAsset, ok := pg.wallet.(*dcr.Asset); ok {
		for _, acct := range walletAccounts {
			if pg.accountNameClicks[acct.Number] == nil {
				pg.accountNameClicks[acct.Number] = pg.Theme.NewClickable(true)
			}
			balances, err := dcrAsset.GetAccountCoinBalances(acct.Number)
			if err != nil {
				log.Errorf("accounts: GetAccountCoinBalances(%d): %v", acct.Number, err)
				continue
			}
			var rows []*coinBreakdown
			for _, ct := range dcrAsset.VisibleCoinTypes() {
				var cb *coinBreakdown
				if ct.IsVAR() {
					// VAR uses the account's int64 Balance (the proven path);
					// the dcrW CoinBalance VAR fields are equivalent but this
					// keeps parity with the legacy display.
					b := acct.Balance
					var total, spendable, locked, immature int64
					if b != nil {
						total = b.Total.ToInt()
						spendable = b.Spendable.ToInt()
						if b.Locked != nil {
							locked += b.Locked.ToInt()
						}
						if b.LockedByTickets != nil {
							locked += b.LockedByTickets.ToInt()
						}
						if b.ImmatureReward != nil {
							immature += b.ImmatureReward.ToInt()
						}
						if b.ImmatureStakeGeneration != nil {
							immature += b.ImmatureStakeGeneration.ToInt()
						}
					}
					cb = &coinBreakdown{
						coinType:  ct,
						symbol:    dcr.CoinSymbol(ct),
						total:     dcr.FormatTxAmount(total, uint8(ct)),
						spendable: dcr.FormatTxAmount(spendable, uint8(ct)),
						locked:    dcr.FormatTxAmount(locked, uint8(ct)),
						immature:  dcr.FormatTxAmount(immature, uint8(ct)),
					}
				} else {
					bal := balances[ct] // zero-value CoinBalance when absent
					atoms := func(a cointype.SKAAmount) *big.Int {
						if v := a.BigInt(); v != nil {
							return v
						}
						return new(big.Int)
					}
					fmtAtoms := func(b *big.Int) string {
						return dcr.FormatTxAmountBig(b.String(), 0, uint8(ct))
					}
					total := atoms(bal.SKATotal)
					spendable := atoms(bal.SKASpendable)
					immature := new(big.Int).Add(atoms(bal.SKAImmatureCoinbaseRewards), atoms(bal.SKAImmatureStakeGeneration))
					// Locked = total minus spendable, immature and still-
					// unconfirmed (clamped at zero).
					locked := new(big.Int).Sub(total, spendable)
					locked.Sub(locked, immature)
					locked.Sub(locked, atoms(bal.SKAUnconfirmed))
					if locked.Sign() < 0 {
						locked.SetInt64(0)
					}
					cb = &coinBreakdown{
						coinType:  ct,
						symbol:    dcr.CoinSymbol(ct),
						total:     fmtAtoms(total),
						spendable: fmtAtoms(spendable),
						locked:    fmtAtoms(locked),
						immature:  fmtAtoms(immature),
					}
				}
				cb.click = pg.Theme.NewClickable(true)
				rows = append(rows, cb)
			}
			cache[acct.Number] = rows
		}
	}
	pg.coinBreakdowns = cache
}

// coinRowKey is the expandedCoins map key for one (account, coin) pair.
func coinRowKey(accountNumber int32, ct cointype.CoinType) string {
	return fmt.Sprintf("%d:%d", accountNumber, uint8(ct))
}

// OnNavigatedTo is called when the page is about to be displayed and
// may be used to initialize page features that are only relevant when
// the page is displayed.
// Part of the load.Page interface.
func (pg *Page) OnNavigatedTo() {
	pg.loadWalletAccount()
	pg.listenForTxNotifications()
}

// listenForTxNotifications refreshes the displayed balances when a tx arrives
// or a block confirms one. The callbacks run on a wallet goroutine, so they
// only STAGE a refresh (atomic flag + Reload); the actual loadWalletAccount
// rebuild happens on the UI thread in HandleUserInteractions (CLAUDE.md §3).
func (pg *Page) listenForTxNotifications() {
	listener := &sharedW.TxAndBlockNotificationListener{
		OnTransaction: func(_ int, _ *sharedW.Transaction) {
			pg.pendingBalanceRefresh.Store(true)
			pg.ParentWindow().Reload()
		},
		OnBlockAttached: func(_ int, _ int32) {
			pg.pendingBalanceRefresh.Store(true)
			pg.ParentWindow().Reload()
		},
	}
	if err := pg.wallet.AddTxAndBlockNotificationListener(listener, AccountsPageID); err != nil {
		log.Errorf("accounts: add tx/block notification listener: %v", err)
	}
}

// OnNavigatedFrom is called when the page is about to be removed from
// the displayed window. This method should ideally be used to disable
// features that are irrelevant when the page is NOT displayed.
// NOTE: The page may be re-displayed on the app's window, in which case
// OnNavigatedTo() will be called again. This method should not destroy UI
// components unless they'll be recreated in the OnNavigatedTo() method.
// Part of the load.Page interface.
func (pg *Page) OnNavigatedFrom() {
	pg.wallet.RemoveTxAndBlockNotificationListener(AccountsPageID)
}

// Layout draws the page UI components into the provided layout context
// to be eventually drawn on screen.
// Part of the load.Page interface.
func (pg *Page) Layout(gtx C) D {
	return pg.Theme.List(pg.container).Layout(gtx, 1, func(gtx C, _ int) D {
		return pg.Theme.Card().Layout(gtx, func(gtx C) D {
			return components.HorizontalInset(values.MarginPadding16).Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(pg.headerLayout),
					layout.Rigid(pg.bodyLayout),
				)
			})
		})
	})
}

func (pg *Page) bodyLayout(gtx C) D {
	dp24 := values.MarginPaddingTransform(pg.IsMobileView(), values.MarginPadding24)
	return layout.Inset{Top: dp24, Bottom: dp24}.Layout(gtx, func(gtx C) D {
		// Plain Flex (not a ClickableList): each account card owns its own
		// per-coin expand clickables, so the whole card must NOT be one big
		// clickable. The outer page list (Layout) supplies scrolling.
		children := make([]layout.FlexChild, 0, len(pg.accounts))
		for i := range pg.accounts {
			account := pg.accounts[i]
			children = append(children, layout.Rigid(func(gtx C) D {
				return layout.Inset{Bottom: values.MarginPadding20}.Layout(gtx, func(gtx C) D {
					return pg.accountItemLayout(gtx, account)
				})
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
}

func (pg *Page) headerLayout(gtx C) D {
	return layout.Inset{
		Top: values.MarginPadding24,
	}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				txt := pg.Theme.Label(values.TextSize20, values.String(values.StrAccounts))
				txt.Font.Weight = font.SemiBold
				return txt.Layout(gtx)
			}),
			layout.Flexed(1, func(gtx C) D {
				if pg.wallet.IsWatchingOnlyWallet() {
					return D{}
				}
				return layout.E.Layout(gtx, pg.addAccountBtnLayout)
			}),
		)
	})
}

func (pg *Page) addAccountBtnLayout(gtx C) D {
	return cryptomaterial.LinearLayout{
		Width:      cryptomaterial.WrapContent,
		Height:     cryptomaterial.WrapContent,
		Background: pg.Theme.Color.DefaultThemeColors().SurfaceHighlight,
		Clickable:  pg.addAccountBtn,
		Alignment:  layout.Middle,
	}.Layout(gtx,
		layout.Rigid(pg.Theme.AddIcon().Layout24dp),
		layout.Rigid(func(gtx C) D {
			txt := pg.Theme.Label(values.TextSize16, values.String(values.StrAddNewAccount))
			txt.Color = pg.Theme.Color.DefaultThemeColors().Primary
			txt.Font.Weight = font.SemiBold
			return layout.Inset{
				Left: values.MarginPadding8,
			}.Layout(gtx, txt.Layout)
		}),
	)
}

func (pg *Page) accountItemLayout(gtx C, account *sharedW.Account) D {
	dp10 := values.MarginPadding10
	children := []layout.FlexChild{
		// Header row: the account NAME only (nothing else). Per the redesign
		// the per-coin totals live in the rows below, not next to the name.
		// The name row is clickable — it opens the account detail page.
		layout.Rigid(func(gtx C) D {
			return cryptomaterial.LinearLayout{
				Width:     cryptomaterial.MatchParent,
				Height:    cryptomaterial.WrapContent,
				Clickable: pg.accountNameClicks[account.Number],
				Padding:   layout.Inset{Top: values.MarginPadding4, Bottom: values.MarginPadding4},
			}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					lbl := pg.Theme.Label(pg.ConvertTextSize(values.TextSize18), account.AccountName)
					lbl.Font.Weight = font.SemiBold
					return lbl.Layout(gtx)
				}),
			)
		}),
	}
	rows := pg.coinBreakdowns[account.Number]
	for i := range rows {
		cb := rows[i]
		expanded := pg.expandedCoins[coinRowKey(account.Number, cb.coinType)]
		children = append(children,
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Top: dp10, Bottom: values.MarginPadding6}.Layout(gtx, pg.Theme.Separator().Layout)
			}),
			layout.Rigid(func(gtx C) D { return pg.coinRowLayout(gtx, cb, expanded) }),
		)
		if expanded {
			children = append(children,
				layout.Rigid(layout.Spacer{Height: values.MarginPadding4}.Layout),
				layout.Rigid(pg.labelledAmountRow(values.String(values.StrLocked), cb.locked)),
				layout.Rigid(pg.labelledAmountRow(values.String(values.StrImmature), cb.immature)),
			)
		}
	}
	return cryptomaterial.LinearLayout{
		Width:       cryptomaterial.MatchParent,
		Height:      cryptomaterial.WrapContent,
		Padding:     layout.UniformInset(dp10),
		Orientation: layout.Vertical,
		Border: cryptomaterial.Border{
			Width:  values.MarginPadding1,
			Color:  pg.Theme.Color.Gray3,
			Radius: cryptomaterial.Radius(8),
		},
	}.Layout(gtx, children...)
}

// coinRowLayout renders one collapsible coin row: the symbol + expand
// triangle on the left, and the coin's total + available (right-aligned) on
// the right. The whole row is clickable to toggle the locked/immature detail.
func (pg *Page) coinRowLayout(gtx C, cb *coinBreakdown, expanded bool) D {
	icon := pg.Theme.Icons.ArrowDropDown
	if expanded {
		icon = pg.Theme.Icons.ArrowDropUp
	}
	return cryptomaterial.LinearLayout{
		Width:       cryptomaterial.MatchParent,
		Height:      cryptomaterial.WrapContent,
		Clickable:   cb.click,
		Padding:     layout.Inset{Top: values.MarginPadding4, Bottom: values.MarginPadding4},
		Alignment:   layout.Middle,
		Orientation: layout.Horizontal,
	}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			sym := pg.Theme.Label(pg.ConvertTextSize(values.TextSize16), cb.symbol)
			sym.Font.Weight = font.SemiBold
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(sym.Layout),
				layout.Rigid(func(gtx C) D {
					return pg.Theme.NewIcon(icon).Layout20dp(gtx)
				}),
			)
		}),
		layout.Flexed(1, func(gtx C) D {
			return layout.E.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical, Alignment: layout.End}.Layout(gtx,
					layout.Rigid(pg.labelledAmountRow(values.String(values.StrTotal), cb.total)),
					layout.Rigid(pg.labelledAmountRow(values.String(values.StrLabelSpendable), cb.spendable)),
				)
			})
		}),
	)
}

// labelledAmountRow renders a right-aligned "Title: amount" line (regular
// weight; per the design only the account-name header is bold).
func (pg *Page) labelledAmountRow(title, amount string) func(gtx C) D {
	return func(gtx C) D {
		return layout.E.Layout(gtx, func(gtx C) D {
			titleLbl := pg.Theme.Label(pg.ConvertTextSize(values.TextSize14), title)
			titleLbl.Color = pg.Theme.Color.GrayText2
			amtLbl := pg.Theme.Label(pg.ConvertTextSize(values.TextSize14), amount)
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(titleLbl.Layout),
				layout.Rigid(layout.Spacer{Width: values.MarginPadding6}.Layout),
				layout.Rigid(amtLbl.Layout),
			)
		})
	}
}

// HandleUserInteractions is called just before Layout() to determine
// if any user interaction recently occurred on the page and may be
// used to update the page's UI components shortly before they are
// displayed.
// Part of the load.Page interface.
func (pg *Page) HandleUserInteractions(gtx C) {
	// Apply a balance refresh staged by the tx/block notification listener
	// (CLAUDE.md §3 — the rebuild of pg.accounts/pg.coinBreakdowns, read by
	// Layout, must happen here on the UI thread, not in the goroutine).
	if pg.pendingBalanceRefresh.CompareAndSwap(true, false) {
		pg.loadWalletAccount()
	}

	if pg.addAccountBtn.Clicked(gtx) {
		createAccountModal := modal.NewCreatePasswordModal(pg.Load).
			Title(values.String(values.StrCreateNewAccount)).
			EnableName(true).
			NameHint(values.String(values.StrAcctName)).
			EnableConfirmPassword(false).
			PasswordHint(values.String(values.StrSpendingPassword)).
			SetPositiveButtonCallback(func(accountName, password string, m *modal.CreatePasswordModal) bool {
				_, err := pg.wallet.CreateNewAccount(accountName, password)
				if err != nil {
					m.SetError(err.Error())
					return false
				}
				pg.loadWalletAccount()
				m.Dismiss()

				info := modal.NewSuccessModal(pg.Load, values.StringF(values.StrAcctCreated),
					modal.DefaultClickFunc())
				pg.ParentWindow().ShowModal(info)
				return true
			})
		pg.ParentWindow().ShowModal(createAccountModal)
	}

	for i := range pg.accounts {
		account := pg.accounts[i]
		// Name-row click → account detail page.
		if click := pg.accountNameClicks[account.Number]; click != nil && click.Clicked(gtx) {
			if pg.wallet.GetAssetType() == libutils.DCRWalletAsset {
				pg.ParentNavigator().Display(NewDCRAcctDetailsPage(pg.Load, pg.wallet, account))
			}
		}
		// Coin-row clicks → toggle the locked/immature detail for that coin.
		for _, cb := range pg.coinBreakdowns[account.Number] {
			if cb.click != nil && cb.click.Clicked(gtx) {
				key := coinRowKey(account.Number, cb.coinType)
				pg.expandedCoins[key] = !pg.expandedCoins[key]
			}
		}
	}
}
