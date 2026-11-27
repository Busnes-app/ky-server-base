package sso_test

import (
	"context"
	"crypto/mldsa"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/sso"
	"github.com/coreos/go-oidc/v3/oidc"
)

func signMLDSA65JWT(t *testing.T, priv *mldsa.PrivateKey, header map[string]any, claims map[string]any) string {
	t.Helper()
	hRaw, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	cRaw, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(hRaw) + "." + base64.RawURLEncoding.EncodeToString(cRaw)
	sig, err := priv.Sign(rand.Reader, []byte(signingInput), nil)
	if err != nil {
		t.Fatalf("mldsa sign: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestOIDCVerifiesMLDSA65IDAndLogoutTokens(t *testing.T) {
	mldsaKey, err := mldsa.GenerateKey(mldsa.MLDSA65())
	if err != nil {
		t.Fatalf("mldsa.GenerateKey: %v", err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}

	var issuer, rawIDToken string
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                                issuer,
				"authorization_endpoint":                issuer + "/authorize",
				"token_endpoint":                        issuer + "/token",
				"jwks_uri":                              issuer + "/keys",
				"id_token_signing_alg_values_supported": []string{"ML-DSA-65", "RS256"},
			})
		case "/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"keys": []any{
					map[string]any{
						"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "rsa-1",
						"n": base64.RawURLEncoding.EncodeToString(rsaKey.N.Bytes()), "e": "AQAB",
					},
					map[string]any{
						"kty": "AKP", "use": "sig", "alg": "ML-DSA-65", "kid": "mldsa65-1",
						"pub": base64.RawURLEncoding.EncodeToString(mldsaKey.PublicKey().Bytes()),
					},
				},
			})
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "synthetic",
				"token_type":   "Bearer",
				"id_token":     rawIDToken,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL

	now := time.Now().UTC()
	rawIDToken = signMLDSA65JWT(t, mldsaKey,
		map[string]any{"alg": "ML-DSA-65", "typ": "JWT", "kid": "mldsa65-1"},
		map[string]any{
			"iss":                issuer,
			"aud":                "client",
			"sub":                "pq-user-1",
			"sid":                "pq-sess-1",
			"email":              "pq@example.com",
			"preferred_username": "pqalice",
			"nonce":              "nonce-pq",
			"exp":                now.Add(time.Hour).Unix(),
			"iat":                now.Unix(),
		},
	)

	ctx := oidc.ClientContext(context.Background(), idp.Client())
	cfg := config.SSOConfig{
		KySignOnIssuer:      issuer,
		KySignOnClientID:    "client",
		GenericOIDCIssuer:   issuer,
		GenericOIDCClientID: "client",
	}

	kyClient := sso.NewKySignOnClient(cfg, nil)
	claims, err := kyClient.ExchangeCode(ctx, "code", "verifier", "https://app.example/cb", "nonce-pq")
	if err != nil {
		t.Fatalf("KySignOnClient.ExchangeCode(ML-DSA-65): %v", err)
	}
	if claims.Subject != "pq-user-1" || claims.SessionID != "pq-sess-1" || claims.Email != "pq@example.com" {
		t.Fatalf("unexpected ML-DSA-65 claims: %+v", claims)
	}

	logoutToken := signMLDSA65JWT(t, mldsaKey,
		map[string]any{"alg": "ML-DSA-65", "typ": "logout+jwt", "kid": "mldsa65-1"},
		map[string]any{
			"iss":    issuer,
			"aud":    "client",
			"sub":    "pq-user-1",
			"sid":    "pq-sess-1",
			"jti":    "logout-pq-1",
			"iat":    now.Unix(),
			"exp":    now.Add(5 * time.Minute).Unix(),
			"events": map[string]any{"http://schemas.openid.net/event/backchannel-logout": map[string]any{}},
		},
	)
	logoutClaims, err := kyClient.VerifyLogoutToken(ctx, logoutToken)
	if err != nil {
		t.Fatalf("KySignOnClient.VerifyLogoutToken(ML-DSA-65): %v", err)
	}
	if logoutClaims.Subject != "pq-user-1" || logoutClaims.SessionID != "pq-sess-1" {
		t.Fatalf("unexpected logout claims: %+v", logoutClaims)
	}

	// Generic OIDC client also verifies ML-DSA-65 when advertised by discovery.
	genClient := sso.NewGenericOIDCClient(cfg, nil)
	genClaims, err := genClient.ExchangeCode(ctx, "code", "verifier", "https://app.example/cb", "nonce-pq")
	if err != nil || genClaims.Subject != "pq-user-1" {
		t.Fatalf("GenericOIDCClient.ExchangeCode(ML-DSA-65): claims=%+v err=%v", genClaims, err)
	}

	// Tampering with an ML-DSA-65 signature must be rejected.
	tampered := rawIDToken[:len(rawIDToken)-2] + "AA"
	if tampered == rawIDToken {
		tampered = rawIDToken[:len(rawIDToken)-2] + "BB"
	}
	rawIDToken = tampered
	if _, err := kyClient.ExchangeCode(ctx, "code", "verifier", "https://app.example/cb", "nonce-pq"); !errors.Is(err, sso.ErrInvalidIDToken) {
		t.Fatalf("expected ErrInvalidIDToken for tampered ML-DSA-65 signature, got %v", err)
	}
}

