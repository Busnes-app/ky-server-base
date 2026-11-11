package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky-primitives/recoverykey"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
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
	go backupLoop(context.Background(), cfg, nil, nil, time.Minute, done)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("backupLoop did not give up on an unbuildable RunConfig")
	}
}

// Shutdown waits on done before the store closes, so the loop must close it: a loop that
// never signalled would spend the whole backup wait on every stop.
func TestBackupLoopClosesDoneOnCancel(t *testing.T) {
	cfg := &config.Config{}
	cfg.Security.EncryptionKey = make([]byte, 32)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go backupLoop(ctx, cfg, nil, nil, time.Minute, done)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("backupLoop did not close done after its context was cancelled")
	}
}

// Stopping must fit the grace period a supervisor grants without being asked: Kubernetes
// gives 30s by default, and the compose file must grant more than the budget because Docker's
// default is 10s. Past either, SIGKILL lands before an interrupted backup has recorded its
// outcome.
func TestShutdownBudgetFitsTheGracePeriods(t *testing.T) {
	budget := shutdownTimeout + backupWaitTimeout
	if limit := 20 * time.Second; budget > limit {
		t.Errorf("shutdownTimeout+backupWaitTimeout is %s; keep it within %s so a 30s termination grace has margin", budget, limit)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*stop_grace_period:\s*(\S+)\s*$`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("docker-compose.yml sets no stop_grace_period, and Docker's default 10s is shorter than the shutdown budget")
	}
	grace, err := time.ParseDuration(string(m[1]))
	if err != nil {
		t.Fatalf("stop_grace_period %q: %v", m[1], err)
	}
	if grace <= budget {
		t.Errorf("stop_grace_period %s does not cover shutdownTimeout+backupWaitTimeout (%s)", grace, budget)
	}
}

// Both wait phases share one context, not one timer channel: a timer channel delivers its value
// once, so a scheduler wait that consumed it would leave the handler wait unbounded. Neither channel here ever closes, so the only way out
// is the deadline, twice.
func TestWaitForBackupWorkIsBoundedInBothPhases(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		waitForBackupWork(ctx, make(chan struct{}), func() { select {} })
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("waitForBackupWork did not return; the second phase outlived the shared deadline")
	}
}

// The two waits run concurrently, not one after the other. A scheduled deposit that hangs eats
// the whole shared budget, and a sequential handler wait would then start already past the
// deadline and give a live detached handler zero time -- the store closes under the pin or the
// deposit it exists to protect. Starting both before either blocks costs nothing and means the
// handler wait has run for the whole budget by the time it is read.
func TestWaitForBackupWorkWaitsBothAtOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var finished atomic.Bool
	// backupDone never closes: the scheduled run is the one that hangs.
	waitForBackupWork(ctx, make(chan struct{}), func() {
		time.Sleep(10 * time.Millisecond)
		finished.Store(true)
	})
	if !finished.Load() {
		t.Fatal("the detached-handler wait had not run when waitForBackupWork returned; a hung scheduled deposit consumed its whole budget")
	}
}

// RESTORE.md rotates these by editing .env and running `docker compose up -d`; that only
// works if the base service forwards them.
func TestComposeForwardsRotatableSecrets(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"KY_SESSION_SECRET", "KY_SCIM_TOKEN"} {
		if !regexp.MustCompile(`(?m)^\s*- ` + name + `=\$\{` + name + `:-\}\s*$`).Match(raw) {
			t.Errorf("docker-compose.yml does not forward %s from .env", name)
		}
	}
	// Every other documented setting (KY_KYSIGNON_*, KY_TRUSTED_PROXIES, ...) reaches the
	// container through .env itself, which an install may not have yet.
	if !regexp.MustCompile(`(?m)^    env_file:\n      - path: \.env\n        required: false$`).Match(raw) {
		t.Error("docker-compose.yml does not read optional .env into the app's environment")
	}
}

// stalledDepositor is an upload that never completes on its own: like the real client's HTTP
// request, it returns only when its context ends.
type stalledDepositor struct{ entered chan struct{} }

func (d stalledDepositor) Deposit(ctx context.Context, _, _ string, _ []byte) (recoveryclient.Receipt, error) {
	close(d.entered)
	<-ctx.Done()
	return recoveryclient.Receipt{}, fmt.Errorf("%w: %w", recoveryclient.ErrRemote, ctx.Err())
}

// SIGTERM during a scheduled deposit: the loop stops within seconds, not after the upload
// budget, the interruption is audited before done closes (the store closes next), and the
// run is due again for the next start.
func TestBackupLoopInterruptsAScheduledDepositOnCancel(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Server.AppName = "busnes_app"
	cfg.Database.Driver = "sqlite"
	cfg.Database.DataDir = dir
	cfg.Database.DSN = filepath.Join(dir, "ky_server.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	cfg.Security.EncryptionKey = bytes.Repeat([]byte{1}, 32)
	cfg.Backup.DepositInterval = time.Hour
	st, err := store.Open(context.Background(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	settings := backup.Settings(context.Background(), st.Settings())
	priv, _ := recoverykey.Generate()
	if err := recoveryclient.StoreRecoveryKey(dir, settings, recoveryclient.RecoveryKey{Public: priv.Public(), Threshold: 2, TotalShares: 3}); err != nil {
		t.Fatal(err)
	}
	sealer, err := backup.NewSealer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := recoveryclient.StorePairing(settings, sealer, "https://recovery.busnes.app", "kyrec_live_t"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	depositor := stalledDepositor{entered: make(chan struct{})}
	done := make(chan struct{})
	go backupLoop(ctx, cfg, st, depositor, 10*time.Millisecond, done)
	select {
	case <-depositor.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the scheduler never started the due deposit")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("backupLoop was still running two seconds after cancel; shutdown would wait out the upload")
	}

	records, _, err := st.Audit().ListAuditRecords(context.Background(), 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var audited bool
	for _, rec := range records {
		if rec.UserID == "system" && rec.Action == "admin.backup_run" && strings.Contains(rec.Details, "interrupted by shutdown") {
			audited = true
		}
	}
	if !audited {
		t.Error("the interrupted scheduled deposit left no audit row")
	}
	if _, ok, _ := recoveryclient.LastDeposit(settings); ok {
		t.Error("an interrupted deposit recorded a receipt")
	}
	if next, on, err := recoveryclient.NextRun(cfg.Backup.DepositInterval, settings); err != nil || !on || time.Now().Before(next) {
		t.Errorf("after the interruption the next run is at %v (on=%v err=%v), want due now", next, on, err)
	}
}
