package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
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

func tickFixture(t *testing.T, peopleEvery, messagesEvery time.Duration) (store.Store, []backupKind) {
	t.Helper()
	st, err := store.Open(context.Background(), config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, []backupKind{
		{name: "people", action: "admin.backup_run", settings: func(ctx context.Context) recoveryclient.Settings { return backup.Settings(ctx, st.Settings()) }, defaultEvery: peopleEvery},
		{name: "messages", action: backup.MessagesRunAction, settings: func(ctx context.Context) recoveryclient.Settings { return backup.MessagesSettings(ctx, st.Settings()) }, defaultEvery: messagesEvery},
	}
}

func stubRun(t *testing.T, fn func(kind string) error) *[]string {
	t.Helper()
	var ran []string
	old := runKind
	t.Cleanup(func() { runKind = old })
	runKind = func(_ context.Context, k backupKind, s recoveryclient.Settings, _ recoveryclient.Depositor) (recoveryclient.Result, error) {
		ran = append(ran, k.name)
		return recoveryclient.Result{}, fn(k.name)
	}
	return &ran
}

func TestBackupTickRunsDueKindsInOrder(t *testing.T) {
	st, kinds := tickFixture(t, time.Hour, time.Hour)
	ran := stubRun(t, func(string) error { return nil })
	backupTick(context.Background(), &config.Config{}, st, kinds, nil)
	if got := strings.Join(*ran, ","); got != "people,messages" {
		t.Fatalf("ran %q", got)
	}
}

func TestBackupTickSkipsAKindThatIsOff(t *testing.T) {
	st, kinds := tickFixture(t, time.Hour, 0)
	ran := stubRun(t, func(string) error { return nil })
	backupTick(context.Background(), &config.Config{}, st, kinds, nil)
	if got := strings.Join(*ran, ","); got != "people" {
		t.Fatalf("ran %q", got)
	}
}

// A run that holds the library lock must not starve the other kind, and must not be stamped
// or audited: the kind stays due, so the next tick retries it.
func TestBackupTickInProgressLeavesTheKindDue(t *testing.T) {
	st, kinds := tickFixture(t, time.Hour, time.Hour)
	ran := stubRun(t, func(k string) error {
		if k == "people" {
			return recoveryclient.ErrInProgress
		}
		return nil
	})
	backupTick(context.Background(), &config.Config{}, st, kinds, nil)
	if got := strings.Join(*ran, ","); got != "people,messages" {
		t.Fatalf("ran %q", got)
	}
	recs, _, err := st.Audit().ListAuditRecords(context.Background(), 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Action == "admin.backup_run" {
			t.Fatalf("in-progress run audited: %+v", r)
		}
	}
	// Still due: NextRun is not in the future because nothing stamped an attempt.
	next, on, err := recoveryclient.NextRun(time.Hour, kinds[0].settings(context.Background()))
	if err != nil || !on || next.After(time.Now()) {
		t.Fatalf("people not due: %v %v %v", next, on, err)
	}
}

// The shutdown wait is sized for one run: once the loop's context is cancelled during the
// first kind, the next kind must not start, and stays unstamped and due.
func TestBackupTickStopsBetweenKindsOnShutdown(t *testing.T) {
	st, kinds := tickFixture(t, time.Hour, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	ran := stubRun(t, func(string) error { cancel(); return nil })
	backupTick(ctx, &config.Config{}, st, kinds, nil)
	if got := strings.Join(*ran, ","); got != "people" {
		t.Fatalf("ran %q", got)
	}
	next, on, err := recoveryclient.NextRun(time.Hour, kinds[1].settings(context.Background()))
	if err != nil || !on || next.After(time.Now()) {
		t.Fatalf("messages not due: %v %v %v", next, on, err)
	}
}
