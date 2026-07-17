//go:build android

package device

import (
	"gioui.org/app"
	"git.wow.st/gmp/jni"

	// Blank import: gogio walks the import graph and, for every
	// gioui.org/app/permission/* package it finds, adds the matching
	// <uses-permission>/<uses-feature> entries to the generated
	// AndroidManifest. This is the only way to get CAMERA in there.
	_ "gioui.org/app/permission/camera"
)

// qrJNI caches the scanner class and its static method IDs. The Java
// side is all-static (one scan session at a time), so one package-level
// cache matches. The class is promoted to a JNI global reference —
// jni.LoadClass returns a local ref that dies with its JNI frame.
type qrJNI struct {
	class     jni.Class
	startScan jni.MethodID
	tick      jni.MethodID
	stopScan  jni.MethodID
	getState  jni.MethodID
	getSeq    jni.MethodID
	getWidth  jni.MethodID
	getHeight jni.MethodID
	getFrame  jni.MethodID
}

var qr *qrJNI

func qrInit(env jni.Env) error {
	if qr != nil {
		return nil
	}
	class, err := jni.LoadClass(env,
		jni.ClassLoaderFor(env, jni.Object(app.AppContext())),
		"org/gioui/x/device/qr_scanner")
	if err != nil {
		return err
	}
	global := jni.Class(jni.NewGlobalRef(env, jni.Object(class)))
	qr = &qrJNI{
		class:     global,
		startScan: jni.GetStaticMethodID(env, global, "startScan", "(Landroid/view/View;Ljava/lang/String;Ljava/lang/String;)V"),
		tick:      jni.GetStaticMethodID(env, global, "tick", "(Landroid/view/View;Ljava/lang/String;Ljava/lang/String;)V"),
		stopScan:  jni.GetStaticMethodID(env, global, "stopScan", "(Landroid/view/View;)V"),
		getState:  jni.GetStaticMethodID(env, global, "getState", "()I"),
		getSeq:    jni.GetStaticMethodID(env, global, "getSeq", "()I"),
		getWidth:  jni.GetStaticMethodID(env, global, "getFrameWidth", "()I"),
		getHeight: jni.GetStaticMethodID(env, global, "getFrameHeight", "()I"),
		getFrame:  jni.GetStaticMethodID(env, global, "getFrame", "()[B"),
	}
	return nil
}

// QRScanSupported reports whether in-app camera QR scanning exists on
// this platform.
func (d *Device) QRScanSupported() bool { return true }

// QRScanStart opens the camera scan overlay — or the system permission
// dialog on first use (poll QRScanTick afterwards; GioActivity drops the
// permission-result callback, so the grant is detected by re-checking).
// A session already running (e.g. another recipient's editor) is
// rejected — the Java scanner is a singleton and silently attaching a
// second poll loop to it would deliver one QR into two editors.
// hint and cancel are the localized overlay strings.
func (d *Device) QRScanStart(hint, cancel string) error {
	return d.qrRun(func(env jni.Env, view uintptr) error {
		state, err := jni.CallStaticIntMethod(env, qr.class, qr.getState)
		if err != nil {
			return err
		}
		if state == QRStateRunning {
			return ErrCameraUnavailable
		}
		return jni.CallStaticVoidMethod(env, qr.class, qr.startScan,
			jni.Value(view),
			jni.Value(jni.JavaString(env, hint)),
			jni.Value(jni.JavaString(env, cancel)),
		)
	})
}

// QRScanTick advances a pending permission request and snapshots the
// scanner state plus the newest camera frame. frame is nil unless the
// scanner is running and a frame newer than lastSeq exists; it is an
// owned copy — decode it OUTSIDE this call (this one runs on the Android
// UI thread).
func (d *Device) QRScanTick(hint, cancel string, lastSeq int) (state, seq int, frame []byte, width, height int, err error) {
	err = d.qrRun(func(env jni.Env, view uintptr) error {
		if err := jni.CallStaticVoidMethod(env, qr.class, qr.tick,
			jni.Value(view),
			jni.Value(jni.JavaString(env, hint)),
			jni.Value(jni.JavaString(env, cancel)),
		); err != nil {
			return err
		}
		var jerr error
		if state, jerr = jni.CallStaticIntMethod(env, qr.class, qr.getState); jerr != nil {
			return jerr
		}
		if seq, jerr = jni.CallStaticIntMethod(env, qr.class, qr.getSeq); jerr != nil {
			return jerr
		}
		if state != QRStateRunning || seq == lastSeq {
			return nil
		}
		obj, jerr := jni.CallStaticObjectMethod(env, qr.class, qr.getFrame)
		if jerr != nil || obj == 0 {
			return jerr
		}
		if width, jerr = jni.CallStaticIntMethod(env, qr.class, qr.getWidth); jerr != nil {
			return jerr
		}
		if height, jerr = jni.CallStaticIntMethod(env, qr.class, qr.getHeight); jerr != nil {
			return jerr
		}
		// GetByteArrayElements copies and releases internally, so the
		// returned slice is safe to keep past this JNI frame.
		frame = jni.GetByteArrayElements(env, jni.ByteArray(obj))
		return nil
	})
	return state, seq, frame, width, height, err
}

// QRScanStop tears the scan session down. Idempotent.
func (d *Device) QRScanStop() {
	_ = d.qrRun(func(env jni.Env, view uintptr) error {
		return jni.CallStaticVoidMethod(env, qr.class, qr.stopScan, jni.Value(view))
	})
}

// qrRun executes f on the Android UI thread with an attached JNI env —
// the Java scanner touches views and the camera, both UI-thread-only.
// The view handle is snapshotted atomically once per call: it is written
// by the window-event goroutine while the scan poll goroutine runs.
func (d *Device) qrRun(f func(env jni.Env, view uintptr) error) error {
	view := d.viewHandle()
	if view == 0 {
		return ErrCameraUnavailable
	}
	var err error
	d.window.Run(func() {
		err = jni.Do(jni.JVMFor(app.JavaVM()), func(env jni.Env) error {
			if e := qrInit(env); e != nil {
				return e
			}
			return f(env, view)
		})
	})
	return err
}
