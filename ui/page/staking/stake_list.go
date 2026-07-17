package staking

import (
	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/text"

	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/ui/page/components"
	"github.com/monetarium/skarb-wallet/ui/values"
)

func (pg *Page) initTicketList() {
	pg.ticketsList = pg.Theme.NewClickableList(layout.Vertical)
}

func (pg *Page) listenForTxNotifications() {
	// Idempotent: the page re-registers when navigation returns to it
	// (OnNavigatedFrom removed the listener) — drop any previous
	// registration under the same ID first so re-adding never errors.
	pg.stopTxNotificationsListener()
	// A bare Reload() only redrew the page with its STORED fields — the
	// overview counts, total rewards, and ticket list stayed stale until the
	// user left and re-entered. Flag a refresh instead; the UI thread drains
	// it in HandleUserInteractions and re-runs the actual data loads
	// (CLAUDE.md §3 — never touch Layout-read state from this goroutine).
	txAndBlockNotificationListener := &sharedW.TxAndBlockNotificationListener{
		OnTransaction: func(_ int, _ *sharedW.Transaction) {
			pg.pendingRefresh.Store(true)
			pg.ParentWindow().Reload()
		},
		OnBlockAttached: func(_ int, _ int32) {
			pg.pendingRefresh.Store(true)
			pg.ParentWindow().Reload()
		},
	}
	err := pg.dcrWallet.AddTxAndBlockNotificationListener(txAndBlockNotificationListener, OverviewPageID)
	if err != nil {
		log.Errorf("Error adding tx and block notification listener: %v", err)
		return
	}
}

func (pg *Page) stopTxNotificationsListener() {
	pg.dcrWallet.RemoveTxAndBlockNotificationListener(OverviewPageID)
}

func (pg *Page) fetchTickets(offset, pageSize int32) ([]*transactionItem, int, bool, error) {
	// A Statistics tile narrows the list to that ticket status; without a
	// tile selection the full tickets list is shown.
	requested := pg.statFilter.Load()
	if requested == 0 {
		requested = dcr.TxFilterTickets
	}
	// Voted/Revoked name VOTE/REVOCATION transactions in the DB query,
	// but this list renders TICKETS. Querying them directly returns vote
	// rows that stakeToTransactionItems drops (nothing ever spends a
	// vote, so its TicketSpender is always nil) — the list came back
	// empty. Instead, walk ALL tickets and refine by the spender's type
	// (the hasFilter path built for exactly this). The whole refined set
	// is served in the first window: refined pages are almost always
	// shorter than pageSize, which the scroll would misread as
	// end-of-list, and its offset arithmetic indexes the UNrefined
	// tickets — per-page refinement both truncated and duplicated rows.
	if requested == dcr.TxFilterVoted || requested == dcr.TxFilterRevoked {
		if offset > 0 {
			return nil, 0, false, nil // everything was served at offset 0
		}
		hasFilter := func(f int32) bool { return f == requested }
		var all []*transactionItem
		const chunk = int32(100)
		for dbOffset := int32(0); ; dbOffset += chunk {
			txs, err := pg.dcrWallet.GetTransactionsRaw(dbOffset, chunk, dcr.TxFilterTickets, true, "")
			if err != nil {
				return nil, -1, false, err
			}
			items, err := pg.stakeToTransactionItems(txs, true, hasFilter)
			if err != nil {
				return nil, -1, false, err
			}
			all = append(all, items...)
			if int32(len(txs)) < chunk {
				break
			}
		}
		return all, len(all), false, nil
	}

	txs, err := pg.dcrWallet.GetTransactionsRaw(offset, pageSize, requested, true, "")
	if err != nil {
		return nil, -1, false, err
	}

	tickets, err := pg.stakeToTransactionItems(txs, true, func(f int32) bool {
		return f == requested
	})
	return tickets, len(tickets), false, err
}

func (pg *Page) ticketListLayout(gtx C) D {
	if pg.showMaterialLoader.Load() {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Center.Layout(gtx, pg.materialLoader.Layout)
	}
	isMobile := pg.IsMobileView()
	margin24 := values.MarginPaddingTransform(pg.IsMobileView(), values.MarginPadding24)
	textSize16 := values.TextSizeTransform(isMobile, values.TextSize16)
	return pg.Theme.Card().Layout(gtx, func(gtx C) D {
		return pg.scroll.List().Layout(gtx, 1, func(gtx C, _ int) D {
			gtx.Constraints.Max.Y = ticketHeight
			return layout.Inset{
				Bottom: values.MarginPadding8,
			}.Layout(gtx, func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.UniformInset(margin24).Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							txt := pg.Theme.Label(values.TextSizeTransform(isMobile, values.TextSize20), values.String(values.StrTickets))
							txt.Font.Weight = font.SemiBold
							return txt.Layout(gtx)
						}),
						layout.Rigid(layout.Spacer{Height: margin24}.Layout),
						layout.Rigid(func(gtx C) D {
							if pg.scroll.ItemsCount() <= 0 {
								gtx.Constraints.Min.X = gtx.Constraints.Max.X

								txt := pg.Theme.Body1(values.String(values.StrNoTickets))
								txt.Color = pg.Theme.Color.GrayText3
								txt.TextSize = textSize16
								txt.Alignment = text.Middle
								return layout.Inset{Top: values.MarginPadding15, Bottom: values.MarginPadding16}.Layout(gtx, txt.Layout)
							}

							tickets := pg.scroll.FetchedData()
							return pg.ticketsList.Layout(gtx, len(tickets), func(gtx C, index int) D {
								ticket := tickets[index]
								return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
									// gray separator line
									layout.Rigid(func(gtx C) D {
										if index == 0 {
											return D{}
										}
										gtx.Constraints.Min.X = gtx.Constraints.Max.X
										separator := pg.Theme.Separator()
										separator.Width = gtx.Constraints.Max.X
										return layout.Inset{
											Bottom: values.MarginPadding5,
											Left:   values.MarginPadding40,
										}.Layout(gtx, func(gtx C) D {
											return layout.E.Layout(gtx, separator.Layout)
										})
									}),
									layout.Rigid(func(gtx C) D {
										return layout.Inset{
											Bottom: values.MarginPadding5,
										}.Layout(gtx, func(gtx C) D {
											return components.LayoutTransactionRow(gtx, pg.Load, pg.dcrWallet, ticket.transaction, true)
										})
									}),
								)
							})
						}),
					)
				})
			})
		})
	})

}
