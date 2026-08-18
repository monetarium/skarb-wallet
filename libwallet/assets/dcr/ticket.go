package dcr

import (
	"context"
	"fmt"
	"math/big"
	"runtime/trace"
	"sync"
	"time"

	vspd "github.com/decred/vspd/types/v3"
	"github.com/monetarium/monetarium-node/chaincfg/chainhash"
	"github.com/monetarium/monetarium-node/cointype"
	"github.com/monetarium/monetarium-node/dcrutil"
	"github.com/monetarium/monetarium-wallet/errors"
	w "github.com/monetarium/monetarium-wallet/wallet"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/libwallet/utils"
)

func (asset *Asset) TotalStakingRewards() (int64, error) {
	voteTransactions, err := asset.GetTransactionsRaw(0, 0, TxFilterVoted, true, "")
	if err != nil {
		return 0, err
	}

	var totalRewards int64
	for _, tx := range voteTransactions {
		totalRewards += tx.VoteReward
	}

	return totalRewards, nil
}

// TotalStakingRewardsByCoin sums this wallet's staking earnings per coin.
// VAR — the vote subsidies (VoteReward over voted tickets, the same value
// TotalStakingRewards returns). Each SKA coin — the PoS-side stake-fee
// (SF SSFee) reward transactions, whose Amount is the wallet-owned minted
// reward (outputs − inputs; see decodetx.go). Totals are big.Int because a
// single SKA amount at 1e18 atoms/coin can overflow int64. Coins with no
// earnings are simply absent from the map — the caller decides how to render
// a zero row.
func (asset *Asset) TotalStakingRewardsByCoin() (map[cointype.CoinType]*big.Int, error) {
	totals := make(map[cointype.CoinType]*big.Int)

	voteTxs, err := asset.GetTransactionsRaw(0, 0, TxFilterVoted, true, "")
	if err != nil {
		return nil, err
	}
	varTotal := new(big.Int)
	for _, tx := range voteTxs {
		varTotal.Add(varTotal, big.NewInt(tx.VoteReward))
	}
	totals[cointype.CoinTypeVAR] = varTotal

	// TxFilterRewardPoS also matches votes and revocations (VAR side, already
	// counted above via VoteReward) — keep only the SSFee distributions.
	rewardTxs, err := asset.GetTransactionsRaw(0, 0, TxFilterRewardPoS, true, "")
	if err != nil {
		return nil, err
	}
	for _, tx := range rewardTxs {
		if !tx.IsStakeFee {
			continue
		}
		ct := cointype.CoinType(tx.CoinType)
		amt := big.NewInt(tx.Amount)
		if tx.AmountAtoms != "" {
			if parsed, ok := new(big.Int).SetString(tx.AmountAtoms, 10); ok {
				amt = parsed
			}
		}
		if cur := totals[ct]; cur != nil {
			cur.Add(cur, amt)
		} else {
			totals[ct] = amt
		}
	}
	return totals, nil
}

func (asset *Asset) TicketMaturity() int32 {
	return int32(asset.chainParams.TicketMaturity)
}

func (asset *Asset) TicketExpiry() int32 {
	return int32(asset.chainParams.TicketExpiry)
}

func (asset *Asset) StakingOverview() (stOverview *StakingOverview, err error) {
	stOverview = &StakingOverview{}

	stOverview.Voted, err = asset.CountTransactions(TxFilterVoted)
	if err != nil {
		return nil, err
	}

	stOverview.Revoked, err = asset.CountTransactions(TxFilterRevoked)
	if err != nil {
		return nil, err
	}

	stOverview.Live, err = asset.CountTransactions(TxFilterLive)
	if err != nil {
		return nil, err
	}

	stOverview.Immature, err = asset.CountTransactions(TxFilterImmature)
	if err != nil {
		return nil, err
	}

	stOverview.Expired, err = asset.CountTransactions(TxFilterExpired)
	if err != nil {
		return nil, err
	}

	stOverview.Unmined, err = asset.CountTransactions(TxFilterUnmined)
	if err != nil {
		return nil, err
	}

	stOverview.All = stOverview.Unmined + stOverview.Immature + stOverview.Live + stOverview.Voted +
		stOverview.Revoked + stOverview.Expired

	return stOverview, nil
}

