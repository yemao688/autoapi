//go:build darwin

// Package dock toggles the macOS Dock icon at runtime by switching
// NSApplication's activation policy between Regular (Dock icon visible)
// and Accessory (no Dock icon, no app menu, no Cmd-Tab entry).
//
// This mirrors the Wails v3 Dock service (v3/pkg/services/dock/dock_darwin.go)
// adapted for Wails v2. All AppKit calls are dispatched onto the main queue
// via dispatch_sync because setActivationPolicy: must run on the main thread.
package dock

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa

#import <Cocoa/Cocoa.h>

// SetActivationPolicy switches the app's activation policy.
//   0 = NSApplicationActivationPolicyRegular
//   1 = NSApplicationActivationPolicyAccessory
//   2 = NSApplicationActivationPolicyProhibited
//
// If already on the main thread, call directly; otherwise dispatch_sync
// onto the main queue to avoid deadlock.
void SetActivationPolicy(int policy) {
    if (NSThread.isMainThread) {
        [NSApp setActivationPolicy:(NSApplicationActivationPolicy)policy];
        return;
    }
    dispatch_sync(dispatch_get_main_queue(), ^{
        [NSApp setActivationPolicy:(NSApplicationActivationPolicy)policy];
    });
}

// IsAccessory returns true if the current policy is Accessory.
bool IsAccessory(void) {
    if (NSThread.isMainThread) {
        return [NSApp activationPolicy] == NSApplicationActivationPolicyAccessory;
    }
    __block BOOL result = NO;
    dispatch_sync(dispatch_get_main_queue(), ^{
        result = [NSApp activationPolicy] == NSApplicationActivationPolicyAccessory;
    });
    return result;
}
*/
import "C"

// HideDockIcon switches the app to Accessory policy: no Dock icon, no
// app-menu bar, no Cmd-Tab entry. The process keeps running and the HTTP
// proxy stays alive. Existing windows remain valid but should be hidden
// by the caller before (or right after) calling this.
func HideDockIcon() {
	C.SetActivationPolicy(1)
}

// ShowDockIcon switches the app back to Regular policy. The Dock icon
// reappears. The caller must follow up with runtime.Show + WindowShow
// to bring the window back to the foreground.
func ShowDockIcon() {
	C.SetActivationPolicy(0)
}

// IsAccessory returns true if the Dock icon is currently hidden.
func IsAccessory() bool {
	return bool(C.IsAccessory())
}
