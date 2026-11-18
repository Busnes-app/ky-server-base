# SQLite, PostgreSQL and replicas

[Overview](../README.md) · [Interfaces](INTERFACES.md) · [Disaster recovery](DISASTER-RECOVERY.md)

SQLite remains the default for a single server. It keeps the existing in-memory login
limits and CAPTCHA replay set, avoiding extra database writes on that deployment path.
The server holds a local instance lock; another server or migration using the same
SQLite file is refused. Keep the persistent `.lock` files in place. They are released
on process exit and must not be deleted while a process is running. SQLite on a network
filesystem is not a multi-host replication mode.

PostgreSQL supports multiple application servers behind your HTTPS reverse proxy.
Docker Compose, separate Docker hosts, systemd and Kubernetes can all run them. No
Kubernetes API, Redis or extra coordination service is required. PostgreSQL itself
remains an availability dependency; application replicas do not make its storage highly
available. Existing SQLite capacity results do not establish PostgreSQL capacity.

## Add replicas

1. Use the same PostgreSQL database **and schema** on all replicas. Connect directly to
   PostgreSQL or through a **session-pooling** proxy. Transaction-pooling proxies cannot
   retain the session advisory locks used for worker ownership.
2. Set the same `KY_APP_NAME`, public `KY_APP_URL`, `KY_SESSION_SECRET`, encryption key,
   identity-provider configuration, `KY_SCIM_TOKEN` and office-network environment list
   on every replica. PostgreSQL requires an explicit session secret, and an explicit
   SCIM token when SCIM is enabled. Preserve `data/encryption.key` and `data/recovery.pub`
   when adding a server, or provide the same retained `KY_ENCRYPTION_KEY` securely.
   A new local encryption key cannot decrypt existing sealed settings.
3. Give each server its own writable data/backup directories. Keep the recovery public
   key pins consistent; manage recovery pairing/pin changes on the designated operations
   server and distribute the resulting public pin to the other servers before use.
4. Point the reverse proxy at the application servers. Sticky sessions are unnecessary:
   sessions, revocations, login windows, office budgets and spent CAPTCHA challenges are
   shared in PostgreSQL. Configure `KY_TRUSTED_PROXIES` for the actual proxy peers.
5. Start with the default pool of 25 connections per server and budget the database's
   total connections across replicas. `KY_DB_MAX_OPEN_CONNS` must be at least 3: the
   elected worker reserves one connection, a running backup reserves a second, and
   ordinary queries need capacity too. PostgreSQL migrations and initial admin creation
   are serialized during concurrent startup.

The base Compose service has a fixed `container_name` and publishes port 8080. For
multiple containers on one host, use an operator-owned overlay that clears that name
and port binding, then attach the containers to your reverse proxy's private network.
Separate hosts can each use the existing single-service Compose file. Keep database
ports private. Kubernetes is an optional deployment choice, not a replication boundary.

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

## Move a deployed SQLite server to PostgreSQL

This command migrates the **base scaffold**. A derived Ky product with extra tables or
columns needs its own migration adapter; the command refuses those schemas instead of
silently omitting content.

Plan a maintenance window. First configure PostgreSQL backups and prove a restore:
**the built-in recovery capsules still snapshot SQLite only**. PostgreSQL needs its own
protected database backups plus retained encryption/recovery key files. Do not remove
the SQLite deployment until both PostgreSQL operation and recovery have been verified.

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
