// resyncprobe opens an EXISTING Skarb datadir, loads the first wallet on the
// chosen network and calls SpvSync() — exactly what the UI Sync-toggle does.
// Whereas syncprobe creates a throwaway fresh wallet (and so always exercises
// the "first sync ever" path), resyncprobe reproduces the user-reported "I
// turn on sync, it immediately turns off" cycle that only happens when the
// wallet is already at chain tip.
//
// Usage:
//
//	go run ./cmd/resyncprobe -net testnet
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/monetarium/skarb-wallet/libwallet"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	"github.com/monetarium/skarb-wallet/libwallet/utils"
)

func main() {
	netFlag := flag.String("net", "testnet", "mainnet | testnet")
	timeout := flag.Duration("timeout", 60*time.Second, "wall-clock budget")
	password := flag.String("password", "hunter2hunter2", "spending password")
	flag.Parse()

	var netType utils.NetworkType
	switch *netFlag {
	case "testnet":
		netType = utils.Testnet
	case "mainnet":
		netType = utils.Mainnet
	default:
		die("unknown net %q", *netFlag)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		die("UserHomeDir: %v", err)
	}
	rootDir := filepath.Join(home, "Library/Application Support/Skarb")
	logDir := filepath.Join(rootDir, "logs")
	fmt.Printf("→ opening AssetsManager (rootDir=%s, net=%s)\n", rootDir, netType)

	mgr, err := libwallet.NewAssetsManager(rootDir, logDir, netType)
	if err != nil {
		die("NewAssetsManager: %v", err)
	}
	defer mgr.Shutdown()

	if err := mgr.OpenWallets(""); err != nil {
		fmt.Printf("  OpenWallets(empty): %v — retrying with password\n", err)
		if err := mgr.OpenWallets(*password); err != nil {
			die("OpenWallets: %v", err)
		}
	}

	wallets := mgr.AllDCRWallets()
	if len(wallets) == 0 {
		die("no DCR wallets in datadir")
	}
	wallet := wallets[0]
	fmt.Printf("  walletID=%d name=%s synced=%v height=%d\n",
		wallet.GetWalletID(), wallet.GetWalletName(), wallet.IsSynced(), wallet.GetBestBlockHeight())

	if wallet.IsLocked() {
		fmt.Println("→ unlocking with password")
		if err := wallet.UnlockWallet(*password); err != nil {
			die("UnlockWallet: %v", err)
		}
	}

	listener := &sharedW.SyncProgressListener{
		OnSyncStarted:                 func() { fmt.Println("• OnSyncStarted") },
		OnPeerConnectedOrDisconnected: func(n int32) { fmt.Printf("• peers=%d\n", n) },
		OnHeadersFetchProgress: func(p *sharedW.HeadersFetchProgressReport) {
			fmt.Printf("• headers: total=%d %d%%\n", p.TotalHeadersToFetch, p.HeadersFetchProgress)
		},
		OnAddressDiscoveryProgress: func(p *sharedW.AddressDiscoveryProgressReport) {
			fmt.Printf("• discovery %d%%\n", p.AddressDiscoveryProgress)
		},
		OnHeadersRescanProgress: func(p *sharedW.HeadersRescanProgressReport) {
			fmt.Printf("• rescan %d/%d %d%%\n", p.CurrentRescanHeight, p.TotalHeadersToScan, p.RescanProgress)
		},
		OnSyncCompleted:      func() { fmt.Println("• OnSyncCompleted ✓") },
		OnSyncCanceled:       func(_ bool) { fmt.Println("• OnSyncCanceled") },
		OnSyncEndedWithError: func(err error) { fmt.Printf("• OnSyncEndedWithError: %v\n", err) },
	}
	if err := wallet.AddSyncProgressListener(listener, "resyncprobe"); err != nil {
		die("AddSyncProgressListener: %v", err)
	}

	fmt.Println("→ calling SpvSync()")
	if err := wallet.SpvSync(); err != nil {
		fmt.Printf("✘ SpvSync returned error immediately: %v\n", err)
		os.Exit(1)
	}

	deadline := time.Now().Add(*timeout)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for time.Now().Before(deadline) {
		<-tick.C
		fmt.Printf("… IsSyncing=%v IsSynced=%v peers=%d height=%d\n",
			wallet.IsSyncing(), wallet.IsSynced(),
			wallet.ConnectedPeers(), wallet.GetBestBlockHeight())
	}
	fmt.Println("→ wall-clock budget exhausted")
	wallet.CancelSync()
}

func die(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "❌ "+format+"\n", args...)
	os.Exit(1)
}
