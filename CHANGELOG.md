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

- Sign out every KySignOn session issued before the back-channel logout release. Those
  sessions carry no `sid`, so a KyIdentity sign-out naming the login could never end
  them; they stayed valid until they expired. Local password sessions and SSO
  sessions issued since that release are kept. Pending device pairings of SSO users
  are dropped the same way.
- A device paired from a KySignOn session now inherits that login: a back-channel
  logout of it ends the paired device's session, and a pairing still pending when the
  login ends no longer issues one. Before, a paired device stayed signed in.

### Upgrade impact

- Schema migration 7 runs automatically on start and deletes SSO sessions without a
  `sid` and SSO users' device pairings, and adds `device_pairings.sso_sid`. Affected
  KySignOn users sign in once more; devices paired from a KySignOn session pair again
  after a provider sign-out. No configuration change.

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
