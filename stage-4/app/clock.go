package app

import "time"

// clockNow is the one wall clock behind every history-relevant time: event stamps, the
// expiry sweep, the start of a read, correction "not later than now" checks and reset.
// Tests pin it with SetClock; the service never changes it.
var clockNow = func() int64 { return time.Now().UnixNano() }

// SetClock replaces the wall clock (tests only) and returns a function restoring it.
func SetClock(f func() int64) (restore func()) {
	old := clockNow
	clockNow = f
	return func() { clockNow = old }
}
