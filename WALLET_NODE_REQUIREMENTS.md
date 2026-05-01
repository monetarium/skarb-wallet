# Skarb Wallet — Requirements From the Node Team

This document lists every change the wallet team needs from `monetarium-node`
/ `monetarium-wallet` so Skarb (the desktop wallet for Monetarium) can ship
to end users responsibly. Each item includes the rationale and a severity
classification:

- **BLOCKER** — without this we cannot release publicly without risking
  user funds.
- **IMPORTANT** — release possible, but with documented risk that will
  hurt users sooner or later.
- **NICE-TO-HAVE** — quality-of-life or future-proofing, not a release
  gate.

It also addresses the concerns Wenzel raised in chat about chains that
"already run on mainnet" and "share address schemes with other coins".

---

## 1. Repository hygiene — IMPORTANT

### 1.1. Enable Issues on `monetarium-node` and `monetarium-wallet`

GitHub Issues is currently disabled on both repos. We have a feedback
loop problem: bugs discovered while integrating the wallet have no
permanent home. Today we relay them in chat, they get lost, and the
next person hits the same wall.

**Why**: open issue tracker is the lowest-friction way to keep node ↔
wallet sync. It also signals to external integrators that the project
is alive.

**Effort for node team**: one toggle in repo settings.

### 1.2. Tag a stable release of `monetarium-wallet`

Skarb currently pins `monetarium-wallet@v1.1.0`. Any uncoordinated
breaking change in `master` between tags blows up our build. A simple
release-tag policy (semver, signed tags) lets us freeze a known-good
SDK version and upgrade deliberately.

**Why**: reproducible builds — both for our CI and for users
verifying our binaries.

---

## 2. Bootstrap — BLOCKER for SPV mode

### 2.1. Ship a list of DNS seeds in `chaincfg.MainNetParams.DNSSeeds`

Right now `DNSSeeds` is empty. Skarb runs in SPV mode — that is, it
needs to discover peers itself by resolving DNS records like
`seed.monetarium.io` to a list of node IPs. Without seeds, a fresh
install on a new machine has **no way to connect** unless the user
manually enters a peer IP, which is a non-starter for end users.

**What we need**:
1. 2-3 DNS records pointing to publicly reachable mainnet nodes.
2. The seed nodes themselves running with a stable uptime
   commitment (at minimum: project-team-operated, not random
   community nodes that disappear).
3. Same setup for testnet.

**Effort for node team**: register DNS records, run nodes (most
likely the team already runs nodes — just expose them), edit
`chaincfg/mainnetparams.go` + `testnetparams.go`, tag a release.

**Why this is a release blocker**: a wallet with empty `DNSSeeds`
boots into a "0 peers connected" state forever. Users will think the
wallet is broken and uninstall. This is not a wallet bug — there is
no work-around at the wallet level.

### 2.2. Add chain checkpoints

Decred-style chains validate the entire blockchain from genesis on
first sync. With no checkpoints, a fresh wallet on a slow connection
can take hours just to verify headers. Checkpoints are hard-coded
`(height, hash)` pairs that let the wallet skip validation up to the
checkpoint and start syncing recent blocks fast.

**What we need**: 4-5 checkpoints in `chaincfg.MainNetParams.Checkpoints`,
spread evenly through chain history. Updated every few months.

**Effort for node team**: trivial — pick heights from the explorer,
record their block hashes, commit, tag release.

**Severity**: IMPORTANT, not BLOCKER. We can ship without it; first
sync will be slow.

---

## 3. SLIP-0044 / HD derivation — addressed at the end of this doc

This is the largest disagreement and deserves its own section
(see §6). Short version: we still believe the wallet needs Monetarium
to register and use its own SLIP-0044 coin type and HD-key prefixes
**before** the user base grows beyond the early-adopter window.
Wenzel's counterargument that "BTC/BCH and ETH-family chains share
schemes" is partially correct but does not resolve the underlying
risk; details below.

---

## 4. Multi-coin transaction API — IMPORTANT (but already mostly there)

Monetarium already supports `CoinType` per `TxOut`. The wallet uses
this. Two requests:

### 4.1. Stable signature for `EstimateFeeForCoinType`

We currently call `wallet.FeeForSerializeSizeDualCoin(...)`. If this
signature changes between releases, our fee estimator silently breaks
(returns wrong amount → tx rejected by mempool → user thinks wallet
is broken).

**What we need**: treat all `*FeeForSerializeSize*` and
`AccountBalancesByCoinType` exports as a stable public API. Document
breaking changes in the release notes.

### 4.2. Per-`CoinType` UTXO selection helpers

