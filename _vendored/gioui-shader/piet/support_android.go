// SPDX-License-Identifier: Unlicense OR MIT

// Skarb addition. Nothing in this package uses cgo on android — every
// *_abi.go file is excluded there — so the go tool would not compile any .c
// file either, and support.c's two coroutine helpers would go missing while
// the *_linux_<arch>.syso objects that call them still get linked (see the
// comment at the top of support.c).
//
// This file exists only to turn cgo on for the package on android, which
// makes support.c part of the build. It deliberately declares nothing: the
// C helpers are referenced from the .syso objects, not from Go.

//go:build android

package piet

/*
// The same .syso objects also call powf. They were compiled on a Linux host
// where the toolchain links libm as a matter of course; the Android link has
// to be told, or the loader stops at the first unresolved math symbol exactly
// as it did at the coroutine helpers.
#cgo LDFLAGS: -lm
*/
import "C"
