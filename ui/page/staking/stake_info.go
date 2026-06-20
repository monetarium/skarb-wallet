package staking

import (
	"fmt"

	"gioui.org/font"
	"gioui.org/layout"

	"github.com/monetarium/monetarium-node/dcrutil"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/page/components"
	"github.com/monetarium/skarb-wallet/ui/values"
)

func (pg *Page) initStakePriceWidget() *Page {
	pg.stakeSettings = pg.Theme.NewClickable(false)
	_, pg.infoButton = components.SubpageHeaderButtons(pg.Load)

	pg.stake = pg.Theme.Switch()
	return pg
}

func (pg *Page) pageHead(gtx C) D {
	isMobile := pg.Load.IsMobileView()
	txt := pg.Theme.Label(values.TextSizeTransform(isMobile, values.TextSize20), values.String(values.StrStakingInfo))
	txt.Font.Weight = font.SemiBold
	return layout.Inset{
		Bottom: values.MarginPaddingTransform(pg.IsMobileView(), values.MarginPadding24),
	}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Spacing: layout.SpaceBetween}.Layout(gtx,
			layout.Rigid(txt.Layout),
			layout.Rigid(func(gtx C) D {
				if pg.dcrWallet.IsWatchingOnlyWallet() {
					return D{}
				}
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						// "Авто-стейкінг" — the toggle is the *automatic* buyer (needs
						// the passphrase, so it doesn't survive a restart). The one-time
						// manual purchase is the separate "Купити тикети" button below.
						title := pg.Theme.Label(values.TextSizeTransform(isMobile, values.TextSize16), values.String(values.StrAutoStaking))
						title.Color = pg.Theme.Color.GrayText2
						return title.Layout(gtx)
					}),
					layout.Rigid(func(gtx C) D {
						return layout.Inset{
							Right: values.MarginPadding24,
							Left:  values.MarginPadding8,
						}.Layout(gtx, pg.stake.Layout)
					}),
					layout.Rigid(func(gtx C) D {
						icon := pg.Theme.Icons.SettingsIcon
						return pg.stakeSettings.Layout(gtx, func(gtx C) D {
							return icon.LayoutTransform(gtx, isMobile, values.MarginPadding24)
						})
					}),
				)
			}),
		)
	})
}

func (pg *Page) stakePriceSection(gtx C) D {
	return pg.pageSections(gtx, func(gtx C) D {
		mobileView := pg.IsMobileView()
		textSize16 := values.TextSizeTransform(mobileView, values.TextSize16)
		grayText := pg.Theme.Color.GrayText2
		flexAxis := layout.Horizontal
		alignment := layout.Middle
		if mobileView {
			flexAxis = layout.Vertical
			alignment = layout.Start
		}

		ticketsCanBuy := pg.CalculateTotalTicketsCanBuy()

		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(pg.pageHead),
			layout.Rigid(func(gtx C) D {
				return pg.stakeStatusLayout(gtx, ticketsCanBuy)
			}),
			layout.Rigid(func(gtx C) D {
				leftWg := func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							return components.VerticalInset(values.MarginPadding6).Layout(gtx, func(gtx C) D {
								return layout.Flex{Axis: flexAxis, Alignment: alignment}.Layout(gtx,
									layout.Rigid(func(gtx C) D {
										title := pg.Theme.Label(textSize16, values.String(values.StrTicketPrice)+" ")
										title.Color = grayText
										return title.Layout(gtx)
									}),
									layout.Rigid(func(gtx C) D {
										return layout.Center.Layout(gtx, func(gtx C) D {
											if !pg.dcrWallet.IsSynced() || pg.dcrWallet.IsRescanning() || !pg.isTicketsPurchaseAllowed() {
												title := pg.Theme.Label(textSize16, values.String(values.StrLoadingPrice))
												title.Color = grayText
												return title.Layout(gtx)
											}
											return components.LayoutBalanceWithUnitSizeBoldText(gtx, pg.Load, pg.ticketPrice, textSize16)
										})
									}),
								)
							})
						}),
						layout.Rigid(layout.Spacer{Height: values.MarginPadding12}.Layout),
						layout.Rigid(func(gtx C) D {
							live := fmt.Sprintf("%d", pg.ticketOverview.Live)
							return pg.dataRows(gtx, values.String(values.StrLiveTickets), live, flexAxis, alignment)
						}),
					)
				}

				rightWg := func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							secs, _ := pg.dcrWallet.NextTicketPriceRemaining()
							timeleft := nextTicketRemaining(int(secs))
							return pg.dataRows(gtx, values.String(values.StrTimeLeft), timeleft, flexAxis, alignment)
						}),
						layout.Rigid(layout.Spacer{Height: values.MarginPadding12}.Layout),
						layout.Rigid(func(gtx C) D {
							canBuy := fmt.Sprintf("%d", ticketsCanBuy)
							return pg.dataRows(gtx, values.String(values.StrCanBuy), canBuy, flexAxis, alignment)
						}),
					)
				}

				return layout.Flex{Alignment: layout.Middle, Spacing: layout.SpaceBetween}.Layout(gtx,
					layout.Rigid(leftWg),
					layout.Rigid(rightWg),
				)
			}),
			layout.Rigid(pg.balanceProgressBarLayout),
			layout.Rigid(pg.buyTicketsButtonLayout),
		)
	})
}

