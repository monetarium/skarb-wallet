package dcr

import (
	"fmt"
	"time"

	"github.com/monetarium/monetarium-node/chaincfg/chainhash"
	"github.com/monetarium/monetarium-node/txscript/stdaddr"
	"github.com/monetarium/monetarium-wallet/wallet/udb"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
)

const vspFeeCacheTTL = 2 * time.Second

// VSPFeeTxPhase is the UI lifecycle of a VSP fee payment transaction.
type VSPFeeTxPhase int

const (
	// VSPFeePhaseNone — not a VSP fee payment.
	VSPFeePhaseNone VSPFeeTxPhase = iota
	// VSPFeePhasePending — fee tx exists but the split that funded it is
	// not yet Completed (required confirmations).
	VSPFeePhasePending
	// VSPFeePhasePendingByVSP — split is complete; the VSP has the signed
	// fee tx and has not yet put it in the mempool.
	VSPFeePhasePendingByVSP
	// VSPFeePhaseUnconfirmed — fee tx is in the mempool (published, not mined).
	VSPFeePhaseUnconfirmed
	// VSPFeePhaseMined — fee tx has a block height.
	VSPFeePhaseMined
)

// IsVSPFeePayment reports whether hash is the fee transaction of a
// VSP-managed ticket.
func (asset *Asset) IsVSPFeePayment(hash string) bool {
	if hash == "" || !asset.WalletOpened() {
		return false
	}
	asset.refreshVSPFeeCache()
	asset.vspFeeMu.Lock()
	defer asset.vspFeeMu.Unlock()
	_, ok := asset.vspFeeHashSet[hash]
	return ok
}

// VSPFeeTxPhase returns the UI phase of tx if it is a VSP fee payment.
func (asset *Asset) VSPFeeTxPhase(tx *sharedW.Transaction) VSPFeeTxPhase {
	if tx == nil || !asset.IsVSPFeePayment(tx.Hash) {
		return VSPFeePhaseNone
	}
	if tx.BlockHeight != sharedW.UnminedTxHeight {
		return VSPFeePhaseMined
	}
	asset.refreshVSPFeeCache()
	asset.vspFeeMu.Lock()
	unpublished := asset.vspFeeUnpublished[tx.Hash]
	asset.vspFeeMu.Unlock()
	if !unpublished {
		return VSPFeePhaseUnconfirmed
	}
	if asset.vspFeeSplitCompleted(tx) {
		return VSPFeePhasePendingByVSP
	}
	return VSPFeePhasePending
}

func (asset *Asset) refreshVSPFeeCache() {
	if !asset.WalletOpened() {
		return
	}
	asset.vspFeeMu.Lock()
	fresh := asset.vspFeeHashSet != nil && time.Since(asset.vspFeeCacheAt) < vspFeeCacheTTL
	asset.vspFeeMu.Unlock()
	if fresh {
		return
	}

	hashSet := make(map[string]struct{})
	unpublished := make(map[string]bool)
	ctx, _ := asset.ShutdownContextWithCancel()
	err := asset.Internal().DCR.ForEachVSPTicket(ctx, func(_ chainhash.Hash, ticket *udb.VSPTicket) error {
		if ticket == nil || ticket.FeeHash == (chainhash.Hash{}) {
			return nil
		}
		h := ticket.FeeHash.String()
		hashSet[h] = struct{}{}
		isUnpub, err := asset.Internal().DCR.TxUnpublished(ctx, &ticket.FeeHash)
		if err != nil {
			log.Debugf("TxUnpublished(%s): %v", h, err)
			unpublished[h] = true
			return nil
		}
		unpublished[h] = isUnpub
		return nil
	})
	if err != nil {
		log.Warnf("[%d] refreshVSPFeeCache: %v", asset.ID, err)
		return
	}
	asset.vspFeeMu.Lock()
	asset.vspFeeHashSet = hashSet
	asset.vspFeeUnpublished = unpublished
	asset.vspFeeCacheAt = time.Now()
	asset.vspFeeMu.Unlock()
}

func (asset *Asset) invalidateVSPFeeCache() {
	asset.vspFeeMu.Lock()
	asset.vspFeeCacheAt = time.Time{}
	asset.vspFeeSplitDone = nil
	asset.vspFeeMu.Unlock()
}

// vspFeeSplitCompleted reports whether the split (or other tx) that funded
// this fee payment has the required confirmations. Missing split rows are
// treated as complete so the status cannot stick on Pending forever.
func (asset *Asset) vspFeeSplitCompleted(tx *sharedW.Transaction) bool {
	if tx == nil || len(tx.Inputs) == 0 {
		return true
	}
	asset.vspFeeMu.Lock()
	if done, ok := asset.vspFeeSplitDone[tx.Hash]; ok {
		asset.vspFeeMu.Unlock()
		return done
	}
	asset.vspFeeMu.Unlock()

	prev := tx.Inputs[0].PreviousTransactionHash
	done := true
	if prev != "" {
		var split sharedW.Transaction
		if err := asset.GetWalletDataDb().FindOne("Hash", prev, &split); err == nil {
			need := asset.RequiredConfirmations()
			done = Confirmations(asset.GetBestBlockHeight(), &split) >= need
		}
	}
	asset.vspFeeMu.Lock()
	if asset.vspFeeSplitDone == nil {
		asset.vspFeeSplitDone = make(map[string]bool)
	}
	asset.vspFeeSplitDone[tx.Hash] = done
	asset.vspFeeMu.Unlock()
	return done
}

