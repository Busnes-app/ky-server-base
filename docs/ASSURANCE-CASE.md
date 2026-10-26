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
3. **Federated identity is accepted only when it verifies**: OIDC tokens and directory webhooks
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
| Forged identity provider or directory | Send tokens or webhooks | Sign in as someone, change roles |
| Someone with the database file or a backup | Read the SQLite file or a `pg_dump` | TOTP secrets, sessions, recovery tokens |
| Network attacker at KyRecovery pairing | Answer the pairing request | Substitute a recovery key whose shares they hold |

Out of scope: an attacker who runs code on the host or holds both the database and
`encryption.key` (they are the server), and a malicious administrator.

## Trust boundaries

1. **Browser → reverse proxy → server.** `X-Forwarded-For` is honoured only from peers listed in
   `KY_TRUSTED_PROXIES`, walking from the right past trusted hops; `X-Real-IP` is ignored
   (`internal/auth`, `ClientIP`). A list entry that trusts every address is refused at startup
   (`internal/config/config.go`).
2. **Server → identity providers.** OIDC goes through `golang.org/x/oauth2` and `coreos/go-oidc`
   with PKCE S256 and a nonce (`internal/sso/oauth.go`). KySignOn directory webhooks are
   HMAC-SHA256 signed with at least 16 key bytes and accepted only within five minutes (`internal/sso/kysignon.go`). No SAML
   assertion route is exposed.
3. **SCIM clients → server.** A bearer token compared in constant time (`internal/scim/handler.go`).
4. **Server → its database.** TOTP secrets are AES-256-GCM sealed; sessions and recovery codes are
   stored only as SHA-256 hashes; passwords as Argon2id hashes from `ky-primitives/password`.
