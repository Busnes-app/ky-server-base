package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Busnes-app/ky-primitives/password"
	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/worker"
)

// appVersion is what the capsule manifest records for this build.
const appVersion = "1.0.0"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "migrate-postgres":
			if err := migratePostgres(context.Background(), os.Args[2:], os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
				log.Fatal(err)
			}
			return
		case "accept-replica-config":
			runAcceptReplicaConfig()
			return
		case "init-admin":
			runInitAdmin(os.Args[2:])
			return
		case "backup-drill":
			runBackupDrill(os.Args[2:])
			return
		case "export-capsule":
			runExportCapsule(os.Args[2:])
			return
		case "deposit":
			runDeposit()
			return
		case "restore":
			runRestore(os.Args[2:])
			return
		case "version":
			fmt.Println("ky_server_base v1.0.0 (Busnes.app base platform)")
			return
		}
	}

	runServer()
}

// readinessDrainDelay is how long a stopping server keeps serving with /readyz at 503: five
// one-second proxy probes. Part of the shutdown budget below.
const readinessDrainDelay = 5 * time.Second

// shutdownTimeout drains in-flight HTTP requests. Backup work is cancelled before the drain
// starts, so an interrupted deposit request can still answer inside it.
const shutdownTimeout = 5 * time.Second

// backupWaitTimeout bounds the wait for cancelled backup work to unwind. The snapshot and the
// upload stop at once; what may still be running is the sealing and local copy of one capsule
// already collected, and the database writes that record the outcome. With the drain delay the
// three must stay inside Kubernetes' default 30s termination grace and docker-compose.yml's
// stop_grace_period; TestShutdownBudgetFitsTheGracePeriods holds them in step.
const backupWaitTimeout = 10 * time.Second

func runServer() {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}
	if cfg.Database.Driver == "sqlite" {
		path, err := store.SQLitePath(cfg.Database.DSN)
		if err != nil {
			log.Fatal(err)
		}
		won, err := store.WithSQLiteInstance(context.Background(), path, func(context.Context) error {
			runConfiguredServer(cfg)
			return nil
		})
		if err != nil {
			log.Fatalf("SQLite instance lock: %v", err)
		}
		if !won {
			log.Fatal("SQLite already has a server or migration running; stop it first. Use PostgreSQL for replicas.")
		}
		return
	}
	runConfiguredServer(cfg)
}

