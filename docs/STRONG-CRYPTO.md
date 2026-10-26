# Supported strong-key configuration

[Overview](../README.md) · [Interfaces](INTERFACES.md) · [Assurance case](ASSURANCE-CASE.md)

This profile keeps local authentication, KyIdentity/KySignOn OIDC, provisioning
and sealed recovery available while refusing undersized signing keys and TLS
peers. It is a supported configuration of this scaffold, not a claim that a live
instance or every derived product has been verified, or a FIPS certification.

## Prepare existing installations

- Keep `data/encryption.key`, the database, recovery public-key pin and existing
  capsules. The encryption key must remain exactly 32 bytes. Do not generate a
  replacement to enable this profile: replacing it orphans encrypted data.
- Provision an independent random session signing secret; 32 random bytes encoded
  as hex is the recommended value. `KY_SESSION_SECRET` is used as the literal
  string's bytes, not hex-decoded. Startup refuses a supplied value below 16
  bytes, and production still requires an explicit durable value. For a new
  instance, generate it with `openssl rand -hex 32` and save it privately.
- If directory-sync webhooks are enabled, coordinate a new independent random
  `KY_KYSIGNON_HMAC_SECRET` with the sender when the existing one is too short.
  A nonempty value below 16 bytes fails startup, and webhook use checks the same
  floor even for a programmatically constructed client. Empty leaves sync
  unconfigured. Do not print either secret in logs or put it in source control.
- Check the IdP's JWKS: every RSA member must have at least 2048 modulus bits.
  A newly fetched set containing a smaller RSA key is refused, including a weak
  key retained beside a strong key. Replace/retire weak signing keys at the IdP;
  do not silently accept one or drop trust verification. Existing cached valid
  keys keep the upstream cache's normal lifecycle.
  The floor parses the envelope and case-sensitive JWK members with the same
  rules as the signature verifier, checking the actual modulus it will use.

An undersized configured signing secret can no longer start the server after
this update, even outside the optional TLS profile. Rotate it deliberately;
the application does not pad it, regenerate it or rewrite stored encryption keys.
Changing the session signing secret invalidates outstanding PoW challenges.

## Enable the process profile

Use the version of Go specified by `go.mod`/`toolchain` (the Dockerfile selects
its build toolchain). Set `GODEBUG=fips140=on` before process startup. With the
documented direct OIDC endpoints, the flow requires HTTPS for issuer,
authorization, token and JWKS URLs and refuses redirects. JWKS redirects are
refused in every mode to prevent bypassing the key policy. Normal certificate
chain/hostname checks, PKCE, signature/issuer/audience/expiry/nonce verification,
upstream key rotation and caching remain in place.

Go's runtime TLS filter requires RSA-2048 or larger and approved P-256/P-384/P-521
or fixed-size Ed25519 certificate keys across the verified chain, including
trusted roots. It applies to this server's Go HTTPS connections, including
OIDC and KyRecovery; a private CA or pin does not waive the floor. Keep normal
verification enabled. The profile uses `on`, not the diagnostic `only` mode;
Argon2id, HMAC-SHA1 TOTP and the fixed X-Wing/AES-256-GCM recovery format remain.

For a process installation, retain its existing environment and add:

```sh
export GODEBUG=fips140=on
export KY_ENV=production
export KY_COOKIE_SECURE=true
export KY_APP_URL=https://your-public-host.example
# Load your previously prepared KY_SESSION_SECRET privately, then start the server.
./ky_server_base
```

If an existing `GODEBUG` contains other settings, retain them and set its
`fips140` entry to `on`. Restart the process after changing the setting. HTTPS
client credentials (`KY_KYSIGNON_SECRET`/`KY_OIDC_SECRET`) are OAuth credentials,
not raw HMAC/cipher keys; keep them separate from the webhook signing secret.

### Compose

