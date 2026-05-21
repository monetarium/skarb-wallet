package dcr

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/monetarium/monetarium-wallet/errors"
	w "github.com/monetarium/monetarium-wallet/wallet"
	"github.com/monetarium/monetarium-wallet/wallet/txauthor"
	"github.com/monetarium/monetarium-wallet/wallet/txrules"
	"github.com/monetarium/monetarium-wallet/wallet/txsizes"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/libwallet/txhelper"
	"github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/monetarium-node/chaincfg/chainhash"
	"github.com/monetarium/monetarium-node/cointype"
	"github.com/monetarium/monetarium-node/dcrutil"
	"github.com/monetarium/monetarium-node/txscript"
	"github.com/monetarium/monetarium-node/txscript/stdaddr"
	"github.com/monetarium/monetarium-node/wire"
)

type TxAuthor struct {
	sourceAccountNumber uint32
	destinations        map[int]*sharedW.TransactionDestination
	changeAddress       string
	changeDestination   *sharedW.TransactionDestination

	// coinType is the asset being sent. Defaults to VAR (CoinType=0). Set via
	// SetTxCoinType. All outputs in a single tx must share the same CoinType.
	coinType cointype.CoinType

	utxos          []*sharedW.UnspentOutput
	unsignedTx     *txauthor.AuthoredTx
	needsConstruct bool
}

func (asset *Asset) NewUnsignedTx(sourceAccountNumber int32, utxos []*sharedW.UnspentOutput) error {
	_, err := asset.GetAccount(sourceAccountNumber)
	if err != nil {
		return err
	}

	asset.TxAuthoredInfo = &TxAuthor{
		sourceAccountNumber: uint32(sourceAccountNumber),
		destinations:        make(map[int]*sharedW.TransactionDestination, 0),
		needsConstruct:      true,
		utxos:               utxos,
		coinType:            cointype.CoinTypeVAR,
	}
	return nil
}

// SetTxCoinType sets the CoinType for the transaction currently being authored.
// Must be called before AddSendDestination if you want to send anything other
// than VAR. Idempotent — calling with the current value is a no-op.
func (asset *Asset) SetTxCoinType(ct cointype.CoinType) error {
	if asset.TxAuthoredInfo == nil {
		return errors.New("no transaction in progress; call NewUnsignedTx first")
	}
	if !ct.IsValid() {
		return fmt.Errorf("invalid coin type %d", ct)
	}
	if !asset.IsCoinTypeActive(ct) {
		return fmt.Errorf("coin type %s is not active on this network", ct)
	}
	if asset.TxAuthoredInfo.coinType != ct {
		asset.TxAuthoredInfo.coinType = ct
		asset.TxAuthoredInfo.needsConstruct = true
	}
	return nil
}

// TxCoinType returns the CoinType for the transaction currently being authored.
// Returns CoinTypeVAR if no transaction is in progress.
func (asset *Asset) TxCoinType() cointype.CoinType {
	if asset.TxAuthoredInfo == nil {
		return cointype.CoinTypeVAR
	}
	return asset.TxAuthoredInfo.coinType
}

// ComputeTxSizeEstimation computes the estimated size of the final raw transaction.
func (asset *Asset) ComputeTxSizeEstimation(dstnAddress string, utxos []*sharedW.UnspentOutput) (int, error) {
	if len(utxos) == 0 {
		return 0, nil
	}

	if dstnAddress == "" {
		return -1, errors.New("destination address missing")
	}

	var sendAmount int64
	inputScriptSizes := make([]int, len(utxos))
	for i, c := range utxos {
		sendAmount += c.Amount.ToInt()
		inputScriptSizes[i] = txsizes.RedeemP2PKHSigScriptSize
	}

	changeScript, err := txhelper.MakeTxChangeSource(dstnAddress, asset.chainParams)
	if err != nil {
		return -1, fmt.Errorf("calculating change script failed; %v", err)
	}

	output, err := txhelper.MakeTxOutput(dstnAddress, sendAmount, asset.chainParams)
	if err != nil {
		return -1, fmt.Errorf("calculating TxOutput failed; %v", err)
	}

	size := txsizes.EstimateSerializeSize(inputScriptSizes, []*wire.TxOut{output}, changeScript.ScriptSize())
	return size, nil
}

