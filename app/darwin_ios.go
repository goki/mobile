// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build darwin && ios
// +build darwin,ios

package app

/*
#cgo CFLAGS: -x objective-c -DGL_SILENCE_DEPRECATION
#cgo LDFLAGS: -framework Foundation -framework UIKit -framework MobileCoreServices -framework QuartzCore -framework UserNotifications
#include <sys/utsname.h>
#include <stdint.h>
#include <stdbool.h>
#include <pthread.h>
#import <UIKit/UIKit.h>
#import <MobileCoreServices/MobileCoreServices.h>
#include <UIKit/UIDevice.h>

extern struct utsname sysInfo;

void runApp(void);
uint64_t threadID();

UIEdgeInsets getDevicePadding();
bool isDark();
void showKeyboard(int keyboardType);
void hideKeyboard();
*/
import "C"
import (
	"log"
	"runtime"
	"strings"
	"unsafe"

	"goki.dev/mobile/event/lifecycle"
	"goki.dev/mobile/event/paint"
	"goki.dev/mobile/event/size"
	"goki.dev/mobile/event/touch"
	"goki.dev/mobile/geom"
)

var initThreadID uint64

func init() {
	// Lock the goroutine responsible for initialization to an OS thread.
	// This means the goroutine running main (and calling the run function
	// below) is locked to the OS thread that started the program. This is
	// necessary for the correct delivery of UIKit events to the process.
	//
	// A discussion on this topic:
	// https://groups.google.com/forum/#!msg/golang-nuts/IiWZ2hUuLDA/SNKYYZBelsYJ
	runtime.LockOSThread()
	initThreadID = uint64(C.threadID())
}

func main(f func(App)) {
	//if tid := uint64(C.threadID()); tid != initThreadID {
	//	log.Fatalf("app.Run called on thread %d, but app.init ran on %d", tid, initThreadID)
	//}

	log.Println("in mobile main")
	go func() {
		f(theApp)
		// TODO(crawshaw): trigger runApp to return
	}()
	C.runApp()
	panic("unexpected return from app.runApp")
}

var pixelsPerPt float32
var screenScale int // [UIScreen mainScreen].scale, either 1, 2, or 3.

var DisplayMetrics struct {
	WidthPx  int
	HeightPx int
}

//export setWindowPtr
func setWindowPtr(window *C.void) {
	theApp.window = uintptr(unsafe.Pointer(window))
	log.Println("set window pointer to:", theApp.window)
}

//export setDisplayMetrics
func setDisplayMetrics(width, height int, scale int) {
	DisplayMetrics.WidthPx = width
	DisplayMetrics.HeightPx = height
}

//export setScreen
func setScreen(scale int) {
	C.uname(&C.sysInfo)
	name := C.GoString(&C.sysInfo.machine[0])

	var v float32

	switch {
	case strings.HasPrefix(name, "iPhone"):
		v = 163
	case strings.HasPrefix(name, "iPad"):
		// TODO: is there a better way to distinguish the iPad Mini?
		switch name {
		case "iPad2,5", "iPad2,6", "iPad2,7", "iPad4,4", "iPad4,5", "iPad4,6", "iPad4,7":
			v = 163 // iPad Mini
		default:
			v = 132
		}
	default:
		v = 163 // names like i386 and x86_64 are the simulator
	}

	if v == 0 {
		log.Printf("unknown machine: %s", name)
		v = 163 // emergency fallback
	}

	pixelsPerPt = v * float32(scale) / 72
	screenScale = scale
}

//export updateConfig
func updateConfig(width, height, orientation int32) {
	o := size.OrientationUnknown
	switch orientation {
	case C.UIDeviceOrientationPortrait, C.UIDeviceOrientationPortraitUpsideDown:
		o = size.OrientationPortrait
	case C.UIDeviceOrientationLandscapeLeft, C.UIDeviceOrientationLandscapeRight:
		o = size.OrientationLandscape
		width, height = height, width
	}
	insets := C.getDevicePadding()

	theApp.eventsIn <- size.Event{
		WidthPx:       int(width),
		HeightPx:      int(height),
		WidthPt:       geom.Pt(float32(width) / pixelsPerPt),
		HeightPt:      geom.Pt(float32(height) / pixelsPerPt),
		InsetTopPx:    int(float32(insets.top) * float32(screenScale)),
		InsetBottomPx: int(float32(insets.bottom) * float32(screenScale)),
		InsetLeftPx:   int(float32(insets.left) * float32(screenScale)),
		InsetRightPx:  int(float32(insets.right) * float32(screenScale)),
		PixelsPerPt:   pixelsPerPt,
		Orientation:   o,
		DarkMode:      bool(C.isDark()),
	}
	theApp.eventsIn <- paint.Event{External: true}
}

// touchIDs is the current active touches. The position in the array
// is the ID, the value is the UITouch* pointer value.
//
// It is widely reported that the iPhone can handle up to 5 simultaneous
// touch events, while the iPad can handle 11.
var touchIDs [11]uintptr

//export sendTouch
func sendTouch(cTouch, cTouchType uintptr, x, y float32) {
	id := -1
	for i, val := range touchIDs {
		if val == cTouch {
			id = i
			break
		}
	}
	if id == -1 {
		for i, val := range touchIDs {
			if val == 0 {
				touchIDs[i] = cTouch
				id = i
				break
			}
		}
		if id == -1 {
			panic("out of touchIDs")
		}
	}

	t := touch.Type(cTouchType)
	if t == touch.TypeEnd {
		// Clear all touchIDs when touch ends. The UITouch pointers are unique
		// at every multi-touch event. See:
		// https://github.com/fyne-io/fyne/issues/2407
		// https://developer.apple.com/documentation/uikit/touches_presses_and_gestures?language=objc
		for idx := range touchIDs {
			touchIDs[idx] = 0
		}
	}

	theApp.eventsIn <- touch.Event{
		X:        x,
		Y:        y,
		Sequence: touch.Sequence(id),
		Type:     t,
	}
}

//export lifecycleDead
func lifecycleDead() { theApp.sendLifecycle(lifecycle.StageDead) }

//export lifecycleAlive
func lifecycleAlive() { theApp.sendLifecycle(lifecycle.StageAlive) }

//export lifecycleVisible
func lifecycleVisible() { theApp.sendLifecycle(lifecycle.StageVisible) }

//export lifecycleFocused
func lifecycleFocused() { theApp.sendLifecycle(lifecycle.StageFocused) }

func cStringsForFilter(filter *FileFilter) (*C.char, *C.char) {
	mimes := strings.Join(filter.MimeTypes, "|")

	// extensions must have the '.' removed for UTI lookups on iOS
	extList := []string{}
	for _, ext := range filter.Extensions {
		extList = append(extList, ext[1:])
	}
	exts := strings.Join(extList, "|")

	return C.CString(mimes), C.CString(exts)
}

// driverShowVirtualKeyboard requests the driver to show a virtual keyboard for text input
func driverShowVirtualKeyboard(keyboard KeyboardType) {
	C.showKeyboard(C.int(int32(keyboard)))
}

// driverHideVirtualKeyboard requests the driver to hide any visible virtual keyboard
func driverHideVirtualKeyboard() {
	C.hideKeyboard()
}
