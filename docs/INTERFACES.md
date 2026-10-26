# External interface reference

This describes the HTTP, command-line and environment interfaces of this scaffold.
Derived products can add routes and settings; consult their own references as well.
HTTP routing lives in [server.go](../internal/api/server.go), with handlers linked
below. [README.md](../README.md) covers installation and backup configuration;
[RESTORE.md](RESTORE.md) covers recovery operations.

## HTTP conventions

Use the configured `KY_APP_URL` as the base URL and HTTPS at the production reverse
proxy. JSON requests use `Content-Type: application/json`; successful JSON responses
use the same media type. Request bodies are capped at 1 MiB, with smaller limits
for key pinning (8 KiB) and schedule updates (1 KiB). A decoding failure, including
an oversized JSON body, normally returns 400 rather than a dedicated 413.

Authenticate with the `ky_session` cookie or `Authorization: Bearer <session-token>`.
The bearer token takes precedence. The session cookie is HttpOnly and SameSite=Lax;
`KY_COOKIE_SECURE` controls Secure. Sessions expire after seven days. SCIM uses a
separate configured bearer token; it does not accept a login session as provisioning
authority. The directory-sync webhook uses a signature instead of a login session.

For an unsafe method (anything except GET, HEAD or OPTIONS), sending a nonempty
`ky_session` cookie requires `X-CSRF-Token` to equal the `ky_csrf` cookie. This also
applies when an Authorization header is present alongside the cookie. Login, MFA
verification and the signed directory-sync webhook are exempt. A bearer-only client
should omit session cookies. CSRF failure is 403 `{"error":"Invalid CSRF token"}`.

CORS permits credentials only for the exact configured application origin. An
OPTIONS request returns 200, or 403 plain text for a different nonempty origin.
Permitted request headers are Content-Type, Authorization, X-CSRF-Token and
X-KySignOn-Signature. Use same-origin browser calls for the alternate webhook
signature header.

Application errors normally have `{"error":"message"}`. Protected routes return
401 without a valid session, or 403 for insufficient privilege. Restricted local
sessions receive 403 with `code:"password_change_required"` on protected operations;
they may check identity, replace their password and sign out. Settings fall back
to their public tier. Account status must be `active` for session authentication.
Malformed input normally returns 400; throttling returns 429; internal failures
return 500. SCIM has its own error format, described below.

The tables name the intended methods. Some older read/SSO handlers are registered
without method restrictions; do not rely on extra methods. The SPA catch-all can
return HTML with 200 for an unknown path or wrong-method backup request. Check the
response media type and documented body, not just the status code.

## Authentication

Sources: [auth handlers](../internal/api/auth_handlers.go),
[password handler](../internal/api/password_handlers.go),
[session cookies](../internal/auth/session.go), [PoW format](../internal/auth/pow.go).

| Method and path | Input and authority | Successful output |
|---|---|---|
| GET `/api/auth/pow-challenge` | Anonymous; no body | 200 challenge object described below |
| POST `/api/auth/login` | Anonymous JSON: `username`, `password`, optional `captcha_token` | 200 authenticated user and session/CSRF cookies, or an MFA transaction |
| POST `/api/auth/mfa/totp` | Anonymous JSON: `mfa_token`, `code` | 200 authenticated user and session/CSRF cookies |
| POST `/api/auth/mfa/recovery-code` | Anonymous JSON: `mfa_token`, `code` | 200 authenticated user and session/CSRF cookies |
| GET `/api/auth/me` | Optional session; no body | 200 `{"authenticated":false}` or `{"authenticated":true,"user":User}` |
| POST `/api/auth/change-password` | Restricted local session; JSON: `current_password`, `new_password`; cookie CSRF applies | 200 `{"password_changed":true,"login_required":true}`; all sessions revoked |
| POST `/api/auth/logout` | Optional session; no body; cookie CSRF applies | 200 `{"logged_out":true}`; current session revoked and cookies expired |

An ordinary completed login returns `{"authenticated":true,"user":User}`. `User`
contains `id`, `username`, `email`, `display_name`, `role`, `status`, `sso_provider`,
`totp_enabled`, `must_change_password`, `created_at` and `updated_at`; `sso_subject`,
`push_device_id` and `last_login_at` appear when set. User timestamps are RFC 3339
strings. Roles used by the scaffold are `admin`, `user` and `manager`; only `admin`
authorizes administrator routes. Password hashes, TOTP secrets/counters and recovery
code hashes are never included.

