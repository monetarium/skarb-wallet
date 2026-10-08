// Copyright (c) 2023-2024 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package wallet

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/decred/vspd/types/v3"
	"github.com/monetarium/monetarium-node/chaincfg"
	"github.com/monetarium/monetarium-node/chaincfg/chainhash"
	"github.com/monetarium/monetarium-node/crypto/rand"
	"github.com/monetarium/monetarium-node/dcrutil"
	"github.com/monetarium/monetarium-node/txscript/stdaddr"
	"github.com/monetarium/monetarium-node/wire"
)

var (
	errStopped    = errors.New("fee processing stopped")
	errFeeTooHigh = errors.New("server fee amount too high")
)

// A random amount of delay (between zero and these jitter constants) is added
// before performing some background action with the VSP.  The delay is reduced
// when a ticket is currently live, as it may be called to vote any time.
const (
	immatureJitter = time.Hour
	liveJitter     = 5 * time.Minute
	unminedJitter  = 2 * time.Minute
)

type vspFeePayment struct {
	client *VSPClient
	ctx    context.Context

	// Set at feePayment creation and never changes
	ticket *VSPTicket
	policy *VSPPolicy

	// procMu serializes fee processing: a scheduled attempt and a Process
	// call for the same ticket must not build or submit fee txs at once.
	procMu sync.Mutex

	// Requires locking for all access outside of Client.feePayment
	mu      sync.Mutex
	fee     dcrutil.Amount
	feeAddr stdaddr.Address
	feeHash chainhash.Hash
	feeTx   *wire.MsgTx
	state   State
	err     error

	timerMu sync.Mutex
	timer   *time.Timer

	params *chaincfg.Params
}

type State uint32

const (
	_ State = iota
	Unprocessed
	FeePublished
	_ // ...
	TicketSpent
)

func (fp *vspFeePayment) removedExpiredOrSpent() bool {
	var reason string
	switch {
	case fp.ticket.Expired(fp.ctx):
		reason = "expired"
	case fp.ticket.Spent(fp.ctx):
		reason = "spent"
	case fp.ticket.Pruned(fp.ctx):
		// An unmined ticket the wallet dropped can never vote.
		reason = "pruned"
		if err := fp.ticket.DeleteVSPRecord(fp.ctx); err != nil {
			fp.client.log.Errorf("Ticket %v: delete VSP record: %v", fp.ticket, err)
		}
	}
	if reason != "" {
		fp.remove(reason)
		// nothing scheduled
		return true
	}
	return false
}

// feeTxSigned reports whether tx is a finished fee transaction: it has outputs
// and every input is signed.  A tx with outputs but missing signatures is left
// over from a failed attempt and must be rebuilt, not submitted.
func feeTxSigned(tx *wire.MsgTx) bool {
	if tx == nil || len(tx.TxOut) == 0 || len(tx.TxIn) == 0 {
		return false
	}
	for _, in := range tx.TxIn {
		if len(in.SignatureScript) == 0 {
			return false
		}
	}
	return true
}

// abandonFeeTx removes an unpublished fee tx from the wallet so its inputs can
// be spent again.
func (fp *vspFeePayment) abandonFeeTx(feeHash chainhash.Hash) {
	if feeHash == (chainhash.Hash{}) {
		return
	}
	err := fp.client.wallet.AbandonTransaction(fp.ctx, &feeHash)
	if err != nil {
		fp.client.log.Errorf("Ticket %v: abandon fee tx %v: %v", fp.ticket, feeHash, err)
		return
	}
	fp.client.log.Infof("Ticket %v: abandoned fee tx %v", fp.ticket, feeHash)
}

func (fp *vspFeePayment) remove(reason string) {
	fp.stop()
	fp.client.log.Infof("Ticket %v is %s; removing from VSP client", fp.ticket, reason)
	fp.client.mu.Lock()
	delete(fp.client.jobs, *fp.ticket.Hash())
	fp.client.mu.Unlock()
}

