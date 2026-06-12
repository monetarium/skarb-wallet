package dcr

import (
	"bytes"
	"context"
	"encoding/hex"
	stdErrors "errors"
	"fmt"
	"math/big"
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

	// feeRateOverride is a user-specified relay fee per kB. Zero means
	// "use the wallet default" (RelayFeeForCoinType reads chainparams
	// MinRelayTxFee). Set via SetFeeRateOverride after validating
	// against per-coin bounds; cleared by ClearFeeRateOverride. The
	// EstimateFeeAndSize and constructTransaction paths consult this
	// before falling back to defaults.
	feeRateOverride cointype.SKAAmount

	utxos          []*sharedW.UnspentOutput
	unsignedTx     *txauthor.AuthoredTx
	needsConstruct bool
}

// FeeRateBounds returns the (min, max) relay-fee rate (atoms per KB) the
// user is allowed to set for the active coin type. min comes from
// chainparams MinRelayTxFee — going below it makes the node reject the
// tx as below the relay floor. max is min × maxFeeRateMultiplier, a
// guardrail against fat-finger overpayments (a 100× legitimate rate is
// already pathological; 1000× = burning coins). For VAR the bounds are
// expressed in VAR atoms (1e8 atoms/coin); for SKA in SKA atoms
// (1e18/coin) wrapped as big.Int via SKAAmount.
//
// Returns (zero, zero) if no default relay fee is configured for the
// active coin — caller should disable the custom-fee UI in that case
// rather than letting the user enter a value that will be rejected.
const maxFeeRateMultiplier = 1000

// Sentinel errors for fee-rate validation. UI callers detect these via
// errors.Is and substitute a localised message — the libwallet error
// text below is the English fallback that's only seen by callers
// who don't translate.
// Use std `errors.New` (aliased to stdErrors to avoid clashing with the
// monetarium-wallet errors package) so that std `errors.Is` works in UI
// callers — the vendored errors package has its own Is but std-based
// detection is more idiomatic in UI code.
var (
	ErrFeeRateBelowMin     = stdErrors.New("fee rate below network minimum")
	ErrFeeRateAboveMax     = stdErrors.New("fee rate above safety cap")
	ErrFeeRateNotSupported = stdErrors.New("custom fee not supported for this coin type")
)

func (asset *Asset) FeeRateBounds() (min, max cointype.SKAAmount) {
	if asset.TxAuthoredInfo == nil {
		return cointype.Zero(), cointype.Zero()
	}
	ct := asset.TxAuthoredInfo.coinType
	if ct.IsVAR() {
		// VAR's default relay fee is the txrules.DefaultRelayFeePerKb
		// constant (1e4 atoms/KB = 0.0001 VAR/KB). Lift into SKAAmount-
		// shape so callers see one unified type, but the numeric value
		// is VAR atoms; UI must dispatch by coinType when formatting.
		minAmt := cointype.SKAAmountFromInt64(int64(txrules.DefaultRelayFeePerKb))
		maxAmt := cointype.SKAAmountFromInt64(int64(txrules.DefaultRelayFeePerKb) * maxFeeRateMultiplier)
		return minAmt, maxAmt
	}
	ctx, _ := asset.ShutdownContextWithCancel()
	skaMin := asset.Internal().DCR.RelayFeeForCoinType(ctx, ct)
	if skaMin.IsZero() {
		return cointype.Zero(), cointype.Zero()
	}
	skaMax := skaMin.Mul(maxFeeRateMultiplier)
	return skaMin, skaMax
}

// SetFeeRateOverride records a user-specified relay-fee rate (atoms per
// KB) for the next construct/broadcast cycle. The rate is validated
// against FeeRateBounds — going below MinRelayTxFee guarantees node
// rejection; going above 1000× wastes coins. Pass cointype.Zero() to
// clear the override (equivalent to ClearFeeRateOverride). All future
// EstimateFeeAndSize and construct cycles will use this rate until
// cleared or NewUnsignedTx wipes TxAuthoredInfo.
func (asset *Asset) SetFeeRateOverride(rate cointype.SKAAmount) error {
	if asset.TxAuthoredInfo == nil {
		return errors.New("no in-progress transaction")
	}
	if rate.IsZero() {
		asset.TxAuthoredInfo.feeRateOverride = cointype.Zero()
		asset.TxAuthoredInfo.needsConstruct = true
		return nil
	}
	minAmt, maxAmt := asset.FeeRateBounds()
	if minAmt.IsZero() {
		return ErrFeeRateNotSupported
	}
	ct := asset.TxAuthoredInfo.coinType
	if rate.Cmp(minAmt) < 0 {
		// Wrap a sentinel + a human-readable English message. UI callers
		// errors.Is(err, ErrFeeRateBelowMin) and substitute their own
		// localised string with FeeRateBounds-formatted numbers. The
		// fallback English text uses coin units so even untranslated
		// callers don't see raw 18-digit atom strings.
		return fmt.Errorf("%w: rate %s is below network minimum %s per KB",
			ErrFeeRateBelowMin,
			FormatTxAmountBig(rate.String(), 0, uint8(ct)),
			FormatTxAmountBig(minAmt.String(), 0, uint8(ct)))
	}
	if rate.Cmp(maxAmt) > 0 {
		return fmt.Errorf("%w: rate %s exceeds safety cap %s per KB",
			ErrFeeRateAboveMax,
			FormatTxAmountBig(rate.String(), 0, uint8(ct)),
			FormatTxAmountBig(maxAmt.String(), 0, uint8(ct)))
	}
	asset.TxAuthoredInfo.feeRateOverride = rate
	asset.TxAuthoredInfo.needsConstruct = true
	return nil
}

