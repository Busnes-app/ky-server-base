package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky-primitives/recoverykey"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

// pinnedSQLite is a SQLite deployment pinned the way releases before the record did it:
// recovery.pub and the pin rows, no recovery_public_key row.
func pinnedSQLite(t *testing.T) (string, recoveryclient.RecoveryKey) {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "ky_server.db")
	st, err := store.Open(context.Background(), config.DatabaseConfig{Driver: "sqlite", DSN: source})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	priv, _ := recoverykey.Generate()
	key := recoveryclient.RecoveryKey{Public: priv.Public(), Threshold: 2, TotalShares: 3}
	if err := recoveryclient.StoreRecoveryKey(dir, backup.Settings(context.Background(), st.Settings()), key); err != nil {
		t.Fatal(err)
	}
	return source, key
}

func postgresTarget(t *testing.T) config.DatabaseConfig {
	t.Helper()
	target := testdb.Config(t)
	if target.Driver != "postgres" {
		t.Skip("needs KY_TEST_POSTGRES_DSN")
	}
	t.Setenv("KY_MIGRATE_POSTGRES_DSN", target.DSN)
	return target
}

// Migrating a source pinned before records existed records its recovery.pub, so a replica
// with an empty data directory writes the same file instead of reporting no key.
func TestMigratePostgresRecordsThePinnedKey(t *testing.T) {
	target := postgresTarget(t)
	source, key := pinnedSQLite(t)
	var out bytes.Buffer
	if err := migratePostgres(context.Background(), []string{"--source", source}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Recorded the pinned recovery public key") {
		t.Fatalf("output: %s", out.String())
	}
	cfg := &config.Config{}
	cfg.Database = target
	cfg.Database.DataDir = t.TempDir()
	st := openStore(t, cfg)
	if err := backup.SyncRecoveryKey(context.Background(), cfg, st); err != nil {
		t.Fatal(err)
	}
	got, err := recoveryclient.LoadRecoveryKey(cfg.Database.DataDir, backup.Settings(context.Background(), st.Settings()))
	if err != nil || got.Public.ID() != key.Public.ID() || got.Threshold != 2 || got.TotalShares != 3 {
		t.Fatalf("replica key after migration: %v %v", got, err)
	}
}

// A source that pins a key its directory no longer holds is refused before anything is
// copied, with the repair, rather than migrated into replicas that silently skip backups.
func TestMigratePostgresRefusesAPinWithoutItsFile(t *testing.T) {
	target := postgresTarget(t)
	source, key := pinnedSQLite(t)
	if err := os.Remove(recoveryclient.RecoveryKeyPath(filepath.Dir(source))); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--check"}, {}} {
		var out bytes.Buffer
		err := migratePostgres(context.Background(), append([]string{"--source", source}, args...), &out)
		if !errors.Is(err, backup.ErrRecoveryKeyUnavailable) || !strings.Contains(err.Error(), key.Public.ID()) || !strings.Contains(err.Error(), "by hand") {
			t.Fatalf("migrate %v: %v", args, err)
		}
	}
	db, err := sql.Open("pgx", target.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM pg_tables WHERE schemaname = current_schema()`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("the refused migration touched the target: %d tables, %v", tables, err)
	}
}
