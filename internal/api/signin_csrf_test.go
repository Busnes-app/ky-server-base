package api_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Sign-in has no CSRF token, so a cross-site or sibling-subdomain form (text/plain JSON) could
// log the victim into an attacker's account. The browser's Sec-Fetch-Site label refuses it;
// API clients send none.
func TestCrossSiteSignInIsRefused(t *testing.T) {
	srv, _, _ := setupTestServer(t)
	for _, path := range []string{"/api/auth/login"} {
		for site, want := range map[string]bool{"cross-site": true, "same-site": true, "same-origin": false, "": false} {
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

// Over plain HTTP (a LAN address) browsers send no Sec-Fetch-Site, but a cross-origin form
// POST still carries Origin: a foreign one is refused, the server's own host is not.
func TestForeignOriginSignInIsRefusedWithoutFetchMetadata(t *testing.T) {
	srv, _, _ := setupTestServer(t)
	for origin, want := range map[string]bool{
		"http://evil.example":      true,
		"null":                     true,
		"http://192.168.1.20:8080": false, // the host the request was sent to
		"":                         false, // an API client
	} {
		req := httptest.NewRequest(http.MethodPost, "http://192.168.1.20:8080/api/auth/login", bytes.NewBufferString(`{"username":"x","password":"y"}`))
		req.Header.Set("Content-Type", "text/plain")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		refused := w.Code == http.StatusForbidden && bytes.Contains(w.Body.Bytes(), []byte("Cross-site sign-in refused"))
		if refused != want {
			t.Errorf("Origin %q: %d %s", origin, w.Code, w.Body.String())
		}
	}
}