// TicketPrice returns the price of a ticket for the next block, also known as
// the stake difficulty. May be incorrect if blockchain sync is ongoing or if
// blockchain is not up-to-date.
func (asset *Asset) TicketPrice() (*TicketPriceResponse, error) {
	if !asset.WalletOpened() {
		return nil, utils.ErrDCRNotInitialized
	}

	ctx, _ := asset.ShutdownContextWithCancel()
	sdiff, err := asset.Internal().DCR.NextStakeDifficulty(ctx)
	if err != nil {
		return nil, err
	}

	_, tipHeight := asset.Internal().DCR.MainChainTip(ctx)
	resp := &TicketPriceResponse{
		TicketPrice: int64(sdiff),
		Height:      tipHeight,
	}
	return resp, nil
}

// PurchaseTickets purchases tickets from the asset.
// Returns a slice of hashes for tickets purchased.
//
// An empty vspHost requests a direct (solo) purchase: no VSP client is
// created and request.VSPClient stays nil, which the vendored
// wallet.purchaseTickets natively treats as "no fee reservation, no fee
// processing" while still deriving voting rights from VotingAccount. The
// ticket must then be voted by a voting-enabled wallet holding this seed —
// this SPV app itself does not vote.
func (asset *Asset) PurchaseTickets(account, numTickets int32, vspHost, passphrase string, vspPubKey []byte) ([]*chainhash.Hash, error) {
	if !asset.WalletOpened() {
		return nil, utils.ErrDCRNotInitialized
	}

	var vspClient *w.VSPClient
	if vspHost != "" {
		if len(vspPubKey) == 0 {
			return nil, fmt.Errorf("cannot buy a VSP ticket without VSP public key")
		}
		var err error
		vspClient, err = asset.VSPClient(account, vspHost, vspPubKey)
		if err != nil {
			return nil, fmt.Errorf("VSP Server instance failed to start: %v", err)
		}
		// Refuse to buy until we actually have a fee quote from the VSP.
		// Mainnet previously published the ticket anyway and then failed
		// feeaddress (fee > MaxFee), leaving a live ticket with no fee tx.
		ctx, _ := asset.ShutdownContextWithCancel()
		pct, err := vspClient.FeePercentage(ctx)
		if err != nil {
			return nil, fmt.Errorf("cannot fetch VSP fee percentage from %s: %w", vspHost, err)
		}
		if err := asset.checkVSPFeeAgainstMax(pct); err != nil {
			return nil, err
		}
	}

	networkBackend, err := asset.Internal().DCR.NetworkBackend()
	if err != nil {
		return nil, err
	}

	wasLocked := asset.IsLocked()
	err = asset.UnlockWallet(passphrase)
	if err != nil {
		return nil, utils.TranslateError(err)
	}
	if wasLocked {
		defer asset.LockWallet()
	}

	// The VSP fee flow moved from FeePercent/Process callbacks to passing the
	// VSP client directly on the request; the wallet processes the fee
	// internally. CoinShuffle++ mixed split buying is disabled in Skarb (mixer
	// removed), so tickets are purchased directly from the source account.
	request := &w.PurchaseTicketsRequest{
		Count:         int(numTickets),
		SourceAccount: uint32(account),
		ChangeAccount: uint32(account),
		MinConf:       asset.RequiredConfirmations(),
		VSPClient:     vspClient,

		// VotingAccount used to derive addresses for specifying voting rights.
		// It is used when VotingAddress == nil, or Mixing == true
		VotingAccount: uint32(account),
		Mixing:        false,
	}

	ctx, _ := asset.ShutdownContextWithCancel()
	ticketsResponse, err := asset.Internal().DCR.PurchaseTickets(ctx, networkBackend, request)
	// Tickets may already be on-chain when the VSP fee step fails. Keep the
	// hashes so the UI can show "bought N" instead of pretending nothing
	// happened (the previous `if err != nil { return nil, err }` dropped them).
	var hashes []*chainhash.Hash
	if ticketsResponse != nil {
		hashes = ticketsResponse.TicketHashes
	}
	if err != nil && len(hashes) > 0 {
		// Still unlocked here: retry unpaid fees with fresh UTXO
		// selection before the deferred lock (if any) runs.
		if retryErr := asset.ProcessUnpaidVSPTickets(); retryErr != nil {
			log.Warnf("retry unpaid VSP fees after purchase: %v", retryErr)
		}
		return hashes, fmt.Errorf("tickets published but VSP fee failed: %w", err)
	}
	return hashes, err
}

// ErrNoVSPRecord reports that this wallet holds no VSP record for a ticket.
// It means only that: the wallet did not register the ticket with a VSP
// itself. A ticket bought solo looks exactly like one bought through a VSP on
// another device and merely observed here, so callers must not present this
// as "bought solo".
var ErrNoVSPRecord = errors.New("no local vsp record for ticket")

