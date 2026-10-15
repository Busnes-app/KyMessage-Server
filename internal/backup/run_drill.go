package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Busnes-app/ky-primitives/capsule"
	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"golang.org/x/sys/unix"
)

// ErrBusy wraps recoveryclient.ErrInProgress, so run callers treat both alike.
var ErrBusy = fmt.Errorf("backup: another backup, export or drill is running: %w", recoveryclient.ErrInProgress)

// Lock takes <dataDir>/backup.lock without waiting, or returns ErrBusy. Every run, deposit,
// export and drill holds it from before Collect to the end, across processes: the lib's run
// lock is per process, and one payload in memory at a time is enough. unlock, or process exit,
// releases it. Never unlink the file: every process must lock the same inode.
func Lock(dataDir string) (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(dataDir, "backup.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}

// RunDrill opens payload in a private scratch root and runs checks. The caller holds Lock.
func RunDrill(ctx context.Context, cfg *config.Config, payload recoveryclient.Payload, checks func(string, capsule.Manifest) []recoveryclient.Check) (*recoveryclient.DrillResult, error) {
	root := DrillRoot(cfg)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(root, 0700); err != nil {
		return nil, err
	}
	return recoveryclient.Drill(ctx, root, payload, checks)
}
