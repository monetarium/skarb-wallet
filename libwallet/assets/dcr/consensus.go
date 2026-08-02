package dcr

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/monetarium/monetarium-node/chaincfg"
	"github.com/monetarium/monetarium-node/chaincfg/chainhash"
	w "github.com/monetarium/monetarium-wallet/wallet"
)

// This file provides the consensus-agenda (on-chain governance) surface for
// the UI: listing every consensus deployment the network defines, reading the
// wallet's saved voting preferences, and updating a preference.
//
// Unlike the upstream Cryptopower implementation this is fully LOCAL:
// deployment metadata comes from chainParams.Deployments and the agenda
// status is derived from the deployment's own time window — there is no
// dcrdata HTTP dependency (Monetarium runs no dcrdata instance, and the
// Decred one obviously doesn't index this chain). The trade-off: a finished
// agenda is reported as plain "ended" — telling apart locked-in vs failed
// needs a chain index the wallet doesn't have.
//
// Setting a vote choice writes the preference into the wallet database
// (SetAgendaChoices) AND pushes it to the VSP of every affected VSP-managed
// ticket. The database copy is what an RPC-connected voting setup reads; a
// ticket held by a VSP votes from the copy the VSP keeps, so both have to be
// updated or the new choice is never cast.

// minListedVoteVersion is the lowest stake (vote) version AllVoteAgendas
// lists. Everything below it is the ancestor chain's deployment history —
// hidden entirely, not just marked historical.
const minListedVoteVersion = 11

// AgendaStatusType labels an agenda's lifecycle stage using dcrd's
// getvoteinfo vocabulary (defined / started / lockedin / active / failed) —
// owner decision 2026-07-24: the wallet should speak the same status
// language as the node and the explorer. The wallet is SPV and cannot run
// getvoteinfo itself, so each status is derived from the best local signal
// (see the derivation in AllVoteAgendas); "lockedin" needs per-interval vote
// tallies no local signal provides and is never emitted today — it becomes
// reachable when a chain-index source exists (explorer API / VSP era).
type AgendaStatusType string

const (
	// AgendaStatusDefined — the agenda exists but its voting window hasn't
	// opened yet (getvoteinfo: "defined").
	AgendaStatusDefined AgendaStatusType = "defined"
	// AgendaStatusStarted — the voting window is open and no outcome is
	// visible on chain yet (getvoteinfo: "started").
	AgendaStatusStarted AgendaStatusType = "started"
	// AgendaStatusLockedIn — the vote passed and awaits activation
	// (getvoteinfo: "lockedin"). Not locally derivable over SPV; reserved
	// for a future chain-index source.
	AgendaStatusLockedIn AgendaStatusType = "lockedin"
	// AgendaStatusActive — the voted rule is activated and enforced
	// (getvoteinfo: "active").
	AgendaStatusActive AgendaStatusType = "active"
	// AgendaStatusFailed — the voting window closed without the agenda
	// locking in (getvoteinfo: "failed").
	AgendaStatusFailed AgendaStatusType = "failed"
)

// Agenda is one consensus deployment presented for the governance UI.
type Agenda struct {
	AgendaID    string
	Description string
	Mask        uint32
	Choices     []chaincfg.Choice
	// VotingPreference is this wallet's saved choice ID ("yes"/"no"/
	// "abstain"), filled from AgendaChoices for current-version agendas;
	// empty when the wallet has no saved preference (defaults to abstain).
	VotingPreference string
	StartTime        int64
	ExpireTime       int64
	Status           AgendaStatusType
	// VoteVersion is the stake version this deployment belongs to.
	VoteVersion uint32
	// IsCurrent marks deployments of the wallet's CURRENT vote version —
	// the only ones whose preference can be changed (SetAgendaChoices
	// rejects agenda IDs outside the current version) and the only ones
	// tickets actually vote on.
	IsCurrent bool
	// ForcedChoiceID is non-empty for deployments whose outcome is
	// consensus-forced (the choice is hardcoded, voting is a formality).
	ForcedChoiceID string
}

