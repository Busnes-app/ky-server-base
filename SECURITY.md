# Security Policy

ky_server_base is the shared scaffold for suite servers. Its defaults and shared code (auth, SSO, storage, backup) become the defaults of every server built from it, so its trust boundaries are inherited broadly.

## Reporting vulnerabilities

Report security vulnerabilities privately through GitHub Security Advisories:
<https://github.com/Busnes-app/ky-server-base/security/advisories>. Do not open a public issue.
Include the affected area, reproduction steps, impact, and affected versions.
We aim to acknowledge reports within two business days and will coordinate a fix
and disclosure.

## Trust boundaries and limitations

- A defect or weak default here is inherited by every derived server. Prefer secure defaults and the standard library; justify new shared dependencies.
- Shared authentication, session, and SSO code is a trust boundary; changes need attack-path tests, not just the happy path.
- The shared backup adapter wires `ky-primitives/recoveryclient`; it must keep the seal, pin, and local-copy contracts and never hold a recovery private key or share.

## Operating requirements

- Derived servers run behind HTTPS with no cleartext fallback; keep the data directory and backups private.
- Provide rate limiting and lockout defaults that derived servers inherit rather than re-implement.
- Keep secret handling in one shared place so every server gets the same guarantees.

## Security-sensitive changes

Changes to shared authentication, sessions, SSO, storage, the backup adapter, or any default that affects derived servers require tests for both the expected path and the relevant
failure or attack path, plus a description of the trust boundary in the pull
request.

## Related documents

- [CONTRIBUTING.md](CONTRIBUTING.md) — contribution and review requirements
- [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) — community expectations
- [README.md](README.md) — what this project is and how to run it

