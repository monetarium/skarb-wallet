package txhelper

import (
	"fmt"
	"math"

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
