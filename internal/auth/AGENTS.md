# Auth

## Purpose
Manages user authentication, password policies, Multi-Factor Authentication (RFC 6238 TOTP), recovery codes, session token lifecycle, and anti-abuse CAPTCHA (Proof-of-Work default).

## Ownership
Owns session issuance and verification, TOTP token generation/validation, recovery code redemption, and client-side PoW verification.

## Local Contracts
- Sessions for `must_change_password` users authenticate only GET `/api/auth/me` and POST `/api/auth/change-password` or `/api/auth/logout`; private settings and product actions fail closed. Session issuance is bound to the verified password hash and serialized with replacement.
- Passwords require at least 12 characters (`ValidatePassword`).
- `Session.CreatedAt` is the credential time. `IssueSession` stamps now.
- `IssueSSOSession` stores the provider's `sid` and dates the session by the ID token's `iat`, so back-channel logout by `sid` or by subject can end it; it fails with `store.ErrSSOSessionEnded` when that login was already ended.
- Active sessions are stored with SHA-256 hashed tokens and verified against secure HttpOnly / SameSite cookies or Bearer headers.
- Session authentication rejects inactive accounts and deletes their presented session.
- PoW solutions must carry unexpired server-signed challenge metadata.
- Password verification creates a five-minute opaque MFA transaction; MFA endpoints never accept a user ID and consume the transaction once.
- TOTP generation uses 20 random decoded bytes; validation refuses fewer than 16 through the pinned shared library. Keep existing sealed records; a short legacy secret requires recovery or an authorized administrator reset and issuer re-enrollment.
- Single-use recovery codes are invalidated immediately upon redemption.
- `ClientIP` is the one client-address helper: the rate limiter and session binding both use it. `X-Forwarded-For` is honoured only when the peer is in `KY_TRUSTED_PROXIES`, walking the chain from the right past trusted hops; `X-Real-IP` is ignored. Each entry may be a bare IP or `ip:port` (Azure App Service/Application Gateway append the port); anything else ends the walk and falls back to the peer.

## Verification
- `go test -v ./internal/auth/...`

## Child DOX Index
None.
