package backup

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Busnes-app/ky-primitives/keyfile"
	"github.com/Busnes-app/ky_server_base/internal/backup/media"
	"github.com/Busnes-app/ky_server_base/internal/config"
)

// ErrNoMediaDir is a Matrix deployment with nowhere to mirror media.
var ErrNoMediaDir = errors.New("backup: Matrix media needs KY_BACKUP_DIR")

// RunMedia mirrors Synapse's media into <KY_BACKUP_DIR>/media under the media key, which
// Collect creates and seals: the mirror opens once a capsule holding the key exists.
func RunMedia(ctx context.Context, cfg *config.Config, now time.Time) (media.Result, error) {
	if cfg.Backup.Dir == "" {
		return media.Result{}, ErrNoMediaDir
	}
	key, err := keyfile.Load(MediaKeyPath(cfg.Database.DataDir), 32)
	if err != nil {
		return media.Result{}, fmt.Errorf("backup: media key (a capsule run creates it): %w", err)
	}
	defer clear(key)
	return media.Run(ctx, cfg.Matrix.MediaDir, filepath.Join(cfg.Backup.Dir, "media"), key, cfg.Backup.MediaFullKeep, now)
}
