package libwallet

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"

	"github.com/asdine/storm"
	"github.com/asdine/storm/q"
	"github.com/monetarium/monetarium-wallet/errors"
	"github.com/monetarium/skarb-wallet/appos"
	"github.com/monetarium/skarb-wallet/libwallet/utils"
	libutils "github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/skarb-wallet/ui/notification"
	bolt "go.etcd.io/bbolt"

	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	"github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
)

// LogFilename is the main app log filename.
const LogFilename = "skarb.log"

// assetIdentifier is used to listen for balance changes of all wallets.
const assetIdentifier = "assets_manager"

const BoltDB = "bdb"        // Bolt db driver
const BadgerDB = "badgerdb" // Badger db driver

// Assets holds all the assets supported by the wallet. After the Monetarium
// fork only DCR (Monetarium) wallets remain.
type Assets struct {
	DCR struct {
		Wallets    map[int]sharedW.Asset
		BadWallets map[int]*sharedW.Wallet
	}
}

// AssetsManager manages the lifecycle of Monetarium wallets.
type AssetsManager struct {
	params *sharedW.InitParams
	Assets *Assets

	shuttingDown chan bool
	cancelFuncs  []context.CancelFunc
	chainsParams utils.ChainsParams

	rateMutex  sync.Mutex
	RateSource rateSourceStub // Phase-1 stub; FX rate fetching removed.
	Politeia   politeiaStub   // Phase-1 stub; governance removed.

	// walletsMu guards the Assets.DCR.Wallets map: wallet create/restore
	// run on background goroutines while the UI (and the per-second
	// screen-awake poll on mobile) iterates the map from the frame
	// goroutine — an unguarded write during iteration is a fatal runtime
	// error.
	walletsMu sync.RWMutex

	toast *notification.Toast

	NeedMigrate bool
}

// initializeAssetsFields validates the network and initializes the manager fields.
func initializeAssetsFields(rootDir, dbDriver, logDir string, netType utils.NetworkType) (*AssetsManager, error) {
	dcrChainParams, err := initializeDCRWalletParameters(netType)
	if err != nil {
		log.Errorf("error initializing DCR parameters: %s", err.Error())
		return nil, errors.Errorf("error initializing DCR parameters: %s", err.Error())
	}

	params := &sharedW.InitParams{
		DbDriver: dbDriver,
		RootDir:  rootDir,
		NetType:  netType,
		LogDir:   logDir,
	}

	mgr := &AssetsManager{
		params: params,
		Assets: new(Assets),
	}

	mgr.Assets.DCR.Wallets = make(map[int]sharedW.Asset)
	mgr.Assets.DCR.BadWallets = make(map[int]*sharedW.Wallet)
	mgr.chainsParams.DCR = dcrChainParams
	return mgr, nil
}

func fileExists(filePath string) bool {
	_, err := os.Stat(filePath)
	if err != nil {
		return false
	}
	return true
}

// NewAssetsManager creates a new AssetsManager instance.
func NewAssetsManager(rootDir, logDir string, netType utils.NetworkType) (*AssetsManager, error) {
	errors.Separator = ":: "
	needMigrate := false
	isMobile := appos.Current().IsMobile()

	dbDriver := BoltDB
	if isMobile {
		dbDriver = BadgerDB
	}

	if fileExists(filepath.Join(rootDir, fmt.Sprintf("%s-%s", string(netType), dbDriver))) {
		rootDir = filepath.Join(rootDir, fmt.Sprintf("%s-%s", string(netType), dbDriver))
	} else if fileExists(filepath.Join(rootDir, string(netType))) {
		dbDriver = BoltDB
		rootDir = filepath.Join(rootDir, string(netType))
		needMigrate = isMobile
	} else {
		rootDir = filepath.Join(rootDir, fmt.Sprintf("%s-%s", string(netType), dbDriver))
	}

	if err := os.MkdirAll(rootDir, utils.UserFilePerm); err != nil {
		return nil, errors.Errorf("failed to create rootDir: %v", err)
	}

	mgr, err := initializeAssetsFields(rootDir, dbDriver, logDir, netType)
	if err != nil {
		return nil, err
	}

	if err := initLogRotator(filepath.Join(rootDir, logFileName)); err != nil {
		return nil, errors.Errorf("failed to init logRotator: %v", err.Error())
	}

	mwDB, err := storm.Open(filepath.Join(rootDir, walletsDbName))
	if err != nil {
		log.Errorf("Error opening wallets database: %s", err.Error())
		if err == bolt.ErrTimeout {
			return nil, errors.E(utils.ErrWalletDatabaseInUse)
		}
		return nil, errors.Errorf("error opening wallets database: %s", err.Error())
	}

	if err = mwDB.Init(&sharedW.Wallet{}); err != nil {
		log.Errorf("Error initializing wallets database: %s", err.Error())
		return nil, err
	}

	mgr.params.DB = mwDB

	mgr.cleanDeletedWallets()

	if err := mgr.prepareExistingWallets(); err != nil {
		return nil, err
	}

	log.Infof("Loaded %d wallets", mgr.LoadedWalletsCount())

	mgr.listenForShutdown()
	mgr.NeedMigrate = needMigrate
	return mgr, nil
}

