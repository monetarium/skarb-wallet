package dcr

import (
	w "github.com/monetarium/monetarium-wallet/wallet"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/monetarium-node/chaincfg/chainhash"
)

// txParserVersionConfigKey + currentTxParserVersion drive a one-shot
// reindex when the tx-decoder semantics change in a way that affects rows
// already in walletdata.db. Bump currentTxParserVersion whenever decoded
// tx fields change (Amount / Direction / CoinType / etc.) — on next sync
// the wallet drops the saved tx rows and rebuilds them from the wallet's
// authoritative tx store. Without this, existing rows from before the
// change keep their stale shape forever and the UI displays the wrong
// direction / address for old transactions.
const (
	txParserVersionConfigKey = "tx_parser_version"

	// v1: pre-multi-coin (every tx assumed VAR).
	// v2: SKA inputs/outputs read from SKAValueIn / SKAValue instead of
	//     the int64 ValueIn/Value (which are zero for SKA), so SKA
	//     receives stop being misclassified as Sent/Transferred and the
	//     "From" panel renders correctly. Forces a one-shot reindex on
	//     upgrade so already-saved rows pick up the new amount/direction.
	// v3: TxInput.SenderAddress populated by addresshelper from each
	//     P2PKH input's sigScript so received-transaction details can
	//     surface a real sender address. Old rows have empty SenderAddress
	//     forever; bumping forces them to be re-decoded with the new
	//     populated field.
	// v4: Transaction.AmountAtoms / FeeAtoms + per-I/O AmountAtoms hold
	//     the lossless big.Int decimal string for SKA flows that exceed
	//     int64 (a single SKA UTXO holding > 9.22 SKA already overflows
	//     int64 at 1e18 atoms/coin). Before this, every such row was
	//     clamped to 9223372036854775807 atoms and the fee column read
	//     "0 SKA1" because in - out cancelled. Bump triggers a reindex
	//     so rows from emission txs and large SKA receives display the
	//     real numbers.
	// v5: dropped `storm:"unique"` from Transaction.TicketSpentHash —
	//     the previous schema saved a Hash-only stub for every non-stake
	//     tx beyond the first (Storm v1 enforces uniqueness even on
	//     empty values, so the second "" collided with the first and
	//     produced a partial write). Bump forces a clean reindex against
	//     the new (non-unique-indexed) schema so previously-stubbed
	//     sent txs come back with real data.
	// v6: v5 reindex used start-height=-1 to also pull unmined txs but
	//     that flag triggers RangeTransactions' BACKWARDS-iteration
	//     branch which then skips every mined block within the requested
	//     range — wallets that had been working on v5 lost their
	//     receive history. Fixed by using end-height=-1 instead (mined
	//     blocks iterate forward, then unmined are appended). Re-bump
	//     to repopulate the storm DB on upgrade so receive txs come back.
	currentTxParserVersion int32 = 6
)

