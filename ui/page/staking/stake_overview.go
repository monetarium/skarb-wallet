package staking

import (
	"context"
	"fmt"
	"sync/atomic"

	"gioui.org/layout"
	"gioui.org/text"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/monetarium/monetarium-node/dcrutil"
	"github.com/monetarium/skarb-wallet/app"
	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	libutils "github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/modal"
	"github.com/monetarium/skarb-wallet/ui/page/components"
	"github.com/monetarium/skarb-wallet/ui/page/settings"
	tpage "github.com/monetarium/skarb-wallet/ui/page/transaction"
	"github.com/monetarium/skarb-wallet/ui/values"
)

type (
	C = layout.Context
	D = layout.Dimensions
)

const (
	OverviewPageID = "staking"

	// pageSize define the maximum number of items fetched for the list scroll view.
	pageSize int32 = 20

	// The ticket height limit helps separate the scrolling of the ticket list and the page
	ticketHeight = 500
)

type Page struct {
	*load.Load
	// GenericPageModal defines methods such as ID() and OnAttachedToNavigator()
	// that helps this Page satisfy the app.Page interface. It also defines
	// helper methods for accessing the PageNavigator that displayed this page
	// and the root WindowNavigator.
	*app.GenericPageModal

	scroll          *components.Scroll[*transactionItem]
	scrollContainer *widget.List

	ticketOverview *dcr.StakingOverview

	ticketsList    *cryptomaterial.ClickableList
	stakeSettings  *cryptomaterial.Clickable
	stake          *cryptomaterial.Switch
	infoButton     cryptomaterial.IconButton
	materialLoader material.LoaderStyle

	ticketPrice string
	// rewardRows holds one pre-formatted "12.34 VAR" / "1.50 SKA1" staking-
	// reward line per visibility-allowed coin (VAR first), zeros included —
	// the user asked for a Total Reward row per visible coin.
	rewardRows []string
	// showMaterialLoader is toggled from the FetchScrollData goroutine and read
	// by Layout (stake_list.go) — atomic to avoid the §3 data race.
	showMaterialLoader atomic.Bool
	// dataLoaded guards the one-time staking-data load so it can be re-triggered
	// exactly once when the wallet transitions from syncing → synced (the page
	// may be opened while still syncing). UI-thread-only.
	dataLoaded bool

	// loadPageData() runs on a goroutine; its results are staged here and
	// applied on the UI thread in HandleUserInteractions. Writing
	// ticketOverview/totalRewards (which Layout reads) directly from the
	// goroutine is a data race (CLAUDE.md §3) — a torn read of the
	// ticketOverview pointer can crash the staking page.
	pendingDataApply atomic.Bool
	stagedOverview   *dcr.StakingOverview
	stagedRewardRows []string
	stagedDataErr    error

	// pendingRefresh is set by the tx/block notification callbacks (goroutine)
	// and drained on the UI thread in HandleUserInteractions, where it re-runs
	// the full data load (price, overview, rewards, ticket list). Without it
	// the notification callbacks only redrew the page — the stored fields went
	// stale until the user left and re-entered. The CAS coalesces notification
	// bursts into one refresh per frame.
	pendingRefresh atomic.Bool

	// loadingData single-flights loadPageData's goroutine: per-notification
	// refreshes could otherwise spawn overlapping loads whose unsynchronized
	// writes to the staged* fields race each other (the pendingDataApply CAS
	// orders producer→consumer, not producer↔producer), each re-scanning the
	// whole tx set.
	loadingData atomic.Bool

	// ticketRefreshInFlight coalesces ticket-list refetches, mirroring
	// transactions_page.go's txRefreshInFlight: loadNewItem=true resets and
	// re-fetches the whole list, and concurrent runs race the scroll
	// component's data/offset/position and break scrolling.
	ticketRefreshInFlight atomic.Bool

	navToSettingsBtn cryptomaterial.Button
	buyTicketBtn     cryptomaterial.Button
	processingTicket uint32

	// Manual one-time purchase runs PurchaseTickets (blocking: VSP round-trips +
	// broadcast) on a goroutine; its result is staged here and applied on the UI
	// thread in HandleUserInteractions (CLAUDE.md §3). purchasing guards against
	// re-entrancy while a purchase is in flight.
	purchasing            atomic.Bool
	pendingPurchaseResult atomic.Bool
	stagedPurchaseCount   int
	stagedPurchaseErr     error

	dcrWallet *dcr.Asset

	// ticketContext is a managed context instance that is shut once a shutdown
	// request is made. It helps avoid the use of context.TODO() that isn't
	// responsive to the shutdown request.
	ticketContext context.Context
}

