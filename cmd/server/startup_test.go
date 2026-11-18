package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky-primitives/recoverykey"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/config"
)

// The start runServer makes, not the functions it calls: a replica holding a recovery.pub
// for another key refuses to start; one with no published key starts and says what to do.
func TestStartupRefusesARecoveryPubForAnotherKey(t *testing.T) {
	newCfg := replicaConfig(t)
	a := newCfg()
	stA := openStore(t, a)
	if err := prepareStart(context.Background(), a, stA); err != nil {
		t.Fatal(err)
	}
	priv, _ := recoverykey.Generate()
	key := recoveryclient.RecoveryKey{Public: priv.Public(), Threshold: 2, TotalShares: 3}
	if err := backup.PinRecoveryKey(context.Background(), a, stA, key); err != nil {
		t.Fatal(err)
	}

	b := newCfg()
	other, _ := recoverykey.Generate()
	if err := os.WriteFile(recoveryclient.RecoveryKeyPath(b.Database.DataDir), other.Public().Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareStart(context.Background(), b, openStore(t, b)); !errors.Is(err, recoveryclient.ErrKeyMismatch) {
		t.Fatalf("start with a recovery.pub for another key: %v, want ErrKeyMismatch", err)
	}

	// A pin nobody recorded (pinned before records existed, file gone): start, and log the fix.
	if err := stA.Settings().DeleteSetting(context.Background(), backup.RecoveryKeyRecordSetting); err != nil {
		t.Fatal(err)
	}
	c := newCfg()
	logs := captureLog(t)
	if err := prepareStart(context.Background(), c, openStore(t, c)); err != nil {
		t.Fatalf("a replica that can be repaired from the screen refused to start: %v", err)
	}
	if !strings.Contains(logs.String(), "pin it by hand") || !strings.Contains(logs.String(), key.Public.ID()) {
		t.Fatalf("startup did not say how to repair the missing key:\n%s", logs)
	}
}

// Startup removes the plaintext snapshot a killed process left, and nothing else.
func TestStartupRemovesLeftoverSnapshots(t *testing.T) {
	t.Setenv("KY_TEST_POSTGRES_DSN", "")
	t.Setenv("KY_DATA_DIR", t.TempDir())
	t.Setenv("KY_DB_DRIVER", "sqlite")
	cfg, err := config.LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database.DSN = filepath.Join(cfg.Database.DataDir, "ky_server.db")
	leftover := filepath.Join(cfg.Database.DataDir, "snapshot-123")
	if err := os.Mkdir(leftover, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leftover, "ky_server.db"), []byte("plaintext"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareStart(context.Background(), cfg, openStore(t, cfg)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("the leftover snapshot survived the start: %v", err)
	}
	if _, err := os.Stat(cfg.Database.DSN); err != nil {
		t.Fatalf("the live database: %v", err)
	}
}
