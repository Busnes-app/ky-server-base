package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

// replicaConfig is a PostgreSQL replica configuration on an isolated schema, or a skip on
// SQLite. Each call returns a fresh copy of the same settings.
func replicaConfig(t *testing.T) func() *config.Config {
	t.Helper()
	db := testdb.Config(t)
	if db.Driver != "postgres" {
		t.Skip("replica configuration checks need KY_TEST_POSTGRES_DSN")
	}
	return func() *config.Config {
		t.Setenv("KY_DATA_DIR", t.TempDir())
		t.Setenv("KY_DB_DRIVER", "postgres")
		t.Setenv("KY_DB_DSN", db.DSN)
		t.Setenv("KY_SESSION_SECRET", "replica-session-secret-0123456789")
		t.Setenv("KY_SCIM_TOKEN", "replica-scim-token-0123456789")
		t.Setenv("KY_ENCRYPTION_KEY", strings.Repeat("ab", 32))
		cfg, err := config.LoadFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
}

func openStore(t *testing.T, cfg *config.Config) store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// captureLog collects everything the standard logger writes while the test runs.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// Each shared field gets a value no replica had; the refusal names that field and nothing
// it holds. A field added to the fingerprint without a case here fails the test.
func TestReplicaWithADifferentSharedFieldIsRefused(t *testing.T) {
	newCfg := replicaConfig(t)
	first := newCfg()
	if err := bootstrap(context.Background(), first, openStore(t, first)); err != nil {
		t.Fatal(err)
	}
	const secret = "s3cr3t-replica-value-0123456789"
	mutate := map[string]func(*config.Config){
		"KY_APP_NAME":                      func(c *config.Config) { c.Server.AppName = secret },
		"KY_APP_URL":                       func(c *config.Config) { c.Server.AppURL = "https://" + secret },
		"KY_ENV":                           func(c *config.Config) { c.Server.Environment = secret },
		"KY_SESSION_SECRET":                func(c *config.Config) { c.Security.SessionSecret = secret },
		"KY_SCIM_ENABLED":                  func(c *config.Config) { c.SCIM.Enabled = false },
		"KY_SCIM_TOKEN":                    func(c *config.Config) { c.SCIM.BearerToken = secret },
		"KY_SCIM_ALLOW_ADMIN":              func(c *config.Config) { c.SCIM.AllowAdmin = true },
		"KY_SSO_ENABLED":                   func(c *config.Config) { c.SSO.Enabled = false },
		"KY_KYSIGNON_ISSUER":               func(c *config.Config) { c.SSO.KySignOnIssuer = secret },
		"KY_KYSIGNON_CLIENT_ID":            func(c *config.Config) { c.SSO.KySignOnClientID = secret },
		"KY_KYSIGNON_SECRET":               func(c *config.Config) { c.SSO.KySignOnSecret = secret },
		"KY_OIDC_ISSUER":                   func(c *config.Config) { c.SSO.GenericOIDCIssuer = secret },
		"KY_OIDC_CLIENT_ID":                func(c *config.Config) { c.SSO.GenericOIDCClientID = secret },
		"KY_OIDC_SECRET":                   func(c *config.Config) { c.SSO.GenericOIDCSecret = secret },
		"KY_SAML_ENTITY_ID":                func(c *config.Config) { c.SSO.SAMLEntityID = secret },
		"KY_SAML_METADATA_URL":             func(c *config.Config) { c.SSO.SAMLMetadataURL = secret },
		"KY_SSO_AUTO_PROVISION":            func(c *config.Config) { c.SSO.AutoProvision = false },
		"KY_CAPTCHA_PROVIDER":              func(c *config.Config) { c.Captcha.Provider = secret },
		"KY_CAPTCHA_SITE_KEY":              func(c *config.Config) { c.Captcha.SiteKey = secret },
		"KY_CAPTCHA_SECRET_KEY":            func(c *config.Config) { c.Captcha.SecretKey = secret },
		"KY_CAPTCHA_POW_DIFFICULTY":        func(c *config.Config) { c.Captcha.DifficultyPoW = 31337 },
		"KY_COOKIE_SECURE":                 func(c *config.Config) { c.Security.CookieSecure = true },
		"KY_COOKIE_DOMAIN":                 func(c *config.Config) { c.Security.CookieDomain = secret },
		"KY_RATE_LIMIT_EXEMPT_NETWORKS":    func(c *config.Config) { c.Security.RateLimitExemptNetworks = "203.0.113.0/24" },
		"KY_BACKUP_DEPOSIT_INTERVAL":       func(c *config.Config) { c.Backup.DepositInterval = 31 * time.Hour },
		"KY_BACKUP_ALLOW_PRIVATE_RECOVERY": func(c *config.Config) { c.Backup.AllowPrivateRecovery = true },
		config.EncryptionKeyField:          func(c *config.Config) { c.Security.EncryptionKey = bytes.Repeat([]byte{7}, 32) },
	}
	for name := range config.ReplicaFingerprint(first) {
		if mutate[name] == nil {
			t.Errorf("shared field %s has no refusal case", name)
		}
	}
	logs := captureLog(t)
	for name, change := range mutate {
		cfg := newCfg()
		change(cfg)
		err := bootstrap(context.Background(), cfg, openStore(t, cfg))
		if !errors.Is(err, errReplicaConfig) || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: got %v, want a refusal naming it", name, err)
			continue
		}
		for _, leak := range []string{secret, "31337", "203.0.113", "31h"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("%s: refusal carries the value: %v", name, err)
			}
		}
		if name == config.EncryptionKeyField && strings.Contains(err.Error(), "KY_APP_NAME") {
			t.Errorf("a different key must be reported alone: %v", err)
		}
	}
	assertNoFingerprintIn(t, openStore(t, first), logs.String())
}

