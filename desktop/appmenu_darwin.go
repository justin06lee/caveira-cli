//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

static void caveiraHideOthers(void) {
	dispatch_async(dispatch_get_main_queue(), ^{ [NSApp hideOtherApplications:nil]; });
}

static void caveiraShowAll(void) {
	dispatch_async(dispatch_get_main_queue(), ^{ [NSApp unhideAllApplications:nil]; });
}
*/
import "C"

// hideOthers and showAll are the caveira menu's Hide Others and Show All,
// which Wails' runtime has no call for.
func hideOthers() { C.caveiraHideOthers() }
func showAll()    { C.caveiraShowAll() }
