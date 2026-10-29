# ky_server_base

[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/15937/badge)](https://www.bestpractices.dev/projects/15937)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/Busnes-app/ky-server-base/badge)](https://scorecard.dev/viewer/?uri=github.com/Busnes-app/ky-server-base)

The scaffold every Busnes.app server is built from: Go backend, embedded React PWA, SQLite or
PostgreSQL, local and federated sign-in (KySignOn, OIDC, SAML), SCIM provisioning, and
disaster recovery through the suite's KyRecovery.

Published image:

```bash
make ci        # gofmt, vet, race tests, smoke test
make run       # build and start on :8080; first start prints the bootstrap admin password
docker compose up -d
```

Source install (never paste this into a published-image install: the build overlay wins over a
`KY_IMAGE` digest pin, and a source install must set this line before its first `up -d` on a
new checkout; an install from before the published image existed has no such line yet, so run
this block once and confirm with `docker compose config --images`, which must print
`ky_server_base:local` rather than the `ghcr.io` name):

```bash
make ci        # gofmt, vet, race tests, smoke test
make run       # build and start on :8080; first start prints the bootstrap admin password
(umask 077; t=$(mktemp ./.env.XXXXXX) && touch .env \
  && cf=$({ grep '^COMPOSE_FILE=' .env || [ $? -eq 1 ]; } | tail -n1 | cut -d= -f2-) && cf=${cf:-docker-compose.yml} \
  && case ":$cf:" in *:docker-compose.build.yml:*) ;; *) cf="$cf:docker-compose.build.yml";; esac \
  && { grep -v -e '^COMPOSE_FILE=' .env || [ $? -eq 1 ]; } > "$t" \
  && printf 'COMPOSE_FILE=%s\n' "$cf" >> "$t" && mv "$t" .env)
docker compose up -d
```

Read the [release notes](CHANGELOG.md) for the selected source revision and every
intervening release before upgrading; they describe migrations and upgrade impact.

Update a published-image install on the rolling tag:

```bash
docker compose pull && docker compose up -d
```

A digest-pinned install (`KY_IMAGE` in `.env`) gets nothing from `pull`: re-run the pin recipe in
`docker-compose.yml` with the commit sha you want first, or delete that line to follow `:latest` again.

`AGENTS.md` is the contract for working in this repository. [CONTRIBUTING.md](CONTRIBUTING.md) has
the process, test policy and coding standard, and [docs/ASSURANCE-CASE.md](docs/ASSURANCE-CASE.md)
the threat model, trust boundaries and why the security requirements hold.

[External interface reference](docs/INTERFACES.md) documents HTTP routes, request and
response fields, authentication/CSRF, SCIM limitations, CLI commands and environment
configuration. It describes the scaffold's implemented interfaces; derived products
document their own additions.

[Strong-key/TLS configuration](docs/STRONG-CRYPTO.md) describes the optional
production profile, Compose overlay and migration for existing signing secrets
and IdP keys. Encryption/recovery keys and stored records stay in place.

## First sign-in

Every bootstrap or `init-admin` password must be replaced, including when
`KY_ADMIN_PASSWORD` supplies it. Operator resets revoke existing sessions, MFA challenges and
device pairings immediately, reactivate local admins and require replacement at the next login. Sign in, enter the current password and a different password
of at least 12 characters, then sign in again. Until replacement, the session can only check
its identity, change the password or sign out; privileged APIs remain blocked. Replacement
revokes existing sessions, MFA transactions and device pairings atomically. Existing accounts
are not retroactively flagged, since the server cannot infer whether they still use a bootstrap password.

## Upgrading after the Busnes-app owner move

The GitHub organisation was renamed on 2026-09-16 and the image now lives at `ghcr.io/busnes-app/ky-server-base`. The project no longer controls `ghcr.io/busness-app`; GHCR does not redirect it, and anything served under that name must be treated as untrusted. If `KY_IMAGE` still names the old namespace or image name, re-pinning is required, not optional: inspect `git remote -v` before any `git pull`, `make ci`, or `docker compose` command, and replace a retired-owner remote with `https://github.com/Busnes-app/ky-server-base.git` (prefer a fresh clone plus a known commit). Then remove `KY_IMAGE` to follow the compose default or verify and pin a digest using `docs/RESTORE.md` before pulling.

## License

ky_server_base is released under the [MIT License](LICENSE.txt).

[![OctoCounts](https://api.octocounts.com/badge/Busnes-app/ky-server-base/branch/master)](https://octocounts.com/github/Busnes-app/ky-server-base/tree/master)

## Documentation

- [Container networking](docs/CONTAINER-IP.md) — give the app a static IP on the Docker network. <a id="container-ip-address"></a>
- [Backups and disaster recovery](docs/DISASTER-RECOVERY.md) — sealed backup capsules, KyRecovery pairing, every `KY_BACKUP_*` variable and the LAN DNS override. <a id="disaster-recovery"></a><a id="two-ways-to-get-a-key"></a><a id="why-tls-matters-here"></a><a id="one-run-every-destination"></a><a id="environment"></a><a id="upgrading-from-plaintext-local-backups"></a><a id="restoring"></a>
- [Interfaces](docs/INTERFACES.md) — the HTTP, command-line and environment reference.
- [Restore](docs/RESTORE.md) — bring a server back from a recovery capsule.
- [Strong keys](docs/STRONG-CRYPTO.md) — the supported strong-key configuration.
- [Assurance case](docs/ASSURANCE-CASE.md) — threat model, trust boundaries, and why the security requirements are met.
- [Contributing](CONTRIBUTING.md) — how to propose a change and what it must pass.
- [Security policy](SECURITY.md) — how to report a vulnerability privately.
- [Release notes](CHANGELOG.md) — what changed in each release, and its upgrade impact.