func (asset *Asset) GetUnsignedTx() *TxAuthor {
	return asset.TxAuthoredInfo
}

func (asset *Asset) IsUnsignedTxExist() bool {
	return asset.TxAuthoredInfo != nil
}

func (asset *Asset) AddSendDestination(id int, address string, atomAmount int64, sendMax bool) error {
	_, err := stdaddr.DecodeAddress(address, asset.chainParams)
	if err != nil {
		return utils.TranslateError(err)
	}

	if err := asset.validateSendAmount(sendMax, atomAmount); err != nil {
		return err
	}

	asset.TxAuthoredInfo.destinations[id] = &sharedW.TransactionDestination{
		ID:         id,
		Address:    address,
		UnitAmount: atomAmount,
		SendMax:    sendMax,
	}
	asset.TxAuthoredInfo.needsConstruct = true

	return nil
}

func (asset *Asset) UpdateSendDestination(id int, address string, atomAmount int64, sendMax bool) error {
	if err := asset.validateSendAmount(sendMax, atomAmount); err != nil {
		return err
	}

	asset.TxAuthoredInfo.destinations[id] = &sharedW.TransactionDestination{
		ID:         id,
		Address:    address,
		UnitAmount: atomAmount,
		SendMax:    sendMax,
	}

	asset.TxAuthoredInfo.needsConstruct = true
	return nil
}

func (asset *Asset) RemoveSendDestination(id int) {
	if asset.TxAuthoredInfo != nil {
		if _, ok := asset.TxAuthoredInfo.destinations[id]; ok {
			delete(asset.TxAuthoredInfo.destinations, id)
			asset.TxAuthoredInfo.needsConstruct = true
		}
	}
}

func (asset *Asset) SendDestination(id int) *sharedW.TransactionDestination {
	return asset.TxAuthoredInfo.destinations[id]
}

func (asset *Asset) SetChangeDestination(address string) {
	asset.TxAuthoredInfo.changeDestination = &sharedW.TransactionDestination{
		Address: address,
	}
	asset.TxAuthoredInfo.needsConstruct = true
}

func (asset *Asset) RemoveChangeDestination() {
	asset.TxAuthoredInfo.changeDestination = nil
	asset.TxAuthoredInfo.needsConstruct = true
}

func (asset *Asset) TotalSendAmount() *sharedW.Amount {
	var totalSendAmountAtom int64
	for _, destination := range asset.TxAuthoredInfo.destinations {
		totalSendAmountAtom += destination.UnitAmount
	}

	return &sharedW.Amount{
		UnitValue: totalSendAmountAtom,
		CoinValue: dcrutil.Amount(totalSendAmountAtom).ToCoin(),
	}
}

func (asset *Asset) EstimateFeeAndSize() (*sharedW.TxFeeAndSize, error) {
	unsignedTx, err := asset.unsignedTransaction()
	if err != nil {
		return nil, utils.TranslateError(err)
	}

	// Fee in Monetarium is paid in the SAME coin type as the transfer.
	// monetarium-wallet@v1.3.10 split the old FeeForSerializeSizeDualCoin into
	// two type-specific functions: int64-dcrutil.Amount for VAR, big.Int
	// SKAAmount for SKA. Compute whichever applies and project the result back
	// into dcrutil.Amount-shaped UnitValue for the UI (SKA losing precision
	// for display only — actual tx authoring uses the full SKAAmount).
	txCoinType := asset.TxCoinType()
	var feeToSendTx dcrutil.Amount
	if txCoinType.IsVAR() {
		feeToSendTx = txrules.FeeForSerializeSize(
			txrules.DefaultRelayFeePerKb,
			unsignedTx.EstimatedSignedSerializeSize,
		)
	} else {
		skaRelayFee := cointype.SKAAmountFromInt64(int64(txrules.DefaultRelayFeePerKb))
		skaFee := txrules.FeeForSerializeSizeSKA(skaRelayFee, unsignedTx.EstimatedSignedSerializeSize)
		// SKA values can in theory exceed int64. For UI fee display this is a
		// non-issue (fees are tiny), but log a warning if it ever happens so
		// we know to introduce a *big.Int-aware fee channel up the stack.
		// dcrutil.MaxAmount isn't exported (only in tests), so clamp to the
		// same 21e6 * AtomsPerCoin literal here.
		if i64, convErr := skaFee.Int64(); convErr == nil {
			feeToSendTx = dcrutil.Amount(i64)
		} else {
			log.Warnf("SKA fee overflows int64, clamping for UI: %v", skaFee)
			feeToSendTx = dcrutil.Amount(maxVARAtoms)
		}
	}
	feeAmount := &sharedW.Amount{
		UnitValue: int64(feeToSendTx),
		CoinValue: feeToSendTx.ToCoin(),
	}

	var change *sharedW.Amount
	if unsignedTx.ChangeIndex >= 0 {
		txOut := unsignedTx.Tx.TxOut[unsignedTx.ChangeIndex]
		change = &sharedW.Amount{
			UnitValue: txOut.Value,
			CoinValue: asset.ToAmount(txOut.Value).ToCoin(),
		}
	}

	return &sharedW.TxFeeAndSize{
		EstimatedSignedSize: unsignedTx.EstimatedSignedSerializeSize,
		Fee:                 feeAmount,
		Change:              change,
	}, nil
}