// VSPTicketRecord returns what the wallet database itself knows about a
// ticket's VSP: the host, the fee transaction and whether the VSP confirmed
// it. It reads the database only — no private key, no network — so it works
// on a locked wallet, which is the normal state while browsing.
//
// VSPTicketInfo below answers a different question (what does the VSP say
// right now) and needs both the voting key and the network for it. Using that
// one to fill a details card meant the card asked for a signature it did not
// need, got ErrWalletLocked, and displayed "not available" for a ticket the
// wallet had a perfectly good record of.
func (asset *Asset) VSPTicketRecord(hash string) (*VSPTicketInfo, error) {
	if !asset.WalletOpened() {
		return nil, utils.ErrDCRNotInitialized
	}

	ticketHash, err := chainhash.NewHashFromStr(hash)
	if err != nil {
		return nil, err
	}

	ctx, _ := asset.ShutdownContextWithCancel()
	host, err := asset.Internal().DCR.VSPHostForTicket(ctx, ticketHash)
	if err != nil || host == "" {
		// The udb lookup reports a missing record as NotExist; anything else
		// is a real read failure and worth surfacing separately.
		if err != nil && !errors.Is(err, errors.E(errors.NotExist)) {
			log.Warnf("VSPTicketRecord: reading host for ticket %s: %v", hash, err)
		}
		return nil, ErrNoVSPRecord
	}

	info := &VSPTicketInfo{VSP: host}

	if feeHash, err := asset.Internal().DCR.VSPFeeHashForTicket(ctx, ticketHash); err == nil {
		info.FeeTxHash = feeHash.String()
	}
	if confirmed, err := asset.Internal().DCR.IsVSPTicketConfirmed(ctx, ticketHash); err == nil {
		info.ConfirmedByVSP = confirmed
	}

	return info, nil
}

// VSPTicketInfo returns vsp-related info for a given ticket. Returns an error
// if the ticket is not yet assigned to a VSP.
func (asset *Asset) VSPTicketInfo(hash string) (*VSPTicketInfo, error) {
	if !asset.WalletOpened() {
		return nil, utils.ErrDCRNotInitialized
	}

	// Cannot query an VSPTicketInfo api if the current instance wallet is locked.
	if asset.IsLocked() {
		log.Warnf("cannot query any ticket info when the wallet is locked")
		return nil, errors.New(utils.ErrWalletLocked)
	}

	ticketHash, err := chainhash.NewHashFromStr(hash)
	if err != nil {
		return nil, err
	}

	// Read the VSP info for this ticket from the wallet db.
	ctx, _ := asset.ShutdownContextWithCancel()
	ticket, err := asset.Internal().DCR.NewVSPTicket(ctx, ticketHash)
	if err != nil {
		return nil, err
	}

	walletTicketInfo, err := ticket.VSPTicketInfo(ctx)
	if err != nil {
		log.Warnf("unable to getWallet info using ticket: %s Error: %v", hash, err)
		return nil, err
	}

	ticketInfo := &VSPTicketInfo{
		VSP:         walletTicketInfo.Host,
		FeeTxHash:   walletTicketInfo.FeeHash.String(),
		FeeTxStatus: VSPFeeStatus(walletTicketInfo.FeeTxStatus),
	}

	// Pay/retry from the ticket's own commitment account, not the
	// auto-buyer setting — a manual buy from account 2 must not retry
	// the fee from account 0.
	feeAcct := asset.vspFeeAccount(ctx, ticket)
	vspClient, err := asset.VSPClient(feeAcct, walletTicketInfo.Host, walletTicketInfo.PubKey)
	if err != nil {
		log.Warnf("unable to connect to host: %s Error: %v", walletTicketInfo.Host, err)
		return ticketInfo, nil
	}

	req := vspd.TicketStatusRequest{
		TicketHash: ticket.Hash().String(),
	}

	vspTicketStatus, err := vspClient.TicketStatus(ctx, req, ticket.CommitmentAddr())
	if err != nil {
		log.Warnf("unable to get vsp ticket: %s Error: %v", hash, err)
		return ticketInfo, nil
	}

	// Sanity check and log any observed discrepancies.
	if ticketInfo.FeeTxHash != vspTicketStatus.FeeTxHash {
		log.Warnf("wallet fee tx hash %s differs from vsp fee tx hash %s for ticket %s",
			ticketInfo.FeeTxHash, vspTicketStatus.FeeTxHash, ticketHash)
	}

	ticketInfo.VSPTicket = ticket
	ticketInfo.Client = vspClient
	ticketInfo.FeeTxHash = vspTicketStatus.FeeTxHash
	ticketInfo.ConfirmedByVSP = vspTicketStatus.TicketConfirmed

	return ticketInfo, nil
}