// buyTicketsButtonLayout renders the one-time "Купити тикети" action — a manual
// purchase of a user-chosen number of tickets, distinct from the auto-buyer
// toggle. Hidden for watching-only wallets (can't sign) and disabled until the
// wallet is synced and the VSP API is allowed.
func (pg *Page) buyTicketsButtonLayout(gtx C) D {
	if pg.dcrWallet.IsWatchingOnlyWallet() {
		return D{}
	}
	ready := pg.dcrWallet.IsSynced() && !pg.dcrWallet.IsRescanning() &&
		pg.isTicketsPurchaseAllowed() && !pg.purchasing.Load()
	pg.buyTicketBtn.SetEnabled(ready)
	if pg.purchasing.Load() {
		pg.buyTicketBtn.Text = values.String(values.StrPurchasingTickets)
	} else {
		pg.buyTicketBtn.Text = values.String(values.StrBuyTickets)
	}
	return layout.Inset{Top: values.MarginPadding16}.Layout(gtx, pg.buyTicketBtn.Layout)
}

// stakeStatusLayout shows a plain-language banner explaining what auto-buy is
// doing right now — directly answering "what amount is staked, what's
// happening, and is the balance enough". States: off (not set up) /
// configured-but-paused-after-restart (the "toggle dropped on restart" case) /
// active with reserve + tickets affordable now / active but underfunded.
func (pg *Page) stakeStatusLayout(gtx C, ticketsCanBuy int) D {
	configured := pg.dcrWallet.TicketBuyerConfigIsSet()
	active := pg.dcrWallet.IsAutoTicketsPurchaseActive()

	var msg string
	switch {
	case active && ticketsCanBuy >= 1:
		reserve := pg.dcrWallet.ToAmount(pg.dcrWallet.AutoTicketsBuyerConfig().BalanceToMaintain).String()
		msg = values.StringF(values.StrStakeStatusActive, reserve, ticketsCanBuy)
	case active:
		msg = values.StringF(values.StrStakeStatusLowBalance, pg.ticketPrice)
	case configured:
		reserve := pg.dcrWallet.ToAmount(pg.dcrWallet.AutoTicketsBuyerConfig().BalanceToMaintain).String()
		msg = values.StringF(values.StrStakeStatusPaused, reserve)
	default:
		msg = values.String(values.StrStakeStatusOff)
	}

	textSize14 := values.TextSizeTransform(pg.IsMobileView(), values.TextSize14)
	lbl := pg.Theme.Label(textSize14, msg)
	lbl.Color = pg.Theme.Color.GrayText1
	return cryptomaterial.LinearLayout{
		Width:      cryptomaterial.MatchParent,
		Height:     cryptomaterial.WrapContent,
		Background: pg.Theme.Color.LightBlue,
		Border:     cryptomaterial.Border{Radius: cryptomaterial.Radius(8)},
		Padding:    layout.UniformInset(values.MarginPadding12),
		Margin:     layout.Inset{Bottom: values.MarginPadding16},
	}.Layout2(gtx, lbl.Layout)
}