func NewStakingPage(l *load.Load, dcrWallet *dcr.Asset) *Page {
	pg := &Page{
		Load:             l,
		GenericPageModal: app.NewGenericPageModal(OverviewPageID),
		scrollContainer: &widget.List{
			List: layout.List{
				Axis:      layout.Vertical,
				Alignment: layout.Middle,
			},
		},
		dcrWallet: dcrWallet,
	}

	// context will list for a shutdown request.
	pg.ticketContext, _ = dcrWallet.ShutdownContextWithCancel()

	pg.scroll = components.NewScroll(l, pageSize, pg.fetchTickets)
	pg.materialLoader = material.Loader(l.Theme.Base)
	pg.ticketOverview = new(dcr.StakingOverview)
	pg.initStakePriceWidget()
	pg.initTicketList()

	pg.navToSettingsBtn = l.Theme.Button(values.StringF(values.StrEnableAPI, values.String(values.StrVsp)))
	pg.buyTicketBtn = l.Theme.Button(values.String(values.StrBuyTickets))

	return pg
}

// OnNavigatedTo is called when the page is about to be displayed and
// may be used to initialize page features that are only relevant when
// the page is displayed.
// Part of the load.Page interface.
func (pg *Page) OnNavigatedTo() {
	// If staking is disabled no startup func should be called
	// Layout will draw an overlay to show that stacking is disabled.

	isSyncingOrRescanning := !pg.dcrWallet.IsSynced() || pg.dcrWallet.IsRescanning()
	if pg.isTicketsPurchaseAllowed() && !isSyncingOrRescanning {
		pg.startPageData()
	}
}

// startPageData kicks off the staking-data fetch (price, overview, rewards,
// ticket list) and the tx-notification listener. Idempotent via dataLoaded so
// it runs exactly once per page instance — from OnNavigatedTo when the wallet
// is already synced, or from HandleUserInteractions the moment it becomes
// synced (the page may be opened mid-sync, when none of this can run yet).
func (pg *Page) startPageData() {
	if pg.dataLoaded {
		return
	}
	pg.dataLoaded = true

	pg.fetchTicketPrice()
	pg.loadPageData() // starts a goroutine to refresh the display
	pg.stake.SetChecked(pg.dcrWallet.IsAutoTicketsPurchaseActive())
	pg.setStakingButtonsState()
	pg.listenForTxNotifications() // tx ntfn listener is stopped in OnNavigatedFrom().

	go func() {
		pg.showMaterialLoader.Store(true)
		// Through the coalescing guard too: a notification arriving during
		// this very first load must not start a second concurrent fetch.
		pg.refreshTicketList()
		pg.showMaterialLoader.Store(false)
		pg.ParentWindow().Reload()
	}()
}

// refreshTicketList resets and re-fetches the staking ticket list, then
// redraws. A plain FetchScrollData(..., isResetList=false) is a NO-OP once
// the list already holds data (items_scroll.go skips the fetch), so every
// post-load refresh must pass loadNewItem=true — and concurrent runs must be
// coalesced (see ticketRefreshInFlight): a block confirms many txs at once,
// and parallel reset+refetch runs corrupt the scroll state. Blocking — run
// it off the UI thread (`go pg.refreshTicketList()`).
func (pg *Page) refreshTicketList() {
	if !pg.ticketRefreshInFlight.CompareAndSwap(false, true) {
		return
	}
	defer pg.ticketRefreshInFlight.Store(false)
	pg.scroll.FetchScrollDataHandler(false, pg.ParentWindow(), false, true)
	pg.ParentWindow().Reload()
}

