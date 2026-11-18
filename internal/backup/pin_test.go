package backup_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky-primitives/recoverykey"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

type mapSettings map[string]string

func (m mapSettings) Get(k string) (string, error) {
	if v, ok := m[k]; ok {
		return v, nil
	}
	return "", recoveryclient.ErrNotFound
}
func (m mapSettings) Set(k, v string) error { m[k] = v; return nil }
func (m mapSettings) Delete(k string) error { delete(m, k); return nil }

// The pin rows this package reads directly are the ones the lib writes.
func TestPinRowNames(t *testing.T) {
	priv, _ := recoverykey.Generate()
	s := mapSettings{}
	if err := recoveryclient.StoreRecoveryKey(t.TempDir(), s, recoveryclient.RecoveryKey{Public: priv.Public(), Threshold: 2, TotalShares: 3}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{backup.PinRows[0]: priv.Public().ID(), backup.PinRows[1]: "2", backup.PinRows[2]: "3"}
	if len(s) != len(want) {
		t.Fatalf("the lib wrote %v", s)
	}
	for k, v := range want {
		if s[k] != v {
			t.Fatalf("pin row %s = %q, want %q (lib wrote %v)", k, s[k], v, s)
		}
	}
}

// replica is another server on the same PostgreSQL database with its own data directory.
func replica(t *testing.T, cfg *config.Config) (*config.Config, store.Store) {
	t.Helper()
	c := *cfg
	c.Database.DataDir = t.TempDir()
	st, err := store.Open(context.Background(), c.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &c, st
}

func newKey(t *testing.T, k, n int) recoveryclient.RecoveryKey {
	t.Helper()
	priv, err := recoverykey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return recoveryclient.RecoveryKey{Public: priv.Public(), Threshold: k, TotalShares: n}
}

// assertPinned checks the pin rows, the record and a replica's recovery.pub all name k.
func assertPinned(t *testing.T, st store.Store, k recoveryclient.RecoveryKey, dirs ...string) {
	t.Helper()
	s := backup.Settings(context.Background(), st.Settings())
	want := []string{k.Public.ID(), strconv.Itoa(k.Threshold), strconv.Itoa(k.TotalShares)}
	for i, row := range backup.PinRows {
		if v, _ := s.Get(row); v != want[i] {
			t.Errorf("pin row %s = %q, want %q", row, v, want[i])
		}
	}
	probe := mapSettings{}
	if err := backup.RecordRecoveryKey(probe, k); err != nil {
		t.Fatal(err)
	}
	if rec, _ := s.Get(backup.RecoveryKeyRecordSetting); rec != probe[backup.RecoveryKeyRecordSetting] {
		t.Errorf("record %s, want the pinned key's", rec)
	}
	for _, dir := range dirs {
		raw, err := os.ReadFile(recoveryclient.RecoveryKeyPath(dir))
		if err != nil || !bytes.Equal(raw, k.Public.Bytes()) {
			t.Errorf("%s/recovery.pub is not the pinned key: %v", dir, err)
		}
	}
}

// Replicas pinning different keys at once: exactly one wins, every other gets the write-once
// refusal, and the id, topology, record and files all name the winner. A loser leaves no
// recovery.pub behind for its own key.
func TestConcurrentPinsOnReplicasPinOneKey(t *testing.T) {
	for round := range 3 {
		cfg, st := postgresInstance(t)
		// Hold the lock a moment, so every other replica contends for it, and catch two pins
		// inside at once (the lib's read-then-write is only safe one at a time).
		var inside, overlapped atomic.Int32
		defer backup.OnPinLockedForTest(func(context.Context) {
			if inside.Add(1) > 1 {
				overlapped.Store(1)
			}
			time.Sleep(50 * time.Millisecond)
			inside.Add(-1)
		})()
		type replicaPin struct {
			cfg *config.Config
			st  store.Store
			key recoveryclient.RecoveryKey
			err error
		}
		pins := make([]*replicaPin, 4)
		for i := range pins {
			c, s := replica(t, cfg)
			pins[i] = &replicaPin{cfg: c, st: s, key: newKey(t, 2+i%2, 3+i)}
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, p := range pins {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				p.err = backup.PinRecoveryKey(context.Background(), p.cfg, p.st, p.key)
			}()
		}
		close(start)
		wg.Wait()
		if overlapped.Load() != 0 {
			t.Fatalf("round %d: two replicas were pinning at once", round)
		}
		var winner *replicaPin
		for _, p := range pins {
			switch {
			case p.err == nil && winner == nil:
				winner = p
			case p.err == nil:
				t.Fatalf("round %d: two different keys were both pinned", round)
			case !errors.Is(p.err, fs.ErrExist):
				t.Fatalf("round %d: loser got %v, want the write-once refusal", round, p.err)
			default:
				if _, err := os.Lstat(recoveryclient.RecoveryKeyPath(p.cfg.Database.DataDir)); !os.IsNotExist(err) {
					t.Errorf("round %d: a losing replica kept a recovery.pub: %v", round, err)
				}
			}
		}
		if winner == nil {
			t.Fatalf("round %d: no pin won", round)
		}
		assertPinned(t, st, winner.key, winner.cfg.Database.DataDir)
		// Every loser now materializes the winner's key.
		for _, p := range pins {
			if err := backup.SyncRecoveryKey(context.Background(), p.cfg, p.st); err != nil {
				t.Fatalf("round %d: sync: %v", round, err)
			}
			assertPinned(t, st, winner.key, p.cfg.Database.DataDir)
		}
	}
}

// A pin whose lock session dies before it writes reports the loss and writes nothing; the
// key stays free for the next pin.
func TestPinFailsClosedWhenTheLockIsLost(t *testing.T) {
	cfg, st := postgresInstance(t)
	db, err := sql.Open("pgx", cfg.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var once sync.Once
	defer backup.OnPinLockedForTest(func(owned context.Context) {
		once.Do(func() {
			var n int
			if err := db.QueryRow(`SELECT count(pg_terminate_backend(l.pid)) FROM pg_locks l
WHERE l.locktype = 'advisory' AND l.granted AND l.classid = 3
AND l.objid::bigint = (hashtext(current_schema() || ':recovery-pin')::bigint + 4294967296) % 4294967296`).Scan(&n); err != nil || n != 1 {
				t.Errorf("terminating the lock session: %d, %v", n, err)
			}
			select {
			case <-owned.Done():
			case <-time.After(5 * time.Second):
				t.Error("the lock's context outlived its session")
			}
		})
	})()
	key := newKey(t, 2, 3)
	if err := backup.PinRecoveryKey(context.Background(), cfg, st, key); !errors.Is(err, backup.ErrPinOwnershipLost) {
		t.Fatalf("pin across a lost lock: %v, want ErrPinOwnershipLost", err)
	}
	s := backup.Settings(context.Background(), st.Settings())
	for _, row := range append(backup.PinRows[:], backup.RecoveryKeyRecordSetting) {
		if v, err := s.Get(row); !errors.Is(err, recoveryclient.ErrNotFound) {
			t.Errorf("%s written across a lost lock: %q %v", row, v, err)
		}
	}
	if _, err := os.Lstat(recoveryclient.RecoveryKeyPath(cfg.Database.DataDir)); !os.IsNotExist(err) {
		t.Errorf("recovery.pub written across a lost lock: %v", err)
	}
	other := newKey(t, 3, 5)
	if err := backup.PinRecoveryKey(context.Background(), cfg, st, other); err != nil {
		t.Fatalf("the next pin: %v", err)
	}
	assertPinned(t, st, other, cfg.Database.DataDir)
}

type pinReadStore struct {
	store.Store
	settings store.SettingsStore
}

func (s pinReadStore) Settings() store.SettingsStore { return s.settings }

type pinReadSettings struct {
	store.SettingsStore
	afterRead func()
}

func (s pinReadSettings) GetSetting(ctx context.Context, key string) (string, error) {
	v, err := s.SettingsStore.GetSetting(ctx, key)
	if key == backup.PinRows[0] {
		s.afterRead()
	}
	return v, err
}

// Kill the owner after its stale read, then let a new owner finish before the heartbeat
// can cancel the old one. Write-once must hold even while the old context is still live.
func TestPinCannotOverwriteTheWinnerAfterItsLockIsLost(t *testing.T) {
	cfg, st := postgresInstance(t)
	db, err := sql.Open("pgx", cfg.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfgB, stB := replica(t, cfg)
	a, b := newKey(t, 2, 3), newKey(t, 3, 5)
	var once sync.Once
	settings := pinReadSettings{SettingsStore: st.Settings(), afterRead: func() {
		once.Do(func() {
			var n int
			err := db.QueryRow(`SELECT count(pg_terminate_backend(l.pid)) FROM pg_locks l
WHERE l.locktype = 'advisory' AND l.granted AND l.classid = 3
AND l.objid::bigint = (hashtext(current_schema() || ':recovery-pin')::bigint + 4294967296) % 4294967296`).Scan(&n)
			if err != nil || n != 1 {
				t.Fatalf("terminate stale owner's lock: n=%d err=%v", n, err)
			}
			if err := backup.PinRecoveryKey(context.Background(), cfgB, stB, b); err != nil {
				t.Fatalf("new owner pin: %v", err)
			}
		})
	}}
	err = backup.PinRecoveryKey(context.Background(), cfg, pinReadStore{st, settings}, a)
	if err == nil {
		t.Fatal("stale owner overwrote a key another replica successfully pinned")
	}
	assertPinned(t, st, b, cfgB.Database.DataDir)
	if _, err := os.Lstat(recoveryclient.RecoveryKeyPath(cfg.Database.DataDir)); !os.IsNotExist(err) {
		t.Fatalf("stale owner left its losing key file: %v", err)
	}
	if err := backup.SyncRecoveryKey(context.Background(), cfg, st); err != nil {
		t.Fatal(err)
	}
	assertPinned(t, st, b, cfg.Database.DataDir)
}

func TestPinnedTopologyCannotChange(t *testing.T) {
	cfg, st := postgresInstance(t)
	k := newKey(t, 2, 3)
	if err := backup.PinRecoveryKey(context.Background(), cfg, st, k); err != nil {
		t.Fatal(err)
	}
	cfgB, stB := replica(t, cfg)
	changed := k
	changed.Threshold, changed.TotalShares = 3, 5
	if err := backup.PinRecoveryKey(context.Background(), cfgB, stB, changed); !errors.Is(err, recoveryclient.ErrKeyMismatch) {
		t.Fatalf("changed pinned topology: %v", err)
	}
	assertPinned(t, st, k, cfg.Database.DataDir)
}

// A pin made before records existed is backfilled by the replica holding its file, while
// another replica tries to pin a different key: the old pin stays and becomes the record.
func TestSyncBackfillRacesAPin(t *testing.T) {
	cfg, st := postgresInstance(t)
	legacy := newKey(t, 2, 3)
	if err := recoveryclient.StoreRecoveryKey(cfg.Database.DataDir, backup.Settings(context.Background(), st.Settings()), legacy); err != nil {
		t.Fatal(err)
	}
	cfgB, stB := replica(t, cfg)
	var syncErr, pinErr error
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(2)
	go func() { defer wg.Done(); <-start; syncErr = backup.SyncRecoveryKey(context.Background(), cfg, st) }()
	go func() {
		defer wg.Done()
		<-start
		pinErr = backup.PinRecoveryKey(context.Background(), cfgB, stB, newKey(t, 3, 5))
	}()
	close(start)
	wg.Wait()
	if syncErr != nil || !errors.Is(pinErr, fs.ErrExist) {
		t.Fatalf("sync %v, pin %v; want the backfill to succeed and the new key refused", syncErr, pinErr)
	}
	cfgC, stC := replica(t, cfg)
	if err := backup.SyncRecoveryKey(context.Background(), cfgC, stC); err != nil {
		t.Fatal(err)
	}
	assertPinned(t, st, legacy, cfg.Database.DataDir, cfgC.Database.DataDir)
}

// Fresh replicas starting together all write the same recovery.pub.
func TestConcurrentMaterializationAgrees(t *testing.T) {
	cfg, st := postgresInstance(t)
	key := newKey(t, 2, 3)
	if err := backup.PinRecoveryKey(context.Background(), cfg, st, key); err != nil {
		t.Fatal(err)
	}
	var dirs []string
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		c, s := replica(t, cfg)
		dirs = append(dirs, c.Database.DataDir)
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = backup.SyncRecoveryKey(context.Background(), c, s) }()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	assertPinned(t, st, key, dirs...)
}

// States a replica must not guess its way out of: each is ErrRecoveryKeyUnavailable with the
// repair, writes nothing, and a backup reports it instead of skipping as "not paired".
func TestUnavailablePinIsReportedNotGuessed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, s recoveryclient.Settings, key recoveryclient.RecoveryKey)
	}{
		{"pin with no record and no file", func(t *testing.T, s recoveryclient.Settings, key recoveryclient.RecoveryKey) {
			if err := recoveryclient.StoreRecoveryKey(t.TempDir(), s, key); err != nil {
				t.Fatal(err)
			}
		}},
		{"record with no pin", func(t *testing.T, s recoveryclient.Settings, key recoveryclient.RecoveryKey) {
			if err := backup.RecordRecoveryKey(s, key); err != nil {
				t.Fatal(err)
			}
		}},
		{"record naming another key", func(t *testing.T, s recoveryclient.Settings, key recoveryclient.RecoveryKey) {
			if err := recoveryclient.StoreRecoveryKey(t.TempDir(), s, key); err != nil {
				t.Fatal(err)
			}
			if err := backup.RecordRecoveryKey(s, newKey(t, 2, 3)); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, st := postgresInstance(t)
			cfg.Backup.Dir = filepath.Join(t.TempDir(), "capsules")
			cfg.Backup.Keep = 2
			s := backup.Settings(context.Background(), st.Settings())
			key := newKey(t, 2, 3)
			tc.setup(t, s, key)
			before, _ := st.Settings().GetAllSettings(context.Background())
			err := backup.SyncRecoveryKey(context.Background(), cfg, st)
			if !errors.Is(err, backup.ErrRecoveryKeyUnavailable) || !bytes.Contains([]byte(err.Error()), []byte("by hand")) {
				t.Fatalf("sync: %v, want ErrRecoveryKeyUnavailable naming the repair", err)
			}
			if _, err := os.Lstat(recoveryclient.RecoveryKeyPath(cfg.Database.DataDir)); !os.IsNotExist(err) {
				t.Errorf("recovery.pub was guessed: %v", err)
			}
			after, _ := st.Settings().GetAllSettings(context.Background())
			for k, v := range before {
				if after[k] != v {
					t.Errorf("setting %s changed", k)
				}
			}
			rc, err := backup.RunConfig(cfg, "test")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := backup.Run(context.Background(), cfg, rc, st, &recordingDepositor{}); !errors.Is(err, backup.ErrRecoveryKeyUnavailable) {
				t.Fatalf("backup run: %v, want ErrRecoveryKeyUnavailable, not a silent skip", err)
			}
		})
	}
}

// A replica whose file is the pin repairs a record that disagrees with it.
func TestSyncRewritesAStaleRecordFromThePinnedFile(t *testing.T) {
	cfg, st := postgresInstance(t)
	key := newKey(t, 2, 3)
	if err := backup.PinRecoveryKey(context.Background(), cfg, st, key); err != nil {
		t.Fatal(err)
	}
	s := backup.Settings(context.Background(), st.Settings())
	if err := backup.RecordRecoveryKey(s, newKey(t, 3, 5)); err != nil {
		t.Fatal(err)
	}
	if err := backup.SyncRecoveryKey(context.Background(), cfg, st); err != nil {
		t.Fatal(err)
	}
	assertPinned(t, st, key, cfg.Database.DataDir)
}
