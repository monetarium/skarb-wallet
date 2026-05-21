package dcr

import (
	"fmt"
	"math"
	"math/big"

	w "github.com/monetarium/monetarium-wallet/wallet"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/monetarium-node/chaincfg/chainhash"
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
		walletInputs[i] = &sharedW.WInput{
			Index:    int32(input.Index),
			AmountIn: skaOrVARAtoms(input.PreviousSKAAmount, int64(input.PreviousAmount), "WInput"),
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

	if decodedTx.TicketSpentHash != "" {
		ticketPurchaseTx, err := asset.GetTransactionRaw(decodedTx.TicketSpentHash)
		if err != nil {
			return nil, err
		}

		timeDifferenceInSeconds := decodedTx.Timestamp - ticketPurchaseTx.Timestamp
		decodedTx.DaysToVoteOrRevoke = int32(timeDifferenceInSeconds / 86400) // seconds to days conversion

		// calculate reward
		var ticketInvestment int64
		for _, input := range ticketPurchaseTx.Inputs {
			if input.AccountNumber > -1 {
				ticketInvestment += input.Amount
			}
		}

		var ticketOutput int64
		for _, output := range walletTx.Outputs {
			if output.AccountNumber > -1 {
				ticketOutput += output.AmountOut
			}
		}

		decodedTx.VoteReward = ticketOutput - ticketInvestment

		// update ticket with spender hash
		ticketPurchaseTx.TicketSpender = decodedTx.Hash
		_, _ = asset.GetWalletDataDb().SaveOrUpdate(&sharedW.Transaction{}, ticketPurchaseTx)
	}

	return decodedTx, nil
}
