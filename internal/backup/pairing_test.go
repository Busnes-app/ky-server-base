package backup_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

func pairingInstance(t *testing.T) (*config.Config, store.Store, store.Store, *sql.DB) {
	t.Helper()
	cfg := &config.Config{Database: testdb.Config(t)}
	cfg.Database.DataDir = t.TempDir()
	cfg.Security.EncryptionKey = bytes.Repeat([]byte{1}, 32)
	a, err := store.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	b, err := store.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	db, err := sql.Open(map[string]string{"sqlite": "sqlite", "postgres": "pgx"}[cfg.Database.Driver], cfg.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := recoveryclient.StoreRecoveryKey(cfg.Database.DataDir, backup.Settings(context.Background(), a.Settings()), newKey(t, 2, 3)); err != nil {
		t.Fatal(err)
	}
	return cfg, a, b, db
}

func rejectPairingWrite(t *testing.T, db *sql.DB, driver, event, row string) {
	t.Helper()
	ref := "NEW"
	if event == "DELETE" {
		ref = "OLD"
	}
	query := fmt.Sprintf(`CREATE TRIGGER reject_pairing BEFORE %s ON server_settings FOR EACH ROW WHEN (%s.key = '%s') BEGIN SELECT RAISE(ABORT, 'synthetic pairing failure'); END`, event, ref, row)
	if driver == "postgres" {
		if _, err := db.Exec(`CREATE FUNCTION reject_pairing_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic pairing failure'; END $$`); err != nil {
			t.Fatal(err)
		}
		query = fmt.Sprintf(`CREATE TRIGGER reject_pairing BEFORE %s ON server_settings FOR EACH ROW WHEN (%s.key = '%s') EXECUTE FUNCTION reject_pairing_write()`, event, ref, row)
	}
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
}

func TestPairingChangesRollBackTogether(t *testing.T) {
	for _, tc := range []struct {
		name, event, row string
		clear            bool
	}{
		{"failed token refresh", "UPDATE", "kyrecovery_token_enc", false},
		{"failed URL refresh after token write", "UPDATE", "kyrecovery_url", false},
		{"failed unpair after token removal", "DELETE", "kyrecovery_url", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, a, b, db := pairingInstance(t)
			ctx := context.Background()
			sealer, _ := backup.NewSealer(cfg)
			if err := backup.StorePairing(ctx, a.Settings(), sealer, "https://old.example.test", "old-token"); err != nil {
				t.Fatal(err)
			}
			before, err := a.Settings().GetAllSettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			rejectPairingWrite(t, db, cfg.Database.Driver, tc.event, tc.row)
			if tc.clear {
				err = backup.ClearPairing(ctx, a.Settings())
			} else {
				err = backup.StorePairing(ctx, a.Settings(), sealer, "https://new.example.test", "new-token")
			}
			if err == nil {
				t.Fatal("injected failure was ignored")
			}
			p, err := recoveryclient.LoadPairing(cfg.Database.DataDir, backup.Settings(ctx, b.Settings()), sealer)
			if err != nil || p.URL != "https://old.example.test" || p.Token != "old-token" {
				t.Fatalf("failed operation changed pairing: URL=%q err=%v", p.URL, err)
			}
			after, err := a.Settings().GetAllSettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if before["kyrecovery_token_enc"] != after["kyrecovery_token_enc"] {
				t.Fatal("failed operation changed persisted ciphertext")
			}
		})
	}
}

func TestPairingReadsStayCoherentAcrossRefreshAndUnpair(t *testing.T) {
	cfg, a, b, _ := pairingInstance(t)
	ctx := context.Background()
	sealer, _ := backup.NewSealer(cfg)
	if err := backup.StorePairing(ctx, a.Settings(), sealer, "https://old.example.test", "old-token"); err != nil {
		t.Fatal(err)
	}
	reader := backup.Settings(ctx, b.Settings())
	url, err := reader.Get("kyrecovery_url")
	if err != nil {
		t.Fatal(err)
	}
	if err := backup.StorePairing(ctx, a.Settings(), sealer, "https://new.example.test", "new-token"); err != nil {
		t.Fatal(err)
	}
	if err := backup.ClearPairing(ctx, a.Settings()); err != nil {
		t.Fatal(err)
	}
	sealed, err := reader.Get("kyrecovery_token_enc")
	if err != nil {
		t.Fatal(err)
	}
	token, err := sealer.Open(sealed)
	if err != nil || url != "https://old.example.test" || string(token) != "old-token" {
		t.Fatal("reader mixed pairing generations")
	}
	if _, err := recoveryclient.LoadPairing(cfg.Database.DataDir, backup.Settings(ctx, b.Settings()), sealer); !errors.Is(err, recoveryclient.ErrNotPaired) {
		t.Fatalf("fresh reader after unpair: %v", err)
	}
}

func TestConcurrentPairingRefreshesAndUnpairNeverMixCredentials(t *testing.T) {
	cfg, a, b, _ := pairingInstance(t)
	ctx := context.Background()
	sealer, _ := backup.NewSealer(cfg)
	const rounds = 40
	var wg sync.WaitGroup
	for id, st := range []store.Store{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < rounds; n++ {
				url := fmt.Sprintf("https://replica-%d-%d.example.test", id, n)
				if err := backup.StorePairing(ctx, st.Settings(), sealer, url, url); err != nil {
					t.Error(err)
					return
				}
				if n%3 == 0 {
					if err := backup.ClearPairing(ctx, st.Settings()); err != nil && !errors.Is(err, recoveryclient.ErrNotPaired) {
						t.Error(err)
						return
					}
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 0; n < rounds*4; n++ {
			p, err := recoveryclient.LoadPairing(cfg.Database.DataDir, backup.Settings(ctx, b.Settings()), sealer)
			if errors.Is(err, recoveryclient.ErrNotPaired) {
				continue
			}
			if err != nil {
				t.Error(err)
				return
			}
			if p.URL != p.Token {
				t.Errorf("read mixed URL/token generations: %q", p.URL)
				return
			}
		}
	}()
	wg.Wait()
}
