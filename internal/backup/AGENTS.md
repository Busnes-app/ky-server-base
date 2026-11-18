# Backup

## Purpose
Adapts the scaffold to `github.com/Busnes-app/ky-primitives/recoveryclient`, which owns the
KyRecovery pairing, sealing, deposit, restore and drill contract. This package supplies only
what differs per product: a `Settings` adapter over `store.SettingsStore`, a `Sealer` under the
deployment key, the payload the scaffold seals (`Collect`), and the drill's verification
checks (`Checks`).

## Ownership
Owns the settings adapter (`settings.go`), payload collection (`payload.go`), and restore-drill
checks (`drill.go`) and serialized drill entry point (`run_drill.go`). It holds no private key, no share, and no pairing state of its own — those
live in `recoveryclient` and in the settings rows it reads and writes through the adapter.

## Local Contracts
- `Settings` maps `store.ErrNotFound` to `recoveryclient.ErrNotFound`; every other error passes
  through unchanged. Each adapter reads URL and sealed token from one settings snapshot.
  Use a fresh adapter for each operation. `StorePairing` and `ClearPairing` run the library's
  validation/sealing or removal against captured changes, then commit both existing rows
  through `SettingsStore.UpdateSettings`. Runtime writers must use these wrappers, not
  the library's separate row writes. Persisted credentials and their seal label stay unchanged.
- `NewSealer` seals the KyRecovery token under the deployment key with label
  `ky_server_base:setting:kyrecovery_token`, domain-separated so a row copied from another
  setting will not open.
- `Collect(ctx, cfg, st, appVersion)` snapshots SQLite with the lib's `SQLiteSnapshot`
  (`VACUUM INTO`; the store runs in WAL mode, so a plain file read misses uncheckpointed
  commits; `st` is unused) and PostgreSQL with `st.SnapshotToSQLite` into a 0700
  `snapshot-*` scratch directory under the data directory, removed on return; both become
  the same `data/ky_server.db` member. `store.ErrSnapshotSchema` becomes
  `ErrNoDatabaseSnapshot` (412). It returns `ErrNoDatabaseSnapshot` for any other driver,
  and (`CheckDatabasePath`) for a SQLite DSN
  other than `<DataDir>/ky_server.db`, the only path a capsule restores to, so a capsule without a consistent database is
  never sealed. A snapshot larger than the capsule member limit is refused with
  `capsule.ErrCapsuleTooLarge` (HTTP 413) before it is read into memory, and one over 80% of
  it is logged. Splitting a large database into several capsule members is not built; the
  library limit is never raised here. It also carries the encryption key (`data/encryption.key`, required — restores
  a database whose sealed KyRecovery token is unreadable otherwise) and the pinned recovery public key
  (`data/recovery.pub`, only when paired).
- `Checks(dir, opened)` reads the opened capsule's manifest, normalizes JSON lists and
  fails malformed or incomplete recipes. Required files include all capsule members and
  the database, settings and encryption key; SQLite integrity and required environment
  checks cannot be disabled; an unset `KY_PORT` or `KY_DB_DRIVER` passes because the
  config defaults apply, any other unset expected variable fails. File checks accept only clean relative manifest members;
  SQLite opens read-only and missing/empty databases fail.
- HTTP and CLI call `RunDrill`, which holds an OS advisory lock on `<data dir>/drill.lock`
  across scratch preparation and the library drill. Contention returns `ErrDrillBusy`;
  closing the descriptor or process exit releases ownership. Keep the lock file in place.
  The Unix lock matches the Linux container deployment.
- `DrillRoot` is under the data directory and forced to 0700; opened payloads stay in the
  library's private, disposable subdirectories. Drills use throwaway keys, not custodian shares.