func (mgr *AssetsManager) RemoveRootDir() error {
	if err := os.RemoveAll(mgr.params.RootDir); err != nil {
		return err
	}
	return os.RemoveAll(mgr.params.LogDir)
}

func (mgr *AssetsManager) RootDir() string     { return mgr.params.RootDir }
func (mgr *AssetsManager) ParamLogDir() string { return mgr.params.LogDir }

func (mgr *AssetsManager) SetToast(toast *notification.Toast) {
	mgr.toast = toast
}

// prepareExistingWallets loads all the valid and bad wallets.
func (mgr *AssetsManager) prepareExistingWallets() error {
	query := mgr.params.DB.Select(q.True()).OrderBy("ID")
	var wallets []*sharedW.Wallet
	if err := query.Find(&wallets); err != nil && err != storm.ErrNotFound {
		return err
	}

	for _, wallet := range wallets {
		wallet.SetNetType(mgr.NetType())

		path := filepath.Join(mgr.params.RootDir, wallet.DataDir())
		log.Infof("loading properties of wallet=%v at location=%v", wallet.Name, path)

		switch wallet.Type {
		case utils.DCRWalletAsset:
			w, err := dcr.LoadExisting(wallet, mgr.params)
			if err != nil {
				mgr.Assets.DCR.BadWallets[wallet.ID] = wallet
				log.Warnf("Ignored dcr wallet load error for wallet %d (%s)", wallet.ID, wallet.Name)
			} else {
				mgr.addWallet(w)
			}
		default:
			mgr.Assets.DCR.BadWallets[wallet.ID] = wallet
		}
	}
	return nil
}

func (mgr *AssetsManager) listenForShutdown() {
	mgr.cancelFuncs = make([]context.CancelFunc, 0)
	mgr.shuttingDown = make(chan bool)
	go func() {
		<-mgr.shuttingDown
		for _, cancel := range mgr.cancelFuncs {
			cancel()
		}
	}()
}

// Shutdown shuts down the assets manager and all its wallets.
func (mgr *AssetsManager) Shutdown() {
	log.Info("Shutting down libwallet")

	mgr.shuttingDown <- true

	for _, wallet := range mgr.AllWallets() {
		wallet.Shutdown()
		wallet.CancelRescan()
	}
	mgr.Assets = new(Assets)

	utils.ShutdownHTTPClients()

	if mgr.params.DB != nil {
		if err := mgr.params.DB.Close(); err != nil {
			log.Errorf("db closed with error: %v", err)
		} else {
			log.Info("db closed successfully")
		}
	}

	if logRotator != nil {
		log.Info("Shutting down log rotator")
		logRotator.Close()
		log.Info("Shutdown log rotator successfully")
	}
}

// NetType returns the network type of the assets manager.
func (mgr *AssetsManager) NetType() utils.NetworkType { return mgr.params.NetType }

// LogDir returns the log directory of the assets manager.
func (mgr *AssetsManager) LogDir() string {
	return filepath.Join(mgr.params.RootDir, logFileName)
}

// DBDriver returns the db driver in use.
func (mgr *AssetsManager) DBDriver() string { return mgr.params.DbDriver }

// OpenWallets opens all wallets in the assets manager.
func (mgr *AssetsManager) OpenWallets(startupPassphrase string) error {
	for _, wallet := range mgr.AllWallets() {
		if wallet.IsSyncing() {
			return errors.New(utils.ErrSyncAlreadyInProgress)
		}
	}

	if err := mgr.VerifyStartupPassphrase(startupPassphrase); err != nil {
		return err
	}

	for _, wallet := range mgr.AllWallets() {
		select {
		case <-mgr.shuttingDown:
			return nil
		default:
			if err := wallet.OpenWallet(); err != nil {
				return err
			}
		}
	}
	return nil
}

// DCRBadWallets returns a map of all bad DCR wallets.
func (mgr *AssetsManager) DCRBadWallets() map[int]*sharedW.Wallet {
	return mgr.Assets.DCR.BadWallets
}

