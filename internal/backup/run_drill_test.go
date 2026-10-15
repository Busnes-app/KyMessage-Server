package backup_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/backup"
)

// A separate process holds backup.lock, as a cron `kymessages deposit` would while the server's
// scheduler ticks. Killing it proves the kernel releases ownership without stale-lock recovery.
func TestBackupLockProcess(t *testing.T) {
	if os.Getenv("KY_BACKUP_LOCK_HELPER") != "1" {
		return
	}
	unlock, err := backup.Lock(os.Getenv("KY_BACKUP_LOCK_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	fmt.Println("locked")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func TestLockSerializesProcessesAndReleasesAfterExit(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBackupLockProcess$")
	cmd.Env = append(os.Environ(), "KY_BACKUP_LOCK_HELPER=1", "KY_BACKUP_LOCK_DIR="+dir)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("helper did not acquire lock: %q %v", line, err)
	}
	if _, err := backup.Lock(dir); !errors.Is(err, backup.ErrBusy) || !errors.Is(err, recoveryclient.ErrInProgress) {
		t.Fatalf("concurrent process accepted: %v", err)
	}
	unlock, err := backup.Lock(t.TempDir())
	if err != nil {
		t.Fatalf("independent data dir blocked: %v", err)
	}
	unlock()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	unlock, err = backup.Lock(dir)
	if err != nil {
		t.Fatalf("lock survived process exit: %v", err)
	}
	// In one process too: the HTTP routes and the scheduler share a data directory.
	if _, err := backup.Lock(dir); !errors.Is(err, backup.ErrBusy) {
		t.Fatalf("second holder in the same process accepted: %v", err)
	}
	unlock()
	unlock, err = backup.Lock(dir)
	if err != nil {
		t.Fatalf("lock survived unlock: %v", err)
	}
	unlock()
}

func TestRunDrillKeepsItsScratchPrivateAndClean(t *testing.T) {
	t.Setenv("KY_PORT", "8080")
	t.Setenv("KY_DB_DRIVER", "sqlite")
	cfg, _ := payloadConfig(t)
	payload, err := backup.Collect(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	root := backup.DrillRoot(cfg)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	// A permissive existing root is repaired before the library opens any plaintext.
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	result, err := backup.RunDrill(context.Background(), cfg, payload, backup.Checks)
	if err != nil || !result.Passed {
		t.Fatalf("drill: %+v %v", result, err)
	}
	info, err := os.Stat(root)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("scratch root not private: %v %v", info, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("scratch left behind: %v %v", entries, err)
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := backup.RunDrill(canceled, cfg, payload, backup.Checks); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled drill: %v", err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("canceled drill left scratch: %v %v", entries, err)
	}
}