// fetch ticket price only when the wallet is synced
func (pg *Page) fetchTicketPrice() {
	ticketPrice, err := pg.dcrWallet.TicketPrice()
	if err != nil {
		// TicketPrice can fail transiently (RPC/context) even on a fully synced
		// wallet, returning (nil, err). Branch on err alone — the old
		// `err != nil && !IsSynced()` guard fell through to dereference the nil
		// ticketPrice on a synced wallet and panicked the page every frame.
		log.Error(err)
		pg.ticketPrice = dcrutil.Amount(0).String()
		if !pg.dcrWallet.IsSynced() {
			errModal := modal.NewErrorModal(pg.Load, values.String(values.StrWalletNotSynced), modal.DefaultClickFunc())
			pg.ParentWindow().ShowModal(errModal)
		}
		return
	}
	pg.ticketPrice = dcrutil.Amount(ticketPrice.TicketPrice).String()
}

func (pg *Page) setStakingButtonsState() {
	// disable auto ticket purchase if wallet is not synced
	pg.stake.SetEnabled(pg.dcrWallet.IsSynced() || !pg.dcrWallet.IsWatchingOnlyWallet())
}

func (pg *Page) loadPageData() {
	// Single-flight: skip when a load is already running — the in-flight
	// goroutine reads current data anyway, and the next notification
	// re-triggers a fresh load.
	if !pg.loadingData.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer pg.loadingData.Store(false)
		if len(pg.dcrWallet.KnownVSPs()) == 0 {
			// TODO: Does this page need this list?
			pg.dcrWallet.ReloadVSPList(context.TODO())
		}

		var rewardRows []string
		var overview *dcr.StakingOverview
		var stageErr error
		if totals, err := pg.dcrWallet.TotalStakingRewardsByCoin(); err != nil {
			stageErr = err
		} else {
			// One "Total Reward" line per visibility-allowed coin, VAR
			// first, zeros included (an allowed coin must not vanish from
			// the stats just because nothing was earned yet).
			for _, ct := range pg.dcrWallet.VisibleCoinTypes() {
				atoms := "0"
				if t := totals[ct]; t != nil {
					atoms = t.String()
				}
				rewardRows = append(rewardRows, dcr.FormatTxAmountBig(atoms, 0, uint8(ct)))
			}
		}
		if ov, err := pg.dcrWallet.StakingOverview(); err != nil {
			stageErr = err
		} else {
			overview = ov
		}

		// Stage for the UI thread — never write ticketOverview/rewardRows
		// (read by Layout) or show a modal from this goroutine (CLAUDE.md §3).
		pg.stagedRewardRows = rewardRows
		pg.stagedOverview = overview
		pg.stagedDataErr = stageErr
		pg.pendingDataApply.Store(true)
		pg.ParentWindow().Reload()
	}()
}

func (pg *Page) isTicketsPurchaseAllowed() bool {
	// The VSP HTTP-API privacy gate used to blank this whole page with a
	// "not allowed" overlay whenever the VSP API was off — and Monetarium has
	// no activated VSP, so the staking UI was never reviewable. Always allow:
	// the overlay stays dormant, page data loads, and individual VSP-backed
	// actions still fail gracefully on their own errors.
	return true
}

// Layout draws the page UI components into the provided layout context
// to be eventually drawn on screen.
// Part of the load.Page interface.
func (pg *Page) Layout(gtx C) D {
	// If Tickets Purchase API is not allowed, display the overlay with the message.
	isSyncingOrRescanning := !pg.dcrWallet.IsSynced() || pg.dcrWallet.IsRescanning()
	overlay := layout.Stacked(func(_ C) D { return D{} })
	if !pg.isTicketsPurchaseAllowed() && !isSyncingOrRescanning {
		gtxCopy := gtx
		overlay = layout.Stacked(func(_ C) D {
			str := values.StringF(values.StrNotAllowed, values.String(values.StrVsp))
			return components.DisablePageWithOverlay(pg.Load, nil, gtxCopy, str, "", &pg.navToSettingsBtn)
		})
		// Disable main page from receiving events
		gtx = gtx.Disabled()
	}

	mainChild := layout.Expanded(func(gtx C) D {
		pg.scroll.OnScrollChangeListener(pg.ParentWindow())
		return pg.Theme.List(pg.scrollContainer).Layout(gtx, 1, func(gtx C, _ int) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(pg.stakePriceSection),
				layout.Rigid(pg.stakeStatisticsSection),
				layout.Rigid(pg.ticketListLayout),
			)
		})
	})

	return layout.Stack{}.Layout(gtx, mainChild, overlay)
}

