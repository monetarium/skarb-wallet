// Phase-2 HomePage — minimal real shell for the Monetarium wallet.
//
// Layout: a left-hand wallet list (one entry per loaded Monetarium wallet)
// plus a right-hand subpage area. The first time the page is shown it pushes
// the OverviewPage as the right-hand subpage; clicking a wallet entry replaces
// the subpage with the per-wallet SingleWalletMasterPage so existing pages
// (Send / Receive / Transactions / Settings) keep working unchanged.
//
// The original Cryptopower HomePage also wired Exchange / Governance / Staking
// tabs and a top "create wallet" CTA — out of scope for the v1 fork.
package root

import (
	"fmt"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/text"
	"gioui.org/unit"

	"github.com/monetarium/monetarium-cryptopower/app"
	sharedW "github.com/monetarium/monetarium-cryptopower/libwallet/assets/wallet"
	"github.com/monetarium/monetarium-cryptopower/ui/cryptomaterial"
	"github.com/monetarium/monetarium-cryptopower/ui/load"
	walletpage "github.com/monetarium/monetarium-cryptopower/ui/page/wallet"
)

// HomePageID is the unique ID of the home page.
const HomePageID = "Home"

// HomePage is the root container while a session is active. It owns the
// per-wallet sidebar and delegates body rendering to its current subpage.
type HomePage struct {
	*app.MasterPage
	*load.Load

	// rendered list of wallets in the sidebar, refreshed on each Layout call.
	walletEntries []walletEntry
	walletsList   layout.List

	overviewBtn cryptomaterial.Button
}

type walletEntry struct {
	wallet sharedW.Asset
	click  *cryptomaterial.Clickable
}

// NewHomePage returns a fresh home page.
func NewHomePage(l *load.Load) *HomePage {
	return &HomePage{
		MasterPage:  app.NewMasterPage(HomePageID),
		Load:        l,
		walletsList: layout.List{Axis: layout.Vertical},
		overviewBtn: l.Theme.Button("Overview"),
	}
}

// ID returns the page ID.
func (hp *HomePage) ID() string { return HomePageID }

// OnNavigatedTo seeds the sidebar from AssetsManager and pushes Overview.
func (hp *HomePage) OnNavigatedTo() {
	hp.refreshWalletList()
	if hp.Load.ToggleSync == nil {
		hp.Load.ToggleSync = hp.toggleWalletSync
	}
	if hp.CurrentPage() == nil {
		hp.Display(NewOverviewPage(hp.Load, func() {}))
	}
}

// toggleWalletSync is the v1 Monetarium ToggleSync implementation. It runs SPV
// directly: no internet-connectivity polling, no low-storage modal. The
// unlock(b) callback flips a UI hint to "syncing"; SPV requires the wallet to
// either be a watch-only wallet, already discovered, or unlocked beforehand.
//
// The full Cryptopower flow (see .phase1-stubs-replaced/home_page.go.orig) had
// password prompts, low-storage warnings, and connectivity polling — restore
// those once HomePage owns proper modal scaffolding.
func (hp *HomePage) toggleWalletSync(wallet sharedW.Asset, unlock load.NeedUnlockRestore) {
	if wallet == nil {
		return
	}
	if wallet.IsConnectedToNetwork() {
		wallet.EnableSyncShuttingDown()
		go wallet.CancelSync()
		if unlock != nil {
			unlock(false)
		}
		return
	}
	if !wallet.ContainsDiscoveredAccounts() && wallet.IsLocked() && !wallet.IsWatchingOnlyWallet() {
		log.Warn("Wallet is locked — unlock it before starting sync (password modal not yet wired in v1).")
		if unlock != nil {
			unlock(false)
		}
		return
	}
	if unlock != nil {
		unlock(true)
	}
	go func() {
		if err := wallet.SpvSync(); err != nil {
			log.Errorf("SpvSync(%s): %v", wallet.GetWalletName(), err)
		}
	}()
}

// OnNavigatedFrom is called when the page is removed from the display.
func (hp *HomePage) OnNavigatedFrom() {
	if cur := hp.CurrentPage(); cur != nil {
		cur.OnNavigatedFrom()
	}
}