With TOTP enabled, password login instead returns
`{"mfa_required":true,"mfa_token":"…","methods":["totp","recovery_code"]}`.
The transaction lasts five minutes and is consumed by the verification attempt,
including a wrong code: start a fresh password login to retry. TOTP counters and
recovery codes are single-use. Login allows 20 attempts per IP per minute; the two
MFA routes share 20 attempts per IP per minute and five per account per five minutes.
Invalid credentials/transactions/codes return 401; inactive or ineligible accounts
and failed CAPTCHA return 403.

The PoW challenge has `algorithm:"SHA-256"`, `salt`, `challenge`, `maxnumber`,
`expires_at` (Unix seconds) and `signature`. Find an integer `number` from 1 through
`maxnumber` whose lowercase hex SHA-256 of UTF-8 `salt + decimal(number)` equals
`challenge`. Add `number` to the unchanged challenge object, encode that JSON in
standard Base64 (padding optional), and submit it as `captcha_token`. The signed
challenge expires after five minutes. Login verifies it when the configured provider
is `pow`; no Turnstile/Friendly server-side verification is wired here.

Password replacement is specifically for a local account flagged
`must_change_password`, not a general password-edit endpoint. Otherwise it returns
409. Both passwords are limited to 1024 bytes; the new value must differ and pass
the current policy (at least 12 bytes after trimming surrounding whitespace).
The current password must verify. Limits are ten attempts per IP and five per
account per minute. Success requires signing in again with the replacement.

## Device pairing

Sources: [handlers](../internal/api/device_handlers.go),
[pairing service](../internal/devices/devices.go).

| Method and path | Input and authority | Successful output |
|---|---|---|
| POST `/api/devices/pair/init` | Active unrestricted session; no body; cookie CSRF applies | 200 `{secret, expires_at, qr_payload}` |
| POST `/api/devices/pair/verify` | Anonymous JSON: `secret`, `device_name`, `platform`, optional `push_token`; cookie CSRF applies if a session cookie is sent | 200 `{paired:true, session_token, device_name}` and session/CSRF cookies when session issuance succeeds |
| GET `/api/devices/pair/poll` | Anonymous query parameter `secret`; no body | 200 `{status, expires_at, device_name}` |

The QR secret is 24 random bytes represented by 48 hex characters, expires after
90 seconds and is single-use. It is not a six-digit code. `qr_payload` is a JSON
string with `action:"ky_pair"`, `app_name`, `app_url`, `secret` and `expires`.
Expiry numbers use Unix seconds. Device name, platform and push token are limited
to 255, 32 and 4096 bytes respectively. Pair-init allows ten requests per account
per minute; verification allows ten per IP per minute.

Unknown, expired or consumed secrets are uniformly 400 `pairing not found or expired`
on verification. Poll without a secret is 400; a lookup miss is 404. Poll exposes
neither the secret, user ID nor push token. Statuses are drawn from `pending`,
`approved`, `consumed` and `expired`; this verifier moves a pending record directly
to consumed. The derived session retains the initiating session's authentication
time. The current handler can return `paired:true` with an empty `session_token`
if session issuance fails; require a nonempty token and verify `/api/auth/me`
before considering the device signed in.

## Settings

Source: [settings handlers](../internal/api/settings_handlers.go).

| Method and path | Input and authority | Successful output |
|---|---|---|
| GET `/api/settings` | Optional session; no body | 200 tiered settings object |
| POST `/api/settings/theme` | Administrator; JSON `{"theme":"name"}`; cookie CSRF applies | 200 `{"theme":"name"}` |

Public settings are `app_name`, `app_url`, `theme` (stored value or `patina`),
`captcha_provider` and `sso_enabled`. An unrestricted session additionally receives
`scim_enabled` and `db_driver`. Administrators receive an `extra_settings` map of
stored settings; it can contain sensitive configuration. All keys beginning with
`kyrecovery_token` are removed, including sealed/legacy token forms. Invalid theme
JSON or an empty name is 400; the handler stores a nonempty name without validating
it against the frontend theme list.

## Federation and directory synchronization

Sources: [SSO handlers](../internal/api/sso_handlers.go),
[KySignOn client and payload](../internal/sso/kysignon.go).