// AllVoteAgendas lists every consensus deployment of every stake version for
// this network, newest-first when newestFirst is set. The wallet's saved
// preferences are merged in for current-version agendas.
func (asset *Asset) AllVoteAgendas(newestFirst bool) ([]*Agenda, error) {
	chainParams := asset.chainParams
	if chainParams.Deployments == nil {
		return nil, nil // no agendas on this network
	}

	currentVersion, _ := w.CurrentAgendas(chainParams)

	// The wallet's saved preferences apply to current-version agendas only;
	// a failure here (e.g. network defines no current agendas) must not
	// hide the historical list, so it degrades to "no preferences".
	preferences, err := asset.AgendaChoices("")
	if err != nil {
		log.Warnf("AllVoteAgendas: reading saved vote choices failed: %v", err)
		preferences = nil
	}

	now := time.Now().Unix()
	agendas := make([]*Agenda, 0, len(chainParams.Deployments))
	for version, deployments := range chainParams.Deployments {
		// The chain parameters inherit the ancestor chain's (Decred's)
		// deployment history — stake versions 4–10 are that legacy noise
		// (SDiffAlgorithm through the DCP-10 era) and none of it is
		// actionable or even informative on this network. Monetarium's own
		// consensus work starts at vote version 11 (VoteIDActivateSKA2);
		// list nothing older (owner decision, 2026-07-20).
		if version < minListedVoteVersion {
			continue
		}
		for i := range deployments {
			d := &deployments[i]

			// getvoteinfo-vocabulary status from local signals, checked in
			// priority order:
			//   defined — the window hasn't opened;
			//   active  — the rule's activation has a direct on-chain
			//             witness (SKA-N live for activateskaN), which wins
			//             even after the window closes;
			//   failed  — the window closed with no activation witness: an
			//             agenda that never locked in by expiry failed by
			//             definition;
			//   started — everything else: window open, outcome not yet
			//             visible. "lockedin" (passed, awaiting activation)
			//             needs per-interval tallies the SPV wallet doesn't
			//             have and is never emitted here.
			status := AgendaStatusStarted
			switch {
			case now < int64(d.StartTime):
				status = AgendaStatusDefined
			case asset.agendaConcludedOnChain(d.Vote.Id):
				status = AgendaStatusActive
			case now >= int64(d.ExpireTime):
				status = AgendaStatusFailed
			}

			agenda := &Agenda{
				AgendaID:       d.Vote.Id,
				Description:    d.Vote.Description,
				Mask:           uint32(d.Vote.Mask),
				Choices:        d.Vote.Choices,
				StartTime:      int64(d.StartTime),
				ExpireTime:     int64(d.ExpireTime),
				Status:         status,
				VoteVersion:    version,
				IsCurrent:      version == currentVersion,
				ForcedChoiceID: d.ForcedChoiceID,
			}
			if agenda.IsCurrent {
				agenda.VotingPreference = preferences[agenda.AgendaID]
			}
			agendas = append(agendas, agenda)
		}
	}

	sort.Slice(agendas, func(i, j int) bool {
		if agendas[i].StartTime != agendas[j].StartTime {
			if newestFirst {
				return agendas[i].StartTime > agendas[j].StartTime
			}
			return agendas[i].StartTime < agendas[j].StartTime
		}
		return agendas[i].AgendaID < agendas[j].AgendaID
	})
	return agendas, nil
}

// agendaConcludedOnChain reports whether an agenda's ACTIVATION is already
// visible on chain (the getvoteinfo "active" state), regardless of its
// wall-clock window. The wallet keeps no per-interval vote tally (that needs
// a chain index à la dcrdata), so this recognizes the one agenda family
// whose outcome has a direct local witness: "activateskaN" activated iff the
// SKA-N coin type is live — protocol-active in chainparams AND past its
// emission height at the wallet's best block (EmittedCoinTypes). Emission is
// configured strictly after the best-case activation height (see the
// SKACoinConfig comments in chainparams), so a live coin implies the rule is
// enforced. Agendas outside that family keep their time-window status until
// a real chain index exists (the monetarium-vsp / explorer-API era).
func (asset *Asset) agendaConcludedOnChain(agendaID string) bool {
	numStr, ok := strings.CutPrefix(agendaID, "activateska")
	if !ok {
		return false
	}
	n, err := strconv.Atoi(numStr)
	if err != nil || n <= 0 || n > 255 {
		return false
	}
	for _, ct := range asset.EmittedCoinTypes() {
		if int(ct) == n {
			return true
		}
	}
	return false
}

// AgendaChoices returns the wallet's saved vote preferences for the agendas
// of the CURRENT stake version, keyed by agenda ID. With a non-empty txHash
// the preferences saved for that specific ticket are returned instead.
func (asset *Asset) AgendaChoices(txHash string) (map[string]string, error) {
	if asset.chainParams.Deployments == nil {
		return nil, nil
	}

	var ticketHash *chainhash.Hash
	if txHash != "" {
		hash, err := chainhash.NewHashFromStr(txHash)
		if err != nil {
			return nil, fmt.Errorf("invalid hash: %w", err)
		}
		ticketHash = hash
	}

	ctx, _ := asset.ShutdownContextWithCancel()
	choicesMap, _, err := asset.Internal().DCR.AgendaChoices(ctx, ticketHash)
	if err != nil {
		return nil, err
	}
	return choicesMap, nil
}

