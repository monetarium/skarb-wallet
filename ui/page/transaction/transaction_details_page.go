package transaction

import (
	"fmt"
	"image"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"github.com/monetarium/skarb-wallet/app"
	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/libwallet/txhelper"
	libutils "github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/skarb-wallet/ui/cryptomaterial"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/modal"
	"github.com/monetarium/skarb-wallet/ui/page/components"
	pageutils "github.com/monetarium/skarb-wallet/ui/utils"
	"github.com/monetarium/skarb-wallet/ui/values"
)

const (
	TransactionDetailsPageID = "TransactionDetails"
	viewBlockID              = "viewBlock"
)

type transactionWdg struct {
	confirmationIcons    *cryptomaterial.Image
	time, status, wallet cryptomaterial.Label

	copyTextButtons []*cryptomaterial.Clickable
	txStatus        *components.TxStatus
}

type moreItem struct {
	text   string
	id     string
	button *cryptomaterial.Clickable
}

type TxDetailsPage struct {
	*load.Load
	// GenericPageModal defines methods such as ID() and OnAttachedToNavigator()
	// that helps this Page satisfy the app.Page interface. It also defines
	// helper methods for accessing the PageNavigator that displayed this page
	// and the root WindowNavigator.
	*app.GenericPageModal

	list                        *widget.List
	transactionInputsContainer  layout.List
	transactionOutputsContainer layout.List

	destAddressClickables []*cryptomaterial.Clickable
	// senderAddressClickables backs the click-to-copy on the "From" panel
	// for received transactions. One stable Clickable per unique sender
	// address (derived from input sigScripts in TxInput.SenderAddress).
	// Must be a long-lived field — creating a fresh Clickable inside the
	// layout callback throws away the click state Gio recorded on the
	// previous input frame, which is exactly why the original
	// layoutSenderAddressList shipped non-copying rows.
	senderAddressClickables   []*cryptomaterial.Clickable
	senderAddresses           []string // mirrors senderAddressClickables (same length, same order)
	associatedTicketClickable *cryptomaterial.Clickable
	hashClickable             *cryptomaterial.Clickable
	rebroadcastClickable      *cryptomaterial.Clickable
	moreOption                *cryptomaterial.Clickable
	outputsCollapsible        *cryptomaterial.Collapsible
	inputsCollapsible         *cryptomaterial.Collapsible
	txLabelCollapsible        *cryptomaterial.Collapsible
	dot                       *cryptomaterial.Icon
	rebroadcastIcon           *cryptomaterial.Image
	shadowBox                 *cryptomaterial.Shadow

	backButton  cryptomaterial.IconButton
	rebroadcast cryptomaterial.Label

	copyURLBtn *cryptomaterial.Clickable

	transaction   *sharedW.Transaction
	ticketSpender *sharedW.Transaction // vote or revoke ticket
	ticketSpent   *sharedW.Transaction // ticket spent in a vote or revoke
	txBackStack   *sharedW.Transaction // track original transaction
	wallet        sharedW.Asset

	moreItems  []moreItem
	txnWidgets transactionWdg

	txSourceAccount, txDestinationAccount string
	txDestinationAddresses                []string
	title                                 string
	vspHost                               string
	vspHostFees                           string
	// staged*/pendingVSPRefresh hand VSP info from the fetch goroutine
	// (OnNavigatedTo) to the UI thread, where vspHost/vspHostFees are read by
	// Layout. Staking is removed in v1 so this path is dormant, but the code is
	// live and must not write UI-read fields off the UI thread.
	stagedVSPHost     string
	stagedVSPHostFees string
	pendingVSPRefresh atomic.Bool

	moreOptionIsOpen bool

	// pendingTxRefresh is set by the block/confirmation notification
	// listener (a goroutine) and consumed on the UI thread in
	// HandleUserInteractions. The notification only flags that something
	// changed; the actual re-fetch of pg.transaction (whose BlockHeight is
	// otherwise a stale snapshot — -1 forever for a tx confirmed while this
	// page is open) and the rebuild of the cached status/icon widgets happen
	// on the UI thread to avoid a data race with Layout (see CLAUDE.md §3).
	pendingTxRefresh atomic.Bool

	// rebroadcastTimes records the wall-clock time of each accepted
	// rebroadcast click. Pressing Rebroadcast too often makes peers flag
	// the wallet as abusive and ban it for ~24h, so we cap it to
	// maxRebroadcastsPerWindow within a rolling rebroadcastWindow. Entries
	// older than the window are pruned on each click.
	rebroadcastTimes []time.Time
}

const (
	// maxRebroadcastsPerWindow / rebroadcastWindow bound how often the user
	// may re-publish unmined transactions before peers would consider the
	// behaviour abusive (and ban the wallet for ~24h).
	maxRebroadcastsPerWindow = 3
	rebroadcastWindow        = time.Hour
)

func NewTransactionDetailsPage(l *load.Load, wallet sharedW.Asset, transaction *sharedW.Transaction) *TxDetailsPage {
	rebroadcast := l.Theme.Label(values.TextSize14, values.String(values.StrRebroadcast))
	rebroadcast.TextSize = values.TextSize14
	rebroadcast.Color = l.Theme.Color.Text
	pg := &TxDetailsPage{
		Load:             l,
		GenericPageModal: app.NewGenericPageModal(TransactionDetailsPageID),
		list: &widget.List{
			List: layout.List{Axis: layout.Vertical},
		},
		transactionInputsContainer: layout.List{
			Axis: layout.Vertical,
		},
		transactionOutputsContainer: layout.List{
			Axis: layout.Vertical,
		},

		outputsCollapsible: l.Theme.Collapsible(),
		inputsCollapsible:  l.Theme.Collapsible(),
		txLabelCollapsible: l.Theme.Collapsible(),

		copyURLBtn: l.Theme.NewClickable(false),

		associatedTicketClickable: l.Theme.NewClickable(true),
		hashClickable:             l.Theme.NewClickable(true),
		destAddressClickables:     make([]*cryptomaterial.Clickable, 0),
		moreOption:                l.Theme.NewClickable(false),
		shadowBox:                 l.Theme.Shadow(),

		transaction:            transaction,
		wallet:                 wallet,
		rebroadcast:            rebroadcast,
		rebroadcastClickable:   l.Theme.NewClickable(true),
		rebroadcastIcon:        l.Theme.Icons.Rebroadcast,
		txDestinationAddresses: make([]string, 0),
	}

	// Splits are priced (Amount = outputs the tickets consumed) by the list
	// fetches; a details page can also be reached with an unpriced row (the
	// stored Amount is the fee), so re-derive here. No-op for non-splits and
	// rows the list already priced.
	ensureSplitAmount(wallet, transaction)

	// Materialize sender-address clickables. See refreshSenderClickables
	// for the rebuild contract — must also fire whenever pg.transaction
	// is reassigned (ticket-spender navigation, back-stack).
	pg.refreshSenderClickables()

	pg.backButton = components.GetBackButton(pg.Load)

	pg.dot = cryptomaterial.NewIcon(l.Theme.Icons.DotIcon)
	pg.dot.Color = l.Theme.Color.Gray1

	pg.moreItems = pg.getMoreItem()

	return pg
}

