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
