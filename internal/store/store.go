package store

import (
	"context"
	"errors"
	"fmt"
	"github.com/Busnes-app/ky-primitives/applysetup"
)

var (
	ErrNotFound      = errors.New("record not found")
	ErrAlreadyExists = errors.New("record already exists")
	// ErrUsernameTaken: another account holds the username (exact case, as the column is unique).
	ErrUsernameTaken  = fmt.Errorf("username is taken: %w", ErrAlreadyExists)
	ErrSessionExpired = errors.New("session expired")
	ErrPairingExpired = errors.New("pairing session expired")
)

// Store defines the unified storage contract implemented across SQLite, PostgreSQL, and MySQL.
type Store interface {
	ApplyInstaller(context.Context, applysetup.Common, bool) (applysetup.Report, error)
	InstallerIdentity(context.Context, string, string) (bool, bool, error)
	InstallerUserAllowed(context.Context, *User) (bool, error)
	ApplyInstallerDirectory(context.Context, string, string, string, string, string, bool, bool) error

	Users() UserStore
	Sessions() SessionStore
	Devices() DeviceStore
	Groups() GroupStore
	Audit() AuditStore
	Settings() SettingsStore
	Agents() AgentStore

	InvalidateRestoredGrants(context.Context) error
	Driver() string
	Ping(ctx context.Context) error
	Close() error
}

// DirectoryEvent identifies one signed directory delivery: the sender's event ID and the
// per-user revision it carries.
type DirectoryEvent struct {
	ID       string
	Revision int64
}

// UserFilter selects users by one exact attribute; the zero value matches everyone.
// Provider, when set, also limits the result to that sso_provider.
type UserFilter struct {
	Field    UserField
	Value    string
	Provider string
}

type UserField int

const (
	UserFieldNone     UserField = iota
	UserFieldUsername           // case-insensitive
	UserFieldEmail              // case-insensitive
	UserFieldSubject            // exact sso_subject (SCIM externalId)
)

// UserStore defines repository operations for accounts.
type UserStore interface {
	CreateUser(ctx context.Context, u *User) error
	GetUserByID(ctx context.Context, id string) (*User, error)
	GetLocalUserByUsername(ctx context.Context, username string) (*User, error)
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	GetUserBySSO(ctx context.Context, provider, subject string) (*User, error)
	UpdateUser(ctx context.Context, u *User) error
	// UpdateProfile writes username, email, display name, role and status only.
	UpdateProfile(ctx context.Context, u *User) error
	// Directory updates are ordered per SSO provider and subject by a record that outlives
	// the user. Each event ID is used once, whether or not it applied; an event applies only
	// when its revision is newer than the last applied one or is -1 (the sender's
	// post-restore resend), atomically with its write, and reports whether it applied.
	ApplyDirectoryProfile(ctx context.Context, u *User, ev DirectoryEvent) (bool, error)
	CreateDirectoryUser(ctx context.Context, u *User, ev DirectoryEvent) (bool, error)
	DeleteDirectoryUser(ctx context.Context, u *User, ev DirectoryEvent) (bool, error)
	// RecordAdoptedStatus records, in the same order, the provider's status (u.Status) for a
	// subject whose person is a SCIM row sign-in adopts; DirectoryStatuses joins it to SCIM's.
	RecordAdoptedStatus(ctx context.Context, u *User, ev DirectoryEvent) (bool, error)
	// DirectoryStatuses maps each subject of provider to its user's status, or to "deleted"
	// when the directory deleted it (an order row with no user and no adopted status). Of
	// several rows for one subject, a non-active status wins. With adoptSCIM (SCIM on) it also
	// maps the SCIM rows a sign-in of provider would adopt (externalId = subject, no provider
	// row or deletion record): the same person, so Matrix must know them too, with SCIM's
	// status (ky-server-base Q1 A) and the provider's recorded adopted status, if any.
	DirectoryStatuses(ctx context.Context, provider string, adoptSCIM bool) (map[string]string, error)
	// ResetAdminPassword is init-admin's recovery of a local account: a forced password
	// replacement, active and admin. It is ErrNotFound for a non-admin unless promote is set.
	ResetAdminPassword(ctx context.Context, userID, newHash string, promote bool) error
	CompletePasswordChange(ctx context.Context, userID, oldHash, newHash, ip string) error
	// ApplySignInRole changes u, a KyIdentity row that is not installer-owned, from u.Role to
	// role, deletes its grants and replaces its password_hash fence (it holds no password),
	// in one transaction, so grants issued on the old snapshot fail; u takes the new values.
	// It is ErrNotFound when the row no longer matches (another writer got there first).
	ApplySignInRole(ctx context.Context, u *User, role string) error
	DeleteUser(ctx context.Context, id string) error
	ListUsers(ctx context.Context, offset, limit int, filter UserFilter) ([]*User, int, error)
	CountUsers(ctx context.Context) (int, error)
}