// ProcessUnpaidVSPTickets retries VSP fee payment for every live ticket this
// wallet registered with a VSP whose fee is not confirmed. Requires an
// unlocked wallet (voting key + signed feeaddress request).
func (asset *Asset) ProcessUnpaidVSPTickets() error {
	if !asset.WalletOpened() {
		return utils.ErrDCRNotInitialized
	}
	if asset.IsLocked() {
		return errors.New(utils.ErrWalletLocked)
	}

	ctx, _ := asset.ShutdownContextWithCancel()
	var lastErr error
	seen := make(map[chainhash.Hash]struct{})
	for _, st := range []int{
		int(VSPFeeProcessStarted),
		int(VSPFeeProcessErrored),
		int(VSPFeeProcessPaid),
	} {
		hashes, err := asset.Internal().DCR.GetVSPTicketsByFeeStatus(ctx, st)
		if err != nil {
			return err
		}
		for i := range hashes {
			h := hashes[i]
			if _, ok := seen[h]; ok {
				continue
			}
			seen[h] = struct{}{}
			if err := asset.processOneUnpaidVSPTicket(ctx, &h); err != nil {
				log.Errorf("ProcessUnpaidVSPTickets: fee for %s: %v", h, err)
				lastErr = err
			}
		}
	}
	return lastErr
}

// RecoverUnregisteredVSPTickets resumes VSP fee payment for tickets this
// wallet already tied to a VSP, and for unmanaged tickets the VSP itself
// already knows (ticketstatus). It does NOT call Process() on unknown
// tickets: vspd feeaddress accepts any votable ticket, so that would
// register solo/Direct-buy tickets and pay a fee without the user asking.
// Requires an unlocked wallet.
func (asset *Asset) RecoverUnregisteredVSPTickets() error {
	if !asset.WalletOpened() {
		return utils.ErrDCRNotInitialized
	}
	if asset.IsLocked() {
		return errors.New(utils.ErrWalletLocked)
	}

	host, pubKey := asset.recoverVSPTarget()
	if host == "" || len(pubKey) == 0 {
		log.Debugf("RecoverUnregisteredVSPTickets: no VSP to recover against")
		return asset.ProcessUnpaidVSPTickets()
	}

	ctx, _ := asset.ShutdownContextWithCancel()
	tickets, err := asset.Internal().DCR.UnprocessedTickets(ctx)
	if err != nil {
		return err
	}
	if len(tickets) > 0 {
		acct := asset.vspFeeAccount(ctx, nil)
		client, err := asset.VSPClient(acct, host, pubKey)
		if err != nil {
			return err
		}
		log.Infof("Checking %d unmanaged tickets against %s (skip if VSP does not know them)", len(tickets), host)
		if err := client.ProcessManagedTickets(ctx, tickets); err != nil {
			log.Warnf("ProcessManagedTickets: %v", err)
		}
	}
	return asset.ProcessUnpaidVSPTickets()
}

// recoverVSPTarget is the VSP we should attach unmanaged tickets to: last
// used, then the auto-buyer host, then the only known VSP on this network.
func (asset *Asset) recoverVSPTarget() (string, []byte) {
	host := normalizeVSPHost(asset.LastUsedVSP())
	if host == "" {
		host = normalizeVSPHost(asset.AutoTicketsBuyerConfig().VspHost)
	}
	if v := asset.vspByHost(host); v != nil && v.VspInfoResponse != nil {
		return v.Host, v.PubKey
	}
	if host != "" {
		if info, err := vspInfo(host); err == nil {
			return host, info.PubKey
		}
		log.Warnf("recoverVSPTarget: vspinfo %s: cannot fetch pubkey", host)
	}
	vsps := asset.KnownVSPs()
	if len(vsps) == 1 && vsps[0] != nil && vsps[0].VspInfoResponse != nil && vsps[0].Host != "" {
		return vsps[0].Host, vsps[0].PubKey
	}
	return "", nil
}

