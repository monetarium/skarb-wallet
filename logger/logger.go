package logger

import (
	"errors"
	"sync"

	"github.com/decred/slog"
)

type logger struct {
	subsystemSLoggers map[string]slog.Logger
}

var (
	instance *logger
	initCtx  sync.Once
)

func New(sLoggers map[string]slog.Logger) {
	initCtx.Do(func() {
		instance = &logger{subsystemSLoggers: sLoggers}
	})
}

func (l *logger) setLogLevel(subsystemID string, logLevel string) {
	subsystem, ok := l.subsystemSLoggers[subsystemID]
	if !ok {
		return
	}
	level, _ := slog.LevelFromString(logLevel)
	subsystem.SetLevel(level)
}

func SetLogLevels(logLevel string) error {
	if instance == nil {
		return errors.New("can not set log level on nil logger")
	}
	for subsystemID := range instance.subsystemSLoggers {
		instance.setLogLevel(subsystemID, logLevel)
	}
	return nil
}

func SetLogLevel(subsystemID string, logLevel string) {
	if subsystem, ok := instance.subsystemSLoggers[subsystemID]; ok {
		level, _ := slog.LevelFromString(logLevel)
		subsystem.SetLevel(level)
	}
}
