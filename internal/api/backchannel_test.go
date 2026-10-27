package api_test

import (
	"context"
	stdcrypto "crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/auth"
)

// fakeKySignOn is a TLS identity provider whose token endpoint returns the next ID token.
type fakeKySignOn struct {
	key     *rsa.PrivateKey
	issuer  string
	idToken string
}

func newFakeKySignOn(t *testing.T) *fakeKySignOn {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeKySignOn{key: key}
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": f.issuer, "authorization_endpoint": f.issuer + "/authorize",
				"token_endpoint": f.issuer + "/token", "jwks_uri": f.issuer + "/keys",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
				"kty": "RSA", "alg": "RS256", "kid": "k",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB",
			}}})
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "a", "token_type": "Bearer", "id_token": f.idToken})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(idp.Close)
	f.issuer = idp.URL
	// The server reaches the provider through the default client; trust the test CA.
	old := http.DefaultTransport
	http.DefaultTransport = idp.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = old })
	return f
}

func (f *fakeKySignOn) sign(t *testing.T, typ string, claims map[string]any) string {
	t.Helper()
	claims["iss"], claims["aud"] = f.issuer, "client"
	claims["exp"] = time.Now().Add(2 * time.Minute).Unix()
	if _, ok := claims["iat"]; !ok {
		claims["iat"] = time.Now().Unix()
	}
	header := map[string]string{"alg": "RS256", "kid": "k"}
	if typ != "" {
		header["typ"] = typ
	}
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, stdcrypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *fakeKySignOn) logoutToken(t *testing.T, sub, sid string) string {
	claims := map[string]any{"sub": sub, "jti": sid + sub, "events": map[string]any{"http://schemas.openid.net/event/backchannel-logout": map[string]any{}}}
	if sid != "" {
		claims["sid"] = sid
	}
	return f.sign(t, "logout+jwt", claims)
}

// ssoLogin runs the KySignOn login and callback for sub/sid and returns the callback response.
func ssoLogin(t *testing.T, srv *api.Server, f *fakeKySignOn, sub, sid string) *httptest.ResponseRecorder {
	return ssoLoginAt(t, srv, f, sub, sid, time.Now())
}

// ssoLoginAt is ssoLogin with an ID token the provider issued at iat.
func ssoLoginAt(t *testing.T, srv *api.Server, f *fakeKySignOn, sub, sid string, iat time.Time) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/sso/kysignon/login", nil))
	loc, err := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusFound || err != nil {
		t.Fatalf("login redirect: %d %s", w.Code, w.Body.String())
	}
	q := loc.Query()
	f.idToken = f.sign(t, "", map[string]any{"sub": sub, "sid": sid, "nonce": q.Get("nonce"), "preferred_username": sub, "iat": iat.Unix()})
	req := httptest.NewRequest(http.MethodGet, "/api/sso/kysignon/callback?code=c&state="+q.Get("state"), nil)
	for _, c := range w.Result().Cookies() {
		req.AddCookie(c)
	}
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func sessionCookie(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			return c
		}
	}
	return nil
}

func signedIn(srv *api.Server, c *http.Cookie) bool {
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.AddCookie(c)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var me struct{ Authenticated bool }
	return json.Unmarshal(w.Body.Bytes(), &me) == nil && me.Authenticated
}

