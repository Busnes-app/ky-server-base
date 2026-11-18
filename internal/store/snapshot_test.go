package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

func postgresStore(t *testing.T, maxOpen int) *SQLStore {
	t.Helper()
	cfg := testdb.Config(t)
	if cfg.Driver != "postgres" {
		t.Skip("snapshots of PostgreSQL need KY_TEST_POSTGRES_DSN")
	}
	cfg.MaxOpenConns = maxOpen
	st, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st.(*SQLStore)
}

// seedEveryTable writes at least one row to every table in migrationTables.
func seedEveryTable(t *testing.T, st *SQLStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.Users().CreateUser(ctx, &User{ID: "u1", Username: "owner", PasswordHash: "hash", Role: "admin", Status: "active", SSOProvider: "local", MustChangePassword: true, LastLoginAt: &now}))
	must(st.Users().CreateUser(ctx, &User{ID: "u2", Username: "member", Role: "user", Status: "active", SSOProvider: "scim", SSOSubject: "ext-2", SCIMManaged: true}))
	must(st.Groups().CreateGroup(ctx, &Group{ID: "g1", DisplayName: "Staff", ExternalID: "x"}))
	must(st.Groups().AddGroupMember(ctx, "g1", "u2"))
	must(st.Sessions().CreateSession(ctx, &Session{TokenHash: "tok", UserID: "u1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, "hash"))
	must(st.Audit().LogAudit(ctx, &AuditRecord{UserID: "u1", Action: "seed", Details: "ä \x01 \"quoted\""}))
	must(st.Settings().SetSetting(ctx, "kyrecovery_token_enc", "sealed"))
	must(st.Sessions().EndSSOSession(ctx, "sid-1", now.Add(time.Hour)))
	must(st.Sessions().EndSSOSubject(ctx, "kysignon", "sub-1", now, now.Add(time.Hour)))
	_, _, err := st.TakeAttempt(ctx, "login:198.51.100.1", 5, time.Minute)
	must(err)
	_, err = st.SpendChallenge(ctx, "0123456789abcdef0123456789abcdef", now.Add(time.Minute).UnixMilli())
	must(err)
	for _, q := range []string{
		`INSERT INTO mfa_challenges (token_hash, user_id, expires_at) VALUES ('m1', 'u1', now())`,
		`INSERT INTO sso_directory_events (provider, subject, last_timestamp) VALUES ('kysignon', 'sub-1', 42)`,
	} {
		if _, err := st.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, table := range migrationTables {
		var n int
		must(st.db.QueryRowContext(ctx, "SELECT count(*) FROM "+quoteIdentifier(table)).Scan(&n))
		if n == 0 {
			t.Fatalf("seed left %s empty", table)
		}
	}
}

func openSnapshot(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSnapshotPostgresCopiesEveryTableVerbatim(t *testing.T) {
	st := postgresStore(t, 0)
	seedEveryTable(t, st)
	path := filepath.Join(t.TempDir(), "ky_server.db")
	if err := st.SnapshotToSQLite(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	db := openSnapshot(t, path)
	ctx := context.Background()
	for _, table := range migrationTables {
		columns, err := postgresColumns(ctx, st.db, table)
		if err != nil {
			t.Fatal(err)
		}
		wantN, want, err := tableDigest(ctx, st.db, table, columns)
		if err != nil {
			t.Fatal(err)
		}
		gotN, got, err := tableDigest(ctx, db, table, columns)
		if err != nil {
			t.Fatal(err)
		}
		if gotN != wantN || got.Cmp(want) != 0 {
			t.Errorf("%s: snapshot has %d rows (digest %x), source %d (%x)", table, gotN, got, wantN, want)
		}
	}
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity_check %q %v", integrity, err)
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Error("snapshot has foreign-key violations")
	}
	rows.Close()
	// The file opens as an ordinary store and reads what was written.
	restored, err := Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: path})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	u, err := restored.Users().GetUserByUsername(ctx, "owner")
	if err != nil || !u.MustChangePassword || u.LastLoginAt == nil {
		t.Fatalf("restored user: %+v %v", u, err)
	}
	if g, err := restored.Groups().GetUserGroups(ctx, "u2"); err != nil || len(g) != 1 {
		t.Fatalf("restored membership: %v %v", g, err)
	}
}

// Writers that commit after the snapshot began are invisible to it, and multi-table writes
// land whole or not at all.
func TestSnapshotPostgresIsPointInTime(t *testing.T) {
	st := postgresStore(t, 0)
	ctx := context.Background()
	write := func(i int) error {
		tx, err := st.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		id := fmt.Sprint(i)
		for _, q := range []string{
			`INSERT INTO users (id, username, created_at, updated_at) VALUES ('w` + id + `', 'w` + id + `', now(), now())`,
			`INSERT INTO groups (id, display_name, created_at, updated_at) VALUES ('wg` + id + `', 'wg` + id + `', now(), now())`,
			`INSERT INTO group_members (group_id, user_id) VALUES ('wg` + id + `', 'w` + id + `')`,
		} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	for i := range 5 {
		if err := write(i); err != nil {
			t.Fatal(err)
		}
	}
	// A writer running throughout, plus ten commits made while the snapshot is open.
	stop := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		for i := 1000; ; i++ {
			select {
			case <-stop:
				writerDone <- nil
				return
			default:
			}
			if err := write(i); err != nil {
				writerDone <- err
				return
			}
		}
	}()
	var during error
	snapshotHook = func() {
		for i := -1; i >= -10 && during == nil; i-- {
			during = write(i)
		}
	}
	t.Cleanup(func() { snapshotHook = nil })
	path := filepath.Join(t.TempDir(), "ky_server.db")
	err := st.SnapshotToSQLite(ctx, path)
	close(stop)
	if werr := <-writerDone; werr != nil {
		t.Fatal(werr)
	}
	if err != nil || during != nil {
		t.Fatalf("snapshot %v, writes during it %v", err, during)
	}
	db := openSnapshot(t, path)
	var users, groups, members, late int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM users), (SELECT count(*) FROM groups), (SELECT count(*) FROM group_members),
(SELECT count(*) FROM users WHERE id LIKE 'w-%')`).Scan(&users, &groups, &members, &late); err != nil {
		t.Fatal(err)
	}
	if users < 5 || users != groups || groups != members {
		t.Fatalf("snapshot is not one committed state: %d users, %d groups, %d members", users, groups, members)
	}
	if late != 0 {
		t.Fatalf("%d transactions committed after the snapshot began are in it", late)
	}
	var orphans int
	if err := db.QueryRow(`SELECT count(*) FROM group_members m LEFT JOIN users u ON u.id = m.user_id LEFT JOIN groups g ON g.id = m.group_id WHERE u.id IS NULL OR g.id IS NULL`).Scan(&orphans); err != nil || orphans != 0 {
		t.Fatalf("orphan memberships: %d %v", orphans, err)
	}
}

func TestSnapshotPostgresRefusesAnUnknownSchema(t *testing.T) {
	for name, change := range map[string]string{
		"extra table":     `CREATE TABLE product_notes (id TEXT PRIMARY KEY)`,
		"extra column":    `ALTER TABLE users ADD COLUMN product_flag TEXT`,
		"unknown history": `INSERT INTO schema_migrations (version, name, applied_at) VALUES (999, 'product_future', now())`,
		"missing table":   `DROP TABLE spent_challenges`,
	} {
		t.Run(name, func(t *testing.T) {
			st := postgresStore(t, 0)
			if _, err := st.db.Exec(change); err != nil {
				t.Fatal(err)
			}
			err := st.SnapshotToSQLite(context.Background(), filepath.Join(t.TempDir(), "ky_server.db"))
			if !errors.Is(err, ErrSnapshotSchema) {
				t.Fatalf("got %v, want ErrSnapshotSchema", err)
			}
		})
	}
}

// A scheduled backup on PostgreSQL holds the worker lock, the backup-run lock and the
// snapshot transaction at once. At the configured floor a query (here the readiness Ping and
// a settings read) still gets a connection while all three are held; one fewer and it waits.
func TestSnapshotLeavesAConnectionAtThePoolFloor(t *testing.T) {
	for _, tc := range []struct {
		conns int
		ok    bool
	}{{config.MinPostgresOpenConns, true}, {config.MinPostgresOpenConns - 1, false}} {
		t.Run(fmt.Sprint(tc.conns), func(t *testing.T) {
			st := postgresStore(t, config.MinPostgresOpenConns)
			st.db.SetMaxOpenConns(tc.conns)
			ctx := context.Background()
			var queryErr error
			snapshotHook = func() {
				probe, cancel := context.WithTimeout(ctx, time.Second)
				defer cancel()
				if queryErr = st.Ping(probe); queryErr == nil {
					_, queryErr = st.Settings().GetSetting(probe, "backup_interval_sec")
					if errors.Is(queryErr, ErrNotFound) {
						queryErr = nil
					}
				}
			}
			t.Cleanup(func() { snapshotHook = nil })
			var snapErr error
			won, err := st.WithLock(ctx, "scheduled-worker", func(ctx context.Context) error {
				owned, err := st.WithLock(ctx, "backup-run", func(ctx context.Context) error {
					snapErr = st.SnapshotToSQLite(ctx, filepath.Join(t.TempDir(), "ky_server.db"))
					return snapErr
				})
				if err == nil && !owned {
					err = errors.New("backup-run not won")
				}
				return err
			})
			if !won || err != nil {
				t.Fatalf("locks: won=%v err=%v", won, err)
			}
			if tc.ok && queryErr != nil {
				t.Fatalf("at %d connections a query during the snapshot failed: %v", tc.conns, queryErr)
			}
			if !tc.ok && queryErr == nil {
				t.Fatalf("at %d connections a query still got a connection; the floor is set too high", tc.conns)
			}
		})
	}
}
