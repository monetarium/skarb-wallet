package device

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework UIKit -lresolv
#import "device_ios.h"
*/
import "C"
import (
	"sync/atomic"

	"gioui.org/app"
	"gioui.org/io/event"
)

type device struct {
	window *app.Window
	// view is the CFTypeRef of the UIViewController backing the Gio
	// window. Written by the window-event goroutine and read by QR-scan
	// code — always through atomics.
	view uintptr
}

func newDevice(w *app.Window) *device {
	return &device{window: w}
}

func (d *Device) listenEvents(evt event.Event) {
	if evt, ok := evt.(app.UIKitViewEvent); ok {
		atomic.StoreUintptr(&d.view, evt.ViewController)
	}
}

func (d *device) viewHandle() uintptr {
	return atomic.LoadUintptr(&d.view)
}

func (d *Device) setScreenAwake(isOn bool) error {
	d.window.Run(func() {
		C.setScreenAwake(C.bool(isOn))
	})
	return nil
}
