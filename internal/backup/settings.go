package backup

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

const recoveryTokenLabel = "ky_server_base:setting:kyrecovery_token"

type settingsAdapter struct {
	ctx context.Context
	s   store.SettingsStore
}

// Settings binds the request context to the settings store for one call into the lib.
func Settings(ctx context.Context, s store.SettingsStore) recoveryclient.Settings {
	return settingsAdapter{ctx: ctx, s: s}
}

func (a settingsAdapter) Get(key string) (string, error) {
	v, err := a.s.GetSetting(a.ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return "", recoveryclient.ErrNotFound
	}
	return v, err
}
func (a settingsAdapter) Set(key, val string) error { return a.s.SetSetting(a.ctx, key, val) }
func (a settingsAdapter) Delete(key string) error   { return a.s.DeleteSetting(a.ctx, key) }

// NewSealer seals the KyRecovery token under the deployment key, domain-separated so a row
// copied from another setting will not open.
func NewSealer(cfg *config.Config) (recoveryclient.Sealer, error) {
	return recoveryclient.NewAESGCMSealer(cfg.Security.EncryptionKey, recoveryTokenLabel)
}

// RunConfig is what Run needs from the scaffold's configuration.
func RunConfig(cfg *config.Config, appVersion string) (recoveryclient.RunConfig, error) {
	sealer, err := NewSealer(cfg)
	if err != nil {
		return recoveryclient.RunConfig{}, err
	}
	return recoveryclient.RunConfig{
		DataDir: cfg.Database.DataDir, AppName: cfg.Server.AppName, AppVersion: appVersion,
		BackupDir: cfg.Backup.Dir, Keep: cfg.Backup.Keep, Sealer: sealer,
	}, nil
}

// MessagesRunAction is the audit action for a messages-capsule run.
const MessagesRunAction = "admin.backup_run_messages"

// The messages capsule has its own schedule, last attempt and receipt; the pairing, token
// and key pin are shared with the people capsule.
var messagesOwnKeys = map[string]bool{"backup_interval_sec": true, "backup_last_attempt": true, "kyrecovery_last_deposit": true}

type messagesSettings struct{ recoveryclient.Settings }

func (m messagesSettings) key(k string) string {
	if messagesOwnKeys[k] {
		return "messages_" + k
	}
	return k
}
func (m messagesSettings) Get(k string) (string, error) { return m.Settings.Get(m.key(k)) }
func (m messagesSettings) Set(k, v string) error        { return m.Settings.Set(m.key(k), v) }
func (m messagesSettings) Delete(k string) error        { return m.Settings.Delete(m.key(k)) }

// MessagesSettings is Settings with the messages capsule's own schedule and receipt keys.
func MessagesSettings(ctx context.Context, s store.SettingsStore) recoveryclient.Settings {
	return messagesSettings{Settings(ctx, s)}
}

// MessagesRunConfig is RunConfig with local copies in their own subdirectory: the library
// prunes by app prefix with one keep count, so sharing a directory would evict people copies.
func MessagesRunConfig(cfg *config.Config, appVersion string) (recoveryclient.RunConfig, error) {
	rc, err := RunConfig(cfg, appVersion)
	if err == nil && rc.BackupDir != "" {
		rc.BackupDir = filepath.Join(rc.BackupDir, "messages")
	}
	return rc, err
}