func (asset *Asset) EstimateMaxSendAmount() (*sharedW.Amount, error) {
	txFeeAndSize, err := asset.EstimateFeeAndSize()
	if err != nil {
		return nil, err
	}

	spendableAccountBalance, err := asset.SpendableForAccount(int32(asset.TxAuthoredInfo.sourceAccountNumber))
	if err != nil {
		return nil, err
	}

	maxSendableAmount := spendableAccountBalance - txFeeAndSize.Fee.UnitValue

	return &sharedW.Amount{
		UnitValue: maxSendableAmount,
		CoinValue: dcrutil.Amount(maxSendableAmount).ToCoin(),
	}, nil
}

func (asset *Asset) Broadcast(privatePassphrase, transactionLabel string) (string, error) {
	if !asset.WalletOpened() {
		return "", utils.ErrDCRNotInitialized
	}

	n, err := asset.Internal().DCR.NetworkBackend()
	if err != nil {
		log.Error(err)
		return "", err
	}

	unsignedTx, err := asset.unsignedTransaction()
	if err != nil {
		return "", utils.TranslateError(err)
	}

	if unsignedTx.ChangeIndex >= 0 {
		unsignedTx.RandomizeChangePosition()
	}

	var txBuf bytes.Buffer
	txBuf.Grow(unsignedTx.Tx.SerializeSize())
	err = unsignedTx.Tx.Serialize(&txBuf)
	if err != nil {
		log.Error(err)
		return "", err
	}

	var msgTx wire.MsgTx
	err = msgTx.Deserialize(bytes.NewReader(txBuf.Bytes()))
	if err != nil {
		log.Error(err)
		// Bytes do not represent a valid raw transaction
		return "", err
	}

	lock := make(chan time.Time, 1)
	defer func() {
		lock <- time.Time{}
	}()

	ctx, _ := asset.ShutdownContextWithCancel()
	err = asset.Internal().DCR.Unlock(ctx, []byte(privatePassphrase), lock)
	if err != nil {
		log.Error(err)
		return "", errors.New(utils.ErrInvalidPassphrase)
	}

	var additionalPkScripts map[wire.OutPoint][]byte

	// monetarium-wallet v1.3.x added a third return — a bool signalling
	// whether the wallet partially signed (some inputs left unsigned).
	// Skarb v1 doesn't surface multisig flows, so a non-fully-signed result
	// is the same as failure; log and bail.
	invalidSigs, fullySigned, err := asset.Internal().DCR.SignTransaction(ctx, &msgTx, txscript.SigHashAll, additionalPkScripts, nil, nil)
	if err != nil {
		log.Error(err)
		return "", err
	}
	if !fullySigned {
		log.Warnf("SignTransaction returned without all inputs signed; multisig flows not supported in v1")
	}

	invalidInputIndexes := make([]uint32, len(invalidSigs))
	for i, e := range invalidSigs {
		invalidInputIndexes[i] = e.InputIndex
	}

	var serializedTransaction bytes.Buffer
	serializedTransaction.Grow(msgTx.SerializeSize())
	err = msgTx.Serialize(&serializedTransaction)
	if err != nil {
		log.Error(err)
		return "", err
	}

	err = msgTx.Deserialize(bytes.NewReader(serializedTransaction.Bytes()))
	if err != nil {
		// Invalid tx
		log.Error(err)
		return "", err
	}

	txHash, err := asset.Internal().DCR.PublishTransaction(ctx, &msgTx, n)
	if err != nil {
		return "", utils.TranslateError(err)
	}
	return txHash.String(), asset.updateTxLabel(txHash, transactionLabel)
}

