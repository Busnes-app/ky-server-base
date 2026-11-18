package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store/migrations"
)

// ErrSnapshotSchema is a PostgreSQL schema this build cannot copy whole: an unknown table or
// column, or a migration history other than the current scaffold's. A derived product needs
// its own snapshot adapter; nothing partial is ever produced.
var ErrSnapshotSchema = errors.New("the PostgreSQL schema is not the base scaffold's; a product-specific backup adapter is required")

// snapshotHook runs inside the snapshot transaction once its snapshot is taken (tests only).
var snapshotHook func()

// SnapshotToSQLite writes one point-in-time copy of a PostgreSQL store into a new SQLite file
// at dest, with the current scaffold schema. Every table in migrationTables is read in one
// REPEATABLE READ READ ONLY transaction, holding one pooled connection; the copy is verified
// per table (count and summed row digest) against that transaction, then by SQLite's
// integrity and foreign-key checks. The caller owns dest's directory and removes it.
func (s *SQLStore) SnapshotToSQLite(ctx context.Context, dest string) error {
	if s.driver != "postgres" {
		return errors.New("store: SnapshotToSQLite copies PostgreSQL; SQLite is snapshotted with VACUUM INTO")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The first query fixes the snapshot every later read sees.
	if err := migrations.ValidateCurrentHistory(ctx, tx); err != nil {
		return fmt.Errorf("%w: %v", ErrSnapshotSchema, err)
	}
	if snapshotHook != nil {
		snapshotHook()
	}
	if err := knownTables(ctx, tx); err != nil {
		return err
	}

	opened, err := Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: dest})
	if err != nil {
		return err
	}
	dst := opened.(*SQLStore)
	defer dst.Close()
	out, err := dst.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer out.Rollback()
	for _, table := range migrationTables {
		columns, err := postgresColumns(ctx, tx, table)
		if err != nil {
			return err
		}
		if err := matchSourceColumns(ctx, out, table, columns); err != nil {
			return fmt.Errorf("%w: %v", ErrSnapshotSchema, err)
		}
		n, digest, err := copyTable(ctx, tx, out, "sqlite", table, columns)
		if err != nil {
			return err
		}
		written, check, err := tableDigest(ctx, out, table, columns)
		if err != nil || n != written || digest.Cmp(check) != 0 {
			return fmt.Errorf("snapshot verification failed for table %q", table)
		}
	}
	if err := out.Commit(); err != nil {
		return err
	}
	var integrity string
	if err := dst.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return errors.New("snapshot failed the SQLite integrity check")
	}
	foreign, err := dst.db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	bad := foreign.Next()
	err = foreign.Err()
	foreign.Close()
	if err != nil || bad {
		return errors.New("snapshot failed the SQLite foreign-key check")
	}
	// One self-contained file: fold the WAL in before it is read and sealed.
	if _, err := dst.db.ExecContext(ctx, "PRAGMA journal_mode=DELETE"); err != nil {
		return err
	}
	return dst.Close()
}

// knownTables refuses a schema with a table outside migrationTables or missing one of them.
func knownTables(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT tablename FROM pg_tables WHERE schemaname = current_schema()")
	if err != nil {
		return err
	}
	defer rows.Close()
	var seen []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name != "schema_migrations" && !slices.Contains(migrationTables, name) {
			return fmt.Errorf("%w: table %q", ErrSnapshotSchema, name)
		}
		seen = append(seen, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, name := range migrationTables {
		if !slices.Contains(seen, name) {
			return fmt.Errorf("%w: table %q is missing", ErrSnapshotSchema, name)
		}
	}
	return nil
}
