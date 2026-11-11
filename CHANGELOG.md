# Release notes

[Overview](README.md) · [Restore and image verification](docs/RESTORE.md) · [Strong-key profile](docs/STRONG-CRYPTO.md)

## Reading a release

This scaffold distributes rolling containers identified by their full source commit,
rather than SemVer tags or GitHub Releases. An image publication is a release even
when it changes only documentation, tests or build tools. For a newly published
`:<commit>` image, read **Current source** in this file **at that exact commit**:
`https://github.com/Busnes-app/ky-server-base/blob/<commit>/CHANGELOG.md#current-source`.
Use the attestation/pinning procedure in the restore runbook to map an image digest
to its source. A mutable `:latest` tag does not identify a historical release.

The historical summaries below were reconstructed from source intervals and the
public CI publish jobs on 2026-10-26. Dates are UTC CI run dates, which can differ
from commit dates. They cover all 42 successful image publications found from the
first publishing workflow through `6c296a2`. A successful publish is evidence of
publication at that time, not a claim that its old registry tag is still available
or that its promote job moved `:latest`. Historical images predate later repairs;
use the current trusted owner/image and the README's owner-move instructions.

Each summary compares with the preceding published revision; unpublished
intermediate commits are included in the next published interval. Source-only
development before the first image is summarized with that initial distribution.
These are human summaries, not a raw version-control log.

## Current source

### Changes

- Stopping the server takes seconds, not up to 17 minutes. On SIGTERM a backup in flight
  is interrupted: the database snapshot and the upload to KyRecovery are cancelled, the
  interruption is written to the audit log, and the server exits within 15 seconds (5 s
  HTTP drain, then at most 10 s for the backup to record its outcome). Before, shutdown
  waited for the deposit to finish.
- An interrupted scheduled backup runs again about a minute after the next start instead
  of one interval later. An interrupted `POST /api/backup/deposit` answers 503. A backup
  that fails for any other reason is still retried once per interval.
- `docker-compose.yml` lowers `stop_grace_period` from 20m to 30s.
- Built with Go 1.27.4, which fixes an HTTP/1 server crash from a `MaxBytesReader` header
  race in `net/http` (GO-2026-6859). The API server reaches the affected code.

### Upgrade impact

- Rebuild and restart. No database or configuration migration is added. A deployment
  that copied the old 20-minute `stop_grace_period` or a Kubernetes
  `terminationGracePeriodSeconds` of about 1,200 can lower it to 30 seconds; the old
  values keep working.
- An upload cut off after KyRecovery stored the capsule but before this server read the
  receipt leaves a complete capsule there with no receipt here. The next run deposits a
  new capsule and records its receipt. The local backup directory only ever holds whole
  capsules.
- Requirements from the preceding source revisions still apply; see the archived
  summaries below. GO-2026-6859 is fixed by the Go 1.27.4 toolchain; rebuild to pick it up.

## Maintaining notes

Before merging a change to `master`, replace Current source with its major changes
and upgrade impact. Include restart/rebuild requirements, schema/configuration or
format migrations, deprecated behavior and any known vulnerability/advisory IDs.
Say explicitly when no migration is needed. Archive the previous Current source
summary under its known full commit after it has shipped; keep existing history.
For an upgrade across several revisions, read each intervening summary.

`make notes-check` runs three standard-library tests and compares the current
summary with `HEAD^`. CI fetches two commits: on a pull request the first parent
of the tested merge is the target branch; on a master push it is the preceding
commit. For local review against the target, also run
`python3 scripts/check-release-notes.py --base origin/master` (fetch it first).
An invalid/missing base fails; a base without this file permits initial adoption.
The gate requires nonempty change/upgrade bullets and rejects an unchanged
Current source section even if the archive changes. It checks presence/freshness,
**not factual accuracy or completeness**; reviewers still verify the prose against
the actual change. The required Go job gates publish, and publish gates promotion.

## Historical image publications

### Source summary at 7c3a89b

