package dcr

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/monetarium/monetarium-wallet/errors"
	w "github.com/monetarium/monetarium-wallet/wallet"
	"github.com/monetarium/skarb-wallet/libwallet/addresshelper"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/monetarium-node/chaincfg"
	"github.com/monetarium/monetarium-node/dcrutil"
)

func (asset *Asset) GetAccounts() (string, error) {
	accountsResponse, err := asset.GetAccountsRaw()
	if err != nil {
		return "", err
	}

	result, _ := json.Marshal(accountsResponse)
	return string(result), nil
}

func (asset *Asset) GetAccountsRaw() (*sharedW.Accounts, error) {
	if !asset.WalletOpened() {
		return nil, utils.ErrDCRNotInitialized
	}

	ctx, _ := asset.ShutdownContextWithCancel()
	resp, err := asset.Internal().DCR.Accounts(ctx)
	if err != nil {
		return nil, err
	}

	accounts := make([]*sharedW.Account, len(resp.Accounts))
	for i, a := range resp.Accounts {
		balance, err := asset.GetAccountBalance(int32(a.AccountNumber))
		if err != nil {
			return nil, err
		}

		accounts[i] = &sharedW.Account{
			AccountProperties: sharedW.AccountProperties{
				AccountNumber: a.AccountNumber,
				AccountName:   a.AccountName,
			},
			WalletID:         asset.ID,
			Number:           int32(a.AccountNumber),
			Name:             a.AccountName,
			Balance:          balance,
			ExternalKeyCount: int32(a.LastUsedExternalIndex + AddressGapLimit), // Add gap limit
			InternalKeyCount: int32(a.LastUsedInternalIndex + AddressGapLimit),
			ImportedKeyCount: int32(a.ImportedKeyCount),
		}
	}

	return &sharedW.Accounts{
		CurrentBlockHash:   resp.CurrentBlockHash[:],
		CurrentBlockHeight: resp.CurrentBlockHeight,
		Accounts:           accounts,
	}, nil
}

func (asset *Asset) AccountsIterator() (*AccountsIterator, error) {
	accounts, err := asset.GetAccountsRaw()
	if err != nil {
		return nil, err
	}

	return &AccountsIterator{
		currentIndex: 0,
		accounts:     accounts.Accounts,
	}, nil
}

func (accountsInterator *AccountsIterator) Next() *sharedW.Account {
	if accountsInterator.currentIndex < len(accountsInterator.accounts) {
		account := accountsInterator.accounts[accountsInterator.currentIndex]
		accountsInterator.currentIndex++
		return account
	}

	return nil
}

func (accountsInterator *AccountsIterator) Reset() {
	accountsInterator.currentIndex = 0
}

func (asset *Asset) GetAccount(accountNumber int32) (*sharedW.Account, error) {
	accounts, err := asset.GetAccountsRaw()
	if err != nil {
		return nil, err
	}

	for _, account := range accounts.Accounts {
		if account.Number == accountNumber {
			return account, nil
		}
	}

	return nil, errors.New(utils.ErrNotExist)
}

func (asset *Asset) GetAccountBalance(accountNumber int32) (*sharedW.Balance, error) {
	if !asset.WalletOpened() {
		return nil, utils.ErrDCRNotInitialized
	}

	ctx, _ := asset.ShutdownContextWithCancel()
	balance, err := asset.Internal().DCR.AccountBalance(ctx, uint32(accountNumber), asset.RequiredConfirmations())
	if err != nil {
		return nil, err
	}

	lockedAmt, err := asset.lockedAmount(ctx, accountNumber)
	if err != nil {
		return nil, err
	}

	return &sharedW.Balance{
		Total:                   Amount(balance.Total),
		Spendable:               Amount(balance.Spendable - lockedAmt),
		ImmatureReward:          Amount(balance.ImmatureCoinbaseRewards),
		ImmatureStakeGeneration: Amount(balance.ImmatureStakeGeneration),
		LockedByTickets:         Amount(balance.LockedByTickets),
		VotingAuthority:         Amount(balance.VotingAuthority),
		UnConfirmed:             Amount(balance.Unconfirmed),
		Locked:                  Amount(lockedAmt),
	}, nil
}

// lockedAmount is the total value of locked outputs, as locked with
// LockUnspent.
func (asset *Asset) lockedAmount(ctx context.Context, acctNumber int32) (dcrutil.Amount, error) {
	accountName, err := asset.AccountName(acctNumber)
	if err != nil {
		return dcrutil.Amount(0), err
	}

	lockedOutpoints, err := asset.Internal().DCR.LockedOutpoints(ctx, accountName)
	if err != nil {
		return 0, err
	}

	var sum float64
	for _, op := range lockedOutpoints {
		sum += op.Amount
	}

	return dcrutil.NewAmount(sum)
}

