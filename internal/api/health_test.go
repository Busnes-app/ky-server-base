package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// pingCountingStore counts Pings; stall makes each one block until its context ends.
type pingCountingStore struct {
	store.Store
	pings atomic.Int32
	stall bool
}

func (c *pingCountingStore) Ping(ctx context.Context) error {
	c.pings.Add(1)
	if c.stall {
		<-ctx.Done()
		return ctx.Err()
	}
	return c.Store.Ping(ctx)
}

func probe(t *testing.T, srv http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

// assertProbe checks the status, that nothing but the status word is sent, and no-store.
func assertProbe(t *testing.T, w *httptest.ResponseRecorder, code int, word string) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("got %d, want %d: %s", w.Code, code, w.Body.String())
	}
	if got := strings.TrimSpace(w.Body.String()); got != `{"status":"`+word+`"}` {
		t.Fatalf("body %q carries more than the status word", got)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control %q, want no-store", w.Header().Get("Cache-Control"))
	}
}

func TestLivezNeedsNoStore(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	assertProbe(t, probe(t, srv, "GET", "/livez"), http.StatusOK, "ok")
	if w := probe(t, srv, "HEAD", "/livez"); w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("HEAD /livez: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
}

func TestReadyzFollowsTheStore(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	assertProbe(t, probe(t, srv, "GET", "/readyz"), http.StatusOK, "ready")
	if w := probe(t, srv, "HEAD", "/readyz"); w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("HEAD /readyz: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}

	_, st2, cfg := setupTestServer(t)
	closed := api.NewServer(cfg, st2)
	if err := st2.Close(); err != nil {
		t.Fatal(err)
	}
	w := probe(t, closed, "GET", "/readyz")
	assertProbe(t, w, http.StatusServiceUnavailable, "unready")
	for _, leak := range []string{st.Driver(), "sql", "closed", "1.0.0"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Fatalf("unready body leaks %q: %s", leak, w.Body.String())
		}
	}
	if w := probe(t, closed, "HEAD", "/readyz"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("HEAD /readyz on a closed store: %d", w.Code)
	}
}

// A Ping that never answers is cut at one second, never held open by the probe.
func TestReadyzTimesOutAStalledPing(t *testing.T) {
	_, st, cfg := setupTestServer(t)
	srv := api.NewServer(cfg, &pingCountingStore{Store: st, stall: true})
	start := time.Now()
	assertProbe(t, probe(t, srv, "GET", "/readyz"), http.StatusServiceUnavailable, "unready")
	if took := time.Since(start); took < 900*time.Millisecond || took > 3*time.Second {
		t.Fatalf("stalled ping answered after %s, want about 1s", took)
	}
}

func TestReadyzCachesOnePingPerSecond(t *testing.T) {
	_, st, cfg := setupTestServer(t)
	counting := &pingCountingStore{Store: st}
	srv := api.NewServer(cfg, counting)
	start := time.Now()
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := probe(t, srv, "GET", "/readyz"); w.Code != http.StatusOK {
				t.Errorf("concurrent probe: %d", w.Code)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	if n, limit := counting.pings.Load(), int32(elapsed/time.Second)+1; n > limit {
		t.Fatalf("100 concurrent probes within %s made %d Pings, want at most %d", elapsed, n, limit)
	}
	time.Sleep(1100 * time.Millisecond) // the cache must expire, not stick
	before := counting.pings.Load()
	probe(t, srv, "GET", "/readyz")
	if n := counting.pings.Load(); n != before+1 {
		t.Fatalf("after the cache second a probe made %d Pings, want 1", n-before)
	}
}

func TestDrainingTurnsOnlyReadinessOff(t *testing.T) {
	srv, _, _ := setupTestServer(t)
	assertProbe(t, probe(t, srv, "GET", "/readyz"), http.StatusOK, "ready")
	srv.StartDraining()
	assertProbe(t, probe(t, srv, "GET", "/readyz"), http.StatusServiceUnavailable, "unready")
	assertProbe(t, probe(t, srv, "GET", "/livez"), http.StatusOK, "ok")
	if w := probe(t, srv, "GET", "/api/auth/me"); w.Code != http.StatusOK {
		t.Fatalf("ordinary route while draining: %d", w.Code)
	}
}

// frozenSessions is a database that stopped answering: every session lookup waits for its
// context, as a query on a paused PostgreSQL does.
type frozenSessions struct{ store.SessionStore }

func (frozenSessions) GetSession(ctx context.Context, _ string) (*store.Session, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type frozenStore struct{ store.Store }

func (f frozenStore) Sessions() store.SessionStore { return frozenSessions{f.Store.Sessions()} }

// Ordinary and backup management requests leave response time before the write deadline.
// Only capsule operations keep the long budget.
func TestStalledRequestEndsAtTheWriteDeadline(t *testing.T) {
	_, st, cfg := setupTestServer(t)
	cfg.Server.WriteTimeout = 200 * time.Millisecond
	srv := api.NewServer(cfg, frozenStore{st})
	req := httptest.NewRequest("GET", "/api/admin/users", nil)
	req.AddCookie(&http.Cookie{Name: "ky_session", Value: "stalled"})
	done := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		done <- w.Code
	}()
	select {
	case code := <-done:
		if code == http.StatusOK {
			t.Fatalf("a request whose database froze answered 200")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a request stalled on the database outlived its write deadline")
	}

	for _, tc := range []struct {
		method, path string
		long         bool
	}{
		{"GET", "/api/backup/status", false},
		{"PUT", "/api/backup/schedule", false},
		{"DELETE", "/api/backup/pairing", false},
		{"POST", "/api/backup/pin-key", false},
		{"POST", "/api/backup/pair-remote", false},
		{"POST", "/api/backup/deposit", true},
		{"POST", "/api/backup/drill", true},
		{"POST", "/api/backup/export-capsule", true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			var seen lookup
			srv := api.NewServer(cfg, deadlineStore{Store: st, seen: &seen})
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.AddCookie(&http.Cookie{Name: "ky_session", Value: "any"})
			req.AddCookie(&http.Cookie{Name: "ky_csrf", Value: "csrf"})
			req.Header.Set("X-CSRF-Token", "csrf")
			start := time.Now()
			srv.ServeHTTP(httptest.NewRecorder(), req)
			if !seen.called || seen.deadline.IsZero() != tc.long {
				t.Fatalf("session lookup called=%v deadline=%v, long operation=%v", seen.called, seen.deadline, tc.long)
			}
			if !tc.long && !seen.deadline.Before(start.Add(cfg.Server.WriteTimeout)) {
				t.Fatal("no time left to write the response")
			}
		})
	}
}

func TestFrozenDatabaseStillAllowsAnErrorReply(t *testing.T) {
	_, st, cfg := setupTestServer(t)
	cfg.Server.WriteTimeout = 2 * time.Second
	srv := httptest.NewUnstartedServer(api.NewServer(cfg, frozenStore{st}))
	srv.Config.WriteTimeout = cfg.Server.WriteTimeout
	srv.Start()
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/backup/status", nil)
	req.AddCookie(&http.Cookie{Name: "ky_session", Value: "stalled"})
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("stalled request lost its error reply: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("frozen session lookup answered 200")
	}
}

type lookup struct {
	called   bool
	deadline time.Time
}

// deadlineStore records the deadline of a session lookup's context.
type deadlineStore struct {
	store.Store
	seen *lookup
}

type deadlineSessions struct {
	store.SessionStore
	seen *lookup
}

func (d deadlineSessions) GetSession(ctx context.Context, h string) (*store.Session, error) {
	d.seen.called = true
	d.seen.deadline, _ = ctx.Deadline()
	return d.SessionStore.GetSession(ctx, h)
}

func (d deadlineStore) Sessions() store.SessionStore {
	return deadlineSessions{d.Store.Sessions(), d.seen}
}
