# SCIM

## Purpose
Adapts the `elimity-com/scim` RFC 7643/7644 server to the local user and group stores for enterprise provisioning from Okta, Azure AD / Microsoft Entra ID, OneLogin, and KySignOn.

## Ownership
Owns local persistence adapters and bearer authentication; the library owns `/scim/v2/Users`, `/scim/v2/Groups`, discovery endpoints, schema validation, filter/PATCH parsing, pagination, and SCIM error serialization.

## Local Contracts
- Content-Type for all SCIM endpoints must be `application/scim+json`.
- Requests must be authenticated with the configured bearer token.
- User de-provisioning via `PATCH` with `active: false` updates user status to `inactive`.
- A user `eq` filter matches the named attribute exactly: `userName` through `GetUserByUsername`; `emails`/`displayName` through the store's LIKE search as a candidate narrower, capped at `maxFilterCandidates` (`tooMany` beyond it).
- Group member ids are checked before any write, and only the membership difference is written.
- User writes go through `UpdateUserProfile`, never `UpdateUser`, so SCIM cannot rewrite credential or MFA columns from a stale read.
- SCIM protocol models and parsing must come from `github.com/elimity-com/scim`; do not add parallel local request/response implementations.

## Verification
- `go test -v ./internal/scim/...`

## Child DOX Index
None.
