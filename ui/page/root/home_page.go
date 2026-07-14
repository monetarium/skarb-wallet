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
	"image/color"
	"strings"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/text"
	"gioui.org/unit"

	"github.com/monetarium/skarb-wallet/app"
	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/modal"
	"github.com/monetarium/skarb-wallet/ui/page/components"
	"github.com/monetarium/skarb-wallet/ui/page/seedbackup"
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

	// Nav items are hoverable Clickables (not Buttons): the cryptomaterial
	// Button's hover/ink uses Hovered(HighlightColor), and the only
	// transparent-friendly highlight (SurfaceHighlight) is unset (== zero), so
	// flat Buttons showed NO hover/press feedback. A LinearLayout+Clickable
	// uses ClickableStyle.HoverColor (Gray5) directly — the same visible hover
	// the wallet entries already use.
	overviewClick     *cryptomaterial.Clickable
	settingsClick     *cryptomaterial.Clickable
	createWalletClick *cryptomaterial.Clickable

	// selectedWalletID is the wallet whose detail page is currently shown in
	// the body, so its sidebar entry can be highlighted. -1 = none (Overview).
	selectedWalletID int
}

type walletEntry struct {
	wallet sharedW.Asset
	click  *cryptomaterial.Clickable
}

// NewHomePage returns a fresh home page.
func NewHomePage(l *load.Load) *HomePage {
	hp := &HomePage{
		MasterPage:        app.NewMasterPage(HomePageID),
		Load:              l,
		walletsList:       layout.List{Axis: layout.Vertical},
		overviewClick:     l.Theme.NewClickable(true),
		settingsClick:     l.Theme.NewClickable(true),
		createWalletClick: l.Theme.NewClickable(true),
		selectedWalletID:  -1,
	}

	return hp
}

// navRow renders a single flat sidebar navigation row: a hoverable Clickable
// (so it shows the standard Gray5 hover + press feedback) wrapping a label.
func (hp *HomePage) navRow(gtx layout.Context, click *cryptomaterial.Clickable, label string, textColor color.NRGBA) layout.Dimensions {
	return cryptomaterial.LinearLayout{
		Width:       cryptomaterial.MatchParent,
		Height:      cryptomaterial.WrapContent,
		Clickable:   click,
		Border:      cryptomaterial.Border{Radius: cryptomaterial.Radius(8)},
		Padding:     layout.Inset{Top: unit.Dp(9), Bottom: unit.Dp(9), Left: unit.Dp(10), Right: unit.Dp(10)},
		Orientation: layout.Vertical,
	}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := hp.Theme.Body1(label)
			lbl.Color = textColor
			lbl.Font.Weight = font.Medium
			return lbl.Layout(gtx)
		}),
	)
}

// ID returns the page ID.
func (hp *HomePage) ID() string { return HomePageID }

// OnNavigatedTo seeds the sidebar from AssetsManager and pushes Overview.
// Also kicks off SPV for any wallet that has the AutoSync flag turned on
// (set in start_page.go when the user opts into "recommended" setup) so the
// user lands on a syncing wallet rather than having to click "Start sync"
// every launch.
func (hp *HomePage) OnNavigatedTo() {
	hp.refreshWalletList()
	if hp.Load.ToggleSync == nil {
		hp.Load.ToggleSync = hp.toggleWalletSync
	}
	if hp.CurrentPage() == nil {
		hp.showOverview()
	}
	hp.maybeAutoSync()
}

// maybeAutoSync scans every loaded wallet and starts SPV in the background
// for those that asked for it via AutoSyncConfigKey. Locked wallets are
// skipped (we'd need a password modal — done on demand from the Start sync
// button instead).
func (hp *HomePage) maybeAutoSync() {
	for _, w := range hp.AssetsManager.AllWallets() {
		if !w.ReadBoolConfigValueForKey(sharedW.AutoSyncConfigKey, false) {
			continue
		}
		if w.IsConnectedToNetwork() {
			continue
		}
		if w.IsLocked() && !w.IsWatchingOnlyWallet() && !w.ContainsDiscoveredAccounts() {
			// Need an explicit unlock — skip silently rather than ambush the
			// user with a password modal during page entry. They'll see the
			// inactive sync indicator and can click Start sync to unlock.
			continue
		}
		hp.startSpv(w)
	}
}

// showOverview pushes the per-asset Overview page onto the body. Wired into
// the wallet-detail subpage as its "back" callback so the top-left arrow on
// each wallet sub-screen returns the user to the dashboard.
func (hp *HomePage) showOverview() {
	hp.selectedWalletID = -1
	// Hand the OverviewPage a callback so clicking a wallet card jumps
	// straight into its detail page — same target as a sidebar entry click.
	hp.Display(NewOverviewPage(hp.Load, func() {}, hp.openWallet))
}

// OpenWallet opens w's detail page (landing on its default Info tab) inside
// this HomePage. Exported for the start page: right after a wallet is
// created or restored the user must land on that wallet's Info page, not on
// the dashboard. Safe to call immediately after constructing the HomePage —
// OnNavigatedTo only pushes Overview when no subpage is set yet.
func (hp *HomePage) OpenWallet(w sharedW.Asset) {
	hp.openWallet(w)
}