// SessionStore defines repository operations for active login sessions.
type SessionStore interface {
	CreateSession(ctx context.Context, s *Session, expectedPasswordHash string) error
	GetSession(ctx context.Context, tokenHash string) (*Session, error)
	DeleteSession(ctx context.Context, tokenHash string) error
	DeleteUserSessions(ctx context.Context, userID string) error
	CleanExpiredSessions(ctx context.Context) error
}

// DeviceStore handles 90s ephemeral QR pairing sessions and paired push clients.
type DeviceStore interface {
	CreatePairing(ctx context.Context, p *DevicePairing, expectedPasswordHash string) error
	GetPairingBySecret(ctx context.Context, secret string) (*DevicePairing, error)
	ConsumePairing(ctx context.Context, secret, deviceName, platform, pushToken string) error
	CleanExpiredPairings(ctx context.Context) error
}

// GroupStore defines repository operations for SCIM and RBAC groups.
type GroupStore interface {
	CreateGroup(ctx context.Context, g *Group) error
	GetGroupByID(ctx context.Context, id string) (*Group, error)
	GetGroupByName(ctx context.Context, name string) (*Group, error)
	UpdateGroup(ctx context.Context, g *Group) error
	DeleteGroup(ctx context.Context, id string) error
	ListGroups(ctx context.Context, offset, limit int) ([]*Group, int, error)
	AddGroupMember(ctx context.Context, groupID, userID string) error
	RemoveGroupMember(ctx context.Context, groupID, userID string) error
	GetUserGroups(ctx context.Context, userID string) ([]*Group, error)
}

// AuditStore logs security events.
type AuditStore interface {
	LogAudit(ctx context.Context, r *AuditRecord) error
	// ListAuditRecords pages rows newest first by insertion order. With prefixes, only rows
	// whose action starts with one of them; the count is of those rows.
	ListAuditRecords(ctx context.Context, offset, limit int, prefixes ...string) ([]*AuditRecord, int, error)
	LatestAuditRecord(ctx context.Context, action string) (*AuditRecord, error)
}

// SettingsStore handles persistent key-value configuration.
type SettingsStore interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, val string) error
	DeleteSetting(ctx context.Context, key string) error
	GetAllSettings(ctx context.Context) (map[string]string, error)
}

// AgentStore is the registry of chat agent accounts.
type AgentStore interface {
	// CreateAgent returns ErrAlreadyExists when the username, MAS user or device is taken.
	CreateAgent(ctx context.Context, a *Agent) error
	GetAgent(ctx context.Context, id string) (*Agent, error)
	// ListAgents returns every agent, oldest first.
	ListAgents(ctx context.Context) ([]*Agent, error)
	// SetAgentToken records the agent's device and current session: a session ID with
	// AgentTokenActive, or "" with AgentTokenRevoked or AgentTokenExpired. It is ErrNotFound
	// unless the agent still holds fromSession, so a concurrent change is never overwritten.
	SetAgentToken(ctx context.Context, id, fromSession, deviceID, sessionID, state string) error
	// ActivateNewAgent gives a just-created agent (no session, AgentTokenExpired) its first
	// session. It is ErrNotFound once anything else changed it: a revoke during the mint wins.
	ActivateNewAgent(ctx context.Context, id, sessionID string) error
	DeleteAgent(ctx context.Context, id string) error
}
