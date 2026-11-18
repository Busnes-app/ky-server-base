package store

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// Database time, rather than a replica's wall clock, defines every shared window.
func (s *SQLStore) nowMillis() string {
	if s.driver == "postgres" {
		return "CAST(EXTRACT(EPOCH FROM statement_timestamp()) * 1000 AS BIGINT)"
	}
	return "CAST(unixepoch('subsec') * 1000 AS INTEGER)"
}

func (s *SQLStore) TakeAttempt(ctx context.Context, key string, limit int, window time.Duration) (int64, bool, error) {
	if len(key) == 0 || len(key) > 200 || limit < 1 || window < time.Millisecond || window > 24*time.Hour {
		return 0, false, errors.New("invalid security window")
	}
	now := s.nowMillis()
	q := `INSERT INTO security_windows (key, hits, expires_at) VALUES (?, 1, ` + now + ` + ?)
ON CONFLICT (key) DO UPDATE SET
hits = CASE WHEN security_windows.expires_at <= ` + now + ` THEN 1
            WHEN security_windows.hits < ? THEN security_windows.hits + 1 ELSE security_windows.hits END,
expires_at = CASE WHEN security_windows.expires_at <= ` + now + ` THEN ` + now + ` + ? ELSE security_windows.expires_at END
RETURNING hits, expires_at`
	var hits, expires int64
	err := s.db.QueryRowContext(ctx, s.rebind(q), key, window.Milliseconds(), limit+1, window.Milliseconds()).Scan(&hits, &expires)
	return expires, err == nil && hits <= int64(limit), err
}

// The expiry is a generation: a slow successful login cannot refund a newer window.
func (s *SQLStore) ReleaseAttempt(ctx context.Context, key string, expires int64) error {
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE security_windows SET hits = hits - 1 WHERE key = ? AND expires_at = ? AND hits > 0`), key, expires)
	return err
}

func (s *SQLStore) SpendChallenge(ctx context.Context, salt string, expires int64) (bool, error) {
	if len(salt) != 32 {
		return false, errors.New("invalid challenge salt")
	}
	q := `INSERT INTO spent_challenges (salt, expires_at) SELECT ?, ? WHERE ? >= ` + s.nowMillis() + `
ON CONFLICT (salt) DO NOTHING`
	res, err := s.db.ExecContext(ctx, s.rebind(q), salt, expires, expires)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *SQLStore) CleanExpired(ctx context.Context) error {
	for _, table := range []string{"security_windows", "spent_challenges"} {
		if _, err := s.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE expires_at < "+s.nowMillis()); err != nil {
			return err
		}
	}
	return nil
}

// PostgreSQL session advisory locks elect one owner without Kubernetes or a lease clock.
// Use a direct connection or session pooling, never transaction pooling. The reserved
// connection probes ownership; callbacks use the ordinary pool for their own queries.
func (s *SQLStore) WithLock(ctx context.Context, name string, work func(context.Context) error) (bool, error) {
	if s.driver != "postgres" {
		// SQLite is a single-host deployment. Closing the descriptor releases ownership,
		// including after a crash; do not unlink the persistent lock inode.
		path := fmt.Sprintf("%s.%x.lock", s.lockPath, sha256.Sum256([]byte(name)))
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return false, err
		}
		defer f.Close()
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			if errors.Is(err, unix.EWOULDBLOCK) {
				return false, nil
			}
			return false, err
		}
		return true, work(ctx)
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	// Discard the physical connection, never return a session lock to the pool, even if
	// unlocking fails or ctx has already ended. Closing the session releases every lock.
	defer conn.Close()
	defer conn.Raw(func(any) error { return driver.ErrBadConn })
	var won bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(3, hashtext(current_schema() || ':' || $1))`, name).Scan(&won); err != nil || !won {
		return false, err
	}
	owned, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-owned.Done():
				return
			case <-ticker.C:
				probe, stop := context.WithTimeout(owned, time.Second)
				err := conn.PingContext(probe)
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	err = work(owned)
	cancel()
	<-done
	return true, err
}

// WithSQLiteInstance excludes a second local server and the migration command. File locks
// are host-local: SQLite files must never be shared by replicas over a network filesystem.
func WithSQLiteInstance(ctx context.Context, path string, work func(context.Context) error) (bool, error) {
	canonical, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	if resolved, err := filepath.EvalSymlinks(canonical); err == nil {
		canonical = resolved
	}
	s := &SQLStore{driver: "sqlite", lockPath: canonical}
	return s.WithLock(ctx, "server-instance", work)
}
