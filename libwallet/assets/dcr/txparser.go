package dcr

import (
	"fmt"
	"math"
	"math/big"

	"github.com/monetarium/monetarium-node/chaincfg/chainhash"
	w "github.com/monetarium/monetarium-wallet/wallet"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
)

// skaOrVARAtoms returns the atom count for a wallet input/output as int64,
// preferring the SKA big.Int when present and falling back to the VAR int64
// otherwise. SKA values larger than MaxInt64 (a single UTXO holding more than
// ~9.22 SKA) get clamped to MaxInt64 and a warning is logged so the
// truncation does not silently zero the row; total-amount arithmetic over a
// clamped value is still wrong, but at least the row classifies as
// "Received"/"Sent" instead of being misread as zero-value. context describes
// the call site for the log.
func skaOrVARAtoms(skaAtoms *big.Int, varAtoms int64, context string) int64 {
	if skaAtoms == nil {
		return varAtoms
	}
	if skaAtoms.IsInt64() {
		return skaAtoms.Int64()
	}
	log.Warnf("%s: SKA value %s overflows int64; clamping to MaxInt64. "+
		"Phase-1 send/receive UI is limited to ~9.22 SKA per row; "+
		"plumb big.Int through the tx-summary path to lift this cap.",
		context, skaAtoms.String())
	return math.MaxInt64
}

func (asset *Asset) decodeTransactionWithTxSummary(txSummary *w.TransactionSummary,
	blockHash *chainhash.Hash,
) (*sharedW.Transaction, error) {
	var blockHeight int32 = sharedW.UnminedTxHeight
	if blockHash != nil {
		blockIdentifier := w.NewBlockIdentifierFromHash(blockHash)
		ctx, _ := asset.ShutdownContextWithCancel()
		blockInfo, err := asset.Internal().DCR.BlockInfo(ctx, blockIdentifier)
		if err != nil {
			log.Error(err)
		} else {
			blockHeight = blockInfo.Height
		}
	}

	walletInputs := make([]*sharedW.WInput, len(txSummary.MyInputs))
	for i, input := range txSummary.MyInputs {
		accountNumber := int32(input.PreviousAccount)
		accountName, err := asset.AccountName(accountNumber)
		if err != nil {
			log.Error(err)
		}

		// PreviousAmount carries VAR atoms (zero for SKA inputs); the SKA
		// atom value lives in PreviousSKAAmount as a *big.Int. If only the
		// VAR field is read, every SKA-input wallet row reports
		// AmountIn=0, which then propagates to total-input arithmetic and
		// causes TransactionAmountAndDirection to misclassify SKA receives
		// (zero in, zero out, zero fee → "Transferred"/"Sent" rather than
		// "Received"). Phase 1 keeps the int64 channel: pick whichever
		// field is set, log loudly if an SKA value overflows int64 (caps
		// at ~9.22 SKA, see AmountAtomForCoinType in utils.go).
		// AmountInBig captures the lossless previous SKA atom value (if any)
		// so DecodeTransaction's big.Int classifier doesn't have to trust
		// the wire-side msgTx.TxIn[*].SKAValueIn, which lives in witness
		// data and gets stripped by SPV mempool relay. Without this, the
		// sender's view of an in-mempool SKA send shows direction=Received
		// with the change amount until the tx confirms (the bug user
		// reported as "showed change as own spend in mempool, correct
		// after confirm"). VAR inputs leave it empty — int64 is enough.
		var amountInBig string
		if input.PreviousSKAAmount != nil && input.PreviousSKAAmount.Sign() > 0 {
			amountInBig = input.PreviousSKAAmount.String()
		}
		walletInputs[i] = &sharedW.WInput{
			Index:       int32(input.Index),
			AmountIn:    skaOrVARAtoms(input.PreviousSKAAmount, int64(input.PreviousAmount), "WInput"),
			AmountInBig: amountInBig,
			WAccount: &sharedW.WAccount{
				AccountNumber: accountNumber,
				AccountName:   accountName,
			},
		}
	}

	walletOutputs := make([]*sharedW.WOutput, len(txSummary.MyOutputs))
	for i, output := range txSummary.MyOutputs {
		accountNumber := int32(output.Account)
		accountName, err := asset.AccountName(accountNumber)
		if err != nil {
			log.Error(err)
		}

		walletOutputs[i] = &sharedW.WOutput{
			Index:     int32(output.Index),
			AmountOut: skaOrVARAtoms(output.SKAAmount, int64(output.Amount), "WOutput"),
			Internal:  output.Internal,
			Address:   output.Address.String(),
			WAccount: &sharedW.WAccount{
				AccountNumber: accountNumber,
				AccountName:   accountName,
			},
		}
	}

	walletTx := &sharedW.TxInfoFromWallet{
		WalletID:    asset.ID,
		BlockHeight: blockHeight,
		Timestamp:   txSummary.Timestamp,
		Hex:         fmt.Sprintf("%x", txSummary.Transaction),
		Inputs:      walletInputs,
		Outputs:     walletOutputs,
	}

	decodedTx, err := asset.DecodeTransaction(walletTx, asset.chainParams)
	if err != nil {
		return nil, err
	}

	// DecodeTransaction never populates TicketSpender (it only sets the
	// vote-side TicketSpentHash). Without this back-fill a freshly decoded
	// voted ticket reads as spender-less, so TxFilterLive/TxFilterExpired
	// match it and the UI flips "Voted" back to "Live" — and the txindex
	// re-index path (SaveOrUpdate = delete-then-save) would wipe the
	// persisted spender the vote decode below had written.
	if decodedTx.Type == TxTypeTicketPurchase {
		if spender, _ := asset.TicketSpender(decodedTx.Hash); spender != nil {
			decodedTx.TicketSpender = spender.Hash
		}
	}

	if decodedTx.TicketSpentHash != "" {
		ticketPurchaseTx, err := asset.GetTransactionRaw(decodedTx.TicketSpentHash)
		if err != nil {
			return nil, err
		}

		timeDifferenceInSeconds := decodedTx.Timestamp - ticketPurchaseTx.Timestamp
		decodedTx.DaysToVoteOrRevoke = int32(timeDifferenceInSeconds / 86400) // seconds to days conversion

		var ticketOutput int64
		for _, output := range walletTx.Outputs {
			if output.AccountNumber > -1 {
				ticketOutput += output.AmountOut
			}
		}

		// The reward is what the vote pays beyond the ticket price coming
		// home: vote output − ticket price. The ticket price is the SStx's
		// stake-submission value (tx.Amount = TxOut[0].Value, decodetx.go).
		// It must NOT be the sum of the purchase tx's wallet inputs — those
		// also cover the purchase fee (and any change), which silently
		// charged that fee against every displayed reward.
		decodedTx.VoteReward = ticketOutput - ticketPurchaseTx.Amount

		// update ticket with spender hash
		ticketPurchaseTx.TicketSpender = decodedTx.Hash
		_, _ = asset.GetWalletDataDb().SaveOrUpdate(&sharedW.Transaction{}, ticketPurchaseTx)
	}

	return decodedTx, nil
}
