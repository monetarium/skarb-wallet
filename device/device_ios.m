#import "device_ios.h"
#import <UIKit/UIKit.h>

BOOL setScreenAwake(BOOL isOn) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[UIApplication sharedApplication].idleTimerDisabled = isOn;
	});
	return isOn;
}