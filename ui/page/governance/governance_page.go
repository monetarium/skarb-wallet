// Package governance hosts the consensus-voting (on-chain governance) page:
// every consensus deployment the network defines, its voting window and
// status, and — for agendas of the wallet's current vote version — the
// wallet's saved voting preference, editable in place. Politeia proposals
// and treasury voting from upstream Cryptopower stay out of Skarb v1; this
// page is consensus agendas only, built fully locally (no dcrdata).
package governance

import (
	"fmt"
	"image/color"
	"sync"
	"sync/atomic"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/monetarium/skarb-wallet/app"
	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/modal"
	"github.com/monetarium/skarb-wallet/ui/page/components"
	"github.com/monetarium/skarb-wallet/ui/values"
)

const GovernancePageID = "Governance"

type (
	C = layout.Context
	D = layout.Dimensions
)

type Page struct {
	*load.Load
	*app.GenericPageModal

	dcrWallet *dcr.Asset

	container *widget.List
	agendas   []*dcr.Agenda
	// choiceDropdowns holds one dropdown per CURRENT-version agenda (the
	// only ones whose preference can change). Keyed by agenda ID; absent
	// for historical agendas, which render read-only.
	choiceDropdowns map[string]*cryptomaterial.DropDown

	// voteInFlight guards against a second SetVoteChoice write starting
	// before the first returns: the walletdb write can block for the
	// duration of an in-progress rescan's batched write transaction, and
	// this page (unlike Send) is reachable while syncing. A click that
	// arrives while a write is in flight is simply dropped — rare, and the
	// dropdown will re-sync from loadAgendas once the in-flight write lands.
	voteInFlight atomic.Bool

	// resultMu guards pendingResult, written by the vote-choice goroutine
	// and drained on the UI thread in HandleUserInteractions — modals and
	// the choiceDropdowns map must only be touched from there (CLAUDE.md
	// SS3: never mutate Layout-read state off the UI thread).
	resultMu      sync.Mutex
	pendingResult *voteResult

	// Header actions: refreshBtn re-reads the agenda list and saved
	// preferences in place; dashboardBtn offers the block explorer's live
	// voting dashboard — the tallies live on-chain and this wallet keeps no
	// chain index to compute them locally. copyRedirect backs the copy
	// control inside the dashboard-link modal.
	refreshBtn   *cryptomaterial.Clickable
	dashboardBtn cryptomaterial.Button
	copyRedirect *cryptomaterial.Clickable
}

type voteResult struct {
	err error
}

func NewGovernancePage(l *load.Load, dcrWallet *dcr.Asset) *Page {
	pg := &Page{
		Load:             l,
		GenericPageModal: app.NewGenericPageModal(GovernancePageID),
		dcrWallet:        dcrWallet,
		container: &widget.List{
			List: layout.List{Axis: layout.Vertical},
		},
		choiceDropdowns: make(map[string]*cryptomaterial.DropDown),
		refreshBtn:      l.Theme.NewClickable(false),
		copyRedirect:    l.Theme.NewClickable(false),
	}
	pg.dashboardBtn = l.Theme.OutlineButton(values.String(values.StrVotingDashboard))
	pg.dashboardBtn.TextSize = values.TextSize14
	pg.dashboardBtn.Inset = layout.Inset{
		Top: values.MarginPadding4, Bottom: values.MarginPadding4,
		Left: values.MarginPadding8, Right: values.MarginPadding8,
	}
	return pg
}

