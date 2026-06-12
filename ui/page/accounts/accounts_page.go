package accounts

import (
	"fmt"
	"math/big"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"github.com/monetarium/skarb-wallet/app"
	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	libutils "github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/modal"
	"github.com/monetarium/skarb-wallet/ui/page/components"
	"github.com/monetarium/skarb-wallet/ui/utils"
	"github.com/monetarium/skarb-wallet/ui/values"
	"github.com/monetarium/monetarium-node/cointype"
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
	accountsList  *cryptomaterial.ClickableList
	accounts      []*sharedW.Account
	// skaBalances caches each account's pre-formatted SKA token rows, computed
	// once in loadWalletAccount. The Layout closure previously called
	// GetAccountCoinBalances (one walletdb.View per active coin type) on EVERY
	// frame for EVERY account row, causing scroll/redraw jank.
	skaBalances map[int32][]skaRow

	exchangeRate   float64
	usdExchangeSet bool
}

// skaRow is a pre-formatted per-coin balance breakdown cached per account:
// the coin's total plus the same available/locked/immature split the VAR
// section shows — SKA coins can be locked or immature too (emission
// maturity), not just VAR.
type skaRow struct {
	symbol    string
	total     string
	spendable string
	locked    string
	immature  string
}

func NewAccountPage(l *load.Load, wallet sharedW.Asset) *Page {
	pg := &Page{
		Load:             l,
		GenericPageModal: app.NewGenericPageModal(AccountsPageID),
		container: &widget.List{
			List: layout.List{Axis: layout.Vertical},
		},
		addAccountBtn: l.Theme.NewClickable(false),
		accountsList:  l.Theme.NewClickableList(layout.Vertical),
		wallet:        wallet,
	}
	pg.accountsList.Radius = cryptomaterial.Radius(8)
	pg.accountsList.CompleteRadius = true
	pg.accountsList.ClickableInset = cryptomaterial.ClickableInset{Bottom: values.MarginPadding20}

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

	// Pre-compute SKA balance rows once per load (here, not per frame). This
	// is the only place the per-account coin balances are queried now.
	cache := make(map[int32][]skaRow)
	if dcrAsset, ok := pg.wallet.(*dcr.Asset); ok {
		for _, acct := range walletAccounts {
			balances, err := dcrAsset.GetAccountCoinBalances(acct.Number)
			if err != nil {
				log.Errorf("accounts: GetAccountCoinBalances(%d): %v", acct.Number, err)
				continue
			}
			var rows []skaRow
			// Every user-visible coin gets a row — including zero balances,
			// so a freshly emitted coin (SKA2) is visible to the user; the
			// settings coin filter hides unwanted ones.
			for _, ct := range dcrAsset.VisibleCoinTypes() {
				if !ct.IsSKA() {
					continue
				}
				bal := balances[ct] // zero-value CoinBalance when absent
				fmtAtoms := func(b *big.Int) string {
					if b == nil {
						b = new(big.Int)
					}
					return dcr.FormatTxAmountBig(b.String(), 0, uint8(ct))
				}
				atoms := func(a cointype.SKAAmount) *big.Int {
					if b := a.BigInt(); b != nil {
						return b
					}
					return new(big.Int)
				}
				total := atoms(bal.SKATotal)
				spendable := atoms(bal.SKASpendable)
				immature := new(big.Int).Add(atoms(bal.SKAImmatureCoinbaseRewards), atoms(bal.SKAImmatureStakeGeneration))
				// Locked = whatever of the total is neither spendable nor
				// immature nor still unconfirmed (clamped at zero).
				locked := new(big.Int).Sub(total, spendable)
				locked.Sub(locked, immature)
				locked.Sub(locked, atoms(bal.SKAUnconfirmed))
				if locked.Sign() < 0 {
					locked.SetInt64(0)
				}
				rows = append(rows, skaRow{
					symbol:    dcr.CoinSymbol(ct),
					total:     fmtAtoms(total),
					spendable: fmtAtoms(spendable),
					locked:    fmtAtoms(locked),
					immature:  fmtAtoms(immature),
				})
			}
			cache[acct.Number] = rows
		}
	}
	pg.skaBalances = cache
}

