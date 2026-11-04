# SSO

## Purpose
Provides unified Single Sign-On federation for KySignOn, Generic OpenID Connect (Google, Microsoft Entra ID, Okta, Keycloak), and SAML 2.0 Service Provider (SP).

## Ownership
Owns the application adapters around OAuth/OIDC login, verified back-channel logout and SAML metadata publication. KyIdentity directory synchronization belongs to the SCIM connector, not a webhook.

## Local Contracts
- PKCE with `S256` is enforced on all OAuth/OIDC authorization requests.
- ID tokens require provider signature, issuer, audience, expiry, and one-time nonce verification before claims are trusted.
- OAuth discovery, authorization URLs, PKCE parameters, code exchange, key
  caching/rotation and token verification stay in `oauth2`/`coreos/go-oidc`.
  `jwks_strength.go` bounds the verifier's private JWKS client, refuses redirects
  and rejects RSA members below 2048 actual modulus bits before caching. The
  provider/verifier pair is initialized once under the discovery mutex.
  Match the verifier's envelope parsing and go-jose's case-sensitive JWK member
  parsing; case variants must never substitute another modulus for this check.
- Discovery and token exchange use a client with a 10 s timeout in every mode
  (`strongOIDCContext`), because discovery holds the provider mutex.
- When `fips140.Enabled()`, issuer/authorization/token/JWKS endpoints require
  HTTPS without URL credentials; discovery/token redirects are refused. Preserve
  caller TLS trust roots and ordinary verification. This mode is process-wide.
- SAML assertion parsing is not implemented locally; metadata XML uses `encoding/xml` and no ACS route is exposed until a maintained SAML service-provider library is configured.
- `VerifyLogoutToken` (`backchannel.go`) reuses the ID-token verifier, then requires
  `iat`, `sub` or `sid`, the back-channel logout event as a JSON object and no `nonce`;
  a present `typ` must be `logout+jwt`. Every failure is `ErrInvalidLogoutToken`.
  `IdentityClaims.SessionID` and `IssuedAt` carry the ID token `sid` and `iat` so sessions can be ended by them.
- `IdentityClaims.Roles` is KyIdentity's per-app `roles` claim (nil when absent; a non-array is an invalid token). `LocalRole` maps it: `admin` in `roles` is admin, otherwise user; without `roles` the legacy `role`; with neither, `ok` is false and the stored role stays.
- `AdoptSCIMUser` links a SCIM account whose externalId equals the KyIdentity
  callback's subject before provisioning, retaining its SCIM management and id.
- A present empty per-app `roles` array means user even with global `role: admin`.
  Operators must assign the app's explicit `admin` role in KyIdentity before upgrading.

## Verification
- `go test -v ./internal/sso/...` (`TestVerifyLogoutToken` holds one rejection case per logout-token rule)
- Repeat with `GODEBUG=fips140=on`: RSA-1024/2047 refusal, strong token/cache/nonce
  behavior, bounded/malformed/redirected JWKS, HTTPS
  metadata policy and refusal of a trusted RSA-1024 TLS peer.
- `TestOIDCJWKMemberParsingMatchesVerifier` uses real RSA signatures in both
  adapters to check case variants, member order, and strong-key acceptance.

## Child DOX Index
None.