func (pg *Page) pageSections(gtx C, body layout.Widget) D {
	return layout.Inset{
		Bottom: values.MarginPadding16,
	}.Layout(gtx, func(gtx C) D {
		return pg.Theme.Card().Layout(gtx, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.UniformInset(values.MarginPaddingTransform(pg.IsMobileView(), values.MarginPadding24)).Layout(gtx, body)
		})
	})
}

// HandleUserInteractions is called just before Layout() to determine
// if any user interaction recently occurred on the page and may be
// used to update the page's UI components shortly before they are
// displayed.
// Part of the load.Page interface.
func (pg *Page) HandleUserInteractions(gtx C) {
	// Apply staking data staged by loadPageData()'s goroutine on the UI thread
	// (CLAUDE.md §3). The atomic CAS publishes the goroutine's writes.
	if pg.pendingDataApply.CompareAndSwap(true, false) {
		if pg.stagedRewardRows != nil {
			pg.rewardRows = pg.stagedRewardRows
		}
		if pg.stagedOverview != nil {
			pg.ticketOverview = pg.stagedOverview
		}
		if pg.stagedDataErr != nil {
			errModal := modal.NewErrorModal(pg.Load, pg.stagedDataErr.Error(), modal.DefaultClickFunc())
			pg.ParentWindow().ShowModal(errModal)
			pg.stagedDataErr = nil
		}
	}

	// A new tx / block arrived: re-run the full data load in place so the
	// user never has to leave and re-enter the page. loadPageData stages its
	// results (drained above); the ticket-list refetch runs on its own
	// goroutine like every other FetchScrollData call site. dataLoaded gates
	// out the pre-first-load window (startPageData will do the initial load).
	// During a rescan the flag is left SET (checked before the CAS): the
	// OnTransaction flood would otherwise re-scan the tx set per frame, and
	// dropping the flag would lose the refresh owed once the rescan ends.
	if pg.dataLoaded && !pg.dcrWallet.IsRescanning() &&
		pg.pendingRefresh.CompareAndSwap(true, false) {
		pg.fetchTicketPrice()
		pg.loadPageData()
		go pg.refreshTicketList()
	}

	// Apply the result of a manual purchase staged by the goroutine in
	// startManualPurchasePasswordModal (CLAUDE.md §3 — the goroutine never shows
	// a modal or touches Layout-read state directly).
	if pg.pendingPurchaseResult.CompareAndSwap(true, false) {
		if pg.stagedPurchaseErr != nil {
			errModal := modal.NewErrorModal(pg.Load, values.StringF(values.StrTicketError, pg.stagedPurchaseErr), modal.DefaultClickFunc())
			pg.ParentWindow().ShowModal(errModal)
			pg.stagedPurchaseErr = nil
		} else {
			successModal := modal.NewSuccessModal(pg.Load,
				values.StringF(values.StrTicketsPurchased, pg.stagedPurchaseCount), modal.DefaultClickFunc())
			pg.ParentWindow().ShowModal(successModal)
			// Refresh overview/rewards and the ticket list to show the new tickets.
			pg.loadPageData()
			go pg.refreshTicketList()
		}
	}

	// If the page was opened while the wallet was still syncing, none of the
	// staking data loaded. Trigger the load once the wallet first becomes synced
	// (startPageData is idempotent via dataLoaded), so Statistics / ticket list
	// fill in without the user having to navigate away and back.
	if !pg.dataLoaded && pg.isTicketsPurchaseAllowed() &&
		pg.dcrWallet.IsSynced() && !pg.dcrWallet.IsRescanning() {
		pg.startPageData()
	}

	pg.setStakingButtonsState()

	if pg.navToSettingsBtn.Clicked(gtx) {
		pg.ParentWindow().Display(settings.NewAppSettingsPage(pg.Load))
	}

	if pg.buyTicketBtn.Clicked(gtx) {
		pg.showManualPurchaseModal()
	}

	if pg.stake.Changed(gtx) {
		if pg.stake.IsChecked() {
			if pg.dcrWallet.TicketBuyerConfigIsSet() {
				// get ticket buyer config to check if the saved wallet account is mixed
				// check if mixer is set, if yes check if allow spend from unmixed account
				// if not set, check if the saved account is mixed before opening modal
				// if it is not, open stake config modal
				tbConfig := pg.dcrWallet.AutoTicketsBuyerConfig()
				if pg.dcrWallet.ReadBoolConfigValueForKey(sharedW.AccountMixerConfigSet, false) &&
					!pg.dcrWallet.ReadBoolConfigValueForKey(sharedW.SpendUnmixedFundsKey, false) &&
					(tbConfig.PurchaseAccount == pg.dcrWallet.MixedAccountNumber()) {
					pg.startTicketBuyerPasswordModal()
				} else {
					pg.ticketBuyerSettingsModal()
				}
			} else {
				pg.ticketBuyerSettingsModal()
			}
		} else {
			_ = pg.dcrWallet.StopAutoTicketsPurchase()
		}
	}

	if pg.stakeSettings.Clicked(gtx) && !pg.dcrWallet.IsWatchingOnlyWallet() {
		if pg.dcrWallet.IsAutoTicketsPurchaseActive() {
			errModal := modal.NewErrorModal(pg.Load, values.String(values.StrAutoTicketWarn), modal.DefaultClickFunc())
			pg.ParentWindow().ShowModal(errModal)
			return
		}

		ticketBuyerModal := newTicketBuyerModal(pg.Load, pg.dcrWallet).
			OnSettingsSaved(func() {
				infoModal := modal.NewSuccessModal(pg.Load, values.String(values.StrTicketSettingSaved), modal.DefaultClickFunc())
				pg.ParentWindow().ShowModal(infoModal)
			}).
			OnCancel(func() {
				pg.stake.SetChecked(false)
			})
		pg.ParentWindow().ShowModal(ticketBuyerModal)
	}

	// Refresh the ticket price only at the stake-difficulty window boundary
	// (the only moment it changes). The old extra per-frame
	// `if IsSynced { fetchTicketPrice }` opened a bbolt read transaction on the
	// UI thread on every redraw — needless disk I/O and jank.
	secs, _ := pg.dcrWallet.NextTicketPriceRemaining()
	if secs <= 0 {
		pg.fetchTicketPrice()
	}

	if clicked, selectedItem := pg.ticketsList.ItemClicked(); clicked {
		tickets := pg.scroll.FetchedData()
		// The list can shrink under us between the click frame and now (async
		// refetches from sync/notifications) — ignore a stale out-of-range index.
		if selectedItem >= 0 && selectedItem < len(tickets) {
			ticketTx := tickets[selectedItem].transaction
			pg.ParentNavigator().Display(tpage.NewTransactionDetailsPage(pg.Load, pg.dcrWallet, ticketTx))

			// Check if this ticket is fully registered with a VSP and process any
			// unpaid fee. VSPTicketInfo + Client.Process each do a blocking HTTPS
			// round-trip to the VSP, so run them OFF the UI thread (the CAS is a
			// real single-flight guard). Capture the tx fields by value so the
			// goroutine never reads mutating page state, and do not touch any
			// UI-read state from it (CLAUDE.md §3). The wallet must be unlocked
			// for the VSP to return info; a locked wallet is ignored (log only).
			if atomic.CompareAndSwapUint32(&pg.processingTicket, 0, 1) {
				txHash := ticketTx.Hash
				inputCount := len(ticketTx.Inputs)
				go func() {
					defer atomic.StoreUint32(&pg.processingTicket, 0)
					ticketInfo, err := pg.dcrWallet.VSPTicketInfo(txHash)
					if err != nil {
						if err.Error() != libutils.ErrWalletLocked {
							log.Errorf("VSPTicketInfo error: %v", err)
						}
						return
					}
					if ticketInfo.FeeTxStatus != dcr.VSPFeeProcessConfirmed || !ticketInfo.ConfirmedByVSP {
						log.Warnf("Ticket %s has unconfirmed fee tx with status %q, vsp %s",
							txHash, ticketInfo.FeeTxStatus.String(), ticketInfo.VSP)
					}
					// Process the unconfirmed fee only if not already paid, the
					// ticket has a single input, and a VSP client is available.
					if ticketInfo.FeeTxStatus != dcr.VSPFeeProcessPaid && inputCount == 1 && ticketInfo.Client != nil {
						log.Infof("Attempting to process the unconfirmed VSP fee for tx: %v", txHash)
						if err := ticketInfo.Client.Process(pg.ticketContext, ticketInfo.VSPTicket, nil); err != nil {
							log.Errorf("processing the unconfirmed tx fee failed: %v", err)
						}
					}
				}()
			}
		}
	}

	if pg.infoButton.Button.Clicked(gtx) {
		backupNowOrLaterModal := modal.NewCustomModal(pg.Load).
			Title(values.String(values.StrStatistics)).
			SetCancelable(true).
			UseCustomWidget(func(gtx C) D {
				return pg.stakingRecordStatistics(gtx)
			}).
			SetPositiveButtonText(values.String(values.StrGotIt))
		pg.ParentWindow().ShowModal(backupNowOrLaterModal)
	}
}