Source: [7c3a89bf8e893d80fc2d4714a854cc603ab04624](https://github.com/Busnes-app/ky-server-base/commit/7c3a89bf8e893d80fc2d4714a854cc603ab04624).

#### Changes

- Refresh the complete ky-ui source copy and manifest from 0.4.3 to 0.11.0 at
  `801879cda3117ae84442043c6b02b3aeb5202d0f`, restoring the suite's exact-byte
  freshness check. Shared fixes include navigation focus/scrolling, readable muted
  text and theme storage handling. Rebuild the committed browser bundle.
- Adopt the current shared theme catalog. Retired choices fall back to Busnes
  following the operating system; aliases retained upstream resolve normally.
  Appearance lists only current catalog entries and Match system.

#### Upgrade impact

- Rebuild and restart the server to serve the new embedded assets. No database or
  configuration migration is added by this change. Supported saved choices and
  aliases follow the upstream catalog. Removed palette IDs now use Busnes following
  the OS; choose another current theme in Appearance if desired.
- Authentication, directory, cookie and database upgrade requirements from the
  preceding source revision still apply; see the archived source summary below.
  No new known vulnerability identifiers apply to this asset refresh.

### Source summary at 1d88179

Source: [1d8817928307c7c476db7608b124c7be80e5721f](https://github.com/Busnes-app/ky-server-base/commit/1d8817928307c7c476db7608b124c7be80e5721f).

#### Changes

- KyIdentity sign-in applies the role KyIdentity sends, every time: `admin` in the ID
  token's per-app `roles` makes the account an admin here, any other `roles` makes it a
  user, and a token without `roles` uses the legacy `role`. A role change signs the
  account out everywhere else and is audited as `sso.role_changed`. Before, sign-in never
  changed the role, so an admin demoted in KyIdentity stayed admin here unless the signed
  directory webhook was configured.
- The incompatible KyIdentity directory webhook is removed. Directory synchronization
  now uses the existing KyIdentity SCIM connector at `/scim/v2`; the old HMAC-secret
  setting remains accepted and ignored with a startup migration warning.
- SCIM `POST /Users` for someone who already signed in through KyIdentity takes that
  account over (201 with its existing id) instead of creating a second account (when
  `externalId` was the KyIdentity subject) or answering 409 forever (when only the
  `userName` matched without an externalId). A nonempty externalId that differs from
  the username-matched account's KyIdentity subject now returns 409 without changing
  that account, preventing adoption of another person after username reuse.
  Local accounts are never taken over.
- With an https `KY_APP_URL`, the session, CSRF and KyIdentity sign-in cookies are
  `__Host-` cookies (`__Host-ky_session`,
  `__Host-ky_csrf`), so a page on another host under the same domain can no longer plant
  its own session and sign a visitor into the wrong account. A plain-HTTP `KY_APP_URL`
  keeps the old names.
- A SQLite `KY_DB_DSN` that is a server URL (such as a `postgres://` URL in `.env`, while
  compose pins `KY_DB_DRIVER=sqlite`) stops the server at startup. Before, it quietly
  created a SQLite file named after the URL outside the data volume, and the data was lost
  when the container was recreated. A `file:` URI no longer creates a stray directory.
- The restore runbook hands the restored files to root before the first start; before,
  the root container refused the operator-owned `encryption.key` and never started.
- SCIM: a PATCH that removes `externalId` now unlinks it (it answered 200 and kept it),
  and concurrent PATCHes of different attributes of one user no longer undo each other.

#### Upgrade impact

- No database migration. Complete the KyIdentity connector and administrator setup
  below before restarting onto the new image.
- Replace any legacy KyIdentity directory webhook connector with system type `scim`,
  base URL `<KY_APP_URL>/scim/v2`, and the bearer token configured by `KY_SCIM_TOKEN`;
  enable `KY_SCIM_ENABLED=true` here. Disable the old connector. The removed
  `/api/sso/kysignon/sync` endpoint cannot apply updates. Remove
  `KY_KYSIGNON_HMAC_SECRET` after migration; leaving it set is accepted with a warning.
- With an https `KY_APP_URL`, everyone is signed out once by the upgrade and signs in
  again. Scripts that read the `ky_session` or
  `ky_csrf` cookie by name on https must use the `__Host-` names.
- `KY_COOKIE_DOMAIN` or `KY_COOKIE_SECURE=false` with an https `KY_APP_URL` now stops the
  server (and CLI commands that load the configuration) at startup with a message naming
  it: `__Host-` cookies must be Secure and take no domain. Remove them from `.env` before
  upgrading an https install.
- KyIdentity: on the next sign-in, accounts take the role KyIdentity sends for this app.
  KyIdentity always sends this app's `roles`: before upgrading, register the app in
  KyIdentity and assign its explicit per-app `admin` role to every administrator
  who signs in through it, or they become users here at their next sign-in. Local accounts (`init-admin`)
  are unaffected and stay a way back in. A present empty `roles: []` always means
  ordinary user, even if a legacy global `role: "admin"` is also present.
- SCIM: an IdP that creates a user who already signed in through KyIdentity now gets that
  account (201) and manages it from then on: matched by `externalId` = KyIdentity subject,
  else by `userName` only without a nonempty `externalId`. An authoritative ID
  mismatch now returns 409; resolve the identity conflict instead of adopting the row.
  An `externalId` already managed under another name is 409.
- A SQLite install whose `KY_DB_DSN` is a `scheme://` URL now refuses to start; it was
  never writing to the data volume. Before upgrading such an install, find its database
  (`docker compose exec app ls -R /app`: a directory named after the URL), then stop the
  container without removing it (`docker compose stop app`; a clean close folds the
  write-ahead log into the file) and copy the file out with `docker compose cp`. To keep
  that data, put it at `data/ky_server.db` in an otherwise empty `data/` (keep the old
  files aside) and remove `KY_DB_DSN` from `.env`; there is no SQLite-to-PostgreSQL import. For PostgreSQL,
  set `KY_DB_DRIVER=postgres` in the compose file or an overlay.


### 2026-11-04 — ab2c472

Source: [ab2c472fccbce7388c2f420bf557584f2a524100](https://github.com/Busnes-app/ky-server-base/commit/ab2c472fccbce7388c2f420bf557584f2a524100) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/41818132521)

- **Changes:** `restore` turns scheduled deposits off in the restored database and says so, so a drill or migration run beside the server still in service cannot deposit a stale copy under the same pairing. The restore runbook deletes the restored sessions before the server first starts, and no longer asks admins to re-enrol a second factor. `docker-compose.yml` passes `.env` to the container (optional `env_file`), so `KY_TRUSTED_PROXIES`, `KY_KYSIGNON_*`, `KY_SSO_AUTO_PROVISION` and every other documented setting can be set there; values the compose file sets itself still win.
- **Upgrade impact:** No migration. Compose: every variable in `.env` now reaches the container on the next `docker compose up -d`; check `.env` for leftover `KY_ENV`, `KY_COOKIE_*`, `KY_KYSIGNON_*` or `KY_TRUSTED_PROXIES` lines, which now take effect. Compose 2.24.0 or later is required. A copy restored with `restore` does not deposit until an admin turns the schedule back on.

### 2026-11-04 — 1354417

Source: [13544179d898057cc4a90de26f8e95ff8b3b4a1b](https://github.com/Busnes-app/ky-server-base/commit/13544179d898057cc4a90de26f8e95ff8b3b4a1b) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/41789647162)

- **Changes:** KyIdentity directory webhooks are ordered per subject: an event older than the last one applied for that `id` is acknowledged and dropped, so a replayed or late event can no longer re-promote, reactivate or recreate an account. A webhook event without `id` is refused (401). A `user.created`/`user.updated` event for a subject SCIM already provisioned adopts that account, as sign-in does, instead of creating a second one. Any local account can change its own password from the key button in the header, after confirming the current one; every session is signed out. Before, only an account flagged for replacement could. SCIM: a PUT with an empty `roles` value, or a roles replace with an empty value, sets the role to `user` (an admin kept the role and its sessions before). `externalId` sent after creation is stored until a KyIdentity sign-in adopts the account (it was dropped with 200). Concurrent group PATCHes no longer drop each other's members, and renaming a group onto a taken name is 409 `uniqueness` instead of 500. Sign-in: a browser `Origin` that is neither the server's host nor `KY_APP_URL` is refused like a cross-site request, closing login CSRF over plain HTTP where browsers send no `Sec-Fetch-Site`. After a sign-in the web app checks that the browser kept the session, and says to open the https address when a Secure cookie was dropped on a plain-HTTP page. Backups refuse a database snapshot too large for a capsule (413) before reading it into memory. Startup warns when `KY_APP_URL` is https and `KY_TRUSTED_PROXIES` is empty. CI: the `reuse` licensing check now gates image publication.
- **Upgrade impact:** Migration 10 runs on start and adds the `sso_directory_events` table. No data changes. KyIdentity: a directory event carrying an older `timestamp` than one already taken for the same subject is now ignored (still 200). A failed event must be retried by the sender; older events for that subject are ignored until it is. Events without `id` get 401. SCIM: an IdP that sends an empty `roles` value now demotes the user to `user`; a PUT that omits `roles` keeps the role as before. `externalId` updates now take effect on accounts that have not signed in through KyIdentity yet. Login: requests from a browser page on another origin get 403; API clients that send no `Origin` are unaffected. `POST /api/auth/change-password` now works for every local account (it answered 409 unless the account was flagged); federated accounts still get 409.

### 2026-11-04 — da9f246

Source: [da9f2469a2f7001b26af4827775eebb511e285ba](https://github.com/Busnes-app/ky-server-base/commit/da9f2469a2f7001b26af4827775eebb511e285ba) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/41767990120)

- **Changes:** Local multi-factor sign-in is removed: MFA is a KyIdentity feature. The `/api/auth/mfa/totp` and `/api/auth/mfa/recovery-code` routes, the login page's verification step and the `totp_enabled` user field are gone. Stored TOTP secrets, recovery codes and MFA challenges stay in the database, unused. Sign-in hardening: an IPv6 client is limited by its /64; ten failed passwords for one account from one client close that pair's window for 15 minutes, checked before any hashing and answered with the same 401 as a wrong password, so nobody elsewhere can lock the account out. Unknown names cost the same hashing as known ones, disabled accounts answer 401 (was 403), and a solved CAPTCHA buys one attempt. `KY_CAPTCHA_POW_DIFFICULTY` defaults to 500000 (was 4): a median solve of about one second, measured in Chromium. Login from a sibling subdomain (`Sec-Fetch-Site: same-site`) is refused like cross-site. The login CAPTCHA no longer needs WebCrypto, so signing in over plain HTTP on a LAN address (the default compose install) works; before, it failed with 403 every time. Sign In waits until the check is solved, and a failed check offers Try again. KyIdentity sign-in adopts the SCIM-provisioned account whose `externalId` is the KyIdentity subject instead of failing with a 500; a username collision is 409 and a disabled account 403. SCIM sees and changes only the accounts it created: local accounts, the bootstrap admin included, are 404 to it. SCIM may not grant `admin` unless the new `KY_SCIM_ALLOW_ADMIN=true`. SCIM group PATCH applies add/remove/replace to the current members (an incremental "Add" no longer replaces the whole group, and "Remove" with a value list no longer leaves only that user); member sub-attribute paths are refused before any write. `GET /Groups?filter=` filters by `displayName`/`externalId` instead of returning every group. Filtered user PATCH paths such as `emails[type eq "work"].value` and removes now apply; unstored attributes are ignored. SCIM and KyIdentity directory writes are a compare-and-swap on role and status and revoke sessions in the same transaction, so a concurrent stale write cannot put back an admin role or active status. Usernames are unique regardless of letter case for new and renamed accounts. The Directory & SCIM page lists every account, with its source, through the new admin-only `GET /api/admin/users`. `backup-drill` exits 1 when the drill fails. Backups refuse to seal a SQLite database that is not `<KY_DATA_DIR>/ky_server.db`, the only path a capsule restores, and the Backup page says why. `init-admin` refuses to reset an existing account that is not an active local administrator unless `-promote` is given, and prints its previous role and status. `KY_COOKIE_SECURE` defaults to true when `KY_APP_URL` is https, and `docker-compose.yml` now takes `KY_APP_URL` from `.env`. `scripts/ky-init.sh` no longer copies `.env` into a new product and points its image and repository coordinates at the new app.
- **Upgrade impact:** Migration 9 runs on start: it adds `users.scim_managed` (set for existing SCIM accounts). On every start the server builds a case-insensitive unique index on usernames; if accounts already differ only by letter case it skips the index, logs each clash and keeps running. Rename or remove all but one of each pair to get the index. Accounts that had local TOTP now sign in with their password alone. Require MFA in KyIdentity and have users sign in through it. Nothing is deleted. A disabled account's login now answers 401 instead of 403, and repeated wrong passwords for one account from one client now answer 401 for 15 minutes. The login CAPTCHA takes about a second to solve at the new default; set `KY_CAPTCHA_POW_DIFFICULTY` to override. Automated clients that reused one solved token now need a fresh one per login. SCIM: an IdP can no longer see or change local accounts, nor make anyone an admin, unless `KY_SCIM_ALLOW_ADMIN=true`. Group PATCH and filtered user PATCH now do what the request says; an unsupported member path is 400 `invalidPath`. Behind an https `KY_APP_URL`, the session cookie is now `Secure` and HSTS is sent; set `KY_COOKIE_SECURE=false` explicitly to keep the old behaviour. Compose users who set `KY_APP_URL` in `.env` now get it in the container. A SQLite `KY_DB_DSN` outside `<KY_DATA_DIR>/ky_server.db` disables backups (412 and a failed drill) until the database is moved there. Scripts that ran `backup-drill` and ignored its output now see exit status 1 on a failed drill. `init-admin` on an existing non-admin account needs `-promote`.

### 2026-11-03 — 413232c

Source: [413232c57f56269b58963763fe028e58e9c11496](https://github.com/Busnes-app/ky-server-base/commit/413232c57f56269b58963763fe028e58e9c11496) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/41722086207)

- **Changes:** SCIM: a `userName`, `emails` or `displayName` `eq` filter now matches that attribute exactly (case-insensitive) instead of any substring of any of the three, so an IdP's lookup of `ann@corp.com` no longer returns `joann@corp.com`. A blank value is 400 `invalidFilter`, and an `emails`/`displayName` value matching more than 1000 accounts as a substring is 400 `tooMany`. SCIM: a group member id that does not exist is refused with 400 before anything is written, and replacing a group writes only the membership difference, so one bad id no longer leaves the group half-rewritten behind a 500. SCIM and KySignOn directory updates write only profile, role, status and SSO identity columns, so a sync cannot undo a concurrent password reset, MFA change or recovery-code spend. KySignOn `user.deactivated`/`user.deleted` events now fail (401) when the account lookup fails, instead of acknowledging and doing nothing. Sign-in: the login CAPTCHA solves one challenge (renewed every four minutes) instead of fetching and solving new ones in a loop for as long as the page is open. After a rejected MFA code the page returns to the password step, because the server has already spent that transaction. A browser `POST` to the login or MFA routes labelled `Sec-Fetch-Site: cross-site` is refused with 403 (login CSRF). Directory & SCIM page: shows why the account list could not load instead of "No users provisioned yet", and asks for `KY_SCIM_TOKEN` instead of offering placeholder text to copy as the token. Backup page: the PostgreSQL warning says no capsule is made at all, and pinning the recovery key asks for confirmation with the k-of-n split. Capsule export and the restore drill get the same 16-minute write budget as a deposit, so a large database is not cut off at the 15-second reply timeout. The drill no longer fails its environment check when `KY_PORT`/`KY_DB_DRIVER` are unset and their defaults apply. OIDC discovery and token exchange use a 10-second client timeout in every mode, not only under strong crypto. `docker-compose.yml` forwards `KY_SESSION_SECRET` and `KY_SCIM_TOKEN` from `.env`, so the restore runbook's rotation (edit `.env`, `docker compose up -d`) takes effect, and the optional `postgres` profile publishes 5432 on 127.0.0.1 only. `make clean` no longer deletes `data/` and `backups/` (the database, encryption key, recovery pin and local capsules); it removes build output only.
- **Upgrade impact:** No migration. Restart onto the new image. Compose: values for `KY_SESSION_SECRET` and `KY_SCIM_TOKEN` already in `.env` now reach the container on the next `docker compose up -d`; unset or empty, the server still mints fresh ones at every start. A `KY_SESSION_SECRET` there that is shorter than 16 bytes now stops the server at startup, as it already did outside compose; fix or remove it before upgrading. Anything that reached the `postgres` profile's 5432 from another host must now go through the compose network or an SSH tunnel. SCIM clients that relied on substring matching from an `eq` filter see exact matching now.

### 2026-11-03 — ba7adf5

Source: [ba7adf535954093c08564c99bd314f79aaf6c8dc](https://github.com/Busnes-app/ky-server-base/commit/ba7adf535954093c08564c99bd314f79aaf6c8dc) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/41647539283)

Covers every publication after c81a19b, including the 2026-11-03 dependency updates (#91-#95).

- **Changes:** Documentation only: the README's Container IP address and Disaster recovery sections moved verbatim to [docs/CONTAINER-IP.md](docs/CONTAINER-IP.md) and [docs/DISASTER-RECOVERY.md](docs/DISASTER-RECOVERY.md). Links to the old README sections land on its Documentation list, which links the new pages. The README has a License section with an [OctoCounts](https://octocounts.com) badge showing the repository's line counts, followed by a Documentation section listing the operator and contributor documents. CI only, same checks: the browser regressions run in the pinned Playwright container instead of installing Chromium and its system packages on every run, the four bounded fuzz runs move from the SQLite Go job to the PostgreSQL job so the two run in parallel, and the vulnerability scan no longer saves the Go cache the test jobs need. The login page's single sign-on link reads "Continue with Single Sign On" (was "KySignOn Identity"), and the "Or continue with Single Sign-On" caption above it is gone so the phrase appears once. CI only: pushes to `master` each get their own concurrency group, so a push run queued behind another is no longer dropped. Superseded pull-request runs are still cancelled. CI only: CodeQL moved from default setup to an advanced workflow with the same languages and query suite, so the docs/ screenshot tooling is excluded. Removed QR device pairing: the sidebar's "Pair Device" button, its "Link Mobile Device" dialog and the anonymous `/api/devices/pair/init`, `/verify` and `/poll` routes. No app could scan the code. Migration 8 drops the `device_pairings` table.
- **Upgrade impact:** The login link's text changed; its address and behavior did not. Migration 8 runs on start and deletes stored device pairings. Calls to `/api/devices/pair/*` now get the web app's page, like any unknown path; no client shipped.

### 2026-10-28 — c81a19b

Source: [c81a19b34f3bec481f6bd852213428e034c582d9](https://github.com/Busnes-app/ky-server-base/commit/c81a19b34f3bec481f6bd852213428e034c582d9) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/40646793340)

- **Changes:** Every file now states its license and copyright in machine-readable form, following the [REUSE](https://reuse.software/) specification 3.3. `REUSE.toml` labels the project's own files MIT; the vendored IBM Plex Mono and Space Grotesk fonts keep the SIL Open Font License 1.1 with their own copyright, and the built `web/dist` bundle names React, lucide-react and qrcode under MIT and ISC. `LICENSES/` holds each text, and a new CI job runs `reuse lint`.
- **Upgrade impact:** None. Repository metadata and CI only; the binary and image behave the same.

### 2026-10-28 — a9dfb16

Source: [a9dfb16ba194cbff07a57778956da6385e622cdb](https://github.com/Busnes-app/ky-server-base/commit/a9dfb16ba194cbff07a57778956da6385e622cdb) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/40618370065)

- **Changes:** The web UI takes ky-ui 0.4.3's contrast fixes: form-field borders are darker, and named palettes pick light or dark danger, success and warning colours by their own scheme rather than the OS setting, so status text stays readable in every theme.
- **Upgrade impact:** None. Restart onto the new image; no migration or configuration change.

### 2026-10-27 — b02bb81

Source: [b02bb812c1aa7eb389fc1438f5b4f61ad05eb163](https://github.com/Busnes-app/ky-server-base/commit/b02bb812c1aa7eb389fc1438f5b4f61ad05eb163) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/40528604354)

- **Changes:** Sign out every KySignOn session issued before the back-channel logout release; those sessions carry no `sid`, so a KyIdentity sign-out naming the login could never end them. Local password sessions and SSO sessions issued since that release are kept; pending device pairings of SSO users are dropped the same way. A device paired from a KySignOn session now inherits that login: a back-channel logout of it ends the paired device's session, and a pairing still pending when the login ends no longer issues one.
- **Upgrade impact:** Schema migration 7 runs automatically on start, deletes SSO sessions without a `sid` and SSO users' device pairings, and adds `device_pairings.sso_sid`. Affected KySignOn users sign in once more; devices paired from a KySignOn session pair again after a provider sign-out. No configuration change.

### 2026-10-27 — bed0552

Source: [bed0552c580f2e49d65a3142eea9e4af75b27b65](https://github.com/Busnes-app/ky-server-base/commit/bed0552c580f2e49d65a3142eea9e4af75b27b65) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/40516977499)

- **Changes:** KySignOn sign-in honours OpenID Connect Back-Channel Logout at `POST /api/sso/kysignon/backchannel-logout`: a provider-signed `logout_token` ends every local session of that login (`sid`), or every login of the subject whose ID token predates the logout when it names no `sid`, and fences callbacks still in flight. SSO sessions are dated by the ID token's `iat`; accepted logouts are audited as `auth.sso_backchannel_logout`. Also took the Dependabot npm runtime/build and Go module updates merged into the branch.
- **Upgrade impact:** Schema migration 6 adds `sessions.sso_sid`, `sso_logouts` and `sso_subject_logouts`. Sessions issued before it carry no `sid`, so only a subject-wide logout reaches them. Register `<KY_APP_URL>/api/sso/kysignon/backchannel-logout` as the client's back-channel logout URI in KyIdentity.

### 2026-10-27 — 90b3c3d

Source: [90b3c3dc9e9f6ef3f4b78615e4d57d37d041b629](https://github.com/Busnes-app/ky-server-base/commit/90b3c3dc9e9f6ef3f4b78615e4d57d37d041b629) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/40499112226)

- **Changes:** CI skips the release-notes check on Dependabot pull requests and on the master push that merges one, identified by the pull request's author. Every other change still needs a changed Current source summary.
- **Upgrade impact:** CI-only change; the image and its behavior are unchanged. No restart, schema, configuration or stored-format migration is needed.

### 2026-10-26 — 6ee7c1a

Source: [6ee7c1aaf214616580b8bbf9908be3d504d74c8f](https://github.com/Busnes-app/ky-server-base/commit/6ee7c1aaf214616580b8bbf9908be3d504d74c8f) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/40370959524)

- **Changes:** Show Settings & DB facts as plain label and value rows on the card: no tinted inner box, accent icon or accent driver chip. The overview's database state uses the neutral badge. Page introductions sit 8px under their title.
- **Upgrade impact:** Rebuild or replace the image and restart to load the embedded web assets. No schema, configuration or stored-format migration; saved `ky_theme` choices and all database, session, encryption and recovery material stay as they are.

### 2026-10-26 — 3bc0780

Source: [3bc078096723820e139bb1978d261e2e99415da1](https://github.com/Busnes-app/ky-server-base/commit/3bc078096723820e139bb1978d261e2e99415da1) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/40347422306)

- **Changes:** Keep error/success text readable on the six new light themes and the filled danger label readable on the 15 suite themes (Busnes and the five base palettes unchanged); bring the current page into view on phones after an Overview action; sync ky-ui 0.4.1. Pin the shared-library TOTP repair (secrets under 16 decoded bytes refused) with its capsule size-budget, Shamir sampling and setup-refusal repairs, and update x/crypto, x/sync and x/text.
- **Upgrade impact:** A stored short TOTP secret can no longer authenticate: keep its sealed record and the database, use a recovery method or an authorized reset, then re-enroll; do not pad secrets or regenerate keys. Keep historical capsules and shares and verify a known restore first. Rebuild/replace the image and restart. No schema migration or new configuration.

### 2026-10-26 — 6c296a2

Source: [6c296a2ca7f33238857a499f8aa1e5444aec6089](https://github.com/Busnes-app/ky-server-base/commit/6c296a2ca7f33238857a499f8aa1e5444aec6089) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/40325239762)

- **Changes:** Add an Appearance page for every signed-in role over the shared ky-ui 0.4.0 catalog, replacing the sidebar theme selector while retaining the sign-in selector. Use shared shell geometry/type and remove decorative title icons and the unverified "Verified Ready" backup badge.
- **Upgrade impact:** Rebuild or replace the image for the embedded web assets. No schema, configuration or stored-format migration is required; saved `ky_theme` choices remain valid, including base `patina`, distinct from suite Patina Ky.

### 2026-10-26 — 95a3f11

Source: [95a3f114712d43151a4a1bc14a1299cbdd8be0b8](https://github.com/Busnes-app/ky-server-base/commit/95a3f114712d43151a4a1bc14a1299cbdd8be0b8) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/40313178303)

- **Changes:** Align the OIDC key-strength check with go-oidc's envelope and go-jose's case-sensitive JWK member parsing, checking the verifier's actual RSA modulus. Real signed-token regressions cover both adapters and preserve strong-key authentication, claim checks and upstream caching.
- **Upgrade impact:** Rebuild/replace the image and restart to replace the previous process's verifier/cache. Retire IdP RSA keys below 2048 bits before rollout, including weak keys alongside strong ones. No schema migration or new settings are required; preserve database/session/encryption/recovery material and overlay chains.

### 2026-10-26 — 77d4d63

Source: [77d4d6337b854ba2ec7ba9c2b546c1aa87809242](https://github.com/Busnes-app/ky-server-base/commit/77d4d6337b854ba2ec7ba9c2b546c1aa87809242) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/40309420264)

- **Changes:** Add the historical image-release archive and require a fresh human change summary and upgrade-impact note in CI before image publication. Link the notes from installation/contribution guidance; application code, embedded web assets and stored formats are unchanged.
- **Upgrade impact:** No application migration is introduced by this notes-only release. Continue to verify and pin images; apply the strong-key migration when upgrading from before `bd9b4e3`. Source contributors need Python 3 for the notes gate and must update Current source for every proposed master update.

### 2026-10-26 — bd9b4e3

Source: [bd9b4e348479387dd8f8233420dc003ffa804e03](https://github.com/Busnes-app/ky-server-base/commit/bd9b4e348479387dd8f8233420dc003ffa804e03) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/40273719186)

- **Changes:** Reject configured session/webhook HMAC secrets shorter than 16 bytes and newly fetched RSA JWKS keys below 2048 bits. Add an optional verified strong-TLS/HTTPS process/Compose profile and refuse JWKS redirects.
- **Upgrade impact:** Prepare and coordinate replacement short signing secrets and weak IdP keys before restarting; session-secret rotation invalidates outstanding proof-of-work challenges. Preserve database/encryption/recovery keys and overlay chains. The optional Compose profile requires Compose 2.24.4+, moves the HTTP listener to loopback and needs a configured HTTPS edge; follow docs/STRONG-CRYPTO.md.

### 2026-10-26 — c305002

Source: [c30500247a5946ac08d8417be0c0be730268b76a](https://github.com/Busnes-app/ky-server-base/commit/c30500247a5946ac08d8417be0c0be730268b76a) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/40265818111)

- **Changes:** Document implemented HTTP/SCIM routes, permissions, request/response examples, errors, CLI commands and process configuration in the public interface reference.
- **Upgrade impact:** Use the reference for integrations and distinguish unwired helpers from exposed routes. This documentation release changes no runtime or data format.

### 2026-10-26 — ccd0a28

Source: [ccd0a2884d95a6a0eb173e969921a610d5d7656b](https://github.com/Busnes-app/ky-server-base/commit/ccd0a2884d95a6a0eb173e969921a610d5d7656b) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/40262519182)

- **Changes:** Link the README to the project's live OpenSSF Best Practices and Scorecard results.
- **Upgrade impact:** This is a README-only release. Badge progress is not an independent audit; application behavior and data are unchanged.

### 2026-10-25 — e400622

Source: [e400622fec75985589dba7a229afa9cb8280149c](https://github.com/Busnes-app/ky-server-base/commit/e400622fec75985589dba7a229afa9cb8280149c) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/40199079650)

- **Changes:** Publish security reporting, contribution and code-of-conduct entry points with the existing project license.
- **Upgrade impact:** Use the documented reporting/contribution channels. This interval changes project documentation, with no runtime or operator migration.

### 2026-10-24 — f7dd1b8

Source: [f7dd1b8652ef92b6a0f72d47fbf3839deb03b08c](https://github.com/Busnes-app/ky-server-base/commit/f7dd1b8652ef92b6a0f72d47fbf3839deb03b08c) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/40086596830)

- **Changes:** Align required Go CI jobs with the version selected by go.mod rather than a separate hard-coded workflow version.
- **Upgrade impact:** Source contributors should use go.mod/toolchain as the version source. This is a CI alignment change without an application schema/configuration migration.

### 2026-10-24 — 10d50ae

Source: [10d50ae9bd798b8cd4834be4fb28389679aabe0b](https://github.com/Busnes-app/ky-server-base/commit/10d50ae9bd798b8cd4834be4fb28389679aabe0b) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/40057179074)

- **Changes:** Publish the coding/test policies and security assurance case, including trust boundaries and existing analysis evidence.
- **Upgrade impact:** Contributors should use the documented checks and update the assurance case when changing a security boundary. This documentation interval introduces no runtime/data migration.

### 2026-10-24 — 274dbb8

Source: [274dbb8c27f4728906f8aac7c030a0d6d46e6163](https://github.com/Busnes-app/ky-server-base/commit/274dbb8c27f4728906f8aac7c030a0d6d46e6163) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/40052035852)

- **Changes:** Add bounded fuzz checks for trusted-proxy parsing, forwarded-client IPs, proof-of-work verification and AES-GCM input handling to CI.
- **Upgrade impact:** This interval adds tests and CI work, with no runtime change or operator migration.

### 2026-10-23 — b0ca0d8

Source: [b0ca0d8227207405157c25ec51abe48cc589f2c5](https://github.com/Busnes-app/ky-server-base/commit/b0ca0d8227207405157c25ec51abe48cc589f2c5) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/40029542106)

- **Changes:** The optional upstream-library compatibility workflow now tidies module requirements after replacing ky-primitives, so version skew does not prevent the intended build/test check.
- **Upgrade impact:** This early-warning workflow remains separate from merge gates and deployed dependencies. No runtime, image configuration or database migration is introduced.

### 2026-10-23 — aaf90af

Source: [aaf90af223788ce62f68d93bcdaf0aa7c4e2af67](https://github.com/Busnes-app/ky-server-base/commit/aaf90af223788ce62f68d93bcdaf0aa7c4e2af67) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/39976507297)

- **Changes:** Build with Go 1.27.3 for GO-2026-6809, pin all Docker stages by digest and use npm ci for repeatable frontend dependency selection. Source builds select the newer Go toolchain too.
- **Upgrade impact:** Replace/rebuild the image with the patched toolchain. Keep module/toolchain settings and locked frontend dependencies; this dependency/build fix requires no application-data migration. GO-2026-6809 identifies an upstream Go advisory, not a newly assigned Ky product advisory.

### 2026-10-20 — 3507db3

Source: [3507db347a5e401310432e6e42680d1d94e2b109](https://github.com/Busnes-app/ky-server-base/commit/3507db347a5e401310432e6e42680d1d94e2b109) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/39357621736)

- **Changes:** Update React/React DOM to 19.3.1 and lucide-react to the 1.58 series, and regenerate committed web/dist.
- **Upgrade impact:** Reload the browser/PWA after replacing the image. No new application schema, configuration or encryption-key migration is introduced.

### 2026-10-20 — 47b3eab

Source: [47b3eab31f2aef17479a783356c61153dc1ff734](https://github.com/Busnes-app/ky-server-base/commit/47b3eab31f2aef17479a783356c61153dc1ff734) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/39356116609)

- **Changes:** Refresh Scorecard's SARIF upload Action. This image also includes the preceding, superseded frontend-tool update to Playwright 1.64.1, jsdom 30.1.3 and Vitest 5.0.5.
- **Upgrade impact:** Source tests use the new lockfile/browser version. The intervening tool-update commit had no successful publish job and is included here; this interval adds no operator configuration or database migration.

### 2026-10-20 — ba25505

Source: [ba25505ab38a98df59d6c004a1a1523c05ee4a6a](https://github.com/Busnes-app/ky-server-base/commit/ba25505ab38a98df59d6c004a1a1523c05ee4a6a) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/39356084991)

- **Changes:** Adopt ky-primitives 0.10.0 with x/crypto 0.57.0 and x/text 0.42.0.
- **Upgrade impact:** Rebuild or replace the image for the shared-library updates and verify authentication/recovery. These tagged libraries predate the later strong-key guards; this update alone does not establish the current strong-key profile.

### 2026-10-14 — 10814fe

Source: [10814fe383a9df3fb1e6ad585b21837fcfd45d2e](https://github.com/Busnes-app/ky-server-base/commit/10814fe383a9df3fb1e6ad585b21837fcfd45d2e) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/38632022165)

- **Changes:** Update the lucide-react dependency to the 1.55 series.
- **Upgrade impact:** This source-dependency update does not regenerate committed web/dist in this interval; the Docker build uses the updated dependency. Rebuild/reload to receive updated icons; no data migration is required.

### 2026-10-14 — 0e6d337

Source: [0e6d337b013b923c1ce8f98d20eb3d529105d210](https://github.com/Busnes-app/ky-server-base/commit/0e6d337b013b923c1ce8f98d20eb3d529105d210) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/38631323065)

- **Changes:** Refresh frontend verification tools, including Playwright 1.64.0, Vite 8.3.4, React/DOM types, the React plugin and jsdom.
- **Upgrade impact:** Source verification uses the updated npm lockfile and Playwright browser version. This interval does not regenerate web/dist or change application settings/schema.

### 2026-10-14 — 74b5e91

Source: [74b5e91e24be11e1aa10fe51caffba9d3170964a](https://github.com/Busnes-app/ky-server-base/commit/74b5e91e24be11e1aa10fe51caffba9d3170964a) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/38630845246)

- **Changes:** Refresh pinned checkout, setup and artifact Actions used by CI and Scorecard.
- **Upgrade impact:** This is a build/analysis workflow update. It introduces no application behavior or operator migration; preserve normal image provenance verification.

### 2026-10-14 — c6f2444

Source: [c6f2444da5e22ff543ad404f1f52d91e34af6e8f](https://github.com/Busnes-app/ky-server-base/commit/c6f2444da5e22ff543ad404f1f52d91e34af6e8f) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/38630182878)

- **Changes:** Update x/sys from 0.48.0 to 0.49.0 for operating-system support.
- **Upgrade impact:** Rebuild or replace the image to use the dependency update. No new application configuration or database migration is introduced.

### 2026-10-11 — a0a1d2e

Source: [a0a1d2eda5c65e94e5848edab6f0eaa82966e4e0](https://github.com/Busnes-app/ky-server-base/commit/a0a1d2eda5c65e94e5848edab6f0eaa82966e4e0) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/38129024413)

- **Changes:** Update Go setup in required and compatibility workflows so newer toolchain/dependency checks continue working under the standard suite process.
- **Upgrade impact:** Source contributors should follow the selected Go version rather than relying on an old CI image. This interval changes verification, with no runtime or data-format migration.

### 2026-10-11 — 26324ed

Source: [26324ed7786aaa8525d2446c0553e795528d40e5](https://github.com/Busnes-app/ky-server-base/commit/26324ed7786aaa8525d2446c0553e795528d40e5) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/38118138550)

- **Changes:** Run frontend CI on Node 26 so verification uses the project's newer JavaScript toolchain.
- **Upgrade impact:** Use Node 26 when reproducing the documented CI environment. Published application behavior and data formats are unchanged by this workflow-only interval.

### 2026-10-11 — aecb228

Source: [aecb22813282103bdca1f1762a96bcdc9e30d491](https://github.com/Busnes-app/ky-server-base/commit/aecb22813282103bdca1f1762a96bcdc9e30d491) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/38103430073)

- **Changes:** Add automated OpenSSF Scorecard analysis and result publication to the repository.
- **Upgrade impact:** This release changes repository security analysis only; no application behavior, database or deployment-setting migration is introduced.

### 2026-10-11 — e782266

Source: [e782266289c9146d321217933d3e0979baef8ab7](https://github.com/Busnes-app/ky-server-base/commit/e782266289c9146d321217933d3e0979baef8ab7) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/38102958128)

- **Changes:** Build production containers with Go 1.27.2 and raise the supported source Go floor to 1.26.9 to receive toolchain security fixes.
- **Upgrade impact:** Upgrade the source toolchain or replace the published image; a source rebuild using an older vulnerable toolchain does not receive the fix. Application data/settings need no migration for this interval.

### 2026-10-06 — f46e082

Source: [f46e0829316a6c1be9ff754a42544bb5a80e669a](https://github.com/Busnes-app/ky-server-base/commit/f46e0829316a6c1be9ff754a42544bb5a80e669a) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/37500311666)

- **Changes:** Update React/React DOM to the 19.3 series and lucide-react icons to the 1.51 series, with regenerated locked dependencies and embedded assets.
- **Upgrade impact:** Reload the web/PWA client after replacing the image. This interval changes frontend dependencies/assets without a new backend schema or configuration requirement.

### 2026-10-06 — 5134c66

Source: [5134c66ef2edcb519e668e701905b1c57d887a94](https://github.com/Busnes-app/ky-server-base/commit/5134c66ef2edcb519e668e701905b1c57d887a94) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/37498995947)

- **Changes:** Refresh the frontend build/test toolchain, including TypeScript 7, Vite 8, the React plugin, jsdom and Node type definitions, and regenerate the committed embedded assets.
- **Upgrade impact:** Source builders need a Node version supported by the updated tools and must run npm ci/build. Published-image users receive rebuilt assets; no operator configuration or database migration is introduced.

### 2026-10-06 — 2226658

Source: [2226658f4b1bf469a5d2185f7f439508b3ad3b77](https://github.com/Busnes-app/ky-server-base/commit/2226658f4b1bf469a5d2185f7f439508b3ad3b77) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/37495881428)

- **Changes:** Update modernc.org/sqlite to 1.60.1, libc support to 1.77.1 and x/sync to 0.23.0.
- **Upgrade impact:** Replace/rebuild the image for the driver updates. This is a dependency update without a new application migration; preserve a known-good database backup and verify database access.

### 2026-10-06 — 9c9ed76

Source: [9c9ed76fc1ef28798679bc458724b5fce3b7642c](https://github.com/Busnes-app/ky-server-base/commit/9c9ed76fc1ef28798679bc458724b5fce3b7642c) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/37495851635)

- **Changes:** Adopt ky-primitives 0.9.0 for shared authentication/recovery primitives.
- **Upgrade impact:** Rebuild or replace the image to use the new library. No application schema, settings or capsule-format change is made in this interval; verify authentication and a recovery drill after updating.

### 2026-10-06 — 7932633

Source: [79326333c19a2a3e38a3400ede2343995385838a](https://github.com/Busnes-app/ky-server-base/commit/79326333c19a2a3e38a3400ede2343995385838a) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/37495088381)

- **Changes:** Update the locked source-map-js dependency after its upstream advisory and broaden automated npm dependency monitoring.
- **Upgrade impact:** Source builders must use the updated lockfile. This interval changes build/dependency metadata rather than application configuration or schema; use a freshly built image to receive build-chain fixes.

### 2026-09-29 — dfbba88

Source: [dfbba88fb182327020930745fbcac10ef51fe184](https://github.com/Busnes-app/ky-server-base/commit/dfbba88fb182327020930745fbcac10ef51fe184) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/36586566970)

- **Changes:** Device pairing now redeems only the QR secret, without accepting caller-selected user identity. Paired sessions inherit the verified parent's expiry; obsolete pairing records are migrated away.
- **Upgrade impact:** Use the new QR flow and recreate outstanding pairings after upgrading. A paired device cannot extend its parent's session lifetime. Keep the database for its automatic migration; no encryption/recovery-key rotation is required.

### 2026-09-25 — 9bea11f

Source: [9bea11f3e7a88b7a17d783c6a01fd56ec2c0e9a6](https://github.com/Busnes-app/ky-server-base/commit/9bea11f3e7a88b7a17d783c6a01fd56ec2c0e9a6) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/36194198032)

- **Changes:** Fix production worker loading, move bootstrap code under the self-only script policy, and add Chromium checks for themes, narrow layouts, dialogs and pairing. The service worker is updated alongside the embedded UI.
- **Upgrade impact:** Reload the PWA so the updated worker/assets take effect. Source builds now need Playwright's Chromium for browser verification. Backend data and recovery keys remain compatible.

### 2026-09-25 — fc7dfd1

Source: [fc7dfd1986a702f607a71457a4818097674a88eb](https://github.com/Busnes-app/ky-server-base/commit/fc7dfd1986a702f607a71457a4818097674a88eb) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/36187416926)

- **Changes:** Vendor the verified shared ky-ui theme/navigation foundation and add hash checks before frontend tests/builds so copied assets cannot drift silently.
- **Upgrade impact:** Rebuild/reload the embedded UI. Keep product-specific layout and saved theme choices; source contributors must update the vendored manifest through the documented sync process. No backend data-format migration is introduced.

### 2026-09-25 — b843bf9

Source: [b843bf9e78a666b7c087b7281ba0b81530da6f00](https://github.com/Busnes-app/ky-server-base/commit/b843bf9e78a666b7c087b7281ba0b81530da6f00) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/36167878852)

- **Changes:** Standardize the server navigation and add an optional static-IP Compose overlay, including container-network setup instructions.
- **Upgrade impact:** Existing deployments retain automatic addressing unless the overlay is selected. To use a static IP, choose a free address/subnet and recreate the network without deleting data volumes, as the README describes.

### 2026-09-24 — 6a4deb8

Source: [6a4deb8b64773882fdd268a978272ab279a53019](https://github.com/Busnes-app/ky-server-base/commit/6a4deb8b64773882fdd268a978272ab279a53019) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/36027168693)

- **Changes:** Add Busnes light/dark themes, product icons and updated PWA assets, with browser theme selection replacing the older presentation.
- **Upgrade impact:** Reload the browser/PWA to receive the new assets and check saved theme choices. This release does not require a database or recovery-key migration.

### 2026-09-22 — 63ecef8

Source: [63ecef80f8ee85212f1c8f5a62dc400a2e1edd7a](https://github.com/Busnes-app/ky-server-base/commit/63ecef80f8ee85212f1c8f5a62dc400a2e1edd7a) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/35781840644)

- **Changes:** Update the embedded SQLite driver to modernc.org/sqlite 1.59.0 and its libc support to 1.75.7.
- **Upgrade impact:** Rebuild or replace the image for the database-library updates. This interval adds no application schema/configuration change; retain a verified backup and exercise normal SQLite operations after upgrading.

### 2026-09-17 — f8234e3

Source: [f8234e3c428db9b56fe7d95d5e23528a2aa8a022](https://github.com/Busnes-app/ky-server-base/commit/f8234e3c428db9b56fe7d95d5e23528a2aa8a022) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/35254418619)

- **Changes:** Strengthen the owner-move documentation and CI checks so image names, repository coordinates and complete attestation identities agree. Restore and pin recipes no longer rely on the retired owner.
- **Upgrade impact:** Follow the revised owner-move and restore instructions even if an older pin still appears usable. Verify and repin through the current repository before pulling; this release does not rewrite a deployment's .env or credentials.

### 2026-09-16 — 2bf3eb8

Source: [2bf3eb89b9f94c0b66a8b7669558e228c8b2e76e](https://github.com/Busnes-app/ky-server-base/commit/2bf3eb89b9f94c0b66a8b7669558e228c8b2e76e) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/35159746108)

- **Changes:** Move the Go module, imports, CI coordinates and published-image owner to Busnes-app, and adopt ky-primitives 0.8.0.
- **Upgrade impact:** Verify the trusted repository remote and replace image/dependency coordinates before fetching. Existing image overrides and digest pins need deliberate repinning under the current owner; follow the README owner-move instructions and retain data/keys.

### 2026-09-15 — 296d6bd

Source: [296d6bddd3ac52a39f79fe4f5b7834d3b7148659](https://github.com/Busnes-app/ky-server-base/commit/296d6bddd3ac52a39f79fe4f5b7834d3b7148659) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/35037262571)

- **Changes:** Bootstrap and init-admin passwords now require replacement before privileged use. Operator password resets atomically revoke sessions, MFA challenges and pairings; paired sessions are tied to verified credentials. The password-change screen remains usable at narrow widths.
- **Upgrade impact:** Bootstrap/reset users must choose a different password of at least 12 characters and sign in again. Existing untouched accounts are not retroactively flagged. Expect reset users' old sessions and device pairings to stop working; preserve the database while applying its automatic migration.

### 2026-09-15 — f4ca19a

Source: [f4ca19a56911e2edf303fb8823b11f6ca321f54d](https://github.com/Busnes-app/ky-server-base/commit/f4ca19a56911e2edf303fb8823b11f6ca321f54d) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/34918867843)

- **Changes:** Update the PostgreSQL driver to pgx 5.11.0, OAuth support to x/oauth2 0.37.0 and operating-system support to x/sys 0.48.0.
- **Upgrade impact:** Rebuild or replace the image to receive the dependency updates. No application configuration or schema migration is introduced in this interval; verify normal authentication and database access.

### 2026-09-13 — 2a31d5c

Source: [2a31d5c68495f93312a458c73dd565152e12f1cb](https://github.com/Busnes-app/ky-server-base/commit/2a31d5c68495f93312a458c73dd565152e12f1cb) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/34771668051)

- **Changes:** The LAN-DNS remediation example now parses correctly and explains that an exported KY_DNS overrides the value stored in .env.
- **Upgrade impact:** Choose the resolver explicitly. Unset a shell override when you intend to retain the saved resolver; preserve the other Compose overlays. This is a deployment-documentation change.

### 2026-09-13 — 038ef5c

Source: [038ef5cc0eb924ef5d5d350d703e6a505b46fe55](https://github.com/Busnes-app/ky-server-base/commit/038ef5cc0eb924ef5d5d350d703e6a505b46fe55) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/34769760971)

- **Changes:** LAN-DNS instructions and Compose overrides now retain an existing resolver, append rather than replace overlay chains, and provide a safe revert. Invalid duplicate overlay text is removed.
- **Upgrade impact:** Reapply the corrected resolver recipe only with a deliberately selected DNS server. Installations predating published images must retain the source-build overlay; check the resolved image and network configuration before recreation.

### 2026-09-13 — b34e26c

Source: [b34e26cffcd7251febb65500108ba8d28183c621](https://github.com/Busnes-app/ky-server-base/commit/b34e26cffcd7251febb65500108ba8d28183c621) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/34767069556)

- **Changes:** Image pinning and restore instructions now bind verification to the requested commit, use the full workflow identity and replace protected environment files atomically. LAN-DNS setup preserves existing overlays and the image/build choice.
- **Upgrade impact:** Use the revised pin/restore recipes before repinning. Retain existing .env values and COMPOSE_FILE overlays; a source build keeps its local image name. Application data and capsule formats are unchanged by this release.

### 2026-09-13 — 78222a4

Source: [78222a493edb8e8e693921c39a451627343fa24d](https://github.com/Busnes-app/ky-server-base/commit/78222a493edb8e8e693921c39a451627343fa24d) · [Published image CI](https://github.com/Busnes-app/ky-server-base/actions/runs/34738302951)

- **Changes:** First published-container distribution of the scaffold: local/federated authentication, SCIM, SQLite/PostgreSQL storage and sealed SQLite recovery are packaged with the embedded web UI. CI now publishes the image it tested, verifies provenance and promotes by digest only at the master tip.
- **Upgrade impact:** Choose either the published image or the source-build overlay; preserve existing volumes and encryption/recovery keys. Older source installations must explicitly opt into the build overlay. PostgreSQL still requires its own protected database backup. See the README and restore runbook.
