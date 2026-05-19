// syncprobe creates a throwaway testnet wallet in a temp datadir, starts
// SPV against the hardcoded bootstrap peer, and watches the actual sync
// machinery for up to N seconds. Reports the real outcome instead of
// guessing from a wire handshake.
//
// Usage:
//
//	go run ./cmd/syncprobe -net testnet -timeout 180s
//	go run ./cmd/syncprobe -net mainnet -timeout 180s
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
	timeout := flag.Duration("timeout", 180*time.Second, "max wall-clock to watch sync before giving up")
	flag.Parse()

	var netType utils.NetworkType
	switch *netFlag {
	case "testnet":
		netType = utils.Testnet
	case "mainnet":
		netType = utils.Mainnet
	default:
		fmt.Fprintf(os.Stderr, "unknown net %q\n", *netFlag)
		os.Exit(2)
	}

	tmp, err := os.MkdirTemp("", "skarb-syncprobe-")
	if err != nil {
		die("mkdir tmp: %v", err)
	}
	defer os.RemoveAll(tmp)

	rootDir := filepath.Join(tmp, "data")
	logDir := filepath.Join(tmp, "logs")
	fmt.Printf("→ AssetsManager in %s (net=%s)\n", rootDir, netType)

	mgr, err := libwallet.NewAssetsManager(rootDir, logDir, netType)
	if err != nil {
		die("NewAssetsManager: %v", err)
	}
	defer mgr.Shutdown()

	fmt.Println("→ creating wallet")
	wallet, err := mgr.CreateNewDCRWallet("syncprobe", "hunter2hunter2",
		sharedW.PassphraseTypePass, sharedW.WordSeed33)
	if err != nil {
		die("CreateNewDCRWallet: %v", err)
	}
	fmt.Printf("  walletID=%d  network=%s\n", wallet.GetWalletID(), netType)

	// Hook listener so the script reports interesting events as they fire.
	listener := &sharedW.SyncProgressListener{
		OnSyncStarted: func() { fmt.Println("• OnSyncStarted") },
		OnPeerConnectedOrDisconnected: func(n int32) {
			fmt.Printf("• peers=%d\n", n)
		},
		OnCFiltersFetchProgress: func(p *sharedW.CFiltersFetchProgressReport) {
			fmt.Printf("• cfilters: fetched=%d/%d %d%%\n",
				p.TotalFetchedCFiltersCount, p.TotalCFiltersToFetch, p.CFiltersFetchProgress)
		},
		OnHeadersFetchProgress: func(p *sharedW.HeadersFetchProgressReport) {
			fmt.Printf("• headers: total=%d %d%%\n", p.TotalHeadersToFetch, p.HeadersFetchProgress)
		},
		OnAddressDiscoveryProgress: func(p *sharedW.AddressDiscoveryProgressReport) {
			fmt.Printf("• address discovery %d%%\n", p.AddressDiscoveryProgress)
		},
		OnHeadersRescanProgress: func(p *sharedW.HeadersRescanProgressReport) {
			fmt.Printf("• rescan: %d/%d %d%%\n",
				p.CurrentRescanHeight, p.TotalHeadersToScan, p.RescanProgress)
		},
		OnSyncCompleted: func() { fmt.Println("• OnSyncCompleted ✓") },
		OnSyncCanceled:  func(_ bool) { fmt.Println("• OnSyncCanceled ✗") },
		OnSyncEndedWithError: func(err error) {
			fmt.Printf("• OnSyncEndedWithError: %v\n", err)
		},
	}
	if err := wallet.AddSyncProgressListener(listener, "syncprobe"); err != nil {
		die("AddSyncProgressListener: %v", err)
	}

	fmt.Println("→ kicking SpvSync()")
	if err := wallet.SpvSync(); err != nil {
		die("SpvSync: %v", err)
	}

	deadline := time.Now().Add(*timeout)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if wallet.IsSynced() {
				bb := wallet.GetBestBlock()
				if bb != nil {
					fmt.Printf("✅ SYNCED. height=%d peers=%d\n", bb.Height, wallet.ConnectedPeers())
				} else {
					fmt.Printf("✅ SYNCED. peers=%d (no best-block yet)\n", wallet.ConnectedPeers())
				}
				wallet.CancelSync()
				return
			}
			fmt.Printf("… IsSyncing=%v peers=%d bestHeight=%d remaining=%s\n",
				wallet.IsSyncing(), wallet.ConnectedPeers(),
				wallet.GetBestBlockHeight(), time.Until(deadline).Round(time.Second))
			if time.Now().After(deadline) {
				fmt.Printf("✘ Timeout after %s — not synced. Final state: peers=%d height=%d syncing=%v\n",
					*timeout, wallet.ConnectedPeers(),
					wallet.GetBestBlockHeight(), wallet.IsSyncing())
				wallet.CancelSync()
				os.Exit(1)
			}
		}
	}
}

func die(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "❌ "+format+"\n", args...)
	os.Exit(1)
}
