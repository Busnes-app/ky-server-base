# Config

## Purpose
Manages environment and file-based configuration loading, defaults, and type conversions for ky_server_base.

## Ownership
Owns environment variable parsing, configuration validation, default fallbacks, and security key generations.

## Local Contracts
- `LoadFromEnv() (*Config, error)` must supply safe, valid defaults for all subsystems.
- Never log plaintext secrets or sensitive tokens.
- `KY_TRUSTED_PROXIES` is a comma-separated list of reverse-proxy IPs or CIDRs, empty by default, parsed once at startup into `[]netip.Prefix`; an unparsable entry fails startup. Only a request whose peer address is in the list may speak for another client through `X-Forwarded-For`. `0.0.0.0/0` and `::/0` are refused at startup; list only the proxy's own address or subnet.
- `KY_RATE_LIMIT_EXEMPT_NETWORKS` is validated at startup by `ky-primitives/netallow` and kept as its canonical string in `Security.RateLimitExemptNetworks`; an invalid, too-broad or loopback entry fails startup. `internal/api` combines it with the admin setting.
- An https `KY_APP_URL` sets `Security.HostCookies` (`__Host-` cookie names, which must be Secure and take no Domain), so `KY_COOKIE_DOMAIN` or `KY_COOKIE_SECURE=false` with it fails startup. A plain-HTTP URL keeps bare names even with Secure cookies (`KY_ENV=production`).
- Production startup requires an explicit, durable `KY_SESSION_SECRET`. The encryption key comes from `KY_ENCRYPTION_KEY` when set, otherwise from the keyfile at `<DataDir>/encryption.key`, which `keyfile.LoadOrCreate` mints on first start; either is a valid production configuration.
- A supplied session signing secret requires at least `crypto.MinHMACKeyBytes` (16)
  bytes before keyfile side effects. Reject short values without logging or replacing
  them; generated signing secrets remain 32 random bytes encoded as hex.
- `KY_KYSIGNON_HMAC_SECRET` and `SSOConfig.KySignOnHMACSecret` remain accepted for
  compatibility but have no effect. A nonempty environment value logs a startup warning
  to use KyIdentity's SCIM connector at `/scim/v2`, never the value itself.

- `KY_BACKUP_DEPOSIT_INTERVAL` is a Go duration (default `24h`), only the default for the schedule the admin screen stores; `0` is off, anything else below `MinDepositInterval` (15m) or negative fails startup. `KY_BACKUP_DIR` (default empty, off) is the sealed local-copy directory and `KY_BACKUP_KEEP` (default 7) how many to retain; below 1 fails startup because the lib refuses it at write time. `KY_BACKUP_ALLOW_PRIVATE_RECOVERY` (default false) admits RFC1918 and CGNAT KyRecovery destinations only.

## Verification
- `go test -v ./internal/config/...`
- `go test -v ./internal/auth/ -run TestClientIP` (the helper that consumes the allowlist)

## Child DOX Index
None.
