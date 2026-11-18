package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/capsule"
	"github.com/Busnes-app/ky-primitives/password"
	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky-primitives/recoverykey"
	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

// throwawayKit is a 2-of-3 recovery kit made for one test; the private key never leaves it.
func throwawayKit(t *testing.T) (recoveryclient.RecoveryKey, recoverykey.PrivateKey, []string) {
	t.Helper()
	priv, err := recoverykey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	shares, err := recoverykey.Split(priv, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	// Non-consecutive shares: {1,2} would let an XOR-shaped bug pass.
	return recoveryclient.RecoveryKey{Public: priv.Public(), Threshold: 2, TotalShares: 3}, priv, []string{shares[0].String(), shares[2].String()}
}

// pinKey pins k as the pin handler does: the lib's write-once pin, then the shared record.
func pinKey(t *testing.T, cfg *config.Config, st store.Store, k recoveryclient.RecoveryKey) {
	t.Helper()
	settings := backup.Settings(context.Background(), st.Settings())
	if err := recoveryclient.StoreRecoveryKey(cfg.Database.DataDir, settings, k); err != nil {
		t.Fatal(err)
	}
	if err := backup.RecordRecoveryKey(settings, k); err != nil {
		t.Fatal(err)
	}
}

// A second PostgreSQL replica, at the validated pool floor, runs the scheduled backup: it
// writes recovery.pub from the pin it did not take, seals while holding the worker lock, the
// backup-run lock and the snapshot transaction, and readiness pings never starve.
func TestScheduledPostgresBackupAtThePoolFloor(t *testing.T) {
	newCfg := replicaConfig(t)
	cfgA := newCfg()
	stA := openStore(t, cfgA)
	if err := bootstrap(context.Background(), cfgA, stA); err != nil {
		t.Fatal(err)
	}
	key, priv, _ := throwawayKit(t)
	pinKey(t, cfgA, stA, key)

	cfgB := newCfg()
	cfgB.Database.MaxOpenConns = config.MinPostgresOpenConns
	cfgB.Backup.Dir = filepath.Join(t.TempDir(), "capsules")
	cfgB.Backup.Keep = 2
	cfgB.Backup.DepositInterval = time.Hour
	stB := openStore(t, cfgB)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var pingFailures atomic.Int32
	go func() {
		for ctx.Err() == nil {
			probe, stop := context.WithTimeout(ctx, time.Second)
			if err := stB.Ping(probe); err != nil && ctx.Err() == nil {
				pingFailures.Add(1)
			}
			stop()
			time.Sleep(20 * time.Millisecond)
		}
	}()
	done := make(chan struct{})
	go backupLoop(ctx, cfgB, stB, nil, 20*time.Millisecond, done)

	var capsulePath string
	deadline := time.Now().Add(60 * time.Second)
	for capsulePath == "" && time.Now().Before(deadline) {
		matches, _ := filepath.Glob(filepath.Join(cfgB.Backup.Dir, "*.kycap"))
		if len(matches) > 0 {
			capsulePath = matches[0]
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if capsulePath == "" {
		t.Fatal("no scheduled capsule on the PostgreSQL replica within 60s")
	}
	if n := pingFailures.Load(); n != 0 {
		t.Fatalf("%d readiness pings failed while the backup held its connections", n)
	}
	want, _ := os.ReadFile(recoveryclient.RecoveryKeyPath(cfgA.Database.DataDir))
	if got, err := os.ReadFile(recoveryclient.RecoveryKeyPath(cfgB.Database.DataDir)); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("replica B recovery.pub: %v", err)
	}
	raw, _ := os.ReadFile(capsulePath)
	out := t.TempDir()
	if _, files, err := capsule.Open(raw, priv, out); err != nil || len(files) < 3 {
		t.Fatalf("scheduled capsule: %v (%d files)", err, len(files))
	}
	var ok bool
	records, _, _ := stB.Audit().ListAuditRecords(context.Background(), 0, 50)
	for _, r := range records {
		ok = ok || (r.UserID == "system" && r.Action == "admin.backup_run" && strings.Contains(r.Details, `outcome="success"`))
	}
	if !ok {
		t.Error("no successful scheduled admin.backup_run row")
	}
}

// tableTexts returns, per table of a PostgreSQL schema, the sorted text of its rows: two
// schemas with equal maps hold the same data.
func tableTexts(t *testing.T, dsn string, skip map[string]string) map[string]string {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT tablename FROM pg_tables WHERE schemaname = current_schema() AND tablename <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		tables = append(tables, name)
	}
	rows.Close()
	out := map[string]string{}
	for _, table := range tables {
		where := ""
		if cond, ok := skip[table]; ok {
			if cond == "" {
				continue
			}
			where = " WHERE " + cond
		}
		var text sql.NullString
		if err := db.QueryRow(`SELECT string_agg(r::text, E'\n' ORDER BY r::text) FROM ` + table + ` r` + where).Scan(&text); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		out[table] = text.String
	}
	return out
}

// The whole PostgreSQL recovery path in-process: a seeded database is collected and sealed
// to a throwaway kit, restored with two non-consecutive shares into an empty directory and
// migrated into a fresh empty schema. The copy matches the source (sessions excepted), a
// local user signs in, the sealed KyRecovery token opens under the restored key, and a
// second migration into the now-populated target is refused without touching it.
func TestPostgresBackupRestoreDrill(t *testing.T) {
	newCfg := replicaConfig(t)
	cfg := newCfg()
	cfg.Server.AppName = "busnes_app"
	st := openStore(t, cfg)
	ctx := context.Background()
	if err := bootstrap(ctx, cfg, st); err != nil {
		t.Fatal(err)
	}
	hash, err := password.Hash("drill-password-0123")
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.Users().CreateUser(ctx, &store.User{ID: "u_drill", Username: "drill", PasswordHash: hash, Role: "user", Status: "active", SSOProvider: "local"}))
	must(st.Users().CreateUser(ctx, &store.User{ID: "u_scim", Username: "member", Role: "user", Status: "active", SSOProvider: "scim", SSOSubject: "ext", SCIMManaged: true}))
	must(st.Groups().CreateGroup(ctx, &store.Group{ID: "g_staff", DisplayName: "Staff"}))
	must(st.Groups().AddGroupMember(ctx, "g_staff", "u_scim"))
	must(st.Sessions().CreateSession(ctx, &store.Session{TokenHash: "live-session", UserID: "u_drill", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}, hash))
	must(st.Audit().LogAudit(ctx, &store.AuditRecord{UserID: "u_drill", Action: "drill.seed"}))
	must(st.Settings().SetSetting(ctx, "theme", "busnes"))
	key, _, shares := throwawayKit(t)
	pinKey(t, cfg, st, key)
	sealer, err := backup.NewSealer(cfg)
	must(err)
	must(recoveryclient.StorePairing(backup.Settings(ctx, st.Settings()), sealer, "https://recovery.busnes.app", "kyrec_drill_token"))
	must(recoveryclient.SetInterval(backup.Settings(ctx, st.Settings()), 3600))

	payload, err := backup.Collect(ctx, cfg, st, appVersion)
	must(err)
	raw, _, err := recoveryclient.Seal(payload, key)
	must(err)
	capsulePath := filepath.Join(t.TempDir(), "drill.kycap")
	must(os.WriteFile(capsulePath, raw, 0600))
	if bytes.Contains(raw, []byte("SQLite format 3")) || bytes.Contains(raw, []byte("kyrec_drill_token")) {
		t.Fatal("the sealed capsule carries the database in the clear")
	}

	target := t.TempDir()
	var out bytes.Buffer
	must(restore(capsulePath, target, "busnes_app", shares, &out))
	restoredDB := filepath.Join(target, "data", "ky_server.db")

	dest := testdb.Config(t)
	report, err := store.MigrateToPostgres(ctx, restoredDB, dest, false)
	must(err)
	if report.Rows["users"] != 3 { // the bootstrap admin, drill and member
		t.Fatalf("migrated rows %v", report.Rows)
	}

	skip := map[string]string{"sessions": "", "server_settings": "key NOT IN ('backup_interval_sec', 'replica_config_fingerprint')"}
	before, after := tableTexts(t, cfg.Database.DSN, skip), tableTexts(t, dest.DSN, skip)
	for table, text := range before {
		if after[table] != text {
			t.Errorf("table %s differs after the drill", table)
		}
	}
	if len(after) != len(before) {
		t.Errorf("restored schema has %d tables, source %d", len(after), len(before))
	}

	// Start the restored copy as a PostgreSQL server would: the restored key, a new data dir.
	keyHex, err := os.ReadFile(filepath.Join(target, "data", "encryption.key"))
	must(err)
	restoredCfg := *cfg
	restoredCfg.Database = dest
	restoredCfg.Database.DataDir = t.TempDir()
	restoredCfg.Security.EncryptionKey, err = hex.DecodeString(strings.TrimSpace(string(keyHex)))
	must(err)
	restoredSt := openStore(t, &restoredCfg)
	must(bootstrap(ctx, &restoredCfg, restoredSt))
	settings := backup.Settings(ctx, restoredSt.Settings())
	must(backup.SyncRecoveryKey(ctx, &restoredCfg, restoredSt))
	restoredSealer, err := backup.NewSealer(&restoredCfg)
	must(err)
	pairing, err := recoveryclient.LoadPairing(restoredCfg.Database.DataDir, settings, restoredSealer)
	if err != nil || pairing.Token != "kyrec_drill_token" {
		t.Fatalf("sealed token under the restored key: %v", err)
	}
	if n, on, err := recoveryclient.NextRun(time.Hour, settings); err != nil || on {
		t.Fatalf("restored schedule is on (next %v, %v)", n, err)
	}
	if _, err := restoredSt.Sessions().GetSession(ctx, "live-session"); err == nil {
		t.Fatal("a session minted before the capsule survived the restore")
	}
	restoredCfg.Captcha.Provider = "none" // the login check here is the password, not the puzzle
	srv := api.NewServer(&restoredCfg, restoredSt)
	body, _ := json.Marshal(map[string]string{"username": "drill", "password": "drill-password-0123"})
	req := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("restored user login: %d %s", w.Code, w.Body.String())
	}

	// A second migration into the populated target is refused and changes nothing.
	again := tableTexts(t, dest.DSN, nil)
	if _, err := store.MigrateToPostgres(ctx, restoredDB, dest, false); err == nil {
		t.Fatal("migration into a populated target was accepted")
	}
	for table, text := range tableTexts(t, dest.DSN, nil) {
		if again[table] != text {
			t.Errorf("refused migration changed table %s", table)
		}
	}
}