// LoadedWalletsCount returns the number of wallets loaded in the assets manager.
func (mgr *AssetsManager) LoadedWalletsCount() int32 {
	return int32(len(mgr.AllWallets()))
}

// OpenedWalletsCount returns the number of wallets opened in the assets manager.
func (mgr *AssetsManager) OpenedWalletsCount() int32 {
	var count int32
	for _, wallet := range mgr.AllWallets() {
		if wallet.WalletOpened() {
			count++
		}
	}
	return count
}

// PiKeys returns the sanctioned Politeia keys for the current network.
func (mgr *AssetsManager) PiKeys() [][]byte {
	return mgr.chainsParams.DCR.PiKeys
}

// sortWallets returns the watchonly wallets ordered last.
func (mgr *AssetsManager) sortWallets(assetType utils.AssetType) []sharedW.Asset {
	normalWallets := make([]sharedW.Asset, 0)
	watchOnlyWallets := make([]sharedW.Asset, 0)

	var unsortedWallets map[int]sharedW.Asset
	if assetType == utils.DCRWalletAsset {
		unsortedWallets = mgr.Assets.DCR.Wallets
	}

	mgr.walletsMu.RLock()
	for _, wallet := range unsortedWallets {
		if wallet.IsWatchingOnlyWallet() {
			watchOnlyWallets = append(watchOnlyWallets, wallet)
		} else {
			normalWallets = append(normalWallets, wallet)
		}
	}
	mgr.walletsMu.RUnlock()

	sort.Slice(normalWallets, func(i, j int) bool {
		return normalWallets[i].GetWalletID() < normalWallets[j].GetWalletID()
	})
	sort.Slice(watchOnlyWallets, func(i, j int) bool {
		return watchOnlyWallets[i].GetWalletID() < watchOnlyWallets[j].GetWalletID()
	})

	return append(normalWallets, watchOnlyWallets...)
}

// AllDCRWallets returns all DCR wallets in the assets manager.
func (mgr *AssetsManager) AllDCRWallets() []sharedW.Asset {
	return mgr.sortWallets(utils.DCRWalletAsset)
}

// AllWallets returns all wallets in the assets manager.
func (mgr *AssetsManager) AllWallets() []sharedW.Asset {
	return mgr.AllDCRWallets()
}

// DeleteWallet deletes a wallet from the assets manager.
func (mgr *AssetsManager) DeleteWallet(walletID int, privPass string) error {
	wallet := mgr.WalletWithID(walletID)
	if wallet == nil {
		return nil
	}

	if err := wallet.DeleteWallet(privPass); err != nil {
		return err
	}

	if wallet.GetAssetType() == utils.DCRWalletAsset {
		mgr.walletsMu.Lock()
		delete(mgr.Assets.DCR.Wallets, walletID)
		mgr.walletsMu.Unlock()
	}
	return nil
}

// addWallet registers a freshly created/restored wallet under the map
// lock — creation runs on background goroutines while the UI iterates
// the map.
func (mgr *AssetsManager) addWallet(wallet sharedW.Asset) {
	mgr.walletsMu.Lock()
	mgr.Assets.DCR.Wallets[wallet.GetWalletID()] = wallet
	mgr.walletsMu.Unlock()
}

// WalletWithID returns a wallet with the given ID.
func (mgr *AssetsManager) WalletWithID(walletID int) sharedW.Asset {
	mgr.walletsMu.RLock()
	defer mgr.walletsMu.RUnlock()
	if wallet, ok := mgr.Assets.DCR.Wallets[walletID]; ok {
		return wallet
	}
	return nil
}

// AssetWallets returns the wallets for the specified asset type(s).
func (mgr *AssetsManager) AssetWallets(assetTypes ...utils.AssetType) []sharedW.Asset {
	var wallets []sharedW.Asset
	for _, asset := range assetTypes {
		if asset == utils.DCRWalletAsset {
			wallets = append(wallets, mgr.AllDCRWallets()...)
		}
	}
	if len(wallets) == 0 && len(assetTypes) == 0 {
		wallets = mgr.AllWallets()
	}
	return wallets
}

func (mgr *AssetsManager) getbadWallet(walletID int) *sharedW.Wallet {
	if badWallet, ok := mgr.Assets.DCR.BadWallets[walletID]; ok {
		return badWallet
	}
	return nil
}

