package piet

import (
	"os"
	"runtime"
)

func isIOSSimulator() bool {
	if runtime.GOOS != "ios" {
		return false
	}
	switch runtime.GOARCH {
	case "amd64", "386":
		return true
	}
	return os.Getenv("SIMULATOR_MODEL_IDENTIFIER") != "" || os.Getenv("SIMULATOR_UDID") != ""
}
