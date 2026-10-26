package sso_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/sso"
	"github.com/coreos/go-oidc/v3/oidc"
)

// Check the key used by the real signature verifier, including JWK member
// casing and order. Transport rejection alone cannot prove that alignment.
func TestOIDCJWKMemberParsingMatchesVerifier(t *testing.T) {
	for _, bits := range []int{1024, 2047, 2048} {
		key, err := rsa.GenerateKey(rand.Reader, bits)
		if err != nil {
			t.Fatal(err)
		}
		for _, variant := range []struct{ name, before, after, envelope string }{
			{name: "canonical", envelope: "keys"},
			{name: "uppercase type", after: `,"KTY":"EC"`, envelope: "keys"},
			{name: "mixed-case type", after: `,"Kty":"EC"`, envelope: "keys"},
			{name: "uppercase modulus", after: `,"N":"` + base64.RawURLEncoding.EncodeToString(new(big.Int).Lsh(big.NewInt(1), 2047).Bytes()) + `"`, envelope: "keys"},
			{name: "uppercase short modulus", after: `,"N":"AQ"`, envelope: "keys"},
			{name: "aliases before canonical", before: `"KTY":"EC","N":"AQ",`, envelope: "keys"},
			{name: "type and modulus aliases", after: `,"KTY":"EC","N":"AQ"`, envelope: "keys"},
			{name: "uppercase envelope", after: `,"KTY":"EC"`, envelope: "KEYS"},
		} {
			for _, provider := range []string{"kysignon", "generic"} {
				t.Run(fmt.Sprintf("%s/RSA%d/%s", provider, bits, variant.name), func(t *testing.T) {
					var issuer, token string
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
							_, _ = fmt.Fprintf(w, `{"%s":[{%s"kty":"RSA","kid":"fixture","alg":"RS256","e":"AQAB","n":"%s"%s}]}`,
								variant.envelope, variant.before, base64.RawURLEncoding.EncodeToString(key.N.Bytes()), variant.after)
						case "/token":
							_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "fixture", "token_type": "Bearer", "id_token": token})
						default:
							http.NotFound(w, r)
						}
					}))
					defer idp.Close()
					issuer = idp.URL
					token = signedIDToken(t, key, issuer)
					ctx := oidc.ClientContext(context.Background(), idp.Client())
					cfg := config.SSOConfig{KySignOnIssuer: issuer, KySignOnClientID: "client", GenericOIDCIssuer: issuer, GenericOIDCClientID: "client"}
					exchange := sso.NewKySignOnClient(cfg, nil).ExchangeCode
					if provider == "generic" {
						exchange = sso.NewGenericOIDCClient(cfg, nil).ExchangeCode
					}
					claims, err := exchange(ctx, "code", "verifier", "https://app.example/callback", "nonce")
					if bits < 2048 {
						if !errors.Is(err, sso.ErrInvalidIDToken) || claims != nil {
							t.Fatalf("accepted RSA-%d token: claims=%+v err=%v", bits, claims, err)
						}
					} else if err != nil || claims.Subject != "subject" {
						t.Fatalf("strong token: claims=%+v err=%v", claims, err)
					}
				})
			}
		}
	}
}
