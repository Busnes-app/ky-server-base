package api

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// readinessTTL caps database load from probes: concurrent probes share one Ping per second.
const readinessTTL = time.Second

// readinessPingTimeout is how long a Ping may take before the replica reports unready.
const readinessPingTimeout = time.Second

// readiness caches the last store Ping. The mutex is held across the Ping, so a burst of
// probes waits for one result instead of starting one Ping each.
type readiness struct {
	mu      sync.Mutex
	checked time.Time
	ready   bool
}

// StartDraining makes /readyz answer 503 from now on while every other route keeps serving,
// so a proxy stops routing here before the listener closes.
func (s *Server) StartDraining() { s.draining.Store(true) }

func (s *Server) handleLivez(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz reports only the status word: no error text, driver or version.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.draining.Load() && s.storeReady(r.Context()) {
		s.writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
		return
	}
	s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unready"})
}

func (s *Server) storeReady(ctx context.Context) bool {
	s.ready.mu.Lock()
	defer s.ready.mu.Unlock()
	if time.Since(s.ready.checked) < readinessTTL {
		return s.ready.ready
	}
	// Detached from the probe so one probe hanging up does not mark the replica unready.
	ping, cancel := context.WithTimeout(context.WithoutCancel(ctx), readinessPingTimeout)
	defer cancel()
	s.ready.ready = s.store.Ping(ping) == nil
	s.ready.checked = time.Now()
	return s.ready.ready
}