// feePayment returns an existing managed fee payment, or creates and begins
// processing a fee payment for a ticket.
func (c *VSPClient) feePayment(ctx context.Context, ticket *VSPTicket, paidConfirmed bool) (fp *vspFeePayment) {
	ticketHash := ticket.Hash()
	c.mu.Lock()
	fp = c.jobs[*ticketHash]
	c.mu.Unlock()
	if fp != nil {
		return fp
	}

	defer func() {
		if fp == nil {
			return
		}
		var schedule bool
		c.mu.Lock()
		fp2 := c.jobs[*ticketHash]
		if fp2 != nil {
			fp.stop()
			fp = fp2
		} else {
			c.jobs[*ticketHash] = fp
			schedule = true
		}
		c.mu.Unlock()
		if schedule {
			fp.schedule("reconcile payment", fp.reconcilePayment)
		}
	}()

	fp = &vspFeePayment{
		client: c,
		ctx:    c.lifetimeCtx(),
		ticket: ticket,
		policy: c.policy,
		params: c.wallet.chainParams,
	}

	// No VSP interaction is required for spent tickets.
	if fp.ticket.Spent(ctx) {
		fp.state = TicketSpent
		return fp
	}

	feeHash, err := ticket.FeeHash(ctx)
	if err != nil {
		// caller must schedule next method, as paying the fee may
		// require using provided transaction inputs.
		return fp
	}

	fee, err := ticket.FeeTx(ctx)
	if err != nil {
		// A fee hash is recorded for this ticket, but was not found in
		// the wallet.  This should not happen and may require manual
		// intervention.
		//
		// TODO: check ticketinfo and, if the fee is not paid, update it
		// with a new fee instead of giving up.
		fp.err = fmt.Errorf("fee transaction not found in wallet: %w", err)
		return fp
	}

	fp.feeTx = fee
	fp.feeHash = feeHash

	// If database has been updated to paid or confirmed status, we can forgo
	// this step.
	if !paidConfirmed {
		err = fp.ticket.UpdateFeeStarted(ctx, feeHash, c.Client.URL, c.Client.PubKey)
		if err != nil {
			return fp
		}

		fp.state = Unprocessed
		fp.fee = 0
	}
	return fp
}

// Schedule a method to be executed.
// Any currently-scheduled method is replaced.
func (fp *vspFeePayment) schedule(name string, method func() error) {
	var delay time.Duration
	if method != nil {
		delay = fp.next()
	}

	fp.timerMu.Lock()
	defer fp.timerMu.Unlock()
	if fp.timer != nil {
		fp.timer.Stop()
		fp.timer = nil
	}
	if method != nil {
		fp.client.log.Debugf("Scheduling %q for ticket %s in %v", name, fp.ticket, delay)
		fp.timer = time.AfterFunc(delay, fp.task(name, method))
	}
}

func (fp *vspFeePayment) next() time.Duration {
	w := fp.client.wallet
	_, tipHeight := w.MainChainTip(fp.ctx)

	ticketLive := fp.ticket.LiveHeight(fp.ctx)
	ticketExpires := fp.ticket.ExpiryHeight(fp.ctx)

	var jitter time.Duration
	switch {
	case tipHeight < ticketLive: // immature, mined ticket
		blocksUntilLive := ticketLive - tipHeight
		jitter = min(fp.params.TargetTimePerBlock*time.Duration(blocksUntilLive), immatureJitter)
	case tipHeight < ticketExpires: // live ticket
		jitter = liveJitter
	default: // unmined ticket
		jitter = unminedJitter
	}

	return rand.Duration(jitter)
}

// task returns a function running a feePayment method.
// If the method errors, the error is logged, and the payment is put
// in an errored state and may require manual processing.
func (fp *vspFeePayment) task(name string, method func() error) func() {
	return func() {
		fp.procMu.Lock()
		err := method()
		fp.procMu.Unlock()
		fp.mu.Lock()
		fp.err = err
		fp.mu.Unlock()
		if err != nil {
			fp.client.log.Errorf("Ticket %v: %v: %v", fp.ticket, name, err)
		}
	}
}

