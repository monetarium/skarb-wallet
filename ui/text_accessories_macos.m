//go:build darwin && !ios

#import <AppKit/AppKit.h>

// Gio v0.8 renders its own caret in this NSView. Use the public
// NSTextInputClient hook to hide macOS cursor accessories in our windows,
// while leaving input methods and the user's system preferences intact.
@interface GioView : NSView
@end

@implementation GioView (SkarbTextAccessories)
- (NSInteger)preferredTextAccessoryPlacement {
    if (@available(macOS 14.0, *)) {
        return NSTextCursorAccessoryPlacementInvisible;
    }
    return 0;
}
@end
