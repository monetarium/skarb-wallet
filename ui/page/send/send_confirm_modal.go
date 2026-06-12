package send

import (
	"fmt"
	"image"

	"gioui.org/font"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/modal"
	"github.com/monetarium/skarb-wallet/ui/page/components"
	"github.com/monetarium/skarb-wallet/ui/values"
)

type sendConfirmModal struct {
	*load.Load
	*cryptomaterial.Modal
	modal.CreatePasswordModal

	closeConfirmationModalButton cryptomaterial.Button
	confirmButton                cryptomaterial.Button
	passwordEditor               cryptomaterial.Editor

	txSent    func()
	// txBroadcastSuccess fires from the broadcast goroutine the instant
	// Broadcast() returns nil, BEFORE the success modal is shown. The page
	// uses it to nudge the underlying Send form out of its pre-broadcast
	// state (amount editor still holds the typed atoms, balance just hit
	// zero, per-frame validation flips to "Insufficient funds" behind the
	// green-check modal — jarring). The callback must be goroutine-safe:
	// the Send page's implementation does an atomic flag flip + window
	// Invalidate, and the actual editor.SetText / autoDefault work happens
	// on the next UI-thread HandleUserInteractions tick.
	txBroadcastSuccess func()
	isSending          bool

	*authoredTxData
	asset           sharedW.Asset
	exchangeRateSet bool
	txLabel         string
	sentHandle      func(string)
}

func newSendConfirmModal(l *load.Load, data *authoredTxData, asset sharedW.Asset, sentHandle func(string)) *sendConfirmModal {
	scm := &sendConfirmModal{
		Load:           l,
		authoredTxData: data,
		asset:          asset,
		sentHandle:     sentHandle,
	}
	scm.Modal = l.Theme.ModalFloatTitle("send_confirm_modal", l.IsMobileView(), scm.firstLoad)

	scm.closeConfirmationModalButton = l.Theme.OutlineButton(values.String(values.StrCancel))
	scm.closeConfirmationModalButton.Font.Weight = font.Medium

	// Pre-resolve the translated label in the constructor: the modal is
	// short-lived (created on Confirm click, gone after "Гаразд") so we
	// don't need a per-frame refresh, and resolving here also fixes the
	// legacy bug where Layout assigned `scm.confirmButton.Text =
	// values.StrSend` — that's the localization-table KEY ("send"), not
	// the translated text, so the final action button showed the literal
	// "send" string verbatim in both EN and UK locales.
	scm.confirmButton = l.Theme.Button(values.String(values.StrSend))
	scm.confirmButton.Font.Weight = font.Medium
	scm.confirmButton.SetEnabled(false)

	scm.passwordEditor = l.Theme.EditorPassword(new(widget.Editor), values.String(values.StrSpendingPassword))
	scm.passwordEditor.Editor.SetText("")
	scm.passwordEditor.Editor.SingleLine = true
	scm.passwordEditor.Editor.Submit = true

	return scm
}

func (scm *sendConfirmModal) OnResume() {}

func (scm *sendConfirmModal) firstLoad(gtx C) {
	gtx.Execute(key.FocusCmd{Tag: scm.passwordEditor.Editor})
}

func (scm *sendConfirmModal) SetError(err string) {
	scm.passwordEditor.SetError(values.TranslateErr(err))
}

func (scm *sendConfirmModal) setLoading(loading bool) {
	scm.isSending = loading
}

func (scm *sendConfirmModal) OnDismiss() {}