func (fp *vspFeePayment) stop() {
	fp.schedule("", nil)
}

func (fp *vspFeePayment) receiveFeeAddress() error {
	ctx := fp.ctx

	// stop processing if ticket is expired or spent
	if fp.removedExpiredOrSpent() {
		// nothing scheduled
		return errStopped
	}

	// Fetch ticket and its parent transaction (typically, a split
	// transaction).
	ticketHex, err := marshalTx(fp.ticket.RawTx())
	if err != nil {
		return err
	}
	parentHex, err := marshalTx(fp.ticket.ParentTx())
	if err != nil {
		return err
	}

	req := types.FeeAddressRequest{
		Timestamp:  time.Now().Unix(),
		TicketHash: fp.ticket.Hash().String(),
		TicketHex:  ticketHex,
		ParentHex:  parentHex,
	}

	resp, err := fp.client.FeeAddress(ctx, req, fp.ticket.CommitmentAddr())
	if err != nil {
		return err
	}

	feeAmount := dcrutil.Amount(resp.FeeAmount)
	feeAddr, err := stdaddr.DecodeAddress(resp.FeeAddress, fp.params)
	if err != nil {
		return fmt.Errorf("server fee address invalid: %w", err)
	}

	fp.client.log.Infof("VSP requires fee %v", feeAmount)
	if feeAmount > fp.policy.MaxFee {
		return fmt.Errorf("%w: %v > %v", errFeeTooHigh,
			feeAmount, fp.policy.MaxFee)
	}

	// TODO: validate server timestamp.

	fp.mu.Lock()
	fp.fee = feeAmount
	fp.feeAddr = feeAddr
	fp.mu.Unlock()

	return nil
}

// makeFeeTx adds outputs to tx to pay a VSP fee, optionally adding inputs as
// well to fund the transaction if no input value is already provided in the
// transaction.
//
// If tx is nil, fp.feeTx may be assigned or modified, but the pointer will not
// be dereferenced.
func (fp *vspFeePayment) makeFeeTx(tx *wire.MsgTx) error {
	ctx := fp.ctx
	w := fp.client.wallet

	fp.mu.Lock()
	fee := fp.fee
	fpFeeTx := fp.feeTx
	feeAddr := fp.feeAddr
	fp.mu.Unlock()
	callerTx := tx

	// The rest of this function will operate on the tx pointer, with fp.feeTx
	// assigned to the result on success.
	// Update tx to use the partially created fpFeeTx if any has been started.
	// The transaction pointed to by the caller will be dereferenced and modified
	// when non-nil.
	if fpFeeTx != nil {
		if tx != nil {
			*tx = *fpFeeTx
		} else {
			tx = fpFeeTx
		}
	}
	// A signed fee transaction is already finished.  One with outputs but
	// no signatures is left from a failed attempt; start over.
	if feeTxSigned(fpFeeTx) {
		return nil
	}
	if fpFeeTx != nil && len(fpFeeTx.TxOut) != 0 {
		fp.client.log.Warnf("Ticket %v: rebuilding unsigned fee tx", fp.ticket)
		if callerTx != nil {
			// The caller reads the result from its own tx.
			*callerTx = *wire.NewMsgTx()
			tx = callerTx
		} else {
			tx = wire.NewMsgTx()
		}
	}
	// When both transactions are nil, create a new empty transaction.
	if tx == nil {
		tx = wire.NewMsgTx()
	}

	if fee == 0 {
		err := fp.receiveFeeAddress()
		if err != nil {
			return err
		}
		fp.mu.Lock()
		fee = fp.fee
		feeAddr = fp.feeAddr
		fp.mu.Unlock()
	}

	err := w.CreateVspPayment(ctx, tx, fee, feeAddr, fp.policy.FeeAcct, fp.policy.ChangeAcct)
	if err != nil {
		return fmt.Errorf("unable to create VSP fee tx for ticket %v: %w", fp.ticket, err)
	}

	feeHash := tx.TxHash()
	err = fp.ticket.UpdateFeePaid(ctx, feeHash, fp.client.URL, fp.client.PubKey)
	if err != nil {
		// CreateVspPayment already added the tx to the wallet.  Abandon
		// it, or each retry would leave another one holding inputs.
		fp.abandonFeeTx(feeHash)
		return err
	}

	fp.mu.Lock()
	fp.feeTx = tx
	fp.feeHash = feeHash
	fp.mu.Unlock()

	// nothing scheduled
	return nil
}