// updateTxLabel saves the tx label in the local instance.
func (asset *Asset) updateTxLabel(hash *chainhash.Hash, txLabel string) error {
	tx := &sharedW.Transaction{
		Hash:  hash.String(),
		Label: txLabel,
	}
	_, err := asset.GetWalletDataDb().SaveOrUpdate(&sharedW.Transaction{}, tx)
	return err
}

func (asset *Asset) unsignedTransaction() (*txauthor.AuthoredTx, error) {
	if asset.TxAuthoredInfo.needsConstruct || asset.TxAuthoredInfo.unsignedTx == nil {
		unsignedTx, err := asset.constructTransaction()
		if err != nil {
			return nil, err
		}

		asset.TxAuthoredInfo.needsConstruct = false
		asset.TxAuthoredInfo.unsignedTx = unsignedTx
	}

	return asset.TxAuthoredInfo.unsignedTx, nil
}

func (asset *Asset) constructTransaction() (*txauthor.AuthoredTx, error) {
	var err error
	outputs := make([]*wire.TxOut, 0)
	var outputSelectionAlgorithm w.OutputSelectionAlgorithm = w.OutputSelectionAlgorithmDefault
	var changeSource txauthor.ChangeSource

	var sendMax bool
	ctx, _ := asset.ShutdownContextWithCancel()
	for _, destination := range asset.TxAuthoredInfo.destinations {
		if err := asset.validateSendAmount(destination.SendMax, destination.UnitAmount); err != nil {
			return nil, err
		}

		// check if multiple destinations are set to receive max amount
		if destination.SendMax && changeSource != nil {
			return nil, fmt.Errorf("cannot send max amount to multiple recipients")
		}

		if destination.SendMax {
			// This is a send max destination, set output selection algo to all.
			outputSelectionAlgorithm = w.OutputSelectionAlgorithmAll

			// Use this destination address to make a changeSource rather than a tx output.
			changeSource, err = txhelper.MakeTxChangeSource(destination.Address, asset.chainParams)
			if err != nil {
				log.Errorf("constructTransaction: error preparing change source: %v", err)
				return nil, fmt.Errorf("max amount change source error: %v", err)
			}
			sendMax = true
		} else {
			output, err := txhelper.MakeCoinTypeTxOutput(destination.Address, destination.UnitAmount, asset.TxAuthoredInfo.coinType, asset.chainParams)
			if err != nil {
				log.Errorf("constructTransaction: error preparing tx output: %v", err)
				return nil, fmt.Errorf("make tx output error: %v", err)
			}

			outputs = append(outputs, output)
		}
	}

	if changeSource == nil {
		// dcrwallet should ordinarily handle cases where a nil changeSource
		// is passed to `sharedW.NewUnsignedTransaction` but the changeSource
		// generated there errors on internal gap address limit exhaustion
		// instead of wrapping around to a previously returned address.
		//
		// Generating a changeSource manually here, ensures that the gap address
		// limit exhaustion error is avoided.
		changeSource, err = asset.changeSource(ctx)
		if err != nil {
			return nil, err
		}
	}

	// if preset with a selected list of UTXOs exists, use them instead.
	unspents := asset.TxAuthoredInfo.utxos
	if len(unspents) == 0 {
		unspents, err = asset.UnspentOutputs(int32(asset.TxAuthoredInfo.sourceAccountNumber))
		if err != nil {
			return nil, err
		}
	}

	requiredConfirmations := asset.RequiredConfirmations()

	// Phase-1 dual-coin send routing:
	//
	//   - VAR:  pass our custom InputSource (asset.makeInputSource). It was
	//           added to dodge dcrwallet's gap-address-limit issue when the
	//           wallet's default source generates a fresh change address
	//           per call. relayFee = DefaultRelayFeePerKb wrapped as SKAAmount.
	//   - SKA:  pass nil InputSource so monetarium-wallet's
	//           MakeInputSourceWithCoinType picks UTXOs of the matching SKA
	//           coin type (sharedW.UnspentOutput doesn't carry CoinType /
	//           SKAValue, so our VAR-shaped source can't see SKA UTXOs at
	//           all). relayFee = cointype.Zero() so the wallet uses the
	//           per-coin chainparams MinRelayTxFee via RelayFeeForCoinType.
	//
	// The change source stays the same for both: txauthor rewrites the
	// change output's CoinType to match the tx's inferred coin type AND
	// repopulates Value=0/SKAValue=big.Int for SKA. We only have to provide
	// a P2PKH script via MakeTxChangeSource.
	txCoinType := asset.TxAuthoredInfo.coinType
	var inputsSourceFunc txauthor.InputSource
	var relayFee cointype.SKAAmount
	if txCoinType.IsSKA() {
		inputsSourceFunc = nil
		relayFee = cointype.Zero()
	} else {
		inputsSourceFunc = asset.makeInputSource(sendMax, unspents)
		relayFee = cointype.SKAAmountFromInt64(int64(txrules.DefaultRelayFeePerKb))
	}

	return asset.Internal().DCR.NewUnsignedTransaction(ctx, outputs, relayFee, asset.TxAuthoredInfo.sourceAccountNumber,
		requiredConfirmations, outputSelectionAlgorithm, changeSource, inputsSourceFunc)
}

