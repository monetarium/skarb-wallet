package utils

import (
	"fmt"
	"net"
	"strings"

	"github.com/asdine/storm"
	"github.com/monetarium/monetarium-wallet/errors"
)

const (
	// Error Codes
	ErrInsufficientBalance          = "insufficient_balance"
	ErrInvalid                      = "invalid"
	ErrWalletLocked                 = "wallet_locked"
	ErrWalletDatabaseInUse          = "wallet_db_in_use"
	ErrWalletNotLoaded              = "wallet_not_loaded"
	ErrWalletNotFound               = "wallet_not_found"
	ErrWalletNameExist              = "wallet_name_exists"
	ErrReservedWalletName           = "wallet_name_reserved"
	ErrWalletIsRestored             = "wallet_is_restored"
	ErrWalletIsWatchOnly            = "watch_only_wallet"
	ErrUnusableSeed                 = "unusable_seed"
	ErrPassphraseRequired           = "passphrase_required"
	ErrInvalidPassphrase            = "invalid_passphrase"
	ErrNotConnected                 = "not_connected"
	ErrExist                        = "exists"
	ErrNotExist                     = "not_exists"
	ErrEmptySeed                    = "empty_seed"
	ErrInvalidAddress               = "invalid_address"
	ErrInvalidAuth                  = "invalid_auth"
	ErrUnavailable                  = "unavailable"
	ErrContextCanceled              = "context_canceled"
	ErrFailedPrecondition           = "failed_precondition"
	ErrSyncAlreadyInProgress        = "sync_already_in_progress"
	ErrNoPeers                      = "no_peers"
	ErrInvalidPeers                 = "invalid_peers"
	ErrListenerAlreadyExist         = "listener_already_exist"
	ErrLoggerAlreadyRegistered      = "logger_already_registered"
	ErrLogRotatorAlreadyInitialized = "log_rotator_already_initialized"
	ErrAddressDiscoveryNotDone      = "address_discovery_not_done"
	ErrChangingPassphrase           = "err_changing_passphrase"
	ErrSavingWallet                 = "err_saving_wallet"
	ErrIndexOutOfRange              = "err_index_out_of_range"
	ErrNoMixableOutput              = "err_no_mixable_output"
	ErrInvalidVoteBit               = "err_invalid_vote_bit"
	ErrNotSynced                    = "err_not_synced"
	ErrNoSeed                       = "no_seed"
)

var (
	ErrInvalidNet        = errors.New("invalid network type found")
	ErrAssetUnknown      = errors.New("unknown asset found")
	ErrBTCNotInitialized = errors.New("btc asset not initialized")
	ErrDCRNotInitialized = errors.New("dcr asset not initialized")
	ErrLTCNotInitialized = errors.New("ltc asset not initialized")

	ErrUnsupporttedIPV6Address = errors.New("IPv6 addresses unsupportted by the current network")
	ErrNetConnectionTimeout    = errors.New("Timeout on network connection")
	ErrPeerConnectionRejected  = errors.New("Peer connection rejected")
	ErrStakingAccountsMissing  = errors.New("Mixing and Unmixing Accounts are not set")

	ErrTicketPurchaseAccMissing = errors.New("ticket purchase account is not set")
)

// todo, should update this method to translate more error kinds.
// ErrLegacyHDKeyWallet is reported for a wallet database whose stored account
// extended key still uses the network's retired HD version bytes. Monetarium
// changed the mainnet HD key IDs (dprv/dpub -> mprv/mpub) and the SLIP-0044
// coin type; a wallet created before that change cannot be opened by the
// current build, and the underlying hdkeychain error ("the provided
// serialized extended key is for the wrong network") surfaces as a bare I/O
// error that tells the user nothing about what to do.
const ErrLegacyHDKeyWallet = "legacy_hd_key_wallet"

// legacyHDKeyMarker is the hdkeychain wrong-network message. Matching on text
// is unpleasant, but the error crosses two module boundaries (hdkeychain ->
// udb -> wallet.Open) wrapped as errors.IO, so the kind alone cannot tell
// this apart from a genuinely unreadable file.
const legacyHDKeyMarker = "extended key is for the wrong network"

func TranslateError(err error) error {
	if err != nil && strings.Contains(err.Error(), legacyHDKeyMarker) {
		return errors.New(ErrLegacyHDKeyWallet)
	}
	if err, ok := err.(*errors.Error); ok {
		switch err.Kind {
		case errors.InsufficientBalance:
			return errors.New(ErrInsufficientBalance)
		case errors.NotExist, storm.ErrNotFound:
			return errors.New(ErrNotExist)
		case errors.Passphrase:
			return errors.New(ErrInvalidPassphrase)
		case errors.NoPeers:
			return errors.New(ErrNoPeers)
		}
	}
	return err
}

func ErrBTCMethodNotImplemented(method string) error {
	return fmt.Errorf("%v not implemented for the %v Asset", method, BTCWalletAsset)
}

func ErrDCRMethodNotImplemented(method string) error {
	return fmt.Errorf("%v not implemented for the %v Asset", method, DCRWalletAsset)
}

func ErrLTCMethodNotImplemented(method string) error {
	return fmt.Errorf("%v not implemented for the %v Asset", method, LTCWalletAsset)
}

func TranslateNetworkError(host string, errMsg error) error {
	switch {
	case net.ParseIP(host).To4() == nil && strings.Contains(errMsg.Error(), "connect: network is unreachable"):
		return ErrUnsupporttedIPV6Address

	case strings.Contains(errMsg.Error(), "context deadline exceeded"):
		return ErrNetConnectionTimeout

	case strings.Contains(errMsg.Error(), "connect: connection refused"):
		return ErrPeerConnectionRejected

	default:
		return errMsg
	}
}
