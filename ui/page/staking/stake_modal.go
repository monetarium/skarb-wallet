package staking

import (
	"context"
	"strconv"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/modal"
	"github.com/monetarium/skarb-wallet/ui/page/components"
	"github.com/monetarium/skarb-wallet/ui/values"
)

type ticketBuyerModal struct {
	*load.Load
	*cryptomaterial.Modal

	settingsSaved func()
	onCancel      func()

	cancel          cryptomaterial.Button
	saveSettingsBtn cryptomaterial.Button

	balToMaintainEditor cryptomaterial.Editor
	accountDropdown     *components.AccountDropdown

	vspSelector *components.VSPSelector

	dcrImpl *dcr.Asset
}

func newTicketBuyerModal(l *load.Load, wallet *dcr.Asset) *ticketBuyerModal {
	tb := &ticketBuyerModal{
		Load:  l,
		Modal: l.Theme.ModalFloatTitle("staking_modal", l.IsMobileView(), nil),

		cancel:          l.Theme.OutlineButton(values.String(values.StrCancel)),
		saveSettingsBtn: l.Theme.Button(values.String(values.StrSave)),
		// AllowDirectBuy: the auto-buyer works solo too (nil VSPClient in
		// StartTicketBuyer) — an empty saved host round-trips as Direct buy.
		vspSelector:     components.NewVSPSelector(l, wallet).Title(values.String(values.StrSelectVSP)).AllowDirectBuy(),
		dcrImpl:         wallet,
	}

	tb.balToMaintainEditor = l.Theme.Editor(new(widget.Editor), values.String(values.StrBalToMaintain))
	tb.balToMaintainEditor.Editor.SingleLine = true

	tb.saveSettingsBtn.SetEnabled(false)

	return tb
}

func (tb *ticketBuyerModal) OnSettingsSaved(settingsSaved func()) *ticketBuyerModal {
	tb.settingsSaved = settingsSaved
	return tb
}

func (tb *ticketBuyerModal) OnCancel(cancel func()) *ticketBuyerModal {
	tb.onCancel = cancel
	return tb
}

func (tb *ticketBuyerModal) SetError(err string) {
	tb.balToMaintainEditor.SetError(values.TranslateErr(err))
}

func (tb *ticketBuyerModal) OnResume() {
	if tb.dcrImpl == nil {
		log.Error("Only DCR implementation is supportted")
		return
	}

	tb.initializeAccountSelector(tb.dcrImpl)
	tb.accountDropdown.ListenForTxNotifications(tb.ParentWindow()) // listener is stopped in OnDismissed()

	if len(tb.dcrImpl.KnownVSPs()) == 0 {
		// TODO: Does this modal need this list?
		go tb.dcrImpl.ReloadVSPList(context.TODO())
	}

	// loop through all available wallets and select the one with ticket buyer config.
	// if non, set the selected wallet to the first.
	// temporary work around for only one wallet.
	if tb.dcrImpl.TicketBuyerConfigIsSet() {
		tbConfig := tb.dcrImpl.AutoTicketsBuyerConfig()

		account, err := components.GetTicketPurchaseAccount(tb.dcrImpl)
		if err != nil {
			errModal := modal.NewErrorModal(tb.Load, err.Error(), modal.DefaultClickFunc())
			tb.ParentWindow().ShowModal(errModal)
		}

		_ = tb.accountDropdown.Setup(tb.dcrImpl, account)

		tb.vspSelector.SelectVSP(tbConfig.VspHost)
		w := tb.dcrImpl
		tb.balToMaintainEditor.Editor.SetText(strconv.FormatFloat(w.ToAmount(tbConfig.BalanceToMaintain).ToCoin(), 'f', 0, 64))
	}

	if tb.accountDropdown.SelectedAccount() == nil {
		_ = tb.accountDropdown.Setup(tb.dcrImpl)
	}
}

