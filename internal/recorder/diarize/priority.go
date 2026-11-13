package diarize

import "runtime"

// LowPriority runs f on an OS thread of its own at low priority (see
// lowerThreadPriority), for background work that must not slow a call:
// the thread is given back as RefineCuts gives back its own.
func LowPriority(f func()) {
	runtime.LockOSThread()
	defer releaseThread()
	lowerThreadPriority()
	f()
}
