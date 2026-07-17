package device

import (
	"bytes"
	"image"
	_ "image/jpeg"
	"testing"

	qrcodegen "github.com/yeqown/go-qrcode"
)

// grayFromImage converts any decoded image into the luminance layout
// DecodeQRLuma expects — the same shape the Android camera's NV21 Y
// plane has.
func grayFromImage(img image.Image) ([]byte, int, int) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	pix := make([]byte, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			// ITU-R BT.601 luma, the same weighting cameras use.
			pix[y*w+x] = byte((299*r + 587*g + 114*bl) / 1000 >> 8)
		}
	}
	return pix, w, h
}

// TestDecodeQRLumaRoundTrip generates a QR with the same library the
// Receive page uses and decodes it with the scanner's decode path: the
// two must agree on a realistic wallet address payload.
func TestDecodeQRLumaRoundTrip(t *testing.T) {
	payloads := []string{
		"TsWkkPRCXZKypnJ9JrBVdEkjWZxJBnLDvfV",          // base58 testnet address shape
		"monetarium:TsWkkPRCXZKypnJ9JrBVdEkjWZxJBnLDvfV", // URI form
	}
	for _, want := range payloads {
		qr, err := qrcodegen.New(want)
		if err != nil {
			t.Fatalf("generate %q: %v", want, err)
		}
		var buf bytes.Buffer
		if err := qr.SaveTo(&buf); err != nil {
			t.Fatalf("encode %q: %v", want, err)
		}
		img, _, err := image.Decode(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("re-decode image for %q: %v", want, err)
		}
		pix, w, h := grayFromImage(img)
		got, err := DecodeQRLuma(pix, w, h)
		if err != nil {
			t.Fatalf("DecodeQRLuma(%q): %v", want, err)
		}
		if got != want {
			t.Fatalf("round-trip mismatch: got %q want %q", got, want)
		}
	}
}

// TestDecodeQRLumaNoCode ensures camera frames without a QR code return
// ErrNoQRCode instead of a false positive or panic.
func TestDecodeQRLumaNoCode(t *testing.T) {
	w, h := 640, 480
	pix := make([]byte, w*h)
	for i := range pix {
		pix[i] = byte(i*31 + i/w*17) // structured noise
	}
	if got, err := DecodeQRLuma(pix, w, h); err == nil {
		t.Fatalf("expected no code, decoded %q", got)
	}
	// Truncated frame must not panic either.
	if _, err := DecodeQRLuma(pix[:100], w, h); err == nil {
		t.Fatal("expected error on truncated frame")
	}
}
