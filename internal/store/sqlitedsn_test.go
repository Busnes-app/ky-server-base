package store_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// A PostgreSQL URL given to the sqlite driver (compose pins KY_DB_DRIVER=sqlite) is refused
// without echoing its password, instead of becoming a SQLite file outside the data volume;
// a file: URI opens the file it names and creates no "file:" directory.
func TestSQLiteDSNIsAFilePath(t *testing.T) {
	t.Chdir(t.TempDir())
	_, err := store.Open(context.Background(), config.DatabaseConfig{Driver: "sqlite", DSN: "postgres://postgres:hunter2pw@postgres:5432/ky_server?sslmode=disable"})
	if err == nil || !strings.Contains(err.Error(), "KY_DB_DRIVER") || strings.Contains(err.Error(), "hunter2pw") {
		t.Fatalf("URL DSN on sqlite: %v", err)
	}
	if entries, _ := os.ReadDir("."); len(entries) != 0 {
		t.Fatalf("refused DSN left %v behind", entries)
	}

	dir := filepath.Join(t.TempDir(), "sub")
	st, err := store.Open(context.Background(), config.DatabaseConfig{Driver: "sqlite", DSN: "file:" + filepath.Join(dir, "ky_server.db")})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if _, err := os.Stat(filepath.Join(dir, "ky_server.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("file:"); !os.IsNotExist(err) {
		t.Fatalf("a file: DSN created a stray directory: %v", err)
	}
}

func TestSQLiteURIAndPlainPathUseTheSameInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.db")
	dsn := "file:" + strings.ReplaceAll(path, "server.db", "server%2Edb")
	actual, err := store.SQLitePath(dsn)
	if err != nil || actual != path {
		t.Fatalf("URI maps to %q, %v", actual, err)
	}
	st, err := store.Open(context.Background(), config.DatabaseConfig{Driver: "sqlite3", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, err = store.WithSQLiteInstance(context.Background(), actual, func(ctx context.Context) error {
		if _, err := store.MigrateToPostgres(ctx, path, config.DatabaseConfig{Driver: "postgres"}, true); err == nil {
			t.Fatal("migration entered a running encoded-URI instance")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
