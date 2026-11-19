package telemetry

import "time"

// SetWallNow replaces the clock span ends are stamped from and returns a func
// that restores it.
func SetWallNow(f func() time.Time) (restore func()) {
	prev := wallNow
	wallNow = f
	return func() { wallNow = prev }
}
