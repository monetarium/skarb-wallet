// Copyright (c) 2026 The Monetarium developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/decred/vspd/types/v3"
	"github.com/monetarium/monetarium-node/chaincfg/chainhash"
	"github.com/monetarium/monetarium-node/wire"
	werrors "github.com/monetarium/monetarium-wallet/errors"
	"github.com/monetarium/monetarium-wallet/wallet/udb"
	"github.com/monetarium/monetarium-wallet/wallet/walletdb"
)

// signedFeeTx returns a fee tx that looks finished to the client: it has an
// output and a signed input.
func signedFeeTx(id byte) *wire.MsgTx {
	tx := wire.NewMsgTx()
	tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{id}}, 2e7, []byte{0x51}))
	tx.AddTxOut(wire.NewTxOut(1e7, []byte{0x51}))
	return tx
}

// insertUnmined records tx as an unmined wallet transaction.
func insertUnmined(ctx context.Context, t *testing.T, w *Wallet, tx *wire.MsgTx) {
	t.Helper()
	err := walletdb.Update(ctx, w.db, func(dbtx walletdb.ReadWriteTx) error {
		rec, err := udb.NewTxRecordFromMsgTx(tx, time.Now())
		if err != nil {
			return err
		}
		return w.txStore.InsertMemPoolTx(dbtx, rec)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func feeStatus(ctx context.Context, t *testing.T, ticket *VSPTicket) udb.FeeStatus {
	t.Helper()
	info, err := ticket.VSPTicketInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return udb.FeeStatus(info.FeeTxStatus)
}

func timerSet(fp *vspFeePayment) bool {
	fp.timerMu.Lock()
	defer fp.timerMu.Unlock()
	return fp.timer != nil
}

// TestVSPProcessDropsCallerFeeTxOnQuoteFailure checks that when the VSP quote
// fails, Process does not leave the purchase's fee tx for the scheduled
// attempt: the purchase unlocks those inputs and may spend them.
func TestVSPProcessDropsCallerFeeTxOnQuoteFailure(t *testing.T) {
	ctx := context.Background()
	c := testVSPClient(ctx, t, func(string, []byte) (int, any) {
		return apiError(types.ErrInternalError)
	})
	ticket := testVSPTicket(ctx, t, c, 1)
	purchaseTx := wire.NewMsgTx()
	purchaseTx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{0xca}}, 1e8, nil))

	if err := c.Process(ctx, ticket, purchaseTx); err == nil {
		t.Fatal("Process: want an error from the fake VSP")
	}
	c.mu.Lock()
	fp := c.jobs[*ticket.Hash()]
	c.mu.Unlock()
	if fp == nil {
		t.Fatal("Process did not register the payment")
	}
	fp.stop()
	fp.mu.Lock()
	kept := fp.feeTx == purchaseTx
	fp.mu.Unlock()
	if kept {
		t.Fatal("the scheduled attempt would build on the purchase's fee tx")
	}
}

// TestVSPReconcileRebuildsUnsignedFeeTx checks that a fee tx with outputs but
// no signatures is rebuilt instead of being sent to the VSP.
func TestVSPReconcileRebuildsUnsignedFeeTx(t *testing.T) {
	ctx := context.Background()
	var payFeeCalls atomic.Int32
	c := testVSPClient(ctx, t, func(path string, _ []byte) (int, any) {
		if path == "/api/v3/payfee" {
			payFeeCalls.Add(1)
		}
		return apiError(types.ErrInternalError)
	})
	ticket := testVSPTicket(ctx, t, c, 1)
	unsigned := wire.NewMsgTx()
	unsigned.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{0xcb}}, 2e7, nil))
	unsigned.AddTxOut(wire.NewTxOut(1e7, []byte{0x51}))
	fp := &vspFeePayment{
		client:  c,
		ctx:     ctx,
		ticket:  ticket,
		policy:  c.policy,
		params:  c.wallet.chainParams,
		fee:     1e7,
		feeAddr: ticket.votingAddr,
		feeTx:   unsigned,
	}
	c.jobs[*ticket.Hash()] = fp

	_ = fp.reconcilePayment()
	fp.stop()
	if n := payFeeCalls.Load(); n != 0 {
		t.Fatalf("an unsigned fee tx was sent to the VSP %d time(s)", n)
	}
}

