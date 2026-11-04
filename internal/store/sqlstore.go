package store

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/store/migrations"
)

// SQLStore implements Store on top of database/sql.
type SQLStore struct {
	db       *sql.DB
	driver   string
	users    *userStore
	sessions *sessionStore
	groups   *groupStore
	audit    *auditStore
	settings *settingsStore
	// dirMu serializes directory-sync events (WithDirectoryLock).
	dirMu sync.Mutex
}

// newSQLStore creates and initializes a SQLStore, running migrations automatically.
func newSQLStore(ctx context.Context, db *sql.DB, driver string) (*SQLStore, error) {
	driver = strings.ToLower(driver)
	if driver == "postgresql" {
		driver = "postgres"
	}

	if err := migrations.Run(ctx, db, driver); err != nil {
		return nil, fmt.Errorf("migration failure on driver %s: %w", driver, err)
	}
	clashes, err := migrations.EnsureUsernameIndex(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("case-insensitive username index: %w", err)
	}
	for _, names := range clashes {
		log.Printf("[STORE] usernames differ only in letter case: %s. Sign-in picks one of them arbitrarily; rename or remove all but one. New case-variant names are refused.", strings.Join(names, ", "))
	}

	s := &SQLStore{
		db:     db,
		driver: driver,
	}

	s.users = &userStore{store: s}
	s.sessions = &sessionStore{store: s}
	s.groups = &groupStore{store: s}
	s.audit = &auditStore{store: s}
	s.settings = &settingsStore{store: s}

	return s, nil
}

func (s *SQLStore) Users() UserStore        { return s.users }
func (s *SQLStore) Sessions() SessionStore  { return s.sessions }
func (s *SQLStore) Groups() GroupStore      { return s.groups }
func (s *SQLStore) Audit() AuditStore       { return s.audit }
func (s *SQLStore) Settings() SettingsStore { return s.settings }

func (s *SQLStore) Driver() string                 { return s.driver }
func (s *SQLStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *SQLStore) Close() error                   { return s.db.Close() }

// rebind converts '?' placeholders to '$1, $2, ...' for Postgres
func (s *SQLStore) rebind(query string) string {
	if s.driver != "postgres" {
		return query
	}

	var b strings.Builder
	b.Grow(len(query) + 16)
	paramIdx := 1

	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(paramIdx))
			paramIdx++
		} else {
			b.WriteByte(query[i])
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------
// User Store
// ---------------------------------------------------------------------

type userStore struct {
	store *SQLStore
}

func (u *userStore) CreateUser(ctx context.Context, user *User) error {
	now := time.Now().UTC()
	if user.CreatedAt.IsZero() {
		user.CreatedAt = now
	}
	if user.UpdatedAt.IsZero() {
		user.UpdatedAt = now
	}
	tx, err := u.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := u.checkUsernameFree(ctx, tx, user.Username, ""); err != nil {
		return err
	}

	q := u.store.rebind(`
INSERT INTO users (
    id, username, email, display_name, password_hash, role, status,
    sso_provider, sso_subject, scim_managed, push_device_id, must_change_password,
    created_at, updated_at, last_login_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`)

	var lastLogin sql.NullTime
	if user.LastLoginAt != nil {
		lastLogin = sql.NullTime{Time: *user.LastLoginAt, Valid: true}
	}

	if _, err := tx.ExecContext(ctx, q,
		user.ID, user.Username, user.Email, user.DisplayName, user.PasswordHash,
		user.Role, user.Status, user.SSOProvider, user.SSOSubject, user.SCIMManaged,
		user.PushDeviceID, user.MustChangePassword,
		user.CreatedAt, user.UpdatedAt, lastLogin,
	); err != nil {
		return uniqueErr(err)
	}
	return uniqueErr(tx.Commit())
}

// uniqueErr maps a unique-constraint violation on either backend to ErrAlreadyExists.
func uniqueErr(err error) error {
	if err != nil && (strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "duplicate key")) {
		return ErrAlreadyExists
	}
	return err
}

// userColumns is every column scanUser reads. The retired local-MFA columns (totp_*,
// recovery_codes_hash) stay in the schema with their data but are no longer read or written.
const userColumns = `id, username, email, display_name, password_hash, role, status,
       sso_provider, sso_subject, scim_managed, push_device_id, must_change_password,
       created_at, updated_at, last_login_at`