// DeleteBadWallet deletes a bad wallet from the assets manager.
func (mgr *AssetsManager) DeleteBadWallet(walletID int) error {
	wallet := mgr.getbadWallet(walletID)
	if wallet == nil {
		return errors.New(utils.ErrNotExist)
	}

	log.Info("Deleting bad wallet")

	if err := mgr.params.DB.DeleteStruct(wallet); err != nil {
		return utils.TranslateError(err)
	}

	os.RemoveAll(wallet.DataDir())

	if wallet.GetAssetType() == utils.DCRWalletAsset {
		delete(mgr.Assets.DCR.BadWallets, walletID)
	}
	return nil
}

// DoesWalletNameExist returns true if a wallet with the same name already exists.
func (mgr *AssetsManager) DoesWalletNameExist(walletName string) (bool, error) {
	w := wallet.Wallet{}
	err := mgr.params.DB.One("Name", walletName, &w)
	if err == nil {
		return true, nil
	} else if err != storm.ErrNotFound {
		return false, err
	}
	return false, nil
}

// RootDirFileSizeInBytes returns the total directory size of
// Assets Manager's root directory in bytes.
func (mgr *AssetsManager) RootDirFileSizeInBytes(dataDir string) (int64, error) {
	var size int64
	err := filepath.Walk(dataDir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return err
	})
	return size, err
}

// WalletWithSeed returns the ID of the wallet with the given seed.
func (mgr *AssetsManager) WalletWithSeed(walletType utils.AssetType, seedMnemonic string, wordSeedType sharedW.WordSeedType) (int, error) {
	if walletType == utils.DCRWalletAsset {
		return mgr.DCRWalletWithSeed(seedMnemonic, wordSeedType)
	}
	return -1, utils.ErrAssetUnknown
}

// RestoreWallet restores a wallet from the given seed.
func (mgr *AssetsManager) RestoreWallet(walletType utils.AssetType, walletName, seedMnemonic, privatePassphrase string, privatePassphraseType int32, wordSeedType sharedW.WordSeedType) (sharedW.Asset, error) {
	if walletType == utils.DCRWalletAsset {
		return mgr.RestoreDCRWallet(walletName, seedMnemonic, privatePassphrase, wordSeedType, privatePassphraseType)
	}
	return nil, utils.ErrAssetUnknown
}

// WalletWithXPub returns the ID of the wallet with the given xpub.
func (mgr *AssetsManager) WalletWithXPub(walletType utils.AssetType, xPub string) (int, error) {
	if walletType == utils.DCRWalletAsset {
		return mgr.DCRWalletWithXPub(xPub)
	}
	return -1, utils.ErrAssetUnknown
}

// cleanDeletedWallets removes leftover dirs of deleted wallets.
func (mgr *AssetsManager) cleanDeletedWallets() {
	query := mgr.params.DB.Select(q.True()).OrderBy("ID")
	var wallets []*sharedW.Wallet
	if err := query.Find(&wallets); err != nil && err != storm.ErrNotFound {
		log.Error("Fail to get all wallet to check deleted wallets")
		return
	}

	log.Info("Starting check and remove all dir of deleted wallets....")
	validWallets := make(map[string]bool, len(wallets))
	deletedWalletDirs := make([]string, 0)

	for _, wallet := range wallets {
		key := wallet.Type.ToStringLower() + strconv.Itoa(wallet.ID)
		validWallets[key] = true
	}

	for _, wType := range mgr.AllAssetTypes() {
		dirName := ""
		if mgr.NetType() == utils.Testnet {
			dirName = utils.NetDir(wType, utils.Testnet)
		}
		rootDir := filepath.Join(mgr.params.RootDir, dirName, wType.ToStringLower())
		files, err := os.ReadDir(rootDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			log.Errorf("can't read %s root wallet type: %v", wType, err)
			return
		}
		for _, f := range files {
			key := wType.ToStringLower() + f.Name()
			if f.IsDir() && !validWallets[key] {
				deletedWalletDirs = append(deletedWalletDirs, filepath.Join(rootDir, f.Name()))
			}
		}
	}

	if len(deletedWalletDirs) == 0 {
		log.Info("No wallets to clean were found")
		return
	}

	for _, v := range deletedWalletDirs {
		if err := os.RemoveAll(v); err != nil {
			log.Errorf("Can't remove the wallet with error: %v", err)
		}
	}

	log.Info("Clean all deleted wallets")
}

// AllAssetTypes returns all asset types supported by the assets manager.
func (mgr *AssetsManager) AllAssetTypes() []utils.AssetType {
	return []utils.AssetType{utils.DCRWalletAsset}
}

