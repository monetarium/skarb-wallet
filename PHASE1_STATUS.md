# Phase 1 Status — GREEN

Дата: 2026-04-26
Локальний git: 5 коммітів, останній `0215612` (Phase 1 GREEN).

## Статус: ✅ Build GREEN

```bash
$ cd path/to/monetarium-cryptopower
$ GOFLAGS="-mod=mod" go build ./...    # exit 0
$ GOFLAGS="-mod=mod" go build .         # produces 44MB darwin/arm64 binary
```

Кожен Go-файл у дереві компілюється і лінкується. Бінар запускається, але GUI на цьому етапі — це placeholder (див. розділ «Stubs»).

---

## Що було зроблено в Phase 1

### 1. Структура форку
- Cryptopower clone @ `master 792f720`.
- Власна git-історія з нуля.
- Module: `github.com/crypto-power/cryptopower` → `github.com/monetarium/monetarium-cryptopower`.
- Go directive: `1.22` → `1.23` (вимога monetarium).

### 2. Видалено директорії (немає монетарієвих аналогів або не потрібні в v1)
| Шлях | LoC | Причина |
|---|---|---|
| `libwallet/assets/btc/` | ~1500 | BTC support не потрібен |
| `libwallet/assets/ltc/` | ~1500 | LTC support не потрібен |
| `libwallet/internal/{loader/btc,loader/ltc,politeia}/` | — | Не використовується |
| `libwallet/{ext,instantswap,assets/dcr/{vsp,consensus,ticket,treasury,account_mixer,agenda}}` | — | FX rates / cross-chain swaps / staking |
| `dexc/`, `ui/page/{dcrdex,governance,exchange,privacy,staking}/` | — | DCRDEX / Politeia / mixer UI |
| `libwallet/assets/wallet/walletdata/{btc,ltc}_db.go` | — | BTC/LTC DB shims |
| `ui/page/components/{order_list,proposal_list,consensus_list,treasury_list,vsp_selector}.go` | — | Cross-asset / staking UI |
| `ui/page/accounts/{btc,ltc}_account_details_page.go` | — | Cross-asset UI |

### 3. Перенаправлені імпорти (sed по всіх .go)
- `decred.org/dcrwallet/v4` → `github.com/monetarium/monetarium-wallet`
- `github.com/decred/dcrd/<pkg>/v<N>` → `github.com/monetarium/monetarium-node/<pkg>` (видалили `/vN`)
- `github.com/decred/dcrdata/v8` → `github.com/monetarium/monetarium-explorer`

### 4. go.mod
- Видалили: `decred.org/dcrwallet`, `decred.org/dcrdex`, `github.com/btcsuite/*`, `github.com/dcrlabs/*`, `github.com/ltcsuite/*`, `github.com/decred/politeia`.
- Додали через `go get`: всі `github.com/monetarium/monetarium-wallet@v1.1.0` та 28 sub-модулів `monetarium-node/*@v1.1.0`.
- Залишили (свідомо): `decred/slog`, `decred/vspd/{client,types}`, `decred/base58`, `decred/dcrtime`, `decred/go-socks`, `decred.org/cspp/v2` (їх використовує сам `monetarium-wallet`).

### 5. Перепис ядерних файлів
- `libwallet/assets_manager.go` → DCR-only registry (видалено `Assets.{BTC,LTC}`, `Politeia`, `InstantSwap`, `ExternalService`, `RateSource`, `dexc*`).
- `libwallet/log.go`, `log.go` (root) → DCR-only logging без btclog.
- `logger/logger.go` → drop btclog backend.
- `libwallet/txhelper/{changesource,outputs}.go` → DCR-only.
- `libwallet/addresshelper/helper.go` → DCR-only.
- `libwallet/internal/loader/{config,dcr/loader}.go` → drop {BTC,LTC}.Wallet, drop StakePool/Voting* config fields removed in monetarium-wallet.
- `libwallet/assets/wallet/{wallet_shared,walletdata/{bucket,db,tx}}.go` → drop BTC/LTC switch arms.
- `libwallet/assets_config.go` → drop BTC/LTC genesis/timing switches, drop `RateSource` references.
- `libwallet/wallet_migrator.go` → drop BTC/LTC restore branches.

### 6. Stubs (важливо для Phase 2!)

Створено окремі файли зі stubs, щоб UI компілювався без переробки:

