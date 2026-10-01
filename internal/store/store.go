package store

import (
	"context"
	"errors"
)

var (
	ErrNotFound       = errors.New("record not found")
	ErrAlreadyExists  = errors.New("record already exists")
	ErrSessionExpired = errors.New("session expired")
	ErrPairingExpired = errors.New("pairing session expired")
)

// Store defines the unified storage contract implemented across SQLite, PostgreSQL, and MySQL.
type Store interface {
	Users() UserStore
	Sessions() SessionStore
	Devices() DeviceStore
	Groups() GroupStore
	Audit() AuditStore
	Settings() SettingsStore

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
type UserFilter struct {
	Field UserField
	Value string
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
	// DirectoryStatuses maps each subject of provider to its user's status, or to "deleted"
	// when the directory deleted it (an order row with no user).
	DirectoryStatuses(ctx context.Context, provider string) (map[string]string, error)
	ResetAdminPassword(ctx context.Context, userID, newHash string) error
	CompletePasswordChange(ctx context.Context, userID, oldHash, newHash, ip string) error
	UpdateRecoveryCodes(ctx context.Context, userID, oldHashes, newHashes string) error
	// SpendTOTPCounter records counter as used. It returns ErrAlreadyExists when counter is
	// not greater than the stored one, which is how a replayed code inside the skew window fails.
	SpendTOTPCounter(ctx context.Context, userID string, counter int64) error
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
	CreateMFAChallenge(ctx context.Context, challenge *MFAChallenge, expectedPasswordHash string) error
	ConsumeMFAChallenge(ctx context.Context, tokenHash string) (userID, passwordHash string, err error)
}

// DeviceStore handles 90s ephemeral QR pairing sessions and paired push clients.
type DeviceStore interface {
	CreatePairing(ctx context.Context, p *DevicePairing) error
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
	ListAuditRecords(ctx context.Context, offset, limit int) ([]*AuditRecord, int, error)
	LatestAuditRecord(ctx context.Context, action string) (*AuditRecord, error)
}

// SettingsStore handles persistent key-value configuration.
type SettingsStore interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, val string) error
	DeleteSetting(ctx context.Context, key string) error
	GetAllSettings(ctx context.Context) (map[string]string, error)
}