func (pg *TxDetailsPage) getTXSourceAccountAndDirection() {
	// find source account
	for _, input := range pg.transaction.Inputs {
		if input.AccountNumber != -1 {
			// One line, one wallet. The legacy code showed
			// `accountName` (literally "default" for any
			// freshly-created wallet) which was meaningless. A previous
			// attempt rendered "WalletName\nAccountName" on two lines
			// but the account name is noise — multi-account wallets
			// are not a v1 feature and the default-named account just
			// duplicates information. Showing the wallet name alone is
			// what the user actually wants ("from this wallet").
			// AccountNumber is still consulted to verify we picked an
			// input the wallet owns; we just don't surface the name.
			if _, err := pg.wallet.AccountName(input.AccountNumber); err != nil {
				log.Error(err)
			}
			pg.txSourceAccount = pg.wallet.GetWalletName()
			break
		}
	}

	// find destination address
destinationAddrLoop:
	for _, output := range pg.transaction.Outputs {
		switch pg.transaction.Direction {
		case txhelper.TxDirectionSent:
			// mixed account number
			var mixedAcc int32 = -1
			txDestinationAddress := ""
			if libutils.DCRWalletAsset == pg.wallet.GetAssetType() {
				mixedAcc = pg.wallet.(*dcr.Asset).MixedAccountNumber()
			}
			if pg.transaction.Type == txhelper.TxTypeMixed {
				if output.AccountNumber == -1 {
					txDestinationAddress = output.Address
				}
				if output.AccountNumber == mixedAcc {
					accountName, err := pg.wallet.AccountName(output.AccountNumber)
					if err != nil {
						log.Error(err)
					} else {
						txDestinationAddress = accountName
					}
				}
				if txDestinationAddress == "" {
					continue
				}
				pg.destAddressClickables = append(pg.destAddressClickables, pg.Theme.NewClickable(true))
				pg.txDestinationAddresses = append(pg.txDestinationAddresses, txDestinationAddress)
				break destinationAddrLoop
			}

			if output.AccountNumber == -1 {
				txDestinationAddress = output.Address
			}

			if txDestinationAddress != "" {
				pg.destAddressClickables = append(pg.destAddressClickables, pg.Theme.NewClickable(true))
				pg.txDestinationAddresses = append(pg.txDestinationAddresses, txDestinationAddress)
			}
		case txhelper.TxDirectionReceived:
			if output.AccountNumber != -1 {
				// Wallet name only — same rationale as txSourceAccount.
				// "default" account name is noise; one line, one wallet.
				if _, err := pg.wallet.AccountName(output.AccountNumber); err != nil {
					log.Error(err)
				}
				pg.txDestinationAddresses = append(pg.txDestinationAddresses, pg.wallet.GetWalletName())
				break destinationAddrLoop
			}
		case txhelper.TxDirectionTransferred:
			if output.AccountNumber != -1 {
				// Wallet name only — see txSourceAccount comment.
				if _, err := pg.wallet.AccountName(output.AccountNumber); err != nil {
					log.Error(err)
				}
				pg.txDestinationAccount = pg.wallet.GetWalletName()
				break destinationAddrLoop
			}
		}
	}
}

// OnNavigatedTo is called when the page is about to be displayed and
// may be used to initialize page features that are only relevant when
// the page is displayed.
// Part of the load.Page interface.
func (pg *TxDetailsPage) OnNavigatedTo() {
	// Hook block-attached notifications so the "Confirmation Status"
	// row (rendered via txConfirmations() = bestBlock - tx.BlockHeight)
	// refreshes every time the wallet's tip advances, without the user
	// having to navigate away and back. Detached in OnNavigatedFrom.
	pg.wireBlockListener()

	if dcrImp, ok := pg.wallet.(*dcr.Asset); ok {
		// this tx is a vote transaction
		if pg.transaction.TicketSpentHash != "" {
			pg.ticketSpent, _ = pg.wallet.GetTransactionRaw(pg.transaction.TicketSpentHash)
		}

		if ok, _ := dcrImp.TicketHasVotedOrRevoked(pg.transaction.Hash); ok {
			pg.ticketSpender, _ = dcrImp.TicketSpender(pg.transaction.Hash)
		}

		if pg.wallet.TxMatchesFilter(pg.transaction, libutils.TxFilterStaking) {
			// Defaults set on the UI thread (OnNavigatedTo); the goroutine
			// publishes its result via staged fields + pendingVSPRefresh so it
			// never writes vspHost/vspHostFees (read by Layout) off-thread.
			pg.vspHost = values.String(values.StrNotAvailable)
			pg.vspHostFees = values.String(values.StrNotAvailable)
			go func() {
				host := values.String(values.StrNotAvailable)
				fees := values.String(values.StrNotAvailable)

				var feeTxHash string
				info, err := dcrImp.VSPTicketInfo(pg.transaction.Hash)
				if info != nil {
					host = info.VSP
					feeTxHash = info.FeeTxHash
				}
				if err != nil {
					if err.Error() != libutils.ErrWalletLocked {
						// Ignore the wallet is locked error.
						log.Errorf("VSPTicketInfo error: %v", err)
					}
					pg.publishVSPInfo(host, fees)
					return
				}

				if feeTxHash != "" {
					feeTx, ferr := pg.wallet.GetTransactionRaw(feeTxHash)
					if feeTx != nil {
						fees = pg.wallet.ToAmount(feeTx.Amount).String()
					}
					if ferr != nil {
						log.Errorf("GetTransactionRaw error: %v", ferr)
					}
				}
				pg.publishVSPInfo(host, fees)
			}()
		}
	}

	pg.title = values.String(values.StrTransactionDetails)
	if pg.transaction.Type == values.String(values.StrTicket) {
		pg.title = values.String(values.StrTicketDetails)
	}

	pg.getTXSourceAccountAndDirection()
	pg.txnWidgets = pg.initTxnWidgets()
}

func (pg *TxDetailsPage) getMoreItem() []moreItem {
	return []moreItem{
		{
			text:   values.String(values.StrViewOnExplorer),
			button: pg.Theme.NewClickable(true),
			id:     viewBlockID,
		},
	}
}

// Layout draws the page UI components into the provided layout context
// to be eventually drawn on screen.
// Part of the load.Page interface.
func (pg *TxDetailsPage) Layout(gtx C) D {
	body := func(gtx C) D {
		sp := components.SubPage{
			Load:       pg.Load,
			Title:      pg.title,
			BackButton: pg.backButton,
			ExtraItem:  pg.moreOption,
			Extra: func(gtx C) D {
				return layout.E.Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(pg.Theme.NewIcon(pg.Theme.Icons.NavigationMore).Layout24dp),
						layout.Rigid(func(gtx C) D {
							if pg.moreOptionIsOpen {
								pg.layoutOptionsMenu(gtx)
							}
							return D{}
						}),
					)
				})
			},
			Back: func() {
				if pg.txBackStack == nil {
					pg.ParentNavigator().CloseCurrentPage()
					return
				}
				pg.transaction = pg.txBackStack
				pg.getTXSourceAccountAndDirection()
				pg.txnWidgets = pg.initTxnWidgets()
				pg.refreshSenderClickables() // tx reassigned; pool must follow
				pg.txBackStack = nil
				pg.ParentWindow().Reload()
			},
			Body: func(gtx C) D {
				widgets := []func(gtx C) D{
					pg.txnTypeAndID,
					pg.txnInputs,
					pg.txnOutputs,
				}

				return pg.Theme.Card().Layout(gtx, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(pg.txDetailsHeader),
						layout.Rigid(pg.Theme.Separator().Layout),
						layout.Rigid(func(gtx C) D {
							return pg.Theme.List(pg.list).Layout(gtx, len(widgets), func(gtx C, i int) D {
								return layout.Inset{}.Layout(gtx, widgets[i])
							})
						}),
					)
				})
			},
		}

		return sp.CombinedLayout(pg.ParentWindow(), gtx)
	}

	if pg.Load.IsMobileView() {
		return pg.layoutMobile(gtx, body)
	}
	return pg.layoutDesktop(gtx, body)
}