func (asset *Asset) SpendableForAccount(account int32) (int64, error) {
	if !asset.WalletOpened() {
		return -1, utils.ErrDCRNotInitialized
	}

	ctx, _ := asset.ShutdownContextWithCancel()
	bals, err := asset.Internal().DCR.AccountBalance(ctx, uint32(account), asset.RequiredConfirmations())
	if err != nil {
		log.Error(err)
		return 0, utils.TranslateError(err)
	}

	lockedAmt, err := asset.lockedAmount(ctx, account)
	if err != nil {
		return 0, err
	}

	return int64(bals.Spendable - lockedAmt), nil
}

// UnspentOutputs returns unspent outputs that can be used for transactions.
// Unspent outputs that are locked by the wallet are not returned as valid
// unspent utxos.
func (asset *Asset) UnspentOutputs(account int32) ([]*sharedW.UnspentOutput, error) {
	if !asset.WalletOpened() {
		return nil, utils.ErrDCRNotInitialized
	}

	// Upstream wallet.UnspentOutputs only walks the bucket for ONE coin
	// type (`policy.CoinType`, defaulting to 0 = VAR). A wallet that
	// holds only SKA1 returns zero results here, so the manual UTXO
	// selector and any downstream consumer gets an empty list to filter
	// — the user-facing symptom is "I have 530 SKA1 but the UTXO
	// selector is blank". Iterate over every active coin type and
	// concatenate the per-coin results into a single flat slice; the
	// UI then filters by the send-page's currently-selected coin type
	// (manual_coin_selection.go) so the user sees only what they can
	// spend with the active selection.
	ctx, _ := asset.ShutdownContextWithCancel()
	activeCoins := asset.ActiveCoinTypes()
	unspents := make([]*w.TransactionOutput, 0)
	for _, ct := range activeCoins {
		policy := w.OutputSelectionPolicy{
			Account:               uint32(account),
			RequiredConfirmations: asset.RequiredConfirmations(),
			CoinType:              ct,
		}
		perCoin, err := asset.Internal().DCR.UnspentOutputs(ctx, policy)
		if err != nil {
			return nil, err
		}
		unspents = append(unspents, perCoin...)
	}

	// blockTimeCache memoises block timestamps by height so a wallet with
	// many UTXOs spread across distinct blocks only does one BlockInfo
	// lookup per height instead of one per UTXO.
	blockTimeCache := make(map[int32]time.Time)

	unspentOutputs := make([]*sharedW.UnspentOutput, 0, len(unspents))
	for _, utxo := range unspents {
		hash := utxo.OutPoint.Hash
		if asset.Internal().DCR.LockedOutpoint(&hash, utxo.OutPoint.Index) {
			continue // utxo is locked.
		}

		addresses := addresshelper.PkScriptAddresses(asset.chainParams, utxo.Output.PkScript)

		var confirmations int32
		inputBlockHeight := utxo.ContainingBlock.Height
		if inputBlockHeight != -1 {
			confirmations = asset.GetBestBlockHeight() - inputBlockHeight + 1
		}

		// The UTXO's real creation time is the timestamp of its containing
		// block, NOT utxo.ReceiveTime — the latter is when this wallet
		// *recorded* the credit, which is identical for every output picked
		// up in a single scan/rescan (symptom: every row in the UTXO
		// selector shows the same date/time). Fall back to ReceiveTime only
		// for still-unconfirmed outputs (height == -1) or if the block
		// lookup fails.
		receiveTime := utxo.ReceiveTime
		if inputBlockHeight != -1 {
			if t, ok := blockTimeCache[inputBlockHeight]; ok {
				receiveTime = t
			} else if bt := asset.blockCreationTime(ctx, inputBlockHeight); !bt.IsZero() {
				blockTimeCache[inputBlockHeight] = bt
				receiveTime = bt
			}
		}

		addr := ""
		if len(addresses) > 0 {
			addr = addresses[0]
		}

		// SKA UTXOs ship with Output.Value=0; their atom count lives in
		// Output.SKAValue (*big.Int). Carry both channels through:
		// the int64 Amount stays VAR-shaped (0 for SKA, real for VAR)
		// to keep legacy callers happy, and SKAAmountAtoms holds the
		// lossless decimal-string atoms when this UTXO is SKA. The
		// CoinType field lets the UI filter — without it, manual-coin-
		// selection treats every UTXO as VAR.
		ct := uint8(utxo.Output.CoinType)
		var skaAtomsStr string
		if utxo.Output.CoinType.IsSKA() && utxo.Output.SKAValue != nil && utxo.Output.SKAValue.Sign() > 0 {
			skaAtomsStr = utxo.Output.SKAValue.String()
		}
		unspentOutputs = append(unspentOutputs, &sharedW.UnspentOutput{
			TxID:           utxo.OutPoint.Hash.String(),
			Vout:           utxo.OutPoint.Index,
			Address:        addr,
			Amount:         Amount(utxo.Output.Value),
			ScriptPubKey:   hex.EncodeToString(utxo.Output.PkScript),
			ReceiveTime:    receiveTime,
			Confirmations:  confirmations,
			Spendable:      true,
			Tree:           utxo.OutPoint.Tree,
			CoinType:       ct,
			SKAAmountAtoms: skaAtomsStr,
		})
	}

	return unspentOutputs, nil
}

