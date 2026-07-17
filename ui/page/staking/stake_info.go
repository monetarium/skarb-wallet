package staking

import (
	"fmt"
	"strings"

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

		// Cached — recomputed by loadPageData on every block/tx. The old
		// per-frame CalculateTotalTicketsCanBuy call opened a bbolt read
		// (TicketPrice) plus a full GetAccountsRaw on EVERY redraw.
		ticketsCanBuy := pg.ticketsCanBuy

		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(pg.pageHead),
			layout.Rigid(func(gtx C) D {
				return pg.stakeStatusLayout(gtx, ticketsCanBuy)
			}),
			// Row 1: Total Reward across the full width, every visible
			// coin's amount inline on ONE line (dataRows carries its own
			// vertical inset).
			layout.Rigid(func(gtx C) D {
				return pg.dataRows(gtx, values.String(values.StrTotalReward),
					strings.Join(pg.rewardRows, "  ·  "), flexAxis, alignment)
			}),
			layout.Rigid(layout.Spacer{Height: values.MarginPadding12}.Layout),
			// Row 2: Ticket Price (left) and Time Left (right).
			layout.Rigid(func(gtx C) D {
				priceWg := func(gtx C) D {
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
				}

				timeLeftWg := func(gtx C) D {
					secs, _ := pg.dcrWallet.NextTicketPriceRemaining()
					timeleft := nextTicketRemaining(int(secs))
					return pg.dataRows(gtx, values.String(values.StrTimeLeft), timeleft, flexAxis, alignment)
				}

				return layout.Flex{Alignment: layout.Middle, Spacing: layout.SpaceBetween}.Layout(gtx,
					layout.Rigid(priceWg),
					layout.Rigid(timeLeftWg),
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
	// tbConfigured/tbIntent/tbReserve are cached snapshots (refreshed by
	// loadPageData and on toggle/settings changes) — the old per-frame
	// TicketBuyerConfigIsSet + AutoTicketsBuyerConfig calls were three DB
	// reads per redraw. The active flag stays live: it's an in-memory mutex
	// check.
	configured := pg.tbConfigured
	active := pg.dcrWallet.IsAutoTicketsPurchaseActive()

	var msg string
	switch {
	case active && ticketsCanBuy >= 1:
		msg = values.StringF(values.StrStakeStatusActive, pg.tbReserve)
	case active:
		msg = values.StringF(values.StrStakeStatusLowBalance, pg.ticketPrice)
	case configured && pg.tbIntent:
		// Intent still true = the buyer died WITH the process (restart),
		// the user never turned it off.
		msg = values.StringF(values.StrStakeStatusPaused, pg.tbReserve)
	case configured:
		msg = values.StringF(values.StrStakeStatusDisabled, pg.tbReserve)
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
	// Cached snapshot (refreshed by loadPageData) — the old per-frame
	// CalculateMixedAccountBalance re-read every account on every redraw.
	// nil until the first load lands or on a load error: bar hidden, same
	// as the old per-frame error path.
	totalBalance := pg.stakeBalance
	if totalBalance == nil {
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
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				text := values.String(values.StrStaked) + ": " + totalBalance.LockedByTickets.String()
				return components.LayoutIconAndTextWithSize(pg.Load, gtx, text, items[0].Color, textSize16, values.MarginPadding10)
			}),
			layout.Rigid(func(gtx C) D {
				text := values.String(values.StrLabelSpendable) + ": " + totalBalance.Spendable.String()
				return components.LayoutIconAndTextWithSize(pg.Load, gtx, text, items[1].Color, textSize16, values.MarginPadding10)
			}),
			// "Tickets You Can Buy" lives in this row, pushed to the right
			// edge — it is derived from the same balances the bar shows.
			layout.Flexed(1, func(gtx C) D {
				return layout.E.Layout(gtx, func(gtx C) D {
					label := pg.Theme.Label(textSize16, values.String(values.StrCanBuy)+": ")
					label.Color = pg.Theme.Color.GrayText2
					value := pg.Theme.Label(textSize16, fmt.Sprintf("%d", pg.ticketsCanBuy))
					value.Color = pg.Theme.Color.Text
					value.Font.Weight = font.SemiBold
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(label.Layout),
						layout.Rigid(value.Layout),
					)
				})
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
		layout.Rigid(pg.stakingRecord(strings.Join(pg.rewardRows, "\n"), fmt.Sprintf("%s %s", values.String(values.StrTotal), values.String(values.StrReward)))),
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
