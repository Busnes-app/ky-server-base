package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Busnes-app/ky-primitives/capsule"
	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky-primitives/recoverykey"
	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// replicaB is a second PostgreSQL replica on the same database with its own data and backup
// directories, as two hosts or two containers would have.
func replicaB(t *testing.T, cfg *config.Config) (*api.Server, *config.Config) {
	t.Helper()
	b := *cfg
	b.Database.DataDir = t.TempDir()
	b.Backup.Dir = filepath.Join(t.TempDir(), "capsules")
	b.Backup.Keep = 2
	st, err := store.Open(context.Background(), b.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return api.NewServer(&b, st), &b
}

// A pin or a pairing lands on whichever replica the proxy picked. The other one writes the
// same recovery.pub from the database before it seals anything, and its backup opens with
// the suite key.
func TestSecondReplicaMaterializesThePinnedKey(t *testing.T) {
	for _, via := range []string{"pin", "pairing"} {
		t.Run(via, func(t *testing.T) {
			a, st, cfg := setupTestServer(t)
			if st.Driver() != "postgres" {
				t.Skip("replicas share PostgreSQL; SQLite is one server")
			}
			b, cfgB := replicaB(t, cfg)
			session := loginAs(t, a, st, "alice", "admin")
			priv, _ := recoverykey.Generate()
			key := recoveryclient.RecoveryKey{Public: priv.Public(), Threshold: 2, TotalShares: 3}
			if via == "pin" {
				if w := adminDo(t, a, session, "POST", "/api/backup/pin-key", pinBody(priv.Public(), 2, 3)); w.Code != http.StatusOK {
					t.Fatal(w.Body.String())
				}
			} else {
				api.SetRecoveryClientForTest(a, fakePairer{result: recoveryclient.PairingResult{APIToken: "tok", Key: key}})
				w := adminDo(t, a, session, "POST", "/api/backup/pair-remote", map[string]string{"recovery_url": "https://recovery.busnes.app", "pairing_code": "123456"})
				if w.Code != http.StatusOK {
					t.Fatal(w.Body.String())
				}
				// B is paired too (shared rows); the fake store takes its deposit.
				api.SetRecoveryClientForTest(b, &fakeDepositor{})
			}
			pubB := recoveryclient.RecoveryKeyPath(cfgB.Database.DataDir)
			if _, err := os.Stat(pubB); !os.IsNotExist(err) {
				t.Fatalf("replica B already has recovery.pub: %v", err)
			}
			w := adminPost(t, b, session, "/api/backup/deposit")
			if w.Code != http.StatusOK {
				t.Fatalf("deposit on replica B: %d %s", w.Code, w.Body.String())
			}
			want, _ := os.ReadFile(recoveryclient.RecoveryKeyPath(cfg.Database.DataDir))
			got, err := os.ReadFile(pubB)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("replica B recovery.pub differs from A's: %v", err)
			}
			if info, _ := os.Stat(pubB); info.Mode().Perm() != 0600 {
				t.Errorf("recovery.pub mode %o", info.Mode().Perm())
			}
			var res recoveryclient.Result
			if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || res.LocalPath == "" {
				t.Fatalf("result %s: %v", w.Body.String(), err)
			}
			raw, _ := os.ReadFile(res.LocalPath)
			if _, _, err := capsule.Open(raw, priv, t.TempDir()); err != nil {
				t.Fatalf("replica B's capsule does not open with the suite key: %v", err)
			}
		})
	}
}

