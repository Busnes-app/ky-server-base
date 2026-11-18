package backup

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// lastAttemptKey is the row recoveryclient.Run stamps before it works and NextRun counts from.
// The lib does not export it; TestInterruptedRunIsDueAgain fails if the two drift apart.
const lastAttemptKey = "backup_last_attempt"

// ErrInterrupted marks a run cut short because the server is stopping.
var ErrInterrupted = errors.New("backup: interrupted by shutdown")

// Run is recoveryclient.Run for a process that may be told to stop mid-run. Cancelling ctx
// aborts the snapshot and the upload. The settings writes ignore the cancellation, so a
// receipt KyRecovery has already returned is still recorded. An interrupted run gives its
// attempt stamp back, so the scheduler runs it again on the next start, not an interval later.
func Run(ctx context.Context, cfg *config.Config, rc recoveryclient.RunConfig, st store.Store, client recoveryclient.Depositor) (recoveryclient.Result, error) {
	settings := Settings(context.WithoutCancel(ctx), st.Settings())
	// A replica that did not handle the pairing writes recovery.pub first. Its failure leaves
	// the file as it was, which the lib's own pin check below reports and audits.
	syncErr := SyncRecoveryKey(ctx, cfg, st)
	if syncErr != nil {
		log.Printf("[BACKUP] recovery.pub not synchronized with the pin: %s", recoveryclient.AuditSafe(syncErr.Error()))
	}
	prev, prevErr := settings.Get(lastAttemptKey)
	res, err := recoveryclient.Run(ctx, rc, settings, func() (recoveryclient.Payload, error) {
		return Collect(ctx, cfg, st, rc.AppVersion)
	}, client)
	// The lib reads a pin this replica cannot load as "not paired", which the scheduler skips
	// silently; the attempt is stamped, so the reason is reported once per interval.
	if errors.Is(err, recoveryclient.ErrNotPaired) && errors.Is(syncErr, ErrRecoveryKeyUnavailable) {
		err = syncErr
	}
	// ErrInProgress stamped nothing: the stamp belongs to the run that holds the lock.
	if err == nil || ctx.Err() == nil || res.Receipt != nil || errors.Is(err, recoveryclient.ErrInProgress) {
		return res, err
	}
	switch {
	case errors.Is(prevErr, recoveryclient.ErrNotFound):
		_ = settings.Delete(lastAttemptKey)
	case prevErr == nil:
		_ = settings.Set(lastAttemptKey, prev)
	}
	return res, fmt.Errorf("%w: %w", ErrInterrupted, err)
}

// RunOwned coordinates manual HTTP/CLI backups with the elected scheduler across hosts.
func RunOwned(ctx context.Context, cfg *config.Config, rc recoveryclient.RunConfig, st store.Store, client recoveryclient.Depositor) (recoveryclient.Result, error) {
	var result recoveryclient.Result
	won, err := st.Coordination().WithLock(ctx, "backup-run", func(owned context.Context) error {
		var err error
		result, err = Run(owned, cfg, rc, st, client)
		return err
	})
	if err == nil && !won {
		err = recoveryclient.ErrInProgress
	}
	return result, err
}
