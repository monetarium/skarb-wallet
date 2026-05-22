// Copyright (c) 2024 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package udb

import (
	"github.com/monetarium/monetarium-wallet/errors"
	"github.com/monetarium/monetarium-wallet/wallet/walletdb"
)

var (
	// accountConsolidationBucketKey is the bucket key for storing per-account
	// consolidation addresses for SSFee UTXO consolidation.
	// Key: account name (string) → Value: addressHash160 (20 bytes)
	accountConsolidationBucketKey = []byte("accountconsolidation")
)

// SetAccountConsolidationAddr sets the consolidation address (as hash160) for
// a specific account. This address will be used in vote transactions to specify
// where SSFee payments should be sent, enabling UTXO consolidation.
//
// The hash160 must be exactly 20 bytes. If the hash160 is nil or empty, this
// function returns an error. To clear a consolidation address and revert to the
// default, use ClearAccountConsolidationAddr instead.
func SetAccountConsolidationAddr(dbtx walletdb.ReadWriteTx, accountName string,
	hash160 []byte) error {

	const op errors.Op = "udb.SetAccountConsolidationAddr"

	if len(hash160) != 20 {
		return errors.E(op, errors.Invalid,
			errors.Errorf("hash160 must be exactly 20 bytes, got %d", len(hash160)))
	}

	if accountName == "" {
		return errors.E(op, errors.Invalid, "account name cannot be empty")
	}

	b := dbtx.ReadWriteBucket(accountConsolidationBucketKey)
	err := b.Put([]byte(accountName), hash160)
	if err != nil {
		return errors.E(op, errors.IO, err)
	}

	return nil
}

// GetAccountConsolidationAddr retrieves the consolidation address (as hash160)
// for a specific account. If no custom consolidation address has been set for
// the account, this function returns nil for the hash160, indicating that the
// default address (first external address of the account) should be used.
//
// The caller is responsible for handling the nil case and deriving the default
// address using GetFirstExternalAddress.
func GetAccountConsolidationAddr(dbtx walletdb.ReadTx, accountName string) ([]byte, error) {
	const op errors.Op = "udb.GetAccountConsolidationAddr"

	if accountName == "" {
		return nil, errors.E(op, errors.Invalid, "account name cannot be empty")
	}

	b := dbtx.ReadBucket(accountConsolidationBucketKey)
	if b == nil {
		// Bucket doesn't exist yet (wallet not upgraded or no addresses set).
		// Return nil to indicate default should be used.
		return nil, nil
	}

	hash160 := b.Get([]byte(accountName))
	if hash160 == nil {
		// No custom consolidation address set for this account.
		// Return nil to indicate default should be used.
		return nil, nil
	}

	if len(hash160) != 20 {
		return nil, errors.E(op, errors.IO,
			errors.Errorf("invalid hash160 length %d for account %q",
				len(hash160), accountName))
	}

	// Return a copy to prevent modifications to database data
	result := make([]byte, 20)
	copy(result, hash160)
	return result, nil
}

// ClearAccountConsolidationAddr removes the custom consolidation address for
// a specific account, causing it to revert to the default behavior (using the
// first external address of the account).
func ClearAccountConsolidationAddr(dbtx walletdb.ReadWriteTx, accountName string) error {
	const op errors.Op = "udb.ClearAccountConsolidationAddr"

	if accountName == "" {
		return errors.E(op, errors.Invalid, "account name cannot be empty")
	}

	b := dbtx.ReadWriteBucket(accountConsolidationBucketKey)
	err := b.Delete([]byte(accountName))
	if err != nil {
		return errors.E(op, errors.IO, err)
	}

	return nil
}

// Note: GetFirstExternalAddress is implemented at the wallet layer
// (wallet/wallet.go) since it requires access to the address derivation
// functionality which is part of the Wallet struct.