func (pg *Page) dataRows(gtx C, title1, value1 string, axis layout.Axis, alignment layout.Alignment) D {
	textSize16 := values.TextSizeTransform(pg.IsMobileView(), values.TextSize16)
	return components.VerticalInset(values.MarginPadding6).Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: axis, Alignment: alignment}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				label := pg.Theme.Label(textSize16, title1)
				label.Color = pg.Theme.Color.GrayText2
				return label.Layout(gtx)
			}),
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Left: values.MarginPadding4}.Layout(gtx, func(gtx C) D {
					label := pg.Theme.Label(textSize16, value1)
					label.Color = pg.Theme.Color.Text
					label.Font.Weight = font.SemiBold
					return label.Layout(gtx)
				})
			}),
		)
	})
}

func (pg *Page) CalculateTotalTicketsCanBuy() int {
	if !pg.dcrWallet.Synced() {
		return 0
	}

	totalBalance, err := components.CalculateMixedAccountBalance(pg.dcrWallet)
	if err != nil {
		log.Debugf("missing set mixed account error: %v", err)
		return 0
	}

	ticketPrice, err := pg.dcrWallet.TicketPrice()
	if err != nil {
		log.Errorf("ticketPrice error: %v", err)
		return 0
	}
	canBuy := totalBalance.Spendable.ToCoin() / dcrutil.Amount(ticketPrice.TicketPrice).ToCoin()
	if canBuy < 0 {
		canBuy = 0
	}

	return int(canBuy)
}

func (pg *Page) balanceProgressBarLayout(gtx C) D {
	totalBalance, err := components.CalculateMixedAccountBalance(pg.dcrWallet)
	if err != nil {
		return D{}
	}
	textSize16 := values.TextSizeTransform(pg.IsMobileView(), values.TextSize16)

	items := []cryptomaterial.ProgressBarItem{
		{
			Value: totalBalance.LockedByTickets.ToCoin(),
			Color: pg.Theme.Color.NavyBlue,
		},
		{
			Value: totalBalance.Spendable.ToCoin(),
			Color: pg.Theme.Color.Turquoise300,
		},
	}

	labelWdg := func(gtx C) D {
		return layout.Flex{}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				text := values.String(values.StrStaked) + ": " + totalBalance.LockedByTickets.String()
				return components.LayoutIconAndTextWithSize(pg.Load, gtx, text, items[0].Color, textSize16, values.MarginPadding10)
			}),
			layout.Rigid(func(gtx C) D {
				text := values.String(values.StrLabelSpendable) + ": " + totalBalance.Spendable.String()
				return components.LayoutIconAndTextWithSize(pg.Load, gtx, text, items[1].Color, textSize16, values.MarginPadding10)
			}),
		)
	}
	total := totalBalance.Spendable.ToInt() + totalBalance.LockedByTickets.ToInt()
	pb := pg.Theme.MultiLayerProgressBar(pg.dcrWallet.ToAmount(total).ToCoin(), items)
	pb.ShowOtherWidgetFirst = true
	pb.Height = values.MarginPadding16
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return components.VerticalInset(values.MarginPadding16).Layout(gtx, pg.Theme.Separator().Layout)
		}),
		layout.Rigid(func(gtx C) D {
			return pb.Layout(gtx, pg.IsMobileView(), labelWdg)
		}),
	)
}

func (pg *Page) stakingRecordStatistics(gtx C) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(pg.stakingRecord(pg.totalRewards, fmt.Sprintf("%s %s", values.String(values.StrTotal), values.String(values.StrReward)))),
		layout.Rigid(pg.stakingRecord(fmt.Sprintf("%d", pg.ticketOverview.Voted), values.String(values.StrVoted))),
		layout.Rigid(pg.stakingRecord(fmt.Sprintf("%d", pg.ticketOverview.Revoked), values.String(values.StrRevoked))),
		layout.Rigid(pg.stakingRecord(fmt.Sprintf("%d", pg.ticketOverview.Immature), values.String(values.StrImmature))),
		layout.Rigid(pg.stakingRecord(fmt.Sprintf("%d", pg.ticketOverview.Unmined), values.String(values.StrUmined))),
		layout.Rigid(pg.stakingRecord(fmt.Sprintf("%d", pg.ticketOverview.Expired), values.String(values.StrExpired))),
	)
}

func (pg *Page) stakingRecord(count, status string) layout.Widget {
	return func(gtx C) D {
		return components.EndToEndRow(gtx,
			pg.Theme.Label(values.TextSize14, status).Layout,
			pg.Theme.Label(values.TextSize14, count).Layout,
		)
	}
}
