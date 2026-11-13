//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>

static void recorder_main_loop(void) {
	@autoreleasepool {
		[NSApplication sharedApplication];
		[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
		[NSApp run];
	}
}
*/
import "C"

import (
	"fmt"
	"os"
	"runtime"
)

// The main goroutine stays on the process's main thread, so withMainLoop
// can hand that thread to AppKit.
func init() { runtime.LockOSThread() }

// withMainLoop runs body on another goroutine while the main thread runs
// AppKit's loop, and exits the process when body returns. Window-audio
// capture (internal/recorder/guestaudio) hops to the main queue and waits
// for it; Tomoe's window or tray app keeps that queue served, and without
// a loop here the first meeting's capture start waited forever. body must
// do its own cleanup: the process exits as soon as it returns.
func withMainLoop(body func() error) error {
	go func() {
		if err := body(); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}()
	C.recorder_main_loop()
	return nil
}