func (pg *TxDetailsPage) layoutDesktop(gtx C, body layout.Widget) D {
	return body(gtx)
}

func (pg *TxDetailsPage) layoutMobile(gtx C, body layout.Widget) D {
	return components.UniformMobile(gtx, false, false, body)
}

func (pg *TxDetailsPage) txDetailsHeader(gtx C) D {
	return cryptomaterial.LinearLayout{
		Width:       cryptomaterial.MatchParent,
		Height:      cryptomaterial.WrapContent,
		Orientation: layout.Horizontal,
		Padding: layout.Inset{
			Left:   values.MarginPadding24,
			Right:  values.MarginPadding24,
			Bottom: values.MarginPadding30,
		},
	}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Inset{
				Right: values.MarginPadding22,
			}.Layout(gtx, pg.txnWidgets.txStatus.Icon.Layout24dp)
		}),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							if pg.transaction.Type == dcr.TxTypeTicketPurchase {
								return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
									layout.Rigid(pg.Theme.Label(values.TextSize16, values.String(values.StrStatus)+": ").Layout),
									layout.Rigid(pg.Theme.Label(values.TextSize16, pg.txnWidgets.txStatus.Title).Layout),
									layout.Rigid(func(gtx C) D {
										if pg.txnWidgets.txStatus.TicketStatus != dcr.TicketStatusImmature {
											return D{}
										}

										// immature tx section
										p := pg.Theme.ProgressBarCircle(pg.maturityProgressPercent())
										p.Color = pg.txnWidgets.txStatus.ProgressBarColor
										return layout.Inset{Left: values.MarginPadding10}.Layout(gtx, func(gtx C) D {
											sz := gtx.Dp(values.MarginPadding22)
											gtx.Constraints.Max = image.Point{X: sz, Y: sz}
											gtx.Constraints.Min = gtx.Constraints.Max
											return p.Layout(gtx)
										})
									}),
								)
							}
							// regular transaction
							col := pg.Theme.Color.GrayText2
							return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
								layout.Rigid(func(gtx C) D {
									// Format the headline amount with the tx's actual coin
									// type — never assume VAR. Without this an SKA receive
									// shows "X.YZ VAR" because dcrutil.Amount.String()
									// hard-codes the VAR suffix. pg.transaction.CoinType
									// (uint8) was set at decode time from outputs[0].CoinType.
									// AmountAtoms is the lossless big.Int decimal string for
									// SKA values that exceed int64 (single UTXO > ~9.22 SKA);
									// FormatTxAmountBig falls back to int64 when empty.
									title := dcr.FormatTxAmountBig(pg.transaction.AmountAtoms, pg.transaction.Amount, pg.transaction.CoinType)
									switch pg.transaction.Type {
									case txhelper.TxTypeMixed:
										title = dcr.FormatTxAmount(pg.transaction.MixDenomination, pg.transaction.CoinType)
									case txhelper.TxTypeRegular:
										if pg.transaction.Direction == txhelper.TxDirectionSent && !strings.Contains(title, "-") {
											title = "-" + title
										}
									case txhelper.TxTypeRevocation, txhelper.TxTypeVote:
										return pg.Theme.Label(values.TextSize20, pg.txnWidgets.txStatus.Title).Layout(gtx)
									}
									return components.LayoutBalanceWithUnit(gtx, pg.Load, title)
								}),
								layout.Rigid(func(gtx C) D {
									date := time.Unix(pg.transaction.Timestamp, 0).Format("2006-01-02")
									timeSplit := time.Unix(pg.transaction.Timestamp, 0).Format("15:04:05")
									dateTime := values.StringF(values.StrDateAtTime, date, timeSplit)

									lbl := pg.Theme.Label(values.TextSize16, dateTime)
									lbl.Color = col
									return layout.Inset{
										Top:    values.MarginPadding7,
										Bottom: values.MarginPadding7,
									}.Layout(gtx, lbl.Layout)
								}),
								layout.Rigid(func(gtx C) D {
									immatureVoteOrRevocation := pg.txnWidgets.txStatus.TicketStatus == dcr.TicketStatusImmature && (pg.transaction.Type == txhelper.TxTypeVote || pg.transaction.Type == txhelper.TxTypeRevocation)
									if !immatureVoteOrRevocation {
										return D{}
									}

									// immature tx section
									title := values.String(values.StrRevoke)
									if pg.transaction.Type == txhelper.TxTypeVote {
										title = values.String(values.StrVote)
									}

									lbl := pg.Theme.Label(values.TextSize16, fmt.Sprintf("%d days to %s", pg.transaction.DaysToVoteOrRevoke, title))
									lbl.Color = col
									return lbl.Layout(gtx)
								}),
							)
						}),
						layout.Rigid(func(gtx C) D {
							col := pg.Theme.Color.GrayText2

							if dcrImpl, ok := pg.wallet.(*dcr.Asset); ok {
								switch pg.txnWidgets.txStatus.TicketStatus {
								case dcr.TicketStatusImmature:
									// "Mature in X of Y blocks (time)": X is the number
									// of blocks left until the ticket goes live (the old
									// code showed a progress PERCENTAGE there), and the
									// time is how long those remaining blocks take (the
									// old code showed the total maturity span, truncated
									// to 0 when the per-block minutes were < 1).
									maturity := dcrImpl.TicketMaturity()
									remaining := pg.getBlocksToMatureOrExpire()
									lbl := pg.Theme.Label(values.TextSize16, values.StringF(values.StrImmatureInfo,
										remaining, maturity, formatBlockSpan(pg.wallet, remaining)))
									lbl.Color = col
									return lbl.Layout(gtx)

								case dcr.TicketStatusLive:
									return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
										layout.Rigid(func(gtx C) D {
											lbl := pg.Theme.Label(values.TextSize16, values.String(values.StrLifeSpan)+": ")
											lbl.Color = col
											return lbl.Layout(gtx)
										}),
										layout.Rigid(func(gtx C) D {
											// "revoke in X of Y blocks (time)": lifetime is
											// measured in BLOCKS (the old template said
											// "days" and put a percentage in X).
											expiry := dcrImpl.TicketExpiry()
											remaining := pg.getBlocksToMatureOrExpire()
											lbl := pg.Theme.Label(values.TextSize16, values.StringF(values.StrLiveInfoDisc,
												remaining, expiry, formatBlockSpan(pg.wallet, remaining)))
											lbl.Color = col
											return lbl.Layout(gtx)
										}),
									)

								case dcr.TicketStatusVotedOrRevoked:
									if pg.ticketSpender == nil || pg.ticketSpender.Type != txhelper.TxTypeVote {
										return D{}
									}

									// voted or revoked
									return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
										layout.Rigid(func(gtx C) D {
											lbl := pg.Theme.Label(values.TextSize16, values.String(values.StrReward)+": ")
											lbl.Color = col
											return lbl.Layout(gtx)
										}),
										layout.Rigid(func(gtx C) D {
											lbl := pg.Theme.Label(values.TextSize16, pg.wallet.ToAmount(pg.ticketSpender.VoteReward).String())
											lbl.Color = col
											return lbl.Layout(gtx)
										}),
									)
								}
							}
							return D{}
						}),
						layout.Rigid(func(gtx C) D {
							if pg.transaction.BlockHeight == -1 {
								if !pg.rebroadcastClickable.Enabled() {
									gtx = pg.rebroadcastClickable.SetEnabled(false, &gtx)
								}
								return cryptomaterial.LinearLayout{
									Width:     cryptomaterial.WrapContent,
									Height:    cryptomaterial.WrapContent,
									Clickable: pg.rebroadcastClickable,
									Direction: layout.Center,
									Alignment: layout.Middle,
									Border: cryptomaterial.Border{
										Color:  pg.Theme.Color.Gray2,
										Width:  values.MarginPadding1,
										Radius: cryptomaterial.Radius(10),
									},
									Padding: layout.Inset{
										Top:    values.MarginPadding3,
										Bottom: values.MarginPadding3,
										Left:   values.MarginPadding8,
										Right:  values.MarginPadding8,
									},
									Margin: layout.Inset{Left: values.MarginPadding10},
								}.Layout(gtx,
									layout.Rigid(func(gtx C) D {
										return layout.Inset{Right: values.MarginPadding4}.Layout(gtx, pg.rebroadcastIcon.Layout16dp)
									}),
									layout.Rigid(pg.rebroadcast.Layout),
								)
							}
							return D{}
						}),
					)
				}),
			)
		}),
	)
}

