package sso_test

import (
	"context"
	"crypto"
	"crypto/fips140"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/config"
	appcrypto "github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/sso"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
	"github.com/coreos/go-oidc/v3/oidc"
)

func TestOIDCRejectsUndersizedSigningKeys(t *testing.T) {
	for _, bits := range []int{1024, 2047, 2048} {
		key, err := rsa.GenerateKey(rand.Reader, bits)
		if err != nil {
			t.Fatal(err)
		}
		for _, provider := range []string{"kysignon", "generic"} {
			t.Run(fmt.Sprintf("%s/RSA%d", provider, bits), func(t *testing.T) {
				var issuer string
				var rawIDToken string
				var fetches atomic.Int32
				idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/.well-known/openid-configuration":
						_ = json.NewEncoder(w).Encode(map[string]any{
							"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
							"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys",
							"id_token_signing_alg_values_supported": []string{"RS256"},
						})
					case "/keys":
						fetches.Add(1)
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
				defer idp.Close()
				issuer = idp.URL
				rawIDToken = signedIDToken(t, key, issuer)
				ctx := oidc.ClientContext(context.Background(), idp.Client())
				var exchange func(context.Context, string, string, string, string) (*sso.IdentityClaims, error)
				if provider == "kysignon" {
					client := sso.NewKySignOnClient(config.SSOConfig{KySignOnIssuer: issuer, KySignOnClientID: "client"}, nil)
					exchange = client.ExchangeCode
				} else {
					client := sso.NewGenericOIDCClient(config.SSOConfig{GenericOIDCIssuer: issuer, GenericOIDCClientID: "client"}, nil)
					exchange = client.ExchangeCode
				}
				for i := 0; i < 2; i++ {
					claims, err := exchange(ctx, "code", "verifier", "https://app.example/callback", "nonce")
					if bits < 2048 {
						if !errors.Is(err, sso.ErrInvalidIDToken) || claims != nil {
							t.Fatalf("accepted RSA-%d ID token: claims=%+v error=%v", bits, claims, err)
						}
						return
					}
					if err != nil || claims.Subject != "subject" {
						t.Fatalf("strong ID token: claims=%+v error=%v", claims, err)
					}
				}
				if fetches.Load() != 1 {
					t.Fatalf("JWKS cache was not retained: got %d fetches", fetches.Load())
				}
				if claims, err := exchange(ctx, "code", "verifier", "https://app.example/callback", "wrong-nonce"); !errors.Is(err, sso.ErrInvalidIDToken) || claims != nil {
					t.Fatal("accepted wrong nonce")
				}
			})
		}
	}
}

func signedIDToken(t *testing.T, key *rsa.PrivateKey, issuer string) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := json.Marshal(map[string]any{
		"iss": issuer, "aud": "client", "sub": "subject", "nonce": "nonce",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func TestDirectorySyncRefusesShortHMACKey(t *testing.T) {
	st, err := store.Open(context.Background(), testdb.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	secret := strings.Repeat("x", 15)
	client := sso.NewKySignOnClient(config.SSOConfig{KySignOnHMACSecret: secret}, st)
	body, err := json.Marshal(sso.KySignOnSyncPayload{
		Event: "user.created", ID: "short-key-subject", Username: "weak-key-user",
		Email: "weak@example.invalid", Status: "active", Role: "user", Timestamp: time.Now().Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.HandleSyncWebhook(context.Background(), body, appcrypto.ComputeHMACSHA256(body, secret)); err == nil || !strings.Contains(err.Error(), "16") {
		t.Fatalf("short HMAC result: %v", err)
	}
	if _, err := st.Users().GetUserBySSO(context.Background(), "kysignon", "short-key-subject"); err == nil {
		t.Fatal("short-key webhook created a user")
	}
}

func TestOIDCStrongModeRequiresHTTPSMetadata(t *testing.T) {
	for _, field := range []string{"issuer", "authorization_endpoint", "token_endpoint", "jwks_uri", "discovery redirect"} {
		t.Run(field, func(t *testing.T) {
			var issuer string
			var requests atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if field == "discovery redirect" && r.URL.Path != "/discovery" {
					http.Redirect(w, r, "/discovery", http.StatusFound)
					return
				}
				metadata := map[string]any{
					"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
					"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys",
				}
				if field != "issuer" && field != "discovery redirect" {
					metadata[field] = "http://id.example.invalid/endpoint"
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(metadata)
			})
			var idp *httptest.Server
			if field == "issuer" {
				idp = httptest.NewServer(handler)
			} else {
				idp = httptest.NewTLSServer(handler)
			}
			defer idp.Close()
			issuer = idp.URL
			ctx := oidc.ClientContext(context.Background(), idp.Client())
			client := sso.NewKySignOnClient(config.SSOConfig{KySignOnIssuer: issuer, KySignOnClientID: "client"}, nil)
			_, err := client.BuildAuthURL(ctx, "https://app.example/callback", "state", "verifier", "nonce")
			if (err != nil) != fips140.Enabled() {
				t.Fatalf("mode=%v, discovery error=%v", fips140.Enabled(), err)
			}
			if field == "issuer" && fips140.Enabled() && requests.Load() != 0 {
				t.Fatal("contacted a plaintext issuer in strong mode")
			}
		})
	}
}

func TestOIDCStrongModeRejectsWeakTLSPeer(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	var requests atomic.Int32
	idp := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
			"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys",
		})
	}))
	idp.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	idp.StartTLS()
	defer idp.Close()
	issuer = idp.URL
	client := sso.NewKySignOnClient(config.SSOConfig{KySignOnIssuer: issuer, KySignOnClientID: "client"}, nil)
	ctx := oidc.ClientContext(context.Background(), idp.Client())
	_, err = client.BuildAuthURL(ctx, "https://app.example/callback", "state", "verifier", "nonce")
	if (err != nil) != fips140.Enabled() {
		t.Fatalf("mode=%v, weak TLS error=%v", fips140.Enabled(), err)
	}
	if fips140.Enabled() && requests.Load() != 0 {
		t.Fatal("sent discovery request to weak TLS peer")
	}
}
