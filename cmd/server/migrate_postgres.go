package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

func migratePostgres(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("migrate-postgres", flag.ContinueOnError)
	fs.SetOutput(out)
	dataDir := os.Getenv("KY_DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	source := fs.String("source", filepath.Join(dataDir, "ky_server.db"), "existing SQLite file (stop the server first)")
	check := fs.Bool("check", false, "copy and verify, then roll back target rows")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	dsn := os.Getenv("KY_MIGRATE_POSTGRES_DSN")
	if dsn == "" {
		return errors.New("set KY_MIGRATE_POSTGRES_DSN to the empty target; credentials are never accepted in arguments")
	}
	// Before anything is copied: a pin replicas could not load would stop backups silently.
	record, err := sourceKeyRecord(*source)
	if err != nil {
		return err
	}
	target := config.DatabaseConfig{Driver: "postgres", DSN: dsn}
	report, err := store.MigrateToPostgres(ctx, *source, target, *check)
	if err != nil {
		return err
	}
	if record != "" && !*check {
		st, err := store.Open(ctx, target)
		if err != nil {
			return fmt.Errorf("copied and verified, but the recovery key record was not written (pin the same key by hand after cutover): %w", err)
		}
		defer st.Close()
		if err := st.Settings().SetSetting(ctx, backup.RecoveryKeyRecordSetting, record); err != nil {
			return fmt.Errorf("copied and verified, but the recovery key record was not written (pin the same key by hand after cutover): %w", err)
		}
		fmt.Fprintln(out, "Recorded the pinned recovery public key from recovery.pub for the replicas.")
	}
	if err := json.NewEncoder(out).Encode(report); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "Verified. SQLite and configuration were not changed. Keep existing encryption.key, recovery.pub and secrets. After cutover, prove a PostgreSQL capsule with Back up now and the restore drill.")
	return err
}

// sourceKeyRecord is the recovery key record a source pinned before records existed lacks:
// built from the recovery.pub beside the database, which must be the pinned key. "" when the
// source pins nothing or already has a record.
func sourceKeyRecord(source string) (string, error) {
	source, err := filepath.Abs(source)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(source); err != nil || !info.Mode().IsRegular() {
		return "", nil // MigrateToPostgres refuses it with its own message
	}
	u := url.URL{Scheme: "file", Path: source, RawQuery: "mode=ro&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return "", err
	}
	defer db.Close()
	rs := &readOnlySettings{db: db}
	if err := backup.RecordRecoveryKeyFromFile(filepath.Dir(source), rs); err != nil {
		return "", err
	}
	return rs.set[backup.RecoveryKeyRecordSetting], nil
}

// readOnlySettings reads the source's settings and keeps what would be written.
type readOnlySettings struct {
	db  *sql.DB
	set map[string]string
}

func (r *readOnlySettings) Get(key string) (string, error) {
	var v string
	err := r.db.QueryRow("SELECT value FROM server_settings WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", recoveryclient.ErrNotFound
	}
	return v, err
}

func (r *readOnlySettings) Set(key, val string) error {
	if r.set == nil {
		r.set = map[string]string{}
	}
	r.set[key] = val
	return nil
}

func (r *readOnlySettings) Delete(string) error { return errors.New("read-only source") }
