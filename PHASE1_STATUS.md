# Phase 1 Status — Bootstrap fork Cryptopower → Monetarium

Дата: 2026-04-25
Локальний git: ініціалізовано, перший коміт зроблено (`6a57e95`).

## Зроблено

### 1. Структура форку
- Склонували `crypto-power/cryptopower@792f720` (master, 2026).
- Видалили старий `.git`, ініціалізували власну історію.
- Перейменували Go-модуль: `github.com/crypto-power/cryptopower` → `github.com/monetarium/monetarium-cryptopower`.

### 2. Видалені директорії (немає монетарієвих аналогів або не потрібні в v1)
| Шлях | LoC | Причина |
|---|---|---|
| `libwallet/assets/btc/` | ~1500 | BTC support не потрібен |
| `libwallet/assets/ltc/` | ~1500 | LTC support не потрібен |
| `libwallet/internal/loader/btc/` | — | те саме |
| `libwallet/internal/loader/ltc/` | — | те саме |
| `libwallet/internal/politeia/` | ~6 файлів | Politeia governance — поза v1 |
| `libwallet/instantswap/` | — | Кросс-чейн обмін — поза v1 |
| `libwallet/ext/` | — | Залежить від dcrdata (не зрозуміло, що в monetarium-explorer); поза v1 |
| `dexc/` | 2 файли | DCRDEX atomic swap — поза v1 |
| `ui/page/dcrdex/` | 7 файлів | UI DCRDEX |
| `ui/page/governance/` | 11 файлів | UI Politeia |
| `ui/page/exchange/` | — | UI instantswap |

Видалені glue-файли: `libwallet/{btc,ltc,dex_interface,dex_wallets_loader,instantswap,politeia}.go`, `libwallet/assets/wallet/walletdata/{btc,ltc}_db.go`.

### 3. Перенаправлені імпорти (sed по всіх .go файлах)
- `decred.org/dcrwallet/v4` → `github.com/monetarium/monetarium-wallet`
- `github.com/decred/dcrd/<pkg>/v<N>` → `github.com/monetarium/monetarium-node/<pkg>` (видалили версійний суфікс — у monetarium-node sub-modules без `/vN`)
- `github.com/decred/dcrdata/v8` → `github.com/monetarium/monetarium-explorer`

### 4. go.mod
- Прибрали записи: `decred.org/dcrwallet`, `decred.org/dcrdex`, `github.com/btcsuite/*`, `github.com/dcrlabs/*`, `github.com/ltcsuite/*`, `github.com/decred/politeia`.
- Додали через `go get`: всі `github.com/monetarium/monetarium-wallet@v1.1.0` та 28 sub-модулів `monetarium-node/*@v1.1.0`.
- Bumped `go 1.22` → `go 1.23` (вимога monetarium).
- Залишені декредівські залежності (свідомо — їх використовує сам `monetarium-wallet`): `decred/slog`, `decred/vspd/{client,types}`, `decred/base58`, `decred/dcrtime`, `decred/go-socks`, `decred.org/cspp/v2`.

---

## Поточний стан компіляції

`go build ./...` ще НЕ проходить. Помилки розподілені на дві групи:

### Група A — посилання на видалені пакети (12 файлів)
Ці файли імпортують пакети, які ми вже видалили (instantswap, ext, exchange, dexc, politeia):

| Файл | Що там |
|---|---|
| `libwallet/assets_manager.go` (1141 LoC) | Центральний реєстр; поля `Assets.BTC`, `Assets.LTC`, `Politeia`, `InstantSwap`, `dexc`; ~50 згадок |
| `libwallet/assets_config.go` (384 LoC) | Конфіги для DEX/instantswap/governance |
| `libwallet/log.go`, `log.go` (топ-рівень) | Subsystem-логери для btc/ltc/dexc/politeia |
| `ui/page/root/home_page.go` (1177 LoC) | Меню навігації — пункти DEX/Exchange/Governance |
| `ui/page/root/overview_page.go` (1489 LoC) | Картки балансів для всіх асетів |
| `ui/page/settings/app_settings_page.go` (889 LoC) | DEX-налаштування, instantswap-налаштування |
| `ui/page/wallet/wallet_settings_page.go` | Wallet-specific DEX settings |
| `ui/page/components/{components.go,order_list.go}` | InstantSwap order rendering |
| `ui/page/send/send_amount.go` (265 LoC) | Спиральний на BTC/LTC unit conversion |
| `ui/load/wallet_utils.go` | Wallet helpers |
| `ui/utils/utils.go` | DEX utility функції |

### Група B — посилання на BTC/LTC ідентифікатори (~25 файлів)
Це константи/типи в shared-коді, які треба чи замінити на DCR-only-варіант, чи просто прибрати:

```
BTCWalletAsset, LTCWalletAsset (utils.AssetType constants)
initializeBTCWalletParameters, initializeLTCWalletParameters (utils/netparams.go)
BTCAddress, LTCAddress (тестова валідація)
btc.Asset / ltc.Asset (тип-світч у shared utils)
```

Найважливіший файл — `libwallet/utils/netparams.go` — саме він повертає chaincfg-параметри для всіх асетів. Треба залишити тільки гілку DCR (Monetarium).

---

## Що залишилось зробити в Phase 1

| Задача | Оцінка |
|---|---|
| Переписати `libwallet/assets_manager.go` — видалити поля BTC/LTC/Politeia/InstantSwap/dexc, залишити тільки DCR (Monetarium) реєстр | 3-5 год |
| Почистити `libwallet/assets_config.go` від DEX/instantswap-конфігів | 1 год |
| Почистити `libwallet/log.go` + топ-рівневий `log.go` (logger subsystems) | 30 хв |
| Почистити `libwallet/utils/netparams.go` — залишити тільки DCR | 1 год |
| Прибрати з `libwallet/assets/wallet/{wallet_shared,wallet_utils}.go` BTC/LTC type-switches | 2 год |
| Перебрати UI: `home_page.go`, `overview_page.go`, `settings/app_settings_page.go`, `wallet_settings_page.go`, `send_amount.go`, `wallet_setup_page.go`, `wallet_list.go`, `accounts_page.go`, `receive_page.go`, `theme.go` — видалити меню-елементи, type-switches, asset-specific UI | 6-10 год |
| Прибрати з `ui/page/components/` залишки instantswap | 1-2 год |
| Запустити `go build ./...`, ловити помилки, латати | 4-8 год |
| **Разом** | **~3-4 робочі дні** |

Після цього Phase 1 буде завершено: десктопна збірка, тільки VAR (моно-актив), без UI-перевороту під мульти-валюту.

## Відкриті питання

1. **Чи використовує monetarium-wallet API якийсь додатковий пакет, якого не було в dcrwallet?** — після того, як виправимо Групу A/B, можуть випливти помилки на рівні методів (нові аргументи через CoinType). Це частина Phase 2, але деякі зміни вилізуть тут.
2. **monetarium-explorer** — чи має ті самі експортовані функції, що `dcrdata/v8/{api/types,db/dbtypes,txhelpers}`. Поки видалили `libwallet/ext`, де було основне використання, але `libwallet/assets/dcr/decodetx.go` ще використовує цей пакет.
3. **dcrtime** — depended-upon але не зрозуміло, для чого; перевірити, чи можна викинути.

## Команди для відтворення / продовження

```bash
cd /Users/eldar/Documents/GitHub/wallet/monetarium-cryptopower
git log --oneline                  # одна точка комміту
GOFLAGS="-mod=mod" go build ./...  # подивитися помилки
git status                         # подивитися робочу копію
```