func (c *VSPClient) status(ctx context.Context, ticket *VSPTicket) (*types.TicketStatusResponse, error) {

	req := types.TicketStatusRequest{
		TicketHash: ticket.Hash().String(),
	}

	resp, err := c.Client.TicketStatus(ctx, req, ticket.CommitmentAddr())
	if err != nil {
		return nil, err
	}

	// TODO: validate server timestamp.

	return resp, nil
}

func (c *VSPClient) setVoteChoices(ctx context.Context, ticket *VSPTicket,
	choices map[string]string, tspendPolicy map[string]string, treasuryPolicy map[string]string) error {

	req := types.SetVoteChoicesRequest{
		Timestamp:      time.Now().Unix(),
		TicketHash:     ticket.Hash().String(),
		VoteChoices:    choices,
		TSpendPolicy:   tspendPolicy,
		TreasuryPolicy: treasuryPolicy,
	}

	_, err := c.Client.SetVoteChoices(ctx, req, ticket.CommitmentAddr())
	if err != nil {
		return err
	}

	// TODO: validate server timestamp.

	return nil
}

func (fp *vspFeePayment) reconcilePayment() error {
	ctx := fp.ctx

	// stop processing if ticket is expired or spent
	// TODO: if the ticket is no longer saved by the wallet (tx expired,
	// double-spent, etc) remove the fee payment.
	if fp.removedExpiredOrSpent() {
		// nothing scheduled
		return errStopped
	}

	// A fee amount and address must have been created by this point.
	// Ensure that the fee transaction can be created, otherwise reschedule
	// this method until it is.  There is no need to check the wallet for a
	// fee transaction matching a known hash; this is performed when
	// creating the feePayment.
	fp.mu.Lock()
	feeTx := fp.feeTx
	fp.mu.Unlock()
	if !feeTxSigned(feeTx) {
		err := fp.makeFeeTx(nil)
		if err != nil {
			var apiErr types.ErrorResponse
			isAPIErr := errors.As(err, &apiErr)
			switch {
			case isAPIErr && apiErr.Code == types.ErrTicketCannotVote:
				fp.remove("ticket cannot vote")
			case isAPIErr && apiErr.Code == types.ErrFeeAlreadyReceived,
				errors.Is(err, errStopped):
				// Nothing to retry.
			case errors.Is(err, errFeeTooHigh):
				// Retrying cannot help until the max fee changes.  Mark
				// the fee errored: the next unlock retries it with the
				// current max fee.
				if markErr := fp.ticket.UpdateFeeErrored(ctx, fp.client.URL, fp.client.PubKey); markErr != nil {
					fp.client.log.Errorf("Ticket %v: mark fee errored: %v", fp.ticket, markErr)
				}
				fp.remove("VSP fee above the configured maximum")
			default:
				// Try again, as with failures to submit the payment.
				// Ask the VSP for the fee again: it may have received
				// one meanwhile.  Start from a new fee tx: the failed
				// one may be half built, or hold inputs a ticket
				// purchase has since unlocked.  Keep a fee tx that a
				// concurrent Process call made instead.
				fp.mu.Lock()
				fp.fee = 0
				if fp.feeTx == feeTx {
					fp.feeTx = nil
				}
				fp.mu.Unlock()
				fp.schedule("reconcile payment", fp.reconcilePayment)
			}
			return err
		}
	}

	// A fee address has been obtained, and the fee transaction has been
	// created, but it is unknown if the VSP has received the fee and will
	// vote using the ticket.
	//
	// If the fee is mined, then check the status of the ticket and payment
	// with the VSP, to ensure that it has marked the fee payment as paid.
	//
	// If the fee is not mined, an API call with the VSP is used so it may
	// receive and publish the transaction.  A follow up on the ticket
	// status is scheduled for some time in the future.

	err := fp.submitPayment()
	fp.mu.Lock()
	feeHash := fp.feeHash
	fp.mu.Unlock()
	var apiErr types.ErrorResponse
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case types.ErrFeeAlreadyReceived:
			// VSP has the signed fee tx ("received"), not necessarily
			// in the mempool yet. Leave it unpublished so the UI stays
			// on Pending by VSP until FeeTxStatus is "broadcast".
			err = fp.ticket.UpdateFeePaid(ctx, feeHash, fp.client.URL, fp.client.PubKey)
			// A failed write is retried below.
		case types.ErrInvalidFeeTx, types.ErrCannotBroadcastFee:
			if markErr := fp.ticket.UpdateFeeErrored(ctx, fp.client.URL, fp.client.PubKey); markErr != nil {
				fp.client.log.Errorf("Ticket %v: mark fee errored: %v", fp.ticket, markErr)
			}
			// The VSP will not take this tx.  Abandon it so its inputs
			// are spendable again, then make a new fee transaction.
			fp.abandonFeeTx(feeHash)
			fp.mu.Lock()
			fp.feeHash = chainhash.Hash{}
			fp.feeTx = nil
			fp.fee = 0
			fp.mu.Unlock()
			// err not nilled, so reconcile payment is rescheduled.
		}
	}
	if err != nil {
		// Nothing left to try except trying again.
		fp.schedule("reconcile payment", fp.reconcilePayment)
		return err
	}

	err = fp.ticket.UpdateFeePaid(ctx, feeHash, fp.client.URL, fp.client.PubKey)
	if err != nil {
		fp.schedule("reconcile payment", fp.reconcilePayment)
		return err
	}

	return fp.confirmPayment()

	/*
		// TODO: for each input, c.Wallet.UnlockOutpoint(&outpoint.Hash, outpoint.Index)
		// — or let the published tx replace the unpublished one, and unlock
		// outpoints as it is processed.

	*/
}