// ReconcileVSPFeeTransactions keeps VSP fee-payment rows in walletdata in
// sync with the wallet core and SPV filter:
//  1. If the core already has the fee tx mined, rewrite the UI row (fixes
//     "unconfirmed until rescan" when only the storm index was stale).
//  2. If it is still unmined, watch its output scripts so the next matching
//     compact filter fetches the block.
//  3. If the VSP already confirmed the fee and the wallet still has it
//     unmined, one-shot-rescan from the ticket height (capped).
func (asset *Asset) ReconcileVSPFeeTransactions() error {
	if !asset.WalletOpened() {
		return nil
	}
	if !asset.vspFeeReconcileBusy.CompareAndSwap(false, true) {
		return nil
	}
	defer asset.vspFeeReconcileBusy.Store(false)

	asset.invalidateVSPFeeCache()
	asset.refreshVSPFeeCache()

	ctx, _ := asset.ShutdownContextWithCancel()
	n, netErr := asset.Internal().DCR.NetworkBackend()
	var watchAddrs []stdaddr.Address
	var stuckMinHeight int32 = -1
	var stuckCount int

	err := asset.Internal().DCR.ForEachVSPTicket(ctx, func(ticketHash chainhash.Hash, ticket *udb.VSPTicket) error {
		if ticket == nil || ticket.FeeHash == (chainhash.Hash{}) {
			return nil
		}
		feeStr := ticket.FeeHash.String()
		coreTx, err := asset.GetTransactionRaw(feeStr)
		if err != nil {
			log.Debugf("[%d] ReconcileVSPFee: fee tx %s not in wallet: %v", asset.ID, feeStr, err)
			return nil
		}
		if coreTx.BlockHeight != sharedW.UnminedTxHeight {
			if _, err := asset.GetWalletDataDb().SaveOrUpdate(&sharedW.Transaction{}, coreTx); err != nil {
				log.Warnf("[%d] ReconcileVSPFee: save mined fee tx %s: %v", asset.ID, feeStr, err)
			} else {
				log.Infof("[%d] ReconcileVSPFee: synced mined VSP fee tx %s at height %d into walletdata",
					asset.ID, feeStr, coreTx.BlockHeight)
				asset.publishTransactionConfirmed(feeStr, coreTx.BlockHeight)
			}
			return nil
		}

		for _, out := range coreTx.Outputs {
			if out.Address == "" {
				continue
			}
			addr, err := stdaddr.DecodeAddress(out.Address, asset.chainParams)
			if err != nil {
				continue
			}
			watchAddrs = append(watchAddrs, addr)
		}

		// VSP "confirmed" means the fee is on-chain. If the wallet still
		// has it unmined, SPV missed the compact filter — rescan from
		// the ticket height. Do not key off ticket age alone: the VSP
		// holds the signed tx until ~6 ticket confirmations, and a
		// premature rescan would burn the one-shot flag.
		if ticket.FeeTxStatus == uint32(udb.VSPFeeProcessConfirmed) {
			stuckCount++
			ticketTx, err := asset.GetTransactionRaw(ticketHash.String())
			if err == nil && ticketTx.BlockHeight > 0 {
				if stuckMinHeight < 0 || ticketTx.BlockHeight < stuckMinHeight {
					stuckMinHeight = ticketTx.BlockHeight
				}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("ReconcileVSPFeeTransactions: %w", err)
	}

	if netErr == nil && len(watchAddrs) > 0 {
		if err := n.LoadTxFilter(ctx, false, watchAddrs, nil); err != nil {
			log.Warnf("[%d] ReconcileVSPFee: watch fee outputs: %v", asset.ID, err)
		} else {
			log.Infof("[%d] ReconcileVSPFee: watching %d VSP fee output address(es)", asset.ID, len(watchAddrs))
		}
	}

	if stuckCount > 0 && !asset.IsRescanning() && asset.vspFeeRescanned.CompareAndSwap(false, true) {
		best := asset.GetBestBlockHeight()
		start := stuckMinHeight
		if start < 0 {
			start = best - 2048
		}
		if start < 0 {
			start = 0
		}
		if best-start > 2048 {
			start = best - 2048
		}
		log.Warnf("[%d] %d VSP fee tx(s) confirmed by VSP but still unmined locally; rescanning from height %d",
			asset.ID, stuckCount, start)
		if err := asset.RescanBlocksFromHeight(start); err != nil {
			log.Warnf("[%d] ReconcileVSPFee: rescan: %v", asset.ID, err)
			asset.vspFeeRescanned.Store(false)
		}
	}
	return nil
}
