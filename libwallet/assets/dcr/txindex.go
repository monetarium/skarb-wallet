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
	currentTxParserVersion int32 = 4
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

	startBlock := w.NewBlockIdentifierFromHeight(beginHeight)
	endBlock := w.NewBlockIdentifierFromHeight(endHeight)

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
