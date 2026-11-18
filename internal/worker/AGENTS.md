# Purpose

Elect one built-in scheduled worker while all application replicas continue serving HTTP.

# Ownership

- Owns the election/retry loop and cancellation propagation. Storage owns locks; callers own jobs.

# Local Contracts

- Hold `scheduled-worker` through the owner's entire loop; standbys retry at the caller's interval.
- Call jobs only under the ownership context. Jobs must observe cancellation and tolerate retries.
- PostgreSQL ownership requires session affinity; no Kubernetes API or transaction pooler.

# Work Guidance

- Add product work through the existing callback; retain one process/codebase unless scaling requires otherwise.

# Verification

- `go test -race ./internal/worker ./internal/store` on SQLite and with `KY_TEST_POSTGRES_DSN`.

# Child DOX Index

- None.