func (asset *Asset) vspByHost(host string) *VSP {
	if host == "" {
		return nil
	}
	host = normalizeVSPHost(host)
	for _, v := range asset.KnownVSPs() {
		if v != nil && normalizeVSPHost(v.Host) == host {
			return v
		}
	}
	return nil
}

func (asset *Asset) processOneUnpaidVSPTicket(ctx context.Context, hash *chainhash.Hash) error {
	info, err := asset.VSPTicketInfo(hash.String())
	if err != nil || info == nil || info.Client == nil || info.VSPTicket == nil {
		if err != nil && err.Error() != utils.ErrWalletLocked {
			return err
		}
		return nil
	}
	if info.FeeTxStatus == VSPFeeProcessConfirmed && info.ConfirmedByVSP {
		return nil
	}
	return info.Client.Process(ctx, info.VSPTicket, nil)
}

// vspFeeAccount is the account that should pay (or retry) the VSP fee for
// this ticket: the ticket's own commitment account when known, otherwise
// the configured ticket-buyer account, otherwise default account 0.
func (asset *Asset) vspFeeAccount(ctx context.Context, ticket *w.VSPTicket) int32 {
	if ticket != nil {
		if addr := ticket.CommitmentAddr(); addr != nil {
			if known, err := asset.Internal().DCR.KnownAddress(ctx, addr); err == nil {
				if num, err := asset.Internal().DCR.AccountNumber(ctx, known.AccountName()); err == nil {
					return int32(num)
				}
			}
		}
	}
	if asset.IsTicketBuyerAccountSet() {
		return asset.AutoTicketsBuyerConfig().PurchaseAccount
	}
	return 0
}

// checkVSPFeeAgainstMax refuses a buy when the VSP's quoted percent of the
// current ticket price is already above the wallet fee cap. Saves publishing
// a ticket that receiveFeeAddress will then reject.
func (asset *Asset) checkVSPFeeAgainstMax(pct float64) error {
	if pct <= 0 {
		return fmt.Errorf("VSP reported invalid fee percentage %v", pct)
	}
	tp, err := asset.TicketPrice()
	if err != nil || tp == nil || tp.TicketPrice <= 0 {
		return nil
	}
	est := dcrutil.Amount(float64(tp.TicketPrice) * pct / 100.0)
	max := asset.vspMaxFee()
	if est > max {
		return fmt.Errorf("VSP fee (~%s at %.2f%%) exceeds wallet maximum %s", est, pct, max)
	}
	return nil
}

// StartTicketBuyer starts the automatic ticket buyer. The wallet
// should already be configured with the required parameters using
// asset.SetAutoTicketsBuyerConfig().
func (asset *Asset) StartTicketBuyer(passphrase string) error {
	if !asset.WalletOpened() {
		return utils.ErrDCRNotInitialized
	}

	// NOTE: the upstream Decred gate that required mixed+unmixed CoinShuffle++
	// accounts was removed — Skarb has no mixer, so MixedAccountNumber()/
	// UnmixedAccountNumber() are hard-coded -1 stubs and that gate would make
	// the ticket buyer impossible to start. Tickets are bought directly from
	// cfg.PurchaseAccount with Mixing:false (see runTicketBuyer/buyTicket).
	cfg := asset.AutoTicketsBuyerConfig()
	// An empty VspHost is a valid SOLO (Direct buy) config — "configured" is
	// signalled by the purchase account, not the host (TicketBuyerConfigIsSet).
	if cfg.PurchaseAccount < 0 {
		return errors.New("ticket buyer config not set for this wallet")
	}
	if cfg.BalanceToMaintain < 0 {
		return errors.New("Negative balance to maintain in ticket buyer config")
	}

	if asset.IsAutoTicketsPurchaseActive() {
		return errors.New("Ticket buyer already running")
	}

	// Validate the passphrase.
	if len(passphrase) > 0 && asset.IsLocked() {
		err := asset.UnlockWallet(passphrase)
		if err != nil {
			return utils.TranslateError(err)
		}
	}

	ctx, cancel := asset.ShutdownContextWithCancel()

	// Solo mode: leave cfg.VspClient nil — buyTicket's PurchaseTicketsRequest
	// then carries no VSPClient and the vendored purchaseTickets natively
	// skips fee reservation/processing, deriving voting rights from the
	// purchase account (same mechanism as a manual Direct buy). The ticket
	// must be voted by a voting-enabled wallet holding this seed.
	if cfg.VspHost != "" {
		// Check the VSP.
		vspInfo, err := vspInfo(cfg.VspHost)
		if err != nil {
			cancel()
			return fmt.Errorf("error setting up vsp client: %v", err)
		}

		cfg.VspClient, err = asset.VSPClient(cfg.PurchaseAccount, cfg.VspHost, vspInfo.PubKey)
		if err != nil {
			cancel()
			log.Errorf("[%d] VSP Client instance failed error: %v", asset.ID, err)
			return errors.New("VSP Client failed to start due to incorrect configuration")
		}
		pct, err := cfg.VspClient.FeePercentage(ctx)
		if err != nil {
			cancel()
			return fmt.Errorf("cannot fetch VSP fee percentage from %s: %w", cfg.VspHost, err)
		}
		if err := asset.checkVSPFeeAgainstMax(pct); err != nil {
			cancel()
			return err
		}
	}

	// Mark the buyer active only after everything that can fail has succeeded;
	// otherwise IsAutoTicketsPurchaseActive() (which just checks this field !=
	// nil) would report it running with no goroutine and wedge future starts.
	asset.cancelAutoTicketBuyerMu.Lock()
	asset.cancelAutoTicketBuyer = cancel
	asset.cancelAutoTicketBuyerMu.Unlock()

	go func() {
		log.Infof("[%d] Running ticket buyer", asset.ID)
		if err := asset.RecoverUnregisteredVSPTickets(); err != nil {
			log.Warnf("[%d] Recover unregistered VSP tickets: %v", asset.ID, err)
		}

		if err := asset.runTicketBuyer(ctx, passphrase, cfg); err != nil {
			if ctx.Err() != nil {
				log.Errorf("[%d] Ticket buyer instance canceled", asset.ID)
			} else {
				log.Errorf("[%d] Ticket buyer instance errored: %v", asset.ID, err)
			}
		}

		if err := asset.StopAutoTicketsPurchase(); err != nil {
			log.Errorf("[%d] Stopping auto ticket purchase errored: %v", asset.ID, err)
		}
	}()

	return nil
}