// HandleUserInteractions wires sidebar clicks to subpage transitions.
func (hp *HomePage) HandleUserInteractions(gtx layout.Context) {
	if hp.overviewBtn.Clicked(gtx) {
		hp.Display(NewOverviewPage(hp.Load, func() {}))
	}
	for _, entry := range hp.walletEntries {
		if entry.click != nil && entry.click.Clicked(gtx) {
			hp.Display(walletpage.NewSingleWalletMasterPage(hp.Load, entry.wallet, func() {}))
		}
	}
	if cur := hp.CurrentPage(); cur != nil {
		cur.HandleUserInteractions(gtx)
	}
}

// Layout draws sidebar + subpage.
func (hp *HomePage) Layout(gtx layout.Context) layout.Dimensions {
	hp.refreshWalletList()
	return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
		layout.Rigid(hp.layoutSidebar),
		layout.Flexed(1, hp.layoutBody),
	)
}

func (hp *HomePage) layoutSidebar(gtx layout.Context) layout.Dimensions {
	const sidebarWidth = 220
	gtx.Constraints.Min.X = gtx.Dp(unit.Dp(sidebarWidth))
	gtx.Constraints.Max.X = gtx.Constraints.Min.X

	return cryptomaterial.LinearLayout{
		Width:       cryptomaterial.MatchParent,
		Height:      cryptomaterial.MatchParent,
		Background:  hp.Theme.Color.Surface,
		Padding:     layout.UniformInset(unit.Dp(12)),
		Orientation: layout.Vertical,
	}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			t := hp.Theme.H6("Monetarium")
			t.Font.Weight = font.Bold
			return t.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			c := hp.Theme.Caption(fmt.Sprintf("%s · %d wallet(s)",
				hp.AssetsManager.NetType(),
				hp.AssetsManager.LoadedWalletsCount()))
			return c.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
		layout.Rigid(hp.overviewBtn.Layout),
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			label := hp.Theme.Caption("WALLETS")
			label.Color = hp.Theme.Color.GrayText2
			return label.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			if len(hp.walletEntries) == 0 {
				empty := hp.Theme.Body2("No wallets yet")
				empty.Alignment = text.Start
				empty.Color = hp.Theme.Color.GrayText3
				return empty.Layout(gtx)
			}
			return hp.walletsList.Layout(gtx, len(hp.walletEntries), func(gtx layout.Context, i int) layout.Dimensions {
				return hp.layoutWalletEntry(gtx, &hp.walletEntries[i])
			})
		}),
	)
}

func (hp *HomePage) layoutWalletEntry(gtx layout.Context, e *walletEntry) layout.Dimensions {
	return cryptomaterial.LinearLayout{
		Width:       cryptomaterial.MatchParent,
		Height:      cryptomaterial.WrapContent,
		Padding:     layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(4), Right: unit.Dp(4)},
		Margin:      layout.Inset{Bottom: unit.Dp(2)},
		Clickable:   e.click,
		Orientation: layout.Vertical,
	}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := hp.Theme.Body1(e.wallet.GetWalletName())
			return lbl.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			meta := hp.Theme.Caption(fmt.Sprintf("ID #%d", e.wallet.GetWalletID()))
			meta.Color = hp.Theme.Color.GrayText3
			return meta.Layout(gtx)
		}),
	)
}

func (hp *HomePage) layoutBody(gtx layout.Context) layout.Dimensions {
	current := hp.CurrentPage()
	if current == nil {
		return cryptomaterial.LinearLayout{
			Width:     cryptomaterial.MatchParent,
			Height:    cryptomaterial.MatchParent,
			Direction: layout.Center,
		}.Layout(gtx, layout.Rigid(hp.Theme.Body1("Loading…").Layout))
	}
	return current.Layout(gtx)
}

// refreshWalletList rebuilds the sidebar entry list from AssetsManager,
// preserving click state for wallets that are still present. New wallets get
// a freshly initialised Clickable; gone wallets are dropped.
func (hp *HomePage) refreshWalletList() {
	wallets := hp.AssetsManager.AllWallets()

	existing := make(map[int]*cryptomaterial.Clickable, len(hp.walletEntries))
	for _, entry := range hp.walletEntries {
		existing[entry.wallet.GetWalletID()] = entry.click
	}

	out := make([]walletEntry, 0, len(wallets))
	for _, w := range wallets {
		entry := walletEntry{wallet: w}
		if prev, ok := existing[w.GetWalletID()]; ok && prev != nil {
			entry.click = prev
		} else {
			entry.click = hp.Theme.NewClickable(true)
		}
		out = append(out, entry)
	}
	hp.walletEntries = out
}