| Method and path | Input and authority | Successful output |
|---|---|---|
| GET `/api/sso/kysignon/login` | Browser; no body | 302 to provider authorization URL; five-minute HttpOnly PKCE/nonce cookies |
| GET `/api/sso/kysignon/callback` | Provider query `code`, `state`; matching PKCE/nonce cookies | 302 to `/` and session/CSRF cookies |
| POST `/api/sso/kysignon/sync` | Signed raw JSON body and `X-KySignOn-Signature` (fallback `X-Signature-SHA256`) | 200 `{"synced":true}` |
| GET `/saml/metadata` | Anonymous; no body | 200 XML service-provider metadata |

The callback verifies provider tokens, PKCE and nonce. Invalid state/nonce is 400;
failed exchange is 401; an unprovisioned account is 403 when automatic provisioning
is off. Automatic provisioning creates role `user`; sign-in does not itself grant
administration. The scaffold constructs generic OIDC and SAML helpers, but registers
no generic OIDC login/callback or SAML assertion-consumer handler. In particular,
the `/saml/acs` URL named in the metadata is not an implemented login route here.

The sync signature is the lowercase hex HMAC-SHA256 of the exact request bytes
under `KY_KYSIGNON_HMAC_SECRET`, without a `sha256=` prefix. JSON fields are `event`,
`id` (provider subject), `username`, `email`, `display_name`, `role`, `status` and
`timestamp` (Unix seconds within five minutes in either direction). Supported
events are `user.created`, `user.updated`, `user.deactivated` and `user.deleted`.
Create/update defaults an empty role to `user` and status to `active`; privilege or
status changes revoke sessions. Deactivation sets `inactive` and revokes sessions;
deletion removes the matching provider user. Unknown events currently return
`synced:true` without changing a user. An unconfigured HMAC secret, invalid signature,
bad timestamp or processing error returns 401; an unreadable body returns 400.

## SCIM 2.0

