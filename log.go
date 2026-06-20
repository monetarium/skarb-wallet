// Copyright (c) 2016, 2018 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/decred/slog"
	"github.com/jrick/logrotate/rotator"

	"github.com/monetarium/skarb-wallet/libwallet"
	"github.com/monetarium/skarb-wallet/libwallet/assets/dcr"
	sharedW "github.com/monetarium/skarb-wallet/libwallet/assets/wallet"
	libutils "github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/skarb-wallet/logger"
	"github.com/monetarium/skarb-wallet/ui"
	"github.com/monetarium/skarb-wallet/ui/load"
	"github.com/monetarium/skarb-wallet/ui/modal"
	"github.com/monetarium/skarb-wallet/ui/page"
	account "github.com/monetarium/skarb-wallet/ui/page/accounts"
	"github.com/monetarium/skarb-wallet/ui/page/components"
	"github.com/monetarium/skarb-wallet/ui/page/info"
	"github.com/monetarium/skarb-wallet/ui/page/receive"
	"github.com/monetarium/skarb-wallet/ui/page/root"
	"github.com/monetarium/skarb-wallet/ui/page/send"
	"github.com/monetarium/skarb-wallet/ui/page/staking"
	"github.com/monetarium/skarb-wallet/ui/page/transaction"
	"github.com/monetarium/skarb-wallet/ui/page/wallet"

	"github.com/monetarium/monetarium-node/addrmgr"
	"github.com/monetarium/monetarium-node/connmgr"
	"github.com/monetarium/monetarium-wallet/p2p"
	"github.com/monetarium/monetarium-wallet/spv"
	"github.com/monetarium/monetarium-wallet/ticketbuyer"
	dcrw "github.com/monetarium/monetarium-wallet/wallet"
	"github.com/monetarium/monetarium-wallet/wallet/udb"
)

type logWriter struct {
	loggerID string
}

func (l logWriter) Write(p []byte) (n int, err error) {
	os.Stdout.Write(p)
	return logRotators[l.loggerID].Write(p)
}

var (
	dcrLogger, mainLogger = "dcr.log", libwallet.LogFilename

	dcrBackendLog = slog.NewBackend(logWriter{dcrLogger})
	backendLog    = slog.NewBackend(logWriter{mainLogger})

	logRotators map[string]*rotator.Rotator

	log          = backendLog.Logger("CRPW")
	sharedWLog   = backendLog.Logger("SHWL")
	winLog       = backendLog.Logger("UI")
	dlwlLog      = backendLog.Logger("DLWL")
	amgrLog      = backendLog.Logger("AMGR")
	cmgrLog      = backendLog.Logger("CMGR")
	dcrLog       = dcrBackendLog.Logger("DCR")
	syncLog      = dcrBackendLog.Logger("SYNC")
	tkbyLog      = dcrBackendLog.Logger("TKBY")
	dcrWalletLog = dcrBackendLog.Logger("WLLT")
	dcrSpv       = dcrBackendLog.Logger("DCR-S")
)

func init() {
	sharedW.UseLogger(sharedWLog)
	page.UseLogger(winLog)
	ui.UseLogger(winLog)
	send.UseLogger(winLog)
	root.UseLogger(winLog)
	libwallet.UseLogger(dlwlLog)
	dcr.UseLogger(dcrLog)
	load.UseLogger(log)
	components.UseLogger(winLog)
	transaction.UseLogger(winLog)
	info.UseLogger(winLog)
	modal.UseLogger(winLog)
	addrmgr.UseLogger(dcrLog)
	connmgr.UseLogger(dcrLog)
	p2p.UseLogger(syncLog)
	ticketbuyer.UseLogger(tkbyLog)
	udb.UseLogger(dcrWalletLog)
	dcrw.UseLogger(dcrLog)
	spv.UseLogger(dcrSpv)
	account.UseLogger(winLog)
	wallet.UseLogger(winLog)
	receive.UseLogger(winLog)
	staking.UseLogger(winLog)

	logger.New(subsystemSLoggers)
	// SPV used to be muted to ERROR-only — that hid the "Headers synced
	// through block ...", "Transactions synced ...", peer connect/disconnect
	// reasons and the actual cause of syncer.Run returning early. We need
	// those at INF while the chain layer is still maturing.
	dcrSpv.SetLevel(slog.LevelInfo)
	syncLog.SetLevel(slog.LevelInfo)
}

var subsystemSLoggers = map[string]slog.Logger{
	"DLWL": dlwlLog,
	"DCR":  dcrLog,
	"UI":   winLog,
	"CRPW": log,
	"AMGR": amgrLog,
	"CMGR": cmgrLog,
	"SYNC": syncLog,
	"TKBY": tkbyLog,
	"WLLT": dcrWalletLog,
	"SHWL": sharedWLog,
}

func initLogRotator(logDir string, maxRolls int) {
	for _, rotator := range logRotators {
		if rotator != nil {
			rotator.Close()
		}
	}

	logRotators = map[string]*rotator.Rotator{
		dcrLogger:  nil,
		mainLogger: nil,
	}

	if err := os.MkdirAll(logDir, libutils.UserFilePerm); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create log directory: %v\n", err)
		os.Exit(1)
	}

	for logFile := range logRotators {
		r, err := rotator.New(filepath.Join(logDir, logFile), 32*1024, false, maxRolls)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to create file rotator: %v\n", err)
			os.Exit(1)
		}
		logRotators[logFile] = r
	}
}

func isExistSystem(subsysID string) bool {
	_, ok := subsystemSLoggers[subsysID]
	return ok
}
