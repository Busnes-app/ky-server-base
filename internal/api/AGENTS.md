# API

## Purpose
Exposes HTTP REST routes, authentication endpoints, Single Sign-On callbacks, SCIM endpoints, backup restore drill handlers, and static React PWA hosting.

## Ownership
Owns HTTP routing, request parsing, session cookie validation, CORS headers, and error response formatting.

## Local Contracts
- PostgreSQL uses shared security-window reservations and CAPTCHA spending; never fall
  back to local allowances on storage errors. SQLite keeps its existing in-memory limits.
- A successful login refunds only its original window. Office exemption decisions on
  PostgreSQL read the current stored list per request; diagnostic exemption counts stay local.
- Update `docs/INTERFACES.md` when a route's inputs, outputs, permissions or errors change.
- POST `/api/auth/change-password` accepts any local-account session (restricted or not; federated accounts get 409), current password and a different policy-valid new password. Browser CSRF and per-IP/account limits apply. Success revokes all sessions and requires sign-in again; flagged sessions get `password_change_required` on protected routes and public-only settings.
- `GET|HEAD /livez` and `/readyz` (`health.go`) are unauthenticated, `Cache-Control: no-store`,
  status word only. Readiness is `!draining && Store.Ping` within 1s, cached 1s behind a
  mutex held across the Ping; `StartDraining` (SIGTERM) turns it 503 while other routes serve.
- Pin and pairing handlers pin through `backup.PinRecoveryKey` (database-wide `recovery-pin`
  lock, read back; `fs.ErrExist`/`ErrKeyMismatch` → 409, `ErrPinBusy`/`ErrPinOwnershipLost` → 503); export
  and status call `backup.SyncRecoveryKey` first, and its `ErrRecoveryKeyUnavailable` is the
  status route's `recovery_key_error` and a 412 on export and deposit.
- Pairing refresh and unpair use `backup.StorePairing`/`ClearPairing` to commit the URL and
  sealed credential together; readers use the operation's coherent settings snapshot.
- `ServeHTTP` ends ordinary request contexts before `WriteTimeout` (14s with the 15s
  default), leaving time for an error reply. Only deposit, drill and export-capsule have
  the longer budget; backup status and management stay bounded.
  `TestStalledRequestEndsAtTheWriteDeadline` and `TestFrozenDatabaseStillAllowsAnErrorReply`
  cover the route classification and real listener response.
- Backup status returns 503 on a deadline or failed settings read, never a successful
  guessed unpaired state. `TestBackupStatusDoesNotTurnReadFailuresIntoUnpaired` freezes
  settings after authentication and also covers immediate store errors. A failure confined
  to shared-key synchronization is also 503 for status, export and deposit, not an absent
  pin; `TestBackupEndpointsDoNotHideARecoveryRecordReadFailure` covers a replica without
  its local public-key file and successful materialization on retry.
- All JSON API endpoints return structured errors `{"error": "message"}` upon failure.
- Non-API routes fall back to serving `web.Handler()` for client-side SPA routing.
- New routes are unauthenticated only by deliberate choice; privileged ones are registered wrapped in `s.requireAdmin` in `routes()`, so the trust level of every route is readable in one place.
- Backup routes and theme writes are admin-only: capsules and settings carry site data and secrets. The scaffold has no step-up; admin-only plus `TestPrivilegedEndpointsRequireAdmin` is its equivalent for every destructive backup route. Routes are registered with method patterns, and because the SPA catch-all answers any method, tests pin that a wrong method never reaches a backup handler rather than expecting 405.