func (scm *sendConfirmModal) broadcastTransaction() {
	password := scm.passwordEditor.Editor.Text()
	if password == "" || scm.isSending {
		return
	}

	scm.setLoading(true)
	go func() {
		defer scm.setLoading(false)
		txHash, err := scm.asset.Broadcast(password, scm.txLabel)
		if err != nil {
			scm.SetError(err.Error())
			scm.confirmButton.SetEnabled(false)
			scm.ParentWindow().Reload()
			return
		}
		// Capture the callbacks once, *before* the modals start moving
		// around. The success-modal "Гаразд" handler runs on a later
		// event-loop tick after the user clicks; if we let scm.txSent()
		// fire in this goroutine (as the original code did) it would
		// synchronously dismiss pg.modalLayout — tearing down the page
		// and its ParentNavigator — and then the "Гаразд" callback
		// would dereference a destroyed page through scm.sentHandle,
		// crashing the goroutine and silently killing the Gio event
		// loop. That's bug #4 in the v1 report ("Skarb closes after
		// Гаразд"). Run sentHandle (navigate to tx details) BEFORE
		// txSent (teardown) so the navigator is still alive at the
		// time we need it.
		handleSent := scm.sentHandle
		teardown := scm.txSent

		// Form-state reset goes here, not in `teardown`. Reset clears the
		// recipient editors and the cached fee/total strings — operations
		// safe to schedule from this goroutine via an atomic-flag-+
		// -invalidate pattern (the Send page picks them up on the next
		// HandleUserInteractions tick). Doing it BEFORE ShowModal means the
		// form behind the green-check success modal is already clean by
		// the time Gio repaints, so the user doesn't see "Недостатньо
		// коштів" appearing under the "Transaction sent!" banner. The
		// page-teardown (`teardown` / `txSent`) still happens after
		// "Гаразд" because that's the call that dismisses pg.modalLayout
		// — running it inline crashes the navigator (bug #4 in v1).
		if scm.txBroadcastSuccess != nil {
			scm.txBroadcastSuccess()
		}

		successModal := modal.NewSuccessModal(scm.Load, values.String(values.StrTxSent), func(_ bool, _ *modal.InfoModal) bool {
			if handleSent != nil {
				handleSent(txHash)
			}
			if teardown != nil {
				teardown()
			}
			return true
		})
		scm.ParentWindow().ShowModal(successModal)
		// Confirm modal goes away now; the success modal sits on top
		// and waits for "Гаразд". The Send page underneath stays
		// mounted until the success modal fires its callback —
		// recipient field state stays visible behind the modal, which
		// is the same as the old behaviour from the user's point of
		// view but without the use-after-free.
		scm.Dismiss()
	}()
}

func (scm *sendConfirmModal) Handle(gtx C) {
	if scm.passwordEditor.Changed() {
		scm.confirmButton.SetEnabled(scm.passwordEditor.Editor.Text() != "")
		scm.passwordEditor.SetError("")
	}

	if scm.passwordEditor.Submitted() || scm.confirmButton.Clicked(gtx) {
		scm.broadcastTransaction()
	}

	if scm.closeConfirmationModalButton.Clicked(gtx) {
		if !scm.isSending {
			scm.Dismiss()
		}
	}
}