// A replica whose recovery.pub is not the pinned key must not seal; the startup sync names it.
func TestReplicaWithADifferentRecoveryPubIsRefused(t *testing.T) {
	a, st, cfg := setupTestServer(t)
	if st.Driver() != "postgres" {
		t.Skip("replicas share PostgreSQL; SQLite is one server")
	}
	_, cfgB := replicaB(t, cfg)
	session := loginAs(t, a, st, "alice", "admin")
	priv, _ := recoverykey.Generate()
	if w := adminDo(t, a, session, "POST", "/api/backup/pin-key", pinBody(priv.Public(), 2, 3)); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	other, _ := recoverykey.Generate()
	if err := os.WriteFile(recoveryclient.RecoveryKeyPath(cfgB.Database.DataDir), other.Public().Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	err := backup.SyncRecoveryKey(context.Background(), cfgB, st)
	if err == nil {
		t.Fatal("a replica holding a different recovery.pub synchronized")
	}
	if _, err := recoveryclient.LoadRecoveryKey(cfgB.Database.DataDir, backup.Settings(context.Background(), st.Settings())); err == nil {
		t.Fatal("the different key loads")
	}
}

// A PostgreSQL schema the scaffold does not know answers 412 naming the adapter requirement.
func TestPostgresDepositRefusesAnUnknownSchema(t *testing.T) {
	srv, st, cfg := setupTestServer(t)
	if st.Driver() != "postgres" {
		t.Skip("needs PostgreSQL")
	}
	cfg.Backup.Dir = filepath.Join(t.TempDir(), "capsules")
	cfg.Backup.Keep = 2
	session := loginAs(t, srv, st, "alice", "admin")
	priv, _ := recoverykey.Generate()
	if w := adminDo(t, srv, session, "POST", "/api/backup/pin-key", pinBody(priv.Public(), 2, 3)); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	db, err := sql.Open("pgx", cfg.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`ALTER TABLE users ADD COLUMN product_flag TEXT`); err != nil {
		t.Fatal(err)
	}
	w := adminPost(t, srv, session, "/api/backup/deposit")
	if w.Code != http.StatusPreconditionFailed || !strings.Contains(w.Body.String(), "adapter") {
		t.Fatalf("unknown schema: %d %s", w.Code, w.Body.String())
	}
	if entries, _ := os.ReadDir(cfg.Backup.Dir); len(entries) != 0 {
		t.Fatalf("a capsule was written: %v", entries)
	}
}

// Through a round-robin proxy, two admins pin different keys on two replicas at once: one
// gets 200, the other 409, and both replicas then report the winner's key and topology.
func TestConcurrentPinsOnTwoReplicasPinOneKey(t *testing.T) {
	a, st, cfg := setupTestServer(t)
	if st.Driver() != "postgres" {
		t.Skip("replicas share PostgreSQL; SQLite is one server")
	}
	b, _ := replicaB(t, cfg)
	session := loginAs(t, a, st, "alice", "admin")
	privA, _ := recoverykey.Generate()
	privB, _ := recoverykey.Generate()
	codes := make([]int, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, pin := range []struct {
		srv  *api.Server
		body map[string]any
	}{{a, pinBody(privA.Public(), 2, 3)}, {b, pinBody(privB.Public(), 3, 5)}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes[i] = adminDo(t, pin.srv, session, "POST", "/api/backup/pin-key", pin.body).Code
		}()
	}
	close(start)
	wg.Wait()
	winner, k, n := privA.Public().ID(), 2.0, 3.0
	switch {
	case codes[0] == http.StatusOK && codes[1] == http.StatusConflict:
	case codes[1] == http.StatusOK && codes[0] == http.StatusConflict:
		winner, k, n = privB.Public().ID(), 3, 5
	default:
		t.Fatalf("concurrent pins answered %v, want one 200 and one 409", codes)
	}
	for name, srv := range map[string]*api.Server{"A": a, "B": b} {
		w := adminDo(t, srv, session, "GET", "/api/backup/status", nil)
		var status map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if status["recovery_key_id"] != winner || status["threshold"] != k || status["total_shares"] != n {
			t.Errorf("replica %s reports %v %v-of-%v, want %s %v-of-%v", name, status["recovery_key_id"], status["threshold"], status["total_shares"], winner, k, n)
		}
	}
}
