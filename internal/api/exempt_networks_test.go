package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Busnes-app/ky-primitives/password"
	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

const (
	officePeer  = "203.0.113.5:41000" // inside officeRange
	officeRange = "203.0.113.0/24"
)

func putNetworks(t *testing.T, srv *api.Server, session *http.Cookie, networks string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"networks": networks})
	req := httptest.NewRequest("PUT", "/api/settings/rate-limit-networks", strings.NewReader(string(body)))
	req.AddCookie(session)
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "test-csrf"})
	req.Header.Set(auth.HeaderCSRF, "test-csrf")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func networksStatus(t *testing.T, w *httptest.ResponseRecorder) (networks, environment string, exempted float64) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out["networks"].(string), out["environment"].(string), out["exempted"].(float64)
}

// An allowlisted office goes past the per-client window; everyone else keeps it.
func TestExemptNetworkSkipsThePerIPLoginLimit(t *testing.T) {
	t.Setenv("KY_RATE_LIMIT_EXEMPT_NETWORKS", officeRange)
	srv, _, _ := setupTestServer(t)

	for i := 1; i <= 60; i++ {
		if code := loginStatus(t, srv, officePeer, ""); code != http.StatusUnauthorized {
			t.Fatalf("office attempt %d answered %d, want the wrong-password 401", i, code)
		}
	}
	for i := 1; i <= 20; i++ {
		if code := loginStatus(t, srv, "198.51.100.9:41000", ""); code == http.StatusTooManyRequests {
			t.Fatalf("outside attempt %d throttled early", i)
		}
	}
	if code := loginStatus(t, srv, "198.51.100.9:41000", ""); code != http.StatusTooManyRequests {
		t.Fatalf("outside attempt 21 answered %d, want 429", code)
	}
}

// The exemption covers the per-IP window only: ten wrong guesses at one account from the
// office still close that account's window there.
func TestExemptNetworkKeepsThePerAccountWindow(t *testing.T) {
	t.Setenv("KY_RATE_LIMIT_EXEMPT_NETWORKS", officeRange)
	srv, st, _ := setupTestServer(t)
	hash, _ := password.Hash("SuperSecretPass123!")
	if err := st.Users().CreateUser(context.Background(), &store.User{
		ID: "usr_carol", Username: "carol", PasswordHash: hash, Role: "user", Status: "active", SSOProvider: "local",
	}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 10; i++ {
		if w := loginStatusFor(t, srv, "carol", "WrongPass123!", officePeer); w.Code != http.StatusUnauthorized {
			t.Fatalf("wrong password %d: %d", i, w.Code)
		}
	}
	if w := loginStatusFor(t, srv, "carol", "SuperSecretPass123!", officePeer); w.Code != http.StatusUnauthorized {
		t.Fatalf("exhausted account window from the office answered %d, want 401", w.Code)
	}
}

// With no trusted proxy, X-Forwarded-For is the caller's own claim and buys no exemption.
func TestForgedForwardedForCannotClaimAnExemptNetwork(t *testing.T) {
	t.Setenv("KY_RATE_LIMIT_EXEMPT_NETWORKS", officeRange)
	srv, _, _ := setupTestServer(t)

	for i := 1; i <= 20; i++ {
		loginStatus(t, srv, "198.51.100.9:41000", "203.0.113.5")
	}
	if code := loginStatus(t, srv, "198.51.100.9:41000", "203.0.113.5"); code != http.StatusTooManyRequests {
		t.Fatalf("attempt 21 answered %d, want 429: a forged header claimed the office", code)
	}
}

// Behind a trusted proxy the forwarded client is the one matched, and a client that
// prepends an office address to the chain is still matched on the entry the proxy appended.
func TestExemptNetworkMatchesTheClientATrustedProxyReports(t *testing.T) {
	t.Setenv("KY_TRUSTED_PROXIES", "192.0.2.0/24")
	t.Setenv("KY_RATE_LIMIT_EXEMPT_NETWORKS", officeRange)
	srv, _, _ := setupTestServer(t)

	for i := 1; i <= 30; i++ {
		if code := loginStatus(t, srv, "192.0.2.7:41000", "203.0.113.5"); code == http.StatusTooManyRequests {
			t.Fatalf("office client behind the proxy throttled at attempt %d", i)
		}
	}
	for i := 1; i <= 20; i++ {
		loginStatus(t, srv, "192.0.2.7:41000", "203.0.113.5, 198.51.100.9")
	}
	if code := loginStatus(t, srv, "192.0.2.7:41000", "203.0.113.5, 198.51.100.9"); code != http.StatusTooManyRequests {
		t.Fatalf("attempt 21 answered %d, want 429: a prepended office address was believed", code)
	}
}

// The password-change route keeps its per-account limit for an office client.
func TestExemptNetworkKeepsThePasswordChangeAccountLimit(t *testing.T) {
	t.Setenv("KY_RATE_LIMIT_EXEMPT_NETWORKS", officeRange)
	srv, st, _ := setupTestServer(t)
	session := loginAs(t, srv, st, "dave", "user")
	change := func() int {
		req := httptest.NewRequest("POST", "/api/auth/change-password", strings.NewReader(`{"current_password":"nope","new_password":"AnotherPass456!"}`))
		req.RemoteAddr = officePeer
		req.AddCookie(session)
		req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "test-csrf"})
		req.Header.Set(auth.HeaderCSRF, "test-csrf")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w.Code
	}
	for i := 1; i <= 5; i++ {
		if code := change(); code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d throttled early", i)
		}
	}
	if code := change(); code != http.StatusTooManyRequests {
		t.Fatalf("attempt 6 answered %d, want 429 from the per-account limit", code)
	}
}