// TestVSPProcessWaitsForScheduledAttempt checks that Process does not work on
// a payment while a scheduled attempt for the same ticket is running.
func TestVSPProcessWaitsForScheduledAttempt(t *testing.T) {
	ctx := context.Background()
	var feeAddressCalls atomic.Int32
	c := testVSPClient(ctx, t, func(path string, _ []byte) (int, any) {
		if path == "/api/v3/feeaddress" {
			feeAddressCalls.Add(1)
		}
		return apiError(types.ErrInternalError)
	})
	ticket := testVSPTicket(ctx, t, c, 1)
	fp := &vspFeePayment{
		client: c,
		ctx:    ctx,
		ticket: ticket,
		policy: c.policy,
		params: c.wallet.chainParams,
	}
	c.jobs[*ticket.Hash()] = fp

	fp.procMu.Lock() // a scheduled attempt is running
	done := make(chan struct{})
	go func() {
		_ = c.Process(ctx, ticket, nil)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	if n := feeAddressCalls.Load(); n != 0 {
		t.Fatal("Process worked on the payment while an attempt was running")
	}
	fp.procMu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Process did not resume after the attempt finished")
	}
	if n := feeAddressCalls.Load(); n != 1 {
		t.Fatalf("Process made %d feeaddress requests after the attempt, want 1", n)
	}
}

// TestVSPExpiredFeeMarksErrored checks that after the VSP reports the fee as
// expired, the ticket is no longer recorded as paid with the abandoned tx.
func TestVSPExpiredFeeMarksErrored(t *testing.T) {
	ctx := context.Background()
	c := testVSPClient(ctx, t, func(string, []byte) (int, any) {
		return apiError(types.ErrFeeExpired)
	})
	ticket := testVSPTicket(ctx, t, c, 1)
	feeTx := signedFeeTx(0xcc)
	if err := ticket.UpdateFeePaid(ctx, feeTx.TxHash(), c.URL, c.PubKey); err != nil {
		t.Fatal(err)
	}
	fp := &vspFeePayment{
		client:  c,
		ctx:     ctx,
		ticket:  ticket,
		policy:  c.policy,
		params:  c.wallet.chainParams,
		fee:     1e7,
		feeAddr: ticket.votingAddr,
		feeHash: feeTx.TxHash(),
		feeTx:   feeTx,
	}

	var apiErr types.ErrorResponse
	if err := fp.submitPayment(); !errors.As(err, &apiErr) || apiErr.Code != types.ErrFeeExpired {
		t.Fatalf("submitPayment: want ErrFeeExpired, got %v", err)
	}
	if got := feeStatus(ctx, t, ticket); got != udb.VSPFeeProcessErrored {
		t.Fatalf("fee status after an expired fee = %v, want errored", got)
	}
}

// TestVSPRejectedFeeTxIsAbandoned checks that a fee tx the VSP rejects is
// removed from the wallet so its inputs can be spent again.
func TestVSPRejectedFeeTxIsAbandoned(t *testing.T) {
	ctx := context.Background()
	c := testVSPClient(ctx, t, func(string, []byte) (int, any) {
		return apiError(types.ErrInvalidFeeTx)
	})
	ticket := testVSPTicket(ctx, t, c, 1)
	feeTx := signedFeeTx(0xcd)
	insertUnmined(ctx, t, c.wallet, feeTx)
	feeHash := feeTx.TxHash()
	fp := &vspFeePayment{
		client:  c,
		ctx:     ctx,
		ticket:  ticket,
		policy:  c.policy,
		params:  c.wallet.chainParams,
		fee:     1e7,
		feeAddr: ticket.votingAddr,
		feeHash: feeHash,
		feeTx:   feeTx,
	}
	c.jobs[*ticket.Hash()] = fp

	if err := fp.reconcilePayment(); err == nil {
		t.Fatal("reconcilePayment: want ErrInvalidFeeTx")
	}
	fp.stop()
	if _, _, err := c.wallet.TxBlock(ctx, &feeHash); !werrors.Is(err, werrors.NotExist) {
		t.Fatalf("rejected fee tx is still in the wallet (err %v)", err)
	}
}

