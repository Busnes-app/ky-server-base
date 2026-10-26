# Contributing to ky_server_base

ky_server_base is the scaffold every Busnes.app server is built from: Go backend, embedded React PWA, SQLite/storage glue, auth, SSO, and the shared backup adapter. A change here propagates to every server that derives from it, so secure-by-default behavior and conservative changes are the whole point.

## Before contributing

Read [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) and [SECURITY.md](SECURITY.md),
the [README](README.md), and any AGENTS.md in the subtree you are about to
touch. Open an issue before proposing a feature larger than a bug fix or
documentation change.

Keep changes small, reuse existing code, and fix the shared root cause rather
than the reported symptom. New non-trivial logic ships with a runnable test or
self-check.

## Security requirements

- Secure by default: new shared behavior fails closed, binds safely, and never ships a permissive default to quiet a first run.
- Shared auth, session, SSO, and backup code must preserve its trust boundaries; a weakening here weakens every derived server.
- Validate at boundaries; never log passwords, tokens, keys, or secrets in shared code paths.
- Do not add telemetry, phone-home, analytics, or unconfigured third-party
  services.
- Do not add silent security fallbacks; fail closed with a readable error and a
  remediation step.
- Name and document any security trade-off in [SECURITY.md](SECURITY.md) before
  it merges.

## Verification

Run the checks that apply to the files you changed, and the repository's
full required checks before hand-off. Record the exact commands and their
results in the pull request.

## AI attribution

AI-assisted contributions are welcome; undisclosed ones are not. Disclose
material AI assistance in the pull request — the tool, what it changed, and what
you personally ran, read, and verified — and add a commit trailer naming any
tool that materially contributed. You are the author of the code you submit for
every purpose, regardless of how it was produced.

## Pull requests

Branch off `master` with a focused name, and use a Conventional Commit subject
(`fix:`, `feat:`, `docs:`, `refactor:`, `test:`, or `chore:`). The pull request
explains why the change exists, the verification performed, the security impact,
and any known limitations.

Every pull request gets an adversarial review pass; see
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md#hostile-reviews-are-about-code-never-people)
for what that is and the boundary it keeps.

## Release notes

Every successful image publication is a commit-identified release, including
dependency, workflow and documentation-only changes. Before merging, replace
`CHANGELOG.md`'s Current source summary with the major changes and upgrade
impact; include known advisory IDs and explicit migration or no-migration guidance.
Archive the preceding shipped summary under its known source commit.

Run `make notes-check` (Python 3) and review the prose against the actual diff.
CI checks presence/freshness against the tested commit's first parent before
publication; the check does not establish that the prose is accurate or complete.
See [the release-note process](CHANGELOG.md#maintaining-notes) for local base
selection and how an image digest maps to its notes.

## Licence

ky_server_base is licensed under the MIT License. See [LICENSE.txt](LICENSE.txt).

