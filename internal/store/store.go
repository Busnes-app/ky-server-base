package store

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound       = errors.New("record not found")
	ErrAlreadyExists  = errors.New("record already exists")
	ErrSessionExpired = errors.New("session expired")
	// ErrConflict means the record changed since it was read; re-read and retry.
	ErrConflict = errors.New("record changed concurrently")
	// ErrSSOSessionEnded means the identity provider already ended the login being signed in.
	ErrSSOSessionEnded = errors.New("SSO session already ended")
)

// Store defines the unified storage contract implemented across SQLite, PostgreSQL, and MySQL.
type Store interface {
	Users() UserStore
	Sessions() SessionStore
	Groups() GroupStore
	Audit() AuditStore
	Settings() SettingsStore

	Driver() string
	Ping(ctx context.Context) error
	Close() error
}

// UserStore defines repository operations for accounts.
type UserStore interface {
	CreateUser(ctx context.Context, u *User) error
	GetUserByID(ctx context.Context, id string) (*User, error)
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	GetUserBySSO(ctx context.Context, provider, subject string) (*User, error)
	UpdateUser(ctx context.Context, u *User) error
	// UpdateUserProfile writes only username, email, display name, role and status, so a
	// directory sync working from a stale read cannot undo a concurrent password reset or SSO
	// link. It applies only while role and status are still prevRole and prevStatus
	// (the values the caller read), returning ErrConflict otherwise, so a stale read cannot put
	// back a privilege a concurrent write revoked. When it changes either, it deletes the
	// user's sessions in the same transaction.
	UpdateUserProfile(ctx context.Context, u *User, prevRole, prevStatus string) error
	// LinkSSOIdentity moves an account from fromProvider to toProvider, keeping its subject;
	// ErrConflict when the account is no longer on fromProvider.
	LinkSSOIdentity(ctx context.Context, id, fromProvider, toProvider string) error
	ResetAdminPassword(ctx context.Context, userID, newHash string) error
	CompletePasswordChange(ctx context.Context, userID, oldHash, newHash, ip string) error
	DeleteUser(ctx context.Context, id string) error
	ListUsers(ctx context.Context, offset, limit int, search string) ([]*User, int, error)
	// ListSCIMUsers is ListUsers restricted to accounts SCIM created (SCIMManaged).
	ListSCIMUsers(ctx context.Context, offset, limit int, search string) ([]*User, int, error)
	CountUsers(ctx context.Context) (int, error)
	// LastDirectoryEvent is the newest directory-webhook timestamp recorded for the subject,
	// 0 if none.
	LastDirectoryEvent(ctx context.Context, provider, subject string) (int64, error)
	// RecordDirectoryEvent records ts as applied for the subject; it never moves back.
	RecordDirectoryEvent(ctx context.Context, provider, subject string, ts int64) error
}

// SessionStore defines repository operations for active login sessions.
type SessionStore interface {
	CreateSession(ctx context.Context, s *Session, expectedPasswordHash string) error
	GetSession(ctx context.Context, tokenHash string) (*Session, error)
	DeleteSession(ctx context.Context, tokenHash string) error
	DeleteUserSessions(ctx context.Context, userID string) error
	// EndSSOSubject ends the provider subject's sessions whose credentials were verified
	// before cutoff and fences that subject's earlier logins until fenceUntil.
	EndSSOSubject(ctx context.Context, provider, subject string, cutoff, fenceUntil time.Time) error
	// EndSSOSession ends every session of an SSO login and fences its sid until fenceUntil.
	EndSSOSession(ctx context.Context, sid string, fenceUntil time.Time) error
	CleanExpiredSessions(ctx context.Context) error
}

// GroupStore defines repository operations for SCIM and RBAC groups.
type GroupStore interface {
	CreateGroup(ctx context.Context, g *Group) error
	GetGroupByID(ctx context.Context, id string) (*Group, error)
	GetGroupByName(ctx context.Context, name string) (*Group, error)
	UpdateGroup(ctx context.Context, g *Group) error
	DeleteGroup(ctx context.Context, id string) error
	ListGroups(ctx context.Context, offset, limit int) ([]*Group, int, error)
	AddGroupMember(ctx context.Context, groupID, userID string) error
	RemoveGroupMember(ctx context.Context, groupID, userID string) error
	GetUserGroups(ctx context.Context, userID string) ([]*Group, error)
}

// AuditStore logs security events.
type AuditStore interface {
	LogAudit(ctx context.Context, r *AuditRecord) error
	ListAuditRecords(ctx context.Context, offset, limit int) ([]*AuditRecord, int, error)
}

// SettingsStore handles persistent key-value configuration.
type SettingsStore interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, val string) error
	DeleteSetting(ctx context.Context, key string) error
	GetAllSettings(ctx context.Context) (map[string]string, error)
}
