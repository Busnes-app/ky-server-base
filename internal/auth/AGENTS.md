# Auth

## Purpose
Manages user authentication, password policies, session token lifecycle, client addresses for rate limits, and anti-abuse CAPTCHA (Proof-of-Work default). There is no local MFA: MFA is a KyIdentity (SSO) feature.

## Ownership
Owns session issuance and verification, the client-address helpers, and PoW challenge generation and verification.

## Local Contracts
- Sessions for `must_change_password` users authenticate only GET `/api/auth/me` and POST `/api/auth/change-password` or `/api/auth/logout`; private settings and product actions fail closed. Session issuance is bound to the verified password hash and serialized with replacement.
- Passwords require at least 12 characters (`ValidatePassword`).
- `Session.CreatedAt` is the credential time. `IssueSession` stamps now.
- `IssueSSOSession` stores the provider's `sid` and dates the session by the ID token's `iat`, so back-channel logout by `sid` or by subject can end it; it fails with `store.ErrSSOSessionEnded` when that login was already ended.
- Active sessions are stored with SHA-256 hashed tokens and verified against secure HttpOnly / SameSite cookies or Bearer headers.
- `CookieName` names every cookie the server sets or reads: with `HostCookies` (an https `KY_APP_URL` and Secure cookies) it adds the `__Host-` prefix (Secure, `Path=/`, no Domain), so a sibling subdomain cannot plant a session, CSRF or SSO state cookie; bare names are then never read. `ValidateCSRF` takes the same flag. A bare-name sign-in expires a stale `__Host-ky_csrf`, which the web app would otherwise prefer.
- Session authentication rejects inactive accounts and deletes their presented session.
- PoW solutions must carry unexpired server-signed challenge metadata. `VerifyPoWSolution` with a `PoWSpent` set accepts each solved salt once until it expires; salts are recorded only after the signature and work check out, and expired ones are pruned whenever the set has doubled; there is no cap that would let a solution through unrecorded.
- The default puzzle size is `config.DefaultPoWDifficulty`, measured so the browser's median solve is about one second; re-measure before changing it.
- Local TOTP and recovery codes are retired: their columns keep their data but nothing reads them. Do not add local MFA back.
- `ClientIP` is the one client-address helper: the rate limiter and session binding both use it. `X-Forwarded-For` is honoured only when the peer is in `KY_TRUSTED_PROXIES`, walking the chain from the right past trusted hops; `X-Real-IP` is ignored. Each entry may be a bare IP or `ip:port` (Azure App Service/Application Gateway append the port); anything else ends the walk and falls back to the peer.
- `RateLimitKey` reduces an IPv6 client to its /64 for rate limits; IPv4 is unchanged.

## Verification
- `go test -v ./internal/auth/...`

## Child DOX Index
None.
