//go:build darwin && !ios
// +build darwin,!ios

package appos

func init() {
	appCurrentOS.isDarwin = true
}
