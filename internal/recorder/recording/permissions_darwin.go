//go:build darwin

package recording

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AVFoundation -framework ApplicationServices -framework CoreGraphics
#import <AVFoundation/AVFoundation.h>
#import <ApplicationServices/ApplicationServices.h>
#import <CoreGraphics/CoreGraphics.h>

// Microphone: 0 = not asked yet, 1 = restricted, 2 = denied, 3 = granted
// (AVAuthorizationStatus).
static int perm_mic_status(void) {
	@autoreleasepool {
		return (int)[AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
	}
}

// Shows the system prompt if the user hasn't been asked yet; a no-op
// afterwards.
static void perm_mic_request(void) {
	@autoreleasepool {
		[AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio completionHandler:^(BOOL granted) {}];
	}
}

static int perm_screen_status(void) {
	@autoreleasepool {
		return CGPreflightScreenCaptureAccess() ? 1 : 0;
	}
}

// Shows the system prompt the first time only.
static void perm_screen_request(void) {
	@autoreleasepool {
		CGRequestScreenCaptureAccess();
	}
}

static int perm_accessibility_status(void) {
	@autoreleasepool {
		return AXIsProcessTrusted() ? 1 : 0;
	}
}
*/
import "C"

import "fmt"

// CheckPermissions logs which macOS privacy permissions the recorder has.
// The recorder has no window to ask from, so a permission never asked
// for is requested here, where macOS shows its own prompt once; one the
// user refused can only be changed in System Settings > Privacy &
// Security, which the log says. Recording goes ahead either way, with
// whatever the missing permission costs.
func CheckPermissions() {
	if C.perm_mic_status() == 0 {
		C.perm_mic_request()
	}
	if C.perm_screen_status() != 1 {
		C.perm_screen_request()
	}
	for _, p := range []struct {
		name, neededFor string
		granted         bool
	}{
		{"Microphone", "recording your voice", C.perm_mic_status() == 3},
		{"Screen Recording", "meeting audio from other apps, and speaker names from the meeting window", C.perm_screen_status() == 1},
		{"Accessibility", "reading speaker names from the meeting window", C.perm_accessibility_status() == 1},
	} {
		if !p.granted {
			fmt.Printf("permissions: %s not granted (needed for %s); grant it in System Settings > Privacy & Security\n", p.name, p.neededFor)
		}
	}
}
