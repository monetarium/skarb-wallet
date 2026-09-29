package cryptomaterial

import "github.com/decred/slog"

var log = slog.Disabled

func init() {
	UseLogger(slog.Disabled)
}

func UseLogger(logger slog.Logger) {
	log = logger
}