func postLogout(srv *api.Server, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/sso/kysignon/backchannel-logout", strings.NewReader(url.Values{"logout_token": {token}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func TestBackchannelLogoutEndsTheSSOLogin(t *testing.T) {
	srv, st, cfg := setupTestServer(t)
	f := newFakeKySignOn(t)
	cfg.SSO.KySignOnIssuer, cfg.SSO.KySignOnClientID, cfg.SSO.AutoProvision = f.issuer, "client", true
	srv = api.NewServer(cfg, st)

	ended := sessionCookie(ssoLogin(t, srv, f, "alice", "sid-a1"))
	kept := sessionCookie(ssoLogin(t, srv, f, "alice", "sid-a2"))
	if ended == nil || kept == nil || !signedIn(srv, ended) || !signedIn(srv, kept) {
		t.Fatal("SSO login did not issue working sessions")
	}

	// An ID token is not a logout token, even when signed by the provider.
	if w := postLogout(srv, f.idToken); w.Code != http.StatusBadRequest {
		t.Fatalf("ID token as logout token: got %d", w.Code)
	}
	w := postLogout(srv, f.logoutToken(t, "alice", "sid-a1"))
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("logout: got %d %q %s", w.Code, w.Header().Get("Cache-Control"), w.Body.String())
	}
	if signedIn(srv, ended) {
		t.Fatal("session survived its back-channel logout")
	}
	if !signedIn(srv, kept) {
		t.Fatal("logout of one login ended another login of the same user")
	}
	// A replay is harmless and still acknowledged.
	if w := postLogout(srv, f.logoutToken(t, "alice", "sid-a1")); w.Code != http.StatusOK {
		t.Fatalf("replayed logout: got %d", w.Code)
	}

	// A logout that lands before the callback finishes fences that login.
	if w := postLogout(srv, f.logoutToken(t, "alice", "sid-late")); w.Code != http.StatusOK {
		t.Fatalf("early logout: got %d", w.Code)
	}
	if w := ssoLogin(t, srv, f, "alice", "sid-late"); w.Code != http.StatusUnauthorized || sessionCookie(w) != nil {
		t.Fatalf("callback revived an ended login: %d", w.Code)
	}

	// A subject-wide token ends the user's logins issued by then, and nobody else's.
	bob := sessionCookie(ssoLogin(t, srv, f, "bob", "sid-b1"))
	if w := postLogout(srv, f.logoutToken(t, "alice", "")); w.Code != http.StatusOK {
		t.Fatalf("subject-wide logout: got %d", w.Code)
	}
	if signedIn(srv, kept) || !signedIn(srv, bob) {
		t.Fatal("subject-wide logout ended the wrong sessions")
	}
	if w := postLogout(srv, f.logoutToken(t, "nobody", "")); w.Code != http.StatusOK {
		t.Fatalf("logout for an unknown subject: got %d", w.Code)
	}

	// A subject-wide logout fences a callback already holding an ID token issued before
	// it, even for a subject not yet provisioned here; a later login still works.
	carolLogout := f.sign(t, "logout+jwt", map[string]any{"sub": "carol", "jti": "carol", "iat": time.Now().Add(-30 * time.Second).Unix(),
		"events": map[string]any{"http://schemas.openid.net/event/backchannel-logout": map[string]any{}}})
	if w := postLogout(srv, carolLogout); w.Code != http.StatusOK {
		t.Fatalf("subject-wide logout of an unprovisioned subject: got %d", w.Code)
	}
	if w := ssoLoginAt(t, srv, f, "carol", "sid-c1", time.Now().Add(-time.Minute)); w.Code != http.StatusUnauthorized || sessionCookie(w) != nil {
		t.Fatalf("pending callback survived a subject-wide logout: %d", w.Code)
	}
	if w := ssoLoginAt(t, srv, f, "carol", "sid-c2", time.Now().Add(5*time.Second)); w.Code != http.StatusFound || !signedIn(srv, sessionCookie(w)) {
		t.Fatalf("login issued after the subject-wide logout was refused: %d", w.Code)
	}

	var audits int
	recs, _, err := st.Audit().ListAuditRecords(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Action == "auth.sso_backchannel_logout" {
			audits++
		}
	}
	if audits == 0 {
		t.Fatal("back-channel logout was not audited")
	}
}

// A paired device inherits its parent's provider login: a logout of that login refuses a
// pairing still pending and ends a device already paired from it.
func TestBackchannelLogoutReachesPairedDevices(t *testing.T) {
	srv, st, cfg := setupTestServer(t)
	f := newFakeKySignOn(t)
	cfg.SSO.KySignOnIssuer, cfg.SSO.KySignOnClientID, cfg.SSO.AutoProvision = f.issuer, "client", true
	srv = api.NewServer(cfg, st)

	parent := sessionCookie(ssoLogin(t, srv, f, "erin", "sid-e1"))
	pairSecret := func() string {
		t.Helper()
		w := do(t, srv, "POST", "/api/devices/pair/init", parent)
		var res struct{ Secret string }
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || res.Secret == "" {
			t.Fatalf("pair init: %d %s", w.Code, w.Body.String())
		}
		return res.Secret
	}
	redeem := func(secret string) string {
		t.Helper()
		w := pairVerify(t, srv, map[string]string{"secret": secret, "platform": "pwa"})
		var res struct {
			Token string `json:"session_token"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		return res.Token
	}

	paired := redeem(pairSecret())
	pending := pairSecret()
	if paired == "" || !signedIn(srv, &http.Cookie{Name: auth.SessionCookieName, Value: paired}) {
		t.Fatal("pairing from an SSO session did not sign the device in")
	}
	if w := postLogout(srv, f.logoutToken(t, "erin", "sid-e1")); w.Code != http.StatusOK {
		t.Fatalf("logout: got %d", w.Code)
	}
	if signedIn(srv, &http.Cookie{Name: auth.SessionCookieName, Value: paired}) {
		t.Fatal("a device paired from the ended login stayed signed in")
	}
	if token := redeem(pending); token != "" {
		t.Fatal("a pairing started from the ended login still minted a session")
	}
}
