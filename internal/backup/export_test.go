package backup

import "context"

// SetMaxSnapshotBytes lowers the snapshot limit for a test and returns the restore func.
func SetMaxSnapshotBytes(n int64) func() {
	old := maxSnapshotBytes
	maxSnapshotBytes = n
	return func() { maxSnapshotBytes = old }
}

// OnSnapshotForTest calls f with the scratch directory while it holds a PostgreSQL snapshot,
// and returns the restore func.
func OnSnapshotForTest(f func(dir string)) func() {
	snapshotTaken = f
	return func() { snapshotTaken = nil }
}

// OnPinLockedForTest calls f with the pin lock's context before a pin writes, and returns the
// restore func.
func OnPinLockedForTest(f func(owned context.Context)) func() {
	pinLocked = f
	return func() { pinLocked = nil }
}

// PinRows are the lib's pin setting names, for tests that check them directly.
var PinRows = [3]string{pinKeyID, pinThreshold, pinTotalShares}