// An admin's list takes effect at once, is stored canonically, audited, counted, and still
// there after a restart; the environment's list stays alongside it.
func TestAdminSetsExemptNetworks(t *testing.T) {
	t.Setenv("KY_RATE_LIMIT_EXEMPT_NETWORKS", "198.18.7.0/24")
	srv, st, cfg := setupTestServer(t)
	admin := loginAs(t, srv, st, "alice", "admin")

	networks, environment, exempted := networksStatus(t, do(t, srv, "GET", "/api/settings/rate-limit-networks", admin))
	if networks != "" || environment != "198.18.7.0/24" || exempted != 0 {
		t.Fatalf("before: networks=%q environment=%q exempted=%v", networks, environment, exempted)
	}

	networks, _, _ = networksStatus(t, putNetworks(t, srv, admin, " 203.0.113.77/24 , 2001:db8:1::/48"))
	if networks != "203.0.113.0/24,2001:db8:1::/48" {
		t.Fatalf("stored %q, want the canonical list", networks)
	}

	limited := func(s *api.Server, peer string) bool {
		for i := 0; i < 25; i++ {
			if loginStatus(t, s, peer, "") == http.StatusTooManyRequests {
				return true
			}
		}
		return false
	}
	if limited(srv, officePeer) || limited(srv, "198.18.7.50:41000") {
		t.Fatal("an allowlisted network was throttled")
	}
	if _, _, exempted = networksStatus(t, do(t, srv, "GET", "/api/settings/rate-limit-networks", admin)); exempted != 50 {
		t.Fatalf("exempted = %v, want 50", exempted)
	}

	records, _, err := st.Audit().ListAuditRecords(context.Background(), 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	audited := false
	for _, rec := range records {
		if rec.Action == "admin.rate_limit_networks" && rec.UserID == "usr_alice" &&
			rec.Details == `from="" to="203.0.113.0/24,2001:db8:1::/48"` {
			audited = true
		}
	}
	if !audited {
		t.Fatalf("no audit row for the change in %d records", len(records))
	}

	if restarted := api.NewServer(cfg, st); limited(restarted, officePeer) {
		t.Fatal("the stored list was not loaded at startup")
	}

	// Clearing it restores the limit for the office; the environment's network stays exempt.
	if networks, _, _ = networksStatus(t, putNetworks(t, srv, admin, "")); networks != "" {
		t.Fatalf("cleared list reads %q", networks)
	}
	if !limited(srv, "203.0.113.200:41000") {
		t.Fatal("a removed network is still exempt")
	}
	if limited(srv, "198.18.7.51:41000") {
		t.Fatal("clearing the admin list dropped the environment's network")
	}
}

// A list that would switch the limit off widely is refused and nothing changes.
func TestExemptNetworksRefuseBroadOrInvalidRanges(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	admin := loginAs(t, srv, st, "alice", "admin")
	if w := putNetworks(t, srv, admin, officeRange); w.Code != http.StatusOK {
		t.Fatalf("seed: %d %s", w.Code, w.Body.String())
	}
	for _, bad := range []string{"0.0.0.0/0", "::/0", "10.0.0.0/8", "2001:db8::/32", "office", "127.0.0.1", "203.0.113.0/24,nope"} {
		if w := putNetworks(t, srv, admin, bad); w.Code != http.StatusBadRequest {
			t.Errorf("PUT %q answered %d, want 400", bad, w.Code)
		}
	}
	if networks, _, _ := networksStatus(t, do(t, srv, "GET", "/api/settings/rate-limit-networks", admin)); networks != officeRange {
		t.Fatalf("a refused list replaced the stored one: %q", networks)
	}
	for i := 1; i <= 21; i++ {
		loginStatus(t, srv, "10.9.9.9:41000", "")
	}
	if code := loginStatus(t, srv, "10.9.9.9:41000", ""); code != http.StatusTooManyRequests {
		t.Fatalf("an address in a refused range answered %d, want 429", code)
	}
}

// Offices share one budget per limit. Past it their requests meet the ordinary per-IP window,
// so one host inside an office cannot make unbounded attempts or churn the limiter's keys.
func TestOfficeBudgetFallsBackToThePerIPLimit(t *testing.T) {
	t.Setenv("KY_RATE_LIMIT_EXEMPT_NETWORKS", officeRange)
	srv, _, _ := setupTestServer(t)

	for i := 1; i <= 320; i++ {
		if code := loginStatus(t, srv, officePeer, ""); code == http.StatusTooManyRequests {
			t.Fatalf("office attempt %d throttled, want the budget of 300 and then a window of 20", i)
		}
	}
	if code := loginStatus(t, srv, officePeer, ""); code != http.StatusTooManyRequests {
		t.Fatalf("office attempt 321 answered %d, want 429", code)
	}
	// Another office address has its own per-IP window once the budget is spent.
	if code := loginStatus(t, srv, "203.0.113.77:41000", ""); code == http.StatusTooManyRequests {
		t.Fatal("a second office address was throttled by the first one's window")
	}
}

// countingUsers counts the account lookups that precede every password verification.
type countingUsers struct {
	store.UserStore
	lookups *atomic.Int64
}

func (c countingUsers) GetUserByUsername(ctx context.Context, name string) (*store.User, error) {
	c.lookups.Add(1)
	return c.UserStore.GetUserByUsername(ctx, name)
}

type countingStore struct {
	store.Store
	lookups *atomic.Int64
}

func (c countingStore) Users() store.UserStore { return countingUsers{c.Store.Users(), c.lookups} }

// Concurrent guesses at one account from an office reserve their place in the account window
// before any hashing: no more than ten reach password verification, however they interleave.
func TestConcurrentOfficeGuessesCannotOutrunTheAccountWindow(t *testing.T) {
	t.Setenv("KY_RATE_LIMIT_EXEMPT_NETWORKS", officeRange)
	srv, st, _ := setupTestServer(t)
	var lookups atomic.Int64
	api.SetStoreForTest(srv, countingStore{st, &lookups})

	body, _ := json.Marshal(map[string]string{"username": "carol", "password": "WrongPass123!"})
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
			req.RemoteAddr = officePeer
			srv.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	wg.Wait()
	if got := lookups.Load(); got != 10 {
		t.Fatalf("%d guesses reached password verification, want exactly 10", got)
	}
}

// A successful sign-in hands its reservation back: only failures count against the account.
func TestSuccessfulSignInsDoNotFillTheAccountWindow(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	hash, _ := password.Hash("SuperSecretPass123!")
	if err := st.Users().CreateUser(context.Background(), &store.User{
		ID: "usr_erin", Username: "erin", PasswordHash: hash, Role: "user", Status: "active", SSOProvider: "local",
	}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 15; i++ {
		if w := loginStatusFor(t, srv, "erin", "SuperSecretPass123!", officePeer); w.Code != http.StatusOK {
			t.Fatalf("sign-in %d: %d %s", i, w.Code, w.Body.String())
		}
	}
}
