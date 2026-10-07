//go:build darwin && !ios

package ui

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
*/
import "C"
