# Contributing

[Overview](README.md) · [Assurance case](docs/ASSURANCE-CASE.md) · [Restore runbook](docs/RESTORE.md)

ky-server-base is the scaffold every Busnes.app server is built from, so a change here reaches
every product that rebases on it.

## Reporting

- **Bugs and requests:** open a GitHub issue.
- **Vulnerabilities:** report privately through the repository's **Security** tab, not in an issue.
  The [Busnes-app security policy](https://github.com/Busnes-app/.github/blob/main/SECURITY.md)
  covers this repository and gives the response times.

## Changing the code

1. Read [AGENTS.md](AGENTS.md) and every `AGENTS.md` on the way to what you change. They are the
   binding contracts for their directories; a change to a contract updates the nearest one in
   the same pull request, and a change to a trust boundary updates
   [docs/ASSURANCE-CASE.md](docs/ASSURANCE-CASE.md).
2. Open a pull request against `master`. The `Protect Main` ruleset requires the CI jobs by name:
   `Go build, vet & tests (SQLite)`, `Go tests (PostgreSQL)`, `Frontend typecheck & build`,
   `Browser regressions (production server)`, `Vulnerability scan`, `Docker image build & run`,
   `publish` and `promote` (the last two run only on `master`).
3. Run `make ci` before you push; add `make test-postgres` when a PostgreSQL server is at hand.

### Tests

New behaviour and every bug fix come with a test that fails without the change. Authentication,
authorization and anything that parses untrusted input get a test of the refused path; a new
privileged route joins `internal/api/authz_test.go`.

### Coding standard

Contributions follow these style guides. CI enforces the parts a tool can check, so a style
failure is a red build, not a review comment.

| Language | Style guide | Enforced by |
|---|---|---|
| Go | [Effective Go](https://go.dev/doc/effective_go) and the [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments) | `gofmt` (must list nothing), `go vet ./...`, `go mod tidy` with no diff and `go mod verify` in the `Go build, vet & tests (SQLite)` job; `make lint` and `make tidy-check` locally |
| TypeScript (`web/`) | The TypeScript compiler in `strict` mode, refusing unused locals and parameters | `tsc -b` in `npm run build` (`Frontend typecheck & build` job) |
| Shell (`scripts/`) | Clean under [ShellCheck](https://www.shellcheck.net/) | `shellcheck scripts/*.sh` in the `End-to-end smoke test` job |

Beyond what the tools check: keep to the comment density and naming of the file you are
editing, do not reimplement what `ky-primitives` provides, such as password hashing ([internal/crypto/AGENTS.md](internal/crypto/AGENTS.md)), and keep `web/dist`
committed and in step with `web/` (CI fails a stale build).
