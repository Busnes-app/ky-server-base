package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

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
	report, err := store.MigrateToPostgres(ctx, *source, config.DatabaseConfig{Driver: "postgres", DSN: dsn}, *check)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(out).Encode(report); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "Verified. SQLite and configuration were not changed. Keep existing encryption.key, recovery.pub and secrets. After cutover, prove a PostgreSQL capsule with Back up now and the restore drill.")
	return err
}