- `PinRecoveryKey` is the only pin path (HTTP pin and pairing): the lib's write-once
  `StoreRecoveryKey` under the `recovery-pin` lock (waiting up to 10 s, then `ErrPinBusy`),
  every write on the lock's context and pin rows read back. `Settings.Set` uses atomic
  conditional inserts for the key ID and topology, so a stale lock owner cannot overwrite
  another pin before the heartbeat cancels it. A newly created `recovery.pub` is removed on
  failure. The public `recovery_public_key` record follows the pin; a failed record write
  is logged and backfilled by sync. `SyncRecoveryKey(ctx, cfg, st)` (PostgreSQL only) plans
  without the lock and writes only under it, re-planning there: a missing file from a
  record that matches the pin, a missing or stale record from a file that matches the pin.
  Unexpected synchronization errors are `ErrRecoveryKeySync`, preserving the original
  cause. A missing local file never turns that failure into `ErrNotPaired` in `Run`.
  A file naming another key is `ErrKeyMismatch` (startup refuses); a pin with no record and
  no file, a record without a pin, or a record that disagrees with the pin is
  `ErrRecoveryKeyUnavailable` (startup logs it and starts; status, export, deposit and
  `Run` report it with the repair, under 200 characters for the audit row). `Run`, the
  HTTP and CLI exports, the status route and startup call it. The lib's pin row names are
  unexported; `TestPinRowNames` fails if they drift. `RecordRecoveryKeyFromFile` is
  `migrate-postgres`'s: it records a source's `recovery.pub` when it is the pin and refuses
  a pin with no matching file.
- Snapshot scratch (`scratch.go`): `snapshotScratch` makes a 0700 `snapshot-*` directory and
  holds a `flock` on it until release; `RemoveStaleSnapshots` (startup, `cmd/server`
  `prepareStart`) removes only real directories of this user whose lock is free, never
  follows a symlink and checks the path still names the locked inode.
- The local backup directory may be shared by replicas: its only writer and pruner is the
  lib's `WriteLocalCopy`, reached only through `Run`, which runs under `backup-run` (the
  scheduler's lock, or `RunOwned`). Export and drill never write there. Keep it that way.
- `Members` names what `Collect` would seal now, for the status route and the screen; keep
  the two in step.
- `GET /api/settings`'s `extra_settings` never carries `kyrecovery_token_enc` or a legacy
  plaintext `kyrecovery_token`; the filter drops every key with the `kyrecovery_token`
  prefix, so a rename in the lib cannot leak. An admin sees the paired `kyrecovery_url`,
  never the credential.
- `RunOwned` coordinates HTTP/CLI deposits with the scheduler through `backup-run`.
  It returns the library's `ErrInProgress` when another process owns a run.
- `Run` (`run.go`) is the only caller of `recoveryclient.Run` in the server, scheduler and
  CLI. Cancelling its context aborts the snapshot and the upload; settings writes use
  `context.WithoutCancel`, so a receipt already returned is recorded. An interrupted run
  restores the previous `backup_last_attempt` row (or deletes it when there was none) and
  returns `ErrInterrupted`. A run that fails for another reason keeps its stamp.
  `backup_last_attempt` is the lib's unexported key; `run_test.go` fails if it changes.
- Pairing, the write-once key pin, the lib's `Run` (one seal, every destination), the schedule, local
  copies and their pruning, drill mechanics, restore and the decrypt guard are the lib's;
  their contracts are in the `recoveryclient` README. `client_test.go` pins only what this
  package's wiring buys: `KY_BACKUP_ALLOW_PRIVATE_RECOVERY` admits RFC1918 and CGNAT and
  nothing else.

## Verification
- `go test -v ./internal/backup/...` (and with `KY_TEST_POSTGRES_DSN`: `postgres_test.go`,
  sealed-only deposit, 0700 scratch removal, unknown-schema refusal; `pin_test.go`, concurrent
  pins with an overlap detector, lock loss, backfill racing a pin, materialization, and
  unavailable pins; `scratch_test.go` the startup sweep) covers decoded seal/open checks, malformed recipes,
  subprocess lock contention/exit, scratch cleanup and the synthetic v0.5.0 pairing fixture
  in `testdata/pairing-v050.json`. The fixture uses a 32-byte 0x01 deployment key and retains
  no recovery private key.

## Child DOX Index
None.