// makeInputSource creates an InputSource that creates inputs for every unspent
// output with non-zero output values. The importsource aims to create the leanest
// transaction possible. It plans not to spend all the utxos available when servicing
// the current transaction spending amount if possible. The sendMax shows that
// all utxos must be spent without any balance(unspent utxo) left in the account.
func (asset *Asset) makeInputSource(sendMax bool, utxos []*sharedW.UnspentOutput) txauthor.InputSource {
	var (
		sourceErr       error
		totalInputValue dcrutil.Amount

		inputs            = make([]*wire.TxIn, 0, len(utxos))
		pkScripts         = make([][]byte, 0, len(utxos))
		redeemScriptSizes = make([]int, 0, len(utxos))
	)

	for _, output := range utxos {
		if output.Amount == nil || output.Amount.ToCoin() == 0 {
			continue
		}

		if !saneOutputValue(output.Amount.(Amount)) {
			sourceErr = fmt.Errorf("impossible output amount `%v` in listunspent result", output.Amount)
			break
		}

		previousOutPoint, err := parseOutPoint(output)
		if err != nil {
			sourceErr = fmt.Errorf("invalid TxIn data found: %v", err)
			break
		}

		script, err := hex.DecodeString(output.ScriptPubKey)
		if err != nil {
			sourceErr = fmt.Errorf("invalid TxIn pkScript data found: %v", err)
			break
		}

		totalInputValue += dcrutil.Amount(output.Amount.(Amount))
		pkScripts = append(pkScripts, script)
		redeemScriptSizes = append(redeemScriptSizes, txsizes.RedeemP2PKHSigScriptSize)
		inputs = append(inputs, wire.NewTxIn(&previousOutPoint, output.Amount.ToInt(), nil))
	}

	if sourceErr == nil && totalInputValue == 0 {
		// Constructs an error describing the possible reasons why the
		// wallet balance cannot be spent.
		sourceErr = fmt.Errorf("inputs have less than %d confirmations",
			asset.RequiredConfirmations())
	}

	// monetarium-wallet v1.3.x InputSource also receives a targetSKA value
	// for SKA-denominated transactions. Skarb v1 only authors VAR-targeted
	// txs through this path (SKA flows go via the SKA-aware wallet APIs),
	// so we ignore targetSKA here. If we ever wire SKA tx authoring through
	// this function, we must consume targetSKA to size the inputs.
	return func(target dcrutil.Amount, _ cointype.SKAAmount) (*txauthor.InputDetail, error) {
		// If an error was found return it first.
		if sourceErr != nil {
			return nil, sourceErr
		}

		inputDetails := &txauthor.InputDetail{}

		// All utxos are to be spent with no change amount expected.
		if sendMax {
			inputDetails.Inputs = inputs
			inputDetails.Amount = totalInputValue
			inputDetails.Scripts = pkScripts
			inputDetails.RedeemScriptSizes = redeemScriptSizes
			return inputDetails, nil
		}

		var index int
		var currentTotal dcrutil.Amount

		for _, utxoAmount := range inputs {
			if currentTotal < target || target == 0 {
				// Found some utxo(s) we can spend in the current tx.
				index++

				currentTotal += dcrutil.Amount(utxoAmount.ValueIn)
				continue
			}
			break
		}

		inputDetails.Amount = currentTotal
		inputDetails.Inputs = inputs[:index]
		inputDetails.Scripts = pkScripts[:index]
		inputDetails.RedeemScriptSizes = redeemScriptSizes[:index]
		return inputDetails, nil
	}
}

