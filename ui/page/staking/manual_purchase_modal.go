package staking

import (
	"context"
	"strconv"
	"sync/atomic"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/monetarium/monetarium-node/dcrutil"
	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/page/components"
	"github.com/monetarium/skarb-wallet/ui/values"
)

// manualPurchaseModal collects the inputs for a one-time, manual ticket
// purchase (account, ticket count, VSP) and hands them to onPurchase. It does
// NOT hold the wallet passphrase or run the purchase itself — the page opens a
// password-confirm modal and runs the blocking PurchaseTickets call off the UI
// thread (see stake_overview.go::startManualPurchasePasswordModal).
//
// Unlike the auto-buyer (ticketBuyerModal), this stakes a user-chosen, fixed
// number of tickets right now — directly answering "what amount is staked": the
// modal shows N × ticketPrice = total live, and gates the Buy button on the
// account's spendable VAR balance.
type manualPurchaseModal struct {
	*load.Load
	*cryptomaterial.Modal

	// onPurchase receives the modal's CURRENT ticket price so the confirm
	// modal's total matches what this card displayed (the price refreshes
	// per block while the card is open — the page's open-time snapshot may
	// be stale by purchase time).
	onPurchase func(accountNumber, numTickets int32, vsp *dcr.VSP, ticketPriceAtoms int64)
	onCancel   func()

	cancelBtn   cryptomaterial.Button
	purchaseBtn cryptomaterial.Button

	ticketsEditor   cryptomaterial.Editor
	accountDropdown *components.AccountDropdown
	vspSelector     *components.VSPSelector

	dcrImpl *dcr.Asset
	// ticketPrice is the network ticket price in VAR atoms (int64-safe: tickets
	// are VAR-only). Seeded by the page when the modal is opened, then kept
	// fresh while the modal is up (see the pendingRefresh drain in Handle).
	ticketPrice int64

	// pendingRefresh is set by the modal's tx/block notification listener
	// (goroutine) and drained on the UI thread in Handle, where the ticket
	// price and the account balances are re-read. Without it both values were
	// open-time snapshots that went stale while the modal stayed up.
	pendingRefresh atomic.Bool
}

// purchaseModalListenerID keys this modal's own TxAndBlockNotificationListener —
// distinct from both the page's (OverviewPageID) and the account dropdown's
// IDs, so registration never collides with either.
const purchaseModalListenerID = "manual_purchase_modal"

func newManualPurchaseModal(l *load.Load, wallet *dcr.Asset, ticketPrice int64) *manualPurchaseModal {
	mp := &manualPurchaseModal{
		Load:        l,
		Modal:       l.Theme.ModalFloatTitle("manual_purchase_modal", l.IsMobileView(), nil),
		cancelBtn:   l.Theme.OutlineButton(values.String(values.StrCancel)),
		purchaseBtn: l.Theme.Button(values.String(values.StrBuyTickets)),
		vspSelector: components.NewVSPSelector(l, wallet).Title(values.String(values.StrSelectVSP)).AllowDirectBuy(),
		dcrImpl:     wallet,
		ticketPrice: ticketPrice,
	}

	mp.ticketsEditor = l.Theme.Editor(new(widget.Editor), values.String(values.StrNumberOfTickets))
	mp.ticketsEditor.Editor.SingleLine = true
	mp.ticketsEditor.Editor.SetText("1")

	mp.purchaseBtn.SetEnabled(false)
	return mp
}

func (mp *manualPurchaseModal) OnPurchase(f func(accountNumber, numTickets int32, vsp *dcr.VSP, ticketPriceAtoms int64)) *manualPurchaseModal {
	mp.onPurchase = f
	return mp
}

func (mp *manualPurchaseModal) OnCancel(f func()) *manualPurchaseModal {
	mp.onCancel = f
	return mp
}

