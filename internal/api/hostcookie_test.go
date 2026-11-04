package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Busnes-app/ky-primitives/password"
	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// On https every cookie this server sets is a __Host- cookie (Secure, Path=/, no Domain), and
// only those are read, so a sibling subdomain that plants a bare ky_session, ky_csrf or SSO
// state cookie on the parent domain cannot sign a victim into the attacker's account.
func TestHTTPSCookiesAreHostCookies(t *testing.T) {
	srv, st, cfg := setupTestServer(t)
	cfg.Security.CookieSecure, cfg.Security.HostCookies = true, true
	srv = api.NewServer(cfg, st)
	hash, _ := password.Hash("SuperSecretPass123!")
	if err := st.Users().CreateUser(context.Background(), &store.User{ID: "usr_h", Username: "hana", PasswordHash: hash, Role: "user", Status: "active", SSOProvider: "local"}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"username": "hana", "password": "SuperSecretPass123!"})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	got := map[string]*http.Cookie{}
	for _, c := range w.Result().Cookies() {
		got[c.Name] = c
	}
	session, csrf := got["__Host-ky_session"], got["__Host-ky_csrf"]
	for _, c := range []*http.Cookie{session, csrf} {
		if c == nil || !c.Secure || c.Path != "/" || c.Domain != "" {
			t.Fatalf("not a __Host- cookie: %+v (all: %v)", c, got)
		}
	}
	if got[auth.SessionCookieName] != nil || got[auth.CSRFCookieName] != nil {
		t.Fatalf("bare cookie names set on https: %v", got)
	}

	if !signedIn(srv, session) {
		t.Fatal("the __Host- session is not accepted")
	}
	// A valid session token under the bare name (what a sibling host can plant) is ignored.
	if signedIn(srv, &http.Cookie{Name: auth.SessionCookieName, Value: session.Value}) {
		t.Fatal("bare ky_session accepted on https")
	}
	// CSRF is checked against the __Host- cookie only.
	logout := func(csrfName string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
		req.AddCookie(session)
		req.AddCookie(&http.Cookie{Name: csrfName, Value: "planted"})
		req.Header.Set(auth.HeaderCSRF, "planted")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w.Code
	}
	if code := logout(auth.CSRFCookieName); code != http.StatusForbidden {
		t.Fatalf("bare ky_csrf satisfied the CSRF check: %d", code)
	}

	// SSO state cookies are __Host- too, and planted bare ones are refused.
	f := newFakeKySignOn(t)
	cfg.SSO.KySignOnIssuer, cfg.SSO.KySignOnClientID, cfg.SSO.AutoProvision = f.issuer, "client", true
	srv = api.NewServer(cfg, st)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/sso/kysignon/login", nil))
	loc, _ := url.Parse(w.Header().Get("Location"))
	state := loc.Query().Get("state")
	planted := httptest.NewRequest(http.MethodGet, "/api/sso/kysignon/callback?code=c&state="+state, nil)
	for _, c := range w.Result().Cookies() {
		if !strings.HasPrefix(c.Name, "__Host-ky_") || !c.Secure || c.Path != "/" || c.Domain != "" {
			t.Fatalf("SSO state cookie is not a __Host- cookie: %+v", c)
		}
		planted.AddCookie(&http.Cookie{Name: strings.TrimPrefix(c.Name, "__Host-"), Value: c.Value})
	}
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, planted)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bare SSO state cookies accepted: %d %s", w.Code, w.Body)
	}
	w = ssoLogin(t, srv, f, "kai", "sid-k")
	var signed bool
	for _, c := range w.Result().Cookies() {
		signed = signed || (c.Name == "__Host-ky_session" && c.Value != "")
	}
	if w.Code != http.StatusFound || !signed {
		t.Fatalf("SSO sign-in on https: %d %s", w.Code, w.Body)
	}
}