func (asset *Asset) IndexTransactions() error {
	if !asset.WalletOpened() {
		return utils.ErrDCRNotInitialized
	}

	// Best-effort: if the saved tx-parser version is older than what
	// this build understands, drop the saved tx rows and reset the
	// index pointer. The normal indexing pass below then rebuilds
	// from the wallet's tx store with the current decoder. We do this
	// once per upgrade — the version is bumped to the current value at
	// the end so subsequent IndexTransactions() runs are a no-op for
	// the migration path. Failures here are logged but don't abort
	// indexing; a stale row is worse than a logged warning.
	storedVersion := asset.ReadInt32ConfigValueForKey(txParserVersionConfigKey, 1)
	if storedVersion < currentTxParserVersion {
		log.Infof("[%d] tx-parser upgrade %d → %d: clearing saved tx rows for one-shot reindex",
			asset.ID, storedVersion, currentTxParserVersion)
		if err := asset.GetWalletDataDb().ClearSavedTransactions(&sharedW.Transaction{}); err != nil {
			log.Warnf("[%d] tx-parser upgrade: ClearSavedTransactions failed: %v "+
				"(continuing with stale rows; you can manually re-trigger via Settings → Rescan)",
				asset.ID, err)
		} else {
			asset.SaveUserConfigValue(txParserVersionConfigKey, currentTxParserVersion)
		}
	}

	asset.dbMutex.Lock()
	defer asset.dbMutex.Unlock()

	ctx, _ := asset.ShutdownContextWithCancel()

	var totalIndex int32
	var txEndHeight uint32
	rangeFn := func(block *w.Block) (bool, error) {
		for _, transaction := range block.Transactions {

			var blockHash *chainhash.Hash
			if block.Header != nil {
				hash := block.Header.BlockHash()
				blockHash = &hash
			} else {
				blockHash = nil
			}

			tx, err := asset.decodeTransactionWithTxSummary(&transaction, blockHash)
			if err != nil {
				return false, err
			}

			_, err = asset.GetWalletDataDb().SaveOrUpdate(&sharedW.Transaction{}, tx)
			if err != nil {
				log.Errorf("[%d] Index tx replace tx err : %v", asset.ID, err)
				return false, err
			}

			totalIndex++
		}

		if block.Header != nil {
			txEndHeight = block.Header.Height
			err := asset.GetWalletDataDb().SaveLastIndexPoint(int32(txEndHeight))
			if err != nil {
				log.Errorf("[%d] Set tx index end block height error: ", asset.ID, err)
				return false, err
			}

			log.Debugf("[%d] Index saved for transactions in block %d", asset.ID, txEndHeight)
		}

		select {
		case <-ctx.Done():
			return true, ctx.Err()
		default:
			return false, nil
		}
	}

	beginHeight, err := asset.GetWalletDataDb().ReadIndexingStartBlock()
	if err != nil {
		log.Errorf("[%d] Get tx indexing start point error: %v", asset.ID, err)
		return err
	}

	endHeight := asset.GetBestBlockHeight()

	// monetarium-wallet's RangeTransactions semantics:
	//   - begin < 0  → iterates unmined FIRST, then runs block iteration
	//                  with begin normalized to MaxInt32 (which causes
	//                  the rangeBlockTransactions to take its BACKWARDS
	//                  branch, starting at MaxInt32 and going down — and
	//                  with end == real tip the only iterations that
	//                  satisfy `end <= height` are heights above tip,
	//                  so block iteration is effectively a no-op).
	//   - end < 0    → iterates blocks normally with begin..MaxInt32,
	//                  THEN appends unmined at the end.
	// We want BOTH mined and unmined when running a migration reindex.
	// Use end=-1 so block iteration covers the real chain forwards and
	// the post-iteration unmined pull happens via the second branch in
	// RangeTransactions. The earlier attempt at begin=-1 dropped every
	// mined tx because of the backwards-iteration trap above.
	startBlock := w.NewBlockIdentifierFromHeight(beginHeight)
	endNum := endHeight
	if beginHeight == 0 {
		endNum = -1
	}
	endBlock := w.NewBlockIdentifierFromHeight(endNum)

	defer func() {
		count, err := asset.GetWalletDataDb().Count(utils.TxFilterAll, asset.RequiredConfirmations(), endHeight, &sharedW.Transaction{})
		if err != nil {
			log.Errorf("[%d] Post-indexing tx count error :%v", asset.ID, err)
		} else if count > 0 {
			log.Infof("[%d] Transaction index finished at %d, %d transaction(s) indexed in total", asset.ID, endHeight, count)
		}

		err = asset.GetWalletDataDb().SaveLastIndexPoint(endHeight)
		if err != nil {
			log.Errorf("[%d] Set tx index end block height error: ", asset.ID, err)
		}
	}()

	log.Infof("[%d] Indexing transactions start height: %d, end height: %d", asset.ID, beginHeight, endHeight)
	return asset.Internal().DCR.GetTransactions(ctx, rangeFn, startBlock, endBlock)
}

func (asset *Asset) reindexTransactions() error {
	err := asset.GetWalletDataDb().ClearSavedTransactions(&sharedW.Transaction{})
	if err != nil {
		return err
	}

	return asset.IndexTransactions()
}
