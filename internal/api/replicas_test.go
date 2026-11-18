package api_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

func replicaServers(t *testing.T) (*api.Server, *api.Server, store.Store) {
	t.Helper()
	a, st, cfg := setupTestServer(t)
	if st.Driver() != "postgres" {
		t.Skip("HTTP replicas require PostgreSQL; SQLite remains a single server")
	}
	other, err := store.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	return a, api.NewServer(cfg, other), st
}

func TestReplicasShareLoginLimitsAndSessions(t *testing.T) {
	a, b, st := replicaServers(t)
	session := loginAs(t, a, st, "replica-user", "user")
	if w := do(t, b, "GET", "/api/auth/me", session); !strings.Contains(w.Body.String(), `"authenticated":true`) {
		t.Fatalf("session rejected on other replica: %s", w.Body.String())
	}
	for i := 0; i < 20; i++ {
		srv := a
		if i%2 != 0 {
			srv = b
		}
		if code := loginStatus(t, srv, "198.51.100.77:1000", ""); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if code := loginStatus(t, b, "198.51.100.77:2000", ""); code != http.StatusTooManyRequests {
		t.Fatalf("other replica gave fresh allowance: %d", code)
	}
	if w := do(t, b, "POST", "/api/auth/logout", session); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	if w := do(t, a, "GET", "/api/auth/me", session); !strings.Contains(w.Body.String(), `"authenticated":false`) {
		t.Fatalf("logout did not revoke other replica: %s", w.Body.String())
	}
}

func TestReplicasSpendCAPTCHAExactlyOnce(t *testing.T) {
	a, st, cfg := setupTestServer(t)
	if st.Driver() != "postgres" {
		t.Skip("requires PostgreSQL")
	}
	cfg.Captcha.Provider = "pow"
	cfg.Captcha.DifficultyPoW = 10
	other, err := store.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	b := api.NewServer(cfg, other)
	challenge, err := auth.GeneratePoWChallenge(10, cfg.Security.SessionSecret)
	if err != nil {
		t.Fatal(err)
	}
	var solved string
	for i := 1; i <= 10; i++ {
		raw, _ := json.Marshal(auth.PoWSolution{Algorithm: challenge.Algorithm, Salt: challenge.Salt, Challenge: challenge.Challenge, Number: i, MaxNumber: challenge.MaxNumber, ExpiresAt: challenge.ExpiresAt, Signature: challenge.Signature})
		candidate := base64.StdEncoding.EncodeToString(raw)
		if auth.VerifyPoWSolution(candidate, cfg.Security.SessionSecret, nil) {
			solved = candidate
			break
		}
	}
	if solved == "" {
		t.Fatal("no solved challenge")
	}
	body, _ := json.Marshal(api.LoginRequest{Username: "unknown", Password: "wrong-password", CaptchaToken: solved})
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i, srv := range []*api.Server{a, b} {
		wg.Add(1)
		go func(i int, srv *api.Server) {
			defer wg.Done()
			r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(string(body)))
			r.RemoteAddr = fmt.Sprintf("198.51.100.%d:1000", i+1)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, r)
			codes <- w.Code
		}(i, srv)
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[http.StatusUnauthorized] != 1 || counts[http.StatusForbidden] != 1 {
		t.Fatalf("CAPTCHA replay was not refused across replicas: %v", counts)
	}
}

func TestReplicaSeesOfficeNetworkRemoval(t *testing.T) {
	a, b, st := replicaServers(t)
	admin := loginAs(t, a, st, "networks-owner", "admin")
	if w := putNetworks(t, a, admin, officeRange); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	for i := 0; i < 25; i++ {
		if code := loginStatus(t, b, officePeer, ""); code == http.StatusTooManyRequests {
			t.Fatal("other replica did not see exemption")
		}
	}
	if w := putNetworks(t, a, admin, ""); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	for i := 0; i < 20; i++ {
		loginStatus(t, b, officePeer, "")
	}
	if code := loginStatus(t, b, officePeer, ""); code != http.StatusTooManyRequests {
		t.Fatalf("other replica kept revoked exemption: %d", code)
	}
}

func TestPostgresSecurityStoreFailureNeverFallsBack(t *testing.T) {
	for _, table := range []string{"security_windows", "spent_challenges"} {
		t.Run(table, func(t *testing.T) {
			srv, st, cfg := setupTestServer(t)
			if st.Driver() != "postgres" {
				t.Skip("requires PostgreSQL")
			}
			request := api.LoginRequest{Username: "unknown", Password: "wrong-password"}
			want := http.StatusTooManyRequests
			if table == "spent_challenges" {
				cfg.Captcha.Provider = "pow"
				challenge, err := auth.GeneratePoWChallenge(1, cfg.Security.SessionSecret)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(auth.PoWSolution{Algorithm: challenge.Algorithm, Salt: challenge.Salt, Challenge: challenge.Challenge, Number: 1, MaxNumber: 1, ExpiresAt: challenge.ExpiresAt, Signature: challenge.Signature})
				request.CaptchaToken = base64.StdEncoding.EncodeToString(raw)
				want = http.StatusServiceUnavailable
			}
			db, err := sql.Open("pgx", cfg.Database.DSN)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec("DROP TABLE " + table); err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(request)
			r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(string(body)))
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, r)
			if w.Code != want {
				t.Fatalf("shared %s failed but admission fell back: %d %s", table, w.Code, w.Body.String())
			}
		})
	}
}