| Method | Path | Handler | Response |
|---|---|---|---|
| POST | `/api/backup/drill` | `handleBackupDrill` | `recoveryclient.DrillResult`; 409 when another HTTP/CLI drill holds the data-directory lock |
| POST | `/api/backup/export-capsule` | `handleExportCapsule` | `.kycap` attachment; POST so the CSRF check covers it |
| POST | `/api/backup/pair-remote` | `handlePairRemoteRecovery` | `{recovery_key_id, threshold, total_shares}` |
| POST | `/api/backup/deposit` | `handleRunBackup` | `recoveryclient.Result` (+`receipt_unrecorded`) |
| DELETE | `/api/backup/pairing` | `handleUnpair` | `{paired:false}`; URL and token rows only, key pin stays |
| POST | `/api/backup/pin-key` | `handlePinKey` | write-once; 409 on a different key |
| PUT | `/api/backup/schedule` | `handleSetSchedule` | `{interval_sec}` read back from the store |
| GET | `/api/backup/status` | `handleBackupStatus` | pairing, key, local copies, schedule, members, `database_driver`; never the token |

- `POST /api/backup/deposit` is one `recoveryclient.Run`: seal once, deliver to the local directory and to KyRecovery when paired. 412 no key, key pin missing, no destination, no database snapshot, or a private destination with `KY_BACKUP_ALLOW_PRIVATE_RECOVERY` off; 409 key mismatch or a run in flight; 413 over the capsule caps; 502 when KyRecovery refused (`recoveryclient.ErrRemote`, naming a local copy that was written, so the `ErrPrivateDestination` arm must stay above it: the lib wraps both on the dial path); 500 for a failure before a byte left; 200 with `receipt_unrecorded` when the store holds the capsule but the receipt was not written. It runs through `backup.Run` on the server's `stopping`, which a dropped connection does not cancel and `StopDetached()` does, with a 16-minute write deadline; 503 when shutdown interrupted it. The acting admin is resolved before the upload and the audit row is written on a context detached from the request.
- The write-once, irreversible backup handlers (`handlePairRemoteRecovery`, `handlePinKey`, `handleRunBackup`) write on `context.WithoutCancel(r.Context())` so a dropped connection cannot leave a pin, a pairing or a deposit half-written with no audit row; the deposit's snapshot and upload and the pairing claim run on `stopping`, which only `StopDetached()` cancels, so shutdown ends them in seconds and their writes still land; the idempotent ones (`handleSetSchedule`, `handleUnpair`) stay on the request context. Their routes are registered as `s.tracked(s.requireAdmin(s.handleX))`, so the `detached` counter is incremented the moment `ServeHTTP` dispatches -- before `requireAdmin`'s session lookup, which is itself a store round-trip that `ReadTimeout` (15s) lets outlast `shutdownTimeout` (5s). Registering inside the handler was too late: `Shutdown` returns after its timeout with requests still active, and one still in the auth lookup would leave `WaitDetached()` reading zero and the store closing under a request about to pin a key. The header-read window before `ServeHTTP` is entered cannot be covered by any counter, because no handler goroutine exists yet; `Shutdown`'s own drain is what covers it. The counter is a mutex and a `sync.Cond`, not a `sync.WaitGroup`, which panics when an `Add` from zero races an in-progress `Wait` -- two admin requests at SIGTERM do exactly that. `WaitDetached()` is what `cmd/server` blocks on before closing the store, because `http.Server.Shutdown` returns without knowing these goroutines exist.
- Audit actions: `backup.paired`, `backup.pair_failed`, `admin.backup_run` (details start with `outcome="success|failure"`), `admin.backup_unpair`, `admin.backup_key_pin`, `admin.backup_schedule`, `admin.backup_export` (a downloaded capsule, resource the capsule ID). `AuditDetails` flattens the lib's details map into the bounded audit field with locally derived fields first and remote error text last; values are quoted and `=` is escaped before the final `AuditSafe` cut. `cmd/server` uses it for the scheduler and CLI rows. Details carry key or capsule IDs, digests and paths, never the token.
- POST `/api/sso/kysignon/backchannel-logout` is anonymous; the KySignOn-signed logout token is its authority. It is deliberately not rate limited: a KyIdentity restore sends one logout per login at once, and a 429 would exhaust its retries. `TestBackchannelLogoutEndsTheSSOLogin` covers the sid, fence, subject-wide (including a pending first-login callback), replay and ID-token-confusion paths.
- Rate-limit keys for `login:` and `login-account:` come from `auth.ClientIP` through `limiterIP` (IPv6 by /64), never from `RemoteAddr` or a raw header, so a limit is neither shared by everyone behind a proxy nor bypassable by forging `X-Forwarded-For`.
- CORS permits only the exact configured `KY_APP_URL` origin and credentialed browser writes require matching CSRF cookie/header tokens. Login, which is CSRF-exempt, refuses `Sec-Fetch-Site: cross-site` and `same-site` (a sibling subdomain), and an `Origin` that is neither `r.Host` nor `KY_APP_URL` (plain HTTP sends no Fetch Metadata) (`TestCrossSiteSignInIsRefused`, `TestForeignOriginSignInIsRefusedWithoutFetchMetadata`).
- Office networks (`KY_RATE_LIMIT_EXEMPT_NETWORKS` plus the `rate_limit_exempt_networks` setting, parsed together by `ky-primitives/netallow`) skip only the per-IP windows `login:` and `password-change:`, through `allowPerIP`, which matches the `requestIP` result and nothing from a raw header. Office requests draw on one shared `officeBudget` (300 a minute per limit) and meet the per-IP window once it is spent: 300 a minute for 15 minutes stays under `attemptsCap`, so office traffic cannot evict live account windows. A new per-IP limiter goes through `allowPerIP`; a per-account or instance-wide one never does. GET/PUT `/api/settings/rate-limit-networks` are admin-only; a PUT validates before storing, swaps the live list and audits `admin.rate_limit_networks`. A stored list that fails to load at startup is dropped for the environment list, never widened. `exempt_networks_test.go` pins each of these.
- Login: the per-client window (20/min) runs first, then the CAPTCHA (each solved token spent once across PostgreSQL replicas), then the per-account-and-client window (10 failures / 15 min), reserved with `allowAttempt` before any password hashing (so concurrent guesses cannot outrun it) and released on success or a store error, answered with the same 401 as a wrong password. Unknown or password-less names run `password.DummyVerify`; disabled accounts answer that same 401. There are no local MFA routes.
- GET `/api/admin/users` (admin) is the read-only, paged account list behind the Directory screen; it returns the `adminUser` shape, never credentials or SSO subjects.
- The incompatible `/api/sso/kysignon/sync` webhook and its CSRF exemption are removed. Directory synchronization uses the existing KyIdentity SCIM connector at `/scim/v2`; sign-in and back-channel logout retain their routes.
- The KyIdentity callback adopts the SCIM row whose externalId equals the subject (`adoptSCIMUser`) before provisioning; a username collision is 409 and a disabled account 403. It then applies `IdentityClaims.LocalRole()` (per-app `roles`, else legacy `role`, else unchanged) with `UpdateUserRole` (role alone, so a concurrent SCIM profile edit stays; it revokes the other sessions in the same transaction) before issuing the session.
- Capsule export, restore drill and deposit extend their write deadline to `depositWriteBudget`; the listener's 15 s `WriteTimeout` is for JSON replies.
- API request bodies are capped at 1 MiB and all responses receive baseline CSP, anti-framing, MIME-sniffing, and referrer-policy headers.
- `GET /api/settings` tiers its payload: public fields for the login screen, `db_driver`/`scim_enabled` for any session, and `extra_settings` for admins only; KyRecovery tokens are omitted in both sealed and legacy plaintext forms, dropped by the `kyrecovery_token` key prefix rather than by literal key name, and `replica_config_fingerprint` is dropped too.

## Verification
- `go test -v ./internal/api/...` (`authz_test.go` pins the per-role exposure of every privileged route; `backup_test.go` the backup routes on both drivers; `health_test.go` probes, cache, drain and the request deadline; `replica_key_test.go` cross-replica key materialization, concurrent pins on two replicas and the PostgreSQL unknown-schema 412)
- `scripts/smoke-test.sh` asserts the same boundaries against a running binary

## Child DOX Index
None.
