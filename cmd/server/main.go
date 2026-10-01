package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Busnes-app/ky-primitives/capsule"
	"github.com/Busnes-app/ky-primitives/password"
	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// appVersion is what the capsule manifest records for this build.
const appVersion = config.AppVersion

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
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
			runDeposit(os.Args[2:])
			return
		case "restore":
			runRestore(os.Args[2:])
			return
		case "restore-messages":
			runRestoreMessages(os.Args[2:])
			return
		case "version":
			fmt.Printf("kymessages %s\n", appVersion)
			return
		}
	}

	runServer()
}

// shutdownTimeout drains in-flight HTTP requests. Short on purpose: it is spent before the
// backup wait below, and both must fit inside the deployment's stop_grace_period.
const shutdownTimeout = 5 * time.Second

// backupWaitTimeout bounds the wait for detached backup work. recoveryclient caps one deposit
// at 15 minutes (its uploadTimeout, for a container of at most capsule.MaxContainerBytes,
// 384 MiB); the extra two minutes cover sealing and the local copy either side of the upload.
// docker-compose.yml's stop_grace_period must exceed shutdownTimeout + backupWaitTimeout, and
// TestComposeGracePeriodCoversTheShutdownBudget holds the two in step.
const backupWaitTimeout = 17 * time.Minute

func runServer() {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}
	if cfg.Backup.AllowPrivateRecovery {
		log.Printf("[BACKUP] KY_BACKUP_ALLOW_PRIVATE_RECOVERY is on: RFC1918 and CGNAT destinations admitted; loopback, link-local and other reserved addresses remain refused (HTTPS still required)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, err := store.Open(ctx, cfg.Database)
	if err != nil {
		log.Fatalf("Failed to initialize database (%s): %v", cfg.Database.Driver, err)
	}
	defer st.Close()

	// Ensure default admin user exists if database is empty
	count, _ := st.Users().CountUsers(ctx)
	if count == 0 {
		adminPass := os.Getenv("KY_ADMIN_PASSWORD")
		if adminPass == "" {
			adminPass = crypto.RandomHex(12)
			log.Printf("[SECURITY] Initial bootstrap: Created admin account. Username: admin | Password: %s", adminPass)
		}
		hash, err := password.Hash(adminPass)
		if err != nil {
			log.Fatalf("Failed to hash bootstrap admin password: %v", err)
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
			log.Fatalf("Failed to create bootstrap admin: %v", err)
		}
	}

	srv := api.NewServer(cfg, st)
	backupDone := make(chan struct{})
	go backupLoop(ctx, cfg, st, backupDone)
	maintenanceDone := make(chan struct{})
	go messagingMaintenanceLoop(ctx, st, maintenanceDone)
	backgroundDone := make(chan struct{})
	go func() { defer close(backgroundDone); <-backupDone; <-maintenanceDone }()

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
		log.Printf("[KYMESSAGES] %s listening on http://%s (DB: %s)", cfg.Server.AppName, addr, cfg.Database.Driver)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	<-stop
	log.Println("[KYMESSAGES] Shutting down gracefully...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	srv.StopMessaging()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Shutdown error: %v", err)
	}
	cancel()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), backupWaitTimeout)
	defer waitCancel()
	waitForBackupWork(waitCtx, backgroundDone, srv.WaitDetached)
	log.Println("[KYMESSAGES] Server stopped")
}

// waitForBackupWork blocks until both background loops and every detached handler have finished,
// or until ctx expires. Backup work ignores cancellation once bytes are moving: the scheduler's
// run, and the pair, pin-key and deposit handlers, all detach from their caller. They are waited
// out before the store closes, or they write into a closed store -- a key pinned on disk with no
// row recording it, or a capsule at KyRecovery with no receipt this side.
//
// Both waits start before either blocks, and both are bounded by the one context rather than a
// timer channel: a timer channel delivers its value once, so whichever wait consumed it would
// leave the other unbounded -- exactly the stuck-deposit case this is written for. Starting them
// together matters as much: waited one after the other, a hung scheduled deposit spends the whole
// budget on its own and the handler wait is read only once the deadline has already passed,
// giving a live detached handler no time at all. Past the deadline the work is abandoned and said
// so; a SIGKILL would have been silent.
func waitForBackupWork(ctx context.Context, backgroundDone <-chan struct{}, waitDetached func()) {
	handlersDone := make(chan struct{})
	go func() { defer close(handlersDone); waitDetached() }()

	select {
	case <-backgroundDone:
	default:
		log.Println("[KYMESSAGES] waiting for scheduled backup or messaging maintenance in flight...")
		select {
		case <-backgroundDone:
		case <-ctx.Done():
			log.Printf("[KYMESSAGES] abandoning background work still running after %s; a backup receipt may be unrecorded", backupWaitTimeout)
		}
	}
	select {
	case <-handlersDone:
	case <-ctx.Done():
		log.Printf("[KYMESSAGES] abandoning a detached backup handler still running after %s; its writes may be unrecorded", backupWaitTimeout)
	}
}