func (fp *vspFeePayment) submitPayment() (err error) {
	ctx := fp.ctx
	w := fp.client.wallet

	// stop processing if ticket is expired or spent
	if fp.removedExpiredOrSpent() {
		// nothing scheduled
		return errStopped
	}

	// submitting a payment requires the fee tx to already be created.
	fp.mu.Lock()
	feeTx := fp.feeTx
	fp.mu.Unlock()
	if !feeTxSigned(feeTx) {
		feeTx = new(wire.MsgTx)
		err := fp.makeFeeTx(feeTx)
		if err != nil {
			return err
		}
	}

	// Retrieve voting preferences
	voteChoices, err := fp.ticket.AgendaChoices(ctx)
	if err != nil {
		return err
	}

	feeTxHex, err := marshalTx(feeTx)
	if err != nil {
		return err
	}

	req := types.PayFeeRequest{
		Timestamp:      time.Now().Unix(),
		TicketHash:     fp.ticket.Hash().String(),
		FeeTx:          feeTxHex,
		VotingKey:      fp.ticket.VotingKey(),
		VoteChoices:    voteChoices,
		TSpendPolicy:   fp.ticket.TSpendPolicy(),
		TreasuryPolicy: fp.ticket.TreasuryKeyPolicy(),
	}

	_, err = fp.client.PayFee(ctx, req, fp.ticket.CommitmentAddr())
	if err != nil {
		var apiErr types.ErrorResponse
		if errors.As(err, &apiErr) && apiErr.Code == types.ErrFeeExpired {
			// Fee has been expired, so abandon current feetx and forget
			// the expired fee amount.  The retry then asks the VSP for a
			// new fee and makes a new fee tx.
			feeHash := feeTx.TxHash()
			err := w.AbandonTransaction(ctx, &feeHash)
			if err != nil {
				fp.client.log.Errorf("error abandoning expired fee tx %v", err)
			}
			fp.mu.Lock()
			fp.feeTx = nil
			fp.feeHash = chainhash.Hash{}
			fp.fee = 0
			fp.mu.Unlock()
			// The abandoned tx must not stay recorded as paid: after a
			// restart nothing would resume the payment.
			if markErr := fp.ticket.UpdateFeeErrored(ctx, fp.client.URL, fp.client.PubKey); markErr != nil {
				fp.client.log.Errorf("Ticket %v: mark fee errored: %v", fp.ticket, markErr)
			}
		}
		return fmt.Errorf("payfee: %w", err)
	}

	// TODO - validate server timestamp?

	fp.client.log.Infof("successfully processed %v", fp.ticket)
	return nil
}

