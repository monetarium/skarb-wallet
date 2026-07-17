package dcr

import (
	"encoding/json"
	"strings"

	"github.com/asdine/storm"
	"github.com/asdine/storm/q"
	"github.com/monetarium/monetarium-node/chaincfg/chainhash"
	"github.com/monetarium/monetarium-node/cointype"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/libwallet/txhelper"
	"github.com/monetarium/skarb-wallet/libwallet/utils"
)

const (
	// Export constants for use in mobile apps
	// since gomobile excludes fields from sub packages.
	TxFilterAll         = utils.TxFilterAll
	TxFilterSent        = utils.TxFilterSent
	TxFilterReceived    = utils.TxFilterReceived
	TxFilterTransferred = utils.TxFilterTransferred
	TxFilterStaking     = utils.TxFilterStaking
	TxFilterCoinBase    = utils.TxFilterCoinBase
	TxFilterRegular     = utils.TxFilterRegular
	TxFilterMixed       = utils.TxFilterMixed
	TxFilterVoted       = utils.TxFilterVoted
	TxFilterRevoked     = utils.TxFilterRevoked
	TxFilterImmature    = utils.TxFilterImmature
	TxFilterLive        = utils.TxFilterLive
	TxFilterUnmined     = utils.TxFilterUnmined
	TxFilterExpired     = utils.TxFilterExpired
	TxFilterTickets     = utils.TxFilterTickets

	TxFilterSplit       = utils.TxFilterSplit
	TxFilterStakeFee    = utils.TxFilterStakeFee
	TxFilterTicketVoted = utils.TxFilterTicketVoted
	TxFilterRegularList = utils.TxFilterRegularList
	TxFilterStakingList = utils.TxFilterStakingList
	TxFilterRewardList  = utils.TxFilterRewardList
	TxFilterMissed      = utils.TxFilterMissed
	TxFilterRewardPoW   = utils.TxFilterRewardPoW
	TxFilterRewardPoS   = utils.TxFilterRewardPoS

	TxFilterStakingNoSplit = utils.TxFilterStakingNoSplit

	TxDirectionInvalid     = txhelper.TxDirectionInvalid
	TxDirectionSent        = txhelper.TxDirectionSent
	TxDirectionReceived    = txhelper.TxDirectionReceived
	TxDirectionTransferred = txhelper.TxDirectionTransferred

	TxTypeRegular        = txhelper.TxTypeRegular
	TxTypeCoinBase       = txhelper.TxTypeCoinBase
	TxTypeTicketPurchase = txhelper.TxTypeTicketPurchase
	TxTypeVote           = txhelper.TxTypeVote
	TxTypeRevocation     = txhelper.TxTypeRevocation
	TxTypeMixed          = txhelper.TxTypeMixed

	TicketStatusUnmined        = "unmined"
	TicketStatusImmature       = "immature"
	TicketStatusLive           = "live"
	TicketStatusVotedOrRevoked = "votedrevoked"
	TicketStatusExpired        = "expired"
)

func (asset *Asset) PublishUnminedTransactions() error {
	if !asset.WalletOpened() {
		return utils.ErrDCRNotInitialized
	}

	n, err := asset.Internal().DCR.NetworkBackend()
	if err != nil {
		log.Error(err)
		return err
	}

	ctx, _ := asset.ShutdownContextWithCancel()
	return asset.Internal().DCR.PublishUnminedTransactions(ctx, n)
}

func (asset *Asset) GetTransaction(txHash string) (string, error) {
	transaction, err := asset.GetTransactionRaw(txHash)
	if err != nil {
		log.Error(err)
		return "", err
	}

	result, err := json.Marshal(transaction)
	if err != nil {
		return "", err
	}

	return string(result), nil
}

func (asset *Asset) GetTransactionRaw(txHash string) (*sharedW.Transaction, error) {
	if !asset.WalletOpened() {
		return nil, utils.ErrDCRNotInitialized
	}

	hash, err := chainhash.NewHashFromStr(txHash)
	if err != nil {
		log.Error(err)
		return nil, err
	}

	ctx, _ := asset.ShutdownContextWithCancel()
	txSummary, _, blockHash, err := asset.Internal().DCR.TransactionSummary(ctx, hash)
	if err != nil {
		log.Error(err)
		return nil, err
	}

	return asset.decodeTransactionWithTxSummary(txSummary, blockHash)
}

