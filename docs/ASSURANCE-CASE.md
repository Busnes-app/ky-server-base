# Assurance case

[Overview](../README.md) · [Restore runbook](RESTORE.md) · [Contributing](../CONTRIBUTING.md)

This is the argument that ky-server-base meets its security requirements: what it protects, from
whom, where the trust boundaries are, and why the design and the code hold at each one. Every
Busnes.app server is built from this scaffold, so each product inherits this case and adds its
own. The binding contracts live in [AGENTS.md](../AGENTS.md) and the package `AGENTS.md` files
(`internal/auth`, `internal/api`, `internal/sso`, `internal/config`, `internal/crypto`); this page
ties them together and points at the evidence. Paths are relative to the repository root.

## What the scaffold must protect

1. **Only the right person signs in.** Passwords, second factors and sessions cannot be guessed,
   replayed, forged or read from storage.
2. **Only administrators reach privileged functions**, and an account still on its bootstrap
   password reaches nothing but the password change.
3. **Federated identity is accepted only when it verifies**: OIDC tokens and SCIM bearer credentials
   are checked before any claim or change is trusted.
4. **Secrets at rest stay sealed** from anyone who copies the database without the key.
5. **Backups can be opened only by the recovery custodians**, never by this server or KyRecovery.
6. **Forwarded headers cannot be forged** to change who the server thinks a client is.

## Threat model

| Attacker | Can | Wants |
|---|---|---|
| Anonymous internet client | Send any request through the reverse proxy | Take over an account, guess passwords, exhaust resources |
| Signed-in ordinary user | Use their own session | Reach admin routes or other users' data |
| Malicious web page in the user's browser | Make cross-site requests with the user's cookies | Act as the user (CSRF), inject script |
| Forged identity provider or directory | Send tokens or provisioning requests | Sign in as someone, change roles |
| Someone with the database file or a backup | Read the SQLite file, a database dump or a restored capsule | Sessions, recovery tokens, retired TOTP secrets |
| Someone who can write the shared database | Change rows every replica reads | Force replica refusals, change pins (they already control the data) |
| Network attacker at KyRecovery pairing | Answer the pairing request | Substitute a recovery key whose shares they hold |

Out of scope: an attacker who runs code on the host or holds both the database and
`encryption.key` (they are the server), and a malicious administrator.

## Trust boundaries

1. **Browser → reverse proxy → server.** `X-Forwarded-For` is honoured only from peers listed in
   `KY_TRUSTED_PROXIES`, walking from the right past trusted hops; `X-Real-IP` is ignored
   (`internal/auth`, `ClientIP`). A list entry that trusts every address is refused at startup
   (`internal/config/config.go`).
2. **Server → identity providers.** OIDC goes through `golang.org/x/oauth2` and `coreos/go-oidc`
   with PKCE S256 and a nonce (`internal/sso/oauth.go`). The incompatible directory webhook is retired; KyIdentity uses the SCIM
   connector below. KyIdentity
   back-channel logout tokens are verified like ID tokens plus the logout-token claims
   (`internal/sso/backchannel.go`). No SAML
   assertion route is exposed.
3. **SCIM clients → server.** A bearer token compared in constant time; SCIM sees only the accounts it
   created or safely adopted from KyIdentity and cannot grant admin unless `KY_SCIM_ALLOW_ADMIN` (`internal/scim/handler.go`).
   Adoption matches the immutable subject first; a username match with a different
   nonempty `externalId` returns 409 without mutation. Local rows are never adopted.
   Sign-in maps a present per-app `roles` claim independently of the global role;
   `roles: []` is an ordinary user.
4. **Server → its database.** Secrets (the KyRecovery token, retired TOTP secrets) are AES-256-GCM
   sealed; sessions are stored only as SHA-256 hashes; passwords as Argon2id hashes from `ky-primitives/password`.
