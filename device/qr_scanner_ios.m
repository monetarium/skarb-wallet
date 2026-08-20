#import "qr_scanner_ios.h"

#import <AVFoundation/AVFoundation.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreVideo/CoreVideo.h>
#import <UIKit/UIKit.h>

@interface SkarbQRScanner : NSObject <AVCaptureVideoDataOutputSampleBufferDelegate>
@end

static NSObject *qrLock;
static int qrState = QR_STATE_IDLE;
static int qrSeq = 0;
static int qrWidth = 0;
static int qrHeight = 0;
static NSMutableData *qrFrame;
static SkarbQRScanner *qrActive;

@implementation SkarbQRScanner {
	UIViewController *host;
	NSString *hintText;
	NSString *cancelText;
	UIView *overlay;
	AVCaptureSession *session;
	AVCaptureVideoPreviewLayer *preview;
	dispatch_queue_t cameraQueue;
}

+ (void)initialize {
	if (self == [SkarbQRScanner class]) {
		qrLock = [[NSObject alloc] init];
	}
}

- (instancetype)initWithHost:(UIViewController *)vc hint:(NSString *)hint cancel:(NSString *)cancel {
	self = [super init];
	if (self) {
		host = vc;
		hintText = hint ?: @"";
		cancelText = cancel ?: @"";
		cameraQueue = dispatch_queue_create("com.monetarium.skarb.qr", DISPATCH_QUEUE_SERIAL);
	}
	return self;
}

- (void)start {
	overlay = [[UIView alloc] initWithFrame:host.view.bounds];
	overlay.autoresizingMask = UIViewAutoresizingFlexibleWidth | UIViewAutoresizingFlexibleHeight;
	overlay.backgroundColor = UIColor.blackColor;

	preview = [AVCaptureVideoPreviewLayer layer];
	preview.videoGravity = AVLayerVideoGravityResizeAspectFill;
	preview.frame = overlay.bounds;
	[overlay.layer addSublayer:preview];

	UILabel *hint = [[UILabel alloc] init];
	hint.text = hintText;
	hint.textColor = UIColor.whiteColor;
	hint.font = [UIFont systemFontOfSize:16];
	hint.backgroundColor = [[UIColor blackColor] colorWithAlphaComponent:0.6];
	hint.textAlignment = NSTextAlignmentCenter;
	hint.numberOfLines = 0;
	hint.translatesAutoresizingMaskIntoConstraints = NO;
	[overlay addSubview:hint];

	UIButton *cancel = [UIButton buttonWithType:UIButtonTypeSystem];
	UIButtonConfiguration *cfg = [UIButtonConfiguration plainButtonConfiguration];
	cfg.title = [NSString stringWithFormat:@"✕  %@", cancelText];
	cfg.baseForegroundColor = UIColor.whiteColor;
	cfg.titleTextAttributesTransformer = ^NSDictionary<NSAttributedStringKey, id> *(NSDictionary<NSAttributedStringKey, id> *incoming) {
		NSMutableDictionary *out = [incoming mutableCopy] ?: [NSMutableDictionary dictionary];
		out[NSFontAttributeName] = [UIFont systemFontOfSize:16];
		return out;
	};
	cfg.contentInsets = NSDirectionalEdgeInsetsMake(10, 14, 10, 14);
	cfg.background.backgroundColor = [[UIColor blackColor] colorWithAlphaComponent:0.6];
	cancel.configuration = cfg;
	cancel.translatesAutoresizingMaskIntoConstraints = NO;
	[cancel addTarget:self action:@selector(onCancel) forControlEvents:UIControlEventTouchUpInside];
	[overlay addSubview:cancel];

	UILayoutGuide *safe = overlay.safeAreaLayoutGuide;
	[NSLayoutConstraint activateConstraints:@[
		[hint.leadingAnchor constraintEqualToAnchor:safe.leadingAnchor constant:16],
		[hint.trailingAnchor constraintEqualToAnchor:safe.trailingAnchor constant:-16],
		[hint.bottomAnchor constraintEqualToAnchor:safe.bottomAnchor constant:-24],
		[cancel.trailingAnchor constraintEqualToAnchor:safe.trailingAnchor constant:-16],
		[cancel.topAnchor constraintEqualToAnchor:safe.topAnchor constant:8],
	]];

	[host.view addSubview:overlay];

	session = [[AVCaptureSession alloc] init];
	session.sessionPreset = AVCaptureSessionPreset640x480;
	AVCaptureDevice *cam = [AVCaptureDevice defaultDeviceWithMediaType:AVMediaTypeVideo];
	if (!cam) {
		NSLog(@"skarb qr: no camera");
		[self fail];
		return;
	}
	NSError *err = nil;
	AVCaptureDeviceInput *input = [AVCaptureDeviceInput deviceInputWithDevice:cam error:&err];
	if (!input || ![session canAddInput:input]) {
		NSLog(@"skarb qr: camera input: %@", err);
		[self fail];
		return;
	}
	[session addInput:input];

	AVCaptureVideoDataOutput *output = [[AVCaptureVideoDataOutput alloc] init];
	output.alwaysDiscardsLateVideoFrames = YES;
	output.videoSettings = @{
		(id)kCVPixelBufferPixelFormatTypeKey : @(kCVPixelFormatType_420YpCbCr8BiPlanarFullRange)
	};
	[output setSampleBufferDelegate:self queue:cameraQueue];
	if (![session canAddOutput:output]) {
		NSLog(@"skarb qr: cannot add video output");
		[self fail];
		return;
	}
	[session addOutput:output];

	preview.session = session;
	preview.frame = overlay.bounds;

	@synchronized (qrLock) {
		qrState = QR_STATE_RUNNING;
	}

	dispatch_async(cameraQueue, ^{
		[self->session startRunning];
	});
}

