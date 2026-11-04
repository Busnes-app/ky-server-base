package backup

// SetMaxSnapshotBytes lowers the snapshot limit for a test and returns the restore func.
func SetMaxSnapshotBytes(n int64) func() {
	old := maxSnapshotBytes
	maxSnapshotBytes = n
	return func() { maxSnapshotBytes = old }
}
