# SQLite, PostgreSQL and replicas

[Overview](../README.md) · [Interfaces](INTERFACES.md) · [Disaster recovery](DISASTER-RECOVERY.md)

SQLite remains the default for a single server. It keeps the existing in-memory login
limits and CAPTCHA replay set, avoiding extra database writes on that deployment path.
The server holds a local instance lock; another server or migration using the same
SQLite file is refused. Keep the persistent `.lock` files in place. They are released
on process exit and must not be deleted while a process is running. SQLite on a network
filesystem is not a multi-host replication mode.

PostgreSQL supports multiple application servers behind a health-checking reverse proxy.
Docker Compose, separate Docker hosts, systemd and Kubernetes can all run them. No
Kubernetes API, Redis or extra coordination service is required. Existing SQLite capacity
results do not establish PostgreSQL capacity.

**Application replicas are not PostgreSQL high availability.** Every replica uses the same
single PostgreSQL database. Its streaming replication, failover and storage redundancy remain
yours to run; when it is down, every replica is unready. Replicas never talk to each other:
their trust root is the shared database credentials and secrets, which the configuration
check below verifies.

## Health and drain

- `GET|HEAD /livez` answers 200 `{"status":"ok"}` without touching the database: the process
  is alive. Use it for restarts only.
- `GET|HEAD /readyz` answers 200 `{"status":"ready"}` while the server is not draining and a
  database ping succeeds within one second; otherwise 503 `{"status":"unready"}`. Results are
  cached for one second behind a lock, so any number of probes cost at most one ping a second.
- Both send `Cache-Control: no-store` and only the status word. The proxy may keep them
  internal.
- On SIGTERM a server turns `/readyz` to 503, cancels backup work, keeps serving every other
  route for 5 seconds (five one-second probes), then stops accepting connections, drains
  in-flight requests for up to 5 seconds and waits up to 10 seconds for interrupted backup
  work to record its outcome: 20 seconds, inside Compose's `stop_grace_period` (30s) and
  Kubernetes' default 30s grace.

The base Compose service has a `/readyz` healthcheck (busybox `wget`, already in the image).

## Add replicas

1. Use the same PostgreSQL database **and schema** on all replicas. Connect directly to
   PostgreSQL or through a **session-pooling** proxy. Transaction-pooling proxies cannot
   retain the session advisory locks used for worker ownership.
