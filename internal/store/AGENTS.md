# Storage Layer

## Purpose
Provides the unified Database Abstraction Layer (DAL) supporting pluggable backends (SQLite zero-CGO default and PostgreSQL enterprise) with automated dialect-aware migrations.

## Ownership
Owns data models, store interfaces (`UserStore`, `SessionStore`, `GroupStore`, `AuditStore`, `SettingsStore`), dialect translations, and schema migrations.

## Local Contracts
- `SettingsStore.UpdateSettings` commits related upserts and deletes in one transaction;
  any failure rolls everything back. Recovery pairing uses it for the URL and sealed token.
  `GetAllSettings` returns one statement's snapshot and propagates scan and row errors.
- `Coordination` persists PostgreSQL security windows and spent CAPTCHA salts using database
  time. Refunds match the admitting window's expiry; failures deny admission.
- `WithLock` uses PostgreSQL session advisory key space 3, scoped to current schema and job;
  probe failures cancel work within about two seconds; this does not fence stale writes.
  `SettingsStore.SetSettingIfAbsent` atomically retains the first value and returns it; the
  recovery adapter uses it for write-once key ID and topology rows. The physical connection
  is discarded on exit. SQLite uses
  persistent OS file locks next to its database. Session pooling is required for PostgreSQL.
- PostgreSQL schema migration and username-index creation run under one lock. Pools need
  at least `config.MinPostgresOpenConns` (4): worker lock, backup-run lock, the snapshot
  transaction and one for queries and readiness (`TestSnapshotLeavesAConnectionAtThePoolFloor`).
- `Store.SnapshotToSQLite` (PostgreSQL only) reads every `migrationTables` table in one
  `REPEATABLE READ READ ONLY` transaction on one pooled connection, writes a fresh SQLite file
  opened with `Open` (current scaffold schema), verifies per-table counts and row digests
  against that transaction, then integrity and foreign keys, and leaves one self-contained
  file. An unknown table/column, a missing table or a non-current migration history is
  `ErrSnapshotSchema`; nothing partial is produced. It shares `copyTable`/`tableDigest`/
  `migrationValues` with `MigrateToPostgres`; `migrationTables` is the single ordered list for
  both directions.
- `MigrateToPostgres` validates original migration history before upgrading a private snapshot,
  preflights destination rows before any DDL, refuses unknown source/target schemas and
  populated targets, copies all scaffold tables in one transaction, verifies counts/content,
  and advances the audit sequence. Check-only rolls back rows. Original SQLite stays untouched.
- `CompletePasswordChange` atomically compares the old password, updates a local account, clears any replacement flag, deletes sessions and records `auth.password_changed`. Session issuance locks the same user row against the verified hash.
- `UpdateUserProfile` writes only username, email, display name, role and status; SCIM profile updates use it so a stale read cannot restore a replaced password hash or undo an SSO link. `LinkSSOIdentity` is the only provider change (a compare-and-swap on the expected provider and subject, `ErrConflict` otherwise); KyIdentity adoption uses it. `TakeOverForSCIM` is the only way `scim_managed` changes after creation: `UpdateUserProfile` that also sets it in the same statement, only on an unmanaged `kysignon` row with the expected subject (SCIM taking over a KyIdentity account, all or nothing). `UpdateUserRole` writes the role alone (the KyIdentity sign-in), with the same compare-and-swap and same-transaction session revocation, so it never writes back a profile it read. `UpdateSCIMProfile` is the only subject change: `UpdateUserProfile` that also compares the subject read and, while the row is still `scim`, writes the new subject (SCIM `externalId` before adoption) in the same statement. It is a compare-and-swap on the role and status the caller read (`ErrConflict` otherwise) and deletes the user's sessions in the same transaction when either changes. `UpdateUser` writes every column it knows.
- Usernames are unique regardless of letter case: `CreateUser`, `UpdateUser` and `UpdateUserProfile` refuse a case variant of another account's name (`ErrAlreadyExists`), checked inside the write's transaction (SQLite has one connection; PostgreSQL takes `pg_advisory_xact_lock(2, hashtext(LOWER(name)))`). An account keeping its own name (by the database's `LOWER`, never Go's folding) passes, so a pre-existing pair stays updatable. `migrations.EnsureUsernameIndex` runs at every open and builds the unique `LOWER(username)` index only when no such duplicates exist; otherwise it returns each clash, `newSQLStore` logs it, and nothing is renamed.
- `users.scim_managed` (migration 9) marks accounts SCIM created; `ListSCIMUsers` lists only those. Migration 10's `sso_directory_events` table is retained for upgrade compatibility but unused after the KyIdentity webhook retirement; no stored data is dropped. Username checks on PostgreSQL use advisory lock key space 2.
- Local MFA is retired: `totp_secret_enc`, `totp_enabled`, `totp_last_counter`, `recovery_codes_hash` and the `mfa_challenges` table keep their data, but `userColumns` and every write leave them out. Do not drop them in a migration without an owner decision.
- `ResetAdminPassword` reactivates a local administrator with the replacement flag set and shares the atomic grant purge and audit path with `CompletePasswordChange`; it also works for disabled accounts.
- Migration 5 rebuilds `device_pairings` without the six-digit code column and with `authenticated_at`; pending pairings (90 s) are dropped on upgrade.
- Migration 7 deletes SSO sessions without a `sid` (pre-migration-6 sessions no logout can reach) and every SSO user's device pairing, then adds `device_pairings.sso_sid`; local sessions and pairings, and SSO sessions with a `sid`, stay (`TestMigration7DropsUnreachableSSOSessions`).
- Migration 8 drops `device_pairings`: QR device pairing was removed because no client used it.
- Migration 6 adds `sessions.sso_sid`, `sso_logouts` (by `sid`) and `sso_subject_logouts` (by provider and subject, with a `cutoff`; it never moves back). `EndSSOSession` and `EndSSOSubject` commit their fence before deleting sessions, and `CreateSession` checks both fences only after its insert commits, so under read committed a logout racing a login always ends it; a fenced create deletes its row and returns `ErrSSOSessionEnded`. The subject fence matches sessions by `created_at`, so it relies on SSO sessions being dated by the ID token's `iat`. Expired fences are pruned on the next call that writes one.
- `store.Open(ctx, cfg)` initializes and auto-migrates the configured database backend.
- `SQLitePath` owns path/file-URI decoding for opening, locks and backup checks; encoded
  and plain names of one file must have the same lock identity.
- SQLite runs in WAL mode with foreign keys enabled. `openSQLite` takes a path or a `file:` URI (the same parse as `internal/backup`) and refuses a server URL (`scheme://`) without echoing it, so a PostgreSQL DSN under the sqlite driver never becomes a stray file.
- PostgreSQL queries are rebound dynamically from standard positional parameters.

## Verification
- `go test -v ./internal/store/...`

## Child DOX Index
None.