func (asset *Asset) GetTransactions(offset, limit, txFilter int32, newestFirst bool) (string, error) {
	transactions, err := asset.GetTransactionsRaw(offset, limit, txFilter, newestFirst, "")
	if err != nil {
		return "", err
	}

	jsonEncodedTransactions, err := json.Marshal(&transactions)
	if err != nil {
		return "", err
	}

	return string(jsonEncodedTransactions), nil
}

func (asset *Asset) GetTransactionsRaw(offset, limit, txFilter int32, newestFirst bool, txHashSearch string) (transactions []*sharedW.Transaction, err error) {
	txHashSearch = strings.TrimSpace(txHashSearch)
	if txHashSearch != "" {
		err = asset.GetWalletDataDb().Find(q.Eq("Hash", txHashSearch), &transactions)
		return
	}
	err = asset.GetWalletDataDb().Read(offset, limit, txFilter, newestFirst, asset.RequiredConfirmations(), asset.GetBestBlockHeight(), &transactions)
	return
}

func (asset *Asset) CountTransactions(txFilter int32) (int, error) {
	return asset.GetWalletDataDb().Count(txFilter, asset.RequiredConfirmations(), asset.GetBestBlockHeight(), &sharedW.Transaction{})
}

func (asset *Asset) TicketHasVotedOrRevoked(ticketHash string) (bool, error) {
	err := asset.GetWalletDataDb().FindOne("TicketSpentHash", ticketHash, &sharedW.Transaction{})
	if err != nil {
		if err == storm.ErrNotFound {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

func (asset *Asset) TicketSpender(ticketHash string) (*sharedW.Transaction, error) {
	var spender sharedW.Transaction
	err := asset.GetWalletDataDb().FindOne("TicketSpentHash", ticketHash, &spender)
	if err != nil {
		if err == storm.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}

	return &spender, nil
}

func (asset *Asset) TransactionOverview() (txOverview *sharedW.TransactionOverview, err error) {
	txOverview = &sharedW.TransactionOverview{}

	txOverview.Sent, err = asset.CountTransactions(TxFilterSent)
	if err != nil {
		return
	}

	txOverview.Received, err = asset.CountTransactions(TxFilterReceived)
	if err != nil {
		return
	}

	txOverview.Transferred, err = asset.CountTransactions(TxFilterTransferred)
	if err != nil {
		return
	}

	txOverview.Mixed, err = asset.CountTransactions(TxFilterMixed)
	if err != nil {
		return
	}

	txOverview.Staking, err = asset.CountTransactions(TxFilterStaking)
	if err != nil {
		return
	}

	txOverview.Coinbase, err = asset.CountTransactions(TxFilterCoinBase)
	if err != nil {
		return
	}

	txOverview.All = txOverview.Sent + txOverview.Received + txOverview.Transferred + txOverview.Mixed +
		txOverview.Staking + txOverview.Coinbase

	return txOverview, nil
}

func (asset *Asset) TxMatchesFilter(tx *sharedW.Transaction, txFilter int32) bool {
	bestBlock := asset.GetBestBlockHeight()

	// tickets with block height less than this are matured.
	maturityBlock := bestBlock - int32(asset.chainParams.TicketMaturity)

	// tickets with block height less than this are expired.
	expiryBlock := bestBlock - int32(asset.chainParams.TicketMaturity+uint16(asset.chainParams.TicketExpiry))

	switch txFilter {
	case TxFilterSent:
		return tx.Type == TxTypeRegular && tx.Direction == TxDirectionSent
	case TxFilterReceived:
		return tx.Type == TxTypeRegular && tx.Direction == TxDirectionReceived
	case TxFilterTransferred:
		return tx.Type == TxTypeRegular && tx.Direction == TxDirectionTransferred
	case TxFilterStaking:
		switch tx.Type {
		case TxTypeTicketPurchase:
			fallthrough
		case TxTypeVote:
			fallthrough
		case TxTypeRevocation:
			return true
		}

		return false
	case TxFilterCoinBase:
		return tx.Type == TxTypeCoinBase
	case TxFilterRegular:
		return tx.Type == TxTypeRegular
	case TxFilterMixed:
		return tx.Type == TxTypeMixed
	case TxFilterVoted:
		return tx.Type == TxTypeVote
	case TxFilterRevoked:
		return tx.Type == TxTypeRevocation
	case TxFilterImmature:
		return tx.Type == TxTypeTicketPurchase &&
			(tx.BlockHeight > maturityBlock) // not matured
	case TxFilterLive:
		// ticket is live if we don't have the spender hash and it hasn't expired.
		// we cannot detect missed tickets over spv.
		return tx.Type == TxTypeTicketPurchase &&
			tx.TicketSpender == "" &&
			tx.BlockHeight > 0 &&
			tx.BlockHeight <= maturityBlock &&
			tx.BlockHeight > expiryBlock // not expired
	case TxFilterUnmined:
		return tx.Type == TxTypeTicketPurchase && tx.BlockHeight == -1
	case TxFilterExpired:
		return tx.Type == TxTypeTicketPurchase &&
			tx.TicketSpender == "" &&
			tx.BlockHeight > 0 &&
			tx.BlockHeight <= expiryBlock
	case TxFilterTickets:
		return tx.Type == TxTypeTicketPurchase
	case TxFilterSplit:
		// A "split" tx is a plain Regular spend that both funds from and
		// returns to the default account (0) only — i.e. it just splits the
		// default account's own coins (e.g. preparing ticket-sized outputs)
		// without touching any other account or an external party. Mined or
		// not (see IsSplitTx).
		return IsSplitTx(tx)
	case TxFilterStakeFee:
		return tx.IsStakeFee
	case TxFilterTicketVoted:
		// A ticket purchase whose spender turned out to be a Vote. Requires a
		// per-tx spender lookup in the wallet DB (TicketSpender mirrors
		// ui/page/staking/utils.go). The lookup is a single indexed FindOne on
		// TicketSpentHash, but note it runs once per candidate tx, so callers
		// filtering large lists pay one DB read per ticket.
		if tx.Type != TxTypeTicketPurchase {
			return false
		}
		spender, err := asset.TicketSpender(tx.Hash)
		if err != nil {
			log.Errorf("TxMatchesFilter(TxFilterTicketVoted) spender lookup for %s: %v", tx.Hash, err)
			return false
		}
		return spender != nil && spender.Type == TxTypeVote
	case TxFilterRegularList:
		// Split rows deliberately stay in the Regular "All types" view —
		// they are ordinary self-transfers that fund tickets, and users
		// look for them next to their sends (a dedicated Split filter
		// exists for isolating them).
		return (tx.Type == TxTypeRegular || tx.Type == TxTypeMixed) &&
			!tx.IsStakeFee
	case TxFilterStakingList:
		return tx.Type == TxTypeTicketPurchase || IsSplitTx(tx)
	case TxFilterStakingNoSplit:
		// Staking "All without Split": every ticket purchase, no split rows.
		return tx.Type == TxTypeTicketPurchase
	case TxFilterRewardList:
		// Only actual rewards: mining (coinbase + MF stake fees) and
		// staking (votes + SF stake fees). Revocations merely return the
		// ticket price without any reward, so they don't belong here —
		// they remain visible through the Staking page statuses.
		return tx.Type == TxTypeCoinBase || tx.IsStakeFee ||
			tx.Type == TxTypeVote
	case TxFilterRewardPoW:
		// Mining rewards: coinbase block reward, or a miner-fee (MF) SSFee.
		return tx.Type == TxTypeCoinBase || (tx.IsStakeFee && tx.StakeFeeKind == "MF")
	case TxFilterRewardPoS:
		// Staking rewards: votes, or a staker-fee (SF) SSFee.
		return tx.Type == TxTypeVote ||
			(tx.IsStakeFee && tx.StakeFeeKind == "SF")
	case TxFilterMissed:
		// Missed tickets aren't detectable over SPV; the filter exists so the
		// UI can keep a "Missed" entry that always yields an empty list.
		return false
	case TxFilterAll:
		return true
	}

	return false
}

// IsSplitTx reports whether tx is a "split" transaction: a Regular spend (mined
// or unmined) whose every input and every output belongs to the default account
// (account 0). Used by TxFilterSplit, TxFilterRegularList and TxFilterStakingList.
//
// Confirmation is deliberately NOT required: a default->default self-transfer
// belongs on the Staking tab whether or not it has been mined yet — an unmined,
// or a not-yet-resync'd (BlockHeight still -1) split must not fall back into the
// Regular tab. A normal send to an external party is still excluded because its
// recipient output carries AccountNumber == -1 (not wallet-owned), so the
// all-outputs-on-account-0 test below fails for it.
//
// It relies on tx being a fully-decoded sharedW.Transaction with Inputs and
// Outputs populated (each carrying an AccountNumber, with -1 meaning the
// in/out is not owned by this wallet). TxMatchesFilter is only ever called on
// such decoded transactions (the UI passes the result of DecodeTransaction /
// GetTransactionsRaw), so the account fields are available here.
func IsSplitTx(tx *sharedW.Transaction) bool {
	if tx.Type != TxTypeRegular {
		return false
	}
	// Splits prepare VAR for ticket purchases and staking is VAR-only, so
	// a same-account SKA transfer (e.g. a send to one's own account) is
	// never a split — it stays on the Regular tab.
	if tx.CoinType != uint8(cointype.CoinTypeVAR) {
		return false
	}
	if len(tx.Inputs) == 0 || len(tx.Outputs) == 0 {
		return false
	}
	for _, in := range tx.Inputs {
		if in.AccountNumber != DefaultAccountNum {
			return false
		}
	}
	for _, out := range tx.Outputs {
		if out.AccountNumber != DefaultAccountNum {
			return false
		}
	}
	return true
}

// ApplySplitAmounts rewrites each split transaction's Amount to the sum of its
// outputs that ticket purchases actually consumed — i.e. "all outputs minus
// change". The int64 classifier reports amount == fee for a split (every input
// AND output is on the default account, so everything looks like change), and
// the split's ticket-funding outputs can't be told apart from its change within
// the tx itself: individualSplit (monetarium-wallet createtx.go) pays them to an
// INTERNAL-branch address just like the change output. The only exact signal is
// cross-tx: an output is ticket-funding iff a ticket purchase spends it. txs
// must therefore contain the ticket purchases alongside the splits (any coarse
// superset fetch does); a split whose tickets are absent keeps its stored
// Amount (the fee) as a fallback. Mutation is by pointer and idempotent, so
// repeated application over cached rows is safe.
func ApplySplitAmounts(txs []*sharedW.Transaction) {
	consumed := make(map[string]int64)
	for _, tx := range txs {
		if tx == nil || tx.Type != TxTypeTicketPurchase {
			continue
		}
		for _, in := range tx.Inputs {
			if in.PreviousTransactionHash != "" && in.Amount > 0 {
				consumed[in.PreviousTransactionHash] += in.Amount
			}
		}
	}
	if len(consumed) == 0 {
		return
	}
	for _, tx := range txs {
		if tx == nil || !IsSplitTx(tx) {
			continue
		}
		if sum, ok := consumed[tx.Hash]; ok && sum > 0 {
			tx.Amount = sum
		}
	}
}

func (asset *Asset) TxMatchesFilter2(direction, blockHeight int32, txType, ticketSpender string, txFilter int32) bool {
	tx := sharedW.Transaction{
		Type:          txType,
		Direction:     direction,
		BlockHeight:   blockHeight,
		TicketSpender: ticketSpender,
	}
	return asset.TxMatchesFilter(&tx, txFilter)
}

func Confirmations(bestBlock int32, tx *sharedW.Transaction) int32 {
	if tx.BlockHeight == sharedW.UnminedTxHeight {
		return 0
	}

	return (bestBlock - tx.BlockHeight) + 1
}

func TicketStatus(ticketMaturity, ticketExpiry, bestBlock int32, tx *sharedW.Transaction) string {
	if tx.Type != TxTypeTicketPurchase {
		return ""
	}

	confirmations := Confirmations(bestBlock, tx)
	if confirmations == 0 {
		return TicketStatusUnmined
	} else if confirmations <= ticketMaturity {
		return TicketStatusImmature
	} else if confirmations > (ticketMaturity + ticketExpiry) {
		return TicketStatusExpired
	} else if tx.TicketSpender != "" {
		return TicketStatusVotedOrRevoked
	}

	return TicketStatusLive
}