// maturityProgressPercent returns how far an immature ticket has progressed
// toward going live, as 0-100 for the circular progress widget.
func (pg *TxDetailsPage) maturityProgressPercent() int {
	dcrImpl, ok := pg.wallet.(*dcr.Asset)
	if !ok {
		return 0
	}
	maturity := dcrImpl.TicketMaturity()
	if maturity <= 0 {
		return 0
	}
	confs := dcr.Confirmations(pg.wallet.GetBestBlockHeight(), pg.transaction)
	p := int(float32(confs) / float32(maturity) * 100)
	if p > 100 {
		p = 100
	}
	if p < 0 {
		p = 0
	}
	return p
}

// getBlocksToMatureOrExpire returns how many blocks remain until the ticket
// leaves its current stage: until it goes live for an immature ticket, until
// it expires (becomes revocable) for a live one. Mirrors the boundaries of
// dcr.TicketStatus — immature while confs <= maturity, expired once
// confs > maturity+expiry — so the count reaches 0 exactly when the status
// flips.
func (pg *TxDetailsPage) getBlocksToMatureOrExpire() int32 {
	dcrImpl, ok := pg.wallet.(*dcr.Asset)
	if !ok {
		return 0
	}
	confs := dcr.Confirmations(pg.wallet.GetBestBlockHeight(), pg.transaction)
	remaining := dcrImpl.TicketMaturity() + 1 - confs
	if pg.txnWidgets.txStatus.TicketStatus == dcr.TicketStatusLive {
		remaining = dcrImpl.TicketMaturity() + dcrImpl.TicketExpiry() + 1 - confs
	}
	if remaining < 0 {
		remaining = 0
	}
	return remaining
}

