# Storage Layer

## Purpose
Provides the unified Database Abstraction Layer (DAL) supporting pluggable backends (SQLite zero-CGO default and PostgreSQL enterprise) with automated dialect-aware migrations.

## Ownership
Owns data models, store interfaces (`UserStore`, `SessionStore`, `DeviceStore`, `GroupStore`, `AuditStore`, `SettingsStore`), dialect translations, and schema migrations.

## Local Contracts
- `CompletePasswordChange` atomically compares the old password, updates a flagged local account, clears the flag, deletes sessions/MFA challenges/device pairings and records `auth.password_changed`. Session/MFA issuance locks the same user row against the verified hash; MFA challenges persist the creation-time password hash, and consumption returns that snapshot to reject stale completions. Migration 4 discards preexisting challenges because their credential snapshot is unknown.
- `ResetAdminPassword` reactivates a local administrator with the replacement flag set and shares the atomic grant purge and audit path with `CompletePasswordChange`; it also works for disabled accounts.
- Migration 5 rebuilds `device_pairings` without the six-digit code column and with `authenticated_at`; pending pairings (90 s) are dropped on upgrade.
- Migration 7 deletes SSO sessions without a `sid` (pre-migration-6 sessions no logout can reach); local sessions and SSO sessions with a `sid` stay (`TestMigration7DropsUnreachableSSOSessions`).
- Migration 6 adds `sessions.sso_sid`, `sso_logouts` (by `sid`) and `sso_subject_logouts` (by provider and subject, with a `cutoff`; it never moves back). `EndSSOSession` and `EndSSOSubject` commit their fence before deleting sessions, and `CreateSession` checks both fences only after its insert commits, so under read committed a logout racing a login always ends it; a fenced create deletes its row and returns `ErrSSOSessionEnded`. The subject fence matches sessions by `created_at`, so it relies on SSO sessions being dated by the ID token's `iat`. Expired fences are pruned on the next call that writes one.
- `store.Open(ctx, cfg)` initializes and auto-migrates the configured database backend.
- SQLite runs in WAL mode with foreign keys enabled.
- PostgreSQL queries are rebound dynamically from standard positional parameters.
- MFA challenges and device pairings are consumed with database state transitions that permit exactly one successful use.
- Recovery-code hash updates use optimistic concurrency so simultaneous redemption cannot reuse a code.

## Verification
- `go test -v ./internal/store/...`

## Child DOX Index
None.
