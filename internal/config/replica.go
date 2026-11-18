package config

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
)

// ReplicaConfigSetting is the server_settings row holding the shared fingerprint. It is never
// returned by the API, logged, or carried over by a restore.
const ReplicaConfigSetting = "replica_config_fingerprint"

// EncryptionKeyField names the encryption key's check value in a replica fingerprint. Every
// other value is keyed by that key, so a differing key is reported alone.
const EncryptionKeyField = "KY_ENCRYPTION_KEY"

// sharedFields lists every setting all PostgreSQL replicas must agree on, as environment name
// and canonical value. Per-host settings (KY_TRUSTED_PROXIES, KY_BACKUP_DIR/KEEP, host, port,
// pool sizes) are deliberately absent; __Host- cookie mode follows from KY_APP_URL. A product
// adds its own shared settings here.
func sharedFields(c *Config) [][2]string {
	b := strconv.FormatBool
	scimToken := ""
	if c.SCIM.Enabled { // a disabled SCIM token is minted per process and means nothing
		scimToken = c.SCIM.BearerToken
	}
	return [][2]string{
		{"KY_APP_NAME", c.Server.AppName},
		{"KY_APP_URL", c.Server.AppURL},
		{"KY_ENV", c.Server.Environment},
		{"KY_SESSION_SECRET", c.Security.SessionSecret},
		{"KY_SCIM_ENABLED", b(c.SCIM.Enabled)},
		{"KY_SCIM_TOKEN", scimToken},
		{"KY_SCIM_ALLOW_ADMIN", b(c.SCIM.AllowAdmin)},
		{"KY_SSO_ENABLED", b(c.SSO.Enabled)},
		{"KY_KYSIGNON_ISSUER", c.SSO.KySignOnIssuer},
		{"KY_KYSIGNON_CLIENT_ID", c.SSO.KySignOnClientID},
		{"KY_KYSIGNON_SECRET", c.SSO.KySignOnSecret},
		{"KY_OIDC_ISSUER", c.SSO.GenericOIDCIssuer},
		{"KY_OIDC_CLIENT_ID", c.SSO.GenericOIDCClientID},
		{"KY_OIDC_SECRET", c.SSO.GenericOIDCSecret},
		{"KY_SAML_ENTITY_ID", c.SSO.SAMLEntityID},
		{"KY_SAML_METADATA_URL", c.SSO.SAMLMetadataURL},
		{"KY_SSO_AUTO_PROVISION", b(c.SSO.AutoProvision)},
		{"KY_CAPTCHA_PROVIDER", c.Captcha.Provider},
		{"KY_CAPTCHA_SITE_KEY", c.Captcha.SiteKey},
		{"KY_CAPTCHA_SECRET_KEY", c.Captcha.SecretKey},
		{"KY_CAPTCHA_POW_DIFFICULTY", strconv.Itoa(c.Captcha.DifficultyPoW)},
		{"KY_COOKIE_SECURE", b(c.Security.CookieSecure)},
		{"KY_COOKIE_DOMAIN", c.Security.CookieDomain},
		{"KY_RATE_LIMIT_EXEMPT_NETWORKS", c.Security.RateLimitExemptNetworks},
		{"KY_BACKUP_DEPOSIT_INTERVAL", c.Backup.DepositInterval.String()},
		{"KY_BACKUP_ALLOW_PRIVATE_RECOVERY", b(c.Backup.AllowPrivateRecovery)},
	}
}

// ReplicaFingerprint maps each shared field name to HMAC-SHA256 under the encryption key, so
// someone reading the database cannot brute-force low-entropy values such as URLs.
func ReplicaFingerprint(c *Config) map[string]string {
	sum := func(label, value string) string {
		m := hmac.New(sha256.New, c.Security.EncryptionKey)
		m.Write([]byte("ky_server_base:replica-config:" + label + "\x00" + value))
		return hex.EncodeToString(m.Sum(nil))
	}
	out := map[string]string{EncryptionKeyField: sum(EncryptionKeyField, "")}
	for _, f := range sharedFields(c) {
		out[f[0]] = sum(f[0], f[1])
	}
	return out
}

// DiffFingerprint names the fields whose fingerprints differ, sorted, never their values. A
// differing encryption key is reported alone: every other comparison depends on it. Names
// only one side knows are not differences: an upgrade that adds a field records it on first
// start (MergeFingerprint), and a removed field no longer matters.
func DiffFingerprint(stored, current map[string]string) []string {
	if !hmac.Equal([]byte(stored[EncryptionKeyField]), []byte(current[EncryptionKeyField])) {
		return []string{EncryptionKeyField}
	}
	var diff []string
	for name, v := range current {
		if old, ok := stored[name]; ok && !hmac.Equal([]byte(old), []byte(v)) {
			diff = append(diff, name)
		}
	}
	slices.Sort(diff)
	return diff
}

// MergeFingerprint adds the fields stored lacks and reports whether it added any.
func MergeFingerprint(stored, current map[string]string) bool {
	added := false
	for name, v := range current {
		if _, ok := stored[name]; !ok {
			stored[name] = v
			added = true
		}
	}
	return added
}
