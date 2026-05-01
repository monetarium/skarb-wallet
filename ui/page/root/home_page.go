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
	"strings"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/text"
	"gioui.org/unit"

	"github.com/monetarium/skarb-wallet/app"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/modal"
	"github.com/monetarium/skarb-wallet/ui/page/settings"
	walletpage "github.com/monetarium/skarb-wallet/ui/page/wallet"
	"github.com/monetarium/skarb-wallet/ui/values"
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
	settingsBtn cryptomaterial.Button
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
		overviewBtn: l.Theme.Button(values.String(values.StrOverview)),
		settingsBtn: l.Theme.Button(values.String(values.StrSettings)),
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
		hp.showOverview()
	}
}

// showOverview pushes the per-asset Overview page onto the body. Wired into
// the wallet-detail subpage as its "back" callback so the top-left arrow on
// each wallet sub-screen returns the user to the dashboard.
func (hp *HomePage) showOverview() {
	// Hand the OverviewPage a callback so clicking a wallet card jumps
	// straight into its detail page — same target as a sidebar entry click.
	hp.Display(NewOverviewPage(hp.Load, func() {}, hp.openWallet))
}

// openWallet swaps the body subpage to a per-wallet detail page. Shared
// between sidebar entries and Overview cards so both paths land on the same
// place.
func (hp *HomePage) openWallet(w sharedW.Asset) {
	hp.Display(walletpage.NewSingleWalletMasterPage(hp.Load, w, hp.showOverview))
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
		// Fresh wallet with a spending password and no discovered accounts.
		// SPV needs the wallet unlocked so it can derive addresses while
		// scanning. Prompt for the password, unlock, then start SPV.
		hp.promptUnlockAndSync(wallet, unlock)
		return
	}
	if unlock != nil {
		unlock(true)
	}
	hp.startSpv(wallet)
}

// promptUnlockAndSync shows a password modal, unlocks the wallet on success
// and kicks off SPV sync. Replaces the older silent log.Warn that left the
// "Start sync" button visibly inert for any wallet protected by a spending
// password.
func (hp *HomePage) promptUnlockAndSync(wallet sharedW.Asset, unlock load.NeedUnlockRestore) {
	if unlock != nil {
		unlock(false)
	}
	pwModal := modal.NewCreatePasswordModal(hp.Load).
		EnableName(false).
		EnableConfirmPassword(false).
		Title(values.String(values.StrUnlockWithPassword)).
		PasswordHint(values.String(values.StrSpendingPassword)).
		SetCancelable(true).
		SetNegativeButtonText(values.String(values.StrCancel)).
		SetPositiveButtonText(values.String(values.StrUnlock)).
		SetPositiveButtonCallback(func(_, password string, m *modal.CreatePasswordModal) bool {
			if err := wallet.UnlockWallet(password); err != nil {
				m.SetError(err.Error())
				return false
			}
			m.Dismiss()
			if unlock != nil {
				unlock(true)
			}
			hp.startSpv(wallet)
			return true
		})
	hp.ParentWindow().ShowModal(pwModal)
}

// startSpv launches SPV in the background and routes any error through the
// log. Centralised so both the locked-then-unlocked and the already-unlocked
// paths use identical lifecycle handling.
func (hp *HomePage) startSpv(wallet sharedW.Asset) {
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
		hp.showOverview()
	}
	if hp.settingsBtn.Clicked(gtx) {
		// AppSettingsPage hosts the network (mainnet ↔ testnet) switcher,
		// language, theme, and other process-wide knobs. It's a top-level
		// modal-style page rather than a wallet-scoped one.
		hp.ParentWindow().Display(settings.NewAppSettingsPage(hp.Load))
	}
	for _, entry := range hp.walletEntries {
		if entry.click != nil && entry.click.Clicked(gtx) {
			hp.openWallet(entry.wallet)
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
			t := hp.Theme.H6("Skarb")
			t.Font.Weight = font.Bold
			return t.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			c := hp.Theme.Caption(fmt.Sprintf("%s · %d %s",
				hp.AssetsManager.NetType(),
				hp.AssetsManager.LoadedWalletsCount(),
				values.String(values.StrWallets)))
			return c.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
		layout.Rigid(hp.overviewBtn.Layout),
		layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
		layout.Rigid(hp.settingsBtn.Layout),
		layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			net := hp.Theme.Caption(fmt.Sprintf("%s: %s",
				values.String(values.StrNetwork),
				hp.AssetsManager.NetType()))
			net.Color = hp.Theme.Color.GrayText3
			return net.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			label := hp.Theme.Caption(strings.ToUpper(values.String(values.StrWallets)))
			label.Color = hp.Theme.Color.GrayText2
			return label.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			if len(hp.walletEntries) == 0 {
				empty := hp.Theme.Body2(values.String(values.StrnoValidWalletFound))
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
		}.Layout(gtx, layout.Rigid(hp.Theme.Body1(values.String(values.StrLoading)).Layout))
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