// OnNavigatedTo is called when the page is about to be displayed and
// may be used to initialize page features that are only relevant when
// the page is displayed.
// Part of the load.Page interface.
func (pg *Page) OnNavigatedTo() {
	pg.loadWalletAccount()
	pg.usdExchangeSet = false
	if pg.AssetsManager.ExchangeRateFetchingEnabled() {
		pg.usdExchangeSet = pg.AssetsManager.RateSource.Ready()
		go pg.fetchExchangeRate()
	}
}

// OnNavigatedFrom is called when the page is about to be removed from
// the displayed window. This method should ideally be used to disable
// features that are irrelevant when the page is NOT displayed.
// NOTE: The page may be re-displayed on the app's window, in which case
// OnNavigatedTo() will be called again. This method should not destroy UI
// components unless they'll be recreated in the OnNavigatedTo() method.
// Part of the load.Page interface.
func (pg *Page) OnNavigatedFrom() {}

func (pg *Page) fetchExchangeRate() {
	market, err := utils.USDMarketFromAsset(pg.wallet.GetAssetType())
	if err != nil {
		log.Errorf("Unsupported asset type: %s", pg.wallet.GetAssetType())
		return
	}

	rate := pg.AssetsManager.RateSource.GetTicker(market, true) // okay to fetch latest rate, this is a goroutine
	if rate == nil || rate.LastTradePrice <= 0 {
		return
	}

	pg.exchangeRate = rate.LastTradePrice
	pg.ParentWindow().Reload()
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
		return pg.accountsList.Layout(gtx, len(pg.accounts), func(gtx C, i int) D {
			return layout.Inset{Bottom: values.MarginPadding20}.Layout(gtx, func(gtx C) D {
				return pg.accountItemLayout(gtx, pg.accounts[i])
			})
		})
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
	bal := account.Balance
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
	}.Layout(gtx,
		// Only the header row (account name + its total) is bold; the
		// breakdown rows below render in regular weight.
		layout.Rigid(pg.accountBalanceLayout(account.AccountName, account.Balance.Total, layout.Vertical, true)),
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Top: dp10, Bottom: dp10}.Layout(gtx, pg.Theme.Separator().Layout)
		}),
		layout.Rigid(func(gtx C) D {
			locked := bal.Locked
			if bal.LockedByTickets != nil {
				locked = pg.wallet.ToAmount(locked.ToInt() + bal.LockedByTickets.ToInt())
			}
			children := []layout.FlexChild{
				layout.Rigid(pg.accountBalanceLayout(values.String(values.StrLabelSpendable), bal.Spendable, layout.Horizontal, false)),
				layout.Rigid(pg.accountBalanceLayout(values.String(values.StrLocked), locked, layout.Horizontal, false)),
				layout.Rigid(func(gtx C) D {
					if pg.wallet.GetAssetType() != libutils.DCRWalletAsset {
						return D{}
					}

					// Display immature for only DCR.
					immature := pg.wallet.ToAmount(bal.ImmatureReward.ToInt() + bal.ImmatureStakeGeneration.ToInt())
					return pg.accountBalanceLayout(values.String(values.StrImmature), immature, layout.Horizontal, false)(gtx)
				}),
			}
			// Append a breakdown group per visible SKA coin. The VAR rows
			// above come from the legacy int64 Balance struct, which has no
			// SKA fields; SKA balances are read from the dcr asset's per-coin
			// big.Int channel — available/locked/immature apply to SKA too.
			children = append(children, pg.skaBalanceRows(account)...)
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
		}),
	)
}

// skaBalanceRows returns one balance row per SKA coin type the account
// holds. VAR is rendered through accountBalanceLayout above; this surfaces
// the SKA token balances the VAR-only Balance struct can't express. Only
// coins with a non-zero balance in *this* account are listed (mirrors the
// wallet-wide DisplayableCoinTypes filter so users don't see SKA-n entries
// they have never received).
func (pg *Page) skaBalanceRows(account *sharedW.Account) []layout.FlexChild {
	// Read from the per-load cache (populated in loadWalletAccount) — no
	// walletdb queries from inside this per-frame Layout closure.
	var rows []layout.FlexChild
	for _, r := range pg.skaBalances[account.Number] {
		r := r
		rows = append(rows,
			layout.Rigid(pg.skaBalanceRow(r.symbol, r.total, 0)),
			layout.Rigid(pg.skaBalanceRow(values.String(values.StrLabelSpendable), r.spendable, values.MarginPadding12)),
			layout.Rigid(pg.skaBalanceRow(values.String(values.StrLocked), r.locked, values.MarginPadding12)),
			layout.Rigid(pg.skaBalanceRow(values.String(values.StrImmature), r.immature, values.MarginPadding12)),
		)
	}
	return rows
}