func (mp *manualPurchaseModal) OnResume() {
	mp.accountDropdown = components.NewAccountDropdown(mp.Load).
		SetChangedCallback(func(_ *sharedW.Account) {}).
		AccountValidator(func(account *sharedW.Account) bool {
			// Imported and watch-only accounts can't fund a ticket purchase.
			return account.Number != dcr.ImportedAccountNumber && !mp.dcrImpl.IsWatchingOnlyWallet()
		}).
		Setup(mp.dcrImpl)
	mp.accountDropdown.ListenForTxNotifications(mp.ParentWindow()) // stopped in OnDismiss()

	// Keep the card live while it's open: each new tx/block flags a refresh
	// of the ticket price and account balances (drained in Handle on the UI
	// thread — CLAUDE.md §3).
	err := mp.dcrImpl.AddTxAndBlockNotificationListener(&sharedW.TxAndBlockNotificationListener{
		OnTransaction: func(_ int, _ *sharedW.Transaction) {
			mp.pendingRefresh.Store(true)
			mp.ParentWindow().Reload()
		},
		OnBlockAttached: func(_ int, _ int32) {
			mp.pendingRefresh.Store(true)
			mp.ParentWindow().Reload()
		},
	}, purchaseModalListenerID)
	if err != nil {
		log.Errorf("manual purchase modal: adding tx/block listener: %v", err)
	}

	if len(mp.dcrImpl.KnownVSPs()) == 0 {
		go mp.dcrImpl.ReloadVSPList(context.TODO())
	}
}

func (mp *manualPurchaseModal) OnDismiss() {
	if mp.accountDropdown != nil {
		mp.accountDropdown.StopTxNtfnListener()
	}
	mp.dcrImpl.RemoveTxAndBlockNotificationListener(purchaseModalListenerID)
}

// parseTicketCount returns the integer ticket count entered, or 0 if the field
// is empty or not a positive integer.
func (mp *manualPurchaseModal) parseTicketCount() int {
	n, err := strconv.Atoi(mp.ticketsEditor.Editor.Text())
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// totalCost returns the cost of n tickets in VAR atoms. n*ticketPrice cannot
// overflow int64 for any sane n (price ~2.6e10 atoms, n bounded by balance).
func (mp *manualPurchaseModal) totalCost(n int) int64 {
	return int64(n) * mp.ticketPrice
}

func (mp *manualPurchaseModal) spendable() int64 {
	account := mp.accountDropdown.SelectedAccount()
	if account == nil || account.Balance == nil || account.Balance.Spendable == nil {
		return 0
	}
	return account.Balance.Spendable.ToInt() // VAR atoms
}

func (mp *manualPurchaseModal) Layout(gtx C) D {
	textSize14 := values.TextSizeTransform(mp.IsMobileView(), values.TextSize14)
	n := mp.parseTicketCount()
	total := mp.totalCost(n)
	spendable := mp.spendable()
	insufficient := n >= 1 && total > spendable

	l := []layout.Widget{
		func(gtx C) D {
			t := mp.Theme.H6(values.String(values.StrBuyTickets))
			t.TextSize = values.TextSizeTransform(mp.IsMobileView(), values.TextSize20)
			t.Font.Weight = font.SemiBold
			return t.Layout(gtx)
		},
		func(gtx C) D {
			hint := mp.Theme.Label(values.TextSize12, values.String(values.StrManualStakeHint))
			hint.Color = mp.Theme.Color.GrayText2
			return layout.Inset{Bottom: values.MarginPadding8}.Layout(gtx, hint.Layout)
		},
		func(gtx C) D {
			return layout.Inset{Top: values.MarginPadding8, Bottom: values.MarginPadding8}.Layout(gtx, func(gtx C) D {
				return mp.accountDropdown.Layout(gtx, "")
			})
		},
		func(gtx C) D {
			mp.ticketsEditor.TextSize = textSize14
			return mp.ticketsEditor.Layout(gtx)
		},
		func(gtx C) D {
			// Live cost breakdown — the whole point: make the staked amount obvious.
			return layout.Inset{Top: values.MarginPadding12, Bottom: values.MarginPadding12}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						return mp.kvRow(gtx, values.String(values.StrTicketPrice), dcrutil.Amount(mp.ticketPrice).String(), false)
					}),
					layout.Rigid(func(gtx C) D {
						return mp.kvRow(gtx, values.String(values.StrTotalCost), dcrutil.Amount(total).String(), true)
					}),
					layout.Rigid(func(gtx C) D {
						return mp.kvRow(gtx, values.String(values.StrLabelSpendable), dcrutil.Amount(spendable).String(), false)
					}),
				)
			})
		},
		func(gtx C) D {
			if !insufficient {
				return D{}
			}
			warn := mp.Theme.Label(values.TextSize12, values.StringF(values.StrInsufficientForTickets,
				dcrutil.Amount(total).String(), dcrutil.Amount(spendable).String()))
			warn.Color = mp.Theme.Color.Danger
			return layout.Inset{Bottom: values.MarginPadding8}.Layout(gtx, warn.Layout)
		},
		func(gtx C) D {
			return components.VerticalInset(values.MarginPadding8).Layout(gtx, func(gtx C) D {
				return mp.vspSelector.Layout(mp.ParentWindow(), gtx)
			})
		},
		func(gtx C) D {
			// Solo staking caveat, shown only once Direct buy is picked.
			if vsp := mp.vspSelector.SelectedVSP(); vsp == nil || !vsp.IsDirectBuy() {
				return D{}
			}
			warn := mp.Theme.Label(values.TextSize12, values.String(values.StrDirectBuyWarning))
			warn.Color = mp.Theme.Color.Danger
			return layout.Inset{Bottom: values.MarginPadding8}.Layout(gtx, warn.Layout)
		},
		func(gtx C) D {
			return layout.E.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						return layout.Inset{Right: values.MarginPadding4}.Layout(gtx, mp.cancelBtn.Layout)
					}),
					layout.Rigid(mp.purchaseBtn.Layout),
				)
			})
		},
	}

	return mp.Modal.Layout(gtx, l)
}