// formatBlockSpan renders how long `blocks` take on this chain as a compact
// human duration ("8d 12h", "1h 28m", "32m"). Uses the chain's target
// per-block time without integer truncation (the old code cast the per-block
// minutes to int32, which is 0 on sub-minute chains).
func formatBlockSpan(wallet sharedW.Asset, blocks int32) string {
	d := time.Duration(float64(blocks) * wallet.TargetTimePerBlockMinutes() * float64(time.Minute))
	d = d.Round(time.Minute)
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	mins := d % time.Hour / time.Minute
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

func (pg *TxDetailsPage) keyValue(gtx C, key string, value layout.Widget) D {
	return layout.Inset{Bottom: values.MarginPadding10}.Layout(gtx, func(gtx C) D {
		return layout.Flex{}.Layout(gtx,
			layout.Flexed(.4, func(gtx C) D {
				return layout.Inset{Right: values.MarginPadding35}.Layout(gtx, func(gtx C) D {
					lbl := pg.Theme.Label(values.TextSize14, key)
					lbl.Color = pg.Theme.Color.GrayText2
					return lbl.Layout(gtx)
				})
			}),
			layout.Flexed(.6, value),
		)
	})
}

// TODO: do this at startup
func (pg *TxDetailsPage) txConfirmations() int32 {
	transaction := pg.transaction
	if transaction.BlockHeight != -1 {
		return (pg.wallet.GetBestBlockHeight() - transaction.BlockHeight) + 1
	}

	return 0
}

func (pg *TxDetailsPage) txnTypeAndID(gtx C) D {
	reqConf := pg.wallet.RequiredConfirmations()
	transaction := pg.transaction
	return cryptomaterial.LinearLayout{
		Width:       cryptomaterial.MatchParent,
		Height:      cryptomaterial.WrapContent,
		Orientation: layout.Vertical,
		Padding: layout.Inset{
			Top:   values.MarginPadding30,
			Left:  values.MarginPadding70,
			Right: values.MarginPadding24,
		},
	}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			// For RECEIVED transactions, "From" = sender address(es) derived
			// from each input's sigScript (which reveals the spender's
			// secp256k1 pubkey, hashed to a P2PKH address). The legacy code
			// hid this block entirely on receive, but Skarb extracts the
			// sender address at decode time (TxInput.SenderAddress) so we
			// can finally show a meaningful "From" panel. Distinct addresses
			// only — a multi-input tx from one wallet usually re-signs from
			// one address.
			if pg.transaction.Type == txhelper.TxTypeRegular && pg.transaction.Direction == txhelper.TxDirectionReceived {
				if len(pg.senderAddresses) == 0 {
					return D{} // no resolvable P2PKH inputs; nothing useful to display
				}
				return pg.keyValue(gtx, values.String(values.StrFrom), func(gtx C) D {
					return layoutSenderAddressList(gtx, pg)
				})
			}

			label := values.String(values.StrFrom)
			if pg.transaction.Type == txhelper.TxTypeTicketPurchase {
				label = values.String(values.StrAccount)
			}
			return pg.keyValue(gtx, label, pg.Theme.Label(values.TextSize14, pg.txSourceAccount).Layout)
		}),
		layout.Rigid(func(gtx C) D {
			if pg.transaction.Type == txhelper.TxTypeRegular || pg.transaction.Type == txhelper.TxTypeMixed {
				dim := func(gtx C) D {
					if pg.transaction.Direction == txhelper.TxDirectionReceived {
						lbl := pg.Theme.Label(values.TextSize14, pageutils.SplitSingleString(pg.txDestinationAddresses[0], 0))
						return lbl.Layout(gtx)
					}
					flexChilds := make([]layout.FlexChild, 0)
					for i := range pg.txDestinationAddresses {
						address := pg.txDestinationAddresses[i]
						clickable := pg.destAddressClickables[i]
						flexChilds = append(flexChilds, layout.Rigid(func(gtx C) D {
							// copy destination Address
							if clickable.Clicked(gtx) {
								gtx.Execute(clipboard.WriteCmd{Data: io.NopCloser(strings.NewReader(address))})
								pg.Toast.Notify(values.String(values.StrTxHashCopied))
							}
							lbl := pg.Theme.Label(values.TextSize14, pageutils.SplitSingleString(address, 0))
							lbl.Color = pg.Theme.Color.Primary
							return clickable.Layout(gtx, lbl.Layout)
						}))
						flexChilds = append(flexChilds, layout.Rigid(layout.Spacer{Height: values.MarginPadding5}.Layout))
					}

					return layout.Flex{Axis: layout.Vertical}.Layout(gtx, flexChilds...)
				}
				// if transaction is transferred, show the destination account
				// without being wrapped in a clickable
				if pg.transaction.Direction == txhelper.TxDirectionTransferred {
					dim = pg.Theme.Label(values.TextSize14, pg.txDestinationAccount).Layout
				}

				return pg.keyValue(gtx, values.String(values.StrTo), dim)
			}
			return D{}
		}),
		layout.Rigid(func(gtx C) D {
			// hide this section for sent, received and mixed transaction
			if pg.transaction.Type == txhelper.TxTypeRegular &&
				pg.transaction.Direction == txhelper.TxDirectionSent ||
				pg.transaction.Direction == txhelper.TxDirectionReceived ||
				pg.transaction.Direction == txhelper.TxDirectionTransferred ||
				pg.transaction.Type == txhelper.TxTypeMixed {
				return D{}
			}

			amount := pg.wallet.ToAmount(pg.transaction.Amount).String()
			if pg.transaction.Type == txhelper.TxTypeMixed {
				amount = pg.wallet.ToAmount(pg.transaction.MixDenomination).String()
			} else if pg.transaction.Type == txhelper.TxTypeRegular && pg.transaction.Direction == txhelper.TxDirectionSent {
				amount = "-" + amount
			}
			return pg.keyValue(gtx, values.String(values.StrTicketPrice), pg.Theme.Label(values.TextSize14, amount).Layout)
		}),
		layout.Rigid(func(gtx C) D {
			// revocation and vote transaction reward
			if pg.transaction.Type == txhelper.TxTypeVote {
				return pg.keyValue(gtx, values.String(values.StrReward), pg.Theme.Label(values.TextSize14, pg.wallet.ToAmount(pg.transaction.VoteReward).String()).Layout)
			}
			return D{}
		}),
		layout.Rigid(func(gtx C) D {
			if transaction.BlockHeight != -1 {
				return pg.keyValue(gtx, values.String(values.StrIncludedInBlock), pg.Theme.Label(values.TextSize14, fmt.Sprintf("%d", transaction.BlockHeight)).Layout)
			}
			return D{}
		}),
		layout.Rigid(func(gtx C) D {
			// hide section for tickets
			if pg.transaction.Type == txhelper.TxTypeTicketPurchase {
				return D{}
			}
			// A split keeps consensus Type "Regular"; surface the
			// classification so it doesn't read as a plain transfer.
			typeText := pg.transaction.Type
			if dcr.IsSplitTx(pg.transaction) {
				typeText = fmt.Sprintf("%s (%s)", typeText, values.String(values.StrSplit))
			}
			return pg.keyValue(gtx, values.String(values.StrType), pg.Theme.Label(values.TextSize14, typeText).Layout)
		}),
		layout.Rigid(func(gtx C) D {
			// hide section for non ticket transactions
			if pg.transaction.Type != txhelper.TxTypeTicketPurchase {
				return D{}
			}

			if pg.ticketSpender != nil { // voted or revoked
				if pg.ticketSpender.Type == txhelper.TxTypeVote {
					return pg.keyValue(gtx, values.String(values.StrVotedOn), pg.Theme.Label(values.TextSize14, timeString(pg.ticketSpender.Timestamp)).Layout)
				} else if pg.ticketSpender.Type == txhelper.TxTypeRevocation {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							return pg.keyValue(gtx, values.String(values.StrMissedOn), pg.Theme.Label(values.TextSize14, timeString(pg.ticketSpender.Timestamp)).Layout)
						}),
						layout.Rigid(func(gtx C) D {
							return pg.keyValue(gtx, values.String(values.StrRevokeCause), pg.Theme.Label(values.TextSize14, values.String(values.StrMissedTickets)).Layout)
						}),
					)
				}
			}

			if pg.wallet.TxMatchesFilter(pg.transaction, libutils.TxFilterExpired) {
				return pg.keyValue(gtx, values.String(values.StrExpiredOn), pg.Theme.Label(values.TextSize14, timeString(pg.transaction.Timestamp)).Layout)
			}

			// TODO vote transaction progress bar (V2 UI missing)
			// missed tickets currently not implemented on libwallet
			return pg.keyValue(gtx, values.String(values.StrPurchasedOn), pg.Theme.Label(values.TextSize14, timeString(pg.transaction.Timestamp)).Layout)
		}),
		layout.Rigid(func(gtx C) D {
			stat := func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						return layout.Inset{Right: values.MarginPadding4}.Layout(gtx, pg.txnWidgets.confirmationIcons.Layout12dp)
					}),
					layout.Rigid(func(gtx C) D {
						// Title-casing the localised "%d з %d підтверджень"
						// string turns "з" (Cyrillic Zhe) into "З", which
						// reads as digit 3 next to the number — "1 З 2"
						// looks like "1 3 2". Render the localised string
						// as-is; localisations choose their own casing
						// upstream in localizable/{uk,en,…}.go.
						txt := pg.Theme.Body2("")
						if pg.txConfirmations() == 0 {
							txt.Text = values.String(values.StrUnconfirmedTx)
							txt.Color = pg.Theme.Color.GrayText2
						} else if pg.txConfirmations() >= reqConf {
							txt.Text = values.String(values.StrConfirmed)
							txt.Color = pg.Theme.Color.Success
						} else {
							txt.Text = values.StringF(values.StrTxStatusPending, pg.txConfirmations(), reqConf)
							txt.Color = pg.Theme.Color.GrayText2
						}
						return txt.Layout(gtx)
					}),
					layout.Rigid(func(gtx C) D {
						if pg.txConfirmations() >= reqConf {
							m := values.MarginPadding10
							return layout.Inset{
								Left:  m,
								Right: m,
							}.Layout(gtx, func(gtx C) D {
								return pg.dot.Layout(gtx, values.MarginPadding6)
							})
						}
						return D{}
					}),
					layout.Rigid(func(gtx C) D {
						if pg.txConfirmations() >= reqConf {
							txt := pg.Theme.Body2(values.StringF(values.StrNConfirmations, pg.txConfirmations()))
							txt.Color = pg.Theme.Color.GrayText2
							return txt.Layout(gtx)
						}
						return D{}
					}),
				)
			}

			return pg.keyValue(gtx, values.String(values.StrConfStatus), stat)
		}),
		layout.Rigid(func(gtx C) D {
			if pg.wallet.GetAssetType() == libutils.BTCWalletAsset && transaction.Direction == txhelper.TxDirectionReceived {
				return D{}
			}
			// Fee is paid in the same coin as the transaction (Monetarium consensus rule),
			// so format it under the tx's CoinType — not always as VAR. For SKA
			// FeeAtoms carries the lossless big.Int decimal; otherwise we fall
			// back to the int64 Fee (exact for VAR; clamped for very large SKA).
			return pg.keyValue(gtx, values.String(values.StrTxFee), pg.Theme.Label(values.TextSize14, dcr.FormatTxAmountBig(transaction.FeeAtoms, transaction.Fee, transaction.CoinType)).Layout)
		}),
		layout.Rigid(func(gtx C) D {
			// hide section for non ticket transactions
			if pg.transaction.Type != txhelper.TxTypeTicketPurchase {
				return D{}
			}

			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return pg.keyValue(gtx, values.String(values.StrVsp), pg.Theme.Label(values.TextSize14, pg.vspHost).Layout)
				}),
				layout.Rigid(func(gtx C) D {
					return pg.keyValue(gtx, values.String(values.StrVspFee), pg.Theme.Label(values.TextSize14, pg.vspHostFees).Layout)
				}),
			)
		}),
		layout.Rigid(func(gtx C) D {
			dim := func(gtx C) D {
				lbl := pg.Theme.Label(values.TextSize14, pageutils.SplitSingleString(transaction.Hash, 30))
				lbl.Color = pg.Theme.Color.Primary

				// copy transaction hash
				if pg.hashClickable.Clicked(gtx) {
					gtx.Execute(clipboard.WriteCmd{Data: io.NopCloser(strings.NewReader(pg.transaction.Hash))})
					pg.Toast.Notify(values.String(values.StrTxHashCopied))
				}
				return pg.hashClickable.Layout(gtx, lbl.Layout)
			}
			return pg.keyValue(gtx, values.String(values.StrTransactionID), dim)
		}),
		layout.Rigid(func(gtx C) D {
			if len(pg.transaction.Label) != 0 {
				txlabel := pg.Theme.Label(values.TextSize14, pg.transaction.Label)
				return pg.keyValue(gtx, values.String(values.StrDescriptionNote), txlabel.Layout)
			}
			return D{}
		}),
	)
}

