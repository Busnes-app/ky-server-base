# Frontend dependency audit

`npm --prefix web run audit` is the CI and `make ci` gate. It runs its regression
checks, an unconditional production-dependency audit, then the full dependency audit.
High/critical production findings always fail. Other high/critical findings also fail
except for the single temporary development-tool exception below. Registry errors,
malformed reports and unknown/cyclic dependency chains fail closed.

## Temporary picomatch exception

- Advisory: [GHSA-fc5q-39xc-vjpf](https://github.com/advisories/GHSA-fc5q-39xc-vjpf),
  CVE-2026-112366, repeated-extglob regular-expression denial of service.
- Accepted version: **4.0.7 only**, with **every affected package node marked `dev: true`**
  in `web/package-lock.json`. Transitive Vite/Vitest findings are accepted only when
  every reported cause resolves to this same advisory. Critical severity is not excepted.
- Expiry: **2026-12-01 00:00 UTC**. The gate fails after this deadline; renewal requires
  another explicit reviewed change. Changing the advisory/range/version also fails.
- State verified on 2026-11-18: the npm registry's latest release is 4.0.7 and the
  GitHub advisory has no `first_patched_version`. The advisory was published October 19
  and updated November 17. A forced toolchain downgrade is not a verified vulnerability fix.

This is temporary risk acceptance, **not a fix to picomatch**. Its vulnerable API takes
an attacker-controlled glob pattern. Here the package is used by Vite, Vitest and their
file-discovery dependencies with repository-owned build/test patterns. It is absent
from the production dependency graph; source inspection found no application import.
The production server is Go with embedded static assets and has no Node.js runtime.
The remaining risk is a stalled build/test when repository-owned patterns trigger the
bug. CI job timeouts still bound those jobs. Reassess this exception before exposing
these tools to untrusted glob inputs or importing the package into application code.

`scripts/npm-audit.mjs` prints the accepted advisory on every affected run. The raw
`npm audit --audit-level=high` continues to report the unresolved finding; the scoped
gate does not describe the full dependency graph as vulnerability-free. It accepts no
other advisory or version and cannot waive a production finding.

When a patched release is available, update the lockfile, run the full raw audit,
frontend tests/build and browser checks, then remove the exception and its dated note.
