package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// errReplicaConfig is a replica whose shared settings differ from the recorded ones.
var errReplicaConfig = errors.New("replica configuration differs from the other replicas")

// errKeyDiffers is never accepted: the sealed settings every replica shares open only under
// the recorded key, so a different one is a typo or the wrong file, not a rotation.
const errKeyDiffers = "KY_ENCRYPTION_KEY differs from the key the other replicas use. Supply the retained key; it cannot be rotated with accept-replica-config"

// checkReplicaConfig records the shared-setting fingerprint on first start and refuses a
// replica that differs, naming only the fields. The caller holds the bootstrap lock.
func checkReplicaConfig(ctx context.Context, cfg *config.Config, settings store.SettingsStore) error {
	current := config.ReplicaFingerprint(cfg)
	raw, err := settings.GetSetting(ctx, config.ReplicaConfigSetting)
	if errors.Is(err, store.ErrNotFound) {
		return storeFingerprint(ctx, settings, current)
	}
	if err != nil {
		return err
	}
	var stored map[string]string
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return fmt.Errorf("%w: the stored record is unreadable; run accept-replica-config", errReplicaConfig)
	}
	if diff := config.DiffFingerprint(stored, current); slices.Equal(diff, []string{config.EncryptionKeyField}) {
		return fmt.Errorf("%w: %s", errReplicaConfig, errKeyDiffers)
	} else if len(diff) > 0 {
		return fmt.Errorf("%w: %s. Use the same values on every replica; after a deliberate change on all of them, run accept-replica-config once", errReplicaConfig, strings.Join(diff, ", "))
	}
	if config.MergeFingerprint(stored, current) {
		return storeFingerprint(ctx, settings, stored)
	}
	return nil
}

func storeFingerprint(ctx context.Context, settings store.SettingsStore, fp map[string]string) error {
	b, err := json.Marshal(fp)
	if err != nil {
		return err
	}
	return settings.SetSetting(ctx, config.ReplicaConfigSetting, string(b))
}

// acceptReplicaConfig rewrites the record from this process's configuration, after a
// deliberate rotation, and audits which fields changed (names only).
func acceptReplicaConfig(ctx context.Context, cfg *config.Config, st store.Store, out io.Writer) error {
	if st.Driver() != "postgres" {
		return errors.New("accept-replica-config applies to PostgreSQL replicas only; SQLite records no replica configuration")
	}
	var accepted bool
	for !accepted {
		won, err := st.Coordination().WithLock(ctx, "bootstrap", func(ctx context.Context) error {
			current := config.ReplicaFingerprint(cfg)
			changed := slices.Sorted(maps.Keys(current))
			if raw, err := st.Settings().GetSetting(ctx, config.ReplicaConfigSetting); err == nil {
				var stored map[string]string
				if json.Unmarshal([]byte(raw), &stored) == nil {
					changed = config.DiffFingerprint(stored, current)
					if slices.Equal(changed, []string{config.EncryptionKeyField}) {
						return errors.New("refusing: " + errKeyDiffers)
					}
				}
			} else if !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if err := storeFingerprint(ctx, st.Settings(), current); err != nil {
				return err
			}
			if err := st.Audit().LogAudit(ctx, &store.AuditRecord{UserID: "cli", Action: "admin.replica_config_accepted",
				Resource: config.ReplicaConfigSetting, Details: "fields=" + strings.Join(changed, ",")}); err != nil {
				return fmt.Errorf("record accepted, but the audit row failed: %w", err)
			}
			if len(changed) == 0 {
				fmt.Fprintln(out, "Replica configuration unchanged; record rewritten.")
			} else {
				fmt.Fprintf(out, "Replica configuration accepted. Changed: %s\n", strings.Join(changed, ", "))
			}
			return nil
		})
		if err != nil {
			return err
		}
		if accepted = won; !accepted {
			time.Sleep(100 * time.Millisecond) // a replica is starting; its bootstrap is short
		}
	}
	return nil
}

func runAcceptReplicaConfig() {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Database)
	if err != nil {
		log.Fatalf("DB error: %v", err)
	}
	defer st.Close()
	if err := acceptReplicaConfig(ctx, cfg, st, os.Stdout); err != nil {
		log.Fatal(err)
	}
}