func (pg *Page) ticketBuyerSettingsModal() {
	ticketBuyerModal := newTicketBuyerModal(pg.Load, pg.dcrWallet).
		OnCancel(func() {
			pg.stake.SetChecked(false)
		}).
		OnSettingsSaved(func() {
			pg.startTicketBuyerPasswordModal()
			infoModal := modal.NewSuccessModal(pg.Load, values.String(values.StrTicketSettingSaved), modal.DefaultClickFunc())
			pg.ParentWindow().ShowModal(infoModal)
		})
	pg.ParentWindow().ShowModal(ticketBuyerModal)
}

// showManualPurchaseModal opens the one-time ticket-purchase modal, seeding it
// with the current ticket price (the modal keeps the price fresh per block
// while open and reports its current value back through OnPurchase).
func (pg *Page) showManualPurchaseModal() {
	if pg.purchasing.Load() {
		return // a purchase is already in flight
	}
	tp, err := pg.dcrWallet.TicketPrice()
	if err != nil {
		errModal := modal.NewErrorModal(pg.Load, values.StringF(values.StrTicketError, err), modal.DefaultClickFunc())
		pg.ParentWindow().ShowModal(errModal)
		return
	}
	if tp == nil {
		return
	}
	ticketPriceAtoms := tp.TicketPrice
	purchaseModal := newManualPurchaseModal(pg.Load, pg.dcrWallet, ticketPriceAtoms).
		OnPurchase(func(accountNumber, numTickets int32, vsp *dcr.VSP, currentPriceAtoms int64) {
			// Use the modal's CURRENT price, not the open-time snapshot —
			// the card refreshes it per block while open, and the confirm
			// modal's total must match what the user just saw.
			pg.startManualPurchasePasswordModal(accountNumber, numTickets, vsp, currentPriceAtoms)
		})
	pg.ParentWindow().ShowModal(purchaseModal)
}