// loadAgendas (re)reads the deployment list and the wallet's saved
// preferences, and (re)builds the per-agenda choice dropdowns. Cheap: the
// deployments come from in-memory chain params and the preferences are one
// walletdb read — safe to run on the UI thread from lifecycle/interaction
// handlers.
func (pg *Page) loadAgendas() {
	agendas, err := pg.dcrWallet.AllVoteAgendas(true)
	if err != nil {
		log.Errorf("governance: AllVoteAgendas: %v", err)
		return
	}
	pg.agendas = agendas

	for _, agenda := range agendas {
		if !agenda.IsCurrent {
			continue
		}
		items := make([]cryptomaterial.DropDownItem, 0, len(agenda.Choices))
		for _, choice := range agenda.Choices {
			// Text stays the raw protocol choice ID ("yes"/"no"/"abstain")
			// — Selected()/SetSelectedValue() match on it, and it round-trips
			// straight into SetVoteChoice. DisplayFn renders a localized
			// label for both the closed and expanded views (itemLayout
			// prefers DisplayFn over Text when set) without touching that
			// value channel.
			items = append(items, cryptomaterial.DropDownItem{
				Text:      choice.Id,
				DisplayFn: choiceLabel(pg.Theme, choice.Id),
			})
		}
		dropdown := pg.Theme.NewCommonDropDown(items, nil,
			values.MarginPadding180, values.ConsensusDropdownGroup, false)
		preference := agenda.VotingPreference
		if preference == "" {
			// The wallet defaults every agenda to abstain until a choice
			// is saved; reflect that instead of an empty selector.
			preference = "abstain"
		}
		dropdown.SetSelectedValue(preference)
		pg.choiceDropdowns[agenda.AgendaID] = dropdown
	}
}

func (pg *Page) OnNavigatedTo() {
	pg.loadAgendas()
}

func (pg *Page) OnNavigatedFrom() {}

func (pg *Page) HandleUserInteractions(gtx C) {
	// Drain a completed vote-choice write first — before reading
	// choiceDropdowns below, since this also rebuilds that map via
	// loadAgendas. Sequential within this one UI-thread call, so replacing
	// the map here and then ranging over it further down is race-free.
	pg.resultMu.Lock()
	result := pg.pendingResult
	pg.pendingResult = nil
	pg.resultMu.Unlock()
	if result != nil {
		if result.err != nil {
			errModal := modal.NewErrorModal(pg.Load, result.err.Error(), modal.DefaultClickFunc())
			pg.ParentWindow().ShowModal(errModal)
		} else {
			infoModal := modal.NewSuccessModal(pg.Load, values.String(values.StrVoteUpdated), modal.DefaultClickFunc())
			pg.ParentWindow().ShowModal(infoModal)
		}
		// Re-sync every dropdown with what is actually saved, whether the
		// write succeeded or failed.
		pg.loadAgendas()
	}

	if pg.refreshBtn.Clicked(gtx) {
		// Cheap by design (see loadAgendas) — safe directly on the UI
		// thread, and the rebuilt dropdown map is only read further down
		// this same sequential call.
		pg.loadAgendas()
	}

	if pg.dashboardBtn.Clicked(gtx) {
		pg.showVotingDashboardModal()
	}

	for agendaID, dropdown := range pg.choiceDropdowns {
		if !dropdown.Changed(gtx) {
			continue
		}
		if !pg.voteInFlight.CompareAndSwap(false, true) {
			continue // a write is already in flight; drop this one
		}
		choiceID := dropdown.Selected()
		// Wallet-wide preference (empty ticket hash): applies to all of
		// this wallet's votable tickets. Off the UI thread: the walletdb
		// write can block for the duration of an in-progress rescan's
		// batched write transaction, and this page is reachable while
		// syncing (CLAUDE.md SS3 async->UI pattern).
		go func(agendaID, choiceID string) {
			err := pg.dcrWallet.SetVoteChoice(agendaID, choiceID, "")
			if err != nil {
				log.Errorf("governance: SetVoteChoice(%s=%s): %v", agendaID, choiceID, err)
			}
			pg.resultMu.Lock()
			pg.pendingResult = &voteResult{err: err}
			pg.resultMu.Unlock()
			pg.voteInFlight.Store(false)
			pg.ParentWindow().Reload()
		}(agendaID, choiceID)
	}
}

