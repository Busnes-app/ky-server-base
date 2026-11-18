package worker_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
	"github.com/Busnes-app/ky_server_base/internal/worker"
)

func TestStandbyTakesOverScheduledWork(t *testing.T) {
	cfg := testdb.Config(t)
	a, err := store.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := store.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	firstCtx, stopFirst := context.WithCancel(ctx)
	defer stopFirst()
	first := make(chan struct{}, 1)
	second := make(chan struct{}, 1)
	firstDone := make(chan struct{})
	secondDone := make(chan struct{})
	var active atomic.Int32
	var overlap atomic.Bool
	job := func(ch chan struct{}) func(context.Context) {
		return func(ctx context.Context) {
			if active.Add(1) != 1 {
				overlap.Store(true)
			}
			defer active.Add(-1)
			select {
			case ch <- struct{}{}:
			default:
			}
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	go func() {
		defer close(firstDone)
		worker.Run(firstCtx, a.Coordination(), 10*time.Millisecond, job(first))
	}()
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("first worker never ran")
	}
	go func() { defer close(secondDone); worker.Run(ctx, b.Coordination(), 10*time.Millisecond, job(second)) }()
	select {
	case <-second:
		t.Error("standby ran while owner was alive")
	case <-time.After(50 * time.Millisecond):
	}
	stopFirst()
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("first worker did not stop")
	}
	select {
	case <-second:
	case <-time.After(5 * time.Second):
		t.Fatal("standby did not take over")
	}
	stop()
	select {
	case <-secondDone:
	case <-time.After(5 * time.Second):
		t.Fatal("second worker did not stop")
	}
	if overlap.Load() {
		t.Fatal("scheduled jobs overlapped")
	}
}