// kvRow renders a "label   value" row; emphasize bolds the value (used for the
// total cost).
func (mp *manualPurchaseModal) kvRow(gtx C, key, value string, emphasize bool) D {
	textSize14 := values.TextSizeTransform(mp.IsMobileView(), values.TextSize14)
	return components.VerticalInset(values.MarginPadding4).Layout(gtx, func(gtx C) D {
		return layout.Flex{Spacing: layout.SpaceBetween}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				lbl := mp.Theme.Label(textSize14, key)
				lbl.Color = mp.Theme.Color.GrayText2
				return lbl.Layout(gtx)
			}),
			layout.Rigid(func(gtx C) D {
				lbl := mp.Theme.Label(textSize14, value)
				if emphasize {
					lbl.Font.Weight = font.SemiBold
					lbl.Color = mp.Theme.Color.Text
				}
				return lbl.Layout(gtx)
			}),
		)
	})
}

func (mp *manualPurchaseModal) canPurchase() bool {
	// vsp.PubKey (used downstream by PurchaseTickets) promotes from the embedded
	// *vspd.VspInfoResponse — require it non-nil, except for the Direct-buy
	// sentinel, which is a valid no-VSP selection with a nil embed.
	if vsp := mp.vspSelector.SelectedVSP(); vsp == nil || (!vsp.IsDirectBuy() && vsp.VspInfoResponse == nil) {
		return false
	}
	if mp.accountDropdown.SelectedAccount() == nil {
		return false
	}
	n := mp.parseTicketCount()
	if n < 1 {
		return false
	}
	return mp.totalCost(n) <= mp.spendable()
}

func (mp *manualPurchaseModal) Handle(gtx C) {
	// Drain the notification flag on the UI thread: re-read the ticket price
	// and the account balances so the displayed price / total / spendable
	// (and the insufficient check derived from them) track the chain instead
	// of freezing at open time. Pass the CURRENT selection — a no-arg Setup
	// overwrites it with the first valid account, silently switching which
	// account would fund the purchase (a nil selection falls back to the
	// first account, same as the initial Setup).
	if mp.pendingRefresh.CompareAndSwap(true, false) {
		if tp, err := mp.dcrImpl.TicketPrice(); err == nil && tp != nil {
			mp.ticketPrice = tp.TicketPrice
		}
		if mp.accountDropdown != nil {
			_ = mp.accountDropdown.Setup(mp.dcrImpl, mp.accountDropdown.SelectedAccount())
		}
	}

	mp.accountDropdown.Handle(gtx)
	mp.purchaseBtn.SetEnabled(mp.canPurchase())

	if mp.cancelBtn.Clicked(gtx) || mp.Modal.BackdropClicked(gtx, true) {
		if mp.onCancel != nil {
			mp.onCancel()
		}
		mp.Dismiss()
	}

	if mp.purchaseBtn.Clicked(gtx) {
		account := mp.accountDropdown.SelectedAccount()
		vsp := mp.vspSelector.SelectedVSP()
		n := mp.parseTicketCount()
		// canPurchase() (which gates the enabled button) guarantees these are
		// non-nil and n >= 1, but guard defensively before dereferencing.
		// Direct buy carries a nil VspInfoResponse by design.
		if account == nil || vsp == nil || (!vsp.IsDirectBuy() && vsp.VspInfoResponse == nil) || n < 1 {
			return
		}
		if mp.onPurchase != nil {
			mp.onPurchase(account.Number, int32(n), vsp, mp.ticketPrice)
		}
		mp.Dismiss()
	}
}