// showVotingDashboardModal offers the block explorer's live voting dashboard.
// Vote tallies live on-chain and the wallet keeps no chain index to compute
// them, so the page links out instead of charting its own results (matching
// upstream Cryptopower, which doesn't chart consensus tallies either — owner
// decision, 2026-07-20). The modal shows the URL with a copy control plus an
// explicit "open in browser" action rather than launching a browser
// unannounced.
func (pg *Page) showVotingDashboardModal() {
	url := pg.AssetsManager.BlockExplorerURLForAgendas()
	if url == "" {
		return // no deployed explorer for this net; the button is hidden too
	}
	info := modal.NewCustomModal(pg.Load).
		Title(values.String(values.StrVotingDashboard)).
		Body(values.String(values.StrCopyLink)).
		SetCancelable(true).
		UseCustomWidget(func(gtx C) D {
			return components.BrowserURLWidget(gtx, pg.Load, url, pg.copyRedirect)
		}).
		SetNegativeButtonText(values.String(values.StrCancel)).
		SetPositiveButtonText(values.String(values.StrOpenInBrowser)).
		SetPositiveButtonCallback(func(_ bool, _ *modal.InfoModal) bool {
			components.GoToURL(url)
			return true
		})
	pg.ParentWindow().ShowModal(info)
}

func (pg *Page) Layout(gtx C) D {
	isMobile := pg.IsMobileView()
	padding := values.MarginPaddingTransform(isMobile, values.MarginPadding24)

	return pg.Theme.Card().Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.UniformInset(padding).Layout(gtx, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Bottom: values.MarginPadding16}.Layout(gtx, func(gtx C) D {
						return components.EndToEndRow(gtx,
							func(gtx C) D {
								title := pg.Theme.Label(values.TextSizeTransform(isMobile, values.TextSize20), values.String(values.StrConsensusChange))
								title.Font.Weight = font.SemiBold
								return title.Layout(gtx)
							},
							func(gtx C) D {
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
									layout.Rigid(func(gtx C) D {
										return layout.Inset{Right: values.MarginPadding12}.Layout(gtx, func(gtx C) D {
											return pg.refreshBtn.Layout(gtx, pg.Theme.NewIcon(pg.Theme.Icons.NavigationRefresh).Layout24dp)
										})
									}),
									layout.Rigid(func(gtx C) D {
										if pg.AssetsManager.BlockExplorerURLForAgendas() == "" {
											return D{}
										}
										return pg.dashboardBtn.Layout(gtx)
									}),
								)
							},
						)
					})
				}),
				layout.Rigid(func(gtx C) D {
					if len(pg.agendas) == 0 {
						lbl := pg.Theme.Body1(values.String(values.StrNoAgendaYet))
						lbl.Color = pg.Theme.Color.GrayText3
						return lbl.Layout(gtx)
					}
					return pg.Theme.List(pg.container).Layout(gtx, len(pg.agendas), func(gtx C, i int) D {
						return layout.Inset{Bottom: values.MarginPadding16}.Layout(gtx, func(gtx C) D {
							return pg.agendaCard(gtx, pg.agendas[i])
						})
					})
				}),
			)
		})
	})
}

// choiceLabel renders a localized label for a consensus choice ID. Every
// deployment's Choices list is drawn from {"abstain","no","yes"} in
// practice (chaincfg's own vote definitions), which already have
// StrAbstain/StrNo/StrYes translations; an unrecognized ID (a future
// deployment with a non-standard choice) falls back to the raw ID rather
// than failing to render.
func choiceLabel(theme *cryptomaterial.Theme, choiceID string) layout.Widget {
	text := choiceID
	switch choiceID {
	case "abstain":
		text = values.String(values.StrAbstain)
	case "no":
		text = values.String(values.StrNo)
	case "yes":
		text = values.String(values.StrYes)
	}
	return func(gtx C) D {
		return theme.Label(values.TextSize16, text).Layout(gtx)
	}
}

// statusLabel maps the locally-derived agenda status onto existing
// localized strings ("ended" reuses "Finished").
func (pg *Page) statusLabel(status dcr.AgendaStatusType) (string, color.NRGBA) {
	switch status {
	case dcr.AgendaStatusUpcoming:
		return values.String(values.StrUpcoming), pg.Theme.Color.GrayText2
	case dcr.AgendaStatusInProgress:
		return values.String(values.StrInProgress), pg.Theme.Color.Primary
	default:
		return values.String(values.StrFinished), pg.Theme.Color.GrayText3
	}
}