func (pg *TxDetailsPage) txnInputs(gtx C) D {
	transaction := pg.transaction

	collapsibleHeader := func(gtx C) D {
		t := pg.Theme.Label(values.TextSize14, values.StringF(values.StrXInputsConsumed, len(transaction.Inputs)))
		t.Color = pg.Theme.Color.GrayText2
		return t.Layout(gtx)
	}

	collapsibleBody := func(gtx C) D {
		return pg.transactionInputsContainer.Layout(gtx, len(transaction.Inputs), func(gtx C, i int) D {
			input := transaction.Inputs[i]
			// Prefer the resolved P2PKH sender address over the raw
			// outpoint when we managed to derive it — that's what an
			// end user actually wants to see in a "where did this come
			// from" row. Fall back to outpoint hash:index otherwise.
			line := input.SenderAddress
			if line == "" {
				line = pageutils.SplitSingleString(input.PreviousOutpoint, 20)
			}
			return pg.txnIORow(gtx, input.Amount, input.AmountAtoms, input.AccountNumber, line, i)
		})
	}
	return pg.pageSections(gtx, func(gtx C) D {
		return pg.inputsCollapsible.Layout(gtx, collapsibleHeader, collapsibleBody)
	})
}

func (pg *TxDetailsPage) txnOutputs(gtx C) D {
	transaction := pg.transaction

	collapsibleHeader := func(gtx C) D {
		t := pg.Theme.Label(values.TextSize14, values.StringF(values.StrXOutputCreated, len(transaction.Outputs)))
		t.Color = pg.Theme.Color.GrayText2
		return t.Layout(gtx)
	}

	collapsibleBody := func(gtx C) D {
		x := len(transaction.Inputs)
		return pg.transactionOutputsContainer.Layout(gtx, len(transaction.Outputs), func(gtx C, i int) D {
			output := transaction.Outputs[i]
			return pg.txnIORow(gtx, output.Amount, output.AmountAtoms, output.AccountNumber, output.Address, i+x)
		})
	}
	return pg.pageSections(gtx, func(gtx C) D {
		return pg.outputsCollapsible.Layout(gtx, collapsibleHeader, collapsibleBody)
	})
}

func (pg *TxDetailsPage) txnIORow(gtx C, amount int64, amountAtoms string, acctNum int32, address string, i int) D {
	// accountLabel is the trailing `(...)` annotation next to each I/O row's
	// amount. We show it ONLY when it carries information: an external
	// counterparty (acctNum == -1 → "External") or a non-default named
	// account. For the v1 single-account-per-wallet setup the wallet's
	// account is always literally "default" — the user just told us
	// "1 рядок — 1 гаманець" and stripped "default" from the tx-details
	// header (From / To). Showing "(default)" next to every input and
	// output here reintroduces the same noise inside the collapsible
	// I/O accordions. Hide it; the wallet identity is already in the
	// header rows.
	var accountLabel string
	switch {
	case acctNum == -1:
		accountLabel = fmt.Sprintf("(%s)", values.String(values.StrExternal))
	default:
		name, err := pg.wallet.AccountName(acctNum)
		if err == nil && name != "" && name != "default" {
			accountLabel = fmt.Sprintf("(%s)", name)
		}
		// else: leave empty — "default" account is the implicit one and
		// adding "(default)" is pure noise.
	}

	// Per-input/output row amount also follows the tx's CoinType. All inputs
	// AND outputs of a single Monetarium tx share one CoinType (consensus
	// enforced), so it's safe to take the tx-level value. amountAtoms (the
	// lossless big.Int decimal) takes precedence over the int64 amount
	// when the SKA value would otherwise be clamped at MaxInt64.
	amt := dcr.FormatTxAmountBig(amountAtoms, amount, pg.transaction.CoinType)

	return layout.Inset{Top: values.MarginPadding8}.Layout(gtx, func(gtx C) D {
		card := pg.Theme.Card()
		card.Color = pg.Theme.Color.Gray4
		return card.Layout(gtx, func(gtx C) D {
			return layout.UniformInset(values.MarginPadding15).Layout(gtx, func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						return layout.Flex{}.Layout(gtx,
							layout.Rigid(pg.Theme.Label(values.TextSize14, amt).Layout),
							layout.Rigid(func(gtx C) D {
								if accountLabel == "" {
									return D{}
								}
								m := values.MarginPadding5
								return layout.Inset{
									Left:  m,
									Right: m,
								}.Layout(gtx, pg.Theme.Label(values.TextSize14, accountLabel).Layout)
							}),
						)
					}),
					layout.Rigid(func(gtx C) D {
						// copy address
						if pg.txnWidgets.copyTextButtons[i].Clicked(gtx) {
							gtx.Execute(clipboard.WriteCmd{Data: io.NopCloser(strings.NewReader(address))})
							pg.Toast.Notify(values.String(values.StrCopied))
						}

						return layout.W.Layout(gtx, func(gtx C) D {
							lbl := pg.Theme.Label(values.TextSize14, address)
							lbl.Color = pg.Theme.Color.Primary
							return pg.txnWidgets.copyTextButtons[i].Layout(gtx, lbl.Layout)
						})
					}),
				)
			})
		})
	})
}

func (pg *TxDetailsPage) showbrowserURLModal(copyredirect *cryptomaterial.Clickable) {
	redirectURL := pg.AssetsManager.BlockExplorerURLForTx(pg.wallet.GetAssetType(), pg.transaction.Hash)
	info := modal.NewCustomModal(pg.Load).
		Title(values.String(values.StrViewOnExplorer)).
		Body(values.String(values.StrCopyLink)).
		SetCancelable(true).
		UseCustomWidget(func(gtx C) D {
			return components.BrowserURLWidget(gtx, pg.Load, redirectURL, copyredirect)
		}).
		SetPositiveButtonText(values.String(values.StrGotIt))

	pg.moreOptionIsOpen = false
	pg.ParentWindow().ShowModal(info)
}

