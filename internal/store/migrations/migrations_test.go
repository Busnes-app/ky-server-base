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

// Sessions from before migration 6 carry no sid, so no back-channel logout could end them:
// the upgrade must drop the SSO ones and keep local sign-ins.
func TestBackchannelMigrationDropsUnreachableSSOSessions(t *testing.T) {
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

	full := registry
	t.Cleanup(func() { registry = full })
	if last := full[len(full)-1]; last.Version != 6 {
		t.Fatalf("test expects migration 6 last, got %d", last.Version)
	}
	registry = full[:len(full)-1]
	if err := Run(ctx, db, cfg.Driver); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, u := range []struct{ id, provider string }{{"usr_local", "local"}, {"usr_sso", "kysignon"}} {
		if _, err := db.ExecContext(ctx, q(`INSERT INTO users (id, username, sso_provider, sso_subject, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`), u.id, u.id, u.provider, u.id, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, q(`INSERT INTO sessions (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`), "tok_"+u.id, u.id, now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	registry = full
	if err := Run(ctx, db, cfg.Driver); err != nil {
		t.Fatal(err)
	}
	var local, sso int
	if err := db.QueryRowContext(ctx, q(`SELECT COUNT(1) FROM sessions WHERE user_id = ?`), "usr_local").Scan(&local); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, q(`SELECT COUNT(1) FROM sessions WHERE user_id = ?`), "usr_sso").Scan(&sso); err != nil {
		t.Fatal(err)
	}
	if local != 1 || sso != 0 {
		t.Fatalf("after migration 6: local sessions %d (want 1), SSO sessions %d (want 0)", local, sso)
	}
}
