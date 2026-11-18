package backup

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/config"
)

// recoveryKeyRecord holds the pinned recovery public key and its topology in the shared
// database. All of it is public; its trust comes from the lib's pin, which it must match.
const recoveryKeyRecord = "recovery_public_key"

type keyRecord struct {
	PublicKey   string `json:"public_key"`
	Threshold   int    `json:"threshold"`
	TotalShares int    `json:"total_shares"`
}

// RecordRecoveryKey stores a key just pinned with recoveryclient.StoreRecoveryKey, so a
// replica that did not handle the pairing can write its own recovery.pub.
func RecordRecoveryKey(settings recoveryclient.Settings, k recoveryclient.RecoveryKey) error {
	b, err := json.Marshal(keyRecord{base64.StdEncoding.EncodeToString(k.Public.Bytes()), k.Threshold, k.TotalShares})
	if err != nil {
		return err
	}
	return settings.Set(recoveryKeyRecord, string(b))
}

// SyncRecoveryKey makes this PostgreSQL replica's <DataDir>/recovery.pub the pinned key: a
// missing file is written from the record through the lib's write-once StoreRecoveryKey,
// which also refuses a record whose key is not the pin. A file that differs from the pin is
// an error the caller must not start or seal past. A pin with no record yet (pinned before
// records existed) is recorded from a matching local file. SQLite has one host: a no-op.
func SyncRecoveryKey(cfg *config.Config, settings recoveryclient.Settings) error {
	if cfg.Database.Driver != "postgres" {
		return nil
	}
	dir := cfg.Database.DataDir
	raw, err := settings.Get(recoveryKeyRecord)
	if errors.Is(err, recoveryclient.ErrNotFound) {
		k, err := recoveryclient.LoadRecoveryKey(dir, settings)
		switch {
		case err == nil:
			return RecordRecoveryKey(settings, k)
		case errors.Is(err, recoveryclient.ErrNotPaired):
			return nil
		default:
			return err
		}
	}
	if err != nil {
		return err
	}
	if _, err := os.Stat(recoveryclient.RecoveryKeyPath(dir)); err == nil {
		_, err := recoveryclient.LoadRecoveryKey(dir, settings)
		if errors.Is(err, recoveryclient.ErrNotPaired) {
			return nil
		}
		return err
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var rec keyRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return fmt.Errorf("backup: the stored recovery key record is unreadable: %w", err)
	}
	k, err := recoveryclient.ParsePinRequest(rec.PublicKey, rec.Threshold, rec.TotalShares)
	if err != nil {
		return fmt.Errorf("backup: the stored recovery key record: %w", err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return recoveryclient.StoreRecoveryKey(dir, settings, k)
}