// backupKind is one capsule the scheduler and CLI can seal: its own schedule, receipt, local
// directory and drill checks over the pairing and key pin they share.
type backupKind struct {
	name, action string
	rc           recoveryclient.RunConfig
	settings     func(context.Context) recoveryclient.Settings
	defaultEvery time.Duration
	collect      func(context.Context) (recoveryclient.Payload, error)
	checks       func(string, capsule.Manifest) []recoveryclient.Check
}

// Built once by callers: a deployment key that cannot seal is a configuration fault, not a
// run that might succeed next minute.
func peopleKind(cfg *config.Config, st store.Store) (backupKind, error) {
	rc, err := backup.RunConfig(cfg, appVersion)
	return backupKind{name: "people", action: "admin.backup_run", rc: rc, defaultEvery: cfg.Backup.DepositInterval,
		settings: func(ctx context.Context) recoveryclient.Settings { return backup.Settings(ctx, st.Settings()) },
		collect:  func(ctx context.Context) (recoveryclient.Payload, error) { return backup.Collect(ctx, cfg, appVersion) },
		checks:   backup.Checks}, err
}

// The messages capsule is opt-in: no env default, so it stays off until an admin sets an interval.
func messagesKind(cfg *config.Config, st store.Store) (backupKind, error) {
	rc, err := backup.MessagesRunConfig(cfg, appVersion)
	return backupKind{name: "messages", action: backup.MessagesRunAction, rc: rc,
		settings: func(ctx context.Context) recoveryclient.Settings { return backup.MessagesSettings(ctx, st.Settings()) },
		collect: func(ctx context.Context) (recoveryclient.Payload, error) {
			return backup.CollectMessages(ctx, cfg, appVersion)
		},
		checks: backup.MessagesChecks}, err
}

// runKind is recoveryclient.Run for one kind; tests replace it.
var runKind = func(ctx context.Context, k backupKind, s recoveryclient.Settings, client recoveryclient.Depositor) (recoveryclient.Result, error) {
	return recoveryclient.Run(ctx, k.rc, s, func() (recoveryclient.Payload, error) { return k.collect(ctx) }, client)
}

// backupLoop polls the admin's schedule once a minute; a change in the UI needs no restart
// and a restart never loses its place, the last attempt is in the database. The wait honours
// shutdown; the run does not, and done is closed only once the loop is between runs, so
// SIGTERM cannot land between KyRecovery storing a capsule and the receipt being written.
func backupLoop(ctx context.Context, cfg *config.Config, st store.Store, done chan<- struct{}) {
	defer close(done)
	// Run never gets far enough to stamp the attempt on a RunConfig failure, so retrying
	// would log and audit it every tick forever.
	people, err := peopleKind(cfg, st)
	if err != nil {
		log.Printf("[BACKUP] scheduler disabled: %v", err)
		return
	}
	messages, err := messagesKind(cfg, st)
	if err != nil {
		log.Printf("[BACKUP] scheduler disabled: %v", err)
		return
	}
	kinds := []backupKind{people, messages}
	client := recoveryclient.NewClient(recoveryclient.Options{AllowPrivate: cfg.Backup.AllowPrivateRecovery})
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		backupTick(ctx, cfg, st, kinds, client)
	}
}

// backupTick runs each due kind in order. A kind whose run finds the library lock held (an admin
// run) is left unstamped and so still due; the next tick retries it.
func backupTick(ctx context.Context, cfg *config.Config, st store.Store, kinds []backupKind, client recoveryclient.Depositor) {
	runCtx := context.WithoutCancel(ctx)
	for _, k := range kinds {
		if ctx.Err() != nil {
			return // shutdown: the wait budget covers one run; later kinds stay due
		}
		next, on, err := recoveryclient.NextRun(k.defaultEvery, k.settings(runCtx))
		if err != nil {
			log.Printf("[BACKUP] %s schedule unreadable: %s", k.name, recoveryclient.AuditSafe(err.Error()))
			continue
		}
		if !on || time.Now().Before(next) {
			continue
		}
		res, err := runKind(runCtx, k, k.settings(runCtx), client)
		if errors.Is(err, recoveryclient.ErrNotPaired) || errors.Is(err, recoveryclient.ErrNoDestination) {
			continue // never configured; nothing to report
		}
		if errors.Is(err, recoveryclient.ErrInProgress) {
			log.Printf("[BACKUP] %s: another run is in progress; retrying next tick", k.name)
			continue
		}
		recordRun(runCtx, st, "system", k.action, res, err)
	}
}

// recordRun audits one run the same way the admin route does, under the actor that started it.
// action names the kind; the library's own action is the people capsule's.
func recordRun(ctx context.Context, st store.Store, actor, action string, res recoveryclient.Result, err error) {
	_, outcome, details := recoveryclient.Outcome(res, err)
	details["outcome"] = outcome
	_ = st.Audit().LogAudit(ctx, &store.AuditRecord{UserID: actor, Action: action,
		Resource: res.Manifest.CapsuleID, Details: api.AuditDetails(details)})
	if err != nil {
		log.Printf("[BACKUP] %s: %s", actor, recoveryclient.AuditSafe(err.Error()))
		return
	}
	log.Printf("[BACKUP] %s: capsule %s (%d bytes) local=%q deposited=%t", actor, res.Manifest.CapsuleID, res.SizeBytes, res.LocalPath, res.Receipt != nil)
}

