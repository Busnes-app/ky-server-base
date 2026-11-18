// Package worker elects one scheduled worker using the application's storage backend.
package worker

import (
	"context"
	"log"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/store"
)

// Run is embedded in every server. One owner runs jobs; other servers stay available for
// requests and retry election. Jobs must honour cancellation and tolerate retries: an
// interrupted external operation can finish remotely before its acknowledgement arrives.
func Run(ctx context.Context, coordination store.CoordinationStore, interval time.Duration, job func(context.Context)) {
	for ctx.Err() == nil {
		_, err := coordination.WithLock(ctx, "scheduled-worker", func(owned context.Context) error {
			log.Print("[WORKER] elected scheduled worker")
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-owned.Done():
					return owned.Err()
				case <-ticker.C:
					job(owned)
				}
			}
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("[WORKER] ownership lost or unavailable: %v", err)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