// ClearFeeRateOverride drops any user-specified fee rate; subsequent
// estimates and broadcasts revert to the wallet default
// (RelayFeeForCoinType for SKA, DefaultRelayFeePerKb for VAR).
func (asset *Asset) ClearFeeRateOverride() {
	if asset.TxAuthoredInfo == nil {
		return
	}
	asset.TxAuthoredInfo.feeRateOverride = cointype.Zero()
	asset.TxAuthoredInfo.needsConstruct = true
}

// FeeRateOverride returns the current user-specified fee rate, or
// cointype.Zero() if none is set (default behaviour).
func (asset *Asset) FeeRateOverride() cointype.SKAAmount {
	if asset.TxAuthoredInfo == nil {
		return cointype.Zero()
	}
	return asset.TxAuthoredInfo.feeRateOverride
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
	// Infer the coin type from the selected UTXOs so the estimated output is
	// built for the right coin. A SKA TxOut serializes a coin-type byte + an
	// SKAValue that a VAR MakeTxOutput omits, so sizing a SKA selection as a
	// VAR output under-counts; input count dominates but the output sizing was
	// systematically off for SKA.
	ct := cointype.CoinTypeVAR
	for i, c := range utxos {
		sendAmount += c.Amount.ToInt()
		inputScriptSizes[i] = txsizes.RedeemP2PKHSigScriptSize
		if i == 0 {
			ct = cointype.CoinType(c.CoinType)
		}
	}

	changeScript, err := txhelper.MakeTxChangeSource(dstnAddress, asset.chainParams)
	if err != nil {
		return -1, fmt.Errorf("calculating change script failed; %v", err)
	}

	output, err := txhelper.MakeCoinTypeTxOutput(dstnAddress, sendAmount, ct, asset.chainParams)
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
	return asset.AddSendDestinationBig(id, address, atomAmount, "", sendMax, false)
}

