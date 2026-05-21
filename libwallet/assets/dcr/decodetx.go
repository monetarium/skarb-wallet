package dcr

import (
	"fmt"

	w "github.com/monetarium/monetarium-wallet/wallet"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/libwallet/addresshelper"
	"github.com/monetarium/skarb-wallet/libwallet/txhelper"
	"github.com/monetarium/monetarium-node/blockchain/stake"
	"github.com/monetarium/monetarium-node/chaincfg"
	"github.com/monetarium/monetarium-node/dcrutil"
	"github.com/monetarium/monetarium-node/txscript/stdscript"
	"github.com/monetarium/monetarium-node/wire"
	"github.com/monetarium/monetarium-explorer/txhelpers"
)

const BlockValid = 1 << 0

// DecodeTransaction uses `walletTx.Hex` to retrieve detailed information for a transaction.
func (asset *Asset) DecodeTransaction(walletTx *sharedW.TxInfoFromWallet, netParams *chaincfg.Params) (*sharedW.Transaction, error) {
	msgTx, txFee, txSize, txFeeRate, err := txhelper.MsgTxFeeSizeRate(walletTx.Hex)
	if err != nil {
		return nil, err
	}

	inputs, totalWalletInput, totalWalletUnmixedInputs := asset.decodeTxInputs(msgTx, netParams, walletTx.Inputs)
	outputs, totalWalletOutput, totalWalletMixedOutputs, mixedOutputsCount := asset.decodeTxOutputs(msgTx, netParams, walletTx.Outputs)

	amount, direction := txhelper.TransactionAmountAndDirection(totalWalletInput, totalWalletOutput, int64(txFee))

	ssGenVersion, lastBlockValid, voteBits, ticketSpentHash := voteInfo(msgTx)

	// ticketSpentHash will be empty if this isn't a vote tx
	if stake.IsSSRtx(msgTx) {
		ticketSpentHash = msgTx.TxIn[0].PreviousOutPoint.Hash.String()
		// set first tx input as amount for revoked txs
		amount = msgTx.TxIn[0].ValueIn
	} else if stake.IsSStx(msgTx) {
		// set first tx output as amount for ticket txs
		amount = msgTx.TxOut[0].Value
	}

	isMixedTx, mixDenom, _ := txhelpers.IsMixTx(msgTx)

	txType := txhelper.FormatTransactionType(w.TxTransactionType(msgTx))
	if isMixedTx {
		txType = txhelper.TxTypeMixed

		mixChange := totalWalletOutput - totalWalletMixedOutputs
		txFee = dcrutil.Amount(totalWalletUnmixedInputs - (totalWalletMixedOutputs + mixChange))
	}

	// All outputs in a Monetarium tx share the same CoinType.
	var txCoinType uint8
	if len(msgTx.TxOut) > 0 {
		txCoinType = uint8(msgTx.TxOut[0].CoinType)
	}

	return &sharedW.Transaction{
		Hash:        msgTx.TxHash().String(),
		Type:        txType,
		Hex:         walletTx.Hex,
		Timestamp:   walletTx.Timestamp,
		BlockHeight: walletTx.BlockHeight,

		MixDenomination: mixDenom,
		MixCount:        mixedOutputsCount,

		Version:  int32(msgTx.Version),
		LockTime: int32(msgTx.LockTime),
		Expiry:   int32(msgTx.Expiry),
		Fee:      int64(txFee),
		FeeRate:  int64(txFeeRate),
		Size:     txSize,

		Direction: direction,
		Amount:    amount,
		Inputs:    inputs,
		Outputs:   outputs,
		CoinType:  txCoinType,

		VoteVersion:     int32(ssGenVersion),
		LastBlockValid:  lastBlockValid,
		VoteBits:        voteBits,
		TicketSpentHash: ticketSpentHash,
	}, nil
}

