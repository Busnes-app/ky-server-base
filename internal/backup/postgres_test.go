package backup_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/capsule"
	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky-primitives/recoverykey"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

func postgresInstance(t *testing.T) (*config.Config, store.Store) {
	t.Helper()
	db := testdb.Config(t)
	if db.Driver != "postgres" {
		t.Skip("needs KY_TEST_POSTGRES_DSN")
	}
	db.DataDir = t.TempDir()
	cfg := &config.Config{}
	cfg.Server.AppName = config.DefaultAppName
	cfg.Server.Port = 8080
	cfg.Database = db
	cfg.Security.EncryptionKey = bytes.Repeat([]byte{1}, 32)
	st, err := store.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return cfg, st
}

// recordingDepositor is KyRecovery as a blind store: it keeps the bytes it was sent.
type recordingDepositor struct{ raw []byte }

func (d *recordingDepositor) Deposit(_ context.Context, _, _ string, raw []byte) (recoveryclient.Receipt, error) {
	d.raw = append([]byte(nil), raw...)
	m, err := capsule.ReadUnverifiedManifest(raw)
	if err != nil {
		return recoveryclient.Receipt{}, err
	}
	// Enforce the KyRecovery claim/deposit service-name contract, not just kycap syntax.
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`).MatchString(m.ServiceName) || strings.Contains(m.ServiceName, "..") {
		return recoveryclient.Receipt{}, fmt.Errorf("KyRecovery refuses service name %q", m.ServiceName)
	}
	sum := sha256.Sum256(raw)
	return recoveryclient.Receipt{CapsuleID: m.CapsuleID, Digest: hex.EncodeToString(sum[:]), SizeBytes: int64(len(raw)), DepositedAt: time.Now().UTC()}, nil
}

// A PostgreSQL deposit hands KyRecovery a kycap/3 container and nothing readable: neither the
// SQLite snapshot nor a value from it is in the bytes, the snapshot sat in a 0700 scratch
// directory, and that directory is gone once the run ends.
func TestPostgresDepositSendsOnlyASealedCapsule(t *testing.T) {
	cfg, st := postgresInstance(t)
	ctx := context.Background()
	const marker = "plaintext-marker-7f3a9c"
	if err := st.Users().CreateUser(ctx, &store.User{ID: "u1", Username: "owner", DisplayName: marker, Role: "admin", Status: "active", SSOProvider: "local"}); err != nil {
		t.Fatal(err)
	}
	settings := backup.Settings(ctx, st.Settings())
	priv, _ := recoverykey.Generate()
	if err := recoveryclient.StoreRecoveryKey(cfg.Database.DataDir, settings, recoveryclient.RecoveryKey{Public: priv.Public(), Threshold: 2, TotalShares: 3}); err != nil {
		t.Fatal(err)
	}
	rc, err := backup.RunConfig(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := recoveryclient.StorePairing(settings, rc.Sealer, "https://recovery.busnes.app", "kyrec_live_t"); err != nil {
		t.Fatal(err)
	}
	var scratch []os.FileMode
	defer backup.OnSnapshotForTest(func(dir string) {
		info, err := os.Stat(dir)
		if err != nil {
			t.Error(err)
			return
		}
		if filepath.Dir(dir) != cfg.Database.DataDir {
			t.Errorf("scratch %s is outside the data directory", dir)
		}
		scratch = append(scratch, info.Mode().Perm())
	})()
	dep := &recordingDepositor{}
	res, err := backup.Run(ctx, cfg, rc, st, dep)
	if err != nil || res.Receipt == nil {
		t.Fatalf("deposit: %v", err)
	}
	if _, err := capsule.ReadUnverifiedManifest(dep.raw); err != nil || !bytes.Contains(dep.raw, []byte(capsule.KycapFileFormat)) {
		t.Fatalf("deposited bytes are not a kycap/3 container: %v", err)
	}
	for _, clear := range []string{"SQLite format 3", marker, "kyrec_live_t"} {
		if bytes.Contains(dep.raw, []byte(clear)) {
			t.Errorf("KyRecovery received %q in the clear", clear)
		}
	}
	if len(scratch) != 1 || scratch[0] != 0700 {
		t.Errorf("scratch directory modes %v, want one 0700", scratch)
	}
	entries, _ := os.ReadDir(cfg.Database.DataDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "snapshot-") {
			t.Errorf("scratch %s left behind", e.Name())
		}
	}
	_, files, err := capsule.Open(dep.raw, priv, "")
	if err != nil {
		t.Fatal(err)
	}
	var db []byte
	for _, f := range files {
		if f.Path == "data/ky_server.db" {
			db = f.Content
		}
	}
	if !bytes.Contains(db, []byte(marker)) {
		t.Fatal("the sealed database member does not carry the PostgreSQL rows")
	}
}

// A schema the snapshot does not know is refused before anything is sealed or sent.
func TestPostgresUnknownSchemaSealsNothing(t *testing.T) {
	cfg, st := postgresInstance(t)
	ctx := context.Background()
	settings := backup.Settings(ctx, st.Settings())
	priv, _ := recoverykey.Generate()
	if err := recoveryclient.StoreRecoveryKey(cfg.Database.DataDir, settings, recoveryclient.RecoveryKey{Public: priv.Public(), Threshold: 2, TotalShares: 3}); err != nil {
		t.Fatal(err)
	}
	cfg.Backup.Dir = filepath.Join(t.TempDir(), "capsules")
	cfg.Backup.Keep = 2
	rc, _ := backup.RunConfig(cfg, "test")
	raw, err := sql.Open("pgx", cfg.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TABLE product_notes (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	_, err = backup.Run(ctx, cfg, rc, st, &recordingDepositor{})
	if !errors.Is(err, backup.ErrNoDatabaseSnapshot) || !strings.Contains(err.Error(), "adapter") {
		t.Fatalf("got %v, want ErrNoDatabaseSnapshot naming the adapter", err)
	}
	if entries, _ := os.ReadDir(cfg.Backup.Dir); len(entries) != 0 {
		t.Fatalf("a capsule was written: %v", entries)
	}
}