// bootstrap runs once per start under the shared "bootstrap" lock: the first admin on an empty
// database, then the replica configuration check on PostgreSQL.
func bootstrap(ctx context.Context, cfg *config.Config, st store.Store) error {
	for {
		won, err := st.Coordination().WithLock(ctx, "bootstrap", func(ctx context.Context) error {
			if err := ensureBootstrapAdmin(ctx, st); err != nil {
				return err
			}
			if st.Driver() != "postgres" {
				return nil
			}
			return checkReplicaConfig(ctx, cfg, st.Settings())
		})
		if err != nil || won {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// prepareStart is everything that must pass before the server listens: bootstrap and the
// replica configuration check, removal of plaintext snapshots a killed process left, and the
// recovery key. A recovery.pub naming another key refuses the start; a pin no replica can
// load is repaired from the screen, so it is logged and the server starts.
func prepareStart(ctx context.Context, cfg *config.Config, st store.Store) error {
	if err := bootstrap(ctx, cfg, st); err != nil {
		return fmt.Errorf("bootstrap failed: %w", err)
	}
	if n, err := backup.RemoveStaleSnapshots(cfg.Database.DataDir); err != nil {
		log.Printf("[BACKUP] leftover snapshot scratch under %s not removed: %v", cfg.Database.DataDir, err)
	} else if n > 0 {
		log.Printf("[BACKUP] removed %d snapshot scratch directories a stopped process left", n)
	}
	err := backup.SyncRecoveryKey(ctx, cfg, st)
	if errors.Is(err, backup.ErrRecoveryKeyUnavailable) {
		log.Printf("[BACKUP] %s", recoveryclient.AuditSafe(err.Error()))
		return nil
	}
	if err != nil {
		return fmt.Errorf("recovery key: %w (recovery.pub must be the key pinned in the database; remove a stale file to have it rewritten; for the replica overlay see the app-volume repair in docs/REPLICAS.md)", err)
	}
	return nil
}

// ensureBootstrapAdmin creates the default admin if the database has no users.
func ensureBootstrapAdmin(ctx context.Context, st store.Store) error {
	count, err := st.Users().CountUsers(ctx)
	if err != nil || count != 0 {
		return err
	}
	adminPass := os.Getenv("KY_ADMIN_PASSWORD")
	if adminPass == "" {
		adminPass = crypto.RandomHex(12)
		log.Printf("[SECURITY] Initial bootstrap: Created admin account. Username: admin | Password: %s", adminPass)
	}
	hash, err := password.Hash(adminPass)
	if err != nil {
		return fmt.Errorf("hash bootstrap password: %w", err)
	}
	if err := st.Users().CreateUser(ctx, &store.User{
		ID:                 fmt.Sprintf("usr_%s", crypto.RandomHex(12)),
		Username:           "admin",
		DisplayName:        "Administrator",
		PasswordHash:       hash,
		Role:               "admin",
		Status:             "active",
		SSOProvider:        "local",
		MustChangePassword: true,
	}); err != nil {
		return fmt.Errorf("create bootstrap admin: %w", err)
	}
	return nil
}

func runConfiguredServer(cfg *config.Config) {
	if cfg.Backup.AllowPrivateRecovery {
		log.Printf("[BACKUP] KY_BACKUP_ALLOW_PRIVATE_RECOVERY is on: RFC1918 and CGNAT destinations admitted; loopback, link-local and other reserved addresses remain refused (HTTPS still required)")
	}
	// An https app URL usually means a TLS proxy in front. Without KY_TRUSTED_PROXIES every
	// visitor shares the proxy's address, so sign-in limits and lockouts apply to all of them.
	if strings.HasPrefix(strings.ToLower(cfg.Server.AppURL), "https://") && len(cfg.Security.TrustedProxies) == 0 {
		log.Printf("[SECURITY] KY_APP_URL is https but KY_TRUSTED_PROXIES is empty: behind a reverse proxy every client counts as the proxy's address, so one client's failed sign-ins lock the account for everyone. Set KY_TRUSTED_PROXIES to the proxy's address.")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, err := store.Open(ctx, cfg.Database)
	if err != nil {
		log.Fatalf("Failed to initialize database (%s): %v", cfg.Database.Driver, err)
	}
	defer st.Close()

	if err := prepareStart(ctx, cfg, st); err != nil {
		log.Fatal(err)
	}

	srv := api.NewServer(cfg, st)
	backupCtx, stopBackups := context.WithCancel(ctx)
	defer stopBackups()
	backupDone := make(chan struct{})
	go backupLoop(backupCtx, cfg, st, recoveryclient.NewClient(recoveryclient.Options{AllowPrivate: cfg.Backup.AllowPrivateRecovery}), time.Minute, backupDone)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	httpServer := &http.Server{
		Addr:         addr,
		Handler:      srv,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	// Graceful shutdown channel
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("[KY-BASE] %s listening on http://%s (DB: %s)", cfg.Server.AppName, addr, cfg.Database.Driver)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	<-stop
	gracefulStop(srv, httpServer, stopBackups, cancel, backupDone, readinessDrainDelay)
}

// gracefulStop is the SIGTERM order: /readyz turns 503, backup work is cancelled, the server
// keeps serving for drain so a proxy probing every second takes this replica out of rotation,
// then the listener drains in-flight requests and the cancelled backup work unwinds.
func gracefulStop(srv *api.Server, httpServer *http.Server, stopBackups, cancel func(), backupDone <-chan struct{}, drain time.Duration) {
	srv.StartDraining()
	log.Printf("[KY-BASE] draining: /readyz answers 503; serving %s more before closing the listener", drain)

	// Backups first: a snapshot or upload in flight is aborted now and runs again after the
	// restart, so the waits below take seconds whatever the backup was doing.
	stopBackups()
	srv.StopDetached()
	time.Sleep(drain)
	log.Println("[KY-BASE] Shutting down gracefully...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Shutdown error: %v", err)
	}
	cancel()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), backupWaitTimeout)
	defer waitCancel()
	waitForBackupWork(waitCtx, backupDone, srv.WaitDetached)
	log.Println("[KY-BASE] Server stopped")
}

// waitForBackupWork blocks until the scheduler loop and every detached handler have finished,
// or until ctx expires. Their slow steps are already cancelled; what is waited out here is the
// unwinding, so the receipt, key pin and audit rows are written before the store closes.
//
// Both waits start before either blocks, and both are bounded by the one context rather than a
// timer channel: a timer channel delivers its value once, so whichever wait consumed it would
// leave the other unbounded. Starting them together matters as much: waited one after the
// other, a stuck scheduled run spends the whole budget and a live detached handler gets no
// time at all. Past the deadline the work is abandoned and said so; a SIGKILL would have been
// silent.
func waitForBackupWork(ctx context.Context, backupDone <-chan struct{}, waitDetached func()) {
	handlersDone := make(chan struct{})
	go func() { defer close(handlersDone); waitDetached() }()

	select {
	case <-backupDone:
	default:
		log.Println("[KY-BASE] waiting for the interrupted scheduled backup to unwind...")
		select {
		case <-backupDone:
		case <-ctx.Done():
			log.Printf("[KY-BASE] abandoning a scheduled backup still unwinding after %s; its outcome may be unrecorded", backupWaitTimeout)
		}
	}
	select {
	case <-handlersDone:
	case <-ctx.Done():
		log.Printf("[KY-BASE] abandoning a detached backup handler still unwinding after %s; its writes may be unrecorded", backupWaitTimeout)
	}
}

// runBackup seals one capsule and delivers it to every configured destination, for the CLI:
// it builds its own RunConfig because a one-shot failure is reported to the operator and ends.
func runBackup(ctx context.Context, cfg *config.Config, st store.Store) (recoveryclient.Result, error) {
	rc, err := backup.RunConfig(cfg, appVersion)
	if err != nil {
		return recoveryclient.Result{}, err
	}
	client := recoveryclient.NewClient(recoveryclient.Options{AllowPrivate: cfg.Backup.AllowPrivateRecovery})
	return backup.RunOwned(ctx, cfg, rc, st, client)
}

// backupLoop polls the admin's schedule every tick; a change in the UI needs no restart and a
// restart never loses its place, the last attempt is in the database. Cancelling ctx stops the
// wait and interrupts a run in flight (backup.Run); done is closed once the loop has returned,
// after the interrupted run's outcome is recorded.
func backupLoop(ctx context.Context, cfg *config.Config, st store.Store, client recoveryclient.Depositor, tick time.Duration, done chan<- struct{}) {
	defer close(done)
	// Built once: a deployment key that cannot seal is a configuration fault, not a run that
	// might succeed next minute. Run never gets far enough to stamp the attempt, so retrying
	// would log and audit a failure every tick forever.
	rc, err := backup.RunConfig(cfg, appVersion)
	if err != nil {
		log.Printf("[BACKUP] scheduler disabled: %v", err)
		return
	}
	if ctx.Err() != nil {
		return
	}
	worker.Run(ctx, st.Coordination(), tick, func(ctx context.Context) {
		if err := st.Coordination().CleanExpired(ctx); err != nil {
			log.Printf("[WORKER] security cleanup: %v", err)
		}
		if err := st.Sessions().CleanExpiredSessions(ctx); err != nil {
			log.Printf("[WORKER] session cleanup: %v", err)
		}
		_, err := st.Coordination().WithLock(ctx, "backup-run", func(ctx context.Context) error {
			// Read the schedule after winning, so a waiter cannot run a stale due job.
			next, on, err := recoveryclient.NextRun(cfg.Backup.DepositInterval, backup.Settings(ctx, st.Settings()))
			if err != nil || !on || time.Now().Before(next) {
				return err
			}
			res, err := backup.Run(ctx, cfg, rc, st, client)
			if !errors.Is(err, recoveryclient.ErrNotPaired) && !errors.Is(err, recoveryclient.ErrNoDestination) {
				recordRun(context.WithoutCancel(ctx), st, "system", res, err)
			}
			return nil
		})
		if err != nil {
			log.Printf("[WORKER] scheduled backup: %v", err)
		}
	})
}

// recordRun audits one run the same way the admin route does, under the actor that started it.
func recordRun(ctx context.Context, st store.Store, actor string, res recoveryclient.Result, err error) {
	action, outcome, details := recoveryclient.Outcome(res, err)
	details["outcome"] = outcome
	_ = st.Audit().LogAudit(ctx, &store.AuditRecord{UserID: actor, Action: action,
		Resource: res.Manifest.CapsuleID, Details: api.AuditDetails(details)})
	if err != nil {
		log.Printf("[BACKUP] %s: %s", actor, recoveryclient.AuditSafe(err.Error()))
		return
	}
	log.Printf("[BACKUP] %s: capsule %s (%d bytes) local=%q deposited=%t", actor, res.Manifest.CapsuleID, res.SizeBytes, res.LocalPath, res.Receipt != nil)
}

// runDeposit seals and delivers one capsule now, for cron or an operator at a shell.
func runDeposit() {
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

	res, err := runBackup(ctx, cfg, st)
	recordRun(ctx, st, "cli", res, err)
	if err != nil {
		log.Fatalf("Backup: %v", err)
	}
	if res.Receipt != nil {
		log.Printf("✓ Capsule %s deposited at %s; digest %s", res.Manifest.CapsuleID, res.Receipt.DepositedAt.Format(time.RFC3339), res.Receipt.Digest)
	}
}

func runInitAdmin(args []string) {
	fs := flag.NewFlagSet("init-admin", flag.ExitOnError)
	username := fs.String("username", "admin", "Admin username")
	passwordFlag := fs.String("password", "", "Admin password (minimum 12 characters)")
	promote := fs.Bool("promote", false, "Allow resetting an existing account that is not an active local administrator, making it one")
	_ = fs.Parse(args)

	if *passwordFlag == "" || len(*passwordFlag) < 12 {
		log.Fatal("Error: -password is required and must be at least 12 characters")
	}

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

	msg, err := initAdmin(ctx, st, *username, *passwordFlag, *promote, os.Stderr)
	if err != nil {
		log.Fatal(err)
	}
	log.Print(msg)
}

// initAdmin creates or resets a local administrator. It never silently grants admin: an
// existing account that is not an active administrator needs promote, and non-local accounts
// are refused. The previous role and status are printed to out before anything changes.
func initAdmin(ctx context.Context, st store.Store, username, pass string, promote bool, out io.Writer) (string, error) {
	hash, err := password.Hash(pass)
	if err != nil {
		return "", fmt.Errorf("password hashing error: %w", err)
	}
	existing, err := st.Users().GetUserByUsername(ctx, username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", fmt.Errorf("DB error: %w", err)
	}
	if existing != nil {
		fmt.Fprintf(out, "Existing account %q: role=%s status=%s provider=%s\n", existing.Username, existing.Role, existing.Status, existing.SSOProvider)
		if existing.SSOProvider != "local" {
			return "", fmt.Errorf("refusing: %q is a %s account; init-admin manages local accounts only", existing.Username, existing.SSOProvider)
		}
		if !promote && (existing.Role != "admin" || existing.Status != "active") {
			return "", fmt.Errorf("refusing: %q is not an active administrator; check the username, or rerun with -promote to make it one", existing.Username)
		}
		if err := st.Users().ResetAdminPassword(ctx, existing.ID, hash); err != nil {
			return "", fmt.Errorf("failed to update admin: %w", err)
		}
		return fmt.Sprintf("✓ Admin user %q password successfully reset", existing.Username), nil
	}
	user := &store.User{
		ID:                 fmt.Sprintf("usr_%s", crypto.RandomHex(12)),
		Username:           username,
		DisplayName:        "Administrator",
		PasswordHash:       hash,
		Role:               "admin",
		Status:             "active",
		SSOProvider:        "local",
		MustChangePassword: true,
	}
	if err := st.Users().CreateUser(ctx, user); err != nil {
		return "", fmt.Errorf("failed to create admin: %w", err)
	}
	return fmt.Sprintf("✓ Admin user %q created successfully", username), nil
}

// collectFiles is what every CLI seal uses; the sealed-only members are safe here and nowhere else.
func collectFiles(ctx context.Context, cfg *config.Config, st store.Store) recoveryclient.Payload {
	payload, err := backup.Collect(ctx, cfg, st, appVersion)
	if err != nil {
		log.Fatalf("Failed to collect backup files: %v", err)
	}
	return payload
}

func runBackupDrill(args []string) {
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

	payload := collectFiles(ctx, cfg, st)
	result, err := backup.RunDrill(ctx, cfg, payload)
	if err != nil {
		log.Fatalf("Drill execution error: %v", err)
	}

	fmt.Printf("\n=== Feature 0: KyBackup Restore Drill Summary ===\n")
	fmt.Printf("Status:   %s\n", map[bool]string{true: "PASSED (OK)", false: "FAILED"}[result.Passed])
	fmt.Printf("Duration: %d ms\n", result.DurationMs)
	for _, check := range result.Checks {
		status := "✓"
		if !check.Passed {
			status = "✗"
		}
		fmt.Printf("  [%s] %s: %s\n", status, check.Name, check.Message)
	}
	fmt.Println("==================================================")
	if !result.Passed {
		os.Exit(1) // a recovery check a script cannot see fail is not a check
	}
}

func runExportCapsule(args []string) {
	fs := flag.NewFlagSet("export-capsule", flag.ExitOnError)
	out := fs.String("out", "", "output path (default <capsule-id>.kycap in the current directory)")
	_ = fs.Parse(args)

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

	settings := backup.Settings(ctx, st.Settings())
	if err := backup.SyncRecoveryKey(ctx, cfg, st); err != nil {
		log.Fatalf("Recovery key: %v", err)
	}
	key, err := recoveryclient.LoadRecoveryKey(cfg.Database.DataDir, settings)
	if err != nil {
		log.Fatalf("Recovery key: %v", err)
	}
	raw, m, err := recoveryclient.Seal(collectFiles(ctx, cfg, st), key)
	if err != nil {
		log.Fatalf("Seal: %v", err)
	}
	path := *out
	if path == "" {
		path = recoveryclient.FilenameSafe(m.CapsuleID) + ".kycap"
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		log.Fatalf("Write: %v", err)
	}
	log.Printf("✓ Capsule %s sealed to recovery key %s, written to %s (%d bytes)", m.CapsuleID, m.RecoveryKeyID, path, len(raw))
}

// restore is the product-side half of the ceremony, owned by the lib: k custodian shares
// combined, used once, dropped; a capsule from another service refused before the key is
// touched; the authenticated manifest printed for comparison with KyRecovery's record.
// The restored copy has scheduled deposits off: it may be a drill or a migration beside a
// server still in service, and two servers depositing under one pairing alternate
// KyRecovery's newest capsule between diverging databases. Its sessions are deleted (every
// bearer credential minted before the capsule) and the replica fingerprint is dropped, so a
// restore with rotated settings records its own.
func restore(capsulePath, targetDir, expectService string, shares []string, stdout io.Writer) error {
	if err := recoveryclient.Restore(capsulePath, targetDir, expectService, shares, stdout); err != nil {
		return err
	}
	dbPath := filepath.Join(targetDir, "data", "ky_server.db")
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	sessions, err := prepareRestoredDatabase(dbPath)
	if err != nil {
		return fmt.Errorf("files restored, but %s could not be made safe to start (do not start it until it is): %w", dbPath, err)
	}
	fmt.Fprintln(stdout, "Scheduled deposits are OFF in the restored copy. Turn them back on in Backup & recovery once this server is the one in service.")
	fmt.Fprintf(stdout, "Sessions deleted from the restored copy: %d. Everyone signs in again.\n", sessions)
	fmt.Fprintln(stdout, "Replica configuration record removed from the restored copy; its first PostgreSQL start records a new one.")
	return nil
}

// prepareRestoredDatabase, in one transaction, writes the "off" schedule (recoveryclient's
// backup_interval_sec = 0) the admin screen writes, so the database setting wins over
// KY_BACKUP_DEPOSIT_INTERVAL; deletes every session; and deletes the replica fingerprint.
// A product adds its own session-like tables here.
func prepareRestoredDatabase(dbPath string) (int64, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO server_settings (key, value, updated_at) VALUES ('backup_interval_sec', '0', ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, time.Now().UTC()); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`DELETE FROM sessions`)
	if err != nil {
		return 0, err
	}
	sessions, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM server_settings WHERE key = ?`, config.ReplicaConfigSetting); err != nil {
		return 0, err
	}
	return sessions, tx.Commit()
}

// stdinIsTerminal reports whether a human is typing, so a pipeline gets no stray prompt.
func stdinIsTerminal() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func runRestore(args []string) {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	capsulePath := fs.String("capsule", "", "path to the .kycap file")
	target := fs.String("to", "", "empty directory to restore into")
	service := fs.String("service", "", "expected service name (default: $KY_APP_NAME)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage: ky_server_base restore -capsule <file.kycap> -to <dir> [-service <name>]\n\n"+
			"Custodian shares are read from stdin, one ky2-... share per line, and never from\n"+
			"the command line: argv is world-readable and lands in shell history.\n\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if *capsulePath == "" || *target == "" {
		fs.Usage()
		os.Exit(2)
	}
	if *service == "" {
		// Not config.LoadFromEnv: it mints <DataDir>/encryption.key as a side effect, and a
		// recovery host has no business growing a key of its own mid-ceremony.
		*service = os.Getenv("KY_APP_NAME")
	}
	if *service == "" {
		*service = config.DefaultAppName
	}
	if *service == "" {
		log.Fatal("Error: -service is required when KY_APP_NAME is not set")
	}

	if stdinIsTerminal() {
		fmt.Fprintln(os.Stderr, "Paste custodian shares, one per line, then Ctrl-D:")
	}
	shares, err := recoveryclient.ReadShares(os.Stdin)
	if err != nil {
		log.Fatalf("Reading shares: %v", err)
	}
	if len(shares) == 0 {
		log.Fatal("Error: no custodian shares on stdin")
	}
	if err := restore(*capsulePath, *target, *service, shares, os.Stdout); err != nil {
		log.Fatalf("Restore failed: %v", err)
	}
}