func (tb *ticketBuyerModal) Layout(gtx C) D {
	l := []layout.Widget{
		func(gtx C) D {
			t := tb.Theme.H6(values.String(values.StrAutoTicketPurchase))
			t.TextSize = values.TextSizeTransform(tb.IsMobileView(), values.TextSize20)
			t.Font.Weight = font.SemiBold
			return t.Layout(gtx)
		},
		func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return layout.Inset{
						Top:    values.MarginPadding8,
						Bottom: values.MarginPadding16,
					}.Layout(gtx, func(gtx C) D {
						return tb.accountDropdown.Layout(gtx, "")
					})
				}),
				layout.Rigid(func(gtx C) D {
					tb.balToMaintainEditor.TextSize = values.TextSizeTransform(tb.IsMobileView(), values.TextSize14)
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(tb.balToMaintainEditor.Layout),
						layout.Rigid(func(gtx C) D {
							// Clarify that this field is a RESERVE, not the spend amount.
							hint := tb.Theme.Label(values.TextSize12, values.String(values.StrBalToMaintainHint))
							hint.Color = tb.Theme.Color.GrayText2
							return layout.Inset{Top: values.MarginPadding4}.Layout(gtx, hint.Layout)
						}),
					)
				}),
				layout.Rigid(func(gtx C) D {
					return components.VerticalInset(values.MarginPadding16).Layout(gtx, func(gtx C) D {
						return tb.vspSelector.Layout(tb.ParentWindow(), gtx)
					})
				}),
				layout.Rigid(func(gtx C) D {
					// Solo caveat, shown only once Direct buy is picked —
					// same warning as the manual purchase modal.
					if vsp := tb.vspSelector.SelectedVSP(); vsp == nil || !vsp.IsDirectBuy() {
						return D{}
					}
					warn := tb.Theme.Label(values.TextSize12, values.String(values.StrDirectBuyWarning))
					warn.Color = tb.Theme.Color.Danger
					return layout.Inset{Bottom: values.MarginPadding8}.Layout(gtx, warn.Layout)
				}),
			)
		},
		func(gtx C) D {
			return layout.E.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						return layout.Inset{
							Right: values.MarginPadding4,
						}.Layout(gtx, tb.cancel.Layout)
					}),
					layout.Rigid(func(gtx C) D {
						return tb.saveSettingsBtn.Layout(gtx)
					}),
				)
			})
		},
	}

	return tb.Modal.Layout(gtx, l)
}

func (tb *ticketBuyerModal) canSave() bool {
	if tb.vspSelector.SelectedVSP() == nil {
		return false
	}

	if tb.balToMaintainEditor.Editor.Text() == "" {
		return false
	}

	// No account is selectable for a watch-only wallet, or when account loading
	// failed — keep Save disabled so the click handler never derefs a nil
	// account (SelectedAccount().Number).
	if tb.accountDropdown.SelectedAccount() == nil {
		return false
	}

	return true
}

func (tb *ticketBuyerModal) initializeAccountSelector(wallet *dcr.Asset) {
	tb.accountDropdown = components.NewAccountDropdown(tb.Load).
		SetChangedCallback(func(_ *sharedW.Account) {}).
		AccountValidator(func(account *sharedW.Account) bool {
			// Imported and watch only wallet accounts are invalid for sending
			accountIsValid := account.Number != dcr.ImportedAccountNumber && !tb.dcrImpl.IsWatchingOnlyWallet()

			if tb.dcrImpl.ReadBoolConfigValueForKey(sharedW.AccountMixerConfigSet, false) &&
				!tb.dcrImpl.ReadBoolConfigValueForKey(sharedW.SpendUnmixedFundsKey, false) {
				// Spending from unmixed accounts is disabled for the selected wallet
				accountIsValid = account.Number == tb.dcrImpl.MixedAccountNumber()
			}

			return accountIsValid
		}).
		Setup(wallet)
}

func (tb *ticketBuyerModal) OnDismiss() {
	tb.accountDropdown.StopTxNtfnListener()
}

func (tb *ticketBuyerModal) Handle(gtx C) {
	tb.accountDropdown.Handle(gtx)
	tb.saveSettingsBtn.SetEnabled(tb.canSave())

	if tb.cancel.Clicked(gtx) || tb.Modal.BackdropClicked(gtx, true) {
		tb.onCancel()
		tb.Dismiss()
	}

	if tb.saveSettingsBtn.Clicked(gtx) {
		account := tb.accountDropdown.SelectedAccount()
		if account == nil {
			// Defensive: canSave() gates the button on a non-nil account, so
			// this should be unreachable — never deref a nil account.
			return
		}
		vspHost := tb.vspSelector.SelectedVSP().Host
		amount, err := strconv.ParseFloat(tb.balToMaintainEditor.Editor.Text(), 64)
		if err != nil {
			tb.SetError(err.Error())
			return
		}

		balToMaintain := dcr.AmountAtom(amount)

		tb.dcrImpl.SetAutoTicketsBuyerConfig(vspHost, account.Number, balToMaintain)
		tb.settingsSaved()
		tb.Dismiss()
	}
}
