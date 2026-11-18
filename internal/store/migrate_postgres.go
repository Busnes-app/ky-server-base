package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store/migrations"
)

// MigrationReport contains counts, never database contents or connection credentials.
type MigrationReport struct {
	Rows      map[string]int64 `json:"rows"`
	CheckOnly bool             `json:"check_only"`
}

// MigrateToPostgres copies a private, upgraded SQLite snapshot into an empty PostgreSQL
// schema. It leaves the original SQLite file and deployment settings untouched. The
// operator must stop all servers before copying and keep them stopped through cutover.
// checkOnly performs the same copy, constraint and content checks, then rolls it back.
func MigrateToPostgres(ctx context.Context, source string, target config.DatabaseConfig, checkOnly bool) (MigrationReport, error) {
	var report MigrationReport
	won, err := WithSQLiteInstance(ctx, source, func(ctx context.Context) error {
		var err error
		report, err = migrateToPostgres(ctx, source, target, checkOnly)
		return err
	})
	if err != nil {
		return report, err
	}
	if !won {
		return report, errors.New("SQLite server is running; stop it before migrating")
	}
	return report, nil
}

func migrateToPostgres(ctx context.Context, source string, target config.DatabaseConfig, checkOnly bool) (MigrationReport, error) {
	report := MigrationReport{Rows: make(map[string]int64), CheckOnly: checkOnly}
	if target.Driver != "postgres" {
		return report, errors.New("migration target must be PostgreSQL")
	}
	path, err := filepath.Abs(source)
	if err != nil {
		return report, err
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return report, errors.New("source must be an existing SQLite database file")
	}
	// mode=ro prevents a typo from creating an empty source database.
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=busy_timeout(5000)"}
	original, err := sql.Open("sqlite", u.String())
	if err != nil {
		return report, errors.New("cannot open SQLite source")
	}
	defer original.Close()
	var integrity string
	if err := original.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return report, errors.New("SQLite source failed integrity check")
	}
	if err := migrations.ValidateHistory(ctx, original); err != nil {
		return report, err
	}
	dir, err := os.MkdirTemp(filepath.Dir(path), ".postgres-migration-")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(dir)
	snapshot := filepath.Join(dir, "snapshot.db")
	if err := recoveryclient.SQLiteSnapshot(ctx, original, snapshot); err != nil {
		return report, errors.New("could not snapshot SQLite source")
	}
	copied, err := Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: snapshot})
	if err != nil {
		return report, fmt.Errorf("upgrade private source snapshot: %w", err)
	}
	defer copied.Close()
	src := copied.(*SQLStore)
	foreign, err := src.db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return report, err
	}
	bad := foreign.Next()
	err = foreign.Err()
	foreign.Close()
	if err != nil || bad {
		return report, errors.New("SQLite source has invalid foreign keys")
	}
	db, err := openPostgresDB(ctx, target.DSN, target.MaxOpenConns, target.MaxIdleConns, target.ConnMaxLifetime)
	if err != nil {
		return report, errors.New("cannot connect to PostgreSQL target; check connectivity and permissions")
	}
	defer db.Close()
	dst := &SQLStore{db: db, driver: "postgres"}
	won, err := dst.WithLock(ctx, "schema-migrations", func(ctx context.Context) error {
		bootstrap, err := dst.WithLock(ctx, "bootstrap", func(ctx context.Context) error {
			// Refuse occupied/unknown targets BEFORE running even one schema migration.
			if err := preflightPostgresTarget(ctx, db); err != nil {
				return err
			}
			if err := applySchema(ctx, db, "postgres"); err != nil {
				return errors.New("cannot initialize PostgreSQL target schema")
			}
			return copyPostgres(ctx, src, dst, &report)
		})
		if err == nil && !bootstrap {
			return errors.New("PostgreSQL target is busy; stop its servers and retry")
		}
		return err
	})
	if err != nil {
		return report, err
	}
	// WithLock also reports a losing contender; a report with no tables was not a copy.
	if !won || len(report.Rows) == 0 {
		return report, errors.New("PostgreSQL target is busy; stop its servers and retry")
	}
	return report, nil
}

