package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// SIGTERM order: readiness goes first while ordinary routes keep answering, backup work is
// cancelled before the drain, and a request in flight when the listener closes still finishes.
func TestGracefulStopDrainsReadinessFirst(t *testing.T) {
	t.Setenv("KY_DATA_DIR", t.TempDir())
	t.Setenv("KY_DB_DRIVER", "sqlite")
	cfg, err := config.LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database.DSN = filepath.Join(cfg.Database.DataDir, "ky_server.db")
	st, err := store.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := api.NewServer(cfg, st)

	mux := http.NewServeMux()
	mux.Handle("/", srv)
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(time.Second)
		_, _ = io.WriteString(w, "finished")
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: mux}
	go func() { _ = httpServer.Serve(ln) }()
	base := "http://" + ln.Addr().String()
	get := func(path string) (int, string) {
		resp, err := http.Get(base + path)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if code, _ := get("/readyz"); code != http.StatusOK {
		t.Fatalf("readyz before stop: %d", code)
	}

	slow := make(chan string, 1)
	go func() { _, body := get("/slow"); slow <- body }()
	time.Sleep(100 * time.Millisecond) // the slow request is in flight

	var backupsStopped atomic.Bool
	backupDone := make(chan struct{})
	close(backupDone)
	stopped := make(chan struct{})
	const drain = 500 * time.Millisecond
	go func() {
		defer close(stopped)
		gracefulStop(srv, httpServer, func() { backupsStopped.Store(true) }, func() {}, backupDone, drain)
	}()
	time.Sleep(100 * time.Millisecond)
	if !backupsStopped.Load() {
		t.Fatal("backup work was not cancelled before the drain")
	}
	if code, _ := get("/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("readyz while draining: %d, want 503", code)
	}
	if code, _ := get("/api/auth/me"); code != http.StatusOK {
		t.Fatalf("ordinary route while draining: %d, want 200", code)
	}
	if body := <-slow; body != "finished" {
		t.Fatalf("in-flight request across the listener close: %q", body)
	}
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("gracefulStop did not return")
	}
	if code, _ := get("/api/auth/me"); code != 0 {
		t.Fatalf("listener still accepting after stop: %d", code)
	}
}
