package backup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// RecoveryKeyRecordSetting holds the pinned recovery public key and its topology in the shared
// database. All of it is public; its trust comes from the lib's pin, which it must match.
const RecoveryKeyRecordSetting = "recovery_public_key"

const recoveryKeyRecord = RecoveryKeyRecordSetting

// The lib's pin rows. It does not export the names; TestPinRowNames fails if they drift.
const (
	pinKeyID       = "kyrecovery_key_id"
	pinThreshold   = "kyrecovery_threshold"
	pinTotalShares = "kyrecovery_total_shares"
)

// recoveryPinLock serializes every write of the pin, its record and recovery.pub across
// replicas: the lib's write-once check is a read before a write, which only one process may
// make at a time once replicas no longer share recovery.pub.
const recoveryPinLock = "recovery-pin"

var (
	// ErrRecoveryKeyUnavailable is a pin this replica cannot load and must not guess at. It is
	// repaired by pinning the pinned key by hand on any replica, not by restarting.
	ErrRecoveryKeyUnavailable = errors.New("backup: the recovery key pinned in the database is not available on this replica")
	// ErrPinBusy is another replica holding the pin lock for longer than a pin takes.
	ErrPinBusy = errors.New("backup: another replica is changing the recovery key; try again")
	// ErrPinOwnershipLost is the pin lock's database session lost mid-write: nothing is
	// reported as pinned, because another replica may have taken the lock meanwhile.
	ErrPinOwnershipLost = errors.New("backup: lost the recovery-key lock while pinning; try again")
)

// pinWait bounds how long a pin or sync waits for another replica's (a variable for tests).
var pinWait = 10 * time.Second

// pinLocked observes a pin holding the lock, before it writes (tests only).
var pinLocked func(ctx context.Context)

type keyRecord struct {
	PublicKey   string `json:"public_key"`
	Threshold   int    `json:"threshold"`
	TotalShares int    `json:"total_shares"`
}

// RecordRecoveryKey stores a pinned key as the public record other replicas materialize from.
func RecordRecoveryKey(settings recoveryclient.Settings, k recoveryclient.RecoveryKey) error {
	b, err := json.Marshal(keyRecord{base64.StdEncoding.EncodeToString(k.Public.Bytes()), k.Threshold, k.TotalShares})
	if err != nil {
		return err
	}
	return settings.Set(recoveryKeyRecord, string(b))
}

// RecordRecoveryKeyFromFile records <dir>/recovery.pub when it is the key the settings pin,
// for migrate-postgres: a SQLite deployment or a restored capsule pinned before records
// existed. A pin with no matching file is refused with what to do; no pin records nothing.
func RecordRecoveryKeyFromFile(dir string, settings recoveryclient.Settings) error {
	id, err := settings.Get(pinKeyID)
	if errors.Is(err, recoveryclient.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := settings.Get(recoveryKeyRecord); err == nil || !errors.Is(err, recoveryclient.ErrNotFound) {
		return err
	}
	k, err := recoveryclient.LoadRecoveryKey(dir, settings)
	if err != nil {
		return unavailable("the database pins recovery key %s, but %s does not hold it (%v). Pin the same key by hand on the source server first (it rewrites recovery.pub), or put its recovery.pub beside the database",
			id, recoveryclient.RecoveryKeyPath(dir), err)
	}
	return RecordRecoveryKey(settings, k)
}

// PinRecoveryKey is the lib's write-once StoreRecoveryKey made global: under the recovery-pin
// lock, with write-once pin rows enforced by Settings even before lock loss is detected.
// Every write uses the lock's context and the pin is read back before success.
// A pin that did not stick is an error, and a recovery.pub it created is removed again.
func PinRecoveryKey(ctx context.Context, cfg *config.Config, st store.Store, k recoveryclient.RecoveryKey) error {
	dir := cfg.Database.DataDir
	return withPinLock(ctx, st, func(owned context.Context) error {
		if pinLocked != nil {
			pinLocked(owned)
		}
		settings := Settings(owned, st.Settings())
		path := recoveryclient.RecoveryKeyPath(dir)
		_, statErr := os.Lstat(path)
		err := owned.Err()
		if err == nil {
			err = recoveryclient.StoreRecoveryKey(dir, settings, k)
		}
		if err == nil {
			err = checkPinned(settings, k)
		}
		if err != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				_ = os.Remove(path)
			}
			return ownershipErr(owned, err)
		}
		// The pin stands; the record is only how other replicas learn it, and the next sync
		// on this replica writes it from recovery.pub if this write failed.
		if err := RecordRecoveryKey(settings, k); err != nil {
			log.Printf("[BACKUP] key %s is pinned but its replica record was not stored: %v", k.Public.ID(), err)
		}
		return nil
	})
}