// AddSendDestinationBig is the lossless variant: pass atomAmountBig as a
// decimal-string big.Int when the SKA atom count exceeds int64. Empty
// string falls back to the int64 atomAmount path for VAR and small-SKA
// sends. UI callers building an SKA send should always pass the big.Int
// string when the amount might overflow int64 (i.e. >9.22 SKA per output).
//
// subtractFeeFromAmount, when true, makes the transaction fee come out of
// THIS recipient's output instead of the sender's change (the recipient
// receives atomAmount minus the fee). At most one destination per tx may set
// it; constructTransaction enforces that and threads the index to the wallet.
func (asset *Asset) AddSendDestinationBig(id int, address string, atomAmount int64, atomAmountBig string, sendMax, subtractFeeFromAmount bool) error {
	_, err := stdaddr.DecodeAddress(address, asset.chainParams)
	if err != nil {
		return utils.TranslateError(err)
	}

	if err := asset.validateSendAmountBig(sendMax, atomAmount, atomAmountBig); err != nil {
		return err
	}

	asset.TxAuthoredInfo.destinations[id] = &sharedW.TransactionDestination{
		ID:                    id,
		Address:               address,
		UnitAmount:            atomAmount,
		UnitAmountBig:         atomAmountBig,
		SendMax:               sendMax,
		SubtractFeeFromAmount: subtractFeeFromAmount,
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
	// Accumulate the lossless big.Int total too: for SKA destinations added via
	// AddSendDestinationBig the real amount is in UnitAmountBig (the int64
	// UnitAmount is a clamped fallback, 0 for amounts plumbed only through the
	// big channel), so the int64 sum alone reports 0/truncated for SKA. Expose
	// the exact value via UnitValueBig (FormatTxAmountBig reads it first).
	totalBig := new(big.Int)
	for _, destination := range asset.TxAuthoredInfo.destinations {
		totalSendAmountAtom += destination.UnitAmount
		if destination.UnitAmountBig != "" {
			if parsed, ok := new(big.Int).SetString(destination.UnitAmountBig, 10); ok {
				totalBig.Add(totalBig, parsed)
				continue
			}
		}
		totalBig.Add(totalBig, big.NewInt(destination.UnitAmount))
	}

	return &sharedW.Amount{
		UnitValue:    totalSendAmountAtom,
		CoinValue:    dcrutil.Amount(totalSendAmountAtom).ToCoin(),
		UnitValueBig: totalBig.String(),
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
	override := asset.FeeRateOverride()
	var feeToSendTx dcrutil.Amount
	if txCoinType.IsVAR() {
		// VAR fee rate: use user override if set (already validated by
		// SetFeeRateOverride), else the chain default. Override is
		// SKAAmount-shaped but for VAR the numeric value is in VAR atoms
		// (1e8/coin) — int64-extractable for the dcrutil.Amount path.
		relayRate := txrules.DefaultRelayFeePerKb
		if !override.IsZero() {
			if i64, err := override.Int64(); err == nil {
				relayRate = dcrutil.Amount(i64)
			} else {
				log.Warnf("VAR fee-rate override %s overflows int64; falling back to default", override.String())
			}
		}
		feeToSendTx = txrules.FeeForSerializeSize(relayRate, unsignedTx.EstimatedSignedSerializeSize)
	} else {
		// Source the relay rate from the wallet's chain-params SKA config
		// (the same channel the broadcast authoring path consults via
		// MakeInputSourceWithCoinType → RelayFeeForCoinType). The old code
		// lifted txrules.DefaultRelayFeePerKb (= 1e4 VAR atoms ≈ 0.0001
		// VAR/KB) into SKAAmount by reinterpreting the numeric value as SKA
		// atoms (each = 1e-18 SKA), so the estimate came out 16 orders of
		// magnitude smaller than the fee the broadcast actually charged —
		// pre-send "Загальна сума" disagreed with the post-broadcast
		// tx-details "Transaction Fee" by ~1.864 SKA on a 555 SKA send.
		// SKA fee rate: prefer user override (already validated against
		// chainparams MinRelayTxFee), else fall back to the wallet's
		// default for this coin.
		skaRelayFee := override
		if skaRelayFee.IsZero() {
			ctx, _ := asset.ShutdownContextWithCancel()
			skaRelayFee = asset.Internal().DCR.RelayFeeForCoinType(ctx, txCoinType)
		}
		if skaRelayFee.IsZero() {
			return nil, errors.E("no relay fee configured for SKA coin type; cannot estimate")
		}
		skaFee := txrules.FeeForSerializeSizeSKA(skaRelayFee, unsignedTx.EstimatedSignedSerializeSize)
		// SKA fee CAN exceed int64 once custom-fee rates approach the
		// 1000× safety cap — a 1-KB tx at 1000× MinRelayTxFee (assume
		// 4 SKA1/KB MinRelay → 4000 SKA1/KB cap) produces 4000e18 ≈ 4e21
		// atoms, several orders past 9.22e18 MaxInt64. The previous
		// "clamp to maxVARAtoms (2.1e15) on overflow" was catastrophic:
		// a 32 SKA1/KB rate fit int64 and displayed ≈8.9 SKA1 fee, but
		// 33 SKA1/KB overflowed and dropped to ≈0.0021 SKA1 — the user
		// saw the displayed total SHRINK as the rate went UP (bug #1
		// from this batch). Carry the lossless atom count through
		// Amount.UnitValueBig and clamp the int64 channel to MaxInt64
		// only as a placeholder so display code that hasn't been
		// upgraded to read UnitValueBig stays at "max representable"
		// rather than wrapping or trying to format MaxVAR-sized atoms.
		// UI display layer (FormatTxAmountBig + addSendDestination
		// totalCostBig math) routes through the big-string.
		feeBigStr := skaFee.String()
		if i64, convErr := skaFee.Int64(); convErr == nil {
			feeToSendTx = dcrutil.Amount(i64)
		} else {
			log.Warnf("SKA fee %s atoms exceeds int64; routing through Amount.UnitValueBig",
				feeBigStr)
			feeToSendTx = dcrutil.Amount(int64(^uint64(0) >> 1)) // MaxInt64 placeholder
		}
		feeAmount := &sharedW.Amount{
			UnitValue:    int64(feeToSendTx),
			CoinValue:    feeToSendTx.ToCoin(),
			UnitValueBig: feeBigStr,
		}
		var change *sharedW.Amount
		if unsignedTx.ChangeIndex >= 0 {
			txOut := unsignedTx.Tx.TxOut[unsignedTx.ChangeIndex]
			// SKA change value lives in SKAValue (*big.Int); txOut.Value is 0
			// by upstream convention for SKA outputs. Read SKAValue and plumb
			// it via UnitValueBig (UnitValue is a clamped int64 fallback) so the
			// reported Change isn't always zero — mirroring how Fee is plumbed.
			change = &sharedW.Amount{}
			if txOut.SKAValue != nil {
				change.UnitValueBig = txOut.SKAValue.String()
				if txOut.SKAValue.IsInt64() {
					change.UnitValue = txOut.SKAValue.Int64()
				} else {
					change.UnitValue = int64(^uint64(0) >> 1) // MaxInt64 placeholder
				}
			}
		}
		return &sharedW.TxFeeAndSize{
			EstimatedSignedSize: unsignedTx.EstimatedSignedSerializeSize,
			Fee:                 feeAmount,
			Change:              change,
		}, nil
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

	// SpendableForAccount returns only the VAR int64 spendable, and the
	// int64 − ToCoin() math below is valid only for VAR (÷1e8). Rather than
	// return a silently-wrong/overflowing SKA max, fail honestly — the send
	// page computes the SKA max losslessly in big.Int itself and does not call
	// this method. (Kept VAR-correct for any VAR caller.)
	if asset.TxCoinType().IsSKA() {
		return nil, fmt.Errorf("EstimateMaxSendAmount: SKA not supported via this int64 path; compute max in big.Int from SKASpendable")
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

	// Build the tx to broadcast WITH subtract-fee-from-amount applied (if the
	// user toggled it). The cached unsignedTransaction() is the SFFA-free
	// estimate, so we construct a fresh one here rather than reuse it — the
	// fee amount is identical, only the recipient output (and absence of a
	// separate fee-from-change) differs. SFFA's recipient-covers-fee+dust
	// validation runs here, at send time, where surfacing the error to the
	// user is appropriate (vs blanking the live estimate).
	unsignedTx, err := asset.constructTransaction(true)
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

	// Refuse to broadcast a transaction whose inputs did not ALL sign. A
	// partially-signed/invalid-sig tx is rejected by the node on script
	// verification, but the user would have already been shown the green
	// "Transaction sent!" modal — money appears sent but is not. v1 has no
	// multisig flow, so a non-fully-signed result is a hard failure, not a
	// warning. (Previously the indexes were collected and then ignored.)
	if !fullySigned || len(invalidSigs) > 0 {
		invalidInputIndexes := make([]uint32, len(invalidSigs))
		for i, e := range invalidSigs {
			invalidInputIndexes[i] = e.InputIndex
		}
		log.Errorf("refusing to broadcast: transaction not fully signed (fullySigned=%v, invalid input indexes=%v); multisig is not supported in v1",
			fullySigned, invalidInputIndexes)
		return "", stdErrors.New("transaction signing failed: not all inputs could be signed")
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

	// Persist the just-broadcast tx into Skarb's storm DB right now
	// (synchronously), don't wait for NtfnServer.TransactionNotifications
	// to deliver — that fires asynchronously and the user can sit on the
	// "Transaction sent!" modal long enough to navigate to the
	// Transactions tab BEFORE the notification reaches our listener,
	// which is exactly the window where loadTransactions returns the
	// list MINUS the new tx (the receiver-side notification arrives via
	// peer relay and races into storm before the user even sees the
	// "Transaction sent!" toast, which is why receiver-side mempool txs
	// appear instantly and sender-side ones don't — different scheduling,
	// same root cause).
	//
	// Errors here are non-fatal: the network publish already succeeded,
	// the upstream bbolt store has the tx, and the regular notification
	// listener (txandblocknotifications.go) will SaveOrUpdate it later
	// when it fires. Log + move on.
	if storedTx, decodeErr := asset.GetTransactionRaw(txHash.String()); decodeErr == nil && storedTx != nil {
		if _, saveErr := asset.GetWalletDataDb().SaveOrUpdate(&sharedW.Transaction{}, storedTx); saveErr != nil {
			log.Warnf("Broadcast: immediate storm-DB save failed for %s (listener will retry): %v",
				txHash.String(), saveErr)
		} else {
			log.Infof("Broadcast: storm-DB save OK for unmined tx %s", txHash.String())
			// Fire the OnTransaction notification listeners manually so
			// every mounted UI page (Info widget, TxList) reloads
			// IMMEDIATELY — without this, the listeners only run when
			// NtfnServer.TransactionNotifications eventually delivers
			// the same event asynchronously (observed: 1-3 seconds
			// delay, sometimes longer if the notification queue is
			// backed up). The user's expectation is "sent — see it
			// instantly"; this closes the visible-latency window. The
			// upstream listener will fire again later with the same
			// hash and SaveOrUpdate is idempotent.
			asset.mempoolTransactionNotification(storedTx)
		}
	} else if decodeErr != nil {
		log.Warnf("Broadcast: decode-for-storm-save failed for %s (listener will retry): %v",
			txHash.String(), decodeErr)
	}

	// Skip label-save when the user didn't enter one. updateTxLabel ->
	// walletdata.SaveOrUpdate is destructive when called with a partial
	// Transaction (Hash + empty Label only): SaveOrUpdate reads the
	// existing record, deletes it, copies only the Label (which is also
	// empty here so it stays ""), then saves a near-zero record with
	// Type="" and Timestamp=0. That breaks two things at once:
	//   - TxFilterAll selects on Type IN (Regular, Mixed, CoinBase), so
	//     a Type="" record is invisible to Info / Transactions tabs.
	//   - Timestamp=0 sinks the row to the bottom of newestFirst order
	//     even if it WERE matched.
	// Both effects combined explain the user-visible "sent SKA tx never
	// appears in the mempool list" bug: the immediate-storm-save above
	// inserts a complete record, then updateTxLabel with an empty label
	// silently clobbers it back out a few microseconds later.
	if transactionLabel != "" {
		return txHash.String(), asset.updateTxLabel(txHash, transactionLabel)
	}
	return txHash.String(), nil
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

// unsignedTransaction builds (and caches) the ESTIMATE transaction — the one
// behind EstimateFeeAndSize / the pre-send "Загальна сума" display. It is
// constructed WITHOUT subtract-fee-from-amount: SFFA only changes WHO pays the
// fee (recipient vs sender's change), never the fee amount itself, and routing
// the live estimate through SFFA made every keystroke subject to SFFA's
// "recipient must cover fee + dust" validation — which fails mid-typing and
// blanked the whole form. SFFA is applied only at broadcast time (Broadcast →
// constructTransaction(true)).
func (asset *Asset) unsignedTransaction() (*txauthor.AuthoredTx, error) {
	if asset.TxAuthoredInfo.needsConstruct || asset.TxAuthoredInfo.unsignedTx == nil {
		unsignedTx, err := asset.constructTransaction(false)
		if err != nil {
			return nil, err
		}

		asset.TxAuthoredInfo.needsConstruct = false
		asset.TxAuthoredInfo.unsignedTx = unsignedTx
	}

	return asset.TxAuthoredInfo.unsignedTx, nil
}

// constructTransaction builds the unsigned tx. applySFFA controls whether a
// recipient flagged SubtractFeeFromAmount actually absorbs the fee: false for
// the cached estimate path (kept SFFA-free so the fee/total display never
// blanks), true for the real broadcast. With no SFFA destination the flag is a
// no-op (index stays -1 → fee from change, the default).
func (asset *Asset) constructTransaction(applySFFA bool) (*txauthor.AuthoredTx, error) {
	var err error
	outputs := make([]*wire.TxOut, 0)
	var outputSelectionAlgorithm w.OutputSelectionAlgorithm = w.OutputSelectionAlgorithmDefault
	var changeSource txauthor.ChangeSource

	var sendMax bool
	// subtractFeeFromAmountIdx is the position WITHIN the `outputs` slice
	// (not the destination map key) of the recipient whose output absorbs
	// the fee; -1 = nobody (fee comes from change, the default). The slice
	// index is what the wallet's SFFA path expects.
	subtractFeeFromAmountIdx := -1
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
			// Prefer the big.Int companion field when set — that's the
			// lossless atom count for SKA outputs whose value exceeds
			// int64. UnitAmount alone caps at MaxInt64 (~9.22 SKA) and
			// would silently truncate larger sends.
			var amountBig *big.Int
			if destination.UnitAmountBig != "" {
				parsed, ok := new(big.Int).SetString(destination.UnitAmountBig, 10)
				if !ok {
					return nil, fmt.Errorf("destination %d: invalid big.Int amount %q", destination.ID, destination.UnitAmountBig)
				}
				amountBig = parsed
			}
			output, err := txhelper.MakeCoinTypeTxOutputBig(destination.Address, destination.UnitAmount, amountBig, asset.TxAuthoredInfo.coinType, asset.chainParams)
			if err != nil {
				log.Errorf("constructTransaction: error preparing tx output: %v", err)
				return nil, fmt.Errorf("make tx output error: %v", err)
			}

			// Record the fee-bearing recipient's position in the outputs
			// slice (after the append below it's len(outputs)-1). Reject a
			// second SFFA destination — the wallet can only subtract the fee
			// from one output. Only honoured when applySFFA is set (broadcast
			// path); the estimate path keeps the index at -1 so the displayed
			// fee never depends on SFFA validation.
			if applySFFA && destination.SubtractFeeFromAmount {
				if subtractFeeFromAmountIdx >= 0 {
					return nil, fmt.Errorf("cannot subtract fee from more than one recipient")
				}
				subtractFeeFromAmountIdx = len(outputs)
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
	//
	// A non-empty preset means the user hand-picked UTXOs on the manual
	// coin-selection page. In that case the tx MUST spend EXACTLY that set:
	// every selected UTXO and nothing outside it. "Nothing outside it" is
	// already guaranteed (only the preset list is handed to the input
	// source); "every selected one" is enforced by manualSelection →
	// forceAll in the input sources below, which otherwise would sub-select
	// only the minimal subset that covers the target (making the manual
	// choice look ignored). An empty preset = automatic selection over the
	// whole account (the wallet picks at its discretion).
	manualSelection := len(asset.TxAuthoredInfo.utxos) > 0
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
	//   - SKA:  pass our DETERMINISTIC largest-first InputSource
	//           (asset.makeSKAInputSource). The upstream
	//           MakeInputSourceWithCoinType picks SKA UTXOs in RANDOM order,
	//           which at high custom fee rates causes a non-monotonic
	//           "need one more input" fee-spiral (rate 3000 ok, 3001 fails on
	//           the same balance). Largest-first + earliest-stop guarantees
	//           minimal inputs and a stable, repeatable fee. relayFee =
	//           cointype.Zero() so the wallet still resolves the per-coin
	//           chainparams MinRelayTxFee when no override is set.
	//
	// The change source stays the same for both: txauthor rewrites the
	// change output's CoinType to match the tx's inferred coin type AND
	// repopulates Value=0/SKAValue=big.Int for SKA. We only have to provide
	// a P2PKH script via MakeTxChangeSource.
	txCoinType := asset.TxAuthoredInfo.coinType
	override := asset.TxAuthoredInfo.feeRateOverride

	// SendMax (sweep) MUST go through the coin-type-aware sweep entry point
	// for BOTH coins. A SendMax tx has NO outputs (the destination is the
	// changeSource), and NewUnsignedTransactionWithSFFA infers the coin type
	// from outputs[0] — with an empty slice it defaults to VAR (createtx.go
	// documents exactly this). Routing SKA through the generic entry point
	// authored a sweep of the account's ENTIRE VAR BALANCE instead of SKA —
	// observed live: Max on SKA1 broadcast all VAR, and afterwards every Max
	// estimate kept trying to sweep the now-empty VAR balance and died with
	// InsufficientBalance ("the Max button stopped working"). VAR is routed
	// explicitly too, instead of relying on the upstream algo==All &&
	// len(outputs)==0 fallback to land on the right path.
	//
	// Manual UTXO selection composes with Max: pass a forceAll input source
	// over the hand-picked set so the sweep drains EXACTLY those UTXOs (a nil
	// source would wrap the store's drain-the-whole-account source, silently
	// ignoring the user's selection).
	if sendMax {
		var sweepSource txauthor.InputSource
		sweepRelayFee := override // Zero → upstream resolves RelayFeeForCoinType
		if txCoinType.IsSKA() {
			if manualSelection {
				sweepSource = asset.makeSKAInputSource(unspents, txCoinType, true)
			}
		} else {
			if manualSelection {
				sweepSource = asset.makeInputSource(true, unspents)
			}
			// Preserve the legacy VAR default rate when no override is set
			// (upstream would also resolve a VAR fee, but keep behavior
			// byte-identical to the previous working path).
			if sweepRelayFee.IsZero() {
				sweepRelayFee = cointype.SKAAmountFromInt64(int64(txrules.DefaultRelayFeePerKb))
			}
		}
		return asset.Internal().DCR.NewUnsignedSweepTransactionForCoinType(ctx,
			txCoinType, sweepRelayFee, asset.TxAuthoredInfo.sourceAccountNumber,
			requiredConfirmations, changeSource, sweepSource)
	}

	var inputsSourceFunc txauthor.InputSource
	var relayFee cointype.SKAAmount
	if txCoinType.IsSKA() {
		// Normal SKA send: deterministic source kills the random-selection
		// fee cliff (see makeSKAInputSource). The coin type scopes selection
		// to UTXOs of this exact SKA coin only. manualSelection forces every
		// preset UTXO to be spent.
		inputsSourceFunc = asset.makeSKAInputSource(unspents, txCoinType, manualSelection)
		// Override takes precedence over the wallet's chainparams
		// default. Zero leaves it for upstream to fill in from
		// RelayFeeForCoinType — the legacy code path.
		if !override.IsZero() {
			relayFee = override
		} else {
			relayFee = cointype.Zero()
		}
	} else {
		// manualSelection forces every hand-picked UTXO to be spent.
		inputsSourceFunc = asset.makeInputSource(manualSelection, unspents)
		// VAR override path: lift the user-specified rate (in VAR atoms)
		// back into SKAAmount-shape for the unified upstream API.
		if !override.IsZero() {
			relayFee = override
		} else {
			relayFee = cointype.SKAAmountFromInt64(int64(txrules.DefaultRelayFeePerKb))
		}
	}

	// Route through the SFFA-aware entry point. subtractFeeFromAmountIdx is
	// -1 for the normal case (fee from change) and a valid outputs-slice
	// index when one recipient was flagged to absorb the fee; the SendMax
	// path never sets it (it builds a changeSource and no fixed output).
	return asset.Internal().DCR.NewUnsignedTransactionWithSFFA(ctx, outputs, relayFee, asset.TxAuthoredInfo.sourceAccountNumber,
		requiredConfirmations, outputSelectionAlgorithm, changeSource, inputsSourceFunc, subtractFeeFromAmountIdx)
}

// makeInputSource creates an InputSource that creates inputs for every unspent
// output with non-zero output values. The input source aims to create the leanest
// transaction possible. It plans not to spend all the utxos available when servicing
// the current transaction spending amount if possible. useAllInputs forces every
// supplied UTXO to be spent (no sub-selection) — true for a SendMax sweep AND for a
// manual coin selection, where the user demands that exactly the chosen UTXOs be used.
func (asset *Asset) makeInputSource(useAllInputs bool, utxos []*sharedW.UnspentOutput) txauthor.InputSource {
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
		// Reachable in two distinct situations that this function
		// can't disambiguate on its own:
		//   - The account has utxos but every one is still
		//     unconfirmed (< RequiredConfirmations blocks deep).
		//   - The account has no VAR utxos at all — typically because
		//     the user is trying to send VAR from an account that
		//     only holds SKA tokens.
		// The original phrasing only mentioned confirmations, which
		// then got translated to "Некоректна сума" via TranslateErr and
		// looked like the user typed an invalid number. Mention the
		// likelier cause first so the form points at the real problem.
		sourceErr = fmt.Errorf("no spendable VAR in this account (need confirmed UTXOs >= %d blocks deep)",
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

		// Spend every supplied UTXO. For a SendMax sweep this drains the
		// account (change ≈ 0); for a manual selection it forces exactly the
		// hand-picked set to be used, with change for whatever is left after
		// amount + fee (the output-selection algorithm, not this source,
		// decides whether a change output is produced).
		if useAllInputs {
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

// makeSKAInputSource builds a DETERMINISTIC, largest-first InputSource for SKA
// sends. It exists because the upstream wallet selector
// (udb.MakeInputSourceWithCoinType) picks SKA UTXOs in RANDOM order
// (rand.ShuffleSlice). With a normal fee that's harmless, but the txauthor
// fee-grow loop adds one input per iteration and recomputes fee = rate ×
// tx_size: at a high custom rate every extra input adds a large fee chunk, so
// a random pick of many small UTXOs can spiral into "need one more input" and
// fail with InsufficientBalance — while a different random pick of a few large
// UTXOs succeeds. That is exactly the reported non-monotonic cliff (rate 3000
// works, 3001 fails, same balance). Sorting UTXOs largest-first and stopping
// as soon as the target is met guarantees the minimum number of inputs and a
// monotonic, repeatable result.
//
// targetSKA == 0 is the sweep sentinel (drain everything) — mirrors the
// upstream contract. The TxIn shape matches the upstream SKA selector exactly:
// Value=0 with the atom count carried in SKAValueIn (V13 wire format).
func (asset *Asset) makeSKAInputSource(utxos []*sharedW.UnspentOutput, coinType cointype.CoinType, forceAll bool) txauthor.InputSource {
	type skaUTXO struct {
		outPoint wire.OutPoint
		script   []byte
		atoms    *big.Int
	}

	var (
		prepared []skaUTXO
		prepErr  error
	)
	for _, output := range utxos {
		// One coin type per tx (CLAUDE.md §arch): a SKA send must consume
		// ONLY UTXOs of the exact SKA coin being sent. asset.UnspentOutputs
		// returns the whole account (VAR + every SKA coin); without this
		// filter the VAR rows (SKAAmountAtoms=="" → tiny int64 Amount) leak
		// in as phantom ~0-atom inputs, inflating tx size (and thus fee) and
		// — worse — producing an invalid mixed-coin tx if ever broadcast.
		if output.CoinType != uint8(coinType) {
			continue
		}
		// Lossless atom count: SKA UTXOs carry it in the SKAAmountAtoms
		// decimal string (the int64 Amount is 0 for SKA by upstream
		// convention); fall back to Amount for any VAR-shaped row.
		atoms := new(big.Int)
		if output.SKAAmountAtoms != "" {
			if v, ok := new(big.Int).SetString(output.SKAAmountAtoms, 10); ok {
				atoms = v
			}
		} else if output.Amount != nil {
			atoms = big.NewInt(output.Amount.ToInt())
		}
		if atoms.Sign() <= 0 {
			continue
		}
		outPoint, err := parseOutPoint(output)
		if err != nil {
			prepErr = fmt.Errorf("invalid SKA TxIn data: %v", err)
			break
		}
		script, err := hex.DecodeString(output.ScriptPubKey)
		if err != nil {
			prepErr = fmt.Errorf("invalid SKA TxIn pkScript: %v", err)
			break
		}
		prepared = append(prepared, skaUTXO{outPoint: outPoint, script: script, atoms: atoms})
	}

	// No sort: consume UTXOs in the natural order returned by
	// asset.UnspentOutputs — the SAME strategy the VAR input source
	// (makeInputSource) uses. This was a deliberate change from an earlier
	// largest-first sort: largest-first minimised input count (often a single
	// big UTXO), which diverged from VAR and let small UTXOs pile up as dust
	// instead of being consolidated. Matching VAR keeps behaviour consistent
	// across coin types AND consolidates small outputs over time (several
	// inputs → one change output), curbing long-term UTXO-count and fee growth.
	// Determinism (estimate == broadcast, no random fee cliff) is preserved
	// because UnspentOutputs returns a stable order — exactly what VAR relies
	// on. The original random-selection cliff came from the UPSTREAM SKA
	// selector's rand.ShuffleSlice, which this source replaces entirely.

	// Compact diagnostic: a single summary line lets an "insufficient" at a
	// fee well under the balance be traced to a shortfall in the *selectable*
	// SKA UTXO set (after the coin-type filter) vs the displayed account
	// balance, without spamming a line per UTXO every estimate cycle.
	availTotal := new(big.Int)
	for i := range prepared {
		availTotal.Add(availTotal, prepared[i].atoms)
	}
	log.Infof("makeSKAInputSource: coin=%d in=%d selectable=%d forceAll=%v totalAtoms=%s",
		uint8(coinType), len(utxos), len(prepared), forceAll, availTotal.String())

	return func(_ dcrutil.Amount, targetSKA cointype.SKAAmount) (*txauthor.InputDetail, error) {
		if prepErr != nil {
			return nil, prepErr
		}
		detail := &txauthor.InputDetail{SKAAmount: cointype.Zero()}
		// takeAll: spend every prepared UTXO regardless of target. True for a
		// sweep (targetSKA==0 sentinel) and for a manual coin selection
		// (forceAll), where the user requires exactly the hand-picked set —
		// all of it — to be used rather than a minimal cover.
		takeAll := targetSKA.IsZero() || forceAll
		runningTotal := new(big.Int)
		target := targetSKA.BigInt()
		for i := range prepared {
			// Stop as soon as the (non-takeAll) target is covered — minimal inputs.
			if !takeAll && runningTotal.Cmp(target) >= 0 {
				break
			}
			u := prepared[i]
			input := wire.NewTxIn(&u.outPoint, 0, nil) // SKA: Value=0
			input.SKAValueIn = new(big.Int).Set(u.atoms)

			detail.Inputs = append(detail.Inputs, input)
			detail.Scripts = append(detail.Scripts, u.script)
			detail.RedeemScriptSizes = append(detail.RedeemScriptSizes, txsizes.RedeemP2PKHSigScriptSize)
			runningTotal.Add(runningTotal, u.atoms)
		}
		// A target-bounded (auto-selection) call that exhausted every UTXO
		// without meeting the target is the exact precondition for the upstream
		// InsufficientBalance. (In forceAll/sweep mode we always return the
		// whole set; the upstream cover check still errors honestly if a manual
		// selection is too small for amount+fee.)
		if !takeAll && runningTotal.Cmp(target) < 0 {
			log.Infof("makeSKAInputSource: SHORTFALL targetAtoms=%s availAtoms=%s selected=%d/%d",
				target.String(), runningTotal.String(), len(detail.Inputs), len(prepared))
		}
		detail.SKAAmount = cointype.NewSKAAmount(runningTotal)
		return detail, nil
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
// expressed in 1e8-atom units; for SKA the int64 channel is unbounded
// (callers use AddSendDestinationBig with a *big.Int string for amounts
// above ~9.22 SKA, and validateSendAmountBig is the actual validator there).
func (asset *Asset) validateSendAmount(sendMax bool, atomAmount int64) error {
	return asset.validateSendAmountBig(sendMax, atomAmount, "")
}

// validateSendAmountBig is the big.Int-aware validator. When atomAmountBig
// is non-empty it parses as a decimal-string *big.Int and is used as the
// source of truth (must be positive); otherwise we fall back to the int64
// atomAmount with the legacy bounds. SKA has no supply cap check yet
// because per-coin SKACoinConfig has the limit only on the node side and
// re-checking it here would duplicate consensus logic — the wallet will
// fail loudly at NewUnsignedTransaction time if the amount is out of range.
func (asset *Asset) validateSendAmountBig(sendMax bool, atomAmount int64, atomAmountBig string) error {
	if sendMax {
		return nil
	}
	ct := asset.TxCoinType()
	if atomAmountBig != "" {
		big, ok := new(big.Int).SetString(atomAmountBig, 10)
		if !ok {
			return errors.E(errors.Invalid, "invalid amount")
		}
		if big.Sign() <= 0 {
			return errors.E(errors.Invalid, "invalid amount")
		}
		// For SKA there is no per-output supply cap enforced here.
		// VAR amounts should never need the big.Int path (VAR fits in
		// int64 by definition); flag if a caller misuses it.
		if ct.IsVAR() && !big.IsInt64() {
			return errors.E(errors.Invalid, "VAR amount exceeds int64")
		}
		return nil
	}
	if atomAmount <= 0 {
		return errors.E(errors.Invalid, "invalid amount")
	}
	if ct.IsSKA() {
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