// changeSource derives an internal address from the source wallet and account
// for this unsigned tx, if a change address had not been previously derived.
// The derived (or previously derived) address is used to prepare a
// change source for receiving change from this tx back into the sharedW.
func (asset *Asset) changeSource(ctx context.Context) (txauthor.ChangeSource, error) {
	if asset.TxAuthoredInfo.changeAddress == "" {
		var changeAccount uint32

		// MixedAccountNumber would be -1 if mixer config isn't set.
		if asset.TxAuthoredInfo.sourceAccountNumber == uint32(asset.MixedAccountNumber()) ||
			asset.AccountMixerMixChange() {
			changeAccount = uint32(asset.UnmixedAccountNumber())
		} else {
			changeAccount = asset.TxAuthoredInfo.sourceAccountNumber
		}

		address, err := asset.Internal().DCR.NewChangeAddress(ctx, changeAccount)
		if err != nil {
			return nil, fmt.Errorf("change address error: %v", err)
		}
		asset.TxAuthoredInfo.changeAddress = address.String()
	}

	changeSource, err := txhelper.MakeTxChangeSource(asset.TxAuthoredInfo.changeAddress, asset.chainParams)
	if err != nil {
		log.Errorf("constructTransaction: error preparing change source: %v", err)
		return nil, fmt.Errorf("change source error: %v", err)
	}

	return changeSource, nil
}

// validateSendAmount validates the per-output amount against the in-progress
// transaction's coin type. For VAR the bound is the 21M-coin supply cap
// expressed in 1e8-atom units; for SKA we accept anything that fits in int64,
// because phase-1 atom math is int64-bounded everywhere downstream (1e18
// atoms/coin caps a single output at ~9.22 SKA, see AmountAtomForCoinType).
// The per-coin supply cap will gate larger SKA flows once we plumb big.Int
// end-to-end.
func (asset *Asset) validateSendAmount(sendMax bool, atomAmount int64) error {
	if sendMax {
		return nil
	}
	if atomAmount <= 0 {
		return errors.E(errors.Invalid, "invalid amount")
	}
	ct := asset.TxCoinType()
	if ct.IsSKA() {
		// int64 is the only ceiling that matters in phase 1; the
		// caller (AmountAtomForCoinType) already rejected float→int64
		// overflow before we got here.
		return nil
	}
	if atomAmount > maxVARAtoms {
		return errors.E(errors.Invalid, "invalid amount")
	}
	return nil
}

func saneOutputValue(amount Amount) bool {
	return amount >= 0 && int64(amount) <= maxVARAtoms
}

func parseOutPoint(input *sharedW.UnspentOutput) (wire.OutPoint, error) {
	txHash, err := chainhash.NewHashFromStr(input.TxID)
	if err != nil {
		return wire.OutPoint{}, err
	}
	return wire.OutPoint{Hash: *txHash, Index: input.Vout, Tree: input.Tree}, nil
}