- (void)layoutPreview {
	preview.frame = overlay.bounds;
}

- (void)onCancel {
	qr_stop();
}

- (void)fail {
	@synchronized (qrLock) {
		qrState = QR_STATE_ERROR;
		if (qrActive == self) {
			qrActive = nil;
		}
	}
	[self teardownUI];
}

- (void)teardownUI {
	if (session) {
		[session stopRunning];
		session = nil;
	}
	if (preview) {
		[preview removeFromSuperlayer];
		preview = nil;
	}
	if (overlay) {
		[overlay removeFromSuperview];
		overlay = nil;
	}
}

- (void)captureOutput:(AVCaptureOutput *)output
 didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer
        fromConnection:(AVCaptureConnection *)connection {
	CVImageBufferRef image = CMSampleBufferGetImageBuffer(sampleBuffer);
	if (!image) {
		return;
	}
	CVPixelBufferLockBaseAddress(image, kCVPixelBufferLock_ReadOnly);
	size_t width = CVPixelBufferGetWidthOfPlane(image, 0);
	size_t height = CVPixelBufferGetHeightOfPlane(image, 0);
	size_t stride = CVPixelBufferGetBytesPerRowOfPlane(image, 0);
	uint8_t *src = (uint8_t *)CVPixelBufferGetBaseAddressOfPlane(image, 0);
	if (!src || width == 0 || height == 0) {
		CVPixelBufferUnlockBaseAddress(image, kCVPixelBufferLock_ReadOnly);
		return;
	}
	NSUInteger len = (NSUInteger)(width * height);
	NSMutableData *copy = [NSMutableData dataWithLength:len];
	uint8_t *dst = (uint8_t *)copy.mutableBytes;
	if (stride == width) {
		memcpy(dst, src, len);
	} else {
		for (size_t y = 0; y < height; y++) {
			memcpy(dst + y * width, src + y * stride, width);
		}
	}
	CVPixelBufferUnlockBaseAddress(image, kCVPixelBufferLock_ReadOnly);

	@synchronized (qrLock) {
		qrFrame = copy;
		qrWidth = (int)width;
		qrHeight = (int)height;
		qrSeq++;
	}
}