5. **Server → KyRecovery.** Capsules are sealed to the suite recovery key before they leave
   (`ky-primitives/recoveryclient`); the key is pinned write-once, and pairing over plain HTTP is
   refused ([Disaster recovery](DISASTER-RECOVERY.md#why-tls-matters-here)).

## Secure design principles applied

- **Least privilege.** Privileged routes are wrapped in `requireAdmin` where they are registered,
  in one place (`internal/api/server.go`, `routes()`); `authz_test.go` pins the exposure of each.
  A `must_change_password` session reaches only `me`, password change and sign-out.
- **Fail-safe defaults.** Production startup refuses a missing `KY_SESSION_SECRET` and a
  trust-everything proxy list; forwarded headers are ignored until a proxy is named; private
  KyRecovery destinations are refused unless `KY_BACKUP_ALLOW_PRIVATE_RECOVERY` is set.
- **Complete mediation.** Session lookup on every request rejects inactive accounts and deletes
  their session; a SCIM status or role change revokes the user's sessions.
- **Economy of mechanism.** Eight direct Go modules; OAuth/OIDC and SCIM come from maintained
  libraries rather than local parsers.
- **Open design.** Only published algorithms (Argon2id, AES-256-GCM, HMAC-SHA-256, SHA-256,
  PKCE S256); the code and this argument are public.

## Common weaknesses and how they are countered

| Weakness | Counter | Where |
|---|---|---|
| Password guessing, credential stuffing | Argon2id hashes; at least 12 characters; login limited to 20 a minute per client and 10 failures per account per client per 15 minutes (IPv6 by /64, keyed by `ClientIP`), checked before hashing and answered like a wrong password; unknown names cost a dummy verification; a solved PoW buys one attempt. MFA is KyIdentity's | `internal/api/auth_handlers.go`, `internal/auth/pow.go`, `internal/auth/policy.go`, `TestLoginPerAccountWindow` |
| Rate-limit allowlist abused or mistyped | Office networks skip only the per-address windows, within a shared budget of 300 a minute per limit that also keeps their traffic from evicting live limiter windows; per-account windows (reserved before hashing), CAPTCHA and later checks still run. The match uses the `ClientIP` result, so an untrusted peer's `X-Forwarded-For` claims nothing. Ranges broader than /16 or /48 and loopback are refused; only administrators change the list and each change is audited | `internal/api/settings_handlers.go`, `internal/api/exempt_networks_test.go`, `ky-primitives/netallow` |
| Session theft | Random 32-byte token, only its SHA-256 stored; `HttpOnly`, `SameSite=Lax`, `Secure` when configured | `internal/auth/session.go`, `internal/store/sqlstore.go` |
| CSRF | Credentialed writes need the CSRF cookie echoed in a header, compared in constant time; CORS admits only the exact `KY_APP_URL` origin. Sign-in (no token yet) refuses `Sec-Fetch-Site` cross-site/same-site and any `Origin` other than the request host or `KY_APP_URL`. On https every cookie is `__Host-`, so a sibling host cannot plant a session, CSRF or SSO state cookie (login CSRF by cookie injection) | `internal/auth/session.go`, `internal/api/server.go`, `TestCookieAuthenticatedWriteRequiresCSRF`, `TestForeignOriginSignInIsRefusedWithoutFetchMetadata`, `TestHTTPSCookiesAreHostCookies` |
| Broken access control | Admin wrapper on every privileged route; per-role tests for each | `internal/api/server.go`, `internal/api/authz_test.go` |
| Forged federated identity | ID token signature, issuer, audience, expiry and one-time nonce verified; PKCE S256 | `internal/sso/oauth.go` |
| Session outliving its identity-provider login | Back-channel logout token verified (signature, issuer, audience, expiry, logout event, no nonce) before ending every session of its `sid`, or of its subject issued before it; the `sid` or the subject's cutoff is fenced so an in-flight callback cannot revive it | `internal/sso/backchannel.go`, `internal/store/sqlstore.go`, `TestVerifyLogoutToken`, `TestBackchannelLogoutEndsTheSSOLogin` |
| Undersized imported signing keys | JWKS RSA moduli below 2048 bits are rejected before the upstream verifier caches them; session signing secrets require 16 bytes; the retired webhook setting is ignored | `internal/sso/jwks_strength.go`, `internal/config/config.go`, `internal/sso/key_strength_test.go` |
| Weak TLS peers or HTTP IdP downgrade | Supported process profile enables Go's verified-chain key policy, requires HTTPS OIDC endpoints and refuses redirects; normal trust/hostname verification remains required | [Strong profile](STRONG-CRYPTO.md), `internal/sso/oauth.go`, `TestOIDCStrongModeRejectsWeakTLSPeer` |
| Identity confusion after username reuse | SCIM refuses adoption when a nonempty externalId differs from the KyIdentity subject; local rows stay excluded | `internal/scim/handler.go`, `TestSCIMCreateTakesOverTheKyIdentityAccount` |
| SQL injection | Parameterised statements throughout (`?`, rebound per driver); the only `Sprintf` builds the SQLite DSN | `internal/store` |
| Cross-site scripting, clickjacking | React escaping with no `dangerouslySetInnerHTML`; CSP `script-src 'self'`, `frame-ancestors 'none'`; `X-Frame-Options: DENY` | `web/src`, `internal/api/server.go` |
| Oversized requests | Request bodies capped at 1 MiB, smaller on backup routes | `internal/api/server.go`, `internal/api/backup_handlers.go` |
| Header spoofing | `ClientIP` and the proxy allow-list; fuzzed | `internal/auth`, `internal/config`, `FuzzClientIP`, `FuzzParseTrustedProxies` |
| Weak randomness | `crypto/rand` only; fresh 12-byte GCM nonce per encryption | `internal/crypto/crypto.go` |
| Vulnerable dependencies | `govulncheck` and `npm run audit` on every push and non-draft pull request; production audit is strict, the full audit has one expiring development-tool exception ([scope](NPM-AUDIT.md)); Dependabot | `.github/workflows/ci.yml` |
| Supply-chain tampering | Actions pinned by commit; base images pinned by digest; the published image is the tested one, attested and verified before `:latest` moves | `.github/workflows/ci.yml`, `Dockerfile`, [RESTORE.md](RESTORE.md) |

## Residual risk

- **SQLite rate limits/replay records are per process**; SQLite is restricted to one local
  server. PostgreSQL coordinates admission and CAPTCHA spending atomically across replicas;
  distributed source addresses still need edge admission controls.
- **Application replicas do not make PostgreSQL highly available.** Its replication,
  failover and storage redundancy stay the operator's ([Replicas](REPLICAS.md)).
- **A database writer can deny service** by editing the replica fingerprint (startup refuses)
  or the recovery key record (backups refuse); either fails closed.
- **Pairing with KyRecovery is trust on first use** unless the operator compares the key ID or
  pins the key by hand.
- **No step-up re-authentication** for destructive admin actions; admin-only is the scaffold's
  control (`internal/api/AGENTS.md`).
- **No independent security audit** has been done.

## Evidence

- **Tests:** the whole suite under the race detector on SQLite and again on PostgreSQL 17;
  `authz_test.go` for route exposure; `scripts/smoke-test.sh` against the built binary; vitest
  and Chromium regressions against the production server for the UI.
- **Static analysis:** CodeQL (`.github/workflows/codeql.yml`: Go, JavaScript/TypeScript, Actions) on every
  push and non-draft pull request, `go vet`, `govulncheck`, `npm audit`, the TypeScript compiler in strict
  mode, and ShellCheck on `scripts/`.
- **Dynamic analysis:** the race detector; bounded fuzzing in CI for the trusted-proxy parser,
  `ClientIP`, the proof-of-work check and AES-GCM.
- **Review:** the `Protect Main` ruleset requires every CI job by name to pass before `master` moves. It does not require a pull request, and organisation administrators can bypass it.
- **Published images:** each `master` commit's image is attested; [RESTORE.md](RESTORE.md) shows
  how to verify one and pin its digest.

This case is updated when a trust boundary or a security requirement changes. A pull request that
moves one must update this page in the same change.

## Replica coordination and migration

PostgreSQL is a shared security boundary: security windows use its clock and atomic upserts;
CAPTCHA spending uses a unique salt. Database errors deny admission. All replicas require
consistent secrets, origin, IdP and proxy policy; the environment is an operator trust boundary.
SQLite retains its existing local security cost and excludes a concurrent server/migration
with an OS file lock. Old binaries predate that lock and must be stopped by the operator.

Scheduled-worker ownership uses a reserved PostgreSQL session and is cancelled when a bounded
probe fails. Session loss is not exactly-once execution of external effects; jobs require
cancellation and idempotency. SQLite locks are local and cannot coordinate network storage.
The migration command opens the original SQLite read-only, upgrades a private snapshot,
refuses unknown schemas/populated targets, and verifies normalized contents inside the target
transaction. Source data/keys/configuration remain available for rollback before new writes.
PostgreSQL capsules copy every scaffold table from one `REPEATABLE READ READ ONLY`
transaction into a SQLite file in a 0700 scratch directory under the data directory, removed
after sealing; a directory a killed process left is removed at the next start (only real
directories of this user whose lock no running process holds; symlinks are not followed).
Plaintext leaves the process only inside a sealed `kycap/3`, and KyRecovery stays a blind
store. Unknown schemas are refused, never sealed in part. `restore` deletes
every session (all bearer credentials minted before the capsule), turns deposits off and
drops the replica fingerprint; restores go into an empty database and never write the old one.

**Health probes.** `/livez` and `/readyz` are unauthenticated and answer only a status word
(no error text, driver or version); readiness pings at most once a second behind a mutex,
so probe floods cannot load the database. The proxy may keep both internal.

**Replica configuration.** At startup on PostgreSQL, under the bootstrap lock, each shared
setting is fingerprinted as HMAC-SHA256 keyed by the encryption key, so someone who reads the
database cannot brute-force low-entropy values such as URLs or network lists. A replica that
differs refuses to start, naming fields, never values; a differing encryption key is
reported alone and is never accepted. `accept-replica-config` is the one audited
(`admin.replica_config_accepted`, field names only) way to rewrite the record, never an
environment switch left on. `internal/config/replica_test.go` fails when a `Config` field is
neither fingerprinted nor classified as per host. The record
never appears in the settings API or logs.

**Recovery key across replicas.** The pinned public key is also stored in settings (public
data). Pins, pairings and every write of the pin, that record or a `recovery.pub` take one
database-wide `recovery-pin` lock and read the pin back before reporting success. Conditional
database inserts preserve the first key ID and topology even while a stale owner's context
is still live during lock-loss detection. Cancellation follows the next ownership probe;
it does not fence writes. The library's write-once rule therefore also holds across replicas
that no longer share `recovery.pub`. A
replica writes its missing file only through the library's `StoreRecoveryKey`, which refuses
a key other than the pin; a local file that differs from the pin refuses startup; a pin no
replica can load, or a record without a pin, is reported and never guessed.

Pairing URL and sealed-token replacements and removals commit in one database transaction
through the product adapter over `recoveryclient`. Readers resolve both rows from one SQL
snapshot per operation, so a failed or concurrent refresh cannot send a previous bearer
credential to a replacement URL. Existing credentials keep their storage format and seal
label. An already-running deposit may finish using the coherent pairing it read before
unpair; the KyRecovery administrator separately revokes that token.

**Replica proxy.** The Compose replica overlay binds HAProxy to loopback, publishes no app or
database port, keeps PostgreSQL on an internal network with a required password, pins images
by digest and trusts only HAProxy's `/32` (plus an explicit edge address, refused unless the
proxy publishes on the default loopback) for `X-Forwarded-For`. HAProxy retries only GET/HEAD
that got an empty answer, never a timed-out request; its 8-second upstream timeout and the
server's 14-second request deadline bound ordinary requests stalled on a frozen database,
including backup status and management. Only deposit, drill and export-capsule have the long
budget. The bundled PostgreSQL has its own volume, so the base profile's public-password volume
is never reused; its TCP readiness probe waits for the final server after initialization.
Replicas share only the capsule archive, whose writes are serialized by `backup-run`.

Tests: `internal/store/coordination_test.go`, `internal/store/migrate_postgres_test.go`,
`internal/store/snapshot_test.go`, `internal/api/replicas_test.go`,
`internal/api/health_test.go`, `internal/api/replica_key_test.go`,
`internal/backup/postgres_test.go`, `internal/backup/pin_test.go`,
`internal/backup/scratch_test.go`, `internal/config/replica_test.go`,
`cmd/server/replica_test.go`, `cmd/server/startup_test.go`,
`cmd/server/migrate_postgres_test.go`, `cmd/server/pgbackup_test.go`,
`internal/worker/worker_test.go` and `e2e/compose_test.go`.
