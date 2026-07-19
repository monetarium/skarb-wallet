package values

import (
	"errors"
	"strings"

	"github.com/monetarium/skarb-wallet/libwallet/utils"
)

// This files holds implementation to translate errors into user friendly messages.

var ErrDCRSupportedOnly = errors.New("only DCR implementation is currently supported")

// TranslateErr translates all server errors to user friendly messages.
func TranslateErr(errStr string) string {
	switch errStr {
	case utils.ErrInvalidPassphrase:
		return String(StrInvalidPassphrase)

	case utils.ErrNotConnected:
		return String(StrNotConnected)

	case utils.ErrInsufficientBalance:
		return String(StrInsufficientFund)

	default:
		if strings.Contains(errStr, "strconv.ParseFloat") {
			return String((StrInvalidAmount))
		}
		if strings.Contains(errStr, "inputs have less than") {
			return String((StrInvalidAmount))
		}
		// "no spendable VAR in this account (need confirmed UTXOs >= N
		// blocks deep)" fires from libwallet/assets/dcr/txauthor.go
		// makeInputSource whenever the source account has no confirmed
		// VAR UTXOs — typically because the user is sending VAR from a
		// SKA-only account, or because recently received VAR is still
		// in mempool. Either way the raw English wording leaks through
		// to the Send form's amount error label. Map it to the existing
		// localized "insufficient funds" message; the cause text in
		// parens is debug detail not user-actionable.
		if strings.Contains(errStr, "no spendable VAR") {
			return String(StrInsufficientFund)
		}
		// "subtractfeefromamount: recipient amount ... is less than fee ..." /
		// "... is below SKA dust threshold ..." / "... does not cover fee ..." /
		// "... minus fee ... is dust" fire from the vendored txauthor when
		// "subtract fee from recipient" is on and the typed amount can't
		// even cover its own fee. Became reachable from the live pre-send
		// estimate (not just Broadcast) once the estimate started honoring
		// SFFA — surface the same actionable message as any other
		// too-small-amount case instead of the raw English + atom counts.
		if strings.Contains(errStr, "subtractfeefromamount") {
			return String(StrInsufficientFundForFee)
		}
	}
	return errStr
}
