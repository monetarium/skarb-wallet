//go:build ios

package device

/*
#cgo LDFLAGS: -framework AVFoundation -framework CoreMedia -framework CoreVideo
#include "qr_scanner_ios.h"
#include <stdlib.h>
*/
import "C"
import (
	"unsafe"
)

// QRScanSupported reports whether in-app camera QR scanning exists on
// this platform.
func (d *Device) QRScanSupported() bool { return true }

// QRScanStart opens the camera scan overlay — or the system permission
// dialog on first use (poll QRScanTick afterwards; the grant is
// detected by re-checking AVCapture authorization).
func (d *Device) QRScanStart(hint, cancel string) error {
	return d.qrRun(func(vc uintptr) {
		ch := C.CString(hint)
		cc := C.CString(cancel)
		defer C.free(unsafe.Pointer(ch))
		defer C.free(unsafe.Pointer(cc))
		C.qr_start(C.uintptr_t(vc), ch, cc)
	})
}

// QRScanTick advances a pending permission request and snapshots the
// scanner state plus the newest luma frame. frame is nil unless a
// frame newer than lastSeq exists; it is an owned copy.
func (d *Device) QRScanTick(hint, cancel string, lastSeq int) (state, seq int, frame []byte, width, height int, err error) {
	err = d.qrRun(func(vc uintptr) {
		ch := C.CString(hint)
		cc := C.CString(cancel)
		defer C.free(unsafe.Pointer(ch))
		defer C.free(unsafe.Pointer(cc))
		C.qr_tick(C.uintptr_t(vc), ch, cc)
		state = int(C.qr_get_state())
		seq = int(C.qr_get_seq())
		width = int(C.qr_get_width())
		height = int(C.qr_get_height())
		if state != QRStateRunning || seq == lastSeq {
			return
		}
		var n C.int
		p := C.qr_copy_frame(&n)
		if p == nil || n <= 0 {
			return
		}
		frame = C.GoBytes(unsafe.Pointer(p), n)
		C.free(unsafe.Pointer(p))
	})
	return state, seq, frame, width, height, err
}

// QRScanStop tears the scan session down. Idempotent.
func (d *Device) QRScanStop() {
	_ = d.qrRun(func(uintptr) {
		C.qr_stop()
	})
}

// qrRun executes f on the iOS main thread. Camera UI and AVCapture
// session setup are main-thread-only.
func (d *Device) qrRun(f func(vc uintptr)) error {
	vc := d.viewHandle()
	if vc == 0 {
		return ErrCameraUnavailable
	}
	d.window.Run(func() {
		f(vc)
	})
	return nil
}
