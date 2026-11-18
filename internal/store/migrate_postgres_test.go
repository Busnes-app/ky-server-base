package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/password"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

func TestPostgresMigrationPreservesAndVerifiesDeployment(t *testing.T) {
	if os.Getenv("KY_TEST_POSTGRES_DSN") == "" {
		t.Skip("requires real PostgreSQL")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "source.db")
	src, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: path})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := password.Hash("a-long-test-password")
	if err != nil {
		t.Fatal(err)
	}
	user := &store.User{ID: "u1", Username: "owner", Email: "owner@example.test", PasswordHash: hash, Role: "admin", Status: "active", SSOProvider: "local", SCIMManaged: true, MustChangePassword: true}
	if err := src.Users().CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := src.Groups().CreateGroup(ctx, &store.Group{ID: "g1", DisplayName: "Staff"}); err != nil {
		t.Fatal(err)
	}
	if err := src.Groups().AddGroupMember(ctx, "g1", "u1"); err != nil {
		t.Fatal(err)
	}
	if err := src.Audit().LogAudit(ctx, &store.AuditRecord{UserID: "u1", Action: "test.original"}); err != nil {
		t.Fatal(err)
	}
	sealed, err := crypto.EncryptAESGCM([]byte("test-kyrecovery-token"), bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := src.Settings().SetSetting(ctx, "kyrecovery_token_enc", sealed); err != nil {
		t.Fatal(err)
	}
	if err := src.Sessions().CreateSession(ctx, &store.Session{TokenHash: "session-hash", UserID: "u1", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}, hash); err != nil {
		t.Fatal(err)
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	target := testdb.Config(t)
	report, err := store.MigrateToPostgres(ctx, path, target, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Rows["users"] != 1 || report.Rows["sessions"] != 1 || report.Rows["group_members"] != 1 {
		t.Fatalf("bad check report: %+v", report)
	}
	dst, err := store.Open(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if n, _ := dst.Users().CountUsers(ctx); n != 0 {
		t.Fatalf("check committed %d users", n)
	}
	report, err = store.MigrateToPostgres(ctx, path, target, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := dst.Users().GetUserByUsername(ctx, "owner")
	if err != nil || got.PasswordHash != hash || !got.MustChangePassword || !got.SCIMManaged {
		t.Fatalf("lost user state: %+v %v", got, err)
	}
	if v, err := dst.Settings().GetSetting(ctx, "kyrecovery_token_enc"); err != nil || v != sealed {
		t.Fatalf("lost sealed setting: %v", err)
	}
	if _, err := dst.Sessions().GetSession(ctx, "session-hash"); err != nil {
		t.Fatalf("lost session: %v", err)
	}
	if groups, err := dst.Groups().GetUserGroups(ctx, "u1"); err != nil || len(groups) != 1 {
		t.Fatalf("lost memberships: %v %v", groups, err)
	}
	if err := dst.Audit().LogAudit(ctx, &store.AuditRecord{Action: "test.after"}); err != nil {
		t.Fatalf("audit sequence not advanced: %v", err)
	}
	if _, err := store.MigrateToPostgres(ctx, path, target, false); err == nil {
		t.Fatal("overwrote populated target")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("source database changed")
	}
}

func TestPostgresMigrationRollbackAndUnknownData(t *testing.T) {
	if os.Getenv("KY_TEST_POSTGRES_DSN") == "" {
		t.Skip("requires real PostgreSQL")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "source.db")
	src, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := src.Users().CreateUser(ctx, &store.User{ID: "u", Username: "owner", Role: "admin", Status: "active", SSOProvider: "local"}); err != nil {
		t.Fatal(err)
	}
	src.Close()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec("CREATE TABLE product_data (content TEXT)"); err != nil {
		t.Fatal(err)
	}
	target := testdb.Config(t)
	if _, err := store.MigrateToPostgres(ctx, path, target, false); err == nil {
		t.Fatal("silently omitted product data")
	}
	if _, err := raw.Exec("DROP TABLE product_data"); err != nil {
		t.Fatal(err)
	}
	// SQLite permits values too long for the PostgreSQL contract. A failed copy must
	// leave every table empty, including tables inserted before the failing one.
	if _, err := raw.Exec("INSERT INTO groups (id,display_name,created_at,updated_at) VALUES ('bad',?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)", string(bytes.Repeat([]byte{'x'}, 300))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MigrateToPostgres(ctx, path, target, false); err == nil {
		t.Fatal("invalid target value accepted")
	}
	dst, err := store.Open(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if n, _ := dst.Users().CountUsers(ctx); n != 0 {
		t.Fatalf("failed migration retained %d users", n)
	}
}

func TestMigrationRejectsAnUninitializedSQLiteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MigrateToPostgres(context.Background(), path, config.DatabaseConfig{Driver: "postgres"}, true); err == nil {
		t.Fatal("empty non-scaffold file reported a verified migration")
	}
	after, err := os.ReadFile(path)
	if err != nil || len(after) != 0 {
		t.Fatalf("source changed: %d %v", len(after), err)
	}
}

func TestMigrationRefusesOldPopulatedTargetBeforeAnyDDL(t *testing.T) {
	if os.Getenv("KY_TEST_POSTGRES_DSN") == "" {
		t.Skip("requires PostgreSQL")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "source.db")
	src, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: path})
	if err != nil {
		t.Fatal(err)
	}
	src.Close()
	target := testdb.Config(t)
	db, err := sql.Open("pgx", target.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// A pre-migration target: even schema initialization must not happen here.
	if _, err := db.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY); INSERT INTO users VALUES ('retained'); CREATE TABLE sessions (token_hash TEXT); INSERT INTO sessions VALUES ('retained-session')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MigrateToPostgres(ctx, path, target, true); err == nil {
		t.Fatal("populated target accepted")
	}
	var sessions int
	if err := db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("populated target changed: sessions=%d %v", sessions, err)
	}
	var historyExists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='schema_migrations')`).Scan(&historyExists); err != nil || historyExists {
		t.Fatalf("target schema was mutated: history=%v %v", historyExists, err)
	}
}