The optional `docker-compose.strong-crypto.yml` requires Compose 2.24.4 or later.
It sets the process profile, production environment, secure cookies, public URL
and durable session secret. It replaces the published port with loopback HTTP
for a host TLS proxy, while a container proxy can join the private application
network. Keep that network limited to the intended service/proxy. The overlay
does not alter image pins, source-build selection, data/backup mounts, recovery
keys, DNS overrides or other services.

Set `KY_APP_URL` and your privately prepared `KY_SESSION_SECRET` in the existing
protected environment file. Append `:docker-compose.strong-crypto.yml` to its
current `COMPOSE_FILE` value, preserving every existing overlay. For example,
an existing source-build/LAN-DNS chain becomes:

```dotenv
COMPOSE_FILE=docker-compose.yml:docker-compose.build.yml:docker-compose.lan-dns.yml:docker-compose.strong-crypto.yml
```

Keep `.env` mode 0600. Do not overwrite the file or its secrets with this example,
and do not mix an explicit `-f` list with a saved `COMPOSE_FILE` chain. Inspect the
resolved configuration privately before recreating the service; Compose output
can contain secrets. `python3 scripts/check-strong-crypto-compose.py` checks the
shipped overlay combinations with synthetic values and starts no containers.
Preserve existing issuer/client/webhook configuration in your operator overlays;
this overlay does not blank those fields or configure a new IdP.

## HTTPS edge and additional integrations

Terminate browser HTTPS at a trusted reverse proxy, with TLS 1.2/1.3, an RSA-2048+
or P-256+ certificate chain and authenticated certificate verification by clients.
Use a private backend connection, restrict direct access to the backend, and keep
`KY_TRUSTED_PROXIES` limited to the proxy's own address. For a host Nginx/OpenSSL
proxy, the relevant policy is:

```nginx
ssl_protocols TLSv1.2 TLSv1.3;
ssl_ciphers 'DEFAULT@SECLEVEL=2';
```

Configure the certificate/key paths and routing for your host and verify the
actual proxy before rollout. Go's process setting does not configure that proxy.
The scaffold itself serves HTTP internally and does not load a TLS private key.

The validated local-data configuration uses SQLite. PostgreSQL remains supported;
for a remote PostgreSQL server use verified TLS (`sslmode=verify-full` with the
correct CA/hostname), strong certificates and the process profile. Backend race
tests use a disposable local database; they do not certify a live PostgreSQL TLS
deployment. PostgreSQL backups remain the operator's protected `pg_dump` workflow,
as documented in the README, rather than sealed SQLite snapshots.

This scaffold has no SMTP client or inbound SAML assertion route. SAML metadata
does not add an assertion verifier. Generic OIDC is a library helper; the exposed
login routes are the KySignOn adapter. Derived products must inventory any added
TLS clients, imported keys, SAML/mail/SSH code or custom transports themselves.

## Evidence and rollout

Tests demonstrate RSA-1024/2047 ID-token refusal in both OIDC adapters, RSA-2048
acceptance, retained JWKS caching and nonce checks; matching the verifier's
case-sensitive JWK members, including differently cased members before and
after the canonical fields; signing-secret boundaries at
15/16 bytes; weak directory-sync refusal before user creation; bounded/malformed
JWKS and redirect refusal; and configured-mode refusal of plaintext OIDC
metadata, discovery redirects and a trusted RSA-1024 TLS peer. Full race suites
run in ordinary and configured modes against SQLite and PostgreSQL in CI.
Normal CLI/auth/recovery smoke, browser, fuzz and build checks remain required.

Check the product's authentication and a known restore before enabling the
profile in an existing installation. Replace incompatible endpoint keys and
short shared signing secrets first. Retain the encryption key and custodian
pin; changing them is a separate recovery/migration operation. No live instance
is changed merely by merging these source/configuration files.

Primary reference: [Go FIPS runtime mode and platform limits](https://go.dev/doc/security/fips140).
This uses a runtime control and does not claim that all application algorithms
or a deployed product are FIPS validated.
