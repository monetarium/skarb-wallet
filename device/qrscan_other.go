//go:build !android

package device

// In-app camera QR scanning is Android-only: desktop has no camera
// pipeline and iOS builds are not produced yet. The UI hides the scan
// affordance when QRScanSupported is false, so these stubs only guard
// against direct calls.

func (d *Device) QRScanSupported() bool { return false }

func (d *Device) QRScanStart(hint, cancel string) error { return ErrCameraUnavailable }

func (d *Device) QRScanTick(hint, cancel string, lastSeq int) (state, seq int, frame []byte, width, height int, err error) {
	return QRStateIdle, 0, nil, 0, 0, ErrCameraUnavailable
}

func (d *Device) QRScanStop() {}
