# SCIM

## Purpose
Adapts the `elimity-com/scim` RFC 7643/7644 server to the local user and group stores for enterprise provisioning from Okta, Azure AD / Microsoft Entra ID, OneLogin, and KySignOn.

## Ownership
Owns local persistence adapters and bearer authentication; the library owns `/scim/v2/Users`, `/scim/v2/Groups`, discovery endpoints, schema validation, filter/PATCH parsing, pagination, and SCIM error serialization.

## Local Contracts
- Content-Type for all SCIM endpoints must be `application/scim+json`.
- Requests must be authenticated with the configured bearer token.
- SCIM is scoped to `SCIMManaged` accounts (`managedUser`, `ListSCIMUsers`): any other id is 404 and cannot be a group member. `checkRole` refuses granting `admin` unless `KY_SCIM_ALLOW_ADMIN`; an existing admin can still be changed.
- User PUT/PATCH go through `update`, which retries on `store.ErrConflict` so a write from a stale read never restores a role or status.
- User de-provisioning via `PATCH` with `active: false` updates user status to `inactive`.
- A user `eq` filter matches the named attribute exactly: `userName` through `GetUserByUsername`; `emails`/`displayName` through the store's LIKE search as a candidate narrower, capped at `maxFilterCandidates` (`tooMany` beyond it).
- A group filter is `displayName eq` (through `GetGroupByName`) or `externalId eq`; anything else is `invalidFilter`, never an unfiltered page.
- An empty roles value sent in PUT or a roles replace sets `user` (`roleValue`); a PUT that omits `roles` or a PATCH without a roles operation leaves the role alone.
- `update` writes through `UpdateSCIMProfile`: the profile and a changed `externalId` in one statement, compared against the subject read (retried on `ErrConflict`), the subject only while the row is still `scim`; an adopted account keeps its sign-in subject and the response shows the stored one. A PUT without `externalId` keeps the stored value.
- User PATCH: filtered `emails`/`roles` paths apply only when `selects` matches the value `userResource` emits; only an unmatched emails/roles replace is `noTarget`. Paths into unstored attributes are ignored, never refused.
- Group PATCH applies `add`/`remove`/`replace` to the current membership. Member sub-attribute paths and member filters other than remove `members[value eq "id"]` are `invalidPath` before any write. Member ids are checked before any write, and only the membership difference is written. Group PATCH and PUT hold `groupResourceHandler.mu`, so a PATCH computing membership from its read cannot drop a concurrent one's change (one server process per database). `UpdateGroup` maps a taken name to `ErrAlreadyExists` (409 `uniqueness`).
- User writes go through `UpdateUserProfile`, never `UpdateUser`, so SCIM cannot rewrite credential columns from a stale read.
- SCIM protocol models and parsing must come from `github.com/elimity-com/scim`; do not add parallel local request/response implementations.

## Verification
- `go test -v ./internal/scim/...`

## Child DOX Index
None.
