// Copyright (c) 2013-2017 The btcsuite developers
// Copyright (c) 2015-2018 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package libwallet

import (
	"os"

	"github.com/decred/slog"
	"github.com/jrick/logrotate/rotator"
	"github.com/monetarium/skarb-wallet/libwallet/internal/loader"
	"github.com/monetarium/skarb-wallet/libwallet/utils"
	"github.com/monetarium/monetarium-wallet/errors"
)

// logWriter implements an io.Writer that outputs to both standard output and
// the write-end pipe of an initialized log rotator.
type logWriter struct{}

func (logWriter) Write(p []byte) (n int, err error) {
	os.Stdout.Write(p)
	logRotator.Write(p)
	return len(p), nil
}

var (
	backendLog = slog.NewBackend(logWriter{})
	logRotator *rotator.Rotator
	vspcLog    = backendLog.Logger("VSPC")
)

var log = slog.Disabled

// subsystemLoggers maps each subsystem identifier to its associated logger.
var subsystemLoggers = map[string]slog.Logger{
	"DLWL": log,
	"VSPC": vspcLog,
}

func initLogRotator(logFile string) error {
	r, err := rotator.New(logFile, 10*1024, false, 3)
	if err != nil {
		return errors.Errorf("failed to create file rotator: %v", err)
	}
	logRotator = r
	return nil
}

func UseLogger(logger slog.Logger) {
	log = logger
	loader.UseLogger(logger)
}

func RegisterLogger(tag string) (slog.Logger, error) {
	if logRotator != nil {
		return nil, errors.E(utils.ErrLogRotatorAlreadyInitialized)
	}
	if _, exists := subsystemLoggers[tag]; exists {
		return nil, errors.E(utils.ErrLoggerAlreadyRegistered)
	}
	logger := backendLog.Logger(tag)
	subsystemLoggers[tag] = logger
	return logger, nil
}

func SetLogLevels(logLevel string) {
	if _, ok := slog.LevelFromString(logLevel); !ok {
		return
	}
	for subsystemID := range subsystemLoggers {
		setLogLevel(subsystemID, logLevel)
	}
}

func setLogLevel(subsystemID string, logLevel string) {
	logger, ok := subsystemLoggers[subsystemID]
	if !ok {
		return
	}
	level, _ := slog.LevelFromString(logLevel)
	logger.SetLevel(level)
}

func Log(m string)            { log.Info(m) }
func LogT(tag, m string)      { log.Infof("%s: %s", tag, m) }
