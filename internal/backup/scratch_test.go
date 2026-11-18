package backup

import (
	"os"
	"path/filepath"
	"testing"
)

// The sweep removes a dead process's snapshot, leaves one a live process holds, never
// follows a symlink out of the data directory, and ignores anything not named snapshot-*.
func TestRemoveStaleSnapshots(t *testing.T) {
	data := t.TempDir()
	plant := func(dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ky_server.db"), []byte("plaintext"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	dead := filepath.Join(data, "snapshot-dead")
	plant(dead)
	outside := filepath.Join(t.TempDir(), "elsewhere")
	plant(outside)
	link := filepath.Join(data, "snapshot-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(data, "drill")
	plant(other)
	live, release, err := snapshotScratch(data)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	plant(live)

	n, err := RemoveStaleSnapshots(data)
	if err != nil || n != 1 {
		t.Fatalf("removed %d, %v; want the one dead snapshot", n, err)
	}
	for path, want := range map[string]bool{dead: false, live: true, link: true, outside: true, other: true} {
		if _, err := os.Lstat(path); (err == nil) != want {
			t.Errorf("%s present=%v, want %v", path, err == nil, want)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "ky_server.db")); err != nil {
		t.Errorf("the sweep followed a symlink: %v", err)
	}
	release()
	if _, err := os.Stat(live); !os.IsNotExist(err) {
		t.Errorf("release left the scratch directory: %v", err)
	}
}

// Release removes the scratch directory; a data directory not created yet sweeps nothing.
func TestSnapshotScratchIsRemovedOnRelease(t *testing.T) {
	data := t.TempDir()
	dir, release, err := snapshotScratch(data)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("scratch survived its release: %v", err)
	}
	if n, err := RemoveStaleSnapshots(filepath.Join(data, "absent")); n != 0 || err != nil {
		t.Fatalf("a missing data directory: %d %v", n, err)
	}
}
