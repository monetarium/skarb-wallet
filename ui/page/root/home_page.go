package root

import (
	"gioui.org/layout"
	"gioui.org/text"

	"github.com/monetarium/monetarium-cryptopower/app"
	"github.com/monetarium/monetarium-cryptopower/ui/cryptomaterial"
	"github.com/monetarium/monetarium-cryptopower/ui/load"
)

// HomePageID is the unique ID of the home page.
const HomePageID = "Home"

// HomePage is the placeholder Phase-1 implementation of the wallet home page.
//
// The original Cryptopower HomePage wired the navigation tab to Decred-specific
// pages (DCRDEX, governance, exchange, instantswap, staking, mixer) which were
// removed for the Monetarium fork. The full home shell will be rebuilt in
// Phase 2 once the multi-coin (VAR + SKAn) UI is wired through libwallet.
//
// For now this stub satisfies the app.Page interface so the rest of the app
// (start_page, migrate_page) can compile and navigate here.
type HomePage struct {
	*app.MasterPage
	*load.Load
}

// NewHomePage returns the placeholder home page.
func NewHomePage(l *load.Load) *HomePage {
	return &HomePage{
		MasterPage: app.NewMasterPage(HomePageID),
		Load:       l,
	}
}

// ID is a unique string that identifies the page.
func (hp *HomePage) ID() string { return HomePageID }

// OnNavigatedTo is called when the page is about to be displayed.
func (hp *HomePage) OnNavigatedTo() {}

// OnNavigatedFrom is called when the page is removed from the display.
func (hp *HomePage) OnNavigatedFrom() {}

// HandleUserInteractions is called just before Layout to process input events.
func (hp *HomePage) HandleUserInteractions(_ layout.Context) {}

// Layout draws a placeholder banner.
func (hp *HomePage) Layout(gtx layout.Context) layout.Dimensions {
	theme := hp.Theme
	title := theme.H4("Monetarium wallet — work in progress")
	title.Alignment = text.Middle
	subtitle := theme.Body1("The desktop home screen is being rebuilt for the multi-coin (VAR + SKAn) Monetarium model.")
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