func (pg *TxDetailsPage) layoutOptionsMenu(gtx C) {
	inset := layout.Inset{
		Left: values.MarginPaddingMinus145,
	}

	m := op.Record(gtx.Ops)
	inset.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Max.X = gtx.Dp(values.MarginPadding168)
		return pg.shadowBox.Layout(gtx, func(gtx C) D {
			optionsMenuCard := cryptomaterial.Card{Color: pg.Theme.Color.Surface}
			optionsMenuCard.Radius = cryptomaterial.Radius(5)
			return optionsMenuCard.Layout(gtx, func(gtx C) D {
				return (&layout.List{Axis: layout.Vertical}).Layout(gtx, len(pg.moreItems), func(gtx C, i int) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D {
							return pg.moreItems[i].button.Layout(gtx, func(gtx C) D {
								return layout.UniformInset(values.MarginPadding10).Layout(gtx, func(gtx C) D {
									gtx.Constraints.Min.X = gtx.Constraints.Max.X
									return pg.Theme.Label(values.TextSize14, pg.moreItems[i].text).Layout(gtx)
								})
							})
						}),
					)
				})
			})
		})
	})
	op.Defer(gtx.Ops, m.Stop())
}

func (pg *TxDetailsPage) pageSections(gtx C, body layout.Widget) D {
	return layout.Inset{
		Left:   values.MarginPadding70,
		Right:  values.MarginPadding24,
		Bottom: values.MarginPadding10,
	}.Layout(gtx, body)
}

// HandleUserInteractions is called just before Layout() to determine
// if any user interaction recently occurred on the page and may be
// used to update the page's UI components shortly before they are
// displayed.
// Part of the load.Page interface.
// publishVSPInfo stages VSP host/fees fetched on a goroutine and flags the UI
// thread (via pendingVSPRefresh + Reload) to copy them into vspHost/vspHostFees
// in HandleUserInteractions. The atomic Store/CompareAndSwap pair provides the
// happens-before so the staged-field reads are race-free.
func (pg *TxDetailsPage) publishVSPInfo(host, fees string) {
	pg.stagedVSPHost = host
	pg.stagedVSPHostFees = fees
	pg.pendingVSPRefresh.Store(true)
	pg.ParentWindow().Reload()
}

func (pg *TxDetailsPage) HandleUserInteractions(gtx C) {
	// Apply a pending refresh flagged by the block/confirmation listener on
	// the UI thread (see pendingTxRefresh). Re-fetch the transaction so its
	// BlockHeight reflects confirmation, then rebuild the cached status/icon
	// widgets — without this the "Confirmation Status" row stays "Pending"
	// until the user navigates away and back.
	if pg.pendingTxRefresh.CompareAndSwap(true, false) {
		if updated, err := pg.wallet.GetTransactionRaw(pg.transaction.Hash); err == nil && updated != nil {
			// A fresh single-tx decode of a split carries the stored fee as its
			// Amount — re-derive the ticket-consumed sum so the header doesn't
			// silently flip from e.g. "205 VAR" to the fee on the next attached
			// block. If the tickets can't be found, keep the already-priced
			// value from the list that opened this page.
			ensureSplitAmount(pg.wallet, updated)
			if dcr.IsSplitTx(updated) && pg.transaction.Amount > updated.Amount {
				updated.Amount = pg.transaction.Amount
			}
			pg.transaction = updated
			pg.txnWidgets = pg.initTxnWidgets()
			pg.refreshSenderClickables() // tx reassigned; clickable pool must follow
		}
	}

	// Apply VSP info fetched off-thread (see publishVSPInfo) on the UI thread.
	if pg.pendingVSPRefresh.CompareAndSwap(true, false) {
		pg.vspHost = pg.stagedVSPHost
		pg.vspHostFees = pg.stagedVSPHostFees
	}

	for _, item := range pg.moreItems {
		if item.button.Clicked(gtx) {
			switch item.id {
			case viewBlockID: // redirect to browser
				pg.showbrowserURLModal(pg.copyURLBtn)
				pg.moreOptionIsOpen = false
			default:
			}
		}
	}

	if pg.moreOption.Clicked(gtx) {
		pg.moreOptionIsOpen = !pg.moreOptionIsOpen
	}

	if pg.associatedTicketClickable.Clicked(gtx) {
		if pg.ticketSpent != nil {
			pg.txBackStack = pg.transaction
			pg.transaction = pg.ticketSpent
			pg.getTXSourceAccountAndDirection()
			pg.txnWidgets = pg.initTxnWidgets()
			pg.refreshSenderClickables() // tx reassigned; pool must follow
			pg.ParentWindow().Reload()
		}
	}

	if pg.rebroadcastClickable.Clicked(gtx) {
		// Connectivity is checked first, on the UI thread, so a click made
		// while offline neither hits the network nor consumes a rate-limit
		// slot.
		if !pg.wallet.IsConnectedToNetwork() {
			errModal := modal.NewErrorModal(pg.Load, values.String(values.StrNotConnected), modal.DefaultClickFunc())
			pg.ParentWindow().ShowModal(errModal)
			return
		}

		// Rolling rate-limit: drop timestamps older than the window, then
		// refuse the click (with an explanatory modal) if the user has
		// already hit the cap. Spamming Rebroadcast otherwise gets the
		// wallet banned by peers for ~24h.
		now := time.Now()
		kept := pg.rebroadcastTimes[:0]
		for _, t := range pg.rebroadcastTimes {
			if now.Sub(t) < rebroadcastWindow {
				kept = append(kept, t)
			}
		}
		pg.rebroadcastTimes = kept

		if len(pg.rebroadcastTimes) >= maxRebroadcastsPerWindow {
			// Minutes until the OLDEST recorded click ages out of the
			// window — i.e. when a slot frees up again.
			wait := rebroadcastWindow - now.Sub(pg.rebroadcastTimes[0])
			minutes := int(wait.Minutes()) + 1
			limitModal := modal.NewErrorModal(pg.Load,
				values.StringF(values.StrRebroadcastLimitExceeded, minutes),
				modal.DefaultClickFunc())
			pg.ParentWindow().ShowModal(limitModal)
			return
		}
		pg.rebroadcastTimes = append(pg.rebroadcastTimes, now)

		go func() {
			pg.rebroadcastClickable.SetEnabled(false, nil)

			err := pg.wallet.PublishUnminedTransactions()
			if err != nil {
				// If transactions are not published, notify the user
				errModal := modal.NewErrorModal(pg.Load, err.Error(), modal.DefaultClickFunc())
				pg.ParentWindow().ShowModal(errModal)
			} else {
				title := values.StringF(values.StrRepublished, pg.wallet.GetAssetType().ToFull())
				infoModal := modal.NewSuccessModal(pg.Load, title, modal.DefaultClickFunc())
				pg.ParentWindow().ShowModal(infoModal)
			}
			if !pg.rebroadcastClickable.Enabled() {
				pg.rebroadcastClickable.SetEnabled(true, nil)
			}
		}()
	}
}

