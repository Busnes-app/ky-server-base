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
3. Give each server its own writable data and backup directories. Per-host settings stay
   per host: `KY_TRUSTED_PROXIES`, `KY_BACKUP_DIR`, `KY_BACKUP_KEEP`, host, port and pool
   sizes. A pairing or a hand pin lands on whichever replica gets the request; the others
   write their own `data/recovery.pub` from the pin in the database before their next backup
   and at startup. A replica whose `recovery.pub` names another key refuses to start; remove
   the stale file and it is rewritten.
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

## Compose replicas on one host

`docker-compose.replicas.yml` runs `KY_REPLICAS` (default 2) app containers on one PostgreSQL
behind HAProxy. Append it **last** to the `COMPOSE_FILE` chain in `.env` (after
`docker-compose.build.yml` for a source install). Never combine it with
`docker-compose.static-ip.yml`, which gives one container one address.

| Variable | Required | Meaning |
|---|---|---|
| `KY_DB_DSN` | yes | `postgres://ky_server:<KY_POSTGRES_PASSWORD>@postgres:5432/ky_server?sslmode=disable` (the private database network); keep it in `.env`, never argv |
| `KY_POSTGRES_PASSWORD` | yes | The bundled PostgreSQL's password; there is no default |
| `KY_SESSION_SECRET`, `KY_SCIM_TOKEN` | yes | Shared secrets, `openssl rand -hex 32` written straight into `.env` |
| `KY_ENCRYPTION_KEY` | yes | The retained 32-byte key in hex; never regenerate it on an existing deployment |
| `KY_REPLICA_SUBNET` | yes | The proxy network, e.g. `172.31.250.0/24` |
| `KY_PROXY_IP` | yes | HAProxy's fixed address in that subnet; the replicas trust only `KY_PROXY_IP/32` |
| `KY_EDGE_TRUSTED_PROXIES` | no | The TLS proxy in front of HAProxy as HAProxy sees it (see below) |
| `KY_PROXY_BIND`, `KY_PROXY_PORT` | no | Published address, default `127.0.0.1:8080` |
| `KY_REPLICAS` | no | Replica count, default 2, at most 8 (`server-template` in `deploy/replicas/haproxy.cfg`) |

```bash
docker compose config --quiet    # names any missing required variable
docker compose up -d --wait
```

What it sets up:

- **App replicas** clear the base `container_name` and port, run `KY_DB_DRIVER=postgres`,
  and share no files: each container has its own anonymous `/app/data` and `/app/backups`
  volumes. A local capsule stays in the volume of the replica that sealed it
  (`docker compose cp --index N app:/app/backups/<file> .`); KyRecovery deposits do not
  depend on which replica ran. `docker compose down -v` deletes those volumes with the
  database; copy capsules out first.
- **PostgreSQL** (digest-pinned) runs on an `internal` network with no route out and no host
  port; replicas wait for its `pg_isready` healthcheck.
- **HAProxy** (digest-pinned, `deploy/replicas/haproxy.cfg`) finds replicas through Docker's
  DNS (`server-template` with a resolver) and probes each one's `/readyz` every second; two
  failures take a replica out, two passes bring it back. A request whose connection failed
  is sent to another replica; a GET or HEAD that died mid-request or timed out is retried
  there too; any other method is never sent twice. `/api/backup/*` gets the server's 16-minute
  budget. With every replica unready HAProxy answers 503 at once; a request already inside a
  replica when the database froze waits for it, up to HAProxy's 30-second server timeout
  (measured: new requests got 503 4.3 s after a pause, 1.8 s after a stop).

HAProxy publishes on loopback. Put your TLS proxy in front of it. HAProxy appends the peer
it saw to `X-Forwarded-For`; with the default loopback publish that peer is the replicas
network gateway (the host side of the bridge, e.g. `172.31.250.1`), so every client counts as
one address for sign-in limits until you set `KY_EDGE_TRUSTED_PROXIES=<that gateway>/32`.
Only processes on the host can reach a loopback publish, which is what makes trusting it
safe; never list a range.

`docker compose stop` is graceful; for one replica, `docker stop <container>` sends the same
SIGTERM. A killed or stopped replica is restarted by `restart: unless-stopped` or the next
`docker compose up -d`.

## Separate hosts

Each host runs the base file plus an overlay that switches it to PostgreSQL (the snippet in
step 4 of the migration below, with `KY_ENCRYPTION_KEY` supplied too), behind your own
health-checking proxy. Give the proxy `/readyz` as the probe, a one-second interval, and
retries for idempotent requests only.

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

1. Stop every replica.
2. Change the value everywhere it is configured.
3. Run once, with the new configuration:
   `docker compose run --rm --no-deps app accept-replica-config`
   (bare binary: `ky_server_base accept-replica-config`). It rewrites the record and audits
   `admin.replica_config_accepted` with the changed field names.
4. Start the replicas. One still holding the old value refuses to start.

Never rotate the encryption key this way: the sealed KyRecovery token is encrypted under it.

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
same `backup-run` lock. Each run snapshots PostgreSQL from one transaction into the usual
`data/ky_server.db` capsule member, so drills, restores and KyRecovery deposits behave as on
SQLite. Restore goes through `migrate-postgres` into an empty database:
[RESTORE.md](RESTORE.md#restoring-a-postgresql-deployment).

## Product rollout contract

A product built on this base becomes replica-ready by keeping these:

- **(a) Health.** Keep `/livez` and `/readyz` and the drain order above; add a product
  dependency to readiness only if the product cannot serve without it.
- **(b) Shared settings.** Add every setting all replicas must agree on to the one list in
  `internal/config/replica.go` (`sharedFields`); leave per-host settings out.
- **(c) Tables.** `migrationTables` in `internal/store/migrate_postgres.go` is the single
  ordered table list for both `migrate-postgres` and the backup snapshot. Append product
  tables in foreign-key order; both directions refuse the schema until you do.
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
