#ifndef SKARB_QR_SCANNER_IOS_H
#define SKARB_QR_SCANNER_IOS_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

// States must match device.QRState* in qrdecode.go.
enum {
	QR_STATE_IDLE = 0,
	QR_STATE_REQUESTING = 1,
	QR_STATE_RUNNING = 2,
	QR_STATE_ERROR = -1
};

// vcRef is a CFTypeRef (UIViewController *) from Gio's UIKitViewEvent.
// All of these except the getters must run on the main thread.
void qr_start(uintptr_t vcRef, const char *hint, const char *cancel);
void qr_tick(uintptr_t vcRef, const char *hint, const char *cancel);
void qr_stop(void);

int qr_get_state(void);
int qr_get_seq(void);
int qr_get_width(void);
int qr_get_height(void);
// Snapshot of the latest luma plane. Caller must free() the returned
// pointer. NULL if no frame is available.
uint8_t *qr_copy_frame(int *outLen);

#ifdef __cplusplus
}
#endif

#endif
