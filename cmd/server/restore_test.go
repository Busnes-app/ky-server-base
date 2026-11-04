package main

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/ky-primitives/capsule"
	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky-primitives/recoverykey"
)

func sealFixture(t *testing.T, service string) (string, []string) {
	t.Helper()
	return sealFiles(t, service, []recoveryclient.File{{Path: "data/x.db", Data: []byte("payload"), Mode: 0600}})
}

func sealFiles(t *testing.T, service string, files []recoveryclient.File) (string, []string) {
	t.Helper()
	priv, err := recoverykey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	shares, err := recoverykey.Split(priv, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	key := recoveryclient.RecoveryKey{Public: priv.Public(), Threshold: 2, TotalShares: 3}
	payload := recoveryclient.Payload{ServiceName: service, AppVersion: "1.0.0", Files: files}
	raw, _, err := recoveryclient.Seal(payload, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "x.kycap")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	// Non-consecutive indices: {1,2} would let an XOR-shaped bug pass.
	return path, []string{shares[0].String(), shares[2].String()}
}

func TestRestoreExtractsWithTwoShares(t *testing.T) {
	path, shares := sealFixture(t, "busnes_app")
	target := t.TempDir()
	var out bytes.Buffer
	if err := restore(path, target, "busnes_app", shares, &out); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(target, "data", "x.db"))
	if err != nil || string(got) != "payload" {
		t.Fatalf("restored file: %q %v", got, err)
	}
	if !strings.Contains(out.String(), "busnes_app") {
		t.Fatalf("manifest not printed: %s", out.String())
	}
}

func TestRestoreRefusesAnotherService(t *testing.T) {
	path, shares := sealFixture(t, "someone_else")
	err := restore(path, t.TempDir(), "busnes_app", shares, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "someone_else") {
		t.Fatalf("got %v, want a service-name refusal naming the capsule's service", err)
	}
}

func TestRestoreRefusesTheWrongKit(t *testing.T) {
	path, _ := sealFixture(t, "busnes_app")
	_, otherShares := sealFixture(t, "busnes_app")
	err := restore(path, t.TempDir(), "busnes_app", otherShares, &bytes.Buffer{})
	if !errors.Is(err, capsule.ErrWrongRecoveryKey) {
		t.Fatalf("got %v, want ErrWrongRecoveryKey", err)
	}
}

func TestRestoreRefusesOneShare(t *testing.T) {
	path, shares := sealFixture(t, "busnes_app")
	if err := restore(path, t.TempDir(), "busnes_app", shares[:1], &bytes.Buffer{}); err == nil {
		t.Fatal("one share of a 2-of-3 kit was accepted")
	}
}

// A restored copy may be a drill beside the live server: it must not deposit on its own,
// even when the old server had a schedule saved.
func TestRestoreTurnsScheduledDepositsOff(t *testing.T) {
	src := filepath.Join(t.TempDir(), "ky_server.db")
	db, err := sql.Open("sqlite", src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE server_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '', updated_at DATETIME NOT NULL);
INSERT INTO server_settings VALUES ('backup_interval_sec', '86400', '2026-01-01 00:00:00');`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	path, shares := sealFiles(t, "busnes_app", []recoveryclient.File{{Path: "data/ky_server.db", Data: raw, Mode: 0600}})
	target := t.TempDir()
	var out bytes.Buffer
	if err := restore(path, target, "busnes_app", shares, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Scheduled deposits are OFF") {
		t.Fatalf("restore does not say deposits are off: %s", out.String())
	}
	db, err = sql.Open("sqlite", filepath.Join(target, "data", "ky_server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v string
	if err := db.QueryRow(`SELECT value FROM server_settings WHERE key = 'backup_interval_sec'`).Scan(&v); err != nil || v != "0" {
		t.Fatalf("backup_interval_sec = %q, %v; want 0", v, err)
	}
	entries, _ := os.ReadDir(filepath.Join(target, "data"))
	if len(entries) != 1 {
		t.Fatalf("restore left extra files beside the database: %v", entries)
	}
}
