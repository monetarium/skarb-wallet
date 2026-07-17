package device

import (
	"errors"
	"image"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
)

// ErrNoQRCode is returned when a frame holds no decodable QR code — the
// normal case for most camera frames while the user is still aiming.
var ErrNoQRCode = errors.New("no QR code found in frame")

// ErrCameraUnavailable is returned by the QR-scan methods when there is
// no camera to scan with (desktop builds, no camera hardware, or the
// Android view is not attached yet).
var ErrCameraUnavailable = errors.New("camera unavailable")

// QR scan session states, mirroring qr_scanner.java.
const (
	QRStateIdle       = 0
	QRStateRequesting = 1 // waiting for the user to grant CAMERA
	QRStateRunning    = 2 // preview up, frames flowing
	QRStateError      = -1
)

// DecodeQRLuma decodes a QR code from an 8-bit luminance (grayscale)
// frame. The Android camera delivers NV21 frames whose first
// width×height bytes are exactly this Y plane, so no color conversion is
// needed — pass the frame prefix straight in. Pure Go (gozxing), so the
// decode path is unit-testable on desktop without a camera.
func DecodeQRLuma(pix []byte, width, height int) (string, error) {
	if width <= 0 || height <= 0 || len(pix) < width*height {
		return "", ErrNoQRCode
	}
	img := &image.Gray{
		Pix:    pix[:width*height],
		Stride: width,
		Rect:   image.Rect(0, 0, width, height),
	}
	src := gozxing.NewLuminanceSourceFromImage(img)
	bmp, err := gozxing.NewBinaryBitmap(gozxing.NewHybridBinarizer(src))
	if err != nil {
		return "", ErrNoQRCode
	}
	result, err := qrcode.NewQRCodeReader().Decode(bmp, nil)
	if err != nil {
		return "", ErrNoQRCode
	}
	return result.GetText(), nil
}