func assertNoFingerprintIn(t *testing.T, st store.Store, text string) {
	t.Helper()
	raw, err := st.Settings().GetSetting(context.Background(), config.ReplicaConfigSetting)
	if err != nil {
		t.Fatal(err)
	}
	var fp map[string]string
	if err := json.Unmarshal([]byte(raw), &fp); err != nil || len(fp) == 0 {
		t.Fatalf("record %v", err)
	}
	for name, v := range fp {
		if strings.Contains(text, v) {
			t.Fatalf("the %s fingerprint reached the logs", name)
		}
	}
}

func TestReplicaWithTheSameConfigStartsAndPerHostFieldsDiffer(t *testing.T) {
	newCfg := replicaConfig(t)
	first := newCfg()
	if err := bootstrap(context.Background(), first, openStore(t, first)); err != nil {
		t.Fatal(err)
	}
	same := newCfg()
	if err := bootstrap(context.Background(), same, openStore(t, same)); err != nil {
		t.Fatalf("an identical replica was refused: %v", err)
	}
	host := newCfg()
	host.Security.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("192.0.2.7/32")}
	host.Backup.Dir = t.TempDir()
	host.Backup.Keep = 2
	host.Server.Host = "127.0.0.1"
	host.Server.Port = 9999
	host.Database.MaxOpenConns = 9
	host.Database.MaxIdleConns = 1
	if err := bootstrap(context.Background(), host, openStore(t, host)); err != nil {
		t.Fatalf("a per-host difference was refused: %v", err)
	}
}

func TestAcceptReplicaConfigRewritesAndAuditsNamesOnly(t *testing.T) {
	newCfg := replicaConfig(t)
	first := newCfg()
	st := openStore(t, first)
	if err := bootstrap(context.Background(), first, st); err != nil {
		t.Fatal(err)
	}
	const rotated = "rotated-session-secret-0123456789"
	next := newCfg()
	next.Security.SessionSecret = rotated
	if err := bootstrap(context.Background(), next, openStore(t, next)); !errors.Is(err, errReplicaConfig) {
		t.Fatalf("rotation before acceptance: %v", err)
	}
	logs := captureLog(t)
	var out bytes.Buffer
	if err := acceptReplicaConfig(context.Background(), next, openStore(t, next), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "KY_SESSION_SECRET") || strings.Contains(out.String(), rotated) {
		t.Fatalf("accept output: %q", out.String())
	}
	if err := bootstrap(context.Background(), next, openStore(t, next)); err != nil {
		t.Fatalf("rotated replica after acceptance: %v", err)
	}
	if err := bootstrap(context.Background(), first, st); !errors.Is(err, errReplicaConfig) {
		t.Fatalf("an unrotated replica after acceptance: %v", err)
	}
	records, _, err := st.Audit().ListAuditRecords(context.Background(), 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var rows []*store.AuditRecord
	for _, r := range records {
		if r.Action == "admin.replica_config_accepted" {
			rows = append(rows, r)
		}
	}
	if len(rows) != 1 || rows[0].Details != "fields=KY_SESSION_SECRET" {
		t.Fatalf("audit rows %+v, want one naming only KY_SESSION_SECRET", rows)
	}
	assertNoFingerprintIn(t, st, logs.String()+out.String()+rows[0].Details)
}

func TestSQLiteRecordsNoReplicaConfig(t *testing.T) {
	t.Setenv("KY_TEST_POSTGRES_DSN", "")
	t.Setenv("KY_DATA_DIR", t.TempDir())
	t.Setenv("KY_DB_DRIVER", "sqlite")
	cfg, err := config.LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database = testdb.Config(t)
	st := openStore(t, cfg)
	if err := bootstrap(context.Background(), cfg, st); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Settings().GetSetting(context.Background(), config.ReplicaConfigSetting); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SQLite recorded a replica fingerprint: %v", err)
	}
	if err := acceptReplicaConfig(context.Background(), cfg, st, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "PostgreSQL") {
		t.Fatalf("accept-replica-config on SQLite: %v", err)
	}
}

// A replica started with the wrong encryption key is told to fix it, and accepting it is
// refused: the record keeps the key every other replica opens the sealed settings with.
func TestAcceptReplicaConfigRefusesADifferentEncryptionKey(t *testing.T) {
	newCfg := replicaConfig(t)
	first := newCfg()
	st := openStore(t, first)
	if err := bootstrap(context.Background(), first, st); err != nil {
		t.Fatal(err)
	}
	before, err := st.Settings().GetSetting(context.Background(), config.ReplicaConfigSetting)
	if err != nil {
		t.Fatal(err)
	}
	typo := newCfg()
	typo.Security.EncryptionKey = bytes.Repeat([]byte{9}, 32)
	err = bootstrap(context.Background(), typo, openStore(t, typo))
	if !errors.Is(err, errReplicaConfig) || strings.Contains(err.Error(), "run accept-replica-config once") {
		t.Fatalf("wrong key at startup: %v, want a refusal that does not offer accept-replica-config", err)
	}
	var out bytes.Buffer
	if err := acceptReplicaConfig(context.Background(), typo, openStore(t, typo), &out); err == nil || !strings.Contains(err.Error(), config.EncryptionKeyField) {
		t.Fatalf("accept-replica-config with a different key: %v", err)
	}
	after, _ := st.Settings().GetSetting(context.Background(), config.ReplicaConfigSetting)
	if after != before {
		t.Fatal("the record changed")
	}
	records, _, _ := st.Audit().ListAuditRecords(context.Background(), 0, 50)
	for _, r := range records {
		if r.Action == "admin.replica_config_accepted" {
			t.Fatalf("a refused acceptance was audited as accepted: %+v", r)
		}
	}
}
