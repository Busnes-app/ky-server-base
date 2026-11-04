package sso_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/sso"
	"github.com/coreos/go-oidc/v3/oidc"
)

func TestOAuthAuthorizationURLUsesDiscoveryAndPKCE(t *testing.T) {
	var issuer string
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
			"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys",
		})
	}))
	defer idp.Close()
	issuer = idp.URL

	client := sso.NewKySignOnClient(config.SSOConfig{KySignOnIssuer: issuer, KySignOnClientID: "client"}, nil)
	ctx := oidc.ClientContext(context.Background(), idp.Client())
	authURL, err := client.BuildAuthURL(ctx, "https://app.example/callback", "state", "verifier", "nonce")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if parsed.Path != "/authorize" || query.Get("state") != "state" || query.Get("nonce") != "nonce" || query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" {
		t.Fatalf("unexpected authorization URL: %s", authURL)
	}
}

func TestSAMLServiceProvider(t *testing.T) {
	sp := sso.NewSAMLServiceProvider("https://app.busnes.app/saml/metadata", "https://app.busnes.app/saml/acs")

	metadata := sp.GenerateMetadata()
	if len(metadata) == 0 || !testing.Verbose() && len(metadata) < 50 {
		if len(metadata) == 0 {
			t.Errorf("expected non-empty metadata")
		}
	}

}
