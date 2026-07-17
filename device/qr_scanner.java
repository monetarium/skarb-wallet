package org.gioui.x.device;

import android.app.Activity;
import android.graphics.Color;
import android.hardware.Camera;
import android.os.Build;
import android.view.Gravity;
import android.view.SurfaceHolder;
import android.view.SurfaceView;
import android.view.View;
import android.view.ViewGroup;
import android.widget.FrameLayout;
import android.widget.TextView;

// qr_scanner shows a full-screen camera preview over the Gio view and
// hands raw NV21 frames to Go, which does the actual QR decoding
// (pure-Go zxing port — keeps every byte of decode logic unit-testable
// off-device). All control methods must run on the Android UI thread;
// the Go side guarantees that by calling them via app.Window.Run. The
// frame/state getters are volatile reads and may be called from any
// attached JNI thread.
//
// The deprecated android.hardware.Camera API is used deliberately: it
// is ~5x less code than camera2, still fully functional on API 34, and
// a QR scan needs none of camera2's capabilities.
public class qr_scanner implements SurfaceHolder.Callback, Camera.PreviewCallback {
	public static final int STATE_IDLE = 0;
	public static final int STATE_REQUESTING = 1; // waiting for CAMERA permission
	public static final int STATE_RUNNING = 2;    // preview up, frames flowing
	public static final int STATE_ERROR = -1;     // camera unavailable / open failed

	private static volatile int state = STATE_IDLE;
	private static volatile byte[] frame; // latest NV21 frame (Y plane first)
	private static volatile int frameWidth;
	private static volatile int frameHeight;
	private static volatile int frameSeq;

	// UI-thread only.
	private static qr_scanner active;

	private final Activity activity;
	private final String hintText;
	private final String cancelText;
	private FrameLayout overlay;
	private Camera camera;
	private int previewWidth;
	private int previewHeight;
	// publishFrame is reused across frames (rotating callback buffers +
	// one publish copy) so a scan session allocates a fixed ~4MB instead
	// of ~40MB/s of per-frame garbage. A rare torn copy just fails that
	// tick's decode — harmless.
	private byte[] publishFrame;

	private qr_scanner(Activity activity, String hintText, String cancelText) {
		this.activity = activity;
		this.hintText = hintText;
		this.cancelText = cancelText;
	}

	// startScan begins a scan session: asks for the CAMERA permission if
	// needed, otherwise opens the preview overlay. UI thread only.
	public static void startScan(View view, String hintText, String cancelText) {
		if (active != null) {
			return;
		}
		Activity activity = (Activity) view.getContext();
		if (!hasPermission(activity)) {
			state = STATE_REQUESTING;
			if (Build.VERSION.SDK_INT >= 23) {
				activity.requestPermissions(new String[]{android.Manifest.permission.CAMERA}, 0x5147);
			}
			return;
		}
		open(activity, hintText, cancelText);
	}

	// tick is polled by Go while STATE_REQUESTING: GioActivity does not
	// forward onRequestPermissionsResult, so the grant is detected by
	// re-checking the permission until the user answers the dialog.
	public static void tick(View view, String hintText, String cancelText) {
		if (active != null || state != STATE_REQUESTING) {
			return;
		}
		Activity activity = (Activity) view.getContext();
		if (hasPermission(activity)) {
			open(activity, hintText, cancelText);
		}
	}

	// stopScan tears the session down (also wired to the ✕ button). UI
	// thread only.
	public static void stopScan(View unused) {
		if (active != null) {
			active.close();
		}
		state = STATE_IDLE;
		frame = null;
	}

	public static int getState() { return state; }
	public static int getSeq() { return frameSeq; }
	public static int getFrameWidth() { return frameWidth; }
	public static int getFrameHeight() { return frameHeight; }
	public static byte[] getFrame() { return frame; }

	private static boolean hasPermission(Activity activity) {
		return Build.VERSION.SDK_INT < 23 ||
			activity.checkSelfPermission(android.Manifest.permission.CAMERA) ==
				android.content.pm.PackageManager.PERMISSION_GRANTED;
	}

	private static void open(Activity activity, String hintText, String cancelText) {
		// The camera itself is opened in surfaceCreated (and re-opened
		// there when the app returns from background); a missing camera
		// surfaces as STATE_ERROR from that path.
		qr_scanner s = new qr_scanner(activity, hintText, cancelText);
		active = s;
		s.showOverlay();
		state = STATE_RUNNING;
	}