// confirmPayment will remove the fee payment processing when the fee has
// reached sufficient confirmations, and reschedule itself if the fee is not
// confirmed yet.  If the fee tx is ever removed from the wallet, this will
// schedule another reconcile.
func (fp *vspFeePayment) confirmPayment() (err error) {
	ctx := fp.ctx

	// stop processing if ticket is expired or spent
	if fp.removedExpiredOrSpent() {
		// nothing scheduled
		return errStopped
	}

	defer func() {
		if err != nil && !errors.Is(err, errStopped) {
			fp.schedule("reconcile payment", fp.reconcilePayment)
		}
	}()

	status, err := fp.client.status(ctx, fp.ticket)
	if err != nil {
		fp.client.log.Warnf("Rescheduling status check for %v: %v", fp.ticket, err)
		// Return the error so callers do not treat a failed VSP status
		// probe as success. defer reschedules reconcilePayment.
		return err
	}

	switch status.FeeTxStatus {
	case "received":
		// VSP has received the fee tx but has not yet broadcast it.
		// VSP will only broadcast the tx when ticket has 6+ confirmations.
		fp.schedule("confirm payment", fp.confirmPayment)
		return nil
	case "broadcast":
		fp.client.log.Infof("VSP has successfully sent the fee tx for %v", fp.ticket)
		// The fee tx was created unpublished so this wallet would not
		// put it in the mempool before the VSP did. Now that the VSP
		// has broadcast it, drop the unpublished flag so the UI can
		// show Unconfirmed (in mempool) instead of Pending by VSP, and
		// so a later reconnect rebroadcasts it if peers dropped it.
		fp.mu.Lock()
		feeHash := fp.feeHash
		feeTx := fp.feeTx
		fp.mu.Unlock()
		w := fp.client.wallet
		if err := w.SetPublished(ctx, &feeHash, true); err != nil {
			fp.client.log.Errorf("SetPublished(%v) after VSP broadcast: %v", feeHash, err)
		}
		if n, err := w.NetworkBackend(); err == nil {
			w.watchTxOutputs(ctx, n, feeTx)
		}
		fp.schedule("confirm payment", fp.confirmPayment)
		return nil
	case "confirmed":
		fp.remove("confirmed by VSP")
		// nothing scheduled
		fp.mu.Lock()
		feeHash := fp.feeHash
		fp.mu.Unlock()
		err = fp.ticket.UpdateFeeConfirmed(ctx, feeHash, fp.client.URL, fp.client.PubKey)
		if err != nil {
			return err
		}
		return nil
	case "error":
		fp.client.log.Warnf("VSP failed to broadcast feetx for %v -- restarting payment",
			fp.ticket)
		fp.schedule("reconcile payment", fp.reconcilePayment)
		return fmt.Errorf("VSP failed to broadcast fee tx for %v", fp.ticket)
	default:
		fp.client.log.Warnf("VSP responded with unknown FeeTxStatus %q for %v",
			status.FeeTxStatus, fp.ticket)
		return fmt.Errorf("unknown VSP FeeTxStatus %q", status.FeeTxStatus)
	}
}

func marshalTx(tx *wire.MsgTx) (string, error) {
	var buf bytes.Buffer
	buf.Grow(tx.SerializeSize() * 2)
	err := tx.Serialize(hex.NewEncoder(&buf))
	return buf.String(), err
}
