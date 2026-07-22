package walletdata

import (
	"github.com/asdine/storm"
	"github.com/asdine/storm/q"
	"github.com/monetarium/skarb-wallet/libwallet/txhelper"
	"github.com/monetarium/skarb-wallet/libwallet/utils"
)

func (db *DB) prepareTxQuery(txFilter, _ /*requiredConfirmations*/, bestBlock int32) (query storm.Query) {
	// tickets with block height less than this are matured.
	maturityBlock := bestBlock - db.ticketMaturity

	// tickets with block height less than this are expired.
	expiryBlock := bestBlock - (db.ticketMaturity + db.ticketExpiry)

	switch txFilter {
	case utils.TxFilterSent:
		query = db.walletDataDB.Select(
			q.Eq(utils.TypeFilter, txhelper.TxTypeRegular),
			q.Eq(utils.DirectionFilter, txhelper.TxDirectionSent),
		)
	case utils.TxFilterReceived:
		query = db.walletDataDB.Select(
			q.Eq(utils.TypeFilter, txhelper.TxTypeRegular),
			q.Eq(utils.DirectionFilter, txhelper.TxDirectionReceived),
		)
	case utils.TxFilterTransferred:
		query = db.walletDataDB.Select(
			q.Eq(utils.TypeFilter, txhelper.TxTypeRegular),
			q.Eq(utils.DirectionFilter, txhelper.TxDirectionTransferred),
		)
	case utils.TxFilterStaking:
		query = db.walletDataDB.Select(
			q.Or(
				q.Eq(utils.TypeFilter, txhelper.TxTypeTicketPurchase),
				q.Eq(utils.TypeFilter, txhelper.TxTypeVote),
				q.Eq(utils.TypeFilter, txhelper.TxTypeRevocation),
			),
		)
	case utils.TxFilterCoinBase:
		query = db.walletDataDB.Select(
			q.Eq(utils.TypeFilter, txhelper.TxTypeCoinBase),
		)
	case utils.TxFilterRegular:
		query = db.walletDataDB.Select(
			q.Eq(utils.TypeFilter, txhelper.TxTypeRegular),
		)
	case utils.TxFilterMixed:
		query = db.walletDataDB.Select(
			q.Eq(utils.TypeFilter, txhelper.TxTypeMixed),
		)
	case utils.TxFilterVoted:
		query = db.walletDataDB.Select(
			q.Eq(utils.TypeFilter, txhelper.TxTypeVote),
		)
	case utils.TxFilterRevoked:
		query = db.walletDataDB.Select(
			q.Eq(utils.TypeFilter, txhelper.TxTypeRevocation),
		)
	case utils.TxFilterImmature:
		query = db.walletDataDB.Select(
			q.Eq(utils.TypeFilter, txhelper.TxTypeTicketPurchase),
			q.And(
				q.Gt(utils.HeightFilter, maturityBlock),
			),
		)
	case utils.TxFilterLive:
		query = db.walletDataDB.Select(
			q.Eq(utils.TypeFilter, txhelper.TxTypeTicketPurchase),
			q.Eq(utils.TicketSpenderFilter, ""),      // not spent by a vote or revoke
			q.Gt(utils.HeightFilter, 0),              // mined
			q.Lte(utils.HeightFilter, maturityBlock), // must be matured
			q.Gt(utils.HeightFilter, expiryBlock),    // not expired
		)
	case utils.TxFilterUnmined:
		query = db.walletDataDB.Select(
			q.Eq(utils.TypeFilter, txhelper.TxTypeTicketPurchase),
			q.Or(
				q.Eq(utils.HeightFilter, -1),
			),
		)
	case utils.TxFilterExpired:
		query = db.walletDataDB.Select(
			q.Eq(utils.TypeFilter, txhelper.TxTypeTicketPurchase),
			q.Eq(utils.TicketSpenderFilter, ""), // not spent by a vote or revoke
			q.Gt(utils.HeightFilter, 0),         // mined
			q.Lte(utils.HeightFilter, expiryBlock),
		)
	case utils.TxFilterTickets:
		query = db.walletDataDB.Select(
			q.Eq(utils.TypeFilter, txhelper.TxTypeTicketPurchase),
		)

	// Coarse supersets for the reclassification filters (Skarb). Their exact
	// membership is decided in TxMatchesFilter over the decoded row, but the
	// query layer must still narrow the paged universe: the old fall-through
	// to q.True() made every bounded scan walk ALL rows newest-first, and on
	// a wallet that votes nonstop the vote/stake-fee/split flood pushed real
	// payments past the scan horizon — the Info page's Regular preview
	// "lost" its third row minutes after it showed (owner report,
	// 2026-07-22). Each case below is a strict superset of the refine's
	// true-set; stored IsStakeFee/IsSplit are written at decode, and rows
	// saved by pre-field builds read back false — such legacy rows stay in
	// the superset and are re-excluded by the refine, costing only scan time
	// until a rescan re-saves them.
	case utils.TxFilterRegularList:
		query = db.walletDataDB.Select(
			q.Or(
				q.Eq(utils.TypeFilter, txhelper.TxTypeRegular),
				q.Eq(utils.TypeFilter, txhelper.TxTypeMixed),
			),
			q.Eq(utils.StakeFeeFilter, false),
		)
	case utils.TxFilterRegularNoSplit:
		query = db.walletDataDB.Select(
			q.Or(
				q.Eq(utils.TypeFilter, txhelper.TxTypeRegular),
				q.Eq(utils.TypeFilter, txhelper.TxTypeMixed),
			),
			q.Eq(utils.StakeFeeFilter, false),
			q.Eq(utils.SplitFilter, false),
		)
	case utils.TxFilterSplit:
		// Splits are Regular-typed by consensus; membership (all ins/outs on
		// the default account) is the refine's job.
		query = db.walletDataDB.Select(
			q.Eq(utils.TypeFilter, txhelper.TxTypeRegular),
		)
	case utils.TxFilterStakingList:
		// Tickets plus splits; splits are Regular/Mixed-typed.
		query = db.walletDataDB.Select(
			q.Or(
				q.Eq(utils.TypeFilter, txhelper.TxTypeTicketPurchase),
				q.Eq(utils.TypeFilter, txhelper.TxTypeRegular),
				q.Eq(utils.TypeFilter, txhelper.TxTypeMixed),
			),
		)
	case utils.TxFilterStakingNoSplit, utils.TxFilterTicketVoted:
		query = db.walletDataDB.Select(
			q.Eq(utils.TypeFilter, txhelper.TxTypeTicketPurchase),
		)
	case utils.TxFilterStakeFee:
		query = db.walletDataDB.Select(
			q.Eq(utils.StakeFeeFilter, true),
		)
	case utils.TxFilterRewardList:
		query = db.walletDataDB.Select(
			q.Or(
				q.Eq(utils.TypeFilter, txhelper.TxTypeCoinBase),
				q.Eq(utils.TypeFilter, txhelper.TxTypeVote),
				q.Eq(utils.StakeFeeFilter, true),
			),
		)
	case utils.TxFilterRewardPoW:
		query = db.walletDataDB.Select(
			q.Or(
				q.Eq(utils.TypeFilter, txhelper.TxTypeCoinBase),
				q.Eq(utils.StakeFeeFilter, true),
			),
		)
	case utils.TxFilterRewardPoS:
		query = db.walletDataDB.Select(
			q.Or(
				q.Eq(utils.TypeFilter, txhelper.TxTypeVote),
				q.Eq(utils.StakeFeeFilter, true),
			),
		)
	case utils.TxFilterMissed:
		// Not detectable over SPV — the refine always returns false; match
		// nothing at the query layer either instead of paging everything.
		// (Type is a string field; this sentinel value can never be stored.)
		query = db.walletDataDB.Select(q.Eq(utils.TypeFilter, "\x00missed-none"))

	case utils.TxFilterAll:
		query = db.walletDataDB.Select(
			q.Or(
				q.Eq(utils.TypeFilter, txhelper.TxTypeRegular),
				q.Eq(utils.TypeFilter, txhelper.TxTypeMixed),
				q.Eq(utils.TypeFilter, txhelper.TxTypeCoinBase),
			),
		)
	default:
		query = db.walletDataDB.Select(
			q.True(),
		)
	}

	return
}
