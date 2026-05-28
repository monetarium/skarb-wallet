package dcr

import (
	"fmt"
	"math"
	"math/big"

	w "github.com/monetarium/monetarium-wallet/wallet"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/libwallet/addresshelper"
	"github.com/monetarium/skarb-wallet/libwallet/txhelper"
	"github.com/monetarium/monetarium-node/blockchain/stake"
	"github.com/monetarium/monetarium-node/chaincfg"
	"github.com/monetarium/monetarium-node/chaincfg/chainhash"
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

	// Cross-check walletTx.Inputs ownership against this wallet's own tx
	// store BEFORE decoding inputs/outputs. Upstream's mempool path
	// (unminedTxDetails) derives Debits live by hitting existsRawUnspent /
	// existsRawUnminedCredit on the outpoint — which over-attributes for
	// receiver wallets in mempool, claiming sender's UTXOs as ours. After
	// confirmation the recorded Debits are correct (different code path)
	// so the bug only manifests in mempool. Symptom: a RECEIVER's mempool
	// view of an incoming tx shows direction=Sent with `external =
	// skaTotalOut - skaTotalWalletOut` ≈ the sender's CHANGE amount,
	// instead of the actual received value. Once confirmed the tx
	// re-decodes correctly.
	//
	// ownedWalletInputs is a strict subset of walletTx.Inputs: only
	// entries whose previous outpoint actually resolves in our tx store
	// via OutputInfo survive. Pass it down to decodeTxInputs and use it
	// for the SKA classifier too, so int64 (`totalWalletInput`) and big.Int
	// (`skaTotalWalletIn`) channels both stay consistent and stop
	// inflating Sent direction for the receiver. Coinbase inputs skip
	// the check — they're never wallet-owned.
	ownedWalletInputs := asset.filterOwnedWalletInputs(msgTx, walletTx.Inputs)

	// Mirror image of the input filter: upstream's unminedTxDetails reads
	// Credits via makeReadUnminedCreditIterator, which only fires for
	// outputs already persisted to the wallet's "unmined credits" bucket
	// by the broadcast path. The broadcast path *does* derive a change
	// address (persistReturnedChild) but the mempool-view read can happen
	// before that change is committed as a credit, so the wallet's own
	// change output silently disappears from txSummary.MyOutputs.
	//
	// Symptom: sender broadcasts "send 1 SKA", change ≈ <input − 1 − fee>
	// goes back to a wallet-owned address. The mempool tx-details row
	// shows BOTH outputs as external "To" and the amount field reports
	// `-(input - fee)` SKA — i.e. the entire input minus fee is rendered
	// as "sent externally", because skaTotalWalletOut is computed from
	// walletTx.Outputs (=MyOutputs) and that list is missing the change.
	// After confirmation a different code path rebuilds Credits and the
	// row flips to the correct `-1 SKA`.
	//
	// Defensive backstop: scan every msgTx.TxOut, derive the pkScript
	// address, ask the wallet via HaveAddress whether it owns the
	// address. Append a synthetic *sharedW.WOutput (Internal=true) for
	// anything wallet-owned that's not already in walletTx.Outputs.
	// Both consumers benefit:
	//   • decodeTxOutputs's int64 totalWalletOutput (the "amount"
	//     classifier in VAR-shaped paths)
	//   • the SKA classifier's big.Int skaTotalWalletOut below
	walletTx.Outputs = asset.augmentWalletOutputs(msgTx, walletTx.Outputs, netParams)

	inputs, totalWalletInput, totalWalletUnmixedInputs := asset.decodeTxInputs(msgTx, netParams, ownedWalletInputs)
	outputs, totalWalletOutput, totalWalletMixedOutputs, mixedOutputsCount := asset.decodeTxOutputs(msgTx, netParams, walletTx.Outputs)

	// Compute SKA tx aggregates losslessly: total-in, total-out, fee. Used
	// when amounts exceed int64 (single SKA UTXO above ~9.22 SKA), where
	// the int64 'amount' / 'fee' fields below would have been clamped to
	// MaxInt64 and the row would display "9.22 SKA1 fee=0" forever.
	var skaTotalIn, skaTotalOut *big.Int
	// skaTotalWalletIn / skaTotalWalletOut are the lossless sums of the
	// wallet-owned inputs and outputs. They drive the SKA-aware
	// direction/amount classifier below — using the int64 totals here
	// silently mis-classifies any SKA tx whose wallet inputs cross int64
	// (single UTXO > ~9.22 SKA): the input clamps at MaxInt64 while the
	// change-only output stays small, the subtraction flips sign, and the
	// outgoing tx renders as "Received <change>" with a "+" icon
	// (bug #5 / #6 in the v1 bug report).
	var skaTotalWalletIn, skaTotalWalletOut *big.Int
	isSKATx := len(msgTx.TxOut) > 0 && msgTx.TxOut[0].CoinType.IsSKA()
	if isSKATx {
		// Build a fast lookup of wallet-known input values keyed by msgTx
		// input index. Used twice below: once when summing the wallet's
		// side of inputs (skaTotalWalletIn) and once when summing the
		// network-wide total (skaTotalIn) so a missing wire SKAValueIn
		// doesn't silently zero an input we already know the value of.
		// Built from ownedWalletInputs so the OutputInfo guard above
		// flows through both totals.
		walletInByIndex := make(map[int32]*big.Int, len(ownedWalletInputs))
		for _, wi := range ownedWalletInputs {
			if wi == nil || wi.AmountInBig == "" {
				continue
			}
			if v, ok := new(big.Int).SetString(wi.AmountInBig, 10); ok {
				walletInByIndex[wi.Index] = v
			}
		}

		skaTotalIn = new(big.Int)
		for i, in := range msgTx.TxIn {
			// Prefer the wire-side SKAValueIn (it's the only number for
			// non-wallet inputs). When wire is empty (SPV mempool relay
			// strips witness data) and the wallet knows this input from
			// its own UTXO set, use that value instead — keeps the fee
			// estimate sensible until the block-included full-witness
			// version arrives.
			switch {
			case in.SKAValueIn != nil && in.SKAValueIn.Sign() > 0:
				skaTotalIn.Add(skaTotalIn, in.SKAValueIn)
			case walletInByIndex[int32(i)] != nil:
				skaTotalIn.Add(skaTotalIn, walletInByIndex[int32(i)])
			}
		}
		skaTotalOut = new(big.Int)
		for _, out := range msgTx.TxOut {
			if out.SKAValue != nil {
				skaTotalOut.Add(skaTotalOut, out.SKAValue)
			}
		}
		// Sum of OUR outputs in big.Int. Used for "received" amount.
		skaTotalWalletOut = new(big.Int)
		for _, wo := range walletTx.Outputs {
			if int(wo.Index) < len(msgTx.TxOut) {
				out := msgTx.TxOut[wo.Index]
				if out.SKAValue != nil {
					skaTotalWalletOut.Add(skaTotalWalletOut, out.SKAValue)
				}
			}
		}
		// Sum of OUR inputs in big.Int. Iterates ownedWalletInputs (the
		// filtered subset that passed the OutputInfo cross-check up top)
		// so cross-attributed Debits from upstream's mempool live-
		// derivation never inflate this total. For each owned input,
		// prefer the wallet-known previous amount (AmountInBig — lossless,
		// populated even in mempool); fall back to wire-side SKAValueIn
		// for confirmed-path consistency.
		skaTotalWalletIn = new(big.Int)
		for _, wi := range ownedWalletInputs {
			if int(wi.Index) >= len(msgTx.TxIn) {
				continue
			}
			if v, ok := walletInByIndex[wi.Index]; ok {
				skaTotalWalletIn.Add(skaTotalWalletIn, v)
				continue
			}
			in := msgTx.TxIn[wi.Index]
			if in.SKAValueIn != nil {
				skaTotalWalletIn.Add(skaTotalWalletIn, in.SKAValueIn)
			}
		}
	}

	var (
		amount    int64
		direction int32
		// feeBig is computed inside the isSKATx branch and reused below
		// when writing FeeAtoms and the int64 Fee override. Declared
		// outside the if so the post-classification block can see it.
		feeBig *big.Int
	)
	if isSKATx {
		// Compute the SKA fee in big.Int (network fee = total_in -
		// total_out; emission txs clamp to zero since they conjure value).
		feeBig = new(big.Int).Sub(skaTotalIn, skaTotalOut)
		if feeBig.Sign() < 0 {
			feeBig.SetInt64(0)
		}
		_, direction = txhelper.TransactionAmountAndDirectionBig(skaTotalWalletIn, skaTotalWalletOut, feeBig)
		// Keep the int64 'amount' field for back-compat readers (the
		// transaction-row label, sort keys, balance widgets). For SKA it
		// only carries the int64-representable slice of the true amount —
		// authoritative display lives in AmountAtoms (string) below. The
		// direction is already correct (it came from the big classifier),
		// so the wallet-side icon / sign on the row is right even when
		// the displayed amount truncates.
		amount, _ = txhelper.TransactionAmountAndDirection(totalWalletInput, totalWalletOutput, int64(txFee))
	} else {
		amount, direction = txhelper.TransactionAmountAndDirection(totalWalletInput, totalWalletOutput, int64(txFee))
	}

	ssGenVersion, lastBlockValid, voteBits, ticketSpentHash := voteInfo(msgTx)

	// Stake-stub branch must not fire for SKA. SKA txs can never be vote /
	// revocation / ticket-purchase by consensus (those are VAR-only stake
	// constructs), but the script-template matcher in stake.IsSStx /
	// stake.IsSSRtx is a pure structural check — an SKA tx whose first
	// input/output happens to tokenize like a stake tx (e.g. by sheer
	// coincidence of pkScript shape) would otherwise blow the just-
	// computed SKA `amount` away by overwriting it with msgTx.TxIn[0].
	// ValueIn (int64 zero for SKA) or msgTx.TxOut[0].Value (int64 zero).
	// AmountAtoms below stays correct via the lossless big.Int classifier
	// — but tx.Amount, which sort keys, the CSV exporter and balance
	// widgets read directly, would go to zero. Guard explicitly.
	if !isSKATx {
		// ticketSpentHash will be empty if this isn't a vote tx
		if stake.IsSSRtx(msgTx) {
			ticketSpentHash = msgTx.TxIn[0].PreviousOutPoint.Hash.String()
			// set first tx input as amount for revoked txs
			amount = msgTx.TxIn[0].ValueIn
		} else if stake.IsSStx(msgTx) {
			// set first tx output as amount for ticket txs
			amount = msgTx.TxOut[0].Value
		}
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

	// Lossless amount/fee for SKA. Direction was decided by the big.Int
	// classifier above (TransactionAmountAndDirectionBig) so the icon
	// and sign on the row are correct even for SKA UTXOs larger than
	// int64 — see the bug #5/#6 commentary on the classifier call.
	var amountAtoms, feeAtoms string
	// feeOverride starts as the wire-derived txFee (which is correct for
	// VAR and for confirmed SKA tx — both ship full-witness on the wire).
	// For SKA in mempool the wire form may be witness-stripped and txFee
	// comes back zero; the SKA branch below replaces it with the
	// feeBig-derived value so the int64 Fee channel doesn't dis-agree
	// with FeeAtoms. CSV export and any other consumer that reads
	// tx.Fee directly thus stays consistent before and after confirmation.
	feeOverride := int64(txFee)
	if isSKATx && feeBig != nil {
		feeAtoms = feeBig.String()
		// Mirror feeBig into the int64 channel where it fits. The cap
		// case (SKA fee > MaxInt64) is theoretical for current per-byte
		// SKA fees; if it ever happens we clamp to MaxInt64 to keep the
		// row classifying as "has a fee" rather than 0, and the lossless
		// FeeAtoms still carries the exact value for any caller that
		// reads it.
		if feeBig.IsInt64() {
			feeOverride = feeBig.Int64()
		} else {
			feeOverride = math.MaxInt64
		}
	}
	if skaTotalOut != nil && skaTotalWalletOut != nil {
		// Pick the display amount based on direction (int64 classifier
		// already decided this — we mirror it):
		//   Received: total wallet-owned output value.
		//   Sent / Transferred: total non-wallet output value (= what
		//   actually left the wallet, before fee). For phase-1 we
		//   approximate Sent as skaTotalOut - skaTotalWalletOut; if
		//   that's zero (everything went to ourselves) fall through to
		//   skaTotalWalletOut.
		switch direction {
		case txhelper.TxDirectionReceived:
			amountAtoms = skaTotalWalletOut.String()
		case txhelper.TxDirectionSent:
			external := new(big.Int).Sub(skaTotalOut, skaTotalWalletOut)
			if external.Sign() > 0 {
				amountAtoms = external.String()
			} else {
				amountAtoms = skaTotalWalletOut.String()
			}
		default:
			amountAtoms = skaTotalWalletOut.String()
		}
	}
	return &sharedW.Transaction{
		Hash:        msgTx.TxHash().String(),
		Type:        txType,
		Hex:         walletTx.Hex,
		Timestamp:   walletTx.Timestamp,
		BlockHeight: walletTx.BlockHeight,

		MixDenomination: mixDenom,
		MixCount:        mixedOutputsCount,

		Version:     int32(msgTx.Version),
		LockTime:    int32(msgTx.LockTime),
		Expiry:      int32(msgTx.Expiry),
		Fee:         feeOverride,
		FeeRate:     int64(txFeeRate),
		Size:        txSize,
		AmountAtoms: amountAtoms,
		FeeAtoms:    feeAtoms,

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
		// outputs. Empty string for non-P2PKH-ECDSA inputs (coinbase,
		// OP_RETURN spend, multisig P2SH, Schnorr-secp256k1, Ed25519);
		// the UI must handle the empty case.
		//
		// Coinbase inputs have a synthetic outpoint hash of all-zero
		// and index 0xffffffff — their sigScript is arbitrary
		// miner-chosen data and may coincidentally tokenize to two
		// pushes that pass shape validation. Skip them explicitly so
		// we never claim a coinbase "came from" some bogus P2PKH
		// hash160 of miner extra-nonce bytes.
		var senderAddress string
		zeroHash := chainhash.Hash{}
		isCoinbase := txIn.PreviousOutPoint.Hash == zeroHash && txIn.PreviousOutPoint.Index == 0xffffffff
		if !isCoinbase {
			if addr, err := addresshelper.SigScriptSenderAddress(txIn.SignatureScript, netParams); err != nil {
				log.Debugf("SigScriptSenderAddress(tx=%s input=%d): %v",
					mtx.TxHash(), i, err)
			} else {
				senderAddress = addr
			}
		}
		// Carry the lossless atom count too. Only set for SKA inputs; VAR
		// inputs already fit in Amount (int64). Display reads AmountAtoms
		// when non-empty.
		var amountAtoms string
		if txIn.SKAValueIn != nil && txIn.SKAValueIn.Sign() > 0 {
			amountAtoms = txIn.SKAValueIn.String()
		}
		input := &sharedW.TxInput{
			PreviousTransactionHash:  txIn.PreviousOutPoint.Hash.String(),
			PreviousTransactionIndex: int32(txIn.PreviousOutPoint.Index),
			PreviousOutpoint:         txIn.PreviousOutPoint.String(),
			Amount:                   skaOrVARAtoms(txIn.SKAValueIn, txIn.ValueIn, "TxInput"),
			AccountNumber:            -1, // correct account number is set below if this is a wallet output
			SenderAddress:            senderAddress,
			AmountAtoms:              amountAtoms,
		}

		// override account details if this is wallet input. Also overlay
		// the wallet-known previous amount when the wire-side value is
		// missing — this is the common case for SKA inputs of mempool txs
		// where SPV relayers ship a no-witness form and strip SKAValueIn.
		// Without the overlay, tx-details rows show "0 SKA1" for the
		// sender's inputs in mempool and the per-row int64 totals fed to
		// the back-compat amount field stay zero too.
		for _, walletInput := range walletInputs {
			if walletInput.Index == int32(i) {
				input.AccountNumber = walletInput.AccountNumber
				if input.Amount == 0 && walletInput.AmountIn > 0 {
					input.Amount = walletInput.AmountIn
				}
				if input.AmountAtoms == "" && walletInput.AmountInBig != "" {
					input.AmountAtoms = walletInput.AmountInBig
				}
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
		var outAmountAtoms string
		if txOut.CoinType.IsSKA() && txOut.SKAValue != nil && txOut.SKAValue.Sign() > 0 {
			outAmountAtoms = txOut.SKAValue.String()
		}
		output := &sharedW.TxOutput{
			Index:         int32(i),
			Amount:        amount,
			Version:       int32(txOut.Version),
			ScriptType:    scriptType,
			Address:       address, // correct address, account name and number set below if this is a wallet output
			AccountNumber: -1,
			CoinType:      uint8(txOut.CoinType),
			AmountAtoms:   outAmountAtoms,
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

// filterOwnedWalletInputs returns the subset of walletInputs whose previous
// outpoints actually resolve in THIS wallet's own tx store. The check is a
// defensive backstop for upstream's unminedTxDetails, which builds Debits
// for unmined txs by hitting bbolt existence buckets (existsRawUnspent /
// existsRawUnminedCredit) — that path is observed to false-positive in
// mempool, attributing the SENDER's input UTXOs to a RECEIVER wallet and
// causing the receiver's mempool view of an incoming tx to flash a "Sent +
// huge external amount" row (the displayed value being the sender's
// CHANGE output) until the tx confirms and a different code path
// rebuilds Debits correctly.
//
// Coinbase inputs are kept without lookup — they have a synthetic outpoint
// (zero hash, MaxUint32 index) that wouldn't resolve, and they're never
// wallet-owned anyway so they contribute zero to ownership totals.
//
// Returns walletInputs unchanged when the input list is empty (the common
// "received from outside" case, where nothing needs filtering) or when the
// wallet is not opened (no tx store to consult — fail open so the legacy
// behavior isn't broken during shutdown).
func (asset *Asset) filterOwnedWalletInputs(msgTx *wire.MsgTx, walletInputs []*sharedW.WInput) []*sharedW.WInput {
	if len(walletInputs) == 0 || !asset.WalletOpened() {
		return walletInputs
	}
	ctx, _ := asset.ShutdownContextWithCancel()
	zeroHash := chainhash.Hash{}
	out := make([]*sharedW.WInput, 0, len(walletInputs))
	for _, wi := range walletInputs {
		if wi == nil || int(wi.Index) >= len(msgTx.TxIn) {
			continue
		}
		txIn := msgTx.TxIn[wi.Index]
		isCoinbase := txIn.PreviousOutPoint.Hash == zeroHash && txIn.PreviousOutPoint.Index == 0xffffffff
		if isCoinbase {
			out = append(out, wi)
			continue
		}
		prevOut := txIn.PreviousOutPoint
		if _, err := asset.Internal().DCR.OutputInfo(ctx, &prevOut); err != nil {
			log.Warnf("filterOwnedWalletInputs(tx=%s): walletTx.Input[%d] "+
				"references outpoint %s which is not in this wallet's tx "+
				"store — upstream Debits likely cross-attributed. "+
				"Dropping from wallet-owned set.",
				msgTx.TxHash(), wi.Index, prevOut.String())
			continue
		}
		out = append(out, wi)
	}
	return out
}

// augmentWalletOutputs returns walletOutputs with any msgTx.TxOut entries
// appended whose pkScript address is owned by this wallet but missing from
// walletOutputs. The mirror image of filterOwnedWalletInputs: upstream's
// unminedTxDetails feeds walletTx.Outputs via the wallet's unmined-credits
// bucket, which the broadcast path doesn't always populate before the
// mempool tx-details renders — so the sender's own change output drops out
// of MyOutputs and the entire input gets misclassified as "sent
// externally" (observed: send 1 SKA, tx-details shows -546.994 SKA1
// because change ≈ 545.994 SKA back to the wallet is invisible). After
// confirmation a different code path re-derives Credits and the row
// corrects itself; this helper closes the mempool-window gap.
//
// Synthetic outputs are flagged Internal=true (they're change by
// construction — a wallet-owned output we couldn't find in upstream's
// list is almost always change from a self-initiated send) and
// AccountNumber=0 with empty AccountName. Downstream the AccountNumber
// is only read for ownership testing (`!= -1`) — actual account
// resolution can wait for confirmation. Internal=true keeps the
// tx-details "To" panel from listing the change as an external
// recipient.
//
// Returns walletOutputs unchanged if the wallet isn't open (defensive,
// matches filterOwnedWalletInputs's fail-open behavior during shutdown).
func (asset *Asset) augmentWalletOutputs(msgTx *wire.MsgTx, walletOutputs []*sharedW.WOutput, netParams *chaincfg.Params) []*sharedW.WOutput {
	if !asset.WalletOpened() {
		return walletOutputs
	}
	known := make(map[int32]bool, len(walletOutputs))
	for _, wo := range walletOutputs {
		if wo != nil {
			known[wo.Index] = true
		}
	}
	out := walletOutputs
	for i, txOut := range msgTx.TxOut {
		if known[int32(i)] {
			continue
		}
		_, addrs := stdscript.ExtractAddrs(txOut.Version, txOut.PkScript, netParams)
		var ownedAddr string
		for _, addr := range addrs {
			a := addr.String()
			if asset.HaveAddress(a) {
				ownedAddr = a
				break
			}
		}
		if ownedAddr == "" {
			continue
		}
		var amount int64
		if txOut.CoinType.IsSKA() {
			amount = skaOrVARAtoms(txOut.SKAValue, 0, "augmentWalletOutputs")
		} else {
			amount = txOut.Value
		}
		log.Warnf("augmentWalletOutputs(tx=%s): output %d (%s atoms, ct=%s) belongs to wallet via address %s but is absent from upstream MyOutputs — appending as Internal=true so totals classify correctly.",
			msgTx.TxHash(), i, func() string {
				if txOut.CoinType.IsSKA() && txOut.SKAValue != nil {
					return txOut.SKAValue.String()
				}
				return fmt.Sprintf("%d", txOut.Value)
			}(), txOut.CoinType, ownedAddr)
		out = append(out, &sharedW.WOutput{
			Index:     int32(i),
			AmountOut: amount,
			Internal:  true,
			Address:   ownedAddr,
			WAccount: &sharedW.WAccount{
				AccountNumber: 0,
				AccountName:   "",
			},
		})
		known[int32(i)] = true
	}
	return out
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
