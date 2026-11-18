package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// snapshotPrefix names the private scratch directories that hold a plaintext database
// snapshot while it is sealed.
const snapshotPrefix = "snapshot-"

// snapshotScratch makes a 0700 scratch directory under dataDir and holds an exclusive lock on
// it until release, so RemoveStaleSnapshots never deletes one a running process still uses.
func snapshotScratch(dataDir string) (string, func(), error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(dataDir, snapshotPrefix+"*")
	if err != nil {
		return "", nil, err
	}
	release := func() { _ = os.RemoveAll(dir) }
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == nil {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		// A sweep that won the directory between MkdirTemp and the lock has removed it.
		if err == nil {
			err = sameDir(fd, dir)
		}
	}
	if err != nil {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		release()
		return "", nil, fmt.Errorf("backup: snapshot scratch: %w", err)
	}
	var once sync.Once
	return dir, func() { once.Do(func() { release(); _ = unix.Close(fd) }) }, nil
}

// RemoveStaleSnapshots deletes the snapshot scratch directories a killed process left under
// dataDir: plaintext copies of the database nothing will remove otherwise. Only real
// directories owned by this user whose lock is free are removed; symlinks are never
// followed, and a directory another process holds is left alone.
func RemoveStaleSnapshots(dataDir string) (int, error) {
	entries, err := os.ReadDir(dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	var errs []error
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), snapshotPrefix) || e.Type() != os.ModeDir {
			continue
		}
		ok, err := removeIfStale(filepath.Join(dataDir, e.Name()))
		if err != nil {
			errs = append(errs, err)
		} else if ok {
			removed++
		}
	}
	return removed, errors.Join(errs...)
}

func removeIfStale(dir string) (bool, error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return false, err
	}
	if int(st.Uid) != os.Geteuid() {
		return false, nil
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return false, nil // in use
		}
		return false, err
	}
	// The path must still name the directory locked; RemoveAll does not follow symlinks.
	if err := sameDir(fd, dir); err != nil {
		return false, err
	}
	return true, os.RemoveAll(dir)
}

// sameDir reports an error unless dir is still the directory open as fd.
func sameDir(fd int, dir string) error {
	var a, b unix.Stat_t
	if err := unix.Fstat(fd, &a); err != nil {
		return err
	}
	if err := unix.Lstat(dir, &b); err != nil {
		return err
	}
	if a.Dev != b.Dev || a.Ino != b.Ino {
		return fmt.Errorf("%s was replaced", dir)
	}
	return nil
}
