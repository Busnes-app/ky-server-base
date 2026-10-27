package migrations

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/testdb"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// Sessions from before migration 6 carry no sid, so no back-channel logout could end
// them: migration 7 drops those SSO sessions, keeps local sign-ins, and keeps SSO
// sessions issued with a sid after migration 6.
func TestMigration7DropsUnreachableSSOSessions(t *testing.T) {
	cfg := testdb.Config(t)
	driverName := "sqlite"
	if cfg.Driver == "postgres" {
		driverName = "pgx"
	}
	db, err := sql.Open(driverName, cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	q := func(query string) string {
		if cfg.Driver != "postgres" {
			return query
		}
		for i := 1; strings.Contains(query, "?"); i++ {
			query = strings.Replace(query, "?", "$"+strconv.Itoa(i), 1)
		}
		return query
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q(query), args...); err != nil {
			t.Fatal(err)
		}
	}
	all := registry
	t.Cleanup(func() { registry = all })
	runThrough := func(version int) {
		t.Helper()
		for i, m := range all {
			if m.Version == version {
				registry = all[:i+1]
			}
		}
		if err := Run(ctx, db, cfg.Driver); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	runThrough(5)
	for _, u := range []struct{ id, provider string }{{"usr_local", "local"}, {"usr_sso", "kysignon"}} {
		exec(`INSERT INTO users (id, username, sso_provider, sso_subject, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, u.id, u.id, u.provider, u.id, now, now)
		exec(`INSERT INTO sessions (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`, "legacy_"+u.id, u.id, now, now.Add(time.Hour))
	}
	runThrough(6)
	exec(`INSERT INTO sessions (token_hash, user_id, created_at, expires_at, sso_sid) VALUES (?, ?, ?, ?, ?)`, "with_sid", "usr_sso", now, now.Add(time.Hour), "sid-1")
	runThrough(7)

	for token, want := range map[string]int{"legacy_usr_local": 1, "legacy_usr_sso": 0, "with_sid": 1} {
		var got int
		if err := db.QueryRowContext(ctx, q(`SELECT COUNT(1) FROM sessions WHERE token_hash = ?`), token).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("after migration 7, session %s: got %d rows, want %d", token, got, want)
		}
	}
}