Currently the wallet has to filter UTXOs by coin type itself. A
helper like `wallet.UTXOsForCoinType(account, coinType)` exposed from
`monetarium-wallet` would let us drop a chunk of fragile filtering
logic.

**Severity**: NICE-TO-HAVE. The wallet team can do this filtering;
it just means less code to maintain on our side if it lives in the
SDK.

---

## 5. PoW limit floor — Wenzel said "doesn't matter, already settled"

### Current state

`MainNetParams.PowLimitBits = 0x1d00ffff` — that's the same minimum
difficulty Bitcoin used at launch. It allows a single CPU to mine a
valid block in seconds.

### Why we raised this

`PowLimitBits` is not the **current** difficulty. It is the
**minimum** the chain will ever accept. Real mining difficulty
adjusts up automatically; that part is fine, no objection.

The danger is a downward shock. Three scenarios:

1. **Hashpower drops 90%** (a single big miner leaves, or coordinated
   attack). Difficulty re-targets down. If real difficulty falls
   below `PowLimitBits`, it stops at the floor — and the floor is
   trivial to mine. An attacker who can produce 1 CPU's worth of
   hashpower (anyone with a laptop) can then produce an
   alternative chain at network speed.

2. **51% attack during a low-activity window** (weekends, holidays).
   Same mechanism. The floor is the maximum re-org cost.

3. **Future fork to Proof-of-Stake or hybrid**. If the chain ever
   migrates, the PoW floor becomes the security baseline of the
   transition window. Too soft a floor = soft transition.

### Wenzel's argument

> "Mining difficulty already settled to current level, initial value
> doesn't matter."

This is correct **for steady-state operation**. It is wrong for the
three scenarios above, all of which are realistic for a young chain
with low hashpower.

### What we ask

Two paths:

**Option A (preferred but breaking)**: hard-fork to raise
`PowLimitBits` to a value comparable to current difficulty / 4 — say
`0x1c0fffff` or higher. This gives a 4× floor of safety while
remaining mineable in emergencies.

**Option B (no fork required)**: keep the current
`PowLimitBits`, document the risk publicly, and treat it as known
debt. The wallet ships with a warning in the README that mainnet
security is bootstrap-grade.

If the node team chooses B, that is a defensible product decision.
We just need it to be **explicit and documented**, not implicit.

**Severity**: IMPORTANT. Not a release blocker if Option B is openly
acknowledged.

---

## 6. SLIP-0044 + HD-key prefixes — the real disagreement

### Current state

`monetarium-node/chaincfg/mainnetparams.go` ships with:

```go
SLIP0044CoinType: 42,                   // = Decred's coin type
HDPrivateKeyID:   [4]byte{0x02, 0xfd, 0xa4, 0xe8},   // "dprv" — Decred private xkey prefix
HDPublicKeyID:    [4]byte{0x02, 0xfd, 0xa9, 0x26},   // "dpub" — Decred public xkey prefix
```

Every BIP-32 extended key exported from a Monetarium wallet starts
with `dprv` / `dpub` — strings that explicitly identify Decred to
any tool that decodes them.

