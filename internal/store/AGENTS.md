# Storage Layer

## Purpose
Provides the unified Database Abstraction Layer (DAL) supporting pluggable backends (SQLite zero-CGO default and PostgreSQL enterprise) with automated dialect-aware migrations.

## Ownership
Owns data models, store interfaces (`UserStore`, `SessionStore`, `GroupStore`, `AuditStore`, `SettingsStore`), dialect translations, and schema migrations.

## Local Contracts
- `CompletePasswordChange` atomically compares the old password, updates a flagged local account, clears the flag, deletes sessions and records `auth.password_changed`. Session issuance locks the same user row against the verified hash.
- `UpdateUserProfile` writes only username, email, display name, role and status; directory syncs (SCIM, KyIdentity) use it so a stale read cannot restore a replaced password hash or undo an SSO link. `LinkSSOIdentity` is the only provider change (a compare-and-swap from the expected provider, `ErrConflict` otherwise); KyIdentity adoption uses it. It is a compare-and-swap on the role and status the caller read (`ErrConflict` otherwise) and deletes the user's sessions in the same transaction when either changes. `UpdateUser` writes every column it knows.
- Usernames are unique regardless of letter case: `CreateUser`, `UpdateUser` and `UpdateUserProfile` refuse a case variant of another account's name (`ErrAlreadyExists`), checked inside the write's transaction (SQLite has one connection; PostgreSQL takes `pg_advisory_xact_lock(2, hashtext(LOWER(name)))`). An account keeping its own name (by the database's `LOWER`, never Go's folding) passes, so a pre-existing pair stays updatable. `migrations.EnsureUsernameIndex` runs at every open and builds the unique `LOWER(username)` index only when no such duplicates exist; otherwise it returns each clash, `newSQLStore` logs it, and nothing is renamed.
- `users.scim_managed` (migration 9) marks accounts SCIM created; `ListSCIMUsers` lists only those. Migration 10 adds `sso_directory_events`: `RecordDirectoryEvent` keeps the newest applied directory-webhook timestamp per provider and subject (it never moves back) and `LastDirectoryEvent` reads it (0 if none). Username checks on PostgreSQL use advisory lock key space 2.
- Local MFA is retired: `totp_secret_enc`, `totp_enabled`, `totp_last_counter`, `recovery_codes_hash` and the `mfa_challenges` table keep their data, but `userColumns` and every write leave them out. Do not drop them in a migration without an owner decision.
- `ResetAdminPassword` reactivates a local administrator with the replacement flag set and shares the atomic grant purge and audit path with `CompletePasswordChange`; it also works for disabled accounts.
- Migration 5 rebuilds `device_pairings` without the six-digit code column and with `authenticated_at`; pending pairings (90 s) are dropped on upgrade.
- Migration 7 deletes SSO sessions without a `sid` (pre-migration-6 sessions no logout can reach) and every SSO user's device pairing, then adds `device_pairings.sso_sid`; local sessions and pairings, and SSO sessions with a `sid`, stay (`TestMigration7DropsUnreachableSSOSessions`).
- Migration 8 drops `device_pairings`: QR device pairing was removed because no client used it.
- Migration 6 adds `sessions.sso_sid`, `sso_logouts` (by `sid`) and `sso_subject_logouts` (by provider and subject, with a `cutoff`; it never moves back). `EndSSOSession` and `EndSSOSubject` commit their fence before deleting sessions, and `CreateSession` checks both fences only after its insert commits, so under read committed a logout racing a login always ends it; a fenced create deletes its row and returns `ErrSSOSessionEnded`. The subject fence matches sessions by `created_at`, so it relies on SSO sessions being dated by the ID token's `iat`. Expired fences are pruned on the next call that writes one.
- `store.Open(ctx, cfg)` initializes and auto-migrates the configured database backend.
- SQLite runs in WAL mode with foreign keys enabled.
- PostgreSQL queries are rebound dynamically from standard positional parameters.

## Verification
- `go test -v ./internal/store/...`

## Child DOX Index
None.