// blockCreationTime resolves the timestamp of the block at the given height
// via the wallet's local header store. Returns the zero time.Time on any
// error so callers can fall back to another time source.
func (asset *Asset) blockCreationTime(ctx context.Context, height int32) time.Time {
	identifier := w.NewBlockIdentifierFromHeight(height)
	info, err := asset.Internal().DCR.BlockInfo(ctx, identifier)
	if err != nil || info == nil {
		return time.Time{}
	}
	return time.Unix(info.Timestamp, 0)
}

func (asset *Asset) CreateNewAccount(accountName, privPass string) (int32, error) {
	err := asset.UnlockWallet(privPass)
	if err != nil {
		return -1, err
	}

	defer asset.LockWallet()

	return asset.NextAccount(accountName)
}

func (asset *Asset) NextAccount(accountName string) (int32, error) {
	if !asset.WalletOpened() {
		return -1, utils.ErrDCRNotInitialized
	}

	if asset.IsLocked() {
		return -1, errors.New(utils.ErrWalletLocked)
	}

	ctx, _ := asset.ShutdownContextWithCancel()
	accountNumber, err := asset.Internal().DCR.NextAccount(ctx, accountName)
	if err != nil {
		return -1, err
	}

	return int32(accountNumber), nil
}

func (asset *Asset) RenameAccount(accountNumber int32, newName string) error {
	if !asset.WalletOpened() {
		return utils.ErrDCRNotInitialized
	}

	ctx, _ := asset.ShutdownContextWithCancel()
	err := asset.Internal().DCR.RenameAccount(ctx, uint32(accountNumber), newName)
	if err != nil {
		return utils.TranslateError(err)
	}

	return nil
}

func (asset *Asset) AccountName(accountNumber int32) (string, error) {
	name, err := asset.AccountNameRaw(uint32(accountNumber))
	if err != nil {
		return "", utils.TranslateError(err)
	}
	return name, nil
}

func (asset *Asset) AccountNameRaw(accountNumber uint32) (string, error) {
	if !asset.WalletOpened() {
		return "", utils.ErrDCRNotInitialized
	}

	ctx, _ := asset.ShutdownContextWithCancel()
	return asset.Internal().DCR.AccountName(ctx, accountNumber)
}

func (asset *Asset) AccountNumber(accountName string) (int32, error) {
	if !asset.WalletOpened() {
		return -1, utils.ErrDCRNotInitialized
	}

	ctx, _ := asset.ShutdownContextWithCancel()
	accountNumber, err := asset.Internal().DCR.AccountNumber(ctx, accountName)
	return int32(accountNumber), utils.TranslateError(err)
}

func (asset *Asset) HasAccount(accountName string) bool {
	if !asset.WalletOpened() {
		return false
	}

	ctx, _ := asset.ShutdownContextWithCancel()
	_, err := asset.Internal().DCR.AccountNumber(ctx, accountName)
	return err == nil
}

func (asset *Asset) HDPathForAccount(accountNumber int32) (string, error) {
	if !asset.WalletOpened() {
		return "", utils.ErrDCRNotInitialized
	}

	ctx, _ := asset.ShutdownContextWithCancel()
	cointype, err := asset.Internal().DCR.CoinType(ctx)
	if err != nil {
		return "", utils.TranslateError(err)
	}

	var hdPath string
	isLegacyCoinType := cointype == asset.chainParams.LegacyCoinType
	if asset.chainParams.Name == chaincfg.MainNetParams().Name {
		if isLegacyCoinType {
			hdPath = LegacyMainnetHDPath
		} else {
			hdPath = MainnetHDPath
		}
	} else {
		if isLegacyCoinType {
			hdPath = LegacyTestnetHDPath
		} else {
			hdPath = TestnetHDPath
		}
	}

	return hdPath + strconv.Itoa(int(accountNumber)), nil
}

func (asset *Asset) GetExtendedPubKey(account int32) (string, error) {
	if !asset.WalletOpened() {
		return "", utils.ErrDCRNotInitialized
	}

	loadedAsset := asset.Internal().DCR
	if loadedAsset == nil {
		return "", fmt.Errorf("dcr asset not initialised")
	}
	ctx, _ := asset.ShutdownContextWithCancel()
	extendedPublicKey, err := loadedAsset.AccountXpub(ctx, uint32(account))
	if err != nil {
		return "", err
	}
	return extendedPublicKey.String(), nil
}