// BlockExplorerURLForTx returns a URL for viewing a transaction on the
// Monetarium block explorer. The hostnames here are the canonical
// deployed explorer endpoints — change them in one place rather than
// per call site. Mainnet at monetarium.online; testnet under the
// testnet subdomain. Any future net (regtest, simnet) defaults to no
// URL — the 3-dots menu hides the "view in explorer" item when this
// returns "".
func (mgr *AssetsManager) BlockExplorerURLForTx(assetType utils.AssetType, txHash string) string {
	if assetType != utils.DCRWalletAsset {
		return ""
	}
	switch mgr.NetType() {
	case utils.Mainnet:
		return "https://monetarium.online/tx/" + txHash
	case utils.Testnet:
		return "https://testnet.monetarium.online/tx/" + txHash
	default:
		return ""
	}
}

// BlockExplorerURLForAgendas returns the Monetarium block explorer's
// consensus-voting dashboard (current agenda tallies) for the active
// network. Same hostname policy as BlockExplorerURLForTx: mainnet at
// monetarium.online, testnet under the testnet subdomain, any other net
// returns "" and callers hide their button.
func (mgr *AssetsManager) BlockExplorerURLForAgendas() string {
	switch mgr.NetType() {
	case utils.Mainnet:
		return "https://monetarium.online/agendas"
	case utils.Testnet:
		return "https://testnet.monetarium.online/agendas"
	default:
		return ""
	}
}

func (mgr *AssetsManager) LogFile() string {
	return filepath.Join(mgr.params.LogDir, LogFilename)
}

func (mgr *AssetsManager) DCRHDPrefix() string {
	switch mgr.NetType() {
	case utils.Testnet:
		return dcr.TestnetHDPath
	case utils.Mainnet:
		return dcr.MainnetHDPath
	default:
		return ""
	}
}

// CalculateTotalAssetsBalance returns the total balance per asset type across all wallets.
func (mgr *AssetsManager) CalculateTotalAssetsBalance(includeWatchWallet bool) (map[utils.AssetType]sharedW.AssetAmount, error) {
	assetsTotalBalance := make(map[utils.AssetType]sharedW.AssetAmount)

	for _, wal := range mgr.AllWallets() {
		if !includeWatchWallet && wal.IsWatchingOnlyWallet() {
			continue
		}

		accountsResult, err := wal.GetAccountsRaw()
		if err != nil {
			return nil, err
		}

		assetType := wal.GetAssetType()
		for _, account := range accountsResult.Accounts {
			assetTotal, ok := assetsTotalBalance[assetType]
			if ok {
				assetTotal = wal.ToAmount(assetTotal.ToInt() + account.Balance.Total.ToInt())
			} else {
				assetTotal = account.Balance.Total
			}
			assetsTotalBalance[assetType] = assetTotal
		}
	}

	return assetsTotalBalance, nil
}

// Listen when new tx is registered.
func (mgr *AssetsManager) ListenForTxAndBlockNotification(listen func(int)) {
	txAndBlockNotificationListener := &sharedW.TxAndBlockNotificationListener{
		OnTransactionConfirmed: func(walletID int, _ string, _ int32) {
			listen(walletID)
		},
		OnTransaction: func(walletID int, _ *sharedW.Transaction) {
			listen(walletID)
		},
	}

	for _, wallet := range mgr.AllWallets() {
		if !wallet.IsNotificationListenerExist(assetIdentifier) {
			if err := wallet.AddTxAndBlockNotificationListener(txAndBlockNotificationListener, assetIdentifier); err != nil {
				log.Errorf("Can't listen tx and block notification for %s wallet", wallet.GetWalletName())
			}
		}
	}
}

func (mgr *AssetsManager) RemoveAssetChange() {
	for _, wallet := range mgr.AllWallets() {
		wallet.RemoveTxAndBlockNotificationListener(assetIdentifier)
	}
}

func (mgr *AssetsManager) BadgerDB() string { return BadgerDB }
func (mgr *AssetsManager) BoltDB() string   { return BoltDB }

// AssetToCreate checks if there is any asset type that has not been created
// and returns the first one.
func (mgr *AssetsManager) AssetToCreate() libutils.AssetType {
	assetToCreate := mgr.AllAssetTypes()
	wallets := mgr.AllWallets()

	assetsNotCreated := make([]libutils.AssetType, 0)

	for _, asset := range assetToCreate {
		assetExists := false
		for _, wallet := range wallets {
			if wallet.GetAssetType() == asset {
				assetExists = true
				break
			}
		}
		if !assetExists {
			assetsNotCreated = append(assetsNotCreated, asset)
		}
	}

	if len(assetsNotCreated) == 0 {
		return ""
	}
	return assetsNotCreated[0]
}
