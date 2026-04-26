package dcr

// maxVARAtoms is the maximum number of VAR atoms (21M VAR * 1e8 atoms/VAR).
// Mirrors what dcrutil.MaxAmount used to provide before monetarium-node dropped
// it. Replace with a chaincfg-derived value when MaxSupply is exposed per
// CoinType.
const maxVARAtoms int64 = 21e6 * 1e8

// Stubs for account-mixer methods removed in v1 (CoinShuffle++/CSPP not part of
// the Monetarium MVP). Re-introduce when mixer support is re-enabled.

func (asset *Asset) MixedAccountNumber() int32   { return -1 }
func (asset *Asset) UnmixedAccountNumber() int32 { return -1 }

func (asset *Asset) IsAccountMixerActive() bool          { return false }
func (asset *Asset) StopAccountMixer() error             { return nil }
func (asset *Asset) AccountMixerMixChange() bool         { return false }
func (asset *Asset) accountHasMixableOutput(_ int32) bool { return false }

// VSPTicketInfo always returns nil — VSP support was removed in v1.
func (asset *Asset) VSPTicketInfo(_ string) (*VSPTicketInfo, error) { return nil, nil }

// TicketMaturity is the chain-level ticket maturity for the network the wallet
// is on. The original implementation returned chainParams.TicketMaturity; with
// staking removed in v1 we simply return zero so call-sites compile.
func (asset *Asset) TicketMaturity() int32 { return 0 }

// TicketExpiry is the chain-level ticket expiry. See TicketMaturity above.
func (asset *Asset) TicketExpiry() int32 { return 0 }

// AutoTicketsBuyerConfig returns the empty default; auto ticket buying is off
// in v1.
func (asset *Asset) AutoTicketsBuyerConfig() *TicketBuyerConfig {
	return &TicketBuyerConfig{}
}

// AccountMixerConfigIsSet always reports false.
func (asset *Asset) AccountMixerConfigIsSet() bool { return false }

// AddAccountMixerNotificationListener is a no-op; mixer was removed in v1.
func (asset *Asset) AddAccountMixerNotificationListener(_ *AccountMixerNotificationListener, _ string) error {
	return nil
}

// RemoveAccountMixerNotificationListener is a no-op; mixer was removed in v1.
func (asset *Asset) RemoveAccountMixerNotificationListener(_ string) {}