5. **Server → KyRecovery.** Capsules are sealed to the suite recovery key before they leave
   (`ky-primitives/recoveryclient`); the key is pinned write-once, and pairing over plain HTTP is
   refused ([README](../README.md#why-tls-matters-here)).

## Secure design principles applied

- **Least privilege.** Privileged routes are wrapped in `requireAdmin` where they are registered,
  in one place (`internal/api/server.go`, `routes()`); `authz_test.go` pins the exposure of each.
  A `must_change_password` session reaches only `me`, password change and sign-out.
- **Fail-safe defaults.** Production startup refuses a missing `KY_SESSION_SECRET` and a
  trust-everything proxy list; forwarded headers are ignored until a proxy is named; private
  KyRecovery destinations are refused unless `KY_BACKUP_ALLOW_PRIVATE_RECOVERY` is set.
- **Complete mediation.** Session lookup on every request rejects inactive accounts and deletes
  their session; a webhook status or role change revokes the user's sessions.
- **Economy of mechanism.** Eight direct Go modules; OAuth/OIDC and SCIM come from maintained
  libraries rather than local parsers.
- **Open design.** Only published algorithms (Argon2id, AES-256-GCM, HMAC-SHA-256, SHA-256, TOTP
  per RFC 6238, PKCE S256); the code and this argument are public.

## Common weaknesses and how they are countered

| Weakness | Counter | Where |
|---|---|---|
| Password guessing, credential stuffing | Argon2id hashes; at least 12 characters; per-address limits on login, MFA and pairing (20, 20 and 10 a minute) keyed by `ClientIP` | `internal/api/auth_handlers.go`, `internal/api/device_handlers.go`, `internal/auth/policy.go` |
| Session theft | Random 32-byte token, only its SHA-256 stored; `HttpOnly`, `SameSite=Lax`, `Secure` when configured | `internal/auth/session.go`, `internal/store/sqlstore.go` |
| CSRF | Credentialed writes need the CSRF cookie echoed in a header, compared in constant time; CORS admits only the exact `KY_APP_URL` origin | `internal/auth/session.go`, `internal/api/server.go`, `TestCookieAuthenticatedWriteRequiresCSRF` |
| Broken access control | Admin wrapper on every privileged route; per-role tests for each | `internal/api/server.go`, `internal/api/authz_test.go` |
| Forged federated identity | ID token signature, issuer, audience, expiry and one-time nonce verified; PKCE S256 | `internal/sso/oauth.go` |
| Undersized imported signing keys | JWKS RSA moduli below 2048 bits are rejected before the upstream verifier caches them; configured HMAC keys require 16 bytes and sync checks again before mutation | `internal/sso/jwks_strength.go`, `internal/config/config.go`, `internal/sso/key_strength_test.go` |
| Weak TLS peers or HTTP IdP downgrade | Supported process profile enables Go's verified-chain key policy, requires HTTPS OIDC endpoints and refuses redirects; normal trust/hostname verification remains required | [Strong profile](STRONG-CRYPTO.md), `internal/sso/oauth.go`, `TestOIDCStrongModeRejectsWeakTLSPeer` |
| Forged directory webhook | HMAC-SHA256 checked in constant time before any change; five-minute window | `internal/sso/kysignon.go`, `internal/crypto/crypto.go` |
| SQL injection | Parameterised statements throughout (`?`, rebound per driver); the only `Sprintf` builds the SQLite DSN | `internal/store` |
| Cross-site scripting, clickjacking | React escaping with no `dangerouslySetInnerHTML`; CSP `script-src 'self'`, `frame-ancestors 'none'`; `X-Frame-Options: DENY` | `web/src`, `internal/api/server.go` |
| Oversized requests | Request bodies capped at 1 MiB, smaller on backup routes | `internal/api/server.go`, `internal/api/backup_handlers.go` |
| Header spoofing | `ClientIP` and the proxy allow-list; fuzzed | `internal/auth`, `internal/config`, `FuzzClientIP`, `FuzzParseTrustedProxies` |
| Weak randomness | `crypto/rand` only; fresh 12-byte GCM nonce per encryption | `internal/crypto/crypto.go` |
| Vulnerable dependencies | `govulncheck` and `npm audit --audit-level=high` on every push and pull request; Dependabot | `.github/workflows/ci.yml` |
| Supply-chain tampering | Actions pinned by commit; base images pinned by digest; the published image is the tested one, attested and verified before `:latest` moves | `.github/workflows/ci.yml`, `Dockerfile`, [RESTORE.md](RESTORE.md) |

## Residual risk

- **Rate limits are per process** and per address; a distributed guesser is slowed by Argon2id,
  not stopped.
- **Postgres deployments have no sealed backup**; their `pg_dump` is plaintext and their own to
  protect ([README](../README.md#disaster-recovery)).
- **Pairing with KyRecovery is trust on first use** unless the operator compares the key ID or
  pins the key by hand.
- **No step-up re-authentication** for destructive admin actions; admin-only is the scaffold's
  control (`internal/api/AGENTS.md`).
- **No independent security audit** has been done.

## Evidence

- **Tests:** the whole suite under the race detector on SQLite and again on PostgreSQL 17;
  `authz_test.go` for route exposure; `scripts/smoke-test.sh` against the built binary; vitest
  and Chromium regressions against the production server for the UI.
- **Static analysis:** CodeQL (GitHub default setup: Go, JavaScript/TypeScript, Actions) on every
  push and pull request, `go vet`, `govulncheck`, `npm audit`, the TypeScript compiler in strict
  mode, and ShellCheck on `scripts/`.
- **Dynamic analysis:** the race detector; bounded fuzzing in CI for the trusted-proxy parser,
  `ClientIP`, the proof-of-work check and AES-GCM.
- **Review:** the `Protect Main` ruleset requires every CI job by name to pass before `master` moves. It does not require a pull request, and organisation administrators can bypass it.
- **Published images:** each `master` commit's image is attested; [RESTORE.md](RESTORE.md) shows
  how to verify one and pin its digest.

This case is updated when a trust boundary or a security requirement changes. A pull request that
moves one must update this page in the same change.