func (pg *Page) agendaCard(gtx C, agenda *dcr.Agenda) D {
	isMobile := pg.IsMobileView()
	textSize14 := values.TextSizeTransform(isMobile, values.TextSize14)
	textSize16 := values.TextSizeTransform(isMobile, values.TextSize16)

	dateFmt := func(ts int64) string {
		return time.Unix(ts, 0).Format("02 Jan 2006")
	}

	return cryptomaterial.LinearLayout{
		Width:       cryptomaterial.MatchParent,
		Height:      cryptomaterial.WrapContent,
		Orientation: layout.Vertical,
		Background:  pg.Theme.Color.Surface,
		Border:      cryptomaterial.Border{Radius: cryptomaterial.Radius(8), Color: pg.Theme.Color.Gray3, Width: values.MarginPadding1},
		Padding:     layout.UniformInset(values.MarginPadding16),
	}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			// EndToEndRow (Rigid left + Flexed east) instead of a
			// SpaceBetween flex: inside this card the row is measured with
			// Min.X == 0, so SpaceBetween had no leftover width to
			// distribute and the status chip rendered glued to the agenda
			// ID. The Flexed side always takes the remaining width, and the
			// inset keeps a gap even when the title grows to fill the row.
			return components.EndToEndRow(gtx,
				func(gtx C) D {
					return layout.Inset{Right: values.MarginPadding16}.Layout(gtx, func(gtx C) D {
						lbl := pg.Theme.Label(textSize16, agenda.AgendaID)
						lbl.Font.Weight = font.SemiBold
						return lbl.Layout(gtx)
					})
				},
				func(gtx C) D {
					text, color := pg.statusLabel(agenda.Status)
					lbl := pg.Theme.Label(textSize14, text)
					lbl.Color = color
					return lbl.Layout(gtx)
				},
			)
		}),
		layout.Rigid(func(gtx C) D {
			lbl := pg.Theme.Label(textSize14, fmt.Sprintf("%s %d", values.String(values.StrVoteVersion), agenda.VoteVersion))
			lbl.Color = pg.Theme.Color.GrayText2
			return layout.Inset{Top: values.MarginPadding2}.Layout(gtx, lbl.Layout)
		}),
		layout.Rigid(func(gtx C) D {
			lbl := pg.Theme.Label(textSize14, agenda.Description)
			return layout.Inset{Top: values.MarginPadding8}.Layout(gtx, lbl.Layout)
		}),
		layout.Rigid(func(gtx C) D {
			period := fmt.Sprintf("%s — %s", dateFmt(agenda.StartTime), dateFmt(agenda.ExpireTime))
			lbl := pg.Theme.Label(textSize14, period)
			lbl.Color = pg.Theme.Color.GrayText2
			return layout.Inset{Top: values.MarginPadding4}.Layout(gtx, lbl.Layout)
		}),
		layout.Rigid(func(gtx C) D {
			if agenda.ForcedChoiceID == "" {
				return D{}
			}
			lbl := pg.Theme.Label(textSize14, fmt.Sprintf("%s: %s", values.String(values.StrForcedChoice), agenda.ForcedChoiceID))
			lbl.Color = pg.Theme.Color.GrayText2
			return layout.Inset{Top: values.MarginPadding4}.Layout(gtx, lbl.Layout)
		}),
		layout.Rigid(func(gtx C) D {
			dropdown := pg.choiceDropdowns[agenda.AgendaID]
			if dropdown == nil {
				return D{}
			}
			return layout.Inset{Top: values.MarginPadding12}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						lbl := pg.Theme.Label(textSize14, values.String(values.StrVotingPreference))
						return layout.Inset{Right: values.MarginPadding8}.Layout(gtx, lbl.Layout)
					}),
					layout.Rigid(dropdown.Layout),
				)
			})
		}),
	)
}