func TestOIDCRejectsMalformedAKPKeysAndUnadvertisedMLDSA(t *testing.T) {
	mldsaKey, err := mldsa.GenerateKey(mldsa.MLDSA65())
	if err != nil {
		t.Fatalf("mldsa.GenerateKey: %v", err)
	}
	mldsa44Key, err := mldsa.GenerateKey(mldsa.MLDSA44())
	if err != nil {
		t.Fatalf("mldsa.GenerateKey(44): %v", err)
	}

	cases := []struct {
		name      string
		supported []string
		jwk       map[string]any
	}{
		{
			name:      "wrong AKP alg ML-DSA-44",
			supported: []string{"ML-DSA-65", "RS256"},
			jwk: map[string]any{
				"kty": "AKP", "use": "sig", "alg": "ML-DSA-44", "kid": "mldsa65-1",
				"pub": base64.RawURLEncoding.EncodeToString(mldsa44Key.PublicKey().Bytes()),
			},
		},
		{
			name:      "truncated AKP pub",
			supported: []string{"ML-DSA-65", "RS256"},
			jwk: map[string]any{
				"kty": "AKP", "use": "sig", "alg": "ML-DSA-65", "kid": "mldsa65-1",
				"pub": base64.RawURLEncoding.EncodeToString([]byte("short")),
			},
		},
		{
			name:      "AKP with RSA n polyglot",
			supported: []string{"ML-DSA-65", "RS256"},
			jwk: map[string]any{
				"kty": "AKP", "use": "sig", "alg": "ML-DSA-65", "kid": "mldsa65-1",
				"pub": base64.RawURLEncoding.EncodeToString(mldsaKey.PublicKey().Bytes()),
				"n":   "AQAB",
			},
		},
		{
			name:      "AKP with use enc",
			supported: []string{"ML-DSA-65", "RS256"},
			jwk: map[string]any{
				"kty": "AKP", "use": "enc", "alg": "ML-DSA-65", "kid": "mldsa65-1",
				"pub": base64.RawURLEncoding.EncodeToString(mldsaKey.PublicKey().Bytes()),
			},
		},
		{
			name:      "ML-DSA-65 token when discovery only advertises RS256",
			supported: []string{"RS256"},
			jwk: map[string]any{
				"kty": "AKP", "use": "sig", "alg": "ML-DSA-65", "kid": "mldsa65-1",
				"pub": base64.RawURLEncoding.EncodeToString(mldsaKey.PublicKey().Bytes()),
			},
		},
		{
			name:      "symmetric-only discovery refused",
			supported: []string{"HS256"},
			jwk: map[string]any{
				"kty": "AKP", "use": "sig", "alg": "ML-DSA-65", "kid": "mldsa65-1",
				"pub": base64.RawURLEncoding.EncodeToString(mldsaKey.PublicKey().Bytes()),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var issuer, rawIDToken string
			idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/.well-known/openid-configuration":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"issuer":                                issuer,
						"authorization_endpoint":                issuer + "/authorize",
						"token_endpoint":                        issuer + "/token",
						"jwks_uri":                              issuer + "/keys",
						"id_token_signing_alg_values_supported": tc.supported,
					})
				case "/keys":
					_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{tc.jwk}})
				case "/token":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"access_token": "synthetic",
						"token_type":   "Bearer",
						"id_token":     rawIDToken,
					})
				default:
					http.NotFound(w, r)
				}
			}))
			defer idp.Close()
			issuer = idp.URL
			now := time.Now().UTC()
			rawIDToken = signMLDSA65JWT(t, mldsaKey,
				map[string]any{"alg": "ML-DSA-65", "typ": "JWT", "kid": "mldsa65-1"},
				map[string]any{
					"iss": issuer, "aud": "client", "sub": "pq-user-1", "nonce": "nonce-pq",
					"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
				},
			)
			ctx := oidc.ClientContext(context.Background(), idp.Client())
			client := sso.NewKySignOnClient(config.SSOConfig{KySignOnIssuer: issuer, KySignOnClientID: "client"}, nil)
			if claims, err := client.ExchangeCode(ctx, "code", "verifier", "https://app.example/cb", "nonce-pq"); err == nil || claims != nil {
				t.Fatalf("expected failure for %s, got claims=%+v err=%v", tc.name, claims, err)
			}
		})
	}
}
