package sso_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Busnes-app/ky-primitives/pqtls"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/sso"
	"github.com/coreos/go-oidc/v3/oidc"
)

type recordingRoundTripper struct {
	base   http.RoundTripper
	mu     sync.Mutex
	states []tls.ConnectionState
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.base.RoundTrip(req)
	if err == nil && resp.TLS != nil {
		r.mu.Lock()
		r.states = append(r.states, *resp.TLS)
		r.mu.Unlock()
	}
	return resp, err
}

func TestOIDCOutboundPQTLS(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	for _, provider := range []string{"kysignon", "generic"} {
		t.Run(provider, func(t *testing.T) {
			var issuer string
			var rawIDToken string
			var mu sync.Mutex
			var serverStates []tls.ConnectionState
			seenPaths := map[string]bool{}

			idp := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.TLS != nil {
					mu.Lock()
					serverStates = append(serverStates, *r.TLS)
					seenPaths[r.URL.Path] = true
					mu.Unlock()
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/.well-known/openid-configuration":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"issuer":                                issuer,
						"authorization_endpoint":                issuer + "/authorize",
						"token_endpoint":                        issuer + "/token",
						"jwks_uri":                              issuer + "/keys",
						"id_token_signing_alg_values_supported": []string{"RS256"},
					})
				case "/keys":
					_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
						"kty": "RSA", "alg": "RS256", "kid": "fixture",
						"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB",
					}}})
				case "/token":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"access_token": "synthetic", "token_type": "Bearer",
						"id_token": rawIDToken,
					})
				default:
					http.NotFound(w, r)
				}
			}))
			// Require TLS 1.3 on the loopback IdP without restricting CurvePreferences so
			// negotiation reflects the outbound client's default key-exchange preference.
			idp.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
			idp.StartTLS()
			defer idp.Close()

			issuer = idp.URL
			rawIDToken = signedIDToken(t, key, issuer)

			// Clone http.DefaultTransport so the test exercises the repo's default outbound
			// TLS configuration, adding only the loopback server's certificate as a root.
			baseTransport, ok := http.DefaultTransport.(*http.Transport)
			if !ok {
				t.Fatalf("http.DefaultTransport is %T, want *http.Transport", http.DefaultTransport)
			}
			transport := baseTransport.Clone()
			roots := x509.NewCertPool()
			roots.AddCert(idp.Certificate())
			if transport.TLSClientConfig == nil {
				transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
			} else {
				transport.TLSClientConfig.RootCAs = roots
			}
			// Force a fresh TLS handshake on discovery, token exchange, and JWKS fetch.
			transport.DisableKeepAlives = true

			recorder := &recordingRoundTripper{base: transport}
			ctx := oidc.ClientContext(context.Background(), &http.Client{Transport: recorder})

			var claims *sso.IdentityClaims
			if provider == "kysignon" {
				client := sso.NewKySignOnClient(config.SSOConfig{KySignOnIssuer: issuer, KySignOnClientID: "client"}, nil)
				claims, err = client.ExchangeCode(ctx, "code", "verifier", "https://app.example/callback", "nonce")
			} else {
				client := sso.NewGenericOIDCClient(config.SSOConfig{GenericOIDCIssuer: issuer, GenericOIDCClientID: "client"}, nil)
				claims, err = client.ExchangeCode(ctx, "code", "verifier", "https://app.example/callback", "nonce")
			}
			if err != nil || claims == nil || claims.Subject != "subject" {
				t.Fatalf("OIDC exchange failed: claims=%+v err=%v", claims, err)
			}

			mu.Lock()
			gotServerStates := append([]tls.ConnectionState(nil), serverStates...)
			for _, p := range []string{"/.well-known/openid-configuration", "/token", "/keys"} {
				if !seenPaths[p] {
					t.Errorf("expected outbound OIDC request to %s", p)
				}
			}
			mu.Unlock()

			recorder.mu.Lock()
			gotClientStates := append([]tls.ConnectionState(nil), recorder.states...)
			recorder.mu.Unlock()

			if len(gotServerStates) < 3 {
				t.Fatalf("expected at least 3 server TLS handshakes, got %d", len(gotServerStates))
			}
			for _, state := range gotServerStates {
				pqtls.RequireHybrid(t, state)
			}
			if len(gotClientStates) < 3 {
				t.Fatalf("expected at least 3 client TLS handshakes, got %d", len(gotClientStates))
			}
			for _, state := range gotClientStates {
				pqtls.RequireHybrid(t, state)
			}
		})
	}
}
