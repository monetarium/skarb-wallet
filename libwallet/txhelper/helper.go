package txhelper

import (
	"fmt"
	"math"
	"math/big"

	"github.com/monetarium/monetarium-wallet/wallet"
	"github.com/monetarium/skarb-wallet/ui/values"
	"github.com/monetarium/monetarium-node/dcrutil"
	"github.com/monetarium/monetarium-node/wire"
	"github.com/monetarium/monetarium-explorer/txhelpers"
)

func MsgTxFeeSizeRate(transactionHex string) (msgTx *wire.MsgTx, fee dcrutil.Amount, size int, feeRate dcrutil.Amount, err error) {
	msgTx, err = txhelpers.MsgTxFromHex(transactionHex)
	if err != nil {
		return
	}

	size = msgTx.SerializeSize()
	fee, feeRate = txhelpers.TxFeeRate(msgTx)
	return
}

func TransactionAmountAndDirection(inputTotal, outputTotal, fee int64) (amount int64, direction int32) {
	amountDifference := outputTotal - inputTotal

	if amountDifference < 0 && float64(fee) == math.Abs(float64(amountDifference)) {
		// transferred internally, the only real amount spent was transaction fee
		direction = TxDirectionTransferred
		amount = fee
	} else if amountDifference > 0 {
		// received
		direction = TxDirectionReceived
		amount = outputTotal
	} else {
		// sent
		direction = TxDirectionSent
		amount = inputTotal - outputTotal - fee
	}

	return
}

// TransactionAmountAndDirectionBig is the lossless companion of
// TransactionAmountAndDirection: it operates on *big.Int totals so it stays
// correct for SKA transactions whose wallet-input / wallet-output sums
// overflow int64 (a single SKA UTXO above ~9.22 SKA). Without this path,
// the int64 classifier received clamped values and mis-classified large
// SKA sends as "Received" (because the input clamped at MaxInt64 while
// the change-only output stayed small and the subtraction inverted).
//
// inputTotal, outputTotal: sum of wallet-owned inputs / outputs in atoms.
// fee: the absolute tx fee. nil arguments are treated as zero so callers
// can pass *big.Int values straight from an SKA aggregate without
// special-casing the empty case.
//
// Returns a fresh *big.Int amount; the direction matches the int64 path
// for amounts that fit in int64.
func TransactionAmountAndDirectionBig(inputTotal, outputTotal, fee *big.Int) (amount *big.Int, direction int32) {
	in := orZero(inputTotal)
	out := orZero(outputTotal)
	f := orZero(fee)

	diff := new(big.Int).Sub(out, in) // out - in
	switch {
	case diff.Sign() < 0:
		absDiff := new(big.Int).Neg(diff)
		if absDiff.Cmp(f) == 0 {
			// transferred internally: the only thing that left the wallet was the fee
			direction = TxDirectionTransferred
			return new(big.Int).Set(f), direction
		}
		direction = TxDirectionSent
		// sent_external = in - out - fee
		amt := new(big.Int).Sub(in, out)
		amt.Sub(amt, f)
		return amt, direction
	case diff.Sign() > 0:
		direction = TxDirectionReceived
		return new(big.Int).Set(out), direction
	default:
		// in == out and != 0 in the normal case → all wallet inputs were
		// also wallet outputs (rare; e.g. consolidating across accounts).
		// Match the int64 fallback: classify as Sent with amount = 0.
		direction = TxDirectionSent
		return new(big.Int).SetInt64(0), direction
	}
}

func orZero(b *big.Int) *big.Int {
	if b == nil {
		return new(big.Int)
	}
	return b
}

func FormatTransactionType(txType wallet.TransactionType) string {
	switch txType {
	case wallet.TransactionTypeCoinbase:
		return TxTypeCoinBase
	case wallet.TransactionTypeTicketPurchase:
		return TxTypeTicketPurchase
	case wallet.TransactionTypeVote:
		return TxTypeVote
	case wallet.TransactionTypeRevocation:
		return TxTypeRevocation
	default:
		return TxTypeRegular
	}
}

func TxDirectionString(direction int32) string {
	switch direction {
	case TxDirectionReceived:
		return values.String(values.StrReceived)
	case TxDirectionSent:
		return values.String(values.StrSent)
	case TxDirectionTransferred:
		return values.String(values.StrTransferred)
	default:
		return fmt.Sprintf("%d", direction)
	}
}