func (asset *Asset) decodeTxInputs(mtx *wire.MsgTx, netParams *chaincfg.Params, walletInputs []*sharedW.WInput) (inputs []*sharedW.TxInput, totalWalletInputs, totalWalletUnmixedInputs int64) {
	inputs = make([]*sharedW.TxInput, len(mtx.TxIn))
	unmixedAccountNumber := asset.ReadInt32ConfigValueForKey(sharedW.AccountMixerUnmixedAccount, -1)

	for i, txIn := range mtx.TxIn {
		// SKA inputs carry their atom value in SKAValueIn (*big.Int) and
		// have ValueIn=0. Reading ValueIn alone zeros every SKA input
		// row, which cascades into TransactionAmountAndDirection
		// classifying SKA receives as "Transferred"/"Sent" (because
		// outputTotal - inputTotal = 0 - 0 = 0 with fee=0) instead of
		// "Received". Phase 1 keeps int64-shaped Amount; the helper
		// logs and clamps when an SKA value overflows int64.
		//
		// SenderAddress is derived from the input's sigScript (which
		// reveals the spender's pubkey for P2PKH); it gives us a real
		// "From" address to show in the UI for received transactions
		// even though SPV mode never stores the sender's prior
		// outputs. Empty string for non-P2PKH inputs (coinbase, OP_RETURN
		// spend, multisig P2SH); the UI must handle the empty case.
		var senderAddress string
		if addr, err := addresshelper.SigScriptSenderAddress(txIn.SignatureScript, netParams); err != nil {
			log.Debugf("SigScriptSenderAddress(tx=%s input=%d): %v",
				mtx.TxHash(), i, err)
		} else {
			senderAddress = addr
		}
		input := &sharedW.TxInput{
			PreviousTransactionHash:  txIn.PreviousOutPoint.Hash.String(),
			PreviousTransactionIndex: int32(txIn.PreviousOutPoint.Index),
			PreviousOutpoint:         txIn.PreviousOutPoint.String(),
			Amount:                   skaOrVARAtoms(txIn.SKAValueIn, txIn.ValueIn, "TxInput"),
			AccountNumber:            -1, // correct account number is set below if this is a wallet output
			SenderAddress:            senderAddress,
		}

		// override account details if this is wallet input
		for _, walletInput := range walletInputs {
			if walletInput.Index == int32(i) {
				input.AccountNumber = walletInput.AccountNumber
				break
			}
		}

		if input.AccountNumber != -1 {
			totalWalletInputs += input.Amount
			if input.AccountNumber == unmixedAccountNumber {
				totalWalletUnmixedInputs += input.Amount
			}
		}

		inputs[i] = input
	}

	return
}

func (asset *Asset) decodeTxOutputs(mtx *wire.MsgTx, netParams *chaincfg.Params,
	walletOutputs []*sharedW.WOutput,
) (outputs []*sharedW.TxOutput, totalWalletOutput, totalWalletMixedOutputs int64, mixedOutputsCount int32) {
	outputs = make([]*sharedW.TxOutput, len(mtx.TxOut))
	txType := stake.DetermineTxType(mtx)
	mixedAccountNumber := asset.MixedAccountNumber()

	for i, txOut := range mtx.TxOut {
		// get address and script type for output
		var address, scriptType string
		if (txType == stake.TxTypeSStx) && (stake.IsStakeCommitmentTxOut(i)) {
			addr, err := stake.AddrFromSStxPkScrCommitment(txOut.PkScript, netParams)
			if err == nil {
				address = addr.String()
			}
			scriptType = stdscript.STStakeSubmissionPubKeyHash.String()
		} else {
			// Ignore the error here since an error means the script
			// couldn't parse and there is no additional information
			// about it anyways.
			scriptClass, addrs := stdscript.ExtractAddrs(txOut.Version, txOut.PkScript, netParams)
			if len(addrs) > 0 {
				address = addrs[0].String()
			}
			scriptType = scriptClass.String()
		}

		// Same SKA/VAR pivot as decodeTxInputs: SKA outputs have Value=0
		// and carry their atom value in SKAValue. Without this pivot
		// SKA receives show "0 SKA" in every row and the direction
		// classifier misreads zero in / zero out as not-received.
		var amount int64
		if txOut.CoinType.IsSKA() {
			amount = skaOrVARAtoms(txOut.SKAValue, 0, "TxOutput")
		} else {
			amount = txOut.Value
		}
		output := &sharedW.TxOutput{
			Index:         int32(i),
			Amount:        amount,
			Version:       int32(txOut.Version),
			ScriptType:    scriptType,
			Address:       address, // correct address, account name and number set below if this is a wallet output
			AccountNumber: -1,
			CoinType:      uint8(txOut.CoinType),
		}

		// override address and account details if this is wallet output
		for _, walletOutput := range walletOutputs {
			if walletOutput.Index == output.Index {
				output.Internal = walletOutput.Internal
				output.Address = walletOutput.Address
				output.AccountNumber = walletOutput.AccountNumber
				break
			}
		}

		if output.AccountNumber != -1 {
			totalWalletOutput += output.Amount
			if output.AccountNumber == mixedAccountNumber {
				totalWalletMixedOutputs += output.Amount
				mixedOutputsCount++
			}
		}

		outputs[i] = output
	}

	return
}

func voteInfo(msgTx *wire.MsgTx) (ssGenVersion uint32, lastBlockValid bool, voteBits string, ticketSpentHash string) {
	if stake.IsSSGen(msgTx) {
		ssGenVersion = stake.SSGenVersion(msgTx)
		bits := stake.SSGenVoteBits(msgTx)
		voteBits = fmt.Sprintf("%#04x", bits)
		lastBlockValid = bits&uint16(BlockValid) != 0
		ticketSpentHash = msgTx.TxIn[1].PreviousOutPoint.Hash.String()
	}
	return
}