func preflightPostgresTarget(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "SELECT tablename FROM pg_tables WHERE schemaname=current_schema()")
	if err != nil {
		return errors.New("cannot inspect PostgreSQL target schema")
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if slices.Contains(tables, "schema_migrations") {
		if err := migrations.ValidateHistory(ctx, db); err != nil {
			return errors.New("target has unsupported scaffold migration history")
		}
	}
	for _, name := range tables {
		if name == "schema_migrations" {
			continue
		}
		if !slices.Contains(migrationTables, name) {
			return fmt.Errorf("target table %q needs a product-specific migration adapter", name)
		}
		var occupied bool
		if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+quoteIdentifier(name)+")").Scan(&occupied); err != nil {
			return errors.New("cannot inspect PostgreSQL target data")
		}
		if occupied {
			return fmt.Errorf("target table %q is not empty; existing data will not be overwritten or migrated", name)
		}
	}
	return nil
}

// The order satisfies every scaffold foreign key. Fail closed for product-specific tables:
// derived products need an adapter that knows their schema, never a partial migration.
var migrationTables = []string{
	"users", "groups", "group_members", "sessions", "audit_records", "server_settings",
	"mfa_challenges", "sso_logouts", "sso_subject_logouts", "sso_directory_events",
	"security_windows", "spent_challenges",
}

type migrationColumn struct{ name, kind string }

func quoteIdentifier(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func copyPostgres(ctx context.Context, src, dst *SQLStore, report *MigrationReport) error {
	// Source snapshots may have extra product tables. Refuse them rather than silently
	// migrating just accounts and leaving application content behind.
	rows, err := src.db.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type='table' AND name NOT GLOB 'sqlite_*'")
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		if name != "schema_migrations" && !slices.Contains(migrationTables, name) {
			rows.Close()
			return fmt.Errorf("source table %q needs a product-specific migration adapter", name)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	targetTables, err := dst.db.QueryContext(ctx, "SELECT tablename FROM pg_tables WHERE schemaname = current_schema()")
	if err != nil {
		return err
	}
	for targetTables.Next() {
		var name string
		if err := targetTables.Scan(&name); err != nil {
			targetTables.Close()
			return err
		}
		if name != "schema_migrations" && !slices.Contains(migrationTables, name) {
			targetTables.Close()
			return fmt.Errorf("target table %q needs a product-specific migration adapter", name)
		}
	}
	err = targetTables.Err()
	targetTables.Close()
	if err != nil {
		return err
	}
	tx, err := dst.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Keep prospective application writers out while checking emptiness and copying.
	quoted := make([]string, len(migrationTables))
	for i, table := range migrationTables {
		quoted[i] = quoteIdentifier(table)
	}
	if _, err := tx.ExecContext(ctx, "LOCK TABLE "+strings.Join(quoted, ",")+" IN ACCESS EXCLUSIVE MODE NOWAIT"); err != nil {
		return errors.New("PostgreSQL target has active writers; stop its servers and retry")
	}
	for _, table := range migrationTables {
		var count int64
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+quoteIdentifier(table)).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("target table %q is not empty; existing data will not be overwritten", table)
		}
	}
	for _, table := range migrationTables {
		columns, err := postgresColumns(ctx, tx, table)
		if err != nil {
			return err
		}
		if err := matchSourceColumns(ctx, src.db, table, columns); err != nil {
			return err
		}
		n, digest, err := copyTable(ctx, src.db, tx, "postgres", table, columns)
		if err != nil {
			return err
		}
		verified, checksum, err := tableDigest(ctx, tx, table, columns)
		if err != nil || n != verified || digest.Cmp(checksum) != 0 {
			return fmt.Errorf("content verification failed for table %q", table)
		}
		report.Rows[table] = n
	}
	if _, err := tx.ExecContext(ctx, `SELECT setval(pg_get_serial_sequence(format('%I.audit_records', current_schema()), 'id'), COALESCE(MAX(id), 1), COUNT(*) > 0) FROM audit_records`); err != nil {
		return err
	}
	if report.CheckOnly {
		return nil
	}
	return tx.Commit()
}

