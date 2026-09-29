package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Migration represents an incremental schema change step.
type Migration struct {
	Version  int
	Name     string
	SQLite   string
	Postgres string
}

var registry = []Migration{
	{
		Version: 1,
		Name:    "initial_schema",
		SQLite: `
CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    email TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT 'user',
    status TEXT NOT NULL DEFAULT 'active',
    sso_provider TEXT NOT NULL DEFAULT 'local',
    sso_subject TEXT NOT NULL DEFAULT '',
    totp_secret_enc TEXT NOT NULL DEFAULT '',
    totp_enabled INTEGER NOT NULL DEFAULT 0,
    recovery_codes_hash TEXT NOT NULL DEFAULT '[]',
    push_device_id TEXT NOT NULL DEFAULT '',
    must_change_password INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    last_login_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
CREATE INDEX IF NOT EXISTS idx_users_sso ON users(sso_provider, sso_subject);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_agent TEXT NOT NULL DEFAULT '',
    ip_address TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    expires_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS device_pairings (
    secret TEXT PRIMARY KEY,
    code TEXT NOT NULL UNIQUE,
    user_id TEXT NOT NULL DEFAULT '',
    device_name TEXT NOT NULL DEFAULT '',
    platform TEXT NOT NULL DEFAULT '',
    push_token TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    created_at DATETIME NOT NULL,
    expires_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_pairings_code ON device_pairings(code);
CREATE INDEX IF NOT EXISTS idx_pairings_expires ON device_pairings(expires_at);

CREATE TABLE IF NOT EXISTS groups (
    id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL UNIQUE,
    external_id TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS group_members (
    group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, user_id)
);

CREATE TABLE IF NOT EXISTS audit_records (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    resource TEXT NOT NULL DEFAULT '',
    details TEXT NOT NULL DEFAULT '',
    ip_address TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_user_created ON audit_records(user_id, created_at);

CREATE TABLE IF NOT EXISTS server_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT '',
    updated_at DATETIME NOT NULL
);
`,
		Postgres: `
CREATE TABLE IF NOT EXISTS users (
    id VARCHAR(64) PRIMARY KEY,
    username VARCHAR(128) NOT NULL UNIQUE,
    email VARCHAR(255) NOT NULL DEFAULT '',
    display_name VARCHAR(255) NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL DEFAULT '',
    role VARCHAR(32) NOT NULL DEFAULT 'user',
    status VARCHAR(32) NOT NULL DEFAULT 'active',
    sso_provider VARCHAR(32) NOT NULL DEFAULT 'local',
    sso_subject VARCHAR(255) NOT NULL DEFAULT '',
    totp_secret_enc TEXT NOT NULL DEFAULT '',
    totp_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    recovery_codes_hash TEXT NOT NULL DEFAULT '[]',
    push_device_id VARCHAR(255) NOT NULL DEFAULT '',
    must_change_password BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    last_login_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
CREATE INDEX IF NOT EXISTS idx_users_sso ON users(sso_provider, sso_subject);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash VARCHAR(64) PRIMARY KEY,
    user_id VARCHAR(64) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_agent TEXT NOT NULL DEFAULT '',
    ip_address VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS device_pairings (
    secret VARCHAR(64) PRIMARY KEY,
    code VARCHAR(16) NOT NULL UNIQUE,
    user_id VARCHAR(64) NOT NULL DEFAULT '',
    device_name VARCHAR(255) NOT NULL DEFAULT '',
    platform VARCHAR(32) NOT NULL DEFAULT '',
    push_token TEXT NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_pairings_code ON device_pairings(code);
CREATE INDEX IF NOT EXISTS idx_pairings_expires ON device_pairings(expires_at);

CREATE TABLE IF NOT EXISTS groups (
    id VARCHAR(64) PRIMARY KEY,
    display_name VARCHAR(255) NOT NULL UNIQUE,
    external_id VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS group_members (
    group_id VARCHAR(64) NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    user_id VARCHAR(64) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, user_id)
);

CREATE TABLE IF NOT EXISTS audit_records (
    id BIGSERIAL PRIMARY KEY,
    user_id VARCHAR(64) NOT NULL DEFAULT '',
    action VARCHAR(64) NOT NULL,
    resource VARCHAR(255) NOT NULL DEFAULT '',
    details TEXT NOT NULL DEFAULT '',
    ip_address VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_user_created ON audit_records(user_id, created_at);

CREATE TABLE IF NOT EXISTS server_settings (
    key VARCHAR(128) PRIMARY KEY,
    value TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL
);
`,
	},
	{
		Version: 2,
		Name:    "one_time_auth_challenges",
		SQLite: `
CREATE TABLE IF NOT EXISTS mfa_challenges (
    token_hash TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mfa_challenges_expires ON mfa_challenges(expires_at);
`,
		Postgres: `
CREATE TABLE IF NOT EXISTS mfa_challenges (
    token_hash VARCHAR(64) PRIMARY KEY,
    user_id VARCHAR(64) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mfa_challenges_expires ON mfa_challenges(expires_at);
`,
	},
	{
		Version:  3,
		Name:     "totp_last_counter",
		SQLite:   `ALTER TABLE users ADD COLUMN totp_last_counter INTEGER NOT NULL DEFAULT 0;`,
		Postgres: `ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_last_counter BIGINT NOT NULL DEFAULT 0;`,
	},
	{
		Version: 4,
		Name:    "mfa_credential_snapshot",
		SQLite: `DELETE FROM mfa_challenges;
ALTER TABLE mfa_challenges ADD COLUMN password_hash TEXT NOT NULL DEFAULT '';`,
		Postgres: `DELETE FROM mfa_challenges;
ALTER TABLE mfa_challenges ADD COLUMN password_hash TEXT NOT NULL DEFAULT '';`,
	},
	{Version: 5, Name: "messaging_devices_and_rooms", SQLite: messagingSchema, Postgres: messagingSchema},
	{Version: 6, Name: "messaging_delivery", SQLite: messagingDeliverySchema, Postgres: messagingDeliverySchema},
	{Version: 7, Name: "messaging_key_packages", SQLite: messagingKeyPackageSchema, Postgres: messagingKeyPackageSchema},
	{Version: 8, Name: "messaging_recovery_auth", SQLite: messagingRecoveryAuthSchema, Postgres: messagingRecoveryAuthSchema},
	{Version: 9, Name: "messaging_identity_generations", SQLite: messagingIdentitySchema, Postgres: messagingIdentitySchema},
	{Version: 10, Name: "messaging_scoped_packages", SQLite: messagingScopedPackageSchema, Postgres: messagingScopedPackageSchema},
	{Version: 11, Name: "messaging_direct_rooms", SQLite: messagingDirectRoomSchema, Postgres: messagingDirectRoomSchema},
	{Version: 12, Name: "messaging_retention", SQLite: messagingRetentionSchema, Postgres: messagingRetentionSchema},
	{Version: 13, Name: "audit_latest_action", SQLite: `CREATE INDEX idx_audit_action_id ON audit_records(action, id);`, Postgres: `CREATE INDEX idx_audit_action_id ON audit_records(action, id);`},
	{Version: 14, Name: "pairing_secret_only", SQLite: pairingSecretOnlySQLite, Postgres: pairingSecretOnlyPostgres},
	// The newest directory-webhook update applied per subject. It is not a users column: it
	// must survive deletion, or a replayed older update could recreate the account.
	{Version: 15, Name: "directory_sync_order", SQLite: directorySyncStateSchema, Postgres: directorySyncStateSchema},
	// KyIdentity orders directory updates by a per-user revision, not a timestamp. Stored
	// timestamps would outrank every revision, so they are discarded. directory_sync_resets
	// remembers every applied post-restore (-1) event, which resets the order, so none applies twice.
	{Version: 16, Name: "directory_sync_revision", SQLite: directorySyncRevision, Postgres: directorySyncRevision},
}

