package host

import "time"

// SetCheckTimeout shortens the per-file budget for the test that has to watch
// it expire. It is a test hook rather than configuration: a timeout a manifest
// could raise is a timeout somebody raises until the hang stops being
// reported.
func SetCheckTimeout(d time.Duration) (restore func()) {
	old := checkTimeout
	checkTimeout = d
	return func() { checkTimeout = old }
}
