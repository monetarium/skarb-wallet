package staking

import (
	"fmt"
	"image/color"

	"gioui.org/font"
	"gioui.org/layout"
	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/values"
)

type statisticsItem struct {
	Icon        *cryptomaterial.Image
	Title       string
	ValueText   string
	ValueWidget func(gtx C) D
	// Filter is the dcr.TxFilter* this tile narrows the ticket list to when
	// clicked (0 = tile is not clickable).
	Filter int32
}

func (pg *Page) stakeStatisticsSection(gtx C) D {
	isMobile := pg.IsMobileView()
	// Swapped with Total Reward (now in the price section): this tile shows
	// the live-ticket count, keeping the statistics grid all-numeric.
	// Every tile carries its ticket-status filter — clicking narrows the
	// Tickets list below to that status (see HandleUserInteractions).
	liveItem := &statisticsItem{Icon: pg.Theme.Icons.TicketLiveIcon, Title: values.String(values.StrLiveTickets), ValueText: fmt.Sprintf("%d", pg.ticketOverview.Live), Filter: dcr.TxFilterLive}
	revokedItem := &statisticsItem{Icon: pg.Theme.Icons.TicketRevokedIcon, Title: values.String(values.StrRevoke), ValueText: fmt.Sprintf("%d", pg.ticketOverview.Revoked), Filter: dcr.TxFilterRevoked}
	uminedItem := &statisticsItem{Icon: pg.Theme.Icons.TicketUnminedIcon, Title: values.String(values.StrUmined), ValueText: fmt.Sprintf("%d", pg.ticketOverview.Unmined), Filter: dcr.TxFilterUnmined}
	votedItem := &statisticsItem{Icon: pg.Theme.Icons.TicketVotedIcon, Title: values.String(values.StrVoted), ValueText: fmt.Sprintf("%d", pg.ticketOverview.Voted), Filter: dcr.TxFilterVoted}
	immatureItem := &statisticsItem{Icon: pg.Theme.Icons.TicketImmatureIcon, Title: values.String(values.StrImmature), ValueText: fmt.Sprintf("%d", pg.ticketOverview.Immature), Filter: dcr.TxFilterImmature}
	expiredItem := &statisticsItem{Icon: pg.Theme.Icons.TicketExpiredIcon, Title: values.String(values.StrExpired), ValueText: fmt.Sprintf("%d", pg.ticketOverview.Expired), Filter: dcr.TxFilterExpired}

	return pg.pageSections(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				txt := pg.Theme.Label(values.TextSizeTransform(isMobile, values.TextSize20), values.String(values.StrStatistics))
				txt.Font.Weight = font.SemiBold
				return layout.Inset{
					Bottom: values.MarginPaddingTransform(isMobile, values.MarginPadding24),
				}.Layout(gtx, txt.Layout)
			}),
			layout.Rigid(func(gtx C) D {
				// Equal-width cells so icons and labels line up as a table
				// (GridWrap packed each cell to its text width, so columns
				// drifted). Desktop: 2×3. Phone: 3×2 — the old 3-column
				// phone grid squeezed "Live Tickets" letter-by-letter.
				var rows [][]*statisticsItem
				if isMobile {
					rows = [][]*statisticsItem{
						{liveItem, votedItem},
						{revokedItem, immatureItem},
						{uminedItem, expiredItem},
					}
				} else {
					rows = [][]*statisticsItem{
						{liveItem, uminedItem, immatureItem},
						{revokedItem, votedItem, expiredItem},
					}
				}
				rowGap := values.MarginPaddingTransform(isMobile, values.MarginPadding24)
				children := make([]layout.FlexChild, 0, len(rows)*2-1)
				for i, row := range rows {
					if i > 0 {
						children = append(children, layout.Rigid(layout.Spacer{Height: rowGap}.Layout))
					}
					row := row
					children = append(children, layout.Rigid(func(gtx C) D {
						return pg.statisticsRow(gtx, row)
					}))
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
			}),
		)
	})
}

func (pg *Page) statisticsRow(gtx C, items []*statisticsItem) D {
	children := make([]layout.FlexChild, 0, len(items))
	for _, item := range items {
		it := item
		children = append(children, layout.Flexed(1, func(gtx C) D {
			return pg.dataStatisticsItem(gtx, it)
		}))
	}
	return layout.Flex{Alignment: layout.Start}.Layout(gtx, children...)
}

func (pg *Page) dataStatisticsItem(gtx C, item *statisticsItem) D {
	content := func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return layout.Inset{
					Right: values.MarginPadding10,
				}.Layout(gtx, item.Icon.Layout36dp)
			}),
			layout.Flexed(1, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						label := pg.Theme.Label(values.TextSize16, item.Title)
						label.Color = pg.Theme.Color.GrayText2
						return label.Layout(gtx)
					}),
					layout.Rigid(func(gtx C) D {
						if item.ValueWidget != nil {
							return item.ValueWidget(gtx)
						}
						label := pg.Theme.Label(values.TextSize16, item.ValueText)
						label.Color = pg.Theme.Color.Text
						label.Font.Weight = font.SemiBold
						return label.Layout(gtx)
					}),
				)
			}),
		)
	}
	clk := pg.statTileClickables[item.Filter]
	if clk == nil {
		return content(gtx)
	}
	// Clickable tile: highlight while it's the active ticket-list filter.
	bg := color.NRGBA{}
	if pg.statFilter.Load() == item.Filter {
		bg = pg.Theme.Color.Gray4
	}
	return cryptomaterial.LinearLayout{
		Width:      cryptomaterial.MatchParent,
		Height:     cryptomaterial.WrapContent,
		Clickable:  clk,
		Background: bg,
		Border:     cryptomaterial.Border{Radius: cryptomaterial.Radius(8)},
		Padding:    layout.UniformInset(values.MarginPadding6),
	}.Layout2(gtx, content)
}