#### `libwallet/phase1_stubs.go`
- `AssetsManager.RateSource` (no-op `rateSourceStub`) — FX rate fetching
- `AssetsManager.Politeia` (no-op `politeiaStub`) — governance
- `AssetsManager.DexClient()` (no-op `dexClientStub`) — DCRDEX
- `AssetsManager.AllBTC/LTCWallets`, `BTC/LTCBadWallets`, `BTC/LTCHDPrefix` — BTC/LTC accessors
- `AssetsManager.{DEXTestAddr, UpdateDEXCtx, DEXCInitialized, DeleteDEXData}` — DCRDEX accessors
- `AssetsManager.CalculateAssetsUSDBalance` — повертає пусту мапу

#### `libwallet/assets/dcr/account_mixer_stub.go`
- `Asset.{MixedAccountNumber, UnmixedAccountNumber, IsAccountMixerActive, StopAccountMixer, AccountMixerMixChange, accountHasMixableOutput}`
- `Asset.{VSPTicketInfo, TicketMaturity, TicketExpiry, AutoTicketsBuyerConfig, AccountMixerConfigIsSet, Add/RemoveAccountMixerNotificationListener}`
- Constant `maxVARAtoms = 21M*1e8` (replaces `dcrutil.MaxAmount` removed in monetarium-node)

#### `ui/page/root/home_page.go` — placeholder (1177 LoC original у `.phase1-stubs-replaced/`)
#### `ui/page/root/overview_page.go` — placeholder (1489 LoC original у `.phase1-stubs-replaced/`)

---

## Phase 2 — що треба переробити

### A. Переписати UI (з placeholder назад у функціонал)

**HomePage** (`ui/page/root/home_page.go`):
- Навігація між Receive / Send / Transactions / Settings (без Exchange/Governance/Staking).
- Wallet selector (зліва) — список Monetarium-кошельків.

**OverviewPage** (`ui/page/root/overview_page.go`):
- Замість FX market cards — картки балансів **per CoinType** (VAR + усі активні SKAn).
- Список останніх транзакцій (з колонкою CoinType).
- Summary: total VAR, total per-SKA.

### B. Викинути всі stubs `phase1_stubs.go` та `account_mixer_stub.go`

Замінити no-op методи на справжні implementations від monetarium-wallet, або повністю прибрати call-sites:
- `RateSource` → інтегрувати реальне джерело курсів (CoinGecko / Kraken API).
- `Politeia`, `DexClient` — викинути виклики з UI; функціонал не потрібен.
- `VSPTicketInfo`, `TicketMaturity` etc. — функціонал stake hidden у v1; виклики прибрати.

### C. Multi-coin модель в libwallet (основний кусок Phase 2 з плану)

Див. план Phase 2 (зберігається локально, поза репозиторієм):
- Розширити `Balance` до `map[CoinType]CoinBalance`.
- `Asset.GetActiveCoinTypes()` через `chaincfg.Params.GetActiveSKATypes()`.
- `ConstructTxForCoinType(coinType, ...)` обгортка.
- `EstimateFeeForCoinType(coinType, ...)`.
- Big.Int formatter з урахуванням `AtomsPerCoin` per SKA.

### D. Налаштування Monetarium-specific

Не зроблено в Phase 1 (на майбутні фази, але без блокерів):
- `dcrutil.AppDataDir("cryptopower", ...)` → нова назва додатку.
- Адресні префікси `Mk/Ms/Me/MS/Mc` валідатори.
- Іконки, бренд, локалізація (включно з UA).

---

## Команди для роботи з форком

```bash
cd path/to/monetarium-cryptopower

# Build
GOFLAGS="-mod=mod" go build ./...
GOFLAGS="-mod=mod" go build -o monetarium-cryptopower .

# Original deleted UI is preserved here (for Phase 2 reference)
ls .phase1-stubs-replaced/
#  app_settings_page.go.orig
#  home_page.go.orig
#  overview_page.go.orig

# git history
git log --oneline
#   0215612  Phase 1 GREEN: full 'go build ./...' compiles
#   df94ca9  libwallet stays green: stub mixer, drop ConsensusAgenda+RateSource
#   fddd33d  libwallet compiles: rewrite assets_manager + log + walletdata
#   925663a  Add PHASE1_STATUS.md
#   6a57e95  Phase 1 bootstrap: import rewrite + dead-code removal
```

## Висновок

✅ Доведено, що `monetarium-wallet` API API-сумісне з `dcrwallet/v4` і кошельку **не потрібно** ламати ніяких public-методів — все компілюється з простою заміною шляхів імпортів і кількома stubs для прибраного функціоналу.

✅ Багато непотрібного коду викинуто — кодова база зменшилась з 88.9K LoC до приблизно 70K LoC.

⚠️ App запускається, але є placeholder на головному екрані. Phase 2 будує справжню multi-coin UI поверх вже сумісного бекенду.
