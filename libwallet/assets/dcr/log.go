// Copyright (c) 2013-2017 The btcsuite developers
// Copyright (c) 2015-2018 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package dcr

import (
	"github.com/decred/slog"
	"github.com/monetarium/monetarium-wallet/p2p"
	"github.com/monetarium/monetarium-wallet/spv"
	"github.com/monetarium/monetarium-wallet/wallet"
	"github.com/monetarium/monetarium-wallet/wallet/udb"
	"github.com/monetarium/skarb-wallet/libwallet/internal/loader"
)

var log = slog.Disabled

// UseLogger sets the subsystem logs to use the provided loggers. Wires the
// same backing logger into every upstream package whose logs we care about
// during SPV — without these the spv, p2p, wallet and udb packages silently
// discard everything (slog.Disabled by default), which made it impossible to
// see why syncer.Run was bailing out before any peer connection.
func UseLogger(logger slog.Logger) {
	log = logger
	loader.UseLogger(logger)
	spv.UseLogger(logger)
	p2p.UseLogger(logger)
	wallet.UseLogger(logger)
	udb.UseLogger(logger)
}

// Log writes a message to the log using LevelInfo.
func Log(m string) {
	log.Info(m)
}

// LogT writes a tagged message to the log using LevelInfo.
func LogT(tag, m string) {
	log.Infof("%s: %s", tag, m)
}