// startManualPurchasePasswordModal confirms the purchase, takes the spending
// passphrase, and runs the blocking PurchaseTickets off the UI thread. The
// result (count or error) is staged and surfaced as a modal in
// HandleUserInteractions (CLAUDE.md §3). The password modal is dismissed
// optimistically; a wrong passphrase or purchase failure comes back as an error
// modal rather than an inline field error (PurchaseTickets is too slow to run
// synchronously in the callback).
func (pg *Page) startManualPurchasePasswordModal(accountNumber, numTickets int32, vsp *dcr.VSP, ticketPriceAtoms int64) {
	// vsp.PubKey promotes from the embedded *vspd.VspInfoResponse; guard against a
	// nil embed before it's dereferenced below. The Direct-buy sentinel carries a
	// nil embed by design and purchases without a VSP (empty host, nil pubkey).
	if vsp == nil || (!vsp.IsDirectBuy() && vsp.VspInfoResponse == nil) {
		return
	}
	name, err := pg.dcrWallet.AccountNameRaw(uint32(accountNumber))
	if err != nil {
		errModal := modal.NewErrorModal(pg.Load, values.StringF(values.StrTicketError, err), modal.DefaultClickFunc())
		pg.ParentWindow().ShowModal(errModal)
		return
	}
	total := dcrutil.Amount(int64(numTickets) * ticketPriceAtoms).String()

	passwordModal := modal.NewCreatePasswordModal(pg.Load).
		EnableName(false).
		EnableConfirmPassword(false).
		Title(values.String(values.StrConfirmManualPurchase)).
		SetCancelable(true).
		UseCustomWidget(func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(pg.Theme.Label(values.TextSize14, values.StringF(values.StrWalletToPurchaseFrom, pg.dcrWallet.GetWalletName())).Layout),
				layout.Rigid(pg.Theme.Label(values.TextSize14, values.StringF(values.StrSelectedAccount, name)).Layout),
				layout.Rigid(pg.Theme.Label(values.TextSize14, fmt.Sprintf("%s: %d", values.String(values.StrNumberOfTickets), numTickets)).Layout),
				layout.Rigid(pg.Theme.Label(values.TextSize14, fmt.Sprintf("%s: %s", values.String(values.StrTotalCost), total)).Layout),
				layout.Rigid(func(gtx C) D {
					vspName := vsp.Host
					if vsp.IsDirectBuy() {
						vspName = values.String(values.StrDirectBuy)
					}
					label := pg.Theme.Label(values.TextSize14, fmt.Sprintf("VSP: %s", vspName))
					return layout.Inset{Bottom: values.MarginPadding12}.Layout(gtx, label.Layout)
				}),
				layout.Rigid(func(gtx C) D {
					if !vsp.IsDirectBuy() {
						return D{}
					}
					warn := pg.Theme.Label(values.TextSize12, values.String(values.StrDirectBuyWarning))
					warn.Color = pg.Theme.Color.Danger
					return layout.Inset{Bottom: values.MarginPadding12}.Layout(gtx, warn.Layout)
				}),
			)
		}).
		SetPositiveButtonCallback(func(_, password string, pm *modal.CreatePasswordModal) bool {
			if !pg.dcrWallet.IsConnectedToNetwork() {
				pm.SetError(values.String(values.StrNotConnected))
				return false
			}
			// Single-flight: the manual modal dismisses as soon as the user
			// confirms, so the page's Buy button re-enables before this password
			// modal is submitted — a second flow could otherwise reach here and
			// start a goroutine racing stagedPurchase* with the first. CAS lets
			// exactly one purchase run; the loser is refused. The flag is cleared
			// by the winning goroutine when the purchase completes.
			if !pg.purchasing.CompareAndSwap(false, true) {
				pm.SetError(values.String(values.StrPurchasingTickets))
				return false
			}
			go func() {
				// PubKey promotes from the embedded response — nil for Direct buy.
				var vspPubKey []byte
				if vsp.VspInfoResponse != nil {
					vspPubKey = vsp.PubKey
				}
				hashes, err := pg.dcrWallet.PurchaseTickets(accountNumber, numTickets, vsp.Host, password, vspPubKey)
				// Clear the counterpart field too: only one branch runs, and a
				// leftover value from a prior purchase would otherwise be drained
				// as a stale result (e.g. a stale error after a later success).
				if err != nil {
					pg.stagedPurchaseErr = err
					pg.stagedPurchaseCount = 0
				} else {
					pg.stagedPurchaseCount = len(hashes)
					pg.stagedPurchaseErr = nil
				}
				pg.purchasing.Store(false)
				pg.pendingPurchaseResult.Store(true)
				pg.ParentWindow().Reload()
			}()
			return true
		})
	pg.ParentWindow().ShowModal(passwordModal)
}