// skaBalanceRow renders a single "title .... amount" line. Regular weight —
// per the design only the account header total is bold. leftInset indents
// the available/locked/immature sub-rows under their coin's symbol row.
func (pg *Page) skaBalanceRow(title, amount string, leftInset unit.Dp) func(gtx C) D {
	label := pg.Theme.Label(pg.ConvertTextSize(values.TextSize16), title)
	amountTxt := pg.Theme.Label(pg.ConvertTextSize(values.TextSize16), amount)
	return func(gtx C) D {
		return layout.Inset{Left: leftInset}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Spacing: layout.SpaceBetween}.Layout(gtx,
				layout.Rigid(label.Layout),
				layout.Flexed(1, func(gtx C) D {
					return layout.E.Layout(gtx, amountTxt.Layout)
				}),
			)
		})
	}
}

// accountBalanceLayout is VAR-only. `bal` arrives as a sharedW.AssetAmount
// (the int64 + .ToCoin() channel), which by Monetarium convention carries
// VAR atoms (1e8/coin). All current call sites in this page pass VAR
// balances (Total, Spendable, Locked, immature/staking). If a future
// refactor pipes a SKA AssetAmount through here, the .String() row and
// the .ToCoin()→USD conversion below will both silently misformat — SKA
// values > 9.22 hit the int64 clamp upstream and .ToCoin() divides by
// 1e8 instead of 1e18. SKA balances are surfaced through a different
// path (GetCoinBalance().SKATotal → FormatTxAmountBig / FormatCoinAmount),
// not this layout.
func (pg *Page) accountBalanceLayout(title string, bal sharedW.AssetAmount, balAxis layout.Axis, bold bool) func(gtx C) D {
	weight := font.Normal
	if bold {
		weight = font.SemiBold
	}
	label := pg.Theme.Label(pg.ConvertTextSize(values.TextSize16), title)
	label.Font.Weight = weight
	balanceTxt := pg.Theme.Label(pg.ConvertTextSize(values.TextSize16), bal.String())
	balanceTxt.Font.Weight = weight
	return func(gtx C) D {
		return layout.Flex{Spacing: layout.SpaceBetween}.Layout(gtx,
			layout.Rigid(label.Layout), // Title
			layout.Flexed(1, func(gtx C) D { // Balances
				return layout.E.Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: balAxis, Alignment: layout.End}.Layout(gtx,
						layout.Rigid(balanceTxt.Layout),
						layout.Rigid(func(gtx C) D {
							if !pg.usdExchangeSet || pg.exchangeRate <= 0 || bal.ToCoin() == 0 {
								return D{}
							}

							balanceUSD := fmt.Sprintf(" (%v)", utils.FormatAsUSDString(pg.Printer, utils.CryptoToUSD(pg.exchangeRate, bal.ToCoin())))
							usdAmtLabel := pg.Theme.Label(pg.ConvertTextSize(values.TextSize16), balanceUSD)
							usdAmtLabel.Font.Weight = font.SemiBold
							return usdAmtLabel.Layout(gtx)
						}),
					)
				})
			}),
		)
	}
}

// HandleUserInteractions is called just before Layout() to determine
// if any user interaction recently occurred on the page and may be
// used to update the page's UI components shortly before they are
// displayed.
// Part of the load.Page interface.
func (pg *Page) HandleUserInteractions(gtx C) {
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

	if clicked, selectedItem := pg.accountsList.ItemClicked(); clicked {
		if pg.wallet.GetAssetType() == libutils.DCRWalletAsset {
			pg.ParentNavigator().Display(NewDCRAcctDetailsPage(pg.Load, pg.wallet, pg.accounts[selectedItem]))
		}
	}
}
