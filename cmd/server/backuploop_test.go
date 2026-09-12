package main

import (
	"context"
	"testing"
	"time"

	"github.com/Busness-app/ky_server_base/internal/config"
)

// A deployment key that cannot seal is a configuration fault, not a run that might succeed
// next minute: the scheduler says so once and stops. Retrying would write a log line and an
// audit row every tick forever, because a RunConfig failure never reaches the point where
// recoveryclient.Run stamps the attempt that moves NextRun along.
func TestBackupLoopStopsWhenTheSealerCannotBeBuilt(t *testing.T) {
	cfg := &config.Config{}
	cfg.Security.EncryptionKey = []byte("too short")

	done := make(chan struct{})
	// A nil store is safe only because the loop must return before it reads one.
	go backupLoop(context.Background(), cfg, nil, done)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("backupLoop did not give up on an unbuildable RunConfig")
	}
}

// Shutdown waits on done before the store closes, so the loop must close it: a loop that
// never signalled would hang the process, and one that signalled early would put us back to
// killing a deposit mid-flight. done is closed only where the loop returns, which is between
// runs.
func TestBackupLoopClosesDoneOnCancel(t *testing.T) {
	cfg := &config.Config{}
	cfg.Security.EncryptionKey = make([]byte, 32)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go backupLoop(ctx, cfg, nil, done)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("backupLoop did not close done after its context was cancelled")
	}
}