// TestVSPProcessManagedTicketsSkipsBadTicket checks that an error for one
// ticket does not stop the remaining tickets from being resumed.
func TestVSPProcessManagedTicketsSkipsBadTicket(t *testing.T) {
	ctx := context.Background()
	var badHash string
	c := testVSPClient(ctx, t, func(path string, body []byte) (int, any) {
		if path != "/api/v3/ticketstatus" {
			return apiError(types.ErrInternalError)
		}
		var req types.TicketStatusRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return apiError(types.ErrBadRequest)
		}
		if req.TicketHash == badHash {
			return http.StatusOK, types.TicketStatusResponse{
				FeeTxStatus: "confirmed",
				FeeTxHash:   "not a hash",
				Request:     body,
			}
		}
		return http.StatusOK, types.TicketStatusResponse{FeeTxStatus: "none", Request: body}
	})
	bad := testVSPTicket(ctx, t, c, 1)
	unpaid := testVSPTicket(ctx, t, c, 2)
	badHash = bad.Hash().String()

	if err := c.ProcessManagedTickets(ctx, []*VSPTicket{bad, unpaid}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	_, resumed := c.jobs[*unpaid.Hash()]
	c.mu.Unlock()
	if !resumed {
		t.Fatal("a bad fee hash for one ticket stopped the next ticket")
	}
}

// TestVSPFeeTooHighStopsRetries checks that a fee above the configured maximum
// is not retried every few minutes but marked errored for the next unlock.
func TestVSPFeeTooHighStopsRetries(t *testing.T) {
	ctx := context.Background()
	var ticket *VSPTicket
	c := testVSPClient(ctx, t, func(path string, body []byte) (int, any) {
		if path != "/api/v3/feeaddress" {
			return apiError(types.ErrInternalError)
		}
		return http.StatusOK, types.FeeAddressResponse{
			Timestamp:  time.Now().Unix(),
			FeeAddress: ticket.votingAddr.String(),
			FeeAmount:  2e8, // above the client's 1e8 MaxFee
			Expiration: time.Now().Add(time.Hour).Unix(),
			Request:    body,
		}
	})
	ticket = testVSPTicket(ctx, t, c, 1)
	if err := ticket.UpdateFeeStarted(ctx, chainhash.Hash{}, c.URL, c.PubKey); err != nil {
		t.Fatal(err)
	}
	fp := &vspFeePayment{
		client: c,
		ctx:    ctx,
		ticket: ticket,
		policy: c.policy,
		params: c.wallet.chainParams,
	}
	c.jobs[*ticket.Hash()] = fp

	if err := fp.reconcilePayment(); !errors.Is(err, errFeeTooHigh) {
		t.Fatalf("reconcilePayment: want errFeeTooHigh, got %v", err)
	}
	if timerSet(fp) {
		t.Fatal("a fee above the maximum was scheduled for another attempt")
	}
	if got := feeStatus(ctx, t, ticket); got != udb.VSPFeeProcessErrored {
		t.Fatalf("fee status = %v, want errored", got)
	}
}

// TestVSPPrunedTicketIsRemoved checks that a ticket the wallet no longer has
// is dropped from the client and the database instead of being retried.
func TestVSPPrunedTicketIsRemoved(t *testing.T) {
	ctx := context.Background()
	c := testVSPClient(ctx, t, func(string, []byte) (int, any) {
		return apiError(types.ErrInternalError)
	})
	kept := testVSPTicket(ctx, t, c, 1)
	// Same ticket data under a hash the wallet does not have.
	h := chainhash.Hash{0xdd}
	pruned := &VSPTicket{
		hash:           &h,
		rawTx:          kept.rawTx,
		parentTx:       kept.parentTx,
		commitmentAddr: kept.commitmentAddr,
		votingAddr:     kept.votingAddr,
		wallet:         c.wallet,
	}
	if err := pruned.UpdateFeeStarted(ctx, chainhash.Hash{}, c.URL, c.PubKey); err != nil {
		t.Fatal(err)
	}
	fp := &vspFeePayment{
		client: c,
		ctx:    ctx,
		ticket: pruned,
		policy: c.policy,
		params: c.wallet.chainParams,
	}
	c.jobs[h] = fp

	if err := fp.reconcilePayment(); !errors.Is(err, errStopped) {
		t.Fatalf("reconcilePayment: want errStopped, got %v", err)
	}
	c.mu.Lock()
	_, still := c.jobs[h]
	c.mu.Unlock()
	if still {
		t.Fatal("the pruned ticket is still managed by the client")
	}
	if _, err := pruned.VSPTicketInfo(ctx); !werrors.Is(err, werrors.NotExist) {
		t.Fatalf("the pruned ticket's VSP record was not deleted (err %v)", err)
	}
}
