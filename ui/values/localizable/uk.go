package localizable

const UKRAINIAN = "uk"

// Ukrainian translation. Covers the most user-visible strings (navigation,
// send/receive/transactions, balances, settings, common errors). Missing keys
// fall back to the English file via values.String's per-language fallback.
const UK = `
"appName" = "Monetarium"
"appTitle" = "Monetarium (%s)"
"appWallet" = "Monetarium Wallet"
"welcomeNote" = "Ласкаво просимо до Monetarium Wallet."

// Navigation
"home" = "Головна"
"overview" = "Огляд"
"send" = "Надіслати"
"receive" = "Отримати"
"transactions" = "Транзакції"
"settings" = "Налаштування"
"info" = "Інформація"
"accounts" = "Акаунти"
"account" = "Акаунт"
"wallets" = "Гаманці"
"wallet" = "Гаманець"
"createNewWallet" = "Створити новий гаманець"
"restoreExistingWallet" = "Відновити наявний гаманець"
"importWallet" = "Імпортувати гаманець"
"selectWallet" = "Оберіть гаманець"

// Common UI
"all" = "Усі"
"cancel" = "Скасувати"
"save" = "Зберегти"
"confirm" = "Підтвердити"
"continue" = "Продовжити"
"next" = "Далі"
"back" = "Назад"
"done" = "Готово"
"ok" = "Гаразд"
"yes" = "Так"
"no" = "Ні"
"close" = "Закрити"
"delete" = "Видалити"
"edit" = "Редагувати"
"copy" = "Копіювати"
"copied" = "Скопійовано"
"loading" = "Завантаження…"
"search" = "Пошук"
"filter" = "Фільтр"

// Balances
"balance" = "Баланс"
"totalBalance" = "Загальний баланс"
"labelSpendable" = "Доступний"
"unconfirmed" = "Непідтверджено"
"locked" = "Заблоковано"
"watchOnly" = "Лише перегляд"

// Send page
"amount" = "Сума"
"address" = "Адреса"
"destinationAddress" = "Адреса отримувача"
"sourceAccount" = "Акаунт-джерело"
"sourceWallet" = "Гаманець-джерело"
"fee" = "Комісія"
"feeRate" = "Ставка комісії"
"totalCost" = "Загальна сума"
"balanceAfterSend" = "Баланс після відправлення"
"sendMax" = "Надіслати все"
"signAndSend" = "Підписати та надіслати"
"addRecipient" = "Додати отримувача"
"removeRecipient" = "Прибрати отримувача"
"invalidAmount" = "Некоректна сума"
"invalidAddress" = "Некоректна адреса"
"insufficientFund" = "Недостатньо коштів"

// Receive page
"yourAddress" = "Ваша адреса"
"generateNewAddress" = "Згенерувати нову адресу"
"warningWatchWallet" = "Увага: це гаманець лише для перегляду — отримання можливе, відправлення ні."

// Transactions
"recentTransactions" = "Останні транзакції"
"noTransactions" = "Транзакцій ще немає"
"transactionDetails" = "Деталі транзакції"
"sent" = "Надіслано"
"received" = "Отримано"
"transferred" = "Переказано"
"complete" = "Завершено"
"unconfirmedTx" = "Непідтверджено"
"newest" = "Найновіші"
"oldest" = "Найстаріші"

// Settings
"general" = "Загальні"
"language" = "Мова"
"english" = "English"
"ukrainian" = "Українська"
"french" = "French"
"spanish" = "Spanish"
"chinese" = "Chinese"
"darkMode" = "Темна тема"
"currency" = "Валюта"
"currencyConversion" = "Конвертація валют"
"about" = "Про програму"
"version" = "Версія"
"security" = "Безпека"
"changeSpendingPassword" = "Змінити пароль на витрачання"
"backupSeed" = "Резервна копія сід-фрази"
"viewSeed" = "Переглянути сід-фразу"
"verifySeed" = "Перевірити сід-фразу"

// Sync
"connecting" = "Підключення…"
"syncing" = "Синхронізація…"
"synced" = "Синхронізовано"
"notSynced" = "Не синхронізовано"
"startSync" = "Почати синхронізацію"
"stopSync" = "Зупинити синхронізацію"
"online" = "Онлайн, "
"offline" = "Офлайн, "
"connectedToPeers" = "Підключено до пірів: %d"

// Onboarding
"selectLanguage" = "Оберіть мову"
"selectNetwork" = "Оберіть мережу"
"mainnet" = "Mainnet"
"testnet" = "Testnet"
"createWallet" = "Створити гаманець"
"walletName" = "Назва гаманця"
"setSpendingPassword" = "Встановіть пароль на витрачання"
"confirmPassword" = "Підтвердіть пароль"
"passwordMismatch" = "Паролі не співпадають"
"passwordTooShort" = "Пароль закороткий"

// Errors
"error" = "Помилка"
"warning" = "Увага"
"unknown" = "Невідомо"
"failed" = "Не вдалося"
"tryAgain" = "Спробувати ще раз"
`