Every hardware wallet (Ledger, Trezor, Coldcard, etc.) that supports
Decred will derive Monetarium addresses **thinking they are Decred
addresses**. Coin type 42 is registered to Decred in
[SLIP-0044](https://github.com/satoshilabs/slips/blob/master/slip-0044.md).

### Wenzel's argument

> "Many blockchains have shared addresses and derivation schemes:
> BTC/BCH, ETH and related chains."

True. Two things to note:

#### 6.1. The BTC/BCH precedent is a cautionary tale, not a model

When BCH forked from BTC in 2017, wallets that imported BTC seeds
into BCH wallets initially derived **the same addresses** — coin
type 0 in both cases. This caused real harm:

- **Replay attacks**: a transaction signed for one chain could be
  rebroadcast on the other, accidentally moving funds. The BCH team
  had to add explicit replay protection (SIGHASH_FORKID).
- **Lost funds**: users sent BTC to BCH addresses and lost them
  (and vice versa) until wallets started displaying CashAddr (a
  different format) for BCH.
- **SLIP-0044 fix**: BCH eventually got its own coin type (145) so
  BIP44-aware wallets would derive distinct addresses. The
  replay-protection problem still exists for non-BIP44 paths.

If Monetarium repeats this pattern, we get the same problems —
**worse**, because Monetarium is multi-asset (VAR + SKAn) and
Decred is single-asset, so users can lose specific SKA tokens
without realizing they pointed a Decred wallet at the wrong chain.

#### 6.2. Ethereum-family is the wrong analogy

Ethereum and EVM clones (BSC, Polygon, Avalanche C-Chain) intentionally
use the same address format because they all share the same VM and
many tokens are bridged between them. Sharing the address space is a
**feature** there. It is also why one wrong-network mistake on MetaMask
loses funds — every chain support team treats this as a known UX
hazard requiring user education.

Decred-family is not in this category. Decred has its own UTXO model,
its own consensus, its own opcode set. Monetarium inherits Decred's
chain structure but adds multi-coin tx semantics. The two chains
are not interchangeable, so re-using Decred's wallet identifiers
implies a compatibility that doesn't exist.

### What this concretely costs the user

A user who:

1. Already has a Decred wallet on a Ledger.
2. Buys VAR / SKA on an exchange and withdraws to "Decred" coin
   type on the same Ledger.

… will see their VAR appear at a Decred-format address that the
Decred wallet "sees" as theirs but cannot spend (Decred RPC won't
recognize VAR UTXOs). The funds are not lost — they're recoverable
if the user knows to switch to a Monetarium-aware wallet — but the
support burden falls entirely on us.

The reverse — a user who imports their Monetarium seed into a Decred
wallet — is worse: they can spend their **DCR balance** with a
seed they thought was Monetarium-only.

### Why "blockchain already works in mainnet" raises the cost rather than lowering it

We understand the reflex: if mainnet is live, breaking changes are
painful. We agree they are painful. Our argument is that they are
**less** painful now than they will be in 3 / 6 / 12 months.

- Today: small user base. Migration = telling the early adopters to
  recover from seed once. They are technically inclined, they will
  do it, and they will respect the project for owning the mistake.
- 6 months from now: 1000+ users including non-technical ones.
  Migration = a forum war, accusations of theft, drop in adoption,
  permanent SEO damage from "Monetarium lost my coins" search
  results.
- Forever (no migration): every Decred-family hardware wallet
  remains a permanent footgun for our users.

### What we ask

**Plan a chain upgrade window** (no need to do it tomorrow) where
the node team:

1. Applies for a new SLIP-0044 coin type. The
   [registration process](https://github.com/satoshilabs/slips/blob/master/slip-0044.md)
   is a PR to satoshilabs/slips with project info; takes days, not
   months.
2. Picks unique 4-byte prefixes for `HDPrivateKeyID` /
   `HDPublicKeyID` (e.g., something that base58-encodes to `mprv`
   / `mpub`).
3. Schedules the activation height. Wallet team coordinates a
   release that, on first launch after the fork height, prompts
   users to back up the seed and re-derive.
4. Updates `chaincfg/mainnetparams.go` and `testnetparams.go` in
   the same upgrade.

**Effort for node team**: ~2-3 days of engineering plus the
governance / communication work for the upgrade.

**Effort for wallet team (us)**: ~1-2 days to add the migration
prompt and update the SLIP-0044 references in `monetarium-wallet`.

**Severity**: BLOCKER for hardware wallet support. IMPORTANT for
correctness with software wallets.

If the node team does not want to do this, we can still ship Skarb,
but we will need to:

- Refuse to load any seed that is also a valid Decred seed without
  an explicit "I understand I might collide with Decred" prompt.
- Display a permanent banner explaining the SLIP-0044 collision in
  Settings → About.
- Document publicly why Skarb does not support Ledger / Trezor.

That is the cost of keeping the current SLIP-44.

---

## 7. Summary table

| # | Requirement | Severity | Effort (node team) |
|---|---|---|---|
| 1.1 | Enable Issues on repos | IMPORTANT | 2 minutes |
| 1.2 | Tag stable wallet SDK releases | IMPORTANT | One-time process setup |
| 2.1 | DNS seeds for mainnet + testnet | **BLOCKER** | 1-2 hours |
| 2.2 | Add chain checkpoints | IMPORTANT | 30 minutes |
| 4.1 | Stable multi-coin tx API | IMPORTANT | Documentation only |
| 4.2 | UTXO-by-coin-type helper | NICE-TO-HAVE | 1 day |
| 5 | PoW floor — fork or document | IMPORTANT | Decision, then 1 day |
| 6 | SLIP-44 + HD prefixes — fork | **BLOCKER** for hardware wallets | 2-3 days + upgrade window |

---

## 8. What we are NOT asking for

To be explicit, since some of our earlier asks may have read as a
laundry list:

- We do NOT need any change to the consensus rules (block time, max
  block size, reward schedule, halving).
- We do NOT need any change to the multi-coin tx semantics.
- We do NOT need ZK or privacy features.
- We do NOT need governance / staking / VSP infrastructure.
- We do NOT block on any of the mobile-wallet items (Skarb v1 is
  desktop only).

The blockers above (DNS seeds, SLIP-44) are the minimum to ship a
trustworthy desktop wallet to end users. Everything else is a
rolling backlog.