// querier is a *sql.DB or a *sql.Tx.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func postgresColumns(ctx context.Context, tx querier, table string) ([]migrationColumn, error) {
	rows, err := tx.QueryContext(ctx, `SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []migrationColumn
	for rows.Next() {
		var c migrationColumn
		if err := rows.Scan(&c.name, &c.kind); err != nil {
			return nil, err
		}
		columns = append(columns, c)
	}
	return columns, rows.Err()
}

func matchSourceColumns(ctx context.Context, db querier, table string, columns []migrationColumn) error {
	rows, err := db.QueryContext(ctx, "SELECT * FROM "+quoteIdentifier(table)+" LIMIT 0")
	if err != nil {
		return fmt.Errorf("source table %q is missing", table)
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return err
	}
	if len(names) != len(columns) {
		return fmt.Errorf("table %q has product-specific columns; a migration adapter is required", table)
	}
	for _, c := range columns {
		if !slices.Contains(names, c.name) {
			return fmt.Errorf("table %q has incompatible columns", table)
		}
	}
	return nil
}

func columnSQL(columns []migrationColumn) string {
	names := make([]string, len(columns))
	for i, c := range columns {
		names[i] = quoteIdentifier(c.name)
	}
	return strings.Join(names, ",")
}

// copyTable copies table from src into tx, whose driver is dstDriver, and returns the row
// count and summed digest of the values written.
func copyTable(ctx context.Context, src querier, tx *sql.Tx, dstDriver, table string, columns []migrationColumn) (int64, *big.Int, error) {
	rows, err := src.QueryContext(ctx, "SELECT "+columnSQL(columns)+" FROM "+quoteIdentifier(table))
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	params := make([]string, len(columns))
	for i := range params {
		params[i] = "?"
		if dstDriver == "postgres" {
			params[i] = fmt.Sprintf("$%d", i+1)
		}
	}
	q := "INSERT INTO " + quoteIdentifier(table) + " (" + columnSQL(columns) + ") VALUES (" + strings.Join(params, ",") + ")"
	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return 0, nil, err
	}
	defer stmt.Close()
	digest := new(big.Int)
	var count int64
	for rows.Next() {
		values, err := migrationValues(rows, columns)
		if err != nil {
			return 0, nil, fmt.Errorf("invalid source value in table %q", table)
		}
		if _, err := stmt.ExecContext(ctx, values...); err != nil {
			// Driver errors can quote passwords, sealed tokens or personal data from the row.
			return 0, nil, fmt.Errorf("copy into table %q failed at row %d; check target constraints and source values", table, count+1)
		}
		addRowDigest(digest, values)
		count++
	}
	return count, digest, rows.Err()
}

func tableDigest(ctx context.Context, tx querier, table string, columns []migrationColumn) (int64, *big.Int, error) {
	rows, err := tx.QueryContext(ctx, "SELECT "+columnSQL(columns)+" FROM "+quoteIdentifier(table))
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	digest := new(big.Int)
	var count int64
	for rows.Next() {
		values, err := migrationValues(rows, columns)
		if err != nil {
			return 0, nil, err
		}
		addRowDigest(digest, values)
		count++
	}
	return count, digest, rows.Err()
}

// Summed SHA-256 row digests compare contents without depending on database collation/order
// or retaining an entire database in memory. Counts are checked separately.
func addRowDigest(sum *big.Int, values []any) {
	encoded, _ := json.Marshal(values) // values have been narrowed to SQL scalars and UTC times
	h := sha256.Sum256(encoded)
	sum.Add(sum, new(big.Int).SetBytes(h[:]))
}

func migrationValues(rows *sql.Rows, columns []migrationColumn) ([]any, error) {
	values := make([]any, len(columns))
	ptrs := make([]any, len(columns))
	for i := range values {
		ptrs[i] = &values[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	for i, c := range columns {
		if values[i] == nil {
			continue
		}
		if raw, ok := values[i].([]byte); ok {
			values[i] = string(raw)
		}
		switch c.kind {
		case "boolean":
			switch v := values[i].(type) {
			case bool:
			case int64:
				if v != 0 && v != 1 {
					return nil, errors.New("invalid boolean")
				}
				values[i] = v == 1
			default:
				return nil, errors.New("invalid boolean")
			}
		case "timestamp with time zone":
			var stamp time.Time
			switch v := values[i].(type) {
			case time.Time:
				stamp = v
			case string:
				var parsed bool
				for _, format := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05.999999999"} {
					if t, err := time.Parse(format, v); err == nil {
						stamp = t
						parsed = true
						break
					}
				}
				if !parsed {
					return nil, errors.New("invalid timestamp")
				}
			default:
				return nil, errors.New("invalid timestamp")
			}
			// PostgreSQL timestamp precision is microseconds; normalize both sides identically.
			values[i] = stamp.UTC().Truncate(time.Microsecond)
		}
	}
	return values, nil
}
