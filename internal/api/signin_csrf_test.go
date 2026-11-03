package api_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Sign-in has no CSRF token, so a cross-site form (text/plain JSON) could log the victim into
// an attacker's account. The browser's Sec-Fetch-Site label refuses it; API clients send none.
func TestCrossSiteSignInIsRefused(t *testing.T) {
	srv, _, _ := setupTestServer(t)
	for _, path := range []string{"/api/auth/login", "/api/auth/mfa/totp", "/api/auth/mfa/recovery-code"} {
		for site, want := range map[string]bool{"cross-site": true, "same-origin": false, "": false} {
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(`{"username":"x","password":"y"}`))
			req.Header.Set("Content-Type", "text/plain")
			if site != "" {
				req.Header.Set("Sec-Fetch-Site", site)
			}
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			refused := w.Code == http.StatusForbidden && bytes.Contains(w.Body.Bytes(), []byte("Cross-site sign-in refused"))
			if refused != want {
				t.Errorf("%s with Sec-Fetch-Site %q: %d %s", path, site, w.Code, w.Body.String())
			}
		}
	}
}
