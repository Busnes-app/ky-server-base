package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

func openReplicas(t *testing.T) (store.Store, store.Store) {
	t.Helper()
	cfg := testdb.Config(t)
	a, err := store.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	b, err := store.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return a, b
}

func TestSharedSecurityAdmissionAndSingleUse(t *testing.T) {
	a, b := openReplicas(t)
	ctx := context.Background()
	var allowed, spent atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st := a
			if i%2 != 0 {
				st = b
			}
			_, ok, err := st.Coordination().TakeAttempt(ctx, "login:one-client", 10, time.Minute)
			if err != nil {
				t.Error(err)
			}
			if ok {
				allowed.Add(1)
			}
			ok, err = st.Coordination().SpendChallenge(ctx, "00112233445566778899aabbccddeeff", time.Now().Add(time.Minute).UnixMilli())
			if err != nil {
				t.Error(err)
			}
			if ok {
				spent.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if allowed.Load() != 10 {
		t.Fatalf("admitted %d, want 10 across both pools", allowed.Load())
	}
	if spent.Load() != 1 {
		t.Fatalf("spent %d times, want one across both pools", spent.Load())
	}
}

func TestRefundCannotChangeANewerWindow(t *testing.T) {
	a, b := openReplicas(t)
	ctx := context.Background()
	expires, ok, err := a.Coordination().TakeAttempt(ctx, "account:slow", 1, 20*time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("reserve: %v %v", ok, err)
	}
	time.Sleep(40 * time.Millisecond)
	_, ok, err = b.Coordination().TakeAttempt(ctx, "account:slow", 1, time.Minute)
	if err != nil || !ok {
		t.Fatalf("new window: %v %v", ok, err)
	}
	if err := a.Coordination().ReleaseAttempt(ctx, "account:slow", expires); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := b.Coordination().TakeAttempt(ctx, "account:slow", 1, time.Minute); err != nil || ok {
		t.Fatalf("stale refund admitted request: %v %v", ok, err)
	}
}

func TestWorkerOwnershipAndTakeover(t *testing.T) {
	a, b := openReplicas(t)
	ctx := context.Background()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := a.Coordination().WithLock(ctx, "election", func(context.Context) error { close(entered); <-release; return nil })
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no owner")
	}
	won, err := b.Coordination().WithLock(ctx, "election", func(context.Context) error { return fmt.Errorf("second owner entered") })
	if err != nil || won {
		t.Errorf("two owners: won=%v err=%v", won, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	won, err = b.Coordination().WithLock(ctx, "election", func(context.Context) error { return nil })
	if err != nil || !won {
		t.Fatalf("standby did not take over: %v %v", won, err)
	}
}

func TestLostPostgresSessionCancelsWorker(t *testing.T) {
	cfg := testdb.Config(t)
	if cfg.Driver != "postgres" {
		t.Skip("requires PostgreSQL")
	}
	name := "worker-" + fmt.Sprint(time.Now().UnixNano())
	cfg.DSN += "&application_name=" + name
	ctx := context.Background()
	a, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	admin, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := a.Coordination().WithLock(ctx, "crash-election", func(owned context.Context) error { close(entered); <-owned.Done(); return owned.Err() })
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no elected owner")
	}
	var terminated bool
	if err := admin.QueryRowContext(ctx, `SELECT pg_terminate_backend(l.pid) FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid WHERE l.locktype='advisory' AND l.classid=3 AND a.application_name=$1`, name).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("terminate owner: %v %v", terminated, err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("worker did not observe lost ownership: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("lost ownership did not cancel work")
	}
	won, err := b.Coordination().WithLock(ctx, "crash-election", func(context.Context) error { return nil })
	if err != nil || !won {
		t.Fatalf("standby could not take over after a crash: %v %v", won, err)
	}
}

func TestReplicaStartsSerializeMigrations(t *testing.T) {
	cfg := testdb.Config(t)
	if cfg.Driver != "postgres" {
		t.Skip("PostgreSQL startup coordination")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := store.Open(ctx, cfg)
			if err != nil {
				t.Error(err)
				return
			}
			st.Close()
		}()
	}
	wg.Wait()
}

func TestSQLiteInstanceAndMigrationExcludeEachOther(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.db")
	_, err := store.WithSQLiteInstance(context.Background(), path, func(ctx context.Context) error {
		won, err := store.WithSQLiteInstance(ctx, path, func(context.Context) error { t.Error("second instance entered"); return nil })
		if won || err != nil {
			t.Fatalf("second instance: %v %v", won, err)
		}
		if _, err := store.MigrateToPostgres(ctx, path, config.DatabaseConfig{Driver: "postgres"}, false); err == nil {
			t.Fatal("migration entered a running instance")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