// SetVoteChoice saves a voting preference for one agenda of the current
// stake version. With a non-empty ticket hash the preference applies to that
// ticket only; otherwise it becomes the wallet-wide default for all tickets.
//
// The choice is written to the wallet database first, then pushed to the VSP
// of every affected VSP-managed ticket. Both steps matter: the database entry
// is what an RPC voting-wallet deployment reads, while a ticket held by a VSP
// votes from the copy the VSP keeps — without the push the user's new choice
// would silently never be cast.
func (asset *Asset) SetVoteChoice(agendaID, choiceID, hash string) error {
	var ticketHash *chainhash.Hash
	if hash != "" {
		h, err := chainhash.NewHashFromStr(hash)
		if err != nil {
			return fmt.Errorf("invalid hash: %w", err)
		}
		ticketHash = h
	}

	choices := map[string]string{agendaID: strings.ToLower(choiceID)}

	ctx, _ := asset.ShutdownContextWithCancel()
	if _, err := asset.Internal().DCR.SetAgendaChoices(ctx, ticketHash, choices); err != nil {
		return err
	}

	return asset.pushVoteChoicesToVSPs(ctx, ticketHash, choices)
}

// pushVoteChoicesToVSPs updates the vote choices held by the VSP for every
// VSP-managed ticket the change applies to: one ticket when ticketHash is
// set, otherwise every unspent unexpired ticket of this wallet.
//
// Tickets that are not VSP-managed are skipped silently — they vote from the
// wallet database alone, which SetVoteChoice already updated.
func (asset *Asset) pushVoteChoicesToVSPs(ctx context.Context, ticketHash *chainhash.Hash,
	choices map[string]string) error {

	hashes, err := asset.affectedTicketHashes(ticketHash)
	if err != nil {
		return err
	}

	// Collect the VSP-managed tickets first. Solo tickets need no push, and a
	// wallet holding only those must not be told about a VSP failure.
	type vspTicket struct {
		hash   *chainhash.Hash
		ticket *w.VSPTicket
		info   *w.TicketInfo
	}

	var (
		managed []vspTicket
		failed  int
	)
	for _, h := range hashes {
		ticket, err := asset.Internal().DCR.NewVSPTicket(ctx, h)
		if err != nil {
			log.Warnf("vote choice push: cannot load ticket %s: %v", h, err)
			failed++
			continue
		}

		// A ticket with no VSP record is bought solo; nothing to push.
		info, err := ticket.VSPTicketInfo(ctx)
		if err != nil {
			continue
		}

		managed = append(managed, vspTicket{hash: h, ticket: ticket, info: info})
	}

	// Updating a VSP requires signing the request with the ticket's
	// commitment key, so a locked wallet cannot push. Report it rather than
	// leaving the user believing a VSP-managed ticket got the new choice.
	if len(managed) > 0 && asset.IsLocked() {
		return errors.New("vote choice saved locally, but the wallet is locked " +
			"so it could not be sent to the VSP")
	}

	for _, m := range managed {
		h, ticket, vspTicketInfo := m.hash, m.ticket, m.info

		vspClient, err := asset.VSPClient(-1, vspTicketInfo.Host, vspTicketInfo.PubKey)
		if err != nil {
			log.Errorf("vote choice push: cannot reach VSP %s for ticket %s: %v",
				vspTicketInfo.Host, h, err)
			failed++
			continue
		}

		if err := vspClient.SetVoteChoice(ctx, ticket, choices, nil, nil); err != nil {
			log.Errorf("vote choice push: VSP %s rejected the update for ticket %s: %v",
				vspTicketInfo.Host, h, err)
			failed++
			continue
		}

		log.Debugf("vote choice push: VSP %s updated for ticket %s", vspTicketInfo.Host, h)
	}

	if failed > 0 {
		return fmt.Errorf("vote choice saved locally, but %d of %d ticket(s) "+
			"could not be updated at their VSP", failed, len(hashes))
	}

	return nil
}

// affectedTicketHashes returns the tickets a vote-choice change applies to:
// the single named ticket, or every unspent unexpired ticket when the change
// is the wallet-wide default.
func (asset *Asset) affectedTicketHashes(ticketHash *chainhash.Hash) ([]*chainhash.Hash, error) {
	if ticketHash != nil {
		return []*chainhash.Hash{ticketHash}, nil
	}

	tickets, err := asset.UnspentUnexpiredTickets()
	if err != nil {
		return nil, err
	}

	hashes := make([]*chainhash.Hash, 0, len(tickets))
	for _, ticket := range tickets {
		h, err := chainhash.NewHashFromStr(ticket.Hash)
		if err != nil {
			log.Warnf("vote choice push: skipping unparsable ticket hash %q: %v",
				ticket.Hash, err)
			continue
		}
		hashes = append(hashes, h)
	}

	return hashes, nil
}