// cliKind parses -messages and returns the chosen kind.
func cliKind(name string, args []string, cfg *config.Config, st store.Store) backupKind {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	messages := fs.Bool("messages", false, "back up the messages capsule instead of the people capsule")
	_ = fs.Parse(args)
	build := peopleKind
	if *messages {
		build = messagesKind
	}
	k, err := build(cfg, st)
	if err != nil {
		log.Fatalf("Backup: %v", err)
	}
	return k
}

// runDeposit seals and delivers one capsule now, for cron or an operator at a shell.
func runDeposit(args []string) {
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

	k := cliKind("deposit", args, cfg, st)
	client := recoveryclient.NewClient(recoveryclient.Options{AllowPrivate: cfg.Backup.AllowPrivateRecovery})
	res, err := runKind(ctx, k, k.settings(ctx), client)
	recordRun(ctx, st, "cli", k.action, res, err)
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

	hash, err := password.Hash(*passwordFlag)
	if err != nil {
		log.Fatalf("Password hashing error: %v", err)
	}

	existing, err := st.Users().GetLocalUserByUsername(ctx, *username)
	if err == nil && existing != nil {
		if err := st.Users().ResetAdminPassword(ctx, existing.ID, hash); err != nil {
			log.Fatalf("Failed to update admin: %v", err)
		}
		log.Printf("✓ Admin user %q password successfully reset", *username)
		return
	}

	user := &store.User{
		ID:                 fmt.Sprintf("usr_%s", crypto.RandomHex(12)),
		Username:           *username,
		DisplayName:        "Administrator",
		PasswordHash:       hash,
		Role:               "admin",
		Status:             "active",
		SSOProvider:        "local",
		MustChangePassword: true,
	}

	if err := st.Users().CreateUser(ctx, user); err != nil {
		log.Fatalf("Failed to create admin: %v", err)
	}
	log.Printf("✓ Admin user %q created successfully", *username)
}

// collectFiles is what every CLI seal uses; the sealed-only members are safe here and nowhere else.
func collectFiles(ctx context.Context, cfg *config.Config) recoveryclient.Payload {
	payload, err := backup.Collect(ctx, cfg, appVersion)
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

	k := cliKind("backup-drill", args, cfg, st)
	payload, err := k.collect(ctx)
	if err != nil {
		log.Fatalf("Failed to collect backup files: %v", err)
	}
	result, err := backup.RunDrill(ctx, cfg, payload, k.checks)
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

	key, err := recoveryclient.LoadRecoveryKey(cfg.Database.DataDir, backup.Settings(ctx, st.Settings()))
	if err != nil {
		log.Fatalf("Recovery key: %v", err)
	}
	raw, m, err := recoveryclient.Seal(collectFiles(ctx, cfg), key)
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

// stdinIsTerminal reports whether a human is typing, so a pipeline gets no stray prompt.
func stdinIsTerminal() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func runRestore(args []string) {
	restoreCommand(args, "restore", "to", "empty directory to restore into", "", restore)
}

// restoreMessagesNote is in the usage text because the command trusts the operator here.
const restoreMessagesNote = "Stop the server first. This command cannot detect a running server; it only refuses\n" +
	"a target that already holds messaging data.\n\n"

func runRestoreMessages(args []string) {
	restoreCommand(args, "restore-messages", "into", "directory a people restore wrote", restoreMessagesNote, func(capsulePath, targetDir, expectService string, shares []string, stdout io.Writer) error {
		// SIGINT/SIGTERM cancel the import and return through the cleanup of the opened
		// capsule instead of killing the process with plaintext on disk.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return restoreMessages(ctx, capsulePath, targetDir, expectService, shares, stdout)
	})
}

// restoreCommand parses a restore command's flags and reads custodian shares from stdin.
func restoreCommand(args []string, name, dirFlag, dirUsage, note string, run func(capsulePath, targetDir, expectService string, shares []string, stdout io.Writer) error) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	capsulePath := fs.String("capsule", "", "path to the .kycap file")
	target := fs.String(dirFlag, "", dirUsage)
	service := fs.String("service", "", "expected service name (default: $KY_APP_NAME)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: kymessages %s -capsule <file.kycap> -%s <dir> [-service <name>]\n\n"+
			"Custodian shares are read from stdin, one ky2-... share per line, and never from\n"+
			"the command line: argv is world-readable and lands in shell history.\n\n%s", name, dirFlag, note)
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
	if err := run(*capsulePath, *target, *service, shares, os.Stdout); err != nil {
		log.Fatalf("%s failed: %v", name, err)
	}
}