// openWallet swaps the body subpage to a per-wallet detail page. Shared
// between sidebar entries and Overview cards so both paths land on the same
// place.
func (hp *HomePage) openWallet(w sharedW.Asset) {
	hp.selectedWalletID = w.GetWalletID()
	// PushAndNavigate silently drops a Display() whose new page shares the
	// current top's ID — and every SingleWalletMasterPage uses the same
	// MainPageID. So when one wallet's detail page is already showing, clicking
	// a different wallet (sidebar or Overview) was a no-op: the switch never
	// happened. Close the open wallet page first so the new wallet's page
	// actually replaces it.
	if hp.CurrentPageID() == walletpage.MainPageID {
		hp.CloseCurrentPage()
	}
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
	if hp.overviewClick.Clicked(gtx) {
		hp.showOverview()
	}
	if hp.settingsClick.Clicked(gtx) {
		// AppSettingsPage hosts the network (mainnet ↔ testnet) switcher,
		// language, theme, and other process-wide knobs. It's a top-level
		// modal-style page rather than a wallet-scoped one.
		hp.ParentWindow().Display(settings.NewAppSettingsPage(hp.Load))
	}
	if hp.createWalletClick.Clicked(gtx) {
		// Reuse the onboarding wallet-creation flow. (The wallet-selector page
		// that used to host "Add wallet" is unused in this fork, so without
		// this button there was no in-app way to create a second wallet after
		// onboarding.) The callback mirrors onboarding: mark the new wallet
		// for auto-sync, pop back home (the sidebar refreshes every Layout),
		// then force the seed-backup flow for a freshly created wallet —
		// restored/watch-only wallets skip it (seed already held / no seed).
		hp.ParentWindow().Display(components.NewCreateWallet(hp.Load, func(newWallet sharedW.Asset) {
			hp.ParentWindow().CloseCurrentPage()
			if newWallet == nil {
				return
			}
			newWallet.SaveUserConfigValue(sharedW.AutoSyncConfigKey, true)
			if newWallet.IsWatchingOnlyWallet() {
				return
			}
			if dcrW, ok := newWallet.(*dcr.Asset); ok && dcrW.IsRestored {
				return
			}
			currentID := hp.ParentWindow().CurrentPageID()
			hp.ParentWindow().Display(seedbackup.NewBackupInstructionsPage(hp.Load, newWallet,
				func(_ *load.Load, navigator app.WindowNavigator) {
					navigator.ClosePagesAfter(currentID)
				}))
		}))
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
		Padding:     layout.UniformInset(unit.Dp(16)),
		Orientation: layout.Vertical,
	}.Layout(gtx,
		// Brand + a single subtle network line (the old layout repeated the
		// network in two captions and stacked a wallet count up here too).
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			t := hp.Theme.H6("Skarb")
			t.Font.Weight = font.Bold
			return t.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(2)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			net := hp.Theme.Caption(string(hp.AssetsManager.NetType()))
			net.Color = hp.Theme.Color.GrayText3
			return net.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
		layout.Rigid(hp.Theme.Separator().Layout),
		layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),

		// Navigation.
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return hp.navRow(gtx, hp.overviewClick, values.String(values.StrOverview), hp.Theme.Color.GrayText1)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(2)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return hp.navRow(gtx, hp.settingsClick, values.String(values.StrSettings), hp.Theme.Color.GrayText1)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
		layout.Rigid(hp.Theme.Separator().Layout),
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),

		// Wallets section: header (label + count), create action, then the list.
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle, Spacing: layout.SpaceBetween}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					label := hp.Theme.Caption(strings.ToUpper(values.String(values.StrWallets)))
					label.Color = hp.Theme.Color.GrayText2
					label.Font.Weight = font.SemiBold
					return label.Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					count := hp.Theme.Caption(fmt.Sprintf("%d", len(hp.walletEntries)))
					count.Color = hp.Theme.Color.GrayText3
					return count.Layout(gtx)
				}),
			)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return hp.navRow(gtx, hp.createWalletClick, values.String(values.StrCreateWallet), hp.Theme.Color.Primary)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
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
	// The active wallet gets a filled highlight + primary-coloured name; the
	// rest are flat rows. This reads as a real selectable list instead of a
	// pile of identical labels.
	selected := e.wallet.GetWalletID() == hp.selectedWalletID
	bg := color.NRGBA{} // transparent
	nameColor := hp.Theme.Color.GrayText1
	nameWeight := font.Medium
	if selected {
		// Gray5 is the theme's hover/active fill (SurfaceHighlight is unset →
		// transparent, so it gave no visible highlight). Paired with the
		// primary-coloured name this clearly marks the active wallet.
		bg = hp.Theme.Color.Gray5
		nameColor = hp.Theme.Color.Primary
		nameWeight = font.SemiBold
	}

	return cryptomaterial.LinearLayout{
		Width:       cryptomaterial.MatchParent,
		Height:      cryptomaterial.WrapContent,
		Background:  bg,
		Border:      cryptomaterial.Border{Radius: cryptomaterial.Radius(8)},
		Padding:     layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8), Left: unit.Dp(10), Right: unit.Dp(10)},
		Margin:      layout.Inset{Bottom: unit.Dp(4)},
		Clickable:   e.click,
		Orientation: layout.Vertical,
	}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := hp.Theme.Body1(e.wallet.GetWalletName())
			lbl.Color = nameColor
			lbl.Font.Weight = nameWeight
			return lbl.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(1)}.Layout),
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