func (pg *TxDetailsPage) initTxnWidgets() transactionWdg {
	var txn transactionWdg

	t := time.Unix(pg.transaction.Timestamp, 0).UTC()
	txn.time = pg.Theme.Body2(t.Format(time.UnixDate))
	txn.status = pg.Theme.Body1("")
	txn.wallet = pg.Theme.Body2(pg.wallet.GetWalletName())

	if components.TxConfirmations(pg.wallet, pg.transaction) >= pg.wallet.RequiredConfirmations() {
		txn.status.Text = pageutils.FormatDateOrTime(pg.transaction.Timestamp)
		txn.confirmationIcons = pg.Theme.Icons.ConfirmIcon
	} else {
		txn.status.Text = values.String(values.StrPending)
		txn.status.Color = pg.Theme.Color.GrayText2
		txn.confirmationIcons = pg.Theme.Icons.PendingIcon
	}

	txStatus := components.TransactionTitleIcon(pg.Load, pg.wallet, pg.transaction)
	txn.txStatus = txStatus

	x := len(pg.transaction.Inputs) + len(pg.transaction.Outputs)
	txn.copyTextButtons = make([]*cryptomaterial.Clickable, x)
	for i := 0; i < x; i++ {
		txn.copyTextButtons[i] = pg.Theme.NewClickable(false)
	}

	return txn
}

// OnNavigatedFrom is called when the page is about to be removed from
// the displayed window. This method should ideally be used to disable
// features that are irrelevant when the page is NOT displayed.
// NOTE: The page may be re-displayed on the app's window, in which case
// OnNavigatedTo() will be called again. This method should not destroy UI
// components unless they'll be recreated in the OnNavigatedTo() method.
// Part of the load.Page interface.
func (pg *TxDetailsPage) OnNavigatedFrom() {
	pg.wallet.RemoveTxAndBlockNotificationListener(TransactionDetailsPageID)
}

// wireBlockListener registers a TxAndBlockNotificationListener that fires
// the page's Reload on every new block while the tx-details page is on
// screen. The Confirmation Status row's only data dependency is the
// wallet best-block height (subtracted from tx.BlockHeight); without
// this listener the row only refreshes when the user triggers a
// re-layout some other way (typing into a search box, switching tabs).
// AddTxAndBlockNotificationListener returns an error if a listener with
// the same key is already registered — log and continue, the existing
// listener is a re-mount of this page and still fires Reload.
func (pg *TxDetailsPage) wireBlockListener() {
	// Flag a refresh (consumed on the UI thread) AND reload. Reload alone is
	// not enough: pg.transaction is a snapshot whose BlockHeight stays -1 even
	// after the tx confirms, so txConfirmations() = bestBlock - (-1) would
	// never leave "0 / pending". The UI-thread consumer re-fetches the tx so
	// the height (and thus the live confirmation count + status icon) updates.
	listener := &sharedW.TxAndBlockNotificationListener{
		OnBlockAttached: func(_ int, _ int32) {
			pg.pendingTxRefresh.Store(true)
			pg.ParentWindow().Reload()
		},
		OnTransactionConfirmed: func(_ int, _ string, _ int32) {
			pg.pendingTxRefresh.Store(true)
			pg.ParentWindow().Reload()
		},
	}
	if err := pg.wallet.AddTxAndBlockNotificationListener(listener, TransactionDetailsPageID); err != nil {
		log.Errorf("TxDetailsPage: add block listener: %v", err)
	}
}

func timeString(timestamp int64) string {
	return time.Unix(timestamp, 0).Format("2006-01-02 15:04:05")
}

// refreshSenderClickables rebuilds pg.senderAddresses + pg.senderAddressClickables
// against the current pg.transaction.Inputs. Must be called whenever
// pg.transaction is reassigned (constructor, ticket-spender forward navigation,
// back-stack unwind), otherwise:
//   - layoutSenderAddressList copies the wrong address on click, because the
//     pre-existing slice describes the previous transaction.
//   - if the new tx has MORE unique senders than the old, the layout indexer
//     panics with index out of range on pg.senderAddressClickables[i].
//
// One stable Clickable per unique sender survives layout passes — Gio routes
// pointer input to a specific Tag value, so creating fresh Clickables inside
// the layout callback (as the original ad-hoc implementation did) silently
// loses every click.
func (pg *TxDetailsPage) refreshSenderClickables() {
	if pg.transaction == nil {
		pg.senderAddresses = nil
		pg.senderAddressClickables = nil
		return
	}
	pg.senderAddresses = uniqueSenderAddresses(pg.transaction.Inputs)
	pg.senderAddressClickables = make([]*cryptomaterial.Clickable, len(pg.senderAddresses))
	for i := range pg.senderAddressClickables {
		pg.senderAddressClickables[i] = pg.Theme.NewClickable(true)
	}
}

// uniqueSenderAddresses returns the de-duplicated, ordered list of
// SenderAddress fields populated by the tx decoder. A typical received
// transaction has every input signed from the same address, so this collapses
// to one entry; multi-source sends (rare but possible) keep the full list.
// Empty SenderAddress values (non-P2PKH inputs we couldn't resolve) are
// silently skipped. Order matches the input order, which is the order the
// chain stored them in.
func uniqueSenderAddresses(inputs []*sharedW.TxInput) []string {
	seen := make(map[string]struct{}, len(inputs))
	out := make([]string, 0, len(inputs))
	for _, in := range inputs {
		if in == nil || in.SenderAddress == "" {
			continue
		}
		if _, dup := seen[in.SenderAddress]; dup {
			continue
		}
		seen[in.SenderAddress] = struct{}{}
		out = append(out, in.SenderAddress)
	}
	return out
}

// layoutSenderAddressList renders the "From" panel content for a received
// transaction as a SINGLE address row — the first unique sender address.
//
// A sender wallet that funds a tx with multiple of its own UTXOs spends
// from multiple of its own addresses; the chain has no way to attribute
// those back to one logical wallet identity, so we used to render them
// all as a vertical list. That broke the "one row, one sender" mental
// model users actually have ("who sent this?"), and produced an
// unwieldy multi-line block when the sender just happened to consolidate
// a few small UTXOs. We now show the first sender address only —
// representative enough for at-a-glance "who paid me", with the full
// input list still verbatim in the "Inputs consumed" section below for
// users who need the audit trail.
//
// Click-to-copy and ellipsisation behave the same as before.
func layoutSenderAddressList(gtx C, pg *TxDetailsPage) D {
	if len(pg.senderAddresses) == 0 || len(pg.senderAddressClickables) == 0 {
		return D{}
	}
	address := pg.senderAddresses[0]
	clickable := pg.senderAddressClickables[0]
	if clickable.Clicked(gtx) {
		gtx.Execute(clipboard.WriteCmd{Data: io.NopCloser(strings.NewReader(address))})
		pg.Toast.Notify(values.String(values.StrTxHashCopied))
	}
	lbl := pg.Theme.Label(values.TextSize14, ellipsizeMiddle(address, 24))
	lbl.Color = pg.Theme.Color.Primary
	return clickable.Layout(gtx, lbl.Layout)
}

// ellipsizeMiddle returns s if it already fits in maxLen runes, otherwise
// returns a "head…tail" version where head+tail together fit. We don't try
// to be perfectly precise about width — Decred mainnet/testnet P2PKH
// addresses are a fixed 35 chars, so maxLen=24 yields a stable
// "Tsxxxxxxxxxxx…xxxxxxxxxx" shape that fits the details header on the
// usual desktop window without wrapping. The full address still lives in
// the click-to-copy payload and in the inputs section below.
func ellipsizeMiddle(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen || maxLen < 4 {
		return s
	}
	keep := maxLen - 1 // 1 rune for the ellipsis
	head := keep / 2
	tail := keep - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}
