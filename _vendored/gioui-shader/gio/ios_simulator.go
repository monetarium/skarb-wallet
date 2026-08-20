package gio

import (
	"os"
	"runtime"
)

// isIOSSimulator reports whether this process is the iOS Simulator.
// Gio's generated shaders.go only treated GOARCH=amd64 as simulator,
// so Apple Silicon (arm64 simulator, same GOARCH as a real iPhone)
// loaded device metallibs and Metal pipeline creation panicked.
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