func (scm *sendConfirmModal) Layout(gtx C) D {
	dp16 := values.MarginPadding16
	w := []layout.Widget{
		func(gtx C) D {
			scm.SetPadding(unit.Dp(0))
			min := gtx.Constraints.Min
			return layout.Stack{Alignment: layout.Center}.Layout(gtx,
				layout.Expanded(func(gtx C) D {
					defer clip.RRect{
						Rect: image.Rectangle{Max: image.Point{
							X: gtx.Constraints.Min.X,
							Y: gtx.Constraints.Min.Y,
						}},

						NE: 14,
						NW: 14,
					}.Push(gtx.Ops).Pop()
					paint.Fill(gtx.Ops, scm.Theme.Color.Gray5)
					return D{Size: gtx.Constraints.Min}
				}),
				layout.Stacked(func(gtx C) D {
					gtx.Constraints.Min = min
					return layout.Inset{Top: values.MarginPadding24, Bottom: dp16}.Layout(gtx, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(func(gtx C) D {
								return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
									layout.Rigid(func(gtx C) D {
										icon := scm.Theme.Icons.SendIcon
										return layout.Inset{Top: values.MarginPaddingMinus8}.Layout(gtx, func(gtx C) D {
											return layout.Center.Layout(gtx, icon.Layout24dp)
										})
									}),
									layout.Rigid(func(gtx C) D {
										sendInfoLabel := scm.Theme.Label(unit.Sp(16), values.String(values.StrSendConfModalTitle))
										return layout.Inset{Top: values.MarginPadding2}.Layout(gtx, func(gtx C) D {
											return layout.Center.Layout(gtx, sendInfoLabel.Layout)
										})
									}),
									layout.Rigid(func(gtx C) D {
										// Only append the USD parenthetical when a rate is set
										// AND the coin has a USD pairing (exchangeRateSet is
										// false for SKA) — otherwise the header rendered
										// "5.0 SKA1 ( - )" or a bogus VAR-rate dollar value.
										amountText := scm.sendAmount
										if scm.exchangeRateSet {
											amountText = scm.sendAmount + " (" + scm.sendAmountUSD + ")"
										}
										balLabel := scm.Theme.Label(unit.Sp(24), amountText)
										return layout.Inset{Top: values.MarginPadding2}.Layout(gtx, func(gtx C) D {
											return layout.Center.Layout(gtx, balLabel.Layout)
										})
									}),
								)
							}),
						)
					})
				}),
			)
		},
		func(gtx C) D {
			return layout.Inset{
				Left: dp16,
				Top:  dp16, Right: dp16,
			}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						sendWallet := scm.AssetsManager.WalletWithID(scm.sourceAccount.WalletID)
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx C) D {
								txt := scm.Theme.Body2(values.String(values.StrFrom))
								txt.Color = scm.Theme.Color.GrayText2
								return txt.Layout(gtx)
							}),
							layout.Rigid(scm.setWalletLogo),
							layout.Rigid(func(gtx C) D {
								txt := scm.Theme.Label(unit.Sp(16), sendWallet.GetWalletName())
								txt.Color = scm.Theme.Color.Text
								txt.Font.Weight = font.Medium
								return txt.Layout(gtx)
							}),
							layout.Rigid(func(gtx C) D {
								card := scm.Theme.Card()
								card.Radius = cryptomaterial.Radius(0)
								card.Color = scm.Theme.Color.Gray4
								inset := layout.Inset{
									Left: values.MarginPadding5,
								}
								return inset.Layout(gtx, func(gtx C) D {
									return card.Layout(gtx, func(gtx C) D {
										txt := scm.Theme.Caption(scm.sourceAccount.Name)
										txt.Color = scm.Theme.Color.GrayText1
										return layout.UniformInset(values.MarginPadding2).Layout(gtx, txt.Layout)
									})
								})
							}),
						)
					}),
					layout.Rigid(func(gtx C) D {
						return layout.Inset{Top: values.MarginPadding8, Bottom: values.MarginPadding8}.Layout(gtx, func(gtx C) D {
							return layout.Center.Layout(gtx, scm.Theme.Icons.ArrowDownIcon.Layout16dp)
						})
					}),
					layout.Rigid(func(gtx C) D {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx C) D {
								txt := scm.Theme.Body2(values.String(values.StrTo))
								txt.Color = scm.Theme.Color.GrayText2
								return txt.Layout(gtx)
							}),
							layout.Rigid(scm.toDestinationLayout),
						)
					}),
					layout.Rigid(layout.Spacer{Height: values.MarginPadding8}.Layout),
					layout.Rigid(scm.Theme.Separator().Layout),
					layout.Rigid(layout.Spacer{Height: values.MarginPadding8}.Layout),
					layout.Rigid(func(gtx C) D {
						return layout.Inset{Bottom: values.MarginPadding8}.Layout(gtx, func(gtx C) D {
							txFeeText := scm.txFee
							if scm.exchangeRateSet {
								txFeeText = fmt.Sprintf("%s (%s)", scm.txFee, scm.txFeeUSD)
							}
							return scm.contentRow(gtx, values.String(values.StrFee), txFeeText, "")
						})
					}),
					layout.Rigid(func(gtx C) D {
						totalCostText := scm.totalCost
						if scm.exchangeRateSet {
							totalCostText = fmt.Sprintf("%s (%s)", scm.totalCost, scm.totalCostUSD)
						}
						return scm.contentRow(gtx, values.String(values.StrTotalCost), totalCostText, "")
					}),
				)
			})
		},
		func(gtx C) D {
			return layout.Inset{Left: dp16, Right: dp16}.Layout(gtx, scm.passwordEditor.Layout)
		},
		func(gtx C) D {
			return layout.Inset{Left: dp16, Right: dp16, Bottom: dp16}.Layout(gtx, func(gtx C) D {
				return layout.E.Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							return layout.Inset{
								Right: values.MarginPadding8,
							}.Layout(gtx, func(gtx C) D {
								if scm.isSending {
									return D{}
								}
								return scm.closeConfirmationModalButton.Layout(gtx)
							})
						}),
						layout.Rigid(func(gtx C) D {
							if scm.isSending {
								return layout.Inset{Top: unit.Dp(7)}.Layout(gtx, func(gtx C) D {
									return material.Loader(scm.Theme.Base).Layout(gtx)
								})
							}
							// Label set once in the constructor with the
							// translated text — don't reassign it here
							// (the old code wrote the literal key "send").
							return scm.confirmButton.Layout(gtx)
						}),
					)
				})
			})
		},
	}
	return scm.Modal.Layout(gtx, w)
}