// checkPinned reads the pin back: id and topology must be k's.
func checkPinned(settings recoveryclient.Settings, k recoveryclient.RecoveryKey) error {
	pinned, err := pinnedTopology(settings)
	if err != nil {
		return err
	}
	if pinned != (pin{k.Public.ID(), k.Threshold, k.TotalShares}) {
		return &recoveryclient.PinnedError{PinnedKeyID: pinned.id, OfferedKeyID: k.Public.ID()}
	}
	return nil
}

type pin struct {
	id               string
	threshold, total int
}

// pinnedTopology reads the lib's pin rows; a missing one reads as zero.
func pinnedTopology(settings recoveryclient.Settings) (pin, error) {
	var p pin
	var err error
	if p.id, err = optional(settings, pinKeyID); err != nil {
		return p, err
	}
	for key, n := range map[string]*int{pinThreshold: &p.threshold, pinTotalShares: &p.total} {
		v, err := optional(settings, key)
		if err != nil {
			return p, err
		}
		if v != "" {
			if *n, err = strconv.Atoi(v); err != nil {
				return p, fmt.Errorf("%s: %w", key, err)
			}
		}
	}
	return p, nil
}

func optional(settings recoveryclient.Settings, key string) (string, error) {
	v, err := settings.Get(key)
	if errors.Is(err, recoveryclient.ErrNotFound) {
		return "", nil
	}
	return v, err
}

func ownershipErr(owned context.Context, err error) error {
	if owned.Err() != nil {
		return fmt.Errorf("%w: %v", ErrPinOwnershipLost, err)
	}
	return err
}

