# SSO

## Purpose
Provides unified Single Sign-On federation for KySignOn, Generic OpenID Connect (Google, Microsoft Entra ID, Okta, Keycloak), and SAML 2.0 Service Provider (SP).

## Ownership
Owns the application adapters around OAuth/OIDC login, KySignOn HMAC-SHA256 signed directory sync webhooks, and SAML metadata publication.

## Local Contracts
- `KySignOnClient.HandleSyncWebhook` refuses keys below `crypto.MinHMACKeyBytes`
  and verifies HMAC-SHA256 signatures before modifying local user state.
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
- Directory deprovisioning (`user.deactivated`, `user.deleted`) ignores only
  `store.ErrNotFound`; any other lookup error fails the webhook. Updates use
  `UpdateUserProfile`, a compare-and-swap on the role and status read.
- When `fips140.Enabled()`, issuer/authorization/token/JWKS endpoints require
  HTTPS without URL credentials; discovery/token redirects are refused. Preserve
  caller TLS trust roots and ordinary verification. This mode is process-wide.
- SAML assertion parsing is not implemented locally; metadata XML uses `encoding/xml` and no ACS route is exposed until a maintained SAML service-provider library is configured.
- `VerifyLogoutToken` (`backchannel.go`) reuses the ID-token verifier, then requires
  `iat`, `sub` or `sid`, the back-channel logout event as a JSON object and no `nonce`;
  a present `typ` must be `logout+jwt`. Every failure is `ErrInvalidLogoutToken`.
  `IdentityClaims.SessionID` and `IssuedAt` carry the ID token `sid` and `iat` so sessions can be ended by them.
- Directory webhook timestamps are accepted only within five minutes and are ordered per subject through `AdvanceDirectoryEvent`: an event older than the last applied one is acknowledged and dropped. Status or role changes revoke the user's sessions in the same transaction (`UpdateUserProfile`); deactivation also deletes sessions when the row was already inactive, so a retry still ends them.

## Verification
- `go test -v ./internal/sso/...` (`TestVerifyLogoutToken` holds one rejection case per logout-token rule)
- Repeat with `GODEBUG=fips140=on`: RSA-1024/2047 refusal, strong token/cache/nonce
  behavior, no short-key sync mutation, bounded/malformed/redirected JWKS, HTTPS
  metadata policy and refusal of a trusted RSA-1024 TLS peer.
- `TestOIDCJWKMemberParsingMatchesVerifier` uses real RSA signatures in both
  adapters to check case variants, member order, and strong-key acceptance.

## Child DOX Index
None.
