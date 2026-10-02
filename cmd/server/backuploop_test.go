package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/backup/media"
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

// The shutdown guarantee is only as good as the grace period the deployment grants: the HTTP
// drain and the backup wait both have to finish inside it, or the supervisor's SIGKILL lands on
// a capsule mid-upload -- the exact outcome the wait exists to prevent. This holds the compose
// file and the two constants in step.
func TestComposeGracePeriodCoversTheShutdownBudget(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*stop_grace_period:\s*(\S+)\s*$`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("docker-compose.yml sets no stop_grace_period, so Docker's default 10s kills an in-flight deposit")
	}
	grace, err := time.ParseDuration(string(m[1]))
	if err != nil {
		t.Fatalf("stop_grace_period %q: %v", m[1], err)
	}
	if budget := shutdownTimeout + backupWaitTimeout; grace <= budget {
		t.Errorf("stop_grace_period %s does not cover shutdownTimeout+backupWaitTimeout (%s)", grace, budget)
	}
}

// Both wait phases share one context, not one timer channel: a timer channel delivers its value
// once, so a scheduler wait that consumed it would leave the handler wait unbounded -- the stuck
// deposit this whole path exists to bound. Neither channel here ever closes, so the only way out
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

func tickFixture(t *testing.T, every time.Duration) (store.Store, *config.Config) {
	t.Helper()
	st, err := store.Open(context.Background(), config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{}
	cfg.Backup.DepositInterval = every
	return st, cfg
}

func stubRun(t *testing.T, fn func() error) *int {
	t.Helper()
	var ran int
	old := runBackup
	t.Cleanup(func() { runBackup = old })
	runBackup = func(context.Context, *config.Config, recoveryclient.RunConfig, recoveryclient.Settings, recoveryclient.Depositor) (recoveryclient.Result, error) {
		ran++
		return recoveryclient.Result{}, fn()
	}
	return &ran
}

func TestBackupTickRunsWhenDue(t *testing.T) {
	st, cfg := tickFixture(t, time.Hour)
	ran := stubRun(t, func() error { return nil })
	backupTick(context.Background(), cfg, st, recoveryclient.RunConfig{}, nil)
	if *ran != 1 {
		t.Fatalf("ran %d times", *ran)
	}
}

func TestBackupTickSkipsWhenOff(t *testing.T) {
	st, cfg := tickFixture(t, 0)
	ran := stubRun(t, func() error { return nil })
	backupTick(context.Background(), cfg, st, recoveryclient.RunConfig{}, nil)
	if *ran != 0 {
		t.Fatalf("ran %d times", *ran)
	}
}

// A run that holds the library lock must not be stamped or audited: it stays due, so the
// next tick retries it.
func TestBackupTickInProgressLeavesItDue(t *testing.T) {
	st, cfg := tickFixture(t, time.Hour)
	stubRun(t, func() error { return recoveryclient.ErrInProgress })
	backupTick(context.Background(), cfg, st, recoveryclient.RunConfig{}, nil)
	recs, _, err := st.Audit().ListAuditRecords(context.Background(), 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Action == backupRunAction {
			t.Fatalf("in-progress run audited: %+v", r)
		}
	}
	// Still due: NextRun is not in the future because nothing stamped an attempt.
	next, on, err := recoveryclient.NextRun(time.Hour, backup.Settings(context.Background(), st.Settings()))
	if err != nil || !on || next.After(time.Now()) {
		t.Fatalf("not due: %v %v %v", next, on, err)
	}
}

// Nothing may start once shutdown has begun: the run uses a detached context, so a tick that
// raced the ticker would seal a full capsule nobody waits for.
func TestBackupTickDoesNothingAfterShutdown(t *testing.T) {
	st, cfg := tickFixture(t, time.Hour)
	ran := stubRun(t, func() error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	backupTick(ctx, cfg, st, recoveryclient.RunConfig{}, nil)
	if *ran != 0 {
		t.Fatalf("ran %d times after shutdown", *ran)
	}
	if _, err := st.Settings().GetSetting(context.Background(), "backup_last_attempt"); err == nil {
		t.Fatal("attempt stamped after shutdown")
	}
}

// deposit and backup-drill take no flags; the retired -messages must be refused, not ignored.
func TestCLIRefusesUnknownFlags(t *testing.T) {
	for _, name := range []string{"deposit", "backup-drill"} {
		for _, args := range [][]string{{"-messages"}, {"-bogus"}, {"extra"}} {
			if err := parseNoArgs(name, args, io.Discard); err == nil {
				t.Errorf("%s %v accepted", name, args)
			}
		}
		if err := parseNoArgs(name, nil, io.Discard); err != nil {
			t.Errorf("%s with no args: %v", name, err)
		}
	}
}

func stubMedia(t *testing.T, fn func() error) *int {
	t.Helper()
	var ran int
	old := runMedia
	t.Cleanup(func() { runMedia = old })
	runMedia = func(context.Context, *config.Config) (media.Result, error) {
		ran++
		return media.Result{Copied: 1}, fn()
	}
	return &ran
}

func latest(t *testing.T, st store.Store, action string) *store.AuditRecord {
	t.Helper()
	rec, err := st.Audit().LatestAuditRecord(context.Background(), action)
	if err != nil {
		t.Fatalf("no %s row: %v", action, err)
	}
	return rec
}

// Media runs after the capsule whatever the capsule's outcome, and is audited on its own row.
func TestBackupTickRunsMediaAfterTheCapsule(t *testing.T) {
	st, cfg := tickFixture(t, time.Hour)
	cfg.Matrix.ServerName = "example.com"
	stubRun(t, func() error { return errors.New("capsule failed") })
	ran := stubMedia(t, func() error { return nil })
	backupTick(context.Background(), cfg, st, recoveryclient.RunConfig{}, nil)
	if *ran != 1 {
		t.Fatalf("media ran %d times", *ran)
	}
	if !strings.HasPrefix(latest(t, st, mediaRunAction).Details, `outcome="success"`) ||
		!strings.HasPrefix(latest(t, st, backupRunAction).Details, `outcome="failure"`) {
		t.Fatal("capsule and media outcomes not recorded separately")
	}
}

func TestBackupTickMediaFailureLeavesTheCapsuleSuccess(t *testing.T) {
	st, cfg := tickFixture(t, time.Hour)
	cfg.Matrix.ServerName = "example.com"
	stubRun(t, func() error { return nil })
	stubMedia(t, func() error { return errors.New("disk full") })
	backupTick(context.Background(), cfg, st, recoveryclient.RunConfig{}, nil)
	if d := latest(t, st, mediaRunAction).Details; !strings.HasPrefix(d, `outcome="failure"`) || !strings.Contains(d, "disk full") {
		t.Errorf("media row %q", d)
	}
	if !strings.HasPrefix(latest(t, st, backupRunAction).Details, `outcome="success"`) {
		t.Error("a media failure changed the capsule outcome")
	}
}

func TestBackupTickSkipsMediaWithoutMatrixOrWhenBusy(t *testing.T) {
	st, cfg := tickFixture(t, time.Hour)
	stubRun(t, func() error { return nil })
	ran := stubMedia(t, func() error { return nil })
	backupTick(context.Background(), cfg, st, recoveryclient.RunConfig{}, nil)
	cfg.Matrix.ServerName = "example.com"
	for _, e := range []error{recoveryclient.ErrInProgress, recoveryclient.ErrNotPaired, recoveryclient.ErrNoDestination} {
		stubRun(t, func() error { return e })
		backupTick(context.Background(), cfg, st, recoveryclient.RunConfig{}, nil)
	}
	if *ran != 0 {
		t.Fatalf("media ran %d times without Matrix or beside an unsealed capsule run", *ran)
	}
}