	private void showOverlay() {
		overlay = new FrameLayout(activity);
		overlay.setBackgroundColor(Color.BLACK);
		// Consume every touch: without this a non-clickable view lets
		// taps fall through to the (invisible) Gio UI underneath — the
		// user could focus editors or press Send while "in" the camera.
		overlay.setClickable(true);

		SurfaceView surface = new SurfaceView(activity);
		surface.getHolder().addCallback(this);
		overlay.addView(surface, new FrameLayout.LayoutParams(
			ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));

		TextView hint = new TextView(activity);
		hint.setText(hintText);
		hint.setTextColor(Color.WHITE);
		hint.setTextSize(16);
		hint.setPadding(dp(16), dp(12), dp(16), dp(12));
		hint.setBackgroundColor(0x99000000);
		FrameLayout.LayoutParams hintLP = new FrameLayout.LayoutParams(
			ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT,
			Gravity.BOTTOM | Gravity.CENTER_HORIZONTAL);
		hintLP.bottomMargin = dp(48);
		overlay.addView(hint, hintLP);

		TextView cancel = new TextView(activity);
		cancel.setText("✕  " + cancelText);
		cancel.setTextColor(Color.WHITE);
		cancel.setTextSize(16);
		cancel.setPadding(dp(16), dp(12), dp(16), dp(12));
		cancel.setBackgroundColor(0x99000000);
		cancel.setOnClickListener(new View.OnClickListener() {
			public void onClick(View v) {
				stopScan(null);
			}
		});
		FrameLayout.LayoutParams cancelLP = new FrameLayout.LayoutParams(
			ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT,
			Gravity.TOP | Gravity.END);
		cancelLP.topMargin = dp(32);
		cancelLP.rightMargin = dp(16);
		overlay.addView(cancel, cancelLP);

		activity.addContentView(overlay, new ViewGroup.LayoutParams(
			ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));
	}

	private void close() {
		if (camera != null) {
			try {
				camera.setPreviewCallback(null);
				camera.stopPreview();
			} catch (Exception ignored) {
			}
			camera.release();
			camera = null;
		}
		if (overlay != null) {
			ViewGroup parent = (ViewGroup) overlay.getParent();
			if (parent != null) {
				parent.removeView(overlay);
			}
			overlay = null;
		}
		active = null;
	}

	private int dp(int v) {
		return (int) (v * activity.getResources().getDisplayMetrics().density);
	}

	// SurfaceHolder.Callback — the camera lives strictly between
	// surfaceCreated and surfaceDestroyed: backgrounding the app tears
	// the surface down (camera released, no background camera hold);
	// returning re-creates the surface and the same session resumes.

	public void surfaceCreated(SurfaceHolder holder) {
		if (camera == null) {
			try {
				camera = Camera.open();
			} catch (Exception e) {
				camera = null;
			}
			if (camera == null) {
				state = STATE_ERROR;
				close();
				return;
			}
		}
		try {
			camera.setErrorCallback(new Camera.ErrorCallback() {
				public void onError(int error, Camera unused) {
					// Camera service died or evicted us (another app,
					// system pressure) — surface Go a clean error.
					state = STATE_ERROR;
					close();
				}
			});
			Camera.Parameters params = camera.getParameters();
			if (params.getSupportedFocusModes()
					.contains(Camera.Parameters.FOCUS_MODE_CONTINUOUS_PICTURE)) {
				params.setFocusMode(Camera.Parameters.FOCUS_MODE_CONTINUOUS_PICTURE);
			}
			camera.setParameters(params);
			Camera.Size size = camera.getParameters().getPreviewSize();
			previewWidth = size.width;
			previewHeight = size.height;
			// Portrait phones: rotate the on-screen preview only; the
			// NV21 frames stay landscape, which QR decoding is
			// indifferent to (finder patterns fix the orientation).
			camera.setDisplayOrientation(90);
			camera.setPreviewDisplay(holder);
			// Rotating callback buffers: Android refills these instead
			// of allocating a fresh ~1.4MB array 30 times a second.
			int bufSize = previewWidth * previewHeight *
				android.graphics.ImageFormat.getBitsPerPixel(
					android.graphics.ImageFormat.NV21) / 8;
			camera.addCallbackBuffer(new byte[bufSize]);
			camera.addCallbackBuffer(new byte[bufSize]);
			camera.setPreviewCallbackWithBuffer(this);
			camera.startPreview();
		} catch (Exception e) {
			state = STATE_ERROR;
			close();
		}
	}

	public void surfaceChanged(SurfaceHolder holder, int format, int width, int height) {
	}

	public void surfaceDestroyed(SurfaceHolder holder) {
		// Backgrounded or overlay being removed: release the camera so
		// the app never holds it while invisible. close() already
		// released it in the teardown path — the null check covers that.
		if (camera != null) {
			try {
				camera.setPreviewCallback(null);
				camera.stopPreview();
			} catch (Exception ignored) {
			}
			camera.release();
			camera = null;
		}
	}

	// Camera.PreviewCallback (buffered mode): copy into the reusable
	// publish array and hand the buffer straight back to the camera.
	public void onPreviewFrame(byte[] data, Camera cam) {
		if (publishFrame == null || publishFrame.length != data.length) {
			publishFrame = new byte[data.length];
		}
		System.arraycopy(data, 0, publishFrame, 0, data.length);
		frameWidth = previewWidth;
		frameHeight = previewHeight;
		frame = publishFrame;
		frameSeq++;
		if (cam != null) {
			cam.addCallbackBuffer(data);
		}
	}
}