// runTicketBuyer executes the ticket buyer. If the private passphrase is
// incorrect, or ever becomes incorrect due to a wallet passphrase change,
// runTicketBuyer exits with an errors.Passphrase error.
func (asset *Asset) runTicketBuyer(ctx context.Context, passphrase string, cfg *TicketBuyerConfig) error {
	if len(passphrase) > 0 && asset.IsLocked() {
		err := asset.UnlockWallet(passphrase)
		if err != nil {
			return utils.TranslateError(err)
		}
	}

	c := asset.Internal().DCR.NtfnServer.MainTipChangedNotifications()
	defer c.Done()

	ctx, outerCancel := context.WithCancel(ctx)
	defer outerCancel()
	var fatal error
	var fatalMu sync.Mutex

	var nextIntervalStart, expiry int32
	var cancels []func()
	for {
		select {
		case <-ctx.Done():
			defer outerCancel()
			fatalMu.Lock()
			err := fatal
			fatalMu.Unlock()
			if err != nil {
				return err
			}
			return ctx.Err()
		case n := <-c.C:
			if len(n.AttachedBlocks) == 0 {
				continue
			}

			tip := n.AttachedBlocks[len(n.AttachedBlocks)-1]
			w := asset.Internal().DCR

			// Don't perform any actions while transactions are not synced through
			// the tip block.
			rp, err := w.RescanPoint(ctx)
			if err != nil {
				return err
			}
			if rp != nil {
				log.Debugf("[%d] Skipping autobuyer actions: transactions are not synced", asset.ID)
				continue
			}

			tipHeader, err := w.BlockHeader(ctx, tip)
			if err != nil {
				log.Error(err)
				continue
			}
			height := int32(tipHeader.Height)

			// Cancel any ongoing ticket purchases which are buying
			// at an old ticket price or are no longer able to
			// create mined tickets the window.
			if height+2 >= nextIntervalStart {
				for i, cancel := range cancels {
					cancel()
					cancels[i] = nil
				}
				cancels = cancels[:0]

				intervalSize := int32(w.ChainParams().StakeDiffWindowSize)
				currentInterval := height / intervalSize
				nextIntervalStart = (currentInterval + 1) * intervalSize

				// Skip this purchase when no more tickets may be purchased in the interval and
				// the next sdiff is unknown.  The earliest any ticket may be mined is two
				// blocks from now, with the next block containing the split transaction
				// that the ticket purchase spends.
				if height+2 == nextIntervalStart {
					log.Debugf("[%d] Skipping purchase: next sdiff interval starts soon", asset.ID)
					continue
				}
				// Set expiry to prevent tickets from being mined in the next
				// sdiff interval.  When the next block begins the new interval,
				// the ticket is being purchased for the next interval; therefore
				// increment expiry by a full sdiff window size to prevent it
				// being mined in the interval after the next.
				expiry = nextIntervalStart
				if height+1 == nextIntervalStart {
					expiry += intervalSize
				}
			}

			// Get the account balance to determine how many tickets to buy
			bal, err := asset.GetAccountBalance(cfg.PurchaseAccount)
			if err != nil {
				return err
			}

			spendable := bal.Spendable.ToInt()
			if spendable < cfg.BalanceToMaintain {
				log.Debugf("[%d] Skipping purchase: low available balance", asset.ID)
				continue
			}

			spendable -= cfg.BalanceToMaintain
			sdiff, err := asset.Internal().DCR.NextStakeDifficultyAfterHeader(ctx, tipHeader)
			if err != nil {
				return err
			}

			buy := int(dcrutil.Amount(spendable) / sdiff)
			if buy == 0 {
				log.Debugf("[%d] Skipping purchase: low available balance", asset.ID)
				continue
			}
			if max := int(w.ChainParams().MaxFreshStakePerBlock); max > 0 && buy > max {
				buy = max
			}

			cancelCtx, cancel := context.WithCancel(ctx)
			cancels = append(cancels, cancel)
			// One PurchaseTickets(Count=N) like a manual multi-buy: one
			// split, N tickets, N VSP fees, all in the mempool together.
			// Count=1-per-loop plus RequiredConfirmations (2 on mainnet)
			// was stretching auto-buy across one ticket per 1–2 blocks.
			if err := asset.buyTickets(cancelCtx, passphrase, sdiff, expiry, cfg, buy); err != nil {
				switch {
				case errors.Is(err, errors.InsufficientBalance):
				case errors.Is(err, context.Canceled):
				case errors.Is(err, context.DeadlineExceeded):
				default:
					log.Errorf("[%d] Ticket purchasing failed: %v", asset.ID, err)
				}
				if errors.Is(err, errors.Passphrase) {
					fatalMu.Lock()
					fatal = err
					fatalMu.Unlock()
					outerCancel()
				}
			}
		}
	}
}