// checkUsernameFree refuses, inside the write's transaction, a name another account already
// holds in any letter case. The LOWER(username) unique index enforces this once existing data
// allows it to be built (migrations.EnsureUsernameIndex); the check covers installations where
// it cannot be. It is atomic with the write: SQLite runs one connection, and PostgreSQL takes a
// transaction advisory lock on the lowered name. An account keeping its own name (in any case)
// passes, so a pre-existing case-variant pair can still be updated and deprovisioned.
func (u *userStore) checkUsernameFree(ctx context.Context, tx *sql.Tx, username, id string) error {
	if u.store.driver == "postgres" {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(2, hashtext(LOWER($1)))`, username); err != nil {
			return err
		}
	}
	// The database's LOWER decides "same name", exactly as the clash query and the index do.
	var own int
	if err := tx.QueryRowContext(ctx, u.store.rebind(`SELECT COUNT(1) FROM users WHERE id = ? AND LOWER(username) = LOWER(?)`), id, username).Scan(&own); err != nil {
		return err
	}
	if own > 0 {
		return nil
	}
	var n int
	if err := tx.QueryRowContext(ctx, u.store.rebind(`SELECT COUNT(1) FROM users WHERE LOWER(username) = LOWER(?) AND id <> ?`), username, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrAlreadyExists
	}
	return nil
}

func (u *userStore) scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var user User
	var lastLogin sql.NullTime

	err := row.Scan(
		&user.ID, &user.Username, &user.Email, &user.DisplayName, &user.PasswordHash,
		&user.Role, &user.Status, &user.SSOProvider, &user.SSOSubject, &user.SCIMManaged,
		&user.PushDeviceID, &user.MustChangePassword,
		&user.CreatedAt, &user.UpdatedAt, &lastLogin,
	)
	if err != nil {
		if errorsIs(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if lastLogin.Valid {
		user.LastLoginAt = &lastLogin.Time
	}
	return &user, nil
}

func (u *userStore) GetUserByID(ctx context.Context, id string) (*User, error) {
	q := u.store.rebind(`
SELECT ` + userColumns + `
FROM users WHERE id = ?
`)
	return u.scanUser(u.store.db.QueryRowContext(ctx, q, id))
}

func (u *userStore) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	q := u.store.rebind(`
SELECT ` + userColumns + `
FROM users WHERE LOWER(username) = LOWER(?)
`)
	return u.scanUser(u.store.db.QueryRowContext(ctx, q, username))
}

func (u *userStore) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	q := u.store.rebind(`
SELECT ` + userColumns + `
FROM users WHERE LOWER(email) = LOWER(?)
`)
	return u.scanUser(u.store.db.QueryRowContext(ctx, q, email))
}

func (u *userStore) GetUserBySSO(ctx context.Context, provider, subject string) (*User, error) {
	q := u.store.rebind(`
SELECT ` + userColumns + `
FROM users WHERE sso_provider = ? AND sso_subject = ?
`)
	return u.scanUser(u.store.db.QueryRowContext(ctx, q, provider, subject))
}

func (u *userStore) UpdateUser(ctx context.Context, user *User) error {
	user.UpdatedAt = time.Now().UTC()
	var lastLogin sql.NullTime
	if user.LastLoginAt != nil {
		lastLogin = sql.NullTime{Time: *user.LastLoginAt, Valid: true}
	}

	tx, err := u.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := u.checkUsernameFree(ctx, tx, user.Username, user.ID); err != nil {
		return err
	}
	q := u.store.rebind(`
UPDATE users SET
    username = ?, email = ?, display_name = ?, password_hash = ?,
    role = ?, status = ?, sso_provider = ?, sso_subject = ?,
    push_device_id = ?, must_change_password = ?, updated_at = ?,
    last_login_at = ?
WHERE id = ?
`)

	res, err := tx.ExecContext(ctx, q,
		user.Username, user.Email, user.DisplayName, user.PasswordHash,
		user.Role, user.Status, user.SSOProvider, user.SSOSubject,
		user.PushDeviceID, user.MustChangePassword, user.UpdatedAt,
		lastLogin, user.ID,
	)
	if err != nil {
		return uniqueErr(err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}
	return uniqueErr(tx.Commit())
}

func (u *userStore) UpdateUserProfile(ctx context.Context, user *User, prevRole, prevStatus string) error {
	user.UpdatedAt = time.Now().UTC()
	tx, err := u.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := u.checkUsernameFree(ctx, tx, user.Username, user.ID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, u.store.rebind(`
UPDATE users SET
    username = ?, email = ?, display_name = ?, role = ?, status = ?, updated_at = ?
WHERE id = ? AND role = ? AND status = ?
`), user.Username, user.Email, user.DisplayName, user.Role, user.Status,
		user.UpdatedAt, user.ID, prevRole, prevStatus)
	if err != nil {
		return uniqueErr(err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		var n int
		if err := tx.QueryRowContext(ctx, u.store.rebind(`SELECT COUNT(1) FROM users WHERE id = ?`), user.ID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return ErrConflict
	}
	if user.Role != prevRole || user.Status != prevStatus {
		if _, err := tx.ExecContext(ctx, u.store.rebind(`DELETE FROM sessions WHERE user_id = ?`), user.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (u *userStore) LinkSSOIdentity(ctx context.Context, id, fromProvider, toProvider string) error {
	res, err := u.store.db.ExecContext(ctx, u.store.rebind(`UPDATE users SET sso_provider = ?, updated_at = ? WHERE id = ? AND sso_provider = ?`), toProvider, time.Now().UTC(), id, fromProvider)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return ErrConflict
	}
	return nil
}

func (u *userStore) DeleteUser(ctx context.Context, id string) error {
	q := u.store.rebind("DELETE FROM users WHERE id = ?")
	res, err := u.store.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (u *userStore) ListUsers(ctx context.Context, offset, limit int, search string) ([]*User, int, error) {
	return u.listUsers(ctx, offset, limit, search, false)
}

func (u *userStore) ListSCIMUsers(ctx context.Context, offset, limit int, search string) ([]*User, int, error) {
	return u.listUsers(ctx, offset, limit, search, true)
}

func (u *userStore) listUsers(ctx context.Context, offset, limit int, search string, scimOnly bool) ([]*User, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	where := " WHERE 1 = 1"
	var args []any
	if scimOnly {
		where += " AND scim_managed = ?"
		args = append(args, true)
	}
	if strings.TrimSpace(search) != "" {
		like := "%" + strings.ToLower(strings.TrimSpace(search)) + "%"
		where += " AND (LOWER(username) LIKE ? OR LOWER(display_name) LIKE ? OR LOWER(email) LIKE ?)"
		args = append(args, like, like, like)
	}

	var total int
	err := u.store.db.QueryRowContext(ctx, u.store.rebind("SELECT COUNT(1) FROM users"+where), args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	listQuery := "SELECT " + userColumns + " FROM users" + where + " ORDER BY created_at DESC, id LIMIT ? OFFSET ?"
	rows, err := u.store.db.QueryContext(ctx, u.store.rebind(listQuery), append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		user, err := u.scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		users = append(users, user)
	}

	return users, total, rows.Err()
}

// AdvanceDirectoryEvent records ts as the newest directory event applied for the subject and
// reports false, recording nothing, when a newer one was already applied. An equal timestamp
// is accepted: a redelivery of the last event changes nothing.
func (u *userStore) AdvanceDirectoryEvent(ctx context.Context, provider, subject string, ts int64) (bool, error) {
	res, err := u.store.db.ExecContext(ctx, u.store.rebind(`INSERT INTO sso_directory_events (provider, subject, last_timestamp) VALUES (?, ?, ?)
ON CONFLICT (provider, subject) DO UPDATE SET last_timestamp = excluded.last_timestamp WHERE sso_directory_events.last_timestamp <= excluded.last_timestamp`), provider, subject, ts)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (u *userStore) LastDirectoryEvent(ctx context.Context, provider, subject string) (int64, error) {
	var ts int64
	err := u.store.db.QueryRowContext(ctx, u.store.rebind(`SELECT last_timestamp FROM sso_directory_events WHERE provider = ? AND subject = ?`), provider, subject).Scan(&ts)
	if errorsIs(err, sql.ErrNoRows) {
		return 0, nil
	}
	return ts, err
}

// WithDirectoryLock serializes directory events in this process. It holds no database
// connection, so it cannot starve the pool. One server process per database is the supported
// deployment (the backup scheduler assumes it too).
func (u *userStore) WithDirectoryLock(ctx context.Context, provider, subject string, fn func() error) error {
	u.store.dirMu.Lock()
	defer u.store.dirMu.Unlock()
	return fn()
}

// SetDirectoryEvent puts the recorded timestamp back to ts, but only while it is still
// current: it undoes an AdvanceDirectoryEvent whose event then failed to apply.
func (u *userStore) SetDirectoryEvent(ctx context.Context, provider, subject string, current, ts int64) error {
	if ts == 0 {
		_, err := u.store.db.ExecContext(ctx, u.store.rebind(`DELETE FROM sso_directory_events WHERE provider = ? AND subject = ? AND last_timestamp = ?`), provider, subject, current)
		return err
	}
	_, err := u.store.db.ExecContext(ctx, u.store.rebind(`UPDATE sso_directory_events SET last_timestamp = ? WHERE provider = ? AND subject = ? AND last_timestamp = ?`), ts, provider, subject, current)
	return err
}

func (u *userStore) CountUsers(ctx context.Context) (int, error) {
	var count int
	err := u.store.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM users").Scan(&count)
	return count, err
}

// ---------------------------------------------------------------------
// Session Store
// ---------------------------------------------------------------------

type sessionStore struct {
	store *SQLStore
}

// withPassword serializes credential-derived grants with password replacement.
// Updating the same user row takes a write lock on both supported databases.
func (s *SQLStore) withPassword(ctx context.Context, userID, expectedHash string, apply func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.rebind("UPDATE users SET id = id WHERE id = ? AND password_hash = ? AND status = 'active'"), userID, expectedHash)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	if err := apply(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sessionStore) CreateSession(ctx context.Context, sess *Session, expectedPasswordHash string) error {
	err := s.store.withPassword(ctx, sess.UserID, expectedPasswordHash, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, s.store.rebind(`INSERT INTO sessions (token_hash, user_id, user_agent, ip_address, created_at, expires_at, sso_sid) VALUES (?, ?, ?, ?, ?, ?, ?)`), sess.TokenHash, sess.UserID, sess.UserAgent, sess.IPAddress, sess.CreatedAt, sess.ExpiresAt, sess.SSOSID)
		return err
	})
	if err != nil {
		return err
	}
	// Check the fences only after the session committed: EndSSOSession and EndSSOSubject
	// commit their fence before deleting, so under read committed one of the two always
	// sees the other.
	now := time.Now().UTC()
	var fenced int
	if err := s.store.db.QueryRowContext(ctx, s.store.rebind(`SELECT
 (SELECT COUNT(1) FROM sso_logouts WHERE sid = ? AND sid <> '' AND expires_at > ?) +
 (SELECT COUNT(1) FROM sso_subject_logouts f JOIN users u ON u.sso_provider = f.provider AND u.sso_subject = f.subject
  WHERE u.id = ? AND f.cutoff > ? AND f.expires_at > ?)`), sess.SSOSID, now, sess.UserID, sess.CreatedAt.UTC(), now).Scan(&fenced); err != nil || fenced > 0 {
		_ = s.DeleteSession(ctx, sess.TokenHash)
		if err != nil {
			return err
		}
		return ErrSSOSessionEnded
	}
	return nil
}

func (s *sessionStore) EndSSOSession(ctx context.Context, sid string, fenceUntil time.Time) error {
	now := time.Now().UTC()
	if _, err := s.store.db.ExecContext(ctx, s.store.rebind(`DELETE FROM sso_logouts WHERE expires_at <= ?`), now); err != nil {
		return err
	}
	if _, err := s.store.db.ExecContext(ctx, s.store.rebind(`INSERT INTO sso_logouts (sid, expires_at) VALUES (?, ?) ON CONFLICT (sid) DO UPDATE SET expires_at = excluded.expires_at`), sid, fenceUntil.UTC()); err != nil {
		return err
	}
	_, err := s.store.db.ExecContext(ctx, s.store.rebind(`DELETE FROM sessions WHERE sso_sid = ?`), sid)
	return err
}

func (s *sessionStore) EndSSOSubject(ctx context.Context, provider, subject string, cutoff, fenceUntil time.Time) error {
	if _, err := s.store.db.ExecContext(ctx, s.store.rebind(`DELETE FROM sso_subject_logouts WHERE expires_at <= ?`), time.Now().UTC()); err != nil {
		return err
	}
	// Keyed by subject, not user, so it also fences a first login that is still provisioning.
	if _, err := s.store.db.ExecContext(ctx, s.store.rebind(`INSERT INTO sso_subject_logouts (provider, subject, cutoff, expires_at) VALUES (?, ?, ?, ?)
ON CONFLICT (provider, subject) DO UPDATE SET cutoff = excluded.cutoff, expires_at = excluded.expires_at WHERE sso_subject_logouts.cutoff < excluded.cutoff`), provider, subject, cutoff.UTC(), fenceUntil.UTC()); err != nil {
		return err
	}
	_, err := s.store.db.ExecContext(ctx, s.store.rebind(`DELETE FROM sessions WHERE created_at < ? AND user_id IN (SELECT id FROM users WHERE sso_provider = ? AND sso_subject = ?)`), cutoff.UTC(), provider, subject)
	return err
}

func (u *userStore) CompletePasswordChange(ctx context.Context, userID, oldHash, newHash, ip string) error {
	return u.store.withPassword(ctx, userID, oldHash, func(tx *sql.Tx) error {
		now := time.Now().UTC()
		result, err := tx.ExecContext(ctx, u.store.rebind(`UPDATE users SET password_hash = ?, must_change_password = ?, updated_at = ? WHERE id = ? AND must_change_password = ? AND sso_provider = 'local'`), newHash, false, now, userID, true)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrNotFound
		}
		return u.revokePasswordGrants(ctx, tx, userID, "forced replacement; sessions revoked", ip, now)
	})
}

// ResetAdminPassword is the operator recovery path, including disabled local accounts.
func (u *userStore) ResetAdminPassword(ctx context.Context, userID, newHash string) error {
	tx, err := u.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, u.store.rebind(`UPDATE users SET password_hash = ?, must_change_password = ?, status = 'active', role = 'admin', updated_at = ? WHERE id = ? AND sso_provider = 'local'`), newHash, true, now, userID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	if err := u.revokePasswordGrants(ctx, tx, userID, "operator reset; sessions revoked", "", now); err != nil {
		return err
	}
	return tx.Commit()
}

func (u *userStore) revokePasswordGrants(ctx context.Context, tx *sql.Tx, userID, details, ip string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, u.store.rebind("DELETE FROM sessions WHERE user_id = ?"), userID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, u.store.rebind(`INSERT INTO audit_records (user_id, action, resource, details, ip_address, created_at) VALUES (?, ?, ?, ?, ?, ?)`), userID, "auth.password_changed", "user", details, ip, now)
	return err
}

func (s *sessionStore) GetSession(ctx context.Context, tokenHash string) (*Session, error) {
	q := s.store.rebind(`
SELECT token_hash, user_id, user_agent, ip_address, created_at, expires_at, sso_sid
FROM sessions WHERE token_hash = ?
`)
	var sess Session
	err := s.store.db.QueryRowContext(ctx, q, tokenHash).Scan(
		&sess.TokenHash, &sess.UserID, &sess.UserAgent, &sess.IPAddress,
		&sess.CreatedAt, &sess.ExpiresAt, &sess.SSOSID,
	)
	if err != nil {
		if errorsIs(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if time.Now().UTC().After(sess.ExpiresAt) {
		_ = s.DeleteSession(ctx, tokenHash)
		return nil, ErrSessionExpired
	}
	return &sess, nil
}

func (s *sessionStore) DeleteSession(ctx context.Context, tokenHash string) error {
	q := s.store.rebind("DELETE FROM sessions WHERE token_hash = ?")
	_, err := s.store.db.ExecContext(ctx, q, tokenHash)
	return err
}

func (s *sessionStore) DeleteUserSessions(ctx context.Context, userID string) error {
	q := s.store.rebind("DELETE FROM sessions WHERE user_id = ?")
	_, err := s.store.db.ExecContext(ctx, q, userID)
	return err
}

func (s *sessionStore) CleanExpiredSessions(ctx context.Context) error {
	q := s.store.rebind("DELETE FROM sessions WHERE expires_at < ?")
	_, err := s.store.db.ExecContext(ctx, q, time.Now().UTC())
	return err
}

// ---------------------------------------------------------------------
// Group Store
// ---------------------------------------------------------------------

type groupStore struct {
	store *SQLStore
}

func (g *groupStore) CreateGroup(ctx context.Context, group *Group) error {
	now := time.Now().UTC()
	if group.CreatedAt.IsZero() {
		group.CreatedAt = now
	}
	if group.UpdatedAt.IsZero() {
		group.UpdatedAt = now
	}

	q := g.store.rebind(`
INSERT INTO groups (id, display_name, external_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
`)
	_, err := g.store.db.ExecContext(ctx, q, group.ID, group.DisplayName, group.ExternalID, group.CreatedAt, group.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "duplicate key") {
			return ErrAlreadyExists
		}
		return err
	}
	return nil
}

func (g *groupStore) GetGroupByID(ctx context.Context, id string) (*Group, error) {
	q := g.store.rebind("SELECT id, display_name, external_id, created_at, updated_at FROM groups WHERE id = ?")
	var grp Group
	err := g.store.db.QueryRowContext(ctx, q, id).Scan(&grp.ID, &grp.DisplayName, &grp.ExternalID, &grp.CreatedAt, &grp.UpdatedAt)
	if err != nil {
		if errorsIs(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	members, err := g.getMembers(ctx, grp.ID)
	if err != nil {
		return nil, err
	}
	grp.Members = members
	return &grp, nil
}

func (g *groupStore) GetGroupByName(ctx context.Context, name string) (*Group, error) {
	q := g.store.rebind("SELECT id, display_name, external_id, created_at, updated_at FROM groups WHERE LOWER(display_name) = LOWER(?)")
	var grp Group
	err := g.store.db.QueryRowContext(ctx, q, name).Scan(&grp.ID, &grp.DisplayName, &grp.ExternalID, &grp.CreatedAt, &grp.UpdatedAt)
	if err != nil {
		if errorsIs(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	members, err := g.getMembers(ctx, grp.ID)
	if err != nil {
		return nil, err
	}
	grp.Members = members
	return &grp, nil
}

func (g *groupStore) getMembers(ctx context.Context, groupID string) ([]string, error) {
	q := g.store.rebind("SELECT user_id FROM group_members WHERE group_id = ?")
	rows, err := g.store.db.QueryContext(ctx, q, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err == nil {
			members = append(members, uid)
		}
	}
	return members, rows.Err()
}

func (g *groupStore) UpdateGroup(ctx context.Context, group *Group) error {
	group.UpdatedAt = time.Now().UTC()
	q := g.store.rebind("UPDATE groups SET display_name = ?, external_id = ?, updated_at = ? WHERE id = ?")
	res, err := g.store.db.ExecContext(ctx, q, group.DisplayName, group.ExternalID, group.UpdatedAt, group.ID)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (g *groupStore) DeleteGroup(ctx context.Context, id string) error {
	q := g.store.rebind("DELETE FROM groups WHERE id = ?")
	res, err := g.store.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (g *groupStore) ListGroups(ctx context.Context, offset, limit int) ([]*Group, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var count int
	err := g.store.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM groups").Scan(&count)
	if err != nil {
		return nil, 0, err
	}

	q := g.store.rebind("SELECT id, display_name, external_id, created_at, updated_at FROM groups ORDER BY display_name ASC LIMIT ? OFFSET ?")
	rows, err := g.store.db.QueryContext(ctx, q, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var groups []*Group
	for rows.Next() {
		var grp Group
		if err := rows.Scan(&grp.ID, &grp.DisplayName, &grp.ExternalID, &grp.CreatedAt, &grp.UpdatedAt); err != nil {
			return nil, 0, err
		}
		groups = append(groups, &grp)
	}

	for _, grp := range groups {
		members, _ := g.getMembers(ctx, grp.ID)
		grp.Members = members
	}

	return groups, count, rows.Err()
}

func (g *groupStore) AddGroupMember(ctx context.Context, groupID, userID string) error {
	q := g.store.rebind("INSERT INTO group_members (group_id, user_id) VALUES (?, ?) ON CONFLICT DO NOTHING")
	_, err := g.store.db.ExecContext(ctx, q, groupID, userID)
	return err
}

func (g *groupStore) RemoveGroupMember(ctx context.Context, groupID, userID string) error {
	q := g.store.rebind("DELETE FROM group_members WHERE group_id = ? AND user_id = ?")
	_, err := g.store.db.ExecContext(ctx, q, groupID, userID)
	return err
}

func (g *groupStore) GetUserGroups(ctx context.Context, userID string) ([]*Group, error) {
	q := g.store.rebind(`
SELECT g.id, g.display_name, g.external_id, g.created_at, g.updated_at
FROM groups g
JOIN group_members gm ON g.id = gm.group_id
WHERE gm.user_id = ?
ORDER BY g.display_name ASC
`)
	rows, err := g.store.db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []*Group
	for rows.Next() {
		var grp Group
		if err := rows.Scan(&grp.ID, &grp.DisplayName, &grp.ExternalID, &grp.CreatedAt, &grp.UpdatedAt); err != nil {
			return nil, err
		}
		groups = append(groups, &grp)
	}
	return groups, rows.Err()
}

// ---------------------------------------------------------------------
// Audit Store
// ---------------------------------------------------------------------

type auditStore struct {
	store *SQLStore
}

func (a *auditStore) LogAudit(ctx context.Context, r *AuditRecord) error {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	q := a.store.rebind(`
INSERT INTO audit_records (user_id, action, resource, details, ip_address, created_at)
VALUES (?, ?, ?, ?, ?, ?)
`)
	_, err := a.store.db.ExecContext(ctx, q, r.UserID, r.Action, r.Resource, r.Details, r.IPAddress, r.CreatedAt)
	return err
}

func (a *auditStore) ListAuditRecords(ctx context.Context, offset, limit int) ([]*AuditRecord, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var count int
	err := a.store.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM audit_records").Scan(&count)
	if err != nil {
		return nil, 0, err
	}

	q := a.store.rebind(`
SELECT id, user_id, action, resource, details, ip_address, created_at
FROM audit_records
ORDER BY created_at DESC LIMIT ? OFFSET ?
`)
	rows, err := a.store.db.QueryContext(ctx, q, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var records []*AuditRecord
	for rows.Next() {
		var r AuditRecord
		if err := rows.Scan(&r.ID, &r.UserID, &r.Action, &r.Resource, &r.Details, &r.IPAddress, &r.CreatedAt); err != nil {
			return nil, 0, err
		}
		records = append(records, &r)
	}
	return records, count, rows.Err()
}

// ---------------------------------------------------------------------
// Settings Store
// ---------------------------------------------------------------------

type settingsStore struct {
	store *SQLStore
}

func (s *settingsStore) GetSetting(ctx context.Context, key string) (string, error) {
	q := s.store.rebind("SELECT value FROM server_settings WHERE key = ?")
	var val string
	err := s.store.db.QueryRowContext(ctx, q, key).Scan(&val)
	if err != nil {
		if errorsIs(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return val, nil
}

func (s *settingsStore) SetSetting(ctx context.Context, key, val string) error {
	now := time.Now().UTC()
	q := s.store.rebind(`
INSERT INTO server_settings (key, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
`)
	_, err := s.store.db.ExecContext(ctx, q, key, val, now)
	return err
}

func (s *settingsStore) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.store.db.ExecContext(ctx, s.store.rebind(`DELETE FROM server_settings WHERE key = ?`), key)
	return err
}

func (s *settingsStore) GetAllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.store.db.QueryContext(ctx, "SELECT key, value FROM server_settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err == nil {
			settings[k] = v
		}
	}
	return settings, rows.Err()
}

func errorsIs(err, target error) bool {
	if err == nil || target == nil {
		return err == target
	}
	return err == target || strings.Contains(err.Error(), target.Error())
}