// Run executes all pending migrations for the specified database driver.
func Run(ctx context.Context, db *sql.DB, driver string) error {
	driver = strings.ToLower(driver)
	if driver == "postgresql" {
		driver = "postgres"
	}

	// Create schema_migrations table
	initTableQuery := `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at DATETIME NOT NULL
);`
	if driver == "postgres" {
		initTableQuery = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL
);`
	}

	if _, err := db.ExecContext(ctx, initTableQuery); err != nil {
		return fmt.Errorf("failed to init schema_migrations: %w", err)
	}

	for _, m := range registry {
		var exists int
		err := db.QueryRowContext(ctx, "SELECT COUNT(1) FROM schema_migrations WHERE version = $1", m.Version).Scan(&exists)
		if err != nil {
			// Try SQLite positional ? parameter if $1 failed
			err = db.QueryRowContext(ctx, "SELECT COUNT(1) FROM schema_migrations WHERE version = ?", m.Version).Scan(&exists)
			if err != nil {
				return fmt.Errorf("failed to check migration version %d: %w", m.Version, err)
			}
		}

		if exists > 0 {
			continue
		}

		ddl := m.SQLite
		if driver == "postgres" {
			ddl = m.Postgres
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to begin migration tx for v%d: %w", m.Version, err)
		}

		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed executing migration v%d (%s): %w", m.Version, m.Name, err)
		}

		recordQuery := "INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)"
		if driver == "postgres" {
			recordQuery = "INSERT INTO schema_migrations (version, name, applied_at) VALUES ($1, $2, $3)"
		}

		if _, err := tx.ExecContext(ctx, recordQuery, m.Version, m.Name, time.Now().UTC()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to record migration v%d: %w", m.Version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit migration v%d: %w", m.Version, err)
		}
	}

	return nil
}

// Pairings live 90 seconds, so dropping pending rows on upgrade loses nothing. The anonymous
// verify route no longer accepts the guessable 6-digit code; only the 24-byte QR secret.
const pairingSecretOnlySQLite = `
DROP TABLE IF EXISTS device_pairings;
CREATE TABLE device_pairings (
    secret TEXT PRIMARY KEY,
    user_id TEXT NOT NULL DEFAULT '',
    device_name TEXT NOT NULL DEFAULT '',
    platform TEXT NOT NULL DEFAULT '',
    push_token TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    created_at DATETIME NOT NULL,
    expires_at DATETIME NOT NULL,
    authenticated_at DATETIME NOT NULL
);
CREATE INDEX idx_pairings_expires ON device_pairings(expires_at);
`

const pairingSecretOnlyPostgres = `
DROP TABLE IF EXISTS device_pairings;
CREATE TABLE device_pairings (
    secret VARCHAR(64) PRIMARY KEY,
    user_id VARCHAR(64) NOT NULL DEFAULT '',
    device_name VARCHAR(255) NOT NULL DEFAULT '',
    platform VARCHAR(32) NOT NULL DEFAULT '',
    push_token TEXT NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    authenticated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_pairings_expires ON device_pairings(expires_at);
`

const directorySyncStateSchema = `
CREATE TABLE directory_sync_state (
    provider VARCHAR(32) NOT NULL,
    subject VARCHAR(255) NOT NULL,
    synced_at BIGINT NOT NULL,
    PRIMARY KEY (provider, subject)
);
`

const directorySyncRevision = `
DELETE FROM directory_sync_state;
ALTER TABLE directory_sync_state RENAME COLUMN synced_at TO revision;
CREATE TABLE directory_sync_resets (
    provider VARCHAR(32) NOT NULL,
    event_id VARCHAR(255) NOT NULL,
    PRIMARY KEY (provider, event_id)
);
`