2. Set the same shared configuration on every replica: `KY_APP_NAME`, `KY_APP_URL`, `KY_ENV`,
   `KY_SESSION_SECRET`, the encryption key, SCIM settings and `KY_SCIM_TOKEN`, every SSO
   setting, CAPTCHA settings, cookie settings, `KY_RATE_LIMIT_EXEMPT_NETWORKS`,
   `KY_BACKUP_DEPOSIT_INTERVAL` and `KY_BACKUP_ALLOW_PRIVATE_RECOVERY`. PostgreSQL requires an
   explicit session secret, and an explicit SCIM token when SCIM is enabled. Supply the same
   retained `KY_ENCRYPTION_KEY` (64 hex characters) to every replica; a new local key cannot
   decrypt existing sealed settings. A replica whose shared configuration differs refuses to
   start ([below](#shared-configuration-check)).
3. Give each server its own writable data directory; never share it. The backup directory
   may be shared (one archive for every replica): every local write and prune runs under the
   database-wide `backup-run` lock. Per-host settings stay per host: `KY_TRUSTED_PROXIES`,
   `KY_BACKUP_DIR`, `KY_BACKUP_KEEP`, host, port and pool sizes. A pairing or a hand pin
   lands on whichever replica gets the request; the others write their own
   `data/recovery.pub` from the pin in the database before their next backup and at startup
   ([recovery key](#the-recovery-key-on-replicas)). A replica whose `recovery.pub` names
   another key refuses to start; remove the stale file and it is rewritten. With the replica
   overlay, recreate only its disposable app volumes:
   `docker compose rm -sfv app && docker compose up -d --wait`. This command is for this
   overlay only: its database and backup archive are separate and retained.
4. Point a **health-checking** reverse proxy at the application servers, probing `/readyz`.
   Sticky sessions are unnecessary: sessions, revocations, login windows, office budgets and
   spent CAPTCHA challenges are shared in PostgreSQL. Configure `KY_TRUSTED_PROXIES` for the
   actual proxy peers.
5. Start with the default pool of 25 connections per server and budget the database's
   total connections across replicas. On PostgreSQL `KY_DB_MAX_OPEN_CONNS` must be at least
   4: a scheduled backup holds the worker lock, the backup-run lock and the snapshot
   transaction at once, and readiness probes and requests need one more
   (`internal/store/snapshot_test.go` proves 4 leaves one free and 3 does not). PostgreSQL
   migrations and initial admin creation are serialized during concurrent startup.

Stop every old-version replica before starting new ones; mixed versions are not supported.
`docker compose up -d` alone recreates replicas one at a time, so old and new overlap. With
the Compose overlay:

```bash
docker compose pull && docker compose stop app && docker compose up -d --wait
```

A source install runs `docker compose build` instead of `pull`. Separate hosts: stop the
service on every host, then start the new version on each.

## Compose replicas on one host

This overlay requires Docker Compose 2.24.4 or later (`!override`).

`docker-compose.replicas.yml` runs `KY_REPLICAS` (default 2) app containers on one PostgreSQL
behind HAProxy. Append it **last** to the `COMPOSE_FILE` chain in `.env` (after
`docker-compose.build.yml` for a source install). Never combine it with
`docker-compose.static-ip.yml`, which gives one container one address.

| Variable | Required | Meaning |
|---|---|---|
| `KY_DB_DSN` | yes | `postgres://ky_server:<KY_POSTGRES_PASSWORD>@postgres:5432/ky_server?sslmode=disable` (the private database network); keep it in `.env`, never argv |
| `KY_POSTGRES_PASSWORD` | yes | The bundled PostgreSQL's password; there is no default |
| `KY_SESSION_SECRET`, `KY_SCIM_TOKEN` | yes | Shared secrets, `openssl rand -hex 32` written straight into `.env` |
| `KY_ENCRYPTION_KEY` | yes | The retained 32-byte key in hex; never regenerate it on an existing deployment ([import it](#from-a-sqlite-base-install)) |
| `KY_REPLICA_SUBNET` | yes | The proxy network, e.g. `172.31.250.0/24` |
| `KY_PROXY_IP` | yes | HAProxy's fixed address in that subnet; the replicas trust only `KY_PROXY_IP/32` |
| `KY_EDGE_TRUSTED_PROXIES` | no | The TLS proxy in front of HAProxy as HAProxy sees it (see below); refused together with `KY_PROXY_BIND` |
| `KY_PROXY_BIND`, `KY_PROXY_PORT` | no | Published address, default `127.0.0.1:8080` |
| `KY_REPLICAS` | no | Replica count, default 2. HAProxy gets one server slot per replica, so change the count here, never with `--scale`: a replica beyond the slots gets no traffic |

```bash
docker compose config --quiet    # names any missing required variable
docker compose up -d --wait
```

What it sets up:

- **App replicas** clear the base `container_name` and port and run `KY_DB_DRIVER=postgres`.
  Each container has its own anonymous `/app/data` volume (locks, scratch, `recovery.pub`,
  which is rewritten from the database), so `down` and `up` or scaling lose nothing there.
  All replicas share `./backups` as `/app/backups`: one archive of local capsules, written
  and pruned to `KY_BACKUP_KEEP` under the database-wide `backup-run` lock, listed the same
  by every replica, and kept across `down`/`up`, scaling and `docker volume prune`.
- **PostgreSQL** (digest-pinned) keeps its data in its own `pgdata-replicas` volume, never
  the base file's `pgdata`, which `--profile postgres` initializes with a public password. It
  runs on an `internal` network with no route out and no host port; replicas wait for its
  `pg_isready` healthcheck. `docker compose down -v` deletes that volume: the database.
- **HAProxy** (digest-pinned, `deploy/replicas/haproxy.cfg`) finds replicas through Docker's
  DNS (`server-template` with a resolver) and probes each one's `/readyz` every second; two
  failures take a replica out, two passes bring it back. A request whose connection failed
  is sent to another replica; a GET or HEAD that got an empty answer (its replica died) is
  retried there too; a timed-out request is never retried, and any other method is never
  sent twice. Replies get 8 seconds (`timeout server`); only `/api/backup/deposit`,
  `/api/backup/drill` and `/api/backup/export-capsule` get the 16-minute budget. Status,
  schedule, pinning, pairing and unpairing keep the short upstream timeout. When the database freezes, a request already inside a replica gets a 504
  after 8 seconds and new requests a 503 once the checks take the replicas out (the e2e
  test fires authenticated requests from the moment of a pause and requires every answer
  within 10 seconds).

HAProxy publishes on loopback. Put your TLS proxy in front of it. HAProxy appends the peer
it saw to `X-Forwarded-For`; with the default loopback publish that peer is the replicas
network gateway (the host side of the bridge, e.g. `172.31.250.1`), so every client counts as
one address for sign-in limits until you set `KY_EDGE_TRUSTED_PROXIES=<that gateway>/32`.
Only processes on the host can reach a loopback publish, which is what makes trusting it
safe. **Set `KY_EDGE_TRUSTED_PROXIES` only with the default loopback publish**: with any
other `KY_PROXY_BIND`, clients from other hosts can arrive as the gateway and their forged
`X-Forwarded-For` would be trusted, so the overlay refuses the two together. Never list a
range.

`docker compose stop` is graceful; for one replica, `docker stop <container>` sends the same
SIGTERM. A killed or stopped replica is restarted by `restart: unless-stopped` or the next
`docker compose up -d`.

### From a SQLite base install

Run from the repository directory, with the SQLite server stopped (`docker compose stop app`)
and backed up. Nothing below prints a secret.

1. Keep `.env` private (mode 0600), with exactly one line per setting. Edit an existing
   line rather than appending a duplicate: Compose silently uses the last value. Retain an
   existing `KY_ENCRYPTION_KEY` exactly. Only if it is missing, import the retained file
   without echoing it; a failed read must not install an empty key:

   ```bash
   (umask 077; replica_retained_key=$(sudo cat data/encryption.key) &&
     printf 'KY_ENCRYPTION_KEY=%s\n' "$replica_retained_key" >> .env)
   ```

2. Retain the effective **`KY_SCIM_TOKEN`**, including a token held in another deployment
   secret. Keep `KY_SESSION_SECRET` until the deliberate cutover rotation in step 5. Generate only missing secrets (`openssl rand -hex 32`)
   and add each once; never replace a working SCIM token on the product alone. If the
   connector's current token cannot be recovered, coordinate its rotation in KyIdentity
   and the product before restarting, then verify provisioning and deprovisioning.
   Add a missing `KY_POSTGRES_PASSWORD`, `KY_REPLICA_SUBNET`, `KY_PROXY_IP` and the
   `KY_DB_DSN` for the bundled database
   (`postgres://ky_server:<KY_POSTGRES_PASSWORD>@postgres:5432/ky_server?sslmode=disable`).
   Keep an existing working `KY_APP_NAME`: it is also the KyRecovery service label.
   New installs default to `Busnes.app`; labels must match
   `[A-Za-z0-9][A-Za-z0-9_.-]{0,63}` and contain no `..`.
   Append `docker-compose.replicas.yml` last to `COMPOSE_FILE`;
   `docker compose config --quiet` names anything missing.
3. Start only the database and make the empty migration target. The bundled database has
   no host port, so the migration runs in a one-off app container on its network:

   ```bash
   docker compose up -d --wait postgres
   docker compose exec postgres createdb -U ky_server ky_server_migrated
   (umask 077; printf 'KY_MIGRATE_POSTGRES_DSN=postgres://ky_server:%s@postgres:5432/ky_server_migrated?sslmode=disable\n' \
     "$(sed -n 's/^KY_POSTGRES_PASSWORD=//p' .env)" > migration.env)
   ```

4. Check, then copy. The overlay gives the app container no `./data`, so mount it; it must
   be writable (the command takes a lock and makes a private snapshot beside the database):

   ```bash
   docker compose run --rm --no-deps --env-from-file migration.env \
     -v "$PWD/data:/src" app migrate-postgres --source /src/ky_server.db --check
   docker compose run --rm --no-deps --env-from-file migration.env \
     -v "$PWD/data:/src" app migrate-postgres --source /src/ky_server.db
   ```

   A source that pins a recovery key records it for the replicas from `data/recovery.pub`;
   a pin without that file is refused before anything is copied.
5. Point `KY_DB_DSN` in `.env` at `ky_server_migrated`. Rotate only `KY_SESSION_SECRET`
   once at cutover on every replica to invalidate old SQLite CAPTCHA solutions; keep the
   SCIM token and encryption key unchanged. Remove `migration.env`, then start:
   `docker compose up -d --wait`. Verify as in
   [step 5 of the migration](#move-a-deployed-sqlite-server-to-postgresql), then keep
   `./data` until a PostgreSQL capsule has been restored in a drill.


### Service identity and older capsules

The Compose app name is now configurable and defaults to the binary's wire-valid
`Busnes.app`, replacing the old hardcoded `Busnes.app Base`. Preserve a working named
pairing's exact label. For a deliberate rename, pause scheduled backups and follow the
shared-setting rotation procedure below. Start one replica, unpair and pair with a new code
for that label while retaining the same pinned public key. Revoke the previous remote token
on KyRecovery, verify a deposit, then restore the schedule and remaining replicas.
Old local capsules retain their original manifest service: restore one made under the old
Compose default with `-service 'Busnes.app Base'`; never rewrite a capsule or its key.
A timeout during pinning or pairing does not prove failure: inspect Backup status and the
audit log before retrying or requesting another single-use code.

## Separate hosts

Each host runs the base file plus an overlay that switches it to PostgreSQL (the snippet in
step 4 of the migration below, with `KY_ENCRYPTION_KEY` supplied too), behind your own
health-checking proxy. Give the proxy `/readyz` as the probe, a one-second interval, and
retries for idempotent requests only, never for a request that timed out. When the database
freezes, a request already inside a server waits until your proxy's upstream timeout (set it
to about 8 seconds, with up to 16 minutes only for deposit, drill and export-capsule) or the
server's own deadline: ordinary requests, including backup status and management, end after
14 seconds, leaving one second to write an error before the 15-second listener deadline.

nginx and Nginx Proxy Manager have no active health checks in their open-source builds:
`max_fails`/`fail_timeout` mark an upstream down only after real requests fail, and
`proxy_next_upstream error timeout` (non-idempotent requests are not retried unless you add
`non_idempotent`; do not) resends a GET whose connection failed. That covers a dead host, but
nginx keeps sending requests to a draining server until it closes its listener, so stopping
a replica can surface errors to clients. Prefer HAProxy, Traefik or another proxy with active
checks.

Kubernetes is an optional deployment choice, not a replication boundary. Probes only:

```yaml
readinessProbe:
  httpGet: { path: /readyz, port: 8080 }
  periodSeconds: 1
  timeoutSeconds: 2
  failureThreshold: 2
livenessProbe:
  httpGet: { path: /livez, port: 8080 }
  periodSeconds: 10
  failureThreshold: 3
terminationGracePeriodSeconds: 30
```

## Shared configuration check

On PostgreSQL, the first replica to start records a fingerprint of the shared settings in
`server_settings` (`replica_config_fingerprint`): HMAC-SHA256 per field, keyed by the
encryption key, so a database reader learns nothing about URLs or networks. Every later
start compares; a replica that differs refuses to start and names the fields, never their
values. A different encryption key is reported alone. SQLite records nothing. The record is
never shown by the settings API or logged; `restore` removes it from a restored database.

**Rotation.** To change a shared setting deliberately (for example `KY_SESSION_SECRET`):

1. Stop every replica (`docker compose stop app`); leave PostgreSQL running.
2. Change the value everywhere it is configured.
3. Run once, with the new configuration:
   `docker compose run --rm --no-deps app accept-replica-config`
   (bare binary: `ky_server_base accept-replica-config`). It rewrites the record and audits
   `admin.replica_config_accepted` with the changed field names.
4. Start the replicas. One still holding the old value refuses to start.

Never rotate the encryption key this way: the sealed KyRecovery token is encrypted under it.
A replica with a different key is told to supply the retained one, and
`accept-replica-config` refuses to record it.

## How shared security behaves

PostgreSQL admission uses one atomic upsert per rate-limit key, with database time
setting the window. Reservations are made before password verification. Successful
logins refund only the window that admitted them, so a slow request cannot weaken a
newer window. Replica restarts preserve these limits. Counter-store failures deny
admission; they never fall back to a fresh in-memory allowance.

A signed CAPTCHA is checked before inserting its salt into a table with a unique key;
only one concurrent request on any replica can spend it. Expired security records and
sessions are removed by the elected worker. The shared tables have indexed expiry
columns. Unlike SQLite's bounded in-memory map, PostgreSQL retains active windows
until expiry; monitor database growth under abusive traffic and apply edge admission
limits appropriate to the deployment.

Every PostgreSQL request that could use an office exemption reads its current stored
list, so removing an exemption on one server takes effect on the others. The displayed
exempted-request count remains a per-process diagnostic and resets when its list changes.

## Scheduled worker and failover

Every server starts the built-in worker. One wins `scheduled-worker` ownership and runs
maintenance and the configured backup schedule. Standbys retry once per minute. The
owner keeps a dedicated PostgreSQL session advisory lock; its death releases the lock.
A probe every second, with a one-second timeout, cancels work if that connection is lost.
The next standby can win without an operator or an orchestrator election.

SQLite elects through an OS advisory file lock on the same host. Manual backup requests
and CLI deposits also compete for `backup-run`, so they cannot overlap the scheduler's
run. The scheduler reads whether a backup is due **after** obtaining job ownership.
Shutdown cancels work and retains the existing interrupted-attempt retry behavior.

Jobs must observe cancellation and tolerate retries. This is single-owner scheduling,
not exactly-once delivery: a remote operation can finish before an acknowledgement is
lost, or an old worker can take time to observe a failed connection. A new product job
with irreversible effects needs its own idempotency key or storage fencing contract.
Add scheduled work through `internal/worker.Run`; keep product collection and processing
outside the election code.

## Backups on replicas

The elected worker runs the schedule; manual runs from any replica and CLI deposits take the
same `backup-run` lock, and only those runs write or prune the local backup directory, so a
directory shared by every replica stays one consistent archive. Each run snapshots
PostgreSQL from one transaction into the usual `data/ky_server.db` capsule member, so
drills, restores and KyRecovery deposits behave as on SQLite. The snapshot sits in a private
`data/snapshot-*` directory while it is sealed; one left by a killed process is removed at
the next start. Restore goes through `migrate-postgres` into an empty database:
[RESTORE.md](RESTORE.md#restoring-a-postgresql-deployment).

## The recovery key on replicas

A pin or pairing writes the key to the replica that handled it and records the public key
in the database; every other replica writes its own `recovery.pub` from that record. Pins,
pairings and those writes take one database-wide `recovery-pin` lock and read the pin back
before they report success, so two admins pinning different keys at once get one 200 and
one 409, never two keys. A pin whose lock connection is lost mid-write is reported as
failed (503, try again).

A pin no replica can load is shown on the Backup page, logged at startup and recorded in
each scheduled run's audit row; the server still starts so the page can repair it. It
happens when a deployment pinned before this release moves to fresh replica volumes. Pin the
same key by hand on any replica (the ceremony page shows it), or copy its `recovery.pub`
into one replica's data directory and restart that replica. `migrate-postgres` records the
key from the `recovery.pub` beside the source database, so a migration or restore that
follows the runbooks does not need this.

## Product rollout contract

Products are separate Go modules: port these mechanisms and executable tests into their
own packages; the base's `internal/` packages cannot be imported by another module. Reuse
`ky-primitives/recoveryclient`; do not copy library code. A product becomes replica-ready
by keeping these:

- **(a) Health.** Keep `/livez` and `/readyz` and the drain order above; add a product
  dependency to readiness only if the product cannot serve without it.
- **(b) Shared settings.** Add every setting all replicas must agree on to the one list in
  `internal/config/replica.go` (`sharedFields`); leave per-host settings out.
- **(c) Tables.** `migrationTables` in `internal/store/migrate_postgres.go` is the single
  ordered table list for both `migrate-postgres` and the backup snapshot. Append product
  tables in foreign-key order; both directions refuse the schema until you do. Advance
  every product sequence/identity after migration and restore, not just the base's
  `audit_records` sequence. The product restore drill must insert into every such table
  afterwards and assert success without duplicate IDs or dropped audit writes.
- **(d) Restore.** `restore` turns the schedule off and drops sessions and the fingerprint
  (`prepareRestoredDatabase` in `cmd/server/main.go`); add the product's own session-like
  tables there.
- **(e) Scheduled work** goes only through `internal/worker.Run`, with idempotent jobs.
- **(f) State outside the database** (files, blobs, maildirs, local indexes) makes a product
  not replica-ready until it has a shared-storage contract. Defer such a product rather
  than half-support it.

## Move a deployed SQLite server to PostgreSQL

This command migrates the **base scaffold**. A derived Ky product with extra tables or
columns needs its own migration adapter; the command refuses those schemas instead of
silently omitting content.

Plan a maintenance window. Do not remove the SQLite deployment until PostgreSQL operation
and recovery have been verified: after cutover, make a capsule with Back up now and run
the restore drill.

1. Back up the deployment and retain its `.env`, `data/encryption.key`,
   `data/recovery.pub` and other operator-owned configuration. Stop the server, keeping
   its volumes (`docker compose stop app`; never remove volumes). Older binaries do not
   hold the new instance lock, so stopping them is an operator responsibility.
2. Provision an empty PostgreSQL database/schema and an account allowed to create the
   scaffold tables. Keep its DSN in a protected environment file as
   `KY_MIGRATE_POSTGRES_DSN`. Credentials are accepted through the environment, never
   through a command argument. Use verified TLS when the database is off-host.
3. Run the new binary once to check and once to copy. With Compose, save the migration
   DSN in a mode-0600 `migration.env` file, then use the existing Compose chain:

   ```bash
   docker compose run --rm --no-deps --env-from-file migration.env app migrate-postgres --check
   docker compose run --rm --no-deps --env-from-file migration.env app migrate-postgres
   ```

   `--env-from-file` requires a recent Docker Compose. If your version lacks it, securely
   export `KY_MIGRATE_POSTGRES_DSN` and use `-e KY_MIGRATE_POSTGRES_DSN` instead. For a
   direct binary install, with that variable already exported:

   ```bash
   ./ky_server_base migrate-postgres --source ./data/ky_server.db --check
   ./ky_server_base migrate-postgres --source ./data/ky_server.db
   ```

   The command identifies the original scaffold migration history, makes a private SQLite
   snapshot, upgrades that copy, checks integrity
   and foreign keys, copies every scaffold table, and verifies row counts and normalized
   content digests before committing. `--check` performs the same copy and checks, then
   rolls back the data. It may initialize empty target tables. Existing target rows,
   unknown tables/columns and incompatible values are refused. Failed copies roll back
   all rows. Timestamps use PostgreSQL's microsecond precision. Source SQLite bytes,
   keys and configuration remain unchanged; temporary plaintext snapshots are removed
   when the command exits normally. After an abrupt kill, inspect/remove the private
   `.postgres-migration-*` scratch directory once no migration is running.
4. Update the existing `.env` with `KY_DB_DRIVER=postgres` and `KY_DB_DSN` pointing at
   that target. Compose pins SQLite in its base environment, so append an overlay to
   the existing `COMPOSE_FILE` chain that sets:

   ```yaml
   services:
     app:
       environment:
         - KY_DB_DRIVER=postgres
         - KY_DB_DSN=${KY_DB_DSN:?set the PostgreSQL DSN}
   ```

   Keep all existing build, network, DNS and TLS overlays. Set an explicit stable
   `KY_SCIM_TOKEN`. Rotate `KY_SESSION_SECRET` once at cutover, sharing the new value
   on every replica, to invalidate CAPTCHA solutions from the former in-memory replay
   store. Keep the **encryption key** unchanged. Database sessions use opaque tokens
   and survive the copy; rotating the CAPTCHA signing secret does not delete them.
5. Start one PostgreSQL server and verify existing login, directory membership, SSO,
   revocation, settings and a real PostgreSQL backup/restore. Then add replicas and
   exercise a worker takeover. Remove the protected migration DSN file after use.

The command never edits `.env` or deletes SQLite. Before the first PostgreSQL write,
rollback is to restore the original connection settings/overlays and start the retained
SQLite deployment. After PostgreSQL accepts writes, switching back to the old SQLite
file would lose those writes; stop and plan a reverse data migration instead. Stop all
old-version replicas before upgrading security coordination; mixed old/new versions do
not share the new rate limits or CAPTCHA replay records.
