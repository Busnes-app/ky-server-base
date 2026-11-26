#!/usr/bin/env bash
# Refuse classical-only TLS overrides and verify outbound hybrid PQ TLS (X25519MLKEM768)
# in both default and GODEBUG=fips140=on modes.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

mapfile -d '' -t go_files < <(git ls-files -z -- '*.go' ':!*_test.go')
if [ "${#go_files[@]}" -gt 0 ]; then
  found=$(grep -HnE '\bCurvePreferences\b' -- "${go_files[@]}" || true)
  if [ -n "$found" ]; then
    echo "CurvePreferences found in non-test Go code (use Go's default hybrid PQ TLS groups):" >&2
    echo "$found" >&2
    exit 1
  fi
fi

mapfile -d '' -t mlkem_files < <(git ls-files -z -- \
  '*.go' 'go.mod' \
  '.github/workflows/*' \
  '*Dockerfile*' \
  '*compose*.yml' '*compose*.yaml' \
  'Makefile' 'deploy/*' 'scripts/*' \
  ':!*_test.go' ':!*_test.*' ':!*.test.*' ':!scripts/check-pqc-tls.sh')
if [ "${#mlkem_files[@]}" -gt 0 ]; then
  found=$(grep -HinF 'tlsmlkem' -- "${mlkem_files[@]}" || true)
  if [ -n "$found" ]; then
    echo "tlsmlkem override found (hybrid ML-KEM TLS must stay enabled):" >&2
    echo "$found" >&2
    exit 1
  fi
fi

go test -race -count=1 -run '^TestOIDCOutboundPQTLS$' ./internal/sso
GODEBUG=fips140=on go test -race -count=1 -run '^TestOIDCOutboundPQTLS$' ./internal/sso