// withPinLock runs work as the one holder of the recovery-pin lock, waiting up to pinWait.
func withPinLock(ctx context.Context, st store.Store, work func(context.Context) error) error {
	deadline := time.Now().Add(pinWait)
	for {
		won, err := st.Coordination().WithLock(ctx, recoveryPinLock, work)
		if err != nil || won {
			return err
		}
		if time.Now().After(deadline) {
			return ErrPinBusy
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type syncAction int

const (
	syncNone   syncAction = iota
	syncRecord            // recovery.pub is the pin; write the record from it
	syncWrite             // the record is the pin; write recovery.pub from it
)

// SyncRecoveryKey makes this PostgreSQL replica's recovery.pub, the pin and the shared record
// agree. A missing file is written from a record that matches the pin; a missing or stale
// record is rewritten from a file that matches the pin. Writes take the recovery-pin lock and
// re-check under it. A file naming another key is recoveryclient.ErrKeyMismatch (refuse to
// start); a pin no replica has published is ErrRecoveryKeyUnavailable. SQLite: a no-op.
func SyncRecoveryKey(ctx context.Context, cfg *config.Config, st store.Store) error {
	if cfg.Database.Driver != "postgres" {
		return nil
	}
	dir := cfg.Database.DataDir
	action, _, err := planSync(dir, Settings(ctx, st.Settings()))
	if err != nil || action == syncNone {
		return err
	}
	return withPinLock(ctx, st, func(owned context.Context) error {
		settings := Settings(owned, st.Settings())
		action, k, err := planSync(dir, settings)
		switch {
		case err != nil:
		case action == syncRecord:
			err = RecordRecoveryKey(settings, k)
		case action == syncWrite:
			if err = os.MkdirAll(dir, 0700); err == nil {
				err = recoveryclient.StoreRecoveryKey(dir, settings, k)
			}
		}
		if err != nil {
			return ownershipErr(owned, err)
		}
		return nil
	})
}

// planSync reads the pin, the record and recovery.pub and says what would make them agree.
func planSync(dir string, settings recoveryclient.Settings) (syncAction, recoveryclient.RecoveryKey, error) {
	rec, hasRec, err := loadRecord(settings)
	if err != nil {
		return syncNone, rec, err
	}
	if _, err := os.Lstat(recoveryclient.RecoveryKeyPath(dir)); err == nil {
		k, err := recoveryclient.LoadRecoveryKey(dir, settings)
		switch {
		case err == nil:
			if !hasRec || !sameKey(rec, k) {
				return syncRecord, k, nil
			}
			return syncNone, k, nil
		case errors.Is(err, recoveryclient.ErrNotPaired) && hasRec:
			return syncNone, rec, unpinnedRecord()
		case errors.Is(err, recoveryclient.ErrNotPaired):
			return syncNone, rec, nil
		default:
			return syncNone, rec, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return syncNone, rec, err
	}
	p, err := pinnedTopology(settings)
	switch {
	case err != nil:
		return syncNone, rec, err
	case p.id == "" && hasRec:
		return syncNone, rec, unpinnedRecord()
	case p.id == "":
		return syncNone, rec, nil
	case !hasRec:
		return syncNone, rec, unavailable("the database pins recovery key %s, which no replica has published: pin it by hand on any replica, or copy its recovery.pub into one", p.id)
	case p != (pin{rec.Public.ID(), rec.Threshold, rec.TotalShares}):
		return syncNone, rec, unavailable("the recovery key record does not match the pin %s: pin that key by hand on any replica to rewrite it", p.id)
	}
	return syncWrite, rec, nil
}

func unpinnedRecord() error {
	return unavailable("the database holds a recovery key record but no pin: pin the suite key by hand to confirm it")
}

// keyUnavailable is ErrRecoveryKeyUnavailable with its repair, kept short enough that an
// audit row (200 characters) still carries the key ID and what to do.
type keyUnavailable struct{ msg string }

func (e *keyUnavailable) Error() string        { return e.msg }
func (e *keyUnavailable) Is(target error) bool { return target == ErrRecoveryKeyUnavailable }

func unavailable(format string, args ...any) error {
	return &keyUnavailable{fmt.Sprintf(format, args...)}
}

func sameKey(a, b recoveryclient.RecoveryKey) bool {
	return a.Public.ID() == b.Public.ID() && a.Threshold == b.Threshold && a.TotalShares == b.TotalShares
}

func loadRecord(settings recoveryclient.Settings) (recoveryclient.RecoveryKey, bool, error) {
	raw, err := settings.Get(recoveryKeyRecord)
	if errors.Is(err, recoveryclient.ErrNotFound) {
		return recoveryclient.RecoveryKey{}, false, nil
	}
	if err != nil {
		return recoveryclient.RecoveryKey{}, false, err
	}
	var rec keyRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return recoveryclient.RecoveryKey{}, false, fmt.Errorf("backup: the stored recovery key record is unreadable: %w", err)
	}
	k, err := recoveryclient.ParsePinRequest(rec.PublicKey, rec.Threshold, rec.TotalShares)
	if err != nil {
		return recoveryclient.RecoveryKey{}, false, fmt.Errorf("backup: the stored recovery key record: %w", err)
	}
	return k, true, nil
}