@end

static NSString *qrGoString(const char *s) {
	if (!s) {
		return @"";
	}
	return [NSString stringWithUTF8String:s];
}

static void qrOpen(uintptr_t vcRef, const char *hint, const char *cancel) {
	UIViewController *vc = (__bridge UIViewController *)(void *)vcRef;
	if (!vc) {
		@synchronized (qrLock) {
			qrState = QR_STATE_ERROR;
		}
		return;
	}
	if (![AVCaptureDevice defaultDeviceWithMediaType:AVMediaTypeVideo]) {
		NSLog(@"skarb qr: no video device");
		@synchronized (qrLock) {
			qrState = QR_STATE_ERROR;
		}
		return;
	}
	SkarbQRScanner *s = [[SkarbQRScanner alloc] initWithHost:vc
	                                                    hint:qrGoString(hint)
	                                                  cancel:qrGoString(cancel)];
	qrActive = s;
	[s start];
}

void qr_start(uintptr_t vcRef, const char *hint, const char *cancel) {
	@synchronized (qrLock) {
		if (qrActive != nil) {
			return;
		}
		qrFrame = nil;
		qrSeq = 0;
		qrWidth = 0;
		qrHeight = 0;
	}

	AVAuthorizationStatus status = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeVideo];
	if (status == AVAuthorizationStatusDenied || status == AVAuthorizationStatusRestricted) {
		@synchronized (qrLock) {
			qrState = QR_STATE_ERROR;
		}
		return;
	}
	if (status == AVAuthorizationStatusNotDetermined) {
		@synchronized (qrLock) {
			qrState = QR_STATE_REQUESTING;
		}
		[AVCaptureDevice requestAccessForMediaType:AVMediaTypeVideo completionHandler:^(BOOL granted) {
			(void)granted;
			// qr_tick observes the new status and opens (or errors).
		}];
		return;
	}
	qrOpen(vcRef, hint, cancel);
}

void qr_tick(uintptr_t vcRef, const char *hint, const char *cancel) {
	int state;
	@synchronized (qrLock) {
		state = qrState;
	}
	if (state != QR_STATE_REQUESTING || qrActive != nil) {
		return;
	}
	AVAuthorizationStatus status = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeVideo];
	if (status == AVAuthorizationStatusAuthorized) {
		qrOpen(vcRef, hint, cancel);
		return;
	}
	if (status == AVAuthorizationStatusDenied || status == AVAuthorizationStatusRestricted) {
		@synchronized (qrLock) {
			qrState = QR_STATE_ERROR;
		}
	}
}

void qr_stop(void) {
	SkarbQRScanner *s;
	@synchronized (qrLock) {
		s = qrActive;
		qrActive = nil;
		qrState = QR_STATE_IDLE;
		qrFrame = nil;
	}
	[s teardownUI];
}

int qr_get_state(void) {
	@synchronized (qrLock) {
		return qrState;
	}
}

int qr_get_seq(void) {
	@synchronized (qrLock) {
		return qrSeq;
	}
}

int qr_get_width(void) {
	@synchronized (qrLock) {
		return qrWidth;
	}
}

int qr_get_height(void) {
	@synchronized (qrLock) {
		return qrHeight;
	}
}

uint8_t *qr_copy_frame(int *outLen) {
	NSData *copy;
	@synchronized (qrLock) {
		copy = qrFrame ? [qrFrame copy] : nil;
	}
	if (!copy || copy.length == 0) {
		if (outLen) {
			*outLen = 0;
		}
		return NULL;
	}
	uint8_t *buf = (uint8_t *)malloc(copy.length);
	if (!buf) {
		if (outLen) {
			*outLen = 0;
		}
		return NULL;
	}
	memcpy(buf, copy.bytes, copy.length);
	if (outLen) {
		*outLen = (int)copy.length;
	}
	return buf;
}
