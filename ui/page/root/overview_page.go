package root

import (
	"gioui.org/layout"
	"gioui.org/text"

	"github.com/monetarium/monetarium-cryptopower/app"
	"github.com/monetarium/monetarium-cryptopower/ui/cryptomaterial"
	"github.com/monetarium/monetarium-cryptopower/ui/load"
)

// OverviewPageID is the unique ID of the overview page.
const OverviewPageID = "Overview"

// OverviewPage is the placeholder Phase-1 implementation of the wallet
// overview / market-rate page.
//
// The original Cryptopower OverviewPage rendered FX rate cards, instantswap
// orders, and a per-asset balance table — all of which depended on packages
// that were removed for the Monetarium fork (libwallet/ext, libwallet/instantswap).
// The full multi-coin overview will be rebuilt in Phase 2.
type OverviewPage struct {
	*app.MasterPage
	*load.Load

	showNavigationFunc func()
}

// NewOverviewPage returns the placeholder overview page.
func NewOverviewPage(l *load.Load, showNavigationFunc func()) *OverviewPage {
	return &OverviewPage{
		MasterPage:         app.NewMasterPage(OverviewPageID),
		Load:               l,
		showNavigationFunc: showNavigationFunc,
	}
}

// ID is a unique string that identifies the page.
func (op *OverviewPage) ID() string { return OverviewPageID }

// OnNavigatedTo is called when the page is about to be displayed.
func (op *OverviewPage) OnNavigatedTo() {}

// OnNavigatedFrom is called when the page is removed from the display.
func (op *OverviewPage) OnNavigatedFrom() {}

// HandleUserInteractions is called just before Layout to process input events.
func (op *OverviewPage) HandleUserInteractions(_ layout.Context) {}

// ListenForNewTx is a no-op stub. The original implementation refreshed the
// market table and balance cards on new transactions; reinstate when the
// multi-coin overview is built.
func (op *OverviewPage) ListenForNewTx() {}

// Layout draws a placeholder banner.
func (op *OverviewPage) Layout(gtx layout.Context) layout.Dimensions {
	theme := op.Theme
	title := theme.H4("Overview — coming soon")
	title.Alignment = text.Middle
	subtitle := theme.Body1("Per-asset balance and rate cards will be rebuilt for VAR + SKAn in Phase 2.")
	subtitle.Alignment = text.Middle

	return cryptomaterial.LinearLayout{
		Width:       cryptomaterial.MatchParent,
		Height:      cryptomaterial.MatchParent,
		Orientation: layout.Vertical,
		Alignment:   layout.Middle,
		Direction:   layout.Center,
	}.Layout(gtx,
		layout.Rigid(title.Layout),
		layout.Rigid(subtitle.Layout),
	)
}
