package main

/*
#cgo LDFLAGS: -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
static void runMainLoop(void) { CFRunLoopRun(); }
static void stopMainLoop(void) { CFRunLoopStop(CFRunLoopGetMain()); }
*/
import "C"

import "runtime"

// Pin the main goroutine to the main OS thread so CFRunLoopRun services the
// process's main run loop / main dispatch queue (what NSApplication.run does
// in Tart even with --no-graphics).
func init() { runtime.LockOSThread() }

func runMainLoop()  { C.runMainLoop() }
func stopMainLoop() { C.stopMainLoop() }