Base path: `/scim/v2`. Source: [SCIM adapter](../internal/scim/handler.go).
The pinned `github.com/elimity-com/scim` dependency owns protocol parsing and
[RFC 7643](https://www.rfc-editor.org/rfc/rfc7643) /
[RFC 7644](https://www.rfc-editor.org/rfc/rfc7644) serialization. Every endpoint,
including discovery, requires `Authorization: Bearer <KY_SCIM_TOKEN>` and SCIM
must be enabled. Use `application/scim+json` for request/response bodies. Do not send
browser session cookies; otherwise the shared CSRF rule also applies to writes.

| Method and path | Input | Output |
|---|---|---|
| GET `/scim/v2/ServiceProviderConfig` | None | 200 capabilities and bearer authentication scheme |
| GET `/scim/v2/Schemas` | Optional SCIM query parameters | 200 schema ListResponse |
| GET `/scim/v2/Schemas/{id}` | Schema URN as ID | 200 schema resource |
| GET `/scim/v2/ResourceTypes` | None | 200 resource-type ListResponse |
| GET `/scim/v2/ResourceTypes/{id}` | `User` or `Group` | 200 resource-type description |
| GET `/scim/v2/Users` | `startIndex`, `count`, optional `filter` | 200 user ListResponse |
| POST `/scim/v2/Users` | Core User resource | 201 created User resource |
| POST `/scim/v2/Users/.search` | RFC 7644 SearchRequest | 200 user ListResponse; see filter limitation below |
| GET `/scim/v2/Users/{id}` | User ID | 200 User resource |
| PUT `/scim/v2/Users/{id}` | Replacement User resource | 200 User resource |
| PATCH `/scim/v2/Users/{id}` | RFC 7644 PatchOp | 200 User resource |
| DELETE `/scim/v2/Users/{id}` | User ID | 204, no body |
| GET `/scim/v2/Groups` | `startIndex`, `count` | 200 group ListResponse |
| POST `/scim/v2/Groups` | Core Group resource | 201 created Group resource |
| POST `/scim/v2/Groups/.search` | RFC 7644 SearchRequest | 200 group ListResponse; filtering is not applied by this adapter |
| GET `/scim/v2/Groups/{id}` | Group ID | 200 Group resource |
| PUT `/scim/v2/Groups/{id}` | Replacement Group resource | 200 Group resource |
| PATCH `/scim/v2/Groups/{id}` | RFC 7644 PatchOp; see limitations below | 200 Group resource |
| DELETE `/scim/v2/Groups/{id}` | Group ID | 204, no body |

List responses have `schemas`, `totalResults`, `startIndex`, `itemsPerPage` and
`Resources`; pagination is one-based and capped at 200 results per page. User
attributes persisted here are `userName`, `displayName`, `emails[].value`,
`roles[].value`, `active` and `externalId`; the first supplied email/role value is
used. Missing role on create defaults to `user`. User resources return those
attributes, `id` and metadata, not passwords. `active:false` maps to local `inactive`.
Changing role/status invalidates sessions. Group resources persist `displayName`,
`externalId` and `members:[{"value":"user-id"}]`, with `id` and metadata on output.

Adapter limits matter when configuring a provisioning connector:

- User list filtering recognizes `userName`, `email` or `displayName` with `eq` and
  a quoted value, then passes that value to the store's shared search. It is not
  attribute-exact RFC filtering. Other query filters return 400 `invalidFilter`.
  User search currently reads this filter from the URL query, not the SearchRequest
  body's filter. Group filtering is not applied.
- User PATCH supports add/replace of active, displayName, userName, emails and
  roles; remove operations are ignored. Group PATCH only applies explicit paths
  into displayName/members; complex member filters and complete RFC patch semantics
  are not implemented by the persistence adapter. Prefer PUT when replacing a group.
- Cross-resource root list/search has no configured root-query handler; `/scim/v2/Me`
  is 501. Bulk is not enabled. Discover capabilities before using optional operations.

Disabled SCIM returns 403. Missing/invalid bearer returns 401 with
`WWW-Authenticate: Bearer error="invalid_token"`. SCIM errors have the standard Error
schema, `status` (string), `detail` and optional `scimType`. Missing resources return
404; duplicate resources 409 `uniqueness`; invalid protocol input 400.

## Recovery and backups

All routes below require an unrestricted administrator session. Cookie CSRF applies
to every write, including the export download. This is the product's administration
API, not KyRecovery's bearer-token deposit API. No route accepts custodian shares.
Source: [backup handlers](../internal/api/backup_handlers.go).

| Method and path | Input | Successful output |
|---|---|---|
| GET `/api/backup/status` | No body | 200 status object described below |
| POST `/api/backup/drill` | No body | 200 `{passed, checks:[{name,passed,message?}], duration_ms, error_message?}` |
| POST `/api/backup/export-capsule` | No body | 200 sealed bytes, `application/octet-stream`, attachment `<capsule-id>.kycap`, `X-Recovery-Key-ID` header |
| POST `/api/backup/pair-remote` | JSON `{recovery_url, pairing_code}` | 200 `{recovery_key_id, threshold, total_shares}` |
| POST `/api/backup/deposit` | No body; collects the configured local payload | 200 backup result described below |
| DELETE `/api/backup/pairing` | No body | 200 `{"paired":false}` |
| POST `/api/backup/pin-key` | JSON `{public_key, threshold, total_shares}` | 200 `{recovery_key_id, threshold, total_shares}` |
| PUT `/api/backup/schedule` | JSON `{interval_sec}` | 200 stored `{interval_sec}` |

Pairing uses a six-digit code from the KyRecovery administrator. The recovery URL
must be HTTPS; redirects and reserved destinations are refused, with an explicit
opt-in for private/CGNAT LAN destinations. Compare the recovery-key fingerprint out
of band. Manual pinning uses standard Base64 of the 1216-byte suite public key and
the ceremony's topology (`2 <= threshold <= total_shares <= 255`). A different
existing pin is 409. Pairing/pinning are write-once; unpair deletes only the URL and
sealed token, leaving the pin, receipts and local copies. A KyRecovery administrator
must separately revoke the remote token. See the [operational guide](../README.md#disaster-recovery).

Schedule seconds must be zero (off) or at least 900; invalid values return 400.
The stored setting overrides the environment default. A drill's HTTP 200 describes
completion of the request, not success of restoration: inspect `passed` and each
check. Drills use a throwaway key and do not prove access to the custodian cards.
Another drill in progress is 409.

Backup results contain `manifest`, `size_bytes`, optional `local_path`, optional
`local_error`, and optional `receipt`. The manifest identifies `capsule_id`,
`service_name`, `app_version`, `created_at`, `payload_hash`, `threshold`,
`total_shares`, `recovery_key_id`, `encapsulated_key` and optional dependency/recipe
metadata. It does not expose the sealed member list. A receipt has `capsule_id`,
`digest`, `size_bytes`, `deposited_at`; the client checks the received SHA-256 digest
against the bytes sent. Timestamps are RFC 3339. Save the receipt for restore
freshness checks. `receipt_unrecorded:true` on a 200 means the remote capsule was
accepted but its local receipt was not recorded; reconcile before claiming a fully
recorded backup. These shared formats are supplied by the pinned ky-primitives module.

Status always includes `paired`, `key_pinned`, `app_name`, `app_version`,
`allow_private_recovery`, `database_driver` and `members` (payload names).
Conditional fields are `recovery_url`, `recovery_key_id`, `threshold`, `total_shares`,
`recovery_key_error`, `last_deposit` (receipt), `local_dir`, `local_keep`,
`local_copies:[{name,size_bytes,created_at}]`, `local_error`, `interval_sec`,
`min_interval_sec` and `next_run_at`. The recovery bearer token is never returned.

Capsule creation currently requires SQLite. No consistent PostgreSQL snapshot is
implemented. Export/deposit return 412 for missing prerequisites (key, destination,
database snapshot, or disallowed private destination as applicable); a PostgreSQL
drill returns 200 with `passed:false`. A changed pin or concurrent deposit is 409;
capsule size-cap failure is 413; remote deposit refusal is 502, which may still
report a successful local copy. Other local failures are 500. Pairing with invalid
input or a failed remote claim is 400; unpair while unpaired is 412. Pair, pin and
deposit may finish after the caller disconnects; read status/audit records before
retrying after an interrupted response.

## Command-line interface

Source: [command dispatcher](../cmd/server/main.go). Commands use the same environment
and data directory as the server unless noted. Do not run them against a live data
directory without following the [restore runbook](RESTORE.md). Diagnostic messages
normally go to stderr. Setup/operational failures return a nonzero exit status.

| Invocation after `ky_server_base` | Inputs and effect | Output |
|---|---|---|
| No subcommand | Load environment, open store, bootstrap if empty, serve HTTP and run configured backup schedule | Startup/operation logs; listener defaults to `:8080` |
| `version` | No inputs or data-directory access | Version text on stdout; use the source commit/image digest for exact build identity |
| `init-admin -username NAME -password VALUE` | Username defaults to `admin`; password required, at least 12 bytes; create/reset local administrator, revoke sessions/challenges/pairings, require password replacement | Confirmation on stderr |
| `backup-drill` | Collect SQLite payload; seal/open with throwaway keys and check restore recipe | Human summary on stdout with PASSED/FAILED and per-check results |
| `export-capsule [-out PATH]` | Require suite pin; seal payload to custodian public key; default output `<capsule-id>.kycap` | File plus capsule/key/size confirmation on stderr |
| `deposit` | Seal once and deliver to configured destinations | Audit record; remote success logs capsule/time/digest on stderr; failures on stderr; no JSON stdout contract |
| `restore -capsule PATH -to DIRECTORY [-service NAME]` | Read custodian shares from stdin, one `ky2-…` line each; target must be empty; expected service defaults to `KY_APP_NAME`, then `Busnes.app` | Authenticated manifest/restore result on stdout; optional terminal prompt on stderr |

`init-admin` currently takes its password in argv, so it can be visible in process
listings or shell history. Custodian shares are accepted only on stdin. A completed
`backup-drill` can exit zero with a FAILED summary; inspect the summary rather than
using exit zero as proof of recovery. Unknown subcommands currently fall through to
starting the server; there is no top-level help command. Flag-bearing subcommands
support `-h`/`-help` without operating on data.

## Environment configuration

Source: [environment loader](../internal/config/config.go). Unless stated otherwise,
nonempty string values are trimmed; invalid integer values use their default.
Booleans accept `true`, `1`, `yes` or `on` (case-insensitive); other explicitly set
values are false. Environment variables configure the process; there is no `.env`
reader in the binary. Docker Compose can supply its `.env` substitutions separately.

| Variable | Default | Meaning |
|---|---|---|
| `KY_HOST` | `0.0.0.0` | HTTP listener address; use network/proxy controls in production |
| `KY_PORT` / `PORT` | `8080` | Integer listener port; KY_PORT wins |
| `KY_APP_URL` | `http://localhost:<port>` | External application origin, redirects and SCIM base URL |
| `KY_APP_NAME` | `Busnes.app` | Application and capsule service identity; preserve it for restore |
| `KY_ENV` | `development` | `production` requires a session secret and defaults to Secure cookies |
| `KY_DB_DRIVER` | `sqlite` | SQLite or `postgres` (`postgresql` alias); other drivers are not implemented |
| `KY_DATA_DIR` | `./data` | Local database/key directory |
| `KY_DB_DSN` | SQLite file under data dir; development PostgreSQL URL when selected | Store connection string; configure production PostgreSQL credentials and TLS explicitly |
| `KY_DB_MAX_OPEN_CONNS` | `25` | Database pool maximum open connections |
| `KY_DB_MAX_IDLE_CONNS` | `5` | Database pool idle connections |
| `KY_SESSION_SECRET` | Fresh 32-byte random hex in development | Required in production; PoW signing secret; supplied values below 16 bytes fail startup |
| `KY_ENCRYPTION_KEY` | Persistent `<data-dir>/encryption.key` | Exactly 32 bytes as hex or standard Base64; encrypts secrets/pairing token; preserve for restore |
| `KY_COOKIE_SECURE` | true only in production | Secure flag and HSTS response header |
| `KY_COOKIE_DOMAIN` | Empty (host-only) | Session/CSRF cookie domain |
| `KY_TRUSTED_PROXIES` | Empty | Comma-separated proxy IPs/CIDRs allowed to supply client forwarding headers; invalid entries and `/0` fail startup |
| `KY_SSO_ENABLED` | `true` | Advertised SSO setting; does not by itself configure an identity provider |
| `KY_KYSIGNON_ISSUER` | Empty | KySignOn OIDC issuer |
| `KY_KYSIGNON_CLIENT_ID` | Empty | Registered KySignOn client ID |
| `KY_KYSIGNON_SECRET` | Empty | Registered client secret |
| `KY_KYSIGNON_HMAC_SECRET` | Empty | Directory-sync webhook HMAC secret; absent means sync refusal; nonempty values below 16 bytes fail startup and use |
| `KY_SSO_AUTO_PROVISION` | `true` | Allow a validated KySignOn login to create a local user |
| `KY_OIDC_ISSUER`, `KY_OIDC_CLIENT_ID`, `KY_OIDC_SECRET` | Empty | Generic OIDC helper configuration; no public login/callback routes are wired |
| `KY_SAML_ENTITY_ID`, `KY_SAML_METADATA_URL` | Empty | SAML helper configuration; only local metadata output is wired |
| `KY_SCIM_ENABLED` | `true` | Enable authenticated provisioning |
| `KY_SCIM_TOKEN` | Fresh random 24-byte hex on each load | Provisioning bearer credential; set explicitly for a stable integration |
| `KY_CAPTCHA_PROVIDER` | `pow` | Login PoW check; `none` disables it; other provider verification is not wired |
| `KY_CAPTCHA_SITE_KEY`, `KY_CAPTCHA_SECRET_KEY` | Empty | Reserved provider configuration; not used by the current PoW verifier |
| `KY_CAPTCHA_POW_DIFFICULTY` | `4` | Maximum puzzle candidate number; nonpositive values fall back to 20000 |
| `KY_BACKUP_DIR` | Empty | Optional local sealed-capsule destination |
| `KY_BACKUP_KEEP` | `7` | Local retention count; must be at least one |
| `KY_BACKUP_DEPOSIT_INTERVAL` | `24h` | Go duration; zero disables; minimum nonzero `15m`; malformed/negative values fail startup |
| `KY_BACKUP_ALLOW_PRIVATE_RECOVERY` | `false` | Permit private/CGNAT HTTPS KyRecovery destinations; loopback/reserved addresses remain refused |
| `KY_ADMIN_PASSWORD` | Random at first bootstrap | Initial local admin password when the store is empty; replacement is mandatory |

Loading configuration can create the encryption-key file. The fixed read/write
timeouts are 15 seconds, session lifetime seven days and database connection lifetime
15 minutes; these are not environment options. Backup deposit extends its response
write deadline to 16 minutes. The [README](../README.md#disaster-recovery) describes
stored schedule precedence, container DNS overrides and backup retention. Compose's
`KY_IMAGE`, `KY_CONTAINER_IP`, `KY_NETWORK_SUBNET` and `KY_DNS` are deployment inputs,
not additional environment-loader options in the binary.

The optional [strong-key configuration](STRONG-CRYPTO.md) supplies the Go runtime
setting `GODEBUG=fips140=on`. It is read by Go, not this environment loader.
Selected mode requires HTTPS OIDC endpoints without redirects; the standard
verifier still checks signatures, issuer, audience, expiry and nonce. In every
mode, JWKS fetching refuses redirects and RSA members below 2048 bits.
