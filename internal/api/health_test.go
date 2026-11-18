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