// buyTickets purchases count tickets in one PurchaseTickets call (one split).
func (asset *Asset) buyTickets(ctx context.Context, passphrase string, sdiff dcrutil.Amount, expiry int32, cfg *TicketBuyerConfig, count int) error {
	ctx, task := trace.NewTask(ctx, "ticketbuyer.buy")
	defer task.End()

	if count < 1 {
		return nil
	}

	if len(passphrase) > 0 && asset.IsLocked() {
		err := asset.UnlockWallet(passphrase)
		if err != nil {
			return utils.TranslateError(err)
		}
	}

	networkBackend, err := asset.Internal().DCR.NetworkBackend()
	if err != nil {
		return err
	}

	if !asset.IsTicketBuyerAccountSet() {
		return utils.ErrTicketPurchaseAccMissing
	}

	// Same shape as a manual multi-buy: one split funds every ticket (and
	// each VSP fee) in this window. Mixing is disabled (mixer removed).
	request := &w.PurchaseTicketsRequest{
		Count:         count,
		SourceAccount: uint32(cfg.PurchaseAccount),
		ChangeAccount: uint32(cfg.PurchaseAccount),
		Expiry:        expiry,
		MinConf:       asset.RequiredConfirmations(),
		VSPClient:     cfg.VspClient,

		// VotingAccount used to derive addresses for specifying voting rights.
		// It is used when VotingAddress == nil, or Mixing == true
		VotingAccount: uint32(cfg.PurchaseAccount),
		Mixing:        false,
	}

	tix, err := asset.Internal().DCR.PurchaseTickets(ctx, networkBackend, request)
	if tix != nil {
		for _, hash := range tix.TicketHashes {
			log.Infof("[%d] Purchased ticket %v at stake difficulty %v", asset.ID, hash, sdiff)
		}
	}

	return err
}

// IsAutoTicketsPurchaseActive returns true if ticket buyer is active.
func (asset *Asset) IsAutoTicketsPurchaseActive() bool {
	asset.cancelAutoTicketBuyerMu.Lock()
	defer asset.cancelAutoTicketBuyerMu.Unlock()
	return asset.cancelAutoTicketBuyer != nil
}

