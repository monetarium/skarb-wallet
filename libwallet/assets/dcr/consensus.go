package dcr

import (
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
// Setting a vote choice writes the preference into the wallet database only
// (SetAgendaChoices); it is applied whenever this wallet's ticket votes are
// cast by an RPC-connected voting setup. When monetarium-vsp exists, the
// choice must ALSO be pushed to the VSP for every VSP-managed ticket — that
// half is deliberately absent here (no VSP infrastructure yet) and is part
// of the planned monetarium-vsp integration.

// minListedVoteVersion is the lowest stake (vote) version AllVoteAgendas
// lists. Everything below it is the ancestor chain's deployment history —
// hidden entirely, not just marked historical.
const minListedVoteVersion = 11

// AgendaStatusType labels an agenda's lifecycle stage as derivable locally
// from its deployment time window.
type AgendaStatusType string

const (
	// AgendaStatusUpcoming — the deployment's voting window hasn't opened.
	AgendaStatusUpcoming AgendaStatusType = "upcoming"
	// AgendaStatusInProgress — now is inside the deployment's time window.
	AgendaStatusInProgress AgendaStatusType = "in progress"
	// AgendaStatusEnded — the deployment's window has expired. Whether it
	// locked in or failed is not locally derivable (needs a chain index).
	AgendaStatusEnded AgendaStatusType = "ended"
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

			status := AgendaStatusInProgress
			switch {
			case now < int64(d.StartTime):
				status = AgendaStatusUpcoming
			case now >= int64(d.ExpireTime):
				status = AgendaStatusEnded
			}
			// The wall-clock window is only the OUTER deadline — on chain a
			// vote concludes as soon as the agenda locks in and activates,
			// which on this network happened months before the window
			// closes. The explorer (which has a chain index) shows such an
			// agenda as Finished; without this override the page claimed
			// "in progress" for a long-decided vote (owner report,
			// 2026-07-22).
			if status == AgendaStatusInProgress && asset.agendaConcludedOnChain(d.Vote.Id) {
				status = AgendaStatusEnded
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

// agendaConcludedOnChain reports whether an agenda's outcome is already
// visible on chain even though its wall-clock window is still open. The
// wallet keeps no per-interval vote tally (that needs a chain index à la
// dcrdata), so this recognizes the one agenda family whose outcome has a
// direct local witness: "activateskaN" activated iff the SKA-N coin type is
// live — protocol-active in chainparams AND past its emission height at the
// wallet's best block (EmittedCoinTypes). Emission is configured strictly
// after the best-case activation height (see the SKACoinConfig comments in
// chainparams), so a live coin implies the vote finished. Agendas outside
// that family keep their time-window status until a real chain index exists
// (the monetarium-vsp / explorer-API era).
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
// Local-only for now: the choice lands in the wallet database and is honored
// wherever this wallet's votes are actually cast (an RPC voting-wallet
// deployment). Pushing the updated choice to a VSP for VSP-managed tickets
// is the monetarium-vsp integration's job once that service exists.
func (asset *Asset) SetVoteChoice(agendaID, choiceID, hash string) error {
	var ticketHash *chainhash.Hash
	if hash != "" {
		h, err := chainhash.NewHashFromStr(hash)
		if err != nil {
			return fmt.Errorf("invalid hash: %w", err)
		}
		ticketHash = h
	}

	ctx, _ := asset.ShutdownContextWithCancel()
	_, err := asset.Internal().DCR.SetAgendaChoices(ctx, ticketHash,
		map[string]string{agendaID: strings.ToLower(choiceID)})
	return err
}