func (pg *Page) startTicketBuyerPasswordModal() {
	tbConfig := pg.dcrWallet.AutoTicketsBuyerConfig()
	balToMaintain := pg.dcrWallet.ToAmount(tbConfig.BalanceToMaintain).ToCoin()
	name, err := pg.dcrWallet.AccountNameRaw(uint32(tbConfig.PurchaseAccount))
	if err != nil {
		errModal := modal.NewErrorModal(pg.Load, values.StringF(values.StrTicketError, err), modal.DefaultClickFunc())
		pg.ParentWindow().ShowModal(errModal)
		return
	}

	walletPasswordModal := modal.NewCreatePasswordModal(pg.Load).
		EnableName(false).
		EnableConfirmPassword(false).
		Title(values.String(values.StrConfirmPurchase)).
		SetCancelable(false).
		UseCustomWidget(func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(pg.Theme.Label(values.TextSize14, values.StringF(values.StrWalletToPurchaseFrom, pg.dcrWallet.GetWalletName())).Layout),
				layout.Rigid(pg.Theme.Label(values.TextSize14, values.StringF(values.StrSelectedAccount, name)).Layout),
				layout.Rigid(pg.Theme.Label(values.TextSize14, values.StringF(values.StrBalToMaintainValue, balToMaintain)).Layout), layout.Rigid(func(gtx C) D {
					// Empty saved host == solo (Direct buy) auto-staking.
					vspName := tbConfig.VspHost
					if vspName == "" {
						vspName = values.String(values.StrDirectBuy)
					}
					label := pg.Theme.Label(values.TextSize14, fmt.Sprintf("VSP: %s", vspName))
					return layout.Inset{Bottom: values.MarginPadding12}.Layout(gtx, label.Layout)
				}),
				layout.Rigid(func(gtx C) D {
					// Solo caveat — same warning as the manual Direct-buy flow.
					if tbConfig.VspHost != "" {
						return D{}
					}
					warn := pg.Theme.Label(values.TextSize12, values.String(values.StrDirectBuyWarning))
					warn.Color = pg.Theme.Color.Danger
					return layout.Inset{Bottom: values.MarginPadding12}.Layout(gtx, warn.Layout)
				}),
				layout.Rigid(func(gtx C) D {
					return cryptomaterial.LinearLayout{
						Width:      cryptomaterial.MatchParent,
						Height:     cryptomaterial.WrapContent,
						Background: pg.Theme.Color.LightBlue,
						Padding: layout.Inset{
							Top:    values.MarginPadding12,
							Bottom: values.MarginPadding12,
						},
						Border:    cryptomaterial.Border{Radius: cryptomaterial.Radius(8)},
						Direction: layout.Center,
						Alignment: layout.Middle,
					}.Layout2(gtx, func(gtx C) D {
						return layout.Inset{Bottom: values.MarginPadding4}.Layout(gtx, func(gtx C) D {
							msg := values.String(values.StrAutoTicketInfo)
							txt := pg.Theme.Label(values.TextSize14, msg)
							txt.Alignment = text.Middle
							txt.Color = pg.Theme.Color.GrayText3
							if pg.AssetsManager.IsDarkModeOn() {
								txt.Color = pg.Theme.Color.Gray3
							}
							return txt.Layout(gtx)
						})
					})
				}),
			)
		}).
		SetNegativeButtonCallback(func() {
			_ = pg.dcrWallet.StopAutoTicketsPurchase()
			pg.stake.SetChecked(false)
		}).
		SetPositiveButtonCallback(func(_, password string, pm *modal.CreatePasswordModal) bool {
			pg.stake.SetChecked(false)

			if !pg.dcrWallet.IsConnectedToNetwork() {
				pm.SetError(values.String(values.StrNotConnected))
				_ = pg.dcrWallet.StopAutoTicketsPurchase() // Halt auto tickets purchase.
				return false
			}

			if err := pg.dcrWallet.StartTicketBuyer(password); err != nil {
				pm.SetError(err.Error())
				_ = pg.dcrWallet.StopAutoTicketsPurchase() // Halt auto tickets purchase.
				return false
			}

			pg.stake.SetChecked(pg.dcrWallet.IsAutoTicketsPurchaseActive())
			pg.ParentWindow().Reload()
			pm.Dismiss()

			// The "balance to maintain" field is a RESERVE, not the spend
			// amount — the auto-buyer spends (spendable − reserve) on tickets,
			// one ticket at the current ticket price. If the wallet can't afford
			// even one ticket, enabling auto-buy otherwise looks like it did
			// nothing (the page just shows "Можна купити 0"). Say so explicitly;
			// the buyer keeps running and will purchase once the wallet is funded.
			if pg.CalculateTotalTicketsCanBuy() < 1 {
				infoModal := modal.NewCustomModal(pg.Load).
					Title(values.String(values.StrAutoTicketPurchase)).
					Body(values.StringF(values.StrAutoTicketInsufficient, pg.ticketPrice)).
					SetPositiveButtonText(values.String(values.StrGotIt))
				pg.ParentWindow().ShowModal(infoModal)
			}

			return true
		})
	pg.ParentWindow().ShowModal(walletPasswordModal)
}

// OnNavigatedFrom is called when the page is about to be removed from
// the displayed window. This method should ideally be used to disable
// features that are irrelevant when the page is NOT displayed.
// NOTE: The page may be re-displayed on the app's window, in which case
// OnNavigatedTo() will be called again. This method should not destroy UI
// components unless they'll be recreated in the OnNavigatedTo() method.
// Part of the load.Page interface.
func (pg *Page) OnNavigatedFrom() {
	pg.stopTxNotificationsListener()
}