// StopAutoTicketsPurchase stops the automatic ticket buyer.
func (asset *Asset) StopAutoTicketsPurchase() error {
	asset.cancelAutoTicketBuyerMu.Lock()
	defer asset.cancelAutoTicketBuyerMu.Unlock()

	if asset.cancelAutoTicketBuyer == nil {
		return errors.New(utils.ErrInvalid)
	}

	asset.cancelAutoTicketBuyer()
	asset.cancelAutoTicketBuyer = nil
	return nil
}

// SetAutoTicketsBuyerConfig sets ticket buyer config for the asset.
func (asset *Asset) SetAutoTicketsBuyerConfig(vspHost string, purchaseAccount int32, amountToMaintain int64) {
	asset.SetLongConfigValueForKey(sharedW.TicketBuyerATMConfigKey, amountToMaintain)
	asset.SetInt32ConfigValueForKey(sharedW.TicketBuyerAccountConfigKey, purchaseAccount)
	asset.SetStringConfigValueForKey(sharedW.TicketBuyerVSPHostConfigKey, vspHost)
}

// AutoTicketsBuyerConfig returns the previously set ticket buyer config for
// the asset.
func (asset *Asset) AutoTicketsBuyerConfig() *TicketBuyerConfig {
	btm := asset.ReadLongConfigValueForKey(sharedW.TicketBuyerATMConfigKey, -1)
	accNum := asset.ReadInt32ConfigValueForKey(sharedW.TicketBuyerAccountConfigKey, -1)
	vspHost := asset.ReadStringConfigValueForKey(sharedW.TicketBuyerVSPHostConfigKey, "")

	return &TicketBuyerConfig{
		VspHost:           vspHost,
		PurchaseAccount:   accNum,
		BalanceToMaintain: btm,
	}
}

// TicketBuyerConfigIsSet checks if ticket buyer config is set for the asset.
// The signal is the purchase account, NOT the VSP host: an empty host is a
// valid SOLO (Direct buy) configuration, while ClearTicketBuyerConfig resets
// the account to -1. Keying off the host would make a saved solo config look
// unconfigured (the toggle would re-open the settings modal forever).
func (asset *Asset) TicketBuyerConfigIsSet() bool {
	return asset.ReadInt32ConfigValueForKey(sharedW.TicketBuyerAccountConfigKey, -1) != -1
}

// IsTicketBuyerAccountSet checks if ticket buyer account is set for the asset.
func (asset *Asset) IsTicketBuyerAccountSet() bool {
	return asset.ReadInt32ConfigValueForKey(sharedW.TicketBuyerAccountConfigKey, -1) != -1
}

// ClearTicketBuyerConfig clears the wallet's ticket buyer config.
func (asset *Asset) ClearTicketBuyerConfig(_ int) error {
	asset.SetLongConfigValueForKey(sharedW.TicketBuyerATMConfigKey, -1)
	asset.SetInt32ConfigValueForKey(sharedW.TicketBuyerAccountConfigKey, -1)
	asset.SetStringConfigValueForKey(sharedW.TicketBuyerVSPHostConfigKey, "")

	return nil
}

// NextTicketPriceRemaining returns the remaning time in seconds of a ticket for the next block,
// if secs equal 0 is imminent
func (asset *Asset) NextTicketPriceRemaining() (secs int64, err error) {
	params, er := utils.DCRChainParams(asset.NetType())
	if er != nil {
		secs, err = -1, er
		return
	}
	bestBestBlock := asset.GetBestBlock()
	idxBlockInWindow := int(int64(bestBestBlock.Height)%params.StakeDiffWindowSize) + 1
	blockTime := params.TargetTimePerBlock.Nanoseconds()
	windowSize := params.StakeDiffWindowSize
	x := (windowSize - int64(idxBlockInWindow)) * blockTime
	if x == 0 {
		secs, err = 0, nil
		return
	}
	secs, err = int64(time.Duration(x).Seconds()), nil
	return
}

// UnspentUnexpiredTickets returns all Unmined, Immature and Live tickets.
func (asset *Asset) UnspentUnexpiredTickets() ([]*sharedW.Transaction, error) {
	var tickets []*sharedW.Transaction
	for _, filter := range []int32{TxFilterUnmined, TxFilterImmature, TxFilterLive} {
		tx, err := asset.GetTransactionsRaw(0, 0, filter, true, "")
		if err != nil {
			return nil, err
		}

		tickets = append(tickets, tx...)
	}

	return tickets, nil
}