func (scm *sendConfirmModal) toDestinationLayout(gtx C) D {
	flexChilds := make([]layout.FlexChild, 0)

	for i := range scm.destinationAccount {
		account := scm.destinationAccount[i]
		flexChilds = append(flexChilds, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return scm.toDestinationAccountLayout(account, gtx)
		}))
	}

	for i := range scm.destinationAddress {
		address := scm.destinationAddress[i]
		flexChilds = append(flexChilds, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return scm.toDestinationAddressLayout(address, gtx)
		}))
	}

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, flexChilds...)
}

func (scm *sendConfirmModal) toDestinationAccountLayout(acccount *sharedW.Account, gtx C) D {
	return layout.E.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(scm.setWalletLogo),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{}.Layout(gtx, func(gtx C) D {
					walletName := scm.AssetsManager.WalletWithID(acccount.WalletID).GetWalletName()
					txt := scm.Theme.Label(unit.Sp(16), walletName)
					txt.Color = scm.Theme.Color.Text
					txt.Font.Weight = font.Medium
					return txt.Layout(gtx)
				})
			}),
			layout.Rigid(func(gtx C) D {
				card := scm.Theme.Card()
				card.Radius = cryptomaterial.Radius(0)
				card.Color = scm.Theme.Color.Gray4
				inset := layout.Inset{
					Left: values.MarginPadding5,
				}
				return inset.Layout(gtx, func(gtx C) D {
					return card.Layout(gtx, func(gtx C) D {
						return layout.UniformInset(values.MarginPadding2).Layout(gtx, func(gtx C) D {
							txt := scm.Theme.Caption(acccount.Name)
							txt.Color = scm.Theme.Color.GrayText1
							return txt.Layout(gtx)
						})
					})
				})
			}),
		)
	})
}

func (scm *sendConfirmModal) toDestinationAddressLayout(address string, gtx C) D {
	inset := layout.Inset{
		Left: values.MarginPadding5,
	}
	return inset.Layout(gtx, func(gtx C) D {
		return layout.UniformInset(values.MarginPadding2).Layout(gtx, scm.Theme.Body2(address).Layout)
	})
}

func (scm *sendConfirmModal) contentRow(gtx C, leftValue, rightValue, walletName string) D {
	return layout.Flex{}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			txt := scm.Theme.Body2(leftValue)
			txt.Color = scm.Theme.Color.GrayText2
			return txt.Layout(gtx)
		}),
		layout.Flexed(1, func(gtx C) D {
			return layout.E.Layout(gtx, func(gtx C) D {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(scm.Theme.Body1(rightValue).Layout),
					layout.Rigid(func(gtx C) D {
						if walletName != "" {
							card := scm.Theme.Card()
							card.Radius = cryptomaterial.Radius(0)
							card.Color = scm.Theme.Color.Gray4
							inset := layout.Inset{
								Left: values.MarginPadding5,
							}
							return inset.Layout(gtx, func(gtx C) D {
								return card.Layout(gtx, func(gtx C) D {
									return layout.UniformInset(values.MarginPadding2).Layout(gtx, func(gtx C) D {
										txt := scm.Theme.Caption(walletName)
										txt.Color = scm.Theme.Color.GrayText2
										return txt.Layout(gtx)
									})
								})
							})
						}
						return D{}
					}),
				)
			})
		}),
	)
}

func (scm *sendConfirmModal) setWalletLogo(gtx C) D {
	walletIcon := components.CoinImageBySymbol(scm.Load, scm.asset.GetAssetType(), false)
	if walletIcon == nil {
		return D{}
	}
	inset := layout.Inset{
		Right: values.MarginPadding8, Left: values.MarginPadding25,
	}
	return inset.Layout(gtx, walletIcon.Layout16dp)
}
